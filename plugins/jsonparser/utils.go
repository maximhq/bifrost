package jsonparser

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// getRequestID extracts a unique identifier for the request to maintain state
func (p *JsonParserPlugin) getRequestID(ctx *schemas.BifrostContext, result *schemas.BifrostResponse) string {
	// Try to get from result
	if result != nil && result.ChatResponse != nil && result.ChatResponse.ID != "" {
		return result.ChatResponse.ID
	}

	// Try to get from context if not available in result
	if ctx != nil {
		if requestID, ok := ctx.Value(schemas.BifrostContextKeyRequestID).(string); ok && requestID != "" {
			return requestID
		}
	}

	return ""
}

// shouldRun determines if the plugin should process the request based on usage type
func (p *JsonParserPlugin) shouldRun(ctx *schemas.BifrostContext, requestType schemas.RequestType) bool {
	// Run only for chat completion stream requests
	if requestType != schemas.ChatCompletionStreamRequest {
		return false
	}

	switch p.usage {
	case AllRequests:
		return true
	case PerRequest:
		// Check if the context contains the plugin-specific key
		if ctx != nil {
			if value, ok := ctx.Value(EnableStreamingJSONParser).(bool); ok {
				return value
			}
		}
		return false
	default:
		return false
	}
}

// accumulateContent adds new content to the accumulated content for a specific request
func (p *JsonParserPlugin) accumulateContent(requestID, newContent string) string {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	// Get existing accumulated content
	existing := p.accumulatedContent[requestID]

	if existing != nil {
		// Append to existing builder
		existing.Content.WriteString(newContent)
		return existing.Content.String()
	} else {
		// Create new builder
		builder := &strings.Builder{}
		builder.WriteString(newContent)
		p.accumulatedContent[requestID] = &AccumulatedContent{
			Content:   builder,
			Timestamp: time.Now(),
		}
		return builder.String()
	}
}

// parsePartialJSON parses a JSON string that may be missing closing braces
func (p *JsonParserPlugin) parsePartialJSON(s string) string {
	// Trim whitespace
	s = strings.TrimSpace(s)
	if s == "" {
		return "{}"
	}

	// Quick check: if it starts with { or [, it might be JSON
	if s[0] != '{' && s[0] != '[' {
		return s
	}

	// First, try to parse the string as-is (fast path)
	if p.isValidJSON(s) {
		return s
	}

	// Use a more efficient approach: build the completion directly
	return p.completeJSON(s)
}

// completeJSON completes partial JSON with O(n) time complexity
func (p *JsonParserPlugin) completeJSON(s string) string {
	// Pre-allocate buffer with estimated capacity
	capacity := len(s) + 10 // Estimate max 10 closing characters needed
	result := make([]byte, 0, capacity)

	var stack []byte
	inString := false
	escaped := false

	// Process the string once
	for i := 0; i < len(s); i++ {
		char := s[i]
		result = append(result, char)

		if escaped {
			escaped = false
			continue
		}

		if char == '\\' {
			escaped = true
			continue
		}

		if char == '"' {
			inString = !inString
			continue
		}

		if inString {
			continue
		}

		switch char {
		case '{', '[':
			if char == '{' {
				stack = append(stack, '}')
			} else {
				stack = append(stack, ']')
			}
		case '}', ']':
			if len(stack) > 0 && stack[len(stack)-1] == char {
				stack = stack[:len(stack)-1]
			}
		}
	}

	// Close any unclosed strings
	if inString {
		if escaped {
			// Remove the trailing backslash
			if len(result) > 0 {
				result = result[:len(result)-1]
			}
		}
		result = append(result, '"')
	}

	// Add closing characters in reverse order
	for i := len(stack) - 1; i >= 0; i-- {
		result = append(result, stack[i])
	}

	// Validate the result
	if p.isValidJSON(string(result)) {
		return string(result)
	}

	// If still invalid, try progressive truncation (but more efficiently)
	return p.progressiveTruncation(s, result)
}

// progressiveTruncation efficiently tries different truncation points
func (p *JsonParserPlugin) progressiveTruncation(original string, completed []byte) string {
	// Try removing characters from the end until we get valid JSON
	// Use binary search for better performance
	left, right := 0, len(completed)

	for left < right {
		mid := (left + right) / 2
		candidate := completed[:mid]

		if p.isValidJSON(string(candidate)) {
			left = mid + 1
		} else {
			right = mid
		}
	}

	// Try the best candidate
	if left > 0 && p.isValidJSON(string(completed[:left-1])) {
		return string(completed[:left-1])
	}

	// Fallback to original
	return original
}

