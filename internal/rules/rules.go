// Package rules loads, checks and matches Aftermarket rule files.
//
// A rule file is YAML. Every rule has a `when:` block that says which traffic
// it applies to, and request and/or response actions that say what to change.
// The `when:` block is shared by every Aftermarket tool; the actions belong to
// the tool reading the file (AfterPatch, for now).
package rules

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/textproto"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aftermarket-dev/aftermarket/internal/ojson"
	"github.com/aftermarket-dev/aftermarket/internal/yamlite"
)

// Defaults for limits.
const (
	DefaultMaxBody         = 1 << 20 // 1 MiB
	DefaultUpstreamTimeout = 30 * time.Second
)

// Config is a loaded and checked rule file.
type Config struct {
	MaxBody         int64
	UpstreamTimeout time.Duration
	Rules           []*Rule
}

// Rule is one compiled rule.
type Rule struct {
	ID          string
	Description string
	Line        int
	When        When
	Request     *Actions
	Response    *Actions
}

// When holds the match conditions. Empty fields match anything.
type When struct {
	Methods     []string
	PathGlob    string
	path        *regexp.Regexp
	Query       []KV
	Headers     []KV
	Statuses    []StatusMatch
	ContentType string
}

// KV is a name and a value glob.
type KV struct {
	Name string
	Glob string
}

// StatusMatch matches one status code or a class such as 2xx.
type StatusMatch struct {
	Code  int // exact code, or 0
	Class int // 1..5 for "Nxx", or 0
}

func (s StatusMatch) String() string {
	if s.Class > 0 {
		return fmt.Sprintf("%dxx", s.Class)
	}
	return strconv.Itoa(s.Code)
}

// Actions are the changes made to a request or response.
type Actions struct {
	SetHeaders    []KV // Glob holds the literal value here
	RemoveHeaders []string
	RewritePath   *Rewrite
	JSON          []JSONOp
}

// HasBodyOps reports whether the actions need to read the body.
func (a *Actions) HasBodyOps() bool { return a != nil && len(a.JSON) > 0 }

// Rewrite replaces the request path.
type Rewrite struct {
	From   *regexp.Regexp
	FromS  string
	To     string
	Tokens []string // query parameter names used as {query.NAME}
}

// JSONOp is one change to a JSON body.
type JSONOp struct {
	Kind  string // rename, copy, set, delete
	From  ojson.Path
	To    ojson.Path
	Value any // for set
}

// Describe returns a short text form of the operation without its value.
func (o JSONOp) Describe() string {
	switch o.Kind {
	case "rename", "copy":
		return fmt.Sprintf("%s %s -> %s", o.Kind, o.From, o.To)
	case "set":
		return fmt.Sprintf("set %s", o.To)
	case "delete":
		return fmt.Sprintf("delete %s", o.From)
	}
	return o.Kind
}

// LoadFile reads and checks a rule file.
func LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Load(data)
}

// LineError is a configuration problem with a line number.
type LineError struct {
	Line int
	Msg  string
}

func (e *LineError) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("line %d: %s", e.Line, e.Msg)
	}
	return e.Msg
}

// Errors collects every problem found in a file.
type Errors []error

func (e Errors) Error() string {
	var parts []string
	for _, err := range e {
		parts = append(parts, err.Error())
	}
	return strings.Join(parts, "\n")
}

type loader struct {
	errs Errors
}

func (l *loader) errf(line int, format string, args ...any) {
	l.errs = append(l.errs, &LineError{Line: line, Msg: fmt.Sprintf(format, args...)})
}

