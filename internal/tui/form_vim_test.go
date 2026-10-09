package tui

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/aronvandepol/mailday/internal/compose"
	"github.com/aronvandepol/mailday/internal/contacts"
	"github.com/aronvandepol/mailday/internal/maildir"
)

// Older composer tests close the body with a single esc, as it behaved before
// modal editing, so the package's tests run with it off; the tests here turn
// it on for themselves. A package variable is set before TestMain runs.
var _ = os.Setenv("MAILDAY_VIM", "off")

func vimReply(t *testing.T) Model {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MAILDAY_VIM", "on")
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
	reply := compose.Reply("gmail", "/mail/gmail/Inbox/cur/1", maildir.Content{FromAddr: "ada@example.org", Subject: "Hello", Body: "Question?", Date: time.Now()}, false)
	updated, _ := model.openComposer("Reply", reply, "", true)
	model = updated.(Model)
	if model.form.vim == nil || model.form.field != fieldBody {
		t.Fatal("the reply should open in the body with modal editing on")
	}
	return model
}

func vimPress(t *testing.T, model Model, messages ...tea.Msg) Model {
	t.Helper()
	for _, message := range messages {
		updated, _ := model.Update(message)
		model = updated.(Model)
	}
	return model
}

var (
	enterKey    = tea.KeyPressMsg{Code: tea.KeyEnter}
	ctrlSKey    = tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl}
	ctrlOKey    = tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl}
	shiftTabKey = tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	tabKey      = tea.KeyPressMsg{Code: tea.KeyTab}
)

func bodyText(model Model) string { return model.form.body.Value() }

