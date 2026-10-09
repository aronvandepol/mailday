package tui

// Full-text search: / filters the loaded mail as you type, and notmuch looks
// through every message (bodies too) a moment after you stop, adding what it
// finds. notmuch's syntax works: from:ada, subject:budget, date:2025..

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/aronvandepol/mailday/internal/maildir"
	"github.com/aronvandepol/mailday/internal/terminal"
)

const searchDelay = 250 * time.Millisecond

// searchLimit is how many hits one notmuch run returns. A full answer means
// there may be more, which the status says.
const searchLimit = 300

type searchTickMsg struct{ seq int }

type searchResultMsg struct {
	query    string
	messages []maildir.Message
	err      error
	// status is notmuch's complaint, first line only, when err is set.
	status string
	// capped: notmuch returned searchLimit hits, so there may be more.
	capped bool
}

func notmuchAvailable() bool {
	_, err := exec.LookPath("notmuch")
	return err == nil
}

// scheduleSearch runs notmuch once typing pauses.
func (m Model) scheduleSearch() (Model, tea.Cmd) {
	if strings.TrimSpace(m.query) == "" {
		m.searchHits, m.searchQuery, m.searchExtra = nil, "", nil
		return m, nil
	}
	if !notmuchAvailable() {
		return m, nil
	}
	m.searchSeq++
	seq := m.searchSeq
	return m, tea.Tick(searchDelay, func(time.Time) tea.Msg { return searchTickMsg{seq: seq} })
}

func (m Model) handleSearchTick(message searchTickMsg) (tea.Model, tea.Cmd) {
	if message.seq != m.searchSeq {
		return m, nil
	}
	return m, searchCmd(m.mailStore, strings.TrimSpace(m.query))
}

func searchCmd(store MailStore, query string) tea.Cmd {
	lookup, ok := store.(interface {
		Lookup([]string) []maildir.Message
	})
	if !ok {
		if pushing, isPushing := store.(pushingStore); isPushing {
			lookup, ok = pushing.MailStore.(interface {
				Lookup([]string) []maildir.Message
			})
		}
	}
	if !ok || query == "" {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		output, err := exec.CommandContext(ctx, "notmuch", "search", "--output=files", "--limit="+strconv.Itoa(searchLimit), "--", query).Output()
		if err != nil {
			return searchResultMsg{query: query, err: err, status: searchFailure(err, ctx.Err())}
		}
		var paths []string
		for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
			if line != "" {
				paths = append(paths, line)
			}
		}
		return searchResultMsg{query: query, messages: lookup.Lookup(paths), capped: len(paths) >= searchLimit}
	}
}

// searchFailure words a failed notmuch run for the status line: the first
// line of what notmuch printed on stderr (a syntax error, a missing index),
// or the exec error when it printed nothing.
func searchFailure(err, contextErr error) string {
	if errors.Is(contextErr, context.DeadlineExceeded) {
		return "Search failed: notmuch took too long"
	}
	text := err.Error()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		for _, line := range strings.Split(string(exit.Stderr), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				text = line
				break
			}
		}
	}
	return "Search failed: " + terminal.SanitizeLine(text)
}

// searchHit reports whether notmuch found the message. Hits stay until the
// next answer replaces them, so typing on shows the previous results instead
// of an empty list; searchStale says when they are not for the current query.
func (m Model) searchHit(message maildir.Message) bool {
	return m.searchHits[maildir.Key(message.Path)]
}

// searchStale: the shown hits answer an earlier query than the one typed.
func (m Model) searchStale() bool {
	return len(m.searchHits) > 0 && m.searchQuery != strings.TrimSpace(m.query)
}

func (m Model) handleSearchResult(message searchResultMsg) (tea.Model, tea.Cmd) {
	if message.query != strings.TrimSpace(m.query) {
		return m, nil
	}
	if message.err != nil {
		// The previous hits stay on screen; the status says why there are no new ones.
		m.status = message.status
		return m, nil
	}
	known := make(map[string]bool, len(m.messages))
	for _, loaded := range m.messages {
		known[maildir.Key(loaded.Path)] = true
	}
	m.searchHits = map[string]bool{}
	m.searchExtra = nil
	for _, found := range message.messages {
		key := maildir.Key(found.Path)
		m.searchHits[key] = true
		if !known[key] {
			known[key] = true
			m.searchExtra = append(m.searchExtra, found)
			m.messages = append(m.messages, found)
		}
	}
	m.searchQuery = message.query
	m.boundCursors()
	if !m.filtering {
		count := len(m.filteredMessageIndexes())
		m.status = searchSummary(count, message.capped)
	}
	return m, nil
}

func searchSummary(count int, capped bool) string {
	if capped {
		return strconv.Itoa(max(count, searchLimit)) + "+ messages in all mail"
	}
	if count == 1 {
		return "1 message in all mail"
	}
	return strconv.Itoa(count) + " messages in all mail"
}

// keptSearchExtras carries search hits across a reload while the search is
// on, dropping files that moved away.
func (m Model) keptSearchExtras(loaded []maildir.Message) []maildir.Message {
	if strings.TrimSpace(m.query) == "" || len(m.searchExtra) == 0 {
		return nil
	}
	known := make(map[string]bool, len(loaded))
	for _, message := range loaded {
		known[maildir.Key(message.Path)] = true
	}
	var kept []maildir.Message
	for _, extra := range m.searchExtra {
		if known[maildir.Key(extra.Path)] {
			continue
		}
		if resolved := m.mailStore.Resolve(extra.Path); resolved != "" {
			extra.Path = resolved
			kept = append(kept, extra)
		}
	}
	return kept
}

// dropSearch forgets the search and takes its extra hits out of the list.
func (m *Model) dropSearch() {
	if len(m.searchExtra) > 0 {
		extra := make(map[string]bool, len(m.searchExtra))
		for _, message := range m.searchExtra {
			extra[message.Path] = true
		}
		kept := m.messages[:0]
		for _, message := range m.messages {
			if !extra[message.Path] {
				kept = append(kept, message)
			}
		}
		m.messages = kept
	}
	m.searchHits, m.searchQuery, m.searchExtra = nil, "", nil
}
