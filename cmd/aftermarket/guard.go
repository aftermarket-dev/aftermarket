package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aftermarket-dev/aftermarket/internal/guard"
	"github.com/aftermarket-dev/aftermarket/internal/rules"
	"github.com/aftermarket-dev/aftermarket/internal/version"
)

// stdin is where guard hash reads from; tests replace it.
var stdin io.Reader = os.Stdin

func runGuard(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd := args[0]
	if cmd == "hash" {
		return guardHash(args[1:], stdout, stderr)
	}
	if cmd != "check" && cmd != "run" && cmd != "test" {
		fmt.Fprintf(stderr, "aftermarket guard: unknown command %q (use check, run, test or hash)\n", cmd)
		return 2
	}
	fs := newFlags("aftermarket guard "+cmd, stderr)
	configPath := fs.String("config", "", "guard config file")
	upstream := fs.String("upstream", "", "run: the app's address, such as http://127.0.0.1:9000")
	listen := fs.String("listen", "127.0.0.1:8080", "run: address to listen on")
	dryRun := fs.Bool("dry-run", false, "run: forward everything and log what would have been turned away")
	logPath := fs.String("log", "-", "run: where to write the decision log (JSON lines); - for stderr, empty to turn it off")
	health := fs.String("health-path", guard.DefaultHealthPath, "run: path AfterGuard answers itself with 200 ok; empty to turn it off")
	fixturePath := fs.String("fixture", "", "test: fixture file with cases")
	if fs.Parse(args[1:]) != nil {
		return 2
	}
	if *configPath == "" {
		fmt.Fprintln(stderr, "aftermarket: --config is required")
		return 2
	}
	if cmd == "test" && *fixturePath == "" {
		fmt.Fprintln(stderr, "aftermarket: --fixture is required")
		return 2
	}
	if cmd == "run" && *upstream == "" {
		fmt.Fprintln(stderr, "aftermarket: --upstream is required")
		return 2
	}
	cfg, err := guard.LoadFile(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "%s: invalid guard config\n", *configPath)
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
		def := "deny"
		if cfg.DefaultAllow {
			def = "allow"
		}
		fmt.Fprintf(stdout, "%s: %d rule(s), %d client(s), default %s\n", *configPath, len(cfg.Rules), len(cfg.Clients), def)
		for i, r := range cfg.Rules {
			fmt.Fprintf(stdout, "  %d. %s (line %d): %s\n", i+1, r.ID, r.Line, summary(r))
		}
		for _, w := range cfg.Warnings {
			fmt.Fprintf(stdout, "  warning: %s\n", w)
		}
		fmt.Fprintln(stdout, "  reminder: the app must be reachable only through AfterGuard, or these rules can be walked around")
		fmt.Fprintln(stdout, "ok")
		return 0
	case "test":
		cases, err := guard.LoadFixture(*fixturePath)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", *fixturePath, err)
			return 1
		}
		results, err := cfg.RunFixture(cases)
		if err != nil {
			fmt.Fprintf(stderr, "aftermarket: %v\n", err)
			return 1
		}
		failed := 0
		for _, r := range results {
			if r.Pass {
				fmt.Fprintf(stdout, "PASS  %s\n", r.Name)
				continue
			}
			failed++
			fmt.Fprintf(stdout, "FAIL  %s\n      want %s\n      got  %s\n", r.Name, r.Want, r.Got)
		}
		fmt.Fprintf(stdout, "%d case(s), %d failed\n", len(results), failed)
		if failed > 0 {
			return 1
		}
		return 0
	}
	u, err := url.Parse(*upstream)
	if err != nil {
		fmt.Fprintf(stderr, "aftermarket: --upstream: %v\n", err)
		return 2
	}
	logw, closeLog, err := openLog(*logPath, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "aftermarket: %v\n", err)
		return 1
	}
	defer closeLog()
	log := guard.NewLog(logw, nil)
	g, err := guard.New(guard.Options{Config: cfg, Upstream: u, DryRun: *dryRun, Log: log, HealthPath: *health})
	if err != nil {
		fmt.Fprintf(stderr, "aftermarket: %v\n", err)
		return 1
	}
	raw, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintf(stderr, "aftermarket: %v\n", err)
		return 1
	}
	ln, err := g.Listener(raw)
	if err != nil {
		raw.Close()
		fmt.Fprintf(stderr, "aftermarket: %v\n", err)
		return 1
	}
	srv := g.Server()
	scheme, mode := "http", ""
	if cfg.TLSCert != "" {
		scheme = "https"
	}
	if *dryRun {
		mode = " (dry run: nothing is turned away)"
	}
	fmt.Fprintf(stdout, "aftermarket guard %s: %s://%s -> %s, %d rule(s)%s\n", version.Version, scheme, raw.Addr(), safeUpstream(u), len(cfg.Rules), mode)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case err := <-errc:
			log.Flush()
			fmt.Fprintf(stderr, "aftermarket: %v\n", err)
			return 1
		case <-tick.C:
			log.Flush()
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			_ = srv.Shutdown(shutdownCtx)
			cancel()
			log.Flush()
			fmt.Fprintln(stdout, "aftermarket guard: stopped")
			return 0
		}
	}
}

func safeUpstream(u *url.URL) string {
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}).String()
}

func summary(r *guard.Rule) string {
	var parts []string
	if r.Allow {
		parts = append(parts, "allow")
	} else {
		parts = append(parts, fmt.Sprintf("deny %d", r.DenyStatus))
	}
	if r.When.PathGlob != "" {
		parts = append(parts, "path "+r.When.PathGlob)
	}
	if len(r.When.Methods) > 0 {
		parts = append(parts, strings.Join(r.When.Methods, ","))
	}
	if len(r.From) > 0 {
		var f []string
		for _, p := range r.From {
			f = append(f, p.String())
		}
		parts = append(parts, "from "+strings.Join(f, ", "))
	}
	if len(r.Clients) > 0 {
		parts = append(parts, "client "+strings.Join(r.Clients, ", "))
	}
	if r.Limit != nil {
		parts = append(parts, fmt.Sprintf("limit %s per %s", r.Limit.Rate, r.Limit.Per))
	}
	if r.MaxBody > 0 {
		parts = append(parts, fmt.Sprintf("body up to %d bytes", r.MaxBody))
	}
	if r.JSON {
		parts = append(parts, "JSON body")
	}
	return strings.Join(parts, ", ")
}

// guardHash prints the SHA-256 of a key or password read from standard
// input, so the secret never lands in shell history or the config.
func guardHash(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintln(stderr, "aftermarket guard hash: type or pipe the key on standard input, not as an argument")
		return 2
	}
	line, err := bufio.NewReader(stdin).ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(stderr, "aftermarket guard hash: nothing on standard input")
		return 2
	}
	secret := strings.TrimRight(line, "\r\n")
	if secret == "" {
		fmt.Fprintln(stderr, "aftermarket guard hash: the key is empty")
		return 2
	}
	if len(secret) < 16 {
		fmt.Fprintln(stderr, "aftermarket guard hash: note: keys and passwords this short can be guessed; use 16 characters or more")
	}
	fmt.Fprintln(stdout, guard.Hash(secret))
	return 0
}
