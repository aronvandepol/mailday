package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/aronvandepol/mailday/internal/maildir"
	"github.com/aronvandepol/mailday/internal/syncd"
)

var threadBase = time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)

// threadMail builds an Inbox message; refs go into References, the first of
// them being the root.
func threadMail(name, id, refs, subject, from string, age time.Duration, unread bool) maildir.Message {
	directory := "cur"
	suffix := ":2,S"
	if unread {
		directory, suffix = "new", ""
	}
	return maildir.Message{
		Path: "/m/a/Inbox/" + directory + "/" + name + ".x" + suffix, Account: "a", Box: maildir.InboxBox,
		Subject: subject, From: from, FromAddr: strings.ToLower(from) + "@x.org", To: "Sam <sam@x.org>",
		MessageID: id, References: refs, Date: threadBase.Add(-age), Unread: unread,
	}
}

// threadModel loads messages into a model with the clock fixed.
func threadModel(t *testing.T, store MailStore, messages ...maildir.Message) Model {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	if store == nil {
		store = &fakeMailStore{}
	}
	model := NewModel(store, &fakeCalendarStore{}, Options{})
	model.now = func() time.Time { return threadBase }
	model.width, model.height = 100, 30
	updated, _ := model.Update(mailLoadedMsg{result: maildir.ListResult{Messages: messages, Accounts: []string{"a"}, Boxes: []string{maildir.InboxBox, "@Reply", "@ESS"}}})
	model = updated.(Model)
	model.loadingCal = false
	return model
}

func space() tea.KeyPressMsg { return tea.KeyPressMsg(tea.Key{Code: tea.KeySpace, Text: " "}) }

func rowSubjects(model Model) []string {
	var subjects []string
	for _, row := range model.mailRows() {
		subjects = append(subjects, fmt.Sprintf("%s/%d", model.messages[row.index].Subject, row.count()))
	}
	return subjects
}

// A conversation of three, an unrelated message, and a second conversation.
func conversationMail() []maildir.Message {
	return []maildir.Message{
		threadMail("3", "<c@x>", "<a@x> <b@x>", "Re: Budget", "Ada", 1*time.Hour, false),
		threadMail("5", "<z@x>", "", "Lunch", "Zed", 2*time.Hour, false),
		threadMail("2", "<b@x>", "<a@x>", "Re: Budget", "Sam", 3*time.Hour, false),
		threadMail("1", "<a@x>", "", "Budget", "Ada", 5*time.Hour, false),
	}
}

func TestThreadsGroupByReferencesRoot(t *testing.T) {
	model := threadModel(t, nil, conversationMail()...)
	got := strings.Join(rowSubjects(model), "|")
	if got != "Re: Budget/3|Lunch/1" {
		t.Fatalf("rows = %s", got)
	}
	head := model.messages[model.mailRows()[0].index]
	if head.MessageID != "<c@x>" {
		t.Fatalf("the row shows %s, want the newest message", head.MessageID)
	}
}

func TestThreadsJoinRepliesWhoseRootIsNotLoaded(t *testing.T) {
	// The original is in another box or gone; both replies name it first.
	// A reply with only In-Reply-To joins through the id it answers.
	model := threadModel(t, nil,
		threadMail("2", "<c@x>", "<a@x> <b@x>", "Re: Budget", "Ada", 1*time.Hour, false),
		threadMail("1", "<b@x>", "<a@x>", "Re: Budget", "Sam", 2*time.Hour, false),
		maildir.Message{Path: "/m/a/Inbox/cur/3.x:2,S", Account: "a", Box: maildir.InboxBox, Subject: "Re: Budget", From: "Bo", MessageID: "<d@x>", InReplyTo: "<b@x>", Date: threadBase.Add(-30 * time.Minute)},
	)
	if got := strings.Join(rowSubjects(model), "|"); got != "Re: Budget/3" {
		t.Fatalf("rows = %s", got)
	}
}

func TestThreadsFallBackToSubjectAndParticipants(t *testing.T) {
	ada := threadMail("1", "", "", "Meeting Friday", "Ada", 3*time.Hour, false)
	ada.To = "sam@x.org"
	reply := threadMail("2", "", "", "AW: [list] Re: meeting friday", "Sam", 1*time.Hour, false)
	reply.To = "Ada <ada@x.org>"
	stranger := threadMail("3", "", "", "Meeting Friday", "Eve", 2*time.Hour, false)
	stranger.To = "other@x.org"
	model := threadModel(t, nil, reply, stranger, ada)
	// The reply joins Ada's message; Eve's same-subject mail shares no
	// participants with it and has no reply marker of its own.
	if got := strings.Join(rowSubjects(model), "|"); got != "AW: [list] Re: meeting friday/2|Meeting Friday/1" {
		t.Fatalf("rows = %s", got)
	}
}

func TestIdenticalSubjectsWithoutRepliesStaySeparate(t *testing.T) {
	first := threadMail("1", "", "", "Your weekly digest", "News", 24*time.Hour, false)
	second := threadMail("2", "", "", "Your weekly digest", "News", 1*time.Hour, false)
	model := threadModel(t, nil, second, first)
	if got := len(model.mailRows()); got != 2 {
		t.Fatalf("%d rows, want 2: %v", got, rowSubjects(model))
	}
}

