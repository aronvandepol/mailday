package maildir

// Read receipts (RFC 8098 message disposition notifications). A receipt is a
// multipart/report message whose report-type is disposition-notification; its
// machine-readable part names the message it answers and what became of it.
// Only those messages are opened beyond their headers, and they are tiny.

import (
	"bufio"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"net/textproto"
	"strings"
	"time"

	"github.com/aronvandepol/mailday/internal/terminal"
)

// maxReceiptPartBytes caps the disposition-notification part, which is a
// handful of header lines; anything bigger is not one.
const maxReceiptPartBytes = 64 << 10

// Receipt is what a recipient's mail program reported about one message.
type Receipt struct {
	OriginalMessageID string // as the original carried it, brackets included
	Recipient         string // Final-Recipient address
	FromName          string // name on the receipt's From, which Outlook and Thunderbird fill in
	Disposition       string // displayed, deleted, denied, failed, dispatched or processed
	Date              time.Time
}

// Displayed reports that the recipient opened the message.
func (r Receipt) Displayed() bool { return r.Disposition == "displayed" }

// Deleted reports that the recipient deleted the message without reading it.
func (r Receipt) Deleted() bool { return r.Disposition == "deleted" }

// ReceiptKey makes Message-IDs comparable: no brackets, no case.
func ReceiptKey(messageID string) string {
	return strings.ToLower(strings.Trim(strings.TrimSpace(messageID), "<>"))
}

// isReceiptReport is the cheap test on the headers already read.
func isReceiptReport(header mail.Header) bool {
	mediaType, params, err := mime.ParseMediaType(header.Get("Content-Type"))
	return err == nil && strings.EqualFold(mediaType, "multipart/report") &&
		strings.EqualFold(params["report-type"], "disposition-notification")
}

// parseReceipt reads the disposition-notification part of a receipt message.
// A report whose part cannot be read still returns a Receipt, so the message
// stays out of the inbox; it just names no original.
func parseReceipt(header mail.Header, body io.Reader, date time.Time) *Receipt {
	receipt := &Receipt{Date: date}
	name, address := addressLabel(header.Get("From"))
	if name != address {
		receipt.FromName = terminal.SanitizeLine(name)
	}
	_, params, _ := mime.ParseMediaType(header.Get("Content-Type"))
	if params["boundary"] == "" {
		return receipt
	}
	parts := multipart.NewReader(body, params["boundary"])
	for {
		part, err := parts.NextPart()
		if err != nil {
			return receipt
		}
		mediaType, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if !strings.EqualFold(mediaType, "message/disposition-notification") {
			continue
		}
		reader := transferReader(part.Header.Get("Content-Transfer-Encoding"), io.LimitReader(part, maxReceiptPartBytes))
		fields, _ := textproto.NewReader(bufio.NewReader(reader)).ReadMIMEHeader()
		receipt.OriginalMessageID = terminal.SanitizeLine(strings.TrimSpace(fields.Get("Original-Message-Id")))
		receipt.Recipient = terminal.SanitizeLine(receiptAddress(fields.Get("Final-Recipient")))
		if receipt.Recipient == "" {
			receipt.Recipient = terminal.SanitizeLine(receiptAddress(fields.Get("Original-Recipient")))
		}
		receipt.Disposition = dispositionType(fields.Get("Disposition"))
		return receipt
	}
}

// receiptAddress drops the address type ("rfc822; ada@example.org").
func receiptAddress(field string) string {
	if _, address, ok := strings.Cut(field, ";"); ok {
		field = address
	}
	return strings.TrimSpace(field)
}

// dispositionType is the last word of "manual-action/MDN-sent-manually;
// displayed/error": the type, without its modifiers.
func dispositionType(field string) string {
	if index := strings.LastIndex(field, ";"); index >= 0 {
		field = field[index+1:]
	}
	kind, _, _ := strings.Cut(field, "/")
	return strings.ToLower(strings.TrimSpace(kind))
}
