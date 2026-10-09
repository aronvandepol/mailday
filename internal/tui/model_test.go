package tui

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/aronvandepol/mailday/internal/calendar"
	"github.com/aronvandepol/mailday/internal/contacts"
	"github.com/aronvandepol/mailday/internal/maildir"
)

type fakeMailStore struct {
	result maildir.ListResult
	body   maildir.Content
	moved  []string
}

func (f *fakeMailStore) List(context.Context) (maildir.ListResult, error) { return f.result, nil }
func (f *fakeMailStore) Read(string) (maildir.Content, error)             { return f.body, nil }
func (f *fakeMailStore) SetUnread(message maildir.Message, unread bool) (maildir.Message, error) {
	message.Unread = unread
	return message, nil
}
func (f *fakeMailStore) Archive(maildir.Message) error            { return nil }
func (f *fakeMailStore) MarkAnswered(path string) (string, error) { return path + "R", nil }
func (f *fakeMailStore) SaveSent(string, []byte) error            { return nil }
func (f *fakeMailStore) Resolve(path string) string               { return path }
func (f *fakeMailStore) Attachments(string) ([]maildir.AttachmentData, error) {
	return nil, nil
}
func (f *fakeMailStore) SaveAttachments(string, string) ([]string, error) {
	return []string{"/tmp/x/a.pdf"}, nil
}
func (f *fakeMailStore) MoveTo(message maildir.Message, box string) (string, error) {
	f.moved = append(f.moved, box)
	return "/mail/a/" + box + "/cur/moved", nil
}

type fakeCalendarStore struct {
	result calendar.Result
}

func (f *fakeCalendarStore) Load(context.Context, time.Time, time.Time) (calendar.Result, error) {
	return f.result, nil
}

func TestModelRendersFullWidthMailAndCalendarSections(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Date(2026, 8, 25, 9, 0, 0, 0, time.Local)
	mailStore := &fakeMailStore{result: maildir.ListResult{Messages: []maildir.Message{{Path: "/mail/gmail/Inbox/cur/one", Account: "gmail", From: "Sender", Subject: "Planning", Date: now, Unread: true}}, Accounts: []string{"gmail"}}}
	calendarStore := &fakeCalendarStore{result: calendar.Result{Events: []calendar.Event{{Summary: "Office hours", Source: "Teaching", Provider: "Google", Start: now.Add(time.Hour), End: now.Add(2 * time.Hour)}}, Sources: []calendar.Source{{Name: "Teaching"}}}}
	model := NewModel(mailStore, calendarStore, Options{Days: 14})
	model.now = func() time.Time { return now }
	model.width = 120
	model.height = 30

	updated, _ := model.Update(mailLoadedMsg{result: mailStore.result})
	model = updated.(Model)
	updated, _ = model.Update(calendarLoadedMsg{result: calendarStore.result})
	model = updated.(Model)
	mailView := ansi.Strip(model.View().Content)
	for _, expected := range []string{"MAILDAY", "Planning", "New for You"} {
		if !strings.Contains(mailView, expected) {
			t.Fatalf("mail view does not contain %q:\n%s", expected, mailView)
		}
	}
	if strings.Contains(mailView, "Office hours") {
		t.Fatal("mail view includes calendar content")
	}

	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	model = updated.(Model)
	calendarView := ansi.Strip(model.View().Content)
	for _, expected := range []string{"Calendar", "Week", "Office hours"} {
		if !strings.Contains(calendarView, expected) {
			t.Fatalf("calendar view does not contain %q:\n%s", expected, calendarView)
		}
	}
}

func TestFilterLimitsMessages(t *testing.T) {
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
	model.messages = []maildir.Message{{Subject: "Alpha", From: "One"}, {Subject: "Beta", From: "Two"}}
	model.filtering = true
	updated, _ := model.Update(key("b"))
	model = updated.(Model)
	updated, _ = model.Update(key("e"))
	model = updated.(Model)
	if got, want := len(model.filteredMessageIndexes()), 1; got != want {
		t.Fatalf("filtered count = %d, want %d", got, want)
	}
	if got, want := model.messages[model.filteredMessageIndexes()[0]].Subject, "Beta"; got != want {
		t.Fatalf("match = %q, want %q", got, want)
	}
}

