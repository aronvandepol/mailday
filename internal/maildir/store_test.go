package maildir

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreListsReadsMarksAndArchives(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"Inbox/new", "Inbox/cur", "Archive/new", "Archive/cur"} {
		if err := os.MkdirAll(filepath.Join(root, "gmail", dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	newer := fixtureMessage("New =?UTF-8?Q?subject_=E2=9C=93?=", "Tue, 25 Aug 2026 10:00:00 +0200", "new body")
	older := fixtureMessage("Older", "Mon, 24 Aug 2026 10:00:00 +0200", "old body")
	newPath := filepath.Join(root, "gmail", "Inbox", "new", "new-message")
	oldPath := filepath.Join(root, "gmail", "Inbox", "cur", "old-message:2,S")
	if err := os.WriteFile(newPath, []byte(newer), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oldPath, []byte(older), 0o600); err != nil {
		t.Fatal(err)
	}

	store := New([]string{root}, 20)
	result, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(result.Messages), 2; got != want {
		t.Fatalf("message count = %d, want %d", got, want)
	}
	if got, want := result.Messages[0].Subject, "New subject ✓"; got != want {
		t.Fatalf("subject = %q, want %q", got, want)
	}
	if !result.Messages[0].Unread || result.Messages[1].Unread {
		t.Fatalf("unexpected unread states: %+v", result.Messages)
	}
	if got, want := result.Messages[0].Snippet, "new body"; got != want {
		t.Fatalf("snippet = %q, want %q", got, want)
	}

	content, err := store.Read(result.Messages[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content.Body, "new body") || strings.Contains(content.Body, "HTML fallback") {
		t.Fatalf("body = %q", content.Body)
	}

	updated, err := store.SetUnread(result.Messages[0], false)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Unread || !strings.Contains(updated.Path, filepath.Join("Inbox", "cur")) || !strings.HasSuffix(updated.Path, ":2,S") {
		t.Fatalf("updated message = %+v", updated)
	}
	if err := store.Archive(updated); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "gmail", "Archive", "cur", filepath.Base(updated.Path))); err != nil {
		t.Fatalf("archived message: %v", err)
	}
}

func TestStoreRejectsPathsOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "message")
	if err := os.WriteFile(outside, []byte("Subject: x\n\nbody"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := New([]string{root}, 10)
	if _, err := store.Read(outside); err == nil {
		t.Fatal("Read accepted a path outside the mail root")
	}
}

func TestHTMLOnlyBodyBecomesText(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a", "Inbox", "cur")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	messagePath := filepath.Join(path, "message:2,S")
	message := "From: Sender <sender@example.com>\r\nTo: Person <person@example.com>\r\nSubject: HTML\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<html><body><script>bad()</script><p>Hello <b>there</b></p></body></html>"
	if err := os.WriteFile(messagePath, []byte(message), 0o600); err != nil {
		t.Fatal(err)
	}
	content, err := New([]string{root}, 10).Read(messagePath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(content.Body, "bad()") || !strings.Contains(content.Body, "Hello there") {
		t.Fatalf("body = %q", content.Body)
	}
}

func fixtureMessage(subject, date, body string) string {
	boundary := "fixture-boundary"
	return fmt.Sprintf("From: Sender <sender@example.com>\r\nTo: Person <person@example.com>\r\nSubject: %s\r\nDate: %s\r\nMessage-ID: <%d@example.com>\r\nContent-Type: multipart/mixed; boundary=%q\r\n\r\n--%s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n--%s\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>HTML fallback</p>\r\n--%s--\r\n", subject, date, time.Now().UnixNano(), boundary, boundary, body, boundary, boundary)
}

func TestStoreReadsTwoRootsTagBoxesAndFilesWithoutUID(t *testing.T) {
	linux, mac := t.TempDir(), t.TempDir()
	for _, dir := range []string{
		filepath.Join(linux, "uni", "Inbox", "cur"), filepath.Join(linux, "uni", "@Reply", "cur"),
		filepath.Join(mac, "me@example.org", "INBOX", "cur"), filepath.Join(mac, "me@example.org", "@ESS", "cur"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, subject string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(fixtureMessage(subject, "Tue, 25 Aug 2026 10:00:00 +0200", "body")), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(linux, "uni", "Inbox", "cur", "a:2,S"), "Inbox mail")
	write(filepath.Join(linux, "uni", "@Reply", "cur", "b:2,S"), "Open request")
	macPath := filepath.Join(mac, "me@example.org", "INBOX", "cur", "c,U=2072:2,S")
	write(macPath, "Mac inbox mail")

	store := New([]string{linux, mac, filepath.Join(t.TempDir(), "missing")}, 10)
	result, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(result.Accounts, ","), "example,uni"; got != want {
		t.Fatalf("accounts = %q, want %q", got, want)
	}
	if got, want := strings.Join(result.Boxes, ","), "Inbox,@Reply,@ESS"; got != want {
		t.Fatalf("boxes = %q, want %q", got, want)
	}
	boxOf := map[string]string{}
	var macMessage Message
	for _, message := range result.Messages {
		boxOf[message.Subject] = message.Box
		if message.Subject == "Mac inbox mail" {
			macMessage = message
		}
	}
	if boxOf["Open request"] != "@Reply" || boxOf["Mac inbox mail"] != InboxBox {
		t.Fatalf("boxes per subject = %v", boxOf)
	}

	moved, err := store.MoveTo(macMessage, "@ESS")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(mac, "me@example.org", "@ESS", "cur", "c:2,S"); moved != want {
		t.Fatalf("moved to %q, want %q (UID field stripped, flags kept)", moved, want)
	}
	if _, err := store.MoveTo(Message{Path: moved}, "@ESS"); err == nil {
		t.Fatal("filing into the box a message is already in should fail")
	}
	if _, err := store.MoveTo(Message{Path: moved}, "../escape"); err == nil {
		t.Fatal("a box name with a path separator must be refused")
	}
	if _, err := store.MoveTo(Message{Path: moved}, InboxBox); err != nil {
		t.Fatalf("moving back to the inbox: %v", err)
	}
}

func TestUnwrapSafelinks(t *testing.T) {
	wrapped := `<a href="https://eur03.safelinks.protection.outlook.com/?url=http%3A%2F%2Fmarcorinaldi.site.example.org%2F&amp;data=05%7C02&amp;reserved=0">site</a>`
	if got, want := unwrapSafelinks(wrapped), `<a href="http://marcorinaldi.site.example.org/">site</a>`; got != want {
		t.Fatalf("unwrapSafelinks = %q, want %q", got, want)
	}
	odd := "https://eur03.safelinks.protection.outlook.com/?url=javascript%3Aalert(1)&data=x"
	if got := unwrapSafelinks(odd); got != odd {
		t.Fatalf("a non-web target must stay wrapped, got %q", got)
	}
}

func TestDeleteMovesToTrashAndBack(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"uni/Inbox/cur", "uni/Deleted Items/cur", "uni/Trash/cur"} {
		_ = os.MkdirAll(filepath.Join(root, dir), 0o700)
	}
	path := filepath.Join(root, "uni", "Inbox", "cur", "m:2,S")
	_ = os.WriteFile(path, []byte(fixtureMessage("Gone", "Tue, 25 Aug 2026 10:00:00 +0200", "x")), 0o600)
	store := New([]string{root}, 10)
	trashed, err := store.MoveTo(Message{Path: path}, "Trash")
	if err != nil || !strings.Contains(trashed, "Deleted Items") {
		t.Fatalf("trashed to %q, %v; Exchange's Deleted Items should win over Trash", trashed, err)
	}
	restored, err := store.MoveTo(Message{Path: trashed}, InboxBox)
	if err != nil || restored != path {
		t.Fatalf("restored to %q, %v", restored, err)
	}
	answered, err := store.MarkAnswered(restored)
	if err != nil || !strings.HasSuffix(answered, ":2,RS") {
		t.Fatalf("answered path %q, %v", answered, err)
	}
}

func TestArchiveBoxIsCappedDedupedAndReadOnly(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"gmail/Inbox/cur", "gmail/Archive/cur", "gmail/Trash/cur"} {
		_ = os.MkdirAll(filepath.Join(root, dir), 0o700)
	}
	both := "Message-ID: <same@x>\r\n" + fixtureMessage("In inbox and All Mail", "Tue, 25 Aug 2026 10:00:00 +0200", "x")
	_ = os.WriteFile(filepath.Join(root, "gmail", "Inbox", "cur", "a:2,S"), []byte(both), 0o600)
	_ = os.WriteFile(filepath.Join(root, "gmail", "Archive", "cur", "a2:2,S"), []byte(both), 0o600)
	archived := filepath.Join(root, "gmail", "Archive", "cur", "b:2,S")
	_ = os.WriteFile(archived, []byte("Message-ID: <old@x>\r\n"+fixtureMessage("Only archived", "Mon, 24 Aug 2026 10:00:00 +0200", "y")), 0o600)

	store := New([]string{root}, 10)
	result, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(result.Boxes, ","); got != "Inbox,Archive" {
		t.Fatalf("boxes = %s", got)
	}
	perBox := map[string][]string{}
	for _, message := range result.Messages {
		perBox[message.Box] = append(perBox[message.Box], message.Subject)
	}
	if len(perBox["Archive"]) != 1 || perBox["Archive"][0] != "Only archived" {
		t.Fatalf("archive box = %v (the All Mail copy of an inbox message must be dropped)", perBox["Archive"])
	}
	if _, err := store.MoveTo(Message{Path: archived}, "Trash"); err == nil {
		t.Fatal("moving out of Archive must be refused")
	}
	if _, err := store.MarkAnswered(archived); err != nil {
		t.Fatalf("flagging an archived reply must still work: %v", err)
	}
}

func TestSaveAttachmentsDecodesAndNeverOverwrites(t *testing.T) {
	root, downloads := t.TempDir(), t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "uni", "Inbox", "cur"), 0o700)
	raw := "From: a@b.c\r\nSubject: files\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=XX\r\n\r\n" +
		"--XX\r\nContent-Type: text/plain\r\n\r\nsee attached\r\n" +
		"--XX\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename*=UTF-8''hand-out%20%C3%A9.pdf\r\nContent-Transfer-Encoding: base64\r\n\r\nJVBERi0xLjQK\r\n" +
		"--XX\r\nContent-Type: image/png\r\nContent-Id: <logo>\r\nContent-Transfer-Encoding: base64\r\n\r\niVBORw0K\r\n" +
		"--XX\r\nContent-Type: text/plain; name=\"../../etc/notes.txt\"\r\nContent-Disposition: attachment\r\n\r\nnotes\r\n--XX--\r\n"
	path := filepath.Join(root, "uni", "Inbox", "cur", "m:2,S")
	_ = os.WriteFile(path, []byte(raw), 0o600)
	store := New([]string{root}, 10)
	first, err := store.SaveAttachments(path, downloads)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || filepath.Base(first[0]) != "hand-out é.pdf" || filepath.Base(first[1]) != "notes.txt" {
		t.Fatalf("saved %v (inline logo skipped, path stripped from the name)", first)
	}
	if data, _ := os.ReadFile(first[0]); string(data) != "%PDF-1.4\n" {
		t.Fatalf("pdf bytes = %q", data)
	}
	second, _ := store.SaveAttachments(path, downloads)
	if filepath.Base(second[0]) != "hand-out é (2).pdf" {
		t.Fatalf("second save = %v, must not overwrite", second)
	}
}

