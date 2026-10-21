// Package yamlite reads the subset of YAML that Aftermarket rule and fixture
// files use: block mappings, block sequences, one-line flow collections
// ([a, b] and {k: v}), plain and quoted scalars, literal (|) and folded (>)
// block scalars, and comments.
//
// Anything outside that subset (anchors, aliases, tags, merge keys, complex
// keys, multiple documents, tabs in indentation) is rejected with a line
// number instead of being guessed at.
package yamlite

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Kind is the type of a Node.
type Kind int

const (
	// Null is an empty value (missing, "~" or "null").
	Null Kind = iota
	// Scalar is a string, number or boolean in its source form.
	Scalar
	// Map is a mapping with keys in source order.
	Map
	// Seq is a sequence.
	Seq
)

func (k Kind) String() string {
	switch k {
	case Null:
		return "null"
	case Scalar:
		return "scalar"
	case Map:
		return "mapping"
	case Seq:
		return "list"
	}
	return "unknown"
}

// Node is one parsed value.
type Node struct {
	Kind   Kind
	Line   int    // 1-based source line
	Value  string // Scalar text (unquoted)
	Quoted bool   // Scalar was written in quotes or as a block scalar
	Pairs  []Pair // Map entries in source order
	Items  []*Node
}

// Pair is one mapping entry.
type Pair struct {
	Key       string
	KeyLine   int
	KeyQuoted bool // the key was written in quotes
	Value     *Node
}

// Get returns the value for key in a Map node, or nil.
func (n *Node) Get(key string) *Node {
	if n == nil || n.Kind != Map {
		return nil
	}
	for _, p := range n.Pairs {
		if p.Key == key {
			return p.Value
		}
	}
	return nil
}

// Error is a parse error with a line number.
type Error struct {
	Line int
	Msg  string
}

func (e *Error) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("line %d: %s", e.Line, e.Msg)
	}
	return e.Msg
}

func errf(line int, format string, args ...any) error {
	return &Error{Line: line, Msg: fmt.Sprintf(format, args...)}
}

type line struct {
	num    int
	indent int
	text   string // content without indentation and without comments
	raw    string // original line without the trailing newline
}

type parser struct {
	lines []line
	pos   int
}

// Parse reads one YAML document.
func Parse(src []byte) (*Node, error) {
	text := strings.ReplaceAll(string(src), "\r\n", "\n")
	text = strings.TrimPrefix(text, "\xef\xbb\xbf")
	raw := strings.Split(text, "\n")
	p := &parser{}
	seenDoc := false
	for i, r := range raw {
		num := i + 1
		trimmed := strings.TrimLeft(r, " ")
		indent := len(r) - len(trimmed)
		if strings.HasPrefix(trimmed, "\t") {
			if strings.TrimSpace(trimmed) != "" {
				return nil, errf(num, "tabs are not allowed in indentation; use spaces")
			}
		}
		content, err := stripComment(trimmed, num)
		if err != nil {
			return nil, err
		}
		content = strings.TrimRight(content, " \t")
		if content == "" {
			// Keep blank lines only as raw text for block scalars.
			p.lines = append(p.lines, line{num: num, indent: -1, raw: r})
			continue
		}
		if indent == 0 && (content == "---" || strings.HasPrefix(content, "--- ")) {
			if seenDoc || len(nonBlank(p.lines)) > 0 {
				return nil, errf(num, "multiple YAML documents are not supported")
			}
			seenDoc = true
			if rest := strings.TrimSpace(strings.TrimPrefix(content, "---")); rest != "" {
				return nil, errf(num, "content after --- is not supported")
			}
			continue
		}
		if indent == 0 && content == "..." {
			return nil, errf(num, "document end markers are not supported")
		}
		if strings.HasPrefix(content, "%") && indent == 0 {
			return nil, errf(num, "YAML directives are not supported")
		}
		p.lines = append(p.lines, line{num: num, indent: indent, text: content, raw: r})
	}
	p.skipBlank()
	if p.pos >= len(p.lines) {
		return &Node{Kind: Null, Line: 1}, nil
	}
	first := p.lines[p.pos]
	if first.indent != 0 {
		return nil, errf(first.num, "the document must start at column 1")
	}
	n, err := p.parseBlock(0)
	if err != nil {
		return nil, err
	}
	p.skipBlank()
	if p.pos < len(p.lines) {
		l := p.lines[p.pos]
		return nil, errf(l.num, "unexpected indentation")
	}
	return n, nil
}

func nonBlank(ls []line) []line {
	var out []line
	for _, l := range ls {
		if l.indent >= 0 {
			out = append(out, l)
		}
	}
	return out
}

