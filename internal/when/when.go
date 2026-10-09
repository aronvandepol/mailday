// Package when reads the short date and time phrases Mailday's calendar
// prompts accept, so an event is typed rather than filled in:
//
//	Lunch with Ada tomorrow 12:30 1h @Atrium #Work
//	Gym fri 18:30-20:00
//	Conference 12 nov all day #Travel
//
// and an event is rebooked with "fri", "14:00", "fri 14:00", "+1d", "-30m",
// "14:00-15:30" or "1h30" (a new length).
//
// Dates: today, tomorrow, overmorgen and the other Dutch day words, weekday
// names (the next one after today), "in 3 days", "+3d", "12 oct", "oct 12",
// "12/10" (day first), "12.10.2026", "2026-10-12"; a dotted day and month
// without a year is a clock time ("14.05"). Times: 14:00, 9:30, 9am, 2pm, noon,
// "at 9". Ranges: 14:00-15:30, 9-11, "14:00 to 15:30". Lengths: 1h, 90m,
// 1h30, 1.5h, "for 2h", "for 3 days" (with "all day"). "all day" makes an
// all-day event. Dutch works too: volgende vrijdag, over 2 weken, vr, mrt,
// half twee, kwart over drie, om 3 uur, hele dag (see dutch.go). Words like
// free, weekly, noon and "next week" only count in lower case: "Free lunch"
// is a title. Words that are none of these form the title; "@" starts the location and "#" names the calendar.
package when

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Parsed is what a phrase said; the Has fields tell which parts it gave.
type Parsed struct {
	Title    string
	Location string
	Calendar string // as typed after #, matched by the caller

	Date     time.Time // midnight, local
	HasDate  bool
	Clock    time.Duration // since midnight
	HasTime  bool
	EndClock time.Duration
	HasEnd   bool
	// EndNextDay: the end clock is on the day after the start ("22:00-02:00").
	EndNextDay bool
	Length     time.Duration
	AllDay     bool
	Shift      time.Duration // +1d, -30m: relative moves
	Free       bool          // "free" / "next free": the caller finds a gap
	Repeat     *Repeat       // "every mon", "daily until 20 dec", "weekly 10 times"

	// Roles says what each word of the input (strings.Fields) was read as:
	// title, date, time, length, repeat, allday, shift, free, filler,
	// location or calendar. Mailday colours the input with it.
	Roles []string
	// Notes lists the words read as something else: tom → tomorrow, 2p →
	// 14:00, wensday → wednesday.
	Notes []Note

	partOfDayTime bool // the time came from "afternoon" and a clock may refine it
}

// Repeat is a recurrence rule, the parts of RFC 5545 RRULE Mailday writes.
type Repeat struct {
	Freq     string // DAILY, WEEKLY, MONTHLY
	Interval int
	Days     []time.Weekday
	Until    time.Time // last day, inclusive; zero for none
	Count    int
}

var ruleDays = []string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}

// Rule is the RRULE value, e.g. FREQ=WEEKLY;BYDAY=MO,WE;UNTIL=20261220T225959Z.
func (r Repeat) Rule() string {
	parts := []string{"FREQ=" + r.Freq}
	if r.Interval > 1 {
		parts = append(parts, fmt.Sprintf("INTERVAL=%d", r.Interval))
	}
	if len(r.Days) > 0 {
		var days []string
		for _, day := range r.Days {
			days = append(days, ruleDays[day])
		}
		parts = append(parts, "BYDAY="+strings.Join(days, ","))
	}
	if !r.Until.IsZero() {
		end := time.Date(r.Until.Year(), r.Until.Month(), r.Until.Day(), 23, 59, 59, 0, r.Until.Location())
		parts = append(parts, "UNTIL="+end.UTC().Format("20060102T150405Z"))
	}
	if r.Count > 0 {
		parts = append(parts, fmt.Sprintf("COUNT=%d", r.Count))
	}
	return strings.Join(parts, ";")
}

// Describe says the rule in words: "every Monday and Wednesday until 20 Oct".
func (r Repeat) Describe() string {
	text := map[string]string{"DAILY": "every day", "WEEKLY": "every week", "MONTHLY": "every month"}[r.Freq]
	if r.Interval > 1 {
		text = fmt.Sprintf("every %d %s", r.Interval, map[string]string{"DAILY": "days", "WEEKLY": "weeks", "MONTHLY": "months"}[r.Freq])
	}
	if len(r.Days) == 5 && r.Days[0] == time.Monday && r.Days[4] == time.Friday {
		text = "every weekday"
	} else if len(r.Days) > 0 {
		var names []string
		for _, day := range r.Days {
			names = append(names, day.String())
		}
		if r.Interval > 1 {
			text += " on " + strings.Join(names, " and ")
		} else {
			text = "every " + strings.Join(names, " and ")
		}
	}
	if !r.Until.IsZero() {
		text += " until " + r.Until.Format("2 Jan")
	}
	if r.Count > 0 {
		text += fmt.Sprintf(", %d times", r.Count)
	}
	return text
}

