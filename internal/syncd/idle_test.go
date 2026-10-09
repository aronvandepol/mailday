package syncd

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/aronvandepol/mailday/internal/mbsyncrc"
)

// startMemoryIMAP serves an in-memory IMAP server with the given mailboxes.
func startMemoryIMAP(t *testing.T, mailboxes ...string) (*imapmemserver.User, *mbsyncrc.Account) {
	t.Helper()
	memory := imapmemserver.New()
	user := imapmemserver.NewUser("me", "pw")
	for _, name := range mailboxes {
		_ = user.Create(name, nil)
	}
	memory.AddUser(user)
	server := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return memory.NewSession(), nil, nil
		},
		Caps:         imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}},
		InsecureAuth: true,
		Logger:       nopLogger{},
	})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
	return user, accountAt(t, listener.Addr().String())
}

func accountAt(t *testing.T, address string) *mbsyncrc.Account {
	t.Helper()
	host, port, _ := net.SplitHostPort(address)
	portNumber, _ := strconv.Atoi(port)
	return &mbsyncrc.Account{Name: "acct", Host: host, Port: portNumber, TLSType: "None", User: "me", Pass: "pw"}
}

func watcherDaemon(t *testing.T) *Daemon {
	t.Helper()
	config, err := mbsyncrc.Parse(strings.NewReader("IMAPAccount acct\nHost h\n\nIMAPStore s\nAccount acct\n\nMaildirStore l\nPath /x/\n\nChannel c\nFar :s:\nNear :l:\n"))
	if err != nil {
		t.Fatal(err)
	}
	return &Daemon{
		config:   config,
		options:  Options{Refresh: time.Second},
		resumeCh: make(chan struct{}),
		status:   map[string]*AccountStatus{"acct": {Name: "acct"}},
		dropped:  map[string]bool{},
		tune:     defaultTuning(),
	}
}

func (d *Daemon) isDropped(account, mailbox string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dropped[account+"/"+mailbox]
}

func (d *Daemon) isWatching(account, mailbox string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, watched := range d.status[account].Watching {
		if watched == mailbox {
			return true
		}
	}
	return false
}

// A mailbox the tagger creates later must be picked up without a restart.
func TestWatcherRetriesAMissingMailbox(t *testing.T) {
	user, account := startMemoryIMAP(t, "INBOX")
	daemon := watcherDaemon(t)
	daemon.tune.missingMailboxRetry = 100 * time.Millisecond
	buffer := captureLog(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		(&watcher{daemon: daemon, account: account, mailbox: "@Reply", target: mbsyncrc.Target{Account: "acct", Spec: "acct-tags:@Reply"}}).loop(ctx)
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()

	waitFor(t, "the watch to be dropped", func() bool { return daemon.isDropped("acct", "@Reply") })
	time.Sleep(350 * time.Millisecond) // several retries, still no box
	if got := buffer.count("not watching it"); got != 1 {
		t.Fatalf("logged the missing mailbox %d times, want once", got)
	}
	if err := user.Create("@Reply", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the watch to come back", func() bool {
		return daemon.isWatching("acct", "@Reply") && !daemon.isDropped("acct", "@Reply")
	})
}

// fakeServerHangingOnIdle answers everything but IDLE.
func fakeServerHangingOnIdle(t *testing.T) *mbsyncrc.Account {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				fmt.Fprint(conn, "* OK [CAPABILITY IMAP4rev1 IDLE] ready\r\n")
				reader := bufio.NewReader(conn)
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					tag, command, _ := strings.Cut(strings.TrimSpace(line), " ")
					switch strings.ToUpper(strings.Fields(command + " x")[0]) {
					case "CAPABILITY":
						fmt.Fprintf(conn, "* CAPABILITY IMAP4rev1 IDLE\r\n%s OK done\r\n", tag)
					case "LOGIN":
						fmt.Fprintf(conn, "%s OK [CAPABILITY IMAP4rev1 IDLE] logged in\r\n", tag)
					case "SELECT":
						fmt.Fprintf(conn, "* 0 EXISTS\r\n* FLAGS (\\Seen)\r\n* OK [UIDVALIDITY 1] ok\r\n* OK [UIDNEXT 1] ok\r\n%s OK [READ-ONLY] selected\r\n", tag)
					case "IDLE":
						// Never send the continuation request.
					default:
						fmt.Fprintf(conn, "%s OK done\r\n", tag)
					}
				}
			}()
		}
	}()
	return accountAt(t, listener.Addr().String())
}

func TestWatcherGivesUpOnAnIdleThatNeverStarts(t *testing.T) {
	account := fakeServerHangingOnIdle(t)
	daemon := watcherDaemon(t)
	daemon.tune.idleStartTimeout = 200 * time.Millisecond
	w := &watcher{daemon: daemon, account: account, mailbox: "INBOX"}
	result := make(chan error, 1)
	go func() { result <- w.session(context.Background(), false) }()
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "idle: timed out") {
			t.Fatalf("err = %v, want the IDLE timeout", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher is stuck waiting for IDLE to start")
	}
}

func TestTLSConfigTrustsCertificateFile(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "private ca"},
		DNSNames:              []string{"imap.example.org"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, _ := x509.ParseCertificate(der)
	path := filepath.Join(t.TempDir(), "ca.pem")
	os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)

	account := &mbsyncrc.Account{Host: "imap.example.org"}
	plain, err := tlsConfigFor(account)
	if err != nil || plain.RootCAs != nil || plain.ServerName != "imap.example.org" {
		t.Fatalf("without a CertificateFile: %+v, %v", plain, err)
	}
	account.CertificateFile = path
	config, err := tlsConfigFor(account)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := certificate.Verify(x509.VerifyOptions{Roots: config.RootCAs, DNSName: "imap.example.org"}); err != nil {
		t.Fatalf("the private CA is not trusted: %v", err)
	}
	account.CertificateFile = filepath.Join(t.TempDir(), "missing.pem")
	if _, err := tlsConfigFor(account); err == nil {
		t.Fatal("a missing CertificateFile should be an error")
	}
}
