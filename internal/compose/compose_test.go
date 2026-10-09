package compose

import (
	"bytes"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aronvandepol/mailday/internal/maildir"
)

func original() maildir.Content {
	return maildir.Content{
		MessageID: "<orig@luc>", References: "<first@luc>",
		FromName: "Jolien Hendriks", FromAddr: "j.j.hendriks@luc.uni.example",
		ToList:  []maildir.Address{{Name: "Ruben Bakker", Addr: "r.e.bakker@hum.uni.example"}, {Name: "Sam", Addr: "s.de.vries@hum.uni.example"}},
		CCList:  []maildir.Address{{Addr: "sam.devries@gmail.example"}},
		Subject: "RE: Laatste zaken", Date: time.Date(2026, 10, 1, 9, 13, 0, 0, time.UTC),
		Body: "Goedemorgen!\n> earlier\n",
	}
}

func TestReplyAllAnswersFromTheAddressedIdentityAndSkipsYourOwn(t *testing.T) {
	draft := Reply("gmail", "/m/uni/Inbox/cur/x", original(), true)
	if !strings.Contains(draft.From, "s.de.vries@hum.uni.example") {
		t.Fatalf("from = %q, want the University address the mail went to", draft.From)
	}
	if draft.To != `"Jolien Hendriks" <j.j.hendriks@luc.uni.example>` {
		t.Fatalf("to = %q", draft.To)
	}
	if draft.Cc != `"Ruben Bakker" <r.e.bakker@hum.uni.example>` {
		t.Fatalf("cc = %q (own addresses must be dropped)", draft.Cc)
	}
	if draft.Subject != "Re: Laatste zaken" || draft.InReplyTo != "<orig@luc>" || draft.References != "<first@luc> <orig@luc>" {
		t.Fatalf("subject %q in-reply-to %q references %q", draft.Subject, draft.InReplyTo, draft.References)
	}
	if !strings.Contains(draft.Body, "Jolien Hendriks wrote:\n> Goedemorgen!\n>> earlier") {
		t.Fatalf("quoted body:\n%s", draft.Body)
	}
}

func TestDraftFileRoundTripsAndKeepsThreading(t *testing.T) {
	base := Reply("uni", "/p", original(), false)
	edited := strings.Replace(base.File(), "---\n\n\n-- ", "---\n## Agenda\n\n- printen\n\n-- ", 1)
	draft, err := Parse(edited, base)
	if err != nil {
		t.Fatal(err)
	}
	if draft.To != base.To || draft.InReplyTo != "<orig@luc>" || !strings.HasPrefix(draft.Body, "## Agenda") {
		t.Fatalf("parsed draft = %+v", draft)
	}
	if _, err := Parse("no header", base); err == nil {
		t.Fatal("a draft without its header block must be refused")
	}
}

func TestBuildMakesMarkdownPlainAndHTMLAlternatives(t *testing.T) {
	draft := Draft{From: "Sam de Vries <sam.devries@gmail.example>", To: "sam.devries@gmail.example", Bcc: "hidden@example.org",
		Subject: "Tëst ## markdown", Body: "## Heading\n\n**bold** and a [link](https://example.org)\n"}
	raw, id, err := draft.Build(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	message, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	subject, _ := new(mime.WordDecoder).DecodeHeader(message.Header.Get("Subject"))
	if subject != "Tëst ## markdown" || message.Header.Get("Message-Id") != id || !strings.HasSuffix(id, "@gmail.example>") {
		t.Fatalf("subject %q id %q", subject, id)
	}
	mediaType, params, _ := mime.ParseMediaType(message.Header.Get("Content-Type"))
	if mediaType != "multipart/alternative" {
		t.Fatalf("content type %q", mediaType)
	}
	reader := multipart.NewReader(message.Body, params["boundary"])
	parts := map[string]string{}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(part)
		kind, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
		parts[kind] = string(data)
	}
	if !strings.Contains(parts["text/plain"], "## Heading") || !strings.Contains(parts["text/html"], "<h2>Heading</h2>") ||
		!strings.Contains(parts["text/html"], "<strong>bold</strong>") || !strings.Contains(parts["text/html"], `href="https://example.org"`) {
		t.Fatalf("parts = %v", parts)
	}
}

func TestCheckRefusesForeignFromAndMissingRecipients(t *testing.T) {
	if _, err := (Draft{From: "x@evil.example", To: "a@b.c", Body: "hi"}).Check(); err == nil {
		t.Fatal("a From outside your identities must be refused")
	}
	if _, err := (Draft{From: "sam.devries@gmail.example", Body: "hi"}).Check(); err == nil {
		t.Fatal("a draft without recipients must be refused")
	}
}

func TestMsmtpAccountMatchesFromLineOnBothLayouts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	_ = os.MkdirAll(filepath.Join(home, ".config", "msmtp"), 0o700)
	config := "defaults\nauth on\n\naccount sam.devries@gmail.example\nhost smtp.gmail.com\nfrom sam.devries@gmail.example\n\naccount university\nfrom vriessde@staff.uni.example\n\naccount default : sam.devries@gmail.example\n"
	_ = os.WriteFile(filepath.Join(home, ".config", "msmtp", "config"), []byte(config), 0o600)
	for address, want := range map[string]string{"sam.devries@gmail.example": "sam.devries@gmail.example", "s.de.vries@hum.uni.example": "university"} {
		identity, _ := forAddress(address)
		if got, err := msmtpAccount(identity); err != nil || got != want {
			t.Fatalf("msmtpAccount(%s) = %q, %v; want %q", address, got, err, want)
		}
	}
}

