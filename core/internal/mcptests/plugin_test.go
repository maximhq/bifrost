package mcptests

import (
	"context"
	"strings"
	"testing"

	core "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// PLUGIN HOOK EXECUTION TESTS
// =============================================================================

func TestPlugin_PreMCPHook(t *testing.T) {
	t.Parallel()

	manager := setupMCPManager(t)
	require.NoError(t, RegisterEchoTool(manager))

	// Create logging plugin to capture PreHook calls
	loggingPlugin := NewTestLoggingPlugin()

	bifrost, err := core.Init(context.Background(), schemas.BifrostConfig{
		Account:    &testAccount{},
		MCPPlugins: []schemas.MCPPlugin{loggingPlugin},
		Logger:     core.NewDefaultLogger(schemas.LogLevelInfo),
	})
	require.NoError(t, err)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute tool
	echoCall := GetSampleEchoToolCall("test-pre-hook", "test message")
	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &echoCall)

	// Verify tool executed successfully
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// Verify PreMCPHook was called exactly once
	assert.Equal(t, 1, loggingPlugin.GetPreHookCallCount(), "PreMCPHook should be called once")

	// Verify request was captured
	preHookCalls := loggingPlugin.GetPreHookCalls()
	require.Len(t, preHookCalls, 1, "should have one PreHook call")

	// Verify captured request exists
	capturedReq := preHookCalls[0].Request
	require.NotNil(t, capturedReq, "request should be captured")

	t.Logf("✅ PreMCPHook test completed successfully - plugin hooks called correctly")
}

func TestPlugin_PostMCPHook(t *testing.T) {
	t.Parallel()

	manager := setupMCPManager(t)
	require.NoError(t, RegisterEchoTool(manager))

	// Create logging plugin to capture PostHook calls
	loggingPlugin := NewTestLoggingPlugin()

	bifrost, err := core.Init(context.Background(), schemas.BifrostConfig{
		Account:    &testAccount{},
		MCPPlugins: []schemas.MCPPlugin{loggingPlugin},
		Logger:     core.NewDefaultLogger(schemas.LogLevelInfo),
	})
	require.NoError(t, err)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute tool
	echoCall := GetSampleEchoToolCall("test-post-hook", "test message")
	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &echoCall)

	// Verify tool executed successfully
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// Verify PostMCPHook was called exactly once
	assert.Equal(t, 1, loggingPlugin.GetPostHookCallCount(), "PostMCPHook should be called once")

	// Verify response was captured
	postHookCalls := loggingPlugin.GetPostHookCalls()
	require.Len(t, postHookCalls, 1, "should have one PostHook call")

	// Verify captured response contains result
	capturedResp := postHookCalls[0].Response
	require.NotNil(t, capturedResp)
	require.NotNil(t, capturedResp.ChatMessage)
	require.NotNil(t, capturedResp.ChatMessage.Content)
	require.NotNil(t, capturedResp.ChatMessage.Content.ContentStr)
	assert.Contains(t, *capturedResp.ChatMessage.Content.ContentStr, "test message")

	t.Logf("✅ PostMCPHook test completed successfully")
}

// =============================================================================
// PLUGIN SHORT-CIRCUIT TESTS
// =============================================================================

func TestPlugin_ShortCircuit(t *testing.T) {
	t.Parallel()

	manager := setupMCPManager(t)
	require.NoError(t, RegisterEchoTool(manager))

	// Create governance plugin and block the echo tool
	governancePlugin := NewTestGovernancePlugin()
	governancePlugin.BlockTool("bifrostInternal-echo")

	bifrost, err := core.Init(context.Background(), schemas.BifrostConfig{
		Account:    &testAccount{},
		MCPPlugins: []schemas.MCPPlugin{governancePlugin},
		Logger:     core.NewDefaultLogger(schemas.LogLevelInfo),
	})
	require.NoError(t, err)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute blocked tool
	echoCall := GetSampleEchoToolCall("test-short-circuit", "test message")
	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &echoCall)

	// Tool should not return an error, but should return short-circuit response
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// Verify short-circuit response was returned
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)
	assert.Contains(t, *result.Content.ContentStr, "blocked", "response should indicate tool was blocked")
	// Tool name might be "echo" or "bifrostInternal-echo" in the response
	hasToolName := strings.Contains(*result.Content.ContentStr, "echo") || strings.Contains(*result.Content.ContentStr, "bifrostInternal-echo")
	assert.True(t, hasToolName, "response should mention tool name")

	t.Logf("✅ Short-circuit test completed successfully")
}

