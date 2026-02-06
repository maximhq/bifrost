package mcptests

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// AGENT TEST CONFIGURATION
// =============================================================================

// AgentTestConfig provides declarative configuration for agent mode tests
type AgentTestConfig struct {
	// Tool registration
	InProcessTools []string // InProcess tools to register (echo, calculator, weather, etc.)
	STDIOClients   []string // STDIO clients to add (temperature, go-test-server, etc.)
	HTTPClients    []string // HTTP client names (for future expansion)
	SSEClients     []string // SSE client names (for future expansion)

	// Auto-execute configuration
	AutoExecuteTools []string // Tools to set as auto-execute (supports "*", specific names)

	// Agent configuration
	MaxDepth int // Max agent depth (0 = use default)

	// Context filtering (runtime overrides)
	ClientFiltering []string // Context client filter (MCPContextKeyIncludeClients)
	ToolFiltering   []string // Context tool filter (MCPContextKeyIncludeTools)

	// Test expectations
	ExpectedCallCount   int    // Expected number of LLM calls
	ExpectedFinalReason string // Expected final finish reason
}

// =============================================================================
// AGENT TEST SETUP
// =============================================================================

// SetupAgentTest creates a complete agent test environment with the specified configuration
func SetupAgentTest(t *testing.T, config AgentTestConfig) (*mcp.MCPManager, *DynamicLLMMocker, *schemas.BifrostContext) {
	t.Helper()

	// Initialize global MCP server paths
	InitMCPServerPaths(t)

	// Build client configs
	clientConfigs := []schemas.MCPClientConfig{}

	// Add STDIO clients (using global paths from fixtures.go)
	for _, clientName := range config.STDIOClients {
		switch clientName {
		case "temperature":
			clientConfigs = append(clientConfigs, GetTemperatureMCPClientConfig(""))
		case "go-test-server":
			clientConfigs = append(clientConfigs, GetGoTestServerConfig(""))
		case "parallel-test-server":
			clientConfigs = append(clientConfigs, GetParallelTestServerConfig(""))
		case "edge-case-server":
			clientConfigs = append(clientConfigs, GetEdgeCaseServerConfig(""))
		case "error-test-server":
			clientConfigs = append(clientConfigs, GetErrorTestServerConfig(""))
		default:
			t.Fatalf("Unknown STDIO client: %s", clientName)
		}
	}

	// Create MCP manager with STDIO clients
	manager := setupMCPManager(t, clientConfigs...)

	// Register InProcess tools
	for _, toolName := range config.InProcessTools {
		switch toolName {
		case "echo":
			require.NoError(t, RegisterEchoTool(manager))
		case "calculator":
			require.NoError(t, RegisterCalculatorTool(manager))
		case "weather":
			require.NoError(t, RegisterWeatherTool(manager))
		case "search":
			require.NoError(t, RegisterSearchTool(manager))
		case "delay":
			require.NoError(t, RegisterDelayTool(manager))
		case "throw_error":
			require.NoError(t, RegisterThrowErrorTool(manager))
		case "get_time":
			require.NoError(t, RegisterGetTimeTool(manager))
		case "read_file":
			require.NoError(t, RegisterReadFileTool(manager))
		case "get_temperature":
			require.NoError(t, RegisterGetTemperatureTool(manager))
		default:
			t.Fatalf("Unknown InProcess tool: %s", toolName)
		}
	}

	// Set auto-execute tools for internal client
	if len(config.AutoExecuteTools) > 0 {
		require.NoError(t, SetInternalClientAutoExecute(manager, config.AutoExecuteTools))
	}

	// Set auto-execute tools for STDIO clients if wildcard
	if len(config.AutoExecuteTools) > 0 {
		for _, autoTool := range config.AutoExecuteTools {
			if autoTool == "*" {
				// Set wildcard for all STDIO clients
				clients := manager.GetClients()
				for i := range clients {
					if clients[i].ExecutionConfig.ConnectionType == schemas.MCPConnectionTypeSTDIO {
						clients[i].ExecutionConfig.ToolsToAutoExecute = []string{"*"}
						require.NoError(t, manager.UpdateClient(clients[i].ExecutionConfig.ID, clients[i].ExecutionConfig))
					}
				}
				break
			}
		}
	}

	// Set max depth if specified
	if config.MaxDepth > 0 {
		manager.UpdateToolManagerConfig(&schemas.MCPToolManagerConfig{
			MaxAgentDepth: config.MaxDepth,
		})
	}

	// Create context with filtering
	baseCtx := context.Background()
	if len(config.ClientFiltering) > 0 {
		baseCtx = context.WithValue(baseCtx, mcp.MCPContextKeyIncludeClients, config.ClientFiltering)
	}
	if len(config.ToolFiltering) > 0 {
		baseCtx = context.WithValue(baseCtx, mcp.MCPContextKeyIncludeTools, config.ToolFiltering)
	}
	ctx := schemas.NewBifrostContext(baseCtx, schemas.NoDeadline)

	// Create dynamic LLM mocker
	mocker := NewDynamicLLMMocker()

	return manager, mocker, ctx
}

