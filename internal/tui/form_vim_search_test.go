package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/aronvandepol/mailday/internal/compose"
	"github.com/aronvandepol/mailday/internal/vimedit"
)

// vimBody opens a reply whose body is text, sized 60x30 and drawn once so the
// text area has its width, with the editor in normal mode at the top.
func vimBody(t *testing.T, text string) Model {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MAILDAY_VIM", "on")
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
	model.width, model.height = 60, 30
	updated, _ := model.openComposer("Reply", compose.Draft{Body: text}, "", true)
	model = updated.(Model)
	_ = model.renderForm(model.width, model.height-3)
	model = vimPress(t, model, escKey())
	return vimPress(t, model, key("g"), key("g"))
}

func drawnBody(model Model) string { return model.renderForm(model.width, model.height-3) }

func TestVimSearchHighlightIsDrawn(t *testing.T) {
	model := vimBody(t, "one two one\nsecond line one")
	plain := ansi.Strip(drawnBody(model))
	model = typeText(t, model, "/one")
	model = vimPress(t, model, enterKey)
	drawn := drawnBody(model)
	match := searchMatchStyle(false).Render("one")
	if strings.Count(drawn, match) != 2 { // the third has the cursor on its first letter
		t.Fatalf("want two matches drawn as %q, got %d in %q", match, strings.Count(drawn, match), drawn)
	}
	// The cursor sits on the first match after the jump; its cell stays as
	// the text area drew it and the rest of the match is still highlighted.
	if model.form.vim.Mode() != vimedit.Normal {
		t.Fatal("not in normal mode")
	}
	if ansi.Strip(drawn) != plain {
		t.Fatalf("highlighting changed the text:\n%q\n%q", ansi.Strip(drawn), plain)
	}
	if strings.Count(drawn, searchMatchStyle(false).Render("ne")) != 1 {
		t.Errorf("the match under the cursor should keep its cursor cell and highlight the rest: %q", drawn)
	}

	model = vimPress(t, model, escKey())
	if !strings.Contains(drawnBody(model), match) {
		t.Error("esc must not clear the highlight")
	}
	model = typeText(t, model, ":noh")
	model = vimPress(t, model, enterKey)
	if strings.Contains(drawnBody(model), searchMatchStyle(false).Render("one")) || strings.Contains(drawnBody(model), searchMatchStyle(false).Render("ne")) {
		t.Error(":noh should clear the highlight")
	}
	model = typeText(t, model, "n")
	if !strings.Contains(drawnBody(model), "\x1b[") || !strings.Contains(drawnBody(model), match) {
		t.Error("n draws the highlight again")
	}
}

func TestVimSearchHighlightOnWrappedKoreanText(t *testing.T) {
	long := strings.Repeat("한국어 텍스트 ", 12) + "마지막 한국어 끝"
	model := vimBody(t, "앞 한국어\n"+long)
	plain := ansi.Strip(drawnBody(model))
	model = typeText(t, model, "/한국어")
	model = vimPress(t, model, enterKey)
	drawn := drawnBody(model)
	if ansi.Strip(drawn) != plain {
		t.Fatalf("highlighting changed the layout")
	}
	style := searchMatchStyle(false)
	// Every one of the 14 matches is on screen and painted, except the cell
	// under the cursor.
	painted := strings.Count(drawn, style.Render("한국어")) + strings.Count(drawn, style.Render("국어"))
	if painted < 13 {
		t.Errorf("painted %d Korean matches, want 14 (13 whole + the cursor's)", painted)
	}
}

func TestVimSearchHighlightOnSecondScreenLine(t *testing.T) {
	model := vimBody(t, strings.Repeat("filler ", 20)+"needle here")
	model = typeText(t, model, "/needle")
	model = vimPress(t, model, enterKey)
	model = typeText(t, model, "gg")
	drawn := drawnBody(model)
	if !strings.Contains(drawn, searchMatchStyle(false).Render("needle")) {
		t.Errorf("a match on a wrapped line was not painted: %q", drawn)
	}
	lines := strings.Split(ansi.Strip(drawn), "\n")
	for _, line := range lines {
		if strings.Contains(line, "needle") && strings.Contains(line, "filler filler filler filler filler filler filler filler filler filler filler filler") {
			t.Errorf("expected the text to wrap before needle: %q", line)
		}
	}
}

func TestVimSelectionShowsThroughSearchHighlight(t *testing.T) {
	model := vimBody(t, "ab ab ab")
	model = typeText(t, model, "/ab")
	model = vimPress(t, model, enterKey)
	model = typeText(t, model, "0ve")
	drawn := drawnBody(model)
	if strings.Count(drawn, searchMatchStyle(false).Render("ab")) != 2 {
		t.Errorf("the two matches outside the selection are painted: %q", drawn)
	}
}

