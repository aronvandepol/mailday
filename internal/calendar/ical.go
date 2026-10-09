package calendar

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/teambition/rrule-go"

	"github.com/aronvandepol/mailday/internal/terminal"
)

const maxICSBytes = 16 << 20

type eventObject struct {
	Raw              string
	Source           Source
	FallbackSummary  string
	FallbackLocation string
}

type property struct {
	Name   string
	Params map[string]string
	Value  string
}

type rawEvent struct {
	Event
	Rule            string
	ExDates         []time.Time
	RDates          []time.Time
	RecurrenceID    *time.Time
	RecurrenceIsDay bool
	Duration        time.Duration
	Days            int // an all-day event's length in calendar days
	Canceled        bool
	// ZoneWarning names a TZID that is unknown here: the event was read as
	// local time.
	ZoneWarning string
}

func (s *Store) loadICSFiles() ([]eventObject, []Source, []string) {
	paths, err := filepath.Glob(filepath.Join(s.ICSRoot, "*.ics"))
	if err != nil {
		return nil, nil, []string{err.Error()}
	}
	var objects []eventObject
	var sources []Source
	var warnings []string
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if info.Size() > maxICSBytes {
			warnings = append(warnings, fmt.Sprintf("%s exceeds the ICS size limit", terminal.SanitizeLine(filepath.Base(path))))
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", terminal.SanitizeLine(filepath.Base(path)), err))
			continue
		}
		name := terminal.SanitizeLine(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
		source := Source{UID: path, Name: name, Backend: "ics", Provider: "ICS"}
		components := eventComponents(string(data))
		for _, component := range components {
			objects = append(objects, eventObject{Raw: component, Source: source})
		}
		sources = append(sources, source)
	}
	return objects, sources, warnings
}

func eventComponents(data string) []string {
	lines := unfoldLines(data)
	var components []string
	var current []string
	inside := false
	for _, line := range lines {
		switch strings.ToUpper(strings.TrimSpace(line)) {
		case "BEGIN:VEVENT":
			inside = true
			current = []string{line}
		case "END:VEVENT":
			if inside {
				current = append(current, line)
				components = append(components, strings.Join(current, "\n"))
			}
			inside = false
			current = nil
		default:
			if inside {
				current = append(current, line)
			}
		}
	}
	return components
}

