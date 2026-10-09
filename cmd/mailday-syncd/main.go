// mailday-syncd keeps the mbsync Maildirs current: IMAP IDLE on the inbox and
// the reply boxes, an immediate push of whatever Mailday changes, a catch-up
// after suspend, and a full mbsync every so often as a safety net.
//
//	mailday-syncd                  run the daemon (systemd user service)
//	mailday-syncd sync [--push] [--wait] [PATH...]
//	                               sync these Maildir folders, or everything
//	mailday-syncd status           show accounts, watches and last sync
//	mailday-syncd lease NAME LENGTH -- COMMAND...
//	                               run COMMAND unless the other machine holds
//	                               the NAME lease (renewed for LENGTH each run)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/aronvandepol/mailday/internal/config"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aronvandepol/mailday/internal/syncd"
)

func main() {
	// journald stamps lines itself; elsewhere (the macOS log file) add a time.
	if os.Getenv("JOURNAL_STREAM") == "" {
		log.SetFlags(log.Ldate | log.Ltime)
	} else {
		log.SetFlags(0)
	}
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "sync":
			os.Exit(runSync(os.Args[2:]))
		case "status":
			os.Exit(runStatus())
		case "lease":
			os.Exit(runLease(os.Args[2:]))
		case "run":
			os.Args = append(os.Args[:1], os.Args[2:]...)
		}
	}
	if err := runDaemon(); err != nil {
		log.Fatal("mailday-syncd: ", err)
	}
}

func runDaemon() error {
	config := flag.String("config", "", "mbsync configuration (default ~/.mbsyncrc)")
	idle := flag.String("idle", "INBOX,@Reply,@Waiting", "remote mailboxes to watch with IMAP IDLE, comma-separated")
	refresh := flag.Duration("refresh", 30*time.Second, "interrupt IDLE for a NOOP this often (Gmail's IDLE alone is late)")
	sweep := flag.Duration("sweep", 30*time.Minute, "interval of the full mbsync of every account (0 disables)")
	settle := flag.Duration("settle", 400*time.Millisecond, "wait this long to merge bursts before running mbsync")
	notify := flag.Bool("notify", true, "desktop notification for new inbox mail")
	postSync := flag.String("post-sync", defaultPostSync(), "shell command to run after syncs (default: [sync].post_sync in config.toml, else notmuch new when notmuch is installed)")
	poll := flag.Duration("poll", 30*time.Second, "check every other synced mailbox with STATUS this often (0 disables)")
	calendar := flag.String("calendar", "auto", `command that refreshes the calendar files ("auto": mailday-calsync if installed, "" disables)`)
	calendarActive := flag.Duration("calendar-active", 3*time.Minute, "calendar refresh interval while Mailday is open")
	calendarIdle := flag.Duration("calendar-idle", 15*time.Minute, "calendar refresh interval otherwise")
	remind := flag.Duration("remind", 10*time.Minute, "notify this long before timed events (Linux; 0 turns it off)")
	remindAll := flag.Bool("remind-all", false, "also remind for calendars evolution-alarm-notify covers")
	verbose := flag.Bool("v", false, "log every sync and IDLE event")
	flag.Parse()

	var mailboxes []string
	for _, mailbox := range strings.Split(*idle, ",") {
		if mailbox = strings.TrimSpace(mailbox); mailbox != "" {
			mailboxes = append(mailboxes, mailbox)
		}
	}
	daemon, err := syncd.New(syncd.Options{
		ConfigPath:     *config,
		Idle:           mailboxes,
		Refresh:        *refresh,
		Sweep:          *sweep,
		Settle:         *settle,
		Notify:         *notify,
		PostSync:       *postSync,
		Poll:           *poll,
		Calendar:       calendarCommand(*calendar),
		CalendarActive: *calendarActive,
		CalendarIdle:   *calendarIdle,
		Remind:         *remind,
		RemindAll:      *remindAll,
		Verbose:        *verbose,
	})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return daemon.Run(ctx)
}

