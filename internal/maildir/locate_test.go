package maildir

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeMessage(t *testing.T, path, subject, messageID string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	raw := "From: Ada <ada@example.com>\r\nSubject: " + subject + "\r\nDate: Tue, 25 Aug 2026 10:00:00 +0200\r\nMessage-ID: " + messageID + "\r\n\r\nbody of " + subject + "\r\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadAndAttachmentsAcceptAnyMaildirButMutatorsDoNot(t *testing.T) {
	root := t.TempDir()
	store := New([]string{root}, 10)
	for _, folder := range []string{"Spam", "Junk Email", filepath.Join("[Gmail]", "Spam"), filepath.Join("Projects", "2026", "Budget")} {
		path := filepath.Join(root, "uni", folder, "cur", "1700.1_1.host:2,S")
		writeMessage(t, path, "Offer", "<offer@x>")
		content, err := store.Read(path)
		if err != nil || !strings.Contains(content.Body, "body of Offer") {
			t.Fatalf("Read in %s: %q, %v", folder, content.Body, err)
		}
		if _, err := store.Attachments(path); err != nil {
			t.Fatalf("Attachments in %s: %v", folder, err)
		}
		if _, err := store.SaveAttachments(path, t.TempDir()); err == nil || !strings.Contains(err.Error(), "no attachments") {
			t.Fatalf("SaveAttachments in %s: %v, want the no-attachments answer rather than a location error", folder, err)
		}
		if _, err := store.SetUnread(Message{Path: path}, true); err == nil || !strings.Contains(err.Error(), "not in a mailbox Mailday shows") {
			t.Fatalf("SetUnread in %s = %v, want it refused", folder, err)
		}
		if _, err := store.MoveTo(Message{Path: path}, InboxBox); err == nil {
			t.Fatalf("MoveTo in %s should be refused", folder)
		}
		if _, err := store.MarkAnswered(path); err == nil {
			t.Fatalf("MarkAnswered in %s should be refused", folder)
		}
	}
	// Reading still stays inside the roots and inside Maildir leaves.
	outside := filepath.Join(t.TempDir(), "uni", "Spam", "cur", "m")
	writeMessage(t, outside, "Outside", "<o@x>")
	if _, err := store.Read(outside); err == nil {
		t.Fatal("Read accepted a path outside the roots")
	}
	if _, err := store.Read(filepath.Join(root, "uni", "Spam", "tmp", "m")); err == nil {
		t.Fatal("Read accepted a path that is not in cur/ or new/")
	}
}

func TestMutatorsAndReadFollowAFileRenamedAfterCapture(t *testing.T) {
	setup := func(t *testing.T) (store *Store, root, captured string) {
		root = t.TempDir()
		for _, dir := range []string{"gmail/Inbox/new", "gmail/Inbox/cur", "gmail/@Reply/cur"} {
			_ = os.MkdirAll(filepath.Join(root, dir), 0o700)
		}
		captured = filepath.Join(root, "gmail", "Inbox", "new", "1700.1_1.host")
		writeMessage(t, captured, "Renamed", "<r@x>")
		return New([]string{root}, 10), root, captured
	}
	// mbsync moves the file to cur/ and gives it a UID and the S flag.
	rename := func(t *testing.T, root, captured string) string {
		renamed := filepath.Join(root, "gmail", "Inbox", "cur", "1700.1_1.host,U=7:2,S")
		if err := os.Rename(captured, renamed); err != nil {
			t.Fatal(err)
		}
		return renamed
	}

	t.Run("SetUnread", func(t *testing.T) {
		store, root, captured := setup(t)
		renamed := rename(t, root, captured)
		// Marking unread drops the S flag the renamed file carries.
		updated, err := store.SetUnread(Message{Path: captured}, true)
		if want := strings.TrimSuffix(renamed, "S"); err != nil || updated.Path != want || !updated.Unread {
			t.Fatalf("SetUnread = %+v, %v; want path %s", updated, err, want)
		}
		updated, err = store.SetUnread(Message{Path: captured, Unread: true}, false)
		if err != nil || updated.Unread || updated.Path != renamed {
			t.Fatalf("marking read = %+v, %v", updated, err)
		}
	})
	t.Run("MoveTo", func(t *testing.T) {
		store, root, captured := setup(t)
		rename(t, root, captured)
		moved, err := store.MoveTo(Message{Path: captured}, "@Reply")
		want := filepath.Join(root, "gmail", "@Reply", "cur", "1700.1_1.host:2,S")
		if err != nil || moved != want {
			t.Fatalf("MoveTo = %q, %v; want %q", moved, err, want)
		}
	})
	t.Run("MarkAnswered", func(t *testing.T) {
		store, root, captured := setup(t)
		rename(t, root, captured)
		answered, err := store.MarkAnswered(captured)
		want := filepath.Join(root, "gmail", "Inbox", "cur", "1700.1_1.host,U=7:2,S")
		want = strings.TrimSuffix(want, ":2,S") + ":2,RS"
		if err != nil || answered != want {
			t.Fatalf("MarkAnswered = %q, %v; want %q", answered, err, want)
		}
	})
	t.Run("Read", func(t *testing.T) {
		store, root, captured := setup(t)
		renamed := rename(t, root, captured)
		content, used, err := store.ReadResolved(captured)
		if err != nil || used != renamed || !strings.Contains(content.Body, "body of Renamed") {
			t.Fatalf("ReadResolved = %q, %q, %v", content.Body, used, err)
		}
		if content, err := store.Read(captured); err != nil || !strings.Contains(content.Body, "body of Renamed") {
			t.Fatalf("Read = %q, %v", content.Body, err)
		}
	})
	t.Run("gone for good keeps the original error", func(t *testing.T) {
		store, _, captured := setup(t)
		if err := os.Remove(captured); err != nil {
			t.Fatal(err)
		}
		if _, err := store.SetUnread(Message{Path: captured}, false); !os.IsNotExist(err) {
			t.Fatalf("SetUnread of a deleted message = %v, want a not-exist error", err)
		}
		if _, err := store.MoveTo(Message{Path: captured}, "@Reply"); err == nil {
			t.Fatal("MoveTo of a deleted message should fail")
		}
	})
}

