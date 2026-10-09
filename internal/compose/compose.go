// Package compose writes mail as Markdown and sends it as plain text plus HTML.
//
// A draft is a Markdown file with a front-matter block for the headers, edited
// in the user's editor. The Markdown itself becomes the text/plain part, so a
// recipient without HTML still reads tidy text, and htmlutil.FromMarkdown
// (goldmark, ported from hey-cli) renders the text/html part. msmtp sends it
// through the account that owns the From address.
package compose

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/mail"
	"net/textproto"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/aronvandepol/mailday/internal/config"
	"github.com/aronvandepol/mailday/internal/contacts"
	"github.com/aronvandepol/mailday/internal/htmlutil"
	"github.com/aronvandepol/mailday/internal/maildir"
)

// Identity is one sending address and the mailday account labels it
// belongs to (Linux and mutt-wizard name the same account differently).
type Identity struct {
	Labels  []string
	Name    string
	Address string
	Aliases []string // other addresses msmtp may list as the account's from
	// Signature goes under new mail and forwards, ShortSignature under replies.
	Signature, ShortSignature []string
	// SaveSent files a local Sent copy. Gmail and Exchange Online keep SMTP
	// submissions in Sent themselves, so a copy there would be a duplicate.
	SaveSent bool
}

// Identities are the [[identity]] entries of config.toml, or, without any,
// one per msmtp account with a from line.
var Identities = loadIdentities()

// Reload reads the identities again from the loaded config.
func Reload() { Identities = loadIdentities() }

func loadIdentities() []Identity {
	cfg := config.Get()
	var out []Identity
	for _, c := range cfg.Identity {
		out = append(out, Identity{Labels: c.Accounts, Name: c.Name, Address: c.Address, Aliases: c.Aliases,
			Signature: c.Signature, ShortSignature: c.ShortSignature, SaveSent: c.SaveSent})
	}
	if len(out) == 0 {
		for _, account := range msmtpAccounts() {
			out = append(out, Identity{Labels: []string{account.name, domainOf(account.from)}, Name: cfg.Name,
				Address: account.from, SaveSent: !keepsOwnSent(account.host)})
		}
	}
	return out
}

func domainOf(address string) string {
	return strings.ToLower(address[strings.LastIndex(address, "@")+1:])
}

