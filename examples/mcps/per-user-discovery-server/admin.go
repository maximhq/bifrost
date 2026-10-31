package main

import (
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// The control API lets a test change what the upstream does between Bifrost
// requests. It has no authentication: the fixture is for local test runs.
func (f *fixture) registerAdmin(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/state", f.adminState)
	mux.HandleFunc("GET /admin/calls", f.adminCalls)
	mux.HandleFunc("POST /admin/reset", f.adminReset)
	mux.HandleFunc("POST /admin/profiles/{profile}/tools", f.adminAddTool)
	mux.HandleFunc("DELETE /admin/profiles/{profile}/tools/{tool}", f.adminRemoveTool)
	mux.HandleFunc("POST /admin/faults", f.adminAddFault)
	mux.HandleFunc("DELETE /admin/faults", f.adminClearFaults)
	mux.HandleFunc("POST /admin/revoke", f.adminIdentity(f.revoke))
	mux.HandleFunc("POST /admin/restore", f.adminIdentity(f.restore))
	mux.HandleFunc("POST /admin/expire", f.adminIdentity(f.expire))
}

// profileState is one profile as /admin/state shows it.
type profileState struct {
	Name         string   `json:"name"`
	Summary      string   `json:"summary"`
	Tools        []string `json:"tools"` // as the main workspace sees them now
	Instructions string   `json:"instructions,omitempty"`
	Resources    []string `json:"resources,omitempty"`
	Templates    []string `json:"resource_templates,omitempty"`
	Prompts      []string `json:"prompts,omitempty"`
	PageSize     int      `json:"page_size,omitempty"`
	Faults       []fault  `json:"faults,omitempty"`
}

func (f *fixture) adminState(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	profiles := make([]profileState, 0, len(f.profiles))
	for _, name := range f.profileNames() {
		p := f.profiles[name]
		state := profileState{
			Name:         p.name,
			Summary:      p.summary,
			Tools:        sortedNames(f.toolsLocked(p, "main")),
			Instructions: p.instructions,
			PageSize:     p.pageSize,
			Faults:       p.faults,
		}
		for _, res := range p.resources {
			state.Resources = append(state.Resources, res.resource.URI)
		}
		for _, tmpl := range p.templates {
			state.Templates = append(state.Templates, tmpl.template.URITemplate.Raw())
		}
		for _, pr := range p.prompts {
			state.Prompts = append(state.Prompts, pr.prompt.Name)
		}
		profiles = append(profiles, state)
	}
	revoked := make([]string, 0, len(f.revoked))
	for identity := range f.revoked {
		revoked = append(revoked, identity)
	}
	sort.Strings(revoked)
	faults := slices.Clone(f.faults)
	headersSeen := make(map[string]map[string]string, len(f.lastSeen))
	for identity, headers := range f.lastSeen {
		headersSeen[identity] = maps.Clone(headers)
	}
	body := map[string]any{
		"profiles":       profiles,
		"default":        defaultProfile,
		"runtime_faults": faults,
		"revoked":        revoked,
		"headers_seen":   headersSeen,
	}
	f.mu.Unlock()
	body["tokens"] = f.auth.tokens()
	writeJSON(w, http.StatusOK, body)
}

// adminCalls answers the requests each identity sent, by method; tools/call
// is counted per tool as "tools/call:<tool>". ?identity= narrows it to one.
func (f *fixture) adminCalls(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if identity := r.URL.Query().Get("identity"); identity != "" {
		writeJSON(w, http.StatusOK, f.calls[strings.ToLower(identity)])
		return
	}
	writeJSON(w, http.StatusOK, f.calls)
}

func (f *fixture) adminReset(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	f.resetLocked()
	f.mu.Unlock()
	log.Printf("[admin] reset: runtime tools, faults, revocations and counters cleared")
	writeJSON(w, http.StatusOK, map[string]string{"status": "reset"})
}

func (f *fixture) adminAddTool(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("profile")
	if _, ok := f.profiles[name]; !ok {
		writeAdminError(w, http.StatusNotFound, fmt.Sprintf("no profile %q", name))
		return
	}
	var req struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeAdminError(w, http.StatusBadRequest, `body must be {"name": "...", "description": "..."}`)
		return
	}
	if req.Description == "" {
		req.Description = "A tool added at runtime through the control API."
	}
	f.addTool(name, toolDef{
		tool:    mcp.NewTool(req.Name, mcp.WithDescription(req.Description), mcp.WithString("input", mcp.Description("Anything"))),
		handler: echoHandler,
	})
	log.Printf("[admin] profile %s gained tool %s", name, req.Name)
	writeJSON(w, http.StatusOK, map[string]any{"profile": name, "tools": f.mainTools(name)})
}

