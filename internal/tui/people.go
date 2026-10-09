package tui

// People on a message and recipient groups.
//
// i in the reader lists everyone on the message, name and address, one a
// line: the header itself shows a few names and "and 9 others".
// Groups are named address lists (internal/groups): G in the reader saves the
// message's people as one, ctrl+g in the composer saves who is in To/Cc/Bcc,
// typing a group's name in To/Cc/Bcc offers it, and P lists them to write to,
// rename, prune or delete.

import (
	"fmt"
	"net/mail"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/aronvandepol/mailday/internal/compose"
	"github.com/aronvandepol/mailday/internal/contacts"
	"github.com/aronvandepol/mailday/internal/groups"
	"github.com/aronvandepol/mailday/internal/maildir"
	"github.com/aronvandepol/mailday/internal/terminal"
)

// ---- everyone on the message (i) ----

// recipientPanel is the full From / Reply-To / To / Cc list shown above the
// body after i. It scrolls with the body, so any length can be read.
func recipientPanel(content maildir.Content, column int) []string {
	label := lipgloss.NewStyle().Foreground(colorChrome).Bold(true)
	name := lipgloss.NewStyle().Foreground(colorBright)
	you := styleMuted
	const labelWidth = 10
	nameWidth := min(max((column-labelWidth)*2/5, 14), 30)
	addressWidth := max(column-labelWidth-nameWidth-2, 10)

	sections := []struct {
		title string
		list  []maildir.Address
	}{
		{"from", []maildir.Address{{Name: content.FromName, Addr: content.FromAddr}}},
		{"reply-to", content.ReplyTo},
		{"to", content.ToList},
		{"cc", content.CCList},
	}
	if len(content.ReplyTo) == 1 && strings.EqualFold(content.ReplyTo[0].Addr, content.FromAddr) {
		sections[1].list = nil // the usual case: says nothing new
	}
	total := len(peopleOn(content, false))
	title := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(fmt.Sprintf("Everyone on this message · %d", total))
	lines := []string{fillBetween(title, styleMuted.Render("G saves them as a group · i hides"), column)}
	for _, section := range sections {
		for index, address := range section.list {
			prefix := strings.Repeat(" ", labelWidth)
			if index == 0 {
				prefix = label.Render(padTo(section.title, labelWidth))
			}
			who := personName(address.Name, address.Addr)
			whoStyle := name
			if compose.IsOwn(address.Addr) {
				who, whoStyle = who+" (you)", you
			}
			line := prefix + whoStyle.Render(padTo(truncateToWidth(who, nameWidth), nameWidth)) + "  " + styleMuted.Render(truncateToWidth(address.Addr, addressWidth))
			lines = append(lines, line)
		}
	}
	return append(lines, lipgloss.NewStyle().Foreground(colorChrome).Render(strings.Repeat("─", column)), "")
}

// peopleOn is everyone on the message, From first, each address once.
// withoutYou drops your own addresses (a group of people you write to).
func peopleOn(content maildir.Content, withoutYou bool) []mail.Address {
	var out []mail.Address
	seen := map[string]bool{}
	all := append([]maildir.Address{{Name: content.FromName, Addr: content.FromAddr}}, content.ReplyTo...)
	all = append(append(all, content.ToList...), content.CCList...)
	for _, address := range all {
		key := strings.ToLower(strings.TrimSpace(address.Addr))
		if key == "" || seen[key] || (withoutYou && compose.IsOwn(key)) {
			continue
		}
		seen[key] = true
		out = append(out, mail.Address{Name: personName(address.Name, address.Addr), Address: address.Addr})
	}
	// Reply-To often repeats a list address that is also in To; harmless.
	if len(out) > 0 && strings.EqualFold(out[0].Name, out[0].Address) {
		out[0].Name = ""
	}
	for index := range out {
		if strings.EqualFold(out[index].Name, out[index].Address) {
			out[index].Name = ""
		}
	}
	return out
}

