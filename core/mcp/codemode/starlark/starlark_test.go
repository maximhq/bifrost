//go:build !tinygo && !wasm

package starlark

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/canonical/starlark/starlark"
	"github.com/canonical/starlark/syntax"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	codemcp "github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
)

type testClientManager struct {
	clients map[string]*schemas.MCPClientState
	tools   map[string][]schemas.ChatTool
}

func (m *testClientManager) GetClientForTool(toolName string) *schemas.MCPClientState {
	for clientName, tools := range m.tools {
		for _, tool := range tools {
			if tool.Function != nil && tool.Function.Name == toolName {
				return m.clients[clientName]
			}
		}
	}
	return nil
}

func (m *testClientManager) GetClientByName(clientName string) *schemas.MCPClientState {
	return m.clients[clientName]
}

func (m *testClientManager) GetToolPerClient(ctx context.Context) map[string][]schemas.ChatTool {
	return m.tools
}

func (m *testClientManager) GetPluginPipeline() codemcp.PluginPipeline {
	return nil
}

func (m *testClientManager) ReleasePluginPipeline(pipeline codemcp.PluginPipeline) {}
func (m *testClientManager) AcquireClientConn(ctx *schemas.BifrostContext, state *schemas.MCPClientState) (*client.Client, func(), error) {
	return nil, func() {}, nil
}
func (m *testClientManager) ReconnectClient(id string) error { return nil }

func (m *testClientManager) AwaitReconnect(clientID string, budget time.Duration) (bool, error) {
	return false, nil
}
func (m *testClientManager) RunWithPluginPipeline(ctx *schemas.BifrostContext, req *schemas.BifrostMCPRequest, op codemcp.MCPOpFunc) (*schemas.BifrostMCPResponse, *schemas.BifrostError) {
	resp, err := op(req)
	if err != nil {
		return nil, &schemas.BifrostError{IsBifrostError: false, Error: &schemas.ErrorField{Message: err.Error()}}
	}
	return resp, nil
}

func TestStarlarkToGo(t *testing.T) {
	t.Run("Convert None", func(t *testing.T) {
		result := mustStarlarkToGo(t, starlark.None)
		if result != nil {
			t.Errorf("Expected nil, got %v", result)
		}
	})

	t.Run("Convert Bool", func(t *testing.T) {
		result := mustStarlarkToGo(t, starlark.Bool(true))
		if result != true {
			t.Errorf("Expected true, got %v", result)
		}
	})

	t.Run("Convert Int", func(t *testing.T) {
		result := mustStarlarkToGo(t, starlark.MakeInt(42))
		if result != int64(42) {
			t.Errorf("Expected 42, got %v", result)
		}
	})

	t.Run("Convert Float", func(t *testing.T) {
		result := mustStarlarkToGo(t, starlark.Float(3.14))
		if result != 3.14 {
			t.Errorf("Expected 3.14, got %v", result)
		}
	})

	t.Run("Convert String", func(t *testing.T) {
		result := mustStarlarkToGo(t, starlark.String("hello"))
		if result != "hello" {
			t.Errorf("Expected 'hello', got %v", result)
		}
	})

	t.Run("Convert List", func(t *testing.T) {
		list := starlark.NewList([]starlark.Value{
			starlark.MakeInt(1),
			starlark.MakeInt(2),
			starlark.MakeInt(3),
		})
		result := mustStarlarkToGo(t, list)
		arr, ok := result.([]interface{})
		if !ok {
			t.Errorf("Expected []interface{}, got %T", result)
		}
		if len(arr) != 3 {
			t.Errorf("Expected length 3, got %d", len(arr))
		}
		if arr[0] != int64(1) {
			t.Errorf("Expected first element 1, got %v", arr[0])
		}
	})

	t.Run("Convert Dict", func(t *testing.T) {
		dict := starlark.NewDict(2)
		dict.SetKey(starlark.String("key1"), starlark.String("value1"))
		dict.SetKey(starlark.String("key2"), starlark.MakeInt(42))

		result := mustStarlarkToGo(t, dict)
		m, ok := result.(map[string]interface{})
		if !ok {
			t.Errorf("Expected map[string]interface{}, got %T", result)
		}
		if m["key1"] != "value1" {
			t.Errorf("Expected key1='value1', got %v", m["key1"])
		}
		if m["key2"] != int64(42) {
			t.Errorf("Expected key2=42, got %v", m["key2"])
		}
	})
}

func TestGoToStarlark(t *testing.T) {
	t.Run("Convert nil", func(t *testing.T) {
		result := mustGoToStarlark(t, nil)
		if result != starlark.None {
			t.Errorf("Expected None, got %v", result)
		}
	})

	t.Run("Convert bool", func(t *testing.T) {
		result := mustGoToStarlark(t, true)
		if result != starlark.Bool(true) {
			t.Errorf("Expected True, got %v", result)
		}
	})

	t.Run("Convert int", func(t *testing.T) {
		result := mustGoToStarlark(t, 42)
		expected := starlark.MakeInt(42)
		if result.String() != expected.String() {
			t.Errorf("Expected %v, got %v", expected, result)
		}
	})

	t.Run("Convert float64", func(t *testing.T) {
		result := mustGoToStarlark(t, 3.14)
		if result != starlark.Float(3.14) {
			t.Errorf("Expected 3.14, got %v", result)
		}
	})

	t.Run("Convert string", func(t *testing.T) {
		result := mustGoToStarlark(t, "hello")
		if result != starlark.String("hello") {
			t.Errorf("Expected 'hello', got %v", result)
		}
	})

	t.Run("Convert slice", func(t *testing.T) {
		result := mustGoToStarlark(t, []interface{}{1, "two", 3.0})
		list, ok := result.(*starlark.List)
		if !ok {
			t.Errorf("Expected *starlark.List, got %T", result)
		}
		if list.Len() != 3 {
			t.Errorf("Expected length 3, got %d", list.Len())
		}
	})

	t.Run("Convert map", func(t *testing.T) {
		result := mustGoToStarlark(t, map[string]interface{}{
			"key1": "value1",
			"key2": 42,
		})
		dict, ok := result.(*starlark.Dict)
		if !ok {
			t.Errorf("Expected *starlark.Dict, got %T", result)
		}
		val, found, _ := dict.Get(starlark.String("key1"))
		if !found {
			t.Errorf("Expected key1 to exist")
		}
		if val != starlark.String("value1") {
			t.Errorf("Expected value1, got %v", val)
		}
	})
}

