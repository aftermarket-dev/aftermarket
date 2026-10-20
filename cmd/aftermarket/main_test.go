package main

import (
	"bytes"
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
