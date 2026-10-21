package record_test

import (
	"io"
	"net/http"
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

// See CheckEarlyAnswerConnections in testkit.
func TestEarlyAnswerConnections(t *testing.T) {
	tk.CheckEarlyAnswerConnections(t, func(up string) string {
		return start(t, "limits: {upstream_timeout: 300ms}\n", up).url
	})
}

// Ordinary requests keep their connection while being recorded.
func TestKeepAliveWithBodies(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.ReadAll(r.Body) }))
	defer up.Close()
	h := start(t, "", up.URL)
	tk.CheckKeepAlive(t, h.url,
		"POST /a HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 9\r\n\r\n{\"qty\":3}",
		"POST /b HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n0\r\n\r\n")
}

// See CheckOddClients.
func TestOddClients(t *testing.T) {
	tk.CheckOddClients(t, func(up string) string {
		return start(t, "limits: {upstream_timeout: 300ms}\n", up).url
	})
}

// See CheckClientGone.
func TestClientGoneFreesTheUpstream(t *testing.T) {
	tk.CheckClientGone(t, func(up string) string {
		return start(t, "limits: {upstream_timeout: 300ms}\n", up).url
	})
}

// A body that breaks off gets 400 and is never recorded.
func TestCutBody(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.ReadAll(r.Body) }))
	defer up.Close()
	h := start(t, "", up.URL)
	tk.CheckCutBody(t, h.url, "/orders", nil)
}
