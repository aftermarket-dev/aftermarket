package patch

import (
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/aftermarket-dev/aftermarket/internal/audit"
	"github.com/aftermarket-dev/aftermarket/internal/rules"
	tk "github.com/aftermarket-dev/aftermarket/internal/testkit"
)

// An upstream that answers before reading the end of the request body must
// not have its answer cut off. Go's server used to close the client's body
// as the answer went out, while the transport was still reading it, and the
// transport then dropped the upstream connection mid-answer. This was the
// rare "unexpected EOF" in the random pass-through tests on CI.
func TestEarlyAnswerIsNotCutOff(t *testing.T) {
	for _, cfg := range []string{neverMatches, shapeRule} {
		c, err := rules.Load([]byte(cfg))
		if err != nil {
			t.Fatal(err)
		}
		ea := &tk.EarlyAnswer{}
		u, _ := url.Parse("http://upstream.invalid")
		px, err := New(Options{Upstream: u, Config: c, Audit: audit.New(&syncBuffer{}), Transport: ea})
		if err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewServer(px)
		tk.CheckEarlyAnswer(t, srv.URL, ea)
		srv.Close()
	}
}

// A health check with a body answers itself and must keep the connection
// usable.
func TestHealthCheckWithBody(t *testing.T) {
	h := startProxy(t, neverMatches, "http://upstream.invalid", false)
	tk.CheckHealthWithBody(t, h.proxyURL, DefaultHealthPath)
}

// A client body that stops arriving gets an answer and its connection let
// go, even when the upstream can't be reached; one that keeps moving,
// however slowly, arrives whole.
func TestStalledAndSlowBodies(t *testing.T) {
	tk.CheckStalledBodies(t, func(up string) string {
		return startProxy(t, "limits: {upstream_timeout: 300ms}\n"+strings.TrimPrefix(neverMatches, "\n"), up, false).proxyURL
	})
}