func TestThreadsDoNotCrossAccounts(t *testing.T) {
	one := threadMail("1", "<a@x>", "", "Hi", "Ada", 2*time.Hour, false)
	two := threadMail("2", "<a@x>", "", "Hi", "Ada", 1*time.Hour, false)
	two.Account = "b"
	two.Path = "/m/b/Inbox/cur/2.x:2,S"
	model := threadModel(t, nil, two, one)
	if got := len(model.mailRows()); got != 2 {
		t.Fatalf("%d rows, want one per account", got)
	}
}

func TestNormaliseSubject(t *testing.T) {
	for subject, want := range map[string]string{
		"Re: Budget":                 "budget",
		"RE: Fwd: Budget":            "budget",
		"Antw: AW: WG: Fw: Budget":   "budget",
		"Doorst: Re[2]: Budget":      "budget",
		"[golang-nuts] Re: Budget":   "budget",
		"Re: [list]   Budget  plan ": "budget plan",
		"Reorganising the budget":    "reorganising the budget",
		"Re:":                        "",
	} {
		if got, _ := normaliseSubject(subject); got != want {
			t.Errorf("normaliseSubject(%q) = %q, want %q", subject, got, want)
		}
	}
	if _, replied := normaliseSubject("[list] Budget"); replied {
		t.Error("a list tag is not a reply")
	}
	if _, replied := normaliseSubject("Doorst: Budget"); !replied {
		t.Error("Doorst: is a forward")
	}
}

func TestRowShowsBadgeUnreadDotAndNewestSender(t *testing.T) {
	mail := conversationMail()
	mail[3].Unread = true // the oldest is unread, the newest is not
	mail[3].Path = "/m/a/Inbox/new/1.x"
	model := threadModel(t, nil, mail...)
	view := ansi.Strip(model.View().Content)
	if !strings.Contains(view, "New for You") || !strings.Contains(view, "Re: Budget (3)") {
		t.Fatalf("view:\n%s", view)
	}
	rows := model.mailRows()
	if rows[0].section != 0 || !rows[0].unread || rows[1].section != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	var line string
	for _, candidate := range strings.Split(view, "\n") {
		if strings.Contains(candidate, "Re: Budget (3)") {
			line = candidate
		}
	}
	if !strings.Contains(line, "●") {
		t.Fatalf("no unread dot on %q", line)
	}
	if !strings.Contains(view, "Ada") { // the newest message's sender, on the second line
		t.Fatalf("sender missing:\n%s", view)
	}
}

func TestThreadsSortByTheirNewestMessage(t *testing.T) {
	// The conversation's newest message is newer than Lunch, though its first is older.
	model := threadModel(t, nil, conversationMail()...)
	if got := rowSubjects(model)[0]; got != "Re: Budget/3" {
		t.Fatalf("first row = %s", got)
	}
	mail := conversationMail()
	mail[0].Date = threadBase.Add(-10 * time.Hour) // now the oldest
	mail[0], mail[1], mail[2], mail[3] = mail[1], mail[2], mail[3], mail[0]
	model = threadModel(t, nil, mail...)
	if got := strings.Join(rowSubjects(model), "|"); got != "Lunch/1|Re: Budget/3" {
		t.Fatalf("rows = %s", got)
	}
}

func TestSpaceOpensAndClosesAConversation(t *testing.T) {
	model := threadModel(t, nil, conversationMail()...)
	updated, _ := model.Update(space())
	model = updated.(Model)
	if rows := model.mailRows(); len(rows) != 4 || !rows[1].child || !rows[2].child || rows[3].child {
		t.Fatalf("after space: %d rows %+v", len(rows), rows)
	}
	view := ansi.Strip(model.View().Content)
	if !strings.Contains(view, "↳ Sam") || !strings.Contains(view, "↳ Ada") {
		t.Fatalf("older messages are not listed under the row:\n%s", view)
	}

	// On an older message space closes it and the cursor goes to the top row.
	model = press(t, model, "j", "j")
	updated, _ = model.Update(space())
	model = updated.(Model)
	if len(model.mailRows()) != 2 || model.mailCursor != 0 {
		t.Fatalf("after closing: %d rows, cursor %d", len(model.mailRows()), model.mailCursor)
	}
}

func TestSpaceOnASingleMessageSaysSo(t *testing.T) {
	model := threadModel(t, nil, conversationMail()[1])
	updated, _ := model.Update(space())
	model = updated.(Model)
	if len(model.mailRows()) != 1 || !strings.Contains(model.status, "One message") {
		t.Fatalf("rows %d, status %q", len(model.mailRows()), model.status)
	}
}

func TestEnterOpensTheNewestUnreadElseTheNewest(t *testing.T) {
	mail := conversationMail()
	mail[2].Unread, mail[2].Path = true, "/m/a/Inbox/new/2.x" // the middle message
	model := threadModel(t, nil, mail...)
	updated, _ := model.Update(key("enter"))
	opened := updated.(Model)
	if opened.contentPath != "/m/a/Inbox/new/2.x" || opened.screen != screenMail {
		t.Fatalf("opened %q, want the unread middle message", opened.contentPath)
	}
	if opened.mailCursor != 0 {
		t.Fatalf("cursor left the conversation's row: %d", opened.mailCursor)
	}

	model = threadModel(t, nil, conversationMail()...)
	updated, _ = model.Update(key("enter"))
	if got := updated.(Model).contentPath; got != "/m/a/Inbox/cur/3.x:2,S" {
		t.Fatalf("opened %q, want the newest", got)
	}
}

