package adapt

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"github.com/aftermarket-dev/aftermarket/internal/audit"
	"github.com/aftermarket-dev/aftermarket/internal/httpx"
	"github.com/aftermarket-dev/aftermarket/internal/ojson"
)

// DefaultHealthPath is answered by the proxy itself.
const DefaultHealthPath = "/_aftermarket/healthz"

// Options configure a Proxy.
type Options struct {
	Upstream   *url.URL
	Config     *Config
	DryRun     bool
	Audit      *audit.Log
	HealthPath string
	Transport  http.RoundTripper
}

// Proxy is the AfterAPI HTTP handler.
type Proxy struct {
	opts  Options
	rp    *httputil.ReverseProxy
	reqID atomic.Uint64
}

type ctxKey struct{}

// plan is what the proxy decided for one request.
type plan struct {
	id        uint64
	method    string // as the client sent it
	path      string // the old path pattern, such as /api/v1/customers/{id}
	queryKeys []string
	ep        *Endpoint
	params    map[string]string
	skipped   bool // matched, but passes through unchanged

	// the new request
	newMethod string
	newPath   string // escaped
	newQuery  string
	newBody   []byte
	bodySet   bool
	fromBody  bool // the new body was built from the old one
	gzipBody  bool // ... which was gzipped, so the new one is too
	dropBody  bool // the new method has no body
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
		return nil, errors.New("no mappings loaded")
	}
	p := &Proxy{opts: opts}
	var tr http.RoundTripper = opts.Transport
	if tr == nil {
		tr = httpx.NewTransport(opts.Config.UpstreamTimeout)
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

// FixtureHandler returns a constructor for `aftermarket adapt test`.
func FixtureHandler(cfg *Config) func(*url.URL) (http.Handler, error) {
	return func(u *url.URL) (http.Handler, error) {
		return New(Options{Upstream: u, Config: cfg})
	}
}

func (p *Proxy) changedKind() string {
	if p.opts.DryRun {
		return audit.WouldChange
	}
	return audit.Changed
}

func (p *Proxy) event(pl *plan, phase, kind string, status int, applied, notApplied []string, reason string) {
	p.opts.Audit.Write(audit.Event{
		Event:      kind,
		Request:    pl.id,
		Rule:       pl.ep.ID,
		Phase:      phase,
		Method:     pl.method,
		Path:       pl.path,
		QueryKeys:  pl.queryKeys,
		Status:     status,
		Actions:    applied,
		NotApplied: notApplied,
		Reason:     reason,
	})
}

// ServeHTTP handles one request.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p.opts.HealthPath != "" && r.URL.Path == p.opts.HealthPath {
		httpx.WriteHealth(w)
		return
	}
	// Work on a copy: the server checks its own request's body when it
	// decides whether to reuse the connection.
	r = r.WithContext(r.Context())
	pl, answered := p.plan(w, r)
	if answered {
		return
	}
	if pl != nil {
		r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, pl))
	}
	out, done := httpx.ForwardBody(w, r, p.opts.Config.UpstreamTimeout)
	defer done()
	p.rp.ServeHTTP(w, out)
}

// skip records why a matched request passes through unchanged. The plan is
// kept so an upstream error is logged with the same request id and the
// endpoint's pattern.
func (p *Proxy) skip(pl *plan, reason string) *plan {
	pl.skipped = true
	p.event(pl, "request", audit.Skipped, 0, nil, nil, reason)
	return pl
}

