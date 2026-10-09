package syncd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// runDispatcher runs scripts/linux/90-mailday-tzupdate with fake tzupdate,
// timeout and logger first on a PATH that holds nothing else, so the real
// tzupdate can never run. It returns what the fakes recorded.
func runDispatcher(t *testing.T, tzupdateStatus string, withTzupdate, expectRun bool, args ...string) (record string, exit int) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	script, err := filepath.Abs("../../scripts/linux/90-mailday-tzupdate")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	recordPath := filepath.Join(dir, "record")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	fake := func(name, body string) {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!"+bash+"\n"+body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	fake("timeout", "echo \"timeout $1\" >> "+recordPath+"\nshift\nexec \"$@\"\n")
	fake("logger", "echo \"logger $*\" >> "+recordPath+"\ncat >/dev/null\n")
	if withTzupdate {
		fake("tzupdate", "echo \"tzupdate $*\" >> "+recordPath+"\nexit "+tzupdateStatus+"\n")
	}
	command := exec.Command(bash, append([]string{script}, args...)...)
	command.Env = []string{"PATH=" + bin, "HOME=" + dir}
	err = command.Run()
	if exitErr, ok := err.(*exec.ExitError); ok {
		exit = exitErr.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	// The work is backgrounded: wait for its closing log line, or, when
	// nothing should run, give a wrongly started run time to show itself.
	deadline := time.Now().Add(1500 * time.Millisecond)
	if !expectRun {
		deadline = time.Now().Add(150 * time.Millisecond)
	}
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(recordPath)
		if expectRun && strings.Contains(string(data), "logger -t mailday-tzupdate tzupdate ") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	data, _ := os.ReadFile(recordPath)
	return strings.Join(strings.Fields(string(data)), " "), exit
}

func TestTzupdateDispatcher(t *testing.T) {
	for _, action := range []string{"up", "connectivity-change"} {
		joined, exit := runDispatcher(t, "0", true, true, "wlan0", action)
		if exit != 0 || !strings.Contains(joined, "timeout 20") || !strings.Contains(joined, "tzupdate -q") || !strings.Contains(joined, "tzupdate ran") {
			t.Errorf("%s: exit %d, record %q", action, exit, joined)
		}
	}
	// A connectivity-change event may carry no interface name.
	if record, exit := runDispatcher(t, "0", true, true, "", "connectivity-change"); exit != 0 || !strings.Contains(record, "tzupdate -q") {
		t.Errorf("no interface: exit %d, record %q", exit, record)
	}
}

func TestTzupdateDispatcherIgnoresWhatIsNotANewNetwork(t *testing.T) {
	for _, args := range [][]string{{"lo", "up"}, {"lo", "connectivity-change"}, {"wlan0", "down"}, {"wlan0", "dhcp4-change"}, {}} {
		record, exit := runDispatcher(t, "0", true, false, args...)
		if exit != 0 || record != "" {
			t.Errorf("%v: exit %d, record %q", args, exit, record)
		}
	}
}

func TestTzupdateDispatcherExitsZeroWhenTzupdateFailsOrIsMissing(t *testing.T) {
	record, exit := runDispatcher(t, "3", true, true, "wlan0", "up")
	if exit != 0 || !strings.Contains(record, "failed with status 3") {
		t.Errorf("failing tzupdate: exit %d, record %q", exit, record)
	}
	record, exit = runDispatcher(t, "124", true, true, "wlan0", "up")
	if exit != 0 || !strings.Contains(record, "timed out") {
		t.Errorf("slow tzupdate: exit %d, record %q", exit, record)
	}
	if record, exit = runDispatcher(t, "0", false, false, "wlan0", "up"); exit != 0 || record != "" {
		t.Errorf("no tzupdate installed: exit %d, record %q", exit, record)
	}
}
