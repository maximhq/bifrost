package chaosmock

import (
	"encoding/json"
	"net/http"
	"strings"
)

// The control API is how a scenario changes a virtual server's behaviour while
// a client is connected to it. It is plain HTTP with JSON bodies rather than a
// Go API so that any driver can use the mock, including one in another process
// or another language, and so that a human can drive it from curl while
// watching a client react.
//
// Every mutating verb answers with the server's state after the change, which
// makes a scenario step self-documenting in a transcript: the request says what
// was asked for and the response says what the server then looked like.

// State is the full readable state of one virtual server. It is what a
// four-way reading calls "intended" (the tool set and faults asked for) and
// "served" (the counters) at the same time, which is why they are returned
// together rather than from separate endpoints that could be sampled apart.
type State struct {
	Name     string           `json:"name"`
	Tools    []Tool           `json:"tools"`
	Revision uint64           `json:"revision"`
	Faults   Faults           `json:"faults"`
	Auth     Auth             `json:"auth"`
	Counters CountersSnapshot `json:"counters"`
	Sessions int              `json:"sessions"`
	Streams  int              `json:"streams"`
	// StdioPID is the child process bridging this server over stdin and
	// stdout, or 0 when none has registered.
	StdioPID int `json:"stdio_pid"`
}

// State captures a server's current state.
func (s *Server) State() State {
	return State{
		Name:     s.name,
		Tools:    s.tools.List(""),
		Revision: s.tools.Revision(),
		Faults:   s.faults.peek(),
		Auth:     s.auth.get(),
		Counters: s.counters.Snapshot(),
		Sessions: s.sessions.count(),
		Streams:  s.streams.count(),
		StdioPID: s.StdioPID(),
	}
}

// routeFleetControl serves the fleet-wide control surface at /control.
func (f *Fleet) routeFleetControl(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		states := map[string]State{}
		for _, name := range f.Names() {
			if s, ok := f.Get(name); ok {
				states[name] = s.State()
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"base_url":    f.URL(),
			"servers":     states,
			"connections": f.Connections(),
		})
	case http.MethodPost:
		var body struct {
			Add    string `json:"add"`
			Remove string `json:"remove"`
		}
		if err := decodeBody(r, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		switch {
		case body.Add != "":
			s := f.Add(body.Add)
			writeJSON(w, http.StatusOK, map[string]any{
				"added": body.Add,
				"url":   f.ServerURL(body.Add),
				"state": s.State(),
			})
		case body.Remove != "":
			writeJSON(w, http.StatusOK, map[string]any{"removed": f.Remove(body.Remove)})
		default:
			http.Error(w, "specify add or remove", http.StatusBadRequest)
		}
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// routeControl serves /control/<name>/<verb>.
func (f *Fleet) routeControl(w http.ResponseWriter, r *http.Request) {
	name, verb, ok := splitServerPath(r.URL.Path, "/control/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	s, found := f.Get(name)
	if !found {
		http.Error(w, "no such server: "+name, http.StatusNotFound)
		return
	}
	if verb == "" || verb == "state" {
		writeJSON(w, http.StatusOK, s.State())
		return
	}
	if err := s.applyControl(verb, r); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, s.State())
}

// controlBody is the union of every control verb's parameters. One shape
// rather than a type per verb keeps the dispatch below readable, and an unused
// field costs nothing since callers only send what a verb needs.
type controlBody struct {
	Tool     Tool            `json:"tool,omitempty"`
	Tools    []Tool          `json:"tools,omitempty"`
	Name     string          `json:"name,omitempty"`
	From     string          `json:"from,omitempty"`
	To       string          `json:"to,omitempty"`
	Text     string          `json:"text,omitempty"`
	Schema   json.RawMessage `json:"schema,omitempty"`
	Identity string          `json:"identity,omitempty"`
	Token    string          `json:"token,omitempty"`
	PID      int             `json:"pid,omitempty"`
	Faults   *Faults         `json:"faults,omitempty"`
	Auth     *Auth           `json:"auth,omitempty"`
	// Clear distinguishes an overlay being removed from one being set to an
	// empty tool list. Both are legitimate and they mean different things: the
	// first returns an identity to the base set, the second shows it nothing.
	Clear bool `json:"clear,omitempty"`
}

func (s *Server) applyControl(verb string, r *http.Request) error {
	var body controlBody
	if err := decodeBody(r, &body); err != nil {
		return err
	}

	switch strings.TrimSuffix(verb, "/") {
	case "tools/add":
		s.tools.Add(body.Tool)
	case "tools/remove":
		s.tools.Remove(body.Name)
	case "tools/set":
		s.tools.Set(body.Tools)
	case "tools/rename":
		if _, err := s.tools.Rename(body.From, body.To); err != nil {
			return err
		}
	case "tools/describe":
		if _, err := s.tools.Describe(body.Name, body.Text); err != nil {
			return err
		}
	case "tools/schema":
		if _, err := s.tools.Schema(body.Name, body.Schema); err != nil {
			return err
		}
	case "tools/overlay":
		if body.Clear {
			s.tools.SetOverlay(body.Identity, nil)
		} else {
			// A non-nil empty slice is what shows an identity an empty tool
			// list, so the zero value has to be replaced explicitly.
			tools := body.Tools
			if tools == nil {
				tools = []Tool{}
			}
			s.tools.SetOverlay(body.Identity, tools)
		}
	case "faults":
		if body.Faults == nil {
			return errBadRequest("faults body is required")
		}
		s.faults.set(*body.Faults)
	case "faults/clear":
		s.faults.clear()
	case "auth":
		if body.Auth == nil {
			return errBadRequest("auth body is required")
		}
		s.auth.set(*body.Auth)
	case "auth/grant":
		s.auth.grant(body.Token, body.Identity)
	case "auth/revoke":
		if !s.auth.revoke(body.Token) {
			return errBadRequest("token not found")
		}
	case "sessions/kill":
		s.sessions.killAll()
	case "streams/drop":
		s.streams.closeAll()
	case "notify/tools-changed":
		s.NotifyToolListChanged()
	case "stdio/register":
		if body.PID <= 0 {
			return errBadRequest("pid is required")
		}
		s.RegisterStdioPID(body.PID)
	case "stdio/kill":
		if err := s.KillStdio(); err != nil {
			return errBadRequest(err.Error())
		}
	case "counters/reset":
		s.counters.Reset()
	default:
		return errBadRequest("unknown control verb: " + verb)
	}
	return nil
}

// decodeBody reads an optional JSON body. Verbs that take no parameters are
// called with an empty body, so absence is not an error.
func decodeBody(r *http.Request, v any) error {
	if r.Body == nil || r.ContentLength == 0 {
		return nil
	}
	dec := json.NewDecoder(r.Body)
	// Unknown fields are rejected so a scenario with a typo fails loudly
	// rather than silently doing nothing, which in a chaos harness would look
	// exactly like the system under test behaving correctly.
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errBadRequest("invalid JSON body: " + err.Error())
	}
	return nil
}

type errBadRequest string

func (e errBadRequest) Error() string { return string(e) }