func (m Model) toggleRecipients() (tea.Model, tea.Cmd) {
	if m.content == nil {
		return m, nil
	}
	m.showRecipients = !m.showRecipients
	m.detailScroll = 0
	return m, nil
}

// ---- the name prompt (G, ctrl+g, rename) ----

type groupPromptKind int

const (
	groupSave groupPromptKind = iota
	groupRename
)

type groupPrompt struct {
	kind    groupPromptKind
	members []mail.Address // groupSave: who goes in
	oldName string         // groupRename
	input   string
}

type groupsSavedMsg struct {
	status string
	err    error
}

// startSaveGroup asks for a name for these people.
func (m Model) startSaveGroup(members []mail.Address) (tea.Model, tea.Cmd) {
	if len(members) == 0 {
		m.status = "No one to put in a group"
		return m, nil
	}
	m.loadGroups()
	m.groupPrompt = &groupPrompt{kind: groupSave, members: members}
	m.status = ""
	return m, nil
}

// composerRecipients is who is in To, Cc and Bcc now, group chips expanded.
func (f *headerForm) composerRecipients() []mail.Address {
	var out []mail.Address
	seen := map[string]bool{}
	texts, _ := f.resolve()
	for _, text := range texts {
		for _, address := range recipientsIn(text) {
			if key := strings.ToLower(address.Address); !seen[key] {
				seen[key] = true
				out = append(out, address)
			}
		}
	}
	return out
}

func (m Model) handleGroupPromptKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	prompt := m.groupPrompt
	switch message.String() {
	case "esc":
		m.groupPrompt = nil
		m.status = ""
		return m, nil
	case "tab":
		// Complete to an existing group, to add to it.
		if found := groups.Search(m.groupList, prompt.input); len(found) > 0 {
			prompt.input = found[0].Name
		}
		return m, nil
	case "enter":
		name := strings.TrimSpace(prompt.input)
		if name == "" {
			m.status = "Type a name for the group"
			return m, nil
		}
		if strings.ContainsAny(name, "[]\n") {
			m.status = "A group name cannot hold [ or ]"
			return m, nil
		}
		m.groupPrompt = nil
		path := groupsPath()
		switch prompt.kind {
		case groupRename:
			old := prompt.oldName
			return m, func() tea.Msg {
				err := groups.Update(path, func(list []groups.Group) ([]groups.Group, error) {
					if other := groups.Find(list, name); other >= 0 && !strings.EqualFold(list[other].Name, old) {
						return nil, fmt.Errorf("there is a group %q already", list[other].Name)
					}
					if index := groups.Find(list, old); index >= 0 {
						list[index].Name = name
					}
					return list, nil
				})
				return groupsSavedMsg{status: "Renamed to " + name, err: err}
			}
		default:
			members := prompt.members
			return m, func() tea.Msg {
				var added int
				var created bool
				err := groups.Update(path, func(list []groups.Group) ([]groups.Group, error) {
					list, added, created = groups.Add(list, name, members)
					return list, nil
				})
				status := fmt.Sprintf("Group %s: %s · P shows your groups", name, people(added))
				if !created {
					status = fmt.Sprintf("Added %s to %s", people(added), name)
					if added == 0 {
						status = "Everyone is in " + name + " already"
					}
				}
				return groupsSavedMsg{status: status, err: err}
			}
		}
	case "backspace":
		prompt.input = trimLastRune(prompt.input)
	case "ctrl+u":
		prompt.input = ""
	default:
		if text := message.Key().Text; text != "" && !message.Key().Mod.Contains(tea.ModCtrl) {
			prompt.input += text
		}
	}
	return m, nil
}

func people(count int) string {
	if count == 1 {
		return "1 person"
	}
	return fmt.Sprintf("%d people", count)
}

func (m Model) handleGroupsSaved(message groupsSavedMsg) (tea.Model, tea.Cmd) {
	if message.err != nil {
		m.status = "Groups not saved: " + terminal.SanitizeLine(message.err.Error())
	} else {
		m.status = message.status
	}
	m.loadGroups()
	return m, nil
}

