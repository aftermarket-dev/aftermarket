package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aftermarket-dev/aftermarket/internal/ojson"
)

// TOML is the fourth format AfterConfig reads and writes.
const TOML = "toml"

// tomlRaw is a TOML value AfterConfig keeps exactly as written: a date or
// time, inf or nan. Everywhere else it reads as text.
type tomlRaw string

// tomlDoc is a TOML file. Changes are made to the file's own lines, so
// comments, layout and every line the overlay doesn't touch stay as they
// were. When a change can't be made that way, or the edited text wouldn't
// read back as exactly the intended values, the file is written again from
// its values instead, without comments.
type tomlDoc struct {
	tree     *ojson.Object
	text     string
	stmts    []tomlStmt
	rewrite  bool // lines can no longer be followed; write from values
	crlf     bool
	newline  bool
	original string
}

const (
	stmtKV = iota
	stmtTable
	stmtArray // [[array of tables]]
)

// tomlStmt is one key/value pair or table header, with where it sits in
// the text, as byte offsets.
type tomlStmt struct {
	kind             int
	path             ojson.Path // absolute, with the index of an array-of-tables element
	textPath         ojson.Path // as written in a header, without indexes
	start, end       int        // the statement's first and last byte
	keyStart, keyEnd int        // the key, or a header's dotted name
	valStart, valEnd int        // a key's value
}

func parseTOMLDoc(data []byte) (document, error) {
	text := string(data)
	tree, stmts, err := parseTOML(text)
	if err != nil {
		return nil, err
	}
	if depth(tree) > maxDepth {
		return nil, fmt.Errorf("the file is nested more than %d levels deep", maxDepth)
	}
	return &tomlDoc{tree: tree, text: text, stmts: stmts, original: text,
		crlf: strings.Contains(text, "\r\n"), newline: strings.HasSuffix(text, "\n") || text == ""}, nil
}

func (d *tomlDoc) root() any { return d.tree }

func (d *tomlDoc) tdoc() *treeDoc { return &treeDoc{format: TOML, v: d.tree} }

// check refuses values TOML can't hold.
func tomlValueOK(v any) error {
	switch t := v.(type) {
	case nil:
		return fmt.Errorf("TOML has no null; delete the key instead")
	case *ojson.Object:
		for _, k := range t.Keys() {
			c, _ := t.Get(k)
			if err := tomlValueOK(c); err != nil {
				return err
			}
		}
	case []any:
		for _, c := range t {
			if err := tomlValueOK(c); err != nil {
				return err
			}
		}
	case json.Number:
		if _, err := tomlNumber(t); err != nil {
			return err
		}
	}
	return nil
}

func (d *tomlDoc) set(p ojson.Path, v any) error {
	if err := tomlValueOK(v); err != nil {
		return err
	}
	if cur, ok := ojson.Get(d.tree, p); ok {
		v = tomlKeepType(cur, v)
		if tomlSame(cur, v) {
			// Already there: the line keeps its spelling, comment and layout.
			return nil
		}
	}
	if err := d.tdoc().set(p, v); err != nil {
		return err
	}
	d.patch(func() bool { return d.patchSet(p) })
	return nil
}

// tomlKeepType keeps a value's TOML type when the overlay gives it in a
// looser form: a date replaced by text that is a valid date of the same
// kind stays a date, and a float set to a whole number stays a float.
func tomlKeepType(cur, v any) any {
	switch c := cur.(type) {
	case tomlRaw:
		if s, ok := v.(string); ok {
			if k := tomlDateKind(string(c)); k != "" && tomlDateKind(s) == k {
				return tomlRaw(s)
			}
		}
	case json.Number:
		if n, ok := v.(json.Number); ok && !tomlIsInt(c) && tomlIsInt(n) {
			return json.Number(string(n) + ".0")
		}
	}
	return v
}

func (d *tomlDoc) del(p ojson.Path) error {
	if err := d.tdoc().del(p); err != nil {
		return err
	}
	d.prune(p)
	d.patch(func() bool { return d.patchDel(p) })
	return nil
}

func (d *tomlDoc) rename(from, to ojson.Path) error {
	if err := d.tdoc().rename(from, to); err != nil {
		return err
	}
	d.prune(from)
	d.patch(func() bool {
		if d.patchRenameKey(from, to) || d.patchRenameTable(from, to) {
			return true
		}
		return d.patchSet(to) && d.reparse() && d.patchDel(from)
	})
	return nil
}

// prune removes tables left empty above p that only existed because keys
// inside them were written as dotted keys or under deeper headers. Their
// text is gone with those keys, so the tree drops them too.
func (d *tomlDoc) prune(p ojson.Path) {
	for n := len(p) - 1; n > 0; n-- {
		parent := p[:n]
		o, ok := ojson.Get(d.tree, parent)
		obj, isObj := o.(*ojson.Object)
		if !ok || !isObj || len(obj.Keys()) > 0 || d.covering(parent) != nil {
			return // inside a value written inline, it stays as written
		}
		if gp, _ := ojson.Get(d.tree, parent[:n-1]); n > 1 {
			if _, inList := gp.([]any); inList {
				return // an element of an array keeps its place
			}
		}
		for _, s := range d.stmts {
			if samePath(s.path, parent) {
				return // written in the file as a table of its own
			}
		}
		if d.tdoc().del(parent) != nil {
			return
		}
	}
}

