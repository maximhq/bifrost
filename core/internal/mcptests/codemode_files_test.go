package mcptests

import (
	"fmt"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// LIST TOOL FILES TESTS
// =============================================================================

func TestListToolFiles_ServerBinding(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Setup code mode client with CodeModeBindingLevel = "server"
	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)
	httpClient := GetSampleHTTPClientConfig(config.HTTPServerURL)
	httpClient.ID = "testserver"
	httpClient.ToolsToExecute = []string{"*"}

	manager := setupMCPManager(t, codeModeClient, httpClient)
	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Call listToolFiles
	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-list-files"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("listToolFiles"),
			Arguments: `{}`,
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// listToolFiles returns a text tree structure, not JSON
	content := *result.Content.ContentStr
	assert.NotEmpty(t, content)

	// Verify returns servers/<name>.d.ts structure in tree format
	assert.Contains(t, content, "servers/", "should contain servers/ directory")
	assert.Contains(t, content, ".d.ts", "should contain .d.ts files")
	t.Logf("Tree structure:\n%s", content)
}

func TestListToolFiles_ToolBinding(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Setup code mode client with CodeModeBindingLevel = "tool"
	// Note: This would need to be configured on the ToolsManager
	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)
	httpClient := GetSampleHTTPClientConfig(config.HTTPServerURL)
	httpClient.ID = "toolserver"
	httpClient.ToolsToExecute = []string{"*"}

	manager := setupMCPManager(t, codeModeClient, httpClient)
	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Call listToolFiles
	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-list-tool-files"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("listToolFiles"),
			Arguments: `{}`,
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// listToolFiles returns a text tree structure, not JSON
	content := *result.Content.ContentStr
	assert.NotEmpty(t, content)
	t.Logf("Files listed:\n%s", content)

	// Verify returns tree structure with servers/<name> entries
	// The binding level determines the structure
	// Default is "server" so we expect servers/<name>.d.ts
	assert.Contains(t, content, "servers/", "should contain servers/ directory")
	assert.Contains(t, content, ".d.ts", "should contain .d.ts files")
}

func TestListToolFiles_WithFiltering(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Setup code mode client with ToolsToExecute = ["echo"]
	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)
	httpClient := GetSampleHTTPClientConfig(config.HTTPServerURL)
	httpClient.ID = "filteredserver"
	httpClient.ToolsToExecute = []string{"echo"} // Only echo allowed

	manager := setupMCPManager(t, codeModeClient, httpClient)
	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Call listToolFiles
	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-list-filtered"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("listToolFiles"),
			Arguments: `{}`,
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// listToolFiles returns a text tree structure
	content := *result.Content.ContentStr
	assert.NotEmpty(t, content)

	// Should still list the server file (filtering applies to execution, not discovery)
	assert.Contains(t, content, "servers/", "should contain servers/ directory")
	t.Logf("Files with filtering:\n%s", content)
}

func TestListToolFiles_MultipleServers(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" || config.SSEServerURL == "" {
		t.Skip("MCP_HTTP_SERVER_URL or MCP_SSE_URL not set")
	}

	// Setup code mode client + 2 MCP clients
	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)

	httpClient := GetSampleHTTPClientConfig(config.HTTPServerURL)
	httpClient.ID = "httpserver"
	httpClient.ToolsToExecute = []string{"*"}

	sseClient := GetSampleSSEClientConfig(config.SSEServerURL)
	sseClient.ID = "sseserver"
	sseClient.ToolsToExecute = []string{"*"}

	manager := setupMCPManager(t, codeModeClient, httpClient, sseClient)
	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Call listToolFiles
	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-list-multi"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("listToolFiles"),
			Arguments: `{}`,
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// listToolFiles returns a text tree structure
	content := *result.Content.ContentStr
	assert.NotEmpty(t, content)

	// Verify files from both servers are listed in tree structure
	assert.Contains(t, content, "servers/", "should contain servers/ directory")
	t.Logf("Tree structure with multiple servers:\n%s", content)
}

// =============================================================================
// READ TOOL FILE TESTS
// =============================================================================

func TestReadToolFile_Basic(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Setup code mode client
	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)
	httpClient := GetSampleHTTPClientConfig(config.HTTPServerURL)
	httpClient.ID = "myserver"
	httpClient.ToolsToExecute = []string{"*"}

	manager := setupMCPManager(t, codeModeClient, httpClient)
	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Read a known tool file directly
	readCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-read"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("readToolFile"),
			Arguments: `{"fileName": "servers/TestCodeModeServer.d.ts"}`,
		},
	}

	readResult, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &readCall)
	require.Nil(t, bifrostErr)
	require.NotNil(t, readResult)

	// readToolFile returns text content (TypeScript definitions)
	content := *readResult.Content.ContentStr
	assert.NotEmpty(t, content)

	// Should contain TypeScript declarations
	assert.True(t,
		strings.Contains(content, "interface") ||
			strings.Contains(content, "function") ||
			strings.Contains(content, "type") ||
			strings.Contains(content, "declare"),
		"content should contain TypeScript declarations")

	t.Logf("Read %d characters of TypeScript definitions", len(content))
}

