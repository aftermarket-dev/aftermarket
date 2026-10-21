package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/aftermarket-dev/aftermarket/internal/yamlite"
)

// Fixture holds test cases for an overlay.
type Fixture struct {
	Cases []FixtureCase
}

// FixtureCase applies the overlay to Base and checks the outcome: either the
// output matches Expect by meaning, or apply stops with a problem that
// contains ExpectError.
type FixtureCase struct {
	Name        string
	Line        int
	Base        string
	Expect      *string
	ExpectError string
}

// LoadFixture reads a config fixture file.
func LoadFixture(path string) (*Fixture, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	root, err := yamlite.Parse(data)
	if err != nil {
		return nil, err
	}
	cs := root.Get("cases")
	if root.Kind != yamlite.Map || cs == nil || cs.Kind != yamlite.Seq || len(cs.Items) == 0 {
		return nil, fmt.Errorf("a fixture file needs a cases: list")
	}
	for _, p := range root.Pairs {
		if p.Key != "cases" {
			return nil, fmt.Errorf("line %d: unknown key %q", p.KeyLine, p.Key)
		}
	}
	f := &Fixture{}
	for i, n := range cs.Items {
		c := FixtureCase{Name: fmt.Sprintf("case %d", i+1), Line: n.Line}
		if n.Kind != yamlite.Map {
			return nil, fmt.Errorf("line %d: each case must be a mapping", n.Line)
		}
		hasBase := false
		for _, p := range n.Pairs {
			if p.Value.Kind != yamlite.Scalar && p.Value.Kind != yamlite.Null {
				return nil, fmt.Errorf("line %d: %s must be text (use | for a block)", p.KeyLine, p.Key)
			}
			v := p.Value.Value
			switch p.Key {
			case "name":
				c.Name = v
			case "base":
				c.Base, hasBase = v, true
			case "expect":
				c.Expect = &v
			case "expect_error":
				c.ExpectError = v
			default:
				return nil, fmt.Errorf("line %d: unknown key %q in a case (use name, base, expect or expect_error)", p.KeyLine, p.Key)
			}
		}
		if !hasBase {
			return nil, fmt.Errorf("line %d: case %q needs base", n.Line, c.Name)
		}
		if (c.Expect == nil) == (c.ExpectError == "") {
			return nil, fmt.Errorf("line %d: case %q needs exactly one of expect or expect_error", n.Line, c.Name)
		}
		f.Cases = append(f.Cases, c)
	}
	return f, nil
}

// CaseResult is the outcome of one case.
type CaseResult struct {
	Name     string
	Pass     bool
	Problems []string
}

// Run runs every case.
func (o *Overlay) Run(f *Fixture) []CaseResult {
	var out []CaseResult
	for _, c := range f.Cases {
		res := CaseResult{Name: c.Name}
		r, err := o.Apply([]byte(c.Base))
		switch {
		case err != nil:
			res.Problems = append(res.Problems, "base: "+err.Error())
		case c.ExpectError != "":
			found := false
			var got []string
			for _, p := range r.Problems {
				got = append(got, p.String())
				found = found || strings.Contains(p.String(), c.ExpectError)
			}
			if !found {
				res.Problems = append(res.Problems, fmt.Sprintf("want a problem containing %q, got %q", c.ExpectError, got))
			}
		case len(r.Problems) > 0:
			for _, p := range r.Problems {
				res.Problems = append(res.Problems, p.String())
			}
		default:
			want, err := parse(o.Format, []byte(*c.Expect))
			if err != nil {
				res.Problems = append(res.Problems, "expect: "+err.Error())
				break
			}
			got, _ := parse(o.Format, r.Out)
			if got == nil || !exact(o.Format, got.root(), want.root()) {
				// Listed field by field from expected to actual, secrets hidden.
				var diff []string
				if got != nil {
					walkDiff(nil, want.root(), got.root(), r.redact, &diff)
				}
				if got != nil && len(diff) == 0 {
					diff = append(diff, "the values match but a TOML type differs (a date against text, or an integer against a float)")
				}
				res.Problems = append(res.Problems, "output differs from expect: "+strings.Join(diff, "; "))
			}
		}
		res.Pass = len(res.Problems) == 0
		out = append(out, res)
	}
	return out
}
