package when

import (
	"os"
	"testing"
	"time"
)

// The expected times in this package's tests are written in Amsterdam time;
// pin the zone so they pass wherever the machine is (Seoul, on the road).
// Tests about other zones set time.Local themselves.
func TestMain(m *testing.M) {
	if zone, err := time.LoadLocation("Europe/Amsterdam"); err == nil {
		time.Local = zone
	}
	os.Exit(m.Run())
}
