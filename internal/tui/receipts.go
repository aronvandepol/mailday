package tui

// Read receipts (RFC 8098) that arrive, from clients that send them on their
// own. Mailday does not ask for them or answer requests, so there is no key
// for either here.
//   - Receipt messages are taken out of the loaded mail on every load, so no
//     box, count or search of the list shows them; the files are untouched.
//     The footer says how many are hidden in the box on screen.
//   - The Sent box marks messages somebody confirmed, and the reader of such a
//     message lists who read it and when.

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/aronvandepol/mailday/internal/maildir"
)

type receiptState struct {
	byOriginal map[string][]maildir.Receipt // ReceiptKey of the original's Message-ID
	hidden     map[string]int               // receipts taken out of the list, by account and box
}

// splitReceipts indexes the receipts among messages and returns the rest. It
// copies, because the caller's slice may be shared.
func (m *Model) splitReceipts(messages []maildir.Message) []maildir.Message {
	m.receipts.byOriginal, m.receipts.hidden = map[string][]maildir.Receipt{}, map[string]int{}
	var kept []maildir.Message
	for index, message := range messages {
		if message.Receipt == nil {
			if kept != nil {
				kept = append(kept, message)
			}
			continue
		}
		if kept == nil {
			kept = append(make([]maildir.Message, 0, len(messages)), messages[:index]...)
		}
		m.receipts.hidden[message.Account+"\x00"+messageBox(message)]++
		if key := maildir.ReceiptKey(message.Receipt.OriginalMessageID); key != "" {
			m.receipts.add(key, *message.Receipt)
		}
	}
	if kept == nil {
		return messages
	}
	return kept
}

// add keeps one receipt per recipient, answer and moment: the same one can sit
// in two boxes.
func (s *receiptState) add(key string, receipt maildir.Receipt) {
	same := func(other maildir.Receipt) bool {
		return strings.EqualFold(other.Recipient, receipt.Recipient) && other.Disposition == receipt.Disposition && other.Date.Equal(receipt.Date)
	}
	if !slices.ContainsFunc(s.byOriginal[key], same) {
		s.byOriginal[key] = append(s.byOriginal[key], receipt)
	}
}

// receiptsFor are the receipts that answer the message with this Message-ID,
// earliest first.
func (m Model) receiptsFor(messageID string) []maildir.Receipt {
	if strings.TrimSpace(messageID) == "" {
		return nil
	}
	found := slices.Clone(m.receipts.byOriginal[maildir.ReceiptKey(messageID)])
	slices.SortStableFunc(found, func(a, b maildir.Receipt) int { return a.Date.Compare(b.Date) })
	return found
}

// receiptMark is what a Sent row says about its message: confirmed read, or
// deleted unread when nobody read it.
func (m Model) receiptMark(message maildir.Message) string {
	if messageBox(message) != maildir.SentBox {
		return ""
	}
	mark := ""
	for _, receipt := range m.receiptsFor(message.MessageID) {
		switch {
		case receipt.Displayed():
			return "✓ read"
		case receipt.Deleted():
			mark = "deleted unread"
		}
	}
	return mark
}

// receiptLabel is the footer's note on receipts kept out of the list on screen.
func (m Model) receiptLabel() string {
	if m.focus != paneMail || strings.TrimSpace(m.query) != "" {
		return ""
	}
	box, count := m.activeBox(), 0
	for key, hidden := range m.receipts.hidden {
		account, name, _ := strings.Cut(key, "\x00")
		if name == box && (m.mailAccount < 0 || m.mailAccount >= len(m.accounts) || account == m.accounts[m.mailAccount]) {
			count += hidden
		}
	}
	switch count {
	case 0:
		return ""
	case 1:
		return "1 read receipt hidden"
	}
	return fmt.Sprintf("%d read receipts hidden", count)
}

// receiptPerson names who a receipt came from: the name on the message they
// were sent to, else the one their mail program put on the receipt.
func receiptPerson(content maildir.Content, receipt maildir.Receipt) string {
	for _, address := range append(slices.Clone(content.ToList), content.CCList...) {
		if strings.EqualFold(address.Addr, receipt.Recipient) && address.Name != "" {
			return personName(address.Name, address.Addr)
		}
	}
	if receipt.FromName != "" {
		return receipt.FromName
	}
	if receipt.Recipient != "" {
		return receipt.Recipient
	}
	return "someone"
}

// receiptAnswerLines is the reader's list of receipts for the open message.
func (m Model) receiptAnswerLines(column int) []string {
	if m.content == nil {
		return nil
	}
	id := m.content.MessageID
	if id == "" {
		id = m.readerMessage.MessageID
	}
	var lines []string
	for _, receipt := range m.receiptsFor(id) {
		verb := "Read by "
		switch {
		case receipt.Deleted():
			verb = "Deleted unread by "
		case !receipt.Displayed() && receipt.Disposition != "":
			verb = strings.ToUpper(receipt.Disposition[:1]) + receipt.Disposition[1:] + " by "
		}
		line := verb + receiptPerson(*m.content, receipt)
		if !receipt.Date.IsZero() {
			line += " · " + receiptDate(receipt.Date, m.now())
		}
		lines = append(lines, lipgloss.NewStyle().Foreground(colorPrimary).Render(truncateToWidth(line, column)))
	}
	return lines
}

func receiptDate(date, now time.Time) string {
	date = date.In(time.Local)
	if date.Year() != now.In(time.Local).Year() {
		return date.Format("Mon 2 Jan 2006 15:04")
	}
	return date.Format("Mon 2 Jan 15:04")
}

// receiptLines is who confirmed reading the open message, indented to the
// reading column. It is nil for most messages.
func (m Model) receiptLines(width int) []string {
	column, margin := readerColumn(width)
	if m.screen != screenMail || m.loadingBody {
		return nil
	}
	lines := m.receiptAnswerLines(column)
	if len(lines) > 0 {
		lines = append(lines, "")
	}
	return indentLines(lines, margin)
}
