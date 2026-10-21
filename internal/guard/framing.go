package guard

import (
	"bytes"
	"context"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
)

// The tracker follows the bytes of each HTTP/1 connection as Go's server
// reads them, request head by request head, and records what Go's parser
// smooths over: a request with both Content-Length and Transfer-Encoding,
// a header folded over two lines, or lines ending in a bare LF. Go accepts
// these quietly, and another server in front of or behind AfterGuard might
// read them another way, so AfterGuard rejects them.
//
// The tracker has to follow bodies too, to find the next head on a
// keep-alive connection, and it reads them exactly as Go does. If it ever
// loses its place, the connection is marked lost and every later request on
// it is rejected; a request the tracker has no verdict for is rejected too,
// so a confused tracker fails closed.

type verdict struct {
	reason string // "" when the head was clean
}

type framing int

const (
	inHead framing = iota
	inBody
	inChunkSize
	inChunkData
	inChunkEnd
	inTrailer
	passThrough // an upgraded connection, or HTTP/2: no longer HTTP/1 framing
	lost
)

const (
	maxChunkLine   = 4096
	maxVerdicts    = 64 // heads judged but not yet handled, on one connection
	maxChunkDigits = 16 // as in Go's chunked reader
)

type tracker struct {
	net.Conn
	maxHead int

	mu        sync.Mutex
	state     framing
	buf       []byte // head or line being collected
	remaining int64
	verdicts  []verdict
	lostWhy   string
}

func newTracker(c net.Conn, maxHead int) *tracker {
	return &tracker{Conn: c, maxHead: maxHead}
}

func (t *tracker) Read(p []byte) (int, error) {
	n, err := t.Conn.Read(p)
	if n > 0 {
		t.mu.Lock()
		t.feed(p[:n])
		t.mu.Unlock()
	}
	return n, err
}

// next returns the verdict for the oldest request head not yet judged.
func (t *tracker) next() (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.verdicts) > 0 {
		v := t.verdicts[0]
		t.verdicts = t.verdicts[1:]
		return v.reason, true
	}
	if t.state == lost {
		return "the connection's framing could not be followed: " + t.lostWhy, true
	}
	return "", false
}

// upgraded is called when the app agreed to an upgrade (101): from here on
// the connection carries another protocol, and nothing more is judged.
func (t *tracker) upgraded() {
	t.mu.Lock()
	if t.state != lost {
		t.state = passThrough
		t.buf = nil
	}
	t.mu.Unlock()
}

// fail marks the connection lost, for a request whose body Go could not
// read: Go and the tracker may no longer agree where the next request
// starts.
func (t *tracker) fail(why string) {
	t.mu.Lock()
	t.lose(why)
	t.mu.Unlock()
}

func (t *tracker) lose(why string) {
	t.state = lost
	t.lostWhy = why
	t.buf = nil
}

func (t *tracker) feed(p []byte) {
	for len(p) > 0 {
		switch t.state {
		case passThrough, lost:
			return
		case inHead:
			if len(t.buf) == 0 {
				// Empty lines before a request line are allowed and ignored.
				for len(p) > 0 && (p[0] == '\r' || p[0] == '\n') {
					p = p[1:]
				}
				if len(p) == 0 {
					return
				}
			}
			end := headEnd(t.buf, p)
			if end < 0 {
				t.buf = append(t.buf, p...)
				if len(t.buf) > t.maxHead+4096 {
					// Go answers 431, or, with part of the head already in
					// its buffer, may still accept it: either way the
					// tracker can't vouch for it.
					t.lose("a request head is too long")
				}
				return
			}
			take := end - len(t.buf)
			t.buf = append(t.buf, p[:take]...)
			p = p[take:]
			t.finishHead()
		case inBody:
			n := int64(len(p))
			if n > t.remaining {
				n = t.remaining
			}
			t.remaining -= n
			p = p[n:]
			if t.remaining == 0 {
				t.endMessage()
			}
		case inChunkEnd:
			// Go wants exactly CRLF after a chunk's data.
			for len(p) > 0 && len(t.buf) < 2 {
				t.buf = append(t.buf, p[0])
				p = p[1:]
			}
			if len(t.buf) == 2 {
				if string(t.buf) != "\r\n" {
					t.lose("a chunk did not end with CRLF")
					return
				}
				t.buf = nil
				t.state = inChunkSize
			}
		case inChunkSize, inTrailer:
			i := bytes.IndexByte(p, '\n')
			if i < 0 {
				t.buf = append(t.buf, p...)
				if len(t.buf) > maxChunkLine {
					t.lose("a chunk line is too long")
				}
				return
			}
			line := append(t.buf, p[:i+1]...)
			t.buf = nil
			p = p[i+1:]
			t.line(string(line))
		case inChunkData:
			n := int64(len(p))
			if n > t.remaining {
				n = t.remaining
			}
			t.remaining -= n
			p = p[n:]
			if t.remaining == 0 {
				t.state = inChunkEnd
				t.buf = nil
			}
		}
	}
}