// keepsOwnSent reports whether an SMTP host files what it sends in Sent.
func keepsOwnSent(host string) bool {
	host = strings.ToLower(host)
	for _, suffix := range []string{"gmail.com", "googlemail.com", "office365.com", "outlook.com"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

// signatureBlock is the signature as it sits in a draft: the standard "-- "
// delimiter, then the lines. Replies use the short form (just the first name
// where no short form is set).
func (identity Identity) SignatureBlock(short bool) string {
	lines := identity.Signature
	if short {
		lines = identity.ShortSignature
		if len(lines) == 0 {
			lines = []string{strings.Fields(identity.Name)[0]}
		}
	}
	if len(lines) == 0 {
		return ""
	}
	return "-- \n" + strings.Join(lines, "\n") + "\n"
}

// IsOwn reports whether address belongs to one of the identities.
func IsOwn(address string) bool {
	address = strings.ToLower(strings.TrimSpace(address))
	for _, identity := range Identities {
		if strings.EqualFold(identity.Address, address) || containsFold(identity.Aliases, address) {
			return true
		}
	}
	return false
}

func containsFold(list []string, value string) bool {
	for _, item := range list {
		if strings.EqualFold(item, value) {
			return true
		}
	}
	return false
}

// ForAccount returns the identity for a mailday account label, or the first
// one (the zero Identity when there are none).
func ForAccount(label string) Identity {
	for _, identity := range Identities {
		if containsFold(identity.Labels, label) {
			return identity
		}
	}
	if len(Identities) == 0 {
		return Identity{}
	}
	return Identities[0]
}

func forAddress(address string) (Identity, bool) {
	for _, identity := range Identities {
		if strings.EqualFold(identity.Address, address) || containsFold(identity.Aliases, address) {
			return identity, true
		}
	}
	return Identity{}, false
}

func (identity Identity) header() string {
	return (&mail.Address{Name: identity.Name, Address: identity.Address}).String()
}

// Draft is one message being written.
type Draft struct {
	From, To, Cc, Bcc, Subject string
	Body                       string // Markdown
	InReplyTo                  string
	References                 string
	AnswersPath                string                   // Maildir file to flag as answered once sent
	Attach                     []string                 // files to attach, as typed (~ allowed)
	Forwarded                  []maildir.AttachmentData // the original's attachments when forwarding
	ForwardOf                  string                   // Maildir file being forwarded, to reload Forwarded from a saved draft
	ReadReceipt                bool                     // ask the recipients for a read receipt (receipt.go)
}

// maxAttachBytes stays under Gmail's 25 MB, which counts the base64 growth.
const maxAttachBytes = 18 << 20

func expandHome(path string) string {
	path = strings.TrimSpace(path)
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	return path
}

// SplitPaths reads the comma-separated attach field.
func SplitPaths(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// AttachmentSize is the total size of the files and forwarded attachments.
func (d Draft) AttachmentSize() (int64, error) {
	var total int64
	for _, path := range d.Attach {
		info, err := os.Stat(expandHome(path))
		if err != nil {
			return total, fmt.Errorf("attach: %v", err)
		}
		if !info.Mode().IsRegular() {
			return total, fmt.Errorf("attach: %s is not a file", path)
		}
		total += info.Size()
	}
	for _, attachment := range d.Forwarded {
		total += int64(len(attachment.Data))
	}
	return total, nil
}

// New starts an empty message from the account's identity.
func New(account string) Draft {
	identity := ForAccount(account)
	return Draft{From: identity.header(), Body: "\n\n" + identity.SignatureBlock(false), ReadReceipt: ReadReceiptsDefault()}
}

var replyPrefix = regexp.MustCompile(`(?i)^\s*(re|aw|antw|sv)\s*:\s*`)
var forwardPrefix = regexp.MustCompile(`(?i)^\s*(fwd?|doorst|wg)\s*:\s*`)

func addressList(list []maildir.Address, skip map[string]bool) string {
	var out []string
	for _, address := range list {
		key := strings.ToLower(address.Addr)
		if address.Addr == "" || skip[key] || IsOwn(address.Addr) {
			continue
		}
		skip[key] = true
		out = append(out, contacts.FormatAddress(address.Name, address.Addr))
	}
	return strings.Join(out, ", ")
}

func attributionLine(content maildir.Content) string {
	name := content.FromName
	if name == "" {
		name = content.FromAddr
	}
	return fmt.Sprintf("On %s, %s wrote:", content.Date.Local().Format("Mon 2 Jan 2006 at 15:04"), name)
}

func quote(text string) string {
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimRight(strings.ReplaceAll(text, "\r\n", "\n"), "\n"), "\n") {
		if strings.HasPrefix(line, ">") {
			b.WriteString(">" + line + "\n")
		} else if line == "" {
			b.WriteString(">\n")
		} else {
			b.WriteString("> " + line + "\n")
		}
	}
	return b.String()
}

// Reply answers content, which was read from the account's message at path.
// Cc keeps the people the sender copied, and with all set it also takes the
// other To recipients, minus your own addresses in both cases.
func Reply(account, path string, content maildir.Content, all bool) Draft {
	identity := ForAccount(account)
	for _, address := range append(append([]maildir.Address{}, content.ToList...), content.CCList...) {
		if found, ok := forAddress(address.Addr); ok {
			identity = found // answer from the address the mail was sent to
			break
		}
	}
	seen := map[string]bool{}
	recipients := content.ReplyTo
	if len(recipients) == 0 {
		recipients = []maildir.Address{{Name: content.FromName, Addr: content.FromAddr}}
	}
	to := addressList(recipients, seen)
	if to == "" { // replying to your own message: write to its recipients
		to = addressList(content.ToList, seen)
	}
	if to == "" { // a note to yourself: answer it to yourself
		to = contacts.FormatAddress(content.FromName, content.FromAddr)
	}
	draft := Draft{
		From:        identity.header(),
		To:          to,
		Subject:     "Re: " + replyPrefix.ReplaceAllString(content.Subject, ""),
		Body:        "\n\n" + identity.SignatureBlock(true) + "\n" + attributionLine(content) + "\n" + quote(content.Body),
		InReplyTo:   content.MessageID,
		References:  strings.TrimSpace(content.References + " " + content.MessageID),
		AnswersPath: path,
		ReadReceipt: ReadReceiptsDefault(),
	}
	// People the sender copied stay copied on a plain reply too. Reply to
	// all also brings in the other To recipients.
	cc := content.CCList
	if all {
		cc = append(append([]maildir.Address{}, content.ToList...), content.CCList...)
	}
	draft.Cc = addressList(cc, seen)
	return draft
}

// Forward passes content on with its headers. Attachments are not forwarded.
func Forward(account string, content maildir.Content) Draft {
	header := fmt.Sprintf("---------- Forwarded message ----------\nFrom: %s <%s>\nDate: %s\nSubject: %s\n",
		content.FromName, content.FromAddr, content.Date.Local().Format("Mon 2 Jan 2006 15:04"), content.Subject)
	identity := ForAccount(account)
	return Draft{
		From:        identity.header(),
		Subject:     "Fwd: " + forwardPrefix.ReplaceAllString(content.Subject, ""),
		Body:        "\n\n" + identity.SignatureBlock(false) + "\n" + header + "\n" + strings.TrimSpace(content.Body) + "\n",
		ReadReceipt: ReadReceiptsDefault(),
	}
}

// File renders the draft as the Markdown file the editor opens.
func (d Draft) File() string {
	var b strings.Builder
	b.WriteString("---\n")
	for _, field := range [][2]string{{"from", d.From}, {"to", d.To}, {"cc", d.Cc}, {"bcc", d.Bcc}, {"subject", d.Subject}, {"attach", strings.Join(d.Attach, ", ")}} {
		b.WriteString(field[0] + ": " + field[1] + "\n")
	}
	// Threading travels with a saved draft, so a reply sent later still
	// threads and still flags the original answered.
	for _, field := range [][2]string{{"in-reply-to", d.InReplyTo}, {"references", d.References}, {"answers", d.AnswersPath}, {"forward-of", d.ForwardOf}} {
		if field[1] != "" {
			b.WriteString(field[0] + ": " + field[1] + "\n")
		}
	}
	if d.ReadReceipt {
		b.WriteString("read-receipt: yes\n") // the choice travels with a saved draft; Build turns it into the header
	}
	b.WriteString("---\n")
	b.WriteString(d.Body)
	return b.String()
}

// Parse reads an edited draft file back. Threading fields come from base.
func Parse(text string, base Draft) (Draft, error) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return base, errors.New("the draft lost its --- header block")
	}
	end := strings.Index(text[4:], "\n---\n")
	if end < 0 {
		return base, errors.New("the draft's header block has no closing ---")
	}
	draft := base
	draft.From, draft.To, draft.Cc, draft.Bcc, draft.Subject, draft.Attach = "", "", "", "", "", nil
	draft.ReadReceipt = false // the file is the truth: a deleted line means no request
	scanner := bufio.NewScanner(strings.NewReader(text[4 : 4+end]))
	for scanner.Scan() {
		key, value, ok := strings.Cut(scanner.Text(), ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "from":
			draft.From = value
		case "to":
			draft.To = value
		case "cc":
			draft.Cc = value
		case "bcc":
			draft.Bcc = value
		case "subject":
			draft.Subject = value
		case "attach":
			draft.Attach = SplitPaths(value)
		case "in-reply-to":
			draft.InReplyTo = value
		case "references":
			draft.References = value
		case "answers":
			draft.AnswersPath = value
		case "forward-of":
			draft.ForwardOf = value
		case "read-receipt":
			draft.ReadReceipt = isOn(value)
		}
	}
	draft.Body = strings.TrimLeft(text[4+end+5:], "\n")
	return draft, nil
}

