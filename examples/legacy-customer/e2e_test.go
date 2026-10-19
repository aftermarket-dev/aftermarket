package legacycustomer_test

import (
	"errors"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	legacycustomer "github.com/aftermarket-dev/aftermarket/examples/legacy-customer"
	"github.com/aftermarket-dev/aftermarket/internal/patch"
	"github.com/aftermarket-dev/aftermarket/internal/rules"
)

func proxyWith(t *testing.T, cfg *rules.Config, upstream string) string {
	t.Helper()
	u, _ := url.Parse(upstream)
	px, err := patch.New(patch.Options{Upstream: u, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(px)
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestOldClientNeedsTheRules is the proof the project is built around: the
// old client fails against the new service, still fails through AfterPatch
// with no rules for it, and works through AfterPatch with rules.yaml.
func TestOldClientNeedsTheRules(t *testing.T) {
	svc := httptest.NewServer(legacycustomer.NewService())
	defer svc.Close()

	if _, err := legacycustomer.Fetch(svc.URL, 7); err == nil {
		t.Fatal("the old client should fail against the new service directly")
	}

	unrelated, err := rules.Load([]byte("rules:\n  - id: unrelated\n    when: {path: /nothing}\n    response: {set_headers: {X-Unrelated: \"1\"}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacycustomer.Fetch(proxyWith(t, unrelated, svc.URL), 7); err == nil {
		t.Fatal("the old client should fail through the proxy without the rules")
	}

	cfg, err := rules.LoadFile(filepath.Join(".", "rules.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	base := proxyWith(t, cfg, svc.URL)
	for id, name := range map[int]string{7: "Ada Lovelace", 42: "Grace Hopper"} {
		c, err := legacycustomer.Fetch(base, id)
		if err != nil {
			t.Fatalf("with the rules, customer %d: %v", id, err)
		}
		if c.CustomerID != id || c.Name != name {
			t.Fatalf("customer %d = %+v", id, c)
		}
	}
	if _, err := legacycustomer.Fetch(base, 999); !errors.Is(err, legacycustomer.ErrNotFound) {
		t.Fatalf("a missing customer should stay a 404, got %v", err)
	}
}

// TestRemovingEitherRuleBreaksTheClient shows each rule is needed.
func TestRemovingEitherRuleBreaksTheClient(t *testing.T) {
	svc := httptest.NewServer(legacycustomer.NewService())
	defer svc.Close()
	cfg, err := rules.LoadFile("rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for i := range cfg.Rules {
		partial := *cfg
		partial.Rules = append(append([]*rules.Rule{}, cfg.Rules[:i]...), cfg.Rules[i+1:]...)
		if _, err := legacycustomer.Fetch(proxyWith(t, &partial, svc.URL), 7); err == nil {
			t.Fatalf("the client still works without rule %s, so that rule is not needed", cfg.Rules[i].ID)
		}
	}
}

func TestFixtureFilePasses(t *testing.T) {
	cfg, err := rules.LoadFile("rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	fx, err := patch.LoadFixture("fixture.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range patch.RunFixture(cfg, fx) {
		if !r.Pass {
			t.Errorf("%s: %v", r.Name, r.Problems)
		}
	}
}
