package replay_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aftermarket-dev/aftermarket/internal/fixture"
	"github.com/aftermarket-dev/aftermarket/internal/replay"
	tk "github.com/aftermarket-dev/aftermarket/internal/testkit"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func load(t *testing.T, src string) *fixture.Fixture {
	t.Helper()
	f, err := fixture.Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func serve(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s.URL
}

func jsonServer(t *testing.T, ct, body string) string {
	return serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", ct)
		io.WriteString(w, body)
	})
}

const oneGet = `cases:
  - name: get
    request: {method: GET, path: /x}
    expect:
      response: {status: 200}
`

func run(t *testing.T, f *fixture.Fixture, o replay.Options) *replay.Report {
	t.Helper()
	rep, err := replay.Run(context.Background(), f, o)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// TestJSONIsComparedByMeaning: key order, spacing and how a number is
// written don't count as differences.
func TestJSONIsComparedByMeaning(t *testing.T) {
	a := jsonServer(t, "application/json", `{"a":1,"b":[1.0,2,{"c":"x"}],"n":100,"big":12345678901234567890}`)
	b := jsonServer(t, "application/json", "{\n  \"big\": 1.2345678901234567890e19, \"n\": 1e2,\n \"b\": [1, 2.00, {\"c\": \"x\"}], \"a\": 1.0}")
	rep := run(t, load(t, oneGet), replay.Options{A: mustURL(t, a), B: mustURL(t, b)})
	if !rep.OK() || rep.Same != 1 {
		t.Fatalf("got %+v", rep.Cases)
	}
}

func TestDifferencesAreListedByPath(t *testing.T) {
	a := jsonServer(t, "application/json", `{"id":1,"name":"Ada","tags":["a","b"],"meta":{"at":"10:00","v":1},"list":[{"x":1},{"x":2}]}`)
	b := jsonServer(t, "application/json; charset=utf-8", `{"id":"1","full_name":"Ada","tags":["a"],"meta":{"at":"11:00","v":1},"list":[{"x":1},{"x":3}]}`)
	f := load(t, oneGet)
	rep := run(t, f, replay.Options{A: mustURL(t, a), B: mustURL(t, b)})
	got := strings.Join(rep.Cases[0].Diffs, "\n")
	for _, w := range []string{`header Content-Type: "application/json" vs "application/json; charset=utf-8"`,
		`id: 1 vs "1"`, `name: only in A ("Ada")`, `full_name: only in B ("Ada")`, "tags: 2 items vs 1 items",
		`meta.at: "10:00" vs "11:00"`, "list.1.x: 2 vs 3"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in:\n%s", w, got)
		}
	}
	if rep.OK() || rep.Diff != 1 {
		t.Fatal("a difference must fail the replay")
	}
	// Ignoring paths, including with *, and comparing a different header set.
	ig := []replay.Pattern{}
	for _, s := range []string{"id", "name", "full_name", "tags", "meta.at", "list.*.x"} {
		p, err := replay.ParsePattern(s)
		if err != nil {
			t.Fatal(err)
		}
		ig = append(ig, p)
	}
	rep = run(t, f, replay.Options{A: mustURL(t, a), B: mustURL(t, b), Ignore: ig, Headers: []string{"x-none"}})
	if !rep.OK() {
		t.Fatalf("still different: %v", rep.Cases[0].Diffs)
	}
}

func TestOtherBodiesAreComparedByteForByte(t *testing.T) {
	a := jsonServer(t, "text/plain", "hello world")
	b := jsonServer(t, "text/plain", "hello World")
	c := jsonServer(t, "text/plain", "hello world")
	f := load(t, oneGet)
	rep := run(t, f, replay.Options{A: mustURL(t, a), B: mustURL(t, b)})
	if rep.OK() || !strings.Contains(rep.Cases[0].Diffs[0], "first difference at byte 6") {
		t.Fatalf("got %v", rep.Cases[0].Diffs)
	}
	if rep := run(t, f, replay.Options{A: mustURL(t, a), B: mustURL(t, c)}); !rep.OK() {
		t.Fatalf("same bytes reported different: %v", rep.Cases[0].Diffs)
	}
}

