package bridge

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aftermarket-dev/aftermarket/internal/schema"
	"github.com/aftermarket-dev/aftermarket/internal/sqlitecli"
)

// Kind is the kind of a value in a snapshot.
type Kind byte

// Value kinds.
const (
	Null Kind = iota
	Str
	Int
	Float
	Bool
)

// Cell is one value: its kind, its Go value and its JSON text.
type Cell struct {
	Kind Kind
	S    string
	I    int64
	F    float64
	B    bool
	JSON []byte
}

// Text is how the cell reads in a URL: a string as it is, numbers in their
// shortest form, booleans as true or false. Keys are looked up by it.
func (c Cell) Text() string {
	switch c.Kind {
	case Str:
		return c.S
	case Int:
		return strconv.FormatInt(c.I, 10)
	case Float:
		return formatFloat(c.F)
	case Bool:
		return strconv.FormatBool(c.B)
	}
	return ""
}

func formatFloat(f float64) string {
	if f == 0 {
		return "0"
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}

var numberPattern = regexp.MustCompile(`^[+-]?([0-9]+(\.[0-9]*)?|\.[0-9]+)([eE][+-]?[0-9]+)?$`)

// parseNumber reads a decimal number as written in a CSV file or a URL:
// digits, a point and an exponent, nothing Go-specific (hex, underscores,
// Inf, NaN). A number too big for a float, or too small to tell from zero,
// is refused rather than changed.
func parseNumber(v string) (float64, bool) {
	if !numberPattern.MatchString(v) {
		return 0, false
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return 0, false
	}
	if f == 0 && strings.ContainsAny(strings.SplitN(strings.ToLower(v), "e", 2)[0], "123456789") {
		return 0, false
	}
	return f, true
}

// parseInt reads a decimal whole number: an optional sign and digits.
func parseInt(v string) (int64, bool) {
	if v == "" || strings.ContainsAny(v[1:], "+-") || strings.Trim(v, "+-0123456789") != "" {
		return 0, false
	}
	i, err := strconv.ParseInt(v, 10, 64)
	return i, err == nil
}

func mkStr(s string) Cell {
	b, _ := json.Marshal(s)
	return Cell{Kind: Str, S: s, JSON: b}
}
func mkInt(i int64) Cell { return Cell{Kind: Int, I: i, JSON: []byte(strconv.FormatInt(i, 10))} }
func mkFloat(f float64) Cell {
	if f == 0 {
		f = 0 // -0 reads as 0, as SQLite and filters treat them
	}
	t := formatFloat(f)
	if !strings.ContainsAny(t, ".eE") {
		t += ".0"
	}
	return Cell{Kind: Float, F: f, JSON: []byte(t)}
}
func mkBool(b bool) Cell { return Cell{Kind: Bool, B: b, JSON: []byte(strconv.FormatBool(b))} }

var nullCell = Cell{Kind: Null, JSON: []byte("null")}

// Snapshot is one reading of a source.
type Snapshot struct {
	Columns []string // served columns, in the source's order
	col     map[string]int
	Rows    [][]Cell
	keys    map[string]int // key text to row, when the source has a key
	From    string         // the file read
	Loaded  time.Time
}

// Column returns a column's position, or -1.
func (s *Snapshot) Column(name string) int {
	if i, ok := s.col[name]; ok {
		return i
	}
	return -1
}

// Lookup returns the row whose key reads as text, or nil.
func (s *Snapshot) Lookup(text string) []Cell {
	if i, ok := s.keys[text]; ok {
		return s.Rows[i]
	}
	return nil
}

// fingerprint says what a source's files look like now: which file, its
// size and modification time. A change means the source should be read
// again.
func (c *Config) fingerprint(s *Source) (string, string, error) {
	var files []string
	switch {
	case s.File != "":
		files = []string{s.File}
	case s.Folder != "":
		f, err := newest(s.Folder, s.Pattern)
		if err != nil {
			return "", "", err
		}
		files = []string{f}
	default:
		files = []string{s.DB, s.DB + "-wal"}
	}
	var b strings.Builder
	for i, f := range files {
		st, err := os.Stat(f)
		if err != nil {
			if i > 0 && errors.Is(err, os.ErrNotExist) {
				continue // no -wal file
			}
			return "", "", err
		}
		if i > 0 && st.Size() == 0 {
			continue // an empty -wal file, as reading a WAL database leaves
		}
		if !st.Mode().IsRegular() {
			return "", "", fmt.Errorf("%s is not a regular file", filepath.Base(f))
		}
		fmt.Fprintf(&b, "%s|%d|%d|%s;", f, st.Size(), st.ModTime().UnixNano(), fileID(st))
	}
	return b.String(), files[0], nil
}

// newest returns the last file by name in dir that matches pattern, so
// exports named by date pick the latest day.
func newest(dir, pattern string) (string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var names []string
	for _, e := range ents {
		if !e.Type().IsRegular() {
			continue
		}
		if ok, _ := filepath.Match(pattern, e.Name()); ok {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("no file in %s matches %s", filepath.Base(dir), pattern)
	}
	sort.Strings(names)
	return filepath.Join(dir, names[len(names)-1]), nil
}

// needs lists the columns the endpoints on a source use, so a file that no
// longer has them is refused instead of served with holes.
func (c *Config) needs(s *Source) []string {
	var cols []string
	add := func(n string) {
		for _, x := range cols {
			if x == n {
				return
			}
		}
		cols = append(cols, n)
	}
	if s.Key != "" {
		add(s.Key)
	}
	for _, ct := range s.Columns {
		if !s.Exclude[ct.Name] {
			add(ct.Name)
		}
	}
	for _, e := range c.Endpoints {
		if e.Source != s {
			continue
		}
		for _, f := range e.Fields {
			add(f.Column)
		}
		for _, f := range e.Filters {
			add(e.FieldColumn(f))
		}
	}
	return cols
}

// FieldColumn returns the column a field name shows.
func (e *Endpoint) FieldColumn(name string) string {
	for _, f := range e.Fields {
		if f.Name == name {
			return f.Column
		}
	}
	return name
}

// ErrNotSettled means a file changed too recently to be read: it may
// still be being written.
var ErrNotSettled = errors.New("the file changed less than settle ago; it may still be being written")

// fileBased reports whether a source is a file written by the app, which
// may be caught half written: a CSV file or a .sql dump. A database file is
// read by SQLite in one consistent state and needs no settle time.
func (s *Source) fileBased() bool {
	return s.IsCSV() || sqlitecli.SourceOf(s.DB).SQL
}

// Read reads a source into a snapshot. A CSV file or .sql dump is read
// only once it has been left alone for the config's settle time: counted
// from firstSeen (when the bridge first saw the file as it is now, on its
// own clock), or from the file's modification time when that is older and
// not in the future. With wait, Read waits for the file to settle, giving
// up if it keeps changing; without, it returns ErrNotSettled.
func (c *Config) Read(ctx context.Context, r *sqlitecli.Runner, s *Source, firstSeen time.Time, wait bool) (*Snapshot, string, error) {
	fp, file, err := c.fingerprint(s)
	if err != nil {
		return nil, "", err
	}
	check := time.Duration(0)
	if s.fileBased() {
		started := time.Now()
		for {
			age := time.Since(firstSeen)
			if st, err := os.Stat(file); err == nil {
				if m := time.Since(st.ModTime()); m >= 0 && m > age {
					age = m
				}
			}
			if age >= c.Settle {
				break
			}
			if !wait {
				return nil, fp, ErrNotSettled
			}
			if time.Since(started) > 3*c.Settle+5*time.Second {
				return nil, fp, errors.New("the file kept changing; it is still being written")
			}
			select {
			case <-ctx.Done():
				return nil, fp, ctx.Err()
			case <-time.After(min(c.Settle-age, 100*time.Millisecond)):
			}
			again, againFile, err := c.fingerprint(s)
			if err != nil {
				return nil, fp, err
			}
			if again != fp {
				fp, file, firstSeen = again, againFile, time.Now()
			}
		}
		// After reading, look again a short while later.
		check = min(c.Settle, 100*time.Millisecond)
	}
	var snap *Snapshot
	if s.IsCSV() {
		snap, err = c.readCSV(s, file)
	} else {
		snap, err = c.readSQLite(ctx, r, s)
	}
	if err != nil {
		return nil, fp, err
	}
	if check > 0 {
		select {
		case <-ctx.Done():
			return nil, fp, ctx.Err()
		case <-time.After(check):
		}
	}
	if again, _, err := c.fingerprint(s); err != nil || again != fp {
		return nil, fp, errors.New("the file changed while it was read; it is still being written")
	}
	snap.From = file
	snap.Loaded = time.Now()
	if err := c.finish(s, snap); err != nil {
		return nil, fp, err
	}
	return snap, fp, nil
}

// checkDump refuses a .sql dump that was cut off: one that doesn't end
// with a line break, or that opens a transaction it never commits (as
// sqlite3's .dump does), since the rows inserted so far would read as the
// whole table.
func checkDump(path string, max int64) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if int64(len(data)) > max {
		return fmt.Errorf("the file is over max_file (%d bytes)", max)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		return errors.New("the dump doesn't end with a line break; it is still being written")
	}
	stmts, err := sqlitecli.Split(string(data))
	if err != nil {
		return err
	}
	open := false
	for _, st := range stmts {
		w := strings.Fields(strings.ToUpper(st.SQL))
		if len(w) == 0 {
			continue
		}
		switch w[0] {
		case "BEGIN":
			open = true
		case "COMMIT", "END", "ROLLBACK":
			open = false
		}
	}
	if open {
		return errors.New("the dump starts a transaction it never commits; it is still being written")
	}
	return nil
}

// finish checks the columns the config needs and builds the key index.
func (c *Config) finish(s *Source, snap *Snapshot) error {
	snap.col = map[string]int{}
	for i, n := range snap.Columns {
		snap.col[n] = i
	}
	var missing []string
	for _, n := range c.needs(s) {
		if snap.Column(n) < 0 {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("column %s is missing", strings.Join(missing, ", "))
	}
	if len(snap.Rows) > c.MaxRows {
		return fmt.Errorf("more than max_rows (%d) rows", c.MaxRows)
	}
	if s.Key != "" {
		k := snap.Column(s.Key)
		snap.keys = make(map[string]int, len(snap.Rows))
		for i, row := range snap.Rows {
			cell := row[k]
			if cell.Kind == Null {
				return fmt.Errorf("row %d has no %s", i+1, s.Key)
			}
			t := cell.Text()
			if j, dup := snap.keys[t]; dup {
				return fmt.Errorf("rows %d and %d have the same %s", j+1, i+1, s.Key)
			}
			snap.keys[t] = i
		}
	}
	return nil
}

// readCSV reads a CSV file: a header row, then one record per line, every
// value typed by its declared column.
func (c *Config) readCSV(s *Source, file string) (*Snapshot, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, c.MaxFile+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > c.MaxFile {
		return nil, fmt.Errorf("the file is over max_file (%d bytes)", c.MaxFile)
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	if len(data) == 0 {
		return nil, errors.New("the file is empty; it is still being written")
	}
	if data[len(data)-1] != '\n' {
		return nil, errors.New("the file doesn't end with a line break; it is still being written")
	}
	if !utf8.Valid(data) {
		return nil, errors.New("the file isn't valid UTF-8")
	}
	cr := csv.NewReader(bytes.NewReader(data))
	cr.Comma = s.Delimiter
	cr.ReuseRecord = false
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("the header: %v", csvMessage(err))
	}
	seen := map[string]bool{}
	var keep []int
	snap := &Snapshot{}
	for i, h := range header {
		if seen[h] {
			return nil, fmt.Errorf("the header names %s twice", h)
		}
		seen[h] = true
		if s.Exclude[h] {
			continue
		}
		keep = append(keep, i)
		snap.Columns = append(snap.Columns, h)
	}
	for _, ct := range s.Columns {
		if !seen[ct.Name] && !s.Exclude[ct.Name] {
			return nil, fmt.Errorf("column %s is missing", ct.Name)
		}
	}
	if line, _ := cr.FieldPos(0); line != 1 {
		return nil, errors.New("line 1 is blank; the header must be the first line")
	}
	next := 2 + newlines(header) // the line the next record must start on
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			if lines := bytes.Count(data, []byte("\n")); next != lines+1 {
				return nil, fmt.Errorf("line %d is blank; blank lines can't be read as rows", next)
			}
			break
		}
		if err != nil {
			return nil, csvMessage(err)
		}
		// encoding/csv skips blank lines; in a one-column file a blank
		// line is a row with an empty value, so refuse rather than lose it.
		if start, _ := cr.FieldPos(0); start != next {
			return nil, fmt.Errorf("line %d is blank; blank lines can't be read as rows", next)
		}
		next += 1 + newlines(rec)
		if len(snap.Rows) >= c.MaxRows {
			return nil, fmt.Errorf("more than max_rows (%d) rows", c.MaxRows)
		}
		line, _ := cr.FieldPos(0)
		row := make([]Cell, len(keep))
		for j, i := range keep {
			cell, err := typed(rec[i], s.TypeOf(header[i]))
			if err != nil {
				return nil, fmt.Errorf("line %d, column %s: %v", line, header[i], err)
			}
			row[j] = cell
		}
		snap.Rows = append(snap.Rows, row)
	}
	return snap, nil
}

