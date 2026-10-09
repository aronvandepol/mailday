package when

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Tuesday 6 October 2026, 14:10.
var now = time.Date(2026, 10, 6, 14, 10, 0, 0, amsterdam())

// amsterdam is the zone the expected values are written in (see TestMain);
// package variables are set before TestMain runs, so it is loaded here.
func amsterdam() *time.Location {
	zone, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		return time.Local
	}
	return zone
}

func TestNewEvents(t *testing.T) {
	selected := time.Date(2026, 10, 8, 0, 0, 0, 0, time.Local) // Thursday in the calendar
	cases := []struct {
		input, title, location, calendar string
		start, end                       string
		allDay                           bool
	}{
		{"Lunch with Ada tomorrow 12:30 1h @Atrium 2.04 #University", "Lunch with Ada", "Atrium 2.04", "University", "2026-10-07 12:30", "2026-10-07 13:30", false},
		{"Gym fri 18:30-20:00", "Gym", "", "", "2026-10-09 18:30", "2026-10-09 20:00", false},
		{"Call 2pm for 30m", "Call", "", "", "2026-10-08 14:00", "2026-10-08 14:30", false},
		{"Call at 9", "Call", "", "", "2026-10-08 09:00", "2026-10-08 10:00", false},
		{"Seminar 9-11am", "Seminar", "", "", "2026-10-08 09:00", "2026-10-08 11:00", false},
		{"Seminar 2-4pm", "Seminar", "", "", "2026-10-08 14:00", "2026-10-08 16:00", false},
		{"Review 14:00 to 15:30 next mon", "Review", "", "", "2026-10-12 14:00", "2026-10-12 15:30", false},
		{"Conference 12 nov all day #Travel", "Conference", "", "Travel", "2026-11-12 00:00", "2026-11-13 00:00", true},
		{"Write chapter 2h", "Write chapter", "", "", "2026-10-08 09:00", "2026-10-08 11:00", false},
		{"Room 12 meeting today", "Room 12 meeting", "", "", "2026-10-06 14:30", "2026-10-06 15:30", false},
		{"Deadline 3/1 all day", "Deadline", "", "", "2027-01-03 00:00", "2027-01-04 00:00", true},
		{"Borrel vrijdag 17:00 1h30", "Borrel", "", "", "2026-10-09 17:00", "2026-10-09 18:30", false},
		{"Trip 2026-12-20 all day", "Trip", "", "", "2026-12-20 00:00", "2026-12-21 00:00", true},
		{"Dinner in 3 days 19:00", "Dinner", "", "", "2026-10-09 19:00", "2026-10-09 20:00", false},
		{"Tuesday thing tue", "Tuesday thing", "", "", "2026-10-13 09:00", "2026-10-13 10:00", false},
	}
	for _, c := range cases {
		parsed, err := Parse(c.input, now)
		if err != nil {
			t.Errorf("%q: %v", c.input, err)
			continue
		}
		start, end, err := Event(parsed, selected, now)
		if err != nil {
			t.Errorf("%q: %v", c.input, err)
			continue
		}
		got := [6]string{parsed.Title, parsed.Location, parsed.Calendar, start.Format("2006-01-02 15:04"), end.Format("2006-01-02 15:04"), ""}
		want := [6]string{c.title, c.location, c.calendar, c.start, c.end, ""}
		if got != want || parsed.AllDay != c.allDay {
			t.Errorf("%q:\n got  %q allDay=%v\n want %q allDay=%v", c.input, got, parsed.AllDay, want, c.allDay)
		}
	}
	if _, err := Parse("Thing 15:00-14:00", now); err == nil {
		t.Error("an end before the start should fail")
	}
	if parsed, _ := Parse("tomorrow 10:00", now); parsed.Title != "" {
		t.Error("no title")
	} else if _, _, err := Event(parsed, now, now); err == nil {
		t.Error("an event needs a title")
	}
}

