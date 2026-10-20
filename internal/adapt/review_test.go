package adapt_test

// Tests added after the independent review of 0.2.0. Each one reproduces a
// finding the first suite missed.

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

	"github.com/aftermarket-dev/aftermarket/internal/adapt"
	"github.com/aftermarket-dev/aftermarket/internal/audit"
	tk "github.com/aftermarket-dev/aftermarket/internal/testkit"
)

func TestSkippedMatchThenUpstreamErrorLogsPatternAndSameID(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	h := start(t, "endpoints:\n  - {id: tok, old: \"GET /tok/{token}?key={key}\", new: \"GET /v2/tok/{token}\"}\n", "http://"+addr, false)
	tk.Do(t, "GET", h.url+"/tok/SECRET-TOKEN?key=a&key=b", nil, nil)
	ev := h.audit.Events(t)
	if len(ev) != 2 || ev[0].Event != audit.Skipped || ev[1].Event != audit.UpstreamError {
		t.Fatalf("events = %+v", ev)
	}
	if ev[0].Request != ev[1].Request || ev[1].Path != "/tok/{token}" || ev[1].Rule != "tok" {
		t.Fatalf("upstream error must use the same id and the pattern: %+v", ev[1])
	}
	if strings.Contains(h.audit.String(), "SECRET") {
		t.Fatalf("value leaked:\n%s", h.audit.String())
	}
}

func slowBodyUpstream(t *testing.T, delay time.Duration) string {
	t.Helper()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"customer":`)
		w.(http.Flusher).Flush()
		time.Sleep(delay)
		io.WriteString(w, `{"id":1}}`)
	}))
	t.Cleanup(up.Close)
	return up.URL
}

func TestDryRunNeverTurnsSlowAnswersInto504(t *testing.T) {
	up := slowBodyUpstream(t, 600*time.Millisecond)
	h := start(t, "limits: {upstream_timeout: 300ms}\n"+exampleConfig(t), up, true)
	got := tk.Do(t, "GET", h.url+"/api/v1/customer?id=1", nil, nil)
	if got.Status != 200 || string(got.Body) != `{"customer":{"id":1}}` {
		t.Fatalf("dry run changed the answer: %d %s", got.Status, got.Body)
	}
}

func TestStalledRequestBodyGets408(t *testing.T) {
	_, up := recordingUpstream(t, 200, "application/json", `{}`)
	h := start(t, "limits: {upstream_timeout: 300ms}\n"+exampleConfig(t), up, false)
	conn, err := net.Dial("tcp", strings.TrimPrefix(h.url, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(conn, "POST /api/v1/customers HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"name\":")
	start := time.Now()
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("no answer to a stalled body: %v", err)
	}
	if resp.StatusCode != 408 || time.Since(start) > 3*time.Second {
		t.Fatalf("status %d after %v", resp.StatusCode, time.Since(start))
	}
	if e := h.audit.Last(t); e.Event != audit.Skipped || !strings.Contains(e.Reason, "did not arrive") {
		t.Fatalf("audit = %+v", e)
	}
}

func TestLeftoverQueryNeverOverridesTranslatedKeys(t *testing.T) {
	rec, up := recordingUpstream(t, 200, "application/json", `{}`)
	h := start(t, "endpoints:\n  - {id: c, old: \"GET /v1/c?id={id}\", new: \"GET /v2/c?cid={id}&version=2\"}\n", up, false)
	tk.Raw(t, h.url, "GET /v1/c?id=7&cid=999&version=1&x=1 HTTP/1.1\r\nHost: x\r\n\r\n")
	if got := rec.Last().URI; got != "/v2/c?cid=7&version=2&x=1" {
		t.Fatalf("got %s", got)
	}
}

func TestRenameNeverOverwritesExistingValues(t *testing.T) {
	rec, up := recordingUpstream(t, 200, "application/json", `{}`)
	h := start(t, "endpoints:\n  - id: h\n    old: GET /a\n    new: GET /b\n    request: {headers: {rename: {X-Old: X-New}}}\n", up, false)
	tk.Do(t, "GET", h.url+"/a", http.Header{"X-Old": {"old"}, "X-New": {"client-new"}}, nil)
	r := rec.Last().Header
	if r.Get("X-New") != "client-new" || r.Get("X-Old") != "old" {
		t.Fatalf("headers = %v", r)
	}
	if e := h.audit.Last(t); len(e.NotApplied) != 1 || !strings.Contains(e.NotApplied[0], "already present") {
		t.Fatalf("audit = %+v", e)
	}
}

func TestInvalidUTF8ParameterIsSkipped(t *testing.T) {
	rec, up := recordingUpstream(t, 200, "application/json", `{}`)
	h := start(t, "endpoints:\n  - id: m\n    old: GET /v1/make?name={name}\n    new: POST /v2/make\n    request: {body: {name: $params.name}}\n", up, false)
	tk.Raw(t, h.url, "GET /v1/make?name=%FF%FE HTTP/1.1\r\nHost: x\r\n\r\n")
	if r := rec.Last(); r.Method != "GET" || r.URI != "/v1/make?name=%FF%FE" {
		t.Fatalf("must pass through unchanged, got %s %s", r.Method, r.URI)
	}
	if e := h.audit.Last(t); e.Event != audit.Skipped || !strings.Contains(e.Reason, "UTF-8") {
		t.Fatalf("audit = %+v", e)
	}
}

func TestRepeatedParameterChecksHeadersFirst(t *testing.T) {
	rec, up := recordingUpstream(t, 200, "application/json", `{}`)
	cfg := `
