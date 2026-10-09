package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/aronvandepol/mailday/internal/compose"
	"github.com/aronvandepol/mailday/internal/maildir"
)

func ctrlC() tea.KeyPressMsg { return tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}) }

func escKey() tea.KeyPressMsg { return tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}) }

// draftFiles lists the saved drafts under the test's HOME.
func draftFiles(t *testing.T) []string {
	t.Helper()
	paths, _ := filepath.Glob(filepath.Join(draftDir(), "*.md"))
	return paths
}

// composerModel opens the mail composer on a new message.
func composerModel(t *testing.T) Model {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
	model.loadingMail, model.loadingCal = false, false
	updated, _ := model.Update(key("c"))
	model = updated.(Model)
	if model.form == nil {
		t.Fatal("c did not open the composer")
	}
	return model
}

func isQuit(command tea.Cmd) bool {
	if command == nil {
		return false
	}
	_, quit := command().(tea.QuitMsg)
	return quit
}

func TestCtrlCInTheComposerSavesTheDraftAndQuits(t *testing.T) {
	model := composerModel(t)
	model = typeInto(t, model, "ada@example.org")
	updated, command := model.Update(ctrlC())
	model = updated.(Model)
	if !isQuit(command) {
		t.Fatal("ctrl+c in the composer should quit")
	}
	if model.form != nil {
		t.Fatal("the composer is still open")
	}
	drafts := draftFiles(t)
	if len(drafts) != 1 {
		t.Fatalf("drafts = %v", drafts)
	}
	data, _ := os.ReadFile(drafts[0])
	if !strings.Contains(string(data), "ada@example.org") {
		t.Fatalf("draft lost the text:\n%s", data)
	}
}

