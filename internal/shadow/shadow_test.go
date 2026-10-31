package shadow_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aftermarket-dev/aftermarket/internal/audit"
	"github.com/aftermarket-dev/aftermarket/internal/fixture"
	"github.com/aftermarket-dev/aftermarket/internal/record"
	"github.com/aftermarket-dev/aftermarket/internal/replay"
	"github.com/aftermarket-dev/aftermarket/internal/shadow"
	tk "github.com/aftermarket-dev/aftermarket/internal/testkit"
)

func mustURL(t testing.TB, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

type harness struct {
	url   string
	sh    *shadow.Shadow
	audit *tk.AuditSink
	dir   string
}

// start runs a shadow proxy in front of primary, copying to candidate.
func start(t testing.TB, primary, candidate string, o shadow.Options) *harness {
	t.Helper()
	h := &harness{audit: &tk.AuditSink{}, dir: t.TempDir()}
	o.Primary, o.Candidate = mustURL(t, primary), mustURL(t, candidate)
	o.Audit = audit.New(h.audit)
	if o.ReportPath == "" {
		o.ReportPath = filepath.Join(h.dir, "shadow.md")
	}
	if o.JSONPath == "" {
		o.JSONPath = filepath.Join(h.dir, "shadow.json")
	}
	if o.HealthPath == "" {
		o.HealthPath = "/_aftermarket/healthz"
	}
	sh, err := shadow.New(o)
	if err != nil {
		t.Fatal(err)
	}
	h.sh = sh
	srv := httptest.NewServer(sh)
	t.Cleanup(func() { srv.Close(); sh.Close() })
	h.url = srv.URL
	return h
}

// stop closes the shadow and returns the final report as written to disk.
func (h *harness) stop(t testing.TB) replay.Report {
	t.Helper()
	if err := h.sh.Close(); err != nil {
		t.Fatal(err)
	}
	var r replay.Report
	data, err := os.ReadFile(filepath.Join(h.dir, "shadow.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// waitIdle waits until every queued copy was compared.
func (h *harness) waitIdle(t testing.TB, mirrored int64) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		r := h.sh.Report()
		if r.Shadow.Mirrored >= mirrored && int64(r.Same+r.Diff+r.Errors) >= r.Shadow.Mirrored && h.sh.Idle() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	r := h.sh.Report()
	t.Fatalf("not idle: %+v %+v", r.Shadow, r.Summary())
}

// randomUpstream answers with a status, headers and body derived from the
// request, so two calls with the same request get the same answer.
func randomUpstream(t testing.TB, rec *tk.Recorder, salt int64) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := rec.Add(r)
		rs := rand.New(rand.NewSource(int64(len(got.Body))*7919 + int64(len(got.URI)) + salt))
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
	t.Cleanup(srv.Close)
	return srv
}

// unruly is a candidate that misbehaves in turn: different answers, errors,
// slow answers, dropped connections and answers that never come.
func unruly(t testing.TB) *httptest.Server {
	var n atomic.Int64
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch n.Add(1) % 5 {
		case 0:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"other":true}`)
		case 1:
			http.Error(w, "candidate broke", http.StatusInternalServerError)
		case 2:
			time.Sleep(300 * time.Millisecond)
			fmt.Fprint(w, "late")
		case 3:
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
		case 4:
			select { // a server can't see a client leave while a body is unread
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
			}
		}
	}))
	srv.Config.ReadTimeout = 10 * time.Second
	srv.Start()
	t.Cleanup(func() { srv.CloseClientConnections(); srv.Close() })
	return srv
}

// The main promise: clients get byte for byte what the primary sent, with
// shadowing on, whatever the candidate does.
func TestAnswersAreByteIdenticalWithShadowing(t *testing.T) {
	seed := time.Now().UnixNano()
	if s := os.Getenv("AFTERMARKET_SEED"); s != "" {
		seed, _ = strconv.ParseInt(s, 10, 64)
	}
	t.Logf("seed %d (set AFTERMARKET_SEED to repeat)", seed)
	rec := &tk.Recorder{}
	up := randomUpstream(t, rec, 0)
	cand := unruly(t)
	h := start(t, up.URL, cand.URL, shadow.Options{IncludeWrites: true, CandidateTimeout: 200 * time.Millisecond, Queue: 8, Workers: 2})

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
		if rs.Intn(3) == 0 {
			hdr.Set("Accept-Encoding", "gzip")
		}
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
			t.Fatalf("%s: primary saw %s %s (%d bytes), want %s %s (%d bytes)", ctx, pReq.Method, pReq.URI, len(pReq.Body), dReq.Method, dReq.URI, len(dReq.Body))
		}
		if ok, diff := tk.HeadersEqual(dReq.Header, pReq.Header); !ok {
			t.Fatalf("%s: request headers differ: %s", ctx, diff)
		}
		if direct.Status != proxied.Status || !bytes.Equal(direct.Body, proxied.Body) {
			t.Fatalf("%s: answer differs: %d vs %d", ctx, direct.Status, proxied.Status)
		}
		if ok, diff := tk.HeadersEqual(direct.Header, proxied.Header); !ok {
			t.Fatalf("%s: answer headers differ: %s", ctx, diff)
		}
	}
	r := h.stop(t)
	s := r.Shadow
	if s.Requests != n {
		t.Fatalf("%d requests counted, want %d", s.Requests, n)
	}
	// Every request is accounted for: copied, dropped or not copied for a reason.
	if got := s.Mirrored + s.Dropped + s.NotMirrored + s.SampledOut + s.WritesNotSent; got != n {
		t.Fatalf("accounted for %d of %d requests: %+v", got, n, *s)
	}
	if int64(r.Same+r.Diff+r.Errors)+s.Unfinished+s.NotCompared != s.Mirrored {
		t.Fatalf("%d compared and %d unfinished of %d copied: %+v", r.Same+r.Diff+r.Errors, s.Unfinished, s.Mirrored, *s)
	}
	if r.Diff == 0 || r.Errors == 0 {
		t.Fatalf("the unruly candidate should give differences and errors: %s", r.Summary())
	}
	md, _ := os.ReadFile(filepath.Join(h.dir, "shadow.md"))
	js, _ := os.ReadFile(filepath.Join(h.dir, "shadow.json"))
	if bytes.Contains(md, []byte("secret-token")) || bytes.Contains(js, []byte("secret-token")) {
		t.Fatal("a credential reached the report")
	}
}

type latencies []time.Duration

func (l latencies) pct(p float64) time.Duration {
	s := append(latencies(nil), l...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[int(float64(len(s)-1)*p)]
}

func timeRequests(t *testing.T, base string, n int, check func(i int, r tk.Response)) latencies {
	var out latencies
	for i := 0; i < n; i++ {
		t0 := time.Now()
		r := tk.Do(t, "GET", base+"/item?id="+strconv.Itoa(i), nil, nil)
		out = append(out, time.Since(t0))
		check(i, r)
	}
	return out
}

func deadURL(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return "http://" + addr
}

// A dead, slow, hanging or failing candidate never changes a primary answer
// and adds no wait: no request may take anywhere near the candidate's delay.
func TestBadCandidatesAddNoLatency(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":%q}`, r.URL.Query().Get("id"))
	}))
	defer primary.Close()
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Second)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":%q}`, r.URL.Query().Get("id"))
	}))
	defer slow.Close()
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer func() { hang.CloseClientConnections(); hang.Close() }()
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer failing.Close()
	reset := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, _ := w.(http.Hijacker).Hijack()
		conn.Close()
	}))
	defer reset.Close()
	same := httptest.NewServer(primary.Config.Handler)
	defer same.Close()

	const n = 150
	want := func(i int, r tk.Response) {
		if r.Status != 200 || string(r.Body) != fmt.Sprintf(`{"id":"%d"}`, i) {
			t.Fatalf("request %d: got %d %q", i, r.Status, r.Body)
		}
	}
	for _, c := range []struct{ name, url string }{
		{"same", same.URL}, {"dead", deadURL(t)}, {"slow", slow.URL}, {"hanging", hang.URL}, {"failing", failing.URL}, {"reset", reset.URL},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := start(t, primary.URL, c.url, shadow.Options{CandidateTimeout: 2 * time.Second, Queue: 4, Workers: 1})
			lat := timeRequests(t, h.url, n, want)
			if max := lat.pct(1); max > 400*time.Millisecond {
				t.Fatalf("slowest request took %s; the candidate must never be waited for (p50 %s)", max, lat.pct(0.5))
			}
			t.Logf("p50 %s p99 %s max %s", lat.pct(0.5), lat.pct(0.99), lat.pct(1))
			r := h.sh.Report()
			if r.Shadow.Mirrored+r.Shadow.Dropped != n {
				t.Fatalf("%d copied and %d dropped of %d", r.Shadow.Mirrored, r.Shadow.Dropped, n)
			}
			switch c.name {
			case "same":
				h.waitIdle(t, r.Shadow.Mirrored)
				if r := h.sh.Report(); r.Diff != 0 || r.Errors != 0 {
					t.Fatalf("a candidate that answers the same gave %s", r.Summary())
				}
			case "slow", "hanging":
				if r.Shadow.Dropped == 0 {
					t.Fatal("a slow candidate with a queue of 4 should have dropped copies")
				}
			}
		})
	}
}

