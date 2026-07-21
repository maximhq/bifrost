package mcp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/maximhq/bifrost/core/mcp/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// AcquireClientConn returns a live upstream MCP client connection for the
// given client state, along with a release function the caller must invoke
// (typically via defer).
//
// For shared-connection auth types (none, headers, server_oauth) the
// connection is the persistent state.Conn and the release is a no-op — the
// caller MUST NOT close it.
//
// For per-user auth types (per_user_oauth, …) a fresh ephemeral connection
// is opened per call. The opening is wrapped in the connect-plugin gate
// (runConnectWithPluginPipeline) just like AddClient/Reconnect does for
// shared connections — PreConnectionHook plugins observe the admin-configured
// static headers and may mutate them; credstore-resolved auth headers are
// layered on top AFTER the plugin gate, so the bearer token is never
// observable by plugins. Credential-resolution errors (including
// *MCPUserOAuthRequiredError) surface from this method without opening any
// connection.
func (m *MCPManager) AcquireClientConn(ctx *schemas.BifrostContext, state *schemas.MCPClientState) (*client.Client, func(), error) {
	if state == nil || state.ExecutionConfig == nil {
		return nil, nil, fmt.Errorf("client state is required")
	}
	config := state.ExecutionConfig

	if !m.credStore.RequiresPerCallConnection(config) {
		if state.Conn == nil {
			return nil, nil, fmt.Errorf("MCP client %s has no active connection", config.Name)
		}
		return state.Conn, func() {}, nil
	}

	// Per-user: open an ephemeral transport per call, wrapped in the
	// connect-plugin gate for parity with shared-connection setup.
	if config.ConnectionString == nil || config.ConnectionString.GetValue() == "" {
		return nil, nil, fmt.Errorf("connection URL is required for ephemeral MCP execution")
	}
	url := config.ConnectionString.GetValue()

	connectReq := &schemas.BifrostMCPConnectRequest{
		ClientName:       config.Name,
		ConnectionType:   config.ConnectionType,
		AuthType:         config.AuthType,
		ConnectionString: &url,
		Headers:          utils.FlattenHeaders(utils.StaticConfigHeaders(config)),
	}

	// Closure-captured outputs from the op so the caller can CallTool on the
	// live client after the gate returns.
	var tempClient *client.Client
	// MCPAuthRequiredError is wrapped into a generic BifrostError by the
	// pipeline before PostConnectionHook runs, so capture it out-of-band to
	// preserve the typed-error info for the envelope path. Same capture
	// covers both per-user-OAuth (Kind=oauth) and per-user-headers
	// (Kind=headers) surfaces.
	var authRequiredErr *schemas.MCPAuthRequiredError
	start := time.Now()

	_, gateErr := m.runConnectWithPluginPipeline(ctx, connectReq, func(preReq *schemas.BifrostMCPConnectRequest) (*schemas.BifrostMCPConnectResponse, error) {
		// Resolve auth headers AFTER PreConnectionHook ran. Plugins never see
		// the Authorization header — it lives only on the wire transport.
		authHeaders, credErr := m.credStore.ConnectionHeaders(ctx, config)
		if credErr != nil {
			errors.As(credErr, &authRequiredErr)
			return nil, credErr
		}

		// Compose final transport headers: plugin-mutated static base + auth on top.
		finalHeaders := make(map[string]string, len(preReq.Headers)+len(authHeaders))
		maps.Copy(finalHeaders, preReq.Headers)
		for k, vals := range authHeaders {
			if len(vals) > 0 {
				finalHeaders[k] = vals[0]
			}
		}

		targetURL := url
		if preReq.ConnectionString != nil && *preReq.ConnectionString != "" {
			targetURL = *preReq.ConnectionString
		}

		// finalHeaders (statics + per-user auth) are baked on; allowlisted per-request
		// extras are injected via headerFunc so they reach tools/call now that the
		// CallToolRequest.Header path has been centralized onto the transport.
		perUserOpts := []transport.StreamableHTTPCOption{
			transport.WithHTTPHeaders(finalHeaders),
			transport.WithHTTPHeaderFunc(func(reqCtx context.Context) map[string]string {
				return utils.FlattenHeaders(utils.ExtractFilteredExtras(reqCtx, config))
			}),
		}
		perUserTLSClient, tlsErr := m.buildTLSHTTPClient(config.TLSConfig)
		if tlsErr != nil {
			return nil, fmt.Errorf("failed to build TLS HTTP client: %w", tlsErr)
		}
		if perUserTLSClient != nil {
			perUserOpts = append(perUserOpts, transport.WithHTTPBasicClient(perUserTLSClient))
		}
		httpTransport, err := transport.NewStreamableHTTP(targetURL, perUserOpts...)
		if err != nil {
			return nil, fmt.Errorf("failed to create HTTP transport: %w", err)
		}
		tempClient = client.NewClient(httpTransport)
		if err := tempClient.Start(ctx); err != nil {
			return nil, fmt.Errorf("failed to start ephemeral MCP connection: %w", err)
		}

		initRequest := mcp.InitializeRequest{
			Params: mcp.InitializeParams{
				ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
				Capabilities:    mcp.ClientCapabilities{},
				ClientInfo: mcp.Implementation{
					Name:    fmt.Sprintf("Bifrost-%s-user", config.Name),
					Version: "1.0.0",
				},
			},
		}
		// Bound the MCP `initialize` handshake — a stalled upstream (TCP open
		// succeeds but JSON-RPC initialize never returns) would otherwise block
		// the entire tool call until the parent request ctx fires. Mirrors the
		// bound used for shared-connection Initialize in connectToMCPClient.
		initCtx, initCancel := context.WithTimeout(ctx, MCPClientConnectionEstablishTimeout)
		defer initCancel()
		initResult, err := tempClient.Initialize(initCtx, initRequest)
		if err != nil {
			_ = tempClient.Close()
			tempClient = nil
			return nil, fmt.Errorf("failed to initialize ephemeral MCP connection: %w", err)
		}

		// Build the gate response from the captured initialize result so
		// PostConnectionHook plugins observe what the upstream advertised.
		resp := &schemas.BifrostMCPConnectResponse{
			ConnectionInfo: &schemas.MCPClientConnectionInfo{
				Type:          config.ConnectionType,
				ConnectionURL: &targetURL,
			},
			ExtraFields: schemas.BifrostMCPResponseExtraFields{
				Latency: time.Since(start).Milliseconds(),
			},
		}
		if initResult != nil {
			resp.ProtocolVersion = initResult.ProtocolVersion
			resp.ServerInfo = &schemas.MCPServerInfo{
				Name:    initResult.ServerInfo.Name,
				Version: initResult.ServerInfo.Version,
			}
			resp.ServerCapabilities = &schemas.MCPServerCapabilities{
				Tools:     initResult.Capabilities.Tools != nil,
				Resources: initResult.Capabilities.Resources != nil,
				Prompts:   initResult.Capabilities.Prompts != nil,
				Logging:   initResult.Capabilities.Logging != nil,
			}
		}
		return resp, nil
	})

	if gateErr != nil {
		if tempClient != nil {
			_ = tempClient.Close()
		}
		if authRequiredErr != nil {
			return nil, nil, authRequiredErr
		}
		if gateErr.Error != nil {
			return nil, nil, fmt.Errorf("%s", gateErr.Error.Message)
		}
		return nil, nil, fmt.Errorf("ephemeral connection setup failed for %s", config.Name)
	}

	if tempClient == nil {
		// Plugin short-circuited connect with a synthetic success response and
		// no live transport — we have nothing to execute against.
		return nil, nil, fmt.Errorf("ephemeral MCP connection was short-circuited by plugin for %s", config.Name)
	}

	release := func() {
		if err := tempClient.Close(); err != nil {
			m.logger.Warn("%s Failed to close ephemeral client for %s: %v", MCPLogPrefix, config.Name, err)
		}
	}
	return tempClient, release, nil
}

// GetClients returns all MCP clients managed by the manager.
//
// Returns:
//   - []*schemas.MCPClientState: List of all MCP clients
func (m *MCPManager) GetClients() []schemas.MCPClientState {
	m.mu.RLock()
	defer m.mu.RUnlock()

	clients := make([]schemas.MCPClientState, 0, len(m.clientMap))
	for _, client := range m.clientMap {
		snapshot := *client
		if client.ToolMap != nil {
			snapshot.ToolMap = make(map[string]schemas.ChatTool, len(client.ToolMap))
			maps.Copy(snapshot.ToolMap, client.ToolMap)
		}
		clients = append(clients, snapshot)
	}

	return clients
}

// ReconnectClient attempts to reconnect an MCP client if it is disconnected.
// It validates that the client exists and then establishes a new connection using
// the client's existing configuration. Retry logic is handled internally by
// connectToMCPClient (5 retries, 1-30 seconds per step).
//
// Parameters:
//   - id: ID of the client to reconnect
//
// Returns:
//   - error: Any error that occurred during reconnection
func (m *MCPManager) ReconnectClient(id string) error {
	// Acquire per-client reconnect/update guard before reading config snapshot.
	if _, alreadyReconnecting := m.reconnectingClients.LoadOrStore(id, true); alreadyReconnecting {
		return fmt.Errorf("reconnect already in progress for this client")
	}
	defer m.reconnectingClients.Delete(id)

	m.mu.Lock()
	client, ok := m.clientMap[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("client %s not found", id)
	}
	// Per-user auth types do not maintain a persistent upstream connection —
	// auth is resolved per request/user identity, so there's nothing to
	// reconnect.
	if client.ExecutionConfig != nil && m.credStore.RequiresPerCallConnection(client.ExecutionConfig) {
		m.mu.Unlock()
		return fmt.Errorf("per-user auth clients do not maintain a shared upstream connection (each user manages their own auth): %w", schemas.ErrMCPReconnectNotApplicable)
	}
	// Clients awaiting admin OAuth authorization have no token to connect
	// with; a reconnect attempt would only flip them out of
	// pending_verification into an error state and hide the authorization
	// prompt in the UI.
	if client.ExecutionConfig != nil && client.ExecutionConfig.PendingOAuthConfig != nil &&
		(client.ExecutionConfig.AuthType == schemas.MCPAuthTypeOauth || client.ExecutionConfig.AuthType == schemas.MCPAuthTypePerUserOauth) {
		m.mu.Unlock()
		return fmt.Errorf("client is awaiting admin OAuth authorization; complete authorization instead of reconnecting: %w", schemas.ErrMCPReconnectNotApplicable)
	}
	config := client.ExecutionConfig
	m.mu.Unlock()

	// Reconnect using the client's configuration
	// Retry logic is handled internally by connectToMCPClient
	if err := m.connectToMCPClient(m.ctx, config); err != nil {
		return fmt.Errorf("failed to reconnect MCP client %s: %w", id, err)
	}

	return nil
}

