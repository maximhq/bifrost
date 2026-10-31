//go:build !tinygo && !wasm

package mcp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// A client with per_user_discovery on serves each caller the tools their own
// credential discovered. Until a caller has a discovery for it, they are offered
// one synthetic tool in place of the client's tools, "<client>-connect": calling
// it discovers with their credential, or hands back the link to sign in when
// they have none. It never reaches the upstream, which does not know it.

// connectToolSuffix ends the name of every connect tool. Client names hold no
// hyphen, so the client a connect tool stands in for is the name before it.
const connectToolSuffix = "-connect"

// addedConnectToolsKey holds, on a request's context, the names of the connect
// tools ("<client>-connect") that ParseAndAddToolsToRequest put into the request's
// tools, as a []string. It is set only once at least one was injected: the caller
// is a user or virtual key with no discovery for a client with per_user_discovery
// on that they can reach, and the request did not already carry a tool of that
// name. On every other request it is absent.
//
// An agent loop runs all its turns on one request context, so each later turn
// reads it to take back a connect tool it no longer offers as one, and that name
// leaves the list (see dropStaleConnectTools). A new request starts with a new
// context, so the list never carries from one request to the next.
const addedConnectToolsKey schemas.BifrostContextKey = "bifrost-mcp-added-connect-tools"

// connectToolName returns the name of clientName's connect tool.
func connectToolName(clientName string) string {
	return clientName + connectToolSuffix
}

// connectTool returns the connect tool a caller is offered for config.
func connectTool(config *schemas.MCPClientConfig) schemas.ChatTool {
	description := fmt.Sprintf("Connect the user's account to %[1]s to use its tools. "+
		"If the user has not signed in to %[1]s yet, this returns a link for them to open; give them the link exactly as returned. "+
		"Otherwise it connects right away, and %[1]s's tools are available from your next turn.", config.Name)
	return schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        connectToolName(config.Name),
			Description: schemas.Ptr(description),
			Parameters: &schemas.ToolFunctionParameters{
				Type:       "object",
				Properties: schemas.NewOrderedMap(),
				Required:   []string{},
			},
		},
	}
}

// connectCaller reports whether ctx names a caller a per-user discovery is kept
// for: a user or a virtual key, resolved the way the per-user credential lookup
// resolves them. A session-only or anonymous caller, and the manager's own
// context, have none.
func connectCaller(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	bctx, ok := ctx.(*schemas.BifrostContext)
	if !ok {
		// Values only: WithoutCancel keeps the wrapper from starting a
		// cancellation watcher for a context it reads a few keys from.
		bctx = schemas.NewBifrostContext(context.WithoutCancel(ctx), schemas.NoDeadline)
	}
	mode := bctx.MCPAuthMode()
	if mode != schemas.MCPAuthModeUser && mode != schemas.MCPAuthModeVK {
		return false
	}
	return bctx.MCPIdentity(mode) != ""
}

// connectAccessible reports whether the caller on ctx may see and call state's
// connect tool. The client itself must be reachable: enabled, in the request's
// client list, executing some tool, and named by the request's tool list when
// there is one. Which of its tools are allowed does not matter, since a caller
// cannot know them before connecting, and a list naming specific tools would
// otherwise leave connect, and so every one of them, out of reach.
func connectAccessible(ctx context.Context, state *schemas.MCPClientState, logger schemas.Logger) bool {
	config := state.ExecutionConfig
	if state.State == schemas.MCPConnectionStateDisabled {
		return false
	}
	includeClients, _ := ctx.Value(schemas.MCPContextKeyIncludeClients).([]string)
	if !shouldIncludeClient(config.Name, includeClients, logger) {
		return false
	}
	if config.ToolsToExecute == nil || config.ToolsToExecute.IsEmpty() {
		return false
	}
	if includeTools, ok := ctx.Value(schemas.MCPContextKeyIncludeTools).([]string); ok {
		prefix := config.Name + "-"
		return slices.ContainsFunc(includeTools, func(entry string) bool { return strings.HasPrefix(entry, prefix) })
	}
	return true
}

// GetConnectTools returns the connect tools the caller on ctx is offered: one
// per client with per_user_discovery on that they can reach and have no
// discovery for, sorted by client name. A caller with no user or virtual-key
// identity is offered none, so such a client stays out of their sight.
func (m *MCPManager) GetConnectTools(ctx context.Context) []schemas.ChatTool {
	// Snapshot the candidates under the lock and read the store outside it (see
	// GetToolPerClient). A manager with no such client allocates nothing.
	var candidates []*schemas.MCPClientState
	m.mu.RLock()
	for _, client := range m.clientMap {
		if perUserDiscoveryOn(client.ExecutionConfig) {
			snapshot := *client
			candidates = append(candidates, &snapshot)
		}
	}
	m.mu.RUnlock()
	if len(candidates) == 0 || !connectCaller(ctx) {
		return nil
	}
	slices.SortFunc(candidates, func(a, b *schemas.MCPClientState) int {
		return strings.Compare(a.ExecutionConfig.Name, b.ExecutionConfig.Name)
	})
	var tools []schemas.ChatTool
	for _, client := range candidates {
		if !connectAccessible(ctx, client, m.logger) || m.discoveryStore.GetDiscovery(ctx, client.ExecutionConfig) != nil {
			continue
		}
		tools = append(tools, connectTool(client.ExecutionConfig))
	}
	return tools
}

// ConnectToolClient returns the client whose connect tool toolName is for the
// caller on ctx, or nil. A client's upstream may itself list a tool named
// "connect": once the caller's discovery holds it, the name is that tool's.
func (m *MCPManager) ConnectToolClient(ctx context.Context, toolName string) *schemas.MCPClientState {
	state := m.connectCandidate(toolName)
	if state == nil {
		return nil
	}
	if _, upstream := m.discoveryStore.GetDiscovery(ctx, state.ExecutionConfig).GetTools()[toolName]; upstream {
		return nil
	}
	return state
}