// savedDraft is the one draft file on disk.
func savedDraft(t *testing.T) string {
	t.Helper()
	files := draftFiles(t)
	if len(files) != 1 {
		t.Fatalf("draft files = %v, want one", files)
	}
	data, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func footer(model Model) string { return ansi.Strip(model.renderFooter(100)) }

func TestVimBodyStartsInInsertAndTypingWorks(t *testing.T) {
	model := vimReply(t)
	if model.form.vim.Mode().String() != "INSERT" || !strings.Contains(footer(model), "-- INSERT --") {
		t.Fatalf("mode %v, footer %q", model.form.vim.Mode(), footer(model))
	}
	model = typeText(t, model, "Thanks, 한국어!")
	if !strings.HasPrefix(bodyText(model), "Thanks, 한국어!\n") || !strings.Contains(bodyText(model), "> Question?") {
		t.Fatalf("body = %q", bodyText(model))
	}
	if bodyText(model) != model.form.vim.Text() {
		t.Fatal("the text area and the editor disagree")
	}
	model = vimPress(t, model, enterKey)
	model = typeText(t, model, "Ada")
	if !strings.HasPrefix(bodyText(model), "Thanks, 한국어!\nAda\n") {
		t.Fatalf("enter should split the line: %q", bodyText(model))
	}
	if model.form.body.Line() != 1 || model.form.body.Column() != 3 {
		t.Fatalf("text area cursor %d,%d", model.form.body.Line(), model.form.body.Column())
	}
}

func TestVimEscDDDeletesTheLineInTheSavedDraftAndURestoresIt(t *testing.T) {
	model := vimReply(t)
	model = typeText(t, model, "Thanks")
	model = vimPress(t, model, escKey())
	if model.form == nil || model.form.vim.Mode().String() != "NORMAL" || !strings.Contains(footer(model), "-- NORMAL --") {
		t.Fatalf("esc should leave insert mode: %v", model.form)
	}
	model = typeText(t, model, "dd")
	if strings.Contains(bodyText(model), "Thanks") {
		t.Fatalf("dd left the line: %q", bodyText(model))
	}
	model = vimPress(t, model, tea.KeyPressMsg{Code: ':', Text: ":"})
	if got := footer(model); !strings.Contains(got, ":") || strings.Contains(got, "NORMAL") {
		t.Fatalf("footer while typing a command: %q", got)
	}
	model = typeText(t, model, "w")
	if !strings.Contains(footer(model), ":w") {
		t.Fatalf("the command being typed should show: %q", footer(model))
	}
	model = vimPress(t, model, enterKey)
	if model.status != "Draft saved" {
		t.Fatalf("status %q", model.status)
	}
	if saved := savedDraft(t); strings.Contains(saved, "Thanks") || !strings.Contains(saved, "> Question?") {
		t.Fatalf("saved draft after dd:\n%s", saved)
	}

	model = typeText(t, model, "u")
	if !strings.HasPrefix(bodyText(model), "Thanks\n") {
		t.Fatalf("u should bring the line back: %q", bodyText(model))
	}
	model = typeText(t, model, ":w")
	model = vimPress(t, model, enterKey)
	if saved := savedDraft(t); !strings.Contains(saved, "Thanks") {
		t.Fatalf("saved draft after u:\n%s", saved)
	}
	if model.form == nil {
		t.Fatal(":w must not close the composer")
	}
}

func TestVimZZOpensThePreview(t *testing.T) {
	model := vimReply(t)
	model = typeText(t, model, "Thanks")
	model = vimPress(t, model, escKey())
	model = typeText(t, model, "ZZ")
	if model.form != nil || model.screen != screenCompose || model.draft == nil || !strings.HasPrefix(model.draft.Body, "Thanks\n") {
		t.Fatalf("ZZ should open the preview: form=%v screen=%v draft=%+v", model.form != nil, model.screen, model.draft)
	}
	if !strings.Contains(savedDraft(t), "Thanks") {
		t.Fatal("the preview saves the draft first")
	}
}

func TestVimCtrlSWorksInEveryMode(t *testing.T) {
	for name, keys := range map[string][]tea.Msg{"insert": nil, "normal": {escKey()}, "visual": {escKey(), key("v")}, "command line": {escKey(), key(":")}} {
		t.Run(name, func(t *testing.T) {
			model := vimReply(t)
			model = typeText(t, model, "Thanks")
			model = vimPress(t, model, keys...)
			model = vimPress(t, model, ctrlSKey)
			if model.form != nil || model.screen != screenCompose || !strings.HasPrefix(model.draft.Body, "Thanks") {
				t.Fatalf("ctrl+s should open the preview: form=%v screen=%v", model.form != nil, model.screen)
			}
		})
	}
}

func TestVimCtrlOHandsOverToTheEditorInNormalMode(t *testing.T) {
	model := vimReply(t)
	model = typeText(t, model, "Thanks")
	updated, command := model.Update(ctrlOKey)
	model = updated.(Model)
	if command == nil || model.form != nil || !strings.Contains(savedDraft(t), "Thanks") {
		t.Fatalf("ctrl+o should save and run $EDITOR: command=%v form=%v", command != nil, model.form != nil)
	}
}

func TestVimDoubleEscClosesKeepingTheDraft(t *testing.T) {
	model := vimReply(t)
	model = typeText(t, model, "Thanks")
	model = vimPress(t, model, escKey())
	if model.form == nil || !strings.Contains(footer(model), "-- NORMAL --") || strings.Contains(footer(model), "esc again closes") {
		t.Fatalf("the first esc only leaves insert mode: form=%v footer %q", model.form != nil, footer(model))
	}
	model = vimPress(t, model, escKey())
	if model.form == nil || !strings.Contains(footer(model), "esc again closes") {
		t.Fatalf("one esc in normal mode must not close, and says how to: %q", footer(model))
	}
	model = vimPress(t, model, escKey())
	if model.form != nil || !strings.Contains(model.status, "Draft saved") || !strings.Contains(savedDraft(t), "Thanks") {
		t.Fatalf("a second esc in normal mode should close and keep the draft: status %q", model.status)
	}
}

func TestVimColonQClosesAndKeepsTheDraft(t *testing.T) {
	model := vimReply(t)
	model = typeText(t, model, "Thanks")
	model = vimPress(t, model, escKey())
	model = typeText(t, model, ":q")
	model = vimPress(t, model, enterKey)
	if model.form != nil || !strings.Contains(savedDraft(t), "Thanks") {
		t.Fatalf(":q should close with the draft kept: form=%v", model.form != nil)
	}
}

func TestVimColonWithNothingWrittenKeepsNoDraft(t *testing.T) {
	model := vimReply(t)
	model = vimPress(t, model, escKey())
	model = typeText(t, model, ":w")
	model = vimPress(t, model, enterKey)
	if len(draftFiles(t)) != 0 || !strings.Contains(model.status, "Nothing written") {
		t.Fatalf("drafts %v status %q", draftFiles(t), model.status)
	}
}

func TestVimOffKeepsTheOldBehaviour(t *testing.T) {
	for _, value := range []string{"off", "OFF", "0", "false", "no"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("MAILDAY_VIM", value)
			model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
			reply := compose.Reply("gmail", "/mail/gmail/Inbox/cur/1", maildir.Content{FromAddr: "ada@example.org", Subject: "Hello", Body: "Question?", Date: time.Now()}, false)
			updated, _ := model.openComposer("Reply", reply, "", true)
			model = updated.(Model)
			if model.form.vim != nil {
				t.Fatal("modal editing should be off")
			}
			model = typeText(t, model, "Thanks")
			model = vimPress(t, model, tabKey)
			if !strings.HasPrefix(bodyText(model), "Thanks  \n") {
				t.Fatalf("tab should insert two spaces as before: %q", bodyText(model))
			}
			if strings.Contains(footer(model), "INSERT") {
				t.Fatalf("no mode in the footer when off: %q", footer(model))
			}
			model = vimPress(t, model, escKey())
			if model.form != nil || !strings.Contains(model.status, "Draft saved") || !strings.Contains(savedDraft(t), "Thanks") {
				t.Fatalf("esc should close at once: status %q", model.status)
			}
		})
	}
}

