package tui

// Quitting without losing work: q and ctrl+c flush a calendar nudge still
// waiting for its debounce, then wait a few seconds for calendar writes and
// a mail being sent before the program exits. A second q quits at once.

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
)

// quitWait is the longest a quit waits for writes in flight; tests shorten it.
var quitWait = 5 * time.Second

type quitWaitMsg struct{ seq int }

// unfinishedWork counts what quitting now would drop: calendar writes and
// free/busy lookups in flight, and a mail counting down or sending.
func (m Model) unfinishedWork() int {
	count := m.calWriting + m.calLooking
	if m.sending || m.sendCountdown > 0 {
		count++
	}
	return count
}

// handleCtrlC never costs text: the mail composer is saved as a draft (esc's
// path) and then quits; the event form and calendar prompts only close, with
// a second ctrl+c to quit.
func (m Model) handleCtrlC() (tea.Model, tea.Cmd) {
	switch {
	case m.form != nil:
		model, _ := m.closeComposer()
		next := model.(Model)
		if next.form != nil {
			return next, nil // the draft could not be saved: stay open
		}
		return next.requestQuit()
	case m.eventForm != nil:
		m.eventForm = nil
		m.status = "ctrl+c again quits"
		return m, nil
	case m.calPrompt != nil:
		m.calPrompt = nil
		m.status = "ctrl+c again quits"
		return m, nil
	}
	return m.requestQuit()
}

// requestQuit applies a pending nudge, then quits at once or, with writes in
// flight, waits for them for up to quitWait.
func (m Model) requestQuit() (tea.Model, tea.Cmd) {
	var flush tea.Cmd
	if m.nudging != nil {
		var model tea.Model
		model, flush = m.flushNudge()
		m = model.(Model)
	}
	work := m.unfinishedWork()
	if work == 0 {
		return m, tea.Quit // a flushed nudge always counts as work, so flush is nil here
	}
	m.quitting = true
	m.quitSeq++
	seq := m.quitSeq
	noun := "changes"
	if work == 1 {
		noun = "change"
	}
	m.status = fmt.Sprintf("Saving %d %s… q again to quit now", work, noun)
	return m, tea.Batch(flush, tea.Tick(quitWait, func(time.Time) tea.Msg { return quitWaitMsg{seq: seq} }))
}

func (m Model) handleQuitWait(message quitWaitMsg) (tea.Model, tea.Cmd) {
	if m.quitting && message.seq == m.quitSeq {
		return m, tea.Quit
	}
	return m, nil
}

// quitIfDone ends a quit that was waiting once the last write has landed.
func (m Model) quitIfDone() tea.Cmd {
	if m.quitting && m.unfinishedWork() == 0 {
		return tea.Quit
	}
	return nil
}
