package compose

// Read receipts (RFC 8098). Asking is a header on the outgoing message, set
// per draft. Nothing asks by default, and MAILDAY_READ_RECEIPTS=on is a
// hidden opt-in. BuildReceipt, which answers a request by hand, is no longer
// reachable from a key; it stays with its tests for the day one is wanted.

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"os"
	"strings"
	"time"

	"github.com/aronvandepol/mailday/internal/maildir"
)

// ReadReceiptsDefault is whether new drafts ask for a read receipt:
// MAILDAY_READ_RECEIPTS=on, a hidden opt-in. Off otherwise.
func ReadReceiptsDefault() bool {
	return isOn(os.Getenv("MAILDAY_READ_RECEIPTS"))
}

func isOn(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "on", "yes", "true", "1":
		return true
	}
	return false
}

// newMessageID makes a Message-ID in the sender's own domain.
func newMessageID(now time.Time, domain string) string {
	random := make([]byte, 8)
	_, _ = rand.Read(random)
	return fmt.Sprintf("<%d.%s@%s>", now.UnixNano(), hex.EncodeToString(random), domain)
}

// BuildReceipt makes the read receipt that answers content, a message read in
// account. It goes to the addresses that asked (Disposition-Notification-To)
// from the address the message was sent to, and is returned with the identity
// that must send it. displayed is when the message was opened.
func BuildReceipt(account string, content maildir.Content, displayed time.Time) ([]byte, Identity, error) {
	if len(content.ReceiptTo) == 0 {
		return nil, Identity{}, errors.New("this message did not ask for a read receipt")
	}
	identity := ForAccount(account)
	recipient := ""
	for _, address := range append(append([]maildir.Address{}, content.ToList...), content.CCList...) {
		if found, ok := forAddress(address.Addr); ok {
			identity, recipient = found, address.Addr
			break
		}
	}
	var to []string
	for _, address := range content.ReceiptTo {
		to = append(to, (&mail.Address{Name: address.Name, Address: address.Addr}).String())
	}
	subject := strings.TrimSpace(content.Subject)
	if subject == "" {
		subject = "(no subject)"
	}

	var b bytes.Buffer
	header := func(key, value string) { fmt.Fprintf(&b, "%s: %s\r\n", key, value) }
	header("From", identity.header())
	header("To", strings.Join(to, ", "))
	header("Subject", mime.QEncoding.Encode("utf-8", "Read: "+subject))
	header("Date", displayed.Format(time.RFC1123Z))
	header("Message-ID", newMessageID(displayed, identity.Address[strings.LastIndex(identity.Address, "@")+1:]))
	header("MIME-Version", "1.0")
	header("User-Agent", "mailday")

	var report bytes.Buffer
	parts := multipart.NewWriter(&report)
	header("Content-Type", "multipart/report; report-type=disposition-notification; boundary="+parts.Boundary())
	b.WriteString("\r\n")

	human, err := parts.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {"text/plain; charset=utf-8"},
		"Content-Transfer-Encoding": {"quoted-printable"},
	})
	if err != nil {
		return nil, Identity{}, err
	}
	encoder := quotedprintable.NewWriter(human)
	if _, err := encoder.Write([]byte(strings.ReplaceAll(receiptText(identity, content, displayed), "\n", "\r\n"))); err != nil {
		return nil, Identity{}, err
	}
	if err := encoder.Close(); err != nil {
		return nil, Identity{}, err
	}

	machine, err := parts.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {"message/disposition-notification"},
		"Content-Transfer-Encoding": {"7bit"},
	})
	if err != nil {
		return nil, Identity{}, err
	}
	fields := []string{"Reporting-UA: Mailday"}
	if recipient != "" {
		fields = append(fields, "Original-Recipient: rfc822;"+recipient)
	}
	fields = append(fields, "Final-Recipient: rfc822;"+identity.Address)
	if content.MessageID != "" {
		fields = append(fields, "Original-Message-ID: "+content.MessageID)
	}
	fields = append(fields, "Disposition: manual-action/MDN-sent-manually; displayed")
	if _, err := machine.Write([]byte(strings.Join(fields, "\r\n") + "\r\n")); err != nil {
		return nil, Identity{}, err
	}
	if err := parts.Close(); err != nil {
		return nil, Identity{}, err
	}
	b.Write(report.Bytes())
	return b.Bytes(), identity, nil
}

// receiptText is the part a person reads, in the shape Outlook gives it.
func receiptText(identity Identity, content maildir.Content, displayed time.Time) string {
	format := "Mon 2 Jan 2006 at 15:04 MST"
	var b strings.Builder
	b.WriteString("Your message\n\n")
	b.WriteString("  To:      " + identity.header() + "\n")
	b.WriteString("  Subject: " + strings.TrimSpace(content.Subject) + "\n")
	if !content.Date.IsZero() {
		b.WriteString("  Sent:    " + content.Date.Local().Format(format) + "\n")
	}
	b.WriteString("\nwas displayed on " + displayed.Local().Format(format) + ".\n\n")
	b.WriteString("This only says it was shown on the screen. It does not mean it was read or understood.\n")
	return b.String()
}