func newlines(rec []string) int {
	n := 0
	for _, f := range rec {
		n += strings.Count(f, "\n")
	}
	return n
}

// csvMessage keeps encoding/csv's line and reason without quoting data.
func csvMessage(err error) error {
	var pe *csv.ParseError
	if errors.As(err, &pe) {
		return fmt.Errorf("line %d: %v", pe.Line, pe.Err)
	}
	return err
}

// typed reads a CSV value as its declared type. An empty value is NULL for
// every type but text.
func typed(v string, t Type) (Cell, error) {
	if t == "" || t == Text {
		return mkStr(v), nil
	}
	v = strings.TrimSpace(v)
	if v == "" {
		return nullCell, nil
	}
	switch t {
	case Integer:
		i, ok := parseInt(v)
		if !ok {
			return Cell{}, errors.New("not a whole number")
		}
		return mkInt(i), nil
	case Number:
		f, ok := parseNumber(v)
		if !ok {
			return Cell{}, errors.New("not a number")
		}
		return mkFloat(f), nil
	case Boolean:
		switch strings.ToLower(v) {
		case "true", "yes", "1":
			return mkBool(true), nil
		case "false", "no", "0":
			return mkBool(false), nil
		}
		return Cell{}, errors.New("not true or false")
	case Date:
		if _, err := time.Parse("2006-01-02", v); err != nil {
			return Cell{}, errors.New("not a date such as 2026-10-22")
		}
		return mkStr(v), nil
	}
	return Cell{}, fmt.Errorf("unknown type %s", t)
}

