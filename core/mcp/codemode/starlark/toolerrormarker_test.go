//go:build !tinygo && !wasm

package starlark

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	codemcp "github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
)

// TestCreateToolResponseMessageMarksError pins the CodeMode copy of the tool
// response builder. CodeMode reports its own failures -- unknown server, unknown
// tool, ambiguous filename, failed sandbox execution -- as ordinary result text,
// so without the marker the model reads a lookup failure as a successful answer.
func TestCreateToolResponseMessageMarksError(t *testing.T) {
	id := "call_1"
	name := "readToolFile"
	toolCall := schemas.ChatAssistantMessageToolCall{
		ID: &id,
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      &name,
			Arguments: `{"path":"servers/missing.pyi"}`,
		},
	}

	failed := createToolResponseMessage(toolCall, "No server found matching 'missing'", true)
	if failed.ChatToolMessage == nil {
		t.Fatal("expected ChatToolMessage to be attached")
	}
	if failed.ChatToolMessage.IsError == nil || !*failed.ChatToolMessage.IsError {
		t.Fatal("a CodeMode failure must set IsError true")
	}

	ok := createToolResponseMessage(toolCall, "def read_file(path: str) -> str", false)
	if ok.ChatToolMessage == nil {
		t.Fatal("expected ChatToolMessage to be attached")
	}
	if ok.ChatToolMessage.IsError != nil {
		t.Fatalf("a successful CodeMode result must leave IsError nil, got %v", *ok.ChatToolMessage.IsError)
	}
}

func TestHandleExecuteToolCodeBudgetResults(t *testing.T) {
	mode := NewStarlarkCodeMode(nil, nil)
	mode.clientManager = &testClientManager{}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	for _, tc := range []struct {
		code, message string
		failed        bool
	}{
		{"result = len([0] * 200000000)", "resource limit", true},
		{"result = []\nresult.append(result)", "nesting limit", true},
		{"result = 1 << 128\nfor i in range(6):\n    result *= result", "integer exceeds size limit", true},
		{"result = 42", "Return value: 42", false},
		// Print output carries the same label whether the run succeeds or fails.
		{"print(\"before\")\nresult = 1", "Print output:\nbefore", false},
		{"print(\"before\")\nfail(\"stop\")", "Print output:\nbefore", true},
	} {
		args, err := schemas.MarshalSorted(map[string]string{"code": tc.code})
		if err != nil {
			t.Fatal(err)
		}
		call := schemas.ChatAssistantMessageToolCall{ID: schemas.Ptr("budget"), Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("executeToolCode"), Arguments: string(args)}}
		msg, err := mode.handleExecuteToolCode(ctx, call)
		if err != nil {
			t.Fatal(err)
		}
		if msg.ChatToolMessage == nil || msg.Content == nil || msg.Content.ContentStr == nil {
			t.Fatalf("missing tool response: %+v", msg)
		}
		if failed := msg.IsError != nil && *msg.IsError; failed != tc.failed {
			t.Fatalf("is_error=%v for %s", failed, tc.code)
		}
		if !strings.Contains(*msg.Content.ContentStr, tc.message) {
			t.Fatalf("expected %q in %q", tc.message, *msg.Content.ContentStr)
		}
	}
}

// TestHandleExecuteToolCodeMarksSandboxErrors covers the caller that decides what to
// hand createToolResponseMessage. A sandbox run that raises produces a non-nil
// result.Errors, and that is a tool failure: without the marker the provider receives
// the traceback as an ordinary tool result and the model reads it as a real answer.
func TestHandleExecuteToolCodeMarksSandboxErrors(t *testing.T) {
	mode := NewStarlarkCodeMode(&codemcp.CodeModeConfig{
		BindingLevel:         schemas.CodeModeBindingLevelTool,
		ToolExecutionTimeout: time.Second,
	}, nil)
	mode.clientManager = &testClientManager{}

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	id := "call_err"
	name := "executeToolCode"
	toolCall := schemas.ChatAssistantMessageToolCall{
		ID: &id,
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name: &name,
			// Raises at runtime inside the sandbox: undefined name.
			Arguments: `{"code":"result = definitely_not_defined + 1"}`,
		},
	}

	msg, err := mode.handleExecuteToolCode(ctx, toolCall)
	if err != nil {
		t.Fatalf("handleExecuteToolCode returned a transport error: %v", err)
	}
	if msg == nil || msg.ChatToolMessage == nil {
		t.Fatal("expected a tool response message")
	}
	if msg.ChatToolMessage.IsError == nil || !*msg.ChatToolMessage.IsError {
		t.Fatalf("a failed sandbox execution must set IsError true, got %v content=%v",
			msg.ChatToolMessage.IsError, msg.Content)
	}
}

// The return value is appended after the print output on every reply, and clients such as
// LibreChat resend tool results on every later turn, so it is rendered as compact JSON with
// sorted keys and without HTML escaping: an & in a URL stays one byte instead of &.
func TestHandleExecuteToolCodeReturnValueIsCompactJSON(t *testing.T) {
	mode := NewStarlarkCodeMode(nil, nil)
	mode.clientManager = &testClientManager{}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	code := "print(\"fetched 2 rows\")\nresult = {\"url\": \"https://example.com/?a=1&b=<2>\", \"rows\": [{\"id\": 1}, {\"id\": 2}]}"
	args, err := schemas.MarshalSorted(map[string]string{"code": code})
	if err != nil {
		t.Fatal(err)
	}
	call := schemas.ChatAssistantMessageToolCall{ID: schemas.Ptr("compact"), Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("executeToolCode"), Arguments: string(args)}}
	msg, err := mode.handleExecuteToolCode(ctx, call)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content == nil || msg.Content.ContentStr == nil {
		t.Fatalf("missing tool response: %+v", msg)
	}
	got := *msg.Content.ContentStr
	want := "Print output:\nfetched 2 rows\n\nExecution completed successfully.\n" +
		`Return value: {"rows":[{"id":1},{"id":2}],"url":"https://example.com/?a=1&b=<2>"}`
	if got != want {
		t.Fatalf("unexpected reply:\n got: %q\nwant: %q", got, want)
	}
}

// Invalid UTF-8 from a tool still renders as valid JSON once HTML escaping is off.
func TestFormatReturnValueReplacesInvalidUTF8(t *testing.T) {
	got, err := formatReturnValue(map[string]interface{}{"s": "a\xffb"})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]string
	if err := json.Unmarshal(got, &decoded); err != nil || !utf8.Valid(got) {
		t.Fatalf("not valid UTF-8 JSON: %q (%v)", got, err)
	}
	if want := "a\uFFFDb"; decoded["s"] != want {
		t.Fatalf("got %q, want %q", decoded["s"], want)
	}
}

func BenchmarkFormatReturnValue(b *testing.B) {
	rows := make([]interface{}, 50)
	for i := range rows {
		rows[i] = map[string]interface{}{"id": i, "name": "row", "url": "https://example.com/?a=1&b=2", "active": i%2 == 0}
	}
	value := map[string]interface{}{"rows": rows, "total": len(rows)}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := formatReturnValue(value); err != nil {
			b.Fatal(err)
		}
	}
}