// headEnd finds where the head ends (just past its empty line) in buf+p, as
// an offset into the joined bytes, or -1.
func headEnd(buf, p []byte) int {
	// Look back a few bytes into buf so a terminator split across reads is
	// found.
	start := len(buf) - 3
	if start < 0 {
		start = 0
	}
	joined := append(append([]byte(nil), buf[start:]...), p...)
	for i := 0; i < len(joined); i++ {
		if joined[i] != '\n' {
			continue
		}
		if i+1 < len(joined) && joined[i+1] == '\n' {
			return start + i + 2
		}
		if i+2 < len(joined) && joined[i+1] == '\r' && joined[i+2] == '\n' {
			return start + i + 3
		}
	}
	return -1
}

func (t *tracker) finishHead() {
	head := string(t.buf)
	t.buf = nil
	if strings.HasPrefix(head, "PRI * HTTP/2.0") {
		t.state = passThrough
		return
	}
	reason, cl, chunked, ok := inspectHead(head)
	if len(t.verdicts) >= maxVerdicts {
		t.lose("too many requests sent ahead of their answers")
		return
	}
	t.verdicts = append(t.verdicts, verdict{reason: reason})
	if !ok {
		// Go will refuse this head or the connection is about to close;
		// either way nothing after it can be trusted.
		t.lose("an earlier request on it could not be framed")
		return
	}
	switch {
	case chunked:
		t.state = inChunkSize
	case cl > 0:
		t.state = inBody
		t.remaining = cl
	default:
		t.endMessage()
	}
}

func (t *tracker) endMessage() {
	t.state = inHead
}

// line handles one chunk-size or trailer line. A size line is read the way
// Go 1.24 reads it: it must end in CRLF with no other CR, then trailing
// spaces and tabs are trimmed, the extension after ; is dropped, and what is
// left must be 1 to 16 hex digits.
func (t *tracker) line(raw string) {
	switch t.state {
	case inChunkSize:
		if !strings.HasSuffix(raw, "\r\n") || strings.IndexByte(raw, '\r') != len(raw)-2 {
			t.lose("a chunk size line does not end in CRLF")
			return
		}
		size := strings.TrimRight(raw[:len(raw)-2], " \t")
		if i := strings.IndexByte(size, ';'); i >= 0 {
			size = size[:i]
		}
		if size == "" || len(size) > maxChunkDigits {
			t.lose("a chunk size could not be read")
			return
		}
		for i := 0; i < len(size); i++ {
			c := size[i]
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				t.lose("a chunk size could not be read")
				return
			}
		}
		n, err := strconv.ParseUint(size, 16, 64)
		if err != nil || n > 1<<62 {
			t.lose("a chunk size is too large")
			return
		}
		if n == 0 {
			t.state = inTrailer
			return
		}
		t.remaining = int64(n)
		t.state = inChunkData
	case inTrailer:
		if strings.TrimRight(raw, "\r\n") == "" {
			t.endMessage()
		}
	}
}