// AddClient adds a new MCP client to the manager.
// It validates the client configuration and establishes a connection.
// If connection fails, the client entry is retained in Disconnected state and
// a health monitor is started to automatically reconnect with exponential backoff.
//
// Parameters:
//   - config: MCP client configuration
//
// Returns:
//   - error: Any error that occurred during client addition or connection
//
// AddClient adds a new MCP client using the provided context for
// request-scoped connect hooks. Existing transport lifetimes still use the
// manager context so persistent MCP connections are not tied to the caller.
func (m *MCPManager) AddClient(requestCtx context.Context, config *schemas.MCPClientConfig) error {
	if requestCtx == nil {
		requestCtx = m.ctx
	}
	if err := validateMCPClientConfig(config); err != nil {
		return fmt.Errorf("invalid MCP client configuration: %w", err)
	}

	// Make a copy of the config to use after unlocking
	configCopy := config

	// Check if a client with the same name already exists (GetClientByName has its own lock)
	if client := m.GetClientByName(config.Name); client != nil {
		return fmt.Errorf("MCP client with name '%s' already exists", config.Name)
	}

	m.mu.Lock()

	if _, ok := m.clientMap[config.ID]; ok {
		m.mu.Unlock()
		return fmt.Errorf("client %s already exists", config.Name)
	}

	// Disabled clients get a dormant placeholder — no connection, no workers.
	if config.Disabled {
		clientState := &schemas.MCPClientState{
			Name:            config.Name,
			ExecutionConfig: config,
			State:           schemas.MCPConnectionStateDisabled,
			ToolMap:         make(map[string]schemas.ChatTool),
			ToolNameMapping: make(map[string]string),
			ConnectionInfo:  &schemas.MCPClientConnectionInfo{Type: config.ConnectionType},
		}
		// Persisted tools for per-user auth types survive restarts in ExecutionConfig.
		if m.credStore.RequiresPerCallConnection(config) && len(config.DiscoveredTools) > 0 {
			for toolName, tool := range config.DiscoveredTools {
				clientState.ToolMap[toolName] = tool
			}
			clientState.ToolNameMapping = config.DiscoveredToolNameMapping
			if config.ConnectionString != nil {
				url := config.ConnectionString.GetValue()
				clientState.ConnectionInfo.ConnectionURL = &url
			}
		}
		m.clientMap[config.ID] = clientState
		m.mu.Unlock()
		m.logger.Debug("%s MCP client '%s' registered in disabled state", MCPLogPrefix, config.Name)
		return nil
	}

	// Create placeholder entry
	m.clientMap[config.ID] = &schemas.MCPClientState{
		Name:            config.Name,
		ExecutionConfig: config,
		ToolMap:         make(map[string]schemas.ChatTool),
		ToolNameMapping: make(map[string]string),
		ConnectionInfo: &schemas.MCPClientConnectionInfo{
			Type: config.ConnectionType,
		},
	}

	// Temporarily unlock for the connection attempt
	// This is to avoid deadlocks when the connection attempt is made
	m.mu.Unlock()

	// Per-user auth types: skip persistent connection. Auth is per-request at
	// runtime. The admin verifies the configuration via a sample login before
	// this is called, and tools are populated separately via SetClientTools().
	if m.credStore.RequiresPerCallConnection(configCopy) {
		m.mu.Lock()
		if client, exists := m.clientMap[config.ID]; exists {
			if config.ConnectionString != nil {
				url := config.ConnectionString.GetValue()
				client.ConnectionInfo.ConnectionURL = &url
			}
			// Restore discovered tools from config (persisted in DB across restarts).
			// Applies to every per-call-connection auth type — currently per-user
			// OAuth and per-user headers — since both populate DiscoveredTools at
			// admin-test time and never hold a persistent client.Conn.
			if len(config.DiscoveredTools) > 0 {
				for toolName, tool := range config.DiscoveredTools {
					client.ToolMap[toolName] = tool
				}
				client.ToolNameMapping = config.DiscoveredToolNameMapping
				client.State = schemas.MCPConnectionStateConnected
				m.logger.Debug("%s Per-user (%s) MCP client '%s' restored with %d tools", MCPLogPrefix, config.AuthType, config.Name, len(config.DiscoveredTools))
			} else {
				client.State = schemas.MCPConnectionStatePendingTools
				m.logger.Debug("%s Per-user (%s) MCP client '%s' registered (connection deferred to runtime)", MCPLogPrefix, config.AuthType, config.Name)
			}
		}
		m.mu.Unlock()
		return nil
	}

	// Shared-OAuth clients with PendingOAuthConfig set carry the inline
	// `oauth_config` block from config.json but have no oauth_configs row
	// or token yet. Attempting to connect would fail in ConnectionHeaders
	// → GetAccessToken. Park them in pending_verification until an admin
	// completes the browser OAuth flow via
	// POST /api/mcp/client/{id}/initiate-verification. PendingOAuthConfig
	// is cleared once the OAuth callback marks the linked oauth_configs
	// row authorized; the subsequent reconnect skips this branch and
	// connectToMCPClient runs normally.
	if config.AuthType == schemas.MCPAuthTypeOauth && config.PendingOAuthConfig != nil {
		m.mu.Lock()
		if client, exists := m.clientMap[config.ID]; exists {
			if config.ConnectionString != nil {
				url := config.ConnectionString.GetValue()
				client.ConnectionInfo.ConnectionURL = &url
			}
			client.State = schemas.MCPConnectionStatePendingVerification
		}
		m.mu.Unlock()
		m.logger.Debug("%s Shared-OAuth MCP client '%s' registered in pending_verification (awaiting admin authorization)", MCPLogPrefix, config.Name)
		return nil
	}

	// Connect using the copied config
	if err := m.connectToMCPClient(requestCtx, configCopy); err != nil {
		// Clean up the failed entry — this is a user-initiated action (UI/API),
		// so surface the error cleanly rather than retaining a ghost entry.
		m.mu.Lock()
		delete(m.clientMap, config.ID)
		m.mu.Unlock()
		return fmt.Errorf("failed to connect to MCP client %s: %w", config.Name, err)
	}

	return nil
}

// VerifyPerUserOAuthConnection creates a temporary MCP connection using the
// provided access token to verify the server is reachable and discover available
// tools. The connection is closed after verification. This is used during
// per-user OAuth client setup when the admin does a test login to validate the
// OAuth configuration before saving the MCP client.
//
// Parameters:
//   - config: MCP client configuration (connection URL, name, etc.)
//   - accessToken: temporary OAuth access token from the admin's test login
//
// Returns:
//   - map[string]schemas.ChatTool: discovered tools keyed by prefixed name
//   - map[string]string: tool name mapping (sanitized → original MCP name)
//   - error: any error during verification
func (m *MCPManager) VerifyPerUserOAuthConnection(ctx context.Context, config *schemas.MCPClientConfig, accessToken string) (map[string]schemas.ChatTool, map[string]string, error) {
	if config.ConnectionString == nil || config.ConnectionString.GetValue() == "" {
		return nil, nil, fmt.Errorf("connection URL is required for per-user OAuth verification")
	}

	// Build prepared inputs for the typed connect plugin gate. PreHooks may mutate
	// Headers / ConnectionString — the mutated values are passed to the transport below.
	// Copy non-Authorization headers from config.Headers so verification sees the same
	// tenant/custom headers as the normal connect path. Authorization is re-injected
	// after PreHooks run (the OAuth bearer comes from the access token, not config).
	url := config.ConnectionString.GetValue()
	preparedHeaders := make(map[string]string, len(config.Headers))
	for k, v := range config.Headers {
		if strings.EqualFold(k, "Authorization") {
			continue
		}
		preparedHeaders[k] = v.GetValue()
	}
	connectReq := &schemas.BifrostMCPConnectRequest{
		ClientName:       config.Name,
		ConnectionType:   schemas.MCPConnectionTypeHTTP,
		AuthType:         config.AuthType,
		ConnectionString: &url,
		Headers:          preparedHeaders,
	}

	verifyCtx, cancel := context.WithTimeout(ctx, MCPClientConnectionEstablishTimeout)
	defer cancel()
	gateCtx := schemas.NewBifrostContext(verifyCtx, schemas.NoDeadline)

	var tempClient *client.Client
	defer func() {
		if tempClient != nil {
			tempClient.Close()
		}
	}()
	start := time.Now()

	_, gateErr := m.runConnectWithPluginPipeline(gateCtx, connectReq, func(preReq *schemas.BifrostMCPConnectRequest) (*schemas.BifrostMCPConnectResponse, error) {
		// Use mutated URL/headers
		finalURL := url
		if preReq.ConnectionString != nil {
			finalURL = *preReq.ConnectionString
		}

		// Copy mutated headers and add Authorization AFTER all PreHooks ran. Copying
		// (rather than mutating preReq.Headers in place) avoids leaking the bearer token
		// back into the request that PreHook plugins may still reference.
		finalHeaders := make(map[string]string, len(preReq.Headers)+1)
		maps.Copy(finalHeaders, preReq.Headers)
		finalHeaders["Authorization"] = fmt.Sprintf("Bearer %s", accessToken)

		verifyOpts := []transport.StreamableHTTPCOption{transport.WithHTTPHeaders(finalHeaders)}
		verifyHTTPClient, tlsErr := m.buildTLSHTTPClient(config.TLSConfig)
		if tlsErr != nil {
			return nil, fmt.Errorf("failed to build TLS HTTP client for verification: %w", tlsErr)
		}
		if verifyHTTPClient != nil {
			verifyOpts = append(verifyOpts, transport.WithHTTPBasicClient(verifyHTTPClient))
		}
		httpTransport, hErr := transport.NewStreamableHTTP(finalURL, verifyOpts...)
		if hErr != nil {
			return nil, fmt.Errorf("failed to create HTTP transport for verification: %w", hErr)
		}
		tempClient = client.NewClient(httpTransport)
		if startErr := tempClient.Start(verifyCtx); startErr != nil {
			return nil, fmt.Errorf("failed to start MCP connection for verification: %w", startErr)
		}

		initRequest := mcp.InitializeRequest{
			Params: mcp.InitializeParams{
				ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
				Capabilities:    mcp.ClientCapabilities{},
				ClientInfo: mcp.Implementation{
					Name:    fmt.Sprintf("Bifrost-%s-verify", config.Name),
					Version: "1.0.0",
				},
			},
		}
		initResult, initErr := tempClient.Initialize(verifyCtx, initRequest)
		if initErr != nil {
			return nil, fmt.Errorf("failed to initialize MCP connection for verification: %w", initErr)
		}

		resp := &schemas.BifrostMCPConnectResponse{
			ConnectionInfo: &schemas.MCPClientConnectionInfo{
				Type:          schemas.MCPConnectionTypeHTTP,
				ConnectionURL: &finalURL,
			},
			ExtraFields: schemas.BifrostMCPResponseExtraFields{
				Latency: time.Since(start).Milliseconds(),
			},
		}
		if initResult != nil {
			resp.ProtocolVersion = initResult.ProtocolVersion
			resp.ServerInfo = &schemas.MCPServerInfo{
				Name:    initResult.ServerInfo.Name,
				Version: initResult.ServerInfo.Version,
			}
			resp.ServerCapabilities = &schemas.MCPServerCapabilities{
				Tools:     initResult.Capabilities.Tools != nil,
				Resources: initResult.Capabilities.Resources != nil,
				Prompts:   initResult.Capabilities.Prompts != nil,
				Logging:   initResult.Capabilities.Logging != nil,
			}
		}
		return resp, nil
	})

	if gateErr != nil {
		return nil, nil, fmt.Errorf("failed to verify MCP connection: %s", gateErr.GetErrorString())
	}
	if tempClient == nil {
		// Plugin short-circuited connect with a synthetic success response. We have no live
		// socket to query for tools — surface this as an error since tool discovery is the
		// whole point of OAuth verification.
		return nil, nil, fmt.Errorf("OAuth verification was short-circuited by plugin; cannot discover tools without a live connection")
	}

	// Discover tools through the list_tools plugin gate. PostHook may filter or augment
	// the discovered set.
	tools, toolNameMapping, err := m.runListToolsWithHooks(verifyCtx, tempClient, config.Name)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to discover tools during verification: %w", err)
	}

	m.logger.Info("%s Per-user OAuth verification succeeded for '%s': discovered %d tools", MCPLogPrefix, config.Name, len(tools))
	return tools, toolNameMapping, nil
}

