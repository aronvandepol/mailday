package groups

import (
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "groups.txt")
	groups, added, created := Add(nil, "ESS board", []mail.Address{{Name: "Kim, Alice", Address: "alice@x.org"}, {Address: "bob@y.org"}, {Address: "ALICE@x.org"}})
	if added != 2 || !created {
		t.Fatalf("added %d created %v", added, created)
	}
	groups, added, created = Add(groups, "ess BOARD ", []mail.Address{{Address: "bob@y.org"}, {Address: "carol@z.org"}})
	if added != 1 || created || len(groups) != 1 {
		t.Fatalf("second add: added %d created %v groups %d", added, created, len(groups))
	}
	groups, _, _ = Add(groups, "Admin", []mail.Address{{Address: "a@b.c"}})
	if err := Save(path, groups); err != nil {
		t.Fatal(err)
	}
	text, _ := os.ReadFile(path)
	if !strings.Contains(string(text), "[Admin]\na@b.c\n\n[ESS board]\n\"Kim, Alice\" <alice@x.org>\nbob@y.org\ncarol@z.org\n") {
		t.Fatalf("file:\n%s", text)
	}
	back, err := Load(path)
	if err != nil || len(back) != 2 || back[1].Name != "ESS board" || len(back[1].Members) != 3 || back[1].Members[0].Name != "Kim, Alice" {
		t.Fatalf("load: %+v %v", back, err)
	}
}

func TestParseNamesTheBadLine(t *testing.T) {
	_, err := Parse(strings.NewReader("[A]\nok@x.org\nnot an address\n"))
	if err == nil || !strings.Contains(err.Error(), "line 3") {
		t.Fatalf("err = %v", err)
	}
	if groups, err := Load(filepath.Join(t.TempDir(), "missing")); err != nil || groups != nil {
		t.Fatalf("missing file: %v %v", groups, err)
	}
}

func TestSearchPutsPrefixMatchesFirst(t *testing.T) {
	groups := []Group{{Name: "University ESS"}, {Name: "ESS board"}, {Name: "Family"}}
	found := Search(groups, "es")
	if len(found) != 2 || found[0].Name != "ESS board" {
		t.Fatalf("found %+v", found)
	}
	if len(Search(groups, "")) != 0 {
		t.Fatal("empty query matched")
	}
}