var (
	clockPattern   = regexp.MustCompile(`^(\d{1,2})(?:([:.hu])(\d{2}))?(am|pm|a|p|u|h)?$`)
	rangePattern   = regexp.MustCompile(`^(\d{1,2}(?:[:.]\d{2})?(?:am|pm|a|p)?)[-–](\d{1,2}(?:[:.]\d{2})?(?:am|pm|a|p|u)?)$`)
	ordinalPattern = regexp.MustCompile(`^(\d{1,2})(st|nd|rd|th|e|ste|de)$`)
	lengthPattern  = regexp.MustCompile(`^(\d+(?:\.\d+)?)(h|hr|hrs|hour|hours|m|min|mins|minutes?|u)$|^(\d+)[hu](\d+)m?$`)
	shiftPattern   = regexp.MustCompile(`^([+-])(\d+)(m|min|h|d|w)$`)
	isoDatePattern = regexp.MustCompile(`^(\d{4})-(\d{1,2})-(\d{1,2})$`)
	// A date with slashes may leave out the year; one with dots needs it,
	// because "14.05" and "9.10" are clock times and "Sprint 2.1" a title.
	slashDatePattern = regexp.MustCompile(`^(\d{1,2})/(\d{1,2})(?:/(\d{2,4}))?$`)
	dotDatePattern   = regexp.MustCompile(`^(\d{1,2})\.(\d{1,2})\.(\d{2,4})$`)
)

var weekdays = map[string]time.Weekday{
	"sun": time.Sunday, "sunday": time.Sunday, "zondag": time.Sunday,
	"mon": time.Monday, "monday": time.Monday, "maandag": time.Monday,
	"tue": time.Tuesday, "tues": time.Tuesday, "tuesday": time.Tuesday, "dinsdag": time.Tuesday,
	"wed": time.Wednesday, "weds": time.Wednesday, "wednesday": time.Wednesday, "woensdag": time.Wednesday,
	"thu": time.Thursday, "thur": time.Thursday, "thurs": time.Thursday, "thursday": time.Thursday, "donderdag": time.Thursday,
	"fri": time.Friday, "friday": time.Friday, "vrijdag": time.Friday,
	"sat": time.Saturday, "saturday": time.Saturday, "zaterdag": time.Saturday,
}

var months = map[string]time.Month{}

func init() {
	for month := time.January; month <= time.December; month++ {
		name := strings.ToLower(month.String())
		months[name] = month
		months[name[:3]] = month
	}
	months["sept"] = time.September
	months["mrt"] = time.March
	for name, month := range map[string]time.Month{"januari": 1, "februari": 2, "maart": 3, "mei": 5, "juni": 6, "juli": 7, "augustus": 8, "oktober": 10, "okt": 10} {
		months[name] = month
	}
}

