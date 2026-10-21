// Package legacyshop holds the AfterSchema example: Shop 1.0's database,
// Shop 2.0's, the mapping between them and the old app's report queries.
package legacyshop_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aftermarket-dev/aftermarket/internal/schema"
	"github.com/aftermarket-dev/aftermarket/internal/sqlitecli"
)

func setup(t *testing.T) (*sqlitecli.Runner, *schema.Map) {
	t.Helper()
	r := sqlitecli.New()
	if r.Available() != nil {
		t.Skip("sqlite3 is not installed")
	}
	m, err := schema.LoadMap("map.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return r, m
}

func plan(t *testing.T, r *sqlitecli.Runner, m *schema.Map, oldPath string) ([]schema.Planned, sqlitecli.Source) {
	t.Helper()
	ctx := context.Background()
	old, err := schema.Read(ctx, r, sqlitecli.SourceOf(oldPath))
	if err != nil {
		t.Fatal(err)
	}
	newSrc := sqlitecli.SourceOf(m.New)
	nw, err := schema.Read(ctx, r, newSrc)
	if err != nil {
		t.Fatal(err)
	}
	views, probs := m.Plan(old, nw)
	probs = append(probs, schema.Verify(ctx, r, newSrc, views)...)
	if len(probs) > 0 {
		t.Fatal(probs)
	}
	return views, newSrc
}

// TestOldReportsReadTheSame is the AfterSchema promise on the example: the
// old reports don't compile on Shop 2.0, and with the views every one of
// them gives the same rows as on Shop 1.0.
func TestOldReportsReadTheSame(t *testing.T) {
	r, m := setup(t)
	ctx := context.Background()
	data, _ := os.ReadFile("reports.sql")
	stmts := schema.Split(string(data))
	if len(stmts) != 12 {
		t.Fatalf("%d statements", len(stmts))
	}
	views, newSrc := plan(t, r, m, m.Old)

	compiled := 0
	for _, res := range schema.Check(ctx, r, newSrc, "", stmts) {
		if res.OK {
			compiled++
		}
	}
	if compiled != 1 {
		t.Fatalf("%d of 12 compile on 2.0 alone, want 1", compiled)
	}
	for _, res := range schema.Compare(ctx, r, sqlitecli.SourceOf(m.Old), newSrc, schema.SQL(views, "", true), stmts) {
		if !res.OK {
			t.Errorf("line %d: %s", res.Line, res.Problem)
		}
	}
	if got, _ := os.ReadFile("compat.sql"); string(got) != schema.SQL(views, "map.yaml", false) {
		t.Fatal("compat.sql is out of date: run aftermarket schema compat --map map.yaml --out compat.sql")
	}
}

// TestLostDataIsCaught: Shop 2.0 has no fax column, so the map gives NULL.
// If any 1.0 customer had a fax, the test names the report and the row
// instead of passing quietly.
func TestLostDataIsCaught(t *testing.T) {
	r, m := setup(t)
	v1, _ := os.ReadFile(m.Old)
	withFax := filepath.Join(t.TempDir(), "shop.sql")
	os.WriteFile(withFax, []byte(string(v1)+"\nUPDATE customers SET fax = '+44 20 7946 0000' WHERE id = 1;\n"), 0o600)
	views, newSrc := plan(t, r, m, withFax)
	stmts := schema.Split("SELECT id, name, fax FROM customers ORDER BY id;\nSELECT count(*) FROM customers;")
	res := schema.Compare(context.Background(), r, sqlitecli.SourceOf(withFax), newSrc, schema.SQL(views, "", true), stmts)
	if res[0].OK || !strings.Contains(res[0].Problem, "row 1 differs") || !res[1].OK {
		t.Fatalf("%+v", res)
	}
}
