package tui

import (
	"fmt"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/aronvandepol/mailday/internal/contacts"
	"github.com/aronvandepol/mailday/internal/groups"
	"github.com/aronvandepol/mailday/internal/maildir"
)

func groupsFile(t *testing.T) string {
	t.Helper()
	// Its own home too: a composer test saves drafts there, which other
	// tests would then list.
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "groups.txt")
	t.Setenv("MAILDAY_GROUPS_FILE", path)
	return path
}

func bigMessage() *maildir.Content {
	content := &maildir.Content{Subject: "Board meeting", FromName: "Park, A. (Alice)", FromAddr: "alice@society.example", Body: "Agenda attached."}
	for index := 1; index <= 11; index++ {
		content.ToList = append(content.ToList, maildir.Address{Name: fmt.Sprintf("Person %d", index), Addr: fmt.Sprintf("p%d@society.example", index)})
	}
	content.ToList = append(content.ToList, maildir.Address{Name: "Sam", Addr: "sam.devries@gmail.example"})
	content.CCList = []maildir.Address{{Addr: "secretary@society.example"}}
	return content
}

func TestIListsEveryoneAndGSavesThemAsAGroup(t *testing.T) {
	path := groupsFile(t)
	model := openMessage(readerWith(t, nil))
	model.content = bigMessage()
	model.width, model.height = 120, 60
	view := ansiStrip(model.View().Content)
	if !strings.Contains(view, "and 9 others") || !strings.Contains(view, "i shows all 14 people") {
		t.Fatalf("the short header should say how to see everyone:\n%s", view)
	}
	model = press(t, model, "i")
	view = ansiStrip(model.View().Content)
	for _, want := range []string{"Everyone on this message · 14", "p11@society.example", "secretary@society.example", "Alice Park", "(you)"} {
		if !strings.Contains(view, want) {
			t.Fatalf("missing %q after i:\n%s", want, view)
		}
	}
	// G: everyone but you, under a name typed in the footer.
	model = press(t, model, "G")
	if model.groupPrompt == nil || len(model.groupPrompt.members) != 13 {
		t.Fatalf("prompt %+v", model.groupPrompt)
	}
	model = typeText(t, model, "ESS board")
	if footer := ansiStrip(model.View().Content); !strings.Contains(footer, "Save 13 people as a group") || !strings.Contains(footer, "new group") {
		t.Fatalf("footer:\n%s", footer)
	}
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = run(t, updated.(Model), command)
	if !strings.Contains(model.status, "ESS board: 13 people") {
		t.Fatalf("status %q", model.status)
	}
	saved, err := groups.Load(path)
	if err != nil || len(saved) != 1 || len(saved[0].Members) != 13 || saved[0].Has("sam.devries@gmail.example") || saved[0].Members[0].Name != "Alice Park" {
		t.Fatalf("saved %+v %v", saved, err)
	}
	// Again into the same group adds nobody twice.
	model = press(t, model, "G")
	model = typeText(t, model, "ess board")
	updated, command = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = run(t, updated.(Model), command)
	if model.status != "Everyone is in ess board already" {
		t.Fatalf("status %q", model.status)
	}
}

func TestTypingAGroupNameInToFillsInEveryone(t *testing.T) {
	path := groupsFile(t)
	members := []mail.Address{{Name: "Alice Park", Address: "alice@society.example"}, {Address: "bob@society.example"}, {Name: "Carol", Address: "carol@society.example"}}
	if err := groups.Save(path, []groups.Group{{Name: "ESS board", Members: members}}); err != nil {
		t.Fatal(err)
	}
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
	model.width, model.height = 120, 40
	model = press(t, model, "c")
	model.form.values[fieldTo] = "bob@society.example, "
	model = typeText(t, model, "ess")
	if len(model.form.groupChoices) != 1 || !strings.Contains(ansiStrip(model.View().Content), "◆ ESS board") {
		t.Fatalf("group not offered: %+v\n%s", model.form.groupChoices, ansiStrip(model.View().Content))
	}
	updated, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	model = updated.(Model)
	// One chip, whose two new people (bob was there already) show when collected.
	if got := model.form.values[fieldTo]; got != "bob@society.example, "+chipToken("ESS board")+", " {
		t.Fatalf("to = %q", got)
	}
	if got := model.form.collect().To; got != `bob@society.example, "Alice Park" <alice@society.example>, "Carol" <carol@society.example>` {
		t.Fatalf("collected to = %q", got)
	}
	if model.status != "ESS board: 2 people · 1 already there" {
		t.Fatalf("status %q", model.status)
	}
}

