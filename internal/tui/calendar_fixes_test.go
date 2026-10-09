package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/aronvandepol/mailday/internal/calendar"
)

// inZone makes name the machine's zone for the test.
func inZone(t *testing.T, name string) *time.Location {
	t.Helper()
	location, err := time.LoadLocation(name)
	if err != nil {
		t.Skip(err)
	}
	old := time.Local
	time.Local = location
	t.Cleanup(func() { time.Local = old })
	return location
}

// utcEvent is a timed event as Google and Exchange hand it over: in UTC.
func utcEvent(start time.Time, length time.Duration) calendar.Event {
	start = start.UTC()
	return calendar.Event{ID: "u/1", UID: "u", Summary: "Seminar", Source: "Work", RemoteID: "sem1", Editable: true, Start: start, End: start.Add(length)}
}

func TestRebookUTCEventKeepsTheMachinesWallClock(t *testing.T) {
	cases := []struct {
		zone, input string
		start       time.Time // an instant
		want        string    // the update call's --start/--end
	}{
		// 18:30 CEST; "m 16:00" is 16:00 in Amsterdam, not 16:00 UTC (18:00).
		{"Europe/Amsterdam", "16:00", time.Date(2026, 10, 8, 16, 30, 0, 0, time.UTC), "--start 2026-10-08T16:00:00+02:00 --end 2026-10-08T17:00:00+02:00"},
		// The same instant on a laptop in Korea is 01:30 on the 9th.
		{"Asia/Seoul", "16:00", time.Date(2026, 10, 8, 16, 30, 0, 0, time.UTC), "--start 2026-10-09T16:00:00+09:00 --end 2026-10-09T17:00:00+09:00"},
		{"Europe/Amsterdam", "fri", time.Date(2026, 10, 8, 8, 0, 0, 0, time.UTC), "--start 2026-10-09T10:00:00+02:00 --end 2026-10-09T11:00:00+02:00"},
		{"Asia/Seoul", "fri", time.Date(2026, 10, 8, 1, 0, 0, 0, time.UTC), "--start 2026-10-09T10:00:00+09:00 --end 2026-10-09T11:00:00+09:00"},
	}
	for _, c := range cases {
		inZone(t, c.zone)
		log := fakeCalendarTool(t, `{"ok":true,"id":"sem1"}`)
		model := calendarModel(t, utcEvent(c.start, time.Hour))
		updated, _ := model.Update(key("m"))
		model = typeInto(t, updated.(Model), c.input)
		updated, command := model.Update(key("enter"))
		run(t, updated.(Model), command)
		if got, want := calls(t, log), "update --calendar Work --id sem1 "+c.want; got != want {
			t.Errorf("%s %q:\n got  %q\n want %q", c.zone, c.input, got, want)
		}
	}
}

func TestNudgeUTCEventAcrossDaylightSaving(t *testing.T) {
	amsterdam := inZone(t, "Europe/Amsterdam")
	// 10:00 CEST on the 24th; two days on is after the clock change of the
	// 25th and must still read 10:00 (09:00Z).
	model := calendarModel(t, utcEvent(time.Date(2026, 10, 24, 8, 0, 0, 0, time.UTC), time.Hour))
	for range 2 {
		updated, _ := model.Update(key(">"))
		model = updated.(Model)
	}
	if got, want := model.events[0].Start, time.Date(2026, 10, 26, 10, 0, 0, 0, amsterdam); !got.Equal(want) {
		t.Fatalf("nudged to %s, want %s", got, want)
	}
}

func TestFormRebooksAUTCEventInLocalTime(t *testing.T) {
	inZone(t, "Europe/Amsterdam")
	model := calendarModel(t, utcEvent(time.Date(2026, 10, 8, 16, 30, 0, 0, time.UTC), time.Hour))
	updated, _ := model.Update(key("e"))
	model = updated.(Model)
	if model.eventForm.when != "2026-10-08 18:30-19:30" {
		t.Fatalf("when = %q, want local wall clock", model.eventForm.when)
	}
	model.eventForm.when = "16:00"
	start, end, allDay, err := model.formTimes()
	if err != nil || allDay || start.Format("2006-01-02 15:04") != "2026-10-08 16:00" || end.Format("15:04") != "17:00" {
		t.Fatalf("formTimes = %s – %s %v %v", start, end, allDay, err)
	}
}

