package mcptests

import (
	"fmt"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// =============================================================================
// TEST LOGGING PLUGIN
// =============================================================================

// TestLoggingPlugin captures all MCP requests and responses for testing
type TestLoggingPlugin struct {
	mu               sync.RWMutex
	preHookCalls     []MCPLogEntry
	postHookCalls    []MCPLogEntry
	captureRequests  bool
	captureResponses bool
}

// MCPLogEntry represents a logged MCP operation
type MCPLogEntry struct {
	Request   *schemas.BifrostMCPRequest
	Response  *schemas.BifrostMCPResponse
	Error     *schemas.BifrostError
	Timestamp int64
}

// NewTestLoggingPlugin creates a new test logging plugin
func NewTestLoggingPlugin() *TestLoggingPlugin {
	return &TestLoggingPlugin{
		preHookCalls:     make([]MCPLogEntry, 0),
		postHookCalls:    make([]MCPLogEntry, 0),
		captureRequests:  true,
		captureResponses: true,
	}
}

// GetName implements schemas.BasePlugin
func (p *TestLoggingPlugin) GetName() string {
	return "TestLoggingPlugin"
}

// Cleanup implements schemas.BasePlugin
func (p *TestLoggingPlugin) Cleanup() error {
	return nil
}

// PreMCPHook implements schemas.MCPPlugin
func (p *TestLoggingPlugin) PreMCPHook(ctx *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPRequest, *schemas.MCPPluginShortCircuit, error) {
	if p.captureRequests {
		p.mu.Lock()
		p.preHookCalls = append(p.preHookCalls, MCPLogEntry{
			Request:   req,
			Timestamp: time.Now().UnixNano(),
		})
		p.mu.Unlock()
	}
	return req, nil, nil
}

// PostMCPHook implements schemas.MCPPlugin
func (p *TestLoggingPlugin) PostMCPHook(ctx *schemas.BifrostContext, resp *schemas.BifrostMCPResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostMCPResponse, *schemas.BifrostError, error) {
	if p.captureResponses {
		p.mu.Lock()
		p.postHookCalls = append(p.postHookCalls, MCPLogEntry{
			Response:  resp,
			Error:     bifrostErr,
			Timestamp: time.Now().UnixNano(),
		})
		p.mu.Unlock()
	}
	return resp, bifrostErr, nil
}

// GetPreHookCallCount returns the number of PreHook calls
func (p *TestLoggingPlugin) GetPreHookCallCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.preHookCalls)
}

// GetPostHookCallCount returns the number of PostHook calls
func (p *TestLoggingPlugin) GetPostHookCallCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.postHookCalls)
}

// GetPreHookCalls returns all PreHook calls
func (p *TestLoggingPlugin) GetPreHookCalls() []MCPLogEntry {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := make([]MCPLogEntry, len(p.preHookCalls))
	copy(result, p.preHookCalls)
	return result
}

// GetPostHookCalls returns all PostHook calls
func (p *TestLoggingPlugin) GetPostHookCalls() []MCPLogEntry {
	p.mu.RLock()
	defer p.mu.RUnlock()
	result := make([]MCPLogEntry, len(p.postHookCalls))
	copy(result, p.postHookCalls)
	return result
}

// Reset clears all captured calls
func (p *TestLoggingPlugin) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.preHookCalls = make([]MCPLogEntry, 0)
	p.postHookCalls = make([]MCPLogEntry, 0)
}

// =============================================================================
// TEST GOVERNANCE PLUGIN
// =============================================================================

// TestGovernancePlugin blocks tool execution based on configurable rules
type TestGovernancePlugin struct {
	mu                sync.RWMutex
	blockedToolNames  map[string]bool
	blockedClientIDs  map[string]bool
	blockAllTools     bool
	blockMessage      string
	allowedToolNames  map[string]bool
	requireApproval   bool
}

