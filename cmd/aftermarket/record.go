package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/aftermarket-dev/aftermarket/internal/fixture"
	"github.com/aftermarket-dev/aftermarket/internal/httpx"
	"github.com/aftermarket-dev/aftermarket/internal/patch"
	"github.com/aftermarket-dev/aftermarket/internal/record"
	"github.com/aftermarket-dev/aftermarket/internal/replay"
	"github.com/aftermarket-dev/aftermarket/internal/rules"
	"github.com/aftermarket-dev/aftermarket/internal/version"
)

// listFlag collects a flag given more than once.
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(s string) error { *l = append(*l, s); return nil }

func loadRecordConfig(path string, stderr io.Writer) (*record.Config, bool) {
	if path == "" {
		return record.Default(), true
	}
	cfg, err := record.LoadFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "%s: invalid record config\n", path)
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

func describeRecordConfig(cfg *record.Config) []string {
	var out []string
	if cfg.When.Empty() {
		out = append(out, "records: all traffic")
	} else {
		out = append(out, "records: traffic that matches when:")
	}
	var hs []string
	for h := range cfg.RedactHeaders {
		hs = append(hs, h)
	}
	sort.Strings(hs)
	out = append(out, "redacts headers: "+strings.Join(hs, ", "))
	if len(cfg.RedactQuery) > 0 {
		var qs []string
		for q := range cfg.RedactQuery {
			qs = append(qs, q)
		}
		sort.Strings(qs)
		out = append(out, "redacts query keys: "+strings.Join(qs, ", "))
	}
	if len(cfg.RedactJSON) > 0 {
		var js []string
		for _, p := range cfg.RedactJSON {
			js = append(js, strings.Join(p, "."))
		}
		out = append(out, "redacts JSON fields: "+strings.Join(js, ", "))
	}
	limit := "no limit"
	if cfg.MaxCases > 0 {
		limit = fmt.Sprintf("%d", cfg.MaxCases)
	}
	out = append(out, fmt.Sprintf("max_body %d bytes, upstream_timeout %s, max_cases %s", cfg.MaxBody, cfg.UpstreamTimeout, limit))
	return out
}

func runRecord(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "check" {
		fs := newFlags("aftermarket record check", stderr)
		config := fs.String("config", "", "record config to check")
		if fs.Parse(args[1:]) != nil {
			return 2
		}
		if *config == "" {
			fmt.Fprintln(stderr, "aftermarket: --config is required")
			return 2
		}
		cfg, ok := loadRecordConfig(*config, stderr)
		if !ok {
			return 1
		}
		fmt.Fprintf(stdout, "%s:\n", *config)
		for _, l := range describeRecordConfig(cfg) {
			fmt.Fprintf(stdout, "  %s\n", l)
		}
		fmt.Fprintln(stdout, "ok")
		return 0
	}
	fs := newFlags("aftermarket record", stderr)
	config := fs.String("config", "", "record config (optional): when, redact, limits, max_cases")
	upstream := fs.String("upstream", "", "URL of the service to forward to, e.g. http://127.0.0.1:9000")
	out := fs.String("out", "", "fixture file to write; must not exist yet")
	listen := fs.String("listen", "127.0.0.1:8080", "address to listen on")
	auditPath := fs.String("audit-log", "-", "where to write the audit log (JSON lines); - for stderr, empty to turn it off")
	health := fs.String("health-path", patch.DefaultHealthPath, "path the proxy answers itself with 200 ok; empty to turn it off")
	if fs.Parse(args) != nil {
		return 2
	}
	cfg, ok := loadRecordConfig(*config, stderr)
	if !ok {
		return 1
	}
	if *upstream == "" || *out == "" {
		fmt.Fprintln(stderr, "aftermarket: --upstream and --out are required")
		return 2
	}
	u, err := url.Parse(*upstream)
	if err != nil {
		fmt.Fprintf(stderr, "aftermarket: bad --upstream: %v\n", err)
		return 2
	}
	if _, err := os.Stat(*out); err == nil {
		fmt.Fprintf(stderr, "aftermarket: %s already exists; record never overwrites a fixture, so choose another --out\n", *out)
		return 1
	}
	// Listen first, so a busy port doesn't leave an empty fixture behind.
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintf(stderr, "aftermarket: %v\n", err)
		return 1
	}
	defer ln.Close()
	f, err := os.OpenFile(*out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			fmt.Fprintf(stderr, "aftermarket: %s already exists; record never overwrites a fixture, so choose another --out\n", *out)
		} else {
			fmt.Fprintf(stderr, "aftermarket: %v\n", err)
		}
		return 1
	}
	defer f.Close()
	log, closeLog, err := openAudit(*auditPath, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "aftermarket: %v\n", err)
		return 1
	}
	defer closeLog()
	rec, err := record.New(record.Options{Upstream: u, Config: cfg, Out: f, Audit: log, HealthPath: *health})
	if err != nil {
		os.Remove(*out)
		fmt.Fprintf(stderr, "aftermarket: %v\n", err)
		return 2
	}
	return serveOn(ln, rec, stdout, stderr, func(addr string) string {
		return fmt.Sprintf("aftermarket record %s: http://%s -> %s, writing %s", version.Version, addr, httpx.SafeURL(u), *out)
	}, func() string {
		return fmt.Sprintf("aftermarket record: stopped, %d case(s) written to %s", rec.Cases(), *out)
	})
}

