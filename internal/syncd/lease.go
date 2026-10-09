package syncd

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/aronvandepol/mailday/internal/config"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A Lease lets one machine at a time run a job that both have installed (the
// mail tagger): the holder renews it on every run, the other machine skips
// while it is fresh, and takes over once the holder has been away for the
// lease's length. It lives in a folder Syncthing shares between the machines.
type Lease struct {
	Holder string    `json:"holder"`
	Until  time.Time `json:"until"`
}

// LeaseDir is MAILDAY_LEASE_DIR, or leases/ in the shared_dir.
func LeaseDir() string {
	if value := os.Getenv("MAILDAY_LEASE_DIR"); value != "" {
		return value
	}
	return config.SharedPath("leases")
}

// ErrLeaseHeld means another machine holds the lease.
var ErrLeaseHeld = errors.New("lease held elsewhere")

// TakeLease claims or renews the named lease for this machine.
func TakeLease(name string, length time.Duration, now time.Time) (Lease, error) {
	host, _ := os.Hostname()
	host = strings.TrimSuffix(host, ".local")
	path := filepath.Join(LeaseDir(), name+".json")
	var current Lease
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &current)
	}
	if current.Holder != "" && current.Holder != host && now.Before(current.Until) {
		return current, fmt.Errorf("%w: %s until %s", ErrLeaseHeld, current.Holder, current.Until.Local().Format("15:04"))
	}
	lease := Lease{Holder: host, Until: now.Add(length)}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return lease, err
	}
	data, _ := json.MarshalIndent(lease, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return lease, err
	}
	return lease, os.Rename(tmp, path)
}