func TestReaderWalksThroughEveryMessageOfAConversation(t *testing.T) {
	model := threadModel(t, nil, conversationMail()...)
	updated, _ := model.Update(key("enter"))
	model = updated.(Model)
	updated, _ = model.Update(bodyLoadedMsg{path: model.contentPath, content: maildir.Content{}})
	model = updated.(Model)
	updated, _ = model.Update(key("n"))
	if got := updated.(Model).contentPath; got != "/m/a/Inbox/cur/2.x:2,S" {
		t.Fatalf("n opened %q, want the next message of the conversation", got)
	}
}

// pathStore records which files moved where and gives each a new path.
type pathStore struct {
	fakeMailStore
	moves []string
}

func (s *pathStore) MoveTo(message maildir.Message, box string) (string, error) {
	s.moves = append(s.moves, filepath.Base(message.Path)+">"+box)
	return "/m/a/" + box + "/cur/" + filepath.Base(message.Path), nil
}

func TestArchivingAConversationMovesEveryMessageAndUndoRestoresAll(t *testing.T) {
	store := &pathStore{}
	model := threadModel(t, store, conversationMail()...)
	updated, command := model.Update(key("a"))
	model = updated.(Model)
	if command != nil || !strings.Contains(model.status, "these 3 messages") {
		t.Fatalf("first a: command=%v status=%q", command != nil, model.status)
	}
	updated, command = model.Update(key("a"))
	model = updated.(Model)
	updated, _ = model.Update(command())
	model = updated.(Model)
	if got := strings.Join(store.moves, ","); got != "3.x:2,S>Archive,2.x:2,S>Archive,1.x:2,S>Archive" {
		t.Fatalf("moves = %s", got)
	}
	if model.status != "Archived 3 messages · u undoes" {
		t.Fatalf("status = %q", model.status)
	}
	if got := strings.Join(rowSubjects(model), "|"); got != "Lunch/1" {
		t.Fatalf("rows after archiving = %s", got)
	}

	store.moves = nil
	updated, command = model.Update(key("u"))
	model = updated.(Model)
	result := command().(messageRestoredMsg)
	if got := strings.Join(store.moves, ","); got != "3.x:2,S>Inbox,2.x:2,S>Inbox,1.x:2,S>Inbox" || result.count != 3 {
		t.Fatalf("undo moves = %s, count %d", got, result.count)
	}
	updated, _ = model.Update(result)
	model = updated.(Model)
	if model.pendingStatus != "Restored 3 messages to Inbox" {
		t.Fatalf("pending status = %q", model.pendingStatus)
	}
}

func TestBulkArchiveLeavesOtherBoxesOfASearchedConversationAlone(t *testing.T) {
	// A search shows a conversation across boxes; a on it acts on the
	// newest message's box.
	store := &pathStore{}
	mail := conversationMail()
	mail[3].Box, mail[3].Path = maildir.ArchiveBox, "/m/a/Archive/cur/1.x:2,S"
	model := threadModel(t, store, mail...)
	model.query = "budget"
	updated, _ := model.Update(key("a"))
	updated, command := updated.Update(key("a"))
	updated, _ = updated.Update(command())
	if got := strings.Join(store.moves, ","); got != "3.x:2,S>Archive,2.x:2,S>Archive" {
		t.Fatalf("moves = %s", got)
	}
	if !strings.Contains(updated.(Model).status, "Archived 2 messages") {
		t.Fatalf("status = %q", updated.(Model).status)
	}
}

func TestDeletingAConversationGoesToTrashAndUndoReturnsIt(t *testing.T) {
	store := &pathStore{}
	model := threadModel(t, store, conversationMail()...)
	// Several messages, some never shown: the first d only asks.
	updated, command := model.Update(key("d"))
	model = updated.(Model)
	if command != nil || !strings.Contains(model.status, "Press d again") || len(store.moves) != 0 {
		t.Fatalf("one d moved something: %q %v", model.status, store.moves)
	}
	updated, command = model.Update(key("d"))
	model = updated.(Model)
	updated, _ = model.Update(command())
	model = updated.(Model)
	if got := strings.Join(store.moves, ","); got != "3.x:2,S>Trash,2.x:2,S>Trash,1.x:2,S>Trash" {
		t.Fatalf("moves = %s", got)
	}
	if model.status != "Moved 3 messages to Trash · u undoes" {
		t.Fatalf("status = %q", model.status)
	}
	if len(model.lastMove.more) != 2 {
		t.Fatalf("undo holds %d extra messages, want 2", len(model.lastMove.more))
	}
}

