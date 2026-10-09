package tui

// Z snoozes the selected message: it asks "when?", files the message in the
// account's @Snoozed box and notes the due time in the list Syncthing shares
// (syncd.SnoozePath). mailday-syncd moves it back to INBOX, unread, when the
// time comes.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/aronvandepol/mailday/internal/maildir"
	"github.com/aronvandepol/mailday/internal/syncd"
	"github.com/aronvandepol/mailday/internal/terminal"
	"github.com/aronvandepol/mailday/internal/when"
)

const snoozedBox = syncd.SnoozedBox

// snoozeDefaultClock is the time of day for "fri" or "tomorrow" alone.
const snoozeDefaultClock = 8 * time.Hour

// snoozePrompt is the open "when?" prompt and the message it will snooze.
type snoozePrompt struct {
	message maildir.Message
	// group is every message of a collapsed conversation that can be
	// snoozed (it has a Message-ID); nil for a single message. skipped
	// counts the ones left out.
	group   []maildir.Message
	skipped int
	input   string
}

func (m Model) startSnoozePrompt() (tea.Model, tea.Cmd) {
	if m.focus != paneMail && m.screen != screenMail {
		return m, nil
	}
	selected, ok := m.selectedMail()
	if !ok || m.refuseDraft(selected) {
		return m, nil
	}
	if messageBox(selected) == maildir.SentBox {
		m.status = "Sent mail stays in Sent"
		return m, nil
	}
	prompt := &snoozePrompt{message: selected}
	// On a collapsed conversation row Z snoozes all of it, as a, d and m do;
	// an opened row or the reader keeps to one message.
	if all := m.snoozeGroup(); all != nil {
		for _, message := range all {
			if message.MessageID != "" {
				prompt.group = append(prompt.group, message)
			}
		}
		prompt.skipped = len(all) - len(prompt.group)
		if len(prompt.group) == 0 {
			m.status = "No Message-ID: these messages cannot be snoozed"
			return m, nil
		}
		prompt.message = prompt.group[0]
	} else if selected.MessageID == "" {
		m.status = "No Message-ID: this message cannot be snoozed"
		return m, nil
	}
	m.snoozing = prompt
	m.pendingArchive = ""
	m.status = ""
	return m, nil
}

// snoozeGroup is the conversation Z applies to: the collapsed row under the
// list cursor, never the reader, which shows one message.
func (m Model) snoozeGroup() []maildir.Message {
	if !threadsEnabled() || m.screen != screenHome || m.focus != paneMail {
		return nil
	}
	return m.collapsedGroup()
}

// parseSnoozeDue reads a when phrase as the moment the message returns:
// 08:00 for a day alone, a bare hour after a day ("tomorrow 9") as that hour,
// a time alone as today (tomorrow when it has passed), and "in 2 hours" from now.
func parseSnoozeDue(input string, now time.Time) (time.Time, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return time.Time{}, errors.New("when? tomorrow 9 · fri · in 3 days · tonight")
	}
	parsed, err := when.Parse(input, now)
	if err != nil {
		return time.Time{}, err
	}
	if !parsed.HasDate && !parsed.HasTime && parsed.Length > 0 && strings.HasPrefix(strings.ToLower(input), "in ") {
		return now.Add(parsed.Length).Truncate(time.Minute), nil
	}
	if !parsed.HasDate && !parsed.HasTime {
		return time.Time{}, errors.New("no day or time in that: try tomorrow 9 · fri · in 3 days")
	}
	// when reads a lone 7 to 23 as part of the title, so "tomorrow 9" arrives as a date and the word "9".
	clock, hasClock := parsed.Clock, parsed.HasTime
	if !hasClock && parsed.HasDate {
		if hour, err := strconv.Atoi(strings.TrimSpace(parsed.Title)); err == nil && hour >= 0 && hour < 24 {
			clock, hasClock = time.Duration(hour)*time.Hour, true
		}
	}
	if !hasClock {
		clock = snoozeDefaultClock
	}
	day := parsed.Date
	if !parsed.HasDate {
		day = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	}
	due := time.Date(day.Year(), day.Month(), day.Day(), int(clock/time.Hour), int(clock%time.Hour/time.Minute), 0, 0, time.Local)
	if !parsed.HasDate && !due.After(now) {
		due = due.AddDate(0, 0, 1)
	}
	if !due.After(now) {
		return time.Time{}, errors.New(due.Format("15:04 Mon 2 Jan") + " has passed")
	}
	return due, nil
}

