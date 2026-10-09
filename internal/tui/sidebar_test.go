package tui

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/aronvandepol/mailday/internal/maildir"
)

var boxTestNow = time.Date(2026, 9, 30, 12, 0, 0, 0, time.Local)

// boxesModel holds mail in the boxes a real setup has: the Now boxes, a dozen
// project tags (@Travel and @Teaching empty), Drafts, Sent, Archive and Trash.
func boxesModel(t *testing.T, width int) Model {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
	model.now = func() time.Time { return boxTestNow }
	model.width, model.height = width, 40
	model.loadingMail, model.loadingCal = false, false
	model.accounts = []string{"a", "b"}
	model.boxes = []string{
		maildir.InboxBox, "@Reply", "@Waiting",
		"@ESS", "@Admin", "@Conferences", "@Grants", "@Lab", "@Journal", "@EC", "@PeerReview", "@PhD", "@Research", "@Teaching", "@Travel",
		draftsBox, maildir.SentBox, maildir.ArchiveBox, "Trash",
	}
	day := func(days int) time.Time { return boxTestNow.AddDate(0, 0, -days) }
	add := func(account, box string, unread bool, days int) {
		model.messages = append(model.messages, maildir.Message{
			Path: "/m/" + account + "/" + box + "/cur/" + strings.Repeat("x", len(model.messages)+1), Account: account, Box: box,
			Subject: "Subject " + box, Unread: unread, Date: day(days),
		})
	}
	add("a", maildir.InboxBox, true, 0)
	add("a", maildir.InboxBox, true, 1)
	add("b", maildir.InboxBox, true, 1)
	add("a", "@Reply", false, 9)
	add("a", "@ESS", true, 40) // oldest of the projects
	add("a", "@ESS", true, 41)
	add("a", "@Admin", false, 3) // newest
	add("a", "@PhD", true, 10)
	add("b", "@Research", true, 2) // only in account b
	add("a", "@Journal", false, 20)
	add("a", "@Grants", false, 30)
	add("a", "@Conferences", false, 31)
	add("a", "@Lab", false, 32)
	add("a", "@EC", false, 33)
	add("a", "@PeerReview", false, 34)
	add("a", draftsBox, false, 1)
	add("a", maildir.SentBox, false, 1)
	add("a", maildir.ArchiveBox, true, 1)
	add("a", "Trash", true, 1)
	return model
}

func TestBoxesAreGroupedIntoNowProjectsAndMail(t *testing.T) {
	model := boxesModel(t, 100)
	now, projects, mailBoxes := model.navGroups(model.boxStats())
	if want := []string{maildir.InboxBox, "@Reply", "@Waiting"}; !slices.Equal(now, want) {
		t.Errorf("Now = %v, want %v", now, want)
	}
	// Newest first; @Teaching and @Travel have no mail, so they are hidden.
	wantProjects := []string{"@Research", "@Admin", "@PhD", "@Journal", "@Grants", "@Conferences", "@Lab", "@EC", "@PeerReview", "@ESS"}
	if !slices.Equal(projects, wantProjects) {
		t.Errorf("Projects = %v, want %v", projects, wantProjects)
	}
	if want := []string{draftsBox, maildir.SentBox, maildir.ArchiveBox, "Trash"}; !slices.Equal(mailBoxes, want) {
		t.Errorf("Mail = %v, want %v", mailBoxes, want)
	}
	// An account's own view hides the tags it has no mail in.
	model.mailAccount = 1
	_, projects, _ = model.navGroups(model.boxStats())
	if want := []string{"@Research"}; !slices.Equal(projects, want) {
		t.Errorf("account b Projects = %v, want %v", projects, want)
	}
	// The open box stays in its group even when empty, so it can be highlighted.
	model.mailBox = slices.Index(model.boxes, "@Travel")
	_, projects, _ = model.navGroups(model.boxStats())
	if want := []string{"@Research", "@Travel"}; !slices.Equal(projects, want) {
		t.Errorf("with @Travel open: %v, want %v", projects, want)
	}
}

