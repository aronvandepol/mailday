package syncd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aronvandepol/mailday/internal/mbsyncrc"
)

type Options struct {
	ConfigPath string        // mbsync configuration
	Idle       []string      // remote mailboxes to watch with IDLE in every account
	Refresh    time.Duration // NOOP on each IDLE connection this often
	Sweep      time.Duration // full mbsync of every account, as a safety net
	Settle     time.Duration // wait before running, to merge bursts
	Notify     bool          // desktop notification for new inbox mail
	PostSync   string        // shell command after syncs that ran, e.g. a notmuch index
	Poll       time.Duration // STATUS round over every other mailbox (0 disables)
	// Calendar is a command that refreshes the calendar files (mailday-calsync).
	// It runs every CalendarActive while Mailday is open, at once when Mailday
	// starts or asks for a full sync, and every CalendarIdle otherwise.
	Calendar       string
	CalendarActive time.Duration
	CalendarIdle   time.Duration
	// Remind notifies this long before timed events (Linux only; 0 is off).
	// RemindAll includes calendars evolution-alarm-notify already covers.
	Remind    time.Duration
	RemindAll bool
	Verbose   bool
}

type Daemon struct {
	config     *mbsyncrc.Config
	options    Options
	settle     time.Duration
	verbose    bool
	mbsyncPath string

	runners    map[string]*runner
	inboxDirs  map[string]string
	inboxSpecs map[string]string
	// destinations are where mail goes when it leaves a watched box:
	// Archive and Trash, as mbsync targets per account.
	destinations map[string][]string

	mu          sync.Mutex
	status      map[string]*AccountStatus
	subscribers map[chan Status]bool
	resumeCh    chan struct{}
	postSync    chan struct{}
	dropped     map[string]bool // account/mailbox whose IDLE watch gave up
	calendarNow chan struct{}
	// calendarForce runs the calendar command now, even right after a run:
	// Mailday just changed an event.
	calendarForce chan struct{}
	calendar      CalendarStatus
	// zone is the system zone the daemon follows (see zoneLoop); zoneError
	// the problem last logged about finding it.
	zone         string
	zoneError    string
	zoneResolver func() (string, error)
	// snoozeLogged is the problem last logged per snooze key (snoozeNote).
	snoozeLogged map[string]string

	tune     tuning
	secrets  secretCache
	runnerWG sync.WaitGroup // the runner loops, so shutdown lets mbsync finish
}

// tuning holds the timings that tests shorten.
type tuning struct {
	heartbeat           time.Duration // status sent to idle subscriptions
	missingMailboxRetry time.Duration // IDLE watch retry for a mailbox that does not exist
	secretTTL           time.Duration // how long a PassCmd result is reused
	idleStartTimeout    time.Duration // server's answer to IDLE
	moveMargin          time.Duration // see moveJob.queued
	zoneInterval        time.Duration // system zone check
}

func defaultTuning() tuning {
	return tuning{
		heartbeat:           time.Minute,
		missingMailboxRetry: 10 * time.Minute,
		secretTTL:           45 * time.Second,
		idleStartTimeout:    30 * time.Second,
		moveMargin:          3 * time.Second,
		zoneInterval:        zoneInterval,
	}
}

// shutdownGrace bounds how long Run waits for runners to finish the mbsync
// they are in: SIGTERM plus the 30 s WaitDelay of mbsync fits in it.
const shutdownGrace = 40 * time.Second