// Load parses and checks rule file contents.
func Load(data []byte) (*Config, error) {
	root, err := yamlite.Parse(data)
	if err != nil {
		return nil, Errors{err}
	}
	l := &loader{}
	cfg := &Config{MaxBody: DefaultMaxBody, UpstreamTimeout: DefaultUpstreamTimeout}
	if root.Kind == yamlite.Null {
		return nil, Errors{&LineError{Msg: "the rule file is empty"}}
	}
	if root.Kind != yamlite.Map {
		return nil, Errors{&LineError{Line: root.Line, Msg: "the rule file must be a mapping with a rules: list"}}
	}
	l.onlyKeys(root, "version", "limits", "rules")
	if v := root.Get("version"); v != nil && !(v.Kind == yamlite.Scalar && v.Value == "1") {
		l.errf(v.Line, "version must be 1")
	}
	if lim := root.Get("limits"); lim != nil {
		l.limits(lim, cfg)
	}
	rs := root.Get("rules")
	switch {
	case rs == nil || rs.Kind == yamlite.Null:
		l.errf(root.Line, "missing rules: list")
	case rs.Kind != yamlite.Seq:
		l.errf(rs.Line, "rules must be a list")
	default:
		ids := map[string]int{}
		for _, rn := range rs.Items {
			r := l.rule(rn)
			if r == nil {
				continue
			}
			if prev, dup := ids[r.ID]; dup {
				l.errf(rn.Line, "rule id %q is already used on line %d", r.ID, prev)
				continue
			}
			ids[r.ID] = rn.Line
			cfg.Rules = append(cfg.Rules, r)
		}
	}
	if len(l.errs) > 0 {
		return nil, l.errs
	}
	return cfg, nil
}

func (l *loader) onlyKeys(n *yamlite.Node, allowed ...string) {
	for _, p := range n.Pairs {
		ok := false
		for _, a := range allowed {
			if p.Key == a {
				ok = true
				break
			}
		}
		if !ok {
			l.errf(p.KeyLine, "unknown key %q (allowed: %s)", p.Key, strings.Join(allowed, ", "))
		}
	}
}

func (l *loader) str(n *yamlite.Node, what string) (string, bool) {
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

func (l *loader) limits(n *yamlite.Node, cfg *Config) {
	if n.Kind != yamlite.Map {
		l.errf(n.Line, "limits must be a mapping")
		return
	}
	l.onlyKeys(n, "max_body", "upstream_timeout")
	if v := n.Get("max_body"); v != nil {
		s, ok := l.str(v, "max_body")
		if ok {
			size, err := ParseSize(s)
			if err != nil {
				l.errf(v.Line, "max_body: %v", err)
			} else {
				cfg.MaxBody = size
			}
		}
	}
	if v := n.Get("upstream_timeout"); v != nil {
		s, ok := l.str(v, "upstream_timeout")
		if ok {
			d, err := time.ParseDuration(s)
			if err != nil || d <= 0 {
				l.errf(v.Line, "upstream_timeout must be a positive duration such as 30s")
			} else {
				cfg.UpstreamTimeout = d
			}
		}
	}
}

// ParseSize reads sizes such as 1048576, 512KB, 1MB, 64KiB or 2MiB.
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	mult := int64(1)
	units := []struct {
		suffix string
		mult   int64
	}{{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30}, {"KB", 1000}, {"MB", 1000 * 1000}, {"GB", 1000 * 1000 * 1000}, {"B", 1}}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			mult = u.mult
			s = strings.TrimSpace(strings.TrimSuffix(s, u.suffix))
			break
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("must be a positive size such as 1MB or 512KiB")
	}
	if n > MaxBodyLimit/mult {
		return 0, fmt.Errorf("must be at most 1GiB")
	}
	return n * mult, nil
}

// MaxBodyLimit is the largest max_body allowed. Bodies a rule changes are
// held in memory, so the limit keeps a typo from exhausting it.
const MaxBodyLimit = 1 << 30

// validHeaderName reports whether s is an HTTP header field name (a token).
func validHeaderName(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// validHeaderValue rejects control characters other than tab.
func validHeaderValue(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < 0x20 && c != '\t') || c == 0x7f {
			return false
		}
	}
	return true
}

