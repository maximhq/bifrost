package chaosmock

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Every virtual server also answers plain HTTP under /s/<name>/rest/..., for
// clients that call an upstream over REST rather than MCP: an OpenAPI-backed
// tool, a catalog download. Responses are set per route; anything unrouted
// gets a deterministic echo; and every request is logged with its headers and
// body, which is the only place a REST client's outgoing request can be
// checked. The mock never interprets what it serves: an OpenAPI document or a
// catalog is just a route's body.

// Route is one canned response.
type Route struct {
	// Method matches case-insensitively; empty matches any method.
	Method string `json:"method,omitempty"`
	// Path starts with "/" and is relative to /s/<name>/rest. A "{x}"
	// segment matches any one segment. The query string is not matched.
	Path string `json:"path"`
	// Status defaults to 200.
	Status int `json:"status,omitempty"`
	// ContentType defaults by body kind: application/json for JSON,
	// text/plain for Body, application/octet-stream for BodyBase64.
	ContentType string            `json:"content_type,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
	// At most one body. JSON is served byte for byte as sent; Body verbatim,
	// so it can be malformed on purpose; BodyBase64 for binary.
	JSON       json.RawMessage `json:"json,omitempty"`
	Body       string          `json:"body,omitempty"`
	BodyBase64 string          `json:"body_base64,omitempty"`
}

// Validate rejects a route that could not be served as written.
func (rt Route) Validate() error {
	if !strings.HasPrefix(rt.Path, "/") {
		return fmt.Errorf("route path must start with /, got %q", rt.Path)
	}
	kinds := 0
	for _, set := range []bool{len(rt.JSON) > 0, rt.Body != "", rt.BodyBase64 != ""} {
		if set {
			kinds++
		}
	}
	if kinds > 1 {
		return fmt.Errorf("route %s: set at most one of json, body and body_base64", rt.Path)
	}
	if _, err := base64.StdEncoding.DecodeString(rt.BodyBase64); err != nil {
		return fmt.Errorf("route %s: body_base64 is not base64: %w", rt.Path, err)
	}
	if rt.Status != 0 && (rt.Status < 100 || rt.Status > 999) {
		return fmt.Errorf("route %s: status %d is not an HTTP status", rt.Path, rt.Status)
	}
	return nil
}

// matches reports whether the route answers method and path.
func (rt Route) matches(method, path string) bool {
	if rt.Method != "" && !strings.EqualFold(rt.Method, method) {
		return false
	}
	want := strings.Split(rt.Path, "/")
	got := strings.Split(path, "/")
	if len(want) != len(got) {
		return false
	}
	for i, seg := range want {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") && got[i] != "" {
			continue
		}
		if seg != got[i] {
			return false
		}
	}
	return true
}

// sameAs reports whether two routes answer the same requests, which is what
// routes/add replaces.
func (rt Route) sameAs(other Route) bool {
	return strings.EqualFold(rt.Method, other.Method) && rt.Path == other.Path
}

// response renders the route: status, content type and body.
func (rt Route) response() (int, string, []byte) {
	status := rt.Status
	if status == 0 {
		status = http.StatusOK
	}
	var body []byte
	contentType := ""
	switch {
	case len(rt.JSON) > 0:
		body, contentType = rt.JSON, "application/json"
	case rt.Body != "":
		body, contentType = []byte(rt.Body), "text/plain; charset=utf-8"
	case rt.BodyBase64 != "":
		body, _ = base64.StdEncoding.DecodeString(rt.BodyBase64)
		contentType = "application/octet-stream"
	}
	if rt.ContentType != "" {
		contentType = rt.ContentType
	}
	return status, contentType, body
}

// RESTCall is one request the REST surface received.
type RESTCall struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Query  string `json:"query"`
	// Headers maps each canonical header name to its values joined with
	// ", ". Credentials are kept: checking what a client sent is the point.
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
	// Status is what the mock answered, 0 when it refused the connection.
	Status int `json:"status"`
	// Route is the path of the route that answered, empty for the echo.
	Route string `json:"route,omitempty"`
}

// RESTState is the REST surface's readable state.
type RESTState struct {
	Routes []Route    `json:"routes"`
	Calls  []RESTCall `json:"calls"`
}

// restLogCap bounds the call log; a long run keeps the most recent calls.
const restLogCap = 1000

// restSurface holds one server's routes and call log.
type restSurface struct {
	mu     sync.Mutex
	routes []Route
	calls  []RESTCall
}

// set replaces every route.
func (rs *restSurface) set(routes []Route) {
	rs.mu.Lock()
	rs.routes = append([]Route{}, routes...)
	rs.mu.Unlock()
}

// add replaces the route answering the same method and path, or appends.
func (rs *restSurface) add(rt Route) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for i := range rs.routes {
		if rs.routes[i].sameAs(rt) {
			rs.routes[i] = rt
			return
		}
	}
	rs.routes = append(rs.routes, rt)
}

// remove drops the routes for path, only those for method when it is set,
// and reports how many went.
func (rs *restSurface) remove(method, path string) int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	kept := rs.routes[:0]
	removed := 0
	for _, rt := range rs.routes {
		if rt.Path == path && (method == "" || strings.EqualFold(rt.Method, method)) {
			removed++
			continue
		}
		kept = append(kept, rt)
	}
	rs.routes = kept
	return removed
}

// match returns the first route answering method and path.
func (rs *restSurface) match(method, path string) (Route, bool) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	for _, rt := range rs.routes {
		if rt.matches(method, path) {
			return rt, true
		}
	}
	return Route{}, false
}

// record appends a call to the log.
func (rs *restSurface) record(c RESTCall) {
	rs.mu.Lock()
	rs.calls = append(rs.calls, c)
	if len(rs.calls) > restLogCap {
		rs.calls = rs.calls[len(rs.calls)-restLogCap:]
	}
	rs.mu.Unlock()
}

// clearLog empties the call log, keeping the routes.
func (rs *restSurface) clearLog() {
	rs.mu.Lock()
	rs.calls = nil
	rs.mu.Unlock()
}

// snapshot returns the readable state, or nil when the surface has neither
// routes nor calls, so a server that never used it reads back without it.
func (rs *restSurface) snapshot() *RESTState {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if len(rs.routes) == 0 && len(rs.calls) == 0 {
		return nil
	}
	return &RESTState{
		Routes: append([]Route{}, rs.routes...),
		Calls:  append([]RESTCall{}, rs.calls...),
	}
}

// ServeREST answers one request on the REST surface; path is relative to
// /s/<name>/rest. The server-wide faults apply as they do to a GET stream:
// only an unscoped set reaches it, and of that set the transport, HTTP and
// timing fields, since nothing here is JSON-RPC.
func (s *Server) ServeREST(w http.ResponseWriter, r *http.Request, path string) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	call := RESTCall{Method: r.Method, Path: path, Query: r.URL.RawQuery, Headers: map[string]string{}, Body: string(body)}
	for name, values := range r.Header {
		call.Headers[name] = strings.Join(values, ", ")
	}

	f := s.faults.forRequest("")
	if f.Refuse || f.Status != 0 {
		if !f.Refuse {
			call.Status = f.Status
		}
		s.rest.record(call)
		s.failRequest(w, f)
		return
	}
	if d := time.Duration(f.Hang); d > 0 {
		time.Sleep(d)
	}
	if d := s.faults.delay(f); d > 0 {
		time.Sleep(d)
	}

	status, contentType, payload := http.StatusOK, "application/json", []byte(nil)
	if rt, ok := s.rest.match(r.Method, path); ok {
		call.Route = rt.Path
		status, contentType, payload = rt.response()
		for k, v := range rt.Headers {
			w.Header().Set(k, v)
		}
	} else {
		// The echo is deterministic, so a test that sets no routes can
		// still compare a response exactly.
		payload, _ = json.Marshal(map[string]string{
			"method": r.Method, "path": path, "query": r.URL.RawQuery, "body": string(body),
		})
	}
	call.Status = status
	s.rest.record(call)

	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(status)
	_, _ = w.Write(payload)
}
