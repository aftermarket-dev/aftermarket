package legacyadmin_test

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	legacyadmin "github.com/aftermarket-dev/aftermarket/examples/legacy-admin"
	"github.com/aftermarket-dev/aftermarket/internal/guard"
)

// The old admin panel, first open to everyone, then behind AfterGuard with
// the example's guard.yaml.
func TestTheAdminPanelBeforeAndAfter(t *testing.T) {
	app := legacyadmin.New()
	appSrv := httptest.NewServer(app)
	defer appSrv.Close()

	get := func(base, path string, set func(*http.Request)) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequest("GET", base+path, nil)
		if set != nil {
			set(req)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}

	// Before: anyone gets the user list and the debug page.
	if resp, body := get(appSrv.URL, "/admin/users", nil); resp.StatusCode != 200 || !strings.Contains(body, "ada") {
		t.Fatalf("before, admin: %d %q", resp.StatusCode, body)
	}
	if _, body := get(appSrv.URL, "/debug/env", nil); !strings.Contains(body, "hunter2") {
		t.Fatal("before, the debug page should leak")
	}

	cfg, err := guard.LoadFile("guard.yaml")
	if err != nil {
		t.Fatal(err)
	}
	up, _ := url.Parse(appSrv.URL)
	g, err := guard.New(guard.Options{Config: cfg, Upstream: up, HealthPath: guard.DefaultHealthPath})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := net.Listen("tcp", "127.0.0.1:0")
	ln, err := g.Listener(raw)
	if err != nil {
		t.Fatal(err)
	}
	srv := g.Server()
	go srv.Serve(ln)
	defer srv.Close()
	base := "http://" + raw.Addr().String()
	before := len(app.Seen())

	admin := func(r *http.Request) { r.SetBasicAuth("admin", "admin-demo-password-change-me") }
	billing := func(r *http.Request) { r.Header.Set("X-Api-Key", "billing-demo-key-change-me") }

	if resp, _ := get(base, "/admin/users", nil); resp.StatusCode != 401 {
		t.Fatalf("anonymous admin: %d", resp.StatusCode)
	}
	if resp, _ := get(base, "/debug/env", admin); resp.StatusCode != 404 {
		t.Fatalf("debug page: %d", resp.StatusCode)
	}
	if resp, _ := get(base, "/admin/users", billing); resp.StatusCode != 403 {
		t.Fatalf("billing on admin: %d", resp.StatusCode)
	}
	if len(app.Seen()) != before {
		t.Fatalf("turned-away requests reached the app: %v", app.Seen()[before:])
	}
	resp, body := get(base, "/admin/users", admin)
	if resp.StatusCode != 200 || !strings.Contains(body, "grace") || resp.Header.Get("Server") != "" || resp.Header.Get("X-Frame-Options") != "DENY" {
		t.Fatalf("admin: %d %q %v", resp.StatusCode, body, resp.Header)
	}
	if seen := app.Seen(); seen[len(seen)-1] != "GET /admin/users admin" {
		t.Fatalf("the app saw %q", seen[len(seen)-1])
	}
	if resp, _ := get(base, "/reports/daily", billing); resp.StatusCode != 200 {
		t.Fatalf("billing reports: %d", resp.StatusCode)
	}
	// Guessing: five wrong passwords, then even the right one waits.
	for i := 0; i < 5; i++ {
		get(base, "/admin/users", func(r *http.Request) { r.SetBasicAuth("admin", "guess") })
	}
	if resp, _ := get(base, "/admin/users", admin); resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("after guessing: %d", resp.StatusCode)
	}
}

func TestFixturePasses(t *testing.T) {
	cfg, err := guard.LoadFile("guard.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cases, err := guard.LoadFixture("fixture.yaml")
	if err != nil {
		t.Fatal(err)
	}
	res, err := cfg.RunFixture(cases)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if !r.Pass {
			t.Errorf("%s: want %s, got %s", r.Name, r.Want, r.Got)
		}
	}
}