func TestMove(t *testing.T) {
	start := time.Date(2026, 10, 8, 13, 30, 0, 0, time.Local)
	end := start.Add(90 * time.Minute)
	cases := []struct{ input, start, end string }{
		{"fri", "2026-10-09 13:30", "2026-10-09 15:00"},
		{"16:00", "2026-10-08 16:00", "2026-10-08 17:30"},
		{"fri 10:00", "2026-10-09 10:00", "2026-10-09 11:30"},
		{"+1d", "2026-10-09 13:30", "2026-10-09 15:00"},
		{"-30m", "2026-10-08 13:00", "2026-10-08 14:30"},
		{"+1w", "2026-10-15 13:30", "2026-10-15 15:00"},
		{"14:00-15:00", "2026-10-08 14:00", "2026-10-08 15:00"},
		{"2h", "2026-10-08 13:30", "2026-10-08 15:30"},
		{"tomorrow 9am for 45m", "2026-10-07 09:00", "2026-10-07 09:45"},
		{"+1d", "2026-10-09 13:30", "2026-10-09 15:00"},
	}
	for _, c := range cases {
		parsed, err := Parse(c.input, now)
		if err != nil {
			t.Errorf("%q: %v", c.input, err)
			continue
		}
		newStart, newEnd, err := Move(parsed, start, end)
		if err != nil {
			t.Errorf("%q: %v", c.input, err)
			continue
		}
		if got := newStart.Format("2006-01-02 15:04") + " " + newEnd.Format("2006-01-02 15:04"); got != c.start+" "+c.end {
			t.Errorf("%q: got %s, want %s %s", c.input, got, c.start, c.end)
		}
	}
	for _, bad := range []string{"", "lunch"} {
		parsed, _ := Parse(bad, now)
		if _, _, err := Move(parsed, start, end); err == nil {
			t.Errorf("%q should not move", bad)
		}
	}
	if parsed, _ := Parse("next free", now); !parsed.Free {
		t.Error("next free")
	}
}

func TestMoveAcrossDaylightSaving(t *testing.T) {
	amsterdam, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Skip(err)
	}
	old := time.Local
	time.Local = amsterdam // Move works in the machine's zone
	t.Cleanup(func() { time.Local = old })
	start := time.Date(2026, 10, 23, 10, 0, 0, 0, amsterdam) // CEST
	parsed, _ := Parse("+3d", start)
	newStart, _, _ := Move(parsed, start, start.Add(time.Hour))
	if newStart.Hour() != 10 || newStart.Day() != 26 {
		t.Fatalf("+3d over the clock change = %s, want 10:00 on the 26th", newStart)
	}
}

func TestRepeats(t *testing.T) {
	selected := time.Date(2026, 10, 8, 0, 0, 0, 0, time.Local) // a Thursday
	cases := []struct{ input, title, start, rule, words string }{
		{"Gym every mon and wed 18:30 1h30", "Gym", "2026-10-12 18:30", "FREQ=WEEKLY;BYDAY=MO,WE", "every Monday and Wednesday"},
		{"Standup every weekday 9:15 15m", "Standup", "2026-10-08 09:15", "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR", "every weekday"},
		{"Review daily 17:00 until 20 oct", "Review", "2026-10-08 17:00", "FREQ=DAILY;UNTIL=20261020T215959Z", "every day until 20 Oct"},
		{"Lab meeting every 2 weeks fri 10:00", "Lab meeting", "2026-10-09 10:00", "FREQ=WEEKLY;INTERVAL=2;BYDAY=FR", "every 2 weeks on Friday"},
		{"Reading group monthly 12:00 10 times", "Reading group", "2026-10-08 12:00", "FREQ=MONTHLY;COUNT=10", "every month, 10 times"},
		{"Every word counts", "Every word counts", "2026-10-08 09:00", "", ""},
		{"Plan tomorrow 13:30 every mon", "Plan", "2026-10-12 13:30", "FREQ=WEEKLY;BYDAY=MO", "every Monday"},
	}
	for _, c := range cases {
		parsed, err := Parse(c.input, now)
		if err != nil {
			t.Errorf("%q: %v", c.input, err)
			continue
		}
		start, _, err := Event(parsed, selected, now)
		if err != nil {
			t.Errorf("%q: %v", c.input, err)
			continue
		}
		rule, words := "", ""
		if parsed.Repeat != nil {
			rule, words = parsed.Repeat.Rule(), parsed.Repeat.Describe()
		}
		if parsed.Title != c.title || start.Format("2006-01-02 15:04") != c.start || rule != c.rule || words != c.words {
			t.Errorf("%q: title %q start %s rule %q words %q", c.input, parsed.Title, start.Format("2006-01-02 15:04"), rule, words)
		}
	}
}

