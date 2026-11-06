package logging

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

// responsesToolCallPayload returns the chat-shaped type, name and arguments of a
// client tool call item, reading `arguments`, `input` or `action` per type.
func responsesToolCallPayload(item *schemas.ResponsesMessage) (toolType, name, args string, ok bool) {
	if item.Type == nil || item.ResponsesToolMessage == nil {
		return "", "", "", false
	}
	tm := item.ResponsesToolMessage
	if tm.Name != nil {
		name = strings.TrimSpace(*tm.Name)
	}
	switch *item.Type {
	case schemas.ResponsesMessageTypeFunctionCall:
		if name == "" {
			return "", "", "", false
		}
		return "function", name, derefString(tm.Arguments), true

	case schemas.ResponsesMessageTypeCustomToolCall:
		if name == "" {
			return "", "", "", false
		}
		if tm.ResponsesCustomToolCall != nil {
			args = tm.ResponsesCustomToolCall.Input
		}
		return "custom", name, args, true

	case schemas.ResponsesMessageTypeLocalShellCall:
		// local_shell_call has no name of its own; the tool type is the name.
		if name == "" {
			name = "local_shell"
		}
		if tm.Action != nil && tm.Action.ResponsesLocalShellToolCallAction != nil {
			if encoded, err := schemas.MarshalString(tm.Action.ResponsesLocalShellToolCallAction); err == nil {
				args = encoded
			}
		}
		return "local_shell", name, args, true

	default:
		return "", "", "", false
	}
}

// chatToolCallsFromResponsesOutput projects Responses API client tool call output
// items into chat-shaped tool calls. Shared by the chat, responses and realtime
// logging paths so every request type lands the same structure in tool_calls.
func chatToolCallsFromResponsesOutput(output []schemas.ResponsesMessage) []schemas.ChatAssistantMessageToolCall {
	var toolCalls []schemas.ChatAssistantMessageToolCall
	for _, item := range output {
		toolType, name, args, ok := responsesToolCallPayload(&item)
		if !ok {
			continue
		}
		toolCall := schemas.ChatAssistantMessageToolCall{
			Index: uint16(len(toolCalls)),
			Type:  &toolType,
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name:      &name,
				Arguments: args,
			},
		}
		if item.CallID != nil && strings.TrimSpace(*item.CallID) != "" {
			toolCall.ID = schemas.Ptr(strings.TrimSpace(*item.CallID))
		} else if item.ID != nil && strings.TrimSpace(*item.ID) != "" {
			toolCall.ID = schemas.Ptr(strings.TrimSpace(*item.ID))
		}
		toolCalls = append(toolCalls, toolCall)
	}
	return toolCalls
}

// collectToolCalls returns the tool calls a response produced. The chat output
// message wins when it carries any; otherwise the Responses API output items
// are projected. Returns nil when the response called no tools.
func collectToolCalls(outputMessage *schemas.ChatMessage, responsesOutput []schemas.ResponsesMessage) []schemas.ChatAssistantMessageToolCall {
	if outputMessage != nil && outputMessage.ChatAssistantMessage != nil && len(outputMessage.ChatAssistantMessage.ToolCalls) > 0 {
		return outputMessage.ChatAssistantMessage.ToolCalls
	}
	return chatToolCallsFromResponsesOutput(responsesOutput)
}

// toolCallNames extracts the distinct function names from tool calls, keeping
// first-seen order. Empty names are dropped, and so are names containing a
// comma, since the names are stored as a comma-separated list.
func toolCallNames(toolCalls []schemas.ChatAssistantMessageToolCall) []string {
	if len(toolCalls) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(toolCalls))
	names := make([]string, 0, len(toolCalls))
	for _, tc := range toolCalls {
		if tc.Function.Name == nil {
			continue
		}
		name := strings.TrimSpace(*tc.Function.Name)
		if name == "" || strings.Contains(name, ",") {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil
	}
	return names
}

// applyToolCallsToEntry records the tool calls a response made. Function names
// are metadata (on par with stop_reason=tool_calls) and are always persisted so
// the logs filter works without content logging. The full tool_calls payload
// carries arguments, which are content, so it follows the content policy.
func applyToolCallsToEntry(entry *logstore.Log, toolCalls []schemas.ChatAssistantMessageToolCall, contentLoggingEnabled bool) {
	if entry == nil || len(toolCalls) == 0 {
		return
	}
	if names := toolCallNames(toolCalls); len(names) > 0 {
		entry.ToolCallNames = names
	}
	if contentLoggingEnabled {
		entry.ToolCallsParsed = toolCalls
	}
}
