package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/aronvandepol/mailday/internal/syncd"
)

func TestMailWatchFollowsNestedMaildirs(t *testing.T) {
	root := t.TempDir()
	makeMaildir(t, filepath.Join(root, "gmail", "Inbox"))
	sent := filepath.Join(root, "gmail", "[Gmail]", "Sent Mail")
	makeMaildir(t, sent)
	l := newLive()
	l.startMailWatch([]string{root})
	time.Sleep(50 * time.Millisecond)
	if l.watchFailures() != 0 {
		t.Fatalf("watch failures = %d", l.watchFailures())
	}

	os.WriteFile(filepath.Join(sent, "cur", "1.x:2,S"), []byte("x"), 0o600)
	waitChange(t, l, "mail in [Gmail]/Sent Mail")

	// A folder created later inside the container is picked up as well.
	later := filepath.Join(root, "gmail", "[Gmail]", "Starred")
	makeMaildir(t, later)
	time.Sleep(300 * time.Millisecond)
	select {
	case <-l.changes:
	case <-time.After(500 * time.Millisecond):
	}
	os.WriteFile(filepath.Join(later, "cur", "2.x:2,S"), []byte("x"), 0o600)
	waitChange(t, l, "mail in a folder created inside [Gmail]")
}

func TestMailWatchCountsFoldersItCannotWatch(t *testing.T) {
	root := t.TempDir()
	makeMaildir(t, filepath.Join(root, "uni", "Inbox"))
	makeMaildir(t, filepath.Join(root, "uni", "@Reply"))
	original := addWatch
	t.Cleanup(func() { addWatch = original })
	addWatch = func(watcher *fsnotify.Watcher, path string) error {
		if strings.Contains(path, "@Reply") {
			return errors.New("too many open files")
		}
		return watcher.Add(path)
	}
	l := newLive()
	l.startMailWatch([]string{root})
	// cur and new of @Reply failed; the rest is watched.
	if got := l.watchFailures(); got != 2 {
		t.Fatalf("watchFailures = %d, want 2", got)
	}
	var none *live
	if none.watchFailures() != 0 {
		t.Fatal("a missing live must report no failures")
	}
}

func TestFooterSaysWhenOnlySomeFoldersAreWatched(t *testing.T) {
	model, _ := readerModel(t)
	model.width, model.height = 120, 30
	if strings.Contains(ansiStrip(model.View().Content), "watching partially") {
		t.Fatal("nothing failed, nothing to say")
	}
	model.live.noteWatch("/mail/uni/@Reply/cur", errors.New("too many open files"))
	if view := ansiStrip(model.View().Content); !strings.Contains(view, "watching partially") {
		t.Fatalf("footer does not warn:\n%s", view)
	}
	// "live" would claim more than is true, so it gives way.
	model.syncStatus = syncd.Status{Accounts: []syncd.AccountStatus{{Name: "a", Watching: []string{"INBOX"}, LastSync: time.Now()}}}
	if got := model.syncLabel(); got != "live" {
		t.Fatalf("setup: syncLabel = %q", got)
	}
	view := ansiStrip(model.View().Content)
	if !strings.Contains(view, "watching partially") || strings.Contains(view, "live") {
		t.Fatalf("footer should say watching partially, not live:\n%s", view)
	}
	model.live.noteWatch("/mail/uni/@Reply/cur", nil)
	if view = ansiStrip(model.View().Content); strings.Contains(view, "watching partially") || !strings.Contains(view, "live") {
		t.Fatalf("watch recovered:\n%s", view)
	}
}
