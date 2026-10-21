package guard

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// app records every request that reaches it, and answers with a body that
// depends only on the request, so a direct call and a call through
// AfterGuard can be compared.
type app struct {
	mu   sync.Mutex
	seen []*seenReq
}

type seenReq struct {
	method, uri string
	header      http.Header
	body        []byte
}

func (a *app) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	a.mu.Lock()
	a.seen = append(a.seen, &seenReq{method: r.Method, uri: r.RequestURI, header: r.Header.Clone(), body: body})
	a.mu.Unlock()
	sum := sha256.Sum256(append([]byte(r.Method+" "+r.RequestURI+"\n"), body...))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Echo-Sum", hex.EncodeToString(sum[:8]))
	w.Header().Set("Server", "Old/1.0")
	w.Header().Add("Set-Cookie", "a=1")
	w.Header().Add("Set-Cookie", "b=2")
	status := 200
	if strings.Contains(r.RequestURI, "status=") {
		fmt.Sscan(r.URL.Query().Get("status"), &status)
	}
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		w.Write(sum[:])
		w.Write(body)
	}
}

func (a *app) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.seen)
}

func (a *app) last() *seenReq {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.seen) == 0 {
		return nil
	}
	return a.seen[len(a.seen)-1]
}

type rig struct {
	app    *app
	appURL string
	url    string // AfterGuard
	addr   string
	log    *bytes.Buffer
	logMu  *sync.Mutex
	guard  *Guard
	limits *connLimits
	clock  *fixedClock
	srv    *http.Server
}

type fixedClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fixedClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fixedClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type lockedWriter struct {
	mu *sync.Mutex
	b  *bytes.Buffer
}

func (w lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.Write(p)
}

func (r *rig) logText() string {
	r.logMu.Lock()
	defer r.logMu.Unlock()
	return r.log.String()
}

func mustLoad(t testing.TB, yaml string) *Config {
	t.Helper()
	c, err := Load([]byte(yaml), t.TempDir())
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	return c
}

func start(t testing.TB, yaml string, opts ...func(*Options)) *rig {
	t.Helper()
	r := &rig{app: &app{}, log: &bytes.Buffer{}, logMu: &sync.Mutex{}, clock: &fixedClock{t: time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)}}
	appSrv := httptest.NewServer(r.app)
	t.Cleanup(appSrv.Close)
	r.appURL = appSrv.URL
	up, _ := url.Parse(appSrv.URL)
	o := Options{Config: mustLoad(t, yaml), Upstream: up, Log: NewLog(lockedWriter{r.logMu, r.log}, r.clock.now), Now: r.clock.now, HealthPath: DefaultHealthPath}
	for _, f := range opts {
		f(&o)
	}
	g, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	r.guard = g
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := g.Listener(raw)
	if err != nil {
		t.Fatal(err)
	}
	r.limits, _ = ln.(trackListener).Listener.(*connLimits) // nil with TLS in between
	r.srv = g.Server()
	go r.srv.Serve(ln)
	t.Cleanup(func() { r.srv.Close() })
	r.addr = raw.Addr().String()
	r.url = "http://" + r.addr
	return r
}

// raw sends bytes as written and reads the response that comes back.
func (r *rig) raw(t testing.TB, req string) []*http.Response {
	t.Helper()
	return r.rawN(t, req, 1)
}

