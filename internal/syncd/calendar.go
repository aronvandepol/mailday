package syncd

import (
	"context"
	"log"
	"strings"
	"time"
)

// refreshCalendar runs the calendar command soon, unless it ran in the last
// minute.
func (d *Daemon) refreshCalendar() {
	if d.options.Calendar == "" {
		return
	}
	select {
	case d.calendarNow <- struct{}{}:
	default:
	}
}

// forceCalendar runs the calendar command soon even right after a run, for
// when what it writes is stale at once (the time zone changed).
func (d *Daemon) forceCalendar() {
	if d.options.Calendar == "" {
		return
	}
	select {
	case d.calendarForce <- struct{}{}:
	default:
	}
}

func (d *Daemon) maildayOpen() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.subscribers) > 0
}

func (d *Daemon) calendarLoop(ctx context.Context) {
	if d.options.Calendar == "" {
		return
	}
	d.setCalendar(func(status *CalendarStatus) { status.Enabled = true })
	var last time.Time
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	run := func() {
		d.setCalendar(func(status *CalendarStatus) { status.Syncing = true })
		runCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		output, err := newCommand(runCtx, "/bin/sh", "-c", d.options.Calendar).CombinedOutput()
		cancel()
		last = time.Now()
		d.setCalendar(func(status *CalendarStatus) {
			status.Syncing = false
			if err != nil {
				status.LastError = lastLine(strings.TrimSpace(string(output)), err)
				log.Printf("calendar: %s", status.LastError)
			} else {
				status.LastError = ""
				status.LastSync = last
			}
		})
	}
	run()
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.calendarNow:
			if time.Since(last) > time.Minute {
				run()
			}
		case <-d.calendarForce:
			// A write lands on the server a moment before it is listed.
			time.Sleep(time.Second)
			run()
		case <-ticker.C:
			every := d.options.CalendarIdle
			if d.maildayOpen() {
				every = d.options.CalendarActive
			}
			if time.Since(last) >= every {
				run()
			}
		case <-d.resumed():
			time.Sleep(5 * time.Second)
			run()
		}
	}
}

func (d *Daemon) setCalendar(change func(*CalendarStatus)) {
	d.mu.Lock()
	change(&d.calendar)
	d.broadcastLocked()
	d.mu.Unlock()
}
