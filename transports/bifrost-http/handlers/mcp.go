// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains MCP (Model Context Protocol) tool execution handlers.
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fasthttp/router"
	"github.com/google/uuid"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/mcp"
	mcputils "github.com/maximhq/bifrost/core/mcp/utils"
	"github.com/maximhq/bifrost/core/network"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/oauth2"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
	"gorm.io/gorm"
)

type MCPManager interface {
	AddMCPClient(ctx context.Context, clientConfig *schemas.MCPClientConfig) error
	RemoveMCPClient(ctx context.Context, id string) error
	UpdateMCPClient(ctx context.Context, id string, updatedConfig *schemas.MCPClientConfig) error
	// UpdateMCPClientCredentials reconnects an existing MCP client using updated headers
	UpdateMCPClientCredentials(ctx context.Context, id string, newConfig *schemas.MCPClientConfig) error
	ReconnectMCPClient(ctx context.Context, id string) error
	// RefreshMCPClientTools re-discovers a client's tools from its upstream
	// server on demand and reports how many it serves afterwards. Unlike
	// ReconnectMCPClient it applies to per-call clients too, which have no
	// persistent connection and so no other way to pick up an upstream
	// tool-set change before the periodic checker's next tick.
	RefreshMCPClientTools(ctx context.Context, id string) (int, error)
	// CloseAndMarkNeedsReauth closes a shared client's live upstream
	// connection and flips it to needs_reauth, without attempting a new
	// dial. Used after OAuth credential rotation.
	CloseAndMarkNeedsReauth(ctx context.Context, id string) error
	DisableMCPClient(ctx context.Context, id string) error
	EnableMCPClient(ctx context.Context, id string) error
	// VerifyPerUserOAuthConnection verifies an MCP server using a temporary access
	// token and discovers available tools. The connection is closed after verification.
	VerifyPerUserOAuthConnection(ctx context.Context, config *schemas.MCPClientConfig, accessToken string) (*schemas.MCPDiscovery, error)
	// VerifyHeadersConnection verifies an MCP server using a caller-supplied set
	// of header values (admin sample or user-submitted) and discovers available
	// tools. The connection is closed after verification. Mirrors
	// VerifyPerUserOAuthConnection's role for MCPAuthTypePerUserHeaders.
	VerifyHeadersConnection(ctx context.Context, config *schemas.MCPClientConfig, userHeaders map[string]string) (*schemas.MCPDiscovery, error)
	// SetClientDiscovery installs discovery on an existing client.
	SetClientDiscovery(clientID string, discovery *schemas.MCPDiscovery)
	// RequiresPerCallConnection reports whether config resolves to a
	// per-call connection (true) or a persistent shared one (false), taking
	// auth type, connection type, and needs_session_stickiness into account
	// together.
	RequiresPerCallConnection(config *schemas.MCPClientConfig) bool
}

// MCPHandler manages HTTP requests for MCP tool operations
type MCPHandler struct {
	client            *bifrost.Bifrost
	store             *lib.Config
	mcpManager        MCPManager
	governanceManager GovernanceManager
	oauthHandler      *OAuthHandler
	// mcpCredentialCacheManager invalidates cached per-user credentials
	// (OAuth access tokens and header credentials) after mutations that
	// rewrite or delete their rows through the configstore (credential
	// rotation, needs_update schema flips, access reconciliation). Always
	// wired by the server; see the interface doc for the non-nil requirement.
	mcpCredentialCacheManager MCPCredentialCacheManager
}

// NewMCPHandler creates a new MCP handler instance
func NewMCPHandler(
	mcpManager MCPManager,
	governanceManager GovernanceManager,
	client *bifrost.Bifrost,
	store *lib.Config,
	oauthHandler *OAuthHandler,
	mcpCredentialCacheManager MCPCredentialCacheManager,
) *MCPHandler {
	return &MCPHandler{
		client:                    client,
		store:                     store,
		mcpManager:                mcpManager,
		governanceManager:         governanceManager,
		oauthHandler:              oauthHandler,
		mcpCredentialCacheManager: mcpCredentialCacheManager,
	}
}

// RegisterRoutes registers all MCP-related routes
func (h *MCPHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/api/mcp/clients", lib.ChainMiddlewares(h.getMCPClients, middlewares...))
	r.GET("/api/mcp/library", lib.ChainMiddlewares(h.getMCPLibrary, middlewares...))
	r.GET("/api/mcp/library/filterdata", lib.ChainMiddlewares(h.getMCPLibraryFilterData, middlewares...))
	r.POST("/api/mcp/library/force-sync", lib.ChainMiddlewares(h.forceSyncMCPLibrary, middlewares...))
	r.POST("/api/mcp/library", lib.ChainMiddlewares(h.createMCPLibraryEntry, middlewares...))
	r.DELETE("/api/mcp/library/{id}", lib.ChainMiddlewares(h.deleteMCPLibraryEntry, middlewares...))
	r.POST("/api/mcp/client", lib.ChainMiddlewares(h.addMCPClient, middlewares...))
	r.POST("/api/mcp/openapi/preview", lib.ChainMiddlewares(h.previewOpenAPISpec, middlewares...))
	r.GET("/api/mcp/client/{id}/openapi-spec", lib.ChainMiddlewares(h.getMCPClientOpenAPISpec, middlewares...))
	r.PUT("/api/mcp/client/{id}", lib.ChainMiddlewares(h.updateMCPClient, middlewares...))
	r.DELETE("/api/mcp/client/{id}", lib.ChainMiddlewares(h.deleteMCPClient, middlewares...))
	r.POST("/api/mcp/client/{id}/reconnect", lib.ChainMiddlewares(h.reconnectMCPClient, middlewares...))
	r.POST("/api/mcp/client/{id}/refresh-tools", lib.ChainMiddlewares(h.refreshMCPClientTools, middlewares...))
	r.POST("/api/mcp/client/{id}/complete-oauth", lib.ChainMiddlewares(h.completeMCPClientOAuth, middlewares...))
	r.POST("/api/mcp/client/{id}/initiate-verification", lib.ChainMiddlewares(h.initiateMCPClientVerification, middlewares...))
	r.POST("/api/mcp/client/{id}/reauthorize", lib.ChainMiddlewares(h.reauthorizeMCPClient, middlewares...))
	r.POST("/api/mcp/client/{id}/reregister", lib.ChainMiddlewares(h.reregisterMCPClient, middlewares...))
	r.POST("/api/mcp/client/{id}/verify-headers", lib.ChainMiddlewares(h.verifyMCPClientHeaders, middlewares...))
	r.POST("/api/mcp/client/{id}/verify-exchange", lib.ChainMiddlewares(h.verifyMCPClientExchange, middlewares...))
}

// runOAuthBootstrap kicks off the shared-OAuth flow for an MCP client and
// returns the OAuth provider's authorize URL.
//
// Behavior:
//   - Computes the OAuth callback redirect URI from the request context,
//     honouring any admin-configured external base URL via
//     BuildBaseURL → GetMCPExternalClientURL.
//   - Calls InitiateOAuthFlow, which generates the CSRF state + PKCE
//     verifier/challenge, runs RFC 8414 metadata discovery on serverURL
//     when authorize_url/token_url are missing, runs RFC 7591 dynamic
//     client registration when client_id is missing, and inserts a fresh
//     oauth_configs row in status='pending' with a 15-minute expiry.
//
// Callers (addMCPClient via the UI Create flow, and
// initiateMCPClientVerification via the config.json bootstrap flow)
// exercise the exact same OAuth dance and differ only in how they manage
// the config_mcp_clients row lifecycle. This helper centralises the
// shared half so the two flows cannot drift (e.g. a discovery fix in one
// caller missed by the other).
//
// Inputs: the resolved OAuth config, the MCP server URL
// (config.ConnectionString.GetValue()), and the client's RequirePublicTarget,
// which decides whether the OAuth chain may reach allowlisted private
// addresses. The returned flowInitiation
// carries oauth_config_id, authorize_url, state, and expires_at.
//
// This helper does NOT touch config_mcp_clients or
// oauth_configs.mcp_client_config_json — row-lifecycle management is the
// caller's concern.
func (h *MCPHandler) runOAuthBootstrap(ctx *fasthttp.RequestCtx, oauthCfg *OAuthConfigRequest, serverURL string, requirePublicTarget bool) (*schemas.OAuth2FlowInitiation, error) {
	base := lib.BuildBaseURL(ctx, h.store.GetMCPExternalClientURL())
	redirectURI := base + oauthCallbackPath
	return h.oauthHandler.InitiateOAuthFlow(ctx, OAuthInitiationRequest{
		ClientID:        oauthCfg.ClientID,
		ClientSecret:    oauthCfg.ClientSecret,
		AuthorizeURL:    oauthCfg.AuthorizeURL,
		TokenURL:        oauthCfg.TokenURL,
		RegistrationURL: oauthCfg.RegistrationURL,
		RedirectURI:     redirectURI,
		Scopes:          oauthCfg.Scopes,
		ServerURL:       serverURL,
		Resource:        oauthCfg.Resource,

		RequirePublicTarget:       requirePublicTarget,
		ClientMetadataURL:         base + oauthClientMetadataPath,
		UseClientMetadataDocument: oauthCfg.UseClientMetadataDocument,
	})
}

// MCPVKConfigResponse is a VK assignment enriched with the VK's display name.
type MCPVKConfigResponse struct {
	VirtualKeyID   string            `json:"virtual_key_id"`
	VirtualKeyName string            `json:"virtual_key_name"`
	ToolsToExecute schemas.WhiteList `json:"tools_to_execute"`
}

// reauthorizeMCPClient handles POST /api/mcp/client/{id}/reauthorize.
//
// Redoes the OAuth consent dance for an already-authorized OAuth-based MCP
// client, without delete-and-recreate. For shared (auth_type='oauth')
// clients it serves two cases with one endpoint: a standalone
// admin-triggered reauth (e.g. the upstream provider revoked the
// credential) and the follow-up to rotating client_id/client_secret (which
// cascades every bound token to needs_reauth; this is how an admin actually
// redoes consent afterward). For per_user_oauth clients it (re)establishes
// the retained admin credential used for periodic tool-list discovery —
// available any time, not just repair, so a different admin can take over
// the credential (e.g. after the original admin's access is revoked) or an
// admin can rotate it proactively; end-user credentials are untouched
// either way. Always redoes consent against whatever credentials are
// currently stored on the oauth_configs row, no mode flag, no branching on
// why the token needs it. To redo consent against a NEWLY registered client
// instead, see reregisterMCPClient.
func (h *MCPHandler) reauthorizeMCPClient(ctx *fasthttp.RequestCtx) {
	h.startMCPClientReauthorization(ctx, false)
}

// reregisterMCPClient handles POST /api/mcp/client/{id}/reregister.
//
// reauthorizeMCPClient's counterpart for the case where redoing consent
// cannot work: the provider no longer recognises the client_id it issued
// through dynamic registration (a client registry that did not survive a
// restart, a client revoked upstream). Both the authorize request and the
// code exchange behind it are then rejected with invalid_client, so an admin
// can redo consent forever without recovering the connection. This registers
// a replacement client first (RFC 7591) and runs consent against that.
//
// A separate route rather than a flag on reauthorize: the two differ in what
// they destroy. Reauthorizing replaces one credential the admin is already
// choosing to replace, while registering a new client invalidates every token
// bound to the config — on a per_user_oauth server, every end user's. That
// belongs in the path, where access control and audit logs can see it, not in
// an optional request body.
func (h *MCPHandler) reregisterMCPClient(ctx *fasthttp.RequestCtx) {
	h.startMCPClientReauthorization(ctx, true)
}

// startMCPClientReauthorization is the shared body of reauthorizeMCPClient and
// reregisterMCPClient: validate the client is one that can
// reauthorize, optionally register a replacement OAuth client, then open an
// admin-mode flow and hand back the upstream authorize URL.
func (h *MCPHandler) startMCPClientReauthorization(ctx *fasthttp.RequestCtx, reregisterClient bool) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "MCP operations unavailable: config store is disabled")
		return
	}
	id, err := getIDFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid mcp client id: %v", err))
		return
	}

	clientConfig, err := h.store.ConfigStore.GetMCPClientConfigByID(ctx, id)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("MCP client '%s' not found", id))
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to load MCP client: %v", err))
		return
	}
	if clientConfig.AuthType != schemas.MCPAuthTypeOauth && clientConfig.AuthType != schemas.MCPAuthTypePerUserOauth {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("reauthorize only applies to OAuth-based clients (oauth, per_user_oauth), got %q", clientConfig.AuthType))
		return
	}
	if clientConfig.OauthConfigID == nil || *clientConfig.OauthConfigID == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "MCP client has not completed initial OAuth authorization yet; use initiate-verification instead")
		return
	}
	if clientConfig.PendingOAuthConfig != nil {
		// A config.json-bootstrapped client links OauthConfigID before the
		// admin ever completes consent (see InitiateOAuthFlow), so the check
		// above alone doesn't catch this case. Reauthorizing here would
		// resolve to the same flow_mode='admin' + mcp_client_id lookup as
		// the in-flight bootstrap flow and rotate its state/code_verifier,
		// breaking the bootstrap authorize URL the admin already has open.
		SendError(ctx, fasthttp.StatusBadRequest, "MCP client has not completed initial OAuth authorization yet; use initiate-verification instead")
		return
	}
	if h.store.OAuthProvider == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "OAuth provider not configured")
		return
	}

	// Register the replacement client BEFORE the flow is initiated, so the
	// authorize URL this call hands back is already built from it. Doing it
	// after would hand the admin a URL carrying the client_id that was just
	// replaced, which is the failure this exists to fix.
	// Computed once and used for both the registration and the consent behind
	// it: the provider checks the authorize request's redirect_uri against
	// what THAT client was registered with, so the two must be the same value,
	// not two values that happen to agree until the external URL changes.
	redirectURI := lib.BuildBaseURL(ctx, h.store.GetMCPExternalClientURL()) + "/api/oauth/callback"

	var previousClientID, newClientID string
	rotated := false
	if reregisterClient {
		previousClientID, newClientID, rotated, err = h.store.OAuthProvider.ReregisterDynamicClient(ctx, *clientConfig.OauthConfigID, redirectURI)
		if err != nil {
			// 400 only for what the caller can change their way out of: no
			// registration endpoint to ask (set one, or reauthorize instead),
			// or a provider that understood the request and refused it. A
			// config that would not load, a provider that fell over, a
			// replacement that would not persist: none of those is a mistake in
			// the request, and calling it one sends the admin to fix the wrong
			// thing.
			status := fasthttp.StatusInternalServerError
			if errors.Is(err, oauth2.ErrDynamicRegistrationUnavailable) || errors.Is(err, oauth2.ErrDynamicRegistrationRejected) {
				status = fasthttp.StatusBadRequest
			}
			SendError(ctx, status, fmt.Sprintf("Failed to register a new OAuth client: %v", err))
			return
		}
		if rotated {
			// Same two steps, for the same reason, as the rotation in
			// updateMCPClient: the cascade to needs_reauth is a database fact,
			// and neither the provider's token cache (answered from before any
			// row is read) nor a shared client's live connection (old bearer
			// baked into its transport) knows about it. Through the cache
			// manager rather than the provider, so a clustered deployment's
			// override reaches its peers.
			h.mcpCredentialCacheManager.EvictOauthTokenCacheByMCPClient(ctx, clientConfig.ID)
			if err := h.mcpManager.CloseAndMarkNeedsReauth(ctx, clientConfig.ID); err != nil && !errors.Is(err, schemas.ErrMCPReconnectNotApplicable) {
				logger.Error(fmt.Sprintf("Failed to close MCP client %s's connection after registering a new OAuth client: %v", clientConfig.ID, err))
			}
		}
	}

	// Recorded here, not after consent starts: the replacement client is already installed, and a failure below must not leave that unrecorded.
	if reregisterClient {
		h.recordMCPClientAction(ctx, "mcp_client.reregistered", clientConfig.ID, clientConfig.Name, map[string]any{
			"auth_type":         clientConfig.AuthType,
			"oauth_config_id":   *clientConfig.OauthConfigID,
			"client_id_changed": rotated,
		})
	}

	// A failure from here on, after a rotation, is a partial success: the
	// replacement client is registered upstream (which cannot be undone) and
	// installed, and only the consent against it has not started. Reported the
	// way updateMCPClient reports its own, and for a sharper reason: a caller
	// who reads a bare failure as "nothing happened" retries this route and
	// registers yet another client. reauthorize is what finishes the job,
	// since it runs consent against whatever is stored, which is now the
	// replacement.
	failConsentSetup := func(what string, err error) {
		if !rotated {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("%s: %v", what, err))
			return
		}
		logger.Error(fmt.Sprintf("[PARTIAL SUCCESS] MCP client %s had a new OAuth client registered and installed (client_id %s replaces %s) but starting consent with it failed: %v", clientConfig.ID, newClientID, previousClientID, err))
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("A new OAuth client was registered and installed (client_id %s replaces %s), but starting consent with it failed: %v. Do not retry this request, which would register another client: call reauthorize to run consent against the client that is now stored", newClientID, previousClientID, err))
	}

	flowInitiation, flowID, err := h.store.OAuthProvider.InitiateUserOAuthFlow(ctx, *clientConfig.OauthConfigID, clientConfig.ID, redirectURI, schemas.MCPAuthModeAdmin)
	if err != nil {
		failConsentSetup("Failed to initiate reauthorization", err)
		return
	}
	authorizeURL, err := h.store.OAuthProvider.BuildAdminUpstreamAuthorizeURL(ctx, flowID)
	if err != nil {
		failConsentSetup("Failed to build authorize URL", err)
		return
	}

	completeURL := fmt.Sprintf("/api/mcp/client/%s/complete-oauth", flowInitiation.OauthConfigID)
	// status_url carries the flow id: this client's oauth_configs row has
	// been "authorized" since its original bootstrap and that status never
	// regresses (see isPrematureOAuthCompletion), so a poller reading the
	// bare config status would see "authorized" before the admin has even
	// signed in. With flow_id the status endpoint answers from the flow row
	// instead, which stays "pending" until the callback actually completes.
	statusURL := fmt.Sprintf("/api/oauth/config/%s/status?flow_id=%s", flowInitiation.OauthConfigID, url.QueryEscape(flowID))
	response := map[string]any{
		"status":          "pending_oauth",
		"oauth_config_id": flowInitiation.OauthConfigID,
		"flow_id":         flowID,
		"authorize_url":   authorizeURL,
		"expires_at":      flowInitiation.ExpiresAt,
		"mcp_client_id":   clientConfig.ID,
		"complete_url":    completeURL,
		"status_url":      statusURL,
		"next_steps": []string{
			"1. Open authorize_url in a browser to approve access",
			"2. Poll status_url to check when status becomes 'authorized'",
			"3. POST complete_url to reconnect the MCP client",
		},
	}
	// Reported back so the caller can show which client_id the consent it is
	// about to run actually belongs to. Present only when a registration
	// happened, and a provider that answers with the client_id it already
	// issued reports both fields equal rather than claiming a swap.
	if reregisterClient {
		response["registered_client_id"] = newClientID
		response["previous_client_id"] = previousClientID
	} else {
		h.recordMCPClientAction(ctx, "mcp_client.reauthorization_started", clientConfig.ID, clientConfig.Name, map[string]any{
			"auth_type":       clientConfig.AuthType,
			"oauth_config_id": flowInitiation.OauthConfigID,
		})
	}
	SendJSON(ctx, response)
}

// initiateMCPClientVerification handles
// POST /api/mcp/client/{id}/initiate-verification.
//
// Surfaced on shared-OAuth MCP clients sitting in pending_verification —
// i.e. clients whose row was persisted with a PendingOAuthConfig stash but
// no linked oauth_configs row yet. The admin clicks Authorize in the UI;
// this handler reads the stash, runs the same OAuth init the UI Create
// flow runs (via runOAuthBootstrap), links the freshly created
// oauth_configs row to the MCP client, and returns the authorize URL.
//
// Retry semantics: if the admin started a previous attempt that expired
// or was abandoned, the linked oauth_configs row may still exist with a
// non-authorized status. We always create a fresh row on every click —
// once oauth_config_id is repointed the previous row is unreferenced and
// inert (its status can never become authorized), and the OAuth sweep
// worker removes it (DeleteAbandonedOauthConfigs).
func (h *MCPHandler) initiateMCPClientVerification(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "MCP operations unavailable: config store is disabled")
		return
	}

	id, err := getIDFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid mcp client id: %v", err))
		return
	}

	clientConfig, err := h.store.ConfigStore.GetMCPClientConfigByID(ctx, id)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("MCP client '%s' not found", id))
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to load MCP client: %v", err))
		return
	}

	if clientConfig.AuthType != schemas.MCPAuthTypeOauth && clientConfig.AuthType != schemas.MCPAuthTypePerUserOauth {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("initiate-verification only applies to OAuth-based clients (oauth, per_user_oauth), got %q", clientConfig.AuthType))
		return
	}
	if clientConfig.PendingOAuthConfig == nil {
		SendError(ctx, fasthttp.StatusBadRequest, "MCP client has no pending OAuth bootstrap config to initiate from")
		return
	}
	if clientConfig.ConnectionString == nil || clientConfig.ConnectionString.GetValue() == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "MCP client connection_string is required to initiate OAuth discovery")
		return
	}

	oauthCfg := pendingOAuthConfigToRequest(clientConfig.PendingOAuthConfig)
	flowInitiation, err := h.runOAuthBootstrap(ctx, oauthCfg, clientConfig.ConnectionString.GetValue(), clientConfig.RequirePublicTarget)
	if err != nil {
		SendError(ctx, oauthBootstrapErrorStatus(err), fmt.Sprintf("Failed to initiate OAuth flow: %v", err))
		return
	}

	if err := h.store.ConfigStore.UpdateMCPClientOAuthConfigID(ctx, clientConfig.ID, &flowInitiation.OauthConfigID); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to link OAuth config to MCP client: %v", err))
		return
	}

	completeURL := fmt.Sprintf("/api/mcp/client/%s/complete-oauth", flowInitiation.OauthConfigID)
	statusURL := fmt.Sprintf("/api/oauth/config/%s/status", flowInitiation.OauthConfigID)
	h.recordMCPClientAction(ctx, "mcp_client.verification_initiated", clientConfig.ID, clientConfig.Name, map[string]any{
		"auth_type":       clientConfig.AuthType,
		"oauth_config_id": flowInitiation.OauthConfigID,
	})
	SendJSON(ctx, map[string]any{
		"status":          "pending_oauth",
		"oauth_config_id": flowInitiation.OauthConfigID,
		"authorize_url":   flowInitiation.AuthorizeURL,
		"expires_at":      flowInitiation.ExpiresAt,
		"mcp_client_id":   clientConfig.ID,
		"complete_url":    completeURL,
		"status_url":      statusURL,
		"next_steps": []string{
			"1. Open authorize_url in a browser to approve access",
			"2. Poll status_url to check when status becomes 'authorized'",
			"3. POST complete_url to activate the MCP client",
		},
	})
}

// VerifyMCPClientHeadersRequest is the body for
// POST /api/mcp/client/{id}/verify-headers. user_headers carries the
// admin's sample header values for the one-time verification; the values
// are used to open an upstream connection, call tools/list, and are then
// discarded. Each end-user submits their own values at runtime.
type VerifyMCPClientHeadersRequest struct {
	UserHeaders map[string]string `json:"user_headers"`
}

// verifyMCPClientHeaders handles
// POST /api/mcp/client/{id}/verify-headers.
//
// Surfaced on per-user-headers MCP clients in two situations: clients
// sitting in pending_verification (declared in config.json or otherwise
// persisted without DiscoveredTools) awaiting their one-time bootstrap
// verification, and already-verified clients whose retained admin
// discovery credential sits in needs_update and needs repair. Either way
// the admin enters sample header values in the UI form; this handler runs
// the same VerifyHeadersConnection the UI Create flow runs (admin sample
// values → upstream connection → tools/list → discovered tools), retains
// the values as the admin discovery credential, persists DiscoveredTools
// on the row, and triggers a runtime refresh.
//
// Synchronous: no callback, no popup. On success the client transitions
// to connected; on failure the row stays in pending_verification and the
// admin can retry.
// isConcurrentVerifyReplay reports whether a reloaded row's DiscoveredTools
// reflects a DIFFERENT (concurrent) verify-headers request completing this
// client's one-time bootstrap during this request's own upstream
// round-trip — the only scenario the replay guard exists to catch. Gated on
// the client's STATE at the start of the request, not on DiscoveredTools'
// own nil-ness: only a client that was genuinely still pending its one-time
// bootstrap (pending_verification) can race another request finishing that
// same bootstrap. A client repairing an already-verified admin credential
// starts in some other state (Healthy, Unstable, ...) and must run through
// to completion — in particular the admin-credential retention step below
// — or that credential could never be repaired once a client has been
// verified once.
func isConcurrentVerifyReplay(wasPendingVerificationBeforeThisRequest, reloadedHasTools bool) bool {
	return wasPendingVerificationBeforeThisRequest && reloadedHasTools
}

// isPrematureOAuthCompletion reports whether a complete-oauth request
// should be rejected because the admin reauth flow it corresponds to never
// actually resolved via a real callback. TableOauthConfig.Status can't be
// used for this: it's write-once and never regresses once "authorized" (see
// its own doc comment), so a client reauthorizing after an earlier
// successful auth reads stale "authorized" throughout a failed attempt.
// CompleteOAuthFlow always cleans up the flow row on completion, success or
// failure alike, so a row still sitting there pending — and not yet expired
// — means the callback never ran at all (e.g. the upstream/mock
// authorization server rejected the request with a dead-end error page
// instead of redirecting back).
func isPrematureOAuthCompletion(flow *configstoreTables.TableMCPOauthFlow, now time.Time) bool {
	return flow != nil && flow.Status == "pending" && now.Before(flow.ExpiresAt)
}

// getMCPClientRuntimeState returns the live MCPConnectionState the running
// manager currently holds for id, or "" if the client isn't registered
// there — MCPClientConfig.State (the DB-sourced struct) is never populated
// by GetMCPClientConfigByID, since state isn't a DB column; this is the
// only way to read it for a single client outside the paginated list path.
func (h *MCPHandler) getMCPClientRuntimeState(id string) (schemas.MCPConnectionState, error) {
	clients, err := h.client.GetMCPClients()
	if err != nil {
		return "", err
	}
	for _, c := range clients {
		if c.Config != nil && c.Config.ID == id {
			return c.State, nil
		}
	}
	return "", nil
}

