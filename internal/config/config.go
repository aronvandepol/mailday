// Package config reads ~/.config/mailday/config.toml: who you send as, and
// where Mailday keeps the files it shares between machines. Everything in it
// is optional. Without identities, Mailday sends as the accounts in the msmtp
// config, under their from addresses.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/BurntSushi/toml"
)

// Identity is one sending address and the Mailday account labels it belongs
// to. A label is the account's directory under the mail root, or its domain.
type Identity struct {
	Accounts []string `toml:"accounts"`
	Name     string   `toml:"name"`
	Address  string   `toml:"address"`
	// Aliases are other addresses that arrive as this identity, or that the
	// msmtp account lists as its from.
	Aliases []string `toml:"aliases"`
	// Signature goes under new mail and forwards, ShortSignature under replies.
	Signature      []string `toml:"signature"`
	ShortSignature []string `toml:"short_signature"`
	// SaveSent files a local Sent copy. Gmail and Exchange Online keep SMTP
	// submissions in Sent themselves, so leave it off for them.
	SaveSent bool `toml:"save_sent"`
}

// Config is the whole file.
type Config struct {
	// Name is the default display name for identities that set none.
	Name string `toml:"name"`
	// SharedDir holds snooze.json, groups.txt and leases/: state worth
	// syncing between machines (point it into a Syncthing or Dropbox folder).
	SharedDir string     `toml:"shared_dir"`
	Identity  []Identity `toml:"identity"`
	Judge     Judge      `toml:"reply_judge"`
	Calendar  Calendar   `toml:"calendar"`
	Exchange  Exchange   `toml:"exchange"`
	Sync      Sync       `toml:"sync"`
	Mail      Mail       `toml:"mail"`
}

// Mail is for the mail screens.
type Mail struct {
	// BoxKeys gives folders a fixed key in the box and file pickers, such
	// as "@Research" = "e".
	BoxKeys map[string]string `toml:"box_keys"`
}

// Sync is for mailday-syncd.
type Sync struct {
	// PostSync is a shell command run after syncs, such as a notmuch index refresh.
	PostSync string `toml:"post_sync"`
}

// Calendar is read by mailday-calsync and mailday-calendar; Mailday itself
// only checks that what it names exists.
type Calendar struct {
	Feeds        []Feed   `toml:"feeds"`
	GoogleScript string   `toml:"google_script"`
	GoogleOwn    []string `toml:"google_own"`
	HomeZone     string   `toml:"home_zone"`
}

// Feed is an iCal address downloaded as published.
type Feed struct {
	Name string `toml:"name"`
	URL  string `toml:"url"`
}

// Exchange is a Microsoft 365 calendar over EWS (mailday-exchange). It is off
// while User is empty.
type Exchange struct {
	Calendar  string `toml:"calendar"`
	User      string `toml:"user"`
	Token     string `toml:"token"`
	ClientID  string `toml:"client_id"`
	EWSURL    string `toml:"ews_url"`
	MailboxTZ string `toml:"mailbox_tz"`
}

// ExchangeToken is MAILDAY_EXCHANGE_TOKEN, else [exchange].token, else
// ~/.config/mailday/ews-token.gpg.
func ExchangeToken() string {
	if path := os.Getenv("MAILDAY_EXCHANGE_TOKEN"); path != "" {
		return path
	}
	if path := Get().Exchange.Token; path != "" {
		return ExpandHome(path)
	}
	return filepath.Join(filepath.Dir(Path()), "ews-token.gpg")
}

// Judge asks a model whether a reply settles the mail it answers, for those
// who keep a Reply folder. Off unless enabled. It runs `claude -p`.
type Judge struct {
	Enabled bool `toml:"enabled"`
}

// Path is MAILDAY_CONFIG, or config.toml in $XDG_CONFIG_HOME/mailday
// (~/.config/mailday on macOS too, beside the other terminal tools).
func Path() string {
	if path := os.Getenv("MAILDAY_CONFIG"); path != "" {
		return path
	}
	if base := os.Getenv("XDG_CONFIG_HOME"); base != "" {
		return filepath.Join(base, "mailday", "config.toml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "mailday", "config.toml")
}

var (
	mu       sync.Mutex
	override *Config
	cached   Config
	cacheErr error
	cacheKey string // path, modification time and size of the file read
)

// Get returns the config, reading the file again when its path or contents
// changed. A missing file is an empty config; a broken one is reported by
// Err and otherwise treated as empty.
func Get() Config {
	c, _ := load()
	return c
}

// Err is the error from reading the file, if any.
func Err() error {
	_, err := load()
	return err
}

func load() (Config, error) {
	mu.Lock()
	defer mu.Unlock()
	if override != nil {
		return *override, nil
	}
	path := Path()
	key := path
	if info, err := os.Stat(path); err == nil {
		key = fmt.Sprintf("%s|%d|%d", path, info.ModTime().UnixNano(), info.Size())
	}
	if key != cacheKey {
		cached, cacheErr = Read(path)
		cacheKey = key
	}
	return cached, cacheErr
}

// Set replaces the loaded config until Set(nil), for tests.
func Set(c *Config) {
	mu.Lock()
	defer mu.Unlock()
	override = c
}

// Read parses one file. A missing file is not an error.
func Read(path string) (Config, error) {
	var c Config
	data, readErr := os.ReadFile(path)
	if os.IsNotExist(readErr) {
		return c, nil
	}
	if readErr != nil {
		return c, readErr
	}
	if _, parseErr := toml.Decode(string(data), &c); parseErr != nil {
		return Config{}, fmt.Errorf("%s: %w", path, parseErr)
	}
	for index := range c.Identity {
		identity := &c.Identity[index]
		if identity.Address == "" {
			return Config{}, fmt.Errorf("%s: identity %d has no address", path, index+1)
		}
		if identity.Name == "" {
			identity.Name = c.Name
		}
	}
	return c, nil
}

// ExpandHome turns a leading ~ into the home directory.
func ExpandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, strings.TrimPrefix(path, "~"))
	}
	return path
}

// DataDir is $XDG_DATA_HOME/mailday, or ~/.local/share/mailday.
func DataDir() string {
	if base := os.Getenv("XDG_DATA_HOME"); base != "" {
		return filepath.Join(base, "mailday")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "mailday")
}

// SharedPath is name inside shared_dir, which defaults to DataDir.
func SharedPath(name string) string {
	dir := Get().SharedDir
	if dir == "" {
		dir = DataDir()
	}
	return filepath.Join(ExpandHome(dir), name)
}