func TestGetCanonicalToolName(t *testing.T) {
	if got := getCanonicalToolName("github", "github-SEARCH_REPOS"); got != "search_repos" {
		t.Fatalf("expected canonical tool name search_repos, got %q", got)
	}

	if got := getCanonicalToolName("math", "math-123Add!"); got != "_123add" {
		t.Fatalf("expected canonical tool name _123add, got %q", got)
	}
}

func TestMatchesToolReferenceSupportsCanonicalAndLegacyNames(t *testing.T) {
	clientName := "github"
	originalToolName := "github-SEARCH_REPOS"

	testCases := []string{
		"search_repos",
		"SEARCH_REPOS",
	}

	for _, toolRef := range testCases {
		if !matchesToolReference(toolRef, clientName, originalToolName) {
			t.Fatalf("expected %q to match %q", toolRef, originalToolName)
		}
	}
}

func TestHandleListToolFilesUsesCanonicalToolIdentifiers(t *testing.T) {
	mode := NewStarlarkCodeMode(&codemcp.CodeModeConfig{
		BindingLevel:         schemas.CodeModeBindingLevelTool,
		ToolExecutionTimeout: time.Second,
	}, nil)

	clientName := "github"
	mode.clientManager = &testClientManager{
		clients: map[string]*schemas.MCPClientState{
			clientName: {
				Name: clientName,
				ExecutionConfig: &schemas.MCPClientConfig{
					Name:             clientName,
					IsCodeModeClient: true,
				},
			},
		},
		tools: map[string][]schemas.ChatTool{
			clientName: {
				{
					Function: &schemas.ChatToolFunction{
						Name: "github-SEARCH_REPOS",
					},
				},
			},
		},
	}

	msg, err := mode.handleListToolFiles(context.Background(), schemas.ChatAssistantMessageToolCall{
		ID: schemas.Ptr("tool-call-1"),
	})
	if err != nil {
		t.Fatalf("handleListToolFiles returned error: %v", err)
	}

	if msg == nil || msg.Content == nil || msg.Content.ContentStr == nil {
		t.Fatal("expected tool response content")
	}

	content := *msg.Content.ContentStr
	if !strings.Contains(content, "search_repos.pyi") {
		t.Fatalf("expected canonical tool file path in response, got:\n%s", content)
	}
	if strings.Contains(content, "SEARCH_REPOS.pyi") {
		t.Fatalf("did not expect raw uppercase tool file path in response, got:\n%s", content)
	}
	if !strings.Contains(content, "readToolFile before executeToolCode") {
		t.Fatalf("expected workflow guidance in response, got:\n%s", content)
	}
}

func TestGeneratePythonErrorHints(t *testing.T) {
	serverKeys := []string{"calculator", "weather"}

	t.Run("Undefined variable hint", func(t *testing.T) {
		hints := generatePythonErrorHints("name 'foo' is not defined", serverKeys)
		if len(hints) == 0 {
			t.Error("Expected hints, got none")
		}
		found := false
		for _, hint := range hints {
			if strings.Contains(hint, "Variable 'foo' is not defined.") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected exact undefined variable hint for foo, got: %v", hints)
		}
	})

	t.Run("Syntax error hint", func(t *testing.T) {
		hints := generatePythonErrorHints("syntax error at line 5", serverKeys)
		if len(hints) == 0 {
			t.Error("Expected hints, got none")
		}
		found := false
		for _, hint := range hints {
			if containsAny(hint, "syntax", "indentation", "colon") {
				found = true
				break
			}
		}
		if !found {
			t.Error("Expected hint about syntax error")
		}
	})

	t.Run("Attribute error hint", func(t *testing.T) {
		hints := generatePythonErrorHints("'dict' object has no attribute 'foo'", serverKeys)
		if len(hints) == 0 {
			t.Error("Expected hints, got none")
		}
		found := false
		for _, hint := range hints {
			if containsAny(hint, "attribute", "brackets", "key") {
				found = true
				break
			}
		}
		if !found {
			t.Error("Expected hint about attribute access")
		}
	})
}

func containsAny(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if containsIgnoreCase(s, sub) {
			return true
		}
	}
	return false
}