func (h *MCPHandler) verifyMCPClientHeaders(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "MCP operations unavailable: config store is disabled")
		return
	}
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.store)
	defer cancel()

	id, err := getIDFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid mcp client id: %v", err))
		return
	}

	var req VerifyMCPClientHeadersRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request body: %v", err))
		return
	}

	clientConfig, err := h.store.ConfigStore.GetMCPClientConfigByID(ctx, id)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("MCP client '%s' not found", id))
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to load MCP client: %v", err))
		return
	}
	if clientConfig.AuthType != schemas.MCPAuthTypePerUserHeaders {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("verify-headers only applies to auth_type='per_user_headers' clients, got %q", clientConfig.AuthType))
		return
	}
	if clientConfig.ConnectionType == schemas.MCPConnectionTypeOpenAPI {
		SendError(ctx, fasthttp.StatusBadRequest, "verify-headers does not apply to openapi clients: their tools come from the OpenAPI document, so there is no remote MCP server to verify against")
		return
	}
	// Snapshot before verification runs — see isConcurrentVerifyReplay's doc:
	// this is what tells a genuine concurrent double-submit of the one-time
	// bootstrap apart from a legitimate repair of an already-verified client.
	runtimeState, err := h.getMCPClientRuntimeState(id)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to check MCP client runtime state: %v", err))
		return
	}
	wasPendingVerificationBeforeThisRequest := runtimeState == schemas.MCPConnectionStatePendingVerification
	// No replay guard: mirrors reauthorizeMCPClient's OAuth equivalent
	// (POST /reauthorize) — always re-runs verification against whatever
	// sample values are submitted, no branching on whether the admin
	// credential actually needs repair. The write path below (activation,
	// persistence, credential upsert) is unconditional on success either way.
	if len(clientConfig.PerUserHeaderKeys) == 0 {
		SendError(ctx, fasthttp.StatusBadRequest, "MCP client has no per_user_header_keys declared; cannot verify")
		return
	}

	// Canonicalize both sides so the missing-keys check matches by
	// canonical form (lowercase + trim), mirroring the UI Create flow.
	canonKeys := mcputils.CanonicalizeHeaderKeys(clientConfig.PerUserHeaderKeys)
	canonUserHeaders := mcputils.CanonicalizeHeaderMap(req.UserHeaders)
	if missing := missingPerUserHeaderValues(canonKeys, canonUserHeaders); len(missing) > 0 {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("user_headers missing values for required keys: %s", strings.Join(missing, ", ")))
		return
	}

	discovery, verifyErr := h.mcpManager.VerifyHeadersConnection(bifrostCtx, clientConfig, canonUserHeaders)
	if verifyErr != nil {
		SendError(ctx, fasthttp.StatusUnprocessableEntity, fmt.Sprintf("Verification failed: %v", verifyErr))
		return
	}
	tools, toolNameMapping := discovery.Tools, discovery.ToolNameMapping

	// Re-load the row before activating and persisting: discovery above
	// holds an upstream network round-trip, and pushing the pre-discovery
	// snapshot into the runtime refresh and the full-row write below would
	// silently restore any fields an admin edited in the meantime (name,
	// filters, pricing, disabled). A fresh read narrows that window to the
	// moments between this read and the write. Nothing has been activated
	// or persisted yet, so a failed reload leaves the retry path open.
	clientConfig, err = h.store.ConfigStore.GetMCPClientConfigByID(ctx, id)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Verification succeeded but reloading the client for activation failed: %v", err))
		return
	}
	// Re-check the replay guard against the freshly reloaded row: a
	// concurrent verify-headers call (e.g. a double-click) may have
	// completed its own discovery and persisted DiscoveredTools during the
	// network round-trip above. The reload already reads the current row,
	// so this costs nothing extra and closes the double-submit race the
	// guard at the top of this handler can't see. Treat it as this
	// request's own success rather than an error: the desired end state
	// (verified, tools discovered) was already reached by the other
	// request, so there's nothing left to do here — redoing the
	// persist/activation below would only risk clobbering its tool set.
	//
	// Gated on isConcurrentVerifyReplay (not a bare DiscoveredTools != nil
	// check): a client that wasn't pending_verification when this request
	// started isn't racing another request's bootstrap, it's a legitimate
	// repair of an already-verified client — that case must fall through
	// and run to completion, or the admin credential (retained further
	// below) could never be repaired once a client has been verified once.
	if isConcurrentVerifyReplay(wasPendingVerificationBeforeThisRequest, clientConfig.DiscoveredTools != nil) {
		SendJSON(ctx, map[string]any{
			"status":      "success",
			"message":     fmt.Sprintf("MCP client verified. %d tools discovered. Each user will submit their own header values on first tool use.", len(clientConfig.DiscoveredTools)),
			"tools_count": len(clientConfig.DiscoveredTools),
		})
		return
	}
	clientConfig.DiscoveredTools = tools
	clientConfig.DiscoveredToolNameMapping = toolNameMapping
	clientConfig.DiscoveredResources, clientConfig.DiscoveredResourceTemplates, clientConfig.DiscoveredPrompts = discovery.Resources, discovery.ResourceTemplates, discovery.Prompts
	clientConfig.DiscoveredInstructions = discovery.Instructions

	// Activate the runtime client BEFORE persisting so a partial failure
	// can always be retried through this endpoint. The replay guard above
	// reads DiscoveredTools from the DB, so persisting first would turn an
	// activation failure into a permanent 409 wedge (runtime still parked,
	// DB claiming verified). In this order every partial failure leaves the
	// DB row without tools and the retry path open:
	//   - activation fails → nothing persisted → retry re-runs verification
	//   - persistence fails → runtime not yet given the tools either (see
	//     below) → retry re-runs verification cleanly, nothing to reconcile
	if err := h.updateMCPClientWithRetry(bifrostCtx, clientConfig.ID, clientConfig); err != nil {
		logger.Error(fmt.Sprintf("Failed to update MCP client after headers verification for client %s: %v", clientConfig.ID, err))
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Verified successfully but failed to activate the client: %v", err))
		return
	}

	// Persist the discovered tools via UpdateMCPClientConfig. The store
	// unconditionally writes every editable column from the given struct
	// (ConfigHash only gates the connection-metadata block), so the update
	// must carry the full config; a sparse struct would zero name,
	// headers, pricing, and the other editable fields. clientConfig was
	// re-loaded after discovery and carries the discovered tools.
	updateReq := &configstoreTables.TableMCPClient{
		ClientID:                  clientConfig.ID,
		Name:                      clientConfig.Name,
		IsCodeModeClient:          clientConfig.IsCodeModeClient,
		ConnectionType:            string(clientConfig.ConnectionType),
		ConnectionString:          clientConfig.ConnectionString,
		StdioConfig:               clientConfig.StdioConfig,
		TLSConfig:                 clientConfig.TLSConfig,
		AuthType:                  string(clientConfig.AuthType),
		OauthConfigID:             clientConfig.OauthConfigID,
		ToolsToExecute:            clientConfig.ToolsToExecute,
		ToolsToAutoExecute:        clientConfig.ToolsToAutoExecute,
		PromptsToExpose:           clientConfig.PromptsToExpose,
		ResourcesToExpose:         clientConfig.ResourcesToExpose,
		Headers:                   clientConfig.Headers,
		AllowedExtraHeaders:       clientConfig.AllowedExtraHeaders,
		IsPingAvailable:           clientConfig.IsPingAvailable,
		NeedsSessionStickiness:    clientConfig.NeedsSessionStickiness,
		ToolPricing:               clientConfig.ToolPricing,
		ToolSyncInterval:          int(clientConfig.ToolSyncInterval / time.Second),
		MaxProtocolVersion:        clientConfig.MaxProtocolVersion,
		ToolExecutionTimeout:      int(clientConfig.ToolExecutionTimeout / time.Second),
		MaxInstructionsLength:     clientConfig.MaxInstructionsLength,
		AllowByDefault:            clientConfig.AllowByDefault,
		PerUserHeaderKeys:         clientConfig.PerUserHeaderKeys,
		DiscoveredTools:           clientConfig.DiscoveredTools,
		DiscoveredToolNameMapping: clientConfig.DiscoveredToolNameMapping,
		Disabled:                  clientConfig.Disabled,
	}
	if err := h.store.ConfigStore.UpdateMCPClientConfig(ctx, clientConfig.ID, updateReq); err != nil {
		// NOTE: Partial success; the runtime client is activated (Healthy)
		// but — since SetClientDiscovery runs below, after this succeeds — has
		// no tools live either, so runtime and DB agree (both empty) rather
		// than drifting. The replay guard reads DiscoveredTools from the
		// DB, so retrying this endpoint re-runs verification cleanly; a
		// restart re-parks the client in pending_verification.
		logger.Error(fmt.Sprintf(
			"[PARTIAL SUCCESS] MCP client %s was activated after headers verification but persisting discovered tools failed: %v. "+
				"The client is connected but has no tools live yet; retry this endpoint to re-run verification and persist.",
			clientConfig.ID, err,
		))
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Client activated but discovered tools could not be persisted. Retry this endpoint to re-run verification and persist: %v", err))
		return
	}
	// Set discovered tools on the client after persistence succeeds — not
	// before. SetClientDiscovery may propagate this update to other observers
	// of the DB row (deployment-specific), so it must run strictly after
	// the write above lands — otherwise a propagated update could be read
	// back before the row it depends on was actually written.
	h.persistMCPDiscovery(ctx, clientConfig.ID, discovery)
	h.mcpManager.SetClientDiscovery(clientConfig.ID, discovery)

	// Retain the admin's sample header values as the auth_mode='admin'
	// credential so the periodic tool syncer (ClientToolSyncer.performSync)
	// can use them for later tool-discovery refresh. Deliberately last: on a
	// repair the upsert flips the credential from needs_update back to
	// active, which closes the replay guard above, so it must only happen
	// once activation and persistence have both succeeded; any earlier
	// failure leaves the credential in needs_update and the retry path open.
	// Best-effort beyond that: a failed upsert doesn't fail the request,
	// since the client is verified and serving; it just means the tool list
	// can't refresh (bootstrap) or the client keeps projecting needs_reauth
	// until this endpoint is retried (repair).
	if h.store.MCPHeadersProvider != nil {
		if err := h.store.MCPHeadersProvider.UpsertCredential(ctx, &schemas.MCPHeadersUserCredential{
			MCPClientID: clientConfig.ID,
			AuthMode:    schemas.MCPAuthModeAdmin,
			Headers:     canonUserHeaders,
			Status:      schemas.MCPHeadersUserCredentialStatusActive,
		}); err != nil {
			logger.Warn(fmt.Sprintf("failed to retain admin header credential for MCP client %s: %v", clientConfig.ID, err))
		}
	}

	h.recordMCPClientAction(ctx, "mcp_client.headers_verified", clientConfig.ID, clientConfig.Name, map[string]any{
		"tools_count": len(tools),
		"header_keys": canonKeys,
	})
	SendJSON(ctx, map[string]any{
		"status":      "success",
		"message":     fmt.Sprintf("MCP client verified. %d tools discovered. Each user will submit their own header values on first tool use.", len(tools)),
		"tools_count": len(tools),
	})
}

// verifyMCPClientExchange handles
// POST /api/mcp/client/{id}/verify-exchange. No request body: the subject of
// the verification exchange is always the signed-in admin's own
// identity-provider token: the one stamped on the request context by the auth
// layer, or the one stored from the admin's sign-in when the request carries
// none.
//
// Surfaced on token_exchange MCP clients in two situations, mirroring
// verify-headers: clients sitting in pending_verification (declared in
// config.json, or created by a request with no token to exchange, such as one
// made with an API key) awaiting
// their one-time bootstrap verification, and a voluntary refresh of an
// already-verified client's retained admin discovery credential —
// resubmitting always re-runs verification and retains a fresh credential,
// whether or not the current one needs repair. Synchronous: the admin's
// token is exchanged exactly like a real caller's would be, the upstream
// connection is verified, tools are discovered and persisted, the fresh
// credential is retained for the tool syncer, and the runtime client
// transitions to connected. On failure the stored state is untouched and
// the admin can retry.
func (h *MCPHandler) verifyMCPClientExchange(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "MCP operations unavailable: config store is disabled")
		return
	}
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.store)
	defer cancel()

	id, err := getIDFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid mcp client id: %v", err))
		return
	}

	clientConfig, err := h.store.ConfigStore.GetMCPClientConfigByID(ctx, id)
	if err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("MCP client '%s' not found", id))
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to load MCP client: %v", err))
		return
	}
	if clientConfig.AuthType != schemas.MCPAuthTypeTokenExchange {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("verify-exchange only applies to auth_type='token_exchange' clients, got %q", clientConfig.AuthType))
		return
	}
	// No replay guard: mirrors reauthorizeMCPClient's OAuth equivalent and
	// verify-headers — always re-runs verification with a fresh exchange of
	// the admin's own identity-provider token, no branching on whether the
	// retained admin credential actually needs repair. The write path below
	// (activation, persistence, credential retention) is unconditional on
	// success either way.
	if h.store.OAuthProvider == nil || !h.store.OAuthProvider.TokenExchangeAvailable() {
		SendError(ctx, fasthttp.StatusBadRequest, "token exchange is unavailable: user-identity authentication with an exchange client must be configured")
		return
	}
	// The verification subject is always the signed-in admin's own
	// identity-provider token ("verify as yourself"): the one the request
	// carries, or the one stored from the admin's sign-in. An
	// API-key-authenticated request has neither and must be retried from an
	// identity-authenticated session; an expired stored sign-in needs a new
	// sign-in.
	adminResponse, err := h.store.OAuthProvider.ExchangeAdminCredential(bifrostCtx, clientConfig)
	if err != nil {
		switch {
		case errors.Is(err, schemas.ErrExchangeSubjectTokenMissing):
			SendError(ctx, fasthttp.StatusBadRequest, "verification exchanges your own identity-provider token, but this request carries none: sign in with your identity provider and retry")
		case errors.Is(err, schemas.ErrStoredSignInExpired):
			SendError(ctx, fasthttp.StatusBadRequest, "verification exchanges your own identity-provider token, but your Bifrost sign-in has expired: sign in to Bifrost again and retry")
		default:
			SendError(ctx, fasthttp.StatusUnprocessableEntity, fmt.Sprintf("Admin credential exchange failed: %v", err))
		}
		return
	}

	discovery, verifyErr := h.mcpManager.VerifyPerUserOAuthConnection(bifrostCtx, clientConfig, adminResponse.AccessToken)
	if verifyErr != nil {
		SendError(ctx, fasthttp.StatusUnprocessableEntity, fmt.Sprintf("Verification failed: %v", verifyErr))
		return
	}
	tools, toolNameMapping := discovery.Tools, discovery.ToolNameMapping

	// Same reload-activate-persist ordering as verify-headers, for the same
	// reasons: the reload narrows the concurrent-edit window opened by the
	// upstream round-trip, and activating before persisting keeps every
	// partial failure retryable through this endpoint (the replay guard
	// reads DiscoveredTools from the DB).
	clientConfig, err = h.store.ConfigStore.GetMCPClientConfigByID(ctx, id)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Verification succeeded but reloading the client for activation failed: %v", err))
		return
	}
	clientConfig.DiscoveredTools = tools
	clientConfig.DiscoveredToolNameMapping = toolNameMapping
	clientConfig.DiscoveredResources, clientConfig.DiscoveredResourceTemplates, clientConfig.DiscoveredPrompts = discovery.Resources, discovery.ResourceTemplates, discovery.Prompts
	clientConfig.DiscoveredInstructions = discovery.Instructions

	if err := h.updateMCPClientWithRetry(bifrostCtx, clientConfig.ID, clientConfig); err != nil {
		logger.Error(fmt.Sprintf("Failed to update MCP client after exchange verification for client %s: %v", clientConfig.ID, err))
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Verified successfully but failed to activate the client: %v", err))
		return
	}

	// Persist the discovered tools. Full struct for the same reason as
	// verify-headers: the store writes every editable column from the given
	// struct, so a sparse one would zero the other editable fields.
	updateReq := &configstoreTables.TableMCPClient{
		ClientID:                  clientConfig.ID,
		Name:                      clientConfig.Name,
		IsCodeModeClient:          clientConfig.IsCodeModeClient,
		ConnectionType:            string(clientConfig.ConnectionType),
		ConnectionString:          clientConfig.ConnectionString,
		StdioConfig:               clientConfig.StdioConfig,
		TLSConfig:                 clientConfig.TLSConfig,
		AuthType:                  string(clientConfig.AuthType),
		OauthConfigID:             clientConfig.OauthConfigID,
		ToolsToExecute:            clientConfig.ToolsToExecute,
		ToolsToAutoExecute:        clientConfig.ToolsToAutoExecute,
		PromptsToExpose:           clientConfig.PromptsToExpose,
		ResourcesToExpose:         clientConfig.ResourcesToExpose,
		Headers:                   clientConfig.Headers,
		AllowedExtraHeaders:       clientConfig.AllowedExtraHeaders,
		IsPingAvailable:           clientConfig.IsPingAvailable,
		NeedsSessionStickiness:    clientConfig.NeedsSessionStickiness,
		ToolPricing:               clientConfig.ToolPricing,
		ToolSyncInterval:          int(clientConfig.ToolSyncInterval / time.Second),
		MaxProtocolVersion:        clientConfig.MaxProtocolVersion,
		ToolExecutionTimeout:      int(clientConfig.ToolExecutionTimeout / time.Second),
		MaxInstructionsLength:     clientConfig.MaxInstructionsLength,
		AllowByDefault:            clientConfig.AllowByDefault,
		TokenExchange:             clientConfig.TokenExchange,
		DiscoveredTools:           clientConfig.DiscoveredTools,
		DiscoveredToolNameMapping: clientConfig.DiscoveredToolNameMapping,
		Disabled:                  clientConfig.Disabled,
	}
	if err := h.store.ConfigStore.UpdateMCPClientConfig(ctx, clientConfig.ID, updateReq); err != nil {
		// NOTE: Partial success; the runtime client is activated (Healthy)
		// but — since SetClientDiscovery runs below, after this succeeds — has
		// no tools live either, so runtime and DB agree (both empty) rather
		// than drifting. The replay guard reads DiscoveredTools from the
		// DB, so retrying this endpoint re-runs verification cleanly; a
		// restart re-parks the client in pending_verification.
		logger.Error(fmt.Sprintf(
			"[PARTIAL SUCCESS] MCP client %s was activated after exchange verification but persisting discovered tools failed: %v. "+
				"The client is connected but has no tools live yet; retry this endpoint to re-run verification and persist.",
			clientConfig.ID, err,
		))
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Client activated but discovered tools could not be persisted. Retry this endpoint to re-run verification and persist: %v", err))
		return
	}
	// Set discovered tools on the client after persistence succeeds — not
	// before. SetClientDiscovery may propagate this update to other observers
	// of the DB row (deployment-specific), so it must run strictly after
	// the write above lands — otherwise a propagated update could be read
	// back before the row it depends on was actually written.
	h.persistMCPDiscovery(ctx, clientConfig.ID, discovery)
	h.mcpManager.SetClientDiscovery(clientConfig.ID, discovery)

	// Retain the admin credential for the periodic tool syncer and the
	// refresh worker. Deliberately last, mirroring verify-headers: on a
	// repair the upsert flips the credential back to active, which closes
	// the replay guard above, so it must only happen once activation and
	// persistence have both succeeded. Best-effort beyond that: the client
	// is verified and serving either way; a failed retention just means
	// tool-list refresh stays unavailable (bootstrap) or the client keeps
	// projecting needs_reauth (repair) until this endpoint is retried.
	if retainErr := h.store.OAuthProvider.RetainExchangeAdminCredential(ctx, clientConfig, adminResponse); retainErr != nil {
		logger.Warn(fmt.Sprintf("failed to retain admin exchange credential for MCP client %s: %v", clientConfig.ID, retainErr))
	}

	h.recordMCPClientAction(ctx, "mcp_client.exchange_verified", clientConfig.ID, clientConfig.Name, map[string]any{
		"tools_count": len(tools),
	})
	SendJSON(ctx, map[string]any{
		"status":      "success",
		"message":     fmt.Sprintf("MCP client verified. %d tools discovered. Callers' identity tokens are exchanged automatically on each tool use.", len(tools)),
		"tools_count": len(tools),
	})
}

// pendingOAuthConfigToRequest converts the persisted shared-OAuth bootstrap
// shape into the request shape consumed by runOAuthBootstrap /
// InitiateOAuthFlow. Credentials are *SecretVar on both sides, so env./vault.
// reference metadata passes through intact.
// oauthBootstrapErrorStatus is the status for a failed OAuth bootstrap: 400 for a request the caller
// must change, 500 otherwise.
func oauthBootstrapErrorStatus(err error) int {
	if errors.Is(err, oauth2.ErrClientMetadataDocumentWithClientID) || errors.Is(err, oauth2.ErrClientMetadataDocumentUnavailable) {
		return fasthttp.StatusBadRequest
	}
	return fasthttp.StatusInternalServerError
}

func pendingOAuthConfigToRequest(cfg *schemas.OAuth2Config) *OAuthConfigRequest {
	req := &OAuthConfigRequest{
		AuthorizeURL: cfg.AuthorizeURL,
		TokenURL:     cfg.TokenURL,
		Scopes:       cfg.Scopes,
		Resource:     cfg.Resource,

		UseClientMetadataDocument: cfg.UseClientMetadataDocument,
	}
	if cfg.ClientID.IsSet() {
		req.ClientID = cfg.ClientID
	}
	if cfg.ClientSecret.IsSet() {
		req.ClientSecret = cfg.ClientSecret
	}
	if cfg.RegistrationURL != nil {
		req.RegistrationURL = *cfg.RegistrationURL
	}
	return req
}

// MCPClientResponse represents the response structure for MCP clients
type MCPClientResponse struct {
	Config    *schemas.MCPClientConfig   `json:"config"`
	Tools     []schemas.MCPClientTool    `json:"tools"`
	State     schemas.MCPConnectionState `json:"state"`
	VKConfigs []MCPVKConfigResponse      `json:"vk_configs"`

	// Resources, ResourceTemplates and Prompts are what the server offers besides tools, as last
	// discovered (prompt names prefixed like tools). Empty when it offers none.
	Resources         []mcpgo.Resource         `json:"resources"`
	ResourceTemplates []mcpgo.ResourceTemplate `json:"resource_templates"`
	Prompts           []mcpgo.Prompt           `json:"prompts"`

	// LastFailure is the instance serving this request's own record of what
	// its connection handling last failed with: the step, the error, when it
	// last happened and when the current run began (see
	// schemas.MCPConnectionFailure). Nil while healthy. It is deliberately not
	// derived from credential rows: when projectMCPCredentialState overlays
	// needs_reauth onto a runtime that has recorded nothing yet, the state and
	// the credential block already say why, and the next connect or check to
	// hit the dead credential records the provider's real error here.
	LastFailure *schemas.MCPConnectionFailure `json:"last_failure,omitempty"`
	// NodeStates is the per-instance breakdown behind State in a distributed
	// deployment (instance ID -> that instance's own self-reported state and
	// failure). Present when instances disagree (State is then
	// schemas.MCPConnectionStateDegraded) and when they agree on unstable, so
	// each instance's own reason is visible; only ever populated by a
	// configured MCPClusterStateAggregator, nil in a single-instance
	// deployment.
	NodeStates map[string]schemas.MCPInstanceState `json:"node_states,omitempty"`
	// Credential is the read-only view of the credential the client holds on
	// its own behalf (see MCPClientCredentialResponse). Nil when the client's
	// auth type has no such credential (none, headers) or when none has been
	// issued yet (a client still pending its one-time authorization).
	Credential *MCPClientCredentialResponse `json:"credential,omitempty"`
}

// setResourcesAndPrompts copies client's lists, empty rather than null when the server offers none.
func (r *MCPClientResponse) setResourcesAndPrompts(client *schemas.MCPClient) {
	r.Resources, r.ResourceTemplates, r.Prompts = []mcpgo.Resource{}, []mcpgo.ResourceTemplate{}, []mcpgo.Prompt{}
	if client == nil {
		return
	}
	if client.Resources != nil {
		r.Resources = client.Resources
	}
	if client.ResourceTemplates != nil {
		r.ResourceTemplates = client.ResourceTemplates
	}
	if client.Prompts != nil {
		r.Prompts = client.Prompts
	}
}

// MCPClientCredentialResponse describes the credential a client holds on its
// own behalf, as opposed to the per-caller credentials listed under
// /api/mcp/sessions: the single shared token for an oauth client, the
// retained admin token for per_user_oauth and token_exchange clients (used
// only to refresh the tool list), or the retained admin header values for a
// per_user_headers client. Secret material never appears here: the refresh
// token is reported as present or absent, and header values only by name.
type MCPClientCredentialResponse struct {
	Kind   string `json:"kind"`   // "oauth" | "headers"
	Status string `json:"status"` // oauth: active | orphaned | needs_reauth. headers: active | orphaned | needs_update.
	// StatusReason says why an oauth credential's status left active: the
	// provider's rejection of the last refresh (HTTP status plus the OAuth
	// error and description), a credential rotation, or a failed admin
	// exchange. Empty while active and for headers credentials.
	StatusReason string `json:"status_reason,omitempty"`
	// ExpiresAt is the access token's expiry on oauth credentials; nil when
	// the provider did not report one. Always nil for headers credentials.
	ExpiresAt *string `json:"expires_at,omitempty"`
	// LastRefreshedAt is set on oauth credentials once the access token has
	// been refreshed or the credential re-authorized at least once.
	LastRefreshedAt *string `json:"last_refreshed_at,omitempty"`
	// HasRefreshToken reports whether the provider issued a refresh token, so
	// the access token can be renewed without another consent. Always false
	// for headers credentials.
	HasRefreshToken bool `json:"has_refresh_token"`
	// Scopes are the scopes the provider reported at consent time. Empty when
	// the provider omitted them from its token response.
	Scopes []string `json:"scopes,omitempty"`
	// HeaderKeys are the names of the headers the stored admin values cover,
	// sorted. Compared against the client's per_user_header_keys, a key
	// present there but missing here is one the stored values predate.
	HeaderKeys []string `json:"header_keys,omitempty"`
	CreatedAt  string   `json:"created_at"`
	UpdatedAt  string   `json:"updated_at"`
}

// mcpClientCredentialResponse picks the credential row a client of the given
// auth type depends on and projects it to the wire shape. Mirrors the row
// selection in projectMCPCredentialState so the sheet's credential block and
// the client's state badge always describe the same row. Returns nil when
// the auth type holds no such credential or the row does not exist.
func mcpClientCredentialResponse(
	authType schemas.MCPAuthType,
	adminToken *configstoreTables.TableMCPOauthToken,
	adminCred *configstoreTables.TableMCPPerUserHeaderCredential,
	sharedToken *configstoreTables.TableMCPOauthToken,
) *MCPClientCredentialResponse {
	oauthCredential := func(t *configstoreTables.TableMCPOauthToken) *MCPClientCredentialResponse {
		if t == nil {
			return nil
		}
		resp := &MCPClientCredentialResponse{
			Kind:            "oauth",
			Status:          t.Status,
			StatusReason:    t.StatusReason,
			HasRefreshToken: t.RefreshToken != "",
			Scopes:          parseOauthScopesJSON(t.Scopes),
			CreatedAt:       t.CreatedAt.UTC().Format(rfc3339Nano),
			UpdatedAt:       t.UpdatedAt.UTC().Format(rfc3339Nano),
		}
		if t.ExpiresAt != nil {
			resp.ExpiresAt = bifrost.Ptr(t.ExpiresAt.UTC().Format(rfc3339Nano))
		}
		if t.LastRefreshedAt != nil {
			resp.LastRefreshedAt = bifrost.Ptr(t.LastRefreshedAt.UTC().Format(rfc3339Nano))
		}
		return resp
	}
	switch authType {
	case schemas.MCPAuthTypePerUserOauth, schemas.MCPAuthTypeTokenExchange:
		return oauthCredential(adminToken)
	case schemas.MCPAuthTypeOauth:
		return oauthCredential(sharedToken)
	case schemas.MCPAuthTypePerUserHeaders:
		if adminCred == nil {
			return nil
		}
		resp := &MCPClientCredentialResponse{
			Kind:      "headers",
			Status:    adminCred.Status,
			CreatedAt: adminCred.CreatedAt.UTC().Format(rfc3339Nano),
			UpdatedAt: adminCred.UpdatedAt.UTC().Format(rfc3339Nano),
		}
		// Names only. A decode failure (a row written before encryption was
		// configured and read after, say) leaves the key list empty rather
		// than hiding the row: status and timestamps are still accurate.
		if headers, err := adminCred.GetHeaders(); err == nil && len(headers) > 0 {
			resp.HeaderKeys = make([]string, 0, len(headers))
			for name := range headers {
				resp.HeaderKeys = append(resp.HeaderKeys, name)
			}
			sort.Strings(resp.HeaderKeys)
		}
		return resp
	}
	return nil
}

// MCPClusterStateAggregator is an optional capability the configured
// MCPManager may additionally implement: given a client's locally resolved
// state and failure record, it folds in every other instance's self-reported
// pair for the same client and returns the deployment-wide view. A nil
// nodeStates means the local view stands as-is (every instance agrees on a
// healthy state, or the state is one that is not compared per instance);
// a non-nil one is the per-instance breakdown to attach, with state the
// aggregate to show above it: schemas.MCPConnectionStateDegraded when the
// instances disagree, or the agreed non-healthy state when they all report
// it but may each have a different reason. Single-instance deployments never
// implement this, so the type assertion in getMCPClientsPaginated simply
// misses and the response is unchanged from today.
type MCPClusterStateAggregator interface {
	AggregateMCPClientState(clientID string, local schemas.MCPInstanceState) (state schemas.MCPConnectionState, nodeStates map[string]schemas.MCPInstanceState)
}

// getMCPClients handles GET /api/mcp/clients - Get all MCP clients
func (h *MCPHandler) getMCPClients(ctx *fasthttp.RequestCtx) {
	emptyResponse := map[string]interface{}{
		"clients":     []MCPClientResponse{},
		"count":       0,
		"total_count": 0,
		"limit":       0,
		"offset":      0,
	}
	if h.store.ConfigStore == nil {
		SendJSON(ctx, emptyResponse)
		return
	}

	params := configstore.MCPClientsQueryParams{
		Search:          string(ctx.QueryArgs().Peek("search")),
		ClientID:        string(ctx.QueryArgs().Peek("server")),
		ConnectionTypes: parseCommaSeparated(string(ctx.QueryArgs().Peek("connection_type"))),
		AuthTypes:       parseCommaSeparated(string(ctx.QueryArgs().Peek("auth_type"))),
		VirtualKeyIDs:   parseCommaSeparated(string(ctx.QueryArgs().Peek("virtual_keys"))),
	}
	// allowed_by_default replaced all_virtual_keys. The earlier name is still read, and the current
	// one decides when both are sent.
	if b, ok, err := parseBoolQueryArg(ctx, "allowed_by_default"); err != nil {
		SendError(ctx, 400, "Invalid allowed_by_default parameter: must be a boolean")
		return
	} else if ok {
		params.OnlyAllowedByDefault = b
	} else if b, ok, err := parseBoolQueryArg(ctx, "all_virtual_keys"); err != nil {
		SendError(ctx, 400, "Invalid all_virtual_keys parameter: must be a boolean")
		return
	} else if ok {
		params.OnlyAllowedByDefault = b
	}
	// Runtime state selection (healthy/unstable) — resolved against the
	// live engine inside getMCPClientsPaginated since it isn't a DB column.
	states := parseCommaSeparated(string(ctx.QueryArgs().Peek("state")))
	for _, s := range states {
		if s != "healthy" && s != "unstable" {
			SendError(ctx, 400, "Invalid state parameter: must be 'healthy' or 'unstable'")
			return
		}
	}

	if limitStr := string(ctx.QueryArgs().Peek("limit")); limitStr != "" {
		n, err := strconv.Atoi(limitStr)
		if err != nil {
			SendError(ctx, 400, "Invalid limit parameter: must be a number")
			return
		}
		if n < 0 {
			SendError(ctx, 400, "Invalid limit parameter: must be non-negative")
			return
		}
		params.Limit = n
	}
	if offsetStr := string(ctx.QueryArgs().Peek("offset")); offsetStr != "" {
		n, err := strconv.Atoi(offsetStr)
		if err != nil {
			SendError(ctx, 400, "Invalid offset parameter: must be a number")
			return
		}
		if n < 0 {
			SendError(ctx, 400, "Invalid offset parameter: must be non-negative")
			return
		}
		params.Offset = n
	}
	// Optional boolean facets — nil = no filter. Unparseable values are a hard
	// error (like limit/offset) so a typo can't silently drop the filter.
	if b, ok, err := parseBoolQueryArg(ctx, "code_mode"); err != nil {
		SendError(ctx, 400, "Invalid code_mode parameter: must be a boolean")
		return
	} else if ok {
		params.IsCodeModeClient = &b
	}
	if b, ok, err := parseBoolQueryArg(ctx, "disabled"); err != nil {
		SendError(ctx, 400, "Invalid disabled parameter: must be a boolean")
		return
	} else if ok {
		params.Disabled = &b
	}

	h.getMCPClientsPaginated(ctx, params, states)
}

// parseBoolQueryArg reads an optional boolean query parameter. It returns
// (value, true, nil) when the parameter is present and parses as a bool,
// (false, false, nil) when the parameter is absent (no filter), and
// (false, false, err) when present but unparseable — callers should surface
// the last case as an HTTP 400 rather than silently dropping the filter.
func parseBoolQueryArg(ctx *fasthttp.RequestCtx, key string) (bool, bool, error) {
	raw := string(ctx.QueryArgs().Peek(key))
	if raw == "" {
		return false, false, nil
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		return false, false, err
	}
	return b, true, nil
}

