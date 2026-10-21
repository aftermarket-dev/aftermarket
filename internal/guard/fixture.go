package guard

import (
	"fmt"
	"net/http/httptest"
	"net/netip"
	"net/textproto"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/aftermarket-dev/aftermarket/internal/rules"
	"github.com/aftermarket-dev/aftermarket/internal/yamlite"
)

// Case is one fixture case: a request, how many times it is sent, and the
// decision expected for the last one.
type Case struct {
	Name    string
	Line    int
	Method  string
	Target  string // path and query
	Headers [][2]string
	From    string
	Body    string
	Times   int

	Decision string
	Status   int
	Rule     string // "-" for none
	Client   string // "-" for none
}

// LoadFixture reads a guard fixture file.
func LoadFixture(path string) ([]*Case, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	root, err := yamlite.Parse(data)
	if err != nil {
		return nil, err
	}
	var errs rules.Errors
	errf := func(line int, f string, a ...any) {
		errs = append(errs, &rules.LineError{Line: line, Msg: fmt.Sprintf(f, a...)})
	}
	casesNode := root.Get("cases")
	if root.Kind != yamlite.Map || casesNode == nil || casesNode.Kind != yamlite.Seq || len(casesNode.Items) == 0 {
		return nil, rules.Errors{&rules.LineError{Line: root.Line, Msg: "a guard fixture needs cases: with at least one case"}}
	}
	var out []*Case
	for _, n := range casesNode.Items {
		c := &Case{Line: n.Line, Method: "GET", From: "127.0.0.1", Times: 1}
		if n.Kind != yamlite.Map {
			errf(n.Line, "each case must be a mapping with name:, request: and expect:")
			continue
		}
		for _, p := range n.Pairs {
			switch p.Key {
			case "name":
				c.Name = p.Value.Value
			case "times":
				t, err := strconv.Atoi(p.Value.Value)
				if err != nil || t < 1 || t > 100000 {
					errf(p.KeyLine, "times must be a whole number from 1 to 100000")
				}
				c.Times = t
			case "request":
				if p.Value.Kind != yamlite.Map {
					errf(p.KeyLine, "request must be a mapping")
					continue
				}
				for _, q := range p.Value.Pairs {
					switch q.Key {
					case "method":
						c.Method = strings.ToUpper(q.Value.Value)
					case "path":
						c.Target = q.Value.Value
					case "from":
						c.From = q.Value.Value
						if _, err := netip.ParseAddr(c.From); err != nil {
							errf(q.KeyLine, "from must be an address such as 203.0.113.7")
						}
					case "body":
						c.Body = q.Value.Value
					case "headers":
						if q.Value.Kind != yamlite.Map {
							errf(q.KeyLine, "headers must be a mapping")
							continue
						}
						for _, h := range q.Value.Pairs {
							c.Headers = append(c.Headers, [2]string{h.Key, h.Value.Value})
						}
					default:
						errf(q.KeyLine, "unknown key %q in request (use method, path, headers, from or body)", q.Key)
					}
				}
			case "expect":
				if p.Value.Kind != yamlite.Map {
					errf(p.KeyLine, "expect must be a mapping")
					continue
				}
				for _, q := range p.Value.Pairs {
					switch q.Key {
					case "decision":
						c.Decision = q.Value.Value
						switch c.Decision {
						case Allowed, Denied, Rejected, Limited:
						default:
							errf(q.KeyLine, "decision must be allowed, denied, rejected or limited")
						}
					case "status":
						s, err := strconv.Atoi(q.Value.Value)
						if err != nil {
							errf(q.KeyLine, "status must be a number")
						}
						c.Status = s
					case "rule":
						c.Rule = q.Value.Value
					case "client":
						c.Client = q.Value.Value
					default:
						errf(q.KeyLine, "unknown key %q in expect (use decision, status, rule or client)", q.Key)
					}
				}
			default:
				errf(p.KeyLine, "unknown key %q in a case (use name, request, times or expect)", p.Key)
			}
		}
		if c.Name == "" {
			errf(n.Line, "each case needs a name")
		}
		if !strings.HasPrefix(c.Target, "/") {
			errf(n.Line, "case %q needs request: path: starting with /", c.Name)
		}
		if c.Decision == "" {
			errf(n.Line, "case %q needs expect: decision:", c.Name)
		}
		out = append(out, c)
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return out, nil
}

// Result is one case's outcome.
type Result struct {
	Name string
	Pass bool
	Got  string
	Want string
}

// RunFixture sends each case's request through the rules, in order, on one
// guard, so a later case sees the limits earlier ones used up.
func (c *Config) RunFixture(cases []*Case) ([]Result, error) {
	g, err := New(Options{Config: c, Upstream: &url.URL{Scheme: "http", Host: "upstream.invalid"}})
	if err != nil {
		return nil, err
	}
	var out []Result
	for _, cs := range cases {
		var d Decision
		for i := 0; i < cs.Times; i++ {
			req := httptest.NewRequest(cs.Method, cs.Target, strings.NewReader(cs.Body))
			if cs.Body == "" {
				req = httptest.NewRequest(cs.Method, cs.Target, nil)
			}
			for _, h := range cs.Headers {
				req.Header.Add(textproto.CanonicalMIMEHeaderKey(h[0]), h[1])
			}
			d = g.Decide(req, netip.AddrPortFrom(mustAddr(cs.From), 40000).String())
		}
		res := Result{Name: cs.Name, Pass: true}
		res.Got = describe(d)
		var want []string
		want = append(want, cs.Decision)
		if d.Event != cs.Decision {
			res.Pass = false
		}
		if cs.Status != 0 {
			want = append(want, fmt.Sprintf("status %d", cs.Status))
			if d.Status != cs.Status {
				res.Pass = false
			}
		}
		if cs.Rule != "" {
			want = append(want, "rule "+cs.Rule)
			if !(d.Rule == cs.Rule || (cs.Rule == "-" && d.Rule == "")) {
				res.Pass = false
			}
		}
		if cs.Client != "" {
			want = append(want, "client "+cs.Client)
			if !(d.Client == cs.Client || (cs.Client == "-" && d.Client == "")) {
				res.Pass = false
			}
		}
		res.Want = strings.Join(want, ", ")
		out = append(out, res)
	}
	return out, nil
}

func mustAddr(s string) netip.Addr {
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.MustParseAddr("127.0.0.1")
	}
	return a
}

func describe(d Decision) string {
	parts := []string{d.Event}
	if d.Event != Allowed {
		parts = append(parts, fmt.Sprintf("status %d", d.Status))
	}
	if d.Rule != "" {
		parts = append(parts, "rule "+d.Rule)
	}
	if d.Client != "" {
		parts = append(parts, "client "+d.Client)
	}
	if d.Reason != "" && d.Event != Allowed {
		parts = append(parts, "("+d.Reason+")")
	}
	return strings.Join(parts, ", ")
}
