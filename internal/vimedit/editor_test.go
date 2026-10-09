package vimedit

import (
	"strings"
	"testing"
	"time"
)

// named keys in a test's key string: <esc>, <cr>, <c-r> and so on.
var namedKeys = map[string][2]string{
	"esc": {"esc", ""}, "cr": {"enter", ""}, "bs": {"backspace", ""}, "tab": {"tab", ""},
	"del": {"delete", ""}, "c-r": {"ctrl+r", ""}, "c-w": {"ctrl+w", ""}, "c-u": {"ctrl+u", ""},
	"left": {"left", ""}, "right": {"right", ""}, "up": {"up", ""}, "down": {"down", ""},
	"home": {"home", ""}, "end": {"end", ""}, "sp": {"space", " "}, "lt": {"<", "<"},
}

// feed plays a key string into e and returns the actions it asked for.
func feed(t *testing.T, e *Editor, keys string) []Action {
	t.Helper()
	var actions []Action
	send := func(key, text string) {
		if result := e.Key(key, text); result.Action != None {
			actions = append(actions, result.Action)
		}
	}
	for rest := keys; rest != ""; {
		if strings.HasPrefix(rest, "<") {
			if end := strings.Index(rest, ">"); end > 0 {
				if named, ok := namedKeys[rest[1:end]]; ok {
					send(named[0], named[1])
					rest = rest[end+1:]
					continue
				}
			}
		}
		r := []rune(rest)[0]
		key := string(r)
		if r == ' ' {
			key = "space"
		}
		send(key, string(r))
		rest = rest[len(string(r)):]
	}
	return actions
}

// marked is text with | where the cursor is, before the character it is on.
func marked(text string, row, col int) string {
	lines := strings.Split(text, "\n")
	runes := []rune(lines[row])
	lines[row] = string(runes[:col]) + "|" + string(runes[col:])
	return strings.Join(lines, "\n")
}

func normalAt(t *testing.T, text string) *Editor {
	t.Helper()
	index := strings.Index(text, "|")
	if index < 0 {
		t.Fatalf("no cursor marker in %q", text)
	}
	before := text[:index]
	row := strings.Count(before, "\n")
	col := len([]rune(before[strings.LastIndex(before, "\n")+1:]))
	e := New(strings.Replace(text, "|", "", 1))
	e.mode = Normal
	e.SetCursor(row, col)
	return e
}

func show(e *Editor) string {
	row, col := e.Cursor()
	return marked(e.Text(), row, col)
}

type editCase struct {
	name, in, keys, want string
}

func runCases(t *testing.T, cases []editCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := normalAt(t, c.in)
			feed(t, e, c.keys)
			if got := show(e); got != c.want {
				t.Errorf("%q then %q\n got %q\nwant %q (mode %v)", c.in, c.keys, got, c.want, e.Mode())
			}
		})
	}
}

func TestMotions(t *testing.T) {
	runCases(t, []editCase{
		{"h", "ab|c", "h", "a|bc"},
		{"h at line start stays", "|abc", "h", "|abc"},
		{"h count", "abcd|e", "3h", "a|bcde"},
		{"h count too big", "ab|c", "9h", "|abc"},
		{"l", "|abc", "l", "a|bc"},
		{"l stops on last char", "ab|c", "l", "ab|c"},
		{"l count clamps", "|abc", "9l", "ab|c"},
		{"left arrow", "a|bc", "<left>", "|abc"},
		{"right arrow", "a|bc", "<right>", "ab|c"},
		{"space is l", "|abc", "<sp>", "a|bc"},
		{"backspace is h", "a|bc", "<bs>", "|abc"},
		{"w", "|foo bar baz", "w", "foo |bar baz"},
		{"2w", "|foo bar baz", "2w", "foo bar |baz"},
		{"w stops at the last char", "|foo bar baz", "9w", "foo bar ba|z"},
		{"w punctuation is a word", "|foo.bar baz", "w", "foo|.bar baz"},
		{"W skips punctuation", "|foo.bar baz", "W", "foo.bar |baz"},
		{"w crosses lines", "foo|\nbar", "w", "foo\n|bar"},
		{"w stops on an empty line", "|a\n\nb", "w", "a\n|\nb"},
		{"w from an empty line", "a\n|\nb", "w", "a\n\n|b"},
		{"b crosses blanks and a line", "foo\n   |bar", "b", "|foo\n   bar"},
		{"w skips blanks", "|foo    bar", "w", "foo    |bar"},
		{"w Korean words", "|한국어 텍스트 끝", "w", "한국어 |텍스트 끝"},
		{"w between scripts", "|abc한국 def", "w", "abc|한국 def"},
		{"b", "foo bar |baz", "b", "foo |bar baz"},
		{"bb", "foo bar |baz", "bb", "|foo bar baz"},
		{"b stops at the start", "|foo", "b", "|foo"},
		{"b to the previous line", "foo\n|bar", "b", "|foo\nbar"},
		{"b mid word", "foo ba|r", "b", "foo |bar"},
		{"B", "foo.bar |baz", "B", "|foo.bar baz"},
		{"b stops on an empty line", "a\n\n|b", "b", "a\n|\nb"},
		{"e", "|foo bar baz", "e", "fo|o bar baz"},
		{"ee", "|foo bar baz", "ee", "foo ba|r baz"},
		{"3e", "|foo bar baz", "3e", "foo bar ba|z"},
		{"e skips empty lines", "fo|o\n\nbar", "e", "foo\n\nba|r"},
		{"E", "|foo.bar baz", "E", "foo.ba|r baz"},
		{"0", "  fo|o", "0", "|  foo"},
		{"^", "  fo|o", "^", "  |foo"},
		{"$", "|  foo", "$", "  fo|o"},
		{"$ on an empty line", "|", "$", "|"},
		{"home end", "a|bc", "<end><home>", "|abc"},
		{"j keeps the column", "ab|cd\nxy\nabcd", "jj", "abcd\nxy\nab|cd"},
		{"j lands on the end of a short line", "ab|cd\nx", "j", "abcd\n|x"},
		{"k", "abcd\nab|cd", "k", "ab|cd\nabcd"},
		{"j at last line stays", "a\n|b", "j", "a\n|b"},
		{"3j", "|a\nb\nc\nd\ne", "3j", "a\nb\nc\n|d\ne"},
		{"j count past the end", "|a\nb", "9j", "a\n|b"},
		{"$ keeps the end for j", "|ab\nabcd\nx", "$jj", "ab\nabcd\n|x"},
		{"down arrow", "|a\nb", "<down>", "a\n|b"},
		{"up arrow", "a\n|b", "<up>", "|a\nb"},
		{"G", "|a\n  b\nc", "G", "a\n  b\n|c"},
		{"G first non-blank", "|a\nb\n  c", "G", "a\nb\n  |c"},
		{"gg", "a\nb\n|c", "gg", "|a\nb\nc"},
		{"3G", "|a\nb\nc\nd", "3G", "a\nb\n|c\nd"},
		{"2gg", "a\nb\n|c\nd", "2gg", "a\n|b\nc\nd"},
		{"9G clamps", "|a\nb", "9G", "a\n|b"},
		{"}", "|a\nb\n\nc\nd\n\ne", "}", "a\nb\n|\nc\nd\n\ne"},
		{"}}", "|a\nb\n\nc\nd\n\ne", "}}", "a\nb\n\nc\nd\n|\ne"},
		{"}} to the end", "|a\n\nb", "}}", "a\n\n|b"},
		{"2}", "|a\n\nb\n\nc", "2}", "a\n\nb\n|\nc"},
		{"{", "a\n\nb\n|c", "{", "a\n|\nb\nc"},
		{"{ to the top", "a\n|b", "{", "|a\nb"},
		{"f", "|a,b,c,d", "f,", "a|,b,c,d"},
		{"2f", "|a,b,c,d", "2f,", "a,b|,c,d"},
		{"f not found", "|abc", "fz", "|abc"},
		{"F", "a,b,c|", "F,", "a,b|,c"},
		{"t", "|a,b", "t,", "|a,b"},
		{"t further", "|ab,cd", "t,", "a|b,cd"},
		{"T", "ab,cd|", "T,", "ab,|cd"},
		{"; repeats f", "|a,b,c,d", "f,;", "a,b|,c,d"},
		{"; repeats t past the neighbour", "|ab,cd,ef", "t,;", "ab,c|d,ef"},
		{", reverses", "|a,b,c,d", "f,f,,", "a|,b,c,d"},
		{"; with count", "|a,b,c,d", "f,2;", "a,b,c|,d"},
		{"f Korean", "|한,국,어", "f,", "한|,국,어"},
		{"; before any find", "|a,b", ";", "|a,b"},
	})
}

