package syncd

// Snooze: Mailday files a message in the account's "@Snoozed" box and writes
// a due time to a list that Syncthing shares between the machines. Whichever
// daemon sees an entry fall due moves the message back to INBOX on the
// server, unread; the other machine then finds nothing and only drops the
// entry.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/aronvandepol/mailday/internal/config"
	"log"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/aronvandepol/mailday/internal/maildir"
)

// SnoozedBox is the tag folder snoozed mail waits in. mbsync's "@*" tag
// channels create it on the server too.
const SnoozedBox = "@Snoozed"

// snoozeUploadGrace is how long a due message may be missing from @Snoozed
// before the entry is dropped as "moved by someone else": until mbsync has
// uploaded it the server has no copy, and dropping then would strand it.
const snoozeUploadGrace = 15 * time.Minute

// snoozeStrandLimit is how long a snooze may wait for its message to leave
// the box it was snoozed from before the entry is given up.
const snoozeStrandLimit = 7 * 24 * time.Hour

// A SnoozeEntry is one snoozed message. Entries are keyed by account and
// Message-ID.
type SnoozeEntry struct {
	MessageID string    `json:"messageID"`
	Account   string    `json:"account"` // as Mailday labels it (the Maildir folder's label)
	Due       time.Time `json:"due"`
	FromBox   string    `json:"fromBox"`
	Subject   string    `json:"subject"`
	Added     time.Time `json:"added"`
	// Host is the machine that snoozed it: only it can tell "not uploaded
	// yet" (its own @Snoozed still holds the message) from "already back".
	Host string `json:"host,omitempty"`
}

// SnoozeHost names this machine in snooze entries.
func SnoozeHost() string {
	host, _ := os.Hostname()
	return strings.TrimSuffix(host, ".local")
}

// Key identifies the message across machines: Message-IDs compare without
// case, angle brackets or padding.
func (e SnoozeEntry) Key() string { return SnoozeKey(e.Account, e.MessageID) }

func SnoozeKey(account, messageID string) string {
	return account + "\x00" + strings.ToLower(bareMessageID(messageID))
}

func bareMessageID(id string) string {
	return strings.Trim(strings.TrimSpace(id), "<>")
}

type snoozeFile struct {
	Entries []SnoozeEntry `json:"entries"`
}

// SnoozePath is MAILDAY_SNOOZE_FILE, or snooze.json in the shared_dir.
func SnoozePath() string {
	if value := os.Getenv("MAILDAY_SNOOZE_FILE"); value != "" {
		return value
	}
	return config.SharedPath("snooze.json")
}

// SnoozeRecoveredNote is what a reader is told when the list was damaged and
// came back from its backup.
const SnoozeRecoveredNote = "snooze list was damaged; restored from the backup"

// ReadSnooze reads the list; a missing file is an empty list. Writes are
// renames, so a read never sees half a file and needs no lock.
//
// The file is shared by Syncthing, so two machines writing at once leave a
// "snooze.sync-conflict-…json" copy beside it: its entries are merged in
// (the later snooze of a message wins) so neither machine's snooze is lost.
// An empty file (a sync cut short) is read from the backup the last write
// left, rather than taken as "nothing snoozed". So is a file that does not
// parse, when the backup does: TakeSnoozeNote then has a note for the user.
func ReadSnooze(path string) ([]SnoozeEntry, error) {
	entries, _, err := loadSnooze(path)
	return entries, err
}

// loadSnooze is ReadSnooze; restored says the main file did not parse and
// the entries are the backup's.
func loadSnooze(path string) (entries []SnoozeEntry, restored bool, err error) {
	entries, empty, err := readSnoozeFile(path)
	switch {
	case err != nil:
		backup, ok := readSnoozeBackup(path + ".bak")
		if !ok {
			return nil, false, err
		}
		entries, restored = backup, true
		noteSnoozeRecovery(path)
	case empty:
		if backup, _, err := readSnoozeFile(path + ".bak"); err == nil {
			entries = backup
		}
	}
	for _, conflict := range snoozeConflicts(path) {
		extra, _, err := readSnoozeFile(conflict)
		if err != nil {
			continue
		}
		entries = mergeSnooze(entries, extra)
	}
	return entries, restored, nil
}

