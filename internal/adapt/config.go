// Package adapt is AfterAPI: a reverse proxy that keeps old clients working
// when an API changes. Each endpoint mapping describes one old endpoint in
// terms of the new one: where it moved, how to build the new request from
// the old, and how to rebuild the old response from the new.
package adapt

import (
	"fmt"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aftermarket-dev/aftermarket/internal/rules"
	"github.com/aftermarket-dev/aftermarket/internal/yamlite"
)

// Config is a loaded and checked mapping file.
type Config struct {
	MaxBody         int64
	UpstreamTimeout time.Duration
	Endpoints       []*Endpoint
}

// Endpoint maps one old endpoint onto a new one.
type Endpoint struct {
	ID          string
	Description string
	Line        int
	Old         OldPattern
	New         NewPattern
	Headers     []rules.KV // extra request header conditions (globs)
	Request     RequestMap
	Response    ResponseMap
}

// OldPattern matches requests to the old endpoint and captures parameters.
type OldPattern struct {
	Method   string
	Segments []segment       // after the leading /
	Query    []queryCapture  // required query parameters
	Params   map[string]bool // every captured name
	Text     string
}

type segment struct {
	literal string // decoded
	param   string // set for {name}
}

type queryCapture struct {
	key   string
	param string
}

// NewPattern builds the new request line.
type NewPattern struct {
	Method string
	Path   []part      // escaped literal text and parameters
	Query  []queryPart // in order
	Text   string
}

type part struct {
	text  string // escaped literal
	param string
}

type queryPart struct {
	key   string // escaped
	value []part // escaped literal text and parameters
}

// RequestMap changes the request.
type RequestMap struct {
	Body    *Template
	Headers HeaderMap
}

// ResponseMap changes the response.
type ResponseMap struct {
	Status  map[int]int
	Body    *Template // for 2xx responses
	Error   *Template // for 4xx and 5xx responses
	Headers HeaderMap
}

// HeaderMap renames, sets and removes headers.
type HeaderMap struct {
	Rename [][2]string
	Set    []rules.KV
	Remove []string
}

func (h HeaderMap) empty() bool { return len(h.Rename) == 0 && len(h.Set) == 0 && len(h.Remove) == 0 }

// credentialHeaders are never renamed, set or removed by adapt: credentials
// pass through exactly as the client and the upstream sent them.
var credentialHeaders = map[string]bool{
	"Authorization": true, "Proxy-Authorization": true, "Cookie": true,
	"Set-Cookie": true, "Www-Authenticate": true, "Proxy-Authenticate": true,
}

// LoadFile reads and checks a mapping file.
func LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Load(data)
}

type loader struct {
	errs rules.Errors
}

func (l *loader) errf(line int, format string, args ...any) {
	l.errs = append(l.errs, &rules.LineError{Line: line, Msg: fmt.Sprintf(format, args...)})
}

func (l *loader) add(err error) {
	if err != nil {
		l.errs = append(l.errs, err)
	}
}

func (l *loader) onlyKeys(n *yamlite.Node, allowed ...string) {
	for _, p := range n.Pairs {
		ok := false
		for _, a := range allowed {
			if p.Key == a {
				ok = true
			}
		}
		if !ok {
			l.errf(p.KeyLine, "unknown key %q (allowed: %s)", p.Key, strings.Join(allowed, ", "))
		}
	}
}

func (l *loader) scalar(n *yamlite.Node, what string) (string, bool) {
	if n == nil || n.Kind != yamlite.Scalar {
		line := 0
		if n != nil {
			line = n.Line
		}
		l.errf(line, "%s must be a single value", what)
		return "", false
	}
	return n.Value, true
}

