package adapt_test

import (
	"bytes"
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

	customerapi "github.com/aftermarket-dev/aftermarket/examples/customer-api"
	"github.com/aftermarket-dev/aftermarket/internal/adapt"
	"github.com/aftermarket-dev/aftermarket/internal/audit"
	tk "github.com/aftermarket-dev/aftermarket/internal/testkit"
)

type harness struct {
	url   string
	audit *tk.AuditSink
}

func start(t *testing.T, cfgYAML, upstream string, dryRun bool) *harness {
	t.Helper()
	cfg, err := adapt.Load([]byte(cfgYAML))
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	u, _ := url.Parse(upstream)
	sink := &tk.AuditSink{}
	px, err := adapt.New(adapt.Options{Upstream: u, Config: cfg, DryRun: dryRun, Audit: audit.New(sink), HealthPath: adapt.DefaultHealthPath})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(px)
	t.Cleanup(srv.Close)
	return &harness{url: srv.URL, audit: sink}
}

func exampleConfig(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../examples/customer-api/adapt.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestUnmatchedTrafficIsByteIdentical sends random requests that no mapping
// covers, including near misses (a v1 path with the wrong method or without
// its query parameter), straight to a test server and through adapt. The
// server must see the same request and the client the same response.
func TestUnmatchedTrafficIsByteIdentical(t *testing.T) {
	seed := time.Now().UnixNano()
	if s := os.Getenv("AFTERMARKET_SEED"); s != "" {
		seed, _ = strconv.ParseInt(s, 10, 64)
	}
	t.Logf("seed %d (set AFTERMARKET_SEED to repeat)", seed)
	rec := &tk.Recorder{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := rec.Add(r)
		rs := rand.New(rand.NewSource(int64(len(got.Body))*7919 + int64(len(got.URI))))
		statuses := []int{200, 201, 204, 304, 400, 404, 409, 422, 500, 503}
		status := statuses[rs.Intn(len(statuses))]
		w.Header().Set("Content-Type", []string{"application/json", "text/plain", "application/octet-stream"}[rs.Intn(3)])
		w.Header().Add("X-Multi", "a")
		w.Header().Add("X-Multi", "b")
		w.Header().Set("X-Api-Version", "2")
		body := make([]byte, rs.Intn(30000))
		rs.Read(body)
		if status != 204 && status != 304 {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		}
		w.WriteHeader(status)
		if status != 204 && status != 304 && r.Method != http.MethodHead {
			w.Write(body)
		}
	}))
	defer up.Close()
	h := start(t, exampleConfig(t), up.URL, false)

	rs := rand.New(rand.NewSource(seed))
	nearMisses := []struct{ method, path string }{
		{"POST", "/api/v1/customer?id=7"},
		{"GET", "/api/v1/customer"},
		{"GET", "/api/v1/customer?id="},
		{"PUT", "/api/v1/customers"},
		{"GET", "/api/v1/customers/"},
		{"GET", "/api/v1/customers/search"},
		{"GET", "/api/v1/customer/7"},
		{"HEAD", "/api/v1/customer?id=7"},
		{"GET", "/API/v1/customer?id=7"},
	}
	segs := []string{"api", "v2", "customers", "a%20b", "x.y", "über", "1", "_"}
	methods := []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}
	for i := 0; i < 400; i++ {
		var method, uri string
		if rs.Intn(3) == 0 {
			nm := nearMisses[rs.Intn(len(nearMisses))]
			method, uri = nm.method, nm.path
		} else {
			method = methods[rs.Intn(len(methods))]
			var p strings.Builder
			for j := 0; j <= rs.Intn(4); j++ {
				p.WriteString("/" + segs[rs.Intn(len(segs))])
			}
			uri = p.String()
			if rs.Intn(2) == 0 {
				uri += fmt.Sprintf("?k%d=%d&q=x%%20y", rs.Intn(3), rs.Intn(100))
			}
		}
		hdr := http.Header{"User-Agent": {"aftermarket-test"}, "X-Rand": {strconv.Itoa(rs.Intn(1000))}}
		var body []byte
		if method != "GET" && method != "HEAD" && rs.Intn(2) == 0 {
			hdr.Set("Content-Type", "application/json")
			body = make([]byte, rs.Intn(40000))
			rs.Read(body)
		}
		direct := tk.Do(t, method, up.URL+uri, hdr, body)
		dReq := rec.Last()
		proxied := tk.Do(t, method, h.url+uri, hdr, body)
		pReq := rec.Last()
		ctx := fmt.Sprintf("case %d: %s %s", i, method, uri)
		if dReq.Method != pReq.Method || dReq.URI != pReq.URI || !bytes.Equal(dReq.Body, pReq.Body) {
			t.Fatalf("%s: upstream saw %s %s (%d bytes), want %s %s (%d bytes)", ctx, pReq.Method, pReq.URI, len(pReq.Body), dReq.Method, dReq.URI, len(dReq.Body))
		}
		if ok, diff := tk.HeadersEqual(dReq.Header, pReq.Header); !ok {
			t.Fatalf("%s: request headers differ: %s", ctx, diff)
		}
		if direct.Status != proxied.Status || !bytes.Equal(direct.Body, proxied.Body) {
			t.Fatalf("%s: response differs: %d vs %d", ctx, direct.Status, proxied.Status)
		}
		if ok, diff := tk.HeadersEqual(direct.Header, proxied.Header); !ok {
			t.Fatalf("%s: response headers differ: %s", ctx, diff)
		}
	}
	if ev := h.audit.Events(t); len(ev) != 0 {
		t.Fatalf("unmatched traffic produced audit events: %+v", ev[0])
	}
}

