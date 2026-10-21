// Package sqlitecli reads SQLite databases through the sqlite3 program, so
// Aftermarket needs no database driver. Every run is read-only: databases
// are opened read-only, the shell runs in safe mode (no ATTACH, no files,
// no shell commands), and the user's ~/.sqliterc is not read. A .sql file
// (a schema dump) is loaded into a database in memory instead.
package sqlitecli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aftermarket-dev/aftermarket/internal/proc"
)

// Source is a database file or a .sql file with statements that build one.
type Source struct {
	Path string
	SQL  bool // a .sql file, loaded into memory
}

// SourceOf picks the kind by the file name: .sql files are statements.
func SourceOf(path string) Source {
	return Source{Path: path, SQL: strings.EqualFold(filepath.Ext(path), ".sql")}
}

// Runner runs sqlite3.
type Runner struct {
	Bin     string        // the sqlite3 program
	Timeout time.Duration // per run
	MaxOut  int64

	once     sync.Once
	availErr error
}

// New returns a runner for the sqlite3 program on the PATH, or the one
// AFTERMARKET_SQLITE3 names.
func New() *Runner {
	bin := os.Getenv("AFTERMARKET_SQLITE3")
	if bin == "" {
		bin = "sqlite3"
	}
	return &Runner{Bin: bin, Timeout: 30 * time.Second, MaxOut: 64 << 20}
}

// ErrNoSQLite3 means the sqlite3 program isn't there.
var ErrNoSQLite3 = errors.New("the sqlite3 program was not found; install it, or set AFTERMARKET_SQLITE3 to its path")

// Available reports whether sqlite3 can be run and is new enough: safe
// mode, which keeps SQL from writing files, came in 3.37. The answer is
// worked out once per runner.
func (r *Runner) Available() error {
	r.once.Do(func() { r.availErr = r.available() })
	return r.availErr
}

func (r *Runner) available() error {
	if _, err := exec.LookPath(r.Bin); err != nil {
		return ErrNoSQLite3
	}
	res, err := proc.Run(context.Background(), proc.Spec{Args: []string{r.Bin, "-version"}, Timeout: r.Timeout, MaxOut: 4096})
	if err != nil {
		return fmt.Errorf("running %s -version: %w", r.Bin, err)
	}
	if v := strings.Fields(string(res.Stdout)); len(v) == 0 || !newEnough(v[0]) {
		return fmt.Errorf("%s is too old: AfterSchema needs sqlite3 3.37 or later (it says %q)", r.Bin, strings.TrimSpace(string(res.Stdout)))
	}
	return nil
}

// newEnough reports whether a version such as 3.45.1 is at least 3.37.
func newEnough(v string) bool {
	var major, minor int
	if n, _ := fmt.Sscanf(v, "%d.%d", &major, &minor); n != 2 {
		return false
	}
	return major > 3 || major == 3 && minor >= 37
}

// Error is an error SQLite reported. Where says which part of the input it
// came from ("the schema file, line 12", "the query"), Msg is SQLite's
// message.
type Error struct {
	Where string
	Msg   string
}

func (e *Error) Error() string {
	if e.Where == "" {
		return e.Msg
	}
	return e.Where + ": " + e.Msg
}

// part is one piece of the input, with a name for errors. Named parts are
// SQL from outside (a file, a mapping, a query) and are split and checked
// before they are sent; unnamed parts are Aftermarket's own shell lines.
type part struct {
	name string
	text string
	file bool // name is a file, so errors give its line numbers
}

