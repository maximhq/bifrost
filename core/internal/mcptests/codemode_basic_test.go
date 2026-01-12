package mcptests

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// PHASE 3.1: MULTI-SERVER CODEBLOCK EXECUTION
// =============================================================================

// TestCodeMode_MultiServer_BasicCalls tests calling tools from multiple servers in one code block
func TestCodeMode_MultiServer_BasicCalls(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Initialize global MCP server paths
	InitMCPServerPaths(t)

	bifrostRoot := GetBifrostRoot(t)

	// Setup code mode client
	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)

	// Setup multiple servers - all with CodeMode enabled
	temperatureClient := GetTemperatureMCPClientConfig(bifrostRoot)
	temperatureClient.IsCodeModeClient = true
	temperatureClient.ToolsToExecute = []string{"*"}

	goTestClient := GetGoTestServerConfig(bifrostRoot)
	goTestClient.ToolsToExecute = []string{"*"}

	manager := setupMCPManager(t, codeModeClient, temperatureClient, goTestClient)

	// Register InProcess echo tool (this creates the InProcess client)
	err := RegisterEchoTool(manager)
	require.NoError(t, err)

	// Now set the InProcess client as CodeMode-enabled - manual approach
	clients := manager.GetClients()
	for _, client := range clients {
		if client.ExecutionConfig.ID == "bifrostInternal" {
			config := client.ExecutionConfig
			config.IsCodeModeClient = true
			config.ToolsToExecute = []string{"*"}
			err = manager.EditClient(config.ID, config)
			require.NoError(t, err)
			t.Logf("Updated InProcess client to CodeMode: ID=%s, Name=%s", config.ID, config.Name)
			break
		}
	}

	// Verify the InProcess client is now a CodeMode client
	clients = manager.GetClients()
	var foundCodeModeClient bool
	for _, client := range clients {
		if client.ExecutionConfig.ID == "bifrostInternal" {
			foundCodeModeClient = client.ExecutionConfig.IsCodeModeClient
			t.Logf("After edit - InProcess client IsCodeModeClient: %v, Name: %s", client.ExecutionConfig.IsCodeModeClient, client.ExecutionConfig.Name)
			break
		}
	}
	require.True(t, foundCodeModeClient, "InProcess client should be a CodeMode client")

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute code calling tools from 3 different servers
	code := `
const temp = await TemperatureMCPServer.get_temperature({location: "Tokyo"});
const uuid = await GoTestServer.uuid_generate({});
const echo = await bifrostInternal.echo({message: "multi-server"});

return {
    temperature: temp,
    uuid: uuid,
    echo: echo,
    servers_used: 3
};
`

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-multiserver"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("executeToolCode"),
			Arguments: fmt.Sprintf(`{"code": %s}`, mustJSONString(code)),
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	require.Nil(t, bifrostErr, "should execute without bifrost error")
	require.NotNil(t, result, "should return result")

	// Parse the code mode response
	returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
	require.False(t, hasError, "should not have execution error: %s", errorMsg)
	require.NotNil(t, returnValue, "should have return value")

	returnObj, ok := returnValue.(map[string]interface{})
	require.True(t, ok, "result should be an object")

	// Assertions
	assert.NotNil(t, returnObj["temperature"], "should have temperature from TemperatureMCPServer")
	assert.NotNil(t, returnObj["uuid"], "should have uuid from GoTestServer")
	assert.NotNil(t, returnObj["echo"], "should have echo from InProcess")
	assert.Equal(t, float64(3), returnObj["servers_used"], "should use 3 servers")
}

