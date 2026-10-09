package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/aronvandepol/mailday/internal/calendar"
	"github.com/aronvandepol/mailday/internal/maildir"
)

const inviteUID = "040000008200E00074C5B7101A82E00800000000AABBCC"

func requestInvitation() *maildir.Invitation {
	invitation := &maildir.Invitation{
		Method: "REQUEST", UID: inviteUID, Summary: "Seminar planning",
		Start:          time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC), // 14:00 in Amsterdam
		End:            time.Date(2026, 10, 14, 13, 0, 0, 0, time.UTC),
		Location:       "Atrium 2.04",
		OrganizerName:  "Roos, A.",
		OrganizerEmail: "a.roos@hum.uni.example",
		Attendees:      []maildir.InviteAttendee{{Email: "s.de.vries@hum.uni.example", Status: "needs-action"}},
		Link:           "https://teams.microsoft.com/l/meetup-join/abc",
	}
	for index := 1; index < 12; index++ {
		invitation.Attendees = append(invitation.Attendees, maildir.InviteAttendee{Email: fmt.Sprintf("guest%d@example.org", index), Status: "accepted"})
	}
	return invitation
}

// inviteEvent is the calendar's copy of requestInvitation, as calsync writes it.
func inviteEvent() calendar.Event {
	start := time.Date(2026, 10, 14, 14, 0, 0, 0, time.Local)
	return calendar.Event{ID: inviteUID + "/x", UID: strings.ToLower(inviteUID), Summary: "Seminar planning", Source: "University", RemoteID: "AAMk1",
		Organizer: "Roos, A.", RSVP: "needs-action", Start: start, End: start.Add(time.Hour)}
}

func readerWith(t *testing.T, invitation *maildir.Invitation, events ...calendar.Event) Model {
	t.Helper()
	model := calendarModel(t, events...)
	model.screen = screenMail
	model.focus = paneMail
	model.content = &maildir.Content{Body: "Agenda attached.", Invitation: invitation}
	model.contentPath = "/mail/university/Inbox/cur/invite"
	model.readerMessage = maildir.Message{Path: model.contentPath, Subject: "Seminar planning"}
	return model
}

func cardText(model Model) string {
	lines := model.inviteCard(120)
	for index := range lines {
		lines[index] = strings.TrimSpace(ansi.Strip(lines[index]))
	}
	return strings.Join(lines, "\n")
}

func TestInviteCardShowsWhatWhenWhoAndYourAnswer(t *testing.T) {
	model := readerWith(t, requestInvitation())
	got := cardText(model)
	// 12 attendees, Sam among them, and the organiser not: 12 guests. The
	// reading column wraps the sentence.
	want := "Invitation · Wed 14 Oct 14:00–15:00 · Atrium 2.04 · from Roos, A. · 12 guests · you: not answered"
	if !strings.Contains(strings.ReplaceAll(got, "\n", " "), want) {
		t.Fatalf("card:\n%s", got)
	}
	if !strings.Contains(got, "y accept · ~ maybe · x decline · J join · c show in calendar") {
		t.Fatalf("hints:\n%s", got)
	}

	// The reader draws it between header and body.
	view := ansi.Strip(model.renderMailDetail(120, 30))
	if !strings.Contains(view, "Invitation · Wed 14 Oct") || strings.Index(view, "Invitation ·") > strings.Index(view, "Agenda attached.") {
		t.Fatalf("reader:\n%s", view)
	}
}

func TestInviteCardTimeFollowsTheMachineZone(t *testing.T) {
	saved := time.Local
	defer func() { time.Local = saved }()
	time.Local = time.FixedZone("Seoul", 9*3600)
	model := readerWith(t, requestInvitation())
	if got := cardText(model); !strings.Contains(got, "Wed 14 Oct 21:00–22:00") {
		t.Fatalf("card:\n%s", got)
	}
}

func TestInviteCardWarnsOfClashesAndPrefersTheCalendarsAnswer(t *testing.T) {
	other := inviteEvent()
	other.ID, other.UID, other.RemoteID, other.Summary = "s/1", "seminar", "s1", "Seminar"
	other.Start, other.End = other.Start.Add(30*time.Minute), other.End.Add(30*time.Minute)
	mine := inviteEvent()
	mine.RSVP = "accepted"
	model := readerWith(t, requestInvitation(), mine, other)
	got := cardText(model)
	if !strings.Contains(got, "⚠ overlaps Seminar 14:30–15:30") {
		t.Fatalf("no clash note:\n%s", got)
	}
	if strings.Contains(got, "overlaps Seminar planning") || !strings.Contains(got, "you: accepted") {
		t.Fatalf("card:\n%s", got)
	}
}

