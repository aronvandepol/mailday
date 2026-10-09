package main

import (
	"bytes"
	"context"
	"github.com/aronvandepol/mailday/internal/compose"
	"github.com/aronvandepol/mailday/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aronvandepol/mailday/internal/calendar"
	"github.com/aronvandepol/mailday/internal/maildir"
)

// checkEnvironment isolates a --check run: a temp HOME, a PATH holding only
// the fake executables asked for, no real daemon socket and no editor or
// zone settings from the machine running the tests.
func checkEnvironment(t *testing.T, tools ...string) (home string) {
	t.Helper()
	home = t.TempDir()
	bin := t.TempDir()
	for _, name := range tools {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", bin)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("MAILDAY_SYNCD_SOCKET", filepath.Join(home, "no-such-daemon.sock"))
	t.Setenv("MAILDAY_EXCHANGE_TOKEN", "")
	t.Setenv("MAILDAY_HOME_TZ", "")
	t.Setenv("MAILDAY_EDITOR", "")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	t.Setenv("TZ", "Europe/Amsterdam")
	return home
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const testMbsyncrc = `IMAPAccount gmail
Host imap.example.com
User me

IMAPStore gmail-remote
Account gmail

MaildirStore gmail-local
Path ~/Mail/gmail/
Inbox ~/Mail/gmail/Inbox

Channel gmail-inbox
Far :gmail-remote:INBOX
Near :gmail-local:Inbox

IMAPAccount uni
Host imap.uni.example.com
User me

IMAPStore uni-remote
Account uni

MaildirStore uni-local
Path ~/Mail/uni/
Inbox ~/Mail/uni/Inbox

Channel uni-inbox
Far :uni-remote:INBOX
Near :uni-local:Inbox
`

func listMail(t *testing.T, root string) (maildir.ListResult, error) {
	t.Helper()
	return maildir.New([]string{root}, 10).List(context.Background())
}

func linesOf(lines []checkLine) string {
	var text []string
	for _, line := range lines {
		text = append(text, line.String())
	}
	return strings.Join(text, "\n")
}

func TestChecklistAllPresent(t *testing.T) {
	home := checkEnvironment(t, "mbsync", "neomutt", "sqlite3", "msmtp", "notmuch", "mailday-calendar", "mailday-calsync", "claude", "nvim")
	root := filepath.Join(home, "Mail")
	for _, account := range []string{"gmail", "uni"} {
		if err := os.MkdirAll(filepath.Join(root, account, "Inbox", "cur"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(home, ".mbsyncrc"), testMbsyncrc)
	writeFile(t, filepath.Join(home, ".config", "mailday", "ews-token.gpg"), "x")
	writeFile(t, filepath.Join(home, "gcal.py"), "")
	useConfig(t, home, config.Config{
		Identity: []config.Identity{{Accounts: []string{"gmail"}, Address: "sam@gmail.example"}},
		Exchange: config.Exchange{User: "s@uni.example"},
		Calendar: config.Calendar{GoogleScript: filepath.Join(home, "gcal.py")},
		Judge:    config.Judge{Enabled: true},
	})
	mail, mailErr := listMail(t, root)

	lines := checklist(checkInput{Home: home, MailRoots: []string{root}, Mail: mail, MailErr: mailErr, CalendarSources: 2, SyncdRunning: true})
	for _, line := range lines {
		if !line.OK {
			t.Errorf("unexpected failure: %s", line)
		}
	}
	text := linesOf(lines)
	for _, want := range []string{
		"✓ mail: 2 accounts (gmail, uni)",
		"✓ mbsync account gmail has a Maildir (gmail)",
		"✓ mbsync account uni has a Maildir (uni)",
		"✓ msmtp (sending mail)",
		"✓ notmuch (search with /)",
		"✓ mailday-syncd running",
		"✓ mailday-calendar (calendar editing)",
		"✓ sending identities: 1 (from ~/.config/mailday/config.toml)",
		"✓ Exchange token ~/.config/mailday/ews-token.gpg",
		"✓ Google calendar module ~/gcal.py",
		"✓ claude (reply judge)",
		"✓ editor nvim",
		"✓ time zone Europe/Amsterdam (home)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

func TestChecklistMissingThingsCarryAHint(t *testing.T) {
	home := checkEnvironment(t)
	root := filepath.Join(home, "Mail")
	if err := os.MkdirAll(filepath.Join(root, "gmail", "Inbox", "cur"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "stray", "Inbox", "cur"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home, ".mbsyncrc"), testMbsyncrc)
	useConfig(t, home, config.Config{
		Exchange: config.Exchange{User: "s@uni.example"},
		Calendar: config.Calendar{GoogleScript: "~/gcal.py"},
		Judge:    config.Judge{Enabled: true},
	})
	mail, mailErr := listMail(t, root)

	lines := checklist(checkInput{Home: home, MailRoots: []string{root}, Mail: mail, MailErr: mailErr})
	text := linesOf(lines)
	for _, want := range []string{
		"✓ mbsync account gmail has a Maildir (gmail)",
		"✗ mbsync account uni has no Maildir with an Inbox → run mbsync uni",
		"✗ Maildir account stray is not in ~/.mbsyncrc → ",
		"✗ msmtp (sending mail) → install msmtp",
		"✗ notmuch (search with /) → ",
		"✗ mailday-syncd running (live sync) → ",
		"✗ mailday-calendar (calendar editing) → ",
		"✗ mailday-calsync ",
		"✗ sending identities → ",
		"✗ Exchange token ~/.config/mailday/ews-token.gpg → run mailday-exchange authorize",
		"✗ Google calendar module ~/gcal.py → ",
		"✗ claude (reply judge) → ",
		"✗ editor nvim (ctrl+o) → install nvim or set $EDITOR",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	for _, line := range lines {
		if !line.OK && !strings.Contains(line.String(), " → ") {
			t.Errorf("a failed line without a hint: %q", line.Text)
		}
	}
}

func TestChecklistWithoutMbsyncrcOrMailRoots(t *testing.T) {
	home := checkEnvironment(t)
	root := filepath.Join(home, "Mail")
	mail, mailErr := listMail(t, root)
	if mailErr == nil {
		t.Fatal("a missing mail root should be an error from the store")
	}
	text := linesOf(checklist(checkInput{Home: home, MailRoots: []string{root}, Mail: mail, MailErr: mailErr}))
	for _, want := range []string{"✗ mail roots: none of ~/Mail exists → ", "✗ ~/.mbsyncrc → "} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

func TestChecklistEditorFromTheEnvironment(t *testing.T) {
	checkEnvironment(t, "hx")
	t.Setenv("EDITOR", "hx --wait")
	if line := editorLine(); !line.OK || !strings.Contains(line.Text, "$EDITOR=hx") {
		t.Errorf("EDITOR on PATH: %+v", line)
	}
	t.Setenv("MAILDAY_EDITOR", "missing-editor")
	if line := editorLine(); line.OK || !strings.Contains(line.String(), "missing-editor") {
		t.Errorf("MAILDAY_EDITOR off PATH: %+v", line)
	}
}

func TestChecklistTimeZone(t *testing.T) {
	home := checkEnvironment(t)
	useConfig(t, home, config.Config{Calendar: config.Calendar{HomeZone: "Europe/Amsterdam"}})
	if line := zoneLine(); !line.OK || line.Text != "time zone Europe/Amsterdam (home)" {
		t.Errorf("at home: %+v", line)
	}
	t.Setenv("TZ", "Asia/Seoul")
	if line := zoneLine(); !line.OK || line.Text != "time zone Asia/Seoul (away from home zone Europe/Amsterdam)" {
		t.Errorf("away: %+v", line)
	}
	t.Setenv("MAILDAY_HOME_TZ", "Asia/Seoul")
	if line := zoneLine(); line.Text != "time zone Asia/Seoul (home)" {
		t.Errorf("home zone from the environment: %+v", line)
	}
	t.Setenv("MAILDAY_HOME_TZ", "")
	useConfig(t, home, config.Config{})
	if line := zoneLine(); line.Text != "time zone Asia/Seoul (home)" {
		t.Errorf("without a home zone, here is home: %+v", line)
	}
}

func TestRunCheckPrintsChecklistThenKeyValues(t *testing.T) {
	home := checkEnvironment(t, "mbsync")
	root := filepath.Join(home, "Mail")
	if err := os.MkdirAll(filepath.Join(root, "gmail", "Inbox", "cur"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := runCheck(&out, home, maildir.New([]string{root}, 10), calendar.NewStore(home), 14)
	if err != nil {
		t.Fatalf("missing tools must not fail the check: %v", err)
	}
	text := out.String()
	checklistEnd := strings.Index(text, "\n\n")
	if checklistEnd < 0 || !strings.HasPrefix(text, "✓ mail: 1 account (gmail)\n") {
		t.Fatalf("checklist first, then a blank line:\n%s", text)
	}
	for _, want := range []string{"\nmail_accounts=1\n", "\ntool_mbsync=true\n", "\ntool_neomutt=false\n", "\nsyncd_running=false\n", "\ncalendar_sources=0\n"} {
		if !strings.Contains(text[checklistEnd:], want) {
			t.Errorf("key=value line %q missing:\n%s", want, text)
		}
	}
	if strings.Contains(text[:checklistEnd], "mail_accounts=") {
		t.Errorf("key=value lines leaked into the checklist:\n%s", text)
	}
}

func TestRunCheckFailsOnlyWithoutMailRoots(t *testing.T) {
	home := checkEnvironment(t)
	var out bytes.Buffer
	err := runCheck(&out, home, maildir.New([]string{filepath.Join(home, "Mail")}, 10), calendar.NewStore(home), 14)
	if err == nil {
		t.Fatal("no mail roots must fail the check")
	}
	if !strings.Contains(out.String(), "✗ mail roots: none of ~/Mail exists") {
		t.Errorf("the checklist is still printed:\n%s", out.String())
	}
}

func TestCheckSaysWhenTheGoogleLoginExpired(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	useConfig(t, home, config.Config{Calendar: config.Calendar{GoogleScript: "~/gcal.py"}})
	os.MkdirAll(filepath.Join(home, "state", "mailday"), 0o700)
	os.WriteFile(filepath.Join(home, "state", "mailday", "google-login"), []byte("needed 2026-10-07T00:00:00+00:00\n"), 0o600)
	var found bool
	for _, line := range checklist(checkInput{Home: home}) {
		if strings.Contains(line.Text, "Google login expired") {
			found = !line.OK
		}
	}
	if !found {
		t.Fatal("an expired login should be a ✗ line")
	}
}

// useConfig makes c the loaded config, with its files under home, and
// reloads the identities from it.
func useConfig(t *testing.T, home string, c config.Config) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("MAILDAY_EXCHANGE_TOKEN", "")
	config.Set(&c)
	compose.Reload()
	t.Cleanup(func() { config.Set(nil); compose.Reload() })
}
