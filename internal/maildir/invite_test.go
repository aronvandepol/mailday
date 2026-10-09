package maildir

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// readFixture files testdata/name in a temporary Maildir and reads it back
// through the Store, as the reader does.
func readFixture(t *testing.T, name string) Content {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	dir := filepath.Join(root, "gmail", "Inbox")
	for _, leaf := range []string{"cur", "new"} {
		if err := os.MkdirAll(filepath.Join(dir, leaf), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "cur", "fixture:2,S")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	content, err := (&Store{Roots: []string{root}}).Read(path)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func ownTestAddress(address string) bool {
	return address == "s.de.vries@hum.uni.example" || address == "sam.devries@gmail.example"
}

func TestOutlookRequestWithWindowsZone(t *testing.T) {
	invitation := readFixture(t, "outlook-request.eml").Invitation
	if invitation == nil {
		t.Fatal("no invitation parsed")
	}
	if invitation.Method != "REQUEST" || invitation.Summary != "Seminar planning" || invitation.Location != "Atrium 2.04" {
		t.Fatalf("basics: %+v", invitation)
	}
	// 14:00 W. Europe Standard Time on 14 Oct 2026 is CEST, UTC+2.
	if want := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC); !invitation.Start.Equal(want) {
		t.Fatalf("start %v, want %v", invitation.Start.UTC(), want)
	}
	if invitation.End.Sub(invitation.Start) != time.Hour || invitation.AllDay || invitation.Repeats {
		t.Fatalf("span: %+v", invitation)
	}
	if invitation.OrganizerName != "Roos, A." || invitation.OrganizerEmail != "a.roos@hum.uni.example" {
		t.Fatalf("organizer %q %q", invitation.OrganizerName, invitation.OrganizerEmail)
	}
	// Ada and Bo are guests; the organiser is not listed, the room is not a guest.
	if got := invitation.Guests(); got != 3 {
		t.Fatalf("guests %d, want 3 (Sam, Ada, Bo)", got)
	}
	if got := invitation.StatusOf(ownTestAddress); got != "needs-action" {
		t.Fatalf("my status %q", got)
	}
	if invitation.Link != "https://teams.microsoft.com/l/meetup-join/19%3ameeting_abc%40thread.v2/0" {
		t.Fatalf("link %q", invitation.Link)
	}
	// The VALARM's DESCRIPTION must not replace the event's.
	if invitation.Description == "Reminder" || invitation.Description == "" {
		t.Fatalf("description %q", invitation.Description)
	}
}

func TestGoogleRequestInUTCKeepsInviteOutOfAttachments(t *testing.T) {
	content := readFixture(t, "google-request.eml")
	invitation := content.Invitation
	if invitation == nil {
		t.Fatal("no invitation parsed")
	}
	if want := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC); !invitation.Start.Equal(want) {
		t.Fatalf("start %v", invitation.Start)
	}
	if !invitation.Repeats || invitation.Sequence != 1 || invitation.UID != "abc123def@google.com" {
		t.Fatalf("details: %+v", invitation)
	}
	if got := invitation.StatusOf(ownTestAddress); got != "tentative" {
		t.Fatalf("my status %q", got)
	}
	if invitation.Guests() != 1 {
		t.Fatalf("guests %d", invitation.Guests())
	}
	// invite.ics (the attachment copy) is the card; agenda.pdf stays.
	if len(content.Attachments) != 1 || content.Attachments[0].Name != "agenda.pdf" {
		t.Fatalf("attachments %+v", content.Attachments)
	}
}

func TestCancelAndReply(t *testing.T) {
	cancel := readFixture(t, "outlook-cancel.eml").Invitation
	if cancel == nil || cancel.Method != "CANCEL" || cancel.Sequence != 2 {
		t.Fatalf("cancel: %+v", cancel)
	}
	replyContent := readFixture(t, "google-reply.eml")
	reply := replyContent.Invitation
	if reply == nil || reply.Method != "REPLY" || len(reply.Attendees) != 1 {
		t.Fatalf("reply: %+v", reply)
	}
	if attendee := reply.Attendees[0]; attendee.Name != "Ada Lovelace" || attendee.Status != "accepted" {
		t.Fatalf("attendee %+v", attendee)
	}
	// A calendar-only message: the .ics is the card, not an attachment.
	if len(replyContent.Attachments) != 0 {
		t.Fatalf("attachments %+v", replyContent.Attachments)
	}
}

func TestNonInvitationCalendarFileStaysAttachment(t *testing.T) {
	ics := "BEGIN:VCALENDAR\nMETHOD:PUBLISH\nBEGIN:VEVENT\nUID:x\nDTSTART:20261014T120000Z\nEND:VEVENT\nEND:VCALENDAR\n"
	if parseInvitation(ics, "") != nil {
		t.Fatal("PUBLISH is not an invitation")
	}
	bare := "BEGIN:VCALENDAR\nBEGIN:VEVENT\nUID:x\nDTSTART:20261014T120000Z\nEND:VEVENT\nEND:VCALENDAR\n"
	if invitation := parseInvitation(bare, "request"); invitation == nil || invitation.Method != "REQUEST" {
		t.Fatal("the Content-Type method is the fallback when METHOD is missing")
	}
	if parseInvitation("BEGIN:VCALENDAR\nMETHOD:REQUEST\nEND:VCALENDAR\n", "") != nil {
		t.Fatal("no event, no invitation")
	}
}

func TestAllDayAndFloatingTimes(t *testing.T) {
	saved := time.Local
	defer func() { time.Local = saved }()
	time.Local = time.FixedZone("test", 9*3600)
	ics := "BEGIN:VCALENDAR\nMETHOD:REQUEST\nBEGIN:VEVENT\nUID:a\nDTSTART;VALUE=DATE:20261112\nEND:VEVENT\nEND:VCALENDAR\n"
	invitation := parseInvitation(ics, "")
	if invitation == nil || !invitation.AllDay || invitation.End.Sub(invitation.Start) != 24*time.Hour {
		t.Fatalf("all-day: %+v", invitation)
	}
	floating := parseInvitation("BEGIN:VCALENDAR\nMETHOD:REQUEST\nBEGIN:VEVENT\nUID:a\nDTSTART:20261112T100000\nEND:VEVENT\nEND:VCALENDAR\n", "")
	if floating == nil || floating.Start.UTC().Hour() != 1 || floating.End.Sub(floating.Start) != time.Hour {
		t.Fatalf("floating: %+v", floating)
	}
}
