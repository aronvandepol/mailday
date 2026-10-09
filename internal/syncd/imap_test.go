package syncd

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

// TestAgainstIMAPServer runs the daemon against an in-memory IMAP server:
// IDLE on INBOX, the STATUS poller on Sent, and Archive following a move.
func TestAgainstIMAPServer(t *testing.T) {
	memory := imapmemserver.New()
	user := imapmemserver.NewUser("me", "pw")
	for _, name := range []string{"INBOX", "Archive", "Sent"} {
		_ = user.Create(name, nil)
	}
	memory.AddUser(user)
	server := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return memory.NewSession(), nil, nil
		},
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}, imap.CapMove: {}},
		InsecureAuth: true,
		Logger:       nopLogger{},
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	host, port, _ := net.SplitHostPort(listener.Addr().String())

	dir := t.TempDir()
	root := filepath.Join(dir, "Mail")
	logPath := filepath.Join(dir, "calls.log")
	bin := filepath.Join(dir, "bin")
	os.MkdirAll(bin, 0o700)
	os.WriteFile(filepath.Join(bin, "mbsync"), []byte("#!/bin/sh\necho \"$*\" >> "+logPath+"\n"), 0o700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MAILDAY_SYNCD_SOCKET", filepath.Join(dir, "s.sock"))
	for _, folder := range []string{"Inbox", "Archive", "Sent"} {
		for _, leaf := range []string{"cur", "new", "tmp"} {
			os.MkdirAll(filepath.Join(root, "acct", folder, leaf), 0o700)
		}
	}
	config := fmt.Sprintf(`IMAPAccount acct
Host %s
Port %s
User me
Pass pw
TLSType None

IMAPStore r
Account acct

MaildirStore l
Path %s/acct/
Inbox %s/acct/Inbox

Channel acct-inbox
Far :r:
Near :l:
Patterns INBOX

Channel acct-archive
Far :r:Archive
Near :l:Archive

Channel acct-sent
Far :r:Sent
Near :l:Sent
`, host, port, root, root)
	configPath := filepath.Join(dir, "rc")
	os.WriteFile(configPath, []byte(config), 0o600)

	daemon, err := New(Options{ConfigPath: configPath, Idle: []string{"INBOX"}, Poll: 300 * time.Millisecond, Settle: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go daemon.Run(ctx)

	waitFor(t, "INBOX watched", func() bool {
		status := daemon.snapshot()
		return len(status.Accounts) == 1 && len(status.Accounts[0].Watching) == 1
	})
	time.Sleep(time.Second) // the startup sweep and the poller's baseline
	os.Truncate(logPath, 0)

	client, err := imapclient.DialInsecure(listener.Addr().String(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Login("me", "pw").Wait(); err != nil {
		t.Fatal(err)
	}
	appendTo := func(mailbox string) {
		message := []byte("From: a@x\r\nSubject: hi\r\n\r\nbody\r\n")
		command := client.Append(mailbox, int64(len(message)), nil)
		command.Write(message)
		command.Close()
		if _, err := command.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	logged := func(want string) func() bool {
		return func() bool {
			data, _ := os.ReadFile(logPath)
			return bytes.Contains(data, []byte(want))
		}
	}

	appendTo("INBOX")
	waitFor(t, "IDLE to sync the inbox", logged("acct-inbox"))
	time.Sleep(300 * time.Millisecond)
	if logged("acct-archive")() {
		t.Error("new mail should not sync Archive (the poller covers it)")
	}

	os.Truncate(logPath, 0)
	appendTo("Sent")
	waitFor(t, "the poller to notice Sent", logged("acct-sent"))
	data, _ := os.ReadFile(logPath)
	if strings.Contains(string(data), "acct-inbox") {
		t.Errorf("a change in Sent synced the inbox too: %q", data)
	}

	// Archiving in Mailday: the local file moved to Archive, the server
	// moves its copy, the local one makes way for the download.
	message := []byte("From: a@x\r\nMessage-ID: <move-me@test>\r\nSubject: file me\r\n\r\nbody\r\n")
	command := client.Append("INBOX", int64(len(message)), nil)
	command.Write(message)
	command.Close()
	command.Wait()
	moved := filepath.Join(root, "acct", "Archive", "cur", "1.moved:2,S")
	os.WriteFile(moved, message, 0o600)
	os.Truncate(logPath, 0)
	// The local copy is removed only when no mbsync ran since the move:
	// let the syncs the new mail set off finish first.
	daemon.tune.moveMargin = 200 * time.Millisecond
	time.Sleep(time.Second)
	// Real mail is days old when archived, and a rename keeps its mtime.
	old := time.Now().Add(-72 * time.Hour)
	os.Chtimes(moved, old, old)
	if err := daemon.requestMove(filepath.Join(root, "acct", "Inbox", "cur", "1.moved:2,S"), moved); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the sync after the move", logged("acct-archive"))
	count := func(mailbox string) uint32 {
		data, err := client.Status(mailbox, &imap.StatusOptions{NumMessages: true}).Wait()
		if err != nil {
			t.Fatal(err)
		}
		return *data.NumMessages
	}
	waitFor(t, "the server move", func() bool { return count("Archive") == 1 })
	waitFor(t, "the download that replaces the local copy", func() bool {
		_, err := os.Stat(moved)
		return os.IsNotExist(err)
	})
	if count("Archive") != 1 || count("INBOX") != 1 {
		t.Fatalf("server: Archive %d INBOX %d, want 1 and 1 (one of the two in INBOX moved)", count("Archive"), count("INBOX"))
	}
	// The local copy was marked read (:2,S) and the server's was not: the
	// move must carry the flag, or the download would be unread again.
	if _, err := client.Select("Archive", nil).Wait(); err != nil {
		t.Fatal(err)
	}
	fetched, err := client.Fetch(imap.SeqSetNum(1), &imap.FetchOptions{Flags: true}).Collect()
	if err != nil || len(fetched) != 1 || !slices.Contains(fetched[0].Flags, imap.FlagSeen) {
		t.Fatalf("flags of the moved message = %+v (err %v), want \\Seen", fetched, err)
	}
	if _, err := os.Stat(moved); !os.IsNotExist(err) {
		t.Fatal("the local copy should make way for the download")
	}
	if data, _ := os.ReadFile(logPath); strings.Contains(string(data), "--push") {
		t.Fatalf("a server move needs no push: %q", data)
	}

	// Archived the moment it arrived: mbsync ran (for the new mail) after
	// the local move and may have paired the copy, so it must stay; a
	// deleted copy would read to mbsync as a deletion to pass on.
	race := []byte("From: a@x\r\nMessage-ID: <race@test>\r\nSubject: quick\r\n\r\nbody\r\n")
	appendRaw := client.Append("INBOX", int64(len(race)), nil)
	appendRaw.Write(race)
	appendRaw.Close()
	appendRaw.Wait()
	kept := filepath.Join(root, "acct", "Archive", "cur", "2.race:2,S")
	os.WriteFile(kept, race, 0o600)
	daemon.tune.moveMargin = 5 * time.Second
	waitFor(t, "the sync the new mail sets off", logged("acct-inbox"))
	// An Archive sync that ran after the local move (the poller, or a sweep).
	os.Truncate(logPath, 0)
	daemon.requestDestinations("acct")
	waitFor(t, "an Archive sync after the move", logged("acct-archive"))
	if err := daemon.requestMove(filepath.Join(root, "acct", "Inbox", "cur", "2.race:2,S"), kept); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the second server move", func() bool { return count("Archive") == 2 })
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(kept); err != nil {
		t.Fatalf("the copy mbsync may have paired was deleted: %v", err)
	}
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

type nopLogger struct{}

func (nopLogger) Printf(string, ...interface{}) {}
