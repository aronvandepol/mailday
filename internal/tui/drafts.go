package tui

// Drafts: messages closed with esc in the composer are .md files in
// ~/.local/share/mailday/drafts. They show as a Drafts box beside Sent, and
// enter reopens one in the composer to finish and send. d moves a draft to
// drafts/.deleted, from where u brings it back.

import (
	"net/mail"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/aronvandepol/mailday/internal/compose"
	"github.com/aronvandepol/mailday/internal/maildir"
	"github.com/aronvandepol/mailday/internal/terminal"
)

const draftsBox = "Drafts"

func isDraft(message maildir.Message) bool {
	return message.Box == draftsBox
}

// draftAccount shows a draft under the account its From address belongs to.
func (m Model) draftAccount(from string) string {
	address, err := mail.ParseAddress(from)
	if err != nil {
		return ""
	}
	for _, identity := range compose.Identities {
		if !strings.EqualFold(identity.Address, address.Address) {
			continue
		}
		for _, label := range identity.Labels {
			if slices.Contains(m.accounts, label) {
				return label
			}
		}
	}
	return ""
}

// loadDrafts reads the saved drafts as list rows, newest first.
func (m Model) loadDrafts() []maildir.Message {
	paths, _ := filepath.Glob(filepath.Join(draftDir(), "*.md"))
	var drafts []maildir.Message
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		draft, err := compose.Parse(string(data), compose.Draft{})
		if err != nil {
			continue
		}
		subject := strings.TrimSpace(draft.Subject)
		if subject == "" {
			subject = "(no subject)"
		}
		snippet := strings.Join(strings.Fields(draft.Body), " ")
		if cut := strings.Index(snippet, "-- "); cut >= 0 {
			snippet = strings.TrimSpace(snippet[:cut])
		}
		drafts = append(drafts, maildir.Message{
			Path: path, Box: draftsBox, Account: m.draftAccount(draft.From),
			Subject: terminal.SanitizeLine(subject), To: terminal.SanitizeLine(draft.To),
			Snippet: terminal.SanitizeLine(snippet), Date: info.ModTime(),
		})
	}
	sort.Slice(drafts, func(i, j int) bool { return drafts[i].Date.After(drafts[j].Date) })
	return drafts
}

// refreshDrafts replaces the Drafts rows and shows or hides the box.
func (m *Model) refreshDrafts() {
	currentBox := m.activeBox()
	kept := m.messages[:0]
	for _, message := range m.messages {
		if !isDraft(message) {
			kept = append(kept, message)
		}
	}
	drafts := m.loadDrafts()
	m.messages = append(kept, drafts...)
	boxes := slices.DeleteFunc(slices.Clone(m.boxes), func(box string) bool { return box == draftsBox })
	if len(boxes) == 0 {
		boxes = []string{maildir.InboxBox}
	}
	if len(drafts) > 0 {
		at := slices.Index(boxes, maildir.SentBox)
		if at < 0 {
			at = len(boxes)
		}
		boxes = slices.Insert(boxes, at, draftsBox)
	}
	m.boxes = boxes
	m.mailBox = max(slices.Index(m.boxes, currentBox), 0)
	m.boundCursors()
}

// openDraft reopens a saved draft in the composer. A forward gets its
// original's attachments back from the Maildir file it named.
func (m Model) openDraft(message maildir.Message) (tea.Model, tea.Cmd) {
	data, err := os.ReadFile(message.Path)
	if err != nil {
		m.status = "Draft unreadable: " + terminal.SanitizeLine(err.Error())
		return m, nil
	}
	draft, err := compose.Parse(string(data), compose.Draft{})
	if err != nil {
		m.status = terminal.SanitizeLine(err.Error())
		return m, nil
	}
	if draft.AnswersPath != "" {
		if found := m.mailStore.Resolve(draft.AnswersPath); found != "" {
			draft.AnswersPath = found
		}
	}
	if draft.ForwardOf != "" {
		if found := m.mailStore.Resolve(draft.ForwardOf); found != "" {
			draft.ForwardOf = found
			if attachments, err := m.mailStore.Attachments(found); err == nil {
				draft.Forwarded = attachments
			}
		} else {
			m.status = "The forwarded message is gone, so its attachments are not included"
		}
	}
	title := "Draft"
	switch {
	case draft.InReplyTo != "":
		title = "Reply (draft)"
	case draft.ForwardOf != "":
		title = "Forward (draft)"
	}
	status := m.status
	updated, cmd := m.openComposer(title, draft, message.Path, strings.TrimSpace(draft.To) != "")
	if status != "" {
		model := updated.(Model)
		model.status = status
		return model, cmd
	}
	return updated, cmd
}

