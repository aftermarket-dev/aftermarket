package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aftermarket-dev/aftermarket/internal/ojson"
	"github.com/aftermarket-dev/aftermarket/internal/rules"
	"github.com/aftermarket-dev/aftermarket/internal/yamlite"
)

func mustLoad(t *testing.T, src string) *Overlay {
	t.Helper()
	o, err := Load([]byte(src))
	if err != nil {
		t.Fatalf("overlay: %v", err)
	}
	return o
}

func apply(t *testing.T, o *Overlay, base string) *Result {
	t.Helper()
	r, err := o.Apply([]byte(base))
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	return r
}

const vendorJSON = `{
  "server": {
    "port": 8080,
    "timeout": 30,
    "ratio": 1.50,
    "big": 12345678901234567890,
    "tags": ["a", "b"]
  },
  "name": "caf\u00e9 \"x\"",
  "db": {"password": "hunter2", "pool": 10}
}
`

// TestFieldsTheOverlayDoesntNameKeepOrderAndValue: an overlay whose changes
// are all already in place gives back the vendor's file byte for byte, and a
// real change leaves every other byte alone.
func TestFieldsTheOverlayDoesntNameKeepOrderAndValue(t *testing.T) {
	base, err := os.ReadFile("../../examples/vendor-app/1.0/config.json")
	if err != nil {
		t.Fatal(err)
	}
	same := mustLoad(t, "base: x.json\nchanges:\n  - set: {path: server.timeout, value: 30, expect: 30}\n")
	r := apply(t, same, string(base))
	if !bytes.Equal(r.Out, base) {
		t.Fatalf("an overlay with nothing to change rewrote the file:\n%s", r.Out)
	}
	if len(r.InPlace) != 1 || len(r.Applied) != 0 {
		t.Fatalf("applied %v, in place %v", r.Applied, r.InPlace)
	}
	noNL := bytes.TrimSuffix(base, []byte("\n"))
	if r := apply(t, same, string(noNL)); !bytes.Equal(r.Out, noNL) {
		t.Fatal("a file without a final newline got one")
	}
	one := mustLoad(t, "base: x.json\nchanges:\n  - set: {path: server.timeout, value: 120}\n")
	r = apply(t, one, string(base))
	want := strings.Replace(string(base), `"timeout": 30,`, `"timeout": 120,`, 1)
	if string(r.Out) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", r.Out, want)
	}
}

// randomTree builds a random JSON document with awkward values.
func randomTree(rs *rand.Rand, depth int) any {
	if depth > 3 || rs.Intn(4) == 0 {
		vals := []any{json.Number("0"), json.Number("-12"), json.Number("1.50"), json.Number("1e3"), json.Number("12345678901234567890"),
			"plain", "two words", "", "true", "null", "007", "a: b", "x #y", "é ü 世", "quote \" and \\ back", "line\nbreak", "tab\there",
			true, false, nil}
		return vals[rs.Intn(len(vals))]
	}
	if rs.Intn(3) == 0 {
		n := rs.Intn(4)
		out := make([]any, 0, n)
		for i := 0; i < n; i++ {
			out = append(out, randomTree(rs, depth+1))
		}
		return out
	}
	o := ojson.NewObject()
	keys := []string{"a", "b_c", "D-e", "f.g", "1", "true", "with space", "x/y", "ünï"}
	for i := 0; i < rs.Intn(5); i++ {
		o.Set(keys[rs.Intn(len(keys))]+fmt.Sprint(i), randomTree(rs, depth+1))
	}
	return o
}

// leaves lists every path whose key has no dot, so a dot path can reach it.
func leaves(v any, p ojson.Path, out *[]ojson.Path) {
	switch t := v.(type) {
	case *ojson.Object:
		for _, k := range t.Keys() {
			if strings.Contains(k, ".") {
				continue
			}
			c, _ := t.Get(k)
			leaves(c, append(append(ojson.Path(nil), p...), k), out)
		}
	case []any:
		for i, it := range t {
			leaves(it, append(append(ojson.Path(nil), p...), fmt.Sprint(i)), out)
		}
	default:
		*out = append(*out, p)
	}
}

func seed(t *testing.T) int64 {
	s := time.Now().UnixNano()
	if v := os.Getenv("AFTERMARKET_SEED"); v != "" {
		fmt.Sscan(v, &s)
	}
	t.Logf("seed %d (set AFTERMARKET_SEED to repeat)", s)
	return s
}

