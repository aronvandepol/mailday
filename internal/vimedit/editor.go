// Package vimedit is a modal, Neovim-style editing engine for the composer's
// message body. It knows nothing of the screen: the caller feeds it keys and
// shows Text and Cursor. The buffer is the single source of truth, insert mode
// included, so the widget that draws the text never edits it.
package vimedit

import (
	"slices"
	"strconv"
	"strings"
	"time"
)

// Mode is what the next key will do.
type Mode int

const (
	Insert Mode = iota
	Normal
	Visual
	VisualLine
	Command // the ':' line
)

func (m Mode) String() string {
	return [...]string{"INSERT", "NORMAL", "VISUAL", "VISUAL LINE", "COMMAND"}[m]
}

// Action is something the editor asks its host to do; the editor cannot
// close or save anything itself.
type Action int

const (
	None Action = iota
	Close
	SaveDraft
	Preview
)

// Result tells the host what a key did.
type Result struct {
	Changed bool // the text or the undo history changed
	Action  Action
}

// EscHint is shown after a lone esc in normal mode.
const EscHint = "esc again closes · :w saves · ZZ previews"

const (
	undoLimit  = 200
	escWindow  = time.Second
	shiftWidth = 2
	maxCount   = 99999
	endOfLine  = 1 << 30 // the wanted column after $
)

type register struct {
	text     string
	linewise bool
}

// snapshot is the state before a change, for undo; redo entries hold the state
// after it, with the same cursor.
type snapshot struct {
	lines    []string
	row, col int
}

// keyEvent is one key as it arrived, kept so '.' can play a change again.
// count marks digits that were a count, which '.' may replace.
type keyEvent struct {
	key, text string
	count     bool
}

type findState struct {
	kind string // f F t T, empty before the first
	char rune
}

// Editor is the modal editor. Create it with New.
type Editor struct {
	Buf Buffer
	// Now is the clock for the double esc; tests replace it.
	Now func() time.Time

	mode   Mode
	anchor Pos // the other end of the visual selection

	// A command being typed: [register] [count] operator [count] motion.
	count, opCount int
	op             string
	pend           string // waiting for the key that completes g f F t T r " i a Z
	regName        rune

	cmdline    []rune
	cmdKind    rune // ':' for a command, '/' or '?' for a search
	cmdReturn  Mode // where a search prompt goes back to
	lastSearch searchState
	status     string
	lastEsc    time.Time
	res        Result

	unnamed register
	named   map[rune]register
	lastFnd findState

	undo, redo []snapshot
	snapTaken  bool // this command already saved its undo snapshot

	recording  bool
	rec        []keyEvent
	replaying  bool
	dotRun     bool
	lastChange []keyEvent

	insertKind  string
	insertCount int
	typed       []rune // what insert mode typed, for 3ifoo<esc>
	typedOK     bool

	want     int  // the column j and k try to keep
	keepWant bool // this key moved vertically, so want stands

	// gj and gk keep a display column, in cells, as wide characters need.
	// -1 means none yet; keepScreen says this key was a gj or gk.
	wrapWidth  int
	wantScreen int
	keepScreen bool

	// hlsearch: the last pattern stays highlighted until :noh.
	hlsearch bool

	// Macros: q{a-z} records keys, @{a-z} plays them back through press.
	macros     map[rune][]keyEvent
	macroName  rune // the register being recorded, 0 when not recording
	macroKeys  []keyEvent
	lastMacro  rune
	macroDepth int  // macros playing inside macros
	macroAbort bool // a motion failed or the depth limit was hit: stop playing
}

// New starts an editor on text in insert mode with the cursor at the start,
// which is how the composer opens its body.
func New(text string) *Editor {
	e := &Editor{Now: time.Now, named: map[rune]register{}, macros: map[rune][]keyEvent{}, wantScreen: -1, mode: Insert, insertCount: 1, typedOK: true}
	e.Buf.setText(text)
	return e
}

func (e *Editor) Mode() Mode { return e.mode }

// Text is the whole buffer.
func (e *Editor) Text() string { return e.Buf.text() }

// Cursor is the row and the rune column of the cursor.
func (e *Editor) Cursor() (row, col int) { return e.Buf.Row, e.Buf.Col }

// CommandLine is what has been typed after ':'.
func (e *Editor) CommandLine() string { return string(e.cmdline) }

// Status is a short message for the last key, such as the esc hint; it is
// empty after most keys.
func (e *Editor) Status() string { return e.status }