// inspectHead reads a request head as raw lines. reason is why the request
// must be rejected ("" when it is clean); ok is false when the framing can't
// be followed at all.
func inspectHead(head string) (reason string, contentLength int64, chunked, ok bool) {
	lines := strings.Split(head, "\n")
	http10 := strings.HasSuffix(strings.TrimRight(lines[0], "\r"), "HTTP/1.0")
	var cls []string
	tes := 0
	ok = true
	for i, ln := range lines {
		last := i == len(lines)-1
		if last && ln == "" {
			break
		}
		if !strings.HasSuffix(ln, "\r") {
			if reason == "" {
				reason = "a line in the request head ends in a bare LF"
			}
		}
		ln = strings.TrimSuffix(ln, "\r")
		if i == 0 || ln == "" {
			continue
		}
		if ln[0] == ' ' || ln[0] == '\t' {
			if reason == "" {
				reason = "a header is folded over two lines"
			}
			continue
		}
		colon := strings.IndexByte(ln, ':')
		if colon <= 0 {
			continue // Go rejects it
		}
		name := strings.ToLower(ln[:colon])
		value := strings.TrimSpace(ln[colon+1:])
		switch name {
		case "content-length":
			cls = append(cls, value)
		case "transfer-encoding":
			tes++
			toks := strings.Split(strings.ToLower(value), ",")
			if strings.TrimSpace(toks[len(toks)-1]) == "chunked" {
				chunked = true
			}
		}
	}
	if tes > 0 && len(cls) > 0 && reason == "" {
		reason = "the request has both Content-Length and Transfer-Encoding"
	}
	if tes > 1 && reason == "" {
		reason = "the request has more than one Transfer-Encoding header"
	}
	if tes > 0 && http10 {
		// Go ignores Transfer-Encoding on HTTP/1.0, others don't.
		if reason == "" {
			reason = "an HTTP/1.0 request has Transfer-Encoding"
		}
		ok = false
	}
	if tes > 0 && !chunked {
		ok = false // Go answers 501 and closes
	}
	if len(cls) > 0 && !chunked {
		for _, v := range cls {
			if v != cls[0] {
				ok = false // Go answers 400 and closes
			}
		}
		n, err := strconv.ParseInt(cls[0], 10, 64)
		if err != nil || n < 0 {
			ok = false
		}
		contentLength = n
	}
	return reason, contentLength, chunked, ok
}

type trackerKey struct{}

// connContext puts the connection's tracker where the handler finds it.
func connContext(ctx context.Context, c net.Conn) context.Context {
	if t, ok := c.(*tracker); ok {
		return context.WithValue(ctx, trackerKey{}, t)
	}
	return ctx
}

// connLimits caps connections in total and per client address, so one
// client can't hold every slot. A connection over a limit is closed at once.
type connLimits struct {
	net.Listener
	max, perAddr int
	onRefuse     func(addr string)

	trusted []netip.Prefix // proxies: many clients share their address

	mu     sync.Mutex
	total  int
	byAddr map[string]int
}

func newListener(ln net.Listener, c *Config, onRefuse func(string)) *connLimits {
	return &connLimits{Listener: ln, max: c.MaxConns, perAddr: c.MaxConnsPerAddr, trusted: c.TrustedProxies,
		onRefuse: onRefuse, byAddr: map[string]int{}}
}

func (l *connLimits) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		addr, _ := clientIP(c.RemoteAddr().String())
		key := addrKey(addr) // an IPv6 /64 counts as one address
		if inAny(addr, l.trusted) {
			key = "" // a trusted proxy carries many clients; only the total counts
		}
		l.mu.Lock()
		over := l.total >= l.max || (key != "" && l.byAddr[key] >= l.perAddr)
		if !over {
			l.total++
			if key != "" {
				l.byAddr[key]++
			}
		}
		l.mu.Unlock()
		if over {
			c.Close()
			if l.onRefuse != nil {
				l.onRefuse(addr.String())
			}
			continue
		}
		return &limitedConn{Conn: c, release: func() { l.release(key) }}, nil
	}
}

// trackListener puts a tracker on each connection. With TLS it sits above
// the TLS layer, where the request bytes are plain.
type trackListener struct {
	net.Listener
	maxHead int
}

func (l trackListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return newTracker(c, l.maxHead), nil
}

func (l *connLimits) release(key string) {
	l.mu.Lock()
	l.total--
	if key != "" {
		if l.byAddr[key]--; l.byAddr[key] <= 0 {
			delete(l.byAddr, key)
		}
	}
	l.mu.Unlock()
}

// open is how many connections are held, for tests.
func (l *connLimits) open() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.total
}

type limitedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}