func TestDiffsAreCapped(t *testing.T) {
	var ab, bb strings.Builder
	ab.WriteString("{")
	bb.WriteString("{")
	for i := 0; i < 50; i++ {
		if i > 0 {
			ab.WriteString(",")
			bb.WriteString(",")
		}
		ab.WriteString(`"k` + strconv.Itoa(i) + `":1`)
		bb.WriteString(`"k` + strconv.Itoa(i) + `":2`)
	}
	ab.WriteString("}")
	bb.WriteString("}")
	rep := run(t, load(t, oneGet), replay.Options{A: mustURL(t, jsonServer(t, "application/json", ab.String())), B: mustURL(t, jsonServer(t, "application/json", bb.String()))})
	d := rep.Cases[0].Diffs
	if len(d) != 21 || d[20] != "and 30 more" {
		t.Fatalf("%d diffs, last %q", len(d), d[len(d)-1])
	}
}

const mixed = `cases:
  - name: read
    request: {method: GET, path: "/items?id=7&q=a%20b;c"}
    expect:
      response: {status: 200}
  - name: create
    request:
      method: POST
      path: /items
      headers: {Content-Type: application/json, Authorization: REDACTED, Accept-Encoding: gzip}
      body: '{"name":"x"}'
    expect:
      response: {status: 201}
  - name: remove
    request: {method: DELETE, path: /items/7}
    expect:
      response: {status: 204}
`

// TestWritesAreSentOnlyWhenAsked: by default replay sends only GET and HEAD,
// so it can't change data on a live service.
func TestWritesAreSentOnlyWhenAsked(t *testing.T) {
	rec := &tk.Recorder{}
	h := func(w http.ResponseWriter, r *http.Request) { rec.Add(r); io.WriteString(w, "ok") }
	a, b := serve(t, h), serve(t, h)
	f := load(t, mixed)
	rep := run(t, f, replay.Options{A: mustURL(t, a), B: mustURL(t, b)})
	if rec.Count() != 2 || rep.Same != 1 || rep.Skipped != 2 || !rep.OK() {
		t.Fatalf("sent %d requests: %+v", rec.Count(), rep.Cases)
	}
	if !strings.Contains(rep.Cases[1].Reason, "--include-writes") {
		t.Fatalf("reason %q", rep.Cases[1].Reason)
	}
	if got := rec.Last().URI; got != "/items?id=7&q=a%20b;c" {
		t.Fatalf("the query changed on the way: %s", got)
	}
	rep = run(t, f, replay.Options{A: mustURL(t, a), B: mustURL(t, b), IncludeWrites: true,
		SetHeaders: [][2]string{{"Authorization", "Bearer real"}}})
	if rec.Count() != 8 || rep.Same != 3 {
		t.Fatalf("sent %d requests: %+v", rec.Count(), rep.Cases)
	}
}

func TestRequestsAreSentAsRecorded(t *testing.T) {
	rec := &tk.Recorder{}
	a := serve(t, func(w http.ResponseWriter, r *http.Request) { rec.Add(r); io.WriteString(w, "ok") })
	f := load(t, mixed)
	run(t, f, replay.Options{A: mustURL(t, a+"/base"), B: mustURL(t, a+"/base"), IncludeWrites: true,
		SetHeaders: [][2]string{{"Authorization", "Bearer real"}}})
	var post *tk.Recorded
	got := rec.All()
	for i := range got {
		if got[i].Method == "POST" {
			post = &got[i]
			break
		}
	}
	if post == nil {
		t.Fatal("no POST was sent")
	}
	if post.URI != "/base/items" || string(post.Body) != `{"name":"x"}` || post.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("POST arrived as %+v", post)
	}
	if post.Header.Get("Authorization") != "Bearer real" {
		t.Fatalf("--set-header was not applied: %q", post.Header.Get("Authorization"))
	}
	if got[0].URI != "/base/items?id=7&q=a%20b;c" {
		t.Fatalf("GET arrived as %s", got[0].URI)
	}
}

