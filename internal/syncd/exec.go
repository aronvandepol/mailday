package syncd

import (
	"context"
	"os/exec"
	"time"
)

// newCommand prepares a child process the daemon must be able to stop: it
// runs in its own process group and, when ctx ends, the whole group gets
// SIGTERM rather than only the direct child (the shell), so a grandchild
// holding the output pipe cannot keep Wait blocked. WaitDelay then bounds the
// wait for stragglers; callers that expect a slow clean-up raise it.
func newCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	command := exec.CommandContext(ctx, name, args...)
	terminateGroupOnCancel(command)
	command.WaitDelay = 10 * time.Second
	return command
}
