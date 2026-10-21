// Package hook is AfterHook: it watches files, folders, HTTP endpoints and
// command output that change without telling anyone, and sends a signed
// webhook for each change. Delivery is at least once: a state file keeps
// what was last seen and what is still waiting to be sent.
package hook

import (
	"fmt"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aftermarket-dev/aftermarket/internal/config"
	"github.com/aftermarket-dev/aftermarket/internal/ojson"
	"github.com/aftermarket-dev/aftermarket/internal/rules"
	"github.com/aftermarket-dev/aftermarket/internal/yamlite"
)

// Defaults.
const (
	DefaultInterval    = 5 * time.Second
	DefaultSettle      = 200 * time.Millisecond
	DefaultMaxBody     = 1 << 20
	DefaultTimeout     = 10 * time.Second
	DefaultMaxAttempts = 0 // retry until delivered
	DefaultBackoff     = time.Second
	DefaultMaxBackoff  = 5 * time.Minute
)

// Config is a loaded hook config.
type Config struct {
	Path        string
	State       string // state file, resolved against the config's folder
	Interval    time.Duration
	Settle      time.Duration // wait between the two reads of a changed file
	MaxBody     int64
	MaxAttempts int // 0: retry until delivered
	Backoff     time.Duration
	MaxBackoff  time.Duration
	Webhooks    []*Webhook
	Sources     []*Source
}

// Webhook is a receiver.
type Webhook struct {
	ID        string
	URL       *url.URL
	SecretEnv string // environment variable holding the signing secret
	Headers   [][2]string
	Timeout   time.Duration
	Line      int
}

// Source is one watched place.
type Source struct {
	ID       string
	Kind     string // file, folder, http or command
	Line     int
	Interval time.Duration
	SendTo   []string
	Ignore   []ojson.Path // fields left out of comparisons

	Path      string   // file, folder
	Format    string   // file: json, yaml, env or "" for plain bytes
	Pattern   string   // folder: file name glob
	Recursive bool     // folder
	URL       *url.URL // http
	Headers   [][2]string
	Run       []string // command and arguments
	Dir       string   // command: runs in the config's folder
	Timeout   time.Duration
}

var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// LoadFile reads a hook config. Relative paths in it are resolved against
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
		return nil, rules.Errors{&rules.LineError{Line: root.Line, Msg: "a hook config must be a mapping with webhooks: and sources:"}}
	}
	c := &Config{Interval: DefaultInterval, Settle: DefaultSettle, MaxBody: DefaultMaxBody,
		MaxAttempts: DefaultMaxAttempts, Backoff: DefaultBackoff, MaxBackoff: DefaultMaxBackoff}
	l := &loader{dir: dir}
	var whNode, srcNode *yamlite.Node
	whLine, srcLine := root.Line, root.Line
	for _, p := range root.Pairs {
		switch p.Key {
		case "version":
			if !(p.Value.Kind == yamlite.Scalar && p.Value.Value == "1") {
				l.errf(p.KeyLine, "version must be 1")
			}
		case "state":
			if s, ok := l.str(p); ok {
				c.State = l.resolve(s)
			}
		case "interval":
			c.Interval = l.duration(p, c.Interval)
		case "settle":
			c.Settle = l.duration(p, c.Settle)
		case "max_body":
			if s, ok := l.str(p); ok {
				n, err := rules.ParseSize(s)
				if err != nil {
					l.errf(p.KeyLine, "max_body: %v", err)
				} else {
					c.MaxBody = n
				}
			}
		case "retry":
			l.retry(p.Value, c)
		case "webhooks":
			whNode, whLine = p.Value, p.KeyLine
		case "sources":
			srcNode, srcLine = p.Value, p.KeyLine
		default:
			l.errf(p.KeyLine, "unknown key %q (use version, state, interval, settle, max_body, retry, webhooks or sources)", p.Key)
		}
	}
	if c.State == "" {
		l.errf(root.Line, "state: is required; it names the file where AfterHook keeps what it has seen and what it still has to send")
	}
	ids := map[string]int{}
	switch {
	case whNode == nil || whNode.Kind != yamlite.Seq || len(whNode.Items) == 0:
		l.errf(whLine, "webhooks: needs at least one webhook")
	default:
		for _, n := range whNode.Items {
			if w := l.webhook(n); w != nil {
				if prev, dup := ids[w.ID]; dup {
					l.errf(n.Line, "webhook id %q is already used on line %d", w.ID, prev)
					continue
				}
				ids[w.ID] = n.Line
				c.Webhooks = append(c.Webhooks, w)
			}
		}
	}
	srcIDs := map[string]int{}
	switch {
	case srcNode == nil || srcNode.Kind != yamlite.Seq || len(srcNode.Items) == 0:
		l.errf(srcLine, "sources: needs at least one source")
	default:
		for _, n := range srcNode.Items {
			s := l.source(n, c)
			if s == nil {
				continue
			}
			if prev, dup := srcIDs[s.ID]; dup {
				l.errf(n.Line, "source id %q is already used on line %d", s.ID, prev)
				continue
			}
			srcIDs[s.ID] = n.Line
			for _, t := range s.SendTo {
				if _, ok := ids[t]; !ok && len(c.Webhooks) > 0 {
					l.errf(s.Line, "source %s sends to %q, which is not a webhook id", s.ID, t)
				}
			}
			c.Sources = append(c.Sources, s)
		}
	}
	if len(l.errs) > 0 {
		sort.SliceStable(l.errs, func(i, j int) bool { return lineOf(l.errs[i]) < lineOf(l.errs[j]) })
		return nil, l.errs
	}
	return c, nil
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