func TestRoles(t *testing.T) {
	cases := map[string]string{
		"Lunch with Ada tomorrow 12:30 1h @Atrium 2.04 #University": "title title title date time length location location calendar",
		"Gym every mon and wed 18:30-20:00":                         "title repeat repeat repeat repeat time",
		"Call next fri at 9":                                        "title filler date time time",
		"Tuesday thing tue":                                         "title title date",
		"Room 12 meeting all day":                                   "title title title allday allday",
	}
	for input, want := range cases {
		parsed, err := Parse(input, now)
		if err != nil {
			t.Errorf("%q: %v", input, err)
			continue
		}
		if got := strings.Join(parsed.Roles, " "); got != want {
			t.Errorf("%q:\n got  %s\n want %s", input, got, want)
		}
	}
}

// Forgiving input: shorthand, unfinished words, typos, spoken times and
// lengths. now is Tuesday 6 October 2026 14:10; the calendar's day is
// Thursday the 8th.
func TestForgiving(t *testing.T) {
	selected := time.Date(2026, 10, 8, 0, 0, 0, 0, time.Local)
	cases := []struct{ input, title, start, end, notes string }{
		{"Lunch tom 12:30", "Lunch", "2026-10-07 12:30", "13:30", "tom→tomorrow"},
		{"Lunch tmrw 12:30", "Lunch", "2026-10-07 12:30", "13:30", "tmrw→tomorrow"},
		{"Call tomo 2p", "Call", "2026-10-07 14:00", "15:00", "tomo→tomorrow 2p→14:00"},
		{"Call tommorow at 3", "Call", "2026-10-07 15:00", "16:00", "tommorow→tomorrow 3→15:00"},
		{"Seminar wensday 10:00", "Seminar", "2026-10-07 10:00", "11:00", "wensday→wednesday"},
		{"Seminar firday 10:00", "Seminar", "2026-10-09 10:00", "11:00", "firday→friday"},
		{"Review wedn 2.30pm", "Review", "2026-10-07 14:30", "15:30", "wedn→wednesday 2.30pm→14:30"},
		{"Borrel vrijdag 17u", "Borrel", "2026-10-09 17:00", "18:00", "17u→17:00"},
		{"Borrel vrijdag 17u30", "Borrel", "2026-10-09 17:30", "18:30", "17u30→17:30"},
		{"Talk 14.30 thu", "Talk", "2026-10-08 14:30", "15:30", "14.30→14:30"},
		{"Plan tomorrow afternoon", "Plan", "2026-10-07 14:00", "15:00", ""},
		{"Plan tomorrow afternoon at 3", "Plan", "2026-10-07 15:00", "16:00", "3→15:00"},
		{"Dinner tonight", "Dinner", "2026-10-06 19:00", "20:00", ""},
		{"Drinks this evening", "Drinks", "2026-10-06 19:00", "20:00", ""},
		{"Sync next week", "Sync", "2026-10-12 09:00", "10:00", ""},
		{"Sync fri next week 10:00", "Sync", "2026-10-16 10:00", "11:00", ""},
		{"Hike this weekend", "Hike", "2026-10-10 09:00", "10:00", ""},
		{"Check in 2 weeks", "Check", "2026-10-20 09:00", "10:00", ""},
		{"Check in a month", "Check", "2026-11-06 09:00", "10:00", ""},
		{"Deadline the 12th", "Deadline", "2026-10-12 09:00", "10:00", ""},
		{"Rent 1st", "Rent", "2026-11-01 09:00", "10:00", ""},
		{"Walk 10:00 90 minutes", "Walk", "2026-10-08 10:00", "11:30", ""},
		{"Walk 10:00 an hour", "Walk", "2026-10-08 10:00", "11:00", ""},
		{"Walk 10:00 half an hour", "Walk", "2026-10-08 10:00", "10:30", ""},
		{"Coffee with Tom tom aftrnoon at 3 for half an hour", "Coffee with Tom", "2026-10-07 15:00", "15:30", "tom→tomorrow aftrnoon→afternoon 3→15:00"},
		{"Walk 10:00 for 90 minutes", "Walk", "2026-10-08 10:00", "11:30", ""},
		{"Talk 2-4", "Talk", "2026-10-08 14:00", "16:00", ""},
		{"Talk 2p-4p", "Talk", "2026-10-08 14:00", "16:00", ""},
		{"Coffee till 11 at 10", "Coffee", "2026-10-08 10:00", "11:00", ""},
		// Names stay names: only lower-case words are rewritten.
		{"Meeting with Tom 10:00", "Meeting with Tom", "2026-10-08 10:00", "11:00", ""},
		{"Tennis match sat 10:00", "Tennis match", "2026-10-10 10:00", "11:00", ""},
		{`Call "tom" 10:00`, "Call tom", "2026-10-08 10:00", "11:00", ""},
		{"Room 12 meeting 09:00", "Room 12 meeting", "2026-10-08 09:00", "10:00", ""},
		{"Work 2h at 9", "Work", "2026-10-08 09:00", "11:00", ""},
	}
	for _, c := range cases {
		parsed, err := Parse(c.input, now)
		if err != nil {
			t.Errorf("%q: %v", c.input, err)
			continue
		}
		start, end, err := Event(parsed, selected, now)
		if err != nil {
			t.Errorf("%q: %v", c.input, err)
			continue
		}
		var notes []string
		for _, note := range parsed.Notes {
			notes = append(notes, note.Word+"→"+note.Meaning)
		}
		got := [4]string{parsed.Title, start.Format("2006-01-02 15:04"), end.Format("15:04"), strings.Join(notes, " ")}
		want := [4]string{c.title, c.start, c.end, c.notes}
		if got != want {
			t.Errorf("%q:\n got  %q\n want %q", c.input, got, want)
		}
	}
}

