package openai

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/internal/memtest"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestOpenAIChatRequest_UnmarshalJSON_BaseFieldsPreserved(t *testing.T) {
	tests := []struct {
		name        string
		jsonPayload string
		validate    func(t *testing.T, req *OpenAIChatRequest)
	}{
		{
			name: "all base fields preserved with ChatParameters",
			jsonPayload: `{
				"model": "gpt-4o",
				"messages": [
					{
						"role": "user",
						"content": "Hello, world!"
					}
				],
				"stream": true,
				"max_tokens": 100,
				"prompt_cache_isolation_key": "cache-key-1",
				"fallbacks": ["gpt-3.5-turbo"],
				"temperature": 0.7,
				"top_p": 0.9
			}`,
			validate: func(t *testing.T, req *OpenAIChatRequest) {
				// Assert base fields are preserved
				if req.Model != "gpt-4o" {
					t.Errorf("Expected Model to be 'gpt-4o', got %q", req.Model)
				}

				if len(req.Messages) != 1 {
					t.Fatalf("Expected 1 message, got %d", len(req.Messages))
				}
				if req.Messages[0].Role != schemas.ChatMessageRoleUser {
					t.Errorf("Expected message role to be 'user', got %q", req.Messages[0].Role)
				}
				if req.Messages[0].Content == nil || req.Messages[0].Content.ContentStr == nil {
					t.Fatal("Expected message content to be set")
				}
				if *req.Messages[0].Content.ContentStr != "Hello, world!" {
					t.Errorf("Expected message content to be 'Hello, world!', got %q", *req.Messages[0].Content.ContentStr)
				}

				if req.Stream == nil || !*req.Stream {
					t.Error("Expected Stream to be true")
				}

				if req.MaxTokens == nil || *req.MaxTokens != 100 {
					t.Errorf("Expected MaxTokens to be 100, got %v", req.MaxTokens)
				}

				if req.PromptCacheIsolationKey == nil || *req.PromptCacheIsolationKey != "cache-key-1" {
					t.Errorf("Expected PromptCacheIsolationKey to be %q, got %v", "cache-key-1", req.PromptCacheIsolationKey)
				}

				if len(req.Fallbacks) != 1 || req.Fallbacks[0] != "gpt-3.5-turbo" {
					t.Errorf("Expected Fallbacks to be ['gpt-3.5-turbo'], got %v", req.Fallbacks)
				}

				// Assert ChatParameters fields are populated
				if req.Temperature == nil || *req.Temperature != 0.7 {
					t.Errorf("Expected Temperature to be 0.7, got %v", req.Temperature)
				}

				if req.TopP == nil || *req.TopP != 0.9 {
					t.Errorf("Expected TopP to be 0.9, got %v", req.TopP)
				}
			},
		},
		{
			name: "base fields with multiple ChatParameters fields",
			jsonPayload: `{
				"model": "gpt-3.5-turbo",
				"messages": [
					{
						"role": "system",
						"content": "You are a helpful assistant."
					},
					{
						"role": "user",
						"content": "What is 2+2?"
					}
				],
				"stream": false,
				"max_tokens": 500,
				"fallbacks": ["gpt-4o", "gpt-4"],
				"temperature": 0.5,
				"top_p": 0.95,
				"frequency_penalty": 0.2,
				"presence_penalty": 0.3,
				"seed": 42,
				"stop": ["STOP", "END"]
			}`,
			validate: func(t *testing.T, req *OpenAIChatRequest) {
				// Assert base fields
				if req.Model != "gpt-3.5-turbo" {
					t.Errorf("Expected Model to be 'gpt-3.5-turbo', got %q", req.Model)
				}

				if len(req.Messages) != 2 {
					t.Fatalf("Expected 2 messages, got %d", len(req.Messages))
				}

				if req.Stream == nil || *req.Stream {
					t.Error("Expected Stream to be false")
				}

				if req.MaxTokens == nil || *req.MaxTokens != 500 {
					t.Errorf("Expected MaxTokens to be 500, got %v", req.MaxTokens)
				}

				if len(req.Fallbacks) != 2 {
					t.Errorf("Expected 2 fallbacks, got %d", len(req.Fallbacks))
				}

				// Assert multiple ChatParameters fields
				if req.Temperature == nil || *req.Temperature != 0.5 {
					t.Errorf("Expected Temperature to be 0.5, got %v", req.Temperature)
				}

				if req.TopP == nil || *req.TopP != 0.95 {
					t.Errorf("Expected TopP to be 0.95, got %v", req.TopP)
				}

				if req.FrequencyPenalty == nil || *req.FrequencyPenalty != 0.2 {
					t.Errorf("Expected FrequencyPenalty to be 0.2, got %v", req.FrequencyPenalty)
				}

				if req.PresencePenalty == nil || *req.PresencePenalty != 0.3 {
					t.Errorf("Expected PresencePenalty to be 0.3, got %v", req.PresencePenalty)
				}

				if req.Seed == nil || *req.Seed != 42 {
					t.Errorf("Expected Seed to be 42, got %v", req.Seed)
				}

				if len(req.Stop) != 2 {
					t.Errorf("Expected Stop to have 2 elements, got %d", len(req.Stop))
				}
			},
		},
		{
			name: "base fields with optional fields omitted",
			jsonPayload: `{
				"model": "gpt-4",
				"messages": [
					{
						"role": "user",
						"content": "Test"
					}
				],
				"temperature": 1.0,
				"top_p": 1.0
			}`,
			validate: func(t *testing.T, req *OpenAIChatRequest) {
				if req.Model != "gpt-4" {
					t.Errorf("Expected Model to be 'gpt-4', got %q", req.Model)
				}

				if len(req.Messages) != 1 {
					t.Fatalf("Expected 1 message, got %d", len(req.Messages))
				}

				// Optional fields should be nil/empty when omitted
				if req.Stream != nil {
					t.Error("Expected Stream to be nil when omitted")
				}

				if req.MaxTokens != nil {
					t.Error("Expected MaxTokens to be nil when omitted")
				}

				if len(req.Fallbacks) != 0 {
					t.Errorf("Expected Fallbacks to be empty when omitted, got %v", req.Fallbacks)
				}

				// ChatParameters fields should still be populated
				if req.Temperature == nil || *req.Temperature != 1.0 {
					t.Errorf("Expected Temperature to be 1.0, got %v", req.Temperature)
				}

				if req.TopP == nil || *req.TopP != 1.0 {
					t.Errorf("Expected TopP to be 1.0, got %v", req.TopP)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req OpenAIChatRequest
			if err := sonic.Unmarshal([]byte(tt.jsonPayload), &req); err != nil {
				t.Fatalf("Failed to unmarshal JSON: %v", err)
			}

			tt.validate(t, &req)
		})
	}
}

