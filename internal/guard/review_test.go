package guard

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// Regression tests for the 0.6.0 review findings, from the reviewer's own
// reproductions.

func statuses(rs []*http.Response) []int {
	var s []int
	for _, x := range rs {
		s = append(s, x.StatusCode)
	}
	return s
}

func appURIs(a *app) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var s []string
	for _, r := range a.seen {
		s = append(s, r.method+" "+r.uri)
	}
	return s
}

func reached(a *app, part string) bool {
	for _, u := range appURIs(a) {
		if strings.Contains(u, part) {
			return true
		}
	}
	return false
}

const smuggle = "POST /smuggled HTTP/1.1\r\nHost: a\r\nContent-Length: 4\r\nTransfer-Encoding: chunked\r\n\r\n0\r\n\r\n"

// H1: an Upgrade header that never got a 101 doesn't switch checks off.
func TestAnUpgradeHeaderAloneKeepsTheChecksOn(t *testing.T) {
	r := start(t, openConfig)
	rs := r.rawN(t, "GET /a HTTP/1.1\r\nHost: a\r\nUpgrade: anything\r\n\r\n"+smuggle, 2)
	if reached(r.app, "/smuggled") || (len(rs) == 2 && rs[1].StatusCode == 200) {
		t.Fatalf("statuses %v, app saw %v", statuses(rs), appURIs(r.app))
	}
}

// H2: OPTIONS * reaches the handler and is rejected, so verdicts stay in
// step; and the queue of verdicts is bounded.
func TestOptionsStarKeepsVerdictsInStep(t *testing.T) {
	r := start(t, openConfig)
	rs := r.rawN(t, "OPTIONS * HTTP/1.1\r\nHost: a\r\n\r\n"+smuggle, 2)
	if len(rs) == 0 || rs[0].StatusCode != 400 || reached(r.app, "/smuggled") {
		t.Fatalf("statuses %v, app saw %v", statuses(rs), appURIs(r.app))
	}
	tr := &tracker{maxHead: 4096}
	for i := 0; i < 10000; i++ {
		tr.feed([]byte("GET / HTTP/1.1\r\nHost: a\r\n\r\n"))
	}
	if len(tr.verdicts) > maxVerdicts || tr.state != lost {
		t.Fatalf("%d verdicts queued, state %v", len(tr.verdicts), tr.state)
	}
}

// H3: a pipelined head too long to follow loses the connection.
func TestAnOversizedPipelinedHeadIsRejected(t *testing.T) {
	r := start(t, "server: {max_header_bytes: 1KiB}\n"+openConfig)
	pad := "X-Pad: " + strings.Repeat("a", 8900) + "\r\n"
	big := "POST /big HTTP/1.1\r\nHost: a\r\n" + pad + "Content-Length: 4\r\nTransfer-Encoding: chunked\r\n\r\n0\r\n\r\n"
	rs := r.rawN(t, "GET /first HTTP/1.1\r\nHost: a\r\n\r\n"+big, 2)
	if reached(r.app, "/big") || (len(rs) == 2 && rs[1].StatusCode == 200) {
		t.Fatalf("statuses %v, app saw %v", statuses(rs), appURIs(r.app))
	}
}

// H4: chunk sizes are read exactly as Go reads them, and a failed body ends
// the connection, so a request can't hide as trailers.
func TestChunkSizesCantHideARequest(t *testing.T) {
	for _, size := range []string{" 0", "+5", "00000000000000005", "5\n", "5 ;x", "5\r;x"} {
		r := start(t, openConfig)
		req := "POST /a HTTP/1.1\r\nHost: a\r\nTransfer-Encoding: chunked\r\n\r\n" + size + "\r\nhello\r\n0\r\n\r\n" + smuggle
		rs := r.rawN(t, req, 2)
		if reached(r.app, "/smuggled") {
			t.Fatalf("size %q: statuses %v, app saw %v", size, statuses(rs), appURIs(r.app))
		}
	}
	// What Go accepts is followed: trailing spaces, extensions, trailers.
	tr := &tracker{maxHead: 4096}
	tr.feed([]byte("POST /a HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n5;x=1\r\nhello\r\n3 \t\r\nabc\r\n0\r\nT: 1\r\n\r\nGET /b HTTP/1.1\r\n\r\n"))
	if tr.state != inHead || len(tr.verdicts) != 2 {
		t.Fatalf("state %v, %d verdicts", tr.state, len(tr.verdicts))
	}
	// A chunk not followed by exactly CRLF loses the connection.
	tr = &tracker{maxHead: 4096}
	tr.feed([]byte("POST /a HTTP/1.1\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\n0\r\n\r\n"))
	if tr.state != lost {
		t.Fatalf("state %v", tr.state)
	}
}

