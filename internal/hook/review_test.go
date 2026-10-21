package hook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Regression tests for the 0.5.0 review findings.

func eventsJSON(t *testing.T, ev []Event) string {
	t.Helper()
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// H1: when a comparison can't go field by field, secret values still never
// leave in an event.
func TestLineDiffsKeepSecretsOut(t *testing.T) {
	t.Run("http JSON turning into an error page", func(t *testing.T) {
		var mu sync.Mutex
		body := `{"status":"ok","api_token":"sk-LIVE-123"}`
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			io.WriteString(w, body)
		}))
		defer up.Close()
		e := setup(t, fmt.Sprintf("  - id: h\n    http: {url: %q}\n", up.URL))
		h := e.hook(t)
		e.round(t, h)
		mu.Lock()
		body = "<html>maintenance</html>"
		mu.Unlock()
		ev := e.round(t, h)
		mu.Lock()
		body = `{"status":"ok","api_token":"sk-LIVE-456"}`
		mu.Unlock()
		ev = append(ev, e.round(t, h)...)
		if len(ev) != 2 {
			t.Fatalf("%d events", len(ev))
		}
		if s := eventsJSON(t, ev); strings.Contains(s, "sk-LIVE") {
			t.Fatalf("a secret was sent: %s", s)
		}
	})
	t.Run("sqlite3 printing nothing for no rows", func(t *testing.T) {
		dir := t.TempDir()
		input := filepath.Join(dir, "out.txt")
		t.Setenv("HOOK_HELPER", input)
		os.WriteFile(input, nil, 0o644)
		e := setup(t, fmt.Sprintf("  - id: c\n    command: {run: [%q, -test.run=TestHelperProcess]}\n", os.Args[0]))
		h := e.hook(t)
		e.round(t, h)
		os.WriteFile(input, []byte(`[{"id":1,"password":"hunter2"}]`), 0o644)
		ev := e.round(t, h)
		os.WriteFile(input, nil, 0o644)
		ev = append(ev, e.round(t, h)...)
		if len(ev) != 2 || ev[0].Changes[0].Op != "added" || ev[0].Changes[0].Path != "0" || ev[1].Changes[0].Op != "removed" {
			t.Fatalf("%s", eventsJSON(t, ev))
		}
		if s := eventsJSON(t, ev); strings.Contains(s, "hunter2") || !strings.Contains(s, "REDACTED") {
			t.Fatalf("%s", s)
		}
	})
	t.Run("text file", func(t *testing.T) {
		e := setup(t, "  - id: ini\n    file: {path: app.ini}\n")
		e.write(t, "app.ini", "[db]\nhost = db1\npassword = old-pass\n")
		h := e.hook(t)
		e.round(t, h)
		e.write(t, "app.ini", "[db]\nhost = db2\npassword = hunter2\n")
		ev := e.round(t, h)
		s := eventsJSON(t, ev)
		if len(ev) != 1 || strings.Contains(s, "hunter2") || strings.Contains(s, "old-pass") || !strings.Contains(s, "db2") {
			t.Fatalf("%s", s)
		}
	})
}

