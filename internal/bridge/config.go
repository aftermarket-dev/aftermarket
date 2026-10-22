// Package bridge is AfterBridge: a read-only JSON API over an old app's CSV
// exports and SQLite files. Each source is read into a typed snapshot in
// memory, read again when its file changes, and served from there; a file
// that is half written or broken is never served, and nothing a request
// sends ever reaches SQL or the file system.
package bridge

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/aftermarket-dev/aftermarket/internal/rules"
	"github.com/aftermarket-dev/aftermarket/internal/sqlitecli"
	"github.com/aftermarket-dev/aftermarket/internal/yamlite"
)

// Defaults.
const (
	DefaultMaxRows    = 100000
	DefaultMaxFile    = 64 << 20
	DefaultInterval   = 2 * time.Second
	DefaultLimit      = 100
	DefaultMaxLimit   = 1000
	DefaultSQLTimeout = 30 * time.Second
	DefaultSettle     = 2 * time.Second
)

// Type is a declared column type.
type Type string

// Column types.
const (
	Text    Type = "text"
	Integer Type = "integer"
	Number  Type = "number"
	Boolean Type = "boolean"
	Date    Type = "date"
)

// Config is a loaded bridge config.
type Config struct {
	Path      string
	Dir       string // the config's folder; source paths are relative to it
	MaxRows   int
	MaxFile   int64
	Interval  time.Duration // how often sources are checked for changes
	Settle    time.Duration // how long a CSV file must be left alone before it is read
	Limit     int           // default page size
	MaxLimit  int
	Sources   []*Source
	Endpoints []*Endpoint
}

// Source is one place data comes from.
type Source struct {
	ID   string
	Line int
	// CSV: File, or the last file by name in Folder matching Pattern.
	File      string
	Folder    string
	Pattern   string
	Delimiter rune
	// SQLite: a database file (or .sql dump) and a table or a query.
	DB    string
	Table string
	Query string

	Key     string          // the column item endpoints look up
	Columns []ColumnType    // declared types, in the order given
	types   map[string]Type // by column name
	Exclude map[string]bool // columns never served
}

// ColumnType is a declared column.
type ColumnType struct {
	Name string
	Type Type
}

// IsCSV reports whether the source is a CSV file or folder.
func (s *Source) IsCSV() bool { return s.DB == "" }

// TypeOf returns the declared type of a column, or "" when undeclared.
func (s *Source) TypeOf(col string) Type { return s.types[col] }

// Endpoint is one URL the bridge answers.
type Endpoint struct {
	Line    int
	Path    string   // as written: /orders or /orders/{id}
	Prefix  []string // fixed segments
	Param   string   // the {name} at the end, for an item endpoint
	Source  *Source
	Fields  []Field  // what is shown, in order; empty means every column
	Filters []string // field names a list can be filtered by
}

// Field is one column shown under a name.
type Field struct {
	Name   string // in the JSON
	Column string // in the source
}

// IsItem reports whether the endpoint returns one row by key.
func (e *Endpoint) IsItem() bool { return e.Param != "" }

// LoadFile reads a bridge config.
func LoadFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	return Parse(data, filepath.Dir(abs), path)
}

type loader struct{ errs rules.Errors }

func (l *loader) errf(line int, format string, args ...any) {
	l.errs = append(l.errs, &rules.LineError{Line: line, Msg: fmt.Sprintf(format, args...)})
}

func (l *loader) keys(n *yamlite.Node, what string, allowed ...string) {
	for _, p := range n.Pairs {
		ok := false
		for _, a := range allowed {
			if p.Key == a {
				ok = true
			}
		}
		if !ok {
			l.errf(p.KeyLine, "unknown key %q in %s (use %s)", p.Key, what, strings.Join(allowed, ", "))
		}
	}
}

func (l *loader) text(p yamlite.Pair) (string, bool) {
	if p.Value.Kind != yamlite.Scalar || strings.TrimSpace(p.Value.Value) == "" {
		l.errf(p.KeyLine, "%s must be text", p.Key)
		return "", false
	}
	return p.Value.Value, true
}

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
var segPattern = regexp.MustCompile(`^[A-Za-z0-9._~-]+$`)
var paramPattern = regexp.MustCompile(`^\{([A-Za-z_][A-Za-z0-9_]*)\}$`)

