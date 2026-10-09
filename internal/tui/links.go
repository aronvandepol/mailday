package tui

// l in the reader: the message's web links as a numbered list, call links
// first, to open with the system opener.

import (
	"errors"
	"net/url"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/aronvandepol/mailday/internal/calendar"
	"github.com/aronvandepol/mailday/internal/htmlutil"
	"github.com/aronvandepol/mailday/internal/maildir"
	"github.com/aronvandepol/mailday/internal/markdown"
	"github.com/aronvandepol/mailday/internal/terminal"
)

var errNotWebLink = errors.New("not a web link")

// isMeetingURL reuses the event view's call-link detection (Teams, Zoom, Meet,
// Webex, Whereby).
func isMeetingURL(link string) bool {
	return meetingLink(calendar.Event{Description: link}) != ""
}

// readerLinks lists the http(s) links the reader shows for a message, without
// duplicates: call links first, then the rest in reading order. Links in
// quoted history count only while the history is shown, as the reader does.
// more is how many were left out beyond pickerLimit.
func readerLinks(content maildir.Content, column int, showHistory bool) (links []string, more int) {
	var found []string
	if strings.TrimSpace(content.HTML) != "" {
		linked := markdown.RenderLinked(htmlutil.ToMarkdown(content.HTML), column, -1)
		limit := -1 // the line the quoted history starts on
		if !showHistory {
			lines := strings.Split(linked.Text, "\n")
			plain := make([]string, len(lines))
			for index, line := range lines {
				plain[index] = strings.TrimSpace(ansi.Strip(line))
			}
			limit = historyIndex(plain)
		}
		for _, occurrence := range linked.Links {
			if limit < 0 || occurrence.StartLine < limit {
				found = append(found, occurrence.Destination)
			}
		}
	} else {
		source := strings.Split(strings.ReplaceAll(content.Body, "\r\n", "\n"), "\n")
		if start := plainHistoryIndex(source); start >= 0 && !showHistory {
			source = source[:start]
		}
		found = linkPattern.FindAllString(strings.Join(source, "\n"), -1)
	}
	seen := make(map[string]bool)
	var meeting, other []string
	for _, link := range found {
		link = strings.TrimRight(link, ".,;:!?")
		if seen[link] || !isWebLink(link) {
			continue
		}
		seen[link] = true
		if isMeetingURL(link) {
			meeting = append(meeting, link)
		} else {
			other = append(other, link)
		}
	}
	links = append(meeting, other...)
	if len(links) > pickerLimit {
		more = len(links) - pickerLimit
		links = links[:pickerLimit]
	}
	return links, more
}

// isWebLink accepts http and https addresses with a host and no control
// characters, the only thing handed to the system opener.
func isWebLink(link string) bool {
	if strings.IndexFunc(link, unicode.IsControl) >= 0 {
		return false
	}
	parsed, err := url.Parse(link)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Hostname() != "" && parsed.User == nil
}

// linkParts splits a link for display: the host without "www.", and the path
// (with … for a query) that follows it.
func linkParts(link string) (host, path string) {
	parsed, err := url.Parse(link)
	if err != nil {
		return terminal.SanitizeLine(link), ""
	}
	host = strings.TrimPrefix(parsed.Hostname(), "www.")
	path = strings.TrimSuffix(parsed.Path, "/")
	if parsed.RawQuery != "" {
		path += "?…"
	}
	return terminal.SanitizeLine(host), terminal.SanitizeLine(path)
}

// listLinks starts l.
func (m Model) listLinks() (Model, tea.Cmd) {
	if !m.readerReady() {
		return m, nil
	}
	if m.content == nil {
		m.status = "Still reading the message"
		return m, nil
	}
	content, path, showHistory := *m.content, m.contentPath, m.showHistory
	column, _ := readerColumn(max(m.width, 20))
	m.status = "Finding links"
	// Rendering the HTML is slow, so it runs off the UI thread.
	return m, func() tea.Msg {
		links, more := readerLinks(content, column, showHistory)
		message := linksListedMsg{path: path, hidden: more}
		for _, link := range links {
			host, rest := linkParts(link)
			item := pickerItem{text: host, detail: rest, url: link}
			if isMeetingURL(link) {
				item.tag = "meeting"
			}
			message.items = append(message.items, item)
		}
		return message
	}
}

func openLinkCmd(item pickerItem) tea.Cmd {
	return func() tea.Msg {
		if !isWebLink(item.url) {
			return openedMsg{what: item.text, err: errNotWebLink}
		}
		return openedMsg{what: item.text, err: launchOpener(item.url)}
	}
}
