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
	"sort"
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
	// Shadow holds the counts of a live run (aftermarket shadow), where A is
	// the primary and B the candidate. It is nil for a replay.
	Shadow *ShadowStats `json:"shadow,omitempty"`
}

// ShadowStats are the counts of a live shadow run. Only cases with a
// difference or an error are listed in Cases; the counts cover every case.
type ShadowStats struct {
	Requests         int64            `json:"requests"`           // requests served from the primary (health checks not counted)
	OutsideWhen      int64            `json:"outside_when"`       // left out by the config's when:
	Mirrored         int64            `json:"mirrored"`           // queued for the candidate
	SampledOut       int64            `json:"sampled_out"`        // left out by --sample
	WritesNotSent    int64            `json:"writes_not_sent"`    // methods other than GET and HEAD, without --include-writes
	Dropped          int64            `json:"dropped_queue_full"` // the queue was full, so the copy was dropped
	NotMirrored      int64            `json:"not_mirrored"`       // matched but couldn't be compared safely (see reasons)
	NotCompared      int64            `json:"not_compared"`       // copied, but the primary's answer couldn't be compared (see reasons)
	NotMirroredWhy   map[string]int64 `json:"not_mirrored_reasons,omitempty"`
	Unfinished       int64            `json:"unfinished_at_stop"` // not compared because shadow stopped (queued, or cut short)
	NotListed        int64            `json:"cases_not_listed"`   // differences and errors beyond the listing limit
	Sample           float64          `json:"sample"`
	Queue            int              `json:"queue"`
	Workers          int              `json:"workers"`
	CandidateTimeout string           `json:"candidate_timeout"`
	Started          string           `json:"started"`
	Updated          string           `json:"updated"`
}

// OK reports whether every case that ran gave the same answer on both
// sides. A replay that compared nothing is not OK: it proves nothing.
func (r *Report) OK() bool { return r.Diff == 0 && r.Errors == 0 && r.Same > 0 }

// Answer is one service's answer to a request: the status, the headers and
// the body (decoded when the transport asked for gzip itself).
type Answer struct {
	Status int
	Header http.Header
	Body   []byte
}

// Request is a request to send with Send: the method, the escaped path and
// raw query as the client sent it, the headers and the body.
type Request struct {
	Method string
	URI    string
	Header http.Header
	Body   []byte
}

