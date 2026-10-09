// Package syncd keeps the Maildirs current without anyone pressing sync.
//
// The daemon holds an IMAP IDLE connection per watched mailbox and runs mbsync
// for just that mailbox when the server reports a change. Mailday tells it
// over a Unix socket which folders it changed locally, and the daemon pushes
// them. mbsync stays the only sync engine, so its state files stay authoritative;
// the daemon decides only when and what to sync, and runs one mbsync per
// account at a time.
package syncd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

// maxRequestSize bounds one request; real ones are a few short paths.
const maxRequestSize = 1 << 20

type Request struct {
	Op    string   `json:"op"`              // sync, move, calendar, status, subscribe
	Paths []string `json:"paths,omitempty"` // Maildir folders or message files; empty syncs everything
	Push  bool     `json:"push,omitempty"`  // only carry local changes to the server
	Wait  bool     `json:"wait,omitempty"`  // reply once the sync has finished
}

type Response struct {
	OK     bool    `json:"ok"`
	Error  string  `json:"error,omitempty"`
	Status *Status `json:"status,omitempty"`
}

type Status struct {
	Accounts []AccountStatus `json:"accounts"`
	Calendar CalendarStatus  `json:"calendar"`
	// Zone is the system time zone the daemon follows, "Asia/Seoul".
	Zone string `json:"zone"`
}

// CalendarStatus reports the calendar refresh command, when one is set.
type CalendarStatus struct {
	Enabled   bool      `json:"enabled,omitempty"`
	Syncing   bool      `json:"syncing,omitempty"`
	LastSync  time.Time `json:"last_sync"`
	LastError string    `json:"last_error,omitempty"`
}

type AccountStatus struct {
	Name      string    `json:"name"`
	Syncing   bool      `json:"syncing"`
	LastSync  time.Time `json:"last_sync"`
	LastError string    `json:"last_error,omitempty"`
	// Watching lists mailboxes with a live IDLE connection; Offline those
	// waiting to reconnect.
	Watching []string `json:"watching,omitempty"`
	Offline  []string `json:"offline,omitempty"`
}

// SocketPath is where the daemon listens. MAILDAY_SYNCD_SOCKET overrides it.
func SocketPath() string {
	if value := os.Getenv("MAILDAY_SYNCD_SOCKET"); value != "" {
		return value
	}
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		return filepath.Join(runtimeDir, "mailday-syncd.sock")
	}
	// macOS has no runtime directory, and TMPDIR differs between launchd
	// and a terminal, so use a fixed per-user path.
	if cache, err := os.UserCacheDir(); err == nil {
		return filepath.Join(cache, "mailday", "syncd.sock")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("mailday-syncd-%d.sock", os.Getuid()))
}

var ErrNotRunning = errors.New("mailday-syncd is not running")

// Send makes one request and returns the reply.
func Send(ctx context.Context, request Request) (Response, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", SocketPath())
	if err != nil {
		return Response{}, ErrNotRunning
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return Response{}, err
	}
	var response Response
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&response); err != nil {
		return Response{}, err
	}
	if !response.OK && response.Error != "" {
		return response, errors.New(response.Error)
	}
	return response, nil
}

// Subscription streams status updates; Next blocks until the next one.
type Subscription struct {
	conn    net.Conn
	decoder *json.Decoder
}

func Subscribe() (*Subscription, error) {
	conn, err := net.DialTimeout("unix", SocketPath(), 2*time.Second)
	if err != nil {
		return nil, ErrNotRunning
	}
	if err := json.NewEncoder(conn).Encode(Request{Op: "subscribe"}); err != nil {
		conn.Close()
		return nil, err
	}
	return &Subscription{conn: conn, decoder: json.NewDecoder(bufio.NewReader(conn))}, nil
}

func (s *Subscription) Next() (Status, error) {
	var response Response
	if err := s.decoder.Decode(&response); err != nil {
		return Status{}, err
	}
	if response.Status == nil {
		return Status{}, errors.New("empty status")
	}
	return *response.Status, nil
}

func (s *Subscription) Close() error { return s.conn.Close() }
