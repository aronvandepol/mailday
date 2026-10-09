package tui

// Crash capture. Bubble Tea catches a panic, restores the terminal and prints
// the stack to stderr, but when Mailday runs in its own window that window
// closes at once and the report is gone. So stderr is copied while Mailday
// runs, and a panic is written to ~/.local/state/mailday/crash-*.log together
// with the last status messages, and the window waits for Enter.

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
)

var crashRecent struct {
	sync.Mutex
	lines []string
}

// rememberForCrash keeps the last status messages for a crash report.
func rememberForCrash(at time.Time, text string) {
	crashRecent.Lock()
	defer crashRecent.Unlock()
	crashRecent.lines = append(crashRecent.lines, at.Format("15:04:05")+"  "+text)
	if len(crashRecent.lines) > statusLogSize {
		crashRecent.lines = crashRecent.lines[len(crashRecent.lines)-statusLogSize:]
	}
}

func crashDir() string {
	if state := os.Getenv("XDG_STATE_HOME"); state != "" {
		return filepath.Join(state, "mailday")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "mailday")
}

// captureStderr copies stderr into a bounded buffer while still showing it.
// stop restores stderr and returns what was written.
func captureStderr() (stop func() []byte) {
	original := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		return func() []byte { return nil }
	}
	os.Stderr = writer
	var buffer bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		chunk := make([]byte, 32*1024)
		for {
			n, err := reader.Read(chunk)
			if n > 0 {
				_, _ = original.Write(chunk[:n])
				if buffer.Len() < 1<<20 {
					buffer.Write(chunk[:n])
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return func() []byte {
		os.Stderr = original
		writer.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
		reader.Close()
		return buffer.Bytes()
	}
}

// writeCrashReport saves a report and returns its path.
func writeCrashReport(runErr error, stderr []byte, now time.Time) (string, error) {
	dir := crashDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "crash-"+now.Format("20060102-150405")+".log")
	var report strings.Builder
	fmt.Fprintf(&report, "Mailday crash %s\n%s/%s %s\nerror: %v\n\n", now.Format(time.RFC3339), runtime.GOOS, runtime.GOARCH, runtime.Version(), runErr)
	crashRecent.Lock()
	if len(crashRecent.lines) > 0 {
		report.WriteString("Last status messages:\n")
		for _, line := range crashRecent.lines {
			report.WriteString("  " + line + "\n")
		}
		report.WriteString("\n")
	}
	crashRecent.Unlock()
	report.WriteString(strings.ReplaceAll(string(stderr), "\r\n", "\n"))
	return path, os.WriteFile(path, []byte(report.String()), 0o600)
}

// afterRun turns a caught panic into a saved report and holds the window.
func afterRun(runErr error, stderr []byte, in io.Reader) {
	if runErr == nil || !(errors.Is(runErr, tea.ErrProgramPanic) || bytes.Contains(stderr, []byte("Caught panic"))) {
		return
	}
	path, err := writeCrashReport(runErr, stderr, time.Now())
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nMailday crashed and the report could not be saved: %v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "\nMailday crashed. The report is in %s\n", path)
	}
	fmt.Fprint(os.Stderr, "Press Enter to close.")
	_, _ = bufio.NewReader(in).ReadString('\n')
}
