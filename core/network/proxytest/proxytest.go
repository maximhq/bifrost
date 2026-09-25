// Package proxytest holds the shared scaffolding for the proxy routing matrices: recording
// HTTP and SOCKS5 proxies, a tunnelling forward proxy, and the spec of every proxy source,
// proxy env state and target the matrices cross.
//
// Each provider HTTP stack has its own matrix test in its own package (fasthttp inference,
// auth and fetch in core/providers/utils, Bedrock's net/http client in
// core/providers/bedrock, the realtime WebSocket dialer in core/providers/utils). They all
// send real requests through a Set of recorders and assert the outcome with AssertRoute,
// so a stack that picks the wrong proxy, leaks or drops credentials, or falls back to a
// direct connection when its proxy refuses it fails the same way everywhere.
//
// It lives outside internal/ so bifrost-enterprise can reuse it for its own outbound
// clients (identity providers, guardrails, plugins). It is test scaffolding: only test
// code imports it.
package proxytest

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// Proxy credentials used by the credential rows. A SOCKS5 recorder refuses
// RejectedPass, so a row using it proves a refused proxy fails the request.
const (
	User         = "svc"
	Pass         = "s3cret"
	RejectedPass = "rejected"
)

// BasicAuth is the Proxy-Authorization an HTTP proxy must receive for User:Pass.
var BasicAuth = "Basic " + base64.StdEncoding.EncodeToString([]byte(User+":"+Pass))

// SOCKSAuth is how a SOCKS5 recorder reports an RFC 1929 username/password login.
func SOCKSAuth(user, pass string) string { return "socks5 " + user + ":" + pass }

// Hit is one request a recorder received.
type Hit struct {
	Target string // host:port; "" when a SOCKS5 login was refused before CONNECT
	Auth   string // Proxy-Authorization, or SOCKSAuth for a SOCKS5 login; "" for none
}

// Recorder answers as an HTTP forward proxy or a SOCKS5 proxy without forwarding
// anything, and records each request.
type Recorder struct {
	Name     string
	listener net.Listener
	mu       sync.Mutex
	hits     []Hit
}

func (r *Recorder) record(hit Hit) {
	r.mu.Lock()
	r.hits = append(r.hits, hit)
	r.mu.Unlock()
}

// Seen returns the requests recorded since the last Reset.
func (r *Recorder) Seen() []Hit {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Hit(nil), r.hits...)
}

// Reset forgets every recorded request.
func (r *Recorder) Reset() {
	r.mu.Lock()
	r.hits = nil
	r.mu.Unlock()
}

// Port is the port the recorder listens on.
func (r *Recorder) Port() string {
	_, port, _ := net.SplitHostPort(r.listener.Addr().String())
	return port
}

// NewHTTPRecorder listens on network ("tcp4" or "tcp6") loopback. CONNECT gets a 200 and
// a tunnel that answers one plain HTTP request with an empty 200 (see answerOneRequest);
// an absolute-URI request gets an empty 200.
func NewHTTPRecorder(t *testing.T, name, network string) *Recorder {
	t.Helper()
	addr := "127.0.0.1:0"
	if network == "tcp6" {
		addr = "[::1]:0"
	}
	listener, err := net.Listen(network, addr)
	if err != nil {
		t.Fatalf("%s: listen %s: %v", name, network, err)
	}
	r := &Recorder{Name: name, listener: listener}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		auth := req.Header.Get("Proxy-Authorization")
		if req.Method == http.MethodConnect {
			r.record(Hit{Target: req.Host, Auth: auth})
			if conn, buffered, err := w.(http.Hijacker).Hijack(); err == nil {
				_, _ = conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
				answerOneRequest(conn, buffered.Reader)
				conn.Close()
			}
			return
		}
		host := req.URL.Host
		if req.URL.Port() == "" {
			host = net.JoinHostPort(req.URL.Hostname(), "80")
		}
		r.record(Hit{Target: host, Auth: auth})
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return r
}

// NewSOCKS5Recorder answers SOCKS5 (RFC 1928). A client offering username/password
// (RFC 1929) is asked to log in; the login is recorded and refused when the password is
// RejectedPass. A CONNECT is recorded and reported successful, then the connection closes.
func NewSOCKS5Recorder(t *testing.T, name string) *Recorder {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("%s: listen: %v", name, err)
	}
	r := &Recorder{Name: name, listener: listener}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				serveSOCKS5(conn, r.record)
			}(conn)
		}
	}()
	t.Cleanup(func() { _ = listener.Close() })
	return r
}

