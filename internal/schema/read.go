// Package schema is AfterSchema: it compares SQLite schemas, writes
// compatibility views that make a new schema look like the old one to old
// queries, and checks old queries against the result. Databases are only
// ever read, through the sqlite3 program.
package schema

import (
	"context"
	"sort"
	"strings"

	"github.com/aftermarket-dev/aftermarket/internal/sqlitecli"
)

// Schema is everything a database defines.
type Schema struct {
	Tables   []*Table
	Views    []*View
	Triggers []Named
}

// Table is one table.
type Table struct {
	Name    string
	Columns []Column
	Indexes []Index
	FKs     []FK
	Strict  bool
	NoRowid bool
}

// View is one view and the columns it shows.
type View struct {
	Name    string
	Columns []string
	SQL     string
}

// Named is a trigger or another object known by name and definition.
type Named struct {
	Name, Table, SQL string
}

// Column is one table column.
type Column struct {
	Name      string
	Type      string
	NotNull   bool
	Default   *string // as written in the schema, nil for none
	PK        int     // position in the primary key, 0 when not part of it
	Generated bool
}

// Index is one index on a table.
type Index struct {
	Name    string
	Columns []string
	Unique  bool
	Partial bool
	Origin  string // c (CREATE INDEX), u (UNIQUE), pk
}

// FK is one foreign key.
type FK struct {
	Table    string
	From, To []string
	OnUpdate string
	OnDelete string
}

// Table returns the table called name, ignoring case as SQLite does.
func (s *Schema) Table(name string) *Table {
	for _, t := range s.Tables {
		if strings.EqualFold(t.Name, name) {
			return t
		}
	}
	return nil
}

// View returns the view called name, ignoring case.
func (s *Schema) View(name string) *View {
	for _, v := range s.Views {
		if strings.EqualFold(v.Name, name) {
			return v
		}
	}
	return nil
}

// Column returns the column called name, ignoring case.
func (t *Table) Column(name string) *Column {
	for i := range t.Columns {
		if strings.EqualFold(t.Columns[i].Name, name) {
			return &t.Columns[i]
		}
	}
	return nil
}

