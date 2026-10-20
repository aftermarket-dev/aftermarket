package record

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/aftermarket-dev/aftermarket/internal/ojson"
)

// redactHeaders replaces the values of listed headers. It returns the names
// it touched.
func redactHeaders(h http.Header, names map[string]bool) []string {
	var hit []string
	for k := range h {
		if names[k] {
			h[k] = []string{Redacted}
			hit = append(hit, k)
		}
	}
	return hit
}

// redactQuery replaces the values of listed query keys and keeps every other
// byte of the query string as it was.
func redactQuery(raw string, keys map[string]bool) (string, bool) {
	if raw == "" || len(keys) == 0 {
		return raw, false
	}
	parts := strings.Split(raw, "&")
	changed := false
	for i, part := range parts {
		k, _, _ := strings.Cut(part, "=")
		name, err := url.QueryUnescape(k)
		if err != nil {
			name = k
		}
		if keys[name] {
			parts[i] = k + "=" + Redacted
			changed = true
		}
	}
	return strings.Join(parts, "&"), changed
}

// redactJSON replaces every value at the given paths. It returns how many
// values it replaced.
func redactJSON(doc any, paths [][]string) (any, int) {
	n := 0
	for _, p := range paths {
		var hit int
		doc, hit = redactAt(doc, p)
		n += hit
	}
	return doc, n
}

func redactAt(v any, p []string) (any, int) {
	if len(p) == 0 {
		return Redacted, 1
	}
	seg, rest := p[0], p[1:]
	n := 0
	switch x := v.(type) {
	case *ojson.Object:
		for _, k := range x.Keys() {
			if seg != "*" && k != seg {
				continue
			}
			child, _ := x.Get(k)
			nv, hit := redactAt(child, rest)
			if hit > 0 {
				x.Set(k, nv)
				n += hit
			}
		}
	case []any:
		for i := range x {
			if seg != "*" && seg != strconv.Itoa(i) {
				continue
			}
			nv, hit := redactAt(x[i], rest)
			if hit > 0 {
				x[i] = nv
				n += hit
			}
		}
	}
	return v, n
}
