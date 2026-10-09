package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/aronvandepol/mailday/internal/calendar"
	"github.com/aronvandepol/mailday/internal/maildir"
	"github.com/aronvandepol/mailday/internal/syncd"
)

// upcomingNow is Wed 7 Oct 2026, 10:30 (snoozeNow), in whatever zone the test runs.
func upcomingNow() time.Time { return time.Date(2026, 10, 7, 10, 30, 0, 0, time.Local) }

// inDays is a clock time on the day offset days from upcomingNow.
func inDays(offset, hour, minute int) time.Time {
	return time.Date(2026, 10, 7+offset, hour, minute, 0, 0, time.Local)
}

// snoozedMessage is a message sitting in @Snoozed.
func snoozedMessage(subject, from, id string) maildir.Message {
	return maildir.Message{
		Path: "/mail/gmail/@Snoozed/cur/" + id, Account: "gmail", Box: snoozedBox, From: from, Subject: subject,
		MessageID: "<" + id + "@x>", Date: inDays(-2, 9, 0),
	}
}

// writeSnooze puts due times in the temporary snooze list, never the real one.
func writeSnooze(t *testing.T, path string, dues map[string]time.Time) {
	t.Helper()
	for id, due := range dues {
		entry := syncd.SnoozeEntry{MessageID: "<" + id + "@x>", Account: "gmail", Due: due, FromBox: "Inbox", Added: upcomingNow()}
		if err := syncd.AddSnooze(path, entry); err != nil {
			t.Fatal(err)
		}
	}
}

// upcomingModel is the agenda at 10:30 with the events and messages given and
// the snooze list (see snoozeEnv) read, as a mail load would have left it.
func upcomingModel(t *testing.T, events []calendar.Event, messages ...maildir.Message) Model {
	t.Helper()
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{Days: 14})
	model.now = upcomingNow
	model.width, model.height = 120, 40
	model.loadingMail, model.loadingCal = false, false
	model.status = ""
	model.boxes = []string{"Inbox", snoozedBox}
	model.accounts = []string{"gmail"}
	model.messages = messages
	model.events = events
	model.focus, model.calendarMode = paneAgenda, calendarAgenda
	model.refreshUpcoming()
	return model
}

func meeting(id, title string, start time.Time) calendar.Event {
	return calendar.Event{ID: id, Summary: title, Source: "Work", Start: start, End: start.Add(time.Hour)}
}

func pressKeys(model Model, keys ...tea.KeyPressMsg) Model {
	for _, k := range keys {
		updated, _ := model.Update(k)
		model = updated.(Model)
	}
	return model
}

var (
	downKey   = tea.KeyPressMsg{Code: tea.KeyDown}
	upKey     = tea.KeyPressMsg{Code: tea.KeyUp}
	returnKey = tea.KeyPressMsg{Code: tea.KeyEnter}
)

// snoozeFixture is two snoozed messages and an event between them, the file
// written first so refreshUpcoming reads it.
func snoozeFixture(t *testing.T) Model {
	t.Helper()
	path := snoozeEnv(t)
	writeSnooze(t, path, map[string]time.Time{"grant": inDays(1, 8, 0), "late": inDays(0, 16, 0)})
	events := []calendar.Event{
		meeting("e1", "Seminar", inDays(0, 14, 0)),
		meeting("e2", "Supervision", inDays(1, 9, 0)),
	}
	return upcomingModel(t, events,
		snoozedMessage("Re: Grant application", "Ruben Bakker", "grant"),
		snoozedMessage("Referee report", "Editor", "late"))
}

