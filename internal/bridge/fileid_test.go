package bridge

import "os"

func fileIDWorks() bool {
	fi, err := os.Stat(".")
	return err == nil && fileID(fi) != ""
}