// When the queue is full, copies are dropped and counted, never waited for.
func TestFullQueueDropsAndCounts(t *testing.T) {
	release := make(chan struct{})
	var got atomic.Int64
	cand := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Add(1)
		<-release
		fmt.Fprint(w, "ok")
	}))
	defer cand.Close()
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") }))
	defer primary.Close()
	h := start(t, primary.URL, cand.URL, shadow.Options{Queue: 2, Workers: 1, CandidateTimeout: 10 * time.Second})
	tk.Do(t, "GET", h.url+"/0", nil, nil)
	for got.Load() == 0 { // the one worker is now busy
		time.Sleep(time.Millisecond)
	}
	for i := 1; i < 10; i++ {
		if r := tk.Do(t, "GET", h.url+"/"+strconv.Itoa(i), nil, nil); string(r.Body) != "ok" {
			t.Fatal(r)
		}
	}
	r := h.sh.Report()
	if r.Shadow.Mirrored != 3 || r.Shadow.Dropped != 7 {
		t.Fatalf("mirrored %d dropped %d, want 3 and 7", r.Shadow.Mirrored, r.Shadow.Dropped)
	}
	close(release)
	r = h.stop(t)
	if r.Same != 3 || r.Shadow.Dropped != 7 {
		t.Fatalf("after stop: %s, %d dropped", r.Summary(), r.Shadow.Dropped)
	}
}

