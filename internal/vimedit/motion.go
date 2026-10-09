package vimedit

import "unicode"

// target is where a motion leads and how an operator treats the ground
// between: linewise covers whole lines, inclusive covers the character at
// the end, and otherwise the end character is left out.
type target struct {
	pos       Pos
	linewise  bool
	inclusive bool
}

// class sorts a character for word motions: 0 blank, 1 punctuation, 2 word
// characters, and a class of its own for each CJK script, which has no spaces
// between words. With big (W, B, E) everything that is not blank is one class.
func class(r rune, big bool) int {
	switch {
	case unicode.IsSpace(r):
		return 0
	case big:
		return 1
	case r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r):
		switch {
		case unicode.Is(unicode.Han, r):
			return 3
		case unicode.Is(unicode.Hiragana, r):
			return 4
		case unicode.Is(unicode.Katakana, r):
			return 5
		case unicode.Is(unicode.Hangul, r):
			return 6
		}
		return 2
	}
	return 1
}

// classAt is the class at p; the empty line has none and counts as blank.
func (e *Editor) classAt(p Pos, big bool) int {
	line := e.Buf.runes(p.Row)
	if p.Col >= len(line) {
		return 0
	}
	return class(line[p.Col], big)
}

// charAt is the rune at p, if there is one.
func (e *Editor) charAt(p Pos) (rune, bool) {
	line := e.Buf.runes(p.Row)
	if p.Col < 0 || p.Col >= len(line) {
		return 0, false
	}
	return line[p.Col], true
}

// step moves one character on, across line breaks; an empty line is one
// stop. It fails at the end of the text.
func (e *Editor) step(p Pos) (Pos, bool) {
	if p.Col+1 < e.Buf.length(p.Row) {
		return Pos{p.Row, p.Col + 1}, true
	}
	if p.Row < e.Buf.lastRow() {
		return Pos{p.Row + 1, 0}, true
	}
	return p, false
}

func (e *Editor) stepBack(p Pos) (Pos, bool) {
	if p.Col > 0 {
		return Pos{p.Row, min(p.Col-1, max(e.Buf.length(p.Row)-1, 0))}, true
	}
	if p.Row > 0 {
		return Pos{p.Row - 1, max(e.Buf.length(p.Row-1)-1, 0)}, true
	}
	return p, false
}

func (e *Editor) lastChar() Pos {
	row := e.Buf.lastRow()
	return Pos{row, max(e.Buf.length(row)-1, 0)}
}

// wordForward is w: the start of the next word. An empty line is a word.
// hitEnd says the text ended first.
func (e *Editor) wordForward(p Pos, big bool) (at Pos, hitEnd bool) {
	current := e.classAt(p, big)
	q := p
	switch {
	case current != 0:
		for {
			next, ok := e.step(q)
			if !ok {
				return e.lastChar(), true
			}
			crossed := next.Row != q.Row
			q = next
			if crossed || e.classAt(q, big) != current {
				break
			}
		}
	case e.Buf.length(p.Row) == 0:
		next, ok := e.step(p)
		if !ok {
			return e.lastChar(), true
		}
		q = next
	}
	for e.classAt(q, big) == 0 && e.Buf.length(q.Row) > 0 {
		next, ok := e.step(q)
		if !ok {
			return e.lastChar(), true
		}
		q = next
	}
	return q, false
}

// wordEnd is e: the end of this word, or of the next when already on an end.
func (e *Editor) wordEnd(p Pos, big bool) (at Pos, hitEnd bool) {
	q, ok := e.step(p)
	if !ok {
		return p, true
	}
	for e.classAt(q, big) == 0 {
		next, ok := e.step(q)
		if !ok {
			return q, true
		}
		q = next
	}
	current := e.classAt(q, big)
	for {
		next, ok := e.step(q)
		if !ok || next.Row != q.Row || e.classAt(next, big) != current {
			return q, false
		}
		q = next
	}
}

// wordEndHere is the end of the word the cursor is in, which cw changes to.
func (e *Editor) wordEndHere(p Pos, big bool) Pos {
	current := e.classAt(p, big)
	for p.Col+1 < e.Buf.length(p.Row) && e.classAt(Pos{p.Row, p.Col + 1}, big) == current {
		p.Col++
	}
	return p
}

