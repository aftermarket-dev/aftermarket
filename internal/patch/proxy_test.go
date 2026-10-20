package patch

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aftermarket-dev/aftermarket/internal/audit"
	"github.com/aftermarket-dev/aftermarket/internal/rules"
)

// syncBuffer is a goroutine-safe audit sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) events(t *testing.T) []audit.Event {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []audit.Event
	for _, line := range strings.Split(strings.TrimSpace(s.b.String()), "\n") {
		if line == "" {
			continue
		}
		var e audit.Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("bad audit line %q: %v", line, err)
		}
		out = append(out, e)
	}
	return out
}

type harness struct {
	proxyURL string
	audit    *syncBuffer
}

func startProxy(t *testing.T, cfgYAML string, upstream string, dryRun bool) *harness {
	t.Helper()
	cfg, err := rules.Load([]byte(cfgYAML))
	if err != nil {
		t.Fatalf("rules: %v", err)
	}
	u, _ := url.Parse(upstream)
	sb := &syncBuffer{}
	px, err := New(Options{Upstream: u, Config: cfg, DryRun: dryRun, Audit: audit.New(sb), HealthPath: DefaultHealthPath})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(px)
	t.Cleanup(srv.Close)
	return &harness{proxyURL: srv.URL, audit: sb}
}

// rawClient never decompresses and never follows redirects, so tests see the
// exact bytes on the wire.
var rawClient = &http.Client{
	Transport:     &http.Transport{DisableCompression: true, MaxIdleConnsPerHost: 64},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Timeout:       20 * time.Second,
}

type captured struct {
	status int
	header http.Header
	body   []byte
}

func do(t *testing.T, method, u string, h http.Header, body []byte) captured {
	t.Helper()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, u, rd)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range h {
		req.Header[k] = v
	}
	resp, err := rawClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, u, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: read body: %v", method, u, err)
	}
	return captured{status: resp.StatusCode, header: resp.Header, body: b}
}

var ignoredHeaders = map[string]bool{"Date": true, "Connection": true, "Keep-Alive": true}

func headersEqual(a, b http.Header) (bool, string) {
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	for k := range keys {
		if ignoredHeaders[k] {
			continue
		}
		if strings.Join(a.Values(k), "\x00") != strings.Join(b.Values(k), "\x00") {
			return false, fmt.Sprintf("%s: %q vs %q", k, a.Values(k), b.Values(k))
		}
	}
	return true, ""
}

// recorder is an upstream that stores what it receives.
type recorder struct {
	mu   sync.Mutex
	reqs []recorded
}

type recorded struct {
	method, uri string
	header      http.Header
	body        []byte
	chunked     bool
}

func (r *recorder) add(req *http.Request) recorded {
	b, _ := io.ReadAll(req.Body)
	rec := recorded{method: req.Method, uri: req.URL.RequestURI(), header: req.Header.Clone(), body: b,
		chunked: len(req.TransferEncoding) > 0}
	r.mu.Lock()
	r.reqs = append(r.reqs, rec)
	r.mu.Unlock()
	return rec
}

func (r *recorder) last() recorded {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reqs[len(r.reqs)-1]
}

const neverMatches = `
rules:
  - id: other-path
    when: {path: /never/**}
    response: {set_headers: {X-Touched: "yes"}, json: [{set: {path: touched, value: true}}]}
  - id: needs-header
    when: {headers: {X-Not-Sent: "*"}}
    request: {set_headers: {X-Touched: "yes"}}
    response: {remove_headers: [Content-Type]}
`

