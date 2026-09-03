package agentcapabilityrouter

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

const (
	maxExtractedStringBytes = 4096
	maxExtractedTextBytes   = 16384
)

func extractAgentSignals(req *schemas.BifrostRequest, historyMessages int) SignalSnapshot {
	if req == nil {
		return SignalSnapshot{}
	}
	if req.ChatRequest != nil {
		input := req.ChatRequest.Input
		if len(input) > historyMessages {
			input = input[len(input)-historyMessages:]
		}
		events := make([]SignalEvent, 0, len(input))
		for _, message := range input {
			text := textFromValue(message)
			kind := string(message.Role)
			if message.ChatToolMessage != nil || message.Role == schemas.ChatMessageRoleTool {
				kind = "tool-result"
			}
			if message.ChatAssistantMessage != nil && len(message.ChatAssistantMessage.ToolCalls) > 0 {
				kind = inferToolKind(text)
			}
			events = append(events, SignalEvent{Kind: kind, Text: text, Failed: kind == "tool-result" && looksLikeFailure(text)})
		}
		return SignalSnapshot{Events: events}
	}
	if req.ResponsesRequest != nil {
		input := req.ResponsesRequest.Input
		if len(input) > historyMessages {
			input = input[len(input)-historyMessages:]
		}
		events := make([]SignalEvent, 0, len(input))
		for _, message := range input {
			text := textFromValue(message)
			kind := inferResponsesKind(message, text)
			events = append(events, SignalEvent{Kind: kind, Text: text, Failed: kind == "tool-result" && looksLikeFailure(text)})
		}
		return SignalSnapshot{Events: events}
	}
	return SignalSnapshot{}
}

func inferResponsesKind(message schemas.ResponsesMessage, text string) string {
	if message.Type != nil {
		switch string(*message.Type) {
		case "function_call_output":
			return "tool-result"
		case "function_call":
			return inferToolKind(text)
		}
	}
	if message.Role != nil {
		switch string(*message.Role) {
		case "user":
			return "user"
		case "assistant":
			return "assistant"
		}
	}
	return "context"
}

func inferToolKind(text string) string {
	lower := strings.ToLower(text)
	if containsAny(lower, "edit", "write", "patch", "apply_patch", "create_file", "replace") {
		return "edit"
	}
	if containsAny(lower, "read", "grep", "glob", "search", "find") {
		return "search"
	}
	return "tool-call"
}

func looksLikeFailure(text string) bool {
	lower := " " + strings.ToLower(text)
	return containsAny(lower,
		" fail", "failed", "failure", "panic", "exception", "traceback",
		"segmentation fault", "exit code 1", "exit status 1", "assertion failed",
		"compile error", "type error", "test failed",
	)
}

func textFromValue(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return ""
	}
	collector := textCollector{parts: make([]string, 0, 8)}
	collector.collect(decoded, "")
	return strings.Join(collector.parts, " ")
}

type textCollector struct {
	parts []string
	bytes int
}

func (c *textCollector) collect(value any, key string) {
	if c.bytes >= maxExtractedTextBytes {
		return
	}
	switch typed := value.(type) {
	case string:
		if typed == "" || len(typed) > maxExtractedStringBytes || isOpaqueField(key, typed) {
			return
		}
		separatorBytes := 0
		if len(c.parts) > 0 {
			separatorBytes = 1
		}
		remaining := maxExtractedTextBytes - c.bytes - separatorBytes
		if remaining <= 0 {
			return
		}
		if len(typed) > remaining {
			typed = typed[:remaining]
		}
		c.parts = append(c.parts, typed)
		c.bytes += separatorBytes + len(typed)
	case []any:
		for _, item := range typed {
			c.collect(item, key)
			if c.bytes >= maxExtractedTextBytes {
				return
			}
		}
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for childKey := range typed {
			keys = append(keys, childKey)
		}
		sort.Strings(keys)
		for _, childKey := range keys {
			c.collect(typed[childKey], childKey)
			if c.bytes >= maxExtractedTextBytes {
				return
			}
		}
	}
}

func isOpaqueField(key, value string) bool {
	lowerKey := strings.ToLower(key)
	lowerValue := strings.ToLower(value)
	return lowerKey == "file_data" || lowerKey == "image_url" || lowerKey == "data" ||
		strings.HasPrefix(lowerValue, "data:image/") || strings.HasPrefix(lowerValue, "data:application/")
}
