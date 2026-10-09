package tui

import (
	"context"
	"errors"
	"fmt"
	"github.com/aronvandepol/mailday/internal/config"
	"github.com/aronvandepol/mailday/internal/groups"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/aronvandepol/mailday/internal/calendar"
	"github.com/aronvandepol/mailday/internal/compose"
	"github.com/aronvandepol/mailday/internal/contacts"
	"github.com/aronvandepol/mailday/internal/maildir"
	"github.com/aronvandepol/mailday/internal/syncd"
	"github.com/aronvandepol/mailday/internal/terminal"
)

type MailStore interface {
	List(context.Context) (maildir.ListResult, error)
	Read(string) (maildir.Content, error)
	SetUnread(maildir.Message, bool) (maildir.Message, error)
	Archive(maildir.Message) error
	MoveTo(maildir.Message, string) (string, error)
	MarkAnswered(string) (string, error)
	SaveSent(string, []byte) error
	Attachments(string) ([]maildir.AttachmentData, error)
	Resolve(string) string
	SaveAttachments(string, string) ([]string, error)
}

type CalendarStore interface {
	Load(context.Context, time.Time, time.Time) (calendar.Result, error)
}

type Options struct {
	Days            int
	MailRoots       []string // for the address book behind recipient autocompletion
	SyncCommand     []string
	SyncEnv         []string
	MailCommand     []string
	CalendarCommand []string
	// Live follows the Maildirs on disk and talks to mailday-syncd.
	Live bool
	// StartCalendar opens on the calendar; OpenPath opens that message once
	// the mail has loaded (both from notifications).
	StartCalendar bool
	OpenPath      string
}

type pane uint8

const (
	paneMail pane = iota
	paneAgenda
)

type calendarMode uint8

const (
	calendarDay calendarMode = iota
	calendarWeek
	calendarAgenda
)

func (mode calendarMode) String() string {
	switch mode {
	case calendarDay:
		return "Day"
	case calendarWeek:
		return "Week"
	case calendarAgenda:
		return "Agenda"
	default:
		return "Week"
	}
}

type screen uint8

const (
	screenHome screen = iota
	screenMail
	screenEvent
	screenCompose
)

type Model struct {
	mailStore     MailStore
	calendarStore CalendarStore
	options       Options
	now           func() time.Time
	judge         compose.JudgeFunc // decides whether a reply settles an @Reply message

	width  int
	height int
	focus  pane
	screen screen

	messages         []maildir.Message
	accounts         []string
	boxes            []string
	events           []calendar.Event
	upcoming         []upcomingMail // snoozed mail with its due time, re-read when mail loads
	sources          []calendar.Source
	mailWarnings     []string
	calendarWarnings []string
	mailLoadErr      string
	calendarLoadErr  string

	mailCursor     int
	mailOffset     int // first message shown in the list; moves only when the cursor leaves the window
	eventCursor    int // below 0: a snoozed row of the agenda (upcoming.go)
	detailScroll   int
	mailAccount    int
	mailBox        int
	calendarMode   calendarMode
	calendarAnchor time.Time

	query     string
	filtering bool

	filing      bool
	fileTargets []string
	fileKeys    []string // one-key shortcut per target, so f then r files to Reply
	fileCursor  int

	draft     *compose.Draft
	draftPath string
	formTitle string
	sending   bool
	lastMove  *lastMove
	showHelp  bool
	form      *headerForm
	book      contacts.Book
	// pendingStatus survives the reload a command triggers, which would
	// otherwise clear the status line when it finishes.
	pendingStatus string

	content     *maildir.Content
	contentPath string
	// bodyCache holds the rendered reader body, because glamour is too slow
	// to run on every keypress.
	bodyCache       []string // without the left margin, for bodyCacheColumn
	bodyCacheColumn int
	showHistory     bool
	loadingBody     bool
	loadingMail     bool
	loadingCal      bool
	// calQuietLoading and calReloadAgain keep one quiet calendar reload in
	// flight; a change arriving meanwhile asks for another when it lands.
	calQuietLoading bool
	calReloadAgain  bool
	syncing         bool

	// live follows the Maildirs and mailday-syncd; reloading and
	// reloadAgain keep at most one quiet reload in flight.
	live        *live
	syncStatus  syncd.Status
	syncDown    bool
	reloading   bool
	reloadAgain bool
	// readerGone: the open message was moved or deleted elsewhere, so
	// actions in the reader are refused instead of hitting its neighbour.
	readerGone bool
	// readerMessage is the message as it was when opened (kept current while
	// it is in the list), so the reader's header never comes from whatever
	// the list cursor points at.
	readerMessage maildir.Message
	calendarDay   time.Time // the day the agenda was loaded for

	// Full-text search through notmuch (search.go).
	searchHits  map[string]bool // Maildir keys notmuch found for searchQuery
	searchQuery string
	searchExtra []maildir.Message // hits outside the loaded mail
	searchSeq   int

	// Calendar editing (calendar_edit.go).
	calPrompt *calendarPrompt
	snoozing  *snoozePrompt // Z asks when (snooze.go)
	// people.go: i lists everyone on the message; recipient groups (P, G, ctrl+g).
	showRecipients    bool
	groupPrompt       *groupPrompt
	groupsView        *groupsView
	groupList         []groups.Group
	groupsError       string
	suggested         []contacts.Suggestion // P's suggested groups, from your mail
	suggestedAt       time.Time
	suggesting        bool
	writableCalendars []string
	calendarBackends  map[string]string
	lastCalendar      string
	pendingDelete     string
	calPending        []pendingChange
	calUndo           *calendarUndo // the last calendar change, for u (calendar_undo.go)
	pendingSeq        int
	nudging           *pendingChange
	nudgeSeq          int
	eventForm         *eventForm
	openPath          string // message to open when the mail first loads
	sendCountdown     int    // seconds until a sent draft goes; u undoes
	sendSeq           int
	autosaveSeq       int // numbers composers, so an old composer's autosave tick ends
	// Quitting waits up to quitWait for calendar writes, lookups and a
	// send (quit.go); calWriting and calLooking count those in flight.
	quitting   bool
	quitSeq    int
	calWriting int
	calLooking int

	status         string
	pendingArchive string
	// Status history and the hold on failures (statuslog.go).
	statusLog   []statusEntry
	helpScroll  int
	errorStatus string
	errorUntil  time.Time
	showLog     bool
	logScroll   int
	pendingGo   bool      // g was pressed; the next key picks a box
	zone        zoneState // follows the system time zone (zone.go)

	// Box sidebar and project picker (sidebar.go, boxpicker.go).
	boxNav boxNavState

	// Moving between messages, attachments and links (reader_nav.go) and the
	// two-step send checks (send_checks.go).
	flow       readerFlow
	sendWarned string
	threads    threadState // conversations in the list (threads.go)

	// Read receipts: hidden from the list, marked in Sent (receipts.go).
	receipts receiptState
}