func unfoldLines(data string) []string {
	data = strings.ReplaceAll(data, "\r\n", "\n")
	data = strings.ReplaceAll(data, "\r", "\n")
	raw := strings.Split(data, "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		if len(lines) > 0 && (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) {
			lines[len(lines)-1] += strings.TrimPrefix(strings.TrimPrefix(line, " "), "\t")
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

// ownLines drops the components nested in a VEVENT (VALARM, and a VTIMEZONE
// when the object is a whole VCALENDAR), whose DESCRIPTION, TRIGGER or
// DTSTART lines would otherwise be read as the event's own.
func ownLines(data string) string {
	var kept []string
	depth := 0
	for _, line := range unfoldLines(data) {
		upper := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(upper, "BEGIN:"):
			if name := strings.TrimPrefix(upper, "BEGIN:"); depth == 0 && (name == "VEVENT" || name == "VCALENDAR") {
				kept = append(kept, line)
				continue
			}
			depth++
		case strings.HasPrefix(upper, "END:"):
			if depth == 0 {
				kept = append(kept, line)
				continue
			}
			depth--
		case depth == 0:
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

func parseProperties(data string) map[string][]property {
	properties := make(map[string][]property)
	for _, line := range unfoldLines(data) {
		left, value, ok := splitPropertyLine(line)
		if !ok {
			continue
		}
		parts := splitOutsideQuotes(left, ';')
		name := strings.ToUpper(strings.TrimSpace(parts[0]))
		if name == "" {
			continue
		}
		item := property{Name: name, Params: make(map[string]string), Value: value}
		for _, rawParam := range parts[1:] {
			key, paramValue, ok := strings.Cut(rawParam, "=")
			if !ok {
				continue
			}
			item.Params[strings.ToUpper(strings.TrimSpace(key))] = strings.Trim(strings.TrimSpace(paramValue), "\"")
		}
		properties[name] = append(properties[name], item)
	}
	return properties
}

func splitPropertyLine(line string) (string, string, bool) {
	quoted := false
	for index, r := range line {
		switch r {
		case '"':
			quoted = !quoted
		case ':':
			if !quoted {
				return line[:index], line[index+1:], true
			}
		}
	}
	return "", "", false
}

func splitOutsideQuotes(value string, separator rune) []string {
	var parts []string
	start := 0
	quoted := false
	for index, r := range value {
		if r == '"' {
			quoted = !quoted
		}
		if r == separator && !quoted {
			parts = append(parts, value[start:index])
			start = index + 1
		}
	}
	return append(parts, value[start:])
}

func expandObjects(objects []eventObject, from, to time.Time) ([]Event, []string) {
	groups := make(map[string][]rawEvent)
	var warnings []string
	warnedZones := make(map[string]bool)
	for _, object := range objects {
		parsed, err := parseRawEvent(object)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("%s: %v", object.Source.Name, err))
			continue
		}
		if parsed.ZoneWarning != "" {
			message := fmt.Sprintf("%s: unknown time zone %q, read as local time", object.Source.Name, parsed.ZoneWarning)
			if !warnedZones[message] {
				warnedZones[message] = true
				warnings = append(warnings, message)
			}
		}
		key := object.Source.UID + "\x00" + parsed.UID
		groups[key] = append(groups[key], parsed)
	}

	// Map order is random: expand the groups in key order, so which copy of a
	// duplicated event survives does not change between refreshes.
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var events []Event
	for _, key := range keys {
		expanded, groupWarnings := expandGroup(groups[key], from, to)
		events = append(events, expanded...)
		warnings = append(warnings, groupWarnings...)
	}

	deduplicated := make([]Event, 0, len(events))
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].Start.Equal(events[j].Start) {
			if events[i].Summary != events[j].Summary {
				return events[i].Summary < events[j].Summary
			}
			if events[i].Source != events[j].Source {
				return events[i].Source < events[j].Source
			}
			return events[i].ID < events[j].ID
		}
		return events[i].Start.Before(events[j].Start)
	})
	// The same Google event can arrive twice, from Evolution's cache and from
	// the mailday-calsync ICS export. Key on UID and start across sources,
	// and keep the ICS copy, which the service account keeps fresh.
	position := make(map[string]int)
	for _, event := range events {
		key := event.UID + "\x00" + event.Start.UTC().Format(time.RFC3339Nano)
		if event.UID == "" {
			key = event.Source + "\x00" + event.Summary + "\x00" + event.Start.UTC().Format(time.RFC3339Nano)
		}
		if index, ok := position[key]; ok {
			if deduplicated[index].Provider != "ICS" && event.Provider == "ICS" {
				deduplicated[index] = event
			}
			continue
		}
		position[key] = len(deduplicated)
		deduplicated = append(deduplicated, event)
	}
	// Google and Exchange write timed events in UTC. Everything on screen and
	// every wall-clock edit (move to 16:00, +1d) works in local time.
	for index := range deduplicated {
		if !deduplicated[index].AllDay {
			deduplicated[index].Start = deduplicated[index].Start.In(time.Local)
			deduplicated[index].End = deduplicated[index].End.In(time.Local)
		}
	}
	return deduplicated, warnings
}

