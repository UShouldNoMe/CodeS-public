//go:build linux || darwin

package tools

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// Isolated commands get a process group so cancelling the shell also stops its
// children. Stop remaining background jobs on normal completion as well, before
// the task releases its worktree lease. Linux additionally uses a PID namespace.
func isolateCommandLifetime(cmd *exec.Cmd) func() {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stop := func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.Cancel = stop
	return func() { _ = stop() }
}
