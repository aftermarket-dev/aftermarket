package config

import (
	"fmt"
	"strings"

	"github.com/aftermarket-dev/aftermarket/internal/ojson"
)

// secretWords mark a key whose value is never printed, in any letter case.
var secretWords = []string{"password", "passwd", "secret", "token", "credential", "apikey", "api_key", "api-key", "private_key", "privatekey", "access_key", "accesskey"}

// Redactor reports whether a value at a path must not be printed: any
// segment looks like a secret, or the overlay lists the path or a parent of
// it under redact.
func (o *Overlay) Redactor() func(ojson.Path) bool {
	return func(p ojson.Path) bool {
		for _, seg := range p {
			low := strings.ToLower(seg)
			for _, w := range secretWords {
				if strings.Contains(low, w) {
					return true
				}
			}
		}
		for _, r := range o.Redact {
			if len(r) <= len(p) && strings.Join(p[:len(r)], ".") == strings.Join(r, ".") {
				return true
			}
		}
		return false
	}
}

// show writes a value compactly, or REDACTED.
func show(p ojson.Path, v any, redact func(ojson.Path) bool) string {
	if redact != nil && redact(p) {
		return "REDACTED"
	}
	switch t := v.(type) {
	case *ojson.Object:
		if containsSecret(p, t, redact) {
			return "an object"
		}
	case []any:
		if containsSecret(p, t, redact) {
			return "a list"
		}
	}
	b, err := ojson.Encode(plain(v))
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	s := string(b)
	if r := []rune(s); len(r) > 80 {
		s = string(r[:77]) + "..."
	}
	return s
}

// containsSecret reports whether anything inside v would be redacted.
func containsSecret(p ojson.Path, v any, redact func(ojson.Path) bool) bool {
	if redact == nil {
		return false
	}
	switch t := v.(type) {
	case *ojson.Object:
		for _, k := range t.Keys() {
			cp := append(append(ojson.Path(nil), p...), k)
			child, _ := t.Get(k)
			if redact(cp) || containsSecret(cp, child, redact) {
				return true
			}
		}
	case []any:
		for i, it := range t {
			cp := append(append(ojson.Path(nil), p...), fmt.Sprint(i))
			if redact(cp) || containsSecret(cp, it, redact) {
				return true
			}
		}
	}
	return false
}

// Diff lists what changed between the base and the result, one line per
// field: "~ a.b: 1 -> 2", "+ a.c: true", "- a.d".
func (o *Overlay) Diff(r *Result) []string {
	var out []string
	walkDiff(nil, r.Base, r.Tree(), o.Redactor(), &out)
	return out
}

func walkDiff(p ojson.Path, a, b any, redact func(ojson.Path) bool, out *[]string) {
	ao, aok := a.(*ojson.Object)
	bo, bok := b.(*ojson.Object)
	if aok && bok {
		for _, k := range ao.Keys() {
			cp := append(append(ojson.Path(nil), p...), k)
			av, _ := ao.Get(k)
			if bv, ok := bo.Get(k); ok {
				walkDiff(cp, av, bv, redact, out)
			} else {
				*out = append(*out, "- "+cp.String())
			}
		}
		for _, k := range bo.Keys() {
			if _, ok := ao.Get(k); !ok {
				cp := append(append(ojson.Path(nil), p...), k)
				bv, _ := bo.Get(k)
				*out = append(*out, fmt.Sprintf("+ %s: %s", cp, show(cp, bv, redact)))
			}
		}
		return
	}
	if equal(a, b) {
		return
	}
	name := p.String()
	if name == "" {
		name = "(whole file)"
	}
	*out = append(*out, fmt.Sprintf("~ %s: %s -> %s", name, show(p, a, redact), show(p, b, redact)))
}
