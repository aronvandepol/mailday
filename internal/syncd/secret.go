package syncd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/aronvandepol/mailday/internal/mbsyncrc"
)

// secretCache keeps PassCmd results for a short while per account. The
// watchers, the poller and server moves all log in separately, and a server
// that drops connections early makes them do it every few seconds; without
// the cache each login would run gpg or an OAuth helper again.
type secretCache struct {
	mu      sync.Mutex
	entries map[string]*secretEntry
}

type secretEntry struct {
	mu      sync.Mutex // held while PassCmd runs, so concurrent logins share one run
	value   string
	fetched time.Time
}

func (c *secretCache) entry(account string) *secretEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]*secretEntry{}
	}
	if c.entries[account] == nil {
		c.entries[account] = &secretEntry{}
	}
	return c.entries[account]
}

// secret returns the account's password or token. PassCmd runs the way mbsync
// runs it (through the shell), so OAuth helpers refresh tokens and gpg-agent
// caching apply here too.
func (d *Daemon) secret(account *mbsyncrc.Account) (string, error) {
	if account.PassCmd == "" {
		if account.Pass == "" {
			return "", errors.New("no Pass or PassCmd in mbsync config")
		}
		return account.Pass, nil
	}
	entry := d.secrets.entry(account.Name)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.value != "" && time.Since(entry.fetched) < d.tune.secretTTL {
		return entry.value, nil
	}
	value, err := runPassCmd(account.PassCmd)
	if err != nil {
		return "", err
	}
	entry.value, entry.fetched = value, time.Now()
	return value, nil
}

// forgetSecret drops the cached result, for when the server refused it.
func (d *Daemon) forgetSecret(account *mbsyncrc.Account) {
	if account.PassCmd == "" {
		return
	}
	entry := d.secrets.entry(account.Name)
	entry.mu.Lock()
	entry.value = ""
	entry.mu.Unlock()
}

func runPassCmd(passCmd string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var stdout, stderr bytes.Buffer
	command := newCommand(ctx, "/bin/sh", "-c", passCmd)
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		if text := clipStderr(stderr.String()); text != "" {
			return "", fmt.Errorf("PassCmd failed: %w: %s", err, text)
		}
		return "", fmt.Errorf("PassCmd failed: %w", err)
	}
	secret := strings.TrimRight(stdout.String(), "\r\n")
	if index := strings.IndexByte(secret, '\n'); index >= 0 {
		secret = secret[:index] // mbsync reads the first line only
	}
	return secret, nil
}

// clipStderr trims a helper's complaint to something fit for a log line.
func clipStderr(text string) string {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) > 300 {
		return string(runes[:300]) + "…"
	}
	return text
}
