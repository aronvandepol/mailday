package tui

// Group chips in To, Cc and Bcc.
//
// Choosing a group there inserts one chip, "◆ KH (4)", instead of its four
// addresses. The field value stays text, so typing, drafts and the rest of the
// composer need no new type: a chip is the token "◆[KH]". That can never be an
// address (no @, and a mail address list does not parse it), and a group name
// holds no "]" (the name prompt refuses one), so it is found without
// ambiguity even when the name has a comma or spaces.
//
// collect() expands chips to the group's members as they are now, once each
// and after every address typed in any field, so drafts, the preview, the send
// checks and msmtp only ever see real addresses. A chip whose group has gone
// stays a token in the draft (nobody is lost silently) and finishForm refuses
// it. A saved draft reopens as plain addresses.

import (
	"fmt"
	"net/mail"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/aronvandepol/mailday/internal/groups"
	"github.com/aronvandepol/mailday/internal/terminal"
)

const (
	chipOpen  = "◆["
	chipClose = "]"
)

// recipientFields are the fields that take chips, in the order they are
// resolved: an address in an earlier field is not repeated by a later chip.
var recipientFields = [3]int{fieldTo, fieldCc, fieldBcc}

func chipToken(name string) string { return chipOpen + name + chipClose }

// chipSpan is where a chip token sits in a field value.
type chipSpan struct {
	start, end int
	name       string
}

func chipSpans(value string) []chipSpan {
	var spans []chipSpan
	for at := 0; at < len(value); {
		open := strings.Index(value[at:], chipOpen)
		if open < 0 {
			break
		}
		open += at
		nameStart := open + len(chipOpen)
		closing := strings.Index(value[nameStart:], chipClose)
		if closing < 0 {
			break
		}
		end := nameStart + closing + len(chipClose)
		spans = append(spans, chipSpan{start: open, end: end, name: value[nameStart : end-len(chipClose)]})
		at = end
	}
	return spans
}

// lastComma is the last comma that separates entries: one inside a chip's
// name does not count. -1 when there is none.
func lastComma(value string) int {
	spans := chipSpans(value)
	for index := len(value) - 1; index >= 0; index-- {
		if value[index] != ',' {
			continue
		}
		inside := false
		for _, span := range spans {
			if index > span.start && index < span.end {
				inside = true
				break
			}
		}
		if !inside {
			return index
		}
	}
	return -1
}

// withoutChips drops every chip token, leaving a comma where it was.
func withoutChips(value string) string {
	var out strings.Builder
	last := 0
	for _, span := range chipSpans(value) {
		out.WriteString(value[last:span.start] + ",")
		last = span.end
	}
	return out.String() + value[last:]
}

// splitEntries cuts a recipient list at the commas outside quotes and angle
// brackets, so "Kim, A." <a@x.org> stays one entry.
func splitEntries(text string) []string {
	var parts []string
	start := 0
	quoted, escaped, angled := false, false, false
	for index, r := range text {
		switch {
		case escaped:
			escaped = false
		case quoted && r == '\\':
			escaped = true
		case r == '"':
			quoted = !quoted
		case quoted:
		case r == '<':
			angled = true
		case r == '>':
			angled = false
		case r == ',' && !angled:
			parts = append(parts, text[start:index])
			start = index + 1
		}
	}
	return append(parts, text[start:])
}

// recipientsIn lists the addresses typed in a value, chips aside. An entry
// still being typed is skipped rather than failing the whole list.
func recipientsIn(value string) []mail.Address {
	var out []mail.Address
	for _, entry := range splitEntries(withoutChips(value)) {
		if entry = strings.TrimSpace(entry); entry == "" {
			continue
		}
		if address, err := mail.ParseAddress(entry); err == nil {
			out = append(out, *address)
		}
	}
	return out
}

// chipView is one chip as resolved against the groups now: who it adds once
// the addresses typed elsewhere and earlier chips are taken out.
type chipView struct {
	name    string
	members []mail.Address
	missing bool
}

func (c chipView) label() string {
	if c.missing {
		return "◆ " + c.name + " (gone)"
	}
	return fmt.Sprintf("◆ %s (%d)", c.name, len(c.members))
}

