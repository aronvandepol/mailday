package compose

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/aronvandepol/mailday/internal/config"
)

// The tests run sealed off from the machine: an empty HOME, so neither the
// user's config.toml nor their msmtp config is read, and the identities are
// the fictional ones in testIdentities. Tests that need a home set their own
// with t.Setenv.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "mailday-compose-test-")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	os.Unsetenv("MAILDAY_READ_RECEIPTS")
	os.Setenv("MAILDAY_CONFIG", filepath.Join(home, "none.toml"))
	config.Set(&testConfig)
	Reload()
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}