// Sampling takes an exact share by counting; writes need --include-writes.
func TestSamplingAndWrites(t *testing.T) {
	var got atomic.Int64
	cand := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got.Add(1); fmt.Fprint(w, "ok") }))
	defer cand.Close()
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") }))
	defer primary.Close()
	h := start(t, primary.URL, cand.URL, shadow.Options{Sample: 0.1})
	for i := 0; i < 100; i++ {
		tk.Do(t, "GET", h.url+"/x", nil, nil)
	}
	for i := 0; i < 5; i++ {
		tk.Do(t, "POST", h.url+"/x", nil, []byte("{}"))
	}
	tk.Do(t, "GET", h.url+"/_aftermarket/healthz", nil, nil)
	r := h.stop(t)
	s := r.Shadow
	if s.Mirrored != 10 || s.SampledOut != 90 || s.WritesNotSent != 5 || s.Requests != 105 || got.Load() != 10 || r.Same != 10 {
		t.Fatalf("got %+v, %d sent to the candidate, %s", *s, got.Load(), r.Summary())
	}
	if _, err := shadow.New(shadow.Options{Primary: mustURL(t, primary.URL), Candidate: mustURL(t, cand.URL), Sample: 1.5}); err == nil {
		t.Fatal("a sample of 1.5 was accepted")
	}
}

// Writes with --include-writes reach the candidate with their body and
// credentials, exactly as the primary got them.
func TestCandidateGetsTheRequestAsSent(t *testing.T) {
	prec, crec := &tk.Recorder{}, &tk.Recorder{}
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { prec.Add(r); w.WriteHeader(201) }))
	defer primary.Close()
	cand := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { crec.Add(r); w.WriteHeader(201) }))
	defer cand.Close()
	h := start(t, primary.URL, cand.URL, shadow.Options{IncludeWrites: true})
	body := []byte(`{"name":"Zoë"}`)
	tk.Do(t, "PUT", h.url+"/api/a%20b?api_key=k1&x=1;y=2", http.Header{"Authorization": {"Bearer real"}, "Content-Type": {"application/json"}, "X-Custom": {"1", "2"}}, body)
	h.waitIdle(t, 1)
	p, c := prec.Last(), crec.Last()
	if c.Method != "PUT" || c.URI != p.URI || !bytes.Equal(c.Body, body) || c.Header.Get("Authorization") != "Bearer real" ||
		strings.Join(c.Header.Values("X-Custom"), ",") != "1,2" || c.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("candidate got %s %s %q %v; primary got %s", c.Method, c.URI, c.Body, c.Header, p.URI)
	}
}