// Pending shows a half-typed command such as 2d or "a for the status line.
func (e *Editor) Pending() string {
	var out strings.Builder
	if e.regName != 0 {
		out.WriteString(`"` + string(e.regName))
	}
	if e.count > 0 {
		out.WriteString(strconv.Itoa(e.count))
	}
	out.WriteString(e.op)
	if e.opCount > 0 {
		out.WriteString(strconv.Itoa(e.opCount))
	}
	out.WriteString(e.pend)
	return out.String()
}

// Selection is the visual selection as the text from start up to, not
// including, end. ok is false outside visual mode.
func (e *Editor) Selection() (start, end Pos, ok bool) {
	if !e.isVisual() {
		return Pos{}, Pos{}, false
	}
	r := e.visualRegion()
	if r.linewise {
		return Pos{r.start.Row, 0}, Pos{r.end.Row, e.Buf.length(r.end.Row)}, true
	}
	return r.start, r.end, true
}

// StartInsert switches to insert mode where the cursor is, for when the host
// moves focus into the body to write.
func (e *Editor) StartInsert() {
	e.closeUndoStep()
	e.recording, e.rec = false, nil
	e.clearPending()
	e.cmdline = nil
	e.mode = Insert
	e.beginInsert("i", 1)
}

// SetCursor places the cursor, clamped to the text.
func (e *Editor) SetCursor(row, col int) {
	e.Buf.Row, e.Buf.Col = row, col
	e.fixCursor()
	e.want = e.Buf.Col
}

// SetText replaces the text because the host changed it, such as swapping a
// signature. It is one undo step; the cursor stays where it was, clamped.
func (e *Editor) SetText(text string) {
	if text == e.Buf.text() {
		return
	}
	e.closeUndoStep()
	e.beginChange()
	e.Buf.setText(text)
	e.closeUndoStep()
	e.fixCursor()
}

// Paste puts pasted text at the cursor: where the cursor is in insert mode,
// after the character under it, as p does, in normal mode.
func (e *Editor) Paste(text string) Result {
	e.res = Result{}
	e.status = ""
	switch {
	case text == "":
	case e.mode == Insert:
		if e.recording && !e.replaying {
			e.rec = append(e.rec, keyEvent{text: text})
		}
		if e.macroName != 0 && e.macroDepth == 0 {
			e.macroKeys = append(e.macroKeys, keyEvent{text: text})
		}
		e.insertText(text)
	case e.mode == Command:
		first, _, _ := strings.Cut(text, "\n")
		e.cmdline = append(e.cmdline, []rune(first)...)
	default:
		e.mode = Normal
		e.clearPending()
		e.beginChange()
		at := e.Buf.cursor()
		if e.Buf.length(at.Row) > 0 {
			at.Col++
		}
		end := e.Buf.insertAt(at, text)
		e.Buf.setCursor(Pos{end.Row, max(end.Col-1, 0)})
		e.endCommand()
	}
	e.fixCursor()
	e.want = e.Buf.Col
	return e.res
}

// Key handles one key. key is bubbletea's key.String() ("h", "ctrl+r",
// "esc", "enter", "backspace") and text is what the key types, empty for
// keys that type nothing.
func (e *Editor) Key(key, text string) Result {
	e.res = Result{}
	e.status = ""
	e.press(key, text)
	return e.res
}

// press is one key typed, or one key of a macro played back, so both go
// through the same handling and '.' and undo see no difference.
func (e *Editor) press(key, text string) {
	if key != "esc" {
		e.lastEsc = time.Time{}
	}
	if e.stopsMacro(key, text) {
		return
	}
	if e.macroName != 0 && e.macroDepth == 0 {
		e.macroKeys = append(e.macroKeys, keyEvent{key: key, text: text})
	}
	if e.mode == Normal && e.idle() && !e.recording {
		e.recording, e.rec = true, nil
	}
	e.keepWant, e.keepScreen = false, false
	e.dispatch(key, text)
	if e.mode == Normal && e.idle() {
		e.endCommand()
	}
	e.fixCursor()
	if !e.keepWant {
		e.want = e.Buf.Col
	}
	if !e.keepScreen && e.idle() { // the g of the next gj is not a break
		e.wantScreen = -1
	}
}

func (e *Editor) dispatch(key, text string) {
	if e.recording && !e.replaying {
		e.rec = append(e.rec, keyEvent{key: key, text: text})
	}
	switch e.mode {
	case Insert:
		e.insertKey(key, text)
	case Command:
		e.commandKey(key, text)
	default:
		tok := text
		if tok == "" {
			tok = key
		}
		e.normalKey(tok)
	}
}

func (e *Editor) idle() bool {
	return e.count == 0 && e.opCount == 0 && e.op == "" && e.pend == "" && e.regName == 0
}

