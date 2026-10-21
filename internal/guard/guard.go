package guard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/aftermarket-dev/aftermarket/internal/httpx"
	"github.com/aftermarket-dev/aftermarket/internal/rules"
)

// DefaultHealthPath is answered by AfterGuard itself.
const DefaultHealthPath = "/_aftermarket/healthz"

// Options configure a Guard.
type Options struct {
	Config     *Config
	Upstream   *url.URL
	DryRun     bool // log what would be turned away, and forward everything
	Log        *Log
	Now        func() time.Time
	Transport  http.RoundTripper
	HealthPath string // "" disables the health check
}

// Guard is the AfterGuard HTTP handler.
type Guard struct {
	cfg    *Config
	opts   Options
	rp     *httputil.ReverseProxy
	limits *limiter
	now    func() time.Time
	reqID  atomic.Uint64

	basic bool // some client uses HTTP Basic
}

// New builds a guard.
func New(opts Options) (*Guard, error) {
	u := opts.Upstream
	if u == nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("upstream must be an absolute http or https URL such as http://127.0.0.1:9000")
	}
	if opts.Config == nil {
		return nil, errors.New("no config loaded")
	}
	g := &Guard{cfg: opts.Config, opts: opts, now: opts.Now, limits: newLimiter(opts.Config.LimitTable)}
	if g.now == nil {
		g.now = time.Now
	}
	for _, c := range g.cfg.Clients {
		g.basic = g.basic || c.usesBasic
	}
	g.limits.onEvict = func() { g.opts.Log.Count(LimitEvicted) }
	g.limits.onFull = func() { g.opts.Log.Count(LimitTableFull) }
	tr := opts.Transport
	if tr == nil {
		tr = httpx.NewTransport(g.cfg.UpstreamTimeout)
	}
	g.rp = &httputil.ReverseProxy{
		Rewrite:        g.rewrite,
		ModifyResponse: g.modifyResponse,
		ErrorHandler:   g.errorHandler,
		Transport:      tr,
		FlushInterval:  -1,
	}
	return g, nil
}

// Decision is what AfterGuard does with one request.
type Decision struct {
	Event      string // Allowed, Denied, Rejected or Limited
	Status     int    // the answer when not allowed
	Rule       string
	Client     string
	Reason     string
	RetryAfter time.Duration
	path       string // the canonical path, when the request has one
	ask        bool   // answer 401 with a Basic challenge
	upgrade    bool   // the request asked for an upgrade
}

type reqState struct {
	id     uint64
	d      Decision
	addr   netip.Addr
	tls    bool
	path   string
	strip  []string // query keys to leave out when forwarding
	logged bool
}

type stateKey struct{}

// ServeHTTP judges and then forwards or answers one request.
func (g *Guard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Every request takes its verdict from the connection, in order, even
	// the health check, so the verdicts stay matched to their requests. A
	// request the tracker has no verdict for is rejected.
	framingReason := ""
	if t, ok := r.Context().Value(trackerKey{}).(*tracker); ok {
		var has bool
		if framingReason, has = t.next(); !has {
			framingReason = "the request has no framing verdict on its connection"
			t.fail("a request had no verdict")
		}
	}
	// Headers spelled with underscores are dropped, as nginx does: some
	// frameworks read X_Role as X-Role, and rules match the hyphen spelling.
	for name := range r.Header {
		if strings.Contains(name, "_") {
			delete(r.Header, name)
		}
	}
	if g.opts.HealthPath != "" && r.URL.Path == g.opts.HealthPath && framingReason == "" && r.Method == http.MethodGet {
		httpx.WriteHealth(w)
		return
	}
	st := &reqState{id: g.reqID.Add(1), tls: r.TLS != nil || g.cfg.TLSCert != ""}
	st.addr = g.clientAddress(r)
	st.d = g.safeDecide(r, st.addr, framingReason)
	st.path = st.d.path
	if st.d.Event == Allowed || g.opts.DryRun {
		g.logDecision(r, st, g.opts.DryRun && st.d.Event != Allowed)
		if st.d.path == "" {
			// Only a dry run gets here with a path that can't be judged;
			// it's forwarded as Go read it.
			st.path = r.URL.Path
		}
		if r.Body != nil && r.Body != http.NoBody {
			r.Body = http.MaxBytesReader(w, r.Body, g.cfg.MaxBody)
		}
		if g.cfg.APIKeyQuery != "" && !g.cfg.KeepCredential {
			st.strip = []string{g.cfg.APIKeyQuery}
		}
		httpx.KeepRequestBody(w)
		g.rp.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), stateKey{}, st)))
		return
	}
	g.logDecision(r, st, false)
	g.answer(w, st.d)
}

