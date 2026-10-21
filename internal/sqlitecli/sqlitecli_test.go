package sqlitecli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
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

func TestExactIsExact(t *testing.T) {
	r := need(t)
	src := dump(t, "create table t(a, b, c, d); insert into t values (1, '1', 1.5, x'00ff'), (2, 'x'||char(10)||'y', NULL, 'NULL');")
	rows, err := r.Exact(context.Background(), src, "", "select * from t")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"a,b,c,d) VALUES(1,'1',1.5,X'00ff');",
		"a,b,c,d) VALUES(2,replace('x\\ny','\\n',char(10)),NULL,'NULL');",
	}
	if strings.Join(rows, "|") != strings.Join(want, "|") {
		t.Fatalf("%q", rows)
	}
	if rows, err := r.Exact(context.Background(), src, "", "select a as \"x\ny\" from t"); err != nil || len(rows) != 2 || !strings.HasPrefix(rows[0], "\"x\ny\") VALUES(1)") {
		t.Fatalf("a name with a line break: %q %v", rows, err)
	}
	if rows, err := r.Exact(context.Background(), src, "", "select * from t where 0"); err != nil || len(rows) != 0 {
		t.Fatalf("no rows: %q %v", rows, err)
	}
}

func TestSplit(t *testing.T) {
	text := "-- header\nSELECT 1;\n\n/* a ; comment */ SELECT 'a;b', \"x;y\", [z;w], `q;r`\n  FROM t; -- trailing ; note\nSELECT 'it''s';\n;;\n  -- only a comment\n" +
		"CREATE TEMP TRIGGER tr AFTER INSERT ON t BEGIN\n  UPDATE t SET a = 1;\n  SELECT 1;\nEND;\nSELECT 2"
	got, err := Split(text)
	if err != nil {
		t.Fatal(err)
	}
	want := []Stmt{
		{2, "SELECT 1"},
		{4, "SELECT 'a;b', \"x;y\", [z;w], `q;r`\n  FROM t"},
		{6, "SELECT 'it''s'"},
		{9, "CREATE TEMP TRIGGER tr AFTER INSERT ON t BEGIN\n  UPDATE t SET a = 1;\n  SELECT 1;\nEND"},
		{13, "SELECT 2"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %q", got)
	}
}

// Text that the sqlite3 shell would read as its own command, or that ends
// differently for the shell than for SQLite, is refused before it is sent.
func TestShellCommandsAreRefused(t *testing.T) {
	for _, text := range []string{
		"SELECT 1;\n.trace out.txt\nSELECT 2;",
		".shell touch x",
		"SELECT 1\nGO\n.trace out.txt",
		"SELECT 1\n  go  \nSELECT 2",
		"SELECT 1\n/\nSELECT 2",
		"SELECT 1;\n#x\n",
		"SELECT 'never ends",
		"SELECT 1 /* never ends",
		"SELECT [never ends",
		"SELECT \"never ends",
		// From the second review: a statement moved to the start of a line,
		// NUL bytes, and terminators followed by comments.
		"SELECT 1; .trace out.txt",
		"CREATE TABLE a(x);\n  .trace out.txt",
		"SELECT 1 \x00'\n;\n.trace out.txt\n'",
		"SELECT 1 AS GO\nGO -- x\n.trace out.txt\n;",
		"SELECT 1\n/ /* x */\n.trace out.txt",
		"SELECT 1\nGO /* c */\n.trace out.txt",
		"SELECT 1\n\tgo\r\n.trace out.txt",
		"SELECT 1\x01",
		// Refused though the shell would accept them: GO and / lines are
		// refused even inside strings and comments.
		"SELECT 'a\nGO\n'",
		"SELECT 1 /*\n/\n*/",
	} {
		if _, err := Split(text); err == nil {
			t.Errorf("%q was accepted", text)
		}
	}
	// The same characters where the shell can't take them as commands.
	for _, text := range []string{
		"SELECT 'a\n.trace x\n'",
		"SELECT 1 /*\n.trace x\n*/",
		"SELECT\n  .5",
		"SELECT\n.5",
		"SELECT 1 -- go",
		"SELECT 10\n  / 2",
		"SELECT 1 AS gone\n",
		"SELECT 1\n//",
		"CREATE TRIGGER t AFTER INSERT ON x BEGIN\n  SELECT 1;\n.5;\nEND",
	} {
		if _, err := Split(text); err != nil {
			t.Errorf("%q: %v", text, err)
		}
	}
}

// The attacks from the 0.8.0 review: none may write a file, and none may
// put words in the output.
func TestNothingIsWrittenThroughTheShell(t *testing.T) {
	r := need(t)
	dir := t.TempDir()
	victim := filepath.Join(dir, "victim.db")
	if out, err := exec.Command(r.Bin, victim, "create table t(a); insert into t values (1);").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	before, _ := os.ReadFile(victim)
	trace := filepath.Join(dir, "trace.txt")
	evil := []string{
		"SELECT 1\nGO\n.trace " + trace + "\nSELECT 'x'",
		"SELECT 1\n/\n.trace " + victim + "\nSELECT 'pwned'",
		"SELECT 1;\n.trace " + trace,
		".testcase x\nSELECT 1",
		"SELECT 1;\n.print fake\nSELECT 2 /*",
	}
	ctx := context.Background()
	for _, q := range evil {
		if _, err := r.Query(ctx, Source{Path: victim}, "", q); err == nil {
			t.Errorf("query %q ran", q)
		}
		if _, err := r.Query(ctx, Source{Path: victim}, q, "select 1"); err == nil {
			t.Errorf("setup %q ran", q)
		}
		if err := r.Check(ctx, Source{Path: victim}, "", q); err == nil {
			t.Errorf("check %q ran", q)
		}
		f := filepath.Join(dir, "evil.sql")
		os.WriteFile(f, []byte("create table t(a);\n"+q+";\n"), 0o600)
		if _, err := r.Query(ctx, SourceOf(f), "", "select 1"); err == nil {
			t.Errorf("schema file %q ran", q)
		}
	}
	// A query may print the old marker; it is chosen afresh each run.
	rows, err := r.Query(ctx, Source{Path: victim}, "", "select '@@aftermarket-output@@' as m")
	if err != nil || len(rows) != 1 || Str(rows[0].Get("m")) != "@@aftermarket-output@@" {
		t.Fatalf("%v %v", rows, err)
	}
	rows, err = r.Query(ctx, Source{Path: victim}, "", "select '@SENTINEL@' as m")
	if err != nil || len(rows) != 1 || Str(rows[0].Get("m")) != "@SENTINEL@" {
		t.Fatalf("the placeholder in user SQL was replaced: %v %v", rows, err)
	}
	if _, err := os.Stat(trace); err == nil {
		t.Fatal("a trace file was written")
	}
	if after, _ := os.ReadFile(victim); string(after) != string(before) {
		t.Fatal("the database changed")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "victim.db" && e.Name() != "evil.sql" {
			t.Errorf("unexpected file %s", e.Name())
		}
	}
	wd, _ := os.Getwd()
	if _, err := os.Stat(filepath.Join(wd, "testcase-out.txt")); err == nil {
		t.Fatal("testcase-out.txt appeared")
	}
}

func TestErrorLinesPointIntoTheFile(t *testing.T) {
	r := need(t)
	src := dump(t, "create table t(a);\n\n-- a comment\ncreate table u(\n  b,\n  c,, d);\n")
	_, err := r.Query(context.Background(), src, "", "select 1")
	var e *Error
	if !errors.As(err, &e) || e.Where != src.Path+", line 4" {
		t.Fatalf("%#v", err)
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
