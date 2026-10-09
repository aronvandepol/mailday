package calendar

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExpandRecurringEventWithExdateAndMovedOccurrence(t *testing.T) {
	location, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatal(err)
	}
	oldLocal := time.Local
	time.Local = location
	t.Cleanup(func() { time.Local = oldLocal })

	source := Source{UID: "calendar-1", Name: "Work", Provider: "Google"}
	master := `BEGIN:VEVENT
UID:weekly-1
SUMMARY:Weekly meeting
DTSTART;TZID=Europe/Amsterdam:20260824T100000
DTEND;TZID=Europe/Amsterdam:20260824T110000
RRULE:FREQ=WEEKLY;COUNT=4
EXDATE;TZID=Europe/Amsterdam:20260831T100000
END:VEVENT`
	override := `BEGIN:VEVENT
UID:weekly-1
RECURRENCE-ID;TZID=Europe/Amsterdam:20260907T100000
SUMMARY:Moved meeting
DTSTART;TZID=Europe/Amsterdam:20260907T140000
DTEND;TZID=Europe/Amsterdam:20260907T150000
END:VEVENT`
	from := time.Date(2026, 8, 24, 0, 0, 0, 0, location)
	to := time.Date(2026, 9, 20, 0, 0, 0, 0, location)
	events, warnings := expandObjects([]eventObject{{Raw: master, Source: source}, {Raw: override, Source: source}}, from, to)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	if got, want := len(events), 3; got != want {
		t.Fatalf("event count = %d, want %d: %+v", got, want, events)
	}
	if got, want := events[1].Summary, "Moved meeting"; got != want {
		t.Fatalf("second event = %q, want %q", got, want)
	}
	if got, want := events[1].Start.Hour(), 14; got != want {
		t.Fatalf("moved event hour = %d, want %d", got, want)
	}
}

func TestAllDayEventUsesExclusiveEnd(t *testing.T) {
	source := Source{UID: "calendar-1", Name: "Personal"}
	raw := `BEGIN:VEVENT
UID:day-1
SUMMARY:Conference
DTSTART;VALUE=DATE:20260825
DTEND;VALUE=DATE:20260827
END:VEVENT`
	from := time.Date(2026, 8, 25, 0, 0, 0, 0, time.Local)
	to := from.Add(7 * 24 * time.Hour)
	events, warnings := expandObjects([]eventObject{{Raw: raw, Source: source}}, from, to)
	if len(warnings) != 0 || len(events) != 1 {
		t.Fatalf("events = %+v, warnings = %v", events, warnings)
	}
	if !events[0].AllDay || events[0].End.Sub(events[0].Start) != 48*time.Hour {
		t.Fatalf("all-day event = %+v", events[0])
	}
}

func TestTerminalControlsAreRemovedFromCalendarText(t *testing.T) {
	source := Source{UID: "calendar-1", Name: "Personal"}
	raw := "BEGIN:VEVENT\nUID:safe\nSUMMARY:hello\\, world\x1b[31m\nDTSTART:20260825T100000Z\nDTEND:20260825T110000Z\nEND:VEVENT"
	from := time.Date(2026, 8, 25, 0, 0, 0, 0, time.UTC)
	events, _ := expandObjects([]eventObject{{Raw: raw, Source: source}}, from, from.Add(24*time.Hour))
	if got, want := events[0].Summary, "hello, world"; got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
}

func TestSameEventFromEvolutionAndICSShowsOnceAsICS(t *testing.T) {
	raw := `BEGIN:VEVENT
UID:launch-1@google.com
SUMMARY:Grants launch
DTSTART:20261001T171500Z
DTEND:20261001T193000Z
END:VEVENT`
	evolution := Source{UID: "evo-work", Name: "sam.devries@gmail.example", Provider: "Google"}
	ics := Source{UID: "/ics/Work.ics", Name: "Work", Provider: "ICS"}
	from := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	to := from.Add(7 * 24 * time.Hour)
	events, warnings := expandObjects([]eventObject{{Raw: raw, Source: evolution}, {Raw: raw, Source: ics}}, from, to)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	if got, want := len(events), 1; got != want {
		t.Fatalf("event count = %d, want %d: %+v", got, want, events)
	}
	if got, want := events[0].Provider, "ICS"; got != want {
		t.Fatalf("kept provider = %q, want %q", got, want)
	}
}

