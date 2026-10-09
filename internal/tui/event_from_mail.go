package tui

// E in the reader opens the quick-add box filled from the message: the
// subject as the title, the first date or time written in the body, and the
// sender as the guest. The user edits it and presses enter as usual.

import (
	"github.com/aronvandepol/mailday/internal/compose"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/aronvandepol/mailday/internal/htmlutil"
	"github.com/aronvandepol/mailday/internal/maildir"
	"github.com/aronvandepol/mailday/internal/when"
)

// maxScannedSentences keeps a long newsletter from being parsed end to end.
const maxScannedSentences = 300

var (
	replyPrefix = regexp.MustCompile(`(?i)^\s*(re|fwd?|aw|antw|wg|doorst|tr|sv|vs)\s*:\s*`)
	bulletLine  = regexp.MustCompile(`^\s*([-*•]|\d+[.)])\s`)
)

// eventFromMail is E in the reader.
func (m Model) eventFromMail() (tea.Model, tea.Cmd) {
	if m.content == nil || !m.readerReady() {
		return m, nil
	}
	if len(m.writableCalendars) == 0 {
		m.status = "Calendar editing needs mailday-calendar (make install)"
		return m, nil
	}
	m.calPrompt = &calendarPrompt{kind: promptNew, input: m.eventLineFromMail(*m.content)}
	m.pendingDelete = ""
	m.status = ""
	return m, nil
}

// eventLineFromMail is the quick-add line for a message. A part the parser
// would not accept is left out rather than shown as an error.
func (m Model) eventLineFromMail(content maildir.Content) string {
	title := m.literalTitle(eventTitle(content.Subject))
	phrase := m.firstWhenPhrase(content)
	guest := ""
	// Not yourself (your own sent mail) and not a robot.
	if address := strings.TrimSpace(content.FromAddr); isAddress(address) && !compose.IsOwn(address) && !looksAutomated(address) {
		guest = "+" + address
	}
	join := func(parts ...string) string {
		var kept []string
		for _, part := range parts {
			if part != "" {
				kept = append(kept, part)
			}
		}
		return strings.Join(kept, " ")
	}
	line := join(title, phrase, guest)
	if _, _, err := m.draftEvent(line); err != nil && phrase != "" {
		line = join(title, guest)
	}
	return line
}

func isAddress(text string) bool {
	at := strings.Index(text, "@")
	return at > 0 && strings.Contains(text[at:], ".") && !strings.ContainsAny(text, " \t,;<>")
}

// eventTitle is the subject without Re:, Fwd: and their translations.
func eventTitle(subject string) string {
	subject = strings.TrimSpace(subject)
	for {
		stripped := replyPrefix.ReplaceAllString(subject, "")
		if stripped == subject {
			return strings.Join(strings.Fields(subject), " ")
		}
		subject = stripped
	}
}

// literalTitle makes a title safe to put in the quick-add line, where @ # +
// and words like "friday" mean something: leading marks go and words the
// parser would read as a date, time or length are quoted to stay words.
func (m Model) literalTitle(title string) string {
	var words []string
	for _, word := range strings.Fields(title) {
		word = strings.Trim(word, `"`)
		if len(word) > 1 {
			word = strings.TrimLeft(word, "@#+")
		}
		if word != "" && word != "@" && word != "#" && word != "+" {
			words = append(words, word)
		}
	}
	parsed, err := when.Parse(strings.Join(words, " "), m.now())
	if err != nil || len(parsed.Roles) != len(words) {
		for index, word := range words {
			words[index] = `"` + word + `"`
		}
		return strings.Join(words, " ")
	}
	for index, role := range parsed.Roles {
		if role != "title" {
			words[index] = `"` + words[index] + `"`
		}
	}
	return strings.Join(words, " ")
}

// firstWhenPhrase is the first date or time written in the body that is not
// in the past, as words the quick-add line reads back; empty when there is
// none. Relative words ("tomorrow", "fri") count from the day the message was
// sent, and an old message gets the date spelled out so it still means the
// same day.
func (m Model) firstWhenPhrase(content maildir.Content) string {
	now := m.now()
	sent := now
	if !content.Date.IsZero() && now.Sub(content.Date) > 12*time.Hour {
		sent = content.Date.In(now.Location())
	}
	for _, sentence := range bodySentences(content) {
		parsed, err := when.Parse(sentence, sent)
		if err != nil || !(parsed.HasDate || parsed.HasTime) || parsed.Repeat != nil || parsed.Free {
			continue
		}
		if parsed.HasDate && !parsedDayIsUpcoming(parsed, now) {
			continue
		}
		phrase := phraseWords(sentence, parsed, sent, now)
		if phrase == "" {
			continue
		}
		// What goes in the box must read back as the same thing.
		again, err := when.Parse(phrase, now)
		if err != nil || again.HasDate != parsed.HasDate || again.HasTime != parsed.HasTime {
			continue
		}
		return phrase
	}
	return ""
}

