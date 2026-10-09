package syncd

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/mail"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// A moveJob files a message on the server with IMAP MOVE instead of letting
// mbsync upload Mailday's local copy to the new folder and expunge the old
// one: nothing is uploaded, and Gmail keeps one message with its labels
// changed rather than a fresh copy.
type moveJob struct {
	messageID        string
	fromBox, toBox   string // server mailboxes
	fromSpec, toSpec string // mbsync targets
	localCopy        string // Mailday's moved file, replaced by the download
	// queued is when Mailday moved the file (a little before the request).
	// If any mbsync of the account finished after it, mbsync may have seen
	// the file and paired it with a server message; deleting it then reads
	// to mbsync as "deleted here" and is passed on to the server. So the
	// copy is kept, at worst a duplicate, never a loss.
	queued time.Time
}

// requestMove queues a move made in Mailday: oldPath is where the message
// was, newPath where Mailday put it. It refuses (and the caller pushes the
// old way) when the folders do not map to one account's mailboxes or the
// message has no Message-ID.
func (d *Daemon) requestMove(oldPath, newPath string) error {
	fromDir, toDir := maildirFolder(oldPath), maildirFolder(newPath)
	fromTarget, ok1 := d.config.LocalTarget(fromDir)
	toTarget, ok2 := d.config.LocalTarget(toDir)
	fromAccount, fromBox, ok3 := d.config.RemoteMailbox(fromDir)
	toAccount, toBox, ok4 := d.config.RemoteMailbox(toDir)
	if !ok1 || !ok2 || !ok3 || !ok4 || fromAccount != toAccount || fromTarget.Account != fromAccount {
		return errors.New("folders are not one account's mailboxes")
	}
	messageID, err := readMessageID(newPath)
	if err != nil {
		return err
	}
	runner := d.runners[fromAccount]
	if runner == nil {
		return errors.New("unknown account")
	}
	runner.mu.Lock()
	// Mailday sends the request right after moving the file; a rename keeps
	// the file's mtime (often days old), so that says nothing about when.
	queued := time.Now().Add(-d.tune.moveMargin)
	runner.moves = append(runner.moves, moveJob{messageID, fromBox, toBox, fromTarget.Spec, toTarget.Spec, newPath, queued})
	runner.mu.Unlock()
	select {
	case runner.wake <- struct{}{}:
	default:
	}
	return nil
}

func readMessageID(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	message, err := mail.ReadMessage(file)
	if err != nil {
		return "", err
	}
	id := message.Header.Get("Message-Id")
	if id == "" {
		return "", errors.New("no Message-ID")
	}
	return id, nil
}

// serverMoves runs queued moves on one connection. Moves that worked need a
// plain sync of both mailboxes; those that failed fall back to a push.
func (r *runner) serverMoves(ctx context.Context, jobs []moveJob) (moved, failed []moveJob) {
	account := r.daemon.config.Accounts[r.account]
	w := &watcher{daemon: r.daemon, account: account}
	client, err := w.dial(&imapclient.Options{Dialer: &net.Dialer{Timeout: 30 * time.Second}})
	if err != nil {
		log.Printf("%s: server move: %v; uploading instead", r.account, err)
		return nil, jobs
	}
	// Closed on every way out, a failed login included.
	defer client.Close()
	if err := withTimeout(ctx, client, 45*time.Second, func() error { return w.login(client) }); err != nil {
		log.Printf("%s: server move: %v; uploading instead", r.account, err)
		return nil, jobs
	}
	// Only a real MOVE: go-imap's COPY, STORE and EXPUNGE fallback is
	// pipelined, so a failed COPY would still expunge the original.
	if !client.Caps().Has(imap.CapMove) {
		log.Printf("%s: server has no MOVE; uploading instead", r.account)
		return nil, jobs
	}
	box := &imapMailbox{client: client}
	for _, job := range jobs {
		err := withTimeout(ctx, client, 60*time.Second, func() error { return moveOnServer(box, job) })
		if err != nil {
			log.Printf("%s: server move %s → %s: %v; uploading instead", r.account, job.fromBox, job.toBox, err)
			failed = append(failed, job)
			continue
		}
		// The server has it in the new mailbox; the sync downloads it there,
		// unless an mbsync run since the move may have paired our copy.
		if seen := r.lastSynced(job.toSpec); seen.After(job.queued) {
			log.Printf("%s: kept %s: %s was synced after the move and may have taken it", r.account, filepath.Base(job.localCopy), job.toSpec)
		} else if err := removeLocalCopy(job.localCopy); err != nil {
			log.Printf("%s: removing %s after the server move: %v", r.account, job.localCopy, err)
		}
		moved = append(moved, job)
		if r.daemon.verbose {
			log.Printf("%s: moved %s → %s on the server", r.account, job.fromBox, job.toBox)
		}
	}
	_ = client.Logout().Wait()
	return moved, failed
}

// serverMailbox is the part of an IMAP connection a move needs, so the
// decisions in moveOnServer can be tested without a server.
type serverMailbox interface {
	// search selects mailbox and lists the UIDs whose Message-ID contains id.
	search(mailbox, id string) ([]imap.UID, error)
	// messageID reads the Message-ID header of a message in the selected mailbox.
	messageID(uid imap.UID) (string, error)
	addFlags(uid imap.UID, flags []imap.Flag) error
	move(uid imap.UID, mailbox string) error
}

