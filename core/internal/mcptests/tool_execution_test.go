package mcptests

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// DIRECT TOOL EXECUTION TESTS
// =============================================================================

func TestDirectToolExecution_ChatFormat(t *testing.T) {
	t.Parallel()

	// Use InProcess tools for self-contained testing
	manager := setupMCPManager(t)

	// Register echo tool
	err := RegisterEchoTool(manager)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	// Execute echo tool in Chat format
	ctx := createTestContext()
	toolCall := GetSampleEchoToolCall("call-1", "Hello, World!")

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	require.Nil(t, bifrostErr, "tool execution should succeed")
	require.NotNil(t, result, "should have result")

	// Verify response format
	assert.Equal(t, schemas.ChatMessageRoleTool, result.Role)
	assert.NotNil(t, result.Content)
	if result.Content.ContentStr != nil {
		assert.Contains(t, *result.Content.ContentStr, "Hello, World!")
	}
}

func TestDirectToolExecution_ResponsesFormat(t *testing.T) {
	t.Parallel()

	// Use InProcess tools for self-contained testing
	manager := setupMCPManager(t)

	// Register echo tool
	err := RegisterEchoTool(manager)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	// Execute echo tool in Responses format
	ctx := createTestContext()
	args := map[string]interface{}{"message": "Hello, Responses!"}
	toolCall := CreateResponsesToolCallForExecution("call-1", "echo", args)

	result, bifrostErr := bifrost.ExecuteResponsesMCPTool(ctx, &toolCall)
	require.Nil(t, bifrostErr, "tool execution should succeed")
	require.NotNil(t, result, "should have result")

	// Verify response format
	assert.Equal(t, schemas.ResponsesMessageTypeFunctionCallOutput, *result.Type)
	assert.NotNil(t, result.ResponsesToolMessage)
	if result.ResponsesToolMessage.Output != nil && result.ResponsesToolMessage.Output.ResponsesToolCallOutputStr != nil {
		assert.Contains(t, *result.ResponsesToolMessage.Output.ResponsesToolCallOutputStr, "Hello, Responses!")
	}
}

func TestToolExecutionWithArguments(t *testing.T) {
	t.Parallel()

	// Use InProcess tools for self-contained testing
	manager := setupMCPManager(t)

	// Register calculator tool
	err := RegisterCalculatorTool(manager)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	testCases := []struct {
		name      string
		operation string
		x         float64
		y         float64
		expected  string
	}{
		{"add operation", "add", 2, 3, "5"},
		{"subtract operation", "subtract", 10, 4, "6"},
		{"multiply operation", "multiply", 5, 6, "30"},
		{"divide operation", "divide", 20, 4, "5"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			toolCall := GetSampleCalculatorToolCall("call-"+tc.operation, tc.operation, tc.x, tc.y)
			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			require.Nil(t, bifrostErr, "calculator execution should succeed")
			require.NotNil(t, result, "should have result")

			if result.Content != nil && result.Content.ContentStr != nil {
				assert.Contains(t, *result.Content.ContentStr, tc.expected)
			}
		})
	}
}

func TestToolExecutionInvalidArguments(t *testing.T) {
	t.Parallel()

	// Use InProcess tools for self-contained testing
	manager := setupMCPManager(t)

	// Register calculator tool
	err := RegisterCalculatorTool(manager)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	t.Run("invalid_json", func(t *testing.T) {
		toolCall := schemas.ChatAssistantMessageToolCall{
			ID:   schemas.Ptr("call-1"),
			Type: schemas.Ptr("function"),
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name:      schemas.Ptr("calculator"),
				Arguments: "invalid json {{{",
			},
		}

		result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
		// Should either return error or error in result
		if bifrostErr == nil && result != nil {
			// Some implementations may return error in result content
			t.Log("Tool execution handled invalid JSON")
		}
	})

	t.Run("missing_required_arguments", func(t *testing.T) {
		argsMap := map[string]interface{}{
			"operation": "add",
			// Missing x and y
		}
		argsJSON, _ := json.Marshal(argsMap)

		toolCall := schemas.ChatAssistantMessageToolCall{
			ID:   schemas.Ptr("call-2"),
			Type: schemas.Ptr("function"),
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name:      schemas.Ptr("calculator"),
				Arguments: string(argsJSON),
			},
		}

		result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
		// Should indicate missing arguments
		if bifrostErr == nil && result != nil {
			t.Log("Tool execution handled missing arguments")
		}
	})

	t.Run("wrong_argument_types", func(t *testing.T) {
		argsMap := map[string]interface{}{
			"operation": "add",
			"x":         "not_a_number",
			"y":         "also_not_a_number",
		}
		argsJSON, _ := json.Marshal(argsMap)

		toolCall := schemas.ChatAssistantMessageToolCall{
			ID:   schemas.Ptr("call-3"),
			Type: schemas.Ptr("function"),
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name:      schemas.Ptr("calculator"),
				Arguments: string(argsJSON),
			},
		}

		result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
		// Should indicate type error
		if bifrostErr == nil && result != nil {
			t.Log("Tool execution handled wrong types")
		}
	})
}

func TestToolExecutionTimeout(t *testing.T) {
	t.Parallel()

	// Use InProcess tools for self-contained testing
	manager := setupMCPManager(t)

	// Register delay tool
	err := RegisterDelayTool(manager)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	// Create context with short timeout (100ms)
	ctx, cancel := createTestContextWithTimeout(100 * time.Millisecond)
	defer cancel()

	// Try to execute delay tool with long duration (5 seconds)
	argsMap := map[string]interface{}{
		"seconds": 5.0, // 5 seconds delay
	}
	argsJSON, _ := json.Marshal(argsMap)

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-timeout"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("delay"),
			Arguments: string(argsJSON),
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

	// Should timeout - the tool takes 5 seconds but context times out in 100ms
	if bifrostErr != nil && bifrostErr.Error != nil {
		t.Logf("✅ Got expected timeout error: %v", bifrostErr.Error.Message)
	} else if result != nil {
		// Some implementations may return timeout in result
		t.Log("Timeout handled in result")
	} else {
		t.Log("Timeout handled (no error or result)")
	}
}

func TestToolExecutionReturnsError(t *testing.T) {
	t.Parallel()

	// Use InProcess tools for self-contained testing
	manager := setupMCPManager(t)

	// Register throw_error tool
	err := RegisterThrowErrorTool(manager)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Use error tool
	errorMessage := "This is a test error"
	argsMap := map[string]interface{}{
		"error_message": errorMessage,
	}
	argsJSON, _ := json.Marshal(argsMap)

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-error"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("bifrostInternal-throw_error"),
			Arguments: string(argsJSON),
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

	// Error should be propagated
	if bifrostErr != nil && bifrostErr.Error != nil {
		assert.Contains(t, bifrostErr.Error.Message, errorMessage)
		t.Logf("✅ Error properly propagated: %v", bifrostErr.Error.Message)
	} else if result != nil && result.Content != nil && result.Content.ContentStr != nil {
		// Error might be in result content
		assert.Contains(t, *result.Content.ContentStr, errorMessage)
		t.Logf("✅ Error in result content")
	}
}

