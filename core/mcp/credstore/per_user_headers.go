package credstore

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/maximhq/bifrost/core/mcp/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// perUserHeadersResolver handles MCPAuthTypePerUserHeaders — each caller's
// upstream API-key / signed-token headers are keyed by (auth_mode, identity,
// mcp_client) in the mcp_per_user_header_credentials table. On miss or stale
// schema, an inline submission flow is initiated and a *MCPAuthRequiredError
// with Kind="headers" is raised so the caller can complete the submission UI.
//
// ConnectionHeaders returns the user-submitted header values. Static admin
// headers are layered separately by AcquireClientConn via
// utils.StaticConfigHeaders (which excludes anything in
// config.PerUserHeaderKeys) so the connect-plugin gate never observes the
// caller's secret values.
//
// RequiresPerCallConnection is true: per-user-headers clients never hold a
// persistent upstream connection; AcquireClientConn opens a fresh ephemeral
// HTTP transport per call using the resolved user headers + plugin-mutated
// static headers.
type perUserHeadersResolver struct {
	provider schemas.MCPHeadersProvider
}

func (r *perUserHeadersResolver) ConnectionHeaders(ctx *schemas.BifrostContext, config *schemas.MCPClientConfig) (http.Header, error) {
	if r.provider == nil {
		return nil, fmt.Errorf("per-user headers requires an MCPHeadersProvider but none is configured")
	}
	if len(config.PerUserHeaderKeys) == 0 {
		return nil, fmt.Errorf("per-user headers client %q has no PerUserHeaderKeys declared (admin config error)", config.Name)
	}

	mode := ctx.MCPAuthMode()
	identity := identityForMCPAuthMode(ctx, mode)
	if identity == "" {
		return nil, fmt.Errorf(
			"per-user headers for %s requires an identity: send a Virtual Key (x-bf-vk), authenticate as a user, or set x-bf-mcp-session-id to any opaque string you'll re-send on subsequent calls",
			config.Name,
		)
	}

	cred, err := r.provider.GetCredentialByMode(ctx, mode, identity, config.ID)
	switch {
	case err == nil:
		// Row present: intersect stored values with the current schema. If any
		// required key is missing on the stored row, the schema has drifted
		// since the user last submitted — surface the same submit-URL flow as
		// "not found" but the underlying row stays (so the UI can prefill
		// known values when the user resubmits).
		if missing := missingRequiredHeaderKeys(config.PerUserHeaderKeys, cred.Headers); len(missing) > 0 {
			return nil, r.buildAuthRequiredError(ctx, config)
		}
		return buildPerUserHeaderValues(config.PerUserHeaderKeys, cred.Headers), nil
	case errors.Is(err, schemas.ErrHeadersCredentialNotFound),
		errors.Is(err, schemas.ErrHeadersCredentialNeedsUpdate):
		return nil, r.buildAuthRequiredError(ctx, config)
	default:
		return nil, fmt.Errorf("failed to load per-user header credential for %s: %w", config.Name, err)
	}
}

func (r *perUserHeadersResolver) RequiresPerCallConnection() bool { return true }

