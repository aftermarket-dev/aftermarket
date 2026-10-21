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

	"github.com/aftermarket-dev/aftermarket/internal/adapt"
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

  aftermarket adapt run   --config adapt.yaml --upstream URL [--listen ADDR] [--dry-run]
  aftermarket adapt check --config adapt.yaml
  aftermarket adapt test  --config adapt.yaml --fixture fixture.yaml

  aftermarket record --upstream URL --out fixture.yaml [--config record.yaml] [--listen ADDR]
  aftermarket record check --config record.yaml
  aftermarket replay --fixture fixture.yaml --a URL [--b URL] [--include-writes]

  aftermarket config check --overlay overlay.yaml [--base FILE]
  aftermarket config diff  --overlay overlay.yaml [--base FILE]
  aftermarket config apply --overlay overlay.yaml --out FILE [--base FILE]
  aftermarket config test  --overlay overlay.yaml --fixture fixture.yaml

  aftermarket hook check --config hook.yaml
  aftermarket hook run   --config hook.yaml [--once] [--dry-run] [--log FILE|-]
  aftermarket hook test  --config hook.yaml --fixture fixture.yaml

  aftermarket guard run   --config guard.yaml --upstream URL [--listen ADDR] [--dry-run] [--log FILE|-]
  aftermarket guard check --config guard.yaml
  aftermarket guard test  --config guard.yaml --fixture fixture.yaml
  aftermarket guard hash  < key.txt

  aftermarket schema diff   --old A --new B [--map map.yaml] [--json]
  aftermarket schema compat --map map.yaml --out compat.sql [--new B]
  aftermarket schema check  --queries old.sql [--map map.yaml] [--new B]
  aftermarket schema test   --map map.yaml --queries old.sql [--old A] [--new B]

  aftermarket version

patch fixes requests and responses with small rules. adapt maps old API
endpoints onto new ones. record saves real traffic as fixtures, with
credentials removed, and replay sends them to two versions of a service and
reports the differences. config changes a vendor's config file through an
overlay, without ever editing the file itself. hook watches files, folders,
HTTP endpoints and command output, and sends a signed webhook for each
change. guard stands in front of an app that can't protect itself, and turns
away requests that break its rules. schema compares SQLite schemas and writes
views that keep old queries working after a schema change. Add -h to any command for its options.
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
		return runTool(patchTool, args[1:], stdout, stderr)
	case "adapt":
		return runTool(adaptTool, args[1:], stdout, stderr)
	case "record":
		return runRecord(args[1:], stdout, stderr)
	case "replay":
		return runReplay(args[1:], stdout, stderr)
	case "config":
		return runConfig(args[1:], stdout, stderr)
	case "hook":
		return runHook(args[1:], stdout, stderr)
	case "schema":
		return runSchema(args[1:], stdout, stderr)
	case "guard":
		return runGuard(args[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "aftermarket: unknown command %q\n\n%s", args[0], usage)
	return 2
}

// tool is what differs between patch and adapt; everything else is shared.
type tool struct {
	name string
	file string // what the config file is called in messages
	// load reads the config file and returns the proxy constructor and the
	// lines `check` prints.
	load func(path string) (*loaded, error)
}

type loaded struct {
	count   int
	unit    string
	limits  string
	lines   []string
	notes   []string
	handler func(upstream *url.URL, dryRun bool, log *audit.Log, health string) (http.Handler, error)
	fixture fixture.NewHandler
}

var patchTool = tool{
	name: "patch",
	file: "rule file",
	load: func(path string) (*loaded, error) {
		cfg, err := rules.LoadFile(path)
		if err != nil {
			return nil, err
		}
		l := &loaded{count: len(cfg.Rules), unit: "rule(s)",
			limits: fmt.Sprintf("max_body %d bytes, upstream_timeout %s", cfg.MaxBody, cfg.UpstreamTimeout)}
		for i, r := range cfg.Rules {
			var parts []string
			if r.Request != nil {
				parts = append(parts, "request")
			}
			if r.Response != nil {
				parts = append(parts, "response")
			}
			l.lines = append(l.lines, fmt.Sprintf("%d. %s (line %d, changes %s)", i+1, r.ID, r.Line, strings.Join(parts, " and ")))
		}
		for _, o := range cfg.Overlaps() {
			l.notes = append(l.notes, fmt.Sprintf("%s (line %d) and %s (line %d) can match the same traffic and both change %s; they run in file order",
				o.A.ID, o.A.Line, o.B.ID, o.B.Line, o.Detail))
		}
		l.handler = func(u *url.URL, dryRun bool, log *audit.Log, health string) (http.Handler, error) {
			return patch.New(patch.Options{Upstream: u, Config: cfg, DryRun: dryRun, Audit: log, HealthPath: health})
		}
		l.fixture = patch.FixtureHandler(cfg)
		return l, nil
	},
}

