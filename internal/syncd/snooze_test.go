package syncd

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

// No test may read the real shared list: the daemon loop reads it on start.
// HOME and the state directory are empty ones.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "snooze-main")
	if err != nil {
		panic(err)
	}
	os.Setenv("MAILDAY_SNOOZE_FILE", filepath.Join(dir, "snooze.json"))
	os.Setenv("HOME", filepath.Join(dir, "home"))
	os.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	os.Setenv("MAILDAY_CONFIG", filepath.Join(dir, "none.toml"))
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func snoozeEntry(account, id string, due time.Time) SnoozeEntry {
	return SnoozeEntry{MessageID: id, Account: account, Due: due, FromBox: "Inbox", Subject: "re " + id, Added: due.Add(-48 * time.Hour)}
}

func TestSnoozePathOverride(t *testing.T) {
	t.Setenv("MAILDAY_SNOOZE_FILE", "/tmp/elsewhere.json")
	if got := SnoozePath(); got != "/tmp/elsewhere.json" {
		t.Fatalf("SnoozePath() = %q", got)
	}
	t.Setenv("MAILDAY_SNOOZE_FILE", "")
	t.Setenv("HOME", "/home/someone")
	t.Setenv("XDG_DATA_HOME", "")
	if got := SnoozePath(); got != "/home/someone/.local/share/mailday/snooze.json" {
		t.Fatalf("default SnoozePath() = %q", got)
	}
}

func TestSnoozeFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared", "snooze.json")
	if entries, err := ReadSnooze(path); err != nil || len(entries) != 0 {
		t.Fatalf("missing file: %v, %v", entries, err)
	}
	base := time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC)
	late := snoozeEntry("gmail", "<b@x>", base.Add(time.Hour))
	early := snoozeEntry("gmail", "<a@x>", base)
	for _, entry := range []SnoozeEntry{late, early} {
		if err := AddSnooze(path, entry); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := ReadSnooze(path)
	if len(entries) != 2 || entries[0].MessageID != "<a@x>" || !entries[0].Due.Equal(base) || entries[0].Subject != "re <a@x>" {
		t.Fatalf("entries = %+v, want the early one first with every field kept", entries)
	}
	// Snoozing again replaces; case and angle brackets do not make a new key.
	again := snoozeEntry("gmail", "A@X", base.Add(24*time.Hour))
	if err := AddSnooze(path, again); err != nil {
		t.Fatal(err)
	}
	entries, _ = ReadSnooze(path)
	if len(entries) != 2 || !entries[1].Due.Equal(again.Due) && !entries[0].Due.Equal(again.Due) {
		t.Fatalf("entries = %+v, want a@x moved to the later time", entries)
	}
	// Another account's message with the same ID is a different entry.
	if err := AddSnooze(path, snoozeEntry("uni", "<a@x>", base)); err != nil {
		t.Fatal(err)
	}
	if entries, _ = ReadSnooze(path); len(entries) != 3 {
		t.Fatalf("entries = %+v, want 3 (keyed by account and ID)", entries)
	}
	if err := RemoveSnooze(path, "gmail", "<a@x>"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveSnooze(path, "gmail", "<never@x>"); err != nil {
		t.Fatalf("removing an unknown entry: %v", err)
	}
	entries, _ = ReadSnooze(path)
	var ids []string
	for _, entry := range entries {
		ids = append(ids, entry.Account+entry.MessageID)
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []string{"gmail<b@x>", "uni<a@x>"}) {
		t.Fatalf("after removal: %v", ids)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".snooze-*")); len(leftovers) != 0 {
		t.Fatalf("temporary files left behind: %v", leftovers)
	}
}

func TestSnoozeFileCorruptIsNotOverwritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snooze.json")
	os.WriteFile(path, []byte("{not json"), 0o600)
	if err := AddSnooze(path, snoozeEntry("gmail", "<a@x>", time.Now())); err == nil {
		t.Fatal("a corrupt list must be reported, not replaced")
	}
	if data, _ := os.ReadFile(path); string(data) != "{not json" {
		t.Fatalf("file was changed: %q", data)
	}
}

