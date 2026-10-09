package maildir

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeSynthetic fills dir with count small messages named like mbsync's, the
// newest having the highest number.
func writeSynthetic(tb testing.TB, dir string, count int, base int64) {
	tb.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		tb.Fatal(err)
	}
	for index := range count {
		stamp := base + int64(index)
		date := time.Unix(stamp, 0).UTC().Format(time.RFC1123Z)
		raw := fmt.Sprintf("From: Sender <s@example.com>\r\nSubject: Message %d\r\nDate: %s\r\nMessage-ID: <%d@example.com>\r\n\r\nBody of message %d\r\n", index, date, stamp, index)
		name := fmt.Sprintf("%d.%d_1.host,U=%d:2,S", stamp, index, index+1)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(raw), 0o600); err != nil {
			tb.Fatal(err)
		}
	}
}

func TestListDoesNotReopenUnchangedFiles(t *testing.T) {
	root := t.TempDir()
	writeSynthetic(t, filepath.Join(root, "uni", "Inbox", "cur"), 20, 1_700_000_000)
	store := New([]string{root}, 100)
	first, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := store.cache.opens.Load(); got != 40 {
		t.Fatalf("first List opened %d files, want 40 (header and preview of 20 messages)", got)
	}
	second, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := store.cache.opens.Load(); got != 40 {
		t.Fatalf("second List opened %d more files, want none", got-40)
	}
	if len(first.Messages) != len(second.Messages) {
		t.Fatalf("lists differ: %d and %d messages", len(first.Messages), len(second.Messages))
	}
	for index := range first.Messages {
		if first.Messages[index] != second.Messages[index] {
			t.Fatalf("message %d differs:\n%+v\n%+v", index, first.Messages[index], second.Messages[index])
		}
	}
	if second.Messages[0].Snippet == "" {
		t.Fatal("cached messages lost their snippet")
	}
}

func TestListCacheFollowsChangesRenamesAndDeletions(t *testing.T) {
	root := t.TempDir()
	cur := filepath.Join(root, "uni", "Inbox", "cur")
	writeSynthetic(t, cur, 3, 1_700_000_000)
	store := New([]string{root}, 100)
	if _, err := store.List(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := store.cache.size(); got != 3 {
		t.Fatalf("cache holds %d entries, want 3", got)
	}
	entries, _ := os.ReadDir(cur)
	changed := filepath.Join(cur, entries[0].Name())
	renamed := filepath.Join(cur, entries[1].Name())
	gone := filepath.Join(cur, entries[2].Name())
	if err := os.WriteFile(changed, []byte("From: x@y.z\r\nSubject: Rewritten subject\r\nDate: Tue, 25 Aug 2026 10:00:00 +0200\r\n\r\nnew body that is longer\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(renamed, renamed[:len(renamed)-1]); err != nil { // drops the S flag: unread again
		t.Fatal(err)
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	result, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(result.Messages))
	}
	seen := map[string]Message{}
	for _, message := range result.Messages {
		seen[message.Path] = message
	}
	if got := seen[changed]; got.Subject != "Rewritten subject" || got.Snippet != "new body that is longer" {
		t.Fatalf("changed file served stale: %+v", got)
	}
	if got := seen[renamed[:len(renamed)-1]]; !got.Unread {
		t.Fatalf("renamed file kept its old flags: %+v", got)
	}
	if got := store.cache.size(); got != 2 {
		t.Fatalf("cache holds %d entries after a rename and a deletion, want 2", got)
	}
}

func TestLimitedBoxesParseOnlyTheNewestFiles(t *testing.T) {
	root := t.TempDir()
	writeSynthetic(t, filepath.Join(root, "gmail", "Inbox", "cur"), 1, 1_700_000_000)
	writeSynthetic(t, filepath.Join(root, "gmail", "Archive", "cur"), 1000, 1_600_000_000)
	store := New([]string{root}, 500)
	result, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	archived := 0
	var newest Message
	for _, message := range result.Messages {
		if message.Box == ArchiveBox {
			archived++
			if message.Date.After(newest.Date) {
				newest = message
			}
		}
	}
	if archived != archiveLimit {
		t.Fatalf("archive shows %d messages, want %d", archived, archiveLimit)
	}
	if newest.Subject != "Message 999" {
		t.Fatalf("newest archived message = %q", newest.Subject)
	}
	// Headers of 2*archiveLimit candidates, the inbox message, then previews
	// of the 301 messages shown.
	if got, want := store.cache.opens.Load(), int64(2*archiveLimit+1+archiveLimit+1); got != want {
		t.Fatalf("opened %d files for a 1000-file Archive, want %d", got, want)
	}
}

func BenchmarkList(b *testing.B) {
	root := b.TempDir()
	writeSynthetic(b, filepath.Join(root, "gmail", "Inbox", "cur"), 1000, 1_700_000_000)
	writeSynthetic(b, filepath.Join(root, "gmail", "@Reply", "cur"), 1000, 1_700_100_000)
	writeSynthetic(b, filepath.Join(root, "gmail", "Archive", "cur"), 3000, 1_600_000_000)
	b.Run("cold", func(b *testing.B) {
		for range b.N {
			if _, err := New([]string{root}, 500).List(context.Background()); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("warm", func(b *testing.B) {
		store := New([]string{root}, 500)
		if _, err := store.List(context.Background()); err != nil {
			b.Fatal(err)
		}
		b.ResetTimer()
		for range b.N {
			if _, err := store.List(context.Background()); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func (c *headerCache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