func TestSentBoxPicksTheLiveFolderAndIsReadOnly(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"g/INBOX/cur", "g/Sent/cur", "g/[Gmail]/Sent Mail/cur", "g/@Admin/cur"} {
		_ = os.MkdirAll(filepath.Join(root, dir), 0o700)
	}
	old := filepath.Join(root, "g", "Sent", "cur", "old:2,S")
	_ = os.WriteFile(old, []byte("Message-ID: <o@x>\r\n"+fixtureMessage("Stale sent", "Mon, 24 Aug 2026 10:00:00 +0200", "x")), 0o600)
	_ = os.Chtimes(old, time.Now().Add(-48*time.Hour), time.Now().Add(-48*time.Hour))
	live := filepath.Join(root, "g", "[Gmail]", "Sent Mail", "cur", "new:2,S")
	_ = os.WriteFile(live, []byte("Message-ID: <n@x>\r\nTo: Ruben <r@x>\r\n"+fixtureMessage("Fresh sent", "Tue, 25 Aug 2026 10:00:00 +0200", "y")), 0o600)
	store := New([]string{root}, 10)
	result, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(result.Boxes, ","); got != "Inbox,@Admin,Sent" {
		t.Fatalf("boxes = %s", got)
	}
	var sent []string
	for _, message := range result.Messages {
		if message.Box == SentBox {
			sent = append(sent, message.Subject)
		}
	}
	if strings.Join(sent, ",") != "Fresh sent" {
		t.Fatalf("sent box = %v, want the newer [Gmail]/Sent Mail folder", sent)
	}
	if _, err := store.Read(live); err != nil {
		t.Fatalf("reading sent mail: %v", err)
	}
	if _, err := store.MoveTo(Message{Path: live}, "@Admin"); err == nil {
		t.Fatal("sent mail must not be filed away")
	}
}