func TestEditDistance(t *testing.T) {
	for pair, want := range map[[2]string]int{{"tommorow", "tomorrow"}: 2, {"wensday", "wednesday"}: 2, {"firday", "friday"}: 1, {"match", "march"}: 1} {
		if got := editDistance(pair[0], pair[1]); got != want {
			t.Errorf("%v = %d, want %d", pair, got, want)
		}
	}
}

func useZone(t *testing.T, name string) *time.Location {
	t.Helper()
	location, err := time.LoadLocation(name)
	if err != nil {
		t.Skip(err)
	}
	old := time.Local
	time.Local = location
	t.Cleanup(func() { time.Local = old })
	return location
}

// Google and Exchange give UTC times; the rebook works on the wall clock of
// the zone the machine is in, wherever that is.
func TestMoveUsesTheMachineZoneForUTCStarts(t *testing.T) {
	cases := []struct {
		zone         string
		utcStart     time.Time // the same instant, written in UTC
		input        string
		wantStart    string
		wantEnd      string
		wantZoneName string
	}{
		// 18:30 CEST: "16:00" is 16:00 in Amsterdam, not 16:00 UTC (18:00).
		{"Europe/Amsterdam", time.Date(2026, 10, 8, 16, 30, 0, 0, time.UTC), "16:00", "2026-10-08 16:00", "2026-10-08 17:00", "CEST"},
		// 10:00 CEST on a Friday is 08:00 UTC: "mon" keeps 10:00, not 08:00.
		{"Europe/Amsterdam", time.Date(2026, 10, 9, 8, 0, 0, 0, time.UTC), "mon", "2026-10-12 10:00", "2026-10-12 11:00", "CEST"},
		// 00:30 CEST on the 9th is still the 8th in UTC.
		{"Europe/Amsterdam", time.Date(2026, 10, 8, 22, 30, 0, 0, time.UTC), "+1d", "2026-10-10 00:30", "2026-10-10 01:30", "CEST"},
		// The same instants in Korea: 01:30 KST on the 9th.
		{"Asia/Seoul", time.Date(2026, 10, 8, 16, 30, 0, 0, time.UTC), "16:00", "2026-10-09 16:00", "2026-10-09 17:00", "KST"},
		{"Asia/Seoul", time.Date(2026, 10, 9, 1, 0, 0, 0, time.UTC), "mon", "2026-10-12 10:00", "2026-10-12 11:00", "KST"},
	}
	for _, c := range cases {
		location := useZone(t, c.zone)
		now := time.Date(2026, 10, 6, 14, 10, 0, 0, location)
		parsed, err := Parse(c.input, now)
		if err != nil {
			t.Errorf("%s %q: %v", c.zone, c.input, err)
			continue
		}
		newStart, newEnd, err := Move(parsed, c.utcStart, c.utcStart.Add(time.Hour))
		if err != nil {
			t.Errorf("%s %q: %v", c.zone, c.input, err)
			continue
		}
		name, _ := newStart.Zone()
		if got := newStart.Format("2006-01-02 15:04") + " " + newEnd.Format("2006-01-02 15:04"); got != c.wantStart+" "+c.wantEnd || name != c.wantZoneName {
			t.Errorf("%s %q on %s: got %s (%s), want %s %s (%s)", c.zone, c.input, c.utcStart, got, name, c.wantStart, c.wantEnd, c.wantZoneName)
		}
	}
}

