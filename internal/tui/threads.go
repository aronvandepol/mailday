package tui

// Conversations in the mail list: messages that answer each other share one
// row (newest message, a "(3)" badge, "●" when any is unread), space opens
// the thread under it, and a, f, d, m and Z on the collapsed row act on all of
// it. T in the reader lists the whole conversation across boxes.
// MAILDAY_THREADS=off keeps one row per message.

import (
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/aronvandepol/mailday/internal/maildir"
)

// threadState is what the list remembers about conversations.
type threadState struct {
	// expanded holds the keys of the threads opened with space. It is
	// replaced, never edited, because Model is copied on every update.
	expanded map[string]bool
	// filingGroup is the thread f was pressed on, so the box picker files
	// all of it.
	filingGroup  []maildir.Message
	conversation *conversation
}

// threadsEnabled is false when MAILDAY_THREADS turns grouping off.
func threadsEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MAILDAY_THREADS"))) {
	case "off", "0", "no", "false":
		return false
	}
	return true
}

func init() {
	addHelp("Mail list", [][2]string{
		{"space", "open or close a conversation; a, f, d, m, Z on its row act on every message"},
	})
	addHelp("Reading a message", [][2]string{{"T", "the whole conversation, across boxes"}})
}

// mailRow is one line pair of the list: a message, or the newest message of
// a conversation, or an older message shown under an opened conversation.
type mailRow struct {
	index   int   // the message the row shows
	section int   // 0 New for You, 1 Previously Seen, 2 Trash and Spam
	members []int // a conversation's messages in this view, newest first; nil on child rows
	child   bool  // indented under an opened conversation
	unread  bool  // any message of the conversation is unread
	key     string
}

// count is how many messages the row stands for.
func (row mailRow) count() int { return len(row.members) }

// collapsed says whether the row hides other messages of its conversation.
func (row mailRow) collapsed(expanded map[string]bool) bool {
	return !row.child && len(row.members) > 1 && !expanded[row.key]
}

// rowMemo remembers the last list built. Grouping thousands of messages takes
// milliseconds and every key press asks for the rows several times (cursor,
// scrolling, drawing), so the answer is reused while its inputs, summed up in
// the fingerprint, are the same. One slot is enough: the list on screen is
// the only one asked for repeatedly.
var rowMemo struct {
	sync.Mutex
	print uint64
	rows  []mailRow
}

// rowsFingerprint sums up everything mailRows reads: the messages as far as
// grouping and ordering see them (a path never changes its headers), the
// box, account and search, the search hits and the opened conversations.
func (m Model) rowsFingerprint() uint64 {
	var sum uint64 = fnvOffset
	for _, message := range m.messages {
		sum = fnvString(sum, message.Path)
		sum = fnvString(sum, message.Box)
		sum = fnvNumber(sum, uint64(message.Date.UnixNano()))
		if message.Unread {
			sum = fnvNumber(sum, 1)
		}
	}
	sum = fnvString(sum, strings.ToLower(strings.TrimSpace(m.query)))
	sum = fnvString(sum, m.activeBox())
	sum = fnvNumber(sum, uint64(m.mailAccount+1))
	if m.mailAccount >= 0 && m.mailAccount < len(m.accounts) {
		sum = fnvString(sum, m.accounts[m.mailAccount])
	}
	if threadsEnabled() {
		sum = fnvNumber(sum, 2)
	}
	// Sets are summed in a way that does not depend on map order.
	var hits, opened uint64
	for key := range m.searchHits {
		hits ^= fnvString(fnvOffset, key)
	}
	for key := range m.threads.expanded {
		opened ^= fnvString(fnvOffset, key)
	}
	sum = fnvNumber(sum, uint64(len(m.searchHits)))
	sum = fnvNumber(sum, hits)
	sum = fnvNumber(sum, uint64(len(m.threads.expanded)))
	return fnvNumber(sum, opened)
}

const (
	fnvOffset = 14695981039346656037
	fnvPrime  = 1099511628211
)

// fnvString and fnvNumber fold a value into an FNV-1a sum without allocating.
func fnvString(sum uint64, value string) uint64 {
	for index := range len(value) {
		sum = (sum ^ uint64(value[index])) * fnvPrime
	}
	return (sum ^ 0xff) * fnvPrime
}

func fnvNumber(sum, value uint64) uint64 {
	for range 8 {
		sum = (sum ^ (value & 0xff)) * fnvPrime
		value >>= 8
	}
	return sum
}