// Mailday and two daemons write the list at once: no entry may be lost.
func TestSnoozeConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snooze.json")
	base := time.Now().Add(time.Hour)
	const writers, each = 8, 10
	var wg sync.WaitGroup
	for writer := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range each {
				entry := snoozeEntry("gmail", fmt.Sprintf("<w%d-%d@x>", writer, index), base.Add(time.Duration(index)*time.Minute))
				if err := AddSnooze(path, entry); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	entries, err := ReadSnooze(path)
	if err != nil || len(entries) != writers*each {
		t.Fatalf("%d entries (%v), want %d", len(entries), err, writers*each)
	}
}

// A reader running while writers rename the file always sees a whole list.
func TestSnoozeReadDuringWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snooze.json")
	AddSnooze(path, snoozeEntry("gmail", "<first@x>", time.Now()))
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for index := 0; ; index++ {
			select {
			case <-stop:
				return
			default:
			}
			AddSnooze(path, snoozeEntry("gmail", fmt.Sprintf("<n%d@x>", index%20), time.Now()))
		}
	}()
	for range 200 {
		if _, err := ReadSnooze(path); err != nil {
			t.Fatalf("read saw a partial file: %v", err)
		}
	}
	close(stop)
	<-done
}

// snoozeServer is an in-memory IMAP server with INBOX and @Snoozed.
type snoozeServer struct {
	addr   string
	client *imapclient.Client
}

func newSnoozeServer(t *testing.T, withMove bool) *snoozeServer {
	t.Helper()
	memory := imapmemserver.New()
	user := imapmemserver.NewUser("me", "pw")
	for _, name := range []string{"INBOX", SnoozedBox} {
		_ = user.Create(name, nil)
	}
	memory.AddUser(user)
	// IMAP4rev2 includes MOVE, so the server without it speaks rev1 only.
	caps := imap.CapSet{imap.CapIMAP4rev1: {}}
	if withMove {
		caps = imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}, imap.CapMove: {}}
	}
	server := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return memory.NewSession(), nil, nil
		},
		Caps:         caps,
		InsecureAuth: true,
		Logger:       nopLogger{},
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	client, err := imapclient.DialInsecure(listener.Addr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	if err := client.Login("me", "pw").Wait(); err != nil {
		t.Fatal(err)
	}
	return &snoozeServer{addr: listener.Addr().String(), client: client}
}

func (s *snoozeServer) append(t *testing.T, mailbox, id string, flags ...imap.Flag) {
	t.Helper()
	message := []byte("From: a@x\r\nMessage-ID: " + id + "\r\nSubject: s\r\n\r\nbody\r\n")
	command := s.client.Append(mailbox, int64(len(message)), &imap.AppendOptions{Flags: flags})
	command.Write(message)
	command.Close()
	if _, err := command.Wait(); err != nil {
		t.Fatal(err)
	}
}

// messages lists the Message-IDs and flags in a mailbox.
func (s *snoozeServer) messages(t *testing.T, mailbox string) map[string][]imap.Flag {
	t.Helper()
	data, err := s.client.Select(mailbox, nil).Wait()
	if err != nil {
		t.Fatal(err)
	}
	found := map[string][]imap.Flag{}
	if data.NumMessages == 0 {
		return found
	}
	section := &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader, HeaderFields: []string{"Message-ID"}, Peek: true}
	var all imap.SeqSet
	all.AddRange(1, 0) // 1:*
	fetched, err := s.client.Fetch(all, &imap.FetchOptions{Flags: true, BodySection: []*imap.FetchItemBodySection{section}}).Collect()
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range fetched {
		found[headerMessageID(message.FindBodySection(section))] = message.Flags
	}
	return found
}

