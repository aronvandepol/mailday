package vimedit

import (
	"strings"
	"testing"
)

func wrappedAt(t *testing.T, text string, width int) *Editor {
	t.Helper()
	e := normalAt(t, text)
	e.SetWrapWidth(width)
	return e
}

func TestWrapLine(t *testing.T) {
	rows := func(text string, width int) []string {
		var out []string
		for _, segment := range WrapLine([]rune(text), width) {
			out = append(out, string(segment))
		}
		return out
	}
	cases := []struct {
		text  string
		width int
		want  []string
	}{
		{"aaaa bbbb cccc", 10, []string{"aaaa bbbb ", "cccc "}},
		{"", 10, []string{" "}},
		{"short", 10, []string{"short "}},
		{"한국어 텍스트", 8, []string{"한국어 ", "텍스트 "}},
		{"abcdefghijkl", 5, []string{"abcde", "fghij", "kl "}},
	}
	for _, c := range cases {
		if got := rows(c.text, c.width); strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("WrapLine(%q, %d) = %q, want %q", c.text, c.width, got, c.want)
		}
	}
}

func TestGjGk(t *testing.T) {
	cases := []struct {
		name, in, keys, want string
		width                int
	}{
		{"gj goes to the next screen line of the same row", "aaaa b|bbb cccc dddd", "gj", "aaaa bbbb cccc d|ddd", 10},
		{"gj keeps the display column", "|aaaa bbbb cccc dddd", "gj", "aaaa bbbb |cccc dddd", 10},
		{"gk goes back up the same row", "aaaa bbbb c|ccc dddd", "gk", "a|aaa bbbb cccc dddd", 10},
		{"gj on the last screen line goes to the next row", "aaaa bbbb c|ccc\nxy", "gj", "aaaa bbbb cccc\nx|y", 10},
		{"gj lands on the last character of a short line", "aaaa bbbb c|ccc\nx", "gj", "aaaa bbbb cccc\n|x", 10},
		{"gk from the next row enters its last screen line", "aaaa bbbb cccc\nx|y", "gk", "aaaa bbbb c|ccc\nxy", 10},
		{"3gj", "|a1 b1 c1 d1 e1 f1 g1 h1", "3gj", "a1 b1 c1 d1 e1 f1 |g1 h1", 6},
		{"j still moves by line", "|aaaa bbbb cccc dddd\nxyz", "j", "aaaa bbbb cccc dddd\n|xyz", 10},
		{"gj without a width is j", "|aaaa bbbb cccc dddd\nxyz", "gj", "aaaa bbbb cccc dddd\n|xyz", 0},
		{"gj at the end of the text stays", "aaaa bbbb c|ccc", "gj", "aaaa bbbb c|ccc", 10},
		{"gk at the start stays", "aa|aa", "gk", "aa|aa", 10},
		{"gj keeps the column over a short screen line", "aaaaaa|aaa\nx\nbbbbbbbbbb", "gjgj", "aaaaaaaaa\nx\nbbbbbb|bbbb", 20},
		{"Korean display column: wide characters count two cells", "|한국어 텍스트 끝", "gj", "한국어 |텍스트 끝", 8},
		{"Korean landing mid-character picks the character covering the cell", "한|국어 가나다라 끝", "gj", "한국어 가|나다라 끝", 8},
		{"d gj deletes to the next screen line", "|aaaa bbbb cccc", "dgj", "|cccc", 10},
		{"g0 and g$ stay on the screen line", "aaaa bbbb c|ccc dddd", "g0", "aaaa bbbb |cccc dddd", 10},
		{"g$ goes to the last character of the screen line", "|aaaa bbbb cccc", "g$", "aaaa bbbb| cccc", 10},
		{"visual gj extends", "|aaaa bbbb cccc", "vgjd", "|ccc", 10},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := wrappedAt(t, c.in, c.width)
			feed(t, e, c.keys)
			if got := show(e); got != c.want {
				t.Errorf("%q then %q at width %d\n got %q\nwant %q", c.in, c.keys, c.width, got, c.want)
			}
		})
	}
}

func TestGjWantedColumnResetsOnOtherKeys(t *testing.T) {
	e := wrappedAt(t, "|aaaa bbbb cccc dddd", 10)
	feed(t, e, "gjgk")
	if got := show(e); got != "|aaaa bbbb cccc dddd" {
		t.Errorf("gj then gk = %q", got)
	}
	feed(t, e, "gjlgk")
	if got := show(e); got != "a|aaa bbbb cccc dddd" {
		t.Errorf("a key between resets the wanted column: %q", got)
	}
}
