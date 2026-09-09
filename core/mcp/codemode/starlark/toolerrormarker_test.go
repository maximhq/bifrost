//go:build !tinygo && !wasm

package starlark

import (
	"context"
	"testing"
	"time"

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