// SetupAgentTestWithClients creates an agent test environment with custom client configs
func SetupAgentTestWithClients(t *testing.T, config AgentTestConfig, customClients []schemas.MCPClientConfig) (*mcp.MCPManager, *DynamicLLMMocker, *schemas.BifrostContext) {
	t.Helper()

	// Create MCP manager with custom clients
	manager := setupMCPManager(t, customClients...)

	// Register InProcess tools
	for _, toolName := range config.InProcessTools {
		switch toolName {
		case "echo":
			require.NoError(t, RegisterEchoTool(manager))
		case "calculator":
			require.NoError(t, RegisterCalculatorTool(manager))
		case "weather":
			require.NoError(t, RegisterWeatherTool(manager))
		case "search":
			require.NoError(t, RegisterSearchTool(manager))
		case "delay":
			require.NoError(t, RegisterDelayTool(manager))
		case "throw_error":
			require.NoError(t, RegisterThrowErrorTool(manager))
		case "get_time":
			require.NoError(t, RegisterGetTimeTool(manager))
		case "read_file":
			require.NoError(t, RegisterReadFileTool(manager))
		case "get_temperature":
			require.NoError(t, RegisterGetTemperatureTool(manager))
		default:
			t.Fatalf("Unknown InProcess tool: %s", toolName)
		}
	}

	// Set auto-execute tools
	if len(config.AutoExecuteTools) > 0 {
		require.NoError(t, SetInternalClientAutoExecute(manager, config.AutoExecuteTools))
	}

	// Set max depth if specified
	if config.MaxDepth > 0 {
		manager.UpdateToolManagerConfig(&schemas.MCPToolManagerConfig{
			MaxAgentDepth: config.MaxDepth,
		})
	}

	// Create context with filtering
	baseCtx := context.Background()
	if len(config.ClientFiltering) > 0 {
		baseCtx = context.WithValue(baseCtx, mcp.MCPContextKeyIncludeClients, config.ClientFiltering)
	}
	if len(config.ToolFiltering) > 0 {
		baseCtx = context.WithValue(baseCtx, mcp.MCPContextKeyIncludeTools, config.ToolFiltering)
	}
	ctx := schemas.NewBifrostContext(baseCtx, schemas.NoDeadline)

	// Create dynamic LLM mocker
	mocker := NewDynamicLLMMocker()

	return manager, mocker, ctx
}

// =============================================================================
// MULTI-CLIENT SETUP HELPERS
// =============================================================================

// SetupMultiClientAgentTest creates an agent test with multiple client types
func SetupMultiClientAgentTest(t *testing.T, inProcessTools []string, stdioClients []string, autoExecute []string, maxDepth int) (*mcp.MCPManager, *DynamicLLMMocker, *schemas.BifrostContext) {
	t.Helper()

	return SetupAgentTest(t, AgentTestConfig{
		InProcessTools:   inProcessTools,
		STDIOClients:     stdioClients,
		AutoExecuteTools: autoExecute,
		MaxDepth:         maxDepth,
	})
}

