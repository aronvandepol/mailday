package tui

import (
	"fmt"
	"net/mail"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/aronvandepol/mailday/internal/contacts"
	"github.com/aronvandepol/mailday/internal/groups"
)

// chipModel is a composer on a groups file holding Journal (four people) and
// Board (two, one shared with Journal).
func chipModel(t *testing.T) Model {
	t.Helper()
	path := groupsFile(t)
	list := []groups.Group{
		{Name: "Journal", Members: []mail.Address{
			{Name: "Alice Park", Address: "alice@kh.org"}, {Address: "bob@kh.org"},
			{Name: "Carol", Address: "carol@kh.org"}, {Address: "dan@kh.org"},
		}},
		{Name: "Board", Members: []mail.Address{{Address: "bob@kh.org"}, {Address: "erin@board.org"}}},
	}
	if err := groups.Save(path, list); err != nil {
		t.Fatal(err)
	}
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
	model.width, model.height = 120, 40
	return press(t, model, "c")
}

func pressCtrl(t *testing.T, model Model, letter rune) Model {
	t.Helper()
	updated, _ := model.Update(tea.KeyPressMsg{Code: letter, Mod: tea.ModCtrl})
	return updated.(Model)
}

func TestChooseGroupInsertsOneChip(t *testing.T) {
	model := chipModel(t)
	model = typeText(t, model, "jo")
	if len(model.form.groupChoices) != 1 {
		t.Fatalf("group not offered: %+v", model.form.groupChoices)
	}
	model = press(t, model, "tab")
	if got := model.form.values[fieldTo]; got != "◆[Journal], " {
		t.Fatalf("to = %q", got)
	}
	if model.status != "Journal: 4 people" {
		t.Fatalf("status %q", model.status)
	}
	view := ansiStrip(model.View().Content)
	if !strings.Contains(view, "◆ Journal (4)") || !strings.Contains(view, "4 people") || strings.Contains(view, "alice@kh.org") {
		t.Fatalf("composer:\n%s", view)
	}
	draft := model.form.collect()
	if draft.To != `"Alice Park" <alice@kh.org>, bob@kh.org, "Carol" <carol@kh.org>, dan@kh.org` {
		t.Fatalf("collected to = %q", draft.To)
	}
}

func TestChipCollectDedupesAcrossFields(t *testing.T) {
	model := chipModel(t)
	form := model.form
	// bob is typed in Cc, so the chip in To leaves him out; Board's bob is
	// already in the Journal chip.
	form.values[fieldTo] = "◆[Journal], ◆[Board], "
	form.values[fieldCc] = "bob@kh.org"
	form.values[fieldBcc] = "Dan <DAN@kh.org>"
	draft := form.collect()
	if draft.To != `"Alice Park" <alice@kh.org>, "Carol" <carol@kh.org>, erin@board.org` {
		t.Fatalf("to = %q", draft.To)
	}
	if draft.Cc != "bob@kh.org" || draft.Bcc != "Dan <DAN@kh.org>" {
		t.Fatalf("cc %q bcc %q", draft.Cc, draft.Bcc)
	}
	_, chips := form.resolve()
	if len(chips[0]) != 2 || len(chips[0][0].members) != 2 || len(chips[0][1].members) != 1 {
		t.Fatalf("chips %+v", chips[0])
	}
	if got := form.composerRecipients(); len(got) != 5 {
		t.Fatalf("recipients %+v", got)
	}
}

func TestChipAnAllDuplicateGroupLeavesNoEntry(t *testing.T) {
	model := chipModel(t)
	form := model.form
	form.values[fieldTo] = "bob@kh.org, ◆[Board], erin@board.org, "
	form.values[fieldCc] = "◆[Board]"
	form.values[fieldTo] = "bob@kh.org, ◆[Board], zed@x.org"
	if got := form.collect().To; got != "bob@kh.org, erin@board.org, zed@x.org" {
		t.Fatalf("to = %q", got)
	}
	form.values[fieldTo] = "bob@kh.org, erin@board.org, "
	form.values[fieldCc] = "◆[Board]"
	if got := form.collect().Cc; got != "" {
		t.Fatalf("cc = %q", got)
	}
}

func TestChooseGroupWithNobodyNewAddsNoChip(t *testing.T) {
	model := chipModel(t)
	model.form.values[fieldTo] = "bob@kh.org, erin@board.org, "
	model = typeText(t, model, "board")
	model = press(t, model, "tab")
	if got := model.form.values[fieldTo]; got != "bob@kh.org, erin@board.org, " || model.status != "Everyone in Board is there already" {
		t.Fatalf("to %q status %q", got, model.status)
	}
}

func TestBackspaceAfterChipRemovesTheWholeChip(t *testing.T) {
	model := chipModel(t)
	model.form.values[fieldTo] = "zed@x.org, ◆[Journal], "
	model = press(t, model, "backspace")
	if got := model.form.values[fieldTo]; got != "zed@x.org, " {
		t.Fatalf("to = %q", got)
	}
	// Typing after a chip, backspace edits the typing, and then the chip goes.
	model.form.values[fieldTo] = "◆[Journal], ab"
	model = press(t, model, "backspace", "backspace")
	if got := model.form.values[fieldTo]; got != "◆[Journal], " {
		t.Fatalf("to = %q", got)
	}
	model = press(t, model, "backspace")
	if got := model.form.values[fieldTo]; got != "" {
		t.Fatalf("to = %q", got)
	}
}

