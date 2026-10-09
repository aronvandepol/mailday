package tui

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/aronvandepol/mailday/internal/calendar"
)

// undoPress sends a key and runs what it asks for, feeding the results back.
func undoPress(t *testing.T, model Model, k string) Model {
	t.Helper()
	updated, command := model.Update(key(k))
	return runBatch(t, updated.(Model), command)
}

func TestUndoCreateDeletesTheNewEvent(t *testing.T) {
	log := fakeCalendarTool(t, `{"ok":true,"id":"evt9"}`)
	model := calendarModel(t)
	updated, _ := model.Update(key("n"))
	model = typeInto(t, updated.(Model), "Lunch tomorrow 12:30 #University")
	updated, command := model.Update(key("enter"))
	model = run(t, updated.(Model), command)
	os.Truncate(log, 0)

	model = undoPress(t, model, "u")
	if got := calls(t, log); got != "delete --calendar University --id evt9" {
		t.Fatalf("call = %q", got)
	}
	if len(model.events) != 0 || model.status != "Undone: removed Lunch" {
		t.Fatalf("events %+v status %q", model.events, model.status)
	}
	// Only one step: a second u has nothing left.
	model = undoPress(t, model, "u")
	if model.status != "Nothing to undo in the calendar" {
		t.Fatalf("status %q", model.status)
	}
}

func TestUndoRepeatingCreateDeletesTheSeries(t *testing.T) {
	log := fakeCalendarTool(t, `{"ok":true,"id":"evt9"}`)
	model := calendarModel(t)
	updated, _ := model.Update(key("n"))
	model = typeInto(t, updated.(Model), "Standup 9:00 every mon #University")
	updated, command := model.Update(key("enter"))
	model = run(t, updated.(Model), command)
	os.Truncate(log, 0)
	undoPress(t, model, "u")
	if got := calls(t, log); got != "delete --calendar University --id evt9 --series" {
		t.Fatalf("call = %q", got)
	}
}

func TestUndoBeforeTheSaveReturnsAsksToWait(t *testing.T) {
	log := fakeCalendarTool(t, `{"ok":true,"id":"evt9"}`)
	model := calendarModel(t)
	updated, _ := model.Update(key("n"))
	model = typeInto(t, updated.(Model), "Lunch tomorrow 12:30 #University")
	updated, _ = model.Update(key("enter")) // the write is not run
	model = updated.(Model)
	model = undoPress(t, model, "u")
	if !strings.Contains(model.status, "Still saving") || calls(t, log) != "" {
		t.Fatalf("status %q calls %q", model.status, calls(t, log))
	}
}

func TestUndoRebookPutsTheTimesBack(t *testing.T) {
	log := fakeCalendarTool(t, `{"ok":true,"id":"gym1"}`)
	model := calendarModel(t, gym())
	updated, _ := model.Update(key("m"))
	model = typeInto(t, updated.(Model), "fri 17:00")
	updated, command := model.Update(key("enter"))
	model = run(t, updated.(Model), command)
	os.Truncate(log, 0)

	model = undoPress(t, model, "u")
	if got := calls(t, log); got != "update --calendar Work --id gym1 --start 2026-10-08T18:30:00+02:00 --end 2026-10-08T20:00:00+02:00" {
		t.Fatalf("call = %q", got)
	}
	if !strings.HasPrefix(model.status, "Undone: Gym is back to Thu 8 Oct 18:30–20:00") {
		t.Fatalf("status %q", model.status)
	}
	if got := model.events[0].Start; !got.Equal(gym().Start) {
		t.Fatalf("shown at %s", got)
	}
}

func TestUndoNudgeAfterItWasSent(t *testing.T) {
	log := fakeCalendarTool(t, `{"ok":true,"id":"gym1"}`)
	model := calendarModel(t, gym())
	var commit tea.Cmd
	for _, k := range []string{">", "+"} {
		updated, c := model.Update(key(k))
		model, commit = updated.(Model), c
	}
	model = runBatch(t, model, commit)
	os.Truncate(log, 0)
	model = undoPress(t, model, "u")
	if got := calls(t, log); got != "update --calendar Work --id gym1 --start 2026-10-08T18:30:00+02:00 --end 2026-10-08T20:00:00+02:00" {
		t.Fatalf("call = %q", got)
	}
}

