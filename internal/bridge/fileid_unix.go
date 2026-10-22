//go:build linux || darwin || freebsd || netbsd || openbsd

package bridge

import (
	"fmt"
	"os"
	"syscall"
)

// fileID names the file itself and when its inode last changed, so a file
// replaced by rename, or rewritten with its old size and modification time
// put back, still counts as changed.
func fileID(fi os.FileInfo) string {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return ""
	}
	sec, nsec := ctime(st)
	return fmt.Sprintf("%d:%d:%d.%d", st.Dev, st.Ino, sec, nsec)
}
