// Package guard is AfterGuard: an HTTP security gateway in front of apps that
// can't protect themselves. It turns away requests that break the rules,
// rejects requests that could be read two ways, checks credentials and
// limits request rates, and forwards the rest unchanged.
package guard

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"net/textproto"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aftermarket-dev/aftermarket/internal/rules"
	"github.com/aftermarket-dev/aftermarket/internal/yamlite"
)

// Defaults.
const (
	DefaultMaxHeaderBytes    = 16 << 10
	DefaultHeaderTimeout     = 10 * time.Second
	DefaultBodyTimeout       = 60 * time.Second
	DefaultIdleTimeout       = 2 * time.Minute
	DefaultMaxConns          = 1024
	DefaultMaxConnsPerAddr   = 64
	DefaultLimitTable        = 100000
	DefaultIdentityHeader    = "X-Aftermarket-Client"
	DefaultAPIKeyHeader      = "X-Api-Key"
	DefaultDenyStatus        = 403
	DefaultFailedLoginRate   = 5
	DefaultFailedLoginPeriod = time.Minute
	DefaultWriteTimeout      = 5 * time.Minute
)

// Config is a loaded guard config.
type Config struct {
	Path            string
	DefaultAllow    bool
	MaxBody         int64
	UpstreamTimeout time.Duration

	MaxHeaderBytes  int
	HeaderTimeout   time.Duration
	BodyTimeout     time.Duration
	IdleTimeout     time.Duration
	MaxConns        int
	MaxConnsPerAddr int

	TLSCert, TLSKey string

	IgnoreCase      bool
	AllowSemicolons bool

	TrustedProxies []netip.Prefix

	APIKeyHeader   string // canonical header name, "" for none
	APIKeyQuery    string // query key, "" for none
	KeepCredential bool
	IdentityHeader string // "" when AfterGuard tells the app nothing
	FailedLogins   Rate

	Clients    []*Client
	LimitTable int
	Rules      []*Rule

	SetHeaders    [][2]string
	RemoveHeaders []string
	StripHeaders  []string // request headers never forwarded, besides the built-in ones

	WriteTimeout time.Duration

	Warnings []string // things check reports without failing
}

// Client is a caller AfterGuard can recognise.
type Client struct {
	ID         string
	Line       int
	APIKey     []byte // SHA-256 of the key
	BasicUser  string
	BasicPass  []byte // SHA-256 of the password
	usesAPIKey bool
	usesBasic  bool
}

// Rate is a token bucket: Count requests per Period, with Burst at once.
type Rate struct {
	Count  int
	Period time.Duration
	Burst  int
}

func (r Rate) String() string {
	unit := map[time.Duration]string{time.Second: "s", time.Minute: "m", time.Hour: "h"}[r.Period]
	return fmt.Sprintf("%d/%s, burst %d", r.Count, unit, r.Burst)
}

// Limit is a rule's rate limit.
type Limit struct {
	Rate
	Per string // address, client or rule
}

// Rule is one entry in rules:.
type Rule struct {
	ID         string
	Line       int
	When       rules.When
	From       []netip.Prefix
	Clients    []string // ids; "any" for any known client
	Allow      bool
	DenyStatus int
	Limit      *Limit
	MaxBody    int64 // 0: no check beyond the global limit
	JSON       bool  // the body, when there is one, must be valid JSON
	Upgrades   []string
}

func (r *Rule) allowsUpgrade(proto string) bool {
	for _, u := range r.Upgrades {
		if u == proto {
			return true
		}
	}
	return false
}

// NeedsClient reports whether the rule only lets known clients through.
func (r *Rule) NeedsClient() bool { return len(r.Clients) > 0 }

var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// LoadFile reads a guard config. Relative paths in it are resolved against
// its folder. Every problem is reported, in line order.
func LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c, err := Load(data, filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	c.Path = path
	return c, nil
}