func (f *fixture) adminRemoveTool(w http.ResponseWriter, r *http.Request) {
	name, tool := r.PathValue("profile"), r.PathValue("tool")
	if _, ok := f.profiles[name]; !ok {
		writeAdminError(w, http.StatusNotFound, fmt.Sprintf("no profile %q", name))
		return
	}
	f.removeTool(name, tool)
	log.Printf("[admin] profile %s lost tool %s", name, tool)
	writeJSON(w, http.StatusOK, map[string]any{"profile": name, "tools": f.mainTools(name)})
}

func (f *fixture) mainTools(name string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return sortedNames(f.toolsLocked(f.profiles[name], "main"))
}

func (f *fixture) adminAddFault(w http.ResponseWriter, r *http.Request) {
	var flt fault
	if err := json.NewDecoder(r.Body).Decode(&flt); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := validFault(flt); err != nil {
		writeAdminError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, ok := f.profiles[flt.Profile]; flt.Profile != "" && !ok {
		writeAdminError(w, http.StatusNotFound, fmt.Sprintf("no profile %q", flt.Profile))
		return
	}
	flt.Identity = strings.ToLower(flt.Identity)
	f.mu.Lock()
	f.faults = append(f.faults, flt)
	faults := slices.Clone(f.faults)
	f.mu.Unlock()
	log.Printf("[admin] fault added: %+v", flt)
	writeJSON(w, http.StatusOK, map[string]any{"runtime_faults": faults})
}

func (f *fixture) adminClearFaults(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	f.faults = nil
	f.mu.Unlock()
	log.Printf("[admin] runtime faults cleared")
	writeJSON(w, http.StatusOK, map[string]any{"runtime_faults": []fault{}})
}

// adminIdentity serves an endpoint whose body is {"identity": "..."}.
func (f *fixture) adminIdentity(apply func(identity string) map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Identity string `json:"identity"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Identity == "" {
			writeAdminError(w, http.StatusBadRequest, `body must be {"identity": "..."}`)
			return
		}
		writeJSON(w, http.StatusOK, apply(strings.ToLower(req.Identity)))
	}
}

// revoke makes both doors refuse identity with a 401, and the OAuth server
// refuse its refresh tokens, until restore.
func (f *fixture) revoke(identity string) map[string]any {
	f.mu.Lock()
	f.revoked[identity] = true
	f.mu.Unlock()
	log.Printf("[admin] %s revoked", identity)
	return map[string]any{"identity": identity, "revoked": true}
}

func (f *fixture) restore(identity string) map[string]any {
	f.mu.Lock()
	delete(f.revoked, identity)
	f.mu.Unlock()
	log.Printf("[admin] %s restored", identity)
	return map[string]any{"identity": identity, "revoked": false}
}

// expire ends identity's OAuth access tokens now, so its next request through
// the OAuth door is refused until the client refreshes.
func (f *fixture) expire(identity string) map[string]any {
	n := f.auth.expire(identity)
	log.Printf("[admin] expired %d access token(s) of %s", n, identity)
	return map[string]any{"identity": identity, "expired": n}
}

func writeAdminError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