// sqlString writes text as an SQL string literal.
func sqlString(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

const viewName = `"aftermarket_bridge"`

// readSQLite reads a table or a fixed query. Only the config's own text
// reaches SQL; each row comes back as one JSON array of (type, value)
// pairs, with reals written to 17 digits so no precision is lost.
func (c *Config) readSQLite(ctx context.Context, r *sqlitecli.Runner, s *Source) (*Snapshot, error) {
	src := sqlitecli.SourceOf(s.DB)
	if src.SQL {
		if err := checkDump(s.DB, c.MaxFile); err != nil {
			return nil, err
		}
	}
	setup, from, colQuery := "", schema.Quote(s.Table), ""
	if s.Query != "" {
		setup = "CREATE TEMP VIEW " + viewName + " AS " + s.Query
		from = viewName
		colQuery = "SELECT name, (SELECT encoding FROM pragma_encoding) AS enc FROM pragma_table_info('aftermarket_bridge') ORDER BY cid"
	} else {
		colQuery = "SELECT name, (SELECT encoding FROM pragma_encoding) AS enc FROM pragma_table_xinfo(" + sqlString(s.Table) + ") WHERE hidden <> 1 ORDER BY cid"
	}
	rows, err := r.Query(ctx, src, setup, colQuery)
	if err != nil {
		return nil, sqlMessage(err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("no table called %s", s.Table)
	}
	if enc := sqlitecli.Str(rows[0].Get("enc")); enc != "UTF-8" {
		return nil, fmt.Errorf("the database stores text as %s; only UTF-8 databases are read", enc)
	}
	snap := &Snapshot{}
	seen := map[string]bool{}
	var parts []string
	for _, row := range rows {
		n := sqlitecli.Str(row.Get("name"))
		if seen[n] {
			return nil, fmt.Errorf("the query gives two columns called %s", n)
		}
		seen[n] = true
		if s.Exclude[n] {
			continue
		}
		snap.Columns = append(snap.Columns, n)
		q := schema.Quote(n)
		// Reals to 17 digits, so none is lost; text as hex, since the
		// shell's JSON mode mangles bytes that aren't UTF-8.
		parts = append(parts, fmt.Sprintf("typeof(%s), CASE typeof(%s) WHEN 'real' THEN printf('%%!.17g', %s) WHEN 'text' THEN hex(%s) ELSE %s END", q, q, q, q, q))
	}
	for _, ct := range s.Columns {
		if !seen[ct.Name] && !s.Exclude[ct.Name] {
			return nil, fmt.Errorf("column %s is missing", ct.Name)
		}
	}
	if len(parts) == 0 {
		return nil, errors.New("every column is excluded")
	}
	data := fmt.Sprintf("SELECT json_array(%s) AS r FROM %s LIMIT %d", strings.Join(parts, ", "), from, c.MaxRows+1)
	out, err := r.Query(ctx, src, setup, data)
	if err != nil {
		return nil, sqlMessage(err)
	}
	if len(out) > c.MaxRows {
		return nil, fmt.Errorf("more than max_rows (%d) rows", c.MaxRows)
	}
	for i, row := range out {
		var pairs []json.RawMessage
		if err := json.Unmarshal([]byte(sqlitecli.Str(row.Get("r"))), &pairs); err != nil || len(pairs) != 2*len(snap.Columns) {
			return nil, errors.New("sqlite3 gave a row that can't be read")
		}
		cells := make([]Cell, len(snap.Columns))
		for j, name := range snap.Columns {
			var typ string
			_ = json.Unmarshal(pairs[2*j], &typ)
			cell, err := sqliteCell(typ, pairs[2*j+1], s.TypeOf(name))
			if err != nil {
				return nil, fmt.Errorf("row %d, column %s: %v", i+1, name, err)
			}
			cells[j] = cell
		}
		snap.Rows = append(snap.Rows, cells)
	}
	return snap, nil
}

func sqlMessage(err error) error {
	var e *sqlitecli.Error
	if errors.As(err, &e) && strings.HasPrefix(e.Where, "the ") {
		return errors.New(e.Msg)
	}
	return err
}

// sqliteCell reads one value by its SQLite type, and checks it against the
// declared type when there is one.
func sqliteCell(typ string, raw json.RawMessage, t Type) (Cell, error) {
	var cell Cell
	switch typ {
	case "null":
		return nullCell, nil
	case "integer":
		var n json.Number
		if err := json.Unmarshal(raw, &n); err != nil {
			return Cell{}, errors.New("sqlite3 gave an integer that can't be read")
		}
		i, err := strconv.ParseInt(n.String(), 10, 64)
		if err != nil {
			return Cell{}, errors.New("sqlite3 gave an integer that can't be read")
		}
		cell = mkInt(i)
	case "real":
		var txt string
		if err := json.Unmarshal(raw, &txt); err != nil {
			return Cell{}, errors.New("sqlite3 gave a number that can't be read")
		}
		f, err := strconv.ParseFloat(txt, 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return Cell{}, errors.New("a number JSON can't hold (infinite or not a number)")
		}
		cell = mkFloat(f)
	case "text":
		var h string
		if err := json.Unmarshal(raw, &h); err != nil {
			return Cell{}, errors.New("sqlite3 gave text that can't be read")
		}
		b, err := hex.DecodeString(h)
		if err != nil {
			return Cell{}, errors.New("sqlite3 gave text that can't be read")
		}
		if !utf8.Valid(b) {
			return Cell{}, errors.New("text that isn't valid UTF-8")
		}
		cell = mkStr(string(b))
	case "blob":
		return Cell{}, errors.New("holds a blob, which JSON can't carry; exclude the column")
	default:
		return Cell{}, fmt.Errorf("unknown SQLite type %q", typ)
	}
	switch t {
	case "":
		return cell, nil
	case Text:
		if cell.Kind != Str {
			return Cell{}, errors.New("not text")
		}
	case Integer:
		if cell.Kind != Int {
			return Cell{}, errors.New("not a whole number")
		}
	case Number:
		switch cell.Kind {
		case Int:
			if f := float64(cell.I); f >= 1<<63 || int64(f) != cell.I {
				return Cell{}, errors.New("a whole number too big to be a number without changing; declare it integer")
			}
			return mkFloat(float64(cell.I)), nil
		case Float:
		default:
			return Cell{}, errors.New("not a number")
		}
	case Boolean:
		if cell.Kind != Int || (cell.I != 0 && cell.I != 1) {
			return Cell{}, errors.New("not 0 or 1")
		}
		return mkBool(cell.I == 1), nil
	case Date:
		if cell.Kind != Str {
			return Cell{}, errors.New("not a date such as 2026-10-22")
		}
		if _, err := time.Parse("2006-01-02", cell.S); err != nil {
			return Cell{}, errors.New("not a date such as 2026-10-22")
		}
	}
	return cell, nil
}

// Match reports whether a cell equals a value from a URL, read as the
// cell's own kind. A declared column refuses a value that can't be its
// type (bad is set); an undeclared one simply doesn't match.
func Match(cell Cell, v string, t Type) (match, bad bool) {
	switch t {
	case Integer:
		if _, ok := parseInt(v); !ok {
			return false, true
		}
	case Number:
		if _, ok := parseNumber(v); !ok {
			return false, true
		}
	case Boolean:
		if _, err := parseBool(v); err != nil {
			return false, true
		}
	case Date:
		if _, err := time.Parse("2006-01-02", v); err != nil {
			return false, true
		}
	}
	switch cell.Kind {
	case Str:
		return cell.S == v, false
	case Int:
		i, ok := parseInt(v)
		return ok && i == cell.I, false
	case Float:
		f, ok := parseNumber(v)
		return ok && f == cell.F, false
	case Bool:
		b, err := parseBool(v)
		return err == nil && b == cell.B, false
	}
	return false, false
}

func parseBool(v string) (bool, error) {
	switch strings.ToLower(v) {
	case "true", "yes", "1":
		return true, nil
	case "false", "no", "0":
		return false, nil
	}
	return false, errors.New("not a boolean")
}

// KeyText turns a key from a URL into the text keys are indexed by, read as
// the key column's declared type: /orders/007 finds order 7 when id is an
// integer. ok is false when the value can't be that type.
func KeyText(v string, t Type) (string, bool) {
	switch t {
	case Integer:
		i, ok := parseInt(v)
		if !ok {
			return "", false
		}
		return strconv.FormatInt(i, 10), true
	case Number:
		f, ok := parseNumber(v)
		if !ok {
			return "", false
		}
		return formatFloat(f), true
	case Boolean:
		b, err := parseBool(v)
		if err != nil {
			return "", false
		}
		return strconv.FormatBool(b), true
	case Date:
		if _, err := time.Parse("2006-01-02", v); err != nil {
			return "", false
		}
	}
	return v, true
}
