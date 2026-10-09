package syncd

import (
	"bytes"
	"log"
	"os"
	"strings"
	"sync"
	"testing"
)

// logBuffer collects log output; daemon goroutines write to it concurrently.
type logBuffer struct {
	mu   sync.Mutex
	data bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.String()
}

func (b *logBuffer) count(text string) int { return strings.Count(b.String(), text) }

// captureLog redirects the standard logger for the test.
func captureLog(t *testing.T) *logBuffer {
	t.Helper()
	buffer := &logBuffer{}
	log.SetOutput(buffer)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return buffer
}
