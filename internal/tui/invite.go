package tui

// Meeting invitations in the reader: a card above the message (what, when,
// who, your answer, clashes) and y ~ x J c on it. Answers go through the
// calendar event with the invitation's UID, because mailday-calendar answers
// by the event's server id and tells the organiser itself; the mail's own
// REPLY is never written.

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/aronvandepol/mailday/internal/calendar"
	"github.com/aronvandepol/mailday/internal/compose"
	"github.com/aronvandepol/mailday/internal/maildir"
)

// inviteLookupMsg carries the events around an invitation's time, for an
// invitation whose event is outside the loaded week.
type inviteLookupMsg struct {
	path   string // the message the key was pressed on
	uid    string
	start  time.Time
	intent string // accept, tentative, decline or show
	events []calendar.Event
	err    error
}

const inviteNotYet = "Not in your calendar yet; try again in a minute"

// activeInvitation is the invitation of the message open in the reader.
func (m Model) activeInvitation() *maildir.Invitation {
	if m.screen != screenMail || m.loadingBody || m.content == nil {
		return nil
	}
	return m.content.Invitation
}

// matchInviteEvent finds the calendar event of an invitation by UID, the
// occurrence nearest its start when the event repeats. Exchange writes UIDs
// in upper-case hex and Outlook mail in whatever case, so case is ignored.
func matchInviteEvent(events []calendar.Event, invitation *maildir.Invitation) (calendar.Event, bool) {
	var best calendar.Event
	found := false
	sources := map[string]bool{}
	for _, event := range events {
		if invitation.UID == "" || !strings.EqualFold(event.UID, invitation.UID) {
			continue
		}
		sources[event.Source] = true
		if !found || absDuration(event.Start.Sub(invitation.Start)) < absDuration(best.Start.Sub(invitation.Start)) {
			best, found = event, true
		}
	}
	// The same invitation in two calendars (invited at two addresses): which
	// one answers is not ours to guess; the calendar view lets you pick.
	if len(sources) > 1 {
		return calendar.Event{}, false
	}
	return best, found
}

func absDuration(value time.Duration) time.Duration {
	if value < 0 {
		return -value
	}
	return value
}

// answerWords are how an RSVP state reads on the card.
var answerWords = map[string]string{
	"needs-action": "not answered",
	"accepted":     "accepted",
	"tentative":    "maybe",
	"declined":     "declined",
	"delegated":    "delegated",
}

func (m Model) inviteLink(invitation *maildir.Invitation) string {
	if invitation.Link != "" {
		return invitation.Link
	}
	return meetingLink(calendar.Event{Location: invitation.Location, Description: invitation.Description})
}

// inviteCard draws the card for the reader header, padded to the reading
// column and ending in a blank line; nil when the message has no invitation.
func (m Model) inviteCard(width int) []string {
	invitation := m.activeInvitation()
	if invitation == nil {
		return nil
	}
	column, margin := readerColumn(width)
	pad := strings.Repeat(" ", margin)
	when := describeSpan(invitation.Start, invitation.End, invitation.AllDay)
	organizer := personName(invitation.OrganizerName, invitation.OrganizerEmail)
	event, matched := matchInviteEvent(m.events, invitation)

	label, parts := "Invitation", []string{}
	labelStyle := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
	switch invitation.Method {
	case "CANCEL":
		label = "Cancelled"
		labelStyle = lipgloss.NewStyle().Foreground(colorError).Bold(true)
		parts = append(parts, when)
		if organizer != "" {
			parts = append(parts, "by "+organizer)
		}
	case "REPLY", "COUNTER":
		who := "Someone"
		verb := " replied"
		if len(invitation.Attendees) > 0 {
			attendee := invitation.Attendees[0]
			who = personName(attendee.Name, attendee.Email)
			verb = map[string]string{
				"accepted":     " accepted",
				"declined":     " declined",
				"tentative":    " said maybe",
				"needs-action": " has not answered",
				"delegated":    " passed it on",
			}[attendee.Status]
		}
		if invitation.Method == "COUNTER" {
			verb = " proposes a new time"
		}
		label = who + verb
		parts = append(parts, when)
	default:
		if invitation.Sequence > 0 {
			label = "Updated invitation"
		}
		parts = append(parts, when)
		if place := inviteLocation(invitation.Location); place != "" {
			parts = append(parts, place)
		}
		if compose.IsOwn(invitation.OrganizerEmail) {
			parts = append(parts, "organised by you")
		} else if organizer != "" {
			parts = append(parts, "from "+organizer)
		}
		if guests := invitation.Guests(); guests == 1 {
			parts = append(parts, "1 guest")
		} else if guests > 1 {
			parts = append(parts, fmt.Sprintf("%d guests", guests))
		}
		if invitation.Repeats {
			parts = append(parts, "repeats")
		}
		if answer := m.inviteAnswer(invitation, event, matched); answer != "" {
			parts = append(parts, "you: "+answer)
		}
	}

	text := label + " · " + strings.Join(parts, " · ")
	lines := wrapText(text, column)
	if len(lines) > 0 && strings.HasPrefix(lines[0], label) {
		lines[0] = labelStyle.Render(label) + lines[0][len(label):]
	}
	if invitation.Method == "REQUEST" && invitation.Start.After(m.now().Add(-time.Hour)) {
		self := event
		if !matched {
			self = calendar.Event{}
		}
		if note := m.clashNote(self, invitation.Start, invitation.End, invitation.AllDay); note != "" {
			warning := lipgloss.NewStyle().Foreground(colorActive)
			lines = append(lines, warning.Render(truncateToWidth("⚠ "+note, column)))
		}
	}
	lines = append(lines, styleMuted.Render(inviteHints(invitation, m.inviteLink(invitation) != "")), "")
	for index := range lines {
		lines[index] = pad + lines[index]
	}
	return lines
}

