package chaosmock

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ServeSSE handles the legacy HTTP+SSE transport, whose shape differs from
// streamable HTTP in the one way that matters for testing a client: a
// request's response does not come back in the reply to the POST that carried
// it. The POST is acknowledged with 202 and the response arrives out-of-band
// on the stream the client opened separately.
//
// That split is worth exercising rather than assuming equivalent, because it
// changes what a dropped connection means. Under streamable HTTP a dropped
// connection fails the request in flight. Here it strands it: the POST already
// succeeded, and the response has nowhere to land, so a client that does not
// notice the stream is gone waits forever for an answer that was written to a
// closed channel.
//
// The stream is also the session. There is no separate session header on this
// transport, so the endpoint event carries the session id and every POST names
// it in the query string.
func (s *Server) ServeSSE(w http.ResponseWriter, r *http.Request) {
	s.counters.request()

	// The stream carries no JSON-RPC method, so only an unscoped fault set
	// reaches it.
	if s.failRequest(w, s.faults.forRequest("")) {
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	authResult := s.auth.check(r.Header)
	if !authResult.OK {
		w.Header().Set("WWW-Authenticate", `Bearer realm="chaosmock"`)
		http.Error(w, authResult.Reason, http.StatusUnauthorized)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	// The session exists from the moment the stream opens, since under this
	// transport the stream is what identifies the caller. It is created before
	// initialize rather than by it, which is the opposite of streamable HTTP.
	sess := s.sessions.create(authResult.Identity, DefaultProtocolVersion)
	s.counters.session()

	ch := s.streams.addKeyed(sess.ID)
	s.counters.streamOpened()
	defer func() {
		s.streams.remove(ch)
		s.counters.streamClosed()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	// The endpoint event has to come first, and a conforming client resolves
	// it against the stream URL and refuses it if the origin differs. It is
	// derived from this request's own path rather than built from the server
	// name, both so the same-origin check holds however the fleet was
	// addressed, and so this transport does not have to know how the fleet
	// routes to it. A bare relative reference would not do: it replaces the
	// last path segment rather than extending it, so "message" against
	// /s/alpha/sse resolves to /s/alpha/message.
	fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", r.URL.Path+"/message?session="+sess.ID)
	flusher.Flush()

	keepAlive := time.NewTicker(sseKeepAlive)
	defer keepAlive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case payload, open := <-ch:
			if !open {
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

// ServeSSEMessage handles the POST half of the legacy transport. It answers
// 202 and writes the response to the caller's stream, so a test can tell
// "the request was accepted" apart from "the response arrived", which on this
// transport are genuinely different events. The body is read and counted
// before any fault or session check answers it, as on streamable HTTP.
func (s *Server) ServeSSEMessage(w http.ResponseWriter, r *http.Request) {
	s.counters.request()

	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}
	sessionID := r.URL.Query().Get("session")

	var req request
	parsed := json.Unmarshal(body, &req) == nil
	if parsed {
		s.countMethod(req.Method, sessionID)
	}
	f := s.faults.forRequest(req.Method)
	if s.failRequest(w, f) {
		return
	}

	sess, known := s.sessions.get(sessionID)
	if !known || f.KillSession {
		// The stream may still be open while the session is gone, which is
		// exactly the state a session-expiry fault produces.
		s.counters.faulted()
		http.Error(w, "unknown session", http.StatusNotFound)
		return
	}

	if !parsed {
		s.deliver(sessionID, newError(nil, CodeParseError, "invalid JSON"))
		w.WriteHeader(http.StatusAccepted)
		return
	}

	f = s.callFaults(&req, sess.Identity, f)
	if s.failRequest(w, f) {
		return
	}

	res := s.Handle(&req, sessionID, sess.Identity, f)

	switch {
	case res.HTTPStatus != 0:
		// An injected HTTP failure is the one case that answers on the POST
		// rather than the stream, since there is no JSON-RPC message to carry.
		http.Error(w, res.Message, res.HTTPStatus)
		return
	case res.Malformed:
		s.deliverRaw(sessionID, []byte(`{"jsonrpc":"2.0","result":{"tools":[{"name":`))
		w.WriteHeader(http.StatusAccepted)
		return
	case res.Response == nil:
		// A notification. Nothing to deliver, and the acknowledgement is the
		// whole answer.
		w.WriteHeader(http.StatusAccepted)
		return
	}

	s.deliver(sessionID, res.Response)
	w.WriteHeader(http.StatusAccepted)
}

// deliver writes a JSON-RPC message to one session's stream.
func (s *Server) deliver(sessionID string, msg *response) {
	payload, err := json.Marshal(msg)
	if err != nil {
		return
	}
	s.deliverRaw(sessionID, payload)
}

// deliverRaw writes bytes verbatim, so a malformed-body fault can put
// something on the stream that no decoder will accept.
func (s *Server) deliverRaw(sessionID string, payload []byte) {
	s.streams.send(sessionID, payload)
}