func containsIgnoreCase(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && (containsIgnoreCase(s[1:], substr) || (len(s) >= len(substr) && equalFold(s[:len(substr)], substr))))
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func TestExtractResultFromResponsesMessage(t *testing.T) {
	t.Run("Extract error from ResponsesMessage", func(t *testing.T) {
		errorMsg := "Tool is not allowed by security policy: dangerous_tool"
		msg := &schemas.ResponsesMessage{
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				Error: &schemas.ResponsesToolMessageError{ResponsesToolMessageErrorStr: &errorMsg},
			},
		}

		result, err := extractResultFromResponsesMessage(msg)
		if err == nil {
			t.Errorf("Expected error, got nil")
		}
		if err.Error() != errorMsg {
			t.Errorf("Expected error message '%s', got '%s'", errorMsg, err.Error())
		}
		if result != nil {
			t.Errorf("Expected nil result when error is present, got %v", result)
		}
	})

	t.Run("Extract string output from ResponsesMessage", func(t *testing.T) {
		outputStr := "success result"
		msg := &schemas.ResponsesMessage{
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				Output: &schemas.ResponsesToolMessageOutputStruct{
					ResponsesToolCallOutputStr: &outputStr,
				},
			},
		}

		result, err := extractResultFromResponsesMessage(msg)
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
		if result != outputStr {
			t.Errorf("Expected result '%s', got '%v'", outputStr, result)
		}
	})

	t.Run("Extract JSON output from ResponsesMessage", func(t *testing.T) {
		outputStr := `{"status": "success", "data": "test"}`
		msg := &schemas.ResponsesMessage{
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				Output: &schemas.ResponsesToolMessageOutputStruct{
					ResponsesToolCallOutputStr: &outputStr,
				},
			},
		}

		result, err := extractResultFromResponsesMessage(msg)
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}

		resultMap, ok := result.(map[string]interface{})
		if !ok {
			t.Errorf("Expected map, got %T", result)
		}

		if resultMap["status"] != "success" {
			t.Errorf("Expected status 'success', got '%v'", resultMap["status"])
		}
	})

	t.Run("Extract from ResponsesFunctionToolCallOutputBlocks", func(t *testing.T) {
		text1 := "First block"
		text2 := "Second block"
		msg := &schemas.ResponsesMessage{
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				Output: &schemas.ResponsesToolMessageOutputStruct{
					ResponsesFunctionToolCallOutputBlocks: []schemas.ResponsesMessageContentBlock{
						{Text: &text1},
						{Text: &text2},
					},
				},
			},
		}

		result, err := extractResultFromResponsesMessage(msg)
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}

		expectedResult := "First block\nSecond block"
		if result != expectedResult {
			t.Errorf("Expected result '%s', got '%v'", expectedResult, result)
		}
	})

	t.Run("Extract JSON from ResponsesFunctionToolCallOutputBlocks", func(t *testing.T) {
		jsonText := `{"key": "value"}`
		msg := &schemas.ResponsesMessage{
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				Output: &schemas.ResponsesToolMessageOutputStruct{
					ResponsesFunctionToolCallOutputBlocks: []schemas.ResponsesMessageContentBlock{
						{Text: &jsonText},
					},
				},
			},
		}

		result, err := extractResultFromResponsesMessage(msg)
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}

		resultMap, ok := result.(map[string]interface{})
		if !ok {
			t.Errorf("Expected map, got %T", result)
		}

		if resultMap["key"] != "value" {
			t.Errorf("Expected key 'value', got '%v'", resultMap["key"])
		}
	})

	t.Run("Handle nil message", func(t *testing.T) {
		result, err := extractResultFromResponsesMessage(nil)
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
		if result != nil {
			t.Errorf("Expected nil result for nil message, got %v", result)
		}
	})

	t.Run("Handle message without ResponsesToolMessage", func(t *testing.T) {
		msg := &schemas.ResponsesMessage{}

		result, err := extractResultFromResponsesMessage(msg)
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
		if result != nil {
			t.Errorf("Expected nil result for message without tool message, got %v", result)
		}
	})

	t.Run("Handle empty error string (should not error)", func(t *testing.T) {
		emptyError := ""
		output := "successful output"
		msg := &schemas.ResponsesMessage{
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				Error: &schemas.ResponsesToolMessageError{ResponsesToolMessageErrorStr: &emptyError},
				Output: &schemas.ResponsesToolMessageOutputStruct{
					ResponsesToolCallOutputStr: &output,
				},
			},
		}

		result, err := extractResultFromResponsesMessage(msg)
		if err != nil {
			t.Errorf("Expected no error for empty error string, got: %v", err)
		}
		if result != output {
			t.Errorf("Expected output for empty legacy error string, got %v", result)
		}
	})

	t.Run("Handle empty structured error before output", func(t *testing.T) {
		output := "must not be returned"
		msg := &schemas.ResponsesMessage{
			ResponsesToolMessage: &schemas.ResponsesToolMessage{
				Error: &schemas.ResponsesToolMessageError{
					ResponsesToolMessageErrorStruct: &schemas.ResponsesToolMessageErrorStruct{},
				},
				Output: &schemas.ResponsesToolMessageOutputStruct{
					ResponsesToolCallOutputStr: &output,
				},
			},
		}

		result, err := extractResultFromResponsesMessage(msg)
		if err == nil || err.Error() != "tool call returned an error" {
			t.Fatalf("Expected fallback structured error, got result=%v err=%v", result, err)
		}
		if result != nil {
			t.Fatalf("Expected nil result when structured error is present, got %v", result)
		}
	})
}

func TestExtractResultFromChatMessage(t *testing.T) {
	t.Run("Extract string from ChatMessage", func(t *testing.T) {
		content := "test result"
		msg := &schemas.ChatMessage{
			Content: &schemas.ChatMessageContent{
				ContentStr: &content,
			},
		}

		result := extractResultFromChatMessage(msg)
		if result != content {
			t.Errorf("Expected result '%s', got '%v'", content, result)
		}
	})

	t.Run("Extract JSON from ChatMessage", func(t *testing.T) {
		content := `{"status": "ok"}`
		msg := &schemas.ChatMessage{
			Content: &schemas.ChatMessageContent{
				ContentStr: &content,
			},
		}

		result := extractResultFromChatMessage(msg)
		resultMap, ok := result.(map[string]interface{})
		if !ok {
			t.Errorf("Expected map, got %T", result)
		}

		if resultMap["status"] != "ok" {
			t.Errorf("Expected status 'ok', got '%v'", resultMap["status"])
		}
	})

	t.Run("Handle nil ChatMessage", func(t *testing.T) {
		result := extractResultFromChatMessage(nil)
		if result != nil {
			t.Errorf("Expected nil result for nil message, got %v", result)
		}
	})

	t.Run("Handle ChatMessage without Content", func(t *testing.T) {
		msg := &schemas.ChatMessage{}
		result := extractResultFromChatMessage(msg)
		if result != nil {
			t.Errorf("Expected nil result for message without content, got %v", result)
		}
	})
}

func TestFormatResultForLog(t *testing.T) {
	t.Run("Format nil result", func(t *testing.T) {
		result := formatResultForLog(nil)
		if result != "null" {
			t.Errorf("Expected 'null', got '%s'", result)
		}
	})

	t.Run("Format string result", func(t *testing.T) {
		result := formatResultForLog("test string")
		if result != `"test string"` {
			t.Errorf("Expected '\"test string\"', got '%s'", result)
		}
	})

	t.Run("Format map result", func(t *testing.T) {
		input := map[string]interface{}{"key": "value"}
		result := formatResultForLog(input)

		// Parse it back to verify it's valid JSON
		var parsed map[string]interface{}
		err := sonic.Unmarshal([]byte(result), &parsed)
		if err != nil {
			t.Errorf("Result is not valid JSON: %v", err)
		}

		if parsed["key"] != "value" {
			t.Errorf("Expected key 'value', got '%v'", parsed["key"])
		}
	})

	t.Run("Truncate long result", func(t *testing.T) {
		longString := ""
		for i := 0; i < 300; i++ {
			longString += "a"
		}

		result := formatResultForLog(longString)
		if len(result) > 200 {
			// Should be truncated to around 200 chars (plus quotes and ellipsis)
			t.Logf("Result length: %d (truncated as expected)", len(result))
		}
	})
}

// starlarkOpts returns the FileOptions used by the code mode executor.
// Kept in sync with executecode.go to test the same dialect configuration.
func starlarkOpts() *syntax.FileOptions {
	return &syntax.FileOptions{
		TopLevelControl: true,
		While:           true,
		Set:             true,
		GlobalReassign:  true,
		Recursion:       true,
	}
}

// execStarlark is a test helper that executes Starlark code with our dialect options
// and returns the globals and any error.
func execStarlark(code string) (starlark.StringDict, error) {
	thread := &starlark.Thread{Name: "test"}
	return starlark.ExecFileOptions(starlarkOpts(), thread, "test.star", code, nil)
}

