package compose

import (
	"context"
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

func receiptDraft() Draft {
	return Draft{From: "Sam de Vries <sam.devries@gmail.example>", To: "ada@example.org", Subject: "Hello", Body: "Hi Ada\n"}
}

func TestBuildAsksForAReceiptOnlyWhenTheDraftDoes(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	plain, _, err := receiptDraft().Build(now)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plain), "Disposition-Notification-To") {
		t.Fatalf("no request was made, yet:\n%s", plain)
	}
	asking := receiptDraft()
	asking.ReadReceipt = true
	raw, _, err := asking.Build(now)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	address, err := mail.ParseAddress(parsed.Header.Get("Disposition-Notification-To"))
	if err != nil || address.Address != "sam.devries@gmail.example" {
		t.Fatalf("Disposition-Notification-To = %q, %v; want the From address", parsed.Header.Get("Disposition-Notification-To"), err)
	}
	if strings.Contains(strings.ToLower(string(raw)), "read-receipt") {
		t.Fatalf("the draft's own header line leaked into the message:\n%s", raw)
	}
}

func TestReadReceiptsDefaultFollowsTheEnvironment(t *testing.T) {
	t.Setenv("MAILDAY_READ_RECEIPTS", "")
	if New("gmail").ReadReceipt || Reply("gmail", "/p", original(), false).ReadReceipt || Forward("gmail", original()).ReadReceipt {
		t.Fatal("receipts must be off unless asked for")
	}
	t.Setenv("MAILDAY_READ_RECEIPTS", "on")
	if !New("gmail").ReadReceipt || !Reply("gmail", "/p", original(), false).ReadReceipt || !Forward("gmail", original()).ReadReceipt {
		t.Fatal("MAILDAY_READ_RECEIPTS=on must turn it on for every new draft")
	}
}

func TestReadReceiptChoiceSurvivesTheDraftFile(t *testing.T) {
	t.Setenv("MAILDAY_READ_RECEIPTS", "")
	draft := receiptDraft()
	if strings.Contains(draft.File(), "read-receipt") {
		t.Fatalf("an off choice writes no line:\n%s", draft.File())
	}
	draft.ReadReceipt = true
	text := draft.File()
	if !strings.Contains(text, "\nread-receipt: yes\n") {
		t.Fatalf("draft file lacks the line:\n%s", text)
	}
	back, err := Parse(text, Draft{})
	if err != nil || !back.ReadReceipt {
		t.Fatalf("round trip: %+v, %v", back, err)
	}
	// Taking the line out in an editor turns the request off, whatever the base said.
	edited, err := Parse(strings.Replace(text, "read-receipt: yes\n", "", 1), draft)
	if err != nil || edited.ReadReceipt {
		t.Fatalf("a removed line must mean no request: %+v, %v", edited, err)
	}
}

// fileInInbox puts raw in a temporary Maildir and lists it back.
func fileInInbox(t *testing.T, raw []byte) maildir.Message {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "gmail", "Inbox", "cur")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mdn:2,S"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := maildir.New([]string{root}, 0).List(context.Background())
	if err != nil || len(result.Messages) != 1 {
		t.Fatalf("list: %v %+v", err, result.Messages)
	}
	return result.Messages[0]
}

func asked() maildir.Content {
	return maildir.Content{
		MessageID: "<ask-1@hum.uni.example>", Subject: "Chapter 3 draft",
		FromName: "Ruben Bakker", FromAddr: "r.e.bakker@hum.uni.example",
		ToList:    []maildir.Address{{Name: "Sam", Addr: "s.de.vries@hum.uni.example"}},
		ReceiptTo: []maildir.Address{{Name: "Ruben Bakker", Addr: "r.e.bakker@hum.uni.example"}},
		Date:      time.Date(2026, 10, 7, 11, 40, 0, 0, time.UTC),
	}
}