func TestGzipAnswersAreComparedDecoded(t *testing.T) {
	a := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			w.Header().Set("Content-Encoding", "gzip")
			w.Write(tk.Gzip([]byte(`{"a":1}`)))
			return
		}
		io.WriteString(w, `{"a":1}`)
	})
	b := jsonServer(t, "application/json", `{"a":1}`)
	rep := run(t, load(t, oneGet), replay.Options{A: mustURL(t, a), B: mustURL(t, b)})
	if !rep.OK() {
		t.Fatalf("got %v", rep.Cases[0].Diffs)
	}
}

func TestOneServiceIsComparedWithTheRecording(t *testing.T) {
	f := load(t, `cases:
  - name: c
    request: {path: /c}
    expect:
      response:
        status: 200
        headers: {Content-Type: application/json}
        body: '{"id": 7, "name": "Ada"}'
`)
	same := jsonServer(t, "application/json", `{"name":"Ada","id":7.0}`)
	if rep := run(t, f, replay.Options{A: mustURL(t, same)}); !rep.OK() || rep.B != "recorded responses" {
		t.Fatalf("got %+v", rep.Cases[0])
	}
	other := jsonServer(t, "text/plain", `{"name":"Grace","id":7}`)
	rep := run(t, f, replay.Options{A: mustURL(t, other)})
	d := strings.Join(rep.Cases[0].Diffs, "\n")
	if rep.OK() || !strings.Contains(d, `name: "Grace" vs "Ada"`) || !strings.Contains(d, "header Content-Type") {
		t.Fatalf("got %s", d)
	}
}

func TestUnreachableServiceIsAnError(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := "http://" + ln.Addr().String()
	ln.Close()
	slow := serve(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	ok := jsonServer(t, "text/plain", "ok")
	f := load(t, oneGet)
	rep := run(t, f, replay.Options{A: mustURL(t, ok), B: mustURL(t, dead)})
	if rep.OK() || rep.Errors != 1 || rep.Cases[0].Reason != "B: could not connect" {
		t.Fatalf("got %+v", rep.Cases[0])
	}
	rep = run(t, f, replay.Options{A: mustURL(t, slow), B: mustURL(t, ok), Timeout: 200 * time.Millisecond})
	if rep.OK() || rep.Cases[0].Reason != "A: no answer within the timeout" {
		t.Fatalf("got %+v", rep.Cases[0])
	}
}

func TestReports(t *testing.T) {
	a := jsonServer(t, "application/json", `{"v":1}`)
	b := jsonServer(t, "application/json", `{"v":2}`)
	rep := run(t, load(t, mixed), replay.Options{A: mustURL(t, a), B: mustURL(t, b)})
	var md bytes.Buffer
	rep.WriteMarkdown(&md)
	for _, w := range []string{"# Replay report", "3 case(s): 0 same, 1 different, 2 skipped, 0 error(s)", "### read", "- v: 1 vs 2", "## Skipped", "- create: POST changes data"} {
		if !strings.Contains(md.String(), w) {
			t.Errorf("missing %q in:\n%s", w, md.String())
		}
	}
	var js bytes.Buffer
	rep.WriteJSON(&js)
	var back replay.Report
	if err := json.Unmarshal(js.Bytes(), &back); err != nil || back.Diff != 1 || len(back.Cases) != 3 || back.Cases[0].Outcome != replay.Different {
		t.Fatalf("JSON report: %v %s", err, js.String())
	}
}

func TestBadOptions(t *testing.T) {
	f := load(t, oneGet)
	for _, o := range []replay.Options{{}, {A: mustURL(t, "/relative")}, {A: mustURL(t, "ftp://x")}, {A: mustURL(t, "http://x"), B: mustURL(t, "nohost")}} {
		if _, err := replay.Run(context.Background(), f, o); err == nil {
			t.Errorf("%+v: no error", o)
		}
	}
	for _, s := range []string{"", "a..b", ".a"} {
		if _, err := replay.ParsePattern(s); err == nil {
			t.Errorf("%q: no error", s)
		}
	}
}