var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func (l *loader) rule(n *yamlite.Node) *Rule {
	if n.Kind != yamlite.Map {
		l.errf(n.Line, "each rule must be a mapping with id, when and actions")
		return nil
	}
	l.onlyKeys(n, "id", "description", "when", "request", "response")
	r := &Rule{Line: n.Line}
	before := len(l.errs)
	if id := n.Get("id"); id == nil {
		l.errf(n.Line, "rule is missing an id")
	} else if s, ok := l.str(id, "id"); ok {
		if !idRe.MatchString(s) {
			l.errf(id.Line, "id %q may only use letters, digits, '.', '_' and '-'", s)
		}
		r.ID = s
	}
	if d := n.Get("description"); d != nil {
		if s, ok := l.str(d, "description"); ok {
			r.Description = s
		}
	}
	w := n.Get("when")
	if w == nil {
		l.errf(n.Line, "rule %q is missing a when: block (use when: {} to match all traffic)", r.ID)
	} else {
		l.when(w, &r.When)
	}
	if rq := n.Get("request"); rq != nil {
		r.Request = l.actions(rq, true)
	}
	if rs := n.Get("response"); rs != nil {
		r.Response = l.actions(rs, false)
	}
	if n.Get("request") == nil && n.Get("response") == nil {
		l.errf(n.Line, "rule %q has no request: or response: actions", r.ID)
	}
	if r.Request != nil && (len(r.When.Statuses) > 0 || r.When.ContentType != "") {
		l.errf(n.Line, "rule %q matches on status or content_type, which are only known after the response arrives, so it can only have response actions", r.ID)
	}
	if len(l.errs) > before {
		return nil
	}
	return r
}

func (l *loader) stringList(n *yamlite.Node, what string) []string {
	switch n.Kind {
	case yamlite.Scalar:
		return []string{n.Value}
	case yamlite.Seq:
		var out []string
		for _, it := range n.Items {
			if s, ok := l.str(it, what); ok {
				out = append(out, s)
			}
		}
		return out
	}
	l.errf(n.Line, "%s must be a value or a list of values", what)
	return nil
}

func (l *loader) kvs(n *yamlite.Node, what string) []KV {
	if n.Kind != yamlite.Map {
		l.errf(n.Line, "%s must be a mapping of name: value", what)
		return nil
	}
	var out []KV
	for _, p := range n.Pairs {
		if p.Value.Kind != yamlite.Scalar {
			l.errf(p.KeyLine, "%s %q must have a single value", what, p.Key)
			continue
		}
		out = append(out, KV{Name: p.Key, Glob: p.Value.Value})
	}
	return out
}

var methodRe = regexp.MustCompile(`^[A-Z]+$`)

func (l *loader) when(n *yamlite.Node, w *When) {
	if n.Kind == yamlite.Null {
		return
	}
	if n.Kind != yamlite.Map {
		l.errf(n.Line, "when must be a mapping")
		return
	}
	l.onlyKeys(n, "method", "path", "query", "headers", "status", "content_type")
	if m := n.Get("method"); m != nil {
		for _, s := range l.stringList(m, "method") {
			up := strings.ToUpper(s)
			if !methodRe.MatchString(up) {
				l.errf(m.Line, "method %q is not an HTTP method", s)
			}
			w.Methods = append(w.Methods, up)
		}
	}
	if p := n.Get("path"); p != nil {
		if s, ok := l.str(p, "path"); ok {
			if !strings.HasPrefix(s, "/") {
				l.errf(p.Line, "path must start with /")
			} else {
				w.PathGlob = s
				w.path = compilePathGlob(s)
			}
		}
	}
	if q := n.Get("query"); q != nil {
		w.Query = l.kvs(q, "query")
	}
	if h := n.Get("headers"); h != nil {
		for _, kv := range l.kvs(h, "header") {
			kv.Name = textproto.CanonicalMIMEHeaderKey(kv.Name)
			if kv.Name == "Host" {
				l.errf(h.Line, "matching on the Host header is not supported")
			} else if !validHeaderName(kv.Name) {
				l.errf(h.Line, "%q is not a valid header name", kv.Name)
			}
			w.Headers = append(w.Headers, kv)
		}
	}
	if s := n.Get("status"); s != nil {
		for _, v := range l.stringList(s, "status") {
			sm, err := parseStatus(v)
			if err != nil {
				l.errf(s.Line, "%v", err)
				continue
			}
			w.Statuses = append(w.Statuses, sm)
		}
	}
	if c := n.Get("content_type"); c != nil {
		if s, ok := l.str(c, "content_type"); ok {
			w.ContentType = strings.ToLower(strings.TrimSpace(s))
		}
	}
}

