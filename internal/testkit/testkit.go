// Package testkit holds helpers the proxy tests share: a raw HTTP client,
// an upstream that records what it receives, and audit log capture.
package testkit

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aftermarket-dev/aftermarket/internal/audit"
)

// AuditSink collects audit lines safely across goroutines.
type AuditSink struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *AuditSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

// String returns everything logged so far.
func (s *AuditSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// Events decodes the log.
func (s *AuditSink) Events(t testing.TB) []audit.Event {
	t.Helper()
	var out []audit.Event
	for _, line := range strings.Split(strings.TrimSpace(s.String()), "\n") {
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

// Last returns the newest event.
func (s *AuditSink) Last(t testing.TB) audit.Event {
	t.Helper()
	ev := s.Events(t)
	if len(ev) == 0 {
		t.Fatal("no audit events")
	}
	return ev[len(ev)-1]
}

// Client never decompresses and never follows redirects, so tests see the
// exact bytes on the wire.
var Client = &http.Client{
	Transport:     &http.Transport{DisableCompression: true, MaxIdleConnsPerHost: 64},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Timeout:       20 * time.Second,
}

// Response is what a client received.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// Do sends one request.
func Do(t testing.TB, method, u string, h http.Header, body []byte) Response {
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
	resp, err := Client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, u, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s %s: read body after status %d (content length %d, %d bytes read): %v", method, u, resp.StatusCode, resp.ContentLength, len(b), err)
	}
	return Response{Status: resp.StatusCode, Header: resp.Header, Body: b}
}

// Raw sends a request exactly as written, for URLs and headers Go's client
// would never produce.
func Raw(t testing.TB, base, raw string) Response {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(conn, raw); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return Response{Status: resp.StatusCode, Header: resp.Header, Body: b}
}

// Recorded is one request an upstream received.
type Recorded struct {
	Method, URI string
	Header      http.Header
	Body        []byte
}

// Recorder is an upstream handler wrapper that records requests.
type Recorder struct {
	mu   sync.Mutex
	reqs []Recorded
}

// Add records r and returns what was recorded.
func (rc *Recorder) Add(r *http.Request) Recorded {
	b, _ := io.ReadAll(r.Body)
	rec := Recorded{Method: r.Method, URI: r.URL.RequestURI(), Header: r.Header.Clone(), Body: b}
	rc.mu.Lock()
	rc.reqs = append(rc.reqs, rec)
	rc.mu.Unlock()
	return rec
}

// Last returns the newest request.
func (rc *Recorder) Last() Recorded {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if len(rc.reqs) == 0 {
		return Recorded{}
	}
	return rc.reqs[len(rc.reqs)-1]
}

// Count returns how many requests were recorded.
func (rc *Recorder) Count() int {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return len(rc.reqs)
}

var ignored = map[string]bool{"Date": true, "Connection": true, "Keep-Alive": true}

// HeadersEqual compares headers, ignoring Date and connection management.
func HeadersEqual(a, b http.Header) (bool, string) {
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	for k := range keys {
		if ignored[k] {
			continue
		}
		if strings.Join(a.Values(k), "\x00") != strings.Join(b.Values(k), "\x00") {
			return false, fmt.Sprintf("%s: %q vs %q", k, a.Values(k), b.Values(k))
		}
	}
	return true, ""
}

// Gzip compresses b.
func Gzip(b []byte) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(b)
	zw.Close()
	return buf.Bytes()
}

// Gunzip decompresses b.
func Gunzip(t testing.TB, b []byte) []byte {
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

// DoChunked sends a request whose body has no declared length, so it goes
// out chunked.
func DoChunked(t testing.TB, method, u string, h http.Header, body []byte) Response {
	t.Helper()
	req, err := http.NewRequest(method, u, io.NopCloser(bytes.NewReader(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.ContentLength = -1
	for k, v := range h {
		req.Header[k] = v
	}
	resp, err := Client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, u, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return Response{Status: resp.StatusCode, Header: resp.Header, Body: b}
}

// All returns every recorded request, oldest first.
func (rc *Recorder) All() []Recorded {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return append([]Recorded(nil), rc.reqs...)
}

// EarlyAnswer is an upstream transport that answers before it has seen the
// end of the request body, as fast upstreams do in real life. In turn it
// reads the whole declared length, half of it, or none of it (an upstream
// that turns a request away unread). It sends the answer in two parts, and
// between them reads the request body once more, which must still work. A proxy whose server closes the client's body when the answer
// starts going out fails that read, and Go's transport then drops the
// upstream connection mid-answer.
type EarlyAnswer struct {
	mu   sync.Mutex
	n    int
	errs []error
}

// Errors returns what the late reads of request bodies returned.
func (e *EarlyAnswer) Errors() []error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]error(nil), e.errs...)
}

func (e *EarlyAnswer) RoundTrip(r *http.Request) (*http.Response, error) {
	e.mu.Lock()
	mode := e.n % 3
	e.n++
	e.mu.Unlock()
	if r.Body != nil && r.ContentLength > 0 && mode < 2 {
		if _, err := io.ReadFull(r.Body, make([]byte, r.ContentLength/int64(1+mode))); err != nil {
			return nil, err
		}
	}
	return &http.Response{
		StatusCode: 200, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header:        http.Header{"Content-Type": {"application/octet-stream"}, "Content-Length": {"2000"}},
		ContentLength: 2000,
		Body:          &lateRead{e: e, req: r.Body},
		Request:       r,
	}, nil
}

type lateRead struct {
	e    *EarlyAnswer
	req  io.ReadCloser
	part int
}

func (l *lateRead) Read(p []byte) (int, error) {
	l.part++
	switch l.part {
	case 1:
		return copy(p, bytes.Repeat([]byte("a"), 1000)), nil
	case 2:
		var err error = io.EOF
		if l.req != nil {
			_, err = l.req.Read(make([]byte, 1))
		}
		l.e.mu.Lock()
		l.e.errs = append(l.e.errs, err)
		l.e.mu.Unlock()
		return copy(p, bytes.Repeat([]byte("b"), 1000)), io.EOF
	}
	return 0, io.EOF
}

func (l *lateRead) Close() error { return nil }

// CheckEarlyAnswer sends requests with a body through a proxy whose
// upstream is ea, one for each way of answering early, and fails unless
// every answer arrived whole and every late read of the request body
// worked.
func CheckEarlyAnswer(t testing.TB, proxyURL string, ea *EarlyAnswer) {
	t.Helper()
	for i := 0; i < 3; i++ {
		got := Do(t, "POST", proxyURL+"/api/upload", http.Header{"Content-Type": {"application/octet-stream"}}, bytes.Repeat([]byte("x"), 5000))
		if got.Status != 200 || len(got.Body) != 2000 {
			t.Fatalf("status %d, %d of 2000 bytes", got.Status, len(got.Body))
		}
	}
	errs := ea.Errors()
	if len(errs) != 3 {
		t.Fatalf("%d late reads, want 3", len(errs))
	}
	for _, err := range errs {
		if err != nil && err != io.EOF {
			t.Fatalf("the request body was closed under the upstream while the answer went out: %v", err)
		}
	}
}

// CheckHealthWithBody sends a health check that carries a body, then a
// second request on the same connection. Both must be answered: a handler
// that answers by itself must leave the body to the server, or the server
// drops the connection.
func CheckHealthWithBody(t testing.TB, base, path string) {
	t.Helper()
	for _, size := range []int{1, 100, 10000, 200000} {
		conn, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		conn.SetDeadline(time.Now().Add(10 * time.Second))
		req := fmt.Sprintf("POST %s HTTP/1.1\r\nHost: x\r\nContent-Length: %d\r\n\r\n%s", path, size, strings.Repeat("x", size))
		req += "GET " + path + " HTTP/1.1\r\nHost: x\r\n\r\n"
		go io.WriteString(conn, req)
		br := bufio.NewReader(conn)
		for i := 0; i < 2; i++ {
			resp, err := http.ReadResponse(br, nil)
			if err != nil {
				conn.Close()
				t.Fatalf("health check with a %d-byte body: response %d: %v", size, i+1, err)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 200 {
				conn.Close()
				t.Fatalf("health check with a %d-byte body: response %d is %d", size, i+1, resp.StatusCode)
			}
			if resp.Close {
				break // the server may close after a large unread body; it must say so
			}
		}
		conn.Close()
	}
}
