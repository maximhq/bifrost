// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains MCP (Model Context Protocol) tool execution handlers.
package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fasthttp/router"
	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
	"gorm.io/gorm"
)

type MCPManager interface {
	AddMCPClient(ctx context.Context, clientConfig *schemas.MCPClientConfig) error
	RemoveMCPClient(ctx context.Context, id string) error
	UpdateMCPClient(ctx context.Context, id string, updatedConfig *schemas.MCPClientConfig) error
	// UpdateMCPClientConnection reconnects an existing MCP client using updated headers
	UpdateMCPClientConnection(ctx context.Context, id string, newConfig *schemas.MCPClientConfig) error
	ReconnectMCPClient(ctx context.Context, id string) error
	DisableMCPClient(ctx context.Context, id string) error
	EnableMCPClient(ctx context.Context, id string) error
	// VerifyPerUserOAuthConnection verifies an MCP server using a temporary access
	// token and discovers available tools. The connection is closed after verification.
	VerifyPerUserOAuthConnection(ctx context.Context, config *schemas.MCPClientConfig, accessToken string) (map[string]schemas.ChatTool, map[string]string, error)
	// SetClientTools updates the tool map for an existing client.
	SetClientTools(clientID string, tools map[string]schemas.ChatTool, toolNameMapping map[string]string)
}

// MCPHandler manages HTTP requests for MCP tool operations
type MCPHandler struct {
	client            *bifrost.Bifrost
	store             *lib.Config
	mcpManager        MCPManager
	governanceManager GovernanceManager
	oauthHandler      *OAuthHandler
}

// NewMCPHandler creates a new MCP handler instance
func NewMCPHandler(mcpManager MCPManager, governanceManager GovernanceManager, client *bifrost.Bifrost, store *lib.Config, oauthHandler *OAuthHandler) *MCPHandler {
	return &MCPHandler{
		client:            client,
		store:             store,
		mcpManager:        mcpManager,
		governanceManager: governanceManager,
		oauthHandler:      oauthHandler,
	}
}

// RegisterRoutes registers all MCP-related routes
func (h *MCPHandler) RegisterRoutes(r *router.Router, middlewares ...schemas.BifrostHTTPMiddleware) {
	r.GET("/api/mcp/clients", lib.ChainMiddlewares(h.getMCPClients, middlewares...))
	r.POST("/api/mcp/client", lib.ChainMiddlewares(h.addMCPClient, middlewares...))
	r.PUT("/api/mcp/client/{id}", lib.ChainMiddlewares(h.updateMCPClient, middlewares...))
	r.DELETE("/api/mcp/client/{id}", lib.ChainMiddlewares(h.deleteMCPClient, middlewares...))
	r.POST("/api/mcp/client/{id}/reconnect", lib.ChainMiddlewares(h.reconnectMCPClient, middlewares...))
	r.POST("/api/mcp/client/{id}/complete-oauth", lib.ChainMiddlewares(h.completeMCPClientOAuth, middlewares...))
}

// MCPVKConfigResponse is a VK assignment enriched with the VK's display name.
type MCPVKConfigResponse struct {
	VirtualKeyID   string            `json:"virtual_key_id"`
	VirtualKeyName string            `json:"virtual_key_name"`
	ToolsToExecute schemas.WhiteList `json:"tools_to_execute"`
}