// Load parses and checks mapping file contents.
func Load(data []byte) (*Config, error) {
	root, err := yamlite.Parse(data)
	if err != nil {
		return nil, rules.Errors{err}
	}
	l := &loader{}
	if root.Kind != yamlite.Map {
		return nil, rules.Errors{&rules.LineError{Line: root.Line, Msg: "the mapping file must be a mapping with an endpoints: list"}}
	}
	cfg := &Config{MaxBody: rules.DefaultMaxBody, UpstreamTimeout: rules.DefaultUpstreamTimeout}
	l.onlyKeys(root, "version", "limits", "endpoints")
	if v := root.Get("version"); v != nil && !(v.Kind == yamlite.Scalar && v.Value == "1") {
		l.errf(v.Line, "version must be 1")
	}
	if lim := root.Get("limits"); lim != nil {
		l.limits(lim, cfg)
	}
	eps := root.Get("endpoints")
	switch {
	case eps == nil || eps.Kind == yamlite.Null:
		l.errf(root.Line, "missing endpoints: list")
	case eps.Kind != yamlite.Seq:
		l.errf(eps.Line, "endpoints must be a list")
	default:
		ids := map[string]int{}
		for _, n := range eps.Items {
			e := l.endpoint(n)
			if e == nil {
				continue
			}
			if prev, dup := ids[e.ID]; dup {
				l.errf(n.Line, "endpoint id %q is already used on line %d", e.ID, prev)
				continue
			}
			ids[e.ID] = n.Line
			cfg.Endpoints = append(cfg.Endpoints, e)
		}
	}
	if len(l.errs) > 0 {
		return nil, l.errs
	}
	return cfg, nil
}

func (l *loader) limits(n *yamlite.Node, cfg *Config) {
	if n.Kind != yamlite.Map {
		l.errf(n.Line, "limits must be a mapping")
		return
	}
	l.onlyKeys(n, "max_body", "upstream_timeout")
	if v := n.Get("max_body"); v != nil {
		if s, ok := l.scalar(v, "max_body"); ok {
			size, err := rules.ParseSize(s)
			if err != nil {
				l.errf(v.Line, "max_body: %v", err)
			} else {
				cfg.MaxBody = size
			}
		}
	}
	if v := n.Get("upstream_timeout"); v != nil {
		if s, ok := l.scalar(v, "upstream_timeout"); ok {
			d, err := time.ParseDuration(s)
			if err != nil || d <= 0 {
				l.errf(v.Line, "upstream_timeout must be a positive duration such as 30s")
			} else {
				cfg.UpstreamTimeout = d
			}
		}
	}
}

var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func (l *loader) endpoint(n *yamlite.Node) *Endpoint {
	if n.Kind != yamlite.Map {
		l.errf(n.Line, "each endpoint must be a mapping with id, old and new")
		return nil
	}
	before := len(l.errs)
	l.onlyKeys(n, "id", "description", "old", "new", "when", "request", "response")
	e := &Endpoint{Line: n.Line}
	if v := n.Get("id"); v == nil {
		l.errf(n.Line, "endpoint is missing an id")
	} else if s, ok := l.scalar(v, "id"); ok {
		if !idRe.MatchString(s) {
			l.errf(v.Line, "id %q may only use letters, digits, '.', '_' and '-'", s)
		}
		e.ID = s
	}
	if v := n.Get("description"); v != nil {
		if s, ok := l.scalar(v, "description"); ok {
			e.Description = s
		}
	}
	oldN, newN := n.Get("old"), n.Get("new")
	if oldN == nil || newN == nil {
		l.errf(n.Line, "endpoint %q needs both old and new, such as old: GET /api/v1/customer?id={id}", e.ID)
		return nil
	}
	if s, ok := l.scalar(oldN, "old"); ok {
		op, err := parseOld(s)
		if err != nil {
			l.errf(oldN.Line, "old: %v", err)
		}
		e.Old = op
	}
	if s, ok := l.scalar(newN, "new"); ok && e.Old.Params != nil {
		np, err := parseNew(s, e.Old.Params)
		if err != nil {
			l.errf(newN.Line, "new: %v", err)
		}
		e.New = np
	}
	if len(l.errs) > before {
		return nil
	}
	if w := n.Get("when"); w != nil {
		l.when(w, e)
	}
	if rq := n.Get("request"); rq != nil {
		l.request(rq, e)
	}
	if rs := n.Get("response"); rs != nil {
		l.response(rs, e)
	}
	if len(l.errs) > before {
		return nil
	}
	return e
}

