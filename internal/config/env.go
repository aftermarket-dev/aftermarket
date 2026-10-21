package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/aftermarket-dev/aftermarket/internal/ojson"
)

// envDoc is a .env file. Comments, blank lines and every line the overlay
// doesn't change are written back exactly as they were.
type envDoc struct {
	lines   []envLine
	newline bool // the file ended with a newline
	crlf    bool // lines end with \r\n
}

type envLine struct {
	raw    string // the line as read; "" for lines the overlay added
	key    string // "" for comments and blank lines
	value  string
	suffix string // what followed the value: spaces and a # comment
	export bool
	dirty  bool // rewrite from key, value and suffix
}

var envKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.\-]*$`)

func parseEnv(data []byte) (document, error) {
	text := string(data)
	d := &envDoc{newline: strings.HasSuffix(text, "\n"), crlf: strings.Contains(text, "\r\n")}
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return d, nil
	}
	seen := map[string]int{}
	for i, raw := range strings.Split(text, "\n") {
		l := envLine{raw: raw}
		s := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if s == "" || strings.HasPrefix(s, "#") {
			d.lines = append(d.lines, l)
			continue
		}
		if rest, ok := strings.CutPrefix(s, "export "); ok {
			l.export = true
			s = strings.TrimSpace(rest)
		}
		k, v, ok := strings.Cut(s, "=")
		k = strings.TrimSpace(k)
		if !ok || !envKey.MatchString(k) {
			return nil, fmt.Errorf("line %d: expected KEY=value", i+1)
		}
		val, suffix, err := envValue(strings.TrimLeft(v, " \t"))
		if err != nil {
			return nil, fmt.Errorf("line %d: %v", i+1, err)
		}
		if prev, dup := seen[k]; dup {
			return nil, fmt.Errorf("line %d: %s is already set on line %d; programs differ on which one wins, so AfterConfig won't guess", i+1, k, prev)
		}
		seen[k] = i + 1
		l.key, l.value, l.suffix = k, val, suffix
		d.lines = append(d.lines, l)
	}
	return d, nil
}

// envValue reads a value: "double quoted" with \n \t \" \\ escapes, 'single
// quoted' as is, or bare up to a " #" comment. It also returns what follows
// the value, so a rewritten line keeps its comment.
func envValue(s string) (string, string, error) {
	s = strings.TrimRight(s, " \t")
	switch {
	case strings.HasPrefix(s, `"`):
		var b strings.Builder
		for i := 1; i < len(s); i++ {
			c := s[i]
			if c == '\\' && i+1 < len(s) {
				i++
				switch s[i] {
				case 'n':
					b.WriteByte('\n')
				case 't':
					b.WriteByte('\t')
				case 'r':
					b.WriteByte('\r')
				default:
					b.WriteByte(s[i])
				}
				continue
			}
			if c == '"' {
				if rest := strings.TrimSpace(s[i+1:]); rest != "" && !strings.HasPrefix(rest, "#") {
					return "", "", fmt.Errorf("unexpected text after the closing quote")
				}
				return b.String(), s[i+1:], nil
			}
			b.WriteByte(c)
		}
		return "", "", fmt.Errorf("unclosed double quote")
	case strings.HasPrefix(s, "'"):
		end := strings.Index(s[1:], "'")
		if end < 0 {
			return "", "", fmt.Errorf("unclosed single quote")
		}
		if rest := strings.TrimSpace(s[end+2:]); rest != "" && !strings.HasPrefix(rest, "#") {
			return "", "", fmt.Errorf("unexpected text after the closing quote")
		}
		return s[1 : end+1], s[end+2:], nil
	}
	if i := strings.Index(s, " #"); i >= 0 {
		v := strings.TrimRight(s[:i], " \t")
		return v, s[len(v):], nil
	}
	return s, "", nil
}

func (d *envDoc) root() any {
	o := ojson.NewObject()
	for _, l := range d.lines {
		if l.key != "" {
			o.Set(l.key, l.value)
		}
	}
	return o
}

func (d *envDoc) find(key string) int {
	for i, l := range d.lines {
		if l.key == key {
			return i
		}
	}
	return -1
}

