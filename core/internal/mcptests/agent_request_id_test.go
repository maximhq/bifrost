package mcptests

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/mcp/codemode/starlark"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// REQUEST ID TEST SETUP HELPERS
// =============================================================================

// setupMCPManagerWithRequestIDFunc creates an MCP manager with a custom request ID generator
func setupMCPManagerWithRequestIDFunc(t *testing.T, fetchNewRequestIDFunc func(ctx *schemas.BifrostContext) string, clientConfigs ...schemas.MCPClientConfig) *mcp.MCPManager {
	t.Helper()

	logger := &testLogger{t: t}

	// Create MCP config with request ID function
	mcpConfig := &schemas.MCPConfig{
		ClientConfigs:         clientConfigs,
		FetchNewRequestIDFunc: fetchNewRequestIDFunc,
	}

	// Create Starlark CodeMode
	starlark.SetLogger(logger)
	codeMode := starlark.NewStarlarkCodeMode(nil)

	// Create MCP manager - dependencies are injected automatically
	manager := mcp.NewMCPManager(context.Background(), *mcpConfig, nil, logger, codeMode)

	// Cleanup
	t.Cleanup(func() {
		// Remove all clients
		clients := manager.GetClients()
		for _, client := range clients {
			_ = manager.RemoveClient(client.ExecutionConfig.ID)
		}
	})

	return manager
}

// =============================================================================
// AGENT MODE: REQUEST ID PROPAGATION TESTS
// =============================================================================
//
// These tests verify that agent mode correctly propagates and updates request IDs
// through agent iterations. Request ID tracking is critical for:
// - Tracing multi-turn agent conversations
// - Plugin hooks identifying iterations
// - Logging and debugging
// - Preserving original request context
//
// Key concepts:
// - BifrostContextKeyRequestID: Current request ID (updated each iteration)
// - BifrostMCPAgentOriginalRequestID: Original request ID (preserved)
// - fetchNewRequestIDFunc: Function to generate new IDs for each iteration
//
// Related code: core/mcp/agent.go:156-158, 347-351
// =============================================================================

// TestAgent_RequestID_Propagation verifies request ID changes through iterations
// Tests that:
// - Original request ID is preserved in context
// - New request IDs are generated for each iteration
// - Request IDs are updated in context before LLM calls
// - Tool results reference the correct request ID
func TestAgent_RequestID_Propagation(t *testing.T) {
	t.Parallel()

	// Track request IDs seen during execution
	requestIDsMutex := sync.Mutex{}
	requestIDsSeen := []string{}

	// Create request ID generator
	iteration := 0
	fetchNewRequestIDFunc := func(ctx *schemas.BifrostContext) string {
		iteration++
		newID := fmt.Sprintf("req-1-iter-%d", iteration)

		// Track request IDs
		requestIDsMutex.Lock()
		requestIDsSeen = append(requestIDsSeen, newID)
		requestIDsMutex.Unlock()

		return newID
	}

	// Setup MCP manager with request ID function
	manager := setupMCPManagerWithRequestIDFunc(t, fetchNewRequestIDFunc)
	require.NoError(t, RegisterEchoTool(manager))
	require.NoError(t, SetInternalClientAutoExecute(manager, []string{"*"}))

	manager.UpdateToolManagerConfig(&schemas.MCPToolManagerConfig{
		MaxAgentDepth: 5,
	})

	// Setup context and mocker
	originalRequestID := "req-1"
	ctx := createTestContext()
	ctx.SetValue(schemas.BifrostContextKeyRequestID, originalRequestID)

	mocker := NewDynamicLLMMocker()

	// Turn 1: LLM calls echo tool
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		GetSampleEchoToolCall("call-1", "first message"),
	))

	// Turn 2: LLM calls echo tool again
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		GetSampleEchoToolCall("call-2", "second message"),
	))

	// Turn 3: LLM responds with text (agent completes)
	mocker.AddChatResponse(CreateAgentTurnWithText("All iterations completed"))

	// Execute agent
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input:    []schemas.ChatMessage{GetSampleUserMessage("Test request ID propagation")},
	}

	initialResponse, initialErr := mocker.MakeChatRequest(ctx, req)
	require.Nil(t, initialErr)
	require.NotNil(t, initialResponse)

	result, bifrostErr := manager.CheckAndExecuteAgentForChatRequest(
		ctx,
		req,
		initialResponse,
		mocker.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	// Assertions
	require.Nil(t, bifrostErr, "Should complete without error")
	require.NotNil(t, result)

	// Verify agent completed in 3 turns
	AssertAgentCompletedInTurns(t, mocker, 3)

	// Verify original request ID is preserved in context
	storedOriginalID, ok := ctx.Value(schemas.BifrostMCPAgentOriginalRequestID).(string)
	require.True(t, ok, "Original request ID should be stored in context")
	assert.Equal(t, originalRequestID, storedOriginalID, "Original request ID should match")

	// Verify current request ID has been updated
	currentRequestID, ok := ctx.Value(schemas.BifrostContextKeyRequestID).(string)
	require.True(t, ok, "Current request ID should be in context")
	// Current ID should be the last iteration's ID (req-1-iter-2, since we have 2 tool calls)
	assert.Equal(t, "req-1-iter-2", currentRequestID, "Current request ID should be from last iteration")

	// Verify request IDs were generated for each iteration
	// We should have 2 new request IDs (one for each tool call iteration)
	requestIDsMutex.Lock()
	assert.Len(t, requestIDsSeen, 2, "Should generate 2 new request IDs (one per tool call iteration)")
	assert.Equal(t, "req-1-iter-1", requestIDsSeen[0], "First iteration should have iter-1 ID")
	assert.Equal(t, "req-1-iter-2", requestIDsSeen[1], "Second iteration should have iter-2 ID")
	requestIDsMutex.Unlock()

	t.Logf("✓ Original request ID preserved: %s", storedOriginalID)
	t.Logf("✓ Current request ID updated: %s", currentRequestID)
	t.Logf("✓ Request IDs generated: %v", requestIDsSeen)
}