func TestLongRecipientListsWrapInTheComposer(t *testing.T) {
	groupsFile(t)
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
	model.width, model.height = 100, 40
	model = press(t, model, "c")
	var list []string
	for index := 1; index <= 30; index++ {
		list = append(list, fmt.Sprintf("person%d@example.org", index))
	}
	model.form.values[fieldTo] = strings.Join(list, ", ") + ", "
	view := ansiStrip(model.View().Content)
	if !strings.Contains(view, "30 people") || !strings.Contains(view, "person30@example.org") || !strings.Contains(view, "more") {
		t.Fatalf("composer:\n%s", view)
	}
}

func TestGroupsScreen(t *testing.T) {
	path := groupsFile(t)
	members := []mail.Address{{Name: "Alice Park", Address: "alice@society.example"}, {Address: "bob@society.example"}}
	if err := groups.Save(path, []groups.Group{{Name: "Family", Members: []mail.Address{{Address: "mum@home.nl"}}}, {Name: "ESS board", Members: members}}); err != nil {
		t.Fatal(err)
	}
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
	model.width, model.height = 120, 40
	model.loadingMail, model.loadingCal = false, false
	model = press(t, model, "P")
	view := ansiStrip(model.View().Content)
	if !strings.Contains(view, "Recipient groups") || !strings.Contains(view, "ESS board") || !strings.Contains(view, "alice@society.example") {
		t.Fatalf("groups screen:\n%s", view)
	}
	// → into the people, x removes one, u puts them back.
	model = press(t, model, "l")
	updated, command := model.Update(key("x"))
	model = run(t, updated.(Model), command)
	if saved, _ := groups.Load(path); saved[0].Has("alice@society.example") || len(saved[0].Members) != 1 {
		t.Fatalf("after x: %+v", saved)
	}
	updated, command = model.Update(key("u"))
	model = run(t, updated.(Model), command)
	if saved, _ := groups.Load(path); !saved[0].Has("alice@society.example") || saved[0].Members[0].Address != "alice@society.example" {
		t.Fatalf("after u: %+v", saved)
	}
	// r renames.
	model = press(t, model, "h", "r")
	model.groupPrompt.input = ""
	model = typeText(t, model, "Board")
	updated, command = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = run(t, updated.(Model), command)
	if saved, _ := groups.Load(path); saved[0].Name != "Board" {
		t.Fatalf("after rename: %+v", saved)
	}
	// enter writes to the whole group.
	model = press(t, model, "k")
	updated, _ = model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = updated.(Model)
	if model.form == nil || model.groupsView != nil || !strings.Contains(model.form.values[fieldTo], "alice@society.example") || !strings.Contains(model.form.values[fieldTo], "bob@society.example") {
		t.Fatalf("enter should open a new message to the group: %+v", model.form)
	}
	// d d deletes.
	model.form = nil
	model = press(t, model, "P", "j")
	model = press(t, model, "d")
	if !strings.Contains(model.status, "Press d again") {
		t.Fatalf("status %q", model.status)
	}
	updated, command = model.Update(key("d"))
	model = run(t, updated.(Model), command)
	if saved, _ := groups.Load(path); len(saved) != 1 || saved[0].Name != "Board" {
		t.Fatalf("after d d: %+v", saved)
	}
}