// getMCPLibrary handles GET /api/mcp/library — paginated, searchable, filterable
// listing of the synced MCP server catalog. All query parameters are optional.
func (h *MCPHandler) getMCPLibrary(ctx *fasthttp.RequestCtx) {
	emptyResponse := map[string]interface{}{
		"servers":     []configstoreTables.TableMCPLibrary{},
		"count":       0,
		"total_count": 0,
		"limit":       0,
		"offset":      0,
	}
	if h.store.ConfigStore == nil {
		SendJSON(ctx, emptyResponse)
		return
	}

	params := configstore.MCPLibraryQueryParams{
		Search:          string(ctx.QueryArgs().Peek("search")),
		Categories:      parseCommaSeparated(string(ctx.QueryArgs().Peek("category"))),
		ConnectionTypes: parseCommaSeparated(string(ctx.QueryArgs().Peek("connection_type"))),
		AuthTypes:       parseCommaSeparated(string(ctx.QueryArgs().Peek("auth_type"))),
		Tags:            parseCommaSeparated(string(ctx.QueryArgs().Peek("tags"))),
		SortBy:          string(ctx.QueryArgs().Peek("sort_by")),
		Order:           string(ctx.QueryArgs().Peek("order")),
	}

	if limitStr := string(ctx.QueryArgs().Peek("limit")); limitStr != "" {
		n, err := strconv.Atoi(limitStr)
		if err != nil {
			SendError(ctx, 400, "Invalid limit parameter: must be a number")
			return
		}
		if n < 0 {
			SendError(ctx, 400, "Invalid limit parameter: must be non-negative")
			return
		}
		params.Limit = n
	}
	if offsetStr := string(ctx.QueryArgs().Peek("offset")); offsetStr != "" {
		n, err := strconv.Atoi(offsetStr)
		if err != nil {
			SendError(ctx, 400, "Invalid offset parameter: must be a number")
			return
		}
		if n < 0 {
			SendError(ctx, 400, "Invalid offset parameter: must be non-negative")
			return
		}
		params.Offset = n
	}
	params.Limit, params.Offset = ClampPaginationParams(params.Limit, params.Offset)

	entries, totalCount, err := h.store.ConfigStore.GetMCPLibraryPaginated(ctx, params)
	if err != nil {
		logger.Error("failed to retrieve MCP library entries: %v", err)
		SendError(ctx, 500, "Failed to retrieve MCP library entries")
		return
	}

	servers := make([]MCPLibraryEntryResponse, len(entries))
	for i := range entries {
		servers[i].TableMCPLibrary = entries[i]
	}
	if len(entries) > 0 {
		clients, err := h.listAllMCPClientRows(ctx)
		if err != nil {
			logger.Error("failed to retrieve MCP clients for library installed check: %v", err)
			SendError(ctx, 500, "Failed to retrieve MCP clients")
			return
		}
		for i := range servers {
			servers[i].Installed = isMCPLibraryEntryInstalled(&servers[i].TableMCPLibrary, clients)
		}
	}

	SendJSON(ctx, map[string]interface{}{
		"servers":     servers,
		"count":       len(entries),
		"total_count": totalCount,
		"limit":       params.Limit,
		"offset":      params.Offset,
	})
}

// MCPLibraryEntryResponse is a library row plus whether an MCP client already
// installs it, so the UI does not have to match against the client list itself.
type MCPLibraryEntryResponse struct {
	configstoreTables.TableMCPLibrary
	Installed bool `json:"installed"`
}

// mcpLibraryClientsPageSize is the page size used to read every MCP client row
// when resolving which library entries are installed.
const mcpLibraryClientsPageSize = 100

// listAllMCPClientRows reads every MCP client row from the config store, page by
// page, so the installed check is not capped at one page of clients.
func (h *MCPHandler) listAllMCPClientRows(ctx context.Context) ([]configstoreTables.TableMCPClient, error) {
	var all []configstoreTables.TableMCPClient
	for offset := 0; ; offset += mcpLibraryClientsPageSize {
		rows, total, err := h.store.ConfigStore.GetMCPClientsPaginated(ctx, configstore.MCPClientsQueryParams{
			Limit:  mcpLibraryClientsPageSize,
			Offset: offset,
		})
		if err != nil {
			return nil, err
		}
		all = append(all, rows...)
		if len(rows) == 0 || int64(offset+len(rows)) >= total {
			return all, nil
		}
	}
}

// sanitizeMCPLibraryServerName mirrors sanitizeServerName in the UI install
// sheet: it turns a catalog display name into the MCP client name the install
// flow proposes (runs of characters outside [a-zA-Z0-9_] become one
// underscore, and an empty or digit-led result gets an "mcp_" prefix).
func sanitizeMCPLibraryServerName(name string) string {
	var b strings.Builder
	pendingUnderscore := false
	for _, r := range strings.TrimSpace(name) {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			if pendingUnderscore && b.Len() > 0 {
				b.WriteByte('_')
			}
			pendingUnderscore = false
			b.WriteRune(r)
			continue
		}
		pendingUnderscore = true
	}
	cleaned := strings.Trim(b.String(), "_")
	if cleaned == "" || (cleaned[0] >= '0' && cleaned[0] <= '9') {
		return "mcp_" + cleaned
	}
	return cleaned
}

// normalizeMCPLibraryURL trims whitespace and trailing slashes so
// "https://x/mcp/" and "https://x/mcp" compare equal.
func normalizeMCPLibraryURL(u string) string {
	return strings.TrimRight(strings.TrimSpace(u), "/")
}

// isMCPLibraryEntryInstalled reports whether any client was installed from the
// library entry. A client matches when it points at the same target (the
// entry's URL for http/sse, the same command and args for stdio), when its name
// equals the name the install flow proposes, or when its endpoint slug was
// derived from that name. The endpoint slug is immutable, so the last rule
// keeps a renamed client recognized.
func isMCPLibraryEntryInstalled(entry *configstoreTables.TableMCPLibrary, clients []configstoreTables.TableMCPClient) bool {
	installName := sanitizeMCPLibraryServerName(entry.Name)
	installSlug := configstore.Slugify(installName)
	entryURL := normalizeMCPLibraryURL(entry.ConnectionURL)
	for i := range clients {
		client := &clients[i]
		if strings.EqualFold(client.Name, installName) {
			return true
		}
		if installSlug != "" && client.EndpointSlug == installSlug {
			return true
		}
		if entryURL != "" && client.ConnectionString != nil {
			if normalizeMCPLibraryURL(client.ConnectionString.Val) == entryURL ||
				normalizeMCPLibraryURL(client.ConnectionString.GetRawRef()) == entryURL {
				return true
			}
		}
		if entry.StdioConfig != nil && client.StdioConfig != nil {
			command := strings.TrimSpace(entry.StdioConfig.Command)
			if command != "" && command == strings.TrimSpace(client.StdioConfig.Command) &&
				slices.Equal(entry.StdioConfig.Args, client.StdioConfig.Args) {
				return true
			}
		}
	}
	return false
}

// getMCPLibraryFilterData handles GET /api/mcp/library/filterdata — returns the
// distinct facet values (categories, connection types, auth types, tags) that
// drive the MCP library filter sidebar.
func (h *MCPHandler) getMCPLibraryFilterData(ctx *fasthttp.RequestCtx) {
	emptyResponse := configstore.MCPLibraryFilterData{
		Categories:      []string{},
		ConnectionTypes: []string{},
		AuthTypes:       []string{},
		Tags:            []string{},
	}
	if h.store.ConfigStore == nil {
		SendJSON(ctx, emptyResponse)
		return
	}

	data, err := h.store.ConfigStore.GetMCPLibraryFilterData(ctx)
	if err != nil {
		logger.Error("failed to retrieve MCP library filter data: %v", err)
		SendError(ctx, 500, "Failed to retrieve MCP library filter data")
		return
	}
	SendJSON(ctx, data)
}

// forceSyncMCPLibrary handles POST /api/mcp/library/force-sync — triggers an
// immediate sync of the MCP server library catalog from the configured source.
// Mirrors ConfigHandler.forceSyncPricing → ForceReloadPricing.
func (h *MCPHandler) forceSyncMCPLibrary(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "config store not available")
		return
	}

	var count int
	var err error
	if h.store.ModelCatalog != nil {
		count, err = h.store.ModelCatalog.ForceReloadMCPLibrary(ctx)
	} else {
		// Resolve the effective MCP library URL from framework config (DB → file → default).
		mcpLibraryURL := modelcatalog.DefaultMCPLibraryURL
		// Snapshot under the read lock; updateConfig swaps this pointer from
		// another request goroutine.
		h.store.Mu.RLock()
		storedFrameworkConfig := h.store.FrameworkConfig
		h.store.Mu.RUnlock()
		if storedFrameworkConfig != nil && storedFrameworkConfig.Pricing != nil && storedFrameworkConfig.Pricing.MCPLibraryURL != nil {
			if u := *storedFrameworkConfig.Pricing.MCPLibraryURL; u != "" {
				mcpLibraryURL = u
			}
		}
		count, err = modelcatalog.SyncMCPLibrary(ctx, mcpLibraryURL, h.store.ConfigStore)
	}
	if err != nil {
		logger.Error("failed to sync MCP library: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to sync MCP library: %v", err))
		return
	}

	lib.RecordAuditChange(ctx, "mcp_library.synced", "mcp_library", "mcp_library", "MCP library", nil, map[string]any{"entries_synced": count})
	SendJSON(ctx, map[string]any{
		"status":  "success",
		"message": fmt.Sprintf("MCP library sync completed, %d entries synced", count),
	})
}

// getMCPClientsPaginated handles the paginated path for GET /api/mcp/clients.
// states carries the raw connection-state selection (connected/disconnected);
// it is resolved against the live engine here because state is not a DB column.
// projectMCPCredentialState overlays a response-only needs_reauth state onto
// a client's runtime state when the credential the registry depends on has
// been invalidated. The runtime manager is never touched: this projection
// only tells the registry UI that an admin has to re-authorize.
//
// Two shapes, one rule. For a per-user client it is the retained admin
// credential used for periodic tool-list discovery: the client keeps serving
// (end-user credentials and tool calls are unaffected) and only discovery is
// broken. For a shared-OAuth client it is the one credential every caller
// uses, so a dead one means the client is not usable at all.
//
// The shared case needs this projection rather than relying on the runtime
// state alone. Rotating oauth_config cascades every bound token row to
// needs_reauth, including the shared one, but the matching in-memory flip
// (CloseAndMarkNeedsReauth) only lands for a client holding a persistent
// connection. A shared client running per-call — the default for anything
// created with needs_session_stickiness unset — has no connection to close,
// so it returns ErrMCPReconnectNotApplicable and the runtime state stays
// Healthy on credentials that no longer work. The in-memory flip is also
// lost on restart, since nothing persists runtime state for shared clients.
// The token row is the durable record in both cases, so read it here.
//
// statuses are "" when no row exists, which leaves the state alone: clients
// verified before credential retention existed have no admin row and are
// healthy.
//
// Applies over both Healthy and Unstable runtime states: needs_reauth is a
// more actionable signal than Unstable (a dead credential never self-heals —
// Unstable might, on the next successful check), so it must win rather than
// being masked by a pre-existing Unstable reading. It does NOT apply over
// Disabled/PendingVerification/anything else: those already carry a more
// specific, authoritative meaning of their own (an explicit admin action, or
// a one-time bootstrap that was never completed at all) that a stale
// credential shouldn't override.
func projectMCPCredentialState(
	authType schemas.MCPAuthType,
	runtimeState schemas.MCPConnectionState,
	adminTokenStatus, adminCredStatus, sharedTokenStatus string,
) schemas.MCPConnectionState {
	if runtimeState != schemas.MCPConnectionStateHealthy && runtimeState != schemas.MCPConnectionStateUnstable {
		return runtimeState
	}
	switch authType {
	case schemas.MCPAuthTypePerUserOauth, schemas.MCPAuthTypeTokenExchange:
		if adminTokenStatus == "needs_reauth" {
			return schemas.MCPConnectionStateNeedsReauth
		}
	case schemas.MCPAuthTypePerUserHeaders:
		if adminCredStatus == "needs_update" {
			return schemas.MCPConnectionStateNeedsReauth
		}
	case schemas.MCPAuthTypeOauth:
		if sharedTokenStatus == "needs_reauth" {
			return schemas.MCPConnectionStateNeedsReauth
		}
	}
	return runtimeState
}

// oauthTokenStatus and headerCredentialStatus read the status off a possibly
// absent credential row, so projectMCPCredentialState's "" (no row) input
// falls out of a plain map miss.
func oauthTokenStatus(t *configstoreTables.TableMCPOauthToken) string {
	if t == nil {
		return ""
	}
	return t.Status
}

func headerCredentialStatus(c *configstoreTables.TableMCPPerUserHeaderCredential) string {
	if c == nil {
		return ""
	}
	return c.Status
}

func (h *MCPHandler) getMCPClientsPaginated(ctx *fasthttp.RequestCtx, params configstore.MCPClientsQueryParams, states []string) {
	// Get connected clients from Bifrost engine — used both to resolve the
	// runtime state filter and to merge live state/tools onto each row below.
	clientsInBifrost, err := h.client.GetMCPClients()
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get MCP clients from Bifrost: %v", err))
		return
	}
	connectedClientsMap := make(map[string]schemas.MCPClient)
	for _, client := range clientsInBifrost {
		connectedClientsMap[client.Config.ID] = client
	}

	// Resolve the runtime state filter into a connected-id allow/block list the
	// store can apply within the same paginated query. "healthy" means the
	// engine reports MCPConnectionStateHealthy; everything else (unstable,
	// error, disabled, not-in-engine) counts as unstable. Selecting both — or
	// neither — is a no-op.
	if wantHealthy, wantUnstable := slices.Contains(states, "healthy"), slices.Contains(states, "unstable"); wantHealthy != wantUnstable {
		healthyIDs := make([]string, 0, len(clientsInBifrost))
		for _, c := range clientsInBifrost {
			if c.State == schemas.MCPConnectionStateHealthy {
				healthyIDs = append(healthyIDs, c.Config.ID)
			}
		}
		params.StateClientIDs = healthyIDs
		params.StateInclude = &wantHealthy
	}

	// Normalise pagination (0 → 25 default, cap 100) before the query so the
	// echoed limit/offset match the rows actually returned — same helper every
	// other paginated handler uses.
	params.Limit, params.Offset = ClampPaginationParams(params.Limit, params.Offset)

	dbClients, totalCount, err := h.store.ConfigStore.GetMCPClientsPaginated(ctx, params)
	if err != nil {
		logger.Error("failed to retrieve MCP clients: %v", err)
		SendError(ctx, 500, "Failed to retrieve MCP clients")
		return
	}

	// Batch-fetch all VK assignments for this page in a single query, then group by client ID.
	vkNameByID := make(map[string]string)
	assignmentsByClientID := make(map[uint][]configstoreTables.TableVirtualKeyMCPConfig)
	if h.store.ConfigStore != nil {
		dbClientIDs := make([]uint, 0, len(dbClients))
		for _, c := range dbClients {
			dbClientIDs = append(dbClientIDs, c.ID)
		}
		if allAssignments, err := h.store.ConfigStore.GetVirtualKeyMCPConfigsByMCPClientIDs(ctx, dbClientIDs); err == nil {
			for _, a := range allAssignments {
				assignmentsByClientID[a.MCPClientID] = append(assignmentsByClientID[a.MCPClientID], a)
			}
		}
		// Collect unique VK IDs across all assignments, then batch-fetch their names
		// in a single query (avoids one GetVirtualKey round trip per unique VK ID).
		uniqueVKIDs := make([]string, 0)
		seenVirtualKeyIDs := make(map[string]struct{})
		for _, assignments := range assignmentsByClientID {
			for _, assignment := range assignments {
				if _, ok := seenVirtualKeyIDs[assignment.VirtualKeyID]; ok {
					continue
				}
				seenVirtualKeyIDs[assignment.VirtualKeyID] = struct{}{}
				uniqueVKIDs = append(uniqueVKIDs, assignment.VirtualKeyID)
			}
		}
		if len(uniqueVKIDs) > 0 {
			if virtualKeys, err := h.store.ConfigStore.GetRedactedVirtualKeys(ctx, uniqueVKIDs); err == nil {
				for _, virtualKey := range virtualKeys {
					vkNameByID[virtualKey.ID] = virtualKey.Name
				}
			} else {
				logger.Error("failed to batch-retrieve virtual keys for MCP client assignments: %v", err)
			}
		}
	}

	// Batch-fetch OAuth configs for clients that have one (avoids N+1 queries)
	oauthConfigsByID := make(map[string]*configstoreTables.TableOauthConfig)
	if h.store.ConfigStore != nil {
		oauthIDs := make([]string, 0)
		for _, c := range dbClients {
			if c.OauthConfigID != nil && *c.OauthConfigID != "" {
				oauthIDs = append(oauthIDs, *c.OauthConfigID)
			}
		}
		if len(oauthIDs) > 0 {
			fetched, err := h.store.ConfigStore.GetOauthConfigsByIDs(ctx, oauthIDs)
			if err != nil {
				SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to fetch OAuth configs: %v", err))
				return
			}
			oauthConfigsByID = fetched
		}
	}

	// Batch-fetch the credential row each of this page's clients holds on its
	// own behalf (the retained admin discovery credential for per-user
	// clients, the shared token for oauth ones). Each row feeds two things:
	// the needs_reauth state projection (see projectMCPCredentialState) and
	// the read-only credential block on the response (see
	// mcpClientCredentialResponse). Best-effort: a batch-read failure only
	// means the projection is skipped, runtime states pass through untouched,
	// and the credential block is absent. Debug, not Error, because this runs
	// on every registry list.
	adminTokenByClientID := make(map[string]*configstoreTables.TableMCPOauthToken)
	adminCredByClientID := make(map[string]*configstoreTables.TableMCPPerUserHeaderCredential)
	sharedTokenByClientID := make(map[string]*configstoreTables.TableMCPOauthToken)
	if h.store.ConfigStore != nil {
		adminTokenClientIDs := make([]string, 0)
		perUserHeaderClientIDs := make([]string, 0)
		// Shared-OAuth rows are keyed by oauth_config_id, not client id, so
		// keep the reverse mapping to fold the result back onto clients.
		sharedOauthConfigIDs := make([]string, 0)
		clientIDsByOauthConfigID := make(map[string][]string)
		for _, c := range dbClients {
			switch schemas.MCPAuthType(c.AuthType) {
			case schemas.MCPAuthTypePerUserOauth, schemas.MCPAuthTypeTokenExchange:
				adminTokenClientIDs = append(adminTokenClientIDs, c.ClientID)
			case schemas.MCPAuthTypePerUserHeaders:
				perUserHeaderClientIDs = append(perUserHeaderClientIDs, c.ClientID)
			case schemas.MCPAuthTypeOauth:
				if c.OauthConfigID != nil && *c.OauthConfigID != "" {
					if _, seen := clientIDsByOauthConfigID[*c.OauthConfigID]; !seen {
						sharedOauthConfigIDs = append(sharedOauthConfigIDs, *c.OauthConfigID)
					}
					clientIDsByOauthConfigID[*c.OauthConfigID] = append(clientIDsByOauthConfigID[*c.OauthConfigID], c.ClientID)
				}
			}
		}
		if len(adminTokenClientIDs) > 0 {
			if adminTokens, err := h.store.ConfigStore.GetAdminOauthTokensByMCPClientIDs(ctx, adminTokenClientIDs); err == nil {
				for clientID, token := range adminTokens {
					adminTokenByClientID[clientID] = token
				}
			} else {
				logger.Debug("failed to batch-get admin oauth tokens for MCP registry state projection: %v", err)
			}
		}
		if len(perUserHeaderClientIDs) > 0 {
			if adminCreds, err := h.store.ConfigStore.GetAdminMCPPerUserHeaderCredentialsByClientIDs(ctx, perUserHeaderClientIDs); err == nil {
				for clientID, cred := range adminCreds {
					adminCredByClientID[clientID] = cred
				}
			} else {
				logger.Debug("failed to batch-get admin header credentials for MCP registry state projection: %v", err)
			}
		}
		if len(sharedOauthConfigIDs) > 0 {
			if sharedTokens, err := h.store.ConfigStore.GetSharedOauthTokensByConfigIDs(ctx, sharedOauthConfigIDs); err == nil {
				for oauthConfigID, token := range sharedTokens {
					for _, clientID := range clientIDsByOauthConfigID[oauthConfigID] {
						sharedTokenByClientID[clientID] = token
					}
				}
			} else {
				logger.Debug("failed to batch-get shared oauth tokens for MCP registry state projection: %v", err)
			}
		}
	}

	// Convert DB rows to MCPClientConfig and merge with engine state
	clients := make([]MCPClientResponse, 0, len(dbClients))
	for _, dbClient := range dbClients {
		isPingAvailable := true
		if dbClient.IsPingAvailable != nil {
			isPingAvailable = *dbClient.IsPingAvailable
		}
		clientConfig := &schemas.MCPClientConfig{
			ID:                     dbClient.ClientID,
			Name:                   dbClient.Name,
			EndpointSlug:           dbClient.EndpointSlug,
			IsCodeModeClient:       dbClient.IsCodeModeClient,
			ConnectionType:         schemas.MCPConnectionType(dbClient.ConnectionType),
			ConnectionString:       dbClient.ConnectionString,
			StdioConfig:            dbClient.StdioConfig,
			TLSConfig:              dbClient.TLSConfig,
			AuthType:               schemas.MCPAuthType(dbClient.AuthType),
			OauthConfigID:          dbClient.OauthConfigID,
			ToolsToExecute:         dbClient.ToolsToExecute,
			ToolsToAutoExecute:     dbClient.ToolsToAutoExecute,
			PromptsToExpose:        dbClient.PromptsToExpose,
			ResourcesToExpose:      dbClient.ResourcesToExpose,
			Headers:                dbClient.Headers,
			AllowedExtraHeaders:    dbClient.AllowedExtraHeaders,
			IsPingAvailable:        &isPingAvailable,
			NeedsSessionStickiness: dbClient.NeedsSessionStickiness,
			RequirePublicTarget:    dbClient.RequirePublicTarget,
			ToolSyncInterval:       time.Duration(dbClient.ToolSyncInterval) * time.Second,
			MaxProtocolVersion:     dbClient.MaxProtocolVersion,
			ToolExecutionTimeout:   time.Duration(dbClient.ToolExecutionTimeout) * time.Second,
			ToolPricing:            dbClient.ToolPricing,
			AllowByDefault:         dbClient.AllowByDefault,
			Disabled:               dbClient.Disabled,
			PerUserHeaderKeys:      dbClient.PerUserHeaderKeys,
			TokenExchange:          dbClient.TokenExchange,
			OpenAPIConfig:          dbClient.OpenAPIConfig,
		}
		// Populate oauth client credentials from pre-fetched batch
		if dbClient.OauthConfigID != nil {
			if oauthCfg, ok := oauthConfigsByID[*dbClient.OauthConfigID]; ok {
				clientConfig.OauthClientID = oauthCfg.ClientID
				clientConfig.OauthClientSecret = oauthCfg.GetClientSecretAsSecretVar()
				clientConfig.OauthAuthorizeURL = oauthCfg.AuthorizeURL
				clientConfig.OauthTokenURL = oauthCfg.TokenURL
				if oauthCfg.RegistrationURL != nil {
					clientConfig.OauthRegistrationURL = *oauthCfg.RegistrationURL
				}
				clientConfig.OauthResource = oauthCfg.Resource
				if oauthCfg.Scopes != "" {
					_ = json.Unmarshal([]byte(oauthCfg.Scopes), &clientConfig.OauthScopes)
				}
			}
		}
		// Enrich VK assignments using the pre-fetched batch result.
		vkConfigs := []MCPVKConfigResponse{}
		for _, a := range assignmentsByClientID[dbClient.ID] {
			vkConfigs = append(vkConfigs, MCPVKConfigResponse{
				VirtualKeyID:   a.VirtualKeyID,
				VirtualKeyName: vkNameByID[a.VirtualKeyID],
				ToolsToExecute: a.ToolsToExecute,
			})
		}
		redactedConfig := h.store.RedactMCPClientConfig(clientConfig)
		adminToken := adminTokenByClientID[dbClient.ClientID]
		adminCred := adminCredByClientID[dbClient.ClientID]
		sharedToken := sharedTokenByClientID[dbClient.ClientID]
		credential := mcpClientCredentialResponse(clientConfig.AuthType, adminToken, adminCred, sharedToken)
		if connectedClient, exists := connectedClientsMap[clientConfig.ID]; exists {
			sortedTools := make([]schemas.MCPClientTool, len(connectedClient.Tools))
			copy(sortedTools, connectedClient.Tools)
			sort.Slice(sortedTools, func(i, j int) bool {
				return sortedTools[i].Name < sortedTools[j].Name
			})
			resolvedState := projectMCPCredentialState(
				clientConfig.AuthType,
				connectedClient.State,
				oauthTokenStatus(adminToken),
				headerCredentialStatus(adminCred),
				oauthTokenStatus(sharedToken),
			)
			resp := MCPClientResponse{
				Config:      redactedConfig,
				Tools:       sortedTools,
				State:       resolvedState,
				LastFailure: connectedClient.LastFailure,
				VKConfigs:   vkConfigs,
				Credential:  credential,
			}
			resp.setResourcesAndPrompts(&connectedClient)
			if aggregator, ok := h.mcpManager.(MCPClusterStateAggregator); ok {
				local := schemas.MCPInstanceState{State: resolvedState, LastFailure: resp.LastFailure}
				if aggState, nodeStates := aggregator.AggregateMCPClientState(clientConfig.ID, local); nodeStates != nil {
					resp.State = aggState
					resp.NodeStates = nodeStates
				}
			}
			clients = append(clients, resp)
		} else {
			resp := MCPClientResponse{
				Config:     redactedConfig,
				Tools:      []schemas.MCPClientTool{},
				State:      schemas.MCPConnectionStateError,
				VKConfigs:  vkConfigs,
				Credential: credential,
			}
			resp.setResourcesAndPrompts(nil)
			clients = append(clients, resp)
		}
	}

	SendJSON(ctx, map[string]interface{}{
		"clients":     clients,
		"count":       len(clients),
		"total_count": totalCount,
		"limit":       params.Limit,
		"offset":      params.Offset,
	})
}

// reconnectMCPClient handles POST /api/mcp/client/{id}/reconnect - Reconnect an MCP client
func (h *MCPHandler) reconnectMCPClient(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "MCP operations unavailable: config store is disabled")
		return
	}
	id, err := getIDFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid id: %v", err))
		return
	}
	// Reject reconnect requests for disabled clients — the client must be enabled first.
	if h.store.MCPConfig != nil {
		for _, client := range h.store.MCPConfig.ClientConfigs {
			if client.ID == id {
				if client.Disabled {
					SendError(ctx, fasthttp.StatusBadRequest, "cannot reconnect a disabled MCP client: enable the client first")
					return
				}
				break
			}
		}
	}
	if err := h.mcpManager.ReconnectMCPClient(ctx, id); err != nil {
		// Per-user OAuth (and any future client type that opts out of the
		// shared-connection model) is a 400-class error: the request is
		// well-formed, the client just doesn't support this operation.
		if errors.Is(err, schemas.ErrMCPReconnectNotApplicable) {
			SendError(ctx, fasthttp.StatusBadRequest, err.Error())
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to reconnect MCP client: %v", err))
		return
	}
	var reconnectedState map[string]any
	if h.client != nil {
		if state, stateErr := h.getMCPClientRuntimeState(id); stateErr == nil && state != "" {
			reconnectedState = map[string]any{"state": state}
		}
	}
	h.recordMCPClientAction(ctx, "mcp_client.reconnected", id, "", reconnectedState)
	SendJSON(ctx, map[string]any{
		"status":  "success",
		"message": "MCP client reconnected successfully",
	})
}

// refreshMCPClientTools re-discovers one client's tools from its upstream MCP
// server right now. It exists because the periodic connection checker is
// otherwise the only thing that revisits a client's tool list, and its
// steady-state cadence is the tool sync interval — 10 minutes by default — so
// an operator who has just added or removed a tool upstream had nothing to
// reach for. Unlike reconnect, this applies to per-call clients as well, which
// hold no persistent connection and previously had no refresh path at all.
func (h *MCPHandler) refreshMCPClientTools(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "MCP operations unavailable: config store is disabled")
		return
	}
	id, err := getIDFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid id: %v", err))
		return
	}
	// Reject a disabled client the same way reconnect does: it holds no
	// connection and runs no workers, so discovered tools would have nothing
	// to execute them.
	if h.store.MCPConfig != nil {
		for _, client := range h.store.MCPConfig.ClientConfigs {
			if client.ID == id {
				if client.Disabled {
					SendError(ctx, fasthttp.StatusBadRequest, "cannot refresh tools for a disabled MCP client: enable the client first")
					return
				}
				break
			}
		}
	}
	count, err := h.mcpManager.RefreshMCPClientTools(ctx, id)
	if err != nil {
		// An unknown client is the caller naming something that does not
		// exist, not a discovery failure.
		if errors.Is(err, schemas.ErrMCPClientNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, err.Error())
			return
		}
		// A client that is disabled, needs reauthorization, or is still
		// awaiting admin verification is a 400: the request is well-formed,
		// the client just is not in a state where discovery means anything.
		if errors.Is(err, schemas.ErrMCPRefreshNotApplicable) {
			SendError(ctx, fasthttp.StatusBadRequest, err.Error())
			return
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to refresh MCP client tools: %v", err))
		return
	}
	h.recordMCPClientAction(ctx, "mcp_client.tools_refreshed", id, "", map[string]any{"tool_count": count})
	SendJSON(ctx, map[string]any{
		"status":     "success",
		"message":    "MCP client tools refreshed successfully",
		"tool_count": count,
	})
}

// OAuthConfigRequest represents OAuth configuration in the request
type OAuthConfigRequest struct {
	ClientID        *schemas.SecretVar `json:"client_id"`
	ClientSecret    *schemas.SecretVar `json:"client_secret"`
	AuthorizeURL    string             `json:"authorize_url"`
	TokenURL        string             `json:"token_url"`
	RegistrationURL string             `json:"registration_url"`
	Scopes          []string           `json:"scopes"`
	Resource        string             `json:"resource"`

	UseClientMetadataDocument bool `json:"use_client_metadata_document,omitempty"` // Register with Bifrost's Client ID Metadata Document
}

// MCPSyncInterval accepts duration strings or integer minutes for MCP client requests.
// Its field-specific validation matches the whole-second database column.
type MCPSyncInterval struct {
	Duration time.Duration
}

// UnmarshalJSON preserves the API's legacy numeric unit of minutes.
func (m *MCPSyncInterval) UnmarshalJSON(data []byte) error {
	duration, err := parseRequestDuration(data, "tool_sync_interval", time.Minute, "minutes")
	if err != nil {
		return err
	}
	m.Duration = duration
	return nil
}

// MarshalJSON renders the resolved value as a duration string, matching how
// GET responses already render this field.
func (m MCPSyncInterval) MarshalJSON() ([]byte, error) {
	return json.Marshal(m.Duration.String())
}

// Validate reports why a resolved interval cannot be used.
//
// The column stores whole seconds, and 0 there means "use the global setting"
// rather than "disabled". A value carrying a sub-second remainder loses it on
// the way to storage, and one below a second truncates to 0 and selects the
// opposite of what was asked for.
//
// The storage layer refuses both already. This repeats the check at the
// request boundary so the answer is a 400 before anything happens, rather than
// a failure after the client has been dialled; the wording is kept identical
// to the storage layer's so a caller never sees the same rejection phrased two
// ways. The ceiling on the bare-integer form is checked during decode instead,
// since that is a property of the wire form rather than of the duration.
func (m MCPSyncInterval) Validate() error {
	if m.Duration < 0 {
		return fmt.Errorf("tool_sync_interval must be 0 (use the global setting) or a positive duration, got %q", m.Duration.String())
	}
	if m.Duration%time.Second != 0 {
		return fmt.Errorf("tool_sync_interval must be a whole number of seconds, got %q", m.Duration.String())
	}
	return nil
}

