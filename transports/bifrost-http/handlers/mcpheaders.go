package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fasthttp/router"
	"github.com/google/uuid"
	mcputils "github.com/maximhq/bifrost/core/mcp/utils"
	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/temptoken"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// MCPPerUserHeadersHandler exposes the per-user-headers flow-detail,
// flow-submit, and credential-revoke endpoints. It is the storage-side
// companion to the inline-401 MCPAuthRequiredError surfaced by the
// credstore resolver.
//
// Identity scoping: the flow-bound endpoints read (mode, identity) from
// the flow row itself (created server-side by the resolver), so per-user
// submissions are tied to the same identity that triggered the
// inline-401. Admin-side verification + tool discovery during MCP client
// creation happens in the unified POST /api/mcp/client handler — mirrors
// the per-user OAuth shape.
type MCPPerUserHeadersHandler struct {
	store      *lib.Config
	mcpManager MCPManager
	tempTokens *temptoken.Service // optional — set when a temp-token service is wired; flowSubmit uses it to revoke the bound token on completion
}

// NewMCPPerUserHeadersHandler constructs the handler. tempTokens is optional —
// pass nil if the deployment does not run the temp-token service (the
// auth-page URL will then only be usable from a dashboard-authenticated
// browser session).
func NewMCPPerUserHeadersHandler(mcpManager MCPManager, store *lib.Config, tempTokens *temptoken.Service) *MCPPerUserHeadersHandler {
	return &MCPPerUserHeadersHandler{
		store:      store,
		mcpManager: mcpManager,
		tempTokens: tempTokens,
	}
}

// RegisterRoutes mounts the per-user-headers routes.
//
// Flow-id-bound endpoints mirror the per-user OAuth surface
// (/api/oauth/per-user/flows/{id}): a pending flow row is created when the
// resolver surfaces the inline-401, its ID rides in the auth-page URL as
// ?flow=<id>, and a temp-token (mcp_headers_auth scope) bound to that
// flow ID is appended as a #t=<token> fragment so anonymous browser
// visitors can complete the submission without a dashboard session.
//
// The DELETE-by-credential-ID route lives under /credential/{id} to
// disambiguate from the flow-ID-keyed routes.
func (h *MCPPerUserHeadersHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/api/mcp/per-user-headers/flows/{id}", lib.ChainMiddlewares(h.flowDetail, middlewares...))
	r.PUT("/api/mcp/per-user-headers/flows/{id}", lib.ChainMiddlewares(h.flowSubmit, middlewares...))
	r.DELETE("/api/mcp/per-user-headers/credential/{id}", lib.ChainMiddlewares(h.revoke, middlewares...))
}

// mcpHeadersFlowDetailResponse is the wire shape for
// GET /api/mcp/per-user-headers/flows/{id}. Mirrors mcpFlowDetailResponse on
// the OAuth side: identity columns + MCP client summary + the schema
// (required keys, admin key names) the submission UI needs to render.
type mcpHeadersFlowDetailResponse struct {
	ID                  string             `json:"id"`
	FlowMode            string             `json:"flow_mode"`
	Status              string             `json:"status"`
	MCPClient           *mcpClientSummary  `json:"mcp_client,omitempty"`
	UserID              *string            `json:"user_id,omitempty"`
	User                *userSummary       `json:"user,omitempty"`
	VirtualKey          *virtualKeySummary `json:"virtual_key,omitempty"`
	SessionID           *string            `json:"session_id,omitempty"`
	ExpiresAt           string             `json:"expires_at"`
	CreatedAt           string             `json:"created_at"`
	RequiredHeaderKeys  []string           `json:"required_header_keys"`
	AdminHeaderKeys     []string           `json:"admin_header_keys,omitempty"`
	SubmittedKeys       []string           `json:"submitted_keys,omitempty"` // Names of keys already on the active credential (no values)
	HasActiveCredential bool               `json:"has_active_credential"`
}