// rawN reads up to n responses, or until the connection closes.
func (r *rig) rawN(t testing.TB, req string, n int) []*http.Response {
	t.Helper()
	c, err := net.Dial("tcp", r.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(c)
	var out []*http.Response
	for {
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			return out
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body = io.NopCloser(bytes.NewReader(b))
		out = append(out, resp)
		if resp.Close || len(out) == n {
			return out
		}
	}
}

var client = &http.Client{Transport: &http.Transport{DisableCompression: true, MaxIdleConnsPerHost: 64}, Timeout: 5 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func do(t testing.TB, method, u string, h http.Header, body []byte) (*http.Response, []byte) {
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
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, u, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: read body: %v", method, u, err)
	}
	return resp, b
}

func seed(t testing.TB) int64 {
	s := time.Now().UnixNano()
	if v := os.Getenv("AFTERMARKET_SEED"); v != "" {
		fmt.Sscan(v, &s)
	}
	t.Logf("seed %d (set AFTERMARKET_SEED to repeat)", s)
	return s
}

const openConfig = `
default: deny
rules:
  - id: no-admin
    when: {path: /admin/**}
    deny: 403
  - id: open
    when: {path: /**}
    allow: true
`

// Promise 1: nothing ambiguous gets through. Each request is sent over a raw
// socket. It must be rejected, or reach the app in exactly the form the
// rules judged.
func TestNothingAmbiguousGetsThrough(t *testing.T) {
	r := start(t, openConfig)
	cases := []struct {
		name, req string
		status    int    // what AfterGuard must answer
		appURI    string // what the app must see, when it sees anything
	}{
		{"both Content-Length and Transfer-Encoding", "POST /x HTTP/1.1\r\nHost: a\r\nContent-Length: 4\r\nTransfer-Encoding: chunked\r\n\r\n3\r\nabc\r\n0\r\n\r\n", 400, ""},
		{"two Transfer-Encoding headers", "POST /x HTTP/1.1\r\nHost: a\r\nTransfer-Encoding: chunked\r\nTransfer-Encoding: chunked\r\n\r\n0\r\n\r\n", 0, ""},
		{"a folded header", "GET /x HTTP/1.1\r\nHost: a\r\nX-A: 1\r\n  folded\r\n\r\n", 400, ""},
		{"bare LF line endings", "GET /x HTTP/1.1\nHost: a\n\n", 400, ""},
		{"encoded slash", "GET /x/..%2fadmin/users HTTP/1.1\r\nHost: a\r\n\r\n", 400, ""},
		{"encoded dots", "GET /x/%2e%2e/admin/users HTTP/1.1\r\nHost: a\r\n\r\n", 400, ""},
		{"encoded dots, upper case", "GET /x/%2E%2E/admin/users HTTP/1.1\r\nHost: a\r\n\r\n", 400, ""},
		{"plain dot-dot", "GET /x/../admin/users HTTP/1.1\r\nHost: a\r\n\r\n", 400, ""},
		{"single dot", "GET /./admin/users HTTP/1.1\r\nHost: a\r\n\r\n", 400, ""},
		{"empty segment", "GET //admin/users HTTP/1.1\r\nHost: a\r\n\r\n", 400, ""},
		{"encoded NUL", "GET /admin%00/users HTTP/1.1\r\nHost: a\r\n\r\n", 400, ""},
		{"double encoding", "GET /%2561dmin/users HTTP/1.1\r\nHost: a\r\n\r\n", 400, ""},
		{"backslash", "GET /x\\..\\admin HTTP/1.1\r\nHost: a\r\n\r\n", 400, ""},
		{"encoded backslash", "GET /x%5c..%5cadmin HTTP/1.1\r\nHost: a\r\n\r\n", 400, ""},
		{"semicolon", "GET /admin;x/users HTTP/1.1\r\nHost: a\r\n\r\n", 400, ""},
		{"semicolon in the query", "GET /x?a=1;b=2 HTTP/1.1\r\nHost: a\r\n\r\n", 400, ""},
		{"bad query escape", "GET /x?a=%zz HTTP/1.1\r\nHost: a\r\n\r\n", 400, ""},
		{"absolute-form target", "GET http://evil.example/admin/users HTTP/1.1\r\nHost: a\r\n\r\n", 400, ""},
		{"CONNECT", "CONNECT evil.example:443 HTTP/1.1\r\nHost: evil.example:443\r\n\r\n", 400, ""},
		{"encoded control character", "GET /x%01 HTTP/1.1\r\nHost: a\r\n\r\n", 400, ""},
		{"invalid UTF-8", "GET /x%ff HTTP/1.1\r\nHost: a\r\n\r\n", 400, ""},
		// Not ambiguous: decoded, judged, and forwarded as judged.
		{"an encoded letter is judged decoded", "GET /%61dmin/users HTTP/1.1\r\nHost: a\r\n\r\n", 403, ""},
		{"an encoded letter elsewhere reaches the app decoded", "GET /c%61t HTTP/1.1\r\nHost: a\r\n\r\n", 200, "/cat"},
		{"UTF-8 is forwarded escaped", "GET /caf%C3%A9 HTTP/1.1\r\nHost: a\r\n\r\n", 200, "/caf%C3%A9"},
		{"the query goes as sent", "GET /q?b=2&a=%20x HTTP/1.1\r\nHost: a\r\n\r\n", 200, "/q?b=2&a=%20x"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := r.app.count()
			resps := r.raw(t, c.req)
			got := 0
			if len(resps) > 0 {
				got = resps[0].StatusCode
			}
			if c.status != 0 && got != c.status {
				t.Fatalf("status %d, want %d", got, c.status)
			}
			if c.status == 0 && got < 400 {
				t.Fatalf("status %d, want a refusal", got)
			}
			reached := r.app.count() - before
			if c.appURI == "" && reached != 0 {
				t.Fatalf("the app got %d request(s): %s", reached, r.app.last().uri)
			}
			if c.appURI != "" && (reached != 1 || r.app.last().uri != c.appURI) {
				t.Fatalf("the app got %d request(s), last %+v; want %s", reached, r.app.last(), c.appURI)
			}
		})
	}
}

