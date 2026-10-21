package record_test

import (
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/aftermarket-dev/aftermarket/internal/audit"
	"github.com/aftermarket-dev/aftermarket/internal/record"
	tk "github.com/aftermarket-dev/aftermarket/internal/testkit"
)

// See TestEarlyAnswerIsNotCutOff in patch: a fast upstream's answer to a
// request with a body must arrive whole, recorded or not.
func TestEarlyAnswerIsNotCutOff(t *testing.T) {
	ea := &tk.EarlyAnswer{}
	u, _ := url.Parse("http://upstream.invalid")
	rec, err := record.New(record.Options{Upstream: u, Config: record.Default(), Out: &lockedBuffer{}, Audit: audit.New(&tk.AuditSink{}), Transport: ea})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	tk.CheckEarlyAnswer(t, srv.URL, ea)
}

// A health check with a body answers itself and must keep the connection
// usable.
func TestHealthCheckWithBody(t *testing.T) {
	h := start(t, "", "http://upstream.invalid")
	tk.CheckHealthWithBody(t, h.url, "/_aftermarket/healthz")
}

// See TestStalledAndSlowBodies in patch.
func TestStalledAndSlowBodies(t *testing.T) {
	tk.CheckStalledBodies(t, func(up string) string {
		return start(t, "limits: {upstream_timeout: 300ms}\n", up).url
	})
}
