//go:build unix

package syncd

import (
	"os/exec"
	"syscall"
)

func terminateGroupOnCancel(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		return syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
	}
}
