package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/aronvandepol/mailday/internal/calendar"
	"github.com/aronvandepol/mailday/internal/contacts"
	"github.com/charmbracelet/x/ansi"
)

func contactsEntry(name, address string) contacts.Contact {
	return contacts.Contact{Name: name, Address: address, Sent: 3}
}

func ansiStrip(text string) string { return ansi.Strip(text) }

// fakeCalendarTool puts a mailday-calendar on PATH that logs its arguments.
func fakeCalendarTool(t *testing.T, reply string) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := "#!/bin/sh\necho \"$*\" >> " + log + "\ncase \"$1\" in calendars) echo '{\"calendars\":[{\"name\":\"Work\"},{\"name\":\"University\"}]}';; *) echo '" + reply + "';; esac\n"
	os.WriteFile(filepath.Join(dir, "mailday-calendar"), []byte(script), 0o700)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MAILDAY_SYNCD_SOCKET", filepath.Join(dir, "none.sock"))
	return log
}

func calendarModel(t *testing.T, events ...calendar.Event) Model {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{Days: 14})
	now := time.Date(2026, 10, 6, 14, 10, 0, 0, time.Local)
	model.now = func() time.Time { return now }
	model.width, model.height = 140, 40
	model.focus = paneAgenda
	model.loadingCal, model.loadingMail = false, false
	model.events = events
	model.writableCalendars = []string{"Work", "University"}
	return model
}

func typeInto(t *testing.T, model Model, text string) Model {
	t.Helper()
	for _, r := range text {
		updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)}))
		model = updated.(Model)
	}
	return model
}

func run(t *testing.T, model Model, command tea.Cmd) Model {
	t.Helper()
	if command == nil {
		t.Fatal("expected a command")
	}
	updated, _ := model.Update(command())
	return updated.(Model)
}

func calls(t *testing.T, log string) string {
	data, _ := os.ReadFile(log)
	return strings.TrimSpace(string(data))
}

func TestNewEventFromOneLine(t *testing.T) {
	log := fakeCalendarTool(t, `{"ok":true,"id":"evt9"}`)
	model := calendarModel(t)
	updated, _ := model.Update(key("n"))
	model = typeInto(t, updated.(Model), "Lunch with Ada tomorrow 12:30 1h @Atrium #Uni")
	if preview, ok := model.promptPreview(); !ok || !strings.Contains(preview, "Wed 7 Oct 12:30–13:30") || !strings.Contains(preview, "University") {
		t.Fatalf("preview = %q", preview)
	}
	updated, command := model.Update(key("enter"))
	model = updated.(Model)
	if model.calPrompt != nil || len(model.events) != 1 || !model.events[0].Pending || model.events[0].Location != "Atrium" {
		t.Fatalf("events %+v", model.events)
	}
	model = run(t, model, command)
	if got := calls(t, log); got != "create --calendar University --title Lunch with Ada --location Atrium --start 2026-10-07T12:30:00+02:00 --end 2026-10-07T13:30:00+02:00" {
		t.Fatalf("call = %q", got)
	}
	if !strings.HasPrefix(model.status, "Added Lunch with Ada") || model.lastCalendar != "University" {
		t.Fatalf("status %q last %q", model.status, model.lastCalendar)
	}
	// The next load does not have it yet: the pending copy stays.
	updated, _ = model.Update(calendarLoadedMsg{result: calendar.Result{}})
	model = updated.(Model)
	if len(model.events) != 1 || model.events[0].RemoteID != "evt9" {
		t.Fatalf("pending copy lost: %+v", model.events)
	}
	// The load after the sync has it: no double.
	synced := calendar.Event{ID: "u/1", Summary: "Lunch with Ada", Source: "University", RemoteID: "evt9", Editable: true,
		Start: time.Date(2026, 10, 7, 12, 30, 0, 0, time.Local), End: time.Date(2026, 10, 7, 13, 30, 0, 0, time.Local)}
	updated, _ = model.Update(calendarLoadedMsg{result: calendar.Result{Events: []calendar.Event{synced}}})
	model = updated.(Model)
	if len(model.events) != 1 || model.events[0].Pending || len(model.calPending) != 0 {
		t.Fatalf("after sync %+v pending %d", model.events, len(model.calPending))
	}
}

