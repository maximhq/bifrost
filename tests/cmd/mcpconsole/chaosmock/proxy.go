package chaosmock

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ProxyDomain is the name suffix the forward proxy resolves. A host under it reaches the fleet on
// loopback at the same port, so a client that cannot resolve it itself only gets there through the
// proxy, as in a proxy-only egress network.
const ProxyDomain = ".proxy.test"

// maxProxyLog bounds the request log; the oldest entries are dropped first.
const maxProxyLog = 2000

// ProxyLogEntry is one request the proxy handled. Path is empty for a CONNECT tunnel; Status 0 means
// the proxy never answered (stall).
type ProxyLogEntry struct {
	Method string `json:"method"`
	Host   string `json:"host"`
	Path   string `json:"path,omitempty"`
	Status int    `json:"status"`
}

// ProxyMode is the proxy's controllable behaviour, modelled on what a corporate firewall or proxy does.
type ProxyMode struct {
	Down          bool     `json:"down"`                      // close every connection, like an unreachable proxy
	Auth          string   `json:"auth,omitempty"`            // "user:pass" required as Basic Proxy-Authorization
	Deny          []string `json:"deny,omitempty"`            // hosts refused with 403, as a proxy's policy does
	Stall         []string `json:"stall,omitempty"`           // hosts never answered, like dropped traffic
	Intercept     bool     `json:"intercept,omitempty"`       // decrypt CONNECT with a certificate from the proxy's CA
	IdleTimeoutMs int      `json:"idle_timeout_ms,omitempty"` // cut client connections quiet this long
}

// forwardProxy is an HTTP forward proxy: absolute-form requests for plain http and CONNECT tunnels.
type forwardProxy struct {
	mu    sync.Mutex
	url   string
	mode  ProxyMode
	log   []ProxyLogEntry
	rp    *httputil.ReverseProxy
	conns map[net.Conn]struct{} // open client connections, so going down or an idle cut can close them

	caPEM  string
	caCert *x509.Certificate
	caKey  *ecdsa.PrivateKey
	leaves map[string]*tls.Certificate
}

func newForwardProxy() (*forwardProxy, error) {
	p := &forwardProxy{conns: map[net.Conn]struct{}{}, leaves: map[string]*tls.Certificate{}}
	p.rp = &httputil.ReverseProxy{
		// The Host header is kept, so the fleet builds its URLs on the proxied name.
		Rewrite: func(pr *httputil.ProxyRequest) {
			host := pr.In.URL.Host
			if host == "" {
				host = pr.In.Host
			}
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = loopbackAddr(host)
			pr.Out.Host = pr.In.Host
			pr.Out.Header.Del("Proxy-Authorization")
			if pr.In.TLS != nil {
				pr.Out.Header.Set("X-Forwarded-Proto", "https")
			}
		},
		FlushInterval: -1,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			http.Error(w, "proxy: "+err.Error(), http.StatusBadGateway)
		},
	}
	if err := p.newCA(); err != nil {
		return nil, err
	}
	return p, nil
}

// ServeProxy starts the forward proxy on addr and returns its URL and a shutdown function.
func (f *Fleet) ServeProxy(addr string) (string, func(context.Context) error, error) {
	p, err := newForwardProxy()
	if err != nil {
		return "", nil, err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", nil, err
	}
	p.url = "http://" + ln.Addr().String()
	srv := &http.Server{Handler: p, ReadHeaderTimeout: 10 * time.Second, ConnState: p.track}
	go func() {
		if err := srv.Serve(&proxyListener{Listener: ln, proxy: p}); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("chaosmock: proxy serve stopped: %v", err)
		}
	}()
	stopIdle := make(chan struct{})
	go p.cutIdle(stopIdle)
	f.mu.Lock()
	f.proxy = p
	f.mu.Unlock()
	return p.url, func(ctx context.Context) error {
		close(stopIdle)
		p.closeAll()
		return srv.Shutdown(ctx)
	}, nil
}

// proxyState returns the proxy's URL, CA, mode and log for GET /control, or nil when none is running.
func (f *Fleet) proxyState() map[string]any {
	f.mu.RLock()
	p := f.proxy
	f.mu.RUnlock()
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	entries := append([]ProxyLogEntry{}, p.log...)
	return map[string]any{"url": p.url, "ca_pem": p.caPEM, "mode": p.mode, "log": entries}
}