func TestMoveAllDay(t *testing.T) {
	amsterdam := useZone(t, "Europe/Amsterdam")
	now := time.Date(2026, 10, 6, 14, 10, 0, 0, amsterdam)
	// Three days over the clock change of 25 October: 73 hours long.
	start := time.Date(2026, 10, 24, 0, 0, 0, 0, amsterdam)
	end := time.Date(2026, 10, 27, 0, 0, 0, 0, amsterdam)
	cases := []struct{ input, start, end string }{
		{"mon", "2026-10-12 00:00", "2026-10-15 00:00"},
		{"+1d", "2026-10-25 00:00", "2026-10-28 00:00"},
		{"2026-10-12 for 2 days", "2026-10-12 00:00", "2026-10-14 00:00"},
		{"+1w", "2026-10-31 00:00", "2026-11-03 00:00"},
	}
	for _, c := range cases {
		parsed, err := Parse(c.input, now)
		if err != nil {
			t.Errorf("%q: %v", c.input, err)
			continue
		}
		newStart, newEnd, err := MoveAllDay(parsed, start, end)
		if err != nil {
			t.Errorf("%q: %v", c.input, err)
			continue
		}
		if got := newStart.Format("2006-01-02 15:04") + " " + newEnd.Format("2006-01-02 15:04"); got != c.start+" "+c.end {
			t.Errorf("%q: got %s, want %s %s", c.input, got, c.start, c.end)
		}
	}
	for _, bad := range []string{"10:00", "+30m", "lunch", ""} {
		parsed, _ := Parse(bad, now)
		if _, _, err := MoveAllDay(parsed, start, end); err == nil {
			t.Errorf("%q should not move an all-day event", bad)
		}
	}
}

func TestDottedNumbersAreTimesOrTitlesNotDates(t *testing.T) {
	selected := time.Date(2026, 10, 8, 0, 0, 0, 0, time.Local)
	cases := []struct{ input, title, start, end string }{
		{"Sprint 2.1", "Sprint 2.1", "2026-10-08 09:00", "10:00"},
		{"Lab 2.04 meeting", "Lab 2.04 meeting", "2026-10-08 09:00", "10:00"},
		{"Call 14.05", "Call", "2026-10-08 14:05", "15:05"},
		{"Standup 9.10", "Standup", "2026-10-08 09:10", "10:10"},
		{"Talk 14.30 thu", "Talk", "2026-10-08 14:30", "15:30"},
		{"Tax 12.10.2026 all day", "Tax", "2026-10-12 00:00", "2026-10-13 00:00"},
		{"Tax 12.10.26 all day", "Tax", "2026-10-12 00:00", "2026-10-13 00:00"},
		{"Tax 12/10 all day", "Tax", "2026-10-12 00:00", "2026-10-13 00:00"},
	}
	for _, c := range cases {
		parsed, err := Parse(c.input, now)
		if err != nil {
			t.Errorf("%q: %v", c.input, err)
			continue
		}
		start, end, err := Event(parsed, selected, now)
		if err != nil {
			t.Errorf("%q: %v", c.input, err)
			continue
		}
		endText := end.Format("15:04")
		if parsed.AllDay {
			endText = end.Format("2006-01-02 15:04")
		}
		if got := [3]string{parsed.Title, start.Format("2006-01-02 15:04"), endText}; got != [3]string{c.title, c.start, c.end} {
			t.Errorf("%q: got %q, want %q", c.input, got, [3]string{c.title, c.start, c.end})
		}
	}
}