// Resolve applies the field to an existing value and returns what to persist.
//
// The receiver is deliberately nil-safe, and that is the common case rather
// than an edge one: these endpoints have PATCH semantics, so an absent field
// is a nil pointer meaning "keep what is already there". Callers can therefore
// write req.ToolSyncInterval.Resolve(existing) without a nil check, and the
// distinction between absent and explicitly zero stays in one place. An
// explicit 0 is a value, not an absence, and clears the override.
func (m *MCPSyncInterval) Resolve(existing time.Duration) (time.Duration, error) {
	if m == nil {
		return existing, nil
	}
	if err := m.Validate(); err != nil {
		return 0, err
	}
	return m.Duration, nil
}

// MCPClientRequest represents the full MCP client creation request with OAuth support.
//
// UserHeaders carries a sample set of per-user-headers values used only for
// upstream verification + tool discovery during create. Mirrors the per-user
// OAuth flow where the admin's temp access token is used the same way: the
// server runs discovery, attaches DiscoveredTools to the persisted config,
// and discards the credentials. Ignored for non-per_user_headers auth types.
type MCPClientRequest struct {
	configstoreTables.TableMCPClient
	OauthConfig      *OAuthConfigRequest `json:"oauth_config,omitempty"`
	UserHeaders      map[string]string   `json:"user_headers,omitempty"`
	ToolSyncInterval *MCPSyncInterval    `json:"tool_sync_interval,omitempty"` // Request units; shadows the embedded seconds column.
}

// UnmarshalJSON reads allow_by_default under its earlier name as well, allow_on_all_virtual_keys, so a
// caller that has not moved to the current key is still understood. The current key decides when both
// are sent; see schemas.ResolveAllowByDefault.
func (r *MCPClientRequest) UnmarshalJSON(data []byte) error {
	type alias MCPClientRequest
	aux := &struct {
		AllowByDefault        *bool `json:"allow_by_default,omitempty"`
		AllowOnAllVirtualKeys *bool `json:"allow_on_all_virtual_keys,omitempty"`
		*alias
	}{alias: (*alias)(r)}
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	r.AllowByDefault = schemas.ResolveAllowByDefault(aux.AllowByDefault, aux.AllowOnAllVirtualKeys)
	return nil
}

// MCPVKConfigRequest represents a per-VK tool access config for an MCP client
type MCPVKConfigRequest struct {
	VirtualKeyID   string            `json:"virtual_key_id"`
	ToolsToExecute schemas.WhiteList `json:"tools_to_execute"`
}

// Bounds for the tool_sync_interval request field, which is expressed in minutes.
// They mark the point where minutes*time.Minute would overflow int64 (~292 years),
// so they reject unit-confused input without constraining any realistic interval.
// The persisted int seconds cannot wrap within these bounds: that would require a
// 32-bit int, and sonic (a core dependency) fails to compile on 32-bit by design.
const maxToolSyncIntervalMinutes = int64(math.MaxInt64) / int64(time.Minute)

// MCPClientUpdateRequest is the body for PUT /api/mcp/client/{id}.
// All fields are optional — omitting a field retains its existing value (PATCH semantics).
// Immutable fields (connection_type, auth_type, connection_string, stdio_config) are not
// accepted here; they cannot be changed after creation.
type MCPClientUpdateRequest struct {
	Name                   *string                         `json:"name,omitempty"`
	Disabled               *bool                           `json:"disabled,omitempty"`
	AllowByDefault         *bool                           `json:"allow_by_default,omitempty"`
	IsCodeModeClient       *bool                           `json:"is_code_mode_client,omitempty"`
	IsPingAvailable        *bool                           `json:"is_ping_available,omitempty"`
	NeedsSessionStickiness *bool                           `json:"needs_session_stickiness,omitempty"`
	ToolSyncInterval       *MCPSyncInterval                `json:"tool_sync_interval,omitempty"`
	MaxProtocolVersion     *string                         `json:"max_protocol_version,omitempty"` // "" = use the global setting
	ToolExecutionTimeout   *int                            `json:"tool_execution_timeout,omitempty"`
	MaxInstructionsLength  *int                            `json:"max_instructions_length,omitempty"`
	Headers                map[string]schemas.SecretVar    `json:"headers,omitempty"`
	AllowedExtraHeaders    *schemas.WhiteList              `json:"allowed_extra_headers,omitempty"`
	ToolPricing            map[string]float64              `json:"tool_pricing,omitempty"`
	ToolsToExecute         *schemas.WhiteList              `json:"tools_to_execute,omitempty"`
	ToolsToAutoExecute     *schemas.WhiteList              `json:"tools_to_auto_execute,omitempty"`
	PromptsToExpose        *schemas.WhiteList              `json:"prompts_to_expose,omitempty"`
	ResourcesToExpose      *schemas.WhiteList              `json:"resources_to_expose,omitempty"`
	PerUserHeaderKeys      *[]string                       `json:"per_user_header_keys,omitempty"`
	TokenExchange          *schemas.MCPTokenExchangeConfig `json:"token_exchange,omitempty"`
	TLSConfig              *schemas.MCPTLSConfig           `json:"tls_config,omitempty"`
	VKConfigs              *[]MCPVKConfigRequest           `json:"vk_configs,omitempty"`
	OauthConfig            *OAuthConfigRequest             `json:"oauth_config,omitempty"`
	OpenAPIConfig          *schemas.MCPOpenAPIConfig       `json:"openapi_config,omitempty"` // openapi clients only; replaces the spec/credentials (PATCH-style inside the block)

	// AllowOnAllVirtualKeys is the earlier name of allow_by_default, still accepted. AllowByDefault
	// decides when both are sent; see schemas.ResolveAllowByDefault.
	AllowOnAllVirtualKeys *bool `json:"allow_on_all_virtual_keys,omitempty"`
}

// resolvedAllowByDefault applies PATCH semantics to allow_by_default: the request's value when it
// sent one under either wire name, otherwise existing. The current name decides when both are sent;
// see schemas.ResolveAllowByDefault.
func (req *MCPClientUpdateRequest) resolvedAllowByDefault(existing bool) bool {
	if req.AllowByDefault == nil && req.AllowOnAllVirtualKeys == nil {
		return existing
	}
	return schemas.ResolveAllowByDefault(req.AllowByDefault, req.AllowOnAllVirtualKeys)
}

// rejectPrivateMCPTargetIfAuthBypassed refuses to register an HTTP/SSE MCP
// client whose connection_string resolves to a loopback, private-network,
// link-local, or CGNAT address when the caller reached this endpoint with no
// credential check at all (default-open posture, BifrostContextKeyAuthBypassed).
// A genuinely authenticated admin keeps the documented ability to point an MCP
// client at a local or private server (see docs/mcp/connecting-to-servers.mdx,
// whose own HTTP-client example uses http://localhost:3001/mcp) - only the
// unauthenticated path is restricted, the same scoping used for the STDIO
// client gate below. Returns true if the request was rejected (the error
// response has already been written); the caller should return immediately.
func rejectPrivateMCPTargetIfAuthBypassed(ctx *fasthttp.RequestCtx, connType string, connectionString *schemas.SecretVar) bool {
	if connType != string(schemas.MCPConnectionTypeHTTP) && connType != string(schemas.MCPConnectionTypeSSE) {
		return false
	}
	if connectionString == nil {
		return false
	}
	return rejectPrivateURLIfAuthBypassed(ctx, connectionString.GetValue())
}

// rejectPrivateURLIfAuthBypassed is the URL-shaped form of the gate above,
// shared with the openapi paths (spec_url and the upstream base_url carry the
// same SSRF exposure as an MCP connection_string). Returns true if the request
// was rejected (the error response has already been written).
func rejectPrivateURLIfAuthBypassed(ctx *fasthttp.RequestCtx, rawURL string) bool {
	if authBypassed, _ := ctx.UserValue(schemas.BifrostContextKeyAuthBypassed).(bool); !authBypassed {
		return false
	}
	msg, refused := privateTargetRefusal(rawURL)
	if !refused {
		return false
	}
	SendError(ctx, fasthttp.StatusForbidden, msg)
	return true
}

// privateTargetRefusal classifies rawURL's host for the unauthenticated-caller
// rule without writing a response: refused is true, with the message to send,
// when the host resolves to a non-public address or cannot be resolved at all
// (fail closed). A malformed URL is not refused here; the connect path reports
// it.
func privateTargetRefusal(rawURL string) (string, bool) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Hostname() == "" {
		// Malformed/unresolvable target: let the normal connect path surface
		// its own error rather than duplicating URL validation here.
		return "", false
	}
	// A bounded, request-independent context: this lookup is a pre-check, not
	// a dial, and must not be tied to the request's own (often much longer)
	// deadline.
	lookupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(lookupCtx, "ip", parsed.Hostname())
	if err != nil || len(ips) == 0 {
		// Fail closed: with no addresses to classify there is nothing this
		// gate can vouch for, and an unresolvable name is also how a caller
		// would make the check inconclusive on purpose.
		return fmt.Sprintf("could not resolve MCP target host %q; unauthenticated callers can only register MCP clients whose target resolves to a public address; set an admin password to allow this", parsed.Hostname()), true
	}
	for _, ip := range ips {
		if !network.IsPublicIP(ip) {
			return "unauthenticated callers cannot register MCP clients that connect to loopback, private-network, or link-local addresses; set an admin password to allow this", true
		}
	}
	return "", false
}

// rejectStdioMCPClientIfAuthBypassed refuses to register a stdio MCP client for
// a caller that was let through because dashboard auth is unconfigured or
// disabled. A stdio client makes Bifrost exec() an admin-supplied command and
// args as the gateway process: intentional admin functionality, but only for a
// caller who actually authenticated. Registering one over this network API with
// no credential check is equivalent to unauthenticated remote code execution,
// so it is refused even though the rest of the management API stays open for
// the zero-config experience. Stdio clients can still be provisioned by an
// operator with host/filesystem access via config.json. Returns true if the
// request was rejected (the error response has already been written).
func rejectStdioMCPClientIfAuthBypassed(ctx *fasthttp.RequestCtx, connType string) bool {
	if connType != string(schemas.MCPConnectionTypeSTDIO) {
		return false
	}
	if bypassed, _ := ctx.UserValue(schemas.BifrostContextKeyAuthBypassed).(bool); !bypassed {
		return false
	}
	SendError(ctx, fasthttp.StatusForbidden, "Registering a stdio MCP client requires an authenticated admin session; dashboard auth is currently disabled or unconfigured. Enable dashboard authentication, or provision this client via config.json instead.")
	return true
}

// resourceCredentialResendRequired refuses an update that changes where the
// id_jag resource credential is sent (the audience it is discovered from,
// resource_token_endpoint, or the client assertion's audience) while keeping
// the stored secret or key through a masked
// placeholder: the stored credential was never paired with the new target.
// Mirrors oauth_config's rule for client_secret.
func resourceCredentialResendRequired(existing, req *schemas.MCPTokenExchangeConfig) error {
	if existing == nil || req == nil {
		return nil
	}
	// The audience counts too: without resource_token_endpoint the token
	// endpoint is discovered from it, so it decides where the secret goes.
	targetChanged := req.ResourceTokenEndpointURL() != existing.ResourceTokenEndpointURL() ||
		strings.TrimSpace(req.ResourceAssertionAudience) != strings.TrimSpace(existing.ResourceAssertionAudience) ||
		strings.TrimSpace(req.Audience) != strings.TrimSpace(existing.Audience)
	if targetChanged && (req.ResourceClientSecret.IsMaskedPlaceholder() || req.ResourcePrivateKey.IsMaskedPlaceholder()) {
		return fmt.Errorf("token_exchange.resource_client_secret and resource_private_key must be resent explicitly when audience, resource_token_endpoint or resource_assertion_audience changes")
	}
	return nil
}

// mcpClientSecretReferences lists the credential-bearing fields of an MCP
// client request whose value is an env./vault. reference rather than a literal.
// Keys are wire field names so a refusal can name what to change.
func mcpClientSecretReferences(headers map[string]schemas.SecretVar, connectionString *schemas.SecretVar, oauth *OAuthConfigRequest, tokenExchange *schemas.MCPTokenExchangeConfig, tlsConfig *schemas.MCPTLSConfig, openAPI *schemas.MCPOpenAPIConfig) []string {
	var refs []string
	isRef := func(v *schemas.SecretVar) bool { return v != nil && v.Type() != schemas.SecretTypePlainText }
	for name, value := range headers {
		if isRef(&value) {
			refs = append(refs, "headers."+name)
		}
	}
	sort.Strings(refs)
	if isRef(connectionString) {
		refs = append(refs, "connection_string")
	}
	if oauth != nil {
		if isRef(oauth.ClientID) {
			refs = append(refs, "oauth_config.client_id")
		}
		if isRef(oauth.ClientSecret) {
			refs = append(refs, "oauth_config.client_secret")
		}
	}
	if tokenExchange != nil {
		if isRef(tokenExchange.ClientID) {
			refs = append(refs, "token_exchange.client_id")
		}
		if isRef(tokenExchange.ClientSecret) {
			refs = append(refs, "token_exchange.client_secret")
		}
		if isRef(tokenExchange.ResourceClientID) {
			refs = append(refs, "token_exchange.resource_client_id")
		}
		if isRef(tokenExchange.ResourceClientSecret) {
			refs = append(refs, "token_exchange.resource_client_secret")
		}
		if isRef(tokenExchange.ResourcePrivateKey) {
			refs = append(refs, "token_exchange.resource_private_key")
		}
	}
	if tlsConfig != nil && isRef(tlsConfig.CACertPEM) {
		refs = append(refs, "tls_config.ca_cert_pem")
	}
	refs = append(refs, openAPICredentialSecretReferences(openAPI)...)
	return refs
}

// rejectSecretReferencesIfAuthBypassed refuses an MCP client create/update
// that resolves env./vault. references when the caller was let through only
// because dashboard auth is unconfigured or disabled. SecretVar expands those
// references from the gateway's own process environment or vault at decode
// time, and the resolved values are then sent to the client's target on
// connect. A real admin uses them to keep credentials out of config; a caller
// with no credential check must not be able to read the gateway's secrets by
// naming them. Literal values are unaffected, and config.json provisioning is
// not gated. Returns true if the request was rejected (the error response has
// already been written).
func rejectSecretReferencesIfAuthBypassed(ctx *fasthttp.RequestCtx, refs []string) bool {
	if len(refs) == 0 {
		return false
	}
	if bypassed, _ := ctx.UserValue(schemas.BifrostContextKeyAuthBypassed).(bool); !bypassed {
		return false
	}
	SendError(ctx, fasthttp.StatusForbidden, fmt.Sprintf("Using env./vault. references in MCP client fields (%s) requires an authenticated admin session; dashboard auth is currently disabled or unconfigured. Enable dashboard authentication, or provision this client via config.json instead.", strings.Join(refs, ", ")))
	return true
}

// addMCPClient handles POST /api/mcp/client - Add a new MCP client
// endpointSlugDerivable reports whether a usable /mcp/<slug> can be derived from the caller's
// endpoint_slug, or failing that the name. The create handlers check this before any upstream dial
// so an underivable slug fails with a 400 rather than after contacting the MCP server.
func endpointSlugDerivable(endpointSlug, name string) bool {
	base := endpointSlug
	if base == "" {
		base = name
	}
	return configstore.Slugify(base) != ""
}