func TestEditing(t *testing.T) {
	runCases(t, []editCase{
		{"x", "|abc", "x", "|bc"},
		{"3x", "|abcd", "3x", "|d"},
		{"x count past the end", "a|bcd", "9x", "|a"},
		{"x on last char", "ab|c", "x", "a|b"},
		{"x on an empty line", "|", "x", "|"},
		{"x Korean", "|한국어", "x", "|국어"},
		{"x Korean last", "한국|어", "x", "한|국"},
		{"X", "ab|c", "X", "a|c"},
		{"X at line start", "|abc", "X", "|abc"},
		{"2X", "abc|d", "2X", "a|d"},
		{"r", "|abc", "rx", "|xbc"},
		{"3r", "|abc", "3rx", "xx|x"},
		{"r too many", "|abc", "5rx", "|abc"},
		{"r Korean", "|abc", "r한", "|한bc"},
		{"r esc cancels", "|abc", "r<esc>", "|abc"},
		{"s", "a|bc", "sX<esc>", "a|Xc"},
		{"2s", "|abcd", "2sX<esc>", "|Xcd"},
		{"S", "  a|b\nc", "SX<esc>", "|X\nc"},
		{"C", "ab|cd", "CX<esc>", "ab|X"},
		{"D", "ab|cd", "D", "a|b"},
		{"2D", "ab|cd\nef\ngh", "2D", "a|b\ngh"},
		{"~", "|abC", "~", "A|bC"},
		{"3~", "|abC", "3~", "AB|c"},
		{"~ at end", "ab|c", "~", "ab|C"},
		{"~ Korean is unchanged", "|한a", "~~", "한|A"},
		{"J", "a|\n  b\nc", "J", "a| b\nc"},
		{"3J", "a|\nb\nc\nd", "3J", "a b| c\nd"},
		{"J on the last line", "a\n|b", "J", "a\n|b"},
		{"J with an empty line", "a|\n\nb", "J", "|a\nb"},
		{"J before a closing bracket", "f(a|\n)", "J", "f(a|)"},
		{"dd", "a\n|b\nc", "dd", "a\n|c"},
		{"dd last line", "a\n|b", "dd", "|a"},
		{"dd only line", "|a", "dd", "|"},
		{"2dd", "|a\nb\nc", "2dd", "|c"},
		{"dd count past the end", "a\n|b\nc", "9dd", "|a"},
		{"d2d", "|a\nb\nc", "d2d", "|c"},
		{"dd keeps indent of the next line", "a\n|b\n  c", "dd", "a\n  |c"},
		{"yy p", "|a\nb", "yyp", "a\n|a\nb"},
		{"yy P", "a\n|b", "yyP", "a\n|b\nb"},
		{"2yy p", "|a\nb\nc", "2yyp", "a\n|a\nb\nb\nc"},
		{"yy 3p", "|a\nb", "yy3p", "a\n|a\na\na\nb"},
		{"yy j p", "|a\nb", "yyjp", "a\nb\n|a"},
		{"Y", "|a\nb", "Yp", "a\n|a\nb"},
		{"p on an empty register", "|abc", "p", "|abc"},
		{"dw p", "|foo bar", "dwp", "bfoo| ar"},
		{"yw $p", "|foo bar", "yw$p", "foo barfoo| "},
		{"yw P", "foo |bar", "ywP", "foo ba|rbar"},
		{"x p swaps", "|ab", "xp", "b|a"},
		{"3p charwise", "|a", "yl3p", "aaa|a"},
		{"p multi-line charwise", "|ab\ncd", "vjy$p", "ab|ab\nc\ncd"},
		{"dw", "|foo bar", "dw", "|bar"},
		{"d2w", "|a b c", "d2w", "|c"},
		{"2dw", "|a b c", "2dw", "|c"},
		{"dw on the last word", "foo |bar", "dw", "foo| "},
		{"dw stops at the line end", "foo |bar\nbaz", "dw", "foo| \nbaz"},
		{"dw on the last word of the buffer", "|foo", "dw", "|"},
		{"dW", "|foo.bar baz", "dW", "|baz"},
		{"de", "|foo bar", "de", "| bar"},
		{"db", "foo |bar", "db", "|bar"},
		{"dB", "a.b |c", "dB", "|c"},
		{"db at a line start deletes the line", "foo\n|bar", "db", "|bar"},
		{"d$", "ab|cd", "d$", "a|b"},
		{"d0", "foo b|ar", "d0", "|ar"},
		{"d^", "  fo|o", "d^", "  |o"},
		{"dh", "ab|c", "dh", "a|c"},
		{"dl", "|abc", "dl", "|bc"},
		{"d3l", "|abcd", "d3l", "|d"},
		{"dl on the last char", "ab|c", "dl", "a|b"},
		{"dj", "a\n|b\nc\nd", "dj", "a\n|d"},
		{"dk", "a\nb\n|c\nd", "dk", "a\n|d"},
		{"dG", "a\n|b\nc", "dG", "|a"},
		{"dgg", "a\nb\n|c", "dgg", "|"},
		{"d2j", "|a\nb\nc\nd", "d2j", "|d"},
		{"dj on the last line fails", "a\n|b", "dj", "a\n|b"},
		{"d}", "|a\nb\n\nc", "d}", "|\nc"},
		{"d} from mid line", "a|b\nc\n\nd", "d}", "|a\n\nd"},
		{"d{", "a\n\nb\n|c", "d{", "a\n|c"},
		{"df", "|a,b,c", "df,", "|b,c"},
		{"dt", "|a,b,c", "dt,", "|,b,c"},
		{"dF", "a,b|,c", "dF,", "a|,c"},
		{"d;", "|a,b,c", "f,d;", "a|c"},
		{"dd Korean", "한국\n|어\n끝", "dd", "한국\n|끝"},
		{"cw", "|foo bar", "cwX<esc>", "|X bar"},
		{"cw mid word", "f|oo bar", "cwX<esc>", "f|X bar"},
		{"c2w", "|a b c", "c2wX<esc>", "|X c"},
		{"cW", "|foo.bar baz", "cWX<esc>", "|X baz"},
		{"cw on a blank", "foo| bar", "cwX<esc>", "foo|Xbar"},
		{"ce", "|foo bar", "ceX<esc>", "|X bar"},
		{"cb", "foo |bar", "cbX<esc>", "|Xbar"},
		{"cc", "a\n  |b\nc", "ccX<esc>", "a\n|X\nc"},
		{"2cc", "|a\nb\nc", "2ccX<esc>", "|X\nc"},
		{"c$", "ab|cd", "c$X<esc>", "ab|X"},
		{"cj", "|a\nb\nc", "cjX<esc>", "|X\nc"},
		{"yw does not change text", "|foo bar", "yw", "|foo bar"},
		{"yb moves the cursor", "foo |bar", "yb", "|foo bar"},
		{"yy keeps the cursor", "a|b\nc", "yy", "a|b\nc"},
		{"yj stays", "a|b\nc", "yj", "a|b\nc"},
		{"yk moves to the top line", "ab\nc|d", "yk", "a|b\ncd"},
		{">>", "|a", ">>", "  |a"},
		{"3>>", "|a\nb\nc\nd", "3>>", "  |a\n  b\n  c\nd"},
		{">> keeps empty lines empty", "|a\n\nb", "3>>", "  |a\n\n  b"},
		{"<<", "  |a", "<<", "|a"},
		{"<< with one space", " |a", "<<", "|a"},
		{"<< strips two of four", "    |a", "<<", "  |a"},
		{"<< on an unindented line", "|a", "<<", "|a"},
		{">j", "|a\nb\nc", ">j", "  |a\n  b\nc"},
		{">ip", "|a\nb\n\nc", ">ip", "  |a\n  b\n\nc"},
		{"<ip", "  |a\n  b\n\nc", "<ip", "|a\nb\n\nc"},
		{">G", "a\n|b\nc", ">G", "a\n  |b\n  c"},
		{"dd then p keeps the register", "a\n|b\nc", "ddp", "a\nc\n|b"},
		{"named register", "|a\nb", `"ayyj"ap`, "a\nb\n|a"},
		{"named register keeps unnamed apart", "|a\nb", `"ayyjyy"ap`, "a\nb\n|a"},
		{"black hole register", "|a\nb", `yyj"_ddp`, "a\n|a"},
		{"unnamed after named delete", "|a\nb", `"add"ap`, "b\n|a"},
	})
}