// Load parses config contents, resolving relative paths against dir.
func Load(data []byte, dir string) (*Config, error) {
	root, err := yamlite.Parse(data)
	if err != nil {
		return nil, rules.Errors{err}
	}
	if root.Kind != yamlite.Map {
		return nil, rules.Errors{&rules.LineError{Line: root.Line, Msg: "a guard config must be a mapping with rules:"}}
	}
	c := &Config{
		MaxBody: rules.DefaultMaxBody, UpstreamTimeout: rules.DefaultUpstreamTimeout,
		MaxHeaderBytes: DefaultMaxHeaderBytes, HeaderTimeout: DefaultHeaderTimeout, BodyTimeout: DefaultBodyTimeout,
		IdleTimeout: DefaultIdleTimeout, WriteTimeout: DefaultWriteTimeout, MaxConns: DefaultMaxConns, MaxConnsPerAddr: DefaultMaxConnsPerAddr,
		APIKeyHeader: DefaultAPIKeyHeader, IdentityHeader: DefaultIdentityHeader, LimitTable: DefaultLimitTable,
		FailedLogins: Rate{Count: DefaultFailedLoginRate, Period: DefaultFailedLoginPeriod, Burst: DefaultFailedLoginRate},
	}
	l := &loader{dir: dir}
	var rulesNode, clientsNode *yamlite.Node
	rulesLine := root.Line
	for _, p := range root.Pairs {
		switch p.Key {
		case "version":
			if !(p.Value.Kind == yamlite.Scalar && p.Value.Value == "1") {
				l.errf(p.KeyLine, "version must be 1")
			}
		case "default":
			switch s, _ := l.str(p); s {
			case "deny":
			case "allow":
				c.DefaultAllow = true
			case "":
			default:
				l.errf(p.KeyLine, "default must be deny or allow")
			}
		case "limits":
			for _, e := range rules.ParseLimits(p.Value, &c.MaxBody, &c.UpstreamTimeout) {
				l.errs = append(l.errs, e)
			}
		case "server":
			l.server(p.Value, p.KeyLine, c)
		case "tls":
			l.tls(p.Value, p.KeyLine, c)
		case "paths":
			l.paths(p.Value, p.KeyLine, c)
		case "trusted_proxies":
			c.TrustedProxies = l.prefixes(p.Value, p.KeyLine, "trusted_proxies")
		case "credentials":
			l.credentials(p.Value, p.KeyLine, c)
		case "clients":
			clientsNode = p.Value
		case "limit_table":
			n, err := strconv.Atoi(p.Value.Value)
			if p.Value.Kind != yamlite.Scalar || err != nil || n < 100 || n > 10_000_000 {
				l.errf(p.KeyLine, "limit_table must be a whole number from 100 to 10000000")
			} else {
				c.LimitTable = n
			}
		case "rules":
			rulesNode, rulesLine = p.Value, p.KeyLine
		case "response":
			l.response(p.Value, p.KeyLine, c)
		case "strip_headers":
			items, ok := scalars(p.Value)
			if !ok {
				l.errf(p.KeyLine, "strip_headers must list header names")
			}
			for _, h := range items {
				name := textproto.CanonicalMIMEHeaderKey(h)
				if !rules.ValidHeaderName(name) {
					l.errf(p.KeyLine, "%q is not a header name", h)
					continue
				}
				c.StripHeaders = append(c.StripHeaders, name)
			}
		default:
			l.errf(p.KeyLine, "unknown key %q (use version, default, limits, server, tls, paths, trusted_proxies, credentials, clients, limit_table, strip_headers, rules or response)", p.Key)
		}
	}
	clientIDs := map[string]int{}
	if clientsNode != nil {
		if clientsNode.Kind != yamlite.Seq {
			l.errf(clientsNode.Line, "clients must be a list")
		} else {
			for _, n := range clientsNode.Items {
				cl := l.client(n)
				if cl == nil {
					continue
				}
				if prev, dup := clientIDs[cl.ID]; dup {
					l.errf(n.Line, "client id %q is already used on line %d", cl.ID, prev)
					continue
				}
				clientIDs[cl.ID] = n.Line
				c.Clients = append(c.Clients, cl)
			}
		}
	}
	for _, cl := range c.Clients {
		if cl.usesAPIKey && c.APIKeyHeader == "" && c.APIKeyQuery == "" {
			l.errf(cl.Line, "client %s has an API key, but credentials: names no api_key_header or api_key_query to read it from", cl.ID)
		}
	}
	switch {
	case rulesNode == nil || rulesNode.Kind != yamlite.Seq || len(rulesNode.Items) == 0:
		l.errf(rulesLine, "rules: needs at least one rule")
	default:
		ruleIDs := map[string]int{}
		for _, n := range rulesNode.Items {
			r := l.rule(n, c)
			if r == nil {
				continue
			}
			if prev, dup := ruleIDs[r.ID]; dup {
				l.errf(n.Line, "rule id %q is already used on line %d", r.ID, prev)
				continue
			}
			ruleIDs[r.ID] = n.Line
			for _, id := range r.Clients {
				if _, ok := clientIDs[id]; !ok && id != "any" {
					l.errf(r.Line, "rule %s lets in client %q, which is not in clients:", r.ID, id)
				}
			}
			if r.NeedsClient() && len(c.Clients) == 0 {
				l.errf(r.Line, "rule %s needs a client, but clients: is empty", r.ID)
			}
			c.Rules = append(c.Rules, r)
		}
	}
	if len(l.errs) > 0 {
		sort.SliceStable(l.errs, func(i, j int) bool { return lineOf(l.errs[i]) < lineOf(l.errs[j]) })
		return nil, l.errs
	}
	c.warn()
	return c, nil
}

