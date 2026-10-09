// Package mbsyncrc reads the parts of an isync/mbsync configuration that
// mailday-syncd needs: the IMAP accounts to watch with IDLE, and which mbsync
// channel (or channel:box) covers a given remote mailbox or local Maildir.
// mbsync stays the only program that syncs; this package only names targets.
package mbsyncrc

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

type Account struct {
	Name      string
	Host      string
	Port      int
	User      string
	PassCmd   string
	Pass      string
	TLSType   string // IMAPS, STARTTLS or None
	AuthMechs []string
	// CertificateFile holds extra trusted CA certificates (PEM), added to
	// the system ones, as mbsync does.
	CertificateFile string
}

// UsesXOAUTH2 reports whether the account authenticates with an OAuth2 token.
func (a *Account) UsesXOAUTH2() bool {
	for _, mech := range a.AuthMechs {
		if strings.EqualFold(mech, "XOAUTH2") {
			return true
		}
	}
	return false
}

// Address is host:port, with the port mbsync would pick by default.
func (a *Account) Address() string {
	port := a.Port
	if port == 0 {
		port = 993
		if strings.EqualFold(a.TLSType, "STARTTLS") || strings.EqualFold(a.TLSType, "None") {
			port = 143
		}
	}
	return fmt.Sprintf("%s:%d", a.Host, port)
}

type MaildirStore struct {
	Name  string
	Path  string
	Inbox string
	// SubFolders is how mbsync lays nested boxes out: Verbatim (its default),
	// Maildir++ or Legacy. Only Verbatim is mapped by BoxPath.
	SubFolders string
}

// Verbatim reports whether boxes live at Path+box, the only layout this
// package maps.
func (s *MaildirStore) Verbatim() bool {
	return s.SubFolders == "" || strings.EqualFold(s.SubFolders, "Verbatim")
}

type Channel struct {
	Name      string
	FarStore  string
	FarBox    string
	NearStore string
	NearBox   string
	Patterns  []string
}

type Config struct {
	Accounts      map[string]*Account
	IMAPStores    map[string]string // store name -> account name
	MaildirStores map[string]*MaildirStore
	Channels      []*Channel
}

// Target is one argument to mbsync: a channel, or channel:box for a single box
// of a pattern channel.
type Target struct {
	Account string
	Spec    string
}

// DefaultPath returns the configuration file mbsync itself would read.
func DefaultPath() string {
	home, _ := os.UserHomeDir()
	candidates := []string{filepath.Join(home, ".mbsyncrc")}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		candidates = append(candidates, filepath.Join(xdg, "isyncrc"))
	}
	candidates = append(candidates, filepath.Join(home, ".config", "isyncrc"))
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return candidates[0]
}

func Load(path string) (*Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return Parse(file)
}

func Parse(reader io.Reader) (*Config, error) {
	config := &Config{
		Accounts:      map[string]*Account{},
		IMAPStores:    map[string]string{},
		MaildirStores: map[string]*MaildirStore{},
	}
	var (
		account      *Account
		imapStore    string
		maildirStore *MaildirStore
		channel      *Channel
		inGroup      bool
	)
	reset := func() {
		account, imapStore, maildirStore, channel, inGroup = nil, "", nil, nil, false
	}
	scanner := bufio.NewScanner(reader)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			// A blank line ends a Group; other sections end at the next header.
			inGroup = false
			continue
		}
		if strings.HasPrefix(line, "#") {
			continue
		}
		// mbsync separates key and value by any whitespace, tabs included.
		key, value := line, ""
		if index := strings.IndexFunc(line, unicode.IsSpace); index >= 0 {
			key, value = line[:index], strings.TrimSpace(line[index:])
		}
		switch strings.ToLower(key) {
		case "imapaccount":
			reset()
			account = &Account{Name: unquote(value)}
			config.Accounts[account.Name] = account
			continue
		case "imapstore":
			reset()
			imapStore = unquote(value)
			config.IMAPStores[imapStore] = ""
			continue
		case "maildirstore":
			reset()
			maildirStore = &MaildirStore{Name: unquote(value)}
			config.MaildirStores[maildirStore.Name] = maildirStore
			continue
		case "channel":
			if inGroup {
				continue
			}
			reset()
			channel = &Channel{Name: unquote(value)}
			config.Channels = append(config.Channels, channel)
			continue
		case "group":
			reset()
			inGroup = true
			continue
		}
		if inGroup {
			continue
		}
		// An IMAPStore may carry its own server settings instead of naming an
		// IMAPAccount; treat it as an account of the same name.
		if imapStore != "" && account == nil && isAccountKey(key) {
			account = &Account{Name: imapStore}
			config.Accounts[imapStore] = account
			config.IMAPStores[imapStore] = imapStore
		}
		switch {
		case account != nil && isAccountKey(key):
			if err := setAccount(account, key, value); err != nil {
				return nil, fmt.Errorf("line %d: %w", lineNumber, err)
			}
		case imapStore != "" && strings.EqualFold(key, "Account"):
			config.IMAPStores[imapStore] = unquote(value)
		case maildirStore != nil && strings.EqualFold(key, "Path"):
			maildirStore.Path = expandHome(unquote(value))
		case maildirStore != nil && strings.EqualFold(key, "SubFolders"):
			maildirStore.SubFolders = unquote(value)
		case maildirStore != nil && strings.EqualFold(key, "Inbox"):
			maildirStore.Inbox = expandHome(unquote(value))
		case channel != nil && (strings.EqualFold(key, "Far") || strings.EqualFold(key, "Master")):
			channel.FarStore, channel.FarBox = splitStoreBox(value)
		case channel != nil && (strings.EqualFold(key, "Near") || strings.EqualFold(key, "Slave")):
			channel.NearStore, channel.NearBox = splitStoreBox(value)
		case channel != nil && strings.EqualFold(key, "Patterns"), channel != nil && strings.EqualFold(key, "Pattern"):
			channel.Patterns = append(channel.Patterns, fields(value)...)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return config, nil
}

