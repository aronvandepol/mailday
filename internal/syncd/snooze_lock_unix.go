//go:build unix

package syncd

import (
	"errors"
	"os"
	"syscall"
	"time"
)

// lockSnooze waits up to ten seconds for an exclusive flock on path (a
// sibling of the snooze file), so Mailday and the daemon never read-modify-
// write the shared list at the same time on one machine.
func lockSnooze(path string) (func(), error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { file.Close() }, nil // closing releases the lock
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			file.Close()
			return nil, err
		}
		time.Sleep(10 * time.Millisecond)
	}
}
