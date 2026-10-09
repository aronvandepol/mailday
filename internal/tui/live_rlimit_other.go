//go:build !darwin

package tui

// raiseOpenFileLimit does nothing here: inotify does not use a descriptor per
// watched file, and Go raises the soft limit itself at start-up on Linux.
func raiseOpenFileLimit() {}
