package tui

// Screen composition and the key guide follow basecamp/hey-cli/internal/tui under the MIT license.

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/aronvandepol/mailday/internal/calendar"
	"github.com/aronvandepol/mailday/internal/when"
)

func (m Model) View() tea.View {
	width := max(m.width, 20)
	header, footer, notice, bodyHeight := m.layout()

	var body string
	switch {
	case m.showHelp:
		body = m.renderHelp(width, bodyHeight)
	case m.showLog:
		body = m.renderLog(width, bodyHeight)
	case m.filing:
		body = m.renderFilePicker(width, bodyHeight)
	case m.form != nil:
		body = m.renderForm(width, bodyHeight)
	case m.eventForm != nil:
		body = m.renderEventForm(width, bodyHeight)
	case m.screen == screenCompose:
		body = m.renderCompose(width, bodyHeight)
	default:
		body = m.renderScreen(width, bodyHeight)
	}
	body = fitHeight(body, bodyHeight)
	if m.calPrompt != nil && m.calPrompt.kind == promptNew {
		body = overlay(body, m.renderQuickAdd(min(width-4, 92)), width, bodyHeight)
	} else if m.calPrompt != nil && m.calPrompt.kind == promptMove {
		body = overlay(body, m.renderRebookBox(min(width-4, 80)), width, bodyHeight)
	}
	if m.boxNav.picker != nil {
		body = overlay(body, m.renderBoxPicker(min(width-4, 56), bodyHeight), width, bodyHeight)
	}
	if m.groupsView != nil {
		body = overlay(body, m.renderGroups(min(width-4, 96), bodyHeight), width, bodyHeight)
	}

	if m.threads.conversation != nil && m.screen == screenMail { // threads_conversation.go
		body = overlay(body, m.renderConversation(min(width-4, 90), bodyHeight), width, bodyHeight)
	}

	parts := []string{header}
	if notice != "" {
		parts = append(parts, notice)
	}
	parts = append(parts, body, footer)
	view := tea.NewView(strings.Join(parts, "\n"))
	view.AltScreen = true
	view.WindowTitle = m.windowTitle()
	return view
}

// layout renders the parts around the body and says how many lines are left
// for it.
func (m Model) layout() (header, footer, notice string, bodyHeight int) {
	width := max(m.width, 20)
	height := max(m.height, 8)
	header = m.renderHeader(width)
	footer = m.renderFooter(width)
	notice = m.renderNotice(width)
	bodyHeight = height - lineCount(header) - lineCount(footer)
	if notice != "" {
		bodyHeight -= lineCount(notice)
	}
	return header, footer, notice, max(bodyHeight, 1)
}

func (m Model) renderScreen(width, bodyHeight int) string {
	var body string
	switch m.screen {
	case screenMail:
		body = m.renderMailDetail(width, bodyHeight)
	case screenEvent:
		body = m.renderEventDetail(width, bodyHeight)
	default:
		if m.focus == paneMail {
			body = m.renderMailPane(width, bodyHeight)
		} else {
			body = m.renderCalendar(width, bodyHeight)
		}
	}
	return body
}

func (m Model) renderFilePicker(width, height int) string {
	selected, ok := m.selectedMail()
	if !ok {
		return ""
	}
	cursorMarker, cursorText := cursorStyles()
	item := lipgloss.NewStyle().Foreground(colorBright).Bold(true)
	var output strings.Builder
	output.WriteString(sectionHeader("File "+truncateToWidth(selected.Subject, max(width-30, 10))+m.filingGroupNote(" (", ")")+" to", width))
	output.WriteByte('\n')
	keyStyle := lipgloss.NewStyle().Foreground(colorActive).Bold(true)
	start := max(0, m.fileCursor-(height-3))
	for index := start; index < len(m.fileTargets) && index-start < height-1; index++ {
		label := boxLabel(m.fileTargets[index])
		shortcut := "   "
		if index < len(m.fileKeys) && m.fileKeys[index] != "" {
			shortcut = keyStyle.Render(m.fileKeys[index]) + "  "
		}
		if index == m.fileCursor {
			output.WriteString(cursorMarker.Render("│") + " " + shortcut + cursorText.Render(padTo(label, max(width-7, 1))))
		} else {
			output.WriteString("  " + shortcut + item.Render(label))
		}
		output.WriteByte('\n')
	}
	return strings.TrimRight(output.String(), "\n")
}

