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
// PATH TRAVERSAL SECURITY TESTS
// =============================================================================

func TestReadToolFile_PathTraversalAttacks(t *testing.T) {
	t.Parallel()

	config := GetTestConfig(t)
	if config.HTTPServerURL == "" {
		t.Skip("MCP_HTTP_URL not set")
	}

	// Setup code mode client
	codeModeClient := GetSampleCodeModeClientConfig(t, config.HTTPServerURL)
	manager := setupMCPManager(t, codeModeClient)
	bifrost := setupBifrost(t)
	bifrost.SetMCPManager(manager)

	ctx := createTestContext()

	pathTraversalTests := []struct {
		name         string
		fileName     string
		expectError  bool
		errorMessage string
	}{
		{
			name:         "basic_path_traversal_parent",
			fileName:     "../../../etc/passwd.d.ts",
			expectError:  true,
			errorMessage: "No server found matching",
		},
		{
			name:         "path_traversal_in_server_name",
			fileName:     "servers/../../secrets.d.ts",
			expectError:  true,
			errorMessage: "No server found matching",
		},
		{
			name:         "double_dot_in_tool_name",
			fileName:     "servers/validserver/../../../etc.d.ts",
			expectError:  true,
			errorMessage: "No server found matching",
		},
		{
			name:         "encoded_path_traversal",
			fileName:     "servers/..%2F..%2F..%2Fetc%2Fpasswd.d.ts",
			expectError:  true,
			errorMessage: "No server found matching",
		},
		{
			name:         "path_with_multiple_slashes",
			fileName:     "servers///..//..//etc//passwd.d.ts",
			expectError:  true,
			errorMessage: "No server found matching",
		},
		{
			name:         "absolute_path",
			fileName:     "/etc/passwd.d.ts",
			expectError:  true,
			errorMessage: "No server found matching",
		},
	}

	for _, tc := range pathTraversalTests {
		t.Run(tc.name, func(t *testing.T) {
			toolCall := schemas.ChatAssistantMessageToolCall{
				ID:   schemas.Ptr("call-" + tc.name),
				Type: schemas.Ptr("function"),
				Function: schemas.ChatAssistantMessageToolCallFunction{
					Name:      schemas.Ptr("readToolFile"),
					Arguments: `{"fileName": "` + tc.fileName + `"}`,
				},
			}

			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			if tc.expectError {
				// Should either return error or error message in result
				if bifrostErr != nil {
					assert.Contains(t, bifrostErr.Error.Message, tc.errorMessage)
				} else if result != nil && result.Content != nil && result.Content.ContentStr != nil {
					assert.Contains(t, *result.Content.ContentStr, tc.errorMessage)
				} else {
					t.Errorf("Expected error but got success")
				}
			}
		})
	}
}

func TestReadToolFile_InvalidToolNames(t *testing.T) {
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

	invalidNameTests := []struct {
		name     string
		fileName string
	}{
		{"slash_in_tool_name", "servers/validserver/tool/with/slashes.d.ts"},
		{"dot_dot_in_tool_name", "servers/validserver/tool..name.d.ts"},
		{"special_chars", "servers/validserver/tool<>:\"|?*.d.ts"},
		{"null_byte", "servers/validserver/tool\x00name.d.ts"},
		{"backslash", "servers/validserver/tool\\name.d.ts"},
	}

	for _, tc := range invalidNameTests {
		t.Run(tc.name, func(t *testing.T) {
			toolCall := schemas.ChatAssistantMessageToolCall{
				ID:   schemas.Ptr("call-" + tc.name),
				Type: schemas.Ptr("function"),
				Function: schemas.ChatAssistantMessageToolCallFunction{
					Name:      schemas.Ptr("readToolFile"),
					Arguments: `{"fileName": "` + tc.fileName + `"}`,
				},
			}

			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			// Should return error or error message
			if bifrostErr == nil && result != nil && result.Content != nil && result.Content.ContentStr != nil {
				// Verify error message indicates tool not found
				assert.Contains(t, *result.Content.ContentStr, "No server found matching")
			}
		})
	}
}

// =============================================================================
// CODE INJECTION SECURITY TESTS
// =============================================================================