func (l *loader) resolve(p string) string {
	if filepath.IsAbs(p) || l.dir == "" {
		return p
	}
	return filepath.Join(l.dir, p)
}

func (l *loader) str(p yamlite.Pair) (string, bool) {
	if p.Value.Kind != yamlite.Scalar || p.Value.Value == "" {
		l.errf(p.KeyLine, "%s must be a single value", p.Key)
		return "", false
	}
	return p.Value.Value, true
}

func (l *loader) duration(p yamlite.Pair, def time.Duration) time.Duration {
	s, ok := l.str(p)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		l.errf(p.KeyLine, "%s must be a positive duration such as 5s or 1m", p.Key)
		return def
	}
	return d
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

func (l *loader) retry(n *yamlite.Node, c *Config) {
	if n.Kind != yamlite.Map {
		l.errf(n.Line, "retry must be a mapping")
		return
	}
	l.only(n, "retry", "max_attempts", "backoff", "max_backoff")
	for _, p := range n.Pairs {
		switch p.Key {
		case "max_attempts":
			v, err := strconv.Atoi(p.Value.Value)
			if p.Value.Kind != yamlite.Scalar || err != nil || v < 0 || v > 1000 {
				l.errf(p.KeyLine, "max_attempts must be a whole number from 0 (retry until delivered) to 1000")
			} else {
				c.MaxAttempts = v
			}
		case "backoff":
			c.Backoff = l.duration(p, c.Backoff)
		case "max_backoff":
			c.MaxBackoff = l.duration(p, c.MaxBackoff)
		}
	}
}

func (l *loader) headers(n *yamlite.Node, line int) [][2]string {
	if n.Kind != yamlite.Map {
		l.errf(line, "headers must be a mapping")
		return nil
	}
	var out [][2]string
	for _, p := range n.Pairs {
		name := textproto.CanonicalMIMEHeaderKey(p.Key)
		if !rules.ValidHeaderName(name) || p.Value.Kind != yamlite.Scalar || !rules.ValidHeaderValue(p.Value.Value) {
			l.errf(p.KeyLine, "header %q needs a valid name and a single value", p.Key)
			continue
		}
		out = append(out, [2]string{name, p.Value.Value})
	}
	return out
}

func (l *loader) id(n *yamlite.Node) string {
	v := n.Get("id")
	if v == nil || v.Kind != yamlite.Scalar || !idRe.MatchString(v.Value) {
		l.errf(n.Line, "id is required: letters, digits, . _ -")
		return ""
	}
	return v.Value
}

func (l *loader) webhook(n *yamlite.Node) *Webhook {
	if n.Kind != yamlite.Map {
		l.errf(n.Line, "each webhook must be a mapping")
		return nil
	}
	l.only(n, "a webhook", "id", "url", "secret_env", "headers", "timeout")
	w := &Webhook{ID: l.id(n), Timeout: DefaultTimeout, Line: n.Line}
	ok := w.ID != ""
	for _, p := range n.Pairs {
		switch p.Key {
		case "url":
			s, good := l.str(p)
			u, err := url.Parse(s)
			if !good || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				l.errf(p.KeyLine, "url must be an http or https URL")
				ok = false
				continue
			}
			w.URL = u
		case "secret_env":
			s, good := l.str(p)
			if good && !regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(s) {
				l.errf(p.KeyLine, "secret_env names an environment variable, such as HOOK_SECRET; the secret itself never goes in this file")
				good = false
			}
			ok = ok && good
			w.SecretEnv = s
		case "headers":
			w.Headers = l.headers(p.Value, p.KeyLine)
		case "timeout":
			w.Timeout = l.duration(p, w.Timeout)
		}
	}
	if w.URL == nil && ok {
		l.errf(n.Line, "webhook %s needs url", w.ID)
		ok = false
	}
	if !ok {
		return nil
	}
	return w
}

var kinds = []string{"file", "folder", "http", "command"}

