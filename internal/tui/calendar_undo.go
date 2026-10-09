package tui

// Calendar undo: u in the calendar pane takes back the last calendar change
// made in this session, for ten minutes. Only one step is kept; an undo is
// itself not undoable.

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/aronvandepol/mailday/internal/calendar"
)

// calendarUndoWindow keeps a stray u from reversing something done long ago.
const calendarUndoWindow = 10 * time.Minute

type calendarUndo struct {
	change  pendingChange // as it was sent, so before and after are known
	expires time.Time
}

// recordUndo remembers a change the user made, or forgets the last one when
// the change is itself an undo.
func (m *Model) recordUndo(change pendingChange) {
	if change.undoNote != "" {
		m.calUndo = nil
		return
	}
	m.calUndo = &calendarUndo{change: change, expires: m.now().Add(calendarUndoWindow)}
}

// forgetUndoOf drops the undo for a change the server refused: nothing
// happened there, so there is nothing to take back.
func (m *Model) forgetUndoOf(change pendingChange) {
	if m.calUndo != nil && m.calUndo.change.op == change.op && m.calUndo.change.after.ID == change.after.ID {
		m.calUndo = nil
	}
}

// learnRemoteID gives a created event's undo the server id it needs to delete
// it; the id only arrives with the reply.
func (m *Model) learnRemoteID(change pendingChange, id string) {
	if m.calUndo == nil || m.calUndo.change.op != "create" || m.calUndo.change.after.ID != change.after.ID {
		return
	}
	undo := *m.calUndo // a copy, as older copies of the model may share the pointer
	undo.change.after.RemoteID = id
	m.calUndo = &undo
}

// findOnScreen is the event as the calendar shows it now, found by its
// server id: a sync may have replaced the copy the change was made on.
// shown is false when another week is on screen.
func (m Model) findOnScreen(event calendar.Event) (found calendar.Event, shown bool) {
	if event.RemoteID != "" {
		if index := m.findEvent(func(e calendar.Event) bool { return e.RemoteID == event.RemoteID && e.Source == event.Source }); index >= 0 {
			return m.events[index], true
		}
	}
	return event, false
}

// onScreen is findOnScreen, or the given copy when the event is not shown.
func (m Model) onScreen(event calendar.Event) calendar.Event {
	found, _ := m.findOnScreen(event)
	return found
}

func (m Model) undoCalendar() (tea.Model, tea.Cmd) {
	if m.nudging != nil {
		// < > + - presses that were not sent yet: put the event back.
		change := *m.nudging
		m.nudging = nil
		m.replaceEvent(change.after.ID, change.before)
		m.status = "Undone: move of " + change.before.Summary
		return m, nil
	}
	undo := m.calUndo
	switch {
	case undo == nil:
		m.status = "Nothing to undo in the calendar"
		return m, nil
	case m.now().After(undo.expires):
		m.calUndo = nil
		m.status = "Too late to undo: only the last 10 minutes"
		return m, nil
	case m.calWriting > 0:
		m.status = "Still saving the last change · u again in a moment"
		return m, nil
	}
	change := undo.change
	switch change.op {
	case "create":
		return m.undoCreate(change)
	case "update":
		return m.undoUpdate(change)
	case "delete":
		return m.undoDelete(change)
	case "respond":
		return m.undoRespond(change)
	}
	m.status = "Cannot undo a move to another calendar · move it back with e"
	return m, nil
}

// undoneChange sends the reverse of a change. The note is shown at once and
// again when the server has it.
func (m Model) undoneChange(reverse pendingChange, note string) (tea.Model, tea.Cmd) {
	reverse.undoNote = note
	model, command := m.applyChange(reverse)
	next := model.(Model)
	next.status = note
	return next, command
}

func (m Model) undoCreate(change pendingChange) (tea.Model, tea.Cmd) {
	if change.after.RemoteID == "" {
		m.status = "Cannot undo yet: the new event has no server id"
		return m, nil
	}
	event := m.onScreen(change.after)
	return m.undoneChange(pendingChange{op: "delete", before: event, series: change.rrule != ""}, "Undone: removed "+event.Summary)
}

