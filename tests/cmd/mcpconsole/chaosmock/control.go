package chaosmock

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
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
	// ProtocolVersions are the MCP protocol versions the server speaks, newest first.
	ProtocolVersions []string `json:"protocol_versions"`
	// StdioPID is the child process bridging this server over stdin and
	// stdout, or 0 when none has registered.
	StdioPID int `json:"stdio_pid"`
	// StdioEnv is the environment that child registered.
	StdioEnv map[string]string `json:"stdio_env,omitempty"`
	// OAuth is the authorization server's state, present while OAuth is on.
	OAuth *OAuthState `json:"oauth,omitempty"`
	// REST is the REST surface's routes and call log, present once used.
	REST *RESTState `json:"rest,omitempty"`
	// LLM is the LLM surface's models and scenarios, present once used.
	LLM *LLMState `json:"llm,omitempty"`
	// Prompts, Resources and ResourceTemplates are what the server offers besides tools, and Reads
	// the prompts/get, resources/read and completion/complete requests it answered.
	Prompts           []Prompt           `json:"prompts,omitempty"`
	Resources         []Resource         `json:"resources,omitempty"`
	ResourceTemplates []ResourceTemplate `json:"resource_templates,omitempty"`
	Reads             []ReadEntry        `json:"reads,omitempty"`

	// Views, PromptViews, ResourceViews and TemplateViews are the
	// per-identity overlays, keyed by identity.
	Views         map[string][]Tool             `json:"views,omitempty"`
	PromptViews   map[string][]Prompt           `json:"prompt_views,omitempty"`
	ResourceViews map[string][]Resource         `json:"resource_views,omitempty"`
	TemplateViews map[string][]ResourceTemplate `json:"template_views,omitempty"`
	// IdentityInstructions and IdentityFaults replace the server-wide
	// instructions and faults for one caller.
	IdentityInstructions map[string]string `json:"identity_instructions,omitempty"`
	IdentityFaults       map[string]Faults `json:"identity_faults,omitempty"`
	// Gates are the barriers set on this server, sorted by name.
	Gates []GateState `json:"gates,omitempty"`
}