func TestFilingAConversationFilesEveryMessage(t *testing.T) {
	store := &pathStore{}
	model := threadModel(t, store, conversationMail()...)
	model.boxes = []string{"Inbox", "@Reply", "@ESS"}
	updated, _ := model.Update(key("f"))
	model = updated.(Model)
	if !model.filing {
		t.Fatal("f did not open the box picker")
	}
	updated, command := model.Update(key("a")) // f a: ESS
	model = updated.(Model)
	updated, _ = model.Update(command())
	model = updated.(Model)
	if got := strings.Join(store.moves, ","); got != "3.x:2,S>@ESS,2.x:2,S>@ESS,1.x:2,S>@ESS" {
		t.Fatalf("moves = %s", got)
	}
	if model.status != "Filed 3 messages to ESS · u undoes" || model.filing {
		t.Fatalf("status %q filing %v", model.status, model.filing)
	}
	// The next f on a single message files that one only.
	store.moves = nil
	model = press(t, model, "f")
	updated, command = model.Update(key("a"))
	updated, _ = updated.Update(command())
	if got := strings.Join(store.moves, ","); got != "5.x:2,S>@ESS" {
		t.Fatalf("single filing moved %s", got)
	}
}

func TestMarkingAConversation(t *testing.T) {
	mail := conversationMail()
	mail[2].Unread, mail[2].Path = true, "/m/a/Inbox/new/2.x"
	model := threadModel(t, nil, mail...)
	updated, command := model.Update(key("m"))
	model = updated.(Model)
	updated, _ = model.Update(command())
	model = updated.(Model)
	for _, message := range model.messages {
		if message.Unread {
			t.Fatalf("%s is still unread", message.Path)
		}
	}
	if model.status != "Marked 3 messages read" {
		t.Fatalf("status = %q", model.status)
	}
	// All read: m marks the lot unread, and the cursor follows to the top.
	updated, command = model.Update(key("m"))
	updated, _ = updated.Update(command())
	model = updated.(Model)
	if model.status != "Marked 3 messages unread" || !model.mailRows()[0].unread {
		t.Fatalf("status %q", model.status)
	}
}

func TestExpandedConversationActsOnTheMessageUnderTheCursor(t *testing.T) {
	store := &pathStore{}
	model := threadModel(t, store, conversationMail()...)
	updated, _ := model.Update(space())
	model = updated.(Model)
	model = press(t, model, "j") // an older message
	model = press(t, model, "a")
	updated, command := model.Update(key("a"))
	updated, _ = updated.Update(command())
	if got := strings.Join(store.moves, ","); got != "2.x:2,S>Archive" {
		t.Fatalf("moves = %s", got)
	}
}

func TestThreadsOffKeepsOneRowPerMessage(t *testing.T) {
	t.Setenv("MAILDAY_THREADS", "off")
	model := threadModel(t, nil, conversationMail()...)
	if len(model.mailRows()) != 4 {
		t.Fatalf("%d rows with threads off", len(model.mailRows()))
	}
	updated, _ := model.Update(space())
	if strings.Contains(updated.(Model).status, "conversation") || len(updated.(Model).mailRows()) != 4 {
		t.Fatal("space did something with threads off")
	}
	if strings.Contains(ansi.Strip(model.View().Content), "(3)") {
		t.Fatal("a badge shows with threads off")
	}
}

func TestSearchResultsGroupAndTrashStaysLast(t *testing.T) {
	mail := conversationMail()
	mail[3].Box, mail[3].Path = "Trash", "/m/a/Trash/cur/1.x:2,S" // the original was deleted
	model := threadModel(t, nil, mail...)
	model.query = "budget"
	subjects := strings.Join(rowSubjects(model), "|")
	if subjects != "Re: Budget/2|Budget/1" {
		t.Fatalf("rows = %s", subjects)
	}
	if rows := model.mailRows(); rows[1].section != 2 {
		t.Fatalf("the trashed hit is in section %d", rows[1].section)
	}
}

func TestListRowsFitTheWidth(t *testing.T) {
	for _, width := range []int{40, 80, 130} {
		model := threadModel(t, nil, conversationMail()...)
		model.width = width
		updated, _ := model.Update(space())
		model = updated.(Model)
		for _, line := range strings.Split(model.View().Content, "\n") {
			if got := ansi.StringWidth(line); got > width {
				t.Errorf("width %d: line is %d wide: %q", width, got, ansi.Strip(line))
			}
		}
	}
}

func TestGroupingManyMessagesIsFast(t *testing.T) {
	var mail []maildir.Message
	for number := range 3000 {
		refs := ""
		if number%3 != 0 {
			refs = fmt.Sprintf("<m%d@x>", number-number%3)
		}
		mail = append(mail, threadMail(fmt.Sprint(number), fmt.Sprintf("<m%d@x>", number), refs, fmt.Sprintf("Subject %d", number/3), "Ada", time.Duration(number)*time.Minute, number%7 == 0))
	}
	model := threadModel(t, nil, mail...)
	started := time.Now()
	rows := model.mailRows()
	if cold := time.Since(started); cold > 150*time.Millisecond {
		t.Fatalf("grouping 3000 messages takes %v", cold)
	}
	if len(rows) != 1000 {
		t.Fatalf("%d rows, want 1000", len(rows))
	}
	// Every key press asks for the rows several times; repeats are reused.
	started = time.Now()
	for range 50 {
		model.mailRows()
	}
	if repeat := time.Since(started) / 50; repeat > 5*time.Millisecond {
		t.Fatalf("a repeated request takes %v", repeat)
	}
}

