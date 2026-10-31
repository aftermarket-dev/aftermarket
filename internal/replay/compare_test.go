package replay_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/aftermarket-dev/aftermarket/internal/replay"
)

func mustPattern(t *testing.T, s string) replay.Pattern {
	t.Helper()
	p, err := replay.ParsePattern(s)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// Values at masked paths and in masked headers are compared but never shown.
func TestCompareMasksValues(t *testing.T) {
	a := &replay.Answer{Status: 200, Header: http.Header{"Content-Type": {"application/json"}, "Set-Cookie": {"sid=secret-a"}},
		Body: []byte(`{"token":"secret-a","user":{"email":"a@x","name":"Ann"},"extra":{"key":"secret-x"},"same":"s3cr3t"}`)}
	b := &replay.Answer{Status: 200, Header: http.Header{"Content-Type": {"application/json"}, "Set-Cookie": {"sid=secret-b"}},
		Body: []byte(`{"token":"secret-b","user":{"email":"b@x","name":"Bob"},"same":"s3cr3t","key2":"secret-y"}`)}
	c := replay.Comparison{
		Headers:     []string{"Content-Type", "Set-Cookie"},
		MaskHeaders: map[string]bool{"Set-Cookie": true},
		MaskJSON:    []replay.Pattern{mustPattern(t, "token"), mustPattern(t, "*.email"), mustPattern(t, "extra"), mustPattern(t, "key2"), mustPattern(t, "same")},
	}
	diffs := c.Compare(a, b)
	got := strings.Join(diffs, "\n")
	for _, want := range []string{
		"header Set-Cookie: differs (REDACTED)",
		"token: differs (REDACTED)",
		"user.email: differs (REDACTED)",
		`user.name: "Ann" vs "Bob"`,
		"extra: only in A (REDACTED)",
		"key2: only in B (REDACTED)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "same") {
		t.Errorf("an equal masked value was reported:\n%s", got)
	}
	for _, secret := range []string{"secret", "a@x", "b@x", "s3cr3t"} {
		if strings.Contains(got, secret) {
			t.Errorf("%q reached the differences:\n%s", secret, got)
		}
	}
	// Ignore still wins inside a masked value.
	c.Ignore = []replay.Pattern{mustPattern(t, "token")}
	if strings.Contains(strings.Join(c.Compare(a, b), "\n"), "token") {
		t.Error("an ignored path was reported")
	}
}

// Without masks Compare reports exactly what replay reports.
func TestCompareIsReplaysComparison(t *testing.T) {
	a := &replay.Answer{Status: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: []byte(`{"a":1,"b":[1,2]}`)}
	b := &replay.Answer{Status: 404, Header: http.Header{"Content-Type": {"text/plain"}}, Body: []byte(`{"b":[1.0,2,3],"a":1e0}`)}
	got := replay.Comparison{Headers: []string{"Content-Type"}}.Compare(a, b)
	want := []string{"status: 200 vs 404", `header Content-Type: "application/json" vs "text/plain"`, "b: 2 items vs 3 items"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %q, want %q", got, want)
	}
}