// VerifyHeadersConnection creates a temporary MCP connection using the
// provided user-submitted header values to verify the server is reachable
// and discover available tools. The connection is closed after verification.
//
// Used in two paths:
//   - Admin test flow: admin enters sample values during MCP client creation,
//     this runs an Initialize handshake against the upstream to validate the
//     schema (PerUserHeaderKeys) + discover tools. The discovered tools then
//     persist on the MCPClient row; the sample values are discarded.
//   - User submission flow: an end user submits their own values via the
//     workspace submit URL surfaced inline by MCPAuthRequiredError. The
//     handler runs this before upserting the row so a bad submission returns
//     422 immediately instead of failing on the next tool call.
//
// Parameters:
//   - config: MCP client configuration (connection URL, name, PerUserHeaderKeys, etc.)
//   - userHeaders: caller-supplied header_name → value map (must cover every
//     PerUserHeaderKeys entry; the caller validates that before invoking).
//
// Returns:
//   - map[string]schemas.ChatTool: discovered tools keyed by prefixed name
//   - map[string]string: tool name mapping (sanitized → original MCP name)
//   - error: any error during verification
func (m *MCPManager) VerifyHeadersConnection(ctx context.Context, config *schemas.MCPClientConfig, userHeaders map[string]string) (map[string]schemas.ChatTool, map[string]string, error) {
	if config.ConnectionString == nil || config.ConnectionString.GetValue() == "" {
		return nil, nil, fmt.Errorf("connection URL is required for per-user headers verification")
	}
	if len(userHeaders) == 0 {
		return nil, nil, fmt.Errorf("user headers are required for per-user headers verification")
	}

	// Build prepared inputs for the typed connect plugin gate. Static admin
	// headers (minus Authorization and minus any PerUserHeaderKeys) are
	// plugin-visible; user-supplied credentials are layered AFTER PreHooks
	// run so plugins cannot read or rewrite them. Mirrors
	// VerifyPerUserOAuthConnection's Authorization-injection pattern.
	url := config.ConnectionString.GetValue()
	preparedHeaders := utils.FlattenHeaders(utils.StaticConfigHeaders(config))
	connectReq := &schemas.BifrostMCPConnectRequest{
		ClientName:       config.Name,
		ConnectionType:   schemas.MCPConnectionTypeHTTP,
		AuthType:         config.AuthType,
		ConnectionString: &url,
		Headers:          preparedHeaders,
	}

	verifyCtx, cancel := context.WithTimeout(ctx, MCPClientConnectionEstablishTimeout)
	defer cancel()
	gateCtx := schemas.NewBifrostContext(verifyCtx, schemas.NoDeadline)

	var tempClient *client.Client
	defer func() {
		if tempClient != nil {
			tempClient.Close()
		}
	}()
	start := time.Now()

	_, gateErr := m.runConnectWithPluginPipeline(gateCtx, connectReq, func(preReq *schemas.BifrostMCPConnectRequest) (*schemas.BifrostMCPConnectResponse, error) {
		finalURL := url
		if preReq.ConnectionString != nil {
			finalURL = *preReq.ConnectionString
		}

		// Copy mutated headers, then layer the user's credential values on
		// top. Copying (rather than mutating preReq.Headers in place) avoids
		// leaking the values back into the request that PreHook plugins may
		// still reference.
		finalHeaders := make(map[string]string, len(preReq.Headers)+len(userHeaders))
		maps.Copy(finalHeaders, preReq.Headers)
		for k, v := range userHeaders {
			finalHeaders[k] = v
		}

		headersVerifyOpts := []transport.StreamableHTTPCOption{transport.WithHTTPHeaders(finalHeaders)}
		headersVerifyTLSClient, tlsErr := m.buildTLSHTTPClient(config.TLSConfig)
		if tlsErr != nil {
			return nil, fmt.Errorf("failed to build TLS HTTP client for verification: %w", tlsErr)
		}
		if headersVerifyTLSClient != nil {
			headersVerifyOpts = append(headersVerifyOpts, transport.WithHTTPBasicClient(headersVerifyTLSClient))
		}
		httpTransport, hErr := transport.NewStreamableHTTP(finalURL, headersVerifyOpts...)
		if hErr != nil {
			return nil, fmt.Errorf("failed to create HTTP transport for verification: %w", hErr)
		}
		tempClient = client.NewClient(httpTransport)
		if startErr := tempClient.Start(verifyCtx); startErr != nil {
			return nil, fmt.Errorf("failed to start MCP connection for verification: %w", startErr)
		}

		initRequest := mcp.InitializeRequest{
			Params: mcp.InitializeParams{
				ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
				Capabilities:    mcp.ClientCapabilities{},
				ClientInfo: mcp.Implementation{
					Name:    fmt.Sprintf("Bifrost-%s-verify", config.Name),
					Version: "1.0.0",
				},
			},
		}
		initResult, initErr := tempClient.Initialize(verifyCtx, initRequest)
		if initErr != nil {
			return nil, fmt.Errorf("failed to initialize MCP connection for verification: %w", initErr)
		}

		resp := &schemas.BifrostMCPConnectResponse{
			ConnectionInfo: &schemas.MCPClientConnectionInfo{
				Type:          schemas.MCPConnectionTypeHTTP,
				ConnectionURL: &finalURL,
			},
			ExtraFields: schemas.BifrostMCPResponseExtraFields{
				Latency: time.Since(start).Milliseconds(),
			},
		}
		if initResult != nil {
			resp.ProtocolVersion = initResult.ProtocolVersion
			resp.ServerInfo = &schemas.MCPServerInfo{
				Name:    initResult.ServerInfo.Name,
				Version: initResult.ServerInfo.Version,
			}
			resp.ServerCapabilities = &schemas.MCPServerCapabilities{
				Tools:     initResult.Capabilities.Tools != nil,
				Resources: initResult.Capabilities.Resources != nil,
				Prompts:   initResult.Capabilities.Prompts != nil,
				Logging:   initResult.Capabilities.Logging != nil,
			}
		}
		return resp, nil
	})

	if gateErr != nil {
		return nil, nil, fmt.Errorf("failed to verify MCP connection: %s", gateErr.GetErrorString())
	}
	if tempClient == nil {
		return nil, nil, fmt.Errorf("headers verification was short-circuited by plugin; cannot discover tools without a live connection")
	}

	tools, toolNameMapping, err := m.runListToolsWithHooks(verifyCtx, tempClient, config.Name)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to discover tools during verification: %w", err)
	}

	m.logger.Info("%s Per-user headers verification succeeded for '%s': discovered %d tools", MCPLogPrefix, config.Name, len(tools))
	return tools, toolNameMapping, nil
}

// SetClientTools updates the tool map and name mapping for an existing client.
// This is used to populate tools discovered during per-user OAuth verification,
// where tool discovery happens separately from client creation.
//
// Parameters:
//   - clientID: ID of the client to update
//   - tools: discovered tools keyed by prefixed name
//   - toolNameMapping: mapping from sanitized tool names to original MCP names
func (m *MCPManager) SetClientTools(clientID string, tools map[string]schemas.ChatTool, toolNameMapping map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if client, exists := m.clientMap[clientID]; exists {
		for toolName, tool := range tools {
			client.ToolMap[toolName] = tool
		}
		client.ToolNameMapping = toolNameMapping
		client.State = schemas.MCPConnectionStateConnected
		m.logger.Debug("%s Set %d tools on client '%s'", MCPLogPrefix, len(tools), client.Name)
	}
}

// RemoveClient removes an MCP client from the manager.
// It handles cleanup for all transport types (HTTP, STDIO, SSE).
//
// Parameters:
//   - id: ID of the client to remove
func (m *MCPManager) RemoveClient(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.removeClientUnsafe(id)
}