// A clean request followed on the same connection by a smuggling attempt:
// the first goes through, the second is rejected, and the connection closes.
func TestSmugglingAfterACleanRequestOnOneConnection(t *testing.T) {
	r := start(t, openConfig)
	req := "POST /one HTTP/1.1\r\nHost: a\r\nTransfer-Encoding: chunked\r\n\r\n5;ext=1\r\nhello\r\n0\r\nX-Trailer: t\r\n\r\n" +
		"POST /two HTTP/1.1\r\nHost: a\r\nContent-Length: 30\r\nTransfer-Encoding: chunked\r\n\r\n0\r\n\r\nGET /admin/users HTTP/1.1\r\n\r\n"
	resps := r.rawN(t, req, 3)
	if len(resps) != 2 || resps[0].StatusCode != 200 || resps[1].StatusCode != 400 {
		var st []int
		for _, x := range resps {
			st = append(st, x.StatusCode)
		}
		t.Fatalf("statuses %v", st)
	}
	if r.app.count() != 1 || r.app.last().uri != "/one" || string(r.app.last().body) != "hello" {
		t.Fatalf("app saw %d: %+v", r.app.count(), r.app.last())
	}
}

// The tracker follows keep-alive traffic split at every byte boundary.
func TestTrackerFollowsSplitReads(t *testing.T) {
	stream := "POST /a HTTP/1.1\r\nHost: a\r\nContent-Length: 5\r\n\r\nhello" +
		"\r\n\r\nGET /b HTTP/1.1\r\nHost: a\r\n\r\n" +
		"POST /c HTTP/1.1\r\nHost: a\r\nTransfer-Encoding: chunked\r\n\r\n3\r\nabc\r\n10\r\n0123456789abcdef\r\n0\r\n\r\n" +
		"GET /d HTTP/1.1\r\nHost: a\r\nX: 1\r\n folded\r\n\r\n"
	for size := 1; size <= 7; size++ {
		tr := &tracker{maxHead: 4096}
		for i := 0; i < len(stream); i += size {
			end := min(i+size, len(stream))
			tr.feed([]byte(stream[i:end]))
		}
		var got []string
		for {
			v, ok := tr.next()
			if !ok {
				break
			}
			got = append(got, v)
			if len(got) > 10 {
				break
			}
		}
		if len(got) != 4 || got[0] != "" || got[1] != "" || got[2] != "" || got[3] != "a header is folded over two lines" {
			t.Fatalf("size %d: %q", size, got)
		}
	}
}

// Promise 2: a denied request never reaches the app. Random rule sets meet
// random requests; the test keeps its own verdict for each, and the app must
// see exactly the requests that verdict allows.
func TestDeniedRequestsNeverReachTheApp(t *testing.T) {
	rs := rand.New(rand.NewSource(seed(t)))
	paths := []string{"/a", "/a/b", "/b/c", "/c", "/d/e/f"}
	methods := []string{"GET", "POST", "DELETE"}
	for round := 0; round < 8; round++ {
		type rl struct {
			path, method string
			fromLocal    bool // from 127.0.0.1, or 10.0.0.0/8, which never matches here
			allow        bool
		}
		var rls []rl
		var b strings.Builder
		def := []string{"deny", "allow"}[rs.Intn(2)]
		fmt.Fprintf(&b, "default: %s\nrules:\n", def)
		for i := 0; i < 1+rs.Intn(6); i++ {
			x := rl{path: paths[rs.Intn(len(paths))], method: methods[rs.Intn(3)], fromLocal: rs.Intn(3) > 0, allow: rs.Intn(2) == 0}
			rls = append(rls, x)
			from := "127.0.0.1"
			if !x.fromLocal {
				from = "10.0.0.0/8"
			}
			action := "deny: 403"
			if x.allow {
				action = "allow: true"
			}
			fmt.Fprintf(&b, "  - id: r%d\n    when: {method: %s, path: %s}\n    from: [%s]\n    %s\n", i, x.method, x.path, from, action)
		}
		r := start(t, b.String())
		want := 0
		for i := 0; i < 60; i++ {
			p, m := paths[rs.Intn(len(paths))], methods[rs.Intn(3)]
			allowed := def == "allow"
			for _, x := range rls {
				if x.path == p && x.method == m && x.fromLocal {
					allowed = x.allow
					break
				}
			}
			before := r.app.count()
			resp, _ := do(t, m, r.url+p+fmt.Sprintf("?n=%d", i), nil, nil)
			reached := r.app.count() - before
			switch {
			case allowed && (reached != 1 || resp.StatusCode != 200):
				t.Fatalf("round %d: %s %s should pass: status %d, reached %d\n%s", round, m, p, resp.StatusCode, reached, b.String())
			case !allowed && (reached != 0 || resp.StatusCode != 403):
				t.Fatalf("round %d: %s %s should be denied: status %d, reached %d\n%s", round, m, p, resp.StatusCode, reached, b.String())
			}
			if allowed {
				want++
			}
		}
		if r.app.count() != want {
			t.Fatalf("round %d: the app saw %d requests, want %d", round, r.app.count(), want)
		}
	}
}

