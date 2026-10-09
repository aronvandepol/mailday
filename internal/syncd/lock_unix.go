//go:build unix

package syncd

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// lockFile takes an exclusive flock on path, creating it if need be. The
// lock lives as long as the returned file stays open (or the process does),
// so a crashed daemon never leaves a stale lock behind.
func lockFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("another mailday-syncd is already running (lock %s)", path)
		}
		return nil, err
	}
	return file, nil
}

// withRestrictiveUmask runs fn with new files private to the user, so the
// socket is never briefly reachable by others, even in a shared directory.
func withRestrictiveUmask(fn func()) {
	previous := syscall.Umask(0o077)
	defer syscall.Umask(previous)
	fn()
}
