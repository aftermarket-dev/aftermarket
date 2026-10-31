// Package shadow is AfterDiff's live mode. Every request is served from the
// primary service through record's pass-through proxy, unchanged. When the
// answer has gone out, a copy of the exchange goes into a bounded queue, and
// workers send the request to the candidate service and compare the two
// answers with replay's comparison. The differences are kept in a report in
// replay's format, rewritten as it grows.
//
// The candidate can't touch the primary path: the copy is queued without
// waiting, a full queue drops it and counts it, and the candidate is only
// called from the workers, with its own client and timeout.
package shadow

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aftermarket-dev/aftermarket/internal/audit"
	"github.com/aftermarket-dev/aftermarket/internal/httpx"
	"github.com/aftermarket-dev/aftermarket/internal/record"
	"github.com/aftermarket-dev/aftermarket/internal/replay"
)

// Defaults.
const (
	DefaultQueue            = 64
	DefaultWorkers          = 4
	DefaultCandidateTimeout = 5 * time.Second
	DefaultMaxListed        = 1000
	DefaultReportEvery      = time.Second
)

// Options configure a Shadow.
type Options struct {
	Primary, Candidate *url.URL
	Config             *record.Config // when, redact, limits; nil for record.Default()
	Audit              *audit.Log
	HealthPath         string

	Sample           float64 // share of matching requests copied, 0 < Sample <= 1; 0 means 1
	Queue            int     // copies waiting for a worker
	Workers          int
	CandidateTimeout time.Duration
	IncludeWrites    bool
	Ignore           []replay.Pattern
	Headers          []string // response headers to compare; nil means Content-Type
	MaxListed        int      // differences and errors listed in the report

	ReportPath  string        // Markdown report, rewritten as it grows; "" for none
	JSONPath    string        // JSON report; "" for none
	ReportEvery time.Duration // at most one rewrite per interval

	PrimaryTransport   http.RoundTripper
	CandidateTransport http.RoundTripper
}

// Shadow is the `aftermarket shadow` HTTP handler.
type Shadow struct {
	opts   Options
	rec    *record.Recorder
	client *http.Client
	cmp    replay.Comparison
	queue  chan *record.Exchange
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	busy   atomic.Int64

	writerDone chan struct{}
	stopWriter chan struct{}
	kick       chan struct{}
	closeOnce  sync.Once

	mu      sync.Mutex
	matched uint64 // requests offered to sampling
	rep     replay.Report
	dirty   bool
	saveErr error
}

// New builds a shadow proxy and starts its workers. Close stops them and
// writes the final report.
func New(o Options) (*Shadow, error) {
	if o.Primary == nil || o.Primary.Scheme == "" || o.Primary.Host == "" {
		return nil, errors.New("primary must be an absolute URL such as http://localhost:9000")
	}
	if o.Candidate == nil || o.Candidate.Scheme == "" || o.Candidate.Host == "" {
		return nil, errors.New("candidate must be an absolute URL such as http://localhost:9001")
	}
	if o.Candidate.Scheme != "http" && o.Candidate.Scheme != "https" {
		return nil, fmt.Errorf("candidate scheme %q is not http or https", o.Candidate.Scheme)
	}
	if o.Config == nil {
		o.Config = record.Default()
	}
	if o.Sample == 0 {
		o.Sample = 1
	}
	if !(o.Sample > 0 && o.Sample <= 1) {
		return nil, errors.New("sample must be more than 0 and at most 1, such as 0.1 for one request in ten")
	}
	if o.Queue <= 0 {
		o.Queue = DefaultQueue
	}
	if o.Workers <= 0 {
		o.Workers = DefaultWorkers
	}
	if o.CandidateTimeout <= 0 {
		o.CandidateTimeout = DefaultCandidateTimeout
	}
	if o.MaxListed <= 0 {
		o.MaxListed = DefaultMaxListed
	}
	if o.ReportEvery <= 0 {
		o.ReportEvery = DefaultReportEvery
	}
	headers := o.Headers
	if headers == nil {
		headers = []string{"Content-Type"}
	}
	headers = append([]string(nil), headers...)
	for i, h := range headers {
		headers[i] = textproto.CanonicalMIMEHeaderKey(h)
	}
	mask := make([]replay.Pattern, 0, len(o.Config.RedactJSON))
	for _, p := range o.Config.RedactJSON {
		mask = append(mask, replay.Pattern(p))
	}
	s := &Shadow{
		opts:       o,
		client:     replay.NewClient(o.CandidateTimeout, o.CandidateTransport),
		cmp:        replay.Comparison{Headers: headers, Ignore: o.Ignore, MaskHeaders: o.Config.RedactHeaders, MaskJSON: mask},
		queue:      make(chan *record.Exchange, o.Queue),
		writerDone: make(chan struct{}),
		stopWriter: make(chan struct{}),
		kick:       make(chan struct{}, 1),
	}
	rec, err := record.New(record.Options{
		Upstream:   o.Primary,
		Config:     o.Config,
		Audit:      o.Audit,
		HealthPath: o.HealthPath,
		Transport:  o.PrimaryTransport,
		Select:     s.selectRequest,
		Mirror:     s.mirror,
		Skipped:    s.skipped,
	})
	if err != nil {
		return nil, err
	}
	s.rec = rec
	now := time.Now().UTC().Format(time.RFC3339)
	s.rep = replay.Report{
		A:        httpx.SafeURL(o.Primary),
		B:        httpx.SafeURL(o.Candidate),
		Compared: headers,
		Cases:    []replay.CaseReport{},
		Shadow: &replay.ShadowStats{
			Sample:           o.Sample,
			Queue:            o.Queue,
			Workers:          o.Workers,
			CandidateTimeout: o.CandidateTimeout.String(),
			Started:          now,
			Updated:          now,
			NotMirroredWhy:   map[string]int64{},
		},
	}
	for _, p := range o.Ignore {
		s.rep.Ignored = append(s.rep.Ignored, strings.Join(p, "."))
	}
	if err := s.save(); err != nil { // fail early on a report path that can't be written
		return nil, err
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	for i := 0; i < o.Workers; i++ {
		s.wg.Add(1)
		go s.worker()
	}
	go s.writer()
	return s, nil
}

// ServeHTTP serves the request from the primary. A copy may be queued for
// the candidate once the answer is complete.
func (s *Shadow) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if s.opts.HealthPath == "" || req.URL.Path != s.opts.HealthPath {
		s.mu.Lock()
		s.rep.Shadow.Requests++
		s.mu.Unlock()
	}
	s.rec.ServeHTTP(w, req)
}

