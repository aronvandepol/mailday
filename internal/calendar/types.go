package calendar

import "time"

type Event struct {
	ID          string
	UID         string
	Source      string
	Provider    string
	Color       string
	Summary     string
	Location    string
	Description string
	Start       time.Time
	End         time.Time
	AllDay      bool
	// RemoteID is the Google event id or Exchange item id that
	// mailday-calendar changes the event through; empty for read-only sources.
	RemoteID string
	// Editable is false for invitations: only their organizer moves them.
	Editable  bool
	Organizer string
	// RSVP is your answer to an invitation: needs-action, accepted,
	// tentative or declined; empty for his own events.
	RSVP      string
	Attendees string // the other guests, comma-separated
	Series    bool   // one occurrence of a repeating event
	// Rule and RuleText describe the repeat of an event being created.
	Rule, RuleText string
	// Pending marks a local change the server has not confirmed yet.
	Pending bool
}

type Source struct {
	UID      string
	Name     string
	Backend  string
	Provider string
	Color    string
	DBPath   string
}

type Result struct {
	Events   []Event
	Sources  []Source
	Warnings []string
}