// Parse reads a phrase relative to now.
func Parse(input string, now time.Time) (Parsed, error) {
	var parsed Parsed
	today := midnight(now)
	text := strings.TrimSpace(input)

	// "#Calendar" anywhere, "@ location" to the end (or to the #).
	fields := strings.Fields(text)
	parsed.Roles = make([]string, len(fields))
	var words []string
	var origin []int // words[i] is fields[origin[i]]
	for position, field := range fields {
		if strings.HasPrefix(field, "#") && len(field) > 1 {
			parsed.Calendar = field[1:]
			parsed.Roles[position] = "calendar"
			continue
		}
		words = append(words, field)
		origin = append(origin, position)
	}
	for index, word := range words {
		if strings.HasPrefix(word, "@") {
			parsed.Location = strings.TrimSpace(strings.TrimPrefix(strings.Join(words[index:], " "), "@"))
			for _, position := range origin[index:] {
				parsed.Roles[position] = "location"
			}
			words, origin = words[:index], origin[:index]
			break
		}
	}
	roleOf := make([]string, len(words))
	// What each word is read as (normalize), and words kept literal in quotes.
	canon := make([]string, len(words))
	literal := make([]bool, len(words))
	quoted := false
	for index, raw := range words {
		if quoted || (strings.HasPrefix(raw, `"`) && len(raw) > 0) {
			opening := !quoted
			literal[index] = true
			quoted = !(strings.HasSuffix(raw, `"`) && (!opening || len(raw) > 1))
			words[index] = strings.Trim(raw, `"`)
			continue
		}
		word, note := normalize(raw)
		canon[index] = word
		if note != nil {
			parsed.Notes = append(parsed.Notes, *note)
		}
	}
	parsed.Notes = append(parsed.Notes, readDutchDays(words, canon, literal)...)
	var lastWeekday *time.Weekday
	nextWeek, partOfDay := false, ""

	// title keeps word positions: when a second date turns up, the first
	// one was part of the title ("Tuesday meeting tue") and goes back in place.
	type word struct {
		index int
		text  string
	}
	var title []word
	var dateWords []word
	current := 0
	consumedWords := func(count int) []word {
		var result []word
		for offset := range count {
			if current+offset < len(words) {
				result = append(result, word{current + offset, words[current+offset]})
			}
		}
		return result
	}
	dateCount := 1
	dateTouched := false
	setDate := func(date time.Time) error {
		dateTouched = true
		if parsed.HasDate {
			title = append(title, dateWords...)
		}
		parsed.Date, parsed.HasDate = date, true
		dateWords = consumedWords(dateCount)
		dateCount = 1
		return nil
	}
	lower := func(index int) string {
		if index < 0 || index >= len(words) || literal[index] {
			return ""
		}
		return canon[index]
	}
	// typedLower is true for a word written in lower case. Keywords that
	// would swallow a title word ("Free lunch", "Weekly sync", "Plan next
	// week") only count in lower case, as normalize.go only rewrites
	// lower case words.
	typedLower := func(index int) bool {
		return index >= 0 && index < len(words) && words[index] == strings.ToLower(words[index])
	}
	// setClock takes a time, guessing the afternoon for a bare 1 to 6 and
	// after "afternoon" or "evening": "at 3" is 15:00.
	setClock := func(clock clockValue, raw string) {
		value := clock.value
		if !clock.explicit && !clock.leadingZero && value >= time.Hour && value < 12*time.Hour &&
			(value < 7*time.Hour || partOfDay == "afternoon" || partOfDay == "evening") {
			value += 12 * time.Hour
		}
		parsed.Clock, parsed.HasTime = value, true
		if clock.shorthand || value != clock.value {
			parsed.Notes = append(parsed.Notes, Note{raw, fmt.Sprintf("%02d:%02d", int(value.Hours()), int(value.Minutes())%60)})
		}
	}
	for index := 0; index < len(words); index++ {
		current = index
		token := lower(index)
		next := lower(index + 1)
		consumed := 1
		before, titleBefore := snapshot(parsed), len(title)
		dutch, dutchUsed, dutchOK := dutchClock(words, canon, index)
		// A time may be set unless one is already given ("14:00 half twee"
		// is a title), though "afternoon" gives way to a clock.
		timeFree := !parsed.HasTime || parsed.partOfDayTime
		afterDay := index > 0 && (roleOf[index-1] == "date" || parsed.partOfDayTime)
		dateTouched = false
		started := index
		if literal[index] {
			title = append(title, word{index, words[index]})
			continue
		}
		switch {
		case token == "":
			continue
		case token == "today" || token == "vandaag":
			if err := setDate(today); err != nil {
				return parsed, err
			}
		case token == "tonight" || token == "vanavond" || token == "vanmiddag" || token == "vanochtend" || token == "vanmorgen":
			if err := setDate(today); err != nil {
				return parsed, err
			}
			partOfDay = map[string]string{"tonight": "evening", "vanavond": "evening", "vanmiddag": "afternoon"}[token]
			if partOfDay == "" {
				partOfDay = "morning"
			}
			if !parsed.HasTime {
				parsed.Clock = map[string]time.Duration{"morning": 9 * time.Hour, "afternoon": 14 * time.Hour, "evening": 19 * time.Hour}[partOfDay]
			}
		case token == "tomorrow" || token == "tmrw" || token == "tmw" || token == "morgen":
			if err := setDate(today.AddDate(0, 0, 1)); err != nil {
				return parsed, err
			}
		case token == "overmorgen":
			if err := setDate(today.AddDate(0, 0, 2)); err != nil {
				return parsed, err
			}
		case token == "all" && (next == "day" || next == "day,"), token == "whole" && next == "day", token == "hele" && next == "dag":
			parsed.AllDay = true
			consumed = 2
		case token == "de" && next == "hele" && lower(index+2) == "dag" && typedLower(index):
			parsed.AllDay = true
			consumed = 3
		case token == "allday" || token == "all-day":
			parsed.AllDay = true
		case (token == "free" && typedLower(index)) || (token == "next" && next == "free" && typedLower(index) && typedLower(index+1)):
			parsed.Free = true
			if token == "next" {
				consumed = 2
			}
		case token == "every" || token == "elke" || token == "iedere":
			repeat := parsed.Repeat
			if repeat == nil {
				repeat = &Repeat{Interval: 1}
			}
			used := 1
			for offset := index + 1; offset < len(words); offset++ {
				next := lower(offset)
				if next == "and" || next == "en" {
					used++
					continue
				}
				if day, ok := weekdays[next]; ok {
					repeat.Freq = "WEEKLY"
					repeat.Days = append(repeat.Days, day)
					used++
					continue
				}
				if day, ok := weekdays[strings.TrimSuffix(next, "s")]; ok {
					repeat.Freq = "WEEKLY"
					repeat.Days = append(repeat.Days, day)
					used++
					continue
				}
				if number, err := strconv.Atoi(next); err == nil && number > 1 && repeat.Freq == "" {
					repeat.Interval = number
					used++
					continue
				}
				if next == "weekday" || next == "weekdays" || next == "werkdag" {
					repeat.Freq, repeat.Days = "WEEKLY", []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday}
					used++
					continue
				}
				freq := map[string]string{"day": "DAILY", "days": "DAILY", "dag": "DAILY", "week": "WEEKLY", "weeks": "WEEKLY",
					"month": "MONTHLY", "months": "MONTHLY", "maand": "MONTHLY"}[next]
				if freq != "" && repeat.Freq == "" {
					repeat.Freq = freq
					used++
					continue
				}
				break
			}
			if repeat.Freq == "" {
				title = append(title, word{index, words[index]})
				break
			}
			parsed.Repeat = repeat
			consumed = used
		case (token == "daily" || token == "weekly" || token == "monthly" || token == "biweekly" || token == "weekdays" || token == "dagelijks" || token == "wekelijks") && typedLower(index):
			repeat := &Repeat{Interval: 1}
			switch token {
			case "daily", "dagelijks":
				repeat.Freq = "DAILY"
			case "weekly", "wekelijks":
				repeat.Freq = "WEEKLY"
			case "biweekly":
				repeat.Freq, repeat.Interval = "WEEKLY", 2
			case "weekdays":
				repeat.Freq, repeat.Days = "WEEKLY", []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday}
			default:
				repeat.Freq = "MONTHLY"
			}
			if parsed.Repeat != nil {
				repeat.Until, repeat.Count = parsed.Repeat.Until, parsed.Repeat.Count
			}
			parsed.Repeat = repeat
		case (token == "until" || token == "tot") && parsed.Repeat != nil && next != "":
			if day, ok := weekdays[next]; ok {
				parsed.Repeat.Until = nextWeekday(today, day)
				consumed = 2
			} else if date, extra, ok, err := parseDate(canon, index+1, today); err != nil {
				return parsed, err
			} else if ok {
				parsed.Repeat.Until = date
				consumed = 2 + extra
			} else {
				title = append(title, word{index, words[index]})
			}
		case isNumber(token) && (next == "times" || next == "keer") && parsed.Repeat != nil:
			parsed.Repeat.Count, _ = strconv.Atoi(token)
			consumed = 2
		case (token == "morning" || token == "afternoon" || token == "evening" || token == "ochtend" || token == "middag" || token == "avond" || token == "eod") &&
			(parsed.HasDate || lower(index-1) == "this" || lower(index-1) == "the" || lower(index-1) == "deze" || token == "eod"):
			// A part of the day after a day ("tomorrow afternoon"), or "this evening".
			part := map[string]string{"ochtend": "morning", "middag": "afternoon", "avond": "evening", "eod": "eod"}[token]
			if part == "" {
				part = token
			}
			if lower(index-1) == "this" && !parsed.HasDate {
				if err := setDate(today); err != nil {
					return parsed, err
				}
			}
			partOfDay = part
			if !parsed.HasTime {
				parsed.Clock = map[string]time.Duration{"morning": 9 * time.Hour, "afternoon": 14 * time.Hour, "evening": 19 * time.Hour, "eod": 17 * time.Hour}[part]
				parsed.HasTime = true
				parsed.partOfDayTime = true
			}
		case token == "next" && next == "week" && typedLower(index) && typedLower(index+1):
			nextWeek = true
			consumed = 2
		case (token == "weekend" && typedLower(index)) || ((token == "this" || token == "next") && next == "weekend" && typedLower(index+1)):
			saturday := nextWeekday(today.AddDate(0, 0, -1), time.Saturday)
			if today.Weekday() == time.Sunday {
				saturday = today
			}
			if token == "next" {
				saturday = saturday.AddDate(0, 0, 7)
			}
			if token != "weekend" {
				consumed = 2
			}
			if err := setDate(saturday); err != nil {
				return parsed, err
			}
		case (token == "in" || (token == "over" && typedLower(index))) && countWord(next) >= 0 && unitDays(lower(index+2)) != 0:
			count := countWord(next)
			unit := unitDays(lower(index + 2))
			date := today.AddDate(0, 0, count*unit)
			if unit == 30 {
				date = today.AddDate(0, count, 0)
			}
			dateCount = 3
			if err := setDate(date); err != nil {
				return parsed, err
			}
			consumed = 3
		case (token == "an" || token == "a" || token == "one" || (token == "een" && typedLower(index))) && (next == "hour" || next == "hours"):
			parsed.Length = time.Hour
			consumed = 2
		case token == "half" && (next == "hour" || (next == "hours" && isUur(words[index+1])) || (next == "an" && lower(index+2) == "hour") || (next == "a" && lower(index+2) == "hour")):
			parsed.Length = 30 * time.Minute
			consumed = 2
			if next != "hour" && next != "hours" {
				consumed = 3
			}
		case dutchOK && typedLower(index) && timeFree && (token == "half" || token == "kwart" || afterDay || strings.ContainsAny(token, ".:")):
			// "half twee", "kwart over drie", "14.00 uur" and "3 uur" straight
			// after a day ("morgen 3 uur"); elsewhere "3 uur" is a length.
			setClock(dutch, strings.Join(words[index:index+dutchUsed], " "))
			parsed.partOfDayTime = false
			consumed = dutchUsed
		case token == "eind" && next == "van" && lower(index+2) == "de" && lower(index+3) == "middag" && typedLower(index):
			partOfDay = "afternoon"
			parsed.Clock, parsed.HasTime, parsed.partOfDayTime = 17*time.Hour, true, false
			consumed = 4
		case isDecimal(token) && unitMinutes(next) != 0 && parsed.Length == 0:
			amount, _ := strconv.ParseFloat(token, 64)
			parsed.Length = time.Duration(amount * float64(unitMinutes(next)) * float64(time.Minute))
			consumed = 2
		case ordinalPattern.MatchString(token) || (token == "the" && ordinalPattern.MatchString(next)):
			if token == "the" {
				consumed = 2
				token = next
			}
			day, _ := strconv.Atoi(ordinalPattern.FindStringSubmatch(token)[1])
			// The next such day: this month, or next month once it has passed.
			date, _, ok := validDate(today.Year(), today.Month(), day, today.Location())
			if !ok || date.Before(today) {
				next := today.AddDate(0, 1, 1-today.Day())
				date, _, ok = validDate(next.Year(), next.Month(), day, today.Location())
			}
			if !ok {
				title = append(title, word{index, words[index]})
				break
			}
			if err := setDate(date); err != nil {
				return parsed, err
			}
		case (token == "noon" || token == "midday") && typedLower(index):
			parsed.Clock, parsed.HasTime = 12*time.Hour, true
		case token == "midnight" && typedLower(index):
			parsed.Clock, parsed.HasTime = 0, true

		case (token == "at" || token == "om") && next != "":
			if clock, used, ok := dutchClock(words, canon, index+1); ok && token == "om" {
				setClock(clock, strings.Join(words[index+1:index+1+used], " "))
				parsed.partOfDayTime = false
				consumed = 1 + used
			} else if clock, ok := parseClock(next, lower(index+2)); ok {
				setClock(clock, words[index+1])
				parsed.partOfDayTime = false
				consumed = 2 + clock.extra
			} else {
				title = append(title, word{index, words[index]})
			}
		case token == "for" && isNumber(next) && unitDays(lower(index+2)) == 1 && next != "0" && parsed.Length == 0:
			// "all day for 3 days": the span of a multi-day event.
			days, _ := strconv.Atoi(next)
			parsed.Length = time.Duration(days) * 24 * time.Hour
			consumed = 3
		case token == "for" && next != "":
			if length, ok := parseLength(next); ok {
				parsed.Length = length
				consumed = 2
			} else if next == "half" || next == "an" || next == "a" || next == "one" || (isDecimal(next) && unitMinutes(lower(index+2)) > 0) {
				// "for half an hour", "for 90 minutes": the length follows
			} else {
				title = append(title, word{index, words[index]})
			}
		case (token == "to" || token == "-" || token == "–") && parsed.HasTime && !parsed.HasEnd,
			(token == "until" || token == "tot") && !parsed.HasEnd && parsed.Repeat == nil && looksLikeClockOrBare(next):
			if clock, ok := parseClock(next, lower(index+2)); ok {
				parsed.EndClock, parsed.HasEnd = clock.value, true
				consumed = 2 + clock.extra
			} else {
				title = append(title, word{index, words[index]})
			}
		case (token == "next" || token == "this" || token == "on" || token == "from") && next != "" && startsDateOrTime(next) &&
			((next != "week" && next != "weekend" && next != "free") || typedLower(index+1)):
			// filler before a date or time
		default:
			if day, ok := weekdays[token]; ok {
				lastWeekday = &day
				if err := setDate(nextWeekday(today, day)); err != nil {
					return parsed, err
				}
				break
			}
			date, extra, ok, err := parseDate(canon, index, today)
			if err != nil {
				return parsed, err
			}
			if ok {
				dateCount = 1 + extra
				if err := setDate(date); err != nil {
					return parsed, err
				}
				consumed = 1 + extra
				break
			}
			if match := shiftPattern.FindStringSubmatch(token); match != nil {
				amount, _ := strconv.Atoi(match[2])
				unit := map[string]time.Duration{"m": time.Minute, "min": time.Minute, "h": time.Hour, "d": 24 * time.Hour, "w": 7 * 24 * time.Hour}[match[3]]
				shift := time.Duration(amount) * unit
				if match[1] == "-" {
					shift = -shift
				}
				parsed.Shift += shift
				break
			}
			if match := rangePattern.FindStringSubmatch(token); match != nil {
				from, okFrom := parseClock(match[1], "")
				to, okTo := parseClock(match[2], "")
				if okFrom && okTo && (!parsed.HasTime || parsed.partOfDayTime) {
					// "9-11am": the suffix on the end applies to the start.
					if (strings.HasSuffix(match[2], "pm") || strings.HasSuffix(match[2], "p")) && !strings.ContainsAny(match[1], "apm") && from.value < 12*time.Hour && from.value+12*time.Hour <= to.value {
						from.value += 12 * time.Hour
					} else if !from.explicit && !to.explicit && !from.leadingZero && from.value >= time.Hour && from.value < 7*time.Hour {
						// "2-4" is the afternoon
						from.value += 12 * time.Hour
						to.value += 12 * time.Hour
					}
					parsed.Clock, parsed.HasTime, parsed.partOfDayTime = from.value, true, false
					parsed.EndClock, parsed.HasEnd = to.value, true
					break
				}
			}
			if clock, ok := parseClock(token, next); ok && looksLikeClock(token, next) && (!parsed.HasTime || parsed.partOfDayTime) {
				setClock(clock, words[index])
				parsed.partOfDayTime = false
				consumed = 1 + clock.extra
				break
			}
			if length, ok := parseLength(token); ok && parsed.Length == 0 {
				parsed.Length = length
				break
			}
			title = append(title, word{index, words[index]})
		}
		titled := false // this word went into the title (not an earlier date pushed back)
		for _, part := range title[titleBefore:] {
			if part.index >= started && part.index < started+consumed {
				titled = true
			}
		}
		if !titled {
			role := changedRole(before, snapshot(parsed))
			if dateTouched && role == "filler" {
				role = "date" // the same day again ("Tuesday … tue")
			}
			for offset := 0; offset < consumed && started+offset < len(roleOf); offset++ {
				roleOf[started+offset] = role
			}
		}
		index += consumed - 1
	}
	// "next week" is that week's Monday, or the weekday given with it.
	if nextWeek {
		monday := today.AddDate(0, 0, (8-int(today.Weekday()))%7)
		if today.Weekday() == time.Monday {
			monday = today.AddDate(0, 0, 7)
		}
		date := monday
		if lastWeekday != nil {
			date = monday.AddDate(0, 0, (int(*lastWeekday)+6)%7)
		}
		parsed.Date, parsed.HasDate = date, true
	}
	if partOfDay != "" && !parsed.HasTime && parsed.Clock != 0 {
		parsed.HasTime = true // "tonight" alone: 19:00
	}
	for _, part := range title {
		roleOf[part.index] = "title"
	}
	for index, role := range roleOf {
		if role == "" {
			role = "title"
		}
		parsed.Roles[origin[index]] = role
	}
	if parsed.HasEnd && parsed.EndClock <= parsed.Clock {
		if parsed.EndClock+12*time.Hour > parsed.Clock && parsed.EndClock < 12*time.Hour {
			parsed.EndClock += 12 * time.Hour // "11-1" means 11:00 to 13:00
		} else if parsed.EndClock < 12*time.Hour && parsed.Clock >= 12*time.Hour {
			parsed.EndNextDay = true // "22:00-02:00" runs past midnight
		} else {
			return parsed, errors.New("the end is before the start")
		}
	}
	sort.Slice(title, func(i, j int) bool { return title[i].index < title[j].index })
	var titleWords []string
	for _, part := range title {
		titleWords = append(titleWords, part.text)
	}
	parsed.Title = strings.Join(titleWords, " ")
	return parsed, nil
}