func TestOpenAIChatRequest_UnmarshalJSON_ChatParametersCustomLogic(t *testing.T) {
	tests := []struct {
		name        string
		jsonPayload string
		validate    func(t *testing.T, req *OpenAIChatRequest)
		expectError bool
	}{
		{
			name: "reasoning_effort converted to Reasoning.Effort",
			jsonPayload: `{
				"model": "gpt-4o",
				"messages": [
					{
						"role": "user",
						"content": "Think step by step"
					}
				],
				"reasoning_effort": "high",
				"temperature": 0.8
			}`,
			validate: func(t *testing.T, req *OpenAIChatRequest) {
				// Assert base fields are preserved
				if req.Model != "gpt-4o" {
					t.Errorf("Expected Model to be 'gpt-4o', got %q", req.Model)
				}

				// Assert reasoning_effort was converted to Reasoning.Effort
				if req.Reasoning == nil {
					t.Fatal("Expected Reasoning to be set from reasoning_effort")
				}
				if req.Reasoning.Effort == nil {
					t.Fatal("Expected Reasoning.Effort to be set")
				}
				if *req.Reasoning.Effort != "high" {
					t.Errorf("Expected Reasoning.Effort to be 'high', got %q", *req.Reasoning.Effort)
				}

				// Assert other ChatParameters fields are still populated
				if req.Temperature == nil || *req.Temperature != 0.8 {
					t.Errorf("Expected Temperature to be 0.8, got %v", req.Temperature)
				}
			},
			expectError: false,
		},
		{
			name: "both reasoning and reasoning_effort should error",
			jsonPayload: `{
				"model": "gpt-4o",
				"messages": [
					{
						"role": "user",
						"content": "Test"
					}
				],
				"reasoning": {
					"effort": "medium"
				},
				"reasoning_effort": "high"
			}`,
			validate: func(t *testing.T, req *OpenAIChatRequest) {
				// This should have failed during unmarshaling
			},
			expectError: true,
		},
		{
			name: "reasoning_effort with multiple ChatParameters fields",
			jsonPayload: `{
				"model": "gpt-4o",
				"messages": [
					{
						"role": "user",
						"content": "Analyze this"
					}
				],
				"reasoning_effort": "medium",
				"temperature": 0.6,
				"top_p": 0.85,
				"max_completion_tokens": 2000
			}`,
			validate: func(t *testing.T, req *OpenAIChatRequest) {
				// Assert base fields
				if req.Model != "gpt-4o" {
					t.Errorf("Expected Model to be 'gpt-4o', got %q", req.Model)
				}

				// Assert reasoning_effort conversion
				if req.Reasoning == nil || req.Reasoning.Effort == nil {
					t.Fatal("Expected Reasoning.Effort to be set from reasoning_effort")
				}
				if *req.Reasoning.Effort != "medium" {
					t.Errorf("Expected Reasoning.Effort to be 'medium', got %q", *req.Reasoning.Effort)
				}

				// Assert other ChatParameters fields
				if req.Temperature == nil || *req.Temperature != 0.6 {
					t.Errorf("Expected Temperature to be 0.6, got %v", req.Temperature)
				}
				if req.TopP == nil || *req.TopP != 0.85 {
					t.Errorf("Expected TopP to be 0.85, got %v", req.TopP)
				}
				if req.MaxCompletionTokens == nil || *req.MaxCompletionTokens != 2000 {
					t.Errorf("Expected MaxCompletionTokens to be 2000, got %v", req.MaxCompletionTokens)
				}
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var req OpenAIChatRequest
			err := sonic.Unmarshal([]byte(tt.jsonPayload), &req)

			if tt.expectError {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error during unmarshaling: %v", err)
			}

			tt.validate(t, &req)
		})
	}
}

