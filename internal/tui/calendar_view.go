package tui

// The week and day views are a time grid: hours down the side, events as
// coloured blocks sized to their length, overlapping events side by side,
// and a line at the current time. The layout started from
// basecamp/hey-cli/internal/tui/calendar_views.go (MIT).

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/aronvandepol/mailday/internal/calendar"
)

const (
	weekRule    = '┊'
	gutterWidth = 6
)

// firstWeekday starts weeks on Monday, as calendars in the Netherlands do.
const firstWeekday = time.Monday

type weekDayInfo struct {
	date   time.Time
	events []calendar.Event
	allDay []calendar.Event
}

func (m Model) renderCalendar(width, height int) string {
	if m.loadingCal && len(m.events) == 0 {
		return styleMuted.Render("  Reading calendars...")
	}
	switch m.calendarMode {
	case calendarDay:
		return m.renderDayCalendar(width, height)
	case calendarWeek:
		return m.renderWeekCalendar(width, height)
	default:
		return m.renderCalendarAgenda(width, height)
	}
}

func calendarHint(base string) string {
	return base + " · n new · m rebook"
}

func (m Model) renderWeekCalendar(width, height int) string {
	start := weekStartDate(m.calendarDate(), firstWeekday)
	days := make([]time.Time, 7)
	for index := range days {
		days[index] = start.AddDate(0, 0, index)
	}
	if (width-gutterWidth)/7 < 9 {
		return m.renderCalendarAgenda(width, height)
	}
	header := hintedSectionHeader(weekLabel(start), calendarHint("←/→ week · t today"), width)
	return header + "\n" + m.renderGrid(days, width, height-1, false)
}

func (m Model) renderDayCalendar(width, height int) string {
	day := m.calendarDate()
	label := day.Format("Monday, January 2")
	if sameDay(day, m.now()) {
		label = "Today · " + label
	}
	header := hintedSectionHeader(label, calendarHint("←/→ day · t today"), width)
	if width < 90 {
		// No panel: the snoozed mail goes in a few lines under the grid.
		mail := m.dayMailLines(width, min(3, max(height-8, 0)))
		return header + "\n" + m.renderGrid([]time.Time{day}, width, height-1-len(mail), true) + strings.Repeat("\n", min(len(mail), 1)) + strings.Join(mail, "\n")
	}
	// A detail panel beside the grid.
	panelWidth := min(max(width/3, 34), 60)
	gridWidth := width - panelWidth - 3
	grid := strings.Split(m.renderGrid([]time.Time{day}, gridWidth, height-1, true), "\n")
	panel := m.dayPanel(day, panelWidth, height-1)
	divider := lipgloss.NewStyle().Foreground(colorChrome).Render(" │ ")
	var output strings.Builder
	output.WriteString(header)
	for row := 0; row < height-1; row++ {
		left, right := "", ""
		if row < len(grid) {
			left = grid[row]
		}
		if row < len(panel) {
			right = panel[row]
		}
		output.WriteString("\n" + padTo(left, gridWidth) + divider + right)
	}
	return output.String()
}

