package syncd

import (
	"context"
	"fmt"
	"log"
	"net"
	"slices"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/aronvandepol/mailday/internal/mbsyncrc"
)

// poller covers the mailboxes nobody idles on (Sent, Archive, Trash, the
// other tags) with one connection per account: STATUS on each of them every
// Poll, and a sync of the one whose counts, next UID or mod-sequence moved.
// That is a handful of cheap commands a minute instead of one IDLE
// connection per mailbox, which Gmail's 15-connection cap would not allow.
type poller struct {
	daemon  *Daemon
	account *mbsyncrc.Account

	client  *imapclient.Client
	boxes   map[string]mbsyncrc.Target
	listed  time.Time
	seen    map[string]string // mailbox -> last STATUS fingerprint
	modSeqs bool
}

// pollBackoff is the wait after the given number of failed rounds in a row:
// 30 s, doubling to 10 minutes. Poll itself is the floor.
func pollBackoff(failures int) time.Duration {
	return min(30*time.Second<<min(failures-1, 5), 10*time.Minute)
}

// errorLog tells when an error is worth a log line: the first of a run of
// failures and each change of text, not the same message every round.
type errorLog struct{ last string }

func (e *errorLog) changed(err error) bool {
	if err.Error() == e.last {
		return false
	}
	e.last = err.Error()
	return true
}

// recovered reports whether a failure had been logged since the last call.
func (e *errorLog) recovered() bool {
	was := e.last != ""
	e.last = ""
	return was
}

func (p *poller) loop(ctx context.Context) {
	defer p.close()
	var failures int
	var logged errorLog
	for {
		err := p.poll(ctx)
		if ctx.Err() != nil {
			return
		}
		wait := p.daemon.options.Poll
		if err != nil {
			failures++
			// Not only with -v: otherwise a refused login or a dead server
			// leaves the poller silently useless.
			if logged.changed(err) {
				log.Printf("%s: poll: %v", p.account.Name, err)
			}
			p.close()
			wait = max(wait, pollBackoff(failures))
		} else {
			failures = 0
			if logged.recovered() {
				log.Printf("%s: poll: recovered", p.account.Name)
			}
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		case <-p.daemon.resumed():
			timer.Stop()
			failures = 0
			p.close() // the connection did not survive the suspend
		}
	}
}

func (p *poller) close() {
	if p.client != nil {
		p.client.Close()
		p.client = nil
	}
}

func (p *poller) connected() bool {
	if p.client == nil {
		return false
	}
	select {
	case <-p.client.Closed():
		return false
	default:
		return true
	}
}

func (p *poller) connect(ctx context.Context) error {
	w := &watcher{daemon: p.daemon, account: p.account}
	client, err := w.dial(&imapclient.Options{Dialer: &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}})
	if err != nil {
		return err
	}
	if err := withTimeout(ctx, client, 45*time.Second, func() error { return w.login(client) }); err != nil {
		client.Close()
		return err
	}
	p.client = client
	p.modSeqs = client.Caps().Has(imap.CapCondStore)
	return nil
}

func (p *poller) poll(ctx context.Context) error {
	// Servers that drop idle connections (some after 10 s) get a fresh
	// one each round; a NOOP tells a live connection from a dead one.
	if p.connected() {
		if err := withTimeout(ctx, p.client, 20*time.Second, func() error { return p.client.Noop().Wait() }); err != nil {
			p.close()
		}
	}
	if !p.connected() {
		if err := p.connect(ctx); err != nil {
			return err
		}
	}
	if p.boxes == nil || time.Since(p.listed) > 15*time.Minute {
		if err := p.list(ctx); err != nil {
			return err
		}
	}
	options := &imap.StatusOptions{NumMessages: true, UIDNext: true, UIDValidity: true, NumUnseen: true, HighestModSeq: p.modSeqs}
	first := p.seen == nil
	if first {
		p.seen = map[string]string{}
	}
	for mailbox, target := range p.boxes {
		var data *imap.StatusData
		err := withTimeout(ctx, p.client, 30*time.Second, func() error {
			var err error
			data, err = p.client.Status(mailbox, options).Wait()
			return err
		})
		if err != nil {
			if !p.connected() {
				return err
			}
			continue // the mailbox went away; the next LIST drops it
		}
		fingerprint := fmt.Sprintf("%d/%d/%d/%d/%d", deref(data.NumMessages), data.UIDNext, data.UIDValidity, deref(data.NumUnseen), data.HighestModSeq)
		previous, known := p.seen[mailbox]
		p.seen[mailbox] = fingerprint
		if known && previous != fingerprint {
			if p.daemon.verbose {
				log.Printf("%s/%s: changed on server (poll)", p.account.Name, mailbox)
			}
			p.daemon.requestTarget(target, false)
		}
	}
	return nil
}

// list finds the synced mailboxes that are not watched with IDLE.
func (p *poller) list(ctx context.Context) error {
	var mailboxes []*imap.ListData
	err := withTimeout(ctx, p.client, 60*time.Second, func() error {
		var err error
		mailboxes, err = p.client.List("", "*", nil).Collect()
		return err
	})
	if err != nil {
		return err
	}
	boxes := map[string]mbsyncrc.Target{}
	for _, data := range mailboxes {
		if slices.Contains(data.Attrs, imap.MailboxAttrNoSelect) || slices.Contains(data.Attrs, imap.MailboxAttrNonExistent) {
			continue
		}
		if p.daemon.watches(p.account.Name, data.Mailbox) {
			continue
		}
		if target, ok := p.daemon.config.RemoteTarget(p.account.Name, data.Mailbox); ok {
			boxes[data.Mailbox] = target
		}
	}
	p.boxes = boxes
	p.listed = time.Now()
	return nil
}

func deref(value *uint32) uint32 {
	if value == nil {
		return 0
	}
	return *value
}