func (e *Editor) clearPending() {
	e.count, e.opCount, e.op, e.pend, e.regName = 0, 0, "", "", 0
}

// countN is the count typed before and after the operator multiplied, or 0
// when none was typed.
func (e *Editor) countN() int {
	if e.count == 0 && e.opCount == 0 {
		return 0
	}
	return max(e.count, 1) * max(e.opCount, 1)
}

func (e *Editor) isVisual() bool { return e.mode == Visual || e.mode == VisualLine }

// fixCursor keeps the cursor on the text: on a character in normal mode, up
// to just past the end in insert mode.
func (e *Editor) fixCursor() {
	b := &e.Buf
	if len(b.Lines) == 0 {
		b.Lines = []string{""}
	}
	b.Row = min(max(b.Row, 0), b.lastRow())
	limit := max(b.length(b.Row)-1, 0)
	if e.mode == Insert {
		limit = b.length(b.Row)
	}
	b.Col = min(max(b.Col, 0), limit)
	e.anchor.Row = min(max(e.anchor.Row, 0), b.lastRow())
	e.anchor.Col = min(max(e.anchor.Col, 0), max(b.length(e.anchor.Row)-1, 0))
}

// --- undo ---------------------------------------------------------------

func (e *Editor) capture() snapshot {
	return snapshot{lines: slices.Clone(e.Buf.Lines), row: e.Buf.Row, col: e.Buf.Col}
}

// beginChange is called before every edit. The first edit of a command saves
// the undo snapshot, so everything typed in one insert session is one step.
func (e *Editor) beginChange() {
	e.res.Changed = true
	if e.snapTaken {
		return
	}
	e.snapTaken = true
	e.undo = append(e.undo, e.capture())
	if len(e.undo) > undoLimit {
		e.undo = e.undo[1:]
	}
	e.redo = nil
}

// closeUndoStep ends the undo step being built. An edit that left the text as
// it was is dropped from the history; it says whether a real change was kept.
func (e *Editor) closeUndoStep() bool {
	if !e.snapTaken {
		return false
	}
	e.snapTaken = false
	if len(e.undo) > 0 && slices.Equal(e.undo[len(e.undo)-1].lines, e.Buf.Lines) {
		e.undo = e.undo[:len(e.undo)-1]
		return false
	}
	return true
}

// endCommand closes the command that has just finished; a real change becomes
// what '.' repeats.
func (e *Editor) endCommand() {
	if e.closeUndoStep() && e.recording && !e.dotRun && len(e.rec) > 0 {
		e.lastChange = slices.Clone(e.rec)
	}
	e.recording, e.rec, e.dotRun = false, nil, false
}

func (e *Editor) undoChange(times int) {
	for range max(times, 1) {
		if len(e.undo) == 0 {
			e.status = "Already at the oldest change"
			return
		}
		previous := e.undo[len(e.undo)-1]
		e.undo = e.undo[:len(e.undo)-1]
		e.redo = append(e.redo, snapshot{lines: slices.Clone(e.Buf.Lines), row: previous.row, col: previous.col})
		e.restore(previous)
	}
}

func (e *Editor) redoChange(times int) {
	for range max(times, 1) {
		if len(e.redo) == 0 {
			e.status = "Already at the newest change"
			return
		}
		next := e.redo[len(e.redo)-1]
		e.redo = e.redo[:len(e.redo)-1]
		e.undo = append(e.undo, snapshot{lines: slices.Clone(e.Buf.Lines), row: next.row, col: next.col})
		e.restore(next)
	}
}

func (e *Editor) restore(s snapshot) {
	e.res.Changed = true
	e.Buf.Lines = slices.Clone(s.lines)
	e.Buf.Row, e.Buf.Col = s.row, s.col
}

// --- '.' ----------------------------------------------------------------

// repeatChange plays the last change again through the same code that ran it,
// so every command and text object repeats without a second implementation.
// A count replaces the one the change had. After a visual operator it repeats
// the keys from the cursor, not vim's same-sized region.
func (e *Editor) repeatChange(count int) {
	keys := e.lastChange
	if count > 0 {
		keys = nil
		for _, digit := range strconv.Itoa(count) {
			keys = append(keys, keyEvent{key: string(digit), text: string(digit)})
		}
		for _, event := range e.lastChange {
			if !event.count {
				keys = append(keys, event)
			}
		}
	}
	e.clearPending()
	e.replaying, e.dotRun = true, true
	for _, event := range keys {
		e.dispatch(event.key, event.text)
	}
	e.replaying = false
}