// snoozeDay is a due time for people: "Fri 08:00", just the time for today,
// and the date once it is more than a week away or in another year.
func snoozeDay(due, now time.Time) string {
	due, now = due.In(time.Local), now.In(time.Local)
	today := dayOf(now)
	switch {
	case sameDay(due, now):
		return due.Format("15:04")
	case due.Before(today.AddDate(0, 0, 7)):
		return due.Format("Mon 15:04")
	case due.Year() == now.Year():
		return due.Format("Mon 2 Jan 15:04")
	}
	return due.Format("Mon 2 Jan 2006 15:04")
}

func (m Model) handleSnoozeKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	prompt := m.snoozing
	switch message.String() {
	case "esc":
		m.snoozing = nil
		m.status = ""
		return m, nil
	case "enter":
		due, err := parseSnoozeDue(prompt.input, m.now())
		if err != nil {
			m.status = err.Error()
			return m, nil
		}
		m.snoozing = nil
		m.status = "Snoozing"
		if prompt.group != nil {
			return m, snoozeGroupCmd(m.mailStore, prompt.group, prompt.skipped, due, m.now())
		}
		return m, snoozeCmd(m.mailStore, prompt.message, due, m.now())
	case "backspace":
		prompt.input = trimLastRune(prompt.input)
	case "ctrl+u":
		prompt.input = ""
	case "ctrl+w":
		prompt.input = strings.TrimRight(prompt.input, " ")
		if index := strings.LastIndex(prompt.input, " "); index >= 0 {
			prompt.input = prompt.input[:index+1]
		} else {
			prompt.input = ""
		}
	default:
		if text := message.Key().Text; text != "" && !message.Key().Mod.Contains(tea.ModCtrl) {
			prompt.input += text
		}
	}
	return m, nil
}

// renderSnoozePrompt is the footer while Z waits: what was typed and when
// the message would come back.
func (m Model) renderSnoozePrompt(width int) string {
	accent := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
	input := accent.Render("› ") + m.snoozing.input + lipgloss.NewStyle().Foreground(colorPrimary).Render("█")
	preview, style := "", lipgloss.NewStyle().Foreground(colorActive)
	if due, err := parseSnoozeDue(m.snoozing.input, m.now()); err != nil {
		preview, style = err.Error(), styleMuted
	} else {
		preview = "Back in your inbox " + due.Format("Mon 2 Jan 15:04")
		if due.Year() != m.now().Year() {
			preview = "Back in your inbox " + due.Format("Mon 2 Jan 2006 15:04")
		}
	}
	title := "Snooze · " + terminal.SanitizeLine(m.snoozing.message.Subject)
	if m.snoozing.group != nil {
		title += " (" + mailCountLabel(len(m.snoozing.group)) + ")"
	}
	return renderRule(width, title) + "\n" +
		truncateToWidth(input, width) + "\n" +
		truncateToWidth("  "+style.Render(preview), width)
}

type messageSnoozedMsg struct {
	message  maildir.Message // as listed, before the move
	newPath  string
	due      time.Time
	resnooze bool // already in @Snoozed: only the due time changed
	err      error
}

// snoozeCmd records the entry first and moves second: the daemon only acts
// on a message it finds in @Snoozed, so an entry without one is harmless,
// while a moved message without an entry would never return.
func snoozeCmd(store MailStore, message maildir.Message, due, now time.Time) tea.Cmd {
	return func() tea.Msg {
		path := syncd.SnoozePath()
		entry := syncd.SnoozeEntry{
			MessageID: message.MessageID, Account: message.Account, Due: due,
			FromBox: messageBox(message), Subject: message.Subject, Added: now,
			Host: syncd.SnoozeHost(),
		}
		if messageBox(message) == snoozedBox {
			// Snoozed again: keep the box it first came from.
			if entries, err := syncd.ReadSnooze(path); err == nil {
				for _, earlier := range entries {
					if earlier.Key() == entry.Key() {
						entry.FromBox = earlier.FromBox
					}
				}
			}
			err := syncd.AddSnooze(path, entry)
			return messageSnoozedMsg{message: message, due: due, resnooze: true, err: err}
		}
		if err := ensureSnoozedBox(store, message.Account); err != nil {
			return messageSnoozedMsg{message: message, err: err}
		}
		if err := syncd.AddSnooze(path, entry); err != nil {
			return messageSnoozedMsg{message: message, err: err}
		}
		newPath, err := store.MoveTo(message, snoozedBox)
		if err != nil {
			_ = syncd.RemoveSnooze(path, message.Account, message.MessageID)
			return messageSnoozedMsg{message: message, err: err}
		}
		return messageSnoozedMsg{message: message, newPath: newPath, due: due}
	}
}

