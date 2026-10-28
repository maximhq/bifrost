package mcptests

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// CODE MODE WITH TOOL AVAILABILITY TESTS
// =============================================================================

func TestCodeMode_NoToolsAvailable(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Setup code mode client with no other clients (no tools available)
	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)
	manager := setupMCPManager(t, codeModeClient)
	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute code that tries to call a tool (server object won't exist)
	code := `def main():
    if hasattr(httpserver, "echo"):
        return httpserver.echo(message="test")
    return "Error: httpserver not defined or echo not available"
result = main()`

	argsJSON, _ := json.Marshal(map[string]interface{}{
		"code": code,
	})

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-no-tools"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("executeToolCode"),
			Arguments: string(argsJSON),
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
	// Note: hasError might be false if error is in return value
	resultStr := fmt.Sprintf("%v", returnValue)
	if hasError {
		resultStr = errorMsg
	}
	assert.Contains(t, strings.ToLower(resultStr), "error")
	t.Logf("Result: %s", resultStr)
}

func TestCodeMode_SomeToolsAvailable(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Setup code mode client with all tools available
	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)

	manager := setupMCPManager(t, codeModeClient)
	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute code that calls available tool from code mode client
	// Note: In code mode, code mode clients are bound in the execution environment
	// The client's ToolsToExecute filters which tools are available.
	//
	// `echo` is one of the three tools examples/mcps/remote-test-server exposes
	// (see TestMain in remoteserver_test.go). This previously called
	// youtube_search_you_tube, a tool that existed only on the hosted MCP
	// instance the suite used to point at - the assertion is about code-mode
	// dispatch returning a dict, not about any particular tool's semantics.
	code := `r = TestCodeModeServer.echo(message="golang")
result = type(r)`

	argsJSON, _ := json.Marshal(map[string]interface{}{
		"code": code,
	})

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-some-tools"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("executeToolCode"),
			Arguments: string(argsJSON),
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
	require.False(t, hasError, "should not have execution error: %s", errorMsg)

	// Should succeed and return the type of the result
	assert.NotNil(t, returnValue)
	resultStr := fmt.Sprintf("%v", returnValue)
	// Should return "dict" since Starlark's type() returns "dict" for dictionaries
	assert.Contains(t, resultStr, "dict")
}

// =============================================================================
// CODE CALLING MCP TOOLS TESTS
// =============================================================================

func TestCodeMode_CallingMCPTool(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Setup code mode client
	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)

	manager := setupMCPManager(t, codeModeClient)
	// Register in-process tools
	require.NoError(t, RegisterEchoTool(manager))
	// Make internal client a code-mode client so its tools are available
	require.NoError(t, SetInternalClientAsCodeMode(manager, []string{"*"}))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute code: serverName.echo(message="test")
	code := `echoResult = bifrostInternal.echo(message="Testing MCP call")
print("Echo result:", echoResult)
result = {
    "echo": echoResult,
    "success": True
}`

	argsJSON, _ := json.Marshal(map[string]interface{}{
		"code": code,
	})

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-mcp-tool"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("executeToolCode"),
			Arguments: string(argsJSON),
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
	require.False(t, hasError, "should not have execution error: %s", errorMsg)

	// Verify tool was called and result returned
	assert.NotNil(t, returnValue)
	resultObj, ok := returnValue.(map[string]interface{})
	require.True(t, ok)
	assert.True(t, resultObj["success"].(bool))
	assert.Contains(t, fmt.Sprintf("%v", resultObj["echo"]), "Testing MCP call")
}

func TestCodeMode_CallingMultipleServers(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Setup code mode client
	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)

	manager := setupMCPManager(t, codeModeClient)
	// Register in-process tools
	require.NoError(t, RegisterEchoTool(manager))
	// Make internal client a code-mode client so its tools are available
	require.NoError(t, SetInternalClientAsCodeMode(manager, []string{"*"}))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute code that calls tools from both servers
	code := `result1 = bifrostInternal.echo(message="from HTTP")
result2 = bifrostInternal.echo(message="from SSE")
result = {
    "http": result1,
    "sse": result2
}`

	argsJSON, _ := json.Marshal(map[string]interface{}{
		"code": code,
	})

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-multi-servers"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("executeToolCode"),
			Arguments: string(argsJSON),
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
	require.False(t, hasError, "should not have execution error: %s", errorMsg)

	// Verify both calls worked
	resultObj, ok := returnValue.(map[string]interface{})
	require.True(t, ok)
	assert.Contains(t, fmt.Sprintf("%v", resultObj["http"]), "from HTTP")
	assert.Contains(t, fmt.Sprintf("%v", resultObj["sse"]), "from SSE")
}

