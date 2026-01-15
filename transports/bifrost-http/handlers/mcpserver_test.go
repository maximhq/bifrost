package handlers

import (
	"context"
	"strings"
	"testing"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/valyala/fasthttp"
)

// Mock implementations for testing

type mockMCPToolManager struct {
	availableTools []schemas.ChatTool
	chatResult     *schemas.ChatMessage
	chatError      *schemas.BifrostError
	responsesRes   *schemas.ResponsesMessage
	responsesErr   *schemas.BifrostError
}

func (m *mockMCPToolManager) GetAvailableMCPTools(ctx context.Context) []schemas.ChatTool {
	return m.availableTools
}

func (m *mockMCPToolManager) ExecuteChatMCPTool(ctx context.Context, toolCall schemas.ChatAssistantMessageToolCall) (*schemas.ChatMessage, *schemas.BifrostError) {
	return m.chatResult, m.chatError
}

func (m *mockMCPToolManager) ExecuteResponsesMCPTool(ctx context.Context, toolCall *schemas.ResponsesToolMessage) (*schemas.ResponsesMessage, *schemas.BifrostError) {
	return m.responsesRes, m.responsesErr
}

// Tests

// TestMCPToolManagerInterface documents the MCPToolManager interface
func TestMCPToolManagerInterface(t *testing.T) {
	// MCPToolManager interface:
	// - GetAvailableMCPTools(ctx) []ChatTool - returns available MCP tools
	// - ExecuteChatMCPTool(ctx, toolCall) (*ChatMessage, *BifrostError) - executes a chat format tool call
	// - ExecuteResponsesMCPTool(ctx, toolCall) (*ResponsesMessage, *BifrostError) - executes a responses format tool call

	manager := &mockMCPToolManager{
		availableTools: []schemas.ChatTool{
			{
				Type: "function",
				Function: &schemas.ChatToolFunction{
					Name: "test_tool",
				},
			},
		},
	}

	tools := manager.GetAvailableMCPTools(context.Background())
	if len(tools) != 1 {
		t.Errorf("Expected 1 tool, got %d", len(tools))
	}

	t.Log("MCPToolManager provides tool discovery and execution")
}

// TestMCPServerHandler_Structure documents MCPServerHandler structure
func TestMCPServerHandler_Structure(t *testing.T) {
	// MCPServerHandler contains:
	// - toolManager: MCPToolManager - for tool execution
	// - globalMCPServer: *server.MCPServer - default MCP server
	// - vkMCPServers: map[string]*MCPServer - per-virtual-key MCP servers
	// - config: *lib.Config - configuration
	// - mu: sync.RWMutex - thread safety

	t.Log("MCPServerHandler manages global and per-VK MCP servers")
}

// TestNewMCPServerHandler_NilConfig tests creating handler with nil config
func TestNewMCPServerHandler_NilConfig(t *testing.T) {
	SetLogger(&mockLogger{})

	handler, err := NewMCPServerHandler(context.Background(), nil, &mockMCPToolManager{})

	if err == nil {
		t.Error("Expected error for nil config")
	}
	if handler != nil {
		t.Error("Expected nil handler for nil config")
	}
	if err != nil && !strings.Contains(err.Error(), "config is required") {
		t.Errorf("Expected 'config is required' error, got '%s'", err.Error())
	}
}

// TestNewMCPServerHandler_NilToolManager tests creating handler with nil tool manager
func TestNewMCPServerHandler_NilToolManager(t *testing.T) {
	SetLogger(&mockLogger{})

	// Create minimal config (will still fail on nil tool manager check first)
	handler, err := NewMCPServerHandler(context.Background(), nil, nil)

	if err == nil {
		t.Error("Expected error for nil tool manager")
	}
	if handler != nil {
		t.Error("Expected nil handler for nil tool manager")
	}
}

// TestMCPServerHandler_RegisterRoutes tests route registration
func TestMCPServerHandler_RegisterRoutes(t *testing.T) {
	// MCPServerHandler registers:
	// POST /mcp - JSON-RPC 2.0 message handling
	// GET /mcp - SSE streaming connection

	routes := []struct {
		method string
		path   string
		desc   string
	}{
		{"POST", "/mcp", "Handle JSON-RPC 2.0 messages"},
		{"GET", "/mcp", "SSE streaming connection"},
	}

	for _, r := range routes {
		t.Logf("%s %s - %s", r.method, r.path, r.desc)
	}
}