func TestCtrlCWithAnUnwritableDraftKeepsTheComposerOpen(t *testing.T) {
	model := composerModel(t)
	model = typeInto(t, model, "ada@example.org")
	// A file where the drafts folder should be makes every save fail.
	if err := os.MkdirAll(filepath.Dir(draftDir()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(draftDir(), []byte("in the way"), 0o600); err != nil {
		t.Fatal(err)
	}
	updated, command := model.Update(ctrlC())
	model = updated.(Model)
	if isQuit(command) || model.form == nil || !strings.Contains(model.status, "not saved") {
		t.Fatalf("form open=%v status=%q", model.form != nil, model.status)
	}
}

func TestCtrlCClosesTheEventFormAndPromptsBeforeQuitting(t *testing.T) {
	model := calendarModel(t, gym())
	updated, _ := model.Update(key("e"))
	model = updated.(Model)
	if model.eventForm == nil {
		t.Fatal("e did not open the form")
	}
	updated, command := model.Update(ctrlC())
	model = updated.(Model)
	if model.eventForm != nil || isQuit(command) || model.status != "ctrl+c again quits" {
		t.Fatalf("form=%v status=%q", model.eventForm != nil, model.status)
	}
	updated, command = model.Update(ctrlC())
	if !isQuit(command) {
		t.Fatal("the second ctrl+c should quit")
	}

	model = calendarModel(t, gym())
	updated, _ = model.Update(key("n"))
	model = updated.(Model)
	model = typeInto(t, model, "Lunch tomorrow")
	updated, command = model.Update(ctrlC())
	model = updated.(Model)
	if model.calPrompt != nil || isQuit(command) || model.status != "ctrl+c again quits" {
		t.Fatalf("prompt=%v status=%q", model.calPrompt != nil, model.status)
	}
}

func TestAutosaveWritesAChangedComposerAndOnlyThen(t *testing.T) {
	model := composerModel(t)
	seq := model.autosaveSeq

	// Untouched: nothing is written.
	updated, command := model.Update(autosaveTickMsg{seq: seq})
	model = updated.(Model)
	if command == nil || len(draftFiles(t)) != 0 {
		t.Fatalf("an untouched composer wrote %v", draftFiles(t))
	}

	model = typeInto(t, model, "ada@example.org")
	updated, _ = model.Update(autosaveTickMsg{seq: seq})
	model = updated.(Model)
	drafts := draftFiles(t)
	if len(drafts) != 1 {
		t.Fatalf("autosave wrote %v", drafts)
	}

	// Unchanged since the last write: the file is left alone.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(drafts[0], old, old); err != nil {
		t.Fatal(err)
	}
	updated, _ = model.Update(autosaveTickMsg{seq: seq})
	model = updated.(Model)
	if info, _ := os.Stat(drafts[0]); !info.ModTime().Equal(old) {
		t.Fatal("autosave rewrote an unchanged draft")
	}

	// A tick from an earlier composer ends its chain.
	model = typeInto(t, model, "x")
	updated, command = model.Update(autosaveTickMsg{seq: seq - 1})
	if command != nil {
		t.Fatal("a stale tick must not reschedule")
	}
	if data, _ := os.ReadFile(drafts[0]); strings.Contains(string(data), "ada@example.orgx") {
		t.Fatal("a stale tick saved")
	}
}

func TestUntouchedRepliesAndNewMessagesAreNotKeptAsDrafts(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
	reply := compose.Reply("gmail", "/mail/gmail/Inbox/cur/1", maildir.Content{FromAddr: "ada@example.org", Subject: "Hello", Body: "Question?", Date: time.Now()}, false)
	updated, _ := model.openComposer("Reply", reply, "", true)
	model = updated.(Model)
	updated, _ = model.Update(escKey())
	model = updated.(Model)
	if model.form != nil || len(draftFiles(t)) != 0 || !strings.Contains(model.status, "Nothing written") {
		t.Fatalf("untouched reply: form=%v drafts=%v status=%q", model.form != nil, draftFiles(t), model.status)
	}

	// Typing in the body makes it a draft.
	updated, _ = model.openComposer("Reply", reply, "", true)
	model = updated.(Model)
	model = typeInto(t, model, "Thanks")
	updated, _ = model.Update(escKey())
	model = updated.(Model)
	if len(draftFiles(t)) != 1 {
		t.Fatalf("a written reply should be kept: %v", draftFiles(t))
	}

	// A new message holds only the signature until something is typed.
	for _, path := range draftFiles(t) {
		os.Remove(path)
	}
	updated, _ = model.Update(key("c"))
	model = updated.(Model)
	updated, _ = model.Update(escKey())
	model = updated.(Model)
	if len(draftFiles(t)) != 0 {
		t.Fatalf("an untouched new message was kept: %v", draftFiles(t))
	}
}

func TestAutosavedButRevertedComposerLeavesNoDraft(t *testing.T) {
	model := composerModel(t)
	model = typeInto(t, model, "a")
	updated, _ := model.Update(autosaveTickMsg{seq: model.autosaveSeq})
	model = updated.(Model)
	if len(draftFiles(t)) != 1 {
		t.Fatal("autosave did not write")
	}
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
	model = updated.(Model)
	updated, _ = model.Update(escKey())
	if len(draftFiles(t)) != 0 {
		t.Fatalf("drafts = %v", draftFiles(t))
	}
}

func TestReopenedDraftIsKeptWhenClosedUnchanged(t *testing.T) {
	model := composerModel(t)
	model = typeInto(t, model, "ada@example.org")
	updated, _ := model.Update(escKey())
	model = updated.(Model)
	drafts := draftFiles(t)
	if len(drafts) != 1 {
		t.Fatalf("drafts = %v", drafts)
	}
	selected := model.loadDrafts()[0]
	updated, _ = model.openDraft(selected)
	model = updated.(Model)
	updated, _ = model.Update(escKey())
	if len(draftFiles(t)) != 1 {
		t.Fatal("closing a reopened draft unchanged lost it")
	}
}

// pendingNudgeModel has a nudge waiting on its debounce, with the fake
// calendar tool in place.
func pendingNudgeModel(t *testing.T) (Model, string) {
	t.Helper()
	log := fakeCalendarTool(t, `{"ok":true,"id":"gym1"}`)
	model := calendarModel(t, gym())
	updated, _ := model.Update(key(">"))
	model = updated.(Model)
	if model.nudging == nil {
		t.Fatal("no nudge pending")
	}
	return model, log
}

// runCommandParts runs a command, or each part of a batch, and returns the
// messages. Waits are shortened first (see shortQuitWait).
func runCommandParts(command tea.Cmd) []tea.Msg {
	if command == nil {
		return nil
	}
	message := command()
	if batch, ok := message.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, inner := range batch {
			out = append(out, runCommandParts(inner)...)
		}
		return out
	}
	return []tea.Msg{message}
}

