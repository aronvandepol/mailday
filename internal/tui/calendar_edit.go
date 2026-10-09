package tui

// Calendar editing: n adds an event from one typed line, m rebooks, e
// renames, d d deletes, < > move a day and + - half an hour. Changes show
// at once (marked pending) and go to Google or Exchange through
// mailday-calendar; the next calendar sync replaces the pending copy.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/aronvandepol/mailday/internal/calendar"
	"github.com/aronvandepol/mailday/internal/syncd"
	"github.com/aronvandepol/mailday/internal/terminal"
	"github.com/aronvandepol/mailday/internal/when"
)

type promptKind uint8

const (
	promptNew promptKind = iota
	promptMove
	promptEdit
)

type calendarPrompt struct {
	kind  promptKind
	input string
	event calendar.Event // the event being moved or edited
}

// pendingChange keeps a change on screen until a calendar load shows it.
type pendingChange struct {
	op        string // create, update, delete, transfer, respond
	before    calendar.Event
	after     calendar.Event
	expires   time.Time
	attendees []string         // addresses to invite
	notes     bool             // send the notes too
	answer    string           // respond: accept, tentative, decline
	rrule     string           // create: repeat rule
	series    bool             // delete: every occurrence
	removed   []calendar.Event // series delete: the other occurrences, to put back
	undoNote  string           // set when this change undoes another: shown when it is saved, and it cannot be undone itself
}

type calendarsLoadedMsg struct {
	names    []string
	backends map[string]string // name -> google or exchange
}

type calendarWriteMsg struct {
	change pendingChange
	id     string
	err    error
}

type nudgeCommitMsg struct{ seq int }

// nudgeDelay lets < > + - presses add up before one update is sent.
const nudgeDelay = 1200 * time.Millisecond

func calendarTool() string {
	if path, err := exec.LookPath("mailday-calendar"); err == nil {
		return path
	}
	return ""
}

func loadWritableCalendarsCmd() tea.Cmd {
	return func() tea.Msg {
		tool := calendarTool()
		if tool == "" {
			return calendarsLoadedMsg{}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		output, err := exec.CommandContext(ctx, tool, "calendars").Output()
		if err != nil {
			return calendarsLoadedMsg{}
		}
		var reply struct {
			Calendars []struct{ Name, Backend string } `json:"calendars"`
		}
		if json.Unmarshal(lastJSONLine(output), &reply) != nil {
			return calendarsLoadedMsg{}
		}
		message := calendarsLoadedMsg{backends: map[string]string{}}
		for _, calendar := range reply.Calendars {
			message.names = append(message.names, calendar.Name)
			message.backends[calendar.Name] = calendar.Backend
		}
		return message
	}
}

func lastJSONLine(output []byte) []byte {
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		if strings.HasPrefix(strings.TrimSpace(lines[index]), "{") {
			return []byte(lines[index])
		}
	}
	return output
}

// defaultCalendar is MAILDAY_CALENDAR, the last one used, or the first.
func (m Model) defaultCalendar() string {
	if m.lastCalendar != "" {
		return m.lastCalendar
	}
	if value := os.Getenv("MAILDAY_CALENDAR"); value != "" {
		for _, name := range m.writableCalendars {
			if strings.EqualFold(name, value) {
				return name
			}
		}
	}
	if len(m.writableCalendars) > 0 {
		return m.writableCalendars[0]
	}
	return ""
}

