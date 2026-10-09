package syncd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aronvandepol/mailday/internal/mbsyncrc"
)

func TestZoneNameFollowsLocaltimeSymlink(t *testing.T) {
	dir := t.TempDir()
	for index, root := range []string{"usr/share/zoneinfo", "private/var/db/timezone/zoneinfo"} {
		zone := filepath.Join(dir, root, "Asia", "Seoul")
		if err := os.MkdirAll(filepath.Dir(zone), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(zone, []byte("TZif"), 0o600); err != nil {
			t.Fatal(err)
		}
		// /etc/localtime on macOS is a link to a link; the real path decides.
		hop := filepath.Join(dir, fmt.Sprintf("hop-%d", index))
		link := filepath.Join(dir, fmt.Sprintf("localtime-%d", index))
		if err := os.Symlink(zone, hop); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(hop, link); err != nil {
			t.Fatal(err)
		}
		if name, err := zoneNameFrom("", link); err != nil || name != "Asia/Seoul" {
			t.Errorf("%s: zoneNameFrom = %q, %v", root, name, err)
		}
	}
}

func TestZoneNamePrefersTZ(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-localtime")
	for tz, want := range map[string]string{"Europe/Amsterdam": "Europe/Amsterdam", ":Asia/Seoul": "Asia/Seoul"} {
		if name, err := zoneNameFrom(tz, missing); err != nil || name != want {
			t.Errorf("TZ=%q: %q, %v; want %q", tz, name, err, want)
		}
	}
	t.Setenv("TZ", "Asia/Seoul")
	if name, err := ZoneName(); err != nil || name != "Asia/Seoul" {
		t.Errorf("ZoneName with TZ set = %q, %v", name, err)
	}
}

func TestZoneNameErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := zoneNameFrom("", filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing /etc/localtime gave no error")
	}
	// A copied file (some distributions) cannot be told from any zone.
	copied := filepath.Join(dir, "localtime")
	if err := os.WriteFile(copied, []byte("TZif"), 0o600); err != nil {
		t.Fatal(err)
	}
	if name, err := zoneNameFrom("", copied); err == nil {
		t.Errorf("a regular file gave %q", name)
	}
}

// newZoneDaemon is a daemon with only what the zone watcher touches, so the
// test is the only goroutine that reads time.Local while it is swapped.
func newZoneDaemon(resolve func() (string, error)) *Daemon {
	return &Daemon{
		config:        &mbsyncrc.Config{},
		subscribers:   map[chan Status]bool{},
		resumeCh:      make(chan struct{}),
		calendarForce: make(chan struct{}, 1),
		options:       Options{Calendar: "true"},
		tune:          tuning{zoneInterval: 10 * time.Millisecond},
		zoneResolver:  resolve,
	}
}

// inOwnProcess runs body in a child test process. Swapping time.Local races
// with any goroutine that calls time.Now, and the daemons earlier tests
// started are still winding down, so the test binary runs the one test alone.
func inOwnProcess(t *testing.T, body func(t *testing.T)) {
	t.Helper()
	if os.Getenv("MAILDAY_ZONE_TEST_CHILD") == "1" {
		body(t)
		return
	}
	command := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$", "-test.count=1")
	command.Env = append(os.Environ(), "MAILDAY_ZONE_TEST_CHILD=1")
	output, err := command.CombinedOutput()
	if err != nil || strings.Contains(string(output), "no tests to run") {
		t.Fatalf("%v\n%s", err, output)
	}
}

func TestCheckZoneSwapsLocalAndTellsEveryone(t *testing.T) {
	inOwnProcess(t, checkZoneSwapsLocalAndTellsEveryone)
}