func TestAgendaListsSnoozedMailAtItsDueTime(t *testing.T) {
	view := ansiStrip(snoozeFixture(t).View().Content)
	for _, want := range []string{
		"08:00  ✉ back: Re: Grant application · Ruben Bakker",
		"16:00  ✉ back: Referee report · Editor",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("no %q in:\n%s", want, view)
		}
	}
	// By time: today's seminar, the 16:00 mail, then tomorrow's 08:00 mail before the 09:00 event.
	order := []string{"Seminar", "Referee report", "Re: Grant application", "Supervision"}
	last := -1
	for _, part := range order {
		at := strings.Index(view, part)
		if at < 0 || at < last {
			t.Fatalf("%q out of order (at %d, after %d) in:\n%s", part, at, last, view)
		}
		last = at
	}
	// The mail sits under its own day's heading.
	if tomorrow := strings.Index(view, "Tomorrow · Thursday, October 8"); tomorrow < 0 || tomorrow > strings.Index(view, "Re: Grant application") {
		t.Fatalf("no Tomorrow heading before the mail:\n%s", view)
	}
}

func TestAgendaLeavesOutMailItDoesNotKnow(t *testing.T) {
	path := snoozeEnv(t)
	writeSnooze(t, path, map[string]time.Time{
		"far":    inDays(40, 8, 0), // beyond the 14 days shown
		"inbox":  inDays(1, 8, 0),  // an entry whose message is not in @Snoozed
		"listed": inDays(2, 8, 0),
	})
	inbox := snoozedMessage("Back in the inbox", "Ada", "inbox")
	inbox.Box = "Inbox"
	model := upcomingModel(t, nil,
		snoozedMessage("Too far", "Ada", "far"),
		inbox,
		snoozedMessage("Filed by hand", "Ada", "unlisted"), // no entry
		snoozedMessage("Shown", "Ada", "listed"),
	)
	model.refreshUpcoming()
	view := ansiStrip(model.View().Content)
	if !strings.Contains(view, "✉ back: Shown · Ada") {
		t.Fatalf("listed mail missing:\n%s", view)
	}
	for _, gone := range []string{"Too far", "Back in the inbox", "Filed by hand"} {
		if strings.Contains(view, gone) {
			t.Fatalf("%q listed:\n%s", gone, view)
		}
	}
}

func TestAgendaShowsPastDueMailAsDueNowUnderToday(t *testing.T) {
	path := snoozeEnv(t)
	writeSnooze(t, path, map[string]time.Time{"over": inDays(-1, 8, 0)})
	events := []calendar.Event{meeting("e0", "Standup", inDays(0, 9, 0)), meeting("e1", "Seminar", inDays(0, 14, 0))}
	model := upcomingModel(t, events, snoozedMessage("Overdue reply", "Ada", "over"))
	model.refreshUpcoming()
	view := ansiStrip(model.View().Content)
	if !strings.Contains(view, "✉ due now: Overdue reply · Ada") {
		t.Fatalf("no due-now row:\n%s", view)
	}
	// Under today, after the meeting that has begun and before the one to come.
	standup, due, seminar := strings.Index(view, "Standup"), strings.Index(view, "✉ due now"), strings.Index(view, "Seminar")
	if !(standup < due && due < seminar) || strings.Index(view, "Today ·") > due {
		t.Fatalf("due-now row misplaced:\n%s", view)
	}
	if strings.Contains(view, "Tuesday, October 6") {
		t.Fatalf("a past day was listed:\n%s", view)
	}
	// Another week on screen: today is not in it, so the row is not either.
	model.calendarAnchor = inDays(21, 0, 0)
	if strings.Contains(ansiStrip(model.View().Content), "due now") {
		t.Fatal("past-due mail shown while today is not in view")
	}
}

func TestAgendaNowLineComesBeforeMailStillToCome(t *testing.T) {
	model := snoozeFixture(t)
	view := ansiStrip(model.View().Content)
	now, late := strings.Index(view, "── now 10:30 · next in 3 h 30 min ──"), strings.Index(view, "Referee report")
	if now < 0 || now > late {
		t.Fatalf("now line (%d) not before the 16:00 mail (%d):\n%s", now, late, view)
	}
}

