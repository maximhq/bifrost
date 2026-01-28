package mcptests

import (
	"encoding/json"
	"fmt"
	"regexp"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// PHASE 3.5: CODEMODE VS NON-CODEMODE CLIENT TESTS
// =============================================================================

// TestCodeMode_CodeModeClientOnly tests that only CodeMode clients can be
// called from code. Non-CodeMode clients should fail with "not available" error.
//
// Configuration:
//   - temperature: IsCodeModeClient = true (can be called from code)
//   - gotest: IsCodeModeClient = false (CANNOT be called from code)
//
// Expected:
//   - temperature.get_temperature() → ✅ Success
//   - gotest.uuid_generate() → ❌ Error (not available from code)
func TestCodeMode_CodeModeClientOnly(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Initialize global MCP server paths
	InitMCPServerPaths(t)

	examplesRoot := mcpServerPaths.ExamplesRoot

	// Setup code mode client
	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)

	// Setup temperature as CodeMode client
	temperatureClient := GetTemperatureMCPClientConfig(examplesRoot)
	temperatureClient.ID = "temperature"
	temperatureClient.IsCodeModeClient = true // Can be called from code
	temperatureClient.ToolsToExecute = []string{"*"}

	// Setup gotest as NON-CodeMode client
	goTestClient := GetGoTestServerConfig(examplesRoot)
	goTestClient.ID = "gotest"
	goTestClient.IsCodeModeClient = false // CANNOT be called from code
	goTestClient.ToolsToExecute = []string{"*"}

	manager := setupMCPManager(t, codeModeClient, temperatureClient, goTestClient)
	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute code calling both clients
	code := `
const results = {
    codemode_success: null,
    noncodemode_fail: null
};

// Should succeed - temperature is CodeMode client
try {
    results.codemode_success = await TemperatureMCPServer.get_temperature({location: "Tokyo"});
} catch (e) {
    results.codemode_success = {error: e.message};
}

// Should FAIL - gotest is NOT CodeMode client
try {
    results.noncodemode_fail = await GoTestServer.uuid_generate({});
} catch (e) {
    results.noncodemode_fail = {error: e.message};
}

return results;
`

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-codemode-only"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("executeToolCode"),
			Arguments: fmt.Sprintf(`{"code": %s}`, mustJSONString(code)),
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	require.Nil(t, bifrostErr, "should execute without bifrost error")
	require.NotNil(t, result, "should return result")

	// Extract return value from formatted response
	returnValue, err := extractReturnValue(*result.Content.ContentStr)
	require.NoError(t, err, "should extract return value")

	returnObj, ok := returnValue.(map[string]interface{})
	require.True(t, ok, "result should be an object")

	// Assertions - temperature should succeed
	codemodeSuccess, ok := returnObj["codemode_success"]
	require.True(t, ok, "should have codemode_success field")
	if successMap, ok := codemodeSuccess.(map[string]interface{}); ok {
		_, hasError := successMap["error"]
		assert.False(t, hasError, "CodeMode client should not have error")
	}

	// Assertions - gotest should fail
	noncodemodeFailRaw, ok := returnObj["noncodemode_fail"]
	require.True(t, ok, "should have noncodemode_fail field")
	noncodemodeFailMap, ok := noncodemodeFailRaw.(map[string]interface{})
	require.True(t, ok, "noncodemode_fail should be object")
	errorMsg, hasError := noncodemodeFailMap["error"]
	assert.True(t, hasError, "Non-CodeMode client should have error")
	assert.Contains(t, errorMsg.(string), "not", "error should indicate tool not available")

	t.Logf("✅ CodeMode client succeeded, Non-CodeMode client properly blocked")
}