func recordingUpstream(t *testing.T, status int, ct, body string) (*tk.Recorder, string) {
	t.Helper()
	rec := &tk.Recorder{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.Add(r)
		if ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(up.Close)
	return rec, up.URL
}

func TestQueryStringsKeepTheirBytes(t *testing.T) {
	rec, up := recordingUpstream(t, 200, "application/json", `{"data":[]}`)
	h := start(t, exampleConfig(t), up, false)
	cases := map[string]string{
		"/api/v1/customers?limit=2;x=1":             "/api/v2/customers?limit=2;x=1",
		"/api/v1/customers?a=100%&b":                "/api/v2/customers?a=100%&b",
		"/api/v1/customers/search?q=a%20b&page=%zz": "/api/v2/customers?search=a+b&page=%zz",
		"/api/v1/customers/search?x=1&q=ada":        "/api/v2/customers?search=ada&x=1",
		"/nothing?a=1;b=2":                          "/nothing?a=1;b=2",
	}
	for in, want := range cases {
		tk.Raw(t, h.url, "GET "+in+" HTTP/1.1\r\nHost: x\r\n\r\n")
		if got := rec.Last().URI; got != want {
			t.Errorf("%s reached the upstream as %s, want %s", in, got, want)
		}
	}
}

func TestUnsafeValuesNeverReachThePath(t *testing.T) {
	rec, up := recordingUpstream(t, 200, "application/json", `{"customer":{}}`)
	h := start(t, exampleConfig(t), up, false)
	for _, uri := range []string{
		"/api/v1/customer?id=..",
		"/api/v1/customer?id=%2e%2e",
		"/api/v1/customer?id=.",
		"/api/v1/customer?id=a%2Fb",
		"/api/v1/customer?id=a%5Cb",
	} {
		tk.Raw(t, h.url, "GET "+uri+" HTTP/1.1\r\nHost: x\r\n\r\n")
		if got := rec.Last().URI; got != uri {
			t.Errorf("%s reached the upstream as %s; it must pass through untouched", uri, got)
		}
		if e := h.audit.Last(t); e.Event != audit.Skipped || !strings.Contains(e.Reason, "safe path segment") {
			t.Errorf("%s: audit = %+v", uri, e)
		}
	}
	tk.Raw(t, h.url, "GET /api/v1/customer?id=a%20b HTTP/1.1\r\nHost: x\r\n\r\n")
	if got := rec.Last().URI; got != "/api/v2/customers/a%20b" {
		t.Errorf("a space should be escaped in the path, got %s", got)
	}
}

func TestDotSegmentPathsPassThrough(t *testing.T) {
	rec, up := recordingUpstream(t, 200, "application/json", `{"data":[]}`)
	h := start(t, "endpoints:\n  - {id: o, old: \"GET /v1/{id}/orders\", new: \"GET /v2/orders/{id}\"}\n", up, false)
	for _, p := range []string{"/v1/../orders", "/v1/./orders", "/v1/a%2Fb/orders"} {
		tk.Raw(t, h.url, "GET "+p+" HTTP/1.1\r\nHost: x\r\n\r\n")
		if got := rec.Last().URI; got != p {
			t.Errorf("%s reached the upstream as %s", p, got)
		}
	}
	tk.Raw(t, h.url, "GET /v1/7/orders HTTP/1.1\r\nHost: x\r\n\r\n")
	if got := rec.Last().URI; got != "/v2/orders/7" {
		t.Errorf("got %s", got)
	}
}

func TestRequestThatCannotBeTranslatedPassesUnchanged(t *testing.T) {
	rec, up := recordingUpstream(t, 201, "application/json", `{"customer":{"id":1}}`)
	cfg := "limits: {max_body: 1KiB}\n" + exampleConfig(t)
	h := start(t, cfg, up, false)
	cases := []struct {
		name   string
		header http.Header
		body   []byte
		reason string
	}{
		{"not JSON", http.Header{"Content-Type": {"text/plain"}}, []byte(`{"name":"a"}`), "not JSON"},
		{"broken JSON", http.Header{"Content-Type": {"application/json"}}, []byte(`{"name":`), "changed safely"},
		{"duplicate keys", http.Header{"Content-Type": {"application/json"}}, []byte(`{"name":"a","name":"b"}`), "changed safely"},
		{"too large", http.Header{"Content-Type": {"application/json"}}, []byte(`{"name":"` + strings.Repeat("x", 5000) + `"}`), "larger than max_body"},
		{"broken gzip", http.Header{"Content-Type": {"application/json"}, "Content-Encoding": {"gzip"}}, []byte("\x1f\x8b nope"), "gzip"},
	}
	for _, c := range cases {
		got := tk.Do(t, "POST", h.url+"/api/v1/customers", c.header, c.body)
		r := rec.Last()
		if r.URI != "/api/v1/customers" || !bytes.Equal(r.Body, c.body) {
			t.Errorf("%s: upstream got %s with %d bytes; the request must pass through untouched", c.name, r.URI, len(r.Body))
		}
		if got.Status != 201 || string(got.Body) != `{"customer":{"id":1}}` {
			t.Errorf("%s: the response must not be translated either, got %d %s", c.name, got.Status, got.Body)
		}
		e := h.audit.Events(t)
		if len(e) == 0 || e[len(e)-1].Event != audit.Skipped || !strings.Contains(e[len(e)-1].Reason, c.reason) {
			t.Errorf("%s: audit = %+v", c.name, e)
		}
	}
	tk.Raw(t, h.url, "GET /api/v1/customer?id=7&id=8 HTTP/1.1\r\nHost: x\r\n\r\n")
	if got := rec.Last().URI; got != "/api/v1/customer?id=7&id=8" {
		t.Errorf("a repeated parameter must pass through, got %s", got)
	}
}

func TestResponseIsAllOrNothing(t *testing.T) {
	for _, c := range []struct {
		name, ct, body, reason string
	}{
		{"not JSON", "text/html", "<p>created</p>", "not JSON"},
		{"broken JSON", "application/json", `{"customer":`, "changed safely"},
	} {
		_, up := recordingUpstream(t, 201, c.ct, c.body)
		h := start(t, exampleConfig(t), up, false)
		got := tk.Do(t, "POST", h.url+"/api/v1/customers", http.Header{"Content-Type": {"application/json"}}, []byte(`{"name":"a","email":"b"}`))
		if got.Status != 201 || string(got.Body) != c.body {
			t.Errorf("%s: got %d %s; status and body must stay as sent", c.name, got.Status, got.Body)
		}
		if e := h.audit.Last(t); e.Event != audit.Skipped || e.Phase != "response" || !strings.Contains(e.Reason, c.reason) {
			t.Errorf("%s: audit = %+v", c.name, e)
		}
	}
	// A list that isn't there can't be rebuilt either.
	_, up := recordingUpstream(t, 200, "application/json", `{"items":[]}`)
	h := start(t, exampleConfig(t), up, false)
	if got := tk.Do(t, "GET", h.url+"/api/v1/customers", nil, nil); string(got.Body) != `{"items":[]}` {
		t.Errorf("got %s", got.Body)
	}
	if e := h.audit.Last(t); e.Event != audit.Skipped || !strings.Contains(e.Reason, "data not found") {
		t.Errorf("audit = %+v", e)
	}
}

func TestMissingFieldsAreLeftOutAndLogged(t *testing.T) {
	_, up := recordingUpstream(t, 200, "application/json", `{"customer":{"id":7,"full_name":"Ada"}}`)
	h := start(t, exampleConfig(t), up, false)
	got := tk.Do(t, "GET", h.url+"/api/v1/customer?id=7", nil, nil)
	if string(got.Body) != `{"customer_id":7,"name":"Ada"}` {
		t.Fatalf("got %s", got.Body)
	}
	e := h.audit.Last(t)
	if e.Event != audit.Changed || len(e.NotApplied) == 0 || e.NotApplied[0] != "customer.email not found" {
		t.Fatalf("audit = %+v", e)
	}
}

func TestDryRunChangesZeroBytes(t *testing.T) {
	rec := &tk.Recorder{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.Add(r)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Api-Version", "2")
		w.WriteHeader(201)
		io.WriteString(w, `{"customer":{"id":1,"full_name":"a","email":"b"}}`)
	}))
	defer up.Close()
	h := start(t, exampleConfig(t), up.URL, true)
	body := []byte(`{"name":"a","email":"b"}`)
	hdr := http.Header{"Content-Type": {"application/json"}}
	for _, c := range []struct{ method, path string }{
		{"POST", "/api/v1/customers"},
		{"GET", "/api/v1/customer?id=7"},
		{"DELETE", "/api/v1/customer?id=7"},
		{"GET", "/api/v1/customers/search?q=a&x=1"},
	} {
		var b []byte
		if c.method == "POST" {
			b = body
		}
		direct := tk.Do(t, c.method, up.URL+c.path, hdr, b)
		dReq := rec.Last()
		proxied := tk.Do(t, c.method, h.url+c.path, hdr, b)
		pReq := rec.Last()
		if dReq.Method != pReq.Method || dReq.URI != pReq.URI || !bytes.Equal(dReq.Body, pReq.Body) {
			t.Fatalf("%s %s: dry run changed the request to %s %s", c.method, c.path, pReq.Method, pReq.URI)
		}
		if ok, diff := tk.HeadersEqual(dReq.Header, pReq.Header); !ok {
			t.Fatalf("dry run changed request headers: %s", diff)
		}
		if direct.Status != proxied.Status || !bytes.Equal(direct.Body, proxied.Body) {
			t.Fatalf("dry run changed the response")
		}
		if ok, diff := tk.HeadersEqual(direct.Header, proxied.Header); !ok {
			t.Fatalf("dry run changed response headers: %s", diff)
		}
	}
	would := 0
	for _, e := range h.audit.Events(t) {
		if e.Event == audit.WouldChange {
			would++
		}
	}
	if would < 6 {
		t.Fatalf("dry run should report what it would change, got %d events: %s", would, h.audit.String())
	}
}

