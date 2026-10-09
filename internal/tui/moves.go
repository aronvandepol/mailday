package tui

// Undo for every move: archive, file (f), delete and the reply judge's
// auto-archive each remember where the message went, and u moves it back.

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/aronvandepol/mailday/internal/maildir"
	"github.com/aronvandepol/mailday/internal/terminal"
)

// lastMove is the last message move, for u. path is where the file went; the
// daemon's server move replaces that file, so messageID, account and toBox let
// undo find the message again.
type lastMove struct {
	path      string
	messageID string
	account   string
	fromBox   string
	toBox     string
	at        time.Time
	// more are the other messages of a conversation moved with this one
	// (threads_actions.go); u restores them all.
	more []lastMove
}

func newLastMove(message maildir.Message, newPath, toBox string, at time.Time) lastMove {
	return lastMove{
		path: newPath, messageID: message.MessageID, account: message.Account,
		fromBox: messageBox(message), toBox: toBox, at: at,
	}
}

// recordMove remembers a move of message (as it was listed) to newPath in toBox.
func (m *Model) recordMove(message maildir.Message, newPath, toBox string) {
	move := newLastMove(message, newPath, toBox, m.now())
	m.lastMove = &move
}

// messageFinder is the part of maildir.Store that finds a message by its
// Message-ID after its file was replaced.
type messageFinder interface {
	FindByMessageID(account, box, messageID string) (maildir.Message, bool)
}

// storeAs reaches an optional method of the maildir store behind the
// daemon-pushing wrapper.
func storeAs[T any](store MailStore) (T, bool) {
	if found, ok := store.(T); ok {
		return found, true
	}
	if pushing, ok := store.(pushingStore); ok {
		found, ok := pushing.MailStore.(T)
		return found, ok
	}
	var none T
	return none, false
}

type messageArchivedMsg struct {
	message maildir.Message // as listed, before the move
	newPath string
	err     error
}

type messageRestoredMsg struct {
	box   string
	count int // messages restored; 0 or 1 is one
	// failed counts messages of a conversation that could not go back.
	failed int
	err    error
}

func (m Model) handleArchived(message messageArchivedMsg) (tea.Model, tea.Cmd) {
	if message.err != nil {
		m.status = terminal.SanitizeLine(message.err.Error())
		return m, nil
	}
	for index := range m.messages {
		if m.messages[index].Path == message.message.Path {
			m.messages = append(m.messages[:index], m.messages[index+1:]...)
			break
		}
	}
	m.recordMove(message.message, message.newPath, maildir.ArchiveBox)
	advance := m.leaveOrAdvance(message.message.Path, "Archived · u undoes")
	m.pendingArchive = ""
	m.status = "Archived · u undoes"
	m.boundCursors()
	return m, advance
}

// leaveReader goes back to the list after the open message was moved away.
func (m *Model) leaveReader() {
	m.screen = screenHome
	m.content = nil
	m.contentPath = ""
}

// undoMove puts the last moved message back where it came from.
func (m Model) undoMove() (tea.Model, tea.Cmd) {
	if m.lastMove == nil {
		m.status = "Nothing to undo"
		return m, nil
	}
	ref, store := *m.lastMove, m.mailStore
	if ref.fromBox == draftsBox {
		return m.undoDraftDelete(ref)
	}
	if ref.toBox == snoozedBox {
		return m.undoSnooze(ref)
	}
	m.lastMove = nil
	m.status = "Undoing"
	return m, func() tea.Msg {
		moves := append([]lastMove{ref}, ref.more...)
		restored := 0
		var firstErr error
		for _, move := range moves {
			if err := restoreMove(store, move); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			restored++
		}
		if restored == 0 {
			return messageRestoredMsg{err: firstErr}
		}
		return messageRestoredMsg{box: ref.fromBox, count: restored, failed: len(moves) - restored}
	}
}

// restoreMove puts one moved message back.
func restoreMove(store MailStore, ref lastMove) error {
	_, err := store.MoveTo(maildir.Message{Path: ref.path}, ref.fromBox)
	if err == nil {
		return nil
	}
	// The file is gone: the daemon moved the message on the server and
	// mbsync downloaded it under a new name. Find it by Message-ID.
	if finder, ok := storeAs[messageFinder](store); ok && ref.messageID != "" {
		if found, ok := finder.FindByMessageID(ref.account, ref.toBox, ref.messageID); ok {
			_, retryErr := store.MoveTo(found, ref.fromBox)
			if retryErr == nil {
				return nil
			}
			err = retryErr
		}
	}
	return err
}

func (m Model) handleRestored(message messageRestoredMsg) (tea.Model, tea.Cmd) {
	if message.err != nil {
		m.status = "Undo failed: " + terminal.SanitizeLine(message.err.Error())
		return m, nil
	}
	m.pendingStatus = "Restored to " + boxLabel(message.box)
	if message.count > 1 || message.failed > 0 {
		m.pendingStatus = "Restored " + mailCountLabel(message.count) + " to " + boxLabel(message.box)
		if message.failed > 0 {
			m.pendingStatus += fmt.Sprintf(" · %d could not be moved back", message.failed)
		}
	}
	m.status = "Restoring"
	m.loadingMail = true
	return m, m.loadMailCmd()
}
