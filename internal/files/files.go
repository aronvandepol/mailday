// Package files finds files to attach by words, the way a launcher does:
// "conf abstract" finds ~/Documents/1_projects/KJG26/abstract_conf.pdf.
//
// Each word must appear somewhere in the path, in any order, ignoring case.
// The search runs on what the system already provides, so it stays fast
// across a home folder of a million files: fd where installed, else Spotlight
// (mdfind) on macOS, else a time-limited directory walk. The longest word
// narrows the search, and the other words filter what comes back.
package files

import (
	"bufio"
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Hit is one file that matches every word.
type Hit struct {
	Path    string
	Size    int64
	ModTime time.Time
	score   float64
}

// skipped folders hold dependencies, environments and caches, never things
// anyone mails.
var skipped = map[string]bool{
	"node_modules": true, ".git": true, ".venv": true, "venv": true, "__pycache__": true,
	"Library": true, ".cache": true, ".Trash": true, "site-packages": true, ".mypy_cache": true,
}

// candidateCap bounds how many paths the narrowing word may return before
// filtering, so a very common word cannot stall typing.
const candidateCap = 20000

// Roots are the folders searched below home. Documents comes first and its
// files rank above the others; Downloads and Desktop are there for the file
// that just arrived.
func Roots(home string) []string {
	var roots []string
	for _, name := range []string{"Documents", "Downloads", "Desktop"} {
		path := filepath.Join(home, name)
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			roots = append(roots, path)
		}
	}
	return roots
}

// Search returns up to limit files under home whose path contains every word
// of query, best first: words in the file name beat words in folder names,
// and recently changed files beat old ones.
func Search(ctx context.Context, home, query string, limit int) []Hit {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return nil
	}
	narrow := words[0]
	for _, word := range words {
		if len([]rune(word)) > len([]rune(narrow)) {
			narrow = word
		}
	}
	roots := Roots(home)
	var paths []string
	backend := os.Getenv("MAILDAY_FILE_SEARCH") // "walk" forces the fallback
	switch {
	case backend == "walk":
		paths = walk(ctx, roots, narrow)
	case lookPath("fd") != "" || lookPath("fdfind") != "":
		tool := lookPath("fd")
		if tool == "" {
			tool = lookPath("fdfind")
		}
		args := []string{"--type", "f", "--no-ignore", "--full-path", "--ignore-case", "--absolute-path", "--fixed-strings",
			"--max-results", "20000"}
		for name := range skipped {
			args = append(args, "--exclude", name)
		}
		args = append(append(args, narrow), roots...)
		paths = runLines(ctx, tool, args...)
	case runtime.GOOS == "darwin" && lookPath("mdfind") != "":
		for _, root := range roots {
			paths = append(paths, runLines(ctx, "mdfind", "-onlyin", root, "-name", narrow)...)
		}
	default:
		paths = walk(ctx, roots, narrow)
	}
	hits := rank(paths, words, home, limit)
	// Spotlight drops files while it rebuilds its index, so an empty answer
	// from it is checked against the disk itself.
	if len(hits) == 0 && backend != "walk" && runtime.GOOS == "darwin" && lookPath("fd") == "" && lookPath("fdfind") == "" {
		hits = rank(walk(ctx, roots, narrow), words, home, limit)
	}
	return hits
}

// inDocuments reports whether path lies in ~/Documents.
func inDocuments(path, home string) bool {
	return strings.HasPrefix(path, filepath.Join(home, "Documents")+string(filepath.Separator))
}

func lookPath(name string) string {
	path, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return path
}

func runLines(ctx context.Context, name string, args ...string) []string {
	command := exec.CommandContext(ctx, name, args...)
	command.Stderr = nil
	output, err := command.StdoutPipe()
	if err != nil {
		return nil
	}
	if err := command.Start(); err != nil {
		return nil
	}
	var lines []string
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		if len(lines) < candidateCap {
			lines = append(lines, scanner.Text())
		}
	}
	_ = command.Wait()
	return lines
}

// walk is the fallback: a directory walk that stops at the context deadline.
func walk(ctx context.Context, roots []string, narrow string) []string {
	var paths []string
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if ctx.Err() != nil || len(paths) >= candidateCap {
				return filepath.SkipAll
			}
			if err != nil {
				return nil
			}
			name := entry.Name()
			if entry.IsDir() {
				if path != root && (skipped[name] || strings.HasPrefix(name, ".")) {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.Contains(strings.ToLower(path), narrow) {
				paths = append(paths, path)
			}
			return nil
		})
	}
	return paths
}

func inRoots(path, home string) bool {
	for _, root := range Roots(home) {
		if strings.HasPrefix(path, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func excluded(path, home string) bool {
	relative, err := filepath.Rel(home, path)
	if err != nil || strings.HasPrefix(relative, "..") {
		return true
	}
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if skipped[part] || strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}

func rank(paths []string, words []string, home string, limit int) []Hit {
	now := time.Now()
	seen := map[string]bool{}
	var hits []Hit
	for _, path := range paths {
		if seen[path] || excluded(path, home) || !inRoots(path, home) {
			continue
		}
		seen[path] = true
		lower := strings.ToLower(path)
		base := strings.ToLower(filepath.Base(path))
		score, ok := 0.0, true
		for _, word := range words {
			switch {
			case strings.Contains(base, word):
				score += 3
				if strings.HasPrefix(base, word) {
					score++
				}
			case strings.Contains(lower, word):
				score++
			default:
				ok = false
			}
			if !ok {
				break
			}
		}
		if !ok {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		// Recent files float up: up to +2 for today, fading over a year.
		age := now.Sub(info.ModTime()).Hours() / 24
		score += 2 * max(0, 1-age/365)
		// Shallow paths are usually the intended ones (abstract.pdf over a
		// copy deep inside a build folder).
		score -= 0.05 * float64(strings.Count(path, string(filepath.Separator)))
		if inDocuments(path, home) {
			score += 1.5
		}
		hits = append(hits, Hit{Path: path, Size: info.Size(), ModTime: info.ModTime(), score: score})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].ModTime.After(hits[j].ModTime)
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}
