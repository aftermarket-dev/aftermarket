//go:build unix

package hook

import (
	"os/exec"
	"syscall"
)

// ownGroup runs cmd in its own process group and kills the whole group when
// its context ends, so children holding stdout open die with it.
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
