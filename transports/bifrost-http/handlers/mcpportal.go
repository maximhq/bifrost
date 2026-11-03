package handlers

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// MCPPortalAccessResolver is the inference access resolver, including enterprise
// permit composition. The portal must not reconstruct access from stored VK rows.
type MCPPortalAccessResolver interface {
	ResolveAccess(*schemas.BifrostContext) (schemas.Access, error)
}

type mcpPortalClientLister interface {
	GetMCPClients() ([]schemas.MCPClient, error)
}

type MCPPortalHandler struct {
	store    *lib.Config
	resolver MCPPortalAccessResolver
	clients  mcpPortalClientLister
}

func NewMCPPortalHandler(store *lib.Config, resolver MCPPortalAccessResolver, clients mcpPortalClientLister) *MCPPortalHandler {
	return &MCPPortalHandler{store: store, resolver: resolver, clients: clients}
}

func (h *MCPPortalHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/api/mcp/portal", lib.ChainMiddlewares(h.list, middlewares...))
	r.POST("/api/mcp/portal/{id}/authenticate", lib.ChainMiddlewares(h.authenticate, middlewares...))
}

type mcpPortalIdentity struct {
	Mode schemas.MCPAuthMode `json:"mode"`
	ID   string              `json:"id"`
	Name string              `json:"name"`
}

type mcpPortalServer struct {
	ID                 string                     `json:"id"`
	Name               string                     `json:"name"`
	AuthType           schemas.MCPAuthType        `json:"auth_type"`
	ConnectionState    schemas.MCPConnectionState `json:"connection_state"`
	CredentialStatus   string                     `json:"credential_status"`
	CanConfigure       bool                       `json:"can_configure"`
	RequiredHeaderKeys []string                   `json:"required_header_keys,omitempty"`
}

type mcpPortalResponse struct {
	Identity *mcpPortalIdentity `json:"identity"`
	Servers  []mcpPortalServer  `json:"servers"`
	Count    int                `json:"count"`
}

// resolve always starts with a fresh grant: middleware may already have resolved
// the dashboard caller, whereas an explicitly selected VK is a different scope.
// The original request context is retained for configstore DAC checks.
func (h *MCPPortalHandler) resolve(ctx *fasthttp.RequestCtx, bfCtx *schemas.BifrostContext, vkID string) (*mcpPortalIdentity, schemas.Access, bool) {
	if ctx.QueryArgs().Has("user_id") {
		SendError(ctx, fasthttp.StatusBadRequest, "user_id is not accepted by the MCP portal")
		return nil, nil, false
	}
	bfCtx.SetGrant(grant.New())
	bfCtx.ClearValue(schemas.BifrostContextKeyVirtualKey)
	bfCtx.ClearValue(schemas.BifrostContextKeyGovernanceVirtualKeyID)
	var identity *mcpPortalIdentity
	if vkID != "" {
		if h.store.ConfigStore == nil {
			SendError(ctx, fasthttp.StatusServiceUnavailable, "MCP portal persistence is not configured")
			return nil, nil, false
		}
		vk, err := h.store.ConfigStore.GetVirtualKey(ctx, vkID)
		if err != nil || vk == nil {
			if err == nil || errors.Is(err, configstore.ErrNotFound) {
				SendError(ctx, fasthttp.StatusNotFound, "Virtual key not found")
			} else {
				SendError(ctx, fasthttp.StatusInternalServerError, "Failed to load virtual key")
			}
			return nil, nil, false
		}
		if (vk.IsActive != nil && !*vk.IsActive) || vk.IsExpiredAt(time.Now()) || vk.Value.GetValue() == "" {
			SendError(ctx, fasthttp.StatusForbidden, "Virtual key is inactive, expired, or unavailable")
			return nil, nil, false
		}
		bfCtx.SetValue(schemas.BifrostContextKeyVirtualKey, vk.Value.GetValue())
		lib.SettleIdentity(bfCtx)
		identity = &mcpPortalIdentity{Mode: schemas.MCPAuthModeVK, ID: vk.ID, Name: vk.Name}
	} else {
		// Never derive a person from request headers, query parameters, or a VK's
		// assignee. Only authentication middleware can name the default caller.
		userID := callerUserIDFromCtx(ctx)
		if userID == "" {
			return nil, nil, true
		}
		name, _ := ctx.UserValue(schemas.BifrostContextKeyUserName).(string)
		if name == "" {
			name = userID
		}
		identity = &mcpPortalIdentity{Mode: schemas.MCPAuthModeUser, ID: userID, Name: name}
		lib.SettleIdentity(bfCtx)
	}
	if h.resolver == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "MCP access resolver is not configured")
		return nil, nil, false
	}
	access, err := h.resolver.ResolveAccess(bfCtx)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to resolve MCP access")
		return nil, nil, false
	}
	// Nil access can also mean an unknown credential. Unlike inference's
	// anonymous default-open path, a self-service portal must prove a grant.
	if access == nil {
		SendError(ctx, fasthttp.StatusForbidden, "No MCP access could be resolved for this identity")
		return nil, nil, false
	}
	if identity.Mode == schemas.MCPAuthModeVK {
		bfCtx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyID, identity.ID)
	}
	return identity, access, true
}

