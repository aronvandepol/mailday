package when

import (
	"strconv"
	"strings"
	"time"
)

// Dutch phrases that need more than a word-for-word alias: the two-letter
// days and the Dutch ways of saying a time. Single words (volgende, deze,
// maart, vanavond) live in normalize.go and when.go beside their English
// twins. As everywhere in this package, only lower case counts, so a title
// such as "Do not forget" or "Zo of zo" is never read as a date.

var dutchNumbers = map[string]int{
	"een": 1, "één": 1, "twee": 2, "drie": 3, "vier": 4, "vijf": 5, "zes": 6,
	"zeven": 7, "acht": 8, "negen": 9, "tien": 10, "elf": 11, "twaalf": 12,
}

// dutchNumber reads a count written as digits or as a Dutch word.
func dutchNumber(word string) (int, bool) {
	if number, ok := dutchNumbers[word]; ok {
		return number, true
	}
	number, err := strconv.Atoi(word)
	return number, err == nil && number >= 0
}

// countWord is the number in "in 3 days", "over twee weken" or "an hour";
// -1 when the word is none.
func countWord(word string) int {
	if number, err := strconv.Atoi(word); err == nil {
		return number
	}
	if number, ok := dutchNumbers[word]; ok {
		return number
	}
	if word == "a" || word == "an" || word == "one" {
		return 1
	}
	return -1
}

// isUur is the Dutch "uur" as typed: normalize reads it as "hours" for
// lengths ("2 uur"), so the time reading has to look at the raw word.
func isUur(raw string) bool {
	return strings.ToLower(strings.Trim(raw, ",.;!?")) == "uur"
}

// dutchTime is a clock of the given hour and minute where hour 0 is twelve:
// "half een" is 12:30. The afternoon guess for a bare 1 to 6 is setClock's.
func dutchTime(hour, minute int) clockValue {
	if hour == 0 {
		hour = 12
	}
	return clockValue{value: time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute, shorthand: true}
}

// dutchClock reads the Dutch ways to say a time at canon[index]:
//
//	half twee        1:30, the half hour before two (13:30 by the afternoon rule)
//	kwart over drie  3:15
//	kwart voor vier  3:45
//	3 uur, drie uur  3:00 (also 14.00 uur)
//
// used is the number of words. The caller decides whether a "3 uur" is a
// time rather than a length.
func dutchClock(words, canon []string, index int) (clock clockValue, used int, ok bool) {
	at := func(i int) string {
		if i < 0 || i >= len(canon) {
			return ""
		}
		return canon[i]
	}
	hourAt := func(i int) (int, bool) {
		hour, ok := dutchNumber(at(i))
		return hour, ok && hour >= 1 && hour <= 12
	}
	switch at(index) {
	case "half":
		if hour, ok := hourAt(index + 1); ok {
			return dutchTime(hour-1, 30), 2, true
		}
	case "kwart":
		if hour, ok := hourAt(index + 2); ok {
			switch at(index + 1) {
			case "over":
				return dutchTime(hour, 15), 3, true
			case "voor":
				return dutchTime(hour-1, 45), 3, true
			}
		}
	default:
		if at(index+1) != "hours" || !isUur(words[index+1]) {
			break
		}
		if hour, ok := dutchNumber(at(index)); ok && hour <= 24 {
			return clockValue{value: time.Duration(hour) * time.Hour, shorthand: true}, 2, true
		}
		// "14.00 uur", "9:30 uur"
		if clock, ok := parseClock(at(index), ""); ok && looksLikeClock(at(index), "") && strings.ContainsAny(at(index), ".:") {
			clock.shorthand = true
			return clock, 2, true
		}
	}
	return clockValue{}, 0, false
}

// dutchDayNames are the two-letter weekdays. do, za and zo are also
// ordinary words ("to do", "zo snel mogelijk"), so they only count beside
// a date or a time.
var dutchDayNames = map[string]string{
	"ma": "maandag", "di": "dinsdag", "wo": "woensdag", "vr": "vrijdag",
	"do": "donderdag", "za": "zaterdag", "zo": "zondag",
}

var riskyDutchDays = map[string]bool{"do": true, "za": true, "zo": true}

// readDutchDays rewrites ma, di, wo and vr (and do, za, zo beside a date or
// time) in canon to the full Dutch day name, so every later rule (every,
// until, "volgende") sees an ordinary weekday. It returns a note per word.
func readDutchDays(words, canon []string, literal []bool) []Note {
	var notes []Note
	typedLower := func(index int) bool { return words[index] == strings.ToLower(words[index]) }
	weekday := func(index int) bool {
		if index < 0 || index >= len(canon) {
			return false
		}
		_, ok := weekdays[canon[index]]
		return ok
	}
	// besideDateOrTime: a neighbour is a date or a time, or the word before
	// is "en" after a weekday ("do en za").
	dateOrTime := func(index int) bool {
		if index < 0 || index >= len(canon) || literal[index] {
			return false
		}
		word := canon[index]
		if _, ok := weekdays[word]; ok {
			return true
		}
		if _, ok := months[word]; ok {
			return true
		}
		switch word {
		case "today", "tomorrow", "tonight", "vandaag", "morgen", "overmorgen", "vanavond", "vanmiddag", "vanochtend", "vanmorgen",
			"noon", "midday", "midnight", "om", "at", "half", "kwart", "next", "this", "every", "elke", "iedere",
			"morning", "afternoon", "evening", "ochtend", "middag", "avond":
			return true
		}
		if isNumber(word) && index+1 < len(canon) {
			if _, ok := months[canon[index+1]]; ok {
				return true // "do 12 nov"
			}
		}
		return looksLikeClock(word, "") || ordinalPattern.MatchString(word) || isoDatePattern.MatchString(word) ||
			slashDatePattern.MatchString(word) || dotDatePattern.MatchString(word)
	}
	for _, risky := range []bool{false, true} {
		for index, word := range canon {
			name, ok := dutchDayNames[word]
			if !ok || literal[index] || riskyDutchDays[word] != risky || !typedLower(index) {
				continue
			}
			if risky {
				beside := dateOrTime(index+1) || dateOrTime(index-1) ||
					((canon[max(index-1, 0)] == "en" || canon[max(index-1, 0)] == "and") && weekday(index-2))
				if !beside || (index > 0 && canon[index-1] == "to") {
					continue
				}
			}
			notes = append(notes, Note{strings.Trim(words[index], ",.;!?"), name})
			canon[index] = name
		}
	}
	return notes
}