// Differences found live are the ones replay finds on the same traffic.
func TestLiveDifferencesMatchReplay(t *testing.T) {
	primaryH := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":%s,"name":"Ann","tags":["a","b"],"price":2.50}`, r.URL.Query().Get("id"))
		case "/gone":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"ok":true}`)
		case "/text":
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "hello world")
		case "/gz":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Encoding", "gzip")
			w.Write(tk.Gzip([]byte(`{"a":1,"b":2}`)))
		default:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"same":1.0}`)
		}
	})
	candH := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			fmt.Fprintf(w, `{"price":2.5,"tags":["a"],"id":%s,"full_name":"Ann"}`, r.URL.Query().Get("id"))
		case "/gone":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error":{"code":"gone"}}`)
		case "/text":
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprint(w, "hello there")
		case "/gz":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"b":2,"a":3}`)
		default:
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"same":1}`)
		}
	})
	primary := httptest.NewServer(primaryH)
	defer primary.Close()
	cand := httptest.NewServer(candH)
	defer cand.Close()
	reqs := []string{"/user?id=7", "/gone", "/text", "/gz", "/other", "/user?id=8"}
	send := func(base string) {
		for _, p := range reqs {
			tk.Do(t, "GET", base+p, http.Header{"Accept-Encoding": {"gzip"}, "Accept": {"application/json"}}, nil)
		}
	}
	ignore := []replay.Pattern{{"tags", "*"}}

	// Live.
	h := start(t, primary.URL, cand.URL, shadow.Options{Workers: 1, Ignore: ignore})
	send(h.url)
	live := h.stop(t)

	// Recorded, then replayed.
	var buf bytes.Buffer
	rec, err := record.New(record.Options{Upstream: mustURL(t, primary.URL), Out: &buf})
	if err != nil {
		t.Fatal(err)
	}
	rsrv := httptest.NewServer(rec)
	send(rsrv.URL)
	rsrv.Close()
	f, err := fixture.Parse(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Cases) != len(reqs) {
		t.Fatalf("%d cases recorded", len(f.Cases))
	}
	rep, err := replay.Run(t.Context(), f, replay.Options{A: mustURL(t, primary.URL), B: mustURL(t, cand.URL), Ignore: ignore})
	if err != nil {
		t.Fatal(err)
	}
	if live.Same != rep.Same || live.Diff != rep.Diff || live.Errors != rep.Errors || live.Same+live.Diff != len(reqs) {
		t.Fatalf("live %s, replay %s", live.Summary(), rep.Summary())
	}
	var want []replay.CaseReport
	for _, c := range rep.Cases {
		if c.Outcome != replay.Same {
			want = append(want, c)
		}
	}
	if len(live.Cases) != len(want) {
		t.Fatalf("live listed %d cases, replay %d", len(live.Cases), len(want))
	}
	for i := range want {
		l, r := live.Cases[i], want[i]
		if l.Name != r.Name || l.Method != r.Method || l.Path != r.Path || l.StatusA != r.StatusA || l.StatusB != r.StatusB ||
			strings.Join(l.Diffs, "\n") != strings.Join(r.Diffs, "\n") {
			t.Errorf("case %d:\nlive   %+v\nreplay %+v", i, l, r)
		}
	}
	if !strings.Contains(strings.Join(live.Cases[0].Diffs, "|"), "full_name: only in B") {
		t.Fatalf("expected differences missing: %v", live.Cases[0].Diffs)
	}
}

