package mcp

import (
	"errors"

	"github.com/maximhq/bifrost/core/schemas"
)

// errReconnectInProgress is returned when another exclusive operation already holds the client.
var errReconnectInProgress = errors.New("reconnect already in progress for this client")

// renegotiateAttempts bounds how many busy reconnects renegotiate waits out.
const renegotiateAttempts = 3

// ResolveMaxProtocolVersion returns the newest MCP protocol version a client may negotiate, or ""
// when uncapped. A per-client value wins over the global one; "auto" means uncapped.
func ResolveMaxProtocolVersion(clientConfig *schemas.MCPClientConfig, global string) string {
	version := global
	if clientConfig != nil && clientConfig.MaxProtocolVersion != "" {
		version = clientConfig.MaxProtocolVersion
	}
	if version == schemas.MCPMaxProtocolVersionAuto || !schemas.IsValidMCPMaxProtocolVersion(version) {
		return ""
	}
	return version
}

// maxProtocolVersionFor is ResolveMaxProtocolVersion against the manager's current global setting.
func (m *MCPManager) maxProtocolVersionFor(clientConfig *schemas.MCPClientConfig) string {
	global, _ := m.maxProtocolVersion.Load().(string)
	return ResolveMaxProtocolVersion(clientConfig, global)
}

// needsRenegotiation reports whether a connection at negotiated must reconnect when its cap moves
// from oldMax to newMax ("" = uncapped): it is now above the cap, or the old cap held it back.
func needsRenegotiation(negotiated, oldMax, newMax string) bool {
	if oldMax == newMax || negotiated == "" {
		return false
	}
	if newMax != "" && negotiated > newMax {
		return true
	}
	return negotiated == oldMax && (newMax == "" || newMax > oldMax)
}

// UpdateMaxProtocolVersion replaces the global max protocol version at runtime. Clients that
// follow it renegotiate when the change affects them: per-call clients forget their cached
// version, shared connections reconnect in the background.
func (m *MCPManager) UpdateMaxProtocolVersion(version string) {
	previous, _ := m.maxProtocolVersion.Swap(version).(string)
	oldMax, newMax := ResolveMaxProtocolVersion(nil, previous), ResolveMaxProtocolVersion(nil, version)
	if oldMax == newMax {
		return
	}
	var reconnect []string
	m.mu.RLock()
	for id, state := range m.clientMap {
		config := state.ExecutionConfig
		if id == BifrostMCPClientKey || config == nil || config.MaxProtocolVersion != "" {
			continue
		}
		m.dropPerCallConn(id)
		if state.Conn != nil && state.State != schemas.MCPConnectionStateDisabled && !m.credStore.RequiresPerCallConnection(config) &&
			needsRenegotiation(state.Conn.ProtocolVersion(), oldMax, newMax) {
			reconnect = append(reconnect, id)
		}
	}
	m.mu.RUnlock()
	for _, id := range reconnect {
		m.renegotiate(id)
	}
}

// renegotiate reconnects id in the background under the current cap. A reconnect already running
// is waited out and retried, not dropped: it may have run initialize under the old cap.
func (m *MCPManager) renegotiate(id string) {
	// Pending until a connection is installed; the connection checker retries it until then.
	m.pendingRenegotiation.Store(id, struct{}{})
	go func() {
		var err error
		for range renegotiateAttempts {
			m.AwaitReconnect(id, MCPClientConnectionEstablishTimeout)
			if err = m.ReconnectClient(id); !errors.Is(err, errReconnectInProgress) {
				break
			}
		}
		if err != nil {
			m.logger.Warn("%s [%s] Reconnect after the max protocol version changed failed: %v", MCPLogPrefix, id, err)
		}
	}()
}
