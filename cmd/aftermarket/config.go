package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/aftermarket-dev/aftermarket/internal/config"
	"github.com/aftermarket-dev/aftermarket/internal/rules"
)

func runConfig(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd := args[0]
	if cmd != "check" && cmd != "diff" && cmd != "apply" && cmd != "test" {
		fmt.Fprintf(stderr, "aftermarket config: unknown command %q (use check, diff, apply or test)\n", cmd)
		return 2
	}
	fs := newFlags("aftermarket config "+cmd, stderr)
	overlayPath := fs.String("overlay", "", "overlay file")
	base := fs.String("base", "", "vendor file to read instead of the overlay's base:")
	out := fs.String("out", "", "file to write the result to (apply)")
	fixturePath := fs.String("fixture", "", "fixture file with cases (test)")
	if fs.Parse(args[1:]) != nil {
		return 2
	}
	if *overlayPath == "" {
		fmt.Fprintln(stderr, "aftermarket: --overlay is required")
		return 2
	}
	o, err := config.LoadFile(*overlayPath)
	if err != nil {
		fmt.Fprintf(stderr, "%s: invalid overlay\n", *overlayPath)
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
	if cmd == "test" {
		if *fixturePath == "" {
			fmt.Fprintln(stderr, "aftermarket: --fixture is required")
			return 2
		}
		fx, err := config.LoadFixture(*fixturePath)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", *fixturePath, err)
			return 1
		}
		failed := 0
		for _, r := range o.Run(fx) {
			if r.Pass {
				fmt.Fprintf(stdout, "PASS  %s\n", r.Name)
				continue
			}
			failed++
			fmt.Fprintf(stdout, "FAIL  %s\n", r.Name)
			for _, p := range r.Problems {
				fmt.Fprintf(stdout, "      %s\n", p)
			}
		}
		fmt.Fprintf(stdout, "%d case(s), %d failed\n", len(fx.Cases), failed)
		if failed > 0 {
			return 1
		}
		return 0
	}
	if *base != "" {
		o.Base = *base
	}
	if cmd == "apply" && *out == "" {
		fmt.Fprintln(stderr, "aftermarket: --out is required; AfterConfig never writes the vendor's file")
		return 2
	}
	data, err := os.ReadFile(o.Base)
	if err != nil {
		fmt.Fprintf(stderr, "aftermarket: can't read the base: %v\n", err)
		return 1
	}
	r, err := o.Apply(data)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", o.Base, err)
		return 1
	}
	if len(r.Problems) > 0 {
		fmt.Fprintf(stderr, "%s: %d of %d change(s) can't be applied to %s\n", *overlayPath, len(r.Problems), len(o.Changes), o.Base)
		for _, p := range r.Problems {
			fmt.Fprintf(stderr, "  %s\n", p)
		}
		if cmd == "apply" {
			fmt.Fprintf(stderr, "nothing was written\n")
		}
		return 1
	}
	switch cmd {
	case "check":
		fmt.Fprintf(stdout, "%s: %d change(s) to %s (%s)\n", *overlayPath, len(o.Changes), o.Base, o.Format)
		redact := o.Redactor()
		for i, c := range o.Changes {
			guard := ""
			switch {
			case c.ExpectAbsent:
				guard = ", guarded: must be absent"
			case c.HasExpect:
				guard = ", guarded"
			}
			fmt.Fprintf(stdout, "  %d. line %d: %s%s\n", i+1, c.Line, c.Describe(redact), guard)
		}
		fmt.Fprintln(stdout, "ok")
	case "diff":
		lines := o.Diff(r)
		for _, l := range lines {
			fmt.Fprintln(stdout, l)
		}
		if len(lines) == 0 {
			fmt.Fprintln(stdout, "no differences: every change is already in the base")
		}
	case "apply":
		if err := config.WriteOut(o.Base, *out, r.Out); err != nil {
			fmt.Fprintf(stderr, "aftermarket: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "wrote %s: %d change(s) applied, %d already in place\n", *out, len(r.Applied), len(r.InPlace))
	}
	return 0
}