func TestCodeMode_CallingCodeModeClient(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Setup 2 code mode clients
	codeModeClient1 := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)
	codeModeClient1.ID = "codemode1"

	codeModeClient2 := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)
	codeModeClient2.ID = "codemode2"

	manager := setupMCPManager(t, codeModeClient1, codeModeClient2)
	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute code on client 1 that tries to call executeToolCode on client 2
	code := `# Code mode clients don't expose their tools to other clients
def main():
    if "codemode2" in dir() and hasattr(codemode2, "executeToolCode"):
        return codemode2.executeToolCode(code="result = 42")
    else:
        return {"error": "codemode2 not accessible", "expected": "Code mode tools not accessible"}
result = main()`

	argsJSON, _ := json.Marshal(map[string]interface{}{
		"code": code,
	})

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-codemode"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("executeToolCode"),
			Arguments: string(argsJSON),
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
	if hasError {
		t.Logf("Error: %s", errorMsg)
	} else {
		t.Logf("Result: %+v", returnValue)
	}
}

func TestCodeMode_NestedToolCalls(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Setup code mode client
	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)

	manager := setupMCPManager(t, codeModeClient)
	// Register in-process tools
	require.NoError(t, RegisterEchoTool(manager))
	require.NoError(t, RegisterCalculatorTool(manager))
	// Make internal client a code-mode client so its tools are available
	require.NoError(t, SetInternalClientAsCodeMode(manager, []string{"*"}))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute code that calls a tool, processes result, then calls another tool
	code := `# First call
echo1 = bifrostInternal.echo(message="step 1")

# Process result
processed = "Processed: " + str(echo1)

# Second call using processed result
echo2 = bifrostInternal.echo(message=processed)

# Third call
calc = bifrostInternal.calculator(operation="add", x=5, y=3)

result = {
    "step1": echo1,
    "step2": echo2,
    "step3": calc
}`

	argsJSON, _ := json.Marshal(map[string]interface{}{
		"code": code,
	})

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-nested"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("executeToolCode"),
			Arguments: string(argsJSON),
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
	require.False(t, hasError, "should not have execution error: %s", errorMsg)

	// Verify nested execution worked correctly
	resultObj, ok := returnValue.(map[string]interface{})
	require.True(t, ok)
	assert.Contains(t, fmt.Sprintf("%v", resultObj["step1"]), "step 1")
	assert.Contains(t, fmt.Sprintf("%v", resultObj["step2"]), "Processed")
	assert.NotNil(t, resultObj["step3"])
}

// =============================================================================
// FILTERING IN CODE MODE TESTS
// =============================================================================

func TestCodeMode_ToolNotInExecuteList(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Setup code mode client
	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)

	manager := setupMCPManager(t, codeModeClient)
	// Register both tools
	require.NoError(t, RegisterEchoTool(manager))
	require.NoError(t, RegisterCalculatorTool(manager))
	// Make internal client a code-mode client with filtering - only allow calculator, not echo
	require.NoError(t, SetInternalClientAsCodeMode(manager, []string{"calculator"}))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute code that tries to call filtered-out tool (echo)
	code := `def main():
    if hasattr(bifrostInternal, "echo"):
        r = bifrostInternal.echo(message="blocked")
        return {"success": True, "result": r}
    else:
        return {"success": False, "error": "echo not available"}
result = main()`

	argsJSON, _ := json.Marshal(map[string]interface{}{
		"code": code,
	})

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-filtered"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("executeToolCode"),
			Arguments: string(argsJSON),
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
	require.False(t, hasError, "should not have execution error: %s", errorMsg)

	// Should fail with appropriate error
	resultObj, ok := returnValue.(map[string]interface{})
	require.True(t, ok)
	assert.False(t, resultObj["success"].(bool))
	assert.NotEmpty(t, resultObj["error"])
	t.Logf("Error: %s", resultObj["error"])
}

