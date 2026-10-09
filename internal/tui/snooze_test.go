package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/aronvandepol/mailday/internal/maildir"
	"github.com/aronvandepol/mailday/internal/syncd"
)

// snoozeNow is Wed 7 Oct 2026, 10:30 (the tests run in Amsterdam).
func snoozeNow() time.Time { return time.Date(2026, 10, 7, 10, 30, 0, 0, time.Local) }

// snoozeEnv points the shared list at a temporary file, never the real one.
func snoozeEnv(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "snooze.json")
	t.Setenv("MAILDAY_SNOOZE_FILE", path)
	t.Setenv("MAILDAY_SYNCD_SOCKET", filepath.Join(t.TempDir(), "none.sock"))
	return path
}

func snoozeModel(t *testing.T, store MailStore, messages ...maildir.Message) Model {
	t.Helper()
	model := NewModel(store, &fakeCalendarStore{}, Options{})
	model.now = snoozeNow
	model.width, model.height = 120, 30
	model.loadingMail, model.loadingCal = false, false
	model.boxes = []string{"Inbox", "@Reply", snoozedBox}
	model.accounts = []string{"gmail"}
	model.messages = messages
	return model
}

func snoozeTestMessage(subject, id string) maildir.Message {
	return maildir.Message{Path: "/mail/gmail/Inbox/cur/" + id, Account: "gmail", Box: "Inbox", Subject: subject, MessageID: "<" + id + "@x>", Date: snoozeNow().Add(-time.Hour)}
}

func TestParseSnoozeDue(t *testing.T) {
	now := snoozeNow()
	at := func(day, hour, minute int) time.Time { return time.Date(2026, 10, day, hour, minute, 0, 0, time.Local) }
	for phrase, want := range map[string]time.Time{
		"tomorrow 9":       at(8, 9, 0),
		"tomorrow":         at(8, 8, 0), // a day alone: 08:00
		"fri":              at(9, 8, 0),
		"fri 14:30":        at(9, 14, 30),
		"volgende week":    at(12, 8, 0),
		"in 3 days":        at(10, 8, 0),
		"tonight":          at(7, 19, 0),
		"in 2 hours":       at(7, 12, 30),
		"15:00":            at(7, 15, 0),
		"9:00":             at(8, 9, 0), // already past today: tomorrow
		"tomorrow evening": at(8, 19, 0),
		"next week":        at(12, 8, 0),
		"12 nov":           time.Date(2026, 11, 12, 8, 0, 0, 0, time.Local),
	} {
		got, err := parseSnoozeDue(phrase, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("%q = %v, %v; want %v", phrase, got, err, want)
		}
	}
	for _, phrase := range []string{"", "   ", "banana", "today", "yesterday 9"} {
		if got, err := parseSnoozeDue(phrase, now); err == nil {
			t.Errorf("%q = %v, want an error", phrase, got)
		}
	}
}

func TestSnoozeDay(t *testing.T) {
	now := snoozeNow()
	for _, c := range []struct {
		due  time.Time
		want string
	}{
		{time.Date(2026, 10, 7, 19, 0, 0, 0, time.Local), "19:00"},
		{time.Date(2026, 10, 9, 8, 0, 0, 0, time.Local), "Fri 08:00"},
		{time.Date(2026, 10, 20, 8, 0, 0, 0, time.Local), "Tue 20 Oct 08:00"},
		{time.Date(2027, 1, 4, 8, 0, 0, 0, time.Local), "Mon 4 Jan 2027 08:00"},
	} {
		if got := snoozeDay(c.due, now); got != c.want {
			t.Errorf("snoozeDay(%v) = %q, want %q", c.due, got, c.want)
		}
	}
}