// setProxy applies a mode and, when clearLog is set, empties the log. Any change of mode cuts the open
// connections, so no kept-alive or intercepted one keeps the old behaviour.
func (f *Fleet) setProxy(mode ProxyMode, clearLog bool) error {
	f.mu.RLock()
	p := f.proxy
	f.mu.RUnlock()
	if p == nil {
		return errors.New("no proxy is running: start mcpchaos with -proxy-addr")
	}
	p.mu.Lock()
	changed := !reflect.DeepEqual(p.mode, mode)
	p.mode = mode
	if clearLog {
		p.log = nil
	}
	p.mu.Unlock()
	if changed {
		p.closeAll()
	}
	return nil
}

func (p *forwardProxy) closeAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for c := range p.conns {
		c.Close()
	}
}

// track keeps the open client connections, hijacked tunnels included.
func (p *forwardProxy) track(c net.Conn, state http.ConnState) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch state {
	case http.StateNew:
		p.conns[c] = struct{}{}
	case http.StateClosed:
		delete(p.conns, c)
	}
}

// cutIdle closes client connections that have been quiet longer than the mode's idle timeout.
func (p *forwardProxy) cutIdle(stop <-chan struct{}) {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		p.mu.Lock()
		idle := time.Duration(p.mode.IdleTimeoutMs) * time.Millisecond
		if idle > 0 {
			now := time.Now().UnixNano()
			for c := range p.conns {
				if ac, ok := c.(*activityConn); ok && now-ac.last.Load() > int64(idle) {
					c.Close()
				}
			}
		}
		p.mu.Unlock()
	}
}

func (p *forwardProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	mode := p.mode
	p.mu.Unlock()
	host := strings.ToLower(hostOnly(r.URL.Host, r.Host))
	if mode.Auth != "" && r.Header.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte(mode.Auth)) {
		w.Header().Set("Proxy-Authenticate", `Basic realm="mcpchaos"`)
		p.record(r, http.StatusProxyAuthRequired)
		http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
		return
	}
	if !strings.HasSuffix(host, ProxyDomain) {
		p.record(r, http.StatusBadGateway)
		http.Error(w, "proxy: cannot resolve "+host, http.StatusBadGateway)
		return
	}
	if slices.Contains(mode.Deny, host) {
		p.record(r, http.StatusForbidden)
		http.Error(w, "proxy: "+host+" is blocked by policy", http.StatusForbidden)
		return
	}
	if slices.Contains(mode.Stall, host) {
		// Never answered: the client sees only its own timeout, as with dropped traffic.
		p.record(r, 0)
		// Held until the client gives up and closes it, reading nothing back.
		if conn := p.hijack(w); conn != nil {
			_ = conn.SetReadDeadline(time.Now().Add(2 * time.Minute))
			_, _ = io.Copy(io.Discard, conn)
			p.release(conn)
		}
		return
	}
	if r.Method == http.MethodConnect {
		if mode.Intercept {
			p.intercept(w, r)
		} else {
			p.tunnel(w, r)
		}
		return
	}
	if !r.URL.IsAbs() {
		p.record(r, http.StatusBadRequest)
		http.Error(w, "proxy: expected an absolute-form request", http.StatusBadRequest)
		return
	}
	p.forward(w, r)
}

// forward relays one request to the fleet, logging it when its headers go out, so a stream that is
// still open is already in the log.
func (p *forwardProxy) forward(w http.ResponseWriter, r *http.Request) {
	rec := &statusRecorder{ResponseWriter: w, onHeader: func(code int) { p.record(r, code) }}
	p.rp.ServeHTTP(rec, r)
	rec.flushHeader(http.StatusOK)
}

// tunnel serves CONNECT by splicing the client to the fleet listener at the requested port.
func (p *forwardProxy) tunnel(w http.ResponseWriter, r *http.Request) {
	upstream, err := net.DialTimeout("tcp", loopbackAddr(r.Host), 5*time.Second)
	if err != nil {
		p.record(r, http.StatusBadGateway)
		http.Error(w, "proxy: "+err.Error(), http.StatusBadGateway)
		return
	}
	conn := p.hijack(w)
	if conn == nil {
		upstream.Close()
		return
	}
	p.record(r, http.StatusOK)
	_, _ = conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	go func() { _, _ = io.Copy(upstream, conn); upstream.Close() }()
	_, _ = io.Copy(conn, upstream)
	p.release(conn)
}

