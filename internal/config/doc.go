// Package config is AfterConfig: overlays that change a vendor's
// configuration file without ever editing it. An overlay lists changes by
// dot path, each optionally guarded by the value it expects to find, and the
// result is written to a separate file.
package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/aftermarket-dev/aftermarket/internal/ojson"
	"github.com/aftermarket-dev/aftermarket/internal/rules"
	"github.com/aftermarket-dev/aftermarket/internal/yamlite"
)

// Formats AfterConfig reads and writes.
const (
	JSON = "json"
	YAML = "yaml"
	Env  = "env"
)

// FormatOf guesses a format from a file name: .json, .yaml, .yml, and .env
// or names starting with .env (such as .env.production).
func FormatOf(name string) string {
	base := strings.ToLower(filepath.Base(name))
	switch {
	case strings.HasSuffix(base, ".json"):
		return JSON
	case strings.HasSuffix(base, ".yaml"), strings.HasSuffix(base, ".yml"):
		return YAML
	case strings.HasSuffix(base, ".env"), base == ".env", strings.HasPrefix(base, ".env."):
		return Env
	}
	return ""
}

// document is a parsed configuration file. Values are ojson values:
// *ojson.Object, []any, string, json.Number, bool or nil.
type document interface {
	root() any
	set(p ojson.Path, v any) error
	del(p ojson.Path) error
	rename(from, to ojson.Path) error
	copy(from, to ojson.Path) error
	encode() ([]byte, error)
}

// parse reads data in the given format.
func parse(format string, data []byte) (document, error) {
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("the file is not valid UTF-8")
	}
	crlf := bytes.Contains(data, []byte("\r\n"))
	switch format {
	case JSON:
		v, err := ojson.Decode(data)
		if err != nil {
			return nil, fmt.Errorf("not valid JSON: %v", err)
		}
		if depth(v) > maxDepth {
			return nil, fmt.Errorf("the file is nested more than %d levels deep", maxDepth)
		}
		return &treeDoc{format: JSON, v: v, indent: jsonIndent(data), newline: bytes.HasSuffix(data, []byte("\n")), crlf: crlf}, nil
	case YAML:
		n, err := yamlite.Parse(data)
		if err != nil {
			return nil, fmt.Errorf("can't read this YAML: %v", err)
		}
		if n.Kind != yamlite.Map && n.Kind != yamlite.Seq {
			return nil, fmt.Errorf("the YAML file must hold a mapping or a list")
		}
		d := &treeDoc{format: YAML, keyStyle: map[string]int{}, crlf: crlf}
		d.v = yamlTree(n, d.keyStyle)
		if depth(d.v) > maxDepth {
			return nil, fmt.Errorf("the file is nested more than %d levels deep", maxDepth)
		}
		return d, nil
	case Env:
		return parseEnv(data)
	}
	return nil, fmt.Errorf("unknown format %q (use json, yaml or env)", format)
}

// maxDepth bounds nesting, so hostile input can't make a diff crawl.
const maxDepth = 200

func depth(v any) int {
	d := 0
	switch t := v.(type) {
	case *ojson.Object:
		for _, k := range t.Keys() {
			c, _ := t.Get(k)
			d = max(d, depth(c))
		}
		return d + 1
	case []any:
		for _, it := range t {
			d = max(d, depth(it))
		}
		return d + 1
	}
	return 0
}

// Key styles seen in a YAML file.
const (
	keyPlain  = 1
	keyQuoted = 2
)

// treeDoc is a JSON or YAML document.
type treeDoc struct {
	format   string
	v        any
	indent   string         // JSON: one level of indentation, "" for compact
	newline  bool           // JSON: the file ended with a newline
	crlf     bool           // lines ended with \r\n
	keyStyle map[string]int // YAML: how each key was written, plain, quoted or both
}

func (d *treeDoc) root() any { return d.v }

func (d *treeDoc) set(p ojson.Path, v any) error {
	_, err := ojson.Set(d.v, p, v)
	if err == ojson.ErrPath {
		return fmt.Errorf("%s runs through a value that is not an object", p)
	}
	return err
}

func (d *treeDoc) del(p ojson.Path) error {
	if list, ok := d.v.([]any); ok && len(p) == 1 {
		i, err := strconv.Atoi(p[0])
		if err != nil || i < 0 || i >= len(list) || strconv.Itoa(i) != p[0] {
			return fmt.Errorf("%s not found", p)
		}
		d.v = append(list[:i:i], list[i+1:]...)
		return nil
	}
	if !ojson.Delete(d.v, p) {
		return fmt.Errorf("%s not found", p)
	}
	return nil
}