type mailLoadedMsg struct {
	result maildir.ListResult
	err    error
	quiet  bool // a reload after a change on disk: keep the status line
}

type calendarLoadedMsg struct {
	result calendar.Result
	err    error
	quiet  bool // a reload after new calendar data: keep the status line
	// from and to are the range the load was for; a result for a range the
	// calendar has since left is dropped. Zero means untagged.
	from, to time.Time
}

type bodyLoadedMsg struct {
	path     string // as requested
	usedPath string // the file actually read, if it was renamed meanwhile
	content  maildir.Content
	err      error
}

type syncFinishedMsg struct {
	output string
	err    error
}

type messageChangedMsg struct {
	oldPath string
	message maildir.Message
	auto    bool // marked read by opening it: no status line
	advance bool // m pressed in the reader: go on to the next message
	err     error
}

type messageFiledMsg struct {
	message maildir.Message // as listed, before the move
	newPath string
	box     string
	err     error
}

type externalFinishedMsg struct {
	name string
	err  error
}

func NewModel(mailStore MailStore, calendarStore CalendarStore, options Options) Model {
	if options.Days <= 0 {
		options.Days = 14
	}
	if len(options.SyncCommand) == 0 {
		options.SyncCommand = []string{"mbsync", "-a"}
	}
	if len(options.MailCommand) == 0 {
		options.MailCommand = []string{"neomutt"}
	}
	if len(options.CalendarCommand) == 0 {
		options.CalendarCommand = defaultCalendarCommand()
	}
	if options.SyncEnv == nil {
		options.SyncEnv = defaultSyncEnv()
	}
	zone := zoneState{}
	if options.Live {
		zone = newZoneState()
	}
	return Model{
		zone:          zone,
		mailStore:     mailStore,
		calendarStore: calendarStore,
		options:       options,
		now:           time.Now,
		judge:         compose.ConfiguredJudge(),
		width:         100,
		height:        30,
		screen:        screenHome,
		mailAccount:   -1,
		calendarMode:  calendarWeek,
		loadingMail:   true,
		loadingCal:    true,
		status:        "Reading local mail and calendar caches",
		live:          newLive(),
		focus:         startPane(options),
		openPath:      options.OpenPath,
	}
}

func startPane(options Options) pane {
	if options.StartCalendar {
		return paneAgenda
	}
	return paneMail
}

func (m Model) Init() tea.Cmd {
	commands := []tea.Cmd{m.loadMailCmd(), m.loadCalendarCmd(), watchThemeCmd(themeWatchDir())}
	commands = append(commands, checkSnoozeCmd()) // snooze.go: reports a damaged snooze list once
	if len(m.options.MailRoots) > 0 {
		commands = append(commands, loadContactsCmd(m.options.MailRoots))
		if m.options.Live {
			m.live.startMailWatch(m.options.MailRoots)
			commands = append(commands, m.live.waitMailChangeCmd(), m.live.subscribeCmd(), clockTickCmd())
			commands = append(commands, loadWritableCalendarsCmd())
			if watcher, ok := m.calendarStore.(interface{ WatchDirs() []string }); ok {
				m.live.startCalendarWatch(watcher.WatchDirs())
				commands = append(commands, m.live.waitCalendarChangeCmd())
			}
		}
	}
	return tea.Batch(commands...)
}

func (m Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	before := m.status
	next, command := m.update(message)
	if model, ok := next.(Model); ok {
		model.trackStatus(before)
		model.keepMailCursorVisible()
		return model, command
	}
	return next, command
}

