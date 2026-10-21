package adapt_test

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

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

// See CheckEarlyAnswerConnections in testkit.
func TestEarlyAnswerConnections(t *testing.T) {
	tk.CheckEarlyAnswerConnections(t, func(up string) string {
		return start(t, "limits: {upstream_timeout: 300ms}\n"+exampleConfig(t), up, false).url
	})
}

// A body adapt read for a template and then forwarded as it was (here, not
// JSON) is already in memory: an upstream that takes longer than
// upstream_timeout to read it must still get all of it, and the client the
// real answer.
func TestBufferedBodyToASlowUpstream(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Second)
		b, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "got %d", len(b))
	}))
	defer up.Close()
	h := start(t, "limits: {upstream_timeout: 300ms, max_body: 64MiB}\n"+exampleConfig(t), up.URL, false)
	body := bytes.Repeat([]byte("x"), 4<<20)
	got := tk.Do(t, "POST", h.url+"/api/v1/customers", http.Header{"Content-Type": {"application/json"}}, body)
	if got.Status != 200 || string(got.Body) != fmt.Sprintf("got %d", len(body)) {
		t.Fatalf("status %d, body %q", got.Status, got.Body)
	}
}

// A JSON body a template reads may arrive slowly, as long as it keeps
// moving.
func TestSlowBodyATemplateReads(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"customer":{"id":7}}`))
	}))
	defer up.Close()
	h := start(t, "limits: {upstream_timeout: 300ms}\n"+exampleConfig(t), up.URL, false)
	conn, err := net.Dial("tcp", strings.TrimPrefix(h.url, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	body := `{"name":"Ada"}`
	fmt.Fprintf(conn, "POST /api/v1/customers HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n", len(body))
	for i := range body {
		time.Sleep(100 * time.Millisecond)
		conn.Write([]byte{body[i]})
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil || resp.StatusCode == 408 {
		t.Fatalf("slow body: %v %v", resp, err)
	}
}
