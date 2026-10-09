package maildir

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"mime"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aronvandepol/mailday/internal/terminal"
)

const defaultLimit = 500

// InboxBox names the inbox in Message.Box. Tag folders keep their own names,
// such as "@Reply", because the mail tagger files read mail into them.
const InboxBox = "Inbox"

// ArchiveBox shows recently archived mail, so mail the tagger archives stays
// findable. Only the newest archiveLimit messages per account are read, and
// nothing moves out of it: Linux maps Gmail's Archive to All Mail, where
// moving a message away would trash it on Gmail's side.
const ArchiveBox = "Archive"

const archiveLimit = 300

// SentBox shows the newest sent mail, read-only like Archive. Servers and
// sync tools name the folder differently, so every spelling is tried and the
// one holding the newest message wins (Exchange carries an empty "Sent" beside
// the real "Sent Items"; mutt-wizard's Gmail has both Sent and [Gmail]/Sent Mail).
const SentBox = "Sent"

var sentNames = []string{"Sent", "Sent Items", "Sent Mail", filepath.Join("[Gmail]", "Sent Mail")}

func isSentName(name string) bool {
	return slices.Contains(sentNames, name)
}

// newestFile is the modification time of the newest message in a Maildir.
func newestFile(folder string) time.Time {
	var newest time.Time
	for _, leaf := range []string{"cur", "new"} {
		entries, err := os.ReadDir(filepath.Join(folder, leaf))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if info, err := entry.Info(); err == nil && info.ModTime().After(newest) {
				newest = info.ModTime()
			}
		}
	}
	return newest
}

func findSent(accountDir string) string {
	best, bestTime := "", time.Time{}
	for _, name := range sentNames {
		path := filepath.Join(accountDir, name)
		if !hasEntry(accountDir, name) || !isMaildir(path) {
			continue
		}
		if newest := newestFile(path); best == "" || newest.After(bestTime) {
			best, bestTime = path, newest
		}
	}
	return best
}

var uidField = regexp.MustCompile(`,U=[0-9]+`)

// Key is the part of a Maildir file name that survives every rename: flag
// changes, new/ to cur/, a move to another box, and mbsync adding ",U=".
func Key(path string) string {
	name := filepath.Base(path)
	if index := strings.Index(name, ":2,"); index >= 0 {
		name = name[:index]
	}
	return uidField.ReplaceAllString(name, "")
}

type Message struct {
	Path      string
	Account   string
	Box       string
	From      string
	FromAddr  string
	To        string
	Subject   string
	MessageID string
	// InReplyTo and References are the raw threading headers, which the
	// conversation view groups by.
	InReplyTo  string
	References string
	Snippet    string
	Date       time.Time
	Unread     bool
	Size       int64
	// Receipt is set on a read receipt (receipts.go), which is not mail to
	// read: the list hides it and the Sent box marks the message it answers.
	Receipt *Receipt
}

type Attachment struct {
	Name string
	Type string
	Size int64
}

// Address is one parsed recipient.
type Address struct {
	Name string
	Addr string
}

type Content struct {
	MessageID   string
	References  string
	ReplyTo     []Address
	FromName    string
	FromAddr    string
	ToList      []Address
	CCList      []Address
	HTML        string // raw HTML alternative, empty for plain-text mail
	From        string
	To          string
	CC          string
	Subject     string
	Date        time.Time
	Body        string
	Attachments []Attachment
	// Invitation is the meeting request, cancellation or reply the message
	// carries, if any; its .ics part is then not in Attachments.
	Invitation *Invitation
	// ReceiptTo is who asked for a read receipt (Disposition-Notification-To).
	ReceiptTo []Address
}

type ListResult struct {
	Messages []Message
	Accounts []string
	// Boxes lists every box found in any account: InboxBox, then tag folders.
	Boxes    []string
	Skipped  int
	Warnings []string
}

// Store reads account directories under one or more roots. Linux mbsync keeps
// ~/Mail/<account>/Inbox; mutt-wizard on macOS keeps
// ~/.local/share/mail/<address>/INBOX, with other accounts under ~/Mail.
type Store struct {
	Roots []string
	Limit int // per box, so a full Inbox cannot crowd out a tag folder

	cache *headerCache
}

