package tui

// The event form edits everything about an event at once: title, when,
// calendar (Google or Exchange/Outlook, ←/→), place, people to invite and
// notes. e opens it on the selected event, N on a new one. Changing the
// calendar moves the event there.

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/aronvandepol/mailday/internal/calendar"
	"github.com/aronvandepol/mailday/internal/terminal"
	"github.com/aronvandepol/mailday/internal/when"
)

type eventFormField int

const (
	formTitle eventFormField = iota
	formWhen
	formCalendar
	formLocation
	formInvite
	formNotes
	formFieldCount
)

type eventForm struct {
	editing  bool
	original calendar.Event
	title    string
	when     string
	calendar string
	location string
	invite   string
	notes    string
	field    eventFormField
}

type notesEditedMsg struct {
	text string
	err  error
}

// whenText writes an event's time the way the when parser reads it back.
func whenText(event calendar.Event) string {
	start, end := event.Start.Local(), event.End.Local()
	if event.AllDay {
		text := start.Format("2006-01-02") + " all day"
		// Midnights are 23 or 25 hours apart over a daylight-saving change.
		if days := int((end.Sub(start) + 12*time.Hour) / (24 * time.Hour)); days > 1 {
			text += fmt.Sprintf(" for %d days", days)
		}
		return text
	}
	if sameDay(start, end) {
		return start.Format("2006-01-02") + " " + start.Format("15:04") + "-" + end.Format("15:04")
	}
	// An end on the next day, midnight included, would read back as an end
	// before the start: say the length instead.
	return start.Format("2006-01-02 15:04") + " for " + lengthPhrase(end.Sub(start))
}

// maxNotesRunes is where mailday-calsync cuts a description off: a copy this
// long is not the whole text, and sending it back would shorten the event's.
const maxNotesRunes = 4000

// editableNotes is a description without the "Join: <url>" line
// mailday-calsync puts in front of it.
func editableNotes(description string) string {
	rest, found := strings.CutPrefix(description, "Join: ")
	if !found {
		return description
	}
	if _, after, hasNotes := strings.Cut(rest, "\n\n"); hasNotes {
		return after
	}
	return ""
}

// notesTooLong says the held description is a truncated copy.
func notesTooLong(description string) bool {
	return utf8.RuneCountInString(editableNotes(description)) >= maxNotesRunes
}

func lengthPhrase(length time.Duration) string {
	hours, minutes := int(length.Hours()), int(length.Minutes())%60
	if minutes == 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dh%dm", hours, minutes)
}

func (m Model) openEventForm(editing bool) Model {
	if len(m.writableCalendars) == 0 {
		m.status = "Calendar editing needs mailday-calendar (make install)"
		return m
	}
	form := &eventForm{editing: editing, calendar: m.defaultCalendar()}
	if editing {
		event, ok, reason := m.editableSelection()
		if !ok {
			m.status = reason
			return m
		}
		form.original = event
		form.title, form.location, form.notes = event.Summary, event.Location, editableNotes(event.Description)
		form.when = whenText(event)
		form.calendar = event.Source
	}
	m.eventForm = form
	m.calPrompt = nil
	m.status = ""
	return m
}

func (f *eventForm) value() *string {
	switch f.field {
	case formTitle:
		return &f.title
	case formWhen:
		return &f.when
	case formLocation:
		return &f.location
	case formInvite:
		return &f.invite
	case formNotes:
		return &f.notes
	}
	return nil
}