func (m Model) update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width = message.Width
		m.height = message.Height
		m.recacheBodyForWidth()
		return m, nil
	case inviteLookupMsg: // invite.go
		return m.handleInviteLookup(message)
	case themeChangedMsg:
		applyTheme(resolveTheme())
		return m, watchThemeCmd(themeWatchDir())
	case mailChangedMsg:
		next := m.live.waitMailChangeCmd()
		if m.loadingMail || m.reloading {
			m.reloadAgain = true
			return m, next
		}
		m.reloading = true
		return m, tea.Batch(next, m.quietLoadMailCmd())
	case syncStatusMsg:
		if message.err != nil {
			m.syncDown = true
			m.syncStatus = syncd.Status{}
			return m, syncRetryCmd()
		}
		if errs := newSyncErrors(m.syncStatus, message.status); errs != "" {
			m.status = terminal.SanitizeLine(errs)
		} else if recoveredSyncError(m.status, message.status) {
			m.status = ""
		}
		m.syncDown = false
		m.syncStatus = message.status
		return m, m.live.nextStatusCmd()
	case sendTickMsg:
		return m.handleSendTick(message)
	case searchResultMsg:
		return m.handleSearchResult(message)
	case searchTickMsg:
		return m.handleSearchTick(message)
	case calendarsLoadedMsg:
		m.writableCalendars = message.names
		m.calendarBackends = message.backends
		return m, nil
	case calendarWriteMsg:
		return m.handleCalendarWrite(message)
	case nudgeCommitMsg:
		return m.commitNudge(message)
	case autosaveTickMsg:
		return m.handleAutosave(message)
	case quitWaitMsg:
		return m.handleQuitWait(message)
	case freeBusyMsg:
		return m.handleFreeBusy(message)
	case notesEditedMsg:
		return m.handleNotesEdited(message)
	case syncRetryMsg:
		return m, m.live.subscribeCmd()
	case clockTickMsg:
		// A new system zone (travelling) reloads the calendar for it.
		var zoneReload tea.Cmd
		m, zoneReload = m.followZone()
		// Left open past midnight: today moved, so the agenda reloads.
		if !m.calendarDay.IsZero() && !dayOf(m.now()).Equal(m.calendarDay) && !m.loadingCal {
			m.calendarDay = dayOf(m.now())
			return m, tea.Batch(clockTickCmd(), zoneReload, m.quietLoadCalendarCmd())
		}
		return m, tea.Batch(clockTickCmd(), zoneReload)
	case calendarChangedMsg:
		next := m.live.waitCalendarChangeCmd()
		if m.loadingCal || m.calQuietLoading {
			// The load in flight may have read the files before this change:
			// load again when it lands.
			m.calReloadAgain = true
			return m, next
		}
		m.calQuietLoading = true
		return m, tea.Batch(next, m.quietLoadCalendarCmd())
	case mailLoadedMsg:
		var again tea.Cmd
		if message.quiet {
			m.reloading = false
		} else {
			m.loadingMail = false
		}
		if m.reloadAgain && !m.loadingMail && !m.reloading {
			m.reloadAgain = false
			m.reloading = true
			again = m.quietLoadMailCmd()
		}
		if message.err != nil {
			if message.quiet {
				return m, again // a half-written folder; the next change retries
			}
			m.mailLoadErr = "Mail error: " + terminal.SanitizeLine(message.err.Error())
			m.updateReadyStatus()
			return m, again
		}
		m.mailLoadErr = ""
		message.result.Messages = m.splitReceipts(message.result.Messages) // receipts.go: read receipts are not mail to read
		selectedPath := ""
		if selected, ok := m.cursorMail(); ok {
			selectedPath = selected.Path
		}
		currentBox := m.activeBox()
		var arrived []maildir.Message
		if message.quiet {
			arrived = newInboxMail(m.messages, message.result.Messages)
		}
		m.messages = append(message.result.Messages, m.keptSearchExtras(message.result.Messages)...)
		m.accounts = message.result.Accounts
		m.boxes = message.result.Boxes
		m.mailBox = max(slices.Index(m.boxes, currentBox), 0)
		m.mailWarnings = message.result.Warnings
		m.refreshDrafts()
		m.refreshUpcoming() // upcoming.go
		m.mailBox = max(slices.Index(m.boxes, currentBox), 0)
		m.setMailCursorToPath(selectedPath)
		if m.screen == screenMail && m.contentPath != "" {
			if index := m.findMessage(m.contentPath); index >= 0 {
				// The reader follows the message by itself; the list cursor
				// stays where reading left it, on the next message.
				m.contentPath = m.messages[index].Path
				m.readerMessage = m.messages[index]
				m.readerGone = false
			} else if !m.readerGone {
				m.readerGone = true
				m.status = "Moved or deleted elsewhere · esc goes back"
			}
		}
		m.boundCursors()
		if m.openPath != "" {
			path := m.openPath
			m.openPath = ""
			if index := m.findMessage(path); index >= 0 {
				m.query = ""
				m.mailBox = max(slices.Index(m.boxes, messageBox(m.messages[index])), 0)
				m.mailAccount = -1
				m.setMailCursorToPath(m.messages[index].Path)
				model, command := m.openSelected()
				return model, tea.Batch(again, command)
			}
		}
		if !message.quiet {
			m.updateReadyStatus()
		} else if len(arrived) > 0 && (m.status == "" || strings.HasPrefix(m.status, "New: ")) {
			m.status = arrivalNotice(arrived)
		}
		return m, again
	case calendarLoadedMsg:
		if message.quiet {
			m.calQuietLoading = false
		}
		if from, to := m.calendarRange(); !message.from.IsZero() && !(message.from.Equal(from) && message.to.Equal(to)) {
			// A load for a day, week or agenda the user has left. If the day
			// rolled over meanwhile no load of the new range may be coming.
			if !m.calendarDay.IsZero() && !dayOf(m.now()).Equal(m.calendarDay) {
				m.calendarDay = dayOf(m.now())
				return m, m.quietLoadCalendarCmd()
			}
			return m, nil
		}
		m.loadingCal = false
		m.calendarDay = dayOf(m.now())
		var reload tea.Cmd
		if m.calReloadAgain {
			m.calReloadAgain = false
			m.calQuietLoading = true
			reload = m.quietLoadCalendarCmd()
		}
		if message.err != nil {
			m.calendarLoadErr = "Calendar error: " + terminal.SanitizeLine(message.err.Error())
			m.updateReadyStatus()
			return m, reload
		}
		m.calendarLoadErr = ""
		selectedID := ""
		if event, ok := m.selectedEvent(); ok {
			selectedID = event.ID
		}
		m.events = message.result.Events
		if loaded, ok := m.overlayPending()[selectedID]; ok {
			selectedID = loaded
		}
		if selectedID != "" {
			m.selectEvent(selectedID)
		}
		m.sources = message.result.Sources
		m.calendarWarnings = message.result.Warnings
		m.boundCursors()
		if !message.quiet {
			m.updateReadyStatus()
		}
		return m, reload
	case bodyLoadedMsg:
		// Matched by Maildir key: a rename while the body loaded (a flag from
		// elsewhere, mbsync adding a UID) must not leave "Reading…" stuck.
		if m.contentPath == "" || maildir.Key(message.path) != maildir.Key(m.contentPath) {
			return m, nil
		}
		m.loadingBody = false
		if message.err != nil {
			m.status = "Preview error: " + terminal.SanitizeLine(message.err.Error())
			return m, nil
		}
		m.content = &message.content
		m.showHistory = false
		m.showRecipients = false
		m.cacheBody()
		m.detailScroll = 0
		m.status, m.flow.note = m.flow.note, "" // what the action that brought us here said
		// Opening a message reads it, as in Outlook; m marks it unread again.
		if index := m.findMessage(message.path); index >= 0 && m.messages[index].Unread {
			selected, store := m.messages[index], m.mailStore
			return m, func() tea.Msg {
				updated, err := store.SetUnread(selected, false)
				return messageChangedMsg{oldPath: selected.Path, message: updated, auto: true, err: err}
			}
		}
		return m, nil
	case syncFinishedMsg:
		m.syncing = false
		if message.err != nil {
			m.status = "mbsync failed"
			if message.output != "" {
				m.status += ": " + terminal.SanitizeLine(message.output)
			}
			return m, nil
		}
		m.status = "Mail synced"
		if m.options.Live {
			// The Maildir watcher reloads what changed; refresh the calendar.
			m.loadingCal = true
			return m, m.loadCalendarCmd()
		}
		m.loadingMail = true
		m.loadingCal = true
		return m, tea.Batch(m.loadMailCmd(), m.loadCalendarCmd())
	case messageChangedMsg:
		if message.err != nil {
			m.status = terminal.SanitizeLine(message.err.Error())
			return m, nil
		}
		for index := range m.messages {
			if m.messages[index].Path != message.oldPath {
				continue
			}
			m.messages[index] = message.message
			if m.contentPath == "" || maildir.Key(m.contentPath) == maildir.Key(message.oldPath) {
				m.contentPath = message.message.Path
				m.readerMessage = message.message
			}
			if message.auto {
				// Read by opening: say nothing.
			} else if message.message.Unread {
				m.status = "Marked unread"
			} else {
				m.status = "Marked read"
			}
			break
		}
		m.pendingArchive = ""
		if message.message.Unread || (!message.auto && !message.advance) {
			m.setMailCursorToPath(message.message.Path) // m from the list, or jumps up to New for You: stay on the message
		}
		// Marked read by opening: the cursor keeps its place in the list and
		// the next message slides under it, so reading goes top-down instead
		// of following each message into Previously Seen.
		m.boundCursors()
		if message.advance && m.readerShows(message.oldPath) {
			note := "Marked read"
			if message.message.Unread {
				note = "Marked unread"
			}
			if command, ok := m.advanceReader(maildir.Key(message.oldPath), note); ok {
				return m, command
			}
		}
		return m, nil
	case attachmentsSavedMsg:
		if message.err != nil {
			m.status = "Not saved: " + terminal.SanitizeLine(message.err.Error())
		} else {
			m.status = fmt.Sprintf("Saved %d file(s) to %s", len(message.paths), terminal.SanitizeLine(collapseHome(filepath.Dir(message.paths[0]))))
		}
		return m, nil
	case contactsLoadedMsg:
		return m.handleContactsLoaded(message)
	case fileSearchTickMsg:
		return m.handleFileSearchTick(message)
	case fileSearchMsg:
		return m.handleFileSearch(message)
	case composerRequestMsg:
		return m.handleComposerRequest(message)
	case draftEditedMsg:
		return m.handleDraftEdited(message)
	case draftSentMsg:
		return m.handleDraftSent(message)
	case messageDeletedMsg:
		return m.handleDeleted(message)
	case messageArchivedMsg:
		return m.handleArchived(message)
	case messageRestoredMsg:
		return m.handleRestored(message)
	case groupSuggestionsMsg:
		return m.handleGroupSuggestions(message)
	case groupsSavedMsg:
		return m.handleGroupsSaved(message)
	case groupsEditedMsg:
		return m.handleGroupsEdited(message)
	case messageSnoozedMsg:
		return m.handleSnoozed(message)
	case replySettledMsg:
		return m.handleReplySettled(message)
	case messageFiledMsg:
		m.filing = false
		if message.err != nil {
			m.status = terminal.SanitizeLine(message.err.Error())
			return m, nil
		}
		for index := range m.messages {
			if m.messages[index].Path == message.message.Path {
				m.messages[index].Path = message.newPath
				m.messages[index].Box = message.box
				break
			}
		}
		m.recordMove(message.message, message.newPath, message.box)
		note := "Filed to " + boxLabel(message.box) + " · u undoes"
		advance := m.leaveOrAdvance(message.message.Path, note)
		m.status = note
		m.boundCursors()
		return m, advance
	case externalFinishedMsg:
		if message.err != nil {
			m.status = fmt.Sprintf("%s failed: %s", message.name, terminal.SanitizeLine(message.err.Error()))
			return m, nil
		}
		if message.name == "Calendar" {
			m.status = "Calendar opened"
			return m, nil
		}
		m.status = message.name + " closed"
		m.loadingMail = true
		return m, m.loadMailCmd()
	case tea.PasteMsg:
		return m.handlePaste(message)
	case tea.KeyPressMsg:
		return m.handleKey(message)
	}
	if model, command, handled := m.handleThreadMsg(message); handled { // threads_actions.go
		return model, command
	}
	return m.handleReaderFlowMsg(message)
}

