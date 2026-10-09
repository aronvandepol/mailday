package tui

// The composer: From, To, Cc, Bcc, Subject and Attach with autocompletion
// (people from the address book that internal/contacts builds out of the mail
// on disk, files from the disk), and the message body in a text area below.
// New mail, replies and forwards all open here. ctrl+s shows the preview that
// y sends; ctrl+o hands the draft to $EDITOR for long writing. The body is
// edited vim-style by internal/vimedit unless MAILDAY_VIM=off.

import (
	"context"
	"fmt"
	"net/mail"
	"os"
	"path/filepath"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/aronvandepol/mailday/internal/compose"
	"github.com/aronvandepol/mailday/internal/contacts"
	"github.com/aronvandepol/mailday/internal/files"
	"github.com/aronvandepol/mailday/internal/groups"
	"github.com/aronvandepol/mailday/internal/terminal"
	"github.com/aronvandepol/mailday/internal/vimedit"
)

const (
	fieldFrom = iota
	fieldTo
	fieldCc
	fieldBcc
	fieldSubject
	fieldAttach
	fieldBody
)

var fieldNames = []string{"from", "to", "cc", "bcc", "subject", "attach", "message"}

type headerForm struct {
	title  string
	draft  compose.Draft
	values [6]string // as typed; from comes from identity
	paths  []string  // file suggestions for the attach field
	hits   []files.Hit
	// searchSeq numbers attach searches, so a slow answer to an old query
	// never replaces the answer to what is typed now.
	searchSeq int
	identity  int
	field     int
	choices   []contacts.Contact
	// groups are the recipient groups chips refer to, kept current by
	// Model.loadGroups; collect() expands a chip to the members listed here.
	groups []groups.Group
	// groupChoices are matching recipient groups, listed before choices;
	// choice counts across both.
	groupChoices []groups.Group
	choice       int
	body         textarea.Model
	// vim edits the body when modal editing is on, nil when MAILDAY_VIM=off.
	// Its buffer is the truth: the text area only draws it (syncBody).
	vim    *vimedit.Editor
	search searchView // the / prompt's scroll, see form_vim.go
	// fromTouched is set once From was chosen by hand; until then the first
	// recipient's organisation picks it (colleagues at your university get your university address).
	fromTouched bool
	// initial is the draft file text the composer opened with, saved the
	// text last written to the draft file, and fromFile says the file
	// existed already. An untouched reply is then not kept as a draft, and
	// autosave writes only what changed.
	initial, saved string
	fromFile       bool
}

// organisation is the last two labels of an address's domain, so
// hum.uni.example and luc.uni.example both give uni.example.
func organisation(address string) string {
	domain := strings.ToLower(address[strings.LastIndex(address, "@")+1:])
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return domain
	}
	return strings.Join(labels[len(labels)-2:], ".")
}

type contactsLoadedMsg struct {
	book  contacts.Book
	fresh bool
}

func loadContactsCmd(roots []string) tea.Cmd {
	return func() tea.Msg {
		if book, ok := contacts.Load(); ok {
			return contactsLoadedMsg{book: book}
		}
		return contactsLoadedMsg{book: contacts.Build(roots, compose.IsOwn), fresh: true}
	}
}

func rebuildContactsCmd(roots []string) tea.Cmd {
	return func() tea.Msg {
		return contactsLoadedMsg{book: contacts.Build(roots, compose.IsOwn), fresh: true}
	}
}

func (m Model) handleContactsLoaded(message contactsLoadedMsg) (tea.Model, tea.Cmd) {
	m.book = message.book
	if message.fresh {
		book := message.book
		return m, func() tea.Msg { _ = contacts.Save(book); return nil }
	}
	if m.book.Stale() {
		return m, rebuildContactsCmd(m.options.MailRoots)
	}
	return m, nil
}

func identityIndex(from string) int {
	if address, err := mail.ParseAddress(from); err == nil {
		for index, identity := range compose.Identities {
			if strings.EqualFold(identity.Address, address.Address) {
				return index
			}
		}
	}
	return 0
}

func newBodyArea(text string) textarea.Model {
	area := textarea.New()
	area.ShowLineNumbers = false
	area.Prompt = ""
	area.Placeholder = "Write in Markdown: **bold**, *italic*, ## heading, - list, [text](link)"
	area.MaxHeight = 0
	area.CharLimit = 0
	styles := area.Styles()
	for _, state := range []*textarea.StyleState{&styles.Focused, &styles.Blurred} {
		state.Base = lipgloss.NewStyle()
		state.CursorLine = lipgloss.NewStyle()
		state.Text = lipgloss.NewStyle().Foreground(colorBright)
		state.Placeholder = styleMuted
		state.EndOfBuffer = lipgloss.NewStyle()
	}
	area.SetStyles(styles)
	area.SetValue(text)
	area.MoveToBegin()
	return area
}

