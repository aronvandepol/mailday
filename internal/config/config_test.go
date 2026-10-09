package config

import (
	"os"
	"path/filepath"
	"testing"
)

// The example in the repository root must stay a valid config.
func TestExampleConfigParses(t *testing.T) {
	c, err := Read(filepath.Join("..", "..", "config.example.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Identity) != 2 || c.Identity[1].Name != "Alex Morgan" || c.Exchange.Calendar != "Outlook" || c.Mail.BoxKeys["@Clients"] != "c" || len(c.Calendar.Feeds) != 1 {
		t.Fatalf("example read as %+v", c)
	}
}

func TestMissingFileIsEmptyAndBrokenFileIsAnError(t *testing.T) {
	dir := t.TempDir()
	if c, err := Read(filepath.Join(dir, "none.toml")); err != nil || len(c.Identity) != 0 {
		t.Fatalf("missing: %+v %v", c, err)
	}
	broken := filepath.Join(dir, "broken.toml")
	os.WriteFile(broken, []byte("[[identity]]\nname = \"x\"\n"), 0o600)
	if _, err := Read(broken); err == nil {
		t.Fatal("an identity without an address should be refused")
	}
}

func TestGetRereadsTheFileWhenItChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	t.Setenv("MAILDAY_CONFIG", path)
	os.WriteFile(path, []byte("name = \"One\"\n"), 0o600)
	if Get().Name != "One" {
		t.Fatalf("first read: %q", Get().Name)
	}
	os.WriteFile(path, []byte("name = \"Second\"\n"), 0o600)
	if Get().Name != "Second" {
		t.Fatalf("after a change: %q", Get().Name)
	}
}