func TestArrowsMoveOverSnoozedRowsLikeEvents(t *testing.T) {
	model := snoozeFixture(t)
	// Rows in order: Seminar, 16:00 mail, 08:00 mail, Supervision.
	if model.eventCursor != 0 {
		t.Fatalf("starts at %d", model.eventCursor)
	}
	model = pressKeys(model, downKey)
	if u, ok := model.selectedUpcoming(); !ok || u.message.Subject != "Referee report" {
		t.Fatalf("second row: %+v", u)
	}
	if _, ok := model.selectedEvent(); ok {
		t.Fatal("an event is selected too")
	}
	model = pressKeys(model, downKey)
	if u, _ := model.selectedUpcoming(); u.message.Subject != "Re: Grant application" {
		t.Fatalf("third row: %q", u.message.Subject)
	}
	model = pressKeys(model, downKey)
	if event, ok := model.selectedEvent(); !ok || event.Summary != "Supervision" {
		t.Fatalf("fourth row: %+v", event)
	}
	model = pressKeys(model, downKey, downKey) // the end stops there
	if event, _ := model.selectedEvent(); event.Summary != "Supervision" {
		t.Fatalf("past the end: %+v", event)
	}
	model = pressKeys(model, upKey, upKey, upKey)
	if event, ok := model.selectedEvent(); !ok || event.Summary != "Seminar" {
		t.Fatalf("back at the top: %+v", event)
	}
	view := ansiStrip(pressKeys(model, downKey).View().Content)
	if !strings.Contains(view, "│ 16:00  ✉ back: Referee report") {
		t.Fatalf("no cursor bar on the snoozed row:\n%s", view)
	}
	// The selected row has its own footer.
	footer := ansiStrip(pressKeys(model, downKey).renderFooter(120))
	if !strings.Contains(footer, "Z") || !strings.Contains(footer, "snooze again") || strings.Contains(footer, "rebook") {
		t.Fatalf("footer on a snoozed row: %q", footer)
	}
}

func TestEnterOnASnoozedRowOpensTheMessage(t *testing.T) {
	model := pressKeys(snoozeFixture(t), downKey)
	updated, command := model.Update(returnKey)
	opened := updated.(Model)
	if opened.screen != screenMail || opened.contentPath != "/mail/gmail/@Snoozed/cur/late" || command == nil {
		t.Fatalf("screen %v, path %q, command %v", opened.screen, opened.contentPath, command)
	}
	if opened.readerMessage.Subject != "Referee report" {
		t.Fatalf("reader has %q", opened.readerMessage.Subject)
	}
	// Back from the reader lands on the same row of the agenda.
	back := pressKeys(opened, tea.KeyPressMsg{Code: tea.KeyEscape})
	if back.screen != screenHome || back.focus != paneAgenda {
		t.Fatalf("after esc: screen %v focus %v", back.screen, back.focus)
	}
	if u, ok := back.selectedUpcoming(); !ok || u.message.Subject != "Referee report" {
		t.Fatalf("selection after esc: %+v", u)
	}
}

func TestEnterOnAnEventStillOpensTheEvent(t *testing.T) {
	updated, _ := snoozeFixture(t).Update(returnKey)
	if updated.(Model).screen != screenEvent {
		t.Fatalf("screen %v", updated.(Model).screen)
	}
}

func TestSnoozedRowsRefuseEventKeys(t *testing.T) {
	model := pressKeys(snoozeFixture(t), downKey)
	for _, k := range []string{"m", "e", "d", "D", "a", "~", "x", "J", "<", ">", "+", "-"} {
		updated, command := model.Update(key(k))
		after := updated.(Model)
		if command != nil || after.status != "That is a snoozed message, not an event" {
			t.Fatalf("%s: status %q, command %v", k, after.status, command)
		}
		if after.calPrompt != nil || after.eventForm != nil || after.pendingDelete != "" {
			t.Fatalf("%s opened something", k)
		}
	}
}