func (m Model) handleKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := message.String()
	if key == "ctrl+c" {
		if m.quitting {
			return m, tea.Quit
		}
		return m.handleCtrlC()
	}
	if m.quitting {
		// A second q quits now; anything else carries on and cancels the wait.
		if key == "q" {
			return m, tea.Quit
		}
		m.quitting = false
	}
	if m.showHelp {
		// j k scroll a list longer than the screen; any other key closes.
		switch key {
		case "j", "down", "pgdown", "ctrl+d":
			m.helpScroll++
		case "k", "up", "pgup", "ctrl+u":
			m.helpScroll = max(m.helpScroll-1, 0)
		default:
			m.showHelp, m.helpScroll = false, 0
		}
		return m, nil
	}
	if m.showLog {
		return m.handleLogKey(message)
	}
	if m.calPrompt != nil && m.calPrompt.kind == promptGoto {
		return m.handleGotoKey(message)
	}
	if m.snoozing != nil {
		return m.handleSnoozeKey(message)
	}
	if m.groupPrompt != nil {
		return m.handleGroupPromptKey(message)
	}
	if m.calPrompt != nil {
		return m.handleCalendarPromptKey(message)
	}
	if m.eventForm != nil {
		return m.handleEventFormKey(message)
	}
	if m.filtering {
		return m.handleFilterKey(message)
	}
	if m.filing {
		return m.handleFileKey(message)
	}
	if m.boxNav.picker != nil {
		return m.handleBoxPickerKey(message)
	}
	if m.groupsView != nil {
		return m.handleGroupsKey(message)
	}
	if m.form != nil {
		return m.handleFormKey(message)
	}
	if key == "?" {
		m.showHelp = true
		return m, nil
	}
	if key == "!" {
		m.showLog, m.logScroll = true, 0
		return m, nil
	}
	if m.screen == screenCompose {
		return m.handleComposeKey(message)
	}
	if m.screen != screenHome {
		return m.handleDetailKey(message)
	}
	if key != "a" && !(key == "d" && strings.HasPrefix(m.pendingArchive, "delete:")) {
		m.pendingArchive = ""
	}
	if m.pendingGo {
		m.pendingGo = false
		if key == "p" && m.focus == paneMail {
			return m.openBoxPicker() // g p: the project picker (p inside the picker jumps to PhD)
		}
		return m.goToBox(key)
	}
	if key != "d" && key != "D" {
		m.pendingDelete = ""
	}
	if model, command, handled := m.handleThreadKey(key); handled { // threads_actions.go: space, bulk a d m
		return model, command
	}
	if m.focus == paneAgenda {
		if model, command, handled := m.handleUpcomingKey(key); handled { // upcoming.go: snoozed rows
			return model, command
		}
		if model, command, handled := m.handleCalendarKey(key); handled {
			return model, command
		}
	}

	switch key {
	case "q":
		return m.requestQuit()
	case "tab":
		if m.focus == paneMail {
			m.focus = paneAgenda
		} else {
			m.focus = paneMail
		}
		m.status = ""
	case "P":
		return m.openGroups()
	case "M":
		m.focus = paneMail
		m.status = ""
	case "C":
		m.focus = paneAgenda
		m.status = ""
	case "left", "h", "[":
		if m.focus == paneAgenda {
			return m.shiftCalendar(-1)
		}
		m.cycleMailAccount(-1)
	case "right", "l", "]":
		if m.focus == paneAgenda {
			return m.shiftCalendar(1)
		}
		m.cycleMailAccount(1)
	case "up", "k":
		m.moveCursor(-1)
	case "down", "j":
		m.moveCursor(1)
	case "pgup", "pgdown", "ctrl+u", "ctrl+d", "n":
		return m.handleListPaging(key)
	case "home":
		m.setCursor(0)
	case "g":
		if m.focus == paneMail {
			m.pendingGo = true
			m.status = "go to: i Inbox · D Drafts · S Sent · x Archive · r Reply · w Waiting · p projects · tag letters as with f · g top"
			return m, nil
		}
		return m.startGotoPrompt(), nil
	case "J":
		if m.focus == paneMail {
			return m.joinStripEvent()
		}
	case "end", "G":
		m.setCursor(m.activeLength() - 1)
	case "enter":
		return m.openSelected()
	case "/":
		m.focus = paneMail
		m.filtering = true
		m.mailCursor = 0
		m.status = ""
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		if m.focus == paneMail {
			m.selectMailAccountKey(key)
			return m, nil
		}
		mode, _ := strconv.Atoi(key)
		if mode >= 1 && mode <= 3 {
			m.calendarMode = calendarMode(mode - 1)
			m.eventCursor = 0
			m.loadingCal = true
			m.status = "Reading " + m.calendarMode.String()
			return m, m.loadCalendarCmd()
		}
	case "t":
		if m.focus == paneAgenda {
			m.calendarAnchor = time.Time{}
			m.eventCursor = 0
			m.loadingCal = true
			m.status = "Returning to today"
			return m, m.loadCalendarCmd()
		}
	case "r":
		applyTheme(resolveTheme())
		m.loadingMail = true
		m.loadingCal = true
		m.status = "Refreshing local data"
		return m, tea.Batch(m.loadMailCmd(), m.loadCalendarCmd())
	case "s":
		if !m.syncing {
			m.syncing = true
			if m.options.Live && !m.syncDown {
				m.status = "Syncing every account"
				return m, daemonSyncCmd(m.options.SyncCommand, m.options.SyncEnv)
			}
			m.status = "Running mbsync"
			return m, runCommand(m.options.SyncCommand, m.options.SyncEnv, 5*time.Minute)
		}
	case "m":
		return m.toggleSelectedUnread()
	case "a":
		return m.archiveSelected()
	case "b", "B":
		if m.focus == paneMail && len(m.boxes) > 0 {
			step := 1
			if key == "B" {
				step = -1
			}
			m.cycleBox(step)
		}
	case "\\":
		if m.focus == paneMail {
			return m.toggleSidebar()
		}
	case "f":
		return m.startFiling()
	case "d":
		return m.deleteSelected()
	case "u":
		if m.focus == paneMail {
			return m.undoMove()
		}
	case "Z":
		if m.focus == paneMail {
			return m.startSnoozePrompt()
		}
	case "R", "A", "F":
		if m.focus == paneMail {
			return m.startCompose(map[string]string{"R": "reply", "A": "all", "F": "forward"}[key])
		}
	case "o":
		return m.openExternal()
	case "c":
		if m.focus == paneAgenda {
			return m.openExternal()
		}
		return m.startCompose("new")
	case "esc":
		if m.query != "" {
			m.query = ""
			m.mailCursor = 0
			m.status = "Filter cleared"
			m.dropSearch()
		}
	}
	return m, nil
}