func TestUpstreamDownGives502(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	h := start(t, exampleConfig(t), "http://"+addr, false)
	got := tk.Do(t, "GET", h.url+"/api/v1/customer?id=7", nil, nil)
	if got.Status != 502 {
		t.Fatalf("status = %d", got.Status)
	}
	if e := h.audit.Last(t); e.Event != audit.UpstreamError || e.Rule != "get-customer" || e.Status != 502 {
		t.Fatalf("audit = %+v", e)
	}
	if got := tk.Do(t, "GET", h.url+"/elsewhere", nil, nil); got.Status != 502 {
		t.Fatalf("unmatched status = %d", got.Status)
	}
}

func TestSlowUpstreamAndStalledBodyGive504(t *testing.T) {
	block := make(chan struct{})
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/customers/1" {
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"customer":`)
			w.(http.Flusher).Flush()
		}
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer up.Close()
	defer close(block)
	h := start(t, "limits: {upstream_timeout: 300ms}\n"+exampleConfig(t), up.URL, false)
	for _, p := range []string{"/api/v1/customer?id=2", "/api/v1/customer?id=1"} {
		start := time.Now()
		got := tk.Do(t, "GET", h.url+p, nil, nil)
		if got.Status != 504 || time.Since(start) > 3*time.Second {
			t.Fatalf("%s: %d after %v", p, got.Status, time.Since(start))
		}
		if e := h.audit.Last(t); e.Event != audit.UpstreamError || e.Status != 504 {
			t.Fatalf("%s: audit = %+v", p, e)
		}
	}
}

func TestAuditNeverContainsValues(t *testing.T) {
	_, up := recordingUpstream(t, 201, "application/json", `{"customer":{"id":1,"full_name":"SECRET-1","email":"SECRET-2"}}`)
	cfg := exampleConfig(t) + `
  - id: token
    old: GET /tok/{token}?key={key}
    new: GET /v2/tok/{token}?k={key}
`
	h := start(t, cfg, up, false)
	tk.Do(t, "POST", h.url+"/api/v1/customers", http.Header{"Content-Type": {"application/json"}, "Authorization": {"Bearer SECRET-3"}}, []byte(`{"name":"SECRET-4","email":"SECRET-5"}`))
	tk.Do(t, "GET", h.url+"/tok/SECRET-6?key=SECRET-7", nil, nil)
	tk.Do(t, "GET", h.url+"/api/v1/customer?id=SECRET-8", nil, nil)
	if log := h.audit.String(); strings.Contains(log, "SECRET") || !strings.Contains(log, `"path":"/tok/{token}"`) {
		t.Fatalf("audit log leaked a value or missed an event:\n%s", log)
	}
}

func TestGzipResponseIsRebuiltAndRecompressed(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Etag", `"v2"`)
		w.Write(tk.Gzip([]byte(`{"customer":{"id":7,"full_name":"Ada","email":"a@x"}}`)))
	}))
	defer up.Close()
	h := start(t, exampleConfig(t), up.URL, false)
	got := tk.Do(t, "GET", h.url+"/api/v1/customer?id=7", nil, nil)
	if got.Header.Get("Content-Encoding") != "gzip" || got.Header.Get("Etag") != "" || got.Header.Get("Content-Length") != strconv.Itoa(len(got.Body)) {
		t.Fatalf("headers = %v", got.Header)
	}
	if out := tk.Gunzip(t, got.Body); string(out) != `{"customer_id":7,"name":"Ada","email":"a@x"}` {
		t.Fatalf("body = %s", out)
	}
}

func TestHeadersRenameAndCredentialsPassUntouched(t *testing.T) {
	rec := &tk.Recorder{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.Add(r)
		w.Header().Set("X-New-Id", "7")
		w.Header().Set("Set-Cookie", "s=1")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{}`)
	}))
	defer up.Close()
	cfg := `
endpoints:
  - id: h
    old: GET /v1/thing
    new: GET /v2/thing
    request: {headers: {rename: {X-Old-Trace: X-Trace}, set: {X-Client: legacy}, remove: [X-Debug]}}
    response: {headers: {rename: {X-New-Id: X-Old-Id}}}
`
	h := start(t, cfg, up.URL, false)
	got := tk.Do(t, "GET", h.url+"/v1/thing", http.Header{"X-Old-Trace": {"t1", "t2"}, "X-Debug": {"1"}, "Authorization": {"Bearer z"}, "Cookie": {"c=1"}}, nil)
	r := rec.Last().Header
	if strings.Join(r.Values("X-Trace"), ",") != "t1,t2" || r.Get("X-Old-Trace") != "" || r.Get("X-Client") != "legacy" || r.Get("X-Debug") != "" {
		t.Fatalf("request headers = %v", r)
	}
	if r.Get("Authorization") != "Bearer z" || r.Get("Cookie") != "c=1" {
		t.Fatal("credentials must pass through as sent")
	}
	if got.Header.Get("X-Old-Id") != "7" || got.Header.Get("X-New-Id") != "" || got.Header.Get("Set-Cookie") != "s=1" {
		t.Fatalf("response headers = %v", got.Header)
	}
}

