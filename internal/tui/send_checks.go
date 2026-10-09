package tui

// Checks on the preview before y starts the send countdown. Anything they
// find is said once and sent only when y is pressed again, because the
// mistakes they catch (no subject, a forgotten attachment, a Cc list that
// grew) cannot be taken back once the mail is out.

import (
	"fmt"
	"net/mail"
	"regexp"
	"strings"

	"github.com/aronvandepol/mailday/internal/compose"
)

// manyRecipients is the most people in To and Cc that go without a warning.
const manyRecipients = 8

// attachmentWord matches the words that promise a file, in English, Dutch,
// German and Korean. Go's \b is ASCII only, so letters are checked by hand
// around the Latin words; the Korean word stands anywhere (it has no spaces
// before its endings).
var attachmentWord = regexp.MustCompile(`(?i)(?:^|[^\p{L}])(?:attach(?:ed|ment|ments|ing)?|bijlagen?|bijgevoegd|angehängt)(?:[^\p{L}]|$)|첨부`)

// writtenBody is what the author wrote: the body without quoted lines and
// without the signature, below which a reply's attribution, a forward's
// original and the quote sit.
func writtenBody(body string) string {
	var kept []string
	for _, line := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		if trimmed := strings.TrimRight(line, " \t"); trimmed == "--" {
			break
		}
		if strings.HasPrefix(strings.TrimLeft(line, " \t"), ">") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// recipientCount counts the addresses in a header line.
func recipientCount(list string) int {
	if strings.TrimSpace(list) == "" {
		return 0
	}
	if parsed, err := mail.ParseAddressList(list); err == nil {
		return len(parsed)
	}
	return strings.Count(list, ",") + 1 // the preview of a list that will not send
}

// sendWarnings lists what looks wrong with a draft that can be sent.
func sendWarnings(draft compose.Draft) []string {
	var warnings []string
	if strings.TrimSpace(draft.Subject) == "" {
		warnings = append(warnings, "No subject")
	}
	if len(draft.Attach) == 0 && len(draft.Forwarded) == 0 && attachmentWord.MatchString(writtenBody(draft.Body)) {
		warnings = append(warnings, "No attachment, but you mention one")
	}
	if people := recipientCount(draft.To) + recipientCount(draft.Cc); people > manyRecipients {
		warnings = append(warnings, fmt.Sprintf("Sending to %d people", people))
	}
	return warnings
}

// warningKey identifies a warning for this draft as it stands, so the second
// y counts only when nothing was edited in between.
func warningKey(draft compose.Draft, warnings []string) string {
	return strings.Join(warnings, "|") + "\x00" + draft.To + "\x00" + draft.Cc + "\x00" + draft.Subject + "\x00" + draft.Body +
		fmt.Sprintf("\x00%d/%d", len(draft.Attach), len(draft.Forwarded))
}

// recipientCounts is the preview's "to 2 · cc 31" line.
func recipientCounts(draft compose.Draft) string {
	var parts []string
	for _, field := range []struct {
		name string
		list string
	}{{"to", draft.To}, {"cc", draft.Cc}, {"bcc", draft.Bcc}} {
		if count := recipientCount(field.list); count > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", field.name, count))
		}
	}
	return strings.Join(parts, " · ")
}