type folderRef struct {
	path    string
	account string
	box     string
}

func New(roots []string, limit int) *Store {
	if limit <= 0 {
		limit = defaultLimit
	}
	cleaned := make([]string, 0, len(roots))
	for _, root := range roots {
		if root == "" {
			continue
		}
		if absolute, err := filepath.Abs(root); err == nil {
			root = absolute
		}
		cleaned = append(cleaned, root)
	}
	return &Store{Roots: cleaned, Limit: limit, cache: newHeaderCache()}
}

// hasEntry reports whether dir holds an entry spelled exactly name. On
// macOS the file system ignores case, so a stat of "Inbox" finds "INBOX";
// mbsync and the IMAP server do not, and must get the real spelling.
func hasEntry(dir, name string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	first := strings.SplitN(name, string(filepath.Separator), 2)
	for _, entry := range entries {
		if entry.Name() == first[0] {
			if len(first) == 1 {
				return true
			}
			return hasEntry(filepath.Join(dir, first[0]), first[1])
		}
	}
	return false
}

func isMaildir(path string) bool {
	for _, leaf := range []string{"cur", "new"} {
		if info, err := os.Stat(filepath.Join(path, leaf)); err == nil && info.IsDir() {
			return true
		}
	}
	return false
}

// findInbox returns the account's inbox Maildir. mbsync names it after the
// MaildirStore Inbox setting, so both spellings occur.
func findInbox(accountDir string) string {
	for _, name := range []string{"Inbox", "INBOX"} {
		if path := filepath.Join(accountDir, name); hasEntry(accountDir, name) && isMaildir(path) {
			return path
		}
	}
	return ""
}

// AccountLabel is the label a Maildir account folder shows under.
func AccountLabel(dir string) string { return accountLabel(dir) }

// accountLabel shortens mutt-wizard's address-named account folders, so
// "you@gmail.com" shows as "gmail" like the Linux "gmail" folder does.
func accountLabel(dir string) string {
	if at := strings.LastIndex(dir, "@"); at > 0 && at < len(dir)-1 {
		domain := dir[at+1:]
		if dot := strings.Index(domain, "."); dot > 0 {
			domain = domain[:dot]
		}
		return terminal.SanitizeLine(domain)
	}
	return terminal.SanitizeLine(dir)
}

// findTrash returns the account's deleted-mail Maildir. Exchange calls it
// "Deleted Items" (mutt-wizard keeps that name), Linux mbsync maps it to Trash.
func findTrash(accountDir string) string {
	for _, name := range []string{"Deleted Items", "Trash"} {
		if path := filepath.Join(accountDir, name); hasEntry(accountDir, name) && isMaildir(path) {
			return path
		}
	}
	return ""
}

func isGmailAccount(accountDir string) bool {
	return strings.Contains(strings.ToLower(filepath.Base(accountDir)), "gmail")
}

func isTrashName(name string) bool {
	return name == "Trash" || name == "Deleted Items"
}

func isInboxName(name string) bool {
	return name == "Inbox" || name == "INBOX"
}

func (s *Store) folders(result *ListResult) ([]folderRef, error) {
	var folders []folderRef
	readable := 0
	accounts := make(map[string]bool)
	boxes := map[string]bool{InboxBox: true}
	for _, root := range s.Roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				result.Warnings = append(result.Warnings, fmt.Sprintf("%s: %v", terminal.SanitizeLine(root), err))
			}
			continue
		}
		readable++
		for _, accountEntry := range entries {
			if !accountEntry.IsDir() || strings.HasPrefix(accountEntry.Name(), ".") {
				continue
			}
			accountDir := filepath.Join(root, accountEntry.Name())
			inbox := findInbox(accountDir)
			if inbox == "" {
				continue
			}
			account := accountLabel(accountEntry.Name())
			if !accounts[account] {
				accounts[account] = true
				result.Accounts = append(result.Accounts, account)
			}
			folders = append(folders, folderRef{path: inbox, account: account, box: InboxBox})
			boxEntries, err := os.ReadDir(accountDir)
			if err != nil {
				continue
			}
			for _, boxEntry := range boxEntries {
				name := boxEntry.Name()
				if !boxEntry.IsDir() || !strings.HasPrefix(name, "@") || !isMaildir(filepath.Join(accountDir, name)) {
					continue
				}
				box := terminal.SanitizeLine(name)
				boxes[box] = true
				folders = append(folders, folderRef{path: filepath.Join(accountDir, name), account: account, box: box})
			}
			if sent := findSent(accountDir); sent != "" {
				folders = append(folders, folderRef{path: sent, account: account, box: SentBox})
				boxes[SentBox] = true
			}
			if archive := filepath.Join(accountDir, "Archive"); isMaildir(archive) {
				folders = append(folders, folderRef{path: archive, account: account, box: ArchiveBox})
				boxes[ArchiveBox] = true
			}
		}
	}
	if readable == 0 {
		return nil, fmt.Errorf("read mail roots: none of %s exists", strings.Join(s.Roots, ", "))
	}
	sort.Strings(result.Accounts)
	result.Boxes = OrderBoxes(boxes)
	return folders, nil
}

