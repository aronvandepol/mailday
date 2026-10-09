package tui

// The quick-add box: n opens it over the calendar, and as you type each word
// is coloured by what it was read as (date, time, length, repeat, place,
// calendar, people), while the fields below fill in with the same colours.
// Fields you did not give show the default that will be used, muted. Enter
// adds the event, tab opens the full form with everything filled in.

import (
	"image/color"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/aronvandepol/mailday/internal/when"
)

// roleHue is the colour of a kind of word, from the theme's event hues.
func roleHue(role string) color.Color {
	hue := map[string]string{
		"date": "blue", "allday": "blue", "time": "green", "shift": "green", "free": "green",
		"length": "yellow", "repeat": "magenta", "location": "cyan", "calendar": "red",
	}[role]
	if role == "people" {
		return colorPrimary // the six event hues are taken; length already uses the accent
	}
	if hue == "" {
		return nil
	}
	if value := themeEventHues[hue]; value != nil {
		return value
	}
	return map[string]color.Color{"blue": lipgloss.Blue, "green": lipgloss.Green, "yellow": lipgloss.Yellow,
		"magenta": lipgloss.Magenta, "cyan": lipgloss.Cyan, "red": lipgloss.Red}[hue]
}

func roleStyle(role string) lipgloss.Style {
	switch role {
	case "title":
		return lipgloss.NewStyle().Foreground(colorBright)
	case "filler":
		return styleMuted
	case "error":
		return lipgloss.NewStyle().Foreground(colorError).Underline(true)
	}
	if hue := roleHue(role); hue != nil && !noColor {
		return lipgloss.NewStyle().Foreground(hue).Bold(true)
	}
	return lipgloss.NewStyle().Bold(true)
}

// readAs lists the words read as something else, coloured by what they
// became: "read as  tom → tomorrow · 2p → 14:00".
func (m Model) readAs(input string) string {
	rest, _ := splitPeople(input)
	parsed, _ := when.Parse(rest, m.now())
	if len(parsed.Notes) == 0 {
		return ""
	}
	words, roles := m.inputRoles(rest)
	roleOfWord := map[string]string{}
	for index, word := range words {
		roleOfWord[strings.Trim(word, ",.;!?")] = roles[index]
	}
	var parts []string
	for _, note := range parsed.Notes {
		parts = append(parts, styleMuted.Render(note.Word+" → ")+roleStyle(roleOfWord[note.Word]).Render(note.Meaning))
	}
	return styleMuted.Render("read as  ") + strings.Join(parts, styleMuted.Render(" · "))
}

// inputRoles reads the quick-add line word by word, people included.
func (m Model) inputRoles(input string) (words, roles []string) {
	rest, _ := splitPeople(input)
	parsed, _ := when.Parse(rest, m.now())
	next := 0
	for _, word := range strings.Fields(input) {
		words = append(words, word)
		if len(word) > 1 && word[0] == '+' && (word[1] < '0' || word[1] > '9') {
			if _, _, err := m.resolveInvitees(word[1:]); err != nil {
				roles = append(roles, "error")
			} else {
				roles = append(roles, "people")
			}
			continue
		}
		role := "title"
		if next < len(parsed.Roles) {
			role = parsed.Roles[next]
		}
		next++
		roles = append(roles, role)
	}
	return words, roles
}

