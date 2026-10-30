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
//	/s/<name>/mcp   streamable HTTP
type Fleet struct {
	mu      sync.RWMutex
	servers map[string]*Server

	ts *httptest.Server
	// baseURL is set when Serve binds its own listener, since there is no
	// httptest.Server to read an address from in that mode.
	baseURL string
}

// NewFleet creates an empty fleet with no listener. Call Start to bind one.
func NewFleet() *Fleet {
	return &Fleet{servers: map[string]*Server{}}
}

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

// Remove deregisters a server. A request for it afterwards is a 404 rather
// than a refused connection, and the difference is visible to a client: one
// reads as gone, the other as unreachable.
func (f *Fleet) Remove(name string) bool {
	f.mu.Lock()
	_, ok := f.servers[name]
	delete(f.servers, name)
	f.mu.Unlock()
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
	f.ts = httptest.NewServer(f.Handler())
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
	srv := &http.Server{Handler: f.Handler()}
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
		return srv.Shutdown(ctx)
	}, nil
}

// Stop shuts the listener down.
func (f *Fleet) Stop() {
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

// Handler returns the fleet's HTTP handler, exported so a test can mount it in
// its own server when it needs control over the listener.
func (f *Fleet) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/s/", f.routeServer)
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
	switch rest {
	case "mcp":
		s.ServeStreamableHTTP(w, r)
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