// intercept serves CONNECT the way a TLS-inspecting proxy does: it terminates TLS with a certificate
// for the requested host signed by its own CA, then relays the decrypted requests to the fleet.
func (p *forwardProxy) intercept(w http.ResponseWriter, r *http.Request) {
	conn := p.hijack(w)
	if conn == nil {
		return
	}
	p.record(r, http.StatusOK)
	_, _ = conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	tlsConn := tls.Server(conn, &tls.Config{GetCertificate: p.leafFor})
	inner := &oneConnListener{conn: tlsConn, done: make(chan struct{})}
	srv := &http.Server{
		Handler: http.HandlerFunc(p.forward),
		ConnState: func(_ net.Conn, s http.ConnState) {
			if s == http.StateClosed || s == http.StateHijacked {
				inner.close()
			}
		},
		ErrorLog: log.New(io.Discard, "", 0),
	}
	_ = srv.Serve(inner)
	p.release(conn)
}

func (p *forwardProxy) hijack(w http.ResponseWriter) net.Conn {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "proxy: hijacking not supported", http.StatusInternalServerError)
		return nil
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		return nil
	}
	return conn
}

// release closes a hijacked connection and stops tracking it.
func (p *forwardProxy) release(conn net.Conn) {
	conn.Close()
	p.mu.Lock()
	delete(p.conns, conn)
	p.mu.Unlock()
}

func (p *forwardProxy) record(r *http.Request, status int) {
	entry := ProxyLogEntry{Method: r.Method, Host: hostOnly(r.URL.Host, r.Host), Status: status}
	if r.Method != http.MethodConnect {
		entry.Path = r.URL.Path
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.log = append(p.log, entry)
	if len(p.log) > maxProxyLog {
		p.log = p.log[len(p.log)-maxProxyLog:]
	}
}

// newCA makes the throwaway CA a TLS-inspecting proxy signs its certificates with.
func (p *forwardProxy) newCA() error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "mcpchaos inspecting proxy CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return err
	}
	p.caCert, p.caKey = cert, key
	p.caPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	return nil
}

// leafFor issues, once per host, a certificate for the name the client asked for, signed by the CA.
func (p *forwardProxy) leafFor(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	name := hello.ServerName
	if name == "" {
		name = "localhost"
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.leaves[name]; ok {
		return c, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		DNSNames:     []string{name},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, p.caCert, &key.PublicKey, p.caKey)
	if err != nil {
		return nil, err
	}
	c := &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	p.leaves[name] = c
	return c, nil
}

// loopbackAddr keeps hostport's port and swaps its host for 127.0.0.1.
func loopbackAddr(hostport string) string {
	_, port, err := net.SplitHostPort(hostport)
	if err != nil {
		port = "80"
	}
	return net.JoinHostPort("127.0.0.1", port)
}

// hostOnly returns the host of the request target, without its port.
func hostOnly(urlHost, hostHeader string) string {
	h := urlHost
	if h == "" {
		h = hostHeader
	}
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}

// proxyListener closes each accepted connection while the proxy is down, and wraps the rest so an
// idle cut can see when they last carried data.
type proxyListener struct {
	net.Listener
	proxy *forwardProxy
}

func (l *proxyListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		l.proxy.mu.Lock()
		down := l.proxy.mode.Down
		l.proxy.mu.Unlock()
		if !down {
			ac := &activityConn{Conn: conn}
			ac.touch()
			return ac, nil
		}
		conn.Close()
	}
}

// activityConn records when it last read or wrote.
type activityConn struct {
	net.Conn
	last atomic.Int64
}

func (c *activityConn) touch() { c.last.Store(time.Now().UnixNano()) }

func (c *activityConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.touch()
	}
	return n, err
}

func (c *activityConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	if n > 0 {
		c.touch()
	}
	return n, err
}

// oneConnListener serves a single connection, then blocks until it closes.
type oneConnListener struct {
	conn net.Conn
	once sync.Once
	shut sync.Once
	done chan struct{}
}

func (l *oneConnListener) Accept() (net.Conn, error) {
	var c net.Conn
	l.once.Do(func() { c = l.conn })
	if c != nil {
		return c, nil
	}
	<-l.done
	return nil, net.ErrClosed
}

func (l *oneConnListener) close()         { l.shut.Do(func() { close(l.done) }) }
func (l *oneConnListener) Close() error   { l.close(); return nil }
func (l *oneConnListener) Addr() net.Addr { return l.conn.LocalAddr() }

// statusRecorder reports the status once, when the headers are written, and passes flushes through for streams.
type statusRecorder struct {
	http.ResponseWriter
	onHeader func(int)
	done     bool
}

func (s *statusRecorder) flushHeader(code int) {
	if !s.done {
		s.done = true
		s.onHeader(code)
	}
}

func (s *statusRecorder) WriteHeader(code int) {
	s.flushHeader(code)
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.flushHeader(http.StatusOK)
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