// newHeaderForm fills the composer from a draft. Replies open in the body,
// above the quoted message; everything else starts on To.
func newHeaderForm(title string, draft compose.Draft, focusBody bool) *headerForm {
	form := &headerForm{title: title, draft: draft, identity: identityIndex(draft.From), field: fieldTo, body: newBodyArea(draft.Body)}
	form.values[fieldTo], form.values[fieldCc], form.values[fieldBcc], form.values[fieldSubject] = draft.To, draft.Cc, draft.Bcc, draft.Subject
	if len(draft.Attach) > 0 {
		form.values[fieldAttach] = strings.Join(draft.Attach, ", ") + ", "
	}
	if draft.To != "" && !strings.HasSuffix(strings.TrimSpace(draft.To), ",") {
		form.values[fieldTo] = strings.TrimSpace(draft.To) + ", "
	}
	if draft.To != "" && draft.Subject == "" {
		form.field = fieldSubject
	}
	if vimEnabled() {
		form.vim = vimedit.New(form.body.Value())
	}
	if focusBody {
		form.focus(fieldBody)
	}
	form.initial = form.collect().File()
	form.saved = form.initial
	return form
}

// setIdentity switches From and swaps the signature in the body along with
// it, as long as the old signature is still there unchanged.
func (f *headerForm) setIdentity(index int) {
	if index == f.identity {
		return
	}
	body := f.body.Value()
	for _, short := range []bool{false, true} {
		old := compose.Identities[f.identity].SignatureBlock(short)
		if old != "" && strings.Contains(body, old) {
			f.body.SetValue(strings.Replace(body, old, compose.Identities[index].SignatureBlock(short), 1))
			f.body.MoveToBegin()
			if f.vim != nil {
				f.vim.SetText(f.body.Value())
			}
			break
		}
	}
	f.identity = index
}

func (f *headerForm) focus(field int) {
	entering := field == fieldBody && f.field != fieldBody
	f.field = field
	if field == fieldBody {
		f.body.Focus()
		if f.vim != nil && entering {
			// Coming to the body is coming to write.
			f.vim.StartInsert()
			f.syncBody()
		}
	} else {
		f.body.Blur()
	}
}

// collect turns what is on screen into a draft. Group chips are expanded to
// addresses here, so nothing downstream knows about them.
func (f *headerForm) collect() compose.Draft {
	draft := f.draft
	identity := compose.Identities[f.identity]
	draft.From = (&mail.Address{Name: identity.Name, Address: identity.Address}).String()
	texts, _ := f.resolve() // chips become their addresses (chips.go)
	draft.To, draft.Cc, draft.Bcc = cleanList(texts[0]), cleanList(texts[1]), cleanList(texts[2])
	draft.Subject = strings.TrimSpace(f.values[fieldSubject])
	draft.Attach = compose.SplitPaths(f.values[fieldAttach])
	draft.Body = f.body.Value()
	return draft
}

// empty is a composer nothing was typed into.
func (f *headerForm) empty() bool {
	return strings.TrimSpace(f.values[fieldTo]+f.values[fieldCc]+f.values[fieldBcc]+f.values[fieldSubject]+f.body.Value()) == "" && f.draft.InReplyTo == ""
}

func isRecipientField(field int) bool {
	return field == fieldTo || field == fieldCc || field == fieldBcc
}

// token is the recipient being typed: the text after the last comma.
func (f *headerForm) token() string {
	value := f.values[f.field]
	if index := lastComma(value); index >= 0 {
		value = value[index+1:]
	}
	return strings.TrimSpace(value)
}

// chosen is every address already in To, Cc or Bcc, the members of group
// chips included.
func (f *headerForm) chosen() map[string]bool {
	chosen := map[string]bool{}
	for _, field := range recipientFields {
		value := f.values[field]
		if field == f.field { // only the finished entries before the token being typed
			if index := lastComma(value); index >= 0 {
				value = value[:index]
			} else {
				value = ""
			}
		}
		for _, address := range append(recipientsIn(value), f.chipMembers(value)...) {
			chosen[strings.ToLower(address.Address)] = true
		}
	}
	return chosen
}

func (m *Model) refreshChoices() {
	form := m.form
	form.choices, form.groupChoices, form.choice = nil, nil, 0
	if isRecipientField(form.field) && len(form.token()) >= 1 {
		form.groupChoices = m.groupSuggestions(form.token()) // people.go
		form.choices = m.book.Search(form.token(), 6-len(form.groupChoices), form.chosen())
	}
	if form.field != fieldAttach {
		form.paths, form.hits = nil, nil
		return
	}
	if token := form.token(); isPathToken(token) || token == "" {
		form.paths, form.hits = completePaths(token), nil
	}
	// Word searches keep the last results on screen until the new ones land.
}