func (h *MCPHandler) addMCPClient(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "MCP operations unavailable: config store is disabled")
		return
	}
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.store)
	defer cancel()

	var req MCPClientRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, requestDurationDecodeError(err))
		return
	}

	// connection_type is validated first: both gates below key on it, and an
	// unknown value would otherwise match neither and reach the connect path
	// unchecked (every later branch treats non-stdio as HTTP/SSE).
	switch schemas.MCPConnectionType(req.ConnectionType) {
	case schemas.MCPConnectionTypeHTTP, schemas.MCPConnectionTypeSSE, schemas.MCPConnectionTypeSTDIO, schemas.MCPConnectionTypeOpenAPI:
	default:
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid connection_type %q: must be one of http, sse, stdio, openapi", req.ConnectionType))
		return
	}
	isOpenAPIClient := req.ConnectionType == string(schemas.MCPConnectionTypeOpenAPI)

	// Both gates run before anything else: registering a client is what makes
	// Bifrost spawn the subprocess / dial the target, so a refusal has to land
	// before any of that work is scheduled.
	if rejectStdioMCPClientIfAuthBypassed(ctx, req.ConnectionType) {
		return
	}
	if rejectSecretReferencesIfAuthBypassed(ctx, mcpClientSecretReferences(req.Headers, req.ConnectionString, req.OauthConfig, req.TokenExchange, req.TLSConfig, req.OpenAPIConfig)) {
		return
	}
	if rejectPrivateMCPTargetIfAuthBypassed(ctx, req.ConnectionType, req.ConnectionString) {
		return
	}
	// The gate above only sees one DNS answer. Recording that this client was
	// registered with no credential check makes the MCP manager apply the
	// public-address rule on every dial for the client's lifetime. Server-set
	// only: whatever the request body carried for this field is overwritten.
	authBypassed, _ := ctx.UserValue(schemas.BifrostContextKeyAuthBypassed).(bool)
	req.RequirePublicTarget = authBypassed

	// Generate a unique client ID if not provided
	if req.ClientID == "" {
		req.ClientID = uuid.New().String()
	}

	if err := validateToolsToExecute(req.ToolsToExecute); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid tools_to_execute: %v", err))
		return
	}
	// Auto-clear tools_to_auto_execute if tools_to_execute is empty
	// If no tools are allowed to execute, no tools can be auto-executed
	if req.ToolsToExecute.IsEmpty() {
		req.ToolsToAutoExecute = schemas.WhiteList{}
	}
	if err := validateToolsToAutoExecute(req.ToolsToAutoExecute, req.ToolsToExecute); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid tools_to_auto_execute: %v", err))
		return
	}
	if err := validateExposeLists(req.PromptsToExpose, req.ResourcesToExpose); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	if err := mcp.ValidateMCPClientName(req.Name); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid client name: %v", err))
		return
	}
	// Reject an underivable endpoint slug up front, before any upstream verify/OAuth dial, so
	// invalid input fails fast with a 400 instead of after contacting the MCP server.
	if !endpointSlugDerivable(req.EndpointSlug, req.Name) {
		SendError(ctx, fasthttp.StatusBadRequest, "Could not derive an endpoint slug from the name; provide an endpoint_slug")
		return
	}
	if err := validateAllowedExtraHeaders(req.AllowedExtraHeaders); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid allowed_extra_headers: %v", err))
		return
	}
	if err := validateNeedsSessionStickiness(req.NeedsSessionStickiness, schemas.MCPConnectionType(req.ConnectionType)); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}

	// openapi: the synthesized server has no remote MCP handshake, so only the
	// header-shaped auth types apply; the spec is validated, inlined and
	// synthesized now so a bad document or an unknown tool name fails here with
	// a 400 rather than after the row is written. The upstream base URL is
	// subject to the same public-target rule as a connection_string.
	var preparedOpenAPI *preparedOpenAPIClient
	if isOpenAPIClient {
		switch req.AuthType {
		case "", string(schemas.MCPAuthTypeNone), string(schemas.MCPAuthTypeHeaders), string(schemas.MCPAuthTypePerUserHeaders):
		default:
			SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("auth_type %q is not supported for connection_type 'openapi' (use none, headers or per_user_headers)", req.AuthType))
			return
		}
		if req.ConnectionString != nil && strings.TrimSpace(req.ConnectionString.GetValue()) != "" {
			SendError(ctx, fasthttp.StatusBadRequest, "connection_string must not be set for connection_type 'openapi'; the upstream is openapi_config.base_url")
			return
		}
		req.ConnectionString = nil
		if msg, refused := requestSpecFileRefusal(req.OpenAPIConfig); refused {
			SendError(ctx, fasthttp.StatusBadRequest, msg)
			return
		}
		prepared, status, msg := prepareOpenAPIConfig(ctx, req.Name, req.OpenAPIConfig)
		if status != 0 {
			SendError(ctx, status, msg)
			return
		}
		if unknown := unknownOpenAPITools(req.ToolsToExecute, prepared.toolNames); len(unknown) > 0 {
			SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid tools_to_execute: the spec defines no tool named %s", strings.Join(unknown, ", ")))
			return
		}
		if unknown := unknownOpenAPITools(req.ToolsToAutoExecute, prepared.toolNames); len(unknown) > 0 {
			SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid tools_to_auto_execute: the spec defines no tool named %s", strings.Join(unknown, ", ")))
			return
		}
		preparedOpenAPI = prepared
		if req.AuthType == string(schemas.MCPAuthTypePerUserHeaders) {
			// Same schema checks the per-user-headers branch below applies;
			// there is no upstream MCP server to verify against, so the
			// client takes the default create path and connects directly.
			if len(req.PerUserHeaderKeys) == 0 {
				SendError(ctx, fasthttp.StatusBadRequest, "per_user_header_keys must be a non-empty list when auth_type is 'per_user_headers'")
				return
			}
			req.PerUserHeaderKeys = mcputils.CanonicalizeHeaderKeys(req.PerUserHeaderKeys)
			for i, key := range req.PerUserHeaderKeys {
				if key == "" {
					SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("per_user_header_keys[%d] is empty", i))
					return
				}
			}
			if lib.HasDuplicates(req.PerUserHeaderKeys) {
				SendError(ctx, fasthttp.StatusBadRequest, "per_user_header_keys contains duplicate entries")
				return
			}
			if msg, refused := perUserHeaderCredentialConflict(prepared.schemes, prepared.config.SecurityCredentials, req.PerUserHeaderKeys); refused {
				SendError(ctx, fasthttp.StatusBadRequest, msg)
				return
			}
		}
	}

	// tool_execution_timeout: 0 (unset) means "use global from
	// tool_manager_config", matching TableMCPClient's own column semantics —
	// req.ToolExecutionTimeout is a plain int (embedded from TableMCPClient),
	// not a pointer, so 0 can't be distinguished from "not sent"; same
	// convention tool_sync_interval already uses on this create path.
	// Computed once here and reused across every creation branch below.
	if req.ToolExecutionTimeout < 0 {
		SendError(ctx, fasthttp.StatusBadRequest, "tool_execution_timeout must not be negative")
		return
	}
	if err := schemas.ValidateMCPMaxProtocolVersion("max_protocol_version", req.MaxProtocolVersion); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	// tool_sync_interval accepts a duration string or a bare number of
	// minutes; 0 (or absent) means follow the global setting. Resolved once
	// here and reused across every creation branch below.
	resolvedToolSyncInterval, err := req.ToolSyncInterval.Resolve(0)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	if req.MaxInstructionsLength < 0 {
		SendError(ctx, fasthttp.StatusBadRequest, "max_instructions_length must not be negative")
		return
	}
	resolvedToolExecutionTimeout := time.Duration(req.ToolExecutionTimeout) * time.Second
	resolvedMaxInstructionsLength := req.MaxInstructionsLength

	// Handle per-user headers: admin declares the required key names (schema)
	// AND supplies a sample set of values inline so the server can verify
	// upstream + discover tools in a single round-trip. Mirrors the per-user
	// OAuth flow: the sample values verify the upstream and are then kept as
	// the client's admin credential, the same one verify-headers keeps, so
	// periodic tool discovery can connect with them. They are never used for a
	// caller's tool call; each end-user submits their own values later via the
	// inline-401 flow.
	if req.AuthType == string(schemas.MCPAuthTypePerUserHeaders) && !isOpenAPIClient {
		if len(req.PerUserHeaderKeys) == 0 {
			SendError(ctx, fasthttp.StatusBadRequest, "per_user_header_keys must be a non-empty list when auth_type is 'per_user_headers'")
			return
		}
		// Canonicalize (lowercase + trim) at the request boundary so the
		// stored schema, credential rows, and runtime comparisons all
		// agree on one form. See the invariant doc on
		// mcputils.CanonicalizeHeaderKey — defensive case-folding on the
		// read side was removed in favor of write-side normalization, so
		// every key that enters this handler MUST go through here before
		// it reaches the schemas/store layer.
		canonHeaderKeys := mcputils.CanonicalizeHeaderKeys(req.PerUserHeaderKeys)
		for i, key := range canonHeaderKeys {
			if key == "" {
				SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("per_user_header_keys[%d] is empty", i))
				return
			}
		}
		// HTTP header names are case-insensitive on the wire — reject duplicates
		// like ["X-Api-Key", "x-api-key"] so downstream change-detection and
		// credential storage stay correct. Run the dup check on the canon
		// form so case-only collisions are caught.
		if lib.HasDuplicates(canonHeaderKeys) {
			SendError(ctx, fasthttp.StatusBadRequest, "per_user_header_keys contains duplicate entries")
			return
		}
		// Canonicalize the admin's sample header values too so the
		// "missing values for required keys" check matches by canonical
		// form. Without this, a UI that sends "Authorization" as a key
		// and "authorization" as a value-map entry would spuriously fail.
		canonUserHeaders := mcputils.CanonicalizeHeaderMap(req.UserHeaders)
		if missing := missingPerUserHeaderValues(canonHeaderKeys, canonUserHeaders); len(missing) > 0 {
			SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("sample user_headers missing values for required keys: %s", strings.Join(missing, ", ")))
			return
		}

		isPingAvailable := true
		if req.IsPingAvailable != nil {
			isPingAvailable = *req.IsPingAvailable
		}

		schemasConfig := &schemas.MCPClientConfig{
			ID:                     req.ClientID,
			Name:                   req.Name,
			EndpointSlug:           req.EndpointSlug,
			IsCodeModeClient:       req.IsCodeModeClient,
			IsPingAvailable:        &isPingAvailable,
			NeedsSessionStickiness: req.NeedsSessionStickiness,
			ToolSyncInterval:       resolvedToolSyncInterval,
			MaxProtocolVersion:     req.MaxProtocolVersion,
			ToolExecutionTimeout:   resolvedToolExecutionTimeout,
			MaxInstructionsLength:  resolvedMaxInstructionsLength,
			ConnectionType:         schemas.MCPConnectionType(req.ConnectionType),
			ConnectionString:       req.ConnectionString,
			RequirePublicTarget:    req.RequirePublicTarget,
			StdioConfig:            req.StdioConfig,
			AuthType:               schemas.MCPAuthTypePerUserHeaders,
			PerUserHeaderKeys:      canonHeaderKeys,
			ToolsToExecute:         req.ToolsToExecute,
			ToolsToAutoExecute:     req.ToolsToAutoExecute,
			PromptsToExpose:        req.PromptsToExpose,
			ResourcesToExpose:      req.ResourcesToExpose,
			ToolPricing:            req.ToolPricing,
			Headers:                req.Headers,
			AllowedExtraHeaders:    req.AllowedExtraHeaders,
			AllowByDefault:         req.AllowByDefault,
		}

		// Verify connection and discover tools using the admin's sample
		// header values. Discovered tools land on schemasConfig before we
		// persist so the DB row includes them from the start — same
		// convention as the per-user OAuth branch below. Pass the canon
		// form so the verify path sees the same keys the schema declares.
		discovery, verifyErr := h.mcpManager.VerifyHeadersConnection(bifrostCtx, schemasConfig, canonUserHeaders)
		if verifyErr != nil {
			SendError(ctx, fasthttp.StatusUnprocessableEntity, fmt.Sprintf("Verification failed: %v", verifyErr))
			return
		}
		tools, toolNameMapping, serverInstructions := discovery.Tools, discovery.ToolNameMapping, discovery.Instructions
		schemasConfig.DiscoveredTools = tools
		schemasConfig.DiscoveredToolNameMapping = toolNameMapping
		schemasConfig.DiscoveredResources, schemasConfig.DiscoveredResourceTemplates, schemasConfig.DiscoveredPrompts = discovery.Resources, discovery.ResourceTemplates, discovery.Prompts
		schemasConfig.DiscoveredInstructions = serverInstructions

		if err := h.store.ConfigStore.CreateMCPClientConfig(ctx, schemasConfig); err != nil {
			if errors.Is(err, configstore.ErrAlreadyExists) {
				SendError(ctx, fasthttp.StatusConflict, "An MCP client with this name already exists")
				return
			}
			if errors.Is(err, configstore.ErrMCPEndpointSlugExists) {
				SendError(ctx, fasthttp.StatusConflict, "An MCP endpoint with this slug already exists")
				return
			}
			if errors.Is(err, configstore.ErrMCPEndpointSlugInvalid) {
				SendError(ctx, fasthttp.StatusBadRequest, "Could not derive an endpoint slug from the name; provide an endpoint_slug")
				return
			}
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to create MCP config: %v", err))
			return
		}
		// Keep the verified sample values as the client's admin credential
		// before the client is registered, so its connection checker finds the
		// credential on its first periodic discovery. A failed registration
		// below deletes the stored client, and that delete removes its header
		// credentials with it. Best-effort, as in verify-headers: the client is
		// verified and serves callers either way, and verify-headers can store
		// the credential later.
		if h.store.MCPHeadersProvider != nil {
			if err := h.store.MCPHeadersProvider.UpsertCredential(ctx, &schemas.MCPHeadersUserCredential{
				MCPClientID: schemasConfig.ID,
				AuthMode:    schemas.MCPAuthModeAdmin,
				Headers:     canonUserHeaders,
				Status:      schemas.MCPHeadersUserCredentialStatusActive,
			}); err != nil {
				logger.Warn(fmt.Sprintf("failed to retain admin header credential for MCP client %s: %v", schemasConfig.ID, err))
			}
		}
		if err := h.mcpManager.AddMCPClient(bifrostCtx, schemasConfig); err != nil {
			if delErr := h.store.ConfigStore.DeleteMCPClientConfig(ctx, schemasConfig.ID); delErr != nil {
				logger.Error(fmt.Sprintf("Failed to roll back MCP client config after AddMCPClient failure: %v", delErr))
			}
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to register MCP client: %v", err))
			return
		}

		lib.RecordAuditChange(ctx, "mcp_client.created", "mcp_client", schemasConfig.ID, schemasConfig.Name, nil, auditMCPClient(h.store, schemasConfig))
		SendJSON(ctx, map[string]any{
			"status":  "success",
			"message": fmt.Sprintf("MCP client registered. %d tools discovered. Each user will submit their own headers on first tool use.", len(tools)),
		})
		return
	}

	// Handle token exchange: the caller's identity-provider token is exchanged
	// per request at runtime, so nothing interactive happens at create.
	// Verification + tool discovery run synchronously when the admin has a
	// token to exchange: the request's own, or the one stored from their
	// sign-in. Without one the client is parked in pending_verification for
	// the verify-exchange endpoint.
	if req.AuthType == string(schemas.MCPAuthTypeTokenExchange) {
		if isEnterprise, _ := bifrostCtx.Value(schemas.BifrostContextKeyIsEnterprise).(bool); !isEnterprise {
			SendError(ctx, fasthttp.StatusBadRequest, "auth_type 'token_exchange' is not supported")
			return
		}
		if h.store.OAuthProvider == nil || !h.store.OAuthProvider.TokenExchangeAvailable() {
			SendError(ctx, fasthttp.StatusBadRequest, "auth_type 'token_exchange' requires user-identity authentication with an exchange client to be configured")
			return
		}
		if req.TokenExchange == nil || strings.TrimSpace(req.TokenExchange.Audience) == "" {
			SendError(ctx, fasthttp.StatusBadRequest, "token_exchange.audience is required when auth_type is 'token_exchange'")
			return
		}
		if !req.TokenExchange.ExchangesAsSSOApplication() && strings.TrimSpace(req.TokenExchange.ClientID.GetValue()) == "" && !req.TokenExchange.ClientID.IsFromSecret() {
			SendError(ctx, fasthttp.StatusBadRequest, "token_exchange.client_id is required when auth_type is 'token_exchange' and use_idp_credentials is not set")
			return
		}
		if err := req.TokenExchange.ValidateProfile(); err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, err.Error())
			return
		}
		// The identity-provider integration may refuse these settings;
		// asking it now reports that to the editor at save rather than on
		// the first tool call.
		if err := h.store.OAuthProvider.ValidateTokenExchangeConfig(ctx, req.TokenExchange); err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, err.Error())
			return
		}
		// Token-exchange clients rely on requests that may carry both an
		// identity token and a virtual key; the 'error' conflict behavior
		// would reject exactly those requests, so the two settings are
		// mutually exclusive (enforced in both directions — see the
		// client-config update path).
		clientConfig, cfgErr := h.store.ConfigStore.GetClientConfig(ctx)
		if cfgErr != nil {
			// Fail closed: silently skipping this check on a read error
			// would let a token_exchange client be created while
			// dual_credential_conflict_behavior is still 'error', the exact
			// pairing this validation exists to prevent.
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to load client config to validate dual_credential_conflict_behavior: %v", cfgErr))
			return
		}
		if clientConfig != nil && clientConfig.DualCredentialConflictBehavior == configstoreTables.DualCredentialConflictBehaviorError {
			SendError(ctx, fasthttp.StatusBadRequest, "auth_type 'token_exchange' cannot be used while dual_credential_conflict_behavior is 'error': change it to 'prefer_idp' or 'prefer_vk' first")
			return
		}

		isPingAvailable := true
		if req.IsPingAvailable != nil {
			isPingAvailable = *req.IsPingAvailable
		}

		schemasConfig := &schemas.MCPClientConfig{
			ID:                     req.ClientID,
			Name:                   req.Name,
			EndpointSlug:           req.EndpointSlug,
			IsCodeModeClient:       req.IsCodeModeClient,
			IsPingAvailable:        &isPingAvailable,
			NeedsSessionStickiness: req.NeedsSessionStickiness,
			ToolSyncInterval:       resolvedToolSyncInterval,
			MaxProtocolVersion:     req.MaxProtocolVersion,
			ToolExecutionTimeout:   resolvedToolExecutionTimeout,
			MaxInstructionsLength:  resolvedMaxInstructionsLength,
			ConnectionType:         schemas.MCPConnectionType(req.ConnectionType),
			ConnectionString:       req.ConnectionString,
			RequirePublicTarget:    req.RequirePublicTarget,
			StdioConfig:            req.StdioConfig,
			TLSConfig:              req.TLSConfig,
			AuthType:               schemas.MCPAuthTypeTokenExchange,
			TokenExchange:          req.TokenExchange,
			ToolsToExecute:         req.ToolsToExecute,
			ToolsToAutoExecute:     req.ToolsToAutoExecute,
			PromptsToExpose:        req.PromptsToExpose,
			ResourcesToExpose:      req.ResourcesToExpose,
			ToolPricing:            req.ToolPricing,
			Headers:                req.Headers,
			AllowedExtraHeaders:    req.AllowedExtraHeaders,
			AllowByDefault:         req.AllowByDefault,
		}

		// Resolve an admin credential for synchronous verification + tool
		// discovery, mirroring the other per-user branches: the signed-in
		// admin's own identity-provider token (the one the request carries,
		// or the one stored from the admin's sign-in) is exchanged exactly
		// like a real caller's would be ("verify as yourself"; there is no
		// manual token input). Without one (e.g. API-key authentication) the
		// client is created in pending_verification for verify-exchange, which
		// the admin must hit from an identity-authenticated session. An
		// expired stored sign-in fails the create instead, since signing in
		// again lets it verify at once. The full response is retained after
		// activation as the admin discovery credential (see the retention
		// block below).
		var adminResponse *schemas.OAuth2TokenExchangeResponse
		response, exchangeErr := h.store.OAuthProvider.ExchangeAdminCredential(bifrostCtx, schemasConfig)
		switch {
		case errors.Is(exchangeErr, schemas.ErrExchangeSubjectTokenMissing):
			// Nothing to verify as: created in pending_verification.
		case errors.Is(exchangeErr, schemas.ErrStoredSignInExpired):
			SendError(ctx, fasthttp.StatusUnprocessableEntity, "Admin credential exchange failed: your Bifrost sign-in has expired. Sign in to Bifrost again and retry")
			return
		case exchangeErr != nil:
			SendError(ctx, fasthttp.StatusUnprocessableEntity, fmt.Sprintf("Admin credential exchange failed: %v", exchangeErr))
			return
		default:
			adminResponse = response
			discovery, verifyErr := h.mcpManager.VerifyPerUserOAuthConnection(bifrostCtx, schemasConfig, response.AccessToken)
			if verifyErr != nil {
				SendError(ctx, fasthttp.StatusUnprocessableEntity, fmt.Sprintf("Verification failed: %v", verifyErr))
				return
			}
			tools, toolNameMapping, serverInstructions := discovery.Tools, discovery.ToolNameMapping, discovery.Instructions
			schemasConfig.DiscoveredTools = tools
			schemasConfig.DiscoveredToolNameMapping = toolNameMapping
			schemasConfig.DiscoveredResources, schemasConfig.DiscoveredResourceTemplates, schemasConfig.DiscoveredPrompts = discovery.Resources, discovery.ResourceTemplates, discovery.Prompts
			schemasConfig.DiscoveredInstructions = serverInstructions
		}

		if err := h.store.ConfigStore.CreateMCPClientConfig(ctx, schemasConfig); err != nil {
			if errors.Is(err, configstore.ErrAlreadyExists) {
				SendError(ctx, fasthttp.StatusConflict, "An MCP client with this name already exists")
				return
			}
			if errors.Is(err, configstore.ErrMCPEndpointSlugExists) {
				SendError(ctx, fasthttp.StatusConflict, "An MCP endpoint with this slug already exists")
				return
			}
			if errors.Is(err, configstore.ErrMCPEndpointSlugInvalid) {
				SendError(ctx, fasthttp.StatusBadRequest, "Could not derive an endpoint slug from the name; provide an endpoint_slug")
				return
			}
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to create MCP config: %v", err))
			return
		}
		if err := h.mcpManager.AddMCPClient(bifrostCtx, schemasConfig); err != nil {
			if delErr := h.store.ConfigStore.DeleteMCPClientConfig(ctx, schemasConfig.ID); delErr != nil {
				logger.Error(fmt.Sprintf("Failed to roll back MCP client config after AddMCPClient failure: %v", delErr))
			}
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to register MCP client: %v", err))
			return
		}

		// Retain the admin credential for the periodic tool syncer and the
		// refresh worker, mirroring the per-user OAuth promotion and the
		// per-user-headers admin upsert. Deliberately after activation and
		// persistence, and best-effort: a failed retention doesn't fail the
		// create — the client is verified and serving, tool-list refresh just
		// stays unavailable until verify-exchange retains a fresh credential.
		if adminResponse != nil {
			if retainErr := h.store.OAuthProvider.RetainExchangeAdminCredential(ctx, schemasConfig, adminResponse); retainErr != nil {
				logger.Warn(fmt.Sprintf("failed to retain admin exchange credential for MCP client %s: %v", schemasConfig.ID, retainErr))
			}
		}

		message := fmt.Sprintf("MCP client registered. %d tools discovered. Callers' identity tokens are exchanged automatically on each tool use.", len(schemasConfig.DiscoveredTools))
		if adminResponse == nil {
			message = "MCP client registered in pending verification. Verify it from an identity-authenticated session to discover tools."
		}
		lib.RecordAuditChange(ctx, "mcp_client.created", "mcp_client", schemasConfig.ID, schemasConfig.Name, nil, auditMCPClient(h.store, schemasConfig))
		SendJSON(ctx, map[string]any{
			"status":  "success",
			"message": message,
		})
		return
	}

	// Handle per-user OAuth: admin does a test OAuth login to verify the configuration.
	// Uses the same pending_oauth pattern as server-level OAuth, but on completion we
	// verify the connection, discover tools, save the client, and discard the admin's token.
	if req.AuthType == string(schemas.MCPAuthTypePerUserOauth) {
		if req.OauthConfig == nil {
			SendError(ctx, fasthttp.StatusBadRequest, "OAuth configuration is required when auth_type is 'per_user_oauth'")
			return
		}

		if !req.OauthConfig.ClientID.IsSet() && req.ConnectionString.GetValue() == "" {
			SendError(ctx, fasthttp.StatusBadRequest, "Either client_id must be provided, or server URL must be set for OAuth discovery and dynamic client registration")
			return
		}

		flowInitiation, err := h.runOAuthBootstrap(ctx, req.OauthConfig, req.ConnectionString.GetValue(), req.RequirePublicTarget)
		if err != nil {
			SendError(ctx, oauthBootstrapErrorStatus(err), fmt.Sprintf("Failed to initiate OAuth flow: %v", err))
			return
		}

		isPingAvailable := true
		if req.IsPingAvailable != nil {
			isPingAvailable = *req.IsPingAvailable
		}

		pendingConfig := schemas.MCPClientConfig{
			ID:                     req.ClientID,
			Name:                   req.Name,
			IsCodeModeClient:       req.IsCodeModeClient,
			IsPingAvailable:        &isPingAvailable,
			NeedsSessionStickiness: req.NeedsSessionStickiness,
			ToolSyncInterval:       resolvedToolSyncInterval,
			MaxProtocolVersion:     req.MaxProtocolVersion,
			ToolExecutionTimeout:   resolvedToolExecutionTimeout,
			MaxInstructionsLength:  resolvedMaxInstructionsLength,
			ConnectionType:         schemas.MCPConnectionType(req.ConnectionType),
			ConnectionString:       req.ConnectionString,
			RequirePublicTarget:    req.RequirePublicTarget,
			StdioConfig:            req.StdioConfig,
			TLSConfig:              req.TLSConfig,
			AuthType:               schemas.MCPAuthTypePerUserOauth,
			OauthConfigID:          &flowInitiation.OauthConfigID,
			ToolsToExecute:         req.ToolsToExecute,
			ToolsToAutoExecute:     req.ToolsToAutoExecute,
			PromptsToExpose:        req.PromptsToExpose,
			ResourcesToExpose:      req.ResourcesToExpose,
			ToolPricing:            req.ToolPricing,
			Headers:                req.Headers,
			AllowedExtraHeaders:    req.AllowedExtraHeaders,
			AllowByDefault:         req.AllowByDefault,
		}

		if err := h.oauthHandler.StorePendingMCPClient(flowInitiation.OauthConfigID, pendingConfig); err != nil {
			logger.Error(fmt.Sprintf("[Add MCP Client] Failed to store pending MCP client: %v", err))
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to store pending MCP client: %v", err))
			return
		}

		// Mirror the server-level OAuth response's next-step hints so API/CLI
		// users can complete the flow without consulting docs.
		completeURL := fmt.Sprintf("/api/mcp/client/%s/complete-oauth", flowInitiation.OauthConfigID)
		statusURL := fmt.Sprintf("/api/oauth/config/%s/status", flowInitiation.OauthConfigID)
		SendJSON(ctx, map[string]any{
			"status":          "pending_oauth",
			"message":         "Test OAuth configuration: please authorize to verify the setup. This login verifies connectivity and discovers the available tools, and is then kept as the client's admin credential for periodic tool discovery; it is never used for a user's tool calls.",
			"oauth_config_id": flowInitiation.OauthConfigID,
			"authorize_url":   flowInitiation.AuthorizeURL,
			"expires_at":      flowInitiation.ExpiresAt,
			"mcp_client_id":   req.ClientID,
			"complete_url":    completeURL,
			"status_url":      statusURL,
			"next_steps": []string{
				"1. Open authorize_url in a browser to approve access",
				"2. Poll status_url to check when status becomes 'authorized'",
				"3. POST complete_url to verify connectivity and activate the MCP client",
			},
		})
		return
	}

	// Check if server-level OAuth flow is needed
	if req.AuthType == string(schemas.MCPAuthTypeOauth) {
		if req.OauthConfig == nil {
			SendError(ctx, fasthttp.StatusBadRequest, "OAuth configuration is required when auth_type is 'oauth'")
			return
		}

		// Validate: Either client_id must be provided, OR we need a server URL for discovery + dynamic registration
		// Client ID can be empty if the OAuth provider supports dynamic client registration (RFC 7591)
		if !req.OauthConfig.ClientID.IsSet() {
			// If no client_id, we need server URL for discovery
			if req.ConnectionString.GetValue() == "" {
				SendError(ctx, fasthttp.StatusBadRequest, "Either client_id must be provided, or server URL must be set for OAuth discovery and dynamic client registration")
				return
			}
			// Note: The InitiateOAuthFlow will check if registration_endpoint is available
			// and return a clear error if dynamic registration is not supported
		}

		// Kick off the shared OAuth flow. See runOAuthBootstrap for the
		// rationale on the helper split. ServerURL is the MCP server URL
		// used for RFC 8414 discovery; ClientID is optional and will be
		// obtained via RFC 7591 dynamic registration when absent.
		flowInitiation, err := h.runOAuthBootstrap(ctx, req.OauthConfig, req.ConnectionString.GetValue(), req.RequirePublicTarget)
		if err != nil {
			SendError(ctx, oauthBootstrapErrorStatus(err), fmt.Sprintf("Failed to initiate OAuth flow: %v", err))
			return
		}

		// Store MCP client config in OAuth provider memory (not in database)
		// It will be stored in database only after OAuth completion
		pendingConfig := schemas.MCPClientConfig{
			ID:                     req.ClientID,
			Name:                   req.Name,
			IsCodeModeClient:       req.IsCodeModeClient,
			IsPingAvailable:        req.IsPingAvailable,
			NeedsSessionStickiness: req.NeedsSessionStickiness,
			ToolSyncInterval:       resolvedToolSyncInterval,
			MaxProtocolVersion:     req.MaxProtocolVersion,
			ToolExecutionTimeout:   resolvedToolExecutionTimeout,
			MaxInstructionsLength:  resolvedMaxInstructionsLength,
			ConnectionType:         schemas.MCPConnectionType(req.ConnectionType),
			ConnectionString:       req.ConnectionString,
			RequirePublicTarget:    req.RequirePublicTarget,
			StdioConfig:            req.StdioConfig,
			TLSConfig:              req.TLSConfig,
			AuthType:               schemas.MCPAuthType(req.AuthType),
			OauthConfigID:          &flowInitiation.OauthConfigID,
			ToolsToExecute:         req.ToolsToExecute,
			ToolsToAutoExecute:     req.ToolsToAutoExecute,
			PromptsToExpose:        req.PromptsToExpose,
			ResourcesToExpose:      req.ResourcesToExpose,
			Headers:                req.Headers,
			AllowedExtraHeaders:    req.AllowedExtraHeaders,
			ToolPricing:            req.ToolPricing,
			AllowByDefault:         req.AllowByDefault,
		}

		// Store pending config in database (associated with oauth_config_id for multi-instance support)
		if err := h.oauthHandler.StorePendingMCPClient(flowInitiation.OauthConfigID, pendingConfig); err != nil {
			logger.Error(fmt.Sprintf("[Add MCP Client] Failed to store pending MCP client: %v", err))
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to store pending MCP client: %v", err))
			return
		}

		// Return OAuth flow initiation response with actionable next-step hints
		// so API/CLI users know how to complete the flow without consulting docs.
		completeURL := fmt.Sprintf("/api/mcp/client/%s/complete-oauth", flowInitiation.OauthConfigID)
		statusURL := fmt.Sprintf("/api/oauth/config/%s/status", flowInitiation.OauthConfigID)
		SendJSON(ctx, map[string]any{
			"status":          "pending_oauth",
			"message":         "OAuth authorization required",
			"oauth_config_id": flowInitiation.OauthConfigID,
			"authorize_url":   flowInitiation.AuthorizeURL,
			"expires_at":      flowInitiation.ExpiresAt,
			"mcp_client_id":   req.ClientID,
			"complete_url":    completeURL,
			"status_url":      statusURL,
			"next_steps": []string{
				"1. Open authorize_url in a browser to approve access",
				"2. Poll status_url to check when status becomes 'authorized'",
				"3. POST complete_url to activate the MCP client",
			},
		})
		return
	}

	// Convert to schemas.MCPClientConfig for runtime bifrost client (without tool_pricing)
	schemasConfig := &schemas.MCPClientConfig{
		ID:                     req.ClientID,
		Name:                   req.Name,
		EndpointSlug:           req.EndpointSlug,
		IsCodeModeClient:       req.IsCodeModeClient,
		ConnectionType:         schemas.MCPConnectionType(req.ConnectionType),
		ConnectionString:       req.ConnectionString,
		RequirePublicTarget:    req.RequirePublicTarget,
		StdioConfig:            req.StdioConfig,
		TLSConfig:              req.TLSConfig,
		ToolsToExecute:         req.ToolsToExecute,
		ToolsToAutoExecute:     req.ToolsToAutoExecute,
		PromptsToExpose:        req.PromptsToExpose,
		ResourcesToExpose:      req.ResourcesToExpose,
		Headers:                req.Headers,
		AllowedExtraHeaders:    req.AllowedExtraHeaders,
		AuthType:               schemas.MCPAuthType(req.AuthType),
		OauthConfigID:          req.OauthConfigID,
		IsPingAvailable:        req.IsPingAvailable,
		NeedsSessionStickiness: req.NeedsSessionStickiness,
		ToolSyncInterval:       resolvedToolSyncInterval,
		MaxProtocolVersion:     req.MaxProtocolVersion,
		ToolExecutionTimeout:   resolvedToolExecutionTimeout,
		MaxInstructionsLength:  resolvedMaxInstructionsLength,
		ToolPricing:            req.ToolPricing,
		AllowByDefault:         req.AllowByDefault,
	}
	if preparedOpenAPI != nil {
		schemasConfig.OpenAPIConfig = preparedOpenAPI.config
		schemasConfig.PerUserHeaderKeys = req.PerUserHeaderKeys
	}

	// Creating MCP client config in config store
	if h.store.ConfigStore != nil {
		if err := h.store.ConfigStore.CreateMCPClientConfig(ctx, schemasConfig); err != nil {
			if errors.Is(err, configstore.ErrAlreadyExists) {
				SendError(ctx, fasthttp.StatusConflict, "An MCP client with this name already exists")
				return
			}
			if errors.Is(err, configstore.ErrMCPEndpointSlugExists) {
				SendError(ctx, fasthttp.StatusConflict, "An MCP endpoint with this slug already exists")
				return
			}
			if errors.Is(err, configstore.ErrMCPEndpointSlugInvalid) {
				SendError(ctx, fasthttp.StatusBadRequest, "Could not derive an endpoint slug from the name; provide an endpoint_slug")
				return
			}
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to create MCP config: %v", err))
			return
		}
	}
	if err := h.mcpManager.AddMCPClient(bifrostCtx, schemasConfig); err != nil {
		// Delete the created config from config store
		if h.store.ConfigStore != nil {
			if err := h.store.ConfigStore.DeleteMCPClientConfig(ctx, schemasConfig.ID); err != nil {
				logger.Error(fmt.Sprintf("Failed to delete MCP client config from database: %v. please restart bifrost to keep core and database in sync", err))
				SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to delete MCP client config from database: %v. please restart bifrost to keep core and database in sync", err))
				return
			}
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to connect MCP client: %v", err))
		return
	}

	lib.RecordAuditChange(ctx, "mcp_client.created", "mcp_client", schemasConfig.ID, schemasConfig.Name, nil, auditMCPClient(h.store, schemasConfig))
	SendJSON(ctx, map[string]any{
		"status":  "success",
		"message": "MCP client connected successfully",
	})
}

