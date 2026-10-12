package server

import (
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"sync"
	"time"
)

// tlsReloadCheckInterval bounds how often the certificate files are stat'ed.
// Renewal tools replace the files in place; the new pair is picked up on the
// first handshake after this interval, without a restart.
const tlsReloadCheckInterval = 30 * time.Second

// certReloader serves a certificate/key pair from disk and reloads it when
// either file changes. A pair that fails to load leaves the current one in
// service, so a half-written renewal never takes the listener down.
type certReloader struct {
	certFile string
	keyFile  string

	mu        sync.RWMutex
	cert      *tls.Certificate
	certInfo  os.FileInfo
	keyInfo   os.FileInfo
	lastCheck time.Time
}

// newCertReloader loads the pair once and fails if it cannot, so a bad path
// or mismatched key is reported at startup rather than on the first handshake.
func newCertReloader(certFile, keyFile string) (*certReloader, error) {
	r := &certReloader{certFile: certFile, keyFile: keyFile}
	certInfo, keyInfo, err := r.stat()
	if err != nil {
		return nil, err
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load TLS certificate %s and key %s: %w", certFile, keyFile, err)
	}
	r.cert, r.certInfo, r.keyInfo, r.lastCheck = &cert, certInfo, keyInfo, time.Now()
	return r, nil
}

// stat follows symlinks, so a Kubernetes Secret volume, which swaps a
// directory symlink on update, reads as the files themselves changing.
func (r *certReloader) stat() (os.FileInfo, os.FileInfo, error) {
	certInfo, err := os.Stat(r.certFile)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read TLS certificate: %w", err)
	}
	keyInfo, err := os.Stat(r.keyFile)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read TLS key: %w", err)
	}
	return certInfo, keyInfo, nil
}

// fileChanged reports whether a file was rewritten or replaced. Replacement is
// checked as well as the mod time because tools that copy with timestamps
// preserved can install a new pair whose mod times match the old one.
func fileChanged(prev, cur os.FileInfo) bool {
	return !os.SameFile(prev, cur) || !prev.ModTime().Equal(cur.ModTime()) || prev.Size() != cur.Size()
}

// getCertificate implements tls.Config.GetCertificate.
func (r *certReloader) getCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mu.RLock()
	cert, due := r.cert, time.Since(r.lastCheck) >= tlsReloadCheckInterval
	r.mu.RUnlock()
	if due {
		cert = r.maybeReload()
	}
	return cert, nil
}

func (r *certReloader) maybeReload() *tls.Certificate {
	r.mu.Lock()
	defer r.mu.Unlock()
	if time.Since(r.lastCheck) < tlsReloadCheckInterval {
		return r.cert
	}
	r.lastCheck = time.Now()
	certInfo, keyInfo, err := r.stat()
	if err != nil {
		logger.Warn("keeping the current TLS certificate: %v", err)
		return r.cert
	}
	if !fileChanged(r.certInfo, certInfo) && !fileChanged(r.keyInfo, keyInfo) {
		return r.cert
	}
	cert, err := tls.LoadX509KeyPair(r.certFile, r.keyFile)
	if err != nil {
		// Commonly the moment between a renewal writing one file and the other;
		// the new file state is left unrecorded so the next check tries again.
		logger.Warn("keeping the current TLS certificate, the new pair did not load: %v", err)
		return r.cert
	}
	r.cert, r.certInfo, r.keyInfo = &cert, certInfo, keyInfo
	logger.Info("reloaded TLS certificate from %s", r.certFile)
	return r.cert
}

// tlsHandshakeTimeout bounds how long a client may take to complete the TLS
// handshake. The server sets no read or write timeouts, so that streams can run
// as long as they need; without this bound a client that never finishes the
// handshake would hold its connection, unauthenticated, indefinitely.
var tlsHandshakeTimeout = 10 * time.Second

