package main

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aftermarket-dev/aftermarket/internal/version"
)

func runCLI(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String(), errb.String()
}

const exampleDir = "../../examples/legacy-customer"

func TestVersion(t *testing.T) {
	code, out, _ := runCLI("version")
	if code != 0 || out != "aftermarket "+version.Version+"\n" {
		t.Fatalf("version: %d %q", code, out)
	}
}

func TestUsage(t *testing.T) {
	if code, _, errs := runCLI(); code != 2 || !strings.Contains(errs, "Usage") {
		t.Fatalf("no args: %d", code)
	}
	if code, _, _ := runCLI("nope"); code != 2 {
		t.Fatal("unknown command should exit 2")
	}
	if code, _, _ := runCLI("patch", "nope"); code != 2 {
		t.Fatal("unknown patch command should exit 2")
	}
}

func TestCheckExample(t *testing.T) {
	code, out, errs := runCLI("patch", "check", "--config", filepath.Join(exampleDir, "rules.yaml"))
	if code != 0 || !strings.Contains(out, "2 rule(s)") || !strings.HasSuffix(out, "ok\n") {
		t.Fatalf("check: %d %q %q", code, out, errs)
	}
}

func TestCheckReportsErrorsAndOverlaps(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.yaml")
	os.WriteFile(bad, []byte("rules:\n  - id: a\n    when: {path: x}\n    response: {set_headers: {Host: y}}\n"), 0o600)
	code, _, errs := runCLI("patch", "check", "--config", bad)
	if code != 1 || !strings.Contains(errs, "line 3: path must start with /") || !strings.Contains(errs, "managed by the proxy") {
		t.Fatalf("bad config: %d %q", code, errs)
	}
	ov := filepath.Join(dir, "ov.yaml")
	os.WriteFile(ov, []byte("rules:\n  - id: a\n    when: {}\n    response: {set_headers: {X: \"1\"}}\n  - id: b\n    when: {}\n    response: {set_headers: {X: \"2\"}}\n"), 0o600)
	code, out, _ := runCLI("patch", "check", "--config", ov)
	if code != 0 || !strings.Contains(out, "note: a (line 2) and b (line 5)") {
		t.Fatalf("overlap: %d %q", code, out)
	}
	if code, _, _ := runCLI("patch", "check"); code != 1 {
		t.Fatal("missing --config should fail")
	}
}

func TestTestExample(t *testing.T) {
	code, out, errs := runCLI("patch", "test", "--config", filepath.Join(exampleDir, "rules.yaml"), "--fixture", filepath.Join(exampleDir, "fixture.yaml"))
	if code != 0 || !strings.Contains(out, "3 case(s), 0 failed") {
		t.Fatalf("test: %d %q %q", code, out, errs)
	}
}

func TestTestFailingFixture(t *testing.T) {
	dir := t.TempDir()
	fx := filepath.Join(dir, "fx.yaml")
	os.WriteFile(fx, []byte("cases:\n  - name: wrong\n    request: {path: /api/v1/customer?id=7}\n    upstream: {status: 200, headers: {Content-Type: application/json}, body: '{\"customer\":{\"id\":7}}'}\n    expect:\n      response: {body: '{\"nope\":1}'}\n"), 0o600)
	code, out, _ := runCLI("patch", "test", "--config", filepath.Join(exampleDir, "rules.yaml"), "--fixture", fx)
	if code != 1 || !strings.Contains(out, "FAIL  wrong") || !strings.Contains(out, "1 failed") {
		t.Fatalf("failing fixture: %d %q", code, out)
	}
}

func TestRunNeedsUpstream(t *testing.T) {
	code, _, errs := runCLI("patch", "run", "--config", filepath.Join(exampleDir, "rules.yaml"))
	if code != 2 || !strings.Contains(errs, "--upstream is required") {
		t.Fatalf("run without upstream: %d %q", code, errs)
	}
	code, _, errs = runCLI("patch", "run", "--config", filepath.Join(exampleDir, "rules.yaml"), "--upstream", "localhost:9")
	if code != 2 || !strings.Contains(errs, "absolute URL") {
		t.Fatalf("run with bad upstream: %d %q", code, errs)
	}
}

const adaptDir = "../../examples/customer-api"

func TestAdaptCheckAndTest(t *testing.T) {
	code, out, errs := runCLI("adapt", "check", "--config", filepath.Join(adaptDir, "adapt.yaml"))
	if code != 0 || !strings.Contains(out, "5 endpoint(s)") || !strings.Contains(out, "create-customer") || !strings.HasSuffix(out, "ok\n") {
		t.Fatalf("check: %d %q %q", code, out, errs)
	}
	code, out, errs = runCLI("adapt", "test", "--config", filepath.Join(adaptDir, "adapt.yaml"), "--fixture", filepath.Join(adaptDir, "fixture.yaml"))
	if code != 0 || !strings.Contains(out, "6 case(s), 0 failed") {
		t.Fatalf("test: %d %q %q", code, out, errs)
	}
}