func TestCodeMode_NonAllowedToolExecution(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Setup code mode client
	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)

	manager := setupMCPManager(t, codeModeClient)
	// Register tools
	require.NoError(t, RegisterEchoTool(manager))
	// Make internal client a code-mode client with empty tools list = deny all
	require.NoError(t, SetInternalClientAsCodeMode(manager, []string{}))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute code that tries to access bifrostInternal
	// When ToolsToExecute = [], the server won't be bound in the environment at all
	// This should cause a runtime error: "undefined: bifrostInternal"
	code := `result = bifrostInternal.echo(message="should fail")`

	argsJSON, _ := json.Marshal(map[string]interface{}{
		"code": code,
	})

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-denied"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("executeToolCode"),
			Arguments: string(argsJSON),
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	_, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)

	// Verify we get an error because bifrostInternal is not defined
	// When ToolsToExecute = [], the server is not bound in the Starlark environment
	require.True(t, hasError, "should have execution error because bifrostInternal is not bound when ToolsToExecute is empty")
	assert.Contains(t, strings.ToLower(errorMsg), "undefined", "error should indicate bifrostInternal is undefined")
	t.Logf("Expected error (bifrostInternal unbound due to empty ToolsToExecute): %s", errorMsg)
}

func TestCodeMode_ToolExecutionTimeout(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Setup with a tool that can timeout
	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)

	manager := setupMCPManager(t, codeModeClient)
	// Use echo tool
	require.NoError(t, RegisterEchoTool(manager))
	// Make internal client a code-mode client
	require.NoError(t, SetInternalClientAsCodeMode(manager, []string{"*"}))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute code that calls a tool
	code := `if hasattr(bifrostInternal, "echo"):
    r = bifrostInternal.echo(message="test")
    result = {"success": True, "result": r}
else:
    result = {"success": False, "error": "echo not available"}`

	argsJSON, _ := json.Marshal(map[string]interface{}{
		"code": code,
	})

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-timeout-tool"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("executeToolCode"),
			Arguments: string(argsJSON),
		},
	}

	// Use shorter overall timeout
	startTime := time.Now()
	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	duration := time.Since(startTime)

	// Tool should timeout (default 30s but may be configured lower)
	t.Logf("Execution took: %v", duration)

	if bifrostErr == nil && result != nil {
		returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
		if hasError {
			t.Logf("Error: %s", errorMsg)
		} else {
			t.Logf("Result: %+v", returnValue)
		}
	}
}

// =============================================================================
// CODE MODE TOOL CALL SYNTAX TESTS
// =============================================================================

func TestCodeMode_ToolCallWithAwait(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)

	manager := setupMCPManager(t, codeModeClient)
	// Register calculator tool
	require.NoError(t, RegisterCalculatorTool(manager))
	// Make internal client a code-mode client
	require.NoError(t, SetInternalClientAsCodeMode(manager, []string{"*"}))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute: result = server.tool(args)
	code := `result = bifrostInternal.calculator(operation="multiply", x=6, y=7)`

	argsJSON, _ := json.Marshal(map[string]interface{}{
		"code": code,
	})

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-await"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("executeToolCode"),
			Arguments: string(argsJSON),
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
	require.False(t, hasError, "should not have execution error: %s", errorMsg)

	// Verify Starlark syntax works
	assert.NotNil(t, returnValue)
	assert.Contains(t, fmt.Sprintf("%v", returnValue), "42")
}

func TestCodeMode_ToolCallWithoutAwait(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)

	manager := setupMCPManager(t, codeModeClient)
	// Register echo tool
	require.NoError(t, RegisterEchoTool(manager))
	// Make internal client a code-mode client
	require.NoError(t, SetInternalClientAsCodeMode(manager, []string{"*"}))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute: result = server.tool(args) - Starlark is synchronous
	code := `result = bifrostInternal.echo(message="promise test")`

	argsJSON, _ := json.Marshal(map[string]interface{}{
		"code": code,
	})

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-promise"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("executeToolCode"),
			Arguments: string(argsJSON),
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
	require.False(t, hasError, "should not have execution error: %s", errorMsg)

	// Verify synchronous execution works
	assert.Contains(t, fmt.Sprintf("%v", returnValue), "promise test")
}