func (l *loader) when(n *yamlite.Node, e *Endpoint) {
	if n.Kind != yamlite.Map {
		l.errf(n.Line, "when must be a mapping")
		return
	}
	l.onlyKeys(n, "headers")
	h := n.Get("headers")
	if h == nil {
		return
	}
	if h.Kind != yamlite.Map {
		l.errf(h.Line, "when.headers must be a mapping of name: value")
		return
	}
	for _, p := range h.Pairs {
		name := textproto.CanonicalMIMEHeaderKey(p.Key)
		if p.Value.Kind != yamlite.Scalar {
			l.errf(p.KeyLine, "header %q must have a single value", p.Key)
			continue
		}
		if name == "Host" || !rules.ValidHeaderName(name) {
			l.errf(p.KeyLine, "%q can't be matched", p.Key)
			continue
		}
		e.Headers = append(e.Headers, rules.KV{Name: name, Glob: p.Value.Value})
	}
}

func (l *loader) request(n *yamlite.Node, e *Endpoint) {
	if n.Kind != yamlite.Map {
		l.errf(n.Line, "request must be a mapping")
		return
	}
	l.onlyKeys(n, "body", "headers")
	if b := n.Get("body"); b != nil {
		if e.New.Method == http.MethodGet || e.New.Method == http.MethodHead {
			l.errf(b.Line, "request.body: the new request is %s, which has no body", e.New.Method)
		}
		t, err := compileTemplate(b, e.Old.Params)
		l.add(err)
		e.Request.Body = t
	}
	if h := n.Get("headers"); h != nil {
		e.Request.Headers = l.headers(h, "request.headers")
	}
}

func (l *loader) response(n *yamlite.Node, e *Endpoint) {
	if n.Kind != yamlite.Map {
		l.errf(n.Line, "response must be a mapping")
		return
	}
	l.onlyKeys(n, "status", "body", "error", "headers")
	if s := n.Get("status"); s != nil {
		if s.Kind != yamlite.Map {
			l.errf(s.Line, "response.status must map new codes to old ones, such as {201: 200}")
		} else {
			e.Response.Status = map[int]int{}
			for _, p := range s.Pairs {
				from, err := strconv.Atoi(p.Key)
				if err != nil || from < 200 || from > 599 {
					l.errf(p.KeyLine, "response.status: %q must be a status code from 200 to 599", p.Key)
					continue
				}
				v, ok := l.scalar(p.Value, "response.status value")
				if !ok {
					continue
				}
				to, err := strconv.Atoi(v)
				if err != nil || to < 200 || to > 599 || to == http.StatusNoContent || to == http.StatusNotModified {
					l.errf(p.KeyLine, "response.status: %q must become a code from 200 to 599 other than 204 and 304", v)
					continue
				}
				e.Response.Status[from] = to
			}
		}
	}
	if b := n.Get("body"); b != nil {
		t, err := compileTemplate(b, e.Old.Params)
		l.add(err)
		e.Response.Body = t
	}
	if b := n.Get("error"); b != nil {
		t, err := compileTemplate(b, e.Old.Params)
		l.add(err)
		e.Response.Error = t
	}
	if h := n.Get("headers"); h != nil {
		e.Response.Headers = l.headers(h, "response.headers")
	}
}

func (l *loader) headerName(name string, line int, what string) (string, bool) {
	c := textproto.CanonicalMIMEHeaderKey(name)
	switch {
	case !rules.ValidHeaderName(c):
		l.errf(line, "%s: %q is not a valid header name", what, name)
	case rules.ManagedHeader(c):
		l.errf(line, "%s: %s is managed by the proxy", what, c)
	case credentialHeaders[c]:
		l.errf(line, "%s: %s carries credentials, which adapt never changes", what, c)
	default:
		return c, true
	}
	return c, false
}