// wordBack is b: the start of this word, or of the one before when on a start.
func (e *Editor) wordBack(p Pos, big bool) Pos {
	q, ok := e.stepBack(p)
	if !ok {
		return p
	}
	for e.classAt(q, big) == 0 && e.Buf.length(q.Row) > 0 {
		next, ok := e.stepBack(q)
		if !ok {
			return q
		}
		q = next
	}
	current := e.classAt(q, big)
	for q.Col > 0 && e.classAt(Pos{q.Row, q.Col - 1}, big) == current {
		q.Col--
	}
	return q
}

func (e *Editor) paragraphForward(row int) (at Pos, hitEnd bool) {
	last := e.Buf.lastRow()
	for row < last && e.Buf.blank(row) {
		row++
	}
	for row < last && !e.Buf.blank(row) {
		row++
	}
	if e.Buf.blank(row) {
		return Pos{row, 0}, false
	}
	return e.lastChar(), true
}

func (e *Editor) paragraphBack(row int) Pos {
	for row > 0 && e.Buf.blank(row) {
		row--
	}
	for row > 0 && !e.Buf.blank(row) {
		row--
	}
	return Pos{row, 0}
}

// findChar is f, F, t, T and, with again, ; and ,. A repeated t or T steps
// past the character it already stands before, so ; keeps moving.
func (e *Editor) findChar(kind string, char rune, n int, again bool) (target, bool) {
	line := e.Buf.runes(e.Buf.Row)
	forward := kind == "f" || kind == "t"
	till := kind == "t" || kind == "T"
	direction := 1
	if !forward {
		direction = -1
	}
	i := e.Buf.Col
	if again && till && n <= 1 {
		if next := i + direction; next >= 0 && next < len(line) && line[next] == char {
			i = next
		}
	}
	for range max(n, 1) {
		for {
			i += direction
			if i < 0 || i >= len(line) {
				return target{}, false
			}
			if line[i] == char {
				break
			}
		}
	}
	if till {
		i -= direction
	}
	return target{pos: Pos{e.Buf.Row, i}, inclusive: forward}, true
}

// runMotion moves the cursor, or applies the pending operator to the ground
// the motion covers.
func (e *Editor) runMotion(name string, char rune) {
	n := e.countN()
	cursor := e.Buf.cursor()
	var t target
	var ok bool
	if e.op == "c" && (name == "w" || name == "W") && e.classAt(cursor, name == "W") != 0 {
		// cw changes the word, not the blanks after it, as ce would.
		big := name == "W"
		end := e.wordEndHere(cursor, big)
		for range max(n, 1) - 1 {
			end, _ = e.wordEnd(end, big)
		}
		t, ok = target{pos: end, inclusive: true}, true
	} else {
		t, ok = e.motionTarget(name, char, n, e.op != "")
	}
	if !ok {
		e.macroAbort = e.macroAbort || e.macroDepth > 0 // a failed motion ends a macro
		e.clearPending()
		return
	}
	if name == "j" || name == "k" {
		e.keepWant = true
	}
	if name == "gj" || name == "gk" {
		e.keepScreen = true
	}
	if name == "$" {
		e.keepWant, e.want = true, endOfLine
	}
	if e.op == "" {
		e.Buf.setCursor(t.pos)
		e.clearPending()
		return
	}
	e.operateOver(t)
	e.clearPending()
}

// operateOver applies the pending operator between the cursor and a motion's
// target.
func (e *Editor) operateOver(t target) {
	start, end := e.Buf.cursor(), t.pos
	if end.before(start) {
		start, end = end, start
	}
	if t.linewise {
		e.applyOperator(e.op, e.lineRegion(start.Row, end.Row))
		return
	}
	r := region{start: start, end: end}
	switch {
	case t.inclusive:
		r.end.Col = min(end.Col+1, e.Buf.length(end.Row))
	case end.Col == 0 && end.Row > start.Row:
		// An exclusive motion that ends at the start of a line stops at the
		// end of the line before; from the line's start it covers whole lines.
		r.end = Pos{end.Row - 1, e.Buf.length(end.Row - 1)}
		if start.Col <= e.Buf.indent(start.Row) {
			r = e.lineRegion(start.Row, r.end.Row)
		}
	}
	e.applyOperator(e.op, r)
}

