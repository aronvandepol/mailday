package tui

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aronvandepol/mailday/internal/calendar"
	"github.com/aronvandepol/mailday/internal/maildir"
	"github.com/aronvandepol/mailday/internal/syncd"
)

func makeMaildir(t *testing.T, folder string) {
	t.Helper()
	for _, leaf := range []string{"cur", "new", "tmp"} {
		if err := os.MkdirAll(filepath.Join(folder, leaf), 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

func waitChange(t *testing.T, l *live, what string) {
	t.Helper()
	select {
	case <-l.changes:
	case <-time.After(3 * time.Second):
		t.Fatalf("no change reported for %s", what)
	}
}

func TestMailWatchReportsNewMailAndNewFolders(t *testing.T) {
	root := t.TempDir()
	inbox := filepath.Join(root, "acct", "Inbox")
	makeMaildir(t, inbox)
	l := newLive()
	l.startMailWatch([]string{root})
	time.Sleep(50 * time.Millisecond)

	// A burst of writes is one change.
	for _, name := range []string{"a", "b", "c"} {
		os.WriteFile(filepath.Join(inbox, "new", name), []byte("x"), 0o600)
	}
	waitChange(t, l, "new mail")
	select {
	case <-l.changes:
		t.Fatal("a burst should settle into one change")
	case <-time.After(600 * time.Millisecond):
	}

	// State files mbsync writes next to cur/new are not mail.
	os.WriteFile(filepath.Join(inbox, ".mbsyncstate"), []byte("x"), 0o600)
	select {
	case <-l.changes:
		t.Fatal(".mbsyncstate is not a mail change")
	case <-time.After(600 * time.Millisecond):
	}

	// A folder mbsync creates later is watched too.
	tag := filepath.Join(root, "acct", "@New")
	makeMaildir(t, tag)
	time.Sleep(200 * time.Millisecond)
	select {
	case <-l.changes: // creating it may count as a change
	case <-time.After(500 * time.Millisecond):
	}
	os.WriteFile(filepath.Join(tag, "cur", "m:2,S"), []byte("x"), 0o600)
	waitChange(t, l, "mail in a new folder")
}

func TestQuietReloadKeepsStatusAndSelection(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	first := maildir.Message{Path: "/m/a/Inbox/cur/1", Account: "a", Subject: "One", Box: maildir.InboxBox, Date: time.Now()}
	second := maildir.Message{Path: "/m/a/Inbox/cur/2", Account: "a", Subject: "Two", Box: maildir.InboxBox, Date: time.Now().Add(-time.Hour)}
	store := &fakeMailStore{result: maildir.ListResult{Messages: []maildir.Message{first, second}, Accounts: []string{"a"}, Boxes: []string{maildir.InboxBox}}}
	model := NewModel(store, &fakeCalendarStore{}, Options{Live: true})
	updated, _ := model.Update(mailLoadedMsg{result: store.result})
	model = updated.(Model)
	model.loadingCal = false
	model.mailCursor = 1
	model.status = "Filed to Reply"

	updated, command := model.Update(mailChangedMsg{})
	model = updated.(Model)
	if !model.reloading || command == nil {
		t.Fatal("a change on disk should start a quiet reload")
	}
	// A second change while reloading is remembered, not run in parallel.
	updated, _ = model.Update(mailChangedMsg{})
	model = updated.(Model)
	if !model.reloadAgain {
		t.Fatal("change during a reload should queue another")
	}

	arrived := maildir.Message{Path: "/m/a/Inbox/new/3", Account: "a", Subject: "New", Box: maildir.InboxBox, Date: time.Now().Add(time.Hour), Unread: true}
	result := store.result
	result.Messages = []maildir.Message{arrived, first, second}
	updated, command = model.Update(mailLoadedMsg{result: result, quiet: true})
	model = updated.(Model)
	if model.status != "Filed to Reply" {
		t.Fatalf("status = %q, a quiet reload must keep it", model.status)
	}
	if selected, _ := model.selectedMail(); selected.Path != second.Path {
		t.Fatalf("selection moved to %q", selected.Path)
	}
	if !model.reloading || model.reloadAgain || command == nil {
		t.Fatal("the queued reload should run now")
	}
}

func TestSyncLabelAndErrorsAnnouncedOnce(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.Local)
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{Live: true})
	model.now = func() time.Time { return now }
	model.syncDown = true
	if label := model.syncLabel(); !strings.Contains(label, "off") {
		t.Fatalf("down label = %q", label)
	}
	ok := syncd.Status{Accounts: []syncd.AccountStatus{
		{Name: "gmail", LastSync: now.Add(-2 * time.Minute), Watching: []string{"INBOX"}},
		{Name: "uni", LastSync: now.Add(-5 * time.Second), Watching: []string{"INBOX"}},
	}}
	updated, _ := model.Update(syncStatusMsg{status: ok})
	model = updated.(Model)
	if label := model.syncLabel(); label != "live" {
		t.Fatalf("label = %q", label)
	}
	degraded := syncd.Status{Accounts: slices.Clone(ok.Accounts)}
	degraded.Accounts[0].Offline = []string{"@Reply"}
	updated, _ = model.Update(syncStatusMsg{status: degraded})
	model = updated.(Model)
	if label := model.syncLabel(); label != "synced 2m ago" {
		t.Fatalf("label = %q", label)
	}
	failing := ok
	failing.Accounts = slices.Clone(ok.Accounts)
	failing.Accounts[1].LastError = "mbsync: auth failed"
	updated, _ = model.Update(syncStatusMsg{status: failing})
	model = updated.(Model)
	if !strings.Contains(model.status, "uni sync failed: mbsync: auth failed") {
		t.Fatalf("status = %q", model.status)
	}
	model.status = ""
	updated, _ = model.Update(syncStatusMsg{status: failing})
	model = updated.(Model)
	if model.status != "" {
		t.Fatal("the same error should not be announced twice")
	}
	if label := model.syncLabel(); !strings.Contains(label, "uni sync failed") {
		t.Fatalf("label = %q", label)
	}
}

func TestPushingStoreNamesTheFoldersAChangeTouched(t *testing.T) {
	var pushed [][]string
	store := pushingStore{MailStore: &fakeMailStore{}, push: func(paths ...string) { pushed = append(pushed, paths) }}
	message := maildir.Message{Path: "/mail/a/Inbox/cur/one"}
	if _, err := store.MoveTo(message, "@Reply"); err != nil {
		t.Fatal(err)
	}
	if err := store.Archive(message); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkAnswered("/mail/a/@Reply/cur/two"); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"/mail/a/Inbox", "/mail/a/@Reply"},
		{"/mail/a/Inbox", "/mail/a/Archive"},
		{"/mail/a/@Reply"},
	}
	if len(pushed) != len(want) {
		t.Fatalf("pushed %v", pushed)
	}
	for index := range want {
		if !slices.Equal(pushed[index], want[index]) {
			t.Errorf("push %d = %v, want %v", index, pushed[index], want[index])
		}
	}
}