func TestOpenAIChatRequest_UnmarshalJSON_PresenceAssertions(t *testing.T) {
	// Test that verifies presence of fields (not just values)
	jsonPayload := `{
		"model": "gpt-4o-mini",
		"messages": [
			{
				"role": "assistant",
				"content": "Hello!"
			}
		],
		"stream": false,
		"max_tokens": 150,
		"fallbacks": ["model1", "model2"],
		"temperature": 0.3,
		"top_p": 0.7,
		"user": "test-user-123"
	}`

	var req OpenAIChatRequest
	if err := sonic.Unmarshal([]byte(jsonPayload), &req); err != nil {
		t.Fatalf("Failed to unmarshal JSON: %v", err)
	}

	// Presence assertions for base fields
	if req.Model == "" {
		t.Error("Model field should be present")
	}

	if len(req.Messages) == 0 {
		t.Error("Messages field should be present and non-empty")
	}

	if req.Stream == nil {
		t.Error("Stream field should be present (even if false)")
	}

	if req.MaxTokens == nil {
		t.Error("MaxTokens field should be present")
	}

	if len(req.Fallbacks) == 0 {
		t.Error("Fallbacks field should be present and non-empty")
	}

	// Presence assertions for ChatParameters fields
	if req.Temperature == nil {
		t.Error("Temperature field should be present")
	}

	if req.TopP == nil {
		t.Error("TopP field should be present")
	}

	if req.User == nil {
		t.Error("User field should be present")
	}
}

