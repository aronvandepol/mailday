package when

import (
	"strings"
	"unicode"
)

// Normalisation turns what people actually type into the words the parser
// knows: shorthand (tom, tmrw, 2moro), unfinished words (tomo, wedn, septem)
// and typos (tommorow, wensday). It only touches words written in lower case,
// so a name in a title ("Meeting with Tom") stays a name; quotes keep any
// word literal. Every rewrite is reported as a Note so the caller can show
// what was read.

// Note records one word read as something else: "tom" → "tomorrow".
type Note struct {
	Word, Meaning string
}

// aliases are shorthand that is not a prefix or a near-miss of the word.
var aliases = map[string]string{
	"tom": "tomorrow", "tmr": "tomorrow", "tmrw": "tomorrow", "tmw": "tomorrow", "tmrow": "tomorrow",
	"2moro": "tomorrow", "2morrow": "tomorrow", "2mrw": "tomorrow",
	"tdy": "today", "2day": "today", "tonite": "tonight", "2nite": "tonight",
	"wknd": "weekend", "wkend": "weekend", "wk": "week", "nxt": "next",
	"arvo": "afternoon", "aft": "afternoon", "eve": "evening", "morn": "morning",
	"til": "until", "till": "until", "untill": "until",
	"mins": "minutes", "min": "minutes", "hr": "hours", "hrs": "hours", "uur": "hours",
	"volgende": "next", "komende": "next", "aankomende": "next", "deze": "this", "dit": "this",
}

// prefixable are the words a unique start of three or more letters may
// stand for, with the shortest start accepted.
var prefixable = map[string]int{
	"today": 3, "tomorrow": 3, "tonight": 4, "overmorgen": 4, "weekend": 5,
	"monday": 4, "tuesday": 4, "wednesday": 4, "thursday": 4, "friday": 4, "saturday": 4, "sunday": 4,
	"maandag": 4, "dinsdag": 4, "woensdag": 4, "donderdag": 4, "vrijdag": 4, "zaterdag": 4, "zondag": 4,
	"january": 4, "february": 4, "march": 4, "april": 4, "june": 4, "july": 4, "august": 4,
	"september": 4, "october": 4, "november": 4, "december": 4,
	"morning": 4, "afternoon": 4, "evening": 5, "every": 4, "until": 3, "weekdays": 5,
}

// wordsNotPrefixes are real words that would otherwise read as the start of
// a day or month: "dag vrij" is a day off, not Friday.
var wordsNotPrefixes = map[string]bool{"vrij": true, "maan": true, "zon": true, "mar": true, "even": true, "ever": true}

// known words are left alone: parser keywords and everything prefixable.
var known = map[string]bool{
	"today": true, "tomorrow": true, "tonight": true, "vandaag": true, "morgen": true, "overmorgen": true,
	"vanavond": true, "vanmiddag": true, "vanochtend": true, "vanmorgen": true,
	"next": true, "this": true, "the": true, "on": true, "at": true, "om": true, "in": true, "for": true, "from": true,
	"to": true, "until": true, "tot": true, "all": true, "day": true, "days": true, "week": true, "weeks": true,
	"month": true, "months": true, "weekend": true, "every": true, "elke": true, "iedere": true, "and": true, "en": true,
	"daily": true, "weekly": true, "monthly": true, "biweekly": true, "weekdays": true, "weekday": true,
	"times": true, "keer": true, "free": true, "noon": true, "midday": true, "midnight": true,
	"morning": true, "afternoon": true, "evening": true, "ochtend": true, "middag": true, "avond": true,
	"hour": true, "hours": true, "minute": true, "minutes": true, "half": true, "an": true, "a": true, "one": true,
	"am": true, "pm": true, "eod": true,
	// "over" would otherwise be read as the start of "overmorgen" and
	// "maand" as the start of "maandag".
	"over": true, "voor": true, "kwart": true,
	"dag": true, "dagen": true, "weken": true, "maand": true, "maanden": true, "uren": true, "minuten": true,
}

func init() {
	for word := range prefixable {
		known[word] = true
	}
	for word := range dutchNumbers {
		known[word] = true
	}
	for word := range weekdays {
		known[word] = true
	}
	for word := range months {
		known[word] = true
	}
}

// normalize returns the word to parse and, when it was rewritten, a note.
func normalize(raw string) (string, *Note) {
	trimmed := strings.Trim(raw, ",.;!?")
	lower := strings.ToLower(trimmed)
	if lower == "" || known[lower] || !isLetters(lower) {
		return lower, nil
	}
	if first := []rune(trimmed)[0]; unicode.IsUpper(first) {
		return lower, nil // a name, or a title word: only exact keywords count
	}
	if meaning, ok := aliases[lower]; ok {
		if quietAliases[meaning] {
			return meaning, nil // till, mins: nothing worth pointing out
		}
		return meaning, &Note{trimmed, meaning}
	}
	// A unique unfinished word: "tomo", "wedn", "septem". Not ordinary words
	// that happen to start a day name ("vrij" free, "maan" moon).
	if len(lower) >= 3 && !wordsNotPrefixes[lower] {
		match := ""
		for word, shortest := range prefixable {
			if len(lower) >= shortest && len(lower) < len(word) && strings.HasPrefix(word, lower) {
				if match != "" {
					match = ""
					break
				}
				match = word
			}
		}
		if match != "" {
			return match, &Note{trimmed, match}
		}
	}
	// A near miss of a long word: "tommorow", "wensday", "firday". Short
	// words are not corrected, so "match" never becomes March.
	if len(lower) >= 6 {
		best, bestDistance, tie := "", 3, false
		for word := range prefixable {
			if len(word) < 6 {
				continue
			}
			limit := 1
			if len(word) >= 8 {
				limit = 2
			}
			distance := editDistance(lower, word)
			if distance > limit {
				continue
			}
			switch {
			case distance < bestDistance:
				best, bestDistance, tie = word, distance, false
			case distance == bestDistance && sense(word) != sense(best):
				tie = true // wednesday and woensdag are one day, not a tie
			case distance == bestDistance && isEnglish(word):
				best = word // the same sense: name it in English, every time
			}
		}
		if best != "" && !tie {
			return best, &Note{trimmed, best}
		}
	}
	return lower, nil
}

// quietAliases are rewrites too ordinary to report.
var quietAliases = map[string]bool{"until": true, "minutes": true, "hours": true, "next": true, "this": true, "week": true}

// sense is what a word means, so English and Dutch names of a day or month
// count as one.
func sense(word string) string {
	if day, ok := weekdays[word]; ok {
		return "day:" + day.String()
	}
	if month, ok := months[word]; ok {
		return "month:" + month.String()
	}
	return word
}

func isEnglish(word string) bool {
	if day, ok := weekdays[word]; ok {
		return strings.ToLower(day.String()) == word
	}
	if month, ok := months[word]; ok {
		return strings.ToLower(month.String()) == word
	}
	return true
}

func isLetters(word string) bool {
	for _, r := range word {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return true
}

// editDistance is the Damerau-Levenshtein distance (optimal string
// alignment): insertions, deletions, substitutions and swapped neighbours.
func editDistance(a, b string) int {
	x, y := []rune(a), []rune(b)
	d := make([][]int, len(x)+1)
	for i := range d {
		d[i] = make([]int, len(y)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(x); i++ {
		for j := 1; j <= len(y); j++ {
			cost := 1
			if x[i-1] == y[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && x[i-1] == y[j-2] && x[i-2] == y[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(x)][len(y)]
}