// removeClientUnsafe removes an MCP client from the manager without acquiring locks.
// This is an internal method that should only be called when the caller already holds
// the appropriate lock. It handles cleanup for all transport types including cancellation
// of SSE contexts and closing of transport connections.
//
// Parameters:
//   - id: ID of the client to remove
//
// Returns:
//   - error: Any error that occurred during client removal
func (m *MCPManager) removeClientUnsafe(id string) error {
	client, ok := m.clientMap[id]
	if !ok {
		return fmt.Errorf("client %s not found", id)
	}
	m.logger.Info("%s Disconnecting MCP server '%s'", MCPLogPrefix, client.ExecutionConfig.Name)
	// Stop health monitoring for this client
	m.healthMonitorManager.StopMonitoring(id)
	m.logger.Debug("%s Stopped health monitoring for MCP server '%s'", MCPLogPrefix, client.ExecutionConfig.Name)
	// Stop tool syncing for this client
	m.toolSyncManager.StopSyncing(id)
	m.logger.Debug("%s Stopped tool syncing for MCP server '%s'", MCPLogPrefix, client.ExecutionConfig.Name)
	// Cancel SSE context if present (required for proper SSE cleanup)
	if client.CancelFunc != nil {
		client.CancelFunc()
		client.CancelFunc = nil
	}
	m.logger.Debug("%s Cancelled SSE context for MCP server '%s'", MCPLogPrefix, client.ExecutionConfig.Name)
	// Close the client transport connection
	// This handles cleanup for all transport types (HTTP, STDIO, SSE)
	if client.Conn != nil {
		if err := client.Conn.Close(); err != nil {
			m.logger.Error("%s Failed to close MCP server '%s': %v", MCPLogPrefix, client.ExecutionConfig.Name, err)
		}
		client.Conn = nil
	}
	m.logger.Debug("%s Closed client transport connection for MCP server '%s'", MCPLogPrefix, client.ExecutionConfig.Name)
	// Clear client tool map
	client.ToolMap = make(map[string]schemas.ChatTool)

	delete(m.clientMap, id)
	return nil
}

// DisableClient shuts down a client's connection, health monitor, and tool syncer
// without removing it from the manager. The client entry is kept in clientMap with
// state MCPConnectionStateDisabled so it can be re-enabled later.
//
// Parameters:
//   - id: ID of the client to disable
//
// Returns:
//   - error: Any error that occurred during disable
func (m *MCPManager) DisableClient(id string) error {
	// The internal in-process client must never be disabled:
	if id == BifrostMCPClientKey {
		return fmt.Errorf("cannot disable internal bifrost client")
	}
	// Use LoadOrStore (not Load) so the check and the sentinel insertion are atomic.
	if _, alreadyInFlight := m.reconnectingClients.LoadOrStore(id, true); alreadyInFlight {
		return fmt.Errorf("reconnect or connection credential update already in progress for MCP client %s", id)
	}
	defer m.reconnectingClients.Delete(id)

	m.mu.Lock()
	defer m.mu.Unlock()

	clientState, ok := m.clientMap[id]
	if !ok {
		return fmt.Errorf("client %s not found", id)
	}
	if clientState.State == schemas.MCPConnectionStateDisabled {
		return fmt.Errorf("client %s is already disabled", clientState.ExecutionConfig.Name)
	}

	m.logger.Debug("%s Disabling MCP client '%s'", MCPLogPrefix, clientState.ExecutionConfig.Name)

	m.healthMonitorManager.StopMonitoring(id)
	m.toolSyncManager.StopSyncing(id)

	if clientState.CancelFunc != nil {
		clientState.CancelFunc()
		clientState.CancelFunc = nil
	}
	if clientState.Conn != nil {
		if err := clientState.Conn.Close(); err != nil {
			m.logger.Error("%s Failed to close connection for MCP client '%s': %v", MCPLogPrefix, clientState.ExecutionConfig.Name, err)
		}
		clientState.Conn = nil
	}

	// Per-user auth clients have no persistent connection — their ToolMap
	// holds tools discovered via the admin verification step that can only
	// be recovered by re-running it. Preserve the ToolMap so re-enabling
	// restores tools immediately.
	if !m.credStore.RequiresPerCallConnection(clientState.ExecutionConfig) {
		clientState.ToolMap = make(map[string]schemas.ChatTool)
		clientState.ToolNameMapping = make(map[string]string)
	}
	clientState.State = schemas.MCPConnectionStateDisabled
	clientState.ExecutionConfig.Disabled = true
	m.logger.Debug("%s MCP client '%s' disabled successfully", MCPLogPrefix, clientState.ExecutionConfig.Name)
	return nil
}

// EnableClient re-enables a previously disabled MCP client by reconnecting it
// and restarting its health monitor and tool syncer.
//
// Parameters:
//   - id: ID of the client to enable
//
// Returns:
//   - error: Any error that occurred during enable or connection
func (m *MCPManager) EnableClient(id string) error {
	m.mu.Lock()
	clientState, ok := m.clientMap[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("client %s not found", id)
	}
	if clientState.State != schemas.MCPConnectionStateDisabled {
		m.mu.Unlock()
		return fmt.Errorf("client %s is not disabled (current state: %s)", clientState.ExecutionConfig.Name, clientState.State)
	}

	clientState.ExecutionConfig.Disabled = false
	configCopy := clientState.ExecutionConfig
	m.mu.Unlock()

	m.logger.Debug("%s Enabling MCP client '%s'", MCPLogPrefix, configCopy.Name)

	// Per-user auth clients have no persistent connection — auth is per-request.
	// Mirror the AddClient early-return path: just restore the runtime state
	// based on whether tools were previously discovered.
	if m.credStore.RequiresPerCallConnection(configCopy) {
		m.mu.Lock()
		if cs, exists := m.clientMap[id]; exists {
			if len(cs.ToolMap) > 0 {
				cs.State = schemas.MCPConnectionStateConnected
			} else {
				cs.State = schemas.MCPConnectionStatePendingTools
			}
		}
		m.mu.Unlock()
		m.logger.Debug("%s Per-user auth MCP client '%s' enabled (no persistent connection)", MCPLogPrefix, configCopy.Name)
		return nil
	}

	// Guard against concurrent reconnects for the same client from any caller
	// (health monitor, manual API call, etc.). LoadOrStore is atomic — whichever
	// caller arrives second gets the "already in progress" error immediately.
	if _, alreadyReconnecting := m.reconnectingClients.LoadOrStore(id, true); alreadyReconnecting {
		return fmt.Errorf("reconnect already in progress for this client")
	}
	defer m.reconnectingClients.Delete(id)

	if err := m.connectToMCPClient(m.ctx, configCopy); err != nil {
		// Connection failed — leave the entry as Disconnected so the health monitor can
		// recover it, but only if the client has not been disabled in the meantime.
		m.mu.Lock()
		alreadyDisabled := false
		if cs, exists := m.clientMap[id]; exists {
			if cs.State == schemas.MCPConnectionStateDisabled {
				alreadyDisabled = true
			} else {
				cs.State = schemas.MCPConnectionStateDisconnected
			}
		}
		m.mu.Unlock()

		if !alreadyDisabled {
			isPingAvailable := true
			if configCopy.IsPingAvailable != nil {
				isPingAvailable = *configCopy.IsPingAvailable
			}
			monitor := NewClientHealthMonitor(m, id, DefaultHealthCheckInterval, isPingAvailable, m.logger)
			m.healthMonitorManager.StartMonitoring(monitor)
		}

		return fmt.Errorf("failed to connect MCP client '%s': %w", configCopy.Name, err)
	}

	m.logger.Debug("%s MCP client '%s' enabled successfully", MCPLogPrefix, configCopy.Name)
	return nil
}