func TestOpenAIChatRequest_UnmarshalJSON_ValueAssertions(t *testing.T) {
	// Test that verifies exact values match expectations
	jsonPayload := `{
		"model": "gpt-4-turbo",
		"messages": [
			{
				"role": "system",
				"content": "System message"
			},
			{
				"role": "user",
				"content": "User message"
			}
		],
		"stream": true,
		"max_tokens": 250,
		"fallbacks": ["fallback1"],
		"temperature": 0.9,
		"top_p": 0.95,
		"seed": 12345,
		"stop": ["END", "STOP"]
	}`

	var req OpenAIChatRequest
	if err := sonic.Unmarshal([]byte(jsonPayload), &req); err != nil {
		t.Fatalf("Failed to unmarshal JSON: %v", err)
	}

	// Value assertions for base fields
	if req.Model != "gpt-4-turbo" {
		t.Errorf("Expected Model value 'gpt-4-turbo', got %q", req.Model)
	}

	if len(req.Messages) != 2 {
		t.Fatalf("Expected 2 messages, got %d", len(req.Messages))
	}
	if req.Messages[0].Role != schemas.ChatMessageRoleSystem {
		t.Errorf("Expected first message role 'system', got %q", req.Messages[0].Role)
	}
	if req.Messages[1].Role != schemas.ChatMessageRoleUser {
		t.Errorf("Expected second message role 'user', got %q", req.Messages[1].Role)
	}

	if req.Stream == nil || !*req.Stream {
		t.Error("Expected Stream value to be true")
	}

	if req.MaxTokens == nil || *req.MaxTokens != 250 {
		t.Errorf("Expected MaxTokens value 250, got %v", req.MaxTokens)
	}

	if len(req.Fallbacks) != 1 || req.Fallbacks[0] != "fallback1" {
		t.Errorf("Expected Fallbacks value ['fallback1'], got %v", req.Fallbacks)
	}

	// Value assertions for ChatParameters fields
	if req.Temperature == nil || *req.Temperature != 0.9 {
		t.Errorf("Expected Temperature value 0.9, got %v", req.Temperature)
	}

	if req.TopP == nil || *req.TopP != 0.95 {
		t.Errorf("Expected TopP value 0.95, got %v", req.TopP)
	}

	if req.Seed == nil || *req.Seed != 12345 {
		t.Errorf("Expected Seed value 12345, got %v", req.Seed)
	}

	if len(req.Stop) != 2 || req.Stop[0] != "END" || req.Stop[1] != "STOP" {
		t.Errorf("Expected Stop value ['END', 'STOP'], got %v", req.Stop)
	}
}

// TestStripCompactionItemSummary_AllocationScaling pins the allocation shape of the
// compaction-summary strip.
//
// The loop deletes "<i>.summary" through the whole input array, once per compaction
// item, and each sjson delete reserialises the entire array. A conversation carrying
// many compaction items therefore costs that many copies of it.
func TestStripCompactionItemSummary_AllocationScaling(t *testing.T) {
	// items must line up with the payload, so both are built together and looked up
	// by payload length when the assertion calls back.
	itemsByLen := map[int][]schemas.ResponsesMessage{}

	memtest.AssertAllocScaling(t, func(n int) []byte {
		var b bytes.Buffer
		items := make([]schemas.ResponsesMessage, 0, n)
		compaction := schemas.ResponsesMessageTypeCompaction
		b.WriteByte('[')
		for i := range n {
			if i > 0 {
				b.WriteByte(',')
			}
			// encrypted_content is the bulk, so the payload grows with n; summary is
			// what gets stripped.
			b.WriteString(`{"type":"compaction","summary":null,"encrypted_content":"`)
			b.WriteString(strings.Repeat("e", 400))
			b.WriteString(`"}`)
			items = append(items, schemas.ResponsesMessage{Type: &compaction})
		}
		b.WriteByte(']')
		payload := b.Bytes()
		itemsByLen[len(payload)] = items
		return payload
	}, func(body []byte) {
		stripCompactionItemSummary(body, itemsByLen[len(body)])
	})
}