// TestUnmatchedTrafficIsByteIdentical sends random traffic straight to the
// upstream and through the proxy, and requires the upstream to see the same
// request and the client to get the same response both ways.
func TestUnmatchedTrafficIsByteIdentical(t *testing.T) {
	seed := time.Now().UnixNano()
	if s := os.Getenv("AFTERMARKET_SEED"); s != "" {
		seed, _ = strconv.ParseInt(s, 10, 64)
	}
	t.Logf("seed %d (set AFTERMARKET_SEED to repeat)", seed)

	rec := &recorder{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := rec.add(r)
		// The response is a pure function of the request, so the direct and
		// proxied calls get the same answer.
		rs := rand.New(rand.NewSource(int64(len(got.body))*7919 + int64(len(got.uri))))
		statuses := []int{200, 201, 202, 204, 206, 301, 304, 400, 401, 404, 409, 418, 500, 502, 503}
		status, _ := strconv.Atoi(r.URL.Query().Get("status"))
		if status == 0 {
			status = statuses[rs.Intn(len(statuses))]
		}
		w.Header().Set("Content-Type", []string{"application/json", "application/octet-stream", "text/plain; charset=utf-8", "application/problem+json"}[rs.Intn(4)])
		w.Header().Add("X-Multi", "a")
		w.Header().Add("X-Multi", "b")
		w.Header().Set("X-Echo-Len", strconv.Itoa(len(got.body)))
		w.Header().Set("Etag", fmt.Sprintf(`"%x"`, rs.Int63()))
		if r.URL.Query().Get("gzip") == "1" {
			w.Header().Set("Content-Encoding", "gzip")
		}
		body := make([]byte, rs.Intn(40000))
		rs.Read(body)
		if r.URL.Query().Get("chunked") == "1" {
			w.WriteHeader(status)
			if status == 204 || status == 304 {
				return
			}
			for i := 0; i < len(body); i += 997 {
				end := i + 997
				if end > len(body) {
					end = len(body)
				}
				w.Write(body[i:end])
				w.(http.Flusher).Flush()
			}
			return
		}
		if status != 204 && status != 304 {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		}
		w.WriteHeader(status)
		if status != 204 && status != 304 && r.Method != http.MethodHead {
			w.Write(body)
		}
	}))
	defer up.Close()
	h := startProxy(t, neverMatches, up.URL, false)

	rs := rand.New(rand.NewSource(seed))
	methods := []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}
	segs := []string{"api", "v1", "customer", "a%20b", "%2Fslash", "x.y", "_", "~tilde", "über", "1"}
	const n = 400
	for i := 0; i < n; i++ {
		method := methods[rs.Intn(len(methods))]
		var path strings.Builder
		for j := 0; j <= rs.Intn(4); j++ {
			path.WriteString("/" + segs[rs.Intn(len(segs))])
		}
		q := url.Values{}
		for j := 0; j < rs.Intn(4); j++ {
			q.Add(fmt.Sprintf("k%d", rs.Intn(5)), fmt.Sprintf("v %d&=%d", rs.Intn(100), j))
		}
		if rs.Intn(5) == 0 {
			q.Set("gzip", "1")
		}
		if rs.Intn(4) == 0 {
			q.Set("chunked", "1")
		}
		uri := path.String()
		if len(q) > 0 {
			uri += "?" + q.Encode()
		}
		hdr := http.Header{}
		hdr.Set("User-Agent", "aftermarket-test")
		for j := 0; j < rs.Intn(5); j++ {
			hdr.Add(fmt.Sprintf("X-Rand-%d", rs.Intn(3)), fmt.Sprintf("value-%d", rs.Intn(1000)))
		}
		if rs.Intn(3) == 0 {
			hdr.Set("Content-Type", "application/json")
		}
		var body []byte
		if method != "GET" && method != "HEAD" && rs.Intn(4) > 0 {
			body = make([]byte, rs.Intn(70000))
			rs.Read(body)
		}

		direct := do(t, method, up.URL+uri, hdr, body)
		directReq := rec.last()
		proxied := do(t, method, h.proxyURL+uri, hdr, body)
		proxiedReq := rec.last()

		ctx := fmt.Sprintf("case %d: %s %s (body %d bytes)", i, method, uri, len(body))
		if directReq.method != proxiedReq.method || directReq.uri != proxiedReq.uri {
			t.Fatalf("%s: upstream saw %s %s, want %s %s", ctx, proxiedReq.method, proxiedReq.uri, directReq.method, directReq.uri)
		}
		if !bytes.Equal(directReq.body, proxiedReq.body) {
			t.Fatalf("%s: upstream request body differs", ctx)
		}
		dh, ph := directReq.header.Clone(), proxiedReq.header.Clone()
		// The proxy adds Accept-Encoding only if the client sent it; the
		// default Go client always sends it, so both sides match.
		if ok, diff := headersEqual(dh, ph); !ok {
			t.Fatalf("%s: upstream request headers differ: %s", ctx, diff)
		}
		if direct.status != proxied.status {
			t.Fatalf("%s: status %d vs %d", ctx, direct.status, proxied.status)
		}
		if !bytes.Equal(direct.body, proxied.body) {
			t.Fatalf("%s: response body differs (%d vs %d bytes)", ctx, len(direct.body), len(proxied.body))
		}
		if ok, diff := headersEqual(direct.header, proxied.header); !ok {
			t.Fatalf("%s: response headers differ: %s", ctx, diff)
		}
	}
	if ev := h.audit.events(t); len(ev) != 0 {
		t.Fatalf("unmatched traffic produced audit events: %+v", ev)
	}
}

