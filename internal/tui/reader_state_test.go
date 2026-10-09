package tui

import (
	tea "charm.land/bubbletea/v2"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/aronvandepol/mailday/internal/maildir"
)

func TestReaderHeaderComesFromTheOpenedMessageAndGoneShowsABanner(t *testing.T) {
	newest := maildir.Message{Path: "/m/a/Inbox/cur/1702.x:2,S", Account: "a", Subject: "Newest", From: "Nina", Box: maildir.InboxBox, Date: time.Now()}
	open := maildir.Message{Path: "/m/a/Inbox/cur/1701.x:2,S", Account: "a", Subject: "Budget plan", From: "Omar", Box: maildir.InboxBox, Date: time.Now().Add(-time.Hour)}
	older := maildir.Message{Path: "/m/a/Inbox/cur/1700.x:2,S", Account: "a", Subject: "Older neighbour", From: "Olga", Box: maildir.InboxBox, Date: time.Now().Add(-2 * time.Hour)}
	model, store := readerModel(t, newest, open, older)
	model.mailCursor = 1
	updated, _ := model.Update(key("enter"))
	model = updated.(Model)
	updated, _ = model.Update(bodyLoadedMsg{path: open.Path, content: maildir.Content{Body: "The body of the open message."}})
	model = updated.(Model)

	// Archived on the phone: the list loses the message, the cursor now
	// points at a neighbour.
	result := store.result
	result.Messages = []maildir.Message{newest, older}
	updated, _ = model.Update(mailLoadedMsg{result: result, quiet: true})
	model = updated.(Model)
	if !model.readerGone {
		t.Fatal("reader should know its message is gone")
	}
	view := ansi.Strip(model.View().Content)
	if !strings.Contains(view, "Budget plan") || strings.Contains(view, "Older neighbour") {
		t.Fatalf("header should still be the open message:\n%s", view)
	}
	if !strings.Contains(view, "Moved or deleted elsewhere · esc goes back") {
		t.Fatalf("no banner:\n%s", view)
	}
	// The next arrival replaces the status line, but the banner stays.
	model.status = "New: Someone — Something"
	if view = ansi.Strip(model.View().Content); !strings.Contains(view, "esc goes back") {
		t.Fatal("banner went with the status line")
	}

	updated, _ = model.Update(key("esc"))
	model = updated.(Model)
	if model.readerGone || strings.Contains(ansi.Strip(model.View().Content), "esc goes back") {
		t.Fatal("the banner should end with the reader")
	}
}

func TestReaderHeaderFollowsTheMessageThroughRenames(t *testing.T) {
	unread := maildir.Message{Path: "/m/a/Inbox/new/1701.x", Account: "a", Subject: "Budget plan", Box: maildir.InboxBox, Date: time.Now(), Unread: true}
	other := maildir.Message{Path: "/m/a/Inbox/new/1700.x", Account: "a", Subject: "Other", Box: maildir.InboxBox, Date: time.Now().Add(-time.Hour), Unread: true}
	model, _ := readerModel(t, unread, other)
	updated, _ := model.Update(key("enter"))
	model = updated.(Model)
	updated, command := model.Update(bodyLoadedMsg{path: unread.Path, content: maildir.Content{Body: "Text"}})
	model = updated.(Model)
	updated, _ = model.Update(command()) // marked read; the cursor slides to Other
	model = updated.(Model)
	view := ansi.Strip(model.View().Content)
	if !strings.Contains(view, "Budget plan") || model.readerMessage.Unread {
		t.Fatalf("reader lost its message after the mark-read:\n%s", view)
	}
}