type clockValue struct {
	value       time.Duration
	extra       int  // a following "am"/"pm" word was used
	explicit    bool // am or pm was said
	leadingZero bool // "09:00" means nine in the morning, no guessing
	shorthand   bool // not the plain H:MM form: 2p, 14u, 14.30
}

// parseClock reads 14:00, 9:30, 14.30, 9am, 2p, 2.30pm, 14u, 14u30, 14h30,
// and 9 with "am"/"pm" as the next word.
func parseClock(word, next string) (clockValue, bool) {
	match := clockPattern.FindStringSubmatch(word)
	if match == nil {
		return clockValue{}, false
	}
	hour, _ := strconv.Atoi(match[1])
	minute := 0
	if match[3] != "" {
		minute, _ = strconv.Atoi(match[3])
	}
	separator, suffix, extra := match[2], match[4], 0
	if suffix == "" && (next == "am" || next == "pm") {
		suffix, extra = next, 1
	}
	value := clockValue{extra: extra, leadingZero: strings.HasPrefix(word, "0")}
	switch suffix {
	case "am", "a":
		if hour == 12 {
			hour = 0
		}
		value.explicit = true
	case "pm", "p":
		if hour < 12 {
			hour += 12
		}
		value.explicit = true
	case "u", "h":
		value.explicit = true // 24-hour by convention
	}
	if separator == "u" || separator == "h" {
		value.explicit = true
	}
	value.shorthand = (suffix != "" && suffix != "am" && suffix != "pm") || (separator != "" && separator != ":")
	if hour > 24 || minute > 59 || (hour == 24 && minute > 0) {
		return clockValue{}, false
	}
	value.value = time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute
	return value, true
}

