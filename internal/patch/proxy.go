// Package patch is AfterPatch: a reverse proxy that applies repair rules to
// HTTP requests and responses on their way between a client and an upstream
// service, neither of which is changed.
package patch

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aftermarket-dev/aftermarket/internal/audit"
	"github.com/aftermarket-dev/aftermarket/internal/ojson"
	"github.com/aftermarket-dev/aftermarket/internal/rules"
)

// DefaultHealthPath is answered by the proxy itself. It sits under
// /_aftermarket/ so it never hides an upstream path such as /healthz.
const DefaultHealthPath = "/_aftermarket/healthz"

// Options configure a Proxy.
type Options struct {
	Upstream   *url.URL
	Config     *rules.Config
	DryRun     bool
	Audit      *audit.Log
	HealthPath string            // "" disables the health check
	Transport  http.RoundTripper // nil for a default transport with the config's timeout
}

// Proxy is the AfterPatch HTTP handler.
type Proxy struct {
	opts  Options
	rp    *httputil.ReverseProxy
	reqID atomic.Uint64
}

type ctxKey struct{}

// state carries what the proxy knows about one request between phases.
type state struct {
	id        uint64
	method    string
	path      string // the path the client sent, decoded
	rawQuery  string // the query string exactly as the client sent it
	queryKeys []string
	reqRules  []*rules.Rule
	respRules []*rules.Rule
}

// New builds a proxy.
func New(opts Options) (*Proxy, error) {
	if opts.Upstream == nil || opts.Upstream.Scheme == "" || opts.Upstream.Host == "" {
		return nil, errors.New("upstream must be an absolute URL such as http://localhost:9000")
	}
	if opts.Upstream.Scheme != "http" && opts.Upstream.Scheme != "https" {
		return nil, fmt.Errorf("upstream scheme %q is not http or https", opts.Upstream.Scheme)
	}
	if opts.Config == nil {
		return nil, errors.New("no rules loaded")
	}
	p := &Proxy{opts: opts}
	tr := opts.Transport
	if tr == nil {
		tr = &http.Transport{
			Proxy:                 nil,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          256,
			MaxIdleConnsPerHost:   64,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: opts.Config.UpstreamTimeout,
			ExpectContinueTimeout: 1 * time.Second,
			// Pass bodies through exactly as the upstream sent them.
			DisableCompression: true,
		}
	}
	p.rp = &httputil.ReverseProxy{
		Rewrite:        p.rewrite,
		ModifyResponse: p.modifyResponse,
		ErrorHandler:   p.errorHandler,
		Transport:      tr,
		FlushInterval:  -1,
	}
	return p, nil
}

func (p *Proxy) eventKind() string {
	if p.opts.DryRun {
		return audit.WouldChange
	}
	return audit.Changed
}

// unsafePath reports whether a path contains "." or ".." segments or an
// encoded slash. Upstreams often resolve these differently from a glob, so
// rules with a path condition never match such requests.
func unsafePath(u *url.URL) bool {
	if strings.Contains(strings.ToLower(u.EscapedPath()), "%2f") {
		return true
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if seg == "." || seg == ".." {
			return true
		}
	}
	return false
}

// ServeHTTP handles one request.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p.opts.HealthPath != "" && r.URL.Path == p.opts.HealthPath {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
		return
	}
	st := &state{
		id:       p.reqID.Add(1),
		method:   r.Method,
		path:     r.URL.Path,
		rawQuery: r.URL.RawQuery,
	}
	query, _ := url.ParseQuery(r.URL.RawQuery) // keeps the parts that parse
	for k := range query {
		st.queryKeys = append(st.queryKeys, k)
	}
	sort.Strings(st.queryKeys)

	pathUnsafe := unsafePath(r.URL)
	for _, rule := range p.opts.Config.Rules {
		if rule.When.PathGlob != "" && pathUnsafe {
			continue
		}
		if !rule.When.MatchRequest(r.Method, r.URL.Path, query, r.Header) {
			continue
		}
		if rule.Request != nil {
			st.reqRules = append(st.reqRules, rule)
		}
		if rule.Response != nil {
			st.respRules = append(st.respRules, rule)
		}
	}
	ctx := context.WithValue(r.Context(), ctxKey{}, st)
	p.rp.ServeHTTP(w, r.WithContext(ctx))
}

