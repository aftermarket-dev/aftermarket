package sqlitecli

import (
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
// Text the sqlite3 shell would read as one of its own commands is refused.
func Split(text string) ([]Stmt, error) {
	toks, err := Lex(text)
	if err != nil {
		return nil, err
	}
	if err := shellSafe(text, toks); err != nil {
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
			if len(words) == 3 {
				break
			}
		}
	}
	if len(words) < 2 || words[0] != "CREATE" {
		return false
	}
	return words[1] == "TRIGGER" || len(words) == 3 && (words[1] == "TEMP" || words[1] == "TEMPORARY") && words[2] == "TRIGGER"
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

// shellSafe refuses lines the sqlite3 shell would take as its own: a line
// that is only GO or / (it ends a statement) anywhere outside a string or
// comment, and a line starting with . or # where no statement is open
// (a shell command or comment).
func shellSafe(text string, toks []Token) error {
	pending := false // a statement has significant text since the last ;
	off := 0
	lineStart := map[int]bool{0: true}
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			lineStart[i+1] = true
		}
	}
	trig := []Token{}
	for _, t := range toks {
		// Check every line that starts within this token.
		for p := off; p < off+len(t.Text); p++ {
			if !lineStart[p] {
				continue
			}
			lineEnd := strings.IndexByte(text[p:], '\n')
			ln := text[p:]
			if lineEnd >= 0 {
				ln = text[p : p+lineEnd]
			}
			trimmed := strings.TrimSpace(ln)
			atStart := p == off
			inStringOrComment := !atStart && (t.Kind == String || t.Kind == Comment)
			if !inStringOrComment && (trimmed == "/" || strings.EqualFold(trimmed, "go")) {
				return fmt.Errorf("line %d is only %q, which the sqlite3 shell reads as the end of a statement", lineOf(text, p), trimmed)
			}
			if !pending && (strings.HasPrefix(ln, ".") || strings.HasPrefix(ln, "#")) {
				return fmt.Errorf("line %d starts with %q between statements, which the sqlite3 shell reads as its own command", lineOf(text, p), ln[:1])
			}
		}
		off += len(t.Text)
		switch {
		case t.Kind == Semi && (!isTrigger(trig) || lastWord(trig) == "END"):
			pending = false
			trig = trig[:0]
		case t.Significant():
			pending = true
			trig = append(trig, t)
		}
	}
	return nil
}

func lineOf(text string, off int) int { return strings.Count(text[:off], "\n") + 1 }
