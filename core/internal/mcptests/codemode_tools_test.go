package mcptests

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

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
	// The client's ToolsToExecute filters which tools are available
	// The test server provides YouTube tools, so we'll call youtube_search_you_tube
	code := `r = TestCodeModeServer.youtube_search_you_tube(query="golang")
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

	ctx := createTestContext()

	// Execute code that calls error-throwing tool
	code := `def main():
    r = bifrostInternal.throw_error(error_message="intentional error")
    # In Starlark, errors are returned in the result
    if "error" in str(r) or r == None:
        return {"success": False, "error": str(r), "caught": True}
    else:
        return {"success": True, "result": r}
result = main()`

	argsJSON, _ := json.Marshal(map[string]interface{}{
		"code": code,
	})

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-error-tool"),
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

	// Tool errors must be propagated as runtime errors in Starlark execution
	// The test fails if the error is caught in code instead of being propagated
	require.True(t, hasError, "tool errors must propagate as runtime errors, not be caught in code")
	assert.Contains(t, errorMsg, "intentional error", "error should contain the thrown error message")
	t.Logf("Tool error propagated as expected: %s", errorMsg)
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
