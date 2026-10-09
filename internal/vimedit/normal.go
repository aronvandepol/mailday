package vimedit

import (
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// region is what an operator works on: the runes from start up to, not
// including, end; or, when linewise, rows start.Row to end.Row inclusive.
type region struct {
	start, end Pos
	linewise   bool
}

func (e *Editor) lineRegion(first, last int) region {
	return region{start: Pos{first, 0}, end: Pos{last, 0}, linewise: true}
}

func oneRune(token string) (rune, bool) {
	r, size := utf8.DecodeRuneInString(token)
	return r, size > 0 && size == len(token) && r != utf8.RuneError
}

// motionNames maps keys to the motion they perform.
var motionNames = map[string]string{
	"h": "h", "left": "h", "backspace": "h",
	"l": "l", "right": "l", " ": "l",
	"j": "j", "down": "j", "k": "k", "up": "k",
	"w": "w", "W": "W", "b": "b", "B": "B", "e": "e", "E": "E",
	"0": "0", "home": "0", "^": "^", "$": "$", "end": "$",
	"G": "G", "{": "{", "}": "}", ";": ";", ",": ",",
	"n": "n", "N": "N", "*": "*", "#": "#",
}

// normalKey handles a key in normal or visual mode.
func (e *Editor) normalKey(tok string) {
	if e.pend != "" {
		e.pendingKey(tok)
		return
	}
	if len(tok) == 1 && (tok[0] >= '1' && tok[0] <= '9' || tok[0] == '0' && e.activeCount() > 0) {
		slot := &e.count
		if e.op != "" {
			slot = &e.opCount
		}
		*slot = min(*slot*10+int(tok[0]-'0'), maxCount)
		e.markCount()
		return
	}
	if tok == "esc" {
		e.escape()
		return
	}
	if tok == "/" || tok == "?" {
		e.startSearch(tok)
		return
	}
	switch tok {
	case "d", "c", "y", ">", "<":
		if e.isVisual() {
			e.visualOperator(tok)
		} else {
			e.operatorKey(tok)
		}
		return
	}
	if name, ok := motionNames[tok]; ok {
		e.runMotion(name, 0)
		return
	}
	switch tok {
	case "g", "f", "F", "t", "T", `"`:
		e.pend = tok
		return
	}
	if e.op != "" {
		if tok == "i" || tok == "a" {
			e.pend = tok
		} else {
			e.clearPending()
		}
		return
	}
	if e.isVisual() {
		e.visualKey(tok)
	} else {
		e.simpleKey(tok)
	}
	if e.pend == "" {
		e.clearPending()
	}
}

func (e *Editor) activeCount() int {
	if e.op != "" {
		return e.opCount
	}
	return e.count
}

// markCount flags the key just recorded as a count digit, so '.' can swap it.
func (e *Editor) markCount() {
	if e.recording && !e.replaying && len(e.rec) > 0 {
		e.rec[len(e.rec)-1].count = true
	}
}

func (e *Editor) escape() {
	switch {
	case !e.idle():
		e.clearPending()
	case e.isVisual():
		e.mode = Normal
	case e.macroDepth > 0:
		// a played-back esc never counts towards closing the composer
	default:
		now := e.Now()
		if !e.lastEsc.IsZero() && now.Sub(e.lastEsc) <= escWindow {
			e.lastEsc = time.Time{}
			e.res.Action = Close
			return
		}
		e.lastEsc = now
		e.status = EscHint
	}
}

// pendingKey completes a command that was waiting for one more key.
func (e *Editor) pendingKey(tok string) {
	kind := e.pend
	e.pend = ""
	char, single := oneRune(tok)
	switch kind {
	case "g":
		switch tok {
		case "g":
			e.runMotion("gg", 0)
			return
		case "j", "k", "0", "$":
			e.runMotion("g"+tok, 0)
			return
		}
	case "f", "F", "t", "T":
		if single {
			e.lastFnd = findState{kind: kind, char: char}
			e.runMotion(kind, char)
			return
		}
	case "r":
		if single {
			e.replaceChars(char)
			return
		}
	case `"`:
		if single && (char == '_' || char == '"' || char >= 'a' && char <= 'z') {
			e.regName = char
			if char == '"' {
				e.regName = 0
			}
			return
		}
	case "i", "a":
		e.objectKey(kind, tok)
		return
	case "q":
		if single && char >= 'a' && char <= 'z' {
			e.startMacro(char)
			return
		}
	case "@":
		if single && (char == '@' || char >= 'a' && char <= 'z') {
			e.playMacro(char)
			return
		}
	case "Z":
		switch tok {
		case "Z":
			e.res.Action = Preview
		case "Q":
			e.res.Action = Close
		}
	}
	e.clearPending()
}

// --- registers ----------------------------------------------------------

func (e *Editor) store(text string, linewise bool) {
	if text == "" && !linewise {
		return // nothing was taken, so the register keeps what it had
	}
	entry := register{text, linewise}
	switch {
	case e.regName == '_':
	case e.regName != 0:
		e.named[e.regName] = entry
		e.unnamed = entry
	default:
		e.unnamed = entry
	}
}

func (e *Editor) loadRegister() register {
	if e.regName != 0 && e.regName != '_' {
		return e.named[e.regName]
	}
	return e.unnamed
}

// --- operators ----------------------------------------------------------

// operatorKey is d, c, y, > or < in normal mode: the first one waits for a
// motion, the same key again works on whole lines (dd, 3yy, >>).
func (e *Editor) operatorKey(tok string) {
	if e.op == "" {
		e.op = tok
		return
	}
	if e.op != tok {
		e.clearPending()
		return
	}
	first := e.Buf.Row
	last := min(first+max(e.countN(), 1)-1, e.Buf.lastRow())
	e.applyOperator(e.op, e.lineRegion(first, last))
	e.clearPending()
}

// applyOperator carries out d, y, c, > or < on a region.
func (e *Editor) applyOperator(op string, r region) {
	if op == "d" && !r.linewise && r.start == r.end {
		return // nothing to delete, so nothing moves either
	}
	if op != "y" {
		// Undo brings the cursor back to where the change began.
		if r.linewise {
			e.Buf.Row = r.start.Row
		} else {
			e.Buf.setCursor(r.start)
		}
	}
	switch op {
	case "y":
		e.store(e.regionText(r), r.linewise)
		if r.linewise {
			e.Buf.Row = r.start.Row
		} else {
			e.Buf.setCursor(r.start)
		}
	case "d":
		e.store(e.regionText(r), r.linewise)
		e.deleteRegion(r)
	case "c":
		e.store(e.regionText(r), r.linewise)
		e.beginChange()
		if r.linewise {
			e.Buf.deleteLines(r.start.Row, r.end.Row)
			e.Buf.insertLines(r.start.Row, []string{""})
			e.Buf.setCursor(Pos{r.start.Row, 0})
		} else {
			if r.start != r.end {
				e.Buf.deleteRange(r.start, r.end)
			}
			e.Buf.setCursor(r.start)
		}
		e.mode = Insert
		e.beginInsert("c", 1)
	case ">", "<":
		e.shiftLines(r.start.Row, r.end.Row, op == ">")
		e.Buf.setCursor(Pos{r.start.Row, e.Buf.firstNonBlank(r.start.Row)})
	}
}

func (e *Editor) regionText(r region) string {
	if r.linewise {
		return strings.Join(e.Buf.Lines[r.start.Row:r.end.Row+1], "\n")
	}
	return e.Buf.slice(r.start, r.end)
}

func (e *Editor) deleteRegion(r region) {
	if r.linewise {
		e.beginChange()
		e.Buf.deleteLines(r.start.Row, r.end.Row)
		row := min(r.start.Row, e.Buf.lastRow())
		e.Buf.setCursor(Pos{row, e.Buf.firstNonBlank(row)})
		return
	}
	if r.start == r.end {
		return
	}
	e.beginChange()
	e.Buf.deleteRange(r.start, r.end)
	e.Buf.setCursor(r.start)
}

func (e *Editor) shiftLines(first, last int, right bool) {
	e.beginChange()
	for row := first; row <= last; row++ {
		line := e.Buf.Lines[row]
		switch {
		case right && line != "":
			e.Buf.Lines[row] = strings.Repeat(" ", shiftWidth) + line
		case !right:
			if strings.HasPrefix(line, "\t") {
				e.Buf.Lines[row] = line[1:]
				continue
			}
			strip := 0
			for strip < shiftWidth && strip < len(line) && line[strip] == ' ' {
				strip++
			}
			e.Buf.Lines[row] = line[strip:]
		}
	}
}

func (e *Editor) mapRegion(r region, change func(rune) rune) {
	e.beginChange()
	last := r.end.Row
	if r.linewise {
		r.start.Col, r.end.Col = 0, 0
	} else if r.end.Col == 0 && r.end.Row > r.start.Row {
		last-- // the end is only the line break
	}
	for row := r.start.Row; row <= last; row++ {
		line := append([]rune(nil), e.Buf.runes(row)...)
		from, to := 0, len(line)
		if row == r.start.Row && !r.linewise {
			from = min(r.start.Col, len(line))
		}
		if row == r.end.Row && !r.linewise {
			to = min(r.end.Col, len(line))
		}
		for i := from; i < to; i++ {
			line[i] = change(line[i])
		}
		e.Buf.Lines[row] = string(line)
	}
}

func toggleCase(r rune) rune {
	switch {
	case unicode.IsUpper(r):
		return unicode.ToLower(r)
	case unicode.IsLower(r):
		return unicode.ToUpper(r)
	}
	return r
}

// --- commands that stand alone -------------------------------------------

func (e *Editor) simpleKey(tok string) {
	b := &e.Buf
	n := max(e.countN(), 1)
	row, col, length := b.Row, b.Col, b.length(b.Row)
	switch tok {
	case "i":
		e.mode = Insert
		e.beginInsert("i", n)
	case "a":
		if length > 0 {
			b.Col++
		}
		e.mode = Insert
		e.beginInsert("a", n)
	case "I":
		b.Col = b.indent(row)
		e.mode = Insert
		e.beginInsert("I", n)
	case "A":
		b.Col = length
		e.mode = Insert
		e.beginInsert("A", n)
	case "o", "O":
		e.beginChange()
		at := row
		if tok == "o" {
			at++
		}
		b.insertLines(at, []string{""})
		b.setCursor(Pos{at, 0})
		e.mode = Insert
		e.beginInsert(tok, n)
	case "x", "delete":
		e.applyOperator("d", region{start: Pos{row, col}, end: Pos{row, min(col+n, length)}})
	case "X":
		e.applyOperator("d", region{start: Pos{row, max(col-n, 0)}, end: Pos{row, col}})
	case "s":
		e.applyOperator("c", region{start: Pos{row, col}, end: Pos{row, min(col+n, length)}})
	case "S":
		e.applyOperator("c", e.lineRegion(row, min(row+n-1, b.lastRow())))
	case "C", "D":
		last := min(row+n-1, b.lastRow())
		op := map[string]string{"C": "c", "D": "d"}[tok]
		e.applyOperator(op, region{start: Pos{row, col}, end: Pos{last, b.length(last)}})
	case "Y":
		e.applyOperator("y", e.lineRegion(row, min(row+n-1, b.lastRow())))
	case "r", "Z", "q", "@":
		e.pend = tok
	case "~":
		if length > 0 {
			end := min(col+n, length)
			e.mapRegion(region{start: Pos{row, col}, end: Pos{row, end}}, toggleCase)
			b.Col = end
		}
	case "J":
		e.joinLines(row, max(n, 2)-1)
	case "p", "P":
		e.put(tok == "p", n)
	case "u":
		e.undoChange(n)
	case "ctrl+r":
		e.redoChange(n)
	case ".":
		e.repeatChange(e.countN())
	case "v":
		e.mode, e.anchor = Visual, b.cursor()
	case "V":
		e.mode, e.anchor = VisualLine, b.cursor()
	case ":":
		e.mode, e.cmdline, e.cmdKind, e.cmdReturn = Command, nil, ':', Normal
	}
}

func (e *Editor) replaceChars(char rune) {
	b := &e.Buf
	n := max(e.countN(), 1)
	line := append([]rune(nil), b.runes(b.Row)...)
	if b.Col+n > len(line) {
		e.clearPending()
		return
	}
	e.beginChange()
	for i := range n {
		line[b.Col+i] = char
	}
	b.Lines[b.Row] = string(line)
	b.Col += n - 1
	e.clearPending()
}

// joinLines joins the line with the next `joins` lines, as J does: the
// next line's indent goes and one space stands between them.
func (e *Editor) joinLines(row, joins int) {
	b := &e.Buf
	joins = min(joins, b.lastRow()-row)
	if joins <= 0 {
		return
	}
	e.beginChange()
	for range joins {
		head := b.Lines[row]
		tail := strings.TrimLeft(b.Lines[row+1], " \t")
		separator := " "
		if head == "" || tail == "" || strings.HasSuffix(head, " ") || strings.HasSuffix(head, "\t") || strings.HasPrefix(tail, ")") {
			separator = ""
		}
		b.Col = utf8.RuneCountInString(head)
		b.Lines[row] = head + separator + tail
		b.deleteLines(row+1, row+1)
	}
	b.Row = row
}

// put is p and P: the register's text after or before the cursor.
func (e *Editor) put(after bool, times int) {
	b := &e.Buf
	entry := e.loadRegister()
	if entry.text == "" && !entry.linewise {
		return
	}
	e.beginChange()
	if entry.linewise {
		lines := strings.Split(entry.text, "\n")
		var all []string
		for range times {
			all = append(all, lines...)
		}
		at := b.Row
		if after {
			at++
		}
		b.insertLines(at, all)
		b.setCursor(Pos{at, b.firstNonBlank(at)})
		return
	}
	text := strings.Repeat(entry.text, times)
	at := b.cursor()
	if after && b.length(at.Row) > 0 {
		at.Col++
	}
	end := b.insertAt(at, text)
	if strings.Contains(text, "\n") {
		b.setCursor(at)
	} else {
		b.setCursor(Pos{end.Row, max(end.Col-1, 0)})
	}
}

// --- visual mode ----------------------------------------------------------

func (e *Editor) visualRegion() region {
	start, end := e.anchor, e.Buf.cursor()
	if end.before(start) {
		start, end = end, start
	}
	if e.mode == VisualLine {
		return e.lineRegion(start.Row, end.Row)
	}
	return region{start: start, end: Pos{end.Row, min(end.Col+1, e.Buf.length(end.Row))}}
}

// visualOperator runs d, y, c, > or < on the selection and leaves visual mode.
func (e *Editor) visualOperator(op string) {
	r := e.visualRegion()
	e.mode = Normal
	e.applyOperator(op, r)
	e.clearPending()
}

func (e *Editor) visualKey(tok string) {
	r := e.visualRegion()
	lines := e.lineRegion(r.start.Row, r.end.Row)
	switch tok {
	case "x", "delete":
		e.visualOperator("d")
	case "s":
		e.visualOperator("c")
	case "D", "X":
		e.mode = Normal
		e.applyOperator("d", lines)
	case "Y":
		e.mode = Normal
		e.applyOperator("y", lines)
	case "C", "S", "R":
		e.mode = Normal
		e.applyOperator("c", lines)
	case "~", "u", "U":
		change := map[string]func(rune) rune{"~": toggleCase, "u": unicode.ToLower, "U": unicode.ToUpper}[tok]
		e.mode = Normal
		e.mapRegion(r, change)
		e.Buf.setCursor(r.start)
	case "o", "O":
		cursor := e.Buf.cursor()
		e.Buf.setCursor(e.anchor)
		e.anchor = cursor
	case "v":
		e.mode = map[Mode]Mode{Visual: Normal, VisualLine: Visual}[e.mode]
	case "V":
		e.mode = map[Mode]Mode{Visual: VisualLine, VisualLine: Normal}[e.mode]
	case "i", "a":
		e.pend = tok
	case ":":
		e.status = "Ranges are not supported · esc, then :w"
	}
}

// selectRegion makes a text object the visual selection.
func (e *Editor) selectRegion(r region) {
	if r.linewise {
		e.mode = VisualLine
		e.anchor = Pos{r.start.Row, 0}
		e.Buf.setCursor(Pos{r.end.Row, 0})
		return
	}
	if r.start == r.end {
		return
	}
	e.anchor = r.start
	last := Pos{r.end.Row, r.end.Col - 1}
	if r.end.Col == 0 { // the object ends with a line break
		last = Pos{r.end.Row - 1, max(e.Buf.length(r.end.Row-1)-1, 0)}
	}
	e.Buf.setCursor(last)
}

// --- keys that arrive in the ':' line ---------------------------------------

func (e *Editor) commandKey(key, text string) {
	switch key {
	case "esc":
		e.leaveCommandLine()
	case "enter":
		if e.cmdKind != ':' {
			e.finishSearch()
			return
		}
		line := strings.TrimSpace(string(e.cmdline))
		e.mode, e.cmdline = Normal, nil
		e.runCommand(line)
	case "backspace":
		if len(e.cmdline) == 0 {
			e.leaveCommandLine()
			return
		}
		e.cmdline = e.cmdline[:len(e.cmdline)-1]
	case "ctrl+u":
		e.cmdline = nil
	case "ctrl+w":
		trimmed := strings.TrimRight(string(e.cmdline), " ")
		e.cmdline = []rune(trimmed[:strings.LastIndex(trimmed, " ")+1])
	default:
		e.cmdline = append(e.cmdline, []rune(text)...)
	}
}

// runCommand runs a ':' command. Drafts are always kept, so there is no
// quitting without saving: :q! is :q, and :x is the preview, where a message
// is finished.
func (e *Editor) runCommand(line string) {
	switch line {
	case "":
	case "noh", "nohl", "nohls", "nohlsearch":
		e.hlsearch = false
	case "w", "w!", "write":
		e.res.Action = SaveDraft
	case "q", "q!", "quit", "quit!", "wq", "wq!":
		e.res.Action = Close
	case "x", "x!", "xit", "exit":
		e.res.Action = Preview
	default:
		if number, ok := parseNumber(line); ok {
			e.Buf.Row = min(max(number-1, 0), e.Buf.lastRow())
			e.Buf.Col = e.Buf.firstNonBlank(e.Buf.Row)
			return
		}
		e.status = "Not an editor command: " + line
	}
}

func parseNumber(s string) (int, bool) {
	number := 0
	for _, r := range s {
		if r < '0' || r > '9' || number > maxCount {
			return 0, false
		}
		number = number*10 + int(r-'0')
	}
	return number, s != ""
}