func (h *MCPPortalHandler) list(ctx *fasthttp.RequestCtx) {
	bfCtx, cancel := lib.ConvertToBifrostContext(ctx, h.store)
	defer cancel()
	identity, access, ok := h.resolve(ctx, bfCtx, strings.TrimSpace(string(ctx.QueryArgs().Peek("virtual_key_id"))))
	if !ok {
		return
	}
	response := mcpPortalResponse{Identity: identity, Servers: []mcpPortalServer{}}
	if identity == nil {
		SendJSON(ctx, response)
		return
	}
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "MCP portal persistence is not configured")
		return
	}
	config, err := h.store.ConfigStore.GetMCPConfig(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to load MCP servers")
		return
	}
	states := h.runtimeStates()
	if config != nil {
		for _, client := range config.ClientConfigs {
			if client == nil || !access.IsMCPToolAllowed(client.Name+"-*") {
				continue
			}
			state := portalConnectionState(client, states)
			server := mcpPortalServer{ID: client.ID, Name: client.Name, AuthType: client.AuthType, ConnectionState: state, CredentialStatus: "admin_managed", CanConfigure: portalCanConfigure(client, state)}
			if client.AuthType == schemas.MCPAuthTypePerUserHeaders {
				server.RequiredHeaderKeys = append([]string(nil), client.PerUserHeaderKeys...)
			}
			if client.AuthType == schemas.MCPAuthTypePerUserOauth || client.AuthType == schemas.MCPAuthTypePerUserHeaders {
				server.CredentialStatus, err = h.credentialStatus(ctx, identity, client)
				if err != nil {
					SendError(ctx, fasthttp.StatusInternalServerError, "Failed to load MCP credential status")
					return
				}
			}
			response.Servers = append(response.Servers, server)
		}
	}
	sort.Slice(response.Servers, func(i, j int) bool {
		if response.Servers[i].Name == response.Servers[j].Name {
			return response.Servers[i].ID < response.Servers[j].ID
		}
		return response.Servers[i].Name < response.Servers[j].Name
	})
	response.Count = len(response.Servers)
	SendJSON(ctx, response)
}

func (h *MCPPortalHandler) runtimeStates() map[string]schemas.MCPConnectionState {
	states := make(map[string]schemas.MCPConnectionState)
	if h.clients == nil {
		return states
	}
	clients, err := h.clients.GetMCPClients()
	if err != nil {
		return states
	}
	for _, client := range clients {
		if client.Config != nil {
			states[client.Config.ID] = client.State
		}
	}
	return states
}

func portalConnectionState(client *schemas.MCPClientConfig, states map[string]schemas.MCPConnectionState) schemas.MCPConnectionState {
	if client.Disabled {
		return schemas.MCPConnectionStateDisabled
	}
	if state := states[client.ID]; state != "" {
		return state
	}
	return schemas.MCPConnectionStateError
}

func portalCanConfigure(client *schemas.MCPClientConfig, state schemas.MCPConnectionState) bool {
	if client.Disabled || state == schemas.MCPConnectionStateDisabled || state == schemas.MCPConnectionStatePendingVerification || state == schemas.MCPConnectionStateError {
		return false
	}
	switch client.AuthType {
	case schemas.MCPAuthTypePerUserOauth:
		return client.OauthConfigID != nil && *client.OauthConfigID != ""
	case schemas.MCPAuthTypePerUserHeaders:
		return len(client.PerUserHeaderKeys) > 0
	default:
		return false
	}
}

