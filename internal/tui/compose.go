package tui

// Writing mail: c, R, A and F build a Markdown draft, open it in the editor,
// then show a preview that y sends. Drafts live in
// ~/.local/share/mailday/drafts and are deleted only once sent, so closing
// the preview or a failed send never loses text. Also here: d deletes to the
// account's Trash, u undoes the last delete, and ? lists every key.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/aronvandepol/mailday/internal/compose"
	"github.com/aronvandepol/mailday/internal/contacts"
	"github.com/aronvandepol/mailday/internal/htmlutil"
	"github.com/aronvandepol/mailday/internal/maildir"
	"github.com/aronvandepol/mailday/internal/markdown"
	"github.com/aronvandepol/mailday/internal/terminal"
)

type draftEditedMsg struct{ err error }

type draftSentMsg struct {
	to          string
	answeredOld string
	answeredNew string
	replyCase   *compose.ReplyCase // set when the original waits in @Reply
	err         error
}

// replySettledMsg reports the judge's verdict on a reply to an @Reply message.
type replySettledMsg struct {
	oldPath string
	newPath string // the Archive path when the reply settled it
	reason  string
	err     error
}

type messageDeletedMsg struct {
	message maildir.Message // as listed, before the move
	newPath string
	err     error
}

type attachmentsSavedMsg struct {
	paths []string
	err   error
}

func downloadDir() string {
	if dir := os.Getenv("MAILDAY_DOWNLOADS"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Downloads")
}

func collapseHome(path string) string {
	home, _ := os.UserHomeDir()
	if home != "" && strings.HasPrefix(path, home) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

func expandPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	return path
}

func (m Model) saveAttachments() (tea.Model, tea.Cmd) {
	selected, ok := m.selectedMail()
	if !ok || m.refuseDraft(selected) {
		return m, nil
	}
	store := m.mailStore
	return m, func() tea.Msg {
		paths, err := store.SaveAttachments(selected.Path, downloadDir())
		return attachmentsSavedMsg{paths: paths, err: err}
	}
}

func draftDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "mailday", "drafts")
}

func editorCommand(path string) *exec.Cmd {
	command := []string{"nvim"}
	for _, name := range []string{"MAILDAY_EDITOR", "VISUAL", "EDITOR"} {
		if fields := strings.Fields(os.Getenv(name)); len(fields) > 0 {
			command = fields
			break
		}
	}
	return exec.Command(command[0], append(command[1:], path)...)
}

// startCompose prepares a draft of the given kind: "new", "reply", "all" or
// "forward". Replies read the selected message first.
func (m Model) startCompose(kind string) (tea.Model, tea.Cmd) {
	account := ""
	if m.mailAccount >= 0 && m.mailAccount < len(m.accounts) {
		account = m.accounts[m.mailAccount]
	}
	selected, haveSelection := m.selectedMail()
	if kind != "new" && !haveSelection {
		return m, nil
	}
	if kind != "new" && m.refuseDraft(selected) {
		return m, nil
	}
	if kind == "new" {
		return m.openComposer("New message", compose.New(account), "", false)
	}
	m.status = "Reading the message"
	store := m.mailStore
	return m, func() tea.Msg {
		content, err := store.Read(selected.Path)
		if err != nil {
			return composerRequestMsg{err: err}
		}
		if kind == "forward" {
			draft := compose.Forward(selected.Account, content)
			draft.ForwardOf = selected.Path
			if attachments, err := store.Attachments(selected.Path); err == nil {
				draft.Forwarded = attachments
			}
			return composerRequestMsg{title: "Forward", draft: draft}
		}
		title := "Reply"
		if kind == "all" {
			title = "Reply to all"
		}
		return composerRequestMsg{title: title, draft: compose.Reply(selected.Account, selected.Path, content, kind == "all"), focusBody: true}
	}
}

type composerRequestMsg struct {
	title     string
	draft     compose.Draft
	focusBody bool
	err       error
}

