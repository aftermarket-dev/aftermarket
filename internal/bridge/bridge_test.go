package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aftermarket-dev/aftermarket/internal/audit"
	"github.com/aftermarket-dev/aftermarket/internal/sqlitecli"
)

// setup writes files into a fresh folder and loads the config from it.
func setup(t *testing.T, cfg string, files map[string]string) (*Config, string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(dir, "bridge.yaml"), []byte(cfg), 0o644)
	c, err := LoadFile(filepath.Join(dir, "bridge.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	c.Settle = 0 // files written by the test are complete; tests of settle set it
	return c, dir
}

func serve(t *testing.T, c *Config, r *sqlitecli.Runner) (*Server, *bytes.Buffer) {
	t.Helper()
	var log bytes.Buffer
	s := New(c, r, audit.New(&syncWriter{w: &log}))
	s.LoadAll(context.Background())
	return s, &log
}

type syncWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

func get(h http.Handler, method, path string) (int, http.Header, string) {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec.Code, rec.Header(), rec.Body.String()
}

func needSQLite(t *testing.T) *sqlitecli.Runner {
	t.Helper()
	r := sqlitecli.New()
	if r.Available() != nil {
		t.Skip("sqlite3 is not installed")
	}
	return r
}

const ordersCfg = `version: 1
sources:
  - id: orders
    csv: {file: orders.csv}
    key: id
    columns: {id: integer, total: number, paid: boolean, placed: date}
    exclude: [note]
endpoints:
  - path: /orders
    source: orders
    filters: [paid, placed, customer]
  - path: /orders/{id}
    source: orders
  - path: /v2/orders
    source: orders
    fields: {number: id, who: customer}
`

const ordersCSV = "id,customer,total,paid,placed,note\n" +
	"1,Ada,39.80,yes,2026-10-19,secret\n" +
	"2,\"Babbage, C\",15,no,2026-10-20,\n" +
	"3,Grace,,true,,\"multi\nline\"\n"

func TestServeOrders(t *testing.T) {
	c, _ := setup(t, ordersCfg, map[string]string{"orders.csv": ordersCSV})
	s, log := serve(t, c, nil)
	for _, tc := range []struct {
		method, path string
		status       int
		body         string
	}{
		{"GET", "/orders", 200, `{"items":[{"id":1,"customer":"Ada","total":39.8,"paid":true,"placed":"2026-10-19"},{"id":2,"customer":"Babbage, C","total":15.0,"paid":false,"placed":"2026-10-20"},{"id":3,"customer":"Grace","total":null,"paid":true,"placed":null}],"total":3,"limit":100,"offset":0}` + "\n"},
		{"GET", "/orders?paid=true&limit=1&offset=1", 200, `{"items":[{"id":3,"customer":"Grace","total":null,"paid":true,"placed":null}],"total":2,"limit":1,"offset":1}` + "\n"},
		{"GET", "/orders?customer=Babbage%2C%20C", 200, `{"items":[{"id":2,"customer":"Babbage, C","total":15.0,"paid":false,"placed":"2026-10-20"}],"total":1,"limit":100,"offset":0}` + "\n"},
		{"GET", "/orders?offset=10", 200, `{"items":[],"total":3,"limit":100,"offset":10}` + "\n"},
		{"GET", "/orders/2", 200, `{"id":2,"customer":"Babbage, C","total":15.0,"paid":false,"placed":"2026-10-20"}` + "\n"},
		{"GET", "/orders/002", 200, `{"id":2,"customer":"Babbage, C","total":15.0,"paid":false,"placed":"2026-10-20"}` + "\n"},
		{"GET", "/v2/orders?limit=1", 200, `{"items":[{"number":1,"who":"Ada"}],"total":3,"limit":1,"offset":0}` + "\n"},
		{"GET", "/orders/x", 404, `{"error":"no such id"}` + "\n"},
		{"GET", "/orders/9", 404, `{"error":"no such id"}` + "\n"},
		{"GET", "/orders/2?x=1", 400, `{"error":"this endpoint takes no query parameters"}` + "\n"},
		{"GET", "/orders?paid=maybe", 400, `{"error":"paid must be true or false"}` + "\n"},
		{"GET", "/orders?placed=yesterday", 400, `{"error":"placed must be a date such as 2026-10-22"}` + "\n"},
		{"GET", "/orders?note=secret", 400, `{"error":"unknown query parameter \"note\""}` + "\n"},
		{"GET", "/orders?paid=true&paid=false", 400, `{"error":"paid is given more than once"}` + "\n"},
		{"GET", "/orders?limit=0", 400, `{"error":"limit must be a whole number from 1 to 1000"}` + "\n"},
		{"GET", "/orders?limit=1001", 400, `{"error":"limit must be a whole number from 1 to 1000"}` + "\n"},
		{"GET", "/orders?offset=-1", 400, `{"error":"offset must be a whole number from 0"}` + "\n"},
		{"GET", "/orders?%zz", 400, `{"error":"the query string can't be read"}` + "\n"},
		{"GET", "/orders/", 404, `{"error":"no such endpoint"}` + "\n"},
		{"GET", "/Orders", 404, `{"error":"no such endpoint"}` + "\n"},
		{"GET", "/orders/1/x", 404, `{"error":"no such endpoint"}` + "\n"},
		{"GET", "/orders/..%2f..%2fetc", 400, `{"error":"the path can't be read one way only"}` + "\n"},
		{"GET", "/x/../orders", 400, `{"error":"the path can't be read one way only"}` + "\n"},
		{"GET", "/orders/%00", 400, `{"error":"the path can't be read one way only"}` + "\n"},
		{"POST", "/orders", 405, `{"error":"only GET and HEAD are answered"}` + "\n"},
		{"DELETE", "/orders/1", 405, `{"error":"only GET and HEAD are answered"}` + "\n"},
		{"GET", "/_aftermarket/healthz", 200, "ok\n"},
	} {
		code, h, body := get(s, tc.method, tc.path)
		if code != tc.status || body != tc.body {
			t.Errorf("%s %s: %d %s", tc.method, tc.path, code, body)
		}
		if tc.status != 200 || tc.path != "/_aftermarket/healthz" {
			if h.Get("Content-Length") != strconv.Itoa(len(body)) {
				t.Errorf("%s %s: Content-Length %q for %d bytes", tc.method, tc.path, h.Get("Content-Length"), len(body))
			}
		}
		if tc.status == 405 && h.Get("Allow") != "GET, HEAD" {
			t.Errorf("405 without Allow")
		}
	}
	code, h, body := get(s, "HEAD", "/orders/1")
	if code != 200 || body != "" || h.Get("Content-Length") == "0" || h.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("HEAD: %d %q %v", code, body, h)
	}
	// The log has no query values and no keys from the path.
	if strings.Contains(log.String(), "secret") || strings.Contains(log.String(), "Babbage") || strings.Contains(log.String(), "maybe") || strings.Contains(log.String(), "/orders/2") {
		t.Fatalf("the log holds request values:\n%s", log.String())
	}
	if !strings.Contains(log.String(), `"path":"/orders/{id}"`) || !strings.Contains(log.String(), `"query_keys":["paid"]`) {
		t.Fatalf("log:\n%s", log.String())
	}
}

