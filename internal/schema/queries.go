package schema

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/aftermarket-dev/aftermarket/internal/sqlitecli"
)

// Stmt is one statement from a file of queries, with the line it starts on.
type Stmt = sqlitecli.Stmt

// Split cuts SQL text into statements as SQLite does (see sqlitecli.Split).
// Text the sqlite3 shell would read as its own command is refused.
func Split(text string) ([]Stmt, error) { return sqlitecli.Split(text) }

// words returns a statement's keywords and names outside parentheses, in
// upper case.
func words(sql string) []string {
	toks, _ := sqlitecli.Lex(sql)
	var w []string
	for _, t := range toks {
		if t.Kind == sqlitecli.Word && t.Depth == 0 {
			w = append(w, t.Upper())
		}
	}
	return w
}

// IsRead reports whether a statement only reads: a SELECT or VALUES, or a
// WITH whose main statement is a SELECT. EXPLAIN is not a read here, since
// its output describes the plan, which differs between schemas.
func IsRead(sql string) bool {
	toks, _ := sqlitecli.Lex(sql)
	first := ""
	for _, t := range toks {
		if t.Significant() {
			first = t.Upper()
			break
		}
	}
	switch first {
	case "SELECT", "VALUES":
		return true
	case "WITH":
		for _, w := range words(sql) {
			switch w {
			case "INSERT", "UPDATE", "DELETE", "REPLACE":
				return false
			}
		}
		return true
	}
	return false
}

// ordered reports whether a statement asks for an order of its rows: an
// ORDER BY outside parentheses (not in a subquery or window).
func ordered(sql string) bool {
	w := words(sql)
	for i := 0; i+1 < len(w); i++ {
		if w[i] == "ORDER" && w[i+1] == "BY" {
			return true
		}
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
// order, so for it the same rows in another order pass too. When neither
// side gives any rows, the column names are compared on their own.
// Statements that write are skipped.
func Compare(ctx context.Context, r *sqlitecli.Runner, oldSrc, newSrc sqlitecli.Source, setup string, stmts []Stmt) []Result {
	return parallel(stmts, func(s Stmt) Result {
		res := Result{Stmt: s}
		if !IsRead(s.SQL) {
			res.OK, res.Skipped = true, "not a read; only reads are compared"
			return res
		}
		want, err := r.Exact(ctx, oldSrc, "", s.SQL)
		if err != nil {
			res.Problem = "on the old schema: " + message(err)
			return res
		}
		got, err := r.Exact(ctx, newSrc, setup, s.SQL)
		if err != nil {
			res.Problem = "on the new schema: " + message(err)
			return res
		}
		if len(want) == 0 && len(got) == 0 {
			a, errA := columnNames(ctx, r, oldSrc, "", s.SQL)
			b, errB := columnNames(ctx, r, newSrc, setup, s.SQL)
			if errA == nil && errB == nil && !slices.Equal(a, b) {
				res.Problem = fmt.Sprintf("the column names differ: old %s, new %s", strings.Join(a, ", "), strings.Join(b, ", "))
				return res
			}
			res.OK = true
			return res
		}
		if slices.Equal(want, got) || (!ordered(s.SQL) && sameRows(want, got)) {
			res.OK = true
			return res
		}
		res.Problem = firstDifference(want, got)
		return res
	})
}

// columnNames gives the names of the columns a read returns, through a
// temporary view, for results without rows.
func columnNames(ctx context.Context, r *sqlitecli.Runner, src sqlitecli.Source, setup, query string) ([]string, error) {
	rows, err := r.Query(ctx, src, setup+"\n;\nCREATE TEMP VIEW \"aftermarket_columns\" AS "+query, `SELECT name FROM pragma_table_info('aftermarket_columns') ORDER BY cid`)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(rows))
	for i, row := range rows {
		names[i] = sqlitecli.Str(row.Get("name"))
	}
	return names, nil
}

// sameRows reports whether two results have the same rows, counting
// repeats, in any order.
func sameRows(want, got []string) bool {
	if len(want) != len(got) {
		return false
	}
	w, g := slices.Clone(want), slices.Clone(got)
	sort.Strings(w)
	sort.Strings(g)
	return slices.Equal(w, g)
}

func message(err error) string {
	if e, ok := err.(*sqlitecli.Error); ok && e.Where == "the query" {
		return e.Msg
	}
	return err.Error()
}

// split cuts an exact row into its column list and its values.
func split(row string) (cols, vals string) {
	i := strings.Index(row, ") VALUES(")
	if i < 0 {
		return "", row
	}
	return row[:i], "(" + strings.TrimSuffix(row[i+len(") VALUES("):], ";")
}

// firstDifference describes where two results part. Values are shown,
// since they come from the sample data the test was given.
func firstDifference(want, got []string) string {
	for i := 0; i < len(want) || i < len(got); i++ {
		switch {
		case i >= len(want):
			_, b := split(got[i])
			return fmt.Sprintf("the new schema gives more rows; first extra: %s", b)
		case i >= len(got):
			_, a := split(want[i])
			return fmt.Sprintf("the new schema gives fewer rows; first missing: %s", a)
		case want[i] == got[i]:
			continue
		}
		ca, a := split(want[i])
		cb, b := split(got[i])
		if ca != cb {
			return fmt.Sprintf("the column names differ: old %s, new %s", ca, cb)
		}
		return fmt.Sprintf("row %d differs: old %s, new %s", i+1, a, b)
	}
	return "the results differ"
}