// looksLikeClock keeps plain numbers in titles ("Room 12") and lengths
// ("2h"): a number is a time with a colon or dot, am/pm (2p), the Dutch u
// (14u, 14u30), or h with minutes from 7 on (14h30; 1h30 is a length).
// looksLikeClockOrBare accepts a bare hour too, for "until 11".
func looksLikeClockOrBare(word string) bool {
	return clockPattern.MatchString(word)
}

func looksLikeClock(word, next string) bool {
	match := clockPattern.FindStringSubmatch(word)
	if match == nil {
		return false
	}
	hour, _ := strconv.Atoi(match[1])
	separator, suffix := match[2], match[4]
	switch {
	case separator == ".":
		// 14.30 and 9.10 are times, but "Lab 2.04" is a room and "Sprint 2.1"
		// a version: a bare dotted number needs an hour from 7 on, a leading
		// zero or am/pm.
		return hour >= 7 || strings.HasPrefix(word, "0") || suffix != ""
	case separator == ":":
		return true
	case separator == "h" || separator == "u":
		// 14h30 and 17u30 are times; 1h30 and 1u30 lengths.
		return hour >= 7
	case suffix == "u":
		// 17u is five o'clock; 2u is two hours (uur).
		return hour >= 7
	case suffix == "am" || suffix == "pm" || suffix == "a" || suffix == "p":
		return true
	}
	return next == "am" || next == "pm"
}