// A request on a connection the tracker can't vouch for is rejected.
func TestARequestWithoutAVerdictIsRejected(t *testing.T) {
	r := start(t, openConfig)
	c, _ := net.Dial("tcp", r.addr)
	defer c.Close()
	// Find the server side's tracker through a request, then empty its queue
	// as if a verdict had been lost.
	g := r.guard
	rec := httptest.NewRecorder()
	tr := &tracker{maxHead: 4096}
	req := httptest.NewRequest("GET", "/x", nil)
	req = req.WithContext(connContext(req.Context(), tr))
	g.ServeHTTP(rec, req)
	if rec.Code != 400 || r.app.count() != 0 || tr.state != lost {
		t.Fatalf("%d, app saw %d, state %v", rec.Code, r.app.count(), tr.state)
	}
}

// M1: no upgrade unless a rule names it, and never h2c.
func TestH2CNeverTunnelsPastTheRules(t *testing.T) {
	var mu sync.Mutex
	var tunnelled string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "h2c" {
			w.WriteHeader(200)
			return
		}
		c, brw, _ := w.(http.Hijacker).Hijack()
		defer c.Close()
		brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: h2c\r\n\r\n")
		brw.Flush()
		c.SetReadDeadline(time.Now().Add(time.Second))
		line, _ := brw.ReadString('\n')
		mu.Lock()
		tunnelled = line
		mu.Unlock()
	}))
	defer backend.Close()
	up, _ := url.Parse(backend.URL)
	r := start(t, "rules:\n  - id: no-admin\n    when: {path: /admin/**}\n    deny: 403\n  - id: status\n    when: {path: /status}\n    allow: true\n", func(o *Options) { o.Upstream = up })
	rs := r.rawN(t, "GET /status HTTP/1.1\r\nHost: a\r\nConnection: Upgrade, HTTP2-Settings\r\nUpgrade: h2c\r\nHTTP2-Settings: AAMAAABkAAQAAP__\r\n\r\nGET /admin/users HTTP/1.1\r\nHost: a\r\n\r\n", 2)
	mu.Lock()
	defer mu.Unlock()
	if len(rs) == 0 || rs[0].StatusCode != 400 || strings.Contains(tunnelled, "/admin") {
		t.Fatalf("statuses %v, tunnelled %q", statuses(rs), tunnelled)
	}
	if _, err := Load([]byte("rules:\n  - id: a\n    allow: true\n    upgrade: [h2c]\n"), ""); err == nil || !strings.Contains(err.Error(), "h2c is never allowed") {
		t.Fatalf("upgrade: [h2c] accepted: %v", err)
	}
}

// M2: path, method and address override headers never reach the app.
func TestOverrideHeadersNeverReachTheApp(t *testing.T) {
	r := start(t, "strip_headers: [X-Internal-Role]\n"+openConfig)
	r.raw(t, "GET /public HTTP/1.1\r\nHost: a\r\nX-Original-URL: /admin/users\r\nX-Rewrite-URL: /admin/users\r\nX-HTTP-Method-Override: DELETE\r\nTrue-Client-IP: 10.0.0.1\r\nX-Client-IP: 10.0.0.1\r\nCF-Connecting-IP: 10.0.0.1\r\nX-Internal-Role: admin\r\n\r\n")
	h := r.app.last().header
	for _, n := range []string{"X-Original-Url", "X-Rewrite-Url", "X-Http-Method-Override", "True-Client-Ip", "X-Client-Ip", "Cf-Connecting-Ip", "X-Internal-Role"} {
		if h.Get(n) != "" {
			t.Errorf("%s reached the app: %q", n, h.Get(n))
		}
	}
}