// undoUpdate puts back the fields the change touched, from its before copy;
// the others keep what the calendar shows now (a sync may have brought news
// the change never meant to undo). Notes are only touched when the change
// sent them. Invitations that went out stay sent.
//
// The event need not be on screen: it is then known by what the server was
// told, its RemoteID and calendar, never by a copy in the list.
func (m Model) undoUpdate(change pendingChange) (tea.Model, tea.Cmd) {
	was, sent := change.before, change.after
	current, shown := m.findOnScreen(sent)
	if !shown {
		current = sent
		if was.RemoteID != "" {
			current.RemoteID = was.RemoteID
		}
		if was.Source != "" {
			current.Source = was.Source
		}
		if was.UID != "" {
			current.UID = was.UID
		}
	}
	if current.RemoteID == "" {
		m.status = "Cannot undo yet: the event has no server id"
		return m, nil
	}
	titleTouched := was.Summary != sent.Summary
	placeTouched := was.Location != sent.Location
	timeTouched := !was.Start.Equal(sent.Start) || !was.End.Equal(sent.End) || was.AllDay != sent.AllDay
	if !titleTouched && !placeTouched && !timeTouched && !change.notes {
		m.status = "Cannot undo: that change only added guests, and they stay invited"
		return m, nil
	}
	back := current
	if titleTouched {
		back.Summary = was.Summary
	}
	if placeTouched {
		back.Location = was.Location
	}
	if timeTouched {
		back.Start, back.End, back.AllDay = was.Start, was.End, was.AllDay
	}
	if change.notes {
		back.Description = was.Description
	}
	if back.Summary == current.Summary && back.Location == current.Location && back.Start.Equal(current.Start) && back.End.Equal(current.End) && !change.notes {
		m.calUndo = nil
		m.status = "Nothing to undo: " + current.Summary + " is already as it was"
		return m, nil
	}
	note := "Undone: " + back.Summary + " is back to " + describeSpan(back.Start, back.End, back.AllDay)
	if len(change.attendees) > 0 {
		note += " · invitations already sent stay sent"
	}
	return m.undoneChange(pendingChange{op: "update", before: current, after: back, notes: change.notes}, note)
}

// undoDelete creates the event again: the server cannot bring a deleted one
// back, so it is new, without its guests and, for a series, a single event.
func (m Model) undoDelete(change pendingChange) (tea.Model, tea.Cmd) {
	was := change.before
	restored := was
	m.pendingSeq++
	restored.ID = fmt.Sprintf("pending-%d", m.pendingSeq)
	restored.RemoteID, restored.UID, restored.Attendees = "", "", ""
	restored.Series, restored.Rule, restored.RuleText = false, "", ""
	restored.Description = editableNotes(was.Description)
	restored.Pending = true
	note := "Undone: " + was.Summary + " restored as a new event; guests are not re-invited"
	if was.Series {
		note += "; the repeat is not restored"
	}
	if notesTooLong(was.Description) {
		note += "; long notes are shortened"
	}
	return m.undoneChange(pendingChange{op: "create", after: restored}, note)
}

// undoRespond answers again as before. Mailday cannot take an answer back
// to "not answered", so that case is refused.
func (m Model) undoRespond(change pendingChange) (tea.Model, tea.Cmd) {
	answer := map[string]string{"accepted": "accept", "tentative": "tentative", "declined": "decline"}[change.before.RSVP]
	if answer == "" {
		m.status = "Cannot undo: you had not answered " + change.before.Summary + " before"
		return m, nil
	}
	current := m.onScreen(change.after)
	back := current
	back.RSVP = change.before.RSVP
	return m.undoneChange(pendingChange{op: "respond", before: current, after: back, answer: answer}, "Undone: answer to "+back.Summary+" is back to "+back.RSVP)
}
