package tui

// The message reader: a header card above a centred reading column. HTML mail
// goes through htmlutil.ToMarkdown and glamour, ported from hey-cli, so lists,
// headings, emphasis and links keep their shape. Plain-text mail is typeset
// here instead, because reading it as Markdown would mangle stray * and #.

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/aronvandepol/mailday/internal/compose"
	"github.com/aronvandepol/mailday/internal/htmlutil"
	"github.com/aronvandepol/mailday/internal/maildir"
	"github.com/aronvandepol/mailday/internal/markdown"
)

const readerMaxWidth = 80

// Some university directories write names as "Jansen, J.J. (Jolien)" or
// "Vries, S. de (Sam)".
var directoryName = regexp.MustCompile(`^([^,()]+),\s*(?:[A-Z]\.\s*)+((?:\s*[a-z]+)*)\s*\(([^)]+)\)$`)

// historyStart matches the line that opens a forwarded or replied-to message
// in Outlook, Gmail and Apple Mail, in English and Dutch.
var historyStart = regexp.MustCompile(`^(-{2,}\s*(Original Message|Oorspronkelijk bericht|Forwarded message|Doorgestuurd bericht|Messaggio originale|Ursprüngliche Nachricht)\s*-{2,}|(On|Op|Il|Am|Le|El) .{4,160}` + attributionVerbs + `:?)$`)

// attributionVerbs ends the "On <date>, <name> wrote:" line in English,
// Dutch, Italian, German, French, Spanish, Korean and Japanese.
const attributionVerbs = `(wrote|schreef( het volgende)?|ha scritto|schrieb|a écrit|escribió|님이 작성:|が書きました)`

var historyHeader = regexp.MustCompile(`^(From|Van|Sent|Verzonden|Date|Datum):\s`)

func readerColumn(width int) (int, int) {
	column := min(max(width-4, 20), readerMaxWidth)
	return column, max((width-column)/2, 0)
}

func personName(name, address string) string {
	name = strings.Trim(strings.TrimSpace(name), `"'`)
	if match := directoryName.FindStringSubmatch(name); match != nil {
		parts := []string{strings.TrimSpace(match[3])}
		if particle := strings.TrimSpace(match[2]); particle != "" {
			parts = append(parts, particle)
		}
		return strings.Join(append(parts, strings.TrimSpace(match[1])), " ")
	}
	if name == "" {
		return address
	}
	return name
}

func recipientNames(list []maildir.Address) string {
	names := make([]string, 0, len(list))
	you := false
	for _, address := range list {
		if compose.IsOwn(address.Addr) {
			you = true
			continue
		}
		names = append(names, personName(address.Name, address.Addr))
	}
	if you {
		names = append(names, "you")
	}
	if len(names) > 4 {
		return strings.Join(names[:3], ", ") + fmt.Sprintf(" and %d others", len(names)-3)
	}
	return strings.Join(names, ", ")
}

func (m Model) renderReaderHeader(selected maildir.Message, width int) []string {
	column, margin := readerColumn(width)
	pad := strings.Repeat(" ", margin)
	rule := lipgloss.NewStyle().Foreground(colorChrome).Render(strings.Repeat("─", column))
	subject := lipgloss.NewStyle().Foreground(colorBright).Bold(true)
	name := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)

	fromName, fromAddr, date := selected.From, selected.FromAddr, selected.Date
	var to, cc string
	if m.content != nil {
		fromName = personName(m.content.FromName, m.content.FromAddr)
		fromAddr = m.content.FromAddr
		if !m.content.Date.IsZero() {
			date = m.content.Date
		}
		to = recipientNames(m.content.ToList)
		cc = recipientNames(m.content.CCList)
	}
	local := date.In(time.Local)

	var lines []string
	subjectLines := wrapText(selected.Subject, column)
	if len(subjectLines) > 2 {
		subjectLines = append(subjectLines[:1], truncateToWidth(strings.Join(subjectLines[1:], " "), column))
	}
	for _, line := range subjectLines {
		lines = append(lines, subject.Render(line))
	}
	lines = append(lines, rule,
		fillBetween(name.Render(truncateToWidth(fromName, column-18)), styleMuted.Render(local.Format("Mon 2 Jan 2006")), column),
		fillBetween(styleMuted.Render(truncateToWidth(fromAddr, column-8)), styleMuted.Render(local.Format("15:04")), column))
	for _, field := range [][2]string{{"to", to}, {"cc", cc}} {
		if field[1] == "" {
			continue
		}
		// Wrapped, not cut off, so a long list can be read to its end.
		for index, line := range wrapText(field[1], max(column-3, 1)) {
			prefix := "   "
			if index == 0 {
				prefix = field[0] + " "
			}
			lines = append(lines, styleMuted.Render(prefix+line))
		}
	}
	if m.content != nil && !m.showRecipients && (len(m.content.ToList) > 4 || len(m.content.CCList) > 4) {
		lines = append(lines, styleMuted.Render(fmt.Sprintf("i shows all %d people", len(peopleOn(*m.content, false)))))
	}
	lines = append(lines, rule, "")
	for index := range lines {
		lines[index] = pad + lines[index]
	}
	return lines
}