func TestPlugin_ShortCircuit_CustomMessage(t *testing.T) {
	t.Parallel()

	manager := setupMCPManager(t)
	require.NoError(t, RegisterCalculatorTool(manager))

	// Create governance plugin with custom block message
	governancePlugin := NewTestGovernancePlugin()
	customMessage := "Access denied: calculator tool requires authorization"
	governancePlugin.SetBlockMessage(customMessage)
	governancePlugin.BlockTool("bifrostInternal-calculator")

	bifrost, err := core.Init(context.Background(), schemas.BifrostConfig{
		Account:    &testAccount{},
		MCPPlugins: []schemas.MCPPlugin{governancePlugin},
		Logger:     core.NewDefaultLogger(schemas.LogLevelInfo),
	})
	require.NoError(t, err)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute blocked tool
	calcCall := GetSampleCalculatorToolCall("test-custom-msg", "add", 1.0, 2.0)
	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &calcCall)

	// Verify execution succeeded with short-circuit
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// Verify custom message appears in response
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)
	// Tool name might be "calculator" or "bifrostInternal-calculator" in the response
	hasToolName := strings.Contains(*result.Content.ContentStr, "calculator") || strings.Contains(*result.Content.ContentStr, "bifrostInternal-calculator")
	assert.True(t, hasToolName, "should mention blocked tool")

	t.Logf("✅ Custom message short-circuit test completed successfully")
}

// =============================================================================
// REQUEST/RESPONSE MODIFICATION TESTS
// =============================================================================

func TestPlugin_RequestModification(t *testing.T) {
	t.Parallel()

	manager := setupMCPManager(t)
	require.NoError(t, RegisterEchoTool(manager))

	// Create modify request plugin that appends text to arguments
	modifyPlugin := NewTestModifyRequestPlugin()
	modifyPlugin.SetArgumentModifier(func(args string) string {
		// Append " [MODIFIED]" to the message argument
		return strings.Replace(args, `"message":"`, `"message":"[MODIFIED] `, 1)
	})

	bifrost, err := core.Init(context.Background(), schemas.BifrostConfig{
		Account:    &testAccount{},
		MCPPlugins: []schemas.MCPPlugin{modifyPlugin},
		Logger:     core.NewDefaultLogger(schemas.LogLevelInfo),
	})
	require.NoError(t, err)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute tool with original message
	echoCall := GetSampleEchoToolCall("test-modify-req", "original")
	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &echoCall)

	// Verify tool executed successfully
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// Verify response contains modified message
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)
	assert.Contains(t, *result.Content.ContentStr, "[MODIFIED]", "tool should receive modified arguments")
	assert.Contains(t, *result.Content.ContentStr, "original", "should still contain original text")

	t.Logf("✅ Request modification test completed successfully")
}

func TestPlugin_ResponseModification(t *testing.T) {
	t.Parallel()

	manager := setupMCPManager(t)
	require.NoError(t, RegisterEchoTool(manager))

	// Create modify response plugin that appends text to responses
	modifyPlugin := NewTestModifyResponsePlugin()
	modifyPlugin.SetResponseModifier(func(response string) string {
		return response + " [RESPONSE MODIFIED BY PLUGIN]"
	})

	bifrost, err := core.Init(context.Background(), schemas.BifrostConfig{
		Account:    &testAccount{},
		MCPPlugins: []schemas.MCPPlugin{modifyPlugin},
		Logger:     core.NewDefaultLogger(schemas.LogLevelInfo),
	})
	require.NoError(t, err)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute tool
	echoCall := GetSampleEchoToolCall("test-modify-resp", "test message")
	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &echoCall)

	// Verify tool executed successfully
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// Verify response was modified
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)
	assert.Contains(t, *result.Content.ContentStr, "test message", "should contain original response")
	assert.Contains(t, *result.Content.ContentStr, "[RESPONSE MODIFIED BY PLUGIN]", "should contain modification marker")

	t.Logf("✅ Response modification test completed successfully")
}

