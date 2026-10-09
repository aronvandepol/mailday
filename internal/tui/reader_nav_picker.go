package tui

// The numbered list O and l show in the reader: attachments to open, links to
// follow. Digits 1-9 pick one, enter takes the highlighted one, esc closes.

import (
	"fmt"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/aronvandepol/mailday/internal/maildir"
	"github.com/aronvandepol/mailday/internal/terminal"
)

// pickerLimit is how many items the digits can reach.
const pickerLimit = 9

type pickerKind uint8

const (
	pickAttachments pickerKind = iota
	pickLinks
)

type pickerItem struct {
	text   string // the file name, or the host of a link
	detail string // size, or the shortened path of a link
	tag    string // "meeting" on a call link
	name   string // attachment file name
	data   []byte // attachment contents, held until it is opened or the list closes
	url    string // link address
}

type openPicker struct {
	kind   pickerKind
	items  []pickerItem
	hidden int // links beyond the ninth
	cursor int
}

// attachmentsListedMsg and linksListedMsg arrive once the work for O and l is
// done off the UI thread.
type attachmentsListedMsg struct {
	path  string
	items []pickerItem
	err   error
}

type linksListedMsg struct {
	path   string
	items  []pickerItem
	hidden int
}

// openedMsg is the answer to handing a file or link to the system opener.
type openedMsg struct {
	what string
	err  error
}

// handleReaderFlowMsg takes the messages of this file; it is the one hook
// update() has for them.
func (m Model) handleReaderFlowMsg(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case attachmentsListedMsg:
		if !m.readerShows(message.path) {
			return m, nil
		}
		if message.err != nil {
			m.status = "Not opened: " + terminal.SanitizeLine(message.err.Error())
			return m, nil
		}
		switch len(message.items) {
		case 0:
			m.status = "No attachments to open"
		case 1:
			m.status = "Opening " + terminal.SanitizeLine(message.items[0].name)
			return m, openAttachmentCmd(message.items[0])
		default:
			m.flow.picker = &openPicker{kind: pickAttachments, items: message.items}
			m.status = ""
		}
	case linksListedMsg:
		if !m.readerShows(message.path) {
			return m, nil
		}
		if len(message.items) == 0 {
			m.status = "No links in this message"
			return m, nil
		}
		m.flow.picker = &openPicker{kind: pickLinks, items: message.items, hidden: message.hidden}
		m.status = ""
	case openedMsg:
		if message.err != nil {
			m.status = "Not opened: " + terminal.SanitizeLine(message.err.Error())
		} else {
			m.status = "Opened " + terminal.SanitizeLine(message.what)
		}
	}
	return m, nil
}

// readerShows says whether the reader is still on the message at path.
func (m Model) readerShows(path string) bool {
	return m.screen == screenMail && m.contentPath != "" && maildir.Key(m.contentPath) == maildir.Key(path)
}

func (m Model) handlePickerKey(key string) (Model, tea.Cmd) {
	picker := m.flow.picker
	switch key {
	case "esc", "q", "backspace", "O", "l":
		m.flow.picker = nil
		m.status = ""
		return m, nil
	case "up", "k":
		picker.cursor = max(picker.cursor-1, 0)
		return m, nil
	case "down", "j":
		picker.cursor = min(picker.cursor+1, len(picker.items)-1)
		return m, nil
	case "enter":
		return m.openPicked(picker.cursor)
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		if number := int(key[0] - '0'); number <= len(picker.items) {
			return m.openPicked(number - 1)
		}
	}
	return m, nil
}

func (m Model) openPicked(index int) (Model, tea.Cmd) {
	picker := m.flow.picker
	item := picker.items[index]
	m.flow.picker = nil // the attachment bytes go with it
	if picker.kind == pickAttachments {
		m.status = "Opening " + terminal.SanitizeLine(item.name)
		return m, openAttachmentCmd(item)
	}
	m.status = "Opening " + item.text
	return m, openLinkCmd(item)
}

// launchOpener hands a file or web address to the system opener (xdg-open, or
// open on macOS) and does not wait for it.
func launchOpener(target string) error {
	command := openCommand()
	path, err := exec.LookPath(command[0])
	if err != nil {
		return err
	}
	cmd := exec.Command(path, append(command[1:], target)...)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }() // reap it
	return nil
}

// renderOpenPicker is the list over the reader's body, or "" when none is open.
func (m Model) renderOpenPicker(width, height int) string {
	picker := m.flow.picker
	if picker == nil {
		return ""
	}
	column, margin := readerColumn(width)
	pad := strings.Repeat(" ", margin)
	title := "Attachments"
	if picker.kind == pickLinks {
		title = "Links"
	}
	cursorMarker, cursorText := cursorStyles()
	number := lipgloss.NewStyle().Foreground(colorActive).Bold(true)
	bright := lipgloss.NewStyle().Foreground(colorBright).Bold(true)
	tag := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
	room := max(column-5, 1) // after "│ 1  "

	lines := []string{pad + hintedSectionHeader(title, "digit or enter opens · esc closes", column), ""}
	for index, item := range picker.items {
		var plain, styled string
		if picker.kind == pickAttachments {
			name := truncateToWidth(item.text, max(room-displayWidth(item.detail)-1, 1))
			plain = fillBetween(name, item.detail, room)
			styled = fillBetween(bright.Render(name), styleMuted.Render(item.detail), room)
		} else {
			plain, styled = item.text, bright.Render(item.text)
			if item.tag != "" {
				plain += " " + item.tag
				styled += " " + tag.Render(item.tag)
			}
			if left := room - displayWidth(plain) - 1; item.detail != "" && left > 3 {
				detail := truncateToWidth(item.detail, left)
				plain += " " + detail
				styled += " " + styleMuted.Render(detail)
			}
		}
		if index == picker.cursor {
			lines = append(lines, pad+cursorMarker.Render("│")+" "+cursorText.Render(fmt.Sprint(index+1)+"  "+plain))
		} else {
			lines = append(lines, pad+"  "+number.Render(fmt.Sprint(index+1))+"  "+styled)
		}
	}
	if picker.hidden > 0 {
		lines = append(lines, "", pad+styleMuted.Render(fmt.Sprintf("%d more links not shown", picker.hidden)))
	}
	return strings.Join(lines[:min(len(lines), height)], "\n")
}
