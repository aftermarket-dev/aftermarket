// Package legacyadmin is a stand-in for an old admin panel with no login,
// no rate limits and a debug page left switched on: the kind of app
// AfterGuard is for. It is used by the example's tests and the quick start.
package legacyadmin

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// App is the admin panel. It trusts every request it gets.
type App struct {
	mu    sync.Mutex
	users []string
	seen  []string // "METHOD path client" of every request that reached it
}

// New returns the app with two users.
func New() *App { return &App{users: []string{"ada", "grace"}} }

// Seen lists the requests that reached the app.
func (a *App) Seen() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.seen...)
}

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	a.seen = append(a.seen, fmt.Sprintf("%s %s %s", r.Method, r.URL.RequestURI(), r.Header.Get("X-Aftermarket-Client")))
	a.mu.Unlock()
	w.Header().Set("Server", "LegacyAdmin/2.3")
	w.Header().Set("X-Powered-By", "PHP/5.6")
	switch {
	case r.URL.Path == "/status":
		fmt.Fprintln(w, "ok")
	case r.URL.Path == "/debug/env":
		fmt.Fprintln(w, "DB_PASSWORD=hunter2") // the leftover every old app has
	case r.URL.Path == "/reports/daily" && r.Method == http.MethodGet:
		w.Header().Set("Content-Type", "text/csv")
		fmt.Fprintln(w, "day,orders\n2026-10-21,42")
	case r.URL.Path == "/admin/users" && r.Method == http.MethodGet:
		a.mu.Lock()
		defer a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(a.users)
	case r.URL.Path == "/admin/users" && r.Method == http.MethodPost:
		var u struct{ Name string }
		b, _ := io.ReadAll(r.Body)
		if json.Unmarshal(b, &u) != nil || u.Name == "" {
			http.Error(w, "bad user", http.StatusBadRequest)
			return
		}
		a.mu.Lock()
		a.users = append(a.users, u.Name)
		a.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	default:
		http.NotFound(w, r)
	}
}