// OrderBoxes puts the inbox first, then the boxes that need you, then the
// project and admin tags alphabetically.
func OrderBoxes(set map[string]bool) []string {
	front := []string{InboxBox, "@Reply", "@Waiting"}
	ordered := make([]string, 0, len(set))
	for _, box := range front {
		if set[box] {
			ordered = append(ordered, box)
		}
	}
	var rest []string
	for box := range set {
		if !slices.Contains(front, box) && box != ArchiveBox && box != SentBox {
			rest = append(rest, box)
		}
	}
	sort.Strings(rest)
	ordered = append(ordered, rest...)
	if set[SentBox] {
		ordered = append(ordered, SentBox)
	}
	if set[ArchiveBox] {
		ordered = append(ordered, ArchiveBox)
	}
	return ordered
}

// folderFile is one message file found in a Maildir leaf.
type folderFile struct {
	path  string
	name  string
	leaf  string
	entry fs.DirEntry
}

// nameStamp is the delivery time at the front of a Maildir file name
// ("1700000000.M1P2.host:2,S"), in seconds. Files named differently fall back
// to their modification time.
func nameStamp(file folderFile) int64 {
	digits := file.name
	if dot := strings.IndexByte(digits, '.'); dot > 0 {
		digits = digits[:dot]
	}
	if stamp, err := strconv.ParseInt(digits, 10, 64); err == nil && stamp > 0 {
		return stamp
	}
	if info, err := file.entry.Info(); err == nil {
		return info.ModTime().Unix()
	}
	return 0
}

// newestFiles keeps the count files with the latest delivery time, in their
// original order. Reading a box only for its newest messages must not parse
// every file in it: Gmail's All Mail holds tens of thousands.
func newestFiles(files []folderFile, count int) []folderFile {
	if len(files) <= count {
		return files
	}
	stamps := make([]int64, len(files))
	order := make([]int, len(files))
	for index, file := range files {
		stamps[index] = nameStamp(file)
		order[index] = index
	}
	sort.SliceStable(order, func(i, j int) bool { return stamps[order[i]] > stamps[order[j]] })
	order = order[:count]
	sort.Ints(order)
	kept := make([]folderFile, 0, count)
	for _, index := range order {
		kept = append(kept, files[index])
	}
	return kept
}

// readFolder adds the folder's newest messages to result. Every path it saw
// goes into live, so the cache can forget files that are gone.
func (s *Store) readFolder(ctx context.Context, folder folderRef, result *ListResult, live map[string]bool) error {
	limited := folder.box == ArchiveBox || folder.box == SentBox
	limit := s.Limit
	if limited {
		limit = min(limit, archiveLimit)
	}
	var files []folderFile
	for _, leaf := range []string{"new", "cur"} {
		dir := filepath.Join(folder.path, leaf)
		entries, err := os.ReadDir(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("%s/%s/%s: %v", folder.account, folder.box, leaf, err))
			continue
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			live[path] = true
			files = append(files, folderFile{path: path, name: entry.Name(), leaf: leaf, entry: entry})
		}
	}
	if limited {
		// A message's Date header can disagree with its delivery time, so
		// parse twice the limit before sorting by Date.
		files = newestFiles(files, limit*2)
	}
	var messages []Message
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := file.entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			result.Skipped++
			continue
		}
		message, err := s.cache.header(file.path, folder.account, file.leaf == "new" || !seenFlag(file.name), info)
		if err != nil {
			result.Skipped++
			continue
		}
		message.Box = folder.box
		messages = append(messages, message)
	}
	sort.SliceStable(messages, func(i, j int) bool { return messages[i].Date.After(messages[j].Date) })
	if len(messages) > limit {
		messages = messages[:limit]
	}
	result.Messages = append(result.Messages, messages...)
	return nil
}