func TestStarlarkDialectOptions(t *testing.T) {
	t.Run("Top-level for loop", func(t *testing.T) {
		code := `
items = []
for i in range(3):
    items.append(i)
result = items
`
		globals, err := execStarlark(code)
		if err != nil {
			t.Fatalf("Top-level for loop should work, got error: %v", err)
		}
		resultVal := globals["result"]
		list, ok := resultVal.(*starlark.List)
		if !ok {
			t.Fatalf("Expected list, got %T", resultVal)
		}
		if list.Len() != 3 {
			t.Errorf("Expected 3 items, got %d", list.Len())
		}
	})

	t.Run("Top-level if statement", func(t *testing.T) {
		code := `
x = 10
if x > 5:
    result = "big"
else:
    result = "small"
`
		globals, err := execStarlark(code)
		if err != nil {
			t.Fatalf("Top-level if should work, got error: %v", err)
		}
		if globals["result"] != starlark.String("big") {
			t.Errorf("Expected 'big', got %v", globals["result"])
		}
	})

	t.Run("Top-level while loop", func(t *testing.T) {
		code := `
count = 0
while count < 5:
    count += 1
result = count
`
		globals, err := execStarlark(code)
		if err != nil {
			t.Fatalf("Top-level while loop should work, got error: %v", err)
		}
		resultVal := globals["result"]
		if resultVal.String() != "5" {
			t.Errorf("Expected 5, got %v", resultVal)
		}
	})

	t.Run("While loop inside function", func(t *testing.T) {
		code := `
def countdown(n):
    items = []
    while n > 0:
        items.append(n)
        n -= 1
    return items
result = countdown(3)
`
		globals, err := execStarlark(code)
		if err != nil {
			t.Fatalf("While in function should work, got error: %v", err)
		}
		list := globals["result"].(*starlark.List)
		if list.Len() != 3 {
			t.Errorf("Expected 3 items, got %d", list.Len())
		}
	})

	t.Run("set() builtin", func(t *testing.T) {
		code := `
s = set([1, 2, 3, 2, 1])
result = len(s)
`
		globals, err := execStarlark(code)
		if err != nil {
			t.Fatalf("set() should work, got error: %v", err)
		}
		if globals["result"].String() != "3" {
			t.Errorf("Expected 3 unique items, got %v", globals["result"])
		}
	})

	t.Run("Global variable reassignment", func(t *testing.T) {
		code := `
x = 1
x = x + 1
x = x * 3
result = x
`
		globals, err := execStarlark(code)
		if err != nil {
			t.Fatalf("Global reassignment should work, got error: %v", err)
		}
		if globals["result"].String() != "6" {
			t.Errorf("Expected 6, got %v", globals["result"])
		}
	})

	t.Run("Recursive function", func(t *testing.T) {
		code := `
def factorial(n):
    if n <= 1:
        return 1
    return n * factorial(n - 1)
result = factorial(5)
`
		globals, err := execStarlark(code)
		if err != nil {
			t.Fatalf("Recursion should work, got error: %v", err)
		}
		if globals["result"].String() != "120" {
			t.Errorf("Expected 120, got %v", globals["result"])
		}
	})
}

func TestStarlarkStringEscapePreservation(t *testing.T) {
	t.Run("Backslash-n in string literal preserved", func(t *testing.T) {
		// Simulate what happens after JSON deserialization:
		// Model writes: {"code": "msg = \"hello\\nworld\""}
		// sonic.Unmarshal produces: msg = "hello\nworld" (where \n is two chars: \ + n)
		// Starlark should interpret \n as newline escape inside the string
		code := "msg = \"hello\\nworld\"\nresult = msg"

		globals, err := execStarlark(code)
		if err != nil {
			t.Fatalf("String with \\n escape should work, got error: %v", err)
		}
		resultStr := string(globals["result"].(starlark.String))
		if resultStr != "hello\nworld" {
			t.Errorf("Expected 'hello<newline>world', got %q", resultStr)
		}
	})

	t.Run("Multiple escape sequences in strings", func(t *testing.T) {
		code := "msg = \"col1\\tcol2\\nrow1\\trow2\"\nresult = msg"

		globals, err := execStarlark(code)
		if err != nil {
			t.Fatalf("String with multiple escapes should work, got error: %v", err)
		}
		resultStr := string(globals["result"].(starlark.String))
		if resultStr != "col1\tcol2\nrow1\trow2" {
			t.Errorf("Expected tab/newline escapes, got %q", resultStr)
		}
	})

	t.Run("Newline join pattern", func(t *testing.T) {
		// This is the exact pattern that failed 7 times in benchmarks
		code := `
def main():
    lines = ["line1", "line2", "line3"]
    content = "\n".join(lines)
    return content
result = main()
`
		globals, err := execStarlark(code)
		if err != nil {
			t.Fatalf("Newline join pattern should work, got error: %v", err)
		}
		resultStr := string(globals["result"].(starlark.String))
		if resultStr != "line1\nline2\nline3" {
			t.Errorf("Expected joined lines, got %q", resultStr)
		}
	})

	t.Run("chr() for newline", func(t *testing.T) {
		code := `
nl = chr(10)
result = "hello" + nl + "world"
`
		globals, err := execStarlark(code)
		if err != nil {
			t.Fatalf("chr(10) should work, got error: %v", err)
		}
		resultStr := string(globals["result"].(starlark.String))
		if resultStr != "hello\nworld" {
			t.Errorf("Expected 'hello<newline>world', got %q", resultStr)
		}
	})

	t.Run("Triple-quoted strings", func(t *testing.T) {
		code := "result = \"\"\"line1\nline2\nline3\"\"\""

		globals, err := execStarlark(code)
		if err != nil {
			t.Fatalf("Triple-quoted string should work, got error: %v", err)
		}
		resultStr := string(globals["result"].(starlark.String))
		if resultStr != "line1\nline2\nline3" {
			t.Errorf("Expected multiline string, got %q", resultStr)
		}
	})

	t.Run("Raw string preserves backslash", func(t *testing.T) {
		code := "result = r\"hello\\nworld\""

		globals, err := execStarlark(code)
		if err != nil {
			t.Fatalf("Raw string should work, got error: %v", err)
		}
		resultStr := string(globals["result"].(starlark.String))
		// Raw string: \n stays as two characters \ and n
		if resultStr != "hello\\nworld" {
			t.Errorf("Expected literal backslash-n, got %q", resultStr)
		}
	})

	t.Run("JSON deserialization then Starlark execution", func(t *testing.T) {
		// End-to-end: simulate the exact flow from model JSON → sonic.Unmarshal → Starlark
		jsonArgs := `{"code": "lines = [\"a\", \"b\", \"c\"]\nresult = \"\\n\".join(lines)"}`

		var arguments map[string]interface{}
		err := sonic.Unmarshal([]byte(jsonArgs), &arguments)
		if err != nil {
			t.Fatalf("JSON unmarshal failed: %v", err)
		}

		code := arguments["code"].(string)

		globals, starlarkErr := execStarlark(code)
		if starlarkErr != nil {
			t.Fatalf("Starlark execution failed: %v", starlarkErr)
		}
		resultStr := string(globals["result"].(starlark.String))
		if resultStr != "a\nb\nc" {
			t.Errorf("Expected 'a<newline>b<newline>c', got %q", resultStr)
		}
	})
}

