package mcp

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
)

// Upstream tools/list_changed: a persistent connection to an upstream that declares
// tools.listChanged re-lists its tools as soon as the upstream says they changed, instead of
// waiting for the connection checker. Per-call clients hold no connection to listen on and keep
// relying on the checker.

// Bounds the wait between attempts to reopen a 2026-07-28 subscriptions/listen stream.
const (
	listenRetryInitial = time.Second
	listenRetryMax     = 30 * time.Second
)

// listChanges reports whether the upstream will notify a change to its tools, resources or prompts.
func listChanges(result *mcp.InitializeResult) bool {
	if result == nil {
		return false
	}
	caps := result.Capabilities
	return (caps.Tools != nil && caps.Tools.ListChanged) ||
		(caps.Resources != nil && caps.Resources.ListChanged) ||
		(caps.Prompts != nil && caps.Prompts.ListChanged)
}

// needsLegacyListenStream reports whether a connection on version needs the 2025-era GET stream to
// hear tools/list_changed.
func needsLegacyListenStream(version string, result *mcp.InitializeResult) bool {
	return version != "" && !mcp.IsModernProtocol(version) && listChanges(result)
}

// withLegacyListenStream returns the HTTP client a connection should keep. A 2025-era upstream only
// delivers tools/list_changed on the GET stream, which mcp-go opens only for a transport built for
// it, so such a connection is dialed once more, pinned to the negotiated version, and replaces conn.
// If that dial fails, conn is kept and tool changes wait for the connection checker.
func (m *MCPManager) withLegacyListenStream(ctx context.Context, config *schemas.MCPClientConfig, overrides *schemas.BifrostMCPConnectRequest, request mcp.InitializeRequest, conn *client.Client, result *mcp.InitializeResult) (*client.Client, *mcp.InitializeResult) {
	version := conn.ProtocolVersion()
	if !needsLegacyListenStream(version, result) {
		return conn, result
	}
	listening, listenResult, err := m.dialLegacyListening(ctx, config, overrides, request, version)
	if err != nil {
		m.logger.Warn("%s [%s] Could not open the notification stream, tool changes will wait for the connection checker: %v", MCPLogPrefix, config.Name, err)
		return conn, result
	}
	m.closeConnHandles(nil, conn, config.Name)
	return listening, listenResult
}

// dialLegacyListening opens a second HTTP connection pinned to version with the GET stream open.
// The stream lives as long as the client (started on the manager context); only the handshake is
// bounded by ctx.
func (m *MCPManager) dialLegacyListening(ctx context.Context, config *schemas.MCPClientConfig, overrides *schemas.BifrostMCPConnectRequest, request mcp.InitializeRequest, version string) (*client.Client, *mcp.InitializeResult, error) {
	listening, _, err := m.createHTTPConnection(m.ctx, config, overrides, version)
	if err != nil {
		return nil, nil, err
	}
	if err := listening.Start(m.ctx); err != nil {
		m.closeConnHandles(nil, listening, config.Name)
		return nil, nil, fmt.Errorf("failed to start listening transport: %w", err)
	}
	request.Params.ProtocolVersion = version
	result, err := initializeClient(ctx, listening, request, m.maxProtocolVersionFor(config))
	if err != nil {
		m.closeConnHandles(nil, listening, config.Name)
		return nil, nil, fmt.Errorf("failed to initialize listening connection: %w", err)
	}
	return listening, result, nil
}

// toolChangeListener re-lists the client's tools whenever its connection reports a tool list change.
// Every 2025-era transport delivers the notification to the connection's handlers already (SSE and
// stdio on their stream, HTTP on the GET stream); 2026-07-28 needs a subscriptions/listen stream.
// Both stop with the connection they were registered for.
//
// It is registered before the connect's first tools/list, so a change announced while that list is
// in flight is held and re-listed once the connection is installed, instead of reaching no handler.
// On 2026-07-28 the listen stream also re-lists when the server acknowledges it: a change made
// before the stream was registered, or while it was down, was announced to nobody.
type toolChangeListener struct {
	m          *MCPManager
	clientID   string
	conn       *client.Client
	generation atomic.Uint64 // 0 until the connection is installed
	missed     atomic.Bool   // a change arrived before generation was set
}

