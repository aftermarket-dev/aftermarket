package adapt

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

const goodConfig = `
version: 1
limits: {max_body: 64KiB, upstream_timeout: 5s}
endpoints:
  - id: get
    old: GET /api/v1/customer?id={id}
    new: GET /api/v2/customers/{id}
    when: {headers: {User-Agent: "Legacy*"}}
    response:
      status: {404: 404}
      body: {customer_id: $.customer.id}
      error: {message: $.error.message}
      headers: {rename: {X-New: X-Old}, set: {X-Adapted: "1"}, remove: [X-Api-Version]}
  - id: orders
    old: GET /api/v1/customers/{cid}/orders/{oid}
    new: GET /api/v2/orders/{oid}.json?customer={cid}&expand=items
  - id: create
    old: POST /api/v1/customers
    new: PUT /api/v2/customers
    request:
      body: {full_name: $.name, source: legacy, id: $$literal}
      headers: {rename: {X-Old-Trace: X-Trace}}
`

func TestLoadGood(t *testing.T) {
	cfg, err := Load([]byte(goodConfig))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Endpoints) != 3 || cfg.MaxBody != 64<<10 {
		t.Fatalf("cfg = %+v", cfg)
	}
	e := cfg.Endpoints[1]
	if len(e.Old.Segments) != 6 || e.Old.Segments[3].param != "cid" || e.Old.Segments[5].param != "oid" {
		t.Fatalf("segments = %+v", e.Old.Segments)
	}
	p, q := e.New.render(map[string]string{"cid": "a b", "oid": "9&x"})
	if p != "/api/v2/orders/9&x.json" || q != "customer=a+b&expand=items" {
		t.Fatalf("render = %q %q", p, q)
	}
	if got := cfg.Endpoints[0].Summary(); !strings.Contains(got, "status 404->404") {
		t.Fatalf("summary = %s", got)
	}
}

func TestLoadErrors(t *testing.T) {
	ep := func(s string) string { return "endpoints:\n  - id: e\n" + s }
	cases := []struct{ name, src, want string }{
		{"no endpoints", "version: 1\n", "missing endpoints"},
		{"unknown key", ep("    old: GET /a\n    new: GET /b\n    bogus: 1\n"), `unknown key "bogus"`},
		{"no new", ep("    old: GET /a\n"), "needs both old and new"},
		{"bad method", ep("    old: get /a\n    new: GET /b\n"), "not an HTTP method"},
		{"no slash", ep("    old: GET a\n    new: GET /b\n"), "must start with /"},
		{"partial param", ep("    old: GET /a/x{id}\n    new: GET /b\n"), "whole path segment"},
		{"dup param", ep("    old: GET /a/{id}?x={id}\n    new: GET /b\n"), "captured twice"},
		{"bad query", ep("    old: GET /a?x=1\n    new: GET /b\n"), "must capture a parameter"},
		{"unknown in new", ep("    old: GET /a/{id}\n    new: GET /b/{other}\n"), "{other} is not captured"},
		{"dot segment", ep("    old: GET /a/{id}\n    new: GET /b/../{id}\n"), "not allowed"},
		{"get body", ep("    old: POST /a\n    new: GET /b\n    request: {body: {x: 1}}\n"), "has no body"},
		{"bad ref", ep("    old: GET /a\n    new: GET /b\n    response: {body: {x: $foo}}\n"), "is not a reference"},
		{"unknown param", ep("    old: GET /a/{id}\n    new: GET /b/{id}\n    response: {body: {x: $params.nope}}\n"), "no parameter called"},
		{"dollar key", ep("    old: GET /a\n    new: GET /b\n    response: {body: {$x: 1}}\n"), "starts with $"},
		{"each alone", ep("    old: GET /a\n    new: GET /b\n    response: {body: {$each: $.a}}\n"), "exactly two keys"},
		{"each param", ep("    old: GET /a/{id}\n    new: GET /b\n    response: {body: {$each: $params.id, $item: 1}}\n"), "list in the body"},
		{"bad status", ep("    old: GET /a\n    new: GET /b\n    response: {status: {201: 204}}\n"), "other than 204"},
		{"bad from", ep("    old: GET /a\n    new: GET /b\n    response: {status: {99: 200}}\n"), "from 200 to 599"},
		{"credential", ep("    old: GET /a\n    new: GET /b\n    request: {headers: {rename: {Authorization: X-Auth}}}\n"), "carries credentials"},
		{"cookie set", ep("    old: GET /a\n    new: GET /b\n    response: {headers: {set: {Set-Cookie: x}}}\n"), "carries credentials"},
		{"managed", ep("    old: GET /a\n    new: GET /b\n    response: {headers: {remove: [Content-Length]}}\n"), "managed by the proxy"},
		{"bad header", ep("    old: GET /a\n    new: GET /b\n    response: {headers: {set: {\"a b\": x}}}\n"), "not a valid header name"},
		{"rename self", ep("    old: GET /a\n    new: GET /b\n    response: {headers: {rename: {X-A: x-a}}}\n"), "renamed to itself"},
		{"host when", ep("    old: GET /a\n    new: GET /b\n    when: {headers: {Host: x}}\n"), "can't be matched"},
		{"dup id", "endpoints:\n  - {id: a, old: GET /a, new: GET /b}\n  - {id: a, old: GET /c, new: GET /d}\n", "already used on line 2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Load([]byte(c.src))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error = %v, want %q", err, c.want)
			}
		})
	}
}