// snoozeDaemon is a daemon (never Run) for one account whose IMAP name,
// "main", differs from its Maildir label, "acct", as it does on a Mac.
func snoozeDaemon(t *testing.T, server *snoozeServer) *Daemon {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	os.MkdirAll(bin, 0o700)
	os.WriteFile(filepath.Join(bin, "mbsync"), []byte("#!/bin/sh\nexit 0\n"), 0o700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	host, port, _ := net.SplitHostPort(server.addr)
	root := filepath.Join(dir, "Mail", "me@acct.example")
	for _, folder := range []string{"Inbox", SnoozedBox} {
		for _, leaf := range []string{"cur", "new", "tmp"} {
			os.MkdirAll(filepath.Join(root, folder, leaf), 0o700)
		}
	}
	config := fmt.Sprintf(`IMAPAccount main
Host %s
Port %s
User me
Pass pw
TLSType None

IMAPStore r
Account main

MaildirStore l
Path %s/
Inbox %s/Inbox

Channel main-inbox
Far :r:
Near :l:
Patterns INBOX

Channel main-tags
Far :r:
Near :l:
Patterns @*
`, host, port, root, root)
	configPath := filepath.Join(dir, "rc")
	os.WriteFile(configPath, []byte(config), 0o600)
	daemon, err := New(Options{ConfigPath: configPath})
	if err != nil {
		t.Fatal(err)
	}
	return daemon
}

func requested(d *Daemon) []string {
	runner := d.runners["main"]
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return slices.Sorted(func(yield func(string) bool) {
		for spec := range runner.full {
			if !yield(spec) {
				return
			}
		}
	})
}

func TestUnsnoozeAgainstIMAPServer(t *testing.T) {
	server := newSnoozeServer(t, true)
	daemon := snoozeDaemon(t, server)
	path := filepath.Join(t.TempDir(), "snooze.json")
	t.Setenv("MAILDAY_SNOOZE_FILE", path)
	now := time.Date(2026, 10, 9, 8, 0, 1, 0, time.UTC)
	past, future := now.Add(-time.Minute), now.Add(time.Hour)

	// due: read, in @Snoozed, comes back unread.
	server.append(t, SnoozedBox, "<due@x>", imap.FlagSeen)
	// already moved: the other machine got it first, it sits in INBOX.
	server.append(t, "INBOX", "<moved@x>")
	// not yet due, and one that is due but Mailday filed a minute ago
	// (mbsync may not have uploaded it, so the entry must wait).
	server.append(t, SnoozedBox, "<later@x>", imap.FlagSeen)
	recent := snoozeEntry("acct", "<fresh@x>", past)
	recent.Added = now.Add(-time.Minute)
	// For another account of this machine's list: left alone.
	foreign := snoozeEntry("elsewhere", "<foreign@x>", past)
	for _, entry := range []SnoozeEntry{
		snoozeEntry("acct", "<due@x>", past),
		snoozeEntry("acct", "<moved@x>", past),
		snoozeEntry("acct", "<vanished@x>", past),
		snoozeEntry("acct", "<later@x>", future),
		recent, foreign,
	} {
		if err := AddSnooze(path, entry); err != nil {
			t.Fatal(err)
		}
	}

	daemon.unsnoozeDue(context.Background(), now)

	inbox := server.messages(t, "INBOX")
	if flags, ok := inbox["<due@x>"]; !ok || slices.Contains(flags, imap.FlagSeen) {
		t.Fatalf("INBOX = %v, want <due@x> there and unread", inbox)
	}
	if len(inbox) != 2 {
		t.Fatalf("INBOX = %v, want only <due@x> and the already moved <moved@x> (no duplicate)", inbox)
	}
	snoozed := server.messages(t, SnoozedBox)
	if len(snoozed) != 1 || snoozed["<later@x>"] == nil {
		t.Fatalf("@Snoozed = %v, want only <later@x>", snoozed)
	}
	left, _ := ReadSnooze(path)
	var ids []string
	for _, entry := range left {
		ids = append(ids, entry.MessageID)
	}
	slices.Sort(ids)
	if want := []string{"<foreign@x>", "<fresh@x>", "<later@x>"}; !slices.Equal(ids, want) {
		t.Fatalf("entries left = %v, want %v (due, already moved and vanished are dropped)", ids, want)
	}
	if got := requested(daemon); !slices.Equal(got, []string{"main-inbox", "main-tags:@Snoozed"}) {
		t.Fatalf("sync requested for %v, want the inbox and @Snoozed", got)
	}

	// The second machine runs the same pass a moment later: nothing to do.
	before := server.messages(t, "INBOX")
	daemon.unsnoozeDue(context.Background(), now.Add(time.Second))
	if after := server.messages(t, "INBOX"); len(after) != len(before) {
		t.Fatalf("a second pass changed INBOX: %v then %v", before, after)
	}
}

// Two machines return the same message at the same moment: one wins, the
// other finds it gone, and INBOX holds exactly one copy.
func TestUnsnoozeTwoDaemonsAtOnce(t *testing.T) {
	server := newSnoozeServer(t, true)
	first, second := snoozeDaemon(t, server), snoozeDaemon(t, server)
	path := filepath.Join(t.TempDir(), "snooze.json")
	t.Setenv("MAILDAY_SNOOZE_FILE", path)
	now := time.Date(2026, 10, 9, 8, 0, 1, 0, time.UTC)
	for index := range 5 {
		id := fmt.Sprintf("<race%d@x>", index)
		server.append(t, SnoozedBox, id, imap.FlagSeen)
		AddSnooze(path, snoozeEntry("acct", id, now.Add(-time.Minute)))
	}
	var wg sync.WaitGroup
	for _, daemon := range []*Daemon{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			daemon.unsnoozeDue(context.Background(), now)
		}()
	}
	wg.Wait()
	inbox := server.messages(t, "INBOX")
	if len(inbox) != 5 {
		t.Fatalf("INBOX = %v, want the five messages once each", inbox)
	}
	if left, _ := ReadSnooze(path); len(left) != 0 {
		t.Fatalf("entries left: %+v", left)
	}
	if rest := server.messages(t, SnoozedBox); len(rest) != 0 {
		t.Fatalf("@Snoozed still holds %v", rest)
	}
}