// motionTarget works out where a motion leads from the cursor. n is the
// count, 0 when none. It fails when the motion cannot move, which also
// cancels a pending operator.
func (e *Editor) motionTarget(name string, char rune, n int, forOp bool) (target, bool) {
	b := &e.Buf
	cursor := b.cursor()
	steps := max(n, 1)
	length := b.length(cursor.Row)
	switch name {
	case "h":
		if cursor.Col == 0 {
			return target{}, false
		}
		return target{pos: Pos{cursor.Row, max(cursor.Col-steps, 0)}}, true
	case "l":
		limit := max(length-1, 0)
		if forOp {
			limit = length
		}
		if cursor.Col >= limit {
			return target{}, false
		}
		return target{pos: Pos{cursor.Row, min(cursor.Col+steps, limit)}}, true
	case "j", "k":
		row := min(cursor.Row+steps, b.lastRow())
		if name == "k" {
			row = max(cursor.Row-steps, 0)
		}
		if row == cursor.Row {
			return target{}, false
		}
		return target{pos: Pos{row, min(e.want, max(b.length(row)-1, 0))}, linewise: true}, true
	case "gj", "gk":
		return e.screenRowTarget(name == "gj", steps)
	case "g0", "g$":
		return e.screenEdgeTarget(name == "g$")
	case "0":
		return target{pos: Pos{cursor.Row, 0}}, true
	case "^":
		return target{pos: Pos{cursor.Row, b.firstNonBlank(cursor.Row)}}, true
	case "$":
		row := cursor.Row + steps - 1
		if row > b.lastRow() {
			return target{}, false
		}
		return target{pos: Pos{row, max(b.length(row)-1, 0)}, inclusive: true}, true
	case "gg", "G":
		row := b.lastRow()
		if name == "gg" {
			row = 0
		}
		if n > 0 {
			row = min(n-1, b.lastRow())
		}
		return target{pos: Pos{row, b.firstNonBlank(row)}, linewise: true}, true
	case "w", "W":
		return e.wordForwardTarget(name == "W", steps, forOp), true
	case "e", "E":
		at := cursor
		for range steps {
			at, _ = e.wordEnd(at, name == "E")
		}
		return target{pos: at, inclusive: true}, true
	case "b", "B":
		at := cursor
		for range steps {
			at = e.wordBack(at, name == "B")
		}
		return target{pos: at}, true
	case "}":
		at := cursor
		for range steps {
			var hit bool
			if at, hit = e.paragraphForward(at.Row); hit {
				if forOp {
					at = Pos{at.Row, b.length(at.Row)}
				}
				break
			}
		}
		return target{pos: at}, true
	case "{":
		at := cursor
		for range steps {
			at = e.paragraphBack(at.Row)
		}
		return target{pos: at}, true
	case "f", "F", "t", "T":
		return e.findChar(name, char, n, false)
	case "n", "N", "*", "#":
		return e.searchTarget(name, n)
	case ";", ",":
		kind := e.lastFnd.kind
		if kind == "" {
			return target{}, false
		}
		if name == "," {
			kind = map[string]string{"f": "F", "F": "f", "t": "T", "T": "t"}[kind]
		}
		return e.findChar(kind, e.lastFnd.char, n, true)
	}
	return target{}, false
}

// wordForwardTarget is w with its operator special case: when the last word
// moved over ends its line, an operator stops there instead of eating the
// line break and the next word.
func (e *Editor) wordForwardTarget(big bool, steps int, forOp bool) target {
	at := e.Buf.cursor()
	for i := range steps {
		next, hit := e.wordForward(at, big)
		if forOp && hit {
			row := e.Buf.lastRow()
			return target{pos: Pos{row, e.Buf.length(row)}}
		}
		if forOp && i == steps-1 && next.Row > at.Row {
			return target{pos: Pos{at.Row, e.Buf.length(at.Row)}}
		}
		at = next
	}
	return target{pos: at}
}