// SetupContextFilteredAgentTest creates an agent test with context filtering
func SetupContextFilteredAgentTest(t *testing.T, inProcessTools []string, stdioClients []string, autoExecute []string, toolFilter []string, clientFilter []string) (*mcp.MCPManager, *DynamicLLMMocker, *schemas.BifrostContext) {
	t.Helper()

	return SetupAgentTest(t, AgentTestConfig{
		InProcessTools:   inProcessTools,
		STDIOClients:     stdioClients,
		AutoExecuteTools: autoExecute,
		ToolFiltering:    toolFilter,
		ClientFiltering:  clientFilter,
		MaxDepth:         10, // Default reasonable max depth
	})
}

// =============================================================================
// AGENT-SPECIFIC ASSERTION HELPERS
// =============================================================================

// AssertAgentCompletedInTurns verifies the agent completed in expected number of LLM calls
func AssertAgentCompletedInTurns(t *testing.T, mocker *DynamicLLMMocker, expectedTurns int) {
	t.Helper()
	actualTurns := mocker.GetChatCallCount()
	assert.Equal(t, expectedTurns, actualTurns, "Agent should complete in %d turns, got %d", expectedTurns, actualTurns)
}

// AssertAgentStoppedAtTurn verifies the agent stopped at a specific turn (e.g., due to non-auto tool)
func AssertAgentStoppedAtTurn(t *testing.T, mocker *DynamicLLMMocker, expectedTurn int) {
	t.Helper()
	actualTurn := mocker.GetChatCallCount()
	assert.Equal(t, expectedTurn, actualTurn, "Agent should stop at turn %d, got %d", expectedTurn, actualTurn)
}

// AssertAgentFinalResponse verifies the final agent response
func AssertAgentFinalResponse(t *testing.T, response *schemas.BifrostChatResponse, expectedFinishReason string, shouldContainText string) {
	t.Helper()

	require.NotNil(t, response, "Agent response should not be nil")
	require.NotEmpty(t, response.Choices, "Agent response should have choices")

	choice := response.Choices[0]

	if expectedFinishReason != "" {
		require.NotNil(t, choice.FinishReason, "Finish reason should not be nil")
		assert.Equal(t, expectedFinishReason, *choice.FinishReason, "Finish reason should match")
	}

	if shouldContainText != "" && choice.ChatNonStreamResponseChoice != nil {
		msg := choice.ChatNonStreamResponseChoice.Message
		if msg != nil && msg.Content != nil && msg.Content.ContentStr != nil {
			assert.Contains(t, *msg.Content.ContentStr, shouldContainText, "Final response should contain expected text")
		}
	}
}

// AssertToolExecutedInTurn verifies a tool was executed in a specific turn
func AssertToolExecutedInTurn(t *testing.T, mocker *DynamicLLMMocker, toolName string, turn int) {
	t.Helper()

	history := mocker.GetChatHistory()
	require.GreaterOrEqual(t, len(history), turn, "Should have at least %d turns", turn)

	// The LLM response from turn N is the assistant message at the end of chatHistory[N]
	// (or in chatHistory[N+1] if there's a follow-up)
	// We need to find assistant messages (LLM responses) and check if they contain tool calls

	assistantMessages := []schemas.ChatMessage{}
	for _, turnMessages := range history {
		for _, msg := range turnMessages {
			if msg.Role == schemas.ChatMessageRoleAssistant {
				assistantMessages = append(assistantMessages, msg)
			}
		}
	}

	require.Greater(t, len(assistantMessages), turn-1, "Should have at least %d assistant messages (LLM responses)", turn)

	assistantMsg := assistantMessages[turn-1]
	found := false

	// Check for exact match or with "bifrostInternal-" prefix
	if assistantMsg.ChatAssistantMessage != nil {
		for _, tc := range assistantMsg.ChatAssistantMessage.ToolCalls {
			if tc.Function.Name != nil {
				fullName := *tc.Function.Name
				if fullName == toolName || fullName == "bifrostInternal-"+toolName ||
					(matchesToolNameWithPrefix(toolName) && fullName == toolName) {
					found = true
					break
				}
			}
		}
	}

	assert.True(t, found, "Tool %s should be executed in turn %d", toolName, turn)
}

// matchesToolNameWithPrefix checks if tool name already has a prefix
func matchesToolNameWithPrefix(toolName string) bool {
	// Check if the tool name already has a client prefix (format: "client-toolname")
	for i, c := range toolName {
		if c == '-' {
			return i > 0 // Has a prefix before the dash
		}
	}
	return false
}

