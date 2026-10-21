package hook

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/aftermarket-dev/aftermarket/internal/yamlite"
)

// Fixture holds before/after cases for a config's sources.
type Fixture struct {
	Cases []FixtureCase
}

// FixtureCase says what a source's content changes from and to, and which
// changes the event must list, or that there is no event.
type FixtureCase struct {
	Name       string
	Line       int
	Source     string
	Before     string
	After      string
	Expect     []string
	ExpectNone bool
}

// LoadFixture reads a hook fixture file.
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
	f := &Fixture{}
	for i, n := range cs.Items {
		c := FixtureCase{Name: fmt.Sprintf("case %d", i+1), Line: n.Line}
		if n.Kind != yamlite.Map {
			return nil, fmt.Errorf("line %d: each case must be a mapping", n.Line)
		}
		hasExpect := false
		for _, p := range n.Pairs {
			v := p.Value
			text := func() (string, error) {
				if v.Kind == yamlite.Null {
					return "", nil
				}
				if v.Kind != yamlite.Scalar {
					return "", fmt.Errorf("line %d: %s must be text (use | for a block)", p.KeyLine, p.Key)
				}
				return v.Value, nil
			}
			var err error
			switch p.Key {
			case "name":
				c.Name, err = text()
			case "source":
				c.Source, err = text()
			case "before":
				c.Before, err = text()
			case "after":
				c.After, err = text()
			case "expect":
				hasExpect = true
				if v.Kind != yamlite.Seq {
					return nil, fmt.Errorf("line %d: expect must be a list of changes, such as - 'changed status: \"open\" -> \"paid\"'", p.KeyLine)
				}
				for _, it := range v.Items {
					if it.Kind != yamlite.Scalar {
						return nil, fmt.Errorf("line %d: each expected change is one line of text", it.Line)
					}
					c.Expect = append(c.Expect, it.Value)
				}
			case "expect_none":
				c.ExpectNone = v.Kind == yamlite.Scalar && v.Value == "true"
			default:
				return nil, fmt.Errorf("line %d: unknown key %q in a case (use name, source, before, after, expect or expect_none)", p.KeyLine, p.Key)
			}
			if err != nil {
				return nil, err
			}
		}
		if c.Source == "" {
			return nil, fmt.Errorf("line %d: case %q needs source", n.Line, c.Name)
		}
		if hasExpect == c.ExpectNone {
			return nil, fmt.Errorf("line %d: case %q needs exactly one of expect or expect_none: true", n.Line, c.Name)
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

// Describe writes a change as one line, the form fixtures use:
// `changed status: "open" -> "paid"`, `added items.2: {...}`,
// `removed report.csv`.
func Describe(c Change) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return string(b)
	}
	path := c.Path
	if path == "" {
		path = "(the whole source)"
	}
	switch c.Op {
	case "changed":
		return fmt.Sprintf("changed %s: %s -> %s", path, enc(c.Old), enc(c.New))
	case "added":
		return fmt.Sprintf("added %s: %s", path, enc(c.New))
	}
	return fmt.Sprintf("removed %s: %s", path, enc(c.Old))
}

// RunFixture runs every case against the config's sources. Folder sources
// can't be described by before and after text.
func (c *Config) RunFixture(f *Fixture) []CaseResult {
	var out []CaseResult
	for _, fc := range f.Cases {
		res := CaseResult{Name: fc.Name}
		var src *Source
		for _, s := range c.Sources {
			if s.ID == fc.Source {
				src = s
			}
		}
		switch {
		case src == nil:
			res.Problems = append(res.Problems, fmt.Sprintf("no source %q in the config", fc.Source))
		case src.Kind == "folder":
			res.Problems = append(res.Problems, "folder sources can't be tested with before and after text")
		default:
			snap := func(text string) Snapshot {
				s := Snapshot{Exists: true, Hash: hashOf([]byte(text)), Size: int64(len(text)), Content: text}
				if src.Kind == "http" {
					s.Status = 200
				}
				return s
			}
			var got []string
			for _, ch := range diff(src, snap(fc.Before), snap(fc.After)) {
				got = append(got, Describe(ch))
			}
			switch {
			case fc.ExpectNone && len(got) > 0:
				res.Problems = append(res.Problems, "want no event, got: "+strings.Join(got, "; "))
			case !fc.ExpectNone && strings.Join(got, "\n") != strings.Join(fc.Expect, "\n"):
				res.Problems = append(res.Problems, fmt.Sprintf("got:\n        %s\n      want:\n        %s", strings.Join(got, "\n        "), strings.Join(fc.Expect, "\n        ")))
			}
		}
		res.Pass = len(res.Problems) == 0
		out = append(out, res)
	}
	return out
}
