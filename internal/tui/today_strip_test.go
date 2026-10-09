package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aronvandepol/mailday/internal/calendar"
	"github.com/aronvandepol/mailday/internal/maildir"
)

// stripModel is the mail list at 13:35 on Tue 6 Oct 2026 with events loaded.
func stripModel(t *testing.T, events ...calendar.Event) Model {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{Days: 14})
	now := time.Date(2026, 10, 6, 13, 35, 0, 0, time.Local)
	model.now = func() time.Time { return now }
	model.width, model.height = 120, 30
	model.loadingCal, model.loadingMail = false, false
	model.status = ""
	model.messages = []maildir.Message{{Path: "/mail/a/Inbox/cur/one", Account: "a", From: "Ada", Subject: "Hello", Date: now}}
	model.accounts = []string{"a"}
	model.events = events
	return model
}

func at(hour, minute int) time.Time {
	return time.Date(2026, 10, 6, hour, minute, 0, 0, time.Local)
}

func seminar() calendar.Event {
	return calendar.Event{ID: "e/1", Summary: "Seminar", Source: "Work", Location: "Atrium", Start: at(14, 0), End: at(15, 0),
		Description: "Join: https://teams.microsoft.com/l/meetup-join/xyz"}
}

func TestTodayStripNamesTheNextMeeting(t *testing.T) {
	view := ansiStrip(stripModel(t, seminar()).View().Content)
	want := "next 14:00 Seminar in 25 min · Atrium · J joins"
	if !strings.Contains(view, want) {
		t.Fatalf("no %q in:\n%s", want, view)
	}
}

func TestTodayStripNamesAMeetingInProgress(t *testing.T) {
	board := calendar.Event{ID: "e/2", Summary: "Board meeting", Source: "Work", Start: at(13, 0), End: at(15, 0),
		Description: "https://zoom.us/j/123"}
	view := ansiStrip(stripModel(t, board).View().Content)
	if want := "now Board meeting until 15:00 · J joins"; !strings.Contains(view, want) {
		t.Fatalf("no %q in:\n%s", want, view)
	}
	// No link, no J.
	board.Description = ""
	if view := ansiStrip(stripModel(t, board).View().Content); strings.Contains(view, "J joins") || !strings.Contains(view, "now Board meeting until 15:00") {
		t.Fatalf("link-less strip:\n%s", view)
	}
}

func TestTodayStripStaysAwayWithNothingNear(t *testing.T) {
	later := seminar()
	later.Start, later.End = at(14, 35), at(15, 0) // exactly an hour away
	allDay := calendar.Event{ID: "e/3", Summary: "Holiday", AllDay: true, Start: at(0, 0), End: at(0, 0).AddDate(0, 0, 1)}
	declined := seminar()
	declined.RSVP = "declined"
	over := seminar()
	over.Start, over.End = at(11, 0), at(12, 0)
	for name, events := range map[string][]calendar.Event{
		"an hour away": {later}, "all day": {allDay}, "declined": {declined}, "over": {over}, "none": nil,
	} {
		model := stripModel(t, events...)
		if line := model.todayStripLine(120); line != "" {
			t.Fatalf("%s: strip %q", name, ansiStrip(line))
		}
	}
	// Only on the mail list: not in the calendar pane or the reader.
	model := stripModel(t, seminar())
	model.focus = paneAgenda
	if model.todayStripLine(120) != "" {
		t.Fatal("strip in the calendar pane")
	}
	model.focus, model.screen = paneMail, screenMail
	if model.todayStripLine(120) != "" {
		t.Fatal("strip in the reader")
	}
}