// warn lists what check reports without failing.
func (c *Config) warn() {
	if c.DefaultAllow {
		c.Warnings = append(c.Warnings, "default: allow lets through every request no rule denies; deny is safer")
	}
	for _, p := range c.TrustedProxies {
		if (p.Addr().Is4() && p.Bits() < 8) || (p.Addr().Is6() && p.Bits() < 16) {
			c.Warnings = append(c.Warnings, fmt.Sprintf("trusted_proxies %s is so wide that almost any client can choose its own address through X-Forwarded-For", p))
		}
	}
	if len(c.TrustedProxies) > 0 {
		c.Warnings = append(c.Warnings, "connections from trusted_proxies count toward max_connections only, not max_connections_per_address")
	}
	hidden := map[*Rule]bool{}
	for i, r := range c.Rules {
		for _, later := range c.Rules[i+1:] {
			if !hidden[later] && covers(r, later) {
				hidden[later] = true
				c.Warnings = append(c.Warnings, fmt.Sprintf("rule %s (line %d) can never match: rule %s (line %d) before it matches every request it would", later.ID, later.Line, r.ID, r.Line))
			}
		}
	}
	if c.TLSCert == "" {
		c.Warnings = append(c.Warnings, "no tls: AfterGuard serves plain HTTP; put it behind TLS or add tls: with a certificate before it faces the internet")
	}
}

// covers reports whether rule a always matches before b could: every request
// b's conditions accept, a's accept too. It looks at the common cases only:
// a has no query, header or address conditions, its methods include b's,
// and its path is empty, the same as b's, or a /** prefix of it.
func covers(a, b *Rule) bool {
	if len(a.When.Query) > 0 || len(a.When.Headers) > 0 || len(a.From) > 0 {
		return false
	}
	if len(a.When.Methods) > 0 {
		if len(b.When.Methods) == 0 {
			return false
		}
		for _, m := range b.When.Methods {
			found := false
			for _, am := range a.When.Methods {
				found = found || am == m
			}
			if !found {
				return false
			}
		}
	}
	ap, bp := a.When.PathGlob, b.When.PathGlob
	switch {
	case ap == "" || ap == "/**":
		return true
	case ap == bp:
		return true
	case strings.HasSuffix(ap, "/**") && bp != "":
		return strings.HasPrefix(bp, strings.TrimSuffix(ap, "**"))
	}
	return false
}

func lineOf(e error) int {
	switch t := e.(type) {
	case *rules.LineError:
		return t.Line
	case *yamlite.Error:
		return t.Line
	}
	return 0
}

type loader struct {
	dir  string
	errs rules.Errors
}

func (l *loader) errf(line int, format string, args ...any) {
	l.errs = append(l.errs, &rules.LineError{Line: line, Msg: fmt.Sprintf(format, args...)})
}

func (l *loader) str(p yamlite.Pair) (string, bool) {
	if p.Value.Kind != yamlite.Scalar || p.Value.Value == "" {
		l.errf(p.KeyLine, "%s must be a single value", p.Key)
		return "", false
	}
	return p.Value.Value, true
}

func (l *loader) boolean(p yamlite.Pair) bool {
	switch p.Value.Value {
	case "true":
		return true
	case "false":
		return false
	}
	l.errf(p.KeyLine, "%s must be true or false", p.Key)
	return false
}

