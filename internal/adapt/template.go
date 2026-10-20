package adapt

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/aftermarket-dev/aftermarket/internal/ojson"
	"github.com/aftermarket-dev/aftermarket/internal/rules"
	"github.com/aftermarket-dev/aftermarket/internal/yamlite"
)

// A Template builds a JSON value. It is written in YAML: plain values are
// copied as they are, and strings starting with $ take values from
// elsewhere:
//
//	$              the whole source body
//	$.a.b.0        a value from the source body, by dot path
//	$params.name   a parameter captured from the old URL (always a string)
//	$$text         the literal string "$text"
//
// A mapping with exactly the keys $each and $item builds a list: one $item
// per element of the list $each points at, with $ inside $item meaning the
// element.
type Template struct {
	kind  tkind
	lit   any
	ref   ref
	keys  []string
	vals  []*Template
	items []*Template
	each  ref
	item  *Template
}

type tkind int

const (
	tLiteral tkind = iota
	tRef
	tObject
	tArray
	tEach
)

type ref struct {
	param string     // $params.NAME
	path  ojson.Path // $.a.b; nil with whole for $
	whole bool
	text  string // as written, for messages
}

func (r ref) isBody() bool { return r.param == "" }

// compileTemplate turns a YAML node into a template. params lists the
// parameters the old URL captures.
func compileTemplate(n *yamlite.Node, params map[string]bool) (*Template, error) {
	switch n.Kind {
	case yamlite.Null:
		return &Template{kind: tLiteral, lit: nil}, nil
	case yamlite.Scalar:
		if strings.HasPrefix(n.Value, "$") {
			if strings.HasPrefix(n.Value, "$$") {
				return &Template{kind: tLiteral, lit: n.Value[1:]}, nil
			}
			r, err := parseRef(n.Value, params)
			if err != nil {
				return nil, &rules.LineError{Line: n.Line, Msg: err.Error()}
			}
			return &Template{kind: tRef, ref: r}, nil
		}
		return &Template{kind: tLiteral, lit: rules.NodeToJSON(n)}, nil
	case yamlite.Seq:
		t := &Template{kind: tArray}
		for _, it := range n.Items {
			c, err := compileTemplate(it, params)
			if err != nil {
				return nil, err
			}
			t.items = append(t.items, c)
		}
		return t, nil
	case yamlite.Map:
		if n.Get("$each") != nil || n.Get("$item") != nil {
			return compileEach(n, params)
		}
		t := &Template{kind: tObject}
		for _, p := range n.Pairs {
			key := p.Key
			if strings.HasPrefix(key, "$") {
				if !strings.HasPrefix(key, "$$") {
					return nil, &rules.LineError{Line: p.KeyLine, Msg: fmt.Sprintf("key %q starts with $; write $$ for a literal $, or use $each with $item to build a list", key)}
				}
				key = key[1:]
			}
			c, err := compileTemplate(p.Value, params)
			if err != nil {
				return nil, err
			}
			t.keys = append(t.keys, key)
			t.vals = append(t.vals, c)
		}
		return t, nil
	}
	return nil, &rules.LineError{Line: n.Line, Msg: "unsupported template value"}
}

func compileEach(n *yamlite.Node, params map[string]bool) (*Template, error) {
	if len(n.Pairs) != 2 || n.Get("$each") == nil || n.Get("$item") == nil {
		return nil, &rules.LineError{Line: n.Line, Msg: "a list template needs exactly two keys, $each and $item"}
	}
	src := n.Get("$each")
	if src.Kind != yamlite.Scalar {
		return nil, &rules.LineError{Line: src.Line, Msg: "$each must point at a list, such as $.items"}
	}
	r, err := parseRef(src.Value, params)
	if err != nil {
		return nil, &rules.LineError{Line: src.Line, Msg: err.Error()}
	}
	if !r.isBody() {
		return nil, &rules.LineError{Line: src.Line, Msg: "$each must point at a list in the body, such as $.items"}
	}
	item, err := compileTemplate(n.Get("$item"), params)
	if err != nil {
		return nil, err
	}
	return &Template{kind: tEach, each: r, item: item}, nil
}