// isPathToken is an attach entry typed as a path (~/…, /…, ./…), which
// completes folder by folder. Anything else is searched for by its words.
func isPathToken(token string) bool {
	return strings.HasPrefix(token, "~") || strings.HasPrefix(token, "/") || strings.HasPrefix(token, ".")
}

type fileSearchTickMsg struct {
	seq   int
	query string
}

type fileSearchMsg struct {
	seq  int
	hits []files.Hit
}

// fileSearchCmd starts a word search for the attach field after a short
// pause in typing, so a fast typist triggers one search, not ten.
func (m *Model) fileSearchCmd() tea.Cmd {
	form := m.form
	if form == nil || form.field != fieldAttach {
		return nil
	}
	form.searchSeq++
	query := form.token()
	if isPathToken(query) || len([]rune(query)) < 2 {
		if !isPathToken(query) {
			form.paths, form.hits = nil, nil
		}
		return nil
	}
	seq := form.searchSeq
	return tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return fileSearchTickMsg{seq: seq, query: query} })
}

func (m Model) handleFileSearchTick(message fileSearchTickMsg) (tea.Model, tea.Cmd) {
	if m.form == nil || message.seq != m.form.searchSeq {
		return m, nil
	}
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		home, _ := os.UserHomeDir()
		return fileSearchMsg{seq: message.seq, hits: files.Search(ctx, home, message.query, 8)}
	}
}

func (m Model) handleFileSearch(message fileSearchMsg) (tea.Model, tea.Cmd) {
	form := m.form
	if form == nil || message.seq != form.searchSeq || form.field != fieldAttach {
		return m, nil
	}
	form.hits = message.hits
	form.paths = make([]string, len(message.hits))
	for index, hit := range message.hits {
		form.paths[index] = collapseHome(hit.Path)
	}
	form.choice = 0
	return m, nil
}

// completePaths lists files and folders that start with what was typed. A
// bare name is looked up in the home folder; folders end in "/".
func completePaths(token string) []string {
	if token == "" {
		return nil
	}
	expanded := expandPath(token)
	dir, base := filepath.Split(expanded)
	if dir == "" {
		dir, _ = os.UserHomeDir()
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(strings.ToLower(name), strings.ToLower(base)) || (strings.HasPrefix(name, ".") && !strings.HasPrefix(base, ".")) {
			continue
		}
		path := collapseHome(filepath.Join(dir, name))
		if entry.IsDir() {
			path += "/"
		}
		out = append(out, path)
		if len(out) == 8 {
			break
		}
	}
	return out
}

func (f *headerForm) suggestionCount() int {
	if f.field == fieldAttach {
		return len(f.paths)
	}
	return len(f.groupChoices) + len(f.choices)
}

func (f *headerForm) acceptPath(path string) {
	value := f.values[fieldAttach]
	if index := strings.LastIndex(value, ","); index >= 0 {
		value = value[:index+1] + " "
	} else {
		value = ""
	}
	if strings.HasSuffix(path, "/") {
		f.values[fieldAttach] = value + path
	} else {
		f.values[fieldAttach] = value + path + ", "
	}
}

func (f *headerForm) accept(contact contacts.Contact) {
	value := f.values[f.field]
	if index := lastComma(value); index >= 0 {
		value = value[:index+1] + " "
	} else {
		value = ""
	}
	f.values[f.field] = value + contact.String() + ", "
	if f.fromTouched || f.field != fieldTo {
		return
	}
	for index, identity := range compose.Identities {
		if org := organisation(contact.Address); org != "gmail.com" && org == organisation(identity.Address) {
			f.setIdentity(index)
			return
		}
	}
}

func cleanList(value string) string {
	return strings.Trim(strings.TrimSpace(value), ", ")
}

// openComposer shows the composer for a draft. path is the draft's file, or
// "" for a new one.
func (m Model) openComposer(title string, draft compose.Draft, path string, focusBody bool) (tea.Model, tea.Cmd) {
	m.form = newHeaderForm(title, draft, focusBody)
	m.form.fromFile = path != ""
	m.loadGroups() // people.go: a group made on the other machine is offered too
	if path == "" {
		path = newDraftPath()
	}
	m.draftPath = path
	m.screen = screenHome
	m.status = ""
	m.autosaveSeq++
	commands := []tea.Cmd{autosaveTick(m.autosaveSeq)}
	// The book takes well under a second to rebuild: refresh it so someone
	// who wrote this morning autocompletes now.
	if len(m.options.MailRoots) > 0 && time.Since(m.book.Built) > time.Minute {
		commands = append(commands, rebuildContactsCmd(m.options.MailRoots))
	}
	return m, tea.Batch(commands...)
}