// MCPClientResponse represents the response structure for MCP clients
type MCPClientResponse struct {
	Config    *schemas.MCPClientConfig   `json:"config"`
	Tools     []schemas.ChatToolFunction `json:"tools"`
	State     schemas.MCPConnectionState `json:"state"`
	VKConfigs []MCPVKConfigResponse      `json:"vk_configs"`
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

	// Check if pagination params are present — if so, use paginated DB path
	limitStr := string(ctx.QueryArgs().Peek("limit"))
	offsetStr := string(ctx.QueryArgs().Peek("offset"))
	searchStr := string(ctx.QueryArgs().Peek("search"))

	if limitStr != "" || offsetStr != "" || searchStr != "" {
		h.getMCPClientsPaginated(ctx, limitStr, offsetStr, searchStr)
		return
	}

	// Non-paginated path: read from in-memory config
	configsInStore := h.store.MCPConfig
	if configsInStore == nil {
		SendJSON(ctx, emptyResponse)
		return
	}
	// Get actual connected clients from Bifrost
	clientsInBifrost, err := h.client.GetMCPClients()
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get MCP clients from Bifrost: %v", err))
		return
	}
	// Create a map of connected clients for quick lookup
	connectedClientsMap := make(map[string]schemas.MCPClient)
	for _, client := range clientsInBifrost {
		connectedClientsMap[client.Config.ID] = client
	}

	// Build VK id→name lookup from in-memory governance data
	vkNameByID := make(map[string]string)
	if h.governanceManager != nil {
		if gd := h.governanceManager.GetGovernanceData(ctx); gd != nil {
			for _, vk := range gd.VirtualKeys {
				vkNameByID[vk.ID] = vk.Name
			}
		}
	}

	// Batch-fetch all VK assignments for these clients in a single query
	assignmentsByClientStringID := make(map[string][]configstoreTables.TableVirtualKeyMCPConfig)
	if h.store.ConfigStore != nil {
		clientIDs := make([]string, 0, len(configsInStore.ClientConfigs))
		for _, c := range configsInStore.ClientConfigs {
			clientIDs = append(clientIDs, c.ID)
		}
		allAssignments, err := h.store.ConfigStore.GetVirtualKeyMCPConfigsByMCPClientStringIDs(ctx, clientIDs)
		if err != nil {
			logger.Error("failed to fetch VK assignments for MCP clients: %v", err)
			SendError(ctx, fasthttp.StatusInternalServerError, "Failed to retrieve MCP client virtual key assignments")
			return
		}
		for _, a := range allAssignments {
			id := a.MCPClient.ClientID
			assignmentsByClientStringID[id] = append(assignmentsByClientStringID[id], a)
		}
	}

	// Batch-fetch OAuth configs for clients that have one (avoids N+1 queries)
	oauthConfigsByID := make(map[string]*configstoreTables.TableOauthConfig)
	if h.store.ConfigStore != nil {
		oauthIDs := make([]string, 0)
		for _, c := range configsInStore.ClientConfigs {
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

	// Build the final client list, including errored clients
	clients := make([]MCPClientResponse, 0, len(configsInStore.ClientConfigs))

	for _, configClient := range configsInStore.ClientConfigs {
		// Copy to avoid mutating the in-memory store config, then populate OAuth credentials
		configClientCopy := *configClient
		if configClient.OauthConfigID != nil {
			if oauthCfg, ok := oauthConfigsByID[*configClient.OauthConfigID]; ok {
				configClientCopy.OauthClientID = oauthCfg.ClientID
				configClientCopy.OauthClientSecret = oauthCfg.GetClientSecretAsEnvVar()
			}
		}
		// Redact sensitive fields before sending to UI
		redactedConfig := h.store.RedactMCPClientConfig(&configClientCopy)

		vkConfigs := []MCPVKConfigResponse{}
		for _, a := range assignmentsByClientStringID[configClient.ID] {
			vkConfigs = append(vkConfigs, MCPVKConfigResponse{
				VirtualKeyID:   a.VirtualKeyID,
				VirtualKeyName: vkNameByID[a.VirtualKeyID],
				ToolsToExecute: a.ToolsToExecute,
			})
		}

		if connectedClient, exists := connectedClientsMap[configClient.ID]; exists {
			// Sort tools alphabetically by name
			sortedTools := make([]schemas.ChatToolFunction, len(connectedClient.Tools))
			copy(sortedTools, connectedClient.Tools)
			sort.Slice(sortedTools, func(i, j int) bool {
				return sortedTools[i].Name < sortedTools[j].Name
			})

			clients = append(clients, MCPClientResponse{
				Config:    redactedConfig,
				Tools:     sortedTools,
				State:     connectedClient.State,
				VKConfigs: vkConfigs,
			})
		} else {
			// Client is in config but not connected, mark as errored
			clients = append(clients, MCPClientResponse{
				Config:    redactedConfig,
				Tools:     []schemas.ChatToolFunction{}, // No tools available since connection failed
				State:     schemas.MCPConnectionStateError,
				VKConfigs: vkConfigs,
			})
		}
	}
	SendJSON(ctx, map[string]interface{}{
		"clients":     clients,
		"count":       len(clients),
		"total_count": len(clients),
		"limit":       len(clients),
		"offset":      0,
	})
}

// getMCPClientsPaginated handles the paginated path for GET /api/mcp/clients
func (h *MCPHandler) getMCPClientsPaginated(ctx *fasthttp.RequestCtx, limitStr, offsetStr, searchStr string) {
	params := configstore.MCPClientsQueryParams{
		Search: searchStr,
	}
	if limitStr != "" {
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
	if offsetStr != "" {
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

	dbClients, totalCount, err := h.store.ConfigStore.GetMCPClientsPaginated(ctx, params)
	if err != nil {
		logger.Error("failed to retrieve MCP clients: %v", err)
		SendError(ctx, 500, "Failed to retrieve MCP clients")
		return
	}

	// Get connected clients from Bifrost engine for state/tools merge
	clientsInBifrost, err := h.client.GetMCPClients()
	if err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to get MCP clients from Bifrost: %v", err))
		return
	}
	connectedClientsMap := make(map[string]schemas.MCPClient)
	for _, client := range clientsInBifrost {
		connectedClientsMap[client.Config.ID] = client
	}

	// Build VK id→name lookup from in-memory governance data (no extra DB queries)
	vkNameByID := make(map[string]string)
	if h.governanceManager != nil {
		if gd := h.governanceManager.GetGovernanceData(ctx); gd != nil {
			for _, vk := range gd.VirtualKeys {
				vkNameByID[vk.ID] = vk.Name
			}
		}
	}

	// Batch-fetch all VK assignments for this page in a single query, then group by client ID.
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

	// Convert DB rows to MCPClientConfig and merge with engine state
	clients := make([]MCPClientResponse, 0, len(dbClients))
	for _, dbClient := range dbClients {
		isPingAvailable := true
		if dbClient.IsPingAvailable != nil {
			isPingAvailable = *dbClient.IsPingAvailable
		}
		clientConfig := &schemas.MCPClientConfig{
			ID:                    dbClient.ClientID,
			Name:                  dbClient.Name,
			IsCodeModeClient:      dbClient.IsCodeModeClient,
			ConnectionType:        schemas.MCPConnectionType(dbClient.ConnectionType),
			ConnectionString:      dbClient.ConnectionString,
			StdioConfig:           dbClient.StdioConfig,
			AuthType:              schemas.MCPAuthType(dbClient.AuthType),
			OauthConfigID:         dbClient.OauthConfigID,
			ToolsToExecute:        dbClient.ToolsToExecute,
			ToolsToAutoExecute:    dbClient.ToolsToAutoExecute,
			Headers:               dbClient.Headers,
			AllowedExtraHeaders:   dbClient.AllowedExtraHeaders,
			IsPingAvailable:       &isPingAvailable,
			ToolSyncInterval:      time.Duration(dbClient.ToolSyncInterval) * time.Second,
			ToolPricing:           dbClient.ToolPricing,
			AllowOnAllVirtualKeys: dbClient.AllowOnAllVirtualKeys,
			Disabled:              dbClient.Disabled,
		}
		// Populate oauth client credentials from pre-fetched batch
		if dbClient.OauthConfigID != nil {
			if oauthCfg, ok := oauthConfigsByID[*dbClient.OauthConfigID]; ok {
				clientConfig.OauthClientID = oauthCfg.ClientID
				clientConfig.OauthClientSecret = oauthCfg.GetClientSecretAsEnvVar()
			}
		}
		// Enrich VK assignments using the pre-fetched batch result (no extra DB call per client)
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
			clients = append(clients, MCPClientResponse{
				Config:    redactedConfig,
				Tools:     sortedTools,
				State:     connectedClient.State,
				VKConfigs: vkConfigs,
			})
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
	ClientID        *schemas.EnvVar `json:"client_id"`
	ClientSecret    *schemas.EnvVar `json:"client_secret"`
	AuthorizeURL    string          `json:"authorize_url"`
	TokenURL        string          `json:"token_url"`
	RegistrationURL string          `json:"registration_url"`
	Scopes          []string        `json:"scopes"`
}

// MCPClientRequest represents the full MCP client creation request with OAuth support
type MCPClientRequest struct {
	configstoreTables.TableMCPClient
	OauthConfig *OAuthConfigRequest `json:"oauth_config,omitempty"`
}

// MCPVKConfigRequest represents a per-VK tool access config for an MCP client
type MCPVKConfigRequest struct {
	VirtualKeyID   string            `json:"virtual_key_id"`
	ToolsToExecute schemas.WhiteList `json:"tools_to_execute"`
}

// MCPClientUpdateRequest wraps TableMCPClient and adds optional VK assignment management, ToolsToExecute and ToolsToAutoExecute
type MCPClientUpdateRequest struct {
	configstoreTables.TableMCPClient
	VKConfigs          *[]MCPVKConfigRequest `json:"vk_configs,omitempty"`
	ToolsToExecute     *schemas.WhiteList    `json:"tools_to_execute,omitempty"`
	ToolsToAutoExecute *schemas.WhiteList    `json:"tools_to_auto_execute,omitempty"`
	OauthConfig        *OAuthConfigRequest   `json:"oauth_config,omitempty"`
}

// addMCPClient handles POST /api/mcp/client - Add a new MCP client
func (h *MCPHandler) addMCPClient(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "MCP operations unavailable: config store is disabled")
		return
	}
	var req MCPClientRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err))
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

	// Handle per-user OAuth: admin does a test OAuth login to verify the configuration.
	// Uses the same pending_oauth pattern as server-level OAuth, but on completion we
	// verify the connection, discover tools, save the client, and discard the admin's token.
	if req.AuthType == "per_user_oauth" {
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
		})
		if err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to initiate OAuth flow: %v", err))
			return
		}

		toolSyncInterval := mcp.DefaultToolSyncInterval
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
			ID:                    req.ClientID,
			Name:                  req.Name,
			IsCodeModeClient:      req.IsCodeModeClient,
			IsPingAvailable:       &isPingAvailable,
			ToolSyncInterval:      toolSyncInterval,
			ConnectionType:        schemas.MCPConnectionType(req.ConnectionType),
			ConnectionString:      req.ConnectionString,
			StdioConfig:           req.StdioConfig,
			AuthType:              schemas.MCPAuthTypePerUserOauth,
			OauthConfigID:         &flowInitiation.OauthConfigID,
			ToolsToExecute:        req.ToolsToExecute,
			ToolsToAutoExecute:    req.ToolsToAutoExecute,
			ToolPricing:           req.ToolPricing,
			Headers:               req.Headers,
			AllowedExtraHeaders:   req.AllowedExtraHeaders,
			AllowOnAllVirtualKeys: req.AllowOnAllVirtualKeys,
		}

		if err := h.oauthHandler.StorePendingMCPClient(flowInitiation.OauthConfigID, pendingConfig); err != nil {
			logger.Error(fmt.Sprintf("[Add MCP Client] Failed to store pending MCP client: %v", err))
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to store pending MCP client: %v", err))
			return
		}

		SendJSON(ctx, map[string]any{
			"status":          "pending_oauth",
			"message":         "Test OAuth configuration: please authorize to verify the setup. This login is only used to verify connectivity and discover available tools — it will not be saved.",
			"oauth_config_id": flowInitiation.OauthConfigID,
			"authorize_url":   flowInitiation.AuthorizeURL,
			"expires_at":      flowInitiation.ExpiresAt,
			"mcp_client_id":   req.ClientID,
		})
		return
	}

	// Check if server-level OAuth flow is needed
	if req.AuthType == "oauth" {
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

		// Build redirect URI - use Bifrost's own callback endpoint
		redirectURI := lib.BuildBaseURL(ctx, h.store.GetMCPExternalClientURL()) + "/api/oauth/callback"

		// Initiate OAuth flow
		// ServerURL comes from ConnectionString (MCP server URL for OAuth discovery)
		// ClientID is optional - will be obtained via dynamic registration if not provided
		flowInitiation, err := h.oauthHandler.InitiateOAuthFlow(ctx, OAuthInitiationRequest{
			ClientID:        req.OauthConfig.ClientID,
			ClientSecret:    req.OauthConfig.ClientSecret,
			AuthorizeURL:    req.OauthConfig.AuthorizeURL,
			TokenURL:        req.OauthConfig.TokenURL,
			RegistrationURL: req.OauthConfig.RegistrationURL,
			RedirectURI:     redirectURI,
			Scopes:          req.OauthConfig.Scopes,
			ServerURL:       req.ConnectionString.GetValue(),
		})
		if err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to initiate OAuth flow: %v", err))
			return
		}

		toolSyncInterval := mcp.DefaultToolSyncInterval
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
			ID:                    req.ClientID,
			Name:                  req.Name,
			IsCodeModeClient:      req.IsCodeModeClient,
			IsPingAvailable:       req.IsPingAvailable,
			ToolSyncInterval:      toolSyncInterval,
			ConnectionType:        schemas.MCPConnectionType(req.ConnectionType),
			ConnectionString:      req.ConnectionString,
			StdioConfig:           req.StdioConfig,
			AuthType:              schemas.MCPAuthType(req.AuthType),
			OauthConfigID:         &flowInitiation.OauthConfigID,
			ToolsToExecute:        req.ToolsToExecute,
			ToolsToAutoExecute:    req.ToolsToAutoExecute,
			Headers:               req.Headers,
			AllowedExtraHeaders:   req.AllowedExtraHeaders,
			ToolPricing:           req.ToolPricing,
			AllowOnAllVirtualKeys: req.AllowOnAllVirtualKeys,
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

	toolSyncInterval := mcp.DefaultToolSyncInterval
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
		ID:                    req.ClientID,
		Name:                  req.Name,
		IsCodeModeClient:      req.IsCodeModeClient,
		ConnectionType:        schemas.MCPConnectionType(req.ConnectionType),
		ConnectionString:      req.ConnectionString,
		StdioConfig:           req.StdioConfig,
		ToolsToExecute:        req.ToolsToExecute,
		ToolsToAutoExecute:    req.ToolsToAutoExecute,
		Headers:               req.Headers,
		AllowedExtraHeaders:   req.AllowedExtraHeaders,
		AuthType:              schemas.MCPAuthType(req.AuthType),
		OauthConfigID:         req.OauthConfigID,
		IsPingAvailable:       req.IsPingAvailable,
		ToolSyncInterval:      toolSyncInterval,
		ToolPricing:           req.ToolPricing,
		AllowOnAllVirtualKeys: req.AllowOnAllVirtualKeys,
	}

	// Creating MCP client config in config store
	if h.store.ConfigStore != nil {
		if err := h.store.ConfigStore.CreateMCPClientConfig(ctx, schemasConfig); err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to create MCP config: %v", err))
			return
		}
	}
	if err := h.mcpManager.AddMCPClient(ctx, schemasConfig); err != nil {
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
	// Accept the full table client config to support tool_pricing, plus optional vk_configs
	var req MCPClientUpdateRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err))
		return
	}
	req.ClientID = id

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
	// connection_type and auth_type and connection string are permanently immutable
	if req.ConnectionType != "" && req.ConnectionType != string(existingConfig.ConnectionType) {
		SendError(ctx, fasthttp.StatusBadRequest, "connection_type cannot be changed for an existing MCP client")
		return
	}
	if req.AuthType != "" && req.AuthType != string(existingConfig.AuthType) {
		SendError(ctx, fasthttp.StatusBadRequest, "auth_type cannot be changed for an existing MCP client")
		return
	}
	if req.ConnectionString != nil && !req.ConnectionString.Equals(existingConfig.ConnectionString) {
		SendError(ctx, fasthttp.StatusBadRequest, "connection_string cannot be changed for an existing MCP client")
		return
	}
	if req.StdioConfig != nil && !stdioConfigsEqual(req.StdioConfig, existingConfig.StdioConfig) {
		SendError(ctx, fasthttp.StatusBadRequest, "stdio_config cannot be changed for an existing MCP client")
		return
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

	// Validate tools_to_execute
	if err := validateToolsToExecute(resolvedToolsToExecute); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid tools_to_execute: %v", err))
		return
	}
	// Validate tools_to_auto_execute
	if err := validateToolsToAutoExecute(resolvedToolsToAutoExecute, resolvedToolsToExecute); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid tools_to_auto_execute: %v", err))
		return
	}
	// Validate client name
	if err := mcp.ValidateMCPClientName(req.Name); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid client name: %v", err))
		return
	}
	if err := validateAllowedExtraHeaders(req.AllowedExtraHeaders); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid allowed_extra_headers: %v", err))
		return
	}

	// OAuth credential rotation is temporarily disabled.
	if req.OauthConfig != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "updating oauth_config is not supported")
		return
	}
	// shouldRotateOAuthConfig := req.OauthConfig != nil && (existingConfig.AuthType == schemas.MCPAuthTypeOauth || existingConfig.AuthType == schemas.MCPAuthTypePerUserOauth)
	// var oauthClientID *schemas.EnvVar
	// var oauthClientSecret *schemas.EnvVar
	// oauthAuthorizeURL := ""
	// oauthTokenURL := ""
	// oauthRegistrationURL := ""
	// oauthScopes := []string{}
	// if req.OauthConfig != nil && !shouldRotateOAuthConfig {
	// 	SendError(ctx, fasthttp.StatusBadRequest, "oauth_config can only be updated for MCP clients using auth_type 'oauth' or 'per_user_oauth'")
	// 	return
	// }
	// if shouldRotateOAuthConfig && req.Disabled {
	// 	SendError(ctx, fasthttp.StatusBadRequest, "oauth credentials cannot be rotated while disabling a client; send these as two separate requests")
	// 	return
	// }
	// if shouldRotateOAuthConfig {
	// 	if req.OauthConfig.ClientID.ShouldPreserveStored() && req.OauthConfig.ClientSecret.ShouldPreserveStored() {
	// 		shouldRotateOAuthConfig = false
	// 	}
	// }
	// if shouldRotateOAuthConfig {
	// 	oauthClientID = req.OauthConfig.ClientID
	// 	oauthClientSecret = req.OauthConfig.ClientSecret
	// 	oauthAuthorizeURL = strings.TrimSpace(req.OauthConfig.AuthorizeURL)
	// 	oauthTokenURL = strings.TrimSpace(req.OauthConfig.TokenURL)
	// 	oauthRegistrationURL = strings.TrimSpace(req.OauthConfig.RegistrationURL)
	// 	oauthScopes = req.OauthConfig.Scopes
	// 	if !oauthClientID.IsSet() && !oauthClientSecret.IsSet() {
	// 		SendError(ctx, fasthttp.StatusBadRequest, "oauth_config.client_id or oauth_config.client_secret is required when updating OAuth credentials")
	// 		return
	// 	}
	// 	var existingOauthConfig *configstoreTables.TableOauthConfig
	// 	if existingConfig.OauthConfigID != nil && *existingConfig.OauthConfigID != "" {
	// 		existingOauthConfig, err = h.store.ConfigStore.GetOauthConfigByID(ctx, *existingConfig.OauthConfigID)
	// 		if err != nil {
	// 			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to get existing OAuth config: %v", err))
	// 			return
	// 		}
	// 		if existingOauthConfig != nil {
	// 			if oauthAuthorizeURL == "" {
	// 				oauthAuthorizeURL = strings.TrimSpace(existingOauthConfig.AuthorizeURL)
	// 			}
	// 			if oauthTokenURL == "" {
	// 				oauthTokenURL = strings.TrimSpace(existingOauthConfig.TokenURL)
	// 			}
	// 			if oauthRegistrationURL == "" && existingOauthConfig.RegistrationURL != nil {
	// 				oauthRegistrationURL = strings.TrimSpace(*existingOauthConfig.RegistrationURL)
	// 			}
	// 			if len(oauthScopes) == 0 && strings.TrimSpace(existingOauthConfig.Scopes) != "" {
	// 				var existingScopes []string
	// 				if err := json.Unmarshal([]byte(existingOauthConfig.Scopes), &existingScopes); err != nil {
	// 					SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to parse existing OAuth scopes: %v", err))
	// 					return
	// 				}
	// 				oauthScopes = existingScopes
	// 			}
	// 		}
	// 	}
	// 	if !oauthClientID.IsSet() || oauthClientID.ShouldPreserveStored() {
	// 		if existingOauthConfig == nil || !existingOauthConfig.ClientID.IsSet() {
	// 			SendError(ctx, fasthttp.StatusBadRequest, "existing OAuth client_id not found; provide oauth_config.client_id")
	// 			return
	// 		}
	// 		oauthClientID = existingOauthConfig.ClientID // preserve env var reference
	// 	}
	// 	if !oauthClientSecret.IsSet() || oauthClientSecret.ShouldPreserveStored() {
	// 		if existingOauthConfig != nil {
	// 			oauthClientSecret = existingOauthConfig.ClientSecret // preserve stored secret
	// 		}
	// 	}
	// 	requiresDiscoveryOrRegistration := !oauthClientID.IsSet() || oauthAuthorizeURL == "" || oauthTokenURL == ""
	// 	if requiresDiscoveryOrRegistration && (existingConfig.ConnectionString == nil || existingConfig.ConnectionString.GetValue() == "") {
	// 		SendError(ctx, fasthttp.StatusBadRequest, "existing connection_string is required when OAuth discovery or dynamic registration is needed")
	// 		return
	// 	}
	// }
	// Merge redacted values - preserve old values if incoming values are redacted and unchanged
	merged := mergeMCPRedactedValues(&req.TableMCPClient, existingConfig, h.store.RedactMCPClientConfig(existingConfig))
	req.TableMCPClient = *merged

	var oldDBConfig *configstoreTables.TableMCPClient
	if h.store.ConfigStore != nil {
		var err error
		oldDBConfig, err = h.store.ConfigStore.GetMCPClientByID(ctx, id)
		if err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to get existing mcp client config: %v", err))
			return
		}
	}

	req.TableMCPClient.ToolsToExecute = resolvedToolsToExecute
	req.TableMCPClient.ToolsToAutoExecute = resolvedToolsToAutoExecute
	dbUpdateRecord := req.TableMCPClient
	if h.store.ConfigStore != nil {
		if err := h.store.ConfigStore.UpdateMCPClientConfig(ctx, id, &dbUpdateRecord); err != nil {
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("failed to update mcp client config in store: %v", err))
			return
		}
	}

	toolSyncInterval := mcp.DefaultToolSyncInterval
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
		ID:                    req.ClientID,
		Name:                  req.Name,
		IsCodeModeClient:      req.IsCodeModeClient,
		ConnectionType:        existingConfig.ConnectionType,
		ConnectionString:      existingConfig.ConnectionString,
		StdioConfig:           existingConfig.StdioConfig,
		ToolsToExecute:        resolvedToolsToExecute,
		ToolsToAutoExecute:    resolvedToolsToAutoExecute,
		Headers:               req.Headers,
		AllowedExtraHeaders:   req.AllowedExtraHeaders,
		AuthType:              existingConfig.AuthType,
		OauthConfigID:         existingConfig.OauthConfigID,
		IsPingAvailable:       req.IsPingAvailable,
		ToolSyncInterval:      toolSyncInterval,
		ToolPricing:           req.ToolPricing,
		AllowOnAllVirtualKeys: req.AllowOnAllVirtualKeys,
		Disabled:              req.Disabled,
	}

	// Update MCP client config in memory (always — applies name/tools/header changes,
	if err := h.mcpManager.UpdateMCPClient(ctx, id, schemasConfig); err != nil {
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

	// if shouldRotateOAuthConfig {
	// 	redirectURI := lib.BuildBaseURL(ctx, h.store.GetMCPExternalClientURL()) + "/api/oauth/callback"
	// 	serverURL := ""
	// 	if existingConfig.ConnectionString != nil {
	// 		serverURL = existingConfig.ConnectionString.GetValue()
	// 	}
	// 	flowInitiation, err := h.oauthHandler.InitiateOAuthFlow(ctx, OAuthInitiationRequest{
	// 		ClientID:        oauthClientID,
	// 		ClientSecret:    oauthClientSecret,
	// 		AuthorizeURL:    oauthAuthorizeURL,
	// 		TokenURL:        oauthTokenURL,
	// 		RegistrationURL: oauthRegistrationURL,
	// 		RedirectURI:     redirectURI,
	// 		Scopes:          oauthScopes,
	// 		ServerURL:       serverURL,
	// 	})
	// 	if err != nil {
	// 		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to initiate OAuth flow: %v", err))
	// 		return
	// 	}
	// 	pendingConfig := *schemasConfig
	// 	pendingConfig.OauthConfigID = &flowInitiation.OauthConfigID
	// 	pendingConfig.Headers = req.Headers
	// 	if err := h.oauthHandler.StorePendingMCPClient(flowInitiation.OauthConfigID, pendingConfig); err != nil {
	// 		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to store pending MCP client update: %v", err))
	// 		return
	// 	}
	// 	SendJSON(ctx, map[string]any{
	// 		"status":          "pending_oauth",
	// 		"message":         "MCP client updated. OAuth re-authorization is required to apply credential rotation.",
	// 		"oauth_config_id": flowInitiation.OauthConfigID,
	// 		"authorize_url":   flowInitiation.AuthorizeURL,
	// 		"expires_at":      flowInitiation.ExpiresAt,
	// 		"mcp_client_id":   req.ClientID,
	// 	})
	// 	return
	// }

	SendJSON(ctx, map[string]any{
		"status":  "success",
		"message": "MCP client edited successfully",
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

// stdioConfigsEqual returns true when a and b represent the same STDIO configuration.
// A nil config is only equal to another nil config.
func stdioConfigsEqual(a, b *schemas.MCPStdioConfig) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Command != b.Command {
		return false
	}
	if len(a.Args) != len(b.Args) || len(a.Envs) != len(b.Envs) {
		return false
	}
	for i, arg := range a.Args {
		if b.Args[i] != arg {
			return false
		}
	}
	for i, env := range a.Envs {
		if b.Envs[i] != env {
			return false
		}
	}
	return true
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

// mergeMCPRedactedValues merges incoming MCP client config with existing config,
// preserving old values when incoming values are redacted and unchanged.
// This follows the same pattern as provider config updates.
func mergeMCPRedactedValues(incoming *configstoreTables.TableMCPClient, oldRaw, oldRedacted *schemas.MCPClientConfig) *configstoreTables.TableMCPClient {
	merged := incoming

	// Handle ConnectionString - if incoming is redacted and equals old redacted, keep old raw value
	if incoming.ConnectionString != nil && oldRaw.ConnectionString != nil && oldRedacted.ConnectionString != nil {
		if incoming.ConnectionString.IsRedacted() && incoming.ConnectionString.Equals(oldRedacted.ConnectionString) {
			merged.ConnectionString = oldRaw.ConnectionString
		}
	}

	// Handle Headers - merge incoming with old, preserving redacted values
	if incoming.Headers != nil {
		incomingHeaders := incoming.Headers
		merged.Headers = make(map[string]schemas.EnvVar, len(incomingHeaders))
		for key, incomingValue := range incomingHeaders {
			if oldRaw.Headers != nil && oldRedacted.Headers != nil {
				if oldRedactedValue, existsInRedacted := oldRedacted.Headers[key]; existsInRedacted {
					if oldRawValue, existsInRaw := oldRaw.Headers[key]; existsInRaw {
						if incomingValue.IsRedacted() && incomingValue.Equals(&oldRedactedValue) {
							merged.Headers[key] = oldRawValue
							continue
						}
					}
				}
			}
			merged.Headers[key] = incomingValue
		}
	} else if oldRaw.Headers != nil {
		merged.Headers = oldRaw.Headers
	}

	// Preserve IsPingAvailable if not explicitly set in incoming request
	// This prevents the zero-value (false) from overwriting the existing DB value
	if incoming.IsPingAvailable == nil {
		merged.IsPingAvailable = oldRaw.IsPingAvailable
	}
	// Preserve AllowedExtraHeaders if not explicitly set in incoming request
	if incoming.AllowedExtraHeaders == nil {
		merged.AllowedExtraHeaders = oldRaw.AllowedExtraHeaders
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

// updateMCPClientConnectionWithRetry calls mcpManager.UpdateMCPClientConnection with a short retry loop.
func (h *MCPHandler) updateMCPClientConnectionWithRetry(ctx context.Context, id string, config *schemas.MCPClientConfig) error {
	const maxAttempts = 3
	const retryDelay = 500 * time.Millisecond

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		lastErr = h.mcpManager.UpdateMCPClientConnection(ctx, id, config)
		if lastErr == nil {
			return nil
		}
		if !strings.Contains(lastErr.Error(), "reconnect") || attempt == maxAttempts {
			return lastErr
		}
		logger.Warn(fmt.Sprintf("[OAuth Complete] UpdateMCPClientConnection attempt %d/%d for client %s blocked by in-flight reconnect; retrying in %s: %v",
			attempt, maxAttempts, id, retryDelay, lastErr))
		time.Sleep(retryDelay)
	}
	return lastErr
}

// completeMCPClientOAuth handles POST /api/mcp/client/{id}/complete-oauth - Complete MCP client creation after OAuth authorization
// The {id} parameter is the oauth_config_id returned from the initial addMCPClient call
func (h *MCPHandler) completeMCPClientOAuth(ctx *fasthttp.RequestCtx) {
	if h.store.ConfigStore == nil {
		SendError(ctx, fasthttp.StatusServiceUnavailable, "MCP operations unavailable: config store is disabled")
		return
	}
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
	if mcpClientConfig == nil {
		SendError(ctx, fasthttp.StatusNotFound, "MCP client not found in pending OAuth clients. The OAuth flow may have expired or already been completed.")
		return
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
		// Always clean up admin's temp token and pending config, even on failure
		defer h.oauthHandler.RevokeToken(ctx, oauthConfigID)
		defer h.oauthHandler.RemovePendingMCPClient(oauthConfigID)

		// Verify connection and discover tools using admin's temp token
		tools, toolNameMapping, err := h.mcpManager.VerifyPerUserOAuthConnection(ctx, mcpClientConfig, accessToken)
		if err != nil {
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
				AuthType:                  string(mcpClientConfig.AuthType),
				OauthConfigID:             mcpClientConfig.OauthConfigID,
				ToolsToExecute:            mcpClientConfig.ToolsToExecute,
				ToolsToAutoExecute:        mcpClientConfig.ToolsToAutoExecute,
				Headers:                   mcpClientConfig.Headers,
				AllowedExtraHeaders:       mcpClientConfig.AllowedExtraHeaders,
				IsPingAvailable:           mcpClientConfig.IsPingAvailable,
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
			if err := h.updateMCPClientWithRetry(ctx, mcpClientConfig.ID, mcpClientConfig); err != nil {
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
					SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to create MCP config: %v", err))
					return
				}
			}

			// Add MCP client to manager (skips connection for per_user_oauth)
			if err := h.mcpManager.AddMCPClient(ctx, mcpClientConfig); err != nil {
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
		if err := h.updateMCPClientConnectionWithRetry(ctx, mcpClientConfig.ID, mcpClientConfig); err != nil {
			if rollbackErr := h.store.ConfigStore.UpdateMCPClientConfig(ctx, mcpClientConfig.ID, &oldDBConfig); rollbackErr != nil {
				logger.Error(fmt.Sprintf("Failed to rollback MCP client DB update: %v. please restart bifrost to keep core and database in sync", rollbackErr))
			}
			logger.Error(fmt.Sprintf("Failed to reconnect MCP client after OAuth DB update for client %s: %v", mcpClientConfig.ID, err))
			SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to reconnect MCP client with updated OAuth credentials: %v", err))
			return
		}
	} else {
		if h.store.ConfigStore != nil {
			if err := h.store.ConfigStore.CreateMCPClientConfig(ctx, mcpClientConfig); err != nil {
				SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to create MCP config: %v", err))
				return
			}
		}

		// Add MCP client to Bifrost and connect
		if err := h.mcpManager.AddMCPClient(ctx, mcpClientConfig); err != nil {
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

	logger.Debug(fmt.Sprintf("[OAuth Complete] MCP client connected successfully: %s", mcpClientConfig.ID))
	message := "MCP client connected successfully with OAuth"
	if isUpdateFlow {
		message = "MCP client OAuth credentials updated successfully"
	}
	SendJSON(ctx, map[string]any{"status": "success", "message": message})
}
