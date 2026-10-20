package replay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
)

// maxDiffs caps the differences listed per case.
const maxDiffs = 20

// Pattern is a dot path to ignore; "*" matches any key or element.
type Pattern []string

// ParsePattern reads a pattern such as customer.updated_at or data.*.id.
func ParsePattern(s string) (Pattern, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("an ignore path can't be empty")
	}
	p := strings.Split(s, ".")
	for _, seg := range p {
		if seg == "" {
			return nil, fmt.Errorf("%q is not a dot path such as customer.updated_at or data.*.id", s)
		}
	}
	return p, nil
}

func (p Pattern) matches(path []string) bool {
	if len(p) != len(path) {
		return false
	}
	for i := range p {
		if p[i] != "*" && p[i] != path[i] {
			return false
		}
	}
	return true
}

// compareBodies compares two bodies. JSON is compared by meaning: key order,
// spacing and the way a number is written don't count. Anything else is
// compared byte for byte.
func compareBodies(a, b []byte, ignore []Pattern) []string {
	av, aok := parseJSON(a)
	bv, bok := parseJSON(b)
	if aok && bok {
		d := &differ{ignore: ignore}
		d.walk(nil, av, bv)
		if d.more > 0 {
			d.out = append(d.out, fmt.Sprintf("and %d more", d.more))
		}
		return d.out
	}
	if bytes.Equal(a, b) {
		return nil
	}
	switch {
	case aok != bok && len(a) > 0 && len(b) > 0:
		which := "A"
		if bok {
			which = "B"
		}
		return []string{fmt.Sprintf("body: only %s is JSON", which)}
	}
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	return []string{fmt.Sprintf("body: %d bytes vs %d bytes, first difference at byte %d", len(a), len(b), i)}
}

func parseJSON(b []byte) (any, bool) {
	if len(bytes.TrimSpace(b)) == 0 {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return nil, false
	}
	if _, err := dec.Token(); err == nil {
		return nil, false // trailing data
	}
	return v, true
}

type differ struct {
	ignore []Pattern
	out    []string
	more   int
}

func (d *differ) add(path []string, format string, args ...any) {
	if len(d.out) >= maxDiffs {
		d.more++
		return
	}
	d.out = append(d.out, pathString(path)+": "+fmt.Sprintf(format, args...))
}

func (d *differ) ignored(path []string) bool {
	for _, p := range d.ignore {
		if p.matches(path) {
			return true
		}
	}
	return false
}

func (d *differ) walk(path []string, a, b any) {
	if d.ignored(path) {
		return
	}
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok {
			d.add(path, "%s vs %s", show(a), show(b))
			return
		}
		keys := map[string]bool{}
		for k := range x {
			keys[k] = true
		}
		for k := range y {
			keys[k] = true
		}
		sorted := make([]string, 0, len(keys))
		for k := range keys {
			sorted = append(sorted, k)
		}
		sort.Strings(sorted)
		for _, k := range sorted {
			p := append(append([]string(nil), path...), k)
			av, ina := x[k]
			bv, inb := y[k]
			switch {
			case d.ignored(p):
			case !inb:
				d.add(p, "only in A (%s)", show(av))
			case !ina:
				d.add(p, "only in B (%s)", show(bv))
			default:
				d.walk(p, av, bv)
			}
		}
	case []any:
		y, ok := b.([]any)
		if !ok {
			d.add(path, "%s vs %s", show(a), show(b))
			return
		}
		if len(x) != len(y) {
			d.add(path, "%d items vs %d items", len(x), len(y))
		}
		for i := 0; i < len(x) && i < len(y); i++ {
			d.walk(append(append([]string(nil), path...), strconv.Itoa(i)), x[i], y[i])
		}
	case json.Number:
		y, ok := b.(json.Number)
		if !ok || !sameNumber(x, y) {
			d.add(path, "%s vs %s", show(a), show(b))
		}
	default:
		if a != b {
			d.add(path, "%s vs %s", show(a), show(b))
		}
	}
}

func sameNumber(a, b json.Number) bool {
	if a == b {
		return true
	}
	x, _, err1 := big.ParseFloat(string(a), 10, 256, big.ToNearestEven)
	y, _, err2 := big.ParseFloat(string(b), 10, 256, big.ToNearestEven)
	return err1 == nil && err2 == nil && x.Cmp(y) == 0
}

func pathString(p []string) string {
	if len(p) == 0 {
		return "body"
	}
	return strings.Join(p, ".")
}

// show writes a value compactly for the report.
func show(v any) string {
	switch v.(type) {
	case map[string]any:
		return "an object"
	case []any:
		return "a list"
	}
	b, _ := json.Marshal(v)
	s := string(b)
	if r := []rune(s); len(r) > 80 {
		s = string(r[:77]) + "..."
	}
	return s
}