func parseRawEvent(object eventObject) (rawEvent, error) {
	properties := parseProperties(ownLines(object.Raw))
	startProperty, ok := firstProperty(properties, "DTSTART")
	if !ok {
		return rawEvent{}, fmt.Errorf("event has no start")
	}
	start, allDay, err := parseDateTime(startProperty)
	if err != nil {
		return rawEvent{}, fmt.Errorf("parse start: %w", err)
	}
	end := time.Time{}
	if endProperty, ok := firstProperty(properties, "DTEND"); ok {
		end, _, err = parseDateTime(endProperty)
		if err != nil {
			return rawEvent{}, fmt.Errorf("parse end: %w", err)
		}
	}
	duration := time.Hour
	if allDay {
		duration = 24 * time.Hour
	}
	hasEnd := !end.IsZero() && end.After(start)
	if hasEnd {
		duration = end.Sub(start)
	} else if durationProperty, ok := firstProperty(properties, "DURATION"); ok {
		if parsedDuration, err := parseICSDuration(durationProperty.Value); err == nil && parsedDuration > 0 {
			duration = parsedDuration
		}
	}
	// An all-day event spans calendar days, which are 23 or 25 hours long on
	// a daylight-saving change: count them, and add them as days.
	days := 0
	if allDay {
		days = max(int((duration+12*time.Hour)/(24*time.Hour)), 1)
	}
	if !hasEnd {
		if allDay {
			end = start.AddDate(0, 0, days)
		} else {
			end = start.Add(duration)
		}
	}

	uid := textProperty(properties, "UID")
	if uid == "" {
		hash := sha256.Sum256([]byte(object.Raw))
		uid = fmt.Sprintf("local-%x", hash[:8])
	}
	summary := textProperty(properties, "SUMMARY")
	if summary == "" {
		summary = object.FallbackSummary
	}
	if strings.TrimSpace(summary) == "" {
		summary = "(untitled event)"
	}
	location := textProperty(properties, "LOCATION")
	if location == "" {
		location = object.FallbackLocation
	}

	parsed := rawEvent{
		Event: Event{
			UID:         terminal.SanitizeLine(uid),
			Source:      object.Source.Name,
			Provider:    object.Source.Provider,
			Color:       object.Source.Color,
			Summary:     terminal.SanitizeLine(summary),
			Location:    terminal.SanitizeLine(location),
			Description: terminal.Sanitize(textProperty(properties, "DESCRIPTION")),
			Start:       start,
			End:         end,
			AllDay:      allDay,
			RemoteID:    terminal.SanitizeLine(textProperty(properties, "X-MAILDAY-ID")),
			Organizer:   terminal.SanitizeLine(textProperty(properties, "X-MAILDAY-ORGANIZER")),
			RSVP:        strings.ToLower(terminal.SanitizeLine(textProperty(properties, "X-MAILDAY-RSVP"))),
			Attendees:   terminal.SanitizeLine(textProperty(properties, "X-MAILDAY-ATTENDEES")),
			Series:      strings.EqualFold(strings.TrimSpace(propertyValue(properties, "X-MAILDAY-SERIES")), "TRUE"),
		},
		Duration: duration,
		Days:     days,
		Canceled: strings.EqualFold(strings.TrimSpace(propertyValue(properties, "STATUS")), "CANCELLED"),
	}
	parsed.Editable = parsed.RemoteID != "" && strings.EqualFold(strings.TrimSpace(propertyValue(properties, "X-MAILDAY-EDITABLE")), "TRUE")
	parsed.ID = parsed.UID + "/" + start.UTC().Format(time.RFC3339Nano)
	if zone := startProperty.Params["TZID"]; zone != "" {
		if _, known := lookupLocation(zone); !known {
			parsed.ZoneWarning = zone
		}
	}
	parsed.Rule = strings.TrimSpace(propertyValue(properties, "RRULE"))
	for _, exdate := range properties["EXDATE"] {
		values, _, err := parseDateList(exdate)
		if err == nil {
			parsed.ExDates = append(parsed.ExDates, values...)
		}
	}
	for _, rdate := range properties["RDATE"] {
		values, _, err := parseDateList(rdate)
		if err == nil {
			parsed.RDates = append(parsed.RDates, values...)
		}
	}
	if recurrence, ok := firstProperty(properties, "RECURRENCE-ID"); ok {
		value, isDay, err := parseDateTime(recurrence)
		if err == nil {
			parsed.RecurrenceID = &value
			parsed.RecurrenceIsDay = isDay
		}
	}
	return parsed, nil
}