// selectRequest decides, for a request that matches when:, whether a copy
// will be kept: writes only with IncludeWrites, then sampling by a counter,
// so a share of 0.1 takes exactly one request in ten.
func (s *Shadow) selectRequest(req *http.Request) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.Method != http.MethodGet && req.Method != http.MethodHead && !s.opts.IncludeWrites {
		s.rep.Shadow.WritesNotSent++
		return false
	}
	s.matched++
	n := float64(s.matched)
	if math.Floor(n*s.opts.Sample+1e-9) > math.Floor((n-1)*s.opts.Sample+1e-9) {
		return true
	}
	s.rep.Shadow.SampledOut++
	return false
}

// mirror runs on the request's goroutine: it must never wait.
func (s *Shadow) mirror(ex *record.Exchange) {
	select {
	case s.queue <- ex:
		s.mu.Lock()
		s.rep.Shadow.Mirrored++
		s.dirty = true
		s.mu.Unlock()
	default:
		s.mu.Lock()
		s.rep.Shadow.Dropped++
		s.dirty = true
		s.mu.Unlock()
	}
}

func (s *Shadow) skipped(reason string) {
	s.mu.Lock()
	s.rep.Shadow.NotMirrored++
	s.rep.Shadow.NotMirroredWhy[reason]++
	s.dirty = true
	s.mu.Unlock()
}

func (s *Shadow) worker() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case ex := <-s.queue:
			s.busy.Add(1)
			s.compare(ex)
			s.busy.Add(-1)
		}
	}
}

