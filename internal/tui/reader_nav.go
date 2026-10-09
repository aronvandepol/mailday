package tui

// Moving through mail without going back to the list: n, p and N in the
// reader, auto-advance after an action, and paging in the list. The reader
// walks the list as it stood when the message was opened, so a message that
// slides from New for You into Previously Seen on being read does not change
// what "next" means.

import (
	"os"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/aronvandepol/mailday/internal/maildir"
)

// readerFlow is what the reader remembers between messages.
type readerFlow struct {
	order  []string // Maildir keys of the list when the reader was opened from it
	note   string   // status for once the next message has loaded (after an action)
	picker *openPicker
}

// autoAdvance reports whether an action in the reader opens the next message.
// MAILDAY_AUTO_ADVANCE=off returns to the list instead.
func autoAdvance() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MAILDAY_AUTO_ADVANCE"))) {
	case "off", "0", "no", "false":
		return false
	}
	return true
}

func init() {
	// Folded into the first row of the list, which has no room for more.
	describeHelp("Mail list", "↑↓ j k", "move · pgup pgdn page · n next unread")
	addHelp("Reading a message", [][2]string{
		{"n p N", "next, previous, next unread"}, {"O  l", "open an attachment or a link (1-9)"},
	})
}

// describeHelp replaces the description of one row of the key list.
func describeHelp(title, key, description string) {
	for _, section := range helpSections {
		if section.title != title {
			continue
		}
		for index := range section.bindings {
			if section.bindings[index][0] == key {
				section.bindings[index][1] = description
			}
		}
	}
}

// addHelp adds rows to a section of the key list, which lives in compose.go.
func addHelp(title string, rows [][2]string) {
	for index := range helpSections {
		if helpSections[index].title == title {
			helpSections[index].bindings = append(helpSections[index].bindings, rows...)
			return
		}
	}
}

// snapshotReaderOrder records the list as it is now.
func (m *Model) snapshotReaderOrder() {
	indexes := m.visibleMessageIndexes() // every message, those inside collapsed conversations too
	m.flow.order = make([]string, 0, len(indexes))
	for _, index := range indexes {
		m.flow.order = append(m.flow.order, maildir.Key(m.messages[index].Path))
	}
}

// readerNeighbour finds the message to open from the one with Maildir key
// from: the next in the remembered order (step 1) or the previous (-1),
// skipping anything that has left the list since, and drafts. unreadOnly
// takes only unread messages and wraps round the list; wrapped says it did.
func (m Model) readerNeighbour(from string, step int, unreadOnly bool) (next maildir.Message, wrapped, ok bool) {
	live := make(map[string]int)
	var liveOrder []string
	for _, index := range m.visibleMessageIndexes() {
		key := maildir.Key(m.messages[index].Path)
		live[key] = index
		liveOrder = append(liveOrder, key)
	}
	order := m.flow.order
	position := slices.Index(order, from)
	if position < 0 {
		// Opened some other way (a notification): use the list as it is.
		order = liveOrder
		position = slices.Index(order, from)
	}
	if position < 0 {
		return maildir.Message{}, false, false
	}
	for offset := 1; offset < len(order); offset++ {
		at := position + step*offset
		if at < 0 || at >= len(order) {
			if !unreadOnly {
				break
			}
			wrapped = true
			at = (at%len(order) + len(order)) % len(order)
		}
		index, visible := live[order[at]]
		if !visible || isDraft(m.messages[index]) || (unreadOnly && !m.messages[index].Unread) {
			continue
		}
		return m.messages[index], wrapped, true
	}
	return maildir.Message{}, false, false
}

// showMessage opens a message in the reader, as enter does in the list. The
// cursor goes to it first, so leaving the reader behaves as if it had been
// opened from there. A note replaces "Reading message" and stays once the
// body has loaded.
func (m *Model) showMessage(selected maildir.Message, note string) tea.Cmd {
	m.setMailCursorToPath(selected.Path)
	m.screen = screenMail
	m.content = nil
	m.contentPath = selected.Path
	m.readerMessage = selected
	m.readerGone = false
	m.loadingBody = true
	m.detailScroll = 0
	m.pendingArchive = ""
	m.flow.picker = nil
	m.flow.note = note
	m.status = note
	if note == "" {
		m.status = "Reading message"
	}
	return loadBodyCmd(m.mailStore, selected.Path)
}