func (l *loader) headers(n *yamlite.Node, what string) HeaderMap {
	var h HeaderMap
	if n.Kind != yamlite.Map {
		l.errf(n.Line, "%s must be a mapping with rename, set or remove", what)
		return h
	}
	l.onlyKeys(n, "rename", "set", "remove")
	if r := n.Get("rename"); r != nil {
		if r.Kind != yamlite.Map {
			l.errf(r.Line, "%s.rename must map old names to new ones", what)
		} else {
			for _, p := range r.Pairs {
				from, ok1 := l.headerName(p.Key, p.KeyLine, what+".rename")
				to, ok2 := "", false
				if v, ok := l.scalar(p.Value, what+".rename value"); ok {
					to, ok2 = l.headerName(v, p.KeyLine, what+".rename")
				}
				if ok1 && ok2 {
					if from == to {
						l.errf(p.KeyLine, "%s.rename: %s is renamed to itself", what, from)
						continue
					}
					h.Rename = append(h.Rename, [2]string{from, to})
				}
			}
		}
	}
	if s := n.Get("set"); s != nil {
		if s.Kind != yamlite.Map {
			l.errf(s.Line, "%s.set must map names to values", what)
		} else {
			for _, p := range s.Pairs {
				name, ok := l.headerName(p.Key, p.KeyLine, what+".set")
				v, ok2 := l.scalar(p.Value, what+".set value")
				if ok2 && !rules.ValidHeaderValue(v) {
					l.errf(p.KeyLine, "%s.set: %s value contains a control character", what, name)
					ok2 = false
				}
				if ok && ok2 {
					h.Set = append(h.Set, rules.KV{Name: name, Glob: v})
				}
			}
		}
	}
	if r := n.Get("remove"); r != nil {
		var names []*yamlite.Node
		switch r.Kind {
		case yamlite.Scalar:
			names = []*yamlite.Node{r}
		case yamlite.Seq:
			names = r.Items
		default:
			l.errf(r.Line, "%s.remove must be a name or a list of names", what)
		}
		for _, it := range names {
			if v, ok := l.scalar(it, what+".remove"); ok {
				if name, ok := l.headerName(v, it.Line, what+".remove"); ok {
					h.Remove = append(h.Remove, name)
				}
			}
		}
	}
	return h
}

var (
	methodRe = regexp.MustCompile(`^[A-Z]+$`)
	nameRe   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	tokenRe  = regexp.MustCompile(`\{([^{}]*)\}`)
)

func splitLine(s string) (method, target string, err error) {
	fields := strings.Fields(s)
	if len(fields) != 2 {
		return "", "", fmt.Errorf("%q must be a method and a path, such as GET /api/v1/customer?id={id}", s)
	}
	method = fields[0]
	if !methodRe.MatchString(method) {
		return "", "", fmt.Errorf("%q is not an HTTP method in capitals", method)
	}
	target = fields[1]
	if !strings.HasPrefix(target, "/") {
		return "", "", fmt.Errorf("path %q must start with /", target)
	}
	if strings.Contains(target, "#") {
		return "", "", fmt.Errorf("path %q can't contain #", target)
	}
	return method, target, nil
}

func parseOld(s string) (OldPattern, error) {
	op := OldPattern{Params: map[string]bool{}, Text: s}
	method, target, err := splitLine(s)
	if err != nil {
		return op, err
	}
	op.Method = method
	path, query, _ := strings.Cut(target, "?")
	for _, seg := range strings.Split(path[1:], "/") {
		if m := tokenRe.FindStringSubmatch(seg); m != nil {
			if m[0] != seg {
				return op, fmt.Errorf("a parameter must be a whole path segment, such as /customers/{id}")
			}
			if err := op.addParam(m[1]); err != nil {
				return op, err
			}
			op.Segments = append(op.Segments, segment{param: m[1]})
			continue
		}
		if strings.ContainsAny(seg, "{}*") {
			return op, fmt.Errorf("segment %q: only whole {name} parameters are supported", seg)
		}
		lit, err := url.PathUnescape(seg)
		if err != nil {
			return op, fmt.Errorf("segment %q is not valid", seg)
		}
		if lit == "." || lit == ".." || strings.Contains(lit, "/") {
			return op, fmt.Errorf("segment %q is not allowed", seg)
		}
		op.Segments = append(op.Segments, segment{literal: lit})
	}
	if query != "" {
		for _, kv := range strings.Split(query, "&") {
			k, v, ok := strings.Cut(kv, "=")
			m := tokenRe.FindStringSubmatch(v)
			if !ok || m == nil || m[0] != v || k == "" {
				return op, fmt.Errorf("query part %q must capture a parameter, such as id={id}", kv)
			}
			key, err := url.QueryUnescape(k)
			if err != nil {
				return op, fmt.Errorf("query key %q is not valid", k)
			}
			for _, q := range op.Query {
				if q.key == key {
					return op, fmt.Errorf("query key %q appears twice", key)
				}
			}
			if err := op.addParam(m[1]); err != nil {
				return op, err
			}
			op.Query = append(op.Query, queryCapture{key: key, param: m[1]})
		}
	}
	return op, nil
}