func TestConfigProblemsAreListed(t *testing.T) {
	_, err := Parse([]byte(`version: 2
surprise: 1
limits: {max_rows: 0, page_size: 50, max_page_size: 10, check_every: 1ms}
sources:
  - id: a
    csv: {file: a.csv, folder: x}
    columns: {n: float, m: integer}
  - id: a
    sqlite: {db: x.db, table: t, query: "SELECT 1"}
  - id: q
    sqlite: {db: x.db, query: "DELETE FROM t"}
  - id: k
    csv: {file: k.csv}
    key: id
    exclude: [id]
  - csv: {folder: f, pattern: "a/b*.csv"}
endpoints:
  - path: /a/{id}
    source: a
  - path: orders
    source: nope
  - path: /{id}
    source: k
  - path: /x/{id}/y
    source: k
  - path: /b
    source: k
    filters: [limit]
  - path: /c
    source: k
  - path: /c
    source: k
  - path: /_aftermarket/x
    source: k
`), ".", "bridge.yaml")
	if err == nil {
		t.Fatal("accepted")
	}
	for _, want := range []string{
		"version must be 1", `unknown key "surprise"`, "max_rows must be", "check_every must be", "page_size 50 is above max_page_size 10",
		"file, or folder with pattern, not both", "column n: type must be",
		"source a is already defined", "table or query, one of them", "query must be one SELECT",
		"the key column id can't be excluded", "a source needs an id", "pattern \"a/b*.csv\"",
		"source a needs key:", "no source called nope", "must start with /", "only be the last part",
		"paging parameter", "answers the same URLs", "/_aftermarket",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in\n%v", want, err)
		}
	}
}

