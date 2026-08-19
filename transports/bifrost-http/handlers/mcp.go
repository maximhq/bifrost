// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains MCP (Model Context Protocol) tool execution handlers.
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fasthttp/router"
	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/mcp"
	mcputils "github.com/maximhq/bifrost/core/mcp/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
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
	// CloseAndMarkNeedsReauth closes a shared client's live upstream
	// connection and flips it to needs_reauth, without attempting a new
	// dial. Used after OAuth credential rotation.
	CloseAndMarkNeedsReauth(ctx context.Context, id string) error
	DisableMCPClient(ctx context.Context, id string) error
	EnableMCPClient(ctx context.Context, id string) error
	// VerifyPerUserOAuthConnection verifies an MCP server using a temporary access
	// token and discovers available tools. The connection is closed after verification.
	VerifyPerUserOAuthConnection(ctx context.Context, config *schemas.MCPClientConfig, accessToken string) (map[string]schemas.ChatTool, map[string]string, error)
	// VerifyHeadersConnection verifies an MCP server using a caller-supplied set
	// of header values (admin sample or user-submitted) and discovers available
	// tools. The connection is closed after verification. Mirrors
	// VerifyPerUserOAuthConnection's role for MCPAuthTypePerUserHeaders.
	VerifyHeadersConnection(ctx context.Context, config *schemas.MCPClientConfig, userHeaders map[string]string) (map[string]schemas.ChatTool, map[string]string, error)
	// SetClientTools updates the tool map for an existing client.
	SetClientTools(clientID string, tools map[string]schemas.ChatTool, toolNameMapping map[string]string)
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
	r.PUT("/api/mcp/client/{id}", lib.ChainMiddlewares(h.updateMCPClient, middlewares...))
	r.DELETE("/api/mcp/client/{id}", lib.ChainMiddlewares(h.deleteMCPClient, middlewares...))
	r.POST("/api/mcp/client/{id}/reconnect", lib.ChainMiddlewares(h.reconnectMCPClient, middlewares...))
	r.POST("/api/mcp/client/{id}/complete-oauth", lib.ChainMiddlewares(h.completeMCPClientOAuth, middlewares...))
	r.POST("/api/mcp/client/{id}/initiate-verification", lib.ChainMiddlewares(h.initiateMCPClientVerification, middlewares...))
	r.POST("/api/mcp/client/{id}/reauthorize", lib.ChainMiddlewares(h.reauthorizeMCPClient, middlewares...))
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
// Inputs: the resolved OAuth config and the MCP server URL
// (config.ConnectionString.GetValue()). The returned flowInitiation
// carries oauth_config_id, authorize_url, state, and expires_at.
//
// This helper does NOT touch config_mcp_clients or
// oauth_configs.mcp_client_config_json — row-lifecycle management is the
// caller's concern.
func (h *MCPHandler) runOAuthBootstrap(ctx *fasthttp.RequestCtx, oauthCfg *OAuthConfigRequest, serverURL string) (*schemas.OAuth2FlowInitiation, error) {
	redirectURI := lib.BuildBaseURL(ctx, h.store.GetMCPExternalClientURL()) + "/api/oauth/callback"
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
// why the token needs it.
func (h *MCPHandler) reauthorizeMCPClient(ctx *fasthttp.RequestCtx) {
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

	redirectURI := lib.BuildBaseURL(ctx, h.store.GetMCPExternalClientURL()) + "/api/oauth/callback"
	flowInitiation, flowID, err := h.store.OAuthProvider.InitiateUserOAuthFlow(ctx, *clientConfig.OauthConfigID, clientConfig.ID, redirectURI, schemas.MCPAuthModeAdmin)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to initiate reauthorization: %v", err))
		return
	}
	authorizeURL, err := h.store.OAuthProvider.BuildAdminUpstreamAuthorizeURL(ctx, flowID)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to build authorize URL: %v", err))
		return
	}

	completeURL := fmt.Sprintf("/api/mcp/client/%s/complete-oauth", flowInitiation.OauthConfigID)
	statusURL := fmt.Sprintf("/api/oauth/config/%s/status", flowInitiation.OauthConfigID)
	SendJSON(ctx, map[string]any{
		"status":          "pending_oauth",
		"oauth_config_id": flowInitiation.OauthConfigID,
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
	})
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
// inert (its status can never become authorized), it is just not cleaned
// up automatically.
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
	flowInitiation, err := h.runOAuthBootstrap(ctx, oauthCfg, clientConfig.ConnectionString.GetValue())
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to initiate OAuth flow: %v", err))
		return
	}

	if err := h.store.ConfigStore.UpdateMCPClientOAuthConfigID(ctx, clientConfig.ID, &flowInitiation.OauthConfigID); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to link OAuth config to MCP client: %v", err))
		return
	}

	completeURL := fmt.Sprintf("/api/mcp/client/%s/complete-oauth", flowInitiation.OauthConfigID)
	statusURL := fmt.Sprintf("/api/oauth/config/%s/status", flowInitiation.OauthConfigID)
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

	tools, toolNameMapping, verifyErr := h.mcpManager.VerifyHeadersConnection(bifrostCtx, clientConfig, canonUserHeaders)
	if verifyErr != nil {
		SendError(ctx, fasthttp.StatusUnprocessableEntity, fmt.Sprintf("Verification failed: %v", verifyErr))
		return
	}

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
		Headers:                   clientConfig.Headers,
		AllowedExtraHeaders:       clientConfig.AllowedExtraHeaders,
		IsPingAvailable:           clientConfig.IsPingAvailable,
		NeedsSessionStickiness:    clientConfig.NeedsSessionStickiness,
		ToolPricing:               clientConfig.ToolPricing,
		ToolSyncInterval:          int(clientConfig.ToolSyncInterval / time.Second),
		ToolExecutionTimeout:      int(clientConfig.ToolExecutionTimeout / time.Second),
		AllowOnAllVirtualKeys:     clientConfig.AllowOnAllVirtualKeys,
		PerUserHeaderKeys:         clientConfig.PerUserHeaderKeys,
		DiscoveredTools:           clientConfig.DiscoveredTools,
		DiscoveredToolNameMapping: clientConfig.DiscoveredToolNameMapping,
		Disabled:                  clientConfig.Disabled,
	}
	if err := h.store.ConfigStore.UpdateMCPClientConfig(ctx, clientConfig.ID, updateReq); err != nil {
		// NOTE: Partial success; the runtime client is activated (Healthy)
		// but — since SetClientTools runs below, after this succeeds — has
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
	// before. SetClientTools may propagate this update to other observers
	// of the DB row (deployment-specific), so it must run strictly after
	// the write above lands — otherwise a propagated update could be read
	// back before the row it depends on was actually written.
	h.mcpManager.SetClientTools(clientConfig.ID, tools, toolNameMapping)

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

	SendJSON(ctx, map[string]any{
		"status":      "success",
		"message":     fmt.Sprintf("MCP client verified. %d tools discovered. Each user will submit their own header values on first tool use.", len(tools)),
		"tools_count": len(tools),
	})
}