// Promise 3: allowed traffic passes byte for byte. Random requests go to the
// app directly and through AfterGuard; the app must get the same request,
// apart from the forwarding headers AfterGuard sets, and the client the same
// answer.
func TestAllowedTrafficPassesByteForByte(t *testing.T) {
	r := start(t, "default: allow\nrules:\n  - id: none\n    when: {path: /never}\n    deny: 404\n")
	rs := rand.New(rand.NewSource(seed(t)))
	segs := []string{"a", "b", "caf%C3%A9", "x-y", "1", "~u", "%20sp"}
	for i := 0; i < 300; i++ {
		var p strings.Builder
		for j := 0; j <= rs.Intn(4); j++ {
			p.WriteString("/" + segs[rs.Intn(len(segs))])
		}
		target := p.String()
		if rs.Intn(2) == 0 {
			target += fmt.Sprintf("?k=%d&v=a%%20b&k=%d", rs.Intn(9), rs.Intn(9))
		}
		if rs.Intn(5) == 0 {
			target += map[bool]string{true: "&", false: "?"}[strings.Contains(target, "?")] + "status=" + []string{"201", "404", "500", "204"}[rs.Intn(4)]
		}
		method := []string{"GET", "POST", "PUT", "DELETE", "PATCH", "HEAD"}[rs.Intn(6)]
		h := http.Header{"X-Custom": {fmt.Sprint(rs.Int())}, "Accept": {"*/*"}}
		var body []byte
		if method == "POST" || method == "PUT" || method == "PATCH" {
			body = make([]byte, rs.Intn(70000))
			rs.Read(body)
		}
		direct, directBody := do(t, method, r.appURL+target, h, body)
		directReq := r.app.last()
		via, viaBody := do(t, method, r.url+target, h, body)
		viaReq := r.app.last()
		if directReq.method != viaReq.method || directReq.uri != viaReq.uri || !bytes.Equal(directReq.body, viaReq.body) {
			t.Fatalf("%d: the app got %s %s (%d bytes) through AfterGuard, %s %s (%d bytes) directly",
				i, viaReq.method, viaReq.uri, len(viaReq.body), directReq.method, directReq.uri, len(directReq.body))
		}
		for name := range directReq.header {
			if directReq.header.Get(name) != viaReq.header.Get(name) {
				t.Fatalf("%d: header %s: %q through AfterGuard, %q directly", i, name, viaReq.header.Get(name), directReq.header.Get(name))
			}
		}
		if direct.StatusCode != via.StatusCode || !bytes.Equal(directBody, viaBody) {
			t.Fatalf("%d: %s %s: answer %d (%d bytes) through AfterGuard, %d (%d bytes) directly", i, method, target, via.StatusCode, len(viaBody), direct.StatusCode, len(directBody))
		}
		for _, name := range []string{"Content-Type", "X-Echo-Sum", "Server"} {
			if direct.Header.Get(name) != via.Header.Get(name) {
				t.Fatalf("%d: answer header %s differs", i, name)
			}
		}
		if fmt.Sprint(direct.Header.Values("Set-Cookie")) != fmt.Sprint(via.Header.Values("Set-Cookie")) {
			t.Fatalf("%d: Set-Cookie differs", i)
		}
	}
	// Chunked request bodies too.
	pr, pw := io.Pipe()
	go func() {
		for i := 0; i < 5; i++ {
			pw.Write(bytes.Repeat([]byte{byte('a' + i)}, 1000))
		}
		pw.Close()
	}()
	req, _ := http.NewRequest("POST", r.url+"/chunked", pr)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || len(r.app.last().body) != 5000 {
		t.Fatalf("chunked: %d, %d bytes", resp.StatusCode, len(r.app.last().body))
	}
}

const limitConfig = `
default: deny
limit_table: 1000
rules:
  - id: shared
    when: {path: /shared}
    allow: true
    limit: {rate: 10/m, burst: 10, per: rule}
  - id: each
    when: {path: /each}
    allow: true
    limit: {rate: 2/m, burst: 2}
`

