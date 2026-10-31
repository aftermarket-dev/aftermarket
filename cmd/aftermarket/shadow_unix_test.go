//go:build unix

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// The shadow command end to end: serve, compare, stop on a signal, and leave
// a final report behind.
func TestShadowCommand(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"name":"Ann","password":"pw-primary"}`)
	}))
	defer primary.Close()
	cand := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"full_name":"Ann","password":"pw-candidate"}`)
	}))
	defer cand.Close()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "record.yaml")
	os.WriteFile(cfg, []byte("version: 1\nredact:\n  json: [password]\n"), 0o600)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	md, js := filepath.Join(dir, "r.md"), filepath.Join(dir, "r.json")
	var out, errb syncBuffer
	done := make(chan int, 1)
	go func() {
		done <- run([]string{"shadow", "--primary", primary.URL, "--candidate", cand.URL, "--config", cfg, "--listen", addr,
			"--report", md, "--json", js, "--audit-log", ""}, &out, &errb)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := http.Get("http://" + addr + "/_aftermarket/healthz")
		if err == nil {
			resp.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("shadow did not start: %q", errb.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	resp, err := http.Get("http://" + addr + "/me?token=tok-1")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != `{"name":"Ann","password":"pw-primary"}` {
		t.Fatalf("client got %q", body)
	}
	time.Sleep(200 * time.Millisecond)
	syscall.Kill(os.Getpid(), syscall.SIGINT)
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit %d: %q", code, errb.String())
		}
	case <-time.After(20 * time.Second):
		t.Fatal("shadow did not stop")
	}
	if !strings.Contains(out.String(), "aftermarket shadow: stopped, 1 case(s): 0 same, 1 different") {
		t.Fatalf("stdout %q", out.String())
	}
	data, _ := os.ReadFile(md)
	for _, w := range []string{"# Shadow report", "full_name: only in B", "name: only in A", "password: differs (REDACTED)", "/me?token=REDACTED"} {
		if !strings.Contains(string(data), w) {
			t.Errorf("report lacks %q:\n%s", w, data)
		}
	}
	jdata, _ := os.ReadFile(js)
	var rep struct{ Different int }
	if json.Unmarshal(jdata, &rep) != nil || rep.Different != 1 {
		t.Errorf("json report: %s", jdata)
	}
	for _, s := range []string{"pw-", "tok-1"} {
		if strings.Contains(string(data)+string(jdata), s) {
			t.Errorf("%q reached the report", s)
		}
	}
}

func TestShadowCommandChecksItsFlags(t *testing.T) {
	for _, args := range [][]string{
		{"shadow"},
		{"shadow", "--primary", "http://127.0.0.1:1"},
		{"shadow", "--primary", "http://127.0.0.1:1", "--candidate", "http://127.0.0.1:2", "--sample", "0"},
		{"shadow", "--primary", "http://127.0.0.1:1", "--candidate", "http://127.0.0.1:2", "--ignore", "a..b"},
		{"shadow", "--primary", "http://127.0.0.1:1", "--candidate", "http://127.0.0.1:2", "--report", ""},
	} {
		if code, _, _ := runCLI(args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
}