func parseStatus(s string) (StatusMatch, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if len(s) == 3 && strings.HasSuffix(s, "xx") && s[0] >= '1' && s[0] <= '5' {
		return StatusMatch{Class: int(s[0] - '0')}, nil
	}
	code, err := strconv.Atoi(s)
	if err != nil || code < 100 || code > 599 {
		return StatusMatch{}, fmt.Errorf("status %q must be a code such as 200 or a class such as 2xx", s)
	}
	return StatusMatch{Code: code}, nil
}

// Headers the proxy manages itself; rules may not set or remove them.
var managedHeaders = map[string]bool{
	"Content-Length": true, "Transfer-Encoding": true, "Content-Encoding": true,
	"Connection": true, "Keep-Alive": true, "Proxy-Connection": true, "Te": true,
	"Trailer": true, "Upgrade": true, "Host": true,
	"Proxy-Authorization": true, "Proxy-Authenticate": true,
}

var groupRef = regexp.MustCompile(`\$\{([^}]*)\}|\$([A-Za-z0-9_]+)`)

func groupExists(re *regexp.Regexp, name string) bool {
	if n, err := strconv.Atoi(name); err == nil {
		return n >= 0 && n <= re.NumSubexp()
	}
	for _, g := range re.SubexpNames() {
		if g != "" && g == name {
			return true
		}
	}
	return false
}

var queryToken = regexp.MustCompile(`\{query\.([^{}]+)\}`)

func (l *loader) actions(n *yamlite.Node, isRequest bool) *Actions {
	side := "response"
	if isRequest {
		side = "request"
	}
	if n.Kind != yamlite.Map {
		l.errf(n.Line, "%s must be a mapping of actions", side)
		return nil
	}
	allowed := []string{"set_headers", "remove_headers", "json"}
	if isRequest {
		allowed = append(allowed, "rewrite_path")
	}
	l.onlyKeys(n, allowed...)
	a := &Actions{}
	if sh := n.Get("set_headers"); sh != nil {
		for _, kv := range l.kvs(sh, "set_headers") {
			kv.Name = textproto.CanonicalMIMEHeaderKey(kv.Name)
			if !validHeaderName(kv.Name) {
				l.errf(sh.Line, "%q is not a valid header name", kv.Name)
			} else if managedHeaders[kv.Name] {
				l.errf(sh.Line, "header %s is managed by the proxy and cannot be set", kv.Name)
			}
			if !validHeaderValue(kv.Glob) {
				l.errf(sh.Line, "header %s value contains a control character", kv.Name)
			}
			a.SetHeaders = append(a.SetHeaders, kv)
		}
	}
	if rh := n.Get("remove_headers"); rh != nil {
		for _, h := range l.stringList(rh, "remove_headers") {
			h = textproto.CanonicalMIMEHeaderKey(h)
			if !validHeaderName(h) {
				l.errf(rh.Line, "%q is not a valid header name", h)
			} else if managedHeaders[h] {
				l.errf(rh.Line, "header %s is managed by the proxy and cannot be removed", h)
			}
			a.RemoveHeaders = append(a.RemoveHeaders, h)
		}
	}
	if rw := n.Get("rewrite_path"); rw != nil {
		a.RewritePath = l.rewrite(rw)
	}
	if js := n.Get("json"); js != nil {
		if js.Kind != yamlite.Seq {
			l.errf(js.Line, "json must be a list of operations")
		} else {
			for _, op := range js.Items {
				if o, ok := l.jsonOp(op); ok {
					a.JSON = append(a.JSON, o)
				}
			}
		}
	}
	if len(a.SetHeaders) == 0 && len(a.RemoveHeaders) == 0 && a.RewritePath == nil && len(a.JSON) == 0 {
		l.errf(n.Line, "%s has no actions", side)
	}
	return a
}