// Check validates the draft and returns the sending identity.
func (d Draft) Check() (Identity, error) {
	from, err := mail.ParseAddress(d.From)
	if err != nil {
		return Identity{}, fmt.Errorf("from: %v", err)
	}
	identity, ok := forAddress(from.Address)
	if !ok {
		return Identity{}, fmt.Errorf("from: %s is not one of your accounts", from.Address)
	}
	if strings.TrimSpace(d.To+d.Cc+d.Bcc) == "" {
		return Identity{}, errors.New("no recipients")
	}
	for name, list := range map[string]string{"to": d.To, "cc": d.Cc, "bcc": d.Bcc} {
		if strings.TrimSpace(list) == "" {
			continue
		}
		if _, err := mail.ParseAddressList(list); err != nil {
			return Identity{}, fmt.Errorf("%s: %v", name, err)
		}
	}
	if strings.TrimSpace(d.Body) == "" {
		return Identity{}, errors.New("the message is empty")
	}
	if size, err := d.AttachmentSize(); err != nil {
		return Identity{}, err
	} else if size > maxAttachBytes {
		return Identity{}, fmt.Errorf("attachments total %d MB, over the %d MB that mail servers accept", size>>20, maxAttachBytes>>20)
	}
	if from.Name == "" {
		from.Name = identity.Name
	}
	identity.Name = from.Name
	return identity, nil
}

