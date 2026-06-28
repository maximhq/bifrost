package handlers

import (
	"fmt"
	"time"

	"github.com/maximhq/bifrost/framework/temptoken"
)

// mcpAuthScope declares the routes the mcp_auth scope grants access to. The
// flow ID is substituted into {id} at validation time, binding each token to
// exactly one flow.
//
// The canonical scope name lives in framework/temptoken so issuers (e.g. the
// OAuth provider that mints these tokens) and registrants reference a single
// source of truth.
//
// The page also calls /api/version and /api/session/is-auth-enabled — those
// are unconditionally whitelisted in APIMiddleware so they do not need to
// appear here.
// The page makes flowDetail then flowStart, so the token must remain valid for
// multiple requests within its TTL. Invalidation isn't single-use — it happens
// at OAuth completion when CompleteUserOAuthFlow deletes the token by
// resource_id.
var mcpAuthScope = temptoken.Scope{
	Name: temptoken.MCPAuthScopeName,
	AllowedRoutes: []temptoken.RoutePattern{
		{Method: "GET", Path: "/api/oauth/per-user/flows/{id}"},
		{Method: "GET", Path: "/api/oauth/per-user/flows/{id}/start"},
	},
	ResourceIDInPath: "{id}",
	MaxTTL:           15 * time.Minute,
}

// mcpHeadersAuthScope declares the routes the mcp_headers_auth scope grants
// access to. The flow ID is substituted into {id} at validation time,
// binding each token to exactly one headers submission flow. Mirrors
// mcpAuthScope structurally — same TTL, same {id} binding pattern.
//
// The page makes flowDetail then flowSubmit; both routes must remain valid
// for multiple requests within the TTL. Invalidation isn't single-use —
// it happens at submission completion when the submit handler deletes the
// flow row (and the token by resource_id alongside it).
var mcpHeadersAuthScope = temptoken.Scope{
	Name: temptoken.MCPHeadersAuthScopeName,
	AllowedRoutes: []temptoken.RoutePattern{
		{Method: "GET", Path: "/api/mcp/per-user-headers/flows/{id}"},
		{Method: "PUT", Path: "/api/mcp/per-user-headers/flows/{id}"},
	},
	ResourceIDInPath: "{id}",
	MaxTTL:           15 * time.Minute,
}

// RegisterTempTokenScopes registers every scope owned by this handlers
// package on the given service. Called at server startup once the service
// has been constructed. Returns an error if any scope is invalid or has
// already been registered.
func RegisterTempTokenScopes(svc *temptoken.Service) error {
	if svc == nil {
		return fmt.Errorf("temp_token_scopes: service is nil")
	}
	if err := svc.Registry().Register(mcpAuthScope); err != nil {
		return fmt.Errorf("temp_token_scopes: register mcp_auth: %w", err)
	}
	if err := svc.Registry().Register(mcpHeadersAuthScope); err != nil {
		return fmt.Errorf("temp_token_scopes: register mcp_headers_auth: %w", err)
	}
	return nil
}