// Read reads src's schema.
func Read(ctx context.Context, r *sqlitecli.Runner, src sqlitecli.Source) (*Schema, error) {
	s := &Schema{}
	objs, err := r.Query(ctx, src, "", `SELECT type, name, tbl_name, coalesce(sql, '') AS sql FROM sqlite_schema
 WHERE name NOT LIKE 'sqlite\_%' ESCAPE '\' ORDER BY type, name`)
	if err != nil {
		return nil, err
	}
	tables := map[string]*Table{}
	views := map[string]*View{}
	for _, o := range objs {
		typ, name, sql := sqlitecli.Str(o.Get("type")), sqlitecli.Str(o.Get("name")), sqlitecli.Str(o.Get("sql"))
		switch typ {
		case "table":
			upper := strings.ToUpper(sql)
			t := &Table{Name: name, NoRowid: strings.Contains(upper, "WITHOUT ROWID"), Strict: tableOption(upper, "STRICT")}
			s.Tables = append(s.Tables, t)
			tables[strings.ToLower(name)] = t
		case "view":
			v := &View{Name: name, SQL: sql}
			s.Views = append(s.Views, v)
			views[strings.ToLower(name)] = v
		case "trigger":
			s.Triggers = append(s.Triggers, Named{Name: name, Table: sqlitecli.Str(o.Get("tbl_name")), SQL: sql})
		}
	}
	cols, err := r.Query(ctx, src, "", `SELECT m.type AS kind, m.name AS tbl, p.name, p.type, p."notnull" AS nn, p.dflt_value AS dflt, p.pk, p.hidden
 FROM sqlite_schema m JOIN pragma_table_xinfo(m.name) p
 WHERE m.type IN ('table', 'view') AND m.name NOT LIKE 'sqlite\_%' ESCAPE '\' ORDER BY m.name, p.cid`)
	if err != nil {
		return nil, err
	}
	for _, c := range cols {
		tbl := strings.ToLower(sqlitecli.Str(c.Get("tbl")))
		if v := views[tbl]; v != nil {
			v.Columns = append(v.Columns, sqlitecli.Str(c.Get("name")))
			continue
		}
		t := tables[tbl]
		if t == nil {
			continue
		}
		col := Column{
			Name:      sqlitecli.Str(c.Get("name")),
			Type:      sqlitecli.Str(c.Get("type")),
			NotNull:   sqlitecli.Int(c.Get("nn")) != 0,
			PK:        int(sqlitecli.Int(c.Get("pk"))),
			Generated: sqlitecli.Int(c.Get("hidden")) >= 2,
		}
		if d := c.Get("dflt"); d != nil {
			v := sqlitecli.Str(d)
			col.Default = &v
		}
		t.Columns = append(t.Columns, col)
	}
	idx, err := r.Query(ctx, src, "", `SELECT m.name AS tbl, il.name AS idx, il."unique" AS uq, il.origin, il.partial, ii.name AS col
 FROM sqlite_schema m JOIN pragma_index_list(m.name) il JOIN pragma_index_info(il.name) ii
 WHERE m.type = 'table' AND m.name NOT LIKE 'sqlite\_%' ESCAPE '\' ORDER BY m.name, il.name, ii.seqno`)
	if err != nil {
		return nil, err
	}
	for _, c := range idx {
		t := tables[strings.ToLower(sqlitecli.Str(c.Get("tbl")))]
		if t == nil {
			continue
		}
		name := sqlitecli.Str(c.Get("idx"))
		if n := len(t.Indexes); n == 0 || t.Indexes[n-1].Name != name {
			t.Indexes = append(t.Indexes, Index{Name: name, Unique: sqlitecli.Int(c.Get("uq")) != 0,
				Partial: sqlitecli.Int(c.Get("partial")) != 0, Origin: sqlitecli.Str(c.Get("origin"))})
		}
		t.Indexes[len(t.Indexes)-1].Columns = append(t.Indexes[len(t.Indexes)-1].Columns, sqlitecli.Str(c.Get("col")))
	}
	fks, err := r.Query(ctx, src, "", `SELECT m.name AS tbl, f.id, f."table" AS ref, f."from" AS c_from, f."to" AS c_to, f.on_update, f.on_delete
 FROM sqlite_schema m JOIN pragma_foreign_key_list(m.name) f
 WHERE m.type = 'table' AND m.name NOT LIKE 'sqlite\_%' ESCAPE '\' ORDER BY m.name, f.id, f.seq`)
	if err != nil {
		return nil, err
	}
	lastID := map[*Table]int64{}
	for _, c := range fks {
		t := tables[strings.ToLower(sqlitecli.Str(c.Get("tbl")))]
		if t == nil {
			continue
		}
		id := sqlitecli.Int(c.Get("id"))
		if prev, ok := lastID[t]; !ok || prev != id || len(t.FKs) == 0 {
			t.FKs = append(t.FKs, FK{Table: sqlitecli.Str(c.Get("ref")), OnUpdate: sqlitecli.Str(c.Get("on_update")), OnDelete: sqlitecli.Str(c.Get("on_delete"))})
			lastID[t] = id
		}
		fk := &t.FKs[len(t.FKs)-1]
		fk.From = append(fk.From, sqlitecli.Str(c.Get("c_from")))
		fk.To = append(fk.To, sqlitecli.Str(c.Get("c_to")))
	}
	sort.SliceStable(s.Tables, func(i, j int) bool { return strings.ToLower(s.Tables[i].Name) < strings.ToLower(s.Tables[j].Name) })
	return s, nil
}

// tableOption reports whether a CREATE TABLE statement ends with opt among
// its table options (after the closing parenthesis).
func tableOption(upper, opt string) bool {
	i := strings.LastIndex(upper, ")")
	if i < 0 {
		return false
	}
	for _, o := range strings.Split(upper[i+1:], ",") {
		if strings.TrimSpace(o) == opt {
			return true
		}
	}
	return false
}