func expandGroup(group []rawEvent, from, to time.Time) ([]Event, []string) {
	overrides := make(map[string]rawEvent)
	for _, item := range group {
		if item.RecurrenceID != nil {
			overrides[occurrenceKey(*item.RecurrenceID, item.RecurrenceIsDay)] = item
		}
	}
	consumed := make(map[string]bool)
	var events []Event
	var warnings []string
	for _, item := range group {
		if item.RecurrenceID != nil || item.Canceled {
			continue
		}
		if item.Rule == "" && len(item.RDates) == 0 {
			if overlaps(item.Start, item.End, from, to) {
				events = append(events, item.Event)
			}
			continue
		}
		starts := []time.Time{item.Start}
		if item.Rule != "" {
			options, err := rrule.StrToROptionInLocation(item.Rule, item.Start.Location())
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s recurrence: %v", item.Summary, err))
			} else if options.Freq == rrule.SECONDLY || options.Freq == rrule.MINUTELY {
				warnings = append(warnings, fmt.Sprintf("%s recurrence is too frequent to display", item.Summary))
			} else {
				options.Dtstart = item.Start
				rule, err := rrule.NewRRule(*options)
				if err != nil {
					warnings = append(warnings, fmt.Sprintf("%s recurrence: %v", item.Summary, err))
				} else {
					starts = rule.Between(from.Add(-item.Duration), to, true)
				}
			}
		}
		starts = append(starts, item.RDates...)
		excluded := make(map[string]bool)
		for _, value := range item.ExDates {
			excluded[occurrenceKey(value, item.AllDay)] = true
		}
		for _, start := range starts {
			key := occurrenceKey(start, item.AllDay)
			if excluded[key] {
				continue
			}
			if override, ok := overrides[key]; ok {
				consumed[key] = true
				if !override.Canceled && overlaps(override.Start, override.End, from, to) {
					events = append(events, override.Event)
				}
				continue
			}
			end := start.Add(item.Duration)
			if item.AllDay {
				end = start.AddDate(0, 0, item.Days)
			}
			if overlaps(start, end, from, to) {
				event := item.Event
				event.Start = start
				event.End = end
				event.ID = event.UID + "/" + start.UTC().Format(time.RFC3339Nano)
				events = append(events, event)
			}
		}
	}
	for key, override := range overrides {
		if consumed[key] || override.Canceled {
			continue
		}
		if overlaps(override.Start, override.End, from, to) {
			events = append(events, override.Event)
		}
	}
	return events, warnings
}

func firstProperty(properties map[string][]property, name string) (property, bool) {
	items := properties[name]
	if len(items) == 0 {
		return property{}, false
	}
	return items[0], true
}

func propertyValue(properties map[string][]property, name string) string {
	item, ok := firstProperty(properties, name)
	if !ok {
		return ""
	}
	return item.Value
}

func textProperty(properties map[string][]property, name string) string {
	return unescapeICSText(propertyValue(properties, name))
}

func unescapeICSText(value string) string {
	var b strings.Builder
	for index := 0; index < len(value); index++ {
		if value[index] != '\\' || index+1 >= len(value) {
			b.WriteByte(value[index])
			continue
		}
		index++
		switch value[index] {
		case 'n', 'N':
			b.WriteByte('\n')
		case '\\', ',', ';':
			b.WriteByte(value[index])
		default:
			b.WriteByte(value[index])
		}
	}
	return b.String()
}

func parseDateList(item property) ([]time.Time, bool, error) {
	values := strings.Split(item.Value, ",")
	parsed := make([]time.Time, 0, len(values))
	allDay := false
	for _, value := range values {
		copy := item
		copy.Value = value
		date, isDay, err := parseDateTime(copy)
		if err != nil {
			return nil, false, err
		}
		allDay = allDay || isDay
		parsed = append(parsed, date)
	}
	return parsed, allDay, nil
}