// dayPanel shows the selected event, or the next one, in full.
func (m Model) dayPanel(day time.Time, width, height int) []string {
	if u, ok := m.selectedUpcoming(); ok {
		return m.mailPanel(u, width)
	}
	event, ok := m.selectedEvent()
	if !ok || !eventOverlapsDay(event, day) {
		for _, candidate := range m.events {
			if eventOverlapsDay(candidate, day) && candidate.End.After(m.now()) {
				event, ok = candidate, true
				break
			}
		}
	}
	if !ok {
		free := lipgloss.NewStyle().Foreground(colorChrome).Render("Nothing planned.")
		return append([]string{"", free, "", styleMuted.Render("n adds an event: \"Lunch 12:30 1h @Atrium\"")}, m.mailListLines(width)...)
	}
	title := lipgloss.NewStyle().Foreground(colorBright).Bold(true)
	label := lipgloss.NewStyle().Foreground(colorChrome)
	dot := lipgloss.NewStyle().Foreground(eventFill(event)).Render("●")
	lines := []string{"", title.Render(truncateToWidth(event.Summary, width))}
	span := describeSpan(event.Start, event.End, event.AllDay)
	if home := homeSpan(event); home != "" {
		// Beside the local time when it fits the panel, else on a line of its own.
		if displayWidth(span)+3+displayWidth(home) <= width {
			span += " · " + home
		} else {
			lines = append(lines, label.Render(span))
			span = truncateToWidth(home, width)
		}
	}
	lines = append(lines, label.Render(span))
	if !event.AllDay {
		lines = append(lines, styleMuted.Render(lengthLabel(event.End.Sub(event.Start))+" · "+relativeLabel(event, m.now())))
	}
	lines = append(lines, "", dot+" "+event.Source)
	if event.Location != "" {
		lines = append(lines, "@ "+truncateToWidth(event.Location, width-2))
	}
	if link := meetingLink(event); link != "" {
		lines = append(lines, lipgloss.NewStyle().Foreground(colorActive).Bold(true).Render("J")+" join "+styleMuted.Render(truncateToWidth(link, width-7)))
	}
	if event.Attendees != "" {
		for _, line := range wrapText("with "+event.Attendees, width) {
			lines = append(lines, styleMuted.Render(line))
		}
	}
	switch {
	case event.Pending:
		lines = append(lines, styleMuted.Render("saving…"))
	case event.Organizer != "":
		lines = append(lines, "", styleMuted.Render("invitation from "+event.Organizer))
		lines = append(lines, rsvpLine(event))
	case event.Editable && m.canWrite(event.Source):
		if event.Series {
			lines = append(lines, styleMuted.Render("↻ repeats · changes apply to this one · D D deletes all"))
		}
		lines = append(lines, styleMuted.Render("e edit · m rebook · < > day · + - 30m · d delete"))
	}
	lines = append(lines, m.mailListLines(width)...)
	if notes := strings.TrimSpace(event.Description); notes != "" {
		lines = append(lines, "", label.Render("Notes"))
		for _, line := range wrapText(notes, width) {
			if len(lines) >= height {
				break
			}
			lines = append(lines, line)
		}
	}
	return lines
}

// rsvpMark puts your answer in front of an invitation's title.
func rsvpMark(event calendar.Event) string {
	switch event.RSVP {
	case "needs-action":
		return "? "
	case "tentative":
		return "~ "
	case "declined":
		return "✕ "
	}
	return ""
}

func rsvpLine(event calendar.Event) string {
	keys := "a accept · ~ maybe · x decline"
	switch event.RSVP {
	case "needs-action":
		return lipgloss.NewStyle().Foreground(colorActive).Bold(true).Render("not answered yet") + styleMuted.Render(" · "+keys)
	case "accepted":
		return styleMuted.Render("you accepted · " + keys)
	case "tentative":
		return styleMuted.Render("you said maybe · " + keys)
	case "declined":
		return styleMuted.Render("you declined · " + keys)
	}
	return styleMuted.Render(keys)
}

func lengthLabel(length time.Duration) string {
	hours, minutes := int(length.Hours()), int(length.Minutes())%60
	switch {
	case hours == 0:
		return fmt.Sprintf("%d min", minutes)
	case minutes == 0:
		return fmt.Sprintf("%d h", hours)
	default:
		return fmt.Sprintf("%d h %d min", hours, minutes)
	}
}

func relativeLabel(event calendar.Event, now time.Time) string {
	switch {
	case event.End.Before(now):
		return "over"
	case event.Start.Before(now):
		return "now, ends in " + roughDuration(event.End.Sub(now))
	default:
		return "in " + roughDuration(event.Start.Sub(now))
	}
}

func roughDuration(value time.Duration) string {
	switch {
	case value < time.Minute:
		return "a moment"
	case value < time.Hour:
		return fmt.Sprintf("%d min", int(value.Minutes()))
	case value < 24*time.Hour:
		hours, minutes := int(value.Hours()), int(value.Minutes())%60
		if minutes == 0 || hours >= 5 {
			return fmt.Sprintf("%d h", hours)
		}
		return fmt.Sprintf("%d h %d min", hours, minutes)
	default:
		return fmt.Sprintf("%d days", int(value.Hours()/24+0.5))
	}
}

