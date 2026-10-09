package tui

// Keys on a conversation row: space opens or closes it, and a a, d, f and m
// on a collapsed row act on every message of it in this box.

import (
	"fmt"
	"maps"
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/aronvandepol/mailday/internal/maildir"
	"github.com/aronvandepol/mailday/internal/terminal"
)

const trashBox = "Trash"

// handleThreadKey takes space and the bulk forms of a, d and m on the list;
// handled is false for everything else, which then acts on the one message
// under the cursor as before. f opens the box picker as usual, after
// remembering the thread for fileThread.
func (m Model) handleThreadKey(key string) (Model, tea.Cmd, bool) {
	if !threadsEnabled() || m.screen != screenHome || m.focus != paneMail {
		return m, nil, false
	}
	switch key {
	case " ", "space":
		return m.toggleThread(), nil, true
	case "f":
		model, command := m.startFiling()
		next := model.(Model)
		next.threads.filingGroup = m.collapsedGroup()
		return next, command, true
	case "a":
		group := m.collapsedGroup()
		if group == nil {
			return m, nil, false
		}
		if m.pendingArchive != group[0].Path {
			m.pendingArchive = group[0].Path
			m.status = fmt.Sprintf("Press a again to archive these %d messages", len(group))
			return m, nil, true
		}
		m.status = fmt.Sprintf("Archiving %d messages", len(group))
		return m, moveGroupCmd(m.mailStore, group, maildir.ArchiveBox), true
	case "d":
		group := m.collapsedGroup()
		if group == nil {
			return m, nil, false
		}
		// Several messages, some never seen on screen: ask, as a a does.
		if m.pendingArchive != "delete:"+group[0].Path {
			m.pendingArchive = "delete:" + group[0].Path
			m.status = fmt.Sprintf("Press d again to move these %d messages to Trash", len(group))
			return m, nil, true
		}
		m.pendingArchive = ""
		m.status = fmt.Sprintf("Moving %d messages to Trash", len(group))
		return m, moveGroupCmd(m.mailStore, group, trashBox), true
	case "m":
		group := m.collapsedGroup()
		if group == nil {
			return m, nil, false
		}
		// Any unread message makes this "mark read"; a read thread goes back
		// to unread, as m does for one message.
		unread := !slices.ContainsFunc(group, func(message maildir.Message) bool { return message.Unread })
		m.status = "Updating messages"
		return m, markGroupCmd(m.mailStore, group, unread), true
	}
	return m, nil, false
}

// toggleThread opens or closes the conversation under the cursor; on one of
// its older messages it closes it and returns the cursor to the top row.
func (m Model) toggleThread() Model {
	rows := m.mailRows()
	if len(rows) == 0 {
		return m
	}
	cursor := bound(m.mailCursor, len(rows))
	for rows[cursor].child && cursor > 0 {
		cursor--
	}
	row := rows[cursor]
	if len(row.members) < 2 {
		m.status = "One message in this conversation"
		return m
	}
	// Model is copied on every update, so the map is replaced, not edited.
	expanded := maps.Clone(m.threads.expanded)
	if expanded == nil {
		expanded = make(map[string]bool)
	}
	if expanded[row.key] {
		delete(expanded, row.key)
	} else {
		expanded[row.key] = true
	}
	m.threads.expanded = expanded
	m.mailCursor = cursor
	m.status = ""
	return m
}

// movedMessage is one message of a bulk move, as listed and where it went.
type movedMessage struct {
	message maildir.Message
	newPath string
}

type threadMovedMsg struct {
	moved  []movedMessage
	failed int
	err    error // the first failure
	box    string
}

// moveGroupCmd moves every message of a conversation, one after another; one
// that cannot move does not stop the rest.
func moveGroupCmd(store MailStore, group []maildir.Message, box string) tea.Cmd {
	group = slices.Clone(group)
	return func() tea.Msg {
		result := threadMovedMsg{box: box}
		for _, message := range group {
			newPath, err := store.MoveTo(message, box)
			if err != nil {
				result.failed++
				if result.err == nil {
					result.err = err
				}
				continue
			}
			result.moved = append(result.moved, movedMessage{message: message, newPath: newPath})
		}
		return result
	}
}

// movedNote is the status after a bulk move.
func movedNote(box string, moved, total int) string {
	count := mailCountLabel(moved)
	if moved < total {
		count = fmt.Sprintf("%d of %s", moved, mailCountLabel(total))
	}
	switch box {
	case maildir.ArchiveBox:
		return "Archived " + count + " · u undoes"
	case trashBox:
		return "Moved " + count + " to Trash · u undoes"
	}
	return "Filed " + count + " to " + boxLabel(box) + " · u undoes"
}