func (m Model) handleComposerRequest(message composerRequestMsg) (tea.Model, tea.Cmd) {
	if message.err != nil {
		m.status = "Cannot write: " + terminal.SanitizeLine(message.err.Error())
		return m, nil
	}
	return m.openComposer(message.title, message.draft, "", message.focusBody)
}

// handleDraftEdited brings the composer back after $EDITOR closes.
func (m Model) handleDraftEdited(message draftEditedMsg) (tea.Model, tea.Cmd) {
	base := compose.Draft{}
	if m.draft != nil {
		base = *m.draft
	}
	if message.err != nil {
		m.status = "Editor failed: " + terminal.SanitizeLine(message.err.Error())
		return m.openComposer(m.formTitle, base, m.draftPath, true)
	}
	data, err := os.ReadFile(m.draftPath)
	if err != nil {
		m.status = "Draft unreadable: " + terminal.SanitizeLine(err.Error())
		return m.openComposer(m.formTitle, base, m.draftPath, true)
	}
	draft, err := compose.Parse(string(data), base)
	if err != nil {
		updated, cmd := m.openComposer(m.formTitle, base, m.draftPath, true)
		model := updated.(Model)
		model.status = terminal.SanitizeLine(err.Error()) + ". The draft was not reloaded."
		return model, cmd
	}
	return m.openComposer(m.formTitle, draft, m.draftPath, true)
}

// sendDelay is how long y waits before sending, so u can take it back.
// MAILDAY_SEND_DELAY sets it in seconds; 0 sends at once.
func sendDelay() int {
	if value := os.Getenv("MAILDAY_SEND_DELAY"); value != "" {
		if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
			return seconds
		}
	}
	return 10
}

type sendTickMsg struct{ seq int }

func sendTick(seq int) tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return sendTickMsg{seq: seq} })
}

// handleSendTick counts down; at zero the mail goes.
func (m Model) handleSendTick(message sendTickMsg) (tea.Model, tea.Cmd) {
	if message.seq != m.sendSeq || m.sendCountdown <= 0 || m.draft == nil {
		return m, nil
	}
	m.sendCountdown--
	if m.sendCountdown > 0 {
		m.status = fmt.Sprintf("Sending in %d s · u undoes", m.sendCountdown)
		return m, sendTick(message.seq)
	}
	return m.sendNow()
}