func TestRowsFollowChangesMadeInPlace(t *testing.T) {
	model := threadModel(t, nil, conversationMail()...)
	if rows := model.mailRows(); rows[0].section != 1 {
		t.Fatalf("setup: section %d", rows[0].section)
	}
	model.messages[2].Unread = true // marked unread without a new slice
	if rows := model.mailRows(); rows[0].section != 0 || !rows[0].unread {
		t.Fatalf("a changed flag did not reach the rows: %+v", rows[0])
	}
	model.query = "lunch"
	if got := strings.Join(rowSubjects(model), "|"); got != "Lunch/1" {
		t.Fatalf("a new search did not reach the rows: %s", got)
	}
	model.query = ""
	model.threads.expanded = map[string]bool{model.mailRows()[0].key: true}
	if len(model.mailRows()) != 4 {
		t.Fatal("opening a conversation did not reach the rows")
	}
}

// conversationStore is a mail store that reads messages by file path, as
// maildir.Store does for notmuch results.
type conversationStore struct {
	fakeMailStore
	known map[string]maildir.Message
}

func (s *conversationStore) Lookup(paths []string) []maildir.Message {
	var found []maildir.Message
	for _, path := range paths {
		if message, ok := s.known[path]; ok {
			found = append(found, message)
		}
	}
	return found
}

// fakeThreadNotmuch answers the two searches T makes and logs its arguments.
func fakeThreadNotmuch(t *testing.T, files ...string) string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	script := "#!/bin/sh\necho \"$@\" >> " + log + "\n" +
		"case \"$*\" in\n" +
		"*--output=threads*) echo thread:0000000000000001 ;;\n" +
		"*--output=files*) printf '%s\\n' " + strings.Join(files, " ") + " ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(bin, "notmuch"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func openReader(t *testing.T, model Model) Model {
	t.Helper()
	updated, _ := model.Update(key("enter"))
	model = updated.(Model)
	updated, _ = model.Update(bodyLoadedMsg{path: model.contentPath, content: maildir.Content{}})
	return updated.(Model)
}

func TestTListsTheConversationAcrossBoxesFromNotmuch(t *testing.T) {
	inbox := threadMail("2", "<b@x>", "<a@x>", "Re: Budget", "Sam", 1*time.Hour, false)
	archived := threadMail("1", "<a@x>", "", "Budget", "Ada", 5*time.Hour, false)
	archived.Box, archived.Path = maildir.ArchiveBox, "/m/a/Archive/cur/1.x:2,S"
	sent := threadMail("3", "<c@x>", "<a@x> <b@x>", "Re: Budget", "Sam", 30*time.Minute, false)
	sent.Box, sent.Path = maildir.SentBox, "/m/a/Sent/cur/3.x:2,S"
	store := &conversationStore{known: map[string]maildir.Message{sent.Path: sent}}
	log := fakeThreadNotmuch(t, sent.Path, archived.Path, inbox.Path)

	model := threadModel(t, store, inbox, archived) // the Sent copy is not loaded
	model = openReader(t, model)
	updated, command := model.Update(key("T"))
	model = updated.(Model)
	list := model.threads.conversation
	if list == nil || !list.loading || command == nil {
		t.Fatalf("T did not start a lookup: %+v", list)
	}
	if len(list.items) != 2 { // the loaded ones show at once
		t.Fatalf("%d items before notmuch answers", len(list.items))
	}
	updated, _ = model.Update(command())
	model = updated.(Model)
	list = model.threads.conversation
	if list.loading || len(list.items) != 3 {
		t.Fatalf("loading=%v items=%d", list.loading, len(list.items))
	}
	var order []string
	for _, item := range list.items {
		order = append(order, item.MessageID)
	}
	if strings.Join(order, " ") != "<a@x> <b@x> <c@x>" {
		t.Fatalf("order = %v, want oldest first", order)
	}
	if list.items[list.cursor].MessageID != "<b@x>" {
		t.Fatalf("cursor on %s, want the message being read", list.items[list.cursor].MessageID)
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), `search --output=threads -- id:"b@x"`) || !strings.Contains(string(calls), "search --output=files -- thread:0000000000000001") {
		t.Fatalf("notmuch calls:\n%s", calls)
	}
	view := ansi.Strip(model.View().Content)
	for _, want := range []string{"Conversation · 3 messages", "Archive", "Sent", "Inbox"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view lacks %q:\n%s", want, view)
		}
	}

	// enter opens the message under the cursor, from another box.
	updated, _ = model.Update(key("k"))
	model = updated.(Model)
	updated, _ = model.Update(key("enter"))
	model = updated.(Model)
	if model.threads.conversation != nil || model.contentPath != archived.Path {
		t.Fatalf("enter opened %q, list open %v", model.contentPath, model.threads.conversation != nil)
	}
}

func TestTFallsBackToTheLoadedMessagesWithoutNotmuch(t *testing.T) {
	model := threadModel(t, nil, conversationMail()...)
	model = openReader(t, model)
	updated, command := model.Update(key("T"))
	model = updated.(Model)
	if command != nil || model.threads.conversation == nil || model.threads.conversation.loading {
		t.Fatal("T without notmuch should list the loaded messages and not wait")
	}
	if got := len(model.threads.conversation.items); got != 3 {
		t.Fatalf("%d items, want the 3 loaded messages", got)
	}
	updated, _ = model.Update(key("esc"))
	model = updated.(Model)
	if model.threads.conversation != nil || model.screen != screenMail {
		t.Fatal("esc should close the list and stay in the reader")
	}
}

