package tui

// O in the reader: open an attachment without saving it to ~/Downloads. The
// file is written to a private folder under the user cache and handed to the
// system opener; S still saves them all.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/aronvandepol/mailday/internal/maildir"
)

// openCacheKeep is how long opened files stay in the cache folder.
const openCacheKeep = 24 * time.Hour

// isCalendarPart is an invitation, which the invitation card handles, not O.
func isCalendarPart(attachment maildir.AttachmentData) bool {
	return strings.EqualFold(attachment.Type, "text/calendar") || strings.EqualFold(filepath.Ext(attachment.Name), ".ics")
}

// listAttachments starts O: it reads the message's attachments and either
// opens the only one or lists them.
func (m Model) listAttachments() (Model, tea.Cmd) {
	if !m.readerReady() {
		return m, nil
	}
	if m.content == nil {
		m.status = "Still reading the message"
		return m, nil
	}
	if len(m.content.Attachments) == 0 {
		m.status = "This message has no attachments"
		return m, nil
	}
	store, path := m.mailStore, m.contentPath
	m.status = "Reading attachments"
	return m, func() tea.Msg {
		attachments, err := store.Attachments(path)
		message := attachmentsListedMsg{path: path, err: err}
		for _, attachment := range attachments {
			if isCalendarPart(attachment) {
				continue
			}
			message.items = append(message.items, pickerItem{
				text: attachment.Name, detail: humanSize(int64(len(attachment.Data))),
				name: attachment.Name, data: attachment.Data,
			})
		}
		return message
	}
}

func openAttachmentCmd(item pickerItem) tea.Cmd {
	return func() tea.Msg {
		path, err := writeOpenFile(item.name, item.data)
		if err == nil {
			err = launchOpener(path)
		}
		return openedMsg{what: item.name, err: err}
	}
}

// openCacheDir is where attachments are written to be opened.
func openCacheDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "mailday", "open"), nil
}

// writeOpenFile writes one attachment to a fresh private folder and returns
// its path. The folder keeps the file's own name, which the opener uses to
// pick a program, and is removed a day later by the next open.
// runnable are file types an opener may run rather than show: Mailday opens
// documents, it does not start programs that came by mail.
var runnable = map[string]bool{".command": true, ".terminal": true, ".app": true, ".sh": true, ".bash": true,
	".zsh": true, ".desktop": true, ".exe": true, ".bat": true, ".cmd": true, ".com": true, ".scr": true,
	".jar": true, ".pkg": true, ".dmg": true, ".appimage": true, ".run": true, ".bin": true, ".js": true,
	".vbs": true, ".ps1": true, ".py": true, ".pl": true, ".scpt": true, ".workflow": true}

func writeOpenFile(name string, data []byte) (string, error) {
	if runnable[strings.ToLower(filepath.Ext(name))] {
		return "", fmt.Errorf("%s is a program; save it with S and look at it first", filepath.Base(name))
	}
	root, err := openCacheDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return "", err
	}
	pruneOpenCache(root)
	folder, err := os.MkdirTemp(root, "a")
	if err != nil {
		return "", err
	}
	name = filepath.Base(strings.ReplaceAll(name, `\`, "/"))
	if name == "." || name == ".." || name == string(filepath.Separator) || name == "" {
		name = "attachment"
	}
	target := filepath.Join(folder, name)
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	_, writeErr := file.Write(data)
	return target, errors.Join(writeErr, file.Close())
}

// pruneOpenCache removes folders older than openCacheKeep from the cache,
// ignoring failures: a file still open elsewhere is not worth an error.
func pruneOpenCache(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if info, err := entry.Info(); err == nil && entry.IsDir() && time.Since(info.ModTime()) > openCacheKeep {
			_ = os.RemoveAll(filepath.Join(root, entry.Name()))
		}
	}
}