func shortQuitWait(t *testing.T) {
	t.Helper()
	old := quitWait
	quitWait = 10 * time.Millisecond
	t.Cleanup(func() { quitWait = old })
}

func TestQuitAppliesAPendingNudgeAndWaitsForTheWrite(t *testing.T) {
	shortQuitWait(t)
	model, log := pendingNudgeModel(t)
	updated, command := model.Update(key("q"))
	model = updated.(Model)
	if model.nudging != nil || model.calWriting != 1 || !model.quitting {
		t.Fatalf("nudging=%v calWriting=%d quitting=%v", model.nudging != nil, model.calWriting, model.quitting)
	}
	if !strings.Contains(model.status, "Saving 1 change… q again to quit now") {
		t.Fatalf("status = %q", model.status)
	}
	var write, wait tea.Msg
	for _, message := range runCommandParts(command) {
		switch message.(type) {
		case calendarWriteMsg:
			write = message
		case quitWaitMsg:
			wait = message
		}
	}
	if write == nil || wait == nil {
		t.Fatalf("write=%v wait=%v", write, wait)
	}
	if got := calls(t, log); !strings.HasPrefix(got, "update --calendar Work --id gym1") {
		t.Fatalf("the nudge was not sent: %q", got)
	}
	// The write landing ends the wait.
	updated, command = model.Update(write)
	if !isQuit(command) {
		t.Fatal("the landed write should quit")
	}
	// So does the timeout, if the write never lands.
	updated, command = model.Update(wait)
	if !isQuit(command) {
		t.Fatal("the timeout should quit")
	}
	_ = updated
}

func TestSecondQuitKeyQuitsAtOnce(t *testing.T) {
	model, _ := pendingNudgeModel(t)
	updated, _ := model.Update(key("q"))
	model = updated.(Model)
	updated, command := model.Update(key("q"))
	if !isQuit(command) {
		t.Fatal("q again should quit at once")
	}
	// Any other key cancels the wait, so a late write does not quit.
	updated, _ = model.Update(key("j"))
	model = updated.(Model)
	if model.quitting {
		t.Fatal("another key should cancel the pending quit")
	}
	updated, command = model.Update(calendarWriteMsg{change: pendingChange{op: "update"}})
	if isQuit(command) {
		t.Fatal("a cancelled quit must not quit when the write lands")
	}
	_ = updated
}

func TestFailedWriteCancelsTheQuit(t *testing.T) {
	model, _ := pendingNudgeModel(t)
	updated, _ := model.Update(key("q"))
	model = updated.(Model)
	change := model.calPending[0]
	updated, command := model.Update(calendarWriteMsg{change: change, err: os.ErrPermission})
	model = updated.(Model)
	if isQuit(command) || model.quitting || !strings.HasPrefix(model.status, "Not saved") {
		t.Fatalf("quitting=%v status=%q", model.quitting, model.status)
	}
}

func TestQuitWithNothingPendingQuitsAtOnce(t *testing.T) {
	model := calendarModel(t, gym())
	_, command := model.Update(key("q"))
	if !isQuit(command) {
		t.Fatal("nothing in flight: q should quit")
	}
	_, command = model.Update(ctrlC())
	if !isQuit(command) {
		t.Fatal("nothing in flight: ctrl+c should quit")
	}
}

func TestQuitWaitsForASendCountdown(t *testing.T) {
	shortQuitWait(t)
	t.Setenv("HOME", t.TempDir())
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
	model.loadingMail, model.loadingCal = false, false
	model.sendCountdown = 4
	updated, command := model.Update(key("q"))
	model = updated.(Model)
	if isQuit(command) || !model.quitting || !strings.Contains(model.status, "Saving 1 change") {
		t.Fatalf("quitting=%v status=%q", model.quitting, model.status)
	}
	// Sent: the quit goes through.
	model.sendCountdown = 0
	model.draft = &compose.Draft{}
	_, command = model.Update(draftSentMsg{to: "ada@example.org"})
	if !isQuit(command) {
		t.Fatal("a sent mail should end the wait")
	}
}

// moveStore records the messages MoveTo is asked to move, refuses paths in
// gone (as a file the daemon's server move replaced) and finds the message
// again by Message-ID.
type moveStore struct {
	*fakeMailStore
	gone      map[string]bool
	movedFrom []string
	found     map[string]maildir.Message // account\x00box\x00id
	lookups   []string
}

