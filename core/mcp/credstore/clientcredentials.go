package credstore

import (
	"context"
	"net/http"

	"github.com/maximhq/bifrost/core/schemas"
)

// clientCredentialsResolver handles MCPAuthTypeClientCredentials: one access
// token, minted with the client_credentials grant from pre-registered
// credentials, shared by every caller. ConnectionHeaders returns only the
// Authorization header; static config headers are layered separately by the
// caller via utils.StaticConfigHeaders.
type clientCredentialsResolver struct {
	provider schemas.OAuth2Provider
}

func (r *clientCredentialsResolver) ConnectionHeaders(ctx *schemas.BifrostContext, config *schemas.MCPClientConfig) (http.Header, error) {
	if r.provider == nil {
		return nil, schemas.ErrOAuth2ProviderNotAvailable
	}
	accessToken, err := r.provider.GetClientCredentialsAccessToken(ctx, config)
	if err != nil {
		return nil, err
	}
	return bearerHeader(accessToken), nil
}

func (r *clientCredentialsResolver) RequiresPerCallConnection() bool { return false }

// ForceRefresh drops the cached token and mints a new one.
func (r *clientCredentialsResolver) ForceRefresh(ctx *schemas.BifrostContext, config *schemas.MCPClientConfig) error {
	return forceRefreshShared(r.provider, ctx, config)
}

// AdminConnectionHeaders: see sharedAdminConnectionHeaders.
func (r *clientCredentialsResolver) AdminConnectionHeaders(ctx context.Context, config *schemas.MCPClientConfig) (http.Header, error) {
	return sharedAdminConnectionHeaders(ctx, config, schemas.MCPAuthTypeClientCredentials, r.ConnectionHeaders)
}