func TestCodeMode_MultipleSequentialToolCalls(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)

	manager := setupMCPManager(t, codeModeClient)
	// Register echo tool
	require.NoError(t, RegisterEchoTool(manager))
	// Make internal client a code-mode client
	require.NoError(t, SetInternalClientAsCodeMode(manager, []string{"*"}))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute code with multiple calls in sequence
	code := `results = []

results.append(bifrostInternal.echo(message="first"))
results.append(bifrostInternal.echo(message="second"))
results.append(bifrostInternal.echo(message="third"))

result = results`

	argsJSON, _ := json.Marshal(map[string]interface{}{
		"code": code,
	})

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-sequential"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("executeToolCode"),
			Arguments: string(argsJSON),
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
	require.False(t, hasError, "should not have execution error: %s", errorMsg)

	// Verify all execute in order
	results, ok := returnValue.([]interface{})
	require.True(t, ok)
	assert.Len(t, results, 3)
	assert.Contains(t, fmt.Sprintf("%v", results[0]), "first")
	assert.Contains(t, fmt.Sprintf("%v", results[1]), "second")
	assert.Contains(t, fmt.Sprintf("%v", results[2]), "third")
}

func TestCodeMode_MultipleParallelToolCalls(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)

	manager := setupMCPManager(t, codeModeClient)
	// Register tools
	require.NoError(t, RegisterEchoTool(manager))
	require.NoError(t, RegisterCalculatorTool(manager))
	// Make internal client a code-mode client
	require.NoError(t, SetInternalClientAsCodeMode(manager, []string{"*"}))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute code: sequential calls (Starlark is synchronous)
	code := `r1 = bifrostInternal.echo(message="parallel1")
r2 = bifrostInternal.echo(message="parallel2")
r3 = bifrostInternal.calculator(operation="add", x=10, y=20)

result = {
    "echo1": r1,
    "echo2": r2,
    "calc": r3
}`

	argsJSON, _ := json.Marshal(map[string]interface{}{
		"code": code,
	})

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-parallel"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("executeToolCode"),
			Arguments: string(argsJSON),
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
	require.False(t, hasError, "should not have execution error: %s", errorMsg)

	// Verify sequential execution works
	resultObj, ok := returnValue.(map[string]interface{})
	require.True(t, ok)
	assert.Contains(t, fmt.Sprintf("%v", resultObj["echo1"]), "parallel1")
	assert.Contains(t, fmt.Sprintf("%v", resultObj["echo2"]), "parallel2")
	assert.NotNil(t, resultObj["calc"])
}

// =============================================================================
// ERROR HANDLING IN CODE MODE TESTS
// =============================================================================

func TestCodeMode_ToolReturnsError(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)

	manager := setupMCPManager(t, codeModeClient)
	// Register error-throwing tool
	require.NoError(t, RegisterThrowErrorTool(manager))
	// Make internal client a code-mode client
	require.NoError(t, SetInternalClientAsCodeMode(manager, []string{"*"}))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	run := func(t *testing.T, code string) *schemas.ChatMessage {
		t.Helper()
		argsJSON, _ := json.Marshal(map[string]interface{}{"code": code})
		toolCall := schemas.ChatAssistantMessageToolCall{
			ID:   schemas.Ptr("call-error-tool"),
			Type: schemas.Ptr("function"),
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name:      schemas.Ptr("executeToolCode"),
				Arguments: string(argsJSON),
			},
		}
		result, bifrostErr := bifrost.ExecuteChatMCPTool(createTestContext(), &toolCall)
		require.Nil(t, bifrostErr)
		require.NotNil(t, result)
		return result
	}

	// An in-process tool error arrives as isError with "Error: " text. Like any failed call it
	// is an error value the script handles, and the reply notes it even when the script does not.
	t.Run("an error value the script handles", func(t *testing.T) {
		msg := run(t, `r = bifrostInternal.throw_error(error_message="intentional error")
result = {"failed": hasattr(r, "error"), "message": r.error if hasattr(r, "error") else r}`)
		text := *msg.Content.ContentStr

		value, hasError, errorMsg := ParseCodeModeResponse(t, text)
		require.False(t, hasError, errorMsg)
		assert.Equal(t, map[string]interface{}{"failed": true, "message": "intentional error"}, value)
		assert.Contains(t, text, "[TOOL] bifrostInternal.throw_error returned an error: intentional error")
		assert.Contains(t, text, "Execution completed, but 1 nested tool call failed.")
		assert.False(t, runFailed(msg), "the script finished, so the run succeeds:\n%s", text)
	})

	t.Run("fail() makes it a runtime error", func(t *testing.T) {
		msg := run(t, `r = bifrostInternal.throw_error(error_message="intentional error")
if hasattr(r, "error"):
    fail(r.error)
result = r`)
		text := *msg.Content.ContentStr

		_, hasError, errorMsg := ParseCodeModeResponse(t, text)
		require.True(t, hasError, "fail() must make the run a runtime error:\n%s", text)
		assert.Contains(t, errorMsg, "intentional error")
		assert.True(t, runFailed(msg), "fail() must mark the run as failed:\n%s", text)
	})
}

