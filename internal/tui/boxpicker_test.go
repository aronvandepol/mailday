package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/aronvandepol/mailday/internal/maildir"
)

func pickerLabels(model Model) []string {
	var labels []string
	for _, item := range model.boxPickerItems() {
		labels = append(labels, boxLabel(item.box))
	}
	return labels
}

func TestGPOpensThePickerListingEveryProjectWithLettersCountsAndDates(t *testing.T) {
	model := boxesModel(t, 100)
	model = press(t, model, "g", "p")
	if model.boxNav.picker == nil {
		t.Fatal("g p did not open the picker")
	}
	if model.activeBox() != maildir.InboxBox {
		t.Fatalf("g p moved to %s", model.activeBox())
	}
	// Recent first, the empty tags last.
	want := []string{"Research", "Admin", "PhD", "Journal", "Grants", "Conferences", "Lab", "EC", "PeerReview", "ESS", "Teaching", "Travel"}
	if got := pickerLabels(model); !slices.Equal(got, want) {
		t.Fatalf("picker = %v, want %v", got, want)
	}
	view := ansi.Strip(model.View().Content)
	for _, fragment := range []string{"Projects", "p  PhD", "a  ESS", "j  Travel", "enter open"} {
		if !strings.Contains(view, fragment) {
			t.Errorf("picker view lacks %q:\n%s", fragment, view)
		}
	}
	var row string
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "ESS") && strings.Contains(line, "│ ") {
			row = line
		}
	}
	if !strings.Contains(row, "2") || !strings.Contains(row, "21 Aug") {
		t.Errorf("ESS row should show 2 unread and its last date: %q", row)
	}
	// g p works from any box.
	other := press(t, boxesModel(t, 100), "b", "g", "p")
	if other.boxNav.picker == nil {
		t.Error("g p did not open from the Reply box")
	}
}

func TestPickerKeysAreInsideTheMailPane(t *testing.T) {
	calendar := boxesModel(t, 100)
	calendar.focus = paneAgenda
	calendar = press(t, calendar, "g")
	if calendar.pendingGo || calendar.boxNav.picker != nil {
		t.Errorf("g in the calendar should open the date prompt, not the picker")
	}
}

func TestPickerFilterSubstringCaseInsensitive(t *testing.T) {
	model := boxesModel(t, 100)
	model = press(t, model, "g", "p", "/", "E", "s")
	if got := pickerLabels(model); !slices.Equal(got, []string{"Research", "Conferences", "ESS"}) {
		t.Fatalf("filter Es = %v", got)
	}
	// "A" is also a jump key, so "/" first makes it text.
	model = boxesModel(t, 100)
	model = press(t, model, "g", "p", "/", "A", "d")
	if got := pickerLabels(model); !slices.Equal(got, []string{"Admin"}) {
		t.Fatalf("filter Ad = %v", got)
	}
	// A substring, not a prefix.
	model = press(t, model, "backspace", "backspace", "h")
	if got, want := pickerLabels(model), []string{"Research", "PhD", "Teaching"}; !slices.Equal(got, want) {
		t.Fatalf("filter h = %v, want %v", got, want)
	}
	// A key that is no project's letter starts the filter without "/".
	model = boxesModel(t, 100)
	model = press(t, model, "g", "p", "3")
	if !model.boxNav.picker.typing || model.boxNav.picker.filter != "3" {
		t.Errorf("an unbound key should start the filter: %+v", model.boxNav.picker)
	}
	if view := ansi.Strip(model.View().Content); !strings.Contains(view, "No project matches") {
		t.Errorf("empty result should say so:\n%s", view)
	}
	// Enter with no match does nothing; backspace out of the filter restores the letters.
	model = press(t, model, "enter")
	if model.boxNav.picker == nil || model.activeBox() != maildir.InboxBox {
		t.Error("enter on an empty result should leave the picker as it was")
	}
	model = press(t, model, "backspace", "backspace")
	if model.boxNav.picker.typing || len(pickerLabels(model)) != 12 {
		t.Errorf("filter not cleared: %+v", model.boxNav.picker)
	}
}

