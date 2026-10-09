package tui

// Status feedback that is hard to miss: failures render in the error colour
// and stay for a few seconds whatever the cursor does, and the last 30
// messages are kept with their times, shown by "!".

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const (
	// errorHold is how long a failure stays on the status line against
	// cursor moves and arrival notices.
	errorHold = 6 * time.Second
	// statusLogSize is how many status messages "!" can show.
	statusLogSize = 30
)

type statusEntry struct {
	at   time.Time
	text string
}

// isErrorStatus tells failures from progress and results by their wording.
func isErrorStatus(text string) bool {
	for _, prefix := range []string{"Not ", "Cannot", "Undo failed", "Mail error", "Search failed", "Draft not saved"} {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	lower := strings.ToLower(text)
	return strings.Contains(lower, "failed") || strings.Contains(lower, "error") || strings.Contains(lower, "unavailable")
}

// trackStatus runs after every update, with the status the update started
// from. It logs a changed status, and gives a recent failure its hold: a
// cursor move that clears the status, or an arrival notice, does not replace
// it before errorHold has passed.
func (m *Model) trackStatus(before string) {
	if m.status == before {
		return
	}
	now := m.now()
	held := before != "" && before == m.errorStatus && now.Before(m.errorUntil)
	if held && (m.status == "" || strings.HasPrefix(m.status, "New: ")) {
		m.status = before
		return
	}
	if m.status == "" {
		return
	}
	if last := len(m.statusLog) - 1; last < 0 || m.statusLog[last].text != m.status {
		m.statusLog = append(m.statusLog, statusEntry{at: now, text: m.status})
		rememberForCrash(now, m.status)
		if len(m.statusLog) > statusLogSize {
			m.statusLog = append([]statusEntry(nil), m.statusLog[len(m.statusLog)-statusLogSize:]...)
		}
	}
	if isErrorStatus(m.status) {
		m.errorStatus, m.errorUntil = m.status, now.Add(errorHold)
	} else {
		m.errorStatus, m.errorUntil = "", time.Time{}
	}
}

// handleLogKey scrolls the status log; esc, q or ! closes it.
func (m Model) handleLogKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	page := max(m.height-8, 1)
	switch message.String() {
	case "esc", "q", "!":
		m.showLog = false
	case "up", "k":
		m.logScroll = max(m.logScroll-1, 0)
	case "down", "j":
		m.logScroll = min(m.logScroll+1, max(len(m.statusLog)-1, 0))
	case "pgup", "ctrl+u":
		m.logScroll = max(m.logScroll-page, 0)
	case "pgdown", "ctrl+d":
		m.logScroll = min(m.logScroll+page, max(len(m.statusLog)-1, 0))
	case "home", "g":
		m.logScroll = 0
	case "end", "G":
		m.logScroll = max(len(m.statusLog)-1, 0)
	}
	return m, nil
}

// renderLog lists the recent status messages, newest first.
func (m Model) renderLog(width, height int) string {
	column, margin := readerColumn(width)
	pad := strings.Repeat(" ", margin)
	lines := []string{pad + sectionHeader("Recent messages", column)}
	if len(m.statusLog) == 0 {
		lines = append(lines, pad+styleMuted.Render("Nothing yet."))
	}
	errorStyle := lipgloss.NewStyle().Foreground(colorError).Bold(true)
	textStyle := lipgloss.NewStyle().Foreground(colorBright)
	for index := len(m.statusLog) - 1 - m.logScroll; index >= 0; index-- {
		entry := m.statusLog[index]
		style := textStyle
		if isErrorStatus(entry.text) {
			style = errorStyle
		}
		stamp := entry.at.Local().Format("15:04:05")
		wrapped := wrapText(entry.text, max(column-10, 10))
		for row, text := range wrapped {
			prefix := strings.Repeat(" ", 10)
			if row == 0 {
				prefix = styleMuted.Render(fmt.Sprintf("%-10s", stamp))
			}
			lines = append(lines, pad+prefix+style.Render(text))
		}
	}
	return strings.Join(lines[:min(len(lines), height)], "\n")
}