// stripComment removes a trailing # comment that is outside quotes.
func stripComment(s string, num int) (string, error) {
	inSingle, inDouble := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inDouble:
			if c == '\\' {
				i++
			} else if c == '"' {
				inDouble = false
			}
		case inSingle:
			if c == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					i++
				} else {
					inSingle = false
				}
			}
		default:
			switch c {
			case '"':
				if i == 0 || isQuoteStart(s, i) {
					inDouble = true
				}
			case '\'':
				if i == 0 || isQuoteStart(s, i) {
					inSingle = true
				}
			case '#':
				if i == 0 || s[i-1] == ' ' || s[i-1] == '\t' {
					return s[:i], nil
				}
			}
		}
	}
	return s, nil
}

// isQuoteStart reports whether a quote at s[i] opens a quoted scalar, which
// is only the case at the start of a value (after ": ", "- ", "[", "{" or ",").
func isQuoteStart(s string, i int) bool {
	j := i - 1
	for j >= 0 && s[j] == ' ' {
		j--
	}
	if j < 0 {
		return true
	}
	switch s[j] {
	case '[', '{', ',':
		return true
	case ':', '-':
		// Only "key: 'x'" and "- 'x'": a space must separate them from the
		// quote, and a list dash must stand on its own.
		if j == i-1 {
			return false
		}
		return s[j] == ':' || j == 0 || s[j-1] == ' '
	}
	return false
}

func (p *parser) skipBlank() {
	for p.pos < len(p.lines) && p.lines[p.pos].indent < 0 {
		p.pos++
	}
}

func (p *parser) peek() (line, bool) {
	p.skipBlank()
	if p.pos >= len(p.lines) {
		return line{}, false
	}
	return p.lines[p.pos], true
}

func isSeqItem(text string) bool {
	return text == "-" || strings.HasPrefix(text, "- ")
}

// parseBlock parses the mapping or sequence that starts at the current line,
// whose items sit at exactly indent.
func (p *parser) parseBlock(indent int) (*Node, error) {
	l, ok := p.peek()
	if !ok {
		return &Node{Kind: Null}, nil
	}
	if l.indent != indent {
		return nil, errf(l.num, "unexpected indentation")
	}
	if isSeqItem(l.text) {
		return p.parseSeq(indent)
	}
	return p.parseMap(indent)
}

func (p *parser) parseSeq(indent int) (*Node, error) {
	first, _ := p.peek()
	n := &Node{Kind: Seq, Line: first.num}
	for {
		l, ok := p.peek()
		if !ok || l.indent < indent {
			return n, nil
		}
		if l.indent > indent {
			return nil, errf(l.num, "unexpected indentation inside a list")
		}
		if !isSeqItem(l.text) {
			return n, nil // a sibling mapping key at the same indent ends the list
		}
		rest := strings.TrimLeft(strings.TrimPrefix(l.text, "-"), " ")
		if rest == "" {
			p.pos++
			next, ok := p.peek()
			if !ok || next.indent <= indent {
				n.Items = append(n.Items, &Node{Kind: Null, Line: l.num})
				continue
			}
			child, err := p.parseBlock(next.indent)
			if err != nil {
				return nil, err
			}
			n.Items = append(n.Items, child)
			continue
		}
		// "- key: value" or "- - x" opens a nested block at the column of rest.
		offset := len(l.text) - len(rest)
		if isSeqItem(rest) || looksLikeKey(rest) {
			p.lines[p.pos].indent = indent + offset
			p.lines[p.pos].text = rest
			child, err := p.parseBlock(indent + offset)
			if err != nil {
				return nil, err
			}
			n.Items = append(n.Items, child)
			continue
		}
		p.pos++
		v, err := p.parseInlineValue(rest, l, indent)
		if err != nil {
			return nil, err
		}
		n.Items = append(n.Items, v)
	}
}

func (p *parser) parseMap(indent int) (*Node, error) {
	first, _ := p.peek()
	n := &Node{Kind: Map, Line: first.num}
	seen := map[string]bool{}
	for {
		l, ok := p.peek()
		if !ok || l.indent < indent {
			return n, nil
		}
		if l.indent > indent {
			return nil, errf(l.num, "unexpected indentation")
		}
		if isSeqItem(l.text) {
			return nil, errf(l.num, "a list item cannot appear here; expected key: value")
		}
		key, rest, err := splitKey(l.text, l.num)
		if err != nil {
			return nil, err
		}
		if seen[key] {
			return nil, errf(l.num, "duplicate key %q", key)
		}
		seen[key] = true
		p.pos++
		var v *Node
		if rest == "" {
			next, ok := p.peek()
			switch {
			case ok && next.indent > indent:
				v, err = p.parseBlock(next.indent)
			case ok && next.indent == indent && isSeqItem(next.text):
				v, err = p.parseSeq(indent)
			default:
				v = &Node{Kind: Null, Line: l.num}
			}
		} else {
			v, err = p.parseInlineValue(rest, l, indent)
		}
		if err != nil {
			return nil, err
		}
		n.Pairs = append(n.Pairs, Pair{Key: key, KeyLine: l.num, KeyQuoted: l.text[0] == '"' || l.text[0] == '\'', Value: v})
	}
}