// leaveOrAdvance is where a message acted on from the reader ends up: the next
// message when there is one and auto-advance is on, else the list. It does
// nothing when the reader has since moved on to another message.
func (m *Model) leaveOrAdvance(movedPath, note string) tea.Cmd {
	if m.screen == screenMail && m.contentPath != "" && maildir.Key(m.contentPath) != maildir.Key(movedPath) {
		return nil
	}
	if cmd, ok := m.advanceReader(maildir.Key(movedPath), note); ok {
		return cmd
	}
	m.leaveReader()
	return nil
}

// advanceReader opens the message after the one with key from.
func (m *Model) advanceReader(from, note string) (tea.Cmd, bool) {
	if !autoAdvance() || m.screen != screenMail || maildir.Key(m.contentPath) != from {
		return nil, false
	}
	next, _, ok := m.readerNeighbour(from, 1, false)
	if !ok {
		return nil, false
	}
	return m.showMessage(next, note), true
}

// handleReaderKey takes the keys of this file in the reader; handled is false
// for everything else.
func (m Model) handleReaderKey(key string) (Model, tea.Cmd, bool) {
	if m.screen != screenMail {
		return m, nil, false
	}
	if m.flow.picker != nil {
		model, command := m.handlePickerKey(key)
		return model, command, true
	}
	switch key {
	case "n":
		model, command := m.openNeighbour(1, false)
		return model, command, true
	case "p":
		model, command := m.openNeighbour(-1, false)
		return model, command, true
	case "N":
		model, command := m.openNeighbour(1, true)
		return model, command, true
	case "O":
		model, command := m.listAttachments()
		return model, command, true
	case "l":
		model, command := m.listLinks()
		return model, command, true
	case "u":
		// With auto-advance the reader no longer ends on the list, where u lives.
		// Only a fresh move: a stray u should not bring back last hour's archive.
		if m.lastMove == nil || m.now().Sub(m.lastMove.at) > 2*time.Minute {
			return m, nil, true
		}
		updated, command := m.undoMove()
		return updated.(Model), command, true
	}
	return m, nil, false
}

func (m Model) openNeighbour(step int, unreadOnly bool) (Model, tea.Cmd) {
	next, wrapped, ok := m.readerNeighbour(maildir.Key(m.contentPath), step, unreadOnly)
	if !ok {
		switch {
		case unreadOnly:
			m.status = "No more unread"
		case step > 0:
			m.status = "That is the last message in the list"
		default:
			m.status = "That is the first message in the list"
		}
		return m, nil
	}
	note := ""
	if wrapped {
		note = "Back at the top of the list"
	}
	return m, m.showMessage(next, note)
}

// handleListPaging is pgup, pgdown, ctrl+u, ctrl+d and n in the mail list.
func (m Model) handleListPaging(key string) (tea.Model, tea.Cmd) {
	if m.focus != paneMail {
		return m, nil
	}
	_, _, _, height := m.layout()
	page := max(height/2-1, 1) // two lines a message, one for the section header
	switch key {
	case "pgup":
		m.moveCursor(-page)
	case "pgdown":
		m.moveCursor(page)
	case "ctrl+u":
		m.moveCursor(-max(page/2, 1))
	case "ctrl+d":
		m.moveCursor(max(page/2, 1))
	case "n":
		rows := m.mailRows()
		for offset := 1; offset < len(rows); offset++ {
			at := (m.mailCursor + offset) % len(rows)
			if rows[at].unread {
				m.setCursor(at)
				return m, nil
			}
		}
		m.status = "No more unread"
	}
	return m, nil
}

// readerFooterBindings is the reader's key line; the attachment and link keys
// show only when the message has something to open.
func (m Model) readerFooterBindings() []helpBinding {
	if m.flow.picker != nil {
		return []helpBinding{{"1-9", "open"}, {"↑↓ enter", "choose"}, {"esc", "close"}}
	}
	bindings := []helpBinding{{"↑↓", "scroll"}, {"esc/q", "back"}, {"n p", "next/prev"}, {"N", "unread"}, {"R", "reply"}, {"A", "all"}, {"F", "forward"}, {"z", "quoted"}, {"i", "everyone"}, {"T", "thread"}, {"f", "file"}, {"d", "delete"}, {"a a", "archive"}}
	if m.content != nil && len(m.content.Attachments) > 0 {
		bindings = append(bindings, helpBinding{"O", "open file"})
	}
	return append(bindings, helpBinding{"l", "links"}, helpBinding{"?", "keys"}, helpBinding{"ctrl+c", "quit"})
}