// Promise 4: limits hold under concurrency, and memory stays bounded however
// many addresses a client sends from.
func TestLimitsHold(t *testing.T) {
	r := start(t, limitConfig)
	var wg sync.WaitGroup
	var mu sync.Mutex
	codes := map[int]int{}
	for w := 0; w < 20; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				resp, _ := do(t, "GET", r.url+"/shared", nil, nil)
				mu.Lock()
				codes[resp.StatusCode]++
				if resp.StatusCode == 429 && resp.Header.Get("Retry-After") == "" {
					t.Error("a 429 without Retry-After")
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if codes[200] != 10 || codes[429] != 190 || r.app.count() != 10 {
		t.Fatalf("codes %v, app saw %d", codes, r.app.count())
	}
	// The bucket refills with time.
	r.clock.add(time.Minute)
	if resp, _ := do(t, "GET", r.url+"/shared", nil, nil); resp.StatusCode != 200 {
		t.Fatal("no refill after a minute")
	}

	// A client rotating through many addresses: each IPv4 address gets its
	// own bucket, but the table never grows past its size, and a full table
	// refuses new keys rather than reset the buckets in use.
	g := r.guard
	r.clock.add(time.Hour) // everything refilled
	if resp, _ := do(t, "GET", r.url+"/shared", nil, nil); resp.StatusCode != 200 {
		t.Fatal("setup")
	}
	limited := 0
	for i := 0; i < 5000; i++ {
		req := httptest.NewRequest("GET", "/each", nil)
		if g.Decide(req, fmt.Sprintf("10.%d.%d.%d:1", i>>16&255, i>>8&255, i&255)).Event == Limited {
			limited++
		}
	}
	if n := g.limits.len(); n > 1000 || limited != 4000 {
		t.Fatalf("%d buckets for a table of 1000, %d new addresses refused, want 4000", n, limited)
	}
	// The one-per-rule bucket still has the 9 tokens left from before.
	shared := 0
	for i := 0; i < 20; i++ {
		if g.Decide(httptest.NewRequest("GET", "/shared", nil), "10.200.0.1:1").Event == Allowed {
			shared++
		}
	}
	if shared != 9 {
		t.Fatalf("the per-rule bucket gave %d after the flood, want 9", shared)
	}
	g.opts.Log.Flush()
	if !strings.Contains(r.logText(), `"event":"limit_table_full_summary"`) {
		t.Fatal("refused keys weren't logged")
	}
	// Once buckets refill, they make room.
	r.clock.add(time.Hour)
	// Addresses in one IPv6 /64 share a bucket, so rotating inside it gains
	// nothing.
	allowed := 0
	for i := 0; i < 1000; i++ {
		req := httptest.NewRequest("GET", "/each", nil)
		if g.Decide(req, fmt.Sprintf("[2001:db8:1:2:%x:%x::1]:1", i, i*7)).Event == Allowed {
			allowed++
		}
	}
	if allowed != 2 {
		t.Fatalf("%d allowed from one /64, want 2", allowed)
	}
	if !strings.Contains(r.logText(), "limit_evicted") {
		g.opts.Log.Flush()
		if !strings.Contains(r.logText(), "limit_evicted") {
			t.Fatal("evictions weren't logged")
		}
	}
}

const identityConfig = `
default: deny
trusted_proxies: [10.9.0.0/16]
clients:
  - id: billing
    api_key_sha256: %s
  - id: admin
    basic_user: admin
    basic_password_sha256: %s
rules:
  - id: office
    when: {path: /office/**}
    from: [192.168.0.0/16]
    allow: true
  - id: reports
    when: {path: /reports/**}
    client: [billing]
    allow: true
  - id: open
    when: {path: /open/**}
    allow: true
`

func identityRig(t *testing.T, opts ...func(*Options)) *rig {
	return start(t, fmt.Sprintf(identityConfig, Hash("billing-key-0123456789"), Hash("admin-password-0123456789")), opts...)
}

// Promise 5: a client can't choose its identity or its address.
func TestClientsCantChooseTheirIdentityOrAddress(t *testing.T) {
	r := identityRig(t)
	spoof := http.Header{
		"X-Aftermarket-Client": {"admin"},
		"X_aftermarket_client": {"admin"},
		"X-Forwarded-For":      {"192.168.1.1"},
		"X-Real-Ip":            {"192.168.1.1"},
		"Forwarded":            {"for=192.168.1.1"},
		"X-Forwarded-Host":     {"evil.example"},
		"X-Forwarded-Proto":    {"https"},
	}
	// Spoofed addresses don't open the office rule.
	if resp, _ := do(t, "GET", r.url+"/office/x", spoof, nil); resp.StatusCode != 403 {
		t.Fatalf("office from a spoofed address: %d", resp.StatusCode)
	}
	// On an open path, the app gets none of the spoofed headers, and the
	// forwarding headers AfterGuard sets itself.
	resp, _ := do(t, "GET", r.url+"/open/x", spoof, nil)
	got := r.app.last().header
	if resp.StatusCode != 200 || got.Get("X-Aftermarket-Client") != "" || got.Get("X_aftermarket_client") != "" ||
		got.Get("X-Real-Ip") != "" || got.Get("Forwarded") != "" || got.Get("X-Forwarded-For") != "127.0.0.1" ||
		got.Get("X-Forwarded-Proto") != "http" || got.Get("X-Forwarded-Host") == "evil.example" {
		t.Fatalf("%d, app got %v", resp.StatusCode, got)
	}
	// With the right key, the app learns who called, and never sees the key.
	h := http.Header{"X-Api-Key": {"billing-key-0123456789"}, "X-Aftermarket-Client": {"admin"}}
	resp, _ = do(t, "GET", r.url+"/reports/daily", h, nil)
	got = r.app.last().header
	if resp.StatusCode != 200 || fmt.Sprint(got.Values("X-Aftermarket-Client")) != "[billing]" || got.Get("X-Api-Key") != "" {
		t.Fatalf("%d, app got %v", resp.StatusCode, got)
	}

	// Behind a trusted proxy, its last hop is believed and nothing before it.
	g := r.guard
	req := httptest.NewRequest("GET", "/office/x", nil)
	req.Header.Set("X-Forwarded-For", "192.168.1.1, 10.1.2.3")
	if d := g.Decide(req, "10.9.0.5:1"); d.Event != Denied {
		t.Fatalf("a spoofed first hop: %+v", d)
	}
	req = httptest.NewRequest("GET", "/office/x", nil)
	req.Header.Set("X-Forwarded-For", "6.6.6.6, 192.168.7.7")
	if d := g.Decide(req, "10.9.0.5:1"); d.Event != Allowed {
		t.Fatalf("the trusted proxy's hop: %+v", d)
	}
	// From an untrusted peer, X-Forwarded-For counts for nothing.
	req = httptest.NewRequest("GET", "/office/x", nil)
	req.Header.Set("X-Forwarded-For", "192.168.7.7")
	if d := g.Decide(req, "203.0.113.9:1"); d.Event != Denied {
		t.Fatalf("an untrusted peer: %+v", d)
	}
}

// Credentials: missing, wrong, right, repeated, and guessed too often.
func TestCredentials(t *testing.T) {
	r := identityRig(t)
	g := r.guard
	try := func(from string, h http.Header) Decision {
		req := httptest.NewRequest("GET", "/reports/daily", nil)
		for k, v := range h {
			req.Header[k] = v
		}
		return g.Decide(req, from+":1")
	}
	if d := try("1.1.1.1", nil); d.Event != Denied || d.Status != 401 {
		t.Fatalf("missing: %+v", d)
	}
	if d := try("1.1.1.1", http.Header{"X-Api-Key": {"billing-key-0123456789"}}); d.Event != Allowed || d.Client != "billing" {
		t.Fatalf("right: %+v", d)
	}
	if d := try("1.1.1.1", http.Header{"X-Api-Key": {"billing-key-0123456789", "other"}}); d.Status != 401 {
		t.Fatalf("two keys: %+v", d)
	}
	basic := func(u, p string) http.Header {
		req := httptest.NewRequest("GET", "/", nil)
		req.SetBasicAuth(u, p)
		return req.Header
	}
	if d := try("2.2.2.2", basic("admin", "admin-password-0123456789")); d.Event != Denied || d.Status != 403 || d.Client != "admin" {
		t.Fatalf("a known client the rule doesn't allow: %+v", d)
	}
	// Five wrong guesses, then even the right key waits.
	for i := 0; i < 5; i++ {
		if d := try("3.3.3.3", basic("admin", fmt.Sprintf("guess-%d", i))); d.Status != 401 {
			t.Fatalf("guess %d: %+v", i, d)
		}
	}
	if d := try("3.3.3.3", http.Header{"X-Api-Key": {"billing-key-0123456789"}}); d.Event != Limited || d.Status != 429 || d.RetryAfter <= 0 {
		t.Fatalf("after five guesses: %+v", d)
	}
	if d := try("4.4.4.4", http.Header{"X-Api-Key": {"billing-key-0123456789"}}); d.Event != Allowed {
		t.Fatalf("another address isn't locked: %+v", d)
	}
	r.clock.add(time.Minute)
	if d := try("3.3.3.3", http.Header{"X-Api-Key": {"billing-key-0123456789"}}); d.Event != Allowed {
		t.Fatalf("a minute later: %+v", d)
	}
	// A 401 over HTTP asks for Basic.
	resp, _ := do(t, "GET", r.url+"/reports/daily", nil, nil)
	if resp.StatusCode != 401 || !strings.HasPrefix(resp.Header.Get("Www-Authenticate"), "Basic ") {
		t.Fatalf("%d %v", resp.StatusCode, resp.Header)
	}
}

// Promise 6: no secret reaches the log, on any path a request can take.
func TestNoSecretReachesTheLog(t *testing.T) {
	secrets := []string{"billing-key-0123456789", "admin-password-0123456789", "wrong-key-secret-777", "cookie-secret-888", "query-secret-999"}
	check := func(t *testing.T, r *rig) {
		h := func(kv ...string) http.Header {
			out := http.Header{"Cookie": {"session=cookie-secret-888"}}
			for i := 0; i+1 < len(kv); i += 2 {
				out[kv[i]] = []string{kv[i+1]}
			}
			return out
		}
		basic := "Basic " + "YWRtaW46YWRtaW4tcGFzc3dvcmQtMDEyMzQ1Njc4OQ==" // admin:admin-password-0123456789
		do(t, "GET", r.url+"/reports/a?token=query-secret-999", h("X-Api-Key", "billing-key-0123456789"), nil)
		do(t, "GET", r.url+"/reports/a", h("X-Api-Key", "wrong-key-secret-777"), nil)
		do(t, "GET", r.url+"/reports/a", h("Authorization", basic), nil)
		do(t, "GET", r.url+"/open/a?password=query-secret-999", h(), nil)
		do(t, "GET", r.url+"/nothing?api_key=query-secret-999", h("X-Api-Key", "billing-key-0123456789"), nil)
		r.raw(t, "GET /open/..%2fx?k=query-secret-999 HTTP/1.1\r\nHost: a\r\nX-Api-Key: wrong-key-secret-777\r\n\r\n")
		for i := 0; i < 8; i++ {
			do(t, "GET", r.url+"/reports/a", h("X-Api-Key", "wrong-key-secret-777"), nil)
		}
		log := r.logText()
		if log == "" {
			t.Fatal("nothing logged")
		}
		for _, s := range secrets {
			if strings.Contains(log, s) {
				t.Fatalf("the log holds %q:\n%s", s, log)
			}
		}
		for _, ev := range []string{`"event":"allowed"`, `"event":"denied"`, `"event":"rejected"`, `"event":"limited"`} {
			if !strings.Contains(log, ev) {
				t.Errorf("no %s line, so that path went untested:\n%s", ev, log)
			}
		}
	}
	t.Run("run", func(t *testing.T) { check(t, identityRig(t)) })
	t.Run("dry run", func(t *testing.T) {
		r := identityRig(t, func(o *Options) { o.DryRun = true })
		check(t, r)
		if !strings.Contains(r.logText(), `"dry_run":true`) {
			t.Fatal("dry run lines aren't marked")
		}
	})
}

// A dry run forwards what it would turn away, and logs it.
func TestDryRunForwardsAndLogs(t *testing.T) {
	r := start(t, openConfig, func(o *Options) { o.DryRun = true })
	resp, _ := do(t, "GET", r.url+"/admin/users", nil, nil)
	if resp.StatusCode != 200 || r.app.count() != 1 {
		t.Fatalf("%d, app saw %d", resp.StatusCode, r.app.count())
	}
	if log := r.logText(); !strings.Contains(log, `"event":"denied","dry_run":true`) || !strings.Contains(log, `"rule":"no-admin"`) {
		t.Fatalf("log:\n%s", log)
	}
}

func TestResponseHeadersAndBodies(t *testing.T) {
	r := start(t, `
default: deny
limits: {max_body: 64KiB}
rules:
  - id: api
    when: {method: POST, path: /api/**}
    allow: true
    max_body: 1KiB
    json: true
  - id: open
    when: {path: /**}
    allow: true
response:
  set_headers: {X-Frame-Options: DENY, Strict-Transport-Security: max-age=63072000}
  remove_headers: [Server]
`)
	resp, _ := do(t, "GET", r.url+"/x", nil, nil)
	if resp.Header.Get("Server") != "" || resp.Header.Get("X-Frame-Options") != "DENY" || resp.Header.Get("Strict-Transport-Security") == "" {
		t.Fatalf("headers %v", resp.Header)
	}
	// AfterGuard's own answers carry the security headers too.
	resp, body := do(t, "POST", r.url+"/api/x", nil, []byte(`{"a":`))
	if resp.StatusCode != 400 || resp.Header.Get("X-Frame-Options") != "DENY" || strings.Contains(string(body), "json") {
		t.Fatalf("%d %v %q", resp.StatusCode, resp.Header, body)
	}
	if resp, _ := do(t, "POST", r.url+"/api/x", nil, []byte(`{"a":1}`)); resp.StatusCode != 200 || string(r.app.last().body) != `{"a":1}` {
		t.Fatalf("valid JSON: %d", resp.StatusCode)
	}
	if resp, _ := do(t, "POST", r.url+"/api/x", nil, bytes.Repeat([]byte("1"), 2000)); resp.StatusCode != 413 {
		t.Fatalf("over the rule's max_body: %d", resp.StatusCode)
	}
	before := r.app.count()
	if resp, _ := do(t, "POST", r.url+"/other", nil, bytes.Repeat([]byte("1"), 70000)); resp.StatusCode != 413 || r.app.count() != before {
		t.Fatalf("over limits max_body: %d", resp.StatusCode)
	}
	// A chunked body over the limit is cut off with 413.
	pr, pw := io.Pipe()
	go func() { pw.Write(bytes.Repeat([]byte("x"), 70000)); pw.Close() }()
	req, _ := http.NewRequest("POST", r.url+"/other", pr)
	if resp, err := client.Do(req); err != nil || resp.StatusCode != 413 {
		t.Fatalf("chunked over max_body: %v %v", resp, err)
	}
}

func TestHealthUpstreamErrorsAndConnections(t *testing.T) {
	r := start(t, "server: {max_connections: 4, max_connections_per_address: 2}\nrules:\n  - id: open\n    when: {path: /**}\n    allow: true\n")
	if resp, _ := do(t, "GET", r.url+DefaultHealthPath, nil, nil); resp.StatusCode != 200 || r.app.count() != 0 {
		t.Fatal("health check")
	}
	// Two connections from one address are held; a third is closed at once.
	var conns []net.Conn
	for i := 0; i < 2; i++ {
		c, err := net.Dial("tcp", r.addr)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
	}
	deadline := time.Now().Add(2 * time.Second)
	for r.limits.open() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	c, _ := net.Dial("tcp", r.addr)
	c.SetDeadline(time.Now().Add(2 * time.Second))
	c.Write([]byte("GET /x HTTP/1.1\r\nHost: a\r\n\r\n"))
	if n, err := c.Read(make([]byte, 10)); n != 0 || err == nil {
		t.Fatalf("a third connection got an answer: %d %v", n, err)
	}
	c.Close()
	for _, c := range conns {
		c.Close()
	}
	if !strings.Contains(r.logText(), `"event":"refused"`) {
		t.Fatal("the refused connection wasn't logged")
	}
	// The app down: a clean 502.
	up, _ := url.Parse("http://127.0.0.1:1")
	g, _ := New(Options{Config: mustLoad(t, "rules:\n  - id: open\n    when: {path: /**}\n    allow: true\n"), Upstream: up})
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	if rec.Code != 502 {
		t.Fatalf("app down: %d", rec.Code)
	}
}

func TestLogSummarisesFloods(t *testing.T) {
	var buf bytes.Buffer
	clock := &fixedClock{t: time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)}
	l := NewLog(&buf, clock.now)
	for i := 0; i < 1000; i++ {
		l.Write(Entry{Event: Denied})
	}
	l.Write(Entry{Event: Allowed})
	clock.add(time.Second)
	l.Write(Entry{Event: Limited})
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != DefaultLogPerSecond+3 || !strings.Contains(lines[len(lines)-2], `"event":"denied_summary"`) || !strings.Contains(lines[len(lines)-2], `"count":980`) {
		t.Fatalf("%d lines, end:\n%s", len(lines), strings.Join(lines[len(lines)-3:], "\n"))
	}
}

