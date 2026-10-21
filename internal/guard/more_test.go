package guard

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeCert(t *testing.T, dir, cn string) (string, string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"localhost"},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	cp, kp := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	os.WriteFile(cp, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(kp, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600)
	return cp, kp
}

// HTTPS from certificate files, with the framing checks still in place, and
// a renewed certificate picked up without a restart.
func TestTLS(t *testing.T) {
	dir := t.TempDir()
	cp, kp := writeCert(t, dir, "first")
	r := start(t, fmt.Sprintf("tls: {cert: %s, key: %s}\nrules:\n  - id: open\n    when: {path: /**}\n    allow: true\n", cp, kp))
	tlsClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, Timeout: 5 * time.Second}
	resp, err := tlsClient.Get("https://" + r.addr + "/x")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.TLS.PeerCertificates[0].Subject.CommonName != "first" || r.app.last().header.Get("X-Forwarded-Proto") != "https" {
		t.Fatalf("%d %v", resp.StatusCode, r.app.last().header)
	}
	// Ambiguous framing over TLS is rejected too.
	c, err := tls.Dial("tcp", r.addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	c.Write([]byte("POST /x HTTP/1.1\r\nHost: a\r\nContent-Length: 4\r\nTransfer-Encoding: chunked\r\n\r\n0\r\n\r\n"))
	resp, err = http.ReadResponse(bufio.NewReader(c), nil)
	c.Close()
	if err != nil || resp.StatusCode != 400 {
		t.Fatalf("framing over TLS: %v %v", resp, err)
	}
	// A renewed pair is used once the files change.
	future := time.Now().Add(time.Minute)
	certs, _ := newCertFiles(cp, kp)
	writeCert(t, dir, "third")
	os.Chtimes(cp, future.Add(time.Minute), future.Add(time.Minute))
	got, _ := certs.get(nil)
	leaf, _ := x509.ParseCertificate(got.Certificate[0])
	if leaf.Subject.CommonName != "third" {
		t.Fatalf("after renewal: %s", leaf.Subject.CommonName)
	}
	// A broken pair keeps the old one in use.
	os.WriteFile(cp, []byte("half"), 0o600)
	os.Chtimes(cp, future.Add(2*time.Minute), future.Add(2*time.Minute))
	certs.checked = time.Time{}
	got, _ = certs.get(nil)
	leaf, _ = x509.ParseCertificate(got.Certificate[0])
	if leaf.Subject.CommonName != "third" {
		t.Fatal("a half-written certificate replaced the good one")
	}
	if _, err := New(Options{}); err == nil {
		t.Fatal("no upstream accepted")
	}
	bad := mustLoad(t, "tls: {cert: /nonexistent.pem, key: /nonexistent.key}\nrules:\n  - id: a\n    allow: true\n")
	up, _ := url.Parse("http://127.0.0.1:1")
	g2, _ := New(Options{Config: bad, Upstream: up})
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	if _, err := g2.Listener(ln); err == nil || !strings.Contains(err.Error(), "could not be loaded") {
		t.Fatalf("missing certificate: %v", err)
	}
}

// An allowed upgrade becomes a plain tunnel; a denied one never reaches the
// app.
func TestUpgrades(t *testing.T) {
	ws := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "echo" {
			http.Error(w, "no", 400)
			return
		}
		c, brw, _ := w.(http.Hijacker).Hijack()
		defer c.Close()
		brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: echo\r\nConnection: Upgrade\r\n\r\n")
		brw.Flush()
		io.Copy(c, brw)
	}))
	defer ws.Close()
	up, _ := url.Parse(ws.URL)
	r := start(t, "rules:\n  - id: ws\n    when: {path: /ws}\n    allow: true\n    upgrade: [echo]\n  - id: plain\n    when: {path: /plain}\n    allow: true\n", func(o *Options) { o.Upstream = up })
	c, _ := net.Dial("tcp", r.addr)
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	c.Write([]byte("GET /ws HTTP/1.1\r\nHost: a\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n"))
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != 101 {
		t.Fatalf("%v %v", resp, err)
	}
	// Bytes that look like a smuggled request are just tunnel data now.
	msg := "POST /x HTTP/1.1\r\nContent-Length: 1\r\nTransfer-Encoding: chunked\r\n\r\n"
	c.Write([]byte(msg))
	got := make([]byte, len(msg))
	if _, err := io.ReadFull(br, got); err != nil || string(got) != msg {
		t.Fatalf("tunnel: %q %v", got, err)
	}
	if resp := r.raw(t, "GET /other HTTP/1.1\r\nHost: a\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n"); len(resps(resp)) != 1 || resp[0].StatusCode != 403 {
		t.Fatalf("denied upgrade: %v", resp)
	}
	// A rule that allows a path doesn't allow upgrades on it unless it says so.
	if resp := r.raw(t, "GET /plain HTTP/1.1\r\nHost: a\r\nConnection: Upgrade\r\nUpgrade: echo\r\n\r\n"); len(resp) != 1 || resp[0].StatusCode != 403 {
		t.Fatalf("upgrade on a plain rule: %v", resp)
	}
}

