package syncd

import (
	"context"
	"errors"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/aronvandepol/mailday/internal/mbsyncrc"
)

func TestPollBackoff(t *testing.T) {
	for failures, want := range map[int]time.Duration{
		1: 30 * time.Second, 2: time.Minute, 3: 2 * time.Minute, 4: 4 * time.Minute,
		5: 8 * time.Minute, 6: 10 * time.Minute, 20: 10 * time.Minute,
	} {
		if got := pollBackoff(failures); got != want {
			t.Errorf("pollBackoff(%d) = %s, want %s", failures, got, want)
		}
	}
}

func TestErrorLogSaysEachFailureOnce(t *testing.T) {
	var logged errorLog
	if logged.recovered() {
		t.Fatal("recovered without a failure")
	}
	one, two := errors.New("login: refused"), errors.New("dial: no route")
	if !logged.changed(one) || logged.changed(one) || !logged.changed(two) || logged.changed(two) {
		t.Fatal("the same text must be logged once, a new text again")
	}
	if !logged.recovered() || logged.recovered() {
		t.Fatal("recovery is reported once")
	}
	if !logged.changed(one) {
		t.Fatal("a failure after recovery is news again")
	}
}

// Without -v the poller used to fail in silence.
func TestPollerLogsConnectFailureWithoutVerbose(t *testing.T) {
	buffer := captureLog(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	listener.Close() // nothing listens there now
	portNumber, _ := strconv.Atoi(port)
	daemon := &Daemon{options: Options{Poll: 20 * time.Millisecond}, resumeCh: make(chan struct{}), tune: defaultTuning()}
	poller := &poller{daemon: daemon, account: &mbsyncrc.Account{Name: "acct", Host: "127.0.0.1", Port: portNumber, TLSType: "None", User: "me", Pass: "x"}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		poller.loop(ctx)
		close(done)
	}()
	waitFor(t, "the poll error in the log", func() bool { return buffer.count("acct: poll:") > 0 })
	cancel()
	<-done
	if got := buffer.count("acct: poll:"); got != 1 {
		t.Fatalf("logged %d times, want once (backoff and unchanged text)", got)
	}
}
