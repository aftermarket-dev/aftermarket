package guard

import (
	"testing"

	tk "github.com/aftermarket-dev/aftermarket/internal/testkit"
)

// See TestEarlyAnswerIsNotCutOff in patch: a fast app's answer to a request
// with a body must arrive whole through AfterGuard.
func TestEarlyAnswerIsNotCutOff(t *testing.T) {
	ea := &tk.EarlyAnswer{}
	r := start(t, openConfig, func(o *Options) { o.Transport = ea })
	tk.CheckEarlyAnswer(t, r.url, ea)
}

// A health check, and a denied request, each with a body, answer without
// the app and must keep the connection usable.
func TestHealthCheckWithBody(t *testing.T) {
	r := start(t, openConfig)
	tk.CheckHealthWithBody(t, r.url, DefaultHealthPath)
}
