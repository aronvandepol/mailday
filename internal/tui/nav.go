package tui

// Navigation rendering is adapted from basecamp/hey-cli/internal/tui/nav.go under the MIT license.

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

type navItem struct {
	shortcut string
	label    string
	dim      bool   // nothing to look at in it
	goKey    string // after g: the letter that opens it, shown highlighted
}

func renderRule(width int, label string) string {
	if width <= 0 {
		return ""
	}
	rule := lipgloss.NewStyle().Foreground(colorChrome)
	if label == "" || width < 3 {
		return rule.Render(strings.Repeat("─", width))
	}
	label = truncateToWidth(label, max(width-2, 1))
	padded := " " + label + " "
	ruleLength := max(width-lipgloss.Width(padded), 0)
	left := ruleLength / 2
	right := ruleLength - left
	return rule.Render(strings.Repeat("─", left) + padded + strings.Repeat("─", right))
}

// selectedPillStyle is the inverted fill of the selected tab, which the
// sidebar uses for its active box too.
func selectedPillStyle() lipgloss.Style {
	if noColor {
		return lipgloss.NewStyle().Bold(true).Reverse(true)
	}
	return lipgloss.NewStyle().Bold(true).Foreground(themeBackground).Background(colorPrimary)
}

// renderNavLabel shows a tab: a key that is not part of the label goes in
// front of it, dimmed ("1 Day"). The selected tab is a filled pill; the others
// are plain, so the row reads as tabs rather than underlined links.
func renderNavLabel(label, shortcut string, base lipgloss.Style, selected bool) string {
	key := ""
	if shortcut != "" && !strings.Contains(strings.ToLower(label), strings.ToLower(shortcut)) {
		key = shortcut + " "
	}
	if selected {
		return selectedPillStyle().Render(" " + key + label + " ")
	}
	keyStyle := base.Bold(false).Faint(true)
	return " " + keyStyle.Render(key) + base.Render(label) + " "
}

// goKeyStyle marks the letter to press after g.
func goKeyStyle() lipgloss.Style {
	if noColor {
		return lipgloss.NewStyle().Bold(true).Reverse(true)
	}
	// Its own colour, used for nothing else, so the letters jump out.
	return lipgloss.NewStyle().Foreground(lipgloss.Color("#000000")).Background(lipgloss.Color("#ff5fd7")).Bold(true)
}

func renderNavRow(items []navItem, selected int, width int, centered bool) string {
	const separator = " "
	type renderedItem struct {
		text  string
		width int
	}
	all := make([]renderedItem, len(items))
	totalWidth := 0
	for index, item := range items {
		style := lipgloss.NewStyle().Foreground(colorChrome)
		if item.dim {
			style = style.Faint(true)
		}
		text := renderNavLabel(item.label, item.shortcut, style, index == selected)
		if item.goKey != "" {
			text = goKeyStyle().Render(" "+item.goKey+" ") + text
		}
		all[index] = renderedItem{text: text, width: lipgloss.Width(text)}
		totalWidth += all[index].width
	}
	totalWidth += max(len(items)-1, 0) * lipgloss.Width(separator)
	if totalWidth <= width {
		parts := make([]string, len(all))
		for index := range all {
			parts[index] = all[index].text
		}
		row := strings.Join(parts, separator)
		if centered {
			return centerText(row, width)
		}
		return row
	}
	if selected < 0 || selected >= len(all) {
		selected = 0
	}
	left, right := selected, selected
	used := all[selected].width
	for {
		changed := false
		if left > 0 && used+2+all[left-1].width+2 <= width {
			left--
			used += 2 + all[left].width
			changed = true
		}
		if right+1 < len(all) && used+2+all[right+1].width+2 <= width {
			right++
			used += 2 + all[right].width
			changed = true
		}
		if !changed {
			break
		}
	}
	parts := make([]string, 0, right-left+1)
	for index := left; index <= right; index++ {
		parts = append(parts, all[index].text)
	}
	row := strings.Join(parts, separator)
	arrow := lipgloss.NewStyle().Foreground(colorChrome)
	if left > 0 {
		row = arrow.Render("‹ ") + row
	}
	if right+1 < len(all) {
		row += arrow.Render(" ›")
	}
	if centered {
		return centerText(row, width)
	}
	return row
}

func renderTopRule(width int, label, account string) string {
	ruleStyle := lipgloss.NewStyle().Foreground(colorChrome)
	labelStyle := lipgloss.NewStyle().Foreground(colorChrome).Bold(true)
	labelWidth := lipgloss.Width(label) + 2
	accountWidth := 0
	if account != "" {
		accountWidth = lipgloss.Width(account) + 2
	}
	const tail = 2
	left := min((width-labelWidth)/2, width-labelWidth-accountWidth-tail-1)
	middle := width - left - labelWidth - accountWidth - tail
	if left < 1 || middle < 1 {
		text := label
		if account != "" {
			text += " · " + account
		}
		return renderRule(width, text)
	}
	var output strings.Builder
	output.WriteString(ruleStyle.Render(strings.Repeat("─", left)))
	output.WriteString(" " + labelStyle.Render(label) + " ")
	output.WriteString(ruleStyle.Render(strings.Repeat("─", middle)))
	if account != "" {
		output.WriteString(" " + labelStyle.Render(account) + " ")
	}
	output.WriteString(ruleStyle.Render(strings.Repeat("─", tail)))
	return output.String()
}

func (m Model) mailNavItems() []navItem {
	items := []navItem{{shortcut: "1", label: "All"}}
	for index, account := range m.accounts {
		shortcut := ""
		if index+2 <= 9 {
			shortcut = fmt.Sprintf("%d", index+2)
		}
		items = append(items, navItem{shortcut: shortcut, label: account})
	}
	return items
}

func calendarNavItems() []navItem {
	return []navItem{
		{shortcut: "1", label: "Day"},
		{shortcut: "2", label: "Week"},
		{shortcut: "3", label: "Agenda"},
	}
}

func (m Model) renderHeader(width int) string {
	section := 0
	if m.focus == paneAgenda {
		section = 1
	}
	var output strings.Builder
	output.WriteString(renderTopRule(width, "MAILDAY", m.activeAccountLabel()))
	output.WriteByte('\n')
	output.WriteString(renderNavRow(m.sectionNavItems(), section, width, true))
	output.WriteByte('\n')
	if m.focus == paneMail {
		title := boxLabel(m.activeBox())
		if m.query != "" && !m.filtering {
			title = "Search: every box"
		}
		output.WriteString(renderRule(width, title))
		output.WriteByte('\n')
		output.WriteString(renderNavRow(m.mailNavItems(), m.mailAccount+1, width, true))
		// The sidebar replaces the box row where there is room for it.
		if len(m.boxes) > 1 && !m.sidebarVisible() {
			items, selected := m.narrowBoxItems()
			output.WriteByte('\n')
			output.WriteString(renderNavRow(items, selected, width, true))
		}
		if strip := m.todayStripLine(width); strip != "" {
			output.WriteByte('\n')
			output.WriteString(strip)
		}
	} else {
		output.WriteString(renderRule(width, m.calendarMode.String()))
		output.WriteByte('\n')
		output.WriteString(renderNavRow(calendarNavItems(), int(m.calendarMode), width, true))
	}
	output.WriteByte('\n')
	output.WriteString(renderRule(width, ""))
	return output.String()
}