// resolve expands the chips of To, Cc and Bcc. texts are the field values with
// each chip replaced by its addresses (a missing group's token stays); chips
// say what each chip came to.
func (f *headerForm) resolve() (texts [3]string, chips [3][]chipView) {
	seen := map[string]bool{}
	for _, field := range recipientFields {
		for _, address := range recipientsIn(f.values[field]) {
			seen[strings.ToLower(address.Address)] = true
		}
	}
	for index, field := range recipientFields {
		value := f.values[field]
		var out strings.Builder
		last := 0
		for _, span := range chipSpans(value) {
			out.WriteString(value[last:span.start])
			last = span.end
			at := groups.Find(f.groups, span.name)
			if at < 0 {
				chips[index] = append(chips[index], chipView{name: span.name, missing: true})
				out.WriteString(value[span.start:span.end])
				continue
			}
			view := chipView{name: f.groups[at].Name}
			var names []string
			for _, member := range f.groups[at].Members {
				if key := strings.ToLower(member.Address); !seen[key] {
					seen[key] = true
					view.members = append(view.members, member)
					names = append(names, groups.FormatMember(member))
				}
			}
			chips[index] = append(chips[index], view)
			if len(names) == 0 {
				last = skipSeparators(value, last) // nothing to add: no empty entry left behind
				continue
			}
			out.WriteString(strings.Join(names, ", "))
		}
		out.WriteString(value[last:])
		texts[index] = out.String()
	}
	return texts, chips
}

func skipSeparators(value string, at int) int {
	for at < len(value) && (value[at] == ',' || value[at] == ' ') {
		at++
	}
	return at
}

// chipMembers are the addresses of the chips in value, for chosen().
func (f *headerForm) chipMembers(value string) []mail.Address {
	var out []mail.Address
	for _, span := range chipSpans(value) {
		if at := groups.Find(f.groups, span.name); at >= 0 {
			out = append(out, f.groups[at].Members...)
		}
	}
	return out
}

// missingChip is the first chip whose group no longer exists, and its field.
func (f *headerForm) missingChip() (name string, field int, ok bool) {
	_, chips := f.resolve()
	for index, list := range chips {
		for _, chip := range list {
			if chip.missing {
				return chip.name, recipientFields[index], true
			}
		}
	}
	return "", 0, false
}

// hasChip says whether the focused field holds a chip.
func (f *headerForm) hasChip() bool {
	return isRecipientField(f.field) && len(chipSpans(f.values[f.field])) > 0
}

// removeChipAtEnd drops the last chip when nothing but separators follows it:
// backspace right after a chip takes the whole chip, not a bracket of it.
func removeChipAtEnd(value string) (string, string, bool) {
	spans := chipSpans(value)
	if len(spans) == 0 {
		return value, "", false
	}
	last := spans[len(spans)-1]
	if strings.Trim(value[last.end:], ", ") != "" {
		return value, "", false
	}
	return value[:last.start], last.name, true
}

// expandLastChip turns the last chip of the focused field into its addresses.
// It says what happened for the status line.
func (f *headerForm) expandLastChip() string {
	value := f.values[f.field]
	spans := chipSpans(value)
	if len(spans) == 0 {
		return ""
	}
	_, chips := f.resolve()
	var views []chipView
	for index, field := range recipientFields {
		if field == f.field {
			views = chips[index]
		}
	}
	last, view := spans[len(spans)-1], views[len(spans)-1]
	if view.missing {
		return "No group " + view.name + " any more: backspace removes it"
	}
	names := make([]string, len(view.members))
	for index, member := range view.members {
		names[index] = groups.FormatMember(member)
	}
	end := last.end
	text := strings.Join(names, ", ")
	if text == "" {
		end = skipSeparators(value, end)
	}
	f.values[f.field] = value[:last.start] + text + value[end:]
	return fmt.Sprintf("%s: %s are plain addresses now", view.name, people(len(view.members)))
}

// deleteWord is ctrl+w in a recipient field: a chip goes whole, anything else
// loses its last word without reaching into an earlier chip.
func deleteWord(value string) string {
	if shorter, _, ok := removeChipAtEnd(value); ok {
		return shorter
	}
	start := 0
	if spans := chipSpans(value); len(spans) > 0 {
		start = spans[len(spans)-1].end
	}
	tail := strings.TrimRight(value[start:], " ,")
	if index := strings.LastIndexAny(tail, ", "); index >= 0 {
		return value[:start] + tail[:index+1]
	}
	return value[:start]
}