// UpdateClient updates an existing MCP client's configuration and refreshes its tool list.
// It updates the client's execution config with new settings and retrieves updated tools
// from the MCP server if the client is connected.
// This method does not refresh the client's tool list.
// To refresh the client's tool list, use the ReconnectClient method.
//
// Parameters:
//   - id: ID of the client to edit
//   - updatedConfig: Updated client configuration with new settings
//
// Returns:
//   - error: Any error that occurred during client update or tool retrieval
func (m *MCPManager) UpdateClient(id string, updatedConfig *schemas.MCPClientConfig) error {
	if _, alreadyInFlight := m.reconnectingClients.LoadOrStore(id, true); alreadyInFlight {
		return fmt.Errorf("reconnect or connection credential update already in progress for MCP client %s", id)
	}
	defer m.reconnectingClients.Delete(id)

	m.mu.Lock()
	defer m.mu.Unlock()

	client, ok := m.clientMap[id]
	if !ok {
		return fmt.Errorf("client %s not found", id)
	}

	if err := ValidateMCPClientName(updatedConfig.Name); err != nil {
		return fmt.Errorf("invalid MCP client configuration: %w", err)
	}

	if updatedConfig.ConnectionType != "" && updatedConfig.ConnectionType != client.ExecutionConfig.ConnectionType {
		return fmt.Errorf("connection type cannot be updated for client %s", id)
	}
	if updatedConfig.ConnectionString != nil && !updatedConfig.ConnectionString.Equals(client.ExecutionConfig.ConnectionString) {
		return fmt.Errorf("connection string cannot be updated for client %s", id)
	}
	if updatedConfig.StdioConfig != nil && !stdioConfigEqual(updatedConfig.StdioConfig, client.ExecutionConfig.StdioConfig) {
		return fmt.Errorf("stdio config cannot be updated for client %s", id)
	}
	if updatedConfig.InProcessServer != nil && updatedConfig.InProcessServer != client.ExecutionConfig.InProcessServer {
		return fmt.Errorf("in-process server cannot be updated for client %s", id)
	}
	// Normalize empty AuthType to "headers" — both are semantically identical
	oldAuthType := client.ExecutionConfig.AuthType
	if oldAuthType == "" {
		oldAuthType = schemas.MCPAuthTypeHeaders
	}
	newAuthType := updatedConfig.AuthType
	if newAuthType == "" {
		newAuthType = schemas.MCPAuthTypeHeaders
	}
	if newAuthType != oldAuthType {
		return fmt.Errorf("auth_type cannot be updated for client %s", id)
	}

	oauthConfigID := client.ExecutionConfig.OauthConfigID
	if updatedConfig.OauthConfigID != nil {
		oauthConfigID = updatedConfig.OauthConfigID
	}

	// Create a new config struct (immutable pattern) to avoid race conditions
	// with concurrent reads. Any snapshot holding the old ExecutionConfig pointer
	// will continue to see consistent data.
	newConfig := &schemas.MCPClientConfig{
		// Immutable fields - copy from existing config
		ID:               client.ExecutionConfig.ID,
		ConnectionType:   client.ExecutionConfig.ConnectionType,
		ConnectionString: client.ExecutionConfig.ConnectionString,
		StdioConfig:      client.ExecutionConfig.StdioConfig,
		AuthType:         client.ExecutionConfig.AuthType,
		OauthConfigID:    oauthConfigID,
		State:            client.ExecutionConfig.State,
		InProcessServer:  client.ExecutionConfig.InProcessServer,
		ConfigHash:       client.ExecutionConfig.ConfigHash,
		ToolPricing:      maps.Clone(client.ExecutionConfig.ToolPricing),
		// Updatable fields - copy from updated config with proper cloning
		Name:                  updatedConfig.Name,
		IsCodeModeClient:      updatedConfig.IsCodeModeClient,
		Headers:               maps.Clone(updatedConfig.Headers),
		ToolsToExecute:        slices.Clone(updatedConfig.ToolsToExecute),
		ToolsToAutoExecute:    slices.Clone(updatedConfig.ToolsToAutoExecute),
		AllowedExtraHeaders:   slices.Clone(updatedConfig.AllowedExtraHeaders),
		IsPingAvailable:       updatedConfig.IsPingAvailable,
		ToolSyncInterval:      updatedConfig.ToolSyncInterval,
		ToolExecutionTimeout:  updatedConfig.ToolExecutionTimeout,
		AllowOnAllVirtualKeys: updatedConfig.AllowOnAllVirtualKeys,
		Disabled:              updatedConfig.Disabled,
		TLSConfig:             updatedConfig.TLSConfig,
		PerUserHeaderKeys:     slices.Clone(updatedConfig.PerUserHeaderKeys),
	}

	// Atomically replace the config pointer
	client.ExecutionConfig = newConfig

	// Rebind ToolMap keys (and inner Function.Name) to the current client name.
	newPrefix := updatedConfig.Name + "-"
	newToolMap := make(map[string]schemas.ChatTool, len(client.ToolMap))
	for oldToolName, tool := range client.ToolMap {
		newToolName := oldToolName
		if _, suffix, ok := strings.Cut(oldToolName, "-"); ok {
			newToolName = newPrefix + suffix
		}
		if tool.Function != nil {
			fn := *tool.Function
			fn.Name = newToolName
			tool.Function = &fn
		}
		newToolMap[newToolName] = tool
	}

	// Replace the old ToolMap with the new one
	client.ToolMap = newToolMap

	// Also update the client Name field
	client.Name = updatedConfig.Name

	return nil
}

// UpdateClientConnection updates auth-related fields (headers) for an existing MCP client by
// closing the current connection and establishing a new one so the new credentials are verified
// before being committed. Non-credential metadata (name, tools, etc.) is preserved from the
// current execution config.
//
// On failure the clientMap entry is left in Disconnected state but its ExecutionConfig is restored
// to the previous value, allowing the health monitor to recover the client using the old credentials.
//
// Parameters:
//   - id: ID of the client whose credentials should be updated
//   - newConfig: Partial config carrying the updated auth fields (Headers, OauthConfigID,
//     PendingOAuthConfig). All other fields are ignored and taken from the current
//     execution config.
//
// Returns:
//   - error: Any connection error; nil on success
func (m *MCPManager) UpdateClientConnection(id string, newConfig *schemas.MCPClientConfig) error {
	if newConfig == nil {
		return fmt.Errorf("newConfig must not be nil")
	}
	// Hold the per-client reconnect guard for the entire read + long reconnect so
	// UpdateClient/DisableClient cannot mutate ExecutionConfig while a failed reconnect
	// restores the pre-attempt snapshot.
	if _, alreadyReconnecting := m.reconnectingClients.LoadOrStore(id, true); alreadyReconnecting {
		return fmt.Errorf("reconnect already in progress for this client")
	}
	defer m.reconnectingClients.Delete(id)

	m.mu.RLock()
	client, ok := m.clientMap[id]
	if !ok {
		m.mu.RUnlock()
		return fmt.Errorf("client %s not found", id)
	}
	// Per-user auth clients have no persistent connection — reconnect/update is not applicable.
	if client.ExecutionConfig != nil && m.credStore.RequiresPerCallConnection(client.ExecutionConfig) {
		m.mu.RUnlock()
		return fmt.Errorf("connection update is not supported for per-user auth clients")
	}
	if client.ExecutionConfig == nil {
		m.mu.RUnlock()
		return fmt.Errorf("client %s has no execution config; cannot update connection", id)
	}
	// Snapshot old execution config and build the merged config while still holding the
	// read lock so the struct copy is consistent with what the map currently holds.
	oldConfig := client.ExecutionConfig
	mergedConfig := *oldConfig
	if newConfig.Headers != nil {
		mergedConfig.Headers = maps.Clone(newConfig.Headers)
	}
	if newConfig.OauthConfigID != nil {
		mergedConfig.OauthConfigID = newConfig.OauthConfigID
	}
	// PendingOAuthConfig is taken verbatim, not merged: nil is meaningful
	// (authorization completed, stash cleared). Carrying the old value
	// forward would make connectToMCPClient park the client back in
	// pending_verification instead of connecting.
	mergedConfig.PendingOAuthConfig = newConfig.PendingOAuthConfig
	m.mu.RUnlock()

	// connectToMCPClient will close the current connection and create a new clientMap entry.
	if err := m.connectToMCPClient(m.ctx, &mergedConfig); err != nil {
		m.mu.Lock()
		if cs, exists := m.clientMap[id]; exists {
			cs.ExecutionConfig = oldConfig
		}
		m.mu.Unlock()
		return fmt.Errorf("failed to reconnect with updated credentials: %w", err)
	}

	return nil
}

func stdioConfigEqual(a, b *schemas.MCPStdioConfig) bool {
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

// RegisterTool registers a typed tool handler with the local MCP server.
// This is a convenience function that handles the conversion between typed Go
// handlers and the MCP protocol.
//
// Type Parameters:
//   - T: The expected argument type for the tool (must be JSON-deserializable)
//
// Parameters:
//   - name: Unique tool name
//   - description: Human-readable tool description
//   - handler: Typed function that handles tool execution
//   - toolSchema: Bifrost tool schema for function calling
//
// Returns:
//   - error: Any registration error
//
// Example:
//
//	type EchoArgs struct {
//	    Message string `json:"message"`
//	}
//
//	err := bifrost.RegisterMCPTool("echo", "Echo a message",
//	    func(args EchoArgs) (string, error) {
//	        return args.Message, nil
//	    }, toolSchema)
func (m *MCPManager) RegisterTool(name, description string, toolFunction MCPToolFunction[any], toolSchema schemas.ChatTool) error {
	// Ensure local server is set up
	if err := m.setupLocalHost(); err != nil {
		return fmt.Errorf("failed to setup local host: %w", err)
	}

	// Validate tool name
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("tool name is required")
	}
	if strings.Contains(name, "-") {
		return fmt.Errorf("tool name cannot contain hyphens")
	}
	if strings.Contains(name, " ") {
		return fmt.Errorf("tool name cannot contain spaces")
	}
	if len(name) > 0 && name[0] >= '0' && name[0] <= '9' {
		return fmt.Errorf("tool name cannot start with a number")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Verify internal client exists
	internalClient, ok := m.clientMap[BifrostMCPClientKey]
	if !ok {
		return fmt.Errorf("bifrost client not found")
	}

	// Create prefixed tool name for consistency with external tools
	// Format: bifrostInternal-toolName
	prefixedToolName := fmt.Sprintf("%s-%s", BifrostMCPClientKey, name)

	// Check if tool name already exists to prevent silent overwrites
	if _, exists := internalClient.ToolMap[prefixedToolName]; exists {
		return fmt.Errorf("tool '%s' is already registered", name)
	}

	m.logger.Debug("%s Registering typed tool: %s -> prefixed as %s (client: %s)", MCPLogPrefix, name, prefixedToolName, BifrostMCPClientKey)
	m.logger.Info("%s Registering typed tool: %s", MCPLogPrefix, name)

	// Create MCP handler wrapper that converts between typed and MCP interfaces
	mcpHandler := func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Extract arguments from the request using the request's methods
		args := request.GetArguments()
		result, err := toolFunction(args)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("Error: %s", err.Error())), nil
		}
		return mcp.NewToolResultText(result), nil
	}

	// Register the tool with the local MCP server using AddTool (unprefixed)
	if m.server != nil {
		tool := mcp.NewTool(name, mcp.WithDescription(description))
		m.server.AddTool(tool, mcpHandler)
	}

	// Store tool definition with prefixed name for consistency with external tools
	// Update the tool schema to use the prefixed name
	toolSchema.Function.Name = prefixedToolName
	internalClient.ToolMap[prefixedToolName] = toolSchema

	return nil
}

// ============================================================================
// CONNECTION HELPER METHODS
// ============================================================================