// readSnoozeBackup reads a backup that exists and parses; a missing or blank
// one is no way back.
func readSnoozeBackup(path string) ([]SnoozeEntry, bool) {
	if _, err := os.Stat(path); err != nil {
		return nil, false
	}
	entries, empty, err := readSnoozeFile(path)
	return entries, err == nil && !empty
}

// snoozeRecovery is the note waiting for a reader, and the damaged file it
// is about (path, size and time), so one damaged file is announced once
// however often it is read.
var snoozeRecovery struct {
	sync.Mutex
	pending string
	seen    string
}

func noteSnoozeRecovery(path string) {
	identity := path
	if info, err := os.Stat(path); err == nil {
		identity = fmt.Sprintf("%s|%d|%d", path, info.Size(), info.ModTime().UnixNano())
	}
	snoozeRecovery.Lock()
	defer snoozeRecovery.Unlock()
	if snoozeRecovery.seen != identity {
		snoozeRecovery.seen, snoozeRecovery.pending = identity, SnoozeRecoveredNote
	}
}

// TakeSnoozeNote returns the recovery note once; "" when there is none.
func TakeSnoozeNote() string {
	snoozeRecovery.Lock()
	defer snoozeRecovery.Unlock()
	note := snoozeRecovery.pending
	snoozeRecovery.pending = ""
	return note
}

func snoozeConflicts(path string) []string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	matches, _ := filepath.Glob(filepath.Join(filepath.Dir(path), base+".sync-conflict-*"+filepath.Ext(path)))
	return matches
}

// mergeSnooze adds extra to entries; for a message in both, the later
// snooze (by Added) wins.
func mergeSnooze(entries, extra []SnoozeEntry) []SnoozeEntry {
	at := map[string]int{}
	for index, entry := range entries {
		at[entry.Key()] = index
	}
	for _, entry := range extra {
		if index, ok := at[entry.Key()]; ok {
			if entry.Added.After(entries[index].Added) {
				entries[index] = entry
			}
			continue
		}
		at[entry.Key()] = len(entries)
		entries = append(entries, entry)
	}
	return entries
}

// readSnoozeFile reads one file; empty reports a missing or blank one.
func readSnoozeFile(path string) (entries []SnoozeEntry, empty bool, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil, true, nil
	}
	var file snoozeFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, false, fmt.Errorf("%s: %w", path, err)
	}
	// The last entry for a key wins: a message snoozed again moves its due time.
	seen := map[string]int{}
	for _, entry := range file.Entries {
		if at, ok := seen[entry.Key()]; ok {
			entries[at] = entry
			continue
		}
		seen[entry.Key()] = len(entries)
		entries = append(entries, entry)
	}
	return entries, false, nil
}

// snoozeMu keeps goroutines of one process apart, for systems without flock.
var snoozeMu sync.Mutex

// UpdateSnooze reads the list under a lock, lets change edit it and writes
// it back atomically, so two writers never lose each other's entries. A file
// that does not parse is overwritten only from a backup that does, after the
// damaged one is kept as snooze.json.corrupt-<unix>; with no such backup it is
// left alone.
func UpdateSnooze(path string, change func([]SnoozeEntry) []SnoozeEntry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	snoozeMu.Lock()
	defer snoozeMu.Unlock()
	unlock, err := lockSnooze(path + ".lock")
	if err != nil {
		return fmt.Errorf("lock %s: %w", path, err)
	}
	defer unlock()
	entries, restored, err := loadSnooze(path)
	if err != nil {
		return err
	}
	if restored {
		// The damaged file is about to be replaced by the backup's list
		// plus this change: keep it for anyone who wants to look.
		if err := keepDamagedSnooze(path); err != nil {
			return err
		}
	}
	entries = change(entries)
	slices.SortStableFunc(entries, func(a, b SnoozeEntry) int { return a.Due.Compare(b.Due) })
	data, err := json.MarshalIndent(snoozeFile{Entries: entries}, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".snooze-*.tmp")
	if err != nil {
		return err
	}
	_, writeErr := temp.Write(append(data, '\n'))
	if closeErr := temp.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		os.Remove(temp.Name())
		return writeErr
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		os.Remove(temp.Name())
		return err
	}
	// The conflict copies are merged into what was just written.
	for _, conflict := range snoozeConflicts(path) {
		os.Remove(conflict)
	}
	// A backup for an empty file (see ReadSnooze); best effort.
	if backup, err := os.CreateTemp(filepath.Dir(path), ".snooze-*.tmp"); err == nil {
		_, err := backup.Write(append(data, '\n'))
		backup.Close()
		if err != nil || os.Rename(backup.Name(), path+".bak") != nil {
			os.Remove(backup.Name())
		}
	}
	return nil
}

