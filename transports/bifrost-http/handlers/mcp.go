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
	// UpdateMCPClientConnection reconnects an existing MCP client using updated headers
	UpdateMCPClientConnection(ctx context.Context, id string, newConfig *schemas.MCPClientConfig) error
	ReconnectMCPClient(ctx context.Context, id string) error
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

	limitStr := string(ctx.QueryArgs().Peek("limit"))
	offsetStr := string(ctx.QueryArgs().Peek("offset"))
	searchStr := string(ctx.QueryArgs().Peek("search"))

	h.getMCPClientsPaginated(ctx, limitStr, offsetStr, searchStr)
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
		if h.store.FrameworkConfig != nil && h.store.FrameworkConfig.Pricing != nil && h.store.FrameworkConfig.Pricing.MCPLibraryURL != nil {
			if u := *h.store.FrameworkConfig.Pricing.MCPLibraryURL; u != "" {
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

// getMCPClientsPaginated handles the paginated path for GET /api/mcp/clients
func (h *MCPHandler) getMCPClientsPaginated(ctx *fasthttp.RequestCtx, limitStr, offsetStr, searchStr string) {
	params := configstore.MCPClientsQueryParams{
		Search: searchStr,
		Limit:  100,
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
			TLSConfig:             dbClient.TLSConfig,
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
			PerUserHeaderKeys:     dbClient.PerUserHeaderKeys,
		}
		// Populate oauth client credentials from pre-fetched batch
		if dbClient.OauthConfigID != nil {
			if oauthCfg, ok := oauthConfigsByID[*dbClient.OauthConfigID]; ok {
				clientConfig.OauthClientID = oauthCfg.ClientID
				clientConfig.OauthClientSecret = oauthCfg.GetClientSecretAsSecretVar()
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
	ClientID        *schemas.SecretVar `json:"client_id"`
	ClientSecret    *schemas.SecretVar `json:"client_secret"`
	AuthorizeURL    string          `json:"authorize_url"`
	TokenURL        string          `json:"token_url"`
	RegistrationURL string          `json:"registration_url"`
	Scopes          []string        `json:"scopes"`
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

// MCPClientUpdateRequest is the body for PUT /api/mcp/client/{id}.
// All fields are optional — omitting a field retains its existing value (PATCH semantics).
// Immutable fields (connection_type, auth_type, connection_string, stdio_config) are not
// accepted here; they cannot be changed after creation.
type MCPClientUpdateRequest struct {
	Name                  *string                   `json:"name,omitempty"`
	Disabled              *bool                     `json:"disabled,omitempty"`
	AllowOnAllVirtualKeys *bool                     `json:"allow_on_all_virtual_keys,omitempty"`
	IsCodeModeClient      *bool                     `json:"is_code_mode_client,omitempty"`
	IsPingAvailable       *bool                     `json:"is_ping_available,omitempty"`
	ToolSyncInterval      *int                      `json:"tool_sync_interval,omitempty"`
	Headers               map[string]schemas.SecretVar `json:"headers,omitempty"`
	AllowedExtraHeaders   *schemas.WhiteList        `json:"allowed_extra_headers,omitempty"`
	ToolPricing           map[string]float64        `json:"tool_pricing,omitempty"`
	ToolsToExecute        *schemas.WhiteList        `json:"tools_to_execute,omitempty"`
	ToolsToAutoExecute    *schemas.WhiteList        `json:"tools_to_auto_execute,omitempty"`
	PerUserHeaderKeys     *[]string                 `json:"per_user_header_keys,omitempty"`
	TLSConfig             *schemas.MCPTLSConfig     `json:"tls_config,omitempty"`
	VKConfigs             *[]MCPVKConfigRequest     `json:"vk_configs,omitempty"`
	OauthConfig           *OAuthConfigRequest       `json:"oauth_config,omitempty"`
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

		toolSyncInterval := mcp.DefaultToolSyncInterval
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
			ID:                    req.ClientID,
			Name:                  req.Name,
			IsCodeModeClient:      req.IsCodeModeClient,
			IsPingAvailable:       &isPingAvailable,
			ToolSyncInterval:      toolSyncInterval,
			ConnectionType:        schemas.MCPConnectionType(req.ConnectionType),
			ConnectionString:      req.ConnectionString,
			StdioConfig:           req.StdioConfig,
			AuthType:              schemas.MCPAuthTypePerUserHeaders,
			PerUserHeaderKeys:     canonHeaderKeys,
			ToolsToExecute:        req.ToolsToExecute,
			ToolsToAutoExecute:    req.ToolsToAutoExecute,
			ToolPricing:           req.ToolPricing,
			Headers:               req.Headers,
			AllowedExtraHeaders:   req.AllowedExtraHeaders,
			AllowOnAllVirtualKeys: req.AllowOnAllVirtualKeys,
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
			TLSConfig:             req.TLSConfig,
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
			TLSConfig:             req.TLSConfig,
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
		TLSConfig:             req.TLSConfig,
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
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err))
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
		resolvedToolSyncInterval = time.Duration(*req.ToolSyncInterval) * time.Minute
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

	// OAuth credential rotation is temporarily disabled.
	if req.OauthConfig != nil {
		SendError(ctx, fasthttp.StatusBadRequest, "updating oauth_config is not supported")
		return
	}
	// shouldRotateOAuthConfig := req.OauthConfig != nil && (existingConfig.AuthType == schemas.MCPAuthTypeOauth || existingConfig.AuthType == schemas.MCPAuthTypePerUserOauth)
	// var oauthClientID *schemas.SecretVar
	// var oauthClientSecret *schemas.SecretVar
	// oauthAuthorizeURL := ""
	// oauthTokenURL := ""
	// oauthRegistrationURL := ""
	// oauthScopes := []string{}
	// if req.OauthConfig != nil && !shouldRotateOAuthConfig {
	// 	SendError(ctx, fasthttp.StatusBadRequest, "oauth_config can only be updated for MCP clients using auth_type 'oauth' or 'per_user_oauth'")
	// 	return
	// }
	// if shouldRotateOAuthConfig && disabled {
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

	// Build the DB update record from all resolved values.
	dbUpdateRecord := configstoreTables.TableMCPClient{
		ClientID:              id,
		Name:                  name,
		IsCodeModeClient:      isCodeMode,
		ConnectionType:        string(existingConfig.ConnectionType),
		ConnectionString:      existingConfig.ConnectionString,
		StdioConfig:           existingConfig.StdioConfig,
		ToolsToExecute:        resolvedToolsToExecute,
		ToolsToAutoExecute:    resolvedToolsToAutoExecute,
		Headers:               headers,
		AllowedExtraHeaders:   allowedExtraHeaders,
		IsPingAvailable:       isPingAvailable,
		ToolPricing:           toolPricing,
		ToolSyncInterval:      int(resolvedToolSyncInterval / time.Second),
		AuthType:              string(existingConfig.AuthType),
		OauthConfigID:         existingConfig.OauthConfigID,
		AllowOnAllVirtualKeys: allowOnAllVKs,
		Disabled:              disabled,
		PerUserHeaderKeys:     perUserHeaderKeys,
		TLSConfig:             tlsConfig,
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
		toolSyncInterval = mcp.DefaultToolSyncInterval
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
		ID:                    id,
		Name:                  name,
		IsCodeModeClient:      isCodeMode,
		ConnectionType:        existingConfig.ConnectionType,
		ConnectionString:      existingConfig.ConnectionString,
		StdioConfig:           existingConfig.StdioConfig,
		TLSConfig:             tlsConfig,
		ToolsToExecute:        resolvedToolsToExecute,
		ToolsToAutoExecute:    resolvedToolsToAutoExecute,
		Headers:               headers,
		AllowedExtraHeaders:   allowedExtraHeaders,
		AuthType:              existingConfig.AuthType,
		OauthConfigID:         existingConfig.OauthConfigID,
		IsPingAvailable:       isPingAvailable,
		ToolSyncInterval:      toolSyncInterval,
		ToolPricing:           toolPricing,
		AllowOnAllVirtualKeys: allowOnAllVKs,
		Disabled:              disabled,
		PerUserHeaderKeys:     perUserHeaderKeys,
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
		}
	}

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
		tools, toolNameMapping, err := h.mcpManager.VerifyPerUserOAuthConnection(bifrostCtx, mcpClientConfig, accessToken)
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
		if err := h.updateMCPClientConnectionWithRetry(bifrostCtx, mcpClientConfig.ID, mcpClientConfig); err != nil {
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
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err))
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
		schemas.MCPAuthTypePerUserOauth, schemas.MCPAuthTypePerUserHeaders:
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