func (d *tomlDoc) copy(from, to ojson.Path) error {
	if err := d.tdoc().copy(from, to); err != nil {
		return err
	}
	d.patch(func() bool { return d.patchSet(to) })
	return nil
}

// patch runs one text edit. If it can't be made, or the text stops reading
// as TOML, the document is written from its values from then on.
func (d *tomlDoc) patch(edit func() bool) {
	if d.rewrite {
		return
	}
	if !edit() || !d.reparse() {
		d.rewrite = true
	}
}

func (d *tomlDoc) reparse() bool {
	_, stmts, err := parseTOML(d.text)
	if err != nil {
		return false
	}
	d.stmts = stmts
	return true
}

func (d *tomlDoc) encode() ([]byte, error) {
	if !d.rewrite {
		if back, _, err := parseTOML(d.text); err == nil && tomlSame(back, d.tree) {
			return []byte(d.text), nil
		}
		d.rewrite = true
	}
	var b bytes.Buffer
	if err := writeTOMLTable(&b, d.tree, nil); err != nil {
		return nil, err
	}
	out := strings.TrimLeft(b.String(), "\n")
	if strings.HasPrefix(d.original, bom) {
		out = bom + out
	}
	if d.crlf {
		out = strings.ReplaceAll(out, "\n", "\r\n")
	}
	return []byte(out), nil
}

func (d *tomlDoc) eol() string {
	if d.crlf {
		return "\r\n"
	}
	return "\n"
}

// lineBegin and lineEnd give the start of the line holding offset i, and
// the start of the next line.
func (d *tomlDoc) lineBegin(i int) int { return strings.LastIndexByte(d.text[:i], '\n') + 1 }
func (d *tomlDoc) lineEnd(i int) int {
	if j := strings.IndexByte(d.text[i:], '\n'); j >= 0 {
		return i + j + 1
	}
	return len(d.text)
}

func (d *tomlDoc) find(kind int, p ojson.Path) *tomlStmt {
	for i := range d.stmts {
		s := &d.stmts[i]
		if s.kind == kind && samePath(s.path, p) {
			return s
		}
	}
	return nil
}

func samePath(a, b ojson.Path) bool { return len(a) == len(b) && within(a, b) }

// covering is the key/value pair whose value holds p inside it (an inline
// table or an array), if any.
func (d *tomlDoc) covering(p ojson.Path) *tomlStmt {
	for i := range d.stmts {
		s := &d.stmts[i]
		if s.kind == stmtKV && len(s.path) < len(p) && within(p, s.path) {
			return s
		}
	}
	return nil
}

func (d *tomlDoc) headers() []*tomlStmt {
	var out []*tomlStmt
	for i := range d.stmts {
		if d.stmts[i].kind != stmtKV {
			out = append(out, &d.stmts[i])
		}
	}
	return out
}

// section is the header that the line at offset i belongs to, or nil for
// the root table.
func (d *tomlDoc) section(i int) *tomlStmt {
	var cur *tomlStmt
	for _, h := range d.headers() {
		if h.start < i {
			cur = h
		}
	}
	return cur
}

func (d *tomlDoc) splice(from, to int, with string) {
	d.text = d.text[:from] + with + d.text[to:]
}

func (d *tomlDoc) replaceValue(s *tomlStmt, v any) bool {
	text, err := tomlInline(v)
	if err != nil {
		return false
	}
	d.splice(s.valStart, s.valEnd, text)
	return true
}

func (d *tomlDoc) patchSet(p ojson.Path) bool {
	v, ok := ojson.Get(d.tree, p)
	if !ok {
		return false
	}
	if s := d.find(stmtKV, p); s != nil {
		return d.replaceValue(s, v)
	}
	if s := d.covering(p); s != nil {
		cv, _ := ojson.Get(d.tree, s.path)
		return d.replaceValue(s, cv)
	}
	// A new key: it goes at the end of the deepest table section that holds
	// it, as a dotted key relative to that table. A path that is itself a
	// table, or holds tables written elsewhere, is left to a rewrite.
	var best *tomlStmt
	for _, h := range d.headers() {
		if within(p, h.path) && len(h.path) == len(p) {
			return false
		}
		if len(h.path) > len(p) && within(h.path, p) {
			return false
		}
		if len(h.path) < len(p) && within(p, h.path) && (best == nil || len(h.path) > len(best.path)) {
			best = h
		}
	}
	var base ojson.Path
	if best != nil {
		base = best.path
	}
	text, err := tomlInline(v)
	if err != nil {
		return false
	}
	line := tomlDotted(p[len(base):]) + " = " + text + d.eol()
	// After the last key/value of that section, or right after its header.
	at, indent, found := 0, "", false
	if best != nil {
		at = d.lineEnd(best.end)
	}
	for i := range d.stmts {
		s := &d.stmts[i]
		if s.kind == stmtKV && d.section(s.start) == best && !(best == nil && d.firstHeaderBefore(s.start)) {
			at = d.lineEnd(s.end)
			lb := d.lineBegin(s.start)
			indent = d.text[lb:s.start]
			found = true
		}
	}
	if best == nil && !found {
		// The root has no keys yet: the new key goes before the first table
		// and the comments that belong to it, below a comment that opens the
		// file.
		if hs := d.headers(); len(hs) > 0 {
			hb := d.lineBegin(hs[0].start)
			at = d.commentsAbove(hb, 0)
			if at == 0 {
				at = hb
			}
			line += d.eol()
		} else {
			at = len(d.text)
		}
	}
	if at == len(d.text) && at > 0 && !strings.HasSuffix(d.text, "\n") {
		// The file had no final newline; it still won't.
		line = d.eol() + strings.TrimSuffix(line, d.eol())
	}
	d.splice(at, at, indent+line)
	return true
}

