package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/aronvandepol/mailday/internal/maildir"
)

func flowMessage(name string, unread bool, age time.Duration) maildir.Message {
	dir := "cur"
	suffix := ":2,S"
	if unread {
		dir, suffix = "new", ""
	}
	return maildir.Message{
		Path: fmt.Sprintf("/m/a/Inbox/%s/%s.x%s", dir, name, suffix), Account: "a", Box: maildir.InboxBox,
		Subject: "Subject " + name, Date: time.Now().Add(-age), Unread: unread,
	}
}

// openInReader opens the message under the cursor and loads its body, then
// applies the "marked read" result, as the real program would.
func openInReader(t *testing.T, model Model, content maildir.Content) Model {
	t.Helper()
	updated, _ := model.Update(key("enter"))
	model = updated.(Model)
	return loadReaderBody(t, model, content)
}

func loadReaderBody(t *testing.T, model Model, content maildir.Content) Model {
	t.Helper()
	updated, command := model.Update(bodyLoadedMsg{path: model.contentPath, content: content})
	model = updated.(Model)
	if command != nil {
		updated, _ = model.Update(command())
		model = updated.(Model)
	}
	return model
}

func readerSubject(model Model) string { return model.readerMessage.Subject }

func TestReaderNextPreviousAndNextUnread(t *testing.T) {
	a, b := flowMessage("1003", true, time.Minute), flowMessage("1002", true, 2*time.Minute)
	c, d := flowMessage("1001", false, 3*time.Minute), flowMessage("1000", true, 4*time.Minute)
	model, _ := readerModel(t, a, b, c, d)
	model = openInReader(t, model, maildir.Content{Body: "one"})
	if got := readerSubject(model); got != "Subject 1003" {
		t.Fatalf("opened %q", got)
	}

	// n goes to the next message in the list as it was when opened, even
	// though reading A moved it out of New for You.
	updated, _ := model.Update(key("n"))
	model = loadReaderBody(t, updated.(Model), maildir.Content{Body: "two"})
	if got := readerSubject(model); got != "Subject 1002" {
		t.Fatalf("n opened %q, want the second message", got)
	}
	updated, _ = model.Update(key("p"))
	model = loadReaderBody(t, updated.(Model), maildir.Content{Body: "one"})
	if got := readerSubject(model); got != "Subject 1003" {
		t.Fatalf("p opened %q, want the first message", got)
	}
	updated, _ = model.Update(key("p"))
	model = updated.(Model)
	if readerSubject(model) != "Subject 1003" || !strings.Contains(model.status, "first message") {
		t.Fatalf("p at the top: %q / %q", readerSubject(model), model.status)
	}

	// B was read on the way, so N skips it and the read C and lands on D.
	updated, _ = model.Update(key("N"))
	model = loadReaderBody(t, updated.(Model), maildir.Content{Body: "d"})
	if got := readerSubject(model); got != "Subject 1000" {
		t.Fatalf("N opened %q, want the last unread", got)
	}
	// Everything is read now: N says so and stays.
	updated, _ = model.Update(key("N"))
	model = updated.(Model)
	if model.status != "No more unread" || readerSubject(model) != "Subject 1000" {
		t.Fatalf("N with nothing unread: %q / %q", model.status, readerSubject(model))
	}
	// The read C closes the remembered list (unread ones came first).
	updated, _ = model.Update(key("n"))
	model = loadReaderBody(t, updated.(Model), maildir.Content{})
	updated, _ = model.Update(key("n"))
	if got := updated.(Model).status; readerSubject(updated.(Model)) != "Subject 1001" || !strings.Contains(got, "last message") {
		t.Fatalf("n at the end: %q on %q", got, readerSubject(updated.(Model)))
	}
}

func TestReaderNextUnreadWrapsRoundTheList(t *testing.T) {
	a, b, c := flowMessage("3", false, time.Minute), flowMessage("2", false, 2*time.Minute), flowMessage("1", false, 3*time.Minute)
	model, _ := readerModel(t, a, b, c)
	model.mailCursor = 2
	model = openInReader(t, model, maildir.Content{})
	updated, _ := model.Update(key("N"))
	if got := updated.(Model).status; got != "No more unread" {
		t.Fatalf("status %q", got)
	}
	// Mail arrives above the reader's place: N wraps back to it.
	for index := range model.messages {
		if model.messages[index].Subject == "Subject 3" {
			model.messages[index].Unread = true
		}
	}
	updated, _ = model.Update(key("N"))
	model = updated.(Model)
	if readerSubject(model) != "Subject 3" || !strings.Contains(model.status, "top") {
		t.Fatalf("N should wrap to Subject 3, got %q (%q)", readerSubject(model), model.status)
	}
}