func (m Model) renderQuickAdd(width int) string {
	prompt := m.calPrompt
	inner := width - 4
	accent := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
	label := func(text, role string, given bool) string {
		style := styleMuted
		if given {
			style = roleStyle(role)
		}
		return style.Render(padTo(text, 10))
	}
	value := func(text string, role string, given bool) string {
		if !given {
			return styleMuted.Render(text)
		}
		return roleStyle(role).Render(text)
	}

	// The line as typed, coloured by what each word became.
	words, roles := m.inputRoles(prompt.input)
	var typed []string
	for index, word := range words {
		typed = append(typed, roleStyle(roles[index]).Render(word))
	}
	line := strings.Join(typed, " ")
	if strings.HasSuffix(prompt.input, " ") && line != "" {
		line += " "
	}
	lines := []string{
		accent.Render("New event"),
		"",
		accent.Render("› ") + line + lipgloss.NewStyle().Foreground(colorPrimary).Render("█"),
	}
	if note := m.readAs(prompt.input); note != "" {
		lines = append(lines, "  "+note)
	}
	lines = append(lines, "")

	rest, people := splitPeople(prompt.input)
	parsed, parseErr := when.Parse(rest, m.now())
	// People are checked on their own row, so a typo in a name does not
	// hide the time.
	event, _, draftErr := m.draftEvent(rest)

	title := event.Summary
	if strings.TrimSpace(parsed.Title) == "" {
		title = "what it is: the words that are not a time, place or person"
	}
	lines = append(lines, label("Title", "title", parsed.Title != "")+value(truncateToWidth(title, inner-10), "title", parsed.Title != ""))

	if draftErr == nil || parsed.Title == "" {
		var parts []string
		start, end := event.Start, event.End
		if parsed.Title == "" {
			// Show the times even before there is a title.
			parsed.Title = "x"
			start, end, _ = when.Event(parsed, m.quickAddDay(), m.now())
			parsed.Title = ""
		}
		day := start.Local().Format("Mon 2 Jan")
		if event.AllDay || parsed.AllDay {
			parts = append(parts, value(day, "date", parsed.HasDate), value("all day", "allday", true))
		} else {
			parts = append(parts, value(day, "date", parsed.HasDate || parsed.Shift != 0 || parsed.Repeat != nil),
				value(start.Local().Format("15:04")+"–"+end.Local().Format("15:04"), "time", parsed.HasTime),
				value("("+lengthLabel(end.Sub(start))+")", "length", parsed.HasEnd || parsed.Length > 0))
		}
		whenLine := strings.Join(parts, "  ")
		if rolledToTomorrow(parsed, start, m.quickAddDay()) {
			whenLine += styleMuted.Render("   after 23:30 the default is tomorrow at 9:00")
		} else if !parsed.HasDate && !parsed.HasTime {
			whenLine += styleMuted.Render("   default: the selected day, next half hour or 9:00, 1 h")
		}
		lines = append(lines, label("When", "date", parsed.HasDate || parsed.HasTime)+whenLine)
	} else {
		lines = append(lines, label("When", "error", true)+roleStyle("error").Render(draftErr.Error()))
	}

	calendarName, calErr := m.matchCalendar(parsed.Calendar)
	calendarText := calendarName
	if service := m.serviceLabel(calendarName); service != "" {
		calendarText += " · " + service
	}
	if calErr != nil {
		lines = append(lines, label("Calendar", "error", true)+roleStyle("error").Render(calErr.Error()))
	} else if parsed.Calendar == "" {
		lines = append(lines, label("Calendar", "calendar", false)+styleMuted.Render(calendarText+"   #name picks another · shift+tab cycles"))
	} else {
		lines = append(lines, label("Calendar", "calendar", true)+value(calendarText, "calendar", true))
	}

	if parsed.Location != "" {
		lines = append(lines, label("Where", "location", true)+value(truncateToWidth(parsed.Location, inner-10), "location", true))
	} else {
		lines = append(lines, label("Where", "location", false)+styleMuted.Render("@place"))
	}

	if len(people) > 0 {
		_, names, err := m.resolveInvitees(strings.Join(people, ","))
		if err != nil {
			lines = append(lines, label("Invite", "error", true)+roleStyle("error").Render(err.Error()))
		} else {
			lines = append(lines, label("Invite", "people", true)+value(truncateToWidth(strings.Join(names, ", "), inner-10), "people", true))
		}
	} else {
		lines = append(lines, label("Invite", "people", false)+styleMuted.Render("+name from the address book"))
	}

	if parsed.Repeat != nil {
		lines = append(lines, label("Repeats", "repeat", true)+value("↻ "+parsed.Repeat.Describe(), "repeat", true))
	} else {
		lines = append(lines, label("Repeats", "repeat", false)+styleMuted.Render("every mon · weekdays · daily until 20 dec"))
	}

	if free, _, names := m.wantsFree(prompt.input); free {
		lines = append(lines, "", roleStyle("free").Render("next free: the first time you and "+strings.Join(names, ", ")+" are free (Exchange, on enter)"))
	} else if draftErr == nil {
		if clash := m.clashNote(event, event.Start, event.End, event.AllDay); clash != "" {
			lines = append(lines, "", lipgloss.NewStyle().Foreground(colorError).Render("⚠ "+clash))
		}
	}
	if parseErr != nil && draftErr == nil {
		lines = append(lines, "", roleStyle("error").Render(parseErr.Error()))
	}

	lines = append(lines, "", styleMuted.Render("enter add · tab all fields · shift+tab calendar · esc cancel"))
	for index, text := range lines {
		lines[index] = padTo(truncateToWidth(text, inner), inner)
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorPrimary).Padding(0, 1)
	return box.Render(strings.Join(lines, "\n"))
}

// rolledToTomorrow is true when a line with no date or time was booked on the
// day after the one it would land on: it was typed after 23:30 on that day,
// so when.Event chose 9:00 the next morning.
func rolledToTomorrow(parsed when.Parsed, start, day time.Time) bool {
	return !parsed.HasDate && !parsed.HasTime && !parsed.AllDay && parsed.Shift == 0 && parsed.Repeat == nil && !sameDay(start.Local(), day)
}

