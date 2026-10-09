package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/aronvandepol/mailday/internal/maildir"
)

var ctrlR = tea.KeyPressMsg(tea.Key{Code: 'r', Mod: tea.ModCtrl})

// sealReceipts points every file receipts touch at temporary places, with a
// fake msmtp first on PATH that records what it is given.
func sealReceipts(t *testing.T) (state, argsLog, stdinLog string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	state = t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	t.Setenv("MAILDAY_READ_RECEIPTS", "")
	if err := os.MkdirAll(filepath.Join(home, ".config", "msmtp"), 0o700); err != nil {
		t.Fatal(err)
	}
	config := "account university\nfrom s.de.vries@hum.uni.example\n\naccount gmail\nfrom sam.devries@gmail.example\n"
	if err := os.WriteFile(filepath.Join(home, ".config", "msmtp", "config"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	argsLog, stdinLog = filepath.Join(bin, "args"), filepath.Join(bin, "stdin")
	script := "#!/bin/sh\necho \"$@\" >> '" + argsLog + "'\n/bin/cat >> '" + stdinLog + "'\n"
	if err := os.WriteFile(filepath.Join(bin, "msmtp"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return state, argsLog, stdinLog
}

var receiptNow = time.Date(2025, 10, 20, 9, 0, 0, 0, time.UTC)

func answered(recipient, name, disposition string) *maildir.Receipt {
	return &maildir.Receipt{
		OriginalMessageID: "<SENT-1@samdevries.example>", Recipient: recipient, FromName: name, Disposition: disposition,
		Date: time.Date(2025, 10, 7, 12, 2, 0, 0, time.UTC),
	}
}

func receiptModel(t *testing.T, messages ...maildir.Message) (Model, *fakeMailStore) {
	t.Helper()
	store := &fakeMailStore{result: maildir.ListResult{Messages: messages, Accounts: []string{"uni"}, Boxes: []string{maildir.InboxBox, maildir.SentBox}}}
	model := NewModel(store, &fakeCalendarStore{}, Options{Days: 14})
	model.now = func() time.Time { return receiptNow }
	model.width, model.height = 120, 30
	updated, _ := model.Update(mailLoadedMsg{result: store.result})
	return updated.(Model), store
}

func sentMessage() maildir.Message {
	return maildir.Message{Path: "/mail/uni/Sent/cur/one", Account: "uni", Box: maildir.SentBox, Subject: "Chapter 3 draft",
		To: "Ruben Bakker <r.e.bakker@hum.uni.example>", MessageID: "<sent-1@samdevries.example>", Date: receiptNow.Add(-time.Hour)}
}

func receiptMessage(receipt *maildir.Receipt) maildir.Message {
	return maildir.Message{Path: "/mail/uni/Inbox/cur/mdn", Account: "uni", Subject: "Read: Chapter 3 draft", From: "Ruben Bakker",
		Date: receiptNow.Add(-30 * time.Minute), Unread: true, Receipt: receipt}
}

func plain(view string) string { return ansi.Strip(view) }

func TestReceiptsAreHiddenFromTheInboxButCounted(t *testing.T) {
	ordinary := maildir.Message{Path: "/mail/uni/Inbox/cur/a", Account: "uni", Subject: "Planning", From: "Ada", Date: receiptNow, Unread: true}
	mdn := receiptMessage(answered("r.e.bakker@hum.uni.example", "Ruben Bakker", "displayed"))
	model, store := receiptModel(t, ordinary, mdn)
	if len(store.result.Messages) != 2 {
		t.Fatalf("the caller's slice was changed: %d messages", len(store.result.Messages))
	}
	if len(model.messages) != 1 || model.messages[0].Subject != "Planning" {
		t.Fatalf("loaded %+v, want only the ordinary message", model.messages)
	}
	if rows := model.filteredMessageIndexes(); len(rows) != 1 {
		t.Fatalf("%d rows in the inbox", len(rows))
	}
	view := plain(model.View().Content)
	if strings.Contains(view, "Read: Chapter 3") || !strings.Contains(view, "1 read receipt hidden") {
		t.Fatalf("inbox view:\n%s", view)
	}
	// Opening the message in the list and counting unread must not see it either.
	if unread := model.messages[0].Unread; !unread || len(model.messages) != 1 {
		t.Fatal("unread bookkeeping changed")
	}
}

func TestNoReceiptsMeansNoNoise(t *testing.T) {
	model, _ := receiptModel(t, maildir.Message{Path: "/m/a", Account: "uni", Subject: "Planning", Date: receiptNow})
	if label := model.receiptLabel(); label != "" {
		t.Fatalf("label %q", label)
	}
}

func TestSentRowsMarkMessagesThatWereRead(t *testing.T) {
	read := sentMessage()
	unanswered := sentMessage()
	unanswered.Path, unanswered.MessageID, unanswered.Subject = "/mail/uni/Sent/cur/two", "<sent-2@samdevries.example>", "Quiet one"
	deleted := sentMessage()
	deleted.Path, deleted.MessageID, deleted.Subject = "/mail/uni/Sent/cur/three", "<sent-3@samdevries.example>", "Thrown away"
	gone := answered("x@example.org", "", "deleted")
	gone.OriginalMessageID = "<sent-3@samdevries.example>"
	model, _ := receiptModel(t, read, unanswered, deleted,
		receiptMessage(answered("r.e.bakker@hum.uni.example", "Ruben Bakker", "displayed")), receiptMessage(gone))
	model.mailBox = 1 // Sent
	view := plain(model.View().Content)
	for _, want := range []string{"✓ read", "deleted unread"} {
		if !strings.Contains(view, want) {
			t.Fatalf("Sent box lacks %q:\n%s", want, view)
		}
	}
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "to Ruben") && strings.Contains(line, "Quiet") {
			t.Fatal("a message nobody answered must not be marked")
		}
	}
	if mark := model.receiptMark(unanswered); mark != "" {
		t.Fatalf("unanswered mark %q", mark)
	}
	if mark := model.receiptMark(read); mark != "✓ read" {
		t.Fatalf("read mark %q (brackets and case must not matter)", mark)
	}
	// Only the Sent box carries the mark.
	inbox := read
	inbox.Box = ""
	if mark := model.receiptMark(inbox); mark != "" {
		t.Fatalf("inbox mark %q", mark)
	}
}

func inReader(t *testing.T, model Model, message maildir.Message, content maildir.Content) Model {
	t.Helper()
	model.screen = screenMail
	model.focus = paneMail
	model.readerMessage = message
	model.contentPath = message.Path
	model.content = &content
	return model
}

func TestReaderOfASentMessageNamesWhoReadItAndWhen(t *testing.T) {
	model, _ := receiptModel(t, sentMessage(),
		receiptMessage(answered("r.e.bakker@hum.uni.example", "", "displayed")),
		receiptMessage(answered("ada@example.org", "Ada Lovelace", "deleted")))
	content := maildir.Content{MessageID: "<sent-1@samdevries.example>", Body: "Please read.",
		ToList: []maildir.Address{{Name: "Bakker, R.E. (Ruben)", Addr: "r.e.bakker@hum.uni.example"}}}
	view := plain(strings.Join(inReader(t, model, sentMessage(), content).receiptLines(120), "\n"))
	for _, want := range []string{"Read by Ruben Bakker · Tue 7 Oct 14:02", "Deleted unread by Ada Lovelace · Tue 7 Oct 14:02"} {
		if !strings.Contains(view, want) {
			t.Fatalf("reader lacks %q:\n%s", want, view)
		}
	}
	full := plain(inReader(t, model, sentMessage(), content).View().Content)
	if !strings.Contains(full, "Read by Ruben Bakker") {
		t.Fatalf("the reader screen does not show the line:\n%s", full)
	}
}

// Asking for receipts and answering a request have no keys; receipts that
// arrive are shown (the tests above). These pin that nothing in the interface asks any more.

func asking() maildir.Content {
	return maildir.Content{
		MessageID: "<ask-1@hum.uni.example>", Subject: "Chapter 3 draft", Body: "Could you look at it?",
		FromName: "Bakker, R.E. (Ruben)", FromAddr: "r.e.bakker@hum.uni.example",
		ToList:    []maildir.Address{{Addr: "s.de.vries@hum.uni.example"}},
		ReceiptTo: []maildir.Address{{Name: "Ruben", Addr: "r.e.bakker@hum.uni.example"}},
		Date:      receiptNow.Add(-2 * time.Hour),
	}
}

func askingMessage() maildir.Message {
	return maildir.Message{Path: "/mail/uni/Inbox/cur/ask", Account: "uni", Subject: "Chapter 3 draft", MessageID: "<ask-1@hum.uni.example>"}
}

func TestAMessageThatAsksForAReceiptGetsNoLineNoKeyAndNoReceipt(t *testing.T) {
	_, argsLog, stdinLog := sealReceipts(t)
	model, _ := receiptModel(t)
	model = inReader(t, model, askingMessage(), asking())
	if lines := model.receiptLines(120); len(lines) != 0 {
		t.Fatalf("the reader still says something about the request: %q", lines)
	}
	if footer := plain(model.renderFooter(120)); strings.Contains(footer, "ctrl+r") || strings.Contains(footer, "receipt") {
		t.Fatalf("footer offers a receipt:\n%s", footer)
	}
	updated, command := model.Update(ctrlR)
	if command != nil || strings.Contains(strings.ToLower(updated.(Model).status), "receipt") {
		t.Fatalf("ctrl+r did something: %v %q", command, updated.(Model).status)
	}
	for _, path := range []string{argsLog, stdinLog} {
		if _, err := os.Stat(path); err == nil {
			t.Fatalf("msmtp ran: %s exists", path)
		}
	}
}

func TestPreviewHasNoReadReceiptKeyAndHelpDoesNotMentionOne(t *testing.T) {
	model := previewModel(t, checkedDraft())
	t.Setenv("MAILDAY_READ_RECEIPTS", "")
	model.draftPath = filepath.Join(draftDir(), "no-r.md")
	updated, _ := model.Update(key("r"))
	if after := updated.(Model); after.draft.ReadReceipt || strings.Contains(strings.ToLower(after.status), "receipt") {
		t.Fatalf("r still toggles a read receipt: %+v %q", after.draft.ReadReceipt, after.status)
	}
	if view := plain(model.View().Content); strings.Contains(view, "read receipt") {
		t.Fatalf("preview mentions read receipts:\n%s", view)
	}
	if footer := plain(model.renderFooter(120)); strings.Contains(footer, "receipt") {
		t.Fatalf("footer:\n%s", footer)
	}
	for _, section := range helpSections {
		for _, binding := range section.bindings {
			if strings.Contains(binding[1], "receipt") {
				t.Fatalf("help still lists a receipt key: %v", binding)
			}
		}
	}
}

func TestHiddenOptInStillAsksForAReceiptButHasNoKey(t *testing.T) {
	t.Setenv("MAILDAY_READ_RECEIPTS", "on")
	model := previewModel(t, checkedDraft())
	model.draft.ReadReceipt = true // what compose.New does under the opt-in
	if view := plain(model.View().Content); strings.Contains(view, "read receipt") {
		t.Fatalf("preview shows a read-receipt line:\n%s", view)
	}
	raw, _, err := model.draft.Build(time.Now())
	if err != nil || !strings.Contains(string(raw), "Disposition-Notification-To: ") {
		t.Fatalf("the opt-in no longer asks: %v", err)
	}
}