// Parse parses config YAML. dir is where relative source paths start.
func Parse(data []byte, dir, path string) (*Config, error) {
	root, err := yamlite.Parse(data)
	if err != nil {
		return nil, err
	}
	l := &loader{}
	cfg := &Config{Path: path, Dir: dir, MaxRows: DefaultMaxRows, MaxFile: DefaultMaxFile, Interval: DefaultInterval, Settle: DefaultSettle, Limit: DefaultLimit, MaxLimit: DefaultMaxLimit}
	if root.Kind != yamlite.Map {
		return nil, fmt.Errorf("a bridge config is a mapping with sources: and endpoints:")
	}
	l.keys(root, "the config", "version", "limits", "sources", "endpoints")
	if v := root.Get("version"); v != nil && (v.Kind != yamlite.Scalar || v.Value != "1") {
		l.errf(v.Line, "version must be 1")
	}
	if lim := root.Get("limits"); lim != nil {
		l.limits(lim, cfg)
	}
	srcs := root.Get("sources")
	if srcs == nil || srcs.Kind != yamlite.Seq || len(srcs.Items) == 0 {
		l.errf(root.Line, "sources: must list at least one source")
	} else {
		seen := map[string]int{}
		for _, n := range srcs.Items {
			s := l.source(n, dir)
			if s == nil {
				continue
			}
			if first, ok := seen[s.ID]; ok {
				l.errf(s.Line, "source %s is already defined on line %d", s.ID, first)
				continue
			}
			seen[s.ID] = s.Line
			cfg.Sources = append(cfg.Sources, s)
		}
	}
	eps := root.Get("endpoints")
	if eps == nil || eps.Kind != yamlite.Seq || len(eps.Items) == 0 {
		l.errf(root.Line, "endpoints: must list at least one endpoint")
	} else {
		seen := map[string]int{}
		for _, n := range eps.Items {
			e := l.endpoint(n, cfg)
			if e == nil {
				continue
			}
			shape := strings.Join(e.Prefix, "/")
			if e.IsItem() {
				shape += "/{}"
			}
			if first, ok := seen[shape]; ok {
				l.errf(e.Line, "%s answers the same URLs as the endpoint on line %d", e.Path, first)
				continue
			}
			seen[shape] = e.Line
			cfg.Endpoints = append(cfg.Endpoints, e)
		}
	}
	if len(l.errs) > 0 {
		return cfg, l.errs
	}
	return cfg, nil
}

func (l *loader) limits(n *yamlite.Node, cfg *Config) {
	if n.Kind != yamlite.Map {
		l.errf(n.Line, "limits must be a mapping")
		return
	}
	l.keys(n, "limits", "max_rows", "max_file", "check_every", "settle", "page_size", "max_page_size")
	for _, p := range n.Pairs {
		v, ok := l.text(p)
		if !ok {
			continue
		}
		switch p.Key {
		case "max_rows", "page_size", "max_page_size":
			i, err := strconv.Atoi(v)
			if err != nil || i < 1 || i > 10000000 {
				l.errf(p.KeyLine, "%s must be a whole number from 1 to 10000000", p.Key)
				continue
			}
			switch p.Key {
			case "max_rows":
				cfg.MaxRows = i
			case "page_size":
				cfg.Limit = i
			default:
				cfg.MaxLimit = i
			}
		case "max_file":
			sz, err := rules.ParseSize(v)
			if err != nil || sz < 1 {
				l.errf(p.KeyLine, "max_file must be a size such as 64MiB")
				continue
			}
			cfg.MaxFile = sz
		case "check_every", "settle":
			d, err := time.ParseDuration(v)
			if err != nil || d < 10*time.Millisecond || d > time.Hour {
				l.errf(p.KeyLine, "%s must be a duration from 10ms to 1h, such as 2s", p.Key)
				continue
			}
			if p.Key == "settle" {
				cfg.Settle = d
			} else {
				cfg.Interval = d
			}
		}
	}
	if cfg.Limit > cfg.MaxLimit {
		l.errf(n.Line, "page_size %d is above max_page_size %d", cfg.Limit, cfg.MaxLimit)
	}
}