func gym() calendar.Event {
	start := time.Date(2026, 10, 8, 18, 30, 0, 0, time.Local)
	return calendar.Event{ID: "g/1", Summary: "Gym", Source: "Work", RemoteID: "gym1", Editable: true, Start: start, End: start.Add(90 * time.Minute)}
}

func TestRebookAndNudge(t *testing.T) {
	log := fakeCalendarTool(t, `{"ok":true,"id":"gym1"}`)
	model := calendarModel(t, gym())
	updated, _ := model.Update(key("m"))
	model = typeInto(t, updated.(Model), "fri 17:00")
	updated, command := model.Update(key("enter"))
	model = run(t, updated.(Model), command)
	if got := calls(t, log); got != "update --calendar Work --id gym1 --start 2026-10-09T17:00:00+02:00 --end 2026-10-09T18:30:00+02:00" {
		t.Fatalf("call = %q", got)
	}
	// Pretend the sync caught up, then nudge: three presses, one update.
	moved := gym()
	moved.Start, moved.End = time.Date(2026, 10, 9, 17, 0, 0, 0, time.Local), time.Date(2026, 10, 9, 18, 30, 0, 0, time.Local)
	updated, _ = model.Update(calendarLoadedMsg{result: calendar.Result{Events: []calendar.Event{moved}}})
	model = updated.(Model)
	os.Truncate(log, 0)
	var commit tea.Cmd
	for _, k := range []string{">", "+", "+"} {
		updated, commit = model.Update(key(k))
		model = updated.(Model)
	}
	if got := model.events[0].Start; !got.Equal(time.Date(2026, 10, 10, 18, 0, 0, 0, time.Local)) {
		t.Fatalf("nudged to %s", got)
	}
	if calls(t, log) != "" {
		t.Fatal("nudges should wait before saving")
	}
	// Only the last tick commits; its batch holds the tick.
	model = runBatch(t, model, commit)
	if got := calls(t, log); got != "update --calendar Work --id gym1 --start 2026-10-10T18:00:00+02:00 --end 2026-10-10T19:30:00+02:00" {
		t.Fatalf("call = %q", got)
	}
}

// runBatch runs a command, following batches and feeding results back.
func runBatch(t *testing.T, model Model, command tea.Cmd) Model {
	t.Helper()
	if command == nil {
		return model
	}
	message := command()
	if batch, ok := message.(tea.BatchMsg); ok {
		for _, inner := range batch {
			model = runBatch(t, model, inner)
		}
		return model
	}
	if message == nil {
		return model
	}
	updated, next := model.Update(message)
	return runBatch(t, updated.(Model), next)
}

func TestInvitationsAndFailures(t *testing.T) {
	fakeCalendarTool(t, `{"error":"EWS: access denied"}`)
	invite := gym()
	invite.Organizer, invite.Editable = "Anouk", false
	model := calendarModel(t, invite)
	updated, _ := model.Update(key("m"))
	model = updated.(Model)
	if model.calPrompt != nil || !strings.Contains(model.status, "invitation from Anouk") {
		t.Fatalf("prompt %v status %q", model.calPrompt, model.status)
	}

	model = calendarModel(t, gym())
	updated, _ = model.Update(key("d"))
	model = updated.(Model)
	if len(model.events) != 1 || !strings.Contains(model.status, "Press d again") {
		t.Fatal("one d should only ask")
	}
	updated, command := model.Update(key("d"))
	model = updated.(Model)
	if len(model.events) != 0 {
		t.Fatal("d d should remove the event at once")
	}
	model = run(t, model, command)
	if len(model.events) != 1 || !strings.HasPrefix(model.status, "Not saved: EWS: access denied") {
		t.Fatalf("a failed delete should put it back: %+v %q", model.events, model.status)
	}
}

func TestGridLaysOutOverlapsSideBySide(t *testing.T) {
	day := time.Date(2026, 10, 8, 0, 0, 0, 0, time.Local)
	at := func(h, m int) time.Time { return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute) }
	events := []calendar.Event{
		{ID: "a", Start: at(9, 0), End: at(11, 0)},
		{ID: "b", Start: at(10, 0), End: at(10, 30)},
		{ID: "c", Start: at(10, 30), End: at(12, 0)},
		{ID: "d", Start: at(14, 0), End: at(15, 0)},
	}
	placed := layoutDay(events, day, at(8, 0), 30*time.Minute, 20)
	lanes := map[string][2]int{}
	for _, item := range placed {
		lanes[item.event.ID] = [2]int{item.lane, item.lanes}
	}
	want := map[string][2]int{"a": {0, 2}, "b": {1, 2}, "c": {1, 2}, "d": {0, 1}}
	for id, value := range want {
		if lanes[id] != value {
			t.Errorf("%s lane %v, want %v", id, lanes[id], value)
		}
	}
	if placed[0].startRow != 2 || placed[0].endRow != 6 {
		t.Errorf("09:00–11:00 at 30-minute rows from 08:00 = rows %d–%d", placed[0].startRow, placed[0].endRow)
	}
}

