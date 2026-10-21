package patch

// Tests added after the independent review of 0.1.0. Each one reproduces a
// finding that the first test suite missed.

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aftermarket-dev/aftermarket/internal/audit"
)

// rawRequest sends a request exactly as written, so tests can use URLs and
// headers Go's client would never produce.
func rawRequest(t *testing.T, base, raw string) *http.Response {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(conn, raw); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp
}

func TestOddQueryStringsPassThroughExactly(t *testing.T) {
	rec := &recorder{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{}`)
	}))
	defer up.Close()
	matching := `
rules:
  - id: hdr
    when: {}
    request: {set_headers: {X-Seen: "1"}}
    response: {set_headers: {X-Seen: "1"}}
`
	for name, cfg := range map[string]string{"no rule matches": neverMatches, "a rule matches": matching} {
		h := startProxy(t, cfg, up.URL, false)
		for _, uri := range []string{"/p?a=1;b=2", "/p?q=100%", "/p?x=%zz&y=1", "/p?a=1&a=2&&b", "/p?", "/p?%41=%42"} {
			rawRequest(t, h.proxyURL, "GET "+uri+" HTTP/1.1\r\nHost: x\r\n\r\n")
			if got := rec.last().uri; got != uri {
				t.Errorf("%s: %s reached the upstream as %s", name, uri, got)
			}
		}
	}
}

func TestConnectionListedHeadersAreDroppedWhenARuleMatches(t *testing.T) {
	rec := &recorder{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { rec.add(r) }))
	defer up.Close()
	h := startProxy(t, "rules:\n  - id: hdr\n    when: {}\n    request: {set_headers: {X-Seen: \"1\"}}\n", up.URL, false)
	rawRequest(t, h.proxyURL, "GET /p HTTP/1.1\r\nHost: x\r\nConnection: keep-alive, X-Hop\r\nX-Hop: secret\r\nX-Keep: k\r\n\r\n")
	got := rec.last().header
	if got.Get("X-Hop") != "" {
		t.Fatal("a header named in Connection was forwarded")
	}
	if got.Get("X-Keep") != "k" || got.Get("X-Seen") != "1" {
		t.Fatalf("headers = %v", got)
	}
}

func TestForwardingHeadersCanBeChanged(t *testing.T) {
	rec := &recorder{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { rec.add(r) }))
	defer up.Close()
	cfg := "rules:\n  - id: fwd\n    when: {}\n    request: {remove_headers: [X-Forwarded-For], set_headers: {Forwarded: for=192.0.2.1}}\n"
	h := startProxy(t, cfg, up.URL, false)
	do(t, "GET", h.proxyURL+"/", http.Header{"X-Forwarded-For": {"10.0.0.1"}}, nil)
	got := rec.last().header
	if got.Get("X-Forwarded-For") != "" || got.Get("Forwarded") != "for=192.0.2.1" {
		t.Fatalf("headers = %v", got)
	}
}

func TestRewriteCannotEscapeItsPrefix(t *testing.T) {
	rec := &recorder{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { rec.add(r) }))
	defer up.Close()
	cfg := `
rules:
  - id: token
    when: {path: /old}
    request: {rewrite_path: {from: "^/old$", to: "/api/customers/{query.id}/profile"}}
  - id: group
    when: {path: /x/**}
    request: {rewrite_path: {from: "^/x/(.*)$", to: "/y/$1"}}
`
	h := startProxy(t, cfg, up.URL, false)
	cases := map[string]string{
		"/old?id=..":          "/old?id=..",
		"/old?id=%2e%2e":      "/old?id=%2e%2e",
		"/old?id=.":           "/old?id=.",
		"/old?id=a%2Fb":       "/old?id=a%2Fb",
		"/old?id=7":           "/api/customers/7/profile?id=7",
		"/old?id=a%20b":       "/api/customers/a%20b/profile?id=a%20b",
		"/x/a%2541":           "/y/a%2541",
		"/x/%252e%252e/admin": "/y/%252e%252e/admin",
		"/x/a/b":              "/y/a/b",
		"/x/a%2Fb":            "/x/a%2Fb", // encoded slash: path rules never match
		"/x/../admin":         "/x/../admin",
		"/x/a/%2e%2e/admin":   "/x/a/%2e%2e/admin",
		"/x/ok%3Fnot-a-query": "/y/ok%3Fnot-a-query",
	}
	for in, want := range cases {
		rawRequest(t, h.proxyURL, "GET "+in+" HTTP/1.1\r\nHost: x\r\n\r\n")
		if got := rec.last().uri; got != want {
			t.Errorf("%s reached the upstream as %s, want %s", in, got, want)
		}
	}
}

func TestPathRulesIgnoreDotSegments(t *testing.T) {
	rec := &recorder{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { rec.add(r) }))
	defer up.Close()
	cfg := "rules:\n  - id: key\n    when: {path: /public/**}\n    request: {set_headers: {X-Internal-Key: k}}\n"
	h := startProxy(t, cfg, up.URL, false)
	for _, p := range []string{"/public/../admin", "/public/./x", "/public/a%2Fb"} {
		rawRequest(t, h.proxyURL, "GET "+p+" HTTP/1.1\r\nHost: x\r\n\r\n")
		if rec.last().header.Get("X-Internal-Key") != "" {
			t.Errorf("rule fired for %s", p)
		}
	}
	rawRequest(t, h.proxyURL, "GET /public/x HTTP/1.1\r\nHost: x\r\n\r\n")
	if rec.last().header.Get("X-Internal-Key") != "k" {
		t.Error("rule should fire for /public/x")
	}
}

func TestUpstreamErrorReasonHasNoValues(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				bufio.NewReader(c).ReadString('\n')
				io.WriteString(c, "HTTP/1.1 200 OK\r\nSet-Cookie session=SECRET123\r\n\r\n")
				c.Close()
			}()
		}
	}()
	h := startProxy(t, shapeRule, "http://"+ln.Addr().String(), false)
	got := do(t, "GET", h.proxyURL+"/c/1", nil, nil)
	if got.status != 502 {
		t.Fatalf("status = %d", got.status)
	}
	h.audit.mu.Lock()
	log := h.audit.b.String()
	h.audit.mu.Unlock()
	if strings.Contains(log, "SECRET") || !strings.Contains(log, "upstream_error") {
		t.Fatalf("audit = %s", log)
	}
}

func TestPartlyAppliedRuleIsLoggedAsChanged(t *testing.T) {
	rec := &recorder{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { rec.add(r) }))
	defer up.Close()
	cfg := "rules:\n  - id: mix\n    when: {}\n    request:\n      set_headers: {X-A: \"1\"}\n      json: [{set: {path: a, value: 1}}]\n"
	h := startProxy(t, cfg, up.URL, false)
	do(t, "POST", h.proxyURL+"/", http.Header{"Content-Type": {"text/plain"}}, []byte("hi"))
	e := lastEvent(t, h)
	if e.Event != audit.Changed || e.Reason != "request body is not JSON" || len(e.Actions) != 1 || len(e.NotApplied) != 1 {
		t.Fatalf("audit = %+v", e)
	}
	if rec.last().header.Get("X-A") != "1" || string(rec.last().body) != "hi" {
		t.Fatal("header should be set and body left alone")
	}
}

func TestStalledBodyGives504(t *testing.T) {
	block := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"customer":`)
		w.(http.Flusher).Flush()
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer up.Close()
	defer close(block)
	cfg := "limits: {upstream_timeout: 300ms}\n" + strings.TrimPrefix(shapeRule, "\nlimits: {max_body: 4KiB}")
	h := startProxy(t, cfg, up.URL, false)
	start := time.Now()
	got := do(t, "GET", h.proxyURL+"/c/1", nil, nil)
	if got.status != 504 || time.Since(start) > 3*time.Second {
		t.Fatalf("status %d after %v", got.status, time.Since(start))
	}
	if e := lastEvent(t, h); e.Event != audit.UpstreamError || e.Status != 504 {
		t.Fatalf("audit = %+v", e)
	}
}

func TestChangedBodyDropsTrailersAndDigests(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Trailer", "X-Sum")
		w.Header().Set("Content-Digest", "sha-256=:abc=:")
		w.Header().Set("Repr-Digest", "sha-256=:abc=:")
		io.WriteString(w, `{"customer":{"full_name":"Ada"}}`)
		w.Header().Set("X-Sum", "123")
	}))
	defer up.Close()
	h := startProxy(t, shapeRule, up.URL, false)
	got := do(t, "GET", h.proxyURL+"/c/1", nil, nil)
	for _, k := range []string{"Trailer", "Content-Digest", "Repr-Digest", "X-Sum"} {
		if got.header.Get(k) != "" {
			t.Errorf("%s should be gone, got %q", k, got.header.Get(k))
		}
	}
	if string(got.body) != `{"customer":{},"name":"Ada"}` {
		t.Fatalf("body = %s", got.body)
	}
}

func TestGzipContentLengthIsCorrected(t *testing.T) {
	up := jsonUpstream(t, string(gz([]byte(`{"customer":{"full_name":"Ada"}}`))), http.Header{"Content-Encoding": {"gzip"}})
	h := startProxy(t, shapeRule, up.URL, false)
	got := do(t, "GET", h.proxyURL+"/c/1", nil, nil)
	if got.header.Get("Content-Length") != fmt.Sprint(len(got.body)) {
		t.Fatalf("content-length %q for %d bytes", got.header.Get("Content-Length"), len(got.body))
	}
}

func TestBodiesThatWouldNotRoundTripAreSkipped(t *testing.T) {
	for _, body := range []string{`{"a":1,"a":2}`, "{\"s\":\"\xff\"}", `{"s":"\ud800"}`} {
		up := jsonUpstream(t, body, nil)
		h := startProxy(t, "rules:\n  - id: s\n    when: {}\n    response: {json: [{set: {path: x, value: 1}}]}\n", up.URL, false)
		got := do(t, "GET", h.proxyURL+"/", nil, nil)
		if string(got.body) != body {
			t.Errorf("%q changed to %q", body, got.body)
		}
		if e := lastEvent(t, h); e.Event != audit.Skipped || !strings.Contains(e.Reason, "safely") {
			t.Errorf("%q: audit = %+v", body, e)
		}
	}
	// A valid surrogate pair and escaped text are fine.
	up := jsonUpstream(t, `{"s":"😀 é"}`, nil)
	h := startProxy(t, "rules:\n  - id: s\n    when: {}\n    response: {json: [{set: {path: x, value: 1}}]}\n", up.URL, false)
	if got := do(t, "GET", h.proxyURL+"/", nil, nil); string(got.body) != `{"s":"😀 é","x":1}` {
		t.Fatalf("body = %s", got.body)
	}
}

func TestNullBodyIsUnchangedNotSkipped(t *testing.T) {
	up := jsonUpstream(t, `null`, nil)
	h := startProxy(t, "rules:\n  - id: s\n    when: {}\n    response: {json: [{set: {path: x, value: 1}}]}\n", up.URL, false)
	if got := do(t, "GET", h.proxyURL+"/", nil, nil); string(got.body) != "null" {
		t.Fatalf("body = %s", got.body)
	}
	if e := lastEvent(t, h); e.Event != audit.Unchanged || len(e.NotApplied) != 1 {
		t.Fatalf("audit = %+v", e)
	}
}

func Test204AuditSaysNoBody(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(204)
	}))
	defer up.Close()
	h := startProxy(t, shapeRule, up.URL, false)
	do(t, "GET", h.proxyURL+"/c/1", nil, nil)
	if e := lastEvent(t, h); e.Event != audit.Skipped || e.Reason != "response has no body" {
		t.Fatalf("audit = %+v", e)
	}
}

// Found in the 0.2.0 review; both date from 0.1.0.

func TestDryRunNeverTurnsSlowAnswersInto504(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"customer":`)
		w.(http.Flusher).Flush()
		time.Sleep(600 * time.Millisecond)
		io.WriteString(w, `{"full_name":"Ada"}}`)
	}))
	defer up.Close()
	cfg := "limits: {upstream_timeout: 300ms}\n" + strings.TrimPrefix(shapeRule, "\nlimits: {max_body: 4KiB}")
	h := startProxy(t, cfg, up.URL, true)
	got := do(t, "GET", h.proxyURL+"/c/1", nil, nil)
	if got.status != 200 || string(got.body) != `{"customer":{"full_name":"Ada"}}` {
		t.Fatalf("dry run changed the answer: %d %s", got.status, got.body)
	}
}

func TestStalledRequestBodyGets408(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer up.Close()
	h := startProxy(t, "limits: {upstream_timeout: 300ms}\n"+strings.TrimPrefix(requestRules, "\n"), up.URL, false)
	conn, err := net.Dial("tcp", strings.TrimPrefix(h.proxyURL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(conn, "POST /orders HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"qty\":")
	start := time.Now()
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("no answer to a stalled body: %v", err)
	}
	// 0.7.1: after the 408 the proxy lets go of the connection at once,
	// instead of waiting on the stalled client for good.
	io.Copy(io.Discard, resp.Body)
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := br.ReadByte(); err != io.EOF {
		t.Errorf("the connection stayed open after the 408: %v", err)
	}
	if resp.StatusCode != 408 || time.Since(start) > 3*time.Second {
		t.Fatalf("status %d after %v", resp.StatusCode, time.Since(start))
	}
	if e := lastEvent(t, h); e.Event != audit.Skipped || !strings.Contains(e.Reason, "did not arrive") {
		t.Fatalf("audit = %+v", e)
	}
}
