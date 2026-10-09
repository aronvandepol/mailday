package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aronvandepol/mailday/internal/compose"
	"github.com/aronvandepol/mailday/internal/maildir"
)

func TestFullTextSearchAddsHitsAndForgetsThem(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	os.MkdirAll(bin, 0o700)
	old := filepath.Join(dir, "Mail", "a", "Archive", "cur", "1500.old:2,S")
	os.WriteFile(filepath.Join(bin, "notmuch"), []byte("#!/bin/sh\necho "+old+"\n"), 0o700)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	loaded := maildir.Message{Path: "/m/a/Inbox/cur/1.x:2,S", Account: "a", Subject: "Budget", Box: maildir.InboxBox, Date: time.Now()}
	model, _ := readerModel(t, loaded)
	model = typeInto(t, model, "/")
	model = typeInto(t, model, "invoice")
	if len(model.filteredMessageIndexes()) != 0 {
		t.Fatal("nothing loaded mentions invoice")
	}
	archived := maildir.Message{Path: old, Account: "a", Subject: "Re: travel", Box: maildir.ArchiveBox, Date: time.Now().AddDate(-2, 0, 0)}
	updated, _ := model.Update(searchResultMsg{query: "invoice", messages: []maildir.Message{archived}})
	model = updated.(Model)
	indexes := model.filteredMessageIndexes()
	if len(indexes) != 1 || model.messages[indexes[0]].Path != old {
		t.Fatalf("hits %v", indexes)
	}
	// A stale answer for an older query is ignored.
	updated, _ = model.Update(searchResultMsg{query: "invo", messages: []maildir.Message{loaded}})
	if updated.(Model).searchQuery != "invoice" {
		t.Fatal("stale result replaced the search")
	}
	// Clearing the search takes the hit out of the list again.
	updated, _ = model.Update(key("esc"))
	model = updated.(Model)
	for _, message := range model.messages {
		if strings.Contains(message.Path, "1500.old") {
			t.Fatal("search hit stayed after clearing")
		}
	}
}

func TestUndoSend(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	model := NewModel(&fakeMailStore{}, &fakeCalendarStore{}, Options{})
	model.screen = screenCompose
	draft := compose.Draft{From: "Sam de Vries <s.de.vries@hum.uni.example>", To: "ada@uni.nl", Subject: "Hi", Body: "Hello"}
	model.draft = &draft
	updated, command := model.Update(key("y"))
	model = updated.(Model)
	if model.sendCountdown != 10 || model.sending || command == nil || !strings.Contains(model.status, "u undoes") {
		t.Fatalf("y should count down: %d %v %q", model.sendCountdown, model.sending, model.status)
	}
	seq := model.sendSeq
	updated, _ = model.Update(sendTickMsg{seq: seq})
	model = updated.(Model)
	if model.sendCountdown != 9 || !strings.HasPrefix(model.status, "Sending in 9 s") {
		t.Fatalf("tick: %d %q", model.sendCountdown, model.status)
	}
	updated, _ = model.Update(key("u"))
	model = updated.(Model)
	if model.sendCountdown != 0 || model.sending || model.draft == nil || !strings.HasPrefix(model.status, "Not sent") {
		t.Fatalf("u: %d %v %q", model.sendCountdown, model.sending, model.status)
	}
	// The old timer's ticks do nothing after the undo.
	updated, command = model.Update(sendTickMsg{seq: seq})
	if updated.(Model).sending || command != nil {
		t.Fatal("a stale tick sent the mail")
	}
}

func TestOpenFromNotification(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	wanted := maildir.Message{Path: "/m/a/@Reply/new/9.x", Account: "a", Subject: "Answer me", Box: "@Reply", Date: time.Now(), Unread: true}
	other := maildir.Message{Path: "/m/a/Inbox/cur/1.x:2,S", Account: "a", Subject: "Other", Box: maildir.InboxBox, Date: time.Now()}
	store := &fakeMailStore{result: maildir.ListResult{Messages: []maildir.Message{other, wanted}, Accounts: []string{"a"}, Boxes: []string{maildir.InboxBox, "@Reply"}}}
	model := NewModel(store, &fakeCalendarStore{}, Options{OpenPath: wanted.Path})
	updated, _ := model.Update(mailLoadedMsg{result: store.result})
	model = updated.(Model)
	if model.screen != screenMail || model.contentPath != wanted.Path || model.activeBox() != "@Reply" {
		t.Fatalf("screen %v content %q box %q", model.screen, model.contentPath, model.activeBox())
	}
	if NewModel(store, &fakeCalendarStore{}, Options{StartCalendar: true}).focus != paneAgenda {
		t.Fatal("--calendar should start in the calendar")
	}
}