// plan finds the endpoint for r and works out the new request. It returns
// nil when no endpoint matches. answered is set when the proxy already
// replied (a stalled request body).
func (p *Proxy) plan(w http.ResponseWriter, r *http.Request) (*plan, bool) {
	if httpx.UnsafePath(r.URL) {
		return nil, false
	}
	segs := strings.Split(r.URL.Path, "/")[1:]
	query, _ := url.ParseQuery(r.URL.RawQuery)
	var ep *Endpoint
	var params map[string]string
	skip := ""
	for _, e := range p.opts.Config.Endpoints {
		ps, ok, sk := e.match(r.Method, segs, query, r.Header)
		if ok {
			ep, params, skip = e, ps, sk
			break
		}
	}
	if ep == nil {
		return nil, false
	}
	// The audit log shows the endpoint's pattern, never parameter values.
	pl := &plan{id: p.reqID.Add(1), method: r.Method, path: ep.Old.PathPattern(), ep: ep, params: params}
	for k := range query {
		pl.queryKeys = append(pl.queryKeys, k)
	}
	sort.Strings(pl.queryKeys)
	if skip != "" {
		return p.skip(pl, skip), false
	}
	for _, name := range sortedKeys(params) {
		if !utf8.ValidString(params[name]) {
			return p.skip(pl, "parameter "+name+" is not valid UTF-8"), false
		}
	}

	var applied, notApplied []string
	pl.newMethod = ep.New.Method
	if pl.newMethod != r.Method {
		applied = append(applied, "method "+r.Method+" -> "+pl.newMethod)
	}
	pl.newPath, pl.newQuery = ep.New.render(params)
	pl.newQuery = joinQuery(pl.newQuery, leftoverQuery(r.URL.RawQuery, ep.Old.Query, ep.New.queryKeys()))
	if pl.newPath != r.URL.EscapedPath() {
		applied = append(applied, "rewrite path")
	}
	if pl.newQuery != r.URL.RawQuery {
		applied = append(applied, "rewrite query")
	}

	if t := ep.Request.Body; t != nil {
		var doc any
		if t.usesBody() {
			reason := ""
			switch {
			case !httpx.IsJSON(r.Header.Get("Content-Type")):
				reason = "request body is not JSON"
			case r.Body == nil || r.Body == http.NoBody:
				reason = "request has no body"
			default:
				if err := httpx.BufferRequestBody(w, r, p.opts.Config.MaxBody, p.opts.Config.UpstreamTimeout); err != nil {
					p.skip(pl, err.Error())
					httpx.WriteRequestTimeout(w)
					return pl, true
				}
				jb, restored, _ := httpx.ReadJSON(r.Body, r.Header.Get("Content-Encoding"), p.opts.Config.MaxBody, 0, "request")
				r.Body = restored
				if !jb.OK {
					reason = jb.Skip
				} else {
					doc = jb.Doc
					pl.fromBody, pl.gzipBody = true, jb.Body.Gzipped
				}
			}
			if reason != "" {
				// Nothing is half-translated: the request goes on as it came.
				return p.skip(pl, reason), false
			}
		}
		v, missing, err := t.Build(doc, params)
		if err != nil {
			return p.skip(pl, "request body could not be built: "+err.Error()), false
		}
		out, err := ojson.Encode(v)
		if err != nil {
			return p.skip(pl, "request body could not be built"), false
		}
		pl.newBody, pl.bodySet = out, true
		applied = append(applied, "rebuild body")
		notApplied = append(notApplied, missing...)
	} else if (pl.newMethod == http.MethodGet || pl.newMethod == http.MethodHead) && r.Body != nil && r.Body != http.NoBody {
		pl.dropBody = true
		applied = append(applied, "drop body ("+pl.newMethod+" has none)")
	}
	for _, a := range headerPlan(r.Header, ep.Request.Headers) {
		if a.applied {
			applied = append(applied, a.desc)
		} else {
			notApplied = append(notApplied, a.desc)
		}
	}
	kind := p.changedKind()
	if len(applied) == 0 {
		kind = audit.Unchanged
	}
	p.event(pl, "request", kind, 0, applied, notApplied, "")
	// In a dry run the plan is kept so the response side is reported too,
	// but nothing is changed.
	return pl, false
}

