package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/aftermarket-dev/aftermarket/internal/audit"
	"github.com/aftermarket-dev/aftermarket/internal/bridge"
	"github.com/aftermarket-dev/aftermarket/internal/config"
	"github.com/aftermarket-dev/aftermarket/internal/fixture"
	"github.com/aftermarket-dev/aftermarket/internal/rules"
	"github.com/aftermarket-dev/aftermarket/internal/sqlitecli"
	"github.com/aftermarket-dev/aftermarket/internal/version"
)

func runBridge(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	cmd := args[0]
	if cmd != "serve" && cmd != "check" && cmd != "convert" && cmd != "test" {
		fmt.Fprintf(stderr, "aftermarket bridge: unknown command %q (use serve, check, convert or test)\n", cmd)
		return 2
	}
	fs := newFlags("aftermarket bridge "+cmd, stderr)
	configPath := fs.String("config", "", "bridge config file")
	listen := fs.String("listen", "127.0.0.1:8080", "serve: address to listen on")
	logPath := fs.String("log", "-", "serve: where to write the log (JSON lines); - for stderr, empty to turn it off")
	source := fs.String("source", "", "convert: the source to write")
	out := fs.String("out", "", "convert: the JSON file to write")
	fixturePath := fs.String("fixture", "", "test: fixture file with cases")
	if fs.Parse(args[1:]) != nil {
		return 2
	}
	switch {
	case *configPath == "":
		fmt.Fprintln(stderr, "aftermarket: --config is required")
		return 2
	case cmd == "convert" && (*source == "" || *out == ""):
		fmt.Fprintln(stderr, "aftermarket: --source and --out are required")
		return 2
	case cmd == "test" && *fixturePath == "":
		fmt.Fprintln(stderr, "aftermarket: --fixture is required")
		return 2
	}
	cfg, err := bridge.LoadFile(*configPath)
	if err != nil {
		fmt.Fprintf(stderr, "%s: invalid bridge config\n", *configPath)
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
	r := sqlitecli.New()
	needSQLite := false
	for _, s := range cfg.Sources {
		if !s.IsCSV() {
			needSQLite = true
		}
	}
	if needSQLite {
		if err := r.Available(); err != nil {
			fmt.Fprintf(stderr, "aftermarket: %v\n", err)
			return 1
		}
	}
	ctx := context.Background()

	switch cmd {
	case "check":
		srv := bridge.New(cfg, r, nil)
		probs := srv.LoadAll(ctx)
		fmt.Fprintf(stdout, "%s: %d source(s), %d endpoint(s)\n", *configPath, len(cfg.Sources), len(cfg.Endpoints))
		for _, s := range cfg.Sources {
			if err := probs[s.ID]; err != nil {
				fmt.Fprintf(stdout, "  source %s: problem: %v\n", s.ID, err)
				continue
			}
			snap := srv.Snapshot(s)
			fmt.Fprintf(stdout, "  source %s: %d row(s), %d column(s) from %s\n", s.ID, len(snap.Rows), len(snap.Columns), filepath.Base(snap.From))
		}
		for _, e := range cfg.Endpoints {
			kind := "list"
			if e.IsItem() {
				kind = "one " + e.Source.Key
			}
			extra := ""
			if len(e.Filters) > 0 {
				extra = ", filters " + strings.Join(e.Filters, ", ")
			}
			fmt.Fprintf(stdout, "  %s -> %s (%s%s)\n", e.Path, e.Source.ID, kind, extra)
		}
		if len(probs) > 0 {
			fmt.Fprintf(stdout, "%d source(s) can't be served\n", len(probs))
			return 1
		}
		fmt.Fprintln(stdout, "ok")
		return 0

	case "convert":
		var src *bridge.Source
		for _, s := range cfg.Sources {
			if s.ID == *source {
				src = s
			}
		}
		if src == nil {
			fmt.Fprintf(stderr, "aftermarket: no source called %s\n", *source)
			return 2
		}
		inputs := []string{*configPath, src.File, src.DB}
		if src.DB != "" {
			inputs = append(inputs, src.DB+"-wal", src.DB+"-shm", src.DB+"-journal")
		}
		if src.Folder != "" {
			if abs, err := filepath.Abs(*out); err == nil && filepath.Dir(abs) == filepath.Clean(src.Folder) {
				if ok, _ := filepath.Match(src.Pattern, filepath.Base(abs)); ok {
					fmt.Fprintf(stderr, "aftermarket: --out would be read as an export of source %s; choose another name or folder\n", src.ID)
					return 1
				}
			}
		}
		snap, _, err := cfg.Read(ctx, r, src, true)
		if err != nil {
			fmt.Fprintf(stderr, "aftermarket: source %s: %v; nothing was written\n", src.ID, err)
			return 1
		}
		inputs = append(inputs, snap.From)
		for _, in := range inputs {
			if in != "" && config.SameFile(in, *out) {
				fmt.Fprintf(stderr, "aftermarket: --out is one of the inputs (%s); AfterBridge never writes its sources, so choose another file\n", in)
				return 1
			}
		}
		var b bytes.Buffer
		b.WriteString("[")
		for i, row := range snap.Rows {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString("\n{")
			for j, name := range snap.Columns {
				if j > 0 {
					b.WriteByte(',')
				}
				k, _ := json.Marshal(name)
				b.Write(k)
				b.WriteByte(':')
				b.Write(row[j].JSON)
			}
			b.WriteString("}")
		}
		b.WriteString("\n]\n")
		if err := config.WriteFile(*out, b.Bytes(), 0o644); err != nil {
			fmt.Fprintf(stderr, "aftermarket: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "wrote %s: %d row(s) of %s from %s\n", *out, len(snap.Rows), src.ID, filepath.Base(snap.From))
		return 0

	case "test":
		fx, err := fixture.Load(*fixturePath)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", *fixturePath, err)
			return 1
		}
		srv := bridge.New(cfg, r, nil)
		probs := srv.LoadAll(ctx)
		ids := make([]string, 0, len(probs))
		for id := range probs {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			fmt.Fprintf(stdout, "note: source %s can't be served: %v\n", id, probs[id])
		}
		failed := 0
		for _, res := range fixture.Run(func(*url.URL) (http.Handler, error) { return srv, nil }, fx) {
			if res.Pass {
				fmt.Fprintf(stdout, "PASS  %s\n", res.Name)
				continue
			}
			failed++
			fmt.Fprintf(stdout, "FAIL  %s\n", res.Name)
			for _, p := range res.Problems {
				fmt.Fprintf(stdout, "      %s\n", p)
			}
		}
		fmt.Fprintf(stdout, "%d case(s), %d failed\n", len(fx.Cases), failed)
		if failed > 0 {
			return 1
		}
		return 0
	}

	// serve
	logw, closeLog, err := openLog(*logPath, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "aftermarket: %v\n", err)
		return 1
	}
	defer closeLog()
	log := audit.New(logw)
	srv := bridge.New(cfg, r, log)
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintf(stderr, "aftermarket: %v\n", err)
		return 1
	}
	probs := srv.LoadAll(ctx)
	fmt.Fprintf(stdout, "aftermarket bridge %s: http://%s, %d source(s), %d endpoint(s)\n", version.Version, ln.Addr(), len(cfg.Sources), len(cfg.Endpoints))
	for _, s := range cfg.Sources {
		if err := probs[s.ID]; err != nil {
			fmt.Fprintf(stdout, "  source %s: not served yet: %v\n", s.ID, err)
		}
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	go srv.Watch(ctx)
	hs := &http.Server{Handler: srv, ReadHeaderTimeout: 30 * time.Second, ReadTimeout: time.Minute, WriteTimeout: 2 * time.Minute, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 64 << 10}
	errc := make(chan error, 1)
	go func() { errc <- hs.Serve(ln) }()
	select {
	case err := <-errc:
		fmt.Fprintf(stderr, "aftermarket: %v\n", err)
		return 1
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = hs.Shutdown(shutdownCtx)
		cancel()
		fmt.Fprintln(stdout, "aftermarket bridge: stopped")
		return 0
	}
}
