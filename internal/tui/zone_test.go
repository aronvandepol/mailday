package tui

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/aronvandepol/mailday/internal/calendar"
)

// zoneChildEnv marks the child process the zone tests run in.
const zoneChildEnv = "MAILDAY_ZONE_TEST_CHILD"

// zoneTests are the tests that swap time.Local. Swapping it is a data race
// with any goroutine an earlier test left behind (the mail watchers of
// live_watch_test.go never stop), and the race detector crashes reporting it
// at exit. They therefore run alone, in a child of the test binary, started
// by TestZoneTestsRunInAProcessOfTheirOwn; in the main run they skip.
var zoneTests = []string{
	"TestClockTickFollowsTheSystemZone",
	"TestZoneChangeReloadsTheCalendarForTheNewZone",
	"TestZoneFollowingIgnoresErrorsAndUnknownZones",
	"TestHomeTimeShowsBesideLocalWhenAway",
	"TestFooterRuleNamesTheZoneWhenAway",
	"TestNothingExtraAtHomeOrInAnEqualOffsetZone",
	"TestHomeZoneComesFromTheEnvironmentAndAllDayIsLeftAlone",
	"TestHomeTimeOnAnotherDayCarriesTheWeekday",
	"TestSnoozedRowsFollowTheZoneInUse",
}