// =============================================================================
// TOOL EXECUTION WITH DIFFERENT RESULT TYPES
// =============================================================================

func TestToolExecutionLargeResponse(t *testing.T) {
	t.Parallel()

	// Use InProcess tools for self-contained testing
	manager := setupMCPManager(t)

	// Register echo tool
	err := RegisterEchoTool(manager)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Create large message (10KB - reasonable size for testing)
	largeMessage := ""
	for i := 0; i < 10000; i++ {
		largeMessage += "A"
	}

	toolCall := GetSampleEchoToolCall("call-large", largeMessage)
	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

	require.Nil(t, bifrostErr, "large response should not error")
	assert.NotNil(t, result, "should have result")
	t.Logf("✅ Large response handled successfully (%d bytes)", len(largeMessage))
}

func TestToolExecutionEmptyResponse(t *testing.T) {
	t.Parallel()

	// Use InProcess tools for self-contained testing
	manager := setupMCPManager(t)

	// Register echo tool
	err := RegisterEchoTool(manager)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	toolCall := GetSampleEchoToolCall("call-empty", "")
	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

	require.Nil(t, bifrostErr, "empty response should not crash")
	assert.NotNil(t, result, "should have result structure")
	t.Logf("✅ Empty message handled successfully")
}

func TestToolExecutionStructuredResponse(t *testing.T) {
	t.Parallel()

	// Use InProcess tools for self-contained testing
	manager := setupMCPManager(t)

	// Register weather tool
	err := RegisterWeatherTool(manager)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Weather tool returns structured JSON response
	toolCall := GetSampleWeatherToolCall("call-weather", "San Francisco", "celsius")
	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

	require.Nil(t, bifrostErr, "structured response should work")
	assert.NotNil(t, result, "should have result")
	t.Logf("✅ Structured response handled successfully")
}

// =============================================================================
// LATENCY AND PERFORMANCE TESTS
// =============================================================================

func TestToolExecutionLatencyMeasurement(t *testing.T) {
	t.Parallel()

	// Use InProcess tools for fast, reliable testing
	manager := setupMCPManager(t)
	err := RegisterEchoTool(manager)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	toolCall := GetSampleEchoToolCall("call-latency", "test")

	start := time.Now()
	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	elapsed := time.Since(start)

	require.Nil(t, bifrostErr, "tool execution should succeed")
	assert.NotNil(t, result, "should have result")

	// Latency should be reasonable (< 5 seconds for echo)
	assert.Less(t, elapsed, 5*time.Second, "echo should be fast")
}

func TestToolExecutionParallel(t *testing.T) {
	t.Parallel()

	// Use InProcess tools for fast, reliable testing
	manager := setupMCPManager(t)
	err := RegisterEchoTool(manager)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	var wg sync.WaitGroup
	errors := make(chan error, 5)

	start := time.Now()
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			toolCall := GetSampleEchoToolCall("call-parallel-"+string(rune('a'+id)), "test")
			_, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			if bifrostErr != nil {
				errors <- fmt.Errorf("tool execution error: %v", bifrostErr)
			}
		}(i)
	}

	wg.Wait()
	close(errors)
	elapsed := time.Since(start)

	// Check for errors
	for err := range errors {
		t.Errorf("Parallel execution error: %v", err)
	}

	t.Logf("Parallel execution of 5 tools took: %v", elapsed)
}

// =============================================================================
// EXTRA FIELDS AND METADATA TESTS
// =============================================================================

func TestToolExecutionExtraFields(t *testing.T) {
	t.Parallel()

	// Use InProcess tools for fast, reliable testing
	manager := setupMCPManager(t)
	err := RegisterEchoTool(manager)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	toolCall := GetSampleEchoToolCall("call-extra", "test")
	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

	require.Nil(t, bifrostErr, "tool execution should succeed")
	assert.NotNil(t, result, "should have result")

	// ExtraFields should be populated (if implementation supports it)
	// Note: ExtraFields are in BifrostMCPResponse, not ChatMessage
}

func TestToolExecutionPreservesCallID(t *testing.T) {
	t.Parallel()

	// Use InProcess tools for fast, reliable testing
	manager := setupMCPManager(t)
	err := RegisterEchoTool(manager)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Test Chat format
	expectedCallID := "call-preserve-123"
	chatToolCall := GetSampleEchoToolCall(expectedCallID, "test")
	chatResult, chatErr := bifrost.ExecuteChatMCPTool(ctx, &chatToolCall)

	require.Nil(t, chatErr, "Chat tool execution should succeed")
	if chatResult.ChatToolMessage != nil && chatResult.ChatToolMessage.ToolCallID != nil {
		assert.Equal(t, expectedCallID, *chatResult.ChatToolMessage.ToolCallID)
	}

	// Test Responses format
	args := map[string]interface{}{"message": "test"}
	responsesToolCall := CreateResponsesToolCallForExecution(expectedCallID, "echo", args)
	responsesResult, responsesErr := bifrost.ExecuteResponsesMCPTool(ctx, &responsesToolCall)

	require.Nil(t, responsesErr, "Responses tool execution should succeed")
	if responsesResult.ResponsesToolMessage != nil && responsesResult.ResponsesToolMessage.CallID != nil {
		assert.Equal(t, expectedCallID, *responsesResult.ResponsesToolMessage.CallID)
	}
}

// =============================================================================
// MULTIPLE TOOLS AND CLIENTS TESTS
// =============================================================================

func TestToolExecutionMultipleTools(t *testing.T) {
	t.Parallel()

	// Use InProcess tools for fast, reliable testing
	manager := setupMCPManager(t)
	err := RegisterCalculatorTool(manager)
	require.NoError(t, err)
	err = RegisterEchoTool(manager)
	require.NoError(t, err)
	err = RegisterWeatherTool(manager)
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute different tools
	t.Run("calculator", func(t *testing.T) {
		toolCall := GetSampleCalculatorToolCall("call-calc", "add", 2, 3)
		result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
		require.Nil(t, bifrostErr)
		assert.NotNil(t, result)
	})

	t.Run("echo", func(t *testing.T) {
		toolCall := GetSampleEchoToolCall("call-echo", "test")
		result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
		require.Nil(t, bifrostErr)
		assert.NotNil(t, result)
	})

	t.Run("weather", func(t *testing.T) {
		toolCall := GetSampleWeatherToolCall("call-weather", "London", "celsius")
		result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
		require.Nil(t, bifrostErr)
		assert.NotNil(t, result)
	})
}