func TestZOnASnoozedRowSnoozesItAgain(t *testing.T) {
	model := snoozeFixture(t)
	path := syncd.SnoozePath()
	model = pressKeys(model, downKey)
	model = pressKeys(model, key("Z"))
	if model.snoozing == nil || model.snoozing.message.Subject != "Referee report" {
		t.Fatalf("no prompt: %+v", model.snoozing)
	}
	for _, r := range "tomorrow 9" {
		model = pressKeys(model, key(string(r)))
	}
	updated, command := model.Update(returnKey)
	model = updated.(Model)
	if command == nil {
		t.Fatalf("no snooze command; status %q", model.status)
	}
	updated, _ = model.Update(command())
	model = updated.(Model)
	if !strings.HasPrefix(model.status, "Snoozed until ") {
		t.Fatalf("status %q", model.status)
	}
	entries, err := syncd.ReadSnooze(path)
	if err != nil {
		t.Fatal(err)
	}
	var due time.Time
	for _, entry := range entries {
		if entry.MessageID == "<late@x>" {
			due = entry.Due
		}
	}
	if want := inDays(1, 9, 0); !due.Equal(want) {
		t.Fatalf("due %v, want %v", due, want)
	}
	// The agenda lists it at the new time straight away.
	if view := ansiStrip(model.View().Content); !strings.Contains(view, "09:00  ✉ back: Referee report") {
		t.Fatalf("not at the new time:\n%s", view)
	}
}

func TestZWithAnEventSelectedDoesNothing(t *testing.T) {
	model := pressKeys(snoozeFixture(t), key("Z"))
	if model.snoozing != nil {
		t.Fatal("snooze prompt on an event")
	}
}

func TestSnoozeEntriesAreReadWhenMailReloadsNotOnEveryFrame(t *testing.T) {
	path := snoozeEnv(t)
	writeSnooze(t, path, map[string]time.Time{"grant": inDays(1, 8, 0)})
	grant := snoozedMessage("Re: Grant application", "Ruben Bakker", "grant")
	store := &fakeMailStore{result: maildir.ListResult{Messages: []maildir.Message{grant}, Accounts: []string{"gmail"}, Boxes: []string{"Inbox", snoozedBox}}}
	model := NewModel(store, &fakeCalendarStore{}, Options{Days: 14})
	model.now = upcomingNow
	model.width, model.height = 120, 40
	model.loadingCal = false
	model.focus, model.calendarMode = paneAgenda, calendarAgenda
	updated, _ := model.Update(mailLoadedMsg{result: store.result})
	model = updated.(Model)
	if !strings.Contains(ansiStrip(model.View().Content), "08:00  ✉ back: Re: Grant application") {
		t.Fatalf("first load:\n%s", ansiStrip(model.View().Content))
	}

	// The list changes under a running Mailday; redraws leave the rows alone.
	writeSnooze(t, path, map[string]time.Time{"grant": inDays(1, 11, 15)})
	if view := ansiStrip(model.View().Content); !strings.Contains(view, "08:00  ✉ back: Re: Grant") || strings.Contains(view, "11:15") {
		t.Fatalf("a redraw re-read the list:\n%s", view)
	}
	updated, _ = model.Update(mailLoadedMsg{result: store.result, quiet: true})
	model = updated.(Model)
	if view := ansiStrip(model.View().Content); !strings.Contains(view, "11:15  ✉ back: Re: Grant") {
		t.Fatalf("a reload did not:\n%s", view)
	}

	// The message leaving @Snoozed takes its row with it, and a selected row with it.
	model = pressKeys(model, downKey)
	store.result.Messages = nil
	updated, _ = model.Update(mailLoadedMsg{result: store.result, quiet: true})
	model = updated.(Model)
	if len(model.upcoming) != 0 || model.eventCursor != 0 {
		t.Fatalf("upcoming %d, cursor %d", len(model.upcoming), model.eventCursor)
	}
}