// snoozeNoteMsg carries the note for a snooze list that was damaged and came
// back from its backup (syncd.TakeSnoozeNote), which is shown once.
type snoozeNoteMsg struct{ note string }

// checkSnoozeCmd reads the list at start, so a damaged one is reported then
// and not at the first Z.
func checkSnoozeCmd() tea.Cmd {
	return func() tea.Msg {
		_, _ = syncd.ReadSnooze(syncd.SnoozePath())
		return snoozeNoteMsg{note: syncd.TakeSnoozeNote()}
	}
}

// withSnoozeNote puts a pending recovery note in front of a status, for the
// case where a snooze was the first thing to read the damaged list.
func withSnoozeNote(status string) string {
	if note := syncd.TakeSnoozeNote(); note != "" {
		return note + " · " + status
	}
	return status
}

// groupSnoozedMsg is the answer to snoozing a conversation.
type groupSnoozedMsg struct {
	moved   []movedMessage
	failed  int
	skipped int // left out for want of a Message-ID
	err     error
	due     time.Time
}

// snoozeGroupCmd snoozes each message with its own entry, all due at once.
// Like snoozeCmd it writes the entry before moving, and takes the entry back
// when the move fails; one message that cannot go does not stop the rest.
func snoozeGroupCmd(store MailStore, group []maildir.Message, skipped int, due, now time.Time) tea.Cmd {
	group = slices.Clone(group)
	return func() tea.Msg {
		result := groupSnoozedMsg{due: due, skipped: skipped}
		fail := func(err error) {
			result.failed++
			if result.err == nil {
				result.err = err
			}
		}
		if err := ensureSnoozedBox(store, group[0].Account); err != nil {
			result.failed, result.err = len(group), err
			return result
		}
		path := syncd.SnoozePath()
		for _, message := range group {
			entry := syncd.SnoozeEntry{
				MessageID: message.MessageID, Account: message.Account, Due: due,
				FromBox: messageBox(message), Subject: message.Subject, Added: now,
				Host: syncd.SnoozeHost(),
			}
			if err := syncd.AddSnooze(path, entry); err != nil {
				fail(err)
				continue
			}
			newPath, err := store.MoveTo(message, snoozedBox)
			if err != nil {
				_ = syncd.RemoveSnooze(path, message.Account, message.MessageID)
				fail(err)
				continue
			}
			result.moved = append(result.moved, movedMessage{message: message, newPath: newPath})
		}
		return result
	}
}

// handleGroupSnoozed takes the snoozed messages off the list and remembers
// them as one move, so u brings them all back.
func (m Model) handleGroupSnoozed(message groupSnoozedMsg) Model {
	m.pendingArchive = ""
	if len(message.moved) == 0 {
		if message.err != nil {
			m.status = terminal.SanitizeLine(message.err.Error())
		}
		return m
	}
	for _, moved := range message.moved {
		if index := m.findMessage(moved.message.Path); index >= 0 {
			m.messages = slices.Delete(m.messages, index, index+1)
		}
	}
	m.recordMove(message.moved[0].message, message.moved[0].newPath, snoozedBox)
	for _, moved := range message.moved[1:] {
		m.lastMove.more = append(m.lastMove.more, newLastMove(moved.message, moved.newPath, snoozedBox, m.lastMove.at))
	}
	m.status = "Snoozed " + mailCountLabel(len(message.moved)) + " until " + snoozeDay(message.due, m.now())
	if message.failed > 0 {
		m.status += " · " + terminal.SanitizeLine(message.err.Error())
	}
	if message.skipped > 0 {
		m.status += fmt.Sprintf(" · %d without a Message-ID stayed", message.skipped)
	}
	m.status = withSnoozeNote(m.status + " · u undoes")
	m.boundCursors()
	return m
}

// ensureSnoozedBox creates the account's @Snoozed Maildir when it is missing:
// mbsync's "@*" tag channel then creates the server folder too. An account
// with no "@" folder at all has no such channel, and mail filed in a folder
// nothing syncs would be lost to the phone and the other machine.
func ensureSnoozedBox(store MailStore, account string) error {
	finder, ok := storeAs[interface {
		AccountDir(string) (string, error)
	}](store)
	if !ok {
		return nil
	}
	dir, err := finder.AccountDir(account)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, snoozedBox, "cur")); err == nil {
		return nil
	}
	tags, _ := filepath.Glob(filepath.Join(dir, "@*", "cur"))
	if len(tags) == 0 {
		return errors.New(account + " has no @ tag folders, so there is nowhere to snooze to")
	}
	for _, leaf := range []string{"cur", "new", "tmp"} {
		if err := os.MkdirAll(filepath.Join(dir, snoozedBox, leaf), 0o700); err != nil {
			return err
		}
	}
	return nil
}

