// Command record runs the full test suite and the latency measurement and
// writes the results to test/results. Every number on aftermarket.dev comes
// from these files.
//
//	go run ./tools/record
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	legacycustomer "github.com/aftermarket-dev/aftermarket/examples/legacy-customer"
	"github.com/aftermarket-dev/aftermarket/internal/patch"
	"github.com/aftermarket-dev/aftermarket/internal/rules"
	"github.com/aftermarket-dev/aftermarket/internal/version"
)

const outDir = "test/results"

type testResult struct {
	Package string  `json:"package"`
	Test    string  `json:"test"`
	Result  string  `json:"result"`
	Seconds float64 `json:"seconds"`
}

type latencyResult struct {
	Name        string  `json:"name"`
	Requests    int     `json:"requests"`
	P50Micros   float64 `json:"p50_us"`
	P99Micros   float64 `json:"p99_us"`
	MeanMicros  float64 `json:"mean_us"`
	Concurrency int     `json:"concurrency"`
	ReqPerSec   float64 `json:"requests_per_second"`
}

type report struct {
	Version     string          `json:"version"`
	RecordedAt  string          `json:"recorded_at"`
	GoVersion   string          `json:"go_version"`
	OS          string          `json:"os"`
	Arch        string          `json:"arch"`
	CPUs        int             `json:"cpus"`
	TestsPassed int             `json:"tests_passed"`
	TestsFailed int             `json:"tests_failed"`
	TestsTotal  int             `json:"tests_total"`
	Tests       []testResult    `json:"tests"`
	Latency     []latencyResult `json:"latency"`
	AddedP50    float64         `json:"added_p50_us"`
	AddedP99    float64         `json:"added_p99_us"`
	PatchedP50  float64         `json:"patched_added_p50_us"`
	PatchedP99  float64         `json:"patched_added_p99_us"`
}

func main() {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fail(err)
	}
	rep := report{
		Version:    version.Version,
		RecordedAt: time.Now().UTC().Format(time.RFC3339),
		GoVersion:  runtime.Version(),
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		CPUs:       runtime.NumCPU(),
	}

	fmt.Println("running go test -count=1 -race ./...")
	tests, err := runTests()
	if err != nil {
		fail(err)
	}
	rep.Tests = tests
	for _, t := range tests {
		rep.TestsTotal++
		if t.Result == "pass" {
			rep.TestsPassed++
		} else {
			rep.TestsFailed++
		}
	}
	fmt.Printf("tests: %d passed, %d failed\n", rep.TestsPassed, rep.TestsFailed)

	fmt.Println("measuring latency")
	lat, err := measure()
	if err != nil {
		fail(err)
	}
	rep.Latency = lat
	byName := map[string]latencyResult{}
	for _, l := range lat {
		byName[l.Name] = l
	}
	rep.AddedP50 = round(byName["proxy, no rule matches"].P50Micros - byName["direct to upstream"].P50Micros)
	rep.AddedP99 = round(byName["proxy, no rule matches"].P99Micros - byName["direct to upstream"].P99Micros)
	rep.PatchedP50 = round(byName["proxy, both example rules applied"].P50Micros - byName["direct to upstream"].P50Micros)
	rep.PatchedP99 = round(byName["proxy, both example rules applied"].P99Micros - byName["direct to upstream"].P99Micros)

	b, _ := json.MarshalIndent(rep, "", "  ")
	if err := os.WriteFile(filepath.Join(outDir, "results.json"), append(b, '\n'), 0o644); err != nil {
		fail(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "README.md"), []byte(summary(rep)), 0o644); err != nil {
		fail(err)
	}
	fmt.Println("wrote test/results/results.json and test/results/README.md")
	if rep.TestsFailed > 0 {
		os.Exit(1)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "record:", err)
	os.Exit(1)
}

func round(f float64) float64 { return float64(int64(f*10+0.5)) / 10 }

func runTests() ([]testResult, error) {
	cmd := exec.Command("go", "test", "-count=1", "-race", "-json", "./...")
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	var results []testResult
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var ev struct {
			Action  string
			Package string
			Test    string
			Elapsed float64
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil || ev.Test == "" {
			continue
		}
		if ev.Action == "pass" || ev.Action == "fail" || ev.Action == "skip" {
			results = append(results, testResult{
				Package: strings.TrimPrefix(ev.Package, "github.com/aftermarket-dev/aftermarket/"),
				Test:    ev.Test, Result: ev.Action, Seconds: ev.Elapsed,
			})
		}
	}
	waitErr := cmd.Wait()
	failed := 0
	for _, r := range results {
		if r.Result == "fail" {
			failed++
		}
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("go test reported no tests (%v)", waitErr)
	}
	if waitErr != nil && failed == 0 {
		return nil, fmt.Errorf("go test failed without a failing test, probably a build error: %v", waitErr)
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Package != results[j].Package {
			return results[i].Package < results[j].Package
		}
		return results[i].Test < results[j].Test
	})
	return results, nil
}

func serve(h http.Handler) (string, func()) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fail(err)
	}
	srv := &http.Server{Handler: h}
	go srv.Serve(ln)
	return "http://" + ln.Addr().String(), func() { srv.Close() }
}