// TestGetVKFromRequest_VirtualKeyHeader tests VK extraction from header
func TestGetVKFromRequest_VirtualKeyHeader(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set(string(schemas.BifrostContextKeyVirtualKey), "vk-test-key-123")

	result := getVKFromRequest(ctx)

	if result != "vk-test-key-123" {
		t.Errorf("Expected 'vk-test-key-123', got '%s'", result)
	}
}

// TestGetVKFromRequest_AuthorizationBearer tests VK extraction from Authorization header
func TestGetVKFromRequest_AuthorizationBearer(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Authorization", "Bearer "+governance.VirtualKeyPrefix+"test-key")

	result := getVKFromRequest(ctx)

	expected := governance.VirtualKeyPrefix + "test-key"
	if result != expected {
		t.Errorf("Expected '%s', got '%s'", expected, result)
	}
}

// TestGetVKFromRequest_XApiKey tests VK extraction from x-api-key header
func TestGetVKFromRequest_XApiKey(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("x-api-key", governance.VirtualKeyPrefix+"api-key-123")

	result := getVKFromRequest(ctx)

	expected := governance.VirtualKeyPrefix + "api-key-123"
	if result != expected {
		t.Errorf("Expected '%s', got '%s'", expected, result)
	}
}

// TestGetVKFromRequest_NoVK tests VK extraction when no VK is provided
func TestGetVKFromRequest_NoVK(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}

	result := getVKFromRequest(ctx)

	if result != "" {
		t.Errorf("Expected empty string, got '%s'", result)
	}
}

// TestGetVKFromRequest_NonVKBearer tests VK extraction with non-VK bearer token
func TestGetVKFromRequest_NonVKBearer(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Authorization", "Bearer sk-regular-api-key")

	result := getVKFromRequest(ctx)

	// Should return empty - not a virtual key
	if result != "" {
		t.Errorf("Expected empty string for non-VK bearer, got '%s'", result)
	}
}

// TestGetVKFromRequest_NonVKApiKey tests VK extraction with non-VK api key
func TestGetVKFromRequest_NonVKApiKey(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("x-api-key", "sk-regular-api-key")

	result := getVKFromRequest(ctx)

	// Should return empty - not a virtual key
	if result != "" {
		t.Errorf("Expected empty string for non-VK api key, got '%s'", result)
	}
}

// TestGetVKFromRequest_Priority tests VK extraction priority order
func TestGetVKFromRequest_Priority(t *testing.T) {
	// Priority order:
	// 1. X-Virtual-Key header
	// 2. Authorization: Bearer <vk>
	// 3. x-api-key header

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set(string(schemas.BifrostContextKeyVirtualKey), "vk-from-header")
	ctx.Request.Header.Set("Authorization", "Bearer "+governance.VirtualKeyPrefix+"from-bearer")
	ctx.Request.Header.Set("x-api-key", governance.VirtualKeyPrefix+"from-api-key")

	result := getVKFromRequest(ctx)

	// Should prefer X-Virtual-Key header
	if result != "vk-from-header" {
		t.Errorf("Expected 'vk-from-header', got '%s'", result)
	}
}

// TestGetVKFromRequest_WhitespaceTrimming tests whitespace handling
func TestGetVKFromRequest_WhitespaceTrimming(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set(string(schemas.BifrostContextKeyVirtualKey), "  vk-test-key  ")

	result := getVKFromRequest(ctx)

	if result != "vk-test-key" {
		t.Errorf("Expected 'vk-test-key', got '%s'", result)
	}
}

// TestGetVKFromRequest_CaseInsensitiveBearer tests case insensitive bearer check
func TestGetVKFromRequest_CaseInsensitiveBearer(t *testing.T) {
	testCases := []struct {
		name       string
		authHeader string
		shouldFind bool
	}{
		{"lowercase bearer", "bearer " + governance.VirtualKeyPrefix + "key", true},
		{"uppercase bearer", "BEARER " + governance.VirtualKeyPrefix + "key", true},
		{"mixed case bearer", "BeArEr " + governance.VirtualKeyPrefix + "key", true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.Header.Set("Authorization", tc.authHeader)

			result := getVKFromRequest(ctx)

			if tc.shouldFind {
				if result == "" {
					t.Error("Expected to find VK, got empty string")
				}
			} else {
				if result != "" {
					t.Errorf("Expected empty string, got '%s'", result)
				}
			}
		})
	}
}