// gridEvent is an event placed in a day column.
type gridEvent struct {
	event                  calendar.Event
	startRow, endRow       int
	lane, lanes            int
	clippedTop, clippedEnd bool
}

// gridScale picks the visible hours and minutes per row: 08:00–18:00 at
// least, every event of the shown days, the finest step that fits, and then
// more hours if rows are left.
func gridScale(days []time.Time, events []calendar.Event, now time.Time, rows int) (startHour, endHour int, step time.Duration) {
	startHour, endHour = 8, 18
	for _, day := range days {
		for _, event := range events {
			if event.AllDay || !eventOverlapsDay(event, day) {
				continue
			}
			from, to := clampToDay(event, day)
			startHour = min(startHour, from.Hour())
			last := to.Hour()
			if to.Minute() > 0 || to.Second() > 0 {
				last++
			}
			if !sameDay(from, to) {
				last = 24
			}
			endHour = max(endHour, last)
		}
		if sameDay(day, now) {
			startHour = min(startHour, now.Hour())
			endHour = max(endHour, now.Hour()+1)
		}
	}
	endHour = min(endHour, 24)
	rows = max(rows, 1)
	for _, candidate := range []time.Duration{15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour} {
		step = candidate
		if (endHour-startHour)*int(time.Hour/step) <= rows {
			break
		}
	}
	perHour := max(int(time.Hour/step), 1)
	if step > time.Hour {
		startHour -= startHour % 2
	}
	// Fill spare rows with the hours just outside the day.
	for {
		used := (endHour - startHour) * perHour
		if step > time.Hour {
			used = (endHour - startHour) / 2
		}
		grow := perHour
		if step > time.Hour {
			grow = 1
		}
		later, earlier := func() {
			endHour++
			if step > time.Hour {
				endHour++
			}
		}, func() {
			startHour--
			if step > time.Hour {
				startHour--
			}
		}
		switch {
		case used+grow > rows:
			return startHour, endHour, step
		case endHour < 22:
			later()
		case startHour > 6:
			earlier()
		case endHour < 24:
			later()
		case startHour > 0:
			earlier()
		default:
			return startHour, endHour, step
		}
	}
}

func clampToDay(event calendar.Event, day time.Time) (time.Time, time.Time) {
	from, to := event.Start.Local(), event.End.Local()
	dayFrom := dayStart(day)
	dayTo := dayFrom.AddDate(0, 0, 1)
	if from.Before(dayFrom) {
		from = dayFrom
	}
	if to.After(dayTo) {
		to = dayTo
	}
	return from, to
}

// layoutDay places a day's timed events in rows and side-by-side lanes.
func layoutDay(events []calendar.Event, day time.Time, top time.Time, step time.Duration, rows int) []gridEvent {
	var placed []gridEvent
	for _, event := range events {
		if event.AllDay || !eventOverlapsDay(event, day) {
			continue
		}
		from, to := clampToDay(event, day)
		item := gridEvent{event: event}
		item.startRow = int(from.Sub(top) / step)
		item.endRow = int((to.Sub(top) + step - 1) / step)
		if item.endRow <= item.startRow {
			item.endRow = item.startRow + 1
		}
		if item.startRow < 0 {
			item.startRow, item.clippedTop = 0, true
		}
		if item.endRow > rows {
			item.endRow, item.clippedEnd = rows, true
		}
		if item.startRow >= rows || item.endRow <= 0 {
			continue
		}
		placed = append(placed, item)
	}
	sort.SliceStable(placed, func(i, j int) bool {
		if placed[i].startRow != placed[j].startRow {
			return placed[i].startRow < placed[j].startRow
		}
		return placed[i].endRow > placed[j].endRow
	})
	// Greedy lanes within each cluster of overlapping events.
	clusterStart, clusterEnd := 0, -1
	var laneEnds []int
	closeCluster := func(upTo int) {
		for index := clusterStart; index < upTo; index++ {
			placed[index].lanes = max(len(laneEnds), 1)
		}
	}
	for index := range placed {
		if placed[index].startRow >= clusterEnd {
			closeCluster(index)
			clusterStart, laneEnds = index, nil
		}
		lane := 0
		for ; lane < len(laneEnds); lane++ {
			if laneEnds[lane] <= placed[index].startRow {
				break
			}
		}
		if lane == len(laneEnds) {
			laneEnds = append(laneEnds, 0)
		}
		laneEnds[lane] = placed[index].endRow
		placed[index].lane = lane
		clusterEnd = max(clusterEnd, placed[index].endRow)
	}
	closeCluster(len(placed))
	return placed
}

