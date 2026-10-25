package chaosmock

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// sseKeepAlive is the comment-ping cadence on an open listening stream. It has
// to stay under the idle timeout of anything sitting between client and server,
// while being long enough that an idle test does not drown its own logs.
const sseKeepAlive = 15 * time.Second

// ServeStreamableHTTP handles one request against the streamable HTTP
// transport: POST carries JSON-RPC, GET opens the standalone listening stream,
// DELETE ends a session.
//
// The ordering here is the whole design. A POST's body is read first, so the
// request is counted and a fault scoped to its method is resolved before
// anything can answer it; nothing is written yet, so a connection that is
// going to be refused or reset still never looks like it worked. Transport
// faults come next, then HTTP faults, because a bare status is not a JSON-RPC
// exchange and must not be mistaken for one. Credentials and session validity
// come after that, since a 404 is the client's signal to re-initialize and has
// to beat any protocol handling. A tool's own fault applies only once the call
// is known to reach that tool. Only then does a well-formed exchange reach the
// protocol layer.
func (s *Server) ServeStreamableHTTP(w http.ResponseWriter, r *http.Request) {
	s.counters.request()

	switch r.Method {
	case http.MethodPost:
		s.servePost(w, r)
	case http.MethodGet, http.MethodDelete:
		// Neither carries a JSON-RPC method, so only an unscoped fault set
		// reaches them.
		if s.failRequest(w, s.faults.forRequest("")) {
			return
		}
		if r.Method == http.MethodGet {
			s.serveListeningStream(w, r)
		} else {
			s.serveDelete(w, r)
		}
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// failRequest answers with the transport or HTTP fault in f, if any, and
// reports whether it did, in which case the caller must stop.
func (s *Server) failRequest(w http.ResponseWriter, f Faults) bool {
	switch {
	case f.Refuse:
		// Close without writing anything. A client sees the connection go away
		// mid-request, which is what a dial against a dead listener looks like
		// once something is accepting on the port but nothing is serving.
		s.counters.faulted()
		hijackClose(w)
		return true
	case f.Status != 0:
		s.counters.faulted()
		s.writeStatusFault(w, f)
		return true
	}
	return false
}

// writeStatusFault renders an injected HTTP status with the headers that make
// it realistic: a 401 without WWW-Authenticate is not something a client can
// act on, and a 429 without Retry-After tells it nothing about when to come
// back.
func (s *Server) writeStatusFault(w http.ResponseWriter, f Faults) {
	if f.WWWAuth != "" {
		w.Header().Set("WWW-Authenticate", f.WWWAuth)
	} else if f.Status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="chaosmock"`)
	}
	if f.RetryAfter != "" {
		w.Header().Set("Retry-After", f.RetryAfter)
	}
	if f.Message != "" {
		// Plain text, because a client that receives a JSON object with an
		// error status may read it as a JSON-RPC response and never see the
		// status at all.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(f.Status)
		_, _ = io.WriteString(w, f.Message)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(f.Status)
	fmt.Fprintf(w, `{"error":"injected status %d"}`, f.Status)
}

// servePost handles one JSON-RPC message in the order ServeStreamableHTTP
// describes.
func (s *Server) servePost(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	sessionID := r.Header.Get(HeaderSessionID)

	var req request
	parsed := json.Unmarshal(body, &req) == nil
	if parsed {
		s.countMethod(req.Method, sessionID)
	}
	f := s.faults.forRequest(req.Method)
	if s.failRequest(w, f) {
		return
	}

	authResult := s.auth.check(r.Header)
	if !authResult.OK {
		// A genuine credential rejection, distinct from an injected 401. Both
		// produce 401 on the wire, but only this one reflects what the caller
		// actually presented, and only this one changes when a token is
		// revoked.
		w.Header().Set("WWW-Authenticate", `Bearer realm="chaosmock"`)
		http.Error(w, authResult.Reason, http.StatusUnauthorized)
		return
	}

	if !parsed {
		writeJSON(w, http.StatusOK, newError(nil, CodeParseError, "invalid JSON"))
		return
	}

	identity := authResult.Identity

	// Session validity is only meaningful once one has been issued. An
	// initialize request legitimately arrives without a session id, and must
	// not be 404'd for it.
	if req.Method != MethodInitialize {
		if f.KillSession {
			s.counters.faulted()
			http.Error(w, "session terminated", http.StatusNotFound)
			return
		}
		if sessionID != "" {
			sess, ok := s.sessions.get(sessionID)
			if !ok {
				http.Error(w, "unknown session", http.StatusNotFound)
				return
			}
			// The session's identity wins over the request's. It was resolved
			// at initialize and is what the server has been attributing work
			// to; letting a later request change it would make per-identity
			// counters meaningless.
			identity = sess.Identity
		}
	}

	f = s.callFaults(&req, identity, f)
	if s.failRequest(w, f) {
		return
	}

	res := s.Handle(&req, sessionID, identity, f)

	if res.NewSession != nil {
		w.Header().Set(HeaderSessionID, res.NewSession.ID)
		w.Header().Set(HeaderProtocolVersion, res.NewSession.ProtocolVersion)
	}

	switch {
	case res.HTTPStatus != 0:
		http.Error(w, res.Message, res.HTTPStatus)
		return
	case res.Malformed:
		// Valid HTTP, valid content type, and a body no JSON-RPC decoder can
		// accept. This exercises a client's decoder rather than its error
		// handling, which are different code paths that fail differently.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","result":{"tools":[{"name":`)
		return
	case res.Response == nil:
		// A notification. The specification allows 202 with no body, and using
		// it rather than 200 lets a test tell the two apart on the wire.
		w.WriteHeader(http.StatusAccepted)
		return
	}

	if f.Reset {
		// Commit to a response, write part of it, then sever the connection.
		// A client that reads eagerly sees a truncated body rather than a
		// clean error, which is the case most likely to be mishandled.
		s.counters.faulted()
		hijackWritePartial(w, res.Response)
		return
	}

	writeJSON(w, http.StatusOK, res.Response)
}

