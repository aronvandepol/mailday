package tui

// Snoozed mail among the events. A snoozed message has not gone away, it
// comes back at a set time, so the agenda and the day panel list it at that
// time as a dim row of its own (an envelope, not a calendar dot), and the
// today strip names the next one when it is under an hour off. The rows are
// built from the @Snoozed messages loaded and the shared snooze list, read
// when the mail reloads, not on every redraw.

import (
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/aronvandepol/mailday/internal/maildir"
	"github.com/aronvandepol/mailday/internal/syncd"
	"github.com/aronvandepol/mailday/internal/terminal"
)

const notAnEventStatus = "That is a snoozed message, not an event"

func init() {
	addHelp("Calendar", [][2]string{
		{"✉ rows", "snoozed mail at its due time: enter reads it, Z snoozes it again; event keys refuse it"},
	})
}

// upcomingMail is a message in @Snoozed with its due time.
type upcomingMail struct {
	message maildir.Message
	due     time.Time
}

func (u upcomingMail) key() string { return syncd.SnoozeKey(u.message.Account, u.message.MessageID) }

// A snoozed row is selected by a negative eventCursor, -(index+1) into
// Model.upcoming: a reset to 0 (new span, new mode) then clears it by itself,
// and selectedEvent already says "no event" for it.
func upcomingCursor(index int) int { return -index - 1 }

// refreshUpcoming rebuilds the rows from the loaded @Snoozed messages and the
// snooze list. A selected row is followed to its new place, or dropped.
func (m *Model) refreshUpcoming() {
	var selectedKey string
	if m.eventCursor < 0 && -m.eventCursor-1 < len(m.upcoming) {
		selectedKey = m.upcoming[-m.eventCursor-1].key()
	}
	m.upcoming = nil
	dues := loadSnoozeDues()
	for _, message := range m.messages {
		if messageBox(message) != snoozedBox || message.MessageID == "" {
			continue
		}
		if due, ok := dues[syncd.SnoozeKey(message.Account, message.MessageID)]; ok {
			m.upcoming = append(m.upcoming, upcomingMail{message: message, due: due})
		}
	}
	slices.SortStableFunc(m.upcoming, func(a, b upcomingMail) int { return a.due.Compare(b.due) })
	if m.eventCursor < 0 {
		m.eventCursor = 0
		for index, u := range m.upcoming {
			if selectedKey != "" && u.key() == selectedKey {
				m.eventCursor = upcomingCursor(index)
			}
		}
	}
}

// boundEventCursor keeps the cursor on an event, or on a snoozed row that
// still exists.
func (m *Model) boundEventCursor() {
	if m.eventCursor < 0 {
		if -m.eventCursor-1 >= len(m.upcoming) {
			m.eventCursor = 0
		}
		return
	}
	m.eventCursor = bound(m.eventCursor, len(m.events))
}

// calRow is one line of the agenda: an event (index into events) or a
// snoozed message (index into upcoming), with the time it is listed at.
type calRow struct {
	event, mail int // -1 for the other kind
	at          time.Time
}

func (row calRow) cursor() int {
	if row.mail >= 0 {
		return upcomingCursor(row.mail)
	}
	return row.event
}

// listsUpcoming says whether the view on screen lists snoozed mail: the agenda
// and the day, and the week where it is too narrow for a grid and shows the
// agenda instead.
func (m Model) listsUpcoming() bool {
	switch m.calendarMode {
	case calendarWeek:
		return (max(m.width, 20)-gutterWidth)/7 < 9
	}
	return true
}

// visibleUpcoming is the snoozed rows in the span on screen, soonest first.
// What is already due is listed as now, under today, when today is in view.
func (m Model) visibleUpcoming() []calRow {
	if !m.listsUpcoming() || len(m.upcoming) == 0 {
		return nil
	}
	from, to := m.calendarRange()
	now := m.now()
	todayShown := !now.Before(from) && now.Before(to)
	var rows []calRow
	for index, u := range m.upcoming {
		at := u.due
		switch {
		case !u.due.After(now):
			if !todayShown {
				continue
			}
			at = now
		case at.Before(from) || !at.Before(to):
			continue
		}
		rows = append(rows, calRow{event: -1, mail: index, at: at})
	}
	return rows
}

// calendarRows is what the agenda lists: the events in the span with the
// snoozed mail between them by time.
func (m Model) calendarRows() []calRow {
	from, to := m.calendarRange()
	mails := m.visibleUpcoming()
	var rows []calRow
	next := 0
	for index, event := range m.events {
		// Until a reload for a new span lands, m.events still holds the old
		// span: list only what falls in the one on screen.
		if !event.End.After(from) || !event.Start.Before(to) {
			continue
		}
		at := event.Start
		if event.AllDay {
			at = dayStart(event.Start)
		}
		for next < len(mails) && mails[next].at.Before(at) {
			rows = append(rows, mails[next])
			next++
		}
		rows = append(rows, calRow{event: index, mail: -1, at: event.Start})
	}
	return append(rows, mails[next:]...)
}

// selectedUpcoming is the snoozed row under the cursor, when it is on screen.
func (m Model) selectedUpcoming() (upcomingMail, bool) {
	if m.eventCursor >= 0 || -m.eventCursor-1 >= len(m.upcoming) {
		return upcomingMail{}, false
	}
	for _, row := range m.visibleUpcoming() {
		if row.cursor() == m.eventCursor {
			return m.upcoming[row.mail], true
		}
	}
	return upcomingMail{}, false
}