// serveSOCKS5 runs one SOCKS5 exchange. It records what the client asked for before
// sending the reply that ends the exchange, so a test that checks the recorder as soon
// as the client returns always finds the hit.
func serveSOCKS5(conn net.Conn, record func(Hit)) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(conn, header); err != nil || header[0] != 5 {
		return
	}
	methods := make([]byte, header[1])
	if _, err := io.ReadFull(conn, methods); err != nil {
		return
	}
	var hit Hit
	if bytes.IndexByte(methods, 2) >= 0 {
		if _, err := conn.Write([]byte{5, 2}); err != nil {
			return
		}
		user, pass, ok := readUserPass(conn)
		if !ok {
			return
		}
		hit.Auth = SOCKSAuth(user, pass)
		if pass == RejectedPass {
			record(hit)
			_, _ = conn.Write([]byte{1, 1})
			return
		}
		if _, err := conn.Write([]byte{1, 0}); err != nil {
			return
		}
	} else if _, err := conn.Write([]byte{5, 0}); err != nil {
		return
	}

	request := make([]byte, 4)
	if _, err := io.ReadFull(conn, request); err != nil || request[1] != 1 {
		return
	}
	var host string
	switch request[3] {
	case 1, 4:
		size := 4
		if request[3] == 4 {
			size = 16
		}
		ip := make([]byte, size)
		if _, err := io.ReadFull(conn, ip); err != nil {
			return
		}
		host = net.IP(ip).String()
	case 3:
		size := make([]byte, 1)
		if _, err := io.ReadFull(conn, size); err != nil {
			return
		}
		name := make([]byte, size[0])
		if _, err := io.ReadFull(conn, name); err != nil {
			return
		}
		host = string(name)
	default:
		return
	}
	port := make([]byte, 2)
	if _, err := io.ReadFull(conn, port); err != nil {
		return
	}
	hit.Target = net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port))))
	record(hit)
	_, _ = conn.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
	answerOneRequest(conn, conn)
}

// answerOneRequest answers one plain HTTP request arriving through an open tunnel with an
// empty 200, the way a working upstream would. Closing the tunnel instead would make a
// client that retries on a closed connection (fasthttp's stale-connection policy) dial
// again and show up as several hits for one request. A TLS client sends a handshake
// instead, which does not parse as a request, so its tunnel just closes.
func answerOneRequest(conn net.Conn, reader io.Reader) {
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	request, err := http.ReadRequest(bufio.NewReader(reader))
	if err != nil {
		return
	}
	request.Body.Close()
	_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"))
}

// readUserPass reads an RFC 1929 username/password request.
func readUserPass(conn net.Conn) (string, string, bool) {
	version := make([]byte, 2)
	if _, err := io.ReadFull(conn, version); err != nil || version[0] != 1 {
		return "", "", false
	}
	user := make([]byte, version[1])
	if _, err := io.ReadFull(conn, user); err != nil {
		return "", "", false
	}
	size := make([]byte, 1)
	if _, err := io.ReadFull(conn, size); err != nil {
		return "", "", false
	}
	pass := make([]byte, size[0])
	if _, err := io.ReadFull(conn, pass); err != nil {
		return "", "", false
	}
	return string(user), string(pass), true
}

// Set is the recorders one matrix run routes through.
type Set struct {
	Config    *Recorder // named by proxy_config (http, IP or hostname URL)
	Config6   *Recorder // named by proxy_config (http, IPv6 literal URL)
	Socks     *Recorder // named by proxy_config or an env var (socks5)
	EnvHTTPS  *Recorder // HTTPS_PROXY / https_proxy
	EnvHTTPS6 *Recorder // HTTPS_PROXY as an IPv6 literal
	EnvHTTP   *Recorder // HTTP_PROXY / http_proxy
	All       []*Recorder
}

