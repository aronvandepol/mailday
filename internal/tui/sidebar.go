package tui

// Boxes in groups. maildir.OrderBoxes stays the source of the list; Now,
// Projects and Mail are only how it is shown: a sidebar on wide terminals and
// a short row on narrow ones (the box picker, boxpicker.go, covers the rest).

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/aronvandepol/mailday/internal/maildir"
)

const (
	// sidebarMinWidth is the narrowest terminal that gets the sidebar; below
	// it the list would be squeezed too far.
	sidebarMinWidth = 130
	// sidebarWidth includes the blank column between sidebar and list.
	sidebarWidth = 24
)

// boxNavState is what the box navigation remembers for the session.
type boxNavState struct {
	sidebarOff bool       // \ hid the sidebar
	picker     *boxPicker // the project picker, while open
}

type boxStat struct {
	unread int
	total  int
	newest time.Time
}

func isNowBox(box string) bool {
	return box == maildir.InboxBox || box == "@Reply" || box == "@Waiting"
}

func isProjectBox(box string) bool {
	return strings.HasPrefix(box, "@") && !isNowBox(box)
}

// boxHasNoCount: Sent, Archive, Drafts and Trash hold nothing to answer, so
// they carry no unread number.
func boxHasNoCount(box string) bool {
	return box == maildir.SentBox || box == maildir.ArchiveBox || box == draftsBox || isBinBox(box)
}

// boxStats counts the messages of the selected account (every account when
// none is) in one pass.
func (m Model) boxStats() map[string]boxStat {
	stats := make(map[string]boxStat, len(m.boxes))
	for _, message := range m.messages {
		if m.mailAccount >= 0 && m.mailAccount < len(m.accounts) && message.Account != m.accounts[m.mailAccount] {
			continue
		}
		box := messageBox(message)
		stat := stats[box]
		stat.total++
		if message.Unread {
			stat.unread++
		}
		if message.Date.After(stat.newest) {
			stat.newest = message.Date
		}
		stats[box] = stat
	}
	return stats
}

// projectBoxes lists every project tag, the most recently active first and
// the ones without messages last, by name.
func (m Model) projectBoxes(stats map[string]boxStat) []string {
	var projects []string
	for _, box := range m.boxes {
		if isProjectBox(box) {
			projects = append(projects, box)
		}
	}
	sort.SliceStable(projects, func(i, j int) bool {
		a, b := stats[projects[i]], stats[projects[j]]
		if a.total == 0 || b.total == 0 {
			return a.total > 0 && b.total == 0
		}
		return a.newest.After(b.newest)
	})
	return projects
}

// navGroups splits the boxes into Now, Projects and Mail. Projects hold only
// tags with mail in them (and the open one, so it can be highlighted).
func (m Model) navGroups(stats map[string]boxStat) (now, projects, mailBoxes []string) {
	active := m.activeBox()
	for _, box := range m.boxes {
		if isNowBox(box) {
			now = append(now, box)
		}
	}
	for _, box := range m.projectBoxes(stats) {
		if stats[box].total > 0 || box == active {
			projects = append(projects, box)
		}
	}
	for _, box := range []string{draftsBox, maildir.SentBox, maildir.ArchiveBox} {
		if slices.Contains(m.boxes, box) {
			mailBoxes = append(mailBoxes, box)
		}
	}
	for _, box := range m.boxes {
		if !isNowBox(box) && !isProjectBox(box) && !slices.Contains(mailBoxes, box) {
			mailBoxes = append(mailBoxes, box)
		}
	}
	return now, projects, mailBoxes
}

// navOrder is the order b and B cycle through: the sidebar from top to bottom.
func (m Model) navOrder() []string {
	now, projects, mailBoxes := m.navGroups(m.boxStats())
	return append(append(now, projects...), mailBoxes...)
}

// cycleBox moves to the next (or previous) box in navOrder.
func (m *Model) cycleBox(step int) {
	order := m.navOrder()
	if len(order) == 0 {
		return
	}
	at := slices.Index(order, m.activeBox())
	if at < 0 {
		at = 0 // not in the order (no Inbox box): start from the top
		step = 0
	}
	target := order[(at+step+len(order))%len(order)]
	if index := slices.Index(m.boxes, target); index >= 0 {
		m.mailBox = index
		m.mailCursor = 0
		m.status = ""
	}
}

// sidebarVisible: wide enough, on the mail list, and not hidden with \. The
// header drops its box row exactly when this is true.
func (m Model) sidebarVisible() bool {
	return m.width >= sidebarMinWidth && !m.boxNav.sidebarOff &&
		m.focus == paneMail && m.screen == screenHome && len(m.boxes) > 0 &&
		m.form == nil && m.eventForm == nil && m.calPrompt == nil &&
		!m.filing && !m.showHelp && !m.showLog
}

func (m Model) toggleSidebar() (tea.Model, tea.Cmd) {
	m.boxNav.sidebarOff = !m.boxNav.sidebarOff
	switch {
	case m.boxNav.sidebarOff:
		m.status = "Sidebar hidden · \\ shows it"
	case m.width < sidebarMinWidth:
		m.status = fmt.Sprintf("Sidebar on · it shows from %d columns", sidebarMinWidth)
	default:
		m.status = "Sidebar shown · \\ hides it"
	}
	return m, nil
}