func TestTextObjects(t *testing.T) {
	runCases(t, []editCase{
		{"diw", "foo b|ar baz", "diw", "foo | baz"},
		{"diw at the line start", "|foo bar", "diw", "| bar"},
		{"diw at the line end", "foo ba|r", "diw", "foo| "},
		{"diw on blanks", "foo|  bar", "diw", "foo|bar"},
		{"diw on punctuation", "fo|o.bar", "diw", "|.bar"},
		{"diw on the dot", "foo|.bar", "diw", "foo|bar"},
		{"d2iw", "|foo bar baz", "d2iw", "|bar baz"},
		{"d3iw", "|foo bar baz", "d3iw", "| baz"},
		{"diw on an empty line", "a\n|\nb", "diw", "a\n|\nb"},
		{"ciw on an empty line", "a\n|\nb", "ciwX<esc>", "a\n|X\nb"},
		{"diw Korean", "한국 |텍스트 끝", "diw", "한국 | 끝"},
		{"ciw single char line", "|a", "ciwX<esc>", "|X"},
		{"diW", "a fo|o.bar b", "diW", "a | b"},
		{"daw", "foo b|ar baz", "daw", "foo |baz"},
		{"daw at the line end takes the blank before", "foo ba|r", "daw", "fo|o"},
		{"daw at the line start", "|foo bar", "daw", "|bar"},
		{"daw on blanks takes the word after", "foo| bar baz", "daw", "foo| baz"},
		{"daw only word", "|foo", "daw", "|"},
		{"d2aw", "|a b c d", "d2aw", "|c d"},
		{"daW", "x fo|o.bar y", "daW", "x |y"},
		{"yiw P", "fo|o bar", "yiwP", "fo|ofoo bar"},
		{"ciw", "foo b|ar baz", "ciwX<esc>", "foo |X baz"},
		{`di"`, `say "he|llo" now`, `di"`, `say "|" now`},
		{`di" on the opening quote`, `say |"hello" now`, `di"`, `say "|" now`},
		{`di" on the closing quote`, `say "hello|" now`, `di"`, `say "|" now`},
		{`di" before the quotes`, `|say "hello" now`, `di"`, `say "|" now`},
		{`di" after the quotes`, `say "hello" n|ow`, `di"`, `say "hello" n|ow`},
		{`di" in the gap picks the next`, `"a" |x "b"`, `di"`, `"a" x "|"`},
		{`di" empty`, `a "|" b`, `di"`, `a "|" b`},
		{`ci" empty`, `a "|" b`, `ci"X<esc>`, `a "|X" b`},
		{`da"`, `say "he|llo" now`, `da"`, `say |now`},
		{`da" at the end takes the blank before`, `say "he|llo"`, `da"`, `sa|y`},
		{`ci"`, `say "he|llo" now`, `ci"X<esc>`, `say "|X" now`},
		{`ya" then p`, `a "b|c" d`, `ya"$p`, `a "bc" d"bc"| `},
		{"escaped quote is skipped", `"a\"b|" c`, `di"`, `"|" c`},
		{"di'", "it 'i|s' ok", "di'", "it '|' ok"},
		{"di`", "x `co|de` y", "di`", "x `|` y"},
		{"di(", "f(a, |b)", "di(", "f(|)"},
		{"da(", "f(a, |b)", "da(", "|f"},
		{"dib", "f(a, |b)", "dib", "f(|)"},
		{"di( on the opening bracket", "f|(a, b)", "di(", "f(|)"},
		{"di( on the closing bracket", "f(a, b|)", "di(", "f(|)"},
		{"di( nested", "(a (b| c) d)", "di(", "(a (|) d)"},
		{"di( takes the pair the cursor is in", "(|a (b c) d)", "di(", "(|)"},
		{"di( outside brackets", "|a (b)", "di(", "|a (b)"},
		{"ci(", "f(a|b)", "ci(X<esc>", "f(|X)"},
		{"di)", "f(a|b)", "di)", "f(|)"},
		{"di[", "x[1, |2]", "di[", "x[|]"},
		{"da[", "x[1, |2]", "da[", "|x"},
		{"di{", "x{a|b}", "di{", "x{|}"},
		{"diB", "x{a|b}", "diB", "x{|}"},
		{"da{", "x {a|b}", "da{", "x| "},
		{"di<", "<a|b>", "di<", "<|>"},
		{"di{ across lines keeps the bracket lines", "if {\n  |a\n  b\n}", "di{", "if {\n|}"},
		{"da{ across lines", "if {\n  |a\n}", "da{", "if| "},
		{"ci{ across lines", "if {\n  |a\n  b\n}", "ci{X<esc>", "if {\n|X\n}"},
		{"di( multi-line text inside a line", "f(a,\n  |b)", "di(", "f(|)"},
		{"di{ with the block on one line break", "x {\n|}", "di{", "x {\n|}"},
		{"dip", "a\nb\n|c\n\nd", "dip", "|\nd"},
		{"dip on blank lines", "a\n|\n\nb", "dip", "a\n|b"},
		{"dip at the buffer start", "|a\nb\n\nc", "dip", "|\nc"},
		{"dip at the buffer end", "a\n\n|b\nc", "dip", "a\n|"},
		{"d2ip", "|a\n\nb", "d2ip", "|b"},
		{"dap", "a\nb\n|c\n\nd", "dap", "|d"},
		{"dap middle", "x\n\na\n|b\n\ny", "dap", "x\n\n|y"},
		{"dap last paragraph takes the blanks before", "x\n\na\n|b", "dap", "|x"},
		{"dap on blanks takes the next paragraph", "x\n|\na\nb\n\ny", "dap", "x\n|\ny"},
		{"yip", "|a\nb\n\nc", "yipGp", "a\nb\n\nc\n|a\nb"},
		{"cip", "a\n|b\n\nc", "cipX<esc>", "|X\n\nc"},
	})
}