func TestRedactLine(t *testing.T) {
	for in, want := range map[string]string{
		`password = hunter2`:                     `password = REDACTED`,
		`  api_key: "abc def"`:                   `  api_key: REDACTED`,
		`export DB_PASSWORD='x y'`:               `export DB_PASSWORD=REDACTED`,
		`{"status":"ok","api_token":"sk-1"}`:     `{"status":"ok","api_token":REDACTED}`,
		`GET /x?user=bob&token=abc&page=2`:       `GET /x?user=bob&token=REDACTED&page=2`,
		`url = postgres://app:pw@db/app`:         `url = postgres://REDACTED@db/app`,
		`host = db1`:                             `host = db1`,
		`a line with no pairs, and: a colon too`: `a line with no pairs, and: a colon too`,
	} {
		if got := redactLine(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

// M1: a file that is gone for one check and back with the same content gives
// no event; one that stays gone gives one.
func TestAFileThatBlinksGivesNoEvent(t *testing.T) {
	e := setup(t, "  - id: s\n    file: {path: s.json}\n")
	e.write(t, "s.json", `{"a":1}`)
	h := e.hook(t)
	e.round(t, h)
	os.Remove(filepath.Join(e.dir, "s.json"))
	if ev := e.round(t, h); len(ev) != 0 {
		t.Fatalf("a missing file was reported at once: %+v", ev)
	}
	e.write(t, "s.json", `{"a":1}`)
	if ev := e.round(t, h); len(ev) != 0 {
		t.Fatalf("a file back as it was gave an event: %+v", ev)
	}
	os.Remove(filepath.Join(e.dir, "s.json"))
	e.round(t, h)
	ev := e.round(t, h)
	if len(ev) != 1 || ev[0].Changes[0].Op != "removed" {
		t.Fatalf("a file that stayed gone: %+v", ev)
	}
	if ev := e.round(t, h); len(ev) != 0 {
		t.Fatal("the removal was reported twice")
	}
}

// M2: a YAML or .env file cut short waits for a second look.
func TestTruncatedStructuredFilesWait(t *testing.T) {
	for _, c := range []struct{ name, full, cut string }{
		{"s.yaml", "a: 1\nb: 2\nc: 3\n", "a: 1\n"},
		{"s.env", "A=1\nB=2\n", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := setup(t, fmt.Sprintf("  - id: s\n    file: {path: %s}\n", c.name))
			e.write(t, c.name, c.full)
			h := e.hook(t)
			e.round(t, h)
			e.write(t, c.name, c.cut)
			if ev := e.round(t, h); len(ev) != 0 {
				t.Fatalf("a file cut short was reported at once: %+v", ev)
			}
			e.write(t, c.name, c.full)
			if ev := e.round(t, h); len(ev) != 0 {
				t.Fatalf("the finished file gave an event: %+v", ev)
			}
			// Cut short on purpose, and still so on the next check: reported.
			e.write(t, c.name, c.cut)
			e.round(t, h)
			if ev := e.round(t, h); len(ev) != 1 {
				t.Fatalf("a real shrink: %+v", ev)
			}
		})
	}
}

// M3: one file still being written doesn't hide the others in its folder.
func TestABusyFileDoesNotBlockTheFolder(t *testing.T) {
	e := setup(t, "  - id: f\n    folder: {path: out}\n")
	e.write(t, "out/a.csv", "1\n")
	busy := false
	log := filepath.Join(e.dir, "out", "log.txt")
	h := e.hook(t, func(o *Options) {
		o.Sleep = func(time.Duration) {
			if busy {
				f, _ := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
				f.WriteString("more\n")
				f.Close()
			}
		}
	})
	e.round(t, h)
	busy = true
	e.write(t, "out/log.txt", "start\n")
	e.write(t, "out/b.csv", "2\n")
	ev := e.round(t, h)
	if len(ev) != 1 || len(ev[0].Changes) != 1 || ev[0].Changes[0].Path != "b.csv" {
		t.Fatalf("%s", eventsJSON(t, ev))
	}
	busy = false
	ev = e.round(t, h)
	if len(ev) != 1 || ev[0].Changes[0].Path != "log.txt" || ev[0].Changes[0].Op != "added" {
		t.Fatalf("the finished log: %s", eventsJSON(t, ev))
	}
}

// M4: a new baseline, such as after the state file is moved aside, never
// reuses an earlier event id.
func TestIDsNeverRepeatAcrossBaselines(t *testing.T) {
	e := setup(t, "  - id: s\n    file: {path: s.json}\n")
	e.write(t, "s.json", `{"maint":false}`)
	h := e.hook(t)
	e.round(t, h)
	e.write(t, "s.json", `{"maint":true}`)
	first := e.round(t, h)
	os.Remove(filepath.Join(e.dir, "state.json"))
	e.write(t, "s.json", `{"maint":false}`)
	h = e.hook(t)
	e.round(t, h)
	e.write(t, "s.json", `{"maint":true}`)
	second := e.round(t, h)
	if len(first) != 1 || len(second) != 1 || first[0].ID == second[0].ID {
		t.Fatalf("ids %+v and %+v", first, second)
	}
}

// M5: a command's timeout holds even when a child keeps stdout open.
func TestCommandTimeoutWithAChildHoldingStdout(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	e := setup(t, "  - id: c\n    command: {run: [sh, -c, \"sleep 6 | cat; echo x\"], timeout: 300ms}\n")
	h := e.hook(t)
	start := time.Now()
	e.round(t, h)
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("one check took %s", took)
	}
	if !strings.Contains(e.log.String(), "did not finish within its timeout") {
		t.Fatalf("log:\n%s", e.log)
	}
}

// L1: a dry run prints each change once, not on every check after it.
func TestDryRunPrintsEachChangeOnce(t *testing.T) {
	e := setup(t, "  - id: s\n    file: {path: s.json}\n")
	e.write(t, "s.json", `{"a":1}`)
	var out strings.Builder
	h := e.hook(t, func(o *Options) { o.DryRun = true; o.Out = &out })
	e.round(t, h)
	e.write(t, "s.json", `{"a":2}`)
	for i := 0; i < 3; i++ {
		e.round(t, h)
	}
	if n := strings.Count(out.String(), "\n"); n != 1 {
		t.Fatalf("%d lines:\n%s", n, out.String())
	}
	if _, err := os.Stat(filepath.Join(e.dir, "state.json")); err == nil {
		t.Fatal("a dry run wrote the state file")
	}
}

// L3: while a webhook's oldest event waits, the run sleeps until then, not
// until the earlier times of the events behind it.
func TestUntilNextWaitsForTheHead(t *testing.T) {
	e := setup(t, "  - id: s\n    file: {path: s.json}\n")
	h := e.hook(t)
	now := e.clock.now()
	h.state.Queue = []*Delivery{
		{Webhook: "w", EventID: "1", NextAt: now.Add(20 * time.Second)},
		{Webhook: "w", EventID: "2", NextAt: now.Add(-time.Minute)},
	}
	if got := h.untilNext(); got != 20*time.Second {
		t.Fatalf("untilNext %s", got)
	}
}

// L4: stopping during a send doesn't use up an attempt or dead-letter the
// event.
func TestStoppingMidSendKeepsTheEvent(t *testing.T) {
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()
	defer close(release)
	e := setup(t, "  - id: s\n    file: {path: s.json}\n")
	e.cfg.Webhooks[0].URL, _ = e.cfg.Webhooks[0].URL.Parse(slow.URL + "/x")
	e.cfg.MaxAttempts = 1
	e.write(t, "s.json", `{"a":1}`)
	h := e.hook(t)
	e.round(t, h)
	e.write(t, "s.json", `{"a":2}`)
	e.clock.add(10 * time.Second)
	if _, err := h.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	h.Deliver(ctx)
	if h.Pending() != 1 || h.state.Queue[0].Attempts != 0 {
		t.Fatalf("pending %d", h.Pending())
	}
	if _, err := os.Stat(DeadLetterPath(e.cfg)); err == nil {
		t.Fatal("the event was dead-lettered")
	}
	// It is still queued in the saved state for the next run.
	h2 := e.hook(t)
	if h2.Pending() != 1 {
		t.Fatalf("saved queue: %d", h2.Pending())
	}
}

// send_to naming a webhook twice still sends each event once.
func TestSendToNamesEachWebhookOnce(t *testing.T) {
	e := setup(t, "  - id: s\n    file: {path: s.json}\n    send_to: [w, w]\n")
	e.write(t, "s.json", `{"a":1}`)
	h := e.hook(t)
	e.round(t, h)
	e.write(t, "s.json", `{"a":2}`)
	e.round(t, h)
	if n := len(e.recv.events(t)); n != 1 {
		t.Fatalf("%d deliveries", n)
	}
}

// A long field diff says how many changes it left out.
func TestFieldDiffSaysHowManyMore(t *testing.T) {
	var a, b strings.Builder
	a.WriteString("{")
	b.WriteString("{")
	for i := 0; i < 150; i++ {
		if i > 0 {
			a.WriteString(",")
			b.WriteString(",")
		}
		fmt.Fprintf(&a, `"k%d":1`, i)
		fmt.Fprintf(&b, `"k%d":2`, i)
	}
	a.WriteString("}")
	b.WriteString("}")
	s := &Source{Kind: "file", Format: "json"}
	ch := diff(s, Snapshot{Exists: true, Hash: "a", Content: a.String()}, Snapshot{Exists: true, Hash: "b", Content: b.String()})
	if len(ch) != maxChanges+1 || ch[maxChanges].New != "and 50 more changes" {
		t.Fatalf("%d changes, last %+v", len(ch), ch[len(ch)-1])
	}
}

func TestDisplayURLHidesPathsAndQueries(t *testing.T) {
	e := setup(t, "  - id: s\n    file: {path: s.json}\n")
	u, _ := e.cfg.Webhooks[0].URL.Parse("https://user:pw@hooks.example.com/services/T0/B0/SECRET?token=abc")
	if got := DisplayURL(u); got != "https://hooks.example.com/…" {
		t.Fatal(got)
	}
	u, _ = u.Parse("https://hooks.example.com/")
	if got := DisplayURL(u); got != "https://hooks.example.com" {
		t.Fatal(got)
	}
}