func TestBadCSVIsNeverServed(t *testing.T) {
	cases := map[string]string{
		"no final newline": "id,customer\n1,Ada",
		"empty":            "",
		"bad integer":      "id,customer\n1,Ada\nx,Bob\n",
		"duplicate key":    "id,customer\n1,Ada\n1,Bob\n",
		"missing key":      "id,customer\n1,Ada\n,Bob\n",
		"short row":        "id,customer\n1,Ada\n2\n",
		"open quote":       "id,customer\n1,\"Ada\n",
		"duplicate header": "id,id\n1,2\n",
		"missing column":   "customer\nAda\n",
		"invalid UTF-8":    "id,customer\n1,\xff\n",
		"too many rows":    "id,customer\n1,a\n2,b\n3,c\n4,d\n",
		"customer gone":    "id,name\n1,Ada\n",
	}
	cfg := `version: 1
limits: {max_rows: 3}
sources:
  - id: o
    csv: {file: o.csv}
    key: id
    columns: {id: integer}
endpoints:
  - path: /o
    source: o
    filters: [customer]
`
	for name, body := range cases {
		c, _ := setup(t, cfg, map[string]string{"o.csv": body})
		s, log := serve(t, c, nil)
		if code, _, _ := get(s, "GET", "/o"); code != 503 {
			t.Errorf("%s: served with %d", name, code)
		}
		if !strings.Contains(log.String(), `"event":"kept"`) || strings.Contains(log.String(), "Bob") {
			t.Errorf("%s: log %s", name, log.String())
		}
	}
	// A good file, then a broken one: the good snapshot stays, logged once.
	c, dir := setup(t, cfg, map[string]string{"o.csv": "id,customer\n1,Ada\n"})
	s, log := serve(t, c, nil)
	os.WriteFile(filepath.Join(dir, "o.csv"), []byte("id,customer\n1,Ada\nx,Bob\n"), 0o644)
	bump(t, filepath.Join(dir, "o.csv"))
	for i := 0; i < 4; i++ {
		s.Check(context.Background())
	}
	if _, _, body := get(s, "GET", "/o"); !strings.Contains(body, `"total":1`) {
		t.Fatalf("%s", body)
	}
	if n := strings.Count(log.String(), `"event":"kept"`); n != 1 || !strings.Contains(log.String(), "line 3, column id: not a whole number; still serving the last good snapshot") {
		t.Fatalf("%d kept events:\n%s", n, log.String())
	}
}

// bump moves a file's modification time back by a random amount, so a
// rewrite within the same clock tick still looks changed.
func bump(t *testing.T, p string) {
	t.Helper()
	at := time.Now().Add(-time.Duration(rand.Intn(1000)+1) * time.Second)
	os.Chtimes(p, at, at)
}

func TestCSVDetails(t *testing.T) {
	cfg := `version: 1
sources:
  - id: s
    csv: {file: s.csv, delimiter: ";"}
    columns: {n: number, b: boolean, d: date, i: integer}
endpoints:
  - path: /s
    source: s
`
	c, _ := setup(t, cfg, map[string]string{"s.csv": "\xef\xbb\xbfn;b;d;i;t\r\n 1e3 ;NO;2026-02-28;-7;\" x \"\r\n0.1;1;;9223372036854775807;\r\n"})
	s, _ := serve(t, c, nil)
	_, _, body := get(s, "GET", "/s")
	want := `{"items":[{"n":1000.0,"b":false,"d":"2026-02-28","i":-7,"t":" x "},{"n":0.1,"b":true,"d":null,"i":9223372036854775807,"t":""}],"total":2,"limit":100,"offset":0}` + "\n"
	if body != want {
		t.Fatalf("%s", body)
	}
	for _, bad := range []string{"n;b;d;i\nNaN;1;;1\n", "n;b;d;i\n1;2;;1\n", "n;b;d;i\n1;1;2026-02-30;1\n", "n;b;d;i\n1;1;;1.5\n"} {
		c, _ := setup(t, cfg, map[string]string{"s.csv": bad})
		s, _ := serve(t, c, nil)
		if code, _, _ := get(s, "GET", "/s"); code != 503 {
			t.Errorf("%q served", bad)
		}
	}
}