endpoints:
  - id: a
    old: GET /v1/c?id={id}
    new: GET /v2/a/{id}
    when: {headers: {X-V: a}}
  - id: b
    old: GET /v1/c
    new: GET /v2/b
`
	h := start(t, cfg, up, false)
	tk.Do(t, "GET", h.url+"/v1/c?id=1&id=2", http.Header{"X-V": {"b"}}, nil)
	if got := rec.Last().URI; got != "/v2/b?id=1&id=2" {
		t.Fatalf("endpoint b should handle it, got %s", got)
	}
	tk.Do(t, "GET", h.url+"/v1/c?id=&id=7", http.Header{"X-V": {"a"}}, nil)
	if got := rec.Last().URI; got != "/v1/c?id=&id=7" {
		t.Fatalf("a repeated parameter must pass through, got %s", got)
	}
	if e := h.audit.Last(t); e.Event != audit.Skipped || e.Rule != "a" || !strings.Contains(e.Reason, "more than once") {
		t.Fatalf("audit = %+v", e)
	}
	// A header near miss falls through to the next endpoint.
	tk.Do(t, "GET", h.url+"/v1/c?id=5", http.Header{"X-V": {"nope"}}, nil)
	if got := rec.Last().URI; got != "/v2/b?id=5" {
		t.Fatalf("header near miss: got %s", got)
	}
}

func TestExplicitHeadersWinOverRebuildDefaults(t *testing.T) {
	_, up := recordingUpstream(t, 200, "application/json", `{"customer":{"id":1}}`)
	cfg := `
endpoints:
  - id: e
    old: GET /v1/c
    new: GET /v2/c
    response:
      body: {id: $.customer.id}
      headers: {set: {Etag: '"v1-etag"', Content-Type: application/vnd.legacy+json}}
`
	h := start(t, cfg, up, false)
	got := tk.Do(t, "GET", h.url+"/v1/c", nil, nil)
	if got.Header.Get("Etag") != `"v1-etag"` || got.Header.Get("Content-Type") != "application/vnd.legacy+json" || string(got.Body) != `{"id":1}` {
		t.Fatalf("got %v %s", got.Header, got.Body)
	}
	rec, up2 := recordingUpstream(t, 200, "application/json", `{}`)
	h2 := start(t, "endpoints:\n  - id: r\n    old: GET /v1/m?n={n}\n    new: POST /v2/m\n    request:\n      body: {n: $params.n}\n      headers: {set: {Content-Type: application/vnd.v2+json}}\n", up2, false)
	tk.Do(t, "GET", h2.url+"/v1/m?n=1", nil, nil)
	if ct := rec.Last().Header.Get("Content-Type"); ct != "application/vnd.v2+json" {
		t.Fatalf("request content type = %q", ct)
	}
}

func TestResponseSkipLeavesHeadersAlone(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Header().Set("X-Api-Version", "2")
		w.Header().Set("X-Keep", "k")
		w.WriteHeader(201)
		io.WriteString(w, "<p>x</p>")
	}))
	defer up.Close()
	cfg := `
endpoints:
  - id: e
    old: GET /v1/c
    new: GET /v2/c
    response:
      status: {201: 200}
      body: {id: $.id}
      headers: {remove: [X-Api-Version], set: {X-Added: "1"}, rename: {X-Keep: X-Kept}}