// TestCodeMode_MultiServer_ParallelExecution tests parallel execution across multiple servers
func TestCodeMode_MultiServer_ParallelExecution(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Initialize global MCP server paths
	InitMCPServerPaths(t)

	bifrostRoot := GetBifrostRoot(t)

	// Setup servers
	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)

	temperatureClient := GetTemperatureMCPClientConfig(bifrostRoot)
	temperatureClient.IsCodeModeClient = true
	temperatureClient.ToolsToExecute = []string{"*"}

	parallelClient := GetParallelTestServerConfig(bifrostRoot)
	parallelClient.ToolsToExecute = []string{"*"}

	edgeClient := GetEdgeCaseServerConfig(bifrostRoot)
	edgeClient.ToolsToExecute = []string{"*"}

	manager := setupMCPManager(t, codeModeClient, temperatureClient, parallelClient, edgeClient)
	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute parallel calls - should complete in ~1s not ~2s
	code := `
const start = Date.now();

const results = await Promise.all([
    TemperatureMCPServer.delay({seconds: 1}),
    ParallelTestServer.medium_operation({}),
    ParallelTestServer.fast_operation({}),
    EdgeCaseServer.return_unicode({type: "emoji"})
]);

const duration = Date.now() - start;

return {
    results: results,
    duration_ms: duration,
    executed_parallel: duration < 1500
};
`

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-parallel"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("executeToolCode"),
			Arguments: fmt.Sprintf(`{"code": %s}`, mustJSONString(code)),
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// Parse the code mode response
	returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
	require.False(t, hasError, "should not have execution error: %s", errorMsg)
	require.NotNil(t, returnValue, "should have return value")

	returnObj, ok := returnValue.(map[string]interface{})
	require.True(t, ok, "result should be an object")

	// Assertions - verify parallel execution
	assert.True(t, returnObj["executed_parallel"].(bool), "should execute in parallel (< 1500ms)")
	assert.Less(t, returnObj["duration_ms"].(float64), 1500.0, "duration should be < 1500ms")

	results, hasResults := returnObj["results"]
	assert.True(t, hasResults, "should have results array")

	resultsArray, ok := results.([]interface{})
	require.True(t, ok, "results should be array")
	assert.Len(t, resultsArray, 4, "should have 4 results")
}

// TestCodeMode_MultiServer_SequentialChaining tests sequential chaining of tool calls
func TestCodeMode_MultiServer_SequentialChaining(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Initialize global MCP server paths
	InitMCPServerPaths(t)

	bifrostRoot := GetBifrostRoot(t)

	// Setup servers
	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)

	temperatureClient := GetTemperatureMCPClientConfig(bifrostRoot)
	temperatureClient.IsCodeModeClient = true
	temperatureClient.ToolsToExecute = []string{"*"}

	goTestClient := GetGoTestServerConfig(bifrostRoot)
	goTestClient.ToolsToExecute = []string{"*"}

	manager := setupMCPManager(t, codeModeClient, temperatureClient, goTestClient)
	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute sequential chain of calls
	code := `
// Call 1: Get temperature
const temp = await TemperatureMCPServer.get_temperature({location: "London"});

// Call 2: Transform the response to uppercase
const transformed = await GoTestServer.string_transform({
    input: JSON.stringify(temp),
    operation: "uppercase"
});

// Call 3: Hash the result
const hashed = await GoTestServer.hash({
    input: transformed,
    algorithm: "sha256"
});

return {
    original: temp,
    transformed: transformed,
    hashed: hashed,
    chain_length: 3
};
`

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-chain"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("executeToolCode"),
			Arguments: fmt.Sprintf(`{"code": %s}`, mustJSONString(code)),
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// Parse result
	returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
	require.False(t, hasError, "should not have execution error: %s", errorMsg)

	returnObj, ok := returnValue.(map[string]interface{})
	require.True(t, ok)

	// Assertions - verify chain worked
	assert.NotEmpty(t, returnObj["original"], "should have original temperature")
	assert.NotEmpty(t, returnObj["transformed"], "should have transformed string")
	assert.NotEmpty(t, returnObj["hashed"], "should have hash")
	assert.Equal(t, float64(3), returnObj["chain_length"], "chain should be 3 calls long")

	// Verify the transformed contains uppercase content
	// string_transform returns an object with input, operation, result fields
	transformedVal := returnObj["transformed"]
	if transformedObj, ok := transformedVal.(map[string]interface{}); ok {
		// It's an object response from the tool
		assert.NotNil(t, transformedObj["result"], "transformed object should have result field")
		result := transformedObj["result"]
		assert.NotEmpty(t, result, "transformed result should not be empty")
	} else if transformedStr, ok := transformedVal.(string); ok {
		// It's a string response
		assert.NotEmpty(t, transformedStr, "transformed string should not be empty")
	} else {
		t.Fatalf("transformed should be either object or string, got %T", transformedVal)
	}
}

// =============================================================================
// PHASE 3.2: TOOL FILTERING SCENARIOS (NON-AGENT)
// =============================================================================