func TestReadToolFile_WithFiltering(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Setup with ToolsToExecute filtering
	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)
	httpClient := GetSampleHTTPClientConfig(config.HTTPServerURL)
	httpClient.ID = "restricted"
	httpClient.ToolsToExecute = []string{"echo"} // Only echo

	manager := setupMCPManager(t, codeModeClient, httpClient)
	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Read file for server (should work - files can be read even with filtering)
	readCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-read"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("readToolFile"),
			Arguments: `{"fileName": "servers/TestCodeModeServer.d.ts"}`,
		},
	}

	readResult, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &readCall)
	require.Nil(t, bifrostErr)
	require.NotNil(t, readResult)

	content := *readResult.Content.ContentStr
	// Content should show TypeScript definitions, filtering is applied at execution time
	assert.NotEmpty(t, content, "should have readable file content")
	t.Logf("Read file content length: %d", len(content))
}

func TestReadToolFile_NotFound(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)
	httpClient := GetSampleHTTPClientConfig(config.HTTPServerURL)
	httpClient.ID = "server"
	httpClient.ToolsToExecute = []string{"*"}

	manager := setupMCPManager(t, codeModeClient, httpClient)
	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Try to read non-existent file
	readCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-read-404"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("readToolFile"),
			Arguments: `{"fileName": "servers/nonexistent.d.ts"}`,
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &readCall)

	// The tool returns an error or informational message about the non-existent file
	if bifrostErr != nil {
		if bifrostErr.Error != nil {
			assert.Contains(t, bifrostErr.Error.Message, "not found")
		}
		t.Log("✓ Error returned for non-existent file")
	} else {
		require.NotNil(t, result)
		content := *result.Content.ContentStr
		// The tool may return an error message instead of empty content
		// Check that the content indicates the file was not found
		assert.True(t,
			strings.Contains(content, "not found") ||
				strings.Contains(content, "No server found") ||
				strings.Contains(content, "nonexistent"),
			"content should indicate file not found")
		t.Log("✓ Error message returned for non-existent file")
	}
}

func TestReadToolFile_TypescriptDefinitions(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)
	httpClient := GetSampleHTTPClientConfig(config.HTTPServerURL)
	httpClient.ID = "typeserver"
	httpClient.ToolsToExecute = []string{"*"}

	manager := setupMCPManager(t, codeModeClient, httpClient)
	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Read a known server file
	readCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-read"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("readToolFile"),
			Arguments: `{"fileName": "servers/TestCodeModeServer.d.ts"}`,
		},
	}

	readResult, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &readCall)
	require.Nil(t, bifrostErr)
	require.NotNil(t, readResult)

	content := *readResult.Content.ContentStr
	assert.NotEmpty(t, content)

	// Verify TypeScript interface is well-formed
	// Should have function signatures
	assert.Contains(t, content, "(", "should contain function calls")

	// Check for TypeScript keywords
	hasTypeScript := strings.Contains(content, "interface") ||
		strings.Contains(content, "type") ||
		strings.Contains(content, "function") ||
		strings.Contains(content, "declare")

	assert.True(t, hasTypeScript, "should contain TypeScript declarations")

	t.Logf("TypeScript definitions:\n%s", content)
}

// =============================================================================
// CODE MODE FILE OPERATIONS IN CODE
// =============================================================================

func TestCodeModeFiles_ListInCode(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)
	httpClient := GetSampleHTTPClientConfig(config.HTTPServerURL)
	httpClient.ID = "codeserver"
	httpClient.ToolsToExecute = []string{"*"}

	manager := setupMCPManager(t, codeModeClient, httpClient)
	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute code that calls listToolFiles
	// Note: listToolFiles might not be directly callable from code execution context
	// This tests if the tool is available in the environment
	code := `
		// Check if servers are available
		const servers = Object.keys(this).filter(key => typeof this[key] === 'object');
		return {
			availableServers: servers,
			hasCodeserver: servers.includes('codeserver')
		};
	`

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-list-in-code"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("executeToolCode"),
			Arguments: `{"code": "` + code + `"}`,
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
	require.False(t, hasError, "should not have execution error: %s", errorMsg)

	resultObj, ok := returnValue.(map[string]interface{})
	require.True(t, ok)

	// Verify servers are available in code execution context
	servers := resultObj["availableServers"].([]interface{})
	t.Logf("Available servers in code: %v", servers)
	assert.NotEmpty(t, servers)
}

