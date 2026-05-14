package mcp

import (
	"errors"
	"fmt"
	"strings"
	"sync"

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
	// Populate top-level ClientName from the prefixed tool name so the gate can
	// attribute short-circuit responses without depending on prefix parsing.
	if request != nil && request.ClientName == "" {
		if toolName := request.GetToolName(); toolName != "" {
			if idx := strings.IndexByte(toolName, '-'); idx > 0 {
				request.ClientName = toolName[:idx]
			}
		}
	}

	// Capture MCPUserOAuthRequiredError out-of-band: runWithPluginPipeline wraps Go errors into
	// a generic BifrostError before PostHooks, which strips typed-error info.
	var oauthErr *schemas.MCPUserOAuthRequiredError

	resp, bErr := m.runWithPluginPipeline(ctx, request, func(preReq *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
		result, opErr := m.ExecuteToolCall(ctx, preReq)
		if opErr != nil {
			errors.As(opErr, &oauthErr)
			return nil, opErr
		}
		if result == nil {
			return nil, fmt.Errorf("tool execution returned nil result")
		}
		return result, nil
	})

	if bErr != nil {
		bErr.ExtraFields.RequestType = requestType
		if oauthErr != nil {
			bErr.ExtraFields.MCPAuthRequired = oauthErr
		}
		return nil, bErr
	}
	return resp, nil
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