func (m Model) handleEventFormKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	form := m.eventForm
	key := message.String()
	switch key {
	case "esc":
		m.eventForm = nil
		m.status = ""
		return m, nil
	case "ctrl+s":
		return m.saveEventForm()
	case "tab", "down":
		form.field = (form.field + 1) % formFieldCount
		return m, nil
	case "shift+tab", "up":
		form.field = (form.field + formFieldCount - 1) % formFieldCount
		return m, nil
	case "enter":
		if form.field == formNotes {
			form.notes += "\n"
			return m, nil
		}
		if form.field == formInvite {
			return m.saveEventForm()
		}
		form.field++
		return m, nil
	case "ctrl+o":
		if form.editing && notesTooLong(form.original.Description) {
			m.status = "Notes are too long to edit here; ctrl+o is disabled for them"
			return m, nil
		}
		return m, editNotesCmd(form.notes)
	}
	if form.field == formCalendar {
		step := 0
		switch key {
		case "left", "h":
			step = len(m.writableCalendars) - 1
		case "right", "l", "space", " ":
			step = 1
		}
		if step != 0 {
			current := 0
			for index, name := range m.writableCalendars {
				if name == form.calendar {
					current = index
				}
			}
			form.calendar = m.writableCalendars[(current+step)%len(m.writableCalendars)]
		}
		return m, nil
	}
	target := form.value()
	if target == nil {
		return m, nil
	}
	switch key {
	case "backspace":
		*target = trimLastRune(*target)
	case "ctrl+u":
		*target = ""
	case "ctrl+w":
		*target = strings.TrimRight(*target, " ")
		if index := strings.LastIndexAny(*target, " \n"); index >= 0 {
			*target = (*target)[:index+1]
		} else {
			*target = ""
		}
	default:
		if text := message.Key().Text; text != "" && !message.Key().Mod.Contains(tea.ModCtrl) {
			*target += text
		}
	}
	return m, nil
}

// editNotesCmd opens the notes in $MAILDAY_EDITOR, $VISUAL, $EDITOR or nvim.
func editNotesCmd(text string) tea.Cmd {
	file, err := os.CreateTemp("", "mailday-notes-*.md")
	if err != nil {
		return func() tea.Msg { return notesEditedMsg{err: err} }
	}
	_, _ = file.WriteString(text)
	file.Close()
	editor := "nvim"
	for _, name := range []string{"MAILDAY_EDITOR", "VISUAL", "EDITOR"} {
		if value := os.Getenv(name); value != "" {
			editor = value
			break
		}
	}
	parts := strings.Fields(editor)
	command := exec.Command(parts[0], append(parts[1:], file.Name())...)
	return tea.ExecProcess(command, func(err error) tea.Msg {
		defer os.Remove(file.Name())
		data, readErr := os.ReadFile(file.Name())
		if err == nil {
			err = readErr
		}
		return notesEditedMsg{text: strings.TrimRight(string(data), "\n"), err: err}
	})
}

// formResult works out the event the form describes, and whom to invite.
func (m Model) formResult() (calendar.Event, []string, []string, error) {
	form := m.eventForm
	event := form.original
	if strings.TrimSpace(form.title) == "" {
		return event, nil, nil, errors.New("type a title")
	}
	event.Summary = strings.TrimSpace(form.title)
	event.Location = strings.TrimSpace(form.location)
	if form.editing && form.notes == editableNotes(form.original.Description) {
		event.Description = form.original.Description // untouched, Join line and all
	} else {
		event.Description = form.notes
	}
	event.Source = form.calendar
	if !form.editing {
		event = calendar.Event{Summary: event.Summary, Location: event.Location, Description: form.notes, Source: form.calendar, Editable: true}
	}
	start, end, allDay, err := m.formTimes()
	if err != nil {
		return event, nil, nil, err
	}
	event.Start, event.End, event.AllDay = start, end, allDay
	if repeat := m.formRepeat(); repeat != nil {
		event.Rule, event.RuleText, event.Series = repeat.Rule(), repeat.Describe(), true
	}
	addresses, labels, err := m.resolveInvitees(form.invite)
	if err != nil {
		return event, nil, nil, err
	}
	return event, addresses, labels, nil
}

// formRepeat is the repeat typed in When, for a new event.
func (m Model) formRepeat() *when.Repeat {
	if m.eventForm.editing {
		return nil
	}
	parsed, err := when.Parse(m.eventForm.when, m.now())
	if err != nil {
		return nil
	}
	return parsed.Repeat
}