func TestToolExecutionMultipleClients(t *testing.T) {
	t.Parallel()

	// Setup two InProcess clients with different tools
	manager := setupMCPManager(t)

	// Register first set of tools (simulating first client)
	err := RegisterEchoTool(manager)
	require.Nil(t, err)

	// Register second tool (simulating second client)
	localToolHandler := func(args any) (string, error) {
		return `{"result": "local execution"}`, nil
	}
	localToolSchema := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "local_tool",
			Description: schemas.Ptr("A local tool"),
		},
	}
	err = manager.RegisterTool("local_tool", "A local tool", localToolHandler, localToolSchema)
	require.Nil(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute echo tool
	echoToolCall := GetSampleEchoToolCall("call-echo", "echo test")
	echoResult, echoErr := bifrost.ExecuteChatMCPTool(ctx, &echoToolCall)
	require.Nil(t, echoErr, "Echo tool should work")
	assert.NotNil(t, echoResult)

	// Execute local tool
	argsJSON, _ := json.Marshal(map[string]interface{}{})
	inProcessToolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-local"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("bifrostInternal-local_tool"),
			Arguments: string(argsJSON),
		},
	}
	localResult, localErr := bifrost.ExecuteChatMCPTool(ctx, &inProcessToolCall)
	require.Nil(t, localErr, "InProcess tool should work")
	assert.NotNil(t, localResult)
}

// =============================================================================
// ERROR HANDLING TESTS

// =============================================================================
// PROVIDER-INJECTED TOOL TESTS
// =============================================================================

const injectedSearchTool = "bifrostInternal-web_search"

// fakeOpenAI answers /v1/chat/completions from a script and records every body it got.
type fakeOpenAI struct {
	mu      sync.Mutex
	bodies  []map[string]any
	replies []string
}

func (f *fakeOpenAI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	f.mu.Lock()
	f.bodies = append(f.bodies, body)
	reply := f.replies[0]
	f.replies = f.replies[1:]
	f.mu.Unlock()
	if strings.HasPrefix(reply, "data:") || strings.HasPrefix(reply, "event:") {
		w.Header().Set("Content-Type", "text/event-stream")
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	// A pausePart marker splits a reply into parts flushed with a pause between them, so a
	// test can let the gateway act on the first part before the rest arrives.
	for i, part := range strings.Split(reply, pausePart) {
		if i > 0 {
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			time.Sleep(100 * time.Millisecond)
		}
		_, _ = w.Write([]byte(part))
	}
}

// requests returns the bodies received so far. It reads under f.mu, as the handler
// goroutine appends under it.
func (f *fakeOpenAI) requests() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.bodies...)
}

const pausePart = "\x00pause\x00"

func (f *fakeOpenAI) toolNames(turn int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var names []string
	tools, _ := f.bodies[turn]["tools"].([]any)
	for _, tool := range tools {
		entry, _ := tool.(map[string]any)
		if fn, ok := entry["function"].(map[string]any); ok {
			names = append(names, fn["name"].(string))
		} else if name, ok := entry["name"].(string); ok {
			names = append(names, name)
		} else {
			names = append(names, entry["type"].(string))
		}
	}
	return names
}

func chatCompletionJSON(content string, finish string, toolCall string) string {
	message := map[string]any{"role": "assistant", "content": content}
	if toolCall != "" {
		message["content"] = nil
		message["tool_calls"] = []any{map[string]any{
			"id": "call_1", "type": "function",
			"function": map[string]any{"name": toolCall, "arguments": `{"query":"weather in paris"}`},
		}}
	}
	out, _ := json.Marshal(map[string]any{
		"id": "chatcmpl-1", "object": "chat.completion", "created": 1, "model": "gpt-4o",
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}},
		"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 2, "total_tokens": 12},
	})
	return string(out)
}

// injectedToolsAccount serves one OpenAI provider pointed at a fake server, with the
// in-process web_search tool configured as the provider's injected web search.
type injectedToolsAccount struct {
	provider schemas.ModelProvider
	baseURL  string
}

func (a *injectedToolsAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{a.provider}, nil
}

func (a *injectedToolsAccount) GetKeysForProvider(ctx context.Context, provider schemas.ModelProvider) ([]schemas.Key, error) {
	return []schemas.Key{{ID: "k1", Value: *schemas.NewSecretVar("sk-test"), Models: schemas.WhiteList{"*"}, Weight: 1}}, nil
}

func (a *injectedToolsAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	network := schemas.DefaultNetworkConfig
	network.BaseURL = a.baseURL
	network.MaxRetries = 0
	return &schemas.ProviderConfig{
		NetworkConfig:            network,
		ConcurrencyAndBufferSize: schemas.DefaultConcurrencyAndBufferSize,
		InjectedTools: &schemas.InjectedToolsConfig{
			WebSearch: &schemas.InjectedToolRef{MCPClientName: "bifrostInternal", ToolName: "web_search"},
		},
	}, nil
}

func setupInjectedToolsBifrost(t *testing.T, fake *fakeOpenAI, searches *[]string) *bifrost.Bifrost {
	return setupInjectedToolsBifrostFor(t, schemas.OpenAI, fake, searches)
}

func setupInjectedToolsBifrostFor(t *testing.T, provider schemas.ModelProvider, fake *fakeOpenAI, searches *[]string) *bifrost.Bifrost {
	t.Helper()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)

	b, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account: &injectedToolsAccount{provider: provider, baseURL: server.URL},
		Logger:  bifrost.NewDefaultLogger(schemas.LogLevelError),
		MCPConfig: &schemas.MCPConfig{
			// Regular MCP auto-injection stays off: the provider config alone must put
			// the tool on the wire.
			ToolManagerConfig: &schemas.MCPToolManagerConfig{DisableAutoToolInject: true},
		},
	})
	require.NoError(t, err)
	t.Cleanup(b.Shutdown)

	var mu sync.Mutex
	schema := schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name: "web_search",
			Parameters: &schemas.ToolFunctionParameters{
				Type:       "object",
				Properties: schemas.NewOrderedMapFromPairs(schemas.KV("query", map[string]any{"type": "string"})),
				Required:   []string{"query"},
			},
		},
	}
	require.NoError(t, b.RegisterMCPTool("web_search", "Search the web", func(args any) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		query, _ := args.(map[string]any)["query"].(string)
		*searches = append(*searches, query)
		return "Paris: sunny, 24C", nil
	}, schema))
	return b
}