func TestFormMovesAnEventToAnotherCalendar(t *testing.T) {
	log := fakeCalendarTool(t, `{"ok":true,"id":"AAMk1"}`)
	model := calendarModel(t, gym())
	updated, _ := model.Update(key("e"))
	model = updated.(Model)
	if model.eventForm == nil || model.eventForm.when != "2026-10-08 18:30-20:00" {
		t.Fatalf("form %+v", model.eventForm)
	}
	// Down to Calendar, → to University, down to Where and type a place.
	for _, k := range []string{"tab", "tab", "right", "tab"} {
		updated, _ = model.Update(key(k))
		model = updated.(Model)
	}
	model = typeInto(t, model, "Sportcentrum")
	if !strings.Contains(ansiStrip(model.renderEventForm(120, 30)), "moves it from Work") {
		t.Fatal("the form should say the event moves")
	}
	updated, command := model.Update(key("ctrl+s"))
	model = updated.(Model)
	if model.eventForm != nil || model.events[0].Source != "University" || !model.events[0].Pending {
		t.Fatalf("after save %+v", model.events[0])
	}
	model = run(t, model, command)
	if got := calls(t, log); got != "transfer --calendar Work --id gym1 --to University --title Gym --location Sportcentrum --start 2026-10-08T18:30:00+02:00 --end 2026-10-08T20:00:00+02:00" {
		t.Fatalf("call = %q", got)
	}
	if !strings.HasPrefix(model.status, "Moved Gym to University") {
		t.Fatalf("status %q", model.status)
	}
	// The sync shows it in University and no longer in Work: one copy, selected.
	synced := gym()
	synced.ID, synced.Source, synced.RemoteID, synced.Location = "l/1", "University", "AAMk1", "Sportcentrum"
	updated, _ = model.Update(calendarLoadedMsg{result: calendar.Result{Events: []calendar.Event{synced}}})
	model = updated.(Model)
	if len(model.events) != 1 || model.events[0].Pending {
		t.Fatalf("after sync %+v", model.events)
	}
	if selected, _ := model.selectedEvent(); selected.ID != "l/1" {
		t.Fatalf("selection = %q", selected.ID)
	}
}

func TestFormNotesTimesAndInvites(t *testing.T) {
	log := fakeCalendarTool(t, `{"ok":true,"id":"gym1"}`)
	model := calendarModel(t, gym())
	model.book.Contacts = append(model.book.Contacts, contactsEntry("Ada Lovelace", "ada@uni.nl"))
	updated, _ := model.Update(key("e"))
	model = updated.(Model)
	model.eventForm.when = "fri 17:00-18:00"
	model.eventForm.notes = "Bring the shoes"
	model.eventForm.invite = "ada, bert@x.org"
	updated, command := model.Update(key("ctrl+s"))
	model = run(t, updated.(Model), command)
	want := "update --calendar Work --id gym1 --start 2026-10-09T17:00:00+02:00 --end 2026-10-09T18:00:00+02:00 --notes Bring the shoes --attendee ada@uni.nl --attendee bert@x.org"
	if got := calls(t, log); got != want {
		t.Fatalf("call =\n%q\nwant\n%q", got, want)
	}
	if !strings.Contains(model.status, "invitations sent") {
		t.Fatalf("status %q", model.status)
	}
	// Saving left it pending; e waits for the server.
	updated, _ = model.Update(key("e"))
	if updated.(Model).eventForm != nil {
		t.Fatal("a pending event should not open")
	}
	model = calendarModel(t, gym())
	updated, _ = model.Update(key("e"))
	model = updated.(Model)
	model.eventForm.invite = "nobody-known"
	if _, _, _, err := model.formResult(); err == nil || !strings.Contains(err.Error(), "address book") {
		t.Fatalf("unknown invitee: %v", err)
	}
}