func TestFormKeepsAnAllDayEventAllDay(t *testing.T) {
	amsterdam := inZone(t, "Europe/Amsterdam")
	event := calendar.Event{ID: "d/1", UID: "d", Summary: "Retreat", Source: "Work", RemoteID: "ret1", Editable: true,
		AllDay: true, Start: time.Date(2026, 10, 24, 0, 0, 0, 0, amsterdam), End: time.Date(2026, 10, 27, 0, 0, 0, 0, amsterdam)}
	if got, want := whenText(event), "2026-10-24 all day for 3 days"; got != want {
		t.Fatalf("whenText = %q, want %q", got, want)
	}
	single := event
	single.End = time.Date(2026, 10, 25, 0, 0, 0, 0, amsterdam)
	if got, want := whenText(single), "2026-10-24 all day"; got != want {
		t.Fatalf("whenText = %q, want %q", got, want)
	}
	model := calendarModel(t, event)
	updated, _ := model.Update(key("e"))
	model = updated.(Model)
	// An unchanged When reads back as it was; a new date keeps the span.
	for input, want := range map[string]string{
		model.eventForm.when: "2026-10-24 – 2026-10-27",
		"2026-10-12":         "2026-10-12 – 2026-10-15",
		"fri":                "2026-10-09 – 2026-10-12",
		"+1d":                "2026-10-25 – 2026-10-28",
		"2026-10-12 all day": "2026-10-12 – 2026-10-13",
	} {
		model.eventForm.when = input
		start, end, allDay, err := model.formTimes()
		if err != nil || !allDay {
			t.Errorf("%q: allDay %v err %v", input, allDay, err)
			continue
		}
		if got := start.Format("2006-01-02") + " – " + end.Format("2006-01-02"); got != want || start.Hour() != 0 || end.Hour() != 0 {
			t.Errorf("%q: got %s (%s – %s), want %s", input, got, start, end, want)
		}
	}
	// A time turns it into a timed event.
	model.eventForm.when = "2026-10-12 14:00"
	if _, _, allDay, err := model.formTimes(); err != nil || allDay {
		t.Errorf("a time should make it timed: allDay %v err %v", allDay, err)
	}
}

func TestWhenTextReadsBackAcrossMidnight(t *testing.T) {
	amsterdam := inZone(t, "Europe/Amsterdam")
	for name, end := range map[string]time.Time{
		"next midnight": time.Date(2026, 10, 9, 0, 0, 0, 0, amsterdam),
		"after":         time.Date(2026, 10, 9, 2, 0, 0, 0, amsterdam),
	} {
		event := calendar.Event{ID: "p/1", Summary: "Party", Source: "Work", RemoteID: "p1", Editable: true,
			Start: time.Date(2026, 10, 8, 22, 0, 0, 0, amsterdam), End: end}
		text := whenText(event)
		if !strings.Contains(text, " for ") || strings.Contains(text, ":00-") {
			t.Errorf("%s: whenText = %q, want a length", name, text)
		}
		model := calendarModel(t, event)
		updated, _ := model.Update(key("e"))
		model = updated.(Model)
		model.eventForm.when = text + " " // any edit: not the untouched string
		start, gotEnd, _, err := model.formTimes()
		if err != nil || !start.Equal(event.Start) || !gotEnd.Equal(event.End) {
			t.Errorf("%s: %q read back as %s – %s, %v", name, text, start, gotEnd, err)
		}
	}
}

func TestQuickAddAfterHalfPastElevenSaysTomorrow(t *testing.T) {
	inZone(t, "Europe/Amsterdam")
	model := calendarModel(t)
	late := time.Date(2026, 10, 6, 23, 40, 0, 0, time.Local)
	model.now = func() time.Time { return late }
	model.calendarAnchor = time.Date(2026, 10, 6, 0, 0, 0, 0, time.Local)
	updated, _ := model.Update(key("n"))
	model = typeInto(t, updated.(Model), "Review")
	preview, ok := model.promptPreview()
	if !ok || !strings.Contains(preview, "Wed 7 Oct 09:00–10:00") || !strings.Contains(preview, "tomorrow at 9:00") {
		t.Fatalf("preview = %q", preview)
	}
	if box := ansiStrip(model.renderQuickAdd(100)); !strings.Contains(box, "Wed 7 Oct") || !strings.Contains(box, "after 23:30") {
		t.Fatalf("box does not show the date and why:\n%s", box)
	}
}

func guestsEvent() calendar.Event {
	event := gym()
	event.Attendees = "ada@uni.nl"
	return event
}

func openFormOnCalendar(t *testing.T, model Model, to string) Model {
	t.Helper()
	updated, _ := model.Update(key("e"))
	model = updated.(Model)
	model.eventForm.calendar = to
	return model
}

