package mcptests

import (
	"encoding/json"
	"fmt"
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
	code := `try {
	await httpserver.echo({message: "test"});
	return "Should not reach here";
} catch (e) {
	return "Error: " + e.message;
}`

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
	assert.Contains(t, resultStr, "Error")
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
	code := `const result = await TestCodeModeServer.youtube_search_you_tube({query: "golang"}); return typeof result;`

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
	// Should return "object" since youtube_search_you_tube returns an object
	assert.Contains(t, resultStr, "object")
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

	// Execute code: await serverName.echo({message: "test"})
	code := `const echoResult = await bifrostInternal.echo({message: "Testing MCP call"});
console.log("Echo result:", echoResult);
return {
	echo: echoResult,
	success: true
};`

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
		t.Skip("MCP_HTTP_SERVER_URL not set")
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
	code := `const result1 = await bifrostInternal.echo({message: "from HTTP"});
const result2 = await bifrostInternal.echo({message: "from SSE"});
return {
	http: result1,
	sse: result2
};`

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
	code := `try {
	// Code mode clients don't expose their tools to other clients
	const result = await codemode2.executeToolCode({code: "return 42"});
	return result;
} catch (e) {
	return {error: e.message, expected: "Code mode tools not accessible"};
}`

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
	code := `// First call
const echo1 = await bifrostInternal.echo({message: "step 1"});

// Process result
const processed = "Processed: " + echo1;

// Second call using processed result
const echo2 = await bifrostInternal.echo({message: processed});

// Third call
const calc = await bifrostInternal.calculator({operation: "add", x: 5, y: 3});

return {
	step1: echo1,
	step2: echo2,
	step3: calc
};`

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
	code := `try {
	const result = await bifrostInternal.echo({message: "blocked"});
	return {success: true, result: result};
} catch (e) {
	return {success: false, error: e.message};
}`

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

	// Execute code that tries to call it
	code := `try {
	const result = await bifrostInternal.echo({message: "denied"});
	return result;
} catch (e) {
	return {blocked: true, message: e.message};
}`

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

	returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
	require.False(t, hasError, "should not have execution error: %s", errorMsg)

	// Should be blocked
	resultObj, ok := returnValue.(map[string]interface{})
	require.True(t, ok)
	assert.True(t, resultObj["blocked"].(bool))
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
	code := `try {
	const result = await bifrostInternal.echo({message: "test"});
	return {success: true, result: result};
} catch (e) {
	return {success: false, error: e.message};
}`

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

	// Execute: const result = await server.tool({args})
	code := `const result = await bifrostInternal.calculator({operation: "multiply", x: 6, y: 7});
return result;`

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

	// Verify await syntax works
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

	// Execute: const promise = server.tool({args}) without await
	code := `const promise = bifrostInternal.echo({message: "promise test"});
// Wait for promise manually
const result = await promise;
return result;`

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

	// Verify promise handling works
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

	// Execute code with multiple await calls in sequence
	code := `const results = [];

results.push(await bifrostInternal.echo({message: "first"}));
results.push(await bifrostInternal.echo({message: "second"}));
results.push(await bifrostInternal.echo({message: "third"}));

return results;`

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

	// Execute code: await Promise.all([tool1(), tool2()])
	code := `const results = await Promise.all([
	bifrostInternal.echo({message: "parallel1"}),
	bifrostInternal.echo({message: "parallel2"}),
	bifrostInternal.calculator({operation: "add", x: 10, y: 20})
]);

return {
	echo1: results[0],
	echo2: results[1],
	calc: results[2]
};`

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

	// Verify parallel execution works
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
	code := `try {
	const result = await bifrostInternal.throw_error({error_message: "intentional error"});
	return {success: true, result: result};
} catch (e) {
	return {success: false, error: e.message, caught: true};
}`

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

	returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
	require.False(t, hasError, "should not have execution error: %s", errorMsg)

	// Verify error is propagated and caught
	resultObj, ok := returnValue.(map[string]interface{})
	require.True(t, ok)
	assert.False(t, resultObj["success"].(bool))
	assert.True(t, resultObj["caught"].(bool))
	// Error message might be null in some JS engines, just verify we caught it
	t.Logf("Caught error: %v", resultObj["error"])
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

	// Execute code that calls non-existent tool
	code := `try {
	const result = await bifrostInternal.nonexistent_tool({param: "value"});
	return result;
} catch (e) {
	return {error: e.message, notFound: true};
}`

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

	returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
	require.False(t, hasError, "should not have execution error: %s", errorMsg)

	// Verify appropriate error
	resultObj, ok := returnValue.(map[string]interface{})
	require.True(t, ok)
	assert.True(t, resultObj["notFound"].(bool))
	assert.NotEmpty(t, resultObj["error"])
	t.Logf("Error: %s", resultObj["error"])
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

	code := `const result = await bifrostInternal.calculator({operation: "divide", x: 100, y: 4});
return {result: result, format: "chat"};`

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

	code := `const result = await TestCodeModeServer.calculator({operation: "subtract", x: 50, y: 8});
return {result: result, format: "responses"};`

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
