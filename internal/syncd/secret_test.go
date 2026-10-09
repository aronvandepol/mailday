package syncd

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aronvandepol/mailday/internal/mbsyncrc"
)

// countingPassCmd is a PassCmd that records each run.
func countingPassCmd(t *testing.T) (account *mbsyncrc.Account, runs func() int) {
	t.Helper()
	counter := filepath.Join(t.TempDir(), "runs")
	account = &mbsyncrc.Account{Name: "acct", PassCmd: "echo run >> " + counter + "; echo hunter2; echo ignored"}
	return account, func() int {
		data, _ := os.ReadFile(counter)
		return strings.Count(string(data), "run")
	}
}

func TestSecretIsCachedAndShared(t *testing.T) {
	account, runs := countingPassCmd(t)
	d := &Daemon{tune: tuning{secretTTL: time.Hour}}
	var wait sync.WaitGroup
	for range 8 { // watchers, poller and moves log in together
		wait.Add(1)
		go func() {
			defer wait.Done()
			if secret, err := d.secret(account); err != nil || secret != "hunter2" {
				t.Errorf("secret = %q, %v", secret, err)
			}
		}()
	}
	wait.Wait()
	if got := runs(); got != 1 {
		t.Fatalf("PassCmd ran %d times for 8 logins, want 1", got)
	}
	d.forgetSecret(account) // the server refused it
	if _, err := d.secret(account); err != nil || runs() != 2 {
		t.Fatalf("after forgetSecret: runs %d, err %v", runs(), err)
	}
}

func TestSecretExpires(t *testing.T) {
	account, runs := countingPassCmd(t)
	d := &Daemon{tune: tuning{secretTTL: 50 * time.Millisecond}}
	d.secret(account)
	time.Sleep(120 * time.Millisecond)
	d.secret(account)
	if got := runs(); got != 2 {
		t.Fatalf("PassCmd ran %d times, want 2 after the TTL", got)
	}
}

func TestSecretWithoutPassCmdIsNotCached(t *testing.T) {
	d := &Daemon{tune: tuning{secretTTL: time.Hour}}
	if secret, err := d.secret(&mbsyncrc.Account{Name: "a", Pass: "plain"}); err != nil || secret != "plain" {
		t.Fatalf("secret = %q, %v", secret, err)
	}
	if _, err := d.secret(&mbsyncrc.Account{Name: "b"}); err == nil {
		t.Fatal("no Pass or PassCmd should be an error")
	}
}

func TestPassCmdErrorCarriesStderr(t *testing.T) {
	d := &Daemon{tune: tuning{secretTTL: time.Hour}}
	_, err := d.secret(&mbsyncrc.Account{Name: "a", PassCmd: "echo 'token refresh denied' >&2; exit 3"})
	if err == nil || !strings.Contains(err.Error(), "exit status 3") || !strings.Contains(err.Error(), "token refresh denied") {
		t.Fatalf("err = %v, want exit status and the helper's message", err)
	}
	if d.secrets.entry("a").value != "" {
		t.Fatal("a failure must not be cached")
	}
}

func TestClipStderr(t *testing.T) {
	if got := clipStderr("  oops\n"); got != "oops" {
		t.Errorf("got %q", got)
	}
	long := strings.Repeat("é", 400)
	got := clipStderr(long)
	if want := strings.Repeat("é", 300) + "…"; got != want {
		t.Errorf("clipped to %d runes, want 300 and an ellipsis", len([]rune(got)))
	}
}