// connectToMCPClient establishes a connection to an external MCP server and
// registers its available tools with the manager. Uses exponential backoff
// retry logic (5 retries, 1-30 seconds) for connection establishment.
func (m *MCPManager) connectToMCPClient(requestCtx context.Context, config *schemas.MCPClientConfig) error {
	if requestCtx == nil {
		requestCtx = m.ctx
	}
	// First lock: Initialize or validate client entry
	m.mu.Lock()

	// Initialize or validate client entry
	if existingClient, exists := m.clientMap[config.ID]; exists {
		// If the client is disabled and the caller is not explicitly re-enabling it
		// (EnableClient sets config.Disabled = false before calling here; ReconnectClient
		// and the health-monitor path leave it as-is), bail out now — before we overwrite
		// the Disabled entry — so the Disabled state is preserved.
		if existingClient.State == schemas.MCPConnectionStateDisabled && config.Disabled {
			m.mu.Unlock()
			return fmt.Errorf("client %s is disabled", config.Name)
		}

		// Client entry exists from config, check for existing connection, if it does then close
		if existingClient.CancelFunc != nil {
			existingClient.CancelFunc()
			existingClient.CancelFunc = nil
		}
		if existingClient.Conn != nil {
			existingClient.Conn.Close()
		}
		// Update connection type for this connection attempt
		existingClient.ConnectionInfo.Type = config.ConnectionType
	}
	// Create new client entry with configuration.
	// Initialize State to Disconnected so the API never returns an empty state
	// during connection attempts; it transitions to Connected only on success.
	m.clientMap[config.ID] = &schemas.MCPClientState{
		Name:            config.Name,
		ExecutionConfig: config,
		State:           schemas.MCPConnectionStateDisconnected,
		ToolMap:         make(map[string]schemas.ChatTool),
		ToolNameMapping: make(map[string]string),
		ConnectionInfo: &schemas.MCPClientConnectionInfo{
			Type: config.ConnectionType,
		},
	}
	m.mu.Unlock()

	// Heavy operations performed outside lock
	var externalClient *client.Client
	var connectionInfo *schemas.MCPClientConnectionInfo

	// Initialize the external client with timeout
	// For SSE and STDIO connections, we need a long-lived context for the connection
	// but use a timeout context for the initialization phase to prevent indefinite hangs
	var ctx context.Context
	var cancel context.CancelFunc
	var longLivedCtx context.Context
	var longLivedCancel context.CancelFunc

	if config.ConnectionType == schemas.MCPConnectionTypeSSE || config.ConnectionType == schemas.MCPConnectionTypeSTDIO {
		// Create long-lived context for the connection (subprocess lifetime)
		// Use context.Background() to avoid inheriting deadline from m.ctx
		// This prevents STDIO/SSE from being limited by HTTP request timeouts
		longLivedCtx, longLivedCancel = context.WithCancel(context.Background())

		// Use long-lived context for starting the transport (spawns subprocess)
		// but create a timeout context for initialization to prevent hangs
		ctx = longLivedCtx
		cancel = longLivedCancel
	} else {
		// Other connection types (HTTP) can use timeout context
		ctx, cancel = context.WithTimeout(m.ctx, MCPClientConnectionEstablishTimeout)
		defer cancel()
	}

	// Build the plugin gate request with prepared inputs. PreHooks may mutate
	// ConnectionString, Headers, StdioCommand, StdioArgs — those mutations flow into
	// the create<X>Connection calls below via the `overrides` parameter.
	//
	// SECURITY: Authorization is stripped from the headers exposed to PreHooks and
	// re-injected after all PreHooks have run. Plugins never see the bearer token.
	connectReq := &schemas.BifrostMCPConnectRequest{
		ClientName:     config.Name,
		ConnectionType: config.ConnectionType,
		AuthType:       config.AuthType,
	}
	if config.ConnectionString != nil {
		u := config.ConnectionString.GetValue()
		connectReq.ConnectionString = &u
	}
	// Plugin-visible headers are ONLY the admin-configured static headers
	// (config.Headers). Credentials from the CredStore (Bearer tokens, signing
	// headers) are layered AFTER the connect-plugin gate runs, inside the op
	// closure — plugins can mutate static headers but never observe or
	// interfere with auth. This is the structural guarantee that replaces
	// the older strip-and-reinject Authorization dance.
	if config.ConnectionType == schemas.MCPConnectionTypeHTTP || config.ConnectionType == schemas.MCPConnectionTypeSSE {
		connectReq.Headers = utils.FlattenHeaders(utils.StaticConfigHeaders(config))
	}
	if config.StdioConfig != nil {
		cmd := config.StdioConfig.Command
		connectReq.StdioCommand = &cmd
		connectReq.StdioArgs = append([]string(nil), config.StdioConfig.Args...)
	}

	// Wrap the caller context so connection hooks can read request-scoped
	// values, such as headers extracted by the HTTP transport.
	gateCtx := schemas.NewBifrostContext(requestCtx, schemas.NoDeadline)

	// To capture InitializeResult for the response, the op closure populates these.
	var initResult *mcp.InitializeResult
	start := time.Now()

	_, gateErr := m.runConnectWithPluginPipeline(gateCtx, connectReq, func(preReq *schemas.BifrostMCPConnectRequest) (*schemas.BifrostMCPConnectResponse, error) {
		// Layer credstore-resolved auth headers onto the plugin-mutated static
		// headers. Build a shallow clone so the merged result doesn't leak back
		// into the request object that plugins captured in PreHook (which they
		// may still reference in PostHook).
		mutForWire := preReq
		if config.ConnectionType == schemas.MCPConnectionTypeHTTP || config.ConnectionType == schemas.MCPConnectionTypeSSE {
			bfCtx := schemas.NewBifrostContext(m.ctx, schemas.NoDeadline)
			authHeaders, credErr := m.credStore.ConnectionHeaders(bfCtx, config)
			if credErr != nil {
				return nil, credErr
			}
			merged := make(map[string]string, len(preReq.Headers)+len(authHeaders))
			maps.Copy(merged, preReq.Headers)
			for k, vals := range authHeaders {
				if len(vals) > 0 {
					merged[k] = vals[0]
				}
			}
			clone := *preReq
			clone.Headers = merged
			mutForWire = &clone
		}

		// Start the transport (with internal retries). Each retry uses a fresh client.
		m.logger.Debug("%s [%s] Starting transport...", MCPLogPrefix, config.Name)
		transportRetryConfig := DefaultRetryConfig
		if startErr := ExecuteWithRetry(
			m.ctx,
			func() error {
				// Close previous client if this is a retry attempt
				if externalClient != nil {
					if closeErr := externalClient.Close(); closeErr != nil {
						m.logger.Warn("%s Failed to close external client during retry: %v", MCPLogPrefix, closeErr)
					}
				}
				// Create a fresh client for this attempt
				var createErr error
				switch config.ConnectionType {
				case schemas.MCPConnectionTypeHTTP:
					externalClient, connectionInfo, createErr = m.createHTTPConnection(m.ctx, config, mutForWire)
				case schemas.MCPConnectionTypeSTDIO:
					externalClient, connectionInfo, createErr = m.createSTDIOConnection(m.ctx, config, mutForWire)
				case schemas.MCPConnectionTypeSSE:
					externalClient, connectionInfo, createErr = m.createSSEConnection(m.ctx, config, mutForWire)
				case schemas.MCPConnectionTypeInProcess:
					externalClient, connectionInfo, createErr = m.createInProcessConnection(m.ctx, config)
				default:
					return fmt.Errorf("unknown connection type: %s", config.ConnectionType)
				}
				if createErr != nil {
					return createErr
				}
				// Create per-attempt timeout context for Start operation
				// Each attempt has a deadline to prevent indefinite hangs
				var perAttemptCtx context.Context
				if config.ConnectionType == schemas.MCPConnectionTypeSSE || config.ConnectionType == schemas.MCPConnectionTypeSTDIO {
					// For STDIO/SSE: use longLivedCtx directly without additional timeout
					// The subprocess needs the context to stay valid for the entire connection lifetime
					// Do NOT defer cancel - the context manages the subprocess lifetime.
					perAttemptCtx = longLivedCtx
					m.logger.Debug("%s [%s] Starting transport...", MCPLogPrefix, config.Name)
				} else {
					// HTTP already has timeout
					perAttemptCtx = ctx
				}
				return externalClient.Start(perAttemptCtx)
			},
			transportRetryConfig,
			m.logger,
		); startErr != nil {
			return nil, fmt.Errorf("failed to start MCP client transport after %d retries: %v", transportRetryConfig.MaxRetries, startErr)
		}
		m.logger.Debug("%s [%s] Transport started successfully", MCPLogPrefix, config.Name)

		// Initialize with retry. Capture InitializeResult so the gate response can expose
		// ServerInfo / ProtocolVersion / Capabilities.
		extInitRequest := mcp.InitializeRequest{
			Params: mcp.InitializeParams{
				ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
				Capabilities:    mcp.ClientCapabilities{},
				ClientInfo: mcp.Implementation{
					Name:    fmt.Sprintf("Bifrost-%s", config.Name),
					Version: "1.0.0",
				},
			},
		}
		initRetryConfig := DefaultRetryConfig
		if initErr := ExecuteWithRetry(
			m.ctx,
			func() error {
				var initCtx context.Context
				if config.ConnectionType == schemas.MCPConnectionTypeSSE || config.ConnectionType == schemas.MCPConnectionTypeSTDIO {
					var initCancel context.CancelFunc
					initCtx, initCancel = context.WithTimeout(longLivedCtx, MCPClientConnectionEstablishTimeout)
					defer initCancel()
					m.logger.Debug("%s [%s] Initializing client with %v timeout...", MCPLogPrefix, config.Name, MCPClientConnectionEstablishTimeout)
				} else {
					initCtx = ctx
				}
				var initErr error
				initResult, initErr = externalClient.Initialize(initCtx, extInitRequest)
				return initErr
			},
			initRetryConfig,
			m.logger,
		); initErr != nil {
			return nil, fmt.Errorf("failed to initialize MCP client after %d retries: %v", initRetryConfig.MaxRetries, initErr)
		}
		m.logger.Debug("%s [%s] Client initialized successfully", MCPLogPrefix, config.Name)

		// Build the gate response from captured initialize result.
		resp := &schemas.BifrostMCPConnectResponse{
			ConnectionInfo: connectionInfo,
			ExtraFields: schemas.BifrostMCPResponseExtraFields{
				Latency: time.Since(start).Milliseconds(),
			},
		}
		if initResult != nil {
			resp.ProtocolVersion = initResult.ProtocolVersion
			resp.ServerInfo = &schemas.MCPServerInfo{
				Name:    initResult.ServerInfo.Name,
				Version: initResult.ServerInfo.Version,
			}
			resp.ServerCapabilities = &schemas.MCPServerCapabilities{
				Tools:     initResult.Capabilities.Tools != nil,
				Resources: initResult.Capabilities.Resources != nil,
				Prompts:   initResult.Capabilities.Prompts != nil,
				Logging:   initResult.Capabilities.Logging != nil,
			}
		}
		return resp, nil
	})

	if gateErr != nil {
		if config.ConnectionType == schemas.MCPConnectionTypeSSE || config.ConnectionType == schemas.MCPConnectionTypeSTDIO {
			cancel()
		}
		if externalClient != nil {
			if closeErr := externalClient.Close(); closeErr != nil {
				m.logger.Warn("%s Failed to close external client during cleanup: %v", MCPLogPrefix, closeErr)
			}
		}
		return fmt.Errorf("failed to connect MCP client %s: %s", config.Name, gateErr.GetErrorString())
	}

	tools := make(map[string]schemas.ChatTool)
	toolNameMapping := make(map[string]string)
	var listToolsErr error
	if externalClient == nil {
		// Plugin short-circuited the connect with a success response; no live transport
		// to query. Register the client as "connected" with an empty tool set — this is
		// the documented Connect-success-shortcircuit gotcha. Subsequent tool calls will
		// fail until a real connect happens.
		m.logger.Warn("%s [%s] Connect plugin short-circuited with success; no live transport — registering with empty tool set", MCPLogPrefix, config.Name)
		if connectionInfo == nil {
			connectionInfo = &schemas.MCPClientConnectionInfo{Type: config.ConnectionType}
		}
	} else {
		// Retrieve tools from the external server through the list_tools plugin gate.
		// Use a bounded timeout context to prevent indefinite hangs during tool retrieval.
		// For STDIO/SSE, ctx is longLivedCtx (no timeout), so we create a separate one here.
		m.logger.Debug("%s [%s] Retrieving tools...", MCPLogPrefix, config.Name)
		toolRetrievalCtx, toolRetrievalCancel := context.WithTimeout(m.ctx, MCPClientConnectionEstablishTimeout)
		defer toolRetrievalCancel()
		t, mapping, err := m.runListToolsWithHooks(toolRetrievalCtx, externalClient, config.Name)
		if err != nil {
			listToolsErr = err
			m.logger.Warn("%s Failed to retrieve tools from %s: %v", MCPLogPrefix, config.Name, err)
		} else {
			tools = t
			toolNameMapping = mapping
		}
		m.logger.Debug("%s [%s] Retrieved %d tools", MCPLogPrefix, config.Name, len(tools))
	}

	// A live transport that cannot enumerate its tools is not healthy. Marking it
	// Connected with an empty ToolMap makes /api/mcp/clients disagree with tools/list
	// and is sticky — ping-based health keeps succeeding (transport is alive), so a
	// reconnect that would re-run discovery never fires. Treat a failed initial
	// list_tools as a connection failure: tear down the transport and return an error
	// so the standard Disconnected + health-monitor reconnect path retries a full
	// connect+list. A server that legitimately exposes zero tools returns success with
	// an empty list (listToolsErr == nil) and is still marked Connected.
	if listToolsErr != nil {
		if (config.ConnectionType == schemas.MCPConnectionTypeSSE || config.ConnectionType == schemas.MCPConnectionTypeSTDIO) && cancel != nil {
			cancel()
		}
		if closeErr := externalClient.Close(); closeErr != nil {
			m.logger.Warn("%s Failed to close external client after tool retrieval failure: %v", MCPLogPrefix, closeErr)
		}
		return fmt.Errorf("failed to retrieve tools from MCP client %s: %w", config.Name, listToolsErr)
	}

	// Second lock: Update client with final connection details and tools
	m.mu.Lock()

	// Verify client still exists (could have been cleaned up during heavy operations)
	if client, exists := m.clientMap[config.ID]; exists {
		// If the client was disabled while we were doing the whole connection process
		// roll back the newly established connection and abort — do not overwrite the Disabled state.
		// This is a rare edge case where the client was disabled while we were doing the whole connection process
		if client.State == schemas.MCPConnectionStateDisabled {
			m.mu.Unlock()
			if (config.ConnectionType == schemas.MCPConnectionTypeSSE || config.ConnectionType == schemas.MCPConnectionTypeSTDIO) && cancel != nil {
				cancel()
			}
			if externalClient != nil {
				if closeErr := externalClient.Close(); closeErr != nil {
					m.logger.Warn("%s Failed to close external client during disable rollback: %v", MCPLogPrefix, closeErr)
				}
			}
			m.logger.Debug("%s [%s] Client was disabled during connection setup; rolling back", MCPLogPrefix, config.Name)
			return fmt.Errorf("client %s was disabled during connection setup", config.Name)
		}

		// Store the external client connection and details
		client.Conn = externalClient
		client.ConnectionInfo = connectionInfo
		client.State = schemas.MCPConnectionStateConnected

		// Store cancel function for SSE and STDIO connections to enable proper cleanup
		if config.ConnectionType == schemas.MCPConnectionTypeSSE || config.ConnectionType == schemas.MCPConnectionTypeSTDIO {
			client.CancelFunc = cancel
		}

		// Store discovered tools
		for toolName, tool := range tools {
			client.ToolMap[toolName] = tool
		}

		// Store tool name mapping for execution (sanitized_name -> original_mcp_name)
		client.ToolNameMapping = toolNameMapping

		m.logger.Debug("%s [%s] Registering %d tools. Client config - ID: %s, Name: %s, IsCodeModeClient: %v", MCPLogPrefix, config.Name, len(tools), config.ID, config.Name, config.IsCodeModeClient)
		m.logger.Info("%s Connected to MCP server '%s'", MCPLogPrefix, config.Name)
	} else {
		// Release lock before cleanup and return
		m.mu.Unlock()
		// Clean up resources before returning error: client was removed during connection setup
		// Cancel long-lived context if it was created
		if (config.ConnectionType == schemas.MCPConnectionTypeSSE || config.ConnectionType == schemas.MCPConnectionTypeSTDIO) && cancel != nil {
			cancel()
		}
		// Close external client connection to prevent transport/goroutine leaks
		if externalClient != nil {
			if err := externalClient.Close(); err != nil {
				m.logger.Warn("%s Failed to close external client during cleanup: %v", MCPLogPrefix, err)
			}
		}
		return fmt.Errorf("client %s was removed during connection setup", config.Name)
	}

	// Release lock BEFORE starting monitors to prevent deadlock
	// (StartMonitoring -> Start() tries to acquire RLock on the same mutex)
	m.mu.Unlock()

	// Register OnConnectionLost hook for SSE connections to detect idle timeouts
	if config.ConnectionType == schemas.MCPConnectionTypeSSE && externalClient != nil {
		externalClient.OnConnectionLost(func(err error) {
			m.logger.Warn("%s SSE connection lost for MCP server '%s': %v", MCPLogPrefix, config.Name, err)
			// Update state to disconnected, but never overwrite a disabled state.
			// DisableClient calls Conn.Close() while holding m.mu; the SSE library
			// fires OnConnectionLost after the lock is released, by which point
			// State is already Disabled — do not clobber it.
			m.mu.Lock()
			if client, exists := m.clientMap[config.ID]; exists && client.State != schemas.MCPConnectionStateDisabled {
				client.State = schemas.MCPConnectionStateDisconnected
			}
			m.mu.Unlock()
		})
	}

	// Start health monitoring for the client
	isPingAvailable := true
	if config.IsPingAvailable != nil {
		isPingAvailable = *config.IsPingAvailable
	}
	monitor := NewClientHealthMonitor(m, config.ID, DefaultHealthCheckInterval, isPingAvailable, m.logger)
	m.healthMonitorManager.StartMonitoring(monitor)

	// Start tool syncing for the client (skip for internal bifrost client)
	if config.ID != BifrostMCPClientKey {
		syncInterval := ResolveToolSyncInterval(config, m.toolSyncManager.GetGlobalInterval())
		if syncInterval > 0 {
			syncer := NewClientToolSyncer(m, config.ID, config.Name, syncInterval, m.logger)
			m.toolSyncManager.StartSyncing(syncer)
		}
	}

	return nil
}