func TestVimIsOnByDefaultAndWithOtherValues(t *testing.T) {
	for _, value := range []string{"", "on", "1", "vim"} {
		t.Setenv("MAILDAY_VIM", value)
		if !vimEnabled() {
			t.Errorf("MAILDAY_VIM=%q should leave modal editing on", value)
		}
	}
}

func TestVimTabInsertsSpacesInInsertAndStaysInNormal(t *testing.T) {
	model := vimReply(t)
	model = vimPress(t, model, tabKey)
	model = typeText(t, model, "x")
	if !strings.HasPrefix(bodyText(model), "  x\n") {
		t.Fatalf("tab in insert mode: %q", bodyText(model))
	}
	model = vimPress(t, model, escKey(), tabKey)
	if model.form.field != fieldBody || !strings.HasPrefix(bodyText(model), "  x\n") {
		t.Fatalf("tab in normal mode should change nothing: field %d body %q", model.form.field, bodyText(model))
	}
}

func TestVimShiftTabLeavesTheBodyAndTabComesBackInInsert(t *testing.T) {
	model := vimReply(t)
	model = typeText(t, model, "ab")
	model = vimPress(t, model, escKey(), shiftTabKey)
	if model.form.field != fieldAttach {
		t.Fatalf("shift+tab should go to the headers, field %d", model.form.field)
	}
	model = vimPress(t, model, tabKey)
	if model.form.field != fieldBody || model.form.vim.Mode().String() != "INSERT" {
		t.Fatalf("coming back should start typing: field %d mode %v", model.form.field, model.form.vim.Mode())
	}
	model = typeText(t, model, "X")
	if !strings.HasPrefix(bodyText(model), "aXb\n") && !strings.HasPrefix(bodyText(model), "abX\n") {
		t.Fatalf("typing after coming back: %q", bodyText(model))
	}
	if bodyText(model) != model.form.vim.Text() {
		t.Fatal("the text area and the editor disagree")
	}
}

func TestVimShiftTabWorksInNormalAndInsert(t *testing.T) {
	model := vimReply(t)
	model = vimPress(t, model, shiftTabKey)
	if model.form.field != fieldAttach {
		t.Fatalf("shift+tab in insert mode: field %d", model.form.field)
	}
}