func TestBodyArrivingUnderANewNameIsStillShown(t *testing.T) {
	open := maildir.Message{Path: "/m/a/Inbox/cur/1701.x:2,S", Account: "a", Subject: "Open", Box: maildir.InboxBox, Date: time.Now()}
	model, store := readerModel(t, open)
	updated, _ := model.Update(key("enter"))
	model = updated.(Model)
	if !model.loadingBody {
		t.Fatal("setup: body not loading")
	}
	// The reload renames the message while its body is being read.
	renamed := open
	renamed.Path = "/m/a/Inbox/cur/1701.x,U=42:2,FS"
	result := store.result
	result.Messages = []maildir.Message{renamed}
	updated, _ = model.Update(mailLoadedMsg{result: result, quiet: true})
	model = updated.(Model)
	if model.contentPath != renamed.Path {
		t.Fatalf("contentPath = %q", model.contentPath)
	}
	// The answer to the request made under the old name.
	updated, _ = model.Update(bodyLoadedMsg{path: open.Path, usedPath: open.Path, content: maildir.Content{Body: "Hello there"}})
	model = updated.(Model)
	if model.loadingBody || model.content == nil {
		t.Fatalf("Reading… is stuck: loadingBody=%v content=%v", model.loadingBody, model.content != nil)
	}
	if !strings.Contains(ansi.Strip(model.View().Content), "Hello there") {
		t.Fatal("the body is not shown")
	}
	// An answer for another message is still dropped.
	other := model
	other.loadingBody, other.content = true, nil
	updated, _ = other.Update(bodyLoadedMsg{path: "/m/a/Inbox/cur/9999.z:2,S", content: maildir.Content{Body: "stray"}})
	if updated.(Model).content != nil {
		t.Fatal("a body for another message was shown")
	}
}

// resolvingStore reads a message by whatever name it has now, as
// maildir.Store.ReadResolved does.
type resolvingStore struct {
	*fakeMailStore
	current string
}

func (s resolvingStore) ReadResolved(path string) (maildir.Content, string, error) {
	return maildir.Content{Body: "read from " + s.current}, s.current, nil
}

func TestLoadBodyUsesReadResolved(t *testing.T) {
	store := resolvingStore{fakeMailStore: &fakeMailStore{}, current: "/m/a/Inbox/cur/1701.x,U=42:2,FS"}
	for name, wrapped := range map[string]MailStore{"direct": store, "pushing": pushingStore{MailStore: store}} {
		message := loadBodyCmd(wrapped, "/m/a/Inbox/cur/1701.x:2,S")().(bodyLoadedMsg)
		if message.usedPath != store.current || message.content.Body != "read from "+store.current || message.path != "/m/a/Inbox/cur/1701.x:2,S" {
			t.Fatalf("%s: %+v", name, message)
		}
	}
	plain := &fakeMailStore{body: maildir.Content{Body: "plain"}}
	if message := loadBodyCmd(plain, "/m/x")().(bodyLoadedMsg); message.content.Body != "plain" || message.usedPath != "/m/x" {
		t.Fatalf("plain store: %+v", message)
	}
}

func TestBodyCacheIsKeyedOnTheReaderColumnNotTheTerminalWidth(t *testing.T) {
	open := maildir.Message{Path: "/m/a/Inbox/cur/1701.x:2,S", Account: "a", Subject: "Open", Box: maildir.InboxBox, Date: time.Now()}
	model, _ := readerModel(t, open)
	model.width, model.height = 100, 30
	updated, _ := model.Update(key("enter"))
	model = updated.(Model)
	updated, _ = model.Update(bodyLoadedMsg{path: open.Path, content: maildir.Content{Body: "Hello there"}})
	model = updated.(Model)
	if model.bodyCacheColumn != readerMaxWidth || len(model.bodyCache) == 0 {
		t.Fatalf("column %d, %d cached lines", model.bodyCacheColumn, len(model.bodyCache))
	}
	cached := &model.bodyCache[0]

	// Every width from 84 up has the same 80-wide column: no re-render.
	for _, width := range []int{84, 120, 200} {
		updated, _ = model.Update(tea.WindowSizeMsg{Width: width, Height: 30})
		model = updated.(Model)
		if &model.bodyCache[0] != cached {
			t.Fatalf("width %d re-rendered the body", width)
		}
		// The margin still centres it.
		_, margin := readerColumn(width)
		var found bool
		for _, line := range strings.Split(ansi.Strip(model.View().Content), "\n") {
			if strings.Contains(line, "Hello there") {
				found = strings.HasPrefix(line, strings.Repeat(" ", margin)+"Hello") && margin > 0
			}
		}
		if !found {
			t.Fatalf("width %d: body not centred with margin %d", width, margin)
		}
	}

	// A narrower terminal changes the column and re-renders.
	updated, _ = model.Update(tea.WindowSizeMsg{Width: 60, Height: 30})
	model = updated.(Model)
	if model.bodyCacheColumn != 56 || &model.bodyCache[0] == cached {
		t.Fatalf("column %d after narrowing, cache reused=%v", model.bodyCacheColumn, &model.bodyCache[0] == cached)
	}
}