func TestBuildReceiptIsAProperDispositionNotification(t *testing.T) {
	displayed := time.Date(2026, 10, 7, 12, 2, 0, 0, time.UTC)
	raw, identity, err := BuildReceipt("gmail", asked(), displayed)
	if err != nil {
		t.Fatal(err)
	}
	// The message was sent to the University address, so that is who answers.
	if identity.Address != "s.de.vries@hum.uni.example" {
		t.Fatalf("identity %q", identity.Address)
	}
	parsed, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	mediaType, params, _ := mime.ParseMediaType(parsed.Header.Get("Content-Type"))
	if mediaType != "multipart/report" || params["report-type"] != "disposition-notification" {
		t.Fatalf("content type %q %v", mediaType, params)
	}
	if to := parsed.Header.Get("To"); !strings.Contains(to, "r.e.bakker@hum.uni.example") {
		t.Fatalf("to %q", to)
	}
	if subject := parsed.Header.Get("Subject"); subject != "Read: Chapter 3 draft" {
		t.Fatalf("subject %q", subject)
	}
	reader := multipart.NewReader(parsed.Body, params["boundary"])
	human, err := reader.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	machine, err := reader.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	if kind, _, _ := mime.ParseMediaType(machine.Header.Get("Content-Type")); kind != "message/disposition-notification" {
		t.Fatalf("second part is %q", kind)
	}
	if !strings.HasPrefix(human.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("first part is %q", human.Header.Get("Content-Type"))
	}
	fields := map[string]string{}
	buffer := make([]byte, 4096)
	count, _ := machine.Read(buffer)
	for _, line := range strings.Split(string(buffer[:count]), "\r\n") {
		if key, value, ok := strings.Cut(line, ": "); ok {
			fields[key] = value
		}
	}
	for key, want := range map[string]string{
		"Reporting-UA":        "Mailday",
		"Original-Recipient":  "rfc822;s.de.vries@hum.uni.example",
		"Final-Recipient":     "rfc822;s.de.vries@hum.uni.example",
		"Original-Message-ID": "<ask-1@hum.uni.example>",
		"Disposition":         "manual-action/MDN-sent-manually; displayed",
	} {
		if fields[key] != want {
			t.Errorf("%s = %q, want %q", key, fields[key], want)
		}
	}
	if text := string(raw); !strings.Contains(text, "Your message") || !strings.Contains(text, "was displayed on ") {
		t.Fatalf("the human part is missing:\n%s", text)
	}
}

func TestBuiltReceiptReadsBackThroughTheMaildirParser(t *testing.T) {
	raw, _, err := BuildReceipt("gmail", asked(), time.Date(2026, 10, 7, 12, 2, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	receipt := fileInInbox(t, raw).Receipt
	if receipt == nil {
		t.Fatal("the receipt we build is not recognised as one")
	}
	if receipt.OriginalMessageID != "<ask-1@hum.uni.example>" || receipt.Recipient != "s.de.vries@hum.uni.example" || !receipt.Displayed() {
		t.Fatalf("receipt %+v", receipt)
	}
	if receipt.FromName != "Sam de Vries" {
		t.Fatalf("from name %q", receipt.FromName)
	}
}

func TestBuildReceiptRefusesWhenNothingAskedAndCopesWithoutAnAddressedIdentity(t *testing.T) {
	content := asked()
	content.ReceiptTo = nil
	if _, _, err := BuildReceipt("gmail", content, time.Now()); err == nil {
		t.Fatal("answering a request nobody made must fail")
	}
	// Sent to a list: the account's own identity answers, with no Original-Recipient.
	content = asked()
	content.ToList = []maildir.Address{{Addr: "list@example.org"}}
	raw, identity, err := BuildReceipt("gmail", content, time.Now())
	if err != nil || identity.Address != "sam.devries@gmail.example" || strings.Contains(string(raw), "Original-Recipient") {
		t.Fatalf("%v %q\n%s", err, identity.Address, raw)
	}
}

// Send goes through msmtp with the identity's account; a fake msmtp on PATH
// stands in, so the test proves what would be handed over.
func TestBuiltReceiptGoesThroughSendWithItsIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	_ = os.MkdirAll(filepath.Join(home, ".config", "msmtp"), 0o700)
	_ = os.WriteFile(filepath.Join(home, ".config", "msmtp", "config"), []byte("account university\nfrom s.de.vries@hum.uni.example\n"), 0o600)
	bin := t.TempDir()
	log := filepath.Join(bin, "msmtp.log")
	script := "#!/bin/sh\necho \"$@\" > '" + log + "'\ncat > '" + log + ".stdin'\n"
	if err := os.WriteFile(filepath.Join(bin, "msmtp"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	raw, identity, err := BuildReceipt("gmail", asked(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := Send(context.Background(), raw, identity); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(log)
	sent, _ := os.ReadFile(log + ".stdin")
	if strings.TrimSpace(string(args)) != "-a university -t" || !strings.Contains(string(sent), "multipart/report") {
		t.Fatalf("msmtp got %q and %q", args, sent)
	}
}
