package record_test

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

	"github.com/aftermarket-dev/aftermarket/internal/audit"
	"github.com/aftermarket-dev/aftermarket/internal/fixture"
	"github.com/aftermarket-dev/aftermarket/internal/record"
	tk "github.com/aftermarket-dev/aftermarket/internal/testkit"
)

// lockedBuffer is the fixture file in tests.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type harness struct {
	url   string
	rec   *record.Recorder
	out   *lockedBuffer
	audit *tk.AuditSink
}

func start(t *testing.T, cfgYAML, upstream string) *harness {
	t.Helper()
	cfg := record.Default()
	if cfgYAML != "" {
		var err error
		if cfg, err = record.Load([]byte(cfgYAML)); err != nil {
			t.Fatalf("config: %v", err)
		}
	}
	u, _ := url.Parse(upstream)
	h := &harness{out: &lockedBuffer{}, audit: &tk.AuditSink{}}
	rec, err := record.New(record.Options{Upstream: u, Config: cfg, Out: h.out, Audit: audit.New(h.audit), HealthPath: "/_aftermarket/healthz"})
	if err != nil {
		t.Fatal(err)
	}
	h.rec = rec
	srv := httptest.NewServer(rec)
	t.Cleanup(srv.Close)
	h.url = srv.URL
	return h
}

// settle waits until n exchanges have been decided (recorded or skipped).
// The recorder decides after the last byte has gone to the client, so a
// test can be a moment ahead of it.
func (h *harness) settle(t *testing.T, n int) []audit.Event {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var ev []audit.Event
		for _, e := range h.audit.Events(t) {
			if e.Event == audit.Recorded || e.Event == audit.Skipped {
				ev = append(ev, e)
			}
		}
		if len(ev) >= n {
			return ev
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d exchanges were decided: %s", len(ev), n, h.audit.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (h *harness) fixture(t *testing.T) *fixture.Fixture {
	t.Helper()
	f, err := fixture.Parse([]byte(h.out.String()))
	if err != nil {
		t.Fatalf("the recording is not a valid fixture: %v\n%s", err, h.out.String())
	}
	return f
}

func count(ev []audit.Event, kind string) int {
	n := 0
	for _, e := range ev {
		if e.Event == kind {
			n++
		}
	}
	return n
}

// TestRecordingChangesZeroBytes sends random traffic straight to a test
// server and through the recorder. Both sides must see exactly the same
// bytes. Then every exchange must be accounted for, recorded or skipped,
// and the recording must be a valid fixture.
func TestRecordingChangesZeroBytes(t *testing.T) {
	seed := time.Now().UnixNano()
	if s := os.Getenv("AFTERMARKET_SEED"); s != "" {
		seed, _ = strconv.ParseInt(s, 10, 64)
	}
	t.Logf("seed %d (set AFTERMARKET_SEED to repeat)", seed)
	rec := &tk.Recorder{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := rec.Add(r)
		rs := rand.New(rand.NewSource(int64(len(got.Body))*7919 + int64(len(got.URI))))
		statuses := []int{200, 201, 204, 304, 400, 404, 500}
		status := statuses[rs.Intn(len(statuses))]
		var body []byte
		if rs.Intn(2) == 0 {
			w.Header().Set("Content-Type", "application/json")
			body = []byte(fmt.Sprintf(`{"n":%d,"s":"é \"x\"\n","a":[1,2.50,true,null]}`, rs.Intn(1000)))
		} else {
			w.Header().Set("Content-Type", "application/octet-stream")
			body = make([]byte, rs.Intn(30000))
			rs.Read(body)
		}
		w.Header().Add("X-Multi", "a")
		w.Header().Add("X-Multi", "b")
		if r.URL.Query().Get("gz") == "1" {
			w.Header().Set("Content-Encoding", "gzip")
			body = tk.Gzip(body)
		}
		chunked := r.URL.Query().Get("chunked") == "1"
		if status != 204 && status != 304 && !chunked {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		}
		w.WriteHeader(status)
		if status == 204 || status == 304 || r.Method == http.MethodHead {
			return
		}
		if !chunked {
			w.Write(body)
			return
		}
		for i := 0; i < len(body); i += 997 {
			w.Write(body[i:min(i+997, len(body))])
			w.(http.Flusher).Flush()
		}
	}))
	defer up.Close()
	h := start(t, "", up.URL)

	rs := rand.New(rand.NewSource(seed))
	segs := []string{"api", "v1", "customers", "a%20b", "x.y", "über", "1", "_", "%2F"}
	methods := []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"}
	const n = 300
	for i := 0; i < n; i++ {
		method := methods[rs.Intn(len(methods))]
		var p strings.Builder
		for j := 0; j <= rs.Intn(4); j++ {
			p.WriteString("/" + segs[rs.Intn(len(segs))])
		}
		uri := p.String() + fmt.Sprintf("?k%d=%d&q=x%%20y;z", rs.Intn(3), rs.Intn(100))
		if rs.Intn(4) == 0 {
			uri += "&gz=1"
		}
		if rs.Intn(4) == 0 {
			uri += "&chunked=1"
		}
		hdr := http.Header{"User-Agent": {"aftermarket-test"}, "X-Rand": {strconv.Itoa(rs.Intn(1000))},
			"Authorization": {"Bearer secret-token-" + strconv.Itoa(i)}}
		var body []byte
		if method != "GET" && method != "HEAD" && rs.Intn(2) == 0 {
			if rs.Intn(2) == 0 {
				hdr.Set("Content-Type", "application/json")
				body = []byte(`{"name":"Zoë","tags":["a","b"]}`)
			} else {
				body = make([]byte, rs.Intn(40000))
				rs.Read(body)
			}
		}
		send := tk.Do
		if body != nil && rs.Intn(3) == 0 {
			send = tk.DoChunked
		}
		direct := send(t, method, up.URL+uri, hdr, body)
		dReq := rec.Last()
		proxied := send(t, method, h.url+uri, hdr, body)
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
	ev := h.settle(t, n)
	recorded := count(ev, audit.Recorded)
	if recorded == 0 || count(ev, audit.Skipped) == 0 {
		t.Fatalf("want both recorded and skipped exchanges, got %d recorded of %d", recorded, len(ev))
	}
	f := h.fixture(t)
	if len(f.Cases) != recorded {
		t.Fatalf("%d cases in the file, %d recorded events", len(f.Cases), recorded)
	}
	if strings.Contains(h.out.String(), "secret-token") {
		t.Fatal("a credential reached the fixture file")
	}
	// Every recorded case replays through a pass-through proxy unchanged.
	for _, r := range fixture.Run(passThrough, f) {
		if !r.Pass {
			t.Fatalf("recorded case %s does not replay: %v", r.Name, r.Problems)
		}
	}
}

