// Package mcp_headers implements schemas.MCPHeadersProvider against the
// configstore. It is the storage backend for MCPAuthTypePerUserHeaders MCP
// clients — the parallel of framework/oauth2 for the OAuth-flavored per-user
// auth type.
//
// The provider is intentionally storage-only: it does not run upstream
// verification (that's clientmanager.VerifyHeadersConnection) and does not
// build inline-401 errors (that's the credstore resolver). It just maps
// between the table type and the in-memory schema view.
package mcp_headers

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/temptoken"
)

// SubmissionFlowTTL caps how long a pending headers submission flow row
// (and the temp token bound to it) remains valid. Mirrors the OAuth flow
// expiry so the two per-user-auth surfaces feel uniform to the user.
const SubmissionFlowTTL = 15 * time.Minute

// Provider implements schemas.MCPHeadersProvider.
type Provider struct {
	configStore configstore.ConfigStore
	logger      schemas.Logger

	// tempTokens, when non-nil and enabled in client config, is used by
	// InitiateUserSubmissionFlow to mint a short-lived mcp_headers_auth
	// token and embed it in the returned auth-page URL as a fragment.
	// Optional — when nil or disabled, the URL is returned without a
	// fragment and the page works only for callers already authenticated
	// to the dashboard. Held as an atomic.Pointer so it can be installed
	// once at startup and read lock-free on the request path. Mirrors
	// oauth2.OAuth2Provider.tempTokens exactly.
	tempTokens atomic.Pointer[temptoken.Service]
}

// NewProvider constructs a configstore-backed MCPHeadersProvider. Mirrors
// oauth2.NewOAuth2Provider so the wiring in transports/bifrost-http stays
// symmetric between the two per-user auth surfaces.
func NewProvider(configStore configstore.ConfigStore, logger schemas.Logger) *Provider {
	if logger == nil {
		logger = bifrost.NewDefaultLogger(schemas.LogLevelInfo)
	}
	return &Provider{configStore: configStore, logger: logger}
}

// SetTempTokenService installs the temp-token service used by
// InitiateUserSubmissionFlow to mint the mcp_headers_auth token embedded
// in the auth-page URL fragment. Called by server startup once both
// services have been constructed (the provider is built first by
// lib/config.go, the service later by the HTTP transport).
func (p *Provider) SetTempTokenService(svc *temptoken.Service) {
	p.tempTokens.Store(svc)
}

// tempTokenService returns the current temp-token service. Lock-free read
// off the atomic.Pointer.
func (p *Provider) tempTokenService() *temptoken.Service {
	return p.tempTokens.Load()
}

// mcpTempTokenAuthEnabled reports whether MCP per-user-headers auth links may
// include temp-token auth. Reads the same MCPEnableTempTokenAuth client-config
// toggle the OAuth surface uses so the UI switch controls both per-user auth
// kinds uniformly. Mirrors oauth2.OAuth2Provider.mcpTempTokenAuthEnabled.
func (p *Provider) mcpTempTokenAuthEnabled(ctx context.Context) bool {
	if p.configStore == nil {
		return false
	}
	clientConfig, err := p.configStore.GetClientConfig(ctx)
	if err != nil {
		p.logger.Warn("Failed to read MCP temp-token auth setting: %v", err)
		return false
	}
	return clientConfig != nil && clientConfig.MCPEnableTempTokenAuth
}

// GetCredentialByMode looks up the active credential row for the given
// identity dimension. Returns ErrHeadersCredentialNotFound when the row is
// absent so callers can switch on the sentinel.
func (p *Provider) GetCredentialByMode(ctx context.Context, mode schemas.MCPAuthMode, identity, mcpClientID string) (*schemas.MCPHeadersUserCredential, error) {
	if p.configStore == nil {
		return nil, schemas.ErrHeadersCredentialProviderNotAvailable
	}
	if strings.TrimSpace(identity) == "" || strings.TrimSpace(mcpClientID) == "" {
		return nil, schemas.ErrHeadersCredentialNotFound
	}
	row, err := p.configStore.GetMCPPerUserHeaderCredentialByMode(ctx, mode, identity, mcpClientID)
	if err != nil {
		return nil, fmt.Errorf("load mcp per-user header credential: %w", err)
	}
	if row == nil {
		return nil, schemas.ErrHeadersCredentialNotFound
	}
	cred, err := rowToCredential(row)
	if err != nil {
		return nil, err
	}
	return cred, nil
}