func (m Model) handleFilterKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := message.String()
	before := m.query
	switch key {
	case "esc":
		m.filtering = false
		m.query = ""
		m.mailCursor = 0
	case "enter":
		m.filtering = false
		m.mailCursor = 0
		if m.query == "" {
			m.status = "Filter cleared"
			return m, nil
		}
		m.status = fmt.Sprintf("%d matching messages", len(m.visibleMessageIndexes()))
		if notmuchAvailable() && m.searchQuery != strings.TrimSpace(m.query) {
			m.status += " · searching all mail"
			return m, searchCmd(m.mailStore, strings.TrimSpace(m.query))
		}
		return m, nil
	case "backspace":
		m.query = trimLastRune(m.query)
		m.mailCursor = 0
	case "ctrl+u":
		m.query = ""
		m.mailCursor = 0
	case "ctrl+w":
		m.query = strings.TrimRight(m.query, " ")
		if index := strings.LastIndex(m.query, " "); index >= 0 {
			m.query = m.query[:index+1]
		} else {
			m.query = ""
		}
		m.mailCursor = 0
	default:
		if text := message.Key().Text; text != "" && !message.Key().Mod.Contains(tea.ModCtrl) {
			m.query += text
			m.mailCursor = 0
		}
	}
	if m.query != before {
		if strings.TrimSpace(m.query) == "" {
			m.dropSearch()
		}
		model, command := m.scheduleSearch()
		return model, command
	}
	return m, nil
}

func (m Model) handleDetailKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := message.String()
	if key != "a" {
		m.pendingArchive = ""
	}
	if model, command, handled := m.handleThreadReaderKey(key); handled { // threads_conversation.go: T and its list
		return model, command
	}
	if m.screen == screenMail {
		if model, command, handled := m.handleInviteKey(key); handled { // invite.go: y ~ x J c on an invitation
			return model, command
		}
	}
	if m.screen == screenMail && key == "E" && m.flow.picker == nil {
		return m.eventFromMail() // event_from_mail.go: the quick-add box filled from the message
	}
	if m.screen == screenEvent {
		if key != "d" && key != "D" {
			m.pendingDelete = ""
		}
		if model, command, handled := m.handleCalendarKey(key); handled {
			return model, command
		}
	}
	if model, command, handled := m.handleReaderKey(key); handled {
		return model, command
	}
	switch key {
	case "m", "a", "f", "d", "S", "R", "A", "F", "o", "r", "Z":
		if !m.readerReady() {
			return m, nil
		}
	}
	switch key {
	case "esc", "q", "backspace":
		m.screen = screenHome
		m.detailScroll = 0
		m.status = ""
		m.readerGone = false
	case "up", "k":
		if m.detailScroll > 0 {
			m.detailScroll--
		}
	case "down", "j":
		m.detailScroll = min(m.detailScroll+1, m.maxDetailScroll())
	case "pgup", "ctrl+u":
		m.detailScroll -= max(m.height-8, 1)
		if m.detailScroll < 0 {
			m.detailScroll = 0
		}
	case "pgdown", "ctrl+d":
		m.detailScroll = min(m.detailScroll+max(m.height-8, 1), m.maxDetailScroll())
	case "home", "g":
		m.detailScroll = 0
	case "i":
		if m.screen == screenMail {
			return m.toggleRecipients()
		}
	case "G":
		if m.screen == screenMail && m.content != nil {
			return m.startSaveGroup(peopleOn(*m.content, true))
		}
	case "z":
		if m.screen == screenMail && m.content != nil {
			m.showHistory = !m.showHistory
			m.cacheBody()
			m.detailScroll = min(m.detailScroll, m.maxDetailScroll())
		}
	case "m":
		return m.toggleSelectedUnread()
	case "a":
		return m.archiveSelected()
	case "f":
		return m.startFiling()
	case "d":
		if m.screen == screenMail {
			return m.deleteSelected()
		}
	case "S":
		if m.screen == screenMail {
			return m.saveAttachments()
		}
	case "Z":
		if m.screen == screenMail {
			return m.startSnoozePrompt()
		}
	case "R", "A", "F":
		if m.screen == screenMail {
			return m.startCompose(map[string]string{"R": "reply", "A": "all", "F": "forward"}[key])
		}
	case "o":
		return m.openExternal()
	case "r":
		if m.screen == screenMail {
			if selected, ok := m.selectedMail(); ok {
				m.loadingBody = true
				m.content = nil
				m.contentPath = selected.Path
				return m, loadBodyCmd(m.mailStore, selected.Path)
			}
		}
	}
	return m, nil
}

func (m Model) openSelected() (tea.Model, tea.Cmd) {
	if m.focus == paneMail {
		selected, ok := m.selectedMail()
		if !ok {
			return m, nil
		}
		if target, ok := m.threadOpenTarget(); ok { // a collapsed conversation opens its newest unread
			selected = target
		}
		if isDraft(selected) {
			return m.openDraft(selected)
		}
		m.snapshotReaderOrder()
		return m, m.showMessage(selected, "")
	}
	if _, ok := m.selectedEvent(); ok {
		m.screen = screenEvent
		m.detailScroll = 0
		m.status = ""
	}
	return m, nil
}

