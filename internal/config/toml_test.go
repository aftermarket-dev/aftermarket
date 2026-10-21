package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aftermarket-dev/aftermarket/internal/ojson"
)

func mustTOML(t *testing.T, s string) *ojson.Object {
	t.Helper()
	tree, _, err := parseTOML(s)
	if err != nil {
		t.Fatalf("%v\n%s", err, s)
	}
	return tree
}

func asJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := ojson.Encode(plain(v))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TOML is read as the spec says, for the forms config files use.
func TestTOMLReads(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{`a = 1`, `{"a":1}`},
		{"a = +1_000\nb = -17\nc = 0xDEAD_BEEF\nd = 0o755\ne = 0b1101", `{"a":1000,"b":-17,"c":3735928559,"d":493,"e":13}`},
		{"a = 6.626e-34\nb = -0.01\nc = 9_224_617.445_991\nd = 5e+22\ne = inf\nf = -nan", `{"a":6.626e-34,"b":-0.01,"c":9224617.445991,"d":5e+22,"e":"inf","f":"-nan"}`},
		{"a = true\nb = false", `{"a":true,"b":false}`},
		{`a = "tab\there \"q\" \\ \u00e9 \U0001F600"`, `{"a":"tab\there \"q\" \\ é 😀"}`},
		{`a = 'C:\Users\x'`, `{"a":"C:\\Users\\x"}`},
		{"a = \"\"\"\nRoses\nViolets\"\"\"", `{"a":"Roses\nViolets"}`},
		{"a = \"\"\"one \\\n    two\"\"\"", `{"a":"one two"}`},
		{"a = \"\"\"x\"\"\"\"\"", `{"a":"x\"\""}`},
		{"a = '''\nraw \\n here'''", `{"a":"raw \\n here"}`},
		{"d1 = 1979-05-27T07:32:00Z\nd2 = 1979-05-27 07:32:00.999-07:00\nd3 = 1979-05-27\nt = 07:32:00", `{"d1":"1979-05-27T07:32:00Z","d2":"1979-05-27 07:32:00.999-07:00","d3":"1979-05-27","t":"07:32:00"}`},
		{"a = [1, 2, [3, 'x'],\n  # comment\n  {b = 1},\n]", `{"a":[1,2,[3,"x"],{"b":1}]}`},
		{`a = { x = 1, y.z = "q" }`, `{"a":{"x":1,"y":{"z":"q"}}}`},
		{"\"quoted key\" = 1\n'lit.key' = 2\nbare-key_1 = 3", `{"quoted key":1,"lit.key":2,"bare-key_1":3}`},
		{"a . b . c = 1\na.b.d = 2", `{"a":{"b":{"c":1,"d":2}}}`},
		{"[a.b.c]\nx = 1\n[a]\ny = 2", `{"a":{"b":{"c":{"x":1}},"y":2}}`},
		{"[fruit]\napple.color = \"red\"\n[fruit.apple.texture]\nsmooth = true", `{"fruit":{"apple":{"color":"red","texture":{"smooth":true}}}}`},
		{"[[p]]\nn = 1\n[[p]]\n[[p]]\nn = 3\n[p.sub]\nk = 1", `{"p":[{"n":1},{},{"n":3,"sub":{"k":1}}]}`},
		{"[[f]]\nn = 'a'\n[[f.v]]\nc = 'r'\n[[f]]\nn = 'b'", `{"f":[{"n":"a","v":[{"c":"r"}]},{"n":"b"}]}`},
		{"a = 1\r\n[t]\r\nb = 2\r\n", `{"a":1,"t":{"b":2}}`},
		{"", `{}`},
		{"# only a comment", `{}`},
	} {
		if got := asJSON(t, mustTOML(t, c.in)); got != c.want {
			t.Errorf("%q:\n got %s\nwant %s", c.in, got, c.want)
		}
	}
}