// renderGrid draws days side by side: a header row, the all-day band, and
// the hours.
func (m Model) renderGrid(days []time.Time, width, height int, wide bool) string {
	columns := len(days)
	columnWidth := (width - gutterWidth - (columns - 1)) / columns
	chrome := lipgloss.NewStyle().Foreground(colorChrome)
	separator := chrome.Render(string(weekRule))
	now := m.now()
	selected, hasSelected := m.selectedEvent()

	var lines []string
	// Day labels.
	if columns > 1 {
		var header strings.Builder
		header.WriteString(strings.Repeat(" ", gutterWidth))
		for index, day := range days {
			if index > 0 {
				header.WriteString(" ")
			}
			style := chrome
			switch {
			case sameDay(day, now):
				style = lipgloss.NewStyle().Foreground(colorActive).Bold(true)
			case hasSelected && sameDay(day, selected.Start):
				style = lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
			case day.Weekday() == time.Saturday || day.Weekday() == time.Sunday:
				style = styleMuted
			}
			label := weekDayColumnLabel(day, index == 0)
			if sameDay(day, now) {
				label = "● " + label
			}
			header.WriteString(style.Render(centerPad(label, columnWidth)))
		}
		lines = append(lines, header.String())
	}

	// All-day band.
	infos := make([]weekDayInfo, columns)
	for index, day := range days {
		infos[index].date = day
		for _, event := range m.events {
			if event.AllDay && eventOverlapsDay(event, day) {
				infos[index].allDay = append(infos[index].allDay, event)
			}
		}
	}
	band := m.allDayBand(infos, columnWidth)
	for row, cells := range band {
		gutter := strings.Repeat(" ", gutterWidth)
		if row == 0 {
			gutter = styleMuted.Render(padTo("all", gutterWidth))
		}
		lines = append(lines, gutter+strings.Join(padCells(cells, columnWidth), " "))
	}
	if len(band) > 0 {
		lines = append(lines, chrome.Render(strings.Repeat("┄", width)))
	}

	rows := max(height-len(lines), 3)
	startHour, endHour, step := gridScale(days, m.events, now, rows)
	top := func(day time.Time) time.Time { return dayStart(day).Add(time.Duration(startHour) * time.Hour) }
	visibleRows := int(time.Duration(endHour-startHour) * time.Hour / step)
	rows = min(rows, max(visibleRows, 1))

	layouts := make([][]gridEvent, columns)
	for index, day := range days {
		layouts[index] = layoutDay(m.events, day, top(day), step, rows)
	}
	nowRow := -1
	for _, day := range days {
		if sameDay(day, now) {
			nowRow = int(now.Sub(top(day)) / step)
		}
	}
	for row := range rows {
		clock := top(days[0]).Add(time.Duration(row) * step)
		gutter := strings.Repeat(" ", gutterWidth)
		switch {
		case row == nowRow:
			gutter = lipgloss.NewStyle().Foreground(colorActive).Bold(true).Render(padTo(now.Format("15:04"), gutterWidth))
		case clock.Minute() == 0:
			gutter = chrome.Render(padTo(clock.Format("15:04"), gutterWidth))
		}
		var line strings.Builder
		line.WriteString(gutter)
		for index, day := range days {
			if index > 0 {
				line.WriteString(separator)
			}
			isNow := row == nowRow && sameDay(day, now)
			line.WriteString(m.gridCell(layouts[index], row, columnWidth, isNow, clock.Minute() == 0, wide))
		}
		lines = append(lines, line.String())
	}
	return strings.Join(lines, "\n")
}

