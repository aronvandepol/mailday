package vimedit

import (
	"strings"
	"testing"
)

func TestSearchMotion(t *testing.T) {
	runCases(t, []editCase{
		{"forward finds the next match", "|one two one", "/one<cr>", "one two |one"},
		{"forward skips the match under the cursor", "|foo foo", "/foo<cr>", "foo |foo"},
		{"forward goes down lines", "|a\nb\nfoo", "/foo<cr>", "a\nb\n|foo"},
		{"backward", "foo bar foo|", "?foo<cr>", "foo bar |foo"},
		{"backward goes up lines", "foo\nb\n|c", "?foo<cr>", "|foo\nb\nc"},
		{"backward skips the match under the cursor", "foo |foo", "?foo<cr>", "|foo foo"},
		{"lower case ignores case", "|a Foo", "/foo<cr>", "a |Foo"},
		{"smartcase: an upper-case letter makes it exact", "|a foo Foo", "/Foo<cr>", "a foo |Foo"},
		{"smartcase: no match for the wrong case", "|a foo", "/Foo<cr>", "|a foo"},
		{"wraps forward", "foo\nbar\n|baz", "/foo<cr>", "|foo\nbar\nbaz"},
		{"wraps backward", "|foo\nbar\nbaz", "?baz<cr>", "foo\nbar\n|baz"},
		{"a lone match wraps onto itself", "a |b c", "/b<cr>", "a |b c"},
		{"not found leaves the cursor", "|abc", "/zzz<cr>", "|abc"},
		{"count", "|x x x x", "3/x<cr>", "x x x |x"},
		{"multibyte columns", "|가나다 라마 다", "/다<cr>", "가나|다 라마 다"},
		{"n repeats", "|x x x", "/x<cr>n", "x x |x"},
		{"N goes the other way", "x x |x", "?x<cr>N", "x x |x"},
		{"N after a forward search goes back", "|x x x", "/x<cr>/x<cr>N", "x |x x"},
		{"count n", "|x x x x", "/x<cr>2n", "x x x |x"},
		{"empty pattern reuses the last", "|x x x", "/x<cr>/<cr>", "x x |x"},
		{"empty ? reuses the pattern backwards", "x |x x x", "/x<cr>?<cr>", "x |x x x"},
		{"n without a search does nothing", "|a a", "n", "|a a"},
		{"esc leaves the prompt", "|a b", "/b<esc>", "|a b"},
		{"backspace on an empty prompt leaves it", "|a b", "/<bs>l", "a| b"},
		{"backspace edits the pattern", "|a b c", "/bx<bs><cr>", "a |b c"},
		{"a slash after f is a character", "|a/b", "f/", "a|/b"},
	})
}

func TestSearchWord(t *testing.T) {
	runCases(t, []editCase{
		{"* finds the next whole word", "|foo foobar foo", "*", "foo foobar |foo"},
		{"* from the middle of a word", "fo|o foobar foo", "*", "foo foobar |foo"},
		{"* wraps", "foo bar |foo", "*", "|foo bar foo"},
		{"# goes back", "foo bar |foo", "#", "|foo bar foo"},
		{"# from the middle of a word", "foo bar fo|o", "#", "|foo bar foo"},
		{"# wraps", "|foo bar foo", "#", "foo bar |foo"},
		{"* skips to the next word when on punctuation", "|-- foo x foo", "*", "-- foo x |foo"},
		{"* then n", "|a x a x a", "*n", "a x a x |a"},
		{"* then N", "|a x a x a", "**N", "a x |a x a"},
		{"* is whole-word: substrings do not match", "|a ab a", "*", "a ab |a"},
		{"* with a count", "|a a a a", "2*", "a a |a a"},
		{"* on a line with no word does nothing", "|--", "*", "|--"},
		{"* is case-sensitive when the word has capitals", "|Foo foo Foo", "*", "Foo foo |Foo"},
	})
}