// handleUpcomingKey takes the calendar keys that concern snoozed rows: moving
// over them among the events, enter, Z, and a refusal for the keys that edit
// an event. handled is false for everything else.
func (m Model) handleUpcomingKey(key string) (Model, tea.Cmd, bool) {
	if m.focus != paneAgenda || len(m.visibleUpcoming()) == 0 {
		return m, nil, false
	}
	switch key {
	case "up", "k", "down", "j", "home", "end", "G":
		rows := m.calendarRows()
		position := slices.IndexFunc(rows, func(row calRow) bool { return row.cursor() == m.eventCursor })
		var target int
		switch key {
		case "up", "k":
			target = max(position-1, 0)
		case "down", "j":
			target = position + 1
		case "home":
			target = 0
		default:
			target = len(rows) - 1
		}
		if len(rows) > 0 {
			m.eventCursor = rows[bound(target, len(rows))].cursor()
		}
		m.status = ""
		return m, nil, true
	}
	selected, ok := m.selectedUpcoming()
	if !ok {
		return m, nil, false
	}
	switch key {
	case "enter":
		m.snapshotReaderOrder()
		return m, m.showMessage(selected.message, ""), true
	case "Z":
		m.snoozing = &snoozePrompt{message: selected.message}
		m.pendingArchive, m.status = "", ""
		return m, nil, true
	case "m", "e", "d", "D", "a", "~", "x", "J", "<", ">", ",", ".", "+", "=", "-", "_":
		m.status = notAnEventStatus
		return m, nil, true
	}
	return m, nil, false
}

// upcomingTitle is "Subject · Sender", as the rows print it.
func upcomingTitle(u upcomingMail) string {
	title := terminal.SanitizeLine(u.message.Subject)
	if title == "" {
		title = "(no subject)"
	}
	sender := terminal.SanitizeLine(u.message.From)
	if sender == "" {
		sender = terminal.SanitizeLine(u.message.FromAddr)
	}
	if sender != "" {
		title += " · " + sender
	}
	return title
}

// agendaMailLine is a snoozed row of the agenda: dim, with an envelope in
// place of the calendar dot, and plainly a message.
func (m Model) agendaMailLine(row calRow, selected bool, width int, now time.Time) string {
	u := m.upcoming[row.mail]
	clock, label := u.due.Local().Format("15:04"), "✉ back: "
	if !u.due.After(now) {
		clock, label = "", "✉ due now: "
	}
	marker, text := "  ", styleMuted
	if selected {
		marker = lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render("│ ")
		text = lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
	}
	return truncateToWidth(marker+styleMuted.Render(padTo(clock, 5))+"  "+text.Render(label+upcomingTitle(u)), width)
}

// dayMailLines lists the day's snoozed rows for the day view, at most limit
// of them, kept around the selected one.
func (m Model) dayMailLines(width, limit int) []string {
	rows := m.visibleUpcoming()
	if len(rows) == 0 || limit <= 0 {
		return nil
	}
	selected := slices.IndexFunc(rows, func(row calRow) bool { return row.cursor() == m.eventCursor })
	first := min(max(selected-limit+1, 0), max(len(rows)-limit, 0))
	rows = rows[first:min(first+limit, len(rows))]
	now := m.now()
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		u := m.upcoming[row.mail]
		clock := u.due.Local().Format("15:04")
		if !u.due.After(now) {
			clock = "now"
		}
		marker, text := "  ", styleMuted
		if row.cursor() == m.eventCursor {
			marker = lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render("│ ")
			text = lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
		}
		lines = append(lines, truncateToWidth(marker+text.Render(padTo(clock, 5)+" ✉ "+upcomingTitle(u)), width))
	}
	return lines
}

// mailPanel is the day panel while a snoozed row is selected.
func (m Model) mailPanel(u upcomingMail, width int) []string {
	title := lipgloss.NewStyle().Foreground(colorBright).Bold(true)
	label := lipgloss.NewStyle().Foreground(colorChrome)
	subject := terminal.SanitizeLine(u.message.Subject)
	if subject == "" {
		subject = "(no subject)"
	}
	lines := []string{"", "✉ " + title.Render(truncateToWidth(subject, max(width-2, 1)))}
	if sender := terminal.SanitizeLine(u.message.From); sender != "" {
		lines = append(lines, label.Render(truncateToWidth(sender, width)))
	}
	back := "Back in your inbox " + snoozeDay(u.due, m.now())
	if !u.due.After(m.now()) {
		back = "Due now: back in your inbox any moment"
	}
	lines = append(lines, label.Render(truncateToWidth(back, width)))
	if u.message.Account != "" {
		lines = append(lines, "", styleMuted.Render(truncateToWidth("Snoozed mail from "+u.message.Account, width)))
	}
	lines = append(lines, "", styleMuted.Render("enter reads it · Z snoozes it again"))
	return append(lines, m.mailListLines(width)...)
}

// mailListLines is the "mail coming back" block of the day panel.
func (m Model) mailListLines(width int) []string {
	list := m.dayMailLines(width, 6)
	if len(list) == 0 {
		return nil
	}
	return append([]string{"", lipgloss.NewStyle().Foreground(colorChrome).Render("Mail coming back")}, list...)
}

// upcomingFooterBindings are the keys while a snoozed row is selected.
func (m Model) upcomingFooterBindings() []helpBinding {
	return []helpBinding{{"↑↓", "move"}, {"enter", "read"}, {"Z", "snooze again"}, {"←→", strings.ToLower(m.calendarMode.String())}, {"1 2 3", "day week agenda"}, {"t", "today"}, {"tab", "Mail"}, {"q", "quit"}}
}

// stripUpcoming is the snoozed message the today strip names: the next one
// to come back within the hour.
func (m Model) stripUpcoming() (upcomingMail, bool) {
	now := m.now()
	for _, u := range m.upcoming {
		if u.due.After(now) && u.due.Sub(now) < stripAhead {
			return u, true
		}
	}
	return upcomingMail{}, false
}