func TestCtrlWTakesAChipWholeAndKeepsEarlierOnes(t *testing.T) {
	for value, want := range map[string]string{
		"◆[Journal], ":                 "",
		"a@x.org, ◆[Journal], b@x.org": "a@x.org, ◆[Journal], ",
		"◆[Journal board], ":           "",
		"◆[Board], ◆[Journal], ":       "◆[Board], ",
		"◆[Board], \"Kim, A\" <a@x>":   "◆[Board], \"Kim, A\" ",
	} {
		if got := deleteWord(value); got != want {
			t.Errorf("deleteWord(%q) = %q, want %q", value, got, want)
		}
	}
}

func TestCtrlEExpandsTheLastChip(t *testing.T) {
	model := chipModel(t)
	model.form.values[fieldTo] = "◆[Board], ◆[Journal], "
	model = pressCtrl(t, model, 'e')
	// The Board chip comes first and keeps bob, so Journal spells out three people.
	if got := model.form.values[fieldTo]; got != `◆[Board], "Alice Park" <alice@kh.org>, "Carol" <carol@kh.org>, dan@kh.org, ` {
		t.Fatalf("to = %q", got)
	}
	if model.status != "Journal: 3 people are plain addresses now" {
		t.Fatalf("status %q", model.status)
	}
	if got := model.form.collect().To; strings.Count(got, "bob@kh.org") != 1 || !strings.Contains(got, "erin@board.org") {
		t.Fatalf("collected %q", got)
	}
	model = pressCtrl(t, model, 'e')
	if want := `bob@kh.org, erin@board.org, "Alice Park" <alice@kh.org>, "Carol" <carol@kh.org>, dan@kh.org, `; model.form.values[fieldTo] != want {
		t.Fatalf("to = %q", model.form.values[fieldTo])
	}
}

func TestCtrlEInAFieldWithoutChipsDoesNothing(t *testing.T) {
	model := chipModel(t)
	model.form.values[fieldTo] = "a@x.org, "
	model = pressCtrl(t, model, 'e')
	if model.form.values[fieldTo] != "a@x.org, " || model.status != "" {
		t.Fatalf("to %q status %q", model.form.values[fieldTo], model.status)
	}
}

func TestMissingGroupChipShowsGoneAndRefusesToPreview(t *testing.T) {
	model := chipModel(t)
	model.form.values[fieldTo] = "a@x.org, "
	model.form.values[fieldCc] = "◆[Gone]"
	model.form.groups = nil // the file lost its groups
	if view := ansiStrip(model.View().Content); !strings.Contains(view, "◆ Gone (gone)") {
		t.Fatalf("composer:\n%s", view)
	}
	// The draft keeps the token rather than quietly dropping people.
	if got := model.form.collect().Cc; got != "◆[Gone]" {
		t.Fatalf("cc = %q", got)
	}
	updated, _ := model.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	model = updated.(Model)
	if model.form == nil {
		t.Fatalf("the composer closed: status %q", model.status)
	}
	if model.status != "No group Gone any more: expand or remove it" || model.form.field != fieldCc {
		t.Fatalf("status %q, field %d", model.status, model.form.field)
	}
	model = pressCtrl(t, model, 'e')
	if !strings.Contains(model.status, "No group Gone any more") || model.form.values[fieldCc] != "◆[Gone]" {
		t.Fatalf("status %q cc %q", model.status, model.form.values[fieldCc])
	}
	model = press(t, model, "backspace")
	if model.form.values[fieldCc] != "" {
		t.Fatalf("cc = %q", model.form.values[fieldCc])
	}
}

func TestChipMissingGroupIsErrorColoured(t *testing.T) {
	text := "◆[Gone], "
	lines := recipientFieldLines("to       ", text, 60, false, styleMuted, "", []chipView{{name: "Gone", missing: true}})
	if len(lines) != 1 || !strings.Contains(ansiStrip(lines[0]), "◆ Gone (gone)") {
		t.Fatalf("lines %q", lines)
	}
}

func TestPreviewCountsChipMembers(t *testing.T) {
	model := chipModel(t)
	model.form.values[fieldTo] = "◆[Journal], "
	model.form.values[fieldCc] = "◆[Board], x@y.org"
	model.form.values[fieldSubject] = "Hello"
	updated, _ := model.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	model = updated.(Model)
	if model.draft == nil || model.form != nil {
		t.Fatalf("no preview: status %q", model.status)
	}
	// Board's bob is in To through Journal, so Cc has erin and x only.
	if model.draft.To == "" || strings.Contains(model.draft.To, "◆") || strings.Contains(model.draft.Cc, "bob@kh.org") {
		t.Fatalf("draft to %q cc %q", model.draft.To, model.draft.Cc)
	}
	view := ansiStrip(model.View().Content)
	if !strings.Contains(view, "to 4 · cc 2") || !strings.Contains(view, "alice@kh.org") {
		t.Fatalf("preview:\n%s", view)
	}
	if got := recipientCount(model.draft.To) + recipientCount(model.draft.Cc); got != 6 {
		t.Fatalf("count %d", got)
	}
}