func TestMaildayProperties(t *testing.T) {
	dir := t.TempDir()
	data := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nUID:a\r\nDTSTART:20261020T100000Z\r\nDTEND:20261020T110000Z\r\nSUMMARY:Mine\r\nX-MAILDAY-ID:evt1\r\nX-MAILDAY-EDITABLE:TRUE\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nUID:b\r\nDTSTART:20261020T120000Z\r\nDTEND:20261020T130000Z\r\nSUMMARY:Invite\r\nX-MAILDAY-ID:evt2\r\nX-MAILDAY-EDITABLE:FALSE\r\nX-MAILDAY-ORGANIZER:Anouk\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	os.WriteFile(filepath.Join(dir, "Work.ics"), []byte(data), 0o600)
	store := &Store{ICSRoot: dir}
	result, err := store.Load(context.Background(), time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC))
	if err != nil || len(result.Events) != 2 {
		t.Fatalf("events %v err %v", result.Events, err)
	}
	mine, invite := result.Events[0], result.Events[1]
	if mine.RemoteID != "evt1" || !mine.Editable || invite.Editable || invite.Organizer != "Anouk" || invite.RemoteID != "evt2" {
		t.Fatalf("mine %+v invite %+v", mine, invite)
	}
}

func useAmsterdam(t *testing.T) *time.Location {
	t.Helper()
	location, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Skip(err)
	}
	old := time.Local
	time.Local = location
	t.Cleanup(func() { time.Local = old })
	return location
}

func TestTimedEventsAreLocal(t *testing.T) {
	amsterdam := useAmsterdam(t)
	raw := "BEGIN:VEVENT\nUID:utc-1\nSUMMARY:Seminar\nDTSTART:20261008T163000Z\nDTEND:20261008T173000Z\nEND:VEVENT"
	from := time.Date(2026, 10, 8, 0, 0, 0, 0, amsterdam)
	events, _ := expandObjects([]eventObject{{Raw: raw, Source: Source{UID: "a", Name: "Work"}}}, from, from.Add(24*time.Hour))
	if len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
	event := events[0]
	if event.Start.Location() != time.Local || event.End.Location() != time.Local {
		t.Fatalf("locations = %v, %v; want local", event.Start.Location(), event.End.Location())
	}
	if got := event.Start.Format("15:04"); got != "18:30" {
		t.Fatalf("start = %s, want 18:30 CEST", got)
	}
	if got := event.Start.Format(time.RFC3339); got != "2026-10-08T18:30:00+02:00" {
		t.Fatalf("start = %s", got)
	}
}

func TestRecurringAllDayEndsOnMidnightAcrossDaylightSaving(t *testing.T) {
	amsterdam := useAmsterdam(t)
	raw := "BEGIN:VEVENT\nUID:day-1\nSUMMARY:Retreat\nDTSTART;VALUE=DATE:20260327\nDTEND;VALUE=DATE:20260329\nRRULE:FREQ=WEEKLY;COUNT=3\nEND:VEVENT"
	from := time.Date(2026, 3, 20, 0, 0, 0, 0, amsterdam)
	events, warnings := expandObjects([]eventObject{{Raw: raw, Source: Source{UID: "a", Name: "Work"}}}, from, from.AddDate(0, 0, 30))
	if len(warnings) != 0 || len(events) != 3 {
		t.Fatalf("events = %+v, warnings = %v", events, warnings)
	}
	for _, event := range events {
		if event.Start.Hour() != 0 || event.End.Hour() != 0 || event.End.Day() != event.Start.AddDate(0, 0, 2).Day() {
			t.Errorf("occurrence %s – %s should run midnight to midnight, two days", event.Start, event.End)
		}
	}
	// A daily one-day event: the 29 March occurrence is 23 hours long.
	daily := "BEGIN:VEVENT\nUID:day-2\nSUMMARY:Fast\nDTSTART;VALUE=DATE:20260328\nRRULE:FREQ=DAILY;COUNT=3\nEND:VEVENT"
	events, _ = expandObjects([]eventObject{{Raw: daily, Source: Source{UID: "a", Name: "Work"}}}, from, from.AddDate(0, 0, 30))
	if len(events) != 3 {
		t.Fatalf("events = %+v", events)
	}
	if end := events[1].End; end.Day() != 30 || end.Hour() != 0 {
		t.Fatalf("29 March all-day ends %s, want 30 March 00:00", end)
	}
}

