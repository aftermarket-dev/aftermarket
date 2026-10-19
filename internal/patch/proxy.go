// Package patch is AfterPatch: a reverse proxy that applies repair rules to
// HTTP requests and responses on their way between a client and an upstream
// service, neither of which is changed.
package patch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

// state carries what the request phase decided into the response phase.
type state struct {
	id        uint64
	method    string
	path      string
	queryKeys []string
	respRules []*rules.Rule
	// outgoing request changes, applied in Rewrite unless dry run
	header  http.Header
	newPath string
	body    []byte // replacement body, or nil to keep the original
	bodySet bool
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
		ErrorLog:       nil,
	}
	return p, nil
}

func (p *Proxy) eventKind() string {
	if p.opts.DryRun {
		return audit.WouldChange
	}
	return audit.Changed
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
		id:     p.reqID.Add(1),
		method: r.Method,
		path:   r.URL.Path,
	}
	query := r.URL.Query()
	for k := range query {
		st.queryKeys = append(st.queryKeys, k)
	}
	sort.Strings(st.queryKeys)

	var reqRules []*rules.Rule
	for _, rule := range p.opts.Config.Rules {
		if !rule.When.MatchRequest(r.Method, r.URL.Path, query, r.Header) {
			continue
		}
		if rule.Request != nil {
			reqRules = append(reqRules, rule)
		}
		if rule.Response != nil {
			st.respRules = append(st.respRules, rule)
		}
	}
	if len(reqRules) > 0 {
		p.requestPhase(r, st, reqRules, query)
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

// requestPhase works out the changes to the outgoing request. Changes are
// computed on copies; Rewrite applies them unless this is a dry run.
func (p *Proxy) requestPhase(r *http.Request, st *state, reqRules []*rules.Rule, query url.Values) {
	st.header = r.Header.Clone()
	path := r.URL.Path

	// Buffer the body once if any rule wants to change JSON.
	var br *bodyRead
	var doc any
	bodySkip := ""
	needBody := false
	for _, rule := range reqRules {
		if rule.Request.HasBodyOps() {
			needBody = true
		}
	}
	if needBody {
		switch {
		case !isJSONType(r.Header.Get("Content-Type")):
			bodySkip = "request body is not JSON"
		default:
			var err error
			br, err = readBody(r.Body, p.opts.Config.MaxBody)
			r.Body = br.restore() // the original bytes stay available either way
			switch {
			case errors.Is(err, errTooLarge):
				bodySkip = "request body is larger than max_body"
			case err != nil:
				bodySkip = "request body could not be read"
			default:
				if reason, ok := br.decode(r.Header.Get("Content-Encoding"), p.opts.Config.MaxBody); !ok {
					bodySkip = reason
				} else if d, err := ojson.Decode(br.plain); err != nil {
					bodySkip = "request body is not valid JSON"
				} else {
					doc = d
				}
			}
		}
	}

	bodyChanged := false
	for _, rule := range reqRules {
		a := rule.Request
		var applied, notApplied []string
		if a.RewritePath != nil {
			np, desc, ok := rewritePath(a.RewritePath, path, query)
			if ok {
				if np != path {
					path = np
					applied = append(applied, desc)
				} else {
					notApplied = append(notApplied, desc+" (already in place)")
				}
			} else {
				notApplied = append(notApplied, desc)
			}
		}
		ha, hn := applyHeaders(st.header, a)
		applied = append(applied, ha...)
		notApplied = append(notApplied, hn...)
		if a.HasBodyOps() {
			if doc == nil {
				p.event(st, rule, "request", audit.Skipped, 0, applied, notApplied, bodySkip)
				continue
			}
			res := applyJSON(doc, a.JSON)
			if len(res.applied) > 0 {
				bodyChanged = true
			}
			applied = append(applied, res.applied...)
			notApplied = append(notApplied, res.notApplied...)
		}
		kind := p.eventKind()
		if len(applied) == 0 {
			kind = audit.Unchanged
		}
		p.event(st, rule, "request", kind, 0, applied, notApplied, "")
	}
	if path != r.URL.Path {
		st.newPath = path
	}
	if bodyChanged {
		plain, err := ojson.Encode(doc)
		if err == nil {
			st.body = encodeBody(plain, br.gzipped)
			st.bodySet = true
		}
	}
}

// rewritePath applies one rewrite_path action.
func rewritePath(rw *rules.Rewrite, path string, query url.Values) (string, string, bool) {
	desc := "rewrite path"
	if !rw.From.MatchString(path) {
		return path, desc + " (from did not match " + path + ")", false
	}
	to := rw.To
	for _, name := range rw.Tokens {
		vals, ok := query[name]
		if !ok || len(vals) == 0 || vals[0] == "" {
			return path, desc + " (query parameter " + name + " missing)", false
		}
		v := strings.ReplaceAll(url.PathEscape(vals[0]), "$", "$$")
		to = strings.ReplaceAll(to, "{query."+name+"}", v)
	}
	np := rw.From.ReplaceAllString(path, to)
	if !strings.HasPrefix(np, "/") {
		return path, desc + " (result does not start with /)", false
	}
	return np, desc, true
}

// rewrite builds the outgoing request.
func (p *Proxy) rewrite(pr *httputil.ProxyRequest) {
	st, _ := pr.In.Context().Value(ctxKey{}).(*state)
	pr.SetURL(p.opts.Upstream)
	// Keep forwarding headers the client sent; Rewrite strips them by default.
	for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Forwarded"} {
		if v, ok := pr.In.Header[h]; ok {
			pr.Out.Header[h] = v
		}
	}
	if st == nil || p.opts.DryRun {
		return
	}
	if st.header != nil {
		// Replace end-to-end headers with the edited copy, keeping what the
		// reverse proxy itself manages (hop-by-hop headers are already gone).
		for k := range pr.Out.Header {
			if _, keep := st.header[k]; !keep && !managed(k) {
				delete(pr.Out.Header, k)
			}
		}
		for k, v := range st.header {
			if managed(k) || hopByHop(k) {
				continue
			}
			pr.Out.Header[k] = v
		}
	}
	if st.newPath != "" {
		// newPath is in escaped form (query values were path-escaped).
		plain, err := url.PathUnescape(st.newPath)
		if err != nil {
			plain = st.newPath
		}
		pr.Out.URL.Path = joinPath(p.opts.Upstream.Path, plain)
		pr.Out.URL.RawPath = joinPath(p.opts.Upstream.EscapedPath(), st.newPath)
	}
	if st.bodySet {
		pr.Out.Body = ioNop(st.body)
		pr.Out.ContentLength = int64(len(st.body))
		pr.Out.Header.Set("Content-Length", strconv.Itoa(len(st.body)))
		pr.Out.TransferEncoding = nil
		pr.Out.GetBody = nil
	}
}