// NewSet starts every recorder a matrix needs.
func NewSet(t *testing.T) *Set {
	s := &Set{
		Config:    NewHTTPRecorder(t, "config", "tcp4"),
		Config6:   NewHTTPRecorder(t, "config6", "tcp6"),
		Socks:     NewSOCKS5Recorder(t, "socks"),
		EnvHTTPS:  NewHTTPRecorder(t, "env-https", "tcp4"),
		EnvHTTPS6: NewHTTPRecorder(t, "env-https6", "tcp6"),
		EnvHTTP:   NewHTTPRecorder(t, "env-http", "tcp4"),
	}
	s.All = []*Recorder{s.Config, s.Config6, s.Socks, s.EnvHTTPS, s.EnvHTTPS6, s.EnvHTTP}
	return s
}

// ByName returns the recorder with the given name, or nil.
func (s *Set) ByName(name string) *Recorder {
	for _, r := range s.All {
		if r.Name == name {
			return r
		}
	}
	return nil
}

// Reset forgets every recorder's requests.
func (s *Set) Reset() {
	for _, r := range s.All {
		r.Reset()
	}
}

// Route is where a request must go.
type Route struct {
	Proxy    string // recorder name; "" for a direct connection
	Auth     string // Hit.Auth the recorder must see
	MustFail bool   // the proxy refuses the login: the request must fail, not go direct
}

// Direct is the route of a request that must not touch any proxy.
var Direct = Route{}

// Source is where a provider's proxy comes from.
type Source struct {
	Name   string
	Config func(s *Set) *schemas.ProxyConfig
	// Route is fixed for a proxy_config that names a proxy; nil when the route depends
	// on the environment.
	Route *Route
}

// Sources are every proxy_config shape the matrices cross.
var Sources = []Source{
	{Name: "unset", Config: func(*Set) *schemas.ProxyConfig { return nil }},
	{Name: "none", Config: func(*Set) *schemas.ProxyConfig { return &schemas.ProxyConfig{Type: schemas.NoProxy} }},
	{Name: "environment", Config: func(*Set) *schemas.ProxyConfig { return &schemas.ProxyConfig{Type: schemas.EnvProxy} }},
	{Name: "http-ip", Route: &Route{Proxy: "config"}, Config: func(s *Set) *schemas.ProxyConfig {
		return &schemas.ProxyConfig{Type: schemas.HTTPProxy, URL: schemas.NewSecretVar("http://127.0.0.1:" + s.Config.Port())}
	}},
	{Name: "http-hostname", Route: &Route{Proxy: "config"}, Config: func(s *Set) *schemas.ProxyConfig {
		return &schemas.ProxyConfig{Type: schemas.HTTPProxy, URL: schemas.NewSecretVar("http://localhost:" + s.Config.Port())}
	}},
	{Name: "http-ipv6", Route: &Route{Proxy: "config6"}, Config: func(s *Set) *schemas.ProxyConfig {
		return &schemas.ProxyConfig{Type: schemas.HTTPProxy, URL: schemas.NewSecretVar("http://[::1]:" + s.Config6.Port())}
	}},
	{Name: "http-credentials", Route: &Route{Proxy: "config", Auth: BasicAuth}, Config: func(s *Set) *schemas.ProxyConfig {
		return &schemas.ProxyConfig{
			Type:     schemas.HTTPProxy,
			URL:      schemas.NewSecretVar("http://127.0.0.1:" + s.Config.Port()),
			Username: schemas.NewSecretVar(User),
			Password: schemas.NewSecretVar(Pass),
		}
	}},
	{Name: "socks5", Route: &Route{Proxy: "socks"}, Config: func(s *Set) *schemas.ProxyConfig {
		return &schemas.ProxyConfig{Type: schemas.Socks5Proxy, URL: schemas.NewSecretVar("socks5://127.0.0.1:" + s.Socks.Port())}
	}},
	{Name: "socks5-credentials", Route: &Route{Proxy: "socks", Auth: SOCKSAuth(User, Pass)}, Config: func(s *Set) *schemas.ProxyConfig {
		return &schemas.ProxyConfig{
			Type:     schemas.Socks5Proxy,
			URL:      schemas.NewSecretVar("socks5://127.0.0.1:" + s.Socks.Port()),
			Username: schemas.NewSecretVar(User),
			Password: schemas.NewSecretVar(Pass),
		}
	}},
	{Name: "socks5-rejected", Route: &Route{Proxy: "socks", Auth: SOCKSAuth(User, RejectedPass), MustFail: true}, Config: func(s *Set) *schemas.ProxyConfig {
		return &schemas.ProxyConfig{
			Type:     schemas.Socks5Proxy,
			URL:      schemas.NewSecretVar("socks5://127.0.0.1:" + s.Socks.Port()),
			Username: schemas.NewSecretVar(User),
			Password: schemas.NewSecretVar(RejectedPass),
		}
	}},
	// The shape an inherited global proxy takes: a proxy plus the global no_proxy list,
	// here naming the target, so every stack must connect directly.
	{Name: "http+no_proxy", Route: &Direct, Config: func(s *Set) *schemas.ProxyConfig {
		return &schemas.ProxyConfig{
			Type:    schemas.HTTPProxy,
			URL:     schemas.NewSecretVar("http://127.0.0.1:" + s.Config.Port()),
			NoProxy: TargetHost + "," + FetchTargetHost,
		}
	}},
}

