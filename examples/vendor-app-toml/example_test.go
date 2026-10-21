// Package vendorapptoml holds the AfterConfig example for TOML: a vendor's
// config.toml at version 1.0 and 1.1, and an overlay with our changes.
package vendorapptoml_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aftermarket-dev/aftermarket/internal/config"
)

// On 1.0 the overlay applies, and only the seven changed lines differ from
// the vendor's file: comments and every other line come through as written.
// On 1.1 it stops at exactly the two defaults the vendor changed.
func TestUpgradeStopsAndCommentsStay(t *testing.T) {
	o, err := config.LoadFile("overlay.yaml")
	if err != nil {
		t.Fatal(err)
	}
	v10, _ := os.ReadFile(filepath.Join("1.0", "config.toml"))
	v11, _ := os.ReadFile(filepath.Join("1.1", "config.toml"))
	out := filepath.Join(t.TempDir(), "config.toml")

	r, err := o.Apply(v10)
	if err != nil || len(r.Problems) > 0 {
		t.Fatalf("1.0: %v %v", err, r.Problems)
	}
	if err := config.WriteOut(o.Base, out, r.Out); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(out)
	before, after := strings.Split(string(v10), "\n"), strings.Split(string(first), "\n")
	if len(before) != len(after) {
		t.Fatalf("line count changed:\n%s", first)
	}
	changed := 0
	for i := range before {
		if before[i] != after[i] {
			changed++
		}
	}
	if changed != 7 || !strings.Contains(string(first), "timeout = 120        # seconds") {
		t.Fatalf("%d lines changed:\n%s", changed, first)
	}

	r, err = o.Apply(v11)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, p := range r.Problems {
		lines = append(lines, p.String())
	}
	want := "line 9: set expects server.timeout to be 30, but the base has 60\n" +
		"line 15: set expects logging.format to be \"text\", but the base has \"json\""
	if strings.Join(lines, "\n") != want || r.Out != nil {
		t.Fatalf("1.1 problems:\n%s", strings.Join(lines, "\n"))
	}
	if again, _ := os.ReadFile(filepath.Join("1.0", "config.toml")); !bytes.Equal(again, v10) {
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
