// Package patch is AfterPatch: a reverse proxy that applies repair rules to
// HTTP requests and responses on their way between a client and an upstream
// service, neither of which is changed.
package patch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/aftermarket-dev/aftermarket/internal/audit"
	"github.com/aftermarket-dev/aftermarket/internal/httpx"
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

func (p *Proxy) eventKind() string {
	if p.opts.DryRun {
		return audit.WouldChange
	}
	return audit.Changed
}

// ServeHTTP handles one request.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p.opts.HealthPath != "" && r.URL.Path == p.opts.HealthPath {
		httpx.WriteHealth(w)
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

	pathUnsafe := httpx.UnsafePath(r.URL)
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
	// A body a rule will read is buffered here, under upstream_timeout, so a
	// client that stalls mid-body can't hold the proxy forever.
	for _, rule := range st.reqRules {
		if !rule.Request.HasBodyOps() || !httpx.IsJSON(r.Header.Get("Content-Type")) {
			continue
		}
		if err := httpx.BufferRequestBody(w, r, p.opts.Config.MaxBody, p.opts.Config.UpstreamTimeout); err != nil {
			for _, rr := range st.reqRules {
				p.event(st, rr, "request", audit.Skipped, 0, nil, nil, err.Error())
			}
			httpx.WriteRequestTimeout(w)
			return
		}
		break
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
	var jb httpx.JSONBody
	bodySkip := ""
	if needBody {
		switch {
		case !httpx.IsJSON(out.Header.Get("Content-Type")):
			bodySkip = "request body is not JSON"
		case out.Body == nil || out.Body == http.NoBody:
			bodySkip = "request has no body"
		default:
			// The body was buffered under a deadline in ServeHTTP.
			jb, out.Body, _ = httpx.ReadJSON(out.Body, out.Header.Get("Content-Encoding"), p.opts.Config.MaxBody, 0, "request")
			bodySkip = jb.Skip
		}
	}
	doc, decoded := jb.Doc, jb.OK

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
			out.URL.Path = httpx.JoinPath(p.opts.Upstream.Path, plain)
			out.URL.RawPath = httpx.JoinPath(p.opts.Upstream.EscapedPath(), newPath)
		}
	}
	if bodyChanged {
		if plain, err := ojson.Encode(doc); err == nil {
			httpx.SetRequestBody(out, httpx.Encode(plain, jb.Body.Gzipped))
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
		if !httpx.SafeSegment(v) {
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
	var jb httpx.JSONBody
	bodySkip := ""
	if needBody {
		switch {
		case st.method == http.MethodHead || resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusNotModified || resp.StatusCode < 200:
			bodySkip = "response has no body"
		case !httpx.IsJSON(resp.Header.Get("Content-Type")):
			bodySkip = "response body is not JSON"
		default:
			var restored io.ReadCloser
			var err error
			deadline := p.opts.Config.UpstreamTimeout
			if p.opts.DryRun {
				deadline = 0 // a dry run never turns a slow answer into an error
			}
			jb, restored, err = httpx.ReadJSON(resp.Body, resp.Header.Get("Content-Encoding"), p.opts.Config.MaxBody, deadline, "response")
			if err != nil {
				return err // answered with 504 by errorHandler; the proxy closes the old body
			}
			resp.Body = restored
			bodySkip = jb.Skip
		}
	}
	doc, decoded := jb.Doc, jb.OK

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
		if plain, err := ojson.Encode(doc); err == nil {
			httpx.SetResponseBody(resp, httpx.Encode(plain, jb.Body.Gzipped))
		}
	}
	return nil
}

// errorHandler answers when the upstream cannot be reached in time.
func (p *Proxy) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	st, _ := r.Context().Value(ctxKey{}).(*state)
	if httpx.ClientGone(r) {
		return // the client went away; nobody to answer
	}
	status, reason := httpx.UpstreamReason(err)
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
	httpx.WriteUpstreamError(w, status)
}

// FixtureHandler returns a constructor for `aftermarket patch test`: a
// proxy with cfg, pointed at the fixture's stand-in upstream.
func FixtureHandler(cfg *rules.Config) func(*url.URL) (http.Handler, error) {
	return func(u *url.URL) (http.Handler, error) {
		return New(Options{Upstream: u, Config: cfg})
	}
}