func TestZOpensThePromptWithAPreview(t *testing.T) {
	snoozeEnv(t)
	model := snoozeModel(t, &fakeMailStore{}, snoozeTestMessage("Budget", "one"))
	updated, _ := model.Update(key("Z"))
	model = updated.(Model)
	if model.snoozing == nil {
		t.Fatal("Z did not open the prompt")
	}
	view := ansiStrip(model.View().Content)
	if !strings.Contains(view, "Snooze · Budget") || !strings.Contains(view, "tomorrow 9") {
		t.Fatalf("footer:\n%s", view)
	}
	model = typeInto(t, model, "fri")
	if footer := ansiStrip(model.renderSnoozePrompt(100)); !strings.Contains(footer, "Back in your inbox Fri 9 Oct 08:00") {
		t.Fatalf("preview:\n%s", footer)
	}
	// An unreadable phrase keeps the prompt open and says why.
	model = typeInto(t, model, "xx")
	updated, command := model.Update(key("enter"))
	model = updated.(Model)
	if command != nil || model.snoozing == nil || model.status == "" {
		t.Fatalf("a bad phrase must not snooze: command %v prompt %v status %q", command, model.snoozing, model.status)
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	model = updated.(Model)
	if model.snoozing != nil {
		t.Fatal("esc left the prompt open")
	}
}

func TestZIsRefusedForDraftsSentAndMessagesWithoutAnID(t *testing.T) {
	snoozeEnv(t)
	sent := snoozeTestMessage("Sent one", "s")
	sent.Box = maildir.SentBox
	noID := snoozeTestMessage("No ID", "n")
	noID.MessageID = ""
	for _, message := range []maildir.Message{sent, noID} {
		model := snoozeModel(t, &fakeMailStore{}, message)
		model.boxes = []string{"Inbox", maildir.SentBox}
		if message.Box == maildir.SentBox {
			model.mailBox = 1
		}
		updated, _ := model.Update(key("Z"))
		if got := updated.(Model); got.snoozing != nil || got.status == "" {
			t.Errorf("%s: prompt %v status %q, want a refusal", message.Subject, got.snoozing, got.status)
		}
	}
}

func TestSnoozeFilesTheMessageAndUndoBringsItBack(t *testing.T) {
	path := snoozeEnv(t)
	store := &fakeMailStore{}
	model := snoozeModel(t, store, snoozeTestMessage("Budget", "one"), snoozeTestMessage("Other", "two"))
	updated, _ := model.Update(key("Z"))
	model = typeInto(t, updated.(Model), "fri")
	updated, command := model.Update(key("enter"))
	model = updated.(Model)
	if command == nil || model.snoozing != nil {
		t.Fatalf("enter: command %v prompt %v", command, model.snoozing)
	}
	model = run(t, model, command)
	if strings.Join(store.moved, ",") != snoozedBox {
		t.Fatalf("moved = %v, want %s", store.moved, snoozedBox)
	}
	if model.status != "Snoozed until Fri 08:00 · u undoes" {
		t.Fatalf("status = %q", model.status)
	}
	if len(model.messages) != 1 || model.messages[0].Subject != "Other" {
		t.Fatalf("messages = %+v, want only the other one left in the list", model.messages)
	}
	entries, err := syncd.ReadSnooze(path)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries = %+v, %v", entries, err)
	}
	entry := entries[0]
	if entry.MessageID != "<one@x>" || entry.Account != "gmail" || entry.FromBox != "Inbox" || entry.Subject != "Budget" ||
		!entry.Due.Equal(time.Date(2026, 10, 9, 8, 0, 0, 0, time.Local)) || !entry.Added.Equal(snoozeNow()) {
		t.Fatalf("entry = %+v", entry)
	}

	// u moves it back and drops the entry.
	updated, command = model.Update(key("u"))
	model = updated.(Model)
	model = run(t, model, command)
	if strings.Join(store.moved, ",") != snoozedBox+",Inbox" {
		t.Fatalf("moved = %v, want it back to Inbox", store.moved)
	}
	if entries, _ := syncd.ReadSnooze(path); len(entries) != 0 {
		t.Fatalf("entry survived the undo: %+v", entries)
	}
	if model.pendingStatus != "Restored to Inbox" {
		t.Fatalf("pendingStatus = %q", model.pendingStatus)
	}
}

func TestSnoozeFromTheReader(t *testing.T) {
	path := snoozeEnv(t)
	store := &fakeMailStore{}
	message := snoozeTestMessage("Budget", "one")
	model := snoozeModel(t, store, message)
	model.screen = screenMail
	model.contentPath = message.Path
	model.content = &maildir.Content{Subject: "Budget"}
	updated, _ := model.Update(key("Z"))
	model = typeInto(t, updated.(Model), "tomorrow 9")
	updated, command := model.Update(key("enter"))
	model = run(t, updated.(Model), command)
	if model.screen != screenHome {
		t.Fatalf("screen = %v, want the list after the last message was snoozed", model.screen)
	}
	entries, _ := syncd.ReadSnooze(path)
	if len(entries) != 1 || !entries[0].Due.Equal(time.Date(2026, 10, 8, 9, 0, 0, 0, time.Local)) {
		t.Fatalf("entries = %+v", entries)
	}
}