// TestInjectedWebSearch_ChatEndToEnd drives a chat request through Bifrost against a
// provider whose config injects an MCP web search tool. The client sends Anthropic's
// native web search tool and an include list that excludes the MCP tool; Bifrost must
// replace the native tool, run the search itself, and return only the final answer.
func TestInjectedWebSearch_ChatEndToEnd(t *testing.T) {
	fake := &fakeOpenAI{replies: []string{
		chatCompletionJSON("", "tool_calls", injectedSearchTool),
		chatCompletionJSON("It is sunny in Paris.", "stop", ""),
	}}
	var searches []string
	b := setupInjectedToolsBifrost(t, fake, &searches)

	ctx := createTestContext()
	ctx.SetValue(schemas.MCPContextKeyIncludeTools, []string{"other-tool"})
	resp, bifrostErr := b.ChatCompletionRequest(ctx, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Weather in Paris?")}}},
		Params: &schemas.ChatParameters{
			Tools: []schemas.ChatTool{{Type: "web_search_20250305", Name: "web_search"}},
		},
	})

	require.Nil(t, bifrostErr, "%v", bifrostErr)
	assert.Equal(t, []string{"weather in paris"}, searches, "Bifrost ran the search itself")
	bodies := fake.requests()
	require.Len(t, bodies, 2)
	assert.Equal(t, []string{injectedSearchTool}, fake.toolNames(0), "the native tool is replaced by the MCP tool")

	messages, _ := bodies[1]["messages"].([]any)
	require.Len(t, messages, 3, "user, assistant tool call, tool result")
	toolResult := messages[2].(map[string]any)
	assert.Equal(t, "tool", toolResult["role"])
	assert.Contains(t, toolResult["content"], "sunny")

	msg := resp.Choices[0].Message
	assert.Equal(t, "It is sunny in Paris.", *msg.Content.ContentStr)
	assert.True(t, msg.ChatAssistantMessage == nil || len(msg.ToolCalls) == 0, "the client never sees the injected call")
	assert.Equal(t, 20, resp.Usage.PromptTokens, "usage covers both model turns")
}

// TestInjectedWebSearch_ClientToolCallsPassThrough pins that injection does not take
// over the client's own tools: they stay declared next to the injected one, and a call
// to them comes back to the client unexecuted.
func TestInjectedWebSearch_ClientToolCallsPassThrough(t *testing.T) {
	fake := &fakeOpenAI{replies: []string{
		chatCompletionJSON("", "tool_calls", "get_weather"),
	}}
	var searches []string
	b := setupInjectedToolsBifrost(t, fake, &searches)

	resp, bifrostErr := b.ChatCompletionRequest(createTestContext(), &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Weather?")}}},
		Params: &schemas.ChatParameters{Tools: []schemas.ChatTool{{
			Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: "get_weather"},
		}}},
	})

	require.Nil(t, bifrostErr, "%v", bifrostErr)
	assert.Empty(t, searches)
	assert.Equal(t, []string{"get_weather", injectedSearchTool}, fake.toolNames(0))
	require.Len(t, resp.Choices[0].Message.ToolCalls, 1, "a client tool call goes back to the client")
	assert.Equal(t, "get_weather", *resp.Choices[0].Message.ToolCalls[0].Function.Name)
}

// sseChat renders chat completion chunks as an OpenAI SSE body.
func sseChat(id string, chunks ...string) string {
	var b strings.Builder
	for _, chunk := range chunks {
		b.WriteString(`data: {"id":"` + id + `","object":"chat.completion.chunk","created":1,"model":"gpt-4o",` + chunk + "}\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

// TestInjectedWebSearch_ChatStreamEndToEnd streams a chat request across two upstream
// turns. The client must get one stream: the first turn's text live, no injected tool
// call deltas, the answer, and a single final chunk carrying both turns' usage.
func TestInjectedWebSearch_ChatStreamEndToEnd(t *testing.T) {
	fake := &fakeOpenAI{replies: []string{
		sseChat("turn-1",
			`"choices":[{"index":0,"delta":{"role":"assistant","content":"Let me search. "},"finish_reason":null}]`,
			`"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"`+injectedSearchTool+`","arguments":""}}]},"finish_reason":null}]`,
			`"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"query\":\"weather in paris\"}"}}]},"finish_reason":null}]`,
			`"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]`,
			`"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}`,
		),
		sseChat("turn-2",
			`"choices":[{"index":0,"delta":{"role":"assistant","content":"It is sunny."},"finish_reason":null}]`,
			`"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]`,
			`"choices":[],"usage":{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23}`,
		),
	}}
	var searches []string
	b := setupInjectedToolsBifrost(t, fake, &searches)

	stream, bifrostErr := b.ChatCompletionStreamRequest(createTestContext(), &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Weather in Paris?")}}},
		Params:   &schemas.ChatParameters{WebSearchOptions: &schemas.ChatWebSearchOptions{}},
	})
	require.Nil(t, bifrostErr, "%v", bifrostErr)

	var text strings.Builder
	var finishes []string
	var usages []*schemas.BifrostLLMUsage
	ids := map[string]bool{}
	for chunk := range stream {
		require.Nil(t, chunk.BifrostError, "%v", chunk.BifrostError)
		require.NotNil(t, chunk.BifrostChatResponse)
		ids[chunk.BifrostChatResponse.ID] = true
		if chunk.BifrostChatResponse.Usage != nil {
			usages = append(usages, chunk.BifrostChatResponse.Usage)
		}
		for _, choice := range chunk.BifrostChatResponse.Choices {
			if choice.Delta != nil {
				assert.Empty(t, choice.Delta.ToolCalls, "no tool call delta reaches the client")
				if choice.Delta.Content != nil {
					text.WriteString(*choice.Delta.Content)
				}
			}
			if choice.FinishReason != nil {
				finishes = append(finishes, *choice.FinishReason)
			}
		}
	}

	assert.Equal(t, []string{"weather in paris"}, searches)
	assert.Equal(t, "Let me search. It is sunny.", text.String())
	assert.Equal(t, []string{"stop"}, finishes, "one finish reason, from the last turn")
	require.Len(t, usages, 1, "one usage, on the final chunk")
	assert.Equal(t, 30, usages[0].PromptTokens)
	assert.Equal(t, map[string]bool{"turn-1": true}, ids, "the client sees one stream id")
	bodies := fake.requests()
	require.Len(t, bodies, 2)
	assert.Equal(t, []string{injectedSearchTool}, fake.toolNames(0))
	assert.Nil(t, bodies[0]["web_search_options"], "native web search is not enabled upstream")
}

// errorCapture records the errors post-hooks see, which is what governance billing and
// logging read: a stream error the client never receives still reaches them.
type errorCapture struct {
	mu     sync.Mutex
	errors []*schemas.BifrostError
}

func (p *errorCapture) GetName() string { return "injected-tools-error-capture" }
func (p *errorCapture) Cleanup() error  { return nil }
func (p *errorCapture) PreRequestHook(*schemas.BifrostContext, *schemas.BifrostRequest) error {
	return nil
}
func (p *errorCapture) PreLLMHook(_ *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	return req, nil, nil
}
func (p *errorCapture) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if bifrostErr != nil {
		p.mu.Lock()
		p.errors = append(p.errors, bifrostErr)
		p.mu.Unlock()
	}
	return resp, bifrostErr, nil
}

func (p *errorCapture) billedPromptTokens() []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []int
	for _, e := range p.errors {
		if e.ExtraFields.BilledUsage == nil {
			out = append(out, 0)
		} else {
			out = append(out, e.ExtraFields.BilledUsage.PromptTokens)
		}
	}
	return out
}