func TestStarlarkUnsupportedFeatures(t *testing.T) {
	t.Run("try/except rejected", func(t *testing.T) {
		code := `
def main():
    try:
        x = 1
    except:
        x = 0
result = main()
`
		_, err := execStarlark(code)
		if err == nil {
			t.Fatal("try/except should be rejected by Starlark")
		}
		if !strings.Contains(err.Error(), "got try") && !strings.Contains(err.Error(), "illegal token") {
			t.Errorf("Expected a parser rejection, got: %v", err)
		}
	})

	t.Run("raise rejected", func(t *testing.T) {
		code := `raise ValueError("test")`

		_, err := execStarlark(code)
		if err == nil {
			t.Fatal("raise should be rejected by Starlark")
		}
	})

	t.Run("class rejected", func(t *testing.T) {
		code := `
class Foo:
    pass
`
		_, err := execStarlark(code)
		if err == nil {
			t.Fatal("class should be rejected by Starlark")
		}
	})

	t.Run("import rejected", func(t *testing.T) {
		code := `import json`

		_, err := execStarlark(code)
		if err == nil {
			t.Fatal("import should be rejected by Starlark")
		}
	})
}

func TestGeneratePythonErrorHintsNewCases(t *testing.T) {
	serverKeys := []string{"Github", "SqLite"}

	t.Run("try/except hint", func(t *testing.T) {
		hints := generatePythonErrorHints("code.star:3:9: got try, want primary expression", serverKeys)
		if len(hints) == 0 {
			t.Fatal("Expected hints for try/except error")
		}
		found := false
		for _, hint := range hints {
			if containsAny(hint, "try/except", "exception handling") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected hint about try/except not being supported, got: %v", hints)
		}
	})

	t.Run("except hint", func(t *testing.T) {
		hints := generatePythonErrorHints("code.star:5:9: got except, want primary expression", serverKeys)
		if len(hints) == 0 {
			t.Fatal("Expected hints for except error")
		}
		found := false
		for _, hint := range hints {
			if containsAny(hint, "try/except", "exception handling") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected hint about exception handling, got: %v", hints)
		}
	})

	t.Run("finally hint", func(t *testing.T) {
		hints := generatePythonErrorHints("code.star:7:9: got finally, want primary expression", serverKeys)
		if len(hints) == 0 {
			t.Fatal("Expected hints for finally error")
		}
		found := false
		for _, hint := range hints {
			if containsAny(hint, "try/except", "exception handling") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected hint about exception handling, got: %v", hints)
		}
	})

	t.Run("raise hint", func(t *testing.T) {
		hints := generatePythonErrorHints("code.star:2:1: got raise, want primary expression", serverKeys)
		if len(hints) == 0 {
			t.Fatal("Expected hints for raise error")
		}
		found := false
		for _, hint := range hints {
			if containsAny(hint, "try/except", "exception handling") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected hint about exception handling, got: %v", hints)
		}
	})

	t.Run("Undefined variable includes scope hint", func(t *testing.T) {
		hints := generatePythonErrorHints("code.star:3:17: undefined: commits_n8n", serverKeys)
		if len(hints) == 0 {
			t.Fatal("Expected hints for undefined variable")
		}
		foundVar := false
		foundScope := false
		for _, hint := range hints {
			if strings.Contains(hint, "Variable 'commits_n8n' is not defined.") {
				foundVar = true
			}
			if containsAny(hint, "fresh scope", "persist") {
				foundScope = true
			}
		}
		if !foundVar {
			t.Errorf("Expected exact undefined variable hint for commits_n8n, got: %v", hints)
		}
		if !foundScope {
			t.Errorf("Expected scope persistence hint, got: %v", hints)
		}
	})
}

func TestCodeModeExecutionBudget(t *testing.T) {
	mode := NewStarlarkCodeMode(&codemcp.CodeModeConfig{ToolExecutionTimeout: 5 * time.Second}, nil)
	mode.clientManager = &testClientManager{}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	result := mode.executeCode(ctx, "result = 0\nfor i in range(2000000):\n    result += i")
	if result.Errors == nil {
		t.Fatal("expected execution budget rejection, computation ran to completion")
	}
	if !strings.Contains(result.Errors.Message, "step") {
		t.Fatalf("expected step limit, got %s", result.Errors.Message)
	}
}

func TestCodeModeSandboxLimitsAndRecovery(t *testing.T) {
	// No case here is about steps; an unbounded step budget makes each one reach
	// the limit it targets instead of running out of steps first.
	mode := NewStarlarkCodeMode(&codemcp.CodeModeConfig{ToolExecutionTimeout: 5 * time.Second, Limits: &schemas.MCPCodeModeLimits{MaxSteps: 1 << 40}}, nil)
	mode.clientManager = &testClientManager{}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	for _, tc := range []struct{ code, wantErr string }{
		{"result = len([0] * 200000000)", "exceeded memory allocation limits"},
		{"result = 'x' * 200000000", "exceeded memory allocation limits"},
		{"for i in range(1000):\n    print('x' * 1024)", "log limit exceeded"},
		{"result = 'x' * 2000000", "value exceeds size limit"},
		{"#" + strings.Repeat("x", 65536), "source limit"},
	} {
		code := tc.code
		result := mode.executeCode(ctx, code)
		if result.Errors == nil || !strings.Contains(result.Errors.Message, tc.wantErr) {
			t.Fatalf("expected %q for %.60s, got %+v", tc.wantErr, code, result.Errors)
		}
		next := mode.executeCode(ctx, "result = 6 * 7")
		if next.Errors != nil || next.Result != int64(42) {
			t.Fatalf("failed execution affected next request: %+v", next)
		}
	}
}