func New(options Options) (*Daemon, error) {
	if options.ConfigPath == "" {
		options.ConfigPath = mbsyncrc.DefaultPath()
	}
	config, err := mbsyncrc.Load(options.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("read mbsync config: %w", err)
	}
	if len(config.AccountNames()) == 0 {
		return nil, fmt.Errorf("%s defines no IMAP accounts with channels", options.ConfigPath)
	}
	mbsyncPath, err := exec.LookPath("mbsync")
	if err != nil {
		return nil, errors.New("mbsync not found in PATH")
	}
	if options.Refresh <= 0 {
		options.Refresh = defaultRefresh
	}
	if options.Settle <= 0 {
		options.Settle = 400 * time.Millisecond
	}
	daemon := &Daemon{
		config:        config,
		options:       options,
		settle:        options.Settle,
		verbose:       options.Verbose,
		mbsyncPath:    mbsyncPath,
		runners:       map[string]*runner{},
		inboxDirs:     map[string]string{},
		inboxSpecs:    map[string]string{},
		destinations:  map[string][]string{},
		status:        map[string]*AccountStatus{},
		subscribers:   map[chan Status]bool{},
		resumeCh:      make(chan struct{}),
		postSync:      make(chan struct{}, 1),
		dropped:       map[string]bool{},
		calendarNow:   make(chan struct{}, 1),
		calendarForce: make(chan struct{}, 1),
		tune:          defaultTuning(),
		zoneResolver:  ZoneName,
	}
	if options.CalendarActive <= 0 {
		daemon.options.CalendarActive = 3 * time.Minute
	}
	if options.CalendarIdle <= 0 {
		daemon.options.CalendarIdle = 15 * time.Minute
	}
	// BoxPath assumes mbsync's default layout; other layouts would map
	// folders to the wrong remote mailboxes, so say so once at startup.
	for _, name := range slices.Sorted(maps.Keys(config.MaildirStores)) {
		if store := config.MaildirStores[name]; !store.Verbatim() {
			log.Printf("warning: MaildirStore %s uses SubFolders %s; mailday-syncd maps only Verbatim, so moves and pushes there may fail", name, store.SubFolders)
		}
	}
	for _, account := range config.AccountNames() {
		daemon.runners[account] = newRunner(daemon, account)
		daemon.status[account] = &AccountStatus{Name: account}
		if dir, ok := config.LocalDir(account, "INBOX"); ok {
			daemon.inboxDirs[account] = dir
		}
		if target, ok := config.RemoteTarget(account, "INBOX"); ok {
			daemon.inboxSpecs[account] = target.Spec
		}
		if inbox := daemon.inboxDirs[account]; inbox != "" {
			for _, name := range []string{"Archive", "Trash", "Deleted Items", "[Gmail]/Trash"} {
				dir := filepath.Join(filepath.Dir(inbox), name)
				if info, err := os.Stat(dir); err != nil || !info.IsDir() {
					continue
				}
				if target, ok := config.LocalTarget(dir); ok && target.Account == account && !slices.Contains(daemon.destinations[account], target.Spec) {
					daemon.destinations[account] = append(daemon.destinations[account], target.Spec)
				}
			}
		}
	}
	return daemon, nil
}

func (d *Daemon) Run(ctx context.Context) error {
	listener, unlock, err := listen()
	if err != nil {
		return err
	}
	defer unlock()
	defer listener.Close()
	go func() {
		<-ctx.Done()
		listener.Close()
	}()

	for _, runner := range d.runners {
		d.runnerWG.Add(1)
		go func() {
			defer d.runnerWG.Done()
			runner.loop(ctx)
		}()
	}
	// Runs first on the way out: a runner cancelled mid-sync is stopping
	// mbsync with SIGTERM, and the process should not exit under it.
	defer func() {
		if ctx.Err() != nil {
			d.waitForRunners()
		}
	}()
	for _, accountName := range d.config.AccountNames() {
		account := d.config.Accounts[accountName]
		for _, mailbox := range d.options.Idle {
			target, ok := d.config.RemoteTarget(accountName, mailbox)
			if !ok {
				if d.verbose {
					log.Printf("%s/%s: no mbsync channel syncs it; not watching", accountName, mailbox)
				}
				continue
			}
			d.updateAccount(accountName, func(status *AccountStatus) {
				status.Offline = appendUnique(status.Offline, mailbox)
			})
			go (&watcher{daemon: d, account: account, mailbox: mailbox, target: target}).loop(ctx)
		}
	}
	d.checkZone() // first, so the first status already names the zone
	go d.watchResume(ctx)
	go d.zoneLoop(ctx)
	go d.postSyncLoop(ctx)
	go d.calendarLoop(ctx)
	go d.remindLoop(ctx)
	go d.snoozeLoop(ctx)
	if d.options.Poll > 0 {
		for _, accountName := range d.config.AccountNames() {
			go (&poller{daemon: d, account: d.config.Accounts[accountName]}).loop(ctx)
		}
	}
	go d.sweepLoop(ctx)
	log.Printf("watching %d account(s); socket %s", len(d.runners), SocketPath())

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go d.serve(ctx, conn)
	}
}