// looksLikeKey reports whether s starts a "key: value" entry.
func looksLikeKey(s string) bool {
	_, _, err := splitKey(s, 0)
	return err == nil
}

// splitKey splits "key: value" into its parts.
func splitKey(s string, num int) (string, string, error) {
	if s == "" {
		return "", "", errf(num, "expected key: value")
	}
	switch s[0] {
	case '"', '\'':
		val, n, err := readQuoted(s, num)
		if err != nil {
			return "", "", err
		}
		after := s[n:]
		if after == ":" {
			return val, "", nil
		}
		if strings.HasPrefix(after, ": ") {
			return val, strings.TrimSpace(after[2:]), nil
		}
		return "", "", errf(num, "expected key: value")
	case '?':
		return "", "", errf(num, "complex keys (?) are not supported")
	case '[', '{':
		return "", "", errf(num, "expected key: value")
	}
	idx := -1
	for i := 0; i < len(s); i++ {
		if s[i] == ':' && (i == len(s)-1 || s[i+1] == ' ') {
			idx = i
			break
		}
	}
	if idx <= 0 {
		return "", "", errf(num, "expected key: value")
	}
	key := strings.TrimSpace(s[:idx])
	if key == "<<" {
		return "", "", errf(num, "merge keys (<<) are not supported")
	}
	if strings.ContainsAny(key[:1], "&*!|>%@`") {
		return "", "", errf(num, "key %q starts with a reserved character", key)
	}
	return key, strings.TrimSpace(s[idx+1:]), nil
}

// parseInlineValue parses the value text that follows "key:" or "- ".
func (p *parser) parseInlineValue(rest string, l line, indent int) (*Node, error) {
	switch {
	case rest == "|" || rest == "|-" || rest == "|+" || rest == ">" || rest == ">-" || rest == ">+":
		return p.parseBlockScalar(rest, l, indent)
	case rest[0] == '[' || rest[0] == '{':
		text := rest
		for !balanced(text) {
			next, ok := p.peek()
			if !ok || next.indent <= indent {
				return nil, errf(l.num, "unclosed %c", rest[0])
			}
			text += " " + next.text
			p.pos++
		}
		fp := &flowParser{s: text, line: l.num}
		v, err := fp.value()
		if err != nil {
			return nil, err
		}
		fp.skipSpace()
		if fp.i != len(fp.s) {
			return nil, errf(l.num, "unexpected text after %c...%c", rest[0], closing(rest[0]))
		}
		return v, nil
	}
	return scalar(rest, l.num)
}

func closing(c byte) byte {
	if c == '[' {
		return ']'
	}
	return '}'
}

func balanced(s string) bool {
	depth := 0
	inS, inD := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inD:
			if c == '\\' {
				i++
			} else if c == '"' {
				inD = false
			}
		case inS:
			if c == '\'' {
				inS = false
			}
		case c == '"':
			inD = true
		case c == '\'':
			inS = true
		case c == '[' || c == '{':
			depth++
		case c == ']' || c == '}':
			depth--
		}
	}
	return depth <= 0
}

