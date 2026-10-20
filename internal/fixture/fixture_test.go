package fixture_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/aftermarket-dev/aftermarket/internal/fixture"
	"github.com/aftermarket-dev/aftermarket/internal/patch"
	"github.com/aftermarket-dev/aftermarket/internal/rules"
)

func patchHandler(cfg *rules.Config) fixture.NewHandler {
	return func(u *url.URL) (http.Handler, error) {
		return patch.New(patch.Options{Upstream: u, Config: cfg})
	}
}

func TestFixtureReportsFailures(t *testing.T) {
	cfg, err := rules.Load([]byte(`
rules:
  - id: h
    when: {path: /a}
    response: {set_headers: {X-Set: "1"}}
`))
	if err != nil {
		t.Fatal(err)
	}
	fx, err := fixture.Parse([]byte(`
cases:
  - name: passes
    request: {path: /a}
    upstream: {status: 200, headers: {X-Gone: x}, body: '{"b":1,"a":2}'}
    expect:
      response:
        status: 200
        headers: {X-Set: "1", X-Missing: ""}
        body: '{"a":2,"b":1}'
  - name: fails
    request: {method: post, path: /b?x=1, body: hi, headers: {Content-Type: text/plain}}
    upstream: {status: 201, body: plain}
    expect:
      upstream_request: {method: PUT, path: /b?x=2, body: ho}
      response: {status: 200, headers: {X-Set: "1"}, body: other}
`))
	if err != nil {
		t.Fatal(err)
	}
	res := fixture.Run(patchHandler(cfg), fx)
	if !res[0].Pass {
		t.Fatalf("case 1 should pass: %v", res[0].Problems)
	}
	if res[1].Pass || len(res[1].Problems) != 6 {
		t.Fatalf("case 2 should report 6 problems, got %d: %v", len(res[1].Problems), res[1].Problems)
	}
}

func TestFixtureErrors(t *testing.T) {
	cases := map[string]string{
		"":                                 "cases",
		"cases: []":                        "no cases",
		"cases:\n  - request: {path: /}\n": "nothing under expect",
		"cases:\n  - expect: {response: {status: 1}}":                                               "status must be",
		"cases:\n  - request: {}\n    expect: {response: {status: 200}}\n":                          "needs request.path",
		"cases:\n  - bogus: 1\n":                                                                    "unknown key",
		"other: 1\ncases: []\n":                                                                     "unknown key",
		"cases:\n  - request: {path: /a}\n    expect: {response: {headers: {X-A: [1]}}}\n":          "single value",
		"cases:\n  - request: {path: /a}\n    expect: {response: {body: {\"a\": 1}}}\n":             "must be a single value",
		"cases:\n  - request: {path: \"/a b\"}\n    expect: {response: {status: 200}}\n":            "must be a path",
		"cases:\n  - request: {path: nopath}\n    expect: {response: {status: 200}}\n":              "must be a path",
		"cases:\n  - request: {path: /a, method: \"G T\"}\n    expect: {response: {status: 200}}\n": "not an HTTP method",
		"cases:\n  - name: [x]\n    request: {path: /a}\n":                                          "name must be",
	}
	for src, want := range cases {
		_, err := fixture.Parse([]byte(src))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("fixture.Parse(%q) = %v, want error containing %q", src, err, want)
		}
	}
}