func checkZoneSwapsLocalAndTellsEveryone(t *testing.T) {
	amsterdam, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatal(err)
	}
	time.Local = amsterdam // own process: see zoneLoopFollowsTheSystem

	current := "Europe/Amsterdam"
	var resolveErr error
	daemon := newZoneDaemon(func() (string, error) { return current, resolveErr })
	updates := make(chan Status, 8)
	daemon.subscribers[updates] = true

	// The first reading only records the zone the process already runs in.
	daemon.checkZone()
	if got := daemon.snapshot().Zone; got != "Europe/Amsterdam" || time.Local != amsterdam {
		t.Fatalf("first reading: zone %q, Local %v", got, time.Local)
	}
	drain(updates)
	daemon.checkZone()
	if len(updates) != 0 || len(daemon.calendarForce) != 0 {
		t.Fatal("an unchanged zone broadcast or refreshed the calendar")
	}

	// A failed lookup keeps the zone it has.
	resolveErr = errors.New("no localtime")
	daemon.checkZone()
	resolveErr = nil
	if daemon.snapshot().Zone != "Europe/Amsterdam" || time.Local != amsterdam {
		t.Fatal("a failed lookup changed the zone")
	}

	// Moving: Local follows, the status says so, the calendar is refreshed.
	current = "Asia/Seoul"
	daemon.checkZone()
	if time.Local.String() != "Asia/Seoul" {
		t.Fatalf("time.Local = %v", time.Local)
	}
	if _, offset := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC).In(time.Local).Zone(); offset != 9*3600 {
		t.Fatalf("Local offset = %d s, want 9 h", offset)
	}
	if got := daemon.snapshot().Zone; got != "Asia/Seoul" {
		t.Fatalf("status zone = %q", got)
	}
	var broadcast Status
	for len(updates) > 0 {
		broadcast = <-updates
	}
	if broadcast.Zone != "Asia/Seoul" {
		t.Fatalf("broadcast zone = %q", broadcast.Zone)
	}
	if len(daemon.calendarForce) != 1 {
		t.Fatal("calendar refresh was not requested")
	}

	// A name the machine cannot load changes nothing, and is logged once.
	current = "Nowhere/Land"
	daemon.checkZone()
	daemon.checkZone()
	if time.Local.String() != "Asia/Seoul" || daemon.snapshot().Zone != "Asia/Seoul" {
		t.Fatal("an unknown zone replaced the working one")
	}
}

func TestZoneLoopFollowsTheSystem(t *testing.T) {
	inOwnProcess(t, zoneLoopFollowsTheSystem)
}

func zoneLoopFollowsTheSystem(t *testing.T) {
	// This runs in its own process (inOwnProcess), which ends after the
	// test: putting time.Local back would itself race with the runtime's
	// timer goroutine, which reads it.
	time.Local = time.UTC

	var mu sync.Mutex
	current := "UTC"
	daemon := newZoneDaemon(func() (string, error) {
		mu.Lock()
		defer mu.Unlock()
		return current, nil
	})
	daemon.checkZone()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		daemon.zoneLoop(ctx)
		close(done)
	}()
	mu.Lock()
	current = "Asia/Seoul"
	mu.Unlock()
	waitFor(t, "the zone watcher to notice", func() bool { return daemon.snapshot().Zone == "Asia/Seoul" })
	cancel()
	<-done
	if time.Local.String() != "Asia/Seoul" {
		t.Fatalf("time.Local = %v", time.Local)
	}
}

func TestZoneLoopChecksOnResume(t *testing.T) {
	checked := make(chan struct{}, 8)
	daemon := newZoneDaemon(func() (string, error) {
		select {
		case checked <- struct{}{}:
		default:
		}
		return "UTC", nil
	})
	daemon.tune.zoneInterval = time.Hour // only a resume can wake it
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		daemon.zoneLoop(ctx)
		close(done)
	}()
	// A resume that lands before the loop first asks for the channel is
	// missed, as in the other loops (the next tick covers it), so repeat it.
	deadline := time.After(3 * time.Second)
	for waiting := true; waiting; {
		daemon.mu.Lock()
		close(daemon.resumeCh)
		daemon.resumeCh = make(chan struct{})
		daemon.mu.Unlock()
		select {
		case <-checked:
			waiting = false
		case <-deadline:
			t.Fatal("no zone check after a resume")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done
}

func drain(updates chan Status) {
	for len(updates) > 0 {
		<-updates
	}
}

func TestStatusNamesTheZone(t *testing.T) {
	_, _, _, _ = startDaemonWith(t, "", func(daemon *Daemon) {
		daemon.zoneResolver = func() (string, error) { return "Asia/Seoul", nil }
	})
	response, err := Send(context.Background(), Request{Op: "status"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Status.Zone != "Asia/Seoul" {
		t.Fatalf("status zone = %q", response.Status.Zone)
	}
}