// Env is one state of the proxy environment variables.
//
// A proxy variable's value names a recorder, spelled as the variable should spell its
// URL: "env-https" (http://127.0.0.1:port), "env-https6" (http://[::1]:port),
// "env-https@localhost" (http://localhost:port), "socks" (socks5://127.0.0.1:port),
// "creds@env-https" or "creds@socks" (User:Pass in the URL), "rejected@socks"
// (User:RejectedPass). NO_PROXY values are literal, except "target", which stands for
// the request's target host.
type Env struct {
	Name string
	Vars map[string]string
}

// Envs are every proxy env state the matrices cross.
var Envs = []Env{
	{Name: "no-env", Vars: map[string]string{}},
	{Name: "HTTP_PROXY", Vars: map[string]string{"HTTP_PROXY": "env-http"}},
	{Name: "HTTPS_PROXY", Vars: map[string]string{"HTTPS_PROXY": "env-https"}},
	{Name: "both", Vars: map[string]string{"HTTPS_PROXY": "env-https", "HTTP_PROXY": "env-http"}},
	{Name: "both+NO_PROXY", Vars: map[string]string{"HTTPS_PROXY": "env-https", "HTTP_PROXY": "env-http", "NO_PROXY": "target"}},
	{Name: "HTTPS_PROXY+NO_PROXY", Vars: map[string]string{"HTTPS_PROXY": "env-https", "NO_PROXY": "target"}},
	{Name: "HTTP_PROXY+NO_PROXY", Vars: map[string]string{"HTTP_PROXY": "env-http", "NO_PROXY": "target"}},
	{Name: "both+NO_PROXY-other-host", Vars: map[string]string{"HTTPS_PROXY": "env-https", "HTTP_PROXY": "env-http", "NO_PROXY": "other.example"}},
	{Name: "both+NO_PROXY-wildcard", Vars: map[string]string{"HTTPS_PROXY": "env-https", "HTTP_PROXY": "env-http", "NO_PROXY": "*"}},
	{Name: "both+no_proxy-lowercase", Vars: map[string]string{"HTTPS_PROXY": "env-https", "HTTP_PROXY": "env-http", "no_proxy": "target"}},
	{Name: "https_proxy-lowercase", Vars: map[string]string{"https_proxy": "env-https"}},
	{Name: "http_proxy-lowercase", Vars: map[string]string{"http_proxy": "env-http"}},
	// Lowercase wins when both spellings are set (golang.org/x/net/http/httpproxy; the
	// copy vendored in Go's net/http prefers uppercase).
	{Name: "https_proxy-over-HTTPS_PROXY", Vars: map[string]string{"HTTPS_PROXY": "env-http", "https_proxy": "env-https"}},
	{Name: "no_proxy-over-NO_PROXY", Vars: map[string]string{"HTTPS_PROXY": "env-https", "HTTP_PROXY": "env-http", "NO_PROXY": "other.example", "no_proxy": "target"}},
	// Go's httpproxy does not read ALL_PROXY, so it must change nothing.
	{Name: "ALL_PROXY-ignored", Vars: map[string]string{"ALL_PROXY": "env-https"}},
	{Name: "HTTPS_PROXY-ipv6", Vars: map[string]string{"HTTPS_PROXY": "env-https6"}},
	{Name: "HTTPS_PROXY-hostname", Vars: map[string]string{"HTTPS_PROXY": "env-https@localhost"}},
	{Name: "HTTPS_PROXY-credentials", Vars: map[string]string{"HTTPS_PROXY": "creds@env-https"}},
	{Name: "HTTPS_PROXY-socks5", Vars: map[string]string{"HTTPS_PROXY": "socks"}},
	{Name: "HTTPS_PROXY-socks5-credentials", Vars: map[string]string{"HTTPS_PROXY": "creds@socks"}},
	{Name: "HTTPS_PROXY-socks5-rejected", Vars: map[string]string{"HTTPS_PROXY": "rejected@socks"}},
}