// HTML is the text/html part: the Markdown rendered with the same CSS as the
// NeoMutt Markdown macro, so mail looks alike whichever tool sent it.
func (d Draft) HTML() string { return d.htmlWith("") }

// htmlWith is HTML with extra as the last thing in the body.
func (d Draft) htmlWith(extra string) string {
	if extra != "" {
		extra = "\n" + extra
	}
	return `<!DOCTYPE html>
<html><head><meta charset="utf-8"><style>
body { font-family: -apple-system, sans-serif; font-size: 14px; line-height: 1.6; color: #333; max-width: 600px; }
code { background: #f5f5f5; padding: 2px 4px; border-radius: 3px; font-size: 13px; }
pre { background: #f5f5f5; padding: 12px; border-radius: 6px; overflow-x: auto; }
blockquote { border-left: 3px solid #ccc; margin-left: 0; padding-left: 12px; color: #666; }
a { color: #0366d6; }
</style></head><body>
` + bodyHTML(d.Body) + extra + "\n</body></html>\n"
}

// bodyHTML renders the Markdown, with the signature (from a "-- " line to the
// next blank line) set apart: a short rule, the name in weight, the rest small
// and grey, web addresses linked. Inline styles, because Gmail drops <style>.
func bodyHTML(body string) string {
	lines := strings.Split(body, "\n")
	start := -1
	for index, line := range lines {
		if line == "-- " || line == "--" {
			start = index
			break
		}
	}
	if start < 0 {
		return htmlutil.FromMarkdown(body)
	}
	end := start + 1
	for end < len(lines) && strings.TrimSpace(lines[end]) != "" {
		end++
	}
	var signature strings.Builder
	signature.WriteString(`<div style="margin-top:22px;font-size:13px;line-height:1.5">` +
		`<div style="width:32px;border-top:1px solid #ccc;margin-bottom:8px"></div>`)
	for index, line := range lines[start+1 : end] {
		text := html.EscapeString(strings.TrimSpace(line))
		text = webAddress.ReplaceAllString(text, `<a href="https://$1" style="color:#0366d6;text-decoration:none">$1</a>`)
		if index == 0 {
			signature.WriteString(`<div style="color:#222;font-weight:600">` + text + `</div>`)
		} else {
			signature.WriteString(`<div style="color:#777">` + text + `</div>`)
		}
	}
	signature.WriteString(`</div>`)
	before := strings.Join(lines[:start], "\n")
	after := strings.Join(lines[end:], "\n")
	out := ""
	if strings.TrimSpace(before) != "" {
		out = htmlutil.FromMarkdown(before)
	}
	out += "\n" + signature.String()
	if strings.TrimSpace(after) != "" {
		out += "\n" + htmlutil.FromMarkdown(after)
	}
	return out
}