// PathPattern returns the old path with parameters shown as {name}, for
// the audit log, which never holds parameter values.
func (op OldPattern) PathPattern() string {
	var b strings.Builder
	for _, s := range op.Segments {
		b.WriteString("/")
		if s.param != "" {
			b.WriteString("{" + s.param + "}")
		} else {
			b.WriteString(url.PathEscape(s.literal))
		}
	}
	if b.Len() == 0 {
		return "/"
	}
	return b.String()
}

func (op *OldPattern) addParam(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("parameter name %q must be letters, digits and _", name)
	}
	if op.Params[name] {
		return fmt.Errorf("parameter %q is captured twice", name)
	}
	op.Params[name] = true
	return nil
}

// splitParts splits text with {name} tokens into parts. Literal text must
// already be valid escaped URL text.
func splitParts(s string, params map[string]bool, inQuery bool) ([]part, error) {
	var parts []part
	rest := s
	for rest != "" {
		loc := tokenRe.FindStringSubmatchIndex(rest)
		lit := rest
		if loc != nil {
			lit = rest[:loc[0]]
		}
		if lit != "" {
			if strings.ContainsAny(lit, "{}") {
				return nil, fmt.Errorf("%q has an unmatched { or }", s)
			}
			unesc := url.PathUnescape
			if inQuery {
				unesc = url.QueryUnescape
			}
			if _, err := unesc(lit); err != nil {
				return nil, fmt.Errorf("%q is not valid URL text", lit)
			}
			parts = append(parts, part{text: lit})
		}
		if loc == nil {
			break
		}
		name := rest[loc[2]:loc[3]]
		if !params[name] {
			return nil, fmt.Errorf("{%s} is not captured by old", name)
		}
		parts = append(parts, part{param: name})
		rest = rest[loc[1]:]
	}
	return parts, nil
}

func parseNew(s string, params map[string]bool) (NewPattern, error) {
	np := NewPattern{Text: s}
	method, target, err := splitLine(s)
	if err != nil {
		return np, err
	}
	np.Method = method
	path, query, _ := strings.Cut(target, "?")
	for _, seg := range strings.Split(path[1:], "/") {
		if lit, err := url.PathUnescape(seg); err == nil && (lit == "." || lit == "..") {
			return np, fmt.Errorf("segment %q is not allowed", seg)
		}
	}
	parts, err := splitParts(path, params, false)
	if err != nil {
		return np, err
	}
	np.Path = parts
	if query != "" {
		for _, kv := range strings.Split(query, "&") {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || k == "" || strings.ContainsAny(k, "{}") {
				return np, fmt.Errorf("query part %q must be key=value, where the value may use {name}", kv)
			}
			if _, err := url.QueryUnescape(k); err != nil {
				return np, fmt.Errorf("query key %q is not valid", k)
			}
			vp, err := splitParts(v, params, true)
			if err != nil {
				return np, err
			}
			np.Query = append(np.Query, queryPart{key: k, value: vp})
		}
	}
	return np, nil
}