func (s *moveStore) MoveTo(message maildir.Message, box string) (string, error) {
	if s.gone[message.Path] {
		return "", &os.PathError{Op: "lstat", Path: message.Path, Err: os.ErrNotExist}
	}
	s.movedFrom = append(s.movedFrom, message.Path)
	return s.fakeMailStore.MoveTo(message, box)
}

func (s *moveStore) FindByMessageID(account, box, messageID string) (maildir.Message, bool) {
	s.lookups = append(s.lookups, account+"/"+box+"/"+messageID)
	found, ok := s.found[account+"\x00"+box+"\x00"+messageID]
	return found, ok
}

func moveModel(t *testing.T, store *moveStore, messages ...maildir.Message) Model {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	model := NewModel(store, &fakeCalendarStore{}, Options{})
	model.loadingMail, model.loadingCal = false, false
	model.boxes = []string{maildir.InboxBox, "@Reply", maildir.ArchiveBox}
	model.accounts = []string{"a"}
	model.messages = messages
	return model
}

func inboxMessage() maildir.Message {
	return maildir.Message{Path: "/mail/a/Inbox/cur/1700.x:2,S", Account: "a", Box: maildir.InboxBox, Subject: "Hello", MessageID: "<one@x>", Date: time.Now()}
}

func newMoveStore() *moveStore {
	return &moveStore{fakeMailStore: &fakeMailStore{}, gone: map[string]bool{}, found: map[string]maildir.Message{}}
}

// perform runs the command a key returned and feeds its message back.
func perform(t *testing.T, model Model, command tea.Cmd) (Model, tea.Cmd) {
	t.Helper()
	if command == nil {
		t.Fatal("expected a command")
	}
	updated, next := model.Update(command())
	return updated.(Model), next
}

func TestArchiveFileAndDeleteAllUndo(t *testing.T) {
	type step struct {
		name   string
		keys   []string
		status string
		toBox  string
	}
	for _, test := range []step{
		{"archive", []string{"a", "a"}, "Archived · u undoes", maildir.ArchiveBox},
		{"file", []string{"f", "r"}, "Filed to Reply · u undoes", "@Reply"},
		{"delete", []string{"d"}, "Moved to Trash · u undoes", "Trash"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newMoveStore()
			model := moveModel(t, store, inboxMessage())
			var command tea.Cmd
			for _, k := range test.keys {
				var updated tea.Model
				updated, command = model.Update(key(k))
				model = updated.(Model)
			}
			model, _ = perform(t, model, command)
			if model.status != test.status {
				t.Fatalf("status = %q, want %q", model.status, test.status)
			}
			move := model.lastMove
			if move == nil || move.fromBox != maildir.InboxBox || move.toBox != test.toBox || move.account != "a" || move.messageID != "<one@x>" || move.at.IsZero() {
				t.Fatalf("lastMove = %+v", move)
			}
			if !strings.Contains(move.path, "moved") {
				t.Fatalf("lastMove.path = %q, want the path MoveTo returned", move.path)
			}

			updated, command := model.Update(key("u"))
			model = updated.(Model)
			if model.lastMove != nil {
				t.Fatal("undo should use the move up")
			}
			model, _ = perform(t, model, command)
			if got := store.moved[len(store.moved)-1]; got != maildir.InboxBox {
				t.Fatalf("undo moved to %q, want Inbox", got)
			}
			if got := store.movedFrom[len(store.movedFrom)-1]; got != move.path {
				t.Fatalf("undo moved %q, want %q", got, move.path)
			}
			if model.pendingStatus != "Restored to Inbox" {
				t.Fatalf("pendingStatus = %q", model.pendingStatus)
			}
		})
	}
}

