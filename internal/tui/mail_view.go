package tui

// Mail list rendering is adapted from basecamp/hey-cli/internal/tui/content.go under the MIT license.

import (
	"fmt"
	"net/mail"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/aronvandepol/mailday/internal/maildir"
)

func (m Model) renderMailList(width, height int) string {
	if m.loadingMail {
		return styleMuted.Render("  Reading Maildir...")
	}
	rows := m.mailRows()
	if len(rows) == 0 {
		if m.query != "" {
			return styleMuted.Render("  No messages match the search")
		}
		return styleMuted.Render("  (empty)")
	}

	cursor := bound(m.mailCursor, len(rows))
	m.scrollMailList(height) // a no-op after Update; covers a cursor set without one
	start := bound(m.mailOffset, len(rows))
	var output strings.Builder
	used := 0
	lastSection := -1
	for visible := start; visible < len(rows); visible++ {
		row := rows[visible]
		needsHeader := visible == start || row.section != lastSection
		needed := 2
		if needsHeader {
			needed++
		}
		if used+needed > height {
			break
		}
		if needsHeader {
			output.WriteString(sectionHeader(mailSectionLabel(row.section), width))
			output.WriteByte('\n')
			used++
		}
		line1, line2 := m.renderMailMessage(row, width, mailDateWidth, visible == cursor)
		output.WriteString(line1)
		output.WriteByte('\n')
		output.WriteString(line2)
		output.WriteByte('\n')
		used += 2
		lastSection = row.section
	}
	return strings.TrimRight(output.String(), "\n")
}

// mailDateWidth is the width of the longest date, "30 Sep 2026", so the
// date column does not shift as the list scrolls.
const mailDateWidth = len("30 Sep 2026")

// mailListContext is how many messages stay in view above and below the
// cursor when the list scrolls; small windows keep one.
func mailListContext(height int) int {
	if height < 12 {
		return 1
	}
	return 2
}

// mailWindowEnd is the index after the last row shown when the list starts at
// start and has height lines: each row takes two lines and a section header
// takes one more.
func mailWindowEnd(rows []mailRow, start, height int) int {
	used := 0
	lastSection := -1
	for visible := start; visible < len(rows); visible++ {
		needed := 2
		if visible == start || rows[visible].section != lastSection {
			needed++
		}
		if used+needed > height {
			return visible
		}
		used += needed
		lastSection = rows[visible].section
	}
	return len(rows)
}

// keepMailCursorVisible scrolls the list only when the cursor leaves the
// window (keeping a little context around it), so the cursor is not pinned to
// the bottom row and the list does not shift on every keypress.
func (m *Model) keepMailCursorVisible() {
	if m.screen != screenHome || m.focus != paneMail {
		return
	}
	_, _, _, height := m.layout()
	m.scrollMailList(height)
}

// scrollMailList does the scrolling for a list height.
func (m *Model) scrollMailList(height int) {
	rows := m.mailRows()
	if len(rows) == 0 {
		m.mailOffset = 0
		return
	}
	cursor := bound(m.mailCursor, len(rows))
	context := mailListContext(height)
	start := bound(m.mailOffset, len(rows))
	if cursor-context < start {
		start = max(cursor-context, 0)
	}
	lastWanted := min(cursor+context, len(rows)-1)
	for start < cursor && mailWindowEnd(rows, start, height) <= lastWanted {
		start++
	}
	m.mailOffset = start
}

// mailSection groups the list: unread, read, and in search results the hits
// from Trash and Spam last.
func (m Model) mailSection(message maildir.Message) int {
	if strings.TrimSpace(m.query) != "" && isBinBox(messageBox(message)) {
		return 2
	}
	if messageBox(message) == snoozedBox && strings.TrimSpace(m.query) == "" {
		return 3 // one list in due order, read or not
	}
	if message.Unread {
		return 0
	}
	return 1
}

func mailSectionLabel(section int) string {
	switch section {
	case 0:
		return "New for You"
	case 2:
		return "Trash and Spam"
	case 3:
		return "Snoozed, in order of return"
	}
	return "Previously Seen"
}