// readerBody renders the message body and its attachments for one terminal
// width, centred in its reading column.
func readerBody(content maildir.Content, width int, showHistory bool) []string {
	column, margin := readerColumn(width)
	return indentLines(readerBodyLines(content, column, showHistory), margin)
}

func indentLines(lines []string, margin int) []string {
	pad := strings.Repeat(" ", margin)
	out := make([]string, len(lines))
	for index, line := range lines {
		out[index] = pad + line
	}
	return out
}

// readerBodyLines renders the body and its attachments for a reading column,
// without the left margin: it depends on the column only, so a terminal wider
// than the column can resize without re-running glamour. Quoted history at the
// end of a reply is collapsed unless showHistory is set.
func readerBodyLines(content maildir.Content, column int, showHistory bool) []string {
	var lines []string
	hidden := 0
	if strings.TrimSpace(content.HTML) != "" {
		rendered := markdown.Render(htmlutil.ToMarkdown(content.HTML), column)
		if strings.TrimSpace(ansi.Strip(rendered)) != "" {
			lines = muteSignature(hangListItems(trimBlankEdges(strings.Split(rendered, "\n")), column))
			plain := make([]string, len(lines))
			for index, line := range lines {
				plain[index] = strings.TrimSpace(ansi.Strip(line))
			}
			if start := historyIndex(plain); start >= 0 {
				if visible := trimBlankEdges(lines[:start]); !showHistory && len(visible) > 0 {
					hidden = len(lines) - start
					lines = visible
				} else {
					for rest := start; rest < len(lines); rest++ {
						lines[rest] = muteLine(lines[rest])
					}
				}
			}
		}
	}
	if lines == nil {
		source := strings.Split(strings.ReplaceAll(content.Body, "\r\n", "\n"), "\n")
		if start := plainHistoryIndex(source); start >= 0 && !showHistory && strings.TrimSpace(strings.Join(source[:start], "")) != "" {
			hidden = len(source) - start
			source = source[:start]
		}
		lines = typesetPlain(strings.Join(source, "\n"), column)
	}
	if hidden > 0 {
		lines = append(lines, "", styleMuted.Render(fmt.Sprintf("··· %d lines of earlier messages hidden · z shows them", hidden)))
	}
	if len(content.Attachments) > 0 {
		lines = append(lines, "", lipgloss.NewStyle().Foreground(colorChrome).Render(strings.Repeat("─", column)))
		nameStyle := lipgloss.NewStyle().Foreground(colorBright)
		for _, attachment := range content.Attachments {
			size := styleMuted.Render(humanSize(attachment.Size))
			label := "📎 " + nameStyle.Render(truncateToWidth(attachment.Name, column-14))
			lines = append(lines, fillBetween(label, size, column))
		}
		lines = append(lines, styleMuted.Render("S saves them to "+collapseHome(downloadDir())+" · O opens one"))
	}
	return lines
}

// typesetPlain styles a text/plain body: quote bars for "> " lines, and the
// signature and any quoted history after it in the muted style.
func typesetPlain(body string, column int) []string {
	bar := lipgloss.NewStyle().Foreground(colorChrome)
	var out []string
	muted := false
	source := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	for index, line := range source {
		trimmed := strings.TrimSpace(line)
		if line == "-- " || signatureRule.MatchString(trimmed) || historyStart.MatchString(trimmed) || startsHeaderBlock(source, index) {
			muted = true
		}
		depth := 0
		rest := line
		for {
			stripped := strings.TrimLeft(rest, " ")
			if !strings.HasPrefix(stripped, ">") {
				break
			}
			depth++
			rest = strings.TrimPrefix(strings.TrimPrefix(stripped, ">"), " ")
		}
		if depth > 0 {
			prefix := bar.Render(strings.Repeat("│ ", min(depth, 4)))
			for _, wrapped := range wrapText(rest, max(column-2*min(depth, 4), 10)) {
				out = append(out, prefix+styleMuted.Render(wrapped))
			}
			continue
		}
		for _, wrapped := range wrapText(line, column) {
			if muted {
				out = append(out, muteLine(wrapped))
			} else {
				out = append(out, markdown.LinkifyURLs(wrapped))
			}
		}
	}
	return trimBlankEdges(compactBlank(out))
}

// startsHeaderBlock spots an Outlook-style "From: / Sent:" block, which opens
// the quoted history in replies that carry no "> " markers.
func startsHeaderBlock(lines []string, index int) bool {
	first := strings.TrimSpace(lines[index])
	if !strings.HasPrefix(first, "From:") && !strings.HasPrefix(first, "Van:") {
		return false
	}
	for next := index + 1; next < min(index+4, len(lines)); next++ {
		if historyHeader.MatchString(strings.TrimSpace(lines[next])) {
			return true
		}
	}
	return false
}