func sortedKeys(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// leftoverQuery returns the parts of a raw query string that the old
// pattern did not capture and the new one does not set, exactly as the
// client wrote them.
func leftoverQuery(raw string, captured []queryCapture, newKeys []string) string {
	if raw == "" {
		return ""
	}
	var keep []string
	for _, part := range strings.Split(raw, "&") {
		k, _, _ := strings.Cut(part, "=")
		key, err := url.QueryUnescape(k)
		drop := false
		if err == nil {
			for _, q := range captured {
				if q.key == key {
					drop = true
				}
			}
			for _, k := range newKeys {
				if k == key {
					drop = true
				}
			}
		}
		if !drop {
			keep = append(keep, part)
		}
	}
	return strings.Join(keep, "&")
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

type headerAction struct {
	desc    string
	applied bool
	do      func(http.Header)
}

// headerPlan works out header changes without making them.
func headerPlan(h http.Header, m HeaderMap) []headerAction {
	var out []headerAction
	for _, rn := range m.Rename {
		from, to := rn[0], rn[1]
		desc := "rename header " + from + " -> " + to
		if _, ok := h[from]; !ok {
			out = append(out, headerAction{desc: desc + " (not present)"})
			continue
		}
		if _, ok := h[to]; ok {
			// Never overwrite values the other side sent under the new name.
			out = append(out, headerAction{desc: desc + " (" + to + " already present)"})
			continue
		}
		out = append(out, headerAction{desc: desc, applied: true, do: func(h http.Header) {
			if v, ok := h[from]; ok {
				h[to] = v
				delete(h, from)
			}
		}})
	}
	for _, name := range m.Remove {
		if _, ok := h[name]; !ok {
			out = append(out, headerAction{desc: "remove header " + name + " (not present)"})
			continue
		}
		name := name
		out = append(out, headerAction{desc: "remove header " + name, applied: true, do: func(h http.Header) { h.Del(name) }})
	}
	for _, kv := range m.Set {
		kv := kv
		if v := h.Values(kv.Name); len(v) == 1 && v[0] == kv.Glob {
			out = append(out, headerAction{desc: "set header " + kv.Name + " (already in place)"})
			continue
		}
		out = append(out, headerAction{desc: "set header " + kv.Name, applied: true, do: func(h http.Header) { h.Set(kv.Name, kv.Glob) }})
	}
	return out
}

func applyHeaderPlan(h http.Header, acts []headerAction) {
	for _, a := range acts {
		if a.applied {
			a.do(h)
		}
	}
}

// rewrite builds the outgoing request.
func (p *Proxy) rewrite(pr *httputil.ProxyRequest) {
	pl, _ := pr.In.Context().Value(ctxKey{}).(*plan)
	pr.SetURL(p.opts.Upstream)
	// Forward the query string exactly as the client sent it.
	raw := pr.In.URL.RawQuery
	active := pl != nil && !pl.skipped && !p.opts.DryRun
	if active {
		raw = pl.newQuery
	}
	pr.Out.URL.RawQuery = joinQuery(p.opts.Upstream.RawQuery, raw)
	for _, h := range []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Forwarded"} {
		if v, ok := pr.In.Header[h]; ok {
			pr.Out.Header[h] = v
		}
	}
	if !active {
		return
	}
	out := pr.Out
	out.Method = pl.newMethod
	plain, err := url.PathUnescape(pl.newPath)
	if err == nil {
		out.URL.Path = httpx.JoinPath(p.opts.Upstream.Path, plain)
		out.URL.RawPath = httpx.JoinPath(p.opts.Upstream.EscapedPath(), pl.newPath)
	}
	switch {
	case pl.bodySet:
		body := pl.newBody
		switch {
		case !pl.fromBody:
			out.Header.Del("Content-Encoding")
			out.Header.Set("Content-Type", "application/json")
		case pl.gzipBody:
			body = httpx.Encode(body, true)
		}
		httpx.SetRequestBody(out, body)
	case pl.dropBody:
		if out.Body != nil {
			_ = out.Body.Close()
		}
		out.Body = http.NoBody
		out.ContentLength = 0
		out.TransferEncoding = nil
		out.GetBody = nil
		for _, h := range []string{"Content-Length", "Content-Type", "Content-Encoding"} {
			out.Header.Del(h)
		}
	}
	// Header actions come last, so what the mapping says explicitly wins.
	applyHeaderPlan(out.Header, headerPlan(out.Header, pl.ep.Request.Headers))
}

func noBody(method string, status int) bool {
	return method == http.MethodHead || status == http.StatusNoContent || status == http.StatusNotModified || status < 200
}

// modifyResponse rebuilds the response in the old shape.
func (p *Proxy) modifyResponse(resp *http.Response) error {
	httpx.CloseIfBodyUnfinished(resp)
	pl, _ := resp.Request.Context().Value(ctxKey{}).(*plan)
	if pl == nil || pl.skipped {
		return nil
	}
	m := pl.ep.Response
	orig := resp.StatusCode
	var applied, notApplied []string
	reason := ""

	to, mapped := m.Status[orig]
	if mapped && to != orig {
		applied = append(applied, fmt.Sprintf("status %d -> %d", orig, to))
	} else {
		to = orig
	}

	var t *Template
	which := ""
	switch {
	case orig >= 200 && orig < 300:
		t, which = m.Body, "body"
	case orig >= 400:
		t, which = m.Error, "error body"
	}

	var newBody []byte
	bodySet, gzipBody := false, false
	if t != nil {
		var doc any
		ok := true
		if noBody(pl.method, to) {
			ok, reason = false, "response has no body"
		} else if t.usesBody() {
			switch {
			case noBody(pl.method, orig):
				ok, reason = false, "response has no body"
			case !httpx.IsJSON(resp.Header.Get("Content-Type")):
				ok, reason = false, "response body is not JSON"
			default:
				deadline := p.opts.Config.UpstreamTimeout
				if p.opts.DryRun {
					deadline = 0 // a dry run never turns a slow answer into an error
				}
				jb, restored, err := httpx.ReadJSON(resp.Body, resp.Header.Get("Content-Encoding"), p.opts.Config.MaxBody, deadline, "response")
				if err != nil {
					return err // 504 from errorHandler
				}
				resp.Body = restored
				if !jb.OK {
					ok, reason = false, jb.Skip
				} else {
					doc, gzipBody = jb.Doc, jb.Body.Gzipped
				}
			}
		}
		if ok {
			v, missing, err := t.Build(doc, pl.params)
			if err != nil {
				reason = which + " could not be built: " + err.Error()
			} else if out, err := ojson.Encode(v); err == nil {
				newBody, bodySet = out, true
				applied = append(applied, "rebuild "+which)
				notApplied = append(notApplied, missing...)
			}
		}
		if reason != "" {
			// All or nothing: a response that can't be rebuilt goes back
			// exactly as the upstream sent it, status and headers included.
			p.event(pl, "response", audit.Skipped, orig, nil, nil, reason)
			return nil
		}
	}

	hacts := headerPlan(resp.Header, m.Headers)
	for _, a := range hacts {
		if a.applied {
			applied = append(applied, a.desc)
		} else {
			notApplied = append(notApplied, a.desc)
		}
	}

	kind := p.changedKind()
	if len(applied) == 0 {
		if len(notApplied) == 0 && t == nil && len(m.Status) == 0 && m.Headers.empty() {
			return nil // nothing configured for responses; nothing to report
		}
		kind = audit.Unchanged
	}
	p.event(pl, "response", kind, orig, applied, notApplied, "")
	if p.opts.DryRun {
		return nil
	}

	if to != orig {
		resp.StatusCode = to
		resp.Status = fmt.Sprintf("%d %s", to, http.StatusText(to))
	}
	switch {
	case bodySet:
		if !gzipBody {
			resp.Header.Del("Content-Encoding")
			if !httpx.IsJSON(resp.Header.Get("Content-Type")) {
				resp.Header.Set("Content-Type", "application/json")
			}
		} else {
			newBody = httpx.Encode(newBody, true)
		}
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
		httpx.SetResponseBody(resp, newBody)
	case noBody(pl.method, orig) && !noBody(pl.method, to) && pl.method != http.MethodHead:
		// A bodyless status became one that has a body: say it is empty.
		httpx.SetResponseBody(resp, []byte{})
	}
	// Header actions come last, so what the mapping says explicitly wins.
	applyHeaderPlan(resp.Header, hacts)
	return nil
}

// errorHandler answers when the upstream cannot be reached in time.
func (p *Proxy) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	status, reason, answer := httpx.ForwardError(w, r, err)
	if !answer {
		return // the client went away; nobody to answer
	}
	if pl, _ := r.Context().Value(ctxKey{}).(*plan); pl != nil {
		// Matched requests, translated or skipped, log the pattern.
		p.opts.Audit.Write(audit.Event{
			Event: audit.UpstreamError, Request: pl.id, Rule: pl.ep.ID, Method: pl.method,
			Path: pl.path, QueryKeys: pl.queryKeys, Status: status, Reason: reason,
		})
	} else {
		// No endpoint matched, so there are no parameters to hide; like
		// AfterPatch, the path is the one the client sent.
		p.opts.Audit.Write(audit.Event{
			Event: audit.UpstreamError, Request: p.reqID.Add(1), Method: r.Method,
			Path: r.URL.Path, Status: status, Reason: reason,
		})
	}
	httpx.WriteUpstreamError(w, status)
}