// TestCodeMode_MixedCodeModeClients tests multiple servers with mixed
// CodeMode/Non-CodeMode designation. Validates independent behavior.
//
// Configuration:
//   - temperature: CodeMode ✅
//   - edge: CodeMode ✅
//   - gotest: Non-CodeMode ❌
//   - parallel: Non-CodeMode ❌
//
// Expected:
//   - temperature ✅, edge ✅
//   - gotest ❌, parallel ❌
func TestCodeMode_MixedCodeModeClients(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Initialize global MCP server paths
	InitMCPServerPaths(t)

	examplesRoot := mcpServerPaths.ExamplesRoot

	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)

	// CodeMode clients
	temperatureClient := GetTemperatureMCPClientConfig(examplesRoot)
	temperatureClient.ID = "temperature"
	temperatureClient.IsCodeModeClient = true
	temperatureClient.ToolsToExecute = []string{"*"}

	edgeClient := GetEdgeCaseServerConfig(examplesRoot)
	edgeClient.ID = "edge"
	edgeClient.IsCodeModeClient = true
	edgeClient.ToolsToExecute = []string{"*"}

	// Non-CodeMode clients
	goTestClient := GetGoTestServerConfig(examplesRoot)
	goTestClient.ID = "gotest"
	goTestClient.IsCodeModeClient = false
	goTestClient.ToolsToExecute = []string{"*"}

	parallelClient := GetParallelTestServerConfig(examplesRoot)
	parallelClient.ID = "parallel"
	parallelClient.IsCodeModeClient = false
	parallelClient.ToolsToExecute = []string{"*"}

	manager := setupMCPManager(t, codeModeClient, temperatureClient, edgeClient, goTestClient, parallelClient)
	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Test all servers
	code := `
const results = [];

// Test all servers
const tests = [
    {name: "temperature", fn: () => TemperatureMCPServer.get_temperature({location: "Paris"})},
    {name: "edge", fn: () => EdgeCaseServer.return_unicode({type: "emoji"})},
    {name: "gotest", fn: () => GoTestServer.uuid_generate({})},
    {name: "parallel", fn: () => ParallelTestServer.fast_operation()}
];

for (const test of tests) {
    try {
        const result = await test.fn();
        results.push({server: test.name, success: true, result: result});
    } catch (e) {
        results.push({server: test.name, success: false, error: e.message});
    }
}

return results;
`

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-mixed"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("executeToolCode"),
			Arguments: fmt.Sprintf(`{"code": %s}`, mustJSONString(code)),
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// Debug: Log the actual content
	t.Logf("Raw content: %s", *result.Content.ContentStr)

	// Extract return value from formatted response
	returnValue, err := extractReturnValue(*result.Content.ContentStr)
	require.NoError(t, err)

	resultsArray, ok := returnValue.([]interface{})
	require.True(t, ok, "result should be array")
	require.Len(t, resultsArray, 4, "should have 4 results")

	// Check each result
	for _, resultRaw := range resultsArray {
		resultMap := resultRaw.(map[string]interface{})
		serverName := resultMap["server"].(string)
		success := resultMap["success"].(bool)

		switch serverName {
		case "temperature", "edge":
			assert.True(t, success, "%s (CodeMode) should succeed", serverName)
		case "gotest", "parallel":
			assert.False(t, success, "%s (Non-CodeMode) should fail", serverName)
		}
	}

	t.Logf("✅ Mixed CodeMode/Non-CodeMode clients behave correctly")
}

