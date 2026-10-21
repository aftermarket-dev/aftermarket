// Package httpx holds the HTTP plumbing every Aftermarket proxy shares:
// reading bodies safely, undoing and redoing gzip, upstream timeouts and the
// fixed wording used for upstream errors.
package httpx

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aftermarket-dev/aftermarket/internal/ojson"
)

// IsJSON reports whether a Content-Type names JSON.
func IsJSON(ct string) bool {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		mt = strings.ToLower(strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]))
	}
	return mt == "application/json" || strings.HasSuffix(mt, "+json")
}

// Body is a buffered body that a rule wants to read.
type Body struct {
	Raw     []byte // bytes exactly as received (possibly compressed)
	Plain   []byte // decoded bytes, set by Decode
	Gzipped bool
	// rest is set when the body was larger than the limit: Raw holds the
	// first part and rest the remainder, still unread.
	rest io.ReadCloser
}

// Restore returns a body that yields exactly the original bytes.
func (b *Body) Restore() io.ReadCloser {
	if b.rest != nil {
		return &multiCloser{Reader: io.MultiReader(bytes.NewReader(b.Raw), b.rest), c: b.rest}
	}
	return memoryBody{bytes.NewReader(b.Raw)}
}

// memoryBody is a request body held in memory in full: the client's body
// has already been read to its end.
type memoryBody struct{ *bytes.Reader }

func (memoryBody) Close() error { return nil }

type multiCloser struct {
	io.Reader
	c io.Closer
}

func (m *multiCloser) Close() error { return m.c.Close() }

// ErrTooLarge is returned when a body is over the limit.
var ErrTooLarge = errors.New("body is larger than max_body")

// ReadBody buffers up to max bytes of body. On ErrTooLarge the returned Body
// can still restore the full original stream.
func ReadBody(body io.ReadCloser, max int64) (*Body, error) {
	br := &Body{}
	if body == nil || body == http.NoBody {
		return br, nil
	}
	buf, err := io.ReadAll(io.LimitReader(body, max+1))
	br.Raw = buf
	if err != nil {
		br.rest = body
		return br, err
	}
	if int64(len(buf)) > max {
		br.rest = body
		return br, ErrTooLarge
	}
	_ = body.Close()
	return br, nil
}

// TimeoutError is returned when a body a rule needs does not arrive in time.
type TimeoutError struct{}

func (TimeoutError) Error() string   { return "upstream body did not arrive within upstream_timeout" }
func (TimeoutError) Timeout() bool   { return true }
func (TimeoutError) Temporary() bool { return true }

// ReadBodyWithin is ReadBody with a deadline. On timeout the body is closed
// and a TimeoutError returned.
func ReadBodyWithin(body io.ReadCloser, max int64, d time.Duration) (*Body, error) {
	type result struct {
		br  *Body
		err error
	}
	ch := make(chan result, 1)
	go func() {
		br, err := ReadBody(body, max)
		ch <- result{br, err}
	}()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case r := <-ch:
		return r.br, r.err
	case <-t.C:
		_ = body.Close()
		<-ch
		return nil, TimeoutError{}
	}
}

// Decode undoes Content-Encoding. Only identity and gzip are supported. On
// failure it returns the reason.
func (b *Body) Decode(encoding string, max int64) (string, bool) {
	enc := strings.ToLower(strings.TrimSpace(encoding))
	switch enc {
	case "", "identity":
		b.Plain = b.Raw
		return "", true
	case "gzip", "x-gzip":
		zr, err := gzip.NewReader(bytes.NewReader(b.Raw))
		if err != nil {
			return "gzip body could not be read", false
		}
		plain, err := io.ReadAll(io.LimitReader(zr, max+1))
		if err != nil {
			return "gzip body could not be read", false
		}
		if int64(len(plain)) > max {
			return "decompressed body is larger than max_body", false
		}
		b.Plain = plain
		b.Gzipped = true
		return "", true
	}
	return "content encoding " + enc + " is not supported for JSON changes", false
}

