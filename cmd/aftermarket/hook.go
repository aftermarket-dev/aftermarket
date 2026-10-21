package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/aftermarket-dev/aftermarket/internal/hook"
	"github.com/aftermarket-dev/aftermarket/internal/rules"
	"github.com/aftermarket-dev/aftermarket/internal/version"
)

func runHook(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd := args[0]
	if cmd != "check" && cmd != "run" && cmd != "test" {
		fmt.Fprintf(stderr, "aftermarket hook: unknown command %q (use check, run or test)\n", cmd)
		return 2
	}
	fs := newFlags("aftermarket hook "+cmd, stderr)
	configPath := fs.String("config", "", "hook config file")
	once := fs.Bool("once", false, "run: check every source and deliver what's due once, then exit")
	dryRun := fs.Bool("dry-run", false, "run: print events instead of saving and sending them")
	logPath := fs.String("log", "-", "run: where to write the log (JSON lines); - for stderr, empty to turn it off")
	fixturePath := fs.String("fixture", "", "test: fixture file with cases")
	if fs.Parse(args[1:]) != nil {
		return 2
	}
	if *configPath == "" {
		fmt.Fprintln(stderr, "aftermarket: --config is required")
		return 2
	}
	cfg, err := hook.LoadFile(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "%s: invalid hook config\n", *configPath)
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
	switch cmd {
	case "check":
		fmt.Fprintf(stdout, "%s: %d source(s), %d webhook(s), state %s\n", *configPath, len(cfg.Sources), len(cfg.Webhooks), cfg.State)
		for _, s := range cfg.Sources {
			what := s.Path
			switch s.Kind {
			case "http":
				what = s.URL.Redacted()
			case "command":
				what = s.Run[0]
			}
			fmt.Fprintf(stdout, "  source %s (line %d): %s %s every %s, to %v\n", s.ID, s.Line, s.Kind, what, s.Interval, s.SendTo)
		}
		missing := 0
		for _, w := range cfg.Webhooks {
			sig := "unsigned"
			if w.SecretEnv != "" {
				sig = "signed with $" + w.SecretEnv
				if os.Getenv(w.SecretEnv) == "" {
					sig += ", which is not set here"
					missing++
				}
			}
			fmt.Fprintf(stdout, "  webhook %s (line %d): %s, %s\n", w.ID, w.Line, w.URL.Redacted(), sig)
		}
		if missing > 0 {
			fmt.Fprintln(stdout, "ok, but set the missing secrets before hook run")
			return 0
		}
		fmt.Fprintln(stdout, "ok")
		return 0
	case "test":
		if *fixturePath == "" {
			fmt.Fprintln(stderr, "aftermarket: --fixture is required")
			return 2
		}
		fx, err := hook.LoadFixture(*fixturePath)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", *fixturePath, err)
			return 1
		}
		failed := 0
		for _, r := range cfg.RunFixture(fx) {
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
	log, closeLog, err := openLog(*logPath, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "aftermarket: %v\n", err)
		return 1
	}
	defer closeLog()
	h, err := hook.New(cfg, hook.Options{DryRun: *dryRun, Out: stdout, Log: log})
	if err != nil {
		fmt.Fprintf(stderr, "aftermarket: %v\n", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if !*once {
		mode := ""
		if *dryRun {
			mode = " (dry run: nothing is saved or sent)"
		}
		fmt.Fprintf(stdout, "aftermarket hook %s: watching %d source(s)%s\n", version.Version, len(cfg.Sources), mode)
	}
	if err := h.Run(ctx, *once); err != nil {
		fmt.Fprintf(stderr, "aftermarket: %v\n", err)
		return 1
	}
	if !*once {
		fmt.Fprintf(stdout, "aftermarket hook: stopped, %d event(s) waiting to be sent\n", h.Pending())
	}
	return 0
}

func openLog(path string, stderr io.Writer) (io.Writer, func(), error) {
	switch path {
	case "":
		return nil, func() {}, nil
	case "-":
		return stderr, func() {}, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, err
	}
	return f, func() { f.Close() }, nil
}