func TestVimPasteInNormalMode(t *testing.T) {
	model := vimReply(t)
	model = typeText(t, model, "ab")
	model = vimPress(t, model, escKey())
	model = vimPress(t, model, tea.PasteMsg{Content: "XY"})
	if !strings.HasPrefix(bodyText(model), "abXY\n") || model.form.vim.Mode().String() != "NORMAL" {
		t.Fatalf("paste in normal mode goes after the cursor: %q mode %v", bodyText(model), model.form.vim.Mode())
	}
	model = typeText(t, model, "u")
	if !strings.HasPrefix(bodyText(model), "ab\n") {
		t.Fatalf("one u should take the paste back: %q", bodyText(model))
	}
}

func TestVimPasteInInsertMode(t *testing.T) {
	model := vimReply(t)
	model = typeText(t, model, "ab")
	model = vimPress(t, model, tea.PasteMsg{Content: "X\r\nY\tZ"})
	if !strings.HasPrefix(bodyText(model), "abX\nY    Z\n") {
		t.Fatalf("pasted CRLF and tab: %q", bodyText(model))
	}
	if bodyText(model) != model.form.vim.Text() {
		t.Fatalf("the text area and the editor disagree: %q vs %q", bodyText(model), model.form.vim.Text())
	}
	if model.form.body.Line() != 1 || model.form.body.Column() != 6 {
		t.Fatalf("cursor after the paste: %d,%d", model.form.body.Line(), model.form.body.Column())
	}
	model = vimPress(t, model, tea.PasteMsg{Content: "\x1b[31mred\x1b[0m"})
	if !strings.Contains(bodyText(model), "Y    Zred") {
		t.Fatalf("escape sequences in a paste should be dropped: %q", bodyText(model))
	}
}

func TestVimSignatureSwapStillWorks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MAILDAY_VIM", "on")
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
	model.book = contacts.Book{Contacts: []contacts.Contact{{Name: "Ruben Bakker", Address: "r.e.bakker@hum.uni.example", Sent: 9}}}
	model = vimPress(t, model, key("c"))
	model = typeText(t, model, "ruben")
	model = vimPress(t, model, tabKey) // accepts the address: From and the signature follow
	if body := bodyText(model); !strings.Contains(body, "Department of Example Studies") || strings.Contains(body, "\n-- \nSam\n") {
		t.Fatalf("a University recipient should bring the University signature: %q", body)
	}
	if model.form.vim.Text() != bodyText(model) {
		t.Fatalf("the editor kept the old signature: %q", model.form.vim.Text())
	}
	// Into the body and on: what is typed lands above the new signature, and
	// a second swap (From by hand) still finds the signature in the editor's text.
	model = vimPress(t, model, tabKey, tabKey, tabKey, tabKey, tabKey)
	if model.form.field != fieldBody {
		t.Fatalf("field %d", model.form.field)
	}
	model = typeText(t, model, "Hi")
	if !strings.HasPrefix(bodyText(model), "Hi\n") || !strings.Contains(bodyText(model), "Department of Example Studies") {
		t.Fatalf("body %q", bodyText(model))
	}
	model = vimPress(t, model, escKey(), shiftTabKey)
	form := model.form
	form.focus(fieldFrom)
	model = vimPress(t, model, tea.KeyPressMsg{Code: tea.KeyLeft})
	if body := bodyText(model); !strings.HasPrefix(body, "Hi\n") || strings.Contains(body, "Department of Example Studies") {
		t.Fatalf("switching From should swap the signature and keep what was typed: %q", body)
	}
	if model.form.vim.Text() != bodyText(model) {
		t.Fatalf("editor %q, text area %q", model.form.vim.Text(), bodyText(model))
	}
	model = vimPress(t, model, tabKey, tabKey, tabKey, tabKey, tabKey, tabKey)
	model = typeText(t, model, "!")
	if !strings.Contains(bodyText(model), "!") || model.form.vim.Text() != bodyText(model) {
		t.Fatalf("typing after the swap: %q", bodyText(model))
	}
}

