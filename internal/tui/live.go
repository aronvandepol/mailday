package tui

// Live mail: the list follows the Maildirs on disk without a keypress, local
// changes are pushed to the server at once through mailday-syncd, and the
// footer shows how fresh the mail is.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/fsnotify/fsnotify"

	"github.com/aronvandepol/mailday/internal/maildir"
	"github.com/aronvandepol/mailday/internal/syncd"
	"github.com/aronvandepol/mailday/internal/terminal"
)

// mailSettle merges the burst of renames one sync or keypress causes into a
// single reload; mailSettleMax caps the wait while mbsync keeps writing.
const (
	mailSettle    = 300 * time.Millisecond
	mailSettleMax = 1500 * time.Millisecond
)

type mailChangedMsg struct{}

type calendarChangedMsg struct{}

type syncStatusMsg struct {
	status syncd.Status
	err    error
}

type syncRetryMsg struct{}

type clockTickMsg struct{}

// live holds the long-lived parts behind the value-typed Model.
type live struct {
	changes         chan struct{}
	calendarChanges chan struct{}

	mu           sync.Mutex
	subscription *syncd.Subscription
	// watchFailed holds the paths the mail watcher could not watch, so the
	// footer can say the list only follows some folders live.
	watchFailed map[string]bool
}

// addWatch is fsnotify's Add; tests replace it to make adding fail.
var addWatch = func(watcher *fsnotify.Watcher, path string) error { return watcher.Add(path) }

// watchFailures is the number of folders the mail watcher could not watch,
// typically because the system ran out of file descriptors or inotify
// watches.
func (l *live) watchFailures() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.watchFailed)
}

func (l *live) noteWatch(path string, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err == nil {
		delete(l.watchFailed, path)
		return
	}
	if l.watchFailed == nil {
		l.watchFailed = map[string]bool{}
	}
	l.watchFailed[path] = true
}

func newLive() *live {
	return &live{changes: make(chan struct{}, 1), calendarChanges: make(chan struct{}, 1)}
}

// startMailWatch watches every cur/ and new/ under the roots, and the account
// directories so a folder mbsync creates is picked up too. Folders nested one
// level down, such as "[Gmail]/Sent Mail", are watched as well. Folders that
// cannot be watched are counted in watchFailures.
func (l *live) startMailWatch(roots []string) {
	if l == nil || len(roots) == 0 {
		return
	}
	// kqueue holds a descriptor per watched file, and macOS starts at 256.
	raiseOpenFileLimit()
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return
	}
	watched := map[string]bool{}
	add := func(path string) bool {
		if watched[path] {
			return true
		}
		err := addWatch(watcher, path)
		l.noteWatch(path, err)
		if err != nil {
			return false
		}
		watched[path] = true
		return true
	}
	addFolder := func(folder string) bool {
		found := false
		for _, leaf := range []string{"cur", "new"} {
			if info, err := os.Stat(filepath.Join(folder, leaf)); err == nil && info.IsDir() {
				add(filepath.Join(folder, leaf))
				found = true
			}
		}
		return found
	}
	// addTree watches path if it is a Maildir. Otherwise, while depth allows,
	// it watches path itself, to see folders created in it, and looks for
	// Maildirs inside it.
	var addTree func(path string, depth int) bool
	addTree = func(path string, depth int) bool {
		if addFolder(path) {
			return true
		}
		if depth == 0 || strings.HasPrefix(filepath.Base(path), ".") {
			return false
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return false
		}
		found := false
		for _, entry := range entries {
			if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") && addTree(filepath.Join(path, entry.Name()), depth-1) {
				found = true
			}
		}
		if found {
			add(path)
		}
		return found
	}
	addAccount := func(account string) {
		add(account)
		entries, _ := os.ReadDir(account)
		for _, entry := range entries {
			if entry.IsDir() {
				addTree(filepath.Join(account, entry.Name()), 1)
			}
		}
	}
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		add(root)
		for _, entry := range entries {
			if entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
				addAccount(filepath.Join(root, entry.Name()))
			}
		}
	}
	isRoot := func(path string) bool { return slices.Contains(roots, path) }

	go func() {
		defer watcher.Close()
		var timer, deadline <-chan time.Time
		fire := func() {
			timer, deadline = nil, nil
			select {
			case l.changes <- struct{}{}:
			default:
			}
		}
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				name := filepath.Base(event.Name)
				parent := filepath.Dir(event.Name)
				leaf := filepath.Base(parent)
				switch {
				case leaf == "cur" || leaf == "new":
					// A message moved, arrived, changed flags or went away.
				case event.Has(fsnotify.Create) && isRoot(parent):
					addAccount(event.Name)
					continue
				case event.Has(fsnotify.Create) && !strings.HasPrefix(name, "."):
					// A new folder in an account, or in a folder such as
					// "[Gmail]"; mbsync makes cur/new right after.
					time.Sleep(50 * time.Millisecond)
					depth := 0
					if isRoot(filepath.Dir(parent)) {
						depth = 1
					}
					addTree(event.Name, depth)
					if !watched[filepath.Join(event.Name, "cur")] {
						continue
					}
				default:
					continue // .mbsyncstate, .uidvalidity and friends
				}
				timer = time.After(mailSettle)
				if deadline == nil {
					deadline = time.After(mailSettleMax)
				}
			case <-timer:
				fire()
			case <-deadline:
				fire()
			case _, ok := <-watcher.Errors:
				if !ok {
					return
				}
			}
		}
	}()
}

