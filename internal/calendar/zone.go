package calendar

import "time"

// LookupLocation resolves an iCalendar TZID the way event parsing does: an
// IANA name, or a Windows name from Outlook. known is false for anything
// else, and the location is then time.Local. Invitation parsing in maildir
// shares it so both read "W. Europe Standard Time" alike.
func LookupLocation(name string) (location *time.Location, known bool) {
	return lookupLocation(name)
}
