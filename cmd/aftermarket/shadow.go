package main

import (
	"fmt"
	"io"
	"net"
	"net/url"

	"github.com/aftermarket-dev/aftermarket/internal/httpx"
	"github.com/aftermarket-dev/aftermarket/internal/patch"
	"github.com/aftermarket-dev/aftermarket/internal/replay"
	"github.com/aftermarket-dev/aftermarket/internal/shadow"
	"github.com/aftermarket-dev/aftermarket/internal/version"
)

// runShadow is AfterDiff's live mode: serve from the primary, compare copies
// with the candidate in the background.
func runShadow(args []string, stdout, stderr io.Writer) int {
	fs := newFlags("aftermarket shadow", stderr)
	primary := fs.String("primary", "", "URL of the service that answers clients, e.g. http://127.0.0.1:9001")
	candidate := fs.String("candidate", "", "URL of the service that gets copies to compare, e.g. http://127.0.0.1:9002")
	config := fs.String("config", "", "record config (optional): when picks the traffic, redact keeps values out of the report, limits")
	listen := fs.String("listen", "127.0.0.1:8080", "address to listen on")
	reportPath := fs.String("report", "shadow-report.md", "Markdown report, rewritten while running; empty for none")
	jsonPath := fs.String("json", "", "also keep the report as JSON in this file")
	sample := fs.Float64("sample", 1, "share of requests to copy, more than 0 and at most 1 (0.1 copies one in ten)")
	queue := fs.Int("queue", shadow.DefaultQueue, "copies that may wait for a worker; more are dropped and counted")
	workers := fs.Int("workers", shadow.DefaultWorkers, "copies sent to the candidate at once")
	ctimeout := fs.Duration("candidate-timeout", shadow.DefaultCandidateTimeout, "time allowed for each candidate answer")
	writes := fs.Bool("include-writes", false, "also copy requests other than GET and HEAD (the candidate may change data)")
	auditPath := fs.String("audit-log", "-", "where to write the audit log (JSON lines); - for stderr, empty to turn it off")
	health := fs.String("health-path", patch.DefaultHealthPath, "path the proxy answers itself with 200 ok; empty to turn it off")
	var ignore, headers listFlag
	fs.Var(&ignore, "ignore", "JSON dot path to leave out of the comparison, e.g. data.*.updated_at (repeatable)")
	fs.Var(&headers, "header", "response header to compare (repeatable; default Content-Type)")
	if fs.Parse(args) != nil {
		return 2
	}
	if *primary == "" || *candidate == "" {
		fmt.Fprintln(stderr, "aftermarket: --primary and --candidate are required")
		return 2
	}
	if *reportPath == "" && *jsonPath == "" {
		fmt.Fprintln(stderr, "aftermarket: with --report empty, give --json, or nothing is kept")
		return 2
	}
	cfg, ok := loadRecordConfig(*config, stderr)
	if !ok {
		return 1
	}
	o := shadow.Options{Config: cfg, HealthPath: *health, Sample: *sample, Queue: *queue, Workers: *workers,
		CandidateTimeout: *ctimeout, IncludeWrites: *writes, ReportPath: *reportPath, JSONPath: *jsonPath}
	if *sample <= 0 || *sample > 1 {
		fmt.Fprintln(stderr, "aftermarket: --sample must be more than 0 and at most 1, such as 0.1")
		return 2
	}
	var err error
	if o.Primary, err = url.Parse(*primary); err != nil {
		fmt.Fprintf(stderr, "aftermarket: bad --primary: %v\n", err)
		return 2
	}
	if o.Candidate, err = url.Parse(*candidate); err != nil {
		fmt.Fprintf(stderr, "aftermarket: bad --candidate: %v\n", err)
		return 2
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
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintf(stderr, "aftermarket: %v\n", err)
		return 1
	}
	defer ln.Close()
	log, closeLog, err := openAudit(*auditPath, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "aftermarket: %v\n", err)
		return 1
	}
	defer closeLog()
	o.Audit = log
	sh, err := shadow.New(o)
	if err != nil {
		fmt.Fprintf(stderr, "aftermarket: %v\n", err)
		return 2
	}
	where := *reportPath
	if where == "" {
		where = *jsonPath
	}
	return serveOn(ln, sh, stdout, stderr, func(addr string) string {
		return fmt.Sprintf("aftermarket shadow %s: http://%s -> %s, copies to %s, report %s", version.Version, addr, httpx.SafeURL(o.Primary), httpx.SafeURL(o.Candidate), where)
	}, func() string {
		if err := sh.Close(); err != nil {
			fmt.Fprintf(stderr, "aftermarket: the report could not be written: %v\n", err)
		}
		r := sh.Report()
		return fmt.Sprintf("aftermarket shadow: stopped, %s; %d dropped (queue full), report %s", r.Summary(), r.Shadow.Dropped, where)
	})
}