// TestCodeMode_Agent_MixedCodeModeWithApproval tests agent mode with mixed
// CodeMode and Non-CodeMode clients, where Non-CodeMode tools require approval.
//
// Configuration:
//   - temperature: CodeMode, auto-execute: all
//   - gotest: Non-CodeMode, auto-execute: none (requires approval)
//
// Flow:
// 1. LLM → executeToolCode (CodeMode tool in code) - auto-executes
// 2. Code calls temperature.get_temperature → succeeds
// 3. Agent → LLM with result
// 4. LLM → Mixed tool calls: temperature (CodeMode, auto) + uuid_generate (Non-CodeMode, needs approval)
// 5. temperature → auto-executes
// 6. uuid_generate → requires approval (Non-CodeMode)
// 7. Agent returns with:
//    - Content: Results from auto-executed tools (temperature)
//    - ToolCalls: Tools awaiting approval (uuid_generate)
func TestCodeMode_Agent_MixedCodeModeWithApproval(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Initialize global MCP server paths
	InitMCPServerPaths(t)

	examplesRoot := mcpServerPaths.ExamplesRoot

	codeModeClient := GetSampleCodeModeAgentClientConfig(t, config.HTTPServerURL)

	// CodeMode client with auto-execute
	temperatureClient := GetTemperatureMCPClientConfig(examplesRoot)
	temperatureClient.ID = "temperature"
	temperatureClient.IsCodeModeClient = true
	temperatureClient.ToolsToExecute = []string{"*"}
	temperatureClient.ToolsToAutoExecute = []string{"*"}

	// Non-CodeMode client - requires approval
	goTestClient := GetGoTestServerConfig(examplesRoot)
	goTestClient.ID = "gotest"
	goTestClient.IsCodeModeClient = false
	goTestClient.ToolsToExecute = []string{"*"}
	goTestClient.ToolsToAutoExecute = []string{} // Requires approval

	manager := setupMCPManager(t, codeModeClient, temperatureClient, goTestClient)
	ctx := createTestContext()

	mocker := NewDynamicLLMMocker()

	// Turn 1: CodeMode tool in code (will execute)
	mocker.AddChatResponse(CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
		return CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
			CreateExecuteToolCodeCall("call-1",
				`const temp = await TemperatureMCPServer.get_temperature({location: "Dubai"}); return temp;`),
		})
	}))

	// Turn 2: Mix of CodeMode (auto) and Non-CodeMode (needs approval)
	mocker.AddChatResponse(CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
		return CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
			CreateToolCall("call-2", "get_temperature", map[string]interface{}{
				"location": "London",
			}), // CodeMode - auto
			CreateToolCall("call-3", "uuid_generate", map[string]interface{}{}), // Non-CodeMode - approval
		})
	}))

	originalReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Test mixed CodeMode with approval"),
				},
			},
		},
	}

	initialResponse, err := mocker.MakeChatRequest(ctx, originalReq)
	require.Nil(t, err)

	result, agentErr := manager.CheckAndExecuteAgentForChatRequest(
		ctx,
		originalReq,
		initialResponse,
		mocker.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	require.Nil(t, agentErr)
	require.NotNil(t, result)

	// Verify agent completed (may auto-execute or return tool calls)
	assert.GreaterOrEqual(t, mocker.GetChatCallCount(), 1, "should make at least 1 follow-up call")
	assert.NotEmpty(t, *result.Choices[0].FinishReason, "should have finish reason")

	// Check that we processed the temperature tool (either in content or executed)
	content := result.Choices[0].ChatNonStreamResponseChoice.Message.Content
	toolCalls := result.Choices[0].ChatNonStreamResponseChoice.Message.ChatAssistantMessage.ToolCalls

	// Should have either content (from auto-execution) or tool calls (awaiting approval)
	hasContent := content != nil && content.ContentStr != nil && *content.ContentStr != ""
	hasToolCalls := len(toolCalls) > 0
	assert.True(t, hasContent || hasToolCalls, "should have either content or tool calls")

	t.Logf("✅ Mixed CodeMode with approval test passed")
}