// isValidJSON checks if a string is valid JSON
func (p *JsonParserPlugin) isValidJSON(s string) bool {
	// Trim whitespace
	s = strings.TrimSpace(s)

	// Empty string after trimming is not valid JSON
	if s == "" {
		return false
	}

	return json.Valid([]byte(s))
}

// DEEP COPY METHODS

// deepCopyBifrostResponse creates a deep copy of BifrostResponse to avoid modifying the original
func (p *JsonParserPlugin) deepCopyBifrostResponse(original *schemas.BifrostResponse) *schemas.BifrostResponse {
	if original == nil {
		return nil
	}

	// Create a new BifrostResponse
	result := &schemas.BifrostResponse{}

	// Copy ChatResponse if it exists (this is what we're interested in for the JSON parser)
	if original.ChatResponse != nil {
		result.ChatResponse = p.deepCopyBifrostChatResponse(original.ChatResponse)
	}

	// Copy other response types if they exist (shallow copy since we don't modify them)
	result.TextCompletionResponse = original.TextCompletionResponse
	result.ResponsesResponse = original.ResponsesResponse
	result.ResponsesStreamResponse = original.ResponsesStreamResponse
	result.EmbeddingResponse = original.EmbeddingResponse
	result.SpeechResponse = original.SpeechResponse
	result.SpeechStreamResponse = original.SpeechStreamResponse
	result.TranscriptionResponse = original.TranscriptionResponse
	result.TranscriptionStreamResponse = original.TranscriptionStreamResponse

	return result
}

// deepCopyBifrostChatResponse creates a deep copy of BifrostChatResponse
func (p *JsonParserPlugin) deepCopyBifrostChatResponse(original *schemas.BifrostChatResponse) *schemas.BifrostChatResponse {
	if original == nil {
		return nil
	}

	result := &schemas.BifrostChatResponse{
		ID:                original.ID,
		Created:           original.Created,
		Model:             original.Model,
		Object:            original.Object,
		ServiceTier:       original.ServiceTier,
		SystemFingerprint: original.SystemFingerprint,
		Usage:             original.Usage,       // Shallow copy - usage shouldn't be modified
		ExtraFields:       original.ExtraFields, // Shallow copy
	}

	// Deep copy Choices slice
	if original.Choices != nil {
		result.Choices = make([]schemas.BifrostResponseChoice, len(original.Choices))
		for i, choice := range original.Choices {
			result.Choices[i] = p.deepCopyBifrostResponseChoice(choice)
		}
	}

	return result
}

// deepCopyBifrostResponseChoice creates a deep copy of BifrostResponseChoice
func (p *JsonParserPlugin) deepCopyBifrostResponseChoice(original schemas.BifrostResponseChoice) schemas.BifrostResponseChoice {
	result := schemas.BifrostResponseChoice{
		Index:        original.Index,
		FinishReason: original.FinishReason,
		LogProbs:     original.LogProbs,
	}

	// Deep copy ChatStreamResponseChoice if it exists (this is what we modify)
	if original.ChatStreamResponseChoice != nil {
		result.ChatStreamResponseChoice = p.deepCopyChatStreamResponseChoice(original.ChatStreamResponseChoice)
	}

	// Shallow copy other choice types since we don't modify them
	result.ChatNonStreamResponseChoice = original.ChatNonStreamResponseChoice
	result.TextCompletionResponseChoice = original.TextCompletionResponseChoice

	return result
}

// deepCopyChatStreamResponseChoice creates a deep copy of ChatStreamResponseChoice
func (p *JsonParserPlugin) deepCopyChatStreamResponseChoice(original *schemas.ChatStreamResponseChoice) *schemas.ChatStreamResponseChoice {
	if original == nil {
		return nil
	}

	result := &schemas.ChatStreamResponseChoice{}

	// Deep copy Delta pointer if it exists
	if original.Delta != nil {
		result.Delta = p.deepCopyChatStreamResponseChoiceDelta(original.Delta)
	}

	return result
}

// deepCopyChatStreamResponseChoiceDelta creates a deep copy of ChatStreamResponseChoiceDelta
func (p *JsonParserPlugin) deepCopyChatStreamResponseChoiceDelta(original *schemas.ChatStreamResponseChoiceDelta) *schemas.ChatStreamResponseChoiceDelta {
	if original == nil {
		return nil
	}

	result := &schemas.ChatStreamResponseChoiceDelta{
		Role:      original.Role,
		Reasoning: original.Reasoning, // Shallow copy
		Refusal:   original.Refusal,   // Shallow copy
		ToolCalls: original.ToolCalls, // Shallow copy - we don't modify tool calls
	}

	// Deep copy Content pointer if it exists (this is what we modify)
	if original.Content != nil {
		contentCopy := *original.Content
		result.Content = &contentCopy
	}

	return result
}
