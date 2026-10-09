package syncd

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOversizedRequestIsRefused(t *testing.T) {
	startDaemon(t, "")
	conn, err := net.Dial("unix", SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	go func() {
		// The daemon stops reading at the limit, so this write may fail.
		payload := `{"op":"sync","paths":["` + strings.Repeat("a", 2*maxRequestSize) + `"]}`
		conn.Write([]byte(payload))
	}()
	var response Response
	if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.OK || !strings.Contains(response.Error, "bad request") {
		t.Fatalf("response = %+v, want a bad request", response)
	}
}

// A subscription that hears nothing cannot tell a quiet daemon from a wedged one.
func TestSubscriptionGetsHeartbeats(t *testing.T) {
	_, _, daemon, _ := startDaemonWith(t, "", func(d *Daemon) { d.tune.heartbeat = 100 * time.Millisecond })
	waitFor(t, "the startup sync to finish", func() bool {
		status := daemon.snapshot()
		return !status.Accounts[0].Syncing && !status.Accounts[0].LastSync.IsZero()
	})
	subscription, err := Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	if _, err := subscription.Next(); err != nil {
		t.Fatal(err)
	}
	// Nothing changes now; only the heartbeat can send the next status.
	received := make(chan error, 1)
	go func() {
		_, err := subscription.Next()
		received <- err
	}()
	select {
	case err := <-received:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no status without a change")
	}
}

func TestLaunchdPlistLogsOutsideTmp(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "scripts", "launchd", "mailday-syncd.plist.in"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "<string>/tmp/") || !strings.Contains(text, "Library/Logs/mailday-syncd.log") {
		t.Fatal("the launchd agent should log to ~/Library/Logs, not /tmp")
	}
}