func TestVimIncsearchScrollsAndEscRestores(t *testing.T) {
	var lines []string
	for i := range 90 {
		lines = append(lines, "line "+strings.Repeat("x", i%7))
	}
	lines[70] = "the needle is here"
	model := vimBody(t, strings.Join(lines, "\n"))
	model = typeText(t, model, "20G")
	_ = drawnBody(model)
	before, cursorBefore := model.form.body.ScrollYOffset(), model.form.body.Line()
	if cursorBefore != 19 {
		t.Fatalf("cursor line %d", cursorBefore)
	}

	model = typeText(t, model, "/need")
	_ = drawnBody(model)
	if row, _ := model.form.vim.Cursor(); row != 19 {
		t.Errorf("incsearch moved the editor's cursor to row %d", row)
	}
	if model.form.body.Line() != 70 {
		t.Errorf("the text area should show the first match, cursor on line %d", model.form.body.Line())
	}
	if top := model.form.body.ScrollYOffset(); top <= before || top > 70 {
		t.Errorf("scrolled to %d, from %d: the match should be on screen", top, before)
	}
	// The text area's cursor sits on the match's first cell and keeps it.
	if !strings.Contains(drawnBody(model), searchMatchStyle(true).Render("eed")) {
		t.Errorf("the live match is painted: %q", drawnBody(model))
	}

	model = typeText(t, model, "x") // "needx" matches nothing: back to where it was
	_ = drawnBody(model)
	if model.form.body.ScrollYOffset() != before || model.form.body.Line() != cursorBefore {
		t.Errorf("no match: scroll %d line %d, want %d %d", model.form.body.ScrollYOffset(), model.form.body.Line(), before, cursorBefore)
	}
	model = vimPress(t, model, key("backspace"))
	model = vimPress(t, model, escKey())
	_ = drawnBody(model)
	if model.form.body.ScrollYOffset() != before || model.form.body.Line() != cursorBefore {
		t.Errorf("esc: scroll %d line %d, want %d %d", model.form.body.ScrollYOffset(), model.form.body.Line(), before, cursorBefore)
	}
	if strings.Contains(drawnBody(model), searchMatchStyle(false).Render("need")) {
		t.Error("a cancelled search leaves no highlight")
	}

	model = typeText(t, model, "/needle")
	model = vimPress(t, model, enterKey)
	if row, _ := model.form.vim.Cursor(); row != 70 || model.form.body.Line() != 70 {
		t.Errorf("enter: editor row %d, text area line %d", row, model.form.body.Line())
	}
	if top := model.form.body.ScrollYOffset(); top > 70 || top+model.form.body.Height() <= 70 {
		t.Errorf("after enter the match is off screen: top %d height %d", top, model.form.body.Height())
	}
}

func TestVimGjMovesByScreenLineAndFollowsResize(t *testing.T) {
	long := strings.Repeat("word ", 40) + "end"
	model := vimBody(t, long+"\nsecond")
	model = typeText(t, model, "gj")
	row, col := model.form.vim.Cursor()
	if row != 0 || col == 0 {
		t.Fatalf("gj on a wrapped line stays in the row: %d,%d", row, col)
	}
	if model.form.body.Line() != 0 || model.form.body.Column() != col {
		t.Errorf("text area cursor %d,%d, editor %d,%d", model.form.body.Line(), model.form.body.Column(), row, col)
	}
	if model.form.body.LineInfo().RowOffset != 1 {
		t.Errorf("the cursor should be on the second screen line, is on %d", model.form.body.LineInfo().RowOffset)
	}
	model = typeText(t, model, "gk")
	if row, col = model.form.vim.Cursor(); row != 0 || col != 0 {
		t.Errorf("gk back: %d,%d", row, col)
	}
	narrow := col
	model.width = 40 // a resize: the next draw tells the editor
	_ = drawnBody(model)
	model = typeText(t, model, "gj")
	if _, wider := model.form.vim.Cursor(); wider >= narrow+len("word ")*12 || wider == 0 {
		t.Errorf("after a resize gj jumped to column %d", wider)
	}
	model = typeText(t, model, "j")
	if row, _ = model.form.vim.Cursor(); row != 1 {
		t.Errorf("j still moves by line: row %d", row)
	}
}

func TestVimFooterShowsRecording(t *testing.T) {
	model := vimBody(t, "abc")
	model = typeText(t, model, "qa")
	if got := footer(model); !strings.Contains(got, "recording @a") || !strings.Contains(got, "NORMAL") {
		t.Errorf("footer while recording: %q", got)
	}
	model = typeText(t, model, "xq")
	if got := footer(model); strings.Contains(got, "recording") {
		t.Errorf("footer after q: %q", got)
	}
	model = typeText(t, model, "@a")
	if bodyText(model) != "" && bodyText(model) != "c" {
		t.Errorf("body %q", bodyText(model))
	}
	if bodyText(model) != model.form.vim.Text() {
		t.Errorf("text area and editor differ")
	}
}

// WrapLine is the text area's wrapping copied into vimedit, so the editor and
// the highlights know where a line breaks. This keeps the two the same.
func TestWrapLineAgreesWithTheTextArea(t *testing.T) {
	texts := []string{
		"", "short", "word word word word word word word word end",
		strings.Repeat("한국어 텍스트 ", 9), strings.Repeat("가", 31), strings.Repeat("x", 59), strings.Repeat("x", 60), strings.Repeat("x", 61),
		strings.Repeat("ab ", 20), "trailing blanks   ", "   leading blanks and a very long word " + strings.Repeat("y", 70),
		strings.Repeat("a", 58) + " 한", strings.Repeat("한", 29) + "a bc", "no  double   spaces  survive " + strings.Repeat("z ", 40),
	}
	for _, width := range []int{20, 40, 59} {
		model := vimBody(t, strings.Join(texts, "\n"))
		model.width = width + 8
		_ = drawnBody(model)
		body := &model.form.body
		for row, text := range texts {
			segments := vimedit.WrapLine([]rune(text), body.Width())
			base := 0
			for index, segment := range segments {
				model.form.placeBodyCursor(row, base)
				info := body.LineInfo()
				if info.Height != len(segments) || info.RowOffset != index || info.StartColumn != base || info.Width != len(segment) {
					t.Errorf("width %d row %d segment %d of %q: text area says %+v, WrapLine %d segments of %d at %d",
						body.Width(), row, index, text, info, len(segments), len(segment), base)
				}
				base += len(segment)
			}
		}
	}
}
