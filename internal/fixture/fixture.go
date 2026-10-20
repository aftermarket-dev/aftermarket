// Package fixture runs saved requests and responses through an Aftermarket
// proxy against a stand-in upstream and compares what comes out. It backs
// `aftermarket patch test` and `aftermarket adapt test`.
package fixture

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/aftermarket-dev/aftermarket/internal/yamlite"
)

// Fixture is a set of test cases.
type Fixture struct {
	Cases []Case
}

// Case sends one request through the proxy to a stand-in upstream.
type Case struct {
	Name     string
	Line     int
	Request  Message
	Upstream Message // what the stand-in upstream answers
	// Expectations; nil means not checked.
	ExpectUpstream *Message // what the upstream should receive
	ExpectResponse *Message // what the client should get back
}

// Message is a request or response in a fixture. For requests Path includes
// any query string.
type Message struct {
	Method  string
	Path    string
	Status  int
	Headers [][2]string
	Body    *string
}

// Load reads a fixture file.
func Load(path string) (*Fixture, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data)
}

// Parse parses fixture YAML.
func Parse(data []byte) (*Fixture, error) {
	root, err := yamlite.Parse(data)
	if err != nil {
		return nil, err
	}
	cs := root.Get("cases")
	if root.Kind != yamlite.Map || cs == nil || cs.Kind != yamlite.Seq {
		return nil, fmt.Errorf("a fixture file needs a cases: list")
	}
	for _, p := range root.Pairs {
		if p.Key != "cases" {
			return nil, fmt.Errorf("line %d: unknown key %q", p.KeyLine, p.Key)
		}
	}
	f := &Fixture{}
	for i, n := range cs.Items {
		c, err := parseCase(n, i)
		if err != nil {
			return nil, err
		}
		f.Cases = append(f.Cases, c)
	}
	if len(f.Cases) == 0 {
		return nil, fmt.Errorf("the fixture has no cases")
	}
	return f, nil
}

func parseCase(n *yamlite.Node, i int) (Case, error) {
	c := Case{Name: fmt.Sprintf("case %d", i+1), Line: n.Line}
	if n.Kind != yamlite.Map {
		return c, fmt.Errorf("line %d: each case must be a mapping", n.Line)
	}
	for _, p := range n.Pairs {
		var err error
		switch p.Key {
		case "name":
			if p.Value.Kind != yamlite.Scalar {
				return c, fmt.Errorf("line %d: name must be a single value", p.KeyLine)
			}
			c.Name = p.Value.Value
		case "request":
			c.Request, err = parseMessage(p.Value, true)
		case "upstream":
			c.Upstream, err = parseMessage(p.Value, false)
		case "expect":
			if p.Value.Kind != yamlite.Map {
				return c, fmt.Errorf("line %d: expect must be a mapping", p.KeyLine)
			}
			for _, e := range p.Value.Pairs {
				switch e.Key {
				case "upstream_request":
					m, err := parseMessage(e.Value, true)
					if err != nil {
						return c, err
					}
					c.ExpectUpstream = &m
				case "response":
					m, err := parseMessage(e.Value, false)
					if err != nil {
						return c, err
					}
					c.ExpectResponse = &m
				default:
					return c, fmt.Errorf("line %d: unknown key %q under expect (use upstream_request or response)", e.KeyLine, e.Key)
				}
			}
		default:
			return c, fmt.Errorf("line %d: unknown key %q in a case", p.KeyLine, p.Key)
		}
		if err != nil {
			return c, err
		}
	}
	if c.Request.Path == "" {
		return c, fmt.Errorf("line %d: case %q needs request.path", n.Line, c.Name)
	}
	if CheckPath(c.Request.Path) != nil {
		return c, fmt.Errorf("line %d: case %q: request.path %q must be a path such as /api/x?id=1, with spaces written as %%20", n.Line, c.Name, c.Request.Path)
	}
	if c.Request.Method != "" && !validMethod(c.Request.Method) {
		return c, fmt.Errorf("line %d: case %q: %q is not an HTTP method", n.Line, c.Name, c.Request.Method)
	}
	if c.Request.Method == "" {
		c.Request.Method = http.MethodGet
	}
	if c.Upstream.Status == 0 {
		c.Upstream.Status = http.StatusOK
	}
	if c.ExpectUpstream == nil && c.ExpectResponse == nil {
		return c, fmt.Errorf("line %d: case %q has nothing under expect", n.Line, c.Name)
	}
	return c, nil
}

func parseMessage(n *yamlite.Node, isRequest bool) (Message, error) {
	var m Message
	if n.Kind != yamlite.Map {
		return m, fmt.Errorf("line %d: expected a mapping", n.Line)
	}
	for _, p := range n.Pairs {
		v := p.Value
		if p.Key != "headers" && v.Kind != yamlite.Scalar && v.Kind != yamlite.Null {
			return m, fmt.Errorf("line %d: %s must be a single value (quote a JSON body: body: '{\"a\": 1}')", p.KeyLine, p.Key)
		}
		switch {
		case p.Key == "method" && isRequest:
			m.Method = strings.ToUpper(v.Value)
		case p.Key == "path" && isRequest:
			m.Path = v.Value
		case p.Key == "status" && !isRequest:
			code, err := strconv.Atoi(v.Value)
			if err != nil || code < 100 || code > 599 {
				return m, fmt.Errorf("line %d: status must be an HTTP status code", v.Line)
			}
			m.Status = code
		case p.Key == "headers":
			if v.Kind != yamlite.Map {
				return m, fmt.Errorf("line %d: headers must be a mapping", v.Line)
			}
			for _, h := range v.Pairs {
				if h.Value.Kind != yamlite.Scalar && h.Value.Kind != yamlite.Null {
					return m, fmt.Errorf("line %d: header %s must have a single value", h.KeyLine, h.Key)
				}
				val := h.Value.Value
				if h.Value.Kind == yamlite.Null {
					val = ""
				}
				m.Headers = append(m.Headers, [2]string{h.Key, val})
			}
		case p.Key == "body":
			s := v.Value
			if v.Kind == yamlite.Null {
				s = ""
			}
			m.Body = &s
		default:
			return m, fmt.Errorf("line %d: unknown key %q", p.KeyLine, p.Key)
		}
	}
	return m, nil
}

