// Package replay is AfterDiff: it sends the requests in a fixture file to two
// versions of a service and reports where their answers differ. With one
// service, the answers are compared with the recorded ones.
package replay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"

	"github.com/aftermarket-dev/aftermarket/internal/fixture"
	"github.com/aftermarket-dev/aftermarket/internal/httpx"
)

// Options configure a replay.
type Options struct {
	A, B          *url.URL // B nil: compare A with the recorded responses
	IncludeWrites bool     // send methods other than GET and HEAD
	Ignore        []Pattern
	Headers       []string    // response headers to compare; nil means Content-Type
	SetHeaders    [][2]string // request headers to set on every request, e.g. real credentials
	Timeout       time.Duration
	MaxBody       int64
	Transport     http.RoundTripper
}

// Outcome of one case.
const (
	Same      = "same"
	Different = "different"
	Skipped   = "skipped"
	Failed    = "error"
)

// CaseReport is the result of one case.
type CaseReport struct {
	Name    string   `json:"name"`
	Method  string   `json:"method"`
	Path    string   `json:"path"`
	Outcome string   `json:"outcome"`
	StatusA int      `json:"status_a,omitempty"`
	StatusB int      `json:"status_b,omitempty"`
	Diffs   []string `json:"differences,omitempty"`
	Reason  string   `json:"reason,omitempty"`
}

// Report is the result of a replay.
type Report struct {
	A        string       `json:"a"`
	B        string       `json:"b"`
	Same     int          `json:"same"`
	Diff     int          `json:"different"`
	Skipped  int          `json:"skipped"`
	Errors   int          `json:"errors"`
	Cases    []CaseReport `json:"cases"`
	Ignored  []string     `json:"ignored,omitempty"`
	Compared []string     `json:"headers_compared"`
}

// OK reports whether every case that ran gave the same answer on both sides.
func (r *Report) OK() bool { return r.Diff == 0 && r.Errors == 0 }

type answer struct {
	status int
	header http.Header
	body   []byte
}

// Run replays every case in order.
func Run(ctx context.Context, f *fixture.Fixture, o Options) (*Report, error) {
	if o.A == nil || o.A.Scheme == "" || o.A.Host == "" {
		return nil, errors.New("--a must be an absolute URL such as http://localhost:9000")
	}
	if o.B != nil && (o.B.Scheme == "" || o.B.Host == "") {
		return nil, errors.New("--b must be an absolute URL such as http://localhost:9001")
	}
	for _, u := range []*url.URL{o.A, o.B} {
		if u != nil && u.Scheme != "http" && u.Scheme != "https" {
			return nil, fmt.Errorf("scheme %q is not http or https", u.Scheme)
		}
	}
	if o.Timeout <= 0 {
		o.Timeout = 30 * time.Second
	}
	if o.MaxBody <= 0 {
		o.MaxBody = 16 << 20
	}
	headers := o.Headers
	if headers == nil {
		headers = []string{"Content-Type"}
	}
	for i, h := range headers {
		headers[i] = textproto.CanonicalMIMEHeaderKey(h)
	}
	tr := o.Transport
	if tr == nil {
		t := httpx.NewTransport(o.Timeout)
		t.DisableCompression = false // gzip answers are compared decoded
		tr = t
	}
	client := &http.Client{
		Transport:     tr,
		Timeout:       o.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	rep := &Report{A: o.A.Redacted(), B: "recorded responses", Compared: headers}
	if o.B != nil {
		rep.B = o.B.Redacted()
	}
	for _, p := range o.Ignore {
		rep.Ignored = append(rep.Ignored, strings.Join(p, "."))
	}
	for _, c := range f.Cases {
		cr := CaseReport{Name: c.Name, Method: c.Request.Method, Path: c.Request.Path}
		switch {
		case c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead && !o.IncludeWrites:
			cr.Outcome, cr.Reason = Skipped, c.Request.Method+" changes data; use --include-writes to send it"
		case o.B == nil && c.ExpectResponse == nil:
			cr.Outcome, cr.Reason = Failed, "the case has no recorded response to compare with"
		default:
			replayCase(ctx, client, o, headers, c, &cr)
		}
		switch cr.Outcome {
		case Same:
			rep.Same++
		case Different:
			rep.Diff++
		case Skipped:
			rep.Skipped++
		default:
			rep.Errors++
		}
		rep.Cases = append(rep.Cases, cr)
	}
	return rep, nil
}

func replayCase(ctx context.Context, client *http.Client, o Options, headers []string, c fixture.Case, cr *CaseReport) {
	a, err := send(ctx, client, o, o.A, c)
	if err != nil {
		cr.Outcome, cr.Reason = Failed, "A: "+err.Error()
		return
	}
	var b *answer
	if o.B != nil {
		b, err = send(ctx, client, o, o.B, c)
		if err != nil {
			cr.Outcome, cr.Reason = Failed, "B: "+err.Error()
			return
		}
	} else {
		e := c.ExpectResponse
		b = &answer{status: e.Status, header: http.Header{}}
		for _, h := range e.Headers {
			b.header.Add(h[0], h[1])
		}
		if e.Body != nil {
			b.body = []byte(*e.Body)
		}
	}
	cr.StatusA, cr.StatusB = a.status, b.status
	var diffs []string
	if b.status != 0 && a.status != b.status {
		diffs = append(diffs, fmt.Sprintf("status: %d vs %d", a.status, b.status))
	}
	for _, h := range headers {
		if o.B == nil && len(b.header.Values(h)) == 0 {
			continue // not recorded
		}
		av, bv := strings.Join(a.header.Values(h), ", "), strings.Join(b.header.Values(h), ", ")
		if av != bv {
			diffs = append(diffs, fmt.Sprintf("header %s: %q vs %q", h, av, bv))
		}
	}
	if o.B != nil || c.ExpectResponse.Body != nil {
		diffs = append(diffs, compareBodies(a.body, b.body, o.Ignore)...)
	}
	cr.Diffs = diffs
	cr.Outcome = Same
	if len(diffs) > 0 {
		cr.Outcome = Different
	}
}

// send replays the case's request against base.
func send(ctx context.Context, client *http.Client, o Options, base *url.URL, c fixture.Case) (*answer, error) {
	path, query, _ := strings.Cut(c.Request.Path, "?")
	u := *base
	u.RawPath = ""
	u.Path = ""
	target := strings.TrimSuffix(base.EscapedPath(), "/") + path
	var body io.Reader
	if c.Request.Body != nil && *c.Request.Body != "" {
		body = strings.NewReader(*c.Request.Body)
	}
	raw := u.Scheme + "://" + u.Host + target
	if q := joinQuery(base.RawQuery, query); q != "" {
		raw += "?" + q
	}
	req, err := http.NewRequestWithContext(ctx, c.Request.Method, raw, body)
	if err != nil {
		return nil, errors.New("the recorded request could not be built")
	}
	for _, h := range c.Request.Headers {
		switch textproto.CanonicalMIMEHeaderKey(h[0]) {
		case "Host", "Content-Length", "Accept-Encoding", "Connection", "Transfer-Encoding":
			continue
		}
		req.Header.Add(h[0], h[1])
	}
	for _, h := range o.SetHeaders {
		req.Header.Set(h[0], h[1])
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New(describe(err))
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, o.MaxBody+1))
	if err != nil {
		return nil, errors.New("the answer could not be read: " + describe(err))
	}
	if int64(len(data)) > o.MaxBody {
		return nil, fmt.Errorf("the answer is larger than %d bytes", o.MaxBody)
	}
	return &answer{status: resp.StatusCode, header: resp.Header, body: data}, nil
}