// startCalendarWatch reloads the calendar when mailday-calsync rewrites its
// ICS files or Evolution refreshes a cache. Evolution writes in bursts, so
// changes settle for two seconds.
func (l *live) startCalendarWatch(dirs []string) {
	if l == nil || len(dirs) == 0 {
		return
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return
	}
	added := 0
	for _, dir := range dirs {
		if watcher.Add(dir) == nil {
			added++
		}
	}
	if added == 0 {
		watcher.Close()
		return
	}
	go func() {
		defer watcher.Close()
		var timer <-chan time.Time
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				name := filepath.Base(event.Name)
				if strings.HasSuffix(name, ".ics") || strings.HasPrefix(name, "cache.db") {
					timer = time.After(2 * time.Second)
				}
			case <-timer:
				timer = nil
				select {
				case l.calendarChanges <- struct{}{}:
				default:
				}
			case _, ok := <-watcher.Errors:
				if !ok {
					return
				}
			}
		}
	}()
}

func (l *live) waitCalendarChangeCmd() tea.Cmd {
	if l == nil {
		return nil
	}
	return func() tea.Msg {
		<-l.calendarChanges
		return calendarChangedMsg{}
	}
}

// pullSent asks mailday-syncd to fetch the Sent folder of the account that
// just sent, once the server has filed its copy.
func pullSent(store MailStore, labels []string) {
	finder, ok := store.(interface{ AccountDir(string) (string, error) })
	if !ok {
		if wrapped, isPushing := store.(pushingStore); isPushing {
			finder, ok = wrapped.MailStore.(interface{ AccountDir(string) (string, error) })
		}
	}
	if !ok {
		return
	}
	var folders []string
	for _, label := range labels {
		dir, err := finder.AccountDir(label)
		if err != nil {
			continue
		}
		for _, name := range []string{"Sent", "Sent Items", "[Gmail]/Sent Mail", "Sent Messages"} {
			if info, err := os.Stat(filepath.Join(dir, name, "cur")); err == nil && info.IsDir() {
				folders = append(folders, filepath.Join(dir, name))
			}
		}
		break
	}
	if len(folders) == 0 {
		return
	}
	go func() {
		// Gmail and Exchange file the copy a moment after SMTP accepts it.
		for _, delay := range []time.Duration{3 * time.Second, 15 * time.Second} {
			time.Sleep(delay)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_, _ = syncd.Send(ctx, syncd.Request{Op: "sync", Paths: folders})
			cancel()
		}
	}()
}

func (l *live) waitMailChangeCmd() tea.Cmd {
	if l == nil {
		return nil
	}
	return func() tea.Msg {
		<-l.changes
		return mailChangedMsg{}
	}
}

// subscribeCmd connects to mailday-syncd, or reports that it is not running.
func (l *live) subscribeCmd() tea.Cmd {
	if l == nil {
		return nil
	}
	return func() tea.Msg {
		subscription, err := syncd.Subscribe()
		if err != nil {
			return syncStatusMsg{err: err}
		}
		l.mu.Lock()
		if l.subscription != nil {
			l.subscription.Close()
		}
		l.subscription = subscription
		l.mu.Unlock()
		status, err := subscription.Next()
		return syncStatusMsg{status: status, err: err}
	}
}