func TestZoneTestsRunInAProcessOfTheirOwn(t *testing.T) {
	if os.Getenv(zoneChildEnv) != "" {
		t.Skip("this is the child")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	command := exec.Command(executable, "-test.v", "-test.count=1", "-test.run", "^("+strings.Join(zoneTests, "|")+")$")
	command.Env = append(os.Environ(), zoneChildEnv+"=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("zone tests failed: %v\n%s", err, output)
	}
	for _, name := range zoneTests {
		// A skip means the machine has no zoneinfo for the zone.
		if !strings.Contains(string(output), "--- PASS: "+name+" ") && !strings.Contains(string(output), "--- SKIP: "+name+" ") {
			t.Errorf("%s did not run in the child:\n%s", name, output)
		}
	}
}

// useZone points time.Local at name for one test and restores it. It skips
// outside the child process (see zoneTests).
func useZone(t *testing.T, name string) *time.Location {
	t.Helper()
	if os.Getenv(zoneChildEnv) == "" {
		t.Skip("runs in TestZoneTestsRunInAProcessOfTheirOwn")
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("no zoneinfo for %s: %v", name, err)
	}
	previous := time.Local
	time.Local = location
	t.Cleanup(func() { time.Local = previous })
	return location
}

func TestClockTickFollowsTheSystemZone(t *testing.T) {
	useZone(t, "Europe/Amsterdam")
	seoul, err := time.LoadLocation("Asia/Seoul")
	if err != nil {
		t.Skip(err)
	}
	system := "Europe/Amsterdam"
	model := calendarModel(t)
	model.status = ""
	model.now = time.Now
	model.zone = zoneState{resolve: func() (string, error) { return system, nil }, name: "Europe/Amsterdam"}
	model.calendarAnchor = time.Date(2026, 10, 12, 0, 0, 0, 0, time.Local)

	updated, command := model.Update(clockTickMsg{})
	if got := updated.(Model); got.status != "" || got.zone.name != "Europe/Amsterdam" {
		t.Fatalf("an unchanged zone said %q", got.status)
	}
	if command == nil {
		t.Fatal("the tick should schedule the next one")
	}

	system = "Asia/Seoul"
	updated, _ = model.Update(clockTickMsg{})
	got := updated.(Model)
	if got.status != "Time zone is now Asia/Seoul" {
		t.Fatalf("status = %q", got.status)
	}
	if time.Local.String() != seoul.String() {
		t.Fatalf("time.Local = %v, want Seoul", time.Local)
	}
	if offset := time.Date(2026, 10, 12, 12, 0, 0, 0, time.Local).Format("-0700"); offset != "+0900" {
		t.Fatalf("local offset = %s", offset)
	}
	// The date on screen stays, at midnight of the new zone.
	if anchor := got.calendarAnchor; anchor.Day() != 12 || anchor.Hour() != 0 || anchor.Format("-0700") != "+0900" {
		t.Fatalf("anchor = %v", anchor)
	}
}

func TestZoneChangeReloadsTheCalendarForTheNewZone(t *testing.T) {
	useZone(t, "Europe/Amsterdam")
	model := calendarModel(t)
	model.now = time.Now
	model.zone = zoneState{resolve: func() (string, error) { return "Asia/Seoul", nil }, name: "Europe/Amsterdam"}
	next, command := model.followZone()
	if command == nil || next.status != "Time zone is now Asia/Seoul" {
		t.Fatalf("command %v, status %q", command, next.status)
	}
	loaded, ok := command().(calendarLoadedMsg)
	if !ok || !loaded.quiet {
		t.Fatalf("want a quiet calendar load, got %#v", loaded)
	}
	if loaded.from.Format("-0700") != "+0900" || loaded.from.Hour() != 0 {
		t.Fatalf("range starts %v, want midnight in Seoul", loaded.from)
	}
}

func TestZoneFollowingIgnoresErrorsAndUnknownZones(t *testing.T) {
	useZone(t, "Europe/Amsterdam")
	model := calendarModel(t)
	model.status = ""
	model.zone = zoneState{resolve: func() (string, error) { return "", errors.New("no /etc/localtime") }, name: "Europe/Amsterdam"}
	if next, command := model.followZone(); command != nil || next.status != "" {
		t.Fatalf("an unreadable zone changed things: %q", next.status)
	}
	// A name that will not load is reported once, not every 15 seconds.
	model.zone.resolve = func() (string, error) { return "Mars/Olympus", nil }
	first, command := model.followZone()
	if command != nil || !strings.Contains(first.status, "Cannot load time zone Mars/Olympus") {
		t.Fatalf("status %q", first.status)
	}
	first.status = ""
	if again, _ := first.followZone(); again.status != "" {
		t.Fatalf("said it again: %q", again.status)
	}
	// The first reading after a failed start-up only records the zone.
	model.zone = zoneState{resolve: func() (string, error) { return "Asia/Seoul", nil }}
	if next, command := model.followZone(); command != nil || next.zone.name != "Asia/Seoul" || next.status != "" {
		t.Fatalf("first reading: %+v", next.zone)
	}
	if time.Local.String() != mustZone(t, "Europe/Amsterdam").String() {
		t.Fatal("the first reading must not switch zones")
	}
}

func mustZone(t *testing.T, name string) *time.Location {
	t.Helper()
	location, err := time.LoadLocation(name)
	if err != nil {
		t.Skip(err)
	}
	return location
}

func TestModelWithoutLiveDoesNotFollowTheZone(t *testing.T) {
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
	model.status = ""
	if model.zone.resolve != nil {
		t.Fatal("zone following should be live-only")
	}
	if next, command := model.followZone(); command != nil || next.status != "" {
		t.Fatal("followZone without a resolver should do nothing")
	}
}

// A 16:00 Seoul meeting is 09:00 in Amsterdam.
func seoulMeeting() calendar.Event {
	start := time.Date(2026, 10, 14, 16, 0, 0, 0, time.Local)
	return calendar.Event{ID: "s/1", Summary: "Seminar", Source: "Work", Start: start, End: start.Add(time.Hour)}
}

func TestHomeTimeShowsBesideLocalWhenAway(t *testing.T) {
	useZone(t, "Asia/Seoul")
	event := seoulMeeting()
	if got := homeSpan(event); got != "09:00–10:00 Amsterdam" {
		t.Fatalf("homeSpan = %q", got)
	}
	if got := formatEventRange(event); !strings.Contains(got, "16:00 – 17:00") {
		t.Fatalf("local range lost: %q", got)
	}

	// Day panel.
	model := calendarModel(t, event)
	model.calendarMode = calendarDay
	model.calendarAnchor = dayOf(event.Start)
	panel := strings.Join(model.dayPanel(model.calendarAnchor, 60, 20), "\n")
	if !strings.Contains(ansiStrip(panel), "16:00–17:00 · 09:00–10:00 Amsterdam") {
		t.Fatalf("day panel:\n%s", ansiStrip(panel))
	}
	// A narrow panel puts the home time on its own line.
	narrow := ansiStrip(strings.Join(model.dayPanel(model.calendarAnchor, 34, 20), "\n"))
	if !strings.Contains(narrow, "09:00–10:00 Amsterdam") {
		t.Fatalf("narrow panel:\n%s", narrow)
	}

	// Event screen.
	model.screen = screenEvent
	detail := ansiStrip(model.renderEventDetail(120, 20))
	if !strings.Contains(detail, "16:00 – 17:00 · 09:00–10:00 Amsterdam") {
		t.Fatalf("event detail:\n%s", detail)
	}

	// Agenda.
	model.screen = screenHome
	model.calendarMode = calendarAgenda
	model.calendarAnchor = time.Time{}
	agenda := ansiStrip(model.renderCalendarAgenda(140, 20))
	if !strings.Contains(agenda, "16:00–17:00") || !strings.Contains(agenda, "09:00–10:00 Amsterdam") {
		t.Fatalf("agenda:\n%s", agenda)
	}

	// The week grid stays as it was.
	model.calendarMode = calendarWeek
	model.calendarAnchor = dayOf(event.Start)
	if week := ansiStrip(model.renderCalendar(140, 30)); strings.Contains(week, "Amsterdam") {
		t.Fatalf("week grid shows home time:\n%s", week)
	}
}

func TestFooterRuleNamesTheZoneWhenAway(t *testing.T) {
	useZone(t, "Asia/Seoul")
	model := calendarModel(t)
	model.zone = zoneState{name: "Asia/Seoul"}
	if view := ansiStrip(model.View().Content); !strings.Contains(view, "Seoul time") {
		t.Fatalf("footer lacks the zone:\n%s", view)
	}
	// Without a resolved name the abbreviation stands in.
	model.zone = zoneState{}
	if got := model.awayLabel(); got != "KST time" {
		t.Fatalf("awayLabel = %q", got)
	}
}

func TestNothingExtraAtHomeOrInAnEqualOffsetZone(t *testing.T) {
	for _, name := range []string{"Europe/Amsterdam", "Europe/Berlin"} {
		useZone(t, name)
		event := seoulMeeting()
		if got := homeSpan(event); got != "" {
			t.Fatalf("%s: homeSpan = %q", name, got)
		}
		model := calendarModel(t, event)
		if got := model.awayLabel(); got != "" {
			t.Fatalf("%s: awayLabel = %q", name, got)
		}
		if view := ansiStrip(model.View().Content); strings.Contains(view, "Amsterdam time") || strings.Contains(view, "Berlin time") {
			t.Fatalf("%s: footer names a zone:\n%s", name, view)
		}
	}
}

func TestHomeZoneComesFromTheEnvironmentAndAllDayIsLeftAlone(t *testing.T) {
	useZone(t, "Europe/Amsterdam")
	t.Setenv("MAILDAY_HOME_TZ", "Asia/Seoul")
	event := seoulMeeting() // 16:00 Amsterdam
	if got := homeSpan(event); got != "23:00–00:00 Seoul" {
		t.Fatalf("homeSpan = %q", got)
	}
	event.AllDay = true
	if got := homeSpan(event); got != "" {
		t.Fatalf("all-day homeSpan = %q", got)
	}
	// A home name that will not load shows nothing rather than failing.
	t.Setenv("MAILDAY_HOME_TZ", "Mars/Olympus")
	if got := homeSpan(seoulMeeting()); got != "" {
		t.Fatalf("bad home zone: %q", got)
	}
}

func TestHomeTimeOnAnotherDayCarriesTheWeekday(t *testing.T) {
	useZone(t, "Asia/Seoul")
	start := time.Date(2026, 10, 14, 2, 0, 0, 0, time.Local) // 19:00 the day before, Amsterdam
	event := calendar.Event{Summary: "Early", Start: start, End: start.Add(time.Hour)}
	if got := homeSpan(event); got != "Tue 19:00–20:00 Amsterdam" {
		t.Fatalf("homeSpan = %q", got)
	}
}