// renderGroupPrompt is the footer while a group name is typed: the name, and
// whether it makes a new group or adds to one.
func (m Model) renderGroupPrompt(width int) string {
	prompt := m.groupPrompt
	accent := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
	input := accent.Render("› ") + terminal.SanitizeLine(prompt.input) + lipgloss.NewStyle().Foreground(colorPrimary).Render("█")
	title, hint := "", ""
	switch prompt.kind {
	case groupRename:
		title = "Rename group " + prompt.oldName
		hint = "enter renames · esc cancels"
	default:
		title = "Save " + people(len(prompt.members)) + " as a group"
		names := make([]string, 0, 4)
		for _, member := range prompt.members {
			if len(names) == 3 {
				names = append(names, fmt.Sprintf("+%d", len(prompt.members)-3))
				break
			}
			names = append(names, contactLabel(member))
		}
		hint = strings.Join(names, ", ")
		if index := groups.Find(m.groupList, prompt.input); index >= 0 {
			hint = "adds to " + m.groupList[index].Name + " (" + people(len(m.groupList[index].Members)) + ") · " + hint
		} else if found := groups.Search(m.groupList, prompt.input); len(found) > 0 && prompt.input != "" {
			hint = "tab adds to " + found[0].Name + " · enter makes a new group · " + hint
		} else {
			hint = "new group · " + hint
		}
	}
	return renderRule(width, title) + "\n" + truncateToWidth(input, width) + "\n" +
		truncateToWidth("  "+lipgloss.NewStyle().Foreground(colorActive).Render(terminal.SanitizeLine(hint)), width)
}

func contactLabel(address mail.Address) string {
	if address.Name != "" {
		return address.Name
	}
	return address.Address
}

// ---- the groups screen (P) ----

type groupsView struct {
	cursor        int
	members       bool // the cursor is in the member list
	memberCursor  int
	confirmDelete string
	removed       *removedMember // x's last removal, for u
}

type removedMember struct {
	group  string
	member mail.Address
	index  int
}

func groupsPath() string { return groups.Path() }

// loadGroups rereads the file: it is small, and another machine or an editor
// may have changed it.
func (m *Model) loadGroups() {
	list, err := groups.Load(groupsPath())
	if err != nil {
		m.groupsError = err.Error()
	} else {
		m.groupsError = ""
	}
	slices.SortStableFunc(list, func(a, b groups.Group) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	m.groupList = list
	if m.form != nil && err == nil {
		m.form.groups = list // chips in the composer follow the file; a broken file keeps what they knew
	}
}

func (m Model) openGroups() (tea.Model, tea.Cmd) {
	m.loadGroups()
	m.groupsView = &groupsView{}
	m.status = ""
	// Suggestions take a moment (every header of three years): look again
	// at most every ten minutes, showing the last ones meanwhile.
	if m.suggesting || len(m.options.MailRoots) == 0 || time.Since(m.suggestedAt) < 10*time.Minute {
		return m, nil
	}
	m.suggesting = true
	roots := m.options.MailRoots
	return m, func() tea.Msg {
		return groupSuggestionsMsg{list: contacts.SuggestGroups(roots, compose.IsOwn, 16, time.Now())}
	}
}

type groupSuggestionsMsg struct{ list []contacts.Suggestion }

func (m Model) handleGroupSuggestions(message groupSuggestionsMsg) (tea.Model, tea.Cmd) {
	m.suggesting = false
	m.suggested, m.suggestedAt = message.list, time.Now()
	return m, nil
}

// visibleSuggestions leaves out what is a group already (mostly the same
// people) and what was waved away with x.
func (m Model) visibleSuggestions() []contacts.Suggestion {
	dismissed := readDismissed()
	var out []contacts.Suggestion
	for _, suggestion := range m.suggested {
		if dismissed[suggestionKey(suggestion)] || slices.ContainsFunc(suggestion.Members, func(member mail.Address) bool {
			return dismissed[strings.ToLower(member.Address)] // a person no longer written to: no group with them
		}) {
			continue
		}
		known := false
		for _, group := range m.groupList {
			both := 0
			for _, member := range suggestion.Members {
				if group.Has(member.Address) {
					both++
				}
			}
			if float64(both) >= 0.6*float64(len(suggestion.Members)+len(group.Members)-both) {
				known = true
				break
			}
		}
		if !known {
			out = append(out, suggestion)
		}
	}
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

func suggestionKey(suggestion contacts.Suggestion) string {
	addresses := make([]string, len(suggestion.Members))
	for index, member := range suggestion.Members {
		addresses[index] = strings.ToLower(member.Address)
	}
	slices.Sort(addresses)
	return strings.Join(addresses, ",")
}

func dismissedPath() string {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "mailday", "group-suggestions-dismissed")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "mailday", "group-suggestions-dismissed")
}