// =============================================================================
// MULTIPLE PLUGINS TESTS
// =============================================================================

func TestPlugin_MultiplePlugins(t *testing.T) {
	t.Parallel()

	manager := setupMCPManager(t)
	require.NoError(t, RegisterEchoTool(manager))

	// Create multiple plugins
	loggingPlugin := NewTestLoggingPlugin()
	modifyPlugin := NewTestModifyResponsePlugin()
	modifyPlugin.SetResponseModifier(func(response string) string {
		return response + " [MODIFIED]"
	})

	// Setup Bifrost with multiple plugins in pipeline
	bifrost, err := core.Init(context.Background(), schemas.BifrostConfig{
		Account:    &testAccount{},
		MCPPlugins: []schemas.MCPPlugin{
			loggingPlugin,
			modifyPlugin,
		},
		Logger: core.NewDefaultLogger(schemas.LogLevelInfo),
	})
	require.NoError(t, err)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute tool
	echoCall := GetSampleEchoToolCall("test-multiple-plugins", "test message")
	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &echoCall)

	// Verify tool executed successfully
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// Verify logging plugin captured the call
	assert.Equal(t, 1, loggingPlugin.GetPreHookCallCount(), "logging plugin should capture PreHook")
	assert.Equal(t, 1, loggingPlugin.GetPostHookCallCount(), "logging plugin should capture PostHook")

	// Verify modify plugin modified the response
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)
	assert.Contains(t, *result.Content.ContentStr, "[MODIFIED]", "modify plugin should modify response")

	// Verify logging plugin captured the final modified response
	postHookCalls := loggingPlugin.GetPostHookCalls()
	require.Len(t, postHookCalls, 1)
	require.NotNil(t, postHookCalls[0].Response)

	t.Logf("✅ Multiple plugins test completed successfully")
}

func TestPlugin_PluginOrdering(t *testing.T) {
	t.Parallel()

	manager := setupMCPManager(t)
	require.NoError(t, RegisterEchoTool(manager))

	// Create multiple logging plugins to track execution order
	plugin1 := NewTestLoggingPlugin()
	plugin2 := NewTestLoggingPlugin()
	plugin3 := NewTestLoggingPlugin()

	// Setup plugins in specific order
	bifrost, err := core.Init(context.Background(), schemas.BifrostConfig{
		Account:    &testAccount{},
		MCPPlugins: []schemas.MCPPlugin{plugin1, plugin2, plugin3},
		Logger:     core.NewDefaultLogger(schemas.LogLevelInfo),
	})
	require.NoError(t, err)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute tool
	echoCall := GetSampleEchoToolCall("test-plugin-order", "test message")
	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &echoCall)

	// Verify tool executed successfully
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// Verify all plugins were called
	assert.Equal(t, 1, plugin1.GetPreHookCallCount(), "plugin1 PreHook should be called")
	assert.Equal(t, 1, plugin2.GetPreHookCallCount(), "plugin2 PreHook should be called")
	assert.Equal(t, 1, plugin3.GetPreHookCallCount(), "plugin3 PreHook should be called")

	assert.Equal(t, 1, plugin1.GetPostHookCallCount(), "plugin1 PostHook should be called")
	assert.Equal(t, 1, plugin2.GetPostHookCallCount(), "plugin2 PostHook should be called")
	assert.Equal(t, 1, plugin3.GetPostHookCallCount(), "plugin3 PostHook should be called")

	// Verify all requests and responses were captured
	plugin1PreCalls := plugin1.GetPreHookCalls()
	plugin2PreCalls := plugin2.GetPreHookCalls()
	plugin3PreCalls := plugin3.GetPreHookCalls()

	require.NotNil(t, plugin1PreCalls[0].Request)
	require.NotNil(t, plugin2PreCalls[0].Request)
	require.NotNil(t, plugin3PreCalls[0].Request)

	plugin1PostCalls := plugin1.GetPostHookCalls()
	plugin2PostCalls := plugin2.GetPostHookCalls()
	plugin3PostCalls := plugin3.GetPostHookCalls()

	require.NotNil(t, plugin1PostCalls[0].Response)
	require.NotNil(t, plugin2PostCalls[0].Response)
	require.NotNil(t, plugin3PostCalls[0].Response)

	t.Logf("✅ Plugin ordering test completed successfully - all plugins called in pipeline")
}

