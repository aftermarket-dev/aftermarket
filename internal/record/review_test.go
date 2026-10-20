package record_test

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aftermarket-dev/aftermarket/internal/adapt"
	"github.com/aftermarket-dev/aftermarket/internal/audit"
	"github.com/aftermarket-dev/aftermarket/internal/fixture"
	"github.com/aftermarket-dev/aftermarket/internal/patch"
	"github.com/aftermarket-dev/aftermarket/internal/rules"
	tk "github.com/aftermarket-dev/aftermarket/internal/testkit"
)

// Review finding: a 101 upgrade answered through record became a 502.
func TestUpgradedConnectionsPassThrough(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, brw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: echo\r\nConnection: Upgrade\r\n\r\n")
		line, _ := brw.ReadString('\n')
		io.WriteString(conn, "echo: "+line)
	}))
	defer up.Close()
	h := start(t, "", up.URL)
	conn, err := (&net.Dialer{}).Dial("tcp", strings.TrimPrefix(h.url, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	io.WriteString(conn, "GET /ws HTTP/1.1\r\nHost: x\r\nUpgrade: echo\r\nConnection: Upgrade\r\n\r\n")
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != 101 {
		t.Fatalf("got %v, %v; want 101", resp, err)
	}
	io.WriteString(conn, "hello\n")
	line, _ := br.ReadString('\n')
	if line != "echo: hello\n" {
		t.Fatalf("the upgraded connection doesn't carry data: %q", line)
	}
	ev := h.settle(t, 1)
	if ev[0].Event != audit.Skipped || !strings.Contains(ev[0].Reason, "upgraded") {
		t.Fatalf("got %+v", ev[0])
	}
	for _, e := range h.audit.Events(t) {
		if e.Event == audit.UpstreamError {
			t.Fatal("an upgrade was logged as an upstream error")
		}
	}
}

// Review findings: ; separators, encoded keys, default credential query keys
// and Location headers.
func TestQueryRedactionCoversEverySeparator(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "/next?code=1&access_token=loc-secret-1;page=2")
		w.WriteHeader(302)
	}))
	defer up.Close()
	h := start(t, "redact: {query: [session id]}\n", up.URL)
	uri := "/x?a=1;token=semi-secret-2&api%5Fkey=enc-secret-3&session+id=plus-secret-4&sig=sig-secret-5;b=2&bare"
	if got := tk.Do(t, "GET", h.url+uri, nil, nil); got.Status != 302 || got.Header.Get("Location") != "/next?code=1&access_token=loc-secret-1;page=2" {
		t.Fatalf("the client got %d %v", got.Status, got.Header)
	}
	h.settle(t, 1)
	out := h.out.String()
	for _, s := range []string{"loc-secret-1", "semi-secret-2", "enc-secret-3", "plus-secret-4", "sig-secret-5"} {
		if strings.Contains(out, s) {
			t.Errorf("%s reached the fixture file", s)
		}
	}
	c := h.fixture(t).Cases[0]
	if c.Request.Path != "/x?a=1;token=REDACTED&api%5Fkey=REDACTED&session+id=REDACTED&sig=REDACTED;b=2&bare" {
		t.Errorf("path = %s", c.Request.Path)
	}
	if !strings.Contains(out, `"/next?code=1&access_token=REDACTED;page=2"`) {
		t.Errorf("Location was not redacted:\n%s", out)
	}
}

// Review finding: an absolute-form request with no path was stored with an
// empty path, which made the whole file unreadable.
func TestRequestWithoutPathIsStoredAsSlash(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "root "+r.URL.RequestURI())
	}))
	defer up.Close()
	h := start(t, "", up.URL)
	host := strings.TrimPrefix(h.url, "http://")
	got := tk.Raw(t, h.url, "GET http://"+host+"?a=1 HTTP/1.1\r\nHost: "+host+"\r\nConnection: close\r\n\r\n")
	if string(got.Body) != "root /?a=1" {
		t.Fatalf("got %q", got.Body)
	}
	h.settle(t, 1)
	if p := h.fixture(t).Cases[0].Request.Path; p != "/?a=1" {
		t.Fatalf("path = %q", p)
	}
}

// Review finding: JSON sent without a JSON Content-Type wasn't redacted.
func TestJSONIsRedactedWhateverTheContentType(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, `{"password":"resp-secret-1"}`)
	}))
	defer up.Close()
	h := start(t, "redact:\n  headers: [Content-Type]\n  json: [password]\n", up.URL)
	tk.Do(t, "POST", h.url+"/a", nil, []byte(`{"password":"req-secret-2","user":"ada"}`))
	tk.Do(t, "POST", h.url+"/b", http.Header{"Content-Type": {"text/json"}}, []byte(`{"password":"req-secret-3"}`))
	tk.Do(t, "POST", h.url+"/c", http.Header{"Content-Type": {"application/json"}}, []byte(`{"password":`))
	tk.Do(t, "POST", h.url+"/d", nil, []byte(`plain text password=fine`))
	ev := h.settle(t, 4)
	out := h.out.String()
	for _, s := range []string{"resp-secret-1", "req-secret-2", "req-secret-3"} {
		if strings.Contains(out, s) {
			t.Errorf("%s reached the fixture file", s)
		}
	}
	if count(ev, audit.Recorded) != 3 || !strings.Contains(out, "plain text password=fine") {
		t.Fatalf("want 3 recorded, the broken JSON skipped: %s", h.audit.String())
	}
}