// blockLines is what an event block says, one entry per row: the title
// (wrapped when the block is tall enough), then its times, then where.
func blockLines(item gridEvent, width int, wide bool) []string {
	event := item.event
	rows := item.endRow - item.startRow
	title := rsvpMark(event) + event.Summary
	if event.Pending {
		title += " …"
	}
	if item.clippedTop {
		title = "↑ " + title
	}
	times := event.Start.Local().Format("15:04") + "–" + event.End.Local().Format("15:04")
	if rows == 1 {
		if width >= 22 {
			return []string{event.Start.Local().Format("15:04") + " " + title}
		}
		return []string{title}
	}
	titleRows := 1
	if rows >= 3 {
		titleRows = rows - 2 // leave a row for the time
	}
	if titleRows > 2 && event.Location != "" {
		titleRows = rows - 3
	}
	wrapped := wrapText(title, max(width, 1))
	if len(wrapped) > titleRows {
		wrapped = wrapped[:titleRows]
		last := wrapped[len(wrapped)-1]
		if displayWidth(last) >= width-1 {
			last = truncateToWidth(last, max(width-1, 1))
		}
		wrapped[len(wrapped)-1] = strings.TrimRight(last, " .") + "…"
	}
	lines := append([]string(nil), wrapped...)
	detail := times
	if wide {
		detail += " · " + event.Source
	}
	lines = append(lines, detail)
	if event.Location != "" {
		lines = append(lines, "@"+event.Location)
	}
	return lines
}

// renderBlockRow draws one row of an event block. The selected block is
// inverted, its ink as background and its calendar colour as text, with a
// bar in that colour down the left edge: it stands out from the pastel
// blocks around it and still shows which calendar it is in.
func renderBlockRow(style lipgloss.Style, selected bool, text string, width int) string {
	if !selected {
		return style.Render(" " + padTo(truncateToWidth(text, max(width-1, 1)), width-1))
	}
	if noColor {
		return lipgloss.NewStyle().Bold(true).Reverse(true).Render("▌" + padTo(truncateToWidth(text, max(width-1, 1)), width-1))
	}
	fill, ink := style.GetBackground(), style.GetForeground()
	inverted := lipgloss.NewStyle().Background(ink).Foreground(fill).Bold(true).Italic(style.GetItalic())
	return inverted.Render("▌" + padTo(truncateToWidth(text, max(width-1, 1)), width-1))
}

func padCells(cells []string, width int) []string {
	padded := make([]string, len(cells))
	for index, cell := range cells {
		padded[index] = padTo(cell, width)
	}
	return padded
}

// gridCell renders one row of one day: event blocks in their lanes, and the
// gaps empty, dotted on the hour, or the now line.
func (m Model) gridCell(placed []gridEvent, row, width int, isNow, onHour, wide bool) string {
	type segment struct {
		from, width int
		text        string
	}
	var segments []segment
	selected, hasSelected := m.selectedEvent()
	for _, item := range placed {
		if row < item.startRow || row >= item.endRow {
			continue
		}
		from := item.lane * width / item.lanes
		to := (item.lane + 1) * width / item.lanes
		blockWidth := to - from
		if item.lane < item.lanes-1 && blockWidth > 2 {
			blockWidth-- // a gap between side-by-side events
		}
		if blockWidth <= 0 {
			continue
		}
		event := item.event
		isSelected := hasSelected && selected.ID == event.ID
		text := ""
		if offset := row - item.startRow; offset < len(blockLines(item, blockWidth-1, wide)) {
			text = blockLines(item, blockWidth-1, wide)[offset]
		}
		if row == item.endRow-1 && item.clippedEnd && row != item.startRow {
			text = "↓"
		}
		style := eventStyle(event, isSelected)
		if row != item.startRow && !isSelected {
			style = style.Bold(false)
		}
		if event.Pending || event.RSVP == "needs-action" {
			style = style.Italic(true)
		}
		if event.RSVP == "declined" {
			style = style.Faint(true).Strikethrough(true)
		}
		segments = append(segments, segment{from, blockWidth, renderBlockRow(style, isSelected, text, blockWidth)})
	}
	sort.Slice(segments, func(i, j int) bool { return segments[i].from < segments[j].from })
	gap := func(length int) string {
		if length <= 0 {
			return ""
		}
		switch {
		case isNow:
			return lipgloss.NewStyle().Foreground(colorActive).Render(strings.Repeat("─", length))
		case onHour:
			return lipgloss.NewStyle().Foreground(colorChrome).Faint(true).Render(strings.Repeat("·", length))
		default:
			return strings.Repeat(" ", length)
		}
	}
	var cell strings.Builder
	position := 0
	for _, part := range segments {
		if part.from < position {
			continue
		}
		cell.WriteString(gap(part.from - position))
		cell.WriteString(part.text)
		position = part.from + part.width
	}
	cell.WriteString(gap(width - position))
	return cell.String()
}