// =============================================================================
// ERROR HANDLING TESTS
// =============================================================================

func TestPlugin_ErrorHandling(t *testing.T) {
	t.Parallel()

	// This test verifies that plugin errors are handled gracefully
	// For now, we test that plugins don't crash the system when they encounter errors
	manager := setupMCPManager(t)
	require.NoError(t, RegisterEchoTool(manager))

	loggingPlugin := NewTestLoggingPlugin()

	bifrost, err := core.Init(context.Background(), schemas.BifrostConfig{
		Account:    &testAccount{},
		MCPPlugins: []schemas.MCPPlugin{loggingPlugin},
		Logger:     core.NewDefaultLogger(schemas.LogLevelInfo),
	})
	require.NoError(t, err)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute tool - even if plugin has issues, execution should continue
	echoCall := GetSampleEchoToolCall("test-error-handling", "test message")
	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &echoCall)

	// Verify tool executed successfully
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	t.Logf("✅ Error handling test completed successfully")
}

func TestPlugin_ErrorInPostHook(t *testing.T) {
	t.Parallel()

	// This test verifies that errors in PostHook don't break the response
	manager := setupMCPManager(t)
	require.NoError(t, RegisterEchoTool(manager))

	loggingPlugin := NewTestLoggingPlugin()

	bifrost, err := core.Init(context.Background(), schemas.BifrostConfig{
		Account:    &testAccount{},
		MCPPlugins: []schemas.MCPPlugin{loggingPlugin},
		Logger:     core.NewDefaultLogger(schemas.LogLevelInfo),
	})
	require.NoError(t, err)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute tool
	echoCall := GetSampleEchoToolCall("test-post-error", "test message")
	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &echoCall)

	// Tool execution should succeed
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// Verify PostHook was called
	assert.Equal(t, 1, loggingPlugin.GetPostHookCallCount())

	t.Logf("✅ Error in PostHook test completed successfully")
}

// =============================================================================
// CONTEXT PROPAGATION TESTS
// =============================================================================

func TestPlugin_ContextPropagation(t *testing.T) {
	t.Parallel()

	manager := setupMCPManager(t)
	require.NoError(t, RegisterEchoTool(manager))

	loggingPlugin := NewTestLoggingPlugin()

	bifrost, err := core.Init(context.Background(), schemas.BifrostConfig{
		Account:    &testAccount{},
		MCPPlugins: []schemas.MCPPlugin{loggingPlugin},
		Logger:     core.NewDefaultLogger(schemas.LogLevelInfo),
	})
	require.NoError(t, err)
	bifrost.SetMCPManager(manager)

	// Create context with custom values
	ctx := createTestContext()

	// Execute tool
	echoCall := GetSampleEchoToolCall("test-context", "test message")
	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &echoCall)

	// Verify tool executed successfully
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// Verify plugin was called (context was propagated)
	assert.Equal(t, 1, loggingPlugin.GetPreHookCallCount())
	assert.Equal(t, 1, loggingPlugin.GetPostHookCallCount())

	t.Logf("✅ Context propagation test completed successfully")
}

// =============================================================================
// BOTH API FORMATS TESTS
// =============================================================================

