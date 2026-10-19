package rules

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

const good = `
version: 1
limits:
  max_body: 64KiB
  upstream_timeout: 5s
rules:
  - id: v1-to-v2
    description: old clients call /api/v1/customer?id=N
    when:
      method: GET
      path: /api/v1/customer
      query: {id: "*"}
    request:
      rewrite_path: {from: "^/api/v1/customer$", to: "/api/v2/customers/{query.id}"}
      set_headers: {X-Aftermarket: v1-to-v2}
  - id: v1-shape
    when:
      method: [GET, head]
      path: /api/v1/**
      status: 2xx
      content_type: application/json
    response:
      json:
        - rename: {from: customer.full_name, to: name}
        - copy: {from: customer.id, to: customer_id}
        - set: {path: legacy, value: true}
        - set: {path: count, value: 3}
        - set: {path: label, value: "3"}
        - delete: {path: customer}
      remove_headers: [X-Powered-By]
`

func TestLoadGood(t *testing.T) {
	cfg, err := Load([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxBody != 64<<10 || cfg.UpstreamTimeout != 5*time.Second {
		t.Fatalf("limits = %d %v", cfg.MaxBody, cfg.UpstreamTimeout)
	}
	if len(cfg.Rules) != 2 {
		t.Fatalf("rules = %d", len(cfg.Rules))
	}
	r0 := cfg.Rules[0]
	if r0.Request.RewritePath == nil || len(r0.Request.RewritePath.Tokens) != 1 || r0.Request.RewritePath.Tokens[0] != "id" {
		t.Fatalf("rewrite = %+v", r0.Request.RewritePath)
	}
	r1 := cfg.Rules[1]
	if got := strings.Join(r1.When.Methods, ","); got != "GET,HEAD" {
		t.Fatalf("methods = %s", got)
	}
	ops := r1.Response.JSON
	if len(ops) != 6 {
		t.Fatalf("ops = %d", len(ops))
	}
	if ops[2].Value != true {
		t.Errorf("set true -> %#v", ops[2].Value)
	}
	if ops[3].Value != interface{}(ops[3].Value) || ops[3].Describe() != "set count" {
		t.Errorf("describe = %s", ops[3].Describe())
	}
	if s, ok := ops[4].Value.(string); !ok || s != "3" {
		t.Errorf("quoted 3 should stay a string, got %#v", ops[4].Value)
	}
}

func TestLoadErrors(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"empty", "", "empty"},
		{"no rules", "version: 1\n", "missing rules"},
		{"unknown top", "rulez: []\n", `unknown key "rulez"`},
		{"bad version", "version: 2\nrules: []\n", "version must be 1"},
		{"missing id", "rules:\n  - when: {}\n    request: {set_headers: {A: b}}\n", "missing an id"},
		{"bad id", "rules:\n  - id: 'a b'\n    when: {}\n    request: {set_headers: {A: b}}\n", "may only use"},
		{"dup id", "rules:\n  - id: a\n    when: {}\n    request: {set_headers: {A: b}}\n  - id: a\n    when: {}\n    request: {set_headers: {A: b}}\n", "already used on line 2"},
		{"no when", "rules:\n  - id: a\n    request: {set_headers: {A: b}}\n", "missing a when"},
		{"no actions", "rules:\n  - id: a\n    when: {}\n", "no request: or response:"},
		{"status on request", "rules:\n  - id: a\n    when: {status: 200}\n    request: {set_headers: {A: b}}\n", "only have response actions"},
		{"bad status", "rules:\n  - id: a\n    when: {status: 700}\n    response: {set_headers: {A: b}}\n", "status \"700\""},
		{"bad path", "rules:\n  - id: a\n    when: {path: api}\n    response: {set_headers: {A: b}}\n", "must start with /"},
		{"managed header", "rules:\n  - id: a\n    when: {}\n    response: {set_headers: {content-length: 1}}\n", "managed by the proxy"},
		{"bad regexp", "rules:\n  - id: a\n    when: {}\n    request: {rewrite_path: {from: '(', to: /x}}\n", "not a valid regular expression"},
		{"rewrite on response", "rules:\n  - id: a\n    when: {}\n    response: {rewrite_path: {from: a, to: /b}}\n", `unknown key "rewrite_path"`},
		{"bad op", "rules:\n  - id: a\n    when: {}\n    response:\n      json:\n        - move: {from: a, to: b}\n", "unknown json operation"},
		{"same path", "rules:\n  - id: a\n    when: {}\n    response:\n      json:\n        - rename: {from: a, to: a}\n", "same path"},
		{"into itself", "rules:\n  - id: a\n    when: {}\n    response:\n      json:\n        - copy: {from: a, to: a.b}\n", "inside itself"},
		{"set no value", "rules:\n  - id: a\n    when: {}\n    response:\n      json:\n        - set: {path: a}\n", "set needs value"},
		{"bad size", "limits: {max_body: lots}\nrules: []\n", "max_body"},
		{"bad timeout", "limits: {upstream_timeout: -1s}\nrules: []\n", "upstream_timeout"},
		{"yaml error", "rules:\n\t- a\n", "tabs"},
		{"huge size", "limits: {max_body: 10000000000GiB}\nrules: []\n", "at most 1GiB"},
		{"over cap", "limits: {max_body: 9223372036854775807}\nrules: []\n", "at most 1GiB"},
		{"bad header name", "rules:\n  - id: a\n    when: {}\n    request: {set_headers: {\"Bad Name\": x}}\n", "not a valid header name"},
		{"control char", "rules:\n  - id: a\n    when: {}\n    request: {set_headers: {X: \"a\\0b\"}}\n", "control character"},
		{"bad remove name", "rules:\n  - id: a\n    when: {}\n    request: {remove_headers: [\"a b\"]}\n", "not a valid header name"},
		{"proxy auth", "rules:\n  - id: a\n    when: {}\n    request: {set_headers: {Proxy-Authorization: x}}\n", "managed by the proxy"},
		{"host match", "rules:\n  - id: a\n    when: {headers: {host: x}}\n    request: {set_headers: {X: y}}\n", "Host header"},
		{"rename to parent", "rules:\n  - id: a\n    when: {}\n    response:\n      json:\n        - rename: {from: a.b, to: a}\n", "onto one of its parents"},
		{"missing group", "rules:\n  - id: a\n    when: {}\n    request: {rewrite_path: {from: \"^/v1/(.*)$\", to: /v2/$1_detail}}\n", "no such group"},
		{"query in to", "rules:\n  - id: a\n    when: {}\n    request: {rewrite_path: {from: \"^/v1/(.*)$\", to: \"/v2/$1?f=json\"}}\n", "cannot contain ?"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Load([]byte(c.src))
			if err == nil {
				t.Fatalf("expected error containing %q", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error:\n%v\ndoes not contain %q", err, c.want)
			}
		})
	}
}

