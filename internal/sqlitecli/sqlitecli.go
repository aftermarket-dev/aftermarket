// Package sqlitecli reads SQLite databases through the sqlite3 program, so
// Aftermarket needs no database driver. Every run is read-only: databases
// are opened read-only, the shell runs in safe mode (no ATTACH, no files,
// no shell commands), and the user's ~/.sqliterc is not read. A .sql file
// (a schema dump) is loaded into a database in memory instead.
package sqlitecli

import (
	"bytes"
	"context"
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

// Available reports whether sqlite3 can be run.
func (r *Runner) Available() error {
	if _, err := exec.LookPath(r.Bin); err != nil {
		return ErrNoSQLite3
	}
	return nil
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

const sentinel = "@@aftermarket-output@@"

// part is one piece of the input, with a name for errors.
type part struct {
	name string // "" for glue lines
	text string
}

// run feeds the parts to sqlite3 on src and returns what was printed after
// the sentinel.
func (r *Runner) run(ctx context.Context, src Source, parts []part) ([]byte, error) {
	if err := r.Available(); err != nil {
		return nil, err
	}
	args := []string{r.Bin, "-batch", "-bail", "-safe", "-init", os.DevNull}
	var in strings.Builder
	var spans []span
	add := func(name, text string) {
		start := strings.Count(in.String(), "\n") + 1
		in.WriteString(text)
		if !strings.HasSuffix(text, "\n") {
			in.WriteString("\n")
		}
		if name != "" {
			spans = append(spans, span{name: name, first: start, last: strings.Count(in.String(), "\n")})
		}
	}
	add("", ".bail on\n.mode list")
	if src.SQL {
		data, err := os.ReadFile(src.Path)
		if err != nil {
			return nil, err
		}
		if !bytes.HasSuffix(bytes.TrimSpace(data), []byte(";")) && len(bytes.TrimSpace(data)) > 0 {
			data = append(data, ';')
		}
		add(src.Path, string(data))
	} else {
		if _, err := os.Stat(src.Path); err != nil {
			return nil, err
		}
		p := src.Path
		if strings.HasPrefix(p, "-") {
			p = "." + string(filepath.Separator) + p
		}
		args = append(args, "-readonly", p)
	}
	for _, pt := range parts {
		add(pt.name, pt.text)
	}
	res, err := proc.Run(ctx, proc.Spec{Args: args, Stdin: strings.NewReader(in.String()), Timeout: r.Timeout, MaxOut: r.MaxOut})
	if err != nil {
		return nil, fmt.Errorf("sqlite3 %w", err)
	}
	if res.ExitCode != 0 || len(bytes.TrimSpace(res.Stderr)) > 0 {
		return nil, parseError(string(res.Stderr), spans)
	}
	out := res.Stdout
	i := bytes.Index(out, []byte(sentinel+"\n"))
	if i < 0 {
		return nil, errors.New("sqlite3 printed something unexpected")
	}
	return out[i+len(sentinel)+1:], nil
}

type span struct {
	name        string
	first, last int
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
				if !strings.HasPrefix(where, "the ") {
					where = fmt.Sprintf("%s, line %d", where, n-s.first+1)
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
// views) and then query on src, and returns query's rows.
func (r *Runner) Query(ctx context.Context, src Source, setup, query string) ([]Row, error) {
	parts := []part{}
	if strings.TrimSpace(setup) != "" {
		parts = append(parts, part{name: "the setup", text: setup + "\n;"})
	}
	parts = append(parts, part{text: ".print " + sentinel + "\n.mode json"}, part{name: "the query", text: query + "\n;"})
	out, err := r.run(ctx, src, parts)
	if err != nil {
		return nil, err
	}
	return parseRows(out)
}

// Quoted runs setup and then query on src, and returns query's output with
// a header line and every value written as an exact SQL literal, so two
// results can be compared byte for byte, types included.
func (r *Runner) Quoted(ctx context.Context, src Source, setup, query string) (string, error) {
	parts := []part{}
	if strings.TrimSpace(setup) != "" {
		parts = append(parts, part{name: "the setup", text: setup + "\n;"})
	}
	parts = append(parts, part{text: ".print " + sentinel + "\n.mode quote\n.headers on"}, part{name: "the query", text: query + "\n;"})
	out, err := r.run(ctx, src, parts)
	return string(out), err
}

// Check compiles each statement after setup without running it (through
// EXPLAIN), and returns nil when SQLite accepts it.
func (r *Runner) Check(ctx context.Context, src Source, setup, stmt string) error {
	// The shell prints EXPLAIN in its own layout; only the error matters.
	_, err := r.Quoted(ctx, src, setup, "EXPLAIN "+stmt)
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