// sendNow sends the previewed draft.
func (m Model) sendNow() (tea.Model, tea.Cmd) {
	identity, err := m.draft.Check()
	if err != nil {
		m.quitting = false
		m.status = "Not sent: " + terminal.SanitizeLine(err.Error())
		return m, nil
	}
	m.sending = true
	m.status = "Sending as " + identity.Address
	draft, path, store := *m.draft, m.draftPath, m.mailStore
	return m, func() tea.Msg {
		sentAt := time.Now()
		raw, _, err := draft.Build(sentAt)
		if err != nil {
			return draftSentMsg{err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		if err := compose.Send(ctx, raw, identity); err != nil {
			return draftSentMsg{err: err}
		}
		result := draftSentMsg{to: draft.To}
		if !identity.SaveSent {
			// Gmail and Exchange file the copy themselves: fetch it so
			// the Sent box shows it without waiting for a sweep.
			pullSent(store, identity.Labels)
		}
		if identity.SaveSent {
			for _, label := range identity.Labels {
				if err := store.SaveSent(label, raw); err == nil {
					break
				}
			}
		}
		if draft.AnswersPath != "" {
			if answered, err := store.MarkAnswered(draft.AnswersPath); err == nil {
				result.answeredOld, result.answeredNew = draft.AnswersPath, answered
				if m.judge != nil && inReplyBox(answered) {
					result.replyCase = replyCaseFor(store, answered, draft)
				}
			}
		}
		_ = os.Remove(path)
		return result
	}
}

func (m Model) handleComposeKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.draft == nil {
		return m, nil
	}
	if m.sendCountdown > 0 {
		// Waiting to send: u (or e, or esc) takes it back.
		switch message.String() {
		case "u", "e", "esc":
			m.sendCountdown = 0
			m.sendSeq++
			m.status = "Not sent · the draft is as you left it"
		}
		return m, nil
	}
	if m.sending {
		return m, nil
	}
	switch message.String() {
	case "y":
		identity, err := m.draft.Check()
		if err != nil {
			m.status = "Not sent: " + terminal.SanitizeLine(err.Error())
			return m, nil
		}
		if warnings := sendWarnings(*m.draft); len(warnings) > 0 {
			if key := warningKey(*m.draft, warnings); m.sendWarned != key {
				m.sendWarned = key
				m.status = strings.Join(warnings, " · ") + " · y again sends anyway"
				return m, nil
			}
		}
		m.sendWarned = ""
		if delay := sendDelay(); delay > 0 {
			m.sendCountdown = delay
			m.sendSeq++
			m.status = fmt.Sprintf("Sending in %d s as %s · u undoes", delay, identity.Address)
			return m, sendTick(m.sendSeq)
		}
		return m.sendNow()
	case "e", "esc":
		m.sendWarned = ""
		return m.openComposer(m.formTitle, *m.draft, m.draftPath, true)
	case "h":
		m.sendWarned = ""
		return m.openComposer(m.formTitle, *m.draft, m.draftPath, false)
	case "q":
		m.sendWarned = ""
		m.screen = screenHome
		m.status = "Draft saved · in the Drafts box"
		m.draft = nil
		m.refreshDrafts()
	case "up", "k":
		if m.detailScroll > 0 {
			m.detailScroll--
		}
	case "down", "j":
		m.detailScroll++
	}
	return m, nil
}

func (m Model) handleDraftSent(message draftSentMsg) (tea.Model, tea.Cmd) {
	m.sending = false
	if message.err != nil {
		m.quitting = false // read the failure, the draft is kept
		m.status = "Not sent: " + terminal.SanitizeLine(message.err.Error()) + ". y retries, e edits."
		return m, nil
	}
	model, command := m.finishSent(message)
	next := model.(Model)
	return next, tea.Batch(command, next.quitIfDone())
}

func (m Model) finishSent(message draftSentMsg) (tea.Model, tea.Cmd) {
	if message.answeredOld != "" {
		for index := range m.messages {
			if m.messages[index].Path == message.answeredOld {
				m.messages[index].Path = message.answeredNew
			}
		}
	}
	m.draft = nil
	m.screen = screenHome
	m.refreshDrafts()
	m.status = "Sent to " + terminal.SanitizeLine(message.to)
	switch {
	}
	var rebuild tea.Cmd
	if len(m.options.MailRoots) > 0 {
		rebuild = rebuildContactsCmd(m.options.MailRoots) // whoever was just written to ranks up
	}
	if message.replyCase == nil {
		return m, rebuild
	}
	m.status += " · checking whether the reply settles it"
	store, judge, path, replyCase := m.mailStore, m.judge, message.answeredNew, *message.replyCase
	return m, tea.Batch(rebuild, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		return settleReply(ctx, store, judge, path, replyCase)
	})
}

func (m Model) handleReplySettled(message replySettledMsg) (tea.Model, tea.Cmd) {
	switch {
	case message.err != nil:
		m.status = "Kept in Reply, the reply could not be judged: " + terminal.SanitizeLine(message.err.Error())
	case message.newPath == "":
		m.status = "Kept in Reply: " + terminal.SanitizeLine(message.reason)
	default:
		for index := range m.messages {
			if m.messages[index].Path == message.oldPath {
				m.recordMove(m.messages[index], message.newPath, maildir.ArchiveBox)
				m.messages[index].Path = message.newPath
				m.messages[index].Box = maildir.ArchiveBox
			}
		}
		m.status = "Archived out of Reply: " + terminal.SanitizeLine(message.reason) + " · u undoes"
		m.boundCursors()
	}
	return m, nil
}