func TestExpandIsDeterministicWhenSourcesRepeatAnEvent(t *testing.T) {
	raw := "BEGIN:VEVENT\nUID:shared@google.com\nSUMMARY:Shared\nDTSTART:20261008T163000Z\nDTEND:20261008T173000Z\nEND:VEVENT"
	first := Source{UID: "/ics/A.ics", Name: "A", Provider: "ICS"}
	second := Source{UID: "/ics/B.ics", Name: "B", Provider: "ICS"}
	from := time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC)
	for round := 0; round < 40; round++ {
		events, _ := expandObjects([]eventObject{{Raw: raw, Source: second}, {Raw: raw, Source: first}}, from, from.AddDate(0, 0, 3))
		if len(events) != 1 || events[0].Source != "A" {
			t.Fatalf("round %d: kept %+v, want the copy from A every time", round, events)
		}
	}
}

func TestAlarmPropertiesAreNotTheEvents(t *testing.T) {
	raw := "BEGIN:VEVENT\nUID:alarm-1\nSUMMARY:Dentist\nDTSTART:20261008T080000Z\nDTEND:20261008T090000Z\nBEGIN:VALARM\nTRIGGER:-PT15M\nACTION:DISPLAY\nDESCRIPTION:Reminder\nEND:VALARM\nEND:VEVENT"
	from := time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)
	events, _ := expandObjects([]eventObject{{Raw: raw, Source: Source{UID: "a", Name: "Work"}}}, from, from.Add(24*time.Hour))
	if len(events) != 1 || events[0].Description != "" {
		t.Fatalf("description = %q, want none: %+v", events[0].Description, events)
	}
	// A whole VCALENDAR object, as some caches store, must not take its
	// VTIMEZONE's DTSTART for the event's.
	wrapped := "BEGIN:VCALENDAR\nBEGIN:VTIMEZONE\nTZID:Europe/Amsterdam\nBEGIN:STANDARD\nDTSTART:19701025T030000\nEND:STANDARD\nEND:VTIMEZONE\n" + raw + "\nEND:VCALENDAR"
	events, _ = expandObjects([]eventObject{{Raw: wrapped, Source: Source{UID: "a", Name: "Work"}}}, from, from.Add(24*time.Hour))
	if len(events) != 1 || events[0].Start.UTC().Hour() != 8 {
		t.Fatalf("events = %+v", events)
	}
}

func TestZoneNames(t *testing.T) {
	cases := map[string]string{
		"Europe/Amsterdam": "Europe/Amsterdam",
		"/freeassociation.sourceforge.net/Tzfile/Europe/Amsterdam": "Europe/Amsterdam",
		"/freeassociation.sourceforge.net/Tzfile/Asia/Tokyo":       "Asia/Tokyo",
		"W. Europe Standard Time":                                  "Europe/Amsterdam",
		"Central Europe Standard Time":                             "Europe/Budapest",
		"Romance Standard Time":                                    "Europe/Paris",
		"Central European Standard Time":                           "Europe/Warsaw",
		"GMT Standard Time":                                        "Europe/London",
		"Korea Standard Time":                                      "Asia/Seoul",
		"Tokyo Standard Time":                                      "Asia/Tokyo",
		"China Standard Time":                                      "Asia/Shanghai",
		"India Standard Time":                                      "Asia/Kolkata",
		"Eastern Standard Time":                                    "America/New_York",
		"Central Standard Time":                                    "America/Chicago",
		"Pacific Standard Time":                                    "America/Los_Angeles",
		"UTC":                                                      "UTC",
	}
	for name, want := range cases {
		location, known := lookupLocation(name)
		if !known || location.String() != want {
			t.Errorf("%q = %v (known %v), want %s", name, location, known, want)
		}
	}
	if _, known := lookupLocation("Mars Standard Time"); known {
		t.Error("an unknown zone should not be reported as known")
	}
}

func TestUnknownZoneWarnsOnceAndReadsAsLocal(t *testing.T) {
	amsterdam := useAmsterdam(t)
	raw := func(uid string) string {
		return "BEGIN:VEVENT\nUID:" + uid + "\nSUMMARY:Call\nDTSTART;TZID=Mars Standard Time:20261008T100000\nDTEND;TZID=Mars Standard Time:20261008T110000\nEND:VEVENT"
	}
	source := Source{UID: "a", Name: "Work"}
	from := time.Date(2026, 10, 8, 0, 0, 0, 0, amsterdam)
	events, warnings := expandObjects([]eventObject{{Raw: raw("one"), Source: source}, {Raw: raw("two"), Source: source}}, from, from.Add(24*time.Hour))
	if len(events) != 2 || events[0].Start.Hour() != 10 {
		t.Fatalf("events = %+v", events)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "Mars Standard Time") {
		t.Fatalf("warnings = %v, want one naming the zone", warnings)
	}
}