// TestAgent_RequestID_PreservationAcrossDepth verifies request ID handling in deep chains
// Tests that original request ID is preserved even after many iterations
func TestAgent_RequestID_PreservationAcrossDepth(t *testing.T) {
	t.Parallel()

	// Track all generated request IDs
	generatedIDs := []string{}
	idMutex := sync.Mutex{}
	originalRequestID := "deep-chain-req-001"

	fetchNewRequestIDFunc := func(ctx *schemas.BifrostContext) string {
		idMutex.Lock()
		defer idMutex.Unlock()

		newID := fmt.Sprintf("%s-depth-%d", originalRequestID, len(generatedIDs)+1)
		generatedIDs = append(generatedIDs, newID)
		return newID
	}

	// Setup MCP manager with request ID function
	manager := setupMCPManagerWithRequestIDFunc(t, fetchNewRequestIDFunc)
	require.NoError(t, RegisterEchoTool(manager))
	require.NoError(t, SetInternalClientAutoExecute(manager, []string{"*"}))

	manager.UpdateToolManagerConfig(&schemas.MCPToolManagerConfig{
		MaxAgentDepth: 10,
	})

	// Setup context and mocker
	ctx := createTestContext()
	ctx.SetValue(schemas.BifrostContextKeyRequestID, originalRequestID)

	mocker := NewDynamicLLMMocker()

	// Create 5 iterations of tool calls
	for i := 1; i <= 5; i++ {
		mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
			GetSampleEchoToolCall(fmt.Sprintf("call-%d", i), fmt.Sprintf("message %d", i)),
		))
	}

	// Final turn: text response
	mocker.AddChatResponse(CreateAgentTurnWithText("Deep chain completed"))

	// Execute agent
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input:    []schemas.ChatMessage{GetSampleUserMessage("Test deep chain")},
	}

	initialResponse, initialErr := mocker.MakeChatRequest(ctx, req)
	require.Nil(t, initialErr)

	result, bifrostErr := manager.CheckAndExecuteAgentForChatRequest(
		ctx, req, initialResponse, mocker.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	// Assertions
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// Verify original request ID is still preserved
	storedOriginalID, ok := ctx.Value(schemas.BifrostMCPAgentOriginalRequestID).(string)
	require.True(t, ok)
	assert.Equal(t, originalRequestID, storedOriginalID, "Original request ID should remain unchanged")

	// Verify we generated 5 request IDs (one per tool call iteration)
	idMutex.Lock()
	assert.Len(t, generatedIDs, 5, "Should generate 5 request IDs for 5 iterations")

	// Verify ID sequence
	for i := 0; i < 5; i++ {
		expectedID := fmt.Sprintf("%s-depth-%d", originalRequestID, i+1)
		assert.Equal(t, expectedID, generatedIDs[i], "Request ID %d should match pattern", i+1)
	}
	idMutex.Unlock()

	// Verify current request ID is the last one
	currentRequestID, ok := ctx.Value(schemas.BifrostContextKeyRequestID).(string)
	require.True(t, ok)
	assert.Equal(t, fmt.Sprintf("%s-depth-5", originalRequestID), currentRequestID)

	t.Logf("✓ Original request ID preserved through 5 iterations: %s", storedOriginalID)
	t.Logf("✓ Generated IDs: %v", generatedIDs)
}