func TestTodayStripPrefersAnImminentMeetingToOneStillRunning(t *testing.T) {
	workshop := calendar.Event{ID: "w", Summary: "Workshop", Start: at(9, 0), End: at(17, 0)}
	soon := seminar()
	soon.Start, soon.End = at(13, 40), at(14, 0)
	if event, running, ok := stripModel(t, workshop, soon).stripEvent(); !ok || running || event.Summary != "Seminar" {
		t.Fatalf("got %q running=%v", event.Summary, running)
	}
	// Further off, the workshop that is on now keeps the strip.
	if event, running, ok := stripModel(t, workshop, seminar()).stripEvent(); !ok || !running || event.Summary != "Workshop" {
		t.Fatalf("got %q running=%v", event.Summary, running)
	}
}

func TestStripRaisesTheHeaderLine(t *testing.T) {
	plain := stripModel(t)
	with := stripModel(t, seminar())
	_, _, _, plainBody := plain.layout()
	_, _, _, withBody := with.layout()
	if withBody != plainBody-1 {
		t.Fatalf("body height %d with the strip, %d without", withBody, plainBody)
	}
}

func TestCalendarTabCountsWhatIsLeftToday(t *testing.T) {
	over := seminar()
	over.ID, over.Start, over.End = "o", at(9, 0), at(10, 0)
	tomorrow := seminar()
	tomorrow.ID, tomorrow.Start, tomorrow.End = "t", at(14, 0).AddDate(0, 0, 1), at(15, 0).AddDate(0, 0, 1)
	running := seminar()
	running.ID, running.Start, running.End = "r", at(13, 0), at(14, 0)
	allDay := calendar.Event{ID: "a", AllDay: true, Start: at(0, 0), End: at(0, 0).AddDate(0, 0, 1)}
	later := seminar()
	later.ID, later.Start, later.End = "l", at(16, 0), at(17, 0)

	model := stripModel(t, over, tomorrow, running, allDay, later, seminar())
	if got := model.calendarTabLabel(); got != "Calendar · 3" {
		t.Fatalf("label = %q", got)
	}
	if view := ansiStrip(model.View().Content); !strings.Contains(view, "Calendar · 3") {
		t.Fatalf("tab not in header:\n%s", view)
	}
	if got := stripModel(t, over).calendarTabLabel(); got != "Calendar" {
		t.Fatalf("empty label = %q", got)
	}
}

func TestJOnTheMailListOpensTheStripMeeting(t *testing.T) {
	dir := t.TempDir()
	opened := filepath.Join(dir, "opened")
	script := "#!/bin/sh\necho \"$1\" > " + opened + "\n"
	for _, name := range []string{"xdg-open", "open"} {
		os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	updated, command := stripModel(t, seminar()).Update(key("J"))
	if command == nil || !strings.HasPrefix(updated.(Model).status, "Joining Seminar") {
		t.Fatalf("status %q", updated.(Model).status)
	}
	command()
	var data []byte
	for wait := 0; len(data) == 0 && wait < 100; wait++ {
		time.Sleep(10 * time.Millisecond)
		data, _ = os.ReadFile(opened)
	}
	if got := strings.TrimSpace(string(data)); got != "https://teams.microsoft.com/l/meetup-join/xyz" {
		t.Fatalf("opened %q", got)
	}

	// Nothing near, or no link: it says so and opens nothing.
	updated, command = stripModel(t).Update(key("J"))
	if command != nil || updated.(Model).status != "No meeting starting soon" {
		t.Fatalf("empty: status %q", updated.(Model).status)
	}
	noLink := seminar()
	noLink.Description, noLink.Location = "", ""
	updated, command = stripModel(t, noLink).Update(key("J"))
	if command != nil || updated.(Model).status != "Seminar has no meeting link" {
		t.Fatalf("no link: status %q", updated.(Model).status)
	}
}

func TestJInTheReaderIsLeftToTheReader(t *testing.T) {
	model := stripModel(t, seminar())
	model.screen = screenMail
	updated, command := model.Update(key("J"))
	if command != nil || strings.HasPrefix(updated.(Model).status, "Joining") {
		t.Fatalf("reader J joined: %q", updated.(Model).status)
	}
}