// replyCaseFor gathers what the judge needs about a reply to the message at
// path: what was asked, and what the draft answers.
func replyCaseFor(store MailStore, path string, draft compose.Draft) *compose.ReplyCase {
	content, err := store.Read(path)
	if err != nil {
		return &compose.ReplyCase{Subject: draft.Subject, Replied: compose.ReplyText(draft.Body)}
	}
	var asked []string
	for _, line := range strings.Split(content.Body, "\n") {
		if !strings.HasPrefix(line, ">") {
			asked = append(asked, line)
		}
	}
	return &compose.ReplyCase{
		Subject: content.Subject,
		From:    contacts.FormatAddress(content.FromName, content.FromAddr),
		Asked:   compose.TruncateRunes(strings.TrimSpace(strings.Join(asked, "\n")), 1500),
		Replied: compose.ReplyText(draft.Body),
	}
}

// settleReply moves an @Reply message to Archive when the judge says the
// reply settles it. A holding reply, an unsure verdict or a failed judge
// keeps it in @Reply, because losing mail that still needs an answer costs
// more than one that lingers.
func settleReply(ctx context.Context, store MailStore, judge compose.JudgeFunc, path string, replyCase compose.ReplyCase) replySettledMsg {
	result := replySettledMsg{oldPath: path}
	if judge == nil {
		result.err = fmt.Errorf("no judge configured")
		return result
	}
	verdict, err := judge(ctx, replyCase)
	if err != nil {
		result.err = err
		return result
	}
	result.reason = verdict.Reason
	if !verdict.Settled {
		return result
	}
	archived, err := store.MoveTo(maildir.Message{Path: path}, maildir.ArchiveBox)
	if err != nil {
		result.err = err
		return result
	}
	result.newPath = archived
	return result
}

// inReplyBox reports whether a Maildir file sits in an account's @Reply folder.
func inReplyBox(path string) bool {
	return filepath.Base(filepath.Dir(filepath.Dir(path))) == "@Reply"
}

func (m Model) renderCompose(width, height int) string {
	if m.draft == nil {
		return ""
	}
	column, margin := readerColumn(width)
	pad := strings.Repeat(" ", margin)
	rule := lipgloss.NewStyle().Foreground(colorChrome).Render(strings.Repeat("─", column))
	label := lipgloss.NewStyle().Foreground(colorChrome).Bold(true)
	value := lipgloss.NewStyle().Foreground(colorBright)
	lines := []string{lipgloss.NewStyle().Foreground(colorBright).Bold(true).Render(truncateToWidth(m.draft.Subject, column)), rule}
	for _, field := range [][2]string{{"from", m.draft.From}, {"to", m.draft.To}, {"cc", m.draft.Cc}, {"bcc", m.draft.Bcc}} {
		if strings.TrimSpace(field[1]) == "" {
			continue
		}
		// Long lists wrap under their label, so every recipient can be read.
		for index, line := range wrapText(terminal.SanitizeLine(field[1]), max(column-5, 1)) {
			name := "     "
			if index == 0 {
				name = fmt.Sprintf("%-5s", field[0])
			}
			lines = append(lines, label.Render(name)+value.Render(line))
		}
	}
	if counts := recipientCounts(*m.draft); counts != "" {
		lines = append(lines, styleMuted.Render(counts))
	}
	for _, path := range m.draft.Attach {
		size := "missing"
		if info, err := os.Stat(expandPath(path)); err == nil {
			size = humanSize(info.Size())
		}
		lines = append(lines, fillBetween("📎 "+value.Render(truncateToWidth(collapseHome(expandPath(path)), column-14)), styleMuted.Render(size), column))
	}
	for _, attachment := range m.draft.Forwarded {
		lines = append(lines, fillBetween("📎 "+value.Render(truncateToWidth(attachment.Name, column-26))+styleMuted.Render(" (forwarded)"), styleMuted.Render(humanSize(int64(len(attachment.Data)))), column))
	}
	if _, err := m.draft.Check(); err != nil {
		lines = append(lines, lipgloss.NewStyle().Foreground(colorError).Bold(true).Render("Cannot send yet: "+terminal.SanitizeLine(err.Error())))
	} else {
		for _, warning := range sendWarnings(*m.draft) {
			lines = append(lines, lipgloss.NewStyle().Foreground(colorActive).Bold(true).Render("Check: "+warning))
		}
	}
	lines = append(lines, rule, styleMuted.Render("Preview of the HTML part, as the recipient sees it"), "")
	preview := markdown.Render(htmlutil.ToMarkdown(htmlutil.FromMarkdown(m.draft.Body)), column)
	lines = append(lines, strings.Split(preview, "\n")...)
	for index := range lines {
		lines[index] = pad + lines[index]
	}
	start := min(m.detailScroll, max(len(lines)-height, 0))
	return strings.Join(lines[start:min(start+height, len(lines))], "\n")
}