// TestCodeMode_Filtering_ServerAllowed_ToolBlocked tests that blocked tools cannot be called
func TestCodeMode_Filtering_ServerAllowed_ToolBlocked(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Initialize global MCP server paths
	InitMCPServerPaths(t)

	bifrostRoot := GetBifrostRoot(t)

	// Setup code mode client
	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)

	// Setup temperature client with filtering - only allow get_temperature and calculator, NOT echo
	temperatureClient := GetTemperatureMCPClientConfig(bifrostRoot)
	temperatureClient.ID = "temp-filtered"
	temperatureClient.IsCodeModeClient = true
	temperatureClient.ToolsToExecute = []string{"get_temperature", "calculator"} // NOT echo

	manager := setupMCPManager(t, codeModeClient, temperatureClient)
	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Try to call echo - should fail
	code := `
try {
    const result = await TemperatureMCPServer.echo({text: "should fail"});
    return {success: true, unexpected: result};
} catch (e) {
    return {success: false, error: e.message, expected: true};
}
`

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-blocked"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("executeToolCode"),
			Arguments: fmt.Sprintf(`{"code": %s}`, mustJSONString(code)),
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// Parse result
	returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
	require.False(t, hasError, "should not have execution error: %s", errorMsg)

	returnObj, ok := returnValue.(map[string]interface{})
	require.True(t, ok)

	// Assertions - echo should have failed
	assert.False(t, returnObj["success"].(bool), "echo call should fail")
	assert.True(t, returnObj["expected"].(bool), "error was expected")
	// The error message is "Object has no member 'echo'" because filtered tools are not bound to the JS object
	errorStr := returnObj["error"].(string)
	// Check that it's either a "not allowed" message or "no member" message (both indicate filtering worked)
	isFilteredError := strings.Contains(errorStr, "not allowed") || strings.Contains(errorStr, "no member")
	assert.True(t, isFilteredError, "error should indicate tool is filtered: %s", errorStr)
}

// TestCodeMode_Filtering_ContextOverride_AllowTool - REMOVED
// Context filtering can only NARROW client configuration, not override it.
// If client has ToolsToExecute = [], context cannot expand that.