func TestCodeMode_ToolNotFound(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)

	manager := setupMCPManager(t, codeModeClient)
	// Register a dummy tool just to ensure internal client exists, then make it code-mode
	require.NoError(t, RegisterEchoTool(manager))
	require.NoError(t, SetInternalClientAsCodeMode(manager, []string{})) // Empty list, no tools accessible

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute code that tries to use bifrostInternal which won't be bound
	// When ToolsToExecute = [], the server won't be bound in the environment at all
	// This should cause a runtime error: "undefined: bifrostInternal"
	code := `result = bifrostInternal.nonexistent_tool(param="value")`

	argsJSON, _ := json.Marshal(map[string]interface{}{
		"code": code,
	})

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-not-found"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("executeToolCode"),
			Arguments: string(argsJSON),
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	_, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)

	// Verify we get an error because bifrostInternal is not defined
	// When ToolsToExecute = [], the server is not bound in the Starlark environment
	require.True(t, hasError, "should have execution error because bifrostInternal is not bound")
	assert.Contains(t, errorMsg, "undefined")
	t.Logf("Expected error: %s", errorMsg)
}

// =============================================================================
// BOTH API FORMATS TESTS
// =============================================================================

func TestCodeMode_ChatFormat(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Execute code with tool calls in Chat format
	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)

	manager := setupMCPManager(t, codeModeClient)
	// Register calculator tool
	require.NoError(t, RegisterCalculatorTool(manager))
	// Make internal client a code-mode client
	require.NoError(t, SetInternalClientAsCodeMode(manager, []string{"*"}))

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	code := `r = bifrostInternal.calculator(operation="divide", x=100, y=4)
result = {"result": r, "format": "chat"}`

	argsJSON, _ := json.Marshal(map[string]interface{}{
		"code": code,
	})

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-chat-format"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("executeToolCode"),
			Arguments: string(argsJSON),
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
	require.False(t, hasError, "should not have execution error: %s", errorMsg)

	resultObj, ok := returnValue.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "chat", resultObj["format"])
	assert.Contains(t, fmt.Sprintf("%v", resultObj["result"]), "25")
}

func TestCodeMode_ResponsesFormat(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Execute code with tool calls in Responses format
	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)
	httpClient := GetSampleHTTPClientConfig(config.HTTPServerURL)
	httpClient.ID = "responsesserver"
	httpClient.ToolsToExecute = []string{"*"}

	manager := setupMCPManager(t, codeModeClient, httpClient)
	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	code := `r = TestCodeModeServer.calculator(operation="subtract", x=50, y=8)
result = {"result": r, "format": "responses"}`

	argsJSON, _ := json.Marshal(map[string]interface{}{
		"code": code,
	})

	responsesTool := schemas.ResponsesToolMessage{
		CallID:    schemas.Ptr("call-responses-format"),
		Name:      schemas.Ptr("executeToolCode"),
		Arguments: schemas.Ptr(string(argsJSON)),
	}

	result, bifrostErr := bifrost.ExecuteResponsesMCPTool(ctx, &responsesTool)
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	if result.Content != nil && result.Content.ContentStr != nil {
		returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
		require.False(t, hasError, "should not have execution error: %s", errorMsg)

		resultObj, ok := returnValue.(map[string]interface{})
		require.True(t, ok)
		assert.Equal(t, "responses", resultObj["format"])
		assert.Contains(t, fmt.Sprintf("%v", resultObj["result"]), "42")
	}
}

