//go:build unix

package syncd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListenRefusesWhileAnotherHoldsTheLock(t *testing.T) {
	t.Setenv("MAILDAY_SYNCD_SOCKET", filepath.Join(t.TempDir(), "run", "s.sock"))
	path := SocketPath()
	os.MkdirAll(filepath.Dir(path), 0o700)

	// Another daemon between its lock and its Listen: no socket to dial yet,
	// which is the window the old dial-then-remove check lost the race in.
	other, err := lockFile(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := listen(); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("err = %v, want a clear 'already running' error", err)
	}
	other.Close()

	listener, unlock, err := listen()
	if err != nil {
		t.Fatalf("after the lock was released: %v", err)
	}
	defer listener.Close()
	if _, _, err := listen(); err == nil {
		t.Fatal("a second daemon started while the first runs")
	}
	unlock()
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket %v %v, want mode 0600", info, err)
	}
}