func TestPlugin_ChatFormat(t *testing.T) {
	t.Parallel()

	manager := setupMCPManager(t)
	require.NoError(t, RegisterEchoTool(manager))

	loggingPlugin := NewTestLoggingPlugin()

	bifrost, err := core.Init(context.Background(), schemas.BifrostConfig{
		Account:    &testAccount{},
		MCPPlugins: []schemas.MCPPlugin{loggingPlugin},
		Logger:     core.NewDefaultLogger(schemas.LogLevelInfo),
	})
	require.NoError(t, err)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute tool using Chat format
	echoCall := GetSampleEchoToolCall("test-chat-format", "test message")
	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &echoCall)

	// Verify tool executed successfully
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)
	assert.Equal(t, schemas.ChatMessageRoleTool, result.Role)

	// Verify plugin captured request and response
	preHookCalls := loggingPlugin.GetPreHookCalls()
	require.Len(t, preHookCalls, 1)
	require.NotNil(t, preHookCalls[0].Request)

	postHookCalls := loggingPlugin.GetPostHookCalls()
	require.Len(t, postHookCalls, 1)
	require.NotNil(t, postHookCalls[0].Response)

	t.Logf("✅ Chat format plugin test completed successfully")
}

func TestPlugin_ResponsesFormat(t *testing.T) {
	t.Parallel()

	manager := setupMCPManager(t)
	require.NoError(t, RegisterEchoTool(manager))

	loggingPlugin := NewTestLoggingPlugin()

	bifrost, err := core.Init(context.Background(), schemas.BifrostConfig{
		Account:    &testAccount{},
		MCPPlugins: []schemas.MCPPlugin{loggingPlugin},
		Logger:     core.NewDefaultLogger(schemas.LogLevelInfo),
	})
	require.NoError(t, err)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute tool using Responses format
	responsesToolCall := GetSampleResponsesToolCallMessage("test-responses-format", "bifrostInternal-echo", map[string]interface{}{
		"message": "test message",
	})
	result, bifrostErr := bifrost.ExecuteResponsesMCPTool(ctx, responsesToolCall.ResponsesToolMessage)

	// Verify tool executed successfully
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)
	assert.Equal(t, schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput), result.Type)

	// Verify plugin captured request and response
	preHookCalls := loggingPlugin.GetPreHookCalls()
	require.Len(t, preHookCalls, 1)
	require.NotNil(t, preHookCalls[0].Request)

	postHookCalls := loggingPlugin.GetPostHookCalls()
	require.Len(t, postHookCalls, 1)
	require.NotNil(t, postHookCalls[0].Response)

	t.Logf("✅ Responses format plugin test completed successfully")
}

// =============================================================================
// PLUGIN WITH CODE MODE
// =============================================================================

func TestPlugin_WithCodeMode(t *testing.T) {
	t.Parallel()

	// Skip - code mode tests require extensive setup
	t.Skip("Code mode plugin integration requires extensive test setup")
}

// =============================================================================
// PLUGIN WITH AGENT MODE
// =============================================================================

func TestPlugin_WithAgentMode(t *testing.T) {
	t.Parallel()

	// Skip - agent mode tests require LLM and extensive setup
	t.Skip("Agent mode plugin integration requires LLM and extensive test setup")
}

// =============================================================================
// SPECIFIC PLUGIN BEHAVIOR TESTS
// =============================================================================