func TestUndoNudgeThatWasNotSentYetJustCancelsIt(t *testing.T) {
	log := fakeCalendarTool(t, `{"ok":true,"id":"gym1"}`)
	model := calendarModel(t, gym())
	updated, _ := model.Update(key(">"))
	model = updated.(Model)
	model = undoPress(t, model, "u")
	if model.nudging != nil || !model.events[0].Start.Equal(gym().Start) || model.events[0].Pending {
		t.Fatalf("not put back: %+v", model.events[0])
	}
	if calls(t, log) != "" || !strings.HasPrefix(model.status, "Undone:") {
		t.Fatalf("calls %q status %q", calls(t, log), model.status)
	}
}

func TestUndoRenameKeepsNotesUnlessTheyChanged(t *testing.T) {
	log := fakeCalendarTool(t, `{"ok":true,"id":"gym1"}`)
	event := gym()
	event.Description = "Bring a towel"
	model := calendarModel(t, event)
	updated, _ := model.Update(key("e"))
	model = updated.(Model)
	model.eventForm.title += " class"
	updated, command := model.Update(key("ctrl+s"))
	model = run(t, updated.(Model), command)
	if !strings.Contains(calls(t, log), "--title Gym class") {
		t.Fatalf("setup call = %q", calls(t, log))
	}
	os.Truncate(log, 0)
	model = undoPress(t, model, "u")
	if got := calls(t, log); got != "update --calendar Work --id gym1 --title Gym" {
		t.Fatalf("call = %q (no times, no notes)", got)
	}
	if model.events[0].Summary != "Gym" {
		t.Fatalf("title %q", model.events[0].Summary)
	}
}

func TestUndoNotesChangeSendsTheOldNotes(t *testing.T) {
	log := fakeCalendarTool(t, `{"ok":true,"id":"gym1"}`)
	event := gym()
	event.Description = "Bring a towel"
	model := calendarModel(t, event)
	updated, _ := model.Update(key("e"))
	model = updated.(Model)
	model.eventForm.notes = "Bring two towels"
	updated, command := model.Update(key("ctrl+s"))
	model = run(t, updated.(Model), command)
	os.Truncate(log, 0)
	undoPress(t, model, "u")
	if got := calls(t, log); got != "update --calendar Work --id gym1 --notes Bring a towel" {
		t.Fatalf("call = %q", got)
	}
}

func TestUndoDeleteRecreatesTheEvent(t *testing.T) {
	log := fakeCalendarTool(t, `{"ok":true,"id":"new7"}`)
	event := gym()
	event.Location, event.Description, event.Attendees = "Sportcentrum", "Join: https://meet.example/x\n\nBring a towel", "Ada"
	model := calendarModel(t, event)
	model = undoPress(t, model, "d")
	model = undoPress(t, model, "d")
	if len(model.events) != 0 {
		t.Fatal("the delete did not happen")
	}
	os.Truncate(log, 0)
	model = undoPress(t, model, "u")
	want := "create --calendar Work --title Gym --location Sportcentrum --start 2026-10-08T18:30:00+02:00 --end 2026-10-08T20:00:00+02:00 --notes Bring a towel"
	if got := calls(t, log); got != want {
		t.Fatalf("call = %q", got)
	}
	if model.status != "Undone: Gym restored as a new event; guests are not re-invited" {
		t.Fatalf("status %q", model.status)
	}
	if len(model.events) != 1 || model.events[0].Attendees != "" || model.events[0].RemoteID != "" {
		t.Fatalf("events %+v", model.events)
	}
}

func TestUndoRespondGivesThePreviousAnswer(t *testing.T) {
	log := fakeCalendarTool(t, `{"ok":true,"id":"inv1"}`)
	invite := gym()
	invite.Summary, invite.Organizer, invite.Editable, invite.RSVP, invite.RemoteID = "Board", "Anouk", false, "tentative", "inv1"
	model := calendarModel(t, invite)
	model = undoPress(t, model, "a")
	os.Truncate(log, 0)
	model = undoPress(t, model, "u")
	if got := calls(t, log); got != "respond --calendar Work --id inv1 --answer tentative" {
		t.Fatalf("call = %q", got)
	}
	if model.events[0].RSVP != "tentative" || !strings.HasPrefix(model.status, "Undone: answer to Board") {
		t.Fatalf("rsvp %q status %q", model.events[0].RSVP, model.status)
	}
}

