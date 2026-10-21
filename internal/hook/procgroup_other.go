//go:build !unix

package hook

import "os/exec"

// ownGroup leaves cmd as it is; WaitDelay still bounds the wait.
func ownGroup(cmd *exec.Cmd) {}