func envPath(p ojson.Path) (string, error) {
	if len(p) != 1 {
		return "", fmt.Errorf("%s: .env files are flat, so a path is one key", p)
	}
	if !envKey.MatchString(p[0]) {
		return "", fmt.Errorf("%q is not a .env key", p[0])
	}
	return p[0], nil
}

// envText turns an overlay value into the text a .env file holds.
func envText(v any) (string, error) {
	switch t := v.(type) {
	case string:
		return t, nil
	case json.Number:
		return string(t), nil
	case bool:
		if t {
			return "true", nil
		}
		return "false", nil
	}
	return "", fmt.Errorf("a .env value must be a string, number or true/false")
}

func (d *envDoc) set(p ojson.Path, v any) error {
	k, err := envPath(p)
	if err != nil {
		return err
	}
	s, err := envText(v)
	if err != nil {
		return err
	}
	if i := d.find(k); i >= 0 {
		if d.lines[i].value != s {
			d.lines[i].value, d.lines[i].dirty = s, true
		}
		return nil
	}
	d.lines = append(d.lines, envLine{key: k, value: s, dirty: true})
	return nil
}

func (d *envDoc) del(p ojson.Path) error {
	k, err := envPath(p)
	if err != nil {
		return err
	}
	i := d.find(k)
	if i < 0 {
		return fmt.Errorf("%s not found", k)
	}
	d.lines = append(d.lines[:i], d.lines[i+1:]...)
	return nil
}

func (d *envDoc) rename(from, to ojson.Path) error {
	fk, err := envPath(from)
	if err != nil {
		return err
	}
	tk, err := envPath(to)
	if err != nil {
		return err
	}
	i := d.find(fk)
	if i < 0 {
		return fmt.Errorf("%s not found", fk)
	}
	if d.find(tk) >= 0 {
		return fmt.Errorf("%s already exists", tk)
	}
	if d.lines[i].dirty {
		d.lines[i].key = tk
		return nil
	}
	// Change only the key, so the value keeps its exact quoting and comment.
	raw := d.lines[i].raw
	at := strings.Index(raw, fk)
	d.lines[i].raw = raw[:at] + tk + raw[at+len(fk):]
	d.lines[i].key = tk
	return nil
}

func (d *envDoc) copy(from, to ojson.Path) error {
	fk, err := envPath(from)
	if err != nil {
		return err
	}
	tk, err := envPath(to)
	if err != nil {
		return err
	}
	i := d.find(fk)
	if i < 0 {
		return fmt.Errorf("%s not found", fk)
	}
	if d.find(tk) >= 0 {
		return fmt.Errorf("%s already exists", tk)
	}
	d.lines = append(d.lines, envLine{key: tk, value: d.lines[i].value, dirty: true})
	return nil
}

// envQuote writes a value so that dotenv readers and shells read it back as
// the same text: bare when it's simple, 'single quoted' when it has no ' or
// line break (nothing inside is expanded), and "double quoted" with \$
// escaped otherwise.
func envQuote(v string) string {
	if envBare.MatchString(v) {
		return v
	}
	if !strings.ContainsAny(v, "'\n\r") {
		return "'" + v + "'"
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range v {
		switch r {
		case '"', '\\', '$', '`':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '\r':
			b.WriteString(`\r`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

var envBare = regexp.MustCompile(`^[A-Za-z0-9_./:@,+\-]*$`)

func (d *envDoc) encode() ([]byte, error) {
	var b bytes.Buffer
	eol := "\n"
	if d.crlf {
		eol = "\r\n"
	}
	for i, l := range d.lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		if !l.dirty {
			b.WriteString(l.raw)
			continue
		}
		if l.export {
			b.WriteString("export ")
		}
		b.WriteString(l.key + "=" + envQuote(l.value) + l.suffix)
		if d.crlf {
			b.WriteByte('\r')
		}
	}
	// Keep the file's own ending, unless the overlay added the last line.
	if n := len(d.lines); n > 0 && (d.newline || d.lines[n-1].raw == "") {
		b.WriteString(eol[len(eol)-1:])
	}
	return b.Bytes(), nil
}