const shapeRule = `
limits: {max_body: 4KiB}
rules:
  - id: shape
    when: {path: /c/*, content_type: application/json}
    response:
      json:
        - rename: {from: customer.full_name, to: name}
`

func jsonUpstream(t *testing.T, body string, header http.Header) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range header {
			w.Header()[k] = v
		}
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "application/json")
		}
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func lastEvent(t *testing.T, h *harness) audit.Event {
	t.Helper()
	ev := h.audit.events(t)
	if len(ev) == 0 {
		t.Fatal("no audit events")
	}
	return ev[len(ev)-1]
}

func TestMatchedResponseIsPatched(t *testing.T) {
	up := jsonUpstream(t, `{"customer":{"id":1,"full_name":"Ada"},"z":[1,2.50]}`, http.Header{"Etag": {`"abc"`}})
	h := startProxy(t, shapeRule, up.URL, false)
	got := do(t, "GET", h.proxyURL+"/c/1", nil, nil)
	if want := `{"customer":{"id":1},"z":[1,2.50],"name":"Ada"}`; string(got.body) != want {
		t.Fatalf("body = %s, want %s", got.body, want)
	}
	if got.header.Get("Content-Length") != strconv.Itoa(len(got.body)) {
		t.Fatalf("content-length %q for %d bytes", got.header.Get("Content-Length"), len(got.body))
	}
	if got.header.Get("Etag") != "" {
		t.Fatal("Etag of the old body must be removed")
	}
	e := lastEvent(t, h)
	if e.Event != audit.Changed || e.Rule != "shape" || e.Phase != "response" || len(e.Actions) != 1 {
		t.Fatalf("audit = %+v", e)
	}
}

func TestMalformedJSONPassesUnchanged(t *testing.T) {
	for _, body := range []string{`{"customer":`, `not json`, `{"a":1} trailing`, ``} {
		up := jsonUpstream(t, body, nil)
		h := startProxy(t, shapeRule, up.URL, false)
		got := do(t, "GET", h.proxyURL+"/c/1", nil, nil)
		if string(got.body) != body {
			t.Fatalf("body %q came back as %q", body, got.body)
		}
		if e := lastEvent(t, h); e.Event != audit.Skipped || !strings.Contains(e.Reason, "changed safely") {
			t.Fatalf("audit for %q = %+v", body, e)
		}
	}
}

func TestOversizeBodyPassesUnchanged(t *testing.T) {
	big := `{"customer":{"full_name":"` + strings.Repeat("x", 100000) + `"}}`
	up := jsonUpstream(t, big, nil)
	h := startProxy(t, shapeRule, up.URL, false)
	got := do(t, "GET", h.proxyURL+"/c/1", nil, nil)
	if string(got.body) != big {
		t.Fatalf("oversize body changed (%d bytes back, want %d)", len(got.body), len(big))
	}
	if e := lastEvent(t, h); e.Event != audit.Skipped || !strings.Contains(e.Reason, "larger than max_body") {
		t.Fatalf("audit = %+v", e)
	}
}

func gz(b []byte) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(b)
	zw.Close()
	return buf.Bytes()
}