func (l *loader) source(n *yamlite.Node, c *Config) *Source {
	if n.Kind != yamlite.Map {
		l.errf(n.Line, "each source must be a mapping")
		return nil
	}
	l.only(n, "a source", "id", "file", "folder", "http", "command", "interval", "send_to", "ignore")
	s := &Source{ID: l.id(n), Line: n.Line, Interval: c.Interval, Timeout: DefaultTimeout, Dir: l.dir}
	ok := s.ID != ""
	var spec *yamlite.Node
	for _, k := range kinds {
		if v := n.Get(k); v != nil {
			if spec != nil {
				l.errf(n.Line, "a source watches one thing: file, folder, http or command")
				return nil
			}
			s.Kind, spec = k, v
		}
	}
	if spec == nil {
		l.errf(n.Line, "source %s needs one of file, folder, http or command", s.ID)
		return nil
	}
	for _, p := range n.Pairs {
		switch p.Key {
		case "interval":
			s.Interval = l.duration(p, s.Interval)
		case "send_to":
			list, good := scalarList(p.Value)
			if !good || len(list) == 0 {
				l.errf(p.KeyLine, "send_to must list webhook ids")
				ok = false
			}
			s.SendTo = list
		case "ignore":
			list, good := scalarList(p.Value)
			if !good {
				l.errf(p.KeyLine, "ignore must list dot paths")
			}
			for _, x := range list {
				path, err := ojson.ParsePath(x)
				if err != nil {
					l.errf(p.KeyLine, "ignore: %v", err)
					continue
				}
				s.Ignore = append(s.Ignore, path)
			}
		}
	}
	if s.SendTo == nil {
		for _, w := range c.Webhooks {
			s.SendTo = append(s.SendTo, w.ID)
		}
	}
	if spec.Kind != yamlite.Map {
		l.errf(spec.Line, "%s must be a mapping, such as %s: {path: status.json}", s.Kind, s.Kind)
		return nil
	}
	get := func(key string) (string, bool) {
		v := spec.Get(key)
		if v == nil {
			return "", false
		}
		if v.Kind != yamlite.Scalar || v.Value == "" {
			l.errf(v.Line, "%s must be a single value", key)
			ok = false
			return "", false
		}
		return v.Value, true
	}
	switch s.Kind {
	case "file":
		l.only(spec, "file", "path", "format")
		p, has := get("path")
		if !has {
			l.errf(spec.Line, "file needs path")
			ok = false
		}
		s.Path = l.resolve(p)
		if f, has := get("format"); has {
			f = strings.ToLower(f)
			if f != config.JSON && f != config.YAML && f != config.Env && f != "text" {
				l.errf(spec.Line, "format must be json, yaml, env or text")
				ok = false
			}
			s.Format = f
		} else {
			s.Format = config.FormatOf(p)
		}
		if s.Format == "text" {
			s.Format = ""
		}
	case "folder":
		l.only(spec, "folder", "path", "pattern", "recursive")
		p, has := get("path")
		if !has {
			l.errf(spec.Line, "folder needs path")
			ok = false
		}
		s.Path = l.resolve(p)
		if pat, has := get("pattern"); has {
			if _, err := filepath.Match(pat, "x"); err != nil {
				l.errf(spec.Line, "pattern %q is not a valid file name pattern", pat)
				ok = false
			}
			s.Pattern = pat
		}
		if r, has := get("recursive"); has {
			if r != "true" && r != "false" {
				l.errf(spec.Line, "recursive must be true or false")
				ok = false
			}
			s.Recursive = r == "true"
		}
	case "http":
		l.only(spec, "http", "url", "headers", "timeout")
		raw, has := get("url")
		u, err := url.Parse(raw)
		if !has || err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			l.errf(spec.Line, "http needs an http or https url")
			ok = false
		}
		s.URL = u
		if h := spec.Get("headers"); h != nil {
			s.Headers = l.headers(h, h.Line)
		}
		for _, p := range spec.Pairs {
			if p.Key == "timeout" {
				s.Timeout = l.duration(p, s.Timeout)
			}
		}
	case "command":
		l.only(spec, "command", "run", "timeout")
		run := spec.Get("run")
		list, good := []string(nil), false
		if run != nil {
			list, good = scalarList(run)
		}
		if !good || len(list) == 0 || run.Kind != yamlite.Seq {
			l.errf(spec.Line, "command needs run: a list such as [sqlite3, app.db, \"select id, status from orders\"]; it runs without a shell")
			ok = false
		}
		s.Run = list
		for _, p := range spec.Pairs {
			if p.Key == "timeout" {
				s.Timeout = l.duration(p, s.Timeout)
			}
		}
	}
	if !ok {
		return nil
	}
	return s
}

func scalarList(n *yamlite.Node) ([]string, bool) {
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
