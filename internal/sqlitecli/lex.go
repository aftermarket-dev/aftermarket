package sqlitecli

import (
	"errors"
	"fmt"
	"strings"
)

// Everything Aftermarket hands to sqlite3 goes through its shell, which
// reads its input line by line: a line starting with "." or "#" between
// statements is a shell command or comment, and a line that is only "GO"
// or "/" ends a statement. SQL text from a file or a mapping must never be
// able to do either, so it is split here, the way SQLite and the shell
// split it, and refused when the two could read it differently. What is
// then sent is each statement on its own, followed by a ";" line.

// Kind is the kind of a token.
type Kind int

// Token kinds.
const (
	Space   Kind = iota // spaces and newlines
	Comment             // -- to the end of the line, or /* */
	String              // '...'
	Quoted              // "...", [...] or `...`: a name
	Word                // a keyword or a bare name
	Semi                // ;
	Open                // (
	Close               // )
	Other               // anything else: numbers, operators, parameters
)

// Token is one piece of SQL text.
type Token struct {
	Kind  Kind
	Text  string
	Line  int // the line it starts on, from 1
	Depth int // how many parentheses it is inside
}

// Upper is the token's text in upper case, for keywords.
func (t Token) Upper() string { return strings.ToUpper(t.Text) }

// Lex cuts text into tokens. It fails on a string, quoted name or comment
// that doesn't end, since SQLite and the shell would read the rest of the
// text differently.
func Lex(text string) ([]Token, error) {
	var out []Token
	line, depth := 1, 0
	for i := 0; i < len(text); {
		c := text[i]
		start, kind := i, Other
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v':
			kind = Space
			for i < len(text) && strings.IndexByte(" \t\n\r\f\v", text[i]) >= 0 {
				i++
			}
		case c == '-' && strings.HasPrefix(text[i:], "--"):
			kind = Comment
			for i < len(text) && text[i] != '\n' {
				i++
			}
		case c == '/' && strings.HasPrefix(text[i:], "/*"):
			kind = Comment
			end := strings.Index(text[i+2:], "*/")
			if end < 0 {
				return nil, fmt.Errorf("line %d: a /* comment that never ends", line)
			}
			i += 2 + end + 2
		case c == '\'' || c == '"' || c == '`' || c == '[':
			kind = Quoted
			if c == '\'' {
				kind = String
			}
			closer := c
			if c == '[' {
				closer = ']'
			}
			j := i + 1
			for {
				if j >= len(text) {
					what := "a quoted name"
					if c == '\'' {
						what = "a string"
					}
					return nil, fmt.Errorf("line %d: %s that never ends", line, what)
				}
				if text[j] == closer {
					if closer != ']' && j+1 < len(text) && text[j+1] == closer {
						j += 2
						continue
					}
					break
				}
				j++
			}
			i = j + 1
		case c == ';':
			kind = Semi
			i++
		case c == '(':
			kind = Open
			i++
		case c == ')':
			kind = Close
			i++
		case isWordStart(c):
			kind = Word
			for i < len(text) && isWordPart(text[i]) {
				i++
			}
		default:
			i++
		}
		t := Token{Kind: kind, Text: text[start:i], Line: line, Depth: depth}
		switch kind {
		case Open:
			depth++
		case Close:
			if depth > 0 {
				depth--
			}
			t.Depth = depth
		}
		out = append(out, t)
		line += strings.Count(t.Text, "\n")
	}
	return out, nil
}

func isWordStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

func isWordPart(c byte) bool {
	return isWordStart(c) || c >= '0' && c <= '9' || c == '$'
}

// Significant reports whether a token is more than space or a comment.
func (t Token) Significant() bool { return t.Kind != Space && t.Kind != Comment }

// Stmt is one statement and the line it starts on.
type Stmt struct {
	Line int
	SQL  string // from its first significant token, without the final ;
}

// Split cuts text into statements at semicolons, as SQLite does: not
// inside strings, quoted names or comments, and inside a CREATE TRIGGER
// only after its END. Statements that are only comments are left out.
// Each statement is checked as it will be sent to sqlite3 (its text, then
// a ";" line), and refused when the shell could take any of it as one of
// its own commands.
func Split(text string) ([]Stmt, error) {
	if err := controlBytes(text); err != nil {
		return nil, err
	}
	toks, err := Lex(text)
	if err != nil {
		return nil, err
	}
	var out []Stmt
	var cur []Token
	flush := func() {
		first := -1
		for i, t := range cur {
			if t.Significant() {
				first = i
				break
			}
		}
		if first >= 0 {
			var b strings.Builder
			for _, t := range cur[first:] {
				b.WriteString(t.Text)
			}
			out = append(out, Stmt{Line: cur[first].Line, SQL: strings.TrimRight(b.String(), " \t\r\n\f\v")})
		}
		cur = cur[:0]
	}
	for _, t := range toks {
		if t.Kind == Semi && (!isTrigger(cur) || lastWord(cur) == "END") {
			flush()
			continue
		}
		cur = append(cur, t)
	}
	flush()
	for _, st := range out {
		if err := ShellSafe(st.SQL + "\n;\n"); err != nil {
			var le *LineError
			if errors.As(err, &le) {
				return nil, fmt.Errorf("line %d: %s", st.Line+le.Line-1, le.Msg)
			}
			return nil, err
		}
	}
	return out, nil
}