func TestVisual(t *testing.T) {
	runCases(t, []editCase{
		{"vld", "|foo bar", "vld", "|o bar"},
		{"vwd takes the first letter of the next word", "|foo bar", "vwd", "|ar"},
		{"vd one char", "a|bc", "vd", "a|c"},
		{"vx", "|abc", "vlx", "|c"},
		{"v$d", "a|bc", "v$d", "|a"},
		{"vy then p", "|ab", "vly$p", "aba|b"},
		{"vy puts the cursor at the start", "ab|cd", "vhy", "a|bcd"},
		{"vc", "|foo bar", "vecX<esc>", "|X bar"},
		{"v across lines", "ab|c\ndef", "vjd", "a|b"},
		{"v o swaps the ends", "ab|cdef", "vlohd", "a|ef"},
		{"v esc cancels", "|abc", "vl<esc>", "a|bc"},
		{"v v cancels", "|abc", "vlv", "a|bc"},
		{"v ~", "|abc", "vl~", "|ABc"},
		{"v u", "|ABC", "vlu", "|abC"},
		{"v U", "|abc", "vlU", "|ABc"},
		{"Vd", "a\n|b\nc", "Vd", "a\n|c"},
		{"Vjd", "|a\nb\nc", "Vjd", "|c"},
		{"Vy p", "|a\nb", "Vyjp", "a\nb\n|a"},
		{"Vc", "a\n|b\nc", "VcX<esc>", "a\n|X\nc"},
		{"V>", "|a\nb", "Vj>", "  |a\n  b"},
		{"V<", "  |a\n  b", "Vj<", "|a\nb"},
		{"V esc", "a\n|b", "V<esc>d", "a\n|b"},
		{"V then v", "|abc\ndef", "Vvld", "|c\ndef"},
		{"v then V", "|abc\ndef", "vVd", "|def"},
		{"vD deletes whole lines", "a\n|bc\nd", "vD", "a\n|d"},
		{"v count motion", "|a b c d", "v2wd", "| d"},
		{"viw", "foo b|ar baz", "viwd", "foo | baz"},
		{"vaw", "foo b|ar baz", "vawd", "foo |baz"},
		{"vi(", "f(a|b)", "vi(d", "f(|)"},
		{`vi"`, `a "b|c" d`, `vi"d`, `a "|" d`},
		{"vip", "a\n|b\n\nc", "vipd", "|\nc"},
		{"v Korean", "|한국어 끝", "vld", "|어 끝"},
	})
}

