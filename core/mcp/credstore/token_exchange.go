package credstore

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/maximhq/bifrost/core/schemas"
)

// tokenExchangeResolver handles MCPAuthTypeTokenExchange — the caller's
// identity-provider token is exchanged for a short-lived upstream token
// scoped to the client's configured audience. Strictly delegated: every
// tool call presents the caller's own identity upstream, and there is no
// interactive flow — on a missing or rejected caller token the raised
// *MCPAuthRequiredError (Kind "exchange") tells the caller to fix the
// request credential and retry.
//
// ConnectionHeaders returns only the Authorization header — static config
// headers are layered separately by AcquireClientConn / clientmanager via
// utils.StaticConfigHeaders so the connect-plugin gate never observes the
// bearer token.
//
// RequiresPerCallConnection is true: exchanged tokens are caller-scoped, so
// clients never hold a persistent upstream connection; AcquireClientConn
// opens a fresh ephemeral HTTP transport per call.
type tokenExchangeResolver struct {
	provider schemas.OAuth2Provider
}

func (r *tokenExchangeResolver) ConnectionHeaders(ctx *schemas.BifrostContext, config *schemas.MCPClientConfig) (http.Header, error) {
	if r.provider == nil {
		return nil, fmt.Errorf("token exchange requires an OAuth2Provider but none is configured")
	}

	accessToken, err := r.provider.GetExchangedAccessToken(ctx, config)
	if err != nil {
		if errors.Is(err, schemas.ErrExchangeSubjectTokenMissing) {
			return nil, &schemas.MCPAuthRequiredError{
				Kind:                schemas.MCPAuthRequiredKindExchange,
				MCPClientID:         config.ID,
				MCPClientName:       config.Name,
				SubjectTokenMissing: true,
				Message:             fmt.Sprintf("Authentication required for %s: this server uses your identity token, so the request must be authenticated with one. Retry with your identity-provider credential.", config.Name),
			}
		}
		var rejected *schemas.TokenExchangeRejectedError
		if errors.As(err, &rejected) {
			return nil, &schemas.MCPAuthRequiredError{
				Kind:          schemas.MCPAuthRequiredKindExchange,
				MCPClientID:   config.ID,
				MCPClientName: config.Name,
				ExchangeError: rejected.Detail,
				Message:       fmt.Sprintf("The identity provider declined to issue a token for %s on your behalf. Re-authenticate and retry; if the problem persists, ask an administrator to check your access to this server.", config.Name),
			}
		}
		return nil, fmt.Errorf("failed to get exchanged access token for MCP server %s: %w", config.Name, err)
	}

	return bearerHeader(accessToken), nil
}

func (r *tokenExchangeResolver) RequiresPerCallConnection() bool { return true }

// ForceRefresh evicts the caller's cached exchanged token and performs a
// fresh exchange. Resolution (mode/identity from ctx, fallback selection)
// happens inside the provider — see
// schemas.OAuth2Provider.ForceRefreshAccessToken.
func (r *tokenExchangeResolver) ForceRefresh(ctx *schemas.BifrostContext, config *schemas.MCPClientConfig) error {
	if r.provider == nil {
		return fmt.Errorf("token exchange requires an OAuth2Provider but none is configured")
	}
	return r.provider.ForceRefreshAccessToken(ctx, config)
}

// AdminConnectionHeaders resolves the retained admin bootstrap token (see
// GetAdminAccessToken's doc comment) for periodic tool-discovery refresh,
// exactly like the per-user OAuth resolver: the credential persisted at
// verification, lazily renewed through the provider's unified refresh path.
func (r *tokenExchangeResolver) AdminConnectionHeaders(ctx context.Context, config *schemas.MCPClientConfig) (http.Header, error) {
	if r.provider == nil {
		return nil, fmt.Errorf("token exchange requires an OAuth2Provider but none is configured")
	}
	accessToken, err := r.provider.GetAdminAccessToken(ctx, config.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to get admin access token for MCP server %s: %w", config.Name, err)
	}
	return bearerHeader(accessToken), nil
}

func bearerHeader(token string) http.Header {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+token)
	return headers
}
