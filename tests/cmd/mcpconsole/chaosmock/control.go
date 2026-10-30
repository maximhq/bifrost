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
	// Instructions is what the initialize result carries, empty for none.
	Instructions string `json:"instructions,omitempty"`
	// StdioPID is the child process bridging this server over stdin and
	// stdout, or 0 when none has registered.
	StdioPID int `json:"stdio_pid"`
	// StdioEnv is the environment that child registered.
	StdioEnv map[string]string `json:"stdio_env,omitempty"`
	// OAuth is the authorization server's state, present while OAuth is on.
	OAuth *OAuthState `json:"oauth,omitempty"`
	// REST is the REST surface's routes and call log, present once used.
	REST *RESTState `json:"rest,omitempty"`
}

// State captures a server's current state.
func (s *Server) State() State {
	var oauth *OAuthState
	if s.auth.get().OAuth != nil {
		oauth = s.oauth.snapshot()
	}
	return State{
		Name:         s.name,
		Tools:        s.tools.List(""),
		Revision:     s.tools.Revision(),
		Faults:       s.faults.peek(),
		Auth:         s.auth.get(),
		Counters:     s.counters.Snapshot(),
		Sessions:     s.sessions.count(),
		Streams:      s.streams.count(),
		Instructions: s.Instructions(),
		StdioPID:     s.StdioPID(),
		StdioEnv:     s.StdioEnv(),
		OAuth:        oauth,
		REST:         s.rest.snapshot(),
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
			Reset  string `json:"reset"`
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
		case body.Reset != "":
			s, ok := f.Reset(body.Reset)
			if !ok {
				http.Error(w, "no seed for server: "+body.Reset, http.StatusNotFound)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"reset": body.Reset, "state": s.State()})
		default:
			http.Error(w, "specify add, remove or reset", http.StatusBadRequest)
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
	// Clear distinguishes removing something from setting it empty. Both are
	// legitimate and they mean different things: an overlay cleared returns
	// an identity to the base set while an empty one shows it nothing, and
	// instructions are removed only by clear, since empty text is refused.
	Clear bool `json:"clear,omitempty"`
	// Env is the environment a stdio bridge shares when it registers.
	Env map[string]string `json:"env,omitempty"`
	// Fail is a failure to queue on the OAuth authorization server.
	Fail *OAuthFail `json:"fail,omitempty"`
	// Route, Routes, Method and Path drive the routes/* verbs.
	Route  *Route  `json:"route,omitempty"`
	Routes []Route `json:"routes,omitempty"`
	Method string  `json:"method,omitempty"`
	Path   string  `json:"path,omitempty"`
}

func (s *Server) applyControl(verb string, r *http.Request) error {
	var body controlBody
	if err := decodeBody(r, &body); err != nil {
		return err
	}

	switch strings.TrimSuffix(verb, "/") {
	case "tools/add":
		if err := body.Tool.Validate(); err != nil {
			return errBadRequest(err.Error())
		}
		s.tools.Add(body.Tool)
	case "tools/remove":
		s.tools.Remove(body.Name)
	case "tools/set":
		if err := validateTools(body.Tools); err != nil {
			return err
		}
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
			if err := validateTools(tools); err != nil {
				return err
			}
			s.tools.SetOverlay(body.Identity, tools)
		}
	case "instructions":
		// An empty body is refused rather than read as "clear", since a
		// scenario that misspelled the text field would otherwise silently
		// serve no instructions.
		switch {
		case body.Clear:
			s.SetInstructions("")
		case body.Text != "":
			s.SetInstructions(body.Text)
		default:
			return errBadRequest("instructions takes text, or clear to remove them")
		}
	case "faults":
		if body.Faults == nil {
			return errBadRequest("faults body is required")
		}
		s.faults.set(*body.Faults)
	case "faults/clear":
		s.faults.clear()
		s.oauth.clearFails()
	case "auth":
		if body.Auth == nil {
			return errBadRequest("auth body is required")
		}
		if body.Auth.OAuth != nil {
			if err := body.Auth.OAuth.validate(); err != nil {
				return errBadRequest(err.Error())
			}
		}
		s.SetAuth(*body.Auth)
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
		s.RegisterStdio(body.PID, body.Env)
	case "stdio/kill":
		if err := s.KillStdio(); err != nil {
			return errBadRequest(err.Error())
		}
	case "counters/reset":
		s.counters.Reset()
		s.oauth.resetCounters()
		s.rest.clearLog()
	case "routes/set":
		for _, rt := range body.Routes {
			if err := rt.Validate(); err != nil {
				return errBadRequest(err.Error())
			}
		}
		s.rest.set(body.Routes)
	case "routes/add":
		if body.Route == nil {
			return errBadRequest("route body is required")
		}
		if err := body.Route.Validate(); err != nil {
			return errBadRequest(err.Error())
		}
		s.rest.add(*body.Route)
	case "routes/remove":
		if s.rest.remove(body.Method, body.Path) == 0 {
			return errBadRequest("no route matched")
		}
	case "oauth/expire", "oauth/revoke", "oauth/fail":
		return s.applyOAuthControl(strings.TrimSuffix(verb, "/"), body)
	default:
		return errBadRequest("unknown control verb: " + verb)
	}
	return nil
}

// applyOAuthControl handles the oauth/* verbs. Each refuses to do nothing:
// expiring or revoking no token is a scenario that names the wrong identity,
// and it would otherwise look like the client coping.
func (s *Server) applyOAuthControl(verb string, body controlBody) error {
	if s.auth.get().OAuth == nil {
		return errBadRequest("oauth is not enabled on this server")
	}
	switch verb {
	case "oauth/expire":
		if s.oauth.expire(body.Identity) == 0 {
			return errBadRequest("no live access token matched")
		}
	case "oauth/revoke":
		if s.oauth.revoke(body.Identity, body.Token) == 0 {
			return errBadRequest("no live grant matched")
		}
	case "oauth/fail":
		if body.Fail == nil {
			return errBadRequest("fail body is required")
		}
		if err := s.oauth.queueFail(*body.Fail); err != nil {
			return errBadRequest(err.Error())
		}
	}
	return nil
}

// validateTools validates every tool in a list.
func validateTools(tools []Tool) error {
	for _, t := range tools {
		if err := t.Validate(); err != nil {
			return errBadRequest(err.Error())
		}
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