func TestReloadKeepsTheSelectedSnoozedRow(t *testing.T) {
	model := pressKeys(snoozeFixture(t), downKey, downKey) // the 08:00 mail
	extra := snoozedMessage("Earlier one", "Ada", "early")
	writeSnooze(t, syncd.SnoozePath(), map[string]time.Time{"early": inDays(0, 12, 0)})
	store := &fakeMailStore{result: maildir.ListResult{Messages: append([]maildir.Message{extra}, model.messages...), Accounts: []string{"gmail"}, Boxes: model.boxes}}
	model.mailStore = store
	updated, _ := model.Update(mailLoadedMsg{result: store.result, quiet: true})
	model = updated.(Model)
	if u, ok := model.selectedUpcoming(); !ok || u.message.Subject != "Re: Grant application" {
		t.Fatalf("selection moved to %+v", u)
	}
}

func TestDayViewListsSnoozedMail(t *testing.T) {
	model := snoozeFixture(t)
	model.calendarMode = calendarDay
	view := ansiStrip(model.View().Content)
	if !strings.Contains(view, "Mail coming back") || !strings.Contains(view, "16:00 ✉ Referee report · Editor") {
		t.Fatalf("wide day view:\n%s", view)
	}
	if strings.Contains(view, "Re: Grant application") {
		t.Fatalf("tomorrow's mail on today's panel:\n%s", view)
	}
	// Selecting the row puts the message in the panel.
	model = pressKeys(model, downKey)
	view = ansiStrip(model.View().Content)
	for _, want := range []string{"✉ Referee report", "Back in your inbox 16:00", "enter reads it · Z snoozes it again"} {
		if !strings.Contains(view, want) {
			t.Fatalf("no %q in:\n%s", want, view)
		}
	}
	// A narrow terminal has no panel: the rows go under the grid.
	model.width = 70
	if view := ansiStrip(model.View().Content); !strings.Contains(view, "16:00 ✉ Referee report") {
		t.Fatalf("narrow day view:\n%s", view)
	}
}

func TestDayViewWithNoEventsStillListsTheMail(t *testing.T) {
	path := snoozeEnv(t)
	writeSnooze(t, path, map[string]time.Time{"late": inDays(0, 16, 0)})
	model := upcomingModel(t, nil, snoozedMessage("Referee report", "Editor", "late"))
	model.calendarMode = calendarDay
	model.refreshUpcoming()
	view := ansiStrip(model.View().Content)
	if !strings.Contains(view, "Nothing planned") || !strings.Contains(view, "16:00 ✉ Referee report") {
		t.Fatalf("day view:\n%s", view)
	}
}

func TestWeekGridDoesNotListMailOrCatchTheCursor(t *testing.T) {
	model := snoozeFixture(t)
	model.calendarMode = calendarWeek
	model.width = 140
	if rows := model.visibleUpcoming(); len(rows) != 0 {
		t.Fatalf("week grid has %d snoozed rows", len(rows))
	}
	model = pressKeys(model, downKey)
	if _, ok := model.selectedUpcoming(); ok || model.eventCursor != 1 {
		t.Fatalf("cursor %d moved onto a row the grid does not show", model.eventCursor)
	}
	// Too narrow for a grid, the week shows the agenda, snoozed mail included.
	model.width = 60
	if len(model.visibleUpcoming()) == 0 {
		t.Fatal("narrow week does not list the mail")
	}
}

func TestSwitchingViewClearsTheSnoozedSelection(t *testing.T) {
	model := pressKeys(snoozeFixture(t), downKey)
	model = pressKeys(model, key("2"))
	if _, ok := model.selectedUpcoming(); ok || model.eventCursor != 0 {
		t.Fatalf("cursor %d after changing the view", model.eventCursor)
	}
}