func readDismissed() map[string]bool {
	data, _ := os.ReadFile(dismissedPath())
	out := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out[line] = true
		}
	}
	return out
}

func dismissSuggestion(key string) error {
	path := dismissedPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = file.WriteString(key + "\n")
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

type groupsEditedMsg struct{ err error }

func (m Model) handleGroupsKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	view := m.groupsView
	key := message.String()
	if key != "d" {
		view.confirmDelete = ""
	}
	suggestions := m.visibleSuggestions()
	count := len(m.groupList)
	total := count + len(suggestions)
	view.cursor = bound(view.cursor, total)
	if view.cursor >= count && len(suggestions) > 0 {
		// A suggestion: enter makes it a group under a name you choose.
		suggestion := suggestions[view.cursor-count]
		switch key {
		case "enter", "a", "c":
			m.groupPrompt = &groupPrompt{kind: groupSave, members: suggestion.Members, input: suggestion.Name}
			return m, nil
		case "x":
			if err := dismissSuggestion(suggestionKey(suggestion)); err != nil {
				m.status = "Could not note that: " + terminal.SanitizeLine(err.Error())
			} else {
				m.status = "Not suggested again"
			}
			return m, nil
		case "down", "j":
			view.cursor = min(view.cursor+1, total-1)
			return m, nil
		case "up", "k":
			view.cursor = max(view.cursor-1, 0)
			return m, nil
		case "esc", "q", "P", "e":
		default:
			return m, nil
		}
	}
	var group *groups.Group
	if count > 0 && view.cursor < count {
		group = &m.groupList[view.cursor]
		view.memberCursor = bound(view.memberCursor, len(group.Members))
	}
	switch key {
	case "esc", "q", "P":
		if view.members && key == "esc" {
			view.members = false
			return m, nil
		}
		m.groupsView = nil
		return m, nil
	case "down", "j":
		if view.members && group != nil {
			view.memberCursor = min(view.memberCursor+1, max(len(group.Members)-1, 0))
		} else {
			view.cursor, view.memberCursor = min(view.cursor+1, max(total-1, 0)), 0
		}
	case "up", "k":
		if view.members {
			view.memberCursor = max(view.memberCursor-1, 0)
		} else {
			view.cursor, view.memberCursor = max(view.cursor-1, 0), 0
		}
	case "right", "l", "tab":
		if group != nil && len(group.Members) > 0 {
			view.members = true
		}
	case "left", "h", "shift+tab":
		view.members = false
	case "enter", "c":
		if group == nil {
			return m, nil
		}
		// A new message to the whole group, or to one person in it.
		to := group.Members
		if view.members && len(group.Members) > 0 {
			to = group.Members[view.memberCursor : view.memberCursor+1]
		}
		names := make([]string, len(to))
		for index, member := range to {
			names[index] = groups.FormatMember(member)
		}
		m.groupsView = nil
		draft := m.newDraft()
		draft.To = strings.Join(names, ", ")
		return m.openComposer("New message", draft, "", false)
	case "x":
		if group == nil || !view.members || len(group.Members) == 0 {
			return m, nil
		}
		gone := group.Members[view.memberCursor]
		name, index := group.Name, view.memberCursor
		view.removed = &removedMember{group: name, member: gone, index: index}
		group.Members = slices.Delete(group.Members, index, index+1)
		if len(group.Members) == 0 {
			view.members = false
		}
		return m, saveGroupsCmd(func(list []groups.Group) []groups.Group {
			if at := groups.Find(list, name); at >= 0 {
				list[at].Members = slices.DeleteFunc(list[at].Members, func(member mail.Address) bool { return strings.EqualFold(member.Address, gone.Address) })
			}
			return list
		}, "Removed "+contactLabel(gone)+" from "+name+" · u puts back")
	case "u":
		removed := view.removed
		if removed == nil {
			return m, nil
		}
		view.removed = nil
		return m, saveGroupsCmd(func(list []groups.Group) []groups.Group {
			if at := groups.Find(list, removed.group); at >= 0 && !list[at].Has(removed.member.Address) {
				index := min(removed.index, len(list[at].Members))
				list[at].Members = slices.Insert(list[at].Members, index, removed.member)
			}
			return list
		}, contactLabel(removed.member)+" is back in "+removed.group)
	case "r":
		if group != nil {
			m.groupPrompt = &groupPrompt{kind: groupRename, oldName: group.Name, input: group.Name}
		}
	case "d":
		if group == nil {
			return m, nil
		}
		if view.confirmDelete != group.Name {
			view.confirmDelete = group.Name
			m.status = "Press d again to delete the group " + group.Name + " (the people stay in your contacts)"
			return m, nil
		}
		name := group.Name
		view.confirmDelete = ""
		return m, saveGroupsCmd(func(list []groups.Group) []groups.Group {
			return slices.DeleteFunc(list, func(group groups.Group) bool { return strings.EqualFold(group.Name, name) })
		}, "Deleted the group "+name)
	case "e":
		// The file by hand: adding many people, reordering, fixing a name.
		path := groupsPath()
		if err := groups.Update(path, func(list []groups.Group) ([]groups.Group, error) { return list, nil }); err != nil {
			m.status = "Groups file: " + terminal.SanitizeLine(err.Error())
			return m, nil
		}
		return m, tea.ExecProcess(editorCommand(path), func(err error) tea.Msg { return groupsEditedMsg{err: err} })
	}
	return m, nil
}