func TestUndoRedo(t *testing.T) {
	runCases(t, []editCase{
		{"u after x", "|abc", "xu", "|abc"},
		{"uu", "|abc", "xxuu", "|abc"},
		{"u once", "|abc", "xxu", "|bc"},
		{"redo", "|abc", "xxuu<c-r>", "|bc"},
		{"redo twice", "|abc", "xxuu<c-r><c-r>", "|c"},
		{"redo after a new change is gone", "|abc", "xuxx<c-r>", "|c"},
		{"u with nothing to undo", "|abc", "u", "|abc"},
		{"redo with nothing to redo", "|abc", "<c-r>", "|abc"},
		{"3u", "|abcd", "xxx3u", "|abcd"},
		{"u of dd puts the line back", "a\n|b\nc", "ddu", "a\n|b\nc"},
		{"u of an insert session is one step", "|x", "ihello world<esc>u", "|x"},
		{"u of o with lines is one step", "|a", "ofoo<cr>bar<esc>u", "|a"},
		{"two insert sessions are two steps", "|", "ia<esc>ib<esc>u", "|a"},
		{"u of cw", "|foo bar", "cwX<esc>u", "|foo bar"},
		{"u of p", "|a\nb", "yyjpu", "a\n|b"},
		{"u of J", "a|\nb", "Ju", "|a\nb"},
		{"u of a backspaced insert", "|ab", "A<bs><bs>x<esc>u", "a|b"},
		{"insert that changes nothing leaves no step", "|abc", "xix<bs><esc>u", "|abc"},
		{"u does not undo what was never done", "|abc", "ix<esc>uu", "|abc"},
		{"redo of an insert", "|a", "ib<esc>u<c-r>", "|ba"},
		{"u of visual d", "|abcd", "vldu", "|abcd"},
		{"u of >>", "|a", ">>u", "|a"},
	})
}

func TestDotRepeat(t *testing.T) {
	runCases(t, []editCase{
		{"dw .", "|a b c d", "dw.", "|c d"},
		{"dw 2.", "|a b c d e", "dw2.", "|d e"},
		{"x .", "|abc", "x.", "|c"},
		{"3x .", "|abcdefgh", "3x.", "|gh"},
		{"x 2.", "|abcdef", "x2.", "|def"},
		{"d2w .", "|a b c d e f", "d2w.", "|e f"},
		{"d2w then 1.", "|a b c d e f", "d2w1.", "|d e f"},
		{"dd .", "|a\nb\nc", "dd.", "|c"},
		{"ciw .", "|foo bar", "ciwX<esc>w.", "X |X"},
		{"A .", "|a\nb", "A!<esc>j.", "a!\nb|!"},
		{"I .", "|a\nb", "I-<esc>j.", "-a\n|-b"},
		{"o .", "|a", "ofoo<esc>.", "a\nfoo\nfo|o"},
		{"O .", "|a", "Ofoo<esc>.", "fo|o\nfoo\na"},
		{"3ix", "|", "3ix<esc>", "xx|x"},
		{"3ix .", "|", "3ix<esc>.", "xxxx|xx"},
		{"2. replaces the insert count", "|", "3ix<esc>2.", "xxx|xx"},
		{"3ox", "|a", "3ox<esc>", "a\nx\nx\n|x"},
		{">> .", "|a\nb", ">>j.", "  a\n  |b"},
		{"J .", "|a\nb\nc", "J.", "a b| c"},
		{"p .", "|a", "ylp.", "aa|a"},
		{"p linewise .", "|a\nb", "yyp.", "a\na\n|a\nb"},
		{"~ .", "|abcd", "~.", "AB|cd"},
		{"r .", "|abc", "rxl.", "x|xc"},
		{"diw .", "|foo bar baz", "diww.", " | baz"},
		{"dot after u", "|abc", "xu.", "|bc"},
		{"dot is not itself repeated", "|abcdef", "x..", "|def"},
		{"dot after a motion keeps the change", "|abcdef", "xl.", "b|def"},
		{"dot with nothing to repeat", "|abc", ".", "|abc"},
		{"dot after visual d repeats the keys", "|abcdef", "vld.", "|ef"},
		{"dot after a failed command is the one before", "|abcd", "xdfz.", "|cd"},
		{"cw and . with a count", "|a b c", "cwX<esc>w.", "X |X c"},
	})
}

