package vimedit

import "unicode"

// searchState is the last pattern, so n, N and an empty / or ? can reuse it.
type searchState struct {
	pattern string
	forward bool
	whole   bool // from * or #: only whole words match
}

// CommandPrefix is the character that starts the line being typed: ':' for
// a command, '/' or '?' for a search. It is empty outside command mode.
func (e *Editor) CommandPrefix() string {
	if e.mode != Command {
		return ""
	}
	return string(e.cmdKind)
}

// startSearch opens the search prompt. The pending operator and count stay,
// so d/foo<enter> and 3/foo<enter> work; the mode it came from is restored
// afterwards, which keeps a visual selection alive.
func (e *Editor) startSearch(tok string) {
	e.cmdReturn = e.mode
	e.mode, e.cmdline, e.cmdKind = Command, nil, rune(tok[0])
}

// finishSearch is enter in a search prompt. An empty pattern reuses the last.
func (e *Editor) finishSearch() {
	pattern := string(e.cmdline)
	forward := e.cmdKind == '/'
	e.mode, e.cmdline = e.cmdReturn, nil
	if pattern == "" {
		if e.lastSearch.pattern == "" {
			e.clearPending()
			return
		}
		e.lastSearch.forward = forward
	} else {
		e.lastSearch = searchState{pattern: pattern, forward: forward}
	}
	e.runMotion("n", 0)
}

// Match is a stretch of the text the search highlights: runes Start up to,
// not including, End of buffer row Row. Current marks the match the prompt
// would jump to while the pattern is still being typed.
type Match struct {
	Row, Start, End int
	Current         bool
}

// livePattern is what a search prompt holds so far, which is highlighted and
// scrolled to before enter, as incsearch does.
func (e *Editor) livePattern() (searchState, bool) {
	if e.mode != Command || e.cmdKind == ':' || len(e.cmdline) == 0 {
		return searchState{}, false
	}
	return searchState{pattern: string(e.cmdline), forward: e.cmdKind == '/'}, true
}

// IncsearchTarget is where enter would go for the pattern typed so far in a
// / or ? prompt: the first match from the cursor. The cursor itself stays put,
// so esc leaves everything as it was.
func (e *Editor) IncsearchTarget() (Pos, bool) {
	search, ok := e.livePattern()
	if !ok {
		return Pos{}, false
	}
	at, _, found := e.findIn(search, e.Buf.cursor(), search.forward)
	return at, found
}

// Matches lists the highlighted matches on buffer rows first to last, in
// order: the pattern in the search prompt, or else the last search until
// :noh. Matches do not overlap, and the part under a visual selection is left
// out so the selection stays visible.
func (e *Editor) Matches(first, last int) []Match {
	search, live := e.livePattern()
	switch {
	case live:
	case e.hlsearch && e.lastSearch.pattern != "":
		search = e.lastSearch
	default:
		return nil
	}
	pattern := []rune(search.pattern)
	fold := !hasUpper(pattern)
	if fold {
		pattern = foldRunes(pattern)
	}
	var current Pos
	hasCurrent := false
	if live {
		current, hasCurrent = e.IncsearchTarget()
	}
	var out []Match
	for row := max(first, 0); row <= min(last, e.Buf.lastRow()); row++ {
		line := e.Buf.runes(row)
		if fold {
			line = foldRunes(line)
		}
		end := 0
		for _, col := range matchColumns(line, pattern, search.whole) {
			if col < end {
				continue // overlaps the one before, as Vim draws it
			}
			end = col + len(pattern)
			match := Match{Row: row, Start: col, End: end, Current: hasCurrent && current == Pos{row, col}}
			out = append(out, e.outsideSelection(match)...)
		}
	}
	return out
}

// outsideSelection cuts the visual selection out of a match.
func (e *Editor) outsideSelection(m Match) []Match {
	from, to, ok := e.Selection()
	if !ok || m.Row < from.Row || m.Row > to.Row {
		return []Match{m}
	}
	cutFrom, cutTo := 0, e.Buf.length(m.Row)
	if m.Row == from.Row {
		cutFrom = from.Col
	}
	if m.Row == to.Row {
		cutTo = to.Col
	}
	var out []Match
	if m.Start < cutFrom {
		head := m
		head.End = min(m.End, cutFrom)
		out = append(out, head)
	}
	if m.End > cutTo {
		tail := m
		tail.Start, tail.Current = max(m.Start, cutTo), false
		out = append(out, tail)
	}
	return out
}