func readerModel(t *testing.T, messages ...maildir.Message) (Model, *fakeMailStore) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	store := &fakeMailStore{result: maildir.ListResult{Messages: messages, Accounts: []string{"a"}, Boxes: []string{maildir.InboxBox}}}
	model := NewModel(store, &fakeCalendarStore{}, Options{Live: true})
	updated, _ := model.Update(mailLoadedMsg{result: store.result})
	model = updated.(Model)
	model.loadingCal = false
	return model, store
}

func TestOpeningMarksReadQuietly(t *testing.T) {
	unread := maildir.Message{Path: "/m/a/Inbox/new/1700.x", Account: "a", Subject: "Hello", Box: maildir.InboxBox, Date: time.Now(), Unread: true}
	model, _ := readerModel(t, unread)
	updated, command := model.Update(key("enter"))
	model = updated.(Model)
	updated, command = model.Update(bodyLoadedMsg{path: unread.Path, content: maildir.Content{}})
	model = updated.(Model)
	if command == nil {
		t.Fatal("opening an unread message should mark it read")
	}
	changed := command().(messageChangedMsg)
	if !changed.auto || changed.message.Unread {
		t.Fatalf("change = %+v", changed)
	}
	model.status = ""
	updated, _ = model.Update(changed)
	model = updated.(Model)
	if model.status != "" || model.messages[0].Unread {
		t.Fatalf("status %q, unread %v", model.status, model.messages[0].Unread)
	}
	// Already read: nothing to do.
	if _, command = model.Update(bodyLoadedMsg{path: unread.Path}); command != nil {
		t.Fatal("a read message should not be marked again")
	}
}