func (m Model) eventPill(event calendar.Event, width int) string {
	title := event.Summary
	if event.Pending {
		title += " …"
	}
	selected, ok := m.selectedEvent()
	isSelected := ok && selected.ID == event.ID
	style := eventStyle(event, isSelected)
	if event.Pending {
		style = style.Italic(true)
	}
	return renderBlockRow(style, isSelected, title, width)
}

type allDaySpan struct {
	event calendar.Event
	days  []int
}

func (m Model) allDayBand(days []weekDayInfo, width int) [][]string {
	spans := weekAllDaySpans(days)
	if len(spans) == 0 {
		return nil
	}
	var occupied [][]bool
	var rows [][]string
	for _, span := range spans {
		lane := 0
		for ; lane < len(occupied); lane++ {
			free := true
			for _, day := range span.days {
				if occupied[lane][day] {
					free = false
					break
				}
			}
			if free {
				break
			}
		}
		if lane == len(occupied) {
			occupied = append(occupied, make([]bool, len(days)))
			rows = append(rows, make([]string, len(days)))
		}
		for _, day := range span.days {
			occupied[lane][day] = true
			rows[lane][day] = m.eventPill(span.event, width)
		}
	}
	return rows
}

func weekAllDaySpans(days []weekDayInfo) []allDaySpan {
	order := make([]string, 0)
	spans := make(map[string]allDaySpan)
	for dayIndex, day := range days {
		for _, event := range day.allDay {
			key := event.ID
			if key == "" {
				key = event.UID + event.Start.Format(time.RFC3339Nano)
			}
			span, exists := spans[key]
			if !exists {
				span.event = event
				order = append(order, key)
			}
			span.days = append(span.days, dayIndex)
			spans[key] = span
		}
	}
	result := make([]allDaySpan, 0, len(order))
	for _, key := range order {
		result = append(result, spans[key])
	}
	sort.SliceStable(result, func(i, j int) bool { return len(result[i].days) > len(result[j].days) })
	return result
}

// renderCalendarAgenda lists the coming days: past events faded, a line at
// the current time with the wait until the next event, and the list
// scrolled to keep the selection in view.
func (m Model) renderCalendarAgenda(width, height int) string {
	from, to := m.calendarRange()
	label := fmt.Sprintf("%s – %s", from.Format("January 2"), to.AddDate(0, 0, -1).Format("January 2"))
	header := hintedSectionHeader(label, calendarHint("←/→ span · t today"), width)
	rows := m.calendarRows() // events with snoozed mail between them (upcoming.go)
	if len(m.events) == 0 && len(rows) == 0 {
		return header + "\n" + styleMuted.Render("  Nothing planned · n adds an event")
	}
	now := m.now()
	var lines []string
	cursorLine := 0
	lastDate := ""
	nowShown := false
	nextEvent := -1 // the first timed event still to come today, for the now line
	for _, row := range rows {
		if row.mail < 0 {
			if event := m.events[row.event]; !event.AllDay && event.Start.After(now) && sameDay(event.Start, now) {
				nextEvent = row.event
				break
			}
		}
	}
	for _, row := range rows {
		start := row.at.Local()
		day := start.Format("Monday, January 2")
		if day != lastDate {
			heading := day
			switch {
			case sameDay(start, now):
				heading = "Today · " + day
			case sameDay(start, now.AddDate(0, 0, 1)):
				heading = "Tomorrow · " + day
			}
			lines = append(lines, sectionHeader(heading, width))
			lastDate = day
		}
		timed := row.mail >= 0 || !m.events[row.event].AllDay
		if !nowShown && timed && sameDay(start, now) && start.After(now) {
			text := "  ── now " + now.Format("15:04")
			if nextEvent >= 0 {
				text += " · next in " + roughDuration(m.events[nextEvent].Start.Sub(now))
			}
			lines = append(lines, lipgloss.NewStyle().Foreground(colorActive).Bold(true).Render(text+" ──"))
			nowShown = true
		}
		selected := row.cursor() == m.eventCursor
		if selected {
			cursorLine = len(lines)
		}
		if row.mail >= 0 {
			lines = append(lines, m.agendaMailLine(row, selected, width, now))
			continue
		}
		lines = append(lines, m.agendaLine(m.events[row.event], selected, width, now))
	}
	available := max(height-1, 1)
	offset := 0
	if len(lines) > available {
		offset = min(max(cursorLine-available/3, 0), len(lines)-available)
	}
	end := min(offset+available, len(lines))
	return header + "\n" + strings.Join(lines[offset:end], "\n")
}

