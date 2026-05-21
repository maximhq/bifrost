package credstore

import (
	"errors"
	"net/http"
	"strings"

	"github.com/maximhq/bifrost/core/mcp/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// serverOAuthResolver handles MCPAuthTypeOauth — admin-once OAuth where the
// upstream token is shared across all callers. The token is fetched from the
// OAuth2Provider's cache (kept fresh by the background sync worker) and added
// as a Bearer header on top of static config headers + any context-extras.
type serverOAuthResolver struct {
	provider schemas.OAuth2Provider
}

func (r *serverOAuthResolver) ConnectionHeaders(ctx *schemas.BifrostContext, config *schemas.MCPClientConfig) (http.Header, error) {
	headers := utils.GetHeadersForToolExecution(ctx, config)

	if config == nil {
		return headers, nil
	}
	// Reject nil OR empty IDs at the resolver boundary so the caller gets
	// the explicit ErrOAuth2ConfigNotFound rather than a less clear
	// lookup-miss error from GetAccessToken downstream. The ID itself is a
	// server-generated UUID (oauth2/main.go), so whitespace-only values
	// aren't reachable today; only the empty case is worth guarding.
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
	// Validate token format — trim whitespace and reject control characters.
	// Preserves the legacy MCPClientConfig.HttpHeaders behavior.
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return nil, errors.New("access token is empty")
	}
	if strings.ContainsAny(accessToken, "\n\r\t") {
		return nil, errors.New("access token contains invalid characters")
	}
	headers.Set("Authorization", "Bearer "+accessToken)
	return headers, nil
}

func (r *serverOAuthResolver) RequiresPerCallConnection() bool { return false }
