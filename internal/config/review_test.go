package config

import (
	"bytes"
	"strings"
	"testing"

	"github.com/aftermarket-dev/aftermarket/internal/ojson"
	"github.com/aftermarket-dev/aftermarket/internal/rules"
	"github.com/aftermarket-dev/aftermarket/internal/yamlite"
)

// Review finding: rename a -> a.b deleted a and everything under it.
func TestRenameIntoItselfIsRefused(t *testing.T) {
	for _, ch := range []string{"rename: {from: a, to: a.b}", "rename: {from: a.x, to: a}", "rename: {from: l.0, to: l.0.x}"} {
		o := mustLoad(t, "base: x.json\nchanges:\n  - "+ch+"\n")
		r := apply(t, o, `{"a": {"x": 1}, "l": [{"y": 2}], "k": 2}`)
		if len(r.Problems) != 1 || !strings.Contains(r.Problems[0].Msg, "into itself") || r.Out != nil {
			t.Errorf("%s: problems %v, out %s", ch, r.Problems, r.Out)
		}
	}
}

// Review finding: strings with a colon were written bare and the YAML
// couldn't be read back. Every YAML output is now read back before it is
// written.
func TestYAMLColonsAndKeysReadBack(t *testing.T) {
	base := `label: "Note:"
ports:
  80: http
  "443": https
on: push
"0x10": hex
"012": octal
url: "http://example.com/a#b"
`
	o := mustLoad(t, "base: x.yaml\nchanges:\n  - set: {path: z, value: \"key:\"}\n  - set: {path: \"8080\", value: alt}\n  - set: {path: \"yes\", value: \"a # b\"}\n")
	r := apply(t, o, base)
	if len(r.Problems) > 0 {
		t.Fatal(r.Problems)
	}
	want := `label: "Note:"
ports:
  80: http
  "443": https
on: push
"0x10": hex
"012": octal
url: "http://example.com/a#b"
z: "key:"
"8080": alt
"yes": "a # b"
`
	if string(r.Out) != want {
		t.Fatalf("got:\n%s\nwant:\n%s", r.Out, want)
	}
	n, err := yamlite.Parse(r.Out)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := ojson.Get(rules.NodeToJSON(n), ojson.Path{"label"}); v != "Note:" {
		t.Fatalf("label reads back as %v", v)
	}
}

// Review finding: a failing fixture printed the whole output, secrets and all.
func TestFixtureFailuresHideSecrets(t *testing.T) {
	o := mustLoad(t, "base: x.json\nchanges:\n  - set: {path: a, value: 2}\n")
	f := &Fixture{Cases: []FixtureCase{{Name: "c", Base: `{"a": 1, "db": {"password": "hunter2"}}`, Expect: strPtr(`{"a": 3, "db": {"password": "other"}}`)}}}
	res := o.Run(f)
	msg := strings.Join(res[0].Problems, "\n")
	if res[0].Pass || strings.Contains(msg, "hunter2") || strings.Contains(msg, "other") || !strings.Contains(msg, "~ a: 3 -> 2") {
		t.Fatalf("%s", msg)
	}
}

func strPtr(s string) *string { return &s }

// Review findings: a secret moved to an innocent-looking path, common
// secret names, and URLs with a password were printed.
func TestSecretsStayHiddenWhenMovedOrNamedDifferently(t *testing.T) {
	o := mustLoad(t, `base: x.json
changes:
  - rename: {from: db.password, to: db.pw}
  - copy: {from: creds, to: backup}
  - set: {path: replica_url, value: "postgres://u:url-secret-1@h/db"}
`)
	r := apply(t, o, `{"db": {"password": "hunter2"}, "creds": {"api_key": "k-2"}, "DB_PASS": "p-3", "auth_header": "a-4"}`)
	text := strings.Join(o.Diff(r), "\n")
	for _, s := range []string{"hunter2", "k-2", "url-secret-1"} {
		if strings.Contains(text, s) {
			t.Errorf("%s was printed:\n%s", s, text)
		}
	}
	if !strings.Contains(text, "postgres://REDACTED@h/db") {
		t.Errorf("diff:\n%s", text)
	}
	bad := mustLoad(t, "base: .env\nchanges:\n  - set: {path: DB_PASS, value: x, expect: guess-5}\n  - set: {path: AUTH_HEADER, value: x, expect: guess-6}\n")
	rb := apply(t, bad, "DB_PASS=p-3\nAUTH_HEADER=a-4\n")
	var msgs []string
	for _, p := range rb.Problems {
		msgs = append(msgs, p.String())
	}
	m := strings.Join(msgs, "\n")
	for _, s := range []string{"p-3", "a-4", "guess-5", "guess-6"} {
		if strings.Contains(m, s) {
			t.Errorf("%s was printed:\n%s", s, m)
		}
	}
}

