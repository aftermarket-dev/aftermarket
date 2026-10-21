package schema

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aftermarket-dev/aftermarket/internal/rules"
	"github.com/aftermarket-dev/aftermarket/internal/yamlite"
)

// Map describes the old tables in terms of the new schema.
type Map struct {
	Path  string
	Old   string // the old schema: a database or a .sql file
	New   string // the new schema, used when no other is given
	Views []*ViewMap
}

// ViewMap is one compatibility view: an old table read from a new table.
type ViewMap struct {
	Line    int
	Old     string // the name old queries use
	From    string // the new table or view it reads
	Where   string // optional filter, SQL
	Columns []ColumnMap
}

// ColumnMap is one old column and the SQL expression over From that gives
// it. A bare column name is an expression too.
type ColumnMap struct {
	Line int
	Old  string
	Expr string
}

// LoadMap reads a mapping file. Paths in it are relative to its folder.
func LoadMap(path string) (*Map, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m, err := ParseMap(data, filepath.Dir(path))
	if m != nil {
		m.Path = path
	}
	return m, err
}

// ParseMap reads a mapping file's text; dir resolves its paths.
func ParseMap(data []byte, dir string) (*Map, error) {
	root, err := yamlite.Parse(data)
	if err != nil {
		return nil, err
	}
	var errs rules.Errors
	errf := func(line int, format string, args ...any) {
		errs = append(errs, &yamlite.Error{Line: line, Msg: fmt.Sprintf(format, args...)})
	}
	if root.Kind != yamlite.Map {
		return nil, &yamlite.Error{Line: root.Line, Msg: "a mapping file is a map with version, old and views"}
	}
	m := &Map{}
	resolve := func(p string) string {
		if p == "" || filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(dir, p)
	}
	for _, p := range root.Pairs {
		switch p.Key {
		case "version":
			if p.Value.Kind != yamlite.Scalar || p.Value.Value != "1" {
				errf(p.KeyLine, "version must be 1")
			}
		case "old", "new":
			if p.Value.Kind != yamlite.Scalar || p.Value.Value == "" {
				errf(p.KeyLine, "%s must be a file: a database or a .sql file", p.Key)
				continue
			}
			if p.Key == "old" {
				m.Old = resolve(p.Value.Value)
			} else {
				m.New = resolve(p.Value.Value)
			}
		case "views":
			if p.Value.Kind != yamlite.Seq {
				errf(p.KeyLine, "views must be a list")
				continue
			}
			for _, it := range p.Value.Items {
				if v := parseView(it, errf); v != nil {
					m.Views = append(m.Views, v)
				}
			}
		default:
			errf(p.KeyLine, "unknown key %q (use version, old, new and views)", p.Key)
		}
	}
	if m.Old == "" {
		errf(root.Line, "old is required: the old schema, so the views show the old columns in their old order")
	}
	if len(m.Views) == 0 && len(errs) == 0 {
		errf(root.Line, "views is required: at least one old table to show")
	}
	seen := map[string]int{}
	for _, v := range m.Views {
		if first, ok := seen[strings.ToLower(v.Old)]; ok {
			errf(v.Line, "%s is already mapped on line %d", v.Old, first)
		}
		seen[strings.ToLower(v.Old)] = v.Line
	}
	if len(errs) > 0 {
		return m, errs
	}
	return m, nil
}

func parseView(n *yamlite.Node, errf func(int, string, ...any)) *ViewMap {
	if n.Kind != yamlite.Map {
		errf(n.Line, "each view is a map with old, from and columns")
		return nil
	}
	v := &ViewMap{Line: n.Line}
	for _, p := range n.Pairs {
		switch p.Key {
		case "old", "from", "where":
			if p.Value.Kind != yamlite.Scalar || strings.TrimSpace(p.Value.Value) == "" {
				errf(p.KeyLine, "%s must be text", p.Key)
				continue
			}
			switch p.Key {
			case "old":
				v.Old = p.Value.Value
			case "from":
				v.From = p.Value.Value
			case "where":
				v.Where = p.Value.Value
			}
		case "columns":
			if p.Value.Kind == yamlite.Null {
				continue
			}
			if p.Value.Kind != yamlite.Map {
				errf(p.KeyLine, "columns must be a map of old column: SQL expression over %s", orFrom(v.From))
				continue
			}
			seen := map[string]bool{}
			for _, c := range p.Value.Pairs {
				if seen[strings.ToLower(c.Key)] {
					errf(c.KeyLine, "column %s is listed twice", c.Key)
				}
				seen[strings.ToLower(c.Key)] = true
				switch c.Value.Kind {
				case yamlite.Null:
					v.Columns = append(v.Columns, ColumnMap{Line: c.KeyLine, Old: c.Key, Expr: "NULL"})
				case yamlite.Scalar:
					if strings.TrimSpace(c.Value.Value) == "" {
						errf(c.KeyLine, "column %s needs an SQL expression (write null for NULL)", c.Key)
						continue
					}
					if strings.Contains(c.Value.Value, ";") && !inQuotes(c.Value.Value, ";") {
						errf(c.KeyLine, "column %s: one expression, without ;", c.Key)
						continue
					}
					v.Columns = append(v.Columns, ColumnMap{Line: c.KeyLine, Old: c.Key, Expr: c.Value.Value})
				default:
					errf(c.KeyLine, "column %s needs an SQL expression as text", c.Key)
				}
			}
		default:
			errf(p.KeyLine, "unknown key %q in a view (use old, from, columns and where)", p.Key)
		}
	}
	if v.Old == "" {
		errf(n.Line, "a view needs old: the table name old queries use")
	}
	if v.From == "" {
		errf(n.Line, "a view needs from: the new table it reads")
	}
	if v.Where != "" && strings.Contains(v.Where, ";") && !inQuotes(v.Where, ";") {
		errf(n.Line, "where: one condition, without ;")
	}
	return v
}

func orFrom(s string) string {
	if s == "" {
		return "the new table"
	}
	return s
}

// inQuotes reports whether every occurrence of sub in s sits inside an SQL
// string literal.
func inQuotes(s, sub string) bool {
	in := false
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			in = !in
			continue
		}
		if !in && strings.HasPrefix(s[i:], sub) {
			return false
		}
	}
	return true
}