func TestReaderFollowsRenamesAndRefusesWhenTheMessageIsGone(t *testing.T) {
	newest := maildir.Message{Path: "/m/a/Inbox/cur/1702.x:2,S", Account: "a", Subject: "Newest", Box: maildir.InboxBox, Date: time.Now()}
	open := maildir.Message{Path: "/m/a/Inbox/cur/1701.x:2,S", Account: "a", Subject: "Open", Box: maildir.InboxBox, Date: time.Now().Add(-time.Hour)}
	older := maildir.Message{Path: "/m/a/Inbox/cur/1700.x:2,S", Account: "a", Subject: "Older", Box: maildir.InboxBox, Date: time.Now().Add(-2 * time.Hour)}
	model, store := readerModel(t, newest, open, older)
	model.mailCursor = 1
	updated, _ := model.Update(key("enter"))
	model = updated.(Model)
	if model.contentPath != open.Path {
		t.Fatalf("opened %q", model.contentPath)
	}

	// mbsync renames the open message (a UID and a flag from elsewhere).
	renamed := open
	renamed.Path = "/m/a/Inbox/cur/1701.x,U=42:2,FS"
	result := store.result
	result.Messages = []maildir.Message{newest, renamed, older}
	updated, _ = model.Update(mailLoadedMsg{result: result, quiet: true})
	model = updated.(Model)
	if model.contentPath != renamed.Path || model.readerGone {
		t.Fatalf("reader lost the renamed message: %q gone=%v", model.contentPath, model.readerGone)
	}
	if selected, _ := model.selectedMail(); selected.Path != renamed.Path {
		t.Fatalf("selection = %q", selected.Path)
	}

	// Archived on the phone: the neighbour must not be deleted by d.
	result.Messages = []maildir.Message{newest, older}
	updated, _ = model.Update(mailLoadedMsg{result: result, quiet: true})
	model = updated.(Model)
	if !model.readerGone || !strings.Contains(model.status, "elsewhere") {
		t.Fatalf("gone=%v status=%q", model.readerGone, model.status)
	}
	updated, command := model.Update(key("d"))
	model = updated.(Model)
	if command != nil || len(store.moved) != 0 {
		t.Fatalf("d acted on another message: moved %v", store.moved)
	}
	updated, _ = model.Update(key("esc"))
	model = updated.(Model)
	if model.readerGone {
		t.Fatal("leaving the reader should clear the flag")
	}
}

func TestOfflineLabel(t *testing.T) {
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{Live: true})
	down := syncd.Status{Accounts: []syncd.AccountStatus{
		{Name: "gmail", LastError: "mbsync: Error: Cannot resolve server 'imap.gmail.com'"},
		{Name: "uni", LastError: "mbsync: Error: Connecting to outlook.office365.com: Network is unreachable"},
	}}
	updated, _ := model.Update(syncStatusMsg{status: down})
	model = updated.(Model)
	if label := model.syncLabel(); !strings.HasPrefix(label, "offline") {
		t.Fatalf("label = %q", label)
	}
	if strings.Contains(model.status, "failed") {
		t.Fatalf("a lost network is not a failure to announce: %q", model.status)
	}
}