// TestRandomDocumentsKeepEverythingElse sets one random leaf in random JSON
// and YAML documents. The output must read back, the changed leaf must hold
// the new value, and every other leaf must hold its old value in its old
// place.
func TestRandomDocumentsKeepEverythingElse(t *testing.T) {
	rs := rand.New(rand.NewSource(seed(t)))
	for i := 0; i < 400; i++ {
		root := ojson.NewObject()
		for j := 0; j < 1+rs.Intn(4); j++ {
			root.Set(fmt.Sprintf("k%d", j), randomTree(rs, 0))
		}
		var ls []ojson.Path
		leaves(root, nil, &ls)
		if len(ls) == 0 {
			continue
		}
		target := ls[rs.Intn(len(ls))]
		for _, format := range []string{JSON, YAML} {
			var src bytes.Buffer
			if format == JSON {
				if err := writeJSON(&src, root, []string{"", "  ", "\t", "    "}[rs.Intn(4)], ""); err != nil {
					t.Fatal(err)
				}
			} else if err := writeYAML(&src, root, nil); err != nil {
				t.Fatal(err)
			}
			o := &Overlay{Format: format, Changes: []Change{{Line: 1, Op: "set", Path: target, Value: "NEW value: #1"}}}
			r, err := o.Apply(src.Bytes())
			if err != nil {
				t.Fatalf("%s case %d: base doesn't read back: %v\n%s", format, i, err, src.String())
			}
			if len(r.Problems) > 0 {
				t.Fatalf("%s case %d: %v", format, i, r.Problems)
			}
			back, err := parse(format, r.Out)
			if err != nil {
				t.Fatalf("%s case %d: output doesn't read back: %v\n%s", format, i, err, r.Out)
			}
			for _, l := range ls {
				got, ok := ojson.Get(back.root(), l)
				want, _ := ojson.Get(root, l)
				if l.String() == target.String() {
					want = "NEW value: #1"
				}
				if !ok || !equal(got, want) {
					t.Fatalf("%s case %d: %s is %v, want %v (target %s)\n%s", format, i, l, got, want, target, r.Out)
				}
			}
			if format == JSON {
				if o := keyOrder(back.root()); o != keyOrder(root) {
					t.Fatalf("JSON case %d: key order changed:\n%s\n%s", i, o, keyOrder(root))
				}
			}
		}
	}
}

func keyOrder(v any) string {
	var b strings.Builder
	switch t := v.(type) {
	case *ojson.Object:
		for _, k := range t.Keys() {
			c, _ := t.Get(k)
			b.WriteString(k + "{" + keyOrder(c) + "}")
		}
	case []any:
		for _, it := range t {
			b.WriteString("[" + keyOrder(it) + "]")
		}
	}
	return b.String()
}

// TestYAMLWriterReadsBack: whatever the YAML writer produces, yamlite reads
// back to the same values.
func TestYAMLWriterReadsBack(t *testing.T) {
	rs := rand.New(rand.NewSource(seed(t)))
	for i := 0; i < 1000; i++ {
		v := randomTree(rs, 0)
		// An empty file-level mapping or list is written as {} or [], which
		// other YAML readers accept but yamlite's subset doesn't.
		switch x := v.(type) {
		case *ojson.Object:
			if len(x.Keys()) == 0 {
				continue
			}
		case []any:
			if len(x) == 0 {
				continue
			}
		default:
			continue
		}
		var b bytes.Buffer
		if err := writeYAML(&b, v, nil); err != nil {
			t.Fatal(err)
		}
		n, err := yamlite.Parse(b.Bytes())
		if err != nil {
			t.Fatalf("case %d: %v\n%s", i, err, b.String())
		}
		if got := rules.NodeToJSON(n); !equal(got, v) {
			ge, _ := ojson.Encode(got)
			we, _ := ojson.Encode(v)
			t.Fatalf("case %d:\n%s\nread back as %s, want %s", i, b.String(), ge, we)
		}
	}
}

const vendorEnv = `# Vendor defaults. Don't edit, upgrades overwrite this file.
APP_PORT=8080
export LOG_LEVEL=info   # info, debug or warn
GREETING="hello world"
RAW='single $quoted'

DB_PASSWORD=vendor-default
OLD_NAME=keep-me
`

