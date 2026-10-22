package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/aftermarket-dev/aftermarket/internal/audit"
	"github.com/aftermarket-dev/aftermarket/internal/httpx"
	"github.com/aftermarket-dev/aftermarket/internal/sqlitecli"
)

// Log events.
const (
	EventServed = "served"
	EventLoaded = "loaded" // a source was read into a new snapshot
	EventKept   = "kept"   // a source couldn't be read; the last snapshot stays
)

// HealthPath is the health check.
const HealthPath = "/_aftermarket/healthz"

// state is what the server knows about one source.
type state struct {
	snap   atomic.Pointer[Snapshot]
	mu     sync.Mutex
	loaded string // fingerprint of the snapshot being served
	failed string // fingerprint and problem last logged, so it is logged once
}

// Server answers requests from the snapshots.
type Server struct {
	cfg    *Config
	runner *sqlitecli.Runner
	log    *audit.Log
	states map[*Source]*state
	reqID  atomic.Uint64
}

// New returns a server with nothing loaded yet.
func New(cfg *Config, r *sqlitecli.Runner, log *audit.Log) *Server {
	s := &Server{cfg: cfg, runner: r, log: log, states: map[*Source]*state{}}
	for _, src := range cfg.Sources {
		s.states[src] = &state{}
	}
	return s
}

// Snapshot returns the snapshot a source is served from, or nil.
func (s *Server) Snapshot(src *Source) *Snapshot { return s.states[src].snap.Load() }

// LoadAll reads every source now. It returns the problems, by source id.
func (s *Server) LoadAll(ctx context.Context) map[string]error {
	probs := map[string]error{}
	for _, src := range s.cfg.Sources {
		if err := s.load(ctx, src, true); err != nil {
			probs[src.ID] = err
		}
	}
	return probs
}

// Check looks at every source once: one whose files changed and have held
// still since the last check is read again.
func (s *Server) Check(ctx context.Context) {
	for _, src := range s.cfg.Sources {
		_ = s.load(ctx, src, false)
	}
}

// Watch checks the sources every interval until ctx ends.
func (s *Server) Watch(ctx context.Context) {
	t := time.NewTicker(s.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Check(ctx)
		}
	}
}

func (s *Server) load(ctx context.Context, src *Source, now bool) error {
	st := s.states[src]
	st.mu.Lock()
	defer st.mu.Unlock()
	fp, _, err := s.cfg.fingerprint(src)
	if err == nil && fp == st.loaded && st.snap.Load() != nil {
		return nil
	}
	var snap *Snapshot
	if err == nil {
		snap, fp, err = s.cfg.Read(ctx, s.runner, src, now)
	}
	if errors.Is(err, ErrNotSettled) {
		return err // read on a later check, once it has settled
	}
	if err != nil {
		if key := fp + "|" + err.Error(); key != st.failed {
			st.failed = key
			reason := err.Error()
			if st.snap.Load() != nil {
				reason += "; still serving the last good snapshot"
			} else {
				reason += "; nothing to serve yet"
			}
			s.log.Write(audit.Event{Event: EventKept, Rule: src.ID, Method: "-", Path: "-", Reason: reason})
		}
		return err
	}
	st.snap.Store(snap)
	st.loaded, st.failed = fp, ""
	s.log.Write(audit.Event{Event: EventLoaded, Rule: src.ID, Method: "-", Path: "-",
		Reason: strconv.Itoa(len(snap.Rows)) + " rows from " + filepath.Base(snap.From)})
	return nil
}

