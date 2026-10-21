// Package vendorapp holds the AfterConfig example: a vendor's config file
// at version 1.0 and 1.1, and an overlay with our changes.
package vendorapp_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aftermarket-dev/aftermarket/internal/config"
)

// TestUpgradeStopsWhereTheVendorChangedSomething is the AfterConfig promise
// on the example. On 1.0 the overlay applies. On 1.1 it stops at exactly the
// two changes whose defaults the vendor changed, and writes nothing.
func TestUpgradeStopsWhereTheVendorChangedSomething(t *testing.T) {
	o, err := config.LoadFile("overlay.yaml")
	if err != nil {
		t.Fatal(err)
	}
	v10, _ := os.ReadFile(filepath.Join("1.0", "config.json"))
	v11, _ := os.ReadFile(filepath.Join("1.1", "config.json"))
	out := filepath.Join(t.TempDir(), "config.json")

	r, err := o.Apply(v10)
	if err != nil || len(r.Problems) > 0 {
		t.Fatalf("1.0: %v %v", err, r.Problems)
	}
	if err := config.WriteOut(o.Base, out, r.Out); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(out)
	if !strings.Contains(string(first), `"timeout": 120`) || !strings.Contains(string(first), `"password": "from-our-vault"`) {
		t.Fatalf("output:\n%s", first)
	}

	r, err = o.Apply(v11)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, p := range r.Problems {
		lines = append(lines, p.String())
	}
	want := "line 8: set expects server.timeout to be 30, but the base has 60\n" +
		"line 14: set expects logging.format to be \"text\", but the base has \"json\""
	if strings.Join(lines, "\n") != want {
		t.Fatalf("1.1 problems:\n%s", strings.Join(lines, "\n"))
	}
	if r.Out != nil {
		t.Fatal("1.1 produced output despite the guards")
	}
	if after, _ := os.ReadFile(out); !bytes.Equal(after, first) {
		t.Fatal("the old output changed")
	}
	if again, _ := os.ReadFile(filepath.Join("1.0", "config.json")); !bytes.Equal(again, v10) {
		t.Fatal("the vendor file changed")
	}
}

func TestFixturePasses(t *testing.T) {
	o, err := config.LoadFile("overlay.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f, err := config.LoadFixture("fixture.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range o.Run(f) {
		if !r.Pass {
			t.Errorf("%s: %v", r.Name, r.Problems)
		}
	}
}