func (s *Store) List(ctx context.Context) (ListResult, error) {
	result := ListResult{}
	folders, err := s.folders(&result)
	if err != nil {
		return ListResult{}, err
	}
	live := make(map[string]bool)
	for _, folder := range folders {
		if err := s.readFolder(ctx, folder, &result, live); err != nil {
			return ListResult{}, err
		}
	}
	// Gmail's All Mail also holds everything in the inbox and the tag
	// folders. Keep those messages in their own box only.
	elsewhere := make(map[string]bool)
	for _, message := range result.Messages {
		if message.Box != ArchiveBox && message.MessageID != "" {
			elsewhere[message.Account+"\x00"+message.MessageID] = true
		}
	}
	kept := result.Messages[:0]
	shown := make(map[string]bool)
	for _, message := range result.Messages {
		if message.Box == ArchiveBox && elsewhere[message.Account+"\x00"+message.MessageID] {
			continue
		}
		// A box can hold two copies of one message, such as Exchange's Sent
		// Items copy beside a local copy saved by another mail tool.
		if message.MessageID != "" {
			key := message.Account + "\x00" + message.Box + "\x00" + message.MessageID
			if shown[key] {
				continue
			}
			shown[key] = true
		}
		kept = append(kept, message)
	}
	result.Messages = kept
	sort.SliceStable(result.Messages, func(i, j int) bool {
		return result.Messages[i].Date.After(result.Messages[j].Date)
	})
	// Previews parse each body, which dominates load time once the tag boxes
	// hold a thousand messages, so read them in parallel.
	jobs := make(chan int)
	var wg sync.WaitGroup
	for range min(runtime.NumCPU(), 8) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				result.Messages[index].Snippet = s.cache.snippet(result.Messages[index])
			}
		}()
	}
	for index := range result.Messages {
		if ctx.Err() != nil {
			break
		}
		jobs <- index
	}
	close(jobs)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return ListResult{}, err
	}
	s.cache.prune(live)
	return result, nil
}

func readHeader(path, account string, unread bool, info fs.FileInfo) (Message, error) {
	f, err := os.Open(path)
	if err != nil {
		return Message{}, err
	}
	defer f.Close()

	parsed, err := mail.ReadMessage(f)
	if err != nil {
		return Message{}, err
	}
	date, err := parsed.Header.Date()
	if err != nil {
		date = info.ModTime()
	}
	from, fromAddr := addressLabel(parsed.Header.Get("From"))
	subject := decodeHeader(parsed.Header.Get("Subject"))
	if strings.TrimSpace(subject) == "" {
		subject = "(no subject)"
	}
	var receipt *Receipt
	if isReceiptReport(parsed.Header) {
		receipt = parseReceipt(parsed.Header, parsed.Body, date)
	}
	return Message{
		Path:       path,
		Account:    terminal.SanitizeLine(account),
		From:       terminal.SanitizeLine(from),
		FromAddr:   terminal.SanitizeLine(fromAddr),
		To:         terminal.SanitizeLine(decodeAddressList(parsed.Header.Get("To"))),
		Subject:    terminal.SanitizeLine(subject),
		MessageID:  terminal.SanitizeLine(parsed.Header.Get("Message-ID")),
		InReplyTo:  terminal.SanitizeLine(parsed.Header.Get("In-Reply-To")),
		References: terminal.SanitizeLine(parsed.Header.Get("References")),
		Date:       date,
		Unread:     unread,
		Size:       info.Size(),
		Receipt:    receipt,
	}, nil
}