func TestAdaptCheckReportsErrorsAndOverlaps(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.yaml")
	os.WriteFile(bad, []byte("endpoints:\n  - id: a\n    old: GET /a/{id}\n    new: GET /b/{nope}\n"), 0o600)
	code, _, errs := runCLI("adapt", "check", "--config", bad)
	if code != 1 || !strings.Contains(errs, "invalid mapping file") || !strings.Contains(errs, "line 4: new: {nope} is not captured by old") {
		t.Fatalf("bad config: %d %q", code, errs)
	}
	ov := filepath.Join(dir, "ov.yaml")
	os.WriteFile(ov, []byte("endpoints:\n  - {id: a, old: \"GET /c/{id}\", new: GET /x}\n  - {id: b, old: GET /c/me, new: GET /y}\n"), 0o600)
	code, out, _ := runCLI("adapt", "check", "--config", ov)
	if code != 0 || !strings.Contains(out, "note: a (line 2) and b (line 3) can match the same requests; a comes first and wins") {
		t.Fatalf("overlap: %d %q", code, out)
	}
}

func TestAdaptRunNeedsUpstream(t *testing.T) {
	code, _, errs := runCLI("adapt", "run", "--config", filepath.Join(adaptDir, "adapt.yaml"))
	if code != 2 || !strings.Contains(errs, "--upstream is required") {
		t.Fatalf("%d %q", code, errs)
	}
	if code, _, _ := runCLI("adapt", "nope"); code != 2 {
		t.Fatal("unknown adapt command should exit 2")
	}
}

func TestRecordCheck(t *testing.T) {
	code, out, errs := runCLI("record", "check", "--config", "../../examples/customer-api/record.yaml")
	if code != 0 || !strings.Contains(out, "redacts headers: Authorization, Cookie, Proxy-Authorization, Set-Cookie, X-Api-Key, X-Request-Token") ||
		!strings.Contains(out, "redacts JSON fields: email, customer.email, data.*.email") || !strings.HasSuffix(out, "ok\n") {
		t.Fatalf("check: %d %q %q", code, out, errs)
	}
	bad := filepath.Join(t.TempDir(), "bad.yaml")
	os.WriteFile(bad, []byte("redact: {json: [a..b]}\nmax_cases: -1\n"), 0o600)
	if code, _, errs := runCLI("record", "check", "--config", bad); code != 1 || !strings.Contains(errs, "line 1:") || !strings.Contains(errs, "line 2: max_cases") {
		t.Fatalf("bad: %d %q", code, errs)
	}
	if code, _, _ := runCLI("record", "check"); code != 2 {
		t.Fatal("check without --config should exit 2")
	}
}

func TestRecordNeverOverwrites(t *testing.T) {
	out := filepath.Join(t.TempDir(), "f.yaml")
	os.WriteFile(out, []byte("keep me"), 0o600)
	code, _, errs := runCLI("record", "--upstream", "http://127.0.0.1:1", "--out", out, "--listen", "127.0.0.1:0")
	if code != 1 || !strings.Contains(errs, "already exists") {
		t.Fatalf("%d %q", code, errs)
	}
	if b, _ := os.ReadFile(out); string(b) != "keep me" {
		t.Fatal("the existing file was changed")
	}
	if code, _, _ := runCLI("record", "--upstream", "http://x"); code != 2 {
		t.Fatal("record without --out should exit 2")
	}
	missing := filepath.Join(t.TempDir(), "new.yaml")
	if code, _, _ := runCLI("record", "--upstream", "ftp://x", "--out", missing); code != 2 {
		t.Fatal("a bad upstream should exit 2")
	}
	if _, err := os.Stat(missing); err == nil {
		t.Fatal("a fixture file was left behind after a bad upstream")
	}
}

