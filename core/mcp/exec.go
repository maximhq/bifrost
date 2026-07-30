package mcp

import (
	"fmt"
	"strings"
	"sync"

	"github.com/mark3labs/mcp-go/client"
	"github.com/maximhq/bifrost/core/schemas"
)

// ============================================================================
// MCP REQUEST POOL
// ============================================================================
//
// Pool for BifrostMCPRequest objects. Owned by the mcp package because these
// requests are only used inside this package — the Bifrost public API just
// delegates to MCPManager's Execute* methods.

var mcpRequestPool = sync.Pool{
	New: func() any {
		return &schemas.BifrostMCPRequest{}
	},
}

// resetMCPRequest zeroes a BifrostMCPRequest for reuse. Must be kept in sync with
// the fields defined on the request struct.
func resetMCPRequest(req *schemas.BifrostMCPRequest) {
	req.RequestType = ""
	req.ClientName = ""
	req.BifrostMCPPingRequest = nil
	req.BifrostMCPListToolsRequest = nil
	req.BifrostMCPExecuteToolRequest = nil
	req.ChatAssistantMessageToolCall = nil
	req.ResponsesToolMessage = nil
}

func getMCPRequest() *schemas.BifrostMCPRequest {
	return mcpRequestPool.Get().(*schemas.BifrostMCPRequest)
}

func releaseMCPRequest(req *schemas.BifrostMCPRequest) {
	resetMCPRequest(req)
	mcpRequestPool.Put(req)
}

// ============================================================================
// EXECUTE-TOOL GATE (matches the pattern of connect/ping/list_tools gates)
// ============================================================================

// executeToolWithHooks runs an MCP tool call through the plugin gate. It is the
// execute-tool counterpart to the connect/ping/list_tools gates. Mirrors the
// short-circuit + PostHook semantics of all other gates by delegating to
// runWithPluginPipeline, then adds two execute-specific touches on the returned BifrostError:
//
//   - stamps ExtraFields.RequestType from the caller-provided RequestType
//   - preserves MCPUserOAuthRequiredError so agent-mode detection still works
//
// requestType is the bifrost-side RequestType (ChatCompletionRequest / ResponsesRequest)
// that error metadata should carry — it isn't the same as request.RequestType.
func (m *MCPManager) executeToolWithHooks(
	ctx *schemas.BifrostContext,
	request *schemas.BifrostMCPRequest,
	requestType schemas.RequestType,
) (*schemas.BifrostMCPResponse, *schemas.BifrostError) {
	if request == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error:          &schemas.ErrorField{Message: "request cannot be nil"},
			ExtraFields:    schemas.BifrostErrorExtraFields{RequestType: requestType},
		}
	}

	// Populate top-level ClientName from the prefixed tool name so the gate can
	// attribute short-circuit responses without depending on prefix parsing.
	if request.ClientName == "" {
		if toolName := request.GetToolName(); toolName != "" {
			if idx := strings.IndexByte(toolName, '-'); idx > 0 {
				request.ClientName = toolName[:idx]
			}
		}
	}

	resp, bErr := m.RunWithPluginPipeline(ctx, request, func(preReq *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
		// Resolve and acquire after PreMCPHook so policy plugins can reject denied
		// MCP clients before any auth or transport work.
		state, conn, release, prepErr := m.prepareToolExecution(ctx, preReq)
		if prepErr != nil {
			return nil, prepErr
		}
		defer release()

		// state == nil signals a code-mode tool: pass nil conn/config/mapping and
		// ToolsManager.ExecuteTool routes directly to CodeMode.
		var executionConfig *schemas.MCPClientConfig
		var toolNameMapping map[string]string
		if state != nil {
			executionConfig = state.ExecutionConfig
			toolNameMapping = state.ToolNameMapping
		}

		result, opErr := m.toolsManager.ExecuteTool(ctx, preReq, conn, executionConfig, toolNameMapping)
		if opErr != nil {
			return nil, opErr
		}
		if result == nil {
			return nil, fmt.Errorf("tool execution returned nil result")
		}
		return result, nil
	})

	if bErr != nil {
		bErr.ExtraFields.RequestType = requestType
		return nil, bErr
	}
	return resp, nil
}