func TestCodeModeSandboxToolRoundTrip(t *testing.T) {
	var called bool
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := runSandbox(ctx, schemas.MCPCodeModeLimits{}.WithDefaults(), "result = server.echo(value=7)", map[string][]string{"server": {"server-echo"}}, func(_ context.Context, client, tool string, args map[string]interface{}, log func(string)) (interface{}, error) {
		called = true
		if client != "server" || tool != "server-echo" || args["value"] != int64(7) {
			t.Fatalf("wrong call: %s %s %+v", client, tool, args)
		}
		log("tool completed")
		return map[string]interface{}{"value": int64(7)}, nil
	})
	if !called || result.Errors != nil {
		t.Fatalf("tool round trip failed: %+v", result)
	}
	if got, ok := result.Result.(map[string]interface{}); !ok || got["value"] != int64(7) {
		t.Fatalf("result=%#v", result.Result)
	}
	if len(result.Logs) != 1 || result.Logs[0] != "tool completed" {
		t.Fatalf("logs=%v", result.Logs)
	}
}

func TestCodeModeSandboxCancellation(t *testing.T) {
	mode := NewStarlarkCodeMode(nil, nil)
	mode.clientManager = &testClientManager{}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	cancelledCtx := schemas.NewBifrostContext(cancelled, schemas.NoDeadline)
	// BifrostContext propagates cancellation asynchronously.
	select {
	case <-cancelledCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("context cancellation did not propagate")
	}
	result := mode.executeCode(cancelledCtx, "result = 42")
	if result.Errors == nil {
		t.Fatal("cancelled execution was accepted")
	}
}

// Executions parked in slow tool calls must not block or reject other executions.
func TestCodeModeSandboxNoGlobalConcurrencyCap(t *testing.T) {
	const executions = 1000
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entered := make(chan struct{}, executions)
	release := make(chan struct{})
	results := make(chan ExecutionResult, executions)
	for i := 0; i < executions; i++ {
		go func() {
			results <- runSandbox(ctx, schemas.MCPCodeModeLimits{}.WithDefaults(), "result = server.wait()", map[string][]string{"server": {"server-wait"}}, func(callCtx context.Context, _, _ string, _ map[string]interface{}, _ func(string)) (interface{}, error) {
				entered <- struct{}{}
				select {
				case <-release:
					return int64(1), nil
				case <-callCtx.Done():
					return nil, callCtx.Err()
				}
			})
		}()
	}
	for i := 0; i < executions; i++ {
		select {
		case <-entered:
		case result := <-results:
			close(release)
			t.Fatalf("execution finished before every execution reached its tool call: %+v", result.Errors)
		case <-ctx.Done():
			t.Fatal("executions did not run concurrently")
		}
	}
	close(release)
	for i := 0; i < executions; i++ {
		if result := <-results; result.Errors != nil || result.Result != int64(1) {
			t.Fatalf("concurrent execution failed: %+v", result.Errors)
		}
	}
}

func TestCodeModeAllocationRejectedBeforeExecution(t *testing.T) {
	mode := NewStarlarkCodeMode(nil, nil)
	mode.clientManager = &testClientManager{}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	result := mode.executeCode(ctx, "result = len([0] * 6000000)")
	if result.Errors == nil || (!strings.Contains(result.Errors.Message, "allocation") && !strings.Contains(result.Errors.Message, "steps")) {
		t.Fatalf("expected a computation or allocation budget error before allocating the list, got %+v", result.Errors)
	}
}

func TestCodeModeMemoryBudget(t *testing.T) {
	// Raise only the computation budget to exercise the independent memory
	// guard: the normal configuration may reject large operations on cost first.
	for _, code := range []string{
		"result = [0] * 6000000",
		"result = 'x' * 100000000",
	} {
		thread := newSandboxThread(context.Background(), schemas.MCPCodeModeLimits{}.WithDefaults())
		thread.SetMaxSteps(10_000_000_000)
		_, err := starlark.ExecFileOptions(starlarkOpts(), thread, "budget.star", code, nil)
		thread.Cancel("test finished")
		var allocationError *starlark.AllocsSafetyError
		if !errors.As(err, &allocationError) {
			t.Fatalf("expected allocation rejection for %s, got %v", code, err)
		}
	}
}

func TestCodeModeBoundedValues(t *testing.T) {
	for _, code := range []string{
		"result = 1 << 128\nfor i in range(6):\n    result *= result",
		"result = 1 << 1000000000",
		"result = []\nresult.append(result)",
		"result = {}\nresult['self'] = result",
		"result = [0]\nfor i in range(30):\n    result = [result, result]",
		"result = []\nfor i in range(100):\n    result = [result]",
		"result = '%1000000000s' % 'x'",
		"result = '{}'.format('x' * 1000000000)",
		"result = ''.join(['abc'] * 100000000)",
	} {
		result := runSandbox(context.Background(), schemas.MCPCodeModeLimits{}.WithDefaults(), code, nil, nil)
		if result.Errors == nil {
			t.Fatalf("expected bounded execution: %s", code)
		}
	}
}

func TestCodeModeRejectsUnmeteredBuiltin(t *testing.T) {
	thread := newSandboxThread(context.Background(), schemas.MCPCodeModeLimits{}.WithDefaults())
	defer thread.Cancel("test finished")
	called := false
	fn := starlark.NewBuiltin("unmetered", func(_ *starlark.Thread, _ *starlark.Builtin, _ starlark.Tuple, _ []starlark.Tuple) (starlark.Value, error) {
		called = true
		return starlark.None, nil
	})
	_, err := starlark.Call(thread, fn, nil, nil)
	if called || !errors.Is(err, starlark.ErrSafety) {
		t.Fatalf("unmetered builtin accepted: called=%v err=%v", called, err)
	}
}

func TestCodeModeToolResponseSize(t *testing.T) {
	oversized := strings.Repeat("x", schemas.DefaultCodeModeMaxValueBytes+1)
	for _, content := range [][]mcp.Content{
		{mcp.TextContent{Text: oversized}},
		{mcp.ImageContent{Data: oversized}},
		{mcp.AudioContent{Data: oversized}},
		{mcp.ResourceLink{URI: oversized}},
		{&mcp.TextContent{Text: oversized}},
		{mcp.TextContent{Text: oversized[:schemas.DefaultCodeModeMaxValueBytes/2]}, mcp.TextContent{Text: oversized[:schemas.DefaultCodeModeMaxValueBytes/2+1]}},
	} {
		_, err := extractTextFromMCPResponse(&mcp.CallToolResult{Content: content}, "echo", schemas.DefaultCodeModeMaxValueBytes)
		if err == nil {
			t.Fatal("oversized tool response accepted")
		}
	}
	thread := newSandboxThread(context.Background(), schemas.MCPCodeModeLimits{}.WithDefaults())
	defer thread.Cancel("test finished")
	thread.SetMaxAllocs(1024)
	if err := chargeToolResult(thread.Context(), 100); err == nil {
		t.Fatal("JSON decoding was not charged to execution budget")
	}
}