// =============================================================================
// RESULT SHAPES REACHING THE SCRIPT
// =============================================================================

// resultShapesPayloadMarker opens the big_payload result, so an echo of that payload is easy to spot.
const resultShapesPayloadMarker = "SHAPES-PAYLOAD-MARKER"

// buildResultShapesServer serves one tool per result shape code mode has to hand a script:
// spec-style errors with and without an "Error: " prefix, legacy "Error: " text, a large
// payload, several JSON blocks, structured content, and JSON wrapped in a JSON string.
func buildResultShapesServer(t *testing.T) *server.MCPServer {
	t.Helper()
	s := server.NewMCPServer("result-shapes", "1.0.0", server.WithToolCapabilities(true))
	add := func(name string, result *mcpgo.CallToolResult) {
		s.AddTool(mcpgo.NewTool(name, mcpgo.WithDescription(name)), func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return result, nil
		})
	}
	// isError with plain text, the way mcp-go and most SDK servers report a failure.
	add("plain_error", mcpgo.NewToolResultError("quota exhausted for this key"))
	// isError with "Error: " text, the way Bifrost's own in-process tools report a failure.
	add("prefixed_error", mcpgo.NewToolResultError("Error: contact not found"))
	// "Error: " text without isError: the legacy soft failure.
	add("prefixed_text", mcpgo.NewToolResultText("Error: legacy failure"))
	add("big_payload", mcpgo.NewToolResultText(`{"body":"`+resultShapesPayloadMarker+strings.Repeat("x", 4096)+`"}`))
	add("multi_block", &mcpgo.CallToolResult{Content: []mcpgo.Content{
		mcpgo.NewTextContent(`{"id":1}`),
		mcpgo.NewTextContent(`{"id":2}`),
	}})
	add("structured", &mcpgo.CallToolResult{
		Content:           []mcpgo.Content{mcpgo.NewTextContent(`{"title":"from text"}`)},
		StructuredContent: map[string]any{"title": "from structured"},
	})
	add("json_string", mcpgo.NewToolResultText(`"{\"title\":\"Example Domain\"}"`))
	add("quoted_number", mcpgo.NewToolResultText(`"123"`))
	return s
}

// runResultShapesCode runs code through executeToolCode with the result-shapes server bound as
// "shapes", and returns the reply text.
func runResultShapesCode(t *testing.T, code string) string {
	t.Helper()
	return *runResultShapesCall(t, code).Content.ContentStr
}

// runResultShapesCall is runResultShapesCode returning the whole tool message.
func runResultShapesCall(t *testing.T, code string) *schemas.ChatMessage {
	t.Helper()
	manager := setupMCPManager(t)
	require.NoError(t, manager.AddClient(context.Background(), &schemas.MCPClientConfig{
		ID:               "result-shapes-id",
		Name:             "shapes",
		ConnectionType:   schemas.MCPConnectionTypeInProcess,
		InProcessServer:  buildResultShapesServer(t),
		IsCodeModeClient: true,
		ToolsToExecute:   []string{"*"},
	}))
	bf := setupBifrost(t)
	bf.SetMCPManager(manager)

	argsJSON, err := json.Marshal(map[string]interface{}{"code": code})
	require.NoError(t, err)
	result, bifrostErr := bf.ExecuteChatMCPTool(createTestContext(), &schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-result-shapes"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("executeToolCode"),
			Arguments: string(argsJSON),
		},
	})
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)
	require.NotNil(t, result.Content)
	require.NotNil(t, result.Content.ContentStr)
	return result
}

// runFailed reports whether executeToolCode marked the run as failed.
func runFailed(msg *schemas.ChatMessage) bool {
	return msg.ChatToolMessage != nil && msg.ChatToolMessage.IsError != nil && *msg.ChatToolMessage.IsError
}

func TestCodeMode_ReplyCarriesNoToolPayload(t *testing.T) {
	t.Parallel()

	text := runResultShapesCode(t, "r = shapes.big_payload()\nresult = len(r[\"body\"])")

	value, hasError, errorMsg := ParseCodeModeResponse(t, text)
	require.False(t, hasError, errorMsg)
	assert.EqualValues(t, len(resultShapesPayloadMarker)+4096, value)
	assert.NotContains(t, text, resultShapesPayloadMarker, "a payload the script never printed reached the reply")
	assert.NotContains(t, text, "[TOOL]")
	assert.NotContains(t, text, "Environment:", "a successful run carries no environment footer")
}

