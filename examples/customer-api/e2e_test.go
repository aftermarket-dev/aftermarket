package customerapi_test

import (
	"net/http/httptest"
	"net/url"
	"testing"

	customerapi "github.com/aftermarket-dev/aftermarket/examples/customer-api"
	"github.com/aftermarket-dev/aftermarket/internal/adapt"
	"github.com/aftermarket-dev/aftermarket/internal/fixture"
)

func adaptOver(t *testing.T, cfg *adapt.Config, upstream string) string {
	t.Helper()
	u, _ := url.Parse(upstream)
	px, err := adapt.New(adapt.Options{Upstream: u, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(px)
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestContractIsValidAgainstV1 shows the suite describes the real v1 API.
func TestContractIsValidAgainstV1(t *testing.T) {
	v1 := httptest.NewServer(customerapi.NewV1())
	defer v1.Close()
	for _, c := range customerapi.Contract(v1.URL) {
		if c.Err != nil {
			t.Errorf("v1 fails its own contract: %s: %v", c.Name, c.Err)
		}
	}
}

// TestV2BreaksTheOldClient shows there is something to fix: against v2
// directly, every check fails.
func TestV2BreaksTheOldClient(t *testing.T) {
	v2 := httptest.NewServer(customerapi.NewV2())
	defer v2.Close()
	checks := customerapi.Contract(v2.URL)
	if failed := customerapi.Failed(checks); len(failed) != len(checks) {
		t.Fatalf("against v2 directly %d of %d checks failed; expected all", len(failed), len(checks))
	}
}

// TestOldClientPassesThroughAdapt is the promise of 0.2.0: the old client's
// whole suite passes against only the v2 service.
func TestOldClientPassesThroughAdapt(t *testing.T) {
	cfg, err := adapt.LoadFile("adapt.yaml")
	if err != nil {
		t.Fatal(err)
	}
	v2 := httptest.NewServer(customerapi.NewV2())
	defer v2.Close()
	for _, c := range customerapi.Contract(adaptOver(t, cfg, v2.URL)) {
		if c.Err != nil {
			t.Errorf("%s: %v", c.Name, c.Err)
		}
	}
}

// TestEveryEndpointIsNeeded removes each mapping in turn; the suite must
// then fail, which shows each mapping does real work.
func TestEveryEndpointIsNeeded(t *testing.T) {
	cfg, err := adapt.LoadFile("adapt.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for i, ep := range cfg.Endpoints {
		partial := *cfg
		partial.Endpoints = append(append([]*adapt.Endpoint{}, cfg.Endpoints[:i]...), cfg.Endpoints[i+1:]...)
		v2 := httptest.NewServer(customerapi.NewV2())
		failed := customerapi.Failed(customerapi.Contract(adaptOver(t, &partial, v2.URL)))
		v2.Close()
		if len(failed) == 0 {
			t.Errorf("the suite still passes without %s, so that mapping is not needed", ep.ID)
		}
	}
}

func TestFixtureFilePasses(t *testing.T) {
	cfg, err := adapt.LoadFile("adapt.yaml")
	if err != nil {
		t.Fatal(err)
	}
	fx, err := fixture.Load("fixture.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range fixture.Run(adapt.FixtureHandler(cfg), fx) {
		if !r.Pass {
			t.Errorf("%s: %v", r.Name, r.Problems)
		}
	}
}