func (p *Proxy) event(st *state, rule *rules.Rule, phase, kind string, status int, applied, notApplied []string, reason string) {
	p.opts.Audit.Write(audit.Event{
		Event:      kind,
		Request:    st.id,
		Rule:       rule.ID,
		Phase:      phase,
		Method:     st.method,
		Path:       st.path,
		QueryKeys:  st.queryKeys,
		Status:     status,
		Actions:    applied,
		NotApplied: notApplied,
		Reason:     reason,
	})
}

// finish writes the audit event for one rule. bodySkip is set when the rule
// has JSON operations that could not run.
func (p *Proxy) finish(st *state, rule *rules.Rule, phase string, status int, applied, notApplied []string, bodySkip string) {
	switch {
	case bodySkip != "" && len(applied) == 0:
		p.event(st, rule, phase, audit.Skipped, status, nil, notApplied, bodySkip)
	case len(applied) == 0:
		p.event(st, rule, phase, audit.Unchanged, status, nil, notApplied, "")
	default:
		p.event(st, rule, phase, p.eventKind(), status, applied, notApplied, bodySkip)
	}
}

// rewrite builds the outgoing request and applies request rules to it.
func (p *Proxy) rewrite(pr *httputil.ProxyRequest) {
	st, _ := pr.In.Context().Value(ctxKey{}).(*state)
	pr.SetURL(p.opts.Upstream)
	if st == nil {
		return
	}
	// Forward the query string exactly as the client sent it.
	switch {
	case p.opts.Upstream.RawQuery == "":
		pr.Out.URL.RawQuery = st.rawQuery
	case st.rawQuery == "":
		pr.Out.URL.RawQuery = p.opts.Upstream.RawQuery
	default:
		pr.Out.URL.RawQuery = p.opts.Upstream.RawQuery + "&" + st.rawQuery
	}
	// Keep forwarding headers the client sent; Rewrite strips them by default.
	for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Forwarded"} {
		if v, ok := pr.In.Header[h]; ok {
			pr.Out.Header[h] = v
		}
	}
	if len(st.reqRules) > 0 {
		p.requestPhase(pr, st)
	}
}

// requestPhase applies request rules to the outgoing request. In a dry run
// the changes are worked out on copies and only logged.
func (p *Proxy) requestPhase(pr *httputil.ProxyRequest, st *state) {
	out := pr.Out
	header := out.Header
	if p.opts.DryRun {
		header = out.Header.Clone()
	}
	escPath := pr.In.URL.EscapedPath()
	query, _ := url.ParseQuery(st.rawQuery)

	needBody := false
	for _, rule := range st.reqRules {
		if rule.Request.HasBodyOps() {
			needBody = true
		}
	}
	var br *bodyRead
	var doc any
	decoded := false
	bodySkip := ""
	if needBody {
		switch {
		case !isJSONType(out.Header.Get("Content-Type")):
			bodySkip = "request body is not JSON"
		case out.Body == nil || out.Body == http.NoBody:
			bodySkip = "request has no body"
		default:
			var err error
			br, err = readBody(out.Body, p.opts.Config.MaxBody)
			out.Body = br.restore() // the original bytes stay available either way
			switch {
			case errors.Is(err, errTooLarge):
				bodySkip = "request body is larger than max_body"
			case err != nil:
				bodySkip = "request body could not be read"
			default:
				if reason, ok := br.decode(out.Header.Get("Content-Encoding"), p.opts.Config.MaxBody); !ok {
					bodySkip = reason
				} else if d, err := ojson.Decode(br.plain); err != nil {
					bodySkip = "request body is not JSON that can be changed safely (" + err.Error() + ")"
				} else {
					doc, decoded = d, true
				}
			}
		}
	}

	newPath := escPath
	bodyChanged := false
	for _, rule := range st.reqRules {
		a := rule.Request
		var applied, notApplied []string
		if a.RewritePath != nil {
			np, desc, ok := rewritePath(a.RewritePath, newPath, query)
			switch {
			case !ok:
				notApplied = append(notApplied, desc)
			case np == newPath:
				notApplied = append(notApplied, desc+" (already in place)")
			default:
				newPath = np
				applied = append(applied, desc)
			}
		}
		ha, hn := applyHeaders(header, a)
		applied = append(applied, ha...)
		notApplied = append(notApplied, hn...)
		skip := ""
		if a.HasBodyOps() {
			if !decoded {
				skip = bodySkip
				notApplied = append(notApplied, "json changes ("+bodySkip+")")
			} else {
				res := applyJSON(doc, a.JSON)
				if len(res.applied) > 0 {
					bodyChanged = true
				}
				applied = append(applied, res.applied...)
				notApplied = append(notApplied, res.notApplied...)
			}
		}
		p.finish(st, rule, "request", 0, applied, notApplied, skip)
	}
	if p.opts.DryRun {
		return
	}
	if newPath != escPath {
		plain, err := url.PathUnescape(newPath)
		if err == nil {
			out.URL.Path = joinPath(p.opts.Upstream.Path, plain)
			out.URL.RawPath = joinPath(p.opts.Upstream.EscapedPath(), newPath)
		}
	}
	if bodyChanged {
		plain, err := ojson.Encode(doc)
		if err == nil {
			body := encodeBody(plain, br.gzipped)
			out.Body = ioNop(body)
			out.ContentLength = int64(len(body))
			out.Header.Set("Content-Length", strconv.Itoa(len(body)))
			out.TransferEncoding = nil
			out.GetBody = nil
			dropBodyValidators(out.Header)
		}
	}
}

