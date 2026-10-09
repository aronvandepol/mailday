package maildir

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"io"
	"io/fs"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	xhtml "golang.org/x/net/html"
	"golang.org/x/net/html/charset"

	"github.com/aronvandepol/mailday/internal/terminal"
)

const (
	maxMessageBytes     = 12 << 20
	maxPreviewReadBytes = 96 << 10
	maxPreviewRunes     = 280
)

type bodyParts struct {
	plain       []string
	html        []string // raw HTML; the reader turns it into Markdown
	attachments []Attachment
	invitations []*Invitation
}

func readMessagePreview(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	message, err := mail.ReadMessage(file)
	if err != nil {
		return "", err
	}
	return previewBody(message.Header, message.Body), nil
}

func previewBody(header mail.Header, reader io.Reader) string {
	parts, _ := collectBody(textproto.MIMEHeader(header), io.LimitReader(reader, maxPreviewReadBytes))
	body := strings.Join(parts.plain, " ")
	if strings.TrimSpace(body) == "" {
		texts := make([]string, 0, len(parts.html))
		for _, html := range parts.html {
			texts = append(texts, htmlToText([]byte(html)))
		}
		body = strings.Join(texts, " ")
	}
	body = terminal.SanitizeLine(strings.Join(strings.Fields(body), " "))
	runes := []rune(body)
	if len(runes) > maxPreviewRunes {
		body = string(runes[:maxPreviewRunes]) + "..."
	}
	return body
}

func (s *Store) Read(path string) (Content, error) {
	content, _, err := s.ReadResolved(path)
	return content, err
}

// ReadResolved is Read for a path captured earlier: if the file was renamed
// meanwhile it reads the message under its new name, which it also returns.
func (s *Store) ReadResolved(path string) (Content, string, error) {
	var content Content
	used, err := s.withResolved(path, func(path string) error {
		var err error
		content, err = s.readAt(path)
		return err
	})
	return content, used, err
}

func (s *Store) readAt(path string) (Content, error) {
	if _, _, err := s.locateRead(path); err != nil {
		return Content{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return Content{}, err
	}
	defer f.Close()

	limited := io.LimitReader(f, maxMessageBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return Content{}, fmt.Errorf("read message: %w", err)
	}
	if len(data) > maxMessageBytes {
		return Content{}, fmt.Errorf("message exceeds the %d MiB preview limit", maxMessageBytes>>20)
	}
	parsed, err := mail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		return Content{}, fmt.Errorf("parse message: %w", err)
	}
	parts, err := collectBody(textproto.MIMEHeader(parsed.Header), parsed.Body)
	if err != nil {
		return Content{}, err
	}
	plain := unwrapSafelinks(strings.TrimSpace(strings.Join(parts.plain, "\n\n")))
	html := unwrapSafelinks(strings.TrimSpace(strings.Join(parts.html, "\n")))
	body := plain
	if body == "" && html != "" {
		body = strings.TrimSpace(htmlToText([]byte(html)))
	}
	if body == "" && len(parts.invitations) > 0 {
		// A bare calendar part has no other text; its notes are the message.
		body = strings.TrimSpace(parts.invitations[0].Description)
	}
	if body == "" {
		body = "(No readable text body)"
	}
	date, _ := parsed.Header.Date()
	fromName, fromAddr := addressLabel(parsed.Header.Get("From"))
	var invitation *Invitation
	if len(parts.invitations) > 0 {
		invitation = parts.invitations[0]
	}
	return Content{
		FromName:    terminal.SanitizeLine(fromName),
		FromAddr:    terminal.SanitizeLine(fromAddr),
		HTML:        html,
		MessageID:   terminal.SanitizeLine(strings.TrimSpace(parsed.Header.Get("Message-Id"))),
		References:  terminal.SanitizeLine(strings.Join(strings.Fields(parsed.Header.Get("References")), " ")),
		ReplyTo:     parseAddresses(parsed.Header.Get("Reply-To")),
		ToList:      parseAddresses(parsed.Header.Get("To")),
		CCList:      parseAddresses(parsed.Header.Get("Cc")),
		From:        terminal.SanitizeLine(decodeAddressList(parsed.Header.Get("From"))),
		To:          terminal.SanitizeLine(decodeAddressList(parsed.Header.Get("To"))),
		CC:          terminal.SanitizeLine(decodeAddressList(parsed.Header.Get("Cc"))),
		Subject:     terminal.SanitizeLine(decodeHeader(parsed.Header.Get("Subject"))),
		Date:        date,
		Body:        terminal.Sanitize(normalizeNewlines(body)),
		Attachments: parts.attachments,
		Invitation:  invitation,
		ReceiptTo:   parseAddresses(parsed.Header.Get("Disposition-Notification-To")),
	}, nil
}

