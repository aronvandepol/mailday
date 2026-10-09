package tui

// Travelling: the TUI follows the machine's time zone while it runs, and
// beside local times shows the home time (MAILDAY_HOME_TZ, else
// [calendar].home_zone, else the zone Mailday started in) so a meeting
// booked in home hours reads both ways.

import (
	"github.com/aronvandepol/mailday/internal/config"
	"os"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/aronvandepol/mailday/internal/calendar"
	"github.com/aronvandepol/mailday/internal/syncd"
)

// startZone is the machine's zone when Mailday started, the home zone when
// none is set.
var startZone = sync.OnceValue(func() string {
	if name, err := syncd.ZoneName(); err == nil && name != "" {
		return name
	}
	return "UTC"
})

// zoneState follows the system zone. resolve is nil when following is off
// (not live, or in tests that do not want it).
type zoneState struct {
	resolve func() (string, error)
	name    string // the zone the TUI runs in, as last seen
	failed  string // a zone name that would not load, so the 15 s tick stays quiet
}

// newZoneState starts following the system zone, noting the one the process
// itself loaded at start.
func newZoneState() zoneState {
	state := zoneState{resolve: syncd.ZoneName}
	if name, err := state.resolve(); err == nil {
		state.name = name
	}
	return state
}

// followZone is run on every clock tick. When the system zone has changed it
// swaps time.Local, as mailday-syncd does for its own process, and reloads
// the calendar quietly: the files were rewritten for the new zone and the
// range on screen starts at another midnight.
//
// time.Local is a plain variable, so the swap races with any goroutine
// formatting a local time at that instant; as in the daemon, the worst case
// is one timestamp in the old zone, on a once-a-trip event.
func (m Model) followZone() (Model, tea.Cmd) {
	if m.zone.resolve == nil {
		return m, nil
	}
	name, err := m.zone.resolve()
	if err != nil || name == "" || name == m.zone.name {
		return m, nil
	}
	if m.zone.name == "" {
		// The start-up reading failed; this one only records.
		m.zone.name = name
		return m, nil
	}
	if name == m.zone.failed {
		return m, nil
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		m.zone.failed = name
		m.status = "Cannot load time zone " + name
		return m, nil
	}
	time.Local = location
	m.zone.name, m.zone.failed = name, ""
	if !m.calendarAnchor.IsZero() {
		// Keep the date on screen: the old midnight is another day here.
		anchor := m.calendarAnchor
		m.calendarAnchor = time.Date(anchor.Year(), anchor.Month(), anchor.Day(), 0, 0, 0, 0, time.Local)
	}
	m.calendarDay = dayOf(m.now())
	m.status = "Time zone is now " + name
	return m, m.quietLoadCalendarCmd()
}

var (
	zoneCacheMu sync.Mutex
	zoneCache   = map[string]*time.Location{}
)

// loadZone is time.LoadLocation remembered, because the renderers ask for
// the home zone for every event on every frame. nil when it will not load.
func loadZone(name string) *time.Location {
	zoneCacheMu.Lock()
	defer zoneCacheMu.Unlock()
	if location, ok := zoneCache[name]; ok {
		return location
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		location = nil
	}
	zoneCache[name] = location
	return location
}

func homeZoneName() string {
	if name := strings.TrimSpace(os.Getenv("MAILDAY_HOME_TZ")); name != "" {
		return name
	}
	if name := strings.TrimSpace(config.Get().Calendar.HomeZone); name != "" {
		return name
	}
	return startZone()
}

// zoneCity is the readable end of a zone name: Europe/Amsterdam is
// "Amsterdam", America/Buenos_Aires "Buenos Aires".
func zoneCity(name string) string {
	if index := strings.LastIndex(name, "/"); index >= 0 {
		name = name[index+1:]
	}
	return strings.ReplaceAll(name, "_", " ")
}

// homeSpan is a timed event's hours in the home zone, "09:00–10:00
// Amsterdam", or "" when they read the same as the local ones (at home, or in
// a zone with the same offset). A different date at home gets its weekday.
func homeSpan(event calendar.Event) string {
	if event.AllDay || event.Start.IsZero() {
		return ""
	}
	home := loadZone(homeZoneName())
	if home == nil {
		return ""
	}
	here := event.Start.Local()
	start, end := event.Start.In(home), event.End.In(home)
	text := start.Format("15:04") + "–" + end.Format("15:04")
	// Compare calendar dates by their text: sameDay would convert both to local.
	otherDay := start.Format("2006-01-02") != here.Format("2006-01-02")
	if text == here.Format("15:04")+"–"+event.End.Local().Format("15:04") && !otherDay {
		return ""
	}
	if otherDay {
		text = start.Format("Mon ") + text
	}
	return text + " " + zoneCity(homeZoneName())
}

// awayLabel is the footer rule's note while the machine is in another zone:
// "Seoul time". Empty at home, and in a zone whose offset is home's.
func (m Model) awayLabel() string {
	home := loadZone(homeZoneName())
	if home == nil {
		return ""
	}
	now := m.now()
	abbreviation, offset := now.Local().Zone()
	if _, homeOffset := now.In(home).Zone(); offset == homeOffset {
		return ""
	}
	if m.zone.name != "" {
		return zoneCity(m.zone.name) + " time"
	}
	return abbreviation + " time"
}

// homeSuffix is homeSpan dimmed and ready to follow a local time.
func homeSuffix(event calendar.Event, lead string) string {
	if span := homeSpan(event); span != "" {
		return styleMuted.Render(lead + span)
	}
	return ""
}