func TestSidebarListsCountsAndHighlightsTheActiveBox(t *testing.T) {
	model := boxesModel(t, 160)
	lines := model.renderSidebar(60)
	plain := make([]string, len(lines))
	for index, line := range lines {
		plain[index] = ansi.Strip(line)
		if got := ansi.StringWidth(line); got != sidebarWidth {
			t.Errorf("line %d is %d wide, want %d: %q", index, got, sidebarWidth, plain[index])
		}
	}
	text := strings.Join(plain, "\n")
	for _, want := range []string{"Now", "Projects", "Mail"} {
		if !strings.Contains(text, want) {
			t.Errorf("sidebar has no %q section:\n%s", want, text)
		}
	}
	row := func(name string) string {
		for _, line := range plain {
			if strings.HasPrefix(strings.TrimSpace(line), name) {
				return strings.TrimSpace(line)
			}
		}
		t.Fatalf("no %s row in\n%s", name, text)
		return ""
	}
	if got := row("Inbox"); !strings.HasSuffix(got, "3") {
		t.Errorf("Inbox row %q should end with its 3 unread", got)
	}
	if got := row("ESS"); !strings.HasSuffix(got, "2") {
		t.Errorf("ESS row %q should end with 2", got)
	}
	for _, name := range []string{"Reply", "Admin", "Sent", "Archive", "Drafts", "Trash"} {
		if got := row(name); got != name {
			t.Errorf("%s row %q should carry no count", name, got)
		}
	}
	if strings.Contains(text, "Travel") || strings.Contains(text, "Teaching") {
		t.Errorf("empty tags should be hidden:\n%s", text)
	}

	// The active box is the selected tab's pill; the others are not.
	pill := strings.SplitN(selectedPillStyle().Render("x"), "x", 2)[0]
	for index, line := range lines {
		isInbox := strings.HasPrefix(strings.TrimSpace(plain[index]), "Inbox")
		if got := strings.Contains(line, pill); got != isInbox {
			t.Errorf("line %q: pill=%v, want %v", plain[index], got, isInbox)
		}
	}
	model.mailBox = slices.Index(model.boxes, "@PhD")
	for index, line := range model.renderSidebar(60) {
		isPhD := strings.HasPrefix(strings.TrimSpace(ansi.Strip(line)), "PhD")
		if got := strings.Contains(line, pill); got != isPhD {
			t.Errorf("PhD open, line %d %q: pill=%v, want %v", index, ansi.Strip(line), got, isPhD)
		}
	}
}

func TestSidebarShrinksToTheHeightKeepingTheActiveBox(t *testing.T) {
	model := boxesModel(t, 160)
	model.mailBox = slices.Index(model.boxes, "Trash")
	for _, height := range []int{25, 15, 8} {
		lines := model.renderSidebar(height)
		if len(lines) > height {
			t.Errorf("height %d: %d lines", height, len(lines))
		}
		if !strings.Contains(ansi.Strip(strings.Join(lines, "\n")), "Trash") {
			t.Errorf("height %d lost the active box:\n%s", height, ansi.Strip(strings.Join(lines, "\n")))
		}
	}
}

func TestMailScreenFitsAtEveryWidth(t *testing.T) {
	for _, width := range []int{60, 80, 100, 129, 130, 160, 200} {
		model := boxesModel(t, width)
		for _, box := range []string{maildir.InboxBox, "@ESS", "Trash"} {
			model.mailBox = slices.Index(model.boxes, box)
			view := model.View().Content
			for index, line := range strings.Split(view, "\n") {
				if got := ansi.StringWidth(line); got > width {
					t.Errorf("width %d, box %s, line %d is %d wide: %q", width, box, index, got, ansi.Strip(line))
				}
			}
		}
	}
}

