package chaosmock

import (
	"encoding/json"
	"io"
	"net/http"
)

// ServeStreamableHTTP handles one request against the streamable HTTP
// transport: POST carries JSON-RPC and DELETE ends a session.
//
// Session validity is checked before the protocol layer is reached, because a
// 404 is the client's signal to re-initialize and has to beat any protocol
// handling: a request against a session the server has forgotten is not a
// request it should try to answer.
func (s *Server) ServeStreamableHTTP(w http.ResponseWriter, r *http.Request) {
	s.counters.request()

	switch r.Method {
	case http.MethodPost:
		s.servePost(w, r)
	case http.MethodDelete:
		s.serveDelete(w, r)
	default:
		w.Header().Set("Allow", "POST, DELETE")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) servePost(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	var req request
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusOK, newError(nil, CodeParseError, "invalid JSON"))
		return
	}

	sessionID := r.Header.Get(HeaderSessionID)

	// Session validity is only meaningful once one has been issued. An
	// initialize request legitimately arrives without a session id, and must
	// not be 404'd for it.
	if req.Method != MethodInitialize && sessionID != "" {
		if _, ok := s.sessions.get(sessionID); !ok {
			http.Error(w, "unknown session", http.StatusNotFound)
			return
		}
	}

	res := s.Handle(&req, sessionID)

	if res.NewSession != nil {
		w.Header().Set(HeaderSessionID, res.NewSession.ID)
		w.Header().Set(HeaderProtocolVersion, res.NewSession.ProtocolVersion)
	}

	if res.Response == nil {
		// A notification. The specification allows 202 with no body, and using
		// it rather than 200 lets a test tell the two apart on the wire.
		w.WriteHeader(http.StatusAccepted)
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

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