// mailRows is the list as shown: filtered, grouped into conversations and
// ordered (unread conversations first). The slice is shared; do not change it.
// filteredMessageIndexes is this, one message index per row.
func (m Model) mailRows() []mailRow {
	print := m.rowsFingerprint()
	rowMemo.Lock()
	if rowMemo.rows != nil && rowMemo.print == print {
		rows := rowMemo.rows
		rowMemo.Unlock()
		return rows
	}
	rowMemo.Unlock()
	rows := m.buildMailRows()
	if rows == nil {
		rows = []mailRow{}
	}
	rowMemo.Lock()
	rowMemo.print, rowMemo.rows = print, rows
	rowMemo.Unlock()
	return rows
}

func (m Model) buildMailRows() []mailRow {
	flat := m.matchingMessageIndexes()
	searching := strings.TrimSpace(m.query) != ""
	// @Snoozed is a to-do list in due order (snooze.go): every message keeps
	// its own row and its own place, not its conversation's.
	if !threadsEnabled() || (!searching && m.activeBox() == snoozedBox) {
		rows := make([]mailRow, len(flat))
		for position, index := range flat {
			message := m.messages[index]
			rows[position] = mailRow{index: index, section: m.mailSection(message), members: []int{index}, unread: message.Unread}
		}
		return rows
	}

	// Trash and Spam hits in a search stay a section of their own, so a
	// conversation is not pulled out of it by one of its messages.
	var live, binned []int
	for _, index := range flat {
		if searching && isBinBox(messageBox(m.messages[index])) {
			binned = append(binned, index)
		} else {
			live = append(live, index)
		}
	}
	type placed struct {
		threadGroup
		section int
	}
	var groups []placed
	for _, group := range threadGroups(m.messages, live) {
		section := 1
		if group.unread {
			section = 0
		}
		groups = append(groups, placed{group, section})
	}
	for _, group := range threadGroups(m.messages, binned) {
		groups = append(groups, placed{group, 2})
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].section != groups[j].section {
			return groups[i].section < groups[j].section
		}
		first, second := groups[i].members[0], groups[j].members[0]
		if searching {
			return m.messages[first].Date.After(m.messages[second].Date)
		}
		return first < second // the store already lists newest first
	})

	rows := make([]mailRow, 0, len(groups))
	for _, group := range groups {
		head := mailRow{index: group.members[0], section: group.section, members: group.members, unread: group.unread, key: group.key}
		rows = append(rows, head)
		if len(group.members) > 1 && m.threads.expanded[group.key] {
			for _, index := range group.members[1:] {
				rows = append(rows, mailRow{index: index, section: group.section, child: true, unread: m.messages[index].Unread, key: group.key})
			}
		}
	}
	return rows
}

// filteredMessageIndexes is the message shown on each row of the list.
func (m Model) filteredMessageIndexes() []int {
	rows := m.mailRows()
	indexes := make([]int, len(rows))
	for position, row := range rows {
		indexes[position] = row.index
	}
	return indexes
}

// visibleMessageIndexes is every message of the list, those inside collapsed
// conversations too, in list order: what n and p in the reader walk through.
func (m Model) visibleMessageIndexes() []int {
	var indexes []int
	for _, row := range m.mailRows() {
		if !row.child {
			indexes = append(indexes, row.members...)
		}
	}
	return indexes
}

// cursorRow is the row under the list cursor.
func (m Model) cursorRow() (mailRow, bool) {
	rows := m.mailRows()
	if len(rows) == 0 || m.mailCursor < 0 || m.mailCursor >= len(rows) {
		return mailRow{}, false
	}
	return rows[m.mailCursor], true
}

// threadRowOf finds the row that shows path, which may be hidden inside a
// collapsed conversation.
func (m Model) threadRowOf(path string) (int, bool) {
	key := maildir.Key(path)
	for position, row := range m.mailRows() {
		for _, index := range row.members {
			if m.messages[index].Path == path || maildir.Key(m.messages[index].Path) == key {
				return position, true
			}
		}
	}
	return 0, false
}

// collapsedGroup is the messages a bulk action on the cursor row applies to:
// the conversation's messages in the row's box, when the row is a collapsed
// conversation, else nil.
func (m Model) collapsedGroup() []maildir.Message {
	row, ok := m.cursorRow()
	if !ok || !row.collapsed(m.threads.expanded) {
		return nil
	}
	head := m.messages[row.index]
	var group []maildir.Message
	for _, index := range row.members {
		message := m.messages[index]
		// A search shows a conversation across boxes; "this box" is the
		// newest message's.
		if message.Account == head.Account && messageBox(message) == messageBox(head) {
			group = append(group, message)
		}
	}
	if len(group) < 2 {
		return nil
	}
	return group
}