// buildAuthRequiredError creates a pending mcp_per_user_header_flows row
// via the provider, then constructs the inline-401 payload pointing at
// that flow's auth-page URL. Mirrors per_user_oauth.go's call to
// InitiateUserOAuthFlow: the provider mints a temp-token bound to the
// flow ID and embeds it as a `#t=<token>` URL fragment so anonymous
// browser visitors can complete the submission without a dashboard
// session.
func (r *perUserHeadersResolver) buildAuthRequiredError(ctx *schemas.BifrostContext, config *schemas.MCPClientConfig) error {
	baseURL := utils.BuildMCPCallbackBaseURL(ctx)
	if baseURL == "" {
		return fmt.Errorf("per-user headers requires a callback base URL but none is available in context")
	}
	mode := ctx.MCPAuthMode()
	identity := identityForMCPAuthMode(ctx, mode)
	if identity == "" {
		// Defensive — the caller already validated identity before invoking
		// the auth-required path, but keep the guard so a future refactor
		// can't accidentally start flow rows with empty identity columns.
		return fmt.Errorf("per-user headers auth-required flow requires an identity")
	}
	// No identity gate here (see per_user_oauth.go for the rationale). For
	// user-mode flows the submission is verified at the cookie-bearing UI step
	// (flowSubmit → canAccessUserFlow requires the dashboard user to match
	// flow.UserID, and user-mode flows mint no shareable temp token); the flow
	// row created here grants nothing on its own.
	initiation, err := r.provider.InitiateUserSubmissionFlow(ctx, mode, identity, config.ID, baseURL)
	if err != nil {
		return fmt.Errorf("failed to initiate per-user headers submission flow for %s: %w", config.Name, err)
	}
	message := fmt.Sprintf("Authentication required for %s. Visit %s to submit the required headers.", config.Name, initiation.FrontendURL)
	if schemas.MCPAuthURLHasTempTokenFragment(initiation.FrontendURL) {
		message += schemas.MCPAuthTempTokenReminder
	}
	return &schemas.MCPAuthRequiredError{
		Kind:               schemas.MCPAuthRequiredKindHeaders,
		MCPClientID:        config.ID,
		MCPClientName:      config.Name,
		SubmitURL:          initiation.FrontendURL,
		SessionID:          initiation.FlowID,
		RequiredHeaderKeys: append([]string(nil), config.PerUserHeaderKeys...),
		AdminHeaderKeys:    adminHeaderKeyNames(config),
		// Include the URL in the message so plain-text clients (curl, basic
		// SDK wrappers) that don't parse extra_fields still get an actionable
		// hint. Matches per_user_oauth.go's behavior.
		Message: message,
	}
}

// missingRequiredHeaderKeys returns the names of any required header key
// that's absent or whose stored value is empty in storedHeaders.
//
// Both inputs are assumed to be in canonical form (lowercase + trimmed) —
// see the invariant doc on mcputils.CanonicalizeHeaderKey. All write
// boundaries (HTTP create/update, flow submit, config.json load) run
// the inputs through that helper, so exact map lookup here is correct.
// Do NOT add defensive case-folding inside this function: it would mask
// a missed write-side canonicalization rather than catching it.
func missingRequiredHeaderKeys(required []string, storedHeaders map[string]string) []string {
	if len(storedHeaders) == 0 {
		return append([]string(nil), required...)
	}
	var missing []string
	for _, key := range required {
		if v, ok := storedHeaders[key]; !ok || v == "" {
			missing = append(missing, key)
		}
	}
	return missing
}

// buildPerUserHeaderValues constructs the http.Header carrying just the
// user-submitted credential values for the required keys. Keys not declared
// by the current schema are dropped on purpose so a stale row that still
// stores a deprecated key cannot leak it onto the wire.
//
// Required keys and storedHeaders keys are both canonical (lowercase +
// trimmed) by the write-side invariant — see missingRequiredHeaderKeys
// above and mcputils.CanonicalizeHeaderKey. http.Header.Set runs its own
// MIME canonicalization on the way out (so "authorization" becomes
// "Authorization" on the wire), which is what upstream servers expect.
func buildPerUserHeaderValues(required []string, storedHeaders map[string]string) http.Header {
	out := http.Header{}
	for _, key := range required {
		if v, ok := storedHeaders[key]; ok && v != "" {
			out.Set(key, v)
		}
	}
	return out
}

// adminHeaderKeyNames returns the names (no values) of static admin headers
// declared on the MCP client. Surfaced to the submission UI so the user can
// see what context will accompany their request without exposing the values.
func adminHeaderKeyNames(config *schemas.MCPClientConfig) []string {
	if config == nil || len(config.Headers) == 0 {
		return nil
	}
	names := make([]string, 0, len(config.Headers))
	for name := range config.Headers {
		names = append(names, name)
	}
	return names
}
