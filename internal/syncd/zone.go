package syncd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A Go process reads the system zone once, when time.Local is first used, so
// a daemon left running keeps the old zone after the machine moves (tzupdate
// on Linux, automatic on macOS). zoneLoop notices and swaps time.Local.
//
// time.Local is a plain variable: the swap races, in the detector's terms,
// with any goroutine formatting a local time at that moment. It is one
// pointer written on a once-a-trip event, so the worst case is one
// timestamp or reminder formatted in the old zone; there is no safe way to
// swap it.

const localtimePath = "/etc/localtime"

// zoneInterval is how often the system zone is looked at (and on resume).
const zoneInterval = time.Minute

// ZoneName is the IANA name of the system's time zone, as the TZ variable
// says or else as /etc/localtime points: "Asia/Seoul". The same lookup works
// on Linux (/usr/share/zoneinfo/...) and on macOS (/var/db/timezone/zoneinfo/...).
func ZoneName() (string, error) {
	return zoneNameFrom(os.Getenv("TZ"), localtimePath)
}

// zoneNameFrom is ZoneName with its two inputs given. An empty tz counts as
// unset (Go would read it as UTC, but a daemon started with TZ= is a mistake
// worth following the machine out of). A leading colon is POSIX's way of
// saying "a zoneinfo name".
func zoneNameFrom(tz, localtime string) (string, error) {
	if tz = strings.TrimPrefix(tz, ":"); tz != "" {
		return tz, nil
	}
	resolved, err := filepath.EvalSymlinks(localtime)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", localtime, err)
	}
	// LastIndex: the database can sit under a longer prefix (/private/var/...,
	// a Nix store path), but zone names themselves never contain "zoneinfo/".
	const marker = "zoneinfo/"
	cut := strings.LastIndex(filepath.ToSlash(resolved), marker)
	if cut < 0 {
		return "", fmt.Errorf("%s resolves to %s, which is not under a zoneinfo directory", localtime, resolved)
	}
	name := filepath.ToSlash(resolved)[cut+len(marker):]
	if name == "" {
		return "", errors.New(localtime + " points at the zoneinfo directory itself")
	}
	return name, nil
}

// zoneLoop follows the system zone every zoneInterval and after a resume
// (a trip usually starts with one). The first reading was taken by Run.
func (d *Daemon) zoneLoop(ctx context.Context) {
	ticker := time.NewTicker(d.tune.zoneInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-d.resumed():
		}
		d.checkZone()
	}
}

// checkZone compares the system zone with the one the daemon runs in and
// follows it. The first reading only records the zone: the process loaded
// that one itself. A failure is logged once, not every minute.
func (d *Daemon) checkZone() {
	name, err := d.zoneResolver()
	if err != nil {
		d.zoneProblem(err.Error())
		return
	}
	d.mu.Lock()
	current := d.zone
	d.mu.Unlock()
	if name == current {
		d.zoneProblem("")
		return
	}
	if current == "" {
		d.mu.Lock()
		d.zone = name
		d.broadcastLocked()
		d.mu.Unlock()
		return
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		d.zoneProblem(fmt.Sprintf("cannot load time zone %q: %v", name, err))
		return
	}
	d.zoneProblem("")
	// Swap before telling anyone: a subscriber that reloads on the update,
	// and the calendar run below, must already see the new zone.
	time.Local = location
	d.mu.Lock()
	d.zone = name
	d.broadcastLocked()
	d.mu.Unlock()
	log.Printf("time zone is now %s", name)
	// Reminders read time.Local on every load and follow by themselves; the
	// calendar files are rewritten for the new zone, even right after a run.
	d.forceCalendar()
}

// zoneProblem logs message unless it is the one already logged; "" clears it.
func (d *Daemon) zoneProblem(message string) {
	d.mu.Lock()
	repeat := d.zoneError == message
	d.zoneError = message
	d.mu.Unlock()
	if message != "" && !repeat {
		log.Printf("time zone: %s", message)
	}
}