func TestTransferBetweenServicesRefusesGuestsAndSeries(t *testing.T) {
	cases := []struct {
		name    string
		event   calendar.Event
		invite  string
		backend map[string]string
		want    string // status; empty: the move goes ahead
	}{
		{"guests across services", guestsEvent(), "", map[string]string{"Work": "google", "University": "exchange"}, "has guests: change its calendar in Google itself"},
		{"series across services", func() calendar.Event { e := gym(); e.Series = true; return e }(), "", map[string]string{"Work": "google", "University": "exchange"}, "repeats: change its calendar in Google itself"},
		{"guests within a service", guestsEvent(), "", map[string]string{"Work": "google", "University": "google"}, ""},
		{"plain event across services", gym(), "", map[string]string{"Work": "google", "University": "exchange"}, ""},
		{"inviting while moving", gym(), "bert@x.org", map[string]string{"Work": "google", "University": "google"}, "Cannot invite while moving"},
	}
	for _, c := range cases {
		log := fakeCalendarTool(t, `{"ok":true,"id":"new1"}`)
		model := calendarModel(t, c.event)
		model.calendarBackends = c.backend
		model = openFormOnCalendar(t, model, "University")
		model.eventForm.invite = c.invite
		updated, command := model.Update(key("ctrl+s"))
		model = updated.(Model)
		if c.want == "" {
			if model.eventForm != nil || command == nil {
				t.Errorf("%s: should move, status %q", c.name, model.status)
				continue
			}
			run(t, model, command)
			if got := calls(t, log); !strings.HasPrefix(got, "transfer ") || strings.Contains(got, "--notes") || strings.Contains(got, "--attendee") {
				t.Errorf("%s: call = %q", c.name, got)
			}
			continue
		}
		if !strings.Contains(model.status, c.want) || model.eventForm == nil || command != nil || calls(t, log) != "" {
			t.Errorf("%s: status %q form open %v command %v calls %q", c.name, model.status, model.eventForm != nil, command != nil, calls(t, log))
		}
		if model.events[0].Source != "Work" || model.events[0].Pending {
			t.Errorf("%s: the event should stay where it is: %+v", c.name, model.events[0])
		}
	}
}

func TestNotesAreSentOnlyWhenEditedAndWithoutTheJoinLine(t *testing.T) {
	joined := gym()
	joined.Description = "Join: https://meet.google.com/abc-defg-hij\n\nBring the shoes"
	log := fakeCalendarTool(t, `{"ok":true,"id":"gym1"}`)

	// Untouched notes: the form shows them without the Join line, and a save
	// of another field leaves them out of the call.
	model := calendarModel(t, joined)
	updated, _ := model.Update(key("e"))
	model = updated.(Model)
	if model.eventForm.notes != "Bring the shoes" {
		t.Fatalf("notes = %q", model.eventForm.notes)
	}
	model.eventForm.location = "Sportcentrum"
	updated, command := model.Update(key("ctrl+s"))
	run(t, updated.(Model), command)
	if got := calls(t, log); strings.Contains(got, "--notes") || !strings.Contains(got, "--location Sportcentrum") {
		t.Fatalf("call = %q", got)
	}

	// Edited notes go without the Join line.
	os.Truncate(log, 0)
	model = calendarModel(t, joined)
	updated, _ = model.Update(key("e"))
	model = updated.(Model)
	model.eventForm.notes = "Bring two pairs"
	updated, command = model.Update(key("ctrl+s"))
	run(t, updated.(Model), command)
	if got := calls(t, log); !strings.HasSuffix(got, "--notes Bring two pairs") {
		t.Fatalf("call = %q", got)
	}

	// A transfer with untouched notes sends none; with edited notes, those.
	os.Truncate(log, 0)
	model = openFormOnCalendar(t, calendarModel(t, joined), "University")
	updated, command = model.Update(key("ctrl+s"))
	run(t, updated.(Model), command)
	if got := calls(t, log); strings.Contains(got, "--notes") {
		t.Fatalf("transfer call = %q", got)
	}
	os.Truncate(log, 0)
	model = openFormOnCalendar(t, calendarModel(t, joined), "University")
	model.eventForm.notes = "New notes"
	updated, command = model.Update(key("ctrl+s"))
	run(t, updated.(Model), command)
	if got := calls(t, log); !strings.Contains(got, "--notes New notes") {
		t.Fatalf("transfer call = %q", got)
	}
}