func TestCodeModeResourceLink(t *testing.T) {
	link := mcp.ResourceLink{Type: "resource_link", URI: "https://example.com/data", Name: "data"}
	want, err := schemas.MarshalSorted(link)
	if err != nil {
		t.Fatal(err)
	}
	for _, content := range []mcp.Content{link, &link} {
		got, err := extractTextFromMCPResponse(&mcp.CallToolResult{Content: []mcp.Content{content}}, "echo", schemas.DefaultCodeModeMaxValueBytes)
		if err != nil || got != string(want) {
			t.Fatalf("resource link changed: got=%s err=%v", got, err)
		}
	}
}

func TestCodeModeToolLogPreview(t *testing.T) {
	preview := formatResultForLog(strings.Repeat("x", 100_000))
	if len(preview) > 4096 || !strings.Contains(preview, "truncated") {
		t.Fatalf("unbounded tool log preview: %d bytes", len(preview))
	}
}

func TestCodeModeToolBoundaryLimits(t *testing.T) {
	bindings := map[string][]string{"server": {"server-echo"}}
	t.Run("cyclic arguments never call tool", func(t *testing.T) {
		called := false
		result := runSandbox(context.Background(), schemas.MCPCodeModeLimits{}.WithDefaults(), "a = []\na.append(a)\nresult = server.echo(value=a)", bindings,
			func(_ context.Context, _, _ string, _ map[string]interface{}, _ func(string)) (interface{}, error) {
				called = true
				return nil, nil
			})
		if called || result.Errors == nil {
			t.Fatalf("cyclic arguments escaped conversion: %+v", result)
		}
	})
	t.Run("tool results", func(t *testing.T) {
		cycle := map[string]interface{}{}
		cycle["self"] = cycle
		for _, value := range []interface{}{cycle, strings.Repeat("x", schemas.DefaultCodeModeMaxValueBytes+1), make([]interface{}, schemas.DefaultCodeModeMaxValueBytes)} {
			result := runSandbox(context.Background(), schemas.MCPCodeModeLimits{}.WithDefaults(), "result = server.echo()", bindings,
				func(_ context.Context, _, _ string, _ map[string]interface{}, _ func(string)) (interface{}, error) {
					return value, nil
				})
			if result.Errors == nil {
				t.Fatal("accepted an unbounded tool result")
			}
		}
	})
	t.Run("call count", func(t *testing.T) {
		calls := 0
		result := runSandbox(context.Background(), schemas.MCPCodeModeLimits{}.WithDefaults(), "for i in range(65):\n    server.echo()", bindings,
			func(_ context.Context, _, _ string, _ map[string]interface{}, _ func(string)) (interface{}, error) {
				calls++
				return nil, nil
			})
		if calls != 64 || result.Errors == nil || !strings.Contains(result.Errors.Message, "tool call limit") {
			t.Fatalf("calls=%d, result=%+v", calls, result)
		}
	})
}

func TestCodeModeConfigurableLimits(t *testing.T) {
	bindings := map[string][]string{"server": {"server-echo"}}
	echo := func(_ context.Context, _, _ string, _ map[string]interface{}, _ func(string)) (interface{}, error) {
		return int64(1), nil
	}
	nested := func(depth int) string {
		return "result = 1\nfor i in range(" + strconv.Itoa(depth) + "):\n    result = [result]"
	}
	for _, tc := range []struct {
		name    string
		limits  schemas.MCPCodeModeLimits
		code    string
		wantErr string // empty means the execution must succeed
	}{
		{name: "source lowered", limits: schemas.MCPCodeModeLimits{MaxSourceBytes: 16}, code: "result = 1 + 2 + 3 + 4", wantErr: "source limit"},
		{name: "steps lowered", limits: schemas.MCPCodeModeLimits{MaxSteps: 1000}, code: "for i in range(100000):\n    pass", wantErr: "steps"},
		{name: "memory lowered", limits: schemas.MCPCodeModeLimits{MaxMemoryBytes: 64 << 10}, code: "result = len([0] * 100000)", wantErr: "allocation"},
		{name: "logs lowered", limits: schemas.MCPCodeModeLimits{MaxLogBytes: 64}, code: "print('x' * 100)", wantErr: "log limit"},
		{name: "tool calls lowered", limits: schemas.MCPCodeModeLimits{MaxToolCalls: 2}, code: "for i in range(3):\n    server.echo()", wantErr: "tool call limit"},
		{name: "value lowered", limits: schemas.MCPCodeModeLimits{MaxValueBytes: 2048}, code: "result = 'x' * 4096", wantErr: "size limit"},
		{name: "depth lowered", limits: schemas.MCPCodeModeLimits{MaxNestingDepth: 3}, code: nested(5), wantErr: "nesting limit"},
		{name: "steps at default", limits: schemas.MCPCodeModeLimits{}, code: "for i in range(2000000):\n    pass", wantErr: "steps"},
		{name: "steps raised", limits: schemas.MCPCodeModeLimits{MaxSteps: 50_000_000}, code: "for i in range(2000000):\n    pass"},
		{name: "tool calls raised", limits: schemas.MCPCodeModeLimits{MaxToolCalls: 100}, code: "for i in range(65):\n    server.echo()"},
		{name: "value raised", limits: schemas.MCPCodeModeLimits{MaxValueBytes: 4 << 20, MaxMemoryBytes: 256 << 20, MaxSteps: 50_000_000}, code: "result = 'x' * 300000"},
		{name: "depth raised", limits: schemas.MCPCodeModeLimits{MaxNestingDepth: 100}, code: nested(80)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := runSandbox(context.Background(), tc.limits.WithDefaults(), tc.code, bindings, echo)
			if tc.wantErr == "" {
				if result.Errors != nil {
					t.Fatalf("expected success, got %+v", result.Errors)
				}
				return
			}
			if result.Errors == nil || !strings.Contains(result.Errors.Message, tc.wantErr) {
				t.Fatalf("expected %q error, got %+v", tc.wantErr, result.Errors)
			}
		})
	}
}

