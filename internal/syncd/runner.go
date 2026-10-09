package syncd

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// runner serialises mbsync for one account. Requests that arrive while mbsync
// runs are merged into the next run, so a burst of IDLE events or keypresses
// costs one extra sync at most.
type runner struct {
	daemon  *Daemon
	account string

	mu      sync.Mutex
	all     bool
	full    map[string]bool
	push    map[string]bool
	waiters []chan error
	moves   []moveJob
	wake    chan struct{}
	// failures counts runs in a row that failed; the failed work is
	// retried after 15 s, doubling to 10 minutes.
	failures int
	// synced records when an mbsync covering a target last finished, by
	// channel or channel:box (see moveJob.queued).
	synced map[string]time.Time
}

// lastSynced is when an mbsync last finished that looked at spec's box: a
// run of the whole channel, or of that box.
func (r *runner) lastSynced(spec string) time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	last := r.synced[spec]
	if whole := r.synced[channelOf(spec)]; whole.After(last) {
		last = whole
	}
	return last
}

func (r *runner) noteSynced(args []string) {
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.synced == nil {
		r.synced = map[string]time.Time{}
	}
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			r.synced[arg] = now
		}
	}
}

func newRunner(daemon *Daemon, account string) *runner {
	return &runner{
		daemon:  daemon,
		account: account,
		full:    map[string]bool{},
		push:    map[string]bool{},
		wake:    make(chan struct{}, 1),
	}
}

// request queues specs (channel or channel:box). all syncs every channel of the
// account. done, if not nil, receives the result of the run that covers it.
func (r *runner) request(specs []string, push, all bool, done chan error) {
	r.mu.Lock()
	if all {
		r.all = true
	}
	for _, spec := range specs {
		if push {
			r.push[spec] = true
		} else {
			r.full[spec] = true
		}
	}
	if done != nil {
		r.waiters = append(r.waiters, done)
	}
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *runner) loop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
		}
		// Let a burst settle: Gmail sends EXISTS and FETCH separately, and a
		// keypress often moves a message out of one box and into another.
		select {
		case <-ctx.Done():
			return
		case <-time.After(r.daemon.settle):
		}
		r.mu.Lock()
		all, full, push, waiters, moves := r.all, r.full, r.push, r.waiters, r.moves
		r.all, r.full, r.push, r.waiters, r.moves = false, map[string]bool{}, map[string]bool{}, nil, nil
		r.mu.Unlock()
		if len(moves) > 0 {
			moved, failed := r.serverMoves(ctx, moves)
			for _, job := range moved {
				full[job.fromSpec], full[job.toSpec] = true, true
			}
			for _, job := range failed {
				push[job.fromSpec], push[job.toSpec] = true, true
			}
		}
		err := r.run(ctx, all, full, push)
		for _, waiter := range waiters {
			waiter <- err
		}
		if err == nil {
			if r.failures > 0 {
				log.Printf("%s: sync recovered", r.account)
			}
			r.failures = 0
			continue
		}
		if ctx.Err() != nil {
			return // shutting down: the failure is the cancellation
		}
		r.failures++
		delay := retryDelay(r.failures)
		log.Printf("%s: sync failed; retry in %s", r.account, delay)
		retryFull, retryPush := keys(full), keys(push)
		time.AfterFunc(delay, func() {
			if ctx.Err() != nil {
				return
			}
			if all {
				r.request(nil, false, true, nil)
			}
			r.request(retryFull, false, false, nil)
			r.request(retryPush, true, false, nil)
		})
	}
}

// retryDelay is the wait after the given number of failed runs in a row:
// 15 s, doubling to 10 minutes.
func retryDelay(failures int) time.Duration {
	return min(15*time.Second<<min(failures-1, 6), 10*time.Minute)
}

func (r *runner) run(ctx context.Context, all bool, full, push map[string]bool) error {
	var fullSpecs, pushSpecs []string
	if all {
		fullSpecs = r.daemon.config.ChannelsFor(r.account)
	} else {
		fullSpecs = keys(full)
		for _, spec := range keys(push) {
			if !full[spec] && !full[channelOf(spec)] {
				pushSpecs = append(pushSpecs, spec)
			}
		}
	}
	if len(fullSpecs) == 0 && len(pushSpecs) == 0 {
		return nil
	}
	inbox := r.daemon.inboxDirs[r.account]
	inboxSpec := r.daemon.inboxSpecs[r.account]
	var before map[string]bool
	if inbox != "" && (all || full[inboxSpec]) {
		before = listNew(inbox)
	}

	r.daemon.updateAccount(r.account, func(status *AccountStatus) { status.Syncing = true })
	var runErr error
	if len(pushSpecs) > 0 {
		runErr = r.mbsync(ctx, append([]string{"--push"}, pushSpecs...))
	}
	if len(fullSpecs) > 0 {
		if err := r.mbsync(ctx, fullSpecs); err != nil {
			runErr = err
		}
	}
	r.daemon.updateAccount(r.account, func(status *AccountStatus) {
		status.Syncing = false
		if runErr != nil {
			status.LastError = runErr.Error()
		} else {
			status.LastError = ""
			status.LastSync = time.Now()
		}
	})

	if runErr == nil {
		r.daemon.afterSync()
	}
	if before != nil && runErr == nil {
		var added []string
		for name := range listNew(inbox) {
			if !before[name] {
				added = append(added, filepath.Join(inbox, "new", name))
			}
		}
		if len(added) > 0 {
			// Off the runner: a hung loginctl or notify-send must not
			// hold up the next sync.
			go r.daemon.notifyNewMail(r.account, added)
		}
	}
	return runErr
}

func (r *runner) mbsync(ctx context.Context, args []string) error {
	args = append([]string{"-q"}, args...)
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 3 * time.Second):
			}
		}
		runCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
		started := time.Now()
		command := newCommand(runCtx, r.daemon.mbsyncPath, args...)
		// SIGTERM is mbsync's clean stop; give it time to close its journal.
		command.WaitDelay = 30 * time.Second
		command.Env = os.Environ()
		var output bytes.Buffer
		command.Stdout = &output
		command.Stderr = &output
		err := command.Run()
		r.noteSynced(args)
		cancel()
		if r.daemon.verbose {
			log.Printf("%s: mbsync %s (%s)", r.account, strings.Join(args[1:], " "), time.Since(started).Round(10*time.Millisecond))
		}
		if err == nil {
			return nil
		}
		text := strings.TrimSpace(output.String())
		lastErr = fmt.Errorf("mbsync: %s", lastLine(text, err))
		log.Printf("%s: %v", r.account, lastErr)
		// Another mbsync (a manual run, NeoMutt) holds the box: wait and retry.
		if !strings.Contains(text, "locked") {
			return lastErr
		}
	}
	return lastErr
}

func lastLine(text string, fallback error) string {
	lines := strings.Split(text, "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		if line := strings.TrimSpace(lines[index]); line != "" {
			return line
		}
	}
	return fallback.Error()
}

func listNew(folder string) map[string]bool {
	names := map[string]bool{}
	entries, _ := os.ReadDir(filepath.Join(folder, "new"))
	for _, entry := range entries {
		names[entry.Name()] = true
	}
	return names
}

func keys(set map[string]bool) []string {
	result := make([]string, 0, len(set))
	for key := range set {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func channelOf(spec string) string {
	channel, _, _ := strings.Cut(spec, ":")
	return channel
}