`
	h := start(t, cfg, up.URL, false)
	got := tk.Do(t, "GET", h.url+"/v1/c", nil, nil)
	if got.Status != 201 || got.Header.Get("X-Api-Version") != "2" || got.Header.Get("X-Added") != "" || got.Header.Get("X-Keep") != "k" || got.Header.Get("X-Kept") != "" {
		t.Fatalf("a skipped response must keep every header: %d %v", got.Status, got.Header)
	}
}

func TestDryRunCoversEveryKindOfChange(t *testing.T) {
	rec := &tk.Recorder{}
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.Add(r)
		switch r.URL.Path {
		case "/v1/del":
			w.WriteHeader(204)
		case "/v1/gz":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Encoding", "gzip")
			w.Write(tk.Gzip([]byte(`{"a":1}`)))
		default:
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"a":1}`)
		}
	}))
	defer up.Close()
	cfg := `
endpoints:
  - {id: del, old: DELETE /v1/del, new: DELETE /v2/del, response: {status: {204: 200}, body: {deleted: true}}}
  - {id: gz, old: GET /v1/gz, new: GET /v2/gz, response: {body: {b: $.a}}}
  - {id: search, old: POST /v1/search, new: GET /v2/search}
`
	h := start(t, cfg, up.URL, true)
	for _, c := range []struct {
		method, path string
		body         []byte
	}{
		{"DELETE", "/v1/del", nil},
		{"GET", "/v1/gz", nil},
		{"POST", "/v1/search", []byte(`{"q":1}`)},
	} {
		got := tk.Do(t, c.method, h.url+c.path, http.Header{"Content-Type": {"application/json"}}, c.body)
		r := rec.Last()
		if r.Method != c.method || r.URI != c.path || string(r.Body) != string(c.body) {
			t.Fatalf("%s %s: dry run changed the request to %s %s %q", c.method, c.path, r.Method, r.URI, r.Body)
		}
		switch c.path {
		case "/v1/del":
			if got.Status != 204 || len(got.Body) != 0 {
				t.Fatalf("dry run changed the delete answer: %d %q", got.Status, got.Body)
			}
		case "/v1/gz":
			if got.Header.Get("Content-Encoding") != "gzip" || string(tk.Gunzip(t, got.Body)) != `{"a":1}` {
				t.Fatalf("dry run changed the gzip answer")
			}
		}
	}
	would := 0
	for _, e := range h.audit.Events(t) {
		if e.Event == audit.WouldChange {
			would++
		}
	}
	if would < 5 {
		t.Fatalf("dry run should report the changes it would make:\n%s", h.audit.String())
	}
}

func TestChunkedRequestBodyIsDroppedForGet(t *testing.T) {
	rec, up := recordingUpstream(t, 200, "application/json", `{}`)
	h := start(t, "endpoints:\n  - {id: s, old: POST /v1/search, new: GET /v2/search}\n", up, false)
	pr, pw := io.Pipe()
	go func() { io.WriteString(pw, `{"q":"x"}`); pw.Close() }()
	req, _ := http.NewRequest("POST", h.url+"/v1/search", pr) // unknown length, sent chunked
	req.Header.Set("Content-Type", "application/json")
	resp, err := tk.Client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if r := rec.Last(); r.Method != "GET" || len(r.Body) != 0 {
		t.Fatalf("got %s with %d bytes", r.Method, len(r.Body))
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
	h := start(t, exampleConfig(t), up.URL, false)
	resp, err := tk.Client.Get(h.url + "/api/v2/customers")
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
		t.Fatal("unmatched traffic is being buffered")
	}
}

func TestHugeNumbersAreRejected(t *testing.T) {
	_, err := adapt.Load([]byte("endpoints:\n  - id: e\n    old: GET /a\n    new: GET /b\n    response: {body: {n: 1e400}}\n"))
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err = %v", err)
	}
	if _, err := adapt.Load([]byte("endpoints:\n  - id: e\n    old: GET /a\n    new: GET /b\n    response: {body: {n: \"1e400\", m: 1.5e300}}\n")); err != nil {
		t.Fatalf("a quoted value and a large finite number are fine: %v", err)
	}
}

func TestCheckReportsEveryProblemInLineOrder(t *testing.T) {
	src := `endpoints:
  - id: one
    old: GET a/{id}
    new: GET /b/{id}
    request:
      headers: {rename: {Authorization: X-Auth}}
    response:
      body: {x: $nope, y: $alsonope}
  - id: two
    old: GET /c
    new: GET /d
    response:
      body: {x: $wrong, y: $alsowrong}
      headers: {rename: {X-A: X-C, X-B: X-C}}
`
	_, err := adapt.Load([]byte(src))
	if err == nil {
		t.Fatal("expected errors")
	}
	msg := err.Error()
	for _, want := range []string{"line 3: old:", "carries credentials", "$nope", "$alsonope", "$wrong", "$alsowrong", "X-C is used by both rename and rename"} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "{id} is not captured") {
		t.Errorf("a broken old must not cause false errors:\n%s", msg)
	}
	lines := strings.Split(msg, "\n")
	for i := 1; i < len(lines); i++ {
		if lineOf(lines[i]) < lineOf(lines[i-1]) {
			t.Fatalf("errors are not in line order:\n%s", msg)
		}
	}
}

func lineOf(s string) int {
	var n int
	if _, err := fmtSscanf(s, &n); err != nil {
		return 0
	}
	return n
}

func TestHeaderMapOverlapsAreRejected(t *testing.T) {
	for _, h := range []string{
		"{rename: {X-A: X-C, X-B: X-C}}",
		"{rename: {X-A: X-B, X-B: X-C}}",
		"{rename: {X-A: X-B}, remove: [X-B]}",
		"{rename: {X-A: X-B}, set: {X-A: v}}",
		"{set: {X-A: v}, remove: [X-A]}",
	} {
		src := "endpoints:\n  - id: e\n    old: GET /a\n    new: GET /b\n    request: {headers: " + h + "}\n"
		if _, err := adapt.Load([]byte(src)); err == nil || !strings.Contains(err.Error(), "each header may appear once") {
			t.Errorf("%s: err = %v", h, err)
		}
	}
}

func fmtSscanf(s string, n *int) (int, error) { return fmt.Sscanf(s, "line %d:", n) }
