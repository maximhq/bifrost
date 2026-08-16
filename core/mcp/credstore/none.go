package credstore

import (
	"context"
	"fmt"
	"net/http"

	"github.com/maximhq/bifrost/core/schemas"
)

// noneResolver handles MCPAuthTypeNone — no credentials, no auth header.
// Static config headers are layered separately by the caller via
// utils.StaticConfigHeaders, and per-request extras flow through
// CredStore.RequestHeaders. ConnectionHeaders here is empty by design.
type noneResolver struct{}

func (r *noneResolver) ConnectionHeaders(_ *schemas.BifrostContext, _ *schemas.MCPClientConfig) (http.Header, error) {
	return http.Header{}, nil
}

func (r *noneResolver) RequiresPerCallConnection() bool { return false }

// ForceRefresh is a no-op — there is no credential to refresh.
func (r *noneResolver) ForceRefresh(_ *schemas.BifrostContext, _ *schemas.MCPClientConfig) error {
	return nil
}

// AdminConnectionHeaders delegates to ConnectionHeaders for a client
// currently running per-call (needs_session_stickiness nil/false) — there is
// no separate "admin" credential distinct from the one used for real calls
// (both are empty), so the periodic connection checker's per-call discovery
// cycle (MCPManager.performAdminToolDiscovery) can still run for a
// none-auth client without an error. A sticky client should never reach
// this method at all (it holds a persistent connection instead, discovered
// via connectToMCPClient) — erroring rather than silently delegating turns
// a caller-chain bug into an immediate, loud failure.
func (r *noneResolver) AdminConnectionHeaders(ctx context.Context, config *schemas.MCPClientConfig) (http.Header, error) {
	if !needsSessionStickiness(config) {
		return r.ConnectionHeaders(schemas.NewBifrostContext(ctx, schemas.NoDeadline), config)
	}
	return nil, fmt.Errorf("admin connection headers not supported for auth_type %q on a sticky connection", "none")
}