// Encode re-applies gzip when gzipped is set.
func Encode(plain []byte, gzipped bool) []byte {
	if !gzipped {
		return plain
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(plain)
	_ = zw.Close()
	return buf.Bytes()
}

// NopBody wraps bytes as a request or response body.
func NopBody(b []byte) io.ReadCloser { return io.NopCloser(bytes.NewReader(b)) }

// DropBodyValidators removes headers that describe the old body bytes.
func DropBodyValidators(h http.Header) {
	for _, k := range []string{"Etag", "Content-Md5", "Digest", "Content-Digest", "Repr-Digest"} {
		h.Del(k)
	}
}

// SetResponseBody replaces a response body and fixes the framing headers.
func SetResponseBody(resp *http.Response, b []byte) {
	resp.Body = NopBody(b)
	resp.ContentLength = int64(len(b))
	resp.Header.Set("Content-Length", strconv.Itoa(len(b)))
	resp.TransferEncoding = nil
	// Trailers announced for the old chunked body cannot follow a body with
	// a length.
	resp.Header.Del("Trailer")
	resp.Trailer = nil
	DropBodyValidators(resp.Header)
}

// SetRequestBody replaces an outgoing request body and fixes the framing
// headers.
func SetRequestBody(r *http.Request, b []byte) {
	r.Body = NopBody(b)
	r.ContentLength = int64(len(b))
	r.Header.Set("Content-Length", strconv.Itoa(len(b)))
	r.TransferEncoding = nil
	r.GetBody = nil
	DropBodyValidators(r.Header)
}

// UnsafePath reports whether a path contains "." or ".." segments or an
// encoded slash. Upstreams resolve these in different ways, so rules that
// depend on the path never match such requests.
func UnsafePath(u *url.URL) bool {
	if strings.Contains(strings.ToLower(u.EscapedPath()), "%2f") {
		return true
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if seg == "." || seg == ".." {
			return true
		}
	}
	return false
}

// SafeSegment reports whether a value can be placed in a path segment
// without adding or climbing segments.
func SafeSegment(v string) bool {
	return v != "" && v != "." && v != ".." && !strings.ContainsAny(v, "/\\")
}

// JoinPath joins an upstream base path and a request path.
func JoinPath(base, p string) string {
	if base == "" || base == "/" {
		return p
	}
	return strings.TrimSuffix(base, "/") + p
}

// NewTransport returns the transport the proxies use: no proxy from the
// environment, bodies passed through as sent, and the response header
// timeout from the rule file.
func NewTransport(timeout time.Duration) *http.Transport {
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: timeout,
		ExpectContinueTimeout: 1 * time.Second,
		DisableCompression:    true,
	}
}

// UpstreamReason turns an upstream error into a status and a fixed
// description, so error text (which can quote upstream bytes) never reaches
// the audit log.
func UpstreamReason(err error) (int, string) {
	var ne net.Error
	if errors.As(err, &TimeoutError{}) {
		return http.StatusGatewayTimeout, "upstream body did not arrive within upstream_timeout"
	}
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return http.StatusGatewayTimeout, "upstream did not answer within upstream_timeout"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return http.StatusBadGateway, "upstream host name could not be resolved"
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return http.StatusBadGateway, "could not connect to upstream"
	}
	var certErr *tls.CertificateVerificationError
	var unknownAuth x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	var recErr tls.RecordHeaderError
	if errors.As(err, &certErr) || errors.As(err, &unknownAuth) || errors.As(err, &hostErr) || errors.As(err, &recErr) {
		return http.StatusBadGateway, "TLS error talking to upstream"
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return http.StatusBadGateway, "upstream closed the connection"
	}
	return http.StatusBadGateway, "upstream sent a response that could not be read"
}