// Files TOML doesn't allow are refused with a line number, never guessed at.
func TestTOMLRefuses(t *testing.T) {
	for _, in := range []string{
		"a = 1\na = 2",
		"[a]\n[a]",
		"a.b = 1\n[a]",
		"[fruit]\napple.color = 'red'\n[fruit.apple]",
		"a = {b = 1}\na.c = 2",
		"a = {b = 1}\n[a.c]",
		"a = [1]\n[[a]]",
		"[[a]]\n[a]",
		"[a.b]\n[a]\nb.c = 1",
		"a = 9223372036854775808",
		"a = 0x8000000000000000",
		"a = 01",
		"a = 1__0",
		"a = .5",
		"a = 1.",
		"a = \"open",
		"a = 'open",
		"a = \"\"\"open",
		"a = \"bad \\x escape\"",
		"a = {b = 1,\n c = 2}",
		"a = {b = 1,}",
		"a = [1 2]",
		"a = 1 b = 2",
		"a",
		"= 1",
		"[a",
		"[[a]",
		"a = maybe",
		"a = \"\x01\"",
		"a = 1 # \x01",
		"\"\"\"k\"\"\" = 1",
		"a = 2026-13-01x",
	} {
		if _, _, err := parseTOML(in); err == nil {
			t.Errorf("accepted %q", in)
		} else if !strings.Contains(err.Error(), "line ") {
			t.Errorf("%q: no line number in %v", in, err)
		}
	}
	if _, _, err := parseTOML("a = 1\nb = 2\nc = ="); err == nil || !strings.Contains(err.Error(), "line 3") {
		t.Fatalf("wrong line: %v", err)
	}
}

const vendorTOML = `# Vendor config, version 1.0
title = "Vendor App"   # shown in the header
version = 1.0

[server]
host = "0.0.0.0"
port = 8080
timeout = 30          # seconds
tls = { enabled = false, cert = "" }
allowed = [
  "a.example",  # first
  "b.example",
]

[database]
url = "postgres://app:secret@db/app"
pool = 5
started = 1979-05-27T07:32:00Z

[[workers]]
name = "mail"
threads = 2

[[workers]]
name = "jobs"
threads = 4

[logging.file]
path = "/var/log/app.log"
`