func TestNewestExportByName(t *testing.T) {
	cfg := `version: 1
sources:
  - id: o
    csv: {folder: exports, pattern: "orders-*.csv"}
endpoints:
  - path: /o
    source: o
`
	c, dir := setup(t, cfg, map[string]string{
		"exports/orders-2026-10-20.csv": "day\n20\n",
		"exports/orders-2026-10-21.csv": "day\n21\n",
		"exports/other-2026-10-22.csv":  "day\n22\n",
	})
	os.Mkdir(filepath.Join(dir, "exports", "orders-2026-10-30.csv"), 0o755) // a folder is not an export
	s, _ := serve(t, c, nil)
	if _, _, body := get(s, "GET", "/o"); !strings.Contains(body, `"day":"21"`) {
		t.Fatalf("%s", body)
	}
	// A new day appears. It is read once it has been left alone for settle.
	c.Settle = 300 * time.Millisecond
	os.WriteFile(filepath.Join(dir, "exports", "orders-2026-10-22.csv"), []byte("day\n22\n"), 0o644)
	s.Check(context.Background())
	if _, _, body := get(s, "GET", "/o"); !strings.Contains(body, `"day":"21"`) {
		t.Fatalf("read before it settled: %s", body)
	}
	time.Sleep(350 * time.Millisecond)
	s.Check(context.Background())
	if _, _, body := get(s, "GET", "/o"); !strings.Contains(body, `"day":"22"`) {
		t.Fatalf("%s", body)
	}
}

func TestSQLiteSources(t *testing.T) {
	r := needSQLite(t)
	dump := `CREATE TABLE p (sku TEXT PRIMARY KEY, name TEXT, price REAL, n INTEGER, flag INTEGER, photo BLOB, big INTEGER);
INSERT INTO p VALUES ('a', 'it''s', 0.1, 1, 1, x'00', 9007199254740993), ('b', 'x' || char(10) || 'y', 3, NULL, 0, NULL, -1);
UPDATE p SET price = 0.1 + 0.2 WHERE sku = 'a';`
	cfg := `version: 1
sources:
  - id: p
    sqlite: {db: p.sql, table: p}
    key: sku
    columns: {flag: boolean}
    exclude: [photo]
  - id: q
    sqlite: {db: p.sql, query: "SELECT sku, n * 2 AS twice FROM p ORDER BY sku DESC"}
  - id: blob
    sqlite: {db: p.sql, table: p}
endpoints:
  - path: /p/{sku}
    source: p
  - path: /q
    source: q
  - path: /blob
    source: blob
`
	c, _ := setup(t, cfg, map[string]string{"p.sql": dump})
	s, log := serve(t, c, r)
	for path, want := range map[string]string{
		"/p/a": `{"sku":"a","name":"it's","price":0.30000000000000004,"n":1,"flag":true,"big":9007199254740993}` + "\n",
		"/p/b": `{"sku":"b","name":"x\ny","price":3.0,"n":null,"flag":false,"big":-1}` + "\n",
		"/q":   `{"items":[{"sku":"b","twice":null},{"sku":"a","twice":2}],"total":2,"limit":100,"offset":0}` + "\n",
	} {
		if _, _, body := get(s, "GET", path); body != want {
			t.Errorf("%s: %s", path, body)
		}
	}
	if code, _, _ := get(s, "GET", "/blob"); code != 503 || !strings.Contains(log.String(), "holds a blob") {
		t.Fatalf("blob: %d %s", code, log.String())
	}
	for _, bad := range []string{
		`columns: {flag: date}`, `columns: {price: integer}`, `columns: {nope: text}`,
	} {
		c, _ := setup(t, strings.Replace(cfg, "columns: {flag: boolean}", bad, 1), map[string]string{"p.sql": dump})
		s, _ := serve(t, c, r)
		if code, _, _ := get(s, "GET", "/p/a"); code != 503 {
			t.Errorf("%s: served", bad)
		}
	}
}