func parseRef(s string, params map[string]bool) (ref, error) {
	switch {
	case s == "$":
		return ref{whole: true, text: s}, nil
	case strings.HasPrefix(s, "$params."):
		name := strings.TrimPrefix(s, "$params.")
		if !params[name] {
			var known []string
			for k := range params {
				known = append(known, k)
			}
			sort.Strings(known)
			if len(known) == 0 {
				return ref{}, fmt.Errorf("%s: the old URL captures no parameters", s)
			}
			return ref{}, fmt.Errorf("%s: the old URL captures no parameter called %q (it has %s)", s, name, strings.Join(known, ", "))
		}
		return ref{param: name, text: s}, nil
	case strings.HasPrefix(s, "$."):
		p, err := ojson.ParsePath(strings.TrimPrefix(s, "$."))
		if err != nil {
			return ref{}, fmt.Errorf("%s: %v", s, err)
		}
		return ref{path: p, text: s}, nil
	}
	return ref{}, fmt.Errorf("%s is not a reference (use $, $.path or $params.name; write $$ for a literal $)", s)
}

// usesBody reports whether the template reads the source body.
func (t *Template) usesBody() bool {
	switch t.kind {
	case tRef:
		return t.ref.isBody()
	case tEach:
		return true
	case tObject:
		for _, v := range t.vals {
			if v.usesBody() {
				return true
			}
		}
	case tArray:
		for _, v := range t.items {
			if v.usesBody() {
				return true
			}
		}
	}
	return false
}

// errNoSource is returned when a template cannot be built at all.
var errNoSource = errors.New("no source")

// evalState collects the fields left out because their source was missing.
type evalState struct {
	params  map[string]string
	missing []string
}

// eval builds the value. ok is false when this value's source is missing.
// A non-nil error means the whole template cannot be built.
func (t *Template) eval(doc any, st *evalState, at string) (any, bool, error) {
	switch t.kind {
	case tLiteral:
		return ojson.Clone(t.lit), true, nil
	case tRef:
		if !t.ref.isBody() {
			return st.params[t.ref.param], true, nil
		}
		if t.ref.whole {
			return ojson.Clone(doc), true, nil
		}
		v, ok := ojson.Get(doc, t.ref.path)
		if !ok {
			st.missing = append(st.missing, t.ref.path.String()+" not found")
			return nil, false, nil
		}
		return ojson.Clone(v), true, nil
	case tObject:
		o := ojson.NewObject()
		for i, k := range t.keys {
			v, ok, err := t.vals[i].eval(doc, st, join(at, k))
			if err != nil {
				return nil, false, err
			}
			if ok {
				o.Set(k, v)
			}
		}
		return o, true, nil
	case tArray:
		out := []any{}
		for i, it := range t.items {
			v, ok, err := it.eval(doc, st, join(at, strconv.Itoa(i)))
			if err != nil {
				return nil, false, err
			}
			if !ok {
				v = nil
			}
			out = append(out, v)
		}
		return out, true, nil
	case tEach:
		var src any
		if t.each.whole {
			src = doc
		} else {
			v, ok := ojson.Get(doc, t.each.path)
			if !ok {
				return nil, false, fmt.Errorf("$each: %s not found", t.each.path)
			}
			src = v
		}
		list, ok := src.([]any)
		if !ok {
			return nil, false, fmt.Errorf("$each: %s is not a list", t.each.text)
		}
		out := make([]any, 0, len(list))
		for i, el := range list {
			v, ok, err := t.item.eval(el, st, join(at, strconv.Itoa(i)))
			if err != nil {
				return nil, false, err
			}
			if !ok {
				v = nil
			}
			out = append(out, v)
		}
		return out, true, nil
	}
	return nil, false, errNoSource
}

func join(a, b string) string {
	if a == "" {
		return b
	}
	return a + "." + b
}

// Build evaluates the template against doc. The result is nil with an error
// when it cannot be built; missing lists source fields that were absent and
// left out of the result.
func (t *Template) Build(doc any, params map[string]string) (any, []string, error) {
	st := &evalState{params: params}
	v, ok, err := t.eval(doc, st, "")
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return nil, nil, fmt.Errorf("the template's value (%s) is missing", strings.Join(st.missing, ", "))
	}
	return v, dedupe(st.missing), nil
}

// dedupe keeps the first few distinct entries, so a list of a thousand
// items with the same missing field logs one line.
func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	if len(out) > 20 {
		out = append(out[:20], fmt.Sprintf("and %d more", len(out)-20))
	}
	return out
}