func TestInsertMode(t *testing.T) {
	insert := func(text string, row, col int) *Editor {
		e := New(text)
		e.SetCursor(row, col)
		return e
	}
	for _, c := range []struct {
		name, text string
		row, col   int
		keys, want string
	}{
		{"typing", "", 0, 0, "hi", "hi|"},
		{"typing in the middle", "ac", 0, 1, "b", "ab|c"},
		{"typing Korean", "", 0, 0, "한국어", "한국어|"},
		{"typing a space", "", 0, 0, "a<sp>b", "a b|"},
		{"enter splits the line", "abcd", 0, 2, "<cr>", "ab\n|cd"},
		{"enter at the end", "ab", 0, 2, "<cr>x", "ab\nx|"},
		{"backspace", "abc", 0, 2, "<bs>", "a|c"},
		{"backspace Korean", "한국어", 0, 2, "<bs>", "한|어"},
		{"backspace joins at the line start", "ab\ncd", 1, 0, "<bs>", "ab|cd"},
		{"backspace at the very start", "ab", 0, 0, "<bs>", "|ab"},
		{"delete", "abc", 0, 1, "<del>", "a|c"},
		{"delete joins at the line end", "ab\ncd", 0, 2, "<del>", "ab|cd"},
		{"ctrl+w deletes a word", "foo bar", 0, 7, "<c-w>", "foo |"},
		{"ctrl+w skips blanks first", "foo bar  ", 0, 9, "<c-w>", "foo |"},
		{"ctrl+w stops at punctuation", "foo.bar", 0, 7, "<c-w>", "foo.|"},
		{"ctrl+w at the line start joins", "ab\ncd", 1, 0, "<c-w>", "ab|cd"},
		{"ctrl+w Korean", "한국어 텍스트", 0, 7, "<c-w>", "한국어 |"},
		{"ctrl+u deletes to the line start", "foo bar", 0, 4, "<c-u>", "|bar"},
		{"ctrl+u at the line start", "foo", 0, 0, "<c-u>", "|foo"},
		{"arrows", "ab\ncd", 0, 0, "<right><right><right>", "ab\n|cd"},
		{"left wraps", "ab\ncd", 1, 0, "<left>", "ab|\ncd"},
		{"down and up keep the column", "abcd\nx\nabcd", 0, 3, "<down><down>", "abcd\nx\nabc|d"},
		{"up", "ab\ncd", 1, 1, "<up>", "a|b\ncd"},
		{"home and end", "abc", 0, 1, "<end>x<home>y", "y|abcx"},
		{"tab inserts two spaces", "", 0, 0, "a<tab>b", "a  b|"},
		{"newline in the middle of typing", "", 0, 0, "a<cr>b<cr>c", "a\nb\nc|"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := insert(c.text, c.row, c.col)
			feed(t, e, c.keys)
			if got := show(e); got != c.want {
				t.Errorf("got %q want %q", got, c.want)
			}
			if e.Mode() != Insert {
				t.Errorf("mode %v, want INSERT", e.Mode())
			}
		})
	}

	runCases(t, []editCase{
		{"esc steps the cursor left", "|", "ia<esc>", "|a"},
		{"esc at the line start stays", "|ab", "i<esc>", "|ab"},
		{"esc after typing mid line", "|ab", "lix<esc>", "a|xb"},
		{"i", "a|b", "iX<esc>", "a|Xb"},
		{"a", "a|b", "aX<esc>", "ab|X"},
		{"a on an empty line", "|", "aX<esc>", "|X"},
		{"I", "  a|b", "IX<esc>", "  |Xab"},
		{"A", "a|b", "AX<esc>", "ab|X"},
		{"o", "a|b\nc", "oX<esc>", "ab\n|X\nc"},
		{"O", "ab\nc|d", "OX<esc>", "ab\n|X\ncd"},
		{"3a", "|a", "3aX<esc>", "aXX|X"},
		{"o at the last line", "|a", "oX<esc>", "a\n|X"},
		{"esc esc from insert does not need a second esc to leave", "|ab", "iX<esc><esc>", "|Xab"},
	})
}