// TestAgent_RequestID_NoGeneratorFunction verifies behavior when fetchNewRequestIDFunc is nil
// Tests that agent works correctly even without request ID generation
func TestAgent_RequestID_NoGeneratorFunction(t *testing.T) {
	t.Parallel()

	// Setup WITHOUT request ID function (uses regular setupMCPManager)
	manager := setupMCPManager(t)
	require.NoError(t, RegisterEchoTool(manager))
	require.NoError(t, SetInternalClientAutoExecute(manager, []string{"*"}))

	manager.UpdateToolManagerConfig(&schemas.MCPToolManagerConfig{
		MaxAgentDepth: 5,
	})

	// Set initial request ID
	originalRequestID := "static-req-001"
	ctx := createTestContext()
	ctx.SetValue(schemas.BifrostContextKeyRequestID, originalRequestID)

	mocker := NewDynamicLLMMocker()

	// Turn 1: Tool call
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		GetSampleEchoToolCall("call-1", "test"),
	))

	// Turn 2: Final text
	mocker.AddChatResponse(CreateAgentTurnWithText("Done"))

	// Execute agent WITHOUT fetchNewRequestIDFunc (nil in config)
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input:    []schemas.ChatMessage{GetSampleUserMessage("Test")},
	}

	initialResponse, initialErr := mocker.MakeChatRequest(ctx, req)
	require.Nil(t, initialErr)

	result, bifrostErr := manager.CheckAndExecuteAgentForChatRequest(
		ctx, req, initialResponse, mocker.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	// Assertions
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// Original request ID should still be stored
	storedOriginalID, ok := ctx.Value(schemas.BifrostMCPAgentOriginalRequestID).(string)
	require.True(t, ok)
	assert.Equal(t, originalRequestID, storedOriginalID)

	// Current request ID should remain the original (not updated since no generator)
	currentRequestID, ok := ctx.Value(schemas.BifrostContextKeyRequestID).(string)
	require.True(t, ok)
	assert.Equal(t, originalRequestID, currentRequestID, "Request ID should remain original when no generator provided")

	t.Logf("✓ Request ID remained unchanged: %s", currentRequestID)
}

// TestAgent_RequestID_EmptyGeneratorResult verifies handling when generator returns empty string
// Tests that empty string from generator doesn't update the request ID
func TestAgent_RequestID_EmptyGeneratorResult(t *testing.T) {
	t.Parallel()

	originalRequestID := "req-empty-test"

	// Generator that returns empty string
	fetchNewRequestIDFunc := func(ctx *schemas.BifrostContext) string {
		return "" // Empty string
	}

	// Setup MCP manager with request ID function
	manager := setupMCPManagerWithRequestIDFunc(t, fetchNewRequestIDFunc)
	require.NoError(t, RegisterEchoTool(manager))
	require.NoError(t, SetInternalClientAutoExecute(manager, []string{"*"}))

	manager.UpdateToolManagerConfig(&schemas.MCPToolManagerConfig{
		MaxAgentDepth: 5,
	})

	// Setup context and mocker
	ctx := createTestContext()
	ctx.SetValue(schemas.BifrostContextKeyRequestID, originalRequestID)

	mocker := NewDynamicLLMMocker()

	// Turn 1: Tool call
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		GetSampleEchoToolCall("call-1", "test"),
	))

	// Turn 2: Final text
	mocker.AddChatResponse(CreateAgentTurnWithText("Done"))

	// Execute agent
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input:    []schemas.ChatMessage{GetSampleUserMessage("Test")},
	}

	initialResponse, initialErr := mocker.MakeChatRequest(ctx, req)
	require.Nil(t, initialErr)

	result, bifrostErr := manager.CheckAndExecuteAgentForChatRequest(
		ctx, req, initialResponse, mocker.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	// Assertions
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// Current request ID should remain original (empty string doesn't update it)
	// See agent.go:347-351 - only updates if newID != ""
	currentRequestID, ok := ctx.Value(schemas.BifrostContextKeyRequestID).(string)
	require.True(t, ok)
	assert.Equal(t, originalRequestID, currentRequestID, "Request ID should not change when generator returns empty string")

	t.Logf("✓ Request ID preserved when generator returns empty: %s", currentRequestID)
}