// m in the list keeps the cursor on the message it marked, wherever the
// message lands (opening a message is different: see
// TestOpeningAndLeavingAMessageLeavesTheCursorOnTheNextUnread).
func TestMarkingReadKeepsTheCursorOnTheMessage(t *testing.T) {
	store := &fakeMailStore{}
	model := NewModel(store, &fakeCalendarStore{}, Options{})
	model.messages = []maildir.Message{
		{Path: "/mail/a/Inbox/new/one", Subject: "One", Unread: true},
		{Path: "/mail/a/Inbox/new/two", Subject: "Two", Unread: true},
	}
	updated, command := model.Update(key("m"))
	model = updated.(Model)
	if command == nil {
		t.Fatal("toggle unread did not return a command")
	}
	updated, _ = model.Update(command())
	model = updated.(Model)
	selected, ok := model.selectedMail()
	if !ok || selected.Subject != "One" || selected.Unread {
		t.Fatalf("selected message = %+v, want the message just marked read, now in Previously Seen", selected)
	}

	// Marking it unread again jumps it back up to New for You, and the
	// cursor goes with it.
	model.mailCursor = 1 // the read message sits in Previously Seen
	if selected, _ := model.selectedMail(); selected.Path != "/mail/a/Inbox/new/one" {
		t.Fatalf("setup: selected %q", selected.Path)
	}
	updated, command = model.Update(key("m"))
	model = updated.(Model)
	updated, _ = model.Update(command())
	model = updated.(Model)
	if selected, _ := model.selectedMail(); selected.Path != "/mail/a/Inbox/new/one" || !selected.Unread {
		t.Fatalf("after marking unread, selected %+v", selected)
	}
}

func TestOpeningAndLeavingAMessageLeavesTheCursorOnTheNextUnread(t *testing.T) {
	first := maildir.Message{Path: "/m/a/Inbox/new/3.x", Account: "a", Subject: "First", Box: maildir.InboxBox, Date: time.Now(), Unread: true}
	second := maildir.Message{Path: "/m/a/Inbox/new/2.x", Account: "a", Subject: "Second", Box: maildir.InboxBox, Date: time.Now().Add(-time.Hour), Unread: true}
	model, store := readerModel(t, first, second)
	updated, _ := model.Update(key("enter"))
	model = updated.(Model)
	updated, command := model.Update(bodyLoadedMsg{path: first.Path, content: maildir.Content{}})
	model = updated.(Model)
	updated, _ = model.Update(command())
	model = updated.(Model)

	// mbsync-free reload caused by our own rename: the reader keeps its
	// message and the cursor keeps its place.
	read := first
	read.Path, read.Unread = "/m/a/Inbox/cur/3.x:2,S", false
	result := store.result
	result.Messages = []maildir.Message{read, second}
	updated, _ = model.Update(mailLoadedMsg{result: result, quiet: true})
	model = updated.(Model)
	if opened, ok := model.selectedMail(); !ok || opened.Subject != "First" {
		t.Fatalf("reader message = %+v", opened)
	}
	updated, _ = model.Update(key("esc"))
	model = updated.(Model)
	if selected, ok := model.selectedMail(); !ok || selected.Subject != "Second" {
		t.Fatalf("after esc the cursor is on %+v, want Second", selected)
	}
}

func TestAnExternalReloadStillFollowsTheCursorMessage(t *testing.T) {
	newest := maildir.Message{Path: "/m/a/Inbox/cur/3.x:2,S", Account: "a", Subject: "Newest", Box: maildir.InboxBox, Date: time.Now()}
	older := maildir.Message{Path: "/m/a/Inbox/cur/2.x:2,S", Account: "a", Subject: "Older", Box: maildir.InboxBox, Date: time.Now().Add(-time.Hour)}
	model, store := readerModel(t, newest, older)
	model.mailCursor = 1
	arrived := maildir.Message{Path: "/m/a/Inbox/new/4.x", Account: "a", Subject: "Arrived", Box: maildir.InboxBox, Date: time.Now(), Unread: true}
	result := store.result
	result.Messages = []maildir.Message{arrived, newest, older}
	updated, _ := model.Update(mailLoadedMsg{result: result, quiet: true})
	model = updated.(Model)
	if selected, _ := model.selectedMail(); selected.Subject != "Older" {
		t.Fatalf("a new arrival moved the selection to %q", selected.Subject)
	}
}