func (p *parser) parseBlockScalar(header string, l line, indent int) (*Node, error) {
	var body []string
	blockIndent := -1
	for p.pos < len(p.lines) {
		cur := p.lines[p.pos]
		if cur.indent < 0 {
			// A blank line, or a line that looked like a comment. Inside a
			// block scalar "# text" is content, so check the raw line.
			raw := strings.TrimRight(cur.raw, " \t\r")
			rawIndent := len(raw) - len(strings.TrimLeft(raw, " "))
			if strings.TrimSpace(raw) != "" && rawIndent > indent {
				if blockIndent < 0 {
					blockIndent = rawIndent
				}
				if rawIndent >= blockIndent {
					body = append(body, cur.raw[blockIndent:])
					p.pos++
					continue
				}
			}
			if strings.TrimSpace(raw) != "" && rawIndent <= indent {
				break // a comment after the block ends it
			}
			body = append(body, "")
			p.pos++
			continue
		}
		if cur.indent <= indent {
			break
		}
		if blockIndent < 0 {
			blockIndent = cur.indent
		}
		if cur.indent < blockIndent {
			return nil, errf(cur.num, "block scalar line is less indented than the first one")
		}
		body = append(body, cur.raw[blockIndent:])
		p.pos++
	}
	// Trailing blank lines belong to chomping, not content.
	trailing := 0
	for len(body) > 0 && strings.TrimSpace(body[len(body)-1]) == "" {
		body = body[:len(body)-1]
		trailing++
	}
	var text string
	if header[0] == '|' {
		text = strings.Join(body, "\n")
	} else {
		text = fold(body)
	}
	switch {
	case strings.HasSuffix(header, "-"):
	case strings.HasSuffix(header, "+"):
		if len(body) > 0 {
			text += "\n" + strings.Repeat("\n", trailing)
		}
	default:
		if len(body) > 0 {
			text += "\n"
		}
	}
	return &Node{Kind: Scalar, Line: l.num, Value: text, Quoted: true}, nil
}

func fold(lines []string) string {
	var b strings.Builder
	for i, ln := range lines {
		if i > 0 {
			switch {
			case ln == "":
				b.WriteString("\n")
			case lines[i-1] == "":
				// the blank line already produced the line break
			case strings.HasPrefix(ln, " ") || strings.HasPrefix(lines[i-1], " "):
				b.WriteString("\n")
			default:
				b.WriteString(" ")
			}
		}
		b.WriteString(ln)
	}
	return b.String()
}

func scalar(s string, num int) (*Node, error) {
	if s == "" {
		return &Node{Kind: Null, Line: num}, nil
	}
	switch s[0] {
	case '"', '\'':
		v, n, err := readQuoted(s, num)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(s[n:]) != "" {
			return nil, errf(num, "unexpected text after quoted value")
		}
		return &Node{Kind: Scalar, Line: num, Value: v, Quoted: true}, nil
	case '&':
		return nil, errf(num, "anchors (&) are not supported")
	case '*':
		return nil, errf(num, "aliases (*) are not supported")
	case '!':
		return nil, errf(num, "tags (!) are not supported")
	case '|', '>':
		return nil, errf(num, "block scalar headers with options are not supported; use | or > on their own")
	case '@', '`':
		return nil, errf(num, "values cannot start with %c; quote them", s[0])
	}
	if s == "~" || s == "null" || s == "Null" || s == "NULL" {
		return &Node{Kind: Null, Line: num}, nil
	}
	if strings.Contains(s, ": ") || strings.HasSuffix(s, ":") {
		return nil, errf(num, "a nested mapping must start on its own line; quote the value if it contains ': '")
	}
	return &Node{Kind: Scalar, Line: num, Value: s}, nil
}

// readQuoted reads a quoted scalar at the start of s and returns its value and
// the number of bytes consumed.
func readQuoted(s string, num int) (string, int, error) {
	q := s[0]
	if q == '\'' {
		var b strings.Builder
		for i := 1; i < len(s); i++ {
			if s[i] == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					b.WriteByte('\'')
					i++
					continue
				}
				return b.String(), i + 1, nil
			}
			b.WriteByte(s[i])
		}
		return "", 0, errf(num, "unclosed single quote")
	}
	for i := 1; i < len(s); i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if s[i] == '"' {
			v, err := unescapeDouble(s[1:i])
			if err != nil {
				return "", 0, errf(num, "bad escape in double-quoted string: %v", err)
			}
			return v, i + 1, nil
		}
	}
	return "", 0, errf(num, "unclosed double quote")
}

func unescapeDouble(s string) (string, error) {
	if !strings.Contains(s, "\\") {
		return s, nil
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		i++
		if i >= len(s) {
			return "", fmt.Errorf("trailing backslash")
		}
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
			b.WriteByte('\r')
		case '0':
			b.WriteByte(0)
		case '"', '\\', '/':
			b.WriteByte(s[i])
		case ' ':
			b.WriteByte(' ')
		case 'u', 'U', 'x':
			size := map[byte]int{'x': 2, 'u': 4, 'U': 8}[s[i]]
			if i+1+size > len(s) {
				return "", fmt.Errorf("short \\%c escape", s[i])
			}
			code, err := strconv.ParseUint(s[i+1:i+1+size], 16, 32)
			if err != nil {
				return "", fmt.Errorf("bad \\%c escape", s[i])
			}
			i += size
			r := rune(code)
			if utf16.IsSurrogate(r) {
				// A high surrogate must be followed by a \u low surrogate.
				if r < 0xdc00 && i+6 < len(s)+0 && s[i+1] == '\\' && s[i+2] == 'u' {
					lo, err := strconv.ParseUint(s[i+3:i+7], 16, 32)
					if err == nil {
						if pair := utf16.DecodeRune(r, rune(lo)); pair != utf8.RuneError {
							b.WriteRune(pair)
							i += 6
							continue
						}
					}
				}
				return "", fmt.Errorf("unpaired surrogate escape")
			}
			if !utf8.ValidRune(r) {
				return "", fmt.Errorf("escape is not a valid character")
			}
			b.WriteRune(r)
		default:
			return "", fmt.Errorf("unknown escape \\%c", s[i])
		}
	}
	return b.String(), nil
}