func managed(k string) bool {
	switch k {
	case "Content-Length", "Transfer-Encoding", "Content-Encoding", "Host",
		"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Forwarded":
		return true
	}
	return false
}

func hopByHop(k string) bool {
	switch k {
	case "Connection", "Keep-Alive", "Proxy-Connection", "Proxy-Authenticate",
		"Proxy-Authorization", "Te", "Trailer", "Upgrade":
		return true
	}
	return false
}

func joinPath(base, p string) string {
	if base == "" || base == "/" {
		return p
	}
	return strings.TrimSuffix(base, "/") + p
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
	bodySkip := ""
	if needBody {
		switch {
		case st.method == http.MethodHead || resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotModified || resp.StatusCode < 200:
			bodySkip = "response has no body"
		case !isJSONType(resp.Header.Get("Content-Type")):
			bodySkip = "response body is not JSON"
		default:
			var err error
			br, err = readBody(resp.Body, p.opts.Config.MaxBody)
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
					bodySkip = "response body is not valid JSON"
				} else {
					doc = d
				}
			}
		}
	}

	bodyChanged := false
	for _, rule := range matched {
		a := rule.Response
		applied, notApplied := applyHeaders(header, a)
		if a.HasBodyOps() {
			if doc == nil {
				p.event(st, rule, "response", audit.Skipped, resp.StatusCode, applied, notApplied, bodySkip)
				continue
			}
			res := applyJSON(doc, a.JSON)
			if len(res.applied) > 0 {
				bodyChanged = true
			}
			applied = append(applied, res.applied...)
			notApplied = append(notApplied, res.notApplied...)
		}
		kind := p.eventKind()
		if len(applied) == 0 {
			kind = audit.Unchanged
		}
		p.event(st, rule, "response", kind, resp.StatusCode, applied, notApplied, "")
	}

	if bodyChanged && !p.opts.DryRun {
		plain, err := ojson.Encode(doc)
		if err == nil {
			out := encodeBody(plain, br.gzipped)
			resp.Body = ioNop(out)
			resp.ContentLength = int64(len(out))
			resp.Header.Set("Content-Length", strconv.Itoa(len(out)))
			resp.TransferEncoding = nil
			// The old validators describe the old bytes.
			resp.Header.Del("Etag")
			resp.Header.Del("Content-Md5")
		}
	}
	return nil
}

func ioNop(b []byte) *nopBody { return &nopBody{Reader: bytes.NewReader(b)} }

type nopBody struct{ *bytes.Reader }

func (nopBody) Close() error { return nil }

// errorHandler answers when the upstream cannot be reached in time.
func (p *Proxy) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	st, _ := r.Context().Value(ctxKey{}).(*state)
	if errors.Is(r.Context().Err(), context.Canceled) {
		return // the client went away; nobody to answer
	}
	status := http.StatusBadGateway
	msg := "aftermarket: upstream unreachable\n"
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		status = http.StatusGatewayTimeout
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
			Reason:    err.Error(),
		})
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Aftermarket-Error", strconv.Itoa(status))
	w.WriteHeader(status)
	_, _ = w.Write([]byte(msg))
}