// EnvNames is every variable SetEnv controls.
var EnvNames = []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy", "ALL_PROXY", "all_proxy", "REQUEST_METHOD"}

// EnvURL turns an env proxy spec into the URL to set and the route it means.
func EnvURL(s *Set, spec string) (string, Route) {
	switch {
	case spec == "socks":
		return "socks5://127.0.0.1:" + s.Socks.Port(), Route{Proxy: "socks"}
	case spec == "creds@socks":
		return "socks5://" + User + ":" + Pass + "@127.0.0.1:" + s.Socks.Port(), Route{Proxy: "socks", Auth: SOCKSAuth(User, Pass)}
	case spec == "rejected@socks":
		return "socks5://" + User + ":" + RejectedPass + "@127.0.0.1:" + s.Socks.Port(), Route{Proxy: "socks", Auth: SOCKSAuth(User, RejectedPass), MustFail: true}
	case spec == "env-https6":
		return "http://[::1]:" + s.EnvHTTPS6.Port(), Route{Proxy: "env-https6"}
	case strings.HasSuffix(spec, "@localhost"):
		name := strings.TrimSuffix(spec, "@localhost")
		return "http://localhost:" + s.ByName(name).Port(), Route{Proxy: name}
	case strings.HasPrefix(spec, "creds@"):
		name := strings.TrimPrefix(spec, "creds@")
		return "http://" + User + ":" + Pass + "@127.0.0.1:" + s.ByName(name).Port(), Route{Proxy: name, Auth: BasicAuth}
	default:
		return "http://127.0.0.1:" + s.ByName(spec).Port(), Route{Proxy: spec}
	}
}

// SetEnv clears every variable httpproxy reads, then applies env. targetHost replaces a
// NO_PROXY value of "target".
func SetEnv(t *testing.T, s *Set, env Env, targetHost string) {
	t.Helper()
	for _, name := range EnvNames {
		t.Setenv(name, "")
	}
	for name, value := range env.Vars {
		if strings.EqualFold(name, "NO_PROXY") {
			if value == "target" {
				value = targetHost
			}
			t.Setenv(name, value)
			continue
		}
		proxyURL, _ := EnvURL(s, value)
		t.Setenv(name, proxyURL)
	}
}

// Target is the upstream a request is for.
type Target struct {
	Name   string
	Scheme string
	Port   string
}

// Targets are every upstream shape the matrices cross.
var Targets = []Target{
	{Name: "https", Scheme: "https", Port: "443"},
	{Name: "http", Scheme: "http", Port: "80"},
	// A TLS upstream on a non-standard port: a fasthttp dialer sees only host:port and
	// treats every port but 443 as plain HTTP, so it picks HTTP_PROXY; net/http and the
	// WebSocket dialer know the scheme and pick HTTPS_PROXY.
	{Name: "https-8443", Scheme: "https", Port: "8443"},
	{Name: "http-8080", Scheme: "http", Port: "8080"},
}

// Target hosts. A .test name never resolves, so a direct connection to TargetHost fails
// at once without leaving the machine. FetchTargetHost is a documentation-range public
// IP: the fetch client resolves targets locally for its SSRF check, which a .test name
// would fail before any proxy is tried.
const (
	TargetHost      = "api.bifrost.test"
	FetchTargetHost = "203.0.113.10"
)

// EnvRule is how a stack chooses between HTTPS_PROXY and HTTP_PROXY.
type EnvRule int

const (
	// ByPort is the fasthttp rule: the dialer sees only host:port, so 443 is https.
	ByPort EnvRule = iota
	// ByScheme is the net/http and WebSocket rule: the request scheme decides.
	ByScheme
)

