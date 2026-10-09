package vimedit

import (
	"reflect"
	"testing"
)

func TestMatchesAfterSearch(t *testing.T) {
	e := normalAt(t, "|foo bar\nFoo foo\nnone")
	if got := e.Matches(0, 2); got != nil {
		t.Fatalf("matches before any search = %v", got)
	}
	feed(t, e, "/foo<cr>")
	want := []Match{{Row: 0, Start: 0, End: 3}, {Row: 1, Start: 0, End: 3}, {Row: 1, Start: 4, End: 7}}
	if got := e.Matches(0, 2); !reflect.DeepEqual(got, want) {
		t.Errorf("smartcase lower: %v, want %v", got, want)
	}
	if got := e.Matches(1, 1); !reflect.DeepEqual(got, want[1:]) {
		t.Errorf("only the asked rows: %v", got)
	}
	feed(t, e, "/Foo<cr>")
	if got := e.Matches(0, 2); !reflect.DeepEqual(got, []Match{{Row: 1, Start: 0, End: 3}}) {
		t.Errorf("smartcase upper is exact: %v", got)
	}
}

func TestMatchesOverlapAndKorean(t *testing.T) {
	e := normalAt(t, "|aaaa\n가나다 라 가나다")
	feed(t, e, "/aa<cr>")
	if got := e.Matches(0, 0); !reflect.DeepEqual(got, []Match{{Row: 0, Start: 0, End: 2}, {Row: 0, Start: 2, End: 4}}) {
		t.Errorf("overlaps are not drawn twice: %v", got)
	}
	feed(t, e, "/가나다<cr>")
	if got := e.Matches(1, 1); !reflect.DeepEqual(got, []Match{{Row: 1, Start: 0, End: 3}, {Row: 1, Start: 6, End: 9}}) {
		t.Errorf("rune columns for Korean: %v", got)
	}
}

func TestHlsearchStaysUntilNoh(t *testing.T) {
	e := normalAt(t, "|x y x")
	feed(t, e, "/x<cr>")
	feed(t, e, "<esc>")
	if len(e.Matches(0, 0)) != 2 {
		t.Fatalf("esc must not clear the highlight: %v", e.Matches(0, 0))
	}
	feed(t, e, ":noh<cr>")
	if got := e.Matches(0, 0); got != nil {
		t.Errorf(":noh left %v", got)
	}
	feed(t, e, "n")
	if len(e.Matches(0, 0)) != 2 {
		t.Errorf("the next search brings it back: %v", e.Matches(0, 0))
	}
	feed(t, e, ":nohlsearch<cr>")
	if e.Matches(0, 0) != nil {
		t.Errorf(":nohlsearch left matches")
	}
	feed(t, e, "*")
	if len(e.Matches(0, 0)) != 2 {
		t.Errorf("* highlights the word: %v", e.Matches(0, 0))
	}
	if e.Status() != "" && e.Status() != "search wrapped" {
		t.Errorf("status %q", e.Status())
	}
}

func TestNohIsNotAnError(t *testing.T) {
	e := normalAt(t, "|a")
	feed(t, e, ":noh<cr>")
	if e.Status() != "" {
		t.Errorf("status = %q", e.Status())
	}
}

func TestWholeWordMatchesHighlightOnlyWords(t *testing.T) {
	e := normalAt(t, "|cat concat cat")
	feed(t, e, "*")
	if got := e.Matches(0, 0); !reflect.DeepEqual(got, []Match{{Row: 0, Start: 0, End: 3}, {Row: 0, Start: 11, End: 14}}) {
		t.Errorf("* matches whole words only: %v", got)
	}
}

func TestMatchesLeaveTheVisualSelectionAlone(t *testing.T) {
	e := normalAt(t, "|ab ab ab")
	feed(t, e, "/ab<cr>")
	feed(t, e, "0vee")
	if _, _, ok := e.Selection(); !ok {
		t.Fatal("no selection")
	}
	// The selection runs from column 0 to 4, and the middle match is inside it.
	got := e.Matches(0, 0)
	want := []Match{{Row: 0, Start: 6, End: 8}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("matches with a selection = %v, want %v", got, want)
	}
}

func TestIncsearchTargetFollowsTheTypedPattern(t *testing.T) {
	e := normalAt(t, "one\n|two\nthree two\nfour")
	if _, ok := e.IncsearchTarget(); ok {
		t.Fatal("no target outside the prompt")
	}
	feed(t, e, "/t")
	if at, ok := e.IncsearchTarget(); !ok || at != (Pos{2, 0}) {
		t.Errorf("first t after the cursor = %v %v", at, ok)
	}
	feed(t, e, "w")
	if at, ok := e.IncsearchTarget(); !ok || at != (Pos{2, 6}) {
		t.Errorf("tw = %v %v", at, ok)
	}
	if row, col := e.Cursor(); row != 1 || col != 0 {
		t.Errorf("the cursor moved to %d,%d while typing", row, col)
	}
	want := []Match{{Row: 1, Start: 0, End: 2}, {Row: 2, Start: 6, End: 8, Current: true}}
	if got := e.Matches(0, 3); !reflect.DeepEqual(got, want) {
		t.Errorf("live matches = %v, want %v", got, want)
	}
	feed(t, e, "x")
	if _, ok := e.IncsearchTarget(); ok {
		t.Error("twx matches nothing")
	}
}

func TestIncsearchBackwardAndEscRestores(t *testing.T) {
	e := normalAt(t, "ab\ncd\n|ef ab")
	feed(t, e, "?ab")
	if at, ok := e.IncsearchTarget(); !ok || at != (Pos{0, 0}) {
		t.Errorf("backward target = %v %v", at, ok)
	}
	feed(t, e, "<esc>")
	if got := show(e); got != "ab\ncd\n|ef ab" {
		t.Errorf("esc must leave the cursor: %q", got)
	}
	if _, ok := e.IncsearchTarget(); ok {
		t.Error("target after esc")
	}
	if e.Matches(0, 2) != nil {
		t.Errorf("a cancelled search leaves no highlight: %v", e.Matches(0, 2))
	}
}

func TestIncsearchKeepsEarlierHighlightWhileTheLineIsEmpty(t *testing.T) {
	e := normalAt(t, "|a b a")
	feed(t, e, "/a<cr>/")
	if len(e.Matches(0, 0)) != 2 {
		t.Errorf("empty prompt: %v", e.Matches(0, 0))
	}
}
