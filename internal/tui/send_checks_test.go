package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/aronvandepol/mailday/internal/compose"
	"github.com/aronvandepol/mailday/internal/maildir"
)

func checkedDraft() compose.Draft {
	return compose.Draft{From: "Sam de Vries <s.de.vries@hum.uni.example>", To: "ada@uni.nl", Subject: "Budget", Body: "Numbers below.\n\n-- \nSam"}
}

func TestSendWarnings(t *testing.T) {
	people := func(count int) string {
		var list []string
		for index := 0; index < count; index++ {
			list = append(list, fmt.Sprintf("p%d@example.org", index))
		}
		return strings.Join(list, ", ")
	}
	for _, test := range []struct {
		name   string
		change func(*compose.Draft)
		want   []string
	}{
		{"fine", func(*compose.Draft) {}, nil},
		{"empty subject", func(d *compose.Draft) { d.Subject = "  " }, []string{"No subject"}},
		{"attached", func(d *compose.Draft) { d.Body = "Please see the attached file." }, []string{"No attachment, but you mention one"}},
		{"Attachment capital", func(d *compose.Draft) { d.Body = "Attachment: none yet" }, []string{"No attachment, but you mention one"}},
		{"bijlage", func(d *compose.Draft) { d.Body = "Zie de bijlage." }, []string{"No attachment, but you mention one"}},
		{"bijgevoegd", func(d *compose.Draft) { d.Body = "Bijgevoegd vind je het plan." }, []string{"No attachment, but you mention one"}},
		{"angehängt", func(d *compose.Draft) { d.Body = "Der Bericht ist angehängt." }, []string{"No attachment, but you mention one"}},
		{"Korean", func(d *compose.Draft) { d.Body = "첨부파일을 확인해 주세요." }, []string{"No attachment, but you mention one"}},
		{"attach in a longer word", func(d *compose.Draft) { d.Body = "The detachment moved on; Attachmentsmith agrees." }, nil},
		{"mention in a quote", func(d *compose.Draft) { d.Body = "Thanks!\n\n> see the attached file\n> bijlage" }, nil},
		{"mention in the signature", func(d *compose.Draft) { d.Body = "Thanks!\n\n-- \nSam\nSee the attached CV on my site" }, nil},
		{"mention in a forward's original", func(d *compose.Draft) {
			d.Body = "FYI\n\n-- \nSam\n---------- Forwarded message ---------\nPlease find the attachment."
		}, nil},
		{"file attached", func(d *compose.Draft) { d.Body = "See attached."; d.Attach = []string{"~/x.pdf"} }, nil},
		{"file forwarded", func(d *compose.Draft) {
			d.Body = "See attached."
			d.Forwarded = []maildir.AttachmentData{{Name: "x.pdf"}}
		}, nil},
		{"eight is fine", func(d *compose.Draft) { d.To, d.Cc = people(5), people(3) }, nil},
		{"nine people", func(d *compose.Draft) { d.To, d.Cc = people(2), people(7) }, []string{"Sending to 9 people"}},
		{"bcc does not count", func(d *compose.Draft) { d.Bcc = people(20) }, nil},
		{"named addresses count once each", func(d *compose.Draft) {
			d.To = `"Pol, A.M. van de" <a@x.org>, "Bakker, R.E." <b@x.org>`
		}, nil},
		{"all three", func(d *compose.Draft) { d.Subject = ""; d.Body = "attached"; d.To = people(12) }, []string{"No subject", "No attachment, but you mention one", "Sending to 12 people"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			draft := checkedDraft()
			test.change(&draft)
			if got := sendWarnings(draft); strings.Join(got, "|") != strings.Join(test.want, "|") {
				t.Fatalf("warnings = %q, want %q", got, test.want)
			}
		})
	}
}

func previewModel(t *testing.T, draft compose.Draft) Model {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MAILDAY_SEND_DELAY", "10")
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
	model.screen = screenCompose
	model.draft = &draft
	return model
}