func TestMethodChangeAndBodyless(t *testing.T) {
	rec := &tk.Recorder{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.Add(r)
		if r.URL.Path == "/v2/empty" {
			w.WriteHeader(204)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"results":[1,2]}`)
	}))
	defer up.Close()
	cfg := `
endpoints:
  - id: search
    old: POST /v1/search
    new: GET /v2/search
  - id: create
    old: GET /v1/make?name={name}
    new: POST /v2/make
    request: {body: {name: $params.name, kind: legacy}}
  - id: empty
    old: GET /v1/empty
    new: GET /v2/empty
    response: {status: {204: 200}}
  - id: literal-on-204
    old: GET /v1/empty2
    new: GET /v2/empty
    response: {body: {ok: true}}
`
	h := start(t, cfg, up.URL, false)
	tk.Do(t, "POST", h.url+"/v1/search", http.Header{"Content-Type": {"application/json"}}, []byte(`{"q":"x"}`))
	if r := rec.Last(); r.Method != "GET" || len(r.Body) != 0 || r.Header.Get("Content-Type") != "" {
		t.Fatalf("POST to GET: %s with %d bytes, %v", r.Method, len(r.Body), r.Header)
	}
	tk.Do(t, "GET", h.url+"/v1/make?name=Zo%C3%AB", nil, nil)
	if r := rec.Last(); r.Method != "POST" || string(r.Body) != `{"name":"Zoë","kind":"legacy"}` || r.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("GET to POST: %s %s %v", r.Method, r.Body, r.Header)
	}
	got := tk.Do(t, "GET", h.url+"/v1/empty", nil, nil)
	if got.Status != 200 || len(got.Body) != 0 || got.Header.Get("Content-Length") != "0" {
		t.Fatalf("204 to 200: %d %q %v", got.Status, got.Body, got.Header)
	}
	got = tk.Do(t, "GET", h.url+"/v1/empty2", nil, nil)
	if got.Status != 204 || len(got.Body) != 0 {
		t.Fatalf("a body can't be put on a 204: %d %q", got.Status, got.Body)
	}
	if e := h.audit.Last(t); e.Event != audit.Skipped || e.Reason != "response has no body" {
		t.Fatalf("audit = %+v", e)
	}
}

func TestHealthPath(t *testing.T) {
	_, up := recordingUpstream(t, 200, "text/plain", "upstream")
	h := start(t, exampleConfig(t), up, false)
	if got := tk.Do(t, "GET", h.url+adapt.DefaultHealthPath, nil, nil); got.Status != 200 || string(got.Body) != "ok\n" {
		t.Fatalf("health = %d %q", got.Status, got.Body)
	}
}

// TestConcurrencySoak runs the old client's whole suite many times at once,
// each against its own v2 service behind one proxy per run, while other
// clients hammer a shared proxy with lookups.
func TestConcurrencySoak(t *testing.T) {
	v2 := httptest.NewServer(customerapi.NewV2())
	defer v2.Close()
	h := start(t, exampleConfig(t), v2.URL, false)
	workers, per := 32, 100
	if testing.Short() {
		workers, per = 8, 20
	}
	var wg sync.WaitGroup
	errs := make(chan string, workers*2)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < per; i++ {
				id := []int{7, 42, 55}[i%3]
				resp, err := tk.Client.Get(fmt.Sprintf("%s/api/v1/customer?id=%d", h.url, id))
				if err != nil {
					errs <- err.Error()
					return
				}
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != 200 || !strings.HasPrefix(string(b), fmt.Sprintf(`{"customer_id":%d,"name":`, id)) {
					errs <- fmt.Sprintf("id %d: %d %s", id, resp.StatusCode, b)
					return
				}
			}
		}()
	}
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			own := httptest.NewServer(customerapi.NewV2())
			defer own.Close()
			oh := start(t, exampleConfig(t), own.URL, false)
			if failed := customerapi.Failed(customerapi.Contract(oh.url)); len(failed) > 0 {
				errs <- fmt.Sprintf("contract failed under load: %v", failed)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
	ids := map[uint64]bool{}
	for _, e := range h.audit.Events(t) {
		if e.Phase == "request" {
			if ids[e.Request] {
				t.Fatal("request ids are not unique")
			}
			ids[e.Request] = true
		}
	}
	if len(ids) != workers*per {
		t.Fatalf("expected %d request events, got %d", workers*per, len(ids))
	}
}
