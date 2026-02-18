package sapaicore

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestConvertToBedrock_BasicMessage(t *testing.T) {
	t.Parallel()

	content := "Hello, world!"
	request := &schemas.BifrostChatRequest{
		Model: "anthropic--claude-3-sonnet",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: &content,
				},
			},
		},
	}

	result := convertToBedrock(request)

	if result == nil {
		t.Fatal("convertToBedrock returned nil")
	}
	if result.AnthropicVersion != "bedrock-2023-05-31" {
		t.Errorf("expected AnthropicVersion 'bedrock-2023-05-31', got %q", result.AnthropicVersion)
	}
	if len(result.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result.Messages))
	}
	if result.Messages[0].Role != "user" {
		t.Errorf("expected role 'user', got %q", result.Messages[0].Role)
	}
	if len(result.Messages[0].Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(result.Messages[0].Content))
	}
	if result.Messages[0].Content[0].Type != "text" {
		t.Errorf("expected content type 'text', got %q", result.Messages[0].Content[0].Type)
	}
	if result.Messages[0].Content[0].Text != content {
		t.Errorf("expected content text %q, got %q", content, result.Messages[0].Content[0].Text)
	}
}

func TestConvertToBedrock_SystemMessage(t *testing.T) {
	t.Parallel()

	systemContent := "You are a helpful assistant."
	userContent := "Hello!"
	request := &schemas.BifrostChatRequest{
		Model: "anthropic--claude-3-sonnet",
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

	result := convertToBedrock(request)

	if result.System != systemContent {
		t.Errorf("expected system message %q, got %q", systemContent, result.System)
	}
	// System message should not be in Messages array
	if len(result.Messages) != 1 {
		t.Errorf("expected 1 message (system excluded), got %d", len(result.Messages))
	}
}

func TestConvertToBedrock_WithParams(t *testing.T) {
	t.Parallel()

	content := "Hello!"
	temp := 0.7
	topP := 0.9
	maxTokens := 1000
	stop := []string{"stop1", "stop2"}

	request := &schemas.BifrostChatRequest{
		Model: "anthropic--claude-3-sonnet",
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

	result := convertToBedrock(request)

	if result.Temperature == nil || *result.Temperature != temp {
		t.Errorf("expected temperature %f, got %v", temp, result.Temperature)
	}
	if result.TopP == nil || *result.TopP != topP {
		t.Errorf("expected topP %f, got %v", topP, result.TopP)
	}
	if result.MaxTokens != maxTokens {
		t.Errorf("expected maxTokens %d, got %d", maxTokens, result.MaxTokens)
	}
	if len(result.StopSequences) != 2 {
		t.Errorf("expected 2 stop sequences, got %d", len(result.StopSequences))
	}
}

func TestConvertToBedrock_ContentBlocks(t *testing.T) {
	t.Parallel()

	textContent := "Describe this image"
	request := &schemas.BifrostChatRequest{
		Model: "anthropic--claude-3-sonnet",
		Input: []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentBlocks: []schemas.ChatContentBlock{
						{
							Type: schemas.ChatContentBlockTypeText,
							Text: &textContent,
						},
					},
				},
			},
		},
	}

	result := convertToBedrock(request)

	if len(result.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(result.Messages))
	}
	if len(result.Messages[0].Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(result.Messages[0].Content))
	}
	if result.Messages[0].Content[0].Type != "text" {
		t.Errorf("expected content type 'text', got %q", result.Messages[0].Content[0].Type)
	}
}

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

func TestMapBedrockStopReason(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "end_turn to stop",
			input:    "end_turn",
			expected: "stop",
		},
		{
			name:     "max_tokens to length",
			input:    "max_tokens",
			expected: "length",
		},
		{
			name:     "stop_sequence to stop",
			input:    "stop_sequence",
			expected: "stop",
		},
		{
			name:     "tool_use to tool_calls",
			input:    "tool_use",
			expected: "tool_calls",
		},
		{
			name:     "unknown passes through",
			input:    "unknown_reason",
			expected: "unknown_reason",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := mapBedrockStopReason(tt.input)
			if result != tt.expected {
				t.Errorf("mapBedrockStopReason(%q) = %q, want %q", tt.input, result, tt.expected)
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

func TestParseBedrockResponse(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"id": "msg_123",
		"type": "message",
		"role": "assistant",
		"content": [{"type": "text", "text": "Hello!"}],
		"model": "claude-3-sonnet",
		"stop_reason": "end_turn",
		"usage": {
			"input_tokens": 10,
			"output_tokens": 5
		}
	}`)

	result, err := parseBedrockResponse(body, "anthropic--claude-3-sonnet")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("parseBedrockResponse returned nil")
	}
	if result.ID != "msg_123" {
		t.Errorf("expected ID 'msg_123', got %q", result.ID)
	}
	if result.Model != "anthropic--claude-3-sonnet" {
		t.Errorf("expected model 'anthropic--claude-3-sonnet', got %q", result.Model)
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

func TestParseBedrockResponse_InvalidJSON(t *testing.T) {
	t.Parallel()

	body := []byte(`{invalid json`)

	_, err := parseBedrockResponse(body, "anthropic--claude-3-sonnet")

	if err == nil {
		t.Error("expected error for invalid JSON")
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

func TestGetModelConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		modelName         string
		expectedMaxTokens int
		expectedContext   int
	}{
		{
			name:              "known claude model",
			modelName:         "anthropic--claude-3-sonnet",
			expectedMaxTokens: 4096,
			expectedContext:   200000,
		},
		{
			name:              "known gemini model",
			modelName:         "gemini-1.5-pro",
			expectedMaxTokens: 8192,
			expectedContext:   2097152,
		},
		{
			name:              "known gpt model",
			modelName:         "gpt-4o",
			expectedMaxTokens: 16384,
			expectedContext:   128000,
		},
		{
			name:              "unknown model gets defaults",
			modelName:         "unknown-model",
			expectedMaxTokens: 8192,
			expectedContext:   200000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := GetModelConfig(tt.modelName)
			if config.MaxTokens != tt.expectedMaxTokens {
				t.Errorf("expected MaxTokens %d, got %d", tt.expectedMaxTokens, config.MaxTokens)
			}
			if config.ContextWindow != tt.expectedContext {
				t.Errorf("expected ContextWindow %d, got %d", tt.expectedContext, config.ContextWindow)
			}
		})
	}
}

func TestBedrockStreamEvent_Types(t *testing.T) {
	t.Parallel()

	// Test that BedrockStreamEvent struct can handle various event types
	textValue := "Hello"
	stopReason := "end_turn"

	event := BedrockStreamEvent{
		Delta: &BedrockDelta{
			Type: "text_delta",
			Text: &textValue,
		},
		StopReason: &stopReason,
		Usage: &BedrockUsage{
			InputTokens:  10,
			OutputTokens: 5,
		},
	}

	if event.Delta == nil {
		t.Error("expected Delta to be set")
	}
	if event.Delta.Type != "text_delta" {
		t.Errorf("expected Delta.Type 'text_delta', got %q", event.Delta.Type)
	}
	if *event.Delta.Text != textValue {
		t.Errorf("expected Delta.Text %q, got %q", textValue, *event.Delta.Text)
	}
	if *event.StopReason != stopReason {
		t.Errorf("expected StopReason %q, got %q", stopReason, *event.StopReason)
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
