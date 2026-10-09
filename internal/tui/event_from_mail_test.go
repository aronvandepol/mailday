package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/aronvandepol/mailday/internal/maildir"
)

// The calendarModel clock is Tue 6 Oct 2026 14:10.
func lineFor(t *testing.T, content maildir.Content) string {
	t.Helper()
	return calendarModel(t).eventLineFromMail(content)
}

func TestEventLineFromMail(t *testing.T) {
	sent := time.Date(2026, 10, 6, 9, 0, 0, 0, time.Local)
	for _, c := range []struct {
		name    string
		content maildir.Content
		want    string
	}{
		{"subject, first date phrase and sender",
			maildir.Content{Subject: "Re: Fwd: Seminar planning", FromAddr: "ada@example.org", Date: sent,
				Body: "Hi Sam,\n\nThanks for your note. Could we meet on Friday at 14:00 in the Atrium?\nBest, Ada"},
			"Seminar planning Friday at 14:00 +ada@example.org"},
		{"Dutch and German prefixes", maildir.Content{Subject: "Antw: AW: WG: Doorst: Budget", FromAddr: "a@b.nl", Date: sent, Body: "Nothing about a time."},
			"Budget +a@b.nl"},
		{"nothing found leaves the time to the defaults", maildir.Content{Subject: "Hello", FromAddr: "ada@example.org", Date: sent, Body: "Just saying hi."},
			"Hello +ada@example.org"},
		{"the first sentence with a date wins", maildir.Content{Subject: "Plan", FromAddr: "a@b.nl", Date: sent,
			Body: "Let us talk tomorrow 10:00. Or maybe friday 15:00."},
			"Plan tomorrow 10:00 +a@b.nl"},
		{"a past date is skipped for a later one", maildir.Content{Subject: "Plan", FromAddr: "a@b.nl", Date: sent,
			Body: "We met on 2020-01-05. The next one is on 2026-11-12 at 9:30."},
			"Plan 2026-11-12 at 9:30 +a@b.nl"},
		{"only past dates leave it empty", maildir.Content{Subject: "Minutes", FromAddr: "a@b.nl", Date: sent, Body: "Held on 2020-01-05."},
			"Minutes +a@b.nl"},
		{"a time alone counts as upcoming", maildir.Content{Subject: "Call", FromAddr: "a@b.nl", Date: sent, Body: "Shall we say at 16:30?"},
			"Call at 16:30 +a@b.nl"},
		{"a date with a length", maildir.Content{Subject: "Workshop", FromAddr: "a@b.nl", Date: sent, Body: "It runs on 12 nov from 10:00 to 12:00 for the whole team."},
			"Workshop 12 nov from 10:00 to 12:00 +a@b.nl"},
		{"quoted earlier messages do not count", maildir.Content{Subject: "Re: Lunch", FromAddr: "a@b.nl", Date: sent,
			Body: "Sounds good.\n\nOn Mon, 5 Oct 2026, Ada wrote:\n> Lunch tomorrow 12:30?\n"},
			"Lunch +a@b.nl"},
		{"the signature does not count", maildir.Content{Subject: "Hi", FromAddr: "a@b.nl", Date: sent,
			Body: "Hello there.\n-- \nAda, office hours tuesday 9:00"},
			"Hi +a@b.nl"},
		{"a wrapped sentence is read whole", maildir.Content{Subject: "Meet", FromAddr: "a@b.nl", Date: sent,
			Body: "Could we meet on friday\nat 14:00 in my office\nplease?"},
			"Meet friday at 14:00 +a@b.nl"},
		{"list items stay separate", maildir.Content{Subject: "Options", FromAddr: "a@b.nl", Date: sent,
			Body: "Choose one:\n- 2026-10-12 10:00\n- 2026-10-13 11:00\n"},
			"Options 2026-10-12 10:00 +a@b.nl"},
		{"HTML-only mail is read through its text", maildir.Content{Subject: "Invite", FromAddr: "a@b.nl", Date: sent,
			HTML: "<html><body><p>See you <b>on 2026-10-20 at 15:00</b>.</p></body></html>"},
			"Invite 2026-10-20 at 15:00 +a@b.nl"},
		{"an old message gets its date spelled out", maildir.Content{Subject: "Lunch", FromAddr: "a@b.nl", Date: time.Date(2026, 10, 5, 9, 0, 0, 0, time.Local),
			Body: "Let us do lunch in 3 days at 12:30."},
			"Lunch 2026-10-08 at 12:30 +a@b.nl"},
		{"an old message whose day has passed leaves it empty", maildir.Content{Subject: "Lunch", FromAddr: "a@b.nl", Date: time.Date(2026, 10, 1, 9, 0, 0, 0, time.Local),
			Body: "Let us do lunch on Friday at 12:30."},
			"Lunch +a@b.nl"},
		{"words in the subject that mean something stay words", maildir.Content{Subject: "Meeting friday 14:00 #3 @room", FromAddr: "a@b.nl", Date: sent, Body: "ok"},
			`Meeting "friday" "14:00" 3 room +a@b.nl`},
		{"no sender address", maildir.Content{Subject: "Hello", Date: sent, Body: "x"}, "Hello"},
		{"a sender that is not an address is not invited", maildir.Content{Subject: "Hello", FromAddr: "undisclosed", Date: sent, Body: "x"}, "Hello"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := lineFor(t, c.content); got != c.want {
				t.Errorf("line\n got %q\nwant %q", got, c.want)
			}
		})
	}
}