func TestInvalidDatesAreErrors(t *testing.T) {
	for _, input := range []string{"Dentist 31 apr", "Dentist 30 feb 10:00", "Trip 2026-02-30 all day", "Trip 2026-13-01", "Dentist apr 31", "Dentist 31/04", "Dentist 31.04.2026", "Review daily until 31 apr"} {
		if parsed, err := Parse(input, now); err == nil {
			t.Errorf("%q should be an error, got title %q", input, parsed.Title)
		}
	}
	for _, input := range []string{"Dentist 30 apr", "Dentist 29 feb", "Meeting 30/60", "Room 40 march"} {
		if _, err := Parse(input, now); err != nil {
			t.Errorf("%q: %v", input, err)
		}
	}
	// 29 February looks on to the next leap year.
	parsed, _ := Parse("Leap 29 feb all day", now)
	if parsed.Date.Format("2006-01-02") != "2028-02-29" {
		t.Errorf("29 feb = %s, want 2028-02-29", parsed.Date.Format("2006-01-02"))
	}
}

func TestKeywordsNeedLowerCase(t *testing.T) {
	selected := time.Date(2026, 10, 8, 0, 0, 0, 0, time.Local)
	cases := []struct {
		input, title string
		repeat       bool
		free         bool
		start        string
	}{
		{"Free lunch 12:00", "Free lunch", false, false, "2026-10-08 12:00"},
		{"free 12:00 Lunch", "Lunch", false, true, "2026-10-08 12:00"},
		{"Weekly sync 10:00", "Weekly sync", false, false, "2026-10-08 10:00"},
		{"sync weekly 10:00", "sync", true, false, "2026-10-08 10:00"},
		{"Daily standup 9:15", "Daily standup", false, false, "2026-10-08 09:15"},
		{"Monthly report", "Monthly report", false, false, "2026-10-08 09:00"},
		{"Biweekly check", "Biweekly check", false, false, "2026-10-08 09:00"},
		{"Weekdays plan", "Weekdays plan", false, false, "2026-10-08 09:00"},
		{"Plan Next Week", "Plan Next Week", false, false, "2026-10-08 09:00"},
		{"Plan next Week", "Plan next Week", false, false, "2026-10-08 09:00"},
		{"Plan next week", "Plan", false, false, "2026-10-12 09:00"},
		{"Weekend trip", "Weekend trip", false, false, "2026-10-08 09:00"},
		{"trip weekend", "trip", false, false, "2026-10-10 09:00"},
		{"Noon service", "Noon service", false, false, "2026-10-08 09:00"},
		{"Midnight run", "Midnight run", false, false, "2026-10-08 09:00"},
		{"Midday walk", "Midday walk", false, false, "2026-10-08 09:00"},
		{"lunch noon", "lunch", false, false, "2026-10-08 12:00"},
	}
	for _, c := range cases {
		parsed, err := Parse(c.input, now)
		if err != nil {
			t.Errorf("%q: %v", c.input, err)
			continue
		}
		start, _, err := Event(parsed, selected, now)
		if err != nil {
			t.Errorf("%q: %v", c.input, err)
			continue
		}
		if got := [4]string{parsed.Title, fmt.Sprint(parsed.Repeat != nil), fmt.Sprint(parsed.Free), start.Format("2006-01-02 15:04")}; got != [4]string{c.title, fmt.Sprint(c.repeat), fmt.Sprint(c.free), c.start} {
			t.Errorf("%q: got %q, want %q %v %v %s", c.input, got, c.title, c.repeat, c.free, c.start)
		}
	}
}