type flowParser struct {
	s    string
	i    int
	line int
}

func (f *flowParser) skipSpace() {
	for f.i < len(f.s) && f.s[f.i] == ' ' {
		f.i++
	}
}

func (f *flowParser) value() (*Node, error) {
	f.skipSpace()
	if f.i >= len(f.s) {
		return nil, errf(f.line, "missing value")
	}
	switch f.s[f.i] {
	case '[':
		f.i++
		n := &Node{Kind: Seq, Line: f.line}
		for {
			f.skipSpace()
			if f.i < len(f.s) && f.s[f.i] == ']' {
				f.i++
				return n, nil
			}
			if f.i < len(f.s) && f.s[f.i] == ',' {
				return nil, errf(f.line, "empty item in list")
			}
			v, err := f.value()
			if err != nil {
				return nil, err
			}
			n.Items = append(n.Items, v)
			f.skipSpace()
			if f.i < len(f.s) && f.s[f.i] == ',' {
				f.i++
				continue
			}
			if f.i < len(f.s) && f.s[f.i] == ']' {
				f.i++
				return n, nil
			}
			return nil, errf(f.line, "expected , or ] in list")
		}
	case '{':
		f.i++
		n := &Node{Kind: Map, Line: f.line}
		seen := map[string]bool{}
		for {
			f.skipSpace()
			if f.i < len(f.s) && f.s[f.i] == '}' {
				f.i++
				return n, nil
			}
			key, quoted, err := f.key()
			if err != nil {
				return nil, err
			}
			if seen[key] {
				return nil, errf(f.line, "duplicate key %q", key)
			}
			seen[key] = true
			v, err := f.value()
			if err != nil {
				return nil, err
			}
			n.Pairs = append(n.Pairs, Pair{Key: key, KeyLine: f.line, KeyQuoted: quoted, Value: v})
			f.skipSpace()
			if f.i < len(f.s) && f.s[f.i] == ',' {
				f.i++
				continue
			}
			if f.i < len(f.s) && f.s[f.i] == '}' {
				f.i++
				return n, nil
			}
			return nil, errf(f.line, "expected , or } in mapping")
		}
	case '"', '\'':
		v, n, err := readQuoted(f.s[f.i:], f.line)
		if err != nil {
			return nil, err
		}
		f.i += n
		return &Node{Kind: Scalar, Line: f.line, Value: v, Quoted: true}, nil
	}
	start := f.i
	for f.i < len(f.s) && !strings.ContainsRune(",]}", rune(f.s[f.i])) {
		f.i++
	}
	text := strings.TrimSpace(f.s[start:f.i])
	if strings.Contains(text, ": ") {
		return nil, errf(f.line, "unexpected ': ' inside a flow value; quote it")
	}
	return scalar(text, f.line)
}

func (f *flowParser) key() (string, bool, error) {
	f.skipSpace()
	if f.i < len(f.s) && (f.s[f.i] == '"' || f.s[f.i] == '\'') {
		v, n, err := readQuoted(f.s[f.i:], f.line)
		if err != nil {
			return "", false, err
		}
		f.i += n
		f.skipSpace()
		if f.i >= len(f.s) || f.s[f.i] != ':' {
			return "", false, errf(f.line, "expected : after key")
		}
		f.i++
		return v, true, nil
	}
	start := f.i
	for f.i < len(f.s) {
		if f.s[f.i] == ':' && (f.i+1 == len(f.s) || f.s[f.i+1] == ' ') {
			key := strings.TrimSpace(f.s[start:f.i])
			f.i++
			if key == "" {
				return "", false, errf(f.line, "empty key")
			}
			return key, false, nil
		}
		if strings.ContainsRune(",]}{[", rune(f.s[f.i])) {
			break
		}
		f.i++
	}
	return "", false, errf(f.line, "expected key: value inside { }")
}