func (m Model) agendaLine(event calendar.Event, selected bool, width int, now time.Time) string {
	marker := "  "
	when := event.Start.Local().Format("15:04") + "–" + event.End.Local().Format("15:04")
	if event.AllDay {
		when = "all day"
	}
	dot := lipgloss.NewStyle().Foreground(eventFill(event)).Render("●")
	source := padTo(truncateToWidth(event.Source, 10), 10)
	title := rsvpMark(event) + event.Summary
	if event.Series {
		title += " ↻"
	}
	if event.Pending {
		title += " …"
	}
	text := lipgloss.NewStyle()
	timeStyle := lipgloss.NewStyle().Foreground(colorChrome)
	over := !event.AllDay && event.End.Before(now)
	if over {
		text, timeStyle = styleMuted, styleMuted
	}
	if event.RSVP == "declined" {
		text = styleMuted.Strikethrough(true)
	}
	if event.Organizer != "" {
		if event.RSVP == "needs-action" {
			title += lipgloss.NewStyle().Foreground(colorActive).Render("  from " + event.Organizer + " · answer a ~ x")
		} else {
			title += styleMuted.Render("  from " + event.Organizer)
		}
	}
	if selected {
		marker = lipgloss.NewStyle().Foreground(colorPrimary).Bold(true).Render("│ ")
		text = lipgloss.NewStyle().Foreground(colorPrimary).Bold(true)
	}
	if event.Pending {
		text = text.Italic(true)
	}
	location := ""
	if event.Location != "" {
		location = styleMuted.Render("  @" + event.Location)
	}
	line := marker + timeStyle.Render(padTo(when, 12)) + dot + " " + styleMuted.Render(source) + " " + text.Render(title) + location + homeSuffix(event, "  · ")
	return truncateToWidth(line, width)
}

func eventTimeSpan(event calendar.Event, width int) string {
	start := event.Start.Local()
	if start.IsZero() {
		return ""
	}
	from := start.Format("15:04")
	end := event.End.Local()
	if end.After(start) {
		span := from + "–" + end.Format("15:04")
		if displayWidth(span) <= width {
			return span
		}
	}
	return from
}

func weekLabel(start time.Time) string {
	end := start.AddDate(0, 0, 6)
	if start.Month() == end.Month() {
		return fmt.Sprintf("%s %d – %d", start.Format("January"), start.Day(), end.Day())
	}
	return fmt.Sprintf("%s – %s", start.Format("January 2"), end.Format("January 2"))
}

func weekDayColumnLabel(day time.Time, first bool) string {
	weekday := day.Weekday().String()[:3]
	if day.Day() == 1 || first {
		return fmt.Sprintf("%s %d %s", weekday, day.Day(), day.Format("Jan"))
	}
	return fmt.Sprintf("%s %d", weekday, day.Day())
}

func eventOverlapsDay(event calendar.Event, day time.Time) bool {
	start := dayStart(day)
	end := start.AddDate(0, 0, 1)
	return event.Start.Before(end) && event.End.After(start)
}

func dayStart(value time.Time) time.Time {
	value = value.Local()
	return time.Date(value.Year(), value.Month(), value.Day(), 0, 0, 0, 0, value.Location())
}

func dateKey(value time.Time) string {
	return value.Local().Format("2006-01-02")
}

func sameDay(a, b time.Time) bool {
	a, b = a.Local(), b.Local()
	return a.Year() == b.Year() && a.YearDay() == b.YearDay()
}
