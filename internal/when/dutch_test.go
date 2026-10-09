package when

import (
	"strings"
	"testing"
	"time"
)

// Dutch phrases, read relative to now (Tuesday 6 October 2026, 14:10) with
// Thursday the 8th selected in the calendar.
func TestDutchPhrases(t *testing.T) {
	selected := time.Date(2026, 10, 8, 0, 0, 0, 0, time.Local)
	cases := []struct {
		input, title, start, end, notes string
		allDay                          bool
	}{
		// volgende, komende, deze
		{"Overleg volgende week", "Overleg", "2026-10-12 09:00", "10:00", "", false},
		{"Overleg volgende vrijdag 10:00", "Overleg", "2026-10-09 10:00", "11:00", "", false},
		{"Overleg komende vrijdag 10:00", "Overleg", "2026-10-09 10:00", "11:00", "", false},
		{"Borrel vrijdag volgende week 17:00", "Borrel", "2026-10-16 17:00", "18:00", "", false},
		{"Lunch deze vrijdag 12:00", "Lunch", "2026-10-09 12:00", "13:00", "", false},
		{"Wandelen dit weekend", "Wandelen", "2026-10-10 09:00", "10:00", "", false},
		{"Rit volgende weekend", "Rit", "2026-10-17 09:00", "10:00", "", false},
		// over N dagen/weken/maanden
		{"Check over 3 dagen", "Check", "2026-10-09 09:00", "10:00", "", false},
		{"Check over 2 weken", "Check", "2026-10-20 09:00", "10:00", "", false},
		{"Check over twee weken", "Check", "2026-10-20 09:00", "10:00", "", false},
		{"Check over een week", "Check", "2026-10-13 09:00", "10:00", "", false},
		{"Check over één week", "Check", "2026-10-13 09:00", "10:00", "", false},
		{"Check over een maand", "Check", "2026-11-06 09:00", "10:00", "", false},
		{"Check over 2 maanden", "Check", "2026-12-06 09:00", "10:00", "", false},
		{"Check in een week", "Check", "2026-10-13 09:00", "10:00", "", false},
		// The word "over" in a title is not a date, nor "overmorgen".
		{"Gesprek over budget 10:00", "Gesprek over budget", "2026-10-08 10:00", "11:00", "", false},
		{"Over 3 dagen Check", "Over 3 dagen Check", "2026-10-08 09:00", "10:00", "", false},
		{"Check overmorgen", "Check", "2026-10-08 09:00", "10:00", "", false},
		{"Volgende week plannen", "Volgende week plannen", "2026-10-08 09:00", "10:00", "", false},
		// Two-letter days
		{"Borrel vr 17:00", "Borrel", "2026-10-09 17:00", "18:00", "vr→vrijdag", false},
		{"Sync ma 10:00", "Sync", "2026-10-12 10:00", "11:00", "ma→maandag", false},
		{"Sync di 10:00", "Sync", "2026-10-13 10:00", "11:00", "di→dinsdag", false},
		{"Sync wo 10:00", "Sync", "2026-10-07 10:00", "11:00", "wo→woensdag", false},
		{"Sync do 14:00", "Sync", "2026-10-08 14:00", "15:00", "do→donderdag", false},
		{"Sync za 10:00", "Sync", "2026-10-10 10:00", "11:00", "za→zaterdag", false},
		{"Sync 10:00 zo", "Sync", "2026-10-11 10:00", "11:00", "zo→zondag", false},
		{"Sync volgende ma 10:00", "Sync", "2026-10-12 10:00", "11:00", "ma→maandag", false},
		{"Sync vr, 10:00", "Sync", "2026-10-09 10:00", "11:00", "vr→vrijdag", false},
		// do, za and zo are ordinary words unless they sit beside a date or time
		{"Do not forget", "Do not forget", "2026-10-08 09:00", "10:00", "", false},
		{"do the dishes", "do the dishes", "2026-10-08 09:00", "10:00", "", false},
		{"Things to do 14:00", "Things to do", "2026-10-08 14:00", "15:00", "", false},
		{"zo snel mogelijk bellen", "zo snel mogelijk bellen", "2026-10-08 09:00", "10:00", "", false},
		{"Bel za", "Bel za", "2026-10-08 09:00", "10:00", "", false},
		{"Zo 10:00", "Zo", "2026-10-08 10:00", "11:00", "", false},
		{"Ma 10:00", "Ma", "2026-10-08 10:00", "11:00", "", false},
		{`Bel "vr" 10:00`, "Bel vr", "2026-10-08 10:00", "11:00", "", false},
		// mrt
		{"Deadline 5 mrt all day", "Deadline", "2027-03-05 00:00", "00:00", "", true},
		{"Deadline mrt 5 all day", "Deadline", "2027-03-05 00:00", "00:00", "", true},
		// half, kwart over, kwart voor
		{"Overleg morgen half twee", "Overleg", "2026-10-07 13:30", "14:30", "half twee→13:30", false},
		{"Overleg morgen half negen", "Overleg", "2026-10-07 08:30", "09:30", "half negen→08:30", false},
		{"Overleg morgen half twaalf", "Overleg", "2026-10-07 11:30", "12:30", "half twaalf→11:30", false},
		{"Overleg morgen half een", "Overleg", "2026-10-07 12:30", "13:30", "half een→12:30", false},
		{"Overleg morgen half 3", "Overleg", "2026-10-07 14:30", "15:30", "half 3→14:30", false},
		{"Overleg om half twee", "Overleg", "2026-10-08 13:30", "14:30", "half twee→13:30", false},
		{"Overleg morgen kwart over drie", "Overleg", "2026-10-07 15:15", "16:15", "kwart over drie→15:15", false},
		{"Overleg morgen kwart voor vier", "Overleg", "2026-10-07 15:45", "16:45", "kwart voor vier→15:45", false},
		{"Overleg om kwart voor vier", "Overleg", "2026-10-08 15:45", "16:45", "kwart voor vier→15:45", false},
		{"Overleg morgen kwart voor negen", "Overleg", "2026-10-07 08:45", "09:45", "kwart voor negen→08:45", false},
		{"Overleg morgen kwart over twaalf", "Overleg", "2026-10-07 12:15", "13:15", "kwart over twaalf→12:15", false},
		{"Overleg morgen kwart voor een", "Overleg", "2026-10-07 12:45", "13:45", "kwart voor een→12:45", false},
		{"Etentje vanavond half negen", "Etentje", "2026-10-06 20:30", "21:30", "half negen→20:30", false},
		{"Overleg vr half twee", "Overleg", "2026-10-09 13:30", "14:30", "vr→vrijdag half twee→13:30", false},
		// A title word is no hour
		{"Half jaar plan 10:00", "Half jaar plan", "2026-10-08 10:00", "11:00", "", false},
		{"half 10:00", "half", "2026-10-08 10:00", "11:00", "", false},
		{"Half twee 10:00", "Half twee", "2026-10-08 10:00", "11:00", "", false},
		// om 3 uur, 3 uur
		{"Overleg om 3 uur", "Overleg", "2026-10-08 15:00", "16:00", "3 uur→15:00", false},
		{"Overleg om 9 uur", "Overleg", "2026-10-08 09:00", "10:00", "9 uur→09:00", false},
		{"Overleg om drie uur", "Overleg", "2026-10-08 15:00", "16:00", "drie uur→15:00", false},
		{"Overleg om 14 uur", "Overleg", "2026-10-08 14:00", "15:00", "14 uur→14:00", false},
		{"Overleg om 14.00 uur", "Overleg", "2026-10-08 14:00", "15:00", "14.00 uur→14:00", false},
		{"Overleg 14.00 uur", "Overleg", "2026-10-08 14:00", "15:00", "14.00 uur→14:00", false},
		{"Schrijven 1.5 uur", "Schrijven", "2026-10-08 09:00", "10:30", "", false},
		{"Overleg om 3", "Overleg", "2026-10-08 15:00", "16:00", "3→15:00", false},
		{"Overleg morgen 3 uur", "Overleg", "2026-10-07 15:00", "16:00", "3 uur→15:00", false},
		{"Overleg morgen 10 uur", "Overleg", "2026-10-07 10:00", "11:00", "10 uur→10:00", false},
		{"Overleg morgen 14.00 uur", "Overleg", "2026-10-07 14:00", "15:00", "14.00 uur→14:00", false},
		{"Overleg morgen middag 3 uur", "Overleg", "2026-10-07 15:00", "16:00", "3 uur→15:00", false},
		{"Overleg vanmiddag 3 uur", "Overleg", "2026-10-06 15:00", "16:00", "3 uur→15:00", false},
		// without a day or "om", "3 uur" stays a length
		{"Schrijven 3 uur", "Schrijven", "2026-10-08 09:00", "12:00", "", false},
		{"Schrijven morgen 10:00 3 uur", "Schrijven", "2026-10-07 10:00", "13:00", "", false},
		{"Schrijven 10:00 2 uur", "Schrijven", "2026-10-08 10:00", "12:00", "", false},
		{"Schrijven 10:00 half uur", "Schrijven", "2026-10-08 10:00", "10:30", "", false},
		{"Schrijven 10:00 een uur", "Schrijven", "2026-10-08 10:00", "11:00", "", false},
		// hele dag, vanmiddag, vanavond
		{"Conferentie 12 nov hele dag", "Conferentie", "2026-11-12 00:00", "00:00", "", true},
		{"Conferentie 12 nov de hele dag", "Conferentie", "2026-11-12 00:00", "00:00", "", true},
		{"Overleg vanmiddag", "Overleg", "2026-10-06 14:00", "15:00", "", false},
		{"Etentje vanavond", "Etentje", "2026-10-06 19:00", "20:00", "", false},
		{"Etentje vanavond 20:00", "Etentje", "2026-10-06 20:00", "21:00", "", false},
		// eind van de middag
		{"Review morgen eind van de middag", "Review", "2026-10-07 17:00", "18:00", "", false},
		{"Eind van de middag review", "Eind van de middag review", "2026-10-08 09:00", "10:00", "", false},
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
		got := [5]string{parsed.Title, start.Format("2006-01-02 15:04"), end.Format("15:04"), strings.Join(notes, " "), ""}
		want := [5]string{c.title, c.start, c.end, c.notes, ""}
		if got != want || parsed.AllDay != c.allDay {
			t.Errorf("%q:\n got  %q allDay=%v\n want %q allDay=%v", c.input, got, parsed.AllDay, want, c.allDay)
		}
	}
}