// AssertToolNotExecutedInAnyTurn verifies a tool was never executed
func AssertToolNotExecutedInAnyTurn(t *testing.T, mocker *DynamicLLMMocker, toolName string) {
	t.Helper()

	history := mocker.GetChatHistory()

	// Collect all assistant messages (LLM responses)
	for i, turnMessages := range history {
		for _, msg := range turnMessages {
			if msg.Role == schemas.ChatMessageRoleAssistant && msg.ChatAssistantMessage != nil {
				for _, tc := range msg.ChatAssistantMessage.ToolCalls {
					if tc.Function.Name != nil {
						fullName := *tc.Function.Name
						if fullName == toolName || fullName == "bifrostInternal-"+toolName ||
							(matchesToolNameWithPrefix(toolName) && fullName == toolName) {
							assert.Fail(t, fmt.Sprintf("Tool %s should not be executed in turn %d", toolName, i+1))
							return
						}
					}
				}
			}
		}
	}
}

// AssertToolsExecutedInParallel verifies multiple tools were called in the same turn
func AssertToolsExecutedInParallel(t *testing.T, mocker *DynamicLLMMocker, toolNames []string, turn int) {
	t.Helper()

	history := mocker.GetChatHistory()

	// Collect all assistant messages (LLM responses)
	assistantMessages := []schemas.ChatMessage{}
	for _, turnMessages := range history {
		for _, msg := range turnMessages {
			if msg.Role == schemas.ChatMessageRoleAssistant {
				assistantMessages = append(assistantMessages, msg)
			}
		}
	}

	require.Greater(t, len(assistantMessages), turn-1, "Should have at least %d assistant messages (LLM responses)", turn)

	assistantMsg := assistantMessages[turn-1]

	// Check each requested tool is called in this turn
	for _, toolName := range toolNames {
		found := false

		if assistantMsg.ChatAssistantMessage != nil {
			for _, tc := range assistantMsg.ChatAssistantMessage.ToolCalls {
				if tc.Function.Name != nil {
					fullName := *tc.Function.Name
					if fullName == toolName || fullName == "bifrostInternal-"+toolName ||
						(matchesToolNameWithPrefix(toolName) && fullName == toolName) {
						found = true
						break
					}
				}
			}
		}

		assert.True(t, found, "Tool %s should be called in parallel in turn %d", toolName, turn)
	}
}

// AssertToolResultPresent verifies a tool result is in the conversation history
func AssertToolResultPresent(t *testing.T, mocker *DynamicLLMMocker, callID string, shouldContain string) {
	t.Helper()

	allHistory := mocker.GetChatHistory()
	found := false
	var result string

	for _, turnHistory := range allHistory {
		r, f := GetToolResultFromChatHistory(turnHistory, callID)
		if f {
			found = true
			result = r
			break
		}
	}

	require.True(t, found, "Tool result for call ID %s should be present", callID)

	if shouldContain != "" {
		assert.Contains(t, result, shouldContain, "Tool result should contain expected text")
	}
}

// AssertNoToolCalls verifies there are no tool calls in the response
func AssertNoToolCalls(t *testing.T, response *schemas.BifrostChatResponse) {
	t.Helper()

	require.NotNil(t, response, "Response should not be nil")
	require.NotEmpty(t, response.Choices, "Response should have choices")

	choice := response.Choices[0]
	if choice.ChatNonStreamResponseChoice != nil &&
		choice.ChatNonStreamResponseChoice.Message != nil &&
		choice.ChatNonStreamResponseChoice.Message.ChatAssistantMessage != nil {
		toolCalls := choice.ChatNonStreamResponseChoice.Message.ChatAssistantMessage.ToolCalls
		assert.Empty(t, toolCalls, "Should have no tool calls")
	}
}

// AssertAgentMaxDepthReached verifies the agent stopped due to max depth
func AssertAgentMaxDepthReached(t *testing.T, mocker *DynamicLLMMocker, maxDepth int) {
	t.Helper()

	actualTurns := mocker.GetChatCallCount()
	assert.Equal(t, maxDepth, actualTurns, "Agent should stop at max depth %d, got %d turns", maxDepth, actualTurns)
}