// moveOnServer moves the message of job on the server, after checking that
// the one search hit really is that message and carrying over the flags the
// local copy has. An error means the caller should upload instead.
func moveOnServer(box serverMailbox, job moveJob) error {
	uids, err := box.search(job.fromBox, job.messageID)
	if err != nil {
		return err
	}
	if len(uids) != 1 {
		return fmt.Errorf("%d messages with that Message-ID", len(uids))
	}
	// SEARCH HEADER matches substrings (and some servers normalise IDs), so
	// make sure the hit is the message and not one that merely contains it.
	found, err := box.messageID(uids[0])
	if err != nil {
		return fmt.Errorf("reading the Message-ID of the match: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(found), strings.TrimSpace(job.messageID)) {
		return fmt.Errorf("the match has Message-ID %q, not %q", found, job.messageID)
	}
	// Flags set in Mailday may not have reached the server yet, and a move
	// would carry the old ones. Only ever add: never undo a change made
	// elsewhere.
	if flags := imapFlagsFromMaildir(filepath.Base(job.localCopy)); len(flags) > 0 {
		if err := box.addFlags(uids[0], flags); err != nil {
			return fmt.Errorf("storing flags: %w", err)
		}
	}
	if err := box.move(uids[0], job.toBox); err != nil {
		// The reply may have been lost after the server acted: the move
		// happened only if the message left the source AND is in the target.
		remaining, searchErr := box.search(job.fromBox, job.messageID)
		if searchErr == nil && len(remaining) == 0 {
			if arrived, err := box.search(job.toBox, job.messageID); err == nil && len(arrived) > 0 {
				return nil
			}
		}
		return err
	}
	return nil
}

// imapMailbox implements serverMailbox on a logged-in client.
type imapMailbox struct {
	client   *imapclient.Client
	selected string
}

func (m *imapMailbox) search(mailbox, id string) ([]imap.UID, error) {
	// Select again even when it is the same mailbox: a failed MOVE may have
	// left the selection stale, and SELECT refreshes the view of the box.
	if _, err := m.client.Select(mailbox, nil).Wait(); err != nil {
		m.selected = ""
		return nil, err
	}
	m.selected = mailbox
	found, err := m.client.UIDSearch(&imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: "Message-ID", Value: id}}}, nil).Wait()
	if err != nil {
		return nil, err
	}
	return found.AllUIDs(), nil
}

func (m *imapMailbox) messageID(uid imap.UID) (string, error) {
	section := &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader, HeaderFields: []string{"Message-ID"}, Peek: true}
	messages, err := m.client.Fetch(imap.UIDSetNum(uid), &imap.FetchOptions{BodySection: []*imap.FetchItemBodySection{section}}).Collect()
	if err != nil {
		return "", err
	}
	if len(messages) != 1 {
		return "", fmt.Errorf("%d messages fetched", len(messages))
	}
	return headerMessageID(messages[0].FindBodySection(section)), nil
}

func (m *imapMailbox) addFlags(uid imap.UID, flags []imap.Flag) error {
	return m.client.Store(imap.UIDSetNum(uid), &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: flags}, nil).Close()
}

func (m *imapMailbox) move(uid imap.UID, mailbox string) error {
	_, err := m.client.Move(imap.UIDSetNum(uid), mailbox).Wait()
	return err
}

// headerMessageID reads Message-ID out of the header block a FETCH of
// HEADER.FIELDS returns.
func headerMessageID(data []byte) string {
	reader := textproto.NewReader(bufio.NewReader(bytes.NewReader(append(bytes.Clone(data), "\r\n\r\n"...))))
	header, _ := reader.ReadMIMEHeader()
	return header.Get("Message-Id")
}

// imapFlagsFromMaildir translates the flags after ":2," in a Maildir file
// name. Trashed (T) has no IMAP counterpart worth setting: the move itself is
// the filing.
func imapFlagsFromMaildir(name string) []imap.Flag {
	index := strings.LastIndex(name, ":2,")
	if index < 0 {
		return nil
	}
	var flags []imap.Flag
	for _, letter := range name[index+3:] {
		switch letter {
		case 'S':
			flags = append(flags, imap.FlagSeen)
		case 'R':
			flags = append(flags, imap.FlagAnswered)
		case 'F':
			flags = append(flags, imap.FlagFlagged)
		case 'D':
			flags = append(flags, imap.FlagDraft)
		}
	}
	return flags
}

// maildirKey is the part of a Maildir file name that identifies the message:
// without the ",U=n" mbsync adds once it has a UID, and without the ":2,"
// flags that change whenever the message is marked.
func maildirKey(name string) string {
	if index := strings.LastIndex(name, ":2,"); index >= 0 {
		name = name[:index]
	}
	key, _, _ := strings.Cut(name, ",U=")
	return key
}

// removeLocalCopy deletes Mailday's moved file. If the name has changed since
// it was recorded (flags toggled, or mbsync assigned a UID), the same message
// is looked for by its key in cur and new, so no stale copy is left to be
// uploaded again as a duplicate.
func removeLocalCopy(path string) error {
	err := os.Remove(path)
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	folder := maildirFolder(path)
	key := maildirKey(filepath.Base(path))
	var firstErr error
	for _, leaf := range []string{"cur", "new"} {
		entries, _ := os.ReadDir(filepath.Join(folder, leaf))
		for _, entry := range entries {
			if entry.IsDir() || maildirKey(entry.Name()) != key {
				continue
			}
			if err := os.Remove(filepath.Join(folder, leaf, entry.Name())); err != nil && !errors.Is(err, fs.ErrNotExist) && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}