func (m Model) renderNotice(width int) string {
	if m.status == "" {
		return ""
	}
	style := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
	if isErrorStatus(m.status) {
		style = lipgloss.NewStyle().Foreground(colorError).Bold(true)
	}
	return centerText(style.Render(truncateToWidth(m.status, width)), width)
}

type helpBinding struct {
	key  string
	desc string
}

func (m Model) renderFooter(width int) string {
	if m.calPrompt != nil && m.calPrompt.kind == promptNew {
		return renderRule(width, "") + "\n" + renderHelpBindings([]helpBinding{{"enter", "add"}, {"tab", "all fields"}, {"shift+tab", "calendar"}, {"esc", "cancel"}}, width)
	}
	if m.calPrompt != nil && m.calPrompt.kind == promptMove {
		return renderRule(width, "") + "\n" + renderHelpBindings([]helpBinding{{"enter", "move"}, {"esc", "cancel"}}, width)
	}
	if m.calPrompt != nil && m.calPrompt.kind == promptGoto {
		return m.renderGotoPrompt(width)
	}
	if m.snoozing != nil {
		return m.renderSnoozePrompt(width)
	}
	if m.groupPrompt != nil {
		return m.renderGroupPrompt(width)
	}
	if m.calPrompt != nil {
		return m.renderCalendarPrompt(width)
	}
	if m.eventForm != nil {
		return m.renderEventFormFooter(width)
	}
	if m.filing {
		return renderRule(width, m.filingGroupNote("Moving ", "")) + "\n" + renderHelpBindings([]helpBinding{{"letter", "file there now"}, {"↑↓", "choose"}, {"enter", "file"}, {"esc", "cancel"}}, width)
	}
	if m.showHelp {
		return renderRule(width, "") + "\n" + renderHelpBindings([]helpBinding{{"any key", "close"}}, width)
	}
	if m.showLog {
		return renderRule(width, "") + "\n" + renderHelpBindings([]helpBinding{{"↑↓", "scroll"}, {"esc", "close"}}, width)
	}
	if m.form != nil {
		if m.form.field == fieldBody && m.form.vim != nil {
			return m.renderVimFooter(width)
		}
		if m.form.field == fieldBody {
			return renderRule(width, "") + "\n" + renderHelpBindings([]helpBinding{{"ctrl+s", "preview & send"}, {"shift+tab", "headers"}, {"ctrl+o", "$EDITOR"}, {"esc", "close, draft kept"}}, width)
		}
		return renderRule(width, "") + "\n" + renderHelpBindings(m.form.headerFooterBindings(), width) // chips.go
	}
	if m.screen == screenCompose {
		return renderRule(width, "") + "\n" + renderHelpBindings([]helpBinding{{"y", "send"}, {"e/esc", "back to writing"}, {"h", "headers"}, {"↑↓", "scroll"}, {"q", "close, draft kept"}, {"?", "keys"}}, width)
	}
	if m.filtering {
		prompt := lipgloss.NewStyle().Foreground(colorChrome).Bold(true).Render("/ ") + m.query + lipgloss.NewStyle().Foreground(colorPrimary).Render("█")
		hint := styleMuted.Render("  enter apply · esc cancel · ctrl+u clear")
		if m.searchStale() {
			// The hits on screen answer an earlier query; the new one is on its way.
			prompt += lipgloss.NewStyle().Foreground(colorActive).Render("  searching…")
		}
		return renderRule(width, "") + "\n" + truncateToWidth(prompt+hint, width)
	}
	var bindings []helpBinding
	switch m.screen {
	case screenMail:
		bindings = m.readerFooterBindings()
	case screenEvent:
		bindings = []helpBinding{{"↑↓", "scroll"}, {"esc/q", "back"}, {"e", "edit"}, {"m", "rebook"}, {"< >", "day"}, {"+ -", "30 min"}, {"d", "delete"}, {"a ~ x", "answer"}, {"o", calendarAppName()}, {"ctrl+c", "quit"}}
		if event, ok := m.selectedEvent(); ok && meetingLink(event) != "" {
			bindings = append([]helpBinding{{"J", "join"}}, bindings...)
		}
	default:
		if m.focus == paneMail {
			bindings = []helpBinding{{"↑↓", "navigate"}, {"enter", "open"}, {"tab", "Calendar"}, {"←→", "account"}, {"b/B", "box"}, {"g", "go to box"}, {"c", "write"}, {"R", "reply"}, {"f", "file"}, {"d", "delete"}, {"/", "search"}, {"m", "seen/unseen"}, {"a a", "archive"}, {"?", "keys"}, {"!", "messages"}, {"s", "sync"}, {"r", "reload"}, {"q", "quit"}}
		} else {
			bindings = []helpBinding{{"↑↓", "event"}, {"enter", "open"}, {"n N", "new"}, {"e", "edit"}, {"m", "rebook"}, {"< >", "day"}, {"+ -", "30 min"}, {"d", "delete"}, {"a ~ x", "answer"}, {"←→", strings.ToLower(m.calendarMode.String())}, {"1 2 3", "day week agenda"}, {"t", "today"}, {"tab", "Mail"}, {"o", calendarAppName()}, {"q", "quit"}}
			if event, ok := m.selectedEvent(); ok && meetingLink(event) != "" {
				bindings = append([]helpBinding{{"J", "join"}}, bindings...)
			}
			if _, ok := m.selectedUpcoming(); ok {
				bindings = m.upcomingFooterBindings() // upcoming.go: a snoozed row, not an event
			}
		}
	}
	label := ""
	if m.screen == screenHome {
		label = m.syncLabel()
		if m.live.watchFailures() > 0 {
			// Some folders could not be watched (out of inotify watches or
			// descriptors): "live" would claim more than is true.
			if label == "live" {
				label = ""
			}
			label = joinLabels(label, "watching partially")
		}
		if m.searchStale() {
			label = joinLabels("searching…", label)
		}
		label = joinLabels(label, m.receiptLabel()) // receipts.go
	}
	label = joinLabels(label, m.awayLabel())
	return renderRule(width, label) + "\n" + renderHelpBindings(bindings, width)
}