func TestCalendarWatchSettles(t *testing.T) {
	dir := t.TempDir()
	l := newLive()
	l.startCalendarWatch([]string{dir})
	time.Sleep(50 * time.Millisecond)
	os.WriteFile(filepath.Join(dir, "Work.ics"), []byte("BEGIN:VCALENDAR"), 0o600)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600)
	select {
	case <-l.calendarChanges:
	case <-time.After(4 * time.Second):
		t.Fatal("no calendar change")
	}
}

func TestMeetingLink(t *testing.T) {
	cases := []struct {
		location, notes, want string
	}{
		{"Atrium 2.04", "Organizer: Roos", ""},
		{"Microsoft Teams Meeting", "Join: https://teams.microsoft.com/l/meetup-join/19%3a abc", "https://teams.microsoft.com/l/meetup-join/19%3a"},
		{"", "Agenda at https://docs.example.org/x and Join: https://meet.google.com/abc-defg-hij.", "https://meet.google.com/abc-defg-hij."},
		{"https://zoom.us/j/123?pwd=x", "", "https://zoom.us/j/123?pwd=x"},
		{"https://example.org/room", "", "https://example.org/room"},
	}
	for _, c := range cases {
		got := meetingLink(calendar.Event{Location: c.location, Description: c.notes})
		if got != c.want {
			t.Errorf("meetingLink(%q, %q) = %q, want %q", c.location, c.notes, got, c.want)
		}
	}
}

func TestArrivalNoticeAndTitle(t *testing.T) {
	old := maildir.Message{Path: "/m/a/Inbox/cur/1.x:2,", Account: "a", From: "Old", Subject: "Earlier", Box: maildir.InboxBox, Date: time.Now().Add(-time.Hour), Unread: true}
	model, store := readerModel(t, old)
	if title := model.windowTitle(); title != "Mailday (1)" {
		t.Fatalf("title = %q", title)
	}
	renamed := old
	renamed.Path = "/m/a/Inbox/cur/1.x,U=7:2,"
	fresh := maildir.Message{Path: "/m/a/Inbox/new/2.x", Account: "a", From: "Ada", Subject: "Draft chapter", Box: maildir.InboxBox, Date: time.Now(), Unread: true}
	result := store.result
	result.Messages = []maildir.Message{fresh, renamed}
	model.status = ""
	updated, _ := model.Update(mailLoadedMsg{result: result, quiet: true})
	model = updated.(Model)
	if model.status != "New: Ada — Draft chapter" {
		t.Fatalf("status = %q (a renamed message is not new)", model.status)
	}
	if title := model.windowTitle(); title != "Mailday (2)" {
		t.Fatalf("title = %q", title)
	}
	// A status the user is reading is not overwritten.
	model.status = "Filed to Reply"
	newer := fresh
	newer.Path = "/m/a/Inbox/new/3.x"
	result.Messages = []maildir.Message{newer, fresh, renamed}
	updated, _ = model.Update(mailLoadedMsg{result: result, quiet: true})
	if updated.(Model).status != "Filed to Reply" {
		t.Fatal("arrival notice replaced another status")
	}
}

func TestNetworkHiccupsAreNotAnnouncedAndARecoveredFailureClears(t *testing.T) {
	failing := syncd.Status{Accounts: []syncd.AccountStatus{{Name: "gmail", LastError: "mbsync: Socket error on imap.gmail.com (142.250.102.108:993): timeout."}}}
	if got := newSyncErrors(syncd.Status{}, failing); got != "" {
		t.Fatalf("a timeout was announced: %q", got)
	}
	real := syncd.Status{Accounts: []syncd.AccountStatus{{Name: "gmail", LastError: "mbsync: IMAP error: auth failed"}}}
	status := newSyncErrors(syncd.Status{}, real)
	if status == "" {
		t.Fatal("a real failure must be announced")
	}
	if recoveredSyncError(status, real) {
		t.Fatal("still failing: keep it")
	}
	if !recoveredSyncError(status, syncd.Status{Accounts: []syncd.AccountStatus{{Name: "gmail"}}}) {
		t.Fatal("synced since: the message should clear")
	}
}
