package tui

// T in the reader: every message of the conversation across boxes, found by
// notmuch (Message-ID to thread to files), with the loaded messages as the
// fallback and as a supplement for mail it has not indexed yet.

import (
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/aronvandepol/mailday/internal/maildir"
	"github.com/aronvandepol/mailday/internal/terminal"
)

// conversationLimit caps the files read for one conversation.
const conversationLimit = 200

type conversation struct {
	path    string // the reader's message the list was opened from
	items   []maildir.Message
	cursor  int
	loading bool // notmuch has not answered yet
}

type conversationLoadedMsg struct {
	path  string
	found []maildir.Message
	err   error
}

// lookupStore is the part of maildir.Store that reads messages by file path.
type lookupStore interface {
	Lookup([]string) []maildir.Message
}

// handleThreadReaderKey takes T in the reader, and every key while the
// conversation list is open.
func (m Model) handleThreadReaderKey(key string) (Model, tea.Cmd, bool) {
	if m.screen != screenMail {
		return m, nil, false
	}
	list := m.threads.conversation
	if list == nil {
		if key != "T" || !m.readerReady() {
			return m, nil, false
		}
		model, command := m.openConversation()
		return model, command, true
	}
	switch key {
	case "esc", "q", "backspace", "T":
		m.threads.conversation = nil
	case "up", "k":
		list.cursor = max(list.cursor-1, 0)
	case "down", "j":
		list.cursor = min(list.cursor+1, len(list.items)-1)
	case "enter":
		if list.cursor < 0 || list.cursor >= len(list.items) {
			return m, nil, true
		}
		target := list.items[list.cursor]
		m.threads.conversation = nil
		if maildir.Key(target.Path) == maildir.Key(m.contentPath) {
			return m, nil, true
		}
		return m, m.showMessage(target, ""), true
	}
	return m, nil, true
}

// openConversation lists the loaded messages of the reader's conversation at
// once and asks notmuch for the rest.
func (m Model) openConversation() (Model, tea.Cmd) {
	opened := m.readerMessage
	items := []maildir.Message{opened}
	if index := m.findMessage(m.contentPath); index >= 0 {
		opened = m.messages[index]
		all := make([]int, len(m.messages))
		for position := range all {
			all[position] = position
		}
		for _, group := range threadGroups(m.messages, all) {
			if slices.Contains(group.members, index) {
				items = nil
				for _, member := range group.members {
					items = append(items, m.messages[member])
				}
				break
			}
		}
	}
	list := &conversation{path: m.contentPath, items: items}
	list.settle(m.contentPath)
	m.threads.conversation = list
	lookup, ok := storeAs[lookupStore](m.mailStore)
	ids := messageIDs(opened.MessageID)
	if !ok || len(ids) == 0 || !notmuchAvailable() {
		return m, nil
	}
	list.loading = true
	return m, conversationCmd(lookup, ids[0], m.contentPath)
}

// conversationCmd asks notmuch for the thread of a message and reads the
// files in it.
func conversationCmd(lookup lookupStore, messageID, path string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// The id is quoted so characters Xapian treats as syntax stay literal.
		query := `id:"` + strings.ReplaceAll(messageID, `"`, `""`) + `"`
		output, err := exec.CommandContext(ctx, "notmuch", "search", "--output=threads", "--", query).Output()
		if err != nil {
			return conversationLoadedMsg{path: path, err: err}
		}
		var files []string
		for _, thread := range strings.Fields(string(output)) {
			listed, err := exec.CommandContext(ctx, "notmuch", "search", "--output=files", "--", thread).Output()
			if err != nil {
				return conversationLoadedMsg{path: path, err: err}
			}
			files = append(files, strings.Split(strings.TrimSpace(string(listed)), "\n")...)
		}
		files = slices.DeleteFunc(files, func(file string) bool { return file == "" })
		if len(files) > conversationLimit {
			files = files[:conversationLimit]
		}
		return conversationLoadedMsg{path: path, found: lookup.Lookup(files)}
	}
}

func (m Model) handleConversationLoaded(message conversationLoadedMsg) Model {
	list := m.threads.conversation
	if list == nil || list.path != message.path {
		return m
	}
	list.loading = false
	if message.err != nil {
		m.status = "Conversation: notmuch failed, showing the loaded messages"
		return m
	}
	// The same message is a file in each box it sits in, and a rename (a flag)
	// gives one file two names; the mailbox and the Maildir key tell them apart.
	identity := func(message maildir.Message) string {
		return filepath.Dir(filepath.Dir(message.Path)) + "\x00" + maildir.Key(message.Path)
	}
	known := make(map[string]bool, len(list.items))
	for _, item := range list.items {
		known[identity(item)] = true
	}
	for _, found := range message.found {
		if known[identity(found)] {
			continue
		}
		known[identity(found)] = true
		if index := m.findMessage(found.Path); index >= 0 {
			found = m.messages[index] // current flags, not the index's
		}
		list.items = append(list.items, found)
	}
	list.settle(m.contentPath)
	return m
}

// settle sorts the conversation oldest first and puts the cursor on the
// message at path.
func (list *conversation) settle(path string) {
	sort.SliceStable(list.items, func(i, j int) bool { return list.items[i].Date.Before(list.items[j].Date) })
	list.cursor = 0
	for position, item := range list.items {
		if maildir.Key(item.Path) == maildir.Key(path) {
			list.cursor = position
		}
	}
}

// renderConversation is the box T opens over the reader; width is its outer
// width and height the room it has.
func (m Model) renderConversation(width, height int) string {
	list := m.threads.conversation
	cursorMarker, cursorText := cursorStyles()
	dot := lipgloss.NewStyle().Foreground(colorAlert).Bold(true)
	sender := lipgloss.NewStyle().Foreground(colorBright).Bold(true)
	inner := max(width-4, 10) // inside the border and padding

	title := "Conversation · " + mailCountLabel(len(list.items))
	if list.loading {
		title += " · looking for more"
	}
	lines := []string{hintedSectionHeader(title, "enter opens · esc closes", inner), ""}
	visible := max(height-6, 3)
	first := max(min(list.cursor-visible/2, len(list.items)-visible), 0)
	textWidth := inner - 4 // after "│ ● "
	for position := first; position < min(first+visible, len(list.items)); position++ {
		item := list.items[position]
		name := item.From
		if messageBox(item) == maildir.SentBox {
			name = "to " + m.firstRecipient(item.To)
		}
		name = terminal.SanitizeLine(name)
		detail := boxLabel(messageBox(item)) + " · " + formatListDate(item.Date, m.now())
		name = truncateToWidth(name, max(textWidth-displayWidth(detail)-1, 1))
		if position == list.cursor {
			mark := "  "
			if item.Unread {
				mark = "● "
			}
			lines = append(lines, cursorMarker.Render("│")+" "+cursorText.Render(mark+fillBetween(name, detail, textWidth)))
			continue
		}
		mark := "  "
		if item.Unread {
			mark = dot.Render("●") + " "
		}
		lines = append(lines, "  "+mark+fillBetween(sender.Render(name), styleMuted.Render(detail), textWidth))
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorPrimary).Padding(0, 1)
	return box.Render(strings.Join(lines, "\n"))
}