// verifyMCPClientExchange handles
// POST /api/mcp/client/{id}/verify-exchange. No request body: the subject of
// the verification exchange is always the signed-in admin's own
// identity-provider token, stamped on the request context by the auth layer.
//
// Surfaced on token_exchange MCP clients in two situations, mirroring
// verify-headers: clients sitting in pending_verification (declared in
// config.json, or created from a session with no identity token) awaiting
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
	// identity-provider token ("verify as yourself"); an API-key-authenticated
	// request has none and must be retried from an identity-authenticated
	// session.
	subjectToken := bifrost.GetStringFromContext(bifrostCtx, schemas.BifrostContextKeyMCPInboundBearer)
	if subjectToken == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "verification exchanges your own identity-provider token, but this request carries none: sign in with your identity provider and retry")
		return
	}

	adminResponse, err := h.store.OAuthProvider.ExchangeAdminCredential(bifrostCtx, clientConfig, subjectToken)
	if err != nil {
		SendError(ctx, fasthttp.StatusUnprocessableEntity, fmt.Sprintf("Admin credential exchange failed: %v", err))
		return
	}

	tools, toolNameMapping, verifyErr := h.mcpManager.VerifyPerUserOAuthConnection(bifrostCtx, clientConfig, adminResponse.AccessToken)
	if verifyErr != nil {
		SendError(ctx, fasthttp.StatusUnprocessableEntity, fmt.Sprintf("Verification failed: %v", verifyErr))
		return
	}

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
		Headers:                   clientConfig.Headers,
		AllowedExtraHeaders:       clientConfig.AllowedExtraHeaders,
		IsPingAvailable:           clientConfig.IsPingAvailable,
		NeedsSessionStickiness:    clientConfig.NeedsSessionStickiness,
		ToolPricing:               clientConfig.ToolPricing,
		ToolSyncInterval:          int(clientConfig.ToolSyncInterval / time.Second),
		ToolExecutionTimeout:      int(clientConfig.ToolExecutionTimeout / time.Second),
		AllowOnAllVirtualKeys:     clientConfig.AllowOnAllVirtualKeys,
		TokenExchange:             clientConfig.TokenExchange,
		DiscoveredTools:           clientConfig.DiscoveredTools,
		DiscoveredToolNameMapping: clientConfig.DiscoveredToolNameMapping,
		Disabled:                  clientConfig.Disabled,
	}
	if err := h.store.ConfigStore.UpdateMCPClientConfig(ctx, clientConfig.ID, updateReq); err != nil {
		// NOTE: Partial success; the runtime client is activated (Healthy)
		// but — since SetClientTools runs below, after this succeeds — has
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
	// before. SetClientTools may propagate this update to other observers
	// of the DB row (deployment-specific), so it must run strictly after
	// the write above lands — otherwise a propagated update could be read
	// back before the row it depends on was actually written.
	h.mcpManager.SetClientTools(clientConfig.ID, tools, toolNameMapping)

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
func pendingOAuthConfigToRequest(cfg *schemas.OAuth2Config) *OAuthConfigRequest {
	req := &OAuthConfigRequest{
		AuthorizeURL: cfg.AuthorizeURL,
		TokenURL:     cfg.TokenURL,
		Scopes:       cfg.Scopes,
		Resource:     cfg.Resource,
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
	Tools     []schemas.ChatToolFunction `json:"tools"`
	State     schemas.MCPConnectionState `json:"state"`
	VKConfigs []MCPVKConfigResponse      `json:"vk_configs"`
	// NodeStates is the per-instance breakdown behind State when it is
	// schemas.MCPConnectionStateDegraded (instance ID -> that instance's own
	// self-reported state). Only ever populated by a configured
	// MCPClusterStateAggregator; nil in a single-instance deployment.
	NodeStates map[string]string `json:"node_states,omitempty"`
}

// MCPClusterStateAggregator is an optional capability the configured
// MCPManager may additionally implement: given a client and the state its
// own runtime already reports locally, it returns the cluster-aggregated
// view — the same value when every instance agrees, or
// schemas.MCPConnectionStateDegraded plus a per-instance breakdown when they
// don't. A nil aggregated map means "agreement" or "not applicable"; a
// non-nil one is only ever attached to the response when the returned state
// is actually Degraded. Single-instance deployments never implement this,
// so the type assertion in getMCPClientsPaginated simply misses and the
// response is unchanged from today.
type MCPClusterStateAggregator interface {
	AggregateMCPClientState(clientID string, localState schemas.MCPConnectionState) (state schemas.MCPConnectionState, nodeStates map[string]string)
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
	if b, ok, err := parseBoolQueryArg(ctx, "all_virtual_keys"); err != nil {
		SendError(ctx, 400, "Invalid all_virtual_keys parameter: must be a boolean")
		return
	} else if ok {
		params.OnlyAllVirtualKeys = b
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

	SendJSON(ctx, map[string]interface{}{
		"servers":     entries,
		"count":       len(entries),
		"total_count": totalCount,
		"limit":       params.Limit,
		"offset":      params.Offset,
	})
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

	SendJSON(ctx, map[string]any{
		"status":  "success",
		"message": fmt.Sprintf("MCP library sync completed, %d entries synced", count),
	})
}

// getMCPClientsPaginated handles the paginated path for GET /api/mcp/clients.
// states carries the raw connection-state selection (connected/disconnected);
// it is resolved against the live engine here because state is not a DB column.
// projectPerUserAdminCredentialState overlays a response-only needs_reauth
// state onto a per-user client's runtime state when the retained admin
// credential used for periodic tool-list discovery needs repair. The runtime
// manager is never touched: per-user clients keep serving (end-user
// credentials and tool calls keep working); this projection only tells the
// registry UI that an admin should repair the discovery credential.
// adminTokenStatus is the admin OAuth token row's status ("" when no row
// exists), adminCredStatus the admin header credential row's status ("" when
// no row exists). A missing row leaves the state alone: clients verified
// before credential retention existed have no admin row and are healthy.
//
// Applies over both Healthy and Unstable runtime states: needs_reauth is a
// more actionable signal than Unstable (a dead admin credential never
// self-heals — Unstable might, on the next successful check), so it must
// win rather than being masked by a pre-existing Unstable reading. It does
// NOT apply over Disabled/PendingVerification/anything else: those already
// carry a more specific, authoritative meaning of their own (an explicit
// admin action, or a one-time bootstrap that was never completed at all)
// that a stale discovery credential shouldn't override.
func projectPerUserAdminCredentialState(authType schemas.MCPAuthType, runtimeState schemas.MCPConnectionState, adminTokenStatus, adminCredStatus string) schemas.MCPConnectionState {
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
	}
	return runtimeState
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

	// Batch-fetch the retained admin discovery credentials for this page's
	// per-user clients so their registry state can carry the needs_reauth
	// projection (see projectPerUserAdminCredentialState). Best-effort: a
	// batch-read failure only means the projection is skipped and runtime
	// states pass through untouched. Debug, not Error, because this runs on
	// every registry list.
	adminTokenStatusByClientID := make(map[string]string)
	adminCredStatusByClientID := make(map[string]string)
	if h.store.ConfigStore != nil {
		adminTokenClientIDs := make([]string, 0)
		perUserHeaderClientIDs := make([]string, 0)
		for _, c := range dbClients {
			switch schemas.MCPAuthType(c.AuthType) {
			case schemas.MCPAuthTypePerUserOauth, schemas.MCPAuthTypeTokenExchange:
				adminTokenClientIDs = append(adminTokenClientIDs, c.ClientID)
			case schemas.MCPAuthTypePerUserHeaders:
				perUserHeaderClientIDs = append(perUserHeaderClientIDs, c.ClientID)
			}
		}
		if len(adminTokenClientIDs) > 0 {
			if adminTokens, err := h.store.ConfigStore.GetAdminOauthTokensByMCPClientIDs(ctx, adminTokenClientIDs); err == nil {
				for clientID, token := range adminTokens {
					adminTokenStatusByClientID[clientID] = token.Status
				}
			} else {
				logger.Debug("failed to batch-get admin oauth tokens for MCP registry state projection: %v", err)
			}
		}
		if len(perUserHeaderClientIDs) > 0 {
			if adminCreds, err := h.store.ConfigStore.GetAdminMCPPerUserHeaderCredentialsByClientIDs(ctx, perUserHeaderClientIDs); err == nil {
				for clientID, cred := range adminCreds {
					adminCredStatusByClientID[clientID] = cred.Status
				}
			} else {
				logger.Debug("failed to batch-get admin header credentials for MCP registry state projection: %v", err)
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
			IsCodeModeClient:       dbClient.IsCodeModeClient,
			ConnectionType:         schemas.MCPConnectionType(dbClient.ConnectionType),
			ConnectionString:       dbClient.ConnectionString,
			StdioConfig:            dbClient.StdioConfig,
			TLSConfig:              dbClient.TLSConfig,
			AuthType:               schemas.MCPAuthType(dbClient.AuthType),
			OauthConfigID:          dbClient.OauthConfigID,
			ToolsToExecute:         dbClient.ToolsToExecute,
			ToolsToAutoExecute:     dbClient.ToolsToAutoExecute,
			Headers:                dbClient.Headers,
			AllowedExtraHeaders:    dbClient.AllowedExtraHeaders,
			IsPingAvailable:        &isPingAvailable,
			NeedsSessionStickiness: dbClient.NeedsSessionStickiness,
			ToolSyncInterval:       time.Duration(dbClient.ToolSyncInterval) * time.Second,
			ToolExecutionTimeout:   time.Duration(dbClient.ToolExecutionTimeout) * time.Second,
			ToolPricing:            dbClient.ToolPricing,
			AllowOnAllVirtualKeys:  dbClient.AllowOnAllVirtualKeys,
			Disabled:               dbClient.Disabled,
			PerUserHeaderKeys:      dbClient.PerUserHeaderKeys,
			TokenExchange:          dbClient.TokenExchange,
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
		if connectedClient, exists := connectedClientsMap[clientConfig.ID]; exists {
			sortedTools := make([]schemas.ChatToolFunction, len(connectedClient.Tools))
			copy(sortedTools, connectedClient.Tools)
			sort.Slice(sortedTools, func(i, j int) bool {
				return sortedTools[i].Name < sortedTools[j].Name
			})
			resolvedState := projectPerUserAdminCredentialState(
				clientConfig.AuthType,
				connectedClient.State,
				adminTokenStatusByClientID[dbClient.ClientID],
				adminCredStatusByClientID[dbClient.ClientID],
			)
			resp := MCPClientResponse{
				Config:    redactedConfig,
				Tools:     sortedTools,
				State:     resolvedState,
				VKConfigs: vkConfigs,
			}
			if aggregator, ok := h.mcpManager.(MCPClusterStateAggregator); ok {
				if aggState, nodeStates := aggregator.AggregateMCPClientState(clientConfig.ID, resolvedState); aggState == schemas.MCPConnectionStateDegraded {
					resp.State = aggState
					resp.NodeStates = nodeStates
				}
			}
			clients = append(clients, resp)
		} else {
			clients = append(clients, MCPClientResponse{
				Config:    redactedConfig,
				Tools:     []schemas.ChatToolFunction{},
				State:     schemas.MCPConnectionStateError,
				VKConfigs: vkConfigs,
			})
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
	SendJSON(ctx, map[string]any{
		"status":  "success",
		"message": "MCP client reconnected successfully",
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
	OauthConfig *OAuthConfigRequest `json:"oauth_config,omitempty"`
	UserHeaders map[string]string   `json:"user_headers,omitempty"`
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
const (
	maxToolSyncIntervalMinutes = int64(math.MaxInt64) / int64(time.Minute)
	minToolSyncIntervalMinutes = int64(math.MinInt64) / int64(time.Minute)
)

// MCPClientUpdateRequest is the body for PUT /api/mcp/client/{id}.
// All fields are optional — omitting a field retains its existing value (PATCH semantics).
// Immutable fields (connection_type, auth_type, connection_string, stdio_config) are not
// accepted here; they cannot be changed after creation.
type MCPClientUpdateRequest struct {
	Name                   *string                         `json:"name,omitempty"`
	Disabled               *bool                           `json:"disabled,omitempty"`
	AllowOnAllVirtualKeys  *bool                           `json:"allow_on_all_virtual_keys,omitempty"`
	IsCodeModeClient       *bool                           `json:"is_code_mode_client,omitempty"`
	IsPingAvailable        *bool                           `json:"is_ping_available,omitempty"`
	NeedsSessionStickiness *bool                           `json:"needs_session_stickiness,omitempty"`
	ToolSyncInterval       *int                            `json:"tool_sync_interval,omitempty"`
	ToolExecutionTimeout   *int                            `json:"tool_execution_timeout,omitempty"`
	Headers                map[string]schemas.SecretVar    `json:"headers,omitempty"`
	AllowedExtraHeaders    *schemas.WhiteList              `json:"allowed_extra_headers,omitempty"`
	ToolPricing            map[string]float64              `json:"tool_pricing,omitempty"`
	ToolsToExecute         *schemas.WhiteList              `json:"tools_to_execute,omitempty"`
	ToolsToAutoExecute     *schemas.WhiteList              `json:"tools_to_auto_execute,omitempty"`
	PerUserHeaderKeys      *[]string                       `json:"per_user_header_keys,omitempty"`
	TokenExchange          *schemas.MCPTokenExchangeConfig `json:"token_exchange,omitempty"`
	TLSConfig              *schemas.MCPTLSConfig           `json:"tls_config,omitempty"`
	VKConfigs              *[]MCPVKConfigRequest           `json:"vk_configs,omitempty"`
	OauthConfig            *OAuthConfigRequest             `json:"oauth_config,omitempty"`
}

// addMCPClient handles POST /api/mcp/client - Add a new MCP client
func (h *MCPHandler) addMCPClient(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "MCP operations unavailable: config store is disabled")
		return
	}
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.store)
	defer cancel()

	var req MCPClientRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
		return
	}

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
	if err := mcp.ValidateMCPClientName(req.Name); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid client name: %v", err))
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
	resolvedToolExecutionTimeout := time.Duration(req.ToolExecutionTimeout) * time.Second

	// Handle per-user headers: admin declares the required key names (schema)
	// AND supplies a sample set of values inline so the server can verify
	// upstream + discover tools in a single round-trip. Mirrors the per-user
	// OAuth flow exactly — the sample values are used once for verification
	// and discarded (never persisted); each end-user submits their own values
	// later via the inline-401 flow.
	if req.AuthType == string(schemas.MCPAuthTypePerUserHeaders) {
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

		toolSyncInterval := mcp.DefaultConnectionCheckInterval
		if req.ToolSyncInterval != 0 {
			toolSyncInterval = time.Duration(req.ToolSyncInterval) * time.Minute
		} else {
			config, cfgErr := h.store.ConfigStore.GetClientConfig(ctx)
			if cfgErr == nil && config != nil {
				toolSyncInterval = time.Duration(config.MCPToolSyncInterval) * time.Minute
			}
		}

		isPingAvailable := true
		if req.IsPingAvailable != nil {
			isPingAvailable = *req.IsPingAvailable
		}

		schemasConfig := &schemas.MCPClientConfig{
			ID:                     req.ClientID,
			Name:                   req.Name,
			IsCodeModeClient:       req.IsCodeModeClient,
			IsPingAvailable:        &isPingAvailable,
			NeedsSessionStickiness: req.NeedsSessionStickiness,
			ToolSyncInterval:       toolSyncInterval,
			ToolExecutionTimeout:   resolvedToolExecutionTimeout,
			ConnectionType:         schemas.MCPConnectionType(req.ConnectionType),
			ConnectionString:       req.ConnectionString,
			StdioConfig:            req.StdioConfig,
			AuthType:               schemas.MCPAuthTypePerUserHeaders,
			PerUserHeaderKeys:      canonHeaderKeys,
			ToolsToExecute:         req.ToolsToExecute,
			ToolsToAutoExecute:     req.ToolsToAutoExecute,
			ToolPricing:            req.ToolPricing,
			Headers:                req.Headers,
			AllowedExtraHeaders:    req.AllowedExtraHeaders,
			AllowOnAllVirtualKeys:  req.AllowOnAllVirtualKeys,
		}

		// Verify connection and discover tools using the admin's sample
		// header values. Discovered tools land on schemasConfig before we
		// persist so the DB row includes them from the start — same
		// convention as the per-user OAuth branch below. Pass the canon
		// form so the verify path sees the same keys the schema declares.
		tools, toolNameMapping, verifyErr := h.mcpManager.VerifyHeadersConnection(bifrostCtx, schemasConfig, canonUserHeaders)
		if verifyErr != nil {
			SendError(ctx, fasthttp.StatusUnprocessableEntity, fmt.Sprintf("Verification failed: %v", verifyErr))
			return
		}
		schemasConfig.DiscoveredTools = tools
		schemasConfig.DiscoveredToolNameMapping = toolNameMapping

		if err := h.store.ConfigStore.CreateMCPClientConfig(ctx, schemasConfig); err != nil {
			if errors.Is(err, configstore.ErrAlreadyExists) {
				SendError(ctx, fasthttp.StatusConflict, "An MCP client with this name already exists")
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

		SendJSON(ctx, map[string]any{
			"status":  "success",
			"message": fmt.Sprintf("MCP client registered. %d tools discovered. Each user will submit their own headers on first tool use.", len(tools)),
		})
		return
	}

	// Handle token exchange: the caller's identity-provider token is exchanged
	// per request at runtime, so nothing interactive happens at create.
	// Verification + tool discovery run synchronously when an admin credential
	// is available (the client-credentials fallback, or a one-time sample
	// caller token); without either the client is parked in
	// pending_verification for the verify-exchange endpoint.
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
		if !req.TokenExchange.UseIdPCredentials && strings.TrimSpace(req.TokenExchange.ClientID.GetValue()) == "" && !req.TokenExchange.ClientID.IsFromSecret() {
			SendError(ctx, fasthttp.StatusBadRequest, "token_exchange.client_id is required when auth_type is 'token_exchange' and use_idp_credentials is not set")
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

		toolSyncInterval := mcp.DefaultConnectionCheckInterval
		if req.ToolSyncInterval != 0 {
			toolSyncInterval = time.Duration(req.ToolSyncInterval) * time.Minute
		} else {
			config, cfgErr := h.store.ConfigStore.GetClientConfig(ctx)
			if cfgErr == nil && config != nil {
				toolSyncInterval = time.Duration(config.MCPToolSyncInterval) * time.Minute
			}
		}

		isPingAvailable := true
		if req.IsPingAvailable != nil {
			isPingAvailable = *req.IsPingAvailable
		}

		schemasConfig := &schemas.MCPClientConfig{
			ID:                     req.ClientID,
			Name:                   req.Name,
			IsCodeModeClient:       req.IsCodeModeClient,
			IsPingAvailable:        &isPingAvailable,
			NeedsSessionStickiness: req.NeedsSessionStickiness,
			ToolSyncInterval:       toolSyncInterval,
			ToolExecutionTimeout:   resolvedToolExecutionTimeout,
			ConnectionType:         schemas.MCPConnectionType(req.ConnectionType),
			ConnectionString:       req.ConnectionString,
			StdioConfig:            req.StdioConfig,
			TLSConfig:              req.TLSConfig,
			AuthType:               schemas.MCPAuthTypeTokenExchange,
			TokenExchange:          req.TokenExchange,
			ToolsToExecute:         req.ToolsToExecute,
			ToolsToAutoExecute:     req.ToolsToAutoExecute,
			ToolPricing:            req.ToolPricing,
			Headers:                req.Headers,
			AllowedExtraHeaders:    req.AllowedExtraHeaders,
			AllowOnAllVirtualKeys:  req.AllowOnAllVirtualKeys,
		}

		// Resolve an admin credential for synchronous verification + tool
		// discovery, mirroring the other per-user branches: the signed-in
		// admin's own identity-provider token — stamped on the request
		// context by the auth layer — is exchanged exactly like a real
		// caller's would be ("verify as yourself"; there is no manual token
		// input). Without one (e.g. API-key authentication) the client is
		// created in pending_verification for verify-exchange, which the
		// admin must hit from an identity-authenticated session. The full
		// response is retained after activation as the admin discovery
		// credential (see the retention block below).
		var adminResponse *schemas.OAuth2TokenExchangeResponse
		sampleSubjectToken, _ := bifrostCtx.Value(schemas.BifrostContextKeyMCPInboundBearer).(string)
		if sampleSubjectToken != "" {
			response, exchangeErr := h.store.OAuthProvider.ExchangeAdminCredential(bifrostCtx, schemasConfig, sampleSubjectToken)
			if exchangeErr != nil {
				SendError(ctx, fasthttp.StatusUnprocessableEntity, fmt.Sprintf("Admin credential exchange failed: %v", exchangeErr))
				return
			}
			adminResponse = response
			tools, toolNameMapping, verifyErr := h.mcpManager.VerifyPerUserOAuthConnection(bifrostCtx, schemasConfig, response.AccessToken)
			if verifyErr != nil {
				SendError(ctx, fasthttp.StatusUnprocessableEntity, fmt.Sprintf("Verification failed: %v", verifyErr))
				return
			}
			schemasConfig.DiscoveredTools = tools
			schemasConfig.DiscoveredToolNameMapping = toolNameMapping
		}

		if err := h.store.ConfigStore.CreateMCPClientConfig(ctx, schemasConfig); err != nil {
			if errors.Is(err, configstore.ErrAlreadyExists) {
				SendError(ctx, fasthttp.StatusConflict, "An MCP client with this name already exists")
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

		redirectURI := lib.BuildBaseURL(ctx, h.store.GetMCPExternalClientURL()) + "/api/oauth/callback"

		flowInitiation, err := h.oauthHandler.InitiateOAuthFlow(ctx, OAuthInitiationRequest{
			ClientID:        req.OauthConfig.ClientID,
			ClientSecret:    req.OauthConfig.ClientSecret,
			AuthorizeURL:    req.OauthConfig.AuthorizeURL,
			TokenURL:        req.OauthConfig.TokenURL,
			RegistrationURL: req.OauthConfig.RegistrationURL,
			RedirectURI:     redirectURI,
			Scopes:          req.OauthConfig.Scopes,
			ServerURL:       req.ConnectionString.GetValue(),
			Resource:        req.OauthConfig.Resource,
		})
		if err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to initiate OAuth flow: %v", err))
			return
		}

		toolSyncInterval := mcp.DefaultConnectionCheckInterval
		if req.ToolSyncInterval != 0 {
			toolSyncInterval = time.Duration(req.ToolSyncInterval) * time.Minute
		} else {
			config, err := h.store.ConfigStore.GetClientConfig(ctx)
			if err == nil && config != nil {
				toolSyncInterval = time.Duration(config.MCPToolSyncInterval) * time.Minute
			}
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
			ToolSyncInterval:       toolSyncInterval,
			ToolExecutionTimeout:   resolvedToolExecutionTimeout,
			ConnectionType:         schemas.MCPConnectionType(req.ConnectionType),
			ConnectionString:       req.ConnectionString,
			StdioConfig:            req.StdioConfig,
			TLSConfig:              req.TLSConfig,
			AuthType:               schemas.MCPAuthTypePerUserOauth,
			OauthConfigID:          &flowInitiation.OauthConfigID,
			ToolsToExecute:         req.ToolsToExecute,
			ToolsToAutoExecute:     req.ToolsToAutoExecute,
			ToolPricing:            req.ToolPricing,
			Headers:                req.Headers,
			AllowedExtraHeaders:    req.AllowedExtraHeaders,
			AllowOnAllVirtualKeys:  req.AllowOnAllVirtualKeys,
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
			"message":         "Test OAuth configuration: please authorize to verify the setup. This login is only used to verify connectivity and discover available tools — it will not be saved.",
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
		flowInitiation, err := h.runOAuthBootstrap(ctx, req.OauthConfig, req.ConnectionString.GetValue())
		if err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to initiate OAuth flow: %v", err))
			return
		}

		toolSyncInterval := mcp.DefaultConnectionCheckInterval
		if req.ToolSyncInterval != 0 {
			toolSyncInterval = time.Duration(req.ToolSyncInterval) * time.Minute
		} else {
			config, err := h.store.ConfigStore.GetClientConfig(ctx)
			if err != nil {
				SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to get client config: %v", err))
				return
			}
			if config != nil {
				toolSyncInterval = time.Duration(config.MCPToolSyncInterval) * time.Minute
			}
		}

		// Store MCP client config in OAuth provider memory (not in database)
		// It will be stored in database only after OAuth completion
		pendingConfig := schemas.MCPClientConfig{
			ID:                     req.ClientID,
			Name:                   req.Name,
			IsCodeModeClient:       req.IsCodeModeClient,
			IsPingAvailable:        req.IsPingAvailable,
			NeedsSessionStickiness: req.NeedsSessionStickiness,
			ToolSyncInterval:       toolSyncInterval,
			ToolExecutionTimeout:   resolvedToolExecutionTimeout,
			ConnectionType:         schemas.MCPConnectionType(req.ConnectionType),
			ConnectionString:       req.ConnectionString,
			StdioConfig:            req.StdioConfig,
			TLSConfig:              req.TLSConfig,
			AuthType:               schemas.MCPAuthType(req.AuthType),
			OauthConfigID:          &flowInitiation.OauthConfigID,
			ToolsToExecute:         req.ToolsToExecute,
			ToolsToAutoExecute:     req.ToolsToAutoExecute,
			Headers:                req.Headers,
			AllowedExtraHeaders:    req.AllowedExtraHeaders,
			ToolPricing:            req.ToolPricing,
			AllowOnAllVirtualKeys:  req.AllowOnAllVirtualKeys,
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

	toolSyncInterval := mcp.DefaultConnectionCheckInterval
	if req.ToolSyncInterval != 0 {
		toolSyncInterval = time.Duration(req.ToolSyncInterval) * time.Minute
	} else {
		config, err := h.store.ConfigStore.GetClientConfig(ctx)
		if err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to get client config: %v", err))
			return
		}
		if config != nil {
			toolSyncInterval = time.Duration(config.MCPToolSyncInterval) * time.Minute
		}
	}

	// Convert to schemas.MCPClientConfig for runtime bifrost client (without tool_pricing)
	schemasConfig := &schemas.MCPClientConfig{
		ID:                     req.ClientID,
		Name:                   req.Name,
		IsCodeModeClient:       req.IsCodeModeClient,
		ConnectionType:         schemas.MCPConnectionType(req.ConnectionType),
		ConnectionString:       req.ConnectionString,
		StdioConfig:            req.StdioConfig,
		TLSConfig:              req.TLSConfig,
		ToolsToExecute:         req.ToolsToExecute,
		ToolsToAutoExecute:     req.ToolsToAutoExecute,
		Headers:                req.Headers,
		AllowedExtraHeaders:    req.AllowedExtraHeaders,
		AuthType:               schemas.MCPAuthType(req.AuthType),
		OauthConfigID:          req.OauthConfigID,
		IsPingAvailable:        req.IsPingAvailable,
		NeedsSessionStickiness: req.NeedsSessionStickiness,
		ToolSyncInterval:       toolSyncInterval,
		ToolExecutionTimeout:   resolvedToolExecutionTimeout,
		ToolPricing:            req.ToolPricing,
		AllowOnAllVirtualKeys:  req.AllowOnAllVirtualKeys,
	}

	// Creating MCP client config in config store
	if h.store.ConfigStore != nil {
		if err := h.store.ConfigStore.CreateMCPClientConfig(ctx, schemasConfig); err != nil {
			if errors.Is(err, configstore.ErrAlreadyExists) {
				SendError(ctx, fasthttp.StatusConflict, "An MCP client with this name already exists")
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
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid request payload")
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
	if err := validateNeedsSessionStickiness(req.NeedsSessionStickiness, existingConfig.ConnectionType); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error())
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
	existingAllowOnAllVirtualKeys := existingConfig.AllowOnAllVirtualKeys
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
	allowOnAllVKs := existingConfig.AllowOnAllVirtualKeys
	if req.AllowOnAllVirtualKeys != nil {
		allowOnAllVKs = *req.AllowOnAllVirtualKeys
	}
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
	// ToolSyncInterval: keep the existing duration when not provided, otherwise
	// take the request value (minutes, matching the create paths). Both the DB
	// column and the rdb load path use seconds, so we convert at the DB-write
	// boundary below; the in-memory duration is the source of truth here.
	resolvedToolSyncInterval := existingConfig.ToolSyncInterval
	if req.ToolSyncInterval != nil {
		// Reject values that would overflow the minutes->Duration multiply. Without
		// this, a caller echoing back the nanosecond value from a GET response wraps
		// int64 and silently persists a garbage interval of either sign.
		if int64(*req.ToolSyncInterval) > maxToolSyncIntervalMinutes || int64(*req.ToolSyncInterval) < minToolSyncIntervalMinutes {
			SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("tool_sync_interval must be between %d and %d minutes", minToolSyncIntervalMinutes, maxToolSyncIntervalMinutes))
			return
		}
		resolvedToolSyncInterval = time.Duration(*req.ToolSyncInterval) * time.Minute
	}
	resolvedToolExecutionTimeout := existingConfig.ToolExecutionTimeout
	if req.ToolExecutionTimeout != nil {
		if *req.ToolExecutionTimeout < 0 {
			SendError(ctx, fasthttp.StatusBadRequest, "tool_execution_timeout must be >= 0")
			return
		}
		resolvedToolExecutionTimeout = time.Duration(*req.ToolExecutionTimeout) * time.Second
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
	if err := mcp.ValidateMCPClientName(name); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid client name: %v", err))
		return
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
		// The GET response redacts the exchange credentials; a round-tripped
		// redacted value means "keep the stored one", mirroring the header
		// and TLS redacted-merge behavior above.
		if existingConfig.TokenExchange != nil {
			if req.TokenExchange.ClientID.IsRedacted() {
				req.TokenExchange.ClientID = existingConfig.TokenExchange.ClientID
			}
			if req.TokenExchange.ClientSecret.IsRedacted() {
				req.TokenExchange.ClientSecret = existingConfig.TokenExchange.ClientSecret
			}
		}
		if !req.TokenExchange.UseIdPCredentials && strings.TrimSpace(req.TokenExchange.ClientID.GetValue()) == "" && !req.TokenExchange.ClientID.IsFromSecret() {
			SendError(ctx, fasthttp.StatusBadRequest, "token_exchange.client_id must be non-empty unless use_idp_credentials is set")
			return
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
		// pairing across an endpoint change.
		endpointChanged := resolvedOauthFields.AuthorizeURL != existingOauthConfig.AuthorizeURL ||
			resolvedOauthFields.TokenURL != existingOauthConfig.TokenURL
		if endpointChanged && req.OauthConfig.ClientSecret.ShouldPreserveStored() {
			SendError(ctx, fasthttp.StatusBadRequest, "client_secret must be resent explicitly when authorize_url or token_url changes")
			return
		}
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
		StdioConfig:            existingConfig.StdioConfig,
		ToolsToExecute:         resolvedToolsToExecute,
		ToolsToAutoExecute:     resolvedToolsToAutoExecute,
		Headers:                headers,
		AllowedExtraHeaders:    allowedExtraHeaders,
		IsPingAvailable:        isPingAvailable,
		NeedsSessionStickiness: needsSessionStickiness,
		ToolPricing:            toolPricing,
		ToolSyncInterval:       int(resolvedToolSyncInterval / time.Second),
		ToolExecutionTimeout:   int(resolvedToolExecutionTimeout / time.Second),
		AuthType:               string(existingConfig.AuthType),
		OauthConfigID:          existingConfig.OauthConfigID,
		AllowOnAllVirtualKeys:  allowOnAllVKs,
		Disabled:               disabled,
		PerUserHeaderKeys:      perUserHeaderKeys,
		TokenExchange:          tokenExchange,
		TLSConfig:              tlsConfig,
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
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to update mcp client config in store: %v", err))
			return
		}
	}

	toolSyncInterval := resolvedToolSyncInterval
	if toolSyncInterval == 0 {
		toolSyncInterval = mcp.DefaultConnectionCheckInterval
		config, err := h.store.ConfigStore.GetClientConfig(ctx)
		if err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to get client config: %v", err))
			return
		}
		if config != nil {
			toolSyncInterval = time.Duration(config.MCPToolSyncInterval) * time.Minute
		}
	}
	// Build in-memory config from resolved values.
	schemasConfig := &schemas.MCPClientConfig{
		ID:                     id,
		Name:                   name,
		IsCodeModeClient:       isCodeMode,
		ConnectionType:         existingConfig.ConnectionType,
		ConnectionString:       existingConfig.ConnectionString,
		StdioConfig:            existingConfig.StdioConfig,
		TLSConfig:              tlsConfig,
		ToolsToExecute:         resolvedToolsToExecute,
		ToolsToAutoExecute:     resolvedToolsToAutoExecute,
		Headers:                headers,
		AllowedExtraHeaders:    allowedExtraHeaders,
		AuthType:               existingConfig.AuthType,
		OauthConfigID:          existingConfig.OauthConfigID,
		IsPingAvailable:        isPingAvailable,
		NeedsSessionStickiness: needsSessionStickiness,
		ToolSyncInterval:       toolSyncInterval,
		ToolExecutionTimeout:   resolvedToolExecutionTimeout,
		ToolPricing:            toolPricing,
		AllowOnAllVirtualKeys:  allowOnAllVKs,
		Disabled:               disabled,
		PerUserHeaderKeys:      perUserHeaderKeys,
		TokenExchange:          tokenExchange,
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
		if errors.Is(err, mcp.ErrMCPSharedConnectFailedAfterUpdate) {
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
		if err := h.store.ConfigStore.MarkAdminExchangeTokenNeedsReauthByMCPClientID(ctx, id); err != nil {
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
	if h.store.ConfigStore != nil && h.governanceManager != nil {
		assignedVKs, listErr := h.store.ConfigStore.GetVirtualKeyMCPConfigsByMCPClientID(ctx, oldDBConfig.ID)
		if listErr != nil {
			logger.Error(fmt.Sprintf("failed to fetch VK assignments for MCP client %s after update: %v", id, listErr))
		} else {
			for _, av := range assignedVKs {
				if _, err := h.governanceManager.ReloadVirtualKey(ctx, av.VirtualKeyID); err != nil {
					logger.Error(fmt.Sprintf("failed to reload virtual key %s after MCP client update: %v", av.VirtualKeyID, err))
				}
			}
		}
	}

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
	//   2. AllowOnAllVirtualKeys flipped — the implicit fallback toggled,
	//      every VK with a credential for this MCP needs re-evaluation.
	//
	// Reconcile is enterprise-only behavior (no-op in OSS). It orphans
	// credentials whose MCP just lost the grant and reactivates orphaned
	// ones whose MCP regained the grant. Both surfaces (OAuth + headers)
	// are reconciled — they share the same VK→MCP allowlist model.
	if h.store.ConfigStore != nil {
		shouldReconcile := req.VKConfigs != nil || allowOnAllVKs != existingAllowOnAllVirtualKeys
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
	// Delete from DB first to avoid memory/DB inconsistency if DB delete fails
	if h.store.ConfigStore != nil {
		if err := h.store.ConfigStore.DeleteMCPClientConfig(ctx, id); err != nil {
			logger.Error(fmt.Sprintf("Failed to delete MCP client config from database: %v", err))
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to delete MCP config: %v", err))
			return
		}
	}
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

	tools, toolNameMapping, err := h.mcpManager.VerifyPerUserOAuthConnection(bifrostCtx, clientConfig, accessToken)
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
		Headers:                   clientConfig.Headers,
		AllowedExtraHeaders:       clientConfig.AllowedExtraHeaders,
		IsPingAvailable:           clientConfig.IsPingAvailable,
		NeedsSessionStickiness:    clientConfig.NeedsSessionStickiness,
		ToolPricing:               clientConfig.ToolPricing,
		ToolSyncInterval:          int(clientConfig.ToolSyncInterval / time.Second),
		ToolExecutionTimeout:      int(clientConfig.ToolExecutionTimeout / time.Second),
		AllowOnAllVirtualKeys:     clientConfig.AllowOnAllVirtualKeys,
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
	h.mcpManager.SetClientTools(clientConfig.ID, tools, toolNameMapping)

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
				logger.Error(fmt.Sprintf("Failed to reconnect MCP client after reauthorization for client %s: %v", reauthClientConfig.ID, err))
				SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("OAuth credentials refreshed but reconnecting the client failed: %v", err))
				return
			}
			// ErrMCPReconnectNotApplicable (a per-call client — no persistent
			// connection to reconnect) is not an error here: the fresh
			// credential is already what the next per-call dial will use.
			SendJSON(ctx, map[string]any{"status": "success", "message": "MCP client re-authorized and reconnected successfully"})
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
		tools, toolNameMapping, err := h.mcpManager.VerifyPerUserOAuthConnection(bifrostCtx, mcpClientConfig, accessToken)
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

		// Attach discovered tools before persisting so the DB row includes them from the start.
		mcpClientConfig.DiscoveredTools = tools
		mcpClientConfig.DiscoveredToolNameMapping = toolNameMapping

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
				Headers:                   mcpClientConfig.Headers,
				AllowedExtraHeaders:       mcpClientConfig.AllowedExtraHeaders,
				IsPingAvailable:           mcpClientConfig.IsPingAvailable,
				NeedsSessionStickiness:    mcpClientConfig.NeedsSessionStickiness,
				ToolPricing:               mcpClientConfig.ToolPricing,
				ToolSyncInterval:          int(mcpClientConfig.ToolSyncInterval / time.Second),
				ToolExecutionTimeout:      int(mcpClientConfig.ToolExecutionTimeout / time.Second),
				AllowOnAllVirtualKeys:     mcpClientConfig.AllowOnAllVirtualKeys,
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
		h.mcpManager.SetClientTools(mcpClientConfig.ID, tools, toolNameMapping)

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
		SendJSON(ctx, map[string]any{"status": "success", "message": message, "tools_count": len(tools)})
		return
	}

	// Standard server-level OAuth completion
	if isUpdateFlow {
		oldDBConfig := *existingDBConfig
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
			Headers:                   mcpClientConfig.Headers,
			AllowedExtraHeaders:       mcpClientConfig.AllowedExtraHeaders,
			IsPingAvailable:           mcpClientConfig.IsPingAvailable,
			NeedsSessionStickiness:    mcpClientConfig.NeedsSessionStickiness,
			ToolPricing:               mcpClientConfig.ToolPricing,
			ToolSyncInterval:          int(mcpClientConfig.ToolSyncInterval / time.Second),
			AllowOnAllVirtualKeys:     mcpClientConfig.AllowOnAllVirtualKeys,
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

	SendJSON(ctx, map[string]any{
		"status":  "success",
		"message": "MCP library server removed successfully",
	})
}