// updateMCPClient handles PUT /api/mcp/client/{id} - Edit MCP client
func (h *MCPHandler) updateMCPClient(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "MCP operations unavailable: config store is disabled")
		return
	}
	id, err := getIDFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid id: %v", err))
		return
	}
	var req MCPClientUpdateRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, requestDurationDecodeError(err))
		return
	}

	// Fetch existing config first — needed to resolve optional fields before validation.
	var existingConfig *schemas.MCPClientConfig
	if h.store.MCPConfig != nil {
		for i, client := range h.store.MCPConfig.ClientConfigs {
			if client.ID == id {
				existingConfig = h.store.MCPConfig.ClientConfigs[i]
				break
			}
		}
	}
	if existingConfig == nil {
		SendError(ctx, fasthttp.StatusNotFound, "MCP client not found")
		return
	}
	before := auditMCPClient(h.store, existingConfig)
	if rejectSecretReferencesIfAuthBypassed(ctx, mcpClientSecretReferences(req.Headers, nil, req.OauthConfig, req.TokenExchange, req.TLSConfig, req.OpenAPIConfig)) {
		return
	}
	if err := validateNeedsSessionStickiness(req.NeedsSessionStickiness, existingConfig.ConnectionType); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	if req.OpenAPIConfig != nil && existingConfig.ConnectionType != schemas.MCPConnectionTypeOpenAPI {
		SendError(ctx, fasthttp.StatusBadRequest, "openapi_config can only be updated for MCP clients with connection_type 'openapi'")
		return
	}
	// Snapshot fields we need to diff against the resolved values AFTER UpdateMCPClient
	// runs further below — UpdateMCPClient mutates the *MCPClientConfig in place (it's
	// the same pointer the manager holds in MCPConfig.ClientConfigs), so post-update
	// reads would already reflect the new value and the diff would always be false.
	//
	// PerUserHeaderKeys is snapshotted via append (independent backing array) rather
	// than a bare slice-header copy, so we're safe if a future change mutates the
	// slice contents in-place instead of reassigning the header.
	existingAllowByDefault := existingConfig.AllowByDefault
	existingPerUserHeaderKeys := append([]string(nil), existingConfig.PerUserHeaderKeys...)

	// Resolve all mutable fields with PATCH semantics: use the provided value if
	// present, otherwise fall back to the existing value.
	name := existingConfig.Name
	if req.Name != nil {
		name = *req.Name
	}
	disabled := existingConfig.Disabled
	if req.Disabled != nil {
		disabled = *req.Disabled
	}
	allowByDefault := req.resolvedAllowByDefault(existingConfig.AllowByDefault)
	isCodeMode := existingConfig.IsCodeModeClient
	if req.IsCodeModeClient != nil {
		isCodeMode = *req.IsCodeModeClient
	}
	isPingAvailable := existingConfig.IsPingAvailable
	if req.IsPingAvailable != nil {
		isPingAvailable = req.IsPingAvailable
	}
	needsSessionStickiness := existingConfig.NeedsSessionStickiness
	if req.NeedsSessionStickiness != nil {
		needsSessionStickiness = req.NeedsSessionStickiness
	}
	toolPricing := existingConfig.ToolPricing
	if req.ToolPricing != nil {
		toolPricing = req.ToolPricing
	}
	allowedExtraHeaders := existingConfig.AllowedExtraHeaders
	if req.AllowedExtraHeaders != nil {
		allowedExtraHeaders = *req.AllowedExtraHeaders
	}
	// Headers: merge incoming with existing, preserving redacted values that are unchanged.
	headers := existingConfig.Headers
	if req.Headers != nil {
		redactedExisting := h.store.RedactMCPClientConfig(existingConfig)
		headers = mergeMCPHeaders(req.Headers, existingConfig.Headers, redactedExisting.Headers)
	}
	// A real change to the static headers has to be applied to the live
	// connection, not just to the stored config: on a sticky client the
	// credential is baked onto the transport at dial time, so updating the
	// config alone would leave the open connection talking to the upstream
	// with the header the admin just replaced. Diffed against the resolved
	// (post-redaction-merge) value so a caller that round-trips the map
	// unchanged doesn't force a needless reconnect. Only shared auth types
	// carry a connection-level header credential — per-user values are
	// supplied per caller and never baked into a shared transport. HTTP only,
	// matching the transport VerifyHeadersConnection builds for the
	// pre-flight below.
	// Skipped for a disabled client: there is no live connection to protect,
	// and refusing the edit because a deliberately-unreachable upstream can't
	// be dialed would make a disabled client's credentials uneditable. It is
	// verified on enable instead, which dials for real.
	staticHeadersChanged := (existingConfig.AuthType == schemas.MCPAuthTypeHeaders || existingConfig.AuthType == schemas.MCPAuthTypeNone) &&
		existingConfig.ConnectionType == schemas.MCPConnectionTypeHTTP &&
		!disabled &&
		req.Headers != nil && !mcpHeadersEqual(existingConfig.Headers, headers)
	headersChanged := staticHeadersChanged && !existingConfig.Disabled

	// TLSConfig: if omitted keep existing; if provided, restore raw CACertPEM when the
	// incoming value is the redacted placeholder returned by the API.
	tlsConfig := existingConfig.TLSConfig
	if req.TLSConfig != nil {
		tlsCopy := *req.TLSConfig
		if tlsCopy.CACertPEM != nil && existingConfig.TLSConfig != nil && existingConfig.TLSConfig.CACertPEM != nil {
			redactedExisting := h.store.RedactMCPClientConfig(existingConfig)
			if redactedExisting.TLSConfig != nil && redactedExisting.TLSConfig.CACertPEM != nil &&
				tlsCopy.CACertPEM.IsRedacted() && tlsCopy.CACertPEM.Equals(redactedExisting.TLSConfig.CACertPEM) {
				tlsCopy.CACertPEM = existingConfig.TLSConfig.CACertPEM
			}
		}
		tlsConfig = &tlsCopy
	}
	resolvedMaxProtocolVersion := existingConfig.MaxProtocolVersion
	if req.MaxProtocolVersion != nil {
		if err := schemas.ValidateMCPMaxProtocolVersion("max_protocol_version", *req.MaxProtocolVersion); err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, err.Error())
			return
		}
		resolvedMaxProtocolVersion = *req.MaxProtocolVersion
	}
	// ToolSyncInterval: keep the existing duration when not provided, otherwise
	// take the request value. Both the DB column and the rdb load path use
	// seconds, so we convert at the DB-write boundary below; the in-memory
	// duration is the source of truth here. 0 means no per-client override,
	// which the runtime resolves against the live global setting, so a later
	// change to the global reaches this client too.
	resolvedToolSyncInterval, err := req.ToolSyncInterval.Resolve(existingConfig.ToolSyncInterval)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	resolvedToolExecutionTimeout := existingConfig.ToolExecutionTimeout
	if req.ToolExecutionTimeout != nil {
		if *req.ToolExecutionTimeout < 0 {
			SendError(ctx, fasthttp.StatusBadRequest, "tool_execution_timeout must be >= 0")
			return
		}
		resolvedToolExecutionTimeout = time.Duration(*req.ToolExecutionTimeout) * time.Second
	}
	resolvedMaxInstructionsLength := existingConfig.MaxInstructionsLength
	if req.MaxInstructionsLength != nil {
		if *req.MaxInstructionsLength < 0 {
			SendError(ctx, fasthttp.StatusBadRequest, "max_instructions_length must not be negative")
			return
		}
		resolvedMaxInstructionsLength = *req.MaxInstructionsLength
	}

	// Resolve tools_to_execute and tools_to_auto_execute.
	resolvedToolsToExecute := existingConfig.ToolsToExecute
	if req.ToolsToExecute != nil {
		resolvedToolsToExecute = *req.ToolsToExecute
	}
	resolvedToolsToAutoExecute := existingConfig.ToolsToAutoExecute
	if resolvedToolsToExecute.IsEmpty() {
		resolvedToolsToAutoExecute = schemas.WhiteList{}
	} else if req.ToolsToAutoExecute != nil {
		resolvedToolsToAutoExecute = *req.ToolsToAutoExecute
	}

	// Validate
	if err := validateToolsToExecute(resolvedToolsToExecute); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid tools_to_execute: %v", err))
		return
	}
	if err := validateToolsToAutoExecute(resolvedToolsToAutoExecute, resolvedToolsToExecute); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid tools_to_auto_execute: %v", err))
		return
	}
	resolvedPromptsToExpose, resolvedResourcesToExpose := existingConfig.PromptsToExpose, existingConfig.ResourcesToExpose
	if req.PromptsToExpose != nil {
		resolvedPromptsToExpose = *req.PromptsToExpose
	}
	if req.ResourcesToExpose != nil {
		resolvedResourcesToExpose = *req.ResourcesToExpose
	}
	if err := validateExposeLists(resolvedPromptsToExpose, resolvedResourcesToExpose); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
		return
	}
	if err := mcp.ValidateMCPClientName(name); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid client name: %v", err))
		return
	}
	// openapi: a replaced spec/credential block is merged PATCH-style onto the
	// stored one, re-synthesized, and the (possibly unchanged) tool allow-lists
	// are checked against the resulting tool names. nil leaves the stored block
	// alone (core keeps it too); the resolved block is what gets persisted and
	// what core rebuilds the server from.
	//
	// Validation runs on a copy: prepareOpenAPIConfig writes into the block it
	// is given, and the live one must stay as it is if the request fails. A
	// tool-only edit of a client with no stored document (a config.json
	// spec_url client, fetched at connect) validates against a fresh fetch, so
	// rebuildOpenAPIServer asks the runtime to rebuild from that same source
	// once the update is saved, rather than inlining the document into the row.
	var resolvedOpenAPIConfig *schemas.MCPOpenAPIConfig
	rebuildOpenAPIServer := false
	// Tool names are shortened to fit beside the client name, so a rename is
	// validated too: it can change the names the tool lists must match.
	renamed := req.Name != nil && *req.Name != existingConfig.Name
	// A per_user_headers client's stored credentials are checked against its
	// per-user header keys, so a key edit is validated too when it stores any.
	perUserKeysEdited := req.PerUserHeaderKeys != nil && existingConfig.AuthType == schemas.MCPAuthTypePerUserHeaders &&
		existingConfig.OpenAPIConfig != nil && len(existingConfig.OpenAPIConfig.SecurityCredentials) > 0
	if existingConfig.ConnectionType == schemas.MCPConnectionTypeOpenAPI && (req.OpenAPIConfig != nil || req.ToolsToExecute != nil || req.ToolsToAutoExecute != nil || renamed || perUserKeysEdited) {
		if msg, refused := requestSpecFileRefusal(req.OpenAPIConfig); refused {
			SendError(ctx, fasthttp.StatusBadRequest, msg)
			return
		}
		candidate := mergeOpenAPIConfigUpdate(existingConfig.OpenAPIConfig, req.OpenAPIConfig, openAPIOptionsSent(ctx.PostBody()))
		if authBypassed, _ := ctx.UserValue(schemas.BifrostContextKeyAuthBypassed).(bool); authBypassed && req.OpenAPIConfig != nil &&
			openAPITargetChanged(existingConfig.OpenAPIConfig, candidate) && openAPIRetargetCarriesStoredSecrets(existingConfig, candidate, headers) {
			SendError(ctx, fasthttp.StatusForbidden, "Changing an OpenAPI client's spec, spec_url or base_url while it keeps stored credentials or headers requires an authenticated admin session; dashboard auth is currently disabled or unconfigured. Send replacement security_credentials and headers (or {} to clear them), or enable dashboard authentication.")
			return
		}
		// A client being disabled has no live server to rebuild; enabling it
		// later connects from the document fresh.
		rebuildOpenAPIServer = !disabled && req.OpenAPIConfig == nil && existingConfig.OpenAPIConfig != nil &&
			strings.TrimSpace(existingConfig.OpenAPIConfig.Spec) == "" && existingConfig.OpenAPIConfig.SpecURL != nil
		prepared, status, msg := prepareOpenAPIConfig(ctx, name, candidate)
		if status != 0 {
			SendError(ctx, status, msg)
			return
		}
		if unknown := unknownOpenAPITools(resolvedToolsToExecute, prepared.toolNames); len(unknown) > 0 {
			SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid tools_to_execute: the spec defines no tool named %s", strings.Join(unknown, ", ")))
			return
		}
		if unknown := unknownOpenAPITools(resolvedToolsToAutoExecute, prepared.toolNames); len(unknown) > 0 {
			SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid tools_to_auto_execute: the spec defines no tool named %s", strings.Join(unknown, ", ")))
			return
		}
		if existingConfig.AuthType == schemas.MCPAuthTypePerUserHeaders {
			perUserKeys := existingConfig.PerUserHeaderKeys
			if req.PerUserHeaderKeys != nil {
				perUserKeys = *req.PerUserHeaderKeys
			}
			if msg, refused := perUserHeaderCredentialConflict(prepared.schemes, prepared.config.SecurityCredentials, perUserKeys); refused {
				SendError(ctx, fasthttp.StatusBadRequest, msg)
				return
			}
		}
		if req.OpenAPIConfig != nil {
			resolvedOpenAPIConfig = prepared.config
		}
	}
	// An unauthenticated caller who changes where an OpenAPI client connects
	// gets the same restriction an unauthenticated create does: every dial must
	// reach a public address, so a redirect or a DNS change cannot reach a
	// private one after the one-time target check above. It is never cleared.
	requirePublicTarget := existingConfig.RequirePublicTarget
	if authBypassed, _ := ctx.UserValue(schemas.BifrostContextKeyAuthBypassed).(bool); authBypassed && resolvedOpenAPIConfig != nil {
		requirePublicTarget = true
	}
	if err := validateAllowedExtraHeaders(allowedExtraHeaders); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid allowed_extra_headers: %v", err))
		return
	}
	// Validate per_user_header_keys only when the request explicitly provides
	// the field — otherwise resolvePerUserHeaderKeys carries the existing list
	// forward unchanged (already validated at create time). Canonicalization
	// happens here AND inside resolvePerUserHeaderKeys; doing it twice is
	// cheap and keeps the validation error messages aligned with the canon
	// form that ultimately gets persisted (see invariant doc on
	// mcputils.CanonicalizeHeaderKey).
	if req.PerUserHeaderKeys != nil {
		// Reject an explicit empty list for per_user_headers clients.
		// AuthType is immutable on update (enforced at clientmanager.go:911),
		// so existingConfig.AuthType is the reliable gate — clients on other
		// auth types may legitimately carry no per_user_header_keys, but for
		// per_user_headers an empty schema means the auth mode has nothing
		// to collect or validate, which violates the feature contract.
		if existingConfig.AuthType == schemas.MCPAuthTypePerUserHeaders && len(*req.PerUserHeaderKeys) == 0 {
			SendError(ctx, fasthttp.StatusBadRequest, "per_user_header_keys must be a non-empty list for per_user_headers clients")
			return
		}
		canonHeaderKeys := mcputils.CanonicalizeHeaderKeys(*req.PerUserHeaderKeys)
		for i, key := range canonHeaderKeys {
			if key == "" {
				SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("per_user_header_keys[%d] is empty", i))
				return
			}
		}
		if lib.HasDuplicates(canonHeaderKeys) {
			SendError(ctx, fasthttp.StatusBadRequest, "per_user_header_keys contains duplicate entries")
			return
		}
	}

	// Validate token_exchange updates. AuthType is immutable on update, so
	// existingConfig.AuthType is the reliable gate here too; the block is
	// meaningless on any other auth type.
	if req.TokenExchange != nil {
		if existingConfig.AuthType != schemas.MCPAuthTypeTokenExchange {
			SendError(ctx, fasthttp.StatusBadRequest, "token_exchange can only be set for token_exchange clients")
			return
		}
		if strings.TrimSpace(req.TokenExchange.Audience) == "" {
			SendError(ctx, fasthttp.StatusBadRequest, "token_exchange.audience must be non-empty")
			return
		}
		if err := resourceCredentialResendRequired(existingConfig.TokenExchange, req.TokenExchange); err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, err.Error())
			return
		}
		// The GET response redacts the exchange credentials; a round-tripped
		// masked value means "keep the stored one", mirroring the header
		// and TLS redacted-merge behavior above. An env/vault reference is
		// never a placeholder, so switching a credential to one is written.
		if existingConfig.TokenExchange != nil {
			if req.TokenExchange.ClientID.IsMaskedPlaceholder() {
				req.TokenExchange.ClientID = existingConfig.TokenExchange.ClientID
			}
			if req.TokenExchange.ClientSecret.IsMaskedPlaceholder() {
				req.TokenExchange.ClientSecret = existingConfig.TokenExchange.ClientSecret
			}
			if req.TokenExchange.ResourceClientID.IsMaskedPlaceholder() {
				req.TokenExchange.ResourceClientID = existingConfig.TokenExchange.ResourceClientID
			}
			if req.TokenExchange.ResourceClientSecret.IsMaskedPlaceholder() {
				req.TokenExchange.ResourceClientSecret = existingConfig.TokenExchange.ResourceClientSecret
			}
			if req.TokenExchange.ResourcePrivateKey.IsMaskedPlaceholder() {
				req.TokenExchange.ResourcePrivateKey = existingConfig.TokenExchange.ResourcePrivateKey
			}
		}
		if !req.TokenExchange.ExchangesAsSSOApplication() && strings.TrimSpace(req.TokenExchange.ClientID.GetValue()) == "" && !req.TokenExchange.ClientID.IsFromSecret() {
			SendError(ctx, fasthttp.StatusBadRequest, "token_exchange.client_id must be non-empty unless use_idp_credentials is set")
			return
		}
		if err := req.TokenExchange.ValidateProfile(); err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, err.Error())
			return
		}
		// Same check as at create.
		if h.store.OAuthProvider != nil {
			if err := h.store.OAuthProvider.ValidateTokenExchangeConfig(ctx, req.TokenExchange); err != nil {
				SendError(ctx, fasthttp.StatusBadRequest, err.Error())
				return
			}
		}
	}
	// A real change to the token_exchange scoping block invalidates the
	// retained admin bootstrap credential the same way rotating oauth_config
	// invalidates per_user_oauth's (see shouldRotateOAuthConfig's comment
	// below for the parallel) — new client_id/client_secret/audience/scopes
	// mean the old admin credential was minted under permissions or an
	// identity-provider registration that no longer applies. Diffed against
	// the resolved (post-redaction-preserve) value above so a caller that
	// round-trips the block unchanged doesn't force a needless
	// "Re-verify as me". Unlike OAuth there's no per-user row to cascade —
	// end-user exchanged tokens are cache-only and simply re-exchange fresh
	// on the next call once evicted below.
	shouldMarkExchangeNeedsReauth := req.TokenExchange != nil &&
		existingConfig.AuthType == schemas.MCPAuthTypeTokenExchange &&
		existingConfig.TokenExchange.DiffersFrom(req.TokenExchange)

	// OAuth config rotation: update every oauth_configs field in place (no new
	// row, no re-discovery/re-registration triggered here) when ANY of them
	// differs from what's stored, then cascade every token bound to that
	// config to needs_reauth regardless of which auth_mode holds it. Every
	// field is treated uniformly — not just client_id/client_secret — since a
	// changed authorize_url/token_url can point at a different identity
	// provider, and changed scopes/resource mean already-issued tokens were
	// consented under permissions that no longer apply. The next reconnect
	// (shared clients) or next tool call (per-user, via the existing
	// needs_reauth->reauth-URL path) surfaces the requirement to
	// re-authenticate — no new plumbing needed.
	shouldRotateOAuthConfig := req.OauthConfig != nil &&
		(existingConfig.AuthType == schemas.MCPAuthTypeOauth || existingConfig.AuthType == schemas.MCPAuthTypePerUserOauth)
	if req.OauthConfig != nil && !shouldRotateOAuthConfig {
		SendError(ctx, fasthttp.StatusBadRequest, "oauth_config can only be updated for MCP clients using auth_type 'oauth' or 'per_user_oauth'")
		return
	}
	var existingOauthConfig *configstoreTables.TableOauthConfig
	var resolvedOauthFields configstore.MCPOAuthConfigFields
	if shouldRotateOAuthConfig {
		if existingConfig.OauthConfigID == nil || *existingConfig.OauthConfigID == "" {
			// Not a server fault: a legitimate pre-authorization state (the
			// client hasn't completed its one-time OAuth verification yet,
			// so there's no oauth_configs row to rotate against).
			SendError(ctx, fasthttp.StatusConflict, "oauth_config cannot be updated before this MCP client's OAuth verification is completed")
			return
		}
		var err error
		existingOauthConfig, err = h.store.ConfigStore.GetOauthConfigByID(ctx, *existingConfig.OauthConfigID)
		if err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to get existing OAuth config: %v", err))
			return
		}
		if existingOauthConfig == nil {
			// Unlike the missing-link case above, this IS a server-side data
			// integrity fault: the client has a link, but the row it points
			// at is gone.
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("oauth config %s referenced by this MCP client no longer exists", *existingConfig.OauthConfigID))
			return
		}
		// Resolve what every field would become: preserve the stored value
		// for any field the caller left unset. client_id/client_secret use
		// SecretVar's own masked-placeholder convention; the plain-string/
		// slice fields use the same "empty means not provided" convention
		// the config.json path already uses for this block.
		resolvedOauthFields = configstore.MCPOAuthConfigFields{
			ClientID:        existingOauthConfig.ClientID,
			ClientSecret:    existingOauthConfig.ClientSecret,
			AuthorizeURL:    existingOauthConfig.AuthorizeURL,
			TokenURL:        existingOauthConfig.TokenURL,
			RegistrationURL: "",
			Resource:        existingOauthConfig.Resource,
			Scopes:          nil,
		}
		if existingOauthConfig.RegistrationURL != nil {
			resolvedOauthFields.RegistrationURL = *existingOauthConfig.RegistrationURL
		}
		if existingOauthConfig.Scopes != "" {
			_ = json.Unmarshal([]byte(existingOauthConfig.Scopes), &resolvedOauthFields.Scopes)
		}
		if !req.OauthConfig.ClientID.ShouldPreserveStored() {
			resolvedOauthFields.ClientID = req.OauthConfig.ClientID
		}
		if !req.OauthConfig.ClientSecret.ShouldPreserveStored() {
			resolvedOauthFields.ClientSecret = req.OauthConfig.ClientSecret
		}
		if trimmed := strings.TrimSpace(req.OauthConfig.AuthorizeURL); trimmed != "" {
			resolvedOauthFields.AuthorizeURL = trimmed
		}
		if trimmed := strings.TrimSpace(req.OauthConfig.TokenURL); trimmed != "" {
			resolvedOauthFields.TokenURL = trimmed
		}
		if trimmed := strings.TrimSpace(req.OauthConfig.RegistrationURL); trimmed != "" {
			resolvedOauthFields.RegistrationURL = trimmed
		}
		if trimmed := strings.TrimSpace(req.OauthConfig.Resource); trimmed != "" {
			resolvedOauthFields.Resource = trimmed
		}
		// nil vs non-nil-empty distinguishes omitted (leave stored scopes
		// alone) from an explicit empty list (clear them) — encoding/json
		// leaves the field nil for an omitted "scopes" key and sets a
		// non-nil empty slice for an explicit "scopes": []. A length check
		// alone couldn't tell these apart and always preserved the stored
		// value, so a request to clear scopes silently no-op'd.
		if req.OauthConfig.Scopes != nil {
			resolvedOauthFields.Scopes = req.OauthConfig.Scopes
		}
		// Changing where credentials get sent (token_url/authorize_url) while
		// silently carrying over the stored client_secret would POST that
		// secret to a caller-supplied endpoint the admin never explicitly
		// paired it with — require the secret to be resent explicitly
		// whenever either endpoint changes, rather than trusting the old
		// pairing across an endpoint change. A public client stores no secret,
		// so it has nothing to resend.
		endpointChanged := resolvedOauthFields.AuthorizeURL != existingOauthConfig.AuthorizeURL ||
			resolvedOauthFields.TokenURL != existingOauthConfig.TokenURL
		if endpointChanged && existingOauthConfig.ClientSecret.IsSet() && req.OauthConfig.ClientSecret.ShouldPreserveStored() {
			SendError(ctx, fasthttp.StatusBadRequest, "client_secret must be resent explicitly when authorize_url or token_url changes")
			return
		}
		resolvedOauthFields.KeepBinding(existingOauthConfig)
		if !resolvedOauthFields.DiffersFrom(existingOauthConfig) {
			// Every field resolved to what's already stored — either the
			// caller left everything unset, or resent the same real values
			// verbatim. Nothing actually changed, so skip the write and the
			// reauth cascade entirely: a fetch-modify-put caller that
			// round-trips oauth_config unchanged, or a request that pairs an
			// unchanged oauth_config with a disable, must not invalidate
			// every existing holder's token for no reason.
			shouldRotateOAuthConfig = false
		}
	}
	if shouldRotateOAuthConfig && disabled {
		SendError(ctx, fasthttp.StatusBadRequest, "oauth credentials cannot be rotated while disabling a client; send these as two separate requests")
		return
	}
	// Pre-flight the new static headers against the upstream before anything
	// is written. VerifyHeadersConnection dials its own ephemeral transport
	// and closes it, touching neither the stored row nor the live connection,
	// so a rejected credential leaves the client exactly as it was — still
	// serving on the headers that work. Committing first and reconnecting
	// afterwards cannot offer that: the failed dial drops the live connection
	// (leaving the client unusable with "no active connection") while the bad
	// headers are already persisted, so the connection checker has nothing
	// good to retry with. Same verify-then-persist order the create path uses
	// for per-user headers.
	if staticHeadersChanged {
		verifyConfig := *existingConfig
		verifyConfig.Headers = headers
		verifyConfig.TLSConfig = tlsConfig
		verifyConfig.MaxProtocolVersion = resolvedMaxProtocolVersion
		// Only Authorization goes in this map. VerifyHeadersConnection layers
		// StaticConfigHeaders(verifyConfig) in itself, which already carries
		// every other header on the config; Authorization is the one entry
		// that set deliberately withholds (so plugins never observe the
		// bearer), and it gets applied after the plugin gate instead. Passing
		// exactly that one header here reproduces the header set a real
		// sticky dial builds, since sharedHeadersResolver.ConnectionHeaders
		// resolves only Authorization too — including its first-match-wins
		// behavior if the map somehow holds two casings of the name.
		verifyHeaders := make(map[string]string, 1)
		for key, value := range headers {
			if strings.EqualFold(key, "Authorization") {
				verifyHeaders["Authorization"] = value.GetValue()
				break
			}
		}
		bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.store)
		_, verifyErr := h.mcpManager.VerifyHeadersConnection(bifrostCtx, &verifyConfig, verifyHeaders)
		cancel()
		if verifyErr != nil {
			SendError(ctx, fasthttp.StatusUnprocessableEntity, fmt.Sprintf("The new headers were rejected by the MCP server, so nothing was changed: %v", verifyErr))
			return
		}
	}

	// Rotation is deferred until after the DB and in-memory client updates
	// below both succeed (see the call site further down): RotateMCPOAuthConfig
	// opens its own transaction and, once it commits, cascades every existing
	// token on this config to needs_reauth — an effect no later rollback in
	// this handler can undo. Rotating here, before UpdateMCPClientConfig/
	// UpdateMCPClient even run, would mean a later failure in either reports
	// this request as failed while every holder was already signed out.

	var oldDBConfig *configstoreTables.TableMCPClient
	if h.store.ConfigStore != nil {
		var err error
		oldDBConfig, err = h.store.ConfigStore.GetMCPClientByID(ctx, id)
		if err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to get existing mcp client config: %v", err))
			return
		}
	}

	perUserHeaderKeys := resolvePerUserHeaderKeys(existingConfig, req)

	// PATCH semantics for the token_exchange scoping block: omitted preserves
	// the stored block (validated above to only apply to token_exchange
	// clients).
	tokenExchange := existingConfig.TokenExchange
	if req.TokenExchange != nil {
		tokenExchange = req.TokenExchange
	}

	// Build the DB update record from all resolved values.
	dbUpdateRecord := configstoreTables.TableMCPClient{
		ClientID:               id,
		Name:                   name,
		IsCodeModeClient:       isCodeMode,
		ConnectionType:         string(existingConfig.ConnectionType),
		ConnectionString:       existingConfig.ConnectionString,
		RequirePublicTarget:    requirePublicTarget,
		StdioConfig:            existingConfig.StdioConfig,
		ToolsToExecute:         resolvedToolsToExecute,
		ToolsToAutoExecute:     resolvedToolsToAutoExecute,
		PromptsToExpose:        resolvedPromptsToExpose,
		ResourcesToExpose:      resolvedResourcesToExpose,
		Headers:                headers,
		AllowedExtraHeaders:    allowedExtraHeaders,
		IsPingAvailable:        isPingAvailable,
		NeedsSessionStickiness: needsSessionStickiness,
		ToolPricing:            toolPricing,
		ToolSyncInterval:       int(resolvedToolSyncInterval / time.Second),
		MaxProtocolVersion:     resolvedMaxProtocolVersion,
		ToolExecutionTimeout:   int(resolvedToolExecutionTimeout / time.Second),
		MaxInstructionsLength:  resolvedMaxInstructionsLength,
		AuthType:               string(existingConfig.AuthType),
		OauthConfigID:          existingConfig.OauthConfigID,
		AllowByDefault:         allowByDefault,
		Disabled:               disabled,
		PerUserHeaderKeys:      perUserHeaderKeys,
		TokenExchange:          tokenExchange,
		TLSConfig:              tlsConfig,
		OpenAPIConfig:          resolvedOpenAPIConfig,
	}
	// Rebind persisted discovered tool keys (and inner Function.Name) to the current
	// client name so a restart restores them under the right prefix.
	if oldDBConfig != nil && len(oldDBConfig.DiscoveredTools) > 0 {
		newPrefix := name + "-"
		migrated := make(map[string]schemas.ChatTool, len(oldDBConfig.DiscoveredTools))
		for oldKey, tool := range oldDBConfig.DiscoveredTools {
			newKey := oldKey
			if _, suffix, ok := strings.Cut(oldKey, "-"); ok {
				newKey = newPrefix + suffix
			}
			if tool.Function != nil {
				fn := *tool.Function
				fn.Name = newKey
				tool.Function = &fn
			}
			migrated[newKey] = tool
		}
		dbUpdateRecord.DiscoveredTools = migrated
		dbUpdateRecord.DiscoveredToolNameMapping = oldDBConfig.DiscoveredToolNameMapping
	}
	if h.store.ConfigStore != nil {
		if err := h.store.ConfigStore.UpdateMCPClientConfig(ctx, id, &dbUpdateRecord); err != nil {
			status := fasthttp.StatusInternalServerError
			if errors.Is(err, configstore.ErrNotFound) {
				status = fasthttp.StatusNotFound
			}
			SendError(ctx, status, fmt.Sprintf("failed to update mcp client config in store: %v", err))
			return
		}
	}

	// Build in-memory config from resolved values. A 0 interval means no
	// per-client override: the runtime resolves it against the live global
	// setting, matching the persisted row above, so a later change to the
	// global reaches this client too.
	schemasConfig := &schemas.MCPClientConfig{
		ID:                     id,
		Name:                   name,
		IsCodeModeClient:       isCodeMode,
		ConnectionType:         existingConfig.ConnectionType,
		ConnectionString:       existingConfig.ConnectionString,
		RequirePublicTarget:    requirePublicTarget,
		StdioConfig:            existingConfig.StdioConfig,
		TLSConfig:              tlsConfig,
		ToolsToExecute:         resolvedToolsToExecute,
		ToolsToAutoExecute:     resolvedToolsToAutoExecute,
		PromptsToExpose:        resolvedPromptsToExpose,
		ResourcesToExpose:      resolvedResourcesToExpose,
		Headers:                headers,
		AllowedExtraHeaders:    allowedExtraHeaders,
		AuthType:               existingConfig.AuthType,
		OauthConfigID:          existingConfig.OauthConfigID,
		IsPingAvailable:        isPingAvailable,
		NeedsSessionStickiness: needsSessionStickiness,
		ToolSyncInterval:       resolvedToolSyncInterval,
		MaxProtocolVersion:     resolvedMaxProtocolVersion,
		ToolExecutionTimeout:   resolvedToolExecutionTimeout,
		MaxInstructionsLength:  resolvedMaxInstructionsLength,
		ToolPricing:            toolPricing,
		AllowByDefault:         allowByDefault,
		Disabled:               disabled,
		PerUserHeaderKeys:      perUserHeaderKeys,
		TokenExchange:          tokenExchange,
		OpenAPIConfig:          resolvedOpenAPIConfig,
	}

	// Compare per-call-ness before/after so the response can tell the admin
	// whether this update actually changed how the client connects (only
	// needs_session_stickiness on a shared http client can do that — every
	// other combination of auth/connection type comes out the same on both
	// sides). UpdateMCPClient below applies the change live: it closes the
	// persistent connection if this just became per-call, or dials one if it
	// just became sticky.
	wasPerCallConnection := h.mcpManager.RequiresPerCallConnection(existingConfig)
	isPerCallConnection := h.mcpManager.RequiresPerCallConnection(schemasConfig)
	// Snapshotted now: existingConfig is the live entry the runtime update replaces.
	after := auditMCPClient(h.store, mcpClientAfterUpdate(existingConfig, schemasConfig))

	// Update MCP client config in memory (always — applies name/tools/header changes,
	// sharedConnectErr records a needs_session_stickiness=true dial failure without
	// aborting the request: the field update itself already committed in memory with
	// no rollback path (see the sentinel's own doc comment), so every block below
	// (OAuth rotation, admin exchange reauth marking, per-user-headers needs_update,
	// VK assignment changes, per-user credential reconciliation) is independent of
	// whether the dial succeeded and must still run. Returning early here used to
	// silently drop all of them for any request that combined
	// needs_session_stickiness with one of those other changes. The dial failure is
	// folded into the final response instead of an early one.
	var sharedConnectErr error
	if err := h.mcpManager.UpdateMCPClient(ctx, id, schemasConfig); err != nil {
		// ErrMCPEnableConnectFailed joins ErrMCPSharedConnectFailedAfterUpdate
		// here for the same reason: the runtime already committed the change
		// (the client is un-disabled, with a checker retrying — its state
		// parked back at Disabled, or left at needs_reauth for a dead OAuth
		// credential), so rolling the row back would leave the DB claiming
		// disabled while the runtime reconnects — and a restart would then
		// silently undo an enable the admin asked for.
		if errors.Is(err, mcp.ErrMCPSharedConnectFailedAfterUpdate) || errors.Is(err, mcp.ErrMCPEnableConnectFailed) {
			// Rolling the DB back to oldDBConfig here would diverge it from
			// the runtime, which already has the new config and a connection
			// checker retrying the dial. Keep the persisted row.
			logger.Error(fmt.Sprintf("MCP client %s updated, but its shared connection could not be established: %v", id, err))
			sharedConnectErr = err
		} else {
			// Rollback DB update to keep DB and memory in sync
			if h.store.ConfigStore != nil && oldDBConfig != nil {
				if rollbackErr := h.store.ConfigStore.UpdateMCPClientConfig(ctx, id, oldDBConfig); rollbackErr != nil {
					logger.Error(fmt.Sprintf("Failed to rollback MCP client DB update: %v. please restart bifrost to keep core and database in sync", rollbackErr))
				}
			}
			logger.Error(fmt.Sprintf("Failed to update MCP client: %v", err))
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to update mcp client: %v", err))
			return
		}
	}
	// The client update is committed from here on (nothing below rolls it back),
	// so record it before the OAuth, exchange and VK steps that can still fail.
	lib.RecordAuditChange(ctx, "mcp_client.updated", "mcp_client", id, name, before, after)

	// Apply the verified headers to the live client. A sticky client holds an
	// open transport with the old header baked in, and
	// UpdateMCPClientCredentials is the only thing that replaces it (redial +
	// tool rediscovery). A per-call client reads the current config on every
	// dial, so its credential is already live, but its tool list would sit
	// on the pre-rotation discovery until the periodic checker's next tick,
	// so UpdateMCPClientCredentials runs a synchronous discovery refresh for
	// it instead of a redial. The one combination skipped is a
	// per-call->sticky flip: UpdateMCPClient above already dialed the new
	// connection with these credentials and rediscovered tools, so running
	// this too would just be a second connect with the same result.
	//
	// The same credentials already completed a full connect during the
	// pre-flight above, so a failure here is a transient dial problem rather
	// than a bad credential — and UpdateClientCredentials restores the
	// previous ExecutionConfig on failure, leaving the connection checker to
	// retry. Reported as a partial success for that reason, not a hard
	// failure: every other effect of this request already committed.
	// Tracked separately from sharedConnectErr: a client that ends up
	// per-call has no connection to swap, so its failure here is the
	// synchronous tool refresh, retried by the periodic tool sync rather
	// than the connection checker's reconnect path. The response wording
	// below differs accordingly.
	var toolRefreshErr error
	if headersChanged && sharedConnectErr == nil && !(wasPerCallConnection && !isPerCallConnection) {
		if err := h.updateMCPClientCredentialsWithRetry(ctx, id, schemasConfig); err != nil &&
			!errors.Is(err, schemas.ErrMCPReconnectNotApplicable) {
			logger.Error(fmt.Sprintf("MCP client %s updated, but applying the new headers to the live client failed: %v", id, err))
			if isPerCallConnection {
				toolRefreshErr = err
			} else {
				sharedConnectErr = err
			}
		}
	}

	if rebuildOpenAPIServer && sharedConnectErr == nil {
		if _, err := h.mcpManager.RefreshMCPClientTools(ctx, id); err != nil {
			logger.Error(fmt.Sprintf("MCP client %s updated, but rebuilding its OpenAPI server from spec_url failed: %v", id, err))
			toolRefreshErr = err
		}
	}

	// Rotate OAuth credentials only now that both the DB row and the runtime
	// config update above have succeeded — see the comment where
	// shouldRotateOAuthConfig was computed for why this can't run earlier.
	if shouldRotateOAuthConfig {
		rotated, err := h.store.ConfigStore.RotateMCPOAuthConfig(ctx, existingOauthConfig, resolvedOauthFields)
		if err != nil {
			// The rest of the update already committed; only credential
			// rotation failed. Report it as a partial success rather than a
			// full failure so the caller doesn't retry the whole request
			// (which would redundantly repeat the client/DB update above).
			logger.Error(fmt.Sprintf("[PARTIAL SUCCESS] MCP client %s was updated but rotating its OAuth credentials failed: %v", id, err))
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("MCP client updated but rotating its OAuth credentials failed: %v", err))
			return
		}
		if rotated {
			h.mcpCredentialCacheManager.EvictOauthTokenCacheByMCPClient(ctx, id)
			// Rotation just cascaded every token bound to this oauth_config to
			// needs_reauth in the DB, but the in-memory client above only had
			// its ExecutionConfig replaced — its live connection, if any, is
			// still open on the now-invalidated Authorization header. Close
			// it and flip the in-memory state to match, rather than leaving
			// a stale connection serving calls until the health monitor
			// eventually notices. Not a hard failure: the DB rotation is
			// what actually matters for correctness, and the health
			// monitor's next cycle would eventually catch a connection this
			// call failed to close.
			if err := h.mcpManager.CloseAndMarkNeedsReauth(ctx, id); err != nil && !errors.Is(err, schemas.ErrMCPReconnectNotApplicable) {
				logger.Error(fmt.Sprintf("Failed to close MCP client %s's connection after OAuth credential rotation: %v", id, err))
			}
		}
	}

	// Mark the retained admin exchange credential needs_reauth only now that
	// the DB row and the in-memory client above have both succeeded — same
	// ordering rationale as shouldRotateOAuthConfig's cascade above: a
	// failure between marking and now would lock the admin out over a
	// client update that itself never went through. Unlike OAuth rotation,
	// there's no per-user row to cascade (see shouldMarkExchangeNeedsReauth's
	// comment) and no separate config table to update in the same
	// transaction — the client row was already persisted above.
	if shouldMarkExchangeNeedsReauth {
		if err := h.store.ConfigStore.MarkAdminExchangeTokenNeedsReauthByMCPClientID(ctx, id, "token exchange settings were updated; the admin credential must be re-verified"); err != nil {
			// Mirrors shouldRotateOAuthConfig's [PARTIAL SUCCESS] handling
			// above: the rest of the update already committed, only the
			// reauth marking failed.
			logger.Error(fmt.Sprintf("[PARTIAL SUCCESS] MCP client %s was updated but marking its admin exchange credential needs_reauth failed: %v", id, err))
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("MCP client updated but marking its admin exchange credential needs_reauth failed: %v", err))
			return
		}
		// No CloseAndMarkNeedsReauth call here (unlike the OAuth rotation
		// block above): token_exchange always requires a per-call connection
		// (RequiresPerCallConnection), so the call would unconditionally hit
		// its early-return no-op — there's no shared connection to close and
		// no clientState.State that anything reads for this auth type.
		h.mcpCredentialCacheManager.EvictOauthTokenCacheByMCPClient(ctx, existingConfig.ID)
	}

	// If the per-user-headers schema now requires additional keys, flip every
	// existing active row to 'needs_update' so callers are forced to submit the
	// new values on next tool use. Removed-only schema changes do not need a
	// resubmission: runtime resolution and flow-submit both filter stored
	// credentials to the current schema before using/persisting them.
	//
	// Runs AFTER the in-memory UpdateMCPClient succeeds — if we flipped
	// credentials first and the runtime update then failed, the rollback
	// above would revert the DB row but leave every credential stuck in
	// needs_update, even though the old schema is still the active one.
	// Users would see a spurious "resubmit" prompt with no actual schema
	// change to reconcile.
	if existingConfig.AuthType == schemas.MCPAuthTypePerUserHeaders &&
		perUserHeaderKeysAdded(existingPerUserHeaderKeys, schemasConfig.PerUserHeaderKeys) &&
		h.store.ConfigStore != nil {
		if err := h.store.ConfigStore.MarkMCPPerUserHeaderCredentialsNeedsUpdate(ctx, existingConfig.ID); err != nil {
			logger.Error(fmt.Sprintf("failed to flip per-user header credentials to needs_update for client %s: %v", existingConfig.ID, err))
		} else {
			// Cached copies still carry the pre-flip status and values; drop
			// them so the next lookup reads the needs_update rows.
			h.mcpCredentialCacheManager.EvictMCPHeaderCredentialCacheByMCPClient(ctx, existingConfig.ID)
		}
	}

	// Reload every VK currently referencing this MCP client so the governance
	// cache's preloaded MCPClient relation picks up the rename / tool / header
	// changes. The VK-assignment-change block below does its own targeted
	// reload, but only fires when req.VKConfigs != nil — a name-only update
	// otherwise leaves every cached VK pointing at the old MCPClient.Name and
	// the per-VK allowlist check rejects tool calls under the new prefix.
	h.refreshMCPClientOnAssignedVirtualKeys(ctx, id, oldDBConfig.ID)

	// Manage VK assignments if vk_configs was provided
	if req.VKConfigs != nil && h.store.ConfigStore != nil {
		current, err := h.store.ConfigStore.GetVirtualKeyMCPConfigsByMCPClientID(ctx, oldDBConfig.ID)
		if err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to get current VK MCP configs: %v", err))
			return
		}
		// Index current assignments by VK ID for diffing
		currentByVKID := make(map[string]*configstoreTables.TableVirtualKeyMCPConfig, len(current))
		for i := range current {
			currentByVKID[current[i].VirtualKeyID] = &current[i]
		}
		// Validate and reject empty/duplicate virtual_key_id entries
		seen := make(map[string]struct{}, len(*req.VKConfigs))
		for _, vc := range *req.VKConfigs {
			if vc.VirtualKeyID == "" {
				SendError(ctx, fasthttp.StatusBadRequest, "virtual_key_id must not be empty")
				return
			}
			if _, exists := seen[vc.VirtualKeyID]; exists {
				SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("duplicate virtual_key_id in vk_configs: %s", vc.VirtualKeyID))
				return
			}
			seen[vc.VirtualKeyID] = struct{}{}
		}
		// Validate tools_to_execute before entering the transaction so failures return 400
		for _, vc := range *req.VKConfigs {
			if err := vc.ToolsToExecute.Validate(); err != nil {
				SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("invalid tools_to_execute for virtual key %s: %v", vc.VirtualKeyID, err))
				return
			}
		}
		// Index requested assignments by VK ID
		requestedByVKID := make(map[string]MCPVKConfigRequest, len(*req.VKConfigs))
		for _, vc := range *req.VKConfigs {
			requestedByVKID[vc.VirtualKeyID] = vc
		}
		if err := h.store.ConfigStore.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
			// Create or update
			for _, vc := range *req.VKConfigs {
				if existing, ok := currentByVKID[vc.VirtualKeyID]; ok {
					existing.ToolsToExecute = vc.ToolsToExecute
					if err := h.store.ConfigStore.UpdateVirtualKeyMCPConfig(ctx, existing, tx); err != nil {
						return fmt.Errorf("failed to update VK MCP config for %s: %w", vc.VirtualKeyID, err)
					}
				} else {
					if err := h.store.ConfigStore.CreateVirtualKeyMCPConfig(ctx, &configstoreTables.TableVirtualKeyMCPConfig{
						VirtualKeyID:   vc.VirtualKeyID,
						MCPClientID:    oldDBConfig.ID,
						ToolsToExecute: vc.ToolsToExecute,
					}, tx); err != nil {
						return fmt.Errorf("failed to create VK MCP config for %s: %w", vc.VirtualKeyID, err)
					}
				}
			}
			// Delete removed assignments
			for vkID, existing := range currentByVKID {
				if _, ok := requestedByVKID[vkID]; !ok {
					if err := h.store.ConfigStore.DeleteVirtualKeyMCPConfig(ctx, existing.ID, tx); err != nil {
						return fmt.Errorf("failed to remove VK MCP config for %s: %w", vkID, err)
					}
				}
			}
			return nil
		}); err != nil {
			// NOTE: Partial success — the MCP client config was already updated in DB and memory above.
			// Only the VK assignment changes failed. The VK assignments remain unchanged in DB.
			// The MCP client update is idempotent, so retrying the full request is safe.
			logger.Error(fmt.Sprintf(
				"[PARTIAL SUCCESS] MCP client %s was updated successfully but VK assignment update failed: %v. "+
					"VK assignments remain unchanged. Retry the request to apply VK changes.",
				id, err,
			))
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("MCP client was updated but VK assignment update failed: %v", err))
			return
		}
		// Reload all affected VKs in memory so governance enforcement reflects the new MCP assignments.
		// requestedByVKID and currentByVKID together cover the full affected set (no duplicates since both are maps).
		if h.governanceManager != nil {
			for vkID := range requestedByVKID {
				if _, err := h.governanceManager.ReloadVirtualKey(ctx, vkID); err != nil {
					logger.Error(fmt.Sprintf("failed to reload virtual key %s in memory after MCP VK assignment update: %v", vkID, err))
				}
			}
			for vkID := range currentByVKID {
				if _, alreadyReloaded := requestedByVKID[vkID]; !alreadyReloaded {
					if _, err := h.governanceManager.ReloadVirtualKey(ctx, vkID); err != nil {
						logger.Error(fmt.Sprintf("failed to reload virtual key %s in memory after MCP VK assignment update: %v", vkID, err))
					}
				}
			}
		}
	}

	// Per-user credential reconciliation for changes that mutate who can
	// access this MCP. Two trigger conditions:
	//   1. vk_configs explicitly diffed (rows added/removed/updated).
	//   2. allow_by_default flipped: the default grant toggled, so every
	//      credential held for this MCP needs re-evaluation.
	//
	// Reconcile is enterprise-only behavior (no-op in OSS). It orphans
	// credentials whose MCP just lost the grant and reactivates orphaned
	// ones whose MCP regained the grant. Both surfaces (OAuth + headers)
	// are reconciled — they share the same VK→MCP allowlist model.
	if h.store.ConfigStore != nil {
		shouldReconcile := req.VKConfigs != nil || allowByDefault != existingAllowByDefault
		if shouldReconcile {
			if err := h.store.ConfigStore.ReconcileOauthAfterMCPChange(ctx, id); err != nil {
				logger.Error(fmt.Sprintf("reconcile OAuth credentials after MCP %s update failed: %v", id, err))
			}
			if err := h.store.ConfigStore.ReconcileMCPHeadersAfterMCPChange(ctx, id); err != nil {
				logger.Error(fmt.Sprintf("reconcile per-user-headers credentials after MCP %s update failed: %v", id, err))
			}
			// Reconciliation may have orphaned or reactivated token and
			// credential rows; cached copies no longer reflect the database.
			h.mcpCredentialCacheManager.EvictOauthTokenCacheByMCPClient(ctx, id)
			h.mcpCredentialCacheManager.EvictMCPHeaderCredentialCacheByMCPClient(ctx, id)
		}
	}

	message := "MCP client edited successfully"
	if sharedConnectErr == nil {
		// Only claimed when the dial actually succeeded — see sharedConnectErr
		// handling below for the case where it didn't.
		switch {
		case wasPerCallConnection && !isPerCallConnection:
			message += ". This client now maintains a persistent shared connection: it has been (re)connected."
		case !wasPerCallConnection && isPerCallConnection:
			message += ". This client now connects per call instead of maintaining a persistent connection: the existing shared connection was closed."
		}
	}
	if sharedConnectErr != nil {
		SendJSON(ctx, map[string]any{
			"status":  "partial_success",
			"message": fmt.Sprintf("%s However, establishing the shared connection failed: %v. The connection checker will keep retrying automatically.", message, sharedConnectErr),
		})
		return
	}
	if toolRefreshErr != nil {
		SendJSON(ctx, map[string]any{
			"status":  "partial_success",
			"message": fmt.Sprintf("%s However, refreshing the client's tools with the new headers failed: %v. The periodic tool sync will retry automatically.", message, toolRefreshErr),
		})
		return
	}
	SendJSON(ctx, map[string]any{
		"status":  "success",
		"message": message,
	})
}