// WriteUpstreamError answers the client when the upstream failed.
func WriteUpstreamError(w http.ResponseWriter, status int) {
	switch status {
	case http.StatusRequestTimeout: // the client's body stalled, not the upstream
		WriteRequestTimeout(w)
		return
	case http.StatusBadRequest: // the client's body broke off
		writeClose(w, http.StatusBadRequest, "aftermarket: request body was cut off\n")
		return
	}
	msg := "aftermarket: upstream unreachable\n"
	if status == http.StatusGatewayTimeout {
		msg = "aftermarket: upstream timed out\n"
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Aftermarket-Error", strconv.Itoa(status))
	w.Header().Set("Content-Length", strconv.Itoa(len(msg)))
	w.WriteHeader(status)
	_, _ = w.Write([]byte(msg))
}

// WriteHealth answers the health check.
func WriteHealth(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

// ClientGone reports whether the client cancelled the request.
func ClientGone(r *http.Request) bool {
	return errors.Is(r.Context().Err(), context.Canceled)
}

// JSONBody is the outcome of reading a body that a rule wants to change as
// JSON. When OK is false, Skip says why and the body must pass unchanged.
type JSONBody struct {
	Body *Body
	Doc  any
	OK   bool
	Skip string
}

// ReadJSON buffers and decodes a JSON body. what is "request" or
// "response" and goes into the skip reason. It returns a body that yields
// the original bytes, to put back on the message whatever happens. A
// deadline of zero means none. The only error is a TimeoutError.
func ReadJSON(rc io.ReadCloser, contentEncoding string, max int64, deadline time.Duration, what string) (JSONBody, io.ReadCloser, error) {
	var res JSONBody
	var br *Body
	var err error
	if deadline > 0 {
		br, err = ReadBodyWithin(rc, max, deadline)
		if errors.As(err, &TimeoutError{}) {
			return res, nil, err
		}
	} else {
		br, err = ReadBody(rc, max)
	}
	res.Body = br
	restored := br.Restore()
	switch {
	case errors.Is(err, ErrTooLarge):
		res.Skip = what + " body is larger than max_body"
	case err != nil:
		res.Skip = what + " body could not be read"
	default:
		if reason, ok := br.Decode(contentEncoding, max); !ok {
			res.Skip = reason
		} else if d, err := ojson.Decode(br.Plain); err != nil {
			res.Skip = what + " body is not JSON that can be changed safely (" + err.Error() + ")"
		} else {
			res.Doc, res.OK = d, true
		}
	}
	return res, restored, nil
}

// ErrRequestBodyTimeout is returned when a client's body does not arrive in
// time.
var ErrRequestBodyTimeout = errors.New("request body did not arrive within upstream_timeout")

// BufferRequestBody reads up to max bytes of the client's body and puts it
// back on r, so later readers get the original bytes from memory. A body
// larger than max keeps streaming after the first max bytes. r must come
// from WatchBody, whose limit on each read stops a stalled client: then it
// returns ErrRequestBodyTimeout and the request can't be forwarded.
func BufferRequestBody(r *http.Request, max int64) error {
	if r.Body == nil || r.Body == http.NoBody {
		return nil
	}
	br, err := ReadBody(r.Body, max)
	switch {
	case errors.Is(err, ErrClientBodyStalled):
		return ErrRequestBodyTimeout
	case err != nil && !errors.Is(err, ErrTooLarge):
		// Never translated or forwarded as if it were whole.
		return ErrRequestBodyCut
	}
	r.Body = br.Restore()
	return nil
}

// ErrRequestBodyCut is returned when the client's body broke off before
// its end: the connection closed or the framing was wrong.
var ErrRequestBodyCut = errors.New("the client's request body was cut off")

// WriteBodyError answers a client whose body couldn't be read: 408 when it
// stalled, 400 when it was cut off.
func WriteBodyError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrRequestBodyCut) {
		writeClose(w, http.StatusBadRequest, "aftermarket: request body was cut off\n")
		return
	}
	WriteRequestTimeout(w)
}

func writeClose(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Aftermarket-Error", strconv.Itoa(status))
	w.Header().Set("Connection", "close")
	w.Header().Set("Content-Length", strconv.Itoa(len(msg)))
	w.WriteHeader(status)
	_, _ = w.Write([]byte(msg))
}

// WriteRequestTimeout answers a client whose body stalled.
func WriteRequestTimeout(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	msg := "aftermarket: request body timed out\n"
	w.Header().Set("X-Aftermarket-Error", "408")
	w.Header().Set("Connection", "close")
	w.Header().Set("Content-Length", strconv.Itoa(len(msg)))
	w.WriteHeader(http.StatusRequestTimeout)
	_, _ = w.Write([]byte(msg))
}

// SafeURL writes u for logs, reports and file headers: scheme, host and
// path only, so a password or a query string key never shows.
func SafeURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path, RawPath: u.RawPath}).String()
}

// KeepRequestBody stops Go's server from finishing and closing the client's
// request body when the answer starts going out. A proxy streams that body
// upstream from the transport's own goroutine, and a fast upstream can
// answer before the transport has read the body's end. Closed under it, the
// transport counts the request as failed and drops the upstream connection
// in the middle of the answer. With the body left alone, the server still
// finishes it once the handler returns.
//
// The handler must then close the body itself before it returns (the
// reverse proxy does, and so does WatchBody's done). Otherwise the server
// finishes the body after its own read of the connection has started, and
// drops the connection with a logged panic.
func KeepRequestBody(w http.ResponseWriter) {
	_ = http.NewResponseController(w).EnableFullDuplex()
}

