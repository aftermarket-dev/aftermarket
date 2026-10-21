package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// clock is a fake clock tests move by hand.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// receiver records what a webhook receives and can fail on purpose.
type receiver struct {
	mu     sync.Mutex
	bodies [][]byte
	ids    []string
	fail   int // answer 503 to this many requests first
	secret string
	bad    int // requests with a wrong signature
}

func (r *receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.secret != "" {
		var ts int64
		fmt.Sscan(req.Header.Get("X-Aftermarket-Timestamp"), &ts)
		if Sign(r.secret, ts, body) != req.Header.Get("X-Aftermarket-Signature") {
			r.bad++
			http.Error(w, "bad signature", 401)
			return
		}
	}
	if r.fail > 0 {
		r.fail--
		http.Error(w, "down", 503)
		return
	}
	r.bodies = append(r.bodies, body)
	r.ids = append(r.ids, req.Header.Get("X-Aftermarket-Event"))
}

func (r *receiver) events(t *testing.T) []Event {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Event
	for _, b := range r.bodies {
		var e Event
		if err := json.Unmarshal(b, &e); err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return out
}

type env struct {
	dir   string
	cfg   *Config
	clock *clock
	recv  *receiver
	log   *bytes.Buffer
	url   string
}

func setup(t *testing.T, sources string) *env {
	t.Helper()
	e := &env{dir: t.TempDir(), clock: &clock{t: time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)}, recv: &receiver{secret: "test-secret"}, log: &bytes.Buffer{}}
	srv := httptest.NewServer(e.recv)
	t.Cleanup(srv.Close)
	e.url = srv.URL
	cfg, err := Load([]byte(fmt.Sprintf(`state: state.json
interval: 1s
settle: 1ms
retry: {backoff: 1s, max_backoff: 8s}
webhooks:
  - id: w
    url: %s/events
    secret_env: TEST_SECRET
sources:
%s`, srv.URL, sources)), e.dir)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	e.cfg = cfg
	return e
}

