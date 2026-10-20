package record

import (
	"math/rand"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/aftermarket-dev/aftermarket/internal/yamlite"
)

// TestQuoteRoundTrips checks that every string the recorder writes is read
// back exactly, whatever characters it holds.
func TestQuoteRoundTrips(t *testing.T) {
	rs := rand.New(rand.NewSource(1))
	pool := []rune{'a', 'Z', '0', ' ', '"', '\\', '\'', '#', ':', '-', '\n', '\r', '\t', 0, 1, 0x1b, 0x7f, 'é', 'ß', '世', '😀', 0x2028, 0xfeff, '{', '}', '[', ']', '&', '*', '!', '|', '>', '%', '@', '`', '$'}
	cases := []string{"", " ", "  lead", "trail ", "null", "true", "~", "1e3", "- x", "a: b", "#x", "x #y", "'", "\\u0041"}
	for i := 0; i < 2000; i++ {
		var b strings.Builder
		for j := 0; j < rs.Intn(40); j++ {
			b.WriteRune(pool[rs.Intn(len(pool))])
		}
		cases = append(cases, b.String())
	}
	for _, s := range cases {
		if !utf8.ValidString(s) {
			continue
		}
		doc := "k: " + quote(s) + "\n"
		n, err := yamlite.Parse([]byte(doc))
		if err != nil {
			t.Fatalf("%q: %v (%s)", s, err, doc)
		}
		got := n.Get("k")
		if got == nil || got.Kind != yamlite.Scalar || got.Value != s {
			t.Fatalf("%q came back as %+v", s, got)
		}
	}
}