// threadOpenTarget is the message enter opens on a collapsed conversation:
// the newest unread one, else the newest.
func (m Model) threadOpenTarget() (maildir.Message, bool) {
	row, ok := m.cursorRow()
	if !ok || !row.collapsed(m.threads.expanded) {
		return maildir.Message{}, false
	}
	for _, index := range row.members {
		if m.messages[index].Unread {
			return m.messages[index], true
		}
	}
	return m.messages[row.index], true
}

// threadGroup is one conversation within a list of messages.
type threadGroup struct {
	key     string // stable while the conversation grows: its oldest message's root id
	members []int  // indexes into the messages, newest first
	unread  bool
}

// threadGroups groups the messages at indexes into conversations, in order of
// first appearance. A message belongs with every id in its References and
// In-Reply-To (so with the root, the first Reference) and with its own id.
// Messages no id links are still joined by a normalised subject when one of
// them is a reply or forward and the senders are among each other's
// participants. Ids are per account: a copy of a message in two accounts is
// not one conversation.
func threadGroups(messages []maildir.Message, indexes []int) []threadGroup {
	sets := newDisjointSet()
	type entry struct {
		anchor    string // the node the message is joined to
		root      string // the thread's id as its message names it
		subject   string
		replied   bool // the subject had Re:, Fwd: or the like
		date      time.Time
		from      string
		to        string
		recipient map[string]bool // the addresses in to, read when a subject bucket needs them
	}
	entries := make([]entry, len(indexes))
	bySubject := make(map[string][]int)
	for position, index := range indexes {
		message := messages[index]
		scope := message.Account + "\x00"
		if isDraft(message) {
			// A draft is not part of a conversation.
			entries[position] = entry{anchor: scope + "path:" + message.Path, root: "path:" + message.Path}
			continue
		}
		own, references := threadIDs(message)
		anchor := own
		if anchor == "" {
			anchor = "path:" + maildir.Key(message.Path)
		}
		root := anchor
		if len(references) > 0 {
			root = references[0]
		}
		entries[position].anchor, entries[position].root = scope+anchor, root
		for _, reference := range references {
			sets.union(scope+anchor, scope+reference)
		}
		subject, replied := normaliseSubject(message.Subject)
		entries[position].subject, entries[position].replied = subject, replied
		entries[position].date = message.Date
		entries[position].from, entries[position].to = strings.ToLower(strings.TrimSpace(message.FromAddr)), message.To
		if !genericSubject(subject) {
			bySubject[scope+subject] = append(bySubject[scope+subject], position)
		}
	}
	// Reading addresses costs a scan per message, so only buckets a reply
	// could join are read, and each message once.
	recipients := func(at int) map[string]bool {
		if entries[at].recipient == nil {
			entries[at].recipient = addressSet(entries[at].to)
		}
		return entries[at].recipient
	}
	for _, bucket := range bySubject {
		if len(bucket) < 2 {
			continue
		}
		for _, replyAt := range bucket {
			if !entries[replyAt].replied {
				continue
			}
			for _, otherAt := range bucket {
				if otherAt == replyAt || sets.find(entries[replyAt].anchor) == sets.find(entries[otherAt].anchor) {
					continue
				}
				if entries[replyAt].date.Sub(entries[otherAt].date).Abs() > subjectLinkWindow {
					continue
				}
				if participantsLink(entries[replyAt].from, recipients(replyAt), entries[otherAt].from, recipients(otherAt)) {
					sets.union(entries[replyAt].anchor, entries[otherAt].anchor)
				}
			}
		}
	}

	var groups []threadGroup
	slot := make(map[string]int)
	for position, index := range indexes {
		set := sets.find(entries[position].anchor)
		at, seen := slot[set]
		if !seen {
			at = len(groups)
			slot[set] = at
			groups = append(groups, threadGroup{})
		}
		groups[at].members = append(groups[at].members, index)
		groups[at].unread = groups[at].unread || messages[index].Unread
	}
	roots := make(map[int]string, len(indexes))
	for position, index := range indexes {
		roots[index] = entries[position].root
	}
	for at := range groups {
		members := groups[at].members
		sort.SliceStable(members, func(i, j int) bool {
			return messages[members[i]].Date.After(messages[members[j]].Date)
		})
		oldest := members[len(members)-1]
		groups[at].key = messages[oldest].Account + "\x00" + roots[oldest]
	}
	return groups
}