// joinLabels joins the parts of the footer rule label that are not empty.
func joinLabels(parts ...string) string {
	var kept []string
	for _, part := range parts {
		if part != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, " · ")
}

// renderCalendarPrompt is the one-line event editor: what was typed, and
// below it what Enter will do.
func (m Model) renderCalendarPrompt(width int) string {
	prompt := m.calPrompt
	target := m.defaultCalendar()
	if prompt.kind == promptNew {
		if parsed, err := when.Parse(prompt.input, m.now()); err == nil {
			if name, err := m.matchCalendar(parsed.Calendar); err == nil {
				target = name
			}
		}
	}
	title := map[promptKind]string{promptNew: "New event in " + target, promptMove: "Rebook " + prompt.event.Summary, promptEdit: "Rename " + prompt.event.Summary}[prompt.kind]
	hints := map[promptKind]string{
		promptNew:  "enter add · tab calendar · esc cancel   e.g. Lunch with Ada tomorrow 12:30 1h @Atrium #Work",
		promptMove: "enter move · esc cancel   e.g. fri · 14:00 · fri 10:00 · +1d · -30m · 14:00-15:30 · 2h · next free",
		promptEdit: "enter save · esc cancel   Title @Location",
	}[prompt.kind]
	accent := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
	input := accent.Render("› ") + prompt.input + lipgloss.NewStyle().Foreground(colorPrimary).Render("█")
	preview, ok := m.promptPreview()
	previewStyle := lipgloss.NewStyle().Foreground(colorActive)
	if !ok {
		previewStyle = styleMuted
	}
	if strings.TrimSpace(prompt.input) == "" && prompt.kind != promptEdit {
		preview, previewStyle = hints, styleMuted
	}
	return renderRule(width, truncateToWidth(title, max(width-4, 1))) + "\n" +
		truncateToWidth(input, width) + "\n" +
		truncateToWidth("  "+previewStyle.Render(preview), width)
}

