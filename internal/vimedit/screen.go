package vimedit

import (
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// SetWrapWidth tells the editor how wide the screen draws the body, in cells,
// so gj and gk can move by wrapped line. The host calls it on every resize;
// 0 means unknown, and then a line is one screen line.
func (e *Editor) SetWrapWidth(width int) { e.wrapWidth = max(width, 0) }

func cells(runes []rune) int { return ansi.StringWidth(string(runes)) }

// WrapLine cuts a line into the screen lines the composer's text area draws
// for it: whole words, with the blanks after a word staying on its line, and
// a word wider than the width broken. It is the text area's own algorithm,
// kept here so the editor and the highlights agree with the screen on where
// a line breaks; the segments joined give the line plus one blank at the end.
func WrapLine(runes []rune, width int) [][]rune {
	var (
		lines  = [][]rune{{}}
		word   []rune
		row    int
		spaces int
	)
	blanks := func(n int) []rune {
		out := make([]rune, n)
		for i := range out {
			out[i] = ' '
		}
		return out
	}
	for _, r := range runes {
		if unicode.IsSpace(r) {
			spaces++
		} else {
			word = append(word, r)
		}
		if spaces > 0 {
			if cells(lines[row])+cells(word)+spaces > width {
				row++
				lines = append(lines, []rune{})
			}
			lines[row] = append(append(lines[row], word...), blanks(spaces)...)
			spaces, word = 0, nil
			continue
		}
		// The text area counts the last character twice, so a word exactly as
		// wide as the line already moves to a line of its own. Kept to agree.
		if cells(word)+cells(word[len(word)-1:]) > width {
			if len(lines[row]) > 0 {
				row++
				lines = append(lines, []rune{})
			}
			lines[row] = append(lines[row], word...)
			word = nil
		}
	}
	if cells(lines[row])+cells(word)+spaces >= width {
		lines = append(lines, append(append([]rune{}, word...), blanks(spaces+1)...))
	} else {
		lines[row] = append(append(lines[row], word...), blanks(spaces+1)...)
	}
	return lines
}

// screenRow is one screen line of a buffer row: the runes from start up to,
// not including, end.
type screenRow struct{ start, end int }

// screenRows lists the screen lines of a buffer row. The blank WrapLine adds
// at the end is not text, so a line that fills its last screen line exactly
// has no empty one after it. Without a width the whole row is one.
func (e *Editor) screenRows(row int) []screenRow {
	length := e.Buf.length(row)
	if e.wrapWidth <= 0 || length == 0 {
		return []screenRow{{0, length}}
	}
	var rows []screenRow
	at := 0
	for _, segment := range WrapLine(e.Buf.runes(row), e.wrapWidth) {
		end := min(at+len(segment), length)
		if end > at {
			rows = append(rows, screenRow{at, end})
		}
		at += len(segment)
	}
	if len(rows) == 0 {
		return []screenRow{{0, length}}
	}
	return rows
}

// screenRowOf is the index of the screen line a column is on.
func screenRowOf(rows []screenRow, col int) int {
	for i, r := range rows {
		if col < r.end {
			return i
		}
	}
	return len(rows) - 1
}

// screenColumn is the cell the cursor is at within its screen line.
func (e *Editor) screenColumn(row int, r screenRow, col int) int {
	return cells(e.Buf.runes(row)[r.start:min(max(col, r.start), r.end)])
}

// columnAtCell is the column of the character that covers a cell of a screen
// line, or its last character when the line is shorter, as the cursor rests.
func (e *Editor) columnAtCell(row int, r screenRow, cell int) int {
	line := e.Buf.runes(row)
	col, at := r.start, 0
	for col < r.end-1 {
		next := at + cells(line[col:col+1])
		if next > cell {
			break
		}
		at, col = next, col+1
	}
	return col
}

// screenRowTarget is gj and gk: the same cell on the screen line below or
// above, which for a wrapped line is the next piece of the same buffer row.
func (e *Editor) screenRowTarget(down bool, steps int) (target, bool) {
	row, col := e.Buf.Row, e.Buf.Col
	rows := e.screenRows(row)
	index := screenRowOf(rows, col)
	if e.wantScreen < 0 {
		e.wantScreen = e.screenColumn(row, rows[index], col)
	}
	moved := false
walk:
	for range steps {
		switch {
		case down && index+1 < len(rows):
			index++
		case down && row < e.Buf.lastRow():
			row++
			rows, index = e.screenRows(row), 0
		case !down && index > 0:
			index--
		case !down && row > 0:
			row--
			rows = e.screenRows(row)
			index = len(rows) - 1
		default:
			break walk // the first or last screen line: go as far as possible
		}
		moved = true
	}
	if !moved {
		return target{}, false
	}
	return target{pos: Pos{row, e.columnAtCell(row, rows[index], e.wantScreen)}}, true
}

// screenEdgeTarget is g0 and g$: the first or last character of the screen
// line the cursor is on.
func (e *Editor) screenEdgeTarget(end bool) (target, bool) {
	rows := e.screenRows(e.Buf.Row)
	r := rows[screenRowOf(rows, e.Buf.Col)]
	if end {
		return target{pos: Pos{e.Buf.Row, max(r.end-1, r.start)}, inclusive: true}, true
	}
	return target{pos: Pos{e.Buf.Row, r.start}}, true
}