// ErrClientBodyStalled is returned by a body from WatchBody when the
// client stops sending it.
var ErrClientBodyStalled = errors.New("the client's request body stopped arriving")

// WatchBody is called at the top of a proxy's handler, after anything the
// handler answers by itself (the health check). It returns the request to
// work on: a copy, since the server checks its own request's body when it
// decides whether to reuse the connection, with a context that the client
// closing its side doesn't cancel, so a client that half-closes after its
// request still gets the real answer (a client that is really gone shows
// up as a failed write). The client's live body is watched once, here:
//
//   - Every read must make progress within idle, so an upload that is slow
//     but moving is never cut off, and one that stops is.
//   - Once every byte has arrived, the connection's read deadline is
//     cleared and never touched again, so it can't trip the server's own
//     read of the connection. Copies of the body read from memory later
//     never move it.
//   - The body is kept open while the answer goes out (see
//     KeepRequestBody).
//
// done must be called when the handler returns. If the body hasn't ended,
// it sends the answer, then reads what the client still sends for at most
// idle, and closes the body inside the handler, as the server needs.
func WatchBody(w http.ResponseWriter, r *http.Request, idle time.Duration) (out *http.Request, done func()) {
	// The forwarded request isn't cancelled the moment the server sees the
	// client's side close, which a half-close looks like, but idle later:
	// a client that half-closed gets its answer if it comes in that time,
	// and one that is really gone frees the upstream call by then. (A
	// context that can't be cancelled at all makes the reverse proxy fall
	// back to CloseNotify, which fires on a half-close.)
	ctx, cancelNow := context.WithCancel(context.WithoutCancel(r.Context()))
	var graceMu sync.Mutex
	var grace *time.Timer
	stopped := false
	stopGrace := context.AfterFunc(r.Context(), func() {
		graceMu.Lock()
		if !stopped {
			grace = time.AfterFunc(idle, cancelNow)
		}
		graceMu.Unlock()
	})
	cancel := func() {
		stopGrace()
		graceMu.Lock()
		stopped = true
		if grace != nil {
			grace.Stop()
		}
		graceMu.Unlock()
		cancelNow()
	}
	if r.Body == nil || r.Body == http.NoBody || idle <= 0 {
		return r.WithContext(ctx), cancel
	}
	KeepRequestBody(w)
	b := &progressBody{ctl: http.NewResponseController(w), body: r.Body, idle: idle, left: r.ContentLength,
		expect: strings.EqualFold(r.Header.Get("Expect"), "100-continue")}
	out = r.WithContext(context.WithValue(ctx, bodyKey{}, b))
	out.Body = b
	return out, func() {
		b.mu.Lock()
		ended, sawEOF, upgraded, readAny := b.ended, b.sawEOF, b.upgraded, b.readAny
		b.mu.Unlock()
		switch {
		case upgraded:
			// The connection was handed over (101); it isn't the server's.
		case ended && !sawEOF:
			// Every byte arrived but the server hasn't seen the end yet. It
			// must, before the handler returns, or its own read of the
			// connection starts too late and clashes with the next request.
			_ = b.body.Close()
		case !ended:
			_ = b.ctl.Flush()
			until := time.Now().Add(b.idle)
			b.mu.Lock()
			broken := b.err != nil
			b.closing = true // from here on the deadline is done's to set
			b.mu.Unlock()
			if broken || (b.expect && !readAny) {
				// The body stalled or broke off, or no 100 Continue went out
				// and none is coming: no reason to wait for more.
				until = time.Now()
			}
			_ = b.ctl.SetReadDeadline(until)
			_ = b.body.Close()
			_ = b.ctl.SetReadDeadline(time.Time{})
		}
		b.mu.Lock()
		b.handlerDone = true
		b.mu.Unlock()
		cancel()
	}
}

type bodyKey struct{}

func watched(r *http.Request) *progressBody {
	b, _ := r.Context().Value(bodyKey{}).(*progressBody)
	return b
}

