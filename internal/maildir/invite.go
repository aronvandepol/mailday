package maildir

import (
	"strconv"
	"strings"
	"time"

	"github.com/aronvandepol/mailday/internal/calendar"
	"github.com/aronvandepol/mailday/internal/terminal"
)

// InviteAttendee is one ATTENDEE of an invitation.
type InviteAttendee struct {
	Name  string
	Email string // lower case
	// Status is PARTSTAT in lower case: needs-action, accepted, tentative,
	// declined (or delegated).
	Status string
	// Room marks a room or other resource, which is not a guest.
	Room bool
}

// Invitation is the text/calendar part of a meeting message: a REQUEST,
// CANCEL, REPLY or COUNTER. Whether an address is yours is decided by the
// caller (maildir cannot import compose, which imports it).
type Invitation struct {
	Method         string // upper case
	UID            string
	Summary        string
	Start, End     time.Time
	AllDay         bool
	Location       string
	Description    string
	OrganizerName  string
	OrganizerEmail string
	Attendees      []InviteAttendee
	Sequence       int
	Repeats        bool // the event has an RRULE
	// Link is a meeting address the invitation names outright (Teams'
	// X-MICROSOFT-SKYPETEAMSMEETINGURL, Google's conference property); links
	// written into the location or description are left for the caller.
	Link string
}

// Guests counts the people invited besides the organiser, rooms excluded.
func (i Invitation) Guests() int {
	count := 0
	for _, attendee := range i.Attendees {
		if attendee.Room || (i.OrganizerEmail != "" && attendee.Email == i.OrganizerEmail) {
			continue
		}
		count++
	}
	return count
}

// StatusOf is the answer of the first attendee isOwn accepts, or "" when
// none of them is.
func (i Invitation) StatusOf(isOwn func(address string) bool) string {
	for _, attendee := range i.Attendees {
		if isOwn(attendee.Email) {
			return attendee.Status
		}
	}
	return ""
}

// invitationMethods are the iTIP methods the reader shows a card for; other
// calendar files (a PUBLISHed schedule, say) stay ordinary attachments.
var invitationMethods = map[string]bool{"REQUEST": true, "CANCEL": true, "REPLY": true, "COUNTER": true}

const maxInviteDescription = 4000

type icsLine struct {
	name   string // upper case
	params map[string]string
	value  string
}

// parseInvitation reads an iCalendar body; it returns nil unless it holds an
// event under one of the invitation methods. methodHint is the Content-Type's
// method parameter, used when the calendar carries no METHOD line.
func parseInvitation(data, methodHint string) *Invitation {
	method := strings.ToUpper(strings.TrimSpace(methodHint))
	var events [][]icsLine
	var current []icsLine
	inEvent, inAlarm := false, false
	for _, raw := range unfoldICS(data) {
		line, ok := parseICSLine(raw)
		if !ok {
			continue
		}
		switch {
		case line.name == "BEGIN" && strings.EqualFold(line.value, "VEVENT"):
			inEvent, current = true, nil
		case line.name == "END" && strings.EqualFold(line.value, "VEVENT"):
			if inEvent {
				events = append(events, current)
			}
			inEvent, inAlarm = false, false
		case line.name == "BEGIN" && strings.EqualFold(line.value, "VALARM"):
			inAlarm = true
		case line.name == "END" && strings.EqualFold(line.value, "VALARM"):
			inAlarm = false
		case inEvent && !inAlarm:
			current = append(current, line)
		case !inEvent && line.name == "METHOD" && line.value != "":
			method = strings.ToUpper(strings.TrimSpace(line.value))
		}
	}
	if !invitationMethods[method] || len(events) == 0 {
		return nil
	}
	// A reply or update can carry one occurrence of a series (RECURRENCE-ID);
	// prefer the event itself when both are there.
	chosen := events[0]
	for _, event := range events {
		if !hasICSProperty(event, "RECURRENCE-ID") {
			chosen = event
			break
		}
	}
	return inviteFromEvent(method, chosen)
}

func hasICSProperty(lines []icsLine, name string) bool {
	for _, line := range lines {
		if line.name == name {
			return true
		}
	}
	return false
}

func icsProperty(lines []icsLine, name string) (icsLine, bool) {
	for _, line := range lines {
		if line.name == name {
			return line, true
		}
	}
	return icsLine{}, false
}

func icsText(lines []icsLine, name string) string {
	line, _ := icsProperty(lines, name)
	return unescapeICS(line.value)
}

