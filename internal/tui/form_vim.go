package tui

import (
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/aronvandepol/mailday/internal/vimedit"
)

// searchView is what the composer keeps to show the editor's search: whether
// a / or ? prompt is open, and how far the body was scrolled before it opened.
type searchView struct {
	active bool
	scroll int
}

// searchMatchStyle is the colour of a search match: the yellow of the footer
// label, apart from the cursor (reverse) and the visual selection. The match
// a prompt would jump to is stronger. Without colour they are underlined.
func searchMatchStyle(current bool) lipgloss.Style {
	if noColor {
		return lipgloss.NewStyle().Underline(true).Bold(current)
	}
	background := colorActive
	if current {
		background = colorPrimary
	}
	return lipgloss.NewStyle().Background(background).Foreground(themeBackground).Bold(current)
}

// viewCursor is where the text area's cursor belongs: the editor's cursor, or
// while a / or ? prompt is open the match enter would jump to (incsearch).
// The scroll from before the prompt is brought back first, so every key
// scrolls from the same place and esc leaves the view as it was.
func (f *headerForm) viewCursor() (row, col int) {
	row, col = f.vim.Cursor()
	typing := f.vim.CommandPrefix() == "/" || f.vim.CommandPrefix() == "?"
	switch {
	case typing && !f.search.active:
		f.search = searchView{active: true, scroll: f.body.ScrollYOffset()}
	case typing:
		f.scrollTo(f.search.scroll)
	case f.search.active:
		f.scrollTo(f.search.scroll)
		f.search.active = false
	}
	if typing {
		if at, ok := f.vim.IncsearchTarget(); ok {
			row, col = at.Row, at.Col
		}
	}
	return row, col
}

// scrollTo brings the text area back to a scroll offset. Only the cursor
// scrolls it, so the cursor goes to the last line of that window and the
// caller puts it where it belongs.
func (f *headerForm) scrollTo(offset int) {
	if f.body.ScrollYOffset() == offset {
		return
	}
	f.body.MoveToBegin()
	for range min(offset+f.body.Height()-1, len(f.body.Value())) {
		f.body.CursorDown()
	}
}

// screenLine is one drawn line of the body: which buffer row it shows and
// the rune it starts at.
type screenLine struct {
	row, base int
	runes     []rune
	cuts      []matchCut
}

// matchCut is a match on one screen line, in cells from its left edge.
type matchCut struct {
	from, to int
	current  bool
}

func cellWidth(runes []rune) int { return ansi.StringWidth(string(runes)) }

// highlightSearch paints the editor's search matches over the drawn body.
// The text area can only style one selection, so the matches are laid over
// its output: the editor says where they are in the text, and WrapLine,
// the text area's own wrapping, says which screen line and cell that is.
// The cell under the cursor is left as drawn.
func (f *headerForm) highlightSearch(view string) string {
	width, height, top := f.body.Width(), f.body.Height(), f.body.ScrollYOffset()
	if f.vim == nil || width <= 0 || f.body.Value() == "" {
		return view
	}
	screen := make([]screenLine, height)
	first, last, line := -1, -1, 0
	cursorRow, cursorLine := f.body.Line(), -1
	for row, text := range strings.Split(f.body.Value(), "\n") {
		segments := vimedit.WrapLine([]rune(text), width)
		if line+len(segments) > top && line < top+height {
			if first < 0 {
				first = row
			}
			last = row
		}
		base := 0
		for index, segment := range segments {
			if y := line + index - top; y >= 0 && y < height {
				screen[y] = screenLine{row: row, base: base, runes: segment}
			}
			base += len(segment)
		}
		if row == cursorRow {
			cursorLine = line + f.body.LineInfo().RowOffset
		}
		line += len(segments)
		if line >= top+height {
			break
		}
	}
	if first < 0 {
		return view
	}
	drawn := false
	for _, match := range f.vim.Matches(first, last) {
		for y := range screen {
			s := &screen[y]
			if s.runes == nil || s.row != match.Row {
				continue
			}
			from, to := max(match.Start, s.base), min(match.End, s.base+len(s.runes))
			if from >= to {
				continue
			}
			cut := matchCut{cellWidth(s.runes[:from-s.base]), cellWidth(s.runes[:to-s.base]), match.Current}
			s.cuts = append(s.cuts, matchCut{min(cut.from, width), min(cut.to, width), cut.current})
			drawn = true
		}
	}
	if !drawn {
		return view
	}
	cursorX, cursorWidth := -1, 1
	if f.body.Focused() && cursorLine >= top && cursorLine < top+height {
		info := f.body.LineInfo()
		cursorX = info.CharOffset
		if runes := []rune(strings.Split(f.body.Value(), "\n")[cursorRow]); f.body.Column() < len(runes) {
			cursorWidth = max(cellWidth(runes[f.body.Column():f.body.Column()+1]), 1)
		}
	}
	lines := strings.Split(view, "\n")
	for y := range min(len(lines), height) {
		if len(screen[y].cuts) > 0 {
			x := -1
			if y == cursorLine-top {
				x = cursorX
			}
			lines[y] = paintCuts(lines[y], screen[y].cuts, x, cursorWidth)
		}
	}
	return strings.Join(lines, "\n")
}

// paintCuts styles the cells of one drawn line that the cuts cover, leaving
// the cursor's cell (cursorX, or -1) as it was drawn.
func paintCuts(line string, cuts []matchCut, cursorX, cursorWidth int) string {
	slices.SortFunc(cuts, func(a, b matchCut) int { return a.from - b.from })
	var out strings.Builder
	at := 0
	paint := func(from, to int, current bool) {
		if to <= from || from < at {
			return
		}
		out.WriteString(ansi.Cut(line, at, from))
		out.WriteString(searchMatchStyle(current).Render(ansi.Strip(ansi.Cut(line, from, to))))
		at = to
	}
	for _, cut := range cuts {
		if cursorX >= 0 && cursorX < cut.to && cursorX+cursorWidth > cut.from {
			paint(cut.from, min(cut.to, cursorX), cut.current)
			paint(max(cut.from, cursorX+cursorWidth), cut.to, cut.current)
			continue
		}
		paint(cut.from, cut.to, cut.current)
	}
	out.WriteString(ansi.Cut(line, at, ansi.StringWidth(line)))
	return out.String()
}

// recordingNote is "recording @a" while a macro is being recorded.
func recordingNote(vim *vimedit.Editor) string {
	if name := vim.Recording(); name != 0 {
		return lipgloss.NewStyle().Foreground(colorAlert).Bold(true).Render("recording @" + string(name))
	}
	return ""
}