func TestMatch(t *testing.T) {
	cfg, err := Load([]byte(goodConfig))
	if err != nil {
		t.Fatal(err)
	}
	get := cfg.Endpoints[0]
	h := http.Header{"User-Agent": {"Legacy/1.0"}}
	segs := func(p string) []string { return strings.Split(p, "/")[1:] }
	q := func(s string) url.Values { v, _ := url.ParseQuery(s); return v }

	if ps, ok, skip := get.match("GET", segs("/api/v1/customer"), q("id=7"), h); !ok || skip != "" || ps["id"] != "7" {
		t.Fatalf("match = %v %v %q", ps, ok, skip)
	}
	if _, ok, _ := get.match("GET", segs("/api/v1/customer"), q("id=7"), http.Header{}); ok {
		t.Fatal("when.headers must hold")
	}
	if _, ok, _ := get.match("GET", segs("/api/v1/customer"), q(""), h); ok {
		t.Fatal("a missing query parameter is no match")
	}
	if _, ok, _ := get.match("GET", segs("/api/v1/customer"), q("id="), h); ok {
		t.Fatal("an empty query parameter is no match")
	}
	if _, ok, _ := get.match("GET", segs("/api/v1/customer/"), q("id=7"), h); ok {
		t.Fatal("a trailing slash is a different path")
	}
	if _, ok, skip := get.match("GET", segs("/api/v1/customer"), q("id=7&id=8"), h); !ok || !strings.Contains(skip, "more than once") {
		t.Fatalf("repeated parameter: %v %q", ok, skip)
	}
	for _, bad := range []string{"..", ".", "a/b", `a\b`} {
		if _, ok, skip := get.match("GET", segs("/api/v1/customer"), url.Values{"id": {bad}}, h); !ok || !strings.Contains(skip, "safe path segment") {
			t.Errorf("id=%q: %v %q", bad, ok, skip)
		}
	}
}

func TestOverlaps(t *testing.T) {
	cfg, err := Load([]byte(`
endpoints:
  - {id: a, old: "GET /c/{id}", new: GET /x}
  - {id: b, old: "GET /c/me", new: GET /y}
  - {id: c, old: "POST /c/me", new: GET /z}
  - {id: d, old: "GET /d/{id}", new: GET /w}
`))
	if err != nil {
		t.Fatal(err)
	}
	ov := cfg.Overlaps()
	if len(ov) != 1 || ov[0].A.ID != "a" || ov[0].B.ID != "b" {
		t.Fatalf("overlaps = %+v", ov)
	}
}

func TestLeftoverQuery(t *testing.T) {
	caps := []queryCapture{{key: "id", param: "id"}, {key: "a b", param: "ab"}}
	cases := map[string]string{
		"":                       "",
		"id=7":                   "",
		"id=7&limit=2":           "limit=2",
		"x=1;y=2&id=7&z":         "x=1;y=2&z",
		"a+b=1&a%20b=2&keep=%zz": "keep=%zz",
		"page=9&id=1&x=2":        "x=2",
	}
	for in, want := range cases {
		if got := leftoverQuery(in, caps, []string{"page"}); got != want {
			t.Errorf("leftoverQuery(%q) = %q, want %q", in, got, want)
		}
	}
}