// ServeHTTP answers one request.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := s.reqID.Add(1)
	ev := audit.Event{Event: EventServed, Request: id, Method: r.Method, Path: "-"}
	status := s.serve(w, r, &ev)
	ev.Status = status
	s.log.Write(ev)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request, ev *audit.Event) int {
	if r.URL.Path == HealthPath && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		ev.Path = HealthPath
		httpx.WriteHealth(w)
		return http.StatusOK
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		return writeError(w, r, http.StatusMethodNotAllowed, "only GET and HEAD are answered")
	}
	if httpx.UnsafePath(r.URL) || strings.ContainsAny(r.URL.Path, "\\\x00") {
		return writeError(w, r, http.StatusBadRequest, "the path can't be read one way only")
	}
	ep, param, ok := s.match(r.URL)
	if !ok {
		return writeError(w, r, http.StatusNotFound, "no such endpoint")
	}
	ev.Path = ep.Path
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return writeError(w, r, http.StatusBadRequest, "the query string can't be read")
	}
	for k := range q {
		ev.QueryKeys = append(ev.QueryKeys, k)
	}
	sort.Strings(ev.QueryKeys)
	snap := s.Snapshot(ep.Source)
	if snap == nil {
		w.Header().Set("Retry-After", strconv.Itoa(int(s.cfg.Interval/time.Second)+1))
		return writeError(w, r, http.StatusServiceUnavailable, "source "+ep.Source.ID+" hasn't been read yet")
	}
	if ep.IsItem() {
		if len(q) > 0 {
			return writeError(w, r, http.StatusBadRequest, "this endpoint takes no query parameters")
		}
		key, ok := KeyText(param, ep.Source.TypeOf(ep.Source.Key))
		var row []Cell
		if ok {
			row = snap.Lookup(key)
		}
		if row == nil {
			return writeError(w, r, http.StatusNotFound, "no such "+ep.Source.Key)
		}
		var b bytes.Buffer
		s.writeRow(&b, ep, snap, row)
		b.WriteByte('\n')
		return writeJSON(w, r, http.StatusOK, b.Bytes())
	}
	limit, offset := s.cfg.Limit, 0
	type filter struct {
		col int
		typ Type
		val string
	}
	var filters []filter
	for k, vs := range q {
		if len(vs) != 1 {
			return writeError(w, r, http.StatusBadRequest, k+" is given more than once")
		}
		v := vs[0]
		switch k {
		case "limit", "offset":
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 || (k == "limit" && (n < 1 || n > s.cfg.MaxLimit)) {
				if k == "limit" {
					return writeError(w, r, http.StatusBadRequest, "limit must be a whole number from 1 to "+strconv.Itoa(s.cfg.MaxLimit))
				}
				return writeError(w, r, http.StatusBadRequest, "offset must be a whole number from 0")
			}
			if k == "limit" {
				limit = n
			} else {
				offset = n
			}
			continue
		}
		allowed := false
		for _, f := range ep.Filters {
			if f == k {
				allowed = true
			}
		}
		if !allowed {
			return writeError(w, r, http.StatusBadRequest, "unknown query parameter "+safeName(k))
		}
		col := ep.FieldColumn(k)
		typ := ep.Source.TypeOf(col)
		if _, bad := Match(nullCell, v, typ); bad {
			return writeError(w, r, http.StatusBadRequest, k+" must be "+article(typ))
		}
		filters = append(filters, filter{col: snap.Column(col), typ: typ, val: v})
	}
	var matched []int
	for i, row := range snap.Rows {
		ok := true
		for _, f := range filters {
			if m, _ := Match(row[f.col], f.val, f.typ); !m {
				ok = false
				break
			}
		}
		if ok {
			matched = append(matched, i)
		}
	}
	var b bytes.Buffer
	b.WriteString(`{"items":[`)
	for n, i := range page(matched, offset, limit) {
		if n > 0 {
			b.WriteByte(',')
		}
		s.writeRow(&b, ep, snap, snap.Rows[i])
	}
	b.WriteString(`],"total":` + strconv.Itoa(len(matched)) + `,"limit":` + strconv.Itoa(limit) + `,"offset":` + strconv.Itoa(offset) + "}\n")
	return writeJSON(w, r, http.StatusOK, b.Bytes())
}

func page(rows []int, offset, limit int) []int {
	if offset >= len(rows) {
		return nil
	}
	end := offset + limit
	if end > len(rows) {
		end = len(rows)
	}
	return rows[offset:end]
}

func article(t Type) string {
	switch t {
	case Integer:
		return "a whole number"
	case Number:
		return "a number"
	case Boolean:
		return "true or false"
	case Date:
		return "a date such as 2026-10-22"
	}
	return "text"
}

// safeName keeps an unknown parameter name printable and short in an error.
func safeName(k string) string {
	if len(k) > 40 {
		k = k[:40]
	}
	return strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) {
			return '?'
		}
		return r
	}, strconv.Quote(k))
}

// match finds the endpoint for a path, and the key for an item endpoint.
func (s *Server) match(u *url.URL) (*Endpoint, string, bool) {
	segs := strings.Split(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	for _, ep := range s.cfg.Endpoints {
		want := len(ep.Prefix)
		if ep.IsItem() {
			want++
		}
		if len(segs) != want {
			continue
		}
		ok := true
		for i, p := range ep.Prefix {
			if segs[i] != p {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		if !ep.IsItem() {
			return ep, "", true
		}
		v, err := url.PathUnescape(segs[len(segs)-1])
		if err != nil || v == "" || strings.ContainsFunc(v, unicode.IsControl) {
			continue
		}
		return ep, v, true
	}
	return nil, "", false
}

// writeRow writes one row as a JSON object, fields in the config's order.
func (s *Server) writeRow(b *bytes.Buffer, ep *Endpoint, snap *Snapshot, row []Cell) {
	b.WriteByte('{')
	if len(ep.Fields) == 0 {
		for i, name := range snap.Columns {
			if i > 0 {
				b.WriteByte(',')
			}
			writeKey(b, name)
			b.Write(row[i].JSON)
		}
	} else {
		for i, f := range ep.Fields {
			if i > 0 {
				b.WriteByte(',')
			}
			writeKey(b, f.Name)
			b.Write(row[snap.Column(f.Column)].JSON)
		}
	}
	b.WriteByte('}')
}

func writeKey(b *bytes.Buffer, k string) {
	enc, _ := json.Marshal(k)
	b.Write(enc)
	b.WriteByte(':')
}

func writeJSON(w http.ResponseWriter, r *http.Request, status int, body []byte) int {
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
	return status
}

func writeError(w http.ResponseWriter, r *http.Request, status int, msg string) int {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return writeJSON(w, r, status, append(b, '\n'))
}