func TestTKeepsLoadedMessagesWhenNotmuchFails(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "notmuch"), []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	store := &conversationStore{}
	model := threadModel(t, store, conversationMail()...)
	model = openReader(t, model)
	updated, command := model.Update(key("T"))
	model = updated.(Model)
	updated, _ = model.Update(command())
	model = updated.(Model)
	if model.threads.conversation.loading || len(model.threads.conversation.items) != 3 || !strings.Contains(model.status, "notmuch failed") {
		t.Fatalf("items %d status %q", len(model.threads.conversation.items), model.status)
	}
}

func TestStaleConversationAnswerIsIgnored(t *testing.T) {
	model := threadModel(t, nil, conversationMail()...)
	updated, _ := model.Update(conversationLoadedMsg{path: "/elsewhere"})
	if updated.(Model).threads.conversation != nil {
		t.Fatal("an answer with no list open opened one")
	}
}

func BenchmarkMailRows(b *testing.B) {
	var mail []maildir.Message
	for number := range 3000 {
		refs := ""
		if number%3 != 0 {
			refs = fmt.Sprintf("<m%d@x>", number-number%3)
		}
		mail = append(mail, threadMail(fmt.Sprint(number), fmt.Sprintf("<m%d@x>", number), refs, fmt.Sprintf("Subject %d", number/3), "Ada", time.Duration(number)*time.Minute, number%7 == 0))
	}
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
	model.messages = mail
	b.ResetTimer()
	for range b.N {
		model.mailRows()
	}
}

func TestFilingInTheReaderAfterACancelledThreadFileMovesOneMessage(t *testing.T) {
	store := &pathStore{}
	model := threadModel(t, store, conversationMail()...)
	// f on the collapsed conversation, then esc: nothing filed.
	updated, _ := model.Update(key("f"))
	updated, _ = updated.(Model).Update(key("esc"))
	model = updated.(Model)
	if model.threads.filingGroup != nil && model.filing {
		t.Fatal("the picker should be closed")
	}
	// Open the message and file it from the reader: just that one.
	model, _ = model.startFilingForTest()
	if model.threads.filingGroup != nil {
		t.Fatal("a cancelled thread file leaked into the next f")
	}
}

// startFilingForTest calls startFiling as the reader's f does.
func (m Model) startFilingForTest() (Model, error) {
	model, _ := m.startFiling()
	return model.(Model), nil
}

// subjectPair is a message and a reply to it with no ids, for the subject
// fallback: ada wrote to sam, sam answers.
func subjectPair(subject string, apart time.Duration) []maildir.Message {
	first := threadMail("1", "", "", subject, "Ada", apart, false)
	first.To = "sam@x.org"
	reply := threadMail("2", "", "", "Re: "+subject, "Sam", 0, false)
	reply.To = "Ada <ada@x.org>"
	return []maildir.Message{reply, first}
}

func TestSubjectFallbackJoinsARealReplyChainWithoutIds(t *testing.T) {
	first := threadMail("1", "", "", "Conference budget", "Ada", 3*time.Hour, false)
	first.To = "sam@x.org, Bo <bo@x.org>"
	second := threadMail("2", "", "", "Re: Conference budget", "Sam", 2*time.Hour, false)
	second.To = "Ada <ada@x.org>"
	third := threadMail("3", "", "", "Re: Re: Conference budget", "Bo", 1*time.Hour, false)
	third.To = "Doe, Ada <ada@x.org>, sam@x.org"
	model := threadModel(t, nil, third, second, first)
	if got := strings.Join(rowSubjects(model), "|"); got != "Re: Re: Conference budget/3" {
		t.Fatalf("rows = %s", got)
	}
}

func TestSubjectFallbackComparesWholeAddresses(t *testing.T) {
	// "an@x.org" is inside "jan@x.org" but is not the same person.
	first := threadMail("1", "", "", "Conference budget", "Jan", 3*time.Hour, false)
	first.To = "sam@x.org"
	reply := threadMail("2", "", "", "Re: Conference budget", "An", 1*time.Hour, false)
	reply.To = "sam@x.org"
	reply.FromAddr = "an@x.org"
	first.FromAddr = "jan@x.org"
	other := threadMail("3", "", "", "Conference budget", "Zed", 2*time.Hour, false)
	other.FromAddr, other.To = "zed@x.org", "jan@x.org"
	// reply (from an) goes to sam; first (from jan) goes to sam: no link.
	model := threadModel(t, nil, reply, other, first)
	if got := len(model.mailRows()); got != 3 {
		t.Fatalf("%d rows, want 3: %v", got, rowSubjects(model))
	}
	// An exact match in To still joins.
	reply.To = "Jan <jan@x.org>"
	model = threadModel(t, nil, reply, first)
	if got := len(model.mailRows()); got != 1 {
		t.Fatalf("%d rows, want the exact address to join: %v", got, rowSubjects(model))
	}
}