// TestCodeMode_Filtering_MultiServer_MixedFiltering tests mixed filtering across multiple servers
func TestCodeMode_Filtering_MultiServer_MixedFiltering(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Initialize global MCP server paths
	InitMCPServerPaths(t)

	bifrostRoot := GetBifrostRoot(t)

	// Setup code mode client
	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)

	// Setup temperature with partial filtering - only get_temperature
	temperatureClient := GetTemperatureMCPClientConfig(bifrostRoot)
	temperatureClient.ID = "temp-partial"
	temperatureClient.IsCodeModeClient = true
	temperatureClient.ToolsToExecute = []string{"get_temperature"} // Only 1 tool

	// Setup go-test with all tools allowed
	goTestClient := GetGoTestServerConfig(bifrostRoot)
	goTestClient.ToolsToExecute = []string{"*"} // All tools

	// Setup InProcess with no tools allowed
	manager := setupMCPManager(t, codeModeClient, temperatureClient, goTestClient)
	err := RegisterEchoTool(manager)
	require.NoError(t, err)

	// Set InProcess client with no tools
	err = SetInternalClientAsCodeMode(manager, []string{}) // No tools
	require.NoError(t, err)

	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Try each tool
	code := `
const results = {
    allowed_temp: null,
    blocked_echo: null,
    allowed_uuid: null,
    blocked_inprocess: null
};

// Should succeed - get_temperature is allowed
try {
    results.allowed_temp = await TemperatureMCPServer.get_temperature({location: "Tokyo"});
} catch (e) {
    results.allowed_temp = {error: e.message};
}

// Should fail - echo is not in ToolsToExecute
try {
    results.blocked_echo = await TemperatureMCPServer.echo({text: "test"});
} catch (e) {
    results.blocked_echo = {error: e.message};
}

// Should succeed - all GoTestServer tools allowed
try {
    results.allowed_uuid = await GoTestServer.uuid_generate({});
} catch (e) {
    results.allowed_uuid = {error: e.message};
}

// Should fail - InProcess has no tools allowed
try {
    results.blocked_inprocess = await bifrostInternal.echo({message: "test"});
} catch (e) {
    results.blocked_inprocess = {error: e.message};
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

	// Parse result
	returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
	require.False(t, hasError, "should not have execution error: %s", errorMsg)

	returnObj, ok := returnValue.(map[string]interface{})
	require.True(t, ok)

	// Assertions - verify filtering behavior
	// allowed_temp: Should succeed (is a string or has no error field)
	allowedTemp := returnObj["allowed_temp"]
	assert.NotNil(t, allowedTemp, "allowed_temp should exist")
	// Check if it's an error object
	if tempObj, ok := allowedTemp.(map[string]interface{}); ok {
		_, hasError := tempObj["error"]
		assert.False(t, hasError, "allowed_temp should not have error")
	} else {
		// It's a string response - that's fine, means it succeeded
		assert.True(t, true, "allowed_temp returned string response (success)")
	}

	// blocked_echo: Should fail
	blockedEcho, ok := returnObj["blocked_echo"].(map[string]interface{})
	assert.True(t, ok, "blocked_echo should be object")
	_, hasError = blockedEcho["error"]
	assert.True(t, hasError, "blocked_echo should have error")

	// allowed_uuid: Should succeed
	allowedUUID, ok := returnObj["allowed_uuid"]
	assert.True(t, ok, "allowed_uuid should exist")
	assert.NotNil(t, allowedUUID, "allowed_uuid should not be nil")

	// blocked_inprocess: Should fail
	blockedInprocess, ok := returnObj["blocked_inprocess"].(map[string]interface{})
	assert.True(t, ok, "blocked_inprocess should be object")
	_, hasError = blockedInprocess["error"]
	assert.True(t, hasError, "blocked_inprocess should have error")
}

// TestCodeMode_Filtering_ClientFiltering tests client-level filtering
func TestCodeMode_Filtering_ClientFiltering(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Initialize global MCP server paths
	InitMCPServerPaths(t)

	bifrostRoot := GetBifrostRoot(t)

	// Setup code mode client
	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)

	// Setup both servers
	temperatureClient := GetTemperatureMCPClientConfig(bifrostRoot)
	temperatureClient.IsCodeModeClient = true
	temperatureClient.ToolsToExecute = []string{"*"}

	goTestClient := GetGoTestServerConfig(bifrostRoot)
	goTestClient.ToolsToExecute = []string{"*"}

	manager := setupMCPManager(t, codeModeClient, temperatureClient, goTestClient)
	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	// Create context that only allows "TemperatureMCPServer" client (filtering by Name, matching the client's Name field)
	ctx := CreateTestContextWithMCPFilter([]string{temperatureClient.Name}, nil)

	// Try to call both servers
	code := `
const results = {};

try {
    results.temp = await TemperatureMCPServer.get_temperature({location: "Dubai"});
} catch (e) {
    results.temp = {error: e.message};
}

try {
    results.gotest = await GoTestServer.uuid_generate({});
} catch (e) {
    results.gotest = {error: e.message};
}

return results;
`

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-clientfilter"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("executeToolCode"),
			Arguments: fmt.Sprintf(`{"code": %s}`, mustJSONString(code)),
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// Parse result
	returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
	require.False(t, hasError, "should not have execution error: %s", errorMsg)

	returnObj, ok := returnValue.(map[string]interface{})
	require.True(t, ok)

	// Assertions - temperature should succeed, gotest should fail
	// temp: Should succeed (client allowed)
	tempVal := returnObj["temp"]
	if tempObj, ok := tempVal.(map[string]interface{}); ok {
		// Object response - check for error
		_, hasErrorField := tempObj["error"]
		assert.False(t, hasErrorField, "temp should not have error (client is allowed)")
	} else {
		// String response from get_temperature - that's fine, means it succeeded
		assert.NotNil(t, tempVal, "temp should have a value (client is allowed)")
		assert.IsType(t, "", tempVal, "temp should be a string response from get_temperature")
	}

	// gotest: Should fail (client filtered out)
	gotestVal := returnObj["gotest"]
	gotestObj, ok := gotestVal.(map[string]interface{})
	assert.True(t, ok, "gotest should be an object with error")
	_, hasError = gotestObj["error"]
	assert.True(t, hasError, "gotest should have error (client is filtered out)")
}

// =============================================================================
// HELPER FUNCTIONS
// =============================================================================

// mustJSONString converts a string to JSON-escaped string for embedding in JSON
func mustJSONString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}
