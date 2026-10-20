package replay_test

import (
	"bytes"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/aftermarket-dev/aftermarket/internal/replay"
)

// Review finding: 256-bit floats called different numbers equal.
func TestNumbersAreComparedExactly(t *testing.T) {
	pairs := []struct {
		a, b string
		same bool
	}{
		{"1", "1.0", true},
		{"100", "1e2", true},
		{"0.5", "5e-1", true},
		{"-0", "0", true},
		{"123456789012345678901234567890123456789012345678901234567890123456789012345678901", "123456789012345678901234567890123456789012345678901234567890123456789012345678902", false},
		{"1", "1.000000000000000000000000000000000000000000000000000000000000000000000000000000000000000001", false},
		{"1e999999999", "2e999999999", false},
		{"1e-999999999", "2e-999999999", false},
		{"1e999999999", "1e999999999", true},
	}
	f := load(t, oneGet)
	for _, p := range pairs {
		rep := run(t, f, replay.Options{A: mustURL(t, jsonServer(t, "application/json", "["+p.a+"]")), B: mustURL(t, jsonServer(t, "application/json", "["+p.b+"]"))})
		if rep.OK() != p.same {
			t.Errorf("%s vs %s: same=%v, want %v (%v)", p.a, p.b, rep.OK(), p.same, rep.Cases[0].Diffs)
		}
	}
}

// Review finding: a replay where every case was skipped passed.
func TestNothingComparedIsNotOK(t *testing.T) {
	f := load(t, `cases:
  - name: w
    request: {method: POST, path: /x}
    expect:
      response: {status: 200}
`)
	a := serve(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") })
	rep := run(t, f, replay.Options{A: mustURL(t, a), B: mustURL(t, a)})
	if rep.OK() || rep.Skipped != 1 {
		t.Fatalf("got %+v", rep)
	}
	var md bytes.Buffer
	rep.WriteMarkdown(&md)
	if !strings.Contains(md.String(), "Nothing was compared") {
		t.Fatalf("report:\n%s", md.String())
	}
}

// Review finding: URLs with a password or a query string showed them in the
// report.
func TestReportHidesURLSecrets(t *testing.T) {
	a := serve(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") })
	u, _ := url.Parse(a)
	withSecrets := mustURL(t, "http://bob:pw-secret@"+u.Host+"/base?api_key=q-secret")
	rep := run(t, load(t, oneGet), replay.Options{A: withSecrets, B: withSecrets})
	var md, js bytes.Buffer
	rep.WriteMarkdown(&md)
	rep.WriteJSON(&js)
	if strings.Contains(md.String()+js.String(), "secret") {
		t.Fatalf("report:\n%s", md.String())
	}
}
