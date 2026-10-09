package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aronvandepol/mailday/internal/compose"
	"github.com/aronvandepol/mailday/internal/maildir"
)

func replyMaildir(t *testing.T) (*maildir.Store, string) {
	t.Helper()
	root := t.TempDir()
	for _, box := range []string{"Inbox", "@Reply", "Archive"} {
		for _, leaf := range []string{"cur", "new", "tmp"} {
			if err := os.MkdirAll(filepath.Join(root, "uni", box, leaf), 0o700); err != nil {
				t.Fatal(err)
			}
		}
	}
	original := filepath.Join(root, "uni", "@Reply", "cur", "1.abc:2,RS")
	if err := os.WriteFile(original, []byte("Subject: Review?\nFrom: Ed <ed@x.org>\n\nCould you review this by Friday?\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return maildir.New([]string{root}, 100), original
}

func fixedJudge(verdict compose.Verdict, err error, seen *compose.ReplyCase) compose.JudgeFunc {
	return func(_ context.Context, c compose.ReplyCase) (compose.Verdict, error) {
		if seen != nil {
			*seen = c
		}
		return verdict, err
	}
}

func TestSettlingReplyArchivesTheOriginal(t *testing.T) {
	store, original := replyMaildir(t)
	draft := compose.Draft{Subject: "Re: Review?", Body: "Done, comments attached.\n\n-- \nSam\n\nOn Fri, Ed wrote:\n> Could you review this by Friday?\n"}
	replyCase := replyCaseFor(store, original, draft)
	var seen compose.ReplyCase
	result := settleReply(context.Background(), store, fixedJudge(compose.Verdict{Settled: true, Reason: "review delivered"}, nil, &seen), original, *replyCase)
	if result.err != nil || !strings.Contains(result.newPath, filepath.Join("uni", "Archive", "cur")) {
		t.Fatalf("settleReply = %+v, want a move to Archive", result)
	}
	if seen.Replied != "Done, comments attached." || !strings.Contains(seen.Asked, "review this by Friday") {
		t.Fatalf("judge saw replied %q asked %q", seen.Replied, seen.Asked)
	}
	if _, err := os.Stat(original); !os.IsNotExist(err) {
		t.Fatal("original still sits in @Reply")
	}
}

func TestHoldingReplyOrFailedJudgeKeepsTheOriginal(t *testing.T) {
	for name, judge := range map[string]compose.JudgeFunc{
		"holding": fixedJudge(compose.Verdict{Settled: false, Reason: "promises an answer next week"}, nil, nil),
		"failed":  fixedJudge(compose.Verdict{}, errors.New("claude not found"), nil),
		"none":    nil,
	} {
		store, original := replyMaildir(t)
		result := settleReply(context.Background(), store, judge, original, compose.ReplyCase{Replied: "I'll get back to you next week."})
		if result.newPath != "" {
			t.Fatalf("%s: original moved to %q", name, result.newPath)
		}
		if _, err := os.Stat(original); err != nil {
			t.Fatalf("%s: original left @Reply: %v", name, err)
		}
	}
}

func TestSentReplyStatusFollowsTheVerdict(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := "/mail/uni/@Reply/cur/one"
	mailStore := &fakeMailStore{result: maildir.ListResult{Messages: []maildir.Message{{Path: path, Account: "uni", Box: "@Reply", Subject: "Answer me", Date: time.Now()}}, Accounts: []string{"uni"}}}
	model := NewModel(mailStore, &fakeCalendarStore{}, Options{Days: 14})
	model.judge = fixedJudge(compose.Verdict{Settled: false, Reason: "holding reply"}, nil, nil)
	updated, _ := model.Update(mailLoadedMsg{result: mailStore.result})
	model = updated.(Model)

	updated, cmd := model.Update(draftSentMsg{to: "x@y.z", answeredOld: path, answeredNew: path + "R", replyCase: &compose.ReplyCase{Replied: "Will check."}})
	model = updated.(Model)
	if cmd == nil || !strings.Contains(model.status, "checking whether the reply settles it") {
		t.Fatalf("no judge step after the send, status %q", model.status)
	}
	updated, _ = model.Update(cmd())
	model = updated.(Model)
	if model.messages[0].Box != "@Reply" || !strings.Contains(model.status, "Kept in Reply: holding reply") {
		t.Fatalf("box %q status %q, want it kept in Reply", model.messages[0].Box, model.status)
	}

	updated, _ = model.Update(replySettledMsg{oldPath: path + "R", newPath: "/mail/uni/Archive/cur/one", reason: "answered"})
	model = updated.(Model)
	if model.messages[0].Box != maildir.ArchiveBox || !strings.Contains(model.status, "Archived out of Reply: answered") {
		t.Fatalf("box %q status %q, want it archived", model.messages[0].Box, model.status)
	}
}