func collectBody(header textproto.MIMEHeader, reader io.Reader) (bodyParts, error) {
	mediaType, params, err := mime.ParseMediaType(header.Get("Content-Type"))
	if err != nil || mediaType == "" {
		mediaType = "text/plain"
	}
	disposition, dispositionParams, _ := mime.ParseMediaType(header.Get("Content-Disposition"))
	filename := dispositionParams["filename"]
	if filename == "" {
		filename = params["name"]
	}
	filename = terminal.SanitizeLine(decodeHeader(filename))
	if strings.EqualFold(disposition, "attachment") {
		if isCalendarPart(mediaType, filename) {
			// An invite.ics attachment repeats the inline calendar part; the
			// reader shows it as a card, not as a file.
			data, _ := io.ReadAll(io.LimitReader(transferReader(header.Get("Content-Transfer-Encoding"), reader), maxMessageBytes))
			if invitation := parseInvitation(string(data), params["method"]); invitation != nil {
				return bodyParts{invitations: []*Invitation{invitation}}, nil
			}
			if filename == "" {
				filename = "(unnamed " + mediaType + ")"
			}
			return bodyParts{attachments: []Attachment{{Name: filename, Type: mediaType, Size: int64(len(data))}}}, nil
		}
		return bodyParts{attachments: []Attachment{attachmentOf(header, mediaType, filename, reader)}}, nil
	}

	if strings.HasPrefix(strings.ToLower(mediaType), "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return bodyParts{}, fmt.Errorf("multipart message has no boundary")
		}
		var result bodyParts
		var alternative []bodyParts
		multipartReader := multipart.NewReader(reader, boundary)
		for {
			part, err := multipartReader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return result, fmt.Errorf("read MIME part: %w", err)
			}
			child, err := collectBody(part.Header, part)
			_ = part.Close()
			if err != nil {
				continue
			}
			if strings.EqualFold(mediaType, "multipart/alternative") {
				alternative = append(alternative, child)
			} else {
				result.merge(child)
			}
		}
		if strings.EqualFold(mediaType, "multipart/alternative") {
			// Keep both alternatives: the reader prefers the HTML for its
			// structure and falls back to the plain text.
			for _, child := range alternative {
				result.merge(child)
			}
		}
		return result, nil
	}

	decoded := transferReader(header.Get("Content-Transfer-Encoding"), reader)
	data, err := io.ReadAll(io.LimitReader(decoded, maxMessageBytes+1))
	if err != nil {
		return bodyParts{}, fmt.Errorf("decode MIME part: %w", err)
	}
	if len(data) > maxMessageBytes {
		return bodyParts{}, fmt.Errorf("MIME part exceeds preview limit")
	}
	if label := params["charset"]; label != "" && !strings.EqualFold(label, "utf-8") && !strings.EqualFold(label, "us-ascii") {
		converted, err := charset.NewReaderLabel(label, bytes.NewReader(data))
		if err == nil {
			if convertedData, readErr := io.ReadAll(io.LimitReader(converted, maxMessageBytes+1)); readErr == nil {
				data = convertedData
			}
		}
	}

	if isCalendarPart(mediaType, filename) {
		if invitation := parseInvitation(string(data), params["method"]); invitation != nil {
			return bodyParts{invitations: []*Invitation{invitation}}, nil
		}
	}

	switch strings.ToLower(mediaType) {
	case "text/plain":
		return bodyParts{plain: []string{string(data)}}, nil
	case "text/html":
		return bodyParts{html: []string{string(data)}}, nil
	default:
		// Inline images referenced from the HTML carry a Content-ID and
		// usually no filename; they are part of the layout, not attachments.
		if filename == "" && header.Get("Content-Id") != "" {
			return bodyParts{}, nil
		}
		if filename == "" {
			filename = "(unnamed " + mediaType + ")"
		}
		return bodyParts{attachments: []Attachment{{Name: filename, Type: mediaType, Size: int64(len(data))}}}, nil
	}
}