// keepDamagedSnooze copies an unparseable list to snooze.json.corrupt-<unix>
// before it is overwritten.
func keepDamagedSnooze(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	keep := fmt.Sprintf("%s.corrupt-%d", path, time.Now().Unix())
	if err := os.WriteFile(keep, data, 0o600); err != nil {
		return fmt.Errorf("keeping the damaged snooze list: %w", err)
	}
	return nil
}

// AddSnooze records entry, replacing an earlier one for the same message.
func AddSnooze(path string, entry SnoozeEntry) error {
	return UpdateSnooze(path, func(entries []SnoozeEntry) []SnoozeEntry {
		return append(slices.DeleteFunc(entries, func(e SnoozeEntry) bool { return e.Key() == entry.Key() }), entry)
	})
}

// RemoveSnooze drops the entry of a message, if there is one.
func RemoveSnooze(path, account, messageID string) error {
	key := SnoozeKey(account, messageID)
	return UpdateSnooze(path, func(entries []SnoozeEntry) []SnoozeEntry {
		return slices.DeleteFunc(entries, func(e SnoozeEntry) bool { return e.Key() == key })
	})
}

// snoozeLoop returns due messages to the inbox: at start, every minute and
// after a resume (the due time may have passed while asleep).
func (d *Daemon) snoozeLoop(ctx context.Context) {
	d.unsnoozeDue(ctx, time.Now())
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	resumed := d.resumed()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-resumed:
			resumed = d.resumed()
			select { // the network needs a moment
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
		d.unsnoozeDue(ctx, time.Now())
	}
}

// snoozeAccount finds the mbsync account behind a label in the list: its own
// name, or the label of the Maildir account folder its inbox lives in.
func (d *Daemon) snoozeAccount(label string) (string, bool) {
	names := d.config.AccountNames()
	if slices.Contains(names, label) {
		return label, true
	}
	for _, name := range names {
		if inbox := d.inboxDirs[name]; inbox != "" {
			folder := filepath.Base(filepath.Dir(inbox))
			if folder == label || maildir.AccountLabel(folder) == label {
				return name, true
			}
		}
	}
	return "", false
}

// snoozeNote logs a problem once per change: the loop runs every minute and
// an offline machine must not fill the journal. An empty text forgets the key.
func (d *Daemon) snoozeNote(key, text string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if text == "" {
		delete(d.snoozeLogged, key)
		return
	}
	if d.snoozeLogged[key] == text {
		return
	}
	if d.snoozeLogged == nil {
		d.snoozeLogged = map[string]string{}
	}
	d.snoozeLogged[key] = text
	log.Print(text)
}

// unsnoozeDue handles every entry due at now. One IMAP connection serves each
// account. Entries for accounts this machine does not sync stay for the
// machine that does.
func (d *Daemon) unsnoozeDue(ctx context.Context, now time.Time) {
	path := SnoozePath()
	entries, err := ReadSnooze(path)
	if err != nil {
		d.snoozeNote("file", "snooze: "+err.Error())
		return
	}
	d.snoozeNote("file", "")
	if note := TakeSnoozeNote(); note != "" {
		log.Print("snooze: " + note)
	}
	perAccount := map[string][]SnoozeEntry{}
	for _, entry := range entries {
		if entry.Due.After(now) {
			continue
		}
		account, ok := d.snoozeAccount(entry.Account)
		if !ok {
			continue
		}
		perAccount[account] = append(perAccount[account], entry)
	}
	for _, account := range slices.Sorted(maps.Keys(perAccount)) {
		if ctx.Err() != nil {
			return
		}
		d.unsnoozeAccount(ctx, account, perAccount[account], now, path)
	}
}