func (m Model) formTimes() (time.Time, time.Time, bool, error) {
	form := m.eventForm
	text := strings.TrimSpace(form.when)
	if form.editing && text == whenText(form.original) {
		return form.original.Start, form.original.End, form.original.AllDay, nil
	}
	parsed, err := when.Parse(text, m.now())
	if err != nil {
		return time.Time{}, time.Time{}, false, err
	}
	if parsed.Title != "" {
		return time.Time{}, time.Time{}, false, fmt.Errorf("not a time: %q", parsed.Title)
	}
	if !form.editing || parsed.AllDay || (form.original.AllDay && parsed.HasTime) {
		parsed.Title = "event"
		day := m.calendarDate()
		if form.editing {
			day = dayStart(form.original.Start)
		} else if event, ok := m.selectedEvent(); ok && m.calendarMode != calendarAgenda {
			day = dayStart(event.Start)
		}
		start, end, err := when.Event(parsed, day, m.now())
		return start, end, parsed.AllDay, err
	}
	if form.original.AllDay {
		// A new day or span for an all-day event stays all-day.
		start, end, err := when.MoveAllDay(parsed, form.original.Start, form.original.End)
		return start, end, true, err
	}
	start, end, err := when.Move(parsed, form.original.Start, form.original.End)
	return start, end, false, err
}

// resolveInvitees reads "ada, +bert, carl@uni.nl": addresses as typed,
// names through the address book (its best-ranked match).
func (m Model) resolveInvitees(text string) ([]string, []string, error) {
	var addresses, labels []string
	for _, part := range strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == ';' }) {
		query := strings.TrimPrefix(strings.TrimSpace(part), "+")
		if query == "" {
			continue
		}
		if strings.Contains(query, "@") && strings.Contains(query[strings.Index(query, "@"):], ".") {
			addresses, labels = append(addresses, query), append(labels, query)
			continue
		}
		matches := m.book.Search(query, 1, nil)
		if len(matches) == 0 {
			return nil, nil, fmt.Errorf("nobody called %q in the address book; type the address", query)
		}
		addresses = append(addresses, matches[0].Address)
		label := matches[0].Name
		if label == "" {
			label = matches[0].Address
		}
		labels = append(labels, label+" <"+matches[0].Address+">")
	}
	return addresses, labels, nil
}

func (m Model) saveEventForm() (tea.Model, tea.Cmd) {
	form := m.eventForm
	event, attendees, labels, err := m.formResult()
	if err != nil {
		m.status = err.Error()
		return m, nil
	}
	notesChanged := form.editing && event.Description != form.original.Description
	if notesChanged && notesTooLong(form.original.Description) {
		m.status = "Notes are too long to edit here; ctrl+o is disabled for them"
		return m, nil
	}
	before := form.original
	transfer := form.editing && event.Source != before.Source
	if transfer {
		if len(attendees) > 0 {
			m.status = "Cannot invite while moving between calendars: move it first, then invite"
			return m, nil
		}
		if m.calendarBackends[before.Source] != m.calendarBackends[event.Source] {
			// A move across services creates a copy and deletes the original:
			// guests would be cancelled and a series left behind.
			service := m.serviceLabel(before.Source)
			if service == "" {
				service = before.Source
			}
			switch {
			case before.Attendees != "":
				m.status = before.Summary + " has guests: change its calendar in " + service + " itself"
				return m, nil
			case before.Series:
				m.status = before.Summary + " repeats: change its calendar in " + service + " itself"
				return m, nil
			}
		}
	}
	m.eventForm = nil
	m.lastCalendar = event.Source
	if len(labels) > 0 {
		event.Attendees = strings.Join(append(splitList(event.Attendees), labels...), ", ")
	}
	if !form.editing {
		m.pendingSeq++
		event.ID = fmt.Sprintf("pending-%d", m.pendingSeq)
		return m.applyChange(pendingChange{op: "create", after: event, attendees: attendees, rrule: event.Rule})
	}
	if transfer {
		return m.applyChange(pendingChange{op: "transfer", before: before, after: event, notes: notesChanged})
	}
	change := pendingChange{op: "update", before: before, after: event, attendees: attendees, notes: notesChanged}
	if event.Summary == before.Summary && event.Location == before.Location && event.Start.Equal(before.Start) &&
		event.End.Equal(before.End) && event.AllDay == before.AllDay && !change.notes && len(attendees) == 0 {
		m.status = "Nothing changed"
		return m, nil
	}
	return m.applyChange(change)
}

