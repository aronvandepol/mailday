package syncd

import (
	"bufio"
	"context"
	"log"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/aronvandepol/mailday/internal/calendar"
)

// Reminders (Linux only): a notification Remind before each timed event,
// with Join when the event has a call link and Open for Mailday's calendar.
// Calendars Evolution also holds are left to evolution-alarm-notify unless
// RemindAll is set, so Google events do not pop up twice.

var callLink = regexp.MustCompile(`https?://[^\s<>"')\]]*(teams\.microsoft\.com|teams\.live\.com|zoom\.us|meet\.google\.com|webex\.com|whereby\.com)[^\s<>"')\]]*`)

func (d *Daemon) remindLoop(ctx context.Context) {
	if d.options.Remind <= 0 || runtime.GOOS != "linux" {
		return
	}
	home, _ := os.UserHomeDir()
	store := calendar.NewStore(home)
	reminded := map[string]time.Time{} // key -> event start
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		now := time.Now()
		loadCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		result, err := store.Load(loadCtx, now.Add(-time.Hour), now.Add(d.options.Remind+time.Hour))
		cancel()
		if err == nil {
			evolution := map[string]bool{}
			for _, source := range result.Sources {
				if source.Backend != "ics" {
					evolution[source.Name] = true
				}
			}
			skip := func(event calendar.Event) bool { return evolution[event.Source] && !d.options.RemindAll }
			for _, event := range remindersDue(result.Events, now, d.options.Remind, skip, reminded, userActive) {
				go d.remind(event)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// remindersDue returns the events to remind of at now and marks them in
// reminded (event key -> start). Whether anyone is at the machine is asked
// before marking, so an event that came due while the user was away stays
// due on later ticks until it starts. Keys of events that started more than
// two hours ago are dropped: they are no longer loaded, so cannot recur.
func remindersDue(events []calendar.Event, now time.Time, lead time.Duration, skip func(calendar.Event) bool, reminded map[string]time.Time, active func() bool) []calendar.Event {
	for key, start := range reminded {
		if now.Sub(start) > 2*time.Hour {
			delete(reminded, key)
		}
	}
	var due []calendar.Event
	queued := map[string]bool{}
	for _, event := range events {
		key := event.UID + "/" + event.Start.UTC().Format(time.RFC3339)
		if _, done := reminded[key]; done || queued[key] || event.AllDay || event.RSVP == "declined" || now.Before(event.Start.Add(-lead)) || !now.Before(event.Start) {
			continue
		}
		if skip(event) {
			continue
		}
		queued[key] = true
		due = append(due, event)
	}
	if len(due) == 0 || !active() {
		return nil
	}
	for _, event := range due {
		reminded[event.UID+"/"+event.Start.UTC().Format(time.RFC3339)] = event.Start
	}
	return due
}

func (d *Daemon) remind(event calendar.Event) {
	minutes := int(time.Until(event.Start).Round(time.Minute).Minutes())
	body := []string{"in " + plural(minutes, "minute") + " · " + event.Start.Local().Format("15:04") + "–" + event.End.Local().Format("15:04")}
	if event.Location != "" {
		body = append(body, event.Location)
	}
	link := callLink.FindString(event.Location + "\n" + event.Description)
	actions := []string{"open=Open calendar"}
	if link != "" {
		actions = append([]string{"join=Join"}, actions...)
	}
	switch notifyWithActions(event.Summary, strings.Join(body, "\n"), "appointment-soon", actions) {
	case "join":
		_ = exec.Command("xdg-open", link).Start()
	case "open", "default":
		openMailday("--calendar")
	}
}

func plural(count int, word string) string {
	if count == 1 {
		return "1 " + word
	}
	return strconv.Itoa(count) + " " + word + "s"
}

// notifyWithActions shows a notification with buttons and waits for the
// choice (notify-send --wait); "" when it was dismissed or timed out.
func notifyWithActions(title, body, icon string, actions []string) string {
	path, err := exec.LookPath("notify-send")
	if err != nil {
		return ""
	}
	args := []string{"-a", "Mailday", "-i", icon, "--wait"}
	for _, action := range actions {
		args = append(args, "-A", action)
	}
	command := exec.Command(path, append(args, title, body)...)
	output, err := command.StdoutPipe()
	if err != nil || command.Start() != nil {
		return ""
	}
	choice := ""
	scanner := bufio.NewScanner(output)
	if scanner.Scan() {
		choice = strings.TrimSpace(scanner.Text())
	}
	_ = command.Wait()
	return choice
}

// openMailday starts Mailday in a new terminal window.
func openMailday(args ...string) {
	mailday, err := exec.LookPath("mailday")
	if err != nil {
		return
	}
	terminal, err := exec.LookPath("xdg-terminal-exec")
	if err != nil {
		log.Printf("cannot open Mailday: no xdg-terminal-exec")
		return
	}
	command := exec.Command(terminal, append([]string{mailday}, args...)...)
	if command.Start() == nil {
		go command.Wait()
	}
}