func TestTruncatedNotesCannotBeEdited(t *testing.T) {
	long := gym()
	long.Description = strings.Repeat("é", 4000)
	log := fakeCalendarTool(t, `{"ok":true,"id":"gym1"}`)
	model := calendarModel(t, long)
	updated, _ := model.Update(key("e"))
	model = updated.(Model)
	updated, command := model.Update(tea.KeyPressMsg(tea.Key{Code: 'o', Mod: tea.ModCtrl}))
	model = updated.(Model)
	if command != nil || !strings.Contains(model.status, "too long to edit here") {
		t.Fatalf("ctrl+o should be refused: status %q", model.status)
	}
	model.eventForm.notes += " more"
	updated, command = model.Update(key("ctrl+s"))
	model = updated.(Model)
	if command != nil || model.eventForm == nil || !strings.Contains(model.status, "too long to edit here") {
		t.Fatalf("save should be refused: status %q", model.status)
	}
	// Other fields still save, and leave the notes alone.
	model.eventForm.notes = strings.Repeat("é", 4000)
	model.eventForm.location = "Hall"
	updated, command = model.Update(key("ctrl+s"))
	run(t, updated.(Model), command)
	if got := calls(t, log); strings.Contains(got, "--notes") || !strings.Contains(got, "--location Hall") {
		t.Fatalf("call = %q", got)
	}
}

func TestSelectionFollowsARebookedEventOnceLoaded(t *testing.T) {
	inZone(t, "Europe/Amsterdam")
	fakeCalendarTool(t, `{"ok":true,"id":"gym1"}`)
	earlier := calendar.Event{ID: "o/1", Summary: "Breakfast", Source: "Work", RemoteID: "b1", Editable: true,
		Start: time.Date(2026, 10, 9, 8, 0, 0, 0, time.Local), End: time.Date(2026, 10, 9, 9, 0, 0, 0, time.Local)}
	model := calendarModel(t, gym(), earlier)
	model.sortEvents()
	model.selectEvent("g/1")
	updated, _ := model.Update(key("m"))
	model = typeInto(t, updated.(Model), "fri 17:00")
	updated, command := model.Update(key("enter"))
	model = run(t, updated.(Model), command)
	moved := gym()
	moved.ID = "g/2"
	moved.Start, moved.End = time.Date(2026, 10, 9, 17, 0, 0, 0, time.Local), time.Date(2026, 10, 9, 18, 30, 0, 0, time.Local)
	updated, _ = model.Update(calendarLoadedMsg{result: calendar.Result{Events: []calendar.Event{earlier, moved}}})
	model = updated.(Model)
	if selected, _ := model.selectedEvent(); selected.ID != "g/2" {
		t.Fatalf("selected %q (cursor %d), want the rebooked event g/2", selected.ID, model.eventCursor)
	}

	// An answer to an invitation: the selection stays on it.
	invite := calendar.Event{ID: "i/1", Summary: "Seminar", Source: "Work", RemoteID: "inv1", Organizer: "Anouk", RSVP: "needs-action",
		Start: time.Date(2026, 10, 9, 10, 0, 0, 0, time.Local), End: time.Date(2026, 10, 9, 11, 0, 0, 0, time.Local)}
	model = calendarModel(t, earlier, invite)
	model.sortEvents()
	model.selectEvent("i/1")
	updated, command = model.Update(key("a"))
	model = run(t, updated.(Model), command)
	answered := invite
	answered.RSVP = "accepted"
	updated, _ = model.Update(calendarLoadedMsg{result: calendar.Result{Events: []calendar.Event{earlier, answered}}})
	model = updated.(Model)
	if selected, _ := model.selectedEvent(); len(model.calPending) != 0 || selected.ID != "i/1" {
		t.Fatalf("answer should be confirmed with the invitation selected: %+v, selected %q", model.calPending, selected.ID)
	}
}

