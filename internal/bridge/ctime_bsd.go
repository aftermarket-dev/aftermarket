//go:build darwin || freebsd || netbsd

package bridge

import "syscall"

func ctime(st *syscall.Stat_t) (int64, int64) {
	return int64(st.Ctimespec.Sec), int64(st.Ctimespec.Nsec)
}
