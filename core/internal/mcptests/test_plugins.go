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

// MCPLogEntry represents a logged MCP operation. For envelope-based ops
// (Ping/ListTools/ExecuteTool) Request/Response are populated. For typed Connect
// ops, ConnectRequest/ConnectResponse are populated instead — the two pipelines
// are separate so each entry carries exactly one shape.
type MCPLogEntry struct {
	Request         *schemas.BifrostMCPRequest
	Response        *schemas.BifrostMCPResponse
	ConnectRequest  *schemas.BifrostMCPConnectRequest
	ConnectResponse *schemas.BifrostMCPConnectResponse
	Error           *schemas.BifrostError
	Timestamp       int64
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

// PreMCPConnectionHook implements schemas.MCPConnectionPlugin so the logging plugin
// observes Connect events too. The typed sub-request lands in ConnectRequest on the
// log entry — the envelope-based Request field is left nil for Connect captures.
func (p *TestLoggingPlugin) PreMCPConnectionHook(ctx *schemas.BifrostContext, req *schemas.BifrostMCPConnectRequest) (*schemas.BifrostMCPConnectRequest, *schemas.MCPConnectionShortCircuit, error) {
	if p.captureRequests {
		p.mu.Lock()
		p.preHookCalls = append(p.preHookCalls, MCPLogEntry{
			ConnectRequest: req,
			Timestamp:      time.Now().UnixNano(),
		})
		p.mu.Unlock()
	}
	return req, nil, nil
}

// PostMCPConnectionHook implements schemas.MCPConnectionPlugin for Connect responses.
func (p *TestLoggingPlugin) PostMCPConnectionHook(ctx *schemas.BifrostContext, resp *schemas.BifrostMCPConnectResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostMCPConnectResponse, *schemas.BifrostError, error) {
	if p.captureResponses {
		p.mu.Lock()
		p.postHookCalls = append(p.postHookCalls, MCPLogEntry{
			ConnectResponse: resp,
			Error:           bifrostErr,
			Timestamp:       time.Now().UnixNano(),
		})
		p.mu.Unlock()
	}
	return resp, bifrostErr, nil
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

// =============================================================================
// TEST CONNECT PLUGIN — observes / mutates / short-circuits Connect requests
// =============================================================================
//
// Only acts on Connect requests via the typed MCPConnectionPlugin interface.
// MCPPluginNoOpHooks provides no-op generic PreMCPHook/PostMCPHook so this
// plugin satisfies MCPPlugin (required by the BifrostConfig.MCPPlugins slice).
type TestConnectPlugin struct {
	schemas.MCPPluginNoOpHooks

	mu            sync.RWMutex
	preHookCalls  []MCPLogEntry
	postHookCalls []MCPLogEntry

	// Mutation knobs (applied in PreHook if set).
	mutateHeaders        map[string]string
	mutateConnString     *string
	mutateStdioCommand   *string
	mutateStdioArgs      []string
	mutateStdioArgsIsSet bool

	// Short-circuit knobs (PreHook).
	shortCircuitResponse *schemas.BifrostMCPConnectResponse
	shortCircuitError    *schemas.BifrostError
}

func NewTestConnectPlugin() *TestConnectPlugin {
	return &TestConnectPlugin{
		preHookCalls:  make([]MCPLogEntry, 0),
		postHookCalls: make([]MCPLogEntry, 0),
	}
}

func (p *TestConnectPlugin) GetName() string { return "TestConnectPlugin" }
func (p *TestConnectPlugin) Cleanup() error  { return nil }

// SetMutateHeaders configures the plugin to overwrite the Headers field in PreHook.
func (p *TestConnectPlugin) SetMutateHeaders(headers map[string]string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mutateHeaders = headers
}

// SetMutateConnectionString configures the plugin to overwrite ConnectionString in PreHook.
func (p *TestConnectPlugin) SetMutateConnectionString(url *string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mutateConnString = url
}

// SetMutateStdioCommand configures the plugin to overwrite StdioCommand in PreHook.
func (p *TestConnectPlugin) SetMutateStdioCommand(cmd *string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mutateStdioCommand = cmd
}

// SetMutateStdioArgs configures the plugin to overwrite StdioArgs in PreHook.
// Pass nil to leave it unchanged; pass [] to explicitly clear it.
func (p *TestConnectPlugin) SetMutateStdioArgs(args []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mutateStdioArgs = args
	p.mutateStdioArgsIsSet = true
}

// SetShortCircuitResponse configures the plugin to short-circuit Connect with a
// synthetic success response carrying the provided sub-response payload.
func (p *TestConnectPlugin) SetShortCircuitResponse(resp *schemas.BifrostMCPConnectResponse) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.shortCircuitResponse = resp
}

// SetShortCircuitError configures the plugin to short-circuit Connect with the given error.
func (p *TestConnectPlugin) SetShortCircuitError(err *schemas.BifrostError) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.shortCircuitError = err
}

