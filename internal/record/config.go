// Package record is AfterTest's recorder: a pass-through proxy that writes
// each finished exchange as a case in the fixture format `patch test`,
// `adapt test` and `replay` read. Traffic is never changed. Credentials and
// any values the config lists are replaced with REDACTED before anything is
// written.
package record

import (
	"fmt"
	"net/textproto"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aftermarket-dev/aftermarket/internal/rules"
	"github.com/aftermarket-dev/aftermarket/internal/yamlite"
)

// Redacted replaces every value that must not reach a fixture file.
const Redacted = "REDACTED"

// CredentialHeaders are always redacted.
var CredentialHeaders = []string{"Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie", "X-Api-Key"}

// Config is a loaded record config. The zero config from Default records
// everything and redacts credentials only.
type Config struct {
	When            rules.When
	RedactHeaders   map[string]bool // canonical names, credentials included
	RedactQuery     map[string]bool
	RedactJSON      [][]string // dot paths; "*" matches any key or element
	MaxBody         int64
	UpstreamTimeout time.Duration
	MaxCases        int // 0 means no limit
}

// Default returns the config used without a config file.
func Default() *Config {
	c := &Config{
		RedactHeaders:   map[string]bool{},
		RedactQuery:     map[string]bool{},
		MaxBody:         rules.DefaultMaxBody,
		UpstreamTimeout: rules.DefaultUpstreamTimeout,
	}
	for _, h := range CredentialHeaders {
		c.RedactHeaders[h] = true
	}
	return c
}

// LoadFile reads a record config file.
func LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Load(data)
}

var jsonSeg = regexp.MustCompile(`^[^.\s]+$`)

// Load parses and checks a record config. Every problem is reported, in line
// order.
func Load(data []byte) (*Config, error) {
	root, err := yamlite.Parse(data)
	if err != nil {
		return nil, rules.Errors{err}
	}
	if root.Kind != yamlite.Map {
		return nil, rules.Errors{&rules.LineError{Line: root.Line, Msg: "the record config must be a mapping"}}
	}
	cfg := Default()
	var errs rules.Errors
	errf := func(line int, format string, args ...any) {
		errs = append(errs, &rules.LineError{Line: line, Msg: fmt.Sprintf(format, args...)})
	}
	for _, p := range root.Pairs {
		switch p.Key {
		case "version":
			if !(p.Value.Kind == yamlite.Scalar && p.Value.Value == "1") {
				errf(p.KeyLine, "version must be 1")
			}
		case "when":
			w, e := rules.ParseWhen(p.Value)
			errs = append(errs, e...)
			cfg.When = w
		case "limits":
			errs = append(errs, rules.ParseLimits(p.Value, &cfg.MaxBody, &cfg.UpstreamTimeout)...)
		case "max_cases":
			n, err := strconv.Atoi(p.Value.Value)
			if p.Value.Kind != yamlite.Scalar || err != nil || n < 1 {
				errf(p.KeyLine, "max_cases must be a whole number of 1 or more")
			} else {
				cfg.MaxCases = n
			}
		case "redact":
			loadRedact(p.Value, cfg, errf)
		default:
			errf(p.KeyLine, "unknown key %q (use version, when, redact, limits or max_cases)", p.Key)
		}
	}
	if len(errs) > 0 {
		sort.SliceStable(errs, func(i, j int) bool { return line(errs[i]) < line(errs[j]) })
		return nil, errs
	}
	return cfg, nil
}

func loadRedact(n *yamlite.Node, cfg *Config, errf func(int, string, ...any)) {
	if n.Kind != yamlite.Map {
		errf(n.Line, "redact must be a mapping with headers, query or json lists")
		return
	}
	for _, p := range n.Pairs {
		if p.Key != "headers" && p.Key != "query" && p.Key != "json" {
			errf(p.KeyLine, "unknown key %q under redact (use headers, query or json)", p.Key)
			continue
		}
		list, ok := scalarList(p.Value)
		if !ok {
			errf(p.KeyLine, "redact.%s must be a list of names", p.Key)
			continue
		}
		for _, s := range list {
			switch p.Key {
			case "headers":
				name := textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(s))
				if !rules.ValidHeaderName(name) {
					errf(p.KeyLine, "%q is not a valid header name", s)
					continue
				}
				cfg.RedactHeaders[name] = true
			case "query":
				if s == "" {
					errf(p.KeyLine, "a query key can't be empty")
					continue
				}
				cfg.RedactQuery[s] = true
			case "json":
				segs := strings.Split(s, ".")
				bad := false
				for _, seg := range segs {
					if !jsonSeg.MatchString(seg) {
						bad = true
					}
				}
				if bad {
					errf(p.KeyLine, "%q is not a dot path such as customer.email or data.*.email", s)
					continue
				}
				cfg.RedactJSON = append(cfg.RedactJSON, segs)
			}
		}
	}
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

func line(e error) int {
	if le, ok := e.(*rules.LineError); ok {
		return le.Line
	}
	if ye, ok := e.(*yamlite.Error); ok {
		return ye.Line
	}
	return 0
}