func TestTodayStripNamesSnoozedMailReturningWithinTheHour(t *testing.T) {
	path := snoozeEnv(t)
	writeSnooze(t, path, map[string]time.Time{"soon": inDays(0, 11, 15), "later": inDays(0, 12, 0)})
	model := upcomingModel(t, nil,
		snoozedMessage("Later one", "Ada", "later"),
		snoozedMessage("Reviewer reply", "Ruben Bakker", "soon"))
	model.refreshUpcoming()
	model.focus = paneMail
	if got := ansiStrip(model.todayStripLine(120)); !strings.Contains(got, "✉ back at 11:15: Reviewer reply") {
		t.Fatalf("strip %q", got)
	}
	if view := ansiStrip(model.View().Content); !strings.Contains(view, "✉ back at 11:15: Reviewer reply") {
		t.Fatalf("strip not in the view:\n%s", view)
	}
}

func TestTodayStripPrefersAMeetingAndIgnoresMailAnHourOrMoreAway(t *testing.T) {
	path := snoozeEnv(t)
	writeSnooze(t, path, map[string]time.Time{"soon": inDays(0, 11, 0), "hour": inDays(0, 11, 30), "gone": inDays(0, 10, 0)})
	events := []calendar.Event{meeting("e1", "Seminar", inDays(0, 10, 50))}
	model := upcomingModel(t, events, snoozedMessage("Soon", "Ada", "soon"))
	model.refreshUpcoming()
	model.focus = paneMail
	if got := ansiStrip(model.todayStripLine(120)); !strings.Contains(got, "next 10:50 Seminar") || strings.Contains(got, "✉") {
		t.Fatalf("meeting should win: %q", got)
	}
	// Exactly an hour away or already due: not for the strip.
	model = upcomingModel(t, nil, snoozedMessage("An hour", "Ada", "hour"), snoozedMessage("Due", "Ada", "gone"))
	model.refreshUpcoming()
	model.focus = paneMail
	if got := model.todayStripLine(120); got != "" {
		t.Fatalf("strip %q", ansiStrip(got))
	}
	// Only on the mail list.
	model.focus = paneAgenda
	if model.todayStripLine(120) != "" {
		t.Fatal("strip in the calendar pane")
	}
}

func TestSnoozedRowsFollowTheZoneInUse(t *testing.T) {
	useZone(t, "Asia/Seoul") // runs in the zone child process, see zoneTests

	// 08:00 in Seoul is 01:00 in Amsterdam: the row says what the wall clock here says.
	path := snoozeEnv(t)
	writeSnooze(t, path, map[string]time.Time{"grant": inDays(1, 8, 0), "late": inDays(0, 23, 30)})
	model := upcomingModel(t, []calendar.Event{meeting("e1", "Seminar", inDays(1, 9, 0))},
		snoozedMessage("Re: Grant application", "Ruben Bakker", "grant"),
		snoozedMessage("Late night", "Ada", "late"))
	model.refreshUpcoming()
	view := ansiStrip(model.View().Content)
	for _, want := range []string{"08:00  ✉ back: Re: Grant application · Ruben Bakker", "23:30  ✉ back: Late night · Ada"} {
		if !strings.Contains(view, want) {
			t.Fatalf("no %q in:\n%s", want, view)
		}
	}
	if tomorrow, row := strings.Index(view, "Tomorrow · Thursday, October 8"), strings.Index(view, "Re: Grant"); tomorrow < 0 || row < tomorrow {
		t.Fatalf("08:00 mail not under Tomorrow:\n%s", view)
	}
	if strings.Index(view, "Re: Grant") > strings.Index(view, "Seminar") {
		t.Fatalf("08:00 mail after the 09:00 event:\n%s", view)
	}
	// And the strip, in Seoul time.
	model = upcomingModel(t, nil, snoozedMessage("Soon", "Ada", "soon"))
	writeSnooze(t, path, map[string]time.Time{"soon": inDays(0, 11, 0)})
	model.refreshUpcoming()
	model.focus = paneMail
	if got := ansiStrip(model.todayStripLine(120)); !strings.Contains(got, "✉ back at 11:00: Soon") {
		t.Fatalf("strip %q", got)
	}
}