func TestVimCursorFollowsSoftWrappedLines(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MAILDAY_VIM", "on")
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
	model.width, model.height = 60, 30
	long := strings.Repeat("word ", 40) + "end"
	draft := compose.Draft{Body: long + "\nsecond\nthird"}
	updated, _ := model.openComposer("Reply", draft, "", true)
	model = updated.(Model)
	_ = model.View() // sets the text area's width, so the first line wraps
	model = vimPress(t, model, escKey(), key("j"))
	if model.form.body.Line() != 1 || model.form.body.Column() != 0 {
		t.Fatalf("j over a wrapped line: %d,%d", model.form.body.Line(), model.form.body.Column())
	}
	model = typeText(t, model, "$k")
	if model.form.body.Line() != 0 || model.form.body.Column() != len([]rune(long))-1 {
		t.Fatalf("k back up to the wrapped line: %d,%d", model.form.body.Line(), model.form.body.Column())
	}
	model = typeText(t, model, "0jjx")
	if model.form.body.Line() != 2 || bodyText(model) != long+"\nsecond\nhird" {
		t.Fatalf("x on the last line: line %d body %q", model.form.body.Line(), bodyText(model))
	}
}

func TestVimEditingDeepInALongMessageKeepsTheScroll(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MAILDAY_VIM", "on")
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
	var lines []string
	for i := range 80 {
		lines = append(lines, "line "+string(rune('a'+i%26)))
	}
	updated, _ := model.openComposer("Reply", compose.Draft{Body: strings.Join(lines, "\n")}, "", true)
	model = updated.(Model)
	form := model.form
	form.body.SetWidth(40)
	form.body.SetHeight(8)
	model = vimPress(t, model, escKey())
	model = typeText(t, model, "40G")
	if form.body.Line() != 39 {
		t.Fatalf("40G: line %d", form.body.Line())
	}
	offset := form.body.ScrollYOffset()
	if offset > 39 || offset < 39-7 {
		t.Fatalf("the cursor line is not on screen: offset %d", offset)
	}
	model = typeText(t, model, "Ax")
	if form.body.ScrollYOffset() != offset || form.body.Line() != 39 {
		t.Fatalf("typing moved the scroll: offset %d -> %d, line %d", offset, form.body.ScrollYOffset(), form.body.Line())
	}
	model = vimPress(t, model, escKey())
	model = typeText(t, model, "dd")
	if form.body.ScrollYOffset() != offset || form.body.Line() != 39 {
		t.Fatalf("dd moved the scroll: offset %d -> %d, line %d", offset, form.body.ScrollYOffset(), form.body.Line())
	}
	model = typeText(t, model, "gg")
	if form.body.Line() != 0 || form.body.ScrollYOffset() != 0 {
		t.Fatalf("gg: line %d offset %d", form.body.Line(), form.body.ScrollYOffset())
	}
	model = typeText(t, model, "G")
	if form.body.Line() != 78 || form.body.ScrollYOffset() < 78-7 {
		t.Fatalf("G: line %d offset %d", form.body.Line(), form.body.ScrollYOffset())
	}
}

func TestVimAutosaveSavesWhatWasEdited(t *testing.T) {
	model := vimReply(t)
	model = typeText(t, model, "Thanks")
	model = vimPress(t, model, autosaveTickMsg{seq: model.autosaveSeq})
	if !strings.Contains(savedDraft(t), "Thanks") {
		t.Fatal("autosave should write the body the editor holds")
	}
}

func TestVimVisualFooterAndOperators(t *testing.T) {
	model := vimReply(t)
	model = typeText(t, model, "hello world")
	model = vimPress(t, model, escKey())
	model = typeText(t, model, "0vee")
	if !strings.Contains(footer(model), "-- VISUAL --") {
		t.Fatalf("footer %q", footer(model))
	}
	model = typeText(t, model, "d")
	if !strings.HasPrefix(bodyText(model), "\n") {
		t.Fatalf("body %q", bodyText(model))
	}
	model = typeText(t, model, "Vy")
	if strings.Contains(footer(model), "VISUAL") {
		t.Fatalf("footer after y: %q", footer(model))
	}
}

