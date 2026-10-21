package schema

import (
	"os/exec"
	"strings"
)

// shell builds test databases with the plain sqlite3 shell.
func shell(bin, db, sql string) ([]byte, error) {
	cmd := exec.Command(bin, "-batch", "-bail", db)
	cmd.Stdin = strings.NewReader(sql)
	return cmd.CombinedOutput()
}