func resps(r []*http.Response) []*http.Response { return r }

func TestPathOptionsAndCredentialsInTheQuery(t *testing.T) {
	r := start(t, fmt.Sprintf(`
paths: {ignore_case: true, allow_semicolons: true}
credentials: {api_key_header: none, api_key_query: key, identity_header: none}
clients:
  - id: c
    api_key_sha256: %s
rules:
  - id: admin
    when: {path: /admin/**}
    deny: 403
  - id: keyed
    when: {path: /k/**}
    client: [c]
    allow: true
  - id: open
    when: {path: /**}
    allow: true
`, Hash("query-key-0123456789")))
	if resp := r.raw(t, "GET /ADMIN/users HTTP/1.1\r\nHost: a\r\n\r\n"); resp[0].StatusCode != 403 {
		t.Fatalf("ignore_case: %d", resp[0].StatusCode)
	}
	if resp := r.raw(t, "GET /x;jsessionid=1 HTTP/1.1\r\nHost: a\r\n\r\n"); resp[0].StatusCode != 200 || r.app.last().uri != "/x;jsessionid=1" {
		t.Fatalf("allow_semicolons: %d %s", resp[0].StatusCode, r.app.last().uri)
	}
	resp, _ := do(t, "GET", r.url+"/k/x?a=1&key=query-key-0123456789&b=%20", nil, nil)
	if resp.StatusCode != 200 || r.app.last().uri != "/k/x?a=1&b=%20" || r.app.last().header.Get("X-Aftermarket-Client") != "" {
		t.Fatalf("query key: %d %s %v", resp.StatusCode, r.app.last().uri, r.app.last().header)
	}
	if err := func() error {
		_, err := Load([]byte("paths: {ignore_case: true}\nrules:\n  - id: a\n    when: {path: /Admin}\n    deny: 403\n"), "")
		return err
	}(); err == nil || !strings.Contains(err.Error(), "lower case") {
		t.Fatalf("upper-case path with ignore_case: %v", err)
	}
}

func TestKeepCredentials(t *testing.T) {
	r := start(t, fmt.Sprintf(`
credentials: {keep: true}
clients:
  - id: c
    basic_user: u
    basic_password_sha256: %s
rules:
  - id: all
    when: {path: /**}
    client: [any]
    allow: true
`, Hash("basic-pass-0123456789")))
	req, _ := http.NewRequest("GET", r.url+"/x", nil)
	req.SetBasicAuth("u", "basic-pass-0123456789")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || r.app.last().header.Get("Authorization") == "" || r.app.last().header.Get("X-Aftermarket-Client") != "c" {
		t.Fatalf("%d %v", resp.StatusCode, r.app.last().header)
	}
}

// HTTP/1.0, HEAD and Expect: 100-continue all keep the tracker in step.
func TestOlderAndOddRequestForms(t *testing.T) {
	r := start(t, openConfig)
	if resp := r.raw(t, "GET /a HTTP/1.0\r\n\r\n"); len(resp) != 1 || resp[0].StatusCode != 200 {
		t.Fatalf("HTTP/1.0: %v", resp)
	}
	hc, _ := net.Dial("tcp", r.addr)
	hc.SetDeadline(time.Now().Add(3 * time.Second))
	hc.Write([]byte("HEAD /a HTTP/1.1\r\nHost: a\r\n\r\nGET /b HTTP/1.1\r\nHost: a\r\n\r\n"))
	hbr := bufio.NewReader(hc)
	first, err1 := http.ReadResponse(hbr, &http.Request{Method: "HEAD"})
	second, err2 := http.ReadResponse(hbr, nil)
	hc.Close()
	if err1 != nil || err2 != nil || first.StatusCode != 200 || second.StatusCode != 200 {
		t.Fatalf("HEAD then GET: %v %v %v %v", first, err1, second, err2)
	}
	c, _ := net.Dial("tcp", r.addr)
	defer c.Close()
	c.SetDeadline(time.Now().Add(3 * time.Second))
	c.Write([]byte("POST /c HTTP/1.1\r\nHost: a\r\nContent-Length: 5\r\nExpect: 100-continue\r\n\r\n"))
	br := bufio.NewReader(c)
	line, _ := br.ReadString('\n')
	if !strings.Contains(line, "100") {
		t.Fatalf("no 100 Continue: %q", line)
	}
	br.ReadString('\n')
	c.Write([]byte("hello" + "GET /admin/x HTTP/1.1\r\nHost: a\r\n\r\n"))
	// The app's own 100 Continue may be forwarded too; HTTP allows several.
	resp, err := http.ReadResponse(br, nil)
	for err == nil && resp.StatusCode == 100 {
		resp, err = http.ReadResponse(br, nil)
	}
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("after 100-continue: %v %v", resp, err)
	}
	io.Copy(io.Discard, resp.Body)
	resp, err = http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != 403 {
		t.Fatalf("the next request on the connection: %v %v", resp, err)
	}
}
