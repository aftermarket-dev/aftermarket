package patch

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

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

// See CheckEarlyAnswerConnections.
func TestEarlyAnswerConnections(t *testing.T) {
	tk.CheckEarlyAnswerConnections(t, func(up string) string {
		return startProxy(t, "limits: {upstream_timeout: 300ms}\n"+strings.TrimPrefix(neverMatches, "\n"), up, false).proxyURL
	})
}

// A JSON body a rule reads may arrive slowly, as long as it keeps moving:
// a byte every 100ms under a 300ms limit gets through and is patched.
func TestSlowBodyARuleReads(t *testing.T) {
	got := make(chan string, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- string(b)
	}))
	defer up.Close()
	h := startProxy(t, "limits: {upstream_timeout: 300ms}\n"+strings.TrimPrefix(requestRules, "\n"), up.URL, false)
	conn, err := net.Dial("tcp", strings.TrimPrefix(h.proxyURL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	body := `{"qty":3}`
	fmt.Fprintf(conn, "POST /orders HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n", len(body))
	for i := range body {
		time.Sleep(100 * time.Millisecond)
		conn.Write([]byte{body[i]})
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("slow body: %v %v", resp, err)
	}
	if b := <-got; !strings.Contains(b, `"quantity":3`) {
		t.Fatalf("upstream got %s", b)
	}
}