func TestLoadReportsEveryProblemInLineOrder(t *testing.T) {
	_, err := Load([]byte(`default: maybe
clients:
  - id: k
    api_key_sha256: not-a-hash
  - id: k
    basic_user: u
rules:
  - id: a
    when: {path: /x, status: 200}
    allow: true
    deny: 404
  - id: b
    when: {path: /y}
    client: [nobody]
    allow: true
    limit: {rate: 5/week}
  - id: c
    when: {path: /z}
    deny: 404
    limit: {rate: 5/m}
bogus: 1
`), "")
	if err == nil {
		t.Fatal("no error")
	}
	msg := err.Error()
	want := []string{"line 1:", "default must be deny or allow", "line 4:", "SHA-256", "line 5:", "already used", "line 8:", "exactly one of allow", "line 9:", "status and content_type",
		"line 12:", "nobody", "line 16:", "rate", "line 17:", "denies", "line 21:", "unknown key"}
	at := 0
	for _, w := range want {
		i := strings.Index(msg[at:], w)
		if i < 0 {
			t.Fatalf("missing or out of order %q in:\n%s", w, msg)
		}
		at += i
	}
}

func TestCheckWarnings(t *testing.T) {
	c := mustLoad(t, "default: allow\nrules:\n  - id: all\n    allow: true\n  - id: later\n    when: {path: /x}\n    deny: 403\n")
	w := strings.Join(c.Warnings, "\n")
	if !strings.Contains(w, "default: allow") || !strings.Contains(w, "later (line 5) can never match") || !strings.Contains(w, "no tls") {
		t.Fatal(w)
	}
}