// safeDecide judges a request, and turns a failure inside AfterGuard into a
// 500 that forwards nothing.
func (g *Guard) safeDecide(r *http.Request, addr netip.Addr, framingReason string) (d Decision) {
	defer func() {
		if p := recover(); p != nil {
			d = Decision{Event: Rejected, Status: http.StatusInternalServerError, Reason: "AfterGuard failed while judging the request"}
		}
	}()
	return g.decide(r, addr, framingReason)
}

// answer writes AfterGuard's own reply to a request it turned away. It says
// nothing about which rule did it.
func (g *Guard) answer(w http.ResponseWriter, d Decision) {
	h := w.Header()
	for _, kv := range g.cfg.SetHeaders {
		h.Set(kv[0], kv[1])
	}
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	if d.RetryAfter > 0 {
		h.Set("Retry-After", strconv.Itoa(int(math.Ceil(d.RetryAfter.Seconds()))))
	}
	if d.ask && d.Status == http.StatusUnauthorized && g.basic {
		h.Set("WWW-Authenticate", `Basic realm="aftermarket", charset="UTF-8"`)
	}
	if d.Event == Rejected || d.upgrade {
		h.Set("Connection", "close")
	}
	w.WriteHeader(d.Status)
	text := strings.ToLower(http.StatusText(d.Status))
	if text == "" {
		text = "request refused"
	}
	fmt.Fprintf(w, "aftermarket guard: %s\n", text)
}

func (g *Guard) logDecision(r *http.Request, st *reqState, dry bool) {
	d := st.d
	path := d.path
	if path == "" {
		path = r.URL.EscapedPath()
		if len(path) > 256 {
			path = path[:256] + "..."
		}
	}
	e := Entry{Event: d.Event, DryRun: dry, Request: st.id, Rule: d.Rule, Method: r.Method, Path: path,
		QueryKeys: queryKeys(r.URL.RawQuery), Address: st.addr.String(), Client: d.Client, Reason: d.Reason}
	if d.Event != Allowed {
		e.Status = d.Status
	}
	g.opts.Log.Write(e)
}

