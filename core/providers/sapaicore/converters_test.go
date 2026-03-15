package sapaicore

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestConvertToVertex_BasicMessage(t *testing.T) {
	t.Parallel()

	content := "Hello, world!"
	request := &schemas.BifrostChatRequest{
		Model: "gemini-1.5-pro",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: &content,
				},
			},
		},
	}

	result := convertToVertex(request)

	if result == nil {
		t.Fatal("convertToVertex returned nil")
	}
	if len(result.Contents) != 1 {
		t.Fatalf("expected 1 content, got %d", len(result.Contents))
	}
	if result.Contents[0].Role != "user" {
		t.Errorf("expected role 'user', got %q", result.Contents[0].Role)
	}
	if len(result.Contents[0].Parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(result.Contents[0].Parts))
	}
	if result.Contents[0].Parts[0].Text != content {
		t.Errorf("expected text %q, got %q", content, result.Contents[0].Parts[0].Text)
	}
}

func TestConvertToVertex_SystemMessage(t *testing.T) {
	t.Parallel()

	systemContent := "You are a helpful assistant."
	userContent := "Hello!"
	request := &schemas.BifrostChatRequest{
		Model: "gemini-1.5-pro",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleSystem,
				Content: &schemas.ChatMessageContent{
					ContentStr: &systemContent,
				},
			},
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: &userContent,
				},
			},
		},
	}

	result := convertToVertex(request)

	if result.SystemInstruction == nil {
		t.Fatal("expected SystemInstruction to be set")
	}
	if len(result.SystemInstruction.Parts) != 1 {
		t.Fatalf("expected 1 part in SystemInstruction, got %d", len(result.SystemInstruction.Parts))
	}
	if result.SystemInstruction.Parts[0].Text != systemContent {
		t.Errorf("expected system text %q, got %q", systemContent, result.SystemInstruction.Parts[0].Text)
	}
	// System message should not be in Contents array
	if len(result.Contents) != 1 {
		t.Errorf("expected 1 content (system excluded), got %d", len(result.Contents))
	}
}

func TestConvertToVertex_WithParams(t *testing.T) {
	t.Parallel()

	content := "Hello!"
	temp := 0.7
	topP := 0.9
	maxTokens := 1000
	stop := []string{"stop1", "stop2"}

	request := &schemas.BifrostChatRequest{
		Model: "gemini-1.5-pro",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: &content,
				},
			},
		},
		Params: &schemas.ChatParameters{
			Temperature:         &temp,
			TopP:                &topP,
			MaxCompletionTokens: &maxTokens,
			Stop:                stop,
		},
	}

	result := convertToVertex(request)

	if result.GenerationConfig == nil {
		t.Fatal("expected GenerationConfig to be set")
	}
	if result.GenerationConfig.Temperature == nil || *result.GenerationConfig.Temperature != temp {
		t.Errorf("expected temperature %f, got %v", temp, result.GenerationConfig.Temperature)
	}
	if result.GenerationConfig.TopP == nil || *result.GenerationConfig.TopP != topP {
		t.Errorf("expected topP %f, got %v", topP, result.GenerationConfig.TopP)
	}
	if result.GenerationConfig.MaxOutputTokens == nil || *result.GenerationConfig.MaxOutputTokens != maxTokens {
		t.Errorf("expected maxOutputTokens %d, got %v", maxTokens, result.GenerationConfig.MaxOutputTokens)
	}
	if len(result.GenerationConfig.StopSequences) != 2 {
		t.Errorf("expected 2 stop sequences, got %d", len(result.GenerationConfig.StopSequences))
	}
}

func TestMapToVertexRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "assistant to model",
			input:    "assistant",
			expected: "model",
		},
		{
			name:     "user stays user",
			input:    "user",
			expected: "user",
		},
		{
			name:     "unknown role passes through",
			input:    "unknown",
			expected: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mapToVertexRole(tt.input)
			if result != tt.expected {
				t.Errorf("mapToVertexRole(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestMapVertexFinishReason(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "STOP to stop",
			input:    "STOP",
			expected: "stop",
		},
		{
			name:     "MAX_TOKENS to length",
			input:    "MAX_TOKENS",
			expected: "length",
		},
		{
			name:     "SAFETY to content_filter",
			input:    "SAFETY",
			expected: "content_filter",
		},
		{
			name:     "RECITATION to content_filter",
			input:    "RECITATION",
			expected: "content_filter",
		},
		{
			name:     "unknown defaults to stop",
			input:    "UNKNOWN",
			expected: "stop",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mapVertexFinishReason(tt.input)
			if result != tt.expected {
				t.Errorf("mapVertexFinishReason(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestParseVertexResponse(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{"text": "Hello!"}]
			},
			"finishReason": "STOP",
			"index": 0
		}],
		"usageMetadata": {
			"promptTokenCount": 10,
			"candidatesTokenCount": 5,
			"totalTokenCount": 15
		}
	}`)

	result, err := parseVertexResponse(body, "gemini-1.5-pro")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("parseVertexResponse returned nil")
	}
	if result.Model != "gemini-1.5-pro" {
		t.Errorf("expected model 'gemini-1.5-pro', got %q", result.Model)
	}
	if result.Object != "chat.completion" {
		t.Errorf("expected object 'chat.completion', got %q", result.Object)
	}
	if len(result.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(result.Choices))
	}
	if result.Choices[0].FinishReason == nil || *result.Choices[0].FinishReason != "stop" {
		t.Errorf("expected finish_reason 'stop', got %v", result.Choices[0].FinishReason)
	}
	if result.Usage == nil {
		t.Fatal("expected usage to be set")
	}
	if result.Usage.PromptTokens != 10 {
		t.Errorf("expected prompt_tokens 10, got %d", result.Usage.PromptTokens)
	}
	if result.Usage.CompletionTokens != 5 {
		t.Errorf("expected completion_tokens 5, got %d", result.Usage.CompletionTokens)
	}
	if result.Usage.TotalTokens != 15 {
		t.Errorf("expected total_tokens 15, got %d", result.Usage.TotalTokens)
	}
}

func TestParseVertexResponse_InvalidJSON(t *testing.T) {
	t.Parallel()

	body := []byte(`{invalid json`)

	_, err := parseVertexResponse(body, "gemini-1.5-pro")

	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestDefaultMaxTokens(t *testing.T) {
	t.Parallel()

	// DefaultMaxTokens should match the anthropic provider's default
	if DefaultMaxTokens != 4096 {
		t.Errorf("expected DefaultMaxTokens to be 4096, got %d", DefaultMaxTokens)
	}
}

func TestExtractMediaType(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		url      string
		expected string
	}{
		{
			name:     "data URL with PNG",
			url:      "data:image/png;base64,iVBORw0KGgo...",
			expected: "image/png",
		},
		{
			name:     "data URL with JPEG",
			url:      "data:image/jpeg;base64,/9j/4AAQSkZJ...",
			expected: "image/jpeg",
		},
		{
			name:     "data URL with GIF",
			url:      "data:image/gif;base64,R0lGODlhAQAB...",
			expected: "image/gif",
		},
		{
			name:     "data URL with WebP",
			url:      "data:image/webp;base64,UklGRh4AAA...",
			expected: "image/webp",
		},
		{
			name:     "plain base64 defaults to JPEG",
			url:      "iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB...",
			expected: "image/jpeg",
		},
		{
			name:     "empty string defaults to JPEG",
			url:      "",
			expected: "image/jpeg",
		},
		{
			name:     "data URL without semicolon",
			url:      "data:image/png,iVBORw0KGgo...",
			expected: "image/png",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractMediaType(tt.url)
			if result != tt.expected {
				t.Errorf("extractMediaType(%q) = %q, want %q", tt.url, result, tt.expected)
			}
		})
	}
}
