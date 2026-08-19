package mcp

import (
	"errors"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func testToolCall(id string) schemas.ChatAssistantMessageToolCall {
	name := "read_file"
	return schemas.ChatAssistantMessageToolCall{
		ID: &id,
		Function: schemas.ChatAssistantMessageToolCallFunction{
			Name:      &name,
			Arguments: `{"path":"config.yaml"}`,
		},
	}
}

// TestCreateToolResultMessageMarksError covers the agent loop's own failures.
// createToolResultMessage already receives the execution error and stringifies
// it into the content, so leaving IsError unset tells the model that a tool call
// bifrost itself watched fail actually succeeded.
func TestCreateToolResultMessageMarksError(t *testing.T) {
	failed := createToolResultMessage(testToolCall("call_1"), "", errors.New("connection refused"))
	if failed.ChatToolMessage == nil {
		t.Fatal("expected ChatToolMessage to be attached")
	}
	if failed.ChatToolMessage.IsError == nil || !*failed.ChatToolMessage.IsError {
		t.Fatal("a tool execution error must set IsError true")
	}

	// Success must stay unmarked so the marker never becomes a constant.
	ok := createToolResultMessage(testToolCall("call_2"), "15 degrees", nil)
	if ok.ChatToolMessage == nil {
		t.Fatal("expected ChatToolMessage to be attached")
	}
	if ok.ChatToolMessage.IsError != nil {
		t.Fatalf("a successful tool execution must leave IsError nil, got %v", *ok.ChatToolMessage.IsError)
	}
}

// TestCreateToolResponseMessageMarksError covers the MCP protocol's own failure
// signal. mcp.CallToolResult carries IsError (mcp-go v0.43.2 mcp/tools.go), which
// a server sets to report that the tool ran and failed — distinct from a
// transport error. toolmanager discarded it, so that signal never reached the model.
func TestCreateToolResponseMessageMarksError(t *testing.T) {
	failed := createToolResponseMessage(testToolCall("call_1"), "ENOENT: no such file or directory", true)
	if failed.ChatToolMessage == nil {
		t.Fatal("expected ChatToolMessage to be attached")
	}
	if failed.ChatToolMessage.IsError == nil || !*failed.ChatToolMessage.IsError {
		t.Fatal("an MCP isError result must set IsError true")
	}

	ok := createToolResponseMessage(testToolCall("call_2"), "15 degrees", false)
	if ok.ChatToolMessage == nil {
		t.Fatal("expected ChatToolMessage to be attached")
	}
	if ok.ChatToolMessage.IsError != nil {
		t.Fatalf("a successful MCP result must leave IsError nil, got %v", *ok.ChatToolMessage.IsError)
	}
}
