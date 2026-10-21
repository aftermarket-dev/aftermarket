package config

import (
	"strings"

	"github.com/aftermarket-dev/aftermarket/internal/ojson"
)

// ParseTree reads a JSON, YAML, TOML or .env file into a value tree of ojson
// values, for other tools that compare configuration.
func ParseTree(format string, data []byte) (any, error) {
	d, err := parse(format, data)
	if err != nil {
		return nil, err
	}
	return plain(d.root()), nil
}

// Equal compares two value trees by meaning: numbers by value, objects
// without regard to key order.
func Equal(a, b any) bool { return equal(a, b) }

// SecretPath reports whether a value at p must never be printed, because a
// part of its path looks like a secret (password, token, api_key and so on).
func SecretPath(p ojson.Path) bool {
	for _, seg := range p {
		low := strings.ToLower(seg)
		for _, w := range secretWords {
			if strings.Contains(low, w) {
				return true
			}
		}
	}
	return false
}

// MaskURLs hides user names and passwords inside URLs in s.
func MaskURLs(s string) string { return userinfo.ReplaceAllString(s, "${1}REDACTED@") }