func runReplay(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("aftermarket replay", stderr)
	fixturePath := fs.String("fixture", "", "fixture file with the requests to send")
	a := fs.String("a", "", "URL of the first service")
	b := fs.String("b", "", "URL of the second service; without it, answers are compared with the recorded ones")
	writes := fs.Bool("include-writes", false, "also send requests other than GET and HEAD (they may change data)")
	timeout := fs.Duration("timeout", 30*time.Second, "time allowed for each answer")
	reportPath := fs.String("report", "", "write the Markdown report to this file instead of stdout")
	jsonPath := fs.String("json", "", "also write the report as JSON to this file")
	var ignore, headers, set listFlag
	fs.Var(&ignore, "ignore", "JSON dot path to leave out of the comparison, e.g. data.*.updated_at (repeatable)")
	fs.Var(&headers, "header", "response header to compare (repeatable; default Content-Type)")
	fs.Var(&set, "set-header", `header to send with every request, e.g. "Authorization: Bearer x" (repeatable)`)
	if fs.Parse(args) != nil {
		return 2
	}
	if *fixturePath == "" || *a == "" {
		fmt.Fprintln(stderr, "aftermarket: --fixture and --a are required")
		return 2
	}
	o := replay.Options{IncludeWrites: *writes, Timeout: *timeout}
	var err error
	if o.A, err = url.Parse(*a); err != nil {
		fmt.Fprintf(stderr, "aftermarket: bad --a: %v\n", err)
		return 2
	}
	if *b != "" {
		if o.B, err = url.Parse(*b); err != nil {
			fmt.Fprintf(stderr, "aftermarket: bad --b: %v\n", err)
			return 2
		}
	}
	for _, s := range ignore {
		p, err := replay.ParsePattern(s)
		if err != nil {
			fmt.Fprintf(stderr, "aftermarket: --ignore: %v\n", err)
			return 2
		}
		o.Ignore = append(o.Ignore, p)
	}
	if len(headers) > 0 {
		o.Headers = append([]string(nil), headers...)
	}
	for _, s := range set {
		k, v, ok := strings.Cut(s, ":")
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if !ok || !rules.ValidHeaderName(k) || !rules.ValidHeaderValue(v) {
			fmt.Fprintf(stderr, "aftermarket: --set-header wants \"Name: value\"\n")
			return 2
		}
		o.SetHeaders = append(o.SetHeaders, [2]string{k, v})
	}
	fx, err := fixture.Load(*fixturePath)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", *fixturePath, err)
		return 1
	}
	rep, err := replay.Run(context.Background(), fx, o)
	if err != nil {
		fmt.Fprintf(stderr, "aftermarket: %v\n", err)
		return 2
	}
	if *reportPath != "" {
		if err := writeFile(*reportPath, rep.WriteMarkdown); err != nil {
			fmt.Fprintf(stderr, "aftermarket: %v\n", err)
			return 1
		}
		fmt.Fprintln(stdout, rep.Summary())
	} else if err := rep.WriteMarkdown(stdout); err != nil {
		return 1
	}
	if *jsonPath != "" {
		if err := writeFile(*jsonPath, rep.WriteJSON); err != nil {
			fmt.Fprintf(stderr, "aftermarket: %v\n", err)
			return 1
		}
	}
	if !rep.OK() {
		return 1
	}
	return 0
}

func writeFile(path string, write func(io.Writer) error) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := write(f); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
