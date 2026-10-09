package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/aronvandepol/mailday/internal/maildir"
)

func TestPersonNameRewritesDirectoryNames(t *testing.T) {
	for raw, want := range map[string]string{
		"Hendriks, J.J. (Jolien)": "Jolien Hendriks",
		"Vries, S. de (Sam)":      "Sam de Vries",
		`"Stijn Dekker"`:          "Stijn Dekker",
		"":                        "x@example.org",
	} {
		if got := personName(raw, "x@example.org"); got != want {
			t.Errorf("personName(%q) = %q, want %q", raw, got, want)
		}
	}
	got := recipientNames([]maildir.Address{{Name: "Bakker, R.E. (Ruben)", Addr: "r@x"}, {Addr: "s.de.vries@hum.uni.example"}})
	if got != "Ruben Bakker, you" {
		t.Errorf("recipientNames = %q", got)
	}
}

func plainBody(t *testing.T, body string, showHistory bool) string {
	t.Helper()
	lines := readerBody(maildir.Content{Body: body}, 100, showHistory)
	for index := range lines {
		lines[index] = strings.TrimSpace(ansi.Strip(lines[index]))
	}
	return strings.Join(lines, "\n")
}

func TestTopPostedQuoteCollapsesAndExpands(t *testing.T) {
	body := "Works for me.\n\nSam\n\nOn 17 Jan 2025, at 06:11, Yann Ryan <y@x>\nwrote:\n\n> Hi Sam,\n> Would 30 April work?\n"
	collapsed := plainBody(t, body, false)
	if strings.Contains(collapsed, "Would 30 April") || !strings.Contains(collapsed, "lines of earlier messages hidden") {
		t.Fatalf("collapsed body:\n%s", collapsed)
	}
	if strings.Contains(collapsed, "wrote:") {
		t.Fatalf("attribution line should be hidden with the quote:\n%s", collapsed)
	}
	expanded := plainBody(t, body, true)
	if !strings.Contains(expanded, "│ Would 30 April work?") {
		t.Fatalf("expanded body:\n%s", expanded)
	}
}

func TestInlineRepliesKeepTheirQuotes(t *testing.T) {
	body := "> Can you chair the session?\nYes, happy to.\n> And the travel grant?\nI applied last week.\n"
	got := plainBody(t, body, false)
	if !strings.Contains(got, "│ Can you chair the session?") || strings.Contains(got, "hidden") {
		t.Fatalf("inline reply lost its quotes:\n%s", got)
	}
}

func TestOutlookHistoryInHTMLCollapses(t *testing.T) {
	html := `<p>Hai Jolien,</p><ol><li>oke</li><li>gedaan</li></ol><hr><p>Van: Hendriks, J.J. (Jolien)<br>Verzonden: donderdag 1 oktober 2026 09:13<br>Onderwerp: Laatste zaken</p><p>Goedemorgen!</p>`
	lines := readerBody(maildir.Content{HTML: html, Body: "fallback"}, 100, false)
	text := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(text, "1. oke") || strings.Contains(text, "Goedemorgen") || !strings.Contains(text, "hidden") {
		t.Fatalf("rendered:\n%s", text)
	}
}

func TestWrappedListItemsHangUnderTheirText(t *testing.T) {
	long := strings.Repeat("word ", 40)
	lines := readerBody(maildir.Content{HTML: "<ol><li>" + long + "</li></ol>", Body: "x"}, 60, false)
	var item []string
	for _, line := range lines {
		if trimmed := strings.TrimRight(ansi.Strip(line), " "); trimmed != "" {
			item = append(item, trimmed)
		}
	}
	if len(item) < 2 || !strings.Contains(item[0], "1. word") {
		t.Fatalf("lines = %q", item)
	}
	first := strings.Index(item[0], "word")
	if indent := len(item[1]) - len(strings.TrimLeft(item[1], " ")); indent != first {
		t.Fatalf("continuation indent %d, want %d:\n%s", indent, first, strings.Join(item, "\n"))
	}
}

func TestItalianAttributionCollapsesAndMutedLinksStayClickable(t *testing.T) {
	html := `<p>Dear Sam,</p><p>Perfect!</p><p>---------------------------------</p><p>Marco Rinaldi, PhD<br>http://marcorinaldi.site.example.org/</p><p>Il giorno mer 16 set 2026 alle ore 16:04 Sam ha scritto:</p><blockquote><p>Dear Marco,</p></blockquote>`
	lines := readerBody(maildir.Content{HTML: html, Body: "x"}, 100, false)
	raw := strings.Join(lines, "\n")
	text := ansi.Strip(raw)
	if strings.Contains(text, "Dear Marco") || !strings.Contains(text, "hidden") {
		t.Fatalf("Italian history not collapsed:\n%s", text)
	}
	if !strings.Contains(raw, "\x1b]8;;http://marcorinaldi.site.example.org/") {
		t.Fatalf("signature URL lost its hyperlink:\n%q", raw)
	}
}
