package logging

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

// chatToolCallsFromResponsesOutput projects Responses API function_call output
// items into chat-shaped tool calls. Shared by the chat, responses and realtime
// logging paths so every request type lands the same structure in tool_calls.
func chatToolCallsFromResponsesOutput(output []schemas.ResponsesMessage) []schemas.ChatAssistantMessageToolCall {
	var toolCalls []schemas.ChatAssistantMessageToolCall
	for _, item := range output {
		if item.Type == nil || *item.Type != schemas.ResponsesMessageTypeFunctionCall {
			continue
		}
		if item.ResponsesToolMessage == nil || item.ResponsesToolMessage.Name == nil {
			continue
		}
		toolType := "function"
		toolCall := schemas.ChatAssistantMessageToolCall{
			Index: uint16(len(toolCalls)),
			Type:  &toolType,
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name:      item.ResponsesToolMessage.Name,
				Arguments: derefString(item.ResponsesToolMessage.Arguments),
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