// rewritePath applies one rewrite_path action to an escaped path and returns
// the new escaped path.
func rewritePath(rw *rules.Rewrite, escPath string, query url.Values) (string, string, bool) {
	desc := "rewrite path"
	if !rw.From.MatchString(escPath) {
		return escPath, desc + " (from did not match)", false
	}
	to := rw.To
	for _, name := range rw.Tokens {
		vals, ok := query[name]
		if !ok || len(vals) == 0 || vals[0] == "" {
			return escPath, desc + " (query parameter " + name + " missing)", false
		}
		v := vals[0]
		if v == "." || v == ".." || strings.ContainsAny(v, "/\\") {
			return escPath, desc + " (query parameter " + name + " is not a safe path segment)", false
		}
		to = strings.ReplaceAll(to, "{query."+name+"}", strings.ReplaceAll(url.PathEscape(v), "$", "$$"))
	}
	np := rw.From.ReplaceAllString(escPath, to)
	if !strings.HasPrefix(np, "/") {
		return escPath, desc + " (result does not start with /)", false
	}
	for _, seg := range strings.Split(np, "/") {
		plain, err := url.PathUnescape(seg)
		if err != nil {
			return escPath, desc + " (result is not a valid path)", false
		}
		if plain == "." || plain == ".." {
			return escPath, desc + " (result contains . or .. segments)", false
		}
	}
	return np, desc, true
}

func joinPath(base, p string) string {
	if base == "" || base == "/" {
		return p
	}
	return strings.TrimSuffix(base, "/") + p
}

// dropBodyValidators removes headers that describe the old body bytes.
func dropBodyValidators(h http.Header) {
	for _, k := range []string{"Etag", "Content-Md5", "Digest", "Content-Digest", "Repr-Digest"} {
		h.Del(k)
	}
}

// bodyTimeoutError is returned when a body a rule needs does not arrive within
// upstream_timeout.
type bodyTimeoutError struct{}

func (bodyTimeoutError) Error() string   { return "upstream body did not arrive within upstream_timeout" }
func (bodyTimeoutError) Timeout() bool   { return true }
func (bodyTimeoutError) Temporary() bool { return true }

// readBodyWithin is readBody with a deadline. On timeout the body is closed.
func readBodyWithin(body io.ReadCloser, max int64, d time.Duration) (*bodyRead, error) {
	type result struct {
		br  *bodyRead
		err error
	}
	ch := make(chan result, 1)
	go func() {
		br, err := readBody(body, max)
		ch <- result{br, err}
	}()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case r := <-ch:
		return r.br, r.err
	case <-t.C:
		_ = body.Close()
		<-ch
		return nil, bodyTimeoutError{}
	}
}

