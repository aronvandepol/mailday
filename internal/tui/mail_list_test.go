package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/aronvandepol/mailday/internal/maildir"
)

func manyMessages(count int) []maildir.Message {
	messages := make([]maildir.Message, count)
	base := time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)
	for index := range messages {
		messages[index] = maildir.Message{
			Path: fmt.Sprintf("/m/a/Inbox/cur/%03d.x:2,S", index), Account: "a", Box: maildir.InboxBox,
			Subject: fmt.Sprintf("Message %02d", index), Date: base.AddDate(0, 0, -index*20),
		}
	}
	return messages
}

func listModel(t *testing.T, count int) Model {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
	model.width, model.height = 100, 24
	updated, _ := model.Update(mailLoadedMsg{result: maildir.ListResult{Messages: manyMessages(count), Accounts: []string{"a"}, Boxes: []string{maildir.InboxBox}}})
	model = updated.(Model)
	model.loadingCal = false
	return model
}

func press(t *testing.T, model Model, keys ...string) Model {
	t.Helper()
	for _, k := range keys {
		updated, _ := model.Update(key(k))
		model = updated.(Model)
	}
	return model
}

func TestMailListScrollsOnlyWhenTheCursorLeavesTheWindow(t *testing.T) {
	model := listModel(t, 60)
	rows := model.mailRows()
	// The status line comes and goes, so the list height is read each time.
	height := func() int { _, _, _, h := model.layout(); return h }
	context := mailListContext(height())
	end := func() int { return mailWindowEnd(rows, model.mailOffset, height()) }
	model = press(t, model, "j")
	model = press(t, model, "k")

	// Moving down inside the window does not scroll.
	firstEnd := end()
	for model.mailCursor+1+context < firstEnd {
		model = press(t, model, "j")
		if model.mailOffset != 0 {
			t.Fatalf("scrolled at cursor %d, window end %d", model.mailCursor, firstEnd)
		}
	}
	// One more step and the list scrolls, keeping context below the cursor.
	for range 15 {
		model = press(t, model, "j")
		if below := end() - model.mailCursor - 1; below < context {
			t.Fatalf("cursor %d has %d rows below, want at least %d (offset %d)", model.mailCursor, below, context, model.mailOffset)
		}
	}
	if model.mailOffset == 0 {
		t.Fatal("the list never scrolled")
	}

	// Moving up inside the window leaves the offset alone.
	offset := model.mailOffset
	for model.mailCursor-1-context >= offset {
		model = press(t, model, "k")
		if model.mailOffset != offset {
			t.Fatalf("scrolled up early: cursor %d offset %d, was %d", model.mailCursor, model.mailOffset, offset)
		}
	}
	// And scrolls once the cursor nears the top.
	model = press(t, model, "k", "k")
	if model.mailOffset >= offset {
		t.Fatalf("offset %d did not move up from %d", model.mailOffset, offset)
	}
	if model.mailCursor-model.mailOffset < 0 {
		t.Fatal("cursor above the window")
	}
}

func TestMailListShowsContextBelowTheCursorOnScreen(t *testing.T) {
	model := listModel(t, 60)
	model = press(t, model, "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j", "j")
	view := ansi.Strip(model.View().Content)
	cursor := fmt.Sprintf("Message %02d", model.mailCursor)
	below := fmt.Sprintf("Message %02d", model.mailCursor+1)
	if !strings.Contains(view, cursor) || !strings.Contains(view, below) {
		t.Fatalf("the message after the cursor is not on screen:\n%s", view)
	}
	last := fmt.Sprintf("Message %02d", model.mailCursor+mailListContext(model.height))
	if !strings.Contains(view, last) {
		t.Fatalf("context below the cursor missing (%s):\n%s", last, view)
	}
}

func TestEndAndHomeKeepTheCursorVisible(t *testing.T) {
	model := listModel(t, 60)
	model = press(t, model, "G")
	view := ansi.Strip(model.View().Content)
	if !strings.Contains(view, "Message 59") {
		t.Fatalf("G did not scroll to the end:\n%s", view)
	}
	model = press(t, model, "home")
	if model.mailOffset != 0 || !strings.Contains(ansi.Strip(model.View().Content), "Message 00") {
		t.Fatalf("home did not scroll back: offset %d", model.mailOffset)
	}
}

func TestDateColumnDoesNotShiftWhileScrolling(t *testing.T) {
	model := listModel(t, 60)
	// Dates run from "15:04" through "30 Sep" to "30 Sep 2026"; every first
	// line must end at the same column.
	model.now = func() time.Time { return time.Date(2026, 10, 2, 12, 0, 0, 0, time.Local) }
	widths := map[int]bool{}
	for step := 0; step < 40; step++ {
		model = press(t, model, "j")
		for _, line := range strings.Split(ansi.Strip(model.View().Content), "\n") {
			if strings.Contains(line, "Message ") {
				widths[ansi.StringWidth(strings.TrimRight(line, " "))] = true
			}
		}
	}
	if len(widths) != 1 {
		t.Fatalf("first lines end at columns %v", widths)
	}
}