// Without MOVE the pipelined COPY, STORE and EXPUNGE fallback could expunge
// after a failed COPY, so nothing is attempted and the entry stays.
func TestUnsnoozeNeedsRealMove(t *testing.T) {
	server := newSnoozeServer(t, false)
	daemon := snoozeDaemon(t, server)
	path := filepath.Join(t.TempDir(), "snooze.json")
	t.Setenv("MAILDAY_SNOOZE_FILE", path)
	now := time.Date(2026, 10, 9, 8, 0, 1, 0, time.UTC)
	server.append(t, SnoozedBox, "<keep@x>", imap.FlagSeen)
	AddSnooze(path, snoozeEntry("acct", "<keep@x>", now.Add(-time.Minute)))
	daemon.unsnoozeDue(context.Background(), now)
	if left, _ := ReadSnooze(path); len(left) != 1 {
		t.Fatalf("entry dropped without a move: %+v", left)
	}
	if rest := server.messages(t, SnoozedBox); len(rest) != 1 {
		t.Fatalf("@Snoozed = %v, want the message untouched", rest)
	}
}

func TestUnsnoozeUnreachableServerKeepsEntries(t *testing.T) {
	server := newSnoozeServer(t, true)
	daemon := snoozeDaemon(t, server)
	path := filepath.Join(t.TempDir(), "snooze.json")
	t.Setenv("MAILDAY_SNOOZE_FILE", path)
	now := time.Date(2026, 10, 9, 8, 0, 1, 0, time.UTC)
	AddSnooze(path, snoozeEntry("acct", "<offline@x>", now.Add(-time.Minute)))
	daemon.config.Accounts["main"].Port = 1 // nothing listens there
	daemon.unsnoozeDue(context.Background(), now)
	daemon.unsnoozeDue(context.Background(), now) // the same failure must not log twice
	if left, _ := ReadSnooze(path); len(left) != 1 {
		t.Fatalf("entry lost while offline: %+v", left)
	}
	if got := len(daemon.snoozeLogged); got != 1 {
		t.Fatalf("snoozeLogged = %v, want one remembered problem", daemon.snoozeLogged)
	}
}

func TestSnoozeAccountMatching(t *testing.T) {
	daemon := snoozeDaemon(t, newSnoozeServer(t, true))
	for label, want := range map[string]string{"main": "main", "acct": "main"} {
		if got, ok := daemon.snoozeAccount(label); !ok || got != want {
			t.Errorf("snoozeAccount(%q) = %q, %v, want %q", label, got, ok, want)
		}
	}
	if _, ok := daemon.snoozeAccount("other"); ok {
		t.Error("an unknown account must not match")
	}
}

func TestUnsnoozeOnServerNoMatchWithinGrace(t *testing.T) {
	now := time.Now()
	entry := SnoozeEntry{MessageID: "<g@x>", Account: "acct", Due: now.Add(-time.Minute), Added: now.Add(-5 * time.Minute)}
	box := &fakeSnoozeMailbox{fakeMailbox: fakeMailbox{hits: [][]imap.UID{nil}}}
	result, err := unsnoozeOnServer(box, entry, now, nil)
	if err != nil || result != snoozePending {
		t.Fatalf("got %v, %v, want pending: the upload may not have finished", result, err)
	}
	entry.Added = now.Add(-snoozeUploadGrace - time.Minute)
	if result, err = unsnoozeOnServer(box, entry, now, nil); err != nil || result != snoozeGone {
		t.Fatalf("got %v, %v, want gone once the grace has passed", result, err)
	}
	if !strings.Contains(strings.Join(box.calls, ","), "search") {
		t.Fatal("expected a search")
	}
}

