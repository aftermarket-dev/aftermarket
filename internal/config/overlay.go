package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/aftermarket-dev/aftermarket/internal/ojson"
	"github.com/aftermarket-dev/aftermarket/internal/rules"
	"github.com/aftermarket-dev/aftermarket/internal/yamlite"
)

// Overlay is a loaded overlay file.
type Overlay struct {
	Path    string // where the overlay was read from
	Base    string // the vendor file, resolved against the overlay's folder
	Format  string
	Redact  []ojson.Path // extra paths whose values are never printed
	Changes []Change
}

// Change is one operation.
type Change struct {
	Line         int
	Op           string // set, delete, rename or copy
	Path         ojson.Path
	To           ojson.Path // rename and copy
	Value        any        // set
	Expect       any
	HasExpect    bool
	ExpectAbsent bool
}

// Target is the path a guard looks at: the source of a rename or copy.
func (c Change) Target() ojson.Path { return c.Path }

// Describe writes the change for people, with values redacted where needed.
func (c Change) Describe(redact func(ojson.Path) bool) string {
	switch c.Op {
	case "set":
		return fmt.Sprintf("set %s to %s", c.Path, show(c.Path, c.Value, redact))
	case "delete":
		return "delete " + c.Path.String()
	}
	return fmt.Sprintf("%s %s to %s", c.Op, c.Path, c.To)
}

// LoadFile reads an overlay. Every problem is reported, in line order.
func LoadFile(path string) (*Overlay, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	o, err := Load(data)
	if err != nil {
		return nil, err
	}
	o.Path = path
	if o.Base != "" && !filepath.IsAbs(o.Base) {
		o.Base = filepath.Join(filepath.Dir(path), o.Base)
	}
	return o, nil
}

// Load parses overlay contents. Base stays as written.
func Load(data []byte) (*Overlay, error) {
	root, err := yamlite.Parse(data)
	if err != nil {
		return nil, rules.Errors{err}
	}
	if root.Kind != yamlite.Map {
		return nil, rules.Errors{&rules.LineError{Line: root.Line, Msg: "an overlay must be a mapping with base: and changes:"}}
	}
	o := &Overlay{}
	var errs rules.Errors
	errf := func(line int, format string, args ...any) {
		errs = append(errs, &rules.LineError{Line: line, Msg: fmt.Sprintf(format, args...)})
	}
	str := func(p yamlite.Pair) (string, bool) {
		if p.Value.Kind != yamlite.Scalar || p.Value.Value == "" {
			errf(p.KeyLine, "%s must be a single value", p.Key)
			return "", false
		}
		return p.Value.Value, true
	}
	var changes *yamlite.Node
	changesLine := root.Line
	baseSeen := false
	for _, p := range root.Pairs {
		switch p.Key {
		case "version":
			if !(p.Value.Kind == yamlite.Scalar && p.Value.Value == "1") {
				errf(p.KeyLine, "version must be 1")
			}
		case "base":
			baseSeen = true
			o.Base, _ = str(p)
		case "format":
			if f, ok := str(p); ok {
				f = strings.ToLower(f)
				if f != JSON && f != YAML && f != Env && f != TOML {
					errf(p.KeyLine, "format must be json, yaml, toml or env")
				}
				o.Format = f
			}
		case "redact":
			list, ok := scalarList(p.Value)
			if !ok {
				errf(p.KeyLine, "redact must be a list of dot paths")
			}
			for _, s := range list {
				path, err := ojson.ParsePath(s)
				if err != nil {
					errf(p.KeyLine, "redact: %v", err)
					continue
				}
				o.Redact = append(o.Redact, path)
			}
		case "changes":
			changes, changesLine = p.Value, p.KeyLine
		default:
			errf(p.KeyLine, "unknown key %q (use version, base, format, redact or changes)", p.Key)
		}
	}
	if o.Base == "" && !baseSeen {
		errf(root.Line, "base: is required; it names the vendor's file")
	}
	if o.Format == "" && o.Base != "" {
		if o.Format = FormatOf(o.Base); o.Format == "" {
			errf(root.Line, "can't tell the format of %q from its name; add format: json, yaml, toml or env", o.Base)
		}
	}
	switch {
	case changes == nil || changes.Kind == yamlite.Null:
		errf(changesLine, "changes: needs at least one change")
	case changes.Kind != yamlite.Seq:
		errf(changes.Line, "changes must be a list")
	default:
		for _, n := range changes.Items {
			if c, ok := loadChange(n, o.Format, errf); ok {
				o.Changes = append(o.Changes, c)
			}
		}
	}
	if len(errs) > 0 {
		sort.SliceStable(errs, func(i, j int) bool { return lineOf(errs[i]) < lineOf(errs[j]) })
		return nil, errs
	}
	return o, nil
}

func lineOf(e error) int {
	switch t := e.(type) {
	case *rules.LineError:
		return t.Line
	case *yamlite.Error:
		return t.Line
	}
	return 0
}