// State captures a server's current state.
func (s *Server) State() State {
	var oauth *OAuthState
	if s.auth.get().OAuth != nil {
		oauth = s.oauth.snapshot()
	}
	prompts, resources, templates, reads := s.prompts.snapshot()
	promptViews, resourceViews, templateViews := s.prompts.views()
	return State{
		Prompts:           prompts,
		Resources:         resources,
		ResourceTemplates: templates,
		Reads:             reads,
		Name:              s.name,
		Tools:             s.tools.List(""),
		Revision:          s.tools.Revision(),
		Faults:            s.faults.peek(),
		Auth:              s.auth.get(),
		Counters:          s.counters.Snapshot(),
		Sessions:          s.sessions.count(),
		Streams:           s.streams.count(),
		Instructions:      s.Instructions(),
		ProtocolVersions:  s.ProtocolVersions(),
		StdioPID:          s.StdioPID(),
		StdioEnv:          s.StdioEnv(),
		OAuth:             oauth,
		REST:              s.rest.snapshot(),
		LLM:               s.llm.snapshot(),

		Views:         s.tools.views(),
		PromptViews:   promptViews,
		ResourceViews: resourceViews,
		TemplateViews: templateViews,

		IdentityInstructions: s.IdentityInstructions(),
		IdentityFaults:       s.faults.identities(),
		Gates:                s.gates.snapshot(),
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
		body := map[string]any{
			"base_url":    f.URL(),
			"servers":     states,
			"connections": f.Connections(),
		}
		if tlsURL, certPEM := f.tlsListener(); tlsURL != "" {
			body["tls_url"] = tlsURL
			body["tls_cert_pem"] = certPEM
		}
		writeJSON(w, http.StatusOK, body)
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
	if strings.TrimSuffix(verb, "/") == "llm/requests" {
		s.serveLLMRequests(w, r)
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
	// Scenario, Turns, Then and Models drive the llm/* verbs.
	Scenario string    `json:"scenario,omitempty"`
	Turns    []LLMTurn `json:"turns,omitempty"`
	Then     *LLMTurn  `json:"then,omitempty"`
	Models   []string  `json:"models,omitempty"`
	// Prompts, Resources and Templates drive the prompts/*, resources/* and
	// templates/* verbs.
	Prompts   []Prompt           `json:"prompts,omitempty"`
	Resources []Resource         `json:"resources,omitempty"`
	Templates []ResourceTemplate `json:"templates,omitempty"`
	// Versions drives the versions verb.
	Versions []string `json:"versions,omitempty"`
	// Gate is the gate gates/set adds; gates/open and gates/clear take Name.
	Gate *Gate `json:"gate,omitempty"`
}

// applyControl applies one per-server control verb, refusing an unknown verb or a body it cannot use.
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
		// serve no instructions. With an identity the verb sets or clears
		// that identity's own text, and clearing text it never had is refused.
		switch {
		case body.Identity != "" && body.Clear:
			if !s.ClearIdentityInstructions(body.Identity) {
				return errBadRequest(fmt.Sprintf("%q has no instructions of its own to clear", body.Identity))
			}
		case body.Identity != "" && body.Text != "":
			s.SetIdentityInstructions(body.Identity, body.Text)
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
	case "faults/identity":
		switch {
		case body.Identity == "":
			return errBadRequest("faults/identity needs an identity")
		case body.Clear:
			if !s.faults.clearIdentity(body.Identity) {
				return errBadRequest(fmt.Sprintf("%q has no faults of its own to clear", body.Identity))
			}
		case body.Faults == nil:
			return errBadRequest("faults/identity takes faults, or clear to remove them")
		default:
			s.faults.setIdentity(body.Identity, *body.Faults)
		}
	case "gates/set":
		if body.Gate == nil {
			return errBadRequest("gate body is required")
		}
		if err := body.Gate.Validate(); err != nil {
			return errBadRequest(err.Error())
		}
		s.gates.set(*body.Gate)
	case "gates/open":
		if err := s.gates.openGate(body.Name); err != nil {
			return errBadRequest(err.Error())
		}
	case "gates/clear":
		if err := s.gates.remove(body.Name); err != nil {
			return errBadRequest(err.Error())
		}
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
	case "prompts/set", "resources/set", "templates/set":
		// The set verbs replace the base lists; an identity gets its own with <kind>/overlay.
		kind := strings.TrimSuffix(strings.TrimSuffix(verb, "/"), "/set")
		if body.Identity != "" || body.Clear {
			return errBadRequest(kind + "/set replaces the base list; give one identity its own with " + kind + "/overlay")
		}
		var err error
		switch kind {
		case "prompts":
			err = s.prompts.setPrompts(body.Prompts)
		case "resources":
			err = s.prompts.setResources(body.Resources, body.Templates)
		default:
			err = s.prompts.setTemplates(body.Templates)
		}
		if err != nil {
			return errBadRequest(err.Error())
		}
	case "prompts/overlay", "resources/overlay", "templates/overlay":
		return s.prompts.applyOverlay(strings.TrimSuffix(strings.TrimSuffix(verb, "/"), "/overlay"), body)
	case "versions":
		if err := s.versions.set(body.Versions); err != nil {
			return errBadRequest(err.Error())
		}
	case "counters/reset":
		s.prompts.clearReads()
		s.counters.Reset()
		s.oauth.resetCounters()
		s.rest.clearLog()
		s.llm.clearLog()
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
	case "llm/script", "llm/append":
		return s.applyLLMScript(strings.TrimSuffix(verb, "/") == "llm/script", body)
	case "llm/clear":
		s.llm.clear(body.Scenario)
	case "llm/models":
		if len(body.Models) == 0 {
			return errBadRequest("models must name at least one model")
		}
		s.llm.setModels(body.Models)
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

// Error returns the refusal's text.
func (e errBadRequest) Error() string { return string(e) }

// applyLLMScript handles llm/script, which replaces a scenario's turns and
// then turn and clears its logged requests, and llm/append, which adds turns
// and keeps them. Both refuse a scenario key the request marker cannot carry
// and a turn that cannot be rendered.
func (s *Server) applyLLMScript(replace bool, body controlBody) error {
	if !scenarioMarker.MatchString("[scenario:" + body.Scenario + "]") {
		return errBadRequest("scenario must be 1 to 64 letters, digits, '.', '_' or '-'")
	}
	if !replace && body.Then != nil {
		return errBadRequest("then is set by llm/script only")
	}
	if replace && len(body.Turns) == 0 && body.Then == nil {
		return errBadRequest("llm/script needs turns or a then turn")
	}
	turns := body.Turns
	if body.Then != nil {
		turns = append(append([]LLMTurn{}, turns...), *body.Then)
	}
	for i, t := range turns {
		if err := t.Validate(); err != nil {
			return errBadRequest(fmt.Sprintf("turn %d: %v", i, err))
		}
	}
	s.llm.script(body.Scenario, body.Turns, body.Then, replace)
	return nil
}

// serveLLMRequests answers GET llm/requests: the logged requests of one
// scenario (?scenario=<key>), or of every scenario when none is named, after
// sequence number ?since=<n>, oldest first.
func (s *Server) serveLLMRequests(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "llm/requests is read with GET", http.StatusMethodNotAllowed)
		return
	}
	since := 0
	if v := r.URL.Query().Get("since"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			http.Error(w, "since must be a non-negative integer", http.StatusBadRequest)
			return
		}
		since = n
	}
	scenario := r.URL.Query().Get("scenario")
	writeJSON(w, http.StatusOK, map[string]any{"scenario": scenario, "requests": s.llm.requests(scenario, since)})
}