var webAddress = regexp.MustCompile(`\b((?:[a-z0-9-]+\.)+(?:com|eu|nl|org|net))\b`)

// Build assembles the MIME message: multipart/alternative with the Markdown
// as text/plain and its rendering as text/html.
func (d Draft) Build(now time.Time) ([]byte, string, error) {
	identity, err := d.Check()
	if err != nil {
		return nil, "", err
	}
	htmlPart := d.HTML()
	domain := identity.Address[strings.LastIndex(identity.Address, "@")+1:]
	messageID := newMessageID(now, domain)

	var b bytes.Buffer
	header := func(key, value string) {
		if strings.TrimSpace(value) != "" {
			fmt.Fprintf(&b, "%s: %s\r\n", key, value)
		}
	}
	header("From", (&mail.Address{Name: identity.Name, Address: identity.Address}).String())
	header("To", formatList(d.To))
	header("Cc", formatList(d.Cc))
	header("Bcc", formatList(d.Bcc))
	header("Subject", mime.QEncoding.Encode("utf-8", d.Subject))
	header("Date", now.Format(time.RFC1123Z))
	header("Message-ID", messageID)
	header("In-Reply-To", d.InReplyTo)
	header("References", d.References)
	if d.ReadReceipt {
		header("Disposition-Notification-To", (&mail.Address{Name: identity.Name, Address: identity.Address}).String())
	}
	header("MIME-Version", "1.0")
	header("User-Agent", "mailday")

	var alternative bytes.Buffer
	textParts := multipart.NewWriter(&alternative)
	for _, part := range []struct{ kind, text string }{{"text/plain", d.Body}, {"text/html", htmlPart}} {
		writer, err := textParts.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {part.kind + "; charset=utf-8"},
			"Content-Transfer-Encoding": {"quoted-printable"},
		})
		if err != nil {
			return nil, "", err
		}
		encoder := quotedprintable.NewWriter(writer)
		if _, err := encoder.Write([]byte(strings.ReplaceAll(part.text, "\n", "\r\n"))); err != nil {
			return nil, "", err
		}
		if err := encoder.Close(); err != nil {
			return nil, "", err
		}
	}
	if err := textParts.Close(); err != nil {
		return nil, "", err
	}
	attachments, err := d.attachments()
	if err != nil {
		return nil, "", err
	}
	if len(attachments) == 0 {
		header("Content-Type", "multipart/alternative; boundary="+textParts.Boundary())
		b.WriteString("\r\n")
		b.Write(alternative.Bytes())
		return b.Bytes(), messageID, nil
	}
	// With files the message is multipart/mixed: the text alternatives first,
	// then one base64 part per attachment.
	var mixed bytes.Buffer
	outer := multipart.NewWriter(&mixed)
	header("Content-Type", "multipart/mixed; boundary="+outer.Boundary())
	b.WriteString("\r\n")
	inner, err := outer.CreatePart(textproto.MIMEHeader{"Content-Type": {"multipart/alternative; boundary=" + textParts.Boundary()}})
	if err != nil {
		return nil, "", err
	}
	if _, err := inner.Write(alternative.Bytes()); err != nil {
		return nil, "", err
	}
	for _, attachment := range attachments {
		writer, err := outer.CreatePart(textproto.MIMEHeader{
			"Content-Type":              {mime.FormatMediaType(attachment.Type, map[string]string{"name": attachment.Name})},
			"Content-Disposition":       {mime.FormatMediaType("attachment", map[string]string{"filename": attachment.Name})},
			"Content-Transfer-Encoding": {"base64"},
		})
		if err != nil {
			return nil, "", err
		}
		encoded := base64.StdEncoding.EncodeToString(attachment.Data)
		for start := 0; start < len(encoded); start += 76 {
			if _, err := writer.Write([]byte(encoded[start:min(start+76, len(encoded))] + "\r\n")); err != nil {
				return nil, "", err
			}
		}
	}
	if err := outer.Close(); err != nil {
		return nil, "", err
	}
	b.Write(mixed.Bytes())
	return b.Bytes(), messageID, nil
}