func TestArchiveNeedsTwoKeyPresses(t *testing.T) {
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
	model.messages = []maildir.Message{{Path: "/mail/a/Inbox/cur/one", Subject: "One"}}
	updated, command := model.Update(key("a"))
	model = updated.(Model)
	if command != nil || model.pendingArchive == "" {
		t.Fatal("first archive key did not arm confirmation")
	}
	_, command = model.Update(key("a"))
	if command == nil {
		t.Fatal("second archive key did not return a command")
	}
}

func key(value string) tea.KeyPressMsg {
	runes := []rune(value)
	return tea.KeyPressMsg(tea.Key{Code: runes[0], Text: value})
}

func TestBoxesSwitchAndFilingMovesTheMessage(t *testing.T) {
	store := &fakeMailStore{}
	model := NewModel(store, &fakeCalendarStore{}, Options{})
	model.boxes = []string{"Inbox", "@Reply", "@ESS"}
	model.messages = []maildir.Message{
		{Path: "/mail/a/Inbox/cur/one", Subject: "Inbox mail", Box: "Inbox"},
		{Path: "/mail/a/@Reply/cur/two", Subject: "Open request", Box: "@Reply"},
	}
	if got := len(model.filteredMessageIndexes()); got != 1 {
		t.Fatalf("Inbox shows %d messages, want 1", got)
	}
	updated, _ := model.Update(key("b"))
	model = updated.(Model)
	if got, want := model.activeBox(), "@Reply"; got != want {
		t.Fatalf("box after b = %q, want %q", got, want)
	}
	if indexes := model.filteredMessageIndexes(); len(indexes) != 1 || model.messages[indexes[0]].Subject != "Open request" {
		t.Fatalf("@Reply shows %v", indexes)
	}

	updated, _ = model.Update(key("f"))
	model = updated.(Model)
	if !model.filing || strings.Join(model.fileTargets, ",") != "Inbox,@ESS,Archive" {
		t.Fatalf("filing=%v targets=%v", model.filing, model.fileTargets)
	}
	if strings.Join(model.fileKeys, ",") != "i,a,x" {
		t.Fatalf("shortcuts = %v", model.fileKeys)
	}
	updated, command := model.Update(key("a")) // f a files straight to ESS
	model = updated.(Model)
	updated, _ = model.Update(command())
	model = updated.(Model)
	if strings.Join(store.moved, ",") != "@ESS" || model.filing {
		t.Fatalf("moved=%v filing=%v", store.moved, model.filing)
	}
	if got := model.messages[1].Box; got != "@ESS" {
		t.Fatalf("filed message box = %q, want @ESS", got)
	}
	if !strings.Contains(model.status, "Filed to ESS") {
		t.Fatalf("status = %q", model.status)
	}
}

func TestDeleteUndoAndHelp(t *testing.T) {
	store := &fakeMailStore{}
	model := NewModel(store, &fakeCalendarStore{}, Options{})
	model.boxes = []string{"Inbox", "@Reply"}
	model.messages = []maildir.Message{{Path: "/mail/a/@Reply/cur/two", Subject: "Open request", Box: "@Reply"}}
	model.mailBox = 1

	updated, command := model.Update(key("d"))
	model = updated.(Model)
	updated, _ = model.Update(command())
	model = updated.(Model)
	if len(model.messages) != 0 || store.moved[0] != "Trash" || !strings.Contains(model.status, "u undoes") {
		t.Fatalf("after d: messages=%d moved=%v status=%q", len(model.messages), store.moved, model.status)
	}
	updated, command = model.Update(key("u"))
	model = updated.(Model)
	command()
	if got := store.moved[len(store.moved)-1]; got != "@Reply" {
		t.Fatalf("undo moved to %q, want back to @Reply", got)
	}

	updated, _ = model.Update(key("?"))
	model = updated.(Model)
	if !model.showHelp || !strings.Contains(model.View().Content, "reply to all") {
		t.Fatal("? should show the key list")
	}
	updated, _ = model.Update(key("x"))
	if updated.(Model).showHelp {
		t.Fatal("any key should close the key list")
	}
}

func typeText(t *testing.T, model Model, text string) Model {
	t.Helper()
	for _, r := range text {
		updated, _ := model.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		model = updated.(Model)
	}
	return model
}

