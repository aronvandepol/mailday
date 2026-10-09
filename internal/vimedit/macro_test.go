package vimedit

import (
	"strings"
	"testing"
)

func TestMacros(t *testing.T) {
	runCases(t, []editCase{
		{"record and replay", "|a\nb\nc\nd", "qaA!<esc>jq@a", "a!\nb!\n|c\nd"},
		{"the recording does the edits once", "|a\nb", "qaA!<esc>q", "a|!\nb"},
		{"@a twice", "|a\nb\nc\nd", "qaA!<esc>jq@a@a", "a!\nb!\nc!\n|d"},
		{"count", "|a\nb\nc\nd", "qaA!<esc>jq2@a", "a!\nb!\nc!\n|d"},
		{"@@ repeats the last", "|a\nb\nc\nd", "qaA!<esc>jq@a@@", "a!\nb!\nc!\n|d"},
		{"count with @@", "|a\nb\nc\nd\ne", "qaA!<esc>jq@a2@@", "a!\nb!\nc!\nd!\n|e"},
		{"two registers", "|a b", "qaxqqbiX<esc>q@a@b", "|X b"},
		{"unrecorded register does nothing", "|ab", "@z", "|ab"},
		{"@@ before any macro does nothing", "|ab", "@@", "|ab"},
		{"counted insert with a blank", "|x", "qa3ia <esc>q", "a a a| x"},
		{"operator and motion", "|one two three", "qadwq@a", "|three"},
		{"search inside a macro", "|a1 b a2 b a3", "qa/a<cr>xq@a", "a1 b 2 b |3"},
		{"Korean text", "|가\n나", "qaA다<esc>jq@a", "가다\n나|다"},
		{"a failed motion stops the count", "|a\nb", "qaA!<esc>jq5@a", "a!\nb|!"},
		{"a recursive macro ends when a motion fails", "|a\nb\nc\nd", "qaqqaA!<esc>j@aq@a", "a!\nb!\nc!\nd|!"},
		{"dot repeats the last change inside the macro", "|abcd", "qaxq@a.", "|d"},
		{"undo undoes one command of the macro at a time", "|abcdef", "qaxxq@au", "|def"},
		{"and the next one", "|abcdef", "qaxxq@auu", "|cdef"},
		{"recording again replaces the register", "|abcd", "qaxqqaxxq@a", "|"},
		{"an empty recording empties it", "|abcd", "qaxqqaq@a", "|bcd"},
		{"q after f is the character searched for", "|aq aq", "qafqxq@a", "a |a"},
	})
}

func TestRecordingNameAndStoredKeys(t *testing.T) {
	e := normalAt(t, "|abc")
	feed(t, e, "q")
	if e.Recording() != 0 || e.Pending() != "q" {
		t.Errorf("q alone: recording %q, pending %q", e.Recording(), e.Pending())
	}
	feed(t, e, "a")
	if e.Recording() != 'a' || e.Pending() != "" {
		t.Errorf("after qa: recording %q, pending %q", e.Recording(), e.Pending())
	}
	feed(t, e, "xl")
	feed(t, e, "q")
	if e.Recording() != 0 {
		t.Errorf("still recording after q")
	}
	if got := len(e.macros['a']); got != 2 {
		t.Errorf("the macro holds %d keys, want x and l without the final q", got)
	}
	feed(t, e, "q1")
	if e.Recording() != 0 {
		t.Errorf("q1 is not a register")
	}
}

func TestMacroRecursionIsLimited(t *testing.T) {
	e := normalAt(t, "|")
	feed(t, e, "qaqqaiX<esc>@aq")
	before := strings.Count(e.Text(), "X")
	if before != 1 {
		t.Fatalf("recording typed %d X, want 1 (the empty macro ran inside it)", before)
	}
	feed(t, e, "@a")
	if got := strings.Count(e.Text(), "X") - before; got != maxMacroDepth {
		t.Errorf("a self-playing macro ran %d times, want the limit %d", got, maxMacroDepth)
	}
	if !strings.Contains(e.Status(), "too deeply") {
		t.Errorf("status = %q", e.Status())
	}
	if e.macroDepth != 0 {
		t.Errorf("depth %d left after the macro", e.macroDepth)
	}
}

func TestMacroReplaysPastedText(t *testing.T) {
	e := normalAt(t, "|x")
	feed(t, e, "qai")
	e.Paste("yo ")
	feed(t, e, "<esc>q@a")
	if got := e.Text(); got != "yoyo  x" { // i inserts before the blank the cursor rests on
		t.Errorf("text = %q", got)
	}
}

func TestMacroReplayNeverClosesWithDoubleEsc(t *testing.T) {
	e := normalAt(t, "|a")
	feed(t, e, "qa<esc><esc>q")
	if actions := feed(t, e, "@a"); len(actions) != 0 {
		t.Errorf("replayed esc esc asked for %v", actions)
	}
}