// runLease runs a job on one machine at a time; see syncd.Lease.
func runLease(args []string) int {
	separator := -1
	for index, arg := range args {
		if arg == "--" {
			separator = index
			break
		}
	}
	if separator != 2 || len(args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: mailday-syncd lease NAME LENGTH -- COMMAND...")
		return 2
	}
	length, err := time.ParseDuration(args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "mailday-syncd lease:", err)
		return 2
	}
	if _, err := syncd.TakeLease(args[0], length, time.Now()); err != nil {
		if errors.Is(err, syncd.ErrLeaseHeld) {
			fmt.Printf("%s: skipped, %v\n", args[0], err)
			return 0
		}
		fmt.Fprintln(os.Stderr, "mailday-syncd lease:", err)
		return 1
	}
	command := exec.Command(args[3], args[4:]...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	runErr := command.Run()
	// Renew at the end too: a long run should not let the lease lapse.
	_, _ = syncd.TakeLease(args[0], length, time.Now())
	if exit, ok := runErr.(*exec.ExitError); ok {
		return exit.ExitCode()
	}
	if runErr != nil {
		fmt.Fprintln(os.Stderr, "mailday-syncd lease:", runErr)
		return 1
	}
	return 0
}

func calendarCommand(value string) string {
	if value != "auto" {
		return value
	}
	if path, err := exec.LookPath("mailday-calsync"); err == nil {
		return path
	}
	return ""
}

func runSync(args []string) int {
	flags := flag.NewFlagSet("sync", flag.ExitOnError)
	push := flags.Bool("push", false, "only carry local changes to the server")
	wait := flags.Bool("wait", false, "return when the sync has finished")
	_ = flags.Parse(args)
	timeout := 5 * time.Second
	if *wait {
		timeout = 20 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if _, err := syncd.Send(ctx, syncd.Request{Op: "sync", Paths: flags.Args(), Push: *push, Wait: *wait}); err != nil {
		fmt.Fprintln(os.Stderr, "mailday-syncd:", err)
		return 1
	}
	return 0
}

func runStatus() int {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	response, err := syncd.Send(ctx, syncd.Request{Op: "status"})
	if err != nil {
		fmt.Fprintln(os.Stderr, "mailday-syncd:", err)
		return 1
	}
	width := 0
	for _, account := range response.Status.Accounts {
		width = max(width, len(account.Name))
	}
	for _, account := range response.Status.Accounts {
		state := "idle"
		if account.Syncing {
			state = "syncing"
		}
		last := "never"
		if !account.LastSync.IsZero() {
			last = time.Since(account.LastSync).Round(time.Second).String() + " ago"
		}
		fmt.Printf("%-*s  %-8s last sync %-10s watching %s", width, account.Name, state, last, strings.Join(account.Watching, ","))
		if len(account.Offline) > 0 {
			fmt.Printf("  offline %s", strings.Join(account.Offline, ","))
		}
		if account.LastError != "" {
			fmt.Printf("\n%*s  error: %s", width, "", account.LastError)
		}
		fmt.Println()
	}
	if zone := response.Status.Zone; zone != "" {
		fmt.Printf("%-*s  %s\n", width, "zone", zone)
	}
	if calendar := response.Status.Calendar; calendar.Enabled {
		last := "never"
		if !calendar.LastSync.IsZero() {
			last = time.Since(calendar.LastSync).Round(time.Second).String() + " ago"
		}
		fmt.Printf("%-*s  %-8s last sync %s\n", width, "calendar", map[bool]string{true: "syncing", false: "idle"}[calendar.Syncing], last)
		if calendar.LastError != "" {
			fmt.Printf("%*s  error: %s\n", width, "", calendar.LastError)
		}
	}
	return 0
}

// defaultPostSync is [sync].post_sync, else "notmuch new" when notmuch is
// installed, so Mailday's / finds new mail as it arrives.
func defaultPostSync() string {
	if command := config.Get().Sync.PostSync; command != "" {
		return command
	}
	if _, err := exec.LookPath("notmuch"); err == nil {
		return "notmuch new --quiet"
	}
	return ""
}