func TestInviteCardForCancelAndReply(t *testing.T) {
	cancel := requestInvitation()
	cancel.Method, cancel.Sequence = "CANCEL", 2
	got := cardText(readerWith(t, cancel))
	if !strings.HasPrefix(got, "Cancelled · Wed 14 Oct 14:00–15:00") || strings.Contains(got, "y accept") {
		t.Fatalf("cancel:\n%s", got)
	}
	reply := &maildir.Invitation{Method: "REPLY", UID: "u", Start: cancel.Start, End: cancel.End,
		Attendees: []maildir.InviteAttendee{{Name: "Ada Lovelace", Email: "ada@example.org", Status: "accepted"}}}
	got = cardText(readerWith(t, reply))
	if !strings.HasPrefix(got, "Ada Lovelace accepted · Wed 14 Oct") {
		t.Fatalf("reply:\n%s", got)
	}
	repeating := requestInvitation()
	repeating.Repeats, repeating.Location = true, "https://meet.google.com/abc"
	got = strings.ReplaceAll(cardText(readerWith(t, repeating)), "\n", " ")
	if !strings.Contains(got, "online · from Roos, A.") || !strings.Contains(got, "· repeats ·") {
		t.Fatalf("repeating:\n%s", got)
	}
}

func TestNoCardWithoutAnInvitation(t *testing.T) {
	model := readerWith(t, nil)
	if lines := model.inviteCard(120); lines != nil {
		t.Fatalf("card %q", lines)
	}
	model = readerWith(t, requestInvitation())
	model.screen = screenHome
	if model.inviteCard(120) != nil {
		t.Fatal("card outside the reader")
	}
}

func TestAnsweringFromTheReaderRespondsOnTheMatchedEvent(t *testing.T) {
	for key_, answer := range map[string]string{"y": "accept", "~": "tentative", "x": "decline"} {
		log := fakeCalendarTool(t, `{"ok":true,"id":"AAMk1"}`)
		unrelated := gym()
		model := readerWith(t, requestInvitation(), unrelated, inviteEvent())
		updated, command := model.Update(key(key_))
		model = updated.(Model)
		if command == nil {
			t.Fatalf("%s: no command", key_)
		}
		selected, _ := model.selectedEvent()
		if selected.UID != strings.ToLower(inviteUID) || !selected.Pending {
			t.Fatalf("%s: selected %+v", key_, selected)
		}
		model = run(t, model, command)
		if got, want := calls(t, log), "respond --calendar University --id AAMk1 --answer "+answer; got != want {
			t.Fatalf("%s: call %q, want %q", key_, got, want)
		}
		if model.screen != screenMail {
			t.Fatalf("%s: left the reader", key_)
		}
	}
}

func TestAnsweringFindsAnEventOutsideTheLoadedWeek(t *testing.T) {
	log := fakeCalendarTool(t, `{"ok":true,"id":"AAMk1"}`)
	model := readerWith(t, requestInvitation())
	model.calendarStore = &fakeCalendarStore{result: calendar.Result{Events: []calendar.Event{gym(), inviteEvent()}}}
	updated, command := model.Update(key("y"))
	model = updated.(Model)
	if len(model.events) != 0 || command == nil {
		t.Fatalf("expected a lookup, got events %v", model.events)
	}
	updated, command = model.Update(command())
	model = updated.(Model)
	model = run(t, model, command)
	if got := calls(t, log); got != "respond --calendar University --id AAMk1 --answer accept" {
		t.Fatalf("call %q", got)
	}
}

