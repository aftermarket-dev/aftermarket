package schema

import (
	"fmt"
	"strings"
)

// Change is one difference between two schemas. Breaking means an old
// query could fail, or read a different kind of value, because of it.
type Change struct {
	Object   string `json:"object"` // "customers", "orders.total"
	Kind     string `json:"kind"`   // table, column, view, index, foreign key, trigger
	What     string `json:"what"`   // removed, added, changed
	Detail   string `json:"detail,omitempty"`
	Breaking bool   `json:"breaking"`
	Covered  string `json:"covered_by,omitempty"` // a compatibility view that hides it
}

func (c Change) String() string {
	s := fmt.Sprintf("%s %s %s", c.Kind, c.Object, c.What)
	if c.Detail != "" {
		s += " (" + c.Detail + ")"
	}
	if c.Covered != "" {
		s += ", covered by the compatibility view " + c.Covered
	}
	return s
}

// Diff lists every change from old to new, tables in name order.
func Diff(old, new *Schema) []Change {
	var out []Change
	add := func(c Change) { out = append(out, c) }
	for _, ot := range old.Tables {
		nt := new.Table(ot.Name)
		if nt == nil {
			if new.View(ot.Name) != nil {
				add(Change{Object: ot.Name, Kind: "table", What: "changed", Detail: "now a view", Breaking: false})
				continue
			}
			add(Change{Object: ot.Name, Kind: "table", What: "removed", Breaking: true})
			continue
		}
		diffTable(ot, nt, add)
	}
	for _, nt := range new.Tables {
		if old.Table(nt.Name) == nil && old.View(nt.Name) == nil {
			add(Change{Object: nt.Name, Kind: "table", What: "added", Detail: columnList(nt)})
		}
	}
	for _, ov := range old.Views {
		nv := new.View(ov.Name)
		if nv == nil {
			if nt := new.Table(ov.Name); nt != nil {
				add(Change{Object: ov.Name, Kind: "view", What: "changed", Detail: "now a table", Breaking: missingColumns(ov.Columns, tableColumnNames(nt)) != ""})
				continue
			}
			add(Change{Object: ov.Name, Kind: "view", What: "removed", Breaking: true})
			continue
		}
		if gone := missingColumns(ov.Columns, nv.Columns); gone != "" {
			add(Change{Object: ov.Name, Kind: "view", What: "changed", Detail: "no longer shows " + gone, Breaking: true})
		} else if normSQL(ov.SQL) != normSQL(nv.SQL) {
			add(Change{Object: ov.Name, Kind: "view", What: "changed", Detail: "definition changed; same columns"})
		}
	}
	for _, nv := range new.Views {
		if old.View(nv.Name) == nil && old.Table(nv.Name) == nil {
			add(Change{Object: nv.Name, Kind: "view", What: "added"})
		}
	}
	oldTrig := map[string]Named{}
	for _, t := range old.Triggers {
		oldTrig[strings.ToLower(t.Name)] = t
	}
	for _, t := range new.Triggers {
		if o, ok := oldTrig[strings.ToLower(t.Name)]; !ok {
			add(Change{Object: t.Name, Kind: "trigger", What: "added", Detail: "on " + t.Table})
		} else if normSQL(o.SQL) != normSQL(t.SQL) {
			add(Change{Object: t.Name, Kind: "trigger", What: "changed", Detail: "on " + t.Table})
		}
		delete(oldTrig, strings.ToLower(t.Name))
	}
	for _, t := range old.Triggers {
		if _, gone := oldTrig[strings.ToLower(t.Name)]; gone {
			add(Change{Object: t.Name, Kind: "trigger", What: "removed", Detail: "on " + t.Table})
		}
	}
	return out
}