// quickAddDay is the day a new event lands on without a date.
func (m Model) quickAddDay() (day time.Time) {
	day = m.calendarDate()
	if event, ok := m.selectedEvent(); ok && m.calendarMode != calendarAgenda {
		day = dayStart(event.Start)
	}
	return day
}

// renderRebookBox shows a rebook: the phrase coloured like the quick-add
// line, and the event's time before and after.
func (m Model) renderRebookBox(width int) string {
	prompt := m.calPrompt
	inner := width - 4
	accent := lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
	words, roles := m.inputRoles(prompt.input)
	var typed []string
	for index, word := range words {
		role := roles[index]
		if role == "title" {
			role = "error" // a rebook phrase has no title words
		}
		typed = append(typed, roleStyle(role).Render(word))
	}
	line := strings.Join(typed, " ")
	if strings.HasSuffix(prompt.input, " ") && line != "" {
		line += " "
	}
	event := prompt.event
	lines := []string{
		accent.Render("Rebook ") + lipgloss.NewStyle().Foreground(colorBright).Bold(true).Render(truncateToWidth(event.Summary, inner-8)),
		"",
		accent.Render("› ") + line + lipgloss.NewStyle().Foreground(colorPrimary).Render("█"),
	}
	if note := m.readAs(prompt.input); note != "" {
		lines = append(lines, "  "+note)
	}
	lines = append(lines, "", styleMuted.Render(padTo("Now", 10))+describeSpan(event.Start, event.End, event.AllDay))
	if free, _, names := m.wantsFree(prompt.input); free {
		lines = append(lines, roleStyle("free").Render(padTo("To", 10))+roleStyle("free").Render("the first time you and "+strings.Join(names, ", ")+" are free"))
	} else if strings.TrimSpace(prompt.input) == "" {
		lines = append(lines, styleMuted.Render(padTo("To", 10)+"fri · 14:00 · fri 10:00 · +1d · -30m · 14:00-15:30 · 2h · next free"))
	} else if start, end, err := m.movedTimes(event, prompt.input); err != nil {
		lines = append(lines, roleStyle("error").Render(padTo("To", 10)+err.Error()))
	} else {
		lines = append(lines, accent.Render(padTo("To", 10))+lipgloss.NewStyle().Foreground(colorBright).Bold(true).Render(describeSpan(start, end, event.AllDay)))
		if clash := m.clashNote(event, start, end, event.AllDay); clash != "" {
			lines = append(lines, "", lipgloss.NewStyle().Foreground(colorError).Render("⚠ "+clash))
		}
	}
	lines = append(lines, "", styleMuted.Render("enter move · esc cancel"))
	for index, text := range lines {
		lines[index] = padTo(truncateToWidth(text, inner), inner)
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorPrimary).Padding(0, 1)
	return box.Render(strings.Join(lines, "\n"))
}

// quickAddToForm opens the full form with what the line said so far.
func (m Model) quickAddToForm() Model {
	input := m.calPrompt.input
	words, roles := m.inputRoles(input)
	rest, people := splitPeople(input)
	parsed, _ := when.Parse(rest, m.now())
	var timeWords []string
	for index, word := range words {
		switch roles[index] {
		case "date", "time", "length", "repeat", "allday", "shift", "free", "filler":
			timeWords = append(timeWords, word)
		}
	}
	m.calPrompt = nil
	m = m.openEventForm(false)
	if m.eventForm == nil {
		return m
	}
	form := m.eventForm
	form.title = parsed.Title
	form.when = strings.Join(timeWords, " ")
	form.location = parsed.Location
	form.invite = strings.Join(people, ", ")
	if name, err := m.matchCalendar(parsed.Calendar); err == nil && name != "" {
		form.calendar = name
	}
	form.field = formTitle
	if form.title != "" {
		form.field = formWhen
	}
	return m
}

// overlay draws box over base, centred across and a third of the way down.
func overlay(base, box string, width, height int) string {
	lines := strings.Split(base, "\n")
	for len(lines) < height {
		lines = append(lines, "")
	}
	boxLines := strings.Split(box, "\n")
	boxWidth := 0
	for _, line := range boxLines {
		boxWidth = max(boxWidth, ansi.StringWidth(line))
	}
	left := max((width-boxWidth)/2, 0)
	top := max((height-len(boxLines))/3, 0)
	for index, line := range boxLines {
		row := top + index
		if row >= len(lines) {
			break
		}
		under := lines[row]
		before := padTo(ansi.Truncate(under, left, ""), left)
		after := ansi.TruncateLeft(under, left+boxWidth, "")
		lines[row] = before + "\x1b[0m" + padTo(line, boxWidth) + "\x1b[0m" + after
	}
	return strings.Join(lines, "\n")
}