func TestReplayCLI(t *testing.T) {
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"v":1,"at":"x"}`)
	}))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"at":"y","v":1.0}`)
	}))
	defer b.Close()
	dir := t.TempDir()
	fx := filepath.Join(dir, "f.yaml")
	os.WriteFile(fx, []byte("cases:\n  - name: one\n    request: {path: /x}\n    expect:\n      response: {status: 200}\n"), 0o600)

	code, out, _ := runCLI("replay", "--fixture", fx, "--a", a.URL, "--b", b.URL)
	if code != 1 || !strings.Contains(out, `- at: "x" vs "y"`) {
		t.Fatalf("different: %d %q", code, out)
	}
	md, js := filepath.Join(dir, "r.md"), filepath.Join(dir, "r.json")
	code, out, _ = runCLI("replay", "--fixture", fx, "--a", a.URL, "--b", b.URL, "--ignore", "at", "--report", md, "--json", js)
	if code != 0 || out != "1 case(s): 1 same, 0 different, 0 skipped, 0 error(s)\n" {
		t.Fatalf("ignored: %d %q", code, out)
	}
	if m, _ := os.ReadFile(md); !strings.Contains(string(m), "- Ignored: at") {
		t.Fatalf("markdown: %s", m)
	}
	if j, _ := os.ReadFile(js); !strings.Contains(string(j), `"same": 1`) {
		t.Fatalf("json: %s", j)
	}
	for _, args := range [][]string{
		{"replay", "--a", a.URL},
		{"replay", "--fixture", fx},
		{"replay", "--fixture", fx, "--a", a.URL, "--ignore", "a..b"},
		{"replay", "--fixture", fx, "--a", a.URL, "--set-header", "no colon"},
		{"replay", "--fixture", fx, "--a", "ftp://x"},
	} {
		if code, _, _ := runCLI(args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	if code, _, _ := runCLI("replay", "--fixture", filepath.Join(dir, "none.yaml"), "--a", a.URL); code != 1 {
		t.Error("a missing fixture should exit 1")
	}
}

// Review finding: a busy port left an empty fixture behind, so a retry
// failed with "already exists".
func TestRecordOnBusyPortLeavesNoFile(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	out := filepath.Join(t.TempDir(), "f.yaml")
	if code, _, errs := runCLI("record", "--upstream", "http://127.0.0.1:1", "--out", out, "--listen", ln.Addr().String()); code != 1 {
		t.Fatalf("%d %q", code, errs)
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("an empty fixture was left behind")
	}
}

func TestConfigCommands(t *testing.T) {
	ov := "../../examples/vendor-app/overlay.yaml"
	code, out, errs := runCLI("config", "check", "--overlay", ov)
	if code != 0 || !strings.Contains(out, "7 change(s)") || !strings.Contains(out, "set database.password to REDACTED") || !strings.HasSuffix(out, "ok\n") {
		t.Fatalf("check: %d %q %q", code, out, errs)
	}
	code, out, _ = runCLI("config", "diff", "--overlay", ov)
	if code != 0 || !strings.Contains(out, "~ server.timeout: 30 -> 120") || strings.Contains(out, "from-our-vault") {
		t.Fatalf("diff: %d %q", code, out)
	}
	code, out, _ = runCLI("config", "test", "--overlay", ov, "--fixture", "../../examples/vendor-app/fixture.yaml")
	if code != 0 || !strings.Contains(out, "3 case(s), 0 failed") {
		t.Fatalf("test: %d %q", code, out)
	}
	dst := filepath.Join(t.TempDir(), "config.json")
	code, out, _ = runCLI("config", "apply", "--overlay", ov, "--out", dst)
	if code != 0 || !strings.Contains(out, "7 change(s) applied") {
		t.Fatalf("apply: %d %q", code, out)
	}
	before, _ := os.ReadFile(dst)
	code, _, errs = runCLI("config", "apply", "--overlay", ov, "--base", "../../examples/vendor-app/1.1/config.json", "--out", dst)
	if code != 1 || !strings.Contains(errs, "2 of 7 change(s) can't be applied") || !strings.Contains(errs, "nothing was written") {
		t.Fatalf("1.1: %d %q", code, errs)
	}
	if after, _ := os.ReadFile(dst); string(after) != string(before) {
		t.Fatal("a failed apply changed the output")
	}
	if code, _, errs := runCLI("config", "apply", "--overlay", ov, "--out", "../../examples/vendor-app/1.0/config.json"); code != 1 || !strings.Contains(errs, "never writes the base") {
		t.Fatalf("same file: %d %q", code, errs)
	}
	for _, args := range [][]string{{"config"}, {"config", "nope"}, {"config", "check"}, {"config", "apply", "--overlay", ov}, {"config", "test", "--overlay", ov}} {
		if code, _, _ := runCLI(args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	bad := filepath.Join(t.TempDir(), "bad.yaml")
	os.WriteFile(bad, []byte("base: x.json\nchanges:\n  - set: {path: a}\n"), 0o600)
	if code, _, errs := runCLI("config", "check", "--overlay", bad); code != 1 || !strings.Contains(errs, "line 3: set needs value") {
		t.Fatalf("bad overlay: %d %q", code, errs)
	}
}

func TestHookCommands(t *testing.T) {
	cfg := "../../examples/legacy-app/hook.yaml"
	t.Setenv("HOOK_SECRET", "")
	code, out, errs := runCLI("hook", "check", "--config", cfg)
	if code != 0 || !strings.Contains(out, "3 source(s), 1 webhook(s)") || !strings.Contains(out, "which is not set here") {
		t.Fatalf("check: %d %q %q", code, out, errs)
	}
	code, out, _ = runCLI("hook", "test", "--config", cfg, "--fixture", "../../examples/legacy-app/fixture.yaml")
	if code != 0 || !strings.Contains(out, "4 case(s), 0 failed") {
		t.Fatalf("test: %d %q", code, out)
	}
	if code, _, errs := runCLI("hook", "run", "--config", cfg, "--once"); code != 1 || !strings.Contains(errs, "$HOOK_SECRET, which is not set") {
		t.Fatalf("run without the secret: %d %q", code, errs)
	}
	for _, args := range [][]string{{"hook"}, {"hook", "nope"}, {"hook", "check"}, {"hook", "test", "--config", cfg}} {
		if code, _, _ := runCLI(args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	// A one-shot run in a temporary folder sets the baseline and writes the state file.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "s.json"), []byte(`{"a":1}`), 0o644)
	os.WriteFile(filepath.Join(dir, "hook.yaml"), []byte("state: st.json\nwebhooks:\n  - id: w\n    url: http://127.0.0.1:1/x\nsources:\n  - id: s\n    file: {path: s.json}\n"), 0o644)
	if code, _, errs := runCLI("hook", "run", "--config", filepath.Join(dir, "hook.yaml"), "--once", "--log", ""); code != 0 {
		t.Fatalf("run --once: %d %q", code, errs)
	}
	if _, err := os.Stat(filepath.Join(dir, "st.json")); err != nil {
		t.Fatal("no state file after run --once")
	}
	if code, out, _ := runCLI("hook", "run", "--config", filepath.Join(dir, "hook.yaml"), "--once", "--dry-run", "--log", ""); code != 0 || out != "" {
		t.Fatalf("dry run with nothing changed: %d %q", code, out)
	}
	// check never prints a URL's path or query, where webhook secrets live.
	os.WriteFile(filepath.Join(dir, "secret.yaml"), []byte("state: st2.json\nwebhooks:\n  - id: slack\n    url: https://hooks.slack.com/services/T000/B000/XXXXSECRET?token=abc\nsources:\n  - id: h\n    http: {url: \"https://user:pw@api.example.com/x?api_key=SECRET123\"}\n"), 0o644)
	code, out, errs = runCLI("hook", "check", "--config", filepath.Join(dir, "secret.yaml"))
	if code != 0 || strings.Contains(out, "SECRET") || strings.Contains(out, "token=") || strings.Contains(out, "user") || !strings.Contains(out, "https://hooks.slack.com/…") {
		t.Fatalf("check printed a secret: %d %q %q", code, out, errs)
	}
}

func TestGuardCommands(t *testing.T) {
	cfg := "../../examples/legacy-admin/guard.yaml"
	code, out, errs := runCLI("guard", "check", "--config", cfg)
	if code != 0 || !strings.Contains(out, "4 rule(s), 2 client(s), default deny") || !strings.Contains(out, "reachable only through AfterGuard") {
		t.Fatalf("check: %d %q %q", code, out, errs)
	}
	code, out, _ = runCLI("guard", "test", "--config", cfg, "--fixture", "../../examples/legacy-admin/fixture.yaml")
	if code != 0 || !strings.Contains(out, "9 case(s), 0 failed") {
		t.Fatalf("test: %d %q", code, out)
	}
	for _, args := range [][]string{{"guard"}, {"guard", "nope"}, {"guard", "check"}, {"guard", "test", "--config", cfg},
		{"guard", "run", "--config", cfg}, {"guard", "hash", "secret-as-an-argument"}} {
		if code, _, _ := runCLI(args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.yaml")
	os.WriteFile(bad, []byte("rules:\n  - id: a\n    allow: maybe\n"), 0o644)
	if code, _, errs := runCLI("guard", "check", "--config", bad); code != 1 || !strings.Contains(errs, "line 3") {
		t.Fatalf("bad config: %d %q", code, errs)
	}
	// hash reads the secret from standard input, never from the command line.
	old := stdin
	defer func() { stdin = old }()
	stdin = strings.NewReader("billing-demo-key-change-me\n")
	code, out, _ = runCLI("guard", "hash")
	if code != 0 || strings.TrimSpace(out) != "f04bf1cd97cf76f0f2d928e4e1fa56033548cf50826db9f0c265d1f349e28c84" {
		t.Fatalf("hash: %d %q", code, out)
	}
	stdin = strings.NewReader("short\n")
	if code, _, errs := runCLI("guard", "hash"); code != 0 || !strings.Contains(errs, "can be guessed") {
		t.Fatalf("short key: %d %q", code, errs)
	}
}