func scalarList(n *yamlite.Node) ([]string, bool) {
	switch n.Kind {
	case yamlite.Null:
		return nil, true
	case yamlite.Scalar:
		return []string{n.Value}, true
	case yamlite.Seq:
		var out []string
		for _, it := range n.Items {
			if it.Kind != yamlite.Scalar {
				return nil, false
			}
			out = append(out, it.Value)
		}
		return out, true
	}
	return nil, false
}

func loadChange(n *yamlite.Node, format string, errf func(int, string, ...any)) (Change, bool) {
	c := Change{Line: n.Line}
	if n.Kind != yamlite.Map || len(n.Pairs) != 1 {
		errf(n.Line, "each change is one of set, delete, rename or copy, such as - set: {path: server.timeout, value: 120}")
		return c, false
	}
	op := n.Pairs[0]
	c.Op = op.Key
	args := op.Value
	if args.Kind != yamlite.Map {
		errf(op.KeyLine, "%s needs a mapping, such as {path: a.b}", op.Key)
		return c, false
	}
	allowed := map[string][]string{
		"set":    {"path", "value", "expect", "expect_absent"},
		"delete": {"path", "expect"},
		"rename": {"from", "to", "expect"},
		"copy":   {"from", "to", "expect"},
	}[op.Key]
	if allowed == nil {
		errf(op.KeyLine, "unknown change %q (use set, delete, rename or copy)", op.Key)
		return c, false
	}
	ok := true
	seen := map[string]bool{}
	for _, p := range args.Pairs {
		seen[p.Key] = true
		known := false
		for _, a := range allowed {
			known = known || a == p.Key
		}
		if !known {
			errf(p.KeyLine, "%s doesn't take %q (it takes %s)", op.Key, p.Key, strings.Join(allowed, ", "))
			ok = false
		}
	}
	path := func(key string) ojson.Path {
		v := args.Get(key)
		if v == nil {
			errf(op.KeyLine, "%s needs %s", op.Key, key)
			ok = false
			return nil
		}
		if v.Kind != yamlite.Scalar {
			errf(v.Line, "%s must be a dot path", key)
			ok = false
			return nil
		}
		p, err := ojson.ParsePath(v.Value)
		if err != nil {
			errf(v.Line, "%s: %v", key, err)
			ok = false
			return nil
		}
		if format == Env && len(p) != 1 {
			errf(v.Line, "%s: a .env file is flat, so a path is one key, such as LOG_LEVEL", key)
			ok = false
		}
		return p
	}
	value := func(key string) any {
		// Plain YAML text in the overlay stays plain in a YAML output.
		v := yamlTree(args.Get(key), nil)
		if err := rules.CheckNumbers(plain(v)); err != nil {
			errf(args.Get(key).Line, "%s: %v", key, err)
			ok = false
		}
		if format == Env {
			if _, err := envText(plain(v)); err != nil {
				errf(args.Get(key).Line, "%s: %v", key, err)
				ok = false
			}
		}
		return v
	}
	switch op.Key {
	case "set", "delete":
		c.Path = path("path")
	default:
		c.Path, c.To = path("from"), path("to")
	}
	if op.Key == "set" {
		if !seen["value"] {
			errf(op.KeyLine, "set needs value")
			ok = false
		} else {
			c.Value = value("value")
		}
	}
	if seen["expect"] {
		c.HasExpect = true
		c.Expect = value("expect")
	}
	if v := args.Get("expect_absent"); v != nil {
		if v.Kind != yamlite.Scalar || (v.Value != "true" && v.Value != "false") {
			errf(v.Line, "expect_absent must be true or false")
			ok = false
		}
		c.ExpectAbsent = v.Value == "true"
		if c.ExpectAbsent && c.HasExpect {
			errf(v.Line, "a change can expect a value or expect_absent, not both")
			ok = false
		}
	}
	return c, ok
}

// Problem is a change that can't be applied to this base.
type Problem struct {
	Line int
	Msg  string
}

func (p Problem) String() string {
	if p.Line == 0 {
		return p.Msg
	}
	return fmt.Sprintf("line %d: %s", p.Line, p.Msg)
}

// Result is an overlay applied to a base in memory.
type Result struct {
	Format   string
	Base     any // the base as read
	Out      []byte
	Applied  []string // descriptions of changes that changed something
	InPlace  []string // changes whose result was already there
	Problems []Problem
	doc      document
	redact   func(ojson.Path) bool
}

