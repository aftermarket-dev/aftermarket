package guard

import (
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
)

// Server returns the HTTP server for g, with the timeouts and header limit
// from the config. HTTP/2 is not offered: every request goes through the
// HTTP/1 framing checks.
func (g *Guard) Server() *http.Server {
	c := g.cfg
	return &http.Server{
		Handler:           g,
		ReadHeaderTimeout: c.HeaderTimeout,
		ReadTimeout:       c.HeaderTimeout + c.BodyTimeout,
		IdleTimeout:       c.IdleTimeout,
		WriteTimeout:      c.HeaderTimeout + c.BodyTimeout + c.WriteTimeout,
		MaxHeaderBytes:    c.MaxHeaderBytes,
		// OPTIONS * goes to the handler, which rejects it, instead of being
		// answered by Go behind the tracker's back.
		DisableGeneralOptionsHandler: true,
		ConnContext:                  connContext,
		TLSNextProto:                 map[string]func(*http.Server, *tls.Conn, http.Handler){},
		ErrorLog:                     nil,
	}
}

// Listener wraps ln with the connection limits, TLS when the config has a
// certificate, and the framing tracker, in that order.
func (g *Guard) Listener(ln net.Listener) (net.Listener, error) {
	l := net.Listener(newListener(ln, g.cfg, func(addr string) {
		g.opts.Log.Write(Entry{Event: Refused, Address: addr, Reason: "over a connection limit"})
	}))
	if g.cfg.TLSCert != "" {
		certs, err := newCertFiles(g.cfg.TLSCert, g.cfg.TLSKey)
		if err != nil {
			return nil, err
		}
		l = tls.NewListener(l, &tls.Config{
			MinVersion:     tls.VersionTLS12,
			GetCertificate: certs.get,
			NextProtos:     []string{"http/1.1"},
		})
	}
	return trackListener{Listener: l, maxHead: g.cfg.MaxHeaderBytes}, nil
}

// certFiles loads a certificate and key, and loads them again when either
// file changes, so a renewed certificate is used without a restart.
type certFiles struct {
	cert, key string

	mu      sync.Mutex
	current *tls.Certificate
	stamp   [2]time.Time
	checked time.Time
}

func newCertFiles(cert, key string) (*certFiles, error) {
	c := &certFiles{cert: cert, key: key}
	if err := c.load(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *certFiles) load() error {
	stamp := c.stamps() // before reading, so a change during the read is seen next time
	pair, err := tls.LoadX509KeyPair(c.cert, c.key)
	if err != nil {
		return errors.New("the tls certificate and key could not be loaded: " + err.Error())
	}
	c.current = &pair
	c.stamp = stamp
	return nil
}

func (c *certFiles) stamps() [2]time.Time {
	var s [2]time.Time
	for i, p := range []string{c.cert, c.key} {
		if fi, err := os.Stat(p); err == nil {
			s[i] = fi.ModTime()
		}
	}
	return s
}

func (c *certFiles) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Since(c.checked) > 10*time.Second {
		c.checked = time.Now()
		if stamp := c.stamps(); stamp != c.stamp {
			// A half-copied pair fails to load; the old one stays in use.
			if pair, err := tls.LoadX509KeyPair(c.cert, c.key); err == nil {
				c.current = &pair
				c.stamp = stamp
			}
		}
	}
	return c.current, nil
}
