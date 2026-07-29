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

// AdminConnectionHeaders is not supported for this auth type — there is no
// separate "admin" credential distinct from the one used for real calls.
func (r *noneResolver) AdminConnectionHeaders(ctx context.Context, config *schemas.MCPClientConfig) (http.Header, error) {
	return nil, fmt.Errorf("admin connection headers not supported for auth_type %q", "none")
}
