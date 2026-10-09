package tui

// Theme watching is adapted from basecamp/hey-cli/internal/tui/theme_watch.go under the MIT license.

import (
	"os"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/fsnotify/fsnotify"
)

type themeChangedMsg struct{}

const themeSettleDelay = 250 * time.Millisecond

func themeWatchDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	directory := filepath.Join(home, ".local", "state", "omarchy", "current")
	if info, err := os.Stat(directory); err != nil || !info.IsDir() {
		return ""
	}
	return directory
}

func watchThemeCmd(directory string) tea.Cmd {
	if directory == "" {
		return nil
	}
	return func() tea.Msg {
		watcher, err := fsnotify.NewWatcher()
		if err != nil {
			return nil
		}
		defer watcher.Close()
		if err := watcher.Add(directory); err != nil {
			return nil
		}
		_ = watcher.Add(filepath.Join(directory, "theme"))
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return nil
				}
				if event.Has(fsnotify.Create) || event.Has(fsnotify.Rename) || event.Has(fsnotify.Write) || event.Has(fsnotify.Remove) {
					time.Sleep(themeSettleDelay)
					return themeChangedMsg{}
				}
			case _, ok := <-watcher.Errors:
				if !ok {
					return nil
				}
			}
		}
	}
}