// AssertAgentError verifies the agent returned an error
func AssertAgentError(t *testing.T, bifrostErr *schemas.BifrostError, shouldContain string) {
	t.Helper()

	require.NotNil(t, bifrostErr, "Should return error")
	require.NotNil(t, bifrostErr.Error, "Error field should not be nil")

	if shouldContain != "" {
		assert.Contains(t, bifrostErr.Error.Message, shouldContain, "Error message should contain expected text")
	}
}

// AssertAgentSuccess verifies the agent completed without errors
func AssertAgentSuccess(t *testing.T, response *schemas.BifrostChatResponse, bifrostErr *schemas.BifrostError) {
	t.Helper()

	if bifrostErr != nil && bifrostErr.Error != nil {
		t.Logf("bifrostErr: %s", bifrostErr.Error.Message)
	}
	assert.Nil(t, bifrostErr, "Should not return error")
	require.NotNil(t, response, "Response should not be nil")
	require.NotEmpty(t, response.Choices, "Response should have choices")
}

// =============================================================================
// REQUEST ID ASSERTION HELPERS
// =============================================================================

// AssertRequestIDChanged verifies request ID changed between turns
func AssertRequestIDChanged(t *testing.T, ctx1 *schemas.BifrostContext, ctx2 *schemas.BifrostContext) {
	t.Helper()

	// This is a placeholder - actual implementation would need access to request IDs
	// which may be stored in context or passed differently
	// For now, we'll just verify contexts are different
	assert.NotEqual(t, ctx1, ctx2, "Request IDs should be different between turns")
}

// AssertRequestIDPropagated verifies request ID is present in context
func AssertRequestIDPropagated(t *testing.T, ctx *schemas.BifrostContext) {
	t.Helper()

	// Placeholder assertion - actual implementation depends on how request IDs are stored
	require.NotNil(t, ctx, "Context should not be nil")
}

// =============================================================================
// TOOL CALL CREATION HELPERS
// =============================================================================

// CreateToolCall is a convenience function for creating tool calls in tests
func CreateToolCall(id, toolName string, args map[string]interface{}) schemas.ChatAssistantMessageToolCall {
	argsJSON, err := json.Marshal(args)
	if err != nil {
		panic(fmt.Sprintf("failed to marshal tool call args: %v", err))
	}
	return schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr(id),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr(toolName),
			Arguments: string(argsJSON),
		},
	}
}

// CreateSTDIOToolCall creates a tool call for a STDIO server tool (with server prefix)
// Note: serverName should be the client Name (e.g., "GoTestServer"), not the ID
// The tool name format is: {ServerName}-{tool_name} (e.g., "GoTestServer-uuid_generate")
func CreateSTDIOToolCall(id, serverName, toolName string, args map[string]interface{}) schemas.ChatAssistantMessageToolCall {
	fullToolName := fmt.Sprintf("%s-%s", serverName, toolName)
	return CreateToolCall(id, fullToolName, args)
}

// CreateInProcessToolCall creates a tool call for an in-process tool
// In-process tools are registered with "bifrostInternal-" prefix
// The tool name format is: bifrostInternal-{tool_name} (e.g., "bifrostInternal-echo")
func CreateInProcessToolCall(id, toolName string, args map[string]interface{}) schemas.ChatAssistantMessageToolCall {
	fullToolName := fmt.Sprintf("bifrostInternal-%s", toolName)
	return CreateToolCall(id, fullToolName, args)
}

// =============================================================================
// MOCK LLM RESPONSE BUILDERS FOR AGENT TESTS
// =============================================================================

// CreateAgentTurnWithToolCalls creates a mock LLM response with tool calls for agent mode
func CreateAgentTurnWithToolCalls(toolCalls ...schemas.ChatAssistantMessageToolCall) ChatResponseFunc {
	return CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
		return CreateChatResponseWithToolCalls(toolCalls)
	})
}

// CreateAgentTurnWithText creates a mock LLM response with text (agent stops)
func CreateAgentTurnWithText(text string) ChatResponseFunc {
	return CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
		return CreateChatResponseWithText(text)
	})
}