func TestStarlarkCodeModeLimitsHotReload(t *testing.T) {
	mode := NewStarlarkCodeMode(&codemcp.CodeModeConfig{Limits: &schemas.MCPCodeModeLimits{MaxSourceBytes: 16}}, nil)
	mode.clientManager = &testClientManager{}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	const code = "result = 1 + 2 + 3 + 4"
	if result := mode.executeCode(ctx, code); result.Errors == nil || !strings.Contains(result.Errors.Message, "source limit") {
		t.Fatalf("configured source limit not applied: %+v", result.Errors)
	}
	mode.UpdateConfig(&codemcp.CodeModeConfig{BindingLevel: schemas.CodeModeBindingLevelServer})
	if result := mode.executeCode(ctx, code); result.Errors == nil {
		t.Fatal("an update without limits must keep the configured limits")
	}
	mode.UpdateConfig(&codemcp.CodeModeConfig{Limits: &schemas.MCPCodeModeLimits{}})
	if result := mode.executeCode(ctx, code); result.Errors != nil || result.Result != int64(10) {
		t.Fatalf("zero limits must restore defaults: %+v", result)
	}
	for _, invalid := range []schemas.MCPCodeModeLimits{
		{MaxSourceBytes: 16, MaxSteps: -1},
		{MaxSourceBytes: 16, MaxNestingDepth: schemas.MaxCodeModeNestingDepth + 1},
	} {
		mode.UpdateConfig(&codemcp.CodeModeConfig{Limits: &invalid})
		if result := mode.executeCode(ctx, code); result.Errors != nil {
			t.Fatalf("invalid limits %+v must not replace the current limits: %+v", invalid, result.Errors)
		}
	}
	invalid := NewStarlarkCodeMode(&codemcp.CodeModeConfig{Limits: &schemas.MCPCodeModeLimits{MaxSourceBytes: 16, MaxToolCalls: -1}}, nil)
	invalid.clientManager = &testClientManager{}
	if result := invalid.executeCode(ctx, code); result.Errors != nil {
		t.Fatalf("invalid startup limits must fall back to defaults: %+v", result.Errors)
	}
}

func TestCodeModeToolLogsDoNotExhaustPrintBudget(t *testing.T) {
	bindings := map[string][]string{"server": {"server-echo"}}
	line := "[TOOL] server.echo raw response: " + strings.Repeat("x", 4000)
	calls := 0
	result := runSandbox(context.Background(), schemas.MCPCodeModeLimits{}.WithDefaults(), "for i in range(30):\n    server.echo()\nresult = 1", bindings,
		func(_ context.Context, _, _ string, _ map[string]interface{}, log func(string)) (interface{}, error) {
			calls++
			log(line)
			return int64(1), nil
		})
	if result.Errors != nil || result.Result != int64(1) || calls != 30 {
		t.Fatalf("tool diagnostics ended the execution: calls=%d result=%+v", calls, result.Errors)
	}
	markers := 0
	for _, l := range result.Logs {
		if strings.Contains(l, "further tool logs omitted") {
			markers++
		}
	}
	if markers != 1 {
		t.Fatalf("expected exactly one omission marker, got %d in %d log lines", markers, len(result.Logs))
	}
}

func TestCodeModeUnsupportedStatementHint(t *testing.T) {
	for _, code := range []string{
		"try:\n    x = 1\nexcept:\n    pass",
		"raise ValueError('x')",
		"x = 1\ntry:\n    x = 2\nfinally:\n    pass",
	} {
		result := runSandbox(context.Background(), schemas.MCPCodeModeLimits{}.WithDefaults(), code, nil, nil)
		if result.Errors == nil {
			t.Fatalf("expected a parse error for %q", code)
		}
		if !strings.Contains(strings.Join(result.Errors.Hints, "\n"), "does NOT support try/except") {
			t.Fatalf("missing try/except hint for %q: %+v", code, result.Errors)
		}
	}
}

func TestCodeModeInboundToolStringsChargedAtRawSize(t *testing.T) {
	bindings := map[string][]string{"server": {"server-big"}}
	big := strings.Repeat("x", 300_000)
	call := func(_ context.Context, _, _ string, _ map[string]interface{}, _ func(string)) (interface{}, error) {
		return map[string]interface{}{"body": big}, nil
	}
	limits := schemas.MCPCodeModeLimits{}.WithDefaults()
	if result := runSandbox(context.Background(), limits, "result = len(server.big()['body'])", bindings, call); result.Errors != nil || result.Result != int64(300_000) {
		t.Fatalf("a tool string within the 1 MiB budget was rejected: %+v", result.Errors)
	}
	if result := runSandbox(context.Background(), limits, "result = server.big()['body']", bindings, call); result.Errors == nil || !strings.Contains(result.Errors.Message, "size limit") {
		t.Fatalf("returning the string must still be charged for JSON escaping: %+v", result.Errors)
	}
}

func TestCodeModeDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	result := runSandbox(ctx, schemas.MCPCodeModeLimits{}.WithDefaults(), "while True:\n    pass", nil, nil)
	if result.Errors == nil || !strings.Contains(result.Errors.Message, "deadline") {
		t.Fatalf("expected deadline error: %+v", result)
	}
}

func BenchmarkCodeModeExecution(b *testing.B) {
	for _, tc := range []struct{ name, code string }{
		{"scalar", "result = 6 * 7"},
		{"transform", "result = [x * 2 for x in range(100)]"},
		{"tool", "result = server.echo(value=7)"},
	} {
		b.Run(tc.name, func(b *testing.B) {
			bindings := map[string][]string{"server": {"server-echo"}}
			call := func(_ context.Context, _, _ string, args map[string]interface{}, _ func(string)) (interface{}, error) {
				return args, nil
			}
			b.ReportAllocs()
			for b.Loop() {
				result := runSandbox(context.Background(), schemas.MCPCodeModeLimits{}.WithDefaults(), tc.code, bindings, call)
				if result.Errors != nil {
					b.Fatal(result.Errors.Message)
				}
			}
		})
	}
}

func mustStarlarkToGo(t *testing.T, v starlark.Value) interface{} {
	t.Helper()
	thread := newSandboxThread(context.Background(), schemas.MCPCodeModeLimits{}.WithDefaults())
	defer thread.Cancel("test finished")
	result, err := newValueConversion(thread).toGo(v, 0)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func mustGoToStarlark(t *testing.T, v interface{}) starlark.Value {
	t.Helper()
	thread := newSandboxThread(context.Background(), schemas.MCPCodeModeLimits{}.WithDefaults())
	defer thread.Cancel("test finished")
	result, err := newValueConversion(thread).fromGo(v, 0)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func BenchmarkCodeModeInProcess(b *testing.B) {
	for _, tc := range []struct{ name, code string }{
		{"scalar", "result = 6 * 7"},
		{"transform", "result = [x * 2 for x in range(100)]"},
	} {
		b.Run(tc.name, func(b *testing.B) {
			mode := NewStarlarkCodeMode(nil, nil)
			mode.clientManager = &testClientManager{}
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			b.ReportAllocs()
			for b.Loop() {
				result := mode.executeCode(ctx, tc.code)
				if result.Errors != nil {
					b.Fatal(result.Errors.Message)
				}
			}
		})
	}
}