func parseDateTime(item property) (time.Time, bool, error) {
	value := strings.TrimSpace(item.Value)
	allDay := strings.EqualFold(item.Params["VALUE"], "DATE") || (len(value) == 8 && !strings.Contains(value, "T"))
	location := time.Local
	if timezone := item.Params["TZID"]; timezone != "" {
		location = loadLocation(timezone)
	}
	if allDay {
		parsed, err := time.ParseInLocation("20060102", value, location)
		return parsed, true, err
	}
	layouts := []string{"20060102T150405Z", "20060102T1504Z"}
	if strings.HasSuffix(value, "Z") {
		for _, layout := range layouts {
			if parsed, err := time.Parse(layout, value); err == nil {
				return parsed, false, nil
			}
		}
	}
	for _, layout := range []string{"20060102T150405", "20060102T1504"} {
		if parsed, err := time.ParseInLocation(layout, value, location); err == nil {
			return parsed, false, nil
		}
	}
	return time.Time{}, false, fmt.Errorf("unsupported date %q", terminal.SanitizeLine(value))
}

// zoneAliases maps the Windows zone names Outlook and Exchange write in TZID
// to IANA names, keyed in lower case.
var zoneAliases = map[string]string{
	"utc":                            "UTC",
	"etc/utc":                        "UTC",
	"w. europe standard time":        "Europe/Amsterdam",
	"central europe standard time":   "Europe/Budapest",
	"romance standard time":          "Europe/Paris",
	"central european standard time": "Europe/Warsaw",
	"gmt standard time":              "Europe/London",
	"korea standard time":            "Asia/Seoul",
	"tokyo standard time":            "Asia/Tokyo",
	"china standard time":            "Asia/Shanghai",
	"india standard time":            "Asia/Kolkata",
	"eastern standard time":          "America/New_York",
	"central standard time":          "America/Chicago",
	"mountain standard time":         "America/Denver",
	"pacific standard time":          "America/Los_Angeles",
}

// lookupLocation resolves a TZID; known is false when it is neither an IANA
// name (with or without a "/freeassociation.sourceforge.net/Tzfile/" style
// prefix) nor a Windows name.
func lookupLocation(name string) (*time.Location, bool) {
	name = strings.TrimSpace(name)
	if _, rest, found := strings.Cut(name, "/Tzfile/"); found {
		name = rest
	}
	if alias, ok := zoneAliases[strings.ToLower(name)]; ok {
		name = alias
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return time.Local, false
	}
	return location, true
}

func loadLocation(name string) *time.Location {
	location, _ := lookupLocation(name)
	return location
}

func parseICSDuration(value string) (time.Duration, error) {
	value = strings.TrimSpace(strings.ToUpper(value))
	if value == "" || value[0] != 'P' {
		return 0, fmt.Errorf("invalid duration")
	}
	value = value[1:]
	inTime := false
	number := ""
	var duration time.Duration
	for _, r := range value {
		if r == 'T' {
			inTime = true
			continue
		}
		if r >= '0' && r <= '9' {
			number += string(r)
			continue
		}
		if number == "" {
			return 0, fmt.Errorf("invalid duration")
		}
		amount, err := strconv.Atoi(number)
		if err != nil {
			return 0, err
		}
		number = ""
		switch r {
		case 'W':
			duration += time.Duration(amount) * 7 * 24 * time.Hour
		case 'D':
			duration += time.Duration(amount) * 24 * time.Hour
		case 'H':
			if !inTime {
				return 0, fmt.Errorf("invalid duration")
			}
			duration += time.Duration(amount) * time.Hour
		case 'M':
			if !inTime {
				return 0, fmt.Errorf("month durations are unsupported")
			}
			duration += time.Duration(amount) * time.Minute
		case 'S':
			duration += time.Duration(amount) * time.Second
		default:
			return 0, fmt.Errorf("invalid duration")
		}
	}
	if number != "" {
		return 0, fmt.Errorf("invalid duration")
	}
	return duration, nil
}

func occurrenceKey(value time.Time, allDay bool) string {
	if allDay {
		return value.Format("20060102")
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func overlaps(start, end, from, to time.Time) bool {
	return start.Before(to) && end.After(from)
}
