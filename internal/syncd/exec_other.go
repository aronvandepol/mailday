//go:build !unix

package syncd

import "os/exec"

// Without process groups the default cancellation (kill the child) stays.
func terminateGroupOnCancel(*exec.Cmd) {}