// PreMCPConnectionHook implements schemas.MCPConnectionPlugin (typed Connect hook).
// No RequestType filtering needed — the pipeline only invokes this method for
// Connect requests, and it gets the typed sub-request directly.
func (p *TestConnectPlugin) PreMCPConnectionHook(ctx *schemas.BifrostContext, req *schemas.BifrostMCPConnectRequest) (*schemas.BifrostMCPConnectRequest, *schemas.MCPConnectionShortCircuit, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.preHookCalls = append(p.preHookCalls, MCPLogEntry{
		ConnectRequest: req,
		Timestamp:      time.Now().UnixNano(),
	})

	// Short-circuit before mutation (mutation only matters if the op runs).
	if p.shortCircuitError != nil {
		return req, &schemas.MCPConnectionShortCircuit{Error: p.shortCircuitError}, nil
	}
	if p.shortCircuitResponse != nil {
		return req, &schemas.MCPConnectionShortCircuit{Response: p.shortCircuitResponse}, nil
	}

	// Apply mutations directly on the typed sub-request — no nil-check on a wrapper needed.
	if p.mutateHeaders != nil {
		req.Headers = p.mutateHeaders
	}
	if p.mutateConnString != nil {
		req.ConnectionString = p.mutateConnString
	}
	if p.mutateStdioCommand != nil {
		req.StdioCommand = p.mutateStdioCommand
	}
	if p.mutateStdioArgsIsSet {
		req.StdioArgs = p.mutateStdioArgs
	}
	return req, nil, nil
}

// PostMCPConnectionHook implements schemas.MCPConnectionPlugin.
// Captures only successful Connect outcomes (resp non-nil). Short-circuit-error
// paths skip capture — matching the "observe outcomes" intent of test logging.
func (p *TestConnectPlugin) PostMCPConnectionHook(ctx *schemas.BifrostContext, resp *schemas.BifrostMCPConnectResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostMCPConnectResponse, *schemas.BifrostError, error) {
	if resp == nil {
		return resp, bifrostErr, nil
	}
	p.mu.Lock()
	p.postHookCalls = append(p.postHookCalls, MCPLogEntry{
		ConnectResponse: resp,
		Error:           bifrostErr,
		Timestamp:       time.Now().UnixNano(),
	})
	p.mu.Unlock()
	return resp, bifrostErr, nil
}

func (p *TestConnectPlugin) GetPreHookCalls() []MCPLogEntry {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]MCPLogEntry, len(p.preHookCalls))
	copy(out, p.preHookCalls)
	return out
}

func (p *TestConnectPlugin) GetPostHookCalls() []MCPLogEntry {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]MCPLogEntry, len(p.postHookCalls))
	copy(out, p.postHookCalls)
	return out
}

func (p *TestConnectPlugin) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.preHookCalls = p.preHookCalls[:0]
	p.postHookCalls = p.postHookCalls[:0]
}

// =============================================================================
// TEST PING PLUGIN — observes / short-circuits Ping requests
// =============================================================================
//
// Only acts on requests with RequestType == MCPRequestTypePing.
type TestPingPlugin struct {
	mu            sync.RWMutex
	preHookCalls  []MCPLogEntry
	postHookCalls []MCPLogEntry

	shortCircuitHealthy bool                  // if true, PreHook returns a synthetic healthy response
	shortCircuitError   *schemas.BifrostError // if non-nil, PreHook returns this error
}