func TestStaleCalendarLoadsAreDroppedAndChangesDuringALoadReload(t *testing.T) {
	model := calendarModel(t)
	model.loadingCal = true
	from, to := model.calendarRange()
	fresh := calendar.Event{ID: "f/1", Summary: "Fresh", Start: from.Add(10 * time.Hour), End: from.Add(11 * time.Hour)}
	stale := calendar.Event{ID: "s/1", Summary: "Stale", Start: from.Add(10 * time.Hour), End: from.Add(11 * time.Hour)}

	// A result for the previous week arrives after the user moved on.
	updated, _ := model.Update(calendarLoadedMsg{result: calendar.Result{Events: []calendar.Event{stale}}, from: from.AddDate(0, 0, -7), to: to.AddDate(0, 0, -7)})
	model = updated.(Model)
	if len(model.events) != 0 || !model.loadingCal {
		t.Fatalf("stale load applied: events %+v loading %v", model.events, model.loadingCal)
	}

	// A change arriving during the load is remembered, not dropped.
	updated, _ = model.Update(calendarChangedMsg{})
	model = updated.(Model)
	if !model.calReloadAgain {
		t.Fatal("a change during a load should ask for another load")
	}
	updated, command := model.Update(calendarLoadedMsg{result: calendar.Result{Events: []calendar.Event{fresh}}, from: from, to: to})
	model = updated.(Model)
	if len(model.events) != 1 || model.events[0].ID != "f/1" || model.loadingCal || model.calReloadAgain {
		t.Fatalf("events %+v loading %v again %v", model.events, model.loadingCal, model.calReloadAgain)
	}
	if command == nil {
		t.Fatal("the load that landed should be followed by a reload")
	}
	if loaded, ok := command().(calendarLoadedMsg); !ok || !loaded.quiet || !loaded.from.Equal(from) {
		t.Fatalf("follow-up = %#v, want a quiet load of the same range", loaded)
	}

	// With nothing in flight a change starts one load, and a second change
	// while it runs asks for one more.
	model = calendarModel(t)
	updated, command = model.Update(calendarChangedMsg{})
	model = updated.(Model)
	if command == nil || !model.calQuietLoading {
		t.Fatal("a change should start a quiet load")
	}
	updated, _ = model.Update(calendarChangedMsg{})
	if !updated.(Model).calReloadAgain {
		t.Fatal("a change during a quiet load should be remembered")
	}
}

func TestJoinWorksFromEveryCalendarView(t *testing.T) {
	dir := t.TempDir()
	opened := filepath.Join(dir, "opened")
	script := "#!/bin/sh\necho \"$1\" > " + opened + "\n"
	for _, name := range []string{"xdg-open", "open"} {
		os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	call := gym()
	call.Description = "Agenda\nhttps://teams.microsoft.com/l/meetup-join/abc"

	for _, mode := range []calendarMode{calendarDay, calendarWeek, calendarAgenda} {
		os.Remove(opened)
		model := calendarModel(t, call)
		model.calendarMode = mode
		updated, command := model.Update(key("J"))
		model = updated.(Model)
		if !strings.HasPrefix(model.status, "Joining Gym") || command == nil {
			t.Fatalf("%s: status %q", mode, model.status)
		}
		command()
		data, _ := os.ReadFile(opened)
		// The detached helper may still be writing.
		for wait := 0; len(data) == 0 && wait < 100; wait++ {
			time.Sleep(10 * time.Millisecond)
			data, _ = os.ReadFile(opened)
		}
		if strings.TrimSpace(string(data)) != "https://teams.microsoft.com/l/meetup-join/abc" {
			t.Fatalf("%s: opened %q", mode, data)
		}
	}
	// The event screen still joins, and an event without a link says so.
	model := calendarModel(t, call)
	model.screen = screenEvent
	if updated, command := model.Update(key("J")); command == nil || !strings.HasPrefix(updated.(Model).status, "Joining") {
		t.Fatal("J should join from the event screen")
	}
	model = calendarModel(t, gym())
	updated, command := model.Update(key("J"))
	if command != nil || updated.(Model).status != "This event has no meeting link" {
		t.Fatalf("status %q", updated.(Model).status)
	}
}

func TestDeletingFromTheEventScreenReturnsToTheList(t *testing.T) {
	fakeCalendarTool(t, `{"ok":true}`)
	other := gym()
	other.ID, other.RemoteID, other.Summary = "g/2", "gym2", "Swim"
	other.Start = other.Start.Add(24 * time.Hour)
	other.End = other.End.Add(24 * time.Hour)
	model := calendarModel(t, gym(), other)
	model.screen = screenEvent
	updated, _ := model.Update(key("d"))
	updated, command := updated.(Model).Update(key("d"))
	model = updated.(Model)
	if model.screen != screenHome {
		t.Fatalf("screen = %v, want the list", model.screen)
	}
	if !strings.Contains(model.status, "Deleting Gym") {
		t.Fatalf("status = %q", model.status)
	}
	model = run(t, model, command)
	if model.screen != screenHome || model.status != "Deleted Gym" {
		t.Fatalf("screen %v status %q", model.screen, model.status)
	}
}