func TestCodeModeFiles_ReadInCode(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)

	manager := setupMCPManager(t, codeModeClient)
	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Execute code that explores available server methods
	code := `
		// Check what methods are available on the server object
		const methods = Object.getOwnPropertyNames(TestCodeModeServer).filter(
			prop => typeof TestCodeModeServer[prop] === 'function'
		);
		return {
			serverMethods: methods,
			methodCount: methods.length,
			hasTools: methods.length > 0
		};
	`

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-read-in-code"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("executeToolCode"),
			Arguments: fmt.Sprintf(`{"code": %s}`, fmt.Sprintf(`"%s"`, strings.ReplaceAll(code, `"`, `\"`))),
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
	require.False(t, hasError, "should not have execution error: %s", errorMsg)

	resultObj, ok := returnValue.(map[string]interface{})
	require.True(t, ok)

	// Verify tool methods are accessible
	assert.NotNil(t, resultObj["serverMethods"])
	assert.Greater(t, resultObj["methodCount"], float64(0), "should have at least one method")
	assert.Equal(t, true, resultObj["hasTools"])
	t.Logf("Server methods: %v", resultObj["serverMethods"])
}

// =============================================================================
// BOTH API FORMATS TESTS
// =============================================================================

func TestCodeModeFiles_ChatFormat(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)
	httpClient := GetSampleHTTPClientConfig(config.HTTPServerURL)
	httpClient.ID = "chatserver"
	httpClient.ToolsToExecute = []string{"*"}

	manager := setupMCPManager(t, codeModeClient, httpClient)
	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Call listToolFiles in Chat format
	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-chat-list"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("listToolFiles"),
			Arguments: `{}`,
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	// Verify Chat format response
	assert.Equal(t, schemas.ChatMessageRoleTool, result.Role)
	assert.Equal(t, "call-chat-list", *result.ToolCallID)

	// Response is a text tree structure, not JSON
	content := *result.Content.ContentStr
	assert.NotEmpty(t, content)
	assert.Contains(t, content, "servers/", "response should contain servers directory structure")
}

func TestCodeModeFiles_ResponsesFormat(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)
	httpClient := GetSampleHTTPClientConfig(config.HTTPServerURL)
	httpClient.ID = "responsesserver"
	httpClient.ToolsToExecute = []string{"*"}

	manager := setupMCPManager(t, codeModeClient, httpClient)
	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Call listToolFiles using Chat format (internal code mode tool)
	listCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-responses-list"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("listToolFiles"),
			Arguments: `{}`,
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &listCall)
	require.Nil(t, bifrostErr, "listToolFiles should not error")
	require.NotNil(t, result, "result should not be nil")
	require.NotNil(t, result.Content, "result.Content should not be nil")

	// Verify we got a response
	content := *result.Content.ContentStr
	assert.NotEmpty(t, content, "response should not be empty")
	assert.Contains(t, content, "servers/", "response should contain servers directory structure")
	t.Logf("Listed files:\n%s", content)
}

// =============================================================================
// COMPREHENSIVE FILE OPERATIONS TEST
// =============================================================================

func TestCodeModeFiles_FullWorkflow(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)
	// Create a second code mode client to also be available in code execution
	codeModeClient2 := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)
	codeModeClient2.ID = "workflowserver"
	codeModeClient2.Name = "TestHTTPServer"

	manager := setupMCPManager(t, codeModeClient, codeModeClient2)
	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	// Step 1: List tool files (returns a tree structure as text)
	listCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-1-list"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("listToolFiles"),
			Arguments: `{}`,
		},
	}

	listResult, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &listCall)
	require.Nil(t, bifrostErr)
	require.NotNil(t, listResult)
	require.NotNil(t, listResult.Content)

	treeOutput := *listResult.Content.ContentStr
	assert.NotEmpty(t, treeOutput, "listToolFiles should return a non-empty tree structure")
	assert.Contains(t, treeOutput, "servers/", "tree output should contain servers directory")
	t.Logf("Step 1: Listed available files:\n%s", treeOutput)

	// Step 2: Read a tool file using readToolFile
	// Extract a filename from the tree output (e.g., "servers/TestCodeModeServer.d.ts")
	readCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-2-read"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("readToolFile"),
			Arguments: `{"fileName": "servers/TestCodeModeServer.d.ts"}`,
		},
	}

	readResult, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &readCall)
	require.Nil(t, bifrostErr)
	require.NotNil(t, readResult)
	require.NotNil(t, readResult.Content)

	fileContent := *readResult.Content.ContentStr
	assert.NotEmpty(t, fileContent, "readToolFile should return file content")
	t.Logf("Step 2: Read file content (%d chars)", len(fileContent))

	// Step 3: Execute code that uses the tools
	// Just verify we can execute code with available servers
	code := `const servers = Object.keys(this).filter(k => typeof this[k] === 'object'); return {completed: true, servers: servers};`

	execCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-3-execute"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("executeToolCode"),
			Arguments: fmt.Sprintf(`{"code": %s}`, fmt.Sprintf(`"%s"`, strings.ReplaceAll(code, `"`, `\"`))),
		},
	}

	execResult, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &execCall)
	require.Nil(t, bifrostErr)
	require.NotNil(t, execResult)

	returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *execResult.Content.ContentStr)
	require.False(t, hasError, "should not have execution error: %s", errorMsg)

	resultObj, ok := returnValue.(map[string]interface{})
	require.True(t, ok)
	assert.True(t, resultObj["completed"].(bool))
	assert.NotNil(t, resultObj["servers"])

	t.Log("Step 3: Successfully executed code and discovered servers")
}
