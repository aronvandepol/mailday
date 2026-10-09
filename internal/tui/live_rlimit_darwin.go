package tui

import "syscall"

// openFileCap is macOS's OPEN_MAX: setrlimit refuses a higher soft limit even
// when the hard limit reads as unlimited.
const openFileCap = 10240

// raiseOpenFileLimit lifts the soft descriptor limit (256 by default on
// macOS) to the hard limit, because the file watcher holds one descriptor per
// watched file. Best effort: on failure the limit stays as it was.
func raiseOpenFileLimit() {
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &limit); err != nil {
		return
	}
	target := min(limit.Max, openFileCap)
	if limit.Cur >= target {
		return
	}
	limit.Cur = target
	_ = syscall.Setrlimit(syscall.RLIMIT_NOFILE, &limit)
}
