package sqlitecli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func need(t *testing.T) *Runner {
	t.Helper()
	r := New()
	if r.Available() != nil {
		t.Skip("sqlite3 is not installed")
	}
	return r
}

func dump(t *testing.T, sql string) Source {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.sql")
	os.WriteFile(p, []byte(sql), 0o644)
	return SourceOf(p)
}

func TestQueryKeepsOrderAndRepeatedNames(t *testing.T) {
	r := need(t)
	src := dump(t, "create table t(b text, a integer); insert into t values ('x', 1), ('y', 2);")
	rows, err := r.Query(context.Background(), src, "", "select b, a, a as b from t order by a")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0][0].Name != "b" || rows[0][2].Name != "b" || Str(rows[1][0].Value) != "y" || Int(rows[1][1].Value) != 2 {
		t.Fatalf("%+v", rows)
	}
	if rows, err := r.Query(context.Background(), src, "", "select * from t where 0"); err != nil || len(rows) != 0 {
		t.Fatalf("no rows: %v %v", rows, err)
	}
}

func TestErrorsNameTheirPart(t *testing.T) {
	r := need(t)
	src := dump(t, "create table t(a);\ncreate tabel u(b);\n")
	_, err := r.Query(context.Background(), src, "", "select 1")
	if e, ok := err.(*Error); !ok || !strings.HasSuffix(e.Where, "s.sql, line 2") {
		t.Fatalf("dump error: %v", err)
	}
	src = dump(t, "create table t(a);")
	_, err = r.Query(context.Background(), src, "create temp view v as select a from t;", "select nosuch from v")
	if e, ok := err.(*Error); !ok || e.Where != "the query" || !strings.Contains(e.Msg, "nosuch") {
		t.Fatalf("query error: %v", err)
	}
	_, err = r.Query(context.Background(), src, "create temp vew v as select a from t;", "select 1")
	if e, ok := err.(*Error); !ok || e.Where != "the setup" {
		t.Fatalf("setup error: %v", err)
	}
	if err := r.Check(context.Background(), src, "", "select a from t where a = ?"); err != nil {
		t.Fatalf("a valid query with a parameter: %v", err)
	}
}

// The database file is never written: writes and ATTACH are refused, and
// the file's bytes don't change.
func TestNeverWrites(t *testing.T) {
	r := need(t)
	dir := t.TempDir()
	db := filepath.Join(dir, "app.db")
	if _, err := r.Query(context.Background(), dump(t, ""), "", "select 1"); err != nil {
		t.Fatal(err)
	}
	// Build a database with the real shell, outside the runner.
	if out, err := runShell(db, "create table t(a); insert into t values (1);"); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	before, _ := os.ReadFile(db)
	src := SourceOf(db)
	for _, q := range []string{
		"insert into t values (2)",
		"create table u(b)",
		"attach 'other.db' as o",
		"pragma user_version = 7",
	} {
		if _, err := r.Query(context.Background(), src, "", q); err == nil {
			t.Errorf("%q was allowed", q)
		}
	}
	if _, err := r.Query(context.Background(), src, "create temp view v as select a from t;", "select * from v"); err != nil {
		t.Fatalf("temporary views must work on a read-only database: %v", err)
	}
	after, _ := os.ReadFile(db)
	if string(before) != string(after) {
		t.Fatal("the database file changed")
	}
	if _, err := os.Stat(filepath.Join(dir, "other.db")); err == nil {
		t.Fatal("ATTACH created a file")
	}
}

func TestQuotedIsExact(t *testing.T) {
	r := need(t)
	src := dump(t, "create table t(a, b, c, d); insert into t values (1, '1', 1.5, x'00ff');")
	out, err := r.Quoted(context.Background(), src, "", "select * from t")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "'a','b','c','d'") || !strings.Contains(out, "1,'1',1.5,X'00ff'") {
		t.Fatalf("%q", out)
	}
}

func TestMissingProgram(t *testing.T) {
	r := &Runner{Bin: "no-such-sqlite3-here"}
	if _, err := r.Query(context.Background(), Source{Path: "x.sql", SQL: true}, "", "select 1"); err != ErrNoSQLite3 {
		t.Fatalf("%v", err)
	}
}

func TestNewEnough(t *testing.T) {
	for v, want := range map[string]bool{"3.37.0": true, "3.45.1": true, "4.0": true, "3.36.9": false, "3.8.11": false, "": false, "x": false} {
		if newEnough(v) != want {
			t.Errorf("%q: want %v", v, want)
		}
	}
}