// run feeds the parts to sqlite3 on src and returns what was printed after
// the output marker.
func (r *Runner) run(ctx context.Context, src Source, parts []part) ([]byte, error) {
	if err := r.Available(); err != nil {
		return nil, err
	}
	var mark [12]byte
	if _, err := rand.Read(mark[:]); err != nil {
		return nil, err
	}
	sentinel := "aftermarket-output-" + hex.EncodeToString(mark[:])
	dir, err := os.MkdirTemp("", "aftermarket-sqlite-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	args := []string{r.Bin, "-batch", "-bail", "-safe", "-init", os.DevNull}
	var in strings.Builder
	lines := 0
	var spans []span
	write := func(text string) {
		in.WriteString(text)
		lines += strings.Count(text, "\n")
	}
	add := func(p part) error {
		if p.name == "" {
			write(p.text + "\n")
			return nil
		}
		stmts, err := Split(p.text)
		if err != nil {
			return &Error{Where: p.name, Msg: err.Error()}
		}
		for _, st := range stmts {
			first := lines + 1
			write(st.SQL + "\n;\n")
			spans = append(spans, span{name: p.name, file: p.file, first: first, last: lines, orig: st.Line})
		}
		return nil
	}
	if err := add(part{text: ".bail on\n.mode list"}); err != nil {
		return nil, err
	}
	if src.SQL {
		data, err := os.ReadFile(src.Path)
		if err != nil {
			return nil, err
		}
		if err := add(part{name: src.Path, text: string(data), file: true}); err != nil {
			return nil, err
		}
	} else {
		abs, err := filepath.Abs(src.Path)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(abs); err != nil {
			return nil, err
		}
		args = append(args, "-readonly", abs)
	}
	for _, pt := range parts {
		pt.text = strings.ReplaceAll(pt.text, "@SENTINEL@", sentinel)
		if err := add(pt); err != nil {
			return nil, err
		}
	}
	res, err := proc.Run(ctx, proc.Spec{Args: args, Dir: dir, Stdin: strings.NewReader(in.String()), Timeout: r.Timeout, MaxOut: r.MaxOut})
	if err != nil {
		return nil, fmt.Errorf("sqlite3 %w", err)
	}
	if res.ExitCode != 0 || len(bytes.TrimSpace(res.Stderr)) > 0 {
		return nil, parseError(string(res.Stderr), spans)
	}
	out := res.Stdout
	i := bytes.LastIndex(out, []byte(sentinel+"\n"))
	if i < 0 {
		return nil, errors.New("sqlite3 printed something unexpected")
	}
	return out[i+len(sentinel)+1:], nil
}

type span struct {
	name        string
	file        bool
	first, last int // lines in what was sent
	orig        int // the line the statement starts on in its source
}

var nearLine = regexp.MustCompile(`^(?:Parse|Runtime) error near line (\d+): (.*)$`)

// parseError turns sqlite3's message into an Error naming the part it came
// from.
func parseError(stderr string, spans []span) error {
	first := strings.TrimSpace(strings.SplitN(strings.TrimSpace(stderr), "\n", 2)[0])
	if m := nearLine.FindStringSubmatch(first); m != nil {
		n, _ := strconv.Atoi(m[1])
		for _, s := range spans {
			if n >= s.first && n <= s.last {
				where := s.name
				if s.file {
					where = fmt.Sprintf("%s, line %d", where, s.orig+n-s.first)
				}
				return &Error{Where: where, Msg: m[2]}
			}
		}
		return &Error{Msg: m[2]}
	}
	first = strings.TrimPrefix(first, "Error: ")
	if first == "" {
		first = "sqlite3 failed"
	}
	return &Error{Msg: first}
}

// Cell is one value in a row, under its column's name.
type Cell struct {
	Name  string
	Value any // string, json.Number or nil
}

// Row is one result row, columns in order (names may repeat).
type Row []Cell

// Get returns the value of the first column called name.
func (r Row) Get(name string) any {
	for _, c := range r {
		if c.Name == name {
			return c.Value
		}
	}
	return nil
}

// Query runs setup (statements whose output is ignored, such as temporary
// views) and then query, which must be one statement, on src, and returns
// query's rows.
func (r *Runner) Query(ctx context.Context, src Source, setup, query string) ([]Row, error) {
	if _, err := One(query); err != nil {
		return nil, &Error{Where: "the query", Msg: err.Error()}
	}
	out, err := r.run(ctx, src, r.parts(setup, ".mode json", query))
	if err != nil {
		return nil, err
	}
	return parseRows(out)
}

func (r *Runner) parts(setup, mode, query string) []part {
	var parts []part
	if strings.TrimSpace(setup) != "" {
		parts = append(parts, part{name: "the setup", text: setup})
	}
	return append(parts, part{text: ".print @SENTINEL@\n" + mode}, part{name: "the query", text: query})
}

// insertPrefix starts every row sqlite3 prints in insert mode.
const insertPrefix = `INSERT INTO "table"(`

// Exact runs setup and then query on src, and returns one line per row,
// every value written as an exact SQL literal (strings with line breaks
// escaped, blobs in hex, numbers as SQLite stores them) after the column
// names, so two results can be compared line for line, types included. A
// query that gives no rows gives no lines.
func (r *Runner) Exact(ctx context.Context, src Source, setup, query string) ([]string, error) {
	if _, err := One(query); err != nil {
		return nil, &Error{Where: "the query", Msg: err.Error()}
	}
	out, err := r.run(ctx, src, r.parts(setup, ".mode insert\n.headers on", query))
	if err != nil {
		return nil, err
	}
	var rows []string
	for _, ln := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
		switch {
		case strings.HasPrefix(ln, insertPrefix):
			rows = append(rows, strings.TrimPrefix(ln, insertPrefix))
		case len(rows) > 0:
			// A column name with a line break in it.
			rows[len(rows)-1] += "\n" + ln
		case ln != "":
			return nil, errors.New("sqlite3 printed something unexpected")
		}
	}
	return rows, nil
}

// Check compiles a statement after setup without running it (through
// EXPLAIN), and returns nil when SQLite accepts it.
func (r *Runner) Check(ctx context.Context, src Source, setup, stmt string) error {
	st, err := One(stmt)
	if err != nil {
		return &Error{Where: "the query", Msg: err.Error()}
	}
	q := st.SQL
	if toks, _ := Lex(q); len(toks) == 0 || toks[0].Upper() != "EXPLAIN" {
		q = "EXPLAIN " + q
	}
	// The shell prints EXPLAIN in its own layout; only the error matters.
	_, err = r.run(ctx, src, r.parts(setup, ".mode list", q))
	return err
}

// parseRows reads sqlite3's JSON output, which may hold several arrays,
// keeping column order and repeated names.
func parseRows(out []byte) ([]Row, error) {
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.UseNumber()
	var rows []Row
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return rows, nil
		}
		if err != nil {
			return nil, fmt.Errorf("sqlite3 printed JSON that can't be read: %w", err)
		}
		if d, ok := tok.(json.Delim); !ok || d != '[' {
			return nil, errors.New("sqlite3 printed JSON that can't be read")
		}
		for dec.More() {
			if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
				return nil, errors.New("sqlite3 printed JSON that can't be read")
			}
			var row Row
			for dec.More() {
				k, err := dec.Token()
				if err != nil {
					return nil, err
				}
				v, err := dec.Token()
				if err != nil {
					return nil, err
				}
				name, _ := k.(string)
				row = append(row, Cell{Name: name, Value: v})
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			rows = append(rows, row)
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
	}
}

// Int reads a cell as an integer (0 when it isn't one).
func Int(v any) int64 {
	switch t := v.(type) {
	case json.Number:
		n, _ := t.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(t, 10, 64)
		return n
	}
	return 0
}

// Str reads a cell as text ("" for NULL).
func Str(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		return t.String()
	case bool:
		return strconv.FormatBool(t)
	}
	return ""
}
