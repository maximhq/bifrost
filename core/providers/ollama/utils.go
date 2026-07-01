package ollama

import (
	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// ndjsonLine marshals v and appends a newline (the NDJSON streaming wire format).
func NdjsonLine(v any) (string, error) {
	b, err := sonic.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b) + "\n", nil
}

// bifrostToolCallsToOllamaToolCalls converts Bifrost tool calls to ollama tool calls.
func bifrostToolCallsToOllamaToolCalls(calls []schemas.ChatAssistantMessageToolCall) []OllamaToolCall {
	if len(calls) == 0 {
		return nil
	}
	result := make([]OllamaToolCall, 0, len(calls))
	for _, tc := range calls {
		fc := OllamaToolFunction{}
		if tc.Function.Name != nil {
			fc.Name = *tc.Function.Name
		}
		result = append(result, OllamaToolCall{Function: &fc})
	}
	return result
}

// extractGenerateText pulls the generated text from any response choice variant.
func extractGenerateText(c schemas.BifrostResponseChoice) string {
	if c.TextCompletionResponseChoice != nil && c.TextCompletionResponseChoice.Text != nil {
		return *c.TextCompletionResponseChoice.Text
	}
	if c.ChatNonStreamResponseChoice != nil && c.ChatNonStreamResponseChoice.Message != nil {
		m := c.ChatNonStreamResponseChoice.Message
		if m.Content != nil && m.Content.ContentStr != nil {
			return *m.Content.ContentStr
		}
	}
	return ""
}

// extractGenerateThinking pulls the reasoning/thinking text from a non-stream choice.
func extractGenerateThinking(c schemas.BifrostResponseChoice) string {
	if c.ChatNonStreamResponseChoice != nil && c.ChatNonStreamResponseChoice.Message != nil {
		m := c.ChatNonStreamResponseChoice.Message
		if m.ChatAssistantMessage != nil && m.ChatAssistantMessage.Reasoning != nil {
			return *m.ChatAssistantMessage.Reasoning
		}
	}
	return ""
}