func TestUndoFindsTheMessageByIDWhenTheMovedFileIsGone(t *testing.T) {
	store := newMoveStore()
	model := moveModel(t, store, inboxMessage())
	updated, _ := model.Update(key("a"))
	updated, command := updated.(Model).Update(key("a"))
	model, _ = perform(t, updated.(Model), command)
	// The daemon moved it on the server and removed the local copy; mbsync
	// later downloaded it under another name.
	store.gone[model.lastMove.path] = true
	downloaded := maildir.Message{Path: "/mail/a/Archive/cur/1800.y,U=7:2,S", Account: "a", Box: maildir.ArchiveBox, MessageID: "<one@x>"}
	store.found["a\x00Archive\x00<one@x>"] = downloaded

	updated, command = model.Update(key("u"))
	model, _ = perform(t, updated.(Model), command)
	if strings.Join(store.lookups, ",") != "a/Archive/<one@x>" {
		t.Fatalf("lookups = %v", store.lookups)
	}
	if store.movedFrom[len(store.movedFrom)-1] != downloaded.Path || model.pendingStatus != "Restored to Inbox" {
		t.Fatalf("movedFrom=%v pending=%q", store.movedFrom, model.pendingStatus)
	}
}

func TestUndoReportsAFailureWhenTheMessageCannotBeFound(t *testing.T) {
	store := newMoveStore()
	model := moveModel(t, store, inboxMessage())
	updated, command := model.Update(key("d"))
	model, _ = perform(t, updated.(Model), command)
	store.gone[model.lastMove.path] = true
	updated, command = model.Update(key("u"))
	model, _ = perform(t, updated.(Model), command)
	if !strings.HasPrefix(model.status, "Undo failed: ") {
		t.Fatalf("status = %q", model.status)
	}
}

func TestUndoIsForTheMailPane(t *testing.T) {
	store := newMoveStore()
	model := moveModel(t, store, inboxMessage())
	updated, command := model.Update(key("d"))
	model, _ = perform(t, updated.(Model), command)
	model.focus = paneAgenda
	updated, command = model.Update(key("u"))
	model = updated.(Model)
	if command != nil || model.lastMove == nil {
		t.Fatal("u in the calendar pane must leave the mail move alone")
	}
	model.focus = paneMail
	if _, command = model.Update(key("u")); command == nil {
		t.Fatal("u in the mail pane should undo")
	}
}

func TestNothingToUndo(t *testing.T) {
	model := moveModel(t, newMoveStore(), inboxMessage())
	updated, command := model.Update(key("u"))
	if command != nil || updated.(Model).status != "Nothing to undo" {
		t.Fatalf("status = %q", updated.(Model).status)
	}
}

func TestFilingIsAllowedFromArchiveAndTrashButNotSentOrDrafts(t *testing.T) {
	for _, box := range []string{maildir.ArchiveBox, "Trash"} {
		model := moveModel(t, newMoveStore(), maildir.Message{Path: "/mail/a/" + box + "/cur/1", Account: "a", Box: box})
		model.mailBox = slices.Index(model.boxes, box)
		if model.mailBox < 0 {
			model.boxes = append(model.boxes, box)
			model.mailBox = len(model.boxes) - 1
		}
		updated, _ := model.Update(key("f"))
		model = updated.(Model)
		if !model.filing {
			t.Fatalf("f in %s should open the picker; status %q", box, model.status)
		}
		if box == maildir.ArchiveBox && slices.Contains(model.fileTargets, maildir.ArchiveBox) {
			t.Fatalf("Archive should not offer itself: %v", model.fileTargets)
		}
		if slices.Contains(model.fileTargets, draftsBox) {
			t.Fatalf("targets include Drafts: %v", model.fileTargets)
		}
	}
	for _, box := range []string{maildir.SentBox, draftsBox} {
		model := moveModel(t, newMoveStore(), maildir.Message{Path: "/mail/a/" + box + "/cur/1", Account: "a", Box: box})
		model.boxes = append(model.boxes, box)
		model.mailBox = len(model.boxes) - 1
		updated, _ := model.Update(key("f"))
		model = updated.(Model)
		if model.filing || !strings.Contains(model.status, "read-only") {
			t.Fatalf("%s: filing=%v status=%q", box, model.filing, model.status)
		}
	}
}

func TestTheReplyJudgesArchiveCanBeUndone(t *testing.T) {
	store := newMoveStore()
	reply := maildir.Message{Path: "/mail/a/@Reply/cur/9", Account: "a", Box: "@Reply", MessageID: "<r@x>"}
	model := moveModel(t, store, reply)
	updated, _ := model.Update(replySettledMsg{oldPath: reply.Path, newPath: "/mail/a/Archive/cur/9", reason: "answered"})
	model = updated.(Model)
	move := model.lastMove
	if move == nil || move.fromBox != "@Reply" || move.toBox != maildir.ArchiveBox || move.messageID != "<r@x>" || move.path != "/mail/a/Archive/cur/9" {
		t.Fatalf("lastMove = %+v", move)
	}
	if !strings.HasSuffix(model.status, "· u undoes") {
		t.Fatalf("status = %q", model.status)
	}
}