func saveGroupsCmd(change func([]groups.Group) []groups.Group, status string) tea.Cmd {
	path := groupsPath()
	return func() tea.Msg {
		err := groups.Update(path, func(list []groups.Group) ([]groups.Group, error) { return change(list), nil })
		return groupsSavedMsg{status: status, err: err}
	}
}

func (m Model) handleGroupsEdited(message groupsEditedMsg) (tea.Model, tea.Cmd) {
	m.loadGroups()
	switch {
	case message.err != nil:
		m.status = "Editor: " + terminal.SanitizeLine(message.err.Error())
	case m.groupsError != "":
		m.status = "Groups file: " + terminal.SanitizeLine(m.groupsError)
	default:
		m.status = fmt.Sprintf("%d groups", len(m.groupList))
	}
	return m, nil
}

// renderGroups is the P box: your groups and, below them, suggested ones
// on the left; the chosen one's people on the right.
func (m Model) renderGroups(width, bodyHeight int) string {
	view := m.groupsView
	inner := width - 4
	accent := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
	suggestedStyle := lipgloss.NewStyle().Foreground(colorActive)
	cursorMarker, cursorText := cursorStyles()
	bright := lipgloss.NewStyle().Foreground(colorBright)
	rows := min(max(bodyHeight-10, 6), 20)
	suggestions := m.visibleSuggestions()

	lines := []string{fillBetween(accent.Render("Recipient groups"), styleMuted.Render(collapseHome(groupsPath())), inner), ""}
	if m.groupsError != "" {
		lines = append(lines, lipgloss.NewStyle().Foreground(colorError).Render(truncateToWidth("The file has a mistake: "+m.groupsError+" · e fixes it", inner)), "")
	}
	if len(m.groupList) == 0 && len(suggestions) == 0 {
		lines = append(lines, bright.Render("No groups yet."), "")
		if m.suggesting {
			lines = append(lines, styleMuted.Render("Looking through your mail for people you write to together…"), "")
		}
		lines = append(lines,
			styleMuted.Render("Make one:"),
			"  "+accent.Render("G")+styleMuted.Render("       in a message: everyone on it"),
			"  "+accent.Render("ctrl+g")+styleMuted.Render("  in the composer: everyone in To, Cc and Bcc"),
			"  "+accent.Render("e")+styleMuted.Render("       here: write the file by hand"),
			"", styleMuted.Render("e edit file · esc close"))
		return m.groupsBox(lines, inner)
	}

	leftWidth := min(max(inner/3, 18), 30)
	rightWidth := inner - leftWidth - 3
	count := len(m.groupList)
	cursor := bound(view.cursor, count+len(suggestions))
	onSuggestion := cursor >= count

	// The left column: groups, a heading, suggestions.
	type leftRow struct {
		text  string
		index int // into groups then suggestions; -1 for a heading
	}
	var all []leftRow
	if count == 0 {
		all = append(all, leftRow{styleMuted.Render("  no groups yet"), -1})
	}
	for index, group := range m.groupList {
		name := padTo(truncateToWidth(group.Name, leftWidth-6), leftWidth-6)
		number := padLeft(fmt.Sprintf("%d", len(group.Members)), 4)
		switch {
		case index == cursor && !view.members:
			all = append(all, leftRow{cursorMarker.Render("│") + " " + cursorText.Render(name+number), index})
		case index == cursor:
			all = append(all, leftRow{"  " + accent.Render(name) + styleMuted.Render(number), index})
		default:
			all = append(all, leftRow{"  " + bright.Render(name) + styleMuted.Render(number), index})
		}
	}
	if len(suggestions) > 0 || m.suggesting {
		all = append(all, leftRow{"", -1}, leftRow{suggestedStyle.Render("Suggested"), -1})
		if m.suggesting && len(suggestions) == 0 {
			all = append(all, leftRow{styleMuted.Render("  looking…"), -1})
		}
	}
	for index, suggestion := range suggestions {
		at := count + index
		name := padTo(truncateToWidth("✦ "+suggestion.Name, leftWidth-6), leftWidth-6)
		number := padLeft(fmt.Sprintf("%d", len(suggestion.Members)), 4)
		if at == cursor {
			all = append(all, leftRow{cursorMarker.Render("│") + " " + cursorText.Render(name+number), at})
			continue
		}
		all = append(all, leftRow{"  " + suggestedStyle.Render(name) + styleMuted.Render(number), at})
	}
	cursorRow := 0
	for row, item := range all {
		if item.index == cursor {
			cursorRow = row
		}
	}
	start := min(max(cursorRow-rows/2, 0), max(len(all)-rows, 0))
	var left []string
	for row := start; row < len(all) && row < start+rows; row++ {
		left = append(left, all[row].text)
	}

	// The right column: the chosen group's or suggestion's people.
	var right []string
	var members []mail.Address
	memberCursor := -1
	if onSuggestion {
		suggestion := suggestions[cursor-count]
		members = suggestion.Members
		right = append(right,
			suggestedStyle.Render(truncateToWidth(fmt.Sprintf("You write to them together · %d messages · last %s", suggestion.Messages, formatListDate(suggestion.Last, m.now())), rightWidth)))
		for _, subject := range suggestion.Subjects {
			right = append(right, styleMuted.Render(truncateToWidth("  “"+terminal.SanitizeLine(subject)+"”", rightWidth)))
		}
		right = append(right, "")
	} else if count > 0 {
		members = m.groupList[cursor].Members
		if view.members {
			memberCursor = bound(view.memberCursor, len(members))
		}
	}
	nameWidth := min(max(rightWidth*2/5, 12), 26)
	room := max(rows-len(right), 3)
	mStart := min(max(memberCursor-room/2, 0), max(len(members)-room, 0))
	for index := mStart; index < len(members) && index < mStart+room; index++ {
		member := members[index]
		who := padTo(truncateToWidth(member.Name, nameWidth), nameWidth)
		address := truncateToWidth(member.Address, max(rightWidth-nameWidth-3, 8))
		if index == memberCursor {
			right = append(right, cursorMarker.Render("│")+" "+cursorText.Render(who+" "+address))
			continue
		}
		right = append(right, "  "+bright.Render(who)+" "+styleMuted.Render(address))
	}
	if !onSuggestion && count > 0 && len(members) == 0 {
		right = append(right, styleMuted.Render("  nobody yet · e adds people"))
	}

	divider := lipgloss.NewStyle().Foreground(colorChrome).Render("│")
	for index := 0; index < max(len(left), len(right)); index++ {
		l, r := "", ""
		if index < len(left) {
			l = left[index]
		}
		if index < len(right) {
			r = right[index]
		}
		lines = append(lines, padTo(l, leftWidth)+" "+divider+" "+r)
	}
	help := "enter write to group · → people · r rename · d delete · e edit file · esc close"
	switch {
	case onSuggestion:
		help = "enter make it a group · x don't suggest again · ↑↓ choose · esc close"
	case view.members:
		help = "enter write to this person · x remove · u undo · ← groups · esc back"
	}
	lines = append(lines, "", styleMuted.Render(help))
	return m.groupsBox(lines, inner)
}