// NewTestGovernancePlugin creates a new test governance plugin
func NewTestGovernancePlugin() *TestGovernancePlugin {
	return &TestGovernancePlugin{
		blockedToolNames: make(map[string]bool),
		blockedClientIDs: make(map[string]bool),
		allowedToolNames: make(map[string]bool),
		blockMessage:     "Tool execution blocked by governance policy",
	}
}

// GetName implements schemas.BasePlugin
func (p *TestGovernancePlugin) GetName() string {
	return "TestGovernancePlugin"
}

// Cleanup implements schemas.BasePlugin
func (p *TestGovernancePlugin) Cleanup() error {
	return nil
}

// BlockTool adds a tool to the block list
func (p *TestGovernancePlugin) BlockTool(toolName string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.blockedToolNames[toolName] = true
}

// UnblockTool removes a tool from the block list
func (p *TestGovernancePlugin) UnblockTool(toolName string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.blockedToolNames, toolName)
}

// BlockClient adds a client to the block list
func (p *TestGovernancePlugin) BlockClient(clientID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.blockedClientIDs[clientID] = true
}

// UnblockClient removes a client from the block list
func (p *TestGovernancePlugin) UnblockClient(clientID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.blockedClientIDs, clientID)
}

// SetBlockAllTools sets whether to block all tools
func (p *TestGovernancePlugin) SetBlockAllTools(block bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.blockAllTools = block
}

// SetBlockMessage sets the message returned when blocking
func (p *TestGovernancePlugin) SetBlockMessage(message string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.blockMessage = message
}

// AllowTool adds a tool to the allow list (only these tools can execute)
func (p *TestGovernancePlugin) AllowTool(toolName string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.allowedToolNames[toolName] = true
}

// ClearAllowList clears the allow list
func (p *TestGovernancePlugin) ClearAllowList() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.allowedToolNames = make(map[string]bool)
}

// PreMCPHook implements schemas.MCPPlugin
func (p *TestGovernancePlugin) PreMCPHook(ctx *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPRequest, *schemas.MCPPluginShortCircuit, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	// Extract tool name from request
	toolName := p.extractToolName(req)
	if toolName == "" {
		return req, nil, nil
	}

	// Check if blocking all tools
	if p.blockAllTools {
		return req, p.createShortCircuit(toolName, p.blockMessage), nil
	}

	// Check if tool is explicitly blocked
	if p.blockedToolNames[toolName] {
		return req, p.createShortCircuit(toolName, fmt.Sprintf("Tool '%s' is blocked", toolName)), nil
	}

	// Check allow list (if configured)
	if len(p.allowedToolNames) > 0 && !p.allowedToolNames[toolName] {
		return req, p.createShortCircuit(toolName, fmt.Sprintf("Tool '%s' is not in allow list", toolName)), nil
	}

	return req, nil, nil
}

// PostMCPHook implements schemas.MCPPlugin
func (p *TestGovernancePlugin) PostMCPHook(ctx *schemas.BifrostContext, resp *schemas.BifrostMCPResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostMCPResponse, *schemas.BifrostError, error) {
	// No post-processing needed for governance
	return resp, bifrostErr, nil
}

// extractToolName extracts tool name from request
func (p *TestGovernancePlugin) extractToolName(req *schemas.BifrostMCPRequest) string {
	if req.ChatAssistantMessageToolCall != nil && req.ChatAssistantMessageToolCall.Function.Name != nil {
		return *req.ChatAssistantMessageToolCall.Function.Name
	}
	if req.ResponsesToolMessage != nil && req.ResponsesToolMessage.Name != nil {
		return *req.ResponsesToolMessage.Name
	}
	return ""
}

