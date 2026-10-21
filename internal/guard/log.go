package guard

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
	"sync"
	"time"
)

// Decisions, as the log names them.
const (
	Allowed  = "allowed"
	Denied   = "denied"   // a rule or the default turned it away
	Rejected = "rejected" // the request could be read two ways, or broke a size or time limit
	Limited  = "limited"  // over a rate limit
	Refused  = "refused"  // a connection over a connection limit

	LimitEvicted   = "limit_evicted"    // counted only
	LimitTableFull = "limit_table_full" // counted only
)

// Entry is one line of the decision log. It never holds a credential, a
// cookie or a query value.
type Entry struct {
	Time      string   `json:"time"`
	Event     string   `json:"event"`
	DryRun    bool     `json:"dry_run,omitempty"` // the request went through anyway
	Request   uint64   `json:"request,omitempty"`
	Rule      string   `json:"rule,omitempty"`
	Status    int      `json:"status,omitempty"`
	Method    string   `json:"method,omitempty"`
	Path      string   `json:"path,omitempty"`
	QueryKeys []string `json:"query_keys,omitempty"`
	Address   string   `json:"address,omitempty"`
	Client    string   `json:"client,omitempty"`
	Reason    string   `json:"reason,omitempty"`
	Count     uint64   `json:"count,omitempty"` // summary lines
	Since     string   `json:"since,omitempty"`
}

// Log writes decisions as JSON lines. Past a number of turned-away requests
// in one second, it counts the rest and writes one summary line for them, so
// a flood can't fill the disk. A nil *Log writes nothing.
type Log struct {
	mu        sync.Mutex
	w         io.Writer
	now       func() time.Time
	perSecond int

	window     time.Time // the second being counted
	inWindow   int
	suppressed map[string]uint64
	since      time.Time
}

// DefaultLogPerSecond is how many turned-away requests are logged one by one
// each second before the rest are summarised.
const DefaultLogPerSecond = 20

// NewLog returns a log writing to w.
func NewLog(w io.Writer, now func() time.Time) *Log {
	if now == nil {
		now = time.Now
	}
	return &Log{w: w, now: now, perSecond: DefaultLogPerSecond, suppressed: map[string]uint64{}}
}

// Write logs one entry.
func (l *Log) Write(e Entry) {
	if l == nil || l.w == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	sec := now.Truncate(time.Second)
	if !sec.Equal(l.window) {
		l.flushLocked(now)
		l.window, l.inWindow = sec, 0
	}
	if e.Event != Allowed {
		if l.inWindow >= l.perSecond {
			if len(l.suppressed) == 0 {
				l.since = now
			}
			l.suppressed[e.Event]++
			return
		}
		l.inWindow++
	}
	e.Time = now.UTC().Format(time.RFC3339Nano)
	l.writeLocked(e)
}

// Count notes an event that is only ever logged as a count, once a second,
// such as a rate-limit bucket dropped to make room.
func (l *Log) Count(event string) {
	if l == nil || l.w == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.suppressed) == 0 {
		l.since = l.now()
	}
	l.suppressed[event]++
}

// Flush writes the summary of anything counted but not logged yet.
func (l *Log) Flush() {
	if l == nil || l.w == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.flushLocked(l.now())
}

func (l *Log) flushLocked(now time.Time) {
	events := make([]string, 0, len(l.suppressed))
	for ev := range l.suppressed {
		events = append(events, ev)
	}
	sort.Strings(events)
	for _, ev := range events {
		reason := "more than the log takes one by one"
		switch ev {
		case LimitEvicted:
			reason = "rate-limit buckets that had refilled were dropped to make room"
		case LimitTableFull:
			reason = "the rate-limit table was full of buckets in use, so new keys were refused"
		}
		l.writeLocked(Entry{Time: now.UTC().Format(time.RFC3339Nano), Event: ev + "_summary", Count: l.suppressed[ev],
			Since: l.since.UTC().Format(time.RFC3339Nano), Reason: reason})
	}
	clear(l.suppressed)
}

func (l *Log) writeLocked(e Entry) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if enc.Encode(e) != nil {
		return
	}
	l.w.Write(buf.Bytes()) // a full disk must never take traffic down
}