// listen takes the single-instance lock, then the socket. The lock (a file
// next to the socket) closes the window in which two daemons both find the
// socket dead, both remove it and both listen; the returned function
// releases it.
func listen() (net.Listener, func(), error) {
	path := SocketPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, nil, err
	}
	lock, err := lockFile(path + ".lock")
	if err != nil {
		return nil, nil, err
	}
	// A daemon from before the lock existed may still own the socket.
	if conn, err := net.DialTimeout("unix", path, time.Second); err == nil {
		conn.Close()
		lock.Close()
		return nil, nil, fmt.Errorf("another mailday-syncd is listening on %s", path)
	}
	_ = os.Remove(path)
	var listener net.Listener
	withRestrictiveUmask(func() { listener, err = net.Listen("unix", path) })
	if err != nil {
		lock.Close()
		return nil, nil, err
	}
	_ = os.Chmod(path, 0o600)
	return listener, func() { lock.Close() }, nil
}

// waitForRunners gives runners shutdownGrace to leave the mbsync they are in.
func (d *Daemon) waitForRunners() {
	done := make(chan struct{})
	go func() {
		d.runnerWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(shutdownGrace):
		log.Printf("runners still busy after %s; exiting", shutdownGrace)
	}
}

// sweepLoop runs everything at start and then every Sweep, so mailboxes
// nobody watches (Sent, Archive, the other tags) still follow the server.
func (d *Daemon) sweepLoop(ctx context.Context) {
	d.requestAll(nil)
	if d.options.Sweep <= 0 {
		return
	}
	ticker := time.NewTicker(d.options.Sweep)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.requestAll(nil)
		}
	}
}

// watchResume notices a suspend: the monotonic clock stops while suspended
// and the wall clock does not. On resume every IDLE connection is presumed
// dead, so watchers reconnect and everything is synced.
func (d *Daemon) watchResume(ctx context.Context) {
	const tick = 15 * time.Second
	last := time.Now()
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			wall := now.Round(0).Sub(last.Round(0))
			monotonic := now.Sub(last)
			last = now
			if wall-monotonic > 30*time.Second {
				log.Printf("resumed after %s asleep; reconnecting", (wall - monotonic).Round(time.Second))
				d.mu.Lock()
				close(d.resumeCh)
				d.resumeCh = make(chan struct{})
				d.mu.Unlock()
				go func() {
					time.Sleep(5 * time.Second) // the network needs a moment
					d.requestAll(nil)
				}()
			}
		}
	}
}

// afterSync schedules the PostSync command; runs that finish while it is
// running fold into one more run.
func (d *Daemon) afterSync() {
	if d.options.PostSync == "" {
		return
	}
	select {
	case d.postSync <- struct{}{}:
	default:
	}
}

func (d *Daemon) postSyncLoop(ctx context.Context) {
	if d.options.PostSync == "" {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.postSync:
		}
		runCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		output, err := newCommand(runCtx, "/bin/sh", "-c", d.options.PostSync).CombinedOutput()
		cancel()
		if err != nil {
			log.Printf("post-sync command: %v: %s", err, strings.TrimSpace(string(output)))
		}
	}
}

func (d *Daemon) resumed() <-chan struct{} {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.resumeCh
}

func (d *Daemon) requestAll(done chan error) int {
	for _, runner := range d.runners {
		runner.request(nil, false, true, done)
	}
	return len(d.runners)
}

func (d *Daemon) requestTarget(target mbsyncrc.Target, push bool) {
	if runner := d.runners[target.Account]; runner != nil {
		runner.request([]string{target.Spec}, push, false, nil)
	}
}

