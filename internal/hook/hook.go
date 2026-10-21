package hook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aftermarket-dev/aftermarket/internal/version"
)

// Event is what a webhook receives.
type Event struct {
	ID       string   `json:"id"`
	Source   string   `json:"source"`
	Kind     string   `json:"kind"`
	Sequence uint64   `json:"sequence"`
	Time     string   `json:"time"`
	Changes  []Change `json:"changes"`
}

// State is kept in the state file between runs.
type State struct {
	Version int                     `json:"version"`
	Sources map[string]*SourceState `json:"sources"`
	Queue   []*Delivery             `json:"queue"`
}

// SourceState is what a source looked like last, and how many changes it
// has had.
type SourceState struct {
	Spec string   `json:"spec"` // the source's definition; a new one starts a new baseline
	Base string   `json:"base"` // random, new with each baseline, so event ids never repeat across baselines
	Seq  uint64   `json:"seq"`
	Snap Snapshot `json:"snapshot"`
}

// Delivery is an event waiting to be sent to one webhook.
type Delivery struct {
	Webhook   string          `json:"webhook"`
	EventID   string          `json:"event_id"`
	Body      json.RawMessage `json:"body"`
	Attempts  int             `json:"attempts"`
	NextAt    time.Time       `json:"next_at"`
	LastError string          `json:"last_error,omitempty"`
}

// Options configure a Hook.
type Options struct {
	DryRun bool      // print events instead of saving and sending them
	Out    io.Writer // dry-run events go here
	Log    io.Writer // JSON lines; nil discards
	Now    func() time.Time
	Sleep  func(time.Duration)
	Client *http.Client
	Env    func(string) string
}

// Hook watches the sources of one config.
type Hook struct {
	cfg     *Config
	opts    Options
	state   *State
	secrets map[string]string
	client  *http.Client
	now     func() time.Time
	sleep   func(time.Duration)
	nextAt  map[string]time.Time
	confirm map[string]string // a removal or big shrink seen once, waiting for a second look
	logMu   sync.Mutex
}