func (d *treeDoc) rename(from, to ojson.Path) error {
	if within(to, from) || within(from, to) {
		return fmt.Errorf("can't rename a value into itself or into its own parent")
	}
	if _, ok := ojson.Get(d.v, to); ok {
		return fmt.Errorf("%s already exists", to)
	}
	_, err := ojson.Rename(d.v, from, to)
	return err
}

func (d *treeDoc) copy(from, to ojson.Path) error {
	v, ok := ojson.Get(d.v, from)
	if !ok {
		return fmt.Errorf("%s not found", from)
	}
	if _, ok := ojson.Get(d.v, to); ok {
		return fmt.Errorf("%s already exists", to)
	}
	return d.set(to, ojson.Clone(v))
}

// within reports whether p is a or lies inside a.
func within(p, a ojson.Path) bool {
	if len(p) < len(a) {
		return false
	}
	for i := range a {
		if p[i] != a[i] {
			return false
		}
	}
	return true
}

func (d *treeDoc) encode() ([]byte, error) {
	var b bytes.Buffer
	var err error
	if d.format == YAML {
		err = writeYAML(&b, d.v, d.keyStyle)
	} else {
		err = writeJSON(&b, d.v, d.indent, "")
		if err == nil && d.newline {
			b.WriteByte('\n')
		}
	}
	out := b.Bytes()
	if d.crlf {
		out = bytes.ReplaceAll(out, []byte("\n"), []byte("\r\n"))
	}
	return out, err
}

// jsonIndent finds one level of indentation in a JSON file, or "" when the
// file is on one line.
func jsonIndent(data []byte) string {
	for _, line := range strings.Split(string(data), "\n")[1:] {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed != "" && len(trimmed) < len(line) {
			return line[:len(line)-len(trimmed)]
		}
	}
	return ""
}

