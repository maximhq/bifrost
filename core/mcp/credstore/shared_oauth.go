package credstore

import (
	"net/http"

	"github.com/maximhq/bifrost/core/schemas"
)

// sharedOAuthResolver handles MCPAuthTypeOauth — admin-once OAuth where the
// upstream token is shared across all callers. ConnectionHeaders returns
// only the Authorization header; static config headers are layered
// separately by the caller via utils.StaticConfigHeaders.
type sharedOAuthResolver struct {
	provider schemas.OAuth2Provider
}

func (r *sharedOAuthResolver) ConnectionHeaders(ctx *schemas.BifrostContext, config *schemas.MCPClientConfig) (http.Header, error) {
	if config.OauthConfigID == nil || *config.OauthConfigID == "" {
		return nil, schemas.ErrOAuth2ConfigNotFound
	}
	if r.provider == nil {
		return nil, schemas.ErrOAuth2ProviderNotAvailable
	}
	accessToken, err := r.provider.GetAccessToken(ctx, *config.OauthConfigID)
	if err != nil {
		return nil, err
	}

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+accessToken)
	return headers, nil
}

func (r *sharedOAuthResolver) RequiresPerCallConnection() bool { return false }
