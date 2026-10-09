package contacts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, path, headers string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(headers+"\r\n\r\nbody\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestBuildRanksPeopleYouWriteToAndSkipsListsAndNoReply(t *testing.T) {
	root := t.TempDir()
	date := time.Now().Add(-24 * time.Hour).Format(time.RFC1123Z)
	write(t, filepath.Join(root, "uni", "Sent", "cur", "1:2,S"), "From: me@campus.example\r\nTo: \"Dekker, S.C. (Stijn)\" <s.c.dekker@hum.uni.example>, info@society.example\r\nDate: "+date)
	write(t, filepath.Join(root, "uni", "Sent", "cur", "2:2,S"), "From: me@campus.example\r\nTo: s.c.dekker@hum.uni.example\r\nDate: "+date)
	write(t, filepath.Join(root, "uni", "Inbox", "cur", "3:2,S"), "From: Jolien Hendriks <j.j.hendriks@luc.uni.example>\r\nTo: me@campus.example\r\nDate: "+date)
	write(t, filepath.Join(root, "uni", "Inbox", "cur", "4:2,S"), "From: Newsletter <list@lists.example>\r\nList-Id: <x.lists.example>\r\nDate: "+date)
	write(t, filepath.Join(root, "gmail", "@Admin", "new", "5"), "From: Shop <noreply@shop.example>\r\nDate: "+date)
	write(t, filepath.Join(root, "gmail", "Inbox", "cur", "6:2,S"), "From: Me <me@campus.example>\r\nDate: "+date)

	book := Build([]string{root}, func(address string) bool { return address == "me@campus.example" })
	var addresses []string
	for _, contact := range book.Contacts {
		addresses = append(addresses, contact.Address)
	}
	if got := strings.Join(addresses, ","); got != "s.c.dekker@hum.uni.example,info@society.example,j.j.hendriks@luc.uni.example" {
		t.Fatalf("contacts = %s", got)
	}
	if book.Contacts[0].Name != "Stijn Dekker" || book.Contacts[0].Sent != 2 {
		t.Fatalf("top contact = %+v", book.Contacts[0])
	}
	if found := book.Search("joli", 5, nil); len(found) != 1 || found[0].String() != `"Jolien Hendriks" <j.j.hendriks@luc.uni.example>` {
		t.Fatalf("search joli = %+v", found)
	}
	if found := book.Search("uni stijn", 5, nil); len(found) != 1 {
		t.Fatalf("every word must match: %+v", found)
	}
	if found := book.Search("dekker", 5, map[string]bool{"s.c.dekker@hum.uni.example": true}); len(found) != 0 {
		t.Fatalf("excluded address came back: %+v", found)
	}
}
