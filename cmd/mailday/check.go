package main

import (
	"fmt"
	"github.com/aronvandepol/mailday/internal/compose"
	"github.com/aronvandepol/mailday/internal/config"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aronvandepol/mailday/internal/maildir"
	"github.com/aronvandepol/mailday/internal/mbsyncrc"
	"github.com/aronvandepol/mailday/internal/syncd"
)

// checkLine is one row of the `mailday --check` checklist: what was looked
// for, and when it is missing, the one thing to do about it.
type checkLine struct {
	OK   bool
	Text string
	Hint string
}

func (line checkLine) String() string {
	if line.OK {
		return "✓ " + line.Text
	}
	return "✗ " + line.Text + " → " + line.Hint
}

// checkInput is what the checklist needs from the loaded mail and calendar.
// The rest it reads itself (PATH, files under Home, TZ), which is fast and
// needs no network.
type checkInput struct {
	Home            string
	MailRoots       []string
	Mail            maildir.ListResult
	MailErr         error
	CalendarSources int
	CalendarErr     error
	SyncdRunning    bool
}

// checklist runs every check. It never prints message or event text.
func checklist(in checkInput) []checkLine {
	var lines []checkLine
	add := func(ok bool, text, hint string) { lines = append(lines, checkLine{ok, text, hint}) }
	tilde := func(path string) string {
		if rest, found := strings.CutPrefix(path, in.Home+string(os.PathSeparator)); found {
			return "~/" + rest
		}
		return path
	}
	exists := func(path string) bool {
		_, err := os.Stat(path)
		return err == nil
	}
	tool := func(name, purpose, hint string) {
		_, err := exec.LookPath(name)
		add(err == nil, fmt.Sprintf("%s (%s)", name, purpose), hint)
	}

	var roots []string
	for _, root := range in.MailRoots {
		roots = append(roots, tilde(root))
	}
	switch {
	case in.MailErr != nil:
		add(false, "mail roots: none of "+strings.Join(roots, ", ")+" exists", "run mbsync -a once, or point MAILDAY_MAIL_ROOT at your Maildirs")
	case len(in.Mail.Accounts) == 0:
		add(false, "mail roots exist but hold no account with an Inbox", "run mbsync -a to download the mail")
	default:
		noun := "accounts"
		if len(in.Mail.Accounts) == 1 {
			noun = "account"
		}
		add(true, fmt.Sprintf("mail: %d %s (%s)", len(in.Mail.Accounts), noun, strings.Join(in.Mail.Accounts, ", ")), "")
	}
	lines = append(lines, mbsyncAccountLines(in)...)

	tool("mbsync", "mail sync", "install isync")
	tool("neomutt", "attachments and everything else", "install neomutt")
	tool("sqlite3", "Evolution calendar cache", "install sqlite3")
	tool("msmtp", "sending mail", "install msmtp and write ~/.msmtprc")
	tool("notmuch", "search with /", "install notmuch and run notmuch new")
	add(in.SyncdRunning, "mailday-syncd running (live sync)", "start it with make install-syncd; until then press s to sync")
	tool("mailday-calendar", "calendar editing", "make install puts it in ~/.local/bin")
	tool("mailday-calsync", "Google and Exchange calendars", "make install puts it in ~/.local/bin")

	switch {
	case in.CalendarErr != nil:
		add(false, "calendars load", in.CalendarErr.Error())
	default:
		add(in.CalendarSources > 0, fmt.Sprintf("calendar sources: %d", in.CalendarSources), "run mailday-calsync, or put .ics files in ~/.local/share/mailday/calendars")
	}

	cfg := config.Get()
	if err := config.Err(); err != nil {
		add(false, "config "+tilde(config.Path()), err.Error())
	}
	if len(compose.Identities) == 0 {
		add(false, "sending identities", "add [[identity]] entries to "+tilde(config.Path())+", or from lines to your msmtp config")
	} else {
		source := "from " + tilde(config.Path())
		if len(cfg.Identity) == 0 {
			source = "from the msmtp config"
		}
		add(true, fmt.Sprintf("sending identities: %d (%s)", len(compose.Identities), source), "")
	}

	if cfg.Exchange.User != "" {
		exchangeToken := config.ExchangeToken()
		add(exists(exchangeToken), "Exchange token "+tilde(exchangeToken), "run mailday-exchange authorize")
	}
	if script := cfg.Calendar.GoogleScript; script != "" {
		script = config.ExpandHome(script)
		add(exists(script), "Google calendar module "+tilde(script), "google_script in config.toml names a file that is not there")
		// The login outlives its token (revoked, or 7 days in Testing mode);
		// mailday-calendar records what happened the last time it used it.
		if data, err := os.ReadFile(filepath.Join(stateDir(in.Home), "google-login")); err == nil {
			if state := strings.Fields(string(data)); len(state) > 0 && state[0] == "needed" {
				add(false, "Google login expired (invitations and answers)", "run mailday-calendar login; to keep it, set the Google Cloud OAuth app to In production")
			}
		}
	}

	if cfg.Judge.Enabled {
		tool("claude", "reply judge", "install the Claude CLI, or turn [reply_judge] off")
	}
	lines = append(lines, editorLine())
	lines = append(lines, zoneLine())
	return lines
}