// attachmentOf measures a declared attachment by decoding it, without keeping it.
func attachmentOf(header textproto.MIMEHeader, mediaType, filename string, reader io.Reader) Attachment {
	size, _ := io.Copy(io.Discard, io.LimitReader(transferReader(header.Get("Content-Transfer-Encoding"), reader), maxMessageBytes))
	if filename == "" {
		filename = "(unnamed " + mediaType + ")"
	}
	return Attachment{Name: filename, Type: mediaType, Size: size}
}

func (p *bodyParts) merge(other bodyParts) {
	p.plain = append(p.plain, other.plain...)
	p.html = append(p.html, other.html...)
	p.attachments = append(p.attachments, other.attachments...)
	p.invitations = append(p.invitations, other.invitations...)
}

func transferReader(encoding string, reader io.Reader) io.Reader {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, reader)
	case "quoted-printable":
		return quotedprintable.NewReader(reader)
	default:
		return reader
	}
}

func htmlToText(data []byte) string {
	tokenizer := xhtml.NewTokenizer(bytes.NewReader(data))
	var b strings.Builder
	skipDepth := 0
	for {
		typeOfToken := tokenizer.Next()
		switch typeOfToken {
		case xhtml.ErrorToken:
			return compactBlankLines(html.UnescapeString(b.String()))
		case xhtml.StartTagToken:
			token := tokenizer.Token()
			name := strings.ToLower(token.Data)
			if name == "script" || name == "style" {
				skipDepth++
				continue
			}
			if skipDepth == 0 && isBlockElement(name) {
				writeBreak(&b)
			}
		case xhtml.EndTagToken:
			token := tokenizer.Token()
			name := strings.ToLower(token.Data)
			if name == "script" || name == "style" {
				if skipDepth > 0 {
					skipDepth--
				}
				continue
			}
			if skipDepth == 0 && isBlockElement(name) {
				writeBreak(&b)
			}
		case xhtml.TextToken:
			if skipDepth == 0 {
				b.Write(tokenizer.Text())
			}
		}
	}
}

func isBlockElement(name string) bool {
	switch name {
	case "br", "p", "div", "li", "tr", "h1", "h2", "h3", "h4", "h5", "h6", "blockquote", "pre", "section", "article":
		return true
	default:
		return false
	}
}

func writeBreak(b *strings.Builder) {
	b.WriteByte('\n')
}

func normalizeNewlines(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	return strings.ReplaceAll(value, "\r", "\n")
}

func compactBlankLines(value string) string {
	lines := strings.Split(normalizeNewlines(value), "\n")
	out := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			if blank {
				continue
			}
			blank = true
			out = append(out, "")
			continue
		}
		blank = false
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func parseAddresses(raw string) []Address {
	addresses, err := mail.ParseAddressList(decodeHeader(raw))
	if err != nil {
		return nil
	}
	out := make([]Address, 0, len(addresses))
	for _, address := range addresses {
		out = append(out, Address{Name: terminal.SanitizeLine(strings.TrimSpace(address.Name)), Addr: terminal.SanitizeLine(address.Address)})
	}
	return out
}

// Microsoft 365 rewrites every link in mail on some tenants into a Safe Links redirect
// that carries the real address in its url parameter.
var safelink = regexp.MustCompile(`https?://[A-Za-z0-9.-]*safelinks\.protection\.outlook\.com/\?url=([^&"'\s<>]+)(?:&(?:amp;)?[^"'\s<>]*)?`)

