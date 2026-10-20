package record

import (
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/aftermarket-dev/aftermarket/internal/version"
)

// recordedCase is one exchange, already redacted and decoded.
type recordedCase struct {
	method   string
	uri      string // escaped path and query, as the client sent it
	reqHdr   http.Header
	reqBody  string
	status   int
	respHdr  http.Header
	respBody string
}

// caseWriter appends cases to the fixture file. The file is valid YAML after
// every case, so stopping the recorder at any moment leaves a usable fixture.
type caseWriter struct {
	mu  sync.Mutex
	w   io.Writer
	n   int
	max int
	err error
}

func newCaseWriter(w io.Writer, upstream string, max int) (*caseWriter, error) {
	cw := &caseWriter{w: w, max: max}
	_, err := fmt.Fprintf(w, "# Recorded by aftermarket record %s from %s.\n# Credentials and listed values read %s.\ncases:\n", version.Version, quote(upstream), Redacted)
	return cw, err
}

// full reports whether max_cases has been reached.
func (cw *caseWriter) full() bool {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	return cw.max > 0 && cw.n >= cw.max
}

// write appends c. It returns false when max_cases was reached or the file
// can't be written.
func (cw *caseWriter) write(c recordedCase) (bool, string) {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	if cw.max > 0 && cw.n >= cw.max {
		return false, "max_cases reached"
	}
	if cw.err != nil {
		return false, "the fixture file could not be written"
	}
	cw.n++
	path, _, _ := strings.Cut(c.uri, "?")
	var b strings.Builder
	fmt.Fprintf(&b, "  - name: %s\n", quote(fmt.Sprintf("%d %s %s", cw.n, c.method, path)))
	b.WriteString("    request:\n")
	fmt.Fprintf(&b, "      method: %s\n", quote(c.method))
	fmt.Fprintf(&b, "      path: %s\n", quote(c.uri))
	writeHeaders(&b, "      ", c.reqHdr)
	if c.reqBody != "" {
		fmt.Fprintf(&b, "      body: %s\n", quote(c.reqBody))
	}
	b.WriteString("    upstream:\n")
	fmt.Fprintf(&b, "      status: %d\n", c.status)
	writeHeaders(&b, "      ", c.respHdr)
	fmt.Fprintf(&b, "      body: %s\n", quote(c.respBody))
	b.WriteString("    expect:\n      response:\n")
	fmt.Fprintf(&b, "        status: %d\n", c.status)
	if ct := c.respHdr.Get("Content-Type"); ct != "" {
		fmt.Fprintf(&b, "        headers:\n          \"Content-Type\": %s\n", quote(ct))
	}
	fmt.Fprintf(&b, "        body: %s\n", quote(c.respBody))
	if _, err := io.WriteString(cw.w, b.String()); err != nil {
		cw.err = err
		cw.n--
		return false, "the fixture file could not be written"
	}
	return true, ""
}

func writeHeaders(b *strings.Builder, indent string, h http.Header) {
	if len(h) == 0 {
		return
	}
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b.WriteString(indent + "headers:\n")
	for _, k := range keys {
		fmt.Fprintf(b, "%s  %s: %s\n", indent, quote(k), quote(strings.Join(h[k], ", ")))
	}
}

// quote writes s as a YAML double-quoted scalar that yamlite reads back
// exactly. s must be valid UTF-8.
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\r':
			b.WriteString(`\r`)
		case r < 0x20 || r == 0x7f || r == 0x2028 || r == 0x2029 || r == 0xfeff:
			fmt.Fprintf(&b, `\u%04x`, r)
		case r == utf8.RuneError:
			b.WriteString(`�`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