func gunzip(t *testing.T, b []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestGzipResponseIsPatchedAndRecompressed(t *testing.T) {
	plain := []byte(`{"customer":{"full_name":"Ada"}}`)
	up := jsonUpstream(t, string(gz(plain)), http.Header{"Content-Encoding": {"gzip"}})
	h := startProxy(t, shapeRule, up.URL, false)
	got := do(t, "GET", h.proxyURL+"/c/1", http.Header{"Accept-Encoding": {"gzip"}}, nil)
	if got.header.Get("Content-Encoding") != "gzip" {
		t.Fatal("content encoding lost")
	}
	if out := gunzip(t, got.body); string(out) != `{"customer":{},"name":"Ada"}` {
		t.Fatalf("decoded body = %s", out)
	}
}

func TestUnsupportedEncodingPassesUnchanged(t *testing.T) {
	body := "\x8b\x01\x80not-really-brotli"
	up := jsonUpstream(t, body, http.Header{"Content-Encoding": {"br"}})
	h := startProxy(t, shapeRule, up.URL, false)
	got := do(t, "GET", h.proxyURL+"/c/1", nil, nil)
	if string(got.body) != body {
		t.Fatal("br body changed")
	}
	if e := lastEvent(t, h); e.Event != audit.Skipped || !strings.Contains(e.Reason, "br is not supported") {
		t.Fatalf("audit = %+v", e)
	}
}

func TestBrokenGzipPassesUnchanged(t *testing.T) {
	body := "\x1f\x8b broken"
	up := jsonUpstream(t, body, http.Header{"Content-Encoding": {"gzip"}})
	h := startProxy(t, shapeRule, up.URL, false)
	got := do(t, "GET", h.proxyURL+"/c/1", nil, nil)
	if string(got.body) != body {
		t.Fatal("broken gzip body changed")
	}
	if e := lastEvent(t, h); e.Event != audit.Skipped {
		t.Fatalf("audit = %+v", e)
	}
}

func TestChunkedResponseIsPatched(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		for _, part := range []string{`{"customer":`, `{"full_name":`, `"Ada"}}`} {
			io.WriteString(w, part)
			w.(http.Flusher).Flush()
		}
	}))
	defer up.Close()
	h := startProxy(t, shapeRule, up.URL, false)
	got := do(t, "GET", h.proxyURL+"/c/1", nil, nil)
	if string(got.body) != `{"customer":{},"name":"Ada"}` {
		t.Fatalf("body = %s", got.body)
	}
}

func TestUnmatchedResponseStreams(t *testing.T) {
	release := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, "first|")
		w.(http.Flusher).Flush()
		<-release
		io.WriteString(w, "second")
	}))
	defer up.Close()
	defer close(release)
	h := startProxy(t, shapeRule, up.URL, false)
	resp, err := rawClient.Get(h.proxyURL + "/other")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 6)
	done := make(chan error, 1)
	go func() { _, err := io.ReadFull(resp.Body, buf); done <- err }()
	select {
	case err := <-done:
		if err != nil || string(buf) != "first|" {
			t.Fatalf("got %q, %v", buf, err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the first chunk did not arrive before the upstream finished: unmatched traffic is being buffered")
	}
}

const requestRules = `
rules:
  - id: req
    when: {method: POST, path: /orders}
    request:
      set_headers: {X-Patched: "1"}
      remove_headers: [X-Legacy]
      json:
        - rename: {from: qty, to: quantity}
        - set: {path: source, value: legacy}
`

func TestRequestIsPatched(t *testing.T) {
	rec := &recorder{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { rec.add(r) }))
	defer up.Close()
	h := startProxy(t, requestRules, up.URL, false)
	hdr := http.Header{"Content-Type": {"application/json"}, "X-Legacy": {"1"}, "X-Keep": {"k"}}
	do(t, "POST", h.proxyURL+"/orders", hdr, []byte(`{"qty":2,"sku":"a"}`))
	got := rec.last()
	if string(got.body) != `{"quantity":2,"sku":"a","source":"legacy"}` {
		t.Fatalf("body = %s", got.body)
	}
	if got.header.Get("X-Patched") != "1" || got.header.Get("X-Legacy") != "" || got.header.Get("X-Keep") != "k" {
		t.Fatalf("headers = %v", got.header)
	}
	if got.header.Get("Content-Length") != strconv.Itoa(len(got.body)) && !got.chunked {
		t.Fatalf("content-length %q for %d bytes", got.header.Get("Content-Length"), len(got.body))
	}

	// gzip request bodies are decoded and re-encoded.
	hdr.Set("Content-Encoding", "gzip")
	do(t, "POST", h.proxyURL+"/orders", hdr, gz([]byte(`{"qty":3}`)))
	if out := gunzip(t, rec.last().body); string(out) != `{"quantity":3,"source":"legacy"}` {
		t.Fatalf("gzip body = %s", out)
	}
}