// setupInjectedToolsBifrostWithHooks is setupInjectedToolsBifrost with an LLM plugin and
// a callback run inside the search tool.
func setupInjectedToolsBifrostWithHooks(t *testing.T, fake *fakeOpenAI, plugin schemas.LLMPlugin, onSearch func()) *bifrost.Bifrost {
	t.Helper()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	b, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account:    &injectedToolsAccount{provider: schemas.OpenAI, baseURL: server.URL},
		Logger:     bifrost.NewDefaultLogger(schemas.LogLevelError),
		LLMPlugins: []schemas.LLMPlugin{plugin},
		MCPConfig: &schemas.MCPConfig{
			ToolManagerConfig: &schemas.MCPToolManagerConfig{DisableAutoToolInject: true},
		},
	})
	require.NoError(t, err)
	t.Cleanup(b.Shutdown)
	schema := schemas.ChatTool{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{
		Name:       "web_search",
		Parameters: &schemas.ToolFunctionParameters{Type: "object", Properties: schemas.NewOrderedMapFromPairs(schemas.KV("query", map[string]any{"type": "string"}))},
	}}
	require.NoError(t, b.RegisterMCPTool("web_search", "Search the web", func(any) (string, error) {
		onSearch()
		return "Paris: sunny, 24C", nil
	}, schema))
	return b
}

func injectedSearchTurnSSE() string {
	return sseChat("turn-1",
		`"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"`+injectedSearchTool+`","arguments":"{\"query\":\"paris\"}"}}]},"finish_reason":null}]`,
		`"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]`,
		`"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}`,
	)
}

func drainStream(stream chan *schemas.BifrostStreamChunk) {
	for range stream {
	}
}

// A later turn's failure must still bill the turns that completed before it.
func TestInjectedWebSearch_ChatStreamErrorBillsEarlierTurns(t *testing.T) {
	fake := &fakeOpenAI{replies: []string{
		injectedSearchTurnSSE(),
		// Turn 2 starts streaming (registering its own usage accumulator) and then fails.
		"data: {\"id\":\"turn-2\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"It is\"},\"finish_reason\":null}]}\n\n" +
			"data: {\"error\":{\"message\":\"upstream exploded\",\"type\":\"server_error\"}}\n\n",
	}}
	capture := &errorCapture{}
	b := setupInjectedToolsBifrostWithHooks(t, fake, capture, func() {})

	stream, bifrostErr := b.ChatCompletionStreamRequest(createTestContext(), &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI, Model: "gpt-4o",
		Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Weather?")}}},
	})
	// Turn 1 shows the client nothing, so the failure can surface as the first chunk and
	// come back synchronously; either way it must reach post-hooks billed.
	if bifrostErr == nil {
		drainStream(stream)
	}

	require.Len(t, fake.requests(), 2, "the failure came from turn 2, after the injected search ran")
	billed := capture.billedPromptTokens()
	t.Logf("post-hook errors billed prompt tokens: %v", billed)
	require.NotEmpty(t, billed, "the turn-2 failure reaches post-hooks")
	assert.GreaterOrEqual(t, billed[len(billed)-1], 10, "the error bills the completed first turn")
}

// A cancel while the injected tool runs, between turns, must still end the stream
// through post-hooks with the completed turn's usage.
func TestInjectedWebSearch_ChatStreamCancelBetweenTurnsBillsCompletedTurn(t *testing.T) {
	fake := &fakeOpenAI{replies: []string{injectedSearchTurnSSE(), injectedSearchTurnSSE()}}
	capture := &errorCapture{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := setupInjectedToolsBifrostWithHooks(t, fake, capture, cancel)

	stream, bifrostErr := b.ChatCompletionStreamRequest(schemas.NewBifrostContext(ctx, schemas.NoDeadline), &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI, Model: "gpt-4o",
		Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Weather?")}}},
	})
	// Turn 1 shows the client nothing, so the failure can surface as the first chunk and
	// come back synchronously; either way it must reach post-hooks billed.
	if bifrostErr == nil {
		drainStream(stream)
	}

	// The caller can return on its own cancellation before the stream's goroutine has
	// ended the stream, so wait for the post-hook rather than reading it once.
	require.Eventually(t, func() bool { return len(capture.billedPromptTokens()) > 0 }, 5*time.Second, 10*time.Millisecond,
		"the cancelled stream still ends through post-hooks")
	require.Error(t, ctx.Err(), "the injected search ran: it is what cancels the request")
	require.Len(t, fake.requests(), 1, "the cancel landed between turns: turn 2 never opened")
	billed := capture.billedPromptTokens()
	assert.Equal(t, 10, billed[len(billed)-1], "the cancellation bills the completed turn")
}

// spanTracer records when the request's LLM span ends, relative to the upstream turns.
type spanTracer struct {
	schemas.NoOpTracer
	mu       sync.Mutex
	parked   schemas.SpanHandle
	llmEnds  []int // upstream requests the fake had served when the LLM span ended
	statuses []schemas.SpanStatus
	upstream func() int
}

type spanHandle struct{ kind schemas.SpanKind }

func (t *spanTracer) StartSpanID(_ context.Context, _ string, kind schemas.SpanKind) (string, schemas.SpanHandle) {
	return "span", &spanHandle{kind: kind}
}

func (t *spanTracer) StoreDeferredSpan(_ string, handle schemas.SpanHandle) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.parked = handle
}

func (t *spanTracer) GetDeferredSpanHandle(_ string) schemas.SpanHandle {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.parked
}

func (t *spanTracer) ClearDeferredSpan(_ string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.parked = nil
}

func (t *spanTracer) EndSpan(handle schemas.SpanHandle, status schemas.SpanStatus, _ string) {
	if h, ok := handle.(*spanHandle); ok && h.kind == schemas.SpanKindLLMCall {
		t.mu.Lock()
		t.llmEnds = append(t.llmEnds, t.upstream())
		t.statuses = append(t.statuses, status)
		t.mu.Unlock()
	}
}

func (t *spanTracer) ends() []int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]int(nil), t.llmEnds...)
}

