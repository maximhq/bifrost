package temptoken

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Well-known scope names. The framework reserves the canonical strings here
// so that issuers (e.g. framework/oauth2) and registrants (the handlers
// package that defines the routes) reference a single source of truth. The
// actual Scope definition — routes, TTL, single-use — is owned by the
// transports/handlers layer that registers the scope with a Service.
const (
	// MCPAuthScopeName names the scope that authorizes the MCP per-user OAuth
	// auth page to call the per-user flow endpoints. Bound resource_id is the
	// OAuth flow ID.
	MCPAuthScopeName = "mcp_auth"

	// MCPHeadersAuthScopeName names the scope that authorizes the MCP
	// per-user-headers auth page to call the per-user-headers submission
	// flow endpoints. Bound resource_id is the headers flow ID. Parallel
	// of MCPAuthScopeName for the per-user-headers surface.
	MCPHeadersAuthScopeName = "mcp_headers_auth"
)

// RoutePattern is one (method, path) pair a Scope grants access to. The path
// may include the placeholder declared in the owning Scope's ResourceIDInPath
// field; at validation time the placeholder is substituted with the row's
// resource_id and the result is exact-matched against the request path.
type RoutePattern struct {
	Method string // "GET", "POST", ... — case-insensitive on the wire, normalized to upper-case here.
	Path   string // e.g. "/api/oauth/per-user/flows/{id}"
}

// Scope describes one class of temp token. The Name is the contract key stored
// on each row's scope column; AllowedRoutes + ResourceIDInPath define which
// requests the token authorizes; MaxTTL caps how long Mint will set
// expires_at. Invalidation is by row deletion — callers either let the
// expiry pass, the sweep worker collect, or explicitly Delete /
// DeleteByResourceID when the work the token authorized is complete.
type Scope struct {
	Name             string
	AllowedRoutes    []RoutePattern
	ResourceIDInPath string // e.g. "{id}" — empty means routes have no resource_id substitution
	MaxTTL           time.Duration
}

// matchesRequest reports whether the (method, path) pair satisfies any of the
// scope's AllowedRoutes after substituting ResourceIDInPath with resourceID.
// Method comparison is case-insensitive; path comparison is exact after
// substitution. An empty ResourceIDInPath means the patterns are matched as-is.
func (s Scope) matchesRequest(method, path, resourceID string) bool {
	method = strings.ToUpper(method)
	for _, r := range s.AllowedRoutes {
		if strings.ToUpper(r.Method) != method {
			continue
		}
		expected := r.Path
		if s.ResourceIDInPath != "" {
			expected = strings.ReplaceAll(expected, s.ResourceIDInPath, resourceID)
		}
		if expected == path {
			return true
		}
	}
	return false
}

// Registry holds the process-global set of declared scopes. Scopes register at
// startup; the middleware looks them up on every validation. Registration is
// keyed by Scope.Name and double-registration is an error so misconfiguration
// fails loudly at boot rather than silently overwriting.
type Registry struct {
	mu     sync.RWMutex
	scopes map[string]Scope
}

// NewRegistry constructs an empty registry.
func NewRegistry() *Registry {
	return &Registry{scopes: make(map[string]Scope)}
}

// Register adds a Scope. Returns an error if a scope with the same Name is
// already registered, or if the Scope is invalid (missing Name, missing
// routes, missing placeholder when ResourceIDInPath references one).
func (r *Registry) Register(s Scope) error {
	if s.Name == "" {
		return fmt.Errorf("temptoken: scope name is required")
	}
	if len(s.AllowedRoutes) == 0 {
		return fmt.Errorf("temptoken: scope %q must declare at least one allowed route", s.Name)
	}
	if s.MaxTTL <= 0 {
		return fmt.Errorf("temptoken: scope %q must declare a positive MaxTTL", s.Name)
	}
	if s.ResourceIDInPath != "" {
		for _, r := range s.AllowedRoutes {
			if !strings.Contains(r.Path, s.ResourceIDInPath) {
				return fmt.Errorf("temptoken: scope %q declares ResourceIDInPath %q but route %q %q does not contain it",
					s.Name, s.ResourceIDInPath, r.Method, r.Path)
			}
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.scopes[s.Name]; exists {
		return fmt.Errorf("temptoken: scope %q already registered", s.Name)
	}
	r.scopes[s.Name] = s
	return nil
}

// Lookup returns the Scope registered under the given name, or false if none.
func (r *Registry) Lookup(name string) (Scope, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.scopes[name]
	return s, ok
}
