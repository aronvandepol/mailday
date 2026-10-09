package tui

// The today strip: one line under the mail header naming the meeting that is
// on now or starts within the hour, so it is never missed behind the inbox,
// with J to join it. The Calendar tab carries the count still to come today.

import (
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/aronvandepol/mailday/internal/calendar"
	"github.com/aronvandepol/mailday/internal/terminal"
)

const (
	// stripAhead is how far ahead (exclusive: "within the hour") an event gets the strip.
	stripAhead = 60 * time.Minute
	// stripSoon: an event starting this soon takes the strip from one that
	// is merely still going (a long workshop must not hide the next call).
	stripSoon = 10 * time.Minute
)

// stripEvent is the event the strip shows. Declined, all-day and untimed
// events never do; m.events is the range on screen, so browsing to another
// week clears the strip.
func (m Model) stripEvent() (event calendar.Event, running, ok bool) {
	now := m.now()
	var current, upcoming *calendar.Event
	for index := range m.events {
		candidate := &m.events[index]
		if candidate.AllDay || candidate.Start.IsZero() || candidate.RSVP == "declined" {
			continue
		}
		switch {
		case !candidate.Start.After(now) && candidate.End.After(now):
			if current == nil {
				current = candidate
			}
		case candidate.Start.After(now) && candidate.Start.Sub(now) < stripAhead:
			if upcoming == nil || candidate.Start.Before(upcoming.Start) {
				upcoming = candidate
			}
		}
	}
	switch {
	case upcoming != nil && (current == nil || upcoming.Start.Sub(now) <= stripSoon):
		return *upcoming, false, true
	case current != nil:
		return *current, true, true
	}
	return calendar.Event{}, false, false
}

// todayStripLine is the strip where it belongs: on the mail list only.
func (m Model) todayStripLine(width int) string {
	if m.screen != screenHome || m.focus != paneMail {
		return ""
	}
	return m.renderTodayStrip(width)
}

// renderTodayStrip is the strip's line, or "" when nothing is near.
func (m Model) renderTodayStrip(width int) string {
	event, running, ok := m.stripEvent()
	accent := lipgloss.NewStyle().Foreground(colorActive).Bold(true)
	if !ok {
		// Nothing else to show: a snoozed message about to come back, so it is not missed.
		if u, ok := m.stripUpcoming(); ok {
			text := accent.Render("✉ back at "+u.due.Local().Format("15:04")+":") + " " + terminal.SanitizeLine(u.message.Subject)
			return centerText(truncateToWidth(text, width), width)
		}
		return ""
	}
	now := m.now()
	var text string
	if running {
		text = accent.Render("now") + " " + event.Summary + " until " + event.End.Local().Format("15:04")
	} else {
		text = accent.Render("next") + " " + event.Start.Local().Format("15:04") + " " + event.Summary +
			" in " + roughDuration(event.Start.Sub(now))
	}
	if event.Location != "" {
		text += " · " + event.Location
	}
	if meetingLink(event) != "" {
		text += " · " + accent.Render("J") + " joins"
	}
	return centerText(truncateToWidth(text, width), width)
}

// calendarTabLabel adds today's remaining timed events to the tab: "Calendar · 3".
func (m Model) calendarTabLabel() string {
	now := m.now()
	remaining := 0
	for _, event := range m.events {
		if !event.AllDay && event.RSVP != "declined" && event.End.After(now) && sameDay(event.Start, now) {
			remaining++
		}
	}
	if remaining == 0 {
		return "Calendar"
	}
	return fmt.Sprintf("Calendar · %d", remaining)
}

// sectionNavItems is the Mail/Calendar tab row.
func (m Model) sectionNavItems() []navItem {
	return []navItem{
		{shortcut: "M", label: "Mail"},
		{shortcut: "C", label: m.calendarTabLabel()},
	}
}

// joinStripEvent is J on the mail list: open the strip's meeting.
func (m Model) joinStripEvent() (Model, tea.Cmd) {
	event, _, ok := m.stripEvent()
	if !ok {
		m.status = "No meeting starting soon"
		return m, nil
	}
	link := meetingLink(event)
	if link == "" {
		m.status = event.Summary + " has no meeting link"
		return m, nil
	}
	m.status = "Joining " + event.Summary
	return m, launchDetached(append(openCommand(), link), "Meeting link")
}