func parseLength(word string) (time.Duration, bool) {
	match := lengthPattern.FindStringSubmatch(word)
	if match == nil {
		return 0, false
	}
	if match[3] != "" {
		hours, _ := strconv.Atoi(match[3])
		minutes, _ := strconv.Atoi(match[4])
		return time.Duration(hours)*time.Hour + time.Duration(minutes)*time.Minute, true
	}
	amount, _ := strconv.ParseFloat(match[1], 64)
	unit := time.Minute
	if strings.HasPrefix(match[2], "h") || match[2] == "u" { // Dutch uur
		unit = time.Hour
	}
	length := time.Duration(amount * float64(unit))
	return length, length > 0
}

// parseDate reads a date at words[index]. ok is false for words that are
// not a date; err is set for a word that is shaped like one but is not a
// day on the calendar ("31 apr", "2026-02-30"), so it is not quietly taken
// for part of the title.
func parseDate(words []string, index int, today time.Time) (date time.Time, extra int, ok bool, err error) {
	word := strings.ToLower(strings.Trim(words[index], ","))
	notADate := func(text string) (time.Time, int, bool, error) {
		return time.Time{}, 0, false, fmt.Errorf("%s is not a date", text)
	}
	if match := isoDatePattern.FindStringSubmatch(word); match != nil {
		year, _ := strconv.Atoi(match[1])
		month, _ := strconv.Atoi(match[2])
		day, _ := strconv.Atoi(match[3])
		date, _, ok := validDate(year, time.Month(month), day, today.Location())
		if !ok {
			return notADate(word)
		}
		return date, 0, true, nil
	}
	if match := slashDatePattern.FindStringSubmatch(word); match != nil {
		return numericDate(match, word, today)
	}
	if match := dotDatePattern.FindStringSubmatch(word); match != nil {
		return numericDate(match, word, today)
	}
	following := ""
	if index+1 < len(words) {
		following = strings.ToLower(strings.Trim(words[index+1], ","))
	}
	// "12 oct" and "oct 12"
	if day, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(word, "th"), "st"), "nd"), "rd")); err == nil {
		if month, found := months[following]; found {
			date, _, ok := upcoming(today, month, day)
			if !ok && day >= 1 && day <= 31 {
				return notADate(word + " " + following)
			}
			return date, 1, ok, nil
		}
	}
	if month, found := months[word]; found {
		if day, err := strconv.Atoi(strings.TrimRight(following, "thsndr")); err == nil {
			date, _, ok := upcoming(today, month, day)
			if !ok && day >= 1 && day <= 31 {
				return notADate(word + " " + following)
			}
			return date, 1, ok, nil
		}
	}
	return time.Time{}, 0, false, nil
}