// prepareToolExecution resolves the tool to its owning MCP client and
// acquires a connection. Returns (state, conn, release, err):
//   - For regular MCP tools: state non-nil, conn is the live transport, release
//     must be called by the caller (defer).
//   - For code-mode tools: state nil, conn nil, release is a no-op. The caller
//     forwards nil conn/config/mapping to ToolsManager.ExecuteTool which
//     dispatches via the CodeMode implementation.
//
// Errors here mean the call should NOT run — neither the envelope plugin
// gate nor the wire op. Typed errors (e.g. *MCPUserOAuthRequiredError)
// propagate so the caller can stamp BifrostError.ExtraFields.
func (m *MCPManager) prepareToolExecution(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.MCPClientState, *client.Client, func(), error) {
	toolName := request.GetToolName()
	if toolName == "" {
		return nil, nil, nil, fmt.Errorf("tool call missing function name")
	}

	// Code-mode tools have no upstream client — skip client lookup.
	codeMode := m.toolsManager.GetCodeMode()
	if codeMode != nil && codeMode.IsCodeModeTool(toolName) {
		return nil, nil, func() {}, nil
	}

	state := m.GetClientForTool(toolName)
	if state == nil {
		return nil, nil, nil, fmt.Errorf("tool '%s' is not available or not permitted", toolName)
	}
	clientName := state.ExecutionConfig.Name
	// Enforce the same filters that GetToolPerClient applies for tool
	// discovery, in the same order. Without these a caller could invoke a
	// tool by name that was deliberately hidden from the tool list.
	//
	//  1. Client lifecycle — a disabled client is not usable.
	//  2. Client allow-list — request-context MCPContextKeyIncludeClients.
	//  3. Tool allow-list   — client-level ToolsToExecute (most restrictive).
	//  4. Tool narrowing    — request-context MCPContextKeyIncludeTools.
	if state.State == schemas.MCPConnectionStateDisabled {
		return nil, nil, nil, fmt.Errorf("tool '%s' is not permitted (client %s is disabled)", toolName, clientName)
	}
	var includeClients []string
	if v, ok := ctx.Value(schemas.MCPContextKeyIncludeClients).([]string); ok {
		includeClients = v
	}
	if !shouldIncludeClient(clientName, includeClients, m.logger) {
		return nil, nil, nil, fmt.Errorf("tool '%s' is not permitted (client %s is not in request-context include list)", toolName, clientName)
	}
	if shouldSkipToolForConfig(toolName, state.ExecutionConfig) {
		return nil, nil, nil, fmt.Errorf("tool '%s' is not permitted (not in client's ToolsToExecute allow-list)", toolName)
	}
	if shouldSkipToolForRequest(ctx, clientName, toolName) {
		return nil, nil, nil, fmt.Errorf("tool '%s' is not permitted (filtered by request context)", toolName)
	}
	conn, release, err := m.AcquireClientConn(ctx, state)
	if err != nil {
		return nil, nil, nil, err
	}
	return state, conn, release, nil
}

// executeToolForAgent is the agent-mode-facing helper. The agent loop expects a
// plain (response, error) signature and doesn't need rich BifrostError fields,
// so we collapse them. MCPUserOAuthRequiredError is returned directly when present
// so agent mode can detect it via errors.As.
func (m *MCPManager) executeToolForAgent(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context cannot be nil")
	}
	if request == nil {
		return nil, fmt.Errorf("request cannot be nil")
	}

	// Derive bifrost RequestType from the MCP request type (only execute-tool variants
	// are valid in the agent loop).
	var requestType schemas.RequestType
	switch request.RequestType {
	case schemas.MCPRequestTypeChatToolCall:
		requestType = schemas.ChatCompletionRequest
	case schemas.MCPRequestTypeResponsesToolCall:
		requestType = schemas.ResponsesRequest
	default:
		return nil, fmt.Errorf("unsupported MCP request type for agent: %s", request.RequestType)
	}

	resp, bErr := m.executeToolWithHooks(ctx, request, requestType)
	if bErr != nil {
		// Surface the typed OAuth error so agent mode can react to it.
		if bErr.ExtraFields.MCPAuthRequired != nil {
			return nil, bErr.ExtraFields.MCPAuthRequired
		}
		return nil, fmt.Errorf("tool execution failed: %s", bErr.GetErrorString())
	}
	return resp, nil
}

// ============================================================================
// PUBLIC EXECUTE-TOOL ENTRY POINTS
// ============================================================================

// ExecuteChatTool executes an MCP tool call and returns the result as a chat message.
// This is the canonical entry point for manual MCP tool execution in Chat format.
// Bifrost.ExecuteChatMCPTool delegates here.
func (m *MCPManager) ExecuteChatTool(ctx *schemas.BifrostContext, toolCall *schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError) {
	if toolCall == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error:          &schemas.ErrorField{Message: "toolCall cannot be nil"},
			ExtraFields:    schemas.BifrostErrorExtraFields{RequestType: schemas.ChatCompletionRequest},
		}
	}

	mcpRequest := getMCPRequest()
	mcpRequest.RequestType = schemas.MCPRequestTypeChatToolCall
	mcpRequest.ChatAssistantMessageToolCall = toolCall
	defer releaseMCPRequest(mcpRequest)

	result, bErr := m.executeToolWithHooks(ctx, mcpRequest, schemas.ChatCompletionRequest)
	if bErr != nil {
		return nil, bErr
	}
	if result == nil || result.ChatMessage == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error:          &schemas.ErrorField{Message: "MCP tool execution returned nil chat message"},
			ExtraFields:    schemas.BifrostErrorExtraFields{RequestType: schemas.ChatCompletionRequest},
		}
	}
	return result.ChatMessage, nil
}

// ExecuteResponsesTool executes an MCP tool call and returns the result as a responses
// message. Bifrost.ExecuteResponsesMCPTool delegates here.
func (m *MCPManager) ExecuteResponsesTool(ctx *schemas.BifrostContext, toolCall *schemas.ResponsesToolMessage) (*schemas.ResponsesMessage, *schemas.BifrostError) {
	if toolCall == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error:          &schemas.ErrorField{Message: "toolCall cannot be nil"},
			ExtraFields:    schemas.BifrostErrorExtraFields{RequestType: schemas.ResponsesRequest},
		}
	}

	mcpRequest := getMCPRequest()
	mcpRequest.RequestType = schemas.MCPRequestTypeResponsesToolCall
	mcpRequest.ResponsesToolMessage = toolCall
	defer releaseMCPRequest(mcpRequest)

	result, bErr := m.executeToolWithHooks(ctx, mcpRequest, schemas.ResponsesRequest)
	if bErr != nil {
		return nil, bErr
	}
	if result == nil || result.ResponsesMessage == nil {
		return nil, &schemas.BifrostError{
			IsBifrostError: false,
			Error:          &schemas.ErrorField{Message: "MCP tool execution returned nil responses message"},
			ExtraFields:    schemas.BifrostErrorExtraFields{RequestType: schemas.ResponsesRequest},
		}
	}
	return result.ResponsesMessage, nil
}