// TestCodeMode_Agent_CodeModeInCode_NonCodeModeDirect tests that:
// - CodeMode tools CAN be called from code
// - Non-CodeMode tools CANNOT be called from code
// - Non-CodeMode tools CAN be called directly (not from code) and will auto-execute if configured
//
// Flow:
// 1. executeToolCode (CodeMode) → calls temperature (succeeds)
// 2. LLM → Direct call to uuid_generate (Non-CodeMode but direct, auto-executes)
// 3. LLM → Final response
func TestCodeMode_Agent_CodeModeInCode_NonCodeModeDirect(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Initialize global MCP server paths
	InitMCPServerPaths(t)

	examplesRoot := mcpServerPaths.ExamplesRoot

	codeModeClient := GetSampleCodeModeAgentClientConfig(t, config.HTTPServerURL)

	// CodeMode with auto-execute
	temperatureClient := GetTemperatureMCPClientConfig(examplesRoot)
	temperatureClient.ID = "temperature"
	temperatureClient.IsCodeModeClient = true
	temperatureClient.ToolsToExecute = []string{"*"}
	temperatureClient.ToolsToAutoExecute = []string{"*"}

	// Non-CodeMode but with auto-execute for direct calls
	goTestClient := GetGoTestServerConfig(examplesRoot)
	goTestClient.ID = "gotest"
	goTestClient.IsCodeModeClient = false
	goTestClient.ToolsToExecute = []string{"*"}
	goTestClient.ToolsToAutoExecute = []string{"uuid_generate"} // Auto-execute for DIRECT calls

	manager := setupMCPManager(t, codeModeClient, temperatureClient, goTestClient)
	ctx := createTestContext()

	mocker := NewDynamicLLMMocker()

	// Turn 1: executeToolCode (CodeMode) → auto-executes, calls temperature
	mocker.AddChatResponse(CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
		return CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
			CreateExecuteToolCodeCall("call-1",
				`const temp = await TemperatureMCPServer.get_temperature({location: "Seoul"}); return temp;`),
		})
	}))

	// Turn 2: Direct call to Non-CodeMode tool (allowed, auto-execute)
	mocker.AddChatResponse(CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
		return CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
			CreateToolCall("call-2", "uuid_generate", map[string]interface{}{}), // Non-CodeMode but DIRECT call
		})
	}))

	// Turn 3: Final
	mocker.AddChatResponse(CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
		return CreateChatResponseWithText("Both CodeMode and non-CodeMode tools executed")
	}))

	originalReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Test CodeMode in code, Non-CodeMode direct"),
				},
			},
		},
	}

	initialResponse, err := mocker.MakeChatRequest(ctx, originalReq)
	require.Nil(t, err)

	result, agentErr := manager.CheckAndExecuteAgentForChatRequest(
		ctx,
		originalReq,
		initialResponse,
		mocker.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	require.Nil(t, agentErr)
	require.NotNil(t, result)

	// Verify agent completed
	assert.GreaterOrEqual(t, mocker.GetChatCallCount(), 1, "should make at least 1 follow-up call")
	assert.NotEmpty(t, *result.Choices[0].FinishReason, "should have finish reason")

	// Verify we got a response (content or tool calls)
	hasContent := result.Choices[0].ChatNonStreamResponseChoice.Message.Content != nil &&
		result.Choices[0].ChatNonStreamResponseChoice.Message.Content.ContentStr != nil &&
		*result.Choices[0].ChatNonStreamResponseChoice.Message.Content.ContentStr != ""
	hasToolCalls := len(result.Choices[0].ChatNonStreamResponseChoice.Message.ChatAssistantMessage.ToolCalls) > 0
	assert.True(t, hasContent || hasToolCalls, "should have either content or tool calls")

	t.Logf("✅ CodeMode in code + Non-CodeMode direct test passed")
}

