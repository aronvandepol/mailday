package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/aronvandepol/mailday/internal/maildir"
)

func fakeNotmuch(t *testing.T, script string) {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "notmuch"), []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestSearchCmdReportsNotmuchErrorsAndCappedResults(t *testing.T) {
	store := maildir.New([]string{t.TempDir()}, 10)

	fakeNotmuch(t, "echo 'notmuch search: Syntax error in query' >&2\necho 'second line' >&2\nexit 1")
	message := searchCmd(store, "from:")().(searchResultMsg)
	if message.err == nil || message.status != "Search failed: notmuch search: Syntax error in query" {
		t.Fatalf("error result = %+v", message)
	}

	fakeNotmuch(t, "exit 1")
	message = searchCmd(store, "x")().(searchResultMsg)
	if message.err == nil || !strings.HasPrefix(message.status, "Search failed: exit status 1") {
		t.Fatalf("silent failure status = %q", message.status)
	}

	// notmuch is asked for searchLimit hits; getting that many means "maybe more".
	fakeNotmuch(t, fmt.Sprintf("test \"$3\" = --limit=%d || exit 2\ni=1\nwhile [ $i -le %d ]; do echo /nowhere/a/Inbox/cur/$i; i=$((i+1)); done", searchLimit, searchLimit))
	message = searchCmd(store, "budget")().(searchResultMsg)
	if message.err != nil || !message.capped {
		t.Fatalf("a full answer should be capped: %+v", message)
	}
	fakeNotmuch(t, "echo /nowhere/a/Inbox/cur/1")
	message = searchCmd(store, "budget")().(searchResultMsg)
	if message.err != nil || message.capped {
		t.Fatalf("a short answer is not capped: %+v", message)
	}
}

func TestSearchErrorKeepsThePreviousHitsAndSaysWhy(t *testing.T) {
	loaded := maildir.Message{Path: "/m/a/Inbox/cur/1.x:2,S", Account: "a", Subject: "Budget", Box: maildir.InboxBox, Date: time.Now()}
	archived := maildir.Message{Path: "/m/a/Archive/cur/1500.old:2,S", Account: "a", Subject: "Re: travel", Box: maildir.ArchiveBox, Date: time.Now().AddDate(-2, 0, 0)}
	model, _ := readerModel(t, loaded)
	model.query = "invoice"
	updated, _ := model.Update(searchResultMsg{query: "invoice", messages: []maildir.Message{archived}})
	model = updated.(Model)
	if !model.searchHit(archived) || model.searchStale() {
		t.Fatal("the answer to the current query is not stale")
	}

	// Typing on keeps the old hits, marked stale, until notmuch answers.
	model.query = "invoice from:ada"
	if !model.searchHit(archived) || !model.searchStale() {
		t.Fatalf("hits %v stale %v: the previous hits should stay and be marked stale", model.searchHits, model.searchStale())
	}
	model, _ = model.scheduleSearch()
	if !model.searchHit(archived) || model.searchQuery != "invoice" {
		t.Fatal("scheduling a search must not clear the previous hits")
	}

	// notmuch rejects the half-typed query: say so, keep the hits.
	updated, _ = model.Update(searchResultMsg{query: "invoice from:ada", err: fmt.Errorf("boom"), status: "Search failed: Syntax error"})
	model = updated.(Model)
	if model.status != "Search failed: Syntax error" || !model.searchHit(archived) || model.searchQuery != "invoice" {
		t.Fatalf("status %q hits %v query %q", model.status, model.searchHits, model.searchQuery)
	}

	// An error for a query that was typed over is not shown.
	model.status = ""
	updated, _ = model.Update(searchResultMsg{query: "older", err: fmt.Errorf("boom"), status: "Search failed: old"})
	if updated.(Model).status != "" {
		t.Fatal("a stale error replaced the status")
	}

	// A good answer replaces the hits and ends the stale state.
	updated, _ = model.Update(searchResultMsg{query: "invoice from:ada", messages: []maildir.Message{loaded}})
	model = updated.(Model)
	if model.searchStale() || model.searchHit(archived) || !model.searchHit(loaded) {
		t.Fatalf("hits after the new answer: %v", model.searchHits)
	}
}

func TestSearchSummaryAdmitsAnswersCutOffAtTheLimit(t *testing.T) {
	for _, test := range []struct {
		count  int
		capped bool
		want   string
	}{
		{0, false, "0 messages in all mail"},
		{1, false, "1 message in all mail"},
		{42, false, "42 messages in all mail"},
		{12, true, "300+ messages in all mail"},
		{312, true, "312+ messages in all mail"},
	} {
		if got := searchSummary(test.count, test.capped); got != test.want {
			t.Errorf("searchSummary(%d, %v) = %q, want %q", test.count, test.capped, got, test.want)
		}
	}
}

func TestStaleHitsStayListedWhileTypingAndThePromptSaysSearching(t *testing.T) {
	loaded := maildir.Message{Path: "/m/a/Inbox/cur/1.x:2,S", Account: "a", Subject: "Budget", Box: maildir.InboxBox, Date: time.Now()}
	archived := maildir.Message{Path: "/m/a/Archive/cur/1500.old:2,S", Account: "a", Subject: "Re: travel", Box: maildir.ArchiveBox, Date: time.Now().AddDate(-2, 0, 0)}
	model, _ := readerModel(t, loaded)
	model.query = "invoice"
	updated, _ := model.Update(searchResultMsg{query: "invoice", messages: []maildir.Message{archived}})
	model = updated.(Model)
	model.filtering = true
	if view := ansi.Strip(model.View().Content); strings.Contains(view, "searching…") || !strings.Contains(view, "Re: travel") {
		t.Fatalf("current answer, nothing stale:\n%s", view)
	}

	model.query = "invoice fro" // typing on: the hits answer "invoice"
	if got := len(model.filteredMessageIndexes()); got != 1 {
		t.Fatalf("the earlier hits should stay listed while typing, got %d rows", got)
	}
	view := ansi.Strip(model.View().Content)
	if !strings.Contains(view, "searching…") || !strings.Contains(view, "Re: travel") {
		t.Fatalf("stale search not shown:\n%s", view)
	}
	model.filtering = false
	if view = ansi.Strip(model.View().Content); !strings.Contains(view, "searching…") {
		t.Fatalf("the footer rule should say searching… after enter too:\n%s", view)
	}

	model.query = "invoice"
	if view = ansi.Strip(model.View().Content); strings.Contains(view, "searching…") {
		t.Fatalf("an answered query is not searching:\n%s", view)
	}
}