func TestVimStatusShowsPendingKeys(t *testing.T) {
	model := vimReply(t)
	model = vimPress(t, model, escKey())
	model = typeText(t, model, "2d")
	if !strings.Contains(footer(model), "2d") {
		t.Fatalf("footer %q", footer(model))
	}
}

func TestVimBodyFooterKeepsHeaderFieldFooters(t *testing.T) {
	model := vimReply(t)
	model = vimPress(t, model, shiftTabKey)
	if strings.Contains(footer(model), "INSERT") || !strings.Contains(footer(model), "search people or files") {
		t.Fatalf("the headers keep their own footer: %q", footer(model))
	}
}

func TestVimVisualSelectionIsHighlightedInTheTextArea(t *testing.T) {
	model := vimReply(t)
	model = typeText(t, model, "hello world")
	model = vimPress(t, model, enterKey)
	model = typeText(t, model, "second line")
	model = vimPress(t, model, escKey())
	plain := model.form.body.View()
	model = typeText(t, model, "kk0ve")
	body := &model.form.body
	if body.SelectedText() != "hello" || body.Line() != 0 || body.Column() != 4 {
		t.Fatalf("selection %q, cursor %d,%d", body.SelectedText(), body.Line(), body.Column())
	}
	if body.View() == plain {
		t.Fatal("the selection should change how the text area is drawn")
	}
	model = typeText(t, model, "j")
	if body.SelectedText() != "hello world\nsecon" {
		t.Fatalf("selection across lines %q", body.SelectedText())
	}
	model = vimPress(t, model, escKey())
	if body.HasSelection() {
		t.Fatal("esc should drop the highlight")
	}
	model = typeText(t, model, "ggVj")
	if body.SelectedText() != "hello world\nsecond line" {
		t.Fatalf("linewise selection %q", body.SelectedText())
	}
	model = typeText(t, model, "d")
	if body.HasSelection() || strings.Contains(bodyText(model), "hello") {
		t.Fatalf("after d: %q", bodyText(model))
	}
}

func TestVimSelectionOnWrappedLines(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MAILDAY_VIM", "on")
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
	model.width, model.height = 60, 30
	long := strings.Repeat("word ", 40) + "end"
	updated, _ := model.openComposer("Reply", compose.Draft{Body: "top\n" + long + "\nlast"}, "", true)
	model = updated.(Model)
	_ = model.View()
	model = vimPress(t, model, escKey())
	model = typeText(t, model, "jvG")
	if got := model.form.body.SelectedText(); got != long+"\nl" {
		t.Fatalf("selection %q", got)
	}
	model = vimPress(t, model, escKey())
	model = typeText(t, model, "ggVjd")
	if bodyText(model) != "last" {
		t.Fatalf("body %q", bodyText(model))
	}
}

func TestVimSearchPromptShowsInTheFooterAndEnterJumps(t *testing.T) {
	model := vimReply(t)
	model = typeText(t, model, "one two one")
	model = vimPress(t, model, escKey())
	model = typeText(t, model, "0/one")
	if got := footer(model); !strings.Contains(got, "/one") {
		t.Fatalf("the footer should show the search being typed: %q", got)
	}
	model = vimPress(t, model, enterKey)
	if _, col := model.form.vim.Cursor(); col != 8 {
		t.Fatalf("the search should land on the second one, cursor col %d", col)
	}
	model = typeText(t, model, "0n")
	if _, col := model.form.vim.Cursor(); col != 8 {
		t.Fatalf("n should find it again, cursor col %d", col)
	}
	model = typeText(t, model, "?two")
	if got := footer(model); !strings.Contains(got, "?two") {
		t.Fatalf("a backward search shows its ?: %q", got)
	}
}