func TestUndoRespondRefusesWhenNeverAnswered(t *testing.T) {
	log := fakeCalendarTool(t, `{"ok":true,"id":"inv1"}`)
	invite := gym()
	invite.Organizer, invite.Editable, invite.RSVP, invite.RemoteID = "Anouk", false, "needs-action", "inv1"
	model := calendarModel(t, invite)
	model = undoPress(t, model, "a")
	os.Truncate(log, 0)
	model = undoPress(t, model, "u")
	if calls(t, log) != "" || !strings.Contains(model.status, "had not answered") {
		t.Fatalf("calls %q status %q", calls(t, log), model.status)
	}
}

func TestUndoRefusesATransfer(t *testing.T) {
	log := fakeCalendarTool(t, `{"ok":true,"id":"AAMk1"}`)
	model := calendarModel(t, gym())
	model.calendarBackends = map[string]string{"Work": "google", "University": "google"}
	updated, _ := model.Update(key("e"))
	model = updated.(Model)
	model.eventForm.calendar = "University"
	updated, command := model.Update(key("ctrl+s"))
	model = run(t, updated.(Model), command)
	if !strings.HasPrefix(calls(t, log), "transfer") {
		t.Fatalf("setup call = %q", calls(t, log))
	}
	os.Truncate(log, 0)
	model = undoPress(t, model, "u")
	if calls(t, log) != "" || !strings.Contains(model.status, "another calendar") {
		t.Fatalf("calls %q status %q", calls(t, log), model.status)
	}
}

func TestUndoExpiresAfterTenMinutes(t *testing.T) {
	log := fakeCalendarTool(t, `{"ok":true,"id":"gym1"}`)
	model := calendarModel(t, gym())
	model = undoPress(t, model, "d")
	model = undoPress(t, model, "d")
	os.Truncate(log, 0)
	later := model.now().Add(10*time.Minute + time.Second)
	model.now = func() time.Time { return later }
	model = undoPress(t, model, "u")
	if calls(t, log) != "" || !strings.HasPrefix(model.status, "Too late") {
		t.Fatalf("calls %q status %q", calls(t, log), model.status)
	}
	// Just inside the window it still works.
	model = calendarModel(t, gym())
	model = undoPress(t, model, "d")
	model = undoPress(t, model, "d")
	inside := model.now().Add(9*time.Minute + 59*time.Second)
	model.now = func() time.Time { return inside }
	model = undoPress(t, model, "u")
	if !strings.HasPrefix(model.status, "Undone:") {
		t.Fatalf("status %q", model.status)
	}
}

func TestUndoIsForgottenWhenTheServerRefusedTheChange(t *testing.T) {
	fakeCalendarTool(t, `{"error":"EWS: access denied"}`)
	model := calendarModel(t, gym())
	model = undoPress(t, model, "d")
	model = undoPress(t, model, "d")
	if !strings.HasPrefix(model.status, "Not saved") {
		t.Fatalf("status %q", model.status)
	}
	model = undoPress(t, model, "u")
	if model.status != "Nothing to undo in the calendar" {
		t.Fatalf("status %q", model.status)
	}
}

func TestUndoKeyInTheMailPaneLeavesTheCalendarAlone(t *testing.T) {
	log := fakeCalendarTool(t, `{"ok":true,"id":"gym1"}`)
	model := calendarModel(t, gym())
	model = undoPress(t, model, "d")
	model = undoPress(t, model, "d")
	os.Truncate(log, 0)
	model.focus = paneMail
	model = undoPress(t, model, "u")
	if calls(t, log) != "" || model.calUndo == nil {
		t.Fatalf("calls %q undo %v", calls(t, log), model.calUndo)
	}
}

func TestUndoIsNotUndoable(t *testing.T) {
	fakeCalendarTool(t, `{"ok":true,"id":"gym1"}`)
	model := calendarModel(t, gym())
	model = undoPress(t, model, "d")
	model = undoPress(t, model, "d")
	model = undoPress(t, model, "u")
	model = undoPress(t, model, "u")
	if model.status != "Nothing to undo in the calendar" {
		t.Fatalf("status %q", model.status)
	}
}

// elsewhere is another week's event: what the list holds once the calendar
// has moved on and the changed event is no longer loaded.
func elsewhere() calendar.Event {
	start := time.Date(2026, 10, 20, 9, 0, 0, 0, time.Local)
	return calendar.Event{ID: "x/1", Summary: "Seminar", Source: "University", RemoteID: "sem1", Editable: true, Start: start, End: start.Add(time.Hour)}
}

