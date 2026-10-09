package contacts

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeMail(t *testing.T, dir string, index int, header string) {
	t.Helper()
	os.MkdirAll(filepath.Join(dir, "cur"), 0o700)
	if err := os.WriteFile(filepath.Join(dir, "cur", fmt.Sprintf("%d:2,S", index)), []byte(header+"\r\n\r\nbody\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSuggestGroupsFindsTeamsNotTheirPairs(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	date := now.Add(-48 * time.Hour).Format(time.RFC1123Z)
	sent := filepath.Join(root, "acct", "Sent")
	inbox := filepath.Join(root, "acct", "Inbox")
	team := "Ruben <ruben@campus.example>, Stijn <stijn@campus.example>, Bart <bart@campus.example>, Rana <rana@campus.example>"
	n := 0
	for range 5 { // the KH four, written to whole
		n++
		writeMail(t, sent, n, "From: me@me.nl\r\nTo: "+team+"\r\nSubject: KH Meeting\r\nDate: "+date)
	}
	for range 3 { // a pair of them, now and then
		n++
		writeMail(t, sent, n, "From: me@me.nl\r\nTo: Ruben <ruben@campus.example>, Stijn <stijn@campus.example>\r\nSubject: Re: KH book\r\nDate: "+date)
	}
	for range 3 { // supervisors, writing to you
		n++
		writeMail(t, inbox, n, "From: Anton <anton@campus.example>\r\nTo: me@me.nl, Jet <jet@campus.example>\r\nCc: Hum - Herta Mohr - 2.137 (6 personen) <room@campus.example>\r\nSubject: Supervision\r\nDate: "+date)
	}
	n++
	writeMail(t, sent, n, "From: me@me.nl\r\nTo: Anton <anton@campus.example>, Jet <jet@campus.example>\r\nSubject: Supervision\r\nDate: "+date)
	for range 6 { // a list: never a group
		n++
		writeMail(t, inbox, n, "From: a@list.org\r\nTo: me@me.nl, b@list.org\r\nList-Id: <x.list.org>\r\nSubject: Digest\r\nDate: "+date)
	}
	// Too old to count.
	old := now.AddDate(-4, 0, 0).Format(time.RFC1123Z)
	for range 4 {
		n++
		writeMail(t, sent, n, "From: me@me.nl\r\nTo: x@old.nl, y@old.nl\r\nSubject: Long ago\r\nDate: "+old)
	}

	own := func(address string) bool { return address == "me@me.nl" }
	got := SuggestGroups([]string{root}, own, 10, now)
	var names []string
	for _, suggestion := range got {
		var people []string
		for _, member := range suggestion.Members {
			people = append(people, member.Address)
		}
		names = append(names, suggestion.Name+"="+strings.Join(people, ","))
	}
	want := []string{
		"KH=bart@campus.example,rana@campus.example,ruben@campus.example,stijn@campus.example",
		"Supervision=anton@campus.example,jet@campus.example",
	}
	if strings.Join(names, " | ") != strings.Join(want, " | ") {
		t.Fatalf("suggestions\n got %v\nwant %v", names, want)
	}
	if got[0].Messages != 5 || got[0].Subjects[0] != "KH Meeting" {
		t.Fatalf("KH: %+v", got[0])
	}
}

func TestGroupNameFallsBackToFirstNames(t *testing.T) {
	got := rankSuggestions([]sighting{
		{members: []string{"anouk@l.nl", "rob@l.nl"}, weight: 2, subject: "Boekscanner", names: map[string]string{"anouk@l.nl": "Roos, A. (Anouk)", "rob@l.nl": "Rob Groot"}},
		{members: []string{"anouk@l.nl", "rob@l.nl"}, weight: 2, subject: "Grant", names: map[string]string{}},
		{members: []string{"anouk@l.nl", "rob@l.nl"}, weight: 2, subject: "Lunch", names: map[string]string{}},
	}, 5)
	if len(got) != 1 || got[0].Name != "Anouk & Rob" {
		t.Fatalf("got %+v", got)
	}
}