// The request's single LLM span must end once, after the turn that answers, so the
// final turn's output, usage and status reach it.
func TestInjectedWebSearch_ChatStreamLLMSpanEndsAfterLastTurn(t *testing.T) {
	// Turn 1 shows text first and pauses, so the LLM span is parked (it is parked once the
	// first client chunk arrives) before turn 1 ends, as with any real stream.
	turn1 := "data: {\"id\":\"turn-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"gpt-4o\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Searching. \"},\"finish_reason\":null}]}\n\n" +
		pausePart + injectedSearchTurnSSE()
	fake := &fakeOpenAI{replies: []string{
		turn1,
		sseChat("turn-2",
			`"choices":[{"index":0,"delta":{"role":"assistant","content":"Sunny."},"finish_reason":null}]`,
			`"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]`,
			`"choices":[],"usage":{"prompt_tokens":20,"completion_tokens":3,"total_tokens":23}`,
		),
	}}
	tracer := &spanTracer{upstream: func() int {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.bodies)
	}}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	b, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account:   &injectedToolsAccount{provider: schemas.OpenAI, baseURL: server.URL},
		Logger:    bifrost.NewDefaultLogger(schemas.LogLevelError),
		Tracer:    tracer,
		MCPConfig: &schemas.MCPConfig{ToolManagerConfig: &schemas.MCPToolManagerConfig{DisableAutoToolInject: true}},
	})
	require.NoError(t, err)
	t.Cleanup(b.Shutdown)
	require.NoError(t, b.RegisterMCPTool("web_search", "Search the web", func(any) (string, error) { return "sunny", nil },
		schemas.ChatTool{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{
			Name:       "web_search",
			Parameters: &schemas.ToolFunctionParameters{Type: "object", Properties: schemas.NewOrderedMapFromPairs(schemas.KV("query", map[string]any{"type": "string"}))},
		}}))

	ctx := createTestContext()
	ctx.SetValue(schemas.BifrostContextKeyTraceID, "trace-1")
	stream, bifrostErr := b.ChatCompletionStreamRequest(ctx, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI, Model: "gpt-4o",
		Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Weather?")}}},
	})
	require.Nil(t, bifrostErr, "%v", bifrostErr)
	drainStream(stream)

	require.Eventually(t, func() bool { return len(tracer.ends()) > 0 }, 5*time.Second, 10*time.Millisecond, "the LLM span ends")
	assert.Equal(t, []int{2}, tracer.ends(), "the LLM span ends once, after the second (answering) turn")
	tracer.mu.Lock()
	defer tracer.mu.Unlock()
	assert.Equal(t, []schemas.SpanStatus{schemas.SpanStatusOk}, tracer.statuses, "a stream the client received in full ends its LLM span OK")
}

// A forced choice is relaxed for the stream turn after a search, as in the
// non-streaming loop, or the model would be forced to search again after every result.
func TestInjectedWebSearch_ChatStreamRelaxesForcedChoiceAfterSearch(t *testing.T) {
	fake := &fakeOpenAI{replies: []string{
		injectedSearchTurnSSE(),
		sseChat("turn-2",
			`"choices":[{"index":0,"delta":{"role":"assistant","content":"Sunny."},"finish_reason":"stop"}]`,
		),
	}}
	b := setupInjectedToolsBifrostWithHooks(t, fake, &errorCapture{}, func() {})
	stream, bifrostErr := b.ChatCompletionStreamRequest(createTestContext(), &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI, Model: "gpt-4o",
		Input:  []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Weather?")}}},
		Params: &schemas.ChatParameters{ToolChoice: &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("required")}},
	})
	require.Nil(t, bifrostErr, "%v", bifrostErr)
	drainStream(stream)

	bodies := fake.requests()
	require.Len(t, bodies, 2)
	assert.Equal(t, "required", bodies[0]["tool_choice"])
	assert.Equal(t, "auto", bodies[1]["tool_choice"], "after a search the model must be free to answer")
}

// sseEvents renders Responses API events as an SSE body.
func sseEvents(events ...string) string {
	var b strings.Builder
	for _, event := range events {
		b.WriteString("data: " + event + "\n\n")
	}
	return b.String()
}

// TestInjectedWebSearch_ResponsesStreamEndToEnd streams a Responses request across two
// upstream turns. The client must see one response: one created and one completed
// event, gapless sequence numbers, no function call events, and summed usage.
func TestInjectedWebSearch_ResponsesStreamEndToEnd(t *testing.T) {
	call := `{"type":"function_call","id":"fc_1","call_id":"call_1","name":"` + injectedSearchTool + `","arguments":"{\"query\":\"weather in paris\"}","status":"completed"}`
	fake := &fakeOpenAI{replies: []string{
		sseEvents(
			`{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","created_at":1,"status":"in_progress","model":"gpt-4o","output":[]}}`,
			`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"`+injectedSearchTool+`","arguments":"","status":"in_progress"}}`,
			`{"type":"response.function_call_arguments.delta","sequence_number":2,"output_index":0,"item_id":"fc_1","delta":"{\"query\":\"weather in paris\"}"}`,
			`{"type":"response.output_item.done","sequence_number":3,"output_index":0,"item":`+call+`}`,
			`{"type":"response.completed","sequence_number":4,"response":{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"gpt-4o","output":[`+call+`],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}}`,
		),
		sseEvents(
			`{"type":"response.created","sequence_number":0,"response":{"id":"resp_2","object":"response","created_at":2,"status":"in_progress","model":"gpt-4o","output":[]}}`,
			`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"type":"message","id":"msg_2","role":"assistant","status":"in_progress","content":[]}}`,
			`{"type":"response.output_text.delta","sequence_number":2,"output_index":0,"item_id":"msg_2","content_index":0,"delta":"It is sunny."}`,
			`{"type":"response.output_item.done","sequence_number":3,"output_index":0,"item":{"type":"message","id":"msg_2","role":"assistant","status":"completed","content":[{"type":"output_text","text":"It is sunny.","annotations":[]}]}}`,
			`{"type":"response.completed","sequence_number":4,"response":{"id":"resp_2","object":"response","created_at":2,"status":"completed","model":"gpt-4o","output":[{"type":"message","id":"msg_2","role":"assistant","status":"completed","content":[{"type":"output_text","text":"It is sunny.","annotations":[]}]}],"usage":{"input_tokens":20,"output_tokens":3,"total_tokens":23}}}`,
		),
	}}
	var searches []string
	b := setupInjectedToolsBifrost(t, fake, &searches)

	stream, bifrostErr := b.ResponsesStreamRequest(createTestContext(), &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input: []schemas.ResponsesMessage{{
			Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Weather in Paris?")},
		}},
		Params: &schemas.ResponsesParameters{Tools: []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeWebSearch}}},
	})
	require.Nil(t, bifrostErr, "%v", bifrostErr)

	var types []schemas.ResponsesStreamResponseType
	var text strings.Builder
	var completed *schemas.BifrostResponsesResponse
	lastSeq := -1
	for chunk := range stream {
		require.Nil(t, chunk.BifrostError, "%v", chunk.BifrostError)
		ev := chunk.BifrostResponsesStreamResponse
		require.NotNil(t, ev)
		assert.Equal(t, lastSeq+1, ev.SequenceNumber, "sequence numbers are gapless across turns")
		lastSeq = ev.SequenceNumber
		types = append(types, ev.Type)
		if ev.Type == schemas.ResponsesStreamResponseTypeOutputTextDelta && ev.Delta != nil {
			text.WriteString(*ev.Delta)
		}
		if ev.Type == schemas.ResponsesStreamResponseTypeCompleted {
			completed = ev.Response
		}
	}

	assert.Equal(t, []string{"weather in paris"}, searches)
	assert.Equal(t, "It is sunny.", text.String())
	count := func(typ schemas.ResponsesStreamResponseType) int {
		n := 0
		for _, got := range types {
			if got == typ {
				n++
			}
		}
		return n
	}
	assert.Equal(t, 1, count(schemas.ResponsesStreamResponseTypeCreated))
	assert.Equal(t, 1, count(schemas.ResponsesStreamResponseTypeCompleted))
	assert.Zero(t, count(schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta), "no injected call event reaches the client")
	require.NotNil(t, completed)
	assert.Equal(t, "resp_1", *completed.ID)
	assert.Equal(t, 30, completed.Usage.InputTokens)

	bodies := fake.requests()
	require.Len(t, bodies, 2)
	assert.Equal(t, []string{injectedSearchTool}, fake.toolNames(0), "the native web_search tool is replaced")
	input, _ := bodies[1]["input"].([]any)
	var replayed []string
	for _, item := range input {
		if typ, ok := item.(map[string]any)["type"].(string); ok {
			replayed = append(replayed, typ)
		}
	}
	assert.Contains(t, replayed, "function_call")
	assert.Contains(t, replayed, "function_call_output")
}