func (l *loader) source(n *yamlite.Node, dir string) *Source {
	if n.Kind != yamlite.Map {
		l.errf(n.Line, "each source is a mapping with id and csv or sqlite")
		return nil
	}
	l.keys(n, "a source", "id", "csv", "sqlite", "key", "columns", "exclude")
	s := &Source{Line: n.Line, Delimiter: ',', types: map[string]Type{}, Exclude: map[string]bool{}}
	rel := func(p string) string {
		if filepath.IsAbs(p) {
			return filepath.Clean(p)
		}
		return filepath.Join(dir, p)
	}
	for _, p := range n.Pairs {
		switch p.Key {
		case "id":
			if v, ok := l.text(p); ok {
				if !idPattern.MatchString(v) {
					l.errf(p.KeyLine, "id %q: use letters, digits, . _ and -", v)
				}
				s.ID = v
			}
		case "key":
			if v, ok := l.text(p); ok {
				s.Key = v
			}
		case "csv":
			if p.Value.Kind != yamlite.Map {
				l.errf(p.KeyLine, "csv must be a mapping with file, or folder and pattern")
				continue
			}
			l.keys(p.Value, "csv", "file", "folder", "pattern", "delimiter")
			for _, c := range p.Value.Pairs {
				v, ok := l.text(c)
				if !ok {
					continue
				}
				switch c.Key {
				case "file":
					s.File = rel(v)
				case "folder":
					s.Folder = rel(v)
				case "pattern":
					if _, err := filepath.Match(v, ""); err != nil || strings.ContainsAny(v, `/\`) {
						l.errf(c.KeyLine, "pattern %q must be a file name pattern such as orders-*.csv", v)
						continue
					}
					s.Pattern = v
				case "delimiter":
					switch v {
					case ",", ";", "|":
						s.Delimiter = rune(v[0])
					case "tab", `\t`:
						s.Delimiter = '\t'
					default:
						l.errf(c.KeyLine, "delimiter must be , ; | or tab")
					}
				}
			}
			switch {
			case s.File != "" && (s.Folder != "" || s.Pattern != ""):
				l.errf(p.KeyLine, "csv takes file, or folder with pattern, not both")
			case s.File == "" && (s.Folder == "" || s.Pattern == ""):
				l.errf(p.KeyLine, "csv needs file, or folder with pattern")
			}
		case "sqlite":
			if p.Value.Kind != yamlite.Map {
				l.errf(p.KeyLine, "sqlite must be a mapping with db and table or query")
				continue
			}
			l.keys(p.Value, "sqlite", "db", "table", "query")
			for _, c := range p.Value.Pairs {
				v, ok := l.text(c)
				if !ok {
					continue
				}
				switch c.Key {
				case "db":
					s.DB = rel(v)
				case "table":
					s.Table = v
				case "query":
					st, err := sqlitecli.One(v)
					if err != nil {
						l.errf(c.KeyLine, "query: %v", err)
						continue
					}
					if !isRead(st.SQL) {
						l.errf(c.KeyLine, "query must be one SELECT (or WITH ... SELECT)")
						continue
					}
					s.Query = st.SQL
				}
			}
			switch {
			case s.DB == "":
				l.errf(p.KeyLine, "sqlite needs db")
			case (s.Table == "") == (s.Query == ""):
				l.errf(p.KeyLine, "sqlite needs table or query, one of them")
			}
		case "columns":
			if p.Value.Kind != yamlite.Map {
				l.errf(p.KeyLine, "columns must map column names to types (text, integer, number, boolean, date)")
				continue
			}
			for _, c := range p.Value.Pairs {
				t := Type(strings.ToLower(strings.TrimSpace(c.Value.Value)))
				switch t {
				case Text, Integer, Number, Boolean, Date:
				default:
					l.errf(c.KeyLine, "column %s: type must be text, integer, number, boolean or date", c.Key)
					continue
				}
				if _, dup := s.types[c.Key]; dup {
					l.errf(c.KeyLine, "column %s is listed twice", c.Key)
					continue
				}
				s.types[c.Key] = t
				s.Columns = append(s.Columns, ColumnType{c.Key, t})
			}
		case "exclude":
			if p.Value.Kind != yamlite.Seq {
				l.errf(p.KeyLine, "exclude must be a list of column names")
				continue
			}
			for _, it := range p.Value.Items {
				if it.Kind != yamlite.Scalar || it.Value == "" {
					l.errf(it.Line, "exclude lists column names")
					continue
				}
				s.Exclude[it.Value] = true
			}
		}
	}
	if s.ID == "" {
		l.errf(n.Line, "a source needs an id")
	}
	if n.Get("csv") == nil && n.Get("sqlite") == nil {
		l.errf(n.Line, "a source needs csv or sqlite")
	} else if n.Get("csv") != nil && n.Get("sqlite") != nil {
		l.errf(n.Line, "a source is csv or sqlite, not both")
	}
	if s.Key != "" && s.Exclude[s.Key] {
		l.errf(n.Line, "the key column %s can't be excluded", s.Key)
	}
	return s
}

// isRead reports whether SQL is a SELECT, VALUES or WITH ... SELECT.
func isRead(sql string) bool {
	toks, _ := sqlitecli.Lex(sql)
	var sig []sqlitecli.Token
	for _, t := range toks {
		if t.Significant() {
			sig = append(sig, t)
		}
	}
	if len(sig) == 0 {
		return false
	}
	switch sig[0].Upper() {
	case "SELECT", "VALUES":
		return true
	case "WITH":
		for i, t := range sig {
			if t.Kind != sqlitecli.Word || t.Depth != 0 {
				continue
			}
			switch t.Upper() {
			case "SELECT", "VALUES":
				return true
			case "INSERT", "UPDATE", "DELETE":
				return false
			case "REPLACE":
				if i+1 < len(sig) && sig[i+1].Kind == sqlitecli.Open {
					continue
				}
				return false
			}
		}
	}
	return false
}

func (l *loader) endpoint(n *yamlite.Node, cfg *Config) *Endpoint {
	if n.Kind != yamlite.Map {
		l.errf(n.Line, "each endpoint is a mapping with path and source")
		return nil
	}
	l.keys(n, "an endpoint", "path", "source", "fields", "filters")
	e := &Endpoint{Line: n.Line}
	for _, p := range n.Pairs {
		switch p.Key {
		case "path":
			v, ok := l.text(p)
			if !ok {
				continue
			}
			e.Path = v
			if !strings.HasPrefix(v, "/") || v == "/" || strings.HasSuffix(v, "/") || strings.HasPrefix(v, "/_aftermarket") {
				l.errf(p.KeyLine, "path %q must start with /, not end with /, and not use /_aftermarket", v)
				continue
			}
			segs := strings.Split(v[1:], "/")
			for i, sg := range segs {
				if m := paramPattern.FindStringSubmatch(sg); m != nil {
					if i != len(segs)-1 || i == 0 {
						l.errf(p.KeyLine, "path %q: a {name} can only be the last part, after at least one fixed part", v)
					}
					e.Param = m[1]
					continue
				}
				if !segPattern.MatchString(sg) || sg == "." || sg == ".." {
					l.errf(p.KeyLine, "path %q: %q must be letters, digits or . _ ~ -", v, sg)
					continue
				}
				e.Prefix = append(e.Prefix, sg)
			}
		case "source":
			v, ok := l.text(p)
			if !ok {
				continue
			}
			for _, s := range cfg.Sources {
				if s.ID == v {
					e.Source = s
				}
			}
			if e.Source == nil {
				l.errf(p.KeyLine, "no source called %s", v)
			}
		case "fields":
			switch p.Value.Kind {
			case yamlite.Seq:
				for _, it := range p.Value.Items {
					if it.Kind != yamlite.Scalar || it.Value == "" {
						l.errf(it.Line, "fields lists column names, or maps names to columns")
						continue
					}
					e.Fields = append(e.Fields, Field{Name: it.Value, Column: it.Value})
				}
			case yamlite.Map:
				for _, f := range p.Value.Pairs {
					if f.Value.Kind != yamlite.Scalar || f.Value.Value == "" {
						l.errf(f.KeyLine, "field %s needs the column it shows", f.Key)
						continue
					}
					e.Fields = append(e.Fields, Field{Name: f.Key, Column: f.Value.Value})
				}
			default:
				l.errf(p.KeyLine, "fields lists column names, or maps names to columns")
			}
			seen := map[string]bool{}
			for _, f := range e.Fields {
				if seen[f.Name] {
					l.errf(p.KeyLine, "field %s is listed twice", f.Name)
				}
				seen[f.Name] = true
			}
		case "filters":
			if p.Value.Kind != yamlite.Seq {
				l.errf(p.KeyLine, "filters lists field names")
				continue
			}
			for _, it := range p.Value.Items {
				if it.Kind != yamlite.Scalar || it.Value == "" {
					l.errf(it.Line, "filters lists field names")
					continue
				}
				switch it.Value {
				case "limit", "offset":
					l.errf(it.Line, "%s is the paging parameter and can't be a filter", it.Value)
					continue
				}
				e.Filters = append(e.Filters, it.Value)
			}
		}
	}
	if e.Path == "" {
		l.errf(n.Line, "an endpoint needs a path")
	}
	if n.Get("source") == nil {
		l.errf(n.Line, "an endpoint needs a source")
	}
	if e.Source != nil {
		if e.IsItem() && e.Source.Key == "" {
			l.errf(n.Line, "%s looks rows up by key, so source %s needs key:", e.Path, e.Source.ID)
		}
		if e.IsItem() && len(e.Filters) > 0 {
			l.errf(n.Line, "%s returns one row; filters are for lists", e.Path)
		}
		for _, f := range e.Fields {
			if e.Source.Exclude[f.Column] {
				l.errf(n.Line, "field %s shows column %s, which source %s excludes", f.Name, f.Column, e.Source.ID)
			}
		}
	}
	return e
}