func (e *env) hook(t *testing.T, opts ...func(*Options)) *Hook {
	t.Helper()
	o := Options{Log: e.log, Now: e.clock.now, Sleep: func(time.Duration) {}, Env: func(k string) string {
		if k == "TEST_SECRET" {
			return "test-secret"
		}
		return ""
	}}
	for _, f := range opts {
		f(&o)
	}
	h, err := New(e.cfg, o)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// round moves the clock past every interval and runs one check and delivery.
func (e *env) round(t *testing.T, h *Hook) []Event {
	t.Helper()
	e.clock.add(10 * time.Second)
	ev, err := h.Poll(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Deliver(context.Background()); err != nil {
		t.Fatal(err)
	}
	return ev
}

func (e *env) write(t *testing.T, name, content string) {
	t.Helper()
	p := filepath.Join(e.dir, name)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func seed(t *testing.T) int64 {
	s := time.Now().UnixNano()
	if v := os.Getenv("AFTERMARKET_SEED"); v != "" {
		fmt.Sscan(v, &s)
	}
	t.Logf("seed %d (set AFTERMARKET_SEED to repeat)", s)
	return s
}

// TestEveryChangeGivesExactlyOneEvent makes random changes to a JSON file,
// some of which change nothing by meaning (spacing, key order, an ignored
// field). The test keeps its own count of the changes that matter: each must
// give exactly one event, delivered once, in order, and the rest none.
func TestEveryChangeGivesExactlyOneEvent(t *testing.T) {
	e := setup(t, "  - id: f\n    file: {path: status.json}\n    ignore: [updated_at]\n")
	rs := rand.New(rand.NewSource(seed(t)))
	statuses := []string{"open", "paid", "shipped"}
	status, count, stamp := "open", 1, "0"
	write := func() {
		var b string
		if rs.Intn(2) == 0 {
			b = fmt.Sprintf(`{"status":%q,"count":%d,"updated_at":%q}`, status, count, stamp)
		} else { // the same document with other spacing, key order and number form
			b = fmt.Sprintf(" { \"updated_at\": %q,\n\"count\": %d.0, \"status\": %q } ", stamp, count, status)
		}
		e.write(t, "status.json", b)
	}
	write()
	h := e.hook(t)
	if ev := e.round(t, h); len(ev) != 0 {
		t.Fatal("the first check must only set the baseline")
	}
	type key struct {
		s string
		c int
	}
	last := key{status, count}
	want := 0
	for i := 0; i < 200; i++ {
		switch rs.Intn(4) {
		case 0:
			status = statuses[rs.Intn(3)]
		case 1:
			count = rs.Intn(3)
		case 2:
			stamp = fmt.Sprint(i) // ignored
		}
		write()
		got := e.round(t, h)
		now := key{status, count}
		expect := 0
		if now != last {
			expect = 1
		}
		if len(got) != expect {
			t.Fatalf("round %d: %d events, want %d (%v -> %v)", i, len(got), expect, last, now)
		}
		last = now
		want += expect
	}
	evs := e.recv.events(t)
	if len(evs) != want || want == 0 {
		t.Fatalf("received %d events, want %d", len(evs), want)
	}
	ids := map[string]bool{}
	for i, ev := range evs {
		if ev.Sequence != uint64(i+1) || ids[ev.ID] {
			t.Fatalf("event %d: sequence %d, id %s seen before: %v", i, ev.Sequence, ev.ID, ids[ev.ID])
		}
		ids[ev.ID] = true
		for _, c := range ev.Changes {
			if c.Path == "updated_at" {
				t.Fatal("an ignored field was reported")
			}
		}
	}
	if e.recv.bad != 0 {
		t.Fatalf("%d requests had a bad signature", e.recv.bad)
	}
	for i := 0; i < 3; i++ {
		if ev := e.round(t, h); len(ev) != 0 {
			t.Fatalf("an unchanged file gave %d events", len(ev))
		}
	}
}

// TestNothingIsLostWhileTheReceiverIsDown: the receiver fails for a while,
// and the tool is restarted twice on the same state file in between. Every
// event must arrive, in order. Repeats are allowed, but only with the same id.
func TestNothingIsLostWhileTheReceiverIsDown(t *testing.T) {
	e := setup(t, "  - id: f\n    file: {path: n.txt}\n")
	e.recv.fail = 7
	e.write(t, "n.txt", "0")
	h := e.hook(t)
	e.round(t, h)
	for i := 1; i <= 6; i++ {
		e.write(t, "n.txt", fmt.Sprint(i))
		e.round(t, h)
		if i == 2 || i == 4 {
			h = e.hook(t) // a restart: everything comes from the state file
		}
	}
	for i := 0; i < 20 && h.Pending() > 0; i++ {
		e.round(t, h)
	}
	if h.Pending() != 0 {
		t.Fatalf("%d events still waiting", h.Pending())
	}
	var seqs []uint64
	firstID := map[uint64]string{}
	for _, ev := range e.recv.events(t) {
		if id, ok := firstID[ev.Sequence]; ok {
			if id != ev.ID {
				t.Fatalf("a repeat of sequence %d has a new id", ev.Sequence)
			}
			continue
		}
		firstID[ev.Sequence] = ev.ID
		seqs = append(seqs, ev.Sequence)
	}
	if fmt.Sprint(seqs) != "[1 2 3 4 5 6]" {
		t.Fatalf("delivered sequences %v", seqs)
	}
	if !strings.Contains(e.log.String(), `"event":"retry"`) {
		t.Fatal("no retries were logged")
	}
}

// TestAChangeSeenAgainAfterACrashKeepsItsID: if the tool dies after finding
// a change but before saving, it finds the change again on restart, with the
// same event id, so a receiver can drop the repeat.
func TestAChangeSeenAgainAfterACrashKeepsItsID(t *testing.T) {
	e := setup(t, "  - id: f\n    file: {path: n.txt}\n")
	e.write(t, "n.txt", "a")
	h := e.hook(t)
	e.round(t, h)
	saved, _ := os.ReadFile(e.cfg.State)
	e.write(t, "n.txt", "b")
	first := e.round(t, h)
	os.WriteFile(e.cfg.State, saved, 0o600) // as if the save never happened
	second := e.round(t, e.hook(t))
	if len(first) != 1 || len(second) != 1 || first[0].ID != second[0].ID {
		t.Fatalf("first %+v, second %+v", first, second)
	}
}

// TestHalfWrittenFilesAreNeverReported: a structured file that doesn't read,
// or any file that changes between the two reads, waits for the next check.
func TestHalfWrittenFilesAreNeverReported(t *testing.T) {
	e := setup(t, "  - id: j\n    file: {path: s.json}\n  - id: t\n    file: {path: log.txt}\n")
	e.write(t, "s.json", `{"status": "open"}`)
	e.write(t, "log.txt", "line 1\n")
	writing := false
	h := e.hook(t, func(o *Options) {
		o.Sleep = func(time.Duration) {
			if writing { // a writer appends while the hook waits between reads
				f, _ := os.OpenFile(filepath.Join(e.dir, "log.txt"), os.O_APPEND|os.O_WRONLY, 0)
				f.WriteString("more\n")
				f.Close()
			}
		}
	})
	e.round(t, h)
	e.write(t, "s.json", `{"status": "pa`)
	writing = true
	if ev := e.round(t, h); len(ev) != 0 {
		t.Fatalf("half-written files were reported: %+v", ev)
	}
	if !strings.Contains(e.log.String(), `"not_ready"`) {
		t.Fatal("not logged as not ready")
	}
	writing = false
	e.write(t, "s.json", `{"status": "paid"}`)
	ev := e.round(t, h)
	if len(ev) != 2 {
		t.Fatalf("got %d events: %+v", len(ev), ev)
	}
	if ev[0].Changes[0].Path != "status" || ev[0].Changes[0].New != "paid" {
		t.Fatalf("json event %+v", ev[0])
	}
}

func TestFolders(t *testing.T) {
	e := setup(t, "  - id: d\n    folder: {path: out, pattern: \"*.csv\", recursive: true}\n")
	e.write(t, "out/a.csv", "1")
	e.write(t, "out/notes.txt", "x")
	h := e.hook(t)
	e.round(t, h)
	e.write(t, "out/b.csv", "2")
	e.write(t, "out/sub/c.csv", "3")
	e.write(t, "out/a.csv", "1 changed")
	e.write(t, "out/notes.txt", "ignored by the pattern")
	ev := e.round(t, h)
	if len(ev) != 1 {
		t.Fatalf("%d events", len(ev))
	}
	var got []string
	for _, c := range ev[0].Changes {
		got = append(got, c.Op+" "+c.Path)
	}
	if strings.Join(got, ", ") != "changed a.csv, added b.csv, added sub/c.csv" {
		t.Fatalf("changes: %v", got)
	}
	os.Remove(filepath.Join(e.dir, "out/b.csv"))
	later := time.Now().Add(time.Hour)
	os.Chtimes(filepath.Join(e.dir, "out/a.csv"), later, later) // touched, same content
	ev = e.round(t, h)
	if len(ev) != 1 || len(ev[0].Changes) != 1 || ev[0].Changes[0].Op != "removed" {
		t.Fatalf("removal: %+v", ev)
	}
	if ev := e.round(t, h); len(ev) != 0 {
		t.Fatalf("a touched file gave an event: %+v", ev)
	}
	// A file still growing while the hook waits is not reported yet.
	growing := true
	h2 := e.hook(t, func(o *Options) {
		o.Sleep = func(time.Duration) {
			if growing {
				f, _ := os.OpenFile(filepath.Join(e.dir, "out/big.csv"), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
				f.WriteString("row\n")
				f.Close()
			}
		}
	})
	e.write(t, "out/big.csv", "row\n")
	if ev := e.round(t, h2); len(ev) != 0 {
		t.Fatalf("a growing file was reported: %+v", ev)
	}
	growing = false
	if ev := e.round(t, h2); len(ev) != 1 || ev[0].Changes[0].Path != "big.csv" {
		t.Fatalf("the finished file: %+v", ev)
	}
}

func TestHTTPSources(t *testing.T) {
	var mu sync.Mutex
	body, status := `{"a": 1, "b": [1, 2]}`, 200
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("X-Key") != "k" {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	defer up.Close()
	e := setup(t, fmt.Sprintf("  - id: h\n    http: {url: %q, headers: {X-Key: k}}\n", up.URL))
	h := e.hook(t)
	e.round(t, h)
	set := func(b string, s int) { mu.Lock(); body, status = b, s; mu.Unlock() }
	set(`{"b": [1, 2], "a": 1.0}`, 200)
	if ev := e.round(t, h); len(ev) != 0 {
		t.Fatal("the same JSON gave an event")
	}
	set(`{"a": 2, "b": [1, 2, 3]}`, 503)
	ev := e.round(t, h)
	if len(ev) != 1 {
		t.Fatalf("%d events", len(ev))
	}
	var got []string
	for _, c := range ev[0].Changes {
		got = append(got, Describe(c))
	}
	if strings.Join(got, "; ") != `changed status: 200 -> 503; added b.2: 3; changed a: 1.0 -> 2` {
		t.Fatalf("%v", got)
	}
	up.Close()
	if ev := e.round(t, h); len(ev) != 0 {
		t.Fatal("an unreachable source gave an event")
	}
	if !strings.Contains(e.log.String(), `"source_error"`) {
		t.Fatal("no source_error logged")
	}
}

// TestHelperProcess is the command the command-source tests run.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("HOOK_HELPER") == "" {
		return
	}
	b, _ := os.ReadFile(os.Getenv("HOOK_HELPER"))
	if string(b) == "fail" {
		os.Exit(3)
	}
	if string(b) == "hang" {
		time.Sleep(10 * time.Second)
	}
	os.Stdout.Write(b)
	os.Exit(0)
}

func TestCommandSources(t *testing.T) {
	dir := t.TempDir()
	input := filepath.Join(dir, "out.txt")
	t.Setenv("HOOK_HELPER", input)
	e := setup(t, fmt.Sprintf("  - id: c\n    command: {run: [%q, -test.run=TestHelperProcess], timeout: 2s}\n", os.Args[0]))
	write := func(s string) { os.WriteFile(input, []byte(s), 0o644) }
	write(`[{"id": 1, "status": "open"}]`)
	h := e.hook(t)
	e.round(t, h)
	write(`[{"id": 1, "status": "paid"}, {"id": 2, "status": "open"}]`)
	ev := e.round(t, h)
	if len(ev) != 1 || len(ev[0].Changes) != 2 {
		t.Fatalf("%+v", ev)
	}
	write("fail")
	if ev := e.round(t, h); len(ev) != 0 {
		t.Fatal("a failing command gave an event")
	}
	write("hang")
	if ev := e.round(t, h); len(ev) != 0 {
		t.Fatal("a command that timed out gave an event")
	}
	write("plain text\nline two\n")
	ev = e.round(t, h)
	if len(ev) != 1 || ev[0].Changes[len(ev[0].Changes)-1].New != "line two" {
		t.Fatalf("text output: %+v", ev)
	}
	log := e.log.String()
	if !strings.Contains(log, "exited with status 3") || !strings.Contains(log, "did not finish within its timeout") {
		t.Fatalf("log:\n%s", log)
	}
}

// TestSecretsStayOut: no secret value reaches an event, the log or the
// state file's readers; the state file is readable by its owner only.
func TestSecretsStayOut(t *testing.T) {
	e := setup(t, "  - id: f\n    file: {path: c.json}\n")
	e.write(t, "c.json", `{"db": {"password": "pw-1", "url": "postgres://u:url-pw-2@h/db"}, "token": "t-3", "level": 1}`)
	h := e.hook(t)
	e.round(t, h)
	e.write(t, "c.json", `{"db": {"password": "pw-4", "url": "postgres://u:url-pw-5@h/db2"}, "token": "t-6", "level": 2, "extra": {"api_key": "k-7"}}`)
	e.round(t, h)
	var all bytes.Buffer
	for _, b := range e.recv.bodies {
		all.Write(b)
	}
	all.WriteString(e.log.String())
	for _, s := range []string{"pw-1", "url-pw-2", "t-3", "pw-4", "url-pw-5", "t-6", "k-7", "test-secret"} {
		if strings.Contains(all.String(), s) {
			t.Errorf("%s reached an event or the log", s)
		}
	}
	if !strings.Contains(all.String(), "postgres://REDACTED@h/db2") {
		t.Errorf("the URL change is missing:\n%s", all.String())
	}
	if fi, _ := os.Stat(e.cfg.State); fi.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode %v", fi.Mode().Perm())
	}
	if b, _ := os.ReadFile(e.cfg.State); bytes.Contains(b, []byte("test-secret")) {
		t.Fatal("the signing secret is in the state file")
	}
}

func TestDeadLetters(t *testing.T) {
	e := setup(t, "  - id: f\n    file: {path: n.txt}\n")
	e.cfg.MaxAttempts = 5
	e.recv.fail = 1000
	e.write(t, "n.txt", "0")
	h := e.hook(t)
	e.round(t, h)
	e.write(t, "n.txt", "1")
	for i := 0; i < 10; i++ {
		e.round(t, h)
	}
	if h.Pending() != 0 {
		t.Fatalf("%d still pending after max_attempts", h.Pending())
	}
	b, err := os.ReadFile(DeadLetterPath(e.cfg))
	if err != nil || !bytes.Contains(b, []byte(`"attempts":5`)) || !bytes.Contains(b, []byte("the webhook answered 503")) {
		t.Fatalf("dead letters: %s %v", b, err)
	}
	if fi, _ := os.Stat(DeadLetterPath(e.cfg)); fi.Mode().Perm() != 0o600 {
		t.Fatalf("dead letter mode %v", fi.Mode().Perm())
	}
}

func TestBaselinesDryRunAndState(t *testing.T) {
	e := setup(t, "  - id: f\n    file: {path: a.txt}\n")
	e.write(t, "a.txt", "1")
	h := e.hook(t)
	e.round(t, h)
	// A new definition for the same id starts a new baseline, with no event.
	cfg2, err := Load([]byte(fmt.Sprintf("state: state.json\nsettle: 1ms\nwebhooks:\n  - id: w\n    url: %s/x\n    secret_env: TEST_SECRET\nsources:\n  - id: f\n    file: {path: b.txt}\n", e.url)), e.dir)
	if err != nil {
		t.Fatal(err)
	}
	e.write(t, "b.txt", "other")
	e.cfg = cfg2
	h = e.hook(t)
	if ev := e.round(t, h); len(ev) != 0 {
		t.Fatal("a changed source definition gave an event")
	}
	// Dry run: events are printed, nothing is saved or sent.
	e.write(t, "b.txt", "changed")
	before, _ := os.ReadFile(e.cfg.State)
	var out bytes.Buffer
	dry := e.hook(t, func(o *Options) { o.DryRun = true; o.Out = &out })
	if ev := e.round(t, dry); len(ev) != 1 {
		t.Fatalf("dry run found %d events", len(ev))
	}
	after, _ := os.ReadFile(e.cfg.State)
	if !bytes.Equal(before, after) || len(e.recv.events(t)) != 0 || !strings.Contains(out.String(), `"source":"f"`) {
		t.Fatalf("dry run saved or sent something; printed %q", out.String())
	}
	// A state file that can't be read stops the tool instead of losing the queue.
	os.WriteFile(e.cfg.State, []byte("{broken"), 0o600)
	if _, err := New(e.cfg, Options{Env: func(string) string { return "x" }}); err == nil || !strings.Contains(err.Error(), "move it aside") {
		t.Fatalf("err = %v", err)
	}
	// A missing secret stops it before it starts.
	e2 := setup(t, "  - id: f\n    file: {path: a.txt}\n")
	if _, err := New(e2.cfg, Options{Env: func(string) string { return "" }}); err == nil || !strings.Contains(err.Error(), "$TEST_SECRET") {
		t.Fatalf("err = %v", err)
	}
}

func TestDeliveryKeepsOrderPerWebhook(t *testing.T) {
	e := setup(t, "  - id: f\n    file: {path: n.txt}\n")
	e.recv.fail = 1
	e.write(t, "n.txt", "0")
	h := e.hook(t)
	e.round(t, h)
	e.write(t, "n.txt", "1")
	e.round(t, h) // fails; retried after the backoff
	e.write(t, "n.txt", "2")
	e.round(t, h)
	evs := e.recv.events(t)
	if len(evs) != 2 || evs[0].Sequence != 1 || evs[1].Sequence != 2 {
		t.Fatalf("%+v", evs)
	}
}

func TestLoadReportsEveryProblemInLineOrder(t *testing.T) {
	_, err := Load([]byte(`version: 2
interval: soon
webhooks:
  - id: w
    url: ftp://x
    secret_env: "my secret value"
  - url: http://x
sources:
  - id: a
    file: {path: x.json}
    folder: {path: d}
  - id: b
  - id: c
    command: {run: "sqlite3 app.db"}
  - id: d
    http: {url: nope}
    send_to: [nobody]
extra: 1
`), "")
	if err == nil {
		t.Fatal("no error")
	}
	msg := err.Error()
	want := []string{"line 1: version must be 1", "state: is required", "line 2: interval", "line 5: url must be", "line 6: secret_env names an environment variable",
		"line 7: id is required", "line 9: a source watches one thing", "line 12: source b needs one of", "line 14: command needs run",
		"line 16: http needs", `line 18: unknown key "extra"`}
	last := -1
	for _, w := range want {
		i := strings.Index(msg, w)
		if i < 0 {
			t.Fatalf("missing %q in:\n%s", w, msg)
		}
		if i < last {
			t.Fatalf("%q out of order in:\n%s", w, msg)
		}
		last = i
	}
}

func TestFixtureLoading(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.yaml")
	for _, bad := range []string{"cases: []\n", "cases:\n  - before: a\n    expect_none: true\n", "cases:\n  - source: s\n", "cases:\n  - source: s\n    expect: [x]\n    expect_none: true\n", "cases:\n  - source: s\n    expect: x\n", "cases:\n  - source: s\n    nope: 1\n    expect_none: true\n"} {
		os.WriteFile(p, []byte(bad), 0o600)
		if _, err := LoadFixture(p); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
	e := setup(t, "  - id: f\n    file: {path: x.json}\n  - id: d\n    folder: {path: out}\n")
	f := &Fixture{Cases: []FixtureCase{
		{Name: "ok", Source: "f", Before: `{"a":1}`, After: `{"a":2}`, Expect: []string{"changed a: 1 -> 2"}},
		{Name: "wrong", Source: "f", Before: `{"a":1}`, After: `{"a":2}`, Expect: []string{"changed a: 1 -> 3"}},
		{Name: "none", Source: "f", Before: `{"a":1}`, After: `{"a":1.0}`, ExpectNone: true},
		{Name: "unknown", Source: "x", ExpectNone: true},
		{Name: "folder", Source: "d", ExpectNone: true},
	}}
	res := e.cfg.RunFixture(f)
	if !res[0].Pass || res[1].Pass || !res[2].Pass || res[3].Pass || res[4].Pass {
		t.Fatalf("%+v", res)
	}
}