// renderMailPane is the mail list, with the sidebar beside it when there is
// room.
func (m Model) renderMailPane(width, height int) string {
	if !m.sidebarVisible() {
		return m.renderMailList(width, height)
	}
	side := m.renderSidebar(height)
	list := strings.Split(m.renderMailList(width-sidebarWidth, height), "\n")
	rows := max(len(side), len(list))
	blank := strings.Repeat(" ", sidebarWidth)
	lines := make([]string, rows)
	for index := range lines {
		left := blank
		if index < len(side) {
			left = side[index]
		}
		if index < len(list) {
			lines[index] = left + list[index]
		} else {
			lines[index] = strings.TrimRight(left, " ")
		}
	}
	return strings.Join(lines, "\n")
}

// renderSidebar draws Now, Projects and Mail, each line sidebarWidth wide.
// When the height is short the blank lines go first, then the lines far from
// the active box.
func (m Model) renderSidebar(height int) []string {
	stats := m.boxStats()
	now, projects, mailBoxes := m.navGroups(stats)
	active := m.activeBox()
	groups := []struct {
		title string
		boxes []string
	}{{"Now", now}, {"Projects", projects}, {"Mail", mailBoxes}}

	build := func(spaced bool) (lines []string, activeLine int) {
		for _, group := range groups {
			if len(group.boxes) == 0 {
				continue
			}
			if spaced && len(lines) > 0 {
				lines = append(lines, strings.Repeat(" ", sidebarWidth))
			}
			lines = append(lines, padTo(sectionHeader(group.title, sidebarWidth-1), sidebarWidth))
			for _, box := range group.boxes {
				if box == active {
					activeLine = len(lines)
				}
				row := sidebarRow(box, stats[box], box == active)
				if m.pendingGo {
					row = sidebarKeyRow(box, m.goKey(box), stats[box], box == active)
				}
				lines = append(lines, row)
			}
		}
		return lines, activeLine
	}
	lines, activeLine := build(true)
	if len(lines) > height {
		lines, activeLine = build(false)
	}
	if len(lines) > height {
		start := min(max(activeLine-height/2, 0), len(lines)-height)
		lines = lines[start : start+height]
	}
	return lines
}

// sidebarRow is one box: its name, and the unread count on the right. The
// active box is filled like the selected tab; an empty one is dim.
func sidebarRow(box string, stat boxStat, active bool) string {
	const inner = sidebarWidth - 1
	count := ""
	if !boxHasNoCount(box) && stat.unread > 0 {
		count = fmt.Sprintf("%d", stat.unread)
	}
	dim := !boxHasNoCount(box) && stat.unread == 0
	right := count
	if right != "" {
		right += " "
	}
	name := truncateToWidth(boxLabel(box), inner-2-displayWidth(right))
	gap := strings.Repeat(" ", max(inner-1-displayWidth(name)-displayWidth(right), 0))
	var row string
	switch {
	case active:
		row = selectedPillStyle().Render(" " + name + gap + right)
	case dim:
		row = lipgloss.NewStyle().Foreground(colorChrome).Faint(true).Render(" " + name + gap)
	default:
		row = lipgloss.NewStyle().Foreground(colorChrome).Render(" "+name+gap) +
			lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(right)
	}
	return row + " "
}

// sidebarKeyRow is a box while g waits for its letter: the letter to press,
// highlighted, in front of the name.
func sidebarKeyRow(box, key string, stat boxStat, active bool) string {
	const inner = sidebarWidth - 1
	keyWidth := 5
	shown := " " + padTo(key, keyWidth-2)
	name := truncateToWidth(boxLabel(box), inner-1-keyWidth)
	gap := strings.Repeat(" ", max(inner-1-keyWidth-displayWidth(name), 0))
	label := goKeyStyle().Render(shown)
	if key == "" {
		label = strings.Repeat(" ", keyWidth-1)
	}
	nameStyle := lipgloss.NewStyle().Foreground(colorBright)
	if active {
		nameStyle = nameStyle.Bold(true)
	}
	return " " + label + " " + nameStyle.Render(name+gap) + " "
}

// narrowBoxItems is the short row for terminals without a sidebar: the Now
// boxes with counts, one Projects item (the unread across projects, or the
// open project's name) and the Mail boxes. selected is the item the open box
// belongs to.
func (m Model) narrowBoxItems() (items []navItem, selected int) {
	stats := m.boxStats()
	active := m.activeBox()
	now, _, mailBoxes := m.navGroups(stats)
	// While g waits for a letter, every box shows it, as in the sidebar.
	shortcut := func(box string) string {
		if m.pendingGo {
			return m.goKey(box)
		}
		return ""
	}
	countedItem := func(box string) navItem {
		label := boxLabel(box)
		if boxHasNoCount(box) {
			return navItem{label: label, goKey: shortcut(box)}
		}
		if unread := stats[box].unread; unread > 0 {
			return navItem{label: fmt.Sprintf("%s %d", label, unread), goKey: shortcut(box)}
		}
		return navItem{label: label, dim: true, goKey: shortcut(box)}
	}
	for _, box := range now {
		if box == active {
			selected = len(items)
		}
		items = append(items, countedItem(box))
	}
	var projects []string
	unread := 0
	for _, box := range m.boxes {
		if isProjectBox(box) {
			projects = append(projects, box)
			unread += stats[box].unread
		}
	}
	if len(projects) > 0 {
		item := navItem{label: "Projects ▾"}
		if m.pendingGo {
			item.goKey = "p"
		}
		switch {
		case isProjectBox(active):
			selected = len(items)
			item.label += " " + boxLabel(active)
		case unread > 0:
			item.label += fmt.Sprintf(" %d", unread)
		default:
			item.dim = true
		}
		items = append(items, item)
	}
	for _, box := range mailBoxes {
		if box == active {
			selected = len(items)
		}
		items = append(items, countedItem(box))
	}
	return items, selected
}