// newToolChangeListener starts collecting conn's tool change notifications for clientID.
func (m *MCPManager) newToolChangeListener(clientID string, conn *client.Client) *toolChangeListener {
	l := &toolChangeListener{m: m, clientID: clientID, conn: conn}
	conn.OnNotification(func(n mcp.JSONRPCNotification) {
		switch n.Method {
		case string(mcp.MethodNotificationToolsListChanged), string(mcp.MethodNotificationResourcesListChanged),
			string(mcp.MethodNotificationPromptsListChanged), mcp.MethodNotificationSubscriptionsAcknowledged:
			l.missed.Store(true)
			l.flush()
		}
	})
	return l
}

// activate binds the listener to the generation conn was installed at, re-lists for any change held
// until now, and opens the 2026-07-28 listen stream. Called after the connection checker starts.
func (l *toolChangeListener) activate(clientName string, generation uint64) {
	l.generation.Store(generation)
	l.flush()
	if mcp.IsModernProtocol(l.conn.ProtocolVersion()) {
		go l.m.listenForToolChanges(l.clientID, clientName, l.conn, generation)
	}
}

// flush re-lists once per held change after activation. Notification and activation both set
// their side first, so whichever runs last sees both and does the re-list.
func (l *toolChangeListener) flush() {
	if generation := l.generation.Load(); generation != 0 && l.missed.Swap(false) {
		l.m.refreshOnUpstreamChange(l.clientID, l.conn, generation)
	}
}

// listenForToolChanges keeps a subscriptions/listen stream open on conn until conn is replaced or
// closed, reopening it with backoff when it drops.
func (m *MCPManager) listenForToolChanges(clientID, clientName string, conn *client.Client, generation uint64) {
	caps := conn.GetServerCapabilities()
	filter := mcp.SubscriptionFilter{
		ToolsListChanged:     caps.Tools != nil && caps.Tools.ListChanged,
		ResourcesListChanged: caps.Resources != nil && caps.Resources.ListChanged,
		PromptsListChanged:   caps.Prompts != nil && caps.Prompts.ListChanged,
	}
	wait := listenRetryInitial
	for m.isCurrentConn(clientID, conn, generation) {
		opened := time.Now()
		err := conn.Listen(m.ctx, filter)
		if m.ctx.Err() != nil || !m.isCurrentConn(clientID, conn, generation) {
			return
		}
		m.logger.Debug("%s [%s] tools/list_changed stream ended, reopening: %v", MCPLogPrefix, clientName, err)
		if time.Since(opened) > listenRetryMax {
			wait = listenRetryInitial // the stream had been healthy; start the backoff over
		}
		select {
		case <-time.After(wait):
		case <-m.ctx.Done():
			return
		}
		wait = min(wait*2, listenRetryMax)
	}
}

// isCurrentConn reports whether clientID still holds conn, the connection swapped in at generation.
// Both are checked: disable and needs_reauth clear Conn without a new generation, and a client
// removed and added again can reach the old generation number.
func (m *MCPManager) isCurrentConn(clientID string, conn *client.Client, generation uint64) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	state, ok := m.clientMap[clientID]
	return ok && state.Conn == conn && state.ConnGeneration == generation
}

// refreshOnUpstreamChange re-lists clientID's tools by kicking its connection checker, which owns
// rediscovery: the re-list is serialized with the checker's own checks, and a burst of
// notifications collapses into one check plus at most one follow-up. Notifications from a
// replaced connection are ignored.
func (m *MCPManager) refreshOnUpstreamChange(clientID string, conn *client.Client, generation uint64) {
	if m.checkerManager != nil && m.isCurrentConn(clientID, conn, generation) {
		m.checkerManager.Kick(clientID)
	}
}