// deleteMCPClient handles DELETE /api/mcp/client/{id} - Remove an MCP client
func (h *MCPHandler) deleteMCPClient(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "MCP operations unavailable: config store is disabled")
		return
	}
	id, err := getIDFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("invalid id: %v", err))
		return
	}
	targetName := id
	existing := findMCPClientConfig(h.store, id)
	if existing != nil {
		targetName = existing.Name
	}
	before := auditMCPClient(h.store, existing)
	// Delete from DB first to avoid memory/DB inconsistency if DB delete fails
	if h.store.ConfigStore != nil {
		if err := h.store.ConfigStore.DeleteMCPClientConfig(ctx, id); err != nil {
			// An unknown id leaves nothing to remove: the runtime manager is not reached.
			if errors.Is(err, configstore.ErrNotFound) {
				SendError(ctx, fasthttp.StatusNotFound, fmt.Sprintf("failed to delete MCP config: %v", err))
				return
			}
			logger.Error(fmt.Sprintf("Failed to delete MCP client config from database: %v", err))
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to delete MCP config: %v", err))
			return
		}
	}
	// The row is gone even if the runtime removal below fails.
	lib.RecordAuditChange(ctx, "mcp_client.deleted", "mcp_client", id, targetName, before, nil)
	// RemoveMCPClient also evicts the client's cached OAuth access tokens
	// and header credentials internally, covering the rows the database
	// delete above cascaded over.
	if err := h.mcpManager.RemoveMCPClient(ctx, id); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to remove MCP client: %v", err))
		return
	}
	SendJSON(ctx, map[string]any{
		"status":  "success",
		"message": "MCP client removed successfully",
	})
}

func getIDFromCtx(ctx *fasthttp.RequestCtx) (string, error) {
	idValue := ctx.UserValue("id")
	if idValue == nil {
		return "", fmt.Errorf("missing id parameter")
	}
	idStr, ok := idValue.(string)
	if !ok {
		return "", fmt.Errorf("invalid id parameter type")
	}

	return idStr, nil
}

// validateExposeLists checks the prompts and resources allow-lists; nil (expose all) is valid.
func validateExposeLists(prompts, resources schemas.WhiteList) error {
	if err := prompts.Validate(); err != nil {
		return fmt.Errorf("invalid prompts_to_expose: %v", err)
	}
	if err := resources.Validate(); err != nil {
		return fmt.Errorf("invalid resources_to_expose: %v", err)
	}
	return nil
}

func validateToolsToExecute(toolsToExecute schemas.WhiteList) error {
	if err := toolsToExecute.Validate(); err != nil {
		return fmt.Errorf("invalid tools_to_execute: %w", err)
	}
	return nil
}

func validateAllowedExtraHeaders(allowedExtraHeaders schemas.WhiteList) error {
	if err := allowedExtraHeaders.Validate(); err != nil {
		return fmt.Errorf("invalid allowed_extra_headers: %w", err)
	}
	return nil
}

// validateNeedsSessionStickiness rejects an explicit false for connection
// types that can't run per-call: SSE has no stateless mode (its session is
// bound to the open stream) and STDIO needs a persistent subprocess. nil and
// true are always fine regardless of connection type.
func validateNeedsSessionStickiness(needsSessionStickiness *bool, connectionType schemas.MCPConnectionType) error {
	if needsSessionStickiness == nil || *needsSessionStickiness {
		return nil
	}
	if connectionType != schemas.MCPConnectionTypeHTTP {
		return fmt.Errorf("needs_session_stickiness cannot be false for connection_type %q: only 'http' supports a per-call connection", connectionType)
	}
	return nil
}

func validateToolsToAutoExecute(toolsToAutoExecute schemas.WhiteList, toolsToExecute schemas.WhiteList) error {
	if err := toolsToAutoExecute.Validate(); err != nil {
		return fmt.Errorf("invalid tools_to_auto_execute: %w", err)
	}

	if !toolsToAutoExecute.IsEmpty() {
		// If ToolsToExecute allows all, no further cross-validation needed
		if toolsToExecute.IsUnrestricted() {
			return nil
		}

		// Check that all tools in ToolsToAutoExecute are also in ToolsToExecute
		for _, tool := range toolsToAutoExecute {
			if tool == "*" {
				return fmt.Errorf("tool '*' in tools_to_auto_execute requires '*' in tools_to_execute")
			}
			if !toolsToExecute.Contains(tool) {
				return fmt.Errorf("tool '%s' in tools_to_auto_execute is not in tools_to_execute", tool)
			}
		}
	}

	return nil
}

// mergeMCPHeaders merges incoming request headers with the existing raw headers,
// preserving stored raw values when an incoming header value is redacted and unchanged.
// Only called when the caller explicitly provided a headers map (req.Headers != nil);
// when headers are omitted entirely the caller retains the existing value directly.
func mergeMCPHeaders(incoming, rawExisting, redactedExisting map[string]schemas.SecretVar) map[string]schemas.SecretVar {
	merged := make(map[string]schemas.SecretVar, len(incoming))
	for key, incomingValue := range incoming {
		if redactedExisting != nil && rawExisting != nil {
			if redactedValue, ok := redactedExisting[key]; ok {
				if rawValue, ok := rawExisting[key]; ok {
					if incomingValue.IsRedacted() && incomingValue.Equals(&redactedValue) {
						merged[key] = rawValue
						continue
					}
				}
			}
		}
		merged[key] = incomingValue
	}
	return merged
}

// persistMCPDiscovery stores a verified discovery's resources, prompts and instructions; the full
// config update leaves those columns alone.
func (h *MCPHandler) persistMCPDiscovery(ctx context.Context, clientID string, discovery *schemas.MCPDiscovery) {
	if h.store.ConfigStore == nil || discovery == nil {
		return
	}
	if err := h.store.ConfigStore.UpdateMCPClientDiscovery(ctx, clientID, discovery); err != nil {
		logger.Warn(fmt.Sprintf("failed to persist discovered resources and prompts for MCP client %s: %v", clientID, err))
	}
}

// updateMCPClientWithRetry calls mcpManager.UpdateMCPClient with a short retry loop
func (h *MCPHandler) updateMCPClientWithRetry(ctx context.Context, id string, config *schemas.MCPClientConfig) error {
	const maxAttempts = 3
	const retryDelay = 500 * time.Millisecond

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		lastErr = h.mcpManager.UpdateMCPClient(ctx, id, config)
		if lastErr == nil {
			return nil
		}
		if !strings.Contains(lastErr.Error(), "reconnect") || attempt == maxAttempts {
			return lastErr
		}
		logger.Warn(fmt.Sprintf("[OAuth Complete] UpdateMCPClient attempt %d/%d for client %s blocked by in-flight reconnect; retrying in %s: %v",
			attempt, maxAttempts, id, retryDelay, lastErr))
		time.Sleep(retryDelay)
	}
	return lastErr
}

// mcpHeadersEqual reports whether two resolved static header maps carry the
// same credentials. Compared on the post-merge values (mergeMCPHeaders has
// already restored any unchanged redacted entry to its stored raw value), so
// a caller round-tripping the redacted map it was served reads as unchanged.
func mcpHeadersEqual(a, b map[string]schemas.SecretVar) bool {
	if len(a) != len(b) {
		return false
	}
	for key, aVal := range a {
		bVal, ok := b[key]
		if !ok || !aVal.Equals(&bVal) {
			return false
		}
	}
	return true
}

// updateMCPClientCredentialsWithRetry calls mcpManager.UpdateMCPClientCredentials with a short retry loop.
func (h *MCPHandler) updateMCPClientCredentialsWithRetry(ctx context.Context, id string, config *schemas.MCPClientConfig) error {
	const maxAttempts = 3
	const retryDelay = 500 * time.Millisecond

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		lastErr = h.mcpManager.UpdateMCPClientCredentials(ctx, id, config)
		if lastErr == nil {
			return nil
		}
		// Not applicable (a per-call client — no persistent connection to
		// update) is never transient: retrying it wouldn't change the
		// client's connection mode, so return immediately rather than
		// wasting the retry budget on a client type this can never apply to.
		if errors.Is(lastErr, schemas.ErrMCPReconnectNotApplicable) {
			return lastErr
		}
		if !strings.Contains(lastErr.Error(), "reconnect") || attempt == maxAttempts {
			return lastErr
		}
		logger.Warn(fmt.Sprintf("[OAuth Complete] UpdateMCPClientCredentials attempt %d/%d for client %s blocked by in-flight reconnect; retrying in %s: %v",
			attempt, maxAttempts, id, retryDelay, lastErr))
		time.Sleep(retryDelay)
	}
	return lastErr
}

// completePerUserOAuthAdminRepair finishes a per_user_oauth admin repair
// flow: the admin redid consent via POST /reauthorize because the retained
// admin tool-discovery credential died, and CompleteOAuthFlow just wrote the
// fresh token as an auth_mode='shared' row. Verify the fresh credential
// against the upstream server (which also re-discovers tools), then promote
// it to the admin row and persist the refreshed tool set. On verification
// failure the fresh token is revoked and the old admin row and tool set are
// left untouched, so the repair can simply be retried.
func (h *MCPHandler) completePerUserOAuthAdminRepair(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, oauthConfigID, clientID string) {
	clientConfig, err := h.store.ConfigStore.GetMCPClientConfigByID(ctx, clientID)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to load MCP client: %v", err))
		return
	}
	if clientConfig == nil {
		SendError(ctx, fasthttp.StatusNotFound, "MCP client no longer exists")
		return
	}

	accessToken, err := h.oauthHandler.GetAccessToken(ctx, oauthConfigID)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get admin access token for verification: %v", err))
		return
	}

	discovery, err := h.mcpManager.VerifyPerUserOAuthConnection(bifrostCtx, clientConfig, accessToken)
	if err != nil {
		// The fresh credential is unusable; revoke it so it isn't left
		// behind as a dangling shared row. The existing admin row and the
		// current tool set stay untouched, so the repair can be retried.
		if revokeErr := h.oauthHandler.RevokeToken(ctx, oauthConfigID); revokeErr != nil {
			logger.Warn(fmt.Sprintf("failed to revoke admin repair token after verification failure for MCP client %s: %v", clientID, revokeErr))
		}
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("OAuth configuration test failed: %v", err))
		return
	}
	tools, toolNameMapping := discovery.Tools, discovery.ToolNameMapping

	// Re-load the row before persisting: verification above holds an
	// upstream network round-trip, and pushing the pre-verification snapshot
	// into the full-row write below would silently restore any fields an
	// admin edited in the meantime.
	clientConfig, err = h.store.ConfigStore.GetMCPClientConfigByID(ctx, clientID)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Verification succeeded but reloading the client failed: %v", err))
		return
	}
	if clientConfig == nil {
		SendError(ctx, fasthttp.StatusNotFound, "Verification succeeded but the MCP client no longer exists")
		return
	}

	// Install the verified credential as the admin discovery credential.
	// Unlike the bootstrap path this is fatal: the whole point of the repair
	// flow is replacing the dead admin row, and failing silently would leave
	// the client stuck in the projected needs_reauth state with a dangling
	// shared row. Tools are deliberately not touched on failure.
	if promoteErr := h.store.ConfigStore.PromoteSharedOauthTokenToAdmin(ctx, oauthConfigID, clientID); promoteErr != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Verification succeeded but installing the repaired admin credential failed: %v", promoteErr))
		return
	}

	clientConfig.DiscoveredTools = tools
	clientConfig.DiscoveredToolNameMapping = toolNameMapping
	clientConfig.DiscoveredResources, clientConfig.DiscoveredResourceTemplates, clientConfig.DiscoveredPrompts = discovery.Resources, discovery.ResourceTemplates, discovery.Prompts
	clientConfig.DiscoveredInstructions = discovery.Instructions

	// Persist the refreshed tool set via UpdateMCPClientConfig. The store
	// unconditionally writes every editable column from the given struct, so
	// the update must carry the full config; clientConfig was re-loaded
	// above and carries the discovered tools.
	updateReq := &configstoreTables.TableMCPClient{
		ClientID:                  clientConfig.ID,
		Name:                      clientConfig.Name,
		IsCodeModeClient:          clientConfig.IsCodeModeClient,
		ConnectionType:            string(clientConfig.ConnectionType),
		ConnectionString:          clientConfig.ConnectionString,
		StdioConfig:               clientConfig.StdioConfig,
		TLSConfig:                 clientConfig.TLSConfig,
		AuthType:                  string(clientConfig.AuthType),
		OauthConfigID:             clientConfig.OauthConfigID,
		ToolsToExecute:            clientConfig.ToolsToExecute,
		ToolsToAutoExecute:        clientConfig.ToolsToAutoExecute,
		PromptsToExpose:           clientConfig.PromptsToExpose,
		ResourcesToExpose:         clientConfig.ResourcesToExpose,
		Headers:                   clientConfig.Headers,
		AllowedExtraHeaders:       clientConfig.AllowedExtraHeaders,
		IsPingAvailable:           clientConfig.IsPingAvailable,
		NeedsSessionStickiness:    clientConfig.NeedsSessionStickiness,
		ToolPricing:               clientConfig.ToolPricing,
		ToolSyncInterval:          int(clientConfig.ToolSyncInterval / time.Second),
		MaxProtocolVersion:        clientConfig.MaxProtocolVersion,
		ToolExecutionTimeout:      int(clientConfig.ToolExecutionTimeout / time.Second),
		MaxInstructionsLength:     clientConfig.MaxInstructionsLength,
		AllowByDefault:            clientConfig.AllowByDefault,
		PerUserHeaderKeys:         clientConfig.PerUserHeaderKeys,
		DiscoveredTools:           clientConfig.DiscoveredTools,
		DiscoveredToolNameMapping: clientConfig.DiscoveredToolNameMapping,
		Disabled:                  clientConfig.Disabled,
	}
	// Past this point the credential is already promoted, so failures here
	// are logged as a partial success rather than returned as an error:
	// PromoteSharedOauthTokenToAdmin already deleted the shared row and
	// flipped the admin row to 'active', so complete-oauth's replay guard
	// now finds no shared row and rejects a repeat hit with 409 "already
	// completed". reauthorizeMCPClient has no such guard and stays usable
	// after promotion — POST /reauthorize is how an admin retries after a
	// tool-list persistence failure like this one. Failing the request here
	// outright would still wedge the client with a stale tool list for no
	// reason: the credential itself is fine, and the periodic tool syncer
	// will refresh the list on its next tick regardless.
	if err := h.store.ConfigStore.UpdateMCPClientConfig(ctx, clientConfig.ID, updateReq); err != nil {
		logger.Error(fmt.Sprintf(
			"[PARTIAL SUCCESS] Admin discovery credential for MCP client %s was repaired but persisting the refreshed tool list failed: %v",
			clientConfig.ID, err,
		))
	}

	// Refresh the manager's config with the discovered tools, then set them
	// on the client. Per-user auth clients hold no shared upstream
	// connection, so there is nothing to reconnect.
	if err := h.updateMCPClientWithRetry(bifrostCtx, clientConfig.ID, clientConfig); err != nil {
		logger.Error(fmt.Sprintf(
			"[PARTIAL SUCCESS] Admin discovery credential for MCP client %s was repaired but refreshing the runtime client failed: %v",
			clientConfig.ID, err,
		))
	}
	h.persistMCPDiscovery(ctx, clientConfig.ID, discovery)
	h.mcpManager.SetClientDiscovery(clientConfig.ID, discovery)

	h.recordMCPClientAction(ctx, "mcp_client.oauth_completed", clientConfig.ID, clientConfig.Name, map[string]any{
		"auth_type":       clientConfig.AuthType,
		"oauth_config_id": oauthConfigID,
		"outcome":         "admin_credential_repaired",
		"tools_count":     len(tools),
	})
	SendJSON(ctx, map[string]any{
		"status":      "success",
		"message":     fmt.Sprintf("Admin discovery credential repaired successfully. %d tools discovered.", len(tools)),
		"tools_count": len(tools),
	})
}