func TestCommandLine(t *testing.T) {
	for _, c := range []struct {
		name, keys string
		want       []Action
	}{
		{":w", ":w<cr>", []Action{SaveDraft}},
		{":write", ":write<cr>", []Action{SaveDraft}},
		{":q", ":q<cr>", []Action{Close}},
		{":q! is :q because drafts are always kept", ":q!<cr>", []Action{Close}},
		{":wq", ":wq<cr>", []Action{Close}},
		{":x", ":x<cr>", []Action{Preview}},
		{"ZZ is :x", "ZZ", []Action{Preview}},
		{"ZQ closes", "ZQ", []Action{Close}},
		{"Z then another key does nothing", "Zx", nil},
		{"esc cancels the command line", ":q<esc>", nil},
		{"backspace edits the command", ":qq<bs><cr>", []Action{Close}},
		{"backspace on an empty line leaves it", ":<bs>l", nil},
		{"ctrl+u clears", ":zzz<c-u>w<cr>", []Action{SaveDraft}},
		{"ctrl+w deletes a word", ":w foo<c-w><bs><cr>", []Action{SaveDraft}},
		{"unknown commands do nothing", ":frob<cr>", nil},
		{"an empty command does nothing", ":<cr>", nil},
		{"commands are trimmed", ":<sp>w<sp><cr>", []Action{SaveDraft}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := normalAt(t, "|abc")
			got := feed(t, e, c.keys)
			if len(got) != len(c.want) || (len(got) > 0 && got[0] != c.want[0]) {
				t.Errorf("actions %v, want %v", got, c.want)
			}
			if e.Text() != "abc" {
				t.Errorf("command line changed the text to %q", e.Text())
			}
		})
	}

	t.Run("mode and the line being typed", func(t *testing.T) {
		e := normalAt(t, "|abc")
		feed(t, e, ":wq")
		if e.Mode() != Command || e.CommandLine() != "wq" {
			t.Errorf("mode %v line %q", e.Mode(), e.CommandLine())
		}
		feed(t, e, "<esc>")
		if e.Mode() != Normal || e.CommandLine() != "" {
			t.Errorf("after esc: mode %v line %q", e.Mode(), e.CommandLine())
		}
	})
	t.Run("unknown command says so", func(t *testing.T) {
		e := normalAt(t, "|abc")
		feed(t, e, ":frob<cr>")
		if !strings.Contains(e.Status(), "frob") {
			t.Errorf("status %q", e.Status())
		}
	})
	t.Run("a number goes to the line", func(t *testing.T) {
		e := normalAt(t, "|a\n  b\nc")
		feed(t, e, ":2<cr>")
		if got := show(e); got != "a\n  |b\nc" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("typed text in the command line is not inserted", func(t *testing.T) {
		e := normalAt(t, "|abc")
		feed(t, e, ":dd")
		if e.Text() != "abc" {
			t.Errorf("text %q", e.Text())
		}
	})
}

func TestEscTwiceCloses(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	editor := func() *Editor {
		e := normalAt(t, "|abc")
		e.Now = func() time.Time { return now }
		return e
	}
	t.Run("one esc shows the hint", func(t *testing.T) {
		e := editor()
		if actions := feed(t, e, "<esc>"); len(actions) != 0 {
			t.Fatalf("actions %v", actions)
		}
		if e.Status() != EscHint {
			t.Errorf("status %q", e.Status())
		}
	})
	t.Run("two esc within a second close", func(t *testing.T) {
		e := editor()
		feed(t, e, "<esc>")
		now = now.Add(900 * time.Millisecond)
		if actions := feed(t, e, "<esc>"); len(actions) != 1 || actions[0] != Close {
			t.Fatalf("actions %v", actions)
		}
	})
	t.Run("two esc a second apart do not", func(t *testing.T) {
		e := editor()
		feed(t, e, "<esc>")
		now = now.Add(1100 * time.Millisecond)
		if actions := feed(t, e, "<esc>"); len(actions) != 0 {
			t.Fatalf("actions %v", actions)
		}
		now = now.Add(100 * time.Millisecond)
		if actions := feed(t, e, "<esc>"); len(actions) != 1 {
			t.Fatalf("the second of a new pair should close: %v", actions)
		}
	})
	t.Run("another key between them resets", func(t *testing.T) {
		e := editor()
		feed(t, e, "<esc>")
		feed(t, e, "l")
		if actions := feed(t, e, "<esc>"); len(actions) != 0 {
			t.Fatalf("actions %v", actions)
		}
	})
	t.Run("leaving insert mode is not the first esc", func(t *testing.T) {
		e := editor()
		if actions := feed(t, e, "ix<esc><esc>"); len(actions) != 0 {
			t.Fatalf("actions %v", actions)
		}
		if actions := feed(t, e, "<esc>"); len(actions) != 1 {
			t.Fatalf("the second normal esc should close: %v", actions)
		}
	})
	t.Run("esc in visual mode only leaves it", func(t *testing.T) {
		e := editor()
		if actions := feed(t, e, "v<esc><esc>"); len(actions) != 0 {
			t.Fatalf("actions %v", actions)
		}
	})
	t.Run("esc cancels a pending operator", func(t *testing.T) {
		e := editor()
		if actions := feed(t, e, "d<esc><esc>"); len(actions) != 0 {
			t.Fatalf("actions %v", actions)
		}
		if e.Text() != "abc" {
			t.Errorf("text %q", e.Text())
		}
	})
	t.Run("esc in the command line does not count", func(t *testing.T) {
		e := editor()
		if actions := feed(t, e, ":<esc><esc>"); len(actions) != 0 {
			t.Fatalf("actions %v", actions)
		}
	})
}

func TestPending(t *testing.T) {
	e := normalAt(t, "|a b c")
	feed(t, e, "2d")
	if e.Pending() != "2d" {
		t.Errorf("pending %q", e.Pending())
	}
	feed(t, e, "3")
	if e.Pending() != "2d3" {
		t.Errorf("pending %q", e.Pending())
	}
	feed(t, e, "<esc>")
	if e.Pending() != "" {
		t.Errorf("pending %q after esc", e.Pending())
	}
	feed(t, e, `"a`)
	if e.Pending() != `"a` {
		t.Errorf("pending %q", e.Pending())
	}
}

func TestPaste(t *testing.T) {
	t.Run("insert mode pastes at the cursor", func(t *testing.T) {
		e := New("ad")
		e.SetCursor(0, 1)
		e.Paste("b\nc")
		if got := show(e); got != "ab\nc|d" {
			t.Errorf("got %q", got)
		}
		feed(t, e, "<esc>u")
		if e.Text() != "ad" {
			t.Errorf("one undo should remove the paste: %q", e.Text())
		}
	})
	t.Run("normal mode pastes after the cursor like p", func(t *testing.T) {
		e := normalAt(t, "|ad")
		result := e.Paste("bc")
		if !result.Changed {
			t.Error("Changed not set")
		}
		if got := show(e); got != "ab|cd" {
			t.Errorf("got %q", got)
		}
		feed(t, e, "u")
		if got := show(e); got != "|ad" {
			t.Errorf("after undo: %q", got)
		}
	})
	t.Run("normal mode on an empty line", func(t *testing.T) {
		e := normalAt(t, "|")
		e.Paste("hi")
		if got := show(e); got != "h|i" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("multi-line paste in normal mode", func(t *testing.T) {
		e := normalAt(t, "|ab")
		e.Paste("x\ny")
		if e.Text() != "ax\nyb" {
			t.Errorf("got %q", e.Text())
		}
	})
	t.Run("visual mode paste leaves visual mode", func(t *testing.T) {
		e := normalAt(t, "|ab")
		feed(t, e, "v")
		e.Paste("x")
		if e.Mode() != Normal || e.Text() != "axb" {
			t.Errorf("mode %v text %q", e.Mode(), e.Text())
		}
	})
	t.Run("command line takes the first line", func(t *testing.T) {
		e := normalAt(t, "|ab")
		feed(t, e, ":")
		e.Paste("w\nx")
		if e.CommandLine() != "w" || e.Text() != "ab" {
			t.Errorf("line %q text %q", e.CommandLine(), e.Text())
		}
	})
	t.Run("a pasted insert repeats with a dot", func(t *testing.T) {
		e := normalAt(t, "|a\nb")
		feed(t, e, "A")
		e.Paste("!!")
		feed(t, e, "<esc>j.")
		if e.Text() != "a!!\nb!!" {
			t.Errorf("got %q", e.Text())
		}
	})
	t.Run("empty paste does nothing", func(t *testing.T) {
		e := normalAt(t, "|ab")
		if e.Paste("").Changed || e.Text() != "ab" {
			t.Error("empty paste changed something")
		}
	})
}

func TestSetTextAndHostEdits(t *testing.T) {
	e := normalAt(t, "ab|c")
	e.SetText("abc\n-- \nSig")
	if got := show(e); got != "ab|c\n-- \nSig" {
		t.Errorf("got %q", got)
	}
	feed(t, e, "u")
	if e.Text() != "abc" {
		t.Errorf("SetText should be one undo step: %q", e.Text())
	}
	e.SetText("a")
	if got := show(e); got != "|a" {
		t.Errorf("cursor clamps: %q", got)
	}
	if e.SetText("a"); len(e.undo) != 1 {
		t.Errorf("setting the same text must not add an undo step: %d", len(e.undo))
	}
}

func TestUndoLimit(t *testing.T) {
	e := normalAt(t, "|"+strings.Repeat("a", 250))
	feed(t, e, strings.Repeat("x", 250))
	feed(t, e, strings.Repeat("u", 300))
	if got := len(e.Text()); got != 200 {
		t.Errorf("undo went back %d steps from 0 letters, text has %d letters; want the 200 newest steps undone", 250-got, got)
	}
}

func TestEmptyAndTinyBuffers(t *testing.T) {
	keys := []string{"x", "X", "dd", "yy", "p", "P", "J", "w", "b", "e", "W", "B", "E", "G", "gg", "}", "{", "$", "0", "^",
		"dw", "cw", "diw", "daw", "dip", "dap", `di"`, "di(", "d$", "D", "C", "S", "s", "r", ">>", "<<", "u", "<c-r>", ".", "~",
		"o", "O", "a", "A", "i", "I", "f", ";", ",", "vd", "Vd", "vy", "Vp", "<left>", "<right>", "<up>", "<down>", ":w<cr>"}
	for _, text := range []string{"", "a", "\n", "a\n", "한", "  "} {
		for _, key := range keys {
			t.Run(strings.ReplaceAll(text, "\n", "/")+"/"+key, func(t *testing.T) {
				e := New(text)
				e.mode = Normal
				feed(t, e, key+"<esc><esc>")
				checkInvariants(t, e)
			})
		}
	}
}

// checkInvariants fails when the cursor is off the text.
func checkInvariants(t *testing.T, e *Editor) {
	t.Helper()
	if len(e.Buf.Lines) == 0 {
		t.Fatal("no lines")
	}
	row, col := e.Cursor()
	if row < 0 || row >= len(e.Buf.Lines) {
		t.Fatalf("row %d of %d", row, len(e.Buf.Lines))
	}
	limit := len([]rune(e.Buf.Lines[row]))
	if e.Mode() != Insert {
		limit = max(limit-1, 0)
	}
	if col < 0 || col > limit {
		t.Fatalf("col %d on %q in mode %v", col, e.Buf.Lines[row], e.Mode())
	}
}

// TestRandomKeysKeepTheCursorOnTheText throws keys at the engine, which must
// never panic or leave the cursor off the text.
func TestRandomKeysKeepTheCursorOnTheText(t *testing.T) {
	alphabet := strings.Split("hjklwWbBeE0^$gGg{}fFtT;,iIaAoOxXrsSCDJ~dcy<>pPuv:V.\"12 3xyz한国\n()[]{}\"'<c-r><esc><cr><bs><left><up><c-w><c-u>", "")
	named := []string{"<esc>", "<cr>", "<bs>", "<c-r>", "<left>", "<up>", "<c-w>", "<c-u>"}
	alphabet = append(alphabet, named...)
	texts := []string{"", "hello world\nfoo (bar) \"baz\"\n\n  indented line\n한국어 텍스트\n", "a"}
	seed := uint64(12345)
	next := func(n int) int {
		seed = seed*6364136223846793005 + 1442695040888963407
		return int((seed >> 33) % uint64(n))
	}
	for run := 0; run < 3000; run++ {
		e := New(texts[run%len(texts)])
		var played []string
		for range 40 {
			key := alphabet[next(len(alphabet))]
			played = append(played, key)
			feed(t, e, key)
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						t.Fatalf("panic %v after keys %q", recovered, played)
					}
				}()
				checkInvariants(t, e)
			}()
		}
	}
}

