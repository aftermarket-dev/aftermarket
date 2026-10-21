package adapt_test

import (
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/aftermarket-dev/aftermarket/internal/adapt"
	"github.com/aftermarket-dev/aftermarket/internal/audit"
	tk "github.com/aftermarket-dev/aftermarket/internal/testkit"
)

// See TestEarlyAnswerIsNotCutOff in patch: a fast upstream's answer to a
// request with a body must arrive whole.
func TestEarlyAnswerIsNotCutOff(t *testing.T) {
	cfg, err := adapt.Load([]byte(exampleConfig(t)))
	if err != nil {
		t.Fatal(err)
	}
	ea := &tk.EarlyAnswer{}
	u, _ := url.Parse("http://upstream.invalid")
	px, err := adapt.New(adapt.Options{Upstream: u, Config: cfg, Audit: audit.New(&tk.AuditSink{}), Transport: ea})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(px)
	defer srv.Close()
	tk.CheckEarlyAnswer(t, srv.URL, ea)
}

// A health check with a body answers itself and must keep the connection
// usable.
func TestHealthCheckWithBody(t *testing.T) {
	h := start(t, exampleConfig(t), "http://upstream.invalid", false)
	tk.CheckHealthWithBody(t, h.url, adapt.DefaultHealthPath)
}

// See TestStalledAndSlowBodies in patch.
func TestStalledAndSlowBodies(t *testing.T) {
	tk.CheckStalledBodies(t, func(up string) string {
		return start(t, "limits: {upstream_timeout: 300ms}\n"+exampleConfig(t), up, false).url
	})
}