func (m Model) toggleSelectedUnread() (tea.Model, tea.Cmd) {
	if m.focus != paneMail && m.screen != screenMail {
		return m, nil
	}
	selected, ok := m.selectedMail()
	if !ok || m.refuseDraft(selected) {
		return m, nil
	}
	m.status = "Updating message"
	advance := m.screen == screenMail
	return m, func() tea.Msg {
		updated, err := m.mailStore.SetUnread(selected, !selected.Unread)
		return messageChangedMsg{oldPath: selected.Path, message: updated, advance: advance, err: err}
	}
}

func (m Model) archiveSelected() (tea.Model, tea.Cmd) {
	if m.focus != paneMail && m.screen != screenMail {
		return m, nil
	}
	selected, ok := m.selectedMail()
	if !ok || m.refuseDraft(selected) {
		return m, nil
	}
	if m.pendingArchive != selected.Path {
		m.pendingArchive = selected.Path
		m.status = "Press a again to archive this message"
		return m, nil
	}
	m.status = "Archiving message"
	return m, archiveCmd(m.mailStore, selected)
}

// refuseDraft says so when an action meant for received mail is aimed at a
// draft row, whose path is a .md file the mail store knows nothing about.
func (m *Model) refuseDraft(message maildir.Message) bool {
	if !isDraft(message) {
		return false
	}
	m.status = "This is a draft; enter edits it"
	return true
}

// archiveCmd moves a message to Archive through MoveTo, which returns the new
// path that undo needs.
func archiveCmd(store MailStore, selected maildir.Message) tea.Cmd {
	return func() tea.Msg {
		newPath, err := store.MoveTo(selected, maildir.ArchiveBox)
		return messageArchivedMsg{message: selected, newPath: newPath, err: err}
	}
}

func (m Model) startFiling() (tea.Model, tea.Cmd) {
	// A thread is filed only when f is pressed on its collapsed row (the
	// caller sets this after); a leftover from a cancelled f must not turn
	// filing one message into filing the whole conversation.
	m.threads.filingGroup = nil
	if m.focus != paneMail && m.screen != screenMail {
		return m, nil
	}
	selected, ok := m.selectedMail()
	if !ok {
		return m, nil
	}
	// Sent and Drafts stay where they are; Archive and Trash mail can be
	// filed back out.
	if box := messageBox(selected); box == maildir.SentBox || box == draftsBox {
		m.status = boxLabel(box) + " is read-only"
		return m, nil
	}
	m.fileTargets = nil
	for _, box := range m.boxes {
		// Filing into @Snoozed by hand would leave a message no entry brings back.
		if box != messageBox(selected) && box != maildir.SentBox && box != maildir.ArchiveBox && box != draftsBox && box != snoozedBox {
			m.fileTargets = append(m.fileTargets, box)
		}
	}
	if messageBox(selected) != maildir.ArchiveBox {
		m.fileTargets = append(m.fileTargets, maildir.ArchiveBox)
	}
	m.fileKeys = fileShortcuts(m.fileTargets)
	m.fileCursor = 0
	m.filing = true
	m.pendingArchive = ""
	m.status = ""
	return m, nil
}

// fixedShortcuts keeps each tag on the same key every time, so filing
// becomes muscle memory: f r is Reply, f x is Archive. [mail].box_keys in
// config.toml adds your own folders ("@Research" = "e").
func fixedShortcuts() map[string]string {
	keys := map[string]string{maildir.InboxBox: "i", "@Reply": "r", "@Waiting": "w", maildir.ArchiveBox: "x"}
	for box, key := range config.Get().Mail.BoxKeys {
		keys[box] = key
	}
	return keys
}

// fileShortcuts gives every target a key: its fixed one, else the first free
// letter of its name, else a digit. f and q stay free to close the picker.
func fileShortcuts(targets []string) []string {
	used := map[string]bool{"f": true, "q": true}
	keys := make([]string, len(targets))
	for index, target := range targets {
		if key, ok := fixedShortcuts()[target]; ok && !used[key] {
			keys[index], used[key] = key, true
		}
	}
	digit := 1
	for index, target := range targets {
		if keys[index] != "" {
			continue
		}
		for _, r := range strings.ToLower(boxLabel(target)) {
			if key := string(r); r >= 'a' && r <= 'z' && !used[key] {
				keys[index], used[key] = key, true
				break
			}
		}
		if keys[index] == "" && digit <= 9 {
			keys[index] = strconv.Itoa(digit)
			digit++
		}
	}
	return keys
}

func (m Model) handleFileKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := message.String()
	if index := slices.Index(m.fileKeys, key); index >= 0 && key != "" {
		m.fileCursor = index
		return m.fileSelected()
	}
	switch key {
	case "esc", "q", "f":
		m.filing = false
	case "up", "ctrl+p":
		m.fileCursor = (m.fileCursor - 1 + len(m.fileTargets)) % len(m.fileTargets)
	case "down", "ctrl+n":
		m.fileCursor = (m.fileCursor + 1) % len(m.fileTargets)
	case "enter":
		return m.fileSelected()
	}
	return m, nil
}

func (m Model) fileSelected() (tea.Model, tea.Cmd) {
	{
		selected, ok := m.selectedMail()
		if !ok || len(m.fileTargets) == 0 {
			m.filing = false
			return m, nil
		}
		box := m.fileTargets[m.fileCursor]
		if command, ok := m.fileThread(box); ok { // threads_actions.go: f on a collapsed conversation
			return m, command
		}
		m.status = "Filing to " + boxLabel(box)
		if box == maildir.ArchiveBox {
			m.filing = false
			return m, archiveCmd(m.mailStore, selected)
		}
		store := m.mailStore
		return m, func() tea.Msg {
			newPath, err := store.MoveTo(selected, box)
			return messageFiledMsg{message: selected, newPath: newPath, box: box, err: err}
		}
	}
}

func (m Model) openExternal() (tea.Model, tea.Cmd) {
	if m.screen == screenEvent || (m.screen == screenHome && m.focus == paneAgenda) {
		return m, launchDetached(m.options.CalendarCommand, "Calendar")
	}
	return m.openMailApp(true)
}

func (m Model) openMailApp(useSelectedMailbox bool) (tea.Model, tea.Cmd) {
	command := append([]string(nil), m.options.MailCommand...)
	if len(command) == 0 {
		m.status = "No mail command configured"
		return m, nil
	}
	if useSelectedMailbox {
		if selected, ok := m.selectedMail(); ok {
			mailbox := filepath.Dir(filepath.Dir(selected.Path))
			command = append(command, "-f", mailbox)
		}
	}
	path, err := exec.LookPath(command[0])
	if err != nil {
		m.status = "Mail app is unavailable: " + command[0]
		return m, nil
	}
	cmd := exec.Command(path, command[1:]...)
	m.status = "Opening NeoMutt"
	return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
		return externalFinishedMsg{name: "NeoMutt", err: err}
	})
}