func TestSubjectFallbackNeedsMessagesWithin60Days(t *testing.T) {
	model := threadModel(t, nil, subjectPair("Conference budget", 59*24*time.Hour)...)
	if got := len(model.mailRows()); got != 1 {
		t.Fatalf("59 days apart: %d rows, want 1", got)
	}
	model = threadModel(t, nil, subjectPair("Conference budget", 61*24*time.Hour)...)
	if got := len(model.mailRows()); got != 2 {
		t.Fatalf("61 days apart: %d rows, want 2", got)
	}
}

func TestSubjectFallbackIgnoresShortAndGenericSubjects(t *testing.T) {
	for _, subject := range []string{"Hi", "Hello", "Question", "Vraag", "Update", "Meeting", "(no subject)", "", "Ok", "Re"} {
		model := threadModel(t, nil, subjectPair(subject, time.Hour)...)
		if got := len(model.mailRows()); got != 2 {
			t.Errorf("subject %q: %d rows, want 2 (never joined by subject alone)", subject, got)
		}
	}
	// The same pair under a specific subject joins.
	model := threadModel(t, nil, subjectPair("Hello from Seoul", time.Hour)...)
	if got := len(model.mailRows()); got != 1 {
		t.Fatalf("a specific subject: %d rows, want 1", got)
	}
}

func TestGenericSubjectsStillJoinThroughIds(t *testing.T) {
	first := threadMail("1", "<a@x>", "", "Hi", "Ada", 2*time.Hour, false)
	reply := threadMail("2", "<b@x>", "<a@x>", "Re: Hi", "Sam", 1*time.Hour, false)
	model := threadModel(t, nil, reply, first)
	if got := len(model.mailRows()); got != 1 {
		t.Fatalf("%d rows, want ids to join", got)
	}
}

// snoozeThreadModel is conversationMail in a model whose snooze list is a
// temporary file.
func snoozeThreadModel(t *testing.T, store MailStore, messages ...maildir.Message) (Model, string) {
	t.Helper()
	path := snoozeEnv(t)
	return threadModel(t, store, messages...), path
}

func TestZOnACollapsedConversationSnoozesEveryMessage(t *testing.T) {
	store := &pathStore{}
	model, path := snoozeThreadModel(t, store, conversationMail()...)
	updated, _ := model.Update(key("Z"))
	model = updated.(Model)
	if model.snoozing == nil {
		t.Fatal("Z did not open the prompt")
	}
	if footer := ansi.Strip(model.renderFooter(100)); !strings.Contains(footer, "Snooze · Re: Budget (3 messages)") {
		t.Fatalf("prompt title: %q", footer)
	}
	model = typeInto(t, model, "fri")
	updated, command := model.Update(key("enter"))
	model = run(t, updated.(Model), command)
	if got := strings.Join(store.moves, ","); got != "3.x:2,S>@Snoozed,2.x:2,S>@Snoozed,1.x:2,S>@Snoozed" {
		t.Fatalf("moves = %s", got)
	}
	if model.status != "Snoozed 3 messages until Fri 08:00 · u undoes" {
		t.Fatalf("status = %q", model.status)
	}
	if got := strings.Join(rowSubjects(model), "|"); got != "Lunch/1" {
		t.Fatalf("rows after snoozing = %s", got)
	}
	entries, err := syncd.ReadSnooze(path)
	if err != nil || len(entries) != 3 {
		t.Fatalf("entries = %+v, %v", entries, err)
	}
	due := time.Date(2026, 10, 2, 8, 0, 0, 0, time.Local)
	for _, entry := range entries {
		if !entry.Due.Equal(due) || entry.FromBox != maildir.InboxBox || entry.Account != "a" {
			t.Fatalf("entry = %+v", entry)
		}
	}

	// One u brings all three back and drops all three entries.
	store.moves = nil
	updated, command = model.Update(key("u"))
	model = updated.(Model)
	result, ok := command().(messageRestoredMsg)
	if !ok || result.count != 3 || result.err != nil {
		t.Fatalf("undo result = %+v", result)
	}
	if got := strings.Join(store.moves, ","); got != "3.x:2,S>Inbox,2.x:2,S>Inbox,1.x:2,S>Inbox" {
		t.Fatalf("undo moves = %s", got)
	}
	if entries, _ := syncd.ReadSnooze(path); len(entries) != 0 {
		t.Fatalf("entries survived the undo: %+v", entries)
	}
}

func TestZOnAConversationSkipsMessagesWithoutAMessageID(t *testing.T) {
	store := &pathStore{}
	mail := conversationMail()
	mail[2].MessageID = ""
	model, path := snoozeThreadModel(t, store, mail...)
	model = press(t, model, "Z")
	model = typeInto(t, model, "fri")
	updated, command := model.Update(key("enter"))
	model = run(t, updated.(Model), command)
	if got := strings.Join(store.moves, ","); got != "3.x:2,S>@Snoozed,1.x:2,S>@Snoozed" {
		t.Fatalf("moves = %s", got)
	}
	if want := "Snoozed 2 messages until Fri 08:00 · 1 without a Message-ID stayed · u undoes"; model.status != want {
		t.Fatalf("status = %q, want %q", model.status, want)
	}
	if entries, _ := syncd.ReadSnooze(path); len(entries) != 2 {
		t.Fatalf("entries = %+v", entries)
	}

	// None with an id: refused, no prompt.
	none := conversationMail()
	for index := range none {
		none[index].MessageID = ""
	}
	model, _ = snoozeThreadModel(t, store, none...)
	if row, _ := model.cursorRow(); row.count() < 2 {
		t.Fatalf("setup: the references should still make a conversation, rows %v", rowSubjects(model))
	}
	model = press(t, model, "Z")
	if model.snoozing != nil || !strings.Contains(model.status, "these messages cannot be snoozed") {
		t.Fatalf("prompt %v status %q", model.snoozing, model.status)
	}
}