func TestOvernightRanges(t *testing.T) {
	selected := time.Date(2026, 10, 8, 0, 0, 0, 0, time.Local)
	cases := []struct{ input, start, end string }{
		{"Party 22:00-02:00", "2026-10-08 22:00", "2026-10-09 02:00"},
		{"Party 22:00-00:00", "2026-10-08 22:00", "2026-10-09 00:00"},
		{"Party 20:00 to 1:30", "2026-10-08 20:00", "2026-10-09 01:30"},
		{"Party 11-1", "2026-10-08 11:00", "2026-10-08 13:00"},
	}
	for _, c := range cases {
		parsed, err := Parse(c.input, now)
		if err != nil {
			t.Errorf("%q: %v", c.input, err)
			continue
		}
		start, end, err := Event(parsed, selected, now)
		if err != nil {
			t.Errorf("%q: %v", c.input, err)
			continue
		}
		if got := start.Format("2006-01-02 15:04") + " " + end.Format("2006-01-02 15:04"); got != c.start+" "+c.end {
			t.Errorf("%q: got %s, want %s %s", c.input, got, c.start, c.end)
		}
	}
	// Rebooking with an overnight range.
	start := time.Date(2026, 10, 8, 13, 30, 0, 0, time.Local)
	parsed, _ := Parse("22:00-02:00", now)
	newStart, newEnd, err := Move(parsed, start, start.Add(time.Hour))
	if err != nil || newStart.Format("2006-01-02 15:04") != "2026-10-08 22:00" || newEnd.Format("2006-01-02 15:04") != "2026-10-09 02:00" {
		t.Errorf("Move overnight = %s – %s, %v", newStart, newEnd, err)
	}
	if _, err := Parse("Thing 15:00-14:00", now); err == nil {
		t.Error("an afternoon end before the start is still an error")
	}
}

func TestMultiDayAllDay(t *testing.T) {
	selected := time.Date(2026, 10, 8, 0, 0, 0, 0, time.Local)
	for input, wantEnd := range map[string]string{
		"Trip 2026-10-12 all day for 3 days": "2026-10-15",
		"Trip all day for 1 day":             "2026-10-09",
		"Trip for 2 days all day fri":        "2026-10-11",
	} {
		parsed, err := Parse(input, now)
		if err != nil {
			t.Errorf("%q: %v", input, err)
			continue
		}
		_, end, _ := Event(parsed, selected, now)
		if !parsed.AllDay || end.Format("2006-01-02") != wantEnd {
			t.Errorf("%q: allDay %v end %s, want %s", input, parsed.AllDay, end.Format("2006-01-02"), wantEnd)
		}
	}
}

func TestEventAfterHalfPastElevenBooksTomorrowMorning(t *testing.T) {
	late := time.Date(2026, 10, 8, 23, 40, 0, 0, time.Local)
	today := time.Date(2026, 10, 8, 0, 0, 0, 0, time.Local)
	parsed, _ := Parse("Review", late)
	start, end, err := Event(parsed, today, late)
	if err != nil || start.Format("2006-01-02 15:04") != "2026-10-09 09:00" || end.Format("15:04") != "10:00" {
		t.Errorf("late default = %s – %s, %v; want 09:00 tomorrow", start, end, err)
	}
	early := time.Date(2026, 10, 8, 23, 10, 0, 0, time.Local)
	start, _, _ = Event(parsed, today, early)
	if start.Format("2006-01-02 15:04") != "2026-10-08 23:30" {
		t.Errorf("23:10 default = %s, want 23:30 today", start)
	}
	parsed, _ = Parse("Review 23:45", late)
	start, _, _ = Event(parsed, today, late)
	if start.Format("2006-01-02 15:04") != "2026-10-08 23:45" {
		t.Errorf("an explicit time stays today, got %s", start)
	}
}

func TestMoveKeepsTheWallClockOnAClockChangeDay(t *testing.T) {
	zone := amsterdam()
	saved := time.Local
	time.Local = zone
	defer func() { time.Local = saved }()
	// 29 March 2026: clocks go forward at 02:00. A 15:00 event that day is
	// 14 h after midnight; moved a day on it must still start at 15:00.
	start := time.Date(2026, 3, 29, 15, 0, 0, 0, zone)
	for _, phrase := range []string{"+1d", "tomorrow"} {
		parsed, _ := Parse(phrase, time.Date(2026, 3, 29, 9, 0, 0, 0, zone))
		moved, _, err := Move(parsed, start, start.Add(time.Hour))
		if err != nil || moved.Hour() != 15 || moved.Day() != 30 {
			t.Errorf("%s: %s %v, want 15:00 on the 30th", phrase, moved, err)
		}
	}
}