func deletedDraftDir() string {
	return filepath.Join(draftDir(), ".deleted")
}

// deleteDraft moves a draft aside so u can bring it back.
func (m Model) deleteDraft(message maildir.Message) (tea.Model, tea.Cmd) {
	if err := os.MkdirAll(deletedDraftDir(), 0o700); err != nil {
		m.status = terminal.SanitizeLine(err.Error())
		return m, nil
	}
	target := filepath.Join(deletedDraftDir(), filepath.Base(message.Path))
	if err := os.Rename(message.Path, target); err != nil {
		m.status = terminal.SanitizeLine(err.Error())
		return m, nil
	}
	m.lastMove = &lastMove{path: target, fromBox: draftsBox, toBox: ".deleted", at: m.now()}
	m.refreshDrafts()
	m.screen = screenHome
	m.status = "Draft deleted · u undoes"
	return m, nil
}

func (m Model) undoDraftDelete(ref lastMove) (tea.Model, tea.Cmd) {
	m.lastMove = nil
	if err := os.Rename(ref.path, filepath.Join(draftDir(), filepath.Base(ref.path))); err != nil {
		m.status = "Undo failed: " + terminal.SanitizeLine(err.Error())
		return m, nil
	}
	m.refreshDrafts()
	m.status = "Draft restored"
	return m, nil
}

// goShortcuts are the system boxes for g; the tags use their f letters, so
// g r opens Reply just as f r files to it. Drafts and Sent take capitals
// because d and s already belong to Admin and Grants.
var goShortcuts = map[string]string{"i": maildir.InboxBox, "D": draftsBox, "S": maildir.SentBox, "x": maildir.ArchiveBox}

// goToBox opens the box behind the key pressed after g; g g goes to the top.
// goKey is the letter that, after g, opens box: what goToBox would match.
// "p p" for the box whose letter p is taken by g p (the project picker).
func (m Model) goKey(box string) string {
	for key, target := range goShortcuts {
		if target == box {
			return key
		}
	}
	if letter, ok := fixedShortcuts()[box]; ok && box != maildir.InboxBox && box != maildir.ArchiveBox {
		if _, taken := goShortcuts[letter]; !taken {
			return goKeyShown(letter)
		}
	}
	keys := fileShortcuts(m.boxes)
	if index := slices.Index(m.boxes, box); index >= 0 && index < len(keys) && keys[index] != "" {
		return goKeyShown(keys[index])
	}
	return ""
}

func goKeyShown(letter string) string {
	if letter == "p" {
		return "p p"
	}
	return letter
}

func (m Model) goToBox(key string) (tea.Model, tea.Cmd) {
	m.status = ""
	if key == "g" {
		m.setCursor(0)
		return m, nil
	}
	target, ok := goShortcuts[key]
	if !ok {
		for box, letter := range fixedShortcuts() {
			if letter == key && box != maildir.InboxBox && box != maildir.ArchiveBox {
				target, ok = box, true
				break
			}
		}
	}
	if !ok {
		keys := fileShortcuts(m.boxes)
		if index := slices.Index(keys, key); index >= 0 {
			target, ok = m.boxes[index], true
		}
	}
	index := slices.Index(m.boxes, target)
	if !ok || index < 0 {
		if target == draftsBox {
			m.status = "No drafts"
		}
		return m, nil
	}
	m.mailBox = index
	m.mailCursor = 0
	m.query = ""
	return m, nil
}
