package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/aronvandepol/mailday/internal/maildir"
)

// attachmentStore serves decoded attachments for the reader's O key.
type attachmentStore struct {
	*fakeMailStore
	files []maildir.AttachmentData
}

func (s attachmentStore) Attachments(string) ([]maildir.AttachmentData, error) { return s.files, nil }

// fakeOpener puts fake xdg-open and open first on PATH; each appends the
// address or file it was given to the returned log.
func fakeOpener(t *testing.T) string {
	t.Helper()
	bin, log := t.TempDir(), filepath.Join(t.TempDir(), "opened.log")
	script := "#!/bin/sh\nprintf '%s\\n' \"$1\" >> '" + log + "'\n"
	for _, name := range []string{"xdg-open", "open"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

// openedTargets waits for the fake opener to have logged count lines.
func openedTargets(t *testing.T, log string, count int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, _ := os.ReadFile(log)
		lines := strings.Fields(string(data))
		if len(lines) >= count || time.Now().After(deadline) {
			return lines
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func attachmentModel(t *testing.T, files ...maildir.AttachmentData) Model {
	t.Helper()
	home := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	model, store := readerModel(t, flowMessage("1", true, time.Minute))
	t.Setenv("HOME", home) // after readerModel, which sets its own
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	model.mailStore = attachmentStore{fakeMailStore: store, files: files}
	content := maildir.Content{Body: "See attached."}
	for _, file := range files {
		content.Attachments = append(content.Attachments, maildir.Attachment{Name: file.Name, Type: file.Type, Size: int64(len(file.Data))})
	}
	return openInReader(t, model, content)
}

func TestOListsAttachmentsAndADigitOpensOne(t *testing.T) {
	log := fakeOpener(t)
	model := attachmentModel(t,
		maildir.AttachmentData{Name: "invite.ics", Type: "text/calendar", Data: []byte("BEGIN:VCALENDAR")},
		maildir.AttachmentData{Name: "budget.xlsx", Type: "application/vnd.ms-excel", Data: []byte("sheet")},
		maildir.AttachmentData{Name: "report.pdf", Type: "application/pdf", Data: []byte("%PDF-report")},
	)
	updated, command := model.Update(key("O"))
	if command == nil {
		t.Fatal("O returned no command")
	}
	updated, _ = updated.(Model).Update(command())
	model = updated.(Model)
	if model.flow.picker == nil || len(model.flow.picker.items) != 2 {
		t.Fatalf("picker = %+v: the calendar part should be left out", model.flow.picker)
	}
	view := ansi.Strip(model.View().Content)
	if !strings.Contains(view, "1  budget.xlsx") || !strings.Contains(view, "2  report.pdf") || strings.Contains(view, "invite.ics") {
		t.Fatalf("picker view:\n%s", view)
	}

	updated, command = model.Update(key("2"))
	model = updated.(Model)
	if model.flow.picker != nil {
		t.Fatal("the list should close once a file is picked")
	}
	updated, _ = model.Update(command())
	if got := updated.(Model).status; got != "Opened report.pdf" {
		t.Fatalf("status %q", got)
	}
	opened := openedTargets(t, log, 1)
	if len(opened) != 1 || filepath.Base(opened[0]) != "report.pdf" {
		t.Fatalf("opener got %v", opened)
	}
	data, err := os.ReadFile(opened[0])
	if err != nil || string(data) != "%PDF-report" {
		t.Fatalf("written file: %q, %v", data, err)
	}
	info, _ := os.Stat(opened[0])
	folder, _ := os.Stat(filepath.Dir(opened[0]))
	root, _ := os.Stat(filepath.Dir(filepath.Dir(opened[0])))
	if info.Mode().Perm() != 0o600 || folder.Mode().Perm() != 0o700 || root.Mode().Perm() != 0o700 {
		t.Fatalf("modes: file %v, folder %v, cache %v", info.Mode().Perm(), folder.Mode().Perm(), root.Mode().Perm())
	}
	if want, _ := openCacheDir(); !strings.HasPrefix(opened[0], want+string(filepath.Separator)) || !strings.Contains(want, "mailday") {
		t.Fatalf("%s is outside the cache folder %s", opened[0], want)
	}
}

func TestOOpensASingleAttachmentDirectly(t *testing.T) {
	log := fakeOpener(t)
	model := attachmentModel(t,
		maildir.AttachmentData{Name: "meeting.ics", Type: "application/ics", Data: []byte("x")},
		maildir.AttachmentData{Name: "photo.jpg", Type: "image/jpeg", Data: []byte("jpeg")},
	)
	updated, command := model.Update(key("O"))
	updated, command = updated.(Model).Update(command())
	model = updated.(Model)
	if model.flow.picker != nil || command == nil || model.status != "Opening photo.jpg" {
		t.Fatalf("picker %+v, status %q: one openable attachment should open at once", model.flow.picker, model.status)
	}
	command()
	if opened := openedTargets(t, log, 1); len(opened) != 1 || filepath.Base(opened[0]) != "photo.jpg" {
		t.Fatalf("opener got %v", opened)
	}
}

func TestOWithoutAttachmentsSaysSo(t *testing.T) {
	model := attachmentModel(t)
	updated, command := model.Update(key("O"))
	if command != nil || updated.(Model).status != "This message has no attachments" {
		t.Fatalf("status %q", updated.(Model).status)
	}
}

func TestAttachmentPickerClosesOnEscAndIgnoresOtherDigits(t *testing.T) {
	fakeOpener(t)
	model := attachmentModel(t,
		maildir.AttachmentData{Name: "a.pdf", Type: "application/pdf", Data: []byte("a")},
		maildir.AttachmentData{Name: "b.pdf", Type: "application/pdf", Data: []byte("b")},
	)
	updated, command := model.Update(key("O"))
	updated, _ = updated.(Model).Update(command())
	model = updated.(Model)
	updated, command = model.Update(key("7"))
	if command != nil || updated.(Model).flow.picker == nil {
		t.Fatal("a digit beyond the list should do nothing")
	}
	updated, _ = updated.(Model).Update(key("a")) // would be archive outside the picker
	if updated.(Model).pendingArchive != "" {
		t.Fatal("keys of the reader leaked through the open list")
	}
	updated, _ = updated.(Model).Update(key("esc"))
	model = updated.(Model)
	if model.flow.picker != nil || model.screen != screenMail {
		t.Fatalf("esc should close the list and stay in the reader (picker %v, screen %v)", model.flow.picker, model.screen)
	}
}

func TestWriteOpenFileKeepsNamesInsideItsFolderAndPrunesOldOnes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	root, err := openCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(root, "astale")
	if err := os.MkdirAll(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	path, err := writeOpenFile("../../escape.txt", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "escape.txt" || !strings.HasPrefix(path, root+string(filepath.Separator)) {
		t.Fatalf("path %s escaped %s", path, root)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatal("a folder from two days ago should have been removed")
	}
}
