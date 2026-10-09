package syncd

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
)

func TestImapFlagsFromMaildir(t *testing.T) {
	cases := []struct {
		name string
		want []imap.Flag
	}{
		{"1.moved:2,S", []imap.Flag{imap.FlagSeen}},
		{"1.moved:2,FRS", []imap.Flag{imap.FlagFlagged, imap.FlagAnswered, imap.FlagSeen}},
		{"1.moved,U=4:2,DS", []imap.Flag{imap.FlagDraft, imap.FlagSeen}},
		{"1.moved:2,T", nil}, // trashed has no IMAP counterpart here
		{"1.moved:2,", nil},
		{"1.moved", nil},
		{"new-and-unread", nil},
	}
	for _, c := range cases {
		if got := imapFlagsFromMaildir(c.name); !slices.Equal(got, c.want) {
			t.Errorf("imapFlagsFromMaildir(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestMaildirKey(t *testing.T) {
	for name, want := range map[string]string{
		"1.moved:2,S":              "1.moved",
		"1.moved":                  "1.moved",
		"1.moved,U=42:2,SR":        "1.moved",
		"1.1.host,U=7,FMD5=ab:2,":  "1.1.host",
		"1.1.host:2,S":             "1.1.host",
		"1.other:2,S":              "1.other",
		"with:colon.host,U=3:2,FS": "with:colon.host",
		"with:colon.host":          "with:colon.host",
	} {
		if got := maildirKey(name); got != want {
			t.Errorf("maildirKey(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestHeaderMessageID(t *testing.T) {
	for in, want := range map[string]string{
		"Message-ID: <a@b>\r\n\r\n":          "<a@b>",
		"message-id:\r\n <folded@b>\r\n\r\n": "<folded@b>",
		"Message-ID: <no-blank-line@b>":      "<no-blank-line@b>",
		"":                                   "",
	} {
		if got := headerMessageID([]byte(in)); got != want {
			t.Errorf("headerMessageID(%q) = %q, want %q", in, got, want)
		}
	}
}

// removeLocalCopy must find the file again when Mailday or mbsync renamed it
// after the move was queued, or the stale copy is uploaded as a duplicate.
func TestRemoveLocalCopy(t *testing.T) {
	setup := func(t *testing.T, files ...string) (folder string) {
		folder = t.TempDir()
		for _, file := range files {
			path := filepath.Join(folder, file)
			os.MkdirAll(filepath.Dir(path), 0o700)
			os.WriteFile(path, nil, 0o600)
		}
		return folder
	}
	exists := func(folder, file string) bool {
		_, err := os.Stat(filepath.Join(folder, file))
		return err == nil
	}

	t.Run("recorded name still there", func(t *testing.T) {
		folder := setup(t, "cur/1.moved:2,S", "cur/2.other:2,S")
		if err := removeLocalCopy(filepath.Join(folder, "cur/1.moved:2,S")); err != nil {
			t.Fatal(err)
		}
		if exists(folder, "cur/1.moved:2,S") || !exists(folder, "cur/2.other:2,S") {
			t.Fatal("removed the wrong files")
		}
	})
	t.Run("flags changed since", func(t *testing.T) {
		folder := setup(t, "cur/1.moved:2,FS", "cur/2.other:2,S")
		if err := removeLocalCopy(filepath.Join(folder, "cur/1.moved:2,S")); err != nil {
			t.Fatal(err)
		}
		if exists(folder, "cur/1.moved:2,FS") || !exists(folder, "cur/2.other:2,S") {
			t.Fatal("did not follow the rename, or removed another message")
		}
	})
	t.Run("mbsync assigned a UID, and it sits in new", func(t *testing.T) {
		folder := setup(t, "new/1.moved,U=9", "cur/1.movedlonger:2,S")
		if err := removeLocalCopy(filepath.Join(folder, "cur/1.moved:2,S")); err != nil {
			t.Fatal(err)
		}
		if exists(folder, "new/1.moved,U=9") || !exists(folder, "cur/1.movedlonger:2,S") {
			t.Fatal("did not follow the UID rename exactly")
		}
	})
	t.Run("already gone", func(t *testing.T) {
		folder := setup(t, "cur/2.other:2,S")
		if err := removeLocalCopy(filepath.Join(folder, "cur/1.moved:2,S")); err != nil {
			t.Fatalf("a missing copy is not an error: %v", err)
		}
	})
}

// fakeMailbox records what moveOnServer does to the server.
type fakeMailbox struct {
	hits       [][]imap.UID // search results, one per call; the last repeats
	searches   int
	id         string
	moveErr    error
	storeErr   error
	calls      []string
	storedWith []imap.Flag
}

func (f *fakeMailbox) search(mailbox, id string) ([]imap.UID, error) {
	f.calls = append(f.calls, "search")
	index := min(f.searches, len(f.hits)-1)
	f.searches++
	return f.hits[index], nil
}

func (f *fakeMailbox) messageID(imap.UID) (string, error) {
	f.calls = append(f.calls, "fetch")
	return f.id, nil
}

func (f *fakeMailbox) addFlags(_ imap.UID, flags []imap.Flag) error {
	f.calls = append(f.calls, "store")
	f.storedWith = flags
	return f.storeErr
}

func (f *fakeMailbox) move(imap.UID, string) error {
	f.calls = append(f.calls, "move")
	return f.moveErr
}

func TestMoveOnServer(t *testing.T) {
	job := moveJob{messageID: "<Move-Me@test>", fromBox: "INBOX", toBox: "Archive", localCopy: "/m/Archive/cur/1.moved:2,SF"}
	cases := []struct {
		name      string
		box       *fakeMailbox
		wantErr   string
		wantCalls string
	}{
		{
			name:      "flags are stored before the move, case and spaces ignored in the ID",
			box:       &fakeMailbox{hits: [][]imap.UID{{7}}, id: "  <move-me@TEST> "},
			wantCalls: "search fetch store move",
		},
		{
			name:      "a hit with another Message-ID is refused before anything changes",
			box:       &fakeMailbox{hits: [][]imap.UID{{7}}, id: "<prefix-move-me@test>"},
			wantErr:   "not",
			wantCalls: "search fetch",
		},
		{
			name:      "no hit",
			box:       &fakeMailbox{hits: [][]imap.UID{{}}},
			wantErr:   "0 messages",
			wantCalls: "search",
		},
		{
			name:      "two hits",
			box:       &fakeMailbox{hits: [][]imap.UID{{7, 8}}},
			wantErr:   "2 messages",
			wantCalls: "search",
		},
		{
			name:      "a failed store falls back to uploading, without moving",
			box:       &fakeMailbox{hits: [][]imap.UID{{7}}, id: "<move-me@test>", storeErr: errors.New("no")},
			wantErr:   "storing flags",
			wantCalls: "search fetch store",
		},
		{
			name:      "move failed, gone from the source and in the target: it happened",
			box:       &fakeMailbox{hits: [][]imap.UID{{7}, {}, {9}}, id: "<move-me@test>", moveErr: errors.New("connection reset")},
			wantCalls: "search fetch store move search search",
		},
		{
			// A pipelined COPY that failed still expunged: not a move.
			name:      "move failed, gone from the source but not in the target",
			box:       &fakeMailbox{hits: [][]imap.UID{{7}, {}, {}}, id: "<move-me@test>", moveErr: errors.New("copy failed")},
			wantErr:   "copy failed",
			wantCalls: "search fetch store move search search",
		},
		{
			name:      "move failed and the source still has it",
			box:       &fakeMailbox{hits: [][]imap.UID{{7}}, id: "<move-me@test>", moveErr: errors.New("no space")},
			wantErr:   "no space",
			wantCalls: "search fetch store move search",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := moveOnServer(c.box, job)
			if c.wantErr == "" && err != nil {
				t.Fatalf("err = %v", err)
			}
			if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) {
				t.Fatalf("err = %v, want it to mention %q", err, c.wantErr)
			}
			if got := strings.Join(c.box.calls, " "); got != c.wantCalls {
				t.Fatalf("calls = %q, want %q", got, c.wantCalls)
			}
		})
	}
	box := &fakeMailbox{hits: [][]imap.UID{{7}}, id: "<move-me@test>"}
	if err := moveOnServer(box, job); err != nil {
		t.Fatal(err)
	}
	if want := []imap.Flag{imap.FlagSeen, imap.FlagFlagged}; !slices.Equal(box.storedWith, want) {
		t.Fatalf("flags stored = %v, want %v", box.storedWith, want)
	}
	unflagged := job
	unflagged.localCopy = "/m/Archive/new/1.moved"
	box = &fakeMailbox{hits: [][]imap.UID{{7}}, id: "<move-me@test>"}
	if err := moveOnServer(box, unflagged); err != nil || strings.Contains(strings.Join(box.calls, " "), "store") {
		t.Fatalf("a copy without flags needs no STORE: err %v calls %v", err, box.calls)
	}
}

func TestLastSyncedCountsOnlyTheDestination(t *testing.T) {
	r := &runner{}
	before := time.Now()
	r.noteSynced([]string{"-q", "gmail-inbox"})
	if r.lastSynced("gmail-archive").After(before) {
		t.Fatal("an inbox sync does not touch Archive")
	}
	r.noteSynced([]string{"-q", "--push", "gmail-tags:@Reply"})
	if !r.lastSynced("gmail-tags:@Reply").After(before) || r.lastSynced("gmail-tags:@Waiting").After(before) {
		t.Fatal("a box of a pattern channel counts only for that box")
	}
	r.noteSynced([]string{"-q", "gmail"}) // a whole channel (the Mac's one per account)
	if !r.lastSynced("gmail:Archive").After(before) {
		t.Fatal("a whole-channel sync covers its boxes")
	}
}
