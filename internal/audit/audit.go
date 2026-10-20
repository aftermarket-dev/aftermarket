// Package audit writes one JSON line per change the proxy makes, would make
// (dry run), or decided not to make.
//
// Events never contain header values, JSON values, query values or bodies:
// only rule ids, the request method and path, status codes, the names of
// what was touched and the reason something was skipped. That keeps secrets
// out of the log without a list of sensitive names to maintain.
package audit

import (
	"bytes"
	"encoding/json"
	"io"
	"sync"
	"time"
)

// Event kinds.
const (
	Changed       = "changed"        // the rule changed traffic
	WouldChange   = "would_change"   // dry run: the rule would have changed traffic
	Skipped       = "skipped"        // the rule matched but could not be applied safely
	Unchanged     = "unchanged"      // the rule matched but nothing needed changing
	UpstreamError = "upstream_error" // the upstream failed; the proxy answered itself
	Recorded      = "recorded"       // record: the exchange was written to the fixture file
)

// Event is one audit record.
type Event struct {
	Time       string   `json:"time"`
	Event      string   `json:"event"`
	Request    uint64   `json:"request"`
	Rule       string   `json:"rule,omitempty"`
	Phase      string   `json:"phase,omitempty"`
	Method     string   `json:"method"`
	Path       string   `json:"path"`
	QueryKeys  []string `json:"query_keys,omitempty"`
	Status     int      `json:"status,omitempty"`
	Actions    []string `json:"actions,omitempty"`
	NotApplied []string `json:"not_applied,omitempty"`
	Reason     string   `json:"reason,omitempty"`
}

// Log writes events as JSON lines. A nil *Log discards everything.
type Log struct {
	mu  sync.Mutex
	w   io.Writer
	now func() time.Time
}

// New returns a log writing to w.
func New(w io.Writer) *Log {
	return &Log{w: w, now: time.Now}
}

// Write records one event. Errors writing the log are ignored so a full disk
// never takes traffic down.
func (l *Log) Write(e Event) {
	if l == nil || l.w == nil {
		return
	}
	e.Time = l.now().UTC().Format(time.RFC3339Nano)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(e); err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.w.Write(buf.Bytes())
}