// M3: a flood of new addresses neither resets a per-rule limit nor unlocks
// an address locked out for failed logins.
func TestEvictionResetsNoLimit(t *testing.T) {
	r := start(t, fmt.Sprintf(`
trusted_proxies: [127.0.0.1]
limit_table: 100
clients:
  - id: c
    api_key_sha256: %s
rules:
  - id: global
    when: {path: /global}
    allow: true
    limit: {rate: 1/h, burst: 1, per: rule}
  - id: login
    when: {path: /login}
    client: [c]
    allow: true
`, Hash("right-key-0123456789")))
	get := func(path, from, key string) int {
		h := http.Header{"X-Forwarded-For": {from}}
		if key != "" {
			h.Set("X-Api-Key", key)
		}
		resp, _ := do(t, "GET", r.url+path, h, nil)
		return resp.StatusCode
	}
	if get("/global", "10.9.9.9", "") != 200 || get("/global", "10.9.9.9", "") != 429 {
		t.Fatal("setup: global limit")
	}
	for i := 0; i < 6; i++ {
		get("/login", "10.0.0.1", "wrong")
	}
	if get("/login", "10.0.0.1", "wrong") != 429 {
		t.Fatal("setup: lockout")
	}
	for i := 0; i < 300; i++ {
		get("/login", fmt.Sprintf("10.1.%d.%d", i/250, i%250+1), "wrong")
	}
	if g, l := get("/global", "10.9.9.9", ""), get("/login", "10.0.0.1", "right-key-0123456789"); g != 429 || l != 429 {
		t.Fatalf("after the flood: /global %d, locked address %d", g, l)
	}
}

// M4: connection limits count an IPv6 /64 as one address, and leave
// trusted proxies to the total.
type fakeConn struct {
	net.Conn
	addr net.Addr
}

func (f fakeConn) RemoteAddr() net.Addr { return f.addr }
func (f fakeConn) Close() error         { return nil }

type fakeLn struct {
	net.Listener
	n    int
	from func(n int) string
}

func (f *fakeLn) Accept() (net.Conn, error) {
	f.n++
	return fakeConn{addr: &net.TCPAddr{IP: net.ParseIP(f.from(f.n)), Port: 1}}, nil
}