// completeMCPClientOAuth handles POST /api/mcp/client/{id}/complete-oauth - Complete MCP client creation after OAuth authorization
// The {id} parameter is the oauth_config_id returned from the initial addMCPClient call
func (h *MCPHandler) completeMCPClientOAuth(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "MCP operations unavailable: config store is disabled")
		return
	}
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.store)
	defer cancel()

	oauthConfigID, err := getIDFromCtx(ctx)
	if err != nil {
		logger.Error(fmt.Sprintf("[OAuth Complete] Invalid oauth_config_id: %v", err))
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid oauth_config_id: %v", err))
		return
	}

	logger.Debug(fmt.Sprintf("[OAuth Complete] Completing OAuth for oauth_config_id: %s", oauthConfigID))

	// Check if OAuth flow is authorized
	oauthConfig, err := h.store.ConfigStore.GetOauthConfigByID(ctx, oauthConfigID)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get OAuth config: %v", err))
		return
	}

	if oauthConfig == nil {
		SendError(ctx, fasthttp.StatusNotFound, "OAuth config not found")
		return
	}

	if oauthConfig.Status != "authorized" {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("OAuth not authorized yet. Current status: %s", oauthConfig.Status))
		return
	}

	// Get MCP client config from database (stored with oauth_config for multi-instance support)
	mcpClientConfig, err := h.oauthHandler.GetPendingMCPClient(oauthConfigID)
	if err != nil {
		logger.Error(fmt.Sprintf("[OAuth Complete] Failed to get pending MCP client: %v", err))
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get pending MCP client: %v", err))
		return
	}
	// Config-bootstrap fallback: the inline mcp_client_config_json blob on
	// oauth_configs is populated only by the UI Create flow's
	// StorePendingMCPClient. For clients bootstrapped from config.json the
	// MCP client row already exists in pending_verification, linked to this
	// oauth_configs row via oauth_config_id; resolve it that way instead.
	configBootstrap := false
	if mcpClientConfig == nil {
		dbClient, lookupErr := h.store.ConfigStore.GetMCPClientByOauthConfigID(ctx, oauthConfigID)
		if lookupErr != nil && !errors.Is(lookupErr, configstore.ErrNotFound) {
			logger.Error(fmt.Sprintf("[OAuth Complete] Failed to look up MCP client by oauth_config_id: %v", lookupErr))
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to look up MCP client: %v", lookupErr))
			return
		}
		if dbClient == nil {
			SendError(ctx, fasthttp.StatusNotFound, "MCP client not found for this OAuth flow. The flow may have expired or already been completed.")
			return
		}
		// Gate the fallback to genuine bootstrap completions:
		// 1. AuthType must be one of the OAuth-based types. The downstream
		//    handler branches on AuthType to take the per-user verification
		//    path (which treats the upstream token as an admin temp
		//    credential and revokes it) vs the shared completion path.
		//    per_user_headers and other non-OAuth types cannot complete
		//    through this endpoint.
		// 2. PendingOAuthConfigJSON must still be set — once cleared, the
		//    client has already completed bootstrap, and any further hit
		//    on complete-oauth with this oauth_config_id is a replay (the
		//    JSON blob is gone because RemovePendingMCPClient ran, and the
		//    bootstrap stash is gone because ClearMCPClientPendingOAuthConfig
		//    ran). Reject with 409 so callers don't trigger redundant DB
		//    writes + reconnects on an already-connected client.
		if dbClient.AuthType != string(schemas.MCPAuthTypeOauth) && dbClient.AuthType != string(schemas.MCPAuthTypePerUserOauth) {
			SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("OAuth config does not match an OAuth-based MCP client (auth_type=%q)", dbClient.AuthType))
			return
		}
		if dbClient.PendingOAuthConfigJSON == nil || *dbClient.PendingOAuthConfigJSON == "" {
			// Not a bootstrap completion. Three possibilities: a genuine
			// replay (client already active, this exact oauth_config_id's
			// flow completed long ago, nothing new happened), a shared
			// reauth (auth_type 'oauth', a fresh admin flow via
			// POST /reauthorize just replaced the connection token), or a
			// per_user_oauth admin repair (same /reauthorize trigger, but
			// what gets replaced is the retained admin tool-discovery
			// credential, not any connection).
			if dbClient.AuthType != string(schemas.MCPAuthTypeOauth) {
				// per_user_oauth: a just-completed admin repair flow always
				// leaves a fresh ACTIVE auth_mode='shared' token behind
				// (CompleteOAuthFlow's default write); its absence means
				// nothing new happened and this hit is a genuine replay.
				sharedToken, tokErr := h.store.ConfigStore.GetSharedOauthTokenByConfigID(ctx, oauthConfigID)
				if tokErr != nil {
					SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to look up bootstrap token: %v", tokErr))
					return
				}
				if sharedToken == nil || sharedToken.Status != "active" {
					SendError(ctx, fasthttp.StatusConflict, "OAuth flow has already been completed for this MCP client")
					return
				}
				h.completePerUserOAuthAdminRepair(ctx, bifrostCtx, oauthConfigID, dbClient.ClientID)
				return
			}
			// Shared oauth's own freshness check: unlike per_user_oauth's
			// sharedToken-presence check above, a shared client already has
			// an active token from its ORIGINAL auth sitting there the whole
			// time a reauth is in flight — token presence/status alone can't
			// tell a stale credential from a freshly-rotated one. The flow
			// row can: see isPrematureOAuthCompletion's doc.
			if pendingFlow, flowErr := h.store.ConfigStore.GetOauthUserSessionByModeIdentityAndMCPClient(ctx, schemas.MCPAuthModeAdmin, "", dbClient.ClientID); flowErr != nil {
				logger.Warn(fmt.Sprintf("failed to check for an in-flight reauth flow for MCP client %s: %v", dbClient.ClientID, flowErr))
			} else if isPrematureOAuthCompletion(pendingFlow, time.Now()) {
				SendError(ctx, fasthttp.StatusConflict, "Authorization has not completed yet: the browser flow may still be open, or the upstream provider rejected it before redirecting back. Wait for it to finish, or retry reauthorize.")
				return
			}
			reauthClientConfig, err := h.store.ConfigStore.GetMCPClientConfigByID(ctx, dbClient.ClientID)
			if err != nil || reauthClientConfig == nil {
				SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to load MCP client: %v", err))
				return
			}
			if err := h.updateMCPClientCredentialsWithRetry(bifrostCtx, reauthClientConfig.ID, reauthClientConfig); err != nil && !errors.Is(err, schemas.ErrMCPReconnectNotApplicable) {
				logger.Error(fmt.Sprintf("Failed to apply refreshed OAuth credentials to the live MCP client %s: %v", reauthClientConfig.ID, err))
				SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("OAuth credentials refreshed but applying them to the live client failed: %v", err))
				return
			}
			// A per-call client refreshes its tool list synchronously inside
			// UpdateMCPClientCredentials (mirroring the rediscovery a sticky
			// client gets from its reconnect), so success means fresh tools
			// on both connection modes, and the message reports what actually
			// happened for each mode. ErrMCPReconnectNotApplicable can still
			// come back for a disabled per-call client and is not an error:
			// the fresh credential is already what the next dial after
			// re-enabling will use.
			successMsg := "MCP client re-authorized and reconnected successfully"
			if h.mcpManager.RequiresPerCallConnection(reauthClientConfig) {
				successMsg = "MCP client re-authorized and tools refreshed successfully"
			}
			h.recordMCPClientAction(ctx, "mcp_client.oauth_completed", reauthClientConfig.ID, reauthClientConfig.Name, map[string]any{
				"auth_type":       reauthClientConfig.AuthType,
				"oauth_config_id": oauthConfigID,
				"outcome":         "reauthorized",
			})
			SendJSON(ctx, map[string]any{"status": "success", "message": successMsg})
			return
		}
		mcpClientConfig, err = h.store.ConfigStore.GetMCPClientConfigByID(ctx, dbClient.ClientID)
		if err != nil || mcpClientConfig == nil {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to load MCP client: %v", err))
			return
		}
		mcpClientConfig.OauthConfigID = &oauthConfigID
		// Drop the bootstrap stash from the in-memory config so the MCP
		// manager's reconnect path takes the normal connect branch instead
		// of re-parking the client in pending_verification.
		mcpClientConfig.PendingOAuthConfig = nil
		configBootstrap = true
	}

	// If pending config points to an existing client, this is an OAuth credential update.
	var existingDBConfig *configstoreTables.TableMCPClient
	if h.store.ConfigStore != nil {
		existingDBConfig, err = h.store.ConfigStore.GetMCPClientByID(ctx, mcpClientConfig.ID)
		if err != nil && !errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to get existing mcp client config: %v", err))
			return
		}
	}
	isUpdateFlow := existingDBConfig != nil

	// Handle per-user OAuth completion: verify connection with admin's temp token,
	// discover tools, create client (without persistent connection), discard token.
	if mcpClientConfig.AuthType == schemas.MCPAuthTypePerUserOauth {
		// Get admin's temporary access token for verification
		accessToken, err := h.oauthHandler.GetAccessToken(ctx, oauthConfigID)
		if err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get admin access token for verification: %v", err))
			return
		}
		// Always clean up admin's pending config, even on failure
		defer h.oauthHandler.RemovePendingMCPClient(oauthConfigID)

		// Verify connection and discover tools using admin's temp token
		discovery, err := h.mcpManager.VerifyPerUserOAuthConnection(bifrostCtx, mcpClientConfig, accessToken)
		if err != nil {
			// Nothing worth retaining on a failed verification: revoke the
			// admin's temp token immediately so it isn't left behind (it
			// used to get this for free from an unconditional defer, before
			// retention on success made cleanup conditional).
			if revokeErr := h.oauthHandler.RevokeToken(ctx, oauthConfigID); revokeErr != nil {
				logger.Warn(fmt.Sprintf("failed to revoke admin bootstrap token after verification failure for MCP client %s: %v", mcpClientConfig.ID, revokeErr))
			}
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("OAuth configuration test failed: %v", err))
			return
		}
		tools, toolNameMapping := discovery.Tools, discovery.ToolNameMapping

		// Attach discovered tools before persisting so the DB row includes them from the start.
		mcpClientConfig.DiscoveredTools = tools
		mcpClientConfig.DiscoveredToolNameMapping = toolNameMapping
		mcpClientConfig.DiscoveredResources, mcpClientConfig.DiscoveredResourceTemplates, mcpClientConfig.DiscoveredPrompts = discovery.Resources, discovery.ResourceTemplates, discovery.Prompts
		mcpClientConfig.DiscoveredInstructions = discovery.Instructions

		if isUpdateFlow {
			oldDBConfig := *existingDBConfig
			updateReq := &configstoreTables.TableMCPClient{
				ClientID:                  mcpClientConfig.ID,
				Name:                      mcpClientConfig.Name,
				IsCodeModeClient:          mcpClientConfig.IsCodeModeClient,
				ConnectionType:            string(mcpClientConfig.ConnectionType),
				ConnectionString:          mcpClientConfig.ConnectionString,
				StdioConfig:               mcpClientConfig.StdioConfig,
				TLSConfig:                 mcpClientConfig.TLSConfig,
				AuthType:                  string(mcpClientConfig.AuthType),
				OauthConfigID:             mcpClientConfig.OauthConfigID,
				ToolsToExecute:            mcpClientConfig.ToolsToExecute,
				ToolsToAutoExecute:        mcpClientConfig.ToolsToAutoExecute,
				PromptsToExpose:           mcpClientConfig.PromptsToExpose,
				ResourcesToExpose:         mcpClientConfig.ResourcesToExpose,
				Headers:                   mcpClientConfig.Headers,
				AllowedExtraHeaders:       mcpClientConfig.AllowedExtraHeaders,
				IsPingAvailable:           mcpClientConfig.IsPingAvailable,
				NeedsSessionStickiness:    mcpClientConfig.NeedsSessionStickiness,
				ToolPricing:               mcpClientConfig.ToolPricing,
				ToolSyncInterval:          int(mcpClientConfig.ToolSyncInterval / time.Second),
				MaxProtocolVersion:        mcpClientConfig.MaxProtocolVersion,
				ToolExecutionTimeout:      int(mcpClientConfig.ToolExecutionTimeout / time.Second),
				MaxInstructionsLength:     mcpClientConfig.MaxInstructionsLength,
				AllowByDefault:            mcpClientConfig.AllowByDefault,
				PerUserHeaderKeys:         mcpClientConfig.PerUserHeaderKeys,
				DiscoveredTools:           mcpClientConfig.DiscoveredTools,
				DiscoveredToolNameMapping: mcpClientConfig.DiscoveredToolNameMapping,
				Disabled:                  mcpClientConfig.Disabled,
			}
			if err := h.store.ConfigStore.UpdateMCPClientConfig(ctx, mcpClientConfig.ID, updateReq); err != nil {
				SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to update MCP config: %v", err))
				return
			}
			if err := h.updateMCPClientWithRetry(bifrostCtx, mcpClientConfig.ID, mcpClientConfig); err != nil {
				if rollbackErr := h.store.ConfigStore.UpdateMCPClientConfig(ctx, mcpClientConfig.ID, &oldDBConfig); rollbackErr != nil {
					logger.Error(fmt.Sprintf("Failed to rollback MCP client DB update: %v. please restart bifrost to keep core and database in sync", rollbackErr))
				}
				SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to update MCP client: %v", err))
				return
			}
		} else {
			// Persist MCP client config in config store (BeforeSave hook serializes DiscoveredTools)
			if h.store.ConfigStore != nil {
				if err := h.store.ConfigStore.CreateMCPClientConfig(ctx, mcpClientConfig); err != nil {
					if errors.Is(err, configstore.ErrAlreadyExists) {
						SendError(ctx, fasthttp.StatusConflict, "An MCP client with this name already exists")
						return
					}
					if errors.Is(err, configstore.ErrMCPEndpointSlugExists) {
						SendError(ctx, fasthttp.StatusConflict, "An MCP endpoint with this slug already exists")
						return
					}
					if errors.Is(err, configstore.ErrMCPEndpointSlugInvalid) {
						SendError(ctx, fasthttp.StatusBadRequest, "Could not derive an endpoint slug from the name; provide an endpoint_slug")
						return
					}
					SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to create MCP config: %v", err))
					return
				}
			}

			// Add MCP client to manager (skips connection for per_user_oauth)
			if err := h.mcpManager.AddMCPClient(bifrostCtx, mcpClientConfig); err != nil {
				// Clean up DB entry on failure
				if h.store.ConfigStore != nil {
					if delErr := h.store.ConfigStore.DeleteMCPClientConfig(ctx, mcpClientConfig.ID); delErr != nil {
						logger.Error(fmt.Sprintf("Failed to delete MCP client config from database: %v. please restart bifrost to keep core and database in sync", delErr))
						SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to delete MCP client config from database: %v. please restart bifrost to keep core and database in sync", delErr))
						return
					}
				}
				SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to register MCP client: %v", err))
				return
			}
		}

		// Set discovered tools on the client
		h.persistMCPDiscovery(ctx, mcpClientConfig.ID, discovery)
		h.mcpManager.SetClientDiscovery(mcpClientConfig.ID, discovery)

		// Retain the admin's bootstrap-verification token instead of
		// discarding it via RevokeToken: promote the row CompleteOAuthFlow
		// wrote as auth_mode='shared' (its default write for any
		// admin-flow-mode completion) to 'admin', so the periodic tool
		// syncer (ClientToolSyncer.performSync) can use it for later
		// tool-discovery refresh. PromoteSharedOauthTokenToAdmin reconciles
		// a pre-existing admin row for this client atomically, so it can't
		// collide with idx_mcp_oauth_tokens_admin_mcp. Deferred until here
		// (after the client's DB row and runtime registration above have
		// both succeeded) so a failure in either doesn't leave a promoted
		// admin credential pointing at a client that was never actually
		// persisted. Best-effort beyond that point — a failure here doesn't
		// fail the request, since the client is otherwise fully verified and
		// working; it just means the tool list can't be refreshed later
		// until the admin repairs the discovery credential.
		if promoteErr := h.store.ConfigStore.PromoteSharedOauthTokenToAdmin(ctx, oauthConfigID, mcpClientConfig.ID); promoteErr != nil {
			logger.Warn(fmt.Sprintf("failed to retain admin bootstrap token for MCP client %s: %v", mcpClientConfig.ID, promoteErr))
		}

		// For clients bootstrapped from config.json, drop the
		// pending_oauth_config_json stash now that verification succeeded.
		// This branch returns before the shared completion path below, so
		// the stash must be cleared here as well; otherwise the next boot
		// rehydrates it and parks the verified client back in
		// pending_verification. A failed clear must surface as an error:
		// a silent success would hide the drift until a restart re-parks
		// the client. The replay guard reads the stash, which is still
		// set, so re-running verification retries the whole flow
		// including this clear.
		if configBootstrap {
			if err := h.store.ConfigStore.ClearMCPClientPendingOAuthConfig(ctx, mcpClientConfig.ID); err != nil {
				logger.Error(fmt.Sprintf(
					"[PARTIAL SUCCESS] Per-user OAuth MCP client %s was verified and activated but clearing the pending bootstrap config failed: %v. "+
						"Runtime and database state have drifted: the client works now but will return to pending_verification on restart. Re-run verification to retry.",
					mcpClientConfig.ID, err,
				))
				SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Client verified and activated but clearing the pending bootstrap config failed, so it will return to pending_verification on restart. Re-run verification to retry: %v", err))
				return
			}
		}

		logger.Debug(fmt.Sprintf("[OAuth Complete] Per-user OAuth MCP client verified and created: %s (%d tools)", mcpClientConfig.ID, len(tools)))
		message := fmt.Sprintf("OAuth configuration verified successfully. %d tools discovered. Each user will authenticate individually when using this MCP server.", len(tools))
		if isUpdateFlow {
			message = fmt.Sprintf("OAuth credentials updated and verified successfully. %d tools discovered.", len(tools))
		}
		h.recordMCPClientOAuthCompletion(ctx, mcpClientConfig, oauthConfigID, isUpdateFlow, configBootstrap, len(tools))
		SendJSON(ctx, map[string]any{"status": "success", "message": message, "tools_count": len(tools)})
		return
	}

	// Standard server-level OAuth completion
	if isUpdateFlow {
		oldDBConfig := *existingDBConfig
		// The store writes every editable column unconditionally, so the row
		// must carry the full config: omitting a field here silently resets
		// it (a per-client timeout back to the global, or the TLS config).
		updateReq := &configstoreTables.TableMCPClient{
			ClientID:                  mcpClientConfig.ID,
			Name:                      mcpClientConfig.Name,
			IsCodeModeClient:          mcpClientConfig.IsCodeModeClient,
			ConnectionType:            string(mcpClientConfig.ConnectionType),
			ConnectionString:          mcpClientConfig.ConnectionString,
			StdioConfig:               mcpClientConfig.StdioConfig,
			AuthType:                  string(mcpClientConfig.AuthType),
			OauthConfigID:             mcpClientConfig.OauthConfigID,
			ToolsToExecute:            mcpClientConfig.ToolsToExecute,
			ToolsToAutoExecute:        mcpClientConfig.ToolsToAutoExecute,
			PromptsToExpose:           mcpClientConfig.PromptsToExpose,
			ResourcesToExpose:         mcpClientConfig.ResourcesToExpose,
			Headers:                   mcpClientConfig.Headers,
			AllowedExtraHeaders:       mcpClientConfig.AllowedExtraHeaders,
			IsPingAvailable:           mcpClientConfig.IsPingAvailable,
			NeedsSessionStickiness:    mcpClientConfig.NeedsSessionStickiness,
			ToolPricing:               mcpClientConfig.ToolPricing,
			ToolSyncInterval:          int(mcpClientConfig.ToolSyncInterval / time.Second),
			MaxProtocolVersion:        mcpClientConfig.MaxProtocolVersion,
			ToolExecutionTimeout:      int(mcpClientConfig.ToolExecutionTimeout / time.Second),
			MaxInstructionsLength:     mcpClientConfig.MaxInstructionsLength,
			TLSConfig:                 mcpClientConfig.TLSConfig,
			AllowByDefault:            mcpClientConfig.AllowByDefault,
			DiscoveredTools:           mcpClientConfig.DiscoveredTools,
			DiscoveredToolNameMapping: mcpClientConfig.DiscoveredToolNameMapping,
			Disabled:                  mcpClientConfig.Disabled,
		}
		if err := h.store.ConfigStore.UpdateMCPClientConfig(ctx, mcpClientConfig.ID, updateReq); err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to update MCP config: %v", err))
			return
		}
		if err := h.updateMCPClientCredentialsWithRetry(bifrostCtx, mcpClientConfig.ID, mcpClientConfig); err != nil {
			if errors.Is(err, schemas.ErrMCPReconnectNotApplicable) {
				// The client is running in per-call mode (no persistent
				// connection — e.g. a shared client with
				// needs_session_stickiness nil/false), so there is nothing
				// to reconnect: the fresh OAuth credential just persisted
				// above is already what the next per-call dial will use.
				// Not an error — fall through to the normal success path
				// below instead of rolling back the DB update that already
				// succeeded.
				logger.Debug(fmt.Sprintf("[OAuth Complete] Client %s uses per-call connections; nothing to reconnect after OAuth update", mcpClientConfig.ID))
			} else {
				if rollbackErr := h.store.ConfigStore.UpdateMCPClientConfig(ctx, mcpClientConfig.ID, &oldDBConfig); rollbackErr != nil {
					logger.Error(fmt.Sprintf("Failed to rollback MCP client DB update: %v. please restart bifrost to keep core and database in sync", rollbackErr))
				}
				logger.Error(fmt.Sprintf("Failed to reconnect MCP client after OAuth DB update for client %s: %v", mcpClientConfig.ID, err))
				SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to reconnect MCP client with updated OAuth credentials: %v", err))
				return
			}
		}
	} else {
		if h.store.ConfigStore != nil {
			if err := h.store.ConfigStore.CreateMCPClientConfig(ctx, mcpClientConfig); err != nil {
				if errors.Is(err, configstore.ErrAlreadyExists) {
					SendError(ctx, fasthttp.StatusConflict, "An MCP client with this name already exists")
					return
				}
				if errors.Is(err, configstore.ErrMCPEndpointSlugExists) {
					SendError(ctx, fasthttp.StatusConflict, "An MCP endpoint with this slug already exists")
					return
				}
				if errors.Is(err, configstore.ErrMCPEndpointSlugInvalid) {
					SendError(ctx, fasthttp.StatusBadRequest, "Could not derive an endpoint slug from the name; provide an endpoint_slug")
					return
				}
				SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to create MCP config: %v", err))
				return
			}
		}

		// Add MCP client to Bifrost and connect
		if err := h.mcpManager.AddMCPClient(bifrostCtx, mcpClientConfig); err != nil {
			if h.store.ConfigStore != nil {
				if delErr := h.store.ConfigStore.DeleteMCPClientConfig(ctx, mcpClientConfig.ID); delErr != nil {
					logger.Warn(fmt.Sprintf("Failed to rollback MCP client config after add failure: %v", delErr))
				}
			}
			logger.Error(fmt.Sprintf("[OAuth Complete] Failed to connect MCP client: %v", err))
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to connect MCP client: %v", err))
			return
		}
	}

	// Clear pending MCP client config from oauth_config (cleanup)
	if err := h.oauthHandler.RemovePendingMCPClient(oauthConfigID); err != nil {
		logger.Warn(fmt.Sprintf("[OAuth Complete] Failed to clear pending MCP client config: %v", err))
		// Don't fail the request - the MCP client was successfully created
	}

	// For clients bootstrapped from config.json, drop the
	// pending_oauth_config_json stash now that authorization succeeded so
	// the runtime no longer treats the client as pending_verification.
	// A failed clear must surface as an error: a silent success would
	// hide the drift until a restart re-parks the connected client. The
	// stash is still set, so re-running verification retries the whole
	// flow including this clear.
	if configBootstrap {
		if err := h.store.ConfigStore.ClearMCPClientPendingOAuthConfig(ctx, mcpClientConfig.ID); err != nil {
			logger.Error(fmt.Sprintf(
				"[PARTIAL SUCCESS] OAuth MCP client %s completed authorization but clearing the pending bootstrap config failed: %v. "+
					"Runtime and database state have drifted: the client is connected now but will return to pending_verification on restart. Re-run verification to retry.",
				mcpClientConfig.ID, err,
			))
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Client connected but clearing the pending bootstrap config failed, so it will return to pending_verification on restart. Re-run verification to retry: %v", err))
			return
		}
	}

	logger.Debug(fmt.Sprintf("[OAuth Complete] MCP client connected successfully: %s", mcpClientConfig.ID))
	message := "MCP client connected successfully with OAuth"
	if isUpdateFlow {
		message = "MCP client OAuth credentials updated successfully"
	}
	h.recordMCPClientOAuthCompletion(ctx, mcpClientConfig, oauthConfigID, isUpdateFlow, configBootstrap, len(mcpClientConfig.DiscoveredTools))
	SendJSON(ctx, map[string]any{"status": "success", "message": message})
}

// resolvePerUserHeaderKeys returns the per-user-header-key list to persist on
// the updated MCP client. If the request explicitly sets the field, the
// request wins; otherwise the existing schema is preserved. The handler
// rejects an explicit empty list for per_user_headers clients upstream
// (see updateMCPClient validation), so this function cannot be invoked
// with an empty slice for that auth type.
//
// Request-supplied keys are canonicalized (lowercase + trim) here so the
// persisted slice matches the canon form already in stored credential rows
// — see mcputils.CanonicalizeHeaderKey for the invariant. Existing values
// are already canon (they came through this path on create/update), so
// they pass through untouched.
func resolvePerUserHeaderKeys(existing *schemas.MCPClientConfig, req MCPClientUpdateRequest) []string {
	if req.PerUserHeaderKeys != nil {
		return mcputils.CanonicalizeHeaderKeys(*req.PerUserHeaderKeys)
	}
	if existing != nil {
		return existing.PerUserHeaderKeys
	}
	return nil
}

// perUserHeaderKeysAdded reports whether the new schema introduces any key
// absent from the old schema (order-insensitive). Used by updateMCPClient to
// decide whether existing user credentials must be marked 'needs_update'.
// Removed-only changes do not require resubmission because stale stored keys
// are filtered out before use.
func perUserHeaderKeysAdded(oldKeys, newKeys []string) bool {
	if len(newKeys) == 0 {
		return false
	}
	seen := make(map[string]struct{}, len(oldKeys))
	for _, k := range oldKeys {
		seen[k] = struct{}{}
	}
	for _, k := range newKeys {
		if _, ok := seen[k]; !ok {
			return true
		}
	}
	return false
}

// CreateMCPLibraryEntryRequest is the body for POST /api/mcp/library. It carries
// the user-supplied fields of a custom library entry; DB-managed fields (id,
// slug, source, timestamps) are derived server-side. The slug is generated from
// Name, and the unique slug index enforces no-duplicate-name.
type CreateMCPLibraryEntryRequest struct {
	Name               string                    `json:"name"`
	Description        string                    `json:"description,omitempty"`
	Category           string                    `json:"category,omitempty"`
	ConnectionType     schemas.MCPConnectionType `json:"connection_type"`
	ConnectionURL      string                    `json:"connection_url,omitempty"`
	StdioConfig        *schemas.MCPStdioConfig   `json:"stdio_config,omitempty"`
	AuthType           schemas.MCPAuthType       `json:"auth_type,omitempty"`
	RequiredHeaderKeys []string                  `json:"required_header_keys,omitempty"`
	IconURL            string                    `json:"icon_url,omitempty"`
	DocsURL            string                    `json:"docs_url,omitempty"`
	Publisher          string                    `json:"publisher,omitempty"`
	Tags               []string                  `json:"tags,omitempty"`
}

// createMCPLibraryEntry handles POST /api/mcp/library — publishes an org-internal
// ("custom") MCP server into the library so other members can discover and
// install it. The entry is protected from the remote sync (see Source/skip-set
// in SyncMCPLibrary). A duplicate name (same generated slug) returns 409.
func (h *MCPHandler) createMCPLibraryEntry(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "MCP operations unavailable: config store is disabled")
		return
	}

	var req CreateMCPLibraryEntryRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "name is required")
		return
	}

	// Validate connection type and the matching connection field.
	switch req.ConnectionType {
	case schemas.MCPConnectionTypeHTTP, schemas.MCPConnectionTypeSSE:
		if strings.TrimSpace(req.ConnectionURL) == "" {
			SendError(ctx, fasthttp.StatusBadRequest, "connection_url is required for http/sse connection types")
			return
		}
	case schemas.MCPConnectionTypeSTDIO:
		if req.StdioConfig == nil || strings.TrimSpace(req.StdioConfig.Command) == "" {
			SendError(ctx, fasthttp.StatusBadRequest, "stdio_config.command is required for stdio connection type")
			return
		}
	default:
		SendError(ctx, fasthttp.StatusBadRequest, "connection_type must be one of: http, stdio, sse")
		return
	}

	// Default and validate auth type.
	if req.AuthType == "" {
		req.AuthType = schemas.MCPAuthTypeNone
	}
	switch req.AuthType {
	case schemas.MCPAuthTypeNone, schemas.MCPAuthTypeHeaders, schemas.MCPAuthTypeOauth,
		schemas.MCPAuthTypePerUserOauth, schemas.MCPAuthTypePerUserHeaders,
		schemas.MCPAuthTypeTokenExchange:
	default:
		SendError(ctx, fasthttp.StatusBadRequest, "invalid auth_type")
		return
	}

	slug := modelcatalog.Slugify(req.Name)
	if slug == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "name must contain at least one alphanumeric character")
		return
	}

	now := time.Now()
	entry := &configstoreTables.TableMCPLibrary{
		Slug:               slug,
		Name:               req.Name,
		Description:        req.Description,
		Category:           req.Category,
		ConnectionType:     req.ConnectionType,
		ConnectionURL:      req.ConnectionURL,
		StdioConfig:        req.StdioConfig,
		AuthType:           req.AuthType,
		RequiredHeaderKeys: req.RequiredHeaderKeys,
		IconURL:            req.IconURL,
		DocsURL:            req.DocsURL,
		Publisher:          req.Publisher,
		Tags:               req.Tags,
		Source:             "custom",
		CreatedAt:          now,
		UpdatedAt:          now,
	}

	if err := h.store.ConfigStore.CreateCustomMCPLibraryEntry(ctx, entry); err != nil {
		if errors.Is(err, configstore.ErrAlreadyExists) {
			SendError(ctx, fasthttp.StatusConflict, "an MCP library server with this name already exists")
			return
		}
		logger.Error("failed to create custom MCP library entry: %v", err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to create MCP library entry")
		return
	}

	lib.RecordAuditChange(ctx, "mcp_library_entry.created", "mcp_library_entry", strconv.FormatUint(uint64(entry.ID), 10), entry.Name, nil, auditMCPLibraryEntry(entry))
	SendJSON(ctx, map[string]any{
		"status":  "success",
		"message": "MCP library server published successfully",
		"entry":   entry,
	})
}

// deleteMCPLibraryEntry handles DELETE /api/mcp/library/{id} — soft-deletes a
// library entry (remote or custom) by numeric ID. The row is hidden from
// listings and the remote sync respects the tombstone, so a hidden remote entry
// is never resurrected. Also the escape hatch for a duplicate-name lockout.
func (h *MCPHandler) deleteMCPLibraryEntry(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "MCP operations unavailable: config store is disabled")
		return
	}
	idStr, err := getIDFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid id: %v", err))
		return
	}
	id, err := strconv.ParseUint(idStr, 10, 64)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "id must be a positive integer")
		return
	}

	if err := h.store.ConfigStore.DeleteMCPLibraryEntry(ctx, uint(id)); err != nil {
		if errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, "MCP library entry not found")
			return
		}
		logger.Error("failed to delete MCP library entry %d: %v", id, err)
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to delete MCP library entry")
		return
	}

	lib.RecordAuditChange(ctx, "mcp_library_entry.deleted", "mcp_library_entry", idStr, idStr, map[string]any{"id": id}, nil)
	SendJSON(ctx, map[string]any{
		"status":  "success",
		"message": "MCP library server removed successfully",
	})
}

// refreshMCPClientOnAssignedVirtualKeys makes every cached virtual key that
// references the updated MCP client see its new row (name, tools, headers).
//
// The assigned keys are reloaded through one batched ReloadVirtualKeys call,
// which does for each key exactly what ReloadVirtualKey does (re-read with
// every relation, VK-scoped model configs, token and credential eviction) and
// which a clustered deployment propagates to peers. It replaces a
// ReloadVirtualKey per assigned key: at 100k assigned keys that was well over a
// million queries, run synchronously inside the request. If the batched reload
// fails, each key is reloaded on its own as before.
func (h *MCPHandler) refreshMCPClientOnAssignedVirtualKeys(ctx context.Context, clientID string, dbID uint) {
	if h.store == nil || h.store.ConfigStore == nil || h.governanceManager == nil {
		return
	}
	assignedVKs, listErr := h.store.ConfigStore.GetVirtualKeyMCPConfigsByMCPClientID(ctx, dbID)
	if listErr != nil {
		logger.Error(fmt.Sprintf("failed to fetch VK assignments for MCP client %s after update: %v", clientID, listErr))
		return
	}
	if len(assignedVKs) == 0 {
		return
	}
	ids := make([]string, 0, len(assignedVKs))
	for _, av := range assignedVKs {
		ids = append(ids, av.VirtualKeyID)
	}
	err := h.governanceManager.ReloadVirtualKeys(ctx, ids)
	if err == nil {
		return
	}
	logger.Error(fmt.Sprintf("batched virtual key reload for MCP client %s failed, falling back to per-key reload: %v", clientID, err))
	for _, id := range ids {
		if _, err := h.governanceManager.ReloadVirtualKey(ctx, id); err != nil {
			logger.Error(fmt.Sprintf("failed to reload virtual key %s after MCP client update: %v", id, err))
		}
	}
}

// recordMCPClientAction records an action-style audit event against an MCP client, naming it from the in-memory config when available.
func (h *MCPHandler) recordMCPClientAction(ctx *fasthttp.RequestCtx, eventName, clientID, clientName string, after map[string]any) {
	if clientName == "" {
		clientName = clientID
		if existing := findMCPClientConfig(h.store, clientID); existing != nil && existing.Name != "" {
			clientName = existing.Name
		}
	}
	lib.RecordAuditChange(ctx, eventName, "mcp_client", clientID, clientName, nil, after)
}

// auditMCPLibraryEntry snapshots a library entry without URL credentials or stdio args/env, which a custom entry may carry inline.
func auditMCPLibraryEntry(entry *configstoreTables.TableMCPLibrary) map[string]any {
	if entry == nil {
		return nil
	}
	snap := map[string]any{
		"id":                   entry.ID,
		"slug":                 entry.Slug,
		"name":                 entry.Name,
		"description":          entry.Description,
		"category":             entry.Category,
		"connection_type":      entry.ConnectionType,
		"auth_type":            entry.AuthType,
		"required_header_keys": entry.RequiredHeaderKeys,
		"publisher":            entry.Publisher,
		"tags":                 entry.Tags,
		"source":               entry.Source,
	}
	if entry.ConnectionURL != "" {
		snap["connection_url"] = lib.RedactURLCredentials(entry.ConnectionURL)
	}
	if entry.StdioConfig != nil {
		snap["stdio_command"] = entry.StdioConfig.Command
	}
	return snap
}

// recordMCPClientOAuthCompletion records complete-oauth, plus mcp_client.created when the flow created the client row.
func (h *MCPHandler) recordMCPClientOAuthCompletion(ctx *fasthttp.RequestCtx, cfg *schemas.MCPClientConfig, oauthConfigID string, isUpdateFlow, configBootstrap bool, toolsCount int) {
	outcome := "connected"
	switch {
	case isUpdateFlow && configBootstrap:
		outcome = "bootstrap_verified"
	case isUpdateFlow:
		outcome = "credentials_updated"
	}
	h.recordMCPClientAction(ctx, "mcp_client.oauth_completed", cfg.ID, cfg.Name, map[string]any{
		"auth_type":       cfg.AuthType,
		"oauth_config_id": oauthConfigID,
		"outcome":         outcome,
		"tools_count":     toolsCount,
	})
	if !isUpdateFlow {
		lib.RecordAuditChange(ctx, "mcp_client.created", "mcp_client", cfg.ID, cfg.Name, nil, auditMCPClient(h.store, cfg))
	}
}