type fakeSnoozeMailbox struct{ fakeMailbox }

func (f *fakeSnoozeMailbox) removeFlags(imap.UID, []imap.Flag) error { return nil }

func TestUnsnoozeWaitsForAnUploadOnlyOnTheMachineThatSnoozed(t *testing.T) {
	now := time.Now()
	// Snoozed offline a day ago and due: the server's @Snoozed is empty.
	entry := SnoozeEntry{MessageID: "<s@x>", Account: "acct", FromBox: "Inbox", Due: now.Add(-time.Minute), Added: now.Add(-24 * time.Hour), Host: SnoozeHost()}
	empty := func() *fakeSnoozeMailbox {
		return &fakeSnoozeMailbox{fakeMailbox: fakeMailbox{hits: [][]imap.UID{nil}}}
	}
	// This machine snoozed it and its own @Snoozed still holds it: wait.
	if result, err := unsnoozeOnServer(empty(), entry, now, func(SnoozeEntry) bool { return true }); err != nil || result != snoozePending {
		t.Fatalf("got %v, %v, want pending until mbsync uploads it", result, err)
	}
	// Not here any more either: it was brought back (or moved): done.
	if result, _ := unsnoozeOnServer(empty(), entry, now, func(SnoozeEntry) bool { return false }); result != snoozeGone {
		t.Fatalf("got %v, want gone", result)
	}
	// Another machine snoozed it: leave the entry to that machine.
	other := entry
	other.Host = "some-other-machine"
	if result, _ := unsnoozeOnServer(empty(), other, now, func(SnoozeEntry) bool { return false }); result != snoozePending {
		t.Fatalf("got %v, want pending: not ours to drop", result)
	}
	// After a week the entry is given up wherever it was made.
	other.Added = now.Add(-snoozeStrandLimit - time.Hour)
	if result, _ := unsnoozeOnServer(empty(), other, now, func(SnoozeEntry) bool { return true }); result != snoozeGone {
		t.Fatalf("got %v, want gone after the limit", result)
	}
}

func TestSnoozeFileSurvivesSyncthingConflictsAndEmptyFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snooze.json")
	now := time.Now().Truncate(time.Second)
	mine := SnoozeEntry{MessageID: "<a@x>", Account: "acct", Due: now.Add(time.Hour), Added: now}
	theirs := SnoozeEntry{MessageID: "<b@x>", Account: "acct", Due: now.Add(2 * time.Hour), Added: now}
	if err := AddSnooze(path, mine); err != nil {
		t.Fatal(err)
	}
	// The other machine wrote at the same time: Syncthing keeps its copy aside.
	conflict, _ := json.Marshal(snoozeFile{Entries: []SnoozeEntry{theirs}})
	os.WriteFile(filepath.Join(dir, "snooze.sync-conflict-20261007-010203-ABCDEFG.json"), conflict, 0o600)
	entries, err := ReadSnooze(path)
	if err != nil || len(entries) != 2 {
		t.Fatalf("merged %d entries, err %v; want both", len(entries), err)
	}
	// The next write folds the conflict in and removes it.
	if err := AddSnooze(path, SnoozeEntry{MessageID: "<c@x>", Account: "acct", Due: now.Add(3 * time.Hour), Added: now}); err != nil {
		t.Fatal(err)
	}
	if left := snoozeConflicts(path); len(left) != 0 {
		t.Fatalf("conflict copies left: %v", left)
	}
	if entries, _ = ReadSnooze(path); len(entries) != 3 {
		t.Fatalf("after write: %d entries, want 3", len(entries))
	}
	// A sync that delivered an empty file does not wipe the list.
	os.WriteFile(path, nil, 0o600)
	if entries, _ = ReadSnooze(path); len(entries) != 3 {
		t.Fatalf("empty file read as %d entries, want the backup's 3", len(entries))
	}
}

// resetSnoozeNote forgets earlier tests' notes.
func resetSnoozeNote(t *testing.T) {
	t.Helper()
	snoozeRecovery.Lock()
	snoozeRecovery.pending, snoozeRecovery.seen = "", ""
	snoozeRecovery.Unlock()
}