func TestCtrlGSavesTheComposersRecipients(t *testing.T) {
	path := groupsFile(t)
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
	model.width, model.height = 120, 40
	model = press(t, model, "c")
	model.form.values[fieldTo] = "a@x.org, b@x.org, "
	model.form.values[fieldCc] = "c@x.org"
	updated, _ := model.Update(tea.KeyPressMsg{Code: 'g', Mod: tea.ModCtrl})
	model = updated.(Model)
	if model.groupPrompt == nil || len(model.groupPrompt.members) != 3 {
		t.Fatalf("prompt %+v", model.groupPrompt)
	}
	model = typeText(t, model, "Trio")
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = run(t, updated.(Model), command)
	if saved, _ := groups.Load(path); len(saved) != 1 || len(saved[0].Members) != 3 || model.form == nil {
		t.Fatalf("saved %+v, composer open %v", saved, model.form != nil)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestGroupsScreenSuggestsGroups(t *testing.T) {
	path := groupsFile(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	if err := groups.Save(path, []groups.Group{{Name: "Family", Members: []mail.Address{{Address: "mum@home.nl"}, {Address: "dad@home.nl"}}}}); err != nil {
		t.Fatal(err)
	}
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
	model.width, model.height = 120, 40
	model.loadingMail, model.loadingCal = false, false
	model = press(t, model, "P")
	kh := contacts.Suggestion{Name: "Journal", Messages: 40, Last: time.Now().Add(-24 * time.Hour), Subjects: []string{"Journal Meeting"},
		Members: []mail.Address{{Name: "Ruben Bakker", Address: "ruben@l.nl"}, {Name: "Stijn Dekker", Address: "stijn@l.nl"}}}
	family := contacts.Suggestion{Name: "Mum & Dad", Messages: 9, Members: []mail.Address{{Address: "mum@home.nl"}, {Address: "dad@home.nl"}}}
	updated, _ := model.Update(groupSuggestionsMsg{list: []contacts.Suggestion{family, kh}})
	model = updated.(Model)
	// Family is a group already, so only Journal is suggested.
	model = press(t, model, "j")
	view := ansiStrip(model.View().Content)
	if !strings.Contains(view, "Suggested") || !strings.Contains(view, "✦ Journal") || strings.Contains(view, "Mum & Dad") ||
		!strings.Contains(view, "40 messages") || !strings.Contains(view, "stijn@l.nl") {
		t.Fatalf("suggestions:\n%s", view)
	}
	// enter: name it (the guess is filled in), and it becomes a group.
	model = press(t, model, "enter")
	if model.groupPrompt == nil || model.groupPrompt.input != "Journal" {
		t.Fatalf("prompt %+v", model.groupPrompt)
	}
	updated, command := model.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	model = run(t, updated.(Model), command)
	if saved, _ := groups.Load(path); len(saved) != 2 || groups.Find(saved, "Journal") < 0 {
		t.Fatalf("saved %+v", saved)
	}
	if len(model.visibleSuggestions()) != 0 {
		t.Fatal("a suggestion that is now a group should go")
	}
	// x waves one away for good.
	other := contacts.Suggestion{Name: "Anouk & Rob", Members: []mail.Address{{Address: "anouk@l.nl"}, {Address: "rob@l.nl"}}}
	updated, _ = model.Update(groupSuggestionsMsg{list: []contacts.Suggestion{other}})
	model = press(t, updated.(Model), "j", "j", "x")
	if len(model.visibleSuggestions()) != 0 || model.status != "Not suggested again" {
		t.Fatalf("after x: %v %q", model.visibleSuggestions(), model.status)
	}
	// A line with one address hides every suggestion with that person.
	if err := dismissSuggestion("stijn@l.nl"); err != nil {
		t.Fatal(err)
	}
	model.groupList, model.suggested = nil, []contacts.Suggestion{kh}
	if got := model.visibleSuggestions(); len(got) != 0 {
		t.Fatalf("a hidden person still suggested: %+v", got)
	}
}