// flowDetail returns the pending headers-submission flow row's metadata so
// the auth landing page can render the form. Authorization is via either a
// dashboard session (caller is signed in and DAC-scoped) OR the
// mcp_headers_auth temp token bound to {id} (anonymous browser visitor that
// followed the auth-page URL from a Bifrost API error response).
func (h *MCPPerUserHeadersHandler) flowDetail(ctx *fasthttp.RequestCtx) {
	flowID, ok := ctx.UserValue("id").(string)
	if !ok || strings.TrimSpace(flowID) == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid flow id")
		return
	}
	flow, err := h.store.ConfigStore.GetMCPPerUserHeaderFlowByID(ctx, flowID)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to load flow: %v", err))
		return
	}
	if flow == nil {
		SendError(ctx, fasthttp.StatusNotFound, "Headers submission flow not found")
		return
	}
	if !canAccessUserFlow(flow.FlowMode, flow.UserID, callerUserIDFromCtx(ctx)) {
		SendError(ctx, fasthttp.StatusForbidden, "This submission link is bound to a different user.")
		return
	}

	config, cfgErr := h.loadMCPClientConfig(ctx, flow.MCPClientID)
	if cfgErr != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, cfgErr.Error())
		return
	}

	resp := mcpHeadersFlowDetailResponse{
		ID:                 flow.ID,
		FlowMode:           flow.FlowMode,
		Status:             flow.Status,
		UserID:             flow.UserID,
		ExpiresAt:          flow.ExpiresAt.UTC().Format(rfc3339Nano),
		CreatedAt:          flow.CreatedAt.UTC().Format(rfc3339Nano),
		RequiredHeaderKeys: append([]string(nil), config.PerUserHeaderKeys...),
		AdminHeaderKeys:    headerNamesFromConfig(config),
	}
	if flow.MCPClient != nil {
		resp.MCPClient = &mcpClientSummary{ClientID: flow.MCPClient.ClientID, Name: flow.MCPClient.Name}
	} else {
		resp.MCPClient = &mcpClientSummary{ClientID: flow.MCPClientID}
	}
	if flow.VirtualKey != nil {
		resp.VirtualKey = &virtualKeySummary{ID: flow.VirtualKey.ID, Name: flow.VirtualKey.Name}
	} else if flow.VirtualKeyID != nil {
		resp.VirtualKey = &virtualKeySummary{ID: *flow.VirtualKeyID}
	}
	if flow.User != nil {
		resp.User = &userSummary{ID: flow.User.ID, Name: flow.User.Name}
	}
	if flow.FlowMode == string(schemas.MCPAuthModeSession) && flow.SessionID != "" {
		sid := flow.SessionID
		resp.SessionID = &sid
	}

	// Surface a "this credential already exists" hint so the UI can render an
	// edit affordance instead of a fresh form. Identity is the flow row's own
	// identity column — same convention as OAuth's flowDetail.
	if h.store.MCPHeadersProvider != nil {
		if mode, identity, ok := headersFlowIdentity(flow); ok {
			// GetCredentialByMode returns 'active' and 'needs_update' rows
			// (orphaned is filtered at the store). SubmittedKeys carries the
			// previously-submitted key NAMES — useful regardless of status
			// so the auth-page can render a "Previously submitted" hint even
			// after an admin schema change has flipped the row. HasActive-
			// Credential gates strictly on the row's lifecycle Status: a
			// needs_update row is NOT an active credential and the UI
			// shouldn't show "you're editing your existing credential"
			// copy for it (the user must resubmit, not edit-in-place).
			if cred, lookupErr := h.store.MCPHeadersProvider.GetCredentialByMode(ctx, mode, identity, flow.MCPClientID); lookupErr == nil && cred != nil {
				resp.HasActiveCredential = cred.Status == schemas.MCPHeadersUserCredentialStatusActive
				resp.SubmittedKeys = sortedKeys(cred.Headers)
			}
		}
	}

	SendJSON(ctx, resp)
}

// flowSubmitRequest is the user-supplied set of header values to persist.
type flowSubmitRequest struct {
	Headers map[string]string `json:"headers"`
}