// passThrough is a recorder with nowhere to write: a proxy that changes
// nothing, for running recorded fixtures.
func passThrough(u *url.URL) (http.Handler, error) {
	return record.New(record.Options{Upstream: u, Out: io.Discard})
}

func TestResponsesStream(t *testing.T) {
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
	h := start(t, "", up.URL)
	resp, err := tk.Client.Get(h.url + "/stream")
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
		t.Fatal("the recorder is buffering the response")
	}
}

// TestRedaction sends credentials and listed values both ways. The upstream
// and the client must see the real values; the fixture file must not hold
// a single one of them.
func TestRedaction(t *testing.T) {
	rec := &tk.Recorder{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.Add(r)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Add("Set-Cookie", "session=cookie-secret-1; HttpOnly")
		w.Header().Add("Set-Cookie", "other=cookie-secret-2")
		w.Header().Set("X-Session-Token", "header-secret-3")
		io.WriteString(w, `{"data":[{"id":1,"email":"one@secret.example"},{"id":2,"email":"two@secret.example"}],"token":"body-secret-4","keep":"visible"}`)
	}))
	defer up.Close()
	h := start(t, `version: 1
redact:
  headers: [X-Session-Token, x-client-secret]
  query: [api_key, "sig"]
  json: [data.*.email, token, password, profile.ssn]
`, up.URL)
	hdr := http.Header{
		"Authorization":       {"Bearer auth-secret-5"},
		"Proxy-Authorization": {"Basic proxy-secret-6"},
		"Cookie":              {"session=cookie-secret-7"},
		"X-Api-Key":           {"apikey-secret-8"},
		"X-Client-Secret":     {"client-secret-9"},
		"Content-Type":        {"application/json"},
		"X-Visible":           {"stays"},
	}
	body := []byte(`{"user":"ada","password":"pw-secret-10","profile":{"ssn":"ssn-secret-11","city":"London"}}`)
	got := tk.Do(t, "POST", h.url+"/login?api_key=query-secret-12&page=2&sig=sig%2Dsecret%2D13&x=1", hdr, body)
	if got.Status != 200 || !strings.Contains(string(got.Body), "body-secret-4") || len(got.Header.Values("Set-Cookie")) != 2 {
		t.Fatalf("the client didn't get the real answer: %d %s %v", got.Status, got.Body, got.Header)
	}
	seen := rec.Last()
	if seen.Header.Get("Authorization") != "Bearer auth-secret-5" || !bytes.Equal(seen.Body, body) ||
		seen.URI != "/login?api_key=query-secret-12&page=2&sig=sig%2Dsecret%2D13&x=1" {
		t.Fatalf("the upstream didn't get the real request: %+v", seen)
	}
	h.settle(t, 1)
	out := h.out.String()
	for i, s := range []string{"cookie-secret-1", "cookie-secret-2", "header-secret-3", "one@secret", "two@secret", "body-secret-4",
		"auth-secret-5", "proxy-secret-6", "cookie-secret-7", "apikey-secret-8", "client-secret-9", "pw-secret-10",
		"ssn-secret-11", "query-secret-12", "secret%2D13"} {
		if strings.Contains(out, s) {
			t.Errorf("secret %d (%s) reached the fixture file", i+1, s)
		}
	}
	for _, s := range []string{"visible", "London", `\"user\":\"ada\"`, "page=2", "x=1", "X-Visible", "api_key=REDACTED", "sig=REDACTED"} {
		if !strings.Contains(out, s) {
			t.Errorf("%q should still be in the fixture file", s)
		}
	}
	f := h.fixture(t)
	c := f.Cases[0]
	if *c.Request.Body != `{"user":"ada","password":"REDACTED","profile":{"ssn":"REDACTED","city":"London"}}` {
		t.Errorf("request body = %s", *c.Request.Body)
	}
	if *c.ExpectResponse.Body != `{"data":[{"id":1,"email":"REDACTED"},{"id":2,"email":"REDACTED"}],"token":"REDACTED","keep":"visible"}` {
		t.Errorf("response body = %s", *c.ExpectResponse.Body)
	}
	if strings.Contains(h.audit.String(), "secret") {
		t.Fatal("the audit log holds a value")
	}
}