func (d *Daemon) requestDestinations(account string) {
	if runner := d.runners[account]; runner != nil && len(d.destinations[account]) > 0 {
		runner.request(d.destinations[account], false, false, nil)
	}
}

// requestPaths maps Maildir paths to mbsync targets, grouped per account.
func (d *Daemon) requestPaths(paths []string, push bool, done chan error) (int, error) {
	perAccount := map[string][]string{}
	var unknown []string
	for _, path := range paths {
		folder := maildirFolder(path)
		target, ok := d.config.LocalTarget(folder)
		if !ok {
			unknown = append(unknown, folder)
			continue
		}
		if !slices.Contains(perAccount[target.Account], target.Spec) {
			perAccount[target.Account] = append(perAccount[target.Account], target.Spec)
		}
	}
	for account, specs := range perAccount {
		d.runners[account].request(specs, push, false, done)
	}
	if len(perAccount) == 0 && len(unknown) > 0 {
		return 0, fmt.Errorf("no mbsync channel syncs %s", strings.Join(unknown, ", "))
	}
	return len(perAccount), nil
}

// maildirFolder turns a message path, or a cur/new/tmp directory, into its
// Maildir folder.
func maildirFolder(path string) string {
	path = filepath.Clean(path)
	isLeaf := func(name string) bool { return name == "cur" || name == "new" || name == "tmp" }
	if isLeaf(filepath.Base(filepath.Dir(path))) {
		return filepath.Dir(filepath.Dir(path))
	}
	if isLeaf(filepath.Base(path)) {
		return filepath.Dir(path)
	}
	return path
}

func (d *Daemon) serve(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	encoder := json.NewEncoder(conn)
	var request Request
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	// A request is a few paths; cap it so a stray client cannot make the
	// daemon buffer without end.
	if err := json.NewDecoder(io.LimitReader(reader, maxRequestSize)).Decode(&request); err != nil {
		_ = encoder.Encode(Response{Error: "bad request: " + err.Error()})
		return
	}
	_ = conn.SetReadDeadline(time.Time{})
	switch request.Op {
	case "move":
		if len(request.Paths) != 2 {
			_ = encoder.Encode(Response{Error: "move takes the old and the new path"})
			return
		}
		if err := d.requestMove(request.Paths[0], request.Paths[1]); err != nil {
			_ = encoder.Encode(Response{Error: err.Error()})
			return
		}
		_ = encoder.Encode(Response{OK: true})
	case "calendar":
		if d.options.Calendar == "" {
			_ = encoder.Encode(Response{Error: "no calendar command configured"})
			return
		}
		select {
		case d.calendarForce <- struct{}{}:
		default:
		}
		_ = encoder.Encode(Response{OK: true})
	case "status":
		status := d.snapshot()
		_ = encoder.Encode(Response{OK: true, Status: &status})
	case "sync":
		var done chan error
		if request.Wait {
			done = make(chan error, 16)
		}
		count := 0
		var err error
		if len(request.Paths) == 0 {
			count = d.requestAll(done)
			d.refreshCalendar()
		} else {
			count, err = d.requestPaths(request.Paths, request.Push, done)
		}
		if err != nil {
			_ = encoder.Encode(Response{Error: err.Error()})
			return
		}
		if done != nil {
			var failures []string
			for range count {
				select {
				case runErr := <-done:
					if runErr != nil {
						failures = append(failures, runErr.Error())
					}
				case <-ctx.Done():
					return
				}
			}
			if len(failures) > 0 {
				_ = encoder.Encode(Response{Error: strings.Join(failures, "; ")})
				return
			}
		}
		_ = encoder.Encode(Response{OK: true})
	case "subscribe":
		updates := make(chan Status, 8)
		d.mu.Lock()
		d.subscribers[updates] = true
		first := len(d.subscribers) == 1
		d.mu.Unlock()
		if first {
			// Mailday just opened: show it today's calendar, not the last run's.
			d.refreshCalendar()
		}
		defer func() {
			d.mu.Lock()
			delete(d.subscribers, updates)
			d.mu.Unlock()
		}()
		// Notice the client going away even while nothing changes.
		gone := make(chan struct{})
		go func() {
			_, _ = reader.ReadByte()
			close(gone)
		}()
		status := d.snapshot()
		if encoder.Encode(Response{OK: true, Status: &status}) != nil {
			return
		}
		// A status when nothing changed tells the client the daemon is
		// alive; a silent stream looks the same as a wedged daemon.
		heartbeat := time.NewTicker(d.tune.heartbeat)
		defer heartbeat.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-gone:
				return
			case <-heartbeat.C:
				status := d.snapshot()
				_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if encoder.Encode(Response{OK: true, Status: &status}) != nil {
					return
				}
			case status := <-updates:
				_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if encoder.Encode(Response{OK: true, Status: &status}) != nil {
					return
				}
			}
		}
	default:
		_ = encoder.Encode(Response{Error: "unknown op " + request.Op})
	}
}