func TestYNeedsASecondPressAfterAWarning(t *testing.T) {
	draft := checkedDraft()
	draft.Body = "Please see the attached report."
	model := previewModel(t, draft)

	updated, command := model.Update(key("y"))
	model = updated.(Model)
	if model.sendCountdown != 0 || command != nil || model.status != "No attachment, but you mention one · y again sends anyway" {
		t.Fatalf("first y: countdown %d, status %q", model.sendCountdown, model.status)
	}
	updated, command = model.Update(key("y"))
	model = updated.(Model)
	if model.sendCountdown != 10 || command == nil || !strings.HasPrefix(model.status, "Sending in 10 s") {
		t.Fatalf("second y should start the countdown: %d %q", model.sendCountdown, model.status)
	}
}

func TestSeveralWarningsShareOneLine(t *testing.T) {
	draft := checkedDraft()
	draft.Subject = ""
	draft.Body = "bijlage"
	model := previewModel(t, draft)
	updated, _ := model.Update(key("y"))
	if got, want := updated.(Model).status, "No subject · No attachment, but you mention one · y again sends anyway"; got != want {
		t.Fatalf("status %q, want %q", got, want)
	}
}

func TestEditingAfterAWarningAsksAgain(t *testing.T) {
	draft := checkedDraft()
	draft.Subject = ""
	model := previewModel(t, draft)
	updated, _ := model.Update(key("y"))
	model = updated.(Model)

	// Back to writing and straight to the preview again: the warning counts afresh.
	updated, _ = model.Update(key("e"))
	model = updated.(Model)
	if model.sendWarned != "" {
		t.Fatal("going back to writing should forget the warning")
	}
	// Fixing the subject removes the warning, so one y goes.
	fixed := checkedDraft()
	model = previewModel(t, fixed)
	updated, command := model.Update(key("y"))
	if updated.(Model).sendCountdown != 10 || command == nil {
		t.Fatal("a clean draft should count down on the first y")
	}
}

func TestAChangedDraftDoesNotInheritTheEarlierConfirmation(t *testing.T) {
	draft := checkedDraft()
	draft.Subject = ""
	model := previewModel(t, draft)
	updated, _ := model.Update(key("y"))
	model = updated.(Model)
	changed := *model.draft
	changed.Body += "\nOne more thing."
	model.draft = &changed
	updated, _ = model.Update(key("y"))
	model = updated.(Model)
	if model.sendCountdown != 0 || !strings.Contains(model.status, "y again sends anyway") {
		t.Fatalf("an edited draft slipped through: countdown %d, %q", model.sendCountdown, model.status)
	}
}

func TestSendDelayZeroStillAsksOnWarnings(t *testing.T) {
	draft := checkedDraft()
	draft.Subject = ""
	model := previewModel(t, draft)
	t.Setenv("MAILDAY_SEND_DELAY", "0")
	updated, command := model.Update(key("y"))
	if command != nil || updated.(Model).sending {
		t.Fatal("a warning should stop the immediate send too")
	}
}

func TestPreviewShowsCountsAndWrapsLongRecipientLines(t *testing.T) {
	var cc []string
	for index := 0; index < 31; index++ {
		cc = append(cc, fmt.Sprintf("colleague%02d@department.example.org", index))
	}
	draft := checkedDraft()
	draft.To = "ada@uni.nl, bob@uni.nl"
	draft.Cc = strings.Join(cc, ", ")
	model := previewModel(t, draft)
	model.width, model.height = 100, 60
	view := ansi.Strip(model.View().Content)
	for _, want := range []string{"to 2 · cc 31", "colleague00@department.example.org", "colleague30@department.example.org", "Check: Sending to 33 people"} {
		if !strings.Contains(view, want) {
			t.Errorf("preview lacks %q:\n%s", want, view)
		}
	}
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "...") {
			t.Errorf("a recipient line was truncated: %q", line)
		}
	}
}
