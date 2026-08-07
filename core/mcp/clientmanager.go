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
		// Build, start, and initialize with a single retry loop covering the
		// whole connect handshake — same recreate-fresh-per-attempt shape as
		// connectToMCPClient's shared-connect path (each attempt closes the
		// previous attempt's client and builds a new one) but with
		// PerCallConnectRetryConfig, not ConnectRetryConfig: unlike the shared
		// dial, this runs synchronously in front of a live tool call with no
		// separate budget wrapper around it, so it can't afford the shared
		// path's full backoff schedule. Previously Start ran once with no
		// retry at all — a transient blip failed the whole tool call outright.
		//
		// Start and Initialize share one retry budget (not two separate
		// ones) deliberately: a failed Initialize can leave the transport
		// broken — mcp-go marks initialization state before validating the
		// response — so a retry needs a fresh client from Start onward
		// regardless of which of the two failed, and one combined attempt
		// achieves that without extra bookkeeping to track which stage a
		// given retry is rebuilding for.
		var initResult *mcp.InitializeResult
		if err := ExecuteWithRetry(
			ctx,
			func() error {
				if tempClient != nil {
					if closeErr := tempClient.Close(); closeErr != nil {
						m.logger.Warn("%s Failed to close ephemeral client during retry: %v", MCPLogPrefix, closeErr)
					}
				}
				httpTransport, err := transport.NewStreamableHTTP(targetURL, perUserOpts...)
				if err != nil {
					return fmt.Errorf("failed to create HTTP transport: %w", err)
				}
				tempClient = client.NewClient(httpTransport)
				if err := tempClient.Start(ctx); err != nil {
					return err
				}

				// Bound the MCP `initialize` handshake — a stalled upstream (TCP
				// open succeeds but JSON-RPC initialize never returns) would
				// otherwise block the entire tool call until the parent request
				// ctx fires. Mirrors the bound used for shared-connection
				// Initialize in connectToMCPClient. Fresh per attempt, not
				// deferred to the end of the retry loop.
				initCtx, initCancel := context.WithTimeout(ctx, MCPClientConnectionEstablishTimeout)
				defer initCancel()
				initResult, err = tempClient.Initialize(initCtx, initRequest)
				return err
			},
			PerCallConnectRetryConfig,
			m.logger,
		); err != nil {
			if tempClient != nil {
				_ = tempClient.Close()
				tempClient = nil
			}
			return nil, fmt.Errorf("failed to establish ephemeral MCP connection: %w", err)
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

// inflightClientOp tracks one in-flight exclusive operation on a client
// (reconnect, enable/disable, credential update). Waiters observe completion
// through the done channel; err carries the operation's final outcome and is
// only safe to read after done is closed.
type inflightClientOp struct {
	done chan struct{}
	err  error
}

// beginExclusiveClientOp atomically registers an in-flight exclusive operation
// for the given client ID. ok is false when another operation already holds the
// slot, preserving the second-caller-errors contract every guarded entry point
// relies on. On success the caller must invoke finish exactly once with the
// operation's final error so waiters parked in AwaitReconnect get the outcome.
//
// A completed op is deliberately left in the map instead of being deleted on
// finish: deleting immediately would let a caller who was just rejected with
// "already in progress" lose the race a second time, observing no entry in
// AwaitReconnect and silently missing a reconnect that in fact just
// succeeded. The completed entry is instead lazily replaced the next time
// this function is called for the same ID, via CompareAndSwap so a fresh
// concurrent begin can't be shadowed by another late reader observing the
// old (already-done) op.
func (m *MCPManager) beginExclusiveClientOp(id string) (finish func(error), ok bool) {
	op := &inflightClientOp{done: make(chan struct{})}
	for {
		existing, loaded := m.reconnectingClients.LoadOrStore(id, op)
		if !loaded {
			return m.finishExclusiveClientOp(op), true
		}
		existingOp := existing.(*inflightClientOp)
		select {
		case <-existingOp.done:
			// Previous op already finished; replace it with ours so a fresh
			// operation can start. Any caller still holding a reference to
			// existingOp (from a Load that raced this one) keeps observing
			// its correct, already-final result.
			if m.reconnectingClients.CompareAndSwap(id, existing, op) {
				return m.finishExclusiveClientOp(op), true
			}
			// Lost the race to install ours; retry from the top.
		default:
			return nil, false
		}
	}
}

func (m *MCPManager) finishExclusiveClientOp(op *inflightClientOp) func(error) {
	return func(err error) {
		op.err = err
		close(op.done)
	}
}

// AwaitReconnect waits up to budget for the in-flight exclusive operation on
// the given client (typically a reconnect) to complete. Returns (true, finalErr)
// when the operation finished within the budget (including one that had
// already finished the instant this was called), and (false, nil) when
// nothing has ever been registered for this client or the wait timed out. A
// timed-out wait never cancels the underlying operation; it keeps running to
// completion in the background.
func (m *MCPManager) AwaitReconnect(clientID string, budget time.Duration) (bool, error) {
	v, ok := m.reconnectingClients.Load(clientID)
	if !ok {
		return false, nil
	}
	op := v.(*inflightClientOp)
	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case <-op.done:
		return true, op.err
	case <-timer.C:
		return false, nil
	}
}