// attachments reads the files to attach and adds the forwarded ones.
func (d Draft) attachments() ([]maildir.AttachmentData, error) {
	var out []maildir.AttachmentData
	for _, path := range d.Attach {
		data, err := os.ReadFile(expandHome(path))
		if err != nil {
			return nil, fmt.Errorf("attach: %v", err)
		}
		kind := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
		if kind == "" {
			kind = http.DetectContentType(data)
		}
		if mediaType, _, err := mime.ParseMediaType(kind); err == nil {
			kind = mediaType
		}
		out = append(out, maildir.AttachmentData{Name: filepath.Base(expandHome(path)), Type: kind, Data: data})
	}
	return append(out, d.Forwarded...), nil
}

func formatList(list string) string {
	addresses, err := mail.ParseAddressList(list)
	if err != nil || len(addresses) == 0 {
		return ""
	}
	out := make([]string, len(addresses))
	for index, address := range addresses {
		out[index] = address.String()
	}
	return strings.Join(out, ", ")
}

type msmtpEntry struct{ name, from, host string }

// msmtpAccounts lists the accounts in the msmtp config that have a from line.
func msmtpAccounts() []msmtpEntry {
	home, _ := os.UserHomeDir()
	var out []msmtpEntry
	for _, path := range []string{filepath.Join(home, ".config", "msmtp", "config"), filepath.Join(home, ".msmtprc")} {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var current *msmtpEntry
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			switch fields[0] {
			case "account":
				current = nil
				if fields[1] != "default" && fields[1] != "default:" {
					out = append(out, msmtpEntry{name: fields[1]})
					current = &out[len(out)-1]
				}
			case "from":
				if current != nil {
					current.from = fields[1]
				}
			case "host":
				if current != nil {
					current.host = fields[1]
				}
			}
		}
		if len(out) > 0 {
			break
		}
	}
	kept := out[:0]
	for _, entry := range out {
		if strings.Contains(entry.from, "@") {
			kept = append(kept, entry)
		}
	}
	return kept
}

// msmtpAccount finds the msmtp account whose from line is one of the
// identity's addresses. Linux and mutt-wizard name the accounts differently.
func msmtpAccount(identity Identity) (string, error) {
	for _, account := range msmtpAccounts() {
		if strings.EqualFold(account.from, identity.Address) || containsFold(identity.Aliases, account.from) {
			return account.name, nil
		}
	}
	return "", fmt.Errorf("no msmtp account sends as %s", identity.Address)
}

// Send hands the message to msmtp, which reads the recipients from the headers
// and drops Bcc before delivery.
func Send(ctx context.Context, message []byte, identity Identity) error {
	account, err := msmtpAccount(identity)
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, "msmtp", "-a", account, "-t")
	command.Stdin = bytes.NewReader(message)
	output, err := command.CombinedOutput()
	if err != nil {
		text := strings.TrimSpace(string(output))
		if len(text) > 300 {
			text = text[len(text)-300:]
		}
		return fmt.Errorf("msmtp: %v %s", err, text)
	}
	return nil
}