// TestAgent_RequestID_SequentialUpdates verifies request ID updates sequentially through agent loop
// Tests that request IDs are updated correctly for each iteration
func TestAgent_RequestID_SequentialUpdates(t *testing.T) {
	t.Parallel()

	originalRequestID := "req-sequential"
	iteration := 0

	fetchNewRequestIDFunc := func(ctx *schemas.BifrostContext) string {
		iteration++
		return fmt.Sprintf("%s-iter-%d", originalRequestID, iteration)
	}

	// Setup MCP manager with request ID function
	manager := setupMCPManagerWithRequestIDFunc(t, fetchNewRequestIDFunc)
	require.NoError(t, RegisterEchoTool(manager))
	require.NoError(t, RegisterCalculatorTool(manager))
	require.NoError(t, SetInternalClientAutoExecute(manager, []string{"*"}))

	manager.UpdateToolManagerConfig(&schemas.MCPToolManagerConfig{
		MaxAgentDepth: 5,
	})

	// Setup context and mocker
	ctx := createTestContext()
	ctx.SetValue(schemas.BifrostContextKeyRequestID, originalRequestID)

	mocker := NewDynamicLLMMocker()

	// Turn 1: Tool call
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		GetSampleEchoToolCall("call-1", "first"),
	))

	// Turn 2: Tool call
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		GetSampleCalculatorToolCall("call-2", "add", 5, 3),
	))

	// Turn 3: Final text
	mocker.AddChatResponse(CreateAgentTurnWithText("Done"))

	// Execute agent
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input:    []schemas.ChatMessage{GetSampleUserMessage("Test")},
	}

	initialResponse, initialErr := mocker.MakeChatRequest(ctx, req)
	require.Nil(t, initialErr)

	result, bifrostErr := manager.CheckAndExecuteAgentForChatRequest(
		ctx, req, initialResponse, mocker.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	// Assertions
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// Verify final request ID is the last iteration
	currentRequestID, ok := ctx.Value(schemas.BifrostContextKeyRequestID).(string)
	require.True(t, ok)
	assert.Equal(t, "req-sequential-iter-2", currentRequestID, "Final request ID should be from last iteration")

	// Verify original request ID preserved
	storedOriginalID, ok := ctx.Value(schemas.BifrostMCPAgentOriginalRequestID).(string)
	require.True(t, ok)
	assert.Equal(t, originalRequestID, storedOriginalID)

	t.Logf("✓ Sequential request ID updates verified: %s -> %s", originalRequestID, currentRequestID)
}

// TestAgent_RequestID_MixedAutoAndNonAuto verifies request ID handling with mixed permissions
// Tests that request IDs are correctly set even when agent stops for approval
func TestAgent_RequestID_MixedAutoAndNonAuto(t *testing.T) {
	t.Parallel()

	requestIDsGenerated := []string{}
	idMutex := sync.Mutex{}

	fetchNewRequestIDFunc := func(ctx *schemas.BifrostContext) string {
		idMutex.Lock()
		defer idMutex.Unlock()

		newID := fmt.Sprintf("req-mixed-iter-%d", len(requestIDsGenerated)+1)
		requestIDsGenerated = append(requestIDsGenerated, newID)
		return newID
	}

	// Setup MCP manager with request ID function
	manager := setupMCPManagerWithRequestIDFunc(t, fetchNewRequestIDFunc)
	require.NoError(t, RegisterEchoTool(manager))
	require.NoError(t, RegisterCalculatorTool(manager))
	// Only echo is auto-executable
	require.NoError(t, SetInternalClientAutoExecute(manager, []string{"echo"}))

	manager.UpdateToolManagerConfig(&schemas.MCPToolManagerConfig{
		MaxAgentDepth: 5,
	})

	// Setup context and mocker
	ctx := createTestContext()
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "req-mixed-001")

	mocker := NewDynamicLLMMocker()

	// Turn 1: LLM calls echo (auto) and calculator (non-auto)
	mocker.AddChatResponse(CreateAgentTurnWithToolCalls(
		GetSampleEchoToolCall("call-1", "test"),
		GetSampleCalculatorToolCall("call-2", "add", 10, 5),
	))

	// Execute agent
	req := &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4",
		Input:    []schemas.ChatMessage{GetSampleUserMessage("Test mixed permissions")},
	}

	initialResponse, initialErr := mocker.MakeChatRequest(ctx, req)
	require.Nil(t, initialErr)

	result, bifrostErr := manager.CheckAndExecuteAgentForChatRequest(
		ctx, req, initialResponse, mocker.MakeChatRequest,
		func(ctx *schemas.BifrostContext, request *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
			return manager.ExecuteToolCall(ctx, request)
		},
	)

	// Assertions
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// Agent should stop at turn 1 (echo executed, calculator waiting for approval)
	AssertAgentStoppedAtTurn(t, mocker, 1)

	// Original request ID should be preserved
	storedOriginalID, ok := ctx.Value(schemas.BifrostMCPAgentOriginalRequestID).(string)
	require.True(t, ok)
	assert.Equal(t, "req-mixed-001", storedOriginalID)

	// Since agent stopped immediately (no continuation), no new request IDs should be generated
	idMutex.Lock()
	assert.Len(t, requestIDsGenerated, 0, "No new request IDs should be generated when agent stops for approval")
	idMutex.Unlock()

	t.Logf("✓ Agent stopped for approval, no request ID updates needed")
}
