package vimedit

import "strings"

// objectKey finishes iw, a", ip and the like: with an operator pending it
// works on the object, in visual mode it selects it.
func (e *Editor) objectKey(kind, tok string) {
	r, ok := e.textObject(kind == "a", tok, max(e.countN(), 1))
	if !ok {
		e.clearPending()
		return
	}
	if e.isVisual() {
		e.selectRegion(r)
		e.clearPending()
		return
	}
	e.applyOperator(e.op, r)
	e.clearPending()
}

var bracketPairs = map[string][2]rune{
	"(": {'(', ')'}, ")": {'(', ')'}, "b": {'(', ')'},
	"[": {'[', ']'}, "]": {'[', ']'},
	"{": {'{', '}'}, "}": {'{', '}'}, "B": {'{', '}'},
	"<": {'<', '>'}, ">": {'<', '>'},
}

func (e *Editor) textObject(around bool, object string, n int) (region, bool) {
	switch object {
	case "w", "W":
		return e.wordObject(around, object == "W", n), true
	case "p":
		return e.paragraphObject(around, n), true
	case `"`, "'", "`":
		return e.quoteObject(around, []rune(object)[0])
	}
	if pair, ok := bracketPairs[object]; ok {
		return e.bracketObject(around, pair[0], pair[1])
	}
	return region{}, false
}

// wordObject is iw and aw. An empty line gives an empty region.
func (e *Editor) wordObject(around, big bool, n int) region {
	row := e.Buf.Row
	line := e.Buf.runes(row)
	if len(line) == 0 {
		return region{start: Pos{row, 0}, end: Pos{row, 0}}
	}
	col := min(e.Buf.Col, len(line)-1)
	kind := func(i int) int { return class(line[i], big) }
	start, end := col, col+1
	for start > 0 && kind(start-1) == kind(col) {
		start--
	}
	for end < len(line) && kind(end) == kind(col) {
		end++
	}
	blanks := func() {
		for end < len(line) && kind(end) == 0 {
			end++
		}
	}
	word := func() {
		if end < len(line) {
			current := kind(end)
			for end < len(line) && kind(end) == current {
				end++
			}
		}
	}
	switch {
	case !around:
		for range n - 1 {
			if end >= len(line) {
				break
			}
			word()
		}
	case kind(col) == 0:
		word() // the blanks and the word after them
	default:
		wordEnd := end
		blanks()
		if end == wordEnd { // nothing after it: take the blanks before instead
			for start > 0 && kind(start-1) == 0 {
				start--
			}
		}
	}
	if around {
		for range n - 1 {
			if end >= len(line) {
				break
			}
			blanks()
			word()
			blanks()
		}
	}
	return region{start: Pos{row, start}, end: Pos{row, end}}
}

// quoteObject is i" and a" (and for ' and `): the quoted text on this line
// around the cursor, or the first quoted text after it. A quote after a
// backslash is not one.
func (e *Editor) quoteObject(around bool, quote rune) (region, bool) {
	row := e.Buf.Row
	line := e.Buf.runes(row)
	var marks []int
	for i, r := range line {
		if r == quote && (i == 0 || line[i-1] != '\\') {
			marks = append(marks, i)
		}
	}
	open, closing := -1, -1
	for i := 0; i+1 < len(marks); i += 2 {
		if e.Buf.Col <= marks[i+1] {
			open, closing = marks[i], marks[i+1]
			break
		}
	}
	if open < 0 {
		return region{}, false
	}
	start, end := open+1, closing
	if around {
		start, end = open, closing+1
		trail := end
		for end < len(line) && (line[end] == ' ' || line[end] == '\t') {
			end++
		}
		if end == trail {
			for start > 0 && (line[start-1] == ' ' || line[start-1] == '\t') {
				start--
			}
		}
	}
	return region{start: Pos{row, start}, end: Pos{row, end}}, true
}

// bracketObject is i( a( and the like, across lines, nesting respected.
func (e *Editor) bracketObject(around bool, open, closing rune) (region, bool) {
	cursor := e.Buf.cursor()
	opening, found := Pos{}, false
	if r, ok := e.charAt(cursor); ok && r == open {
		opening, found = cursor, true
	} else {
		depth := 0
		at := cursor
		for !found {
			var ok bool
			if at, ok = e.stepBack(at); !ok {
				break
			}
			switch r, _ := e.charAt(at); r {
			case closing:
				depth++
			case open:
				if depth == 0 {
					opening, found = at, true
				} else {
					depth--
				}
			}
		}
	}
	if !found {
		return region{}, false
	}
	end, found := Pos{}, false
	depth := 0
	for at := opening; !found; {
		var ok bool
		if at, ok = e.step(at); !ok {
			break
		}
		switch r, _ := e.charAt(at); r {
		case open:
			depth++
		case closing:
			if depth == 0 {
				end, found = at, true
			} else {
				depth--
			}
		}
	}
	if !found {
		return region{}, false
	}
	if around {
		return region{start: opening, end: Pos{end.Row, end.Col + 1}}, true
	}
	// A block written over several lines: its inner part is the lines between.
	if opening.Col == e.Buf.length(opening.Row)-1 && end.Row > opening.Row &&
		strings.TrimSpace(string(e.Buf.runes(end.Row)[:end.Col])) == "" {
		if end.Row == opening.Row+1 {
			at := Pos{opening.Row, e.Buf.length(opening.Row)}
			return region{start: at, end: at}, true
		}
		return e.lineRegion(opening.Row+1, end.Row-1), true
	}
	return region{start: Pos{opening.Row, opening.Col + 1}, end: end}, true
}

// paragraphObject is ip and ap: the lines up to the next blank line, or a
// run of blank lines. ap takes the blank lines that follow too, or those
// before when nothing follows. The count is honoured by ip only.
func (e *Editor) paragraphObject(around bool, n int) region {
	b := &e.Buf
	row, last := b.Row, b.lastRow()
	inBlank := b.blank(row)
	start, end := row, row
	for start > 0 && b.blank(start-1) == inBlank {
		start--
	}
	for end < last && b.blank(end+1) == inBlank {
		end++
	}
	if !around {
		for range n - 1 {
			if end >= last {
				break
			}
			end++
			kind := b.blank(end)
			for end < last && b.blank(end+1) == kind {
				end++
			}
		}
		return e.lineRegion(start, end)
	}
	switch {
	case inBlank && end < last: // the blanks and the paragraph after them
		end++
		for end < last && !b.blank(end+1) {
			end++
		}
	case !inBlank && end < last: // the paragraph and the blanks after it
		end++
		for end < last && b.blank(end+1) {
			end++
		}
	case !inBlank: // the last paragraph: take the blanks before it
		for start > 0 && b.blank(start-1) {
			start--
		}
	}
	return e.lineRegion(start, end)
}