// No credential, listed query value, listed header or listed JSON value
// reaches either report, while the differences are still reported.
func TestCredentialsNeverReachTheReport(t *testing.T) {
	cfg, err := record.Load([]byte(`version: 1
redact:
  headers: [X-Session]
  query: [email]
  json: [password, user.*.ssn]
`))
	if err != nil {
		t.Fatal(err)
	}
	var seenAuth atomic.Value
	answer := func(who string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if who == "cand" {
				seenAuth.Store(r.Header.Get("Authorization"))
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Set-Cookie", "sid=cookie-"+who)
			w.Header().Set("X-Session", "session-"+who)
			fmt.Fprintf(w, `{"password":"pw-%s","user":{"a":{"ssn":"ssn-%s","name":"name-%s"}},"api_key":"%s"}`, who, who, who, r.URL.Query().Get("api_key"))
		}
	}
	primary := httptest.NewServer(answer("prim"))
	defer primary.Close()
	cand := httptest.NewServer(answer("cand"))
	defer cand.Close()
	h := start(t, primary.URL, cand.URL, shadow.Options{Config: cfg, Headers: []string{"Content-Type", "Set-Cookie", "X-Session"}})
	tk.Do(t, "GET", h.url+"/me?api_key=key-1&email=mail-1&token=tok-1&page=2", http.Header{"Authorization": {"Bearer auth-1"}, "Cookie": {"c=cookie-req"}}, nil)
	r := h.stop(t)
	if seenAuth.Load() != "Bearer auth-1" {
		t.Fatalf("the candidate got Authorization %q, want the real one", seenAuth.Load())
	}
	if r.Diff != 1 || len(r.Cases) != 1 {
		t.Fatalf("%s", r.Summary())
	}
	c := r.Cases[0]
	diffs := strings.Join(c.Diffs, "\n")
	for _, w := range []string{"header Set-Cookie: differs (REDACTED)", "header X-Session: differs (REDACTED)", "password: differs (REDACTED)",
		"user.a.ssn: differs (REDACTED)", `user.a.name: "name-prim" vs "name-cand"`} {
		if !strings.Contains(diffs, w) {
			t.Errorf("missing %q in\n%s", w, diffs)
		}
	}
	if c.Path != "/me?api_key=REDACTED&email=REDACTED&token=REDACTED&page=2" {
		t.Errorf("path %q", c.Path)
	}
	md, _ := os.ReadFile(filepath.Join(h.dir, "shadow.md"))
	js, _ := os.ReadFile(filepath.Join(h.dir, "shadow.json"))
	for _, secret := range []string{"key-1", "mail-1", "tok-1", "auth-1", "cookie-", "session-", "pw-", "ssn-"} {
		if bytes.Contains(md, []byte(secret)) || bytes.Contains(js, []byte(secret)) || strings.Contains(h.audit.String(), secret) {
			t.Errorf("%q reached the report or the log", secret)
		}
	}
}

// The report is rewritten while traffic runs, each time in one step.
func TestReportIsWrittenWhileRunning(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "a") }))
	defer primary.Close()
	cand := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "b") }))
	defer cand.Close()
	h := start(t, primary.URL, cand.URL, shadow.Options{ReportEvery: 20 * time.Millisecond})
	md := filepath.Join(h.dir, "shadow.md")
	if data, err := os.ReadFile(md); err != nil || !bytes.Contains(data, []byte("0 case(s)")) {
		t.Fatalf("no empty report at start: %v %q", err, data)
	}
	for i := 0; i < 3; i++ {
		tk.Do(t, "GET", h.url+"/x", nil, nil)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, _ := os.ReadFile(md)
		if bytes.Contains(data, []byte("3 case(s): 0 same, 3 different")) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("report not updated while running:\n%s", data)
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.stop(t)
	entries, _ := os.ReadDir(h.dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
	data, _ := os.ReadFile(md)
	for _, w := range []string{"# Shadow report", "3 request(s) served from A; 3 copied to B", "body: 1 bytes vs 1 bytes, first difference at byte 0"} {
		if !bytes.Contains(data, []byte(w)) {
			t.Errorf("report lacks %q:\n%s", w, data)
		}
	}
}

// Exchanges that can't be compared safely pass through and are counted with
// a reason; listing stops at MaxListed while the counts go on.
func TestNotMirroredAndListingLimit(t *testing.T) {
	big := bytes.Repeat([]byte("x"), 2000)
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/big":
			w.Write(big)
		case "/br":
			w.Header().Set("Content-Encoding", "br")
			w.Write([]byte("not really brotli"))
		default:
			fmt.Fprint(w, "a")
		}
	}))
	defer primary.Close()
	cand := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "b") }))
	defer cand.Close()
	cfg, err := record.Load([]byte("version: 1\nlimits: {max_body: 1000}\n"))
	if err != nil {
		t.Fatal(err)
	}
	h := start(t, primary.URL, cand.URL, shadow.Options{Config: cfg, MaxListed: 2, IncludeWrites: true})
	if r := tk.Do(t, "GET", h.url+"/big", nil, nil); !bytes.Equal(r.Body, big) {
		t.Fatal("big answer changed")
	}
	if r := tk.Do(t, "POST", h.url+"/x", nil, big); string(r.Body) != "a" {
		t.Fatal("answer changed")
	}
	if r := tk.Do(t, "GET", h.url+"/br", nil, nil); string(r.Body) != "not really brotli" {
		t.Fatal("answer changed")
	}
	for i := 0; i < 5; i++ {
		tk.Do(t, "GET", h.url+"/x", nil, nil)
	}
	r := h.stop(t)
	why := r.Shadow.NotMirroredWhy
	if why["response body is larger than max_body"] != 1 || why["request body is larger than max_body"] != 1 ||
		why["primary answer is compressed with an encoding other than gzip"] != 1 {
		t.Fatalf("reasons: %v", why)
	}
	if r.Diff != 5 || len(r.Cases) != 2 || r.Shadow.NotListed != 3 {
		t.Fatalf("%s, %d listed, %d not listed", r.Summary(), len(r.Cases), r.Shadow.NotListed)
	}
}

