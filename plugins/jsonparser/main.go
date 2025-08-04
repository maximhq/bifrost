package jsonparser

import (
	"context"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

const (
	PluginName = "streaming-json-parser"
)

// JsonParserPlugin provides JSON parsing capabilities for streaming responses
// It handles partial JSON chunks by making them valid JSON objects
type JsonParserPlugin struct {
	enabled bool
}

// NewJsonParserPlugin creates a new JSON parser plugin instance
func NewJsonParserPlugin(enabled bool) *JsonParserPlugin {
	return &JsonParserPlugin{
		enabled: enabled,
	}
}

// GetName returns the plugin name
func (p *JsonParserPlugin) GetName() string {
	return PluginName
}

// PreHook is not used for this plugin as we only process responses
func (p *JsonParserPlugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
	return req, nil, nil
}

// PostHook processes streaming responses to make partial JSON chunks valid
func (p *JsonParserPlugin) PostHook(ctx *context.Context, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	// Skip processing if plugin is disabled
	if !p.enabled {
		return result, err, nil
	}

	// If there's an error, don't process
	if err != nil {
		return result, err, nil
	}

	// If no result, return as is
	if result == nil {
		return result, err, nil
	}

	// Process only streaming choices to fix partial JSON
	if len(result.Choices) > 0 {
		for i := range result.Choices {
			choice := &result.Choices[i]

			// Handle only streaming response
			if choice.BifrostStreamResponseChoice != nil {
				if choice.BifrostStreamResponseChoice.Delta.Content != nil {
					content := *choice.BifrostStreamResponseChoice.Delta.Content
					if content != "" {
						// Try to parse as JSON and fix if needed
						fixedContent := p.parsePartialJSON(content)
						if fixedContent != content {
							choice.BifrostStreamResponseChoice.Delta.Content = &fixedContent
						}
					}
				}
			}
		}
	}

	return result, err, nil
}

// Cleanup performs plugin cleanup
func (p *JsonParserPlugin) Cleanup() error {
	return nil
}

// parsePartialJSON parses a JSON string that may be missing closing braces
func (p *JsonParserPlugin) parsePartialJSON(s string) string {
	// Trim whitespace
	s = strings.TrimSpace(s)
	if s == "" {
		return "{}"
	}

	// Quick check: if it starts with { or [, it might be JSON
	if (s[0] != '{' && s[0] != '[') {
		return s
	}

	// First, try to parse the string as-is (fast path)
	if p.isValidJSON(s) {
		return s
	}

	// Use a more efficient approach: build the completion directly
	return p.completeJSON(s)
}

// isValidJSON checks if a string is valid JSON without parsing into interface{}
func (p *JsonParserPlugin) isValidJSON(s string) bool {
	// Empty string is not valid JSON
	if s == "" {
		return false
	}

	// Quick check: must start with { or [ to be valid JSON
	s = strings.TrimSpace(s)
	if len(s) == 0 || (s[0] != '{' && s[0] != '[') {
		return false
	}

	// Use a simple state machine instead of json.Unmarshal for better performance
	var stack []byte
	inString := false
	escaped := false

	for i := 0; i < len(s); i++ {
		char := s[i]

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
			if len(stack) == 0 || stack[len(stack)-1] != char {
				return false
			}
			stack = stack[:len(stack)-1]
		}
	}

	return len(stack) == 0 && !inString && !escaped
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