// Nothing a request sends reaches sqlite3: a wrapper records every input it
// is given, and none holds a request's values.
func TestRequestsNeverReachSQL(t *testing.T) {
	real := needSQLite(t)
	dir := t.TempDir()
	logf := filepath.Join(dir, "sqlite-input.log")
	wrapper := filepath.Join(dir, "sqlite3")
	os.WriteFile(wrapper, []byte("#!/bin/sh\nif [ \"$1\" = -version ]; then exec "+real.Bin+" -version; fi\ntee -a "+logf+" | "+real.Bin+" \"$@\"\n"), 0o755)
	if _, err := exec.Command(wrapper, "-version").Output(); err != nil {
		t.Skip("can't run a shell wrapper here")
	}
	cfg := "version: 1\nsources:\n  - id: p\n    sqlite: {db: p.sql, table: p}\n    key: sku\nendpoints:\n  - path: /p/{sku}\n    source: p\n  - path: /p\n    source: p\n    filters: [name]\n"
	c, _ := setup(t, cfg, map[string]string{"p.sql": "CREATE TABLE p (sku TEXT, name TEXT); INSERT INTO p VALUES ('a', 'b');"})
	r := &sqlitecli.Runner{Bin: wrapper, Timeout: 10 * time.Second, MaxOut: 1 << 20}
	s, _ := serve(t, c, r)
	before, _ := os.ReadFile(logf)
	for _, p := range []string{"/p/a'%3B%20DROP%20TABLE%20p%3B--", "/p?name=x'%20OR%201=1--", "/p/.trace%20x", "/p?name=MARKER42"} {
		get(s, "GET", p)
	}
	s.Check(context.Background())
	after, _ := os.ReadFile(logf)
	if !bytes.Equal(before, after) {
		t.Fatalf("requests made sqlite3 run:\n%s", after[len(before):])
	}
	if bytes.Contains(after, []byte("DROP")) || bytes.Contains(after, []byte("MARKER42")) {
		t.Fatal("a request value reached sqlite3")
	}
}