func (m Model) deleteSelected() (tea.Model, tea.Cmd) {
	if m.focus != paneMail && m.screen != screenMail {
		return m, nil
	}
	selected, ok := m.selectedMail()
	if !ok {
		return m, nil
	}
	if isDraft(selected) {
		return m.deleteDraft(selected)
	}
	store := m.mailStore
	return m, func() tea.Msg {
		newPath, err := store.MoveTo(selected, "Trash")
		return messageDeletedMsg{message: selected, newPath: newPath, err: err}
	}
}

func (m Model) handleDeleted(message messageDeletedMsg) (tea.Model, tea.Cmd) {
	if message.err != nil {
		m.status = terminal.SanitizeLine(message.err.Error())
		return m, nil
	}
	for index := range m.messages {
		if m.messages[index].Path == message.message.Path {
			m.messages = append(m.messages[:index], m.messages[index+1:]...)
			break
		}
	}
	m.recordMove(message.message, message.newPath, "Trash")
	advance := m.leaveOrAdvance(message.message.Path, "Moved to Trash · u undoes")
	m.status = "Moved to Trash · u undoes"
	m.boundCursors()
	return m, advance
}

var helpSections = []struct {
	title    string
	bindings [][2]string
}{
	{"Mail list", [][2]string{
		{"↑↓ j k", "move"}, {"enter", "read"}, {"← → 1-9", "account"}, {"b B", "next or previous box"},
		{"g + key", "go to a box: g D Drafts, g S Sent, g i Inbox, g x Archive, g r Reply, g p project picker, tags as f"}, {"g g", "top of the list"}, {"\\", "show or hide the box sidebar (wide terminals)"},
		{"/", "search every box"}, {"m", "seen or unseen"}, {"f + key", "tag: f r Reply, f w Waiting, f x Archive …"}, {"a a", "archive"},
		{"d", "delete to Trash"}, {"u", "undo the last move: archive, file or delete"},
		{"Z", "snooze until when (tomorrow 9, fri, in 3 days): back in the inbox, unread; u undoes"},
		{"J", "join the meeting in the today line"},
		{"◉ ◌", "in Sent: opened, sent, or only Apple's proxy loaded it"},
		{"s", "sync every account now"}, {"r", "reload"}, {"!", "recent messages and errors"},
		{"P", "recipient groups: write to one, rename, remove people"},
	}},
	{"Writing", [][2]string{
		{"c", "new message"}, {"R", "reply"}, {"A", "reply to all"}, {"F", "forward"},
		{"tab ↑↓", "next field (to, subject, attach, message)"}, {"ctrl+s", "preview"}, {"ctrl+o", "write in $EDITOR"},
		{"esc", "close, draft kept (esc esc in message)"}, {"y", "send, in the preview"}, {"e", "back to writing"},
		{"esc i a o", "message is modal: esc, then i a o types"}, {"dd yy p u .", "delete line, copy, paste, undo, repeat"},
		{"d c y + w iw", "dw c$ yip di( ci\" · counts · v V select"}, {":w ZZ", "save the draft, open the preview"},
		{"/ n * :noh", "search the message, matches lit as you type; :noh clears"}, {"gj gk", "move by screen line"}, {"qa … q @a", "record and play a macro; @@ again"},
		{"group name", "in To, Cc, Bcc: offers the group; tab adds it as one chip ◆ KH (4)"}, {"ctrl+g", "save To, Cc and Bcc as a group"},
		{"backspace ctrl+e", "after a group chip: remove it, or turn it into addresses"},
	}},
	{"Reading a message", [][2]string{
		{"↑↓ pgup pgdn", "scroll"}, {"z", "show quoted history"}, {"i", "everyone on the message, with addresses"}, {"G", "save everyone on it as a recipient group"}, {"R A F", "reply, all, forward"},
		{"f d a a", "file, delete, archive (then the next opens)"}, {"S", "save attachments"},
		{"y ~ x", "answer an invitation"}, {"J c", "join the call, show it in the calendar"}, {"E", "new calendar event from the message"}, {"o", "open in NeoMutt"}, {"esc q", "back"},
	}},
	{"Calendar", [][2]string{
		{"1 2 3", "day, week, agenda"}, {"← →", "previous or next span"}, {"t", "today"}, {"g", "go to a date: 12 nov, volgende week"},
		{"n", "new: Lunch tomorrow 12:30 1h @Atrium #Work"}, {"m", "rebook: fri, 14:00, +1d, 2h, next free"},
		{"< > + -", "move a day or half an hour"}, {"e  N", "edit every field, new in the form"},
		{"←→ in e", "calendar: Google or Exchange (moves it)"}, {"d d", "delete"}, {"u", "undo the last change (10 min)"}, {"a ~ x", "accept, maybe, decline"},
		{"enter J", "event details, join the call"}, {"o", "open the calendar app"},
	}},
	{"Anywhere", [][2]string{{"tab", "switch Mail and Calendar"}, {"?", "this list"}, {"!", "the last 30 messages and errors"}, {"q ctrl+c", "quit"}}},
}