func renderHelpBindings(bindings []helpBinding, width int) string {
	keyStyle := lipgloss.NewStyle().Foreground(colorChrome).Bold(true)
	descStyle := lipgloss.NewStyle().Foreground(colorChrome)
	separator := descStyle.Render(" • ")
	var lines []string
	var line strings.Builder
	lineWidth := 0
	for _, binding := range bindings {
		item := keyStyle.Render(binding.key) + " " + descStyle.Render(binding.desc)
		itemWidth := displayWidth(item)
		separatorWidth := displayWidth(separator)
		if lineWidth > 0 && lineWidth+separatorWidth+itemWidth > width {
			lines = append(lines, line.String())
			line.Reset()
			lineWidth = 0
		}
		if lineWidth > 0 {
			line.WriteString(separator)
			lineWidth += separatorWidth
		}
		line.WriteString(item)
		lineWidth += itemWidth
	}
	if line.Len() > 0 {
		lines = append(lines, line.String())
	}
	return strings.Join(lines, "\n")
}

func (m Model) renderMailDetail(width, height int) string {
	if picker := m.renderOpenPicker(width, height); picker != "" {
		return strings.Join(append(m.renderReaderHeader(m.readerMessage, width), strings.Split(picker, "\n")...), "\n")
	}
	header := m.renderReaderHeader(m.readerMessage, width)
	if m.readerGone {
		// Persistent, unlike the status line, which the next arrival replaces.
		_, margin := readerColumn(width)
		banner := lipgloss.NewStyle().Foreground(colorError).Bold(true).Render("Moved or deleted elsewhere · esc goes back")
		header = append(header, strings.Repeat(" ", margin)+banner, "")
	}
	header = append(header, m.inviteCard(width)...) // invite.go: nil without an invitation
	// receipts.go: who read the message, or its request for a receipt.
	header = append(header, m.receiptLines(width)...)
	body := m.mailDetailBody(width)
	available := max(height-len(header), 1)
	start := min(m.detailScroll, max(len(body)-available, 0))
	end := min(start+available, len(body))
	return strings.Join(append(header, body[start:end]...), "\n")
}

func (m Model) mailDetailBody(width int) []string {
	if m.loadingBody || m.content == nil {
		_, margin := readerColumn(width)
		return []string{strings.Repeat(" ", margin) + styleMuted.Render("Reading message body...")}
	}
	column, margin := readerColumn(width)
	var panel []string
	if m.showRecipients {
		panel = recipientPanel(*m.content, column) // people.go
	}
	if m.bodyCache != nil && m.bodyCacheColumn == column {
		return indentLines(append(panel, m.bodyCache...), margin)
	}
	return indentLines(append(panel, readerBodyLines(*m.content, column, m.showHistory)...), margin)
}

func (m Model) renderEventDetail(width, height int) string {
	event, ok := m.selectedEvent()
	if !ok {
		return lipgloss.NewStyle().Foreground(colorError).Bold(true).Render("Event is no longer in the calendar")
	}
	header := []string{
		centerText(lipgloss.NewStyle().Foreground(colorBright).Bold(true).Render(truncateToWidth(event.Summary, width)), width),
		"",
		lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render(event.Source) + styleMuted.Render("  "+event.Provider),
		lipgloss.NewStyle().Foreground(colorChrome).Render(formatEventRange(event)) + homeSuffix(event, " · "),
	}
	if event.Location != "" {
		header = append(header, "Location  "+truncateToWidth(event.Location, max(width-10, 1)))
	}
	if link := meetingLink(event); link != "" {
		header = append(header, lipgloss.NewStyle().Foreground(colorActive).Bold(true).Render("J")+" joins  "+styleMuted.Render(truncateToWidth(link, max(width-10, 1))))
	}
	if event.Organizer != "" {
		header = append(header, "From      "+event.Organizer+"   "+rsvpLine(event))
	}
	if event.Attendees != "" {
		header = append(header, "With      "+truncateToWidth(event.Attendees, max(width-10, 1)))
	}
	header = append(header, "", renderRule(width, "Notes"))
	bodyText := strings.TrimSpace(event.Description)
	if bodyText == "" {
		bodyText = "No event notes"
	}
	body := wrapText(bodyText, width)
	available := max(height-len(header), 1)
	start := min(m.detailScroll, max(len(body)-available, 0))
	end := min(start+available, len(body))
	return strings.Join(append(header, body[start:end]...), "\n")
}

