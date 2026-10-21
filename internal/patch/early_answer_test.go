package patch

import (
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/aftermarket-dev/aftermarket/internal/audit"
	"github.com/aftermarket-dev/aftermarket/internal/rules"
	tk "github.com/aftermarket-dev/aftermarket/internal/testkit"
)

// An upstream that answers before reading the end of the request body must
// not have its answer cut off. Go's server used to close the client's body
// as the answer went out, while the transport was still reading it, and the
// transport then dropped the upstream connection mid-answer. This was the
// rare "unexpected EOF" in the random pass-through tests on CI.
func TestEarlyAnswerIsNotCutOff(t *testing.T) {
	for _, cfg := range []string{neverMatches, shapeRule} {
		c, err := rules.Load([]byte(cfg))
		if err != nil {
			t.Fatal(err)
		}
		ea := &tk.EarlyAnswer{}
		u, _ := url.Parse("http://upstream.invalid")
		px, err := New(Options{Upstream: u, Config: c, Audit: audit.New(&syncBuffer{}), Transport: ea})
		if err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewServer(px)
		tk.CheckEarlyAnswer(t, srv.URL, ea)
		srv.Close()
	}
}
