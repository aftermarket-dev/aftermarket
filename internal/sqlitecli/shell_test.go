package sqlitecli

import (
	"os/exec"
	"strings"
)

// runShell builds test databases with the plain sqlite3 shell.
func runShell(db, sql string) ([]byte, error) {
	cmd := exec.Command(New().Bin, "-batch", "-bail", db)
	cmd.Stdin = strings.NewReader(sql)
	return cmd.CombinedOutput()
}