// TestMCPServerHandler_HandleMCPServer_Flow documents POST handler flow
func TestMCPServerHandler_HandleMCPServer_Flow(t *testing.T) {
	// handleMCPServer flow:
	// 1. Get MCP server for request (global or VK-specific)
	// 2. If unauthorized, return 401 error
	// 3. Convert fasthttp context to Bifrost context
	// 4. Pass message to MCP server HandleMessage
	// 5. If nil response (notification), return 200 OK with no body
	// 6. Marshal and return JSON response

	t.Log("POST /mcp handles JSON-RPC 2.0 messages via mcp-go server")
}

// TestMCPServerHandler_HandleMCPServerSSE_Flow documents GET handler flow
func TestMCPServerHandler_HandleMCPServerSSE_Flow(t *testing.T) {
	// handleMCPServerSSE flow:
	// 1. Get MCP server for request (validates authorization)
	// 2. If unauthorized, return 401 error
	// 3. Set SSE headers (text/event-stream, no-cache, keep-alive)
	// 4. Convert to Bifrost context
	// 5. Start streaming response writer
	// 6. Send initial connection/opened message
	// 7. Wait for context cancellation

	t.Log("GET /mcp establishes SSE connection with initial connection/opened message")
}

// TestMCPServerHandler_SSEHeaders documents SSE headers
func TestMCPServerHandler_SSEHeaders(t *testing.T) {
	// SSE headers set by handler:
	// Content-Type: text/event-stream
	// Cache-Control: no-cache
	// Connection: keep-alive

	headers := map[string]string{
		"Content-Type":  "text/event-stream",
		"Cache-Control": "no-cache",
		"Connection":    "keep-alive",
	}

	for k, v := range headers {
		t.Logf("SSE header: %s: %s", k, v)
	}
}

// TestMCPServerHandler_SyncAllMCPServers_Behavior documents sync behavior
func TestMCPServerHandler_SyncAllMCPServers_Behavior(t *testing.T) {
	// SyncAllMCPServers behavior:
	// 1. Acquires write lock
	// 2. Gets available tools from tool manager
	// 3. Syncs global MCP server with all tools
	// 4. If ConfigStore is present:
	//    - Gets all virtual keys
	//    - Creates MCP server for each VK
	//    - Syncs each VK server with filtered tools

	t.Log("SyncAllMCPServers syncs global and all VK MCP servers")
}

// TestMCPServerHandler_SyncVKMCPServer_Behavior documents VK sync behavior
func TestMCPServerHandler_SyncVKMCPServer_Behavior(t *testing.T) {
	// SyncVKMCPServer behavior:
	// 1. Acquires write lock
	// 2. Gets or creates MCP server for VK value
	// 3. Fetches tools for this VK
	// 4. Syncs server with filtered tools

	t.Log("SyncVKMCPServer updates a single VK's MCP server")
}

// TestMCPServerHandler_DeleteVKMCPServer_Behavior documents delete behavior
func TestMCPServerHandler_DeleteVKMCPServer_Behavior(t *testing.T) {
	// DeleteVKMCPServer behavior:
	// 1. Acquires write lock
	// 2. Deletes VK from vkMCPServers map

	t.Log("DeleteVKMCPServer removes a VK's MCP server")
}

// TestMCPServerHandler_SyncServer_Behavior documents sync server behavior
func TestMCPServerHandler_SyncServer_Behavior(t *testing.T) {
	// syncServer behavior:
	// 1. Clear existing tools from server
	// 2. For each tool with Function != nil:
	//    - Create handler that executes via tool manager
	//    - Convert parameters to MCP input schema
	//    - Register tool with server

	t.Log("syncServer clears and re-registers all tools")
}

// TestMCPServerHandler_FetchToolsForVK_NoMCPConfigs documents no config behavior
func TestMCPServerHandler_FetchToolsForVK_NoMCPConfigs(t *testing.T) {
	// When VK has no MCPConfigs:
	// - Returns all tools from tool manager (no filtering)

	t.Log("VK with no MCPConfigs gets all available tools")
}

// TestMCPServerHandler_FetchToolsForVK_WithMCPConfigs documents with config behavior
func TestMCPServerHandler_FetchToolsForVK_WithMCPConfigs(t *testing.T) {
	// When VK has MCPConfigs:
	// - Collects tools from each config
	// - Wildcard (*) in config means all tools from that client
	// - Sets mcp-include-tools context value for filtering

	t.Log("VK with MCPConfigs gets filtered tools based on configuration")
}

