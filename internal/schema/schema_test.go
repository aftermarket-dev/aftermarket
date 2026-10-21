package schema

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aftermarket-dev/aftermarket/internal/sqlitecli"
)

func runner(t *testing.T) *sqlitecli.Runner {
	t.Helper()
	r := sqlitecli.New()
	if r.Available() != nil {
		t.Skip("sqlite3 is not installed")
	}
	return r
}

func sqlFile(t *testing.T, sql string) sqlitecli.Source {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.sql")
	if err := os.WriteFile(p, []byte(sql), 0o644); err != nil {
		t.Fatal(err)
	}
	return sqlitecli.SourceOf(p)
}

func read(t *testing.T, r *sqlitecli.Runner, sql string) *Schema {
	t.Helper()
	s, err := Read(context.Background(), r, sqlFile(t, sql))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// mustSplit is Split for test text that is known to be good.
func mustSplit(t *testing.T, text string) []Stmt {
	t.Helper()
	s, err := Split(text)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDiffMarksWhatBreaks(t *testing.T) {
	r := runner(t)
	old := read(t, r, `
CREATE TABLE a (id INTEGER PRIMARY KEY, name TEXT NOT NULL, n INT, gone TEXT, d TEXT DEFAULT 'x', v VARCHAR(10));
CREATE TABLE dropped (x);
CREATE TABLE keep (k);
CREATE INDEX a_n ON a(n);
CREATE VIEW va AS SELECT id, name FROM a;
CREATE VIEW vgone AS SELECT 1 AS one;
CREATE TRIGGER t1 AFTER INSERT ON a BEGIN SELECT 1; END;`)
	nw := read(t, r, `
CREATE TABLE a (id INTEGER PRIMARY KEY, name TEXT, n TEXT, d TEXT DEFAULT 'y', v TEXT, extra INT NOT NULL) STRICT;
CREATE TABLE keep (k PRIMARY KEY, z) WITHOUT ROWID;
CREATE TABLE added (y);
CREATE UNIQUE INDEX a_name ON a(name);
CREATE VIEW va AS SELECT id FROM a;
CREATE VIEW vnew AS SELECT 2 AS two;`)
	var lines []string
	for _, c := range Diff(old, nw) {
		mark := "safe"
		if c.Breaking {
			mark = "BREAKING"
		}
		lines = append(lines, mark+" "+c.String())
	}
	got := strings.Join(lines, "\n")
	for _, want := range []string{
		"BREAKING column a.name changed (may now be NULL)",
		"BREAKING column a.n changed (type INT -> TEXT, values read differently)",
		"BREAKING column a.gone removed",
		"safe column a.d changed (default 'x' -> 'y')",
		"safe column a.v changed (declared type VARCHAR(10) -> TEXT, same affinity)",
		"safe column a.extra added (INT NOT NULL with no default; old INSERTs that don't name it fail)",
		"safe table a changed (now STRICT; writes of the wrong type fail)",
		"safe index a(n) removed",
		"safe index a(name) added (unique index; writes of duplicates fail)",
		"BREAKING table dropped removed",
		"BREAKING table keep changed (now WITHOUT ROWID; queries using rowid fail)",
		"safe table added added",
		"BREAKING view va changed (no longer shows name)",
		"BREAKING view vgone removed",
		"safe view vnew added",
		"safe trigger t1 removed (on a)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if same := Diff(old, old); len(same) != 0 {
		t.Fatalf("a schema compared with itself: %v", same)
	}
}

func TestMapErrorsAreListed(t *testing.T) {
	_, err := ParseMap([]byte(`
version: 2
newer: x
views:
  - old: a
    columns:
      x: ""
      y: z
  - old: a
    from: b
    where: "1; DROP TABLE b"
    surprise: 1
`), ".")
	if err == nil {
		t.Fatal("accepted")
	}
	for _, want := range []string{"version must be 1", `unknown key "newer"`, "old is required", "a view needs from", "needs an SQL expression", "a is already mapped", "without ;", `unknown key "surprise"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in\n%v", want, err)
		}
	}
	if _, err := ParseMap([]byte("version: 1\nold: o\nviews:\n  - old: a\n    from: b\n    columns:\n      x: y\n      x: z\n"), "."); err == nil || !strings.Contains(err.Error(), "duplicate key") {
		t.Fatalf("a column listed twice: %v", err)
	}
	m, err := ParseMap([]byte("version: 1\nold: v1.sql\nviews:\n  - old: c\n    from: d\n    columns:\n      note: \"'a;b'\"\n"), "/base")
	if err != nil || m.Old != filepath.Join("/base", "v1.sql") || m.Views[0].Columns[0].Expr != "'a;b'" {
		t.Fatalf("%+v %v", m, err)
	}
}

func TestPlanProblems(t *testing.T) {
	r := runner(t)
	old := read(t, r, "CREATE TABLE customers (id, name, fax); CREATE TABLE orders (id, total); CREATE TABLE same (a, b);")
	nw := read(t, r, "CREATE TABLE accounts (id, first, last); CREATE TABLE same (a);")
	m, err := ParseMap([]byte(`version: 1
old: o.sql
views:
  - old: customers
    from: accounts
    columns:
      name: first || ' ' || last
      nosuch: 1
  - old: orders
    from: purchases
  - old: same
    from: accounts
  - old: ghost
    from: accounts
`), ".")
	if err != nil {
		t.Fatal(err)
	}
	views, probs := m.Plan(old, nw)
	got := fmt.Sprint(probs)
	for _, want := range []string{
		"customers had no column called nosuch",
		"old column customers.fax has no mapping and accounts has no column called fax",
		"the new schema has no table or view called purchases",
		"the new schema still has a table called same",
		"the old schema has no table or view called ghost",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %s", want, got)
		}
	}
	if len(views) != 0 {
		t.Fatalf("views with problems were kept: %v", views)
	}
}

func TestVerifyFindsBadExpressions(t *testing.T) {
	r := runner(t)
	src := sqlFile(t, "CREATE TABLE accounts (id, first);")
	nw, _ := Read(context.Background(), r, src)
	old := read(t, r, "CREATE TABLE customers (id, name);")
	m, _ := ParseMap([]byte("version: 1\nold: o.sql\nviews:\n  - old: customers\n    from: accounts\n    columns:\n      name: frist\n"), ".")
	views, probs := m.Plan(old, nw)
	if len(probs) != 0 {
		t.Fatal(probs)
	}
	probs = Verify(context.Background(), r, src, views)
	if len(probs) != 1 || !strings.Contains(probs[0].String(), "no such column: frist") || probs[0].Line != 4 {
		t.Fatalf("%v", probs)
	}
}

// The views keep every old column in its old order, so SELECT * reads the
// same; the script is the same bytes every time.
func TestSQLIsStable(t *testing.T) {
	r := runner(t)
	old := read(t, r, "CREATE TABLE c (z, a, \"we\"\"ird\");")
	nw := read(t, r, "CREATE TABLE n (a, z2, w);")
	m, _ := ParseMap([]byte("version: 1\nold: o.sql\nviews:\n  - old: c\n    from: n\n    columns:\n      z: z2\n      we\"ird: w\n"), ".")
	views, probs := m.Plan(old, nw)
	if len(probs) != 0 {
		t.Fatal(probs)
	}
	a, b := SQL(views, "m.yaml", false), SQL(views, "m.yaml", false)
	if a != b || !strings.Contains(a, `CREATE VIEW "c" ("z", "a", "we""ird") AS`) || !strings.Contains(a, "BEGIN;") {
		t.Fatalf("%s", a)
	}
	if tmp := SQL(views, "", true); !strings.HasPrefix(tmp, "CREATE TEMP VIEW") || strings.Contains(tmp, "BEGIN") {
		t.Fatalf("%s", tmp)
	}
}

// AfterSchema never writes a database: every command leaves the files
// byte for byte as they were.
func TestDatabasesAreNeverWritten(t *testing.T) {
	r := runner(t)
	dir := t.TempDir()
	oldDB, newDB := filepath.Join(dir, "old.db"), filepath.Join(dir, "new.db")
	mk := func(db, sql string) {
		if out, err := shell(r.Bin, db, sql); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	mk(oldDB, "CREATE TABLE customers (id INTEGER PRIMARY KEY, name TEXT); INSERT INTO customers VALUES (1, 'Ada');")
	mk(newDB, "CREATE TABLE accounts (id INTEGER PRIMARY KEY, first TEXT); INSERT INTO accounts VALUES (1, 'Ada');")
	before := map[string]string{}
	for _, f := range []string{oldDB, newDB} {
		b, _ := os.ReadFile(f)
		before[f] = string(b)
	}
	ctx := context.Background()
	old, _ := Read(ctx, r, sqlitecli.SourceOf(oldDB))
	nw, _ := Read(ctx, r, sqlitecli.SourceOf(newDB))
	m, _ := ParseMap([]byte("version: 1\nold: o\nviews:\n  - old: customers\n    from: accounts\n    columns:\n      name: first\n"), ".")
	views, probs := m.Plan(old, nw)
	probs = append(probs, Verify(ctx, r, sqlitecli.SourceOf(newDB), views)...)
	if len(probs) != 0 {
		t.Fatal(probs)
	}
	setup := SQL(views, "", true)
	stmts := mustSplit(t, "SELECT * FROM customers; INSERT INTO customers VALUES (2, 'Bob'); DELETE FROM accounts; DROP TABLE accounts")
	Check(ctx, r, sqlitecli.SourceOf(newDB), setup, stmts)
	res := Compare(ctx, r, sqlitecli.SourceOf(oldDB), sqlitecli.SourceOf(newDB), setup, stmts)
	if !res[0].OK || res[1].Skipped == "" {
		t.Fatalf("%+v", res)
	}
	// Even a hostile setup can't write.
	if _, err := r.Query(ctx, sqlitecli.SourceOf(newDB), "DELETE FROM accounts;", "SELECT 1"); err == nil {
		t.Fatal("a write in the setup was allowed")
	}
	for f, b := range before {
		after, _ := os.ReadFile(f)
		if string(after) != b {
			t.Fatalf("%s changed", f)
		}
	}
}

// Random tables, renamed with random column renames and expressions that
// give the old values back: every old read gives the same rows through
// the views as from the old tables.
func TestRandomRenamesReadTheSame(t *testing.T) {
	r := runner(t)
	seed := time.Now().UnixNano()
	t.Logf("seed %d", seed)
	rs := rand.New(rand.NewSource(seed))
	for round := 0; round < 6; round++ {
		var oldSQL, newSQL, mapText, queries strings.Builder
		fmt.Fprintf(&mapText, "version: 1\nold: o.sql\nviews:\n")
		for ti := 0; ti < 1+rs.Intn(3); ti++ {
			tn := fmt.Sprintf("t%d_%d", round, ti)
			ncols := 1 + rs.Intn(5)
			var oc, nc []string
			fmt.Fprintf(&mapText, "  - old: %s\n    from: n_%s\n    columns:\n", tn, tn)
			kinds := make([]int, ncols)
			for c := 0; c < ncols; c++ {
				kinds[c] = rs.Intn(4)
				oc = append(oc, fmt.Sprintf("c%d TEXT", c))
				switch kinds[c] {
				case 0: // same name
					nc = append(nc, fmt.Sprintf("c%d TEXT", c))
				case 1: // renamed
					nc = append(nc, fmt.Sprintf("r%d TEXT", c))
					fmt.Fprintf(&mapText, "      c%d: r%d\n", c, c)
				case 2: // an integer stored times 100
					oc[c] = fmt.Sprintf("c%d REAL", c)
					nc = append(nc, fmt.Sprintf("x%d INTEGER", c))
					fmt.Fprintf(&mapText, "      c%d: x%d / 100.0\n", c, c)
				case 3: // split in two
					nc = append(nc, fmt.Sprintf("a%d TEXT", c), fmt.Sprintf("b%d TEXT", c))
					fmt.Fprintf(&mapText, "      c%d: a%d || '-' || b%d\n", c, c, c)
				}
			}
			fmt.Fprintf(&oldSQL, "CREATE TABLE %s (%s);\n", tn, strings.Join(oc, ", "))
			fmt.Fprintf(&newSQL, "CREATE TABLE n_%s (%s);\n", tn, strings.Join(nc, ", "))
			for row := 0; row < 1+rs.Intn(6); row++ {
				var ov, nv []string
				for c := 0; c < ncols; c++ {
					a, b := fmt.Sprintf("'v%d''x'", rs.Intn(50)), fmt.Sprintf("'w%d'", rs.Intn(50))
					switch kinds[c] {
					case 0, 1:
						ov, nv = append(ov, a), append(nv, a)
					case 2:
						n := rs.Intn(100000)
						ov, nv = append(ov, fmt.Sprintf("%d/100.0", n)), append(nv, fmt.Sprint(n))
					case 3:
						ov = append(ov, a+" || '-' || "+b)
						nv = append(nv, a, b)
					}
				}
				fmt.Fprintf(&oldSQL, "INSERT INTO %s VALUES (%s);\n", tn, strings.Join(ov, ", "))
				fmt.Fprintf(&newSQL, "INSERT INTO n_%s VALUES (%s);\n", tn, strings.Join(nv, ", "))
			}
			fmt.Fprintf(&queries, "SELECT * FROM %s ORDER BY rowid;\nSELECT count(*) FROM %s;\n", tn, tn)
			for c := 0; c < ncols; c++ {
				fmt.Fprintf(&queries, "SELECT c%d FROM %s ORDER BY c%d, rowid;\n", c, tn, c)
			}
		}
		oldSrc, newSrc := sqlFile(t, oldSQL.String()), sqlFile(t, newSQL.String())
		ctx := context.Background()
		old, err := Read(ctx, r, oldSrc)
		if err != nil {
			t.Fatal(err)
		}
		nw, err := Read(ctx, r, newSrc)
		if err != nil {
			t.Fatal(err)
		}
		m, err := ParseMap([]byte(mapText.String()), ".")
		if err != nil {
			t.Fatalf("%v\n%s", err, mapText.String())
		}
		views, probs := m.Plan(old, nw)
		probs = append(probs, Verify(ctx, r, newSrc, views)...)
		if len(probs) != 0 {
			t.Fatalf("%v\n%s", probs, mapText.String())
		}
		for _, res := range Compare(ctx, r, oldSrc, newSrc, SQL(views, "", true), mustSplit(t, queries.String())) {
			if !res.OK {
				t.Fatalf("round %d, %s: %s\nmap:\n%s\nold:\n%s\nnew:\n%s", round, res.SQL, res.Problem, mapText.String(), oldSQL.String(), newSQL.String())
			}
		}
	}
}

// Without ORDER BY the order of rows is not part of the answer; with it,
// it is.
func TestCompareOrder(t *testing.T) {
	r := runner(t)
	old := sqlFile(t, "CREATE TABLE t (a); INSERT INTO t VALUES (1), (2), (2), (3);")
	nw := sqlFile(t, "CREATE TABLE n (a); INSERT INTO n VALUES (3), (2), (1), (2);")
	setup := "CREATE TEMP VIEW t AS SELECT a FROM n;"
	res := Compare(context.Background(), r, old, nw, setup, mustSplit(t, "SELECT a FROM t; SELECT a FROM t ORDER BY rowid; SELECT a FROM t WHERE a > 1"))
	if !res[0].OK || res[1].OK || !strings.Contains(res[1].Problem, "row 1 differs: old (1), new (3)") || !res[2].OK {
		t.Fatalf("%+v", res)
	}
	nw2 := sqlFile(t, "CREATE TABLE n (a); INSERT INTO n VALUES (3), (2), (1), (1);")
	if res := Compare(context.Background(), r, old, nw2, setup, mustSplit(t, "SELECT a FROM t")); res[0].OK {
		t.Fatal("different repeats passed")
	}
}

// Values with line breaks, carriage returns and blobs are compared whole:
// swapping halves between rows must not pass, in any order.
func TestCompareWholeValues(t *testing.T) {
	r := runner(t)
	old := sqlFile(t, "CREATE TABLE notes (body); INSERT INTO notes VALUES ('a'||char(10)||'b'), ('c'||char(10)||'d'), (x'0a00'), ('e'||char(13));")
	nw := sqlFile(t, "CREATE TABLE memos (body); INSERT INTO memos VALUES ('a'||char(10)||'d'), ('c'||char(10)||'b'), (x'0a00'), ('e'||char(13));")
	same := sqlFile(t, "CREATE TABLE memos (body); INSERT INTO memos VALUES ('e'||char(13)), (x'0a00'), ('c'||char(10)||'d'), ('a'||char(10)||'b');")
	setup := "CREATE TEMP VIEW notes AS SELECT body FROM memos;"
	ctx := context.Background()
	if res := Compare(ctx, r, old, nw, setup, mustSplit(t, "SELECT body FROM notes")); res[0].OK {
		t.Fatal("different values passed")
	} else if !strings.Contains(res[0].Problem, "row 1 differs") {
		t.Fatalf("%s", res[0].Problem)
	}
	if res := Compare(ctx, r, old, same, setup, mustSplit(t, "SELECT body FROM notes")); !res[0].OK {
		t.Fatalf("the same values in another order: %s", res[0].Problem)
	}
}

// With no rows on either side, the column names still count.
func TestCompareEmptyResults(t *testing.T) {
	r := runner(t)
	old := sqlFile(t, "CREATE TABLE c (id, name);")
	nw := sqlFile(t, "CREATE TABLE n (id, first);")
	ctx := context.Background()
	res := Compare(ctx, r, old, nw, "CREATE TEMP VIEW c AS SELECT id, first FROM n;", mustSplit(t, "SELECT * FROM c"))
	if res[0].OK || !strings.Contains(res[0].Problem, "the column names differ: old id, name, new id, first") {
		t.Fatalf("%+v", res[0])
	}
	res = Compare(ctx, r, old, nw, `CREATE TEMP VIEW c ("id", "name") AS SELECT id, first FROM n;`, mustSplit(t, "SELECT * FROM c"))
	if !res[0].OK {
		t.Fatalf("%+v", res[0])
	}
}

func TestReadsAndOrder(t *testing.T) {
	for sql, want := range map[string]bool{
		"SELECT 1":                             true,
		"  /* c */ select 1":                   true,
		"VALUES (1)":                           true,
		"WITH x AS (SELECT 1) SELECT * FROM x": true,
		"WITH x AS (SELECT 1) INSERT INTO t SELECT * FROM x": false,
		"WITH x AS (SELECT 1) DELETE FROM t":                 false,
		"EXPLAIN SELECT 1":                                   false,
		"EXPLAIN QUERY PLAN SELECT 1":                        false,
		"PRAGMA table_info(t)":                               false,
		"INSERT INTO t VALUES (1)":                           false,
	} {
		if IsRead(sql) != want {
			t.Errorf("IsRead(%q) = %v", sql, !want)
		}
	}
	for sql, want := range map[string]bool{
		"SELECT a FROM t ORDER BY a":                               true,
		"SELECT a FROM (SELECT a FROM t ORDER BY a)":               false,
		"SELECT row_number() OVER (ORDER BY a) FROM t":             false,
		"SELECT 'ORDER BY' FROM t":                                 false,
		"SELECT a FROM t -- ORDER BY a":                            false,
		"SELECT a FROM t WHERE b IN (SELECT b FROM u) order  by a": true,
	} {
		if ordered(sql) != want {
			t.Errorf("ordered(%q) = %v", sql, !want)
		}
	}
}

// check compiles EXPLAIN statements and triggers as they are.
func TestCheckExplainAndTriggers(t *testing.T) {
	r := runner(t)
	src := sqlFile(t, "CREATE TABLE t (a);")
	stmts := mustSplit(t, "EXPLAIN QUERY PLAN SELECT a FROM t;\nCREATE TRIGGER tr AFTER INSERT ON t BEGIN\n  SELECT 1;\n  SELECT 2;\nEND;\nSELECT a FROM t")
	if len(stmts) != 3 {
		t.Fatalf("%q", stmts)
	}
	for _, res := range Check(context.Background(), r, src, "", stmts) {
		if !res.OK {
			t.Errorf("line %d: %s", res.Line, res.Problem)
		}
	}
}

// SQL in the mapping must be one piece that ends where it seems to.
func TestMapFragments(t *testing.T) {
	for _, bad := range []string{
		`deleted_at IS NULL /* ' */; DELETE FROM accounts`,
		`deleted_at IS NULL -- 2.0 keeps deleted`,
		"deleted_at IS NULL\n/\n.trace x",
		"deleted_at IS NULL\nGO",
		`"x;y" ; 1`,
		`'never ends`,
		`1 /* never ends`,
	} {
		yaml := "version: 1\nold: o\nviews:\n  - old: a\n    from: b\n    where: " + quoteYAML(bad) + "\n    columns:\n      c: " + quoteYAML(bad) + "\n"
		_, err := ParseMap([]byte(yaml), ".")
		if err == nil || strings.Count(err.Error(), "line ") < 2 {
			t.Errorf("%q: %v", bad, err)
		}
	}
	good := "version: 1\nold: o\nviews:\n  - old: a\n    from: b\n    where: \"note = 'x;y' /* ok */\"  # a YAML comment\n    columns:\n      c: \"\\\"we;ird\\\" || ';'\"\n"
	if _, err := ParseMap([]byte(good), "."); err != nil {
		t.Fatal(err)
	}
}

func quoteYAML(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s) + `"`
}

func TestDiffDetails(t *testing.T) {
	r := runner(t)
	old := read(t, r, `CREATE TABLE customers (id, name, fax);
CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT DEFAULT 'WITHOUT ROWID');
CREATE TABLE ids (id INTEGER PRIMARY KEY, v);
CREATE VIRTUAL TABLE docs USING fts5(body);`)
	nw := read(t, r, `CREATE TABLE accounts (id, name);
CREATE VIEW customers AS SELECT id FROM accounts;
CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT DEFAULT 'WITHOUT ROWID');
CREATE TABLE ids (id INT PRIMARY KEY, v);
CREATE VIRTUAL TABLE docs USING fts5(body);`)
	var lines []string
	for _, c := range Diff(old, nw) {
		lines = append(lines, fmt.Sprintf("%v %s", c.Breaking, c))
	}
	got := strings.Join(lines, "\n")
	for _, want := range []string{
		"true table customers changed (now a view that no longer shows name, fax)",
		"true column ids.id changed (declared type INTEGER -> INT, same affinity; no longer the rowid; its values can differ from rowid)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	for _, not := range []string{"notes", "docs_", "rank"} {
		if strings.Contains(got, not) {
			t.Errorf("%q should not appear in\n%s", not, got)
		}
	}
	if d := old.Table("docs"); d == nil || len(d.Columns) != 1 {
		t.Fatalf("the virtual table's hidden columns were kept: %+v", d)
	}
}

// The mapping's file name goes into a comment in compat.sql; a line break
// in it must not end the comment. From the third 0.8.0 review.
func TestMapNameStaysInTheComment(t *testing.T) {
	r := runner(t)
	old := read(t, r, "CREATE TABLE c (a);")
	nw := read(t, r, "CREATE TABLE n (a);")
	m, _ := ParseMap([]byte("version: 1\nold: o\nviews:\n  - old: c\n    from: n\n"), ".")
	views, probs := m.Plan(old, nw)
	if len(probs) != 0 {
		t.Fatal(probs)
	}
	sql := SQL(views, "m\nDELETE FROM accounts AS\n--", false)
	stmts := mustSplit(t, sql)
	if !ScriptShape(stmts, 1) || strings.Contains(sql, "\nDELETE") {
		t.Fatalf("%s", sql)
	}
	// A script with something else in place of BEGIN is refused.
	bad := mustSplit(t, "DELETE FROM accounts AS BEGIN;\nDROP VIEW IF EXISTS c;\nCREATE VIEW c AS SELECT 1;\nCOMMIT;")
	if ScriptShape(bad, 1) {
		t.Fatal("a script with a DELETE passed")
	}
}