// compare sends one copy to the candidate and adds the outcome to the report.
func (s *Shadow) compare(ex *record.Exchange) {
	cfg := s.opts.Config
	primaryBody, why := decode(ex.RespBody, ex.RespHeader.Get("Content-Encoding"), cfg.MaxBody)
	if why != "" {
		s.mu.Lock()
		s.rep.Shadow.NotCompared++
		s.rep.Shadow.NotMirroredWhy["primary answer "+why]++
		s.dirty = true
		s.mu.Unlock()
		return
	}
	a := &replay.Answer{Status: ex.Status, Header: ex.RespHeader, Body: primaryBody}
	uri := cfg.RedactURI(ex.URI)
	path, _, _ := strings.Cut(uri, "?")
	cr := replay.CaseReport{Name: fmt.Sprintf("%d %s %s", ex.ID, ex.Method, path), Method: ex.Method, Path: uri, StatusA: ex.Status}
	b, err := replay.Send(s.ctx, s.client, s.opts.Candidate, replay.Request{Method: ex.Method, URI: ex.URI, Header: requestHeaders(ex.Header), Body: ex.Body}, nil, cfg.MaxBody)
	if err != nil {
		cr.Outcome, cr.Reason = replay.Failed, "B: "+err.Error()
	} else {
		cr.StatusB = b.Status
		cr.Diffs = s.cmp.Compare(a, b)
		cr.Outcome = replay.Same
		if len(cr.Diffs) > 0 {
			cr.Outcome = replay.Different
		}
	}
	reason := cr.Outcome
	if cr.Outcome == replay.Failed {
		reason = "error: " + cr.Reason
	}
	s.opts.Audit.Write(audit.Event{Event: audit.Compared, Request: ex.ID, Method: ex.Method, Path: ex.Path,
		QueryKeys: ex.QueryKeys, Status: ex.Status, Reason: reason})
	s.mu.Lock()
	switch cr.Outcome {
	case replay.Same:
		s.rep.Same++
	case replay.Different:
		s.rep.Diff++
	default:
		s.rep.Errors++
	}
	if cr.Outcome != replay.Same {
		if len(s.rep.Cases) < s.opts.MaxListed {
			s.rep.Cases = append(s.rep.Cases, cr)
		} else {
			s.rep.Shadow.NotListed++
		}
	}
	s.dirty = true
	s.mu.Unlock()
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// writer rewrites the report at most once per ReportEvery while it changes.
func (s *Shadow) writer() {
	defer close(s.writerDone)
	t := time.NewTicker(s.opts.ReportEvery)
	defer t.Stop()
	for {
		select {
		case <-s.stopWriter:
			return
		case <-t.C:
			s.mu.Lock()
			dirty := s.dirty
			s.mu.Unlock()
			if dirty {
				_ = s.save()
			}
		}
	}
}

// Report returns a copy of the report as it stands.
func (s *Shadow) Report() replay.Report {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot()
}

func (s *Shadow) snapshot() replay.Report {
	r := s.rep
	r.Cases = append([]replay.CaseReport(nil), s.rep.Cases...)
	st := *s.rep.Shadow
	st.NotMirroredWhy = map[string]int64{}
	for k, v := range s.rep.Shadow.NotMirroredWhy {
		st.NotMirroredWhy[k] = v
	}
	r.Shadow = &st
	return r
}

// Idle reports whether nothing is queued or being compared.
func (s *Shadow) Idle() bool { return len(s.queue) == 0 && s.busy.Load() == 0 }

// save writes both reports, each in one step (a temporary file renamed over
// the old one), so a reader never sees half a report.
func (s *Shadow) save() error {
	s.mu.Lock()
	s.rep.Shadow.Updated = time.Now().UTC().Format(time.RFC3339)
	r := s.snapshot()
	s.dirty = false
	s.mu.Unlock()
	var err error
	if s.opts.ReportPath != "" {
		err = writeAtomic(s.opts.ReportPath, r.WriteMarkdown)
	}
	if s.opts.JSONPath != "" {
		if e := writeAtomic(s.opts.JSONPath, r.WriteJSON); err == nil {
			err = e
		}
	}
	s.mu.Lock()
	s.saveErr = err
	s.mu.Unlock()
	return err
}

// Close waits up to the candidate timeout for queued copies, stops the
// workers and writes the final report. Call it after the server has stopped.
func (s *Shadow) Close() error {
	var err error
	s.closeOnce.Do(func() {
		deadline := time.Now().Add(s.opts.CandidateTimeout + time.Second)
		for !s.Idle() && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		s.cancel()
		s.wg.Wait()
		close(s.stopWriter)
		<-s.writerDone
		s.mu.Lock()
		s.rep.Shadow.Unfinished += int64(len(s.queue))
		s.mu.Unlock()
		err = s.save()
	})
	return err
}

// requestHeaders drops the headers that belong to the client's connection
// to the proxy, as a recording would.
func requestHeaders(h http.Header) http.Header {
	out := h.Clone()
	for _, k := range []string{"Connection", "Keep-Alive", "Proxy-Connection", "Te", "Trailer", "Upgrade"} {
		delete(out, k)
	}
	for _, k := range h.Values("Connection") {
		for _, name := range strings.Split(k, ",") {
			delete(out, textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(name)))
		}
	}
	return out
}

func writeAtomic(path string, write func(io.Writer) error) error {
	dir, base := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	f, err := os.CreateTemp(dir, "."+base+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if err := write(f); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// decode returns the body as the candidate's client would see it: gzip is
// decoded, as replay's client does; other encodings can't be compared.
func decode(data []byte, encoding string, max int64) ([]byte, string) {
	if len(data) == 0 {
		return data, "" // HEAD, 204 and 304 answers carry no body to decode
	}
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "identity":
		return data, ""
	case "gzip":
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, "is gzip that could not be decoded"
		}
		plain, err := io.ReadAll(io.LimitReader(zr, max+1))
		if err != nil {
			return nil, "is gzip that could not be decoded"
		}
		if int64(len(plain)) > max {
			return nil, "is larger than max_body once decompressed"
		}
		return plain, ""
	}
	return nil, "is compressed with an encoding other than gzip"
}