// NewClient returns the client replay sends with: no redirects followed,
// gzip answers decoded, a timeout per answer. tr nil means a default
// transport.
func NewClient(timeout time.Duration, tr http.RoundTripper) *http.Client {
	if tr == nil {
		t := httpx.NewTransport(timeout)
		t.DisableCompression = false // gzip answers are compared decoded
		tr = t
	}
	return &http.Client{
		Transport:     tr,
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
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
	client := NewClient(o.Timeout, o.Transport)
	rep := &Report{A: httpx.SafeURL(o.A), B: "recorded responses", Compared: headers}
	if o.B != nil {
		rep.B = httpx.SafeURL(o.B)
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
	req := caseRequest(c)
	a, err := Send(ctx, client, o.A, req, o.SetHeaders, o.MaxBody)
	if err != nil {
		cr.Outcome, cr.Reason = Failed, "A: "+err.Error()
		return
	}
	var b *Answer
	if o.B != nil {
		b, err = Send(ctx, client, o.B, req, o.SetHeaders, o.MaxBody)
		if err != nil {
			cr.Outcome, cr.Reason = Failed, "B: "+err.Error()
			return
		}
	} else {
		e := c.ExpectResponse
		b = &Answer{Status: e.Status, Header: http.Header{}}
		for _, h := range e.Headers {
			b.Header.Add(h[0], h[1])
		}
		if e.Body != nil {
			b.Body = []byte(*e.Body)
		}
	}
	cr.StatusA, cr.StatusB = a.Status, b.Status
	cmp := Comparison{Headers: headers, Ignore: o.Ignore}
	var diffs []string
	if o.B != nil {
		diffs = cmp.Compare(a, b)
	} else {
		diffs = cmp.compare(a, b, true, c.ExpectResponse.Body != nil)
	}
	cr.Diffs = diffs
	cr.Outcome = Same
	if len(diffs) > 0 {
		cr.Outcome = Different
	}
}

// Comparison says what Compare looks at.
type Comparison struct {
	Headers []string  // canonical names of the response headers to compare
	Ignore  []Pattern // JSON paths left out
	// MaskHeaders and MaskJSON name values that must never be shown: a
	// difference there is reported without the values.
	MaskHeaders map[string]bool
	MaskJSON    []Pattern
}

// Compare lists the differences between two answers: status, the chosen
// headers and the bodies (JSON by meaning). No differences means the same.
func (c Comparison) Compare(a, b *Answer) []string { return c.compare(a, b, false, true) }

// compare with recorded set compares a live answer with a recorded one:
// status 0 and headers that weren't recorded are not compared, and the body
// only when one was recorded.
func (c Comparison) compare(a, b *Answer, recorded, bodies bool) []string {
	var diffs []string
	if b.Status != 0 && a.Status != b.Status {
		diffs = append(diffs, fmt.Sprintf("status: %d vs %d", a.Status, b.Status))
	}
	for _, h := range c.Headers {
		if recorded && len(b.Header.Values(h)) == 0 {
			continue // not recorded
		}
		av, bv := strings.Join(a.Header.Values(h), ", "), strings.Join(b.Header.Values(h), ", ")
		if av == bv {
			continue
		}
		if c.MaskHeaders[h] {
			diffs = append(diffs, fmt.Sprintf("header %s: differs (REDACTED)", h))
		} else {
			diffs = append(diffs, fmt.Sprintf("header %s: %q vs %q", h, av, bv))
		}
	}
	if bodies {
		diffs = append(diffs, compareBodies(a.Body, b.Body, c.Ignore, c.MaskJSON)...)
	}
	return diffs
}

func caseRequest(c fixture.Case) Request {
	r := Request{Method: c.Request.Method, URI: c.Request.Path, Header: http.Header{}}
	for _, h := range c.Request.Headers {
		r.Header.Add(h[0], h[1])
	}
	if c.Request.Body != nil {
		r.Body = []byte(*c.Request.Body)
	}
	return r
}

// Send sends r to base the way replay does: base's path is put in front of
// the request's, its query joined, and connection, length and encoding
// headers left for the client to set. setHeaders replace headers of the same
// name. Errors are described in fixed words, without URLs or values.
func Send(ctx context.Context, client *http.Client, base *url.URL, r Request, setHeaders [][2]string, maxBody int64) (*Answer, error) {
	if maxBody <= 0 {
		maxBody = 16 << 20
	}
	path, query, _ := strings.Cut(r.URI, "?")
	u := *base
	u.RawPath = ""
	u.Path = ""
	target := strings.TrimSuffix(base.EscapedPath(), "/") + path
	var body io.Reader
	if len(r.Body) > 0 {
		body = bytes.NewReader(r.Body)
	}
	raw := u.Scheme + "://" + u.Host + target
	if q := joinQuery(base.RawQuery, query); q != "" {
		raw += "?" + q
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, raw, body)
	if err != nil {
		return nil, errors.New("the recorded request could not be built")
	}
	for k, vs := range r.Header {
		switch textproto.CanonicalMIMEHeaderKey(k) {
		case "Host", "Content-Length", "Accept-Encoding", "Connection", "Transfer-Encoding":
			continue
		}
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	for _, h := range setHeaders {
		req.Header.Set(h[0], h[1])
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New(describe(err))
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, errors.New("the answer could not be read: " + describe(err))
	}
	if int64(len(data)) > maxBody {
		return nil, fmt.Errorf("the answer is larger than %d bytes", maxBody)
	}
	return &Answer{Status: resp.StatusCode, Header: resp.Header, Body: data}, nil
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
	if r.Shadow != nil {
		fmt.Fprintf(&b, "# Shadow report\n\n")
		fmt.Fprintf(&b, "- A (primary, answers clients): %s\n- B (candidate, gets copies): %s\n", r.A, r.B)
	} else {
		fmt.Fprintf(&b, "# Replay report\n\n")
		fmt.Fprintf(&b, "- A: %s\n- B: %s\n", r.A, r.B)
	}
	fmt.Fprintf(&b, "- Headers compared: %s\n", strings.Join(r.Compared, ", "))
	if len(r.Ignored) > 0 {
		fmt.Fprintf(&b, "- Ignored: %s\n", strings.Join(r.Ignored, ", "))
	}
	fmt.Fprintf(&b, "\n%s\n", r.Summary())
	if s := r.Shadow; s != nil {
		fmt.Fprintf(&b, "\n## Traffic\n\n")
		fmt.Fprintf(&b, "- Started %s, updated %s\n", s.Started, s.Updated)
		fmt.Fprintf(&b, "- %d request(s) served from A; %d copied to B\n", s.Requests, s.Mirrored)
		fmt.Fprintf(&b, "- Not copied: %d outside when:, %d left out by sampling (%g), %d writes without --include-writes, %d dropped because the queue (%d) was full, %d that couldn't be compared safely\n",
			s.OutsideWhen, s.SampledOut, s.Sample, s.WritesNotSent, s.Dropped, s.Queue, s.NotMirrored)
		if s.NotCompared > 0 {
			fmt.Fprintf(&b, "- Copied but not compared: %d (the primary's answer couldn't be read the way replay reads it)\n", s.NotCompared)
		}
		reasons := make([]string, 0, len(s.NotMirroredWhy))
		for k := range s.NotMirroredWhy {
			reasons = append(reasons, k)
		}
		sort.Strings(reasons)
		for _, k := range reasons {
			fmt.Fprintf(&b, "  - %s: %d\n", k, s.NotMirroredWhy[k])
		}
		if s.Unfinished > 0 {
			fmt.Fprintf(&b, "- %d copy(ies) not compared because shadow stopped\n", s.Unfinished)
		}
		fmt.Fprintf(&b, "- %d worker(s), candidate timeout %s\n", s.Workers, s.CandidateTimeout)
		if s.NotListed > 0 {
			fmt.Fprintf(&b, "- %d more difference(s) or error(s) counted but not listed\n", s.NotListed)
		}
	} else if r.Same+r.Diff+r.Errors == 0 {
		fmt.Fprintf(&b, "\nNothing was compared, so the replay proves nothing. Use --include-writes to send the skipped cases.\n")
	}
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
		r.Same+r.Diff+r.Skipped+r.Errors, r.Same, r.Diff, r.Skipped, r.Errors)
}
