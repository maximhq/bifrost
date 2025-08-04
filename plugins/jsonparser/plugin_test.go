package jsonparser

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestStreamingResponse(t *testing.T) {
	plugin := NewJsonParserPlugin(true)

	// Test with partial JSON in streaming response
	partialJSON := `{"name": "John", "age": 30, "city": "New York"`
	expectedJSON := `{"name": "John", "age": 30, "city": "New York"}`

	result := &schemas.BifrostResponse{
		Choices: []schemas.BifrostResponseChoice{
			{
				BifrostStreamResponseChoice: &schemas.BifrostStreamResponseChoice{
					Delta: schemas.BifrostStreamDelta{
						Content: &partialJSON,
					},
				},
			},
		},
	}

	ctx := context.Background()
	modifiedResult, err, pluginErr := plugin.PostHook(&ctx, result, nil)

	if pluginErr != nil {
		t.Errorf("PostHook should not return error, got %v", pluginErr)
	}
	if err != nil {
		t.Errorf("PostHook should not return bifrost error, got %v", err)
	}
	if modifiedResult == nil {
		t.Fatal("PostHook should return a result")
	}

	content := *modifiedResult.Choices[0].BifrostStreamResponseChoice.Delta.Content
	if content != expectedJSON {
		t.Errorf("Expected JSON %s, got %s", expectedJSON, content)
	}
}

func TestParsePartialJSON(t *testing.T) {
	plugin := NewJsonParserPlugin(true)

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Already valid JSON object",
			input:    `{"name": "John", "age": 30}`,
			expected: `{"name": "John", "age": 30}`,
		},
		{
			name:     "Partial JSON object missing closing brace",
			input:    `{"name": "John", "age": 30, "city": "New York"`,
			expected: `{"name": "John", "age": 30, "city": "New York"}`,
		},
		{
			name:     "Partial JSON array missing closing bracket",
			input:    `["apple", "banana", "cherry"`,
			expected: `["apple", "banana", "cherry"]`,
		},
		{
			name:     "Nested partial JSON",
			input:    `{"user": {"name": "John", "details": {"age": 30, "city": "NY"`,
			expected: `{"user": {"name": "John", "details": {"age": 30, "city": "NY"}}}`,
		},
		{
			name:     "Partial JSON with string containing newline",
			input:    `{"message": "Hello\nWorld"`,
			expected: `{"message": "Hello\nWorld"}`,
		},
		{
			name:     "Empty string",
			input:    "",
			expected: "{}",
		},
		{
			name:     "Whitespace only",
			input:    "   \n\t  ",
			expected: "{}",
		},
		{
			name:     "Non-JSON string",
			input:    "This is not JSON",
			expected: "This is not JSON",
		},
		{
			name:     "Partial JSON with escaped quotes",
			input:    `{"message": "He said \"Hello\""`,
			expected: `{"message": "He said \"Hello\""}`,
		},
		{
			name:     "Complex nested structure",
			input:    `{"data": {"users": [{"id": 1, "name": "John"}, {"id": 2, "name": "Jane"`,
			expected: `{"data": {"users": [{"id": 1, "name": "John"}, {"id": 2, "name": "Jane"}]}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := plugin.parsePartialJSON(tt.input)
			if result != tt.expected {
				t.Errorf("parsePartialJSON(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestIsValidJSON(t *testing.T) {
	plugin := NewJsonParserPlugin(true)

	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "Valid JSON object",
			input:    `{"name": "John", "age": 30}`,
			expected: true,
		},
		{
			name:     "Valid JSON array",
			input:    `["apple", "banana", "cherry"]`,
			expected: true,
		},
		{
			name:     "Valid nested JSON",
			input:    `{"user": {"name": "John", "details": {"age": 30, "city": "NY"}}}`,
			expected: true,
		},
		{
			name:     "Valid JSON with escaped quotes",
			input:    `{"message": "He said \"Hello\""}`,
			expected: true,
		},
		{
			name:     "Valid JSON with newlines",
			input:    `{"message": "Hello\nWorld"}`,
			expected: true,
		},
		{
			name:     "Invalid JSON - missing closing brace",
			input:    `{"name": "John", "age": 30`,
			expected: false,
		},
		{
			name:     "Invalid JSON - mismatched braces",
			input:    `{"name": "John", "age": 30]`,
			expected: false,
		},
		{
			name:     "Invalid JSON - unclosed string",
			input:    `{"message": "Hello`,
			expected: false,
		},
		{
			name:     "Invalid JSON - unclosed escape",
			input:    `{"message": "Hello\`,
			expected: false,
		},
		{
			name:     "Empty string",
			input:    "",
			expected: false,
		},
		{
			name:     "Non-JSON string",
			input:    "This is not JSON",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := plugin.isValidJSON(tt.input)
			if result != tt.expected {
				t.Errorf("isValidJSON(%q) = %t, want %t", tt.input, result, tt.expected)
			}
		})
	}
}
