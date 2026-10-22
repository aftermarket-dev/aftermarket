// Package legacyexports holds the AfterBridge example: an old app's nightly
// orders exports and its SQLite inventory, served as a read-only JSON API.
package legacyexports_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aftermarket-dev/aftermarket/internal/bridge"
	"github.com/aftermarket-dev/aftermarket/internal/fixture"
	"github.com/aftermarket-dev/aftermarket/internal/sqlitecli"
)

func TestExampleFixture(t *testing.T) {
	r := sqlitecli.New()
	if r.Available() != nil {
		t.Skip("sqlite3 is not installed")
	}
	cfg, err := bridge.LoadFile("bridge.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Settle = 0
	srv := bridge.New(cfg, r, nil)
	if probs := srv.LoadAll(context.Background()); len(probs) > 0 {
		t.Fatal(probs)
	}
	fx, err := fixture.Load("fixture.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, res := range fixture.Run(func(*url.URL) (http.Handler, error) { return srv, nil }, fx) {
		if !res.Pass {
			t.Errorf("%s: %v", res.Name, res.Problems)
		}
	}
}

// The inventory as a real database file answers exactly as the dump does.
func TestInventoryDatabaseFile(t *testing.T) {
	r := sqlitecli.New()
	if r.Available() != nil {
		t.Skip("sqlite3 is not installed")
	}
	db := filepath.Join(t.TempDir(), "inventory.db")
	dump, _ := os.ReadFile("inventory.sql")
	cmd := exec.Command(r.Bin, db)
	cmd.Stdin = bytes.NewReader(dump)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	answer := func(dbPath string) string {
		data, _ := os.ReadFile("bridge.yaml")
		abs, _ := filepath.Abs(".")
		cfg, err := bridge.Parse([]byte(strings.Replace(string(data), "db: inventory.sql", "db: "+dbPath, 1)), abs, "bridge.yaml")
		if err != nil {
			t.Fatal(err)
		}
		cfg.Settle = 0
		srv := bridge.New(cfg, r, nil)
		srv.LoadAll(context.Background())
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest("GET", "/products/BK-001", nil))
		return rec.Body.String()
	}
	a, b := answer("inventory.sql"), answer(db)
	if a != b || a != `{"sku":"BK-001","title":"Notes on the Analytical Engine","price":24.5,"in_stock":12,"discontinued":false}`+"\n" {
		t.Fatalf("dump %s\ndb   %s", a, b)
	}
}