func (m Model) handleThreadMoved(message threadMovedMsg) (Model, tea.Cmd) {
	m.filing = false
	m.threads.filingGroup = nil
	m.pendingArchive = ""
	if len(message.moved) == 0 {
		if message.err != nil {
			m.status = terminal.SanitizeLine(message.err.Error())
		}
		return m, nil
	}
	for _, moved := range message.moved {
		index := slices.IndexFunc(m.messages, func(listed maildir.Message) bool { return listed.Path == moved.message.Path })
		if index < 0 {
			continue
		}
		if message.box == maildir.ArchiveBox || message.box == trashBox {
			m.messages = slices.Delete(m.messages, index, index+1)
		} else {
			m.messages[index].Path, m.messages[index].Box = moved.newPath, message.box
		}
	}
	m.recordMove(message.moved[0].message, message.moved[0].newPath, message.box)
	for _, moved := range message.moved[1:] {
		m.lastMove.more = append(m.lastMove.more, newLastMove(moved.message, moved.newPath, message.box, m.lastMove.at))
	}
	total := len(message.moved) + message.failed
	m.status = movedNote(message.box, len(message.moved), total)
	if message.failed > 0 {
		m.status += " · " + terminal.SanitizeLine(message.err.Error())
	}
	m.boundCursors()
	return m, nil
}

// filingGroupNote is "5 messages" between before and after while the picker
// is open on a collapsed conversation, so the footer says how many move; ""
// for a single message.
func (m Model) filingGroupNote(before, after string) string {
	if len(m.threads.filingGroup) < 2 {
		return ""
	}
	return before + mailCountLabel(len(m.threads.filingGroup)) + after
}

// fileThread files the whole conversation f was pressed on, from the box
// picker's choice; ok is false for a single message.
func (m *Model) fileThread(box string) (tea.Cmd, bool) {
	group := m.threads.filingGroup
	selected, found := m.selectedMail()
	if len(group) < 2 || !found || group[0].Path != selected.Path {
		return nil, false
	}
	m.filing = false
	m.threads.filingGroup = nil
	m.status = fmt.Sprintf("Filing %d messages to %s", len(group), boxLabel(box))
	return moveGroupCmd(m.mailStore, group, box), true
}

type threadMarkedMsg struct {
	oldPaths []string
	updated  []maildir.Message
	unread   bool
	err      error
}

func markGroupCmd(store MailStore, group []maildir.Message, unread bool) tea.Cmd {
	group = slices.Clone(group)
	return func() tea.Msg {
		result := threadMarkedMsg{unread: unread}
		for _, message := range group {
			updated, err := store.SetUnread(message, unread)
			if err != nil {
				if result.err == nil {
					result.err = err
				}
				continue
			}
			result.oldPaths = append(result.oldPaths, message.Path)
			result.updated = append(result.updated, updated)
		}
		return result
	}
}

func (m Model) handleThreadMarked(message threadMarkedMsg) (Model, tea.Cmd) {
	for position, oldPath := range message.oldPaths {
		if index := m.findMessage(oldPath); index >= 0 {
			m.messages[index] = message.updated[position]
		}
	}
	if len(message.updated) > 0 {
		state := "read"
		if message.unread {
			state = "unread"
		}
		// The conversation moves between New for You and Previously Seen:
		// the cursor stays on it, not on the row that slides into its place.
		m.setMailCursorToPath(message.updated[0].Path)
		m.status = "Marked " + mailCountLabel(len(message.updated)) + " " + state
	}
	if message.err != nil {
		m.status = terminal.SanitizeLine(message.err.Error())
	}
	m.pendingArchive = ""
	m.boundCursors()
	return m, nil
}

// handleThreadMsg takes the messages of this feature; it is update()'s one
// hook for them.
func (m Model) handleThreadMsg(message tea.Msg) (Model, tea.Cmd, bool) {
	switch message := message.(type) {
	case threadMovedMsg:
		model, command := m.handleThreadMoved(message)
		return model, command, true
	case threadMarkedMsg:
		model, command := m.handleThreadMarked(message)
		return model, command, true
	case snoozeNoteMsg: // snooze.go
		if message.note != "" {
			m.status = message.note
		}
		return m, nil, true
	case groupSnoozedMsg: // snooze.go
		return m.handleGroupSnoozed(message), nil, true
	case conversationLoadedMsg:
		return m.handleConversationLoaded(message), nil, true
	}
	return m, nil, false
}
