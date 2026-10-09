// Package contacts builds an address book from the mail already on disk, for
// recipient autocompletion. People you write to weigh most; people who write
// to him personally count too; no-reply senders and mailing lists are left out.
package contacts

import (
	"bufio"
	"encoding/json"
	"math"
	"mime"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

type Contact struct {
	Name     string    `json:"name"`
	Address  string    `json:"address"`
	Sent     int       `json:"sent"`     // messages you sent them
	Received int       `json:"received"` // personal messages from them
	Last     time.Time `json:"last"`
}

// Book is the ranked address book.
type Book struct {
	Built    time.Time `json:"built"`
	Contacts []Contact `json:"contacts"`
}

var sentFolders = map[string]bool{"Sent": true, "Sent Items": true, "Sent Mail": true, "[Gmail]/Sent Mail": true}

var automated = regexp.MustCompile(`(?i)(^|[._+-])(no-?reply|do-?not-?reply|notifications?|mailer-daemon|postmaster|bounces?|newsletter|news|info|support|billing|invoice|receipts?|alerts?|updates?|marketing|hello|team|noreply)([._+-]|@)`)

// Directory names arrive as "Jansen, J.J. (Jolien)" or "Vries, S. de (Sam)".
var directoryName = regexp.MustCompile(`^([^,()]+),\s*(?:[A-Z]\.\s*)+((?:\s*[a-z]+)*)\s*\(([^)]+)\)$`)

// DisplayName turns a header name into how a person is called: university
// directory names are reordered, quotes dropped, and a missing name falls
// back to the address.
func DisplayName(name, address string) string {
	name = strings.Trim(strings.TrimSpace(name), `"'`)
	if match := directoryName.FindStringSubmatch(name); match != nil {
		parts := []string{strings.TrimSpace(match[3])}
		if particle := strings.TrimSpace(match[2]); particle != "" {
			parts = append(parts, particle)
		}
		return strings.Join(append(parts, strings.TrimSpace(match[1])), " ")
	}
	if name == "" || strings.EqualFold(name, address) {
		return address
	}
	return name
}

// String is the contact as you type it: "Name" <address>, with the name
// left readable. Encoding for the wire happens when the message is built.
func (c Contact) String() string {
	return FormatAddress(c.Name, c.Address)
}

// FormatAddress writes "Name" <address> without MIME-encoding the name, so
// Korean and accented names stay legible while editing. net/mail parses it
// back, and the sender encodes it on the way out.
func FormatAddress(name, address string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return address
	}
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(name)
	return `"` + escaped + `" <` + address + `>`
}

type headerInfo struct {
	from, to   []*mail.Address
	date       time.Time
	bulk, sent bool
}

func readHeader(path string, sent bool) (headerInfo, bool) {
	file, err := os.Open(path)
	if err != nil {
		return headerInfo{}, false
	}
	defer file.Close()
	message, err := mail.ReadMessage(bufio.NewReaderSize(file, 8192))
	if err != nil {
		return headerInfo{}, false
	}
	header := message.Header
	decoder := new(mime.WordDecoder)
	parse := func(keys ...string) []*mail.Address {
		var out []*mail.Address
		for _, key := range keys {
			raw := header.Get(key)
			if raw == "" {
				continue
			}
			if decoded, err := decoder.DecodeHeader(raw); err == nil {
				raw = decoded
			}
			if list, err := mail.ParseAddressList(raw); err == nil {
				out = append(out, list...)
			}
		}
		return out
	}
	info := headerInfo{sent: sent}
	info.date, _ = header.Date()
	info.bulk = header.Get("List-Id") != "" || header.Get("List-Unsubscribe") != "" ||
		strings.Contains(strings.ToLower(header.Get("Precedence")), "bulk") ||
		strings.HasPrefix(strings.ToLower(header.Get("Auto-Submitted")), "auto-")
	if sent {
		info.to = parse("To", "Cc", "Bcc")
	} else {
		info.from = parse("From")
	}
	return info, true
}