// buildTLSHTTPClient constructs an *http.Client with a custom TLS configuration derived
// from MCPTLSConfig. Returns nil when tlsCfg is nil so callers can use the library default.
// InsecureSkipVerify takes priority over CACertPEM when both are set.
func (m *MCPManager) buildTLSHTTPClient(tlsCfg *schemas.MCPTLSConfig) (*http.Client, error) {
	if tlsCfg == nil {
		return nil, nil
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if tlsCfg.InsecureSkipVerify {
		m.logger.Warn("MCP client: skipping TLS verification — do not use in production")
		tlsConfig.InsecureSkipVerify = true
	} else if tlsCfg.CACertPEM != nil {
		caPEM := tlsCfg.CACertPEM.GetValue()
		if caPEM != "" {
			rootCAs, err := x509.SystemCertPool()
			if err != nil {
				rootCAs = x509.NewCertPool()
			}
			if !rootCAs.AppendCertsFromPEM([]byte(caPEM)) {
				return nil, fmt.Errorf("failed to parse MCP CA certificate PEM")
			}
			tlsConfig.RootCAs = rootCAs
		}
	}
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		transport = &http.Transport{}
	}
	cloned := transport.Clone()
	cloned.TLSClientConfig = tlsConfig
	return &http.Client{Transport: cloned}, nil
}

// createHTTPConnection creates an HTTP-based MCP client connection without holding locks.
// If overrides is non-nil and carries a populated ConnectionString or Headers, those values
// are used instead of resolving them from config. This is how plugin PreHook mutations flow
// into the transport.
func (m *MCPManager) createHTTPConnection(ctx context.Context, config *schemas.MCPClientConfig, overrides *schemas.BifrostMCPConnectRequest) (*client.Client, *schemas.MCPClientConnectionInfo, error) {
	if config.ConnectionString == nil {
		return nil, nil, fmt.Errorf("HTTP connection string is required")
	}

	// Resolve URL (override wins)
	url := config.ConnectionString.GetValue()
	if overrides != nil && overrides.ConnectionString != nil {
		url = *overrides.ConnectionString
	}

	// Resolve headers (override wins). The override path is used when the
	// connect-plugin gate has already supplied final headers (static + plugin
	// mutations + auth). The fallback path is for direct callers that bypass
	// the gate; it composes static config headers with credstore auth here.
	var headers map[string]string
	if overrides != nil && overrides.Headers != nil {
		headers = overrides.Headers
	} else {
		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		authHeaders, err := m.credStore.ConnectionHeaders(bfCtx, config)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get HTTP headers: %w", err)
		}
		headers = utils.FlattenHeaders(utils.StaticConfigHeaders(config))
		for k, vals := range authHeaders {
			if len(vals) > 0 {
				headers[k] = vals[0]
			}
		}
	}

	// Create StreamableHTTP transport. The static headers above are baked onto the
	// transport once; per-request "extra" headers (BifrostContextKeyMCPExtraHeaders,
	// allowlisted by AllowedExtraHeaders) are injected per outgoing request via the
	// headerFunc, which runs for every method — including ping/list_tools, whose
	// request.Header the mcp-go client otherwise drops.
	opts := []transport.StreamableHTTPCOption{
		transport.WithHTTPHeaders(headers),
		transport.WithHTTPHeaderFunc(func(reqCtx context.Context) map[string]string {
			return utils.FlattenHeaders(utils.ExtractFilteredExtras(reqCtx, config))
		}),
	}
	httpClient, err := m.buildTLSHTTPClient(config.TLSConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to build TLS HTTP client: %w", err)
	}
	if httpClient != nil {
		opts = append(opts, transport.WithHTTPBasicClient(httpClient))
	}
	httpTransport, err := transport.NewStreamableHTTP(url, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create HTTP transport: %w", err)
	}
	connectionInfo := &schemas.MCPClientConnectionInfo{
		Type:          config.ConnectionType,
		ConnectionURL: &url,
	}
	return client.NewClient(httpTransport), connectionInfo, nil
}