func TestActionsInTheReaderOpenTheNextMessage(t *testing.T) {
	for _, step := range []struct {
		name string
		keys []string
		want string
	}{
		{"archive", []string{"a", "a"}, "Archived"},
		{"delete", []string{"d"}, "Moved to Trash"},
		{"file", []string{"f", "r"}, "Filed to Reply"},
	} {
		t.Run(step.name, func(t *testing.T) {
			a, b, c := flowMessage("3", true, time.Minute), flowMessage("2", true, 2*time.Minute), flowMessage("1", true, 3*time.Minute)
			model, _ := readerModel(t, a, b, c)
			model.boxes = []string{maildir.InboxBox, "@Reply"}
			model = openInReader(t, model, maildir.Content{Body: "one"})
			var command tea.Cmd
			for _, k := range step.keys {
				var updated tea.Model
				updated, command = model.Update(key(k))
				model = updated.(Model)
			}
			updated, next := model.Update(command())
			model = updated.(Model)
			if model.screen != screenMail || readerSubject(model) != "Subject 2" || next == nil {
				t.Fatalf("screen %v, reader on %q: want the next message", model.screen, readerSubject(model))
			}
			if !strings.Contains(model.status, step.want) || !strings.Contains(model.status, "u undoes") {
				t.Fatalf("status %q", model.status)
			}
			// The note survives the new message loading.
			model = loadReaderBody(t, model, maildir.Content{Body: "two"})
			if !strings.Contains(model.status, step.want) {
				t.Fatalf("note lost once the body loaded: %q", model.status)
			}
		})
	}
}

func TestMarkUnreadInTheReaderOpensTheNextMessage(t *testing.T) {
	a, b := flowMessage("2", true, time.Minute), flowMessage("1", true, 2*time.Minute)
	model, _ := readerModel(t, a, b)
	model = openInReader(t, model, maildir.Content{})
	updated, command := model.Update(key("m"))
	updated, _ = updated.(Model).Update(command())
	model = updated.(Model)
	if readerSubject(model) != "Subject 1" || !strings.Contains(model.status, "Marked unread") {
		t.Fatalf("reader on %q, status %q", readerSubject(model), model.status)
	}
}

func TestTheLastMessageReturnsToTheList(t *testing.T) {
	only := flowMessage("1", true, time.Minute)
	model, _ := readerModel(t, only)
	model = openInReader(t, model, maildir.Content{})
	updated, _ := model.Update(key("a"))
	updated, command := updated.(Model).Update(key("a"))
	updated, _ = updated.(Model).Update(command())
	model = updated.(Model)
	if model.screen != screenHome || !strings.Contains(model.status, "Archived") {
		t.Fatalf("screen %v status %q: the last message should return to the list", model.screen, model.status)
	}
}

func TestAutoAdvanceOffReturnsToTheList(t *testing.T) {
	t.Setenv("MAILDAY_AUTO_ADVANCE", "off")
	a, b := flowMessage("2", true, time.Minute), flowMessage("1", true, 2*time.Minute)
	model, _ := readerModel(t, a, b)
	model = openInReader(t, model, maildir.Content{})
	updated, command := model.Update(key("d"))
	updated, _ = updated.(Model).Update(command())
	model = updated.(Model)
	if model.screen != screenHome {
		t.Fatalf("MAILDAY_AUTO_ADVANCE=off left the reader open on %q", readerSubject(model))
	}
}

func TestAMoveFinishingAfterTheReaderMovedOnLeavesItAlone(t *testing.T) {
	a, b, c := flowMessage("3", true, time.Minute), flowMessage("2", true, 2*time.Minute), flowMessage("1", true, 3*time.Minute)
	model, _ := readerModel(t, a, b, c)
	model = openInReader(t, model, maildir.Content{})
	updated, _ := model.Update(key("d"))
	model = updated.(Model)
	updated, _ = model.Update(key("n")) // on to B before the delete reports back
	model = loadReaderBody(t, updated.(Model), maildir.Content{})
	updated, _ = model.Update(messageDeletedMsg{message: a, newPath: "/m/a/Trash/cur/x"})
	model = updated.(Model)
	if model.screen != screenMail || readerSubject(model) != "Subject 2" {
		t.Fatalf("a delete of the first message moved the reader (screen %v, %q)", model.screen, readerSubject(model))
	}
}

