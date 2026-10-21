package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/aftermarket-dev/aftermarket/internal/config"
	"github.com/aftermarket-dev/aftermarket/internal/rules"
	"github.com/aftermarket-dev/aftermarket/internal/schema"
	"github.com/aftermarket-dev/aftermarket/internal/sqlitecli"
)

func runSchema(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd := args[0]
	if cmd != "diff" && cmd != "compat" && cmd != "check" && cmd != "test" {
		fmt.Fprintf(stderr, "aftermarket schema: unknown command %q (use diff, compat, check or test)\n", cmd)
		return 2
	}
	fs := newFlags("aftermarket schema "+cmd, stderr)
	mapPath := fs.String("map", "", "mapping file: the old tables in terms of the new schema")
	oldPath := fs.String("old", "", "old schema: a database or a .sql file (default: the map's old)")
	newPath := fs.String("new", "", "new schema: a database or a .sql file (default: the map's new)")
	out := fs.String("out", "", "file to write the compatibility views to (compat)")
	queries := fs.String("queries", "", "file of old queries, separated by ; (check, test)")
	asJSON := fs.Bool("json", false, "print JSON (diff)")
	if fs.Parse(args[1:]) != nil {
		return 2
	}
	var m *schema.Map
	if *mapPath != "" {
		var err error
		if m, err = schema.LoadMap(*mapPath); err != nil {
			fmt.Fprintf(stderr, "%s: invalid mapping\n", *mapPath)
			var errs rules.Errors
			if errors.As(err, &errs) {
				for _, e := range errs {
					fmt.Fprintf(stderr, "  %s\n", e)
				}
			} else {
				fmt.Fprintf(stderr, "  %s\n", err)
			}
			return 1
		}
		if *oldPath == "" {
			*oldPath = m.Old
		}
		if *newPath == "" {
			*newPath = m.New
		}
	}
	need := func(ok bool, what string) bool {
		if !ok {
			fmt.Fprintf(stderr, "aftermarket schema %s: %s\n", cmd, what)
		}
		return ok
	}
	switch cmd {
	case "diff":
		if !need(*oldPath != "" && *newPath != "", "--old and --new are required (or a --map that names them)") {
			return 2
		}
	case "compat":
		if !need(m != nil && *out != "", "--map and --out are required") || !need(*newPath != "", "--new is required (or new: in the map)") {
			return 2
		}
	case "check":
		if !need(*queries != "" && *newPath != "", "--queries and --new are required (--new can come from --map)") {
			return 2
		}
	case "test":
		if !need(m != nil && *queries != "", "--map and --queries are required") || !need(*oldPath != "" && *newPath != "", "--old and --new are required (or old: and new: in the map)") {
			return 2
		}
	}
	r := sqlitecli.New()
	if err := r.Available(); err != nil {
		fmt.Fprintf(stderr, "aftermarket: %v\n", err)
		return 1
	}
	ctx := context.Background()
	read := func(path string) (*schema.Schema, sqlitecli.Source, bool) {
		src := sqlitecli.SourceOf(path)
		s, err := schema.Read(ctx, r, src)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", path, err)
			return nil, src, false
		}
		return s, src, true
	}
	// plan reads both schemas and matches the map; problems are printed.
	plan := func() ([]schema.Planned, sqlitecli.Source, bool) {
		oldS, _, ok1 := read(*oldPath)
		newS, newSrc, ok2 := read(*newPath)
		if !ok1 || !ok2 {
			return nil, newSrc, false
		}
		views, probs := m.Plan(oldS, newS)
		if len(probs) == 0 {
			probs = schema.Verify(ctx, r, newSrc, views)
		}
		if len(probs) > 0 {
			fmt.Fprintf(stderr, "%s: %d problem(s) with the mapping\n", *mapPath, len(probs))
			for _, p := range probs {
				fmt.Fprintf(stderr, "  %s\n", p)
			}
			return nil, newSrc, false
		}
		return views, newSrc, true
	}

	switch cmd {
	case "diff":
		oldS, _, ok1 := read(*oldPath)
		newS, _, ok2 := read(*newPath)
		if !ok1 || !ok2 {
			return 1
		}
		changes := schema.Diff(oldS, newS)
		if m != nil {
			views, probs := m.Plan(oldS, newS)
			for _, p := range probs {
				fmt.Fprintf(stderr, "%s: %s\n", *mapPath, p)
			}
			changes = schema.Cover(changes, views)
		}
		broken := schema.Breaking(changes)
		if *asJSON {
			b, _ := json.MarshalIndent(map[string]any{"old": *oldPath, "new": *newPath, "breaking": broken, "changes": orEmpty(changes)}, "", "  ")
			fmt.Fprintln(stdout, string(b))
		} else {
			fmt.Fprintf(stdout, "%s -> %s: %d change(s), %d breaking\n", *oldPath, *newPath, len(changes), broken)
			for _, c := range changes {
				label := "safe    "
				switch {
				case c.Breaking && c.Covered != "":
					label = "covered "
				case c.Breaking:
					label = "breaking"
				}
				fmt.Fprintf(stdout, "  %s  %s\n", label, c)
			}
		}
		if broken > 0 {
			return 1
		}
		return 0

	case "compat":
		views, _, ok := plan()
		if !ok {
			fmt.Fprintln(stderr, "nothing was written")
			return 1
		}
		for _, in := range []string{*oldPath, *newPath, *mapPath, *queries} {
			if in != "" && config.SameFile(in, *out) {
				fmt.Fprintf(stderr, "aftermarket: --out is one of the inputs (%s); AfterSchema never writes its inputs, so choose another file\n", in)
				return 1
			}
		}
		sql := schema.SQL(views, filepath.Base(*mapPath), false)
		if err := config.WriteFile(*out, []byte(sql), 0o644); err != nil {
			fmt.Fprintf(stderr, "aftermarket: %v\n", err)
			return 1
		}
		names := make([]string, len(views))
		for i, v := range views {
			names[i] = v.Map.Old
		}
		fmt.Fprintf(stdout, "wrote %s: %d view(s): %s\n", *out, len(views), strings.Join(names, ", "))
		return 0

	case "check", "test":
		data, err := os.ReadFile(*queries)
		if err != nil {
			fmt.Fprintf(stderr, "aftermarket: %v\n", err)
			return 1
		}
		stmts := schema.Split(string(data))
		if len(stmts) == 0 {
			fmt.Fprintf(stderr, "%s: no statements\n", *queries)
			return 1
		}
		setup := ""
		newSrc := sqlitecli.SourceOf(*newPath)
		if m != nil {
			views, src, ok := plan()
			if !ok {
				return 1
			}
			newSrc, setup = src, schema.SQL(views, "", true)
		}
		var res []schema.Result
		if cmd == "check" {
			res = schema.Check(ctx, r, newSrc, setup, stmts)
		} else {
			res = schema.Compare(ctx, r, sqlitecli.SourceOf(*oldPath), newSrc, setup, stmts)
		}
		failed, skipped := 0, 0
		for _, x := range res {
			first := strings.SplitN(x.SQL, "\n", 2)[0]
			if len(first) > 70 {
				first = first[:67] + "..."
			}
			switch {
			case x.Skipped != "":
				skipped++
				fmt.Fprintf(stdout, "SKIP  line %d: %s (%s)\n", x.Line, first, x.Skipped)
			case x.OK:
				if cmd == "test" {
					fmt.Fprintf(stdout, "PASS  line %d: %s\n", x.Line, first)
				}
			default:
				failed++
				fmt.Fprintf(stdout, "FAIL  line %d: %s\n      %s\n", x.Line, first, x.Problem)
			}
		}
		against := "the new schema"
		if m != nil {
			against += " with the compatibility views"
		}
		if cmd == "check" {
			fmt.Fprintf(stdout, "%d of %d queries compile on %s\n", len(res)-failed, len(res), against)
		} else {
			fmt.Fprintf(stdout, "%d of %d reads give the same rows on %s as on the old one", len(res)-failed-skipped, len(res)-skipped, against)
			if skipped > 0 {
				fmt.Fprintf(stdout, " (%d not reads, skipped)", skipped)
			}
			fmt.Fprintln(stdout)
		}
		if failed > 0 {
			return 1
		}
		return 0
	}
	return 2
}

func orEmpty(c []schema.Change) []schema.Change {
	if c == nil {
		return []schema.Change{}
	}
	return c
}
