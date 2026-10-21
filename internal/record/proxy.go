package record

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/aftermarket-dev/aftermarket/internal/audit"
	"github.com/aftermarket-dev/aftermarket/internal/fixture"
	"github.com/aftermarket-dev/aftermarket/internal/httpx"
	"github.com/aftermarket-dev/aftermarket/internal/ojson"
)

// Options configure a Recorder.
type Options struct {
	Upstream   *url.URL
	Config     *Config
	Out        io.Writer // the fixture file
	Audit      *audit.Log
	HealthPath string            // "" disables the health check
	Transport  http.RoundTripper // nil for a default transport with the config's timeout
}

// Recorder is the `aftermarket record` HTTP handler.
type Recorder struct {
	opts  Options
	rp    *httputil.ReverseProxy
	out   *caseWriter
	reqID atomic.Uint64
}

type ctxKey struct{}

// exchange is what the recorder keeps about one request it may record.
type exchange struct {
	id        uint64
	method    string
	path      string // decoded, for the audit log
	uri       string // escaped path and raw query, as sent
	queryKeys []string
	header    http.Header
	record    bool // the exchange matches and may be written
	reqTap    *tap // nil when the request has no body
	reqLen    int64
}

// New builds a recorder and writes the fixture file's header.
func New(opts Options) (*Recorder, error) {
	if opts.Upstream == nil || opts.Upstream.Scheme == "" || opts.Upstream.Host == "" {
		return nil, errors.New("upstream must be an absolute URL such as http://localhost:9000")
	}
	if opts.Upstream.Scheme != "http" && opts.Upstream.Scheme != "https" {
		return nil, fmt.Errorf("upstream scheme %q is not http or https", opts.Upstream.Scheme)
	}
	if opts.Config == nil {
		opts.Config = Default()
	}
	if opts.Out == nil {
		return nil, errors.New("no fixture file to write to")
	}
	cw, err := newCaseWriter(opts.Out, httpx.SafeURL(opts.Upstream), opts.Config.MaxCases)
	if err != nil {
		return nil, err
	}
	r := &Recorder{opts: opts, out: cw}
	tr := opts.Transport
	if tr == nil {
		tr = httpx.NewTransport(opts.Config.UpstreamTimeout)
	}
	r.rp = &httputil.ReverseProxy{
		Rewrite:        r.rewrite,
		ModifyResponse: r.modifyResponse,
		ErrorHandler:   r.errorHandler,
		Transport:      tr,
		FlushInterval:  -1,
	}
	return r, nil
}

// Cases returns how many cases have been written.
func (r *Recorder) Cases() int {
	r.out.mu.Lock()
	defer r.out.mu.Unlock()
	return r.out.n
}

// ServeHTTP passes one request through and, when it matches, records it.
func (r *Recorder) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	httpx.KeepRequestBody(w)
	if r.opts.HealthPath != "" && req.URL.Path == r.opts.HealthPath {
		httpx.WriteHealth(w)
		return
	}
	ex := &exchange{
		id:     r.reqID.Add(1),
		method: req.Method,
		path:   req.URL.Path,
		uri:    req.URL.EscapedPath(),
		header: req.Header.Clone(),
	}
	if ex.uri == "" {
		ex.uri = "/" // an absolute-form request with no path goes upstream as /
	}
	if req.URL.RawQuery != "" {
		ex.uri += "?" + req.URL.RawQuery
	}
	query, _ := url.ParseQuery(req.URL.RawQuery)
	for k := range query {
		ex.queryKeys = append(ex.queryKeys, k)
	}
	sort.Strings(ex.queryKeys)

	w2 := r.opts.Config.When
	candidate := !(w2.PathGlob != "" && httpx.UnsafePath(req.URL)) &&
		w2.MatchRequest(req.Method, req.URL.Path, query, req.Header)
	if candidate && r.out.full() {
		r.skip(ex, 0, "max_cases reached")
	} else if candidate {
		ex.record = true
		if req.Body != nil && req.Body != http.NoBody && req.ContentLength != 0 {
			ex.reqTap = &tap{rc: req.Body, max: r.opts.Config.MaxBody}
			ex.reqLen = req.ContentLength
			req.Body = ex.reqTap
		}
	}
	r.rp.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), ctxKey{}, ex)))
}

func (r *Recorder) rewrite(pr *httputil.ProxyRequest) {
	pr.SetURL(r.opts.Upstream)
	// Forward the query string exactly as the client sent it.
	switch {
	case r.opts.Upstream.RawQuery == "":
		pr.Out.URL.RawQuery = pr.In.URL.RawQuery
	case pr.In.URL.RawQuery == "":
		pr.Out.URL.RawQuery = r.opts.Upstream.RawQuery
	default:
		pr.Out.URL.RawQuery = r.opts.Upstream.RawQuery + "&" + pr.In.URL.RawQuery
	}
	for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Forwarded"} {
		if v, ok := pr.In.Header[h]; ok {
			pr.Out.Header[h] = v
		}
	}
}

