package tui

// g in the calendar asks "when?" and jumps there, with the phrases the
// event prompts take ("12 nov", "next week", "fri").

import (
	"errors"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/aronvandepol/mailday/internal/when"
)

// promptGoto is the jump prompt. It sits well clear of the event prompts'
// kinds, whose switches and maps know nothing of it: its keys and footer
// have their own handlers.
const promptGoto promptKind = 100

func (m Model) startGotoPrompt() Model {
	m.calPrompt = &calendarPrompt{kind: promptGoto}
	m.pendingDelete = ""
	m.status = ""
	return m
}

// gotoDate reads the phrase as a day. Only the date matters; other words are ignored.
func (m Model) gotoDate(input string) (time.Time, error) {
	if strings.TrimSpace(input) == "" {
		return time.Time{}, errors.New("when? 12 nov · next week · fri · tomorrow")
	}
	parsed, err := when.Parse(input, m.now())
	if err != nil {
		return time.Time{}, err
	}
	if !parsed.HasDate {
		return time.Time{}, errors.New("no date in that: try 12 nov · next week · fri")
	}
	day := parsed.Date
	return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.Local), nil
}

func (m Model) handleGotoKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	prompt := m.calPrompt
	switch message.String() {
	case "esc":
		m.calPrompt = nil
		m.status = ""
		return m, nil
	case "enter":
		day, err := m.gotoDate(prompt.input)
		if err != nil {
			m.status = err.Error()
			return m, nil
		}
		m.calPrompt = nil
		if sameDay(day, m.now()) {
			day = time.Time{} // today follows midnight, as t does
		}
		m.calendarAnchor = day
		m.eventCursor = 0
		m.loadingCal = true
		m.status = "Going to " + m.calendarDate().Format("Monday 2 January")
		return m, m.loadCalendarCmd()
	case "backspace":
		prompt.input = trimLastRune(prompt.input)
	case "ctrl+u":
		prompt.input = ""
	case "ctrl+w":
		prompt.input = strings.TrimRight(prompt.input, " ")
		if index := strings.LastIndex(prompt.input, " "); index >= 0 {
			prompt.input = prompt.input[:index+1]
		} else {
			prompt.input = ""
		}
	default:
		if text := message.Key().Text; text != "" && !message.Key().Mod.Contains(tea.ModCtrl) {
			prompt.input += text
		}
	}
	return m, nil
}

// renderGotoPrompt is the footer while g waits: what was typed and the day
// Enter would go to.
func (m Model) renderGotoPrompt(width int) string {
	accent := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
	input := accent.Render("› ") + m.calPrompt.input + lipgloss.NewStyle().Foreground(colorPrimary).Render("█")
	preview, style := "", lipgloss.NewStyle().Foreground(colorActive)
	if day, err := m.gotoDate(m.calPrompt.input); err != nil {
		preview, style = err.Error(), styleMuted
	} else {
		preview = "enter goes to " + day.Format("Monday 2 January 2006")
	}
	return renderRule(width, "Go to date") + "\n" +
		truncateToWidth(input, width) + "\n" +
		truncateToWidth("  "+style.Render(preview), width)
}