func TestUndoWorksInTheReader(t *testing.T) {
	a, b := flowMessage("2", true, time.Minute), flowMessage("1", true, 2*time.Minute)
	model, store := readerModel(t, a, b)
	model = openInReader(t, model, maildir.Content{})
	updated, command := model.Update(key("d"))
	updated, _ = updated.(Model).Update(command())
	model = loadReaderBody(t, updated.(Model), maildir.Content{})
	updated, command = model.Update(key("u"))
	if command == nil {
		t.Fatal("u did nothing in the reader")
	}
	command()
	if got := store.moved[len(store.moved)-1]; got != maildir.InboxBox {
		t.Fatalf("undo moved to %q", got)
	}
}

func TestListPagingAndNextUnread(t *testing.T) {
	model := listModel(t, 60)
	_, _, _, height := model.layout()
	page := max(height/2-1, 1)
	model = press(t, model, "pgdown")
	if model.mailCursor != page {
		t.Fatalf("pgdown moved to %d, want %d", model.mailCursor, page)
	}
	model = press(t, model, "pgup")
	if model.mailCursor != 0 {
		t.Fatalf("pgup moved to %d", model.mailCursor)
	}
	updated, _ := model.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	model = updated.(Model)
	if model.mailCursor != max(page/2, 1) {
		t.Fatalf("ctrl+d moved to %d", model.mailCursor)
	}
	updated, _ = model.Update(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	if got := updated.(Model).mailCursor; got != 0 {
		t.Fatalf("ctrl+u moved to %d", got)
	}

	// n: the next unread after the cursor, wrapping.
	model.messages[3].Unread, model.messages[5].Unread = true, true
	model.mailCursor = 0
	// The two unread messages sit on top, in list order: 03 then 05.
	model = press(t, model, "n")
	if selected, _ := model.selectedMail(); selected.Subject != "Message 05" {
		t.Fatalf("n went to %q", selected.Subject)
	}
	model = press(t, model, "n")
	if selected, _ := model.selectedMail(); selected.Subject != "Message 03" {
		t.Fatalf("n should wrap to the first unread, got %q", selected.Subject)
	}
	model.messages[3].Unread, model.messages[5].Unread = false, false
	model = press(t, model, "n")
	if model.status != "No more unread" {
		t.Fatalf("status %q", model.status)
	}
}

func TestReaderKeysDoNotLeakIntoTheCalendar(t *testing.T) {
	model, _ := readerModel(t, flowMessage("1", true, time.Minute))
	model.focus = paneAgenda
	model.writableCalendars = []string{"University"}
	updated, _ := model.Update(key("n"))
	if updated.(Model).calPrompt == nil {
		t.Fatal("n in the calendar should still start a new event")
	}
}

func TestReaderFooterAndHelpMentionTheNewKeys(t *testing.T) {
	model, _ := readerModel(t, flowMessage("1", true, time.Minute))
	model = openInReader(t, model, maildir.Content{Body: "x", Attachments: []maildir.Attachment{{Name: "a.pdf", Size: 10}}})
	view := ansi.Strip(model.View().Content)
	for _, want := range []string{"n p", "next/prev", "open file", "links"} {
		if !strings.Contains(view, want) {
			t.Errorf("reader footer lacks %q:\n%s", want, view)
		}
	}
	model.showHelp = true
	help := ""
	for range 40 { // the list is longer than a 30-row screen: scroll it
		help += ansi.Strip(model.View().Content)
		updated, _ := model.Update(key("j"))
		model = updated.(Model)
	}
	if !strings.Contains(help, "next, previous, next unread") {
		t.Errorf("help lacks the reader keys:\n%s", help)
	}
}

func TestReaderHeaderWrapsLongRecipientLines(t *testing.T) {
	model, _ := readerModel(t, flowMessage("1", true, time.Minute))
	var to []maildir.Address
	for _, name := range []string{"Ada Lovelace", "Brian Kernighan", "Chris Wellons"} {
		to = append(to, maildir.Address{Name: name, Addr: strings.ToLower(strings.Fields(name)[0]) + "@example.org"})
	}
	model.width = 40
	model = openInReader(t, model, maildir.Content{Body: "x", ToList: to})
	view := ansi.Strip(model.View().Content)
	if !strings.Contains(view, "Ada Lovelace, Brian") || !strings.Contains(view, "Chris Wellons") || strings.Contains(view, "...") {
		t.Fatalf("to line cut off:\n%s", view)
	}
}
