package vimedit

import (
	"slices"
	"strings"
	"unicode/utf8"
)

// Pos is a place in the buffer. Col counts runes, never bytes, so Korean and
// other multibyte text is edited a character at a time.
type Pos struct{ Row, Col int }

func (p Pos) before(q Pos) bool {
	return p.Row < q.Row || (p.Row == q.Row && p.Col < q.Col)
}

// Buffer is the text being edited: one string per line and the cursor.
type Buffer struct {
	Lines    []string
	Row, Col int

	cache []rowRunes
}

// rowRunes remembers a line's runes. It is keyed on the line's text, so a
// stale entry is never used even when Lines is replaced wholesale.
type rowRunes struct {
	text  string
	runes []rune
	ok    bool
}

// runes returns a line as runes. Callers must not modify the slice.
func (b *Buffer) runes(row int) []rune {
	if len(b.cache) != len(b.Lines) {
		b.cache = make([]rowRunes, len(b.Lines))
	}
	entry := &b.cache[row]
	if !entry.ok || entry.text != b.Lines[row] {
		entry.text, entry.runes, entry.ok = b.Lines[row], []rune(b.Lines[row]), true
	}
	return entry.runes
}

func (b *Buffer) length(row int) int { return len(b.runes(row)) }

func (b *Buffer) lastRow() int { return len(b.Lines) - 1 }

func (b *Buffer) text() string { return strings.Join(b.Lines, "\n") }

func (b *Buffer) setText(text string) { b.Lines = strings.Split(text, "\n") }

func (b *Buffer) cursor() Pos { return Pos{b.Row, b.Col} }

func (b *Buffer) setCursor(p Pos) { b.Row, b.Col = p.Row, p.Col }

// slice is the text from start up to, not including, end.
func (b *Buffer) slice(start, end Pos) string {
	if start.Row == end.Row {
		line := b.runes(start.Row)
		return string(line[min(start.Col, len(line)):min(end.Col, len(line))])
	}
	first := b.runes(start.Row)
	parts := []string{string(first[min(start.Col, len(first)):])}
	parts = append(parts, b.Lines[start.Row+1:end.Row]...)
	last := b.runes(end.Row)
	return strings.Join(append(parts, string(last[:min(end.Col, len(last))])), "\n")
}

// deleteRange removes the text from start up to, not including, end.
func (b *Buffer) deleteRange(start, end Pos) {
	head := b.runes(start.Row)[:min(start.Col, b.length(start.Row))]
	tail := b.runes(end.Row)[min(end.Col, b.length(end.Row)):]
	joined := string(head) + string(tail)
	b.Lines = slices.Delete(b.Lines, start.Row+1, end.Row+1)
	b.Lines[start.Row] = joined
}

// insertAt puts text, which may hold line breaks, at p and returns the
// position just after it.
func (b *Buffer) insertAt(p Pos, text string) Pos {
	line := b.runes(p.Row)
	col := min(p.Col, len(line))
	head, tail := string(line[:col]), string(line[col:])
	parts := strings.Split(text, "\n")
	if len(parts) == 1 {
		b.Lines[p.Row] = head + text + tail
		return Pos{p.Row, col + utf8.RuneCountInString(text)}
	}
	replacement := make([]string, len(parts))
	copy(replacement, parts)
	replacement[0] = head + parts[0]
	replacement[len(parts)-1] = parts[len(parts)-1] + tail
	b.Lines = slices.Replace(b.Lines, p.Row, p.Row+1, replacement...)
	return Pos{p.Row + len(parts) - 1, utf8.RuneCountInString(parts[len(parts)-1])}
}

// deleteLines removes rows first to last, inclusive. A buffer is never left
// without a line.
func (b *Buffer) deleteLines(first, last int) {
	b.Lines = slices.Delete(b.Lines, first, last+1)
	if len(b.Lines) == 0 {
		b.Lines = []string{""}
	}
}

func (b *Buffer) insertLines(at int, lines []string) {
	b.Lines = slices.Insert(b.Lines, at, lines...)
}

// indent is the number of blanks that start a line.
func (b *Buffer) indent(row int) int {
	line := b.runes(row)
	for i, r := range line {
		if r != ' ' && r != '\t' {
			return i
		}
	}
	return len(line)
}

// firstNonBlank is where ^ lands: the first non-blank, or the last character
// of a line of blanks.
func (b *Buffer) firstNonBlank(row int) int {
	return min(b.indent(row), max(b.length(row)-1, 0))
}

func (b *Buffer) blank(row int) bool { return strings.TrimSpace(b.Lines[row]) == "" }