func TestUndoRebookAfterNavigatingToAnotherWeekSendsTheOldTimes(t *testing.T) {
	log := fakeCalendarTool(t, `{"ok":true,"id":"gym1"}`)
	model := calendarModel(t, gym())
	updated, _ := model.Update(key("m"))
	model = typeInto(t, updated.(Model), "fri 17:00")
	updated, command := model.Update(key("enter"))
	model = run(t, updated.(Model), command)

	model.events = []calendar.Event{elsewhere()} // another week is loaded
	os.Truncate(log, 0)
	model = undoPress(t, model, "u")
	if got := calls(t, log); got != "update --calendar Work --id gym1 --start 2026-10-08T18:30:00+02:00 --end 2026-10-08T20:00:00+02:00" {
		t.Fatalf("call = %q", got)
	}
	if len(model.events) != 1 || model.events[0].Summary != "Seminar" {
		t.Fatalf("the list gained or lost events: %+v", model.events)
	}
	if !strings.HasPrefix(model.status, "Undone: Gym is back to Thu 8 Oct 18:30") {
		t.Fatalf("status %q", model.status)
	}
}

func TestUndoNudgeAfterNavigatingAwaySendsTheOldTimes(t *testing.T) {
	log := fakeCalendarTool(t, `{"ok":true,"id":"gym1"}`)
	model := calendarModel(t, gym())
	var commit tea.Cmd
	for _, k := range []string{">", "+"} {
		updated, c := model.Update(key(k))
		model, commit = updated.(Model), c
	}
	model = runBatch(t, model, commit)
	model.events = []calendar.Event{elsewhere()}
	os.Truncate(log, 0)
	model = undoPress(t, model, "u")
	if got := calls(t, log); got != "update --calendar Work --id gym1 --start 2026-10-08T18:30:00+02:00 --end 2026-10-08T20:00:00+02:00" {
		t.Fatalf("call = %q", got)
	}
}

func TestUndoRenameAfterNavigatingAwaySendsOnlyTheTitle(t *testing.T) {
	log := fakeCalendarTool(t, `{"ok":true,"id":"gym1"}`)
	event := gym()
	event.Description = "Bring a towel"
	model := calendarModel(t, event)
	updated, _ := model.Update(key("e"))
	model = updated.(Model)
	model.eventForm.title += " class"
	updated, command := model.Update(key("ctrl+s"))
	model = run(t, updated.(Model), command)

	model.events = nil
	os.Truncate(log, 0)
	model = undoPress(t, model, "u")
	if got := calls(t, log); got != "update --calendar Work --id gym1 --title Gym" {
		t.Fatalf("call = %q (no times, no place, no notes)", got)
	}
	if len(model.events) != 0 {
		t.Fatalf("an off-screen undo put the event on screen: %+v", model.events)
	}
}

func TestUndoRenameKeepsWhatASyncChangedOnScreen(t *testing.T) {
	log := fakeCalendarTool(t, `{"ok":true,"id":"gym1"}`)
	model := calendarModel(t, gym())
	updated, _ := model.Update(key("e"))
	model = updated.(Model)
	model.eventForm.title += " class"
	updated, command := model.Update(key("ctrl+s"))
	model = run(t, updated.(Model), command)

	// The sync brings the event back as the organiser has since changed it.
	synced := gym()
	synced.ID, synced.Summary, synced.Location = "g/2", "Gym class", "Room 4"
	synced.Start, synced.End = synced.Start.Add(time.Hour), synced.End.Add(time.Hour)
	model.events = []calendar.Event{synced}
	os.Truncate(log, 0)
	model = undoPress(t, model, "u")
	if got := calls(t, log); got != "update --calendar Work --id gym1 --title Gym" {
		t.Fatalf("call = %q: the organiser's place and time are not ours to undo", got)
	}
	if got := model.events[0]; got.Summary != "Gym" || got.Location != "Room 4" || !got.Start.Equal(synced.Start) {
		t.Fatalf("event after undo = %+v", got)
	}
}

func TestUndoOffScreenWithoutAServerIDIsRefused(t *testing.T) {
	log := fakeCalendarTool(t, `{"ok":true,"id":"gym1"}`)
	pending := gym()
	pending.RemoteID = ""
	model := calendarModel(t, pending)
	model.calUndo = &calendarUndo{expires: model.now().Add(time.Minute), change: pendingChange{op: "update", before: pending, after: pending}}
	model.calUndo.change.after.Summary = "Gym class"
	model.events = nil
	model = undoPress(t, model, "u")
	if calls(t, log) != "" || !strings.Contains(model.status, "no server id") {
		t.Fatalf("calls %q status %q", calls(t, log), model.status)
	}
}