// New loads the state file and checks that every webhook's secret is set.
func New(cfg *Config, opts Options) (*Hook, error) {
	h := &Hook{cfg: cfg, opts: opts, now: opts.Now, sleep: opts.Sleep, client: opts.Client, secrets: map[string]string{}, nextAt: map[string]time.Time{}, confirm: map[string]string{}}
	if h.now == nil {
		h.now = time.Now
	}
	if h.sleep == nil {
		h.sleep = time.Sleep
	}
	if h.client == nil {
		h.client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	env := opts.Env
	if env == nil {
		env = os.Getenv
	}
	for _, w := range cfg.Webhooks {
		if w.SecretEnv == "" {
			continue
		}
		v := env(w.SecretEnv)
		if v == "" {
			return nil, fmt.Errorf("webhook %s signs with the secret in $%s, which is not set", w.ID, w.SecretEnv)
		}
		h.secrets[w.ID] = v
	}
	st, err := loadState(cfg.State)
	if err != nil {
		return nil, err
	}
	h.state = st
	return h, nil
}

func loadState(path string) (*State, error) {
	st := &State{Version: 1, Sources: map[string]*SourceState{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, st); err != nil || st.Version != 1 {
		// Never start over silently: the queue may hold events not yet sent.
		return nil, fmt.Errorf("the state file %s can't be read; move it aside to start from a new baseline (its queue may hold unsent events)", path)
	}
	if st.Sources == nil {
		st.Sources = map[string]*SourceState{}
	}
	// The state file is written indented, and that indents the stored bodies
	// too. Compact them again, so an event sent after a restart is the same
	// bytes as one sent at once.
	for _, d := range st.Queue {
		if d == nil || len(d.Body) == 0 || d.Webhook == "" {
			return nil, fmt.Errorf("the state file %s can't be read: a queued event has no body or webhook; move it aside to start from a new baseline (its queue may hold unsent events)", path)
		}
		var buf bytes.Buffer
		if json.Compact(&buf, d.Body) == nil {
			d.Body = buf.Bytes()
		}
	}
	return st, nil
}

// save writes the state file in one step, readable only by its owner, since
// snapshots can hold file contents.
func (h *Hook) save() error {
	if h.opts.DryRun {
		return nil
	}
	data, err := json.MarshalIndent(h.state, "", " ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(h.cfg.State)
	tmp, err := os.CreateTemp(dir, ".aftermarket-hook-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, h.cfg.State); err != nil {
		os.Remove(name)
		return err
	}
	// Make the rename itself durable where the system allows it.
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

func newBase() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b)
}

// logLine is one line of the log. It never holds values.
type logLine struct {
	Time     string `json:"time"`
	Event    string `json:"event"`
	Source   string `json:"source,omitempty"`
	Webhook  string `json:"webhook,omitempty"`
	EventID  string `json:"event_id,omitempty"`
	Changes  int    `json:"changes,omitempty"`
	Attempt  int    `json:"attempt,omitempty"`
	Status   int    `json:"status,omitempty"`
	Reason   string `json:"reason,omitempty"`
	RetryIn  string `json:"retry_in,omitempty"`
	Sequence uint64 `json:"sequence,omitempty"`
}

func (h *Hook) log(l logLine) {
	if h.opts.Log == nil {
		return
	}
	l.Time = h.now().UTC().Format(time.RFC3339Nano)
	b, _ := json.Marshal(l)
	h.logMu.Lock()
	h.opts.Log.Write(append(b, '\n'))
	h.logMu.Unlock()
}

func spec(s *Source) string {
	u := ""
	if s.URL != nil {
		u = s.URL.String()
	}
	b, _ := json.Marshal([]any{s.Kind, s.Path, s.Format, s.Pattern, s.Recursive, u, s.Run})
	return hashOf(b)
}

// eventID is derived from the change itself, so a change seen again after a
// crash, before the state was saved, gets the same id. The baseline's random
// base keeps ids from repeating after a new baseline restarts the sequence.
func eventID(source, base string, seq uint64, before, after string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%s\x00%s", source, base, seq, before, after)))
	return hex.EncodeToString(sum[:12])
}

// Poll checks every source that is due and queues an event for each change.
// It returns the events it found.
func (h *Hook) Poll(ctx context.Context) ([]Event, error) {
	now := h.now()
	var events []Event
	dirty := false
	known := map[string]bool{}
	for _, s := range h.cfg.Sources {
		known[s.ID] = true
		if next, ok := h.nextAt[s.ID]; ok && now.Before(next) {
			continue
		}
		h.nextAt[s.ID] = now.Add(s.Interval)
		snap, err := h.look(ctx, s)
		switch {
		case errors.Is(err, errNotReady):
			h.log(logLine{Event: "not_ready", Source: s.ID, Reason: "the source is changing; checking again next time"})
			continue
		case errors.Is(err, errTooBig):
			h.log(logLine{Event: "source_error", Source: s.ID, Reason: "larger than max_body"})
			continue
		case err != nil:
			h.log(logLine{Event: "source_error", Source: s.ID, Reason: describe(err)})
			continue
		}
		if snap.Busy {
			h.log(logLine{Event: "not_ready", Source: s.ID, Reason: "a file in the folder is still being written; it is left out until it settles"})
		}
		st := h.state.Sources[s.ID]
		if st == nil || st.Spec != spec(s) {
			h.state.Sources[s.ID] = &SourceState{Spec: spec(s), Base: newBase(), Snap: snap}
			delete(h.confirm, s.ID)
			dirty = true
			h.log(logLine{Event: "baseline", Source: s.ID})
			continue
		}
		if suspicious(s, st.Snap, snap) {
			// A file that vanishes for a moment, or a structured file cut
			// short, is only reported when the next check sees the same.
			key := fmt.Sprint(snap.Exists, snap.Hash)
			if h.confirm[s.ID] != key {
				h.confirm[s.ID] = key
				h.nextAt[s.ID] = now.Add(min(s.Interval, time.Second))
				h.log(logLine{Event: "not_ready", Source: s.ID, Reason: "the source shrank or disappeared; checking again before reporting it"})
				continue
			}
		}
		delete(h.confirm, s.ID)
		changes := diff(s, st.Snap, snap)
		if len(changes) == 0 {
			if st.Snap.Hash != snap.Hash || st.Snap.Exists != snap.Exists || st.Snap.Status != snap.Status {
				st.Snap = snap // same by meaning; keep the new bytes as the reference
				dirty = true
			} else if s.Kind == "folder" && fmt.Sprint(st.Snap.Files) != fmt.Sprint(snap.Files) {
				st.Snap = snap // only modification times moved
				dirty = true
			}
			continue
		}
		seq := st.Seq + 1
		ev := Event{ID: eventID(s.ID, st.Base, seq, st.Snap.Hash, snap.Hash), Source: s.ID, Kind: s.Kind, Sequence: seq,
			Time: now.UTC().Format(time.RFC3339), Changes: changes}
		events = append(events, ev)
		if h.opts.DryRun {
			// Nothing is saved or sent, but the run moves on, so each change
			// is printed once.
			if h.opts.Out != nil {
				b, _ := json.Marshal(ev)
				fmt.Fprintf(h.opts.Out, "%s\n", b)
			}
			st.Seq, st.Snap = seq, snap
			continue
		}
		body, _ := json.Marshal(ev)
		for _, w := range s.SendTo { // send_to holds each webhook once
			h.state.Queue = append(h.state.Queue, &Delivery{Webhook: w, EventID: ev.ID, Body: body, NextAt: now})
		}
		st.Seq, st.Snap = seq, snap
		dirty = true
		h.log(logLine{Event: "changed", Source: s.ID, EventID: ev.ID, Changes: len(changes), Sequence: seq})
	}
	for id := range h.state.Sources {
		if !known[id] {
			delete(h.state.Sources, id)
			dirty = true
		}
	}
	if dirty {
		if err := h.save(); err != nil {
			return events, fmt.Errorf("could not save the state file: %w", err)
		}
	}
	return events, nil
}

// Deliver sends what is due, oldest first. Each webhook gets its events in
// order: while the oldest one waits for a retry, the later ones wait too. The
// state file is saved once at the end; a crash before that only means some
// events are sent again, which at-least-once delivery allows.
func (h *Hook) Deliver(ctx context.Context) (err error) {
	dirty := false
	defer func() {
		if dirty {
			if serr := h.save(); serr != nil && err == nil {
				err = fmt.Errorf("could not save the state file: %w", serr)
			}
		}
	}()
	blocked := map[string]bool{}
	i := 0
	for i < len(h.state.Queue) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		d := h.state.Queue[i]
		if blocked[d.Webhook] || h.now().Before(d.NextAt) {
			blocked[d.Webhook] = true
			i++
			continue
		}
		w := h.webhook(d.Webhook)
		if w == nil {
			// The webhook was removed from the config; keep the event as dead.
			h.dead(d, "the webhook is no longer in the config")
			h.state.Queue = append(h.state.Queue[:i], h.state.Queue[i+1:]...)
			dirty = true
			continue
		}
		status, serr := h.send(ctx, w, d)
		if serr != nil && ctx.Err() != nil {
			// Stopped mid-send: this attempt doesn't count, and the event
			// stays queued for the next run.
			return ctx.Err()
		}
		d.Attempts++
		dirty = true
		if serr == nil {
			h.log(logLine{Event: "delivered", Webhook: w.ID, EventID: d.EventID, Attempt: d.Attempts, Status: status})
			h.state.Queue = append(h.state.Queue[:i], h.state.Queue[i+1:]...)
			continue
		}
		d.LastError = serr.Error()
		if h.cfg.MaxAttempts > 0 && d.Attempts >= h.cfg.MaxAttempts {
			h.dead(d, d.LastError)
			h.state.Queue = append(h.state.Queue[:i], h.state.Queue[i+1:]...)
			continue
		}
		wait := h.backoff(d.Attempts)
		d.NextAt = h.now().Add(wait)
		blocked[d.Webhook] = true
		h.log(logLine{Event: "retry", Webhook: w.ID, EventID: d.EventID, Attempt: d.Attempts, Status: status, Reason: d.LastError, RetryIn: wait.String()})
		i++
	}
	return nil
}

func (h *Hook) backoff(attempts int) time.Duration {
	d := h.cfg.Backoff
	for i := 1; i < attempts && d < h.cfg.MaxBackoff; i++ {
		d *= 2
	}
	return min(d, h.cfg.MaxBackoff)
}

func (h *Hook) webhook(id string) *Webhook {
	for _, w := range h.cfg.Webhooks {
		if w.ID == id {
			return w
		}
	}
	return nil
}

// dead appends an undeliverable event to the dead-letter file next to the
// state file.
func (h *Hook) dead(d *Delivery, reason string) {
	h.log(logLine{Event: "dead", Webhook: d.Webhook, EventID: d.EventID, Attempt: d.Attempts, Reason: reason})
	line, _ := json.Marshal(map[string]any{"webhook": d.Webhook, "event_id": d.EventID, "attempts": d.Attempts, "last_error": reason, "event": d.Body})
	f, err := os.OpenFile(DeadLetterPath(h.cfg), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	f.Write(append(line, '\n'))
	f.Close()
}

// DeadLetterPath is where events that could not be delivered go.
func DeadLetterPath(c *Config) string { return c.State + ".dead.jsonl" }

// Sign returns the signature header value for a body sent at ts.
func Sign(secret string, ts int64, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(m, "%d.", ts)
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

func (h *Hook) send(ctx context.Context, w *Webhook, d *Delivery) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, w.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL.String(), bytes.NewReader(d.Body))
	if err != nil {
		return 0, errors.New("the request could not be built")
	}
	for _, kv := range w.Headers {
		req.Header.Set(kv[0], kv[1])
	}
	ts := h.now().Unix()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "aftermarket-hook/"+version.Version)
	req.Header.Set("X-Aftermarket-Event", d.EventID)
	req.Header.Set("X-Aftermarket-Timestamp", strconv.FormatInt(ts, 10))
	if secret, ok := h.secrets[w.ID]; ok {
		req.Header.Set("X-Aftermarket-Signature", Sign(secret, ts, d.Body))
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return 0, errors.New(describe(err))
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, fmt.Errorf("the webhook answered %d", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

// describe turns an error into fixed words, so nothing from a file, a
// command or a server reaches the log.
func describe(err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
		return "no answer within the timeout"
	case errors.Is(err, os.ErrPermission):
		return "permission denied"
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return "could not connect"
	}
	msg := err.Error()
	for _, fixed := range []string{"is not a folder", "the command", "could not get an answer", "the answer was cut off"} {
		if strings.Contains(msg, fixed) {
			return msg
		}
	}
	return "the source could not be read"
}

// suspicious says whether a change could be a moment in the middle of a
// rewrite: a source that disappeared, or a structured file that lost more
// than half its size.
func suspicious(s *Source, a, b Snapshot) bool {
	if a.Exists && !b.Exists {
		return true
	}
	return s.Kind == "file" && s.Format != "" && a.Exists && b.Exists && b.Size*2 < a.Size
}

// Pending is how many deliveries are waiting.
func (h *Hook) Pending() int { return len(h.state.Queue) }

// Run checks and delivers until ctx ends. With once, it does one round.
func (h *Hook) Run(ctx context.Context, once bool) error {
	for {
		if _, err := h.Poll(ctx); err != nil {
			return err
		}
		if err := h.Deliver(ctx); err != nil && ctx.Err() == nil {
			return err
		}
		if once {
			return nil
		}
		wait := h.untilNext()
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
	}
}

// untilNext is how long to wait before something is due.
func (h *Hook) untilNext() time.Duration {
	now := h.now()
	next := now.Add(time.Minute)
	for _, s := range h.cfg.Sources {
		if t, ok := h.nextAt[s.ID]; ok && t.Before(next) {
			next = t
		}
	}
	// Only the oldest event of each webhook can be sent next; the ones
	// behind it wait for it, whatever their own time says.
	head := map[string]bool{}
	for _, d := range h.state.Queue {
		if head[d.Webhook] {
			continue
		}
		head[d.Webhook] = true
		if d.NextAt.Before(next) {
			next = d.NextAt
		}
	}
	return max(next.Sub(now), 50*time.Millisecond)
}