func TestAnsweringInvitations(t *testing.T) {
	log := fakeCalendarTool(t, `{"ok":true,"id":"inv1","answer":"accepted"}`)
	invite := gym()
	invite.Summary, invite.Organizer, invite.Editable, invite.RSVP, invite.RemoteID = "Board meeting", "Anouk", false, "needs-action", "inv1"
	model := calendarModel(t, invite)
	updated, _ := model.Update(key("e"))
	model = updated.(Model)
	if model.eventForm != nil || !strings.Contains(model.status, "a accept") {
		t.Fatalf("an invitation is not editable: %q", model.status)
	}
	updated, command := model.Update(key("a"))
	model = updated.(Model)
	if model.events[0].RSVP != "accepted" || !model.events[0].Pending {
		t.Fatalf("not shown at once: %+v", model.events[0])
	}
	model = run(t, model, command)
	if got := calls(t, log); got != "respond --calendar Work --id inv1 --answer accept" {
		t.Fatalf("call %q", got)
	}
	if !strings.Contains(model.status, "Accepted Board meeting · Anouk is told") {
		t.Fatalf("status %q", model.status)
	}
	// Own events have nothing to answer.
	model = calendarModel(t, gym())
	updated, _ = model.Update(key("x"))
	if !strings.Contains(updated.(Model).status, "this event is yours") {
		t.Fatal("x on an own event should explain")
	}
}

func TestQuickAddInvites(t *testing.T) {
	fakeCalendarTool(t, `{"ok":true,"id":"n1"}`)
	model := calendarModel(t)
	model.book.Contacts = append(model.book.Contacts, contactsEntry("Ada Lovelace", "ada@uni.nl"))
	event, attendees, err := model.draftEvent("Lunch with Ada tomorrow 12:30 +ada")
	if err != nil || event.Summary != "Lunch with Ada" || len(attendees) != 1 || attendees[0] != "ada@uni.nl" {
		t.Fatalf("event %+v attendees %v err %v", event, attendees, err)
	}
	if _, _, err := model.draftEvent("Thing +1d +zed"); err == nil {
		t.Fatal("+zed is nobody")
	}
}

func TestClashWarnings(t *testing.T) {
	fakeCalendarTool(t, `{"ok":true}`)
	model := calendarModel(t, gym())
	updated, _ := model.Update(key("n"))
	model = typeInto(t, updated.(Model), "Call thu 19:00 30m")
	if preview, _ := model.promptPreview(); !strings.Contains(preview, "overlaps Gym 18:30–20:00") {
		t.Fatalf("preview = %q", preview)
	}
	model.calPrompt.input = "Call thu 20:00 30m"
	if preview, _ := model.promptPreview(); strings.Contains(preview, "overlaps") {
		t.Fatalf("no clash expected: %q", preview)
	}
	// Rebooking an event does not clash with itself.
	model = calendarModel(t, gym())
	updated, _ = model.Update(key("m"))
	model = typeInto(t, updated.(Model), "+30m")
	if preview, _ := model.promptPreview(); strings.Contains(preview, "overlaps") {
		t.Fatalf("clashed with itself: %q", preview)
	}
}

func TestRepeatingEvents(t *testing.T) {
	log := fakeCalendarTool(t, `{"ok":true,"id":"r1"}`)
	model := calendarModel(t)
	updated, _ := model.Update(key("n"))
	model = typeInto(t, updated.(Model), "Standup every weekday 9:15 15m #Work")
	if preview, _ := model.promptPreview(); !strings.Contains(preview, "↻ every weekday") {
		t.Fatalf("preview %q", preview)
	}
	updated, command := model.Update(key("enter"))
	model = run(t, updated.(Model), command)
	if got := calls(t, log); !strings.HasSuffix(got, "--rrule FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR") {
		t.Fatalf("call %q", got)
	}

	// D D deletes every occurrence; d d just this one.
	os.Truncate(log, 0)
	one, two := gym(), gym()
	one.UID, two.UID, one.Series, two.Series = "series@x", "series@x", true, true
	two.ID, two.RemoteID = "g/2", "gym1_2"
	two.Start, two.End = two.Start.AddDate(0, 0, 7), two.End.AddDate(0, 0, 7)
	model = calendarModel(t, one, two)
	updated, _ = model.Update(key("D"))
	updated, command = updated.(Model).Update(key("D"))
	model = updated.(Model)
	if len(model.events) != 0 {
		t.Fatalf("D D should clear the series: %d left", len(model.events))
	}
	model = run(t, model, command)
	if got := calls(t, log); got != "delete --calendar Work --id gym1 --series" {
		t.Fatalf("call %q", got)
	}
}

