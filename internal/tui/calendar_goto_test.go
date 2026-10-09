package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestGOpensTheJumpPromptInTheCalendar(t *testing.T) {
	model := calendarModel(t, gym())
	updated, _ := model.Update(key("g"))
	model = updated.(Model)
	if model.calPrompt == nil || model.calPrompt.kind != promptGoto {
		t.Fatalf("prompt = %+v", model.calPrompt)
	}
	view := ansiStrip(model.View().Content)
	if !strings.Contains(view, "Go to date") || !strings.Contains(view, "12 nov") {
		t.Fatalf("footer:\n%s", view)
	}
	model = typeInto(t, model, "12 nov")
	if preview := ansiStrip(model.renderGotoPrompt(100)); !strings.Contains(preview, "enter goes to Thursday 12 November 2026") {
		t.Fatalf("preview:\n%s", preview)
	}
	updated, command := model.Update(key("enter"))
	model = updated.(Model)
	if model.calPrompt != nil || command == nil || !model.loadingCal {
		t.Fatalf("prompt %v command %v loading %v", model.calPrompt, command, model.loadingCal)
	}
	if anchor := model.calendarAnchor; anchor.Year() != 2026 || anchor.Month() != time.November || anchor.Day() != 12 || anchor.Hour() != 0 {
		t.Fatalf("anchor = %v", anchor)
	}
	if model.eventCursor != 0 {
		t.Fatalf("cursor = %d", model.eventCursor)
	}
}

func TestGotoPhrases(t *testing.T) {
	// now is Tue 6 Oct 2026.
	for phrase, want := range map[string]string{
		"fri":        "2026-10-09",
		"next week":  "2026-10-12",
		"tomorrow":   "2026-10-07",
		"2026-12-01": "2026-12-01",
	} {
		model := calendarModel(t)
		day, err := model.gotoDate(phrase)
		if err != nil || day.Format("2006-01-02") != want {
			t.Fatalf("%q = %v, %v; want %s", phrase, day, err, want)
		}
	}
	model := calendarModel(t)
	for _, phrase := range []string{"", "banana"} {
		if _, err := model.gotoDate(phrase); err == nil {
			t.Fatalf("%q should not name a day", phrase)
		}
	}
}

func TestGotoKeepsThePromptOnAnUnreadablePhrase(t *testing.T) {
	model := calendarModel(t)
	updated, _ := model.Update(key("g"))
	model = typeInto(t, updated.(Model), "banana")
	updated, command := model.Update(key("enter"))
	model = updated.(Model)
	if model.calPrompt == nil || command != nil || !strings.HasPrefix(model.status, "no date in that") {
		t.Fatalf("prompt %v status %q", model.calPrompt, model.status)
	}
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if updated.(Model).calPrompt != nil {
		t.Fatal("esc should close the prompt")
	}
	if updated.(Model).calendarAnchor != (time.Time{}) {
		t.Fatal("esc moved the calendar")
	}
}

func TestGotoTodayClearsTheAnchor(t *testing.T) {
	model := calendarModel(t)
	model.calendarAnchor = time.Date(2026, 12, 1, 0, 0, 0, 0, time.Local)
	updated, _ := model.Update(key("g"))
	model = typeInto(t, updated.(Model), "today")
	updated, _ = model.Update(key("enter"))
	if !updated.(Model).calendarAnchor.IsZero() {
		t.Fatalf("anchor = %v", updated.(Model).calendarAnchor)
	}
}

func TestGOnTheMailListStillGoesToBoxes(t *testing.T) {
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
	updated, _ := model.Update(key("g"))
	if !updated.(Model).pendingGo || updated.(Model).calPrompt != nil {
		t.Fatal("g on the mail list should still wait for a box key")
	}
}
