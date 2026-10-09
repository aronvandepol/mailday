package contacts

// Group suggestions: the same few people written to together again and
// again (a board, a project team, co-authors) are a group waiting to be
// named. Sent mail counts double; mail others sent to you and them counts
// once; lists and robots not at all.

import (
	"bufio"
	"fmt"
	"math"
	"mime"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// Suggestion is a set of people often on the same message.
type Suggestion struct {
	Name     string         // a guess: shared organisation or recurring subject word
	Members  []mail.Address // most-written-to first
	Messages int            // messages with all of them on it
	Last     time.Time
	Subjects []string // a few recent subjects, to recognise the group by
}

type sighting struct {
	members []string // sorted lower-case addresses
	weight  int
	date    time.Time
	subject string
	names   map[string]string
}

const (
	suggestMinPeople = 2
	suggestMaxPeople = 25 // larger is a list, not a group
	suggestSince     = 3 * 365 * 24 * time.Hour
)

// Exchange rooms: "Hum - Herta Mohr - 2.137 (6 personen)".
var roomName = regexp.MustCompile(`(?i)\(\d+\s*(personen|persons|people|pers\.?|seats|plaatsen)\)|\b(room|zaal|vergaderruimte|meeting ?room)\b`)

// Reply and forward markers, and the ones calendar mail puts in front.
var subjectPrefix = regexp.MustCompile(`(?i)^\s*((re|fw|fwd|aw|wg|antw|sv|vs|canceled|cancelled|accepted|declined|tentative|updated|geannuleerd|geaccepteerd|afgewezen|voorlopig|bijgewerkt|invitation|uitnodiging)\s*(\[\d+\])?\s*:\s*)+`)

// SuggestGroups scans the Maildirs under roots and returns up to limit
// suggested groups, best first. own tells your own addresses.
func SuggestGroups(roots []string, own func(string) bool, limit int, now time.Time) []Suggestion {
	paths := make(chan [2]string, 256) // path, "sent" or ""
	var mutex sync.Mutex
	var seen []sighting
	var wg sync.WaitGroup
	for range runtime.NumCPU() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range paths {
				if s, ok := readSighting(item[0], item[1] == "sent", own, now); ok {
					mutex.Lock()
					seen = append(seen, s)
					mutex.Unlock()
				}
			}
		}()
	}
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return nil
			}
			leaf := filepath.Base(filepath.Dir(path))
			if leaf != "cur" && leaf != "new" {
				return nil
			}
			folder := filepath.Dir(filepath.Dir(path))
			relative, _ := filepath.Rel(root, folder)
			parts := strings.SplitN(relative, string(filepath.Separator), 2)
			if len(parts) < 2 {
				return nil
			}
			box := parts[1]
			// Trash, junk and drafts say nothing about who belongs together.
			lower := strings.ToLower(box)
			if strings.Contains(lower, "trash") || strings.Contains(lower, "deleted") || strings.Contains(lower, "junk") || strings.Contains(lower, "spam") || strings.Contains(lower, "draft") {
				return nil
			}
			kind := ""
			if sentFolders[box] {
				kind = "sent"
			}
			paths <- [2]string{path, kind}
			return nil
		})
	}
	close(paths)
	wg.Wait()
	return rankSuggestions(seen, limit)
}

func readSighting(path string, sent bool, own func(string) bool, now time.Time) (sighting, bool) {
	file, err := os.Open(path)
	if err != nil {
		return sighting{}, false
	}
	defer file.Close()
	message, err := mail.ReadMessage(bufio.NewReaderSize(file, 8192))
	if err != nil {
		return sighting{}, false
	}
	header := message.Header
	if header.Get("List-Id") != "" || header.Get("List-Unsubscribe") != "" ||
		strings.Contains(strings.ToLower(header.Get("Precedence")), "bulk") ||
		strings.HasPrefix(strings.ToLower(header.Get("Auto-Submitted")), "auto-") {
		return sighting{}, false
	}
	date, _ := header.Date()
	if date.IsZero() || now.Sub(date) > suggestSince {
		return sighting{}, false
	}
	decoder := new(mime.WordDecoder)
	keys := []string{"From", "To", "Cc"}
	if sent {
		keys = []string{"To", "Cc", "Bcc"}
	}
	names := map[string]string{}
	you := sent
	for _, key := range keys {
		raw := header.Get(key)
		if raw == "" {
			continue
		}
		if decoded, err := decoder.DecodeHeader(raw); err == nil {
			raw = decoded
		}
		list, err := mail.ParseAddressList(raw)
		if err != nil {
			continue
		}
		for _, address := range list {
			addr := strings.ToLower(strings.TrimSpace(address.Address))
			if addr == "" || !strings.Contains(addr, "@") {
				continue
			}
			if own(addr) {
				you = true
				continue
			}
			if automated.MatchString(addr) {
				return sighting{}, false // a robot on it: not a group conversation
			}
			if roomName.MatchString(address.Name) {
				continue // a meeting room booked on an invitation, not a person
			}
			if _, ok := names[addr]; !ok || names[addr] == "" {
				names[addr] = strings.TrimSpace(address.Name)
			}
		}
	}
	// Only mail you were on: a forward of someone else's thread is not yours.
	if !you || len(names) < suggestMinPeople || len(names) > suggestMaxPeople {
		return sighting{}, false
	}
	members := make([]string, 0, len(names))
	for addr := range names {
		members = append(members, addr)
	}
	sort.Strings(members)
	subject := header.Get("Subject")
	if decoded, err := decoder.DecodeHeader(subject); err == nil {
		subject = decoded
	}
	weight := 1
	if sent {
		weight = 2
	}
	return sighting{members: members, weight: weight, date: date, subject: strings.TrimSpace(subjectPrefix.ReplaceAllString(subject, "")), names: names}, true
}