func (m Model) matchCalendar(typed string) (string, error) {
	if typed == "" {
		return m.defaultCalendar(), nil
	}
	var matches []string
	for _, name := range m.writableCalendars {
		if strings.EqualFold(name, typed) {
			return name, nil
		}
		if strings.HasPrefix(strings.ToLower(name), strings.ToLower(typed)) {
			matches = append(matches, name)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("#%s could be %s", typed, strings.Join(matches, " or "))
	}
	return "", fmt.Errorf("no calendar #%s; there is %s", typed, strings.Join(m.writableCalendars, ", "))
}

// handleCalendarKey takes the editing keys while the calendar has focus.
func (m Model) handleCalendarKey(key string) (Model, tea.Cmd, bool) {
	switch key {
	case "n":
		return m.startCalendarPrompt(promptNew), nil, true
	case "m":
		return m.startCalendarPrompt(promptMove), nil, true
	case "e":
		return m.openEventForm(true), nil, true
	case "N":
		return m.openEventForm(false), nil, true
	case "a":
		model, command := m.respond("accept")
		return model.(Model), command, true
	case "~":
		model, command := m.respond("tentative")
		return model.(Model), command, true
	case "x":
		model, command := m.respond("decline")
		return model.(Model), command, true
	case "d":
		model, command := m.deleteEvent(false)
		return model.(Model), command, true
	case "D":
		model, command := m.deleteEvent(true)
		return model.(Model), command, true
	case "J":
		model, command := m.joinMeeting()
		return model, command, true
	case "u":
		model, command := m.undoCalendar()
		return model.(Model), command, true
	case "<", ",":
		model, command := m.nudge(-24 * time.Hour)
		return model.(Model), command, true
	case ">", ".":
		model, command := m.nudge(24 * time.Hour)
		return model.(Model), command, true
	case "+", "=":
		model, command := m.nudge(30 * time.Minute)
		return model.(Model), command, true
	case "-", "_":
		model, command := m.nudge(-30 * time.Minute)
		return model.(Model), command, true
	}
	return m, nil, false
}

// joinMeeting opens the selected event's video-call link (J), from the
// calendar lists and from the event screen alike.
func (m Model) joinMeeting() (Model, tea.Cmd) {
	event, ok := m.selectedEvent()
	if !ok {
		m.status = "Select an event first"
		return m, nil
	}
	link := meetingLink(event)
	if link == "" {
		m.status = "This event has no meeting link"
		return m, nil
	}
	m.status = "Joining " + event.Summary
	return m, launchDetached(append(openCommand(), link), "Meeting link")
}

func (m Model) editableSelection() (calendar.Event, bool, string) {
	event, ok := m.selectedEvent()
	switch {
	case !ok:
		return event, false, "Select an event first"
	case event.Pending:
		return event, false, "Still saving the last change to this event"
	case event.Organizer != "":
		return event, false, "An invitation from " + event.Organizer + ": a accept · ~ maybe · x decline"
	case !event.Editable:
		return event, false, event.Source + " is read-only here"
	case !m.canWrite(event.Source):
		return event, false, "Mailday cannot write to " + event.Source
	}
	return event, true, ""
}

// serviceLabel names where a calendar lives, for the form.
func (m Model) serviceLabel(name string) string {
	switch m.calendarBackends[name] {
	case "google":
		return "Google"
	case "exchange":
		return "Outlook"
	}
	return ""
}

func (m Model) canWrite(name string) bool {
	for _, writable := range m.writableCalendars {
		if writable == name {
			return true
		}
	}
	return false
}

func (m Model) startCalendarPrompt(kind promptKind) Model {
	if len(m.writableCalendars) == 0 {
		m.status = "Calendar editing needs mailday-calendar (make install)"
		return m
	}
	prompt := &calendarPrompt{kind: kind}
	if kind != promptNew {
		event, ok, reason := m.editableSelection()
		if !ok {
			m.status = reason
			return m
		}
		prompt.event = event
		if kind == promptEdit {
			prompt.input = event.Summary
			if event.Location != "" {
				prompt.input += " @" + event.Location
			}
		}
	}
	m.calPrompt = prompt
	m.pendingDelete = ""
	m.status = ""
	return m
}

func (m Model) handleCalendarPromptKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	prompt := m.calPrompt
	switch key := message.String(); key {
	case "esc":
		m.calPrompt = nil
		m.status = ""
		return m, nil
	case "enter":
		return m.submitCalendarPrompt()
	case "tab":
		// The full form, with what the line said filled in.
		if prompt.kind == promptNew {
			return m.quickAddToForm(), nil
		}
		return m, nil
	case "shift+tab":
		// Cycle the calendar of a new event.
		if prompt.kind == promptNew && len(m.writableCalendars) > 0 {
			step := 1
			current := 0
			for index, name := range m.writableCalendars {
				if name == m.defaultCalendar() {
					current = index
				}
			}
			m.lastCalendar = m.writableCalendars[(current+step)%len(m.writableCalendars)]
		}
		return m, nil
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

// promptPreview says what Enter would do, or what is wrong with the input.
func (m Model) promptPreview() (string, bool) {
	prompt := m.calPrompt
	if prompt == nil {
		return "", false
	}
	switch prompt.kind {
	case promptNew:
		event, _, err := m.draftEvent(prompt.input)
		if err != nil {
			return err.Error(), false
		}
		if free, _, labels := m.wantsFree(prompt.input); free {
			return event.Summary + " · " + lengthLabel(event.End.Sub(event.Start)) + " · the first time you and " + strings.Join(labels, ", ") + " are free (asks Exchange on enter)", true
		}
		text := describeEvent(event)
		rest, _ := splitPeople(prompt.input)
		if parsed, err := when.Parse(rest, m.now()); err == nil && rolledToTomorrow(parsed, event.Start, m.quickAddDay()) {
			text += " · after 23:30, so tomorrow at 9:00"
		}
		if event.RuleText != "" {
			text += " · ↻ " + event.RuleText
		}
		if event.Attendees != "" {
			text += " · invites " + event.Attendees
		}
		if clash := m.clashNote(event, event.Start, event.End, event.AllDay); clash != "" {
			text += " · ⚠ " + clash
		}
		return text, true
	case promptMove:
		if free, _, labels := m.wantsFree(prompt.input); free {
			return "the first time you and " + strings.Join(labels, ", ") + " are free (asks Exchange on enter)", true
		}
		start, end, err := m.movedTimes(prompt.event, prompt.input)
		if err != nil {
			return err.Error(), false
		}
		text := describeSpan(start, end, prompt.event.AllDay) + "   was " + describeSpan(prompt.event.Start, prompt.event.End, prompt.event.AllDay)
		if clash := m.clashNote(prompt.event, start, end, prompt.event.AllDay); clash != "" {
			text += " · ⚠ " + clash
		}
		return text, true
	default:
		title, location := splitTitleLocation(prompt.input)
		if title == "" {
			return "type a title", false
		}
		text := title
		if location != "" {
			text += " · @" + location
		}
		return text, true
	}
}

// draftEvent reads the quick-add line; +name words invite people.
func (m Model) draftEvent(input string) (calendar.Event, []string, error) {
	rest, people := splitPeople(input)
	addresses, labels, err := m.resolveInvitees(strings.Join(people, ","))
	if err != nil {
		return calendar.Event{}, nil, err
	}
	parsed, err := when.Parse(rest, m.now())
	if err != nil {
		return calendar.Event{}, nil, err
	}
	calendarName, err := m.matchCalendar(parsed.Calendar)
	if err != nil {
		return calendar.Event{}, nil, err
	}
	start, end, err := when.Event(parsed, m.quickAddDay(), m.now())
	if err != nil {
		return calendar.Event{}, nil, err
	}
	event := calendar.Event{
		Summary:   parsed.Title,
		Location:  parsed.Location,
		Source:    calendarName,
		Start:     start,
		End:       end,
		AllDay:    parsed.AllDay,
		Editable:  true,
		Attendees: strings.Join(labels, ", "),
	}
	if parsed.Repeat != nil {
		event.Rule, event.RuleText, event.Series = parsed.Repeat.Rule(), parsed.Repeat.Describe(), true
	}
	return event, addresses, nil
}

func (m Model) movedTimes(event calendar.Event, input string) (time.Time, time.Time, error) {
	rest, _ := splitPeople(input)
	parsed, err := when.Parse(rest, m.now())
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if parsed.Free {
		return m.nextFree(event)
	}
	if event.AllDay && !parsed.HasTime && !parsed.HasEnd && (parsed.Length == 0 || parsed.Length >= 24*time.Hour) {
		return when.MoveAllDay(parsed, event.Start, event.End)
	}
	return when.Move(parsed, event.Start, event.End)
}

// nextFree finds the first gap as long as the event, on a weekday between
// 08:00 and 18:00, after the event's current start (or now, if later),
// among the events loaded for this view.
func (m Model) nextFree(event calendar.Event) (time.Time, time.Time, error) {
	return m.findFree(event, nil)
}

// span is a busy stretch of someone else's calendar.
type span struct{ start, end time.Time }

// findFree is the first gap as long as event, on a weekday between 08:00 and
// 18:00, from the event's start (or now, if later), free in your loaded
// calendar and in the others' busy times.
func (m Model) findFree(event calendar.Event, others []span) (time.Time, time.Time, error) {
	length := event.End.Sub(event.Start)
	if event.AllDay || length <= 0 {
		return time.Time{}, time.Time{}, errors.New("next free works for timed events")
	}
	from := event.Start
	if now := m.now(); now.After(from) {
		from = now.Truncate(15 * time.Minute).Add(15 * time.Minute)
	}
	busy := append([]span(nil), others...)
	for _, other := range m.events {
		if other.ID != event.ID && !other.AllDay && other.RSVP != "declined" && (event.RemoteID == "" || other.RemoteID != event.RemoteID) {
			busy = append(busy, span{other.Start, other.End})
		}
	}
	for candidate := from; candidate.Before(from.AddDate(0, 0, 14)); candidate = candidate.Add(15 * time.Minute) {
		day := dayStart(candidate)
		if candidate.Weekday() == time.Saturday || candidate.Weekday() == time.Sunday {
			continue
		}
		if candidate.Before(day.Add(8*time.Hour)) || candidate.Add(length).After(day.Add(18*time.Hour)) {
			continue
		}
		clash := false
		for _, other := range busy {
			if candidate.Before(other.end) && candidate.Add(length).After(other.start) {
				clash = true
				break
			}
		}
		if !clash {
			return candidate, candidate.Add(length), nil
		}
	}
	return time.Time{}, time.Time{}, errors.New("no common free slot in the next two weeks")
}

// splitPeople takes the +name words out of a prompt line.
func splitPeople(input string) (rest string, people []string) {
	var words []string
	for _, word := range strings.Fields(input) {
		if len(word) > 1 && word[0] == '+' && (word[1] < '0' || word[1] > '9') {
			people = append(people, word[1:])
			continue
		}
		words = append(words, word)
	}
	return strings.Join(words, " "), people
}

// wantsFree reports whether a prompt asks for a free time with other people.
func (m Model) wantsFree(input string) (bool, []string, []string) {
	rest, people := splitPeople(input)
	parsed, err := when.Parse(rest, m.now())
	if err != nil || !parsed.Free || len(people) == 0 {
		return false, nil, nil
	}
	addresses, labels, err := m.resolveInvitees(strings.Join(people, ","))
	if err != nil {
		return false, nil, nil
	}
	return true, addresses, labels
}

type freeBusyMsg struct {
	change pendingChange
	busy   []span
	missed []string // addresses Exchange had no free/busy for
	err    error
}

// freeBusyCmd asks Exchange when the attendees are busy over two weeks,
// then hands back the change to place in the first common gap.
func freeBusyCmd(change pendingChange, addresses []string, from time.Time) tea.Cmd {
	return func() tea.Msg {
		tool := calendarTool()
		if tool == "" {
			return freeBusyMsg{change: change, err: errors.New("mailday-calendar is not installed")}
		}
		args := []string{"freebusy", "--start", from.Format(time.RFC3339), "--end", from.AddDate(0, 0, 15).Format(time.RFC3339)}
		for _, address := range addresses {
			args = append(args, "--attendee", address)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		output, err := exec.CommandContext(ctx, tool, args...).Output()
		var reply struct {
			Busy   map[string][][2]string `json:"busy"`
			Errors map[string]string      `json:"errors"`
			Error  string                 `json:"error"`
		}
		_ = json.Unmarshal(lastJSONLine(output), &reply)
		if reply.Error != "" {
			return freeBusyMsg{change: change, err: errors.New(reply.Error)}
		}
		if err != nil {
			return freeBusyMsg{change: change, err: err}
		}
		message := freeBusyMsg{change: change}
		for _, spans := range reply.Busy {
			for _, pair := range spans {
				start, errStart := time.Parse(time.RFC3339, pair[0])
				end, errEnd := time.Parse(time.RFC3339, pair[1])
				if errStart == nil && errEnd == nil {
					message.busy = append(message.busy, span{start, end})
				}
			}
		}
		for address := range reply.Errors {
			message.missed = append(message.missed, address)
		}
		return message
	}
}

func (m Model) handleFreeBusy(message freeBusyMsg) (tea.Model, tea.Cmd) {
	m.calLooking = max(m.calLooking-1, 0)
	if message.err != nil {
		m.quitting = false
		m.status = "Free/busy failed: " + terminal.SanitizeLine(message.err.Error())
		return m, nil
	}
	change := message.change
	probe := change.after
	if change.op == "update" {
		probe = change.before
	}
	start, end, err := m.findFree(probe, message.busy)
	if err != nil {
		m.quitting = false
		m.status = err.Error()
		return m, nil
	}
	change.after.Start, change.after.End = start, end
	model, command := m.applyChange(change)
	if len(message.missed) > 0 {
		next := model.(Model)
		next.status += " · no free/busy for " + strings.Join(message.missed, ", ")
		return next, command
	}
	return model, command
}

// clashNote names what a proposed time overlaps, among the loaded events:
// timed ones, not declined, other than the event itself.
func (m Model) clashNote(self calendar.Event, start, end time.Time, allDay bool) string {
	if allDay {
		return ""
	}
	var clashes []calendar.Event
	for _, other := range m.events {
		if other.AllDay || other.RSVP == "declined" || other.ID == self.ID || (self.RemoteID != "" && other.RemoteID == self.RemoteID) {
			continue
		}
		if start.Before(other.End) && end.After(other.Start) {
			clashes = append(clashes, other)
		}
	}
	if len(clashes) == 0 {
		return ""
	}
	first := clashes[0]
	note := "overlaps " + first.Summary + " " + first.Start.Local().Format("15:04") + "–" + first.End.Local().Format("15:04")
	if len(clashes) > 1 {
		note += fmt.Sprintf(" and %d more", len(clashes)-1)
	}
	return note
}

func splitTitleLocation(input string) (string, string) {
	title, location, _ := strings.Cut(input, "@")
	return strings.TrimSpace(title), strings.TrimSpace(location)
}

func (m Model) submitCalendarPrompt() (tea.Model, tea.Cmd) {
	prompt := m.calPrompt
	switch prompt.kind {
	case promptNew:
		event, attendees, err := m.draftEvent(prompt.input)
		if err != nil {
			m.status = err.Error()
			return m, nil
		}
		m.calPrompt = nil
		m.lastCalendar = event.Source
		m.pendingSeq++
		event.ID = fmt.Sprintf("pending-%d", m.pendingSeq)
		event.Pending = true
		if free, addresses, labels := m.wantsFree(prompt.input); free {
			m.status = "Asking Exchange when " + strings.Join(labels, ", ") + " are free…"
			m.calLooking++
			return m, freeBusyCmd(pendingChange{op: "create", after: event, attendees: attendees, rrule: event.Rule}, addresses, event.Start)
		}
		return m.applyChange(pendingChange{op: "create", after: event, attendees: attendees, rrule: event.Rule})
	case promptMove:
		if free, addresses, labels := m.wantsFree(prompt.input); free {
			m.calPrompt = nil
			m.status = "Asking Exchange when " + strings.Join(labels, ", ") + " are free…"
			from := prompt.event.Start
			if m.now().After(from) {
				from = m.now()
			}
			m.calLooking++
			return m, freeBusyCmd(pendingChange{op: "update", before: prompt.event, after: prompt.event}, addresses, from)
		}
		start, end, err := m.movedTimes(prompt.event, prompt.input)
		if err != nil {
			m.status = err.Error()
			return m, nil
		}
		m.calPrompt = nil
		after := prompt.event
		after.Start, after.End = start, end
		return m.applyChange(pendingChange{op: "update", before: prompt.event, after: after})
	default:
		title, location := splitTitleLocation(prompt.input)
		if title == "" {
			m.status = "type a title"
			return m, nil
		}
		m.calPrompt = nil
		after := prompt.event
		after.Summary, after.Location = title, location
		return m.applyChange(pendingChange{op: "update", before: prompt.event, after: after})
	}
}

// respond answers the selected invitation.
func (m Model) respond(answer string) (tea.Model, tea.Cmd) {
	event, ok := m.selectedEvent()
	switch {
	case !ok:
		m.status = "Select an invitation first"
		return m, nil
	case event.Organizer == "" || event.RemoteID == "":
		m.status = "a ~ x answer invitations; this event is yours"
		return m, nil
	case !m.canWrite(event.Source):
		m.status = "Mailday cannot answer for " + event.Source
		return m, nil
	case event.Pending:
		m.status = "Still sending the last answer"
		return m, nil
	}
	after := event
	after.RSVP = map[string]string{"accept": "accepted", "tentative": "tentative", "decline": "declined"}[answer]
	if after.RSVP == event.RSVP {
		m.status = "Already " + after.RSVP
		return m, nil
	}
	return m.applyChange(pendingChange{op: "respond", before: event, after: after, answer: answer})
}

// deleteEvent removes the selected event after a second press; with series,
// every occurrence of a repeating event.
func (m Model) deleteEvent(series bool) (tea.Model, tea.Cmd) {
	event, ok, reason := m.editableSelection()
	if !ok {
		m.status = reason
		return m, nil
	}
	if series && !event.Series {
		m.status = "D deletes a repeating series; this event does not repeat (d d deletes it)"
		return m, nil
	}
	key := event.ID
	if series {
		key = "series:" + event.ID
	}
	if m.pendingDelete != key {
		m.pendingDelete = key
		if series {
			m.status = "Press D again to delete every " + event.Summary
		} else if event.Series {
			m.status = "Press d again to delete this " + event.Summary + " (D D deletes them all)"
		} else {
			m.status = "Press d again to delete " + event.Summary
		}
		return m, nil
	}
	m.pendingDelete = ""
	return m.applyChange(pendingChange{op: "delete", before: event, series: series})
}

// nudge moves the selected event by delta on screen at once and sends one
// update after the presses stop.
func (m Model) nudge(delta time.Duration) (tea.Model, tea.Cmd) {
	var flush tea.Cmd
	selected, _ := m.selectedEvent()
	if m.nudging != nil && m.nudging.after.ID != selected.ID {
		// Another event was being nudged: send that one now.
		previous := *m.nudging
		m.nudging = nil
		var model tea.Model
		model, flush = m.applyChange(previous)
		m = model.(Model)
	}
	if m.nudging == nil {
		event, ok, reason := m.editableSelection()
		if !ok {
			m.status = reason
			return m, flush
		}
		m.nudging = &pendingChange{op: "update", before: event, after: event}
	}
	if m.nudging.after.AllDay && delta%(24*time.Hour) != 0 {
		m.status = "All-day events move by days: < >"
		return m, flush
	}
	after := m.nudging.after
	if delta%(24*time.Hour) == 0 {
		// Whole days keep the wall clock of the machine's zone, which is
		// not the zone of a UTC start from Google or Exchange.
		days := int(delta / (24 * time.Hour))
		after.Start = after.Start.Local().AddDate(0, 0, days)
		after.End = after.End.Local().AddDate(0, 0, days)
	} else {
		after.Start = after.Start.Add(delta).Local()
		after.End = after.End.Add(delta).Local()
	}
	after.Pending = true
	m.nudging = &pendingChange{op: "update", before: m.nudging.before, after: after}
	m.replaceEvent(after.ID, after)
	m.nudgeSeq++
	m.status = "Moving to " + describeSpan(after.Start, after.End, after.AllDay) + " · keep pressing, it saves when you stop"
	seq := m.nudgeSeq
	return m, tea.Batch(flush, tea.Tick(nudgeDelay, func(time.Time) tea.Msg { return nudgeCommitMsg{seq: seq} }))
}

func (m Model) commitNudge(message nudgeCommitMsg) (tea.Model, tea.Cmd) {
	if m.nudging == nil || message.seq != m.nudgeSeq {
		return m, nil
	}
	return m.flushNudge()
}

// flushNudge sends the nudged event now, or puts it back when the presses
// cancelled out. It is also the quit path's way of not losing a nudge.
func (m Model) flushNudge() (tea.Model, tea.Cmd) {
	change := *m.nudging
	m.nudging = nil
	if change.after.Start.Equal(change.before.Start) && change.after.End.Equal(change.before.End) {
		m.replaceEvent(change.after.ID, change.before)
		m.status = ""
		return m, nil
	}
	return m.applyChange(change)
}

// applyChange shows the change, then sends it.
func (m Model) applyChange(change pendingChange) (tea.Model, tea.Cmd) {
	change.expires = m.now().Add(3 * time.Minute)
	change.after.Pending = true
	switch change.op {
	case "create":
		m.events = append(m.events, change.after)
		m.status = "Adding " + change.after.Summary + " to " + change.after.Source
	case "update":
		m.replaceEvent(change.before.ID, change.after)
		m.status = "Saving " + change.after.Summary
	case "transfer":
		m.replaceEvent(change.before.ID, change.after)
		m.status = "Moving " + change.after.Summary + " to " + change.after.Source
	case "respond":
		m.replaceEvent(change.before.ID, change.after)
		m.status = "Answering " + change.after.Organizer + ": " + change.after.RSVP
	case "delete":
		m.removeEvent(change.before.ID)
		if change.series {
			change.removed = m.removeSeries(change.before)
		}
		m.status = "Deleting " + change.before.Summary
		if m.screen == screenEvent {
			// The cursor now sits on the next event: do not show its details
			// as if they were the deleted one's.
			m.screen = screenHome
			m.detailScroll = 0
		}
	}
	m.sortEvents()
	if change.op != "delete" {
		m.selectEvent(change.after.ID)
	}
	m.boundCursors()
	m.calPending = append(m.calPending, change)
	m.recordUndo(change)
	m.calWriting++
	return m, writeCalendarCmd(change)
}

func writeCalendarCmd(change pendingChange) tea.Cmd {
	return func() tea.Msg {
		tool := calendarTool()
		if tool == "" {
			return calendarWriteMsg{change: change, err: errors.New("mailday-calendar is not installed")}
		}
		args := []string{change.op, "--calendar", change.after.Source}
		event := change.after
		switch change.op {
		case "delete":
			args = []string{"delete", "--calendar", change.before.Source, "--id", change.before.RemoteID}
			if change.series {
				args = append(args, "--series")
			}
		case "respond":
			args = []string{"respond", "--calendar", change.before.Source, "--id", change.before.RemoteID, "--answer", change.answer}
		case "transfer":
			args = []string{"transfer", "--calendar", change.before.Source, "--id", change.before.RemoteID, "--to", event.Source,
				"--title", event.Summary, "--location", event.Location}
			if change.notes {
				// Only what the user wrote: the copy Mailday holds is cut at
				// 4000 characters and has the Join line added.
				args = append(args, "--notes", editableNotes(event.Description))
			}
			if event.AllDay {
				args = append(args, "--all-day", "--start", event.Start.Format("2006-01-02"), "--end", event.End.Format("2006-01-02"))
			} else {
				args = append(args, "--start", event.Start.Format(time.RFC3339), "--end", event.End.Format(time.RFC3339))
			}
		default:
			if change.op == "update" {
				args = append(args, "--id", change.before.RemoteID)
			}
			if change.op == "create" || event.Summary != change.before.Summary {
				args = append(args, "--title", event.Summary)
			}
			if change.op == "create" || event.Location != change.before.Location {
				args = append(args, "--location", event.Location)
			}
			if change.op == "create" || !event.Start.Equal(change.before.Start) || !event.End.Equal(change.before.End) {
				if event.AllDay {
					args = append(args, "--all-day", "--start", event.Start.Format("2006-01-02"), "--end", event.End.Format("2006-01-02"))
				} else {
					args = append(args, "--start", event.Start.Format(time.RFC3339), "--end", event.End.Format(time.RFC3339))
				}
			}
			if change.op == "create" && event.Description != "" {
				args = append(args, "--notes", event.Description)
			} else if change.notes {
				args = append(args, "--notes", editableNotes(event.Description))
			}
			for _, address := range change.attendees {
				args = append(args, "--attendee", address)
			}
			if change.rrule != "" {
				args = append(args, "--rrule", change.rrule)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		output, err := exec.CommandContext(ctx, tool, args...).Output()
		var reply struct {
			ID    string `json:"id"`
			Error string `json:"error"`
		}
		_ = json.Unmarshal(lastJSONLine(output), &reply)
		if reply.Error != "" {
			return calendarWriteMsg{change: change, err: errors.New(reply.Error)}
		}
		if err != nil {
			return calendarWriteMsg{change: change, err: err}
		}
		refreshCalendarSoon()
		return calendarWriteMsg{change: change, id: reply.ID}
	}
}

// refreshCalendarSoon asks mailday-syncd to fetch the calendars now, or runs
// mailday-calsync itself when the daemon is not there.
func refreshCalendarSoon() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := syncd.Send(ctx, syncd.Request{Op: "calendar"}); err == nil {
		return
	}
	if path, err := exec.LookPath("mailday-calsync"); err == nil {
		command := exec.Command(path)
		if command.Start() == nil {
			go command.Wait()
		}
	}
}

func (m Model) handleCalendarWrite(message calendarWriteMsg) (tea.Model, tea.Cmd) {
	m.calWriting = max(m.calWriting-1, 0)
	model, command := m.applyCalendarWrite(message)
	next := model.(Model)
	if message.err != nil {
		next.quitting = false // a failed write must be read, not quit over
		return next, command
	}
	return next, tea.Batch(command, next.quitIfDone())
}

func (m Model) applyCalendarWrite(message calendarWriteMsg) (tea.Model, tea.Cmd) {
	change := message.change
	if message.err != nil {
		// Put things back the way the server still has them.
		m.dropPending(change)
		m.forgetUndoOf(change)
		switch change.op {
		case "create":
			m.removeEvent(change.after.ID)
		case "update", "transfer", "respond":
			m.replaceEvent(change.after.ID, change.before)
		case "delete":
			m.events = append(m.events, change.before)
			m.events = append(m.events, change.removed...)
		}
		m.sortEvents()
		m.boundCursors()
		m.status = "Not saved: " + terminal.SanitizeLine(message.err.Error())
		return m, nil
	}
	switch change.op {
	case "create":
		m.status = "Added " + change.after.Summary + " · " + describeSpan(change.after.Start, change.after.End, change.after.AllDay)
		if change.after.RuleText != "" {
			m.status += " · ↻ " + change.after.RuleText + " (the rest appear after the sync)"
		}
		// Give the pending copy its server id, so it can be moved before
		// the next sync.
		for index := range m.calPending {
			if m.calPending[index].after.ID == change.after.ID {
				m.calPending[index].after.RemoteID = message.id
			}
		}
		m.learnRemoteID(change, message.id)
	case "update":
		m.status = "Saved " + change.after.Summary + " · " + describeSpan(change.after.Start, change.after.End, change.after.AllDay)
		if len(change.attendees) > 0 {
			m.status += " · invitations sent"
		}
	case "transfer":
		m.status = "Moved " + change.after.Summary + " to " + change.after.Source
		for index := range m.calPending {
			if m.calPending[index].op == "transfer" && m.calPending[index].after.ID == change.after.ID {
				m.calPending[index].after.RemoteID = message.id
			}
		}
	case "respond":
		m.status = map[string]string{"accept": "Accepted", "tentative": "Said maybe to", "decline": "Declined"}[change.answer] + " " + change.after.Summary + " · " + change.after.Organizer + " is told"
	case "delete":
		m.status = "Deleted " + change.before.Summary
	}
	if change.undoNote != "" {
		m.status = change.undoNote
	}
	return m, nil
}

// overlayPending re-applies changes a fresh calendar load does not show yet,
// and forgets those it does show or that are too old. It returns the loaded
// ids of pending events the load now has, so the selection can follow them.
func (m *Model) overlayPending() map[string]string {
	confirmed := map[string]string{}
	var keep []pendingChange
	for _, change := range m.calPending {
		if m.now().After(change.expires) {
			continue
		}
		switch change.op {
		case "create":
			if index := m.findEvent(func(e calendar.Event) bool {
				return e.Source == change.after.Source && e.Summary == change.after.Summary && e.Start.Equal(change.after.Start)
			}); index >= 0 {
				confirmed[change.after.ID] = m.events[index].ID
				continue
			}
			m.events = append(m.events, change.after)
		case "update":
			index := m.findEvent(func(e calendar.Event) bool { return e.RemoteID == change.before.RemoteID })
			if index < 0 {
				continue
			}
			loaded := m.events[index]
			if loaded.Start.Equal(change.after.Start) && loaded.End.Equal(change.after.End) && loaded.Summary == change.after.Summary && loaded.Location == change.after.Location {
				// The id embeds the start, so a rebooked event has a new one:
				// the selection follows it.
				confirmed[change.before.ID] = loaded.ID
				continue
			}
			pending := change.after
			pending.ID = loaded.ID
			m.events[index] = pending
		case "delete":
			index := m.findEvent(func(e calendar.Event) bool { return e.RemoteID == change.before.RemoteID })
			if index < 0 {
				continue
			}
			m.events = append(m.events[:index], m.events[index+1:]...)
			if change.series {
				m.removeSeries(change.before)
			}
		case "transfer":
			old := m.findEvent(func(e calendar.Event) bool {
				return e.RemoteID == change.before.RemoteID && e.Source == change.before.Source
			})
			if old >= 0 {
				m.events = append(m.events[:old], m.events[old+1:]...)
			}
			arrived := m.findEvent(func(e calendar.Event) bool {
				return e.Source == change.after.Source && e.Summary == change.after.Summary && e.Start.Equal(change.after.Start)
			})
			if arrived >= 0 && old < 0 {
				confirmed[change.after.ID] = m.events[arrived].ID
				continue
			}
			if arrived < 0 {
				m.events = append(m.events, change.after)
			}
		case "respond":
			index := m.findEvent(func(e calendar.Event) bool { return e.RemoteID == change.before.RemoteID })
			if index < 0 {
				continue
			}
			if m.events[index].RSVP == change.after.RSVP {
				confirmed[change.before.ID] = m.events[index].ID
				continue
			}
			m.events[index].RSVP = change.after.RSVP
			m.events[index].Pending = true
		}
		keep = append(keep, change)
	}
	m.calPending = keep
	m.sortEvents()
	return confirmed
}

func (m *Model) dropPending(change pendingChange) {
	for index := range m.calPending {
		if m.calPending[index].after.ID == change.after.ID && m.calPending[index].op == change.op {
			m.calPending = append(m.calPending[:index], m.calPending[index+1:]...)
			return
		}
	}
}

func (m Model) findEvent(match func(calendar.Event) bool) int {
	for index, event := range m.events {
		if match(event) {
			return index
		}
	}
	return -1
}

func (m *Model) replaceEvent(id string, event calendar.Event) {
	if index := m.findEvent(func(e calendar.Event) bool { return e.ID == id }); index >= 0 {
		event.ID = id
		m.events[index] = event
	}
	m.sortEvents()
	m.selectEvent(id)
}

func (m *Model) removeEvent(id string) {
	if index := m.findEvent(func(e calendar.Event) bool { return e.ID == id }); index >= 0 {
		m.events = append(m.events[:index], m.events[index+1:]...)
	}
}

// removeSeries takes the other occurrences of a repeating event off the
// screen: they share its UID and calendar. It returns what it removed.
func (m *Model) removeSeries(event calendar.Event) []calendar.Event {
	var kept, removed []calendar.Event
	for _, other := range m.events {
		if other.UID == event.UID && other.Source == event.Source && event.UID != "" {
			removed = append(removed, other)
			continue
		}
		kept = append(kept, other)
	}
	m.events = kept
	return removed
}

func (m *Model) selectEvent(id string) {
	if index := m.findEvent(func(e calendar.Event) bool { return e.ID == id }); index >= 0 {
		m.eventCursor = index
	}
}

func (m *Model) sortEvents() {
	sort.SliceStable(m.events, func(i, j int) bool {
		if !m.events[i].Start.Equal(m.events[j].Start) {
			return m.events[i].Start.Before(m.events[j].Start)
		}
		return m.events[i].AllDay && !m.events[j].AllDay
	})
}

func describeSpan(start, end time.Time, allDay bool) string {
	start, end = start.Local(), end.Local()
	if allDay {
		last := end.AddDate(0, 0, -1)
		if !last.After(start) {
			return start.Format("Mon 2 Jan") + " all day"
		}
		return start.Format("Mon 2 Jan") + " – " + last.Format("Mon 2 Jan")
	}
	if sameDay(start, end) || end.Sub(start) <= 0 {
		return start.Format("Mon 2 Jan 15:04") + "–" + end.Format("15:04")
	}
	return start.Format("Mon 2 Jan 15:04") + " – " + end.Format("Mon 2 Jan 15:04")
}

func describeEvent(event calendar.Event) string {
	text := describeSpan(event.Start, event.End, event.AllDay) + " · " + event.Summary + " · " + event.Source
	if event.Location != "" {
		text += " · @" + event.Location
	}
	return text
}
