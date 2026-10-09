package vimedit

import "unicode"

// beginInsert starts an insert session; the caller has set the mode. n is
// the count of 3ifoo<esc>, which types foo three times.
func (e *Editor) beginInsert(kind string, n int) {
	e.insertKind, e.insertCount, e.typed, e.typedOK = kind, max(n, 1), nil, true
}

func (e *Editor) insertKey(key, text string) {
	switch key {
	case "esc":
		e.leaveInsert()
	case "enter":
		e.insertText("\n")
	case "tab":
		e.insertText("  ")
	case "backspace", "ctrl+h":
		e.backspace()
	case "delete":
		e.deleteForward()
	case "ctrl+w":
		e.deleteWordBack()
	case "ctrl+u":
		e.deleteToLineStart()
	case "left", "right", "up", "down", "home", "end":
		e.insertMove(key)
	default:
		if text != "" {
			e.insertText(text)
		}
	}
}

func (e *Editor) insertText(text string) {
	e.beginChange()
	e.Buf.setCursor(e.Buf.insertAt(e.Buf.cursor(), text))
	if e.typedOK {
		e.typed = append(e.typed, []rune(text)...)
	}
}

// untype forgets the last typed rune when it is deleted again; deleting
// anything else means the session can no longer be repeated with a count.
func (e *Editor) untype() {
	if len(e.typed) == 0 {
		e.typedOK = false
		return
	}
	e.typed = e.typed[:len(e.typed)-1]
}

func (e *Editor) backspace() {
	b := &e.Buf
	switch {
	case b.Col > 0:
		e.beginChange()
		b.deleteRange(Pos{b.Row, b.Col - 1}, Pos{b.Row, b.Col})
		b.Col--
		e.untype()
	case b.Row > 0:
		e.beginChange()
		at := Pos{b.Row - 1, b.length(b.Row - 1)}
		b.deleteRange(at, Pos{b.Row, 0})
		b.setCursor(at)
		e.untype()
	}
}

func (e *Editor) deleteForward() {
	b := &e.Buf
	switch {
	case b.Col < b.length(b.Row):
		e.beginChange()
		b.deleteRange(b.cursor(), Pos{b.Row, b.Col + 1})
	case b.Row < b.lastRow():
		e.beginChange()
		b.deleteRange(b.cursor(), Pos{b.Row + 1, 0})
	}
	e.typedOK = false
}

// deleteWordBack is ctrl+w: the blanks before the cursor and the word before
// them; at the start of a line it joins the line to the one above.
func (e *Editor) deleteWordBack() {
	b := &e.Buf
	if b.Col == 0 {
		e.backspace()
		return
	}
	line := b.runes(b.Row)
	from := b.Col
	for from > 0 && unicode.IsSpace(line[from-1]) {
		from--
	}
	if from > 0 {
		kind := class(line[from-1], false)
		for from > 0 && class(line[from-1], false) == kind {
			from--
		}
	}
	e.beginChange()
	b.deleteRange(Pos{b.Row, from}, b.cursor())
	b.Col = from
	e.typedOK = false
}

func (e *Editor) deleteToLineStart() {
	b := &e.Buf
	if b.Col == 0 {
		return
	}
	e.beginChange()
	b.deleteRange(Pos{b.Row, 0}, b.cursor())
	b.Col = 0
	e.typedOK = false
}

func (e *Editor) insertMove(key string) {
	b := &e.Buf
	e.typedOK = false
	switch key {
	case "left":
		if b.Col > 0 {
			b.Col--
		} else if b.Row > 0 {
			b.Row--
			b.Col = b.length(b.Row)
		}
	case "right":
		if b.Col < b.length(b.Row) {
			b.Col++
		} else if b.Row < b.lastRow() {
			b.Row++
			b.Col = 0
		}
	case "up", "down":
		step := 1
		if key == "up" {
			step = -1
		}
		if row := b.Row + step; row >= 0 && row <= b.lastRow() {
			b.Row, b.Col = row, min(e.want, b.length(row))
		}
		e.keepWant = true
	case "home":
		b.Col = 0
	case "end":
		b.Col = b.length(b.Row)
	}
}

// leaveInsert is esc in insert mode: a count repeats what was typed, and the
// cursor steps back onto the last character, as in vim.
func (e *Editor) leaveInsert() {
	if e.insertCount > 1 && e.typedOK && len(e.typed) > 0 {
		again := string(e.typed)
		if e.insertKind == "o" || e.insertKind == "O" {
			again = "\n" + again
		}
		for range e.insertCount - 1 {
			e.Buf.setCursor(e.Buf.insertAt(e.Buf.cursor(), again))
		}
	}
	e.insertCount, e.typed = 1, nil
	e.mode = Normal
	if e.Buf.Col > 0 {
		e.Buf.Col--
	}
}
