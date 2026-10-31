package shadow_test

// Tests from the independent review of shadow mode (Oct 31 2026). Each one
// failed on the 0.10.0 code.

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aftermarket-dev/aftermarket/internal/fixture"
	"github.com/aftermarket-dev/aftermarket/internal/record"
	"github.com/aftermarket-dev/aftermarket/internal/replay"
	"github.com/aftermarket-dev/aftermarket/internal/shadow"
	tk "github.com/aftermarket-dev/aftermarket/internal/testkit"
)

// Copies cut short by the stop are unfinished, not candidate errors: the
// candidate never failed them. 0.10.0 listed them as "B: cancelled".
func TestStopCountsCancelledCopiesAsUnfinished(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "a") }))
	defer primary.Close()
	cand := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer cand.Close()
	h := start(t, primary.URL, cand.URL, shadow.Options{Workers: 1, Queue: 10, CandidateTimeout: time.Second})
	for i := 0; i < 5; i++ {
		tk.Do(t, "GET", h.url+"/x", nil, nil)
	}
	// Close waits the candidate timeout plus a second: the first two copies
	// time out by themselves, the third is in flight when the wait ends.
	time.Sleep(500 * time.Millisecond)
	r := h.stop(t)
	for _, c := range r.Cases {
		if strings.Contains(c.Reason, "cancel") {
			t.Errorf("case %s reported as %q", c.Name, c.Reason)
		}
	}
	if r.Errors < 1 || r.Shadow.Unfinished < 1 || int64(r.Errors)+r.Shadow.Unfinished != 5 || r.Same+r.Diff != 0 {
		t.Fatalf("%s, %d unfinished", r.Summary(), r.Shadow.Unfinished)
	}
	if strings.Contains(h.audit.String(), "cancel") {
		t.Fatalf("audit log reports a cancelled copy as compared:\n%s", h.audit.String())
	}
}

// A gzip answer from the primary is compared as replay's client sees it:
// decoded, without Content-Encoding and the compressed Content-Length.
// 0.10.0 compared the raw headers, so --header Content-Length or
// Content-Encoding reported a difference that replay doesn't.
func TestGzipAnswerHeadersMatchReplay(t *testing.T) {
	body := []byte(`{"same":true,"pad":"` + strings.Repeat("x", 500) + `"}`)
	h1 := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		out := body
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			w.Header().Set("Content-Encoding", "gzip")
			out = tk.Gzip(body)
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(out)))
		w.Write(out)
	})
	primary := httptest.NewServer(h1)
	defer primary.Close()
	cand := httptest.NewServer(h1)
	defer cand.Close()
	headers := []string{"Content-Type", "Content-Encoding", "Content-Length"}
	send := func(base string) {
		r := tk.Do(t, "GET", base+"/x", http.Header{"Accept-Encoding": {"gzip"}}, nil)
		if r.Header.Get("Content-Encoding") != "gzip" {
			t.Fatal("primary answer not passed through as gzip")
		}
	}

	h := start(t, primary.URL, cand.URL, shadow.Options{Headers: headers})
	send(h.url)
	live := h.stop(t)

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
	rep, err := replay.Run(t.Context(), f, replay.Options{A: mustURL(t, primary.URL), B: mustURL(t, cand.URL), Headers: headers})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Same != 1 {
		t.Fatalf("replay: %s %+v", rep.Summary(), rep.Cases)
	}
	if live.Same != 1 || live.Diff != 0 {
		t.Fatalf("live: %s %+v", live.Summary(), live.Cases)
	}
}

// The running report shows the traffic counts even when nothing is copied.
// 0.10.0 only rewrote it after a copy, so a run of writes (or a low sample)
// left the counts at zero until stop.
func TestRunningReportShowsCountsWithoutCopies(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "a") }))
	defer primary.Close()
	cand := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "b") }))
	defer cand.Close()
	h := start(t, primary.URL, cand.URL, shadow.Options{ReportEvery: 20 * time.Millisecond})
	for i := 0; i < 3; i++ {
		tk.Do(t, "POST", h.url+"/x", nil, []byte("{}"))
	}
	md := filepath.Join(h.dir, "shadow.md")
	deadline := time.Now().Add(3 * time.Second)
	for {
		data, _ := os.ReadFile(md)
		if bytes.Contains(data, []byte("3 request(s) served from A")) && bytes.Contains(data, []byte("3 writes without --include-writes")) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("counts not in the running report:\n%s", data)
		}
		time.Sleep(10 * time.Millisecond)
	}
	h.stop(t)
}

