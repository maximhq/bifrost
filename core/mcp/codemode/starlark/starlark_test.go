//go:build !tinygo && !wasm

package starlark

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bytedance/sonic"
	"github.com/canonical/starlark/starlark"
	"github.com/canonical/starlark/syntax"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	codemcp "github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
)

type testClientManager struct {
	perClientCap int
	clients      map[string]*schemas.MCPClientState
	tools        map[string][]schemas.ChatTool
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

func (m *testClientManager) InstructionCaps() codemcp.InstructionCaps {
	return codemcp.InstructionCaps{PerClient: m.perClientCap}
}

func (m *testClientManager) GetServerInstructions(_ context.Context) []schemas.MCPServerInstructions {
	return nil
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

// Some servers wrap their JSON payload in a JSON string. One extra decode unwraps an object or
// array; anything else stays what the first decode produced.
func TestDecodeToolResult(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want interface{}
	}{
		{name: "wrapped object", raw: `"{\"title\":\"Example Domain\"}"`, want: map[string]interface{}{"title": "Example Domain"}},
		{name: "wrapped array", raw: `" [1, 2]"`, want: []interface{}{float64(1), float64(2)}},
		{name: "quoted scalar stays a string", raw: `"123"`, want: "123"},
		{name: "quoted invalid JSON stays a string", raw: `"{not json"`, want: "{not json"},
		{name: "a second wrap is not unwrapped", raw: `"\"{\\\"a\\\":1}\""`, want: `"{\"a\":1}"`},
		{name: "plain text stays as sent", raw: "not json at all", want: "not json at all"},
		{name: "bare object", raw: `{"a":1}`, want: map[string]interface{}{"a": float64(1)}},
		{name: "bare number", raw: `42`, want: float64(42)},
		{name: "bare bool", raw: `true`, want: true},
		{name: "bare null", raw: `null`, want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := decodeToolResult(tc.raw)
			gotJSON, _ := sonic.Marshal(got)
			wantJSON, _ := sonic.Marshal(tc.want)
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("decodeToolResult(%s) = %#v, want %#v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestExtractTextFromMCPResponseResultShapes(t *testing.T) {
	limit := schemas.DefaultCodeModeMaxValueBytes
	for _, tc := range []struct {
		name   string
		result *mcp.CallToolResult
		want   string
	}{
		{
			name:   "several JSON blocks become one array",
			result: &mcp.CallToolResult{Content: []mcp.Content{mcp.NewTextContent(`{"id":1}`), &mcp.TextContent{Type: "text", Text: `{"id":2}`}}},
			want:   `[{"id":1},{"id":2}]`,
		},
		{
			name:   "mixed blocks are joined with newlines",
			result: &mcp.CallToolResult{Content: []mcp.Content{mcp.NewTextContent(`{"id":1}`), mcp.NewTextContent("Found 2 files")}},
			want:   "{\"id\":1}\nFound 2 files",
		},
		{
			name:   "text next to an image keeps its own line",
			result: &mcp.CallToolResult{Content: []mcp.Content{mcp.NewTextContent("Found 2 files"), mcp.ImageContent{Data: "AAAA", MIMEType: "image/png"}}},
			want:   "Found 2 files\n[Image Response: AAAA, MIME: image/png]",
		},
		{
			name:   "a single block is passed through",
			result: &mcp.CallToolResult{Content: []mcp.Content{mcp.NewTextContent(`{"id":1}`)}},
			want:   `{"id":1}`,
		},
		{
			name: "structured content wins over its text copy",
			result: &mcp.CallToolResult{
				Content:           []mcp.Content{mcp.NewTextContent(`{"title":"from text"}`)},
				StructuredContent: map[string]any{"title": "from structured"},
			},
			want: `{"title":"from structured"}`,
		},
		{
			name: "an error result keeps its text",
			result: &mcp.CallToolResult{
				Content:           []mcp.Content{mcp.NewTextContent("quota exhausted")},
				StructuredContent: map[string]any{"code": 429},
				IsError:           true,
			},
			want: "quota exhausted",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := extractTextFromMCPResponse(tc.result, "echo", limit)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}

	t.Run("oversized structured content is rejected", func(t *testing.T) {
		result := &mcp.CallToolResult{StructuredContent: map[string]any{"body": strings.Repeat("x", limit)}}
		if _, err := extractTextFromMCPResponse(result, "echo", limit); err == nil {
			t.Fatal("oversized structured content accepted")
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

// json and math are predeclared so a script can parse a string result itself; a server bound
// under either name keeps that name.
func TestCodeModeSandboxJSONAndMathModules(t *testing.T) {
	limits := schemas.MCPCodeModeLimits{}.WithDefaults()
	returning := func(value interface{}) sandboxToolCaller {
		return func(context.Context, string, string, map[string]interface{}, func(string)) (interface{}, error) {
			return value, nil
		}
	}

	t.Run("decode, encode and math are available", func(t *testing.T) {
		code := "parsed = json.decode(server.echo())\nresult = [len(parsed[\"items\"]), json.encode({\"a\": 1}), math.floor(2.7)]"
		result := runSandbox(context.Background(), limits, code, map[string][]string{"server": {"server-echo"}}, returning(`{"items":[1,2,3]}`))
		if result.Errors != nil {
			t.Fatalf("modules unavailable: %s", result.Errors.Message)
		}
		got, _ := sonic.Marshal(result.Result)
		if string(got) != `[3,"{\"a\":1}",2]` {
			t.Fatalf("result=%s", got)
		}
	})

	t.Run("a server named json keeps its binding", func(t *testing.T) {
		result := runSandbox(context.Background(), limits, "result = [json.echo(), math.floor(1.5)]", map[string][]string{"json": {"json-echo"}}, returning("from the server"))
		if result.Errors != nil {
			t.Fatalf("server binding lost: %s", result.Errors.Message)
		}
		got, _ := sonic.Marshal(result.Result)
		if string(got) != `["from the server",1]` {
			t.Fatalf("result=%s", got)
		}
	})

	t.Run("decoding is charged to the memory budget", func(t *testing.T) {
		small := limits
		small.MaxMemoryBytes = 1 << 20
		payload := "[" + strings.TrimSuffix(strings.Repeat("1,", 200_000), ",") + "]"
		bindings := map[string][]string{"server": {"server-echo"}}
		// Control: the payload itself fits, so the decode is what exceeds the budget.
		if result := runSandbox(context.Background(), small, "result = len(server.echo())", bindings, returning(payload)); result.Errors != nil {
			t.Fatalf("the payload alone exceeded the budget: %s", result.Errors.Message)
		}
		result := runSandbox(context.Background(), small, "result = len(json.decode(server.echo()))", bindings, returning(payload))
		if result.Errors == nil || !strings.Contains(result.Errors.Message, "exceeded memory allocation limits") {
			t.Fatalf("a 200,000-element decode was not charged to a 1 MiB budget: %+v", result)
		}
	})
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

// A tool's error text is raised into the script and reaches the model in the reply, so it stays
// bounded and valid UTF-8 however long the server's message is.
func TestCodeModeToolErrorTextIsBounded(t *testing.T) {
	for _, text := range []string{strings.Repeat("x", 100_000), strings.Repeat("é", 2048)} {
		got := truncateDiagnostic(text)
		if len(got) > codeModeDiagnosticBytes || !strings.Contains(got, "truncated") || !utf8.ValidString(got) {
			t.Fatalf("unbounded or invalid error text: %d bytes", len(got))
		}
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
		hints := strings.Join(result.Errors.Hints, "\n")
		if !strings.Contains(hints, "does NOT support try/except") {
			t.Fatalf("missing try/except hint for %q: %+v", code, result.Errors)
		}
		// The parse error says nothing about dicts or tool calls, so generic advice only buries the hint.
		if strings.Contains(hints, "Access dict values with brackets") {
			t.Fatalf("generic hints follow the try/except hint for %q:\n%s", code, hints)
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

// Upstream server instructions on the code mode surface: the stub is what the workflow tells the
// model to re-read before executeToolCode, so the server's rules ride with the server.

const probeInstructions = "Always call list_items before mutating.\n\nNever retry a write."

func newInstructionsCodeMode(level schemas.CodeModeBindingLevel) *StarlarkCodeMode {
	mode := NewStarlarkCodeMode(&codemcp.CodeModeConfig{
		BindingLevel:         level,
		ToolExecutionTimeout: time.Second,
	}, nil)
	mode.clientManager = &testClientManager{
		clients: map[string]*schemas.MCPClientState{
			"codey": {
				Name:               "codey",
				ExecutionConfig:    &schemas.MCPClientConfig{Name: "codey", IsCodeModeClient: true},
				ServerInstructions: probeInstructions,
			},
			"plain": {
				Name:            "plain",
				ExecutionConfig: &schemas.MCPClientConfig{Name: "plain", IsCodeModeClient: true},
			},
		},
		tools: map[string][]schemas.ChatTool{
			"codey": {{Function: &schemas.ChatToolFunction{Name: "codey-add", Description: schemas.Ptr("Adds two numbers")}}},
			"plain": {{Function: &schemas.ChatToolFunction{Name: "plain-echo"}}},
		},
	}
	return mode
}

func instructionsToolResponse(t *testing.T, msg *schemas.ChatMessage, err error) string {
	t.Helper()
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if msg == nil || msg.Content == nil || msg.Content.ContentStr == nil {
		t.Fatal("expected tool response content")
	}
	return *msg.Content.ContentStr
}

func TestReadToolFileCarriesServerInstructions(t *testing.T) {
	mode := newInstructionsCodeMode(schemas.CodeModeBindingLevelServer)

	msg, err := mode.handleReadToolFile(context.Background(), schemas.ChatAssistantMessageToolCall{
		ID:       schemas.Ptr("tool-call-1"),
		Function: schemas.ChatAssistantMessageToolCallFunction{Arguments: `{"fileName":"servers/codey.pyi"}`},
	})
	content := instructionsToolResponse(t, msg, err)

	if !strings.Contains(content, "# Always call list_items before mutating.") {
		t.Fatalf("stub dropped the server's instructions, got:\n%s", content)
	}
	if !strings.Contains(content, "# ---- server instructions (from the upstream handshake) ----") {
		t.Fatalf("stub carried the text without a delimiter, got:\n%s", content)
	}
	// The block has to sit above the signatures it constrains, not after them.
	if strings.Index(content, "server instructions") > strings.Index(content, "def add(") {
		t.Fatalf("instructions landed below the signatures, got:\n%s", content)
	}
}

func TestReadToolFileOmitsBlockForServerWithNoInstructions(t *testing.T) {
	mode := newInstructionsCodeMode(schemas.CodeModeBindingLevelServer)

	msg, err := mode.handleReadToolFile(context.Background(), schemas.ChatAssistantMessageToolCall{
		ID:       schemas.Ptr("tool-call-1"),
		Function: schemas.ChatAssistantMessageToolCallFunction{Arguments: `{"fileName":"servers/plain.pyi"}`},
	})
	content := instructionsToolResponse(t, msg, err)

	if strings.Contains(content, "server instructions") {
		t.Fatalf("expected no block at all for a server advertising none, got:\n%s", content)
	}
}

// At tool binding level there is no servers/<name>.pyi, so the per-tool file is the only place
// the server's rules can reach the model.
func TestReadToolFileCarriesServerInstructionsAtToolBindingLevel(t *testing.T) {
	mode := newInstructionsCodeMode(schemas.CodeModeBindingLevelTool)

	msg, err := mode.handleReadToolFile(context.Background(), schemas.ChatAssistantMessageToolCall{
		ID:       schemas.Ptr("tool-call-1"),
		Function: schemas.ChatAssistantMessageToolCallFunction{Arguments: `{"fileName":"servers/codey/add.pyi"}`},
	})
	content := instructionsToolResponse(t, msg, err)

	if !strings.Contains(content, "# Always call list_items before mutating.") {
		t.Fatalf("tool-level stub dropped the server's instructions, got:\n%s", content)
	}
}

// The listing is an index: it names which servers carry instructions, never the prose itself.
func TestListToolFilesPointsAtServersWithInstructionsWithoutTheirText(t *testing.T) {
	mode := newInstructionsCodeMode(schemas.CodeModeBindingLevelServer)

	msg, err := mode.handleListToolFiles(context.Background(), schemas.ChatAssistantMessageToolCall{
		ID: schemas.Ptr("tool-call-1"),
	})
	content := instructionsToolResponse(t, msg, err)

	if !strings.Contains(content, "Servers with their own usage instructions: codey.") {
		t.Fatalf("listing did not point at the server carrying instructions, got:\n%s", content)
	}
	if strings.Contains(content, "plain.") && strings.Contains(content, "instructions: codey, plain") {
		t.Fatalf("listing named a server that advertises none, got:\n%s", content)
	}
	if strings.Contains(content, "Always call list_items") {
		t.Fatalf("listing carried the instruction prose instead of a pointer, got:\n%s", content)
	}
}

// A non-code-mode client is unreachable from the sandbox, so naming it would advertise tools this
// surface cannot call.
func TestListToolFilesOmitsNonCodeModeServersFromInstructionPointer(t *testing.T) {
	mode := newInstructionsCodeMode(schemas.CodeModeBindingLevelServer)
	manager := mode.clientManager.(*testClientManager)
	manager.clients["direct"] = &schemas.MCPClientState{
		Name:               "direct",
		ExecutionConfig:    &schemas.MCPClientConfig{Name: "direct", IsCodeModeClient: false},
		ServerInstructions: "Direct-only rule.",
	}
	manager.tools["direct"] = []schemas.ChatTool{{Function: &schemas.ChatToolFunction{Name: "direct-ping"}}}

	msg, err := mode.handleListToolFiles(context.Background(), schemas.ChatAssistantMessageToolCall{
		ID: schemas.Ptr("tool-call-1"),
	})
	content := instructionsToolResponse(t, msg, err)

	if strings.Contains(content, "direct") {
		t.Fatalf("listing named a non-code-mode server, got:\n%s", content)
	}
}

func TestGetToolDocsCarriesServerInstructions(t *testing.T) {
	mode := newInstructionsCodeMode(schemas.CodeModeBindingLevelServer)

	msg, err := mode.handleGetToolDocs(context.Background(), schemas.ChatAssistantMessageToolCall{
		ID:       schemas.Ptr("tool-call-1"),
		Function: schemas.ChatAssistantMessageToolCallFunction{Arguments: `{"server":"codey","tool":"add"}`},
	})
	content := instructionsToolResponse(t, msg, err)

	if !strings.Contains(content, "# Always call list_items before mutating.") {
		t.Fatalf("getToolDocs dropped the server's instructions, got:\n%s", content)
	}
}

// The stub's byte bound comes from the client override, else the configured global — never the
// compile-time default, which would silently exceed a lowered global or cut short a raised one.

func readGithubStub(t *testing.T, mode *StarlarkCodeMode) string {
	t.Helper()
	msg, err := mode.handleReadToolFile(context.Background(), schemas.ChatAssistantMessageToolCall{
		ID:       schemas.Ptr("tool-call-1"),
		Function: schemas.ChatAssistantMessageToolCallFunction{Arguments: `{"fileName":"servers/codey.pyi"}`},
	})
	return instructionsToolResponse(t, msg, err)
}

func TestReadToolFileHonorsConfiguredGlobalInstructionCap(t *testing.T) {
	mode := newInstructionsCodeMode(schemas.CodeModeBindingLevelServer)
	mode.clientManager.(*testClientManager).perClientCap = 20

	content := readGithubStub(t, mode)

	if !strings.Contains(content, "[truncated:") {
		t.Fatalf("a lowered global cap was ignored, got:\n%s", content)
	}
	if strings.Contains(content, "Never retry a write") {
		t.Fatalf("text past the global cap reached the stub, got:\n%s", content)
	}
}

func TestReadToolFileClientCapOverridesConfiguredGlobal(t *testing.T) {
	mode := newInstructionsCodeMode(schemas.CodeModeBindingLevelServer)
	manager := mode.clientManager.(*testClientManager)
	manager.perClientCap = 20
	manager.clients["codey"].ExecutionConfig.MaxInstructionsLength = 1000

	content := readGithubStub(t, mode)

	if strings.Contains(content, "[truncated:") {
		t.Fatalf("the client's own larger cap was not honoured, got:\n%s", content)
	}
	if !strings.Contains(content, "Never retry a write") {
		t.Fatalf("stub lost text the client's cap allows, got:\n%s", content)
	}
}

// A per-call client (per-user OAuth, per-user headers, token exchange) gets its tools back from
// the config store after a restart instead of from a live tools/list. That decode turns every
// nested schema object into *OrderedMap, where live discovery keeps plain maps, and the stubs
// must render both the same way.

func searchToolSchema() schemas.ChatTool {
	return schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        "codey-search",
			Description: schemas.Ptr("Searches the index"),
			Parameters: &schemas.ToolFunctionParameters{
				Type: "object",
				// A shallow wrap of plain maps, as live discovery builds it.
				Properties: schemas.OrderedMapFromMap(map[string]interface{}{
					"query": map[string]interface{}{"type": "string", "description": "Text to search for"},
					"limit": map[string]interface{}{"type": "integer"},
					"tags":  map[string]interface{}{"type": "array", "items": map[string]interface{}{"type": "string"}},
				}),
				Required: []string{"query"},
			},
		},
	}
}

// restoreDiscoveredTool round-trips a tool the way the config store persists DiscoveredTools:
// json.Marshal on save, sonic.Unmarshal on load (framework/configstore/tables/mcp.go).
func restoreDiscoveredTool(t *testing.T, tool schemas.ChatTool) schemas.ChatTool {
	t.Helper()
	data, err := json.Marshal(map[string]schemas.ChatTool{tool.Function.Name: tool})
	if err != nil {
		t.Fatal(err)
	}
	var restored map[string]schemas.ChatTool
	if err := sonic.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	return restored[tool.Function.Name]
}

func newSearchCodeMode(tool schemas.ChatTool) *StarlarkCodeMode {
	mode := NewStarlarkCodeMode(&codemcp.CodeModeConfig{
		BindingLevel:         schemas.CodeModeBindingLevelServer,
		ToolExecutionTimeout: time.Second,
	}, nil)
	mode.clientManager = &testClientManager{
		clients: map[string]*schemas.MCPClientState{
			"codey": {Name: "codey", ExecutionConfig: &schemas.MCPClientConfig{Name: "codey", IsCodeModeClient: true}},
		},
		tools: map[string][]schemas.ChatTool{"codey": {tool}},
	}
	return mode
}

func readSearchStubs(t *testing.T, mode *StarlarkCodeMode) (stub, docs string) {
	t.Helper()
	msg, err := mode.handleReadToolFile(context.Background(), schemas.ChatAssistantMessageToolCall{
		ID:       schemas.Ptr("tool-call-1"),
		Function: schemas.ChatAssistantMessageToolCallFunction{Arguments: `{"fileName":"servers/codey.pyi"}`},
	})
	stub = instructionsToolResponse(t, msg, err)
	msg, err = mode.handleGetToolDocs(context.Background(), schemas.ChatAssistantMessageToolCall{
		ID:       schemas.Ptr("tool-call-2"),
		Function: schemas.ChatAssistantMessageToolCallFunction{Arguments: `{"server":"codey","tool":"search"}`},
	})
	docs = instructionsToolResponse(t, msg, err)
	return stub, docs
}

func TestStubsKeepParametersOfRestoredTools(t *testing.T) {
	restored := restoreDiscoveredTool(t, searchToolSchema())
	// Guard the reproduction itself: the property must have the shape a restart produces.
	prop, _ := restored.Function.Parameters.Properties.Get("tags")
	if _, ok := prop.(*schemas.OrderedMap); !ok {
		t.Fatalf("restore did not produce *OrderedMap property schemas, got %T", prop)
	}

	stub, docs := readSearchStubs(t, newSearchCodeMode(restored))

	if !strings.Contains(stub, "def search(query: str, limit: int = None, tags: list[str] = None) -> dict") {
		t.Fatalf("readToolFile lost the restored tool's parameters, got:\n%s", stub)
	}
	for _, want := range []string{
		"def search(query: str, limit: int = None, tags: list[str] = None) -> dict:",
		"query (str): Text to search for (required)",
		"tags (list[str]): tags parameter (optional)",
	} {
		if !strings.Contains(docs, want) {
			t.Fatalf("getToolDocs is missing %q for the restored tool, got:\n%s", want, docs)
		}
	}
}

func TestStubsRenderFreshAndRestoredToolsIdentically(t *testing.T) {
	freshStub, freshDocs := readSearchStubs(t, newSearchCodeMode(searchToolSchema()))
	restoredStub, restoredDocs := readSearchStubs(t, newSearchCodeMode(restoreDiscoveredTool(t, searchToolSchema())))

	if freshStub != restoredStub {
		t.Fatalf("readToolFile differs after a restore:\nfresh:\n%s\nrestored:\n%s", freshStub, restoredStub)
	}
	if freshDocs != restoredDocs {
		t.Fatalf("getToolDocs differs after a restore:\nfresh:\n%s\nrestored:\n%s", freshDocs, restoredDocs)
	}
}

func BenchmarkFormatPythonParams(b *testing.B) {
	data, err := json.Marshal(map[string]schemas.ChatTool{"codey-search": searchToolSchema()})
	if err != nil {
		b.Fatal(err)
	}
	var restored map[string]schemas.ChatTool
	if err := sonic.Unmarshal(data, &restored); err != nil {
		b.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		params *schemas.ToolFunctionParameters
	}{
		{"fresh", searchToolSchema().Function.Parameters},
		{"restored", restored["codey-search"].Function.Parameters},
	} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = formatPythonParams(tc.params)
			}
		})
	}
}

// The stubs describe the result shapes a script really gets, and steer away from printing a
// whole response into the model's context.
func TestStubsDescribeResultShapes(t *testing.T) {
	stub, docs := readSearchStubs(t, newSearchCodeMode(searchToolSchema()))

	for name, content := range map[string]string{"readToolFile": stub, "getToolDocs": docs} {
		if !strings.Contains(content, "json.decode(") {
			t.Errorf("%s does not say how to parse a str result, got:\n%s", name, content)
		}
		if !strings.Contains(content, "number, bool or None") {
			t.Errorf("%s does not say a bare JSON value arrives as a scalar, got:\n%s", name, content)
		}
		// Most str results are plain text, and json.decode raises on those.
		if !strings.Contains(content, "plain text raises") {
			t.Errorf("%s does not limit json.decode to a str holding JSON, got:\n%s", name, content)
		}
	}
	if strings.Contains(docs, "dict: Response from the tool") {
		t.Errorf("getToolDocs still labels every response a dict, got:\n%s", docs)
	}
	if strings.Contains(docs, "Tool responses are dicts") {
		t.Errorf("getToolDocs still claims every response is a dict, got:\n%s", docs)
	}
	if strings.Contains(docs, "print(result)") {
		t.Errorf("getToolDocs still tells the model to print a whole response, got:\n%s", docs)
	}
}

// A code mode server bound as "json" keeps that name in the sandbox, so the stubs must not point
// the model at a json module its scripts cannot reach.
func TestStubsOmitJSONDecodeWhenAServerTakesTheName(t *testing.T) {
	mode := newSearchCodeMode(searchToolSchema())
	manager := mode.clientManager.(*testClientManager)
	manager.clients["json"] = &schemas.MCPClientState{Name: "json", ExecutionConfig: &schemas.MCPClientConfig{Name: "json", IsCodeModeClient: true}}
	manager.tools["json"] = []schemas.ChatTool{{Function: &schemas.ChatToolFunction{Name: "json-lint"}}}

	stub, docs := readSearchStubs(t, mode)

	for name, content := range map[string]string{"readToolFile": stub, "getToolDocs": docs} {
		if strings.Contains(content, "json.decode(") {
			t.Errorf("%s points at a json module a server shadows, got:\n%s", name, content)
		}
		if !strings.Contains(content, "number, bool or None") {
			t.Errorf("%s does not say a bare JSON value arrives as a scalar, got:\n%s", name, content)
		}
	}
}

// Indexing a str is the error a model hits when it trusts a "dict" it never checked.
func TestGeneratePythonErrorHintsForStringResults(t *testing.T) {
	for _, message := range []string{"string index: got string, want int", "string has no .get field or method"} {
		hints := strings.Join(generatePythonErrorHints(message, []string{"server"}), "\n")
		if !strings.Contains(hints, "json.decode(") || strings.Contains(hints, "brackets") {
			t.Errorf("hints for %q do not explain a str result:\n%s", message, hints)
		}
		shadowed := strings.Join(generatePythonErrorHints(message, []string{"json", "server"}), "\n")
		if strings.Contains(shadowed, "json.decode(") {
			t.Errorf("hints for %q point at a json module a server shadows:\n%s", message, shadowed)
		}
	}
}

func BenchmarkExtractResultFromChatMessage(b *testing.B) {
	for _, tc := range []struct{ name, text string }{
		{"bare-object", `{"title":"Example Domain","url":"https://example.com","items":[1,2,3]}`},
		{"wrapped-object", `"{\"title\":\"Example Domain\",\"url\":\"https://example.com\",\"items\":[1,2,3]}"`},
		{"plain-text", "Found 3 results for the query, see the attached list"},
	} {
		b.Run(tc.name, func(b *testing.B) {
			msg := &schemas.ChatMessage{Content: &schemas.ChatMessageContent{ContentStr: &tc.text}}
			b.ReportAllocs()
			for b.Loop() {
				_ = extractResultFromChatMessage(msg)
			}
		})
	}
}

// Each hint is keyed on a message Starlark actually produces, so every case runs a real script
// rather than a hand-typed message that could drift from the interpreter's wording.
func TestGeneratePythonErrorHintsMatchStarlarkMessages(t *testing.T) {
	limits := schemas.MCPCodeModeLimits{}.WithDefaults()
	server := map[string][]string{"srv": {"srv-get"}}
	returning := func(value interface{}, err error) sandboxToolCaller {
		return func(context.Context, string, string, map[string]interface{}, func(string)) (interface{}, error) {
			return value, err
		}
	}

	for _, tc := range []struct {
		name     string
		code     string
		bindings map[string][]string
		call     sandboxToolCaller
		want     []string
		notWant  []string
	}{
		{
			name: "a missing dict key", code: "d = {\"a\": 1}\nx = d[\"x\"]",
			want: []string{`The dict has no key "x"`, `r.get("x", default)`}, notWant: []string{"Check the error message above"},
		},
		{
			name: "a tool error that mentions a key", code: "r = srv.get()", bindings: server, call: returning(nil, errors.New("API key not found")),
			notWant: []string{"Dictionary key", "dict has no key"},
		},
		{
			name: "an unknown tool", code: "r = srv.gett()", bindings: server,
			want: []string{"srv has no tool gett", "readToolFile"}, notWant: []string{"Use dict access syntax"},
		},
		{
			name: "an attribute read on a dict", code: "d = {\"a\": 1}\nx = d.a",
			want: []string{`r["a"] or r.get("a", default)`},
		},
		{
			name: "a function a module lacks", code: "x = json.parse(\"{}\")",
			want: []string{"The json module has decode, encode, indent", "floor"},
		},
		{
			name: "a server bound under a module's name", code: "x = json.parse()", bindings: map[string][]string{"json": {"json-get"}},
			want: []string{"json has no tool parse"}, notWant: []string{"json module"},
		},
		{
			name: "a str method Starlark lacks", code: "x = \"name\".ljust(10)",
			want: []string{"str has no .ljust method"}, notWant: []string{"json.decode", "server's text"},
		},
		{
			name: "a dict method called on a str result", code: "r = srv.get()\nx = r.get(\"a\")", bindings: server, call: returning(`{"a": 1}`, nil),
			want: []string{"json.decode("},
		},
		{
			name: "indexing a str result", code: "r = srv.get()\nx = r[\"a\"]", bindings: server, call: returning(`{"a": 1}`, nil),
			want: []string{"json.decode("},
		},
		{
			name: "an attribute read on a failed call", code: "r = srv.get()\nx = r.email", bindings: server, call: returning(toolErrorValue{message: "not found"}, nil),
			want: []string{`hasattr(r, "error")`}, notWant: []string{"has no tool"},
		},
		{
			name: "an undefined name", code: "x = youtube.search()", bindings: server,
			want: []string{"Variable 'youtube' is not defined."},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := runSandbox(context.Background(), limits, tc.code, tc.bindings, tc.call)
			if result.Errors == nil {
				t.Fatalf("expected an error, got result %v", result.Result)
			}
			hints := strings.Join(result.Errors.Hints, "\n")
			for _, want := range tc.want {
				if !strings.Contains(hints, want) {
					t.Errorf("hints for %q lack %q:\n%s", result.Errors.Message, want, hints)
				}
			}
			for _, notWant := range tc.notWant {
				if strings.Contains(hints, notWant) {
					t.Errorf("hints for %q contain %q:\n%s", result.Errors.Message, notWant, hints)
				}
			}
		})
	}

	// A name can come from tool data through getattr. The error line already carries it once, so a
	// hint that repeated it would multiply an oversized reply; a long name gets the generic hint.
	longName := returning(strings.Repeat("b", 100_000), nil)
	for _, tc := range []struct{ name, code string }{
		{name: "a long attribute name on a dict", code: "n = srv.get()\nx = getattr({\"a\": 1}, n)"},
		{name: "a long attribute name on a server", code: "n = srv.get()\nx = getattr(srv, n)"},
		{name: "a long method name on a str", code: "n = srv.get()\nx = getattr(\"s\", n)"},
		{name: "a long missing key", code: "n = srv.get()\nx = {\"a\": 1}[n]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := runSandbox(context.Background(), limits, tc.code, server, longName)
			if result.Errors == nil {
				t.Fatalf("expected an error, got result %v", result.Result)
			}
			if hints := strings.Join(result.Errors.Hints, "\n"); len(hints) > 1024 {
				t.Fatalf("hints repeat the long name: %d bytes, starting %q", len(hints), hints[:200])
			}
		})
	}
}

// getToolDocs describes the sandbox a script runs in: top-level control flow works, and the
// common errors it quotes are messages Starlark really produces.
func TestGetToolDocsMatchesTheSandbox(t *testing.T) {
	_, docs := readSearchStubs(t, newSearchCodeMode(searchToolSchema()))
	if strings.Contains(docs, "MUST be inside a function") {
		t.Errorf("getToolDocs still says top-level loops must be wrapped in a function:\n%s", docs)
	}
	if !strings.Contains(docs, `key "x" not in dict`) {
		t.Errorf("getToolDocs does not quote Starlark's missing-key message:\n%s", docs)
	}

	code := "total = 0\nfor i in range(3):\n    total += i\nif total == 3:\n    total += 10\nn = 0\nwhile n < 2:\n    n += 1\nresult = [total, n]"
	result := runSandbox(context.Background(), schemas.MCPCodeModeLimits{}.WithDefaults(), code, nil, nil)
	if result.Errors != nil {
		t.Fatalf("top-level for/if/while failed: %s", result.Errors.Message)
	}
}

// A failed nested call reaches the script as a tool_error struct: hasattr finds .error, and
// returned as-is it converts back to {"error": ...}. Using it as a dict raises with a hint.
func TestCodeModeFailedCallIsAToolErrorValue(t *testing.T) {
	limits := schemas.MCPCodeModeLimits{}.WithDefaults()
	bindings := map[string][]string{"srv": {"srv-get"}}
	failing := func(context.Context, string, string, map[string]interface{}, func(string)) (interface{}, error) {
		return toolErrorValue{message: "contact 7 not found"}, nil
	}

	for _, tc := range []struct {
		name string
		code string
		want string
	}{
		{name: "detected and read", code: "r = srv.get()\nresult = [hasattr(r, \"error\"), r.error, type(r)]", want: `[true,"contact 7 not found","struct"]`},
		{name: "returned as-is", code: "result = srv.get()", want: `{"error":"contact 7 not found"}`},
		{name: "a dict result has no error attribute", code: "result = hasattr({\"error\": \"x\"}, \"error\")", want: `false`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := runSandbox(context.Background(), limits, tc.code, bindings, failing)
			if result.Errors != nil {
				t.Fatalf("unexpected error: %s", result.Errors.Message)
			}
			got, _ := sonic.Marshal(result.Result)
			if string(got) != tc.want {
				t.Fatalf("result=%s, want %s", got, tc.want)
			}
		})
	}

	t.Run("indexing it raises with a hint", func(t *testing.T) {
		result := runSandbox(context.Background(), limits, "r = srv.get()\nresult = r[\"email\"]", bindings, failing)
		if result.Errors == nil {
			t.Fatalf("indexing an error value succeeded: %v", result.Result)
		}
		if hints := strings.Join(result.Errors.Hints, "\n"); !strings.Contains(hints, `hasattr(r, "error")`) {
			t.Fatalf("hints do not explain the failed call:\n%s", hints)
		}
	})
}

// The try/except hint hands the model a check to copy. Running that exact check against a failed
// call and a successful result keeps the hint from drifting away from what a failed call is.
func TestExceptionHandlingHintCatchesAFailedCall(t *testing.T) {
	var variable, check string
	for _, line := range exceptionHandlingHints() {
		line = strings.TrimSpace(line)
		if name, _, ok := strings.Cut(line, " = server.tool("); ok {
			variable = name
		}
		if strings.HasPrefix(line, "if ") && strings.HasSuffix(line, ":") {
			check = strings.TrimSuffix(strings.TrimPrefix(line, "if "), ":")
		}
	}
	if variable == "" || check == "" {
		t.Fatalf("the hint shows no call and check to copy: %q", exceptionHandlingHints())
	}

	limits := schemas.MCPCodeModeLimits{}.WithDefaults()
	bindings := map[string][]string{"srv": {"srv-get"}}
	for _, tc := range []struct {
		name  string
		value interface{}
		want  bool
	}{
		{name: "a failed call", value: toolErrorValue{message: "contact 7 not found"}, want: true},
		{name: "a dict result", value: map[string]interface{}{"id": 7}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call := func(context.Context, string, string, map[string]interface{}, func(string)) (interface{}, error) {
				return tc.value, nil
			}
			code := variable + " = srv.get()\nfailed = bool(" + check + ")\nresult = failed"
			result := runSandbox(context.Background(), limits, code, bindings, call)
			if result.Errors != nil {
				t.Fatalf("the hint's check raised: %s", result.Errors.Message)
			}
			if result.Result != tc.want {
				t.Fatalf("the hint's check %q gave %v, want %v", check, result.Result, tc.want)
			}
		})
	}
}

// A script that calls fail() chose to stop: the hints say so instead of generic code advice.
func TestGeneratePythonErrorHintsForFail(t *testing.T) {
	hints := generatePythonErrorHints("fail: update failed: permission denied", []string{"crm"})
	if len(hints) != 1 || !strings.Contains(hints[0], "fail()") {
		t.Fatalf("hints for fail() = %q", hints)
	}
}

// The stubs teach the failed-call check and fail(), the two halves of handling a failed call.
func TestStubsExplainFailedCalls(t *testing.T) {
	stub, docs := readSearchStubs(t, newSearchCodeMode(searchToolSchema()))

	for name, content := range map[string]string{"readToolFile": stub, "getToolDocs": docs} {
		for _, want := range []string{`hasattr(r, "error")`, "fail(r.error)"} {
			if !strings.Contains(content, want) {
				t.Errorf("%s does not mention %s, got:\n%s", name, want, content)
			}
		}
	}
}

// The executeToolCode description is the one text every model reads before its first script, so
// it must describe failed calls as values and list only names the sandbox resolves.
func TestExecuteToolCodeDescriptionMatchesTheSandbox(t *testing.T) {
	description := *newSearchCodeMode(searchToolSchema()).createExecuteToolCodeTool().Function.Description

	for _, want := range []string{`hasattr(r, "error")`, "fail(r.error)", "json.decode", "math."} {
		if !strings.Contains(description, want) {
			t.Errorf("description does not mention %s:\n%s", want, description)
		}
	}
	// A server bound under a module's name takes it, so the fixed text must say that for every module.
	if shadowing := "a server named " + strings.Join(sandboxModules.Keys(), " or ") + " takes that name"; !strings.Contains(description, shadowing) {
		t.Errorf("description does not say %q:\n%s", shadowing, description)
	}
	if strings.Contains(description, "cannot be caught") {
		t.Errorf("description still says tool failures cannot be caught:\n%s", description)
	}

	_, rest, ok := strings.Cut(description, "AVAILABLE BUILTINS: ")
	if !ok {
		t.Fatalf("description lists no builtins:\n%s", description)
	}
	// Cut at the next section's label, not at a period, so a sentence inside the list fails below.
	listedWithDot, _, _ := strings.Cut(rest, " MODULES:")
	listed := strings.TrimSuffix(listedWithDot, ".")
	names := strings.Split(listed, ", ")
	if !slices.Contains(names, "fail") {
		t.Errorf("builtins %q leave out fail", names)
	}
	result := runSandbox(context.Background(), schemas.MCPCodeModeLimits{}.WithDefaults(), "result = len(["+listed+"])", nil, nil)
	if result.Errors != nil {
		t.Fatalf("a listed builtin does not exist in the sandbox: %s", result.Errors.Message)
	}
}

// A runtime error names the script lines that led to it, in the Python traceback layout, so
// the model can fix the right line. Errors raised before the script runs already carry their
// position and are left as they are.
func TestCodeModeRuntimeErrorsNameTheFailingLine(t *testing.T) {
	limits := schemas.MCPCodeModeLimits{}.WithDefaults()
	bindings := map[string][]string{"crm": {"crm-get_contact"}}
	errorValue := func(context.Context, string, string, map[string]interface{}, func(string)) (interface{}, error) {
		return toolErrorValue{message: "contact 7 not found"}, nil
	}
	run := func(t *testing.T, code string) *ExecutionError {
		t.Helper()
		result := runSandbox(context.Background(), limits, code, bindings, errorValue)
		if result.Errors == nil {
			t.Fatalf("expected an error, got result %v", result.Result)
		}
		return result.Errors
	}

	t.Run("an error inside a function", func(t *testing.T) {
		got := run(t, "def main():\n    r = crm.get_contact(id=7)\n    return r[\"email\"]\nresult = main()")
		for _, want := range []string{
			"Traceback (most recent call last):",
			"code.star:4:", "in <toplevel>",
			"code.star:3:", "in main",
			"Error: unhandled index operation struct[string]",
		} {
			if !strings.Contains(got.Message, want) {
				t.Fatalf("message lacks %q:\n%s", want, got.Message)
			}
		}
	})

	t.Run("fail() keeps its message and its hint", func(t *testing.T) {
		got := run(t, "def main():\n    r = crm.get_contact(id=7)\n    if hasattr(r, \"error\"):\n        fail(\"lookup failed: \" + r.error)\nresult = main()")
		if !strings.Contains(got.Message, "code.star:4:") || !strings.HasSuffix(got.Message, "Error: fail: lookup failed: contact 7 not found") {
			t.Fatalf("fail() message:\n%s", got.Message)
		}
		if strings.Contains(got.Message, "in fail") {
			t.Fatalf("the builtin frame leaked into the traceback:\n%s", got.Message)
		}
		if len(got.Hints) != 1 || !strings.Contains(got.Hints[0], "fail()") {
			t.Fatalf("hints = %q", got.Hints)
		}
	})

	t.Run("deep recursion keeps the outermost frame and the innermost five", func(t *testing.T) {
		// 202 frames: <toplevel> and 201 calls of f. The outermost is kept, a marker stands in
		// for the 196 left out, and the innermost five follow, ending where the error is.
		got := run(t, "def f(n):\n    if n == 0:\n        return {}[\"k\"]\n    return f(n - 1)\nresult = f(200)")
		start := strings.Index(got.Message, "Traceback (most recent call last):")
		if start == -1 {
			t.Fatalf("no traceback:\n%s", got.Message)
		}
		lines := strings.Split(got.Message[start:], "\n")
		want := []struct{ prefix, suffix string }{
			{"Traceback (most recent call last):", ""},
			{"  code.star:5:", " in <toplevel>"},
			{"  ... 196 more frames ...", ""},
			{"  code.star:4:", " in f"},
			{"  code.star:4:", " in f"},
			{"  code.star:4:", " in f"},
			{"  code.star:4:", " in f"},
			{"  code.star:3:", " in f"},
			{"Error: ", ""},
		}
		if len(lines) != len(want) {
			t.Fatalf("traceback has %d lines, want %d:\n%s", len(lines), len(want), got.Message)
		}
		for i, w := range want {
			if !strings.HasPrefix(lines[i], w.prefix) || !strings.HasSuffix(lines[i], w.suffix) {
				t.Fatalf("line %d = %q, want %q...%q:\n%s", i, lines[i], w.prefix, w.suffix, got.Message)
			}
		}
	})

	t.Run("a syntax error keeps its own position", func(t *testing.T) {
		got := run(t, "result = (")
		if strings.Contains(got.Message, "Traceback") || !strings.Contains(got.Message, "code.star:1:") {
			t.Fatalf("syntax error message:\n%s", got.Message)
		}
	})
}