// Expect is the routing spec. unsetUsesEnv says whether the stack proxies from the
// environment when proxy_config is unset or "none" (the auth client and Bedrock do,
// inference, fetch and WebSocket connect directly).
func Expect(s *Set, source Source, env Env, target Target, rule EnvRule, unsetUsesEnv bool) Route {
	if source.Route != nil {
		return *source.Route
	}
	switch source.Name {
	case "unset", "none":
		if !unsetUsesEnv {
			return Direct
		}
	case "environment":
	default:
		panic("proxytest: unknown source " + source.Name)
	}
	https := target.Scheme == "https"
	if rule == ByPort {
		https = target.Port == "443"
	}
	spec := EnvPick(env, https)
	if spec == "" {
		return Direct
	}
	_, route := EnvURL(s, spec)
	return route
}

// EnvPick is golang.org/x/net/http/httpproxy's rule: a NO_PROXY match (or "*") is
// direct; https uses HTTPS_PROXY and http uses HTTP_PROXY, with no fallback between
// them; the lowercase spelling wins over the uppercase one; ALL_PROXY is not read. It
// returns the chosen variable's spec, or "" for a direct connection.
func EnvPick(env Env, https bool) string {
	switch FirstNonEmpty(env.Vars["no_proxy"], env.Vars["NO_PROXY"]) {
	case "target", "*":
		return ""
	}
	if https {
		return FirstNonEmpty(env.Vars["https_proxy"], env.Vars["HTTPS_PROXY"])
	}
	return FirstNonEmpty(env.Vars["http_proxy"], env.Vars["HTTP_PROXY"])
}

// FirstNonEmpty returns the first non-empty value.
func FirstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// directFailureMarkers are what an attempt to connect straight to the target looks like:
// a .test name failing to resolve, or a fetch to the documentation-range IP timing out.
var directFailureMarkers = []string{"no such host", "lookup ", "deadline exceeded", "i/o timeout"}

// AssertRoute checks one request's outcome: the expected recorder saw exactly one hit for
// target with the expected credentials and every other recorder saw nothing. For a
// MustFail route the request must also have failed, and not with the error a direct
// connection to the target would give.
func AssertRoute(t *testing.T, s *Set, want Route, target string, err error) {
	t.Helper()
	wantHit := Hit{Target: target, Auth: want.Auth}
	if want.MustFail {
		wantHit.Target = ""
		if err == nil {
			t.Errorf("request succeeded, want it to fail when the proxy refuses the login")
		} else {
			for _, marker := range directFailureMarkers {
				if strings.Contains(err.Error(), marker) {
					t.Errorf("request fell back to a direct connection after the proxy refused it: %v", err)
				}
			}
		}
	}
	for _, r := range s.All {
		seen := r.Seen()
		if r.Name == want.Proxy {
			if len(seen) != 1 || seen[0] != wantHit {
				t.Errorf("proxy %q saw %+v, want exactly [%+v]", r.Name, seen, wantHit)
			}
			continue
		}
		if len(seen) != 0 {
			if want.Proxy == "" {
				t.Errorf("want a direct connection, but proxy %q saw %+v", r.Name, seen)
			} else {
				t.Errorf("want proxy %q, but proxy %q saw %+v", want.Proxy, r.Name, seen)
			}
		}
	}
}

// ForwardProxy starts an HTTP proxy that serves handler for both proxy styles:
// absolute-URI requests and CONNECT tunnels carrying plain HTTP requests. fasthttp's
// proxy dialer tunnels every target with CONNECT, http:// ones included, so a test proxy
// for a fasthttp-backed client must speak both. handler sees r.URL.Host set to the target
// (the port is dropped when it is 80).
func ForwardProxy(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			handler(w, r)
			return
		}
		target := strings.TrimSuffix(r.Host, ":80")
		conn, buffered, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = conn.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n"))
		reader := bufio.NewReader(io.MultiReader(buffered.Reader, conn))
		for {
			inner, err := http.ReadRequest(reader)
			if err != nil {
				return
			}
			inner.URL.Scheme = "http"
			inner.URL.Host = target
			recorder := httptest.NewRecorder()
			handler(recorder, inner)
			// Result leaves the length unknown, which Write turns into a close-delimited
			// body; the tunnel stays open, so give it a length.
			resp := recorder.Result()
			resp.ContentLength = int64(recorder.Body.Len())
			if err := resp.Write(conn); err != nil {
				return
			}
		}
	}))
	t.Cleanup(server.Close)
	return server
}