// ---- drawing ----

// chipStyle is the pill: the tab pill's colours, the error colour when the
// group is gone.
func chipStyle(missing bool) lipgloss.Style {
	style := selectedPillStyle()
	if missing && !noColor {
		style = style.Background(colorError)
	}
	return style
}

// recipientUnit is a word or a chip: the least that is never split across lines.
type recipientUnit struct {
	text    string // as drawn
	width   int
	members int // addresses a chip adds
	at      int // "@" signs in a word, for the "… N more" count
}

// recipientFieldLines draws a To, Cc or Bcc value too long for one line, or
// holding chips. Words wrap; a chip is one unit and never breaks.
func recipientFieldLines(label, text string, room int, focused bool, value lipgloss.Style, cursor string, chips []chipView) []string {
	var units []recipientUnit
	last, chipIndex := 0, 0
	addWords := func(part string) {
		for _, word := range strings.Fields(terminal.SanitizeLine(part)) {
			units = append(units, recipientUnit{text: value.Render(word), width: displayWidth(word), at: strings.Count(word, "@")})
		}
	}
	for _, span := range chipSpans(text) {
		addWords(text[last:span.start])
		last = skipSeparators(text, span.end) // the pill needs no comma after it
		view := chipView{name: span.name, missing: true}
		if chipIndex < len(chips) {
			view = chips[chipIndex]
		}
		chipIndex++
		pill := " " + truncateToWidth(terminal.SanitizeLine(view.label()), max(room-2, 1)) + " "
		unit := recipientUnit{text: chipStyle(view.missing).Render(pill), width: displayWidth(pill)}
		if !view.missing {
			unit.members = len(view.members)
		}
		units = append(units, unit)
	}
	addWords(text[last:])

	var rows [][]recipientUnit
	var row []recipientUnit
	used := 0
	for _, unit := range units {
		if len(row) > 0 && used+1+unit.width > room {
			rows, row, used = append(rows, row), nil, 0
		}
		if len(row) > 0 {
			used++
		}
		row = append(row, unit)
		used += unit.width
	}
	rows = append(rows, row)

	limit := 3
	if focused {
		limit = 5
	}
	hidden, hiddenPeople := 0, 0
	if len(rows) > limit {
		hidden = len(rows) - limit + 1
		for _, folded := range rows[:hidden] {
			for _, unit := range folded {
				hiddenPeople += unit.at + unit.members
			}
		}
		rows = rows[hidden:]
	}
	count := len(recipientsIn(text))
	for _, view := range chips {
		count += len(view.members)
	}

	out := make([]string, 0, len(rows)+1)
	if hidden > 0 {
		out = append(out, styleMuted.Render(fmt.Sprintf("… %d more", hiddenPeople)))
	}
	for _, row := range rows {
		parts := make([]string, len(row))
		for index, unit := range row {
			parts[index] = unit.text
		}
		out = append(out, strings.Join(parts, " "))
	}
	for index := range out {
		prefix := strings.Repeat(" ", 9)
		if index == 0 {
			prefix = label
		}
		out[index] = prefix + out[index]
	}
	if focused {
		if strings.HasSuffix(text, " ") || strings.HasSuffix(text, ",") {
			out[len(out)-1] += " " // the comma and space typed after the last entry
		}
		out[len(out)-1] += cursor
	}
	if count > 1 {
		out[0] = fillBetween(out[0], styleMuted.Render(people(count)), room+9)
	}
	return out
}

// headerFooterBindings are the keys under the To, Subject and Attach fields;
// a chip adds the two that act on it.
func (f *headerForm) headerFooterBindings() []helpBinding {
	bindings := []helpBinding{{"type", "search people or files"}, {"↑↓", "choose"}, {"tab/enter", "add or next"}, {"←→", "from"}, {"ctrl+s", "preview & send"}, {"ctrl+o", "$EDITOR"}, {"esc", "close"}}
	if f.hasChip() {
		bindings = append([]helpBinding{{"backspace", "remove group"}, {"ctrl+e", "show people"}}, bindings...)
	}
	return bindings
}