// newTLSListener wraps ln so connections are served over TLS with the pair in
// certFile and keyFile, reloaded when the files change. HTTP/1.1 only, which is
// what the fasthttp server speaks.
func newTLSListener(ln net.Listener, certFile, keyFile string) (net.Listener, error) {
	reloader, err := newCertReloader(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	return &tlsListener{Listener: ln, config: &tls.Config{
		MinVersion:     tls.VersionTLS12,
		NextProtos:     []string{"http/1.1"},
		GetCertificate: reloader.getCertificate,
	}}, nil
}

type tlsListener struct {
	net.Listener
	config *tls.Config
}

// Accept returns the connection without handshaking, like tls.NewListener, so
// a slow client never holds up the accept loop. The handshake runs on first use.
func (l *tlsListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &handshakeDeadlineConn{Conn: tls.Server(c, l.config), timeout: tlsHandshakeTimeout}, nil
}

// handshakeDeadlineConn completes the TLS handshake under a deadline on first
// use, then clears the deadline so the connection itself has no time limit.
type handshakeDeadlineConn struct {
	*tls.Conn
	timeout time.Duration
	once    sync.Once
	err     error
}

// Handshake also serves callers that run the handshake themselves; fasthttp's
// IsTLS detects TLS connections by this method and ConnectionState.
func (c *handshakeDeadlineConn) Handshake() error {
	c.once.Do(func() {
		if c.err = c.Conn.SetDeadline(time.Now().Add(c.timeout)); c.err != nil {
			return
		}
		if c.err = c.Conn.Handshake(); c.err != nil {
			return
		}
		c.err = c.Conn.SetDeadline(time.Time{})
	})
	return c.err
}

func (c *handshakeDeadlineConn) Read(b []byte) (int, error) {
	if err := c.Handshake(); err != nil {
		return 0, err
	}
	return c.Conn.Read(b)
}

func (c *handshakeDeadlineConn) Write(b []byte) (int, error) {
	if err := c.Handshake(); err != nil {
		return 0, err
	}
	return c.Conn.Write(b)
}

type serverListener struct {
	ln     net.Listener
	scheme string
	addr   string
}

// listen opens the server's listeners: plain HTTP on Port, or HTTPS on Port
// when a certificate is configured, plus HTTPS on TLSPort when that is set.
func (s *BifrostHTTPServer) listen() ([]serverListener, error) {
	tlsEnabled := s.TLSCertFile != "" || s.TLSKeyFile != ""
	if tlsEnabled && (s.TLSCertFile == "" || s.TLSKeyFile == "") {
		return nil, fmt.Errorf("both a TLS certificate and key are required, got certificate %q and key %q", s.TLSCertFile, s.TLSKeyFile)
	}
	if !tlsEnabled && s.TLSPort != "" {
		return nil, fmt.Errorf("a TLS port was set without a TLS certificate and key")
	}
	type spec struct {
		port   string
		secure bool
	}
	specs := []spec{{port: s.Port, secure: tlsEnabled && s.TLSPort == ""}}
	if tlsEnabled && s.TLSPort != "" {
		specs = append(specs, spec{port: s.TLSPort, secure: true})
	}
	var listeners []serverListener
	closeAll := func() {
		for _, l := range listeners {
			_ = l.ln.Close()
		}
	}
	for _, sp := range specs {
		addr := net.JoinHostPort(s.Host, sp.port)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			closeAll()
			return nil, fmt.Errorf("failed to create listener on %s: %v", addr, err)
		}
		scheme := "http"
		if sp.secure {
			tlsLn, err := newTLSListener(ln, s.TLSCertFile, s.TLSKeyFile)
			if err != nil {
				_ = ln.Close()
				closeAll()
				return nil, err
			}
			ln, scheme = tlsLn, "https"
		}
		listeners = append(listeners, serverListener{ln: ln, scheme: scheme, addr: addr})
	}
	return listeners, nil
}