func splitList(text string) []string {
	var items []string
	for _, item := range strings.Split(text, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}

// renderEventForm draws the form in the body.
func (m Model) renderEventForm(width, height int) string {
	form := m.eventForm
	label := lipgloss.NewStyle().Foreground(colorChrome)
	active := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
	cursor := lipgloss.NewStyle().Foreground(colorPrimary).Render("█")
	note := func(text string, ok bool) string {
		if ok {
			return lipgloss.NewStyle().Foreground(colorActive).Render(text)
		}
		return styleMuted.Render(text)
	}
	heading := "New event"
	if form.editing {
		heading = "Edit " + form.original.Summary
	}
	lines := []string{sectionHeader(truncateToWidth(heading, max(width-4, 1)), width), ""}
	row := func(field eventFormField, name, value, hint string, hintOK bool) {
		marker, nameStyle := "  ", label
		if form.field == field {
			marker, nameStyle = active.Render("› "), active
			if field != formCalendar {
				value += cursor
			}
		}
		line := marker + nameStyle.Render(padTo(name, 10)) + value
		if hint != "" {
			line += "   " + note(hint, hintOK)
		}
		lines = append(lines, truncateToWidth(line, width))
	}
	row(formTitle, "Title", form.title, "", false)

	start, end, allDay, timeErr := m.formTimes()
	whenHint := ""
	if timeErr != nil {
		whenHint = timeErr.Error()
		if strings.TrimSpace(form.when) == "" {
			whenHint = "fri 14:00 · tomorrow 9-11 · 12 nov all day · every mon 9:00"
		}
	} else {
		whenHint = describeSpan(start, end, allDay)
		if repeat := m.formRepeat(); repeat != nil {
			whenHint += " · ↻ " + repeat.Describe()
		}
		if clash := m.clashNote(form.original, start, end, allDay); clash != "" {
			whenHint += " · ⚠ " + clash
		}
	}
	row(formWhen, "When", form.when, whenHint, timeErr == nil)

	calendarValue := "‹ " + form.calendar
	if service := m.serviceLabel(form.calendar); service != "" {
		calendarValue += styleMuted.Render(" · " + service)
	}
	calendarValue += " ›"
	calendarHint := "←/→ " + strings.Join(m.writableCalendars, " · ")
	if form.editing && form.calendar != form.original.Source {
		calendarHint = "moves it from " + form.original.Source
		if from, to := m.serviceLabel(form.original.Source), m.serviceLabel(form.calendar); from != to && from != "" && to != "" {
			calendarHint += " (" + from + " to " + to + ")"
		}
	}
	row(formCalendar, "Calendar", calendarValue, calendarHint, form.editing && form.calendar != form.original.Source)
	row(formLocation, "Where", form.location, "", false)

	_, labels, inviteErr := m.resolveInvitees(form.invite)
	inviteHint := "names from the address book or addresses, comma-separated"
	inviteOK := false
	if inviteErr != nil {
		inviteHint = inviteErr.Error()
	} else if len(labels) > 0 {
		inviteHint, inviteOK = "invites "+strings.Join(labels, ", "), true
	}
	row(formInvite, "Invite", form.invite, inviteHint, inviteOK)
	if form.editing && form.original.Attendees != "" {
		lines = append(lines, "  "+label.Render(padTo("", 10))+styleMuted.Render(truncateToWidth("already: "+form.original.Attendees, max(width-12, 1))))
	}

	lines = append(lines, "")
	notesLabel := label.Render("Notes")
	if form.field == formNotes {
		notesLabel = active.Render("› Notes")
	}
	lines = append(lines, "  "+notesLabel+styleMuted.Render("   enter new line · ctrl+o in $EDITOR"))
	notes := form.notes
	if form.field == formNotes {
		notes += "█"
	}
	for _, line := range wrapText(notes, max(width-4, 10)) {
		if len(lines) >= height {
			break
		}
		lines = append(lines, "  "+line)
	}
	return strings.Join(lines, "\n")
}

func (m Model) renderEventFormFooter(width int) string {
	bindings := []helpBinding{{"tab ↑↓", "field"}, {"←→", "calendar"}, {"ctrl+s", "save"}, {"ctrl+o", "notes in $EDITOR"}, {"esc", "cancel"}}
	return renderRule(width, "") + "\n" + renderHelpBindings(bindings, width)
}

func (m Model) handleNotesEdited(message notesEditedMsg) (tea.Model, tea.Cmd) {
	if message.err != nil {
		m.status = "Editor: " + terminal.SanitizeLine(message.err.Error())
		return m, nil
	}
	if m.eventForm != nil {
		m.eventForm.notes = message.text
		m.eventForm.field = formNotes
	}
	return m, nil
}