func TestAddrKey(t *testing.T) {
	for in, want := range map[string]string{"1.2.3.4": "1.2.3.4", "::ffff:1.2.3.4": "1.2.3.4", "2001:db8::1": "2001:db8::/64", "2001:db8:0:0:ffff::9": "2001:db8::/64"} {
		if got := addrKey(netip.MustParseAddr(in)); got != want {
			t.Errorf("%s: %s, want %s", in, got, want)
		}
	}
}

func TestFixtureRuns(t *testing.T) {
	c := mustLoad(t, fmt.Sprintf(identityConfig, Hash("billing-key-0123456789"), Hash("admin-password-0123456789")))
	dir := t.TempDir()
	p := dir + "/f.yaml"
	os.WriteFile(p, []byte(`cases:
  - name: right key
    request: {path: /reports/x, headers: {X-Api-Key: billing-key-0123456789}}
    expect: {decision: allowed, client: billing, rule: reports}
  - name: a wrong expectation fails
    request: {path: /reports/x}
    expect: {decision: allowed}
`), 0o644)
	cases, err := LoadFixture(p)
	if err != nil {
		t.Fatal(err)
	}
	res, err := c.RunFixture(cases)
	if err != nil {
		t.Fatal(err)
	}
	if !res[0].Pass || res[1].Pass || !strings.Contains(res[1].Got, "status 401") {
		t.Fatalf("%+v", res)
	}
	os.WriteFile(p, []byte("cases:\n  - request: {path: x}\n    expect: {decision: maybe}\n"), 0o644)
	if _, err := LoadFixture(p); err == nil || !strings.Contains(err.Error(), "decision must be") || !strings.Contains(err.Error(), "needs a name") {
		t.Fatalf("%v", err)
	}
}