// flowSubmit consumes a pending headers-submission flow row: verifies the
// caller's values against the upstream, upserts the credential keyed by the
// flow row's (mode, identity), then deletes the flow row and the temp token
// bound to it. Mirrors the OAuth callback's "complete the flow" semantics.
func (h *MCPPerUserHeadersHandler) flowSubmit(ctx *fasthttp.RequestCtx) {
	if h.store.MCPHeadersProvider == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "per-user headers credential provider is not configured")
		return
	}
	bifrostCtx, cancel := lib.ConvertToBifrostContext(ctx, h.store)
	defer cancel()

	flowID, ok := ctx.UserValue("id").(string)
	if !ok || strings.TrimSpace(flowID) == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid flow id")
		return
	}
	var req flowSubmitRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err))
		return
	}

	flow, err := h.store.ConfigStore.GetMCPPerUserHeaderFlowByID(ctx, flowID)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to load flow: %v", err))
		return
	}
	if flow == nil {
		SendError(ctx, fasthttp.StatusNotFound, "Headers submission flow not found")
		return
	}
	if !canAccessUserFlow(flow.FlowMode, flow.UserID, callerUserIDFromCtx(ctx)) {
		SendError(ctx, fasthttp.StatusForbidden, "This submission link is bound to a different user.")
		return
	}
	if !flow.ExpiresAt.IsZero() && flow.ExpiresAt.Before(time.Now()) {
		SendError(ctx, fasthttp.StatusGone, "Headers submission flow has expired; restart from the API error link")
		return
	}

	config, cfgErr := h.loadMCPClientConfig(ctx, flow.MCPClientID)
	if cfgErr != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, cfgErr.Error())
		return
	}
	// Canonicalize incoming header keys (lowercase + trim) before comparing
	// against the schema. The schema is already canon by the write-time
	// invariant (see mcputils.CanonicalizeHeaderKey doc), so once we
	// normalize the request side the exact map lookups below "just work"
	// regardless of whether the client UI sent "Authorization" or
	// "authorization". A pre-normalization mismatch would otherwise force
	// users into unnecessary re-submission loops.
	canonHeaders := mcputils.CanonicalizeHeaderMap(req.Headers)
	mergedHeaders := canonHeaders
	if mode, identity, ok := headersFlowIdentity(flow); ok {
		mergedHeaders = mergeExistingPerUserHeaders(ctx, h.store.MCPHeadersProvider, mode, identity, flow.MCPClientID, canonHeaders)
	}
	if missing := missingPerUserHeaderValues(config.PerUserHeaderKeys, mergedHeaders); len(missing) > 0 {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("missing values for required keys: %s", strings.Join(missing, ", ")))
		return
	}

	// Filter to declared keys only — extras get dropped on purpose so a stale
	// UI cannot persist values that would never be sent on the wire. Both
	// the schema and the lookup map are canon at this point, so exact-key
	// lookup is correct.
	filtered := make(map[string]string, len(config.PerUserHeaderKeys))
	for _, key := range config.PerUserHeaderKeys {
		if v, ok := mergedHeaders[key]; ok {
			filtered[key] = v
		}
	}

	if _, _, verifyErr := h.mcpManager.VerifyHeadersConnection(bifrostCtx, config, filtered); verifyErr != nil {
		SendError(ctx, fasthttp.StatusUnprocessableEntity, fmt.Sprintf("Verification failed: %v", verifyErr))
		return
	}

	mode := schemas.MCPAuthMode(flow.FlowMode)
	cred := &schemas.MCPHeadersUserCredential{
		ID:          uuid.New().String(),
		MCPClientID: flow.MCPClientID,
		AuthMode:    mode,
		Headers:     filtered,
		Status:      schemas.MCPHeadersUserCredentialStatusActive,
	}
	switch mode {
	case schemas.MCPAuthModeUser:
		cred.UserID = flow.UserID
	case schemas.MCPAuthModeVK:
		cred.VirtualKeyID = flow.VirtualKeyID
	case schemas.MCPAuthModeSession:
		if flow.SessionID != "" {
			s := flow.SessionID
			cred.SessionID = &s
		}
	default:
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Flow has unknown auth mode: %s", flow.FlowMode))
		return
	}

	if upsertErr := h.store.MCPHeadersProvider.UpsertCredential(ctx, cred); upsertErr != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to store credential: %v", upsertErr))
		return
	}

	// Best-effort: delete the consumed flow row and the temp token bound to
	// its ID. Failure to clean up is not fatal — the sweep worker will
	// collect both on the next pass. We log but don't surface to the caller.
	if delErr := h.store.ConfigStore.DeleteMCPPerUserHeaderFlow(ctx, flow.ID); delErr != nil {
		logger.Warn("[mcp/per-user-headers] failed to delete flow %s after successful submit: %v", flow.ID, delErr)
	}
	if h.tempTokens != nil {
		if _, delErr := h.tempTokens.DeleteByResourceID(ctx, temptoken.MCPHeadersAuthScopeName, flow.ID); delErr != nil {
			logger.Warn("[mcp/per-user-headers] failed to delete temp token for flow %s: %v", flow.ID, delErr)
		}
	}

	SendJSON(ctx, map[string]any{
		"status":        "success",
		"credential_id": cred.ID,
		"updated_at":    cred.UpdatedAt,
	})
}