// An answer left out by when:'s status or content_type is counted as outside
// when:, as the docs say. 0.10.0 lost it from every count.
func TestAnswerOutsideWhenIsCounted(t *testing.T) {
	answer := func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, "ok")
	}
	primary := httptest.NewServer(http.HandlerFunc(answer))
	defer primary.Close()
	cand := httptest.NewServer(http.HandlerFunc(answer))
	defer cand.Close()
	cfg, err := record.Load([]byte("version: 1\nwhen: {status: [200]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	h := start(t, primary.URL, cand.URL, shadow.Options{Config: cfg})
	tk.Do(t, "GET", h.url+"/x", nil, nil)
	tk.Do(t, "GET", h.url+"/missing", nil, nil)
	h.waitIdle(t, 1)
	r := h.stop(t)
	s := r.Shadow
	accounted := s.OutsideWhen + s.SampledOut + s.WritesNotSent + s.Mirrored + s.Dropped + s.NotMirrored
	if s.Requests != 2 || s.Mirrored != 1 || s.OutsideWhen != 1 || accounted != s.Requests {
		t.Fatalf("%+v", *s)
	}
}

// The candidate gets the request the primary got. Go's reverse proxy drops
// hop-by-hop headers such as Proxy-Authorization, a credential meant for the
// proxy; 0.10.0 still sent it on to the candidate.
func TestCandidateDoesNotGetProxyCredentials(t *testing.T) {
	var prec, crec tk.Recorder
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { prec.Add(r); fmt.Fprint(w, "a") }))
	defer primary.Close()
	cand := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { crec.Add(r); fmt.Fprint(w, "a") }))
	defer cand.Close()
	h := start(t, primary.URL, cand.URL, shadow.Options{})
	tk.Do(t, "GET", h.url+"/x", http.Header{"Proxy-Authorization": {"Basic cHJveHk6c2VjcmV0"}, "Authorization": {"Bearer real"},
		"Te": {"trailers"}, "Proxy-Authenticate": {"x"}}, nil)
	h.waitIdle(t, 1)
	h.stop(t)
	p, c := prec.Last(), crec.Last()
	if p.Header.Get("Proxy-Authorization") != "" {
		t.Fatal("test assumption: the primary got Proxy-Authorization")
	}
	for _, k := range []string{"Proxy-Authorization", "Proxy-Authenticate"} {
		if v := c.Header.Get(k); v != "" {
			t.Errorf("candidate got %s: %q", k, v)
		}
	}
	if c.Header.Get("Authorization") != "Bearer real" {
		t.Error("candidate lost the real Authorization header")
	}
}

// The proxy safety checks record passes, run through shadow with writes
// copied and a candidate that misbehaves.
func TestPrimaryPathChecksThroughShadow(t *testing.T) {
	cfg, err := record.Load([]byte("version: 1\nlimits: {upstream_timeout: 300ms}\n"))
	if err != nil {
		t.Fatal(err)
	}
	startOn := func(up string) string {
		return start(t, up, unruly(t).URL, shadow.Options{Config: cfg, IncludeWrites: true}).url
	}
	t.Run("stalled", func(t *testing.T) { tk.CheckStalledBodies(t, startOn) })
	t.Run("early", func(t *testing.T) { tk.CheckEarlyAnswerConnections(t, startOn) })
	t.Run("odd", func(t *testing.T) { tk.CheckOddClients(t, startOn) })
	t.Run("gone", func(t *testing.T) { tk.CheckClientGone(t, startOn) })
	t.Run("health", func(t *testing.T) {
		tk.CheckHealthWithBody(t, startOn("http://upstream.invalid"), "/_aftermarket/healthz")
	})
}