func TestResolveSearchesNestedFoldersAndIgnoresUID(t *testing.T) {
	root := t.TempDir()
	store := New([]string{root}, 10)
	nested := filepath.Join(root, "gmail", "[Gmail]", "Sent Mail", "cur", "1700.5_1.host,U=99:2,S")
	writeMessage(t, nested, "Sent", "<s@x>")
	// The old path had no UID and sat in the inbox.
	if got := store.Resolve(filepath.Join(root, "gmail", "Inbox", "new", "1700.5_1.host")); got != nested {
		t.Fatalf("Resolve = %q, want %q", got, nested)
	}
	// A longer unique part must not match by prefix.
	if got := store.Resolve(filepath.Join(root, "gmail", "Inbox", "new", "1700.5_1")); got != "" {
		t.Fatalf("Resolve matched a name that only starts with the key: %q", got)
	}
	// Names with glob characters resolve too.
	odd := filepath.Join(root, "gmail", "Inbox", "cur", "17[0].6_1.host:2,S")
	writeMessage(t, odd, "Odd", "<odd@x>")
	if got := store.Resolve(filepath.Join(root, "gmail", "Inbox", "new", "17[0].6_1.host")); got != odd {
		t.Fatalf("Resolve = %q, want %q", got, odd)
	}
}

func TestFindByMessageIDFindsAReplacedFile(t *testing.T) {
	root := t.TempDir()
	store := New([]string{root}, 10)
	trashed := filepath.Join(root, "uni", "Deleted Items", "cur", "1800.9_1.host,U=3:2,S")
	writeMessage(t, trashed, "Deleted by mistake", "<undo-me@example.com>")
	writeMessage(t, filepath.Join(root, "uni", "Deleted Items", "cur", "1800.8_1.host:2,S"), "Other", "<other@example.com>")
	writeMessage(t, filepath.Join(root, "uni", "Inbox", "cur", "1800.7_1.host:2,S"), "Inbox copy", "<undo-me@example.com>")
	if _, err := store.List(t.Context()); err != nil {
		t.Fatal(err)
	}

	// The daemon deleted the local copy and mbsync fetched it under a new name.
	if err := os.Remove(trashed); err != nil {
		t.Fatal(err)
	}
	replaced := filepath.Join(root, "uni", "Deleted Items", "new", "1800.20_1.host,U=11")
	writeMessage(t, replaced, "Deleted by mistake", "<undo-me@example.com>")
	if got := store.Resolve(trashed); got != "" {
		t.Fatalf("Resolve found %q although the unique name changed", got)
	}
	for _, id := range []string{"<undo-me@example.com>", "undo-me@example.com", "  <undo-me@example.com> "} {
		found, ok := store.FindByMessageID("uni", "Trash", id)
		if !ok || found.Path != replaced || found.Subject != "Deleted by mistake" || found.Box != "Trash" || !found.Unread {
			t.Fatalf("FindByMessageID(%q) = %+v, %v", id, found, ok)
		}
	}
	moved, err := store.MoveTo(Message{Path: replaced}, InboxBox)
	if err != nil || filepath.Dir(filepath.Dir(moved)) != filepath.Join(root, "uni", "Inbox") {
		t.Fatalf("undo move = %q, %v", moved, err)
	}
	if found, ok := store.FindByMessageID("uni", InboxBox, "<undo-me@example.com>"); !ok || (found.Path != moved && !strings.Contains(found.Path, "1800.7_1")) {
		t.Fatalf("inbox lookup = %+v, %v", found, ok)
	}
	for _, miss := range [][3]string{
		{"uni", "Trash", "<nobody@example.com>"},
		{"uni", "Trash", ""},
		{"uni", "@Nope", "<undo-me@example.com>"},
		{"other", "Trash", "<undo-me@example.com>"},
		{"uni", "../uni/Inbox", "<undo-me@example.com>"},
	} {
		if found, ok := store.FindByMessageID(miss[0], miss[1], miss[2]); ok {
			t.Fatalf("FindByMessageID(%q) found %+v", miss, found)
		}
	}
}

func TestFindByMessageIDAcceptsAddressNamedAccountsAndUsesTheCache(t *testing.T) {
	root := t.TempDir()
	store := New([]string{root}, 10)
	writeMessage(t, filepath.Join(root, "me@example.org", "@Reply", "cur", "1.a:2,S"), "Reply me", "<m@x>")
	for _, account := range []string{"example", "me@example.org"} {
		if _, ok := store.FindByMessageID(account, "@Reply", "<m@x>"); !ok {
			t.Fatalf("account %q not found", account)
		}
	}
	opened := store.cache.opens.Load()
	if _, ok := store.FindByMessageID("example", "@Reply", "<m@x>"); !ok || store.cache.opens.Load() != opened {
		t.Fatalf("a repeated lookup reopened the file (%d opens, was %d)", store.cache.opens.Load(), opened)
	}
}