func (d *Daemon) unsnoozeAccount(ctx context.Context, account string, due []SnoozeEntry, now time.Time, path string) {
	snoozed, ok := d.config.RemoteTarget(account, SnoozedBox)
	if !ok {
		d.snoozeNote(account+"/target", fmt.Sprintf("%s: snooze: no mbsync channel syncs %s", account, SnoozedBox))
		return
	}
	d.snoozeNote(account+"/target", "")
	w := &watcher{daemon: d, account: d.config.Accounts[account]}
	client, err := w.dial(&imapclient.Options{Dialer: &net.Dialer{Timeout: 30 * time.Second}})
	if err != nil {
		d.snoozeNote(account+"/connect", fmt.Sprintf("%s: snooze: %v", account, err))
		return
	}
	defer client.Close()
	if err := withTimeout(ctx, client, 45*time.Second, func() error { return w.login(client) }); err != nil {
		d.snoozeNote(account+"/connect", fmt.Sprintf("%s: snooze: %v", account, err))
		return
	}
	// Only a real MOVE, as in move.go: the COPY and EXPUNGE fallback is pipelined.
	if !client.Caps().Has(imap.CapMove) {
		d.snoozeNote(account+"/connect", fmt.Sprintf("%s: snooze: server has no MOVE", account))
		return
	}
	d.snoozeNote(account+"/connect", "")

	box := &imapMailbox{client: client}
	var finished []SnoozeEntry
	for _, entry := range due {
		var result snoozeResult
		err := withTimeout(ctx, client, 60*time.Second, func() error {
			var err error
			result, err = unsnoozeOnServer(box, entry, now, func(entry SnoozeEntry) bool { return d.localSnoozed(account, entry) })
			return err
		})
		key := account + "/" + entry.Key()
		if err != nil {
			d.snoozeNote(key, fmt.Sprintf("%s: snooze: %q: %v", account, entry.Subject, err))
			continue
		}
		d.snoozeNote(key, "")
		switch result {
		case snoozeMoved:
			finished = append(finished, entry)
			log.Printf("%s: snooze: %q is back in the inbox", account, entry.Subject)
		case snoozeGone:
			finished = append(finished, entry)
			log.Printf("%s: snooze: %q is no longer in %s (moved elsewhere); dropping the entry", account, entry.Subject, SnoozedBox)
		}
	}
	_ = client.Logout().Wait()
	if len(finished) == 0 {
		return
	}
	// Only entries still as read: a message snoozed again meanwhile has a new due time.
	err = UpdateSnooze(path, func(entries []SnoozeEntry) []SnoozeEntry {
		return slices.DeleteFunc(entries, func(e SnoozeEntry) bool {
			return slices.ContainsFunc(finished, func(done SnoozeEntry) bool { return done.Key() == e.Key() && done.Due.Equal(e.Due) })
		})
	})
	if err != nil {
		d.snoozeNote("file", "snooze: dropping finished entries: "+err.Error())
	}
	if runner := d.runners[account]; runner != nil {
		specs := []string{snoozed.Spec}
		if inbox := d.inboxSpecs[account]; inbox != "" {
			specs = append(specs, inbox)
		}
		runner.request(specs, false, false, nil)
	}
}

type snoozeResult uint8

const (
	snoozePending snoozeResult = iota // not in @Snoozed yet; try again
	snoozeMoved                       // moved to INBOX by this call
	snoozeGone                        // not in @Snoozed any more: someone else moved it
)

