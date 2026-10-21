package schema

import (
	"context"
	"fmt"
	"strings"

	"github.com/aftermarket-dev/aftermarket/internal/sqlitecli"
)

// Problem is something wrong with a mapping, at a line of the mapping
// file when there is one.
type Problem struct {
	Line int
	Msg  string
}

func (p Problem) String() string {
	if p.Line > 0 {
		return fmt.Sprintf("line %d: %s", p.Line, p.Msg)
	}
	return p.Msg
}

// Planned is one view ready to be written: the old columns, in the old
// order, each with the expression that gives it.
type Planned struct {
	Map     *ViewMap
	Columns []string
	Exprs   []string
}

// Plan matches the mapping against the old and new schemas. Every problem
// is listed; views with problems are left out.
func (m *Map) Plan(old, new *Schema) ([]Planned, []Problem) {
	var out []Planned
	var probs []Problem
	for _, v := range m.Views {
		var oldCols []string
		switch {
		case old.Table(v.Old) != nil:
			for _, c := range old.Table(v.Old).Columns {
				oldCols = append(oldCols, c.Name)
			}
		case old.View(v.Old) != nil:
			oldCols = old.View(v.Old).Columns
		default:
			probs = append(probs, Problem{v.Line, fmt.Sprintf("the old schema has no table or view called %s", v.Old)})
			continue
		}
		if new.Table(v.Old) != nil {
			probs = append(probs, Problem{v.Line, fmt.Sprintf("the new schema still has a table called %s, and a view can't take its name (SQLite keeps tables and views in one namespace); old queries on %s read the new table as it is, so check what changed in it with schema diff", v.Old, v.Old)})
			continue
		}
		var fromCols []string
		switch {
		case new.Table(v.From) != nil:
			for _, c := range new.Table(v.From).Columns {
				fromCols = append(fromCols, c.Name)
			}
		case new.View(v.From) != nil && !strings.EqualFold(v.From, v.Old):
			fromCols = new.View(v.From).Columns
		default:
			probs = append(probs, Problem{v.Line, fmt.Sprintf("the new schema has no table or view called %s", v.From)})
			continue
		}
		given := map[string]ColumnMap{}
		bad := false
		for _, c := range v.Columns {
			if !hasName(oldCols, c.Old) {
				probs = append(probs, Problem{c.Line, fmt.Sprintf("%s had no column called %s", v.Old, c.Old)})
				bad = true
			}
			given[strings.ToLower(c.Old)] = c
		}
		p := Planned{Map: v}
		for _, oc := range oldCols {
			if c, ok := given[strings.ToLower(oc)]; ok {
				p.Columns, p.Exprs = append(p.Columns, oc), append(p.Exprs, c.Expr)
				continue
			}
			if hasName(fromCols, oc) {
				p.Columns, p.Exprs = append(p.Columns, oc), append(p.Exprs, Quote(oc))
				continue
			}
			probs = append(probs, Problem{v.Line, fmt.Sprintf("old column %s.%s has no mapping and %s has no column called %s; add it under columns (null for NULL)", v.Old, oc, v.From, oc)})
			bad = true
		}
		if !bad {
			out = append(out, p)
		}
	}
	return out, probs
}

// Verify builds each view as a temporary view on the new schema and asks
// SQLite to compile a read of it, so a wrong expression is found here and
// not by the old app.
func Verify(ctx context.Context, r *sqlitecli.Runner, newSrc sqlitecli.Source, views []Planned) []Problem {
	var probs []Problem
	for _, p := range views {
		setup := createView(p, true)
		if err := r.Check(ctx, newSrc, setup, "SELECT * FROM "+Quote(p.Map.Old)); err != nil {
			msg := err.Error()
			if e, ok := err.(*sqlitecli.Error); ok {
				msg = e.Msg
			}
			probs = append(probs, Problem{p.Map.Line, fmt.Sprintf("the view %s doesn't work on the new schema: %s", p.Map.Old, msg)})
		}
	}
	return probs
}

// SQL writes the views as one script. With temp set they are temporary
// views, for checks on a read-only connection.
func SQL(views []Planned, mapName string, temp bool) string {
	var b strings.Builder
	if !temp {
		fmt.Fprintf(&b, "-- Compatibility views from %s, written by aftermarket schema compat.\n", mapName)
		b.WriteString("-- They show the old tables, read from the new schema, for old queries that read.\n")
		b.WriteString("-- Review them, then apply to the new database, for example: sqlite3 new.db < compat.sql\n\n")
		b.WriteString("BEGIN;\n")
	}
	for _, p := range views {
		if !temp {
			fmt.Fprintf(&b, "\nDROP VIEW IF EXISTS %s;\n", Quote(p.Map.Old))
		}
		b.WriteString(createView(p, temp))
	}
	if !temp {
		b.WriteString("\nCOMMIT;\n")
	}
	return b.String()
}

func createView(p Planned, temp bool) string {
	var b strings.Builder
	kw := "CREATE VIEW"
	if temp {
		kw = "CREATE TEMP VIEW"
	}
	cols := make([]string, len(p.Columns))
	for i, c := range p.Columns {
		cols[i] = Quote(c)
	}
	fmt.Fprintf(&b, "%s %s (%s) AS\n  SELECT", kw, Quote(p.Map.Old), strings.Join(cols, ", "))
	for i, e := range p.Exprs {
		sep := ","
		if i == len(p.Exprs)-1 {
			sep = ""
		}
		fmt.Fprintf(&b, "\n    %s%s", e, sep)
	}
	fmt.Fprintf(&b, "\n  FROM %s", Quote(p.Map.From))
	if p.Map.Where != "" {
		fmt.Fprintf(&b, "\n  WHERE %s", p.Map.Where)
	}
	b.WriteString(";\n")
	return b.String()
}

// Quote writes an SQL identifier in double quotes.
func Quote(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func hasName(list []string, name string) bool {
	for _, n := range list {
		if strings.EqualFold(n, name) {
			return true
		}
	}
	return false
}

// Cover marks the changes that a planned view hides from old queries: an
// old table or view that is gone, now shown by a view of the same name.
func Cover(changes []Change, views []Planned) []Change {
	out := make([]Change, len(changes))
	copy(out, changes)
	for i, c := range out {
		if c.Kind != "table" && c.Kind != "view" {
			continue
		}
		if c.What != "removed" && !(c.What == "changed" && c.Breaking) {
			continue
		}
		for _, p := range views {
			if strings.EqualFold(p.Map.Old, c.Object) {
				out[i].Covered = p.Map.Old
			}
		}
	}
	return out
}
