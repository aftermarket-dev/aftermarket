package customerapi_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	customerapi "github.com/aftermarket-dev/aftermarket/examples/customer-api"
	"github.com/aftermarket-dev/aftermarket/internal/adapt"
	"github.com/aftermarket-dev/aftermarket/internal/fixture"
	"github.com/aftermarket-dev/aftermarket/internal/record"
	"github.com/aftermarket-dev/aftermarket/internal/replay"
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

// recordContract runs the old client's suite against a fresh v1 through the
// recorder and returns the recording.
func recordContract(t *testing.T) *fixture.Fixture {
	t.Helper()
	v1 := httptest.NewServer(customerapi.NewV1())
	defer v1.Close()
	u, _ := url.Parse(v1.URL)
	var out bytes.Buffer
	var mu sync.Mutex
	sink := &lockedWriter{mu: &mu, w: &out}
	rec, err := record.New(record.Options{Upstream: u, Out: sink})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(rec)
	defer srv.Close()
	if failed := customerapi.Failed(customerapi.Contract(srv.URL)); len(failed) > 0 {
		t.Fatalf("the suite fails through the recorder: %v", failed)
	}
	deadline := time.Now().Add(5 * time.Second)
	for rec.Cases() < 12 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	f, err := fixture.Parse(out.Bytes())
	if err != nil {
		t.Fatalf("recording: %v", err)
	}
	if len(f.Cases) != 12 {
		t.Fatalf("recorded %d cases, want one per check (12)", len(f.Cases))
	}
	return f
}

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// TestRecordedSuiteReplaysTheSameThroughAdapt is the AfterTest and AfterDiff
// promise on the example: the old client's traffic, recorded against v1,
// gets exactly the same answers from v2 through adapt, and different answers
// from v2 directly.
func TestRecordedSuiteReplaysTheSameThroughAdapt(t *testing.T) {
	f := recordContract(t)
	cfg, err := adapt.LoadFile("adapt.yaml")
	if err != nil {
		t.Fatal(err)
	}
	fresh := func(h http.Handler) *url.URL {
		s := httptest.NewServer(h)
		t.Cleanup(s.Close)
		u, _ := url.Parse(s.URL)
		return u
	}
	v2 := fresh(customerapi.NewV2())
	adapted, _ := url.Parse(adaptOver(t, cfg, v2.String()))

	rep, err := replay.Run(context.Background(), f, replay.Options{A: fresh(customerapi.NewV1()), B: adapted, IncludeWrites: true})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK() || rep.Same != 12 {
		var md bytes.Buffer
		rep.WriteMarkdown(&md)
		t.Fatalf("v1 and v2 through adapt differ:\n%s", md.String())
	}

	rep, _ = replay.Run(context.Background(), f, replay.Options{A: fresh(customerapi.NewV1()), B: fresh(customerapi.NewV2()), IncludeWrites: true})
	if rep.Diff != 12 {
		t.Fatalf("v1 and v2 directly: %s; every case should differ", rep.Summary())
	}

	// Without --include-writes only the eight reads are sent.
	rep, _ = replay.Run(context.Background(), f, replay.Options{A: fresh(customerapi.NewV1()), B: fresh(customerapi.NewV2())})
	if rep.Diff != 8 || rep.Skipped != 4 {
		t.Fatalf("reads only: %s", rep.Summary())
	}

	// One service against the recording: v2 through adapt answers what v1 did.
	rep, _ = replay.Run(context.Background(), f, replay.Options{A: fresh(adaptHandler(t, cfg, fresh(customerapi.NewV2()))), IncludeWrites: true})
	if !rep.OK() || rep.Same != 12 {
		t.Fatalf("against the recording: %s", rep.Summary())
	}

	// The recording is an ordinary fixture: through a proxy that changes
	// nothing, every case passes.
	for _, r := range fixture.Run(func(u *url.URL) (http.Handler, error) {
		return record.New(record.Options{Upstream: u, Out: io.Discard})
	}, f) {
		if !r.Pass {
			t.Fatalf("%s: %v", r.Name, r.Problems)
		}
	}
}

func adaptHandler(t *testing.T, cfg *adapt.Config, upstream *url.URL) http.Handler {
	t.Helper()
	px, err := adapt.New(adapt.Options{Upstream: upstream, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	return px
}