// autosaveEvery is how often an open composer is written to its draft file,
// so a crash, a closed terminal or a stray ctrl+c costs seconds, not the mail.
const autosaveEvery = 20 * time.Second

type autosaveTickMsg struct{ seq int }

func autosaveTick(seq int) tea.Cmd {
	return tea.Tick(autosaveEvery, func(time.Time) tea.Msg { return autosaveTickMsg{seq: seq} })
}

// handleAutosave writes the composer when it changed since the last write.
// A tick from an earlier composer (older seq) ends its chain.
func (m Model) handleAutosave(message autosaveTickMsg) (tea.Model, tea.Cmd) {
	if m.form == nil || message.seq != m.autosaveSeq {
		return m, nil
	}
	form := m.form
	if text := form.collect().File(); text != form.saved && !form.empty() {
		if err := m.saveDraft(form.collect()); err != nil {
			m.status = "Draft not saved: " + terminal.SanitizeLine(err.Error())
		} else {
			form.saved = text
		}
	}
	return m, autosaveTick(message.seq)
}

func newDraftPath() string {
	return filepath.Join(draftDir(), time.Now().Format("2006-01-02T150405.000")+".md")
}

// saveDraft writes the composer to its draft file.
func (m Model) saveDraft(draft compose.Draft) error {
	if err := os.MkdirAll(draftDir(), 0o700); err != nil {
		return err
	}
	return os.WriteFile(m.draftPath, []byte(draft.File()), 0o600)
}

// closeComposer leaves the composer. Anything typed stays as a draft file;
// when the file cannot be written the composer stays open (m.form non-nil).
func (m Model) closeComposer() (tea.Model, tea.Cmd) {
	form := m.form
	m.form = nil
	if form.empty() {
		_ = os.Remove(m.draftPath)
		m.status = "Message discarded"
		m.refreshDrafts()
		return m, nil
	}
	if !form.fromFile && form.collect().File() == form.initial {
		// A reply or forward nobody wrote in: only the quote and signature.
		_ = os.Remove(m.draftPath) // an autosave may have written it
		m.status = "Nothing written · no draft kept"
		m.refreshDrafts()
		return m, nil
	}
	if err := m.saveDraft(form.collect()); err != nil {
		// Stay in the composer: closing now would lose what was written.
		m.form = form
		m.status = "Draft not saved, composer still open: " + terminal.SanitizeLine(err.Error())
		return m, nil
	}
	m.status = "Draft saved · in the Drafts box"
	m.refreshDrafts()
	return m, nil
}

func (m Model) handleFormKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	form := m.form
	key := message.String()
	if form.vim != nil && form.field == fieldBody && key != "ctrl+s" && key != "ctrl+o" && key != "ctrl+g" {
		return m.handleVimKey(message)
	}
	switch key {
	case "esc":
		if form.suggestionCount() > 0 && form.field != fieldBody {
			form.choices, form.groupChoices, form.paths = nil, nil, nil
			return m, nil
		}
		return m.closeComposer()
	case "ctrl+s":
		return m.finishForm()
	case "ctrl+o":
		return m.editInEditor()
	case "ctrl+g":
		return m.startSaveGroup(form.composerRecipients()) // people.go
	}
	if form.field == fieldBody {
		switch key {
		case "shift+tab":
			form.focus(fieldAttach)
			m.refreshChoices()
			return m, nil
		case "tab":
			form.body.InsertString("  ")
			return m, nil
		}
		var cmd tea.Cmd
		form.body, cmd = form.body.Update(message)
		return m, cmd
	}
	switch key {
	case "left", "right":
		if form.field == fieldFrom {
			step := 1
			if key == "left" {
				step = -1
			}
			form.setIdentity((form.identity + step + len(compose.Identities)) % len(compose.Identities))
			form.fromTouched = true
			return m, nil
		}
	case "up", "down":
		if count := form.suggestionCount(); count > 0 {
			step := 1
			if key == "up" {
				step = -1
			}
			form.choice = (form.choice + step + count) % count
			return m, nil
		}
		if key == "up" {
			form.focus(max(form.field-1, fieldFrom))
		} else {
			form.focus(min(form.field+1, fieldBody))
		}
		m.refreshChoices()
		return m, nil
	case "tab", "enter":
		if form.field == fieldAttach && len(form.paths) > 0 {
			form.acceptPath(form.paths[form.choice])
			m.refreshChoices()
			return m, nil
		}
		if form.choice < len(form.groupChoices) {
			group := form.groupChoices[form.choice]
			added, already := form.acceptGroup(group)
			switch {
			case added == 0:
				m.status = "Everyone in " + group.Name + " is there already"
			case already > 0:
				m.status = fmt.Sprintf("%s: %s · %d already there", group.Name, people(added), already)
			default:
				m.status = fmt.Sprintf("%s: %s", group.Name, people(added))
			}
			m.refreshChoices()
			return m, nil
		}
		if index := form.choice - len(form.groupChoices); index >= 0 && index < len(form.choices) {
			form.accept(form.choices[index])
			m.refreshChoices()
			return m, nil
		}
		form.focus(min(form.field+1, fieldBody))
		m.refreshChoices()
		return m, nil
	case "shift+tab":
		form.focus(max(form.field-1, fieldFrom))
		m.refreshChoices()
		return m, nil
	}
	if form.field == fieldFrom {
		return m, nil
	}
	switch key {
	case "backspace":
		if shorter, name, ok := removeChipAtEnd(form.values[form.field]); ok && isRecipientField(form.field) {
			form.values[form.field] = shorter // the whole chip, not a bracket of it
			m.status = "Took " + name + " out"
		} else {
			form.values[form.field] = trimLastRune(form.values[form.field])
		}
	case "ctrl+u":
		form.values[form.field] = ""
	case "ctrl+w":
		form.values[form.field] = deleteWord(form.values[form.field])
	case "ctrl+e":
		if isRecipientField(form.field) {
			if status := form.expandLastChip(); status != "" {
				m.status = status
			}
		}
	default:
		if text := message.Key().Text; text != "" && !message.Key().Mod.Contains(tea.ModCtrl) {
			form.values[form.field] += text
		}
	}
	m.refreshChoices()
	return m, m.fileSearchCmd()
}