// snoozeMailbox is serverMailbox plus clearing flags, so unsnoozeOnServer
// can be tested against a fake as well as the in-memory server.
type snoozeMailbox interface {
	serverMailbox
	removeFlags(uid imap.UID, flags []imap.Flag) error
}

func (m *imapMailbox) removeFlags(uid imap.UID, flags []imap.Flag) error {
	return m.client.Store(imap.UIDSetNum(uid), &imap.StoreFlags{Op: imap.StoreFlagsDel, Silent: true, Flags: flags}, nil).Close()
}

// unsnoozeOnServer marks the message of entry unread and moves it from
// @Snoozed to INBOX. An error means nothing is known to have happened. The
// other machine may do the same at the same moment: whoever loses the race
// finds the message gone from @Snoozed (and in INBOX) and reports snoozeGone,
// never a duplicate, because the move is by UID.
func unsnoozeOnServer(box snoozeMailbox, entry SnoozeEntry, now time.Time, stillLocal func(SnoozeEntry) bool) (snoozeResult, error) {
	wanted := "<" + bareMessageID(entry.MessageID) + ">"
	uids, err := box.search(SnoozedBox, wanted)
	if err != nil {
		return snoozePending, err
	}
	switch {
	case len(uids) == 0:
		// Mailday filed it a moment ago and mbsync may not have uploaded it.
		if !entry.Added.IsZero() && now.Sub(entry.Added) < snoozeUploadGrace {
			return snoozePending, nil
		}
		// Not uploaded yet (snoozed offline, lid closed) looks the same from
		// the server as "already brought back". The machine that snoozed it
		// knows (stillLocal); another machine leaves the entry to it, for a
		// week at most.
		if now.Sub(entry.Added) < snoozeStrandLimit {
			if entry.Host != "" && entry.Host != SnoozeHost() {
				return snoozePending, nil
			}
			if stillLocal != nil && stillLocal(entry) {
				return snoozePending, nil
			}
		}
		return snoozeGone, nil
	case len(uids) > 1:
		return snoozePending, fmt.Errorf("%d messages with that Message-ID", len(uids))
	}
	// Any failure from here may be the other machine having moved it first.
	vanished := func() bool {
		remaining, err := box.search(SnoozedBox, wanted)
		return err == nil && len(remaining) == 0
	}
	found, err := box.messageID(uids[0])
	if err != nil || bareMessageID(found) == "" {
		if vanished() {
			return snoozeGone, nil
		}
		return snoozePending, fmt.Errorf("reading the Message-ID of the match: %v", err)
	}
	if !strings.EqualFold(bareMessageID(found), bareMessageID(entry.MessageID)) {
		return snoozePending, fmt.Errorf("the match has Message-ID %q, not %q", found, entry.MessageID)
	}
	if err := box.removeFlags(uids[0], []imap.Flag{imap.FlagSeen}); err != nil {
		if vanished() {
			return snoozeGone, nil
		}
		return snoozePending, fmt.Errorf("clearing \\Seen: %w", err)
	}
	if err := box.move(uids[0], "INBOX"); err != nil {
		// The reply may have been lost after the server acted.
		if vanished() {
			if arrived, err := box.search("INBOX", wanted); err == nil && len(arrived) > 0 {
				return snoozeGone, nil
			}
		}
		return snoozePending, err
	}
	return snoozeMoved, nil
}

// localSnoozed reports whether this machine's own @Snoozed Maildir still
// holds the message: Mailday filed it there and mbsync has not uploaded it.
func (d *Daemon) localSnoozed(account string, entry SnoozeEntry) bool {
	dir, ok := d.config.LocalDir(account, SnoozedBox)
	if !ok {
		return false
	}
	wanted := bareMessageID(entry.MessageID)
	for _, leaf := range []string{"cur", "new"} {
		files, _ := os.ReadDir(filepath.Join(dir, leaf))
		for _, file := range files {
			if found, err := readMessageID(filepath.Join(dir, leaf, file.Name())); err == nil && strings.EqualFold(bareMessageID(found), wanted) {
				return true
			}
		}
	}
	return false
}