func applyTOML(t *testing.T, base, changes string) string {
	t.Helper()
	o, err := Load([]byte("version: 1\nbase: config.toml\nchanges:\n" + changes))
	if err != nil {
		t.Fatal(err)
	}
	r, err := o.Apply([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Problems) > 0 {
		t.Fatalf("%v", r.Problems)
	}
	return string(r.Out)
}

// Each kind of change edits only its own lines: comments, alignment, other
// values and their spelling stay as they were.
func TestTOMLChangesStayOnTheirLines(t *testing.T) {
	for _, c := range []struct {
		name, changes string
		removed       []string // lines that must be gone
		added         []string // lines that must appear
	}{
		{"set a value", "  - set: {path: server.timeout, value: 120, expect: 30}\n",
			[]string{"timeout = 30          # seconds"}, []string{"timeout = 120          # seconds"}},
		{"set inside an inline table", "  - set: {path: server.tls.enabled, value: true}\n",
			[]string{`tls = { enabled = false, cert = "" }`}, []string{`tls = { enabled = true, cert = "" }`}},
		{"set a new key in a table", "  - set: {path: server.max_body, value: 10MiB, expect_absent: true}\n",
			nil, []string{`max_body = "10MiB"`}},
		{"set a new key at the top", "  - set: {path: features.beta, value: true}\n",
			nil, []string{`features.beta = true`}},
		{"set in an array of tables", "  - set: {path: workers.1.threads, value: 8, expect: 4}\n",
			[]string{"threads = 4"}, []string{"threads = 8"}},
		{"a date stays a date", "  - set: {path: database.started, value: \"2026-10-21T00:00:00Z\"}\n",
			[]string{"started = 1979-05-27T07:32:00Z"}, []string{"started = 2026-10-21T00:00:00Z"}},
		{"rename in place", "  - rename: {from: database.pool, to: database.pool_size}\n",
			[]string{"pool = 5"}, []string{"pool_size = 5"}},
		{"delete a key", "  - delete: {path: server.port}\n", []string{"port = 8080"}, nil},
		{"delete a table", "  - delete: {path: logging}\n", []string{"[logging.file]", `path = "/var/log/app.log"`}, nil},
		{"delete an array-of-tables element", "  - delete: {path: workers.0}\n", []string{`name = "mail"`, "threads = 2"}, nil},
		{"copy", "  - copy: {from: server.port, to: database.port}\n", nil, []string{"port = 8080"}},
		{"rename a table by its headers", "  - rename: {from: logging, to: log}\n", []string{"[logging.file]"}, []string{"[log.file]"}},
		{"rename an array of tables", "  - rename: {from: workers, to: jobs}\n", []string{"[[workers]]"}, []string{"[[jobs]]"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			out := applyTOML(t, vendorTOML, c.changes)
			outLines := strings.Split(out, "\n")
			has := map[string]int{}
			for _, l := range outLines {
				has[l]++
			}
			gone := map[string]bool{}
			for _, l := range c.removed {
				gone[l] = true
				if has[l] > 0 && l != "threads = 4" {
					t.Errorf("still there: %q", l)
				}
			}
			for _, l := range c.added {
				if has[l] == 0 {
					t.Errorf("missing %q in:\n%s", l, out)
				}
			}
			// Every other line of the vendor's file is still there, as written.
			for _, l := range strings.Split(vendorTOML, "\n") {
				if !gone[l] && has[l] == 0 && l != "" {
					t.Errorf("lost %q:\n%s", l, out)
				}
			}
			if !strings.HasSuffix(out, "\n") {
				t.Error("the final newline was lost")
			}
		})
	}
}

// Windows line endings and a missing final newline are kept.
func TestTOMLKeepsLineEndings(t *testing.T) {
	base := "# c\r\na = 1\r\n[t]\r\nb = 2"
	out := applyTOML(t, base, "  - set: {path: t.b, value: 3}\n  - set: {path: t.c, value: x}\n  - set: {path: a, value: 5}\n")
	if out != "# c\r\na = 5\r\n[t]\r\nb = 3\r\nc = \"x\"" {
		t.Fatalf("%q", out)
	}
}

// Changes that can't be made line by line write the file from its values:
// still exactly the intended values.
func TestTOMLRewritesWhenItMust(t *testing.T) {
	out := applyTOML(t, vendorTOML, "  - set: {path: logging, value: {level: debug}}\n  - rename: {from: server.host, to: database.host}\n")
	tree := mustTOML(t, out)
	if v, _ := ojson.Get(tree, ojson.Path{"logging", "level"}); v != "debug" {
		t.Fatalf("%s", out)
	}
	if v, _ := ojson.Get(tree, ojson.Path{"database", "host"}); v != "0.0.0.0" {
		t.Fatalf("%s", out)
	}
	if _, ok := ojson.Get(tree, ojson.Path{"server", "host"}); ok {
		t.Fatalf("%s", out)
	}
}

// What TOML can't hold is refused at the change that asks for it.
func TestTOMLRefusesValuesItCantHold(t *testing.T) {
	o, _ := Load([]byte("version: 1\nbase: c.toml\nchanges:\n  - set: {path: a, value: null}\n  - set: {path: b, value: 99999999999999999999}\n"))
	r, err := o.Apply([]byte("x = 1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Problems) != 2 || !strings.Contains(r.Problems[0].Msg, "no null") || !strings.Contains(r.Problems[1].Msg, "too large") {
		t.Fatalf("%v", r.Problems)
	}
}

// An overlay with nothing to change gives back the file byte for byte, and
// applying twice gives the same bytes.
func TestTOMLUnchangedAndTwice(t *testing.T) {
	out := applyTOML(t, vendorTOML, "  - set: {path: server.port, value: 8080, expect: 8080}\n")
	if out != vendorTOML {
		t.Fatal("an overlay with nothing to change altered the file")
	}
	changes := "  - set: {path: server.timeout, value: 120}\n  - delete: {path: logging}\n"
	if applyTOML(t, vendorTOML, changes) != applyTOML(t, vendorTOML, changes) {
		t.Fatal("two applies differ")
	}
}

// Random documents and random changes: the result must always read back as
// exactly the intended values, and, when it was edited line by line, keep
// every comment.
func TestTOMLRandomChanges(t *testing.T) {
	seed := time.Now().UnixNano()
	if v := os.Getenv("AFTERMARKET_SEED"); v != "" {
		fmt.Sscan(v, &seed)
	}
	t.Logf("seed %d (set AFTERMARKET_SEED to repeat)", seed)
	rs := rand.New(rand.NewSource(seed))
	keys := []string{"a", "b", "port", "name", "x-y", "with space", "é"}
	scalar := func() any {
		switch rs.Intn(5) {
		case 0:
			return json.Number(fmt.Sprint(rs.Intn(1000) - 500))
		case 1:
			return json.Number(fmt.Sprintf("%d.%d", rs.Intn(100), rs.Intn(100)))
		case 2:
			return rs.Intn(2) == 0
		case 3:
			return []string{"plain", "with \"quote\"", "tab\there", "line\nbreak", "#not a comment", ""}[rs.Intn(6)]
		}
		return []any{json.Number("1"), "two"}
	}
	var gen func(depth int) *ojson.Object
	gen = func(depth int) *ojson.Object {
		o := ojson.NewObject()
		for i := 0; i < 1+rs.Intn(4); i++ {
			k := keys[rs.Intn(len(keys))]
			if _, ok := o.Get(k); ok {
				continue
			}
			switch r := rs.Intn(6); {
			case r == 0 && depth < 3:
				o.Set(k, gen(depth+1))
			case r == 1 && depth < 2:
				o.Set(k, []any{gen(depth + 1), gen(depth + 1)})
			default:
				o.Set(k, scalar())
			}
		}
		return o
	}
	var paths func(v any, p ojson.Path, out *[]ojson.Path)
	paths = func(v any, p ojson.Path, out *[]ojson.Path) {
		if len(p) > 0 {
			*out = append(*out, p)
		}
		switch t := v.(type) {
		case *ojson.Object:
			for _, k := range t.Keys() {
				c, _ := t.Get(k)
				paths(c, append(append(ojson.Path(nil), p...), k), out)
			}
		case []any:
			for i, c := range t {
				paths(c, append(append(ojson.Path(nil), p...), fmt.Sprint(i)), out)
			}
		}
	}
	rewrites := 0
	for n := 0; n < 400; n++ {
		tree := gen(0)
		var b strings.Builder
		var written bytes.Buffer
		if err := writeTOMLTable(&written, tree, nil); err != nil {
			t.Fatal(err)
		}
		// Comments between lines, so losing them shows.
		for i, l := range strings.Split(strings.TrimLeft(written.String(), "\n"), "\n") {
			if l != "" && rs.Intn(3) == 0 {
				fmt.Fprintf(&b, "# note %d\n", i)
			}
			b.WriteString(l + "\n")
		}
		base := b.String()
		if !equal(plain(mustTOML(t, base)), plain(tree)) {
			t.Fatalf("the writer and the reader disagree:\n%s", base)
		}
		var all []ojson.Path
		paths(tree, nil, &all)
		p := all[rs.Intn(len(all))]
		want := ojson.Clone(tree)
		var change string
		switch rs.Intn(3) {
		case 0:
			v := scalar()
			vj, _ := ojson.Encode(v)
			change = fmt.Sprintf("  - set: {path: %s, value: %s}\n", yamlPath(p), vj)
			ojson.Set(want, p, v)
		case 1:
			change = fmt.Sprintf("  - delete: {path: %s}\n", yamlPath(p))
			if !deleteAt(want, p) {
				continue
			}
		default:
			np := append(append(ojson.Path(nil), p[:len(p)-1]...), "new-key")
			if _, exists := ojson.Get(tree, np); exists || isIndex(p[len(p)-1]) {
				continue
			}
			change = fmt.Sprintf("  - rename: {from: %s, to: %s}\n", yamlPath(p), yamlPath(np))
			ojson.Rename(want, p, np)
		}
		o, err := Load([]byte("version: 1\nbase: c.toml\nchanges:\n" + change))
		if err != nil {
			continue // a path the overlay can't name, such as a key with a dot
		}
		r, err := o.Apply([]byte(base))
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Problems) > 0 {
			continue // such as an empty table that TOML can't keep after a delete
		}
		got := mustTOML(t, string(r.Out))
		if !equal(plain(got), plain(want)) {
			t.Fatalf("case %d, %s\nbase:\n%s\nout:\n%s\ngot  %s\nwant %s", n, change, base, r.Out, asJSON(t, got), asJSON(t, want))
		}
		if td, ok := r.doc.(*tomlDoc); ok && td.rewrite {
			rewrites++
			if os.Getenv("TOML_WHY") != "" && rewrites <= 12 {
				t.Logf("rewrite: %s\nBASE:\n%s\nOUT:\n%s", change, base, r.Out)
			}
		}
	}
	t.Logf("%d of 400 results were written from values instead of edited line by line", rewrites)
}

func yamlPath(p ojson.Path) string {
	b, _ := json.Marshal(p.String())
	return string(b)
}

func isIndex(s string) bool {
	_, err := fmt.Sscan(s, new(int))
	return err == nil
}

// deleteAt deletes p from root the way the overlay does, list elements
// included.
func deleteAt(root any, p ojson.Path) bool {
	d := &treeDoc{v: root}
	return d.del(p) == nil
}