// historyIndex finds where quoted history opens in a rendered HTML body: an
// Outlook "From:/Sent:" block, a separator, or an "On … wrote:" line. A rule
// drawn just above it belongs to the history too. It returns -1 for none.
func historyIndex(plain []string) int {
	for index := range plain {
		if historyStart.MatchString(plain[index]) || startsHeaderBlock(plain, index) {
			for index > 0 && (plain[index-1] == "" || strings.Trim(plain[index-1], "─_-") == "") {
				index--
			}
			return index
		}
	}
	return -1
}

// signatureRule is a line of dashes or underscores, which opens a signature
// in most mail clients ("-- " is the formal delimiter).
var signatureRule = regexp.MustCompile(`^(-{2,}|_{4,})$`)

// muteSignature dims a rendered HTML body from its signature rule onward. The
// text stays readable; only its weight drops.
func muteSignature(lines []string) []string {
	for index, line := range lines {
		if plain := strings.TrimSpace(ansi.Strip(line)); signatureRule.MatchString(plain) || plain == "--" {
			for rest := index; rest < len(lines); rest++ {
				lines[rest] = muteLine(lines[rest])
			}
			break
		}
	}
	return lines
}

// muteLine drops a rendered line's colours for the muted style but keeps its
// URLs clickable.
func muteLine(line string) string {
	return styleMuted.Render(markdown.LinkifyURLs(ansi.Strip(line)))
}

var attribution = regexp.MustCompile(`(?i)` + attributionVerbs + `:\s*$`)

// plainHistoryIndex finds where quoted history starts in a text/plain body: a
// separator or From:/Sent: block (everything after it is history), or the
// trailing run of "> " lines with its attribution line. Inline replies, where
// quotes and answers alternate, keep their quotes. It returns -1 for none.
func plainHistoryIndex(lines []string) int {
	quoted := func(line string) bool { return strings.HasPrefix(strings.TrimLeft(line, " "), ">") }
	best := -1
	for index := range lines {
		trimmed := strings.TrimSpace(lines[index])
		if (historyStart.MatchString(trimmed) && !attribution.MatchString(trimmed)) || startsHeaderBlock(lines, index) {
			best = index
			break
		}
	}
	last := len(lines) - 1
	for last >= 0 && strings.TrimSpace(lines[last]) == "" {
		last--
	}
	if last >= 0 && quoted(lines[last]) {
		start := last
		for start > 0 && (quoted(lines[start-1]) || strings.TrimSpace(lines[start-1]) == "") {
			start--
		}
		for start < last && strings.TrimSpace(lines[start]) == "" {
			start++
		}
		// Pull in the "On … wrote:" line, which may wrap over two lines.
		previous := start - 1
		for previous >= 0 && strings.TrimSpace(lines[previous]) == "" {
			previous--
		}
		if previous >= 0 && attribution.MatchString(strings.TrimSpace(lines[previous])) {
			start = previous
			if start > 0 && !attribution.MatchString(strings.TrimSpace(lines[start-1])) && strings.TrimSpace(lines[start-1]) != "" && len(strings.TrimSpace(lines[start])) < 30 {
				start--
			}
		}
		if best < 0 || start < best {
			best = start
		}
	}
	return best
}

var listMarker = regexp.MustCompile(`^(\s*)(\d+\. |• )`)

// hangListItems rewraps list items so wrapped lines sit under the item text
// rather than under the marker; glamour starts them at the left edge.
func hangListItems(lines []string, column int) []string {
	out := make([]string, 0, len(lines))
	for index := 0; index < len(lines); index++ {
		match := listMarker.FindStringSubmatch(ansi.Strip(lines[index]))
		if match == nil {
			out = append(out, lines[index])
			continue
		}
		item := []string{lines[index]}
		for index+1 < len(lines) {
			next := ansi.Strip(lines[index+1])
			if strings.TrimSpace(next) == "" || listMarker.MatchString(next) {
				break
			}
			item = append(item, strings.TrimLeft(lines[index+1], " "))
			index++
		}
		if len(item) == 1 {
			out = append(out, item[0])
			continue
		}
		hang := strings.Repeat(" ", ansi.StringWidth(match[0]))
		wrapped := strings.Split(ansi.Wordwrap(strings.Join(item, " "), column, ""), "\n")
		out = append(out, wrapped[0])
		if len(wrapped) > 1 {
			rest := ansi.Wordwrap(strings.Join(wrapped[1:], " "), column-len(hang), "")
			for _, line := range strings.Split(rest, "\n") {
				out = append(out, hang+line)
			}
		}
	}
	return out
}

func compactBlank(lines []string) []string {
	out := make([]string, 0, len(lines))
	blank := 0
	for _, line := range lines {
		if strings.TrimSpace(ansi.Strip(line)) == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, line)
	}
	return out
}

func trimBlankEdges(lines []string) []string {
	start, end := 0, len(lines)
	for start < end && strings.TrimSpace(ansi.Strip(lines[start])) == "" {
		start++
	}
	for end > start && strings.TrimSpace(ansi.Strip(lines[end-1])) == "" {
		end--
	}
	return lines[start:end]
}

func humanSize(bytes int64) string {
	switch {
	case bytes >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1<<20))
	case bytes >= 1<<10:
		return fmt.Sprintf("%d KB", bytes>>10)
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