func queryKeys(raw string) []string {
	if raw == "" {
		return nil
	}
	q, _ := url.ParseQuery(raw)
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// clientAddress is the socket address, or the last hop a trusted proxy
// added to X-Forwarded-For. Nothing a client writes itself is believed.
func (g *Guard) clientAddress(r *http.Request) netip.Addr {
	peer, ok := clientIP(r.RemoteAddr)
	if !ok || len(g.cfg.TrustedProxies) == 0 || !inAny(peer, g.cfg.TrustedProxies) {
		return peer
	}
	vals := r.Header.Values("X-Forwarded-For")
	if len(vals) == 0 {
		return peer
	}
	hops := strings.Split(vals[len(vals)-1], ",")
	if a, err := netip.ParseAddr(strings.TrimSpace(hops[len(hops)-1])); err == nil {
		return a.Unmap().WithZone("")
	}
	return peer
}

func inAny(a netip.Addr, list []netip.Prefix) bool {
	for _, p := range list {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func reject(reason string) Decision {
	return Decision{Event: Rejected, Status: http.StatusBadRequest, Reason: reason}
}

// canonicalPath returns the decoded path the rules see, or why the request
// could be read two ways and is rejected. Nothing is cleaned up: a request
// that needs cleaning is turned away.
func (g *Guard) canonicalPath(r *http.Request) (string, string) {
	target := r.RequestURI
	if !strings.HasPrefix(target, "/") {
		return "", "the request target is not a path"
	}
	raw := target
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		raw = raw[:i]
	}
	lower := strings.ToLower(raw)
	for _, enc := range []string{"%2f", "%5c", "%2e", "%00", "%25"} {
		if strings.Contains(lower, enc) {
			return "", "the path has an encoded slash, backslash, dot, NUL or percent sign"
		}
	}
	p := r.URL.Path
	switch {
	case !utf8.ValidString(p):
		return "", "the path is not valid UTF-8"
	case g.cfg.IgnoreCase && !isASCII(p):
		// Apps that ignore case fold some letters outside ASCII onto ASCII
		// ones (ſ to s), in ways AfterGuard can't know.
		return "", "the path has characters outside ASCII, and paths: ignore_case is on"
	case strings.ContainsAny(p, "\\"):
		return "", "the path has a backslash"
	case strings.Contains(p, "//"):
		return "", "the path has an empty segment"
	case !g.cfg.AllowSemicolons && strings.Contains(p, ";"):
		return "", "the path has a semicolon"
	}
	for _, c := range p {
		if c < 0x20 || c == 0x7f {
			return "", "the path has a control character"
		}
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "." || seg == ".." {
			return "", "the path has a . or .. segment"
		}
	}
	return p, ""
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// decide judges one request.
func (g *Guard) decide(r *http.Request, addr netip.Addr, framingReason string) Decision {
	if framingReason != "" {
		return reject(framingReason)
	}
	if r.Method != strings.ToUpper(r.Method) {
		return reject("the method is not in upper case")
	}
	if up := upgradeOf(r); up != "" {
		if up == "h2c" || r.Header.Get("Http2-Settings") != "" {
			return reject("AfterGuard doesn't pass on HTTP/2 upgrades")
		}
	}
	p, why := g.canonicalPath(r)
	if why != "" {
		return reject(why)
	}
	d := g.decideRules(r, addr, p)
	d.path = p
	d.upgrade = upgradeOf(r) != ""
	return d
}

// upgradeOf is the protocol a request asks to switch to, in lower case, or
// "" when it asks for none.
func upgradeOf(r *http.Request) string {
	return strings.ToLower(strings.TrimSpace(r.Header.Get("Upgrade")))
}

func (g *Guard) decideRules(r *http.Request, addr netip.Addr, p string) Decision {
	if strings.Contains(r.URL.RawQuery, ";") {
		return reject("the query string has a semicolon")
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return reject("the query string can't be read")
	}
	if r.ContentLength > g.cfg.MaxBody {
		return Decision{Event: Rejected, Status: http.StatusRequestEntityTooLarge, Reason: "the body is larger than max_body"}
	}
	matchPath := p
	if g.cfg.IgnoreCase {
		matchPath = strings.ToLower(p)
	}
	now := g.now()
	for _, rule := range g.cfg.Rules {
		if !rule.When.MatchRequest(r.Method, matchPath, query, r.Header) {
			continue
		}
		if rule.Allow && !everyValueMatches(&rule.When, query, r.Header) {
			continue
		}
		if len(rule.From) > 0 && !inAny(addr, rule.From) {
			continue
		}
		if !rule.Allow {
			return Decision{Event: Denied, Status: rule.DenyStatus, Rule: rule.ID, Reason: "denied by rule"}
		}
		d := Decision{Event: Allowed, Rule: rule.ID}
		if up := upgradeOf(r); up != "" && !rule.allowsUpgrade(up) {
			return Decision{Event: Denied, Status: http.StatusForbidden, Rule: rule.ID, Reason: "the rule doesn't allow an upgrade to " + up}
		}
		if rule.NeedsClient() {
			id, res, wait := g.authenticate(r, addr, now)
			switch res {
			case authLocked:
				return Decision{Event: Limited, Status: http.StatusTooManyRequests, Rule: rule.ID, RetryAfter: wait, Reason: "too many failed logins from this address"}
			case authMissing:
				return Decision{Event: Denied, Status: http.StatusUnauthorized, Rule: rule.ID, Reason: "no credential", ask: true}
			case authBad:
				return Decision{Event: Denied, Status: http.StatusUnauthorized, Rule: rule.ID, Reason: "the credential is not recognised", ask: true}
			}
			if !clientAllowed(rule, id) {
				return Decision{Event: Denied, Status: http.StatusForbidden, Rule: rule.ID, Client: id, Reason: "this client may not use this rule"}
			}
			d.Client = id
		}
		if rule.Limit != nil {
			key := "rule|" + rule.ID
			switch rule.Limit.Per {
			case "address":
				key += "|addr|" + addrKey(addr)
			case "client":
				key += "|client|" + d.Client
			}
			if ok, wait := g.limits.take(key, rule.Limit.Rate, now, rule.Limit.Per == "rule"); !ok {
				return Decision{Event: Limited, Status: http.StatusTooManyRequests, Rule: rule.ID, Client: d.Client, RetryAfter: wait, Reason: "over the rule's rate limit"}
			}
		}
		if rule.MaxBody > 0 || rule.JSON {
			if bd, ok := g.checkBody(r, rule); !ok {
				bd.Rule, bd.Client = rule.ID, d.Client
				return bd
			}
		}
		return d
	}
	if g.cfg.DefaultAllow {
		if up := upgradeOf(r); up != "" {
			return Decision{Event: Denied, Status: http.StatusForbidden, Reason: "upgrades need a rule that allows them"}
		}
		return Decision{Event: Allowed, Reason: "no rule matched; the default allows it"}
	}
	return Decision{Event: Denied, Status: http.StatusForbidden, Reason: "no rule allows it"}
}

// everyValueMatches holds an allow rule to every value of a repeated query
// key or header, so a?a=good&a=evil can't be let in on the good value while
// the app reads the other one.
func everyValueMatches(w *rules.When, query url.Values, h http.Header) bool {
	for _, q := range w.Query {
		for _, v := range query[q.Name] {
			if !rules.GlobMatch(q.Glob, v) {
				return false
			}
		}
	}
	for _, hd := range w.Headers {
		for _, v := range h.Values(hd.Name) {
			if !rules.GlobMatch(hd.Glob, v) {
				return false
			}
		}
	}
	return true
}

func clientAllowed(rule *Rule, id string) bool {
	for _, c := range rule.Clients {
		if c == "any" || c == id {
			return true
		}
	}
	return false
}

func (g *Guard) checkBody(r *http.Request, rule *Rule) (Decision, bool) {
	limit := g.cfg.MaxBody
	if rule.MaxBody > 0 {
		limit = rule.MaxBody
	}
	if r.ContentLength > limit {
		return Decision{Event: Rejected, Status: http.StatusRequestEntityTooLarge, Reason: "the body is larger than the rule allows"}, false
	}
	var body []byte
	if r.Body != nil && r.Body != http.NoBody {
		b, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
		if err != nil {
			return Decision{Event: Rejected, Status: http.StatusRequestTimeout, Reason: "the body did not arrive"}, false
		}
		if int64(len(b)) > limit {
			return Decision{Event: Rejected, Status: http.StatusRequestEntityTooLarge, Reason: "the body is larger than the rule allows"}, false
		}
		body = b
		r.Body = io.NopCloser(bytes.NewReader(b))
	}
	if rule.JSON && len(body) > 0 && !json.Valid(body) {
		return Decision{Event: Rejected, Status: http.StatusBadRequest, Reason: "the body is not valid JSON"}, false
	}
	return Decision{}, true
}

type authResult int

const (
	authOK authResult = iota
	authMissing
	authBad
	authLocked
)

// authenticate finds the client a request's credential belongs to. Every
// configured credential is compared, in constant time, so the answer takes
// as long whichever one matches. Failed attempts are limited per address.
func (g *Guard) authenticate(r *http.Request, addr netip.Addr, now time.Time) (string, authResult, time.Duration) {
	failKey := "failed-logins|" + addrKey(addr)
	// Every attempt spends a failed-login token before anything is
	// compared, and gets it back if the credential is right. So concurrent
	// guesses can't all pass the check before any is counted, and a guess
	// is never compared without a place to count it.
	if ok, wait := g.limits.take(failKey, g.cfg.FailedLogins, now, false); !ok {
		return "", authLocked, wait
	}
	key, hasKey := "", false
	if g.cfg.APIKeyHeader != "" {
		if v := r.Header.Values(g.cfg.APIKeyHeader); len(v) > 0 {
			if len(v) > 1 {
				return g.failed(failKey, now)
			}
			key, hasKey = v[0], true
		}
	}
	if !hasKey && g.cfg.APIKeyQuery != "" {
		if v, ok := r.URL.Query()[g.cfg.APIKeyQuery]; ok {
			if len(v) > 1 {
				return g.failed(failKey, now)
			}
			key, hasKey = v[0], true
		}
	}
	user, pass, hasBasic := "", "", false
	if g.basic && len(r.Header.Values("Authorization")) > 1 {
		return g.failed(failKey, now)
	}
	if !hasKey && g.basic {
		user, pass, hasBasic = r.BasicAuth()
		if !hasBasic && r.Header.Get("Authorization") != "" {
			return g.failed(failKey, now)
		}
	}
	if !hasKey && !hasBasic {
		g.limits.refund(failKey) // nothing was tried
		return "", authMissing, 0
	}
	found := ""
	if hasKey {
		sum := sha256.Sum256([]byte(key))
		for _, c := range g.cfg.Clients {
			if c.usesAPIKey && subtle.ConstantTimeCompare(sum[:], c.APIKey) == 1 && found == "" {
				found = c.ID
			}
		}
	} else {
		us := sha256.Sum256([]byte(user))
		ps := sha256.Sum256([]byte(pass))
		for _, c := range g.cfg.Clients {
			if !c.usesBasic {
				continue
			}
			cu := sha256.Sum256([]byte(c.BasicUser))
			okUser := subtle.ConstantTimeCompare(us[:], cu[:])
			okPass := subtle.ConstantTimeCompare(ps[:], c.BasicPass)
			if okUser&okPass == 1 && found == "" {
				found = c.ID
			}
		}
	}
	if found == "" {
		return g.failed(failKey, now)
	}
	g.limits.refund(failKey)
	return found, authOK, 0
}

// failed counts a failed attempt; its token was spent before comparing.
func (g *Guard) failed(failKey string, now time.Time) (string, authResult, time.Duration) {
	return "", authBad, 0
}

// forwardingHeaders are set by AfterGuard alone; client copies never reach
// the app, under any spelling.
var forwardingHeaders = []string{"x-forwarded-for", "x-forwarded-host", "x-forwarded-proto", "forwarded", "x-real-ip"}

// overrideHeaders let a client rewrite the path, the method or its address
// in some frameworks and servers, behind AfterGuard's back. They never reach
// the app; strip_headers adds more.
var overrideHeaders = []string{"x-original-url", "x-rewrite-url", "x-override-url", "x-http-method-override", "x-http-method",
	"x-method-override", "true-client-ip", "x-client-ip", "cf-connecting-ip", "x-cluster-client-ip", "x-forwarded-port",
	"x-forwarded-prefix", "x-forwarded-server", "x-forwarded-scheme", "x-forwarded-ssl", "x-url-scheme", "http2-settings"}

// normName folds a header name the way some servers and frameworks do, so
// X_Aftermarket_Client and x-aftermarket-client count as the same header.
func normName(s string) string {
	return strings.ReplaceAll(strings.ToLower(s), "_", "-")
}

// rewrite builds the request the app gets, from AfterGuard's own reading.
func (g *Guard) rewrite(pr *httputil.ProxyRequest) {
	st, _ := pr.In.Context().Value(stateKey{}).(*reqState)
	u := g.opts.Upstream
	pr.SetURL(u)
	if st == nil {
		return
	}
	out := pr.Out
	out.URL.Path = httpx.JoinPath(u.Path, st.path)
	out.URL.RawPath = ""
	q := dropQueryKeys(pr.In.URL.RawQuery, st.strip)
	switch {
	case u.RawQuery == "":
		out.URL.RawQuery = q
	case q == "":
		out.URL.RawQuery = u.RawQuery
	default:
		out.URL.RawQuery = u.RawQuery + "&" + q
	}
	managed := map[string]bool{}
	for _, h := range forwardingHeaders {
		managed[h] = true
	}
	for _, h := range overrideHeaders {
		managed[h] = true
	}
	for _, h := range g.cfg.StripHeaders {
		managed[normName(h)] = true
	}
	if g.cfg.IdentityHeader != "" {
		managed[normName(g.cfg.IdentityHeader)] = true
	}
	if !g.cfg.KeepCredential {
		if g.cfg.APIKeyHeader != "" {
			managed[normName(g.cfg.APIKeyHeader)] = true
		}
		if g.basic {
			managed["authorization"] = true
		}
	}
	for name := range out.Header {
		if managed[normName(name)] {
			delete(out.Header, name)
		}
	}
	if st.addr.IsValid() {
		out.Header.Set("X-Forwarded-For", st.addr.String())
	}
	out.Header.Set("X-Forwarded-Host", pr.In.Host)
	if st.tls {
		out.Header.Set("X-Forwarded-Proto", "https")
	} else {
		out.Header.Set("X-Forwarded-Proto", "http")
	}
	if g.cfg.IdentityHeader != "" && st.d.Client != "" && st.d.Event == Allowed {
		out.Header.Set(g.cfg.IdentityHeader, st.d.Client)
	}
}

// dropQueryKeys leaves out the named keys and keeps every other part of the
// query string exactly as sent.
func dropQueryKeys(raw string, keys []string) string {
	if raw == "" || len(keys) == 0 {
		return raw
	}
	var kept []string
	for _, part := range strings.Split(raw, "&") {
		name := part
		if i := strings.IndexByte(part, '='); i >= 0 {
			name = part[:i]
		}
		if dec, err := url.QueryUnescape(name); err == nil {
			name = dec
		}
		drop := false
		for _, k := range keys {
			drop = drop || name == k
		}
		if !drop {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, "&")
}

func (g *Guard) modifyResponse(resp *http.Response) error {
	if resp.StatusCode == http.StatusSwitchingProtocols {
		if t, ok := resp.Request.Context().Value(trackerKey{}).(*tracker); ok {
			t.upgraded()
		}
	}
	for _, name := range g.cfg.RemoveHeaders {
		resp.Header.Del(name)
	}
	for _, kv := range g.cfg.SetHeaders {
		resp.Header.Set(kv[0], kv[1])
	}
	return nil
}

func (g *Guard) errorHandler(w http.ResponseWriter, r *http.Request, err error) {
	st, _ := r.Context().Value(stateKey{}).(*reqState)
	// After a failed exchange, Go and the tracker may not agree where the
	// next request on this connection starts, so it ends here.
	if t, ok := r.Context().Value(trackerKey{}).(*tracker); ok {
		t.fail("an exchange on it failed")
	}
	w.Header().Set("Connection", "close")
	var mbe *http.MaxBytesError
	d := Decision{Event: "upstream_error"}
	switch {
	case errors.As(err, &mbe):
		d = Decision{Event: Rejected, Status: http.StatusRequestEntityTooLarge, Reason: "the body is larger than max_body"}
	case httpx.ClientGone(r):
		d.Status, d.Reason = 499, "the client went away"
	default:
		d.Status, d.Reason = httpx.UpstreamReason(err)
	}
	if st != nil {
		e := Entry{Event: d.Event, Request: st.id, Rule: st.d.Rule, Status: d.Status, Method: r.Method, Path: st.path,
			Address: st.addr.String(), Client: st.d.Client, Reason: d.Reason}
		g.opts.Log.Write(e)
	}
	if d.Status == 499 {
		return
	}
	if d.Event == Rejected {
		g.answer(w, d)
		return
	}
	httpx.WriteUpstreamError(w, d.Status)
}

// Decide judges a request without forwarding it, for guard test. remote is
// the client's socket address.
func (g *Guard) Decide(r *http.Request, remote string) Decision {
	r.RemoteAddr = remote
	return g.decide(r, g.clientAddress(r), "")
}