func (m Model) groupsBox(lines []string, inner int) string {
	for index, text := range lines {
		lines[index] = padTo(truncateToWidth(text, inner), inner)
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorPrimary).Padding(0, 1)
	return box.Render(strings.Join(lines, "\n"))
}

// ---- groups in the composer ----

// groupSuggestions are the groups whose name matches what is being typed in
// To, Cc or Bcc; they come before people in the suggestions.
func (m *Model) groupSuggestions(token string) []groups.Group {
	if len([]rune(strings.TrimSpace(token))) < 2 {
		return nil
	}
	found := groups.Search(m.groupList, token)
	if len(found) > 3 {
		found = found[:3]
	}
	return found
}

// acceptGroup puts the group in place of the name being typed, as one chip
// (chips.go). added is who the chip brings, already who was in To, Cc or Bcc
// before. A group with nobody new leaves no chip. A name that cannot be a
// chip token (a hand-edited "]") is spelt out as addresses instead.
func (f *headerForm) acceptGroup(group groups.Group) (added, already int) {
	value := f.values[f.field]
	if index := lastComma(value); index >= 0 {
		value = value[:index+1] + " "
	} else {
		value = ""
	}
	chosen := f.chosen()
	var names []string
	for _, member := range group.Members {
		key := strings.ToLower(member.Address)
		if chosen[key] {
			already++
			continue
		}
		chosen[key] = true
		added++
		names = append(names, groups.FormatMember(member))
	}
	switch {
	case added == 0:
	case strings.ContainsAny(group.Name, "]\n"):
		value += strings.Join(names, ", ") + ", "
	default:
		if groups.Find(f.groups, group.Name) < 0 {
			f.groups = append(f.groups, group)
		}
		value += chipToken(group.Name) + ", "
	}
	f.values[f.field] = value
	return added, already
}

// newDraft is an empty message from the account shown, as c makes.
func (m Model) newDraft() compose.Draft {
	account := ""
	if m.mailAccount >= 0 && m.mailAccount < len(m.accounts) {
		account = m.accounts[m.mailAccount]
	}
	return compose.New(account)
}