func TestTwoCopiesOfOneMessageShowOnce(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "uni", "Inbox", "cur"), 0o700)
	_ = os.MkdirAll(filepath.Join(root, "uni", "Sent", "cur"), 0o700)
	raw := []byte("Message-ID: <dup@x>\r\n" + fixtureMessage("Sent twice", "Fri, 02 Oct 2026 10:10:20 +0200", "x"))
	_ = os.WriteFile(filepath.Join(root, "uni", "Sent", "cur", "a:2,S"), raw, 0o600)
	_ = os.WriteFile(filepath.Join(root, "uni", "Sent", "cur", "b:2,S"), raw, 0o600)
	result, err := New([]string{root}, 10).List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(result.Messages))
	}
}

func TestResolveFindsAMessageAfterItWasReadAndFiled(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"gmail/Inbox/new", "gmail/@Travel/cur"} {
		_ = os.MkdirAll(filepath.Join(root, dir), 0o700)
	}
	original := filepath.Join(root, "gmail", "Inbox", "new", "1790949026.1101027_1.arch:2,")
	moved := filepath.Join(root, "gmail", "@Travel", "cur", "1790949026.1101027_1.arch:2,S")
	_ = os.WriteFile(moved, []byte("x"), 0o600)
	store := New([]string{root}, 10)
	if got := store.Resolve(original); got != moved {
		t.Fatalf("Resolve = %q, want %q", got, moved)
	}
	if got := store.Resolve(filepath.Join(root, "gmail", "Inbox", "new", "gone:2,")); got != "" {
		t.Fatalf("a missing message resolved to %q", got)
	}
}