// Apply runs the overlay on base data. Every change is tried, so all
// problems are reported at once; the output is only meaningful when there
// are none.
func (o *Overlay) Apply(data []byte) (*Result, error) {
	doc, err := parse(o.Format, data)
	if err != nil {
		return nil, err
	}
	r := &Result{Format: o.Format, doc: doc}
	orig, _ := parse(o.Format, data)
	r.Base = orig.root()
	// A secret moved or copied somewhere else stays a secret.
	var carried []ojson.Path
	byPath := o.Redactor()
	redact := func(p ojson.Path) bool {
		if byPath(p) {
			return true
		}
		for _, c := range carried {
			if within(p, c) {
				return true
			}
		}
		return false
	}
	r.redact = redact
	for _, c := range o.Changes {
		cur, exists := ojson.Get(doc.root(), c.Path)
		fail := func(format string, args ...any) {
			r.Problems = append(r.Problems, Problem{Line: c.Line, Msg: fmt.Sprintf(format, args...)})
		}
		switch {
		case c.ExpectAbsent && exists:
			fail("%s expects %s to be absent, but the base has %s", c.Op, c.Path, show(c.Path, cur, redact))
			continue
		case c.HasExpect && !exists:
			fail("%s expects %s to be %s, but the base doesn't have it", c.Op, c.Path, show(c.Path, c.Expect, redact))
			continue
		case c.HasExpect && !same(cur, c.Expect, o.Format):
			fail("%s expects %s to be %s, but the base has %s", c.Op, c.Path, show(c.Path, c.Expect, redact), show(c.Path, cur, redact))
			continue
		}
		before, _ := doc.encode()
		var err error
		switch c.Op {
		case "set":
			v := ojson.Clone(c.Value)
			if o.Format != YAML {
				v = plain(v)
			}
			err = doc.set(c.Path, v)
		case "delete":
			err = doc.del(c.Path)
		case "rename":
			err = doc.rename(c.Path, c.To)
		case "copy":
			err = doc.copy(c.Path, c.To)
		}
		if err != nil {
			fail("%s: %v", c.Describe(redact), err)
			continue
		}
		if (c.Op == "rename" || c.Op == "copy") && (redact(c.Path) || containsSecret(c.Path, cur, redact)) {
			carried = append(carried, c.To)
		}
		after, _ := doc.encode()
		if bytes.Equal(before, after) {
			r.InPlace = append(r.InPlace, c.Describe(redact))
		} else {
			r.Applied = append(r.Applied, c.Describe(redact))
		}
	}
	if len(r.Problems) > 0 {
		return r, nil
	}
	if len(r.Applied) == 0 {
		// Nothing changed, so the output is the vendor's file exactly.
		r.Out = append([]byte(nil), data...)
		return r, nil
	}
	if r.Out, err = doc.encode(); err != nil {
		return nil, err
	}
	// Read the output back: it must hold exactly what was meant, or it isn't
	// written at all.
	back, err := parse(o.Format, r.Out)
	if err != nil || !exact(o.Format, back.root(), doc.root()) {
		r.Out = nil
		r.Problems = append(r.Problems, Problem{Msg: "the result can't be written safely in this format, so nothing is written; please report this with the overlay"})
	}
	return r, nil
}

// Tree returns the result as a value tree.
func (r *Result) Tree() any { return r.doc.root() }

// same compares two values by meaning. In a .env file everything is text,
// so an expected 30 matches "30".
func same(a, b any, format string) bool {
	a, b = plain(a), plain(b)
	if format == Env {
		as, err1 := envText(a)
		bs, err2 := envText(b)
		return err1 == nil && err2 == nil && as == bs
	}
	return equal(a, b)
}

// exact compares a result with what was meant. TOML keeps its types apart
// (a date is not text, 30 is not 30.0); the other formats compare by meaning.
func exact(format string, a, b any) bool {
	if format == TOML {
		return tomlSame(a, b)
	}
	return equal(plain(a), plain(b))
}

func equal(a, b any) bool {
	if p, ok := a.(yamlPlain); ok {
		a = string(p)
	}
	if p, ok := b.(yamlPlain); ok {
		b = string(p)
	}
	if p, ok := a.(tomlRaw); ok {
		a = string(p)
	}
	if p, ok := b.(tomlRaw); ok {
		b = string(p)
	}
	switch x := a.(type) {
	case *ojson.Object:
		y, ok := b.(*ojson.Object)
		if !ok || len(x.Keys()) != len(y.Keys()) {
			return false
		}
		for _, k := range x.Keys() {
			av, _ := x.Get(k)
			bv, ok := y.Get(k)
			if !ok || !equal(av, bv) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !equal(x[i], y[i]) {
				return false
			}
		}
		return true
	}
	if an, ok := asNumber(a); ok {
		if bn, ok := asNumber(b); ok {
			return an.Cmp(bn) == 0
		}
	}
	return a == b
}

func asNumber(v any) (*big.Rat, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return nil, false
	}
	s := string(n)
	mant, exp := s, 0
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		e, err := strconv.Atoi(s[i+1:])
		if err != nil || e > 400 || e < -400 {
			return nil, false
		}
		mant, exp = s[:i], e
	}
	if len(mant) > 400 {
		return nil, false
	}
	r, ok := new(big.Rat).SetString(mant)
	if !ok {
		return nil, false
	}
	if exp != 0 {
		ten := new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(max(exp, -exp))), nil))
		if exp > 0 {
			r.Mul(r, ten)
		} else {
			r.Quo(r, ten)
		}
	}
	return r, true
}