func TestDraftDeleteStillUndoesThroughLastMove(t *testing.T) {
	model := composerModel(t)
	model = typeInto(t, model, "ada@example.org")
	updated, _ := model.Update(escKey())
	model = updated.(Model)
	model.boxes = []string{maildir.InboxBox, draftsBox}
	model.mailBox = 1
	updated, _ = model.Update(key("d"))
	model = updated.(Model)
	if len(draftFiles(t)) != 0 || model.lastMove == nil || model.lastMove.fromBox != draftsBox {
		t.Fatalf("drafts=%v lastMove=%+v", draftFiles(t), model.lastMove)
	}
	updated, _ = model.Update(key("u"))
	model = updated.(Model)
	if len(draftFiles(t)) != 1 || model.status != "Draft restored" {
		t.Fatalf("drafts=%v status=%q", draftFiles(t), model.status)
	}
}

func TestMailActionsOnADraftRowSayItIsADraft(t *testing.T) {
	store := newMoveStore()
	model := moveModel(t, store)
	model.messages = []maildir.Message{{Path: "/home/x/drafts/2026.md", Box: draftsBox, Subject: "Half-written"}}
	model.boxes = []string{maildir.InboxBox, draftsBox}
	model.mailBox = 1
	for _, keys := range [][]string{{"m"}, {"a"}, {"R"}, {"A"}, {"F"}} {
		updated, command := model.Update(key(keys[0]))
		after := updated.(Model)
		if command != nil || after.status != "This is a draft; enter edits it" || after.pendingArchive != "" {
			t.Fatalf("%v: command=%v status=%q pending=%q", keys, command != nil, after.status, after.pendingArchive)
		}
	}
	updated, command := model.saveAttachments()
	if command != nil || updated.(Model).status != "This is a draft; enter edits it" {
		t.Fatalf("S: status %q", updated.(Model).status)
	}
	if len(store.movedFrom) != 0 {
		t.Fatalf("a draft was moved: %v", store.movedFrom)
	}
}

func TestPasteReachesTheCalendarPromptsAndEventForm(t *testing.T) {
	// Quick-add: one line.
	model := calendarModel(t, gym())
	updated, _ := model.Update(key("n"))
	model = updated.(Model)
	updated, _ = model.Update(tea.PasteMsg{Content: "Lunch with Ada\r\ntomorrow  12:30\t1h\x1b[31m\n"})
	model = updated.(Model)
	if got := model.calPrompt.input; got != "Lunch with Ada tomorrow 12:30 1h" {
		t.Fatalf("quick-add input = %q", got)
	}

	// Rebook box.
	model = calendarModel(t, gym())
	updated, _ = model.Update(key("m"))
	model = updated.(Model)
	updated, _ = model.Update(tea.PasteMsg{Content: "fri\n10:00"})
	model = updated.(Model)
	if got := model.calPrompt.input; got != "fri 10:00" {
		t.Fatalf("rebook input = %q", got)
	}

	// Event form: single-line fields collapse, notes keep their lines, the
	// calendar field ignores the paste.
	model = calendarModel(t, gym())
	updated, _ = model.Update(key("e"))
	model = updated.(Model)
	form := model.eventForm
	form.title, form.location, form.notes = "", "", ""
	form.field = formLocation
	updated, _ = model.Update(tea.PasteMsg{Content: "Room 1.02\n Atrium\r\n"})
	model = updated.(Model)
	if form.location != "Room 1.02 Atrium" {
		t.Fatalf("location = %q", form.location)
	}
	form.field = formNotes
	updated, _ = model.Update(tea.PasteMsg{Content: "Agenda:\r\n- one\r\n- two\x1b[31m"})
	model = updated.(Model)
	if form.notes != "Agenda:\n- one\n- two" {
		t.Fatalf("notes = %q", form.notes)
	}
	before := form.calendar
	form.field = formCalendar
	updated, _ = model.Update(tea.PasteMsg{Content: "University"})
	model = updated.(Model)
	if form.calendar != before || form.title != "" {
		t.Fatalf("calendar field changed: %q", form.calendar)
	}
}