func NewTestPingPlugin() *TestPingPlugin {
	return &TestPingPlugin{
		preHookCalls:  make([]MCPLogEntry, 0),
		postHookCalls: make([]MCPLogEntry, 0),
	}
}

func (p *TestPingPlugin) GetName() string { return "TestPingPlugin" }
func (p *TestPingPlugin) Cleanup() error  { return nil }

// SetShortCircuitHealthy makes PreHook return a synthetic healthy ping response.
func (p *TestPingPlugin) SetShortCircuitHealthy(healthy bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.shortCircuitHealthy = healthy
}

// SetShortCircuitError makes PreHook return the given error (counts as ping failure).
func (p *TestPingPlugin) SetShortCircuitError(err *schemas.BifrostError) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.shortCircuitError = err
}

func (p *TestPingPlugin) PreMCPHook(ctx *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPRequest, *schemas.MCPPluginShortCircuit, error) {
	if req == nil || req.RequestType != schemas.MCPRequestTypePing {
		return req, nil, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.preHookCalls = append(p.preHookCalls, MCPLogEntry{
		Request:   req,
		Timestamp: time.Now().UnixNano(),
	})

	if p.shortCircuitError != nil {
		return req, &schemas.MCPPluginShortCircuit{Error: p.shortCircuitError}, nil
	}
	if p.shortCircuitHealthy {
		return req, &schemas.MCPPluginShortCircuit{
			Response: &schemas.BifrostMCPResponse{
				BifrostMCPPingResponse: &schemas.BifrostMCPPingResponse{},
			},
		}, nil
	}
	return req, nil, nil
}

func (p *TestPingPlugin) PostMCPHook(ctx *schemas.BifrostContext, resp *schemas.BifrostMCPResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostMCPResponse, *schemas.BifrostError, error) {
	// Distinguish ping responses from other ops. Successful ping carries a non-nil
	// BifrostMCPPingResponse; failed ping has nil response + non-nil error — in that
	// case we can't tell from the response alone, but the err path is reached for
	// any failed op, so for now only capture successful pings (matches the typical
	// observability use case).
	if resp == nil || resp.BifrostMCPPingResponse == nil {
		return resp, bifrostErr, nil
	}
	p.mu.Lock()
	p.postHookCalls = append(p.postHookCalls, MCPLogEntry{
		Response:  resp,
		Error:     bifrostErr,
		Timestamp: time.Now().UnixNano(),
	})
	p.mu.Unlock()
	return resp, bifrostErr, nil
}

func (p *TestPingPlugin) GetPreHookCallCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.preHookCalls)
}

func (p *TestPingPlugin) GetPostHookCallCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.postHookCalls)
}

func (p *TestPingPlugin) GetPreHookCalls() []MCPLogEntry {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]MCPLogEntry, len(p.preHookCalls))
	copy(out, p.preHookCalls)
	return out
}

func (p *TestPingPlugin) GetPostHookCalls() []MCPLogEntry {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]MCPLogEntry, len(p.postHookCalls))
	copy(out, p.postHookCalls)
	return out
}

func (p *TestPingPlugin) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.preHookCalls = p.preHookCalls[:0]
	p.postHookCalls = p.postHookCalls[:0]
}

// =============================================================================
// TEST LISTTOOLS PLUGIN — observes / mutates / short-circuits ListTools requests
// =============================================================================
//
// Only acts on requests with RequestType == MCPRequestTypeListTools.
type TestListToolsPlugin struct {
	mu            sync.RWMutex
	preHookCalls  []MCPLogEntry
	postHookCalls []MCPLogEntry

	// PreHook short-circuit knobs.
	shortCircuitResponse *schemas.BifrostMCPListToolsResponse
	shortCircuitError    *schemas.BifrostError

	// PostHook mutation knob: optional filter applied to the Tools map. If non-nil,
	// only keys returned true are kept; ToolNameMapping is filtered to match.
	postHookFilter func(toolName string) bool
}

func NewTestListToolsPlugin() *TestListToolsPlugin {
	return &TestListToolsPlugin{
		preHookCalls:  make([]MCPLogEntry, 0),
		postHookCalls: make([]MCPLogEntry, 0),
	}
}