func TestDutchRepeatsAndRoles(t *testing.T) {
	parsed, err := Parse("Sporten elke ma en wo 18:30", now)
	if err != nil || parsed.Repeat == nil || parsed.Repeat.Rule() != "FREQ=WEEKLY;BYDAY=MO,WE" {
		t.Errorf("elke ma en wo: %+v %v", parsed.Repeat, err)
	}
	parsed, err = Parse("Sporten elke do en za 18:30", now)
	if err != nil || parsed.Repeat == nil || parsed.Repeat.Rule() != "FREQ=WEEKLY;BYDAY=TH,SA" {
		t.Errorf("elke do en za: %+v %v", parsed.Repeat, err)
	}
	roles := map[string]string{
		"Overleg volgende vr om half twee": "title filler date time time time",
		"Overleg morgen 3 uur":             "title date time time",
		"Check over 2 weken":               "title date date date",
		"Overleg do 14:00":                 "title date time",
		"Things to do":                     "title title title",
	}
	for input, want := range roles {
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

func TestDutchUurMarksLengthsAndTimes(t *testing.T) {
	cases := []struct {
		input  string
		clock  time.Duration
		length time.Duration
	}{
		{"Overleg vrijdag half twee 1u", 13*time.Hour + 30*time.Minute, time.Hour},
		{"Schrijven 2u", 0, 2 * time.Hour},
		{"Lunch 1u30 om 12", 12 * time.Hour, 90 * time.Minute},
		{"Borrel vrijdag 17u", 17 * time.Hour, 0},
		{"Borrel vr 17u30", 17*time.Hour + 30*time.Minute, 0},
	}
	for _, c := range cases {
		parsed, err := Parse(c.input, now)
		if err != nil || parsed.Clock != c.clock || parsed.Length != c.length {
			t.Errorf("%q: clock %v length %v err %v; want %v %v", c.input, parsed.Clock, parsed.Length, err, c.clock, c.length)
		}
	}
}

func TestOrdinaryDutchWordsAreNotDays(t *testing.T) {
	for _, input := range []string{"Dag vrij morgen", "Volle maan fri 21:00"} {
		parsed, err := Parse(input, now)
		if err != nil {
			t.Fatalf("%q: %v", input, err)
		}
		for _, note := range parsed.Notes {
			if note.Word == "vrij" || note.Word == "maan" {
				t.Errorf("%q: %q was read as %q", input, note.Word, note.Meaning)
			}
		}
	}
}