func TestPickerEnterOpensTheChosenBoxAndEscCloses(t *testing.T) {
	model := boxesModel(t, 100)
	model.query = "leftover"
	model.mailCursor = 3
	model = press(t, model, "g", "p", "down", "down", "enter") // Research, Admin, PhD
	if model.boxNav.picker != nil {
		t.Fatal("enter should close the picker")
	}
	if got := model.activeBox(); got != "@PhD" {
		t.Fatalf("opened %s, want @PhD", got)
	}
	if model.mailCursor != 0 || model.query != "" {
		t.Errorf("cursor %d query %q: opening a box starts at the top without a search", model.mailCursor, model.query)
	}

	// Enter opens the first match of a filter.
	model = boxesModel(t, 100)
	model = press(t, model, "g", "p", "/", "c", "o", "n", "enter")
	if got := model.activeBox(); got != "@Conferences" {
		t.Fatalf("filter con + enter opened %s", got)
	}

	// Up stops at the top, down at the bottom.
	model = boxesModel(t, 100)
	model = press(t, model, "g", "p", "up", "up")
	if model.boxNav.picker.cursor != 0 {
		t.Errorf("cursor above the list: %d", model.boxNav.picker.cursor)
	}
	for range 30 {
		model = press(t, model, "down")
	}
	if got := model.boxNav.picker.cursor; got != 11 {
		t.Errorf("cursor below the list: %d", got)
	}

	// Esc closes without moving.
	model = boxesModel(t, 100)
	model = press(t, model, "b", "g", "p", "down", "esc")
	if model.boxNav.picker != nil || model.activeBox() != "@Reply" {
		t.Errorf("esc: picker=%v box=%s", model.boxNav.picker != nil, model.activeBox())
	}
}

func TestPickerLettersJumpWhileTheFilterIsEmpty(t *testing.T) {
	for letter, want := range map[string]string{"a": "@ESS", "p": "@PhD", "d": "@Admin", "j": "@Travel", "t": "@Teaching"} {
		model := press(t, boxesModel(t, 100), "g", "p", letter)
		if model.boxNav.picker != nil || model.activeBox() != want {
			t.Errorf("%s: box %s picker %v, want %s", letter, model.activeBox(), model.boxNav.picker != nil, want)
		}
	}
	// With a filter started the same letter is text.
	model := press(t, boxesModel(t, 100), "g", "p", "/", "a")
	if model.boxNav.picker == nil || model.boxNav.picker.filter != "a" {
		t.Errorf("a after / should filter: %+v", model.boxNav.picker)
	}
}

func TestPickerFitsEveryWidthAndHeight(t *testing.T) {
	for _, width := range []int{40, 60, 100, 160} {
		for _, height := range []int{24, 40} {
			model := boxesModel(t, width)
			model.height = height
			model = press(t, model, "g", "p")
			lines := strings.Split(model.View().Content, "\n")
			if len(lines) > height {
				t.Errorf("%dx%d: %d lines", width, height, len(lines))
			}
			for index, line := range lines {
				if got := ansi.StringWidth(line); got > width {
					t.Errorf("%dx%d: line %d is %d wide: %q", width, height, index, got, ansi.Strip(line))
				}
			}
			if !strings.Contains(ansi.Strip(model.View().Content), "Projects") {
				t.Errorf("%dx%d: picker not drawn", width, height)
			}
		}
	}
}

func TestPickerWithNoProjectTagsSaysSo(t *testing.T) {
	model := boxesModel(t, 100)
	model.boxes = []string{maildir.InboxBox, "@Reply", maildir.SentBox}
	model = press(t, model, "g", "p")
	if model.boxNav.picker != nil || model.status != "No project tags" {
		t.Errorf("picker=%v status=%q", model.boxNav.picker != nil, model.status)
	}
}