func addressLabel(raw string) (string, string) {
	raw = decodeHeader(raw)
	address, err := mail.ParseAddress(raw)
	if err != nil {
		return raw, ""
	}
	name := strings.TrimSpace(address.Name)
	if name == "" {
		name = address.Address
	}
	return name, address.Address
}

func decodeAddressList(raw string) string {
	raw = decodeHeader(raw)
	addresses, err := mail.ParseAddressList(raw)
	if err != nil {
		return raw
	}
	parts := make([]string, 0, len(addresses))
	for _, address := range addresses {
		if strings.TrimSpace(address.Name) != "" {
			parts = append(parts, fmt.Sprintf("%s <%s>", address.Name, address.Address))
		} else {
			parts = append(parts, address.Address)
		}
	}
	return strings.Join(parts, ", ")
}

func decodeHeader(value string) string {
	decoded, err := new(mime.WordDecoder).DecodeHeader(value)
	if err != nil {
		return value
	}
	return decoded
}

func seenFlag(name string) bool {
	_, flags := splitFlags(name)
	return strings.Contains(flags, "S")
}

func splitFlags(name string) (string, string) {
	idx := strings.LastIndex(name, ":2,")
	if idx < 0 {
		return name, ""
	}
	return name[:idx], name[idx+3:]
}

func withSeenFlag(name string, seen bool) string {
	base, flags := splitFlags(name)
	set := make(map[rune]bool, len(flags)+1)
	for _, flag := range flags {
		set[flag] = true
	}
	if seen {
		set['S'] = true
	} else {
		delete(set, 'S')
	}
	ordered := make([]rune, 0, len(set))
	for flag := range set {
		ordered = append(ordered, flag)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	return base + ":2," + string(ordered)
}

// withResolved runs attempt on path. If the file is gone, because mbsync or
// another client renamed it after the caller captured the path, it finds the
// message again by Key and tries once more. It returns the path that was used.
func (s *Store) withResolved(path string, attempt func(path string) error) (string, error) {
	err := attempt(path)
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		return path, err
	}
	resolved := s.Resolve(path)
	if resolved == "" || resolved == path {
		return path, err
	}
	return resolved, attempt(resolved)
}