// openMessage puts the reader's message in the list, as it is when really open.
func openMessage(model Model) Model {
	model.messages = []maildir.Message{model.readerMessage}
	return model
}

func TestEventLineReadsBackAsAnEvent(t *testing.T) {
	model := calendarModel(t)
	line := model.eventLineFromMail(maildir.Content{Subject: "Re: Seminar planning", FromAddr: "ada@example.org", Date: time.Date(2026, 10, 6, 9, 0, 0, 0, time.Local),
		Body: "Could we meet on Friday at 14:00?"})
	event, people, err := model.draftEvent(line)
	if err != nil {
		t.Fatal(err)
	}
	if event.Summary != "Seminar planning" || !event.Start.Equal(time.Date(2026, 10, 9, 14, 0, 0, 0, time.Local)) || len(people) != 1 || people[0] != "ada@example.org" {
		t.Fatalf("event %+v people %v", event, people)
	}
}

func TestEFillsTheQuickAddBoxInTheReader(t *testing.T) {
	log := fakeCalendarTool(t, `{"ok":true,"id":"e1"}`)
	model := openMessage(readerWith(t, nil))
	model.content = &maildir.Content{Subject: "Re: Seminar planning", FromAddr: "ada@example.org", Date: time.Date(2026, 10, 6, 9, 0, 0, 0, time.Local),
		Body: "Could we meet on Friday at 14:00?"}
	updated, _ := model.Update(key("E"))
	model = updated.(Model)
	if model.calPrompt == nil || model.calPrompt.kind != promptNew || model.calPrompt.input != "Seminar planning Friday at 14:00 +ada@example.org" {
		t.Fatalf("prompt %+v", model.calPrompt)
	}
	if model.screen != screenMail {
		t.Fatal("the reader should stay underneath")
	}
	if view := ansiStrip(model.View().Content); !strings.Contains(view, "New event") || !strings.Contains(view, "Seminar planning") {
		t.Fatalf("the box is not drawn over the reader:\n%s", view)
	}
	// Edit as usual, then enter.
	model = typeInto(t, model, " #University")
	updated, command := model.Update(key("enter"))
	model = run(t, updated.(Model), command)
	want := "create --calendar University --title Seminar planning --location  --start 2026-10-09T14:00:00+02:00 --end 2026-10-09T15:00:00+02:00 --attendee ada@example.org"
	if got := calls(t, log); got != want {
		t.Fatalf("call\n got %q\nwant %q", got, want)
	}
	if model.calPrompt != nil || model.screen != screenMail {
		t.Fatalf("prompt %v screen %v", model.calPrompt, model.screen)
	}
}

func TestEEscLeavesTheMessageAlone(t *testing.T) {
	model := openMessage(readerWith(t, nil))
	model.content = &maildir.Content{Subject: "Hi", FromAddr: "a@b.nl", Body: "x"}
	updated, _ := model.Update(key("E"))
	updated, _ = updated.Update(key("esc"))
	model = updated.(Model)
	if model.calPrompt != nil || model.screen != screenMail || model.contentPath == "" {
		t.Fatalf("prompt %v screen %v", model.calPrompt, model.screen)
	}
}

func TestENeedsTheCalendarTool(t *testing.T) {
	model := openMessage(readerWith(t, nil))
	model.writableCalendars = nil
	updated, _ := model.Update(key("E"))
	model = updated.(Model)
	if model.calPrompt != nil || !strings.Contains(model.status, "mailday-calendar") {
		t.Fatalf("prompt %v status %q", model.calPrompt, model.status)
	}
}

func TestEIsIgnoredOutsideTheReader(t *testing.T) {
	model := calendarModel(t)
	model.focus = paneMail
	updated, _ := model.Update(key("E"))
	if updated.(Model).calPrompt != nil {
		t.Fatal("E on the list should not open the box")
	}
}

func TestEventFromMailDoesNotInviteYouOrRobots(t *testing.T) {
	for _, address := range []string{"sam.devries@gmail.example", "no-reply@accounts.google.com", "notifications@github.com"} {
		model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
		line := model.eventLineFromMail(maildir.Content{Subject: "Meeting", FromAddr: address, Body: "See you tomorrow at 14:00."})
		if strings.Contains(line, "+") {
			t.Errorf("%s: %q invites the sender", address, line)
		}
	}
}