func TestReplyToANoteToYourselfGoesToYou(t *testing.T) {
	note := maildir.Content{FromName: "Sam de Vries", FromAddr: "sam.devries@gmail.example", ToList: []maildir.Address{{Addr: "sam.devries@gmail.example"}}, Subject: "note", MessageID: "<n@gmail.com>"}
	if draft := Reply("gmail", "/p", note, false); !strings.Contains(draft.To, "sam.devries@gmail.example") {
		t.Fatalf("to = %q", draft.To)
	}
}

func TestBuildAttachesFilesAndForwardedAttachments(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "agenda é.pdf")
	_ = os.WriteFile(file, []byte("%PDF-1.4 test"), 0o600)
	draft := Draft{From: "sam.devries@gmail.example", To: "sam.devries@gmail.example", Subject: "files", Body: "See **attached**.\n",
		Attach:    []string{file},
		Forwarded: []maildir.AttachmentData{{Name: "logo.png", Type: "image/png", Data: []byte{0x89, 'P', 'N', 'G'}}}}
	raw, _, err := draft.Build(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	message, _ := mail.ReadMessage(bytes.NewReader(raw))
	mediaType, params, _ := mime.ParseMediaType(message.Header.Get("Content-Type"))
	if mediaType != "multipart/mixed" {
		t.Fatalf("content type %q", mediaType)
	}
	reader := multipart.NewReader(message.Body, params["boundary"])
	var kinds, names []string
	var pdf []byte
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		kind, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
		kinds = append(kinds, kind)
		if name := part.FileName(); name != "" {
			names = append(names, name)
			data, _ := io.ReadAll(base64.NewDecoder(base64.StdEncoding, part))
			if strings.HasSuffix(name, ".pdf") {
				pdf = data
			}
		}
	}
	if strings.Join(kinds, ",") != "multipart/alternative,application/pdf,image/png" || strings.Join(names, ",") != "agenda é.pdf,logo.png" {
		t.Fatalf("parts %v names %v", kinds, names)
	}
	if string(pdf) != "%PDF-1.4 test" {
		t.Fatalf("pdf round trip = %q", pdf)
	}
	missing := Draft{From: "sam.devries@gmail.example", To: "a@b.c", Body: "x", Attach: []string{filepath.Join(dir, "nope.pdf")}}
	if _, err := missing.Check(); err == nil {
		t.Fatal("a missing attachment must stop the send")
	}
}

func TestKoreanNamesStayReadableInTheComposerAndEncodedOnTheWire(t *testing.T) {
	content := maildir.Content{FromName: "서울 예시 연구소", FromAddr: "conf@univ.example", Subject: "안내", MessageID: "<k@x>", Body: "x",
		ToList: []maildir.Address{{Addr: "sam.devries@gmail.example"}}}
	draft := Reply("gmail", "/p", content, false)
	if draft.To != `"서울 예시 연구소" <conf@univ.example>` {
		t.Fatalf("to = %q", draft.To)
	}
	draft.Body = "dank u"
	raw, _, err := draft.Build(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	message, _ := mail.ReadMessage(bytes.NewReader(raw))
	if header := message.Header.Get("To"); !strings.Contains(header, "=?utf-8?") || !strings.Contains(header, "<conf@univ.example>") {
		t.Fatalf("wire To = %q, want an encoded name", header)
	}
	list, err := message.Header.AddressList("To")
	if err != nil || list[0].Name != "서울 예시 연구소" {
		t.Fatalf("decoded To = %+v, %v", list, err)
	}
}

func TestSignaturesPerAccountAndStyledInHTML(t *testing.T) {
	if body := New("society").Body; !strings.Contains(body, "-- \nSam de Vries\nWeb editor · Example Society\n") {
		t.Fatalf("ESS body = %q", body)
	}
	reply := Reply("uni", "/p", original(), false)
	if !strings.HasPrefix(reply.Body, "\n\n-- \nSam de Vries\nPhD Candidate · Example University\n\nOn ") {
		t.Fatalf("reply body = %q (short signature above the quote)", reply.Body)
	}
	draft := New("uni")
	draft.Body = "Dear Ruben,\n\nSee **below**.\n" + draft.Body
	html := draft.HTML()
	for _, want := range []string{"<strong>below</strong>", `font-weight:600">Sam de Vries</div>`, `color:#777">Example University Centre`, `href="https://sam.example.org"`} {
		if !strings.Contains(html, want) {
			t.Fatalf("html lacks %q:\n%s", want, html)
		}
	}
	reply.Body = "Thanks.\n" + reply.Body
	if html := reply.HTML(); !strings.Contains(html, "<blockquote>") || strings.Index(html, "font-weight:600") > strings.Index(html, "<blockquote>") {
		t.Fatalf("signature should sit above the quote, which stays Markdown:\n%s", html)
	}
}

func TestPlainReplyKeepsTheCcLine(t *testing.T) {
	content := original()
	content.CCList = append(content.CCList, maildir.Address{Name: "DHQ Editors", Addr: "editors@dhq.example"})
	draft := Reply("uni", "/p", content, false)
	if draft.Cc != `"DHQ Editors" <editors@dhq.example>` {
		t.Fatalf("cc = %q, want the copied editor and none of your own addresses or the other To recipients", draft.Cc)
	}
}

func TestReplyTextStopsAtTheSignatureOrQuote(t *testing.T) {
	if got := ReplyText("Thanks, will read it.\n\n-- \nSam\nOn Fri, Ed wrote:\n> hi\n"); got != "Thanks, will read it." {
		t.Fatalf("with signature: %q", got)
	}
	if got := ReplyText("Yes.\n> quoted\n"); got != "Yes." {
		t.Fatalf("without signature: %q", got)
	}
}