// renderMailMessage draws a row: the message, with a count badge when it
// stands for a conversation, or an older message of an opened conversation
// (indented, led by its sender since the subject repeats).
func (m Model) renderMailMessage(row mailRow, width, dateWidth int, selected bool) (string, string) {
	message := m.messages[row.index]
	cursorMarker, cursorText := cursorStyles()
	unreadDot := lipgloss.NewStyle().Foreground(colorAlert).Bold(true)
	subjectStyle := lipgloss.NewStyle().Foreground(colorBright).Bold(true)
	dateStyle := lipgloss.NewStyle().Foreground(colorBright)
	senderStyle := lipgloss.NewStyle().Foreground(colorLink).Bold(true)
	gapStyle := lipgloss.NewStyle()
	if selected {
		unreadDot = selectionStyle(unreadDot)
		gapStyle = selectionStyle(gapStyle)
	}

	emphasize := func(style lipgloss.Style) lipgloss.Style {
		if selected {
			return cursorText
		}
		return style
	}

	const prefixWidth = 4
	textWidth := max(width-prefixWidth-2-dateWidth, 10)
	date := m.listDate(message)
	sender := m.senderLabel(message, !row.child)
	title := message.Subject
	if row.child {
		title = "  ↳ " + sender
	}
	badge := ""
	if row.count() > 1 {
		badge = fmt.Sprintf(" (%d)", row.count())
	}
	subject := truncateToWidth(title, max(textWidth-displayWidth(badge), 1))
	gap := max(textWidth-displayWidth(subject)-displayWidth(badge)+2+dateWidth-displayWidth(date), 1)

	var first strings.Builder
	if selected {
		first.WriteString(cursorMarker.Render("│") + gapStyle.Render(" "))
	} else {
		first.WriteString("  ")
	}
	if row.unread {
		first.WriteString(unreadDot.Render("●") + gapStyle.Render(" "))
	} else {
		first.WriteString(gapStyle.Render("  "))
	}
	first.WriteString(emphasize(subjectStyle).Render(subject))
	if badge != "" {
		first.WriteString(emphasize(styleMuted).Render(badge))
	}
	first.WriteString(gapStyle.Render(strings.Repeat(" ", gap)))
	first.WriteString(emphasize(dateStyle).Render(date))

	var second strings.Builder
	if selected {
		second.WriteString(cursorMarker.Render("│") + gapStyle.Render("     "))
	} else {
		second.WriteString("      ")
	}
	if row.child {
		sender = "" // the first line already names it
	}
	detailWidth := max(width-6, 1)
	excerpt := message.Snippet
	if excerpt == "" && message.To != "" {
		excerpt = "to " + message.To
	}
	if excerpt != "" {
		excerpt = " — " + excerpt
	}
	if row.child {
		excerpt = "  " + strings.TrimPrefix(excerpt, " — ")
	}
	if displayWidth(sender) > detailWidth {
		sender = truncateToWidth(sender, detailWidth)
		excerpt = ""
	} else {
		excerpt = truncateToWidth(excerpt, detailWidth-displayWidth(sender))
	}
	second.WriteString(emphasize(senderStyle).Render(sender))
	second.WriteString(emphasize(styleMuted).Render(excerpt))
	if selected {
		padding := width - displayWidth(second.String())
		if padding > 0 {
			second.WriteString(gapStyle.Render(strings.Repeat(" ", padding)))
		}
	}

	return truncateToWidth(first.String(), width), truncateToWidth(second.String(), width)
}

// senderLabel is the second line's lead: who wrote the message (or to whom
// it went), with its box when that is not the one on screen and, when
// withAccount, its account in the all-accounts view.
func (m Model) senderLabel(message maildir.Message, withAccount bool) string {
	sender := message.From
	if messageBox(message) == maildir.SentBox || isDraft(message) {
		sender = "to " + m.firstRecipient(message.To)
	}
	if mark := m.receiptMark(message); mark != "" { // receipts.go: ✓ read
		sender += "  " + mark
	}
	if messageBox(message) != m.activeBox() {
		sender = boxLabel(message.Box) + " · " + sender
	}
	if withAccount && m.mailAccount < 0 && message.Account != "" {
		sender = message.Account + "@ " + sender
	}
	return sender
}

// listDate is the date column of a list row: the age ("9d") in @Reply and
// @Waiting, where how long a message has waited is what matters, else the
// time for today, the weekday for earlier days of this week, "2 Jan" for this
// year and "2 Jan 2006" before that.
func (m Model) listDate(message maildir.Message) string {
	if messageBox(message) == snoozedBox {
		if date, ok := m.snoozeListDate(message); ok {
			return date
		}
	}
	if box := messageBox(message); box == "@Reply" || box == "@Waiting" {
		return messageAge(message.Date, m.now())
	}
	return formatListDate(message.Date, m.now())
}

func formatListDate(value, now time.Time) string {
	if value.IsZero() {
		return ""
	}
	value, now = value.In(time.Local), now.In(time.Local)
	today := dayOf(now)
	switch {
	case !value.Before(today) && value.Before(today.AddDate(0, 0, 1)):
		return value.Format("15:04")
	case !value.Before(weekStartDate(now, firstWeekday)) && value.Before(today):
		return value.Format("Mon")
	case value.Year() == now.Year():
		return value.Format("2 Jan")
	}
	return value.Format("2 Jan 2006")
}

// messageAge is how long ago a message arrived: minutes, hours, then days.
func messageAge(value, now time.Time) string {
	if value.IsZero() {
		return ""
	}
	age := now.Sub(value)
	switch {
	case age < time.Hour:
		return fmt.Sprintf("%dm", max(int(age/time.Minute), 0))
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh", int(age/time.Hour))
	}
	return fmt.Sprintf("%dd", int(age/(24*time.Hour)))
}

func sectionHeader(label string, width int) string {
	style := lipgloss.NewStyle().Foreground(colorChrome).Bold(true)
	result := style.Render(label)
	if fill := width - displayWidth(label) - 3; fill > 0 {
		result += " " + lipgloss.NewStyle().Foreground(colorChrome).Render(strings.Repeat("─", fill))
	}
	return result
}

func hintedSectionHeader(label, hint string, width int) string {
	rule := lipgloss.NewStyle().Foreground(colorChrome)
	fill := width - displayWidth(label) - displayWidth(hint) - 4
	if fill < 1 {
		return sectionHeader(label, width)
	}
	return rule.Bold(true).Render(label) + " " + rule.Render(strings.Repeat("─", fill)) + " " + styleMuted.Render(hint)
}

func mailCountLabel(count int) string {
	if count == 1 {
		return "1 message"
	}
	return fmt.Sprintf("%d messages", count)
}

// firstRecipient names the first addressee of a sent message, plus a count.
func (m Model) firstRecipient(to string) string {
	list, err := mail.ParseAddressList(to)
	if err != nil || len(list) == 0 {
		return to
	}
	name := personName(list[0].Name, list[0].Address)
	if list[0].Name == "" {
		if known := m.book.NameFor(list[0].Address); known != "" {
			name = known
		}
	}
	if len(list) > 1 {
		name += fmt.Sprintf(" +%d", len(list)-1)
	}
	return name
}