func isAccountKey(key string) bool {
	switch strings.ToLower(key) {
	case "host", "port", "user", "pass", "passcmd", "tlstype", "ssltype", "authmechs", "authmech", "certificatefile":
		return true
	}
	return false
}

func setAccount(account *Account, key, value string) error {
	switch strings.ToLower(key) {
	case "host":
		account.Host = unquote(value)
	case "port":
		port, err := strconv.Atoi(unquote(value))
		if err != nil {
			return fmt.Errorf("bad Port %q", value)
		}
		account.Port = port
	case "user":
		account.User = unquote(value)
	case "pass":
		account.Pass = unquote(value)
	case "passcmd":
		account.PassCmd = strings.TrimPrefix(unquote(value), "+")
	case "tlstype", "ssltype":
		account.TLSType = unquote(value)
	case "authmechs", "authmech":
		account.AuthMechs = fields(value)
	case "certificatefile":
		account.CertificateFile = expandHome(unquote(value))
	}
	return nil
}

// splitStoreBox splits ":store:box" (box possibly quoted).
func splitStoreBox(value string) (string, string) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, ":") {
		return "", unquote(value)
	}
	store, box, _ := strings.Cut(value[1:], ":")
	return store, unquote(strings.TrimSpace(box))
}

func unquote(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		return strings.ReplaceAll(value[1:len(value)-1], `\"`, `"`)
	}
	return value
}

// fields splits on spaces, keeping "quoted words" together.
func fields(value string) []string {
	var result []string
	var current strings.Builder
	quoted, started := false, false
	for _, r := range value {
		switch {
		case r == '"':
			quoted = !quoted
			started = true
		case (r == ' ' || r == '\t') && !quoted:
			if started {
				result = append(result, current.String())
				current.Reset()
				started = false
			}
		default:
			current.WriteRune(r)
			started = true
		}
	}
	if started {
		result = append(result, current.String())
	}
	return result
}

func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return home + path[1:]
	}
	return path
}

// AccountOf returns the IMAP account behind a channel's Far side.
func (c *Config) AccountOf(channel *Channel) string {
	return c.IMAPStores[channel.FarStore]
}

// ChannelsFor lists every channel that syncs the account, in config order.
func (c *Config) ChannelsFor(account string) []string {
	var names []string
	for _, channel := range c.Channels {
		if c.AccountOf(channel) == account {
			names = append(names, channel.Name)
		}
	}
	return names
}

// AccountNames lists accounts that have at least one channel.
func (c *Config) AccountNames() []string {
	var names []string
	seen := map[string]bool{}
	for _, channel := range c.Channels {
		account := c.AccountOf(channel)
		if account != "" && !seen[account] && c.Accounts[account] != nil {
			seen[account] = true
			names = append(names, account)
		}
	}
	return names
}

// RemoteTarget names the mbsync target that syncs a remote mailbox.
func (c *Config) RemoteTarget(account, mailbox string) (Target, bool) {
	for _, channel := range c.Channels {
		if c.AccountOf(channel) != account {
			continue
		}
		if len(channel.Patterns) == 0 {
			box := channel.FarBox
			if box == "" {
				box = "INBOX"
			}
			if sameMailbox(box, mailbox) {
				return Target{account, channel.Name}, true
			}
			continue
		}
		name, ok := strings.CutPrefix(mailbox, channel.FarBox)
		if !ok {
			continue
		}
		if matchPatterns(channel.Patterns, name) {
			return Target{account, specFor(channel, name)}, true
		}
	}
	return Target{}, false
}

// LocalTarget names the mbsync target that syncs a local Maildir folder.
func (c *Config) LocalTarget(dir string) (Target, bool) {
	dir = canonical(dir)
	for _, channel := range c.Channels {
		store := c.MaildirStores[channel.NearStore]
		account := c.AccountOf(channel)
		if store == nil || account == "" {
			continue
		}
		if len(channel.Patterns) == 0 {
			if canonical(store.BoxPath(channel.NearBox)) == dir {
				return Target{account, channel.Name}, true
			}
			continue
		}
		name := ""
		if store.Inbox != "" && canonical(store.Inbox) == dir && channel.NearBox == "" {
			name = "INBOX"
		} else {
			base := canonical(store.Path + channel.NearBox)
			relative, err := filepath.Rel(base, dir)
			if err != nil || relative == "." || strings.HasPrefix(relative, "..") {
				continue
			}
			name = filepath.ToSlash(relative)
		}
		if matchPatterns(channel.Patterns, name) {
			return Target{account, specFor(channel, name)}, true
		}
	}
	return Target{}, false
}