// inviteLocation drops a bare web address, which is the meeting link and has
// its own key, and keeps a room or street.
func inviteLocation(location string) string {
	location = strings.TrimSpace(location)
	if strings.HasPrefix(location, "http://") || strings.HasPrefix(location, "https://") {
		return "online"
	}
	return location
}

// inviteAnswer is your answer, preferring the calendar's: after you answer
// there the mail still carries the PARTSTAT it was sent with.
func (m Model) inviteAnswer(invitation *maildir.Invitation, event calendar.Event, matched bool) string {
	if compose.IsOwn(invitation.OrganizerEmail) {
		return ""
	}
	state := invitation.StatusOf(compose.IsOwn)
	if matched && event.RSVP != "" {
		state = event.RSVP
	}
	if state == "" {
		return ""
	}
	if word, ok := answerWords[state]; ok {
		return word
	}
	return state
}

func inviteHints(invitation *maildir.Invitation, hasLink bool) string {
	if invitation.Method != "REQUEST" {
		return "c show in calendar"
	}
	hints := "y accept · ~ maybe · x decline"
	if hasLink {
		hints += " · J join"
	}
	return hints + " · c show in calendar"
}

// handleInviteKey takes y ~ x J c in the reader when the message has an
// invitation. a stays archive.
func (m Model) handleInviteKey(key string) (tea.Model, tea.Cmd, bool) {
	invitation := m.activeInvitation()
	if invitation == nil || m.flow.picker != nil { // the picker's keys come first
		return m, nil, false
	}
	request := invitation.Method == "REQUEST"
	switch key {
	case "y", "~", "x":
		if !request {
			return m, nil, false
		}
		answer := map[string]string{"y": "accept", "~": "tentative", "x": "decline"}[key]
		if event, ok := matchInviteEvent(m.events, invitation); ok {
			model, command := m.answerEvent(event, answer)
			return model, command, true
		}
		m.status = "Looking for the event in your calendar…"
		return m, m.inviteLookupCmd(invitation, answer), true
	case "J":
		if !request {
			return m, nil, false
		}
		link := m.inviteLink(invitation)
		if link == "" {
			m.status = "This invitation has no meeting link"
			return m, nil, true
		}
		m.status = "Joining " + invitation.Summary
		return m, launchDetached(append(openCommand(), link), "Meeting link"), true
	case "c":
		if event, ok := matchInviteEvent(m.events, invitation); ok {
			model, command := m.showInCalendar(event, nil)
			return model, command, true
		}
		m.status = "Looking for the event in your calendar…"
		return m, m.inviteLookupCmd(invitation, "show"), true
	}
	return m, nil, false
}

// inviteLookupCmd loads the calendar around the invitation: its own day, or
// the coming days for a repeating event whose first date is past.
func (m Model) inviteLookupCmd(invitation *maildir.Invitation, intent string) tea.Cmd {
	from := dayOf(invitation.Start.Local())
	to := dayOf(invitation.End.Local()).AddDate(0, 0, 1)
	if invitation.Repeats {
		if today := dayOf(m.now()); today.After(from) {
			from = today
		}
		to = from.AddDate(0, 0, m.options.Days)
	}
	store, uid, start, path := m.calendarStore, invitation.UID, invitation.Start, m.contentPath
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		result, err := store.Load(ctx, from, to)
		return inviteLookupMsg{path: path, uid: uid, start: start, intent: intent, events: result.Events, err: err}
	}
}

// handleInviteLookup carries on with an answer or "show" once the event has
// been found in the calendar, or says it is not there yet.
func (m Model) handleInviteLookup(message inviteLookupMsg) (tea.Model, tea.Cmd) {
	// The user may have moved on (another message, the composer): act only
	// while the invitation is still on screen.
	if message.path != "" && !m.readerShows(message.path) {
		return m, nil
	}
	if message.err != nil {
		m.status = "Calendar error: " + oneLine(message.err.Error())
		return m, nil
	}
	event, ok := matchInviteEvent(message.events, &maildir.Invitation{UID: message.uid, Start: message.start})
	if !ok {
		m.status = inviteNotYet
		return m, func() tea.Msg { refreshCalendarSoon(); return nil }
	}
	if message.intent == "show" {
		return m.showInCalendar(event, message.events)
	}
	return m.answerEvent(event, message.intent)
}

func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// answerEvent selects the invitation's event in the loaded list, adding it
// when it comes from outside the loaded range, and answers it as a does in
// the calendar.
func (m Model) answerEvent(event calendar.Event, answer string) (tea.Model, tea.Cmd) {
	if event.Organizer == "" {
		m.status = "You organised this event; there is nothing to answer"
		return m, nil
	}
	if m.findEvent(func(e calendar.Event) bool { return e.ID == event.ID }) < 0 {
		m.events = append(m.events, event)
		m.sortEvents()
	}
	m.selectEvent(event.ID)
	return m.respond(answer)
}

// showInCalendar leaves the reader for the calendar's day view on the event.
// dayEvents, when given, replace the loaded list so the event is there at
// once; a quiet reload for the day follows either way.
func (m Model) showInCalendar(event calendar.Event, dayEvents []calendar.Event) (tea.Model, tea.Cmd) {
	if dayEvents != nil {
		m.events = dayEvents
	}
	m.screen = screenHome
	m.detailScroll = 0
	m.readerGone = false
	m.focus = paneAgenda
	m.calendarMode = calendarDay
	m.calendarAnchor = dayOf(event.Start.Local())
	m.selectEvent(event.ID)
	m.calQuietLoading = true
	m.status = event.Summary
	return m, m.quietLoadCalendarCmd()
}