func (p *TestListToolsPlugin) GetName() string { return "TestListToolsPlugin" }
func (p *TestListToolsPlugin) Cleanup() error  { return nil }

// SetShortCircuitResponse makes PreHook return a synthetic list_tools response.
func (p *TestListToolsPlugin) SetShortCircuitResponse(resp *schemas.BifrostMCPListToolsResponse) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.shortCircuitResponse = resp
}

// SetShortCircuitError makes PreHook return the given error.
func (p *TestListToolsPlugin) SetShortCircuitError(err *schemas.BifrostError) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.shortCircuitError = err
}

// SetPostHookFilter configures a PostHook tool-name predicate. Tools whose prefixed
// name passes (returns true) are kept; others are removed from both Tools and
// ToolNameMapping (matching the sanitized->original lookup).
func (p *TestListToolsPlugin) SetPostHookFilter(filter func(toolName string) bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.postHookFilter = filter
}

func (p *TestListToolsPlugin) PreMCPHook(ctx *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPRequest, *schemas.MCPPluginShortCircuit, error) {
	if req == nil || req.RequestType != schemas.MCPRequestTypeListTools {
		return req, nil, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.preHookCalls = append(p.preHookCalls, MCPLogEntry{
		Request:   req,
		Timestamp: time.Now().UnixNano(),
	})

	if p.shortCircuitError != nil {
		return req, &schemas.MCPPluginShortCircuit{Error: p.shortCircuitError}, nil
	}
	if p.shortCircuitResponse != nil {
		return req, &schemas.MCPPluginShortCircuit{
			Response: &schemas.BifrostMCPResponse{
				BifrostMCPListToolsResponse: p.shortCircuitResponse,
			},
		}, nil
	}
	return req, nil, nil
}

func (p *TestListToolsPlugin) PostMCPHook(ctx *schemas.BifrostContext, resp *schemas.BifrostMCPResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostMCPResponse, *schemas.BifrostError, error) {
	if resp == nil || resp.BifrostMCPListToolsResponse == nil {
		return resp, bifrostErr, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Apply filter if configured. Tools are keyed by prefixed name; ToolNameMapping
	// is keyed by sanitized name — we filter by prefixed name and drop the matching
	// sanitized entry too (sanitized = stripped client prefix, with '-'→'_').
	if p.postHookFilter != nil {
		filteredTools := make(map[string]schemas.ChatTool, len(resp.Tools))
		keep := make(map[string]bool, len(resp.Tools))
		for name, tool := range resp.Tools {
			if p.postHookFilter(name) {
				filteredTools[name] = tool
				keep[name] = true
			}
		}
		// Filter ToolNameMapping: keep mapping entries whose original value still
		// corresponds to a tool that survived.
		filteredMapping := make(map[string]string, len(resp.ToolNameMapping))
		for sanitized, original := range resp.ToolNameMapping {
			// Find the prefixed key that would have been used for `original`.
			for prefixed := range keep {
				// prefixed == "<client>-<original>"; check suffix match.
				if len(prefixed) > len(original) && prefixed[len(prefixed)-len(original):] == original {
					filteredMapping[sanitized] = original
					break
				}
			}
		}
		resp.Tools = filteredTools
		resp.ToolNameMapping = filteredMapping
	}

	p.postHookCalls = append(p.postHookCalls, MCPLogEntry{
		Response:  resp,
		Error:     bifrostErr,
		Timestamp: time.Now().UnixNano(),
	})
	return resp, bifrostErr, nil
}

func (p *TestListToolsPlugin) GetPreHookCallCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.preHookCalls)
}

func (p *TestListToolsPlugin) GetPostHookCallCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.postHookCalls)
}

func (p *TestListToolsPlugin) GetPreHookCalls() []MCPLogEntry {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]MCPLogEntry, len(p.preHookCalls))
	copy(out, p.preHookCalls)
	return out
}

func (p *TestListToolsPlugin) GetPostHookCalls() []MCPLogEntry {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]MCPLogEntry, len(p.postHookCalls))
	copy(out, p.postHookCalls)
	return out
}

func (p *TestListToolsPlugin) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.preHookCalls = p.preHookCalls[:0]
	p.postHookCalls = p.postHookCalls[:0]
}