// A call that answers with isError hands the script an error value: hasattr(r, "error") detects
// it, r.error holds the message, and returned as-is it becomes {"error": ...}. The script finished,
// so the run succeeds, and the reply still notes the failed call.
func TestCodeMode_FailedCallIsAnErrorValue(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		code string
		want interface{}
	}{
		{
			name: "detected and read",
			code: "r = shapes.plain_error()\nresult = {\"failed\": hasattr(r, \"error\"), \"message\": r.error if hasattr(r, \"error\") else r}",
			want: map[string]interface{}{"failed": true, "message": "quota exhausted for this key"},
		},
		{
			name: "returned as-is",
			code: "result = shapes.plain_error()",
			want: map[string]interface{}{"error": "quota exhausted for this key"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := runResultShapesCall(t, tc.code)
			text := *msg.Content.ContentStr

			value, hasError, errorMsg := ParseCodeModeResponse(t, text)
			require.False(t, hasError, errorMsg)
			assert.Equal(t, tc.want, value)
			assert.Contains(t, text, "[TOOL] shapes.plain_error returned an error: quota exhausted for this key")
			assert.Contains(t, text, "Execution completed, but 1 nested tool call failed.")
			assert.False(t, runFailed(msg), "the script finished, so the run succeeds:\n%s", text)
		})
	}
}

// A failure the server flags with isError is an error value even when its text opens with
// "Error: ", which loses the prefix. Only "Error: " text the server did not flag still raises.
func TestCodeMode_ErrorPrefixedResults(t *testing.T) {
	t.Parallel()

	t.Run("flagged with isError", func(t *testing.T) {
		msg := runResultShapesCall(t, "r = shapes.prefixed_error()\nresult = {\"failed\": hasattr(r, \"error\"), \"message\": r.error if hasattr(r, \"error\") else r}")
		text := *msg.Content.ContentStr

		value, hasError, errorMsg := ParseCodeModeResponse(t, text)
		require.False(t, hasError, errorMsg)
		assert.Equal(t, map[string]interface{}{"failed": true, "message": "contact not found"}, value)
		assert.Contains(t, text, "[TOOL] shapes.prefixed_error returned an error: contact not found")
		assert.Contains(t, text, "Execution completed, but 1 nested tool call failed.")
		assert.False(t, runFailed(msg), "the script finished, so the run succeeds:\n%s", text)
	})

	t.Run("not flagged", func(t *testing.T) {
		msg := runResultShapesCall(t, "r = shapes.prefixed_text()\nresult = \"kept going\"")
		text := *msg.Content.ContentStr

		_, hasError, _ := ParseCodeModeResponse(t, text)
		assert.True(t, hasError, "unflagged \"Error: \" text must still raise:\n%s", text)
		assert.Contains(t, text, "legacy failure")
		assert.NotContains(t, text, "kept going")
		assert.True(t, runFailed(msg), "a raised call fails the run:\n%s", text)
	})
}

// fail() is how a script says a failed call is fatal: the run then fails.
func TestCodeMode_FailInScriptFailsTheRun(t *testing.T) {
	t.Parallel()

	msg := runResultShapesCall(t, "r = shapes.plain_error()\nif hasattr(r, \"error\"):\n    fail(\"lookup failed: \" + r.error)\nresult = \"unreachable\"")
	text := *msg.Content.ContentStr

	assert.True(t, runFailed(msg), "a script that called fail() must mark the run failed:\n%s", text)
	assert.Contains(t, text, "fail: lookup failed: quota exhausted for this key")
	assert.Contains(t, text, "Traceback (most recent call last):\n  code.star:3:", "the error should name the line the script stopped on")
	assert.Contains(t, text, "[TOOL] shapes.plain_error returned an error: quota exhausted for this key")
	assert.Contains(t, text, "The script stopped itself with fail()")
	assert.NotContains(t, text, "unreachable")
}