func TestSearchAsMotion(t *testing.T) {
	runCases(t, []editCase{
		{"d/foo is exclusive", "|a b foo c", "d/foo<cr>", "|foo c"},
		{"d? goes back", "foo a b |c", "d?foo<cr>", "|c"},
		{"y/ then p", "|ab cd ef", "y/ef<cr>$p", "ab cd efab cd| "},
		{"c/ changes up to the match", "|a b foo", "c/foo<cr>X<esc>", "|Xfoo"},
		{"dn", "|a x b x c", "/x<cr>0dn", "|x b x c"},
		{"dn after ?", "a x b x |c", "?x<cr>$dn", "a x b |c"},
		{"d* deletes up to the next word", "|foo bar foo", "d*", "|foo"},
		{"d/ not found changes nothing", "|a b", "d/zzz<cr>", "|a b"},
		{"d/ across lines", "|a\nb\nfoo", "d/foo<cr>", "|foo"},
		{"d/ esc cancels the operator", "|abc", "d/b<esc>x", "|bc"},
		{"> over a search", "|a\nb\nfoo", ">/foo<cr>", "  |a\n  b\nfoo"},
		{"dot repeats the operator and search", "|a x b x c x d", "d/x<cr>.", "|x c x d"},
		{"dot with n", "|a x b x c x d", "/x<cr>0dn.", "|x c x d"},
	})
}

func TestSearchInVisualMode(t *testing.T) {
	e := normalAt(t, "|one two three")
	feed(t, e, "v/thr<cr>")
	if e.Mode() != Visual {
		t.Fatalf("mode %v, want VISUAL", e.Mode())
	}
	feed(t, e, "d")
	if got := e.Text(); got != "hree" {
		t.Errorf("text %q, want %q", got, "hree")
	}
}

func TestSearchPrompt(t *testing.T) {
	e := normalAt(t, "|a b")
	feed(t, e, "/b")
	if e.Mode() != Command || e.CommandPrefix() != "/" || e.CommandLine() != "b" {
		t.Errorf("mode %v prefix %q line %q", e.Mode(), e.CommandPrefix(), e.CommandLine())
	}
	feed(t, e, "<esc>?x")
	if e.CommandPrefix() != "?" {
		t.Errorf("prefix %q, want ?", e.CommandPrefix())
	}
	feed(t, e, "<cr>")
	if e.Mode() != Normal || e.CommandPrefix() != "" {
		t.Errorf("after enter: mode %v prefix %q", e.Mode(), e.CommandPrefix())
	}
	feed(t, e, ":")
	if e.CommandPrefix() != ":" {
		t.Errorf("prefix %q, want :", e.CommandPrefix())
	}
}

func TestSearchStatus(t *testing.T) {
	for _, c := range []struct{ name, in, keys, want string }{
		{"wrapped forward", "x|\ny", "/x<cr>", "search wrapped"},
		{"wrapped backward", "|x\ny", "?y<cr>", "search wrapped"},
		{"wrapped by n", "x\n|y x", "/x<cr>n", "search wrapped"},
		{"not found", "|abc", "/zzz<cr>", "Pattern not found: zzz"},
		{"not wrapped", "|x\ny\nx", "/x<cr>", ""},
		{"n with nothing before", "|abc", "n", "No previous search"},
		{"* with no word", "|--", "*", "No string under cursor"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := normalAt(t, c.in)
			feed(t, e, c.keys)
			if got := e.Status(); got != c.want {
				t.Errorf("status %q, want %q", got, c.want)
			}
		})
	}
}

func TestSearchKeepsUndoAndRegisters(t *testing.T) {
	e := normalAt(t, "|a foo b")
	feed(t, e, "/foo<cr>")
	if strings.Contains(e.Status(), "oldest") || len(e.undo) != 0 {
		t.Errorf("a search left an undo step: %d", len(e.undo))
	}
	feed(t, e, "d/b<cr>u")
	if got := e.Text(); got != "a foo b" {
		t.Errorf("undo gave %q", got)
	}
}