// TestEnvFilesKeepEveryLineTheOverlayDoesntChange.
func TestEnvFilesKeepEveryLineTheOverlayDoesntChange(t *testing.T) {
	o := mustLoad(t, `base: app.env
changes:
  - set: {path: LOG_LEVEL, value: debug, expect: info}
  - set: {path: APP_PORT, value: 8080, expect: 8080}
  - set: {path: DB_PASSWORD, value: "s3cret with space", expect: vendor-default}
  - rename: {from: OLD_NAME, to: NEW_NAME}
  - copy: {from: GREETING, to: GREETING_COPY}
  - delete: {path: RAW}
  - set: {path: FEATURE_X, value: true, expect_absent: true}
`)
	r := apply(t, o, vendorEnv)
	if len(r.Problems) > 0 {
		t.Fatal(r.Problems)
	}
	want := `# Vendor defaults. Don't edit, upgrades overwrite this file.
APP_PORT=8080
export LOG_LEVEL=debug   # info, debug or warn
GREETING="hello world"

DB_PASSWORD='s3cret with space'
NEW_NAME=keep-me
GREETING_COPY='hello world'
FEATURE_X=true
`
	if string(r.Out) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", r.Out, want)
	}
	back, err := parse(Env, r.Out)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := ojson.Get(back.root(), ojson.Path{"DB_PASSWORD"}); v != "s3cret with space" {
		t.Fatalf("DB_PASSWORD reads back as %q", v)
	}
	for _, bad := range []string{"A=1\nA=2\n", "not a line\n", `A="open` + "\n", "A='x' y\n"} {
		if _, err := parse(Env, []byte(bad)); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
	noNL := "A=1\nB=2"
	r = apply(t, mustLoad(t, "base: x.env\nchanges:\n  - set: {path: A, value: 1}\n"), noNL)
	if string(r.Out) != noNL {
		t.Fatalf("a file without a final newline got one: %q", r.Out)
	}
}