// leaveCommandLine is esc, or backspace on an empty line.
func (e *Editor) leaveCommandLine() {
	e.cmdline = nil
	if e.cmdKind == ':' {
		e.mode = Normal
		return
	}
	e.mode = e.cmdReturn
	e.clearPending()
}

func isKeyword(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

// wordUnderCursor is the keyword at the cursor or the next one on the line,
// as * finds it, with the column it starts at.
func (e *Editor) wordUnderCursor() (word string, start int, ok bool) {
	line := e.Buf.runes(e.Buf.Row)
	i := e.Buf.Col
	for i < len(line) && !isKeyword(line[i]) {
		i++
	}
	if i >= len(line) {
		return "", 0, false
	}
	start = i
	for start > 0 && isKeyword(line[start-1]) {
		start--
	}
	end := i
	for end < len(line) && isKeyword(line[end]) {
		end++
	}
	return string(line[start:end]), start, true
}

// searchTarget is n, N, * and #. count repeats the search; reverse flips the
// direction of the last one (N).
func (e *Editor) searchTarget(name string, count int) (target, bool) {
	origin := e.Buf.cursor()
	reverse := name == "N"
	switch name {
	case "*", "#":
		word, start, ok := e.wordUnderCursor()
		if !ok {
			e.status = "No string under cursor"
			return target{}, false
		}
		e.lastSearch = searchState{pattern: word, forward: name == "*", whole: true}
		origin.Col = start
	}
	if e.lastSearch.pattern == "" {
		e.status = "No previous search"
		return target{}, false
	}
	e.hlsearch = true // n and * bring the highlight back after :noh
	forward := e.lastSearch.forward != reverse
	wrapped := false
	for range max(count, 1) {
		next, wrap, found := e.findPattern(origin, forward)
		if !found {
			e.status = "Pattern not found: " + e.lastSearch.pattern
			return target{}, false
		}
		origin, wrapped = next, wrapped || wrap
	}
	if wrapped {
		e.status = "search wrapped"
	}
	return target{pos: origin}, true
}

// findPattern finds the next match after (or before) from, wrapping round the
// text once. A match that starts at from does not count until it wraps.
func (e *Editor) findPattern(from Pos, forward bool) (at Pos, wrapped, found bool) {
	return e.findIn(e.lastSearch, from, forward)
}

// findIn is findPattern for any search, so the pattern being typed can be
// tried without becoming the last one.
func (e *Editor) findIn(search searchState, from Pos, forward bool) (at Pos, wrapped, found bool) {
	pattern := []rune(search.pattern)
	fold := !hasUpper(pattern) // smartcase
	if fold {
		pattern = foldRunes(pattern)
	}
	matches := func(row int) []int {
		line := e.Buf.runes(row)
		if fold {
			line = foldRunes(line)
		}
		return matchColumns(line, pattern, search.whole)
	}
	rows := e.Buf.lastRow() + 1
	for step := 0; step <= rows; step++ {
		row := from.Row + step
		if !forward {
			row = from.Row - step
		}
		wrap := row < 0 || row >= rows
		row = (row%rows + rows) % rows
		columns := matches(row)
		switch {
		case forward:
			for _, col := range columns {
				// The first and the wrapped-back visit of the cursor's own row
				// each see only their half, so a lone match is found once.
				if step == 0 && col <= from.Col || step == rows && col > from.Col {
					continue
				}
				return Pos{row, col}, wrap, true
			}
		default:
			for i := len(columns) - 1; i >= 0; i-- {
				col := columns[i]
				if step == 0 && col >= from.Col || step == rows && col < from.Col {
					continue
				}
				return Pos{row, col}, wrap, true
			}
		}
	}
	return Pos{}, false, false
}

// matchColumns lists where pattern starts in line, overlapping hits included.
func matchColumns(line, pattern []rune, whole bool) []int {
	var columns []int
	for i := 0; i+len(pattern) <= len(line) && len(pattern) > 0; i++ {
		if !runesEqual(line[i:i+len(pattern)], pattern) {
			continue
		}
		if whole && (i > 0 && isKeyword(line[i-1]) || i+len(pattern) < len(line) && isKeyword(line[i+len(pattern)])) {
			continue
		}
		columns = append(columns, i)
	}
	return columns
}

func runesEqual(a, b []rune) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func hasUpper(runes []rune) bool {
	for _, r := range runes {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

// foldRunes lower-cases rune by rune, so columns stay the same.
func foldRunes(runes []rune) []rune {
	out := make([]rune, len(runes))
	for i, r := range runes {
		out[i] = unicode.ToLower(r)
	}
	return out
}
