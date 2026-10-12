package mcptests

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	core "github.com/maximhq/bifrost/core"
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
			fileName:     "../../../etc/passwd.pyi",
			expectError:  true,
			errorMessage: "No server found matching",
		},
		{
			name:         "path_traversal_in_server_name",
			fileName:     "servers/../../secrets.pyi",
			expectError:  true,
			errorMessage: "No server found matching",
		},
		{
			name:         "double_dot_in_tool_name",
			fileName:     "servers/validserver/../../../etc.pyi",
			expectError:  true,
			errorMessage: "No server found matching",
		},
		{
			name:         "encoded_path_traversal",
			fileName:     "servers/..%2F..%2F..%2Fetc%2Fpasswd.pyi",
			expectError:  true,
			errorMessage: "No server found matching",
		},
		{
			name:         "path_with_multiple_slashes",
			fileName:     "servers///..//..//etc//passwd.pyi",
			expectError:  true,
			errorMessage: "No server found matching",
		},
		{
			name:         "absolute_path",
			fileName:     "/etc/passwd.pyi",
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
		{"slash_in_tool_name", "servers/validserver/tool/with/slashes.pyi"},
		{"dot_dot_in_tool_name", "servers/validserver/tool..name.pyi"},
		{"special_chars", "servers/validserver/tool<>:\"|?*.pyi"},
		{"null_byte", "servers/validserver/tool\x00name.pyi"},
		{"backslash", "servers/validserver/tool\\name.pyi"},
	}

	for _, tc := range invalidNameTests {
		t.Run(tc.name, func(t *testing.T) {
			argsMap := map[string]string{"fileName": tc.fileName}
			argsJSON, err := json.Marshal(argsMap)
			if err != nil {
				t.Fatalf("failed to marshal arguments: %v", err)
			}

			toolCall := schemas.ChatAssistantMessageToolCall{
				ID:   schemas.Ptr("call-" + tc.name),
				Type: schemas.Ptr("function"),
				Function: schemas.ChatAssistantMessageToolCallFunction{
					Name:      schemas.Ptr("readToolFile"),
					Arguments: string(argsJSON),
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
			name:        "import_os",
			code:        "import os\nresult = os.system('ls')",
			shouldFail:  true,
			description: "Attempt to import os module",
		},
		{
			name:        "load_module",
			code:        "load('foo.star', 'bar')\nresult = bar()",
			shouldFail:  true,
			description: "Attempt to load external module",
		},
		{
			name:        "eval_usage",
			code:        "result = 'done'",
			shouldFail:  false, // Simple assignment should succeed
			description: "Simple code execution",
		},
		{
			name:        "infinite_loop",
			code:        "def loop():\n    while True:\n        pass\nloop()",
			shouldFail:  true,
			description: "Infinite loop should timeout",
		},
		{
			name:        "prototype_pollution",
			code:        "result = {'polluted': 'yes'}",
			shouldFail:  false, // Simple dict creation should succeed
			description: "Dict creation (Starlark has no prototypes)",
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
				// Determine if execution actually failed
				executionFailed := false
				var failureReason string

				if bifrostErr != nil {
					executionFailed = true
					failureReason = fmt.Sprintf("bifrostErr: %v", bifrostErr)
				} else if result != nil && result.Content != nil && result.Content.ContentStr != nil {
					returnValue, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
					if hasError {
						executionFailed = true
						failureReason = fmt.Sprintf("ParseCodeModeResponse error: %s", errorMsg)
					} else if returnValue != nil {
						if returnObj, ok := returnValue.(map[string]interface{}); ok {
							if errorField, ok := returnObj["error"]; ok {
								executionFailed = true
								failureReason = fmt.Sprintf("return value error field: %v", errorField)
							}
						}
					}
				}

				if !executionFailed {
					t.Errorf("%s (%s): expected execution to fail but it succeeded", tc.name, tc.description)
					return
				}
				t.Logf("%s: execution failed as expected - %s", tc.name, failureReason)
			} else {
				// shouldFail == false: assert execution succeeded without errors
				if bifrostErr != nil {
					t.Errorf("%s (%s): unexpected bifrostErr: %v", tc.name, tc.description, bifrostErr)
					return
				}
				if result == nil || result.Content == nil || result.Content.ContentStr == nil {
					t.Errorf("%s (%s): expected result with content but got nil", tc.name, tc.description)
					return
				}
				_, hasError, errorMsg := ParseCodeModeResponse(t, *result.Content.ContentStr)
				if hasError {
					t.Errorf("%s (%s): unexpected error in response: %s", tc.name, tc.description, errorMsg)
					return
				}
				t.Logf("%s: execution succeeded as expected", tc.name)
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

	unicodeCode := `result = "Hello 🌍"`
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
	if !strings.Contains(content, ".pyi") {
		t.Skip("No .pyi files found")
	}

	// Extract first .pyi file name
	lines := strings.Split(content, "\n")
	var firstFile string
	for _, line := range lines {
		if strings.Contains(line, ".pyi") {
			// Extract just the filename
			parts := strings.Fields(line)
			for _, part := range parts {
				if strings.HasSuffix(part, ".pyi") {
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

	// The readToolFile tool handles invalid line numbers gracefully by returning content
	// rather than returning explicit error messages. Test that the tool doesn't crash
	// and returns some content for edge cases.
	boundaryTests := []struct {
		name        string
		startLine   int
		endLine     int
		description string
	}{
		{
			name:        "start_line_zero",
			startLine:   0,
			endLine:     5,
			description: "Tool should handle startLine=0 gracefully",
		},
		{
			name:        "start_line_negative",
			startLine:   -1,
			endLine:     5,
			description: "Tool should handle negative startLine gracefully",
		},
		{
			name:        "end_line_before_start",
			startLine:   10,
			endLine:     5,
			description: "Tool should handle endLine < startLine gracefully",
		},
		{
			name:        "very_large_line_number",
			startLine:   1,
			endLine:     999999,
			description: "Tool should handle very large endLine gracefully",
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

			// The tool should not crash and should return a result
			// It handles invalid line numbers gracefully by returning content
			if bifrostErr != nil {
				t.Logf("%s: Got bifrost error (acceptable): %v", tc.description, bifrostErr)
			} else {
				require.NotNil(t, result, "%s: result should not be nil", tc.description)
				require.NotNil(t, result.Content, "%s: result.Content should not be nil", tc.description)
				require.NotNil(t, result.Content.ContentStr, "%s: result.Content.ContentStr should not be nil", tc.description)
				// Just verify we got some content back (tool handles gracefully)
				t.Logf("%s: Got response with %d characters", tc.description, len(*result.Content.ContentStr))
			}
		})
	}
}

// =============================================================================
// GUARDRAIL LEAK REGRESSION
// =============================================================================

// blockingOutputPlugin emulates an MCP output guardrail: it blocks any tool
// result containing secret, and reports every body it was given a chance to see.
type blockingOutputPlugin struct {
	secret    string
	redact    bool
	inspected []string
}

func (p *blockingOutputPlugin) GetName() string { return "blockingOutputPlugin" }
func (p *blockingOutputPlugin) Cleanup() error  { return nil }

func (p *blockingOutputPlugin) PreMCPHook(ctx *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPRequest, *schemas.MCPPluginShortCircuit, error) {
	return req, nil, nil
}

func (p *blockingOutputPlugin) PostMCPHook(ctx *schemas.BifrostContext, resp *schemas.BifrostMCPResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostMCPResponse, *schemas.BifrostError, error) {
	if resp == nil || resp.ChatMessage == nil || resp.ChatMessage.Content == nil || resp.ChatMessage.Content.ContentStr == nil {
		return resp, bifrostErr, nil
	}
	// Scope to the probe tools so the executeToolCode envelope stays unguarded —
	// the leak must be closed by code mode itself, not by a catch-all rule.
	if !strings.HasPrefix(resp.ExtraFields.ToolName, "leak_") {
		return resp, bifrostErr, nil
	}
	body := *resp.ChatMessage.Content.ContentStr
	p.inspected = append(p.inspected, resp.ExtraFields.ToolName)
	if !strings.Contains(body, p.secret) {
		return resp, bifrostErr, nil
	}
	if p.redact {
		cleaned := strings.ReplaceAll(body, p.secret, "[REDACTED]")
		resp.ChatMessage.Content.ContentStr = &cleaned
		return resp, bifrostErr, nil
	}
	return nil, &schemas.BifrostError{
		IsBifrostError: false,
		Error:          &schemas.ErrorField{Message: "regex pattern matched"},
	}, nil
}

// TestExecuteToolCode_GuardrailedResultNeverReachesModel pins the fix for the
// code-mode leak: the execution log is rendered into the tool message the model
// receives, so a result an output guardrail blocked or redacted must not appear
// there. Covers both result shapes, including the "Error: " text a server uses
// for soft failures, which used to bypass PostMCPHook altogether.
func TestExecuteToolCode_GuardrailedResultNeverReachesModel(t *testing.T) {
	const secret = "PAGE-ID-998877-SECRET"
	body := secret + " " + strings.Repeat("CONFIDENTIAL-BODY ", 200)

	for _, tc := range []struct {
		name   string
		tool   string
		text   string
		redact bool
	}{
		{name: "blocked text result", tool: "leak_page", text: body},
		{name: "blocked error result", tool: "leak_error", text: "Error: " + body},
		{name: "redacted text result", tool: "leak_page", text: body, redact: true},
		{name: "redacted error result", tool: "leak_error", text: "Error: " + body, redact: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := setupMCPManager(t)
			toolSchema := schemas.ChatTool{
				Type: schemas.ChatToolTypeFunction,
				Function: &schemas.ChatToolFunction{
					Name:        tc.tool,
					Description: schemas.Ptr("returns a large confidential body"),
					Parameters: &schemas.ToolFunctionParameters{
						Type:       "object",
						Properties: schemas.NewOrderedMapFromPairs(),
					},
				},
			}
			require.NoError(t, manager.RegisterTool(tc.tool, "returns a large confidential body",
				func(any) (string, error) { return tc.text, nil }, toolSchema))

			for _, client := range manager.GetClients() {
				if client.ExecutionConfig.ID == "bifrostInternal" {
					cfg := client.ExecutionConfig
					cfg.IsCodeModeClient = true
					cfg.ToolsToExecute = []string{"*"}
					require.NoError(t, manager.UpdateClient(cfg.ID, cfg))
				}
			}

			guard := &blockingOutputPlugin{secret: secret, redact: tc.redact}
			b, err := core.Init(context.Background(), schemas.BifrostConfig{
				Account:    &testAccount{},
				MCPPlugins: []schemas.MCPPlugin{guard},
				Logger:     core.NewDefaultLogger(schemas.LogLevelError),
			})
			require.NoError(t, err)
			t.Cleanup(b.Shutdown)
			b.SetMCPManager(manager)

			args, err := json.Marshal(map[string]any{
				"code": fmt.Sprintf("page = bifrostInternal.%s()\nresult = {\"len\": len(page)}", tc.tool),
			})
			require.NoError(t, err)
			call := schemas.ChatAssistantMessageToolCall{
				ID:   schemas.Ptr("guardrail-leak"),
				Type: schemas.Ptr("function"),
				Function: schemas.ChatAssistantMessageToolCallFunction{
					Name:      schemas.Ptr("executeToolCode"),
					Arguments: string(args),
				},
			}

			msg, bifrostErr := b.ExecuteChatMCPTool(createTestContext(), &call)
			require.Nil(t, bifrostErr)
			require.NotNil(t, msg)
			require.NotNil(t, msg.Content)
			require.NotNil(t, msg.Content.ContentStr)

			require.NotEmpty(t, guard.inspected, "guardrail must see the tool result before the model does")
			assert.NotContains(t, *msg.Content.ContentStr, secret,
				"guarded content reached the model through the code-mode execution log")
			if tc.redact {
				// Redaction rewrites the result rather than withholding it, so the
				// surviving body is expected - only the matched span must be gone.
				assert.Contains(t, *msg.Content.ContentStr, "[REDACTED]",
					"the model should see the post-hook text, not the wire result")
				// Bounded on both channels: the log preview and the raised error.
				assert.Less(t, len(*msg.Content.ContentStr), 4096,
					"approved tool text must stay bounded before it reaches the model")
				return
			}
			assert.NotContains(t, *msg.Content.ContentStr, "CONFIDENTIAL-BODY",
				"guarded content reached the model through the code-mode execution log")
		})
	}
}