// finishForm saves the draft and shows the preview, where y sends it.
func (m Model) finishForm() (tea.Model, tea.Cmd) {
	form := m.form
	m.loadGroups() // chips expand to the group as the file has it now (the other machine may have changed it)
	draft := form.collect()
	if _, err := draft.AttachmentSize(); err != nil {
		m.status = terminal.SanitizeLine(err.Error())
		form.focus(fieldAttach)
		return m, nil
	}
	if name, field, gone := form.missingChip(); gone {
		m.status = "No group " + terminal.SanitizeLine(name) + " any more: expand or remove it"
		form.focus(field)
		return m, nil
	}
	// The collected lists are checked, chips already expanded to addresses.
	for _, check := range []struct {
		field int
		value string
	}{{fieldTo, draft.To}, {fieldCc, draft.Cc}, {fieldBcc, draft.Bcc}} {
		field, value := check.field, check.value
		if value != "" {
			if _, err := mail.ParseAddressList(value); err != nil {
				m.status = fieldNames[field] + ": " + terminal.SanitizeLine(err.Error())
				form.focus(field)
				return m, nil
			}
		}
	}
	if err := m.saveDraft(draft); err != nil {
		m.status = "Draft not saved: " + terminal.SanitizeLine(err.Error())
	}
	m.form = nil
	m.draft = &draft
	m.formTitle = form.title
	m.screen = screenCompose
	m.detailScroll = 0
	return m, nil
}

// editInEditor saves the composer and opens the draft file in $EDITOR, for
// writing at length. Closing the editor brings the composer back.
func (m Model) editInEditor() (tea.Model, tea.Cmd) {
	draft := m.form.collect()
	if err := m.saveDraft(draft); err != nil {
		m.status = "Draft not saved: " + terminal.SanitizeLine(err.Error())
		return m, nil
	}
	m.draft = &draft
	m.formTitle = m.form.title
	m.form = nil
	return m, tea.ExecProcess(editorCommand(m.draftPath), func(err error) tea.Msg { return draftEditedMsg{err: err} })
}