func (d *Daemon) updateAccount(account string, change func(*AccountStatus)) {
	d.mu.Lock()
	status := d.status[account]
	if status == nil {
		d.mu.Unlock()
		return
	}
	change(status)
	d.broadcastLocked()
	d.mu.Unlock()
}

// broadcastLocked sends the current state to every subscriber; d.mu is held.
func (d *Daemon) broadcastLocked() {
	snapshot := d.snapshotLocked()
	for subscriber := range d.subscribers {
		select {
		case subscriber <- snapshot:
		default: // a slow client misses an intermediate state, not the last one
			select {
			case <-subscriber:
			default:
			}
			select {
			case subscriber <- snapshot:
			default:
			}
		}
	}
}

func (d *Daemon) setWatching(account, mailbox string, live bool) {
	d.updateAccount(account, func(status *AccountStatus) {
		if live {
			status.Watching = appendUnique(status.Watching, mailbox)
			status.Offline = slices.DeleteFunc(status.Offline, func(s string) bool { return s == mailbox })
		} else {
			status.Offline = appendUnique(status.Offline, mailbox)
			status.Watching = slices.DeleteFunc(status.Watching, func(s string) bool { return s == mailbox })
		}
	})
}

// watches reports whether an IDLE watcher covers the mailbox.
func (d *Daemon) watches(account, mailbox string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dropped[account+"/"+mailbox] {
		return false
	}
	for _, watched := range d.options.Idle {
		if watched == mailbox || (strings.EqualFold(watched, "INBOX") && strings.EqualFold(mailbox, "INBOX")) {
			return true
		}
	}
	return false
}

// restoreWatch undoes dropWatch once the mailbox exists after all.
func (d *Daemon) restoreWatch(account, mailbox string) (wasDropped bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	wasDropped = d.dropped[account+"/"+mailbox]
	delete(d.dropped, account+"/"+mailbox)
	return wasDropped
}

func (d *Daemon) dropWatch(account, mailbox string) {
	d.mu.Lock()
	d.dropped[account+"/"+mailbox] = true
	d.mu.Unlock()
	d.updateAccount(account, func(status *AccountStatus) {
		status.Offline = slices.DeleteFunc(status.Offline, func(s string) bool { return s == mailbox })
		status.Watching = slices.DeleteFunc(status.Watching, func(s string) bool { return s == mailbox })
	})
}

func (d *Daemon) snapshot() Status {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.snapshotLocked()
}

func (d *Daemon) snapshotLocked() Status {
	status := Status{Calendar: d.calendar, Zone: d.zone}
	for _, account := range d.config.AccountNames() {
		if current := d.status[account]; current != nil {
			copied := *current
			copied.Watching = slices.Clone(current.Watching)
			copied.Offline = slices.Clone(current.Offline)
			slices.Sort(copied.Watching)
			slices.Sort(copied.Offline)
			status.Accounts = append(status.Accounts, copied)
		}
	}
	return status
}

func appendUnique(list []string, value string) []string {
	if slices.Contains(list, value) {
		return list
	}
	return append(list, value)
}