func (s *Store) SetUnread(message Message, unread bool) (Message, error) {
	var destination string
	_, err := s.withResolved(message.Path, func(path string) error {
		folder, leaf, err := s.locate(path)
		if err != nil {
			return err
		}
		name := filepath.Base(path)
		destinationLeaf := leaf
		if leaf == "new" && !unread {
			destinationLeaf = "cur"
		}
		newName := withSeenFlag(name, !unread)
		if destinationLeaf == "new" {
			newName = name
		}
		destination = filepath.Join(folder, destinationLeaf, newName)
		if destination != path {
			if err := moveWithoutReplace(path, destination); err != nil {
				return fmt.Errorf("update unread state: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return message, err
	}
	message.Path = destination
	message.Unread = unread
	return message, nil
}

func (s *Store) Archive(message Message) error {
	_, err := s.MoveTo(message, "Archive")
	return err
}

// MoveTo files a message into another box of the same account: InboxBox, an
// "@" tag folder, or "Archive". It returns the new path. The file keeps its
// flags but loses any ",U=" field, because mbsync reads that field as the UID
// in the destination folder (isync RECOMMENDATIONS). The next mbsync run
// carries the move to the server.
func (s *Store) MoveTo(message Message, box string) (string, error) {
	var destination string
	_, err := s.withResolved(message.Path, func(path string) error {
		var err error
		destination, err = s.moveTo(path, box)
		return err
	})
	if err != nil {
		return "", err
	}
	return destination, nil
}

func (s *Store) moveTo(path, box string) (string, error) {
	folder, leaf, err := s.locate(path)
	if err != nil {
		return "", err
	}
	// Gmail's Archive is a copy of All Mail, which holds every message: a
	// message taken out of it locally reads to mbsync as deleted from All
	// Mail. Elsewhere Archive is a folder like any other.
	if filepath.Base(folder) == ArchiveBox && isGmailAccount(filepath.Dir(folder)) {
		return "", fmt.Errorf("Gmail's Archive is All Mail: move it back in Gmail itself")
	}
	if box, _ := s.boxOf(folder); isSentName(box) {
		return "", fmt.Errorf("sent mail stays in Sent")
	}
	accountDir := filepath.Dir(folder)
	account := terminal.SanitizeLine(filepath.Base(accountDir))
	var destinationFolder string
	switch {
	case box == InboxBox:
		destinationFolder = findInbox(accountDir)
	case box == "Trash":
		destinationFolder = findTrash(accountDir)
	case box == "Archive" || (strings.HasPrefix(box, "@") && !strings.ContainsAny(box, `/\`)):
		destinationFolder = filepath.Join(accountDir, box)
	default:
		return "", fmt.Errorf("cannot file into %q", terminal.SanitizeLine(box))
	}
	if destinationFolder == "" || !isMaildir(destinationFolder) {
		return "", fmt.Errorf("%s has no %s mailbox", account, terminal.SanitizeLine(box))
	}
	if same, _ := sameDir(destinationFolder, folder); same {
		return "", fmt.Errorf("message is already in %s", terminal.SanitizeLine(box))
	}
	destinationDir := filepath.Join(destinationFolder, leaf)
	if err := os.MkdirAll(destinationDir, 0o700); err != nil {
		return "", err
	}
	destination := filepath.Join(destinationDir, uidField.ReplaceAllString(filepath.Base(path), ""))
	if err := moveWithoutReplace(path, destination); err != nil {
		return "", fmt.Errorf("move message: %w", err)
	}
	return destination, nil
}

// MarkAnswered adds the Maildir R flag, which mbsync carries to the server as
// \Answered. The mail tagger reads that flag to keep replied mail out of @Reply.
func (s *Store) MarkAnswered(path string) (string, error) {
	destination := path
	used, err := s.withResolved(path, func(path string) error {
		folder, _, err := s.locate(path)
		if err != nil {
			return err
		}
		base, flags := splitFlags(filepath.Base(path))
		if strings.Contains(flags, "R") {
			destination = path
			return nil
		}
		set := []rune(flags + "R")
		sort.Slice(set, func(i, j int) bool { return set[i] < set[j] })
		destination = filepath.Join(folder, "cur", base+":2,"+string(set))
		if err := moveWithoutReplace(path, destination); err != nil {
			return fmt.Errorf("mark answered: %w", err)
		}
		return nil
	})
	if err != nil {
		return used, err
	}
	return destination, nil
}

// AccountDir returns the Maildir directory of the account shown under label.
func (s *Store) AccountDir(label string) (string, error) {
	for _, root := range s.Roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() && accountLabel(entry.Name()) == label && findInbox(filepath.Join(root, entry.Name())) != "" {
				return filepath.Join(root, entry.Name()), nil
			}
		}
	}
	return "", fmt.Errorf("no Maildir account %q", label)
}

// SaveSent files a copy of a sent message, marked seen, in the account's Sent
// Maildir. Only for servers that do not keep SMTP submissions themselves.
func (s *Store) SaveSent(label string, message []byte) error {
	accountDir, err := s.AccountDir(label)
	if err != nil {
		return err
	}
	dir := filepath.Join(accountDir, "Sent", "cur")
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("%s has no Sent Maildir", label)
	}
	host, _ := os.Hostname()
	name := fmt.Sprintf("%d.%d_%d.%s:2,S", time.Now().Unix(), os.Getpid(), time.Now().UnixNano()%1e9, strings.ReplaceAll(host, "/", "_"))
	tmp := filepath.Join(accountDir, "Sent", "tmp")
	_ = os.MkdirAll(tmp, 0o700)
	staging := filepath.Join(tmp, name)
	if err := os.WriteFile(staging, message, 0o600); err != nil {
		return err
	}
	return os.Rename(staging, filepath.Join(dir, name))
}

func sameDir(a, b string) (bool, error) {
	infoA, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	infoB, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(infoA, infoB), nil
}

func moveWithoutReplace(source, destination string) error {
	if _, err := os.Lstat(destination); err == nil {
		return fs.ErrExist
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.Rename(source, destination)
}

// maildirParts splits path, relative to the root holding it, into account,
// box and leaf names. It checks only that the path is a file inside some
// account's Maildir cur/ or new/ under one of the roots.
func (s *Store) maildirParts(path string) (absolute string, parts []string, err error) {
	absolute, err = filepath.Abs(path)
	if err != nil {
		return "", nil, err
	}
	for _, root := range s.Roots {
		relative, err := filepath.Rel(root, absolute)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		parts = strings.Split(relative, string(filepath.Separator))
		break
	}
	if parts == nil {
		return "", nil, fmt.Errorf("message is outside the mail roots")
	}
	if len(parts) < 4 || (parts[len(parts)-2] != "cur" && parts[len(parts)-2] != "new") {
		return "", nil, fmt.Errorf("message is not in a Maildir")
	}
	return absolute, parts, nil
}

func regularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("message is not a regular file")
	}
	return nil
}

// locateRead checks that path is a message file in any Maildir under one of
// the roots, such as Spam, Junk Email or a nested folder that search hits come
// from, and returns that folder and its cur/new leaf. Reading changes nothing,
// so unlike locate it does not limit the folders.
func (s *Store) locateRead(path string) (folder, leaf string, err error) {
	absolute, parts, err := s.maildirParts(path)
	if err != nil {
		return "", "", err
	}
	if err := regularFile(absolute); err != nil {
		return "", "", err
	}
	return filepath.Dir(filepath.Dir(absolute)), parts[len(parts)-2], nil
}

// locate is locateRead for the mutating calls: it also demands that the
// message sits in an inbox, tag, Trash, Archive or Sent Maildir, the
// mailboxes Mailday shows and files into.
func (s *Store) locate(path string) (folder, leaf string, err error) {
	absolute, parts, err := s.maildirParts(path)
	if err != nil {
		return "", "", err
	}
	box := filepath.Join(parts[1 : len(parts)-2]...)
	if !(isInboxName(box) || (strings.HasPrefix(box, "@") && len(parts) == 4) || isTrashName(box) || box == ArchiveBox || isSentName(box)) {
		return "", "", fmt.Errorf("message is not in a mailbox Mailday shows")
	}
	if err := regularFile(absolute); err != nil {
		return "", "", err
	}
	return filepath.Dir(filepath.Dir(absolute)), parts[len(parts)-2], nil
}

// Lookup reads the messages at paths (notmuch search results), naming their
// box as List does; folders List does not show keep their own name.
func (s *Store) Lookup(paths []string) []Message {
	var messages []Message
	for _, path := range paths {
		absolute, err := filepath.Abs(path)
		if err != nil {
			continue
		}
		for _, root := range s.Roots {
			relative, err := filepath.Rel(root, absolute)
			if err != nil || strings.HasPrefix(relative, "..") {
				continue
			}
			parts := strings.Split(relative, string(filepath.Separator))
			if len(parts) < 4 || (parts[len(parts)-2] != "cur" && parts[len(parts)-2] != "new") {
				break
			}
			info, err := os.Stat(absolute)
			if err != nil || !info.Mode().IsRegular() {
				break
			}
			leaf := parts[len(parts)-2]
			message, err := readHeader(absolute, accountLabel(parts[0]), leaf == "new" || !seenFlag(filepath.Base(absolute)), info)
			if err != nil {
				break
			}
			box := filepath.Join(parts[1 : len(parts)-2]...)
			switch {
			case isInboxName(box):
				message.Box = InboxBox
			case isSentName(box):
				message.Box = SentBox
			case box == ArchiveBox:
				message.Box = ArchiveBox
			default:
				message.Box = terminal.SanitizeLine(box)
			}
			messages = append(messages, message)
			break
		}
	}
	return messages
}

// boxOf returns a Maildir folder's name relative to its account directory.
func (s *Store) boxOf(folder string) (string, bool) {
	for _, root := range s.Roots {
		relative, err := filepath.Rel(root, folder)
		if err != nil || strings.HasPrefix(relative, "..") {
			continue
		}
		parts := strings.Split(relative, string(filepath.Separator))
		if len(parts) >= 2 {
			return filepath.Join(parts[1:]...), true
		}
	}
	return "", false
}

// Resolve finds a message again after it moved: reading it renames the file
// (new/ to cur/, flags change), filing moves it to another box, and mbsync
// adds ",U=" once it has a UID, but Key, the unique part of a Maildir name,
// stays. It returns path itself when that still exists, and "" when the
// message is gone. It looks in every Maildir one or two levels below an
// account (so "[Gmail]/Sent Mail" counts), the message's own account first.
func (s *Store) Resolve(path string) string {
	if _, err := os.Stat(path); err == nil {
		return path
	}
	unique := Key(path)
	if unique == "" {
		return ""
	}
	var accounts []string
	if absolute, parts, err := s.maildirParts(path); err == nil {
		for _, root := range s.Roots {
			if relative, err := filepath.Rel(root, absolute); err == nil && !strings.HasPrefix(relative, "..") {
				accounts = append(accounts, filepath.Join(root, parts[0]))
				break
			}
		}
	}
	for _, root := range s.Roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				if account := filepath.Join(root, entry.Name()); !slices.Contains(accounts, account) {
					accounts = append(accounts, account)
				}
			}
		}
	}
	for _, account := range accounts {
		if found := findKey(account, 2, unique); found != "" {
			return found
		}
	}
	return ""
}

// findKey looks for a message with the given Key in the Maildirs at most
// depth directory levels below dir.
func findKey(dir string, depth int, unique string) string {
	for _, leaf := range []string{"cur", "new"} {
		names, err := readNames(filepath.Join(dir, leaf))
		if err != nil {
			continue
		}
		for _, name := range names {
			if Key(name) == unique {
				return filepath.Join(dir, leaf, name)
			}
		}
	}
	if depth == 0 {
		return ""
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == "cur" || entry.Name() == "new" || entry.Name() == "tmp" {
			continue
		}
		if found := findKey(filepath.Join(dir, entry.Name()), depth-1, unique); found != "" {
			return found
		}
	}
	return ""
}

// readNames lists a directory without sorting it, which matters in Gmail's
// All Mail.
func readNames(dir string) ([]string, error) {
	file, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return file.Readdirnames(-1)
}

// FindByMessageID finds the message with the given Message-ID in one box of an
// account, for when its file was replaced: after the daemon moves a message on
// the server it deletes the local copy, and mbsync later downloads it under a
// new name, so neither the old path nor its Key finds it. account is the label
// shown in the list or the account's folder name; box is InboxBox, SentBox,
// "Trash", ArchiveBox or any folder name below the account.
func (s *Store) FindByMessageID(account, box, messageID string) (Message, bool) {
	wanted := normaliseMessageID(messageID)
	if wanted == "" {
		return Message{}, false
	}
	for _, root := range s.Roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() || (entry.Name() != account && accountLabel(entry.Name()) != account) {
				continue
			}
			folder := boxFolder(filepath.Join(root, entry.Name()), box)
			if folder == "" {
				continue
			}
			if message, ok := s.findInFolder(folder, accountLabel(entry.Name()), box, wanted); ok {
				return message, true
			}
		}
	}
	return Message{}, false
}

// boxFolder is the Maildir that holds box in an account directory, or "".
func boxFolder(accountDir, box string) string {
	switch {
	case box == InboxBox:
		return findInbox(accountDir)
	case box == SentBox:
		return findSent(accountDir)
	case box == "Trash" || box == "Deleted Items":
		return findTrash(accountDir)
	case box == "" || filepath.IsAbs(box) || slices.Contains(strings.Split(filepath.ToSlash(box), "/"), ".."):
		return ""
	}
	if folder := filepath.Join(accountDir, box); isMaildir(folder) {
		return folder
	}
	return ""
}

func (s *Store) findInFolder(folder, account, box, wanted string) (Message, bool) {
	for _, leaf := range []string{"new", "cur"} {
		dir := filepath.Join(folder, leaf)
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range files {
			if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			message, err := s.cache.header(path, account, leaf == "new" || !seenFlag(entry.Name()), info)
			if err != nil || normaliseMessageID(message.MessageID) != wanted {
				continue
			}
			message.Box = box
			return message, true
		}
	}
	return Message{}, false
}

func normaliseMessageID(id string) string {
	return strings.Trim(strings.TrimSpace(id), "<>")
}