// connectCandidate returns the client with per_user_discovery on whose connect
// tool toolName is named like, or nil, without reading any caller's discovery.
func (m *MCPManager) connectCandidate(toolName string) *schemas.MCPClientState {
	clientName, ok := strings.CutSuffix(toolName, connectToolSuffix)
	if !ok || clientName == "" {
		return nil
	}
	state := m.GetClientByName(clientName)
	if state == nil || !perUserDiscoveryOn(state.ExecutionConfig) {
		return nil
	}
	return state
}

// runConnect answers a call to state's connect tool for the caller on ctx. Every
// outcome a caller can act on is a normal tool result, including the sign-in
// link: an error that reaches agent mode as a missing credential stops the whole
// loop with a 401, which is what connect exists to replace. Errors are left for
// what a caller cannot fix by connecting.
func (m *MCPManager) runConnect(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest, state *schemas.MCPClientState) (*schemas.BifrostMCPResponse, error) {
	config := state.ExecutionConfig
	if !connectCaller(ctx) {
		return nil, fmt.Errorf("%s keeps tools per caller and needs a signed-in user or a virtual key to connect", config.Name)
	}
	if !connectAccessible(ctx, state, m.logger) {
		return nil, fmt.Errorf("tool '%s' is not available or not permitted", connectToolName(config.Name))
	}

	var text string
	if m.discoveryStore.GetDiscovery(ctx, config) != nil {
		text = fmt.Sprintf("Already connected to %s; its tools are in your tool list.", config.Name)
	} else {
		count, err := m.discoverForCaller(ctx, state)
		var authRequired *schemas.MCPAuthRequiredError
		switch {
		case errors.As(err, &authRequired):
			text = authRequired.Message
			if text == "" {
				text = fmt.Sprintf("Sign-in to %s is required before connecting.", config.Name)
			}
		case err != nil:
			return nil, err
		default:
			text = fmt.Sprintf("Connected to %s: %d tools are available from your next turn.", config.Name, count)
		}
	}
	return connectResponse(request, config.Name, text)
}

// discoverForCaller discovers state's tools, resources and prompts with the
// credential of the caller on ctx and stores them as that caller's discovery,
// returning how many tools it found. The connection is the one a tool call
// opens, so the credential, the connect plugin gate and the identity the
// discovery is stored under all come from the same ctx. A caller with no
// credential gets the resolver's *MCPAuthRequiredError, before anything is
// dialed.
func (m *MCPManager) discoverForCaller(ctx *schemas.BifrostContext, state *schemas.MCPClientState) (int, error) {
	config := state.ExecutionConfig
	if !m.credStore.RequiresPerCallConnection(config) {
		return 0, fmt.Errorf("%s does not connect per caller", config.Name)
	}
	conn, release, instructions, err := m.acquireClientConn(ctx, state)
	if err != nil {
		return 0, err
	}
	defer release()

	listCtx, cancel := context.WithTimeout(ctx, ConnectionCheckTimeout)
	defer cancel()
	tools, mapping, err := m.runListToolsWithHooks(listCtx, conn, config.Name)
	if err != nil {
		return 0, fmt.Errorf("failed to list the tools of MCP client %s: %w", config.Name, err)
	}
	precomputeToolSerialization(tools)
	discovery := &schemas.MCPDiscovery{
		Tools:           tools,
		ToolNameMapping: mapping,
		Instructions:    instructions,
		DiscoveredAt:    time.Now(),
	}
	// The same connection lists the caller's resources and prompts. The caller had no discovery,
	// so a list that fails leaves that list empty rather than keeping an older one.
	_ = m.listResourcesAndPrompts(ctx, ConnectionCheckTimeout, conn, config.Name, conn.GetServerCapabilities(), discovery, nil)
	if err := m.discoveryStore.SetDiscovery(ctx, config, discovery); err != nil {
		return 0, fmt.Errorf("failed to store the tools of MCP client %s: %w", config.Name, err)
	}
	return len(tools), nil
}

// connectResponse wraps a connect call's text in a tool result of the request's
// format.
func connectResponse(request *schemas.BifrostMCPRequest, clientName, text string) (*schemas.BifrostMCPResponse, error) {
	extraFields := schemas.BifrostMCPResponseExtraFields{ClientName: clientName, ToolName: "connect"}
	switch request.RequestType {
	case schemas.MCPRequestTypeChatToolCall:
		if request.ChatAssistantMessageToolCall == nil {
			return nil, fmt.Errorf("tool call cannot be nil")
		}
		return &schemas.BifrostMCPResponse{
			ChatMessage: createToolResultMessage(*request.ChatAssistantMessageToolCall, text, nil),
			ExtraFields: extraFields,
		}, nil
	case schemas.MCPRequestTypeResponsesToolCall:
		if request.ResponsesToolMessage == nil {
			return nil, fmt.Errorf("ResponsesToolMessage cannot be nil for ResponsesToolCall request type")
		}
		toolCall := request.ResponsesToolMessage.ToChatAssistantMessageToolCall()
		if toolCall == nil {
			return nil, fmt.Errorf("failed to convert Responses tool message to Chat format")
		}
		message := createToolResultMessage(*toolCall, text, nil).ToResponsesToolMessage()
		if message == nil {
			return nil, fmt.Errorf("failed to convert tool result to Responses format")
		}
		return &schemas.BifrostMCPResponse{ResponsesMessage: message, ExtraFields: extraFields}, nil
	default:
		return nil, fmt.Errorf("invalid request type: %s", request.RequestType)
	}
}