func (l *loader) rewrite(n *yamlite.Node) *Rewrite {
	if n.Kind != yamlite.Map {
		l.errf(n.Line, "rewrite_path must be a mapping with from and to")
		return nil
	}
	l.onlyKeys(n, "from", "to")
	from, to := n.Get("from"), n.Get("to")
	if from == nil || to == nil {
		l.errf(n.Line, "rewrite_path needs both from and to")
		return nil
	}
	fs, ok1 := l.str(from, "rewrite_path.from")
	ts, ok2 := l.str(to, "rewrite_path.to")
	if !ok1 || !ok2 {
		return nil
	}
	re, err := regexp.Compile(fs)
	if err != nil {
		l.errf(from.Line, "rewrite_path.from is not a valid regular expression: %v", err)
		return nil
	}
	if !strings.HasPrefix(ts, "/") && !strings.HasPrefix(ts, "$") {
		l.errf(to.Line, "rewrite_path.to must start with /")
	}
	if strings.ContainsAny(ts, "?#") {
		l.errf(to.Line, "rewrite_path.to changes the path only; it cannot contain ? or #")
	}
	for _, m := range groupRef.FindAllStringSubmatch(strings.ReplaceAll(ts, "$$", ""), -1) {
		name := m[1]
		if name == "" {
			name = m[2]
		}
		if !groupExists(re, name) {
			l.errf(to.Line, "rewrite_path.to refers to $%s, but from has no such group (write ${1}x to put text right after a group)", name)
		}
	}
	rw := &Rewrite{From: re, FromS: fs, To: ts}
	for _, m := range queryToken.FindAllStringSubmatch(ts, -1) {
		rw.Tokens = append(rw.Tokens, m[1])
	}
	return rw
}

func (l *loader) jsonOp(n *yamlite.Node) (JSONOp, bool) {
	if n.Kind != yamlite.Map || len(n.Pairs) != 1 {
		l.errf(n.Line, "each json operation must be one of rename, copy, set or delete")
		return JSONOp{}, false
	}
	p := n.Pairs[0]
	op := JSONOp{Kind: p.Key}
	body := p.Value
	path := func(key string) (ojson.Path, bool) {
		v := body.Get(key)
		if v == nil {
			l.errf(p.KeyLine, "%s needs %s", op.Kind, key)
			return nil, false
		}
		s, ok := l.str(v, key)
		if !ok {
			return nil, false
		}
		pp, err := ojson.ParsePath(s)
		if err != nil {
			l.errf(v.Line, "%v", err)
			return nil, false
		}
		return pp, true
	}
	if body == nil || body.Kind != yamlite.Map {
		if op.Kind == "delete" && body != nil && body.Kind == yamlite.Scalar {
			pp, err := ojson.ParsePath(body.Value)
			if err != nil {
				l.errf(body.Line, "%v", err)
				return JSONOp{}, false
			}
			op.From = pp
			return op, true
		}
		l.errf(p.KeyLine, "%s must be a mapping such as {from: a, to: b}", op.Kind)
		return JSONOp{}, false
	}
	var ok = true
	switch op.Kind {
	case "rename", "copy":
		l.onlyKeys(body, "from", "to")
		var ok1, ok2 bool
		op.From, ok1 = path("from")
		op.To, ok2 = path("to")
		ok = ok1 && ok2
		if ok && op.From.String() == op.To.String() {
			l.errf(p.KeyLine, "%s from and to are the same path", op.Kind)
			ok = false
		}
		if ok && strings.HasPrefix(op.To.String()+".", op.From.String()+".") {
			l.errf(p.KeyLine, "%s cannot move a field inside itself", op.Kind)
			ok = false
		}
		if ok && op.Kind == "rename" && strings.HasPrefix(op.From.String()+".", op.To.String()+".") {
			l.errf(p.KeyLine, "rename cannot move a field onto one of its parents; use copy and delete")
			ok = false
		}
	case "set":
		l.onlyKeys(body, "path", "value")
		op.To, ok = path("path")
		v := body.Get("value")
		if v == nil {
			l.errf(p.KeyLine, "set needs value")
			ok = false
		} else {
			op.Value = NodeToJSON(v)
		}
	case "delete":
		l.onlyKeys(body, "path")
		op.From, ok = path("path")
	default:
		l.errf(p.KeyLine, "unknown json operation %q (use rename, copy, set or delete)", op.Kind)
		ok = false
	}
	return op, ok
}