func (l *loader) duration(p yamlite.Pair, def time.Duration) time.Duration {
	s, ok := l.str(p)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		l.errf(p.KeyLine, "%s must be a positive duration such as 10s or 2m", p.Key)
		return def
	}
	return d
}

func (l *loader) count(p yamlite.Pair, lo, hi int) int {
	n, err := strconv.Atoi(p.Value.Value)
	if p.Value.Kind != yamlite.Scalar || err != nil || n < lo || n > hi {
		l.errf(p.KeyLine, "%s must be a whole number from %d to %d", p.Key, lo, hi)
		return 0
	}
	return n
}

func (l *loader) only(n *yamlite.Node, what string, keys ...string) {
	for _, p := range n.Pairs {
		ok := false
		for _, k := range keys {
			ok = ok || k == p.Key
		}
		if !ok {
			l.errf(p.KeyLine, "unknown key %q in %s (use %s)", p.Key, what, strings.Join(keys, ", "))
		}
	}
}

func (l *loader) mapping(n *yamlite.Node, line int, what string) bool {
	if n.Kind != yamlite.Map {
		l.errf(line, "%s must be a mapping", what)
		return false
	}
	return true
}

func (l *loader) server(n *yamlite.Node, line int, c *Config) {
	if !l.mapping(n, line, "server") {
		return
	}
	l.only(n, "server", "max_header_bytes", "header_timeout", "body_timeout", "idle_timeout", "write_timeout", "max_connections", "max_connections_per_address")
	for _, p := range n.Pairs {
		switch p.Key {
		case "max_header_bytes":
			if s, ok := l.str(p); ok {
				v, err := rules.ParseSize(s)
				if err != nil || v < 1024 || v > 1<<20 {
					l.errf(p.KeyLine, "max_header_bytes must be a size from 1KiB to 1MiB")
				} else {
					c.MaxHeaderBytes = int(v)
				}
			}
		case "header_timeout":
			c.HeaderTimeout = l.duration(p, c.HeaderTimeout)
		case "body_timeout":
			c.BodyTimeout = l.duration(p, c.BodyTimeout)
		case "idle_timeout":
			c.IdleTimeout = l.duration(p, c.IdleTimeout)
		case "write_timeout":
			c.WriteTimeout = l.duration(p, c.WriteTimeout)
		case "max_connections":
			if v := l.count(p, 1, 1_000_000); v > 0 {
				c.MaxConns = v
			}
		case "max_connections_per_address":
			if v := l.count(p, 1, 1_000_000); v > 0 {
				c.MaxConnsPerAddr = v
			}
		}
	}
}

func (l *loader) tls(n *yamlite.Node, line int, c *Config) {
	if !l.mapping(n, line, "tls") {
		return
	}
	l.only(n, "tls", "cert", "key")
	for _, p := range n.Pairs {
		s, ok := l.str(p)
		if !ok {
			continue
		}
		if !filepath.IsAbs(s) && l.dir != "" {
			s = filepath.Join(l.dir, s)
		}
		if p.Key == "cert" {
			c.TLSCert = s
		} else {
			c.TLSKey = s
		}
	}
	if (c.TLSCert == "") != (c.TLSKey == "") {
		l.errf(line, "tls needs both cert and key")
	}
}

func (l *loader) paths(n *yamlite.Node, line int, c *Config) {
	if !l.mapping(n, line, "paths") {
		return
	}
	l.only(n, "paths", "ignore_case", "allow_semicolons")
	for _, p := range n.Pairs {
		switch p.Key {
		case "ignore_case":
			c.IgnoreCase = l.boolean(p)
		case "allow_semicolons":
			c.AllowSemicolons = l.boolean(p)
		}
	}
}

func scalars(n *yamlite.Node) ([]string, bool) {
	switch n.Kind {
	case yamlite.Scalar:
		return []string{n.Value}, true
	case yamlite.Seq:
		var out []string
		for _, it := range n.Items {
			if it.Kind != yamlite.Scalar {
				return nil, false
			}
			out = append(out, it.Value)
		}
		return out, true
	}
	return nil, false
}