// revoke deletes a credential row by its primary key. Authorization is
// gated by a scoped GetCredentialByID lookup first: in enterprise the DAC
// scope filters the row out for callers that don't own it (returns nil →
// 404 here), so the unscoped delete that follows can never touch a row the
// caller couldn't see. Mirrors the sessions handler's revoke pattern.
func (h *MCPPerUserHeadersHandler) revoke(ctx *fasthttp.RequestCtx) {
	if h.store.MCPHeadersProvider == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "per-user headers credential provider is not configured")
		return
	}
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "config store is not configured")
		return
	}
	id, ok := ctx.UserValue("id").(string)
	if !ok || strings.TrimSpace(id) == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "id is required")
		return
	}
	cred, err := h.store.ConfigStore.GetMCPPerUserHeaderCredentialByID(ctx, id)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to load credential: %v", err))
		return
	}
	if cred == nil {
		SendError(ctx, fasthttp.StatusNotFound, "credential not found")
		return
	}
	// Drop pending submission flow rows for the same binding BEFORE the
	// credential. A holder of the 15-min temp-token who has the auth-page
	// URL open in another tab can still PUT /flows/{id}; if the submit
	// lands after the credential is gone, the upsert would mint a fresh
	// credential and silently undo the revoke. Mirrors mcp_sessions.go.
	credMode := schemas.MCPAuthMode(cred.AuthMode)
	credIdentity := ""
	switch credMode {
	case schemas.MCPAuthModeUser:
		if cred.UserID != nil {
			credIdentity = *cred.UserID
		}
	case schemas.MCPAuthModeVK:
		if cred.VirtualKeyID != nil {
			credIdentity = *cred.VirtualKeyID
		}
	case schemas.MCPAuthModeSession:
		credIdentity = cred.SessionID
	}
	if credIdentity != "" {
		if err := h.store.ConfigStore.DeleteMCPPerUserHeaderFlowsByModeIdentityAndMCPClient(ctx, credMode, credIdentity, cred.MCPClientID); err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to clear pending submission flows: %v", err))
			return
		}
	}
	if err := h.store.MCPHeadersProvider.DeleteCredential(ctx, cred.ID); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to revoke credential: %v", err))
		return
	}
	ctx.SetStatusCode(fasthttp.StatusNoContent)
}

func headersFlowIdentity(flow *configstoreTables.TableMCPPerUserHeaderFlow) (schemas.MCPAuthMode, string, bool) {
	if flow == nil {
		return "", "", false
	}
	mode := schemas.MCPAuthMode(flow.FlowMode)
	switch mode {
	case schemas.MCPAuthModeUser:
		if flow.UserID != nil && *flow.UserID != "" {
			return mode, *flow.UserID, true
		}
	case schemas.MCPAuthModeVK:
		if flow.VirtualKeyID != nil && *flow.VirtualKeyID != "" {
			return mode, *flow.VirtualKeyID, true
		}
	case schemas.MCPAuthModeSession:
		if flow.SessionID != "" {
			return mode, flow.SessionID, true
		}
	}
	return mode, "", false
}

func mergeExistingPerUserHeaders(
	ctx context.Context,
	provider schemas.MCPHeadersProvider,
	mode schemas.MCPAuthMode,
	identity string,
	mcpClientID string,
	submitted map[string]string,
) map[string]string {
	merged := make(map[string]string, len(submitted))
	if provider != nil && identity != "" {
		if cred, err := provider.GetCredentialByMode(ctx, mode, identity, mcpClientID); err == nil && cred != nil {
			for key, value := range cred.Headers {
				if strings.TrimSpace(value) != "" {
					merged[key] = value
				}
			}
		}
	}
	for key, value := range submitted {
		if strings.TrimSpace(value) != "" {
			merged[key] = value
		}
	}
	return merged
}

// loadMCPClientConfig fetches the MCP client config and verifies it is a
// per-user-headers client. Returns a typed error so the handler can pick the
// right HTTP status.
func (h *MCPPerUserHeadersHandler) loadMCPClientConfig(ctx context.Context, mcpClientID string) (*schemas.MCPClientConfig, error) {
	if h.store.ConfigStore == nil {
		return nil, fmt.Errorf("config store is not configured")
	}
	row, err := h.store.ConfigStore.GetMCPClientConfigByID(ctx, mcpClientID)
	if err != nil {
		return nil, fmt.Errorf("failed to load mcp client: %w", err)
	}
	if row == nil {
		return nil, fmt.Errorf("mcp client %s not found", mcpClientID)
	}
	if row.AuthType != schemas.MCPAuthTypePerUserHeaders {
		return nil, fmt.Errorf("mcp client %s is not configured for per-user headers auth", mcpClientID)
	}
	return row, nil
}

// missingPerUserHeaderValues returns the names of any required key whose
// value is missing or empty in the supplied map.
func missingPerUserHeaderValues(required []string, values map[string]string) []string {
	var missing []string
	for _, key := range required {
		if v, ok := values[key]; !ok || strings.TrimSpace(v) == "" {
			missing = append(missing, key)
		}
	}
	return missing
}

// headerNamesFromConfig returns just the names (no values) of static admin
// headers on the MCP client config. Used by the submission UI to display
// context the user can't edit.
func headerNamesFromConfig(config *schemas.MCPClientConfig) []string {
	if config == nil || len(config.Headers) == 0 {
		return nil
	}
	names := make([]string, 0, len(config.Headers))
	for name := range config.Headers {
		names = append(names, name)
	}
	return names
}

// sortedKeys returns the keys of m in deterministic order (helpful for stable
// UI rendering — Go map iteration order is random).
func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