// A failed call the script recovers from leaves a successful run.
func TestCodeMode_FallbackAfterFailedCallSucceeds(t *testing.T) {
	t.Parallel()

	msg := runResultShapesCall(t, "r = shapes.plain_error()\nif hasattr(r, \"error\"):\n    r = shapes.json_string()\nresult = r[\"title\"]")
	text := *msg.Content.ContentStr

	value, hasError, errorMsg := ParseCodeModeResponse(t, text)
	require.False(t, hasError, errorMsg)
	assert.Equal(t, "Example Domain", value)
	assert.Contains(t, text, "Execution completed, but 1 nested tool call failed.")
	assert.False(t, runFailed(msg), "the script recovered, so the run succeeds:\n%s", text)
}

func TestCodeMode_MultiBlockResultIsAList(t *testing.T) {
	t.Parallel()

	text := runResultShapesCode(t, "r = shapes.multi_block()\nresult = [type(r)] + ([len(r), r[1][\"id\"]] if type(r) == \"list\" else [])")

	value, hasError, errorMsg := ParseCodeModeResponse(t, text)
	require.False(t, hasError, errorMsg)
	assert.Equal(t, []interface{}{"list", float64(2), float64(2)}, value)
}

func TestCodeMode_StructuredContentWinsOverText(t *testing.T) {
	t.Parallel()

	text := runResultShapesCode(t, "r = shapes.structured()\nresult = r[\"title\"]")

	value, hasError, errorMsg := ParseCodeModeResponse(t, text)
	require.False(t, hasError, errorMsg)
	assert.Equal(t, "from structured", value)
}

func TestCodeMode_JSONStringResultIsDecoded(t *testing.T) {
	t.Parallel()

	text := runResultShapesCode(t, "r = shapes.json_string()\nresult = r[\"title\"] if type(r) == \"dict\" else type(r)")

	value, hasError, errorMsg := ParseCodeModeResponse(t, text)
	require.False(t, hasError, errorMsg)
	assert.Equal(t, "Example Domain", value)
}

// Only an object or array is unwrapped: a quoted scalar is the string the server meant to send.
func TestCodeMode_QuotedScalarResultStaysString(t *testing.T) {
	t.Parallel()

	text := runResultShapesCode(t, "r = shapes.quoted_number()\nresult = [type(r), r]")

	value, hasError, errorMsg := ParseCodeModeResponse(t, text)
	require.False(t, hasError, errorMsg)
	assert.Equal(t, []interface{}{"string", "123"}, value)
}

// A code-mode tool call whose upstream needs input only a user-facing client can provide reaches
// the script with an explanation, not mcp-go's internal wording.
func TestCodeMode_UpstreamNeedsUserInput_ExplainsWhy(t *testing.T) {
	t.Parallel()

	s := server.NewMCPServer("needs-input-upstream", "1.0.0", server.WithToolCapabilities(true))
	s.AddTool(mcpgo.NewTool("elicit"), func(context.Context, mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		return server.NewInputRequestBuilder("").Elicit("github_login", mcpgo.ElicitationParams{Message: "Please provide your GitHub username"}).ToolResult(), nil
	})
	ts := httptest.NewServer(server.NewStreamableHTTPServer(s))
	t.Cleanup(func() {
		ts.CloseClientConnections()
		ts.Close()
	})

	manager := setupMCPManager(t, schemas.MCPClientConfig{
		ID:                     "needs-input-codemode",
		Name:                   "needsInput",
		ConnectionType:         schemas.MCPConnectionTypeHTTP,
		ConnectionString:       schemas.NewSecretVar(ts.URL),
		NeedsSessionStickiness: schemas.Ptr(true),
		IsCodeModeClient:       true,
		ToolsToExecute:         []string{"*"},
	})
	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	argsJSON, _ := json.Marshal(map[string]interface{}{"code": "result = needsInput.elicit()"})
	result, bifrostErr := bifrost.ExecuteChatMCPTool(createTestContext(), &schemas.ChatAssistantMessageToolCall{
		ID:       schemas.Ptr("call-needs-input"),
		Type:     schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("executeToolCode"), Arguments: string(argsJSON)},
	})
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)
	reply := *result.Content.ContentStr
	assert.Contains(t, reply, `MCP server "needsInput" asked for input from the user (elicitation), which Bifrost cannot provide`)
	assert.NotContains(t, reply, "handler configured", "mcp-go's internal wording must not leak")
}