func (r *Recorder) modifyResponse(resp *http.Response) error {
	ex, _ := resp.Request.Context().Value(ctxKey{}).(*exchange)
	if ex == nil || !ex.record || !r.opts.Config.When.MatchResponse(resp.StatusCode, resp.Header.Get("Content-Type")) {
		return nil
	}
	if resp.StatusCode < 200 {
		// An upgraded connection (101) carries no HTTP body to record, and the
		// proxy needs its raw connection untouched.
		r.skip(ex, resp.StatusCode, "the connection was upgraded, which can't be recorded")
		return nil
	}
	status, header := resp.StatusCode, resp.Header.Clone()
	emptyExpected := resp.ContentLength == 0 || ex.method == http.MethodHead ||
		resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotModified
	t := &tap{rc: resp.Body, max: r.opts.Config.MaxBody}
	t.onDone = func(complete bool) {
		if !complete && t.total() == 0 && emptyExpected {
			complete = true
		}
		r.finish(ex, status, header, t, complete)
	}
	resp.Body = t
	return nil
}

func (r *Recorder) errorHandler(w http.ResponseWriter, req *http.Request, err error) {
	if httpx.ClientGone(req) {
		return
	}
	status, reason := httpx.UpstreamReason(err)
	if ex, _ := req.Context().Value(ctxKey{}).(*exchange); ex != nil {
		r.opts.Audit.Write(audit.Event{
			Event:     audit.UpstreamError,
			Request:   ex.id,
			Method:    ex.method,
			Path:      ex.path,
			QueryKeys: ex.queryKeys,
			Status:    status,
			Reason:    reason,
		})
		if ex.record {
			r.skip(ex, status, "the upstream failed, so there is no answer to record")
		}
	}
	httpx.WriteUpstreamError(w, status)
}

func (r *Recorder) skip(ex *exchange, status int, reason string) {
	r.opts.Audit.Write(audit.Event{
		Event:     audit.Skipped,
		Request:   ex.id,
		Method:    ex.method,
		Path:      ex.path,
		QueryKeys: ex.queryKeys,
		Status:    status,
		Reason:    reason,
	})
}

// finish decides whether a finished exchange can be stored safely, redacts
// it and writes it.
func (r *Recorder) finish(ex *exchange, status int, respHdr http.Header, resp *tap, complete bool) {
	cfg := r.opts.Config
	reqBody := ""
	if ex.reqTap != nil {
		data, over, eof := ex.reqTap.result()
		if ex.reqLen > 0 && int64(len(data)) == ex.reqLen {
			eof = true
		}
		switch {
		case over:
			r.skip(ex, status, "request body is larger than max_body")
			return
		case !eof:
			r.skip(ex, status, "request body was not read to the end")
			return
		}
		b, why := decode(data, ex.header.Get("Content-Encoding"), cfg.MaxBody)
		if why != "" {
			r.skip(ex, status, "request "+why)
			return
		}
		reqBody = b
	}
	data, over, _ := resp.result()
	switch {
	case over:
		r.skip(ex, status, "response body is larger than max_body")
		return
	case !complete:
		r.skip(ex, status, "response body was cut off")
		return
	}
	respBody, why := decode(data, respHdr.Get("Content-Encoding"), cfg.MaxBody)
	if why != "" {
		r.skip(ex, status, "response "+why)
		return
	}

	reqCT, respCT := ex.header.Get("Content-Type"), respHdr.Get("Content-Type")
	reqHdr := cleanHeaders(ex.header, "Host", "Accept-Encoding")
	respHdr = cleanHeaders(respHdr, "Date")
	if !textHeaders(reqHdr) || !textHeaders(respHdr) || !utf8.ValidString(ex.uri) {
		r.skip(ex, status, "a header or the path is not valid UTF-8, so it can't be stored exactly")
		return
	}
	if fixture.CheckPath(ex.uri) != nil {
		r.skip(ex, status, "the request path can't be stored in a fixture")
		return
	}
	redactHeaders(reqHdr, cfg.RedactHeaders)
	redactHeaders(respHdr, cfg.RedactHeaders)
	uri := ex.uri
	if p, q, ok := strings.Cut(uri, "?"); ok {
		if nq, changed := redactQuery(q, cfg.RedactQuery); changed {
			uri = p + "?" + nq
		}
	}
	if loc := respHdr.Get("Location"); loc != "" {
		if p, q, ok := strings.Cut(loc, "?"); ok {
			if nq, changed := redactQuery(q, cfg.RedactQuery); changed {
				respHdr.Set("Location", p+"?"+nq)
			}
		}
	}
	if reqBody, why = redactBody(reqBody, reqCT, cfg.RedactJSON); why != "" {
		r.skip(ex, status, "request "+why)
		return
	}
	if respBody, why = redactBody(respBody, respCT, cfg.RedactJSON); why != "" {
		r.skip(ex, status, "response "+why)
		return
	}
	ok, why := r.out.write(recordedCase{method: ex.method, uri: uri, reqHdr: reqHdr, reqBody: reqBody,
		status: status, respHdr: respHdr, respBody: respBody})
	if !ok {
		r.skip(ex, status, why)
		return
	}
	r.opts.Audit.Write(audit.Event{
		Event:     audit.Recorded,
		Request:   ex.id,
		Method:    ex.method,
		Path:      ex.path,
		QueryKeys: ex.queryKeys,
		Status:    status,
	})
}