// CloseAndMarkNeedsReauth tears down a shared client's live upstream
// connection and flips it to NeedsReauth, without attempting a new dial.
// Used after OAuth credential rotation: RotateMCPOAuthConfig has already
// cascaded every token bound to this client's oauth_config to
// needs_reauth in the DB, so the in-memory connection is still open on the
// now-invalidated Authorization header, and calling ReconnectClient instead
// would just fail immediately (GetAccessToken refuses to hand out a token
// for a needs_reauth row) while leaving the stale connection in place.
//
// Parameters:
//   - id: ID of the client to close and flip to needs_reauth
//
// Returns:
//   - error: if the client does not exist or has no persistent connection
func (m *MCPManager) CloseAndMarkNeedsReauth(id string) (retErr error) {
	finish, ok := m.beginExclusiveClientOp(id)
	if !ok {
		return fmt.Errorf("reconnect or connection credential update already in progress for MCP client %s", id)
	}
	defer func() { finish(retErr) }()

	m.mu.Lock()
	defer m.mu.Unlock()

	clientState, ok := m.clientMap[id]
	if !ok {
		return fmt.Errorf("client %s not found", id)
	}
	// Covers both genuine per-user auth types (always per-call) and a shared
	// HTTP client (oauth/headers) currently running in per-call mode via
	// needs_session_stickiness nil/false — either way there is no
	// persistent connection here to close.
	if clientState.ExecutionConfig != nil && m.credStore.RequiresPerCallConnection(clientState.ExecutionConfig) {
		return fmt.Errorf("client uses per-call connections; there is no persistent connection to close: %w", schemas.ErrMCPReconnectNotApplicable)
	}
	if clientState.State == schemas.MCPConnectionStateDisabled {
		// DisableClient is authoritative; a rotation racing a disable must
		// not resurrect the client into needs_reauth.
		return nil
	}

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
	clientState.State = schemas.MCPConnectionStateNeedsReauth
	m.logger.Debug("%s MCP client '%s' closed and marked needs_reauth after credential rotation", MCPLogPrefix, clientState.ExecutionConfig.Name)
	return nil
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
func (m *MCPManager) ReconnectClient(id string) (retErr error) {
	// Acquire per-client reconnect/update guard before reading config snapshot.
	finish, ok := m.beginExclusiveClientOp(id)
	if !ok {
		return fmt.Errorf("reconnect already in progress for this client")
	}
	defer func() { finish(retErr) }()

	m.mu.Lock()
	client, ok := m.clientMap[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("client %s not found", id)
	}
	// Per-call clients have no persistent upstream connection to reconnect —
	// this covers both genuine per-user auth types (always per-call, auth
	// resolved per request/user identity) and a shared HTTP client
	// (oauth/headers) currently running in per-call mode via
	// needs_session_stickiness nil/false.
	if client.ExecutionConfig != nil && m.credStore.RequiresPerCallConnection(client.ExecutionConfig) {
		m.mu.Unlock()
		return fmt.Errorf("client uses per-call connections; there is no persistent connection to reconnect: %w", schemas.ErrMCPReconnectNotApplicable)
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
func (m *MCPManager) AddClient(requestCtx context.Context, config *schemas.MCPClientConfig) (retErr error) {
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

	// Claim the per-client exclusive-op slot before the placeholder becomes
	// visible below (m.mu.Unlock): every other entry point that dials this
	// client (ReconnectClient, EnableClient, UpdateClient, ...) already
	// guards itself the same way, and without this guard here one of them
	// could start a concurrent dial for this ID the instant the placeholder
	// is published, racing this function's own connect below. config.ID was
	// just confirmed absent from clientMap under the same lock, so no
	// exclusive op can already be tracking it — !ok is not expected.
	finish, ok := m.beginExclusiveClientOp(config.ID)
	if !ok {
		delete(m.clientMap, config.ID)
		m.mu.Unlock()
		return fmt.Errorf("client %s: conflicting operation already in progress", config.Name)
	}
	defer func() { finish(retErr) }()

	// Temporarily unlock for the connection attempt
	// This is to avoid deadlocks when the connection attempt is made
	m.mu.Unlock()

	// OAuth-type clients with PendingOAuthConfig set carry the inline
	// `oauth_config` block from config.json but have not yet been
	// authorized by an admin. For shared OAuth there is no oauth_configs
	// row or token; for per-user OAuth admin verification + tool discovery
	// has not run. In both cases the normal connect / per-call paths would
	// either fail or land the client in a state that misrepresents the
	// missing admin step. Park them in pending_verification until an admin
	// completes the browser flow via
	// POST /api/mcp/client/{id}/initiate-verification. PendingOAuthConfig
	// is cleared once the OAuth callback completes; the subsequent
	// reconnect skips this branch and the normal path runs.
	if config.PendingOAuthConfig != nil &&
		(config.AuthType == schemas.MCPAuthTypeOauth || config.AuthType == schemas.MCPAuthTypePerUserOauth) {
		m.mu.Lock()
		if client, exists := m.clientMap[config.ID]; exists {
			if config.ConnectionString != nil {
				url := config.ConnectionString.GetValue()
				client.ConnectionInfo.ConnectionURL = &url
			}
			client.State = schemas.MCPConnectionStatePendingVerification
		}
		m.mu.Unlock()
		m.logger.Debug("%s OAuth MCP client '%s' (auth_type=%s) registered in pending_verification (awaiting admin authorization)", MCPLogPrefix, config.Name, config.AuthType)
		return nil
	}

	// Per-user-headers clients whose DiscoveredTools has never been set
	// have not had the one-time admin verification + discovery run yet.
	// The UI Create flow runs that synchronously during create, so any
	// per-user-headers row arriving here with a nil map was either
	// declared in config.json or had its tools cleared by schema drift /
	// manual edit. A verified server that legitimately exposes zero tools
	// persists a non-nil empty map (see BeforeSave/AfterFind in
	// framework/configstore/tables/mcp.go), so nil-check rather than
	// len-check to avoid re-parking it in pending_verification on every
	// reload. Park in pending_verification so the UI surfaces an admin
	// "Verify" CTA that hits POST /api/mcp/client/{id}/verify-headers; the
	// normal per-call path takes over once DiscoveredTools is set.
	if config.AuthType == schemas.MCPAuthTypePerUserHeaders && config.DiscoveredTools == nil {
		m.mu.Lock()
		if client, exists := m.clientMap[config.ID]; exists {
			if config.ConnectionString != nil {
				url := config.ConnectionString.GetValue()
				client.ConnectionInfo.ConnectionURL = &url
			}
			client.State = schemas.MCPConnectionStatePendingVerification
		}
		m.mu.Unlock()
		m.logger.Debug("%s Per-user-headers MCP client '%s' registered in pending_verification (awaiting admin verification)", MCPLogPrefix, config.Name)
		return nil
	}

	// Token-exchange clients with no DiscoveredTools have not had their
	// one-time admin verification run yet (which also retains the admin
	// discovery credential). Park in pending_verification like
	// per-user-headers; the per-call path takes over once verified.
	if config.AuthType == schemas.MCPAuthTypeTokenExchange && len(config.DiscoveredTools) == 0 {
		m.mu.Lock()
		if client, exists := m.clientMap[config.ID]; exists {
			if config.ConnectionString != nil {
				url := config.ConnectionString.GetValue()
				client.ConnectionInfo.ConnectionURL = &url
			}
			client.State = schemas.MCPConnectionStatePendingVerification
		}
		m.mu.Unlock()
		m.logger.Debug("%s Token-exchange MCP client '%s' registered in pending_verification (awaiting admin verification)", MCPLogPrefix, config.Name)
		return nil
	}

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
				client.State = schemas.MCPConnectionStateHealthy
				// Seed the change-detection hash from what was just
				// restored (without firing the callback — restoring
				// already-known tools is not a change) so a subsequent
				// rediscovery of the exact same tools correctly no-ops
				// instead of looking like a change on the first post-boot
				// tick.
				client.LastToolsHash = computeToolsHash(config.DiscoveredTools, config.DiscoveredToolNameMapping)
				m.logger.Debug("%s Per-user (%s) MCP client '%s' restored with %d tools", MCPLogPrefix, config.AuthType, config.Name, len(config.DiscoveredTools))
			} else {
				// No dedicated "pending tools" state: Healthy + an empty
				// ToolMap already says "no tools discovered yet" on its own —
				// nothing downstream (execution gating, GetToolPerClient)
				// ever treated the two differently.
				client.State = schemas.MCPConnectionStateHealthy
				m.logger.Debug("%s Per-user (%s) MCP client '%s' registered (connection deferred to runtime)", MCPLogPrefix, config.AuthType, config.Name)
			}
		}
		m.mu.Unlock()
		// A client with nothing to restore gets its first discovery pass
		// synchronously here — mirrors per_user_headers/token_exchange's own
		// admin-verify-at-setup-time step (VerifyHeadersConnection /
		// VerifyPerUserOAuthConnection, run by the create/verify HTTP
		// handler before this ever runs). Shared oauth/headers/none have no
		// such separate step, so without this they'd sit at Healthy/0-tools
		// until the periodic checker's first tick — which, since Start()
		// uses the slow healthyInterval (not the fast Unstable one) for a
		// client that already starts Healthy, could be minutes away.
		// Best-effort: a failure here just means the periodic checker's own
		// retries pick it up shortly instead — not a reason to fail the
		// whole add/connect.
		if len(config.DiscoveredTools) == 0 {
			if tools, mapping, discErr := m.performAdminToolDiscovery(requestCtx, config); discErr != nil {
				m.logger.Debug("%s Initial per-call tool discovery failed for MCP client '%s': %v — the periodic checker will retry", MCPLogPrefix, config.Name, discErr)
			} else {
				m.SetClientTools(config.ID, tools, mapping)
			}
		}
		// Start the connection checker so this client's tools stay current
		// going forward. Released BEFORE this, mirroring connectToMCPClient's
		// own success path (StartMonitoring -> Start() takes m.mu.RLock
		// internally, so calling it under the write lock above would deadlock).
		if config.ID != BifrostMCPClientKey {
			isPingAvailable := true
			if config.IsPingAvailable != nil {
				isPingAvailable = *config.IsPingAvailable
			}
			syncInterval := ResolveToolSyncInterval(config, m.checkerManager.GetGlobalInterval())
			checker := NewClientConnectionChecker(m, config.ID, syncInterval, isPingAvailable, m.logger)
			m.checkerManager.StartChecking(checker)
		}
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
// Used in three paths:
//   - Admin test flow: admin enters sample values during MCP client creation,
//     this runs an Initialize handshake against the upstream to validate the
//     schema (PerUserHeaderKeys) + discover tools. The discovered tools then
//     persist on the MCPClient row; the sample values are discarded.
//   - User submission flow: an end user submits their own values via the
//     workspace submit URL surfaced inline by MCPAuthRequiredError. The
//     handler runs this before upserting the row so a bad submission returns
//     422 immediately instead of failing on the next tool call.
//   - Shared headers/none per-call discovery: performAdminToolDiscovery's
//     periodic refresh for a client running per-call (needs_session_stickiness
//     nil/false). userHeaders here is whatever AdminConnectionHeaders
//     resolved, which may legitimately be empty for these auth types.
//
// Parameters:
//   - config: MCP client configuration (connection URL, name, PerUserHeaderKeys, etc.)
//   - userHeaders: caller-supplied header_name → value map. Must cover every
//     PerUserHeaderKeys entry for auth_type=per_user_headers (the caller
//     validates that before invoking); may be empty for shared headers/none.
//
// Returns:
//   - map[string]schemas.ChatTool: discovered tools keyed by prefixed name
//   - map[string]string: tool name mapping (sanitized → original MCP name)
//   - error: any error during verification
func (m *MCPManager) VerifyHeadersConnection(ctx context.Context, config *schemas.MCPClientConfig, userHeaders map[string]string) (map[string]schemas.ChatTool, map[string]string, error) {
	if config.ConnectionString == nil || config.ConnectionString.GetValue() == "" {
		return nil, nil, fmt.Errorf("connection URL is required for per-user headers verification")
	}
	// Non-empty userHeaders is only required for genuinely per-user auth:
	// PerUserHeaderKeys declares a schema of headers every caller must
	// supply, so an empty map there is always a caller bug. A shared
	// headers/none client (performAdminToolDiscovery's per-call discovery
	// path, added for the needs_session_stickiness feature) has no such
	// schema — it may legitimately have no admin-configured Authorization
	// header at all, with auth carried entirely by other static config
	// headers (already layered in below) or none.
	if config.AuthType == schemas.MCPAuthTypePerUserHeaders && len(userHeaders) == 0 {
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

// performAdminToolDiscovery resolves the credential for a per-call client
// (via credStore.AdminConnectionHeaders) and runs a one-shot ephemeral
// connect-discover-close cycle, reusing the exact same verification
// functions the one-time bootstrap flow itself uses
// (VerifyPerUserOAuthConnection / VerifyHeadersConnection) — this is the
// periodic connection checker's per-call counterpart to reusing a live Conn
// for sticky clients.
//
// Covers two families:
//   - Per-user (per_user_oauth, per_user_headers, token_exchange): always
//     per-call, resolves the retained admin bootstrap credential — a
//     separate credential from any individual end user's own session.
//   - Shared (oauth, headers, none) running per-call via
//     needs_session_stickiness nil/false: resolves the same credential a
//     real tool call would (see each resolver's AdminConnectionHeaders —
//     there is no separate "admin" concept for these types).
func (m *MCPManager) performAdminToolDiscovery(ctx context.Context, config *schemas.MCPClientConfig) (map[string]schemas.ChatTool, map[string]string, error) {
	headers, err := m.credStore.AdminConnectionHeaders(ctx, config)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to resolve admin credential: %w", err)
	}
	switch config.AuthType {
	case schemas.MCPAuthTypePerUserOauth, schemas.MCPAuthTypeTokenExchange, schemas.MCPAuthTypeOauth:
		// All three resolve to a bearer credential (the retained bootstrap
		// token, a client-credentials token for token exchange, or the
		// shared oauth token), so they share the bearer-based one-shot
		// verification path.
		accessToken := strings.TrimPrefix(headers.Get("Authorization"), "Bearer ")
		if accessToken == "" {
			return nil, nil, fmt.Errorf("admin credential resolved no access token")
		}
		return m.VerifyPerUserOAuthConnection(ctx, config, accessToken)
	case schemas.MCPAuthTypePerUserHeaders, schemas.MCPAuthTypeHeaders, schemas.MCPAuthTypeNone:
		userHeaders := make(map[string]string, len(headers))
		for k := range headers {
			userHeaders[k] = headers.Get(k)
		}
		return m.VerifyHeadersConnection(ctx, config, userHeaders)
	default:
		return nil, nil, fmt.Errorf("admin tool discovery not supported for auth_type %q", config.AuthType)
	}
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
	client, exists := m.clientMap[clientID]
	if !exists {
		m.mu.Unlock()
		return
	}
	maps.Copy(client.ToolMap, tools)
	client.ToolNameMapping = toolNameMapping
	client.State = schemas.MCPConnectionStateHealthy
	m.logger.Debug("%s Set %d tools on client '%s'", MCPLogPrefix, len(tools), client.Name)
	fire := m.toolsChangedCallback(client, clientID, tools, toolNameMapping)
	m.mu.Unlock()

	// Fired outside the lock — see toolsChangeCallback's field doc.
	if fire != nil {
		fire()
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
	// Stop the connection checker for this client
	m.checkerManager.StopChecking(id)
	m.logger.Debug("%s Stopped connection checker for MCP server '%s'", MCPLogPrefix, client.ExecutionConfig.Name)
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
func (m *MCPManager) DisableClient(id string) (retErr error) {
	// The internal in-process client must never be disabled:
	if id == BifrostMCPClientKey {
		return fmt.Errorf("cannot disable internal bifrost client")
	}
	// beginExclusiveClientOp registers atomically, so the check and the
	// in-flight record insertion cannot race another operation.
	finish, ok := m.beginExclusiveClientOp(id)
	if !ok {
		return fmt.Errorf("reconnect or connection credential update already in progress for MCP client %s", id)
	}
	defer func() { finish(retErr) }()

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

	m.checkerManager.StopChecking(id)

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
func (m *MCPManager) EnableClient(id string) (retErr error) {
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
	m.mu.Unlock()

	// Guard against concurrent reconnects for the same client from any caller
	// (health monitor, manual API call, etc.) BEFORE mutating Disabled, not
	// after: connectToMCPClient's own Disabled guard only bails out while
	// State is Disabled AND config.Disabled is still true (see its call
	// site), so flipping Disabled first and only losing this guard
	// afterwards would let a concurrent ReconnectClient read Disabled=false,
	// sail past that guard, and connect the entry while its State is still
	// Disabled — silently enabling the client out from under an EnableClient
	// call that then reports "already in progress" as if nothing happened.
	// Registration is atomic: whichever caller arrives second gets the error
	// immediately.
	finish, ok := m.beginExclusiveClientOp(id)
	if !ok {
		return fmt.Errorf("reconnect already in progress for this client")
	}
	defer func() { finish(retErr) }()

	m.mu.Lock()
	clientState, ok = m.clientMap[id]
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
		// Healthy either way — an empty ToolMap already says "no tools
		// discovered yet" on its own, no dedicated state needed.
		if cs, exists := m.clientMap[id]; exists {
			cs.State = schemas.MCPConnectionStateHealthy
		}
		m.mu.Unlock()
		m.logger.Debug("%s Per-user auth MCP client '%s' enabled (no persistent connection)", MCPLogPrefix, configCopy.Name)
		return nil
	}

	if err := m.connectToMCPClient(m.ctx, configCopy); err != nil {
		// Connection failed — leave the entry as Disconnected so the health monitor can
		// recover it, but only if the client has not been disabled in the meantime, and
		// don't clobber NeedsReauth: connectToMCPClient already classified this failure
		// as a dead OAuth2 credential (under its own lock, above), and a generic
		// Disconnected here would silently erase that more specific signal.
		m.mu.Lock()
		alreadyDisabled := false
		if cs, exists := m.clientMap[id]; exists {
			switch cs.State {
			case schemas.MCPConnectionStateDisabled:
				alreadyDisabled = true
			case schemas.MCPConnectionStateNeedsReauth:
				// preserve as-is
			default:
				cs.State = schemas.MCPConnectionStateUnstable
			}
		}
		m.mu.Unlock()

		if !alreadyDisabled {
			isPingAvailable := true
			if configCopy.IsPingAvailable != nil {
				isPingAvailable = *configCopy.IsPingAvailable
			}
			syncInterval := ResolveToolSyncInterval(configCopy, m.checkerManager.GetGlobalInterval())
			checker := NewClientConnectionChecker(m, id, syncInterval, isPingAvailable, m.logger)
			m.checkerManager.StartChecking(checker)
		}

		return fmt.Errorf("failed to connect MCP client '%s': %w", configCopy.Name, err)
	}

	m.logger.Debug("%s MCP client '%s' enabled successfully", MCPLogPrefix, configCopy.Name)
	return nil
}

// RequiresPerCallConnection reports whether config resolves to a per-call
// connection (true) or a persistent shared one (false), taking auth type,
// connection type, and needs_session_stickiness into account together — the
// same decision AcquireClientConn and UpdateClient act on. Read-only
// passthrough to the credential store's resolver dispatch, exposed so
// callers (e.g. the HTTP handler, to describe what an update just changed)
// don't have to duplicate that dispatch themselves.
func (m *MCPManager) RequiresPerCallConnection(config *schemas.MCPClientConfig) bool {
	return m.credStore.RequiresPerCallConnection(config)
}

// ErrMCPSharedConnectFailedAfterUpdate signals that UpdateClient's own field
// update already committed (client.ExecutionConfig now holds the new config,
// in-memory, with no rollback path) before the needs_session_stickiness
// per-call->sticky dial was attempted and failed. Callers that persist config
// to a store before calling UpdateClient (see the HTTP update handler) must
// not roll back that persisted row on this specific error — doing so would
// diverge the DB (old value) from the runtime (new value, parked Unstable
// with a checker already retrying the connection) until a restart re-reads
// the DB and silently discards the change. Wrapped via %w so errors.Is still
// finds it under the caller-facing message.
var ErrMCPSharedConnectFailedAfterUpdate = errors.New("mcp client fields updated, but establishing the shared connection failed")

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
func (m *MCPManager) UpdateClient(id string, updatedConfig *schemas.MCPClientConfig) (retErr error) {
	finish, ok := m.beginExclusiveClientOp(id)
	if !ok {
		return fmt.Errorf("reconnect or connection credential update already in progress for MCP client %s", id)
	}
	defer func() { finish(retErr) }()

	// The metadata swap runs under its own scoped lock so the two possible
	// stickiness-transition follow-ups below — closing a connection versus
	// dialing a new one via connectToMCPClient — can run after it's
	// released; connectToMCPClient acquires m.mu itself, so calling it while
	// still holding the lock here would deadlock.
	var newConfig *schemas.MCPClientConfig
	var becamePerCall, becameSticky bool
	var clientName string

	err := func() error {
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

		// Snapshot the pre-update per-call-ness so it can be compared against
		// the post-update value below — this is what actually detects a
		// needs_session_stickiness flip (auth types that are always per-call
		// regardless of the field, e.g. per-user, never show a transition here).
		wasPerCall := m.credStore.RequiresPerCallConnection(client.ExecutionConfig)

		// Create a new config struct (immutable pattern) to avoid race conditions
		// with concurrent reads. Any snapshot holding the old ExecutionConfig pointer
		// will continue to see consistent data.
		newConfig = &schemas.MCPClientConfig{
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
			Name:                   updatedConfig.Name,
			IsCodeModeClient:       updatedConfig.IsCodeModeClient,
			Headers:                maps.Clone(updatedConfig.Headers),
			ToolsToExecute:         slices.Clone(updatedConfig.ToolsToExecute),
			ToolsToAutoExecute:     slices.Clone(updatedConfig.ToolsToAutoExecute),
			AllowedExtraHeaders:    slices.Clone(updatedConfig.AllowedExtraHeaders),
			IsPingAvailable:        updatedConfig.IsPingAvailable,
			NeedsSessionStickiness: updatedConfig.NeedsSessionStickiness,
			ToolSyncInterval:       updatedConfig.ToolSyncInterval,
			ToolExecutionTimeout:   updatedConfig.ToolExecutionTimeout,
			AllowOnAllVirtualKeys:  updatedConfig.AllowOnAllVirtualKeys,
			Disabled:               updatedConfig.Disabled,
			TLSConfig:              updatedConfig.TLSConfig,
			PerUserHeaderKeys:      slices.Clone(updatedConfig.PerUserHeaderKeys),
			PendingOAuthConfig:     updatedConfig.PendingOAuthConfig,
			TokenExchange:          updatedConfig.TokenExchange,
		}

		// Atomically replace the config pointer
		client.ExecutionConfig = newConfig
		clientName = updatedConfig.Name

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

		// needs_session_stickiness just flipped: apply it now instead of
		// leaving the live connection state stale until some unrelated event
		// (a restart, a manual reconnect) happens to re-evaluate it.
		isPerCall := m.credStore.RequiresPerCallConnection(newConfig)
		switch {
		case !wasPerCall && isPerCall:
			// Sticky -> per-call: release the persistent connection right
			// here, under the same lock CloseAndMarkNeedsReauth uses for the
			// equivalent close (cheap, no network I/O). The checker is
			// stopped just below, once the lock is released.
			//
			// Bump ConnGeneration before closing: StopChecking (below, after
			// the lock releases) does not cancel a check already in flight,
			// so one that started just before this transition can still
			// complete afterward. Its result — a tool-map write-back or a
			// state write (Unstable/Healthy) — must be recognized as stale
			// and dropped, the same guard writeBackTools already applies to
			// tool maps and setState now applies to state writes too.
			client.ConnGeneration++
			if client.CancelFunc != nil {
				client.CancelFunc()
				client.CancelFunc = nil
			}
			if client.Conn != nil {
				if closeErr := client.Conn.Close(); closeErr != nil {
					m.logger.Error("%s Failed to close connection for MCP client '%s': %v", MCPLogPrefix, clientName, closeErr)
				}
				client.Conn = nil
			}
			// Disabled/NeedsReauth are authoritative states set by other
			// flows (DisableClient, credential rotation) — this transition
			// never overrides them. Otherwise a per-call client with no
			// persistent connection to monitor is simply Healthy, the same
			// state per-user auth types are given at connect time.
			if client.State != schemas.MCPConnectionStateDisabled && client.State != schemas.MCPConnectionStateNeedsReauth {
				client.State = schemas.MCPConnectionStateHealthy
			}
			becamePerCall = true
		case wasPerCall && !isPerCall:
			// Per-call -> sticky: there is nothing to release, but a
			// persistent connection needs to be dialed for the first time —
			// handled after the lock is released, below.
			becameSticky = true
		}

		return nil
	}()
	if err != nil {
		return err
	}

	if becamePerCall {
		m.checkerManager.StopChecking(id)
		m.logger.Info("%s MCP client '%s' switched to per-call connections (needs_session_stickiness=false): released its persistent connection", MCPLogPrefix, clientName)
	}
	if becameSticky {
		// connectToMCPClient starts the connection checker itself on
		// success, mirroring every other successful-connect path. On
		// failure it does NOT start one (mirroring EnableClient's own
		// failure handling below) — without this, a failed first dial would
		// leave the client Unstable with nothing ever periodically retrying
		// it, since a per-call client never had a checker running to begin
		// with. Start one explicitly, same as EnableClient does.
		if connErr := m.connectToMCPClient(m.ctx, newConfig); connErr != nil {
			m.mu.Lock()
			alreadyTerminal := false
			if cs, exists := m.clientMap[id]; exists {
				switch cs.State {
				case schemas.MCPConnectionStateDisabled, schemas.MCPConnectionStateNeedsReauth:
					alreadyTerminal = true
				default:
					cs.State = schemas.MCPConnectionStateUnstable
				}
			}
			m.mu.Unlock()

			if !alreadyTerminal {
				isPingAvailable := true
				if newConfig.IsPingAvailable != nil {
					isPingAvailable = *newConfig.IsPingAvailable
				}
				syncInterval := ResolveToolSyncInterval(newConfig, m.checkerManager.GetGlobalInterval())
				checker := NewClientConnectionChecker(m, id, syncInterval, isPingAvailable, m.logger)
				m.checkerManager.StartChecking(checker)
			}

			return fmt.Errorf("client updated, but establishing the shared connection for needs_session_stickiness=true failed: %w: %w", ErrMCPSharedConnectFailedAfterUpdate, connErr)
		}
		m.logger.Info("%s MCP client '%s' switched to a shared connection (needs_session_stickiness=true): established it now", MCPLogPrefix, clientName)
	}

	return nil
}

// UpdateClientCredentials updates auth-related fields (headers) for an existing MCP client by
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
func (m *MCPManager) UpdateClientCredentials(id string, newConfig *schemas.MCPClientConfig) (retErr error) {
	if newConfig == nil {
		return fmt.Errorf("newConfig must not be nil")
	}
	// Hold the per-client reconnect guard for the entire read + long reconnect so
	// UpdateClient/DisableClient cannot mutate ExecutionConfig while a failed reconnect
	// restores the pre-attempt snapshot.
	finish, ok := m.beginExclusiveClientOp(id)
	if !ok {
		return fmt.Errorf("reconnect already in progress for this client")
	}
	defer func() { finish(retErr) }()

	m.mu.RLock()
	client, ok := m.clientMap[id]
	if !ok {
		m.mu.RUnlock()
		return fmt.Errorf("client %s not found", id)
	}
	// Per-call clients have no persistent connection to update — this covers
	// both genuine per-user auth types (always per-call) and a shared HTTP
	// client (oauth/headers) currently running in per-call mode via
	// needs_session_stickiness nil/false. Either way connectToMCPClient below
	// (a real persistent dial) does not apply.
	if client.ExecutionConfig != nil && m.credStore.RequiresPerCallConnection(client.ExecutionConfig) {
		// A client still in PendingVerification is completing its FIRST
		// connection here — e.g. a config.json-bootstrapped OAuth client
		// whose admin just finished the browser consent flow. AddClient's
		// own per-call branch (see its PendingOAuthConfig early-return) is
		// what would normally perform this exact state transition, but it
		// never gets a second chance to run for an already-registered
		// client: the entry was created once, at boot, and parked here.
		// Mirror that same transition now, or this client is stuck showing
		// pending_verification — with no way to retry (the pending OAuth
		// stash this endpoint requires is already cleared) — until a
		// restart re-runs AddClient from scratch and does it correctly.
		if client.State == schemas.MCPConnectionStatePendingVerification {
			m.mu.RUnlock()
			m.mu.Lock()
			if cs, exists := m.clientMap[id]; exists && cs.State == schemas.MCPConnectionStatePendingVerification {
				cs.ExecutionConfig = newConfig
				if len(newConfig.DiscoveredTools) > 0 {
					maps.Copy(cs.ToolMap, newConfig.DiscoveredTools)
					cs.ToolNameMapping = newConfig.DiscoveredToolNameMapping
				}
				cs.State = schemas.MCPConnectionStateHealthy
			}
			m.mu.Unlock()
			// A client with nothing to restore gets its first discovery pass
			// synchronously here — see AddClient's per-call branch (mirrors
			// its own identical comment). Without this, a shared
			// oauth/headers/none client completing its first connection here
			// would sit at Healthy/0-tools until the periodic checker's
			// first tick, which could be minutes away (Start() uses the slow
			// healthyInterval, not the fast Unstable one, for an
			// already-Healthy client). Best-effort: a failure here just
			// means the periodic checker's own retries pick it up shortly.
			if len(newConfig.DiscoveredTools) == 0 {
				if tools, mapping, discErr := m.performAdminToolDiscovery(m.ctx, newConfig); discErr != nil {
					m.logger.Debug("%s Initial per-call tool discovery failed for MCP client '%s': %v — the periodic checker will retry", MCPLogPrefix, newConfig.Name, discErr)
				} else {
					m.SetClientTools(id, tools, mapping)
				}
			}
			// Start the connection checker so this client's tools stay
			// current going forward — mirrors AddClient's per-call branch
			// (see its own comment): without this, nothing else ever
			// revisits this client.
			if id != BifrostMCPClientKey {
				isPingAvailable := true
				if newConfig.IsPingAvailable != nil {
					isPingAvailable = *newConfig.IsPingAvailable
				}
				syncInterval := ResolveToolSyncInterval(newConfig, m.checkerManager.GetGlobalInterval())
				checker := NewClientConnectionChecker(m, id, syncInterval, isPingAvailable, m.logger)
				m.checkerManager.StartChecking(checker)
			}
			return nil
		}
		// Already past that first connection (a plain reauthorize of an
		// already-Healthy client): a per-call client dials fresh on every
		// call and already picks up whatever credential is current in the
		// DB, so there is genuinely nothing left to do — that's a no-op,
		// not a failure, hence the shared "not applicable" sentinel rather
		// than an opaque error.
		m.mu.RUnlock()
		return fmt.Errorf("client uses per-call connections; there is no persistent connection to update: %w", schemas.ErrMCPReconnectNotApplicable)
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

	// connectToMCPClient dials with the merged credentials and swaps them into the
	// existing clientMap entry, replacing the current connection on success.
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
	// First lock: initialize or validate the client entry.
	//
	// Two strategies, chosen by connection type and current liveness:
	//   - Make-before-break (HTTP/SSE with a live connection): the entry keeps
	//     serving tool calls on its current connection for the whole dial
	//     window. The old handles are captured here and torn down only after
	//     the new connection has been swapped in (or on a failed dial).
	//   - Close-first (STDIO, in-process, or no live connection): the old
	//     handles are captured here, the entry is reset in place, and the
	//     handles are closed right after unlocking, before the dial starts.
	//     Two subprocesses coexisting during the dial would double resource
	//     use and break servers holding exclusive resources (port binds,
	//     lockfiles), so STDIO stays close-first.
	//
	// In both cases the existing *MCPClientState is mutated in place, never
	// replaced with a new pointer: concurrent readers holding the pointer must
	// keep observing the live entry rather than a detached copy.
	var oldConn *client.Client
	var oldCancel context.CancelFunc
	makeBeforeBreak := false
	// Captured below and compared by pointer identity at commit/failure time.
	// A RemoveClient + AddClient cycle for the same ID during this dial
	// deletes this entry and stores a fresh *MCPClientState under the same
	// key; without this check a stale attempt would resolve the new pointer
	// by ID alone and clobber the freshly re-added client's connection and
	// tools while leaving its ExecutionConfig untouched.
	var entry *schemas.MCPClientState

	m.mu.Lock()
	if existingClient, exists := m.clientMap[config.ID]; exists {
		// If the client is disabled and the caller is not explicitly re-enabling it
		// (EnableClient sets config.Disabled = false before calling here; ReconnectClient
		// and the health-monitor path leave it as-is), bail out now, before we touch
		// the Disabled entry, so the Disabled state is preserved.
		if existingClient.State == schemas.MCPConnectionStateDisabled && config.Disabled {
			m.mu.Unlock()
			return fmt.Errorf("client %s is disabled", config.Name)
		}

		entry = existingClient

		oldConn = existingClient.Conn
		oldCancel = existingClient.CancelFunc
		existingClient.CancelFunc = nil
		existingClient.Name = config.Name
		existingClient.ExecutionConfig = config

		makeBeforeBreak = oldConn != nil &&
			(config.ConnectionType == schemas.MCPConnectionTypeHTTP || config.ConnectionType == schemas.MCPConnectionTypeSSE)
		if makeBeforeBreak {
			// Leave State, Conn, and the tool maps untouched: the client keeps
			// serving on the old connection until the new one is swapped in.
			if existingClient.ConnectionInfo == nil {
				existingClient.ConnectionInfo = &schemas.MCPClientConnectionInfo{}
			}
			existingClient.ConnectionInfo.Type = config.ConnectionType
		} else {
			// Reset to Disconnected so the API never returns an empty state
			// during connection attempts; it transitions to Connected only on
			// success. The generation bump invalidates any late writer that
			// captured the detached connection (tool syncer, SSE loss callback).
			//
			// ToolMap/ToolNameMapping are deliberately left as last-known-good,
			// not cleared here: the success path below replaces both wholesale
			// after discovery regardless, and failConnectAttempt relies on
			// finding the previous maps still in place if this dial fails —
			// clearing them here would make a second consecutive failed
			// close-first reconnect (STDIO, in-process, or no live connection)
			// lose its last-known tools instead of just the first one.
			existingClient.State = schemas.MCPConnectionStateUnstable
			existingClient.Conn = nil
			existingClient.ConnectionInfo = &schemas.MCPClientConnectionInfo{
				Type: config.ConnectionType,
			}
			existingClient.ConnGeneration++
		}
	} else {
		// Create new client entry with configuration.
		// Initialize State to Disconnected so the API never returns an empty state
		// during connection attempts; it transitions to Connected only on success.
		entry = &schemas.MCPClientState{
			Name:            config.Name,
			ExecutionConfig: config,
			State:           schemas.MCPConnectionStateUnstable,
			ToolMap:         make(map[string]schemas.ChatTool),
			ToolNameMapping: make(map[string]string),
			ConnectionInfo: &schemas.MCPClientConnectionInfo{
				Type: config.ConnectionType,
			},
		}
		m.clientMap[config.ID] = entry
	}
	m.mu.Unlock()

	// Close-first path: tear down the captured handles now, outside the manager
	// lock (a STDIO Close blocks on the subprocess exiting), before the new
	// dial begins.
	if !makeBeforeBreak {
		m.closeConnHandles(oldCancel, oldConn, config.Name)
		oldConn = nil
		oldCancel = nil
	}

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
		transportRetryConfig := ConnectRetryConfig
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
		initRetryConfig := ConnectRetryConfig
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
		// A dead OAuth2 credential (refresh permanently rejected/expired) is not a
		// transient connectivity problem: no amount of automatic reconnecting will
		// fix it, only a human reauthorizing the client will. Land the entry in
		// NeedsReauth instead of the generic Disconnected so the state itself
		// carries that signal. Never clobber Disabled (DisableClient is
		// authoritative, same invariant the health monitor's updateClientState
		// guard preserves).
		needsReauth := gateErr.Error != nil && errors.Is(gateErr.Error.Error, schemas.ErrOAuth2TokenExpired)
		m.failConnectAttempt(entry, config, cancel, externalClient, oldCancel, oldConn, needsReauth)
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
		m.failConnectAttempt(entry, config, cancel, externalClient, oldCancel, oldConn, false)
		return fmt.Errorf("failed to retrieve tools from MCP client %s: %w", config.Name, listToolsErr)
	}

	// Second lock: atomically swap the new connection details and tools into
	// the client entry.
	m.mu.Lock()

	// Verify the entry is still the same one this attempt started with:
	// resolving by ID alone isn't enough, since a RemoveClient + AddClient
	// cycle for this ID during the dial deletes this entry and installs a
	// new pointer under the same key.
	client, exists := m.clientMap[config.ID]
	if !exists || client != entry {
		m.mu.Unlock()
		// Client was removed (or removed and re-added) during connection
		// setup: release both the new handles and any captured old ones to
		// prevent transport/goroutine leaks, without touching the
		// replacement entry.
		m.failConnectAttempt(entry, config, cancel, externalClient, oldCancel, oldConn, false)
		return fmt.Errorf("client %s was removed during connection setup", config.Name)
	}

	// If the client was disabled while we were doing the whole connection process
	// roll back the newly established connection and abort, without overwriting
	// the Disabled state. This is a rare edge case.
	if client.State == schemas.MCPConnectionStateDisabled {
		m.mu.Unlock()
		m.failConnectAttempt(entry, config, cancel, externalClient, oldCancel, oldConn, false)
		m.logger.Debug("%s [%s] Client was disabled during connection setup; rolling back", MCPLogPrefix, config.Name)
		return fmt.Errorf("client %s was disabled during connection setup", config.Name)
	}

	// Store the external client connection and details
	client.Conn = externalClient
	client.ConnectionInfo = connectionInfo
	client.State = schemas.MCPConnectionStateHealthy

	// Store cancel function for SSE and STDIO connections to enable proper cleanup
	if config.ConnectionType == schemas.MCPConnectionTypeSSE || config.ConnectionType == schemas.MCPConnectionTypeSTDIO {
		client.CancelFunc = cancel
	}

	// Replace the tool set wholesale (the entry may still hold the previous
	// connection's tools on the make-before-break path) and store the tool
	// name mapping for execution (sanitized_name -> original_mcp_name).
	client.ToolMap = tools
	client.ToolNameMapping = toolNameMapping

	// Bump the connection generation: anything still bound to the previous
	// connection (a tool-sync tick that snapshotted it, the old SSE
	// OnConnectionLost callback) compares its recorded generation against the
	// entry's and becomes a no-op instead of clobbering the fresh connection.
	client.ConnGeneration++
	swapGeneration := client.ConnGeneration

	m.logger.Debug("%s [%s] Registering %d tools. Client config - ID: %s, Name: %s, IsCodeModeClient: %v", MCPLogPrefix, config.Name, len(tools), config.ID, config.Name, config.IsCodeModeClient)
	m.logger.Info("%s Connected to MCP server '%s'", MCPLogPrefix, config.Name)

	// Release lock BEFORE starting monitors to prevent deadlock
	// (StartMonitoring -> Start() tries to acquire RLock on the same mutex)
	fire := m.toolsChangedCallback(client, config.ID, tools, toolNameMapping)
	m.mu.Unlock()

	// Fired outside the lock — see toolsChangeCallback's field doc. Covers
	// both the initial dial and every reconnect (make-before-break swap):
	// sticky clients have the same "freshly discovered tools never reach the
	// DB" gap per-call clients do. Gated on genuine content change — a
	// reconnect after a network blip almost always rediscovers the exact
	// same tools.
	if fire != nil {
		fire()
	}

	// Register OnConnectionLost hook for SSE connections to detect idle timeouts
	if config.ConnectionType == schemas.MCPConnectionTypeSSE && externalClient != nil {
		externalClient.OnConnectionLost(func(err error) {
			m.handleSSEConnectionLost(config.ID, config.Name, swapGeneration, err)
		})
	}

	// Start the connection checker for the client (skip for the internal
	// bifrost client — an in-process construct with nothing external to
	// check). This now covers both liveness and discovery refresh in one
	// mechanism, so unlike the old separate tool-syncer, a per-client
	// ToolSyncInterval < 0 ("disable discovery refresh") no longer fully
	// disables checking here — liveness/recovery must never be silently
	// disabled by that setting, and the two are no longer separable calls.
	// ResolveToolSyncInterval's 0 (disabled) return still falls back to
	// DefaultConnectionCheckInterval inside NewClientConnectionChecker.
	if config.ID != BifrostMCPClientKey {
		isPingAvailable := true
		if config.IsPingAvailable != nil {
			isPingAvailable = *config.IsPingAvailable
		}
		syncInterval := ResolveToolSyncInterval(config, m.checkerManager.GetGlobalInterval())
		checker := NewClientConnectionChecker(m, config.ID, syncInterval, isPingAvailable, m.logger)
		m.checkerManager.StartChecking(checker)
	}

	// Make-before-break: the swap is committed and the workers re-registered,
	// so the previous connection can now be torn down. In-flight calls that
	// snapshotted the old connection get close-time errors at this instant
	// only, instead of failing for the whole dial window.
	m.closeConnHandles(oldCancel, oldConn, config.Name)

	return nil
}

// handleSSEConnectionLost is the OnConnectionLost callback body for SSE
// clients, registered with the connection generation that was current when
// its connection was swapped in. A callback whose generation no longer
// matches the entry's belongs to an already-replaced connection (its close
// during a reconnect swap can fire the SSE library's loss handler) and must
// not flip the freshly connected client to Disconnected. The Disabled guard
// is unchanged: DisableClient calls Conn.Close() while holding m.mu, the SSE
// library fires OnConnectionLost after the lock is released, by which point
// State is already Disabled and must not be clobbered.
func (m *MCPManager) handleSSEConnectionLost(clientID, clientName string, generation uint64, err error) {
	m.mu.Lock()
	// Never overwrite Disabled (DisableClient is authoritative) or
	// NeedsReauth: the connection can still be the one this generation was
	// swapped in for (ConnGeneration unchanged) at the moment a separate
	// dead-credential detection (e.g. a failed refresh) flips it to
	// NeedsReauth — clobbering that back to the generic Disconnected would
	// hide the "a human needs to reauthorize this" signal.
	client, exists := m.clientMap[clientID]
	if !exists || client.ConnGeneration != generation ||
		client.State == schemas.MCPConnectionStateDisabled ||
		client.State == schemas.MCPConnectionStateNeedsReauth {
		m.mu.Unlock()
		m.logger.Debug("%s Ignoring stale SSE connection-lost callback for MCP server '%s': %v", MCPLogPrefix, clientName, err)
		return
	}
	client.State = schemas.MCPConnectionStateUnstable
	m.mu.Unlock()
	m.logger.Warn("%s SSE connection lost for MCP server '%s': %v", MCPLogPrefix, clientName, err)
}

// closeConnHandles cancels and closes one connection handle pair. Both may be
// nil. Close errors are logged and swallowed since every caller is on a
// teardown path.
func (m *MCPManager) closeConnHandles(cancel context.CancelFunc, conn *client.Client, clientName string) {
	if cancel != nil {
		cancel()
	}
	if conn != nil {
		if err := conn.Close(); err != nil {
			m.logger.Warn("%s Failed to close connection for MCP client '%s': %v", MCPLogPrefix, clientName, err)
		}
	}
}

// failConnectAttempt is the shared teardown for every failure exit of
// connectToMCPClient after the client entry has been prepared. It first
// writes the standard post-failure entry state under m.mu: no connection,
// last-known tool maps left intact, connection info reduced to the bare
// type, a generation bump, and State Disconnected (or NeedsReauth when the
// failure was classified as a dead OAuth2 credential). Only then are the
// half-built new connection handles and the previous connection captured
// for make-before-break closed, outside the lock.
//
// The order is load-bearing: during a make-before-break dial the map entry
// still references the old connection, and other closers (RemoveClient)
// close whatever the entry references under m.mu. Detaching under the lock
// before closing gives any residual double-close a happens-before edge, so
// the transport's own idempotence check suffices; closing first would race
// it. The generation bump invalidates late writers that captured the old
// connection: a tool-sync tick whose list_tools succeeded on the old
// connection mid-dial must not write its results over this entry (toolsync.go
// compares the generation it captured before running against the current one
// and drops the write on mismatch), and a late SSE loss callback from the
// torn-down connection must not overwrite the failure state.
//
// Disabled entries keep the state DisableClient wrote, and an entry removed
// mid-dial leaves the map untouched. entry is the *MCPClientState this
// attempt started with; a RemoveClient + AddClient cycle for the same ID
// during the dial installs a different pointer under the same key, and this
// attempt must not mutate that replacement.
func (m *MCPManager) failConnectAttempt(entry *schemas.MCPClientState, config *schemas.MCPClientConfig, newCancel context.CancelFunc, newConn *client.Client, oldCancel context.CancelFunc, oldConn *client.Client, needsReauth bool) {
	m.mu.Lock()
	var oldState, newState schemas.MCPConnectionState
	stateChanged := false
	if clientState, exists := m.clientMap[config.ID]; exists && clientState == entry && clientState.State != schemas.MCPConnectionStateDisabled {
		clientState.Conn = nil
		clientState.CancelFunc = nil
		// ToolMap/ToolNameMapping are deliberately left as last-known-good, not
		// cleared: GetClientForTool resolves by tool name against this map, and
		// wiping it here made a dead connection indistinguishable from a tool
		// that never existed ("not available or not permitted" instead of
		// "client needs re-authorization" — see prepareToolExecution's
		// State-specific checks, which now give the real reason). Callers that
		// build the advertised tool list (GetToolPerClient) filter on State
		// directly rather than relying on an empty map, so this doesn't cause
		// a disconnected client's tools to keep being offered.
		clientState.ConnectionInfo = &schemas.MCPClientConnectionInfo{
			Type: config.ConnectionType,
		}
		clientState.ConnGeneration++
		oldState = clientState.State
		if needsReauth {
			newState = schemas.MCPConnectionStateNeedsReauth
		} else {
			newState = schemas.MCPConnectionStateUnstable
		}
		stateChanged = oldState != newState
		clientState.State = newState
	}
	cb := m.stateChangeCallback
	m.mu.Unlock()

	m.closeConnHandles(newCancel, newConn, config.Name)
	m.closeConnHandles(oldCancel, oldConn, config.Name)

	// Fired outside the lock — same rationale as ClientConnectionChecker's
	// setState: a registered callback is caller-supplied and may do
	// arbitrary work (including I/O), which must never run while holding
	// m.mu. This is specifically the "reactive" NeedsReauth path (a connect
	// attempt — triggered by a live request, a manual reconnect, or the
	// periodic checker's own reconnect-on-nil-conn branch — classifying its
	// failure as permanent), which unlike CloseAndMarkNeedsReauth has no
	// admin-API call site of its own for a caller to observe the change
	// through.
	if stateChanged && cb != nil {
		cb(config.ID, config.Name, oldState, newState)
	}
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