// createSTDIOConnection creates a STDIO-based MCP client connection without holding locks.
// If overrides is non-nil with a populated StdioCommand/StdioArgs, those replace the config values.
func (m *MCPManager) createSTDIOConnection(_ context.Context, config *schemas.MCPClientConfig, overrides *schemas.BifrostMCPConnectRequest) (*client.Client, *schemas.MCPClientConnectionInfo, error) {
	if config.StdioConfig == nil {
		return nil, nil, fmt.Errorf("stdio config is required")
	}

	// Resolve command and args (override wins)
	cmd := config.StdioConfig.Command
	args := config.StdioConfig.Args
	if overrides != nil {
		if overrides.StdioCommand != nil {
			cmd = *overrides.StdioCommand
		}
		if overrides.StdioArgs != nil {
			args = overrides.StdioArgs
		}
	}

	cmdString := fmt.Sprintf("%s %s", cmd, strings.Join(args, " "))

	// Check referenced environment variables are set. Inline KEY=value
	// assignments are passed directly to the stdio transport.
	for _, env := range config.StdioConfig.Envs {
		envName, _, hasInlineValue := strings.Cut(env, "=")
		if envName == "" {
			return nil, nil, fmt.Errorf("environment variable name is empty for MCP client %s", config.Name)
		}
		if hasInlineValue {
			continue
		}
		if os.Getenv(envName) == "" {
			return nil, nil, fmt.Errorf("environment variable %s is not set for MCP client %s", envName, config.Name)
		}
	}

	// Create STDIO transport
	stdioTransport := transport.NewStdio(cmd, config.StdioConfig.Envs, args...)

	// Prepare connection info
	connectionInfo := &schemas.MCPClientConnectionInfo{
		Type:               config.ConnectionType,
		StdioCommandString: &cmdString,
	}

	// Return nil for cmd since mark3labs/mcp-go manages the process internally
	return client.NewClient(stdioTransport), connectionInfo, nil
}

// createSSEConnection creates a SSE-based MCP client connection without holding locks.
// Same override semantics as createHTTPConnection.
func (m *MCPManager) createSSEConnection(ctx context.Context, config *schemas.MCPClientConfig, overrides *schemas.BifrostMCPConnectRequest) (*client.Client, *schemas.MCPClientConnectionInfo, error) {
	if config.ConnectionString == nil {
		return nil, nil, fmt.Errorf("SSE connection string is required")
	}

	url := config.ConnectionString.GetValue()
	if overrides != nil && overrides.ConnectionString != nil {
		url = *overrides.ConnectionString
	}

	// Same composition rule as createHTTPConnection: override wins (gate-supplied
	// final headers); otherwise compose static + credstore auth.
	var headers map[string]string
	if overrides != nil && overrides.Headers != nil {
		headers = overrides.Headers
	} else {
		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		authHeaders, err := m.credStore.ConnectionHeaders(bfCtx, config)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to get HTTP headers: %w", err)
		}
		headers = utils.FlattenHeaders(utils.StaticConfigHeaders(config))
		for k, vals := range authHeaders {
			if len(vals) > 0 {
				headers[k] = vals[0]
			}
		}
	}

	// Per-request extra headers are injected via headerFunc; see createHTTPConnection.
	sseOpts := []transport.ClientOption{
		transport.WithHeaders(headers),
		transport.WithHeaderFunc(func(reqCtx context.Context) map[string]string {
			return utils.FlattenHeaders(utils.ExtractFilteredExtras(reqCtx, config))
		}),
	}
	sseHTTPClient, err := m.buildTLSHTTPClient(config.TLSConfig)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to build TLS HTTP client: %w", err)
	}
	if sseHTTPClient != nil {
		sseOpts = append(sseOpts, transport.WithHTTPClient(sseHTTPClient))
	}
	sseTransport, err := transport.NewSSE(url, sseOpts...)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create SSE transport: %w", err)
	}

	connectionInfo := &schemas.MCPClientConnectionInfo{
		Type:          config.ConnectionType,
		ConnectionURL: &url,
	}
	return client.NewClient(sseTransport), connectionInfo, nil
}

// createInProcessConnection creates an in-process MCP client connection without holding locks.
// This allows direct connection to an MCP server running in the same process, providing
// the lowest latency and highest performance for tool execution.
func (m *MCPManager) createInProcessConnection(_ context.Context, config *schemas.MCPClientConfig) (*client.Client, *schemas.MCPClientConnectionInfo, error) {
	if config.InProcessServer == nil {
		return nil, nil, fmt.Errorf("InProcess connection requires a server instance")
	}

	// Create in-process client directly connected to the provided server
	inProcessClient, err := client.NewInProcessClient(config.InProcessServer)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create in-process client: %w", err)
	}

	// Prepare connection info
	connectionInfo := &schemas.MCPClientConnectionInfo{
		Type: config.ConnectionType,
	}

	return inProcessClient, connectionInfo, nil
}

// ============================================================================
// LOCAL MCP SERVER AND CLIENT MANAGEMENT
// ============================================================================

// setupLocalHost initializes the local MCP server and client if not already running.
// This creates a STDIO-based server for local tool hosting and a corresponding client.
// This is called automatically when tools are registered or when the server is needed.
//
// Returns:
//   - error: Any setup error
func (m *MCPManager) setupLocalHost() error {
	// First check: fast path if already initialized
	m.mu.Lock()
	if m.server != nil && m.serverRunning {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	// Create server and client into local variables (outside lock to avoid
	// holding lock during object creation, even though it's lightweight)
	server, err := m.createLocalMCPServer()
	if err != nil {
		return fmt.Errorf("failed to create local MCP server: %w", err)
	}

	client, err := m.createLocalMCPClient()
	if err != nil {
		return fmt.Errorf("failed to create local MCP client: %w", err)
	}

	// Second check and assignment: hold lock for atomic check-and-set
	m.mu.Lock()
	// Double-check: another goroutine might have initialized while we were creating
	if m.server != nil && m.serverRunning {
		m.mu.Unlock()
		return nil
	}

	// Assign server and client atomically while holding the lock
	m.server = server
	m.clientMap[BifrostMCPClientKey] = client
	m.mu.Unlock()

	// Start the server and initialize client connection
	// (startLocalMCPServer already locks internally)
	return m.startLocalMCPServer()
}

// createLocalMCPServer creates a new local MCP server instance with STDIO transport.
// This server will host tools registered via RegisterTool function.
//
// Returns:
//   - *server.MCPServer: Configured MCP server instance
//   - error: Any creation error
func (m *MCPManager) createLocalMCPServer() (*server.MCPServer, error) {
	// Create MCP server
	mcpServer := server.NewMCPServer(
		"Bifrost-MCP-Server",
		"1.0.0",
		server.WithToolCapabilities(true),
	)

	return mcpServer, nil
}

// createLocalMCPClient creates a placeholder client entry for the local MCP server.
// The actual in-process client connection will be established in startLocalMCPServer.
//
// Returns:
//   - *schemas.MCPClientState: Placeholder client for local server
//   - error: Any creation error
func (m *MCPManager) createLocalMCPClient() (*schemas.MCPClientState, error) {
	// Don't create the actual client connection here - it will be created
	// after the server is ready using NewInProcessClient
	return &schemas.MCPClientState{
		ExecutionConfig: &schemas.MCPClientConfig{
			ID:             BifrostMCPClientKey,
			Name:           BifrostMCPClientKey, // Use same value as ID for consistent prefixing
			ToolsToExecute: []string{"*"},       // Allow all tools for internal client
		},
		ToolMap:         make(map[string]schemas.ChatTool),
		ToolNameMapping: make(map[string]string),
		ConnectionInfo: &schemas.MCPClientConnectionInfo{
			Type: schemas.MCPConnectionTypeInProcess, // Accurate: in-process (in-memory) transport
		},
	}, nil
}

// startLocalMCPServer creates an in-process connection between the local server and client.
//
// Returns:
//   - error: Any startup error
func (m *MCPManager) startLocalMCPServer() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if server is already running
	if m.server != nil && m.serverRunning {
		return nil
	}

	if m.server == nil {
		return fmt.Errorf("server not initialized")
	}

	// Create in-process client directly connected to the server
	inProcessClient, err := client.NewInProcessClient(m.server)
	if err != nil {
		return fmt.Errorf("failed to create in-process MCP client: %w", err)
	}

	// Update the client connection
	clientEntry, ok := m.clientMap[BifrostMCPClientKey]
	if !ok {
		return fmt.Errorf("bifrost client not found")
	}
	clientEntry.Conn = inProcessClient

	// Initialize the in-process client
	ctx, cancel := context.WithTimeout(m.ctx, MCPClientConnectionEstablishTimeout)
	defer cancel()

	// Create proper initialize request with correct structure
	initRequest := mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			Capabilities:    mcp.ClientCapabilities{},
			ClientInfo: mcp.Implementation{
				Name:    BifrostMCPClientName,
				Version: BifrostMCPVersion,
			},
		},
	}

	_, err = inProcessClient.Initialize(ctx, initRequest)
	if err != nil {
		return fmt.Errorf("failed to initialize MCP client: %w", err)
	}

	// Mark server as running
	m.serverRunning = true

	return nil
}
