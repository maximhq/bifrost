package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

type config struct {
	issuer     string // public base URL, also the OAuth issuer
	accessTTL  time.Duration
	refreshTTL time.Duration
	slowDelay  time.Duration
}

// caller is who a request came from, as a door resolved it.
type caller struct {
	door      string // "oauth" or "headers"
	identity  string // the OAuth username, or the name in "key-<name>"
	profile   string
	workspace string // "main" or "beta"
}

type callerKey struct{}

// callerFrom returns the caller a door put on ctx.
func callerFrom(ctx context.Context) caller {
	c, _ := ctx.Value(callerKey{}).(caller)
	return c
}

// instanceKey names one built MCP server: a profile as one workspace sees it.
type instanceKey struct {
	profile   string
	workspace string
}

type instance struct {
	mcp  *server.MCPServer
	http *server.StreamableHTTPServer
}

// fixture is the whole server: the profiles, the MCP servers built from them,
// and the runtime state the control API changes.
type fixture struct {
	cfg      config
	profiles map[string]*profile
	auth     *authServer

	mu        sync.Mutex
	instances map[instanceKey]*instance
	added     map[string][]toolDef         // tools added at runtime, by profile
	removed   map[string]map[string]bool   // tool names removed at runtime, by profile
	faults    []fault                      // runtime faults, on top of each profile's own
	revoked   map[string]bool              // identities both doors refuse
	calls     map[string]map[string]int    // requests by identity, then by method
	lastSeen  map[string]map[string]string // the X-Workspace and X-User-Key an identity last sent, for headers callers
}

func newFixture(cfg config) *fixture {
	f := &fixture{cfg: cfg, profiles: catalog(cfg)}
	f.auth = newAuthServer(cfg, f.isRevoked, f.profileNames())
	f.resetLocked()
	return f
}

// resetLocked clears every runtime change. The OAuth registrations and tokens
// stay, so callers stay signed in.
func (f *fixture) resetLocked() {
	f.instances = map[instanceKey]*instance{}
	f.added = map[string][]toolDef{}
	f.removed = map[string]map[string]bool{}
	f.faults = nil
	f.revoked = map[string]bool{}
	f.calls = map[string]map[string]int{}
	f.lastSeen = map[string]map[string]string{}
}

func (f *fixture) handler() http.Handler {
	mux := http.NewServeMux()
	f.auth.register(mux)
	mux.Handle("/oauth/mcp", f.door("oauth", f.oauthCaller, f.auth.challenge))
	mux.Handle("/headers/mcp", f.door("headers", f.headersCaller, refuseHeaders))
	f.registerAdmin(mux)
	return mux
}

func (f *fixture) profileNames() []string {
	names := make([]string, 0, len(f.profiles))
	for name := range f.profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (f *fixture) isRevoked(identity string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.revoked[identity]
}

// ─── Doors ───────────────────────────────────────────────────────────────────

// oauthCaller resolves a Bearer token the authorization server issued.
func (f *fixture) oauthCaller(r *http.Request) (caller, string) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || strings.TrimSpace(token) == "" {
		return caller{}, "missing Bearer token"
	}
	identity, err := f.auth.identityFor(strings.TrimSpace(token))
	if err != nil {
		return caller{}, err.Error()
	}
	return caller{door: "oauth", identity: identity, profile: profileFor(f.profiles, identity), workspace: "main"}, ""
}

// headersCaller resolves X-User-Key "key-<name>" and X-Workspace.
func (f *fixture) headersCaller(r *http.Request) (caller, string) {
	key := r.Header.Get("X-User-Key")
	identity, ok := strings.CutPrefix(key, "key-")
	if !ok || identity == "" {
		return caller{}, `X-User-Key must be "key-<name>"`
	}
	identity = strings.ToLower(identity)
	workspace := "main"
	if strings.EqualFold(r.Header.Get("X-Workspace"), "beta") {
		workspace = "beta"
	}
	f.mu.Lock()
	f.lastSeen[identity] = map[string]string{"X-User-Key": key, "X-Workspace": r.Header.Get("X-Workspace")}
	f.mu.Unlock()
	return caller{door: "headers", identity: identity, profile: profileFor(f.profiles, identity), workspace: workspace}, ""
}

func refuseHeaders(w http.ResponseWriter, reason string) {
	http.Error(w, "unauthorized: "+reason, http.StatusUnauthorized)
}

// door serves one front door: it resolves the caller, refuses a revoked one,
// counts the request, applies any fault, and hands the request to the MCP
// server built for the caller's profile.
func (f *fixture) door(name string, resolve func(*http.Request) (caller, string), refuse func(http.ResponseWriter, string)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		msg := readMessage(r)
		c, reason := resolve(r)
		if reason == "" && f.isRevoked(c.identity) {
			reason = fmt.Sprintf("%s was revoked through /admin/revoke", c.identity)
		}
		if reason != "" {
			log.Printf("[%s] %s -> 401 (%s)", name, msg.label(), reason)
			refuse(w, reason)
			return
		}
		f.count(c.identity, msg)

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			log.Printf("[%s] identity=%s profile=%s workspace=%s %s -> %d (%s)",
				name, c.identity, c.profile, c.workspace, msg.label(), rec.status, time.Since(start).Round(time.Millisecond))
		}()
		if f.applyFaults(rec, r, c, msg, refuse) {
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), callerKey{}, c))
		f.instance(c.profile, c.workspace).http.ServeHTTP(rec, r)
	})
}

