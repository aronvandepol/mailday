package syncd

import (
	"testing"
	"time"

	"github.com/aronvandepol/mailday/internal/calendar"
)

func TestRemindersDue(t *testing.T) {
	now := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	lead := 10 * time.Minute
	event := calendar.Event{UID: "e1", Summary: "Board", Start: now.Add(5 * time.Minute)}
	noSkip := func(calendar.Event) bool { return false }
	present, asked := false, 0
	active := func() bool { asked++; return present }
	reminded := map[string]time.Time{}

	// Nobody there: not reminded, and not marked, so a later tick tries again.
	if due := remindersDue([]calendar.Event{event}, now, lead, noSkip, reminded, active); len(due) != 0 || len(reminded) != 0 {
		t.Fatalf("away: due %v, reminded %v", due, reminded)
	}
	// Back before the event starts: now it is due, once.
	present = true
	later := now.Add(2 * time.Minute)
	if due := remindersDue([]calendar.Event{event}, later, lead, noSkip, reminded, active); len(due) != 1 {
		t.Fatalf("back: due %v", due)
	}
	if due := remindersDue([]calendar.Event{event}, later.Add(30*time.Second), lead, noSkip, reminded, active); len(due) != 0 {
		t.Fatalf("reminded twice: %v", due)
	}
	// After it started it is never due.
	away := map[string]time.Time{}
	if due := remindersDue([]calendar.Event{event}, event.Start.Add(time.Second), lead, noSkip, away, active); len(due) != 0 {
		t.Fatalf("started: %v", due)
	}
	// Presence is only checked when something is due.
	asked = 0
	remindersDue([]calendar.Event{{UID: "far", Start: now.Add(3 * time.Hour)}}, now, lead, noSkip, map[string]time.Time{}, active)
	if asked != 0 {
		t.Fatal("probed the session with nothing due")
	}
	// Skipped (evolution's) and declined events are left alone.
	skipped := remindersDue([]calendar.Event{event}, later, lead, func(calendar.Event) bool { return true }, map[string]time.Time{}, active)
	declined := event
	declined.RSVP = "declined"
	if len(skipped) != 0 || len(remindersDue([]calendar.Event{declined}, later, lead, noSkip, map[string]time.Time{}, active)) != 0 {
		t.Fatal("skipped or declined event was reminded")
	}
}

func TestRemindedKeysArePruned(t *testing.T) {
	now := time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)
	reminded := map[string]time.Time{
		"old/x":    now.Add(-2*time.Hour - time.Minute),
		"recent/x": now.Add(-90 * time.Minute),
	}
	remindersDue(nil, now, 10*time.Minute, func(calendar.Event) bool { return false }, reminded, func() bool { return true })
	if _, ok := reminded["old/x"]; ok || len(reminded) != 1 {
		t.Fatalf("reminded = %v, want only the recent key", reminded)
	}
}

// An event at 10:00Z is the same instant in every zone, so its reminder fires
// ten minutes before it whichever zone time.Local holds, including after the
// zone watcher swaps it. calendar.Store.Load reads time.Local when it parses
// the ICS, which is why reminders follow a swap without any further work.
// The zone watcher's swap itself is tested in zone_test.go.
func TestRemindersDueAtTheSameInstantInAnyZone(t *testing.T) {
	start := time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)
	lead := 10 * time.Minute
	noSkip := func(calendar.Event) bool { return false }
	present := func() bool { return true }
	for _, name := range []string{"Europe/Amsterdam", "Asia/Seoul", "America/Los_Angeles", "UTC"} {
		zone, err := time.LoadLocation(name)
		if err != nil {
			t.Fatal(err)
		}
		// Events carry the local zone as Store.Load gives it; the zones are
		// passed in directly because swapping time.Local would race with the
		// other tests' daemon goroutines.
		event := calendar.Event{UID: "e1", Summary: "Board", Start: start.In(zone), End: start.Add(time.Hour).In(zone)}
		for _, check := range []struct {
			at  time.Time
			due bool
		}{
			{start.Add(-11 * time.Minute), false},
			{start.Add(-10 * time.Minute), true},
			{start.Add(-time.Second), true},
			{start, false},
		} {
			// The clock the loop reads is time.Now() in the local zone.
			due := remindersDue([]calendar.Event{event}, check.at.In(zone), lead, noSkip, map[string]time.Time{}, present)
			if (len(due) == 1) != check.due {
				t.Errorf("%s at %s: due %v, want %v", name, check.at.UTC().Format("15:04:05Z"), len(due) == 1, check.due)
			}
		}
	}
}
