//go:build unix

package proc

import (
	"os/exec"
	"syscall"
)

// OwnGroup runs cmd in its own process group and kills the whole group when
// its context ends, so children holding stdout open die with it.
func OwnGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