func TestRecipientFormAutocompletesFromTheAddressBook(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
	model.book = contacts.Book{Contacts: []contacts.Contact{
		{Name: "Stijn Dekker", Address: "s.c.dekker@hum.uni.example", Sent: 102},
		{Name: "Ruben Bakker", Address: "r.e.bakker@hum.uni.example", Sent: 372},
	}}
	updated, _ := model.Update(key("c"))
	model = updated.(Model)
	if model.form == nil || model.form.field != fieldTo {
		t.Fatal("c should open the recipient form on To")
	}
	model = typeText(t, model, "stij")
	if len(model.form.choices) != 1 || !strings.Contains(model.View().Content, "s.c.dekker@hum.uni.example") {
		t.Fatalf("choices = %+v", model.form.choices)
	}
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	model = updated.(Model)
	if got := model.form.values[fieldTo]; got != `"Stijn Dekker" <s.c.dekker@hum.uni.example>, ` {
		t.Fatalf("to after accepting = %q", got)
	}
	model = typeText(t, model, "stijn")
	if len(model.form.choices) != 0 {
		t.Fatalf("an address already chosen must not be offered again: %+v", model.form.choices)
	}
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyBackspace, Mod: tea.ModCtrl})
	model = updated.(Model)
	model.form.values[fieldTo] = `"Stijn Dekker" <s.c.dekker@hum.uni.example>, `
	for range 3 { // to, cc, bcc → subject
		updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		model = updated.(Model)
	}
	model = typeText(t, model, "ESS panel")
	for range 2 { // subject → attach → message
		updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		model = updated.(Model)
	}
	if model.form.field != fieldBody {
		t.Fatalf("field = %d, want the message body", model.form.field)
	}
	model = typeText(t, model, "Hi Stijn,")
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)
	model = typeText(t, model, "**draft** attached")
	updated, _ = model.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	model = updated.(Model)
	if model.form != nil || model.screen != screenCompose || model.draft == nil {
		t.Fatal("ctrl+s should close the composer and show the preview")
	}
	draft := *model.draft
	if draft.To != `"Stijn Dekker" <s.c.dekker@hum.uni.example>` || draft.Subject != "ESS panel" || !strings.Contains(draft.From, "s.de.vries@hum.uni.example") {
		t.Fatalf("draft = %+v", draft)
	}
	if draft.Body != "Hi Stijn,\n**draft** attached\n\n-- \nSam de Vries\nPhD Candidate · Example University\nDepartment of Example Studies\nExample University Centre for Digital Humanities\nsam.example.org\n" && draft.Body != "Hi Stijn,\n**draft** attached\n\n-- \nSam\n" {
		t.Fatalf("body = %q", draft.Body)
	}
	if saved, err := os.ReadFile(model.draftPath); err != nil || !strings.Contains(string(saved), "**draft** attached") {
		t.Fatalf("draft file: %v %q", err, saved)
	}
	updated, _ = model.Update(key("e")) // back to writing keeps everything
	model = updated.(Model)
	if model.form == nil || model.form.field != fieldBody || model.form.body.Value() != draft.Body {
		t.Fatal("e in the preview should reopen the composer on the body")
	}
}

func TestReplyOpensTheComposerInTheBody(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := &fakeMailStore{body: maildir.Content{FromName: "Ruben Bakker", FromAddr: "r.e.bakker@hum.uni.example",
		ToList: []maildir.Address{{Addr: "s.de.vries@hum.uni.example"}}, Subject: "Index", Body: "Can you check?", MessageID: "<i@x>"}}
	model := NewModel(store, &fakeCalendarStore{}, Options{})
	model.messages = []maildir.Message{{Path: "/mail/uni/Inbox/cur/one", Account: "uni", Subject: "Index"}}
	updated, command := model.Update(key("R"))
	model = updated.(Model)
	updated, _ = model.Update(command())
	model = updated.(Model)
	if model.form == nil || model.form.field != fieldBody || model.form.title != "Reply" {
		t.Fatalf("R should open the composer in the body, form=%+v", model.form)
	}
	if !strings.Contains(model.form.values[fieldTo], "r.e.bakker@hum.uni.example") || model.form.values[fieldSubject] != "Re: Index" {
		t.Fatalf("to %q subject %q", model.form.values[fieldTo], model.form.values[fieldSubject])
	}
	model = typeText(t, model, "Done.")
	if !strings.HasPrefix(model.form.body.Value(), "Done.") || !strings.Contains(model.form.body.Value(), "> Can you check?") {
		t.Fatalf("body = %q", model.form.body.Value())
	}
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	model = updated.(Model)
	if model.form != nil || !strings.Contains(model.status, "Draft saved") {
		t.Fatalf("esc should keep the draft: status %q", model.status)
	}
}