// textHeaders reports whether every header value is valid UTF-8, which a
// fixture can store exactly.
func textHeaders(h http.Header) bool {
	for k, vs := range h {
		if !utf8.ValidString(k) {
			return false
		}
		for _, v := range vs {
			if !utf8.ValidString(v) {
				return false
			}
		}
	}
	return true
}

// cleanHeaders drops headers a fixture must not carry: connection handling,
// lengths and encodings (bodies are stored decoded), and any extra names.
func cleanHeaders(h http.Header, extra ...string) http.Header {
	out := h.Clone()
	for _, k := range []string{"Connection", "Keep-Alive", "Proxy-Connection", "Te", "Trailer",
		"Transfer-Encoding", "Upgrade", "Content-Length", "Content-Encoding"} {
		delete(out, k)
	}
	for _, k := range extra {
		delete(out, k)
	}
	for k := range out {
		if strings.HasPrefix(k, ":") {
			delete(out, k)
		}
	}
	return out
}

// decode turns a body into the text a fixture stores, or says why it can't.
func decode(data []byte, encoding string, max int64) (string, string) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "identity":
	case "gzip":
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return "", "gzip body could not be decoded"
		}
		plain, err := io.ReadAll(io.LimitReader(zr, max+1))
		if err != nil {
			return "", "gzip body could not be decoded"
		}
		if int64(len(plain)) > max {
			return "", "body is larger than max_body once decompressed"
		}
		data = plain
	default:
		return "", "body is compressed with " + encoding + ", which can't be stored"
	}
	if !utf8.Valid(data) {
		return "", "body is not text (not valid UTF-8)"
	}
	return string(data), ""
}

// redactBody replaces listed JSON values in any body that is JSON, whatever
// its Content-Type says. A body labelled JSON that can't be read can't be
// checked, so it isn't stored.
func redactBody(body, contentType string, paths [][]string) (string, string) {
	if len(paths) == 0 || strings.TrimSpace(body) == "" {
		return body, ""
	}
	doc, err := ojson.Decode([]byte(body))
	if err != nil {
		if httpx.IsJSON(contentType) {
			return "", "JSON body could not be read to redact it"
		}
		return body, "" // not JSON, so no JSON field to redact
	}
	doc, n := redactJSON(doc, paths)
	if n == 0 {
		return body, ""
	}
	b, err := ojson.Encode(doc)
	if err != nil {
		return "", "JSON body could not be written after redacting it"
	}
	return string(b), ""
}

// tap passes a body through untouched and keeps a copy up to max bytes.
type tap struct {
	rc     io.ReadCloser
	max    int64
	onDone func(complete bool)

	mu   sync.Mutex
	buf  bytes.Buffer
	n    int64
	over bool
	eof  bool
	once sync.Once
}

func (t *tap) Read(p []byte) (int, error) {
	n, err := t.rc.Read(p)
	t.mu.Lock()
	if n > 0 {
		t.n += int64(n)
		if !t.over {
			if t.n > t.max {
				t.over = true
				t.buf = bytes.Buffer{}
			} else {
				t.buf.Write(p[:n])
			}
		}
	}
	if err == io.EOF {
		t.eof = true
	}
	t.mu.Unlock()
	if err == io.EOF {
		t.done(true)
	}
	return n, err
}

func (t *tap) Close() error {
	err := t.rc.Close()
	t.mu.Lock()
	eof := t.eof
	t.mu.Unlock()
	t.done(eof)
	return err
}

func (t *tap) done(complete bool) {
	if t.onDone != nil {
		t.once.Do(func() { t.onDone(complete) })
	}
}

func (t *tap) total() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.n
}

func (t *tap) result() ([]byte, bool, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]byte(nil), t.buf.Bytes()...), t.over, t.eof
}
