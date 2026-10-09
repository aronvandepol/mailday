package syncd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-sasl"

	"github.com/aronvandepol/mailday/internal/mbsyncrc"
)

// defaultRefresh is how often IDLE is interrupted for a NOOP. Gmail's IDLE
// often reports new mail late and flag changes not at all, but answers a
// NOOP with everything pending, so the NOOP bounds the delay. It also proves
// the link is alive, which TCP keepalive alone notices only after minutes.
const defaultRefresh = 30 * time.Second

var errResumed = errors.New("system resumed")

// watcher keeps one IDLE connection on one mailbox and asks the runner to
// sync that mailbox whenever the server reports a change.
type watcher struct {
	daemon  *Daemon
	account *mbsyncrc.Account
	mailbox string
	target  mbsyncrc.Target
	// policy sets how long IDLE runs before a NOOP. It starts at
	// Options.Refresh and shrinks when the server turns out to drop idle
	// connections sooner (some close them after 10 seconds).
	policy idlePolicy
}

func (w *watcher) loop(ctx context.Context) {
	backoff := 5 * time.Second
	missing := false // already logged that the mailbox does not exist
	for attempt := 0; ctx.Err() == nil; attempt++ {
		started := time.Now()
		err := w.session(ctx, attempt > 0)
		w.daemon.setWatching(w.account.Name, w.mailbox, false)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > 2*time.Minute || errors.Is(err, errResumed) {
			backoff = 5 * time.Second
		}
		if errors.Is(err, errResumed) {
			w.policy.reset()
		}
		wait := backoff
		switch {
		case errors.Is(err, errNoMailbox):
			// The tagger creates @Reply and @Waiting lazily: the poller
			// covers the box meanwhile, and the watch is retried in case
			// it appears.
			if !missing {
				log.Printf("%s/%s: %v; not watching it, retrying every %s", w.account.Name, w.mailbox, err, w.daemon.tune.missingMailboxRetry)
				missing = true
			}
			w.daemon.dropWatch(w.account.Name, w.mailbox)
			wait = w.daemon.tune.missingMailboxRetry
		case errors.Is(err, errResumed):
			missing = false
			wait = 2 * time.Second // let the network come back up
		default:
			missing = false
			if err != nil {
				log.Printf("%s/%s: %v (retry in %s)", w.account.Name, w.mailbox, err, backoff)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		case <-w.daemon.resumed():
			// Woke from suspend while waiting: try again now.
		}
		if !errors.Is(err, errResumed) && !errors.Is(err, errNoMailbox) {
			backoff = min(backoff*2, 5*time.Minute)
		}
	}
}

var errNoMailbox = errors.New("mailbox does not exist")

func (w *watcher) session(ctx context.Context, catchUp bool) error {
	resumed := w.daemon.resumed()
	changed := make(chan struct{}, 1)
	// expunged notes that a message left or entered the mailbox: archived,
	// deleted, filed or moved back elsewhere, so Archive and Trash are synced
	// along with this box. Flag changes alone do not set it.
	var expunged atomic.Bool
	signal := func() {
		select {
		case changed <- struct{}{}:
		default:
		}
	}
	options := &imapclient.Options{
		Dialer: &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second},
		UnilateralDataHandler: &imapclient.UnilateralDataHandler{
			Expunge: func(uint32) {
				expunged.Store(true)
				signal()
			},
			Mailbox: func(data *imapclient.UnilateralDataMailbox) {
				if data.NumMessages != nil {
					// New mail, or mail moved back from Archive or Trash:
					// this box is synced; Archive and Trash are left to the
					// poller. Syncing them on every new message made the
					// race in moveJob.queued likely (archive right away).
					signal()
				}
			},
			Fetch: func(message *imapclient.FetchMessageData) {
				_, _ = message.Collect() // flags changed elsewhere
				signal()
			},
		},
	}
	client, err := w.dial(options)
	if err != nil {
		return err
	}
	defer client.Close()

	if err := withTimeout(ctx, client, 45*time.Second, func() error { return w.login(client) }); err != nil {
		return err
	}
	if err := withTimeout(ctx, client, 45*time.Second, func() error {
		_, err := client.Select(w.mailbox, &imap.SelectOptions{ReadOnly: true}).Wait()
		return err
	}); err != nil {
		if missingMailbox(err) {
			return fmt.Errorf("%w: %v", errNoMailbox, err)
		}
		return fmt.Errorf("select: %w", err)
	}
	if w.daemon.restoreWatch(w.account.Name, w.mailbox) {
		log.Printf("%s/%s: exists now; watching it", w.account.Name, w.mailbox)
	}
	w.daemon.setWatching(w.account.Name, w.mailbox, true)
	if catchUp {
		// Mail may have arrived while the connection was down.
		w.daemon.requestTarget(w.target, false)
	}
	if w.daemon.verbose {
		log.Printf("%s/%s: idling", w.account.Name, w.mailbox)
	}

	if w.policy.max <= 0 {
		w.policy = newIdlePolicy(w.daemon.options.Refresh)
	}
	for {
		// Idle waits for the server's continuation request; a server that
		// accepts the command and never answers must not stall the watcher.
		var idle *imapclient.IdleCommand
		if err := withTimeout(ctx, client, w.daemon.tune.idleStartTimeout, func() error {
			var err error
			idle, err = client.Idle()
			return err
		}); err != nil {
			return fmt.Errorf("idle: %w", err)
		}
		idleStarted := time.Now()
		health := time.NewTimer(w.policy.interval())
		var reason error
		wasChange := false
		select {
		case <-ctx.Done():
			reason = ctx.Err()
		case <-client.Closed():
			health.Stop()
			lived := time.Since(idleStarted)
			if w.policy.dropped(lived) {
				log.Printf("%s/%s: server keeps dropping IDLE after %s; restarting it every %s", w.account.Name, w.mailbox, lived.Round(time.Second), w.policy.interval().Round(time.Second))
			}
			return errors.New("connection closed by server")
		case <-resumed:
			return errResumed
		case <-changed:
			wasChange = true
		case <-health.C:
		}
		health.Stop()
		if err := stopIdle(idle, client); err != nil {
			return fmt.Errorf("leave idle: %w", err)
		}
		if reason != nil {
			_ = client.Logout().Wait()
			return reason
		}
		// NOOP proves the link is alive and collects anything the server
		// queued while IDLE was being torn down.
		if err := withTimeout(ctx, client, 30*time.Second, func() error { return client.Noop().Wait() }); err != nil {
			return fmt.Errorf("noop: %w", err)
		}
		w.policy.healthyRound()
		if drained(changed) || wasChange {
			if w.daemon.verbose {
				log.Printf("%s/%s: changed on server", w.account.Name, w.mailbox)
			}
			w.daemon.requestTarget(w.target, false)
			if expunged.Swap(false) {
				w.daemon.requestDestinations(w.account.Name)
			}
		}
	}
}