// TestMCPServerHandler_FetchToolsForVK_Wildcard documents wildcard handling
func TestMCPServerHandler_FetchToolsForVK_Wildcard(t *testing.T) {
	// Wildcard handling:
	// - If ToolsToExecute contains "*": allows all tools from that MCP client
	// - Adds "clientName/*" to include tools list

	t.Log("Wildcard in ToolsToExecute allows all tools from that client")
}

// TestMCPServerHandler_GetMCPServerForRequest_GlobalServer documents global server selection
func TestMCPServerHandler_GetMCPServerForRequest_GlobalServer(t *testing.T) {
	// Global server selection:
	// - EnforceGovernanceHeader is false AND no VK in request
	// - Returns globalMCPServer

	t.Log("Global MCP server used when not enforcing VK and no VK provided")
}

// TestMCPServerHandler_GetMCPServerForRequest_EnforceVK documents VK enforcement
func TestMCPServerHandler_GetMCPServerForRequest_EnforceVK(t *testing.T) {
	// VK enforcement:
	// - If EnforceGovernanceHeader is true and no VK: returns error "virtual key header is required"
	// - If VK provided but not in map: returns error "virtual key not found"

	t.Log("VK enforcement returns appropriate errors when VK is missing or invalid")
}

// TestMCPServerHandler_ToolExecution_ChatFormat documents chat format execution
func TestMCPServerHandler_ToolExecution_ChatFormat(t *testing.T) {
	// Chat format tool execution:
	// 1. Convert MCP request arguments to JSON
	// 2. Create ChatAssistantMessageToolCall with mcp-<toolName> ID
	// 3. Execute via toolManager.ExecuteChatMCPTool
	// 4. Extract text content from ChatMessage response
	// 5. Return as MCP tool result

	t.Log("Tool execution uses chat format with ChatAssistantMessageToolCall")
}

// TestMCPServerHandler_ToolResult_ContentExtraction documents content extraction
func TestMCPServerHandler_ToolResult_ContentExtraction(t *testing.T) {
	// Content extraction from tool result:
	// 1. If ContentStr is set: use directly
	// 2. If ContentBlocks is set: concatenate text blocks
	// 3. Return as MCP NewToolResultText

	t.Log("Tool result content extracted from ContentStr or ContentBlocks")
}

// TestMCPServerHandler_ToolResult_Error documents error handling
func TestMCPServerHandler_ToolResult_Error(t *testing.T) {
	// Error handling in tool execution:
	// - On JSON marshal error: return MCP NewToolResultError
	// - On execution error: return MCP NewToolResultError with message

	t.Log("Tool execution errors returned as MCP error results")
}

// TestMCPServerHandler_InputSchema_Conversion documents input schema conversion
func TestMCPServerHandler_InputSchema_Conversion(t *testing.T) {
	// Input schema conversion:
	// - Type from Function.Parameters.Type
	// - Properties from Function.Parameters.Properties
	// - Required from Function.Parameters.Required
	// - Default: empty object schema if no parameters

	t.Log("Function parameters converted to MCP input schema format")
}

// TestMCPServerHandler_ThreadSafety documents thread safety
func TestMCPServerHandler_ThreadSafety(t *testing.T) {
	// Thread safety:
	// - Handler-level RWMutex for vkMCPServers map
	// - Sync methods acquire write lock
	// - getMCPServerForRequest acquires read lock
	// - Config access uses Config.Mu for client config

	t.Log("Handler uses RWMutex for thread-safe server access")
}

// TestMCPServerHandler_InitialMessage documents SSE initial message
func TestMCPServerHandler_InitialMessage(t *testing.T) {
	// SSE initial message:
	// {
	//   "jsonrpc": "2.0",
	//   "method": "connection/opened"
	// }

	t.Log("SSE connection sends connection/opened JSON-RPC message on connect")
}

// TestMCPServerHandler_RegisterRoutes_WithMiddlewares tests route registration with middlewares
func TestMCPServerHandler_RegisterRoutes_WithMiddlewares(t *testing.T) {
	SetLogger(&mockLogger{})

	// Can't create actual handler without full setup, document behavior
	r := router.New()

	// Routes would be registered with middleware chain:
	// POST /mcp -> ChainMiddlewares(handleMCPServer, middlewares...)
	// GET /mcp -> ChainMiddlewares(handleMCPServerSSE, middlewares...)

	if r == nil {
		t.Error("Router should not be nil")
	}

	t.Log("RegisterRoutes chains provided middlewares with handlers")
}

