package maildir

import (
	"io/fs"
	"sync"
	"sync/atomic"
	"time"
)

// headerCache remembers parsed headers and previews by absolute path, so a
// reload only opens files that are new or changed. Maildir files are written
// once and then renamed, so size and modification time are enough to tell a
// changed file; a rename gives a new path and is parsed again.
type headerCache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
	// opens counts files opened for headers or previews, so tests can show
	// that an unchanged file is not read twice.
	opens atomic.Int64
}

type cacheEntry struct {
	size       int64
	modTime    time.Time
	account    string
	unread     bool
	message    Message
	snippet    string
	hasSnippet bool
}

func newHeaderCache() *headerCache {
	return &headerCache{entries: make(map[string]cacheEntry)}
}

// header returns the parsed header of path, from the cache when the file is
// unchanged. A nil cache parses every time.
func (c *headerCache) header(path, account string, unread bool, info fs.FileInfo) (Message, error) {
	if c == nil {
		return readHeader(path, account, unread, info)
	}
	c.mu.Lock()
	entry, ok := c.entries[path]
	c.mu.Unlock()
	if ok && entry.size == info.Size() && entry.modTime.Equal(info.ModTime()) && entry.account == account && entry.unread == unread {
		message := entry.message
		message.Snippet = entry.snippet
		return message, nil
	}
	c.opens.Add(1)
	message, err := readHeader(path, account, unread, info)
	if err != nil {
		return Message{}, err
	}
	c.mu.Lock()
	c.entries[path] = cacheEntry{size: info.Size(), modTime: info.ModTime(), account: account, unread: unread, message: message}
	c.mu.Unlock()
	return message, nil
}

// snippet returns the preview of a message that header returned. A failed
// read is not remembered, so a file that was briefly unreadable is retried.
func (c *headerCache) snippet(message Message) string {
	if c == nil {
		snippet, _ := readMessagePreview(message.Path)
		return snippet
	}
	c.mu.Lock()
	entry, ok := c.entries[message.Path]
	c.mu.Unlock()
	if ok && entry.hasSnippet && entry.size == message.Size {
		return entry.snippet
	}
	c.opens.Add(1)
	snippet, err := readMessagePreview(message.Path)
	if err != nil {
		return snippet
	}
	c.mu.Lock()
	if entry, ok := c.entries[message.Path]; ok && entry.size == message.Size {
		entry.snippet, entry.hasSnippet = snippet, true
		c.entries[message.Path] = entry
	}
	c.mu.Unlock()
	return snippet
}

// prune forgets every file that was not seen in the latest listing, so
// deleted and renamed files do not pile up.
func (c *headerCache) prune(live map[string]bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	for path := range c.entries {
		if !live[path] {
			delete(c.entries, path)
		}
	}
	c.mu.Unlock()
}