func TestMalformedRequestBodyPassesUnchanged(t *testing.T) {
	rec := &recorder{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { rec.add(r) }))
	defer up.Close()
	h := startProxy(t, requestRules, up.URL, false)
	for _, body := range []string{`{"qty":`, `[1,2`, `nope`} {
		do(t, "POST", h.proxyURL+"/orders", http.Header{"Content-Type": {"application/json"}}, []byte(body))
		if got := rec.last(); string(got.body) != body {
			t.Fatalf("request body %q arrived as %q", body, got.body)
		}
		// The header changes still apply; only the JSON part is skipped.
		if e := lastEvent(t, h); e.Event != audit.Changed || e.Phase != "request" || !strings.Contains(e.Reason, "changed safely") {
			t.Fatalf("audit = %+v", e)
		}
	}
	// A body that is not JSON at all is never read.
	do(t, "POST", h.proxyURL+"/orders", http.Header{"Content-Type": {"text/plain"}}, []byte(`{"qty":1}`))
	if got := rec.last(); string(got.body) != `{"qty":1}` {
		t.Fatalf("text body changed: %s", got.body)
	}
}

func TestRuleOrderIsDeterministic(t *testing.T) {
	cfg := `
rules:
  - id: a
    when: {}
    response: {json: [{set: {path: x, value: 1}}], set_headers: {X-Order: a}}
  - id: b
    when: {}
    response: {json: [{set: {path: x, value: 2}}], set_headers: {X-Order: b}}
  - id: c
    when: {}
    response: {json: [{copy: {from: x, to: y}}, {set: {path: n.m, value: [1, "2", true, null]}}]}
`
	up := jsonUpstream(t, `{"k":0}`, nil)
	h := startProxy(t, cfg, up.URL, false)
	want := `{"k":0,"x":2,"y":2,"n":{"m":[1,"2",true,null]}}`
	for i := 0; i < 200; i++ {
		got := do(t, "GET", h.proxyURL+"/", nil, nil)
		if string(got.body) != want || got.header.Get("X-Order") != "b" {
			t.Fatalf("run %d: body %s, X-Order %s", i, got.body, got.header.Get("X-Order"))
		}
	}
}

func TestDryRunChangesZeroBytes(t *testing.T) {
	cfg := `
rules:
  - id: path
    when: {path: /c/*}
    request:
      rewrite_path: {from: "^/c/(.*)$", to: "/v2/$1"}
      set_headers: {X-Patched: "1"}
      json: [{set: {path: added, value: 1}}]
  - id: shape
    when: {path: /c/*}
    response:
      set_headers: {X-Patched: "1"}
      remove_headers: [X-Api]
      json: [{delete: {path: secret}}]
`
	rec := &recorder{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Api", "2")
		io.WriteString(w, `{"secret":"s","ok":true}`)
	}))
	defer up.Close()
	h := startProxy(t, cfg, up.URL, true)
	reqBody := []byte(`{"a":1}`)
	hdr := http.Header{"Content-Type": {"application/json"}}

	direct := do(t, "POST", up.URL+"/c/9", hdr, reqBody)
	directReq := rec.last()
	proxied := do(t, "POST", h.proxyURL+"/c/9", hdr, reqBody)
	proxiedReq := rec.last()

	if proxiedReq.uri != directReq.uri || !bytes.Equal(proxiedReq.body, directReq.body) {
		t.Fatalf("dry run changed the request: %s %q", proxiedReq.uri, proxiedReq.body)
	}
	if ok, diff := headersEqual(directReq.header, proxiedReq.header); !ok {
		t.Fatalf("dry run changed request headers: %s", diff)
	}
	if !bytes.Equal(direct.body, proxied.body) || direct.status != proxied.status {
		t.Fatalf("dry run changed the response: %s", proxied.body)
	}
	if ok, diff := headersEqual(direct.header, proxied.header); !ok {
		t.Fatalf("dry run changed response headers: %s", diff)
	}
	ev := h.audit.events(t)
	if len(ev) != 2 || ev[0].Event != audit.WouldChange || ev[1].Event != audit.WouldChange {
		t.Fatalf("dry-run audit = %+v", ev)
	}
	if len(ev[0].Actions) != 3 || len(ev[1].Actions) != 3 {
		t.Fatalf("dry run should report every change it would make: %+v", ev)
	}
}