func inviteFromEvent(method string, lines []icsLine) *Invitation {
	invitation := &Invitation{
		Method:      method,
		UID:         terminal.SanitizeLine(icsText(lines, "UID")),
		Summary:     terminal.SanitizeLine(icsText(lines, "SUMMARY")),
		Location:    terminal.SanitizeLine(icsText(lines, "LOCATION")),
		Description: terminal.Sanitize(truncateBytes(icsText(lines, "DESCRIPTION"), maxInviteDescription)),
		Repeats:     hasICSProperty(lines, "RRULE"),
	}
	invitation.Sequence, _ = strconv.Atoi(strings.TrimSpace(icsText(lines, "SEQUENCE")))

	startLine, hasStart := icsProperty(lines, "DTSTART")
	if !hasStart {
		return nil
	}
	start, allDay, ok := parseICSTime(startLine)
	if !ok {
		return nil
	}
	invitation.Start, invitation.AllDay = start, allDay
	if endLine, ok := icsProperty(lines, "DTEND"); ok {
		if end, _, ok := parseICSTime(endLine); ok && end.After(start) {
			invitation.End = end
		}
	}
	if invitation.End.IsZero() {
		if allDay {
			invitation.End = start.AddDate(0, 0, 1)
		} else {
			invitation.End = start.Add(time.Hour)
		}
	}

	if organizer, ok := icsProperty(lines, "ORGANIZER"); ok {
		invitation.OrganizerEmail = mailtoAddress(organizer.value)
		invitation.OrganizerName = terminal.SanitizeLine(organizer.params["CN"])
	}
	for _, line := range lines {
		if line.name != "ATTENDEE" {
			continue
		}
		status := strings.ToLower(strings.TrimSpace(line.params["PARTSTAT"]))
		if status == "" {
			status = "needs-action"
		}
		cutype := strings.ToUpper(line.params["CUTYPE"])
		invitation.Attendees = append(invitation.Attendees, InviteAttendee{
			Name:   terminal.SanitizeLine(line.params["CN"]),
			Email:  mailtoAddress(line.value),
			Status: status,
			Room:   cutype == "ROOM" || cutype == "RESOURCE",
		})
	}
	for _, name := range []string{"X-MICROSOFT-SKYPETEAMSMEETINGURL", "X-GOOGLE-CONFERENCE"} {
		if line, ok := icsProperty(lines, name); ok && isWebAddress(line.value) {
			invitation.Link = terminal.SanitizeLine(strings.TrimSpace(line.value))
			break
		}
	}
	return invitation
}

func isWebAddress(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(value, "https://") || strings.HasPrefix(value, "http://")
}

func mailtoAddress(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 7 && strings.EqualFold(value[:7], "mailto:") {
		value = value[7:]
	}
	return terminal.SanitizeLine(strings.ToLower(strings.TrimSpace(value)))
}

// parseICSTime reads a DTSTART or DTEND: UTC with a Z, in its TZID zone
// (Outlook's Windows names included), or floating, which means the machine's
// zone. A bare date is an all-day event.
func parseICSTime(line icsLine) (time.Time, bool, bool) {
	value := strings.TrimSpace(line.value)
	location := time.Local
	if zone := line.params["TZID"]; zone != "" {
		location, _ = calendar.LookupLocation(zone)
	}
	if strings.EqualFold(line.params["VALUE"], "DATE") || (len(value) == 8 && !strings.Contains(value, "T")) {
		parsed, err := time.ParseInLocation("20060102", value, location)
		return parsed, true, err == nil
	}
	if strings.HasSuffix(value, "Z") {
		for _, layout := range []string{"20060102T150405Z", "20060102T1504Z"} {
			if parsed, err := time.Parse(layout, value); err == nil {
				return parsed, false, true
			}
		}
		return time.Time{}, false, false
	}
	for _, layout := range []string{"20060102T150405", "20060102T1504"} {
		if parsed, err := time.ParseInLocation(layout, value, location); err == nil {
			return parsed, false, true
		}
	}
	return time.Time{}, false, false
}

func unfoldICS(data string) []string {
	var lines []string
	for _, raw := range strings.Split(normalizeNewlines(data), "\n") {
		if len(lines) > 0 && (strings.HasPrefix(raw, " ") || strings.HasPrefix(raw, "\t")) {
			lines[len(lines)-1] += raw[1:]
			continue
		}
		lines = append(lines, raw)
	}
	return lines
}

// parseICSLine splits NAME;PARAM=value:value, honouring quotes: Outlook
// writes CN="Roos, A." and TZID values can hold colons.
func parseICSLine(raw string) (icsLine, bool) {
	colon := indexOutsideQuotes(raw, ':')
	if colon <= 0 {
		return icsLine{}, false
	}
	head, value := raw[:colon], raw[colon+1:]
	pieces := splitOutsideQuotes(head, ';')
	line := icsLine{name: strings.ToUpper(strings.TrimSpace(pieces[0])), params: map[string]string{}, value: value}
	for _, piece := range pieces[1:] {
		key, parameter, found := strings.Cut(piece, "=")
		if !found {
			continue
		}
		line.params[strings.ToUpper(strings.TrimSpace(key))] = strings.Trim(strings.TrimSpace(parameter), `"`)
	}
	return line, true
}

func indexOutsideQuotes(text string, target byte) int {
	quoted := false
	for index := 0; index < len(text); index++ {
		switch {
		case text[index] == '"':
			quoted = !quoted
		case text[index] == target && !quoted:
			return index
		}
	}
	return -1
}

func splitOutsideQuotes(text string, separator byte) []string {
	var pieces []string
	for {
		index := indexOutsideQuotes(text, separator)
		if index < 0 {
			return append(pieces, text)
		}
		pieces = append(pieces, text[:index])
		text = text[index+1:]
	}
}

func unescapeICS(value string) string {
	if !strings.Contains(value, `\`) {
		return value
	}
	var out strings.Builder
	for index := 0; index < len(value); index++ {
		if value[index] != '\\' || index+1 == len(value) {
			out.WriteByte(value[index])
			continue
		}
		index++
		switch value[index] {
		case 'n', 'N':
			out.WriteByte('\n')
		default:
			out.WriteByte(value[index])
		}
	}
	return out.String()
}

func truncateBytes(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && text[cut]&0xC0 == 0x80 { // do not split a rune
		cut--
	}
	return text[:cut]
}

// isCalendarPart tells a text/calendar part, or a file that is one under
// another type (mail clients label .ics attachments application/ics,
// application/octet-stream, text/plain).
func isCalendarPart(mediaType, filename string) bool {
	switch strings.ToLower(mediaType) {
	case "text/calendar", "application/ics":
		return true
	}
	return strings.HasSuffix(strings.ToLower(filename), ".ics")
}