func TestRewriteGroupReferences(t *testing.T) {
	ok := []string{"/v2/$1", "/v2/${1}_detail", "/v2/${name}", "/v2/$name", "/v2/$$1", "/v2/{query.id}"}
	for _, to := range ok {
		src := "rules:\n  - id: a\n    when: {}\n    request: {rewrite_path: {from: \"^/v1/(?P<name>.*)$\", to: \"" + to + "\"}}\n"
		if _, err := Load([]byte(src)); err != nil {
			t.Errorf("to %q: %v", to, err)
		}
	}
}

func TestReportsAllErrors(t *testing.T) {
	_, err := Load([]byte("rules:\n  - id: a\n    when: {path: x}\n    response: {set_headers: {Host: y}}\n  - id: b\n"))
	errs, ok := err.(Errors)
	if !ok || len(errs) < 3 {
		t.Fatalf("expected several errors, got %v", err)
	}
}

func TestMatching(t *testing.T) {
	cfg, err := Load([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	w0, w1 := &cfg.Rules[0].When, &cfg.Rules[1].When
	h := http.Header{}
	if !w0.MatchRequest("GET", "/api/v1/customer", map[string][]string{"id": {"7"}}, h) {
		t.Error("rule 0 should match")
	}
	if w0.MatchRequest("GET", "/api/v1/customer", nil, h) {
		t.Error("rule 0 needs ?id")
	}
	if w0.MatchRequest("POST", "/api/v1/customer", map[string][]string{"id": {"7"}}, h) {
		t.Error("rule 0 is GET only")
	}
	if !w1.MatchRequest("HEAD", "/api/v1/a/b/c", nil, h) || w1.MatchRequest("GET", "/api/v2/x", nil, h) {
		t.Error("** glob")
	}
	if !w1.MatchResponse(204, "application/json; charset=utf-8") || w1.MatchResponse(404, "application/json") || w1.MatchResponse(200, "text/html") {
		t.Error("response match")
	}
}

func TestPathGlob(t *testing.T) {
	cases := []struct {
		glob, path string
		want       bool
	}{
		{"/a/*", "/a/b", true},
		{"/a/*", "/a/b/c", false},
		{"/a/**", "/a/b/c", true},
		{"/a/*/c", "/a/x/c", true},
		{"/a.b", "/aXb", false},
		{"/v*/x", "/v2/x", true},
	}
	for _, c := range cases {
		if got := compilePathGlob(c.glob).MatchString(c.path); got != c.want {
			t.Errorf("%s ~ %s = %v", c.glob, c.path, got)
		}
	}
}

func TestGlobMatch(t *testing.T) {
	cases := []struct {
		glob, s string
		want    bool
	}{
		{"*", "", true},
		{"abc", "abc", true},
		{"abc", "abcd", false},
		{"a*", "abc", true},
		{"*c", "abc", true},
		{"a*c", "ac", true},
		{"a*b*c", "aXbYc", true},
		{"a*b*c", "aXcYb", false},
		{"application/*json", "application/problem+json", true},
		{"ab*ba", "aba", false},
	}
	for _, c := range cases {
		if got := globMatch(c.glob, c.s); got != c.want {
			t.Errorf("globMatch(%q, %q) = %v", c.glob, c.s, got)
		}
	}
}

func TestOverlaps(t *testing.T) {
	src := `
rules:
  - id: a
    when: {path: /x/*}
    response: {set_headers: {X-A: "1"}}
  - id: b
    when: {path: /x/y}
    response: {set_headers: {X-A: "2"}}
  - id: c
    when: {path: /z}
    response: {set_headers: {X-A: "3"}}
  - id: d
    when: {path: /x/y, method: POST}
    response: {set_headers: {X-B: "3"}}
`
	cfg, err := Load([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	ov := cfg.Overlaps()
	if len(ov) != 1 || ov[0].A.ID != "a" || ov[0].B.ID != "b" {
		t.Fatalf("overlaps = %+v", ov)
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string]int64{"10": 10, "1KB": 1000, "1KiB": 1024, "2MiB": 2 << 20, "3 MB": 3000000} {
		got, err := ParseSize(in)
		if err != nil || got != want {
			t.Errorf("ParseSize(%q) = %d, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "0", "-1", "1TB", "x"} {
		if _, err := ParseSize(bad); err == nil {
			t.Errorf("ParseSize(%q) should fail", bad)
		}
	}
}