// numericDate reads day, month and an optional year from a 12/10, 12/10/26
// or 12.10.2026 match. A day and month that could be a date but are not
// (31/04) are an error; other numbers ("30/60") are left to the title.
func numericDate(match []string, word string, today time.Time) (time.Time, int, bool, error) {
	day, _ := strconv.Atoi(match[1])
	month, _ := strconv.Atoi(match[2])
	if match[3] != "" {
		year, _ := strconv.Atoi(match[3])
		if year < 100 {
			year += 2000
		}
		date, _, ok := validDate(year, time.Month(month), day, today.Location())
		if !ok {
			return time.Time{}, 0, false, fmt.Errorf("%s is not a date", word)
		}
		return date, 0, true, nil
	}
	date, _, ok := upcoming(today, time.Month(month), day)
	if !ok && day >= 1 && day <= 31 && month >= 1 && month <= 12 {
		return time.Time{}, 0, false, fmt.Errorf("%s is not a date", word)
	}
	return date, 0, ok, nil
}

func validDate(year int, month time.Month, day int, location *time.Location) (time.Time, int, bool) {
	date := time.Date(year, month, day, 0, 0, 0, 0, location)
	if date.Month() != month || date.Day() != day {
		return time.Time{}, 0, false
	}
	return date, 0, true
}

// upcoming is the next day-month on or after yesterday, so "3 jan" typed in
// December means next January. A 29 February looks on to the next leap year.
func upcoming(today time.Time, month time.Month, day int) (time.Time, int, bool) {
	for year := today.Year(); year <= today.Year()+8; year++ {
		date, _, ok := validDate(year, month, day, today.Location())
		if ok && !date.Before(today.AddDate(0, 0, -1)) {
			return date, 0, true
		}
	}
	return time.Time{}, 0, false
}

// nextWeekday is the next such day after today: "fri" on a Friday is a week out.
func nextWeekday(today time.Time, day time.Weekday) time.Time {
	ahead := (int(day) - int(today.Weekday()) + 7) % 7
	if ahead == 0 {
		ahead = 7
	}
	return today.AddDate(0, 0, ahead)
}

// state is what changed when a word was read; see changedRole.
type state struct {
	date            time.Time
	hasDate         bool
	clock, end      time.Duration
	hasTime, hasEnd bool
	length, shift   time.Duration
	allDay, free    bool
	repeat          string
}

func snapshot(parsed Parsed) state {
	value := state{parsed.Date, parsed.HasDate, parsed.Clock, parsed.EndClock, parsed.HasTime, parsed.HasEnd,
		parsed.Length, parsed.Shift, parsed.AllDay, parsed.Free, ""}
	if parsed.Repeat != nil {
		value.repeat = parsed.Repeat.Rule()
	}
	return value
}

// changedRole names a word by what it changed; a word that changed nothing
// and is not in the title was filler ("next", "on", "at").
func changedRole(before, after state) string {
	switch {
	case before.repeat != after.repeat:
		return "repeat"
	case before.date != after.date || before.hasDate != after.hasDate:
		return "date"
	case before.clock != after.clock || before.hasTime != after.hasTime || before.end != after.end || before.hasEnd != after.hasEnd:
		return "time"
	case before.length != after.length:
		return "length"
	case before.allDay != after.allDay:
		return "allday"
	case before.shift != after.shift:
		return "shift"
	case before.free != after.free:
		return "free"
	}
	return "filler"
}

func containsDay(days []time.Weekday, day time.Weekday) bool {
	for _, candidate := range days {
		if candidate == day {
			return true
		}
	}
	return false
}

func startsDateOrTime(word string) bool {
	if _, ok := weekdays[word]; ok {
		return true
	}
	if _, ok := months[word]; ok {
		return true
	}
	switch word {
	case "morning", "afternoon", "evening", "weekend", "week", "night":
		return true
	}
	return word == "today" || word == "tomorrow" || clockPattern.MatchString(word) || rangePattern.MatchString(word) ||
		isoDatePattern.MatchString(word) || slashDatePattern.MatchString(word) || dotDatePattern.MatchString(word) || word == "free"
}

// unitDays reads day, week and month words as days (a month is 30, and
// handled by the caller as a calendar month).
func unitDays(word string) int {
	switch word {
	case "day", "days", "dag", "dagen":
		return 1
	case "week", "weeks", "weken":
		return 7
	case "month", "months", "maand", "maanden":
		return 30
	}
	return 0
}

// unitMinutes reads length words: "90 minutes", "2 hours".
func unitMinutes(word string) int {
	switch word {
	case "minute", "minutes", "minuten":
		return 1
	case "hour", "hours", "uren":
		return 60
	}
	return 0
}

func isDecimal(word string) bool {
	_, err := strconv.ParseFloat(word, 64)
	return err == nil && !strings.HasPrefix(word, "+") && !strings.HasPrefix(word, "-")
}

func isNumber(word string) bool {
	_, err := strconv.Atoi(word)
	return err == nil
}

func midnight(value time.Time) time.Time {
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, value.Location())
}