// TestGuardsStopApply covers every guard and every way a change can't apply.
// All problems are reported together, in line order, and nothing is output.
func TestGuardsStopApply(t *testing.T) {
	o := mustLoad(t, `base: x.json
changes:
  - set: {path: server.timeout, value: 120, expect: 30}
  - set: {path: server.port, value: 9090, expect: 8080}
  - set: {path: server.ratio, value: 2, expect: 1.5}
  - set: {path: server.big, value: 1, expect: 1.2345678901234567890e19}
  - set: {path: server.new, value: 1, expect_absent: true}
  - set: {path: server.port, value: 1, expect_absent: true}
  - delete: {path: nowhere}
  - delete: {path: server.tags, expect: [a, c]}
  - rename: {from: name, to: server}
  - copy: {from: missing, to: x}
  - set: {path: server.port.deeper, value: 1}
  - set: {path: absent.key, value: 1, expect: 5}
`)
	r := apply(t, o, strings.Replace(vendorJSON, `"timeout": 30`, `"timeout": 60`, 1))
	var got []string
	for _, p := range r.Problems {
		got = append(got, p.String())
	}
	want := []string{
		"line 3: set expects server.timeout to be 30, but the base has 60",
		"line 8: set expects server.port to be absent, but the base has 9090", // line 4 already set it
		"line 9: delete nowhere: nowhere not found",
		`line 10: delete expects server.tags to be ["a","c"], but the base has ["a","b"]`,
		"line 11: rename name to server: server already exists",
		"line 12: copy missing to x: missing not found",
		"line 13: set server.port.deeper to 1: server.port.deeper runs through a value that is not an object",
		"line 14: set expects absent.key to be 5, but the base doesn't have it",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if r.Out != nil {
		t.Fatal("output was produced despite problems")
	}
}

// TestSecretsAreNeverPrinted: values under secret-looking keys, or paths the
// overlay lists, show as REDACTED in check, diff and problems.
func TestSecretsAreNeverPrinted(t *testing.T) {
	o := mustLoad(t, `base: x.json
redact: [server.port]
changes:
  - set: {path: db.password, value: new-pass-1, expect: hunter2}
  - set: {path: auth.api_key, value: key-2}
  - set: {path: server.port, value: 9999}
  - set: {path: nested, value: {inner: {token: tok-3}}}
`)
	r := apply(t, o, vendorJSON)
	var out []string
	out = append(out, o.Diff(r)...)
	for _, c := range o.Changes {
		out = append(out, c.Describe(o.Redactor()))
	}
	bad := mustLoad(t, "base: x.json\nchanges:\n  - set: {path: db.password, value: x, expect: wrong-guess-4}\n")
	for _, p := range apply(t, bad, vendorJSON).Problems {
		out = append(out, p.String())
	}
	text := strings.Join(out, "\n")
	for _, s := range []string{"hunter2", "new-pass-1", "key-2", "9999", "8080", "tok-3", "wrong-guess-4"} {
		if strings.Contains(text, s) {
			t.Errorf("%s was printed:\n%s", s, text)
		}
	}
	if !strings.Contains(text, "~ db.password: REDACTED -> REDACTED") || !strings.Contains(text, "+ nested: an object") {
		t.Errorf("output:\n%s", text)
	}
}

func TestLoadReportsEveryProblemInLineOrder(t *testing.T) {
	_, err := Load([]byte(`version: 2
format: toml
redact: [a..b]
changes:
  - set: {path: a}
  - set: {value: 1}
  - remove: {path: a}
  - set: {path: a, value: 1, expect: 1, expect_absent: true}
  - set: {path: a, value: 1e400}
  - set: {path: a, value: 1}
    delete: {path: b}
  - rename: {from: a}
  - copy: {from: a, to: b, value: 1}
extra: 1
`))
	if err == nil {
		t.Fatal("no error")
	}
	msg := err.Error()
	want := []string{"line 1: version must be 1", "line 2: format must be json, yaml or env", "line 3: redact", "line 5: set needs value",
		"line 6: set needs path", `line 7: unknown change "remove"`, "line 8: a change can expect a value or expect_absent",
		"line 9: value", "line 10: each change is one of", "line 12: rename needs to", `line 13: copy doesn't take "value"`, `line 14: unknown key "extra"`}
	last := -1
	for _, w := range want {
		i := strings.Index(msg, w)
		if i < 0 {
			t.Fatalf("missing %q in:\n%s", w, msg)
		}
		if i < last {
			t.Fatalf("%q out of order in:\n%s", w, msg)
		}
		last = i
	}
	if _, err := Load([]byte("changes:\n  - delete: {path: a}\n")); err == nil || !strings.Contains(err.Error(), "base: is required") {
		t.Fatalf("missing base: %v", err)
	}
	if _, err := Load([]byte("base: settings.conf\nchanges:\n  - delete: {path: a}\n")); err == nil || !strings.Contains(err.Error(), "add format:") {
		t.Fatalf("unknown format: %v", err)
	}
	if _, err := Load([]byte("base: .env\nchanges:\n  - set: {path: a.b, value: 1}\n  - set: {path: A, value: [1]}\n")); err == nil ||
		!strings.Contains(err.Error(), "line 3: path: a .env file is flat") || !strings.Contains(err.Error(), "line 4: value: a .env value") {
		t.Fatalf(".env paths: %v", err)
	}
}

// TestTheBaseIsNeverWritten: a read-only base survives every command, and
// every way of naming it as the output is refused.
func TestTheBaseIsNeverWritten(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "vendor.json")
	os.WriteFile(base, []byte(vendorJSON), 0o444)
	os.Symlink(base, filepath.Join(dir, "link.json"))
	sub := filepath.Join(dir, "sub")
	os.Mkdir(sub, 0o755)
	wd, _ := os.Getwd()
	rel, _ := filepath.Rel(wd, base)
	os.Link(base, filepath.Join(dir, "hard.json"))
	os.Symlink(dir, filepath.Join(sub, "dirlink"))
	for _, out := range []string{base, filepath.Join(dir, "link.json"), filepath.Join(sub, "..", "vendor.json"), rel,
		filepath.Join(dir, "hard.json"), filepath.Join(sub, "dirlink", "vendor.json")} {
		if err := WriteOut(base, out, []byte("{}")); err != ErrSameFile {
			t.Errorf("%s: got %v, want ErrSameFile", out, err)
		}
	}
	if b, _ := os.ReadFile(base); string(b) != vendorJSON {
		t.Fatal("the base changed")
	}
	out := filepath.Join(dir, "out.json")
	if err := WriteOut(base, out, []byte("{\"a\":1}\n")); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(out); fi.Mode().Perm() != 0o444 {
		t.Errorf("output mode %v, want the base's 0444", fi.Mode().Perm())
	}
}