// UpsertCredential persists the caller-supplied credential. The caller is
// expected to have run clientmanager.VerifyHeadersConnection before invoking
// this — the provider trusts the values and only handles serialization +
// storage.
func (p *Provider) UpsertCredential(ctx context.Context, cred *schemas.MCPHeadersUserCredential) error {
	if p.configStore == nil {
		return schemas.ErrHeadersCredentialProviderNotAvailable
	}
	if cred == nil {
		return errors.New("nil credential")
	}
	if strings.TrimSpace(cred.MCPClientID) == "" {
		return errors.New("mcp_client_id is required")
	}
	row, err := credentialToRow(cred)
	if err != nil {
		return err
	}
	if err := p.configStore.UpsertMCPPerUserHeaderCredential(ctx, row); err != nil {
		return fmt.Errorf("upsert mcp per-user header credential: %w", err)
	}
	// Propagate the row ID back so the caller can reference it (e.g. revoke later).
	cred.ID = row.ID
	cred.CreatedAt = row.CreatedAt
	cred.UpdatedAt = row.UpdatedAt
	return nil
}

// DeleteCredential removes a credential by primary key.
func (p *Provider) DeleteCredential(ctx context.Context, id string) error {
	if p.configStore == nil {
		return schemas.ErrHeadersCredentialProviderNotAvailable
	}
	if strings.TrimSpace(id) == "" {
		return nil
	}
	if err := p.configStore.DeleteMCPPerUserHeaderCredential(ctx, id); err != nil {
		return fmt.Errorf("delete mcp per-user header credential: %w", err)
	}
	return nil
}