// damagedSnoozeDir is a list with two entries whose main file was then cut
// off mid-write; the backup the last write left is intact.
func damagedSnoozeDir(t *testing.T) (path string, damaged []byte) {
	t.Helper()
	resetSnoozeNote(t)
	path = filepath.Join(t.TempDir(), "snooze.json")
	due := time.Now().Add(time.Hour).Truncate(time.Second)
	for _, id := range []string{"<a@x>", "<b@x>"} {
		if err := AddSnooze(path, snoozeEntry("gmail", id, due)); err != nil {
			t.Fatal(err)
		}
	}
	damaged = []byte(`{"entries": [{"messageID": "<a@x>", "acc`)
	if err := os.WriteFile(path, damaged, 0o600); err != nil {
		t.Fatal(err)
	}
	return path, damaged
}

func TestSnoozeDamagedFileIsReadFromTheBackupWithOneNote(t *testing.T) {
	path, damaged := damagedSnoozeDir(t)
	entries, err := ReadSnooze(path)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries %+v, err %v; want the backup's two", entries, err)
	}
	if got := TakeSnoozeNote(); got != "snooze list was damaged; restored from the backup" {
		t.Fatalf("note = %q", got)
	}
	// Every redraw reads again: the same damaged file is not announced twice.
	if _, err := ReadSnooze(path); err != nil {
		t.Fatal(err)
	}
	if got := TakeSnoozeNote(); got != "" {
		t.Fatalf("second note = %q, want none", got)
	}
	if data, _ := os.ReadFile(path); string(data) != string(damaged) {
		t.Fatal("reading must not change the file")
	}
}

func TestSnoozeUpdateKeepsTheDamagedFileBeforeReplacingIt(t *testing.T) {
	path, damaged := damagedSnoozeDir(t)
	due := time.Now().Add(2 * time.Hour)
	if err := AddSnooze(path, snoozeEntry("gmail", "<c@x>", due)); err != nil {
		t.Fatal(err)
	}
	kept, _ := filepath.Glob(path + ".corrupt-*")
	if len(kept) != 1 {
		t.Fatalf("kept copies = %v, want one", kept)
	}
	if data, _ := os.ReadFile(kept[0]); string(data) != string(damaged) {
		t.Fatalf("kept copy = %q, want the damaged bytes", data)
	}
	if suffix := strings.TrimPrefix(kept[0], path+".corrupt-"); strings.Trim(suffix, "0123456789") != "" {
		t.Fatalf("copy name %q does not end in a unix time", kept[0])
	}
	entries, err := ReadSnooze(path)
	if err != nil || len(entries) != 3 {
		t.Fatalf("after update: %+v, %v; want the backup's two and the new one", entries, err)
	}
	if got := TakeSnoozeNote(); got == "" {
		t.Fatal("the recovery went unannounced")
	}
	// Healthy again: no further copies, no further notes.
	if err := AddSnooze(path, snoozeEntry("gmail", "<d@x>", due)); err != nil {
		t.Fatal(err)
	}
	if again, _ := filepath.Glob(path + ".corrupt-*"); len(again) != 1 {
		t.Fatalf("copies now %v", again)
	}
}

func TestSnoozeDamagedFileWithoutAUsableBackupIsLeftAlone(t *testing.T) {
	for name, backup := range map[string]*string{"missing": nil, "damaged too": ptr("{also not json"), "blank": ptr("  \n")} {
		t.Run(name, func(t *testing.T) {
			resetSnoozeNote(t)
			path := filepath.Join(t.TempDir(), "snooze.json")
			os.WriteFile(path, []byte("{not json"), 0o600)
			if backup != nil {
				os.WriteFile(path+".bak", []byte(*backup), 0o600)
			}
			if _, err := ReadSnooze(path); err == nil {
				t.Fatal("no backup to restore from: the read must report the damage")
			}
			if err := AddSnooze(path, snoozeEntry("gmail", "<a@x>", time.Now())); err == nil {
				t.Fatal("a damaged list with no backup must not be replaced")
			}
			if data, _ := os.ReadFile(path); string(data) != "{not json" {
				t.Fatalf("file was changed: %q", data)
			}
			if kept, _ := filepath.Glob(path + ".corrupt-*"); len(kept) != 0 {
				t.Fatalf("copies = %v", kept)
			}
			if got := TakeSnoozeNote(); got != "" {
				t.Fatalf("note = %q, nothing was restored", got)
			}
		})
	}
}

func ptr(text string) *string { return &text }