// Sources are never written, and nothing new appears next to them.
func TestSourcesAreNeverWritten(t *testing.T) {
	r := needSQLite(t)
	dir := t.TempDir()
	db := filepath.Join(dir, "p.db")
	if out, err := exec.Command(r.Bin, db, "CREATE TABLE p (sku TEXT, n INTEGER); INSERT INTO p VALUES ('a', 1);").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	os.WriteFile(filepath.Join(dir, "o.csv"), []byte("id\n1\n"), 0o644)
	cfg := "version: 1\nsources:\n  - id: p\n    sqlite: {db: p.db, table: p}\n  - id: o\n    csv: {file: o.csv}\nendpoints:\n  - path: /p\n    source: p\n  - path: /o\n    source: o\n"
	os.WriteFile(filepath.Join(dir, "bridge.yaml"), []byte(cfg), 0o644)
	sums := map[string]string{}
	for _, f := range []string{"p.db", "o.csv", "bridge.yaml"} {
		b, _ := os.ReadFile(filepath.Join(dir, f))
		sums[f] = string(b)
	}
	c, err := LoadFile(filepath.Join(dir, "bridge.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	s, _ := serve(t, c, r)
	for i := 0; i < 20; i++ {
		get(s, "GET", "/p")
		get(s, "GET", "/o?limit=1")
		s.Check(context.Background())
	}
	for f, b := range sums {
		if got, _ := os.ReadFile(filepath.Join(dir, f)); string(got) != b {
			t.Fatalf("%s changed", f)
		}
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 3 {
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Fatalf("files: %v", names)
	}
}

// The main promise under load: a writer rewrites the export slowly, in
// pieces, while the server checks and answers. Every answer is one of the
// complete versions, never a mix or a half.
func TestHalfWrittenFilesAreNeverServed(t *testing.T) {
	cfg := "version: 1\nlimits: {check_every: 10ms}\nsources:\n  - id: o\n    csv: {file: o.csv}\n    columns: {v: integer, n: integer}\nendpoints:\n  - path: /o\n    source: o\n"
	version := func(v int) string {
		var b strings.Builder
		b.WriteString("v,n\n")
		for i := 0; i < 50+v%7; i++ {
			fmt.Fprintf(&b, "%d,%d\n", v, i)
		}
		return b.String()
	}
	c, dir := setup(t, cfg, map[string]string{"o.csv": version(0)})
	c.Settle = 150 * time.Millisecond // longer than any pause in a write below
	s, _ := serve(t, c, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Watch(ctx)
	path := filepath.Join(dir, "o.csv")
	done := make(chan struct{})
	go func() {
		defer close(done)
		rs := rand.New(rand.NewSource(time.Now().UnixNano()))
		for v := 1; v <= 15; v++ {
			f, _ := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o644)
			data := []byte(version(v))
			for len(data) > 0 {
				n := 1 + rs.Intn(len(data))
				if rs.Intn(3) == 0 && n > 3 {
					n = n / 2 // stop mid-line now and then
				}
				f.Write(data[:n])
				data = data[n:]
				time.Sleep(time.Duration(rs.Intn(25)) * time.Millisecond)
			}
			f.Close()
			time.Sleep(time.Duration(rs.Intn(400)) * time.Millisecond)
		}
	}()
	seen := map[int]bool{}
	for {
		select {
		case <-done:
			time.Sleep(300 * time.Millisecond)
			_, _, body := get(s, "GET", "/o?limit=1000")
			check(t, body, version, seen)
			if len(seen) < 2 {
				t.Fatalf("only saw versions %v", seen)
			}
			t.Logf("served %d versions", len(seen))
			return
		default:
		}
		code, _, body := get(s, "GET", "/o?limit=1000")
		if code != 200 {
			t.Fatalf("%d %s", code, body)
		}
		check(t, body, version, seen)
	}
}

func check(t *testing.T, body string, version func(int) string, seen map[int]bool) {
	t.Helper()
	var got struct {
		Items []struct{ V, N int }
		Total int
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil || len(got.Items) == 0 {
		t.Fatalf("%v %s", err, body)
	}
	v := got.Items[0].V
	want := strings.Count(version(v), "\n") - 1
	if got.Total != want || len(got.Items) != want {
		t.Fatalf("version %d served with %d of %d rows", v, len(got.Items), want)
	}
	for i, it := range got.Items {
		if it.V != v || it.N != i {
			t.Fatalf("version %d mixed with another at row %d", v, i)
		}
	}
	seen[v] = true
}

// Random CSV files with random values of every type: what is served reads
// back as exactly the values written, types included.
func TestRandomValuesReadBackExactly(t *testing.T) {
	seed := time.Now().UnixNano()
	t.Logf("seed %d", seed)
	rs := rand.New(rand.NewSource(seed))
	for round := 0; round < 30; round++ {
		types := []Type{Text, Integer, Number, Boolean, Date}
		var header []string
		var decl []string
		var colT []Type
		for i := 0; i < 1+rs.Intn(6); i++ {
			ty := types[rs.Intn(len(types))]
			name := fmt.Sprintf("c%d", i)
			header = append(header, name)
			decl = append(decl, name+": "+string(ty))
			colT = append(colT, ty)
		}
		var csvb strings.Builder
		csvb.WriteString(strings.Join(header, ",") + "\n")
		var want []map[string]any
		for r := 0; r < rs.Intn(20); r++ {
			row := map[string]any{}
			var cells []string
			for i, ty := range colT {
				var cell string
				var v any
				switch ty {
				case Text:
					s := randText(rs)
					cell, v = `"`+strings.ReplaceAll(s, `"`, `""`)+`"`, s
				case Integer:
					n := rs.Int63() - rs.Int63()
					cell, v = strconv.FormatInt(n, 10), json.Number(strconv.FormatInt(n, 10))
				case Number:
					f := (rs.Float64() - 0.5) * float64(rs.Int63n(1e12))
					cell = strconv.FormatFloat(f, 'g', -1, 64)
					v = f
				case Boolean:
					b := rs.Intn(2) == 0
					cell, v = strconv.FormatBool(b), b
				case Date:
					d := time.Date(1900+rs.Intn(200), time.Month(1+rs.Intn(12)), 1+rs.Intn(28), 0, 0, 0, 0, time.UTC).Format("2006-01-02")
					cell, v = d, d
				}
				if ty != Text && len(colT) > 1 && rs.Intn(10) == 0 {
					cell, v = "", nil
				}
				cells = append(cells, cell)
				row[header[i]] = v
			}
			csvb.WriteString(strings.Join(cells, ",") + "\n")
			want = append(want, row)
		}
		cfg := "version: 1\nsources:\n  - id: r\n    csv: {file: r.csv}\n    columns: {" + strings.Join(decl, ", ") + "}\nendpoints:\n  - path: /r\n    source: r\n"
		c, _ := setup(t, cfg, map[string]string{"r.csv": csvb.String()})
		s, log := serve(t, c, nil)
		code, _, body := get(s, "GET", "/r?limit=1000")
		if code != 200 {
			t.Fatalf("round %d: %d %s\n%s\n%s", round, code, body, log.String(), csvb.String())
		}
		dec := json.NewDecoder(strings.NewReader(body))
		dec.UseNumber()
		var got struct{ Items []map[string]any }
		if err := dec.Decode(&got); err != nil || len(got.Items) != len(want) {
			t.Fatalf("round %d: %v %s", round, err, body)
		}
		for i := range want {
			for k, w := range want[i] {
				g := got.Items[i][k]
				switch wv := w.(type) {
				case float64:
					n, ok := g.(json.Number)
					f, err := strconv.ParseFloat(string(n), 64)
					if !ok || err != nil || f != wv || !strings.ContainsAny(string(n), ".eE") {
						t.Fatalf("round %d row %d %s: got %v, want %v", round, i, k, g, wv)
					}
				default:
					if fmt.Sprint(g) != fmt.Sprint(w) || fmt.Sprintf("%T", g) != fmt.Sprintf("%T", w) {
						t.Fatalf("round %d row %d %s: got %#v, want %#v", round, i, k, g, w)
					}
				}
			}
		}
	}
}

func randText(rs *rand.Rand) string {
	alphabet := []string{"a", "Z", " ", ",", "\"", "\n", "é", "日", "'", ";", "\\", "{", "0"}
	var b strings.Builder
	for i := 0; i < rs.Intn(12); i++ {
		b.WriteString(alphabet[rs.Intn(len(alphabet))])
	}
	return b.String()
}

// A source that was never read answers 503 with Retry-After; the others
// keep working.
func TestUnreadSourceGives503(t *testing.T) {
	cfg := "version: 1\nsources:\n  - id: a\n    csv: {file: a.csv}\n  - id: b\n    csv: {file: missing.csv}\nendpoints:\n  - path: /a\n    source: a\n  - path: /b\n    source: b\n"
	c, dir := setup(t, cfg, map[string]string{"a.csv": "x\n1\n"})
	s, _ := serve(t, c, nil)
	if code, h, _ := get(s, "GET", "/b"); code != 503 || h.Get("Retry-After") == "" {
		t.Fatalf("%d %v", code, h)
	}
	if code, _, _ := get(s, "GET", "/a"); code != 200 {
		t.Fatal(code)
	}
	os.WriteFile(filepath.Join(dir, "missing.csv"), []byte("y\n2\n"), 0o644)
	s.Check(context.Background())
	s.Check(context.Background())
	if code, _, body := get(s, "GET", "/b"); code != 200 || !strings.Contains(body, `"y":"2"`) {
		t.Fatalf("%d %s", code, body)
	}
}

// encoding/csv skips blank lines, which in a one-column file would lose
// rows with an empty value; they are refused instead.
func TestBlankLinesAreRefused(t *testing.T) {
	cfg := "version: 1\nsources:\n  - id: o\n    csv: {file: o.csv}\n    columns: {n: integer}\nendpoints:\n  - path: /o\n    source: o\n"
	for _, body := range []string{"n\n1\n\n2\n", "\nn\n1\n", "n\n1\n\n", "n,t\n1,a\n\n2,b\n"} {
		c, _ := setup(t, cfg, map[string]string{"o.csv": body})
		s, log := serve(t, c, nil)
		if code, _, _ := get(s, "GET", "/o"); code != 503 || !strings.Contains(log.String(), "blank") {
			t.Errorf("%q: %d %s", body, code, log.String())
		}
	}
	// Line breaks inside quoted values are not blank lines.
	c, _ := setup(t, "version: 1\nsources:\n  - id: o\n    csv: {file: o.csv}\nendpoints:\n  - path: /o\n    source: o\n", map[string]string{"o.csv": "t,u\n\"a\r\n\nb\",x\n\"\",y\n"})
	s, _ := serve(t, c, nil)
	if _, _, body := get(s, "GET", "/o"); !strings.Contains(body, `"total":2`) {
		t.Fatalf("%s", body)
	}
}