// createShortCircuit creates a short-circuit response
func (p *TestGovernancePlugin) createShortCircuit(toolName, message string) *schemas.MCPPluginShortCircuit {
	return &schemas.MCPPluginShortCircuit{
		Response: &schemas.BifrostMCPResponse{
			ChatMessage: &schemas.ChatMessage{
				Role: schemas.ChatMessageRoleTool,
				Content: &schemas.ChatMessageContent{
					ContentStr: &message,
				},
			},
			ResponsesMessage: &schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					Output: &schemas.ResponsesToolMessageOutputStruct{
						ResponsesToolCallOutputStr: &message,
					},
				},
			},
		},
	}
}

// =============================================================================
// TEST MODIFY REQUEST PLUGIN
// =============================================================================

// TestModifyRequestPlugin modifies MCP requests in PreHook
type TestModifyRequestPlugin struct {
	mu                 sync.RWMutex
	argumentModifier   func(string) string
	shouldModify       bool
}

// NewTestModifyRequestPlugin creates a new test modify request plugin
func NewTestModifyRequestPlugin() *TestModifyRequestPlugin {
	return &TestModifyRequestPlugin{
		shouldModify: true,
	}
}

// GetName implements schemas.BasePlugin
func (p *TestModifyRequestPlugin) GetName() string {
	return "TestModifyRequestPlugin"
}

// Cleanup implements schemas.BasePlugin
func (p *TestModifyRequestPlugin) Cleanup() error {
	return nil
}

// SetArgumentModifier sets a function to modify tool arguments
func (p *TestModifyRequestPlugin) SetArgumentModifier(modifier func(string) string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.argumentModifier = modifier
}

// SetShouldModify sets whether to modify requests
func (p *TestModifyRequestPlugin) SetShouldModify(should bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.shouldModify = should
}

// PreMCPHook implements schemas.MCPPlugin
func (p *TestModifyRequestPlugin) PreMCPHook(ctx *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPRequest, *schemas.MCPPluginShortCircuit, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if !p.shouldModify || p.argumentModifier == nil {
		return req, nil, nil
	}

	// Modify Chat format
	if req.ChatAssistantMessageToolCall != nil {
		modifiedArgs := p.argumentModifier(req.ChatAssistantMessageToolCall.Function.Arguments)
		req.ChatAssistantMessageToolCall.Function.Arguments = modifiedArgs
	}

	// Modify Responses format
	if req.ResponsesToolMessage != nil && req.ResponsesToolMessage.Arguments != nil {
		modifiedArgs := p.argumentModifier(*req.ResponsesToolMessage.Arguments)
		req.ResponsesToolMessage.Arguments = &modifiedArgs
	}

	return req, nil, nil
}

// PostMCPHook implements schemas.MCPPlugin
func (p *TestModifyRequestPlugin) PostMCPHook(ctx *schemas.BifrostContext, resp *schemas.BifrostMCPResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostMCPResponse, *schemas.BifrostError, error) {
	return resp, bifrostErr, nil
}

// =============================================================================
// TEST MODIFY RESPONSE PLUGIN
// =============================================================================

// TestModifyResponsePlugin modifies MCP responses in PostHook
type TestModifyResponsePlugin struct {
	mu               sync.RWMutex
	responseModifier func(string) string
	shouldModify     bool
}

// NewTestModifyResponsePlugin creates a new test modify response plugin
func NewTestModifyResponsePlugin() *TestModifyResponsePlugin {
	return &TestModifyResponsePlugin{
		shouldModify: true,
	}
}

// GetName implements schemas.BasePlugin
func (p *TestModifyResponsePlugin) GetName() string {
	return "TestModifyResponsePlugin"
}

// Cleanup implements schemas.BasePlugin
func (p *TestModifyResponsePlugin) Cleanup() error {
	return nil
}

// SetResponseModifier sets a function to modify tool responses
func (p *TestModifyResponsePlugin) SetResponseModifier(modifier func(string) string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.responseModifier = modifier
}

// SetShouldModify sets whether to modify responses
func (p *TestModifyResponsePlugin) SetShouldModify(should bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.shouldModify = should
}

