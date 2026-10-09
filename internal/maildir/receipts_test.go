package maildir

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// listFixtures files the named testdata messages in one Inbox and lists it.
func listFixtures(t *testing.T, names ...string) []Message {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "gmail", "Inbox")
	for _, leaf := range []string{"cur", "new"} {
		if err := os.MkdirAll(filepath.Join(dir, leaf), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for index, name := range names {
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(dir, "cur", name+":2,S")
		if err := os.WriteFile(file, data, 0o600); err != nil {
			t.Fatal(err)
		}
		stamp := time.Date(2025, 10, 8, 12, index, 0, 0, time.UTC)
		_ = os.Chtimes(file, stamp, stamp)
	}
	result, err := New([]string{root}, 0).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return result.Messages
}

func receiptOf(t *testing.T, name string) Receipt {
	t.Helper()
	messages := listFixtures(t, name)
	if len(messages) != 1 || messages[0].Receipt == nil {
		t.Fatalf("%s: want one message holding a receipt, got %+v", name, messages)
	}
	return *messages[0].Receipt
}

func TestOutlookReceiptIsParsed(t *testing.T) {
	receipt := receiptOf(t, "outlook-mdn.eml")
	if receipt.OriginalMessageID != "<1759837212000000000.a1b2c3d4e5f60718@samdevries.example>" {
		t.Fatalf("original %q", receipt.OriginalMessageID)
	}
	if receipt.Recipient != "r.e.bakker@hum.uni.example" || receipt.FromName != "Ruben Bakker" {
		t.Fatalf("recipient %q name %q", receipt.Recipient, receipt.FromName)
	}
	if !receipt.Displayed() || receipt.Deleted() {
		t.Fatalf("disposition %q", receipt.Disposition)
	}
	if want := time.Date(2025, 10, 7, 12, 2, 9, 0, time.UTC); !receipt.Date.Equal(want) {
		t.Fatalf("date %v, want %v", receipt.Date.UTC(), want)
	}
}

func TestThunderbirdReceiptIsParsed(t *testing.T) {
	receipt := receiptOf(t, "thunderbird-mdn.eml")
	if receipt.OriginalMessageID != "<1759910000000000000.0011223344556677@samdevries.example>" || receipt.Recipient != "ada@example.org" {
		t.Fatalf("receipt %+v", receipt)
	}
	if receipt.FromName != "Ada Lovelace" || !receipt.Displayed() {
		t.Fatalf("name %q disposition %q", receipt.FromName, receipt.Disposition)
	}
}

func TestDeletedReceiptSaysSo(t *testing.T) {
	receipt := receiptOf(t, "outlook-mdn-deleted.eml")
	if !receipt.Deleted() || receipt.Displayed() {
		t.Fatalf("disposition %q", receipt.Disposition)
	}
}

func TestOrdinaryMailIsNotAReceiptButRemembersWhoAsked(t *testing.T) {
	messages := listFixtures(t, "asks-receipt.eml", "outlook-request.eml")
	for _, message := range messages {
		if message.Receipt != nil {
			t.Fatalf("%s was taken for a receipt", message.Subject)
		}
	}
	content := readFixture(t, "asks-receipt.eml")
	if len(content.ReceiptTo) != 1 || content.ReceiptTo[0].Addr != "r.e.bakker@hum.uni.example" {
		t.Fatalf("receipt-to %+v", content.ReceiptTo)
	}
	if invite := readFixture(t, "outlook-request.eml"); len(invite.ReceiptTo) != 0 {
		t.Fatalf("a message that asks for nothing has receipt-to %+v", invite.ReceiptTo)
	}
}

func TestReceiptKeyIgnoresBracketsAndCase(t *testing.T) {
	if ReceiptKey(" <AbC@Example.org> ") != ReceiptKey("abc@example.org") {
		t.Fatal("brackets and case must not matter")
	}
}

func TestBrokenReportStillCountsAsAReceipt(t *testing.T) {
	broken := "From: x@example.org\r\nSubject: s\r\nContent-Type: multipart/report; report-type=disposition-notification; boundary=b\r\n\r\n--b\r\nContent-Type: text/plain\r\n\r\nhello\r\n--b--\r\n"
	root := t.TempDir()
	dir := filepath.Join(root, "gmail", "Inbox", "cur")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken:2,S"), []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := New([]string{root}, 0).List(context.Background())
	if err != nil || len(result.Messages) != 1 || result.Messages[0].Receipt == nil || result.Messages[0].Receipt.OriginalMessageID != "" {
		t.Fatalf("%v %+v", err, result.Messages)
	}
}