func measure() ([]latencyResult, error) {
	svcURL, stopSvc := serve(legacycustomer.NewService())
	defer stopSvc()
	cfg, err := rules.LoadFile("examples/legacy-customer/rules.yaml")
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(svcURL)
	px, err := patch.New(patch.Options{Upstream: u, Config: cfg})
	if err != nil {
		return nil, err
	}
	pxURL, stopPx := serve(px)
	defer stopPx()

	cases := []struct {
		name, url, want string
	}{
		{"direct to upstream", svcURL + "/api/v2/customers/7", `"full_name":"Ada Lovelace"`},
		{"proxy, no rule matches", pxURL + "/api/v2/customers/7", `"full_name":"Ada Lovelace"`},
		{"proxy, both example rules applied", pxURL + "/api/v1/customer?id=7", `"name":"Ada Lovelace"`},
	}
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 64, DisableCompression: true}}
	get := func(u, want string) (time.Duration, error) {
		start := time.Now()
		resp, err := client.Get(u)
		if err != nil {
			return 0, err
		}
		b, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		d := time.Since(start)
		if err != nil || !bytes.Contains(b, []byte(want)) {
			return 0, fmt.Errorf("unexpected answer from %s: %s", u, b)
		}
		return d, nil
	}

	const warm, n = 2000, 20000
	var out []latencyResult
	for _, c := range cases {
		for i := 0; i < warm; i++ {
			if _, err := get(c.url, c.want); err != nil {
				return nil, err
			}
		}
		ds := make([]time.Duration, 0, n)
		for i := 0; i < n; i++ {
			d, err := get(c.url, c.want)
			if err != nil {
				return nil, err
			}
			ds = append(ds, d)
		}
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		var sum time.Duration
		for _, d := range ds {
			sum += d
		}
		r := latencyResult{
			Name:       c.name,
			Requests:   n,
			P50Micros:  us(ds[n/2]),
			P99Micros:  us(ds[n*99/100]),
			MeanMicros: round(float64(sum.Microseconds()) / float64(n)),
		}
		// Throughput with 16 clients for two seconds.
		r.Concurrency = 16
		var count int64
		var mu sync.Mutex
		var firstErr error
		deadline := time.Now().Add(2 * time.Second)
		var wg sync.WaitGroup
		start := time.Now()
		for w := 0; w < r.Concurrency; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				local := int64(0)
				for time.Now().Before(deadline) {
					if _, err := get(c.url, c.want); err != nil {
						mu.Lock()
						if firstErr == nil {
							firstErr = err
						}
						mu.Unlock()
						return
					}
					local++
				}
				mu.Lock()
				count += local
				mu.Unlock()
			}()
		}
		wg.Wait()
		if firstErr != nil {
			return nil, firstErr
		}
		r.ReqPerSec = float64(int64(float64(count) / time.Since(start).Seconds()))
		fmt.Printf("  %-36s p50 %6.1fus  p99 %6.1fus  %8.0f req/s\n", c.name, r.P50Micros, r.P99Micros, r.ReqPerSec)
		out = append(out, r)
	}
	return out, nil
}

func us(d time.Duration) float64 { return round(float64(d.Nanoseconds()) / 1000) }

func summary(r report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Recorded test results\n\n")
	fmt.Fprintf(&b, "Written by `go run ./tools/record`. Do not edit by hand.\n\n")
	fmt.Fprintf(&b, "- Version: %s\n- Recorded: %s\n- Machine: %s/%s, %d CPUs, %s\n\n", r.Version, r.RecordedAt, r.OS, r.Arch, r.CPUs, r.GoVersion)
	fmt.Fprintf(&b, "## Tests\n\n%d of %d tests passed (race detector on).\n\n", r.TestsPassed, r.TestsTotal)
	b.WriteString("| Package | Test | Result |\n| --- | --- | --- |\n")
	for _, t := range r.Tests {
		if strings.Contains(t.Test, "/") {
			continue // subtests are in results.json
		}
		fmt.Fprintf(&b, "| %s | %s | %s |\n", t.Package, t.Test, t.Result)
	}
	b.WriteString("\n## Latency\n\nThe legacy-customer example over loopback, 20,000 sequential requests after a 2,000 request warm-up, then 16 clients for 2 seconds.\n\n")
	b.WriteString("| Path | p50 | p99 | Requests/s (16 clients) |\n| --- | --- | --- | --- |\n")
	for _, l := range r.Latency {
		fmt.Fprintf(&b, "| %s | %.1f µs | %.1f µs | %.0f |\n", l.Name, l.P50Micros, l.P99Micros, l.ReqPerSec)
	}
	fmt.Fprintf(&b, "\nAdded by the proxy when no rule matches: %.1f µs at p50, %.1f µs at p99.\n", r.AddedP50, r.AddedP99)
	fmt.Fprintf(&b, "Added by the proxy with both example rules applied: %.1f µs at p50, %.1f µs at p99.\n", r.PatchedP50, r.PatchedP99)
	return b.String()
}