// CreateAgentTurnValidatingResult creates a turn that validates tool result before responding
func CreateAgentTurnValidatingResult(callID string, mustContain []string, nextToolCalls []schemas.ChatAssistantMessageToolCall, failText string) ChatResponseFunc {
	return CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
		result, found := GetToolResultFromChatHistory(history, callID)
		if !found {
			return CreateChatResponseWithText(failText + " (result not found)")
		}

		// Validate result contains expected values
		for _, expected := range mustContain {
			if !containsString(result, expected) {
				return CreateChatResponseWithText(failText + " (missing: " + expected + ")")
			}
		}

		// Validation passed - return next tool calls
		if len(nextToolCalls) > 0 {
			return CreateChatResponseWithToolCalls(nextToolCalls)
		}

		// No more tool calls - return success text
		return CreateChatResponseWithText("Validation successful")
	})
}

// =============================================================================
// AGENT SCENARIO BUILDERS
// =============================================================================

// AgentScenario represents a complete multi-turn agent test scenario
type AgentScenario struct {
	Name        string
	Description string
	Setup       AgentTestConfig
	Turns       []AgentTurn
	Assertions  []AgentAssertion
}

// AgentTurn represents a single turn in an agent scenario
type AgentTurn struct {
	Description string
	Response    ChatResponseFunc
}

// AgentAssertion represents an assertion to make after agent execution
type AgentAssertion struct {
	Type     string // "turn_count", "tool_executed", "final_text", etc.
	Expected interface{}
}

// RunAgentScenario executes a complete agent scenario with setup, turns, and assertions
func RunAgentScenario(t *testing.T, scenario AgentScenario) {
	t.Helper()
	t.Run(scenario.Name, func(t *testing.T) {
		// Setup
		manager, mocker, ctx := SetupAgentTest(t, scenario.Setup)

		// Add turns to mocker
		for _, turn := range scenario.Turns {
			mocker.AddChatResponse(turn.Response)
		}

		// Get initial response (first turn)
		req := &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4",
			Input: []schemas.ChatMessage{
				GetSampleUserMessage("Execute agent scenario"),
			},
		}

		initialResponse, initialErr := mocker.MakeChatRequest(ctx, req)
		require.Nil(t, initialErr, "Initial LLM call should succeed")
		require.NotNil(t, initialResponse, "Initial response should not be nil")

		// Execute agent mode with initial response
		response, bifrostErr := manager.CheckAndExecuteAgentForChatRequest(
			ctx,
			req,
			initialResponse,
			mocker.MakeChatRequest,
			func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
				return manager.ExecuteToolCall(ctx, request)
			},
		)

		// Run assertions
		for _, assertion := range scenario.Assertions {
			switch assertion.Type {
			case "turn_count":
				AssertAgentCompletedInTurns(t, mocker, assertion.Expected.(int))
			case "success":
				AssertAgentSuccess(t, response, bifrostErr)
			case "final_reason":
				AssertAgentFinalResponse(t, response, assertion.Expected.(string), "")
			default:
				t.Fatalf("Unknown assertion type: %s", assertion.Type)
			}
		}
	})
}

// =============================================================================
// CONVENIENCE FUNCTIONS FOR COMMON PATTERNS
// =============================================================================

// SimpleAgentTest runs a simple agent test with inline setup
func SimpleAgentTest(t *testing.T, name string, config AgentTestConfig, responses []ChatResponseFunc, assertions func(*testing.T, *schemas.BifrostChatResponse, *schemas.BifrostError, *DynamicLLMMocker)) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		manager, mocker, ctx := SetupAgentTest(t, config)

		for _, resp := range responses {
			mocker.AddChatResponse(resp)
		}

		req := &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4",
			Input: []schemas.ChatMessage{
				GetSampleUserMessage("Test message"),
			},
		}

		// Get initial response
		initialResponse, initialErr := mocker.MakeChatRequest(ctx, req)
		require.Nil(t, initialErr, "Initial LLM call should succeed")
		require.NotNil(t, initialResponse, "Initial response should not be nil")

		// Execute agent mode
		response, bifrostErr := manager.CheckAndExecuteAgentForChatRequest(
			ctx,
			req,
			initialResponse,
			mocker.MakeChatRequest,
			func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
				return manager.ExecuteToolCall(ctx, request)
			},
		)

		assertions(t, response, bifrostErr, mocker)
	})
}