func TestConnectionLimitsPerSlash64AndProxies(t *testing.T) {
	c := &Config{MaxConns: 1000, MaxConnsPerAddr: 1}
	refused := 0
	l := newListener(&fakeLn{from: func(n int) string { return fmt.Sprintf("2001:db8::%x", n) }}, c, func(string) { refused++ })
	go func() {
		for i := 0; i < 50; i++ {
			l.Accept()
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for l.open() < 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	if l.open() != 1 {
		t.Fatalf("one /64 holds %d connections", l.open())
	}
	c = &Config{MaxConns: 1000, MaxConnsPerAddr: 1, TrustedProxies: mustLoad(t, "trusted_proxies: [10.0.0.1]\nrules:\n  - id: a\n    allow: true\n").TrustedProxies}
	l = newListener(&fakeLn{from: func(int) string { return "10.0.0.1" }}, c, nil)
	for i := 0; i < 20; i++ {
		l.Accept()
	}
	if l.open() != 20 {
		t.Fatalf("a trusted proxy holds %d connections, want 20", l.open())
	}
}

// M5: with ignore_case, paths outside ASCII are rejected.
func TestIgnoreCaseRejectsNonASCIIPaths(t *testing.T) {
	r := start(t, "paths: {ignore_case: true}\nrules:\n  - id: no\n    when: {path: /settings/**}\n    deny: 403\n  - id: open\n    when: {path: /**}\n    allow: true\n")
	if rs := r.raw(t, "GET /%C5%BFettings/x HTTP/1.1\r\nHost: a\r\n\r\n"); len(rs) != 1 || rs[0].StatusCode != 400 {
		t.Fatalf("statuses %v", statuses(rs))
	}
}

// M6: methods must be upper case.
func TestLowerCaseMethodsAreRejected(t *testing.T) {
	r := start(t, "rules:\n  - id: nodelete\n    when: {method: DELETE}\n    deny: 403\n  - id: open\n    when: {path: /**}\n    allow: true\n")
	if rs := r.raw(t, "delete /x HTTP/1.1\r\nHost: a\r\n\r\n"); len(rs) != 1 || rs[0].StatusCode != 400 || r.app.count() != 0 {
		t.Fatalf("statuses %v, app saw %v", statuses(rs), appURIs(r.app))
	}
}

// M7: headers spelled with underscores never reach the app.
func TestUnderscoreHeadersAreDropped(t *testing.T) {
	r := start(t, "rules:\n  - id: noadmin\n    when: {headers: {X-Role: admin}}\n    deny: 403\n  - id: open\n    when: {path: /**}\n    allow: true\n")
	r.raw(t, "GET /x HTTP/1.1\r\nHost: a\r\nX_Role: admin\r\n\r\n")
	for name := range r.app.last().header {
		if strings.Contains(name, "_") {
			t.Fatalf("%s reached the app", name)
		}
	}
	if rs := r.raw(t, "GET /x HTTP/1.1\r\nHost: a\r\nX-Role: admin\r\n\r\n"); rs[0].StatusCode != 403 {
		t.Fatalf("the hyphen spelling: %d", rs[0].StatusCode)
	}
}

// L1: every kind of event over the budget gets a summary.
func TestLogSummarisesEveryEvent(t *testing.T) {
	var buf strings.Builder
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	lg := NewLog(&buf, func() time.Time { return now })
	for i := 0; i < 20; i++ {
		lg.Write(Entry{Event: Denied})
	}
	for i := 0; i < 7; i++ {
		lg.Write(Entry{Event: "upstream_error"})
	}
	lg.Flush()
	if !strings.Contains(buf.String(), `"event":"upstream_error_summary"`) || !strings.Contains(buf.String(), `"count":7`) {
		t.Fatal(buf.String())
	}
}

// L2: configs that mean something dangerous are warned about or refused.
func TestDangerousConfigs(t *testing.T) {
	c := mustLoad(t, "trusted_proxies: [0.0.0.0/0, ::/0]\nrules:\n  - id: a\n    allow: true\n    when: {path: /x}\n")
	if w := strings.Join(c.Warnings, "\n"); !strings.Contains(w, "0.0.0.0/0 is so wide") || !strings.Contains(w, "::/0 is so wide") {
		t.Fatal(w)
	}
	c = mustLoad(t, "rules:\n  - id: v4\n    from: ['::ffff:10.0.0.0/104']\n    deny: 403\n  - id: open\n    allow: true\n    when: {path: /**}\n")
	g, _ := New(Options{Config: c, Upstream: &url.URL{Scheme: "http", Host: "127.0.0.1:1"}})
	if d := g.Decide(httptest.NewRequest("GET", "/x", nil), "10.1.2.3:1"); d.Event != Denied {
		t.Fatalf("an IPv4-mapped range: %+v", d)
	}
	if _, err := Load([]byte("rules:\n  - id: v4\n    from: ['::ffff:0:0/80']\n    deny: 403\n"), ""); err == nil {
		t.Fatal("a range across IPv4 and IPv6 was accepted")
	}
	c = mustLoad(t, "rules:\n  - id: all\n    json: true\n    allow: true\n  - id: later\n    when: {path: /x}\n    deny: 403\n")
	if w := strings.Join(c.Warnings, "\n"); !strings.Contains(w, "later (line 5) can never match") {
		t.Fatal(w)
	}
	c = mustLoad(t, "rules:\n  - id: admin\n    when: {path: /admin/**}\n    deny: 403\n  - id: users\n    when: {method: GET, path: /admin/users}\n    allow: true\n  - id: other\n    when: {path: /administrator}\n    allow: true\n")
	if w := strings.Join(c.Warnings, "\n"); !strings.Contains(w, "users (line 5) can never match") || strings.Contains(w, "other") {
		t.Fatal(w)
	}
}

// L3: two Authorization headers count as a failed login and go nowhere.
func TestTwoAuthorizationHeaders(t *testing.T) {
	r := start(t, fmt.Sprintf("credentials: {keep: true}\nclients:\n  - id: c\n    basic_user: u\n    basic_password_sha256: %s\nrules:\n  - id: all\n    when: {path: /**}\n    client: [any]\n    allow: true\n", Hash("basic-pass-0123456789")))
	rs := r.raw(t, "GET /x HTTP/1.1\r\nHost: a\r\nAuthorization: Basic dTpiYXNpYy1wYXNzLTAxMjM0NTY3ODk=\r\nAuthorization: Basic YWRtaW46eA==\r\n\r\n")
	if len(rs) != 1 || rs[0].StatusCode != 401 || r.app.count() != 0 {
		t.Fatalf("statuses %v, app saw %d", statuses(rs), r.app.count())
	}
}

// L7: concurrent guesses from one address get no more comparisons than the
// failed-login burst.
func TestConcurrentGuessesStayWithinTheLimit(t *testing.T) {
	c := mustLoad(t, fmt.Sprintf("clients:\n  - id: c\n    api_key_sha256: %s\nrules:\n  - id: all\n    when: {path: /**}\n    client: [any]\n    allow: true\n", Hash("right-key-0123456789")))
	fixed := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	g, _ := New(Options{Config: c, Upstream: &url.URL{Scheme: "http", Host: "127.0.0.1:1"}, Now: func() time.Time { return fixed }})
	var wg sync.WaitGroup
	var mu sync.Mutex
	judged := 0
	begin := make(chan struct{})
	for i := 0; i < 400; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/x", nil)
			req.Header.Set("X-Api-Key", "guess")
			<-begin
			if d := g.Decide(req, "10.0.0.1:1"); d.Status == 401 {
				mu.Lock()
				judged++
				mu.Unlock()
			}
		}()
	}
	close(begin)
	wg.Wait()
	if judged != 5 {
		t.Fatalf("%d guesses compared, want 5", judged)
	}
}