// One splits text that must hold exactly one statement.
func One(text string) (Stmt, error) {
	s, err := Split(text)
	if err != nil {
		return Stmt{}, err
	}
	if len(s) != 1 {
		return Stmt{}, fmt.Errorf("expected one statement, found %d", len(s))
	}
	return s[0], nil
}

func isTrigger(ts []Token) bool {
	var words []string
	for _, t := range ts {
		if t.Significant() {
			if t.Kind != Word {
				break
			}
			words = append(words, t.Upper())
			if len(words) == 6 {
				break
			}
		}
	}
	// sqlite3_complete reads EXPLAIN and EXPLAIN QUERY PLAN before CREATE.
	if len(words) > 0 && words[0] == "EXPLAIN" {
		words = words[1:]
		if len(words) >= 2 && words[0] == "QUERY" && words[1] == "PLAN" {
			words = words[2:]
		}
	}
	if len(words) < 2 || words[0] != "CREATE" {
		return false
	}
	return words[1] == "TRIGGER" || len(words) >= 3 && (words[1] == "TEMP" || words[1] == "TEMPORARY") && words[2] == "TRIGGER"
}

func lastWord(ts []Token) string {
	for i := len(ts) - 1; i >= 0; i-- {
		if ts[i].Significant() {
			if ts[i].Kind == Word {
				return ts[i].Upper()
			}
			return ""
		}
	}
	return ""
}

// LineError is a problem at a line of the text checked.
type LineError struct {
	Line int
	Msg  string
}

func (e *LineError) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Msg) }

// controlBytes refuses bytes the shell's line reader or SQLite's tokenizer
// could read differently from Lex: NUL and other control characters.
func controlBytes(text string) error {
	line := 1
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case c == '\n':
			line++
		case c == '\t' || c == '\r' || c == '\f':
		case c < 0x20 || c == 0x7f:
			return &LineError{line, fmt.Sprintf("control character 0x%02x; SQL text may hold only tabs and line breaks", c)}
		}
	}
	return nil
}

// ShellSafe replays text through the rules the sqlite3 shell reads its
// input by, from a point where no statement is open, and refuses it when
// the shell could run any part of it as one of its own commands:
//   - a line starting with "." or "#" while no statement is open is a
//     shell command or comment;
//   - a line that is only GO or "/" (comments may follow) ends a
//     statement, so text after it could be a command.
//
// The GO and "/" rule is applied everywhere, even inside strings, which
// refuses a little more than the shell would.
func ShellSafe(text string) error {
	if err := controlBytes(text); err != nil {
		return err
	}
	lines := strings.SplitAfter(text, "\n")
	var buf strings.Builder
	for i, ln := range lines {
		n := i + 1
		if !open(buf.String()) && (strings.HasPrefix(ln, ".") || strings.HasPrefix(ln, "#")) {
			return &LineError{n, fmt.Sprintf("starts with %q where no statement is open; the sqlite3 shell would run it as its own command", ln[:1])}
		}
		if terminator(ln) {
			return &LineError{n, "is only GO or /, which the sqlite3 shell reads as the end of a statement"}
		}
		buf.WriteString(ln)
		if !open(buf.String()) {
			buf.Reset()
		}
	}
	return nil
}

// open reports whether text holds the start of a statement that hasn't
// ended: anything but space and comments after the last statement end, or
// a string or comment that is still open.
func open(text string) bool {
	toks, err := Lex(text)
	if err != nil {
		return true
	}
	var cur []Token
	for _, t := range toks {
		if t.Kind == Semi && (!isTrigger(cur) || lastWord(cur) == "END") {
			cur = cur[:0]
			continue
		}
		if t.Significant() {
			cur = append(cur, t)
		}
	}
	return len(cur) > 0
}

// terminator reports whether a line is one the shell takes as the end of
// a statement: GO or "/", then only spaces and comments. A comment that
// doesn't end on the line counts as one, to be safe.
func terminator(ln string) bool {
	t := strings.TrimLeft(ln, " \t\r\f\v\n")
	switch {
	case strings.HasPrefix(t, "/") && !strings.HasPrefix(t, "/*"):
		t = t[1:]
	case len(t) >= 2 && strings.EqualFold(t[:2], "go") && (len(t) == 2 || !isWordPart(t[2])):
		t = t[2:]
	default:
		return false
	}
	for {
		t = strings.TrimLeft(t, " \t\r\f\v\n")
		switch {
		case t == "", strings.HasPrefix(t, "--"):
			return true
		case strings.HasPrefix(t, "/*"):
			end := strings.Index(t[2:], "*/")
			if end < 0 {
				return true
			}
			t = t[2+end+2:]
		default:
			return false
		}
	}
}