// failingUpstream closes the connection halfway through the body.
func cutOffUpstream(t *testing.T) string {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(200)
		io.WriteString(w, "only part")
		w.(http.Flusher).Flush()
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	}))
	t.Cleanup(up.Close)
	return up.URL
}

// TestUnstorableExchangesPassAndAreSkipped covers every reason an exchange
// can't be stored. Each must reach the client exactly as a direct call
// would, and be logged as skipped with the reason.
func TestUnstorableExchangesPassAndAreSkipped(t *testing.T) {
	big := strings.Repeat("x", 3000)
	binary := []byte{0xff, 0xfe, 0x00, 0x80}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/big":
			io.WriteString(w, big)
		case "/binary":
			w.Write(binary)
		case "/brotli":
			w.Header().Set("Content-Encoding", "br")
			w.Write([]byte("not really brotli"))
		case "/badgzip":
			w.Header().Set("Content-Encoding", "gzip")
			w.Write([]byte("not gzip"))
		case "/gzipbomb":
			w.Header().Set("Content-Encoding", "gzip")
			w.Write(tk.Gzip(bytes.Repeat([]byte("a"), 100000)))
		case "/badjson":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"email": "x", "email": "y"}`)
		default:
			io.WriteString(w, "ok")
		}
	}))
	defer up.Close()
	h := start(t, "limits: {max_body: 2KB}\nredact: {json: [email]}\n", up.URL)
	cases := []struct {
		method, path string
		ct           string
		body         []byte
		reason       string
	}{
		{"GET", "/big", "", nil, "response body is larger than max_body"},
		{"POST", "/ok", "text/plain", []byte(big), "request body is larger than max_body"},
		{"GET", "/binary", "", nil, "response body is not text"},
		{"POST", "/ok", "application/octet-stream", binary, "request body is not text"},
		{"GET", "/brotli", "", nil, "compressed with br"},
		{"GET", "/badgzip", "", nil, "gzip body could not be decoded"},
		{"GET", "/gzipbomb", "", nil, "larger than max_body once decompressed"},
		{"GET", "/badjson", "", nil, "response JSON body could not be read to redact it"},
		{"POST", "/ok", "application/json", []byte(`{"email":`), "request JSON body could not be read to redact it"},
	}
	for i, c := range cases {
		hdr := http.Header{}
		if c.ct != "" {
			hdr.Set("Content-Type", c.ct)
		}
		direct := tk.Do(t, c.method, up.URL+c.path, hdr, c.body)
		proxied := tk.Do(t, c.method, h.url+c.path, hdr, c.body)
		if direct.Status != proxied.Status || !bytes.Equal(direct.Body, proxied.Body) {
			t.Fatalf("%s %s: the client got something else through the recorder", c.method, c.path)
		}
		ev := h.settle(t, i+1)
		last := ev[len(ev)-1]
		if last.Event != audit.Skipped || !strings.Contains(last.Reason, c.reason) {
			t.Fatalf("%s %s: got %s %q, want skipped %q", c.method, c.path, last.Event, last.Reason, c.reason)
		}
	}
	// A cut-off upstream body: the client sees the same failure, nothing is stored.
	h2 := start(t, "", cutOffUpstream(t))
	resp, err := tk.Client.Get(h2.url + "/x")
	if err == nil {
		io.ReadAll(resp.Body)
		resp.Body.Close()
	}
	ev := h2.settle(t, 1)
	if ev[0].Event != audit.Skipped || !strings.Contains(ev[0].Reason, "cut off") {
		t.Fatalf("got %+v", ev[0])
	}
	if strings.Contains(h.out.String()+h2.out.String(), "- name:") {
		t.Fatalf("something was recorded:\n%s", h.out.String())
	}
}

func TestGzipBodiesAreStoredDecoded(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.Write(tk.Gzip([]byte(`{"echo":` + string(tk.Gunzip(t, b)) + `}`)))
	}))
	defer up.Close()
	h := start(t, "", up.URL)
	got := tk.Do(t, "POST", h.url+"/echo", http.Header{"Content-Type": {"application/json"}, "Content-Encoding": {"gzip"}}, tk.Gzip([]byte(`[1,2]`)))
	if got.Header.Get("Content-Encoding") != "gzip" || string(tk.Gunzip(t, got.Body)) != `{"echo":[1,2]}` {
		t.Fatalf("the client didn't get the gzip answer: %v", got.Header)
	}
	h.settle(t, 1)
	c := h.fixture(t).Cases[0]
	if *c.Request.Body != "[1,2]" || *c.ExpectResponse.Body != `{"echo":[1,2]}` {
		t.Fatalf("bodies = %q, %q", *c.Request.Body, *c.ExpectResponse.Body)
	}
	if strings.Contains(h.out.String(), "Content-Encoding") || strings.Contains(h.out.String(), "Content-Length") {
		t.Fatal("encoding headers were stored with decoded bodies")
	}
}

func TestWhenPicksTrafficAndMaxCasesStops(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/missing" {
			w.WriteHeader(404)
		}
		io.WriteString(w, "ok")
	}))
	defer up.Close()
	h := start(t, "when:\n  method: GET\n  path: /api/**\n  status: 2xx\nmax_cases: 2\n", up.URL)
	for _, p := range []string{"/other", "/api/missing"} {
		tk.Do(t, "GET", h.url+p, nil, nil)
	}
	tk.Do(t, "POST", h.url+"/api/a", nil, []byte("x"))
	for _, p := range []string{"/api/a", "/api/b", "/api/c", "/api/d"} {
		if got := tk.Do(t, "GET", h.url+p, nil, nil); string(got.Body) != "ok" {
			t.Fatalf("%s: %q", p, got.Body)
		}
	}
	time.Sleep(50 * time.Millisecond)
	f := h.fixture(t)
	if len(f.Cases) != 2 || f.Cases[0].Request.Path != "/api/a" || f.Cases[1].Request.Path != "/api/b" {
		t.Fatalf("cases: %+v", f.Cases)
	}
	if h.rec.Cases() != 2 {
		t.Fatalf("Cases() = %d", h.rec.Cases())
	}
}

func TestUpstreamDownAndSlow(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := "http://" + ln.Addr().String()
	ln.Close()
	h := start(t, "", dead)
	if got := tk.Do(t, "GET", h.url+"/x?token=secret", nil, nil); got.Status != 502 {
		t.Fatalf("status %d", got.Status)
	}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer slow.Close()
	h2 := start(t, "limits: {upstream_timeout: 200ms}\n", slow.URL)
	if got := tk.Do(t, "GET", h2.url+"/x", nil, nil); got.Status != 504 {
		t.Fatalf("status %d", got.Status)
	}
	for _, hh := range []*harness{h, h2} {
		ev := hh.audit.Events(t)
		if len(ev) != 2 || ev[0].Event != audit.UpstreamError || ev[1].Event != audit.Skipped {
			t.Fatalf("events: %s", hh.audit.String())
		}
		if strings.Contains(hh.out.String(), "- name:") {
			t.Fatal("a failed exchange was recorded")
		}
	}
	if strings.Contains(h.audit.String(), "secret") {
		t.Fatal("a query value reached the audit log")
	}
}

func TestHealthPath(t *testing.T) {
	h := start(t, "", "http://127.0.0.1:1")
	if got := tk.Do(t, "GET", h.url+"/_aftermarket/healthz", nil, nil); got.Status != 200 || string(got.Body) != "ok\n" {
		t.Fatalf("%d %q", got.Status, got.Body)
	}
}

func TestConcurrentRecording(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"path":%q}`, r.URL.Path)
	}))
	defer up.Close()
	h := start(t, "", up.URL)
	workers, per := 16, 50
	if testing.Short() {
		workers, per = 4, 10
	}
	var wg sync.WaitGroup
	errs := make(chan string, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < per; i++ {
				p := fmt.Sprintf("/w%d/%d", w, i)
				resp, err := tk.Client.Get(h.url + p)
				if err != nil {
					errs <- err.Error()
					return
				}
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if string(b) != fmt.Sprintf(`{"path":%q}`, p) {
					errs <- "wrong answer for " + p
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
	h.settle(t, workers*per)
	f := h.fixture(t)
	if len(f.Cases) != workers*per {
		t.Fatalf("%d cases, want %d", len(f.Cases), workers*per)
	}
	seen := map[string]bool{}
	for _, c := range f.Cases {
		if *c.ExpectResponse.Body != fmt.Sprintf(`{"path":%q}`, c.Request.Path) || seen[c.Request.Path] {
			t.Fatalf("case %s is mixed up: %s", c.Request.Path, *c.ExpectResponse.Body)
		}
		seen[c.Request.Path] = true
	}
}

func TestConfigErrorsAreAllReportedInLineOrder(t *testing.T) {
	_, err := record.Load([]byte(`version: 2
when:
  method: [get, "a b"]
  path: nope
redact:
  headers: ["bad header"]
  query: [""]
  json: [a..b, " x"]
  cookies: [x]
limits: {max_body: lots}
max_cases: 0
extra: 1
`))
	if err == nil {
		t.Fatal("no error")
	}
	msg := err.Error()
	want := []string{"line 1: version must be 1", "not an HTTP method", "path must start with /", "not a valid header name",
		"query key can't be empty", `"a..b" is not a dot path`, `" x" is not a dot path`, `unknown key "cookies"`, "max_body", "max_cases", `unknown key "extra"`}
	last := -1
	for _, w := range want {
		i := strings.Index(msg, w)
		if i < 0 {
			t.Fatalf("missing %q in:\n%s", w, msg)
		}
		if i < last {
			t.Fatalf("%q is out of line order in:\n%s", w, msg)
		}
		last = i
	}
	if _, err := record.Load([]byte("redact:\n  headers: X-Token\n  json: [data.*.email]\n")); err != nil {
		t.Fatalf("a single value and a * path are fine: %v", err)
	}
}