// serveDelete ends the named session. Clients use this to shut down cleanly,
// and a test uses the resulting session count to tell a clean teardown from an
// abandoned connection.
func (s *Server) serveDelete(w http.ResponseWriter, r *http.Request) {
	sessionID := r.Header.Get(HeaderSessionID)
	if sessionID == "" {
		http.Error(w, "missing session id", http.StatusBadRequest)
		return
	}
	if _, ok := s.sessions.get(sessionID); !ok {
		http.Error(w, "unknown session", http.StatusNotFound)
		return
	}
	s.sessions.mu.Lock()
	delete(s.sessions.live, sessionID)
	s.sessions.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// serveListeningStream holds the standalone GET stream a client opens to
// receive server-initiated messages. Whether a client opens one at all is
// itself worth measuring, so this counts opens and holds the stream registered
// for the whole time it is up.
func (s *Server) serveListeningStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch := s.streams.add()
	s.counters.streamOpened()
	defer func() {
		s.streams.remove(ch)
		s.counters.streamClosed()
	}()

	keepAlive := time.NewTicker(sseKeepAlive)
	defer keepAlive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case payload, open := <-ch:
			if !open {
				// The registry closed this stream, either because a fault
				// dropped it or the server is shutting down. Returning ends
				// the HTTP response, which the client sees as its stream
				// ending.
				return
			}
			if _, err := fmt.Fprintf(w, "event: message\ndata: %s\n\n", payload); err != nil {
				return
			}
			flusher.Flush()
		case <-keepAlive.C:
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// hijackClose takes the connection away from net/http and closes it without a
// response. If hijacking is unavailable the best available approximation is a
// 502, which at least fails the request rather than succeeding it.
func hijackClose(w http.ResponseWriter) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "connection refused", http.StatusBadGateway)
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		http.Error(w, "connection refused", http.StatusBadGateway)
		return
	}
	_ = conn.Close()
}

// hijackWritePartial writes a valid response prefix and then severs the
// connection, producing a truncated read rather than a clean error.
func hijackWritePartial(w http.ResponseWriter, resp *response) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "connection reset", http.StatusBadGateway)
		return
	}
	conn, buf, err := hj.Hijack()
	if err != nil {
		http.Error(w, "connection reset", http.StatusBadGateway)
		return
	}
	defer conn.Close()

	body, err := json.Marshal(resp)
	if err != nil {
		return
	}
	// Announce a length the body will never reach, so a client reading to
	// Content-Length hits EOF early instead of parsing a short but complete
	// document.
	fmt.Fprintf(buf, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n", len(body))
	if len(body) > 8 {
		body = body[:len(body)/2]
	}
	buf.Write(body)
	_ = buf.Flush()
}