func (m Model) renderForm(width, height int) string {
	form := m.form
	column, margin := readerColumn(width)
	pad := strings.Repeat(" ", margin)
	rule := lipgloss.NewStyle().Foreground(colorChrome).Render(strings.Repeat("─", column))
	label := lipgloss.NewStyle().Foreground(colorChrome).Bold(true)
	active := lipgloss.NewStyle().Foreground(colorActive).Bold(true)
	value := lipgloss.NewStyle().Foreground(colorBright)
	cursor := lipgloss.NewStyle().Foreground(colorPrimary).Render("█")
	lines := []string{lipgloss.NewStyle().Foreground(colorBright).Bold(true).Render(form.title), rule}
	_, resolved := form.resolve()
	chipViews := map[int][]chipView{fieldTo: resolved[0], fieldCc: resolved[1], fieldBcc: resolved[2]}
	for field := fieldFrom; field <= fieldAttach; field++ {
		if (field == fieldCc || field == fieldBcc || field == fieldAttach) && form.values[field] == "" && form.field != field {
			continue // empty optional fields fold away; Tab or ↑↓ still visits them
		}
		name := label.Render(fmt.Sprintf("%-9s", fieldNames[field]))
		if field == form.field {
			name = active.Render(fmt.Sprintf("%-9s", fieldNames[field]))
		}
		text := form.values[field]
		if field == fieldFrom {
			identity := compose.Identities[form.identity]
			text = identity.Name + " <" + identity.Address + ">"
			if field == form.field {
				text += styleMuted.Render("   ← → switches account")
			}
			lines = append(lines, name+value.Render(text))
			continue
		}
		room := column - 10
		if isRecipientField(field) && (displayWidth(text) > room || len(chipSpans(text)) > 0) {
			// A long list wraps, so every name can be checked before sending;
			// past a few lines the start folds into "… N more". Chips are drawn
			// as pills and wrap as one piece.
			lines = append(lines, recipientFieldLines(name, text, room, field == form.field, value, cursor, chipViews[field])...)
			if field == form.field {
				lines = m.appendChoiceLines(lines, column, value)
			}
			continue
		}
		if displayWidth(text) > room {
			runes := []rune(text)
			for displayWidth(string(runes)) > room-1 && len(runes) > 0 {
				runes = runes[1:]
			}
			text = "…" + string(runes)
		}
		line := name + value.Render(terminal.SanitizeLine(text))
		if field == form.field {
			line += cursor
		}
		lines = append(lines, line)
		if field == form.field {
			lines = m.appendChoiceLines(lines, column, value)
			for index, path := range form.paths {
				marker := "  "
				style := value
				if index == form.choice {
					marker = lipgloss.NewStyle().Foreground(colorPrimary).Render("▸ ")
					style = lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
				}
				if index < len(form.hits) {
					// A search result: the file name, then where it lives,
					// how big it is and when it last changed.
					hit := form.hits[index]
					meta := humanSize(hit.Size) + "  " + hit.ModTime.Format("2 Jan 2006")
					name := style.Render(truncateToWidth(filepath.Base(hit.Path), column-11-len(meta)-2))
					lines = append(lines, strings.Repeat(" ", 9)+marker+fillBetween(name, styleMuted.Render(meta), column-11))
					lines = append(lines, strings.Repeat(" ", 13)+styleMuted.Render(truncateToWidth(collapseHome(filepath.Dir(hit.Path))+"/", column-13)))
					continue
				}
				lines = append(lines, strings.Repeat(" ", 9)+marker+style.Render(truncateToWidth(path, column-11)))
			}
			if field == fieldAttach && len(form.paths) == 0 && len([]rune(form.token())) >= 2 && !isPathToken(form.token()) {
				lines = append(lines, strings.Repeat(" ", 11)+styleMuted.Render("Searching your files for \""+form.token()+"\""))
			}
			if isRecipientField(field) && len(form.choices) == 0 && form.token() != "" && len(m.book.Contacts) == 0 {
				lines = append(lines, strings.Repeat(" ", 11)+styleMuted.Render("Address book still loading"))
			}
		}
	}
	if form.field == fieldBody {
		lines = append(lines, active.Render(strings.Repeat("─", column)))
	} else {
		lines = append(lines, rule)
	}
	form.body.SetWidth(column)
	form.body.SetHeight(max(height-len(lines), 3))
	body := form.body.View()
	if form.vim != nil {
		form.vim.SetWrapWidth(form.body.Width()) // gj and gk follow the wrapping on screen
		body = form.highlightSearch(body)
	}
	lines = append(lines, strings.Split(body, "\n")...)
	for index := range lines {
		lines[index] = pad + lines[index]
	}
	return strings.Join(lines[:min(len(lines), height)], "\n")
}

// singleLine collapses pasted whitespace, line breaks included, to single spaces.
func singleLine(text string) string {
	return terminal.SanitizeLine(strings.Join(strings.Fields(text), " "))
}