// at puts a clock time on a date, through daylight-saving changes.
func at(date time.Time, clock time.Duration) time.Time {
	minutes := int(clock / time.Minute)
	return time.Date(date.Year(), date.Month(), date.Day(), minutes/60, minutes%60, 0, 0, date.Location())
}

// Event turns a phrase for a new event into its start and end. Without a
// date it lands on day (the day selected in the calendar); without a time at
// the next half hour when day is today, at 9:00 otherwise; without a length
// it lasts an hour.
func Event(parsed Parsed, day, now time.Time) (start, end time.Time, err error) {
	if parsed.Title == "" {
		return start, end, errors.New("type a title")
	}
	date := midnight(day)
	if parsed.HasDate {
		date = parsed.Date
	}
	if parsed.Shift != 0 && !parsed.HasDate {
		date = midnight(date.Add(parsed.Shift))
	}
	// A weekly repeat starts on its first day from the date on: "every mon"
	// alone, or "from tomorrow every mon". (Google and Exchange count the
	// start as the first occurrence even when it is not one of the days.)
	if parsed.Repeat != nil && len(parsed.Repeat.Days) > 0 {
		for step := 0; step < 7; step++ {
			candidate := date.AddDate(0, 0, step)
			if containsDay(parsed.Repeat.Days, candidate.Weekday()) {
				date = candidate
				break
			}
		}
	}
	if parsed.AllDay {
		days := 1
		if parsed.Length >= 24*time.Hour {
			days = int(parsed.Length / (24 * time.Hour))
		}
		return date, date.AddDate(0, 0, days), nil
	}
	if parsed.HasTime {
		start = at(date, parsed.Clock)
	} else if date.Equal(midnight(now)) {
		start = now.Truncate(30 * time.Minute).Add(30 * time.Minute)
		if !sameDate(start, date) {
			// After 23:30 the next half hour is tomorrow's: book 9:00 then,
			// rather than a midnight start on a day the calendar is not showing.
			start = at(date.AddDate(0, 0, 1), 9*time.Hour)
		}
	} else {
		start = at(date, 9*time.Hour)
	}
	switch {
	case parsed.HasEnd:
		end = at(endDate(date, parsed), parsed.EndClock)
	case parsed.Length > 0:
		end = start.Add(parsed.Length)
	default:
		end = start.Add(time.Hour)
	}
	return start, end, nil
}

func sameDate(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}

// endDate is the day the end clock falls on.
func endDate(date time.Time, parsed Parsed) time.Time {
	if parsed.EndNextDay {
		return date.AddDate(0, 0, 1)
	}
	return date
}

// Move rebooks an event: a date keeps the time of day, a time keeps the date,
// a range or length sets a new length, and +1d/-30m shift it. The event's
// wall clock is the one in the machine's zone (time.Local), whatever zone
// start and end are written in: Google and Exchange hand over UTC.
func Move(parsed Parsed, start, end time.Time) (time.Time, time.Time, error) {
	if parsed.Title != "" {
		return start, end, fmt.Errorf("not a time: %q", parsed.Title)
	}
	if !parsed.HasDate && !parsed.HasTime && parsed.Length == 0 && parsed.Shift == 0 && !parsed.AllDay {
		return start, end, errors.New("type a day, a time, +1d or a length")
	}
	start, end = start.In(time.Local), end.In(time.Local)
	length := end.Sub(start)
	date := midnight(start)
	if parsed.HasDate {
		date = parsed.Date
	}
	// The wall clock, not the time since midnight: those differ by an hour
	// on the days the clocks change.
	clock := time.Duration(start.Hour())*time.Hour + time.Duration(start.Minute())*time.Minute
	if parsed.HasTime {
		clock = parsed.Clock
	}
	newStart := at(date, clock)
	switch {
	case parsed.HasEnd:
		length = at(endDate(date, parsed), parsed.EndClock).Sub(newStart)
	case parsed.Length > 0:
		length = parsed.Length
	}
	newStart = newStart.Add(parsed.Shift)
	if parsed.Shift%(24*time.Hour) == 0 && parsed.Shift != 0 {
		// Whole days keep the wall-clock time across a DST change.
		days := int(parsed.Shift / (24 * time.Hour))
		newStart = at(date.AddDate(0, 0, days), clock)
	}
	return newStart, newStart.Add(length), nil
}

// MoveAllDay rebooks an all-day event by whole days: a date or +2d moves it,
// "for 3 days" changes its span. start is midnight and end the midnight after
// its last day (exclusive), as on the calendar.
func MoveAllDay(parsed Parsed, start, end time.Time) (time.Time, time.Time, error) {
	if parsed.Title != "" {
		return start, end, fmt.Errorf("not a time: %q", parsed.Title)
	}
	if parsed.HasTime || parsed.HasEnd || (parsed.Shift != 0 && parsed.Shift%(24*time.Hour) != 0) {
		return start, end, errors.New("an all-day event moves by days")
	}
	if !parsed.HasDate && parsed.Length == 0 && parsed.Shift == 0 && !parsed.AllDay {
		return start, end, errors.New("type a day, +1d or a length")
	}
	start, end = start.In(time.Local), end.In(time.Local)
	// Calendar days between two midnights, which are 23 or 25 hours long
	// on a daylight-saving change.
	days := max(int((end.Sub(start)+12*time.Hour)/(24*time.Hour)), 1)
	if parsed.Length >= 24*time.Hour {
		days = int(parsed.Length / (24 * time.Hour))
	}
	date := midnight(start)
	if parsed.HasDate {
		date = parsed.Date
	}
	date = date.AddDate(0, 0, int(parsed.Shift/(24*time.Hour)))
	return date, date.AddDate(0, 0, days), nil
}