// CheckPath reports whether p is a request path a fixture can hold: it
// starts with /, has no host and no unescaped whitespace.
func CheckPath(p string) error {
	if u, err := url.ParseRequestURI(p); err != nil || !strings.HasPrefix(p, "/") || strings.ContainsAny(p, " \t\r\n") || u.Host != "" {
		return fmt.Errorf("%q is not a request path such as /api/x?id=1", p)
	}
	return nil
}

func validMethod(m string) bool {
	if m == "" {
		return false
	}
	for _, c := range m {
		if c < 'A' || c > 'Z' {
			return false
		}
	}
	return true
}

// CaseResult is the outcome of one case.
type CaseResult struct {
	Name     string
	Pass     bool
	Problems []string
}

// NewHandler builds the proxy under test, pointed at the stand-in upstream.
type NewHandler func(upstream *url.URL) (http.Handler, error)

// Run runs every case through a proxy made by newHandler.
func Run(newHandler NewHandler, f *Fixture) []CaseResult {
	var out []CaseResult
	for _, c := range f.Cases {
		out = append(out, runCase(newHandler, c))
	}
	return out
}

type seenRequest struct {
	method string
	uri    string
	header http.Header
	body   []byte
}

func runCase(newHandler NewHandler, c Case) CaseResult {
	res := CaseResult{Name: c.Name}
	var mu sync.Mutex
	var seen *seenRequest
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		seen = &seenRequest{method: r.Method, uri: r.URL.RequestURI(), header: r.Header.Clone(), body: b}
		mu.Unlock()
		for _, h := range c.Upstream.Headers {
			w.Header().Add(h[0], h[1])
		}
		w.WriteHeader(c.Upstream.Status)
		if c.Upstream.Body != nil {
			_, _ = io.WriteString(w, *c.Upstream.Body)
		}
	}))
	defer up.Close()
	u, _ := url.Parse(up.URL)
	px, err := newHandler(u)
	if err != nil {
		res.Problems = append(res.Problems, err.Error())
		return res
	}
	var body io.Reader
	if c.Request.Body != nil {
		body = strings.NewReader(*c.Request.Body)
	}
	req := httptest.NewRequest(c.Request.Method, c.Request.Path, body)
	for _, h := range c.Request.Headers {
		req.Header.Add(h[0], h[1])
	}
	rec := httptest.NewRecorder()
	px.ServeHTTP(rec, req)

	if e := c.ExpectUpstream; e != nil {
		mu.Lock()
		s := seen
		mu.Unlock()
		if s == nil {
			res.Problems = append(res.Problems, "the upstream received no request")
		} else {
			if e.Method != "" && e.Method != s.method {
				res.Problems = append(res.Problems, fmt.Sprintf("upstream method: got %s, want %s", s.method, e.Method))
			}
			if e.Path != "" && e.Path != s.uri {
				res.Problems = append(res.Problems, fmt.Sprintf("upstream path: got %s, want %s", s.uri, e.Path))
			}
			res.Problems = append(res.Problems, compareHeaders("upstream", s.header, e.Headers)...)
			if e.Body != nil {
				if p := compareBody("upstream", s.body, *e.Body); p != "" {
					res.Problems = append(res.Problems, p)
				}
			}
		}
	}
	if e := c.ExpectResponse; e != nil {
		if e.Status != 0 && e.Status != rec.Code {
			res.Problems = append(res.Problems, fmt.Sprintf("response status: got %d, want %d", rec.Code, e.Status))
		}
		res.Problems = append(res.Problems, compareHeaders("response", rec.Header(), e.Headers)...)
		if e.Body != nil {
			if p := compareBody("response", rec.Body.Bytes(), *e.Body); p != "" {
				res.Problems = append(res.Problems, p)
			}
		}
	}
	res.Pass = len(res.Problems) == 0
	return res
}

// compareHeaders checks the expected headers. An empty expected value means
// the header must be absent.
func compareHeaders(where string, got http.Header, want [][2]string) []string {
	var out []string
	for _, h := range want {
		vals := got.Values(h[0])
		if h[1] == "" {
			if len(vals) > 0 {
				out = append(out, fmt.Sprintf("%s header %s: should be absent, got %q", where, h[0], strings.Join(vals, ", ")))
			}
			continue
		}
		if strings.Join(vals, ", ") != h[1] {
			out = append(out, fmt.Sprintf("%s header %s: got %q, want %q", where, h[0], strings.Join(vals, ", "), h[1]))
		}
	}
	sort.Strings(out)
	return out
}

// compareBody compares JSON by meaning (key order and spacing ignored) and
// everything else byte for byte.
func compareBody(where string, got []byte, want string) string {
	var gv, wv any
	if json.Unmarshal(got, &gv) == nil && json.Unmarshal([]byte(want), &wv) == nil {
		if reflect.DeepEqual(gv, wv) {
			return ""
		}
		return fmt.Sprintf("%s body: got %s, want %s", where, strings.TrimSpace(string(got)), strings.TrimSpace(want))
	}
	if string(got) == want {
		return ""
	}
	return fmt.Sprintf("%s body: got %q, want %q", where, string(got), want)
}