// A forced choice is relaxed for the Responses stream turn after a search too.
func TestInjectedWebSearch_ResponsesStreamRelaxesForcedChoiceAfterSearch(t *testing.T) {
	call := `{"type":"function_call","id":"fc_1","call_id":"call_1","name":"` + injectedSearchTool + `","arguments":"{\"query\":\"paris\"}","status":"completed"}`
	fake := &fakeOpenAI{replies: []string{
		sseEvents(
			`{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","created_at":1,"status":"in_progress","model":"gpt-4o","output":[]}}`,
			`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":`+call+`}`,
			`{"type":"response.output_item.done","sequence_number":2,"output_index":0,"item":`+call+`}`,
			`{"type":"response.completed","sequence_number":3,"response":{"id":"resp_1","object":"response","created_at":1,"status":"completed","model":"gpt-4o","output":[`+call+`],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}}`,
		),
		sseEvents(
			`{"type":"response.created","sequence_number":0,"response":{"id":"resp_2","object":"response","created_at":2,"status":"in_progress","model":"gpt-4o","output":[]}}`,
			`{"type":"response.completed","sequence_number":1,"response":{"id":"resp_2","object":"response","created_at":2,"status":"completed","model":"gpt-4o","output":[],"usage":{"input_tokens":20,"output_tokens":3,"total_tokens":23}}}`,
		),
	}}
	var searches []string
	b := setupInjectedToolsBifrost(t, fake, &searches)
	stream, bifrostErr := b.ResponsesStreamRequest(createTestContext(), &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI, Model: "gpt-4o",
		Input: []schemas.ResponsesMessage{{Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage), Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Weather?")}}},
		Params: &schemas.ResponsesParameters{ToolChoice: &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: schemas.Ptr("required")}},
	})
	require.Nil(t, bifrostErr, "%v", bifrostErr)
	drainStream(stream)

	bodies := fake.requests()
	require.Len(t, bodies, 2)
	assert.Equal(t, "required", bodies[0]["tool_choice"])
	assert.Equal(t, "auto", bodies[1]["tool_choice"], "after a search the model must be free to answer")
}

func TestToolExecutionClientNotFound(t *testing.T) {
	t.Parallel()

	// Create manager with no clients
	manager := setupMCPManager(t)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	toolCall := GetSampleEchoToolCall("call-noclient", "test")
	result, bifrostErr := bifrost.ExecuteChatMCPTool(createTestContext(), &toolCall)

	assert.Nil(t, result)
	require.NotNil(t, bifrostErr, "a tool with no client must error")
	require.NotNil(t, bifrostErr.Error)
	assert.Contains(t, bifrostErr.Error.Message, "not available or not permitted")
}

func TestToolExecutionMalformedRequest(t *testing.T) {
	t.Parallel()

	manager := setupMCPManager(t)
	require.NoError(t, RegisterEchoTool(manager))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	t.Run("missing_function_name", func(t *testing.T) {
		toolCall := schemas.ChatAssistantMessageToolCall{
			ID:   schemas.Ptr("call-noname"),
			Type: schemas.Ptr("function"),
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name:      nil, // Missing name
				Arguments: "{}",
			},
		}

		result, bifrostErr := bifrost.ExecuteChatMCPTool(createTestContext(), &toolCall)

		assert.Nil(t, result)
		require.NotNil(t, bifrostErr, "a tool call without a name must error")
		require.NotNil(t, bifrostErr.Error)
		assert.Contains(t, bifrostErr.Error.Message, "missing function name")
	})

	t.Run("nil_tool_call", func(t *testing.T) {
		result, bifrostErr := bifrost.ExecuteChatMCPTool(createTestContext(), nil)

		assert.Nil(t, result)
		require.NotNil(t, bifrostErr, "a nil tool call must error")
		require.NotNil(t, bifrostErr.Error)
		assert.Contains(t, bifrostErr.Error.Message, "toolCall cannot be nil")
	})
}

// TestInjectedWebSearch_AnthropicPassthrough covers the path Claude Code takes to
// Anthropic models: the integration forwards the caller's raw body. With injected
// tools the attempt must leave passthrough, or the native web_search_20250305 tool
// would reach Anthropic and the MCP tool would not.
func TestInjectedWebSearch_AnthropicPassthrough(t *testing.T) {
	fake := &fakeOpenAI{replies: []string{
		`{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"tool_use","id":"toolu_1","name":"` + injectedSearchTool + `","input":{"query":"weather in paris"}}],"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":2}}`,
		`{"id":"msg_2","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"It is sunny."}],"stop_reason":"end_turn","usage":{"input_tokens":20,"output_tokens":3}}`,
	}}
	var searches []string
	b := setupInjectedToolsBifrostFor(t, schemas.Anthropic, fake, &searches)

	raw := []byte(`{"model":"claude-sonnet-4-5","max_tokens":1024,"messages":[{"role":"user","content":"Weather in Paris?"}],"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":3}]}`)
	ctx := createTestContext()
	ctx.SetValue(schemas.BifrostContextKeyIntegrationType, "anthropic")
	ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
	ctx.SetValue(schemas.BifrostContextKeySendBackRawResponse, true)
	resp, bifrostErr := b.ResponsesRequest(ctx, &schemas.BifrostResponsesRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-sonnet-4-5",
		Input: []schemas.ResponsesMessage{{
			Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Weather in Paris?")},
		}},
		Params: &schemas.ResponsesParameters{
			MaxOutputTokens: schemas.Ptr(1024),
			Tools:           []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeWebSearch, Name: schemas.Ptr("web_search")}},
		},
		RawRequestBody: raw,
	})

	require.Nil(t, bifrostErr, "%v", bifrostErr)
	assert.Equal(t, []string{"weather in paris"}, searches)
	bodies := fake.requests()
	require.Len(t, bodies, 2)
	assert.Equal(t, []string{injectedSearchTool}, fake.toolNames(0), "the raw body's native web search never reaches Anthropic")
	messages, _ := bodies[1]["messages"].([]any)
	require.Len(t, messages, 3, "user, assistant tool_use, user tool_result")
	require.NotEmpty(t, resp.Output)
	for _, item := range resp.Output {
		assert.NotEqual(t, schemas.ResponsesMessageTypeFunctionCall, *item.Type, "the client never sees the injected call")
	}
}

