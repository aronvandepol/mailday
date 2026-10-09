package tui

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/aronvandepol/mailday/internal/maildir"
)

func TestReaderLinksFromHTMLPutCallsFirstAndDedupe(t *testing.T) {
	content := maildir.Content{HTML: `<p>See <a href="https://example.com/report?id=1">the report</a>, ` +
		`or <a href="https://example.com/report?id=1">again</a>.</p>` +
		`<p>Join <a href="https://teams.microsoft.com/l/meetup-join/19%3ameeting">the meeting</a> ` +
		`or write to <a href="mailto:ada@example.com">Ada</a> ` +
		`or <a href="javascript:alert(1)">click</a>.</p>`}
	links, more := readerLinks(content, 80, false)
	want := []string{"https://teams.microsoft.com/l/meetup-join/19%3ameeting", "https://example.com/report?id=1"}
	if !slices.Equal(links, want) || more != 0 {
		t.Fatalf("links = %v (+%d), want %v", links, more, want)
	}
}

func TestReaderLinksFromPlainTextSkipQuotedHistory(t *testing.T) {
	content := maildir.Content{Body: "Notes are at https://example.org/notes/2026. Call: https://zoom.us/j/12345,\n" +
		"and again https://example.org/notes/2026.\n\n" +
		"On Mon, 5 Oct 2026, Ada wrote:\n> old link https://old.example.net/x\n"}
	links, _ := readerLinks(content, 80, false)
	want := []string{"https://zoom.us/j/12345", "https://example.org/notes/2026"}
	if !slices.Equal(links, want) {
		t.Fatalf("links = %v, want %v", links, want)
	}
	all, _ := readerLinks(content, 80, true)
	if !slices.Contains(all, "https://old.example.net/x") {
		t.Fatalf("with the history shown its links count too: %v", all)
	}
}

func TestReaderLinksAreCappedAtNine(t *testing.T) {
	var body strings.Builder
	for _, name := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k"} {
		body.WriteString("https://example.com/" + name + "\n")
	}
	links, more := readerLinks(maildir.Content{Body: body.String()}, 80, false)
	if len(links) != 9 || more != 2 {
		t.Fatalf("%d links, %d more", len(links), more)
	}
}

func TestLOpensLinksWithTheSystemOpener(t *testing.T) {
	log := fakeOpener(t)
	model, _ := readerModel(t, flowMessage("1", true, time.Minute))
	model = openInReader(t, model, maildir.Content{Body: "Docs: https://example.org/very/long/path/to/some/document.pdf\nCall https://meet.google.com/abc-defg-hij\n"})

	updated, command := model.Update(key("l"))
	if command == nil {
		t.Fatal("l returned no command")
	}
	updated, _ = updated.(Model).Update(command())
	model = updated.(Model)
	if model.flow.picker == nil || len(model.flow.picker.items) != 2 {
		t.Fatalf("picker = %+v", model.flow.picker)
	}
	view := ansi.Strip(model.View().Content)
	if !strings.Contains(view, "1  meet.google.com meeting /abc-defg-hij") || !strings.Contains(view, "2  example.org /very/long/path/to/some/document.pdf") {
		t.Fatalf("link list:\n%s", view)
	}

	updated, command = model.Update(key("2"))
	updated, _ = updated.(Model).Update(command())
	if got := updated.(Model).status; got != "Opened example.org" {
		t.Fatalf("status %q", got)
	}
	if opened := openedTargets(t, log, 1); !slices.Equal(opened, []string{"https://example.org/very/long/path/to/some/document.pdf"}) {
		t.Fatalf("opener got %v", opened)
	}
}

func TestLinkPickerEnterOpensTheHighlightedLink(t *testing.T) {
	log := fakeOpener(t)
	model, _ := readerModel(t, flowMessage("1", true, time.Minute))
	model = openInReader(t, model, maildir.Content{Body: "https://example.org/one\nhttps://example.org/two\n"})
	updated, command := model.Update(key("l"))
	updated, _ = updated.(Model).Update(command())
	updated, _ = updated.(Model).Update(key("j"))
	updated, command = updated.(Model).Update(key("enter"))
	updated, _ = updated.(Model).Update(command())
	if opened := openedTargets(t, log, 1); !slices.Equal(opened, []string{"https://example.org/two"}) {
		t.Fatalf("opener got %v", opened)
	}
}

func TestLWithoutLinksSaysSo(t *testing.T) {
	model, _ := readerModel(t, flowMessage("1", true, time.Minute))
	model = openInReader(t, model, maildir.Content{Body: "Nothing to follow here."})
	updated, command := model.Update(key("l"))
	updated, _ = updated.(Model).Update(command())
	if got := updated.(Model).status; got != "No links in this message" {
		t.Fatalf("status %q", got)
	}
}

func TestOnlyWebLinksReachTheOpener(t *testing.T) {
	for _, bad := range []string{"file:///etc/passwd", "-o=/tmp/x", "https://user@evil.example/", "https://", "https://a.example/\x1b[31m"} {
		if isWebLink(bad) {
			t.Errorf("%q was accepted", bad)
		}
	}
	if message := openLinkCmd(pickerItem{url: "-x", text: "x"})().(openedMsg); message.err == nil {
		t.Error("a link that is not a web address was opened")
	}
}
