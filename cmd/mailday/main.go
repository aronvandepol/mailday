package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/aronvandepol/mailday/internal/calendar"
	"github.com/aronvandepol/mailday/internal/maildir"
	"github.com/aronvandepol/mailday/internal/syncd"
	"github.com/aronvandepol/mailday/internal/tui"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "mailday:", err)
		os.Exit(1)
	}
}

func run() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("find home directory: %w", err)
	}
	calendarStore := calendar.NewStore(home)
	// mbsync on Linux writes ~/Mail; mutt-wizard on macOS writes
	// ~/.local/share/mail. Missing roots are skipped.
	mailRootDefault := strings.Join([]string{filepath.Join(home, "Mail"), filepath.Join(home, ".local", "share", "mail")}, string(os.PathListSeparator))
	if value := os.Getenv("MAILDAY_MAIL_ROOT"); value != "" {
		mailRootDefault = value
	}

	mailRoot := flag.String("mail-root", mailRootDefault, "Maildir roots containing account directories, separated by "+string(os.PathListSeparator))
	limit := flag.Int("limit", 500, "maximum messages loaded per box")
	days := flag.Int("days", 14, "number of agenda days")
	check := flag.Bool("check", false, "check local integrations and print counts")
	startCalendar := flag.Bool("calendar", false, "start in the calendar")
	openPath := flag.String("open", "", "open this message file on start (used by notifications)")
	calendarCache := flag.String("calendar-cache", calendarStore.CalendarCacheRoot, "Evolution calendar cache root")
	dynamicSources := flag.String("calendar-sources", calendarStore.DynamicSourcesRoot, "Evolution generated source root")
	configSources := flag.String("calendar-config", calendarStore.ConfigSourcesRoot, "Evolution account source root")
	icsRoot := flag.String("ics-dir", calendarStore.ICSRoot, "directory containing local ICS files")
	flag.Parse()

	if *limit <= 0 {
		return errors.New("--limit must be positive")
	}
	if *days <= 0 || *days > 366 {
		return errors.New("--days must be between 1 and 366")
	}
	calendarStore.CalendarCacheRoot = expandHome(*calendarCache, home)
	calendarStore.DynamicSourcesRoot = expandHome(*dynamicSources, home)
	calendarStore.ConfigSourcesRoot = expandHome(*configSources, home)
	calendarStore.ICSRoot = expandHome(*icsRoot, home)
	var roots []string
	for _, root := range filepath.SplitList(*mailRoot) {
		roots = append(roots, expandHome(root, home))
	}
	mailStore := maildir.New(roots, *limit)

	if *check {
		return runCheck(os.Stdout, home, mailStore, calendarStore, *days)
	}
	return tui.Run(mailStore, calendarStore, tui.Options{Days: *days, MailRoots: mailStore.Roots, StartCalendar: *startCalendar, OpenPath: *openPath})
}

// runCheck prints the checklist, then the key=value lines scripts read. It
// fails only when the mail roots are missing: a missing tool is a ✗, not an
// error.
func runCheck(out io.Writer, home string, mailStore *maildir.Store, calendarStore *calendar.Store, days int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	mailResult, mailErr := mailStore.List(ctx)
	now := time.Now()
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	calendarResult, calendarErr := calendarStore.Load(ctx, from, from.AddDate(0, 0, days))

	unread := 0
	for _, message := range mailResult.Messages {
		if message.Unread {
			unread++
		}
	}
	google, exchange, other := 0, 0, 0
	for _, source := range calendarResult.Sources {
		switch strings.ToLower(source.Provider) {
		case "google":
			google++
		case "exchange":
			exchange++
		default:
			other++
		}
	}

	statusCtx, statusCancel := context.WithTimeout(context.Background(), 2*time.Second)
	_, syncdErr := syncd.Send(statusCtx, syncd.Request{Op: "status"})
	statusCancel()
	for _, line := range checklist(checkInput{
		Home: home, MailRoots: mailStore.Roots, Mail: mailResult, MailErr: mailErr,
		CalendarSources: len(calendarResult.Sources), CalendarErr: calendarErr, SyncdRunning: syncdErr == nil,
	}) {
		fmt.Fprintln(out, line)
	}
	fmt.Fprintln(out)

	perBox := make(map[string]int)
	for _, message := range mailResult.Messages {
		perBox[message.Box]++
	}
	fmt.Fprintf(out, "mail_roots=%s\n", strings.Join(mailStore.Roots, string(os.PathListSeparator)))
	fmt.Fprintf(out, "mail_accounts=%d\n", len(mailResult.Accounts))
	fmt.Fprintf(out, "messages_loaded=%d\n", len(mailResult.Messages))
	for _, box := range mailResult.Boxes {
		fmt.Fprintf(out, "box_%s=%d\n", strings.TrimPrefix(box, "@"), perBox[box])
	}
	fmt.Fprintf(out, "unread_loaded=%d\n", unread)
	fmt.Fprintf(out, "mail_skipped=%d\n", mailResult.Skipped)
	fmt.Fprintf(out, "calendar_sources=%d\n", len(calendarResult.Sources))
	fmt.Fprintf(out, "google_calendar_sources=%d\n", google)
	fmt.Fprintf(out, "exchange_calendar_sources=%d\n", exchange)
	fmt.Fprintf(out, "other_calendar_sources=%d\n", other)
	fmt.Fprintf(out, "agenda_events=%d\n", len(calendarResult.Events))
	fmt.Fprintf(out, "warnings=%d\n", len(mailResult.Warnings)+len(calendarResult.Warnings))
	for _, tool := range []string{"mbsync", "neomutt", "sqlite3"} {
		_, err := exec.LookPath(tool)
		fmt.Fprintf(out, "tool_%s=%t\n", tool, err == nil)
	}
	fmt.Fprintf(out, "syncd_running=%t\n", syncdErr == nil)

	return mailErr
}

func expandHome(path, home string) string {
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	return path
}