// Review findings: .env rewrites changed meaning ($ expanded, \x lost),
// dropped comments and mixed line endings.
func TestEnvRewritesKeepMeaning(t *testing.T) {
	base := "A='lit $USER'  # note\r\nB=\"a\\$b\"\r\nC=1 # keep me\r\nD=plain\r\n"
	o := mustLoad(t, `base: .env
changes:
  - rename: {from: A, to: A2}
  - rename: {from: B, to: B2}
  - set: {path: C, value: 2}
  - set: {path: D, value: "has $HOME and 'quote'"}
  - set: {path: E, value: "x $y"}
`)
	r := apply(t, o, base)
	if len(r.Problems) > 0 {
		t.Fatal(r.Problems)
	}
	want := "A2='lit $USER'  # note\r\nB2=\"a\\$b\"\r\nC=2 # keep me\r\nD=\"has \\$HOME and 'quote'\"\r\nE='x $y'\r\n"
	if string(r.Out) != want {
		t.Fatalf("got  %q\nwant %q", r.Out, want)
	}
	back, err := parse(Env, r.Out)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"A2": "lit $USER", "B2": "a$b", "C": "2", "D": "has $HOME and 'quote'", "E": "x $y"} {
		if got, _ := ojson.Get(back.root(), ojson.Path{k}); got != v {
			t.Errorf("%s reads back as %q, want %q", k, got, v)
		}
	}
}

// Review finding: the docs promised byte-for-byte output for any JSON. Now an
// overlay with nothing to change returns the file exactly, whatever its
// layout, and CRLF files keep CRLF.
func TestUnchangedFilesComeBackExactly(t *testing.T) {
	o := mustLoad(t, "base: x.json\nchanges:\n  - set: {path: a, value: 1, expect: 1}\n")
	for _, base := range []string{"{\"a\": 1, \"l\": [1, 2, 3], \"s\": \"\\u00e9\\/\"}\n", "{\"a\":1}", "{\r\n  \"a\": 1\r\n}\r\n"} {
		if r := apply(t, o, base); string(r.Out) != base {
			t.Errorf("%q came back as %q", base, r.Out)
		}
	}
	crlf := "{\r\n  \"a\": 1,\r\n  \"b\": 2\r\n}\r\n"
	r := apply(t, mustLoad(t, "base: x.json\nchanges:\n  - set: {path: b, value: 3}\n"), crlf)
	if string(r.Out) != "{\r\n  \"a\": 1,\r\n  \"b\": 3\r\n}\r\n" {
		t.Fatalf("got %q", r.Out)
	}
}

// Review finding: deleting from a top-level list failed.
func TestTopLevelListDelete(t *testing.T) {
	o := mustLoad(t, "base: x.json\nchanges:\n  - delete: {path: \"1\", expect: b}\n")
	r := apply(t, o, `["a", "b", "c"]`)
	if len(r.Problems) > 0 || string(r.Out) != `["a","c"]` {
		t.Fatalf("%v %s", r.Problems, r.Out)
	}
}

// Review finding: deep nesting made a diff crawl. It's refused when read.
func TestDeepNestingIsRefused(t *testing.T) {
	deep := strings.Repeat(`{"a":`, 300) + "1" + strings.Repeat("}", 300)
	if _, err := mustLoad(t, "base: x.json\nchanges:\n  - set: {path: b, value: 1}\n").Apply([]byte(deep)); err == nil || !strings.Contains(err.Error(), "nested more than") {
		t.Fatalf("err = %v", err)
	}
	if _, err := parse(JSON, bytes.Repeat([]byte("["), 10)); err == nil {
		t.Fatal("broken JSON read")
	}
}