// InitiateUserSubmissionFlow creates a pending mcp_per_user_header_flows
// row keyed by (mode, identity, mcp_client_id), mints a
// mcp_headers_auth temp-token bound to the new row's ID, and returns the
// auth-page URL with the token embedded as a `#t=<token>` fragment.
// Mirrors oauth2.OAuth2Provider.InitiateUserOAuthFlow.
//
// The temp-token mint is best-effort: if it fails, the URL is returned
// without a fragment and remains usable for callers already authenticated
// to the dashboard. The behavior is the same as the OAuth equivalent so
// the two surfaces feel uniform.
func (p *Provider) InitiateUserSubmissionFlow(ctx context.Context, mode schemas.MCPAuthMode, identity, mcpClientID, baseURL string) (*schemas.MCPHeadersFlowInitiation, error) {
	if p.configStore == nil {
		return nil, schemas.ErrHeadersCredentialProviderNotAvailable
	}
	if strings.TrimSpace(mcpClientID) == "" {
		return nil, errors.New("mcp_client_id is required")
	}
	if strings.TrimSpace(identity) == "" {
		return nil, errors.New("identity is required to initiate per-user-headers submission flow")
	}
	if strings.TrimSpace(baseURL) == "" {
		return nil, errors.New("base URL is required to build the submission auth page URL")
	}

	// Single canonical lookup: at most one pending row per (mode, identity,
	// mcp_client). If a pending row already exists, refresh its ExpiresAt
	// in place rather than spawning a duplicate (which a user clicking
	// "Edit values" repeatedly would otherwise produce). Mirrors
	// oauth2.InitiateUserOAuthFlow's find-or-update pattern.
	existing, lookupErr := p.configStore.GetMCPPerUserHeaderFlowByModeIdentityAndMCPClient(ctx, mode, identity, mcpClientID)
	if lookupErr != nil {
		return nil, fmt.Errorf("look up existing header flow: %w", lookupErr)
	}

	now := time.Now()
	var flow *tables.TableMCPPerUserHeaderFlow
	if existing != nil && existing.Status == "pending" {
		// Re-init path: keep the same row, rotate the expiry.
		existing.ExpiresAt = now.Add(SubmissionFlowTTL)
		existing.UpdatedAt = now
		if err := p.configStore.UpdateMCPPerUserHeaderFlow(ctx, existing); err != nil {
			return nil, fmt.Errorf("update mcp per-user header flow: %w", err)
		}
		flow = existing
	} else {
		// Fresh row. Exactly one of (UserID, VirtualKeyID, SessionID) is
		// populated based on mode — same convention as TableOauthUserSession
		// so the sessions UI rendering stays uniform.
		flow = &tables.TableMCPPerUserHeaderFlow{
			ID:          uuid.NewString(),
			MCPClientID: mcpClientID,
			FlowMode:    string(mode),
			Status:      "pending",
			ExpiresAt:   now.Add(SubmissionFlowTTL),
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		switch mode {
		case schemas.MCPAuthModeUser:
			v := identity
			flow.UserID = &v
		case schemas.MCPAuthModeVK:
			v := identity
			flow.VirtualKeyID = &v
		case schemas.MCPAuthModeSession:
			flow.SessionID = identity
		default:
			return nil, fmt.Errorf("unknown auth mode for headers flow: %s", mode)
		}
		if err := p.configStore.CreateMCPPerUserHeaderFlow(ctx, flow); err != nil {
			return nil, fmt.Errorf("create mcp per-user header flow: %w", err)
		}
	}

	// Build the frontend URL: {base}/workspace/mcp-sessions/auth?flow={id}&kind=headers.
	// The auth page hosts both per-user-OAuth and per-user-headers flows on
	// the same URL pattern; `kind=headers` tells the page to call the
	// per-user-headers flow APIs instead of the OAuth ones. The OAuth
	// counterpart omits the `kind` param (default branch).
	frontendURL := strings.TrimRight(baseURL, "/") + "/workspace/mcp-sessions/auth?flow=" + flow.ID + "&kind=headers"

	// Mint a mcp_headers_auth temp token bound to this flow's row ID and
	// embed it in the URL as a fragment so a browser hitting the auth page
	// without a dashboard session can still call the per-user submission
	// endpoints. The fragment never leaves the browser (not in server
	// logs, not in upstream Referer), unlike a query param. Best-effort:
	// mint failure does not fail the flow init.
	//
	// User-mode flows skip the mint: the handler-side identity gate requires
	// caller's user_id to match flow.UserID, which is only populated by
	// normal SCIM enforcement on the auth-page route. Minting a temp token
	// would route the request through the temp-token middleware branch that
	// bypasses cookie resolution, leaving caller user_id empty and the gate
	// would 403 even legitimate users. VK and session-mode flows are
	// intentionally shareable and continue to mint.
	// Gated by the same MCPEnableTempTokenAuth client-config toggle the
	// OAuth surface reads, so the UI switch controls both per-user auth
	// kinds uniformly.
	if svc := p.tempTokenService(); svc != nil && p.mcpTempTokenAuthEnabled(ctx) && mode != schemas.MCPAuthModeUser {
		ttl := time.Until(flow.ExpiresAt)
		if ttl > 0 {
			plaintext, mintErr := svc.Mint(ctx, temptoken.MCPHeadersAuthScopeName, flow.ID, ttl)
			if mintErr != nil {
				p.logger.Warn("Failed to mint mcp_headers_auth temp token for flow %s: %v (link still usable for dashboard-authenticated callers)", flow.ID, mintErr)
			} else {
				frontendURL = frontendURL + "#t=" + plaintext
			}
		}
	}

	return &schemas.MCPHeadersFlowInitiation{
		FlowID:      flow.ID,
		FrontendURL: frontendURL,
		ExpiresAt:   flow.ExpiresAt,
	}, nil
}

// rowToCredential converts the gorm row into the in-memory schema view,
// decrypting and deserializing HeadersJSON in the process.
func rowToCredential(row *tables.TableMCPPerUserHeaderCredential) (*schemas.MCPHeadersUserCredential, error) {
	headers, err := row.GetHeaders()
	if err != nil {
		return nil, err
	}
	return &schemas.MCPHeadersUserCredential{
		ID:           row.ID,
		MCPClientID:  row.MCPClientID,
		AuthMode:     schemas.MCPAuthMode(row.AuthMode),
		UserID:       row.UserID,
		VirtualKeyID: row.VirtualKeyID,
		SessionID:    nilIfEmpty(row.SessionID),
		Headers:      headers,
		Status:       schemas.MCPHeadersUserCredentialStatus(row.Status),
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}, nil
}

// credentialToRow builds a fresh table row from the in-memory credential.
// Sets timestamps when zero so a re-upsert behaves like an update.
func credentialToRow(cred *schemas.MCPHeadersUserCredential) (*tables.TableMCPPerUserHeaderCredential, error) {
	status := string(cred.Status)
	if status == "" {
		status = string(schemas.MCPHeadersUserCredentialStatusActive)
	}
	row := &tables.TableMCPPerUserHeaderCredential{
		ID:           cred.ID,
		MCPClientID:  cred.MCPClientID,
		AuthMode:     string(cred.AuthMode),
		UserID:       cred.UserID,
		VirtualKeyID: cred.VirtualKeyID,
		Status:       status,
		CreatedAt:    cred.CreatedAt,
		UpdatedAt:    cred.UpdatedAt,
	}
	if cred.SessionID != nil {
		row.SessionID = *cred.SessionID
	}
	if row.CreatedAt.IsZero() {
		row.CreatedAt = time.Now()
	}
	row.UpdatedAt = time.Now()
	if err := row.SetHeaders(cred.Headers); err != nil {
		return nil, err
	}
	return row, nil
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
