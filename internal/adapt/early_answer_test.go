package adapt_test

import (
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/aftermarket-dev/aftermarket/internal/adapt"
	"github.com/aftermarket-dev/aftermarket/internal/audit"
	tk "github.com/aftermarket-dev/aftermarket/internal/testkit"
)

// See TestEarlyAnswerIsNotCutOff in patch: a fast upstream's answer to a
// request with a body must arrive whole.
func TestEarlyAnswerIsNotCutOff(t *testing.T) {
	cfg, err := adapt.Load([]byte(exampleConfig(t)))
	if err != nil {
		t.Fatal(err)
	}
	ea := &tk.EarlyAnswer{}
	u, _ := url.Parse("http://upstream.invalid")
	px, err := adapt.New(adapt.Options{Upstream: u, Config: cfg, Audit: audit.New(&tk.AuditSink{}), Transport: ea})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(px)
	defer srv.Close()
	tk.CheckEarlyAnswer(t, srv.URL, ea)
}