func (l *live) nextStatusCmd() tea.Cmd {
	return func() tea.Msg {
		l.mu.Lock()
		subscription := l.subscription
		l.mu.Unlock()
		if subscription == nil {
			return syncStatusMsg{err: syncd.ErrNotRunning}
		}
		status, err := subscription.Next()
		return syncStatusMsg{status: status, err: err}
	}
}

func syncRetryCmd() tea.Cmd {
	return tea.Tick(10*time.Second, func(time.Time) tea.Msg { return syncRetryMsg{} })
}

func clockTickCmd() tea.Cmd {
	return tea.Tick(15*time.Second, func(time.Time) tea.Msg { return clockTickMsg{} })
}

// daemonSyncCmd asks mailday-syncd for a full sync and waits for it; without
// the daemon it falls back to running the sync command directly.
func daemonSyncCmd(fallback []string, env []string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		_, err := syncd.Send(ctx, syncd.Request{Op: "sync", Wait: true})
		if errors.Is(err, syncd.ErrNotRunning) {
			return runCommand(fallback, env, 5*time.Minute)()
		}
		if err != nil {
			return syncFinishedMsg{output: err.Error(), err: err}
		}
		return syncFinishedMsg{}
	}
}

// newInboxMail lists unread inbox messages in after that were not in before,
// matched by Maildir key so a rename is not news.
func newInboxMail(before, after []maildir.Message) []maildir.Message {
	known := make(map[string]bool, len(before))
	for _, message := range before {
		known[maildir.Key(message.Path)] = true
	}
	var arrived []maildir.Message
	for _, message := range after {
		if message.Unread && message.Box == maildir.InboxBox && !known[maildir.Key(message.Path)] {
			arrived = append(arrived, message)
		}
	}
	return arrived
}

func arrivalNotice(arrived []maildir.Message) string {
	first := arrived[0]
	for _, message := range arrived[1:] {
		if message.Date.After(first.Date) {
			first = message
		}
	}
	from := first.From
	if from == "" {
		from = first.Account
	}
	notice := "New: " + terminal.SanitizeLine(from) + " — " + terminal.SanitizeLine(first.Subject)
	if len(arrived) > 1 {
		notice += fmt.Sprintf(" (+%d more)", len(arrived)-1)
	}
	return notice
}

// windowTitle puts the unread inbox count in the terminal or tab title.
func (m Model) windowTitle() string {
	unread := 0
	for _, message := range m.messages {
		if message.Unread && message.Box == maildir.InboxBox {
			unread++
		}
	}
	if unread == 0 {
		return "Mailday"
	}
	return fmt.Sprintf("Mailday (%d)", unread)
}

// syncLabel sums up the daemon's state for the footer rule.
func (m Model) syncLabel() string {
	if m.syncDown {
		return "sync daemon off · s syncs"
	}
	if len(m.syncStatus.Accounts) == 0 {
		return ""
	}
	var syncing, failing, offline, unreachable []string
	var oldest time.Time
	for _, account := range m.syncStatus.Accounts {
		if account.Syncing {
			syncing = append(syncing, account.Name)
		}
		if account.LastError != "" {
			if isNetworkError(account.LastError) {
				unreachable = append(unreachable, account.Name)
			} else {
				failing = append(failing, account.Name)
			}
		}
		if len(account.Offline) > 0 && len(account.Watching) == 0 {
			offline = append(offline, account.Name)
		}
		if oldest.IsZero() || account.LastSync.Before(oldest) {
			oldest = account.LastSync
		}
	}
	switch {
	case len(unreachable) == len(m.syncStatus.Accounts):
		return "offline · changes are kept and sent when back"
	case len(failing) > 0:
		return strings.Join(failing, ", ") + " sync failed · s retries"
	case len(syncing) > 0:
		return "syncing " + strings.Join(syncing, ", ")
	case len(unreachable) > 0:
		return strings.Join(unreachable, ", ") + " unreachable · retrying"
	case len(offline) > 0:
		return strings.Join(offline, ", ") + " offline"
	case oldest.IsZero():
		return "connecting"
	case allWatching(m.syncStatus):
		// IDLE plus a NOOP every 30 seconds: the inbox is current.
		return "live"
	default:
		return "synced " + ago(m.now().Sub(oldest))
	}
}