func TestAnsweringBeforeCalsyncHasRunSaysSoAndRefreshes(t *testing.T) {
	log := fakeCalendarTool(t, `{"ok":true}`)
	bin := t.TempDir()
	refreshed := filepath.Join(bin, "refreshed")
	script := "#!/bin/sh\necho run >> " + refreshed + "\n"
	if err := os.WriteFile(filepath.Join(bin, "mailday-calsync"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	model := readerWith(t, requestInvitation())
	model.calendarStore = &fakeCalendarStore{}
	updated, command := model.Update(key("y"))
	updated, command = updated.(Model).Update(command())
	model = updated.(Model)
	if model.status != "Not in your calendar yet; try again in a minute" {
		t.Fatalf("status %q", model.status)
	}
	if command == nil {
		t.Fatal("no calendar refresh asked for")
	}
	command()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if data, _ := os.ReadFile(refreshed); len(data) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("mailday-calsync was not started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := calls(t, log); got != "" {
		t.Fatalf("answered an event that is not there: %q", got)
	}
}

func TestOwnEventsAndOtherMethodsAreNotAnswered(t *testing.T) {
	log := fakeCalendarTool(t, `{"ok":true}`)
	own := inviteEvent()
	own.Organizer = ""
	model := readerWith(t, requestInvitation(), own)
	updated, command := model.Update(key("y"))
	if command != nil || !strings.Contains(updated.(Model).status, "nothing to answer") {
		t.Fatalf("own event: %q", updated.(Model).status)
	}
	cancel := requestInvitation()
	cancel.Method = "CANCEL"
	model = readerWith(t, cancel, inviteEvent())
	updated, command = model.Update(key("y"))
	if command != nil || calls(t, log) != "" {
		t.Fatal("y answered a cancellation")
	}
}

func TestArchiveKeepsItsKeyOnAnInvitation(t *testing.T) {
	model := readerWith(t, requestInvitation(), inviteEvent())
	model.messages = []maildir.Message{model.readerMessage}
	updated, command := model.Update(key("a"))
	model = updated.(Model)
	if command != nil || model.status != "Press a again to archive this message" {
		t.Fatalf("a: %q", model.status)
	}
	if event, _ := model.selectedEvent(); event.Pending {
		t.Fatal("a answered the invitation")
	}
}

func TestJoinOpensTheInvitationsLink(t *testing.T) {
	bin := t.TempDir()
	log := filepath.Join(bin, "opened")
	for _, name := range []string{"xdg-open", "open"} {
		script := "#!/bin/sh\necho \"$*\" >> " + log + "\n"
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	model := readerWith(t, requestInvitation())
	updated, command := model.Update(key("J"))
	if command == nil || !strings.Contains(updated.(Model).status, "Joining Seminar planning") {
		t.Fatalf("status %q", updated.(Model).status)
	}
	command()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if data, _ := os.ReadFile(log); strings.TrimSpace(string(data)) == "https://teams.microsoft.com/l/meetup-join/abc" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the meeting link was not opened")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// A link written into the notes counts too; none at all says so.
	noted := requestInvitation()
	noted.Link, noted.Description = "", "Join: https://zoom.us/j/123"
	if got := readerWith(t, noted); got.inviteLink(noted) != "https://zoom.us/j/123" {
		t.Fatalf("link %q", got.inviteLink(noted))
	}
	bare := requestInvitation()
	bare.Link = ""
	updated, command = readerWith(t, bare).Update(key("J"))
	if command != nil || !strings.Contains(updated.(Model).status, "no meeting link") {
		t.Fatalf("status %q", updated.(Model).status)
	}
}

func TestShowInCalendarOpensTheDayViewOnTheEvent(t *testing.T) {
	model := readerWith(t, requestInvitation(), gym(), inviteEvent())
	updated, command := model.Update(key("c"))
	model = updated.(Model)
	if model.screen != screenHome || model.focus != paneAgenda || model.calendarMode != calendarDay {
		t.Fatalf("screen %v focus %v mode %v", model.screen, model.focus, model.calendarMode)
	}
	if !model.calendarAnchor.Equal(time.Date(2026, 10, 14, 0, 0, 0, 0, time.Local)) {
		t.Fatalf("anchor %v", model.calendarAnchor)
	}
	if event, _ := model.selectedEvent(); event.RemoteID != "AAMk1" {
		t.Fatalf("selected %+v", event)
	}
	if command == nil {
		t.Fatal("no reload of the day")
	}

	// Outside the loaded week the day's events are fetched first.
	model = readerWith(t, requestInvitation())
	model.calendarStore = &fakeCalendarStore{result: calendar.Result{Events: []calendar.Event{inviteEvent()}}}
	updated, command = model.Update(key("c"))
	updated, _ = updated.(Model).Update(command())
	model = updated.(Model)
	if event, ok := model.selectedEvent(); !ok || event.RemoteID != "AAMk1" || model.focus != paneAgenda {
		t.Fatalf("lookup: %+v focus %v", event, model.focus)
	}
}

// The Outlook fixture, read through the real Store, gives the card the
// spec's example (the organiser's name arrives as Exchange writes it).
func TestCardFromTheOutlookFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "maildir", "testdata", "outlook-request.eml"))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	for _, leaf := range []string{"cur", "new"} {
		if err := os.MkdirAll(filepath.Join(root, "university", "Inbox", leaf), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(root, "university", "Inbox", "cur", "invite:2,S")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	content, err := (&maildir.Store{Roots: []string{root}}).Read(path)
	if err != nil {
		t.Fatal(err)
	}
	model := readerWith(t, content.Invitation)
	got := strings.ReplaceAll(cardText(model), "\n", " ")
	want := "Invitation · Wed 14 Oct 14:00–15:00 · Atrium 2.04 · from Roos, A. · 3 guests · you: not answered"
	if !strings.Contains(got, want) {
		t.Fatalf("card:\n%s", got)
	}
}

func TestInvitationInTwoCalendarsIsNotAnsweredByGuess(t *testing.T) {
	start := time.Date(2026, 10, 14, 14, 0, 0, 0, time.Local)
	events := []calendar.Event{
		{ID: "a", UID: "inv@x", Source: "University", Start: start, End: start.Add(time.Hour)},
		{ID: "b", UID: "inv@x", Source: "Work", Start: start, End: start.Add(time.Hour)},
	}
	if _, ok := matchInviteEvent(events, &maildir.Invitation{UID: "inv@x", Start: start}); ok {
		t.Fatal("two calendars hold the invitation: no guessing")
	}
	if _, ok := matchInviteEvent(events[:1], &maildir.Invitation{UID: "inv@x", Start: start}); !ok {
		t.Fatal("one calendar: match")
	}
}

func TestProgramsAreNotOpened(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	for _, name := range []string{"run.command", "setup.EXE", "x.sh"} {
		if _, err := writeOpenFile(name, []byte("x")); err == nil {
			t.Errorf("%s was written to be opened", name)
		}
	}
	if _, err := writeOpenFile("paper.pdf", []byte("%PDF")); err != nil {
		t.Fatal(err)
	}
}