// handlePaste takes text pasted into the terminal (bracketed paste). The
// message body keeps it as is; a header field gets one line, with pasted
// line breaks between addresses turned into commas; search, the calendar
// prompts and the event form's one-line fields get one line, and the
// event notes keep their line breaks.
func (m Model) handlePaste(message tea.PasteMsg) (tea.Model, tea.Cmd) {
	// Terminals send pasted line breaks as \r\n or as a bare \r.
	text := strings.ReplaceAll(strings.ReplaceAll(message.Content, "\r\n", "\n"), "\r", "\n")
	switch {
	case m.calPrompt != nil:
		// The quick-add, rebook and rename lines are one line.
		m.calPrompt.input += singleLine(text)
	case m.eventForm != nil:
		target := m.eventForm.value()
		if target == nil {
			return m, nil // the calendar field takes ← →, not text
		}
		if m.eventForm.field == formNotes {
			*target += terminal.Sanitize(text) // notes keep their line breaks
		} else {
			*target += singleLine(text)
		}
	case m.form != nil && m.form.field == fieldBody && m.form.vim != nil:
		m.form.vim.Paste(strings.ReplaceAll(terminal.Sanitize(text), "\t", "    "))
		m.form.syncBody()
		return m, nil
	case m.form != nil && m.form.field == fieldBody:
		var cmd tea.Cmd
		m.form.body, cmd = m.form.body.Update(tea.PasteMsg{Content: terminal.Sanitize(text)})
		return m, cmd
	case m.form != nil && m.form.field != fieldFrom:
		separator := " "
		if isRecipientField(m.form.field) || m.form.field == fieldAttach {
			separator = ", "
		}
		var parts []string
		for _, line := range strings.Split(text, "\n") {
			if line = strings.TrimSpace(line); line != "" {
				parts = append(parts, line)
			}
		}
		m.form.values[m.form.field] += terminal.SanitizeLine(strings.Join(parts, separator))
		m.refreshChoices()
		return m, m.fileSearchCmd()
	case m.filtering:
		m.query += singleLine(text)
		m.mailCursor = 0
	}
	return m, nil
}

// vimEnabled says whether the body is edited modally. It is on unless
// MAILDAY_VIM is off, 0, false or no, which keeps the plain text area.
func vimEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MAILDAY_VIM"))) {
	case "off", "0", "false", "no":
		return false
	}
	return true
}

// handleVimKey sends a key in the body to the modal editor. ctrl+s and
// ctrl+o never get here: they work in every mode.
func (m Model) handleVimKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	form := m.form
	key := message.String()
	m.status = ""
	switch key {
	case "shift+tab":
		form.focus(fieldAttach)
		m.refreshChoices()
		return m, nil
	case "tab":
		if form.vim.Mode() != vimedit.Insert {
			return m, nil // the body is the last field: nothing to move on to
		}
	}
	text := message.Key().Text
	if message.Key().Mod.Contains(tea.ModCtrl) || message.Key().Mod.Contains(tea.ModAlt) {
		text = ""
	}
	return m.applyVimResult(form.vim.Key(key, text))
}

// applyVimResult shows what the editor holds and carries out what it asked
// for: close as esc used to, save the draft, or open the preview.
func (m Model) applyVimResult(result vimedit.Result) (tea.Model, tea.Cmd) {
	form := m.form
	form.syncBody()
	switch result.Action {
	case vimedit.Close:
		return m.closeComposer()
	case vimedit.Preview:
		return m.finishForm()
	case vimedit.SaveDraft:
		draft := form.collect()
		if form.empty() || (!form.fromFile && draft.File() == form.initial) {
			m.status = "Nothing written yet · no draft saved" // as when closing: a bare signature or quote is no draft
			return m, nil
		}
		if err := m.saveDraft(draft); err != nil {
			m.status = "Draft not saved: " + terminal.SanitizeLine(err.Error())
			return m, nil
		}
		form.saved = draft.File()
		m.status = "Draft saved"
	}
	return m, nil
}

// syncBody makes the text area show the editor's text and cursor. The text is
// only set when it changed, because setting it resets the scroll: the old
// offset is brought back, then the cursor is placed.
func (f *headerForm) syncBody() {
	text := f.vim.Text()
	if f.body.Value() != text {
		offset, height := f.body.ScrollYOffset(), f.body.Height()
		f.body.SetValue(text)
		f.body, _ = f.body.Update(nil) // hands the viewport the new text before it scrolls
		if shown := f.body.Value(); shown != text {
			f.vim.SetText(shown) // the text area drops characters it cannot hold
		}
		f.body.MoveToBegin()
		for range min(offset+height-1, len(text)) {
			f.body.CursorDown()
		}
	}
	row, col := f.viewCursor()
	f.placeBodyCursor(row, col)
	if f.showSelection() {
		f.placeBodyCursor(row, col)
	}
	f.body.Focus()                 // typing hides its cursor until told otherwise
	f.body, _ = f.body.Update(nil) // scrolls the cursor into view
}

// placeBodyCursor moves the text area's cursor to a row and rune column. A
// line the text area wraps takes several CursorDown or CursorUp steps, and
// every step moves at least one display line, so the text bounds them.
func (f *headerForm) placeBodyCursor(row, col int) {
	for range len(f.vim.Text()) + f.body.LineCount() + 2 {
		line := f.body.Line()
		if line == row {
			break
		}
		if line < row {
			f.body.CursorDown()
		} else {
			f.body.CursorUp()
		}
	}
	f.body.SetCursorColumn(col)
}