var adaptTool = tool{
	name: "adapt",
	file: "mapping file",
	load: func(path string) (*loaded, error) {
		cfg, err := adapt.LoadFile(path)
		if err != nil {
			return nil, err
		}
		l := &loaded{count: len(cfg.Endpoints), unit: "endpoint(s)",
			limits: fmt.Sprintf("max_body %d bytes, upstream_timeout %s", cfg.MaxBody, cfg.UpstreamTimeout)}
		for i, e := range cfg.Endpoints {
			l.lines = append(l.lines, fmt.Sprintf("%d. %s (line %d): %s", i+1, e.ID, e.Line, e.Summary()))
		}
		for _, o := range cfg.Overlaps() {
			l.notes = append(l.notes, fmt.Sprintf("%s (line %d) and %s (line %d) can match the same requests; %s comes first and wins",
				o.A.ID, o.A.Line, o.B.ID, o.B.Line, o.A.ID))
		}
		l.handler = func(u *url.URL, dryRun bool, log *audit.Log, health string) (http.Handler, error) {
			return adapt.New(adapt.Options{Upstream: u, Config: cfg, DryRun: dryRun, Audit: log, HealthPath: health})
		}
		l.fixture = adapt.FixtureHandler(cfg)
		return l, nil
	},
}

func runTool(t tool, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "run":
		return toolRun(t, args[1:], stdout, stderr)
	case "check":
		return toolCheck(t, args[1:], stdout, stderr)
	case "test":
		return toolTest(t, args[1:], stdout, stderr)
	}
	fmt.Fprintf(stderr, "aftermarket %s: unknown command %q (use run, check or test)\n", t.name, args[0])
	return 2
}

func newFlags(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

func loadConfig(t tool, path string, stderr io.Writer) (*loaded, bool) {
	if path == "" {
		fmt.Fprintln(stderr, "aftermarket: --config is required")
		return nil, false
	}
	l, err := t.load(path)
	if err != nil {
		fmt.Fprintf(stderr, "%s: invalid %s\n", path, t.file)
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
	return l, true
}

func toolCheck(t tool, args []string, stdout, stderr io.Writer) int {
	fs := newFlags("aftermarket "+t.name+" check", stderr)
	config := fs.String("config", "", t.file+" to check")
	if fs.Parse(args) != nil {
		return 2
	}
	l, ok := loadConfig(t, *config, stderr)
	if !ok {
		return 1
	}
	fmt.Fprintf(stdout, "%s: %d %s, %s\n", *config, l.count, l.unit, l.limits)
	for _, line := range l.lines {
		fmt.Fprintf(stdout, "  %s\n", line)
	}
	for _, n := range l.notes {
		fmt.Fprintf(stdout, "  note: %s\n", n)
	}
	fmt.Fprintln(stdout, "ok")
	return 0
}

func toolTest(t tool, args []string, stdout, stderr io.Writer) int {
	fs := newFlags("aftermarket "+t.name+" test", stderr)
	config := fs.String("config", "", t.file)
	fixturePath := fs.String("fixture", "", "fixture file with cases")
	if fs.Parse(args) != nil {
		return 2
	}
	l, ok := loadConfig(t, *config, stderr)
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
	for _, r := range fixture.Run(l.fixture, fx) {
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

func toolRun(t tool, args []string, stdout, stderr io.Writer) int {
	fs := newFlags("aftermarket "+t.name+" run", stderr)
	config := fs.String("config", "", t.file)
	upstream := fs.String("upstream", "", "URL of the service to forward to, e.g. http://127.0.0.1:9000")
	listen := fs.String("listen", "127.0.0.1:8080", "address to listen on")
	dryRun := fs.Bool("dry-run", false, "forward traffic unchanged and only log what would change")
	auditPath := fs.String("audit-log", "-", "where to write the audit log (JSON lines); - for stderr, empty to turn it off")
	health := fs.String("health-path", patch.DefaultHealthPath, "path the proxy answers itself with 200 ok; empty to turn it off")
	if fs.Parse(args) != nil {
		return 2
	}
	l, ok := loadConfig(t, *config, stderr)
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
	log, closeLog, err := openAudit(*auditPath, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "aftermarket: %v\n", err)
		return 1
	}
	defer closeLog()
	h, err := l.handler(u, *dryRun, log, *health)
	if err != nil {
		fmt.Fprintf(stderr, "aftermarket: %v\n", err)
		return 2
	}
	mode := ""
	if *dryRun {
		mode = " (dry run: traffic is not changed)"
	}
	return serve(*listen, h, stdout, stderr, func(addr string) string {
		return fmt.Sprintf("aftermarket %s %s: http://%s -> %s, %d %s%s", t.name, version.Version, addr, u, l.count, l.unit, mode)
	}, func() string { return fmt.Sprintf("aftermarket %s: stopped", t.name) })
}

// openAudit opens the audit log: "" turns it off, "-" is stderr, anything
// else is a file appended to.
func openAudit(path string, stderr io.Writer) (*audit.Log, func(), error) {
	switch path {
	case "":
		return nil, func() {}, nil
	case "-":
		return audit.New(stderr), func() {}, nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, err
	}
	return audit.New(f), func() { f.Close() }, nil
}

// serve runs h until interrupted. started and stopped give the lines printed
// when the server starts (with its address) and after it stops.
func serve(listen string, h http.Handler, stdout, stderr io.Writer, started func(addr string) string, stopped func() string) int {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		fmt.Fprintf(stderr, "aftermarket: %v\n", err)
		return 1
	}
	return serveOn(ln, h, stdout, stderr, started, stopped)
}

// serveOn is serve on a listener that is already open.
func serveOn(ln net.Listener, h http.Handler, stdout, stderr io.Writer, started func(addr string) string, stopped func() string) int {
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute}
	fmt.Fprintln(stdout, started(ln.Addr().String()))

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
	fmt.Fprintln(stdout, stopped())
	return 0
}