func TestWideScreenHasSidebarAndNoBoxRowNarrowHasShortRow(t *testing.T) {
	wide := boxesModel(t, 160)
	if !wide.sidebarVisible() {
		t.Fatal("no sidebar at 160 columns")
	}
	view := ansi.Strip(wide.View().Content)
	if !strings.Contains(view, "Projects") || strings.Contains(view, "Projects ▾") {
		t.Errorf("wide view should have the sidebar and no short row:\n%s", view)
	}
	header := ansi.Strip(wide.renderHeader(160))
	if strings.Contains(header, "Waiting") {
		t.Errorf("the header should not draw the box row beside the sidebar:\n%s", header)
	}
	if !strings.Contains(header, "All") || !strings.Contains(header, "a") {
		t.Errorf("the account row stays:\n%s", header)
	}

	narrow := boxesModel(t, 100)
	if narrow.sidebarVisible() {
		t.Fatal("sidebar at 100 columns")
	}
	header = ansi.Strip(narrow.renderHeader(100))
	if !strings.Contains(header, "Projects ▾ 4") {
		t.Errorf("narrow header should show Projects ▾ with the 4 unread across projects:\n%s", header)
	}
	if strings.Contains(header, "ESS") || strings.Contains(header, "‹") || strings.Contains(header, "›") {
		t.Errorf("narrow header should fit without listing tags or overflowing:\n%s", header)
	}
}

func TestNarrowRowNamesTheOpenProject(t *testing.T) {
	model := boxesModel(t, 100)
	items, selected := model.narrowBoxItems()
	labels := make([]string, len(items))
	for index, item := range items {
		labels[index] = item.label
	}
	want := []string{"Inbox 3", "Reply", "Waiting", "Projects ▾ 4", "Drafts", "Sent", "Archive", "Trash"}
	if !slices.Equal(labels, want) {
		t.Fatalf("items = %v, want %v", labels, want)
	}
	if selected != 0 {
		t.Errorf("Inbox open: selected = %d", selected)
	}
	model.mailBox = slices.Index(model.boxes, "@ESS")
	items, selected = model.narrowBoxItems()
	if items[selected].label != "Projects ▾ ESS" {
		t.Errorf("ESS open: %q", items[selected].label)
	}
	model.mailBox = slices.Index(model.boxes, maildir.SentBox)
	items, selected = model.narrowBoxItems()
	if items[selected].label != "Sent" {
		t.Errorf("Sent open: %q", items[selected].label)
	}
	// Nothing unread in projects: the item goes dim without a number.
	for index := range model.messages {
		model.messages[index].Unread = false
	}
	items, _ = model.narrowBoxItems()
	if items[3].label != "Projects ▾" || !items[3].dim {
		t.Errorf("no unread: %+v", items[3])
	}
}

func TestBackslashTogglesTheSidebarForTheSession(t *testing.T) {
	model := boxesModel(t, 160)
	model = press(t, model, "\\")
	if model.sidebarVisible() || !model.boxNav.sidebarOff {
		t.Fatal(`\ should hide the sidebar`)
	}
	if header := ansi.Strip(model.renderHeader(160)); !strings.Contains(header, "Projects ▾") {
		t.Errorf("with the sidebar hidden the short row comes back:\n%s", header)
	}
	// The choice survives a reload and a change of box.
	updated, _ := model.Update(mailLoadedMsg{result: maildir.ListResult{Messages: model.messages, Accounts: model.accounts, Boxes: model.boxes}})
	model = press(t, updated.(Model), "b")
	if model.sidebarVisible() {
		t.Fatal("the sidebar came back by itself")
	}
	model = press(t, model, "\\")
	if !model.sidebarVisible() {
		t.Fatal(`a second \ should show it`)
	}

	// On a narrow terminal the toggle is remembered but says why nothing shows.
	narrow := boxesModel(t, 100)
	narrow = press(t, narrow, "\\")
	narrow = press(t, narrow, "\\")
	if narrow.sidebarVisible() || !strings.Contains(narrow.status, "130") {
		t.Errorf("narrow: visible=%v status=%q", narrow.sidebarVisible(), narrow.status)
	}
	// Not in the calendar.
	calendar := boxesModel(t, 160)
	calendar.focus = paneAgenda
	if calendar = press(t, calendar, "\\"); calendar.boxNav.sidebarOff {
		t.Error(`\ in the calendar should do nothing`)
	}
	// Nor beside the reader.
	reader := boxesModel(t, 160)
	reader.screen = screenMail
	if reader.sidebarVisible() {
		t.Error("the reader has no sidebar")
	}
}