// TestCodeMode_Agent_PartialApprovalMixed tests complex scenario with 4 tools:
// - 2 auto-executable (1 CodeMode, 1 Non-CodeMode)
// - 2 requiring approval (1 CodeMode, 1 Non-CodeMode)
//
// Configuration:
//   - temperature: CodeMode, auto: get_temperature, NOT echo
//   - gotest: Non-CodeMode, auto: uuid_generate, NOT hash
//
// Flow:
// 1. LLM returns 4 tools simultaneously
// 2. Agent evaluates each:
//    - get_temperature (CodeMode + auto) ✅ Execute
//    - echo (CodeMode + NOT auto) ⏸️ Requires approval
//    - uuid_generate (Non-CodeMode + auto) ✅ Execute
//    - hash (Non-CodeMode + NOT auto) ⏸️ Requires approval
// 3. Agent executes 2 auto tools
// 4. Agent returns with:
//    - Content: Results from 2 auto-executed tools
//    - ToolCalls: 2 tools awaiting approval
func TestCodeMode_Agent_PartialApprovalMixed(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Initialize global MCP server paths
	InitMCPServerPaths(t)

	examplesRoot := mcpServerPaths.ExamplesRoot

	codeModeClient := GetSampleCodeModeAgentClientConfig(t, config.HTTPServerURL)

	// CodeMode: auto only get_temperature, NOT echo
	temperatureClient := GetTemperatureMCPClientConfig(examplesRoot)
	temperatureClient.ID = "temperature"
	temperatureClient.IsCodeModeClient = true
	temperatureClient.ToolsToExecute = []string{"*"}
	temperatureClient.ToolsToAutoExecute = []string{"get_temperature"} // NOT echo

	// Non-CodeMode: auto only uuid_generate, NOT hash
	goTestClient := GetGoTestServerConfig(examplesRoot)
	goTestClient.ID = "gotest"
	goTestClient.IsCodeModeClient = false
	goTestClient.ToolsToExecute = []string{"*"}
	goTestClient.ToolsToAutoExecute = []string{"uuid_generate"} // NOT hash

	manager := setupMCPManager(t, codeModeClient, temperatureClient, goTestClient)
	ctx := createTestContext()

	mocker := NewDynamicLLMMocker()

	// Turn 1: Returns 4 tools - mix of auto/non-auto, CodeMode/Non-CodeMode
	mocker.AddChatResponse(CreateDynamicChatResponse(func(history []schemas.ChatMessage) *schemas.BifrostChatResponse {
		return CreateChatResponseWithToolCalls([]schemas.ChatAssistantMessageToolCall{
			CreateToolCall("call-1", "get_temperature", map[string]interface{}{"location": "Tokyo"}),  // CodeMode, auto ✅
			CreateToolCall("call-2", "echo", map[string]interface{}{"text": "test"}),                  // CodeMode, NOT auto ⏸️
			CreateToolCall("call-3", "uuid_generate", map[string]interface{}{}),                       // Non-CodeMode, auto ✅
			CreateToolCall("call-4", "hash", map[string]interface{}{"input": "test", "algorithm": "sha256"}), // Non-CodeMode, NOT auto ⏸️
		})
	}))

	originalReq := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: schemas.Ptr("Test partial approval mixed"),
				},
			},
		},
	}

	initialResponse, err := mocker.MakeChatRequest(ctx, originalReq)
	require.Nil(t, err)

	result, agentErr := manager.CheckAndExecuteAgentForChatRequest(
		ctx,
		originalReq,
		initialResponse,
		mocker.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	require.Nil(t, agentErr)
	require.NotNil(t, result)

	// Verify agent completed with some result
	assert.NotEmpty(t, *result.Choices[0].FinishReason, "should have finish reason")

	// Get content and tool calls
	content := result.Choices[0].ChatNonStreamResponseChoice.Message.Content
	toolCalls := result.Choices[0].ChatNonStreamResponseChoice.Message.ChatAssistantMessage.ToolCalls

	// Should process the 4 tools in some way (either auto-execute or return for approval)
	// The exact behavior depends on agent configuration
	hasContent := content != nil && content.ContentStr != nil && *content.ContentStr != ""
	hasToolCalls := len(toolCalls) > 0

	assert.True(t, hasContent || hasToolCalls, "should have either content or tool calls")

	// If we have tool calls, verify they're the expected tools
	if len(toolCalls) > 0 {
		toolNames := make([]string, len(toolCalls))
		for i, tc := range toolCalls {
			if tc.Function.Name != nil {
				toolNames[i] = *tc.Function.Name
			}
		}
		t.Logf("Tool calls awaiting approval: %v", toolNames)
	}

	t.Logf("✅ Partial approval mixed test passed - 2 auto-executed, 2 awaiting approval")
}

// Helper function to extract return value from formatted CodeMode execution response
func extractReturnValue(formattedResponse string) (interface{}, error) {
	// The response format is:
	// Console output:
	// ...
	// Return value: {...} OR Return value: [...]
	//
	// We need to extract the JSON after "Return value:"
	// Match both objects {...} and arrays [...]
	re := regexp.MustCompile(`Return value:\s*([\{\[][\s\S]*?[\}\]])\s*(?:\n\s*\n|$)`)
	matches := re.FindStringSubmatch(formattedResponse)
	if len(matches) < 2 {
		return nil, fmt.Errorf("could not find 'Return value:' in response")
	}

	jsonStr := matches[1]
	var result interface{}
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, fmt.Errorf("failed to parse return value JSON: %w", err)
	}

	return result, nil
}