func TestUpstreamDownGives502(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close() // nothing listens here now
	h := startProxy(t, shapeRule, "http://"+addr, false)
	got := do(t, "GET", h.proxyURL+"/c/1", nil, nil)
	if got.status != http.StatusBadGateway || got.header.Get("X-Aftermarket-Error") != "502" {
		t.Fatalf("status = %d", got.status)
	}
	if e := lastEvent(t, h); e.Event != audit.UpstreamError || e.Status != 502 {
		t.Fatalf("audit = %+v", e)
	}
}

func TestSlowUpstreamGives504(t *testing.T) {
	block := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer up.Close()
	defer close(block)
	h := startProxy(t, "limits: {upstream_timeout: 300ms}\n"+strings.TrimPrefix(shapeRule, "\nlimits: {max_body: 4KiB}"), up.URL, false)
	start := time.Now()
	got := do(t, "GET", h.proxyURL+"/c/1", nil, nil)
	took := time.Since(start)
	if got.status != http.StatusGatewayTimeout {
		t.Fatalf("status = %d", got.status)
	}
	if took > 3*time.Second {
		t.Fatalf("timeout took %v", took)
	}
	if e := lastEvent(t, h); e.Event != audit.UpstreamError || e.Status != 504 {
		t.Fatalf("audit = %+v", e)
	}
}

func TestHealthPath(t *testing.T) {
	up := jsonUpstream(t, `{}`, nil)
	h := startProxy(t, shapeRule, up.URL, false)
	got := do(t, "GET", h.proxyURL+DefaultHealthPath, nil, nil)
	if got.status != 200 || string(got.body) != "ok\n" {
		t.Fatalf("health = %d %q", got.status, got.body)
	}
	// /healthz is not taken from the upstream.
	if got := do(t, "GET", h.proxyURL+"/healthz", nil, nil); string(got.body) != `{}` {
		t.Fatalf("/healthz should reach the upstream, got %q", got.body)
	}
}

func TestNoBodyResponsesAreSkipped(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/c/204" {
			w.WriteHeader(204)
		}
	}))
	defer up.Close()
	h := startProxy(t, shapeRule, up.URL, false)
	if got := do(t, "HEAD", h.proxyURL+"/c/1", nil, nil); got.status != 200 {
		t.Fatalf("HEAD = %d", got.status)
	}
	if e := lastEvent(t, h); e.Reason != "response has no body" {
		t.Fatalf("audit = %+v", e)
	}
	if got := do(t, "GET", h.proxyURL+"/c/204", nil, nil); got.status != 204 || len(got.body) != 0 {
		t.Fatalf("204 = %d %q", got.status, got.body)
	}
}

func TestRewriteNeedsItsQueryParameter(t *testing.T) {
	cfg := `
rules:
  - id: rw
    when: {path: /api/v1/customer}
    request:
      rewrite_path: {from: "^/api/v1/customer$", to: "/api/v2/customers/{query.id}"}
`
	rec := &recorder{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { rec.add(r) }))
	defer up.Close()
	h := startProxy(t, cfg, up.URL, false)
	do(t, "GET", h.proxyURL+"/api/v1/customer?id=a%20b", nil, nil)
	if got := rec.last().uri; got != "/api/v2/customers/a%20b?id=a%20b" {
		t.Fatalf("uri = %s", got)
	}
	// A value with a slash would add path segments, so it is refused.
	do(t, "GET", h.proxyURL+"/api/v1/customer?id=a%2Fb", nil, nil)
	if got := rec.last().uri; got != "/api/v1/customer?id=a%2Fb" {
		t.Fatalf("uri = %s", got)
	}
	do(t, "GET", h.proxyURL+"/api/v1/customer", nil, nil)
	if got := rec.last().uri; got != "/api/v1/customer" {
		t.Fatalf("without id the path must stay, got %s", got)
	}
	if e := lastEvent(t, h); e.Event != audit.Unchanged || len(e.NotApplied) != 1 {
		t.Fatalf("audit = %+v", e)
	}
}

