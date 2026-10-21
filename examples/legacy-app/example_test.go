// Package legacyapp holds the AfterHook example: a legacy app that writes a
// status file, drops CSV exports and keeps orders in SQLite, none of which
// it announces.
package legacyapp_test

import (
	"context"
	"encoding/json"
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

	"github.com/aftermarket-dev/aftermarket/internal/hook"
)

func copyDir(t *testing.T, dst string) {
	t.Helper()
	for _, f := range []string{"hook.yaml", "status.json", "fixture.yaml"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dst, f), b, 0o644)
	}
	os.Mkdir(filepath.Join(dst, "exports"), 0o755)
}

// TestTheExampleEndToEnd changes all three sources of the example and checks
// that a signed event arrives for each, and nothing for a quiet round.
func TestTheExampleEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 is not installed here")
	}
	dir := t.TempDir()
	copyDir(t, dir)
	run := func(sql string) {
		out, err := exec.Command("sqlite3", filepath.Join(dir, "app.db"), sql).CombinedOutput()
		if err != nil {
			t.Fatalf("sqlite3: %v %s", err, out)
		}
	}
	run("create table orders(id integer primary key, status text, total real); insert into orders values (1001,'open',39.8);")

	var mu sync.Mutex
	var got []hook.Event
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var ts int64
		json.Unmarshal([]byte(r.Header.Get("X-Aftermarket-Timestamp")), &ts)
		if hook.Sign("example-secret", ts, body) != r.Header.Get("X-Aftermarket-Signature") {
			http.Error(w, "bad signature", 401)
			return
		}
		var ev hook.Event
		json.Unmarshal(body, &ev)
		mu.Lock()
		got = append(got, ev)
		mu.Unlock()
	}))
	defer recv.Close()
	cfgText, _ := os.ReadFile(filepath.Join(dir, "hook.yaml"))
	os.WriteFile(filepath.Join(dir, "hook.yaml"), []byte(strings.Replace(string(cfgText), "http://127.0.0.1:9900/events", recv.URL+"/events", 1)), 0o644)
	cfg, err := hook.LoadFile(filepath.Join(dir, "hook.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Settle = time.Millisecond
	newHook := func() *hook.Hook {
		h, err := hook.New(cfg, hook.Options{Env: func(k string) string { return map[string]string{"HOOK_SECRET": "example-secret"}[k] }})
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	ctx := context.Background()
	if err := newHook().Run(ctx, true); err != nil {
		t.Fatal(err)
	}
	status, _ := os.ReadFile(filepath.Join(dir, "status.json"))
	os.WriteFile(filepath.Join(dir, "status.json"), []byte(strings.Replace(strings.Replace(string(status), `"open"`, `"paid"`, 1), "09:00:00Z", "09:05:00Z", 1)), 0o644)
	os.WriteFile(filepath.Join(dir, "exports", "orders.csv"), []byte("id,total\n1001,39.8\n"), 0o644)
	run("update orders set status='paid' where id=1001; insert into orders values (1002,'open',12);")
	if err := newHook().Run(ctx, true); err != nil {
		t.Fatal(err)
	}
	if err := newHook().Run(ctx, true); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	var lines []string
	for _, ev := range got {
		for _, c := range ev.Changes {
			lines = append(lines, ev.Source+": "+hook.Describe(c))
		}
	}
	want := `order-status: changed status: "open" -> "paid"
exports: added orders.csv: {"sha256":"`
	joined := strings.Join(lines, "\n")
	if len(got) != 3 || !strings.HasPrefix(joined, want) || !strings.Contains(joined, `orders-table: changed 0.status: "open" -> "paid"`) ||
		!strings.Contains(joined, `orders-table: added 1: {"id":1002,"status":"open","total":12}`) {
		t.Fatalf("%d events:\n%s", len(got), joined)
	}
	if strings.Contains(joined, "app-internal-token") {
		t.Fatal("a token reached an event")
	}
}

func TestFixturePasses(t *testing.T) {
	cfg, err := hook.LoadFile("hook.yaml")
	if err != nil {
		t.Fatal(err)
	}
	f, err := hook.LoadFixture("fixture.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range cfg.RunFixture(f) {
		if !r.Pass {
			t.Errorf("%s: %v", r.Name, r.Problems)
		}
	}
}