// Review finding: the upstream URL's password and query reached the file.
func TestUpstreamURLSecretsStayOut(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer up.Close()
	u, _ := url.Parse(up.URL)
	h := start(t, "", "http://bob:pw-secret@"+u.Host+"/base?api_key=up-secret")
	tk.Do(t, "GET", h.url+"/x", nil, nil)
	h.settle(t, 1)
	if out := h.out.String(); strings.Contains(out, "secret") || !strings.Contains(out, "http://"+u.Host+"/base") {
		t.Fatalf("header: %s", strings.SplitN(out, "\n", 2)[0])
	}
}

// Review findings: upstream errors carried the wrong request number, and
// exchanges past max_cases weren't logged.
func TestAuditIDsAndMaxCases(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			time.Sleep(400 * time.Millisecond)
		}
		io.WriteString(w, "ok")
	}))
	defer up.Close()
	h := start(t, "limits: {upstream_timeout: 150ms}\nwhen: {path: /rec/**}\nmax_cases: 1\n", up.URL)
	done := make(chan struct{})
	go func() { tk.Do(t, "GET", h.url+"/slow", nil, nil); close(done) }()
	time.Sleep(20 * time.Millisecond)
	for _, p := range []string{"/rec/1", "/rec/2", "/other"} {
		tk.Do(t, "GET", h.url+p, nil, nil)
	}
	<-done
	var upErr, maxSkip *audit.Event
	for _, e := range h.audit.Events(t) {
		e := e
		switch {
		case e.Event == audit.UpstreamError:
			upErr = &e
		case e.Event == audit.Skipped && e.Reason == "max_cases reached":
			maxSkip = &e
		case e.Event == audit.Skipped:
			t.Errorf("unexpected skip: %+v", e)
		}
	}
	if upErr == nil || upErr.Request != 1 || upErr.Path != "/slow" {
		t.Fatalf("upstream error: %+v", upErr)
	}
	if maxSkip == nil || maxSkip.Path != "/rec/2" {
		t.Fatalf("max_cases skip: %+v", maxSkip)
	}
}

// Review finding: header values that aren't UTF-8 were stored altered.
func TestHeadersThatArentTextAreSkipped(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }))
	defer up.Close()
	h := start(t, "", up.URL)
	got := tk.Raw(t, h.url, "GET /x HTTP/1.1\r\nHost: x\r\nX-In: na\xefve\r\nConnection: close\r\n\r\n")
	if got.Status != 200 {
		t.Fatalf("status %d", got.Status)
	}
	ev := h.settle(t, 1)
	if ev[0].Event != audit.Skipped || !strings.Contains(ev[0].Reason, "UTF-8") {
		t.Fatalf("got %+v", ev[0])
	}
}

// Test gap: a recording runs with patch test and adapt test, not only
// through record's own pass-through.
func TestRecordingsRunWithPatchAndAdaptTest(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/none":
			w.WriteHeader(204)
		case "/cached":
			w.WriteHeader(304)
		case "/moved":
			w.Header().Set("Location", "/elsewhere")
			w.WriteHeader(302)
		default:
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"method":"`+r.Method+`","got":`+strconvQuote(string(b))+`}`)
		}
	}))
	defer up.Close()
	h := start(t, "", up.URL)
	tk.Do(t, "GET", h.url+"/items?a=1;b=2", nil, nil)
	tk.Do(t, "HEAD", h.url+"/items", nil, nil)
	tk.Do(t, "POST", h.url+"/items", http.Header{"Content-Type": {"application/json"}}, []byte(`{"n":1}`))
	tk.Do(t, "OPTIONS", h.url+"/items", nil, nil)
	tk.Do(t, "DELETE", h.url+"/none", nil, nil)
	tk.Do(t, "GET", h.url+"/cached", nil, nil)
	tk.Do(t, "GET", h.url+"/moved", nil, nil)
	h.settle(t, 7)
	f := h.fixture(t)
	if len(f.Cases) != 7 {
		t.Fatalf("%d cases", len(f.Cases))
	}
	pcfg, err := rules.Load([]byte("rules:\n  - id: never\n    when: {path: /never}\n    response:\n      set_headers: {X-Never: \"1\"}\n"))
	if err != nil {
		t.Fatal(err)
	}
	acfg, err := adapt.Load([]byte("endpoints:\n  - id: never\n    old: GET /never\n    new: GET /also-never\n"))
	if err != nil {
		t.Fatal(err)
	}
	for name, newHandler := range map[string]fixture.NewHandler{"patch": patch.FixtureHandler(pcfg), "adapt": adapt.FixtureHandler(acfg)} {
		for _, r := range fixture.Run(newHandler, f) {
			if !r.Pass {
				t.Errorf("%s test: %s: %v", name, r.Name, r.Problems)
			}
		}
	}
}

func strconvQuote(s string) string {
	b := new(strings.Builder)
	b.WriteByte('"')
	for _, r := range s {
		if r == '"' || r == '\\' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('"')
	return b.String()
}