func unwrapSafelinks(text string) string {
	return safelink.ReplaceAllStringFunc(text, func(match string) string {
		target, err := url.QueryUnescape(safelink.FindStringSubmatch(match)[1])
		if err != nil || !(strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "mailto:")) {
			return match
		}
		return target
	})
}

// AttachmentData is one decoded attachment.
type AttachmentData struct {
	Name string
	Type string
	Data []byte
}

// Attachments decodes the attachments of the message at path: parts marked
// as attachments, and non-text parts with a filename. Inline images that only
// serve the HTML layout are skipped, as in the reader's list.
func (s *Store) Attachments(path string) ([]AttachmentData, error) {
	if _, _, err := s.locateRead(path); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	parsed, err := mail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("parse message: %w", err)
	}
	var out []AttachmentData
	walkAttachments(textproto.MIMEHeader(parsed.Header), parsed.Body, &out)
	return out, nil
}

func walkAttachments(header textproto.MIMEHeader, reader io.Reader, out *[]AttachmentData) {
	mediaType, params, err := mime.ParseMediaType(header.Get("Content-Type"))
	if err != nil || mediaType == "" {
		mediaType = "text/plain"
	}
	if strings.HasPrefix(strings.ToLower(mediaType), "multipart/") {
		if params["boundary"] == "" {
			return
		}
		parts := multipart.NewReader(reader, params["boundary"])
		for {
			part, err := parts.NextPart()
			if err != nil {
				return
			}
			walkAttachments(part.Header, part, out)
			_ = part.Close()
		}
	}
	disposition, dispositionParams, _ := mime.ParseMediaType(header.Get("Content-Disposition"))
	filename := dispositionParams["filename"]
	if filename == "" {
		filename = params["name"]
	}
	filename = decodeHeader(filename)
	isAttachment := strings.EqualFold(disposition, "attachment") ||
		(filename != "" && !strings.HasPrefix(strings.ToLower(mediaType), "text/")) ||
		(!strings.HasPrefix(strings.ToLower(mediaType), "text/") && header.Get("Content-Id") == "")
	if !isAttachment {
		return
	}
	data, err := io.ReadAll(io.LimitReader(transferReader(header.Get("Content-Transfer-Encoding"), reader), maxMessageBytes))
	if err != nil {
		return
	}
	*out = append(*out, AttachmentData{Name: safeFilename(filename, mediaType, len(*out)+1), Type: mediaType, Data: data})
}

// safeFilename keeps an attachment's name usable as a file: no directories,
// no control characters, and a made-up name when the sender gave none.
func safeFilename(name, mediaType string, index int) string {
	name = terminal.SanitizeLine(filepath.Base(strings.ReplaceAll(name, `\`, "/")))
	name = strings.Trim(name, ". ")
	if name == "" || name == "/" {
		extension := ""
		if extensions, _ := mime.ExtensionsByType(mediaType); len(extensions) > 0 {
			extension = extensions[0]
		}
		name = fmt.Sprintf("attachment-%d%s", index, extension)
	}
	return name
}

// SaveAttachments writes the message's attachments into dir, adding " (2)"
// and so on instead of overwriting, and returns the paths written.
func (s *Store) SaveAttachments(path, dir string) ([]string, error) {
	attachments, err := s.Attachments(path)
	if err != nil {
		return nil, err
	}
	if len(attachments) == 0 {
		return nil, fmt.Errorf("this message has no attachments")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var written []string
	for _, attachment := range attachments {
		extension := filepath.Ext(attachment.Name)
		stem := strings.TrimSuffix(attachment.Name, extension)
		target := filepath.Join(dir, attachment.Name)
		for counter := 2; ; counter++ {
			file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
			if errors.Is(err, fs.ErrExist) {
				target = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, counter, extension))
				continue
			}
			if err != nil {
				return written, err
			}
			_, writeErr := file.Write(attachment.Data)
			closeErr := file.Close()
			if writeErr != nil || closeErr != nil {
				return written, errors.Join(writeErr, closeErr)
			}
			break
		}
		written = append(written, target)
	}
	return written, nil
}