func TestUpstreamBasePath(t *testing.T) {
	rec := &recorder{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { rec.add(r) }))
	defer up.Close()
	cfg := "rules:\n  - id: rw\n    when: {}\n    request: {rewrite_path: {from: \"^/old$\", to: /new}}\n"
	h := startProxy(t, cfg, up.URL+"/base/", false)
	do(t, "GET", h.proxyURL+"/old?x=1", nil, nil)
	if got := rec.last().uri; got != "/base/new?x=1" {
		t.Fatalf("uri = %s", got)
	}
	do(t, "GET", h.proxyURL+"/other", nil, nil)
	if got := rec.last().uri; got != "/base/other" {
		t.Fatalf("uri = %s", got)
	}
}

// TestConcurrencySoak mixes patched and untouched traffic from many clients
// and checks every answer.
func TestConcurrencySoak(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"customer":{"full_name":"n%s"},"path":%q}`, r.URL.Query().Get("i"), r.URL.Path)
	}))
	defer up.Close()
	h := startProxy(t, shapeRule, up.URL, false)
	workers, per := 32, 150
	if testing.Short() {
		workers, per = 8, 25
	}
	var wg sync.WaitGroup
	errs := make(chan string, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < per; i++ {
				id := strconv.Itoa(w*per + i)
				path := "/c/" + id
				want := `{"customer":{},"path":"` + path + `","name":"n` + id + `"}`
				if i%2 == 1 {
					path = "/x/" + id
					want = `{"customer":{"full_name":"n` + id + `"},"path":"` + path + `"}`
				}
				resp, err := rawClient.Get(h.proxyURL + path + "?i=" + id)
				if err != nil {
					errs <- err.Error()
					return
				}
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if string(b) != want {
					errs <- fmt.Sprintf("%s: got %s want %s", path, b, want)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	ev := h.audit.events(t)
	if len(ev) != workers*per/2 {
		t.Fatalf("expected %d audit events, got %d", workers*per/2, len(ev))
	}
	ids := map[uint64]bool{}
	for _, e := range ev {
		ids[e.Request] = true
	}
	if len(ids) != len(ev) {
		t.Fatal("request ids are not unique")
	}
}

func TestAuditNeverContainsValues(t *testing.T) {
	cfg := `
rules:
  - id: s
    when: {}
    request: {set_headers: {Authorization: "Bearer TOPSECRET-1"}}
    response:
      set_headers: {X-Token: TOPSECRET-2}
      json: [{set: {path: password, value: TOPSECRET-3}}]
`
	up := jsonUpstream(t, `{"user":"TOPSECRET-4"}`, nil)
	h := startProxy(t, cfg, up.URL, false)
	do(t, "GET", h.proxyURL+"/p?token=TOPSECRET-5", http.Header{"Cookie": {"TOPSECRET-6"}}, nil)
	h.audit.mu.Lock()
	log := h.audit.b.String()
	h.audit.mu.Unlock()
	if strings.Contains(log, "TOPSECRET") {
		t.Fatalf("audit log leaked a value:\n%s", log)
	}
	if !strings.Contains(log, `"query_keys":["token"]`) {
		t.Fatalf("query keys should be logged:\n%s", log)
	}
}

// TestUpgradePassesThrough checks a connection upgrade (as WebSocket uses)
// is relayed in both directions.
func TestUpgradePassesThrough(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "echo" {
			http.Error(w, "no upgrade", 400)
			return
		}
		conn, brw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
		brw.Flush()
		line, _ := brw.ReadString('\n')
		brw.WriteString("echo:" + line)
		brw.Flush()
	}))
	defer up.Close()
	h := startProxy(t, shapeRule, up.URL, false)
	conn, err := net.Dial("tcp", strings.TrimPrefix(h.proxyURL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(conn, "GET /c/1 HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n")
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 101 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	fmt.Fprintf(conn, "hello\n")
	if got, _ := br.ReadString('\n'); got != "echo:hello\n" {
		t.Fatalf("got %q", got)
	}
}

func TestNewValidatesUpstream(t *testing.T) {
	cfg, _ := rules.Load([]byte(shapeRule))
	for _, bad := range []string{"", "localhost:9000", "ftp://x", "/path"} {
		u, _ := url.Parse(bad)
		if _, err := New(Options{Upstream: u, Config: cfg}); err == nil {
			t.Errorf("upstream %q should be rejected", bad)
		}
	}
}