func TestNextFreeWithColleagues(t *testing.T) {
	// Anouk is busy 09:00–11:00 local on Wednesday the 7th (07:00–09:00 UTC).
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	script := "#!/bin/sh\necho \"$*\" >> " + log + "\ncase \"$1\" in freebusy) echo '{\"ok\":true,\"busy\":{\"anouk@uni.nl\":[[\"2026-10-07T07:00:00Z\",\"2026-10-07T09:00:00Z\"]]},\"errors\":{}}';; *) echo '{\"ok\":true,\"id\":\"m1\"}';; esac\n"
	os.WriteFile(filepath.Join(dir, "mailday-calendar"), []byte(script), 0o700)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MAILDAY_SYNCD_SOCKET", filepath.Join(dir, "none.sock"))

	model := calendarModel(t)
	model.book.Contacts = append(model.book.Contacts, contactsEntry("Anouk Roos", "anouk@uni.nl"))
	updated, _ := model.Update(key("n"))
	model = typeInto(t, updated.(Model), "Plan with Anouk tomorrow 9:30 1h next free +anouk #University")
	if preview, _ := model.promptPreview(); !strings.Contains(preview, "you and Anouk Roos <anouk@uni.nl> are free") {
		t.Fatalf("preview %q", preview)
	}
	updated, command := model.Update(key("enter"))
	model = updated.(Model)
	if !strings.Contains(model.status, "Asking Exchange") || len(model.events) != 0 {
		t.Fatalf("should ask first: %q", model.status)
	}
	model = run(t, model, command) // free/busy → create
	if len(model.events) != 1 {
		t.Fatalf("events %+v", model.events)
	}
	got := model.events[0]
	if got.Start.Format("2006-01-02 15:04") != "2026-10-07 11:00" || got.End.Format("15:04") != "12:00" {
		t.Fatalf("placed at %s–%s, want after Anouk's 09:00–11:00", got.Start, got.End)
	}
}

func TestQuickAddBox(t *testing.T) {
	fakeCalendarTool(t, `{"ok":true,"id":"q1"}`)
	model := calendarModel(t)
	model.book.Contacts = append(model.book.Contacts, contactsEntry("Ada Lovelace", "ada@uni.nl"))
	updated, _ := model.Update(key("n"))
	model = typeInto(t, updated.(Model), "Plan chapter tomorrow 13:30 for 1h +ada +zed @Atrium #uni")
	words, roles := model.inputRoles(model.calPrompt.input)
	got := map[string]string{}
	for index, word := range words {
		got[word] = roles[index]
	}
	want := map[string]string{"Plan": "title", "tomorrow": "date", "13:30": "time", "for": "length", "1h": "length",
		"+ada": "people", "+zed": "error", "@Atrium": "location", "#uni": "calendar"}
	for word, role := range want {
		if got[word] != role {
			t.Errorf("%s = %q, want %q", word, got[word], role)
		}
	}
	box := ansiStrip(model.renderQuickAdd(90))
	for _, expected := range []string{"Title     Plan chapter", "Wed 7 Oct  13:30–14:30  (1 h)", "Calendar  University", "Where     Atrium", "nobody called \"zed\""} {
		if !strings.Contains(box, expected) {
			t.Errorf("box lacks %q:\n%s", expected, box)
		}
	}
	// tab carries everything over to the full form.
	model.calPrompt.input = "Plan chapter tomorrow 13:30 for 1h +ada @Atrium #uni"
	updated, _ = model.Update(key("tab"))
	model = updated.(Model)
	form := model.eventForm
	if form == nil || form.title != "Plan chapter" || form.when != "tomorrow 13:30 for 1h" || form.location != "Atrium" || form.invite != "ada" || form.calendar != "University" {
		t.Fatalf("form %+v", form)
	}
	// The overlay keeps every line as wide as the screen.
	base := strings.Repeat(strings.Repeat("·", 60)+"\n", 20)
	for _, line := range strings.Split(overlay(strings.TrimSuffix(base, "\n"), "┌──┐\n│hi│\n└──┘", 60, 20), "\n") {
		if width := ansi.StringWidth(line); width != 60 {
			t.Fatalf("line width %d: %q", width, line)
		}
	}
}