func TestZOnAnOpenedConversationOrTheReaderSnoozesOneMessage(t *testing.T) {
	store := &pathStore{}
	model, _ := snoozeThreadModel(t, store, conversationMail()...)
	model = press(t, model, " ") // open the conversation
	model = press(t, model, "j") // an older message
	model = press(t, model, "Z")
	if model.snoozing == nil || model.snoozing.group != nil {
		t.Fatalf("prompt = %+v, want a single message", model.snoozing)
	}
	if footer := ansi.Strip(model.renderFooter(100)); strings.Contains(footer, "messages)") {
		t.Fatalf("title counts messages: %q", footer)
	}
	model = typeInto(t, model, "fri")
	updated, command := model.Update(key("enter"))
	model = run(t, updated.(Model), command)
	if got := strings.Join(store.moves, ","); got != "2.x:2,S>@Snoozed" || model.status != "Snoozed until Fri 08:00 · u undoes" {
		t.Fatalf("moves %s status %q", got, model.status)
	}

	// The reader shows one message even on a collapsed row.
	store = &pathStore{}
	model, _ = snoozeThreadModel(t, store, conversationMail()...)
	model.screen, model.contentPath = screenMail, model.messages[0].Path
	model = press(t, model, "Z")
	if model.snoozing == nil || model.snoozing.group != nil {
		t.Fatalf("reader prompt = %+v, want a single message", model.snoozing)
	}
}

func TestFilingFooterSaysHowManyMessagesMove(t *testing.T) {
	model := threadModel(t, &pathStore{}, conversationMail()...)
	model = press(t, model, "f")
	if !model.filing {
		t.Fatal("f did not open the picker")
	}
	if footer := ansi.Strip(model.renderFooter(100)); !strings.Contains(footer, "Moving 3 messages") {
		t.Fatalf("footer = %q", footer)
	}
	if body := ansi.Strip(model.renderFilePicker(100, 20)); !strings.Contains(body, "(3 messages)") {
		t.Fatalf("picker = %q", body)
	}
	// A single message says nothing of the kind.
	model = press(t, model, "esc")
	model = press(t, model, "j") // Lunch
	model = press(t, model, "f")
	if footer := ansi.Strip(model.renderFooter(100)); strings.Contains(footer, "Moving") {
		t.Fatalf("single message footer = %q", footer)
	}
	if body := ansi.Strip(model.renderFilePicker(100, 20)); strings.Contains(body, "messages)") {
		t.Fatalf("single message picker = %q", body)
	}
}

// Two unread conversations; m on the first moves it below the second.
func twoUnreadConversations() []maildir.Message {
	return []maildir.Message{
		threadMail("4", "<d@x>", "<a@x>", "Re: Budget", "Ada", 1*time.Hour, true),
		threadMail("5", "<z@x>", "", "Lunch", "Zed", 2*time.Hour, true),
		threadMail("1", "<a@x>", "", "Budget", "Ada", 5*time.Hour, true),
	}
}

func TestMarkingAConversationReadKeepsTheCursorOnIt(t *testing.T) {
	model := threadModel(t, nil, twoUnreadConversations()...)
	if got := strings.Join(rowSubjects(model), "|"); got != "Re: Budget/2|Lunch/1" {
		t.Fatalf("rows = %s", got)
	}
	updated, command := model.Update(key("m"))
	model = run(t, updated.(Model), command)
	if got := strings.Join(rowSubjects(model), "|"); got != "Lunch/1|Re: Budget/2" {
		t.Fatalf("rows after m = %s, want the read conversation in Previously Seen", got)
	}
	if row, _ := model.cursorRow(); model.messages[row.index].Subject != "Re: Budget" {
		t.Fatalf("cursor on %q, want it to follow the conversation", model.messages[row.index].Subject)
	}
	// And back: marked unread it returns to New for You, cursor along.
	updated, command = model.Update(key("m"))
	model = run(t, updated.(Model), command)
	if row, _ := model.cursorRow(); model.messages[row.index].Subject != "Re: Budget" || model.mailCursor != 0 {
		t.Fatalf("after unread: cursor %d on %q", model.mailCursor, model.messages[row.index].Subject)
	}
}

func TestMarkingASingleMessageReadKeepsTheCursorOnIt(t *testing.T) {
	model := threadModel(t, nil,
		threadMail("1", "<a@x>", "", "First", "Ada", 1*time.Hour, true),
		threadMail("2", "<b@x>", "", "Second", "Bo", 2*time.Hour, true),
	)
	updated, command := model.Update(key("m"))
	model = run(t, updated.(Model), command)
	if row, _ := model.cursorRow(); model.messages[row.index].Subject != "First" {
		t.Fatalf("cursor on %q, want First (now in Previously Seen)", model.messages[row.index].Subject)
	}
}