func TestChipsWrapAsOneUnit(t *testing.T) {
	var chips []chipView
	var text strings.Builder
	for index := 1; index <= 8; index++ {
		name := fmt.Sprintf("Group number %d", index)
		chips = append(chips, chipView{name: name, members: make([]mail.Address, index)})
		text.WriteString(chipToken(name) + ", ")
	}
	lines := recipientFieldLines("to       ", text.String(), 40, true, styleMuted, "█", chips)
	joined := ansiStrip(strings.Join(lines, "\n"))
	if len(lines) < 4 {
		t.Fatalf("did not wrap:\n%s", joined)
	}
	// The 3 lines hidden by the fold keep their people in the count.
	for _, line := range strings.Split(ansiStrip(strings.Join(lines, "\n")), "\n") {
		if strings.Count(line, "◆") != strings.Count(line, "(") {
			t.Fatalf("a chip was split across lines:\n%s", joined)
		}
		if displayWidth(line) > 49 {
			t.Fatalf("line %q is %d wide", line, displayWidth(line))
		}
	}
	if !strings.Contains(joined, "36 people") {
		t.Fatalf("count missing:\n%s", joined)
	}
	if !strings.Contains(joined, "◆ Group number 8 (8)") || !strings.Contains(joined, "more") {
		t.Fatalf("lines:\n%s", joined)
	}
}

func TestChipsAndAddressesShareALine(t *testing.T) {
	chips := []chipView{{name: "Journal", members: make([]mail.Address, 4)}}
	lines := recipientFieldLines("to       ", "a@x.org, ◆[Journal], b@x.org, ", 70, true, styleMuted, "█", chips)
	if len(lines) != 1 {
		t.Fatalf("lines %q", lines)
	}
	if got := ansiStrip(lines[0]); !strings.Contains(got, "a@x.org,  ◆ Journal (4)  b@x.org, █") || !strings.HasSuffix(got, "6 people") {
		t.Fatalf("line %q", got)
	}
}

func TestChosenCountsChipMembers(t *testing.T) {
	model := chipModel(t)
	form := model.form
	form.values[fieldTo] = "a@x.org, ◆[Journal], "
	form.values[fieldCc] = "◆[Board]"
	form.field = fieldTo
	chosen := form.chosen()
	for _, want := range []string{"a@x.org", "alice@kh.org", "dan@kh.org", "erin@board.org"} {
		if !chosen[want] {
			t.Errorf("%s not chosen: %v", want, chosen)
		}
	}
	// The chip being typed after is not finished yet: the token does not count.
	form.values[fieldTo] = "a@x.org, ◆[Journal], ali"
	form.field = fieldTo
	if token := form.token(); token != "ali" {
		t.Fatalf("token %q", token)
	}
	// Suggestions leave out people the chip already adds.
	model.book = contacts.Book{Contacts: []contacts.Contact{
		{Name: "Alice Park", Address: "alice@kh.org", Sent: 3}, {Name: "Alice Other", Address: "alice@other.org", Sent: 1},
	}}
	model.refreshChoices()
	if len(form.choices) != 1 || form.choices[0].Address != "alice@other.org" {
		t.Fatalf("choices %+v", form.choices)
	}
}

func TestChipNameWithACommaIsOneChip(t *testing.T) {
	model := chipModel(t)
	form := model.form
	form.groups = append(form.groups, groups.Group{Name: "Kim, Lee and co", Members: []mail.Address{{Address: "k@x.org"}, {Address: "l@x.org"}}})
	form.values[fieldTo] = "a@x.org, ◆[Kim, Lee and co], "
	form.field = fieldTo
	if token := form.token(); token != "" {
		t.Fatalf("token %q", token)
	}
	if got := form.collect().To; got != "a@x.org, k@x.org, l@x.org" {
		t.Fatalf("to = %q", got)
	}
}

func TestCtrlGSavesChipMembers(t *testing.T) {
	model := chipModel(t)
	model.form.values[fieldTo] = "◆[Journal], "
	model.form.values[fieldCc] = "z@x.org"
	model = pressCtrl(t, model, 'g')
	if model.groupPrompt == nil || len(model.groupPrompt.members) != 5 {
		t.Fatalf("prompt %+v", model.groupPrompt)
	}
}

func TestChipFooterOffersItsKeysOnlyOnAChip(t *testing.T) {
	model := chipModel(t)
	model.form.values[fieldTo] = "◆[Journal], "
	if footer := ansiStrip(model.View().Content); !strings.Contains(footer, "ctrl+e") {
		t.Fatalf("no ctrl+e hint:\n%s", footer)
	}
	model.form.values[fieldTo] = "a@x.org, "
	if footer := ansiStrip(model.View().Content); strings.Contains(footer, "ctrl+e") {
		t.Fatalf("hint without a chip:\n%s", footer)
	}
}
