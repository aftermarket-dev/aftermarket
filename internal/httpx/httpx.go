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
	return io.NopCloser(bytes.NewReader(b.Raw))
}

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
	if status == http.StatusRequestTimeout {
		WriteRequestTimeout(w) // the client's body stalled, not the upstream
		return
	}
	msg := "aftermarket: upstream unreachable\n"
	if status == http.StatusGatewayTimeout {
		msg = "aftermarket: upstream timed out\n"
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Aftermarket-Error", strconv.Itoa(status))
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

// BufferRequestBody reads up to max bytes of the client's body within d and
// puts it back on r, so later readers get the original bytes from memory. A
// body larger than max keeps streaming after the first max bytes. If the
// body stalls, it returns ErrRequestBodyTimeout and the request can't be
// forwarded.
func BufferRequestBody(w http.ResponseWriter, r *http.Request, max int64, d time.Duration) error {
	if r.Body == nil || r.Body == http.NoBody {
		return nil
	}
	rc := http.NewResponseController(w)
	deadlineSet := rc.SetReadDeadline(time.Now().Add(d)) == nil
	br, err := ReadBody(r.Body, max)
	var ne net.Error
	if err != nil && !errors.Is(err, ErrTooLarge) && errors.As(err, &ne) && ne.Timeout() {
		// The deadline stays: the server's last read of the stalled body,
		// after the 408, then gives up at once instead of waiting on the
		// client for good.
		return ErrRequestBodyTimeout
	}
	if deadlineSet {
		_ = rc.SetReadDeadline(time.Time{})
	}
	// On any other read error, what arrived is forwarded, so the upstream
	// sees the same broken body the proxy saw.
	r.Body = br.Restore()
	return nil
}

// WriteRequestTimeout answers a client whose body stalled.
func WriteRequestTimeout(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Aftermarket-Error", "408")
	w.Header().Set("Connection", "close")
	w.WriteHeader(http.StatusRequestTimeout)
	_, _ = w.Write([]byte("aftermarket: request body timed out\n"))
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
// Call it only right before handing the request to the reverse proxy,
// which closes the body before it returns. A handler that answers by itself
// without reading the body (a health check, a denial) must not call it:
// the server would then finish the body after its background read started,
// and drop the connection with a logged panic.
func KeepRequestBody(w http.ResponseWriter) {
	_ = http.NewResponseController(w).EnableFullDuplex()
}

// ErrClientBodyStalled is returned by a body from ForwardBody when the
// client stops sending it.
var ErrClientBodyStalled = errors.New("the client's request body stopped arriving")

// ForwardBody gets r's body ready to be streamed upstream, and is called
// right before the reverse proxy. It keeps the body open while the answer
// goes out (see KeepRequestBody), and gives up on a client whose body stops
// arriving: every read must make progress within idle, so an upload that is
// slow but moving is never cut off. The same limit covers the server's last
// read when the body is closed before its end, as when the upstream can't
// be reached, so a stalled client can't hold the connection. The caller
// must call the returned function when its handler returns; after that the
// connection's deadline is never touched.
func ForwardBody(w http.ResponseWriter, r *http.Request, idle time.Duration) (done func()) {
	KeepRequestBody(w)
	if r.Body == nil || r.Body == http.NoBody || idle <= 0 {
		return func() {}
	}
	b := &progressBody{ctl: http.NewResponseController(w), body: r.Body, idle: idle}
	r.Body = b
	return func() {
		b.mu.Lock()
		ended := b.ended
		b.mu.Unlock()
		if !ended {
			// The body is closed here, inside the handler, as the server
			// needs. The answer goes out first, and the rest of the body is
			// read for at most idle.
			_ = b.ctl.Flush()
			_ = b.ctl.SetReadDeadline(time.Now().Add(b.idle))
			_ = b.body.Close()
			_ = b.ctl.SetReadDeadline(time.Time{})
		}
		b.mu.Lock()
		b.handlerDone = true
		b.mu.Unlock()
	}
}

type progressBody struct {
	ctl  *http.ResponseController
	body io.ReadCloser
	idle time.Duration

	mu          sync.Mutex
	handlerDone bool
	ended       bool // the body reached its end; the server reads the connection now
	stalled     bool
}

// deadline moves the connection's read deadline, while that is still the
// body's to decide.
func (b *progressBody) deadline(t time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.handlerDone && !b.ended {
		_ = b.ctl.SetReadDeadline(t)
	}
}

func (b *progressBody) Read(p []byte) (int, error) {
	b.deadline(time.Now().Add(b.idle))
	n, err := b.body.Read(p)
	if err == io.EOF {
		// Clear the deadline before the server's own read of the connection
		// can trip over it, then leave the connection alone.
		b.deadline(time.Time{})
		b.mu.Lock()
		b.ended = true
		b.mu.Unlock()
		return n, err
	}
	var ne net.Error
	if err != nil && errors.As(err, &ne) && ne.Timeout() {
		b.mu.Lock()
		b.stalled = true
		b.mu.Unlock()
		return n, ErrClientBodyStalled
	}
	return n, err
}

// Close doesn't read the rest of the body: the transport closes it when the
// upstream can't be reached, and reading a stalled client there would hold
// up the 502. The function ForwardBody returned closes it once the answer
// has gone out.
func (b *progressBody) Close() error { return nil }

// ForwardError decides how to answer when forwarding r failed with err: a
// status and a reason for the log, or answer false when the client went
// away and there is nobody to answer. A client body that stalled gets 408
// even though its read error also ended the request's context. When the
// body hasn't reached its end, the answer closes the connection, so the
// server reads at most what arrives before the deadline and then lets go.
func ForwardError(w http.ResponseWriter, r *http.Request, err error) (status int, reason string, answer bool) {
	if b, ok := r.Body.(*progressBody); ok {
		b.mu.Lock()
		stalled, ended := b.stalled, b.ended
		b.mu.Unlock()
		if !ended {
			w.Header().Set("Connection", "close")
		}
		if stalled {
			return http.StatusRequestTimeout, "request body stopped arriving for upstream_timeout", true
		}
	}
	if ClientGone(r) {
		return 0, "", false
	}
	status, reason = UpstreamReason(err)
	return status, reason, true
}