func joinQuery(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "&" + b
}

// describe turns a client error into fixed words.
func describe(err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
		return "no answer within the timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return "could not connect"
	}
	_, reason := httpx.UpstreamReason(err)
	return strings.TrimPrefix(reason, "upstream ")
}

// WriteJSON writes the report as JSON.
func (r *Report) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(r)
}

// WriteMarkdown writes the report for people to read.
func (r *Report) WriteMarkdown(w io.Writer) error {
	var b bytes.Buffer
	fmt.Fprintf(&b, "# Replay report\n\n")
	fmt.Fprintf(&b, "- A: %s\n- B: %s\n", r.A, r.B)
	fmt.Fprintf(&b, "- Headers compared: %s\n", strings.Join(r.Compared, ", "))
	if len(r.Ignored) > 0 {
		fmt.Fprintf(&b, "- Ignored: %s\n", strings.Join(r.Ignored, ", "))
	}
	fmt.Fprintf(&b, "\n%s\n", r.Summary())
	if r.Diff+r.Errors > 0 {
		fmt.Fprintf(&b, "\n## Differences\n")
		for _, c := range r.Cases {
			if c.Outcome != Different && c.Outcome != Failed {
				continue
			}
			fmt.Fprintf(&b, "\n### %s\n\n`%s %s`\n\n", c.Name, c.Method, c.Path)
			if c.Outcome == Failed {
				fmt.Fprintf(&b, "- error: %s\n", c.Reason)
				continue
			}
			for _, d := range c.Diffs {
				fmt.Fprintf(&b, "- %s\n", d)
			}
		}
	}
	if r.Skipped > 0 {
		fmt.Fprintf(&b, "\n## Skipped\n\n")
		for _, c := range r.Cases {
			if c.Outcome == Skipped {
				fmt.Fprintf(&b, "- %s: %s\n", c.Name, c.Reason)
			}
		}
	}
	_, err := w.Write(b.Bytes())
	return err
}

// Summary is one line with the counts.
func (r *Report) Summary() string {
	return fmt.Sprintf("%d case(s): %d same, %d different, %d skipped, %d error(s)",
		len(r.Cases), r.Same, r.Diff, r.Skipped, r.Errors)
}