var jsonNumber = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)

// NodeToJSON converts a YAML value to a JSON value. Unquoted true, false and
// numbers keep their JSON types; everything else is a string.
func NodeToJSON(n *yamlite.Node) any {
	switch n.Kind {
	case yamlite.Null:
		return nil
	case yamlite.Scalar:
		if n.Quoted {
			return n.Value
		}
		switch n.Value {
		case "true":
			return true
		case "false":
			return false
		}
		if jsonNumber.MatchString(n.Value) {
			return json.Number(n.Value)
		}
		return n.Value
	case yamlite.Seq:
		out := []any{}
		for _, it := range n.Items {
			out = append(out, NodeToJSON(it))
		}
		return out
	case yamlite.Map:
		o := ojson.NewObject()
		for _, p := range n.Pairs {
			o.Set(p.Key, NodeToJSON(p.Value))
		}
		return o
	}
	return nil
}

// compilePathGlob turns a path glob into a regexp. "*" matches within one
// path segment and "**" matches across segments.
func compilePathGlob(g string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(g); i++ {
		if g[i] == '*' {
			if i+1 < len(g) && g[i+1] == '*' {
				b.WriteString(".*")
				i++
			} else {
				b.WriteString("[^/]*")
			}
			continue
		}
		b.WriteString(regexp.QuoteMeta(string(g[i])))
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

// globMatch matches a value glob where "*" matches any run of characters.
func globMatch(glob, s string) bool {
	if glob == "*" {
		return true
	}
	parts := strings.Split(glob, "*")
	if len(parts) == 1 {
		return glob == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	last := parts[len(parts)-1]
	for _, p := range parts[1 : len(parts)-1] {
		i := strings.Index(s, p)
		if i < 0 {
			return false
		}
		s = s[i+len(p):]
	}
	return strings.HasSuffix(s, last) && len(s) >= len(last)
}

// MatchRequest reports whether the request-side conditions hold. path is the
// path the client sent (before any rewrite).
func (w *When) MatchRequest(method, path string, query map[string][]string, h http.Header) bool {
	if len(w.Methods) > 0 {
		ok := false
		for _, m := range w.Methods {
			if m == method {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if w.path != nil && !w.path.MatchString(path) {
		return false
	}
	for _, q := range w.Query {
		vals, present := query[q.Name]
		if !present {
			return false
		}
		if !anyMatch(q.Glob, vals) {
			return false
		}
	}
	for _, hd := range w.Headers {
		vals := h.Values(hd.Name)
		if len(vals) == 0 || !anyMatch(hd.Glob, vals) {
			return false
		}
	}
	return true
}

func anyMatch(glob string, vals []string) bool {
	if len(vals) == 0 {
		return glob == "*" || glob == ""
	}
	for _, v := range vals {
		if globMatch(glob, v) {
			return true
		}
	}
	return false
}

// MatchResponse reports whether the response-side conditions hold.
func (w *When) MatchResponse(status int, contentType string) bool {
	if len(w.Statuses) > 0 && !statusIn(w.Statuses, status) {
		return false
	}
	if w.ContentType != "" {
		mt := strings.ToLower(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]))
		if !globMatch(w.ContentType, mt) {
			return false
		}
	}
	return true
}

func statusIn(list []StatusMatch, status int) bool {
	for _, s := range list {
		if (s.Code != 0 && s.Code == status) || (s.Class != 0 && status/100 == s.Class) {
			return true
		}
	}
	return false
}

// Overlap describes two rules that can match the same traffic.
type Overlap struct {
	A, B   *Rule
	Detail string
}

// Overlaps lists pairs of rules that could apply to the same request and
// change the same part of it. They are not errors: rules run in file order.
func (c *Config) Overlaps() []Overlap {
	var out []Overlap
	for i := 0; i < len(c.Rules); i++ {
		for j := i + 1; j < len(c.Rules); j++ {
			a, b := c.Rules[i], c.Rules[j]
			if !mayOverlap(&a.When, &b.When) {
				continue
			}
			var shared []string
			shared = append(shared, sharedTargets("request", a.Request, b.Request)...)
			shared = append(shared, sharedTargets("response", a.Response, b.Response)...)
			if len(shared) == 0 {
				continue
			}
			out = append(out, Overlap{A: a, B: b, Detail: strings.Join(shared, ", ")})
		}
	}
	return out
}

func mayOverlap(a, b *When) bool {
	if len(a.Methods) > 0 && len(b.Methods) > 0 {
		hit := false
		for _, x := range a.Methods {
			for _, y := range b.Methods {
				if x == y {
					hit = true
				}
			}
		}
		if !hit {
			return false
		}
	}
	if a.path != nil && b.path != nil {
		sa := strings.ReplaceAll(a.PathGlob, "*", "x")
		sb := strings.ReplaceAll(b.PathGlob, "*", "x")
		if a.PathGlob != b.PathGlob && !a.path.MatchString(sb) && !b.path.MatchString(sa) {
			return false
		}
	}
	if len(a.Statuses) > 0 && len(b.Statuses) > 0 {
		hit := false
		for code := 100; code < 600 && !hit; code++ {
			hit = statusIn(a.Statuses, code) && statusIn(b.Statuses, code)
		}
		if !hit {
			return false
		}
	}
	return true
}

func sharedTargets(side string, a, b *Actions) []string {
	if a == nil || b == nil {
		return nil
	}
	ta, tb := targets(a), targets(b)
	var out []string
	for t := range ta {
		if tb[t] {
			out = append(out, side+" "+t)
		}
	}
	sort.Strings(out)
	return out
}

func targets(a *Actions) map[string]bool {
	t := map[string]bool{}
	for _, h := range a.SetHeaders {
		t["header "+h.Name] = true
	}
	for _, h := range a.RemoveHeaders {
		t["header "+h] = true
	}
	if a.RewritePath != nil {
		t["path"] = true
	}
	for _, op := range a.JSON {
		for _, p := range []ojson.Path{op.From, op.To} {
			if len(p) > 0 {
				t["json "+p[0]] = true
			}
		}
	}
	return t
}

// ValidHeaderName reports whether s is an HTTP header field name.
func ValidHeaderName(s string) bool { return validHeaderName(s) }

// ValidHeaderValue reports whether s is safe as a header value.
func ValidHeaderValue(s string) bool { return validHeaderValue(s) }

// ManagedHeader reports whether the proxy manages a header itself, so rules
// may not set, remove or rename it. name must be in canonical form.
func ManagedHeader(name string) bool { return managedHeaders[name] }

// GlobMatch matches a value glob where "*" matches any run of characters.
func GlobMatch(glob, s string) bool { return globMatch(glob, s) }

// ParseStatus reads a status code such as 200.
func ParseStatus(s string) (int, error) {
	sm, err := parseStatus(s)
	if err != nil || sm.Code == 0 {
		return 0, fmt.Errorf("status %q must be a code such as 200", s)
	}
	return sm.Code, nil
}