// subjectLinkWindow is how far apart two messages without a linking id may
// be and still count as one conversation: the same subject months later is
// a new exchange.
const subjectLinkWindow = 60 * 24 * time.Hour

// genericSubjects are subjects so common that sharing one says nothing.
var genericSubjects = map[string]bool{
	"hi": true, "hello": true, "question": true, "vraag": true,
	"update": true, "meeting": true, "(no subject)": true,
}

// genericSubject says a normalised subject is too short or too common to
// join messages on its own.
func genericSubject(subject string) bool {
	return utf8.RuneCountInString(subject) < 4 || genericSubjects[subject]
}

// participantsLink says whether two messages could belong to one exchange:
// one's sender wrote the other, or both have the same sender. Addresses are
// compared whole ("an@x.nl" is not in "jan@x.nl"); from is lower-case.
func participantsLink(fromA string, toA map[string]bool, fromB string, toB map[string]bool) bool {
	if fromA == "" || fromB == "" {
		return false
	}
	return fromA == fromB || toB[fromA] || toA[fromB]
}

// addressPattern finds the addresses in a To header however it is written
// ("Ada <ada@x.org>, b@x.org"); a display name with a comma does not parse
// as a list, so the text is scanned rather than parsed.
var addressPattern = regexp.MustCompile(`[^\s<>,;"'()]+@[^\s<>,;"'()]+`)

// addressSet is the lower-case addresses in a header.
func addressSet(header string) map[string]bool {
	found := addressPattern.FindAllString(strings.ToLower(header), -1)
	set := make(map[string]bool, len(found))
	for _, address := range found {
		set[address] = true
	}
	return set
}

// threadIDs returns the message's own id and the ids it answers, References
// first (its first is the conversation's root) then In-Reply-To.
func threadIDs(message maildir.Message) (own string, references []string) {
	if ids := messageIDs(message.MessageID); len(ids) > 0 {
		own = ids[0]
	}
	for _, id := range append(messageIDs(message.References), messageIDs(message.InReplyTo)...) {
		if id != own && !slices.Contains(references, id) {
			references = append(references, id)
		}
	}
	return own, references
}

// messageIDs reads the <ids> of a Message-ID, References or In-Reply-To
// header, lower-cased; a header without brackets is read as words.
func messageIDs(header string) []string {
	var ids []string
	rest := header
	for {
		open := strings.IndexByte(rest, '<')
		if open < 0 {
			break
		}
		closing := strings.IndexByte(rest[open:], '>')
		if closing < 0 {
			break
		}
		if id := strings.TrimSpace(rest[open+1 : open+closing]); id != "" {
			ids = append(ids, strings.ToLower(id))
		}
		rest = rest[open+closing+1:]
	}
	if len(ids) == 0 {
		for _, word := range strings.Fields(header) {
			ids = append(ids, strings.ToLower(word))
		}
	}
	return ids
}

var (
	// subjectPrefix is a reply or forward marker in English, Dutch and German.
	subjectPrefix = regexp.MustCompile(`(?i)^\s*(re|fwd?|antw|aw|wg|doorst)(\[\d+\])?\s*:`)
	// subjectTag is a mailing-list tag such as "[golang-nuts]".
	subjectTag = regexp.MustCompile(`^\s*\[[^\]]{1,40}\]`)
)

// normaliseSubject strips Re:, Fwd:, Antw:, AW:, WG:, Fw: and Doorst: and
// [list] tags, and lower-cases what is left. replied says a reply or forward
// marker was there.
func normaliseSubject(subject string) (normalised string, replied bool) {
	for {
		if match := subjectPrefix.FindString(subject); match != "" {
			subject, replied = subject[len(match):], true
			continue
		}
		if match := subjectTag.FindString(subject); match != "" {
			subject = subject[len(match):]
			continue
		}
		break
	}
	return strings.ToLower(strings.Join(strings.Fields(subject), " ")), replied
}

// disjointSet joins strings into groups.
type disjointSet struct{ parent map[string]string }

func newDisjointSet() *disjointSet { return &disjointSet{parent: make(map[string]string)} }

func (s *disjointSet) find(node string) string {
	root := node
	for {
		next, known := s.parent[root]
		if !known || next == root {
			break
		}
		root = next
	}
	for node != root { // compress the path
		next := s.parent[node]
		s.parent[node] = root
		node = next
	}
	return root
}

func (s *disjointSet) union(a, b string) {
	rootA, rootB := s.find(a), s.find(b)
	if rootA != rootB {
		s.parent[rootB] = rootA
	}
}