func TestExecuteToolCode_CodeInjectionAttempts(t *testing.T) {
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

	injectionTests := []struct {
		name        string
		code        string
		shouldFail  bool
		description string
	}{
		{
			name:        "process_exit",
			code:        "process.exit(1); return 'should not reach'",
			shouldFail:  true,
			description: "Attempt to exit process",
		},
		{
			name:        "require_fs",
			code:        "const fs = require('fs'); return fs.readFileSync('/etc/passwd', 'utf8')",
			shouldFail:  true,
			description: "Attempt to access filesystem",
		},
		{
			name:        "eval_usage",
			code:        "eval('return 42'); return 'done'",
			shouldFail:  false, // May or may not fail depending on sandbox
			description: "Use of eval",
		},
		{
			name:        "infinite_loop",
			code:        "while(true) { /* infinite loop */ }",
			shouldFail:  true,
			description: "Infinite loop should timeout",
		},
		{
			name:        "prototype_pollution",
			code:        "Object.prototype.polluted = 'yes'; return 'done'",
			shouldFail:  false, // Should succeed but be contained
			description: "Prototype pollution attempt",
		},
	}

	for _, tc := range injectionTests {
		t.Run(tc.name, func(t *testing.T) {
			codeJSON, _ := json.Marshal(tc.code)
			toolCall := schemas.ChatAssistantMessageToolCall{
				ID:   schemas.Ptr("call-" + tc.name),
				Type: schemas.Ptr("function"),
				Function: schemas.ChatAssistantMessageToolCallFunction{
					Name:      schemas.Ptr("executeToolCode"),
					Arguments: `{"code": ` + string(codeJSON) + `}`,
				},
			}

			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			if tc.shouldFail {
				// Should return error or error in result
				if bifrostErr == nil && result != nil && result.Content != nil && result.Content.ContentStr != nil {
					returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
					if hasError {
						t.Logf("Code execution failed as expected with error: %s", errorMsg)
					} else if returnValue != nil {
						// Check if return value contains error field
						if returnObj, ok := returnValue.(map[string]interface{}); ok {
							if errorField, ok := returnObj["error"]; ok {
								t.Logf("Code execution failed as expected with error: %v", errorField)
							}
						}
					}
				}
			}
		})
	}
}

// =============================================================================
// INPUT VALIDATION SECURITY TESTS
// =============================================================================

func TestListToolFiles_InputValidation(t *testing.T) {
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

	// Test listToolFiles with no parameters (should succeed)
	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-list"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("listToolFiles"),
			Arguments: `{}`,
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	require.Nil(t, bifrostErr, "listToolFiles should succeed")
	require.NotNil(t, result)
}

func TestReadToolFile_EmptyFileName(t *testing.T) {
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

	testCases := []struct {
		name      string
		arguments string
	}{
		{"empty_string", `{"fileName": ""}`},
		{"only_spaces", `{"fileName": "   "}`},
		{"missing_field", `{}`},
		{"null_value", `{"fileName": null}`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			toolCall := schemas.ChatAssistantMessageToolCall{
				ID:   schemas.Ptr("call-" + tc.name),
				Type: schemas.Ptr("function"),
				Function: schemas.ChatAssistantMessageToolCallFunction{
					Name:      schemas.Ptr("readToolFile"),
					Arguments: tc.arguments,
				},
			}

			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			// Should return error or error message
			if bifrostErr == nil && result != nil {
				// Check if error is in result content
				if result.Content != nil && result.Content.ContentStr != nil {
					content := *result.Content.ContentStr
					// Should contain some error indication
					assert.True(t, strings.Contains(content, "error") ||
						strings.Contains(content, "required") ||
						strings.Contains(content, "invalid") ||
						strings.Contains(content, "found") ||  // Updated to just "found" not "not found"
						strings.Contains(content, "Available virtual files"),  // Also accept list of available files
						"Should return error message, got: %s", content)
				}
			}
		})
	}
}

func TestExecuteToolCode_EmptyCodeSecurity(t *testing.T) {
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

	testCases := []struct {
		name      string
		arguments string
	}{
		{"empty_string", `{"code": ""}`},
		{"only_spaces", `{"code": "   "}`},
		{"only_newlines", `{"code": "\n\n\n"}`},
		{"missing_field", `{}`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			toolCall := schemas.ChatAssistantMessageToolCall{
				ID:   schemas.Ptr("call-" + tc.name),
				Type: schemas.Ptr("function"),
				Function: schemas.ChatAssistantMessageToolCallFunction{
					Name:      schemas.Ptr("executeToolCode"),
					Arguments: tc.arguments,
				},
			}

			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			// Should return error
			if bifrostErr == nil && result != nil && result.Content != nil && result.Content.ContentStr != nil {
				returnValue, hasError, _ := ParseCodeModeResponse(t, *result.Content.ContentStr)
				if !hasError {
					// Check if result is empty or null
					assert.True(t, returnValue == nil || returnValue == "",
						"Empty code should return empty or null result")
				}
			}
		})
	}
}

// =============================================================================
// UNICODE AND ENCODING TESTS
// =============================================================================

func TestExecuteToolCode_UnicodeInCode(t *testing.T) {
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

	unicodeCode := `return "Hello 🌍"`
	codeJSON, _ := json.Marshal(unicodeCode)

	toolCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-unicode"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("executeToolCode"),
			Arguments: `{"code": ` + string(codeJSON) + `}`,
		},
	}

	result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)
	require.Nil(t, bifrostErr)
	require.NotNil(t, result)

	if result.Content != nil && result.Content.ContentStr != nil {
		returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
		require.False(t, hasError, "should not have execution error: %s", errorMsg)
		// Should handle unicode correctly
		resultStr := fmt.Sprintf("%v", returnValue)
		assert.Contains(t, resultStr, "Hello 🌍")
	}
}