// TestFailedWritesLeaveTheOldOutput: a write that can't finish leaves the
// old output and no temporary files.
func TestFailedWritesLeaveTheOldOutput(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "vendor.json")
	os.WriteFile(base, []byte(vendorJSON), 0o644)
	if err := WriteOut(base, filepath.Join(dir, "missing", "out.json"), []byte("{}")); err == nil {
		t.Fatal("writing into a missing folder worked")
	}
	out := filepath.Join(dir, "out")
	os.Mkdir(out, 0o755) // a folder where the output should go can't be replaced
	if err := WriteOut(base, out, []byte("{}")); err == nil {
		t.Fatal("replacing a folder worked")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".aftermarket-config-") {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
}

func TestApplyTwiceGivesTheSameBytes(t *testing.T) {
	o := mustLoad(t, "base: x.json\nchanges:\n  - set: {path: server.timeout, value: 120}\n  - set: {path: extra.list, value: [1, {a: b}]}\n  - delete: {path: name}\n")
	a := apply(t, o, vendorJSON)
	b := apply(t, o, vendorJSON)
	if !bytes.Equal(a.Out, b.Out) || len(a.Out) == 0 {
		t.Fatal("two runs differ")
	}
}

func TestFixtures(t *testing.T) {
	dir := t.TempDir()
	o := mustLoad(t, "base: x.json\nchanges:\n  - set: {path: a, value: 2, expect: 1}\n")
	fx := filepath.Join(dir, "f.yaml")
	os.WriteFile(fx, []byte(`cases:
  - name: works
    base: '{"a": 1, "b": 2}'
    expect: |
      {"b": 2, "a": 2.0}
  - name: guard
    base: '{"a": 5}'
    expect_error: expects a to be 1
  - name: wrong expectation
    base: '{"a": 1}'
    expect: '{"a": 3}'
  - name: wrong error
    base: '{"a": 1}'
    expect_error: nope
`), 0o600)
	f, err := LoadFixture(fx)
	if err != nil {
		t.Fatal(err)
	}
	res := o.Run(f)
	if !res[0].Pass || !res[1].Pass || res[2].Pass || res[3].Pass {
		t.Fatalf("%+v", res)
	}
	for _, bad := range []string{"cases: []\n", "cases:\n  - name: x\n", "cases:\n  - base: x\n", "cases:\n  - base: x\n    expect: y\n    expect_error: z\n", "other: 1\ncases:\n  - base: x\n    expect: y\n"} {
		os.WriteFile(fx, []byte(bad), 0o600)
		if _, err := LoadFixture(fx); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

func TestFormatOf(t *testing.T) {
	for name, want := range map[string]string{"a.json": JSON, "A.YAML": YAML, "x.yml": YAML, ".env": Env, ".env.production": Env, "prod.env": Env, "x.toml": "", "env": ""} {
		if got := FormatOf(name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
}

// TestYAMLVendorFile applies an overlay to a commented YAML file. Values and
// order survive; comments don't, as the docs say.
func TestYAMLVendorFile(t *testing.T) {
	base := `# Vendor settings
server:
  host: 0.0.0.0   # listen everywhere
  port: 8080
  timeout: 30s
  compress: yes
  level: "2"
features:
  - search
  - "export: csv"
mail:
  from: "noreply@example.com"
  smtp_password: vendor-pass
`
	o := mustLoad(t, `base: settings.yaml
changes:
  - set: {path: server.timeout, value: 2m, expect: 30s}
  - set: {path: mail.smtp_password, value: ours}
  - delete: {path: features.0, expect: search}
  - set: {path: server.retries, value: 3}
  - set: {path: server.mode, value: "on"}
`)
	r := apply(t, o, base)
	if len(r.Problems) > 0 {
		t.Fatal(r.Problems)
	}
	want := `server:
  host: 0.0.0.0
  port: 8080
  timeout: 2m
  compress: yes
  level: "2"
  retries: 3
  mode: "on"
features:
  - "export: csv"
mail:
  from: noreply@example.com
  smtp_password: ours
`
	if string(r.Out) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", r.Out, want)
	}
	if d := strings.Join(o.Diff(r), "\n"); strings.Contains(d, "ours") || strings.Contains(d, "vendor-pass") {
		t.Fatalf("diff printed a password:\n%s", d)
	}
}
