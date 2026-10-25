package chaosmock

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
)

// Fleet hosts many virtual MCP servers behind one listener. One listener rather
// than one per server is deliberate: a test that needs a server to become
// unreachable should be able to do that without taking down the servers it is
// comparing against, and separate ports would tempt a scenario to stop the
// whole process instead.
//
// Paths are stable and derivable from a server's name, so a test that
// provisions a client can compute the URL without asking the fleet:
//
//	/s/<name>/mcp        streamable HTTP
//	/s/<name>/as/...     that server's OAuth authorization server, when on
//	/control/<name>/...  that server's control API
//	/control             fleet-wide control API
//
// plus the path-inserted OAuth metadata documents described in oauth.go.
type Fleet struct {
	mu      sync.RWMutex
	servers map[string]*Server

	ts *httptest.Server
	// baseURL is set when Serve binds its own listener, since there is no
	// httptest.Server to read an address from in that mode.
	baseURL string

	conns *connTracker
}

// NewFleet creates an empty fleet with no listener. Call Start to bind one.
func NewFleet() *Fleet {
	return &Fleet{servers: map[string]*Server{}, conns: newConnTracker()}
}

// Connections reports the fleet's TCP-level accounting, which is the leak and
// reconnect-storm signal. It is fleet-wide because one listener serves every
// virtual server; see connTracker for why that is the honest scope.
func (f *Fleet) Connections() ConnectionStats { return f.conns.stats() }

// ResetConnectionStats zeroes the tallies while preserving the live open count.
func (f *Fleet) ResetConnectionStats() { f.conns.reset() }

// Add creates and registers a virtual server. Re-adding an existing name
// replaces it, which is how a scenario models a server being torn down and
// rebuilt with different behaviour at the same address.
func (f *Fleet) Add(name string) *Server {
	s := NewServer(name)
	f.mu.Lock()
	f.servers[name] = s
	f.mu.Unlock()
	return s
}

// Get returns a registered server.
func (f *Fleet) Get(name string) (*Server, bool) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	s, ok := f.servers[name]
	return s, ok
}

// Remove deregisters a server and drops its open streams, so a client holding
// one learns immediately rather than at its next request.
func (f *Fleet) Remove(name string) bool {
	f.mu.Lock()
	s, ok := f.servers[name]
	delete(f.servers, name)
	f.mu.Unlock()
	if ok {
		s.DropStreams()
	}
	return ok
}

// Names lists registered servers in a stable order.
func (f *Fleet) Names() []string {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]string, 0, len(f.servers))
	for name := range f.servers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Start binds a listener on a free port and begins serving. The port is chosen
// by the OS so a fleet never collides with a development server or another
// fleet in a parallel test.
func (f *Fleet) Start() string {
	f.ts = httptest.NewUnstartedServer(f.Handler())
	f.ts.Config.ConnState = f.conns.hook()
	f.ts.Start()
	return f.ts.URL
}

// Serve binds addr and serves until the returned shutdown function is called.
// This is the standalone-process counterpart to Start: a test wants an
// ephemeral port and automatic cleanup, while an operator running the mock
// alongside a real deployment needs a stable address to point configuration at.
func (f *Fleet) Serve(addr string) (string, func(context.Context) error, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", nil, err
	}
	srv := &http.Server{Handler: f.Handler(), ConnState: f.conns.hook()}
	go func() {
		// A closed listener is the normal way this ends, so it is not news.
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("chaosmock: serve stopped: %v", err)
		}
	}()
	url := "http://" + ln.Addr().String()
	f.mu.Lock()
	f.baseURL = url
	f.mu.Unlock()
	return url, func(ctx context.Context) error {
		f.mu.RLock()
		for _, s := range f.servers {
			s.DropStreams()
		}
		f.mu.RUnlock()
		return srv.Shutdown(ctx)
	}, nil
}

// Stop shuts the listener down and drops every open stream.
func (f *Fleet) Stop() {
	f.mu.RLock()
	for _, s := range f.servers {
		s.DropStreams()
	}
	f.mu.RUnlock()
	if f.ts != nil {
		f.ts.Close()
	}
}