// isNetworkError recognises mbsync's messages for a missing network.
func isNetworkError(text string) bool {
	text = strings.ToLower(text)
	for _, marker := range []string{"cannot resolve", "connect", "network is unreachable", "timed out", "timeout", "socket error", "connection reset", "broken pipe", "temporary failure in name resolution", "no route to host", "nodename nor servname"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func allWatching(status syncd.Status) bool {
	for _, account := range status.Accounts {
		if len(account.Watching) == 0 || len(account.Offline) > 0 {
			return false
		}
	}
	return len(status.Accounts) > 0
}

// recoveredSyncError says the status line shows a sync failure of an account
// that has synced since: a failure that fixed itself should not linger.
func recoveredSyncError(status string, current syncd.Status) bool {
	for _, account := range current.Accounts {
		if account.LastError == "" && strings.Contains(status, account.Name+" sync failed: ") {
			return true
		}
	}
	return false
}

// newSyncErrors returns errors that were not in the previous status, so each
// failure is announced once rather than on every update.
func newSyncErrors(previous, current syncd.Status) string {
	before := map[string]string{}
	for _, account := range previous.Accounts {
		before[account.Name] = account.LastError
	}
	var parts []string
	for _, account := range current.Accounts {
		if account.LastError != "" && account.LastError != before[account.Name] && !isNetworkError(account.LastError) {
			parts = append(parts, account.Name+" sync failed: "+account.LastError)
		}
	}
	if current.Calendar.LastError != "" && current.Calendar.LastError != previous.Calendar.LastError && !isNetworkError(current.Calendar.LastError) {
		parts = append(parts, "calendar sync failed: "+current.Calendar.LastError)
	}
	return strings.Join(parts, " · ")
}

func ago(duration time.Duration) string {
	switch {
	case duration < 10*time.Second:
		return "just now"
	case duration < time.Minute:
		return fmt.Sprintf("%ds ago", int(duration.Seconds()))
	case duration < time.Hour:
		return fmt.Sprintf("%dm ago", int(duration.Minutes()))
	default:
		return fmt.Sprintf("%dh ago", int(duration.Hours()))
	}
}

// pushingStore tells mailday-syncd which Maildir folders a change touched, so
// it reaches the server within a second instead of at the next sync.
type pushingStore struct {
	MailStore
	push func(paths ...string)
	move func(oldPath, newPath string)
}

func withPush(store MailStore) MailStore {
	return pushingStore{MailStore: store, push: pushToDaemon, move: moveOnServer}
}

// moveOnServer asks mailday-syncd to move the message on the server; when it
// cannot, both folders are pushed the old way (upload and expunge).
func moveOnServer(oldPath, newPath string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := syncd.Send(ctx, syncd.Request{Op: "move", Paths: []string{oldPath, newPath}}); err != nil && !errors.Is(err, syncd.ErrNotRunning) {
			_, _ = syncd.Send(ctx, syncd.Request{Op: "sync", Paths: []string{folderOf(oldPath), folderOf(newPath)}, Push: true})
		}
	}()
}

func pushToDaemon(paths ...string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = syncd.Send(ctx, syncd.Request{Op: "sync", Paths: paths, Push: true})
	}()
}

func folderOf(path string) string {
	return filepath.Dir(filepath.Dir(path))
}

func (s pushingStore) SetUnread(message maildir.Message, unread bool) (maildir.Message, error) {
	changed, err := s.MailStore.SetUnread(message, unread)
	if err == nil && changed.Path != message.Path {
		s.push(folderOf(message.Path))
	}
	return changed, err
}

// Archive goes through MoveTo so the server move applies to it too.
func (s pushingStore) Archive(message maildir.Message) error {
	_, err := s.MoveTo(message, maildir.ArchiveBox)
	return err
}

func (s pushingStore) MoveTo(message maildir.Message, box string) (string, error) {
	newPath, err := s.MailStore.MoveTo(message, box)
	if err == nil {
		if s.move != nil {
			s.move(message.Path, newPath)
		} else {
			s.push(folderOf(message.Path), folderOf(newPath))
		}
	}
	return newPath, err
}

func (s pushingStore) MarkAnswered(path string) (string, error) {
	newPath, err := s.MailStore.MarkAnswered(path)
	if err == nil && newPath != path {
		s.push(folderOf(path))
	}
	return newPath, err
}

func (s pushingStore) SaveSent(label string, message []byte) error {
	err := s.MailStore.SaveSent(label, message)
	if err == nil {
		if finder, ok := s.MailStore.(interface{ AccountDir(string) (string, error) }); ok {
			if dir, findErr := finder.AccountDir(label); findErr == nil {
				s.push(filepath.Join(dir, "Sent"))
			}
		}
	}
	return err
}