func missingMailbox(err error) bool {
	text := strings.ToLower(err.Error())
	for _, marker := range []string{"nonexistent", "not exist", "unknown mailbox", "not found", "no such"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func drained(changed chan struct{}) bool {
	select {
	case <-changed:
		return true
	default:
		return false
	}
}

func stopIdle(idle *imapclient.IdleCommand, client *imapclient.Client) error {
	done := make(chan error, 1)
	go func() {
		if err := idle.Close(); err != nil {
			done <- err
			return
		}
		done <- idle.Wait()
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(30 * time.Second):
		client.Close()
		return errors.New("timed out")
	}
}

// withTimeout runs fn and closes the connection if it hangs, which unblocks it.
func withTimeout(ctx context.Context, client *imapclient.Client, timeout time.Duration, fn func() error) error {
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		client.Close()
		<-done
		return errors.New("timed out")
	case <-ctx.Done():
		client.Close()
		<-done
		return ctx.Err()
	}
}

func (w *watcher) dial(options *imapclient.Options) (*imapclient.Client, error) {
	address := w.account.Address()
	tlsConfig, err := tlsConfigFor(w.account)
	if err != nil {
		return nil, err
	}
	options.TLSConfig = tlsConfig
	switch strings.ToUpper(w.account.TLSType) {
	case "STARTTLS":
		return imapclient.DialStartTLS(address, options)
	case "NONE":
		return imapclient.DialInsecure(address, options)
	default:
		return imapclient.DialTLS(address, options)
	}
}

// tlsConfigFor trusts the system roots plus the account's CertificateFile,
// as mbsync does, so a private CA that works for mbsync works here too.
func tlsConfigFor(account *mbsyncrc.Account) (*tls.Config, error) {
	host, _, _ := net.SplitHostPort(account.Address())
	config := &tls.Config{ServerName: host}
	if account.CertificateFile == "" {
		return config, nil
	}
	pem, err := os.ReadFile(account.CertificateFile)
	if err != nil {
		return nil, fmt.Errorf("CertificateFile: %w", err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("CertificateFile %s holds no certificate", account.CertificateFile)
	}
	config.RootCAs = pool
	return config, nil
}

func (w *watcher) login(client *imapclient.Client) error {
	secret, err := w.daemon.secret(w.account)
	if err != nil {
		return err
	}
	if w.account.UsesXOAUTH2() {
		if err := client.Authenticate(&xoauth2{user: w.account.User, token: secret}); err != nil {
			w.daemon.forgetSecret(w.account) // the token may have expired
			return fmt.Errorf("XOAUTH2 login: %w", err)
		}
		return nil
	}
	if err := client.Login(w.account.User, secret).Wait(); err != nil {
		w.daemon.forgetSecret(w.account)
		return fmt.Errorf("login: %w", err)
	}
	return nil
}

// xoauth2 is the SASL mechanism Gmail and Microsoft 365 accept for OAuth2.
type xoauth2 struct{ user, token string }

func (x *xoauth2) Start() (string, []byte, error) {
	return "XOAUTH2", []byte("user=" + x.user + "\x01auth=Bearer " + x.token + "\x01\x01"), nil
}

// Next answers the server's error challenge with an empty line, after which
// the server fails the command with the real reason.
func (x *xoauth2) Next([]byte) ([]byte, error) { return []byte{}, nil }

var _ sasl.Client = (*xoauth2)(nil)