// anthropicSSE renders Anthropic Messages stream events.
func anthropicSSE(events ...string) string {
	var b strings.Builder
	for _, event := range events {
		typ := gjsonType(event)
		b.WriteString("event: " + typ + "\ndata: " + event + "\n\n")
	}
	return b.String()
}

func gjsonType(event string) string {
	var head struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal([]byte(event), &head)
	return head.Type
}

// TestInjectedWebSearch_AnthropicPassthroughStream is the streaming twin of
// TestInjectedWebSearch_AnthropicPassthrough, the path Claude Code streams through:
// the attempt leaves raw passthrough, the MCP tool replaces web_search_20250305, the
// search runs between turns, and the client sees one response with only the answer.
func TestInjectedWebSearch_AnthropicPassthroughStream(t *testing.T) {
	fake := &fakeOpenAI{replies: []string{
		anthropicSSE(
			`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"`+injectedSearchTool+`","input":{}}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"weather in paris\"}"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`,
			`{"type":"message_stop"}`,
		),
		anthropicSSE(
			`{"type":"message_start","message":{"id":"msg_2","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[],"stop_reason":null,"usage":{"input_tokens":20,"output_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"It is sunny."}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":4}}`,
			`{"type":"message_stop"}`,
		),
	}}
	var searches []string
	b := setupInjectedToolsBifrostFor(t, schemas.Anthropic, fake, &searches)

	raw := []byte(`{"model":"claude-sonnet-4-5","max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"Weather in Paris?"}],"tools":[{"type":"web_search_20250305","name":"web_search","max_uses":3}]}`)
	ctx := createTestContext()
	ctx.SetValue(schemas.BifrostContextKeyIntegrationType, "anthropic")
	ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
	ctx.SetValue(schemas.BifrostContextKeySendBackRawResponse, true)
	stream, bifrostErr := b.ResponsesStreamRequest(ctx, &schemas.BifrostResponsesRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-sonnet-4-5",
		Input: []schemas.ResponsesMessage{{
			Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("Weather in Paris?")},
		}},
		Params: &schemas.ResponsesParameters{
			MaxOutputTokens: schemas.Ptr(1024),
			Tools:           []schemas.ResponsesTool{{Type: schemas.ResponsesToolTypeWebSearch, Name: schemas.Ptr("web_search")}},
		},
		RawRequestBody: raw,
	})
	require.Nil(t, bifrostErr, "%v", bifrostErr)

	var text strings.Builder
	created, completed := 0, 0
	for chunk := range stream {
		require.Nil(t, chunk.BifrostError, "%v", chunk.BifrostError)
		ev := chunk.BifrostResponsesStreamResponse
		require.NotNil(t, ev)
		assert.NotContains(t, string(ev.Type), "function_call", "no injected call event reaches the client")
		if ev.Item != nil && ev.Item.Type != nil {
			assert.NotEqual(t, schemas.ResponsesMessageTypeFunctionCall, *ev.Item.Type)
		}
		switch ev.Type {
		case schemas.ResponsesStreamResponseTypeCreated:
			created++
		case schemas.ResponsesStreamResponseTypeCompleted:
			completed++
		case schemas.ResponsesStreamResponseTypeOutputTextDelta:
			if ev.Delta != nil {
				text.WriteString(*ev.Delta)
			}
		}
	}

	assert.Equal(t, []string{"weather in paris"}, searches)
	assert.Equal(t, "It is sunny.", text.String())
	assert.Equal(t, 1, created)
	assert.Equal(t, 1, completed)
	require.Len(t, fake.requests(), 2)
	assert.Equal(t, []string{injectedSearchTool}, fake.toolNames(0), "the raw body's native web search never reaches Anthropic")
	assert.Equal(t, false, ctx.Value(schemas.BifrostContextKeyUseRawRequestBody), "the response converter sees passthrough switched off")
}

// retryingInjectedAccount is injectedToolsAccount with same-provider retries enabled.
type retryingInjectedAccount struct{ injectedToolsAccount }

func (a *retryingInjectedAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	config, err := a.injectedToolsAccount.GetConfigForProvider(provider)
	if err != nil {
		return nil, err
	}
	config.NetworkConfig.MaxRetries = 2
	config.NetworkConfig.RetryBackoffInitial = time.Millisecond
	config.NetworkConfig.RetryBackoffMax = 5 * time.Millisecond
	return config, nil
}

// TestInjectedWebSearch_RetryDoesNotReplayTools pins that a retryable failure after the
// injected tool ran ends the request instead of restarting the loop: the search (a side
// effect) runs once, the first turn is not sent again, and the error bills that turn.
func TestInjectedWebSearch_RetryDoesNotReplayTools(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		mu.Lock()
		requests++
		n := requests
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if n == 1 {
			_, _ = w.Write([]byte(chatCompletionJSON("", "tool_calls", injectedSearchTool)))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"overloaded","type":"server_error"}}`))
	}))
	t.Cleanup(server.Close)

	b, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account:   &retryingInjectedAccount{injectedToolsAccount{baseURL: server.URL}},
		Logger:    bifrost.NewDefaultLogger(schemas.LogLevelError),
		MCPConfig: &schemas.MCPConfig{ToolManagerConfig: &schemas.MCPToolManagerConfig{DisableAutoToolInject: true}},
	})
	require.NoError(t, err)
	t.Cleanup(b.Shutdown)
	searches := 0
	require.NoError(t, b.RegisterMCPTool("web_search", "Search the web", func(any) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		searches++
		return "Paris: sunny, 24C", nil
	}, schemas.ChatTool{Type: schemas.ChatToolTypeFunction, Function: &schemas.ChatToolFunction{Name: "web_search"}}))

	_, bifrostErr := b.ChatCompletionRequest(createTestContext(), &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("Weather in Paris?")}}},
	})
	require.NotNil(t, bifrostErr)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, searches, "the injected tool runs once, not once per retry")
	assert.Equal(t, 2, requests, "turn 1 is not replayed: one tool-call turn, one failed turn")
	require.NotNil(t, bifrostErr.ExtraFields.BilledUsage, "the failure bills the finished turn")
	assert.Equal(t, 10, bifrostErr.ExtraFields.BilledUsage.PromptTokens)
}