// A failed move leaves no entry behind that promises a message back.
type failingMoveStore struct{ fakeMailStore }

func (f *failingMoveStore) MoveTo(maildir.Message, string) (string, error) {
	return "", os.ErrPermission
}

func TestSnoozeMoveFailureDropsTheEntry(t *testing.T) {
	path := snoozeEnv(t)
	model := snoozeModel(t, &failingMoveStore{}, snoozeTestMessage("Budget", "one"))
	updated, _ := model.Update(key("Z"))
	model = typeInto(t, updated.(Model), "fri")
	updated, command := model.Update(key("enter"))
	model = run(t, updated.(Model), command)
	if entries, _ := syncd.ReadSnooze(path); len(entries) != 0 {
		t.Fatalf("entries = %+v, want none after a failed move", entries)
	}
	if len(model.messages) != 1 || model.status == "" || strings.Contains(model.status, "Snoozed") {
		t.Fatalf("messages %d status %q", len(model.messages), model.status)
	}
}

func TestSnoozingAgainOnlyMovesTheDueTime(t *testing.T) {
	path := snoozeEnv(t)
	store := &fakeMailStore{}
	message := snoozeTestMessage("Budget", "one")
	message.Box, message.Path = snoozedBox, "/mail/gmail/@Snoozed/cur/one"
	first := time.Date(2026, 10, 9, 8, 0, 0, 0, time.Local)
	if err := syncd.AddSnooze(path, syncd.SnoozeEntry{MessageID: message.MessageID, Account: "gmail", Due: first, FromBox: "@Reply", Subject: "Budget"}); err != nil {
		t.Fatal(err)
	}
	model := snoozeModel(t, store, message)
	model.mailBox = 2
	updated, _ := model.Update(key("Z"))
	model = typeInto(t, updated.(Model), "next week")
	updated, command := model.Update(key("enter"))
	model = run(t, updated.(Model), command)
	if len(store.moved) != 0 {
		t.Fatalf("moved = %v, want no move: it is already in @Snoozed", store.moved)
	}
	entries, _ := syncd.ReadSnooze(path)
	if len(entries) != 1 || entries[0].FromBox != "@Reply" || !entries[0].Due.Equal(time.Date(2026, 10, 12, 8, 0, 0, 0, time.Local)) {
		t.Fatalf("entries = %+v, want the new time and the original box", entries)
	}
	if !strings.HasPrefix(model.status, "Snoozed until Mon 08:00") {
		t.Fatalf("status = %q", model.status)
	}
}