// ParsePrefix reads an address or a CIDR range.
func ParsePrefix(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, err
		}
		if p.Addr().Is4In6() {
			// Clients are compared as plain IPv4, so an IPv4-mapped range
			// is written as the IPv4 range it means.
			if p.Bits() < 96 {
				return netip.Prefix{}, fmt.Errorf("%s covers IPv4 and IPv6 addresses at once; list the ranges separately", s)
			}
			p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
		}
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), nil
}

func (l *loader) prefixes(n *yamlite.Node, line int, what string) []netip.Prefix {
	items, ok := scalars(n)
	if !ok || len(items) == 0 {
		l.errf(line, "%s must list addresses or ranges such as 10.0.0.0/8", what)
		return nil
	}
	var out []netip.Prefix
	for _, s := range items {
		p, err := ParsePrefix(s)
		if err != nil {
			l.errf(line, "%s: %q is not an address or a range such as 10.0.0.0/8", what, s)
			continue
		}
		out = append(out, p)
	}
	return out
}

func (l *loader) credentials(n *yamlite.Node, line int, c *Config) {
	if !l.mapping(n, line, "credentials") {
		return
	}
	l.only(n, "credentials", "api_key_header", "api_key_query", "keep", "identity_header", "failed_logins")
	for _, p := range n.Pairs {
		switch p.Key {
		case "api_key_header", "identity_header":
			s := p.Value.Value
			if p.Value.Kind == yamlite.Null || s == "none" {
				s = ""
			} else {
				s = textproto.CanonicalMIMEHeaderKey(s)
				if p.Value.Kind != yamlite.Scalar || !rules.ValidHeaderName(s) || strings.Contains(s, "_") || reservedHeader(s) {
					l.errf(p.KeyLine, "%s must be a header name with no underscore, other than the ones AfterGuard manages, or none", p.Key)
					continue
				}
			}
			if p.Key == "api_key_header" {
				c.APIKeyHeader = s
			} else {
				c.IdentityHeader = s
			}
		case "api_key_query":
			if s, ok := l.str(p); ok {
				c.APIKeyQuery = s
			}
		case "keep":
			c.KeepCredential = l.boolean(p)
		case "failed_logins":
			if r, ok := l.rate(p.Value, p.KeyLine, "failed_logins"); ok {
				c.FailedLogins = r
			}
		}
	}
	if c.APIKeyHeader != "" && c.APIKeyHeader == c.IdentityHeader {
		l.errf(line, "api_key_header and identity_header must be different headers")
	}
}

// reservedHeader reports whether AfterGuard sets or removes the header
// itself, so a config can't hand it to clients.
func reservedHeader(name string) bool {
	switch strings.ToLower(name) {
	case "authorization", "host", "content-length", "transfer-encoding", "connection", "x-forwarded-for",
		"x-forwarded-host", "x-forwarded-proto", "forwarded", "x-real-ip", "cookie":
		return true
	}
	return false
}

var rateRe = regexp.MustCompile(`^([0-9]+)/(s|m|h)$`)

func (l *loader) rate(n *yamlite.Node, line int, what string) (Rate, bool) {
	if !l.mapping(n, line, what) {
		return Rate{}, false
	}
	l.only(n, what, "rate", "burst")
	r := Rate{}
	ok := true
	if v := n.Get("rate"); v == nil || v.Kind != yamlite.Scalar || !rateRe.MatchString(v.Value) {
		l.errf(line, "%s needs rate: such as 60/m (per s, m or h)", what)
		ok = false
	} else {
		m := rateRe.FindStringSubmatch(v.Value)
		r.Count, _ = strconv.Atoi(m[1])
		r.Period = map[string]time.Duration{"s": time.Second, "m": time.Minute, "h": time.Hour}[m[2]]
		if r.Count < 1 || r.Count > 1_000_000 {
			l.errf(line, "%s: rate must be from 1 to 1000000 per period", what)
			ok = false
		}
		r.Burst = r.Count
	}
	if v := n.Get("burst"); v != nil {
		b, err := strconv.Atoi(v.Value)
		if v.Kind != yamlite.Scalar || err != nil || b < 1 || b > 1_000_000 {
			l.errf(v.Line, "%s: burst must be a whole number from 1 to 1000000", what)
			ok = false
		} else {
			r.Burst = b
		}
	}
	return r, ok
}

var hexSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (l *loader) hash(p yamlite.Pair) []byte {
	s, ok := l.str(p)
	if !ok {
		return nil
	}
	s = strings.ToLower(strings.TrimPrefix(s, "sha256:"))
	if !hexSHA256.MatchString(s) {
		l.errf(p.KeyLine, "%s must be a SHA-256 hash in hex, as printed by aftermarket guard hash, never the key itself", p.Key)
		return nil
	}
	b, _ := hex.DecodeString(s)
	return b
}

func (l *loader) client(n *yamlite.Node) *Client {
	if n.Kind != yamlite.Map {
		l.errf(n.Line, "each client must be a mapping with id: and a credential")
		return nil
	}
	l.only(n, "a client", "id", "api_key_sha256", "basic_user", "basic_password_sha256")
	cl := &Client{Line: n.Line}
	if v := n.Get("id"); v == nil || v.Kind != yamlite.Scalar || !idRe.MatchString(v.Value) || v.Value == "any" {
		l.errf(n.Line, "client id is required: letters, digits, . _ - (and not \"any\")")
		return nil
	} else {
		cl.ID = v.Value
	}
	for _, p := range n.Pairs {
		switch p.Key {
		case "api_key_sha256":
			cl.APIKey = l.hash(p)
			cl.usesAPIKey = true
		case "basic_user":
			if s, ok := l.str(p); ok {
				if strings.Contains(s, ":") {
					l.errf(p.KeyLine, "basic_user can't contain a colon")
				}
				cl.BasicUser = s
			}
			cl.usesBasic = true
		case "basic_password_sha256":
			cl.BasicPass = l.hash(p)
			cl.usesBasic = true
		}
	}
	switch {
	case !cl.usesAPIKey && !cl.usesBasic:
		l.errf(n.Line, "client %s needs api_key_sha256, or basic_user and basic_password_sha256", cl.ID)
	case cl.usesBasic && (cl.BasicUser == "" || cl.BasicPass == nil):
		l.errf(n.Line, "client %s needs both basic_user and basic_password_sha256", cl.ID)
	}
	return cl
}