// mbsyncAccountLines sets the accounts in ~/.mbsyncrc beside the Maildir
// accounts found: one without the other is a sync that never ran, or mail
// that nothing keeps current.
func mbsyncAccountLines(in checkInput) []checkLine {
	var path string
	for _, candidate := range []string{filepath.Join(in.Home, ".mbsyncrc"), filepath.Join(in.Home, ".config", "isyncrc")} {
		if _, err := os.Stat(candidate); err == nil {
			path = candidate
			break
		}
	}
	if path == "" {
		return []checkLine{{false, "~/.mbsyncrc", "mbsync has no configuration, so nothing syncs"}}
	}
	config, err := mbsyncrc.Load(path)
	if err != nil {
		return []checkLine{{false, "~/.mbsyncrc readable", err.Error()}}
	}
	have := map[string]bool{}
	for _, account := range in.Mail.Accounts {
		have[account] = true
	}
	var lines []checkLine
	matched := map[string]bool{}
	for _, name := range config.AccountNames() {
		label := ""
		for _, channel := range config.Channels {
			if config.AccountOf(channel) != name {
				continue
			}
			if store := config.MaildirStores[channel.NearStore]; store != nil && store.Path != "" {
				label = maildirLabel(filepath.Base(filepath.Clean(store.Path)))
				break
			}
		}
		if label != "" && have[label] {
			matched[label] = true
			lines = append(lines, checkLine{true, fmt.Sprintf("mbsync account %s has a Maildir (%s)", name, label), ""})
		} else {
			lines = append(lines, checkLine{false, fmt.Sprintf("mbsync account %s has no Maildir with an Inbox", name), "run mbsync " + name})
		}
	}
	var orphans []string
	for _, account := range in.Mail.Accounts {
		if !matched[account] {
			orphans = append(orphans, account)
		}
	}
	sort.Strings(orphans)
	for _, account := range orphans {
		lines = append(lines, checkLine{false, fmt.Sprintf("Maildir account %s is not in ~/.mbsyncrc", account), "nothing keeps it in sync: add a channel for it"})
	}
	return lines
}

// maildirLabel is how the mail view names an account folder: mutt-wizard's
// "me@gmail.com" shows as "gmail" (the same rule as maildir's account label).
func maildirLabel(dir string) string {
	if at := strings.LastIndex(dir, "@"); at > 0 && at < len(dir)-1 {
		domain := dir[at+1:]
		if dot := strings.Index(domain, "."); dot > 0 {
			domain = domain[:dot]
		}
		return domain
	}
	return dir
}

// editorLine checks the editor ctrl+o starts: $MAILDAY_EDITOR, $VISUAL,
// $EDITOR, else nvim.
func editorLine() checkLine {
	for _, name := range []string{"MAILDAY_EDITOR", "VISUAL", "EDITOR"} {
		value := strings.Fields(os.Getenv(name))
		if len(value) == 0 {
			continue
		}
		if _, err := exec.LookPath(value[0]); err != nil {
			return checkLine{false, fmt.Sprintf("editor $%s=%s", name, value[0]), "it is not on PATH"}
		}
		return checkLine{true, fmt.Sprintf("editor $%s=%s (ctrl+o)", name, value[0]), ""}
	}
	_, err := exec.LookPath("nvim")
	return checkLine{err == nil, "editor nvim (ctrl+o)", "install nvim or set $EDITOR"}
}

// zoneLine says the time zone and whether it is the home zone
// (MAILDAY_HOME_TZ, else [calendar].home_zone; without either, here is home). Being away is not a fault.
func zoneLine() checkLine {
	zone, err := syncd.ZoneName()
	if err != nil {
		zone = time.Now().Format("MST") // no zoneinfo name to be had: the abbreviation
	}
	home := strings.TrimSpace(os.Getenv("MAILDAY_HOME_TZ"))
	if home == "" {
		home = strings.TrimSpace(config.Get().Calendar.HomeZone)
	}
	if home == "" || zone == home {
		return checkLine{true, "time zone " + zone + " (home)", ""}
	}
	return checkLine{true, fmt.Sprintf("time zone %s (away from home zone %s)", zone, home), ""}
}

func stateDir(home string) string {
	if state := os.Getenv("XDG_STATE_HOME"); state != "" {
		return filepath.Join(state, "mailday")
	}
	return filepath.Join(home, ".local", "state", "mailday")
}