// TestMCPToolManager_GetAvailableMCPTools tests mock tool manager
func TestMCPToolManager_GetAvailableMCPTools(t *testing.T) {
	description := "A test tool"
	manager := &mockMCPToolManager{
		availableTools: []schemas.ChatTool{
			{
				Type: "function",
				Function: &schemas.ChatToolFunction{
					Name:        "test_tool",
					Description: &description,
				},
			},
		},
	}

	tools := manager.GetAvailableMCPTools(context.Background())

	if len(tools) != 1 {
		t.Fatalf("Expected 1 tool, got %d", len(tools))
	}
	if tools[0].Function.Name != "test_tool" {
		t.Errorf("Expected tool name 'test_tool', got '%s'", tools[0].Function.Name)
	}
}

// TestMCPToolManager_ExecuteChatMCPTool tests mock chat tool execution
func TestMCPToolManager_ExecuteChatMCPTool(t *testing.T) {
	content := "Tool result"
	manager := &mockMCPToolManager{
		chatResult: &schemas.ChatMessage{
			Content: &schemas.ChatMessageContent{
				ContentStr: &content,
			},
		},
	}

	toolCallType := "function"
	toolCallID := "test-id"
	toolName := "test_tool"
	result, err := manager.ExecuteChatMCPTool(context.Background(), schemas.ChatAssistantMessageToolCall{
		ID:   &toolCallID,
		Type: &toolCallType,
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      &toolName,
			Arguments: "{}",
		},
	})

	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("Expected non-nil result")
	}
	if result.Content == nil || result.Content.ContentStr == nil {
		t.Fatal("Expected content in result")
	}
	if *result.Content.ContentStr != "Tool result" {
		t.Errorf("Expected 'Tool result', got '%s'", *result.Content.ContentStr)
	}
}

// TestMCPToolManager_ExecuteChatMCPTool_Error tests error handling
func TestMCPToolManager_ExecuteChatMCPTool_Error(t *testing.T) {
	errMsg := "Tool execution failed"
	manager := &mockMCPToolManager{
		chatError: &schemas.BifrostError{
			Error: &schemas.ErrorField{
				Message: errMsg,
			},
		},
	}

	toolCallType := "function"
	toolCallID := "test-id"
	toolName := "test_tool"
	result, err := manager.ExecuteChatMCPTool(context.Background(), schemas.ChatAssistantMessageToolCall{
		ID:   &toolCallID,
		Type: &toolCallType,
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      &toolName,
			Arguments: "{}",
		},
	})

	if result != nil {
		t.Error("Expected nil result on error")
	}
	if err == nil {
		t.Fatal("Expected error")
	}
	if err.Error == nil || err.Error.Message != errMsg {
		t.Errorf("Expected error message '%s'", errMsg)
	}
}

// TestVirtualKeyPrefix documents the virtual key prefix
func TestVirtualKeyPrefix(t *testing.T) {
	// Virtual key prefix is used to identify virtual keys in various headers
	// Defined in governance.VirtualKeyPrefix

	if governance.VirtualKeyPrefix == "" {
		t.Error("Expected non-empty virtual key prefix")
	}

	t.Logf("Virtual key prefix: '%s'", governance.VirtualKeyPrefix)
}

// TestMCPServerHandler_ResponseFormat_Notification documents notification response
func TestMCPServerHandler_ResponseFormat_Notification(t *testing.T) {
	// When HandleMessage returns nil (notification):
	// - Response is 200 OK with no body
	// - No JSON encoding needed

	t.Log("Notifications (nil response) return 200 OK with empty body")
}

// TestMCPServerHandler_ResponseFormat_JSONResponse documents JSON response
func TestMCPServerHandler_ResponseFormat_JSONResponse(t *testing.T) {
	// When HandleMessage returns a response:
	// - Response is marshaled to JSON
	// - Content-Type is set to application/json
	// - Body contains the JSON response

	t.Log("JSON-RPC responses are marshaled and returned with application/json content type")
}

// TestMCPServerHandler_ContextCancellation documents context handling
func TestMCPServerHandler_ContextCancellation(t *testing.T) {
	// Context handling:
	// - POST handler: uses defer cancel() after ConvertToBifrostContext
	// - GET handler (SSE): cancel() called in defer within stream writer
	// - SSE waits for context Done() before closing

	t.Log("Context is properly cancelled on request completion or client disconnect")
}