// PreMCPHook implements schemas.MCPPlugin
func (p *TestModifyResponsePlugin) PreMCPHook(ctx *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPRequest, *schemas.MCPPluginShortCircuit, error) {
	return req, nil, nil
}

// PostMCPHook implements schemas.MCPPlugin
func (p *TestModifyResponsePlugin) PostMCPHook(ctx *schemas.BifrostContext, resp *schemas.BifrostMCPResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostMCPResponse, *schemas.BifrostError, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if !p.shouldModify || p.responseModifier == nil || resp == nil {
		return resp, bifrostErr, nil
	}

	// Modify Chat format response
	if resp.ChatMessage != nil && resp.ChatMessage.Content != nil && resp.ChatMessage.Content.ContentStr != nil {
		modified := p.responseModifier(*resp.ChatMessage.Content.ContentStr)
		resp.ChatMessage.Content.ContentStr = &modified
	}

	// Modify Responses format response
	if resp.ResponsesMessage != nil && resp.ResponsesMessage.ResponsesToolMessage != nil && resp.ResponsesMessage.ResponsesToolMessage.Output != nil {
		if resp.ResponsesMessage.ResponsesToolMessage.Output.ResponsesToolCallOutputStr != nil {
			modified := p.responseModifier(*resp.ResponsesMessage.ResponsesToolMessage.Output.ResponsesToolCallOutputStr)
			resp.ResponsesMessage.ResponsesToolMessage.Output.ResponsesToolCallOutputStr = &modified
		}
	}

	return resp, bifrostErr, nil
}

// =============================================================================
// TEST SHORT CIRCUIT PLUGIN
// =============================================================================

// TestShortCircuitPlugin short-circuits MCP execution and returns immediately
type TestShortCircuitPlugin struct {
	mu                  sync.RWMutex
	shouldShortCircuit  bool
	shortCircuitMessage string
}

// NewTestShortCircuitPlugin creates a new test short circuit plugin
func NewTestShortCircuitPlugin() *TestShortCircuitPlugin {
	return &TestShortCircuitPlugin{
		shouldShortCircuit:  false,
		shortCircuitMessage: "Short-circuited by test plugin",
	}
}

// GetName implements schemas.BasePlugin
func (p *TestShortCircuitPlugin) GetName() string {
	return "TestShortCircuitPlugin"
}

// Cleanup implements schemas.BasePlugin
func (p *TestShortCircuitPlugin) Cleanup() error {
	return nil
}

// SetShouldShortCircuit sets whether to short-circuit execution
func (p *TestShortCircuitPlugin) SetShouldShortCircuit(should bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.shouldShortCircuit = should
}

// SetShortCircuitMessage sets the message returned when short-circuiting
func (p *TestShortCircuitPlugin) SetShortCircuitMessage(message string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.shortCircuitMessage = message
}

// PreMCPHook implements schemas.MCPPlugin
func (p *TestShortCircuitPlugin) PreMCPHook(ctx *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPRequest, *schemas.MCPPluginShortCircuit, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if !p.shouldShortCircuit {
		return req, nil, nil
	}

	return req, &schemas.MCPPluginShortCircuit{
		Response: &schemas.BifrostMCPResponse{
			ChatMessage: &schemas.ChatMessage{
				Role: schemas.ChatMessageRoleTool,
				Content: &schemas.ChatMessageContent{
					ContentStr: &p.shortCircuitMessage,
				},
			},
			ResponsesMessage: &schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					Output: &schemas.ResponsesToolMessageOutputStruct{
						ResponsesToolCallOutputStr: &p.shortCircuitMessage,
					},
				},
			},
		},
	}, nil
}

// PostMCPHook implements schemas.MCPPlugin
func (p *TestShortCircuitPlugin) PostMCPHook(ctx *schemas.BifrostContext, resp *schemas.BifrostMCPResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostMCPResponse, *schemas.BifrostError, error) {
	return resp, bifrostErr, nil
}