// L4: HTTP/1.0 with Transfer-Encoding is rejected.
func TestHTTP10WithTransferEncoding(t *testing.T) {
	r := start(t, openConfig)
	rs := r.rawN(t, "POST /x HTTP/1.0\r\nConnection: keep-alive\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n0\r\n\r\n", 2)
	if len(rs) == 0 || rs[0].StatusCode != 400 || r.app.count() != 0 {
		t.Fatalf("statuses %v, app saw %v", statuses(rs), appURIs(r.app))
	}
}

// L5: answers have a write deadline.
func TestServerHasAWriteTimeout(t *testing.T) {
	c := mustLoad(t, "server: {write_timeout: 30s}\nrules:\n  - id: a\n    allow: true\n    when: {path: /x}\n")
	g, _ := New(Options{Config: c, Upstream: &url.URL{Scheme: "http", Host: "127.0.0.1:1"}})
	if s := g.Server(); s.WriteTimeout != c.HeaderTimeout+c.BodyTimeout+30*time.Second || !s.DisableGeneralOptionsHandler {
		t.Fatalf("WriteTimeout %v", s.WriteTimeout)
	}
}

// A panic while judging a request answers 500 and forwards nothing.
func TestAFailureWhileJudgingForwardsNothing(t *testing.T) {
	r := start(t, openConfig)
	r.guard.cfg.Rules[0] = nil // makes decide panic
	resp, _ := do(t, "GET", r.url+"/x", nil, nil)
	if resp.StatusCode != 500 || r.app.count() != 0 {
		t.Fatalf("%d, app saw %d", resp.StatusCode, r.app.count())
	}
}