func (m Model) maxDetailScroll() int {
	width := max(m.width, 20)
	available := max(m.height-12, 1)
	switch m.screen {
	case screenMail:
		return max(len(m.mailDetailBody(width))+len(m.inviteCard(width))-available, 0)
	case screenEvent:
		event, ok := m.selectedEvent()
		if !ok {
			return 0
		}
		body := strings.TrimSpace(event.Description)
		if body == "" {
			body = "No event notes"
		}
		return max(len(wrapText(body, width))-available, 0)
	default:
		return 0
	}
}

func formatDisplayDateTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Local().Format("Jan 2, 2006 15:04")
}

func formatEventRange(event calendar.Event) string {
	start := event.Start.Local()
	end := event.End.Local()
	if event.AllDay {
		last := end.Add(-time.Nanosecond)
		if sameDay(start, last) {
			return start.Format("Monday, January 2, 2006") + " · all day"
		}
		return start.Format("Monday, January 2") + " – " + last.Format("Monday, January 2, 2006")
	}
	if sameDay(start, end) {
		return start.Format("Monday, January 2, 2006 15:04") + " – " + end.Format("15:04")
	}
	return start.Format("Monday, January 2, 2006 15:04") + " – " + end.Format("Monday, January 2, 2006 15:04")
}

func wrapText(value string, width int) []string {
	if width <= 1 {
		return strings.Split(value, "\n")
	}
	var result []string
	for _, line := range strings.Split(value, "\n") {
		if line == "" {
			result = append(result, "")
			continue
		}
		wrapped := ansi.Wordwrap(line, width, "")
		for _, wrappedLine := range strings.Split(wrapped, "\n") {
			hardWrapped := ansi.Hardwrap(wrappedLine, width, true)
			result = append(result, strings.Split(hardWrapped, "\n")...)
		}
	}
	if len(result) == 0 {
		return []string{""}
	}
	return result
}

func truncateToWidth(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if displayWidth(value) <= width {
		return value
	}
	if width <= 3 {
		return ansi.Truncate(value, width, "")
	}
	return ansi.Truncate(value, width, "...")
}

func displayWidth(value string) int {
	return lipgloss.Width(value)
}

func padTo(value string, width int) string {
	value = truncateToWidth(value, width)
	if padding := width - displayWidth(value); padding > 0 {
		value += strings.Repeat(" ", padding)
	}
	return value
}

func centerText(value string, width int) string {
	padding := max((width-displayWidth(value))/2, 0)
	return strings.Repeat(" ", padding) + value
}

func centerPad(value string, width int) string {
	value = truncateToWidth(value, width)
	left := max((width-displayWidth(value))/2, 0)
	right := max(width-displayWidth(value)-left, 0)
	return strings.Repeat(" ", left) + value + strings.Repeat(" ", right)
}

func fillBetween(left, right string, width int) string {
	space := width - displayWidth(left) - displayWidth(right)
	if space < 1 {
		left = truncateToWidth(left, max(width-displayWidth(right)-1, 1))
		space = 1
	}
	return left + strings.Repeat(" ", space) + right
}

func fitHeight(value string, height int) string {
	lines := strings.Split(value, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func lineCount(value string) int {
	if value == "" {
		return 0
	}
	return strings.Count(value, "\n") + 1
}