func diffTable(ot, nt *Table, add func(Change)) {
	for _, oc := range ot.Columns {
		obj := ot.Name + "." + oc.Name
		nc := nt.Column(oc.Name)
		if nc == nil {
			add(Change{Object: obj, Kind: "column", What: "removed", Breaking: true})
			continue
		}
		var details []string
		breaking := false
		if !strings.EqualFold(strings.TrimSpace(oc.Type), strings.TrimSpace(nc.Type)) {
			if affinity(oc.Type) != affinity(nc.Type) {
				breaking = true
				details = append(details, fmt.Sprintf("type %s -> %s, values read differently", typeName(oc.Type), typeName(nc.Type)))
			} else {
				details = append(details, fmt.Sprintf("declared type %s -> %s, same affinity", typeName(oc.Type), typeName(nc.Type)))
			}
		}
		if oc.NotNull && !nc.NotNull {
			breaking = true
			details = append(details, "may now be NULL")
		}
		if !oc.NotNull && nc.NotNull {
			details = append(details, "now NOT NULL; writes of NULL fail")
		}
		if def(oc.Default) != def(nc.Default) {
			details = append(details, fmt.Sprintf("default %s -> %s", def(oc.Default), def(nc.Default)))
		}
		if (oc.PK > 0) != (nc.PK > 0) {
			if nc.PK > 0 {
				details = append(details, "now part of the primary key")
			} else {
				details = append(details, "no longer part of the primary key")
			}
		}
		if oc.Generated != nc.Generated {
			if nc.Generated {
				details = append(details, "now generated; writes to it fail")
			} else {
				details = append(details, "no longer generated")
			}
		}
		if len(details) > 0 {
			add(Change{Object: obj, Kind: "column", What: "changed", Detail: strings.Join(details, "; "), Breaking: breaking})
		}
	}
	for _, nc := range nt.Columns {
		if ot.Column(nc.Name) == nil {
			d := typeName(nc.Type)
			if nc.NotNull && nc.Default == nil && !nc.Generated && nc.PK == 0 {
				d += " NOT NULL with no default; old INSERTs that don't name it fail"
			}
			add(Change{Object: nt.Name + "." + nc.Name, Kind: "column", What: "added", Detail: d})
		}
	}
	if !ot.NoRowid && nt.NoRowid {
		add(Change{Object: nt.Name, Kind: "table", What: "changed", Detail: "now WITHOUT ROWID; queries using rowid fail", Breaking: true})
	}
	if ot.NoRowid && !nt.NoRowid {
		add(Change{Object: nt.Name, Kind: "table", What: "changed", Detail: "now has a rowid"})
	}
	if ot.Strict != nt.Strict {
		d := "now STRICT; writes of the wrong type fail"
		if !nt.Strict {
			d = "no longer STRICT"
		}
		add(Change{Object: nt.Name, Kind: "table", What: "changed", Detail: d})
	}
	oldIdx := map[string]Index{}
	for _, i := range ot.Indexes {
		oldIdx[indexKey(i)] = i
	}
	newIdx := map[string]Index{}
	for _, i := range nt.Indexes {
		newIdx[indexKey(i)] = i
	}
	for _, i := range ot.Indexes {
		if _, ok := newIdx[indexKey(i)]; !ok {
			add(Change{Object: ot.Name + "(" + strings.Join(i.Columns, ", ") + ")", Kind: "index", What: "removed", Detail: indexDetail(i)})
		}
	}
	for _, i := range nt.Indexes {
		if _, ok := oldIdx[indexKey(i)]; !ok {
			d := indexDetail(i)
			if i.Unique {
				d += "; writes of duplicates fail"
			}
			add(Change{Object: nt.Name + "(" + strings.Join(i.Columns, ", ") + ")", Kind: "index", What: "added", Detail: d})
		}
	}
	oldFK := map[string]bool{}
	for _, f := range ot.FKs {
		oldFK[fkKey(f)] = true
	}
	newFK := map[string]bool{}
	for _, f := range nt.FKs {
		newFK[fkKey(f)] = true
	}
	for _, f := range ot.FKs {
		if !newFK[fkKey(f)] {
			add(Change{Object: ot.Name + "(" + strings.Join(f.From, ", ") + ")", Kind: "foreign key", What: "removed", Detail: "to " + f.Table})
		}
	}
	for _, f := range nt.FKs {
		if !oldFK[fkKey(f)] {
			add(Change{Object: nt.Name + "(" + strings.Join(f.From, ", ") + ")", Kind: "foreign key", What: "added", Detail: "to " + f.Table})
		}
	}
}

// affinity is SQLite's type affinity for a declared type (section 3.1 of
// "Datatypes In SQLite").
func affinity(t string) string {
	u := strings.ToUpper(t)
	switch {
	case strings.Contains(u, "INT"):
		return "INTEGER"
	case strings.Contains(u, "CHAR"), strings.Contains(u, "CLOB"), strings.Contains(u, "TEXT"):
		return "TEXT"
	case strings.Contains(u, "BLOB"), strings.TrimSpace(u) == "":
		return "BLOB"
	case strings.Contains(u, "REAL"), strings.Contains(u, "FLOA"), strings.Contains(u, "DOUB"):
		return "REAL"
	}
	return "NUMERIC"
}

func typeName(t string) string {
	if strings.TrimSpace(t) == "" {
		return "no type"
	}
	return t
}

func def(d *string) string {
	if d == nil {
		return "none"
	}
	return *d
}

func indexKey(i Index) string {
	return strings.ToLower(strings.Join(i.Columns, ",")) + fmt.Sprintf("|%v|%v", i.Unique, i.Partial)
}

func indexDetail(i Index) string {
	d := "index"
	if i.Unique {
		d = "unique index"
	}
	if i.Partial {
		d = "partial " + d
	}
	if i.Origin == "pk" {
		d = "primary key"
	}
	return d
}

func fkKey(f FK) string {
	return strings.ToLower(f.Table + "|" + strings.Join(f.From, ",") + "|" + strings.Join(f.To, ","))
}

func columnList(t *Table) string {
	var names []string
	for _, c := range t.Columns {
		names = append(names, c.Name)
	}
	return strings.Join(names, ", ")
}

func tableColumnNames(t *Table) []string {
	var names []string
	for _, c := range t.Columns {
		names = append(names, c.Name)
	}
	return names
}

// missingColumns lists the names in old that new lacks.
func missingColumns(old, new []string) string {
	have := map[string]bool{}
	for _, n := range new {
		have[strings.ToLower(n)] = true
	}
	var gone []string
	for _, o := range old {
		if !have[strings.ToLower(o)] {
			gone = append(gone, o)
		}
	}
	return strings.Join(gone, ", ")
}

// normSQL compares definitions without caring about spacing or case.
func normSQL(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// Breaking counts the breaking changes nothing covers.
func Breaking(cs []Change) int {
	n := 0
	for _, c := range cs {
		if c.Breaking && c.Covered == "" {
			n++
		}
	}
	return n
}