// match reports whether r is a request to this endpoint and returns the
// captured parameters. skip is set when the request matches but can't be
// adapted safely.
func (e *Endpoint) match(method string, segs []string, query url.Values, h http.Header) (params map[string]string, matched bool, skip string) {
	if method != e.Old.Method || len(segs) != len(e.Old.Segments) {
		return nil, false, ""
	}
	params = map[string]string{}
	for i, s := range e.Old.Segments {
		if s.param != "" {
			if segs[i] == "" {
				return nil, false, ""
			}
			params[s.param] = segs[i]
		} else if segs[i] != s.literal {
			return nil, false, ""
		}
	}
	for _, q := range e.Old.Query {
		vals := query[q.key]
		if len(vals) == 0 || vals[0] == "" {
			return nil, false, ""
		}
		params[q.param] = vals[0]
		if len(vals) > 1 {
			return params, true, "query parameter " + q.key + " appears more than once"
		}
	}
	for _, kv := range e.Headers {
		vals := h.Values(kv.Name)
		ok := false
		for _, v := range vals {
			if rules.GlobMatch(kv.Glob, v) {
				ok = true
			}
		}
		if !ok {
			return nil, false, ""
		}
	}
	// Values that go into the new path must stay inside one segment.
	for _, p := range e.New.Path {
		if p.param != "" && !safeSegment(params[p.param]) {
			return params, true, "parameter " + p.param + " is not a safe path segment"
		}
	}
	return params, true, ""
}

func safeSegment(v string) bool {
	return v != "" && v != "." && v != ".." && !strings.ContainsAny(v, "/\\")
}

// render builds the escaped path and the query string of the new request.
func (np NewPattern) render(params map[string]string) (string, string) {
	var b strings.Builder
	for _, p := range np.Path {
		if p.param != "" {
			b.WriteString(url.PathEscape(params[p.param]))
		} else {
			b.WriteString(p.text)
		}
	}
	var qs []string
	for _, q := range np.Query {
		var v strings.Builder
		for _, p := range q.value {
			if p.param != "" {
				v.WriteString(url.QueryEscape(params[p.param]))
			} else {
				v.WriteString(p.text)
			}
		}
		qs = append(qs, q.key+"="+v.String())
	}
	return b.String(), strings.Join(qs, "&")
}

// Overlap describes two endpoints that can match the same request.
type Overlap struct{ A, B *Endpoint }

// Overlaps lists pairs of endpoints that can match the same request. The
// first one in the file wins, so the second is unreachable for those
// requests.
func (c *Config) Overlaps() []Overlap {
	var out []Overlap
	for i := 0; i < len(c.Endpoints); i++ {
		for j := i + 1; j < len(c.Endpoints); j++ {
			a, b := c.Endpoints[i].Old, c.Endpoints[j].Old
			if a.Method != b.Method || len(a.Segments) != len(b.Segments) {
				continue
			}
			ok := true
			for k := range a.Segments {
				x, y := a.Segments[k], b.Segments[k]
				if x.param == "" && y.param == "" && x.literal != y.literal {
					ok = false
				}
			}
			if ok {
				out = append(out, Overlap{A: c.Endpoints[i], B: c.Endpoints[j]})
			}
		}
	}
	return out
}

// Summary describes an endpoint in one line for `adapt check`.
func (e *Endpoint) Summary() string {
	var parts []string
	if e.Request.Body != nil {
		parts = append(parts, "request body")
	}
	if !e.Request.Headers.empty() {
		parts = append(parts, "request headers")
	}
	if len(e.Response.Status) > 0 {
		var codes []string
		for from, to := range e.Response.Status {
			codes = append(codes, fmt.Sprintf("%d->%d", from, to))
		}
		sort.Strings(codes)
		parts = append(parts, "status "+strings.Join(codes, ", "))
	}
	if e.Response.Body != nil {
		parts = append(parts, "response body")
	}
	if e.Response.Error != nil {
		parts = append(parts, "error body")
	}
	if !e.Response.Headers.empty() {
		parts = append(parts, "response headers")
	}
	s := e.Old.Text + "  =>  " + e.New.Text
	if len(parts) > 0 {
		s += " (" + strings.Join(parts, ", ") + ")"
	}
	return s
}