// rankSuggestions groups sightings by their exact set of people, counts for
// each set every message that has all of them (a thread where one more was
// copied in still counts), and keeps the strongest sets that are not mostly
// another one.
func rankSuggestions(seen []sighting, limit int) []Suggestion {
	type set struct {
		members  []string
		weight   int // of messages with exactly these people
		support  int // weighted messages with at least these people
		messages int
		last     time.Time
		subjects map[string]int
		names    map[string]string
	}
	byKey := map[string]*set{}
	for _, s := range seen {
		key := strings.Join(s.members, ",")
		entry := byKey[key]
		if entry == nil {
			entry = &set{members: s.members, subjects: map[string]int{}, names: map[string]string{}}
			byKey[key] = entry
		}
		entry.weight += s.weight
		if s.subject != "" {
			entry.subjects[s.subject]++
		}
		for addr, name := range s.names {
			if entry.names[addr] == "" {
				entry.names[addr] = name
			}
		}
	}
	sets := make([]*set, 0, len(byKey))
	for _, entry := range byKey {
		sets = append(sets, entry)
	}
	for _, candidate := range sets {
		subjects := map[string]int{}
		for _, other := range sets {
			if containsAll(other.members, candidate.members) {
				candidate.support += other.weight
				candidate.messages += other.weight
				for subject, count := range other.subjects {
					subjects[subject] += count
				}
				for addr, name := range other.names {
					if candidate.names[addr] == "" {
						candidate.names[addr] = name
					}
				}
			}
		}
		candidate.subjects = subjects
	}
	for _, s := range seen {
		for _, candidate := range sets {
			if s.date.After(candidate.last) && containsAll(s.members, candidate.members) {
				candidate.last = s.date
			}
		}
	}
	score := func(entry *set) float64 { return float64(entry.support) * math.Sqrt(float64(len(entry.members))) }
	sort.Slice(sets, func(i, j int) bool {
		if score(sets[i]) != score(sets[j]) {
			return score(sets[i]) > score(sets[j])
		}
		return strings.Join(sets[i].members, ",") < strings.Join(sets[j].members, ",")
	})

	eligible := func(entry *set) bool { return entry.support >= 4 && entry.weight >= 2 }
	// A pair inside a team that is mostly written to whole is the team: the
	// KH four, not three KH pairs.
	inTeam := func(candidate *set) bool {
		for _, other := range sets {
			if len(other.members) > len(candidate.members) && eligible(other) &&
				containsAll(other.members, candidate.members) && float64(other.support) >= 0.4*float64(candidate.support) {
				return true
			}
		}
		return false
	}
	var picked []*set
	pickedNames := map[string]bool{}
	for _, candidate := range sets {
		if inTeam(candidate) {
			continue
		}
		// At least three messages' worth (a sent one counts two), and seen
		// more than once with exactly these people.
		if candidate.support < 4 || candidate.weight < 2 {
			continue
		}
		redundant := false
		for _, kept := range picked {
			// Mostly the same people as a stronger suggestion.
			if jaccard(kept.members, candidate.members) >= 0.6 {
				redundant = true
				break
			}
			// A part of a stronger group that is hardly written to alone.
			if containsAll(kept.members, candidate.members) && float64(kept.support) >= 0.6*float64(candidate.support) {
				redundant = true
				break
			}
		}
		// The same people under other addresses (a Gmail and a work one).
		var people []string
		for _, addr := range candidate.members {
			people = append(people, strings.ToLower(DisplayName(candidate.names[addr], addr)))
		}
		sort.Strings(people)
		if key := strings.Join(people, "|"); !redundant && !pickedNames[key] {
			pickedNames[key] = true
			picked = append(picked, candidate)
		}
		if len(picked) == limit {
			break
		}
	}

	out := make([]Suggestion, 0, len(picked))
	for _, entry := range picked {
		suggestion := Suggestion{Messages: (entry.messages + 1) / 2, Last: entry.last}
		for _, addr := range entry.members {
			name := DisplayName(entry.names[addr], addr)
			if name == addr {
				name = ""
			}
			suggestion.Members = append(suggestion.Members, mail.Address{Name: name, Address: addr})
		}
		subjects := make([]string, 0, len(entry.subjects))
		for subject := range entry.subjects {
			subjects = append(subjects, subject)
		}
		sort.Slice(subjects, func(i, j int) bool {
			if entry.subjects[subjects[i]] != entry.subjects[subjects[j]] {
				return entry.subjects[subjects[i]] > entry.subjects[subjects[j]]
			}
			return subjects[i] < subjects[j]
		})
		if len(subjects) > 3 {
			subjects = subjects[:3]
		}
		suggestion.Subjects = subjects
		suggestion.Name = guessGroupName(suggestion.Members, entry.subjects)
		out = append(out, suggestion)
	}
	return out
}

