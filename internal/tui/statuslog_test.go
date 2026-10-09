package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/aronvandepol/mailday/internal/maildir"
)

func TestIsErrorStatus(t *testing.T) {
	for text, want := range map[string]bool{
		"Not sent: no recipients":               true,
		"Not saved: Google refused":             true,
		"Cannot write: disk full":               true,
		"Undo failed: no such file":             true,
		"Mail error: timeout":                   true,
		"Search failed: Syntax error":           true,
		"Draft not saved: read-only":            true,
		"mbsync failed: auth":                   true,
		"Calendar error: boom":                  true,
		"Mail app is unavailable: neomutt":      true,
		"Moved to Trash · u undoes":             false,
		"Nothing written · no draft kept":       false,
		"Nothing to undo":                       false,
		"Archived · u undoes":                   false,
		"New: Ada — Hello":                      false,
		"Press a again to archive this message": false,
	} {
		if got := isErrorStatus(text); got != want {
			t.Errorf("isErrorStatus(%q) = %v, want %v", text, got, want)
		}
	}
}

func statusModel(t *testing.T) (Model, *time.Time) {
	t.Helper()
	model, _ := readerModel(t, maildir.Message{Path: "/m/a/Inbox/cur/1.x:2,S", Account: "a", Subject: "One", Box: maildir.InboxBox, Date: time.Now()},
		maildir.Message{Path: "/m/a/Inbox/cur/2.x:2,S", Account: "a", Subject: "Two", Box: maildir.InboxBox, Date: time.Now()})
	model.width, model.height = 100, 30
	clock := time.Date(2026, 10, 6, 14, 0, 0, 0, time.Local)
	model.now = func() time.Time { return clock }
	model.status = ""
	return model, &clock
}

func TestErrorsRenderRedAndSurviveCursorMovesForSixSeconds(t *testing.T) {
	model, clock := statusModel(t)
	// The clock is read through the closure, so it can move on.
	model.now = func() time.Time { return *clock }
	updated, _ := model.Update(key("x")) // a key with no effect, to have an Update
	model = updated.(Model)
	updated, _ = model.Update(calendarWriteMsg{change: pendingChange{op: "update"}, err: fmt.Errorf("Google refused")})
	model = updated.(Model)
	if !strings.HasPrefix(model.status, "Not saved: Google refused") {
		t.Fatalf("status = %q", model.status)
	}
	want := lipgloss.NewStyle().Foreground(colorError).Bold(true).Render(model.status)
	if notice := model.renderNotice(100); !strings.Contains(notice, want) {
		t.Fatalf("notice %q is not in the error style %q", notice, want)
	}

	// Cursor moves do not clear it for six seconds…
	*clock = clock.Add(5 * time.Second)
	updated, _ = model.Update(key("j"))
	model = updated.(Model)
	if !strings.HasPrefix(model.status, "Not saved") {
		t.Fatalf("a cursor move cleared the error after 5 s: %q", model.status)
	}
	// …nor does an arrival notice replace it…
	model.status = "New: Ada — Hello"
	model.trackStatus("Not saved: Google refused")
	if !strings.HasPrefix(model.status, "Not saved") {
		t.Fatalf("an arrival replaced the error: %q", model.status)
	}
	// …but a real new status does, and after six seconds a move clears it.
	*clock = clock.Add(2 * time.Second)
	updated, _ = model.Update(key("k"))
	model = updated.(Model)
	if model.status != "" {
		t.Fatalf("after 7 s the move should clear the error, status %q", model.status)
	}
}

func TestStatusLogKeepsTheLast30WithTimes(t *testing.T) {
	model, clock := statusModel(t)
	model.now = func() time.Time { return *clock }
	for index := 0; index < 40; index++ {
		*clock = clock.Add(time.Second)
		before := model.status
		model.status = fmt.Sprintf("Message %d", index)
		model.trackStatus(before)
	}
	if len(model.statusLog) != statusLogSize {
		t.Fatalf("log holds %d entries", len(model.statusLog))
	}
	if first, last := model.statusLog[0], model.statusLog[len(model.statusLog)-1]; first.text != "Message 10" || last.text != "Message 39" || !last.at.After(first.at) {
		t.Fatalf("first %+v last %+v", first, last)
	}
}

func TestBangShowsTheStatusLogAndEscClosesIt(t *testing.T) {
	model, clock := statusModel(t)
	model.now = func() time.Time { return *clock }
	for _, text := range []string{"Saved Gym", "Not sent: no recipients", "Moved to Trash · u undoes"} {
		*clock = clock.Add(time.Minute)
		before := model.status
		model.status = text
		model.trackStatus(before)
	}
	updated, _ := model.Update(key("!"))
	model = updated.(Model)
	if !model.showLog {
		t.Fatal("! did not open the log")
	}
	view := ansi.Strip(model.View().Content)
	for _, want := range []string{"Recent messages", "Saved Gym", "Not sent: no recipients", "Moved to Trash", "14:03:00"} {
		if !strings.Contains(view, want) {
			t.Fatalf("log lacks %q:\n%s", want, view)
		}
	}
	if strings.Index(view, "Moved to Trash") > strings.Index(view, "Saved Gym") {
		t.Fatalf("the newest message should come first:\n%s", view)
	}
	updated, _ = model.Update(key("j")) // scrolls, does not close or move the list
	model = updated.(Model)
	if !model.showLog || model.logScroll != 1 {
		t.Fatalf("showLog=%v scroll=%d", model.showLog, model.logScroll)
	}
	updated, _ = model.Update(escKey())
	model = updated.(Model)
	if model.showLog {
		t.Fatal("esc did not close the log")
	}
	if strings.Contains(ansi.Strip(model.View().Content), "Recent messages") {
		t.Fatal("the log is still drawn")
	}
}