// modifyResponse applies response rules.
func (p *Proxy) modifyResponse(resp *http.Response) error {
	st, _ := resp.Request.Context().Value(ctxKey{}).(*state)
	if st == nil || len(st.respRules) == 0 {
		return nil
	}
	var matched []*rules.Rule
	for _, rule := range st.respRules {
		if rule.When.MatchResponse(resp.StatusCode, resp.Header.Get("Content-Type")) {
			matched = append(matched, rule)
		}
	}
	if len(matched) == 0 {
		return nil
	}

	header := resp.Header
	if p.opts.DryRun {
		header = resp.Header.Clone()
	}

	needBody := false
	for _, rule := range matched {
		if rule.Response.HasBodyOps() {
			needBody = true
		}
	}
	var br *bodyRead
	var doc any
	decoded := false
	bodySkip := ""
	if needBody {
		switch {
		case st.method == http.MethodHead || resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotModified || resp.StatusCode < 200:
			bodySkip = "response has no body"
		case !isJSONType(resp.Header.Get("Content-Type")):
			bodySkip = "response body is not JSON"
		default:
			var err error
			br, err = readBodyWithin(resp.Body, p.opts.Config.MaxBody, p.opts.Config.UpstreamTimeout)
			if errors.As(err, &bodyTimeoutError{}) {
				return err // answered with 504 by errorHandler
			}
			resp.Body = br.restore()
			switch {
			case errors.Is(err, errTooLarge):
				bodySkip = "response body is larger than max_body"
			case err != nil:
				bodySkip = "response body could not be read"
			default:
				if reason, ok := br.decode(resp.Header.Get("Content-Encoding"), p.opts.Config.MaxBody); !ok {
					bodySkip = reason
				} else if d, err := ojson.Decode(br.plain); err != nil {
					bodySkip = "response body is not JSON that can be changed safely (" + err.Error() + ")"
				} else {
					doc, decoded = d, true
				}
			}
		}
	}

	bodyChanged := false
	for _, rule := range matched {
		a := rule.Response
		applied, notApplied := applyHeaders(header, a)
		skip := ""
		if a.HasBodyOps() {
			if !decoded {
				skip = bodySkip
				notApplied = append(notApplied, "json changes ("+bodySkip+")")
			} else {
				res := applyJSON(doc, a.JSON)
				if len(res.applied) > 0 {
					bodyChanged = true
				}
				applied = append(applied, res.applied...)
				notApplied = append(notApplied, res.notApplied...)
			}
		}
		p.finish(st, rule, "response", resp.StatusCode, applied, notApplied, skip)
	}

	if bodyChanged && !p.opts.DryRun {
		plain, err := ojson.Encode(doc)
		if err == nil {
			out := encodeBody(plain, br.gzipped)
			resp.Body = ioNop(out)
			resp.ContentLength = int64(len(out))
			resp.Header.Set("Content-Length", strconv.Itoa(len(out)))
			resp.TransferEncoding = nil
			// Trailers announced for the old chunked body cannot follow a
			// body with a length.
			resp.Header.Del("Trailer")
			resp.Trailer = nil
			dropBodyValidators(resp.Header)
		}
	}
	return nil
}

func ioNop(b []byte) *nopBody { return &nopBody{Reader: bytes.NewReader(b)} }

type nopBody struct{ *bytes.Reader }

func (nopBody) Close() error { return nil }

// upstreamReason turns an upstream error into a fixed description, so error
// text (which can quote upstream bytes) never reaches the audit log.
func upstreamReason(err error) (int, string) {
	var ne net.Error
	if errors.As(err, &bodyTimeoutError{}) {
		return http.StatusGatewayTimeout, "upstream body did not arrive within upstream_timeout"
	}
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return http.StatusGatewayTimeout, "upstream did not answer within upstream_timeout"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return http.StatusBadGateway, "upstream host name could not be resolved"
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return http.StatusBadGateway, "could not connect to upstream"
	}
	var certErr *tls.CertificateVerificationError
	var unknownAuth x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var recErr tls.RecordHeaderError
	if errors.As(err, &certErr) || errors.As(err, &unknownAuth) || errors.As(err, &hostErr) || errors.As(err, &recErr) {
		return http.StatusBadGateway, "TLS error talking to upstream"
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return http.StatusBadGateway, "upstream closed the connection"
	}
	return http.StatusBadGateway, "upstream sent a response that could not be read"
}

// errorHandler answers when the upstream cannot be reached in time.
func (p *Proxy) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	st, _ := r.Context().Value(ctxKey{}).(*state)
	if errors.Is(r.Context().Err(), context.Canceled) {
		return // the client went away; nobody to answer
	}
	status, reason := upstreamReason(err)
	msg := "aftermarket: upstream unreachable\n"
	if status == http.StatusGatewayTimeout {
		msg = "aftermarket: upstream timed out\n"
	}
	if st != nil {
		p.opts.Audit.Write(audit.Event{
			Event:     audit.UpstreamError,
			Request:   st.id,
			Method:    st.method,
			Path:      st.path,
			QueryKeys: st.queryKeys,
			Status:    status,
			Reason:    reason,
		})
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Aftermarket-Error", strconv.Itoa(status))
	w.WriteHeader(status)
	_, _ = w.Write([]byte(msg))
}
