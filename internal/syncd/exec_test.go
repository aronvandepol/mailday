//go:build unix

package syncd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A shell that starts a grandchild holding the output pipe used to keep
// CombinedOutput blocked after the timeout killed only the shell.
func TestNewCommandStopsTheWholeProcessGroup(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "pid")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	command := newCommand(ctx, "/bin/sh", "-c", "sleep 60 & echo $! > "+pidFile+"; wait")
	command.WaitDelay = 2 * time.Second
	started := time.Now()
	_, err := command.CombinedOutput()
	if err == nil {
		t.Fatal("expected the timeout to be an error")
	}
	if took := time.Since(started); took > 5*time.Second {
		t.Fatalf("returned after %s: a grandchild held the command", took)
	}
	data, _ := os.ReadFile(pidFile)
	pid, convErr := strconv.Atoi(strings.TrimSpace(string(data)))
	if convErr != nil {
		t.Fatalf("grandchild pid %q: %v", data, convErr)
	}
	waitFor(t, "the grandchild to be terminated", func() bool {
		return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
	})
}

func TestNewCommandWaitDelayDefault(t *testing.T) {
	if got := newCommand(context.Background(), "true").WaitDelay; got != 10*time.Second {
		t.Fatalf("WaitDelay = %s, want 10s", got)
	}
}