func (h *MCPPortalHandler) credentialStatus(ctx *fasthttp.RequestCtx, identity *mcpPortalIdentity, client *schemas.MCPClientConfig) (string, error) {
	filter := configstore.MCPSessionsFilterParams{AuthModes: []string{string(identity.Mode)}, MCPClientIDs: []string{client.ID}, Identity: identity.ID}
	if client.AuthType == schemas.MCPAuthTypePerUserHeaders {
		rows, err := h.store.ConfigStore.ListMCPPerUserHeaderCredentials(ctx, filter)
		if err != nil {
			return "", err
		}
		for _, row := range rows {
			if !portalMatchesIdentity(identity, row.AuthMode, row.UserID, row.VirtualKeyID) || row.MCPClientID != client.ID {
				continue
			}
			if row.Status != "active" {
				return "needs_update", nil
			}
			headers, err := row.GetHeaders()
			if err != nil {
				return "", err
			}
			for _, key := range client.PerUserHeaderKeys {
				found := false
				for name, value := range headers {
					if strings.EqualFold(name, key) && strings.TrimSpace(value) != "" {
						found = true
						break
					}
				}
				if !found {
					return "needs_update", nil
				}
			}
			return "ready", nil
		}
		return "missing", nil
	}
	rows, err := h.store.ConfigStore.ListOauthUserTokens(ctx, filter)
	if err != nil {
		return "", err
	}
	for _, row := range rows {
		if !portalMatchesIdentity(identity, row.AuthMode, row.UserID, row.VirtualKeyID) || row.MCPClientID != client.ID {
			continue
		}
		if row.Status != "active" || row.AccessToken == "" || (row.ExpiresAt != nil && !time.Now().Before(*row.ExpiresAt) && row.RefreshToken == "") {
			return "needs_reauth", nil
		}
		return "ready", nil
	}
	return "missing", nil
}

func portalMatchesIdentity(identity *mcpPortalIdentity, mode string, userID, vkID *string) bool {
	if mode != string(identity.Mode) {
		return false
	}
	if identity.Mode == schemas.MCPAuthModeUser {
		return userID != nil && *userID == identity.ID
	}
	return vkID != nil && *vkID == identity.ID
}

func (h *MCPPortalHandler) authenticate(ctx *fasthttp.RequestCtx) {
	var request struct {
		VirtualKeyID string `json:"virtual_key_id,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(ctx.PostBody()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid MCP portal authentication request")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid MCP portal authentication request")
		return
	}
	bfCtx, cancel := lib.ConvertToBifrostContext(ctx, h.store)
	defer cancel()
	identity, access, ok := h.resolve(ctx, bfCtx, strings.TrimSpace(request.VirtualKeyID))
	if !ok {
		return
	}
	if identity == nil {
		SendError(ctx, fasthttp.StatusUnauthorized, "A user identity or authorized virtual key is required")
		return
	}
	id, err := getIDFromCtx(ctx)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "Invalid MCP server id")
		return
	}
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "MCP portal persistence is not configured")
		return
	}
	client, err := h.store.ConfigStore.GetMCPClientConfigByID(ctx, id)
	if err != nil || client == nil {
		if err == nil || errors.Is(err, configstore.ErrNotFound) {
			SendError(ctx, fasthttp.StatusNotFound, "MCP server not found")
		} else {
			SendError(ctx, fasthttp.StatusInternalServerError, "Failed to load MCP server")
		}
		return
	}
	if !access.IsMCPToolAllowed(client.Name + "-*") {
		SendError(ctx, fasthttp.StatusForbidden, "Access to this MCP server is not permitted")
		return
	}
	if !portalCanConfigure(client, portalConnectionState(client, h.runtimeStates())) {
		SendError(ctx, fasthttp.StatusConflict, "This MCP server cannot be configured by the current identity")
		return
	}
	baseURL := lib.BuildBaseURL(ctx, h.store.GetMCPExternalClientURL())
	var authURL string
	switch client.AuthType {
	case schemas.MCPAuthTypePerUserOauth:
		if h.store.OAuthProvider == nil {
			SendError(ctx, fasthttp.StatusServiceUnavailable, "OAuth provider is not configured")
			return
		}
		flow, _, err := h.store.OAuthProvider.InitiateUserOAuthFlow(bfCtx, *client.OauthConfigID, client.ID, baseURL+"/api/oauth/callback", identity.Mode)
		if err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, "Failed to initiate MCP authentication")
			return
		}
		authURL = flow.AuthorizeURL
	case schemas.MCPAuthTypePerUserHeaders:
		if h.store.MCPHeadersProvider == nil {
			SendError(ctx, fasthttp.StatusServiceUnavailable, "Per-user headers provider is not configured")
			return
		}
		flow, err := h.store.MCPHeadersProvider.InitiateUserSubmissionFlow(bfCtx, identity.Mode, identity.ID, client.ID, baseURL)
		if err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, "Failed to initiate MCP authentication")
			return
		}
		authURL = flow.FrontendURL
	}
	SendJSON(ctx, map[string]string{"auth_url": authURL})
}
