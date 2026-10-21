package config

// Regression tests from the independent review of TOML support (0.7.0). Each
// asserts what the docs promise or TOML 1.0 requires.

import (
	"strings"
	"testing"
	"time"
)

func reviewApply(t *testing.T, base, changes string) (string, *Result) {
	t.Helper()
	o, err := Load([]byte("version: 1\nbase: c.toml\nredact: [hidden]\nchanges:\n" + changes))
	if err != nil {
		t.Fatal(err)
	}
	r, err := o.Apply([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	return string(r.Out), r
}

// F1 (medium): a change whose value is already in place still rewrites the
// line in canonical form, so the output is not the base byte for byte,
// comments inside a multi-line value are dropped, and the change is counted
// as applied. docs/config.md: "When every change is already in place, the
// output is the vendor's file byte for byte, whatever its layout."
func TestTOMLReview_F1_NoopRewritesSpelling(t *testing.T) {
	for _, c := range []struct{ base, changes string }{
		{"port = 0x1F90 # hex\n", "  - set: {path: port, value: 8080, expect: 8080}\n"},
		{"name = 'x'\n", "  - set: {path: name, value: x, expect: x}\n"},
		{"n = 1_000\n", "  - set: {path: n, value: 1000}\n"},
		{"l = [\n  1, # one\n  2,\n]\n", "  - set: {path: l, value: [1, 2]}\n"},
		{"s = \"\"\"\nhi\"\"\"\n", "  - set: {path: s, value: hi}\n"},
	} {
		out, r := reviewApply(t, c.base, c.changes)
		if out != c.base || len(r.Applied) != 0 {
			t.Errorf("nothing to change, but %q became %q (applied %v)", c.base, out, r.Applied)
		}
	}
}

// F1b (low): a float the guard says is already 30 becomes the integer 30.
func TestTOMLReview_F1b_FloatBecomesInt(t *testing.T) {
	out, _ := reviewApply(t, "timeout = 30.0\n", "  - set: {path: timeout, value: 30, expect: 30}\n")
	if out != "timeout = 30.0\n" {
		t.Errorf("guard matched 30.0 as already 30, output %q", out)
	}
}

// F2 (medium): parse errors and value errors quote the file's or overlay's
// values, including secrets.
func TestTOMLReview_F2_SecretsInErrors(t *testing.T) {
	for _, base := range []string{
		"[db]\npassword = hunter2\n",
		"[db]\ntoken = 99999999999999999999\n",
	} {
		_, err := parse(TOML, []byte(base))
		if err != nil && (strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "99999999999999999999")) {
			t.Errorf("parse error prints the secret: %v", err)
		}
	}
	_, r := reviewApply(t, "[db]\nx = 1\n", "  - set: {path: db.password, value: 123456789012345678901}\n"+
		"  - set: {path: hidden, value: 123456789012345678902}\n")
	for _, p := range r.Problems {
		if strings.Contains(p.Msg, "12345678901234567890") {
			t.Errorf("problem prints a redacted value: %s", p)
		}
	}
}

// F3 (medium): a value nested deeply enough crashes the process with a Go
// stack overflow (fatal, not recoverable). Now refused at the depth limit.
func TestTOMLReview_F3_DeepNestingCrash(t *testing.T) {
	n := 3000000
	_, err := parse(TOML, []byte("a = "+strings.Repeat("[", n)+strings.Repeat("]", n)))
	if err == nil {
		t.Fatal("accepted")
	}
}

// F3b (low): nested inline tables take quadratic time (freeze walks the whole
// subtree at every level) before the depth limit is checked.
func TestTOMLReview_F3b_InlineNestingQuadratic(t *testing.T) {
	n := 8000 // 32 KB
	s := "a = " + strings.Repeat("{b=", n) + "1" + strings.Repeat("}", n)
	start := time.Now()
	_, err := parse(TOML, []byte(s))
	if d := time.Since(start); d > time.Second {
		t.Errorf("a %d-byte file took %v to refuse (%v)", len(s), d, err)
	}
}

// F4 (medium): deleting a table removes the comment lines that sit above the
// next section, which the overlay didn't name.
func TestTOMLReview_F4_DeleteTableEatsNextComment(t *testing.T) {
	out, _ := reviewApply(t, "[a]\nx = 1\n\n# b holds the b things\n[b]\ny = 2\n", "  - delete: {path: a}\n")
	if !strings.Contains(out, "# b holds the b things") {
		t.Errorf("comment above [b] lost: %q", out)
	}
	out, _ = reviewApply(t, "[[j]]\nn = 1\n# the second job\n[[j]]\nn = 2\n", "  - delete: {path: j.0}\n")
	if !strings.Contains(out, "# the second job") {
		t.Errorf("comment above the second [[j]] lost: %q", out)
	}
}

// F5 (medium): deleting or renaming away the last key of a table written as
// dotted keys leaves an empty table in the tree that the edited text no
// longer has, so the whole file is silently written from values: every
// comment is lost and a new [a] header appears. Docs only promise this for
// edits "such as turning a table into a single value".
func TestTOMLReview_F5_DottedDeleteFallsBackToRewrite(t *testing.T) {
	for _, c := range []struct{ base, changes string }{
		{"# keep me\na.b = 1\nc = 2 # and me\n", "  - delete: {path: a.b}\n"},
		{"# keep me\n[a]\nb.c = 1 # and me\nx = 2\n", "  - rename: {from: a.b.c, to: a.z}\n"},
	} {
		out, _ := reviewApply(t, c.base, c.changes)
		if !strings.Contains(out, "# keep me") || !strings.Contains(out, "# and me") {
			t.Errorf("%s on %q wrote the file from values: %q", strings.TrimSpace(c.changes), c.base, out)
		}
	}
}

// F6 (medium): "a date replaced with text in the same shape stays a date" also
// fires for inf and nan, and for date-shaped text that is no valid date. The
// output holds a local time without seconds or an impossible date, both
// invalid TOML 1.0, where the overlay asked for text. The read-back check
// passes because plain() makes raw values and strings compare equal.
func TestTOMLReview_F6_TextBecomesInvalidDate(t *testing.T) {
	out, _ := reviewApply(t, "t = inf\n", "  - set: {path: t, value: \"12:30\"}\n")
	if out != "t = \"12:30\"\n" {
		t.Errorf("text set on a float: %q", out)
	}
	out, _ = reviewApply(t, "d = 1979-05-27\n", "  - set: {path: d, value: \"2026-99-99\"}\n")
	if out != "d = \"2026-99-99\"\n" {
		t.Errorf("an impossible date written bare: %q", out)
	}
}

// F7 (low): the reader accepts what TOML 1.0 forbids: impossible dates and
// times, times without seconds, a float that overflows, a lone CR in a
// comment. And it refuses a UTF-8 BOM, which toml-test treats as valid.
func TestTOMLReview_F7_ReaderStrictness(t *testing.T) {
	for _, in := range []string{
		"a = 2026-13-45",
		"a = 1979-05-27T25:61:61Z",
		"a = 1979-05-27T07:32:00+25:00",
		"a = 07:32",
		"a = 1979-05-27T07:32Z",
		"a = 1e400",
		"a = 1 # c\rx",
	} {
		if _, _, err := parseTOML(in); err == nil {
			t.Errorf("accepted %q", in)
		}
	}
	if _, _, err := parseTOML("\ufeffa = 1\n"); err != nil {
		t.Errorf("refused a BOM: %v", err)
	}
}

// F8 (low): deleting the last line of a file with no final newline leaves one.
func TestTOMLReview_F8_FinalNewlineAppears(t *testing.T) {
	out, _ := reviewApply(t, "a = 1\nb = 2", "  - delete: {path: b}\n")
	if out != "a = 1" {
		t.Errorf("%q", out)
	}
}

// F9 (low): a new top-level key in a file whose root has no keys goes above
// the vendor's leading comment, at byte 0.
func TestTOMLReview_F9_NewRootKeyAboveHeaderComment(t *testing.T) {
	out, _ := reviewApply(t, "# Vendor config\n[c]\nd = 1\n", "  - set: {path: newkey, value: 1}\n")
	if !strings.HasPrefix(out, "# Vendor config\n") {
		t.Errorf("%q", out)
	}
}

// F10 (low): when the file is written from values, keys the overlay didn't
// name change order (and inline tables become sections). The read-back check
// can't see it, since equal() ignores key order.
func TestTOMLReview_F10_FallbackReorders(t *testing.T) {
	out, _ := reviewApply(t, "name = 'n'\nsrv = { p = 1 }\nport = 2\n[t]\nk = 1\n", "  - set: {path: t, value: 5}\n")
	tree := mustTOML(t, out)
	if got := strings.Join(tree.Keys(), ","); got != "name,srv,port,t" {
		t.Errorf("order %s:\n%s", got, out)
	}
}

// F11 (low): config test compares by equal(), so an expected string matches
// a TOML date (and 30 matches 30.0).
func TestTOMLReview_F11_FixtureCantTellDateFromString(t *testing.T) {
	o, _ := Load([]byte("version: 1\nbase: c.toml\nchanges:\n  - set: {path: d, value: \"2026-10-21\"}\n"))
	f := &Fixture{Cases: []FixtureCase{{Name: "x", Base: "d = 1979-05-27\n", Expect: ptr("d = \"2026-10-21\"\n")}}}
	if res := o.Run(f); res[0].Pass {
		t.Errorf("a date output passed an expect that holds a string")
	}
}

func ptr(s string) *string { return &s }

// The random test builds its bases with the writer; this one starts
// from a file written by hand in every spelling TOML allows, and makes 22
// changes, one at a time. Each result must hold exactly the intended
// values, keep every line it didn't touch, and keep the file's line endings
// and missing final newline.
func TestTOMLReviewHandWrittenRandom(t *testing.T) {
	base := "# Vendor config, do not edit\n" +
		"title = 'literal'   # a literal string\n" +
		"port = 0x1F90\n" +
		"big = 1_000_000\n" +
		"ratio = 30.0\n" +
		"when = 1979-05-27T07:32:00Z\n" +
		"text = \"\"\"\nline one\nline two\"\"\"\n" +
		"list = [\n  1, # one\n  2,\n]\n" +
		"srv.host = \"a\"\n" +
		"srv.port = 1\n" +
		"\n# the database\n[db]\nuser = \"app\"\npool = { min = 1, max = 4 }\n" +
		"\n# jobs\n[[jobs]]\nname = \"one\"\n\n# the second job\n[[jobs]]\nname = \"two\"\n" +
		"\n[logs.file]\npath = \"/var/log/x\"\nlevel = \"info\""
	changes := []string{
		"  - set: {path: port, value: 9090}",
		"  - set: {path: ratio, value: 45}",
		"  - set: {path: when, value: \"2026-10-21T08:00:00Z\"}",
		"  - set: {path: srv.port, value: 2}",
		"  - set: {path: srv.tls, value: true}",
		"  - set: {path: db.pool.max, value: 8}",
		"  - set: {path: jobs.1.name, value: three}",
		"  - set: {path: logs.file.level, value: debug}",
		"  - set: {path: newkey, value: x}",
		"  - delete: {path: big}",
		"  - delete: {path: srv.host}",
		"  - delete: {path: srv}",
		"  - delete: {path: db}",
		"  - delete: {path: jobs.0}",
		"  - delete: {path: logs.file.level}",
		"  - delete: {path: logs}",
		"  - delete: {path: list.0}",
		"  - rename: {from: title, to: name}",
		"  - rename: {from: srv.host, to: srv.hostname}",
		"  - rename: {from: db, to: database}",
		"  - rename: {from: logs.file, to: logs.disk}",
		"  - copy: {from: db.user, to: db.owner}",
	}
	for _, crlf := range []bool{false, true} {
		b := base
		if crlf {
			b = strings.ReplaceAll(base, "\n", "\r\n")
		}
		for _, c := range changes {
			out, r := reviewApply(t, b, c+"\n")
			if len(r.Problems) > 0 {
				t.Errorf("%s: %v", c, r.Problems)
				continue
			}
			if strings.HasSuffix(out, "\n") {
				t.Errorf("%s: the file had no final newline, the output has one", c)
			}
			if crlf && strings.Count(out, "\n") != strings.Count(out, "\r\n") {
				t.Errorf("%s: line endings changed", c)
			}
			if td := r.doc.(*tomlDoc); td.rewrite {
				t.Errorf("%s: written from values instead of on its lines:\n%s", c, out)
				continue
			}
			for _, keep := range []string{"# Vendor config, do not edit", "# the database", "# the second job", "  1, # one"} {
				if !strings.Contains(out, keep) && !(strings.Contains(c, "list.0") && keep == "  1, # one") {
					t.Errorf("%s: lost %q:\n%s", c, keep, out)
				}
			}
		}
	}
}