func (d *tomlDoc) firstHeaderBefore(i int) bool {
	for _, h := range d.headers() {
		if h.start < i {
			return true
		}
	}
	return false
}

// commentsAbove returns where the run of comment lines directly above the
// line starting at i begins, going no further up than floor. With no such
// comments it returns i.
func (d *tomlDoc) commentsAbove(i, floor int) int {
	for i > floor {
		prev := d.lineBegin(i - 1)
		if prev < floor || !strings.HasPrefix(strings.TrimLeft(d.text[prev:i], " \t"), "#") {
			break
		}
		i = prev
	}
	return i
}

// cut removes the text from..to; when that reaches the end of a file with
// no final newline, the line breaks before it go too, so there still isn't
// one.
func (d *tomlDoc) cut(from, to int) {
	if to == len(d.text) && from > 0 && !strings.HasSuffix(d.text, "\n") {
		for from > 0 && (d.text[from-1] == '\n' || d.text[from-1] == '\r') {
			from--
		}
	}
	d.splice(from, to, "")
}

func (d *tomlDoc) patchDel(p ojson.Path) bool {
	if s := d.find(stmtKV, p); s != nil {
		d.cut(d.lineBegin(s.start), d.lineEnd(s.end))
		return true
	}
	if s := d.covering(p); s != nil {
		cv, _ := ojson.Get(d.tree, s.path)
		return d.replaceValue(s, cv)
	}
	// A table: its sections, and keys elsewhere that define parts of it.
	type span struct{ from, to int }
	var spans []span
	hs := d.headers()
	for i, h := range hs {
		if !within(h.path, p) {
			continue
		}
		from, to := d.lineBegin(h.start), len(d.text)
		if i+1 < len(hs) {
			// Comments directly above the next header belong to it.
			to = d.commentsAbove(d.lineBegin(hs[i+1].start), d.lineEnd(h.end))
		}
		spans = append(spans, span{from, to})
	}
	for i := range d.stmts {
		s := &d.stmts[i]
		if s.kind != stmtKV || !within(s.path, p) {
			continue
		}
		inside := false
		for _, sp := range spans {
			inside = inside || (s.start >= sp.from && s.start < sp.to)
		}
		if !inside {
			spans = append(spans, span{d.lineBegin(s.start), d.lineEnd(s.end)})
		}
	}
	if len(spans) == 0 {
		return false
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].from > spans[j].from })
	for _, sp := range spans {
		d.cut(sp.from, sp.to)
	}
	return true
}

// patchRenameKey renames a key in place when it stays in the same table
// section, so it keeps its line, its value as written and its comment.
func (d *tomlDoc) patchRenameKey(from, to ojson.Path) bool {
	s := d.find(stmtKV, from)
	if s == nil || d.covering(to) != nil {
		return false
	}
	sec := d.section(s.start)
	var base ojson.Path
	if sec != nil {
		base = sec.path
	}
	if !within(to, base) || len(to) <= len(base) {
		return false
	}
	for _, h := range d.headers() {
		if within(to, h.path) && len(h.path) > len(base) {
			return false // the new name belongs to another section
		}
	}
	d.splice(s.keyStart, s.keyEnd, tomlDotted(to[len(base):]))
	return true
}