// DrainBody reads and drops up to max bytes of a body that won't be
// forwarded (adapt replaced or dropped it), so the connection can be used
// again. A stalled client gives ErrRequestBodyTimeout. Past max, the rest
// is left, and the answer closes the connection.
func DrainBody(r *http.Request, max int64) error {
	if r.Body == nil || r.Body == http.NoBody {
		return nil
	}
	_, err := io.CopyN(io.Discard, r.Body, max+1)
	switch {
	case errors.Is(err, ErrClientBodyStalled):
		return ErrRequestBodyTimeout
	case err != nil && err != io.EOF:
		return ErrRequestBodyCut
	}
	return nil
}

// CloseIfBodyUnfinished marks an answer to close the connection when the
// client's body hasn't reached its end as the answer starts: the upstream
// answered early, or turned the request away unread. Whatever the client
// still sends is then read for a bounded time and the connection closed,
// never parsed as a next request. Call it from ModifyResponse. A switch of
// protocols (101) is left alone.
func CloseIfBodyUnfinished(resp *http.Response) {
	b := watched(resp.Request)
	if b == nil {
		return
	}
	if resp.StatusCode < 200 {
		if resp.StatusCode == http.StatusSwitchingProtocols {
			b.mu.Lock()
			b.upgraded = true
			b.mu.Unlock()
		}
		return
	}
	if !b.isEnded() {
		resp.Header.Set("Connection", "close")
	}
}

type progressBody struct {
	ctl    *http.ResponseController
	body   io.ReadCloser
	idle   time.Duration
	expect bool

	mu          sync.Mutex
	left        int64 // bytes still due by Content-Length, or -1
	readAny     bool
	handlerDone bool
	ended       bool // every byte of the body has arrived
	sawEOF      bool // the server has seen the end and reads the connection now
	stalled     bool
	err         error // the first read error, returned from then on
	upgraded    bool
	closing     bool
}

func (b *progressBody) isEnded() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ended
}

// deadline moves the connection's read deadline, while that is still the
// body's to decide.
func (b *progressBody) deadline(t time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.handlerDone && !b.ended && !b.upgraded && !b.closing {
		_ = b.ctl.SetReadDeadline(t)
	}
}

func (b *progressBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	if b.err != nil {
		// A body that broke off stays broken: Go's server reports a clean
		// end after an unexpected one, and the rest would pass as whole.
		err := b.err
		b.mu.Unlock()
		return 0, err
	}
	b.mu.Unlock()
	b.deadline(time.Now().Add(b.idle))
	n, err := b.body.Read(p)
	b.mu.Lock()
	b.readAny = true
	if b.left >= 0 {
		b.left -= int64(n)
	}
	end := err == io.EOF || (b.left == 0 && err == nil)
	b.mu.Unlock()
	if end {
		// Every byte has arrived. Clear the deadline before the server's own
		// read of the connection can trip over it, then leave it alone.
		b.deadline(time.Time{})
		b.mu.Lock()
		b.ended = true
		b.sawEOF = b.sawEOF || err == io.EOF
		b.mu.Unlock()
		return n, err
	}
	if err == nil {
		return n, nil
	}
	var ne net.Error
	b.mu.Lock()
	defer b.mu.Unlock()
	if errors.As(err, &ne) && ne.Timeout() {
		b.stalled = true
		b.err = ErrClientBodyStalled
	} else {
		b.err = ErrRequestBodyCut
	}
	return n, b.err
}

// Close doesn't read the rest of the body: the transport closes it when the
// upstream can't be reached, and reading a stalled client there would hold
// up the 502. The function WatchBody returned closes it once the answer has
// gone out.
func (b *progressBody) Close() error { return nil }

// ForwardError decides how to answer when forwarding r failed with err: a
// status and a reason for the log. A client body that stalled gets 408.
// When the body hasn't reached its end, the answer closes the connection,
// so the server reads at most what arrives before the deadline and then
// lets go.
func ForwardError(w http.ResponseWriter, r *http.Request, err error) (status int, reason string) {
	if b := watched(r); b != nil {
		b.mu.Lock()
		stalled, ended, broken := b.stalled, b.ended, b.err != nil
		b.mu.Unlock()
		if !ended {
			w.Header().Set("Connection", "close")
		}
		switch {
		case stalled:
			return http.StatusRequestTimeout, "request body stopped arriving for upstream_timeout"
		case broken:
			return http.StatusBadRequest, "request body was cut off by the client"
		}
	}
	return UpstreamReason(err)
}
