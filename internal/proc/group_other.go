//go:build !unix

package proc

import "os/exec"

// OwnGroup leaves cmd as it is; WaitDelay still bounds the wait.
func OwnGroup(cmd *exec.Cmd) {}