// patchRenameTable renames a table written with headers by rewriting the
// headers, so its sections, keys and comments stay where they are.
func (d *tomlDoc) patchRenameTable(from, to ojson.Path) bool {
	for _, seg := range append(append(ojson.Path(nil), from...), to...) {
		if isDigits(seg) {
			return false // through an array of tables: left to the general way
		}
	}
	var hs []*tomlStmt
	for _, h := range d.headers() {
		if within(h.textPath, from) {
			hs = append(hs, h)
		}
	}
	if len(hs) == 0 {
		return false
	}
	for i := range d.stmts {
		s := &d.stmts[i]
		if s.kind == stmtKV && within(s.path, from) {
			sec := d.section(s.start)
			if sec == nil || !within(sec.textPath, from) {
				return false // part of the table is written as dotted keys elsewhere
			}
		}
	}
	// Last first, so earlier offsets stay right.
	for i := len(hs) - 1; i >= 0; i-- {
		h := hs[i]
		np := append(append(ojson.Path(nil), to...), h.textPath[len(from):]...)
		d.splice(h.keyStart, h.keyEnd, tomlDotted(np))
	}
	return true
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

var tomlBareKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// tomlDotted writes a key path, quoting segments that need it.
func tomlDotted(p ojson.Path) string {
	parts := make([]string, len(p))
	for i, seg := range p {
		if tomlBareKey.MatchString(seg) {
			parts[i] = seg
		} else {
			parts[i] = tomlString(seg)
		}
	}
	return strings.Join(parts, ".")
}

func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

var (
	tomlIntRe   = regexp.MustCompile(`^-?(0|[1-9][0-9]*)$`)
	tomlFloatRe = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?$`)
)

// tomlNumber checks that a number can be written in TOML: integers must fit
// in 64 bits.
func tomlNumber(n json.Number) (string, error) {
	s := string(n)
	if tomlIntRe.MatchString(s) {
		if _, err := strconv.ParseInt(s, 10, 64); err != nil {
			return "", fmt.Errorf("an integer too large for TOML (64 bits at most)")
		}
		return s, nil
	}
	if tomlFloatRe.MatchString(s) {
		if f, err := strconv.ParseFloat(s, 64); err != nil && math.IsInf(f, 0) {
			return "", fmt.Errorf("a number too large for a TOML float")
		}
		return s, nil
	}
	return "", fmt.Errorf("a number TOML can't hold")
}

// tomlInline writes a value on one line: inline tables and arrays.
func tomlInline(v any) (string, error) {
	switch t := v.(type) {
	case string:
		return tomlString(t), nil
	case yamlPlain:
		return tomlString(string(t)), nil
	case tomlRaw:
		return string(t), nil
	case json.Number:
		return tomlNumber(t)
	case bool:
		return strconv.FormatBool(t), nil
	case nil:
		return "", fmt.Errorf("TOML has no null")
	case []any:
		parts := make([]string, len(t))
		for i, it := range t {
			s, err := tomlInline(it)
			if err != nil {
				return "", err
			}
			parts[i] = s
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	case *ojson.Object:
		var parts []string
		for _, k := range t.Keys() {
			c, _ := t.Get(k)
			s, err := tomlInline(c)
			if err != nil {
				return "", err
			}
			parts = append(parts, tomlDotted(ojson.Path{k})+" = "+s)
		}
		if len(parts) == 0 {
			return "{}", nil
		}
		return "{ " + strings.Join(parts, ", ") + " }", nil
	}
	return "", fmt.Errorf("a value of type %T can't be written in TOML", v)
}

func isArrayOfTables(v any) bool {
	list, ok := v.([]any)
	if !ok || len(list) == 0 {
		return false
	}
	for _, it := range list {
		if _, ok := it.(*ojson.Object); !ok {
			return false
		}
	}
	return true
}

// writeTOMLTable writes a whole document from its values: a table's own
// keys first, then its sub-tables and arrays of tables as sections.
func writeTOMLTable(b *bytes.Buffer, o *ojson.Object, path ojson.Path) error {
	// TOML puts a table's own keys before its sections. To keep the keys in
	// their order, a table that comes before a plain value is written inline.
	keys := o.Keys()
	lastPlain := -1
	for i, k := range keys {
		v, _ := o.Get(k)
		if _, isObj := v.(*ojson.Object); !isObj && !isArrayOfTables(v) {
			lastPlain = i
		}
	}
	for i, k := range keys {
		v, _ := o.Get(k)
		if _, isObj := v.(*ojson.Object); (isObj || isArrayOfTables(v)) && i > lastPlain {
			continue
		}
		s, err := tomlInline(v)
		if err != nil {
			return err
		}
		b.WriteString(tomlDotted(ojson.Path{k}) + " = " + s + "\n")
	}
	for _, k := range keys[lastPlain+1:] {
		v, _ := o.Get(k)
		sub := append(append(ojson.Path(nil), path...), k)
		switch t := v.(type) {
		case *ojson.Object:
			b.WriteString("\n[" + tomlDotted(sub) + "]\n")
			if err := writeTOMLTable(b, t, sub); err != nil {
				return err
			}
		case []any:
			if !isArrayOfTables(t) {
				continue
			}
			for _, it := range t {
				b.WriteString("\n[[" + tomlDotted(sub) + "]]\n")
				if err := writeTOMLTable(b, it.(*ojson.Object), sub); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// ---- reading ----

var (
	tomlDateRe = regexp.MustCompile(`^(?:([0-9]{4})-([0-9]{2})-([0-9]{2}))?(?:(^|[Tt ])([0-9]{2}):([0-9]{2}):([0-9]{2})(?:\.[0-9]+)?(?:([Zz])|([+-])([0-9]{2}):([0-9]{2}))?)?$`)
	tomlDecRe  = regexp.MustCompile(`^[+-]?(0|[1-9](_?[0-9])*)$`)
	tomlHexRe  = regexp.MustCompile(`^0x[0-9A-Fa-f](_?[0-9A-Fa-f])*$`)
	tomlOctRe  = regexp.MustCompile(`^0o[0-7](_?[0-7])*$`)
	tomlBinRe  = regexp.MustCompile(`^0b[01](_?[01])*$`)
	tomlFltRe  = regexp.MustCompile(`^[+-]?(0|[1-9](_?[0-9])*)(\.[0-9](_?[0-9])*)?([eE][+-]?[0-9](_?[0-9])*)?$`)
	tomlSpcRe  = regexp.MustCompile(`^[+-]?(inf|nan)$`)
	tomlDayRe  = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
)

const bom = "\uFEFF"

// tomlDateKind says which of TOML's four date and time types s is, after
// checking every field is in range: "offset", "datetime", "date" or "time".
// Anything else gives "".
func tomlDateKind(s string) string {
	m := tomlDateRe.FindStringSubmatch(s)
	if m == nil || s == "" {
		return ""
	}
	num := func(x string) int { n, _ := strconv.Atoi(x); return n }
	hasDate, hasTime := m[1] != "", m[5] != ""
	if hasDate {
		y, mo, da := num(m[1]), num(m[2]), num(m[3])
		if mo < 1 || mo > 12 || da < 1 || da > time.Date(y, time.Month(mo)+1, 0, 0, 0, 0, 0, time.UTC).Day() {
			return ""
		}
	}
	if hasTime {
		if num(m[5]) > 23 || num(m[6]) > 59 || num(m[7]) > 60 {
			return ""
		}
	}
	offset := m[8] != "" || m[9] != ""
	if m[9] != "" && (num(m[10]) > 23 || num(m[11]) > 59) {
		return ""
	}
	switch {
	case hasDate && hasTime && m[4] == "":
		return "" // a date and a time need T or a space between them
	case !hasDate && m[4] != "":
		return "" // a time can't start with T or a space
	case hasDate && hasTime && offset:
		return "offset"
	case offset:
		return "" // an offset needs a date and a time
	case hasDate && hasTime:
		return "datetime"
	case hasDate:
		return "date"
	case hasTime:
		return "time"
	}
	return ""
}

func tomlIsInt(n json.Number) bool { return tomlIntRe.MatchString(string(n)) }

// tomlSame compares two TOML values strictly: by value, and by type, so a
// date is not text and 30 is not 30.0. Key order doesn't count.
func tomlSame(a, b any) bool {
	if p, ok := a.(yamlPlain); ok {
		a = string(p)
	}
	if p, ok := b.(yamlPlain); ok {
		b = string(p)
	}
	switch x := a.(type) {
	case tomlRaw:
		y, ok := b.(tomlRaw)
		return ok && x == y
	case string:
		y, ok := b.(string)
		return ok && x == y
	case json.Number:
		y, ok := b.(json.Number)
		if !ok || tomlIsInt(x) != tomlIsInt(y) {
			return false
		}
		if tomlIsInt(x) {
			xi, e1 := strconv.ParseInt(string(x), 10, 64)
			yi, e2 := strconv.ParseInt(string(y), 10, 64)
			return e1 == nil && e2 == nil && xi == yi
		}
		xf, e1 := strconv.ParseFloat(string(x), 64)
		yf, e2 := strconv.ParseFloat(string(y), 64)
		return e1 == nil && e2 == nil && xf == yf && math.Signbit(xf) == math.Signbit(yf)
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case *ojson.Object:
		y, ok := b.(*ojson.Object)
		if !ok || len(x.Keys()) != len(y.Keys()) {
			return false
		}
		for _, k := range x.Keys() {
			av, _ := x.Get(k)
			bv, ok := y.Get(k)
			if !ok || !tomlSame(av, bv) {
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
			if !tomlSame(x[i], y[i]) {
				return false
			}
		}
		return true
	}
	return equal(a, b)
}

type tomlParser struct {
	s       string
	i       int
	root    *ojson.Object
	cur     *ojson.Object
	curPath ojson.Path
	defined map[*ojson.Object]bool // by a header
	dotted  map[*ojson.Object]bool // by dotted keys
	frozen  map[*ojson.Object]bool // inline tables
	aot     map[*ojson.Object]map[string]bool
	stmts   []tomlStmt
	depth   int // arrays and inline tables open
}

type tomlErr struct {
	line int
	msg  string
}

func (e *tomlErr) Error() string { return fmt.Sprintf("line %d: %s", e.line, e.msg) }

func (p *tomlParser) fail(format string, args ...any) {
	panic(&tomlErr{line: strings.Count(p.s[:min(p.i, len(p.s))], "\n") + 1, msg: fmt.Sprintf(format, args...)})
}

func parseTOML(s string) (tree *ojson.Object, stmts []tomlStmt, err error) {
	if !utf8.ValidString(s) {
		return nil, nil, fmt.Errorf("the file is not valid UTF-8")
	}
	p := &tomlParser{s: s, root: ojson.NewObject(), defined: map[*ojson.Object]bool{}, dotted: map[*ojson.Object]bool{},
		frozen: map[*ojson.Object]bool{}, aot: map[*ojson.Object]map[string]bool{}}
	p.cur = p.root
	if strings.HasPrefix(s, bom) {
		p.i = len(bom) // a byte order mark is allowed and kept as it is
	}
	defer func() {
		if r := recover(); r != nil {
			te, ok := r.(*tomlErr)
			if !ok {
				panic(r)
			}
			tree, stmts, err = nil, nil, fmt.Errorf("can't read this TOML: %v", te)
		}
	}()
	p.document()
	return p.root, p.stmts, nil
}

func (p *tomlParser) eof() bool { return p.i >= len(p.s) }

func (p *tomlParser) skipWS() {
	for !p.eof() && (p.s[p.i] == ' ' || p.s[p.i] == '\t') {
		p.i++
	}
}

func (p *tomlParser) skipComment() {
	if !p.eof() && p.s[p.i] == '#' {
		for !p.eof() && p.s[p.i] != '\n' {
			c := p.s[p.i]
			if (c < 0x20 && c != '\t' && c != '\r') || c == 0x7f || (c == '\r' && !strings.HasPrefix(p.s[p.i:], "\r\n")) {
				p.fail("a control character in a comment")
			}
			p.i++
		}
	}
}

// newline consumes \n or \r\n and reports whether there was one.
func (p *tomlParser) newline() bool {
	if strings.HasPrefix(p.s[p.i:], "\r\n") {
		p.i += 2
		return true
	}
	if !p.eof() && p.s[p.i] == '\n' {
		p.i++
		return true
	}
	return false
}

func (p *tomlParser) endOfLine() {
	p.skipWS()
	p.skipComment()
	if !p.eof() && !p.newline() {
		p.fail("expected the end of the line")
	}
}

func (p *tomlParser) document() {
	for !p.eof() {
		p.skipWS()
		p.skipComment()
		if p.eof() {
			return
		}
		if p.newline() {
			continue
		}
		switch {
		case strings.HasPrefix(p.s[p.i:], "[["):
			p.arrayHeader()
		case p.s[p.i] == '[':
			p.tableHeader()
		default:
			p.keyValue(p.cur, p.curPath, true)
		}
		p.endOfLine()
	}
}

// key reads a dotted key.
func (p *tomlParser) key() []string {
	var segs []string
	for {
		p.skipWS()
		if p.eof() {
			p.fail("expected a key")
		}
		switch c := p.s[p.i]; {
		case c == '"':
			if strings.HasPrefix(p.s[p.i:], `"""`) {
				p.fail("a key can't be a multi-line string")
			}
			segs = append(segs, p.basicString())
		case c == '\'':
			if strings.HasPrefix(p.s[p.i:], `'''`) {
				p.fail("a key can't be a multi-line string")
			}
			segs = append(segs, p.literalString())
		default:
			j := p.i
			for j < len(p.s) && (isBareKeyChar(p.s[j])) {
				j++
			}
			if j == p.i {
				p.fail("expected a key")
			}
			segs = append(segs, p.s[p.i:j])
			p.i = j
		}
		if len(segs) > maxDepth {
			p.fail("a key with more than %d parts", maxDepth)
		}
		save := p.i
		p.skipWS()
		if !p.eof() && p.s[p.i] == '.' {
			p.i++
			continue
		}
		p.i = save
		return segs
	}
}

func isBareKeyChar(c byte) bool {
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

// table walks to a table for a header, creating missing tables on the way.
// Arrays of tables lead to their last element.
func (p *tomlParser) walk(segs []string) (*ojson.Object, ojson.Path) {
	obj, path := p.root, ojson.Path(nil)
	for _, seg := range segs {
		v, ok := obj.Get(seg)
		switch t := v.(type) {
		case nil:
			if ok {
				p.fail("%s is not a table", seg)
			}
			n := ojson.NewObject()
			obj.Set(seg, n)
			obj, path = n, append(path, seg)
		case *ojson.Object:
			if p.frozen[t] {
				p.fail("%s is an inline table and can't be extended", seg)
			}
			obj, path = t, append(path, seg)
		case []any:
			if !p.aot[obj][seg] || len(t) == 0 {
				p.fail("%s is an array, not a table", seg)
			}
			obj, path = t[len(t)-1].(*ojson.Object), append(path, seg, strconv.Itoa(len(t)-1))
		default:
			p.fail("%s already holds a value", seg)
		}
	}
	return obj, path
}

func (p *tomlParser) tableHeader() {
	start := p.i
	p.i++
	ks := p.i
	segs := p.key()
	ke := p.i
	p.skipWS()
	if p.eof() || p.s[p.i] != ']' {
		p.fail("expected ] after the table name")
	}
	p.i++
	parent, ppath := p.walk(segs[:len(segs)-1])
	last := segs[len(segs)-1]
	var t *ojson.Object
	switch v, ok := parent.Get(last); x := v.(type) {
	case nil:
		if ok {
			p.fail("%s already holds a value", last)
		}
		t = ojson.NewObject()
		parent.Set(last, t)
	case *ojson.Object:
		if p.defined[x] || p.dotted[x] || p.frozen[x] {
			p.fail("table %s is defined twice", strings.Join(segs, "."))
		}
		t = x
	default:
		p.fail("table %s is defined twice", strings.Join(segs, "."))
	}
	p.defined[t] = true
	p.cur, p.curPath = t, append(ppath, last)
	p.stmts = append(p.stmts, tomlStmt{kind: stmtTable, path: append(ojson.Path(nil), p.curPath...), textPath: segs,
		start: start, end: p.i - 1, keyStart: ks, keyEnd: ke})
}

func (p *tomlParser) arrayHeader() {
	start := p.i
	p.i += 2
	ks := p.i
	segs := p.key()
	ke := p.i
	p.skipWS()
	if !strings.HasPrefix(p.s[p.i:], "]]") {
		p.fail("expected ]] after the array name")
	}
	p.i += 2
	parent, ppath := p.walk(segs[:len(segs)-1])
	last := segs[len(segs)-1]
	t := ojson.NewObject()
	var list []any
	switch v, ok := parent.Get(last); x := v.(type) {
	case nil:
		if ok {
			p.fail("%s already holds a value", last)
		}
		if p.aot[parent] == nil {
			p.aot[parent] = map[string]bool{}
		}
		p.aot[parent][last] = true
	case []any:
		if !p.aot[parent][last] {
			p.fail("%s is an array written inline and can't be extended", last)
		}
		list = x
	default:
		p.fail("%s already holds a value", last)
	}
	list = append(list, t)
	parent.Set(last, list)
	p.defined[t] = true
	p.cur, p.curPath = t, append(ppath, last, strconv.Itoa(len(list)-1))
	p.stmts = append(p.stmts, tomlStmt{kind: stmtArray, path: append(ojson.Path(nil), p.curPath...), textPath: segs,
		start: start, end: p.i - 1, keyStart: ks, keyEnd: ke})
}

// keyValue reads key = value into table t. record says whether it's a
// statement of its own (not inside an inline table).
func (p *tomlParser) keyValue(t *ojson.Object, tpath ojson.Path, record bool) {
	start := p.i
	segs := p.key()
	ke := p.i
	p.skipWS()
	if p.eof() || p.s[p.i] != '=' {
		p.fail("expected = after the key")
	}
	p.i++
	p.skipWS()
	vs := p.i
	v := p.value()
	ve := p.i
	obj, path := t, append(ojson.Path(nil), tpath...)
	for _, seg := range segs[:len(segs)-1] {
		cur, ok := obj.Get(seg)
		switch x := cur.(type) {
		case nil:
			if ok {
				p.fail("%s already holds a value", seg)
			}
			n := ojson.NewObject()
			p.dotted[n] = true
			if p.frozen[obj] {
				p.frozen[n] = true
			}
			obj.Set(seg, n)
			obj = n
		case *ojson.Object:
			if !p.dotted[x] || p.frozen[x] && !p.frozen[obj] {
				p.fail("%s is a table defined elsewhere and can't be extended with a dotted key", seg)
			}
			obj = x
		default:
			p.fail("%s already holds a value", seg)
		}
		path = append(path, seg)
	}
	last := segs[len(segs)-1]
	if _, exists := obj.Get(last); exists {
		p.fail("%s is set twice", strings.Join(segs, "."))
	}
	obj.Set(last, v)
	path = append(path, last)
	if record {
		p.stmts = append(p.stmts, tomlStmt{kind: stmtKV, path: path, start: start, end: ve - 1,
			keyStart: start, keyEnd: ke, valStart: vs, valEnd: ve})
	}
}

func (p *tomlParser) value() any {
	if p.eof() {
		p.fail("expected a value")
	}
	switch c := p.s[p.i]; {
	case strings.HasPrefix(p.s[p.i:], `"""`):
		return p.multiBasic()
	case c == '"':
		return p.basicString()
	case strings.HasPrefix(p.s[p.i:], `'''`):
		return p.multiLiteral()
	case c == '\'':
		return p.literalString()
	case c == '[', c == '{':
		p.depth++
		if p.depth > maxDepth {
			p.fail("values nested more than %d levels deep", maxDepth)
		}
		defer func() { p.depth-- }()
		if c == '[' {
			return p.array()
		}
		return p.inlineTable()
	}
	return p.scalar()
}

func (p *tomlParser) array() any {
	p.i++
	out := []any{}
	for {
		p.skipBlank()
		if p.eof() {
			p.fail("an array is not closed")
		}
		if p.s[p.i] == ']' {
			p.i++
			return out
		}
		out = append(out, p.value())
		p.skipBlank()
		if p.eof() {
			p.fail("an array is not closed")
		}
		switch p.s[p.i] {
		case ',':
			p.i++
		case ']':
			p.i++
			return out
		default:
			p.fail("expected , or ] in an array")
		}
	}
}

// skipBlank skips spaces, comments and line breaks inside an array.
func (p *tomlParser) skipBlank() {
	for {
		p.skipWS()
		p.skipComment()
		if !p.newline() {
			return
		}
	}
}

func (p *tomlParser) inlineTable() any {
	p.i++
	t := ojson.NewObject()
	p.frozen[t] = true
	p.skipWS()
	if !p.eof() && p.s[p.i] == '}' {
		p.i++
		return t
	}
	for {
		p.skipWS()
		p.keyValue(t, nil, false)
		p.skipWS()
		if p.eof() {
			p.fail("an inline table is not closed")
		}
		switch p.s[p.i] {
		case ',':
			p.i++
		case '}':
			p.i++
			return t
		default:
			p.fail("expected , or } in an inline table (inline tables stay on one line)")
		}
	}
}

func (p *tomlParser) scalar() any {
	j := p.i
	for j < len(p.s) && strings.IndexByte("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_+-.:", p.s[j]) >= 0 {
		j++
	}
	tok := p.s[p.i:j]
	// A date and a time may be separated by a space.
	if tomlDayRe.MatchString(tok) && j+3 < len(p.s) && p.s[j] == ' ' &&
		p.s[j+1] >= '0' && p.s[j+1] <= '9' && p.s[j+2] >= '0' && p.s[j+2] <= '9' && p.s[j+3] == ':' {
		k := j + 1
		for k < len(p.s) && strings.IndexByte("0123456789:.Zz+-", p.s[k]) >= 0 {
			k++
		}
		tok, j = p.s[p.i:k], k
	}
	if tok == "" {
		p.fail("expected a value")
	}
	p.i = j
	switch {
	case tok == "true":
		return true
	case tok == "false":
		return false
	case tomlDateKind(tok) != "", tomlSpcRe.MatchString(tok):
		return tomlRaw(tok)
	case tomlDecRe.MatchString(tok):
		s := strings.TrimPrefix(strings.ReplaceAll(tok, "_", ""), "+")
		if _, err := strconv.ParseInt(s, 10, 64); err != nil {
			p.fail("an integer too large for TOML (64 bits at most)")
		}
		return json.Number(s)
	case tomlHexRe.MatchString(tok), tomlOctRe.MatchString(tok), tomlBinRe.MatchString(tok):
		base := map[byte]int{'x': 16, 'o': 8, 'b': 2}[tok[1]]
		n, err := strconv.ParseInt(strings.ReplaceAll(tok[2:], "_", ""), base, 64)
		if err != nil {
			p.fail("an integer too large for TOML (64 bits at most)")
		}
		return json.Number(strconv.FormatInt(n, 10))
	case tomlFltRe.MatchString(tok):
		n := strings.TrimPrefix(strings.ReplaceAll(tok, "_", ""), "+")
		if f, err := strconv.ParseFloat(n, 64); err != nil && math.IsInf(f, 0) {
			p.fail("a number too large for a TOML float")
		}
		return json.Number(n)
	}
	// The token isn't quoted in the message: it may be a secret written
	// without quotes.
	p.fail("not a TOML value (text needs quotes)")
	return nil
}

func (p *tomlParser) basicString() string {
	p.i++
	var b strings.Builder
	for {
		if p.eof() || p.s[p.i] == '\n' {
			p.fail("a string is not closed")
		}
		c := p.s[p.i]
		switch {
		case c == '"':
			p.i++
			return b.String()
		case c == '\\':
			p.escape(&b)
		case (c < 0x20 && c != '\t') || c == 0x7f:
			p.fail("a control character in a string")
		default:
			r, n := utf8.DecodeRuneInString(p.s[p.i:])
			b.WriteRune(r)
			p.i += n
		}
	}
}

func (p *tomlParser) escape(b *strings.Builder) {
	p.i++
	if p.eof() {
		p.fail("a string is not closed")
	}
	c := p.s[p.i]
	p.i++
	switch c {
	case 'b':
		b.WriteByte('\b')
	case 't':
		b.WriteByte('\t')
	case 'n':
		b.WriteByte('\n')
	case 'f':
		b.WriteByte('\f')
	case 'r':
		b.WriteByte('\r')
	case '"':
		b.WriteByte('"')
	case '\\':
		b.WriteByte('\\')
	case 'u', 'U':
		n := 4
		if c == 'U' {
			n = 8
		}
		if p.i+n > len(p.s) {
			p.fail("a short \\%c escape", c)
		}
		v, err := strconv.ParseUint(p.s[p.i:p.i+n], 16, 32)
		if err != nil || !utf8.ValidRune(rune(v)) {
			p.fail("a bad \\%c escape", c)
		}
		b.WriteRune(rune(v))
		p.i += n
	default:
		p.fail("an unknown escape \\%c", c)
	}
}

func (p *tomlParser) literalString() string {
	p.i++
	j := p.i
	for j < len(p.s) && p.s[j] != '\'' {
		if p.s[j] == '\n' || (p.s[j] < 0x20 && p.s[j] != '\t') || p.s[j] == 0x7f {
			p.fail("a string is not closed")
		}
		j++
	}
	if j >= len(p.s) {
		p.fail("a string is not closed")
	}
	s := p.s[p.i:j]
	p.i = j + 1
	return s
}

// closeMulti handles a closing delimiter that comes with up to two extra
// quotes, which belong to the string.
func (p *tomlParser) closeMulti(b *strings.Builder, q byte) bool {
	n := 0
	for p.i+n < len(p.s) && p.s[p.i+n] == q && n < 5 {
		n++
	}
	if n < 3 {
		return false
	}
	for k := 0; k < n-3; k++ {
		b.WriteByte(q)
	}
	p.i += n
	return true
}

func (p *tomlParser) multiBasic() string {
	p.i += 3
	p.newline() // a line break right after the opening quotes is dropped
	var b strings.Builder
	for {
		if p.eof() {
			p.fail("a multi-line string is not closed")
		}
		c := p.s[p.i]
		switch {
		case c == '"' && p.closeMulti(&b, '"'):
			return b.String()
		case c == '\\':
			// A backslash at the end of a line drops the break and the
			// space that follows.
			k := p.i + 1
			for k < len(p.s) && (p.s[k] == ' ' || p.s[k] == '\t') {
				k++
			}
			if k < len(p.s) && (p.s[k] == '\n' || strings.HasPrefix(p.s[k:], "\r\n")) {
				p.i = k
				for !p.eof() && strings.IndexByte(" \t\r\n", p.s[p.i]) >= 0 {
					p.i++
				}
				continue
			}
			p.escape(&b)
		case c == '\r' && strings.HasPrefix(p.s[p.i:], "\r\n"):
			b.WriteString("\r\n")
			p.i += 2
		case (c < 0x20 && c != '\t' && c != '\n') || c == 0x7f:
			p.fail("a control character in a string")
		default:
			r, n := utf8.DecodeRuneInString(p.s[p.i:])
			b.WriteRune(r)
			p.i += n
		}
	}
}

func (p *tomlParser) multiLiteral() string {
	p.i += 3
	p.newline()
	var b strings.Builder
	for {
		if p.eof() {
			p.fail("a multi-line string is not closed")
		}
		c := p.s[p.i]
		switch {
		case c == '\'' && p.closeMulti(&b, '\''):
			return b.String()
		case c == '\r' && strings.HasPrefix(p.s[p.i:], "\r\n"):
			b.WriteString("\r\n")
			p.i += 2
		case (c < 0x20 && c != '\t' && c != '\n') || c == 0x7f:
			p.fail("a control character in a string")
		default:
			r, n := utf8.DecodeRuneInString(p.s[p.i:])
			b.WriteRune(r)
			p.i += n
		}
	}
}
