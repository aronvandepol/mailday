package tui

// The box picker: g p (or the Projects item of the narrow row) opens a box
// over the list with every project tag, its f/g letter, unread count and last
// date. A letter jumps while the filter is empty, exactly as g + letter does;
// "/" or any key that is not a project letter starts the filter instead, so a
// name can be typed even when its first letter is a shortcut.

import (
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

type boxPicker struct {
	filter string
	typing bool // the filter has been started, so letters are text
	cursor int
}

type boxPickerItem struct {
	box  string
	key  string
	stat boxStat
}

// boxPickerItems lists the project tags that match the filter, in the order of
// projectBoxes.
func (m Model) boxPickerItems() []boxPickerItem {
	stats := m.boxStats()
	keys := fileShortcuts(m.boxes) // the letters g uses
	filter := strings.ToLower(m.boxNav.picker.filter)
	var items []boxPickerItem
	for _, box := range m.projectBoxes(stats) {
		if filter != "" && !strings.Contains(strings.ToLower(boxLabel(box)), filter) {
			continue
		}
		key := ""
		if index := slices.Index(m.boxes, box); index >= 0 {
			key = keys[index]
		}
		items = append(items, boxPickerItem{box: box, key: key, stat: stats[box]})
	}
	return items
}

func (m Model) openBoxPicker() (tea.Model, tea.Cmd) {
	m.status = ""
	for _, box := range m.boxes {
		if isProjectBox(box) {
			m.boxNav.picker = &boxPicker{}
			return m, nil
		}
	}
	m.status = "No project tags"
	return m, nil
}

func (m Model) openPickedBox(box string) (tea.Model, tea.Cmd) {
	m.boxNav.picker = nil
	if index := slices.Index(m.boxes, box); index >= 0 {
		m.mailBox = index
		m.mailCursor = 0
		m.query = ""
	}
	return m, nil
}

func (m Model) handleBoxPickerKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	picker := *m.boxNav.picker
	key := message.String()
	items := m.boxPickerItems()
	picker.cursor = bound(picker.cursor, len(items))
	switch key {
	case "esc":
		m.boxNav.picker = nil
		return m, nil
	case "up", "ctrl+p":
		picker.cursor = max(picker.cursor-1, 0)
	case "down", "ctrl+n":
		picker.cursor = min(picker.cursor+1, max(len(items)-1, 0))
	case "enter":
		if picker.cursor < len(items) {
			return m.openPickedBox(items[picker.cursor].box)
		}
	case "backspace":
		if picker.filter == "" {
			picker.typing = false
		} else {
			_, size := utf8.DecodeLastRuneInString(picker.filter)
			picker.filter = picker.filter[:len(picker.filter)-size]
		}
		picker.cursor = 0
	case "ctrl+u":
		picker.filter, picker.typing, picker.cursor = "", false, 0
	default:
		if key == "space" {
			key = " "
		}
		if utf8.RuneCountInString(key) != 1 {
			break
		}
		if !picker.typing && picker.filter == "" {
			if key == "/" {
				picker.typing = true
				break
			}
			for _, item := range items {
				if item.key == key {
					return m.openPickedBox(item.box)
				}
			}
			picker.typing = true
		}
		picker.filter += key
		picker.cursor = 0
	}
	m.boxNav.picker = &picker
	return m, nil
}

// renderBoxPicker is the picker box; bodyHeight limits how many tags show.
func (m Model) renderBoxPicker(width, bodyHeight int) string {
	picker := m.boxNav.picker
	inner := width - 4
	accent := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
	cursorMarker, cursorText := cursorStyles()
	keyStyle := lipgloss.NewStyle().Foreground(colorActive).Bold(true)
	items := m.boxPickerItems()
	cursor := bound(picker.cursor, len(items))

	prompt := styleMuted.Render("letter jumps · / or other keys filter")
	if picker.typing || picker.filter != "" {
		prompt = accent.Render("› ") + picker.filter + lipgloss.NewStyle().Foreground(colorPrimary).Render("█")
	}
	lines := []string{accent.Render("Projects"), "", prompt, ""}

	const dateWidth = len("30 Sep 2026")
	rows := min(max(bodyHeight-9, 3), 16)
	start := min(max(cursor-rows/2, 0), max(len(items)-rows, 0))
	for index := start; index < len(items) && index < start+rows; index++ {
		item := items[index]
		count := ""
		switch {
		case item.stat.unread > 0:
			count = lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(padLeft(strconv.Itoa(item.stat.unread), 4))
		default:
			count = styleMuted.Render(padLeft("", 4))
		}
		date := ""
		if !item.stat.newest.IsZero() {
			date = formatListDate(item.stat.newest, m.now())
		}
		nameWidth := max(inner-2-3-4-1-dateWidth, 8)
		name := padTo(boxLabel(item.box), nameWidth)
		letter := padTo(item.key, 2)
		dateText := padLeft(date, dateWidth)
		if index == cursor {
			lines = append(lines, cursorMarker.Render("│")+" "+cursorText.Render(letter+" "+name)+count+" "+cursorText.Render(dateText))
			continue
		}
		rowStyle := lipgloss.NewStyle().Foreground(colorBright)
		if item.stat.total == 0 {
			rowStyle = styleMuted
		}
		lines = append(lines, "  "+keyStyle.Render(letter)+" "+rowStyle.Render(name)+count+" "+styleMuted.Render(dateText))
	}
	if len(items) == 0 {
		lines = append(lines, styleMuted.Render("  No project matches"))
	}
	lines = append(lines, "", styleMuted.Render("enter open · ↑↓ choose · esc close"))
	for index, text := range lines {
		lines[index] = padTo(truncateToWidth(text, inner), inner)
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorPrimary).Padding(0, 1)
	return box.Render(strings.Join(lines, "\n"))
}

func padLeft(value string, width int) string {
	value = ansi.Truncate(value, width, "")
	return strings.Repeat(" ", max(width-displayWidth(value), 0)) + value
}
