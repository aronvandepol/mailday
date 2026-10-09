package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func TestCrashReportIsSavedWithStackAndStatuses(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	rememberForCrash(time.Now(), "Archived · u undoes")
	stop := captureStderr()
	fmt.Fprint(os.Stderr, "Caught panic:\r\n\r\nruntime error: index out of range\r\n\r\ngoroutine 1 [running]:\r\n")
	captured := stop()
	afterRun(fmt.Errorf("%w", tea.ErrProgramPanic), captured, strings.NewReader("\n"))
	matches, _ := filepath.Glob(filepath.Join(os.Getenv("XDG_STATE_HOME"), "mailday", "crash-*.log"))
	if len(matches) != 1 {
		t.Fatalf("reports: %v", matches)
	}
	data, _ := os.ReadFile(matches[0])
	for _, want := range []string{"index out of range", "goroutine 1", "Archived · u undoes"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("report lacks %q:\n%s", want, data)
		}
	}
	// A normal exit writes nothing.
	afterRun(errors.New("quit"), []byte("nothing"), strings.NewReader("\n"))
	afterRun(nil, nil, strings.NewReader(""))
	if again, _ := filepath.Glob(filepath.Join(os.Getenv("XDG_STATE_HOME"), "mailday", "crash-*.log")); len(again) != 1 {
		t.Fatal("a normal exit wrote a report")
	}
}