// renderHelp lists every key, in two columns when the terminal is wide
// enough, so the whole list fits on one screen.
func (m Model) renderHelp(width, height int) string {
	key := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
	block := func(sections []int, column int) []string {
		var lines []string
		for _, index := range sections {
			section := helpSections[index]
			lines = append(lines, sectionHeader(section.title, column))
			for _, binding := range section.bindings {
				lines = append(lines, "  "+key.Render(padTo(binding[0], 14))+truncateToWidth(binding[1], column-16))
			}
			lines = append(lines, "")
		}
		return lines
	}
	var lines []string
	if width >= 96 {
		column := min((width-8)/2, 56)
		left, right := block([]int{0, 2}, column), block([]int{1, 3, 4}, column)
		margin := strings.Repeat(" ", max((width-2*column-4)/2, 0))
		for row := 0; row < max(len(left), len(right)); row++ {
			l, r := "", ""
			if row < len(left) {
				l = left[row]
			}
			if row < len(right) {
				r = right[row]
			}
			lines = append(lines, margin+padTo(l, column)+"    "+r)
		}
	} else {
		column, margin := readerColumn(width)
		for _, line := range block([]int{0, 1, 2, 3, 4}, column) {
			lines = append(lines, strings.Repeat(" ", margin)+line)
		}
	}
	footer := "Any key closes this list."
	if len(lines)+1 > height {
		footer = "j k scroll · any other key closes"
	}
	start := min(m.helpScroll, max(len(lines)+1-height, 0))
	lines = append(lines[start:], centerText(styleMuted.Render(footer), width))
	return strings.Join(lines[:min(len(lines), height)], "\n")
}
