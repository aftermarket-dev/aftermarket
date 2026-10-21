package schema

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/aftermarket-dev/aftermarket/internal/sqlitecli"
)

// Stmt is one statement from a file of queries, with the line it starts on.
type Stmt struct {
	Line int
	SQL  string
}

// Split cuts SQL text into statements at semicolons, skipping those inside
// strings, quoted names and comments. Statements that are only comments
// are left out.
func Split(text string) []Stmt {
	var out []Stmt
	start, line, startLine := 0, 1, 0
	flush := func(end int) {
		body := strings.TrimSpace(text[start:end])
		if startLine > 0 && body != "" {
			out = append(out, Stmt{Line: startLine, SQL: body})
		}
		start, startLine = end+1, 0
	}
	for i := 0; i < len(text); i++ {
		c := text[i]
		switch {
		case c == '\n':
			line++
		case c == '-' && strings.HasPrefix(text[i:], "--"):
			for i < len(text) && text[i] != '\n' {
				i++
			}
			line++
		case c == '/' && strings.HasPrefix(text[i:], "/*"):
			end := strings.Index(text[i+2:], "*/")
			if end < 0 {
				end = len(text) - i - 2
			}
			line += strings.Count(text[i:i+2+end], "\n")
			i += 2 + end + 1
		case c == '\'' || c == '"' || c == '`' || c == '[':
			if startLine == 0 {
				startLine = line
			}
			closer := c
			if c == '[' {
				closer = ']'
			}
			j := i + 1
			for j < len(text) {
				if text[j] == closer {
					if closer != ']' && j+1 < len(text) && text[j+1] == closer {
						j += 2
						continue
					}
					break
				}
				j++
			}
			line += strings.Count(text[i:min(j+1, len(text))], "\n")
			i = j
		case c == ';':
			flush(i)
		case c == ' ' || c == '\t' || c == '\r':
		default:
			if startLine == 0 {
				startLine = line
			}
		}
	}
	flush(len(text))
	// Comments before a statement were skipped when finding its first line,
	// but its text keeps them; trim leading comment lines for readability.
	for i := range out {
		out[i].SQL = stripLeadingComments(out[i].SQL)
	}
	return out
}

func stripLeadingComments(s string) string {
	for {
		s = strings.TrimSpace(s)
		switch {
		case strings.HasPrefix(s, "--"):
			if j := strings.IndexByte(s, '\n'); j >= 0 {
				s = s[j+1:]
				continue
			}
			return ""
		case strings.HasPrefix(s, "/*"):
			if j := strings.Index(s, "*/"); j >= 0 {
				s = s[j+2:]
				continue
			}
			return ""
		}
		return s
	}
}

// IsRead reports whether a statement only reads: SELECT, WITH or VALUES.
func IsRead(s string) bool {
	f := strings.Fields(s)
	if len(f) == 0 {
		return false
	}
	switch strings.ToUpper(f[0]) {
	case "SELECT", "WITH", "VALUES":
		return true
	}
	return false
}

// Result is the outcome for one statement.
type Result struct {
	Stmt
	OK      bool
	Skipped string // why it wasn't compared (test)
	Problem string
}

// parallel runs f over stmts, four at a time, keeping their order.
func parallel(stmts []Stmt, f func(Stmt) Result) []Result {
	out := make([]Result, len(stmts))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, s := range stmts {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			out[i] = f(s)
		}()
	}
	wg.Wait()
	return out
}

// Check asks SQLite to compile each statement against src with setup (the
// compatibility views, as temporary views) without running it.
func Check(ctx context.Context, r *sqlitecli.Runner, src sqlitecli.Source, setup string, stmts []Stmt) []Result {
	return parallel(stmts, func(s Stmt) Result {
		res := Result{Stmt: s, OK: true}
		if err := r.Check(ctx, src, setup, s.SQL); err != nil {
			res.OK, res.Problem = false, message(err)
		}
		return res
	})
}

// Compare runs each read on the old schema and on the new one with setup,
// and passes it when both give the same rows, column names, values and
// types alike, in the same order. A query with no ORDER BY promises no
// order, so for it the same rows in another order pass too. Statements
// that write are skipped.
func Compare(ctx context.Context, r *sqlitecli.Runner, oldSrc, newSrc sqlitecli.Source, setup string, stmts []Stmt) []Result {
	return parallel(stmts, func(s Stmt) Result {
		res := Result{Stmt: s}
		if !IsRead(s.SQL) {
			res.OK, res.Skipped = true, "not a read; only reads are compared"
			return res
		}
		want, err := r.Quoted(ctx, oldSrc, "", s.SQL)
		if err != nil {
			res.Problem = "on the old schema: " + message(err)
			return res
		}
		got, err := r.Quoted(ctx, newSrc, setup, s.SQL)
		if err != nil {
			res.Problem = "on the new schema: " + message(err)
			return res
		}
		if want == got || (!orderBy.MatchString(s.SQL) && sameRows(want, got)) {
			res.OK = true
			return res
		}
		res.Problem = firstDifference(want, got)
		return res
	})
}

var orderBy = regexp.MustCompile(`(?i)\border\s+by\b`)

// sameRows reports whether two quoted results have the same header and the
// same rows, counting repeats, in any order.
func sameRows(want, got string) bool {
	w := strings.Split(strings.TrimSuffix(want, "\n"), "\n")
	g := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(w) != len(g) || w[0] != g[0] {
		return false
	}
	sort.Strings(w[1:])
	sort.Strings(g[1:])
	return slices.Equal(w, g)
}

func message(err error) string {
	if e, ok := err.(*sqlitecli.Error); ok && e.Where == "the query" {
		return e.Msg
	}
	return err.Error()
}

// firstDifference describes where two quoted results part. Values are
// shown, since they come from the sample data the test was given.
func firstDifference(want, got string) string {
	w := strings.Split(strings.TrimSuffix(want, "\n"), "\n")
	g := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	for i := 0; i < len(w) || i < len(g); i++ {
		var a, b string
		if i < len(w) {
			a = w[i]
		}
		if i < len(g) {
			b = g[i]
		}
		if a == b {
			continue
		}
		what := fmt.Sprintf("row %d differs", i)
		if i == 0 {
			what = "the column names differ"
		}
		switch {
		case i >= len(w):
			return fmt.Sprintf("the new schema gives more rows; first extra: %s", b)
		case i >= len(g):
			return fmt.Sprintf("the new schema gives fewer rows; first missing: %s", a)
		}
		return fmt.Sprintf("%s: old %s, new %s", what, a, b)
	}
	return "the results differ"
}