// parsedDayIsUpcoming: today counts until its time has passed.
func parsedDayIsUpcoming(parsed when.Parsed, now time.Time) bool {
	last := parsed.Date.AddDate(0, 0, 1) // end of that day
	if parsed.HasTime {
		last = parsed.Date.Add(parsed.Clock)
	}
	return !last.Before(now)
}

// phraseWords picks the words of a sentence that were read as date, time or
// length, with the little words between them ("friday at 14:00"). For an old
// message the date words become one ISO date.
func phraseWords(sentence string, parsed when.Parsed, sent, now time.Time) string {
	words := strings.Fields(sentence)
	if len(parsed.Roles) != len(words) {
		return ""
	}
	core := func(role string) bool {
		return role == "date" || role == "time" || role == "length" || role == "allday"
	}
	first, last := -1, -1
	for index, role := range parsed.Roles {
		if core(role) {
			if first < 0 {
				first = index
			}
			last = index
		}
	}
	if first < 0 {
		return ""
	}
	spelled := !sent.Equal(now) && parsed.HasDate
	var kept []string
	dated := false
	for index := first; index <= last; index++ {
		switch role := parsed.Roles[index]; {
		case role == "date" && spelled:
			if !dated {
				kept = append(kept, parsed.Date.Format("2006-01-02"))
				dated = true
			}
		case core(role) || role == "filler":
			kept = append(kept, words[index])
		}
	}
	return strings.Join(kept, " ")
}

// bodySentences are the sentences of what the sender wrote, without quoted
// replies and signature, with each word stripped of the punctuation around it.
func bodySentences(content maildir.Content) []string {
	body := content.Body
	if strings.TrimSpace(body) == "" && strings.TrimSpace(content.HTML) != "" {
		body = htmlutil.ToMarkdown(content.HTML).String()
	}
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	if start := plainHistoryIndex(lines); start > 0 && strings.TrimSpace(strings.Join(lines[:start], "")) != "" {
		lines = lines[:start]
	}
	var units []string
	var open string
	flush := func() {
		if open != "" {
			units = append(units, open)
			open = ""
		}
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case line == "-- ":
			flush()
			return sentencesOf(units)
		case trimmed == "" || strings.HasPrefix(trimmed, ">"):
			flush()
		case open != "" && !bulletLine.MatchString(line) && !strings.ContainsAny(open[len(open)-1:], ".!?:;"):
			open += " " + trimmed // a wrapped line continues the sentence
		default:
			flush()
			open = trimmed
		}
	}
	flush()
	return sentencesOf(units)
}

func sentencesOf(units []string) []string {
	var sentences []string
	for _, unit := range units {
		for _, part := range splitSentences(unit) {
			if cleaned := cleanSentence(part); cleaned != "" {
				sentences = append(sentences, cleaned)
			}
			if len(sentences) >= maxScannedSentences {
				return sentences
			}
		}
	}
	return sentences
}

// splitSentences cuts after . ! or ? only when a capital follows: "Fri. 12
// Nov" and "at 5. then" stay whole.
func splitSentences(text string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(text); i++ {
		if !strings.ContainsRune(".!?", rune(text[i])) {
			continue
		}
		end := i + 1
		for end < len(text) && strings.ContainsRune(".!?", rune(text[end])) {
			end++
		}
		next := end
		for next < len(text) && (text[next] == ' ' || text[next] == '\t') {
			next++
		}
		if next > end && next < len(text) {
			if r, _ := utf8.DecodeRuneInString(text[next:]); unicode.IsUpper(r) {
				parts = append(parts, text[start:end])
				start = next
			}
		}
		i = end - 1
	}
	return append(parts, text[start:])
}

const marks = "()[]{}<>,;:!?\"'*_`~"

// cleanSentence takes brackets, commas and markdown marks off the words, so
// "(Friday," reads as Friday.
func cleanSentence(sentence string) string {
	var words []string
	for _, word := range strings.Fields(sentence) {
		word = strings.TrimRight(strings.TrimLeft(word, marks), marks+".")
		if word != "" {
			words = append(words, word)
		}
	}
	return strings.Join(words, " ")
}

func looksAutomated(address string) bool {
	local := strings.ToLower(address)
	if at := strings.Index(local, "@"); at >= 0 {
		local = local[:at]
	}
	for _, word := range []string{"noreply", "no-reply", "donotreply", "do-not-reply", "notification", "mailer-daemon", "bounce"} {
		if strings.Contains(local, word) {
			return true
		}
	}
	return false
}
