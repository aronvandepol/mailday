package tui

import (
	"github.com/aronvandepol/mailday/internal/compose"
	"github.com/aronvandepol/mailday/internal/config"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The expected times in this package's tests are written in Amsterdam time;
// pin the zone so they pass wherever the machine is (Seoul, on the road).
// Tests about other zones set time.Local themselves.
//
// The tests also run sealed off from the machine: PATH holds only a no-op
// mailday-calsync (the calendar write path starts it), HOME is empty and the
// daemon socket does not exist, so no test can reach a real binary, mailbox
// or server. Tests that need a tool put a fake first on this PATH.
func TestMain(m *testing.M) {
	if zone, err := time.LoadLocation("Europe/Amsterdam"); err == nil {
		time.Local = zone
	}
	sealed, err := os.MkdirTemp("", "mailday-tui-test-")
	if err != nil {
		panic(err)
	}
	bin := filepath.Join(sealed, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "mailday-calsync"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		panic(err)
	}
	os.Setenv("PATH", bin)
	os.Setenv("HOME", filepath.Join(sealed, "home"))
	os.Setenv("MAILDAY_SYNCD_SOCKET", filepath.Join(sealed, "none.sock"))
	os.Setenv("XDG_RUNTIME_DIR", filepath.Join(sealed, "run"))
	os.Setenv("MAILDAY_CONFIG", filepath.Join(sealed, "none.toml"))
	config.Set(&testConfig)
	compose.Reload()
	code := m.Run()
	os.RemoveAll(sealed)
	os.Exit(code)
}