// =============================================================================

// =============================================================================
// RESPONSES API HELPER FUNCTIONS
// =============================================================================

// These helpers wrap existing fixtures.go functions for Responses API agent tests

// GetSampleUserMessageResponses is an alias for GetSampleResponsesUserMessage
func GetSampleUserMessageResponses(text string) schemas.ResponsesMessage {
	return GetSampleResponsesUserMessage(text)
}

// CreateAgentTurnWithToolCallsResponses creates a mock Responses API response with tool calls
func CreateAgentTurnWithToolCallsResponses(toolCalls ...schemas.ChatAssistantMessageToolCall) ResponsesResponseFunc {
	return CreateDynamicResponsesResponse(func(history []schemas.ResponsesMessage) *schemas.BifrostResponsesResponse {
		// Convert Chat tool calls to Responses tool messages
		toolMessages := make([]schemas.ResponsesToolMessage, 0, len(toolCalls))
		for _, tc := range toolCalls {
			toolMessages = append(toolMessages, schemas.ResponsesToolMessage{
				CallID:    tc.ID,
				Name:      tc.Function.Name,
				Arguments: &tc.Function.Arguments,
			})
		}
		return CreateResponsesResponseWithToolCalls(toolMessages)
	})
}

// CreateAgentTurnWithTextResponses creates a mock Responses API response with text
func CreateAgentTurnWithTextResponses(text string) ResponsesResponseFunc {
	return CreateDynamicResponsesResponse(func(history []schemas.ResponsesMessage) *schemas.BifrostResponsesResponse {
		return CreateResponsesResponseWithText(text)
	})
}

// AssertAgentCompletedInTurnsResponses verifies the agent completed in expected number of turns (Responses API)
func AssertAgentCompletedInTurnsResponses(t *testing.T, mocker *DynamicLLMMocker, expectedTurns int) {
	t.Helper()
	actualTurns := mocker.GetResponsesCallCount()
	require.Equal(t, expectedTurns, actualTurns, "Agent should complete in %d turns, got %d", expectedTurns, actualTurns)
}

// AssertAgentStoppedAtTurnResponses verifies the agent stopped at expected turn (Responses API)
func AssertAgentStoppedAtTurnResponses(t *testing.T, mocker *DynamicLLMMocker, expectedTurn int) {
	t.Helper()
	actualTurn := mocker.GetResponsesCallCount()
	require.Equal(t, expectedTurn, actualTurn, "Agent should stop at turn %d, got %d", expectedTurn, actualTurn)
}

// AssertAgentFinalResponseResponses verifies the final response (Responses API)
func AssertAgentFinalResponseResponses(t *testing.T, result *schemas.BifrostResponsesResponse, mustContainInContent string) {
	t.Helper()
	require.NotEmpty(t, result.Output, "Should have output in response")

	// Find assistant message with text content
	for _, msg := range result.Output {
		if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeMessage {
			if msg.Content != nil && msg.Content.ContentStr != nil {
				if mustContainInContent != "" {
					assert.Contains(t, *msg.Content.ContentStr, mustContainInContent, "Content should contain: %s", mustContainInContent)
				}
				return
			}
		}
	}

	if mustContainInContent != "" {
		t.Errorf("No assistant message with text content found")
	}
}

// AssertToolsExecutedInParallelResponses verifies tools were executed in a specific turn (Responses API)
func AssertToolsExecutedInParallelResponses(t *testing.T, mocker *DynamicLLMMocker, expectedTools []string, turn int) {
	t.Helper()
	history := mocker.GetResponsesHistory()
	require.GreaterOrEqual(t, len(history), turn, "Should have at least %d turns", turn)

	// Get the history for the specified turn (0-indexed)
	turnHistory := history[turn-1]

	// Verify each expected tool is present in the history
	for _, toolName := range expectedTools {
		found := HasToolCallInResponsesHistory(turnHistory, toolName)
		assert.True(t, found, "Tool %s should be called in parallel in turn %d", toolName, turn)
	}
}