func containsAll(have, want []string) bool {
	// Both sorted.
	index := 0
	for _, item := range want {
		for index < len(have) && have[index] < item {
			index++
		}
		if index == len(have) || have[index] != item {
			return false
		}
		index++
	}
	return true
}

func jaccard(a, b []string) float64 {
	both := 0
	for _, item := range a {
		if slices.Contains(b, item) {
			both++
		}
	}
	return float64(both) / float64(len(a)+len(b)-both)
}

var nameStopwords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "from": true, "your": true, "about": true, "this": true, "that": true,
	"meeting": true, "update": true, "question": true, "request": true, "invitation": true, "updated": true, "accepted": true, "draft": true,
	"van": true, "het": true, "een": true, "voor": true, "met": true, "over": true, "naar": true, "vergadering": true, "afspraak": true,
	"re": true, "fw": true, "fwd": true, "aw": true, "2024": true, "2025": true, "2026": true, "2027": true,
	"canceled": true, "cancelled": true, "declined": true, "tentative": true, "geannuleerd": true, "geaccepteerd": true,
	"uitnodiging": true, "afgewezen": true, "new": true, "time": true, "proposed": true, "call": true, "zoom": true, "teams": true,
}

// Words of three letters or more, or an acronym of two (KH, PR).
var wordPattern = regexp.MustCompile(`[\p{L}\p{N}][\p{L}\p{N}&-]{2,}|\b\p{Lu}{2}\b`)

// guessGroupName names a suggestion after a word most of its subjects share
// (ESS, Supervision), else after its people: "Anouk & Rob".
func guessGroupName(members []mail.Address, subjects map[string]int) string {
	total := 0
	words := map[string]int{}
	shown := map[string]string{}
	for subject, count := range subjects {
		total += count
		seen := map[string]bool{}
		for _, word := range wordPattern.FindAllString(subject, -1) {
			lower := strings.ToLower(word)
			if nameStopwords[lower] || seen[lower] {
				continue
			}
			seen[lower] = true
			words[lower] += count
			// Keep the most capitalised spelling: ESS over Ess.
			if strings.ToUpper(word) == word || shown[lower] == "" {
				shown[lower] = word
			}
		}
	}
	best, bestCount := "", 0
	for word, count := range words {
		if count > bestCount || (count == bestCount && word < best) {
			best, bestCount = word, count
		}
	}
	if best != "" && total > 0 && float64(bestCount) >= 0.4*float64(total) && bestCount >= 2 {
		word := shown[best]
		if strings.ToLower(word) == word {
			word = strings.ToUpper(word[:1]) + word[1:]
		}
		return word
	}
	var first []string
	for _, member := range members {
		name := member.Name
		if name == "" {
			name = member.Address[:strings.Index(member.Address, "@")]
		}
		first = append(first, strings.Fields(name)[0])
	}
	switch {
	case len(first) == 2:
		return first[0] + " & " + first[1]
	case len(first) == 3:
		return first[0] + ", " + first[1] + " & " + first[2]
	default:
		return fmt.Sprintf("%s, %s +%d", first[0], first[1], len(first)-2)
	}
}