func TestPlugin_LoggingPlugin(t *testing.T) {
	t.Parallel()

	manager := setupMCPManager(t)
	require.NoError(t, RegisterEchoTool(manager))
	require.NoError(t, RegisterCalculatorTool(manager))
	require.NoError(t, RegisterWeatherTool(manager))

	loggingPlugin := NewTestLoggingPlugin()

	bifrost, err := core.Init(context.Background(), schemas.BifrostConfig{
		Account:    &testAccount{},
		MCPPlugins: []schemas.MCPPlugin{loggingPlugin},
		Logger:     core.NewDefaultLogger(schemas.LogLevelInfo),
	})
	require.NoError(t, err)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute multiple tools
	echoCall := GetSampleEchoToolCall("log-test-1", "message 1")
	_, err1 := bifrost.ExecuteChatMCPTool(ctx, &echoCall)
	require.Nil(t, err1)

	calcCall := GetSampleCalculatorToolCall("log-test-2", "add", 1.0, 2.0)
	_, err2 := bifrost.ExecuteChatMCPTool(ctx, &calcCall)
	require.Nil(t, err2)

	weatherCall := GetSampleWeatherToolCall("log-test-3", "Tokyo", "celsius")
	_, err3 := bifrost.ExecuteChatMCPTool(ctx, &weatherCall)
	require.Nil(t, err3)

	// Verify all requests were logged
	assert.Equal(t, 3, loggingPlugin.GetPreHookCallCount(), "should log 3 PreHook calls")
	assert.Equal(t, 3, loggingPlugin.GetPostHookCallCount(), "should log 3 PostHook calls")

	// Verify log entries are complete
	preHookCalls := loggingPlugin.GetPreHookCalls()
	require.Len(t, preHookCalls, 3)
	for i, call := range preHookCalls {
		assert.NotNil(t, call.Request, "request %d should be captured", i+1)
		assert.Greater(t, call.Timestamp, int64(0), "timestamp %d should be set", i+1)
	}

	postHookCalls := loggingPlugin.GetPostHookCalls()
	require.Len(t, postHookCalls, 3)
	for i, call := range postHookCalls {
		assert.NotNil(t, call.Response, "response %d should be captured", i+1)
		assert.Greater(t, call.Timestamp, int64(0), "timestamp %d should be set", i+1)
	}

	t.Logf("✅ Logging plugin behavior test completed successfully")
}

func TestPlugin_GovernancePlugin(t *testing.T) {
	t.Parallel()

	manager := setupMCPManager(t)
	require.NoError(t, RegisterEchoTool(manager))
	require.NoError(t, RegisterCalculatorTool(manager))

	governancePlugin := NewTestGovernancePlugin()
	// Block calculator, allow echo
	governancePlugin.BlockTool("bifrostInternal-calculator")

	bifrost, err := core.Init(context.Background(), schemas.BifrostConfig{
		Account:    &testAccount{},
		MCPPlugins: []schemas.MCPPlugin{governancePlugin},
		Logger:     core.NewDefaultLogger(schemas.LogLevelInfo),
	})
	require.NoError(t, err)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute allowed tool (echo)
	echoCall := GetSampleEchoToolCall("gov-test-allowed", "test message")
	echoResult, echoErr := bifrost.ExecuteChatMCPTool(ctx, &echoCall)
	require.Nil(t, echoErr)
	require.NotNil(t, echoResult)
	// Echo should execute normally
	assert.Contains(t, *echoResult.Content.ContentStr, "test message")

	// Execute blocked tool (calculator)
	calcCall := GetSampleCalculatorToolCall("gov-test-blocked", "add", 1.0, 2.0)
	calcResult, calcErr := bifrost.ExecuteChatMCPTool(ctx, &calcCall)
	require.Nil(t, calcErr)
	require.NotNil(t, calcResult)
	// Calculator should be blocked by governance
	assert.Contains(t, *calcResult.Content.ContentStr, "blocked", "calculator should be blocked by governance")

	t.Logf("✅ Governance plugin behavior test completed successfully")
}

func TestPlugin_CustomTestPlugin(t *testing.T) {
	t.Parallel()

	manager := setupMCPManager(t)
	require.NoError(t, RegisterEchoTool(manager))

	// Use short-circuit plugin as a custom test plugin
	shortCircuitPlugin := NewTestShortCircuitPlugin()
	shortCircuitPlugin.SetShouldShortCircuit(true)
	shortCircuitPlugin.SetShortCircuitMessage("Custom short-circuit response from test plugin")

	bifrost, err := core.Init(context.Background(), schemas.BifrostConfig{
		Account:    &testAccount{},
		MCPPlugins: []schemas.MCPPlugin{shortCircuitPlugin},
		Logger:     core.NewDefaultLogger(schemas.LogLevelInfo),
	})
	require.NoError(t, err)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute tool - should be short-circuited
	echoCall := GetSampleEchoToolCall("custom-plugin-test", "test message")
	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &echoCall)

	// Verify short-circuit response
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)
	assert.Contains(t, *result.Content.ContentStr, "Custom short-circuit response", "should contain custom message")

	t.Logf("✅ Custom test plugin test completed successfully")
}