func TestAttachFieldCompletesPathsAndSSaves(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	_ = os.MkdirAll(filepath.Join(home, "Downloads"), 0o700)
	_ = os.WriteFile(filepath.Join(home, "Downloads", "handout.pdf"), []byte("%PDF"), 0o600)
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
	updated, _ := model.Update(key("c"))
	model = updated.(Model)
	for range 4 { // to → cc → bcc → subject → attach
		updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyTab})
		model = updated.(Model)
	}
	if model.form.field != fieldAttach {
		t.Fatalf("field = %d, want attach", model.form.field)
	}
	model = typeText(t, model, "~/Down")
	if len(model.form.paths) != 1 || model.form.paths[0] != "~/Downloads/" {
		t.Fatalf("paths = %v", model.form.paths)
	}
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	model = updated.(Model)
	model = typeText(t, model, "hand")
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	model = updated.(Model)
	if got := model.form.values[fieldAttach]; got != "~/Downloads/handout.pdf, " {
		t.Fatalf("attach = %q", got)
	}
	updated, _ = model.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	model = updated.(Model)
	if model.draft == nil || len(model.draft.Attach) != 1 || !strings.Contains(model.draft.File(), "attach: ~/Downloads/handout.pdf") {
		t.Fatalf("draft = %+v", model.draft)
	}
	var command tea.Cmd

	reader := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
	reader.messages = []maildir.Message{{Path: "/mail/a/Inbox/cur/one", Subject: "files"}}
	reader.screen = screenMail
	updated, command = reader.Update(key("S"))
	updated, _ = updated.(Model).Update(command())
	if status := updated.(Model).status; status != "Saved 1 file(s) to /tmp/x" {
		t.Fatalf("status = %q", status)
	}
}

func TestAttachWordsSearchFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	target := filepath.Join(home, "Documents", "KJG26", "abstract_conf.pdf")
	_ = os.MkdirAll(filepath.Dir(target), 0o700)
	_ = os.WriteFile(target, []byte("%PDF"), 0o600)
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
	updated, _ := model.Update(key("c"))
	model = updated.(Model)
	model.form.focus(fieldAttach)
	var command tea.Cmd
	for _, r := range "conf abstract" {
		updated, command = model.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		model = updated.(Model)
	}
	if command == nil {
		t.Fatal("typing words in attach should schedule a search")
	}
	tick := command().(fileSearchTickMsg)
	updated, command = model.Update(tick)
	model = updated.(Model)
	updated, _ = model.Update(command())
	model = updated.(Model)
	if len(model.form.hits) != 1 || model.form.paths[0] != "~/Documents/KJG26/abstract_conf.pdf" {
		t.Fatalf("hits = %+v paths = %v", model.form.hits, model.form.paths)
	}
	if view := model.View().Content; !strings.Contains(view, "abstract_conf.pdf") || !strings.Contains(view, "~/Documents/KJG26/") {
		t.Fatal("results should show the file name and its folder")
	}
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	model = updated.(Model)
	if got := model.form.values[fieldAttach]; got != "~/Documents/KJG26/abstract_conf.pdf, " {
		t.Fatalf("attach = %q", got)
	}
}

func TestPasteGoesIntoTheBodyHeadersAndSearch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
	updated, _ := model.Update(key("c"))
	model = updated.(Model)
	updated, _ = model.Update(tea.PasteMsg{Content: "ruben@x.nl\nstijn@y.nl\n"})
	model = updated.(Model)
	if got := model.form.values[fieldTo]; got != "ruben@x.nl, stijn@y.nl" {
		t.Fatalf("to = %q", got)
	}
	model.form.focus(fieldBody)
	updated, _ = model.Update(tea.PasteMsg{Content: "Dear all,\r\rSee **below**.\x1b[31m"})
	model = updated.(Model)
	if got := model.form.body.Value(); got != "Dear all,\n\nSee **below**.\n\n-- \nSam\n" {
		t.Fatalf("body = %q (line breaks kept, escape codes dropped)", got)
	}

	list := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
	list.filtering = true
	updated, _ = list.Update(tea.PasteMsg{Content: "Spring\nsymposium"})
	if got := updated.(Model).query; got != "Spring symposium" {
		t.Fatalf("query = %q", got)
	}
}