// showSelection highlights the visual selection with the text area's own
// selection, which can only be set by screen position: it finds each end by
// putting the cursor there and reading where the cursor is drawn. That moves
// the cursor, so the caller puts it back. It says whether it did.
func (f *headerForm) showSelection() bool {
	f.body.ClearSelection()
	start, end, ok := f.vim.Selection()
	if !ok || start == end {
		return false
	}
	screen := func(at vimedit.Pos) (x, y int) {
		f.placeBodyCursor(at.Row, at.Col)
		f.body.SetVirtualCursor(false) // only then does the text area report its cursor
		cursor := f.body.Cursor()
		f.body.SetVirtualCursor(true)
		if cursor == nil {
			return 0, 0
		}
		// The text area's mouse mapping still counts the prompt it was created
		// with, which is gone here, so nudge x until it lands on this place.
		x = cursor.X
		for nudge := 0; nudge <= 8; nudge++ {
			if f.body.PositionAt(cursor.X+nudge, cursor.Y) == (textarea.Position{Row: at.Row, Col: at.Col}) {
				x = cursor.X + nudge
				break
			}
		}
		return x, cursor.Y + f.body.ScrollYOffset()
	}
	startX, startY := screen(start)
	endX, endY := screen(end)
	f.body.BeginSelection(startX, startY-f.body.ScrollYOffset())
	f.body.ExtendSelection(endX, endY-f.body.ScrollYOffset())
	f.body.EndSelection()
	return true
}

// renderVimFooter shows the editor's mode, or the ':' line being typed, with
// the keys that matter in that mode. The ':' line is also the / and ? search.
func (m Model) renderVimFooter(width int) string {
	vim := m.form.vim
	rule := renderRule(width, "")
	if vim.Mode() == vimedit.Command {
		prompt := lipgloss.NewStyle().Foreground(colorChrome).Bold(true).Render(vim.CommandPrefix())
		cursor := lipgloss.NewStyle().Foreground(colorPrimary).Render("█")
		return rule + "\n" + truncateToWidth(prompt+terminal.SanitizeLine(vim.CommandLine())+cursor, width)
	}
	label := lipgloss.NewStyle().Foreground(colorActive).Bold(true).Render("-- " + vim.Mode().String() + " --")
	if note := recordingNote(vim); note != "" {
		label += " " + note
	}
	var bindings []helpBinding
	switch vim.Mode() {
	case vimedit.Insert:
		bindings = []helpBinding{{"esc", "normal"}, {"ctrl+s", "preview & send"}, {"shift+tab", "headers"}, {"ctrl+o", "$EDITOR"}}
	case vimedit.Normal:
		bindings = []helpBinding{{"i a o", "type"}, {"dd yy p u", "edit"}, {"/ n", "search"}, {"q @", "macro"}, {":w", "save"}, {"ZZ", "preview"}, {"esc esc", "close, draft kept"}}
	default:
		bindings = []helpBinding{{"d y c", "act"}, {"> <", "indent"}, {"~", "case"}, {"esc", "back"}}
	}
	note := vim.Pending()
	if status := vim.Status(); status != "" {
		note = status
	}
	if note != "" {
		return rule + "\n" + truncateToWidth(label+"  "+styleMuted.Render(terminal.SanitizeLine(note)), width)
	}
	return rule + "\n" + truncateToWidth(label+"  "+strings.ReplaceAll(renderHelpBindings(bindings, max(width-displayWidth(label)-2, 10)), "\n", "  "), width)
}

// appendChoiceLines lists the groups and people that match what is typed.
func (m Model) appendChoiceLines(lines []string, column int, value lipgloss.Style) []string {
	form := m.form
	for index, group := range form.groupChoices {
		marker := "  "
		nameStyle := lipgloss.NewStyle().Foreground(colorActive).Bold(true)
		if index == form.choice {
			marker = lipgloss.NewStyle().Foreground(colorPrimary).Render("▸ ")
			nameStyle = lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
		}
		names := make([]string, 0, 3)
		for _, member := range group.Members {
			if len(names) == 3 {
				break
			}
			names = append(names, contactLabel(member))
		}
		entry := nameStyle.Render(truncateToWidth("◆ "+group.Name, 28))
		lines = append(lines, strings.Repeat(" ", 9)+marker+padTo(entry, 30)+styleMuted.Render(truncateToWidth(people(len(group.Members))+" · "+strings.Join(names, ", "), column-41)))
	}
	for index, contact := range form.choices {
		index += len(form.groupChoices)
		marker := "  "
		nameStyle := value
		if index == form.choice {
			marker = lipgloss.NewStyle().Foreground(colorPrimary).Render("▸ ")
			nameStyle = lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
		}
		who := contact.Name
		if who == "" {
			who = contact.Address
		}
		entry := nameStyle.Render(truncateToWidth(who, 28))
		lines = append(lines, strings.Repeat(" ", 9)+marker+padTo(entry, 30)+styleMuted.Render(truncateToWidth(contact.Address, column-41)))
	}
	return lines
}