func TestKeySurvivesRenames(t *testing.T) {
	want := Key("/m/a/Inbox/new/1700.123_4.arch")
	for _, path := range []string{
		"/m/a/Inbox/cur/1700.123_4.arch:2,S",
		"/m/a/Inbox/cur/1700.123_4.arch,U=812:2,FRS",
		"/m/a/@Reply/cur/1700.123_4.arch:2,",
	} {
		if got := Key(path); got != want {
			t.Errorf("Key(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestArchiveMovesBackExceptOnGmail(t *testing.T) {
	root := t.TempDir()
	for _, account := range []string{"uni", "gmail"} {
		for _, box := range []string{"Inbox", "Archive"} {
			for _, leaf := range []string{"cur", "new", "tmp"} {
				os.MkdirAll(filepath.Join(root, account, box, leaf), 0o700)
			}
		}
		path := filepath.Join(root, account, "Inbox", "cur", "1.x:2,S")
		os.WriteFile(path, []byte("From: a@x\r\nSubject: s\r\n\r\nb\r\n"), 0o600)
		store := New([]string{root}, 100)
		archived, err := store.MoveTo(Message{Path: path}, ArchiveBox)
		if err != nil {
			t.Fatal(err)
		}
		_, err = store.MoveTo(Message{Path: archived}, InboxBox)
		if account == "uni" && err != nil {
			t.Fatalf("uni: Archive back to Inbox: %v", err)
		}
		if account == "gmail" && (err == nil || !strings.Contains(err.Error(), "All Mail")) {
			t.Fatalf("gmail: want the All Mail refusal, got %v", err)
		}
	}
}

func TestListReadsThreadingHeaders(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "gmail", "Inbox", "cur"), 0o700); err != nil {
		t.Fatal(err)
	}
	raw := "From: Ada <ada@example.org>\r\nTo: sam@example.org\r\nSubject: Re: Plan\r\nDate: Tue, 25 Aug 2026 10:00:00 +0200\r\n" +
		"Message-ID: <c@example.org>\r\nIn-Reply-To: <b@example.org>\r\nReferences: <a@example.org>\r\n\t<b@example.org>\r\n\r\nbody\r\n"
	if err := os.WriteFile(filepath.Join(root, "gmail", "Inbox", "cur", "one:2,S"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := New([]string{root}, 20).List(context.Background())
	if err != nil || len(result.Messages) != 1 {
		t.Fatalf("list: %v, %d messages", err, len(result.Messages))
	}
	message := result.Messages[0]
	if message.InReplyTo != "<b@example.org>" || !strings.Contains(message.References, "<a@example.org>") || !strings.Contains(message.References, "<b@example.org>") {
		t.Fatalf("in-reply-to %q, references %q", message.InReplyTo, message.References)
	}
}