// TestOpenAIChatRequest_MarshalJSON_FoldsFileTypeIntoDataURL pins that file_type, a Bifrost
// extension the OpenAI wire format lacks, is carried into file_data rather than dropped.
// OpenAI-compatible upstreams accept file_data only as a data URL, so raw base64 (Bifrost's
// canonical file shape) with its media type stripped was rejected with a 400 (#7539).
func TestOpenAIChatRequest_MarshalJSON_FoldsFileTypeIntoDataURL(t *testing.T) {
	const pdfBase64 = "JVBERi0xLjQK"
	tests := []struct {
		name         string
		fileData     string
		fileType     *string
		wantFileData string
	}{
		{"raw base64 is folded into a data URL", pdfBase64, schemas.Ptr("application/pdf"), "data:application/pdf;base64," + pdfBase64},
		{"existing data URL is unchanged", "data:application/pdf;base64," + pdfBase64, schemas.Ptr("application/pdf"), "data:application/pdf;base64," + pdfBase64},
		{"literal text is base64-encoded", "hello", schemas.Ptr("text/plain"), "data:text/plain;base64,aGVsbG8="},
		{"no file_type leaves file_data unchanged", pdfBase64, nil, pdfBase64},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := &schemas.ChatInputFile{FileData: schemas.Ptr(tt.fileData), FileType: tt.fileType}
			req := &OpenAIChatRequest{
				Model: "gpt-4o-mini",
				Messages: []OpenAIMessage{{
					Role: schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
						{Type: schemas.ChatContentBlockTypeFile, File: file},
					}},
				}},
			}

			jsonBytes, err := req.MarshalJSON()
			if err != nil {
				t.Fatalf("Failed to marshal: %v", err)
			}
			var wire struct {
				Messages []struct {
					Content []struct {
						File map[string]interface{} `json:"file"`
					} `json:"content"`
				} `json:"messages"`
			}
			if err := sonic.Unmarshal(jsonBytes, &wire); err != nil {
				t.Fatalf("Failed to unmarshal: %v\nraw=%s", err, string(jsonBytes))
			}
			if len(wire.Messages) != 1 || len(wire.Messages[0].Content) != 1 {
				t.Fatalf("Expected one message with one content block; raw=%s", string(jsonBytes))
			}
			wireFile := wire.Messages[0].Content[0].File
			if wireFile["file_data"] != tt.wantFileData {
				t.Errorf("Expected file_data %q, got %v", tt.wantFileData, wireFile["file_data"])
			}
			if _, ok := wireFile["file_type"]; ok {
				t.Errorf("file_type must not reach the OpenAI wire; raw=%s", string(jsonBytes))
			}
			if *file.FileData != tt.fileData {
				t.Errorf("Original request was mutated: file_data %q", *file.FileData)
			}
		})
	}
}

// TestOpenAIResponsesRequestInput_MarshalJSON_FoldsFileTypeIntoDataURL is the Responses
// counterpart: input_file blocks in message content and in function_call_output blocks.
func TestOpenAIResponsesRequestInput_MarshalJSON_FoldsFileTypeIntoDataURL(t *testing.T) {
	const pdfBase64 = "JVBERi0xLjQK"
	newFileBlock := func() schemas.ResponsesMessageContentBlock {
		return schemas.ResponsesMessageContentBlock{
			Type: schemas.ResponsesInputMessageContentBlockTypeFile,
			ResponsesInputMessageContentBlockFile: &schemas.ResponsesInputMessageContentBlockFile{
				Filename: schemas.Ptr("marker.pdf"),
				FileData: schemas.Ptr(pdfBase64),
				FileType: schemas.Ptr("application/pdf"),
			},
		}
	}
	tests := []struct {
		name     string
		message  schemas.ResponsesMessage
		blockKey string
	}{
		{
			name: "input message content block",
			message: schemas.ResponsesMessage{
				Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{newFileBlock()}},
			},
			blockKey: "content",
		},
		{
			name: "function_call_output block",
			message: schemas.ResponsesMessage{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: schemas.Ptr("call_file"),
					Output: &schemas.ResponsesToolMessageOutputStruct{
						ResponsesFunctionToolCallOutputBlocks: []schemas.ResponsesMessageContentBlock{newFileBlock()},
					},
				},
			},
			blockKey: "output",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := &OpenAIResponsesRequestInput{OpenAIResponsesRequestInputArray: []schemas.ResponsesMessage{tt.message}}

			jsonBytes, err := input.MarshalJSON()
			if err != nil {
				t.Fatalf("Failed to marshal: %v", err)
			}
			var messages []map[string]interface{}
			if err := sonic.Unmarshal(jsonBytes, &messages); err != nil {
				t.Fatalf("Failed to unmarshal: %v\nraw=%s", err, string(jsonBytes))
			}
			blocks, ok := messages[0][tt.blockKey].([]interface{})
			if !ok || len(blocks) != 1 {
				t.Fatalf("Expected one %s block; raw=%s", tt.blockKey, string(jsonBytes))
			}
			block, ok := blocks[0].(map[string]interface{})
			if !ok {
				t.Fatalf("Expected an object block; raw=%s", string(jsonBytes))
			}
			if want := "data:application/pdf;base64," + pdfBase64; block["file_data"] != want {
				t.Errorf("Expected file_data %q, got %v", want, block["file_data"])
			}
			if _, ok := block["file_type"]; ok {
				t.Errorf("file_type must not reach the OpenAI wire; raw=%s", string(jsonBytes))
			}
		})
	}
}