func (m Model) handleSnoozed(message messageSnoozedMsg) (tea.Model, tea.Cmd) {
	if message.err != nil {
		m.status = terminal.SanitizeLine(message.err.Error())
		return m, nil
	}
	label := snoozeDay(message.due, m.now())
	if message.resnooze {
		m.refreshUpcoming() // the agenda lists the new due time (upcoming.go)
		m.status = withSnoozeNote("Snoozed until " + label)
		return m, nil
	}
	for index := range m.messages {
		if m.messages[index].Path == message.message.Path {
			m.messages = slices.Delete(m.messages, index, index+1)
			break
		}
	}
	m.recordMove(message.message, message.newPath, snoozedBox)
	note := "Snoozed until " + label + " · u undoes"
	advance := m.leaveOrAdvance(message.message.Path, note)
	m.status = withSnoozeNote(note)
	m.boundCursors()
	return m, advance
}

// undoSnooze moves the message (every message of a snoozed conversation)
// back to its box and drops its entry.
func (m Model) undoSnooze(ref lastMove) (tea.Model, tea.Cmd) {
	m.lastMove = nil
	m.status = "Undoing"
	store := m.mailStore
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
			// A leftover entry would do no harm (the daemon finds nothing in
			// @Snoozed and drops it), so a failure here is not worth refusing the undo.
			_ = syncd.RemoveSnooze(syncd.SnoozePath(), move.account, move.messageID)
		}
		if restored == 0 {
			return messageRestoredMsg{err: firstErr}
		}
		return messageRestoredMsg{box: ref.fromBox, count: restored, failed: len(moves) - restored}
	}
}

// snoozeDues is the shared list as due times by account and Message-ID,
// re-read only when the file changed: the list asks for it on every redraw.
var snoozeDues = struct {
	sync.Mutex
	path  string
	mtime time.Time
	size  int64
	due   map[string]time.Time
}{}

func loadSnoozeDues() map[string]time.Time {
	path := syncd.SnoozePath()
	info, err := os.Stat(path)
	snoozeDues.Lock()
	defer snoozeDues.Unlock()
	if err != nil {
		snoozeDues.path, snoozeDues.due = path, nil
		return nil
	}
	if snoozeDues.path == path && snoozeDues.mtime.Equal(info.ModTime()) && snoozeDues.size == info.Size() {
		return snoozeDues.due
	}
	snoozeDues.path, snoozeDues.mtime, snoozeDues.size = path, info.ModTime(), info.Size()
	snoozeDues.due = nil
	if entries, err := syncd.ReadSnooze(path); err == nil {
		snoozeDues.due = make(map[string]time.Time, len(entries))
		for _, entry := range entries {
			snoozeDues.due[entry.Key()] = entry.Due
		}
	}
	return snoozeDues.due
}

func snoozeDueOf(message maildir.Message) (time.Time, bool) {
	due, ok := loadSnoozeDues()[syncd.SnoozeKey(message.Account, message.MessageID)]
	return due, ok
}

// orderSnoozed puts the @Snoozed box in due order, soonest first, with
// anything the list does not know (filed there by hand) at the end. Other
// boxes and searches keep their order.
func (m Model) orderSnoozed(indexes []int) []int {
	if m.activeBox() != snoozedBox || strings.TrimSpace(m.query) != "" {
		return indexes
	}
	dues := loadSnoozeDues()
	dueAt := func(index int) (time.Time, bool) {
		message := m.messages[index]
		due, ok := dues[syncd.SnoozeKey(message.Account, message.MessageID)]
		return due, ok
	}
	slices.SortStableFunc(indexes, func(a, b int) int {
		dueA, okA := dueAt(a)
		dueB, okB := dueAt(b)
		switch {
		case okA && okB:
			return dueA.Compare(dueB)
		case okA:
			return -1
		case okB:
			return 1
		}
		return 0
	})
	return indexes
}

// snoozeListDate is the date column of a row in @Snoozed: when it returns.
func (m Model) snoozeListDate(message maildir.Message) (string, bool) {
	due, ok := snoozeDueOf(message)
	if !ok {
		return "", false
	}
	// Within a week the weekday and time; further out just the date, to fit the column.
	if now := m.now(); due.In(time.Local).Before(dayOf(now.In(time.Local)).AddDate(0, 0, 7)) {
		return snoozeDay(due, now), true
	}
	return formatListDate(due, m.now()), true
}