// message is what the door reads of a JSON-RPC request body.
type message struct {
	method string          // the JSON-RPC method, or the HTTP method for a body-less request
	id     json.RawMessage // nil for a notification
	tool   string          // params.name of a tools/call
}

func (m message) label() string {
	if m.tool != "" {
		return m.method + ":" + m.tool
	}
	return m.method
}

// readMessage reads the JSON-RPC message in r's body and puts the body back.
// A batch is labelled by its first message.
func readMessage(r *http.Request) message {
	if r.Method != http.MethodPost || r.Body == nil {
		return message{method: r.Method}
	}
	body, err := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err != nil {
		return message{method: "unreadable"}
	}
	var raw struct {
		Method string          `json:"method"`
		ID     json.RawMessage `json:"id"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var batch []json.RawMessage
		if json.Unmarshal(trimmed, &batch) == nil && len(batch) > 0 {
			trimmed = batch[0]
		}
	}
	if json.Unmarshal(trimmed, &raw) != nil || raw.Method == "" {
		return message{method: "response"}
	}
	m := message{method: raw.Method, id: raw.ID}
	if raw.Method == "tools/call" {
		m.tool = raw.Params.Name
	}
	return m
}

func (f *fixture) count(identity string, msg message) {
	f.mu.Lock()
	defer f.mu.Unlock()
	byMethod := f.calls[identity]
	if byMethod == nil {
		byMethod = map[string]int{}
		f.calls[identity] = byMethod
	}
	byMethod[msg.label()]++
}

// statusRecorder keeps the status for the log line. It passes Flush through,
// which the MCP server needs to stream.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// ─── Faults ──────────────────────────────────────────────────────────────────

const (
	faultError          = "error"            // a JSON-RPC internal error answer
	faultMethodNotFound = "method_not_found" // a JSON-RPC method-not-found answer, as a server that lacks the method gives
	faultHTTP500        = "http500"          // a bare 500
	faultHTTP401        = "http401"          // a 401 as the door refuses a credential, even a valid one
	faultDelay          = "delay"            // wait delay_ms, then answer normally
)

// fault makes requests that match it fail or wait.
type fault struct {
	Profile  string `json:"profile,omitempty"`  // "" matches every profile
	Identity string `json:"identity,omitempty"` // "" matches every identity
	Method   string `json:"method,omitempty"`   // a JSON-RPC method such as tools/list; "" matches every request
	Tool     string `json:"tool,omitempty"`     // for tools/call, the tool name; "" matches every tool
	Kind     string `json:"kind"`               // error | method_not_found | http500 | http401 | delay
	DelayMS  int    `json:"delay_ms,omitempty"` // for delay
}

func (flt fault) matches(c caller, msg message) bool {
	return (flt.Profile == "" || flt.Profile == c.profile) &&
		(flt.Identity == "" || flt.Identity == c.identity) &&
		(flt.Method == "" || flt.Method == msg.method) &&
		(flt.Tool == "" || flt.Tool == msg.tool)
}

func validFault(flt fault) error {
	switch flt.Kind {
	case faultError, faultMethodNotFound, faultHTTP500, faultHTTP401:
		return nil
	case faultDelay:
		if flt.DelayMS <= 0 {
			return fmt.Errorf("a delay fault needs delay_ms > 0")
		}
		return nil
	default:
		return fmt.Errorf("kind must be one of error, method_not_found, http500, http401, delay; got %q", flt.Kind)
	}
}

// applyFaults applies the profile's own faults, then the runtime ones, to a
// request. Delays add up and the request goes on; the first failing fault
// answers it, and applyFaults reports true.
func (f *fixture) applyFaults(w http.ResponseWriter, r *http.Request, c caller, msg message, refuse func(http.ResponseWriter, string)) bool {
	f.mu.Lock()
	faults := append(slices.Clone(f.profiles[c.profile].faults), f.faults...)
	f.mu.Unlock()
	for _, flt := range faults {
		if !flt.matches(c, msg) {
			continue
		}
		switch flt.Kind {
		case faultDelay:
			select {
			case <-time.After(time.Duration(flt.DelayMS) * time.Millisecond):
			case <-r.Context().Done():
				return true
			}
		case faultHTTP500:
			http.Error(w, "injected fault: "+msg.label(), http.StatusInternalServerError)
			return true
		case faultHTTP401:
			refuse(w, "injected fault: "+msg.label())
			return true
		case faultError, faultMethodNotFound:
			if msg.id == nil {
				continue // a notification has no answer to fail
			}
			code, text := mcp.INTERNAL_ERROR, fmt.Sprintf("injected fault: %s for profile %s", msg.label(), c.profile)
			if flt.Kind == faultMethodNotFound {
				code, text = mcp.METHOD_NOT_FOUND, "Method not found: "+msg.method
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      msg.id,
				"error":   map[string]any{"code": code, "message": text},
			})
			return true
		}
	}
	return false
}

// ─── MCP servers ─────────────────────────────────────────────────────────────

// instance returns the MCP server for a profile as a workspace sees it,
// building it on first use from the profile and the runtime changes to it.
func (f *fixture) instance(name, workspace string) *instance {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := instanceKey{profile: name, workspace: workspace}
	if inst := f.instances[key]; inst != nil {
		return inst
	}
	p := f.profiles[name]
	opts := []server.ServerOption{server.WithToolCapabilities(true)}
	if p.instructions != "" {
		opts = append(opts, server.WithInstructions(p.instructions))
	}
	if len(p.resources) > 0 || len(p.templates) > 0 {
		opts = append(opts, server.WithResourceCapabilities(false, true))
	}
	if len(p.prompts) > 0 {
		opts = append(opts, server.WithPromptCapabilities(true))
	}
	if p.pageSize > 0 {
		opts = append(opts, server.WithPaginationLimit(p.pageSize))
	}
	s := server.NewMCPServer("per-user-discovery-server", "1.0.0", opts...)
	for _, t := range f.toolsLocked(p, workspace) {
		s.AddTool(t.tool, t.handler)
	}
	for _, res := range p.resources {
		s.AddResource(res.resource, textResource(res.text))
	}
	for _, tmpl := range p.templates {
		s.AddResourceTemplate(tmpl.template, templateResource)
	}
	for _, pr := range p.prompts {
		s.AddPrompt(pr.prompt, textPrompt(pr.text))
	}

	httpOpts := []server.StreamableHTTPOption{
		server.WithHTTPContextFunc(func(ctx context.Context, r *http.Request) context.Context {
			return context.WithValue(ctx, callerKey{}, callerFrom(r.Context()))
		}),
		server.WithDisableLocalhostProtection(true),
	}
	inst := &instance{mcp: s, http: server.NewStreamableHTTPServer(s, httpOpts...)}
	f.instances[key] = inst
	return inst
}

// toolsLocked returns the tools a profile serves a workspace now: its own,
// less the removed, plus beta_search for the beta workspace, plus the added.
// The added come last, so one named like a built-in replaces it, as addTool
// does on a server already built.
func (f *fixture) toolsLocked(p *profile, workspace string) []toolDef {
	var tools []toolDef
	for _, t := range p.tools {
		if !f.removed[p.name][t.tool.Name] {
			tools = append(tools, t)
		}
	}
	if beta := betaTool(); workspace == "beta" && !f.removed[p.name][beta.tool.Name] {
		tools = append(tools, beta)
	}
	return append(tools, f.added[p.name]...)
}

// addTool adds a tool to a profile, including the servers already built for
// it, which tell their open sessions the list changed.
func (f *fixture) addTool(profileName string, def toolDef) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.removed[profileName] != nil {
		delete(f.removed[profileName], def.tool.Name)
	}
	f.added[profileName] = append(slices.DeleteFunc(f.added[profileName], func(t toolDef) bool { return t.tool.Name == def.tool.Name }), def)
	for key, inst := range f.instances {
		if key.profile == profileName {
			inst.mcp.AddTool(def.tool, def.handler)
		}
	}
}

// removeTool removes a tool from a profile the same way.
func (f *fixture) removeTool(profileName, toolName string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.added[profileName] = slices.DeleteFunc(f.added[profileName], func(t toolDef) bool { return t.tool.Name == toolName })
	if f.removed[profileName] == nil {
		f.removed[profileName] = map[string]bool{}
	}
	f.removed[profileName][toolName] = true
	for key, inst := range f.instances {
		if key.profile == profileName {
			inst.mcp.DeleteTools(toolName)
		}
	}
}

func textResource(text string) server.ResourceHandlerFunc {
	return func(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		return []mcp.ResourceContents{mcp.TextResourceContents{
			URI:      req.Params.URI,
			MIMEType: "text/plain",
			Text:     fmt.Sprintf("%s (read by %s)", text, callerFrom(ctx).identity),
		}}, nil
	}
}

func templateResource(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	return []mcp.ResourceContents{mcp.TextResourceContents{
		URI:      req.Params.URI,
		MIMEType: "text/plain",
		Text:     fmt.Sprintf("note %s (read by %s)", strings.TrimPrefix(req.Params.URI, "notes://note/"), callerFrom(ctx).identity),
	}}, nil
}

func textPrompt(text string) server.PromptHandlerFunc {
	return func(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		args, _ := json.Marshal(req.Params.Arguments)
		return mcp.NewGetPromptResult(req.Params.Name, []mcp.PromptMessage{
			mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent(fmt.Sprintf("%s Arguments: %s. Caller: %s.", text, args, callerFrom(ctx).identity))),
		}), nil
	}
}
