//go:build !(linux || darwin || freebsd || netbsd || openbsd)

package bridge

import "os"

// fileID has nothing more to add where the system gives no inode.
func fileID(os.FileInfo) string { return "" }
