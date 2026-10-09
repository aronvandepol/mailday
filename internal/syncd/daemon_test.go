package syncd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// startDaemon runs a daemon against a fake mbsync that logs its arguments.
func startDaemon(t *testing.T, script string) (log string, root string) {
	t.Helper()
	log, root, _, _ = startDaemonWith(t, script, nil)
	return log, root
}

// startDaemonWith is startDaemon for tests that shorten a timing: tweak
// runs on the daemon before it starts, and stop shuts it down and waits for
// Run to return.
func startDaemonWith(t *testing.T, script string, tweak func(*Daemon)) (log string, root string, daemon *Daemon, stop func()) {
	t.Helper()
	dir := t.TempDir()
	root = filepath.Join(dir, "Mail")
	log = filepath.Join(dir, "calls.log")
	bin := filepath.Join(dir, "bin")
	os.MkdirAll(bin, 0o700)
	fake := "#!/bin/sh\necho \"$*\" >> " + log + "\n" + script + "\n"
	if err := os.WriteFile(filepath.Join(bin, "mbsync"), []byte(fake), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MAILDAY_SYNCD_SOCKET", filepath.Join(dir, "s.sock"))
	config := `IMAPAccount acct
Host imap.example.org
User me
Pass x

IMAPStore acct-remote
Account acct

MaildirStore acct-local
Path ` + root + `/acct/
Inbox ` + root + `/acct/Inbox

Channel acct-inbox
Far :acct-remote:
Near :acct-local:
Patterns INBOX

Channel acct-sent
Far :acct-remote:Sent
Near :acct-local:Sent

Channel acct-tags
Far :acct-remote:
Near :acct-local:
Patterns "@*"
`
	configPath := filepath.Join(dir, "mbsyncrc")
	os.WriteFile(configPath, []byte(config), 0o600)
	for _, folder := range []string{"Inbox", "Sent", "@Reply"} {
		for _, leaf := range []string{"cur", "new", "tmp"} {
			os.MkdirAll(filepath.Join(root, "acct", folder, leaf), 0o700)
		}
	}
	daemon, err := New(Options{ConfigPath: configPath, Settle: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if tweak != nil {
		tweak(daemon)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stopped := make(chan struct{})
	go func() {
		daemon.Run(ctx)
		close(stopped)
	}()
	stop = func() {
		cancel()
		<-stopped
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := Send(context.Background(), Request{Op: "status"}); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return log, root, daemon, stop
}

func calls(t *testing.T, log string) []string {
	data, _ := os.ReadFile(log)
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func TestDaemonSyncsWhatChanged(t *testing.T) {
	log, root := startDaemon(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Startup runs every channel once.
	if _, err := Send(ctx, Request{Op: "sync", Wait: true}); err != nil {
		t.Fatal(err)
	}
	os.Truncate(log, 0)

	// A move from the inbox to a tag pushes both boxes in one run, given
	// as a message path and as a folder.
	_, err := Send(ctx, Request{Op: "sync", Push: true, Wait: true, Paths: []string{
		filepath.Join(root, "acct", "Inbox", "cur", "123:2,S"),
		filepath.Join(root, "acct", "@Reply"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := calls(t, log)
	if len(got) != 1 || got[0] != "-q --push acct-inbox acct-tags:@Reply" {
		t.Fatalf("calls = %q", got)
	}

	if _, err := Send(ctx, Request{Op: "sync", Paths: []string{"/elsewhere"}}); err == nil {
		t.Fatal("a folder no channel syncs should be an error")
	}

	response, err := Send(ctx, Request{Op: "status"})
	if err != nil || len(response.Status.Accounts) != 1 || response.Status.Accounts[0].LastSync.IsZero() {
		t.Fatalf("status = %+v %v", response.Status, err)
	}
}

func TestDaemonReportsNewInboxMailAndFailures(t *testing.T) {
	// The fake mbsync delivers one message on the first inbox sync, then fails.
	log, root := startDaemon(t, `
inbox="`+"$(dirname \"$0\")"+`/../Mail/acct/Inbox/new"
if [ ! -e "$(dirname "$0")/delivered" ]; then touch "$(dirname "$0")/delivered"; printf 'From: A <a@x>\nSubject: Hi\n\nbody\n' > "$inbox/1.msg"; exit 0; fi
if [ -e "$(dirname "$0")/fail" ]; then echo "IMAP error: auth failed" >&2; exit 1; fi
`)
	_ = log
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := Send(ctx, Request{Op: "sync", Wait: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "acct", "Inbox", "new", "1.msg")); err != nil {
		t.Fatal("fake delivery did not happen")
	}
	from, subject := summary(filepath.Join(root, "acct", "Inbox", "new", "1.msg"))
	if from != "A" || subject != "Hi" {
		t.Fatalf("summary = %q %q", from, subject)
	}

	subscription, err := Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	if _, err := subscription.Next(); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(filepath.Dir(log), "bin", "fail"), nil, 0o600)
	if _, err := Send(ctx, Request{Op: "sync", Paths: []string{filepath.Join(root, "acct", "Inbox")}, Wait: true}); err == nil || !strings.Contains(err.Error(), "auth failed") {
		t.Fatalf("err = %v", err)
	}
	for {
		status, err := subscription.Next()
		if err != nil {
			t.Fatal(err)
		}
		if account := status.Accounts[0]; !account.Syncing && account.LastError != "" {
			if !strings.Contains(account.LastError, "auth failed") {
				t.Fatalf("error = %q", account.LastError)
			}
			break
		}
	}
}

func TestMaildirFolder(t *testing.T) {
	for in, want := range map[string]string{
		"/m/a/Inbox/cur/1:2,S": "/m/a/Inbox",
		"/m/a/Inbox/new":       "/m/a/Inbox",
		"/m/a/@Reply":          "/m/a/@Reply",
		"/m/a/@Reply/":         "/m/a/@Reply",
	} {
		if got := maildirFolder(in); got != want {
			t.Errorf("maildirFolder(%q) = %q", in, got)
		}
	}
}

func TestFailedSyncIsRetried(t *testing.T) {
	// Fails once, then succeeds: the retry must happen without a new request.
	log, root := startDaemon(t, `
marker="$(dirname "$0")/failed-once"
case "$*" in *acct-sent*) if [ ! -e "$marker" ]; then touch "$marker"; echo "far side box cannot be opened" >&2; exit 1; fi;; esac
`)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := Send(ctx, Request{Op: "sync", Paths: []string{filepath.Join(root, "acct", "Sent")}, Wait: true}); err == nil {
		// The startup sweep may have taken the failure; either way a retry follows.
		t.Log("first sync succeeded; the sweep failed instead")
	}
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		response, err := Send(ctx, Request{Op: "status"})
		if err == nil && response.Status.Accounts[0].LastError == "" && strings.Count(strings.Join(calls(t, log), "\n"), "acct-sent") >= 2 {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("no successful retry; calls = %q", calls(t, log))
}

func TestDestinationsAreArchiveAndTrash(t *testing.T) {
	_, root := startDaemon(t, "")
	for _, folder := range []string{"Archive", "Trash"} {
		for _, leaf := range []string{"cur", "new", "tmp"} {
			os.MkdirAll(filepath.Join(root, "acct", folder, leaf), 0o700)
		}
	}
	config := `IMAPAccount acct
Host h
User u
Pass p

IMAPStore r
Account acct

MaildirStore l
Path ` + root + `/acct/
Inbox ` + root + `/acct/Inbox

Channel acct-inbox
Far :r:
Near :l:
Patterns INBOX

Channel acct-archive
Far :r:Archive
Near :l:Archive

Channel acct-trash
Far :r:"Deleted Items"
Near :l:Trash
`
	path := filepath.Join(t.TempDir(), "rc")
	os.WriteFile(path, []byte(config), 0o600)
	daemon, err := New(Options{ConfigPath: path})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(daemon.destinations["acct"], ","); got != "acct-archive,acct-trash" {
		t.Fatalf("destinations = %q", got)
	}
}

func TestCalendarCommandRunsAndReports(t *testing.T) {
	_, root := startDaemon(t, "")
	marker := filepath.Join(filepath.Dir(root), "calendar-ran")
	config := filepath.Join(filepath.Dir(root), "mbsyncrc")
	daemon, err := New(Options{ConfigPath: config, Calendar: "echo ran >> " + marker, Settle: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAILDAY_SYNCD_SOCKET", filepath.Join(filepath.Dir(root), "second.sock"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go daemon.Run(ctx)
	waitFor(t, "the calendar command", func() bool {
		status := daemon.snapshot()
		return status.Calendar.Enabled && !status.Calendar.LastSync.IsZero()
	})
	if data, _ := os.ReadFile(marker); string(data) != "ran\n" {
		t.Fatalf("marker = %q", data)
	}
}

func TestLeaseOneMachineAtATime(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("MAILDAY_LEASE_DIR", dir)
	now := time.Now()
	if _, err := TakeLease("tagger", 40*time.Minute, now); err != nil {
		t.Fatal(err)
	}
	// This machine renews freely.
	if _, err := TakeLease("tagger", 40*time.Minute, now.Add(15*time.Minute)); err != nil {
		t.Fatal(err)
	}
	// Another machine is turned away while it is fresh, and takes over after.
	os.WriteFile(filepath.Join(dir, "tagger.json"), []byte(`{"holder":"other-machine","until":"`+now.Add(30*time.Minute).Format(time.RFC3339)+`"}`), 0o600)
	if _, err := TakeLease("tagger", 40*time.Minute, now); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("err = %v", err)
	}
	if _, err := TakeLease("tagger", 40*time.Minute, now.Add(31*time.Minute)); err != nil {
		t.Fatalf("expired lease: %v", err)
	}
}

func TestReminderForExchangeWithJoin(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reminders are Linux only")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	ics := filepath.Join(home, ".local", "share", "mailday", "calendars")
	os.MkdirAll(ics, 0o700)
	start := time.Now().Add(5 * time.Minute).UTC()
	stamp := func(t time.Time) string { return t.Format("20060102T150405Z") }
	os.WriteFile(filepath.Join(ics, "University.ics"), []byte("BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:r1\r\nDTSTART:"+stamp(start)+"\r\nDTEND:"+stamp(start.Add(time.Hour))+
		"\r\nSUMMARY:Board meeting\r\nLOCATION:Atrium 2.04\r\nDESCRIPTION:Join: https://teams.microsoft.com/l/meetup-join/abc\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"), 0o600)
	bin := filepath.Join(home, "bin")
	os.MkdirAll(bin, 0o700)
	log := filepath.Join(home, "notified")
	os.WriteFile(filepath.Join(bin, "notify-send"), []byte("#!/bin/sh\necho \"$*\" >> "+log+"\n"), 0o700)
	os.WriteFile(filepath.Join(bin, "pidof"), []byte("#!/bin/sh\nexit 1\n"), 0o700)              // no hyprlock
	os.WriteFile(filepath.Join(bin, "loginctl"), []byte("#!/bin/sh\necho IdleHint=no\n"), 0o700) // someone is here
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	daemon := &Daemon{options: Options{Remind: 10 * time.Minute}, resumeCh: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go daemon.remindLoop(ctx)
	waitFor(t, "the reminder", func() bool {
		data, _ := os.ReadFile(log)
		return strings.Contains(string(data), "Board meeting")
	})
	data, _ := os.ReadFile(log)
	if !strings.Contains(string(data), "-A join=Join") || !strings.Contains(string(data), "Atrium 2.04") {
		t.Fatalf("notification = %q", data)
	}
	time.Sleep(100 * time.Millisecond)
	if strings.Count(string(data), "Board meeting") != 1 {
		t.Fatal("reminded twice")
	}
}

// Shutdown must let the mbsync in progress finish (SIGTERM, then Run returns)
// instead of killing it and walking away.
func TestShutdownWaitsForMbsyncToStopCleanly(t *testing.T) {
	log, _, daemon, stop := startDaemonWith(t, "", nil)
	waitFor(t, "the startup sync to finish", func() bool {
		status := daemon.snapshot()
		return !status.Accounts[0].Syncing && !status.Accounts[0].LastSync.IsZero()
	})
	marker := filepath.Join(filepath.Dir(log), "terminated")
	// Replace the fake mbsync: it traps SIGTERM, takes a moment, and says so.
	script := "#!/bin/sh\necho started >> " + log + "\ntrap 'sleep 1; echo terminated > " + marker + "; exit 0' TERM\nsleep 30 &\nwait\n"
	os.Truncate(log, 0)
	os.WriteFile(filepath.Join(filepath.Dir(log), "bin", "mbsync.new"), []byte(script), 0o700)
	os.Rename(filepath.Join(filepath.Dir(log), "bin", "mbsync.new"), filepath.Join(filepath.Dir(log), "bin", "mbsync"))
	if _, err := Send(context.Background(), Request{Op: "sync"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "mbsync to start", func() bool {
		data, _ := os.ReadFile(log)
		return strings.Contains(string(data), "started")
	})
	time.Sleep(200 * time.Millisecond) // let the shell reach its trap
	stop()
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("Run returned before mbsync had stopped")
	}
}