// URL returns the fleet's base URL, empty before Start.
func (f *Fleet) URL() string {
	if f.ts != nil {
		return f.ts.URL
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.baseURL
}

// ServerURL returns the streamable HTTP endpoint for one server, which is what
// an MCP client config points at.
func (f *Fleet) ServerURL(name string) string {
	return f.URL() + "/s/" + name + "/mcp"
}

// SSEURL returns the legacy HTTP+SSE endpoint for one server. The message
// endpoint is not exposed separately because a client is told where it is by
// the endpoint event, which is part of what this transport is for.
func (f *Fleet) SSEURL(name string) string {
	return f.URL() + "/s/" + name + "/sse"
}

// Handler returns the fleet's HTTP handler, exported so a test can mount it in
// its own server when it needs control over the listener.
func (f *Fleet) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/s/", f.routeServer)
	mux.HandleFunc(wellKnownResource, f.routeWellKnown)
	mux.HandleFunc(wellKnownAuthServer, f.routeWellKnown)
	mux.HandleFunc("/control", f.routeFleetControl)
	mux.HandleFunc("/control/", f.routeControl)
	return mux
}

// routeServer dispatches /s/<name>/<transport> to the named server.
func (f *Fleet) routeServer(w http.ResponseWriter, r *http.Request) {
	name, rest, ok := splitServerPath(r.URL.Path, "/s/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	s, found := f.Get(name)
	if !found {
		// A request for a server that was removed is a genuine 404 rather than
		// a refused connection, and the difference is visible to a client:
		// one reads as "gone", the other as "unreachable".
		http.Error(w, "no such server: "+name, http.StatusNotFound)
		return
	}
	switch {
	case rest == "mcp":
		s.ServeStreamableHTTP(w, r)
	case rest == "sse":
		s.ServeSSE(w, r)
	case rest == "sse/message":
		s.ServeSSEMessage(w, r)
	case strings.HasPrefix(rest, "as/"):
		s.ServeOAuth(w, r, strings.TrimPrefix(rest, "as/"))
	default:
		http.NotFound(w, r)
	}
}

// The well-known prefixes the OAuth metadata documents sit under. A server's
// document is path-inserted (RFC 8414 section 3.1, RFC 9728 section 3.1): the
// prefix followed by the path of the thing it describes.
const (
	wellKnownResource   = "/.well-known/oauth-protected-resource/"
	wellKnownAuthServer = "/.well-known/oauth-authorization-server/"
)

// routeWellKnown dispatches a path-inserted metadata request to the server
// whose endpoint the inserted path names. Anything else, the bare root
// documents included, is a 404.
func (f *Fleet) routeWellKnown(w http.ResponseWriter, r *http.Request) {
	prefix := wellKnownResource
	if strings.HasPrefix(r.URL.Path, wellKnownAuthServer) {
		prefix = wellKnownAuthServer
	}
	inserted := "/" + strings.TrimPrefix(r.URL.Path, prefix)
	name, rest, ok := splitServerPath(inserted, "/s/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	s, found := f.Get(name)
	switch {
	case !found:
		http.NotFound(w, r)
	case prefix == wellKnownResource && (rest == "mcp" || rest == "sse"):
		s.ServeProtectedResourceMetadata(w, r, inserted)
	case prefix == wellKnownAuthServer && rest == "as":
		s.ServeAuthorizationServerMetadata(w, r)
	default:
		http.NotFound(w, r)
	}
}

// splitServerPath pulls the server name and the remainder out of a prefixed
// path. It returns ok false rather than guessing when either part is missing,
// so a malformed path is a 404 rather than a request against an arbitrary
// server.
func splitServerPath(path, prefix string) (name, rest string, ok bool) {
	if !strings.HasPrefix(path, prefix) {
		return "", "", false
	}
	trimmed := strings.TrimPrefix(path, prefix)
	name, rest, found := strings.Cut(trimmed, "/")
	if name == "" {
		return "", "", false
	}
	if !found {
		return name, "", true
	}
	return name, rest, true
}