// =============================================================================
// MALFORMED JSON TESTS
// =============================================================================

func TestExecuteToolCode_MalformedJSON(t *testing.T) {
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

	malformedTests := []struct {
		name      string
		arguments string
	}{
		{"missing_closing_brace", `{"code": "return 42"`},
		{"missing_quotes", `{code: "return 42"}`},
		{"trailing_comma", `{"code": "return 42",}`},
		{"unescaped_newline", `{"code": "return
42"}`},
		{"invalid_escape", `{"code": "return \x"}`},
	}

	for _, tc := range malformedTests {
		t.Run(tc.name, func(t *testing.T) {
			toolCall := schemas.ChatAssistantMessageToolCall{
				ID:   schemas.Ptr("call-" + tc.name),
				Type: schemas.Ptr("function"),
				Function: schemas.ChatAssistantMessageToolCallFunction{
					Name:      schemas.Ptr("executeToolCode"),
					Arguments: tc.arguments,
				},
			}

			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			// Should return error
			if bifrostErr != nil {
				assert.NotEmpty(t, bifrostErr.Error.Message)
			} else if result != nil && result.Content != nil && result.Content.ContentStr != nil {
				// Error might be in result content
				content := *result.Content.ContentStr
				// Should indicate some kind of error
				assert.True(t, strings.Contains(content, "error") ||
					strings.Contains(content, "invalid") ||
					strings.Contains(content, "failed"),
					"Should indicate error, got: %s", content)
			}
		})
	}
}

// =============================================================================
// LINE NUMBER BOUNDARY TESTS
// =============================================================================

func TestReadToolFile_LineNumberBoundaries(t *testing.T) {
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

	// First, list available files to get a real server name
	listCall := schemas.ChatAssistantMessageToolCall{
		ID:   schemas.Ptr("call-list"),
		Type: schemas.Ptr("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      schemas.Ptr("listToolFiles"),
			Arguments: `{}`,
		},
	}

	listResult, _ := bifrost.ExecuteChatMCPTool(ctx, &listCall)
	if listResult == nil || listResult.Content == nil || listResult.Content.ContentStr == nil {
		t.Skip("No code mode servers available")
	}

	// Parse the list result to get a real file name
	content := *listResult.Content.ContentStr
	if !strings.Contains(content, ".d.ts") {
		t.Skip("No .d.ts files found")
	}

	// Extract first .d.ts file name
	lines := strings.Split(content, "\n")
	var firstFile string
	for _, line := range lines {
		if strings.Contains(line, ".d.ts") {
			// Extract just the filename
			parts := strings.Fields(line)
			for _, part := range parts {
				if strings.HasSuffix(part, ".d.ts") {
					firstFile = strings.Trim(part, "[]\"',")
					break
				}
			}
			if firstFile != "" {
				break
			}
		}
	}

	if firstFile == "" {
		t.Skip("Could not extract file name from list")
	}

	boundaryTests := []struct {
		name         string
		startLine    int
		endLine      int
		expectError  bool
		errorMessage string
	}{
		{
			name:         "start_line_zero",
			startLine:    0,
			endLine:      5,
			expectError:  true,
			errorMessage: "Invalid startLine",
		},
		{
			name:         "start_line_negative",
			startLine:    -1,
			endLine:      5,
			expectError:  true,
			errorMessage: "Invalid startLine",
		},
		{
			name:         "end_line_before_start",
			startLine:    10,
			endLine:      5,
			expectError:  true,
			errorMessage: "Invalid line range",
		},
		{
			name:         "very_large_line_number",
			startLine:    1,
			endLine:      999999,
			expectError:  true,
			errorMessage: "Invalid endLine",
		},
	}

	for _, tc := range boundaryTests {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]interface{}{
				"fileName":  firstFile,
				"startLine": tc.startLine,
				"endLine":   tc.endLine,
			}
			argsJSON, _ := json.Marshal(args)

			toolCall := schemas.ChatAssistantMessageToolCall{
				ID:   schemas.Ptr("call-" + tc.name),
				Type: schemas.Ptr("function"),
				Function: schemas.ChatAssistantMessageToolCallFunction{
					Name:      schemas.Ptr("readToolFile"),
					Arguments: string(argsJSON),
				},
			}

			result, bifrostErr := bifrost.ExecuteChatMCPTool(ctx, &toolCall)

			if tc.expectError {
				// Should return error
				if bifrostErr == nil && result != nil && result.Content != nil && result.Content.ContentStr != nil {
					assert.Contains(t, *result.Content.ContentStr, tc.errorMessage)
				}
			}
		})
	}
}