func writeJSON(b *bytes.Buffer, v any, indent, prefix string) error {
	if indent == "" {
		enc, err := ojson.Encode(v)
		b.Write(enc)
		return err
	}
	inner := prefix + indent
	switch t := v.(type) {
	case *ojson.Object:
		keys := t.Keys()
		if len(keys) == 0 {
			b.WriteString("{}")
			return nil
		}
		b.WriteString("{\n")
		for i, k := range keys {
			kb, _ := ojson.Encode(k)
			b.WriteString(inner)
			b.Write(kb)
			b.WriteString(": ")
			child, _ := t.Get(k)
			if err := writeJSON(b, child, indent, inner); err != nil {
				return err
			}
			if i < len(keys)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(prefix + "}")
	case []any:
		if len(t) == 0 {
			b.WriteString("[]")
			return nil
		}
		b.WriteString("[\n")
		for i, it := range t {
			b.WriteString(inner)
			if err := writeJSON(b, it, indent, inner); err != nil {
				return err
			}
			if i < len(t)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(prefix + "]")
	default:
		enc, err := ojson.Encode(v)
		if err != nil {
			return err
		}
		b.Write(enc)
	}
	return nil
}

// yamlPlain is a YAML scalar that was written without quotes and reads as
// text. It is written back exactly as it was, so a value the overlay doesn't
// touch means the same to any YAML reader, not only to this one: an
// unquoted yes stays an unquoted yes.
type yamlPlain string

// yamlTree converts parsed YAML into a value tree, keeping plain text
// scalars as yamlPlain.
func yamlTree(n *yamlite.Node, keyStyle map[string]int) any {
	switch n.Kind {
	case yamlite.Scalar:
		v := rules.NodeToJSON(n)
		if s, ok := v.(string); ok && !n.Quoted {
			return yamlPlain(s)
		}
		return v
	case yamlite.Seq:
		out := []any{}
		for _, it := range n.Items {
			out = append(out, yamlTree(it, keyStyle))
		}
		return out
	case yamlite.Map:
		o := ojson.NewObject()
		for _, p := range n.Pairs {
			if keyStyle != nil {
				if p.KeyQuoted {
					keyStyle[p.Key] |= keyQuoted
				} else {
					keyStyle[p.Key] |= keyPlain
				}
			}
			o.Set(p.Key, yamlTree(p.Value, keyStyle))
		}
		return o
	}
	return nil
}

// plain turns yamlPlain values back into strings, for JSON, .env, printing
// and comparing.
func plain(v any) any {
	switch t := v.(type) {
	case yamlPlain:
		return string(t)
	case *ojson.Object:
		o := ojson.NewObject()
		for _, k := range t.Keys() {
			c, _ := t.Get(k)
			o.Set(k, plain(c))
		}
		return o
	case []any:
		out := make([]any, len(t))
		for i, it := range t {
			out[i] = plain(it)
		}
		return out
	}
	return v
}

var (
	plainKey    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.\-/]*$`)
	plainString = regexp.MustCompile(`^[A-Za-z_/][A-Za-z0-9_ ./:@+\-]*$`)
	jsonNumber  = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)
)

// yamlWords would read back as something other than text if written
// without quotes, in this reader or in YAML 1.1 readers. Compared in lower
// case.
var yamlWords = map[string]bool{"true": true, "false": true, "null": true, "~": true,
	"yes": true, "no": true, "on": true, "off": true, "y": true, "n": true}

// yamlKey writes a key the way the file wrote it, so a reader that types
// keys (80: is a number to most) sees the same key. New keys are left plain
// only when no reader could take them for anything but text.
func yamlKey(k string, keyStyle map[string]int) string {
	switch keyStyle[k] {
	case keyPlain:
		return k
	case keyQuoted:
		return quote(k)
	}
	if plainKey.MatchString(k) && !yamlWords[strings.ToLower(k)] && !strings.ContainsAny(k[:1], "0123456789+-.") {
		return k
	}
	return quote(k)
}

func yamlScalar(v any) (string, error) {
	switch t := v.(type) {
	case nil:
		return "null", nil
	case bool:
		return strconv.FormatBool(t), nil
	case json.Number:
		if !jsonNumber.MatchString(string(t)) {
			return "", fmt.Errorf("invalid number %q", string(t))
		}
		return string(t), nil
	case yamlPlain:
		return string(t), nil
	case string:
		if plainString.MatchString(t) && !strings.HasSuffix(t, " ") && !strings.ContainsAny(t, ":#") &&
			!yamlWords[strings.ToLower(t)] {
			return t, nil
		}
		return quote(t), nil
	}
	return "", fmt.Errorf("unsupported value of type %T", v)
}

func writeYAML(b *bytes.Buffer, v any, keyStyle map[string]int) error {
	switch t := v.(type) {
	case *ojson.Object:
		if len(t.Keys()) == 0 {
			b.WriteString("{}\n")
			return nil
		}
		return yamlMap(b, t, "", keyStyle)
	case []any:
		if len(t) == 0 {
			b.WriteString("[]\n")
			return nil
		}
		return yamlSeq(b, t, "", keyStyle)
	}
	return fmt.Errorf("a YAML file must hold a mapping or a list")
}

// yamlValue writes what follows "key:" or "- ".
func yamlValue(b *bytes.Buffer, v any, indent string, keyStyle map[string]int) error {
	switch t := v.(type) {
	case *ojson.Object:
		if len(t.Keys()) == 0 {
			b.WriteString(" {}\n")
			return nil
		}
		b.WriteByte('\n')
		return yamlMap(b, t, indent+"  ", keyStyle)
	case []any:
		if len(t) == 0 {
			b.WriteString(" []\n")
			return nil
		}
		b.WriteByte('\n')
		return yamlSeq(b, t, indent+"  ", keyStyle)
	}
	s, err := yamlScalar(v)
	if err != nil {
		return err
	}
	b.WriteString(" " + s + "\n")
	return nil
}

func yamlMap(b *bytes.Buffer, o *ojson.Object, indent string, keyStyle map[string]int) error {
	for _, k := range o.Keys() {
		child, _ := o.Get(k)
		b.WriteString(indent + yamlKey(k, keyStyle) + ":")
		if err := yamlValue(b, child, indent, keyStyle); err != nil {
			return err
		}
	}
	return nil
}

func yamlSeq(b *bytes.Buffer, items []any, indent string, keyStyle map[string]int) error {
	for _, it := range items {
		b.WriteString(indent + "-")
		if err := yamlValue(b, it, indent, keyStyle); err != nil {
			return err
		}
	}
	return nil
}

// quote writes s as a YAML double-quoted scalar that yamlite reads back
// exactly.
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\r':
			b.WriteString(`\r`)
		case r < 0x20 || r == 0x7f || r == 0x2028 || r == 0x2029 || r == 0xfeff:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