// Build scans every Maildir folder under roots. own lists your addresses,
// which never appear as suggestions.
func Build(roots []string, own func(string) bool) Book {
	type job struct {
		path string
		sent bool
	}
	jobs := make(chan job, 256)
	var mutex sync.Mutex
	byAddress := map[string]*Contact{}
	names := map[string]map[string]int{}
	note := func(address *mail.Address, date time.Time, sent bool) {
		key := strings.ToLower(strings.TrimSpace(address.Address))
		// Automated-looking addresses count only when you wrote to them.
		if key == "" || !strings.Contains(key, "@") || own(key) || (!sent && automated.MatchString(key)) {
			return
		}
		mutex.Lock()
		defer mutex.Unlock()
		contact := byAddress[key]
		if contact == nil {
			contact = &Contact{Address: key}
			byAddress[key] = contact
			names[key] = map[string]int{}
		}
		if sent {
			contact.Sent++
		} else {
			contact.Received++
		}
		if date.After(contact.Last) {
			contact.Last = date
		}
		if name := strings.TrimSpace(address.Name); name != "" {
			names[key][name]++
		}
	}
	var wg sync.WaitGroup
	for range runtime.NumCPU() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range jobs {
				info, ok := readHeader(item.path, item.sent)
				if !ok || (info.bulk && !item.sent) {
					continue
				}
				for _, address := range info.to {
					note(address, info.date, true)
				}
				for _, address := range info.from {
					note(address, info.date, false)
				}
			}
		}()
	}
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return nil
			}
			leaf := filepath.Base(filepath.Dir(path))
			if leaf != "cur" && leaf != "new" {
				return nil
			}
			folder := filepath.Dir(filepath.Dir(path))
			relative, _ := filepath.Rel(root, folder)
			parts := strings.SplitN(relative, string(filepath.Separator), 2)
			sent := len(parts) == 2 && sentFolders[parts[1]]
			jobs <- job{path: path, sent: sent}
			return nil
		})
	}
	close(jobs)
	wg.Wait()

	book := Book{Built: time.Now()}
	for key, contact := range byAddress {
		best, bestCount := "", 0
		for name, count := range names[key] {
			if count > bestCount || (count == bestCount && name < best) {
				best, bestCount = name, count
			}
		}
		contact.Name = DisplayName(best, contact.Address)
		if contact.Name == contact.Address {
			contact.Name = ""
		}
		book.Contacts = append(book.Contacts, *contact)
	}
	now := time.Now()
	sort.Slice(book.Contacts, func(i, j int) bool {
		return book.Contacts[i].score(now) > book.Contacts[j].score(now)
	})
	return book
}

// score ranks by how much you write to someone, then how often they write,
// halving every 180 days since the last message.
func (c Contact) score(now time.Time) float64 {
	age := now.Sub(c.Last).Hours() / 24
	return (3*float64(c.Sent) + float64(c.Received) + 1) * math.Pow(0.5, math.Max(age, 0)/180)
}

// NameFor returns the known name for an address, or "".
func (b Book) NameFor(address string) string {
	address = strings.ToLower(strings.TrimSpace(address))
	for _, contact := range b.Contacts {
		if contact.Address == address {
			return contact.Name
		}
	}
	return ""
}

// Search returns up to limit contacts whose name or address contains every
// word of query, best ranked first. Addresses in exclude are skipped.
func (b Book) Search(query string, limit int, exclude map[string]bool) []Contact {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return nil
	}
	var out []Contact
	for _, contact := range b.Contacts {
		if exclude[contact.Address] {
			continue
		}
		haystack := strings.ToLower(contact.Name + " " + contact.Address)
		match := true
		for _, word := range words {
			if !strings.Contains(haystack, word) {
				match = false
				break
			}
		}
		if match {
			out = append(out, contact)
			if len(out) == limit {
				break
			}
		}
	}
	return out
}

func cachePath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".cache")
	}
	return filepath.Join(dir, "mailday", "contacts.json")
}

// Load reads the cached book. ok is false when there is none.
func Load() (Book, bool) {
	data, err := os.ReadFile(cachePath())
	if err != nil {
		return Book{}, false
	}
	var book Book
	if json.Unmarshal(data, &book) != nil {
		return Book{}, false
	}
	return book, true
}

// Save writes the book to the cache, atomically.
func Save(book Book) error {
	path := cachePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(book)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Stale reports whether the book should be rebuilt.
func (b Book) Stale() bool {
	return time.Since(b.Built) > 15*time.Minute
}