func TestSnoozedBoxShowsDueTimesInDueOrder(t *testing.T) {
	path := snoozeEnv(t)
	at := func(day, hour int) time.Time { return time.Date(2026, 10, day, hour, 0, 0, 0, time.Local) }
	var messages []maildir.Message
	for _, c := range []struct {
		subject, id string
		due         time.Time
		unread      bool
	}{
		{"Later", "late", at(20, 8), false},
		{"Soonest", "soon", at(8, 9), true},
		{"Middle", "mid", at(9, 8), false},
	} {
		message := snoozeTestMessage(c.subject, c.id)
		message.Box, message.Unread = snoozedBox, c.unread
		messages = append(messages, message)
		if err := syncd.AddSnooze(path, syncd.SnoozeEntry{MessageID: message.MessageID, Account: "gmail", Due: c.due, Subject: c.subject}); err != nil {
			t.Fatal(err)
		}
	}
	stray := snoozeTestMessage("Filed by hand", "stray")
	stray.Box = snoozedBox
	messages = append([]maildir.Message{stray}, messages...)
	model := snoozeModel(t, &fakeMailStore{}, messages...)
	model.mailBox = 2
	var order []string
	for _, index := range model.filteredMessageIndexes() {
		order = append(order, model.messages[index].Subject)
	}
	if want := []string{"Soonest", "Middle", "Later", "Filed by hand"}; !slices.Equal(order, want) {
		t.Fatalf("order = %v, want %v (unread or not, by due time, unknown last)", order, want)
	}
	view := ansiStrip(model.View().Content)
	for _, want := range []string{"Thu 09:00", "Fri 08:00", "20 Oct", "Snoozed, in order of return"} {
		if !strings.Contains(view, want) {
			t.Errorf("view lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Previously Seen") || strings.Contains(view, "New for You") {
		t.Errorf("the box is one list in due order, not sections:\n%s", view)
	}
	// Elsewhere dates are as before.
	model.mailBox = 0
	if got := model.listDate(snoozeTestMessage("x", "y")); got != "09:30" {
		t.Errorf("inbox date = %q", got)
	}
}

func TestFilingOffersNoSnoozedBox(t *testing.T) {
	snoozeEnv(t)
	model := snoozeModel(t, &fakeMailStore{}, snoozeTestMessage("Budget", "one"))
	updated, _ := model.Update(key("f"))
	model = updated.(Model)
	if slices.Contains(model.fileTargets, snoozedBox) {
		t.Fatalf("fileTargets = %v: filing by hand would strand the message", model.fileTargets)
	}
}

// With the real Maildir store: the @Snoozed folder is created next to the
// account's other tag folders, the message moves into it and undo returns it.
func TestSnoozeWithTheMaildirStore(t *testing.T) {
	path := snoozeEnv(t)
	root := t.TempDir()
	account := filepath.Join(root, "gmail")
	for _, folder := range []string{"Inbox", "@Reply"} {
		for _, leaf := range []string{"cur", "new", "tmp"} {
			os.MkdirAll(filepath.Join(account, folder, leaf), 0o700)
		}
	}
	file := filepath.Join(account, "Inbox", "cur", "1700000000.1_1.host:2,S")
	raw := "From: Ada <ada@example.com>\r\nSubject: Budget\r\nDate: Tue, 25 Aug 2026 10:00:00 +0200\r\nMessage-ID: <real@x>\r\n\r\nbody\r\n"
	os.WriteFile(file, []byte(raw), 0o600)
	store := maildir.New([]string{root}, 50)
	listed, err := store.List(t.Context())
	if err != nil || len(listed.Messages) != 1 {
		t.Fatalf("List: %v, %v", listed.Messages, err)
	}
	model := snoozeModel(t, store, listed.Messages...)
	model.mailStore = store

	updated, _ := model.Update(key("Z"))
	model = typeInto(t, updated.(Model), "fri")
	updated, command := model.Update(key("enter"))
	model = run(t, updated.(Model), command)
	if !strings.HasPrefix(model.status, "Snoozed until Fri 08:00") {
		t.Fatalf("status = %q", model.status)
	}
	for _, leaf := range []string{"cur", "new", "tmp"} {
		if info, err := os.Stat(filepath.Join(account, snoozedBox, leaf)); err != nil || !info.IsDir() {
			t.Fatalf("@Snoozed/%s was not created: %v", leaf, err)
		}
	}
	moved, _ := filepath.Glob(filepath.Join(account, snoozedBox, "cur", "*"))
	if len(moved) != 1 {
		t.Fatalf("files in @Snoozed/cur: %v", moved)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("the message is still in the inbox")
	}
	if entries, _ := syncd.ReadSnooze(path); len(entries) != 1 || entries[0].MessageID != "<real@x>" {
		t.Fatalf("entries = %+v", entries)
	}

	updated, command = model.Update(key("u"))
	run(t, updated.(Model), command)
	if back, _ := filepath.Glob(filepath.Join(account, "Inbox", "cur", "*")); len(back) != 1 {
		t.Fatalf("undo did not bring it back: %v", back)
	}
	if entries, _ := syncd.ReadSnooze(path); len(entries) != 0 {
		t.Fatalf("entry survived the undo: %+v", entries)
	}
}

// An account with no "@" folders has no mbsync channel that syncs @Snoozed.
func TestSnoozeRefusesAnAccountWithoutTagFolders(t *testing.T) {
	path := snoozeEnv(t)
	root := t.TempDir()
	account := filepath.Join(root, "gmail")
	for _, leaf := range []string{"cur", "new", "tmp"} {
		os.MkdirAll(filepath.Join(account, "Inbox", leaf), 0o700)
	}
	file := filepath.Join(account, "Inbox", "cur", "1700000000.1_1.host:2,S")
	os.WriteFile(file, []byte("From: a@x\r\nSubject: s\r\nMessage-ID: <m@x>\r\n\r\nb\r\n"), 0o600)
	store := maildir.New([]string{root}, 50)
	listed, _ := store.List(t.Context())
	model := snoozeModel(t, store, listed.Messages...)
	updated, _ := model.Update(key("Z"))
	model = typeInto(t, updated.(Model), "fri")
	updated, command := model.Update(key("enter"))
	model = run(t, updated.(Model), command)
	if !strings.Contains(model.status, "no @ tag folders") {
		t.Fatalf("status = %q", model.status)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal("the message must stay put")
	}
	if entries, _ := syncd.ReadSnooze(path); len(entries) != 0 {
		t.Fatalf("entries = %+v", entries)
	}
	if _, err := os.Stat(filepath.Join(account, snoozedBox)); !os.IsNotExist(err) {
		t.Fatal("no @Snoozed folder should be created")
	}
}

func TestSnoozedBoxIsNotFoldedIntoConversations(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MAILDAY_SNOOZE_FILE", filepath.Join(t.TempDir(), "snooze.json"))
	now := time.Now()
	// Two messages of one conversation, both snoozed.
	first := maildir.Message{Path: "/m/a/@Snoozed/cur/1.x:2,S", Account: "a", Subject: "Plan", Box: snoozedBox, MessageID: "<1@x>", Date: now.Add(-2 * time.Hour)}
	second := maildir.Message{Path: "/m/a/@Snoozed/cur/2.x:2,S", Account: "a", Subject: "Re: Plan", Box: snoozedBox, MessageID: "<2@x>", InReplyTo: "<1@x>", References: "<1@x>", Date: now.Add(-time.Hour)}
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
	model.messages = []maildir.Message{second, first}
	model.boxes = []string{maildir.InboxBox, snoozedBox}
	model.mailBox = 1
	model.loadingMail = false
	if rows := model.filteredMessageIndexes(); len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 (one per snoozed message)", len(rows))
	}
}

// damageSnoozeList leaves the list with one entry in its backup and a main
// file cut off mid-write.
func damageSnoozeList(t *testing.T, path string) {
	t.Helper()
	entry := syncd.SnoozeEntry{MessageID: "<old@x>", Account: "gmail", Due: snoozeNow().Add(48 * time.Hour), FromBox: "Inbox", Subject: "Old", Added: snoozeNow()}
	if err := syncd.AddSnooze(path, entry); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"entries": [{"messageID"`), 0o600); err != nil {
		t.Fatal(err)
	}
	syncd.TakeSnoozeNote() // earlier tests' notes
}

func TestDamagedSnoozeListIsReportedOnceAtStart(t *testing.T) {
	path := snoozeEnv(t)
	damageSnoozeList(t, path)
	model := snoozeModel(t, &fakeMailStore{})
	model = run(t, model, checkSnoozeCmd())
	if model.status != "snooze list was damaged; restored from the backup" {
		t.Fatalf("status = %q", model.status)
	}
	// The list screen reads it again on every redraw; no second note.
	loadSnoozeDues()
	model.status = ""
	model = run(t, model, checkSnoozeCmd())
	if model.status != "" {
		t.Fatalf("second status = %q, want none", model.status)
	}
	if _, ok := snoozeDueOf(maildir.Message{Account: "gmail", MessageID: "<old@x>"}); !ok {
		t.Fatal("the list screen does not see the backup's entry")
	}
}

func TestSnoozeOnADamagedListRestoresItKeepsTheCopyAndSays(t *testing.T) {
	path := snoozeEnv(t)
	damageSnoozeList(t, path)
	store := &fakeMailStore{}
	model := snoozeModel(t, store, snoozeTestMessage("Budget", "one"))
	model = press(t, model, "Z")
	model = typeInto(t, model, "fri")
	updated, command := model.Update(key("enter"))
	model = run(t, updated.(Model), command)
	if !strings.HasPrefix(model.status, "snooze list was damaged; restored from the backup · Snoozed until Fri 08:00") {
		t.Fatalf("status = %q", model.status)
	}
	entries, err := syncd.ReadSnooze(path)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries = %+v, %v; want the backup's and the new one", entries, err)
	}
	if kept, _ := filepath.Glob(path + ".corrupt-*"); len(kept) != 1 {
		t.Fatalf("damaged copies kept = %v", kept)
	}
}