func (l *loader) rule(n *yamlite.Node, c *Config) *Rule {
	if n.Kind != yamlite.Map {
		l.errf(n.Line, "each rule must be a mapping with id:, when: and allow: or deny:")
		return nil
	}
	l.only(n, "a rule", "id", "description", "when", "from", "client", "allow", "deny", "limit", "max_body", "json", "upgrade")
	r := &Rule{Line: n.Line}
	if v := n.Get("id"); v == nil || v.Kind != yamlite.Scalar || !idRe.MatchString(v.Value) {
		l.errf(n.Line, "rule id is required: letters, digits, . _ -")
		return nil
	} else {
		r.ID = v.Value
	}
	hasAllow, hasDeny := false, false
	for _, p := range n.Pairs {
		switch p.Key {
		case "when":
			w, errs := rules.ParseWhen(p.Value)
			l.errs = append(l.errs, errs...)
			if len(w.Statuses) > 0 || w.ContentType != "" {
				l.errf(p.KeyLine, "status and content_type match answers, and AfterGuard decides before there is one; match a request's Content-Type with headers:")
			}
			if c.IgnoreCase && w.PathGlob != strings.ToLower(w.PathGlob) {
				l.errf(p.KeyLine, "paths: ignore_case is on, so write the path in lower case")
			}
			r.When = w
		case "from":
			r.From = l.prefixes(p.Value, p.KeyLine, "from")
		case "client":
			ids, ok := scalars(p.Value)
			if !ok || len(ids) == 0 {
				l.errf(p.KeyLine, "client must list client ids, or any")
			}
			r.Clients = ids
		case "allow":
			if !l.boolean(p) {
				l.errf(p.KeyLine, "allow can only be true; use deny: to turn requests away")
			}
			hasAllow = true
		case "deny":
			hasDeny = true
			r.DenyStatus = DefaultDenyStatus
			if p.Value.Kind == yamlite.Scalar && p.Value.Value != "true" {
				st, err := strconv.Atoi(p.Value.Value)
				if err != nil || st < 400 || st > 599 {
					l.errf(p.KeyLine, "deny must be true or a status from 400 to 599")
				} else {
					r.DenyStatus = st
				}
			} else if p.Value.Kind != yamlite.Scalar {
				l.errf(p.KeyLine, "deny must be true or a status from 400 to 599")
			}
		case "limit":
			lim := &Limit{Per: "address"}
			if p.Value.Kind == yamlite.Map {
				if v := p.Value.Get("per"); v != nil {
					switch v.Value {
					case "address", "client", "rule":
						lim.Per = v.Value
					default:
						l.errf(v.Line, "limit per must be address, client or rule")
					}
				}
				sub := &yamlite.Node{Kind: yamlite.Map, Line: p.Value.Line}
				for _, q := range p.Value.Pairs {
					if q.Key != "per" {
						sub.Pairs = append(sub.Pairs, q)
					}
				}
				if rt, ok := l.rate(sub, p.KeyLine, "limit"); ok {
					lim.Rate = rt
				}
			} else {
				l.errf(p.KeyLine, "limit must be a mapping such as {rate: 60/m, burst: 10, per: address}")
			}
			r.Limit = lim
		case "max_body":
			if s, ok := l.str(p); ok {
				v, err := rules.ParseSize(s)
				if err != nil {
					l.errf(p.KeyLine, "max_body: %v", err)
				} else {
					r.MaxBody = v
				}
			}
		case "json":
			r.JSON = l.boolean(p)
		case "upgrade":
			items, ok := scalars(p.Value)
			if !ok || len(items) == 0 {
				l.errf(p.KeyLine, "upgrade must list protocols such as websocket")
			}
			for _, u := range items {
				u = strings.ToLower(u)
				if u == "h2c" {
					l.errf(p.KeyLine, "upgrade: h2c is never allowed; it would carry requests past every rule")
					continue
				}
				r.Upgrades = append(r.Upgrades, u)
			}
		}
	}
	switch {
	case hasAllow == hasDeny:
		l.errf(n.Line, "rule %s needs exactly one of allow: true or deny:", r.ID)
	case hasAllow:
		r.Allow = true
	}
	if hasDeny && (r.Limit != nil || r.MaxBody > 0 || r.JSON || r.NeedsClient() || len(r.Upgrades) > 0) {
		l.errf(n.Line, "rule %s denies, so limit, max_body, json, client and upgrade have nothing to do; they go on allow rules", r.ID)
	}
	if r.Limit != nil && r.Limit.Per == "client" && !r.NeedsClient() {
		l.errf(n.Line, "rule %s limits per client, so it needs client:", r.ID)
	}
	if r.MaxBody > c.MaxBody && c.MaxBody > 0 {
		l.errf(n.Line, "rule %s: max_body is larger than limits: max_body (%d bytes)", r.ID, c.MaxBody)
	}
	return r
}

func (l *loader) response(n *yamlite.Node, line int, c *Config) {
	if !l.mapping(n, line, "response") {
		return
	}
	l.only(n, "response", "set_headers", "remove_headers")
	if v := n.Get("set_headers"); v != nil {
		if v.Kind != yamlite.Map {
			l.errf(v.Line, "set_headers must be a mapping")
		} else {
			for _, p := range v.Pairs {
				name := textproto.CanonicalMIMEHeaderKey(p.Key)
				if !rules.ValidHeaderName(name) || p.Value.Kind != yamlite.Scalar || !rules.ValidHeaderValue(p.Value.Value) || rules.ManagedHeader(name) {
					l.errf(p.KeyLine, "header %q needs a valid name and a single value, and can't be one HTTP manages itself", p.Key)
					continue
				}
				c.SetHeaders = append(c.SetHeaders, [2]string{name, p.Value.Value})
			}
		}
	}
	if v := n.Get("remove_headers"); v != nil {
		items, ok := scalars(v)
		if !ok {
			l.errf(v.Line, "remove_headers must list header names")
		}
		for _, s := range items {
			name := textproto.CanonicalMIMEHeaderKey(s)
			if !rules.ValidHeaderName(name) || rules.ManagedHeader(name) {
				l.errf(v.Line, "%q can't be removed", s)
				continue
			}
			c.RemoveHeaders = append(c.RemoveHeaders, name)
		}
	}
}

// Hash returns the hex SHA-256 of a key or password, as the config holds it.
func Hash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

// clientIP is the address part of a socket address.
func clientIP(remote string) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return a.Unmap().WithZone(""), true
}