func TestBoxKeysCycleInSidebarOrder(t *testing.T) {
	model := boxesModel(t, 160)
	order := model.navOrder()
	want := []string{
		maildir.InboxBox, "@Reply", "@Waiting",
		"@Research", "@Admin", "@PhD", "@Journal", "@Grants", "@Conferences", "@Lab", "@EC", "@PeerReview", "@ESS",
		draftsBox, maildir.SentBox, maildir.ArchiveBox, "Trash",
	}
	if !slices.Equal(order, want) {
		t.Fatalf("order = %v\nwant    %v", order, want)
	}
	for index := 1; index < len(want); index++ {
		model = press(t, model, "b")
		if got := model.activeBox(); got != want[index] {
			t.Fatalf("b #%d: %s, want %s", index, got, want[index])
		}
	}
	model = press(t, model, "b") // wraps
	if got := model.activeBox(); got != maildir.InboxBox {
		t.Fatalf("b after the last box: %s", got)
	}
	model = press(t, model, "B")
	if got := model.activeBox(); got != "Trash" {
		t.Fatalf("B from Inbox: %s", got)
	}
	// An empty tag opened with g sits at the end of Projects, so b goes on
	// from there.
	model.mailBox = slices.Index(model.boxes, "@Travel")
	if model = press(t, model, "b"); model.activeBox() != draftsBox {
		t.Errorf("b from @Travel: %s", model.activeBox())
	}
	model.mailBox = slices.Index(model.boxes, "@Travel")
	if model = press(t, model, "B"); model.activeBox() != "@ESS" {
		t.Errorf("B from @Travel: %s", model.activeBox())
	}
}

func TestGoLetterStillJumpsToABox(t *testing.T) {
	model := boxesModel(t, 100)
	for key, want := range map[string]string{
		"a": "@ESS", "r": "@Reply", "w": "@Waiting", "S": maildir.SentBox, "x": maildir.ArchiveBox,
		"d": "@Admin", "j": "@Travel", "i": maildir.InboxBox, "D": draftsBox,
	} {
		got := press(t, model, "g", key)
		if got.activeBox() != want || got.boxNav.picker != nil {
			t.Errorf("g %s: box %s (picker %v), want %s", key, got.activeBox(), got.boxNav.picker != nil, want)
		}
	}
}

func TestGoShowsEachBoxLetter(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now()
	messages := []maildir.Message{
		{Path: "/m/a/Inbox/cur/1", Account: "a", Box: maildir.InboxBox, Subject: "x", Date: now},
		{Path: "/m/a/@Reply/cur/2", Account: "a", Box: "@Reply", Subject: "y", Date: now},
		{Path: "/m/a/@ESS/cur/3", Account: "a", Box: "@ESS", Subject: "z", Date: now},
		{Path: "/m/a/@PhD/cur/4", Account: "a", Box: "@PhD", Subject: "w", Date: now},
	}
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
	model.messages = messages
	model.boxes = []string{maildir.InboxBox, "@Reply", "@ESS", "@PhD", maildir.SentBox}
	model.loadingMail, model.loadingCal = false, false
	model.width, model.height = 160, 40
	if model.goKey(maildir.InboxBox) != "i" || model.goKey("@Reply") != "r" || model.goKey("@PhD") != "p p" || model.goKey(maildir.SentBox) != "S" {
		t.Fatalf("keys: inbox %q reply %q phd %q sent %q", model.goKey(maildir.InboxBox), model.goKey("@Reply"), model.goKey("@PhD"), model.goKey(maildir.SentBox))
	}
	before := ansi.Strip(model.View().Content)
	updated, _ := model.Update(key("g"))
	after := ansi.Strip(updated.(Model).View().Content)
	if strings.Contains(before, " i   Inbox") || !strings.Contains(after, " i   Inbox") || !strings.Contains(after, " r   Reply") {
		t.Fatalf("after g the sidebar should show the letters:\n%s", after)
	}
	// Each letter does what it shows.
	updated, _ = updated.(Model).Update(key("r"))
	if updated.(Model).activeBox() != "@Reply" {
		t.Fatalf("g r opened %q", updated.(Model).activeBox())
	}
	// Narrow: the row shows them too.
	model.width = 100
	updated, _ = model.Update(key("g"))
	if !strings.Contains(ansi.Strip(updated.(Model).View().Content), "i Inbox") {
		t.Fatalf("narrow row lacks the letters:\n%s", ansi.Strip(updated.(Model).View().Content))
	}
}
