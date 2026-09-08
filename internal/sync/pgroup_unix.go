//go:build unix

package sync

import (
	"os/exec"
	"syscall"
)

// isolate puts the command in its own process group so a timeout can kill the
// whole tree. Without this, children like npm's subprocesses keep the output
// pipe open and the job hangs past its deadline.
func isolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