func TestSignatureFollowsFrom(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
	model.book = contacts.Book{Contacts: []contacts.Contact{{Name: "Ruben Bakker", Address: "r.e.bakker@hum.uni.example", Sent: 9}}}
	updated, _ := model.Update(key("c"))
	model = updated.(Model)
	model = typeText(t, model, "ruben")
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	model = updated.(Model)
	if body := model.form.body.Value(); !strings.Contains(body, "Department of Example Studies") || strings.Contains(body, "\n-- \nSam\n") {
		t.Fatalf("a University recipient should bring the University signature: %q", body)
	}
}

func TestDraftsBoxReopensAndSendsWithThreading(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := &fakeMailStore{body: maildir.Content{FromName: "Ruben Bakker", FromAddr: "r.e.bakker@hum.uni.example",
		ToList: []maildir.Address{{Addr: "s.de.vries@hum.uni.example"}}, Subject: "Index", Body: "Can you check?", MessageID: "<i@x>"}}
	model := NewModel(store, &fakeCalendarStore{}, Options{})
	model.accounts = []string{"uni"}
	model.loadingMail = false
	model.boxes = []string{"Inbox", "Sent"}
	model.messages = []maildir.Message{{Path: "/mail/uni/Inbox/cur/one", Account: "uni", Subject: "Index", Box: "Inbox"}}
	updated, command := model.Update(key("R"))
	model = updated.(Model)
	updated, _ = model.Update(command())
	model = updated.(Model)
	model = typeText(t, model, "Half done")
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	model = updated.(Model)
	if strings.Join(model.boxes, ",") != "Inbox,Drafts,Sent" {
		t.Fatalf("boxes = %v", model.boxes)
	}
	model.mailBox = 1
	indexes := model.filteredMessageIndexes()
	if len(indexes) != 1 || model.messages[indexes[0]].Subject != "Re: Index" || model.messages[indexes[0]].Account != "uni" {
		t.Fatalf("drafts box = %+v", model.messages)
	}
	if !strings.Contains(model.View().Content, "to Ruben Bakker") {
		t.Fatalf("a draft row should show who it is to: to=%q\n%s", model.messages[indexes[0]].To, ansi.Strip(model.View().Content))
	}
	updated, _ = model.Update(key("enter"))
	model = updated.(Model)
	if model.form == nil || model.form.title != "Reply (draft)" || !strings.HasPrefix(model.form.body.Value(), "Half done") {
		t.Fatalf("enter should reopen the draft in the composer: %+v", model.form)
	}
	draft := model.form.collect()
	if draft.InReplyTo != "<i@x>" || draft.AnswersPath != "/mail/uni/Inbox/cur/one" {
		t.Fatalf("threading lost: %+v", draft)
	}
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	model = updated.(Model)
	model.mailBox = 1
	updated, _ = model.Update(key("d"))
	model = updated.(Model)
	if slices.Contains(model.boxes, "Drafts") || !strings.Contains(model.status, "u undoes") {
		t.Fatalf("after d: boxes %v status %q", model.boxes, model.status)
	}
	updated, _ = model.Update(key("u"))
	model = updated.(Model)
	if !slices.Contains(model.boxes, "Drafts") {
		t.Fatal("u should restore the draft")
	}
}

func TestGoKeysOpenBoxes(t *testing.T) {
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
	model.boxes = []string{"Inbox", "@Reply", "@Admin", "Drafts", "Sent", "Archive"}
	for _, step := range []struct{ key, box string }{{"D", "Drafts"}, {"S", "Sent"}, {"r", "@Reply"}, {"d", "@Admin"}, {"x", "Archive"}, {"i", "Inbox"}} {
		updated, _ := model.Update(key("g"))
		updated, _ = updated.(Model).Update(key(step.key))
		model = updated.(Model)
		if got := model.activeBox(); got != step.box {
			t.Fatalf("g %s opened %q, want %q", step.key, got, step.box)
		}
	}
}