func TestStartInsertBeginsANewUndoStepAndLeavesOtherModes(t *testing.T) {
	e := New("")
	feed(t, e, "ab")
	e.StartInsert() // the host moved focus away and back
	feed(t, e, "c<esc>u")
	if e.Text() != "ab" {
		t.Errorf("u should undo only what was typed since: %q", e.Text())
	}
	feed(t, e, ":wq")
	e.StartInsert()
	if e.Mode() != Insert || e.CommandLine() != "" {
		t.Errorf("mode %v line %q", e.Mode(), e.CommandLine())
	}
	feed(t, e, "v")
	if e.Text() != "avb" {
		t.Errorf("typing after StartInsert: %q", e.Text())
	}
}

func TestModeNamesAndSelection(t *testing.T) {
	for mode, want := range map[Mode]string{Insert: "INSERT", Normal: "NORMAL", Visual: "VISUAL", VisualLine: "VISUAL LINE", Command: "COMMAND"} {
		if mode.String() != want {
			t.Errorf("%d is %q, want %q", mode, mode.String(), want)
		}
	}
	e := normalAt(t, "ab|cd\nef")
	if _, _, ok := e.Selection(); ok {
		t.Error("no selection in normal mode")
	}
	feed(t, e, "vj")
	if start, end, ok := e.Selection(); !ok || start != (Pos{0, 2}) || end != (Pos{1, 2}) {
		t.Errorf("selection %v-%v %v", start, end, ok)
	}
	feed(t, e, "V")
	if start, end, ok := e.Selection(); !ok || start != (Pos{0, 0}) || end != (Pos{1, 2}) {
		t.Errorf("linewise selection %v-%v %v", start, end, ok)
	}
}