// Copies still queued at stop are counted, and Close doesn't wait past the
// candidate timeout.
func TestStopDoesNotWaitForever(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "a") }))
	defer primary.Close()
	block := make(chan struct{})
	defer close(block)
	cand := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer cand.Close()
	h := start(t, primary.URL, cand.URL, shadow.Options{Workers: 1, Queue: 10, CandidateTimeout: 300 * time.Millisecond})
	for i := 0; i < 5; i++ {
		tk.Do(t, "GET", h.url+"/x", nil, nil)
	}
	t0 := time.Now()
	r := h.stop(t)
	if d := time.Since(t0); d > 3*time.Second {
		t.Fatalf("Close took %s", d)
	}
	if int64(r.Same+r.Diff+r.Errors)+r.Shadow.Unfinished != 5 {
		t.Fatalf("%s, %d unfinished", r.Summary(), r.Shadow.Unfinished)
	}
}

// Many clients at once: every answer right, every copy accounted for.
func TestConcurrentShadowing(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"q":%q}`, r.URL.RawQuery)
	}))
	defer primary.Close()
	h := start(t, primary.URL, primary.URL, shadow.Options{Queue: 1000})
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				q := fmt.Sprintf("g=%d&i=%d", g, i)
				r := tk.Do(t, "GET", h.url+"/?"+q, nil, nil)
				if string(r.Body) != fmt.Sprintf(`{"q":%q}`, q) {
					t.Errorf("got %q", r.Body)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	r := h.stop(t)
	if r.Same != 800 || r.Shadow.Mirrored != 800 {
		t.Fatalf("%s, %d mirrored", r.Summary(), r.Shadow.Mirrored)
	}
}

func TestOptionsAreChecked(t *testing.T) {
	u := mustURL(t, "http://127.0.0.1:1")
	for _, o := range []shadow.Options{
		{Candidate: u},
		{Primary: u},
		{Primary: u, Candidate: mustURL(t, "ftp://x")},
		{Primary: u, Candidate: u, Sample: -1},
		{Primary: u, Candidate: u, ReportPath: filepath.Join(t.TempDir(), "missing", "r.md")},
	} {
		if _, err := shadow.New(o); err == nil {
			t.Errorf("%+v accepted", o)
		}
	}
}

// when: picks the traffic that is copied; the rest is counted as outside
// when:. A compared Location keeps listed query values out of the report.
func TestWhenAndLocation(t *testing.T) {
	answer := func(who string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", "/next?token=loc-"+who+"&page="+who)
			w.WriteHeader(302)
		}
	}
	primary := httptest.NewServer(answer("p"))
	defer primary.Close()
	cand := httptest.NewServer(answer("c"))
	defer cand.Close()
	cfg, err := record.Load([]byte("version: 1\nwhen: {path: /api/**}\n"))
	if err != nil {
		t.Fatal(err)
	}
	h := start(t, primary.URL, cand.URL, shadow.Options{Config: cfg, Headers: []string{"Location"}})
	tk.Do(t, "GET", h.url+"/api/x", nil, nil)
	tk.Do(t, "GET", h.url+"/other", nil, nil)
	r := h.stop(t)
	if r.Shadow.Requests != 2 || r.Shadow.OutsideWhen != 1 || r.Shadow.Mirrored != 1 || r.Diff != 1 {
		t.Fatalf("%+v %s", *r.Shadow, r.Summary())
	}
	d := strings.Join(r.Cases[0].Diffs, "|")
	if strings.Contains(d, "loc-") || !strings.Contains(d, `"/next?token=REDACTED&page=p" vs "/next?token=REDACTED&page=c"`) {
		t.Fatalf("diffs %q", d)
	}
}