// RemoteMailbox names the server mailbox a local Maildir folder syncs with.
func (c *Config) RemoteMailbox(dir string) (account, mailbox string, ok bool) {
	dir = canonical(dir)
	for _, channel := range c.Channels {
		store := c.MaildirStores[channel.NearStore]
		account := c.AccountOf(channel)
		if store == nil || account == "" {
			continue
		}
		if len(channel.Patterns) == 0 {
			if canonical(store.BoxPath(channel.NearBox)) == dir {
				box := channel.FarBox
				if box == "" {
					box = "INBOX"
				}
				return account, box, true
			}
			continue
		}
		name := ""
		if store.Inbox != "" && canonical(store.Inbox) == dir && channel.NearBox == "" {
			name = "INBOX"
		} else {
			relative, err := filepath.Rel(canonical(store.Path+channel.NearBox), dir)
			if err != nil || relative == "." || strings.HasPrefix(relative, "..") {
				continue
			}
			name = filepath.ToSlash(relative)
		}
		if matchPatterns(channel.Patterns, name) {
			return account, channel.FarBox + name, true
		}
	}
	return "", "", false
}

// LocalDir returns the Maildir folder the account's remote mailbox syncs into.
func (c *Config) LocalDir(account, mailbox string) (string, bool) {
	for _, channel := range c.Channels {
		store := c.MaildirStores[channel.NearStore]
		if c.AccountOf(channel) != account || store == nil {
			continue
		}
		if len(channel.Patterns) == 0 {
			box := channel.FarBox
			if box == "" {
				box = "INBOX"
			}
			if sameMailbox(box, mailbox) {
				return store.BoxPath(channel.NearBox), true
			}
			continue
		}
		name, ok := strings.CutPrefix(mailbox, channel.FarBox)
		if ok && matchPatterns(channel.Patterns, name) {
			if sameMailbox(name, "INBOX") && channel.NearBox == "" && store.Inbox != "" {
				return store.Inbox, true
			}
			return store.BoxPath(channel.NearBox + name), true
		}
	}
	return "", false
}

// BoxPath is where mbsync keeps a box of this store (SubFolders Verbatim).
func (s *MaildirStore) BoxPath(box string) string {
	if box == "" || sameMailbox(box, "INBOX") {
		if s.Inbox != "" {
			return s.Inbox
		}
	}
	return s.Path + box
}

func specFor(channel *Channel, name string) string {
	if len(channel.Patterns) == 1 && !strings.ContainsAny(channel.Patterns[0], "*%!") {
		return channel.Name
	}
	return channel.Name + ":" + name
}

func sameMailbox(a, b string) bool {
	if strings.EqualFold(a, "INBOX") && strings.EqualFold(b, "INBOX") {
		return true
	}
	return a == b
}

// matchPatterns follows mbsync: * matches anything, % anything but the
// hierarchy delimiter, a leading ! excludes, and the last match decides.
func matchPatterns(patterns []string, name string) bool {
	matched := false
	for _, pattern := range patterns {
		negate := strings.HasPrefix(pattern, "!")
		pattern = strings.TrimPrefix(pattern, "!")
		if globMatch(pattern, name) {
			matched = !negate
		}
	}
	return matched
}

func globMatch(pattern, name string) bool {
	if strings.EqualFold(pattern, "INBOX") {
		return strings.EqualFold(name, "INBOX")
	}
	var expression strings.Builder
	expression.WriteString("^")
	for _, r := range pattern {
		switch r {
		case '*':
			expression.WriteString(".*")
		case '%':
			expression.WriteString("[^/]*")
		default:
			expression.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	expression.WriteString("$")
	matched, _ := regexp.MatchString(expression.String(), name)
	return matched
}

func canonical(path string) string {
	path = filepath.Clean(expandHome(path))
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return trueCase(path)
}

// trueCase spells each part of path as it is on disk. On macOS the file
// system ignores case, so ".../Inbox" opens ".../INBOX"; the box name sent
// to mbsync (and on to the server) must be the real one.
func trueCase(path string) string {
	if path == "" || path == string(filepath.Separator) || path == "." {
		return path
	}
	parent, name := filepath.Split(path)
	parent = filepath.Clean(parent)
	if parent != path {
		parent = trueCase(parent)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		return filepath.Join(parent, name)
	}
	for _, entry := range entries {
		if entry.Name() == name {
			return filepath.Join(parent, name)
		}
	}
	for _, entry := range entries {
		if strings.EqualFold(entry.Name(), name) {
			return filepath.Join(parent, entry.Name())
		}
	}
	return filepath.Join(parent, name)
}