func (m Model) loadMailCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		result, err := m.mailStore.List(ctx)
		return mailLoadedMsg{result: result, err: err}
	}
}

func (m Model) quietLoadMailCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		result, err := m.mailStore.List(ctx)
		return mailLoadedMsg{result: result, err: err, quiet: true}
	}
}

func (m Model) loadCalendarCmd() tea.Cmd {
	from, to := m.calendarRange()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		result, err := m.calendarStore.Load(ctx, from, to)
		return calendarLoadedMsg{result: result, err: err, from: from, to: to}
	}
}

func (m Model) quietLoadCalendarCmd() tea.Cmd {
	from, to := m.calendarRange()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		result, err := m.calendarStore.Load(ctx, from, to)
		return calendarLoadedMsg{result: result, err: err, quiet: true, from: from, to: to}
	}
}

func dayOf(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// bodyReader is maildir.Store's Read for a path captured earlier: it finds
// the message under its new name when the file was renamed meanwhile.
type bodyReader interface {
	ReadResolved(path string) (maildir.Content, string, error)
}

func loadBodyCmd(store MailStore, path string) tea.Cmd {
	return func() tea.Msg {
		if reader, ok := storeAs[bodyReader](store); ok {
			content, used, err := reader.ReadResolved(path)
			return bodyLoadedMsg{path: path, usedPath: used, content: content, err: err}
		}
		content, err := store.Read(path)
		return bodyLoadedMsg{path: path, usedPath: path, content: content, err: err}
	}
}

func runCommand(command []string, env []string, timeout time.Duration) tea.Cmd {
	return func() tea.Msg {
		if len(command) == 0 {
			return syncFinishedMsg{err: errors.New("no sync command configured")}
		}
		path, err := exec.LookPath(command[0])
		if err != nil {
			return syncFinishedMsg{err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, path, command[1:]...)
		if len(env) > 0 {
			cmd.Env = append(os.Environ(), env...)
		}
		output, err := cmd.CombinedOutput()
		text := terminal.SanitizeLine(strings.TrimSpace(string(output)))
		if runes := []rune(text); len(runes) > 500 {
			text = string(runes[len(runes)-500:])
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return syncFinishedMsg{output: text, err: err}
	}
}

func launchDetached(command []string, name string) tea.Cmd {
	return func() tea.Msg {
		if len(command) == 0 {
			return externalFinishedMsg{name: name, err: errors.New("no command configured")}
		}
		path, err := exec.LookPath(command[0])
		if err != nil {
			return externalFinishedMsg{name: name, err: err}
		}
		cmd := exec.Command(path, command[1:]...)
		if err := cmd.Start(); err != nil {
			return externalFinishedMsg{name: name, err: err}
		}
		_ = cmd.Process.Release()
		return externalFinishedMsg{name: name}
	}
}

func (m *Model) updateReadyStatus() {
	if m.loadingMail || m.loadingCal {
		return
	}
	if m.mailLoadErr != "" {
		m.status = m.mailLoadErr
		return
	}
	if m.calendarLoadErr != "" {
		m.status = m.calendarLoadErr
		return
	}
	m.status = m.pendingStatus
	m.pendingStatus = ""
	if m.status != "" {
		return
	}
	warningCount := len(m.mailWarnings) + len(m.calendarWarnings)
	if warningCount > 0 {
		m.status = fmt.Sprintf("Loaded with %d warning(s)", warningCount)
	}
}

func (m *Model) boundCursors() {
	m.mailCursor = bound(m.mailCursor, len(m.filteredMessageIndexes()))
	m.boundEventCursor() // upcoming.go: also allows a snoozed row
}

// matchingMessageIndexes is the messages the box, account and search show,
// unread first; mailRows (threads.go) groups them into conversations.
func (m Model) matchingMessageIndexes() []int {
	query := strings.ToLower(strings.TrimSpace(m.query))
	unread := make([]int, 0, len(m.messages))
	seen := make([]int, 0, len(m.messages))
	var binned []int
	box := m.activeBox()
	for index, message := range m.messages {
		if m.mailAccount >= 0 && m.mailAccount < len(m.accounts) && message.Account != m.accounts[m.mailAccount] {
			continue
		}
		// A search looks through every box, so filed mail stays findable.
		if query == "" && messageBox(message) != box {
			continue
		}
		matches := query == "" || strings.Contains(strings.ToLower(message.Subject), query) || strings.Contains(strings.ToLower(message.From), query) || strings.Contains(strings.ToLower(message.FromAddr), query) || strings.Contains(strings.ToLower(message.Account), query) ||
			m.searchHit(message)
		if !matches {
			continue
		}
		if query != "" && isBinBox(messageBox(message)) {
			binned = append(binned, index)
			continue
		}
		if message.Unread {
			unread = append(unread, index)
		} else {
			seen = append(seen, index)
		}
	}
	if query != "" {
		// Search results: newest first, Trash and Spam after the rest.
		newest := func(indexes []int) {
			sort.SliceStable(indexes, func(i, j int) bool {
				return m.messages[indexes[i]].Date.After(m.messages[indexes[j]].Date)
			})
		}
		newest(unread)
		newest(seen)
		newest(binned)
	}
	return m.orderSnoozed(append(append(unread, seen...), binned...))
}

func isBinBox(box string) bool {
	lower := strings.ToLower(box)
	return strings.Contains(lower, "trash") || strings.Contains(lower, "spam") || strings.Contains(lower, "junk") || strings.Contains(lower, "deleted")
}

func (m *Model) cacheBody() {
	column, _ := readerColumn(max(m.width, 20))
	m.bodyCache = readerBodyLines(*m.content, column, m.showHistory)
	m.bodyCacheColumn = column
}

// recacheBodyForWidth re-renders the body only when the reading column
// changed; wider terminals only move the margin.
func (m *Model) recacheBodyForWidth() {
	if column, _ := readerColumn(max(m.width, 20)); m.content != nil && column != m.bodyCacheColumn {
		m.cacheBody()
	}
}

func (m Model) activeBox() string {
	if m.mailBox >= 0 && m.mailBox < len(m.boxes) {
		return m.boxes[m.mailBox]
	}
	return maildir.InboxBox
}

func messageBox(message maildir.Message) string {
	if message.Box == "" {
		return maildir.InboxBox
	}
	return message.Box
}

// boxLabel drops the "@" the tag folders carry on the server.
func boxLabel(box string) string {
	return strings.TrimPrefix(box, "@")
}

func defaultCalendarCommand() []string {
	if runtime.GOOS == "darwin" {
		return []string{"open", "-a", "Calendar"}
	}
	return []string{"evolution", "--component=calendar"}
}

var linkPattern = regexp.MustCompile(`https?://[^\s<>"')\]]+`)

// meetingLink finds the video-call link of an event (J in the event view): Teams, Zoom, Meet or
// Webex first, any other web address in the location or notes after that.
func meetingLink(event calendar.Event) string {
	links := linkPattern.FindAllString(event.Location+"\n"+event.Description, -1)
	for _, link := range links {
		for _, host := range []string{"teams.microsoft.com", "teams.live.com", "zoom.us", "meet.google.com", "webex.com", "whereby.com"} {
			if strings.Contains(link, host) {
				return link
			}
		}
	}
	if len(links) > 0 && strings.HasPrefix(event.Location, "http") {
		return links[0]
	}
	return ""
}

func openCommand() []string {
	if runtime.GOOS == "darwin" {
		return []string{"open"}
	}
	return []string{"xdg-open"}
}

func calendarAppName() string {
	if runtime.GOOS == "darwin" {
		return "Calendar"
	}
	return "Evolution"
}

// defaultSyncEnv lets mbsync find the XOAUTH2 plugin on macOS. Homebrew's
// isync links the system libsasl2, which looks for plugins only on SASL_PATH.
func defaultSyncEnv() []string {
	if runtime.GOOS != "darwin" || os.Getenv("SASL_PATH") != "" {
		return []string{}
	}
	const plugins = "/opt/homebrew/opt/cyrus-sasl/lib/sasl2"
	if info, err := os.Stat(plugins); err == nil && info.IsDir() {
		return []string{"SASL_PATH=" + plugins}
	}
	return []string{}
}

func (m Model) activeAccountLabel() string {
	if m.mailAccount >= 0 && m.mailAccount < len(m.accounts) {
		return m.accounts[m.mailAccount]
	}
	return "All Accounts"
}

func (m *Model) cycleMailAccount(delta int) {
	total := len(m.accounts) + 1
	if total <= 1 {
		return
	}
	position := m.mailAccount + 1
	position = (position + delta + total) % total
	m.mailAccount = position - 1
	m.mailCursor = 0
	m.status = m.activeAccountLabel()
}

func (m *Model) selectMailAccountKey(key string) {
	value, err := strconv.Atoi(key)
	if err != nil || value < 1 || value > len(m.accounts)+1 {
		return
	}
	m.mailAccount = value - 2
	m.mailCursor = 0
	m.status = m.activeAccountLabel()
}

func (m Model) calendarDate() time.Time {
	if !m.calendarAnchor.IsZero() {
		return m.calendarAnchor
	}
	now := m.now()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
}

func weekStartDate(value time.Time, firstDay time.Weekday) time.Time {
	day := time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, value.Location())
	difference := (int(day.Weekday()) - int(firstDay) + 7) % 7
	return day.AddDate(0, 0, -difference)
}

func (m Model) calendarRange() (time.Time, time.Time) {
	anchor := m.calendarDate()
	switch m.calendarMode {
	case calendarDay:
		return anchor, anchor.AddDate(0, 0, 1)
	case calendarWeek:
		start := weekStartDate(anchor, firstWeekday)
		return start, start.AddDate(0, 0, 7)
	default:
		return anchor, anchor.AddDate(0, 0, m.options.Days)
	}
}

func (m Model) shiftCalendar(delta int) (tea.Model, tea.Cmd) {
	anchor := m.calendarDate()
	days := 1
	switch m.calendarMode {
	case calendarWeek:
		days = 7
	case calendarAgenda:
		days = m.options.Days
	}
	m.calendarAnchor = anchor.AddDate(0, 0, delta*days)
	m.eventCursor = 0
	m.loadingCal = true
	m.status = "Reading " + m.calendarMode.String()
	return m, m.loadCalendarCmd()
}

func (m *Model) setMailCursorToPath(path string) {
	if path == "" {
		return
	}
	indexes := m.filteredMessageIndexes()
	for cursor, index := range indexes {
		if m.messages[index].Path == path {
			m.mailCursor = cursor
			return
		}
	}
	// The file was renamed (flags changed elsewhere, mbsync added a UID):
	// follow the message, not the old name.
	key := maildir.Key(path)
	for cursor, index := range indexes {
		if maildir.Key(m.messages[index].Path) == key {
			m.mailCursor = cursor
			return
		}
	}
	if cursor, ok := m.threadRowOf(path); ok { // inside a collapsed conversation
		m.mailCursor = cursor
	}
}

// findMessage returns the index of the message with path's Maildir key.
func (m Model) findMessage(path string) int {
	key := maildir.Key(path)
	for index := range m.messages {
		if m.messages[index].Path == path {
			return index
		}
	}
	for index := range m.messages {
		if maildir.Key(m.messages[index].Path) == key {
			return index
		}
	}
	return -1
}

// readerReady checks, before an action in the reader, that the message on
// screen is still in the list.
func (m *Model) readerReady() bool {
	if m.screen != screenMail || m.contentPath == "" {
		return true
	}
	if m.readerGone {
		m.status = "This message was moved or deleted elsewhere"
		return false
	}
	if _, ok := m.selectedMail(); ok {
		return true
	}
	m.status = "This message is no longer in the list; press esc"
	return false
}

// selectedMail is the message actions apply to: the one open in the reader,
// found by its Maildir key so a rename does not lose it, else the one under
// the list cursor.
func (m Model) selectedMail() (maildir.Message, bool) {
	if m.screen == screenMail && m.contentPath != "" {
		if index := m.findMessage(m.contentPath); index >= 0 {
			return m.messages[index], true
		}
		return maildir.Message{}, false
	}
	return m.cursorMail()
}

// cursorMail is the message under the list cursor.
func (m Model) cursorMail() (maildir.Message, bool) {
	indexes := m.filteredMessageIndexes()
	if len(indexes) == 0 || m.mailCursor < 0 || m.mailCursor >= len(indexes) {
		return maildir.Message{}, false
	}
	return m.messages[indexes[m.mailCursor]], true
}

func (m Model) selectedEvent() (calendar.Event, bool) {
	if len(m.events) == 0 || m.eventCursor < 0 || m.eventCursor >= len(m.events) {
		return calendar.Event{}, false
	}
	return m.events[m.eventCursor], true
}

func (m *Model) moveCursor(delta int) {
	m.setCursor(m.activeCursor() + delta)
}

func (m *Model) setCursor(value int) {
	if m.focus == paneMail {
		m.mailCursor = bound(value, len(m.filteredMessageIndexes()))
	} else {
		m.eventCursor = bound(value, len(m.events))
	}
	m.status = ""
}

func (m Model) activeCursor() int {
	if m.focus == paneMail {
		return m.mailCursor
	}
	return m.eventCursor
}

func (m Model) activeLength() int {
	if m.focus == paneMail {
		return len(m.filteredMessageIndexes())
	}
	return len(m.events)
}

func bound(value, length int) int {
	if length <= 0 || value < 0 {
		return 0
	}
	if value >= length {
		return length - 1
	}
	return value
}

func trimLastRune(value string) string {
	runes := []rune(value)
	if len(runes) == 0 {
		return ""
	}
	return string(runes[:len(runes)-1])
}
