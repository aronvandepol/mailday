//go:build !unix

package syncd

// lockSnooze has no file lock here; snoozeMu still keeps one process honest.
func lockSnooze(string) (func(), error) { return func() {}, nil }
