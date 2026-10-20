// Command aftermarket is the Aftermarket toolkit: tools that repair, adapt,
// test and preserve software you can't change.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aftermarket-dev/aftermarket/internal/audit"
	"github.com/aftermarket-dev/aftermarket/internal/fixture"
	"github.com/aftermarket-dev/aftermarket/internal/patch"
	"github.com/aftermarket-dev/aftermarket/internal/rules"
	"github.com/aftermarket-dev/aftermarket/internal/version"
)

const usage = `aftermarket: tools that repair, adapt, test and preserve software you can't change

Usage:
  aftermarket patch run   --config rules.yaml --upstream URL [--listen ADDR] [--dry-run]
  aftermarket patch check --config rules.yaml
  aftermarket patch test  --config rules.yaml --fixture fixture.yaml
  aftermarket version

Run "aftermarket patch <command> -h" for the options of a command.
Manual: https://github.com/aftermarket-dev/aftermarket
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "aftermarket %s\n", version.Version)
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	case "patch":
		return runPatch(args[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "aftermarket: unknown command %q\n\n%s", args[0], usage)
	return 2
}

func runPatch(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "run":
		return patchRun(args[1:], stdout, stderr)
	case "check":
		return patchCheck(args[1:], stdout, stderr)
	case "test":
		return patchTest(args[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "aftermarket patch: unknown command %q (use run, check or test)\n", args[0])
	return 2
}

func newFlags(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

func loadRules(path string, stderr io.Writer) (*rules.Config, bool) {
	if path == "" {
		fmt.Fprintln(stderr, "aftermarket: --config is required")
		return nil, false
	}
	cfg, err := rules.LoadFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "%s: invalid rule file\n", path)
		var errs rules.Errors
		if errors.As(err, &errs) {
			for _, e := range errs {
				fmt.Fprintf(stderr, "  %s\n", e)
			}
		} else {
			fmt.Fprintf(stderr, "  %s\n", err)
		}
		return nil, false
	}
	return cfg, true
}

func patchCheck(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("aftermarket patch check", stderr)
	config := fs.String("config", "", "rule file to check")
	if fs.Parse(args) != nil {
		return 2
	}
	cfg, ok := loadRules(*config, stderr)
	if !ok {
		return 1
	}
	fmt.Fprintf(stdout, "%s: %d rule(s), max_body %d bytes, upstream_timeout %s\n", *config, len(cfg.Rules), cfg.MaxBody, cfg.UpstreamTimeout)
	for i, r := range cfg.Rules {
		var parts []string
		if r.Request != nil {
			parts = append(parts, "request")
		}
		if r.Response != nil {
			parts = append(parts, "response")
		}
		fmt.Fprintf(stdout, "  %d. %s (line %d, changes %s)\n", i+1, r.ID, r.Line, strings.Join(parts, " and "))
	}
	for _, o := range cfg.Overlaps() {
		fmt.Fprintf(stdout, "  note: %s (line %d) and %s (line %d) can match the same traffic and both change %s; they run in file order\n",
			o.A.ID, o.A.Line, o.B.ID, o.B.Line, o.Detail)
	}
	fmt.Fprintln(stdout, "ok")
	return 0
}

func patchTest(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("aftermarket patch test", stderr)
	config := fs.String("config", "", "rule file")
	fixturePath := fs.String("fixture", "", "fixture file with cases")
	if fs.Parse(args) != nil {
		return 2
	}
	cfg, ok := loadRules(*config, stderr)
	if !ok {
		return 1
	}
	if *fixturePath == "" {
		fmt.Fprintln(stderr, "aftermarket: --fixture is required")
		return 2
	}
	fx, err := fixture.Load(*fixturePath)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", *fixturePath, err)
		return 1
	}
	failed := 0
	for _, r := range fixture.Run(patch.FixtureHandler(cfg), fx) {
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

func patchRun(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("aftermarket patch run", stderr)
	config := fs.String("config", "", "rule file")
	upstream := fs.String("upstream", "", "URL of the service to forward to, e.g. http://127.0.0.1:9000")
	listen := fs.String("listen", "127.0.0.1:8080", "address to listen on")
	dryRun := fs.Bool("dry-run", false, "forward traffic unchanged and only log what would change")
	auditPath := fs.String("audit-log", "-", "where to write the audit log (JSON lines); - for stderr, empty to turn it off")
	health := fs.String("health-path", patch.DefaultHealthPath, "path the proxy answers itself with 200 ok; empty to turn it off")
	if fs.Parse(args) != nil {
		return 2
	}
	cfg, ok := loadRules(*config, stderr)
	if !ok {
		return 1
	}
	if *upstream == "" {
		fmt.Fprintln(stderr, "aftermarket: --upstream is required")
		return 2
	}
	u, err := url.Parse(*upstream)
	if err != nil {
		fmt.Fprintf(stderr, "aftermarket: bad --upstream: %v\n", err)
		return 2
	}
	var log *audit.Log
	switch *auditPath {
	case "":
	case "-":
		log = audit.New(stderr)
	default:
		f, err := os.OpenFile(*auditPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			fmt.Fprintf(stderr, "aftermarket: %v\n", err)
			return 1
		}
		defer f.Close()
		log = audit.New(f)
	}
	px, err := patch.New(patch.Options{Upstream: u, Config: cfg, DryRun: *dryRun, Audit: log, HealthPath: *health})
	if err != nil {
		fmt.Fprintf(stderr, "aftermarket: %v\n", err)
		return 2
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintf(stderr, "aftermarket: %v\n", err)
		return 1
	}
	srv := &http.Server{Handler: px, ReadHeaderTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute}
	mode := ""
	if *dryRun {
		mode = " (dry run: traffic is not changed)"
	}
	fmt.Fprintf(stdout, "aftermarket patch %s: http://%s -> %s, %d rule(s)%s\n", version.Version, ln.Addr(), u, len(cfg.Rules), mode)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	select {
	case err := <-errc:
		fmt.Fprintf(stderr, "aftermarket: %v\n", err)
		return 1
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	fmt.Fprintln(stdout, "aftermarket patch: stopped")
	return 0
}
