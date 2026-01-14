package anthropic

import (
	"testing"

	"github.com/bytedance/sonic"
	"github.com/google/go-cmp/cmp"
	"github.com/maximhq/bifrost/core/schemas"
)

// Helper functions
func strPtr(s string) *string       { return &s }
func intPtr(i int) *int             { return &i }
func boolPtr(b bool) *bool          { return &b }
func float64Ptr(f float64) *float64 { return &f }

// =============================================================================
// Bifrost → Anthropic Request Conversion
// =============================================================================

func TestToAnthropicChatRequest(t *testing.T) {
	tests := []struct {
		name  string
		input *schemas.BifrostChatRequest
		want  *AnthropicMessageRequest
	}{
		{
			name: "basic fields",
			input: &schemas.BifrostChatRequest{
				Provider: schemas.Anthropic,
				Model:    "claude-3-5-sonnet-20241022",
				Input: []schemas.ChatMessage{
					{
						Role:    schemas.ChatMessageRoleUser,
						Content: &schemas.ChatMessageContent{ContentStr: strPtr("Hello")},
					},
				},
				Params: &schemas.ChatParameters{
					MaxCompletionTokens: intPtr(1024),
					Temperature:         float64Ptr(0.7),
				},
			},
			want: &AnthropicMessageRequest{
				Model:       "claude-3-5-sonnet-20241022",
				MaxTokens:   1024,
				Temperature: float64Ptr(0.7),
				Messages: []AnthropicMessage{
					{Role: "user", Content: AnthropicContent{ContentStr: strPtr("Hello")}},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ToAnthropicChatRequest(tt.input)
			if err != nil {
				t.Fatalf("ToAnthropicChatRequest failed: %v", err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// =============================================================================
// Anthropic → Bifrost Response Conversion
// =============================================================================

func TestToBifrostChatResponse(t *testing.T) {
	tests := []struct {
		name      string
		response  *AnthropicMessageResponse
		wantUsage *schemas.BifrostLLMUsage
	}{
		{
			name: "basic response",
			response: &AnthropicMessageResponse{
				ID:         "msg_123",
				Type:       "message",
				Role:       "assistant",
				Model:      "claude-3-5-sonnet-20241022",
				StopReason: AnthropicStopReasonEndTurn,
				Usage: &AnthropicUsage{
					InputTokens:  10,
					OutputTokens: 20,
				},
			},
			wantUsage: &schemas.BifrostLLMUsage{
				PromptTokens:            10,
				PromptTokensDetails:     &schemas.ChatPromptTokensDetails{},
				CompletionTokens:        20,
				CompletionTokensDetails: &schemas.ChatCompletionTokensDetails{},
				TotalTokens:             30,
			},
		},
		{
			name: "server_tool_use maps to NumSearchQueries",
			response: &AnthropicMessageResponse{
				ID:         "msg_123",
				Type:       "message",
				Role:       "assistant",
				Model:      "claude-3-5-sonnet-20241022",
				StopReason: AnthropicStopReasonEndTurn,
				Usage: &AnthropicUsage{
					InputTokens:   10,
					OutputTokens:  20,
					ServerToolUse: &AnthropicServerToolUsage{WebSearchRequests: 3},
					ServiceTier:   "standard",
				},
			},
			wantUsage: &schemas.BifrostLLMUsage{
				PromptTokens:            10,
				PromptTokensDetails:     &schemas.ChatPromptTokensDetails{},
				CompletionTokens:        20,
				CompletionTokensDetails: &schemas.ChatCompletionTokensDetails{NumSearchQueries: intPtr(3)},
				TotalTokens:             30,
			},
		},
		{
			name: "cache read and creation tokens",
			response: &AnthropicMessageResponse{
				ID:         "msg_123",
				Type:       "message",
				Role:       "assistant",
				Model:      "claude-3-5-sonnet-20241022",
				StopReason: AnthropicStopReasonEndTurn,
				Usage: &AnthropicUsage{
					InputTokens:              1000,
					OutputTokens:             500,
					CacheReadInputTokens:     100,
					CacheCreationInputTokens: 200,
				},
			},
			wantUsage: &schemas.BifrostLLMUsage{
				PromptTokens: 1000,
				PromptTokensDetails: &schemas.ChatPromptTokensDetails{
					CachedTokens:        100,
					CacheReadTokens:     100,
					CacheCreationTokens: 200,
				},
				CompletionTokens:        500,
				CompletionTokensDetails: &schemas.ChatCompletionTokensDetails{CachedTokens: 200},
				TotalTokens:             1500,
			},
		},
		{
			name: "ephemeral TTL tokens",
			response: &AnthropicMessageResponse{
				ID:         "msg_123",
				Type:       "message",
				Role:       "assistant",
				Model:      "claude-3-5-sonnet-20241022",
				StopReason: AnthropicStopReasonEndTurn,
				Usage: &AnthropicUsage{
					InputTokens:              1000,
					OutputTokens:             500,
					CacheCreationInputTokens: 200,
					CacheCreation: AnthropicUsageCacheCreation{
						Ephemeral5mInputTokens: 150,
						Ephemeral1hInputTokens: 50,
					},
				},
			},
			wantUsage: &schemas.BifrostLLMUsage{
				PromptTokens: 1000,
				PromptTokensDetails: &schemas.ChatPromptTokensDetails{
					CacheCreationTokens: 200,
					CacheCreation: &schemas.CacheCreationTokens{
						Ephemeral5mInputTokens: 150,
						Ephemeral1hInputTokens: 50,
					},
				},
				CompletionTokens:        500,
				CompletionTokensDetails: &schemas.ChatCompletionTokensDetails{CachedTokens: 200},
				TotalTokens:             1500,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.response.ToBifrostChatResponse()
			if diff := cmp.Diff(tt.wantUsage, got.Usage); diff != "" {
				t.Errorf("Usage mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// =============================================================================
// Anthropic Type JSON Parsing
// =============================================================================

func TestAnthropicToolWebSearchUserLocation(t *testing.T) {
	tests := []struct {
		name        string
		jsonPayload string
		want        AnthropicToolWebSearchUserLocation
	}{
		{
			name: "all fields",
			jsonPayload: `{
				"type": "approximate",
				"city": "San Francisco",
				"region": "California",
				"country": "US",
				"timezone": "America/Los_Angeles"
			}`,
			want: AnthropicToolWebSearchUserLocation{
				Type:     strPtr("approximate"),
				City:     strPtr("San Francisco"),
				Region:   strPtr("California"),
				Country:  strPtr("US"),
				Timezone: strPtr("America/Los_Angeles"),
			},
		},
		{
			name: "region omitted",
			jsonPayload: `{
				"type": "approximate",
				"city": "London",
				"country": "UK"
			}`,
			want: AnthropicToolWebSearchUserLocation{
				Type:    strPtr("approximate"),
				City:    strPtr("London"),
				Country: strPtr("UK"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got AnthropicToolWebSearchUserLocation
			if err := sonic.Unmarshal([]byte(tt.jsonPayload), &got); err != nil {
				t.Fatalf("Failed to unmarshal: %v", err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestAnthropicUsage(t *testing.T) {
	tests := []struct {
		name        string
		jsonPayload string
		want        AnthropicUsage
	}{
		{
			name: "with server_tool_use and service_tier",
			jsonPayload: `{
				"input_tokens": 100,
				"output_tokens": 50,
				"cache_creation_input_tokens": 10,
				"cache_read_input_tokens": 5,
				"server_tool_use": {"web_search_requests": 3},
				"service_tier": "standard"
			}`,
			want: AnthropicUsage{
				InputTokens:              100,
				OutputTokens:             50,
				CacheCreationInputTokens: 10,
				CacheReadInputTokens:     5,
				ServerToolUse:            &AnthropicServerToolUsage{WebSearchRequests: 3},
				ServiceTier:              "standard",
			},
		},
		{
			name:        "minimal",
			jsonPayload: `{"input_tokens": 100, "output_tokens": 50}`,
			want: AnthropicUsage{
				InputTokens:  100,
				OutputTokens: 50,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got AnthropicUsage
			if err := sonic.Unmarshal([]byte(tt.jsonPayload), &got); err != nil {
				t.Fatalf("Failed to unmarshal: %v", err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestAnthropicContentBlock_ToolResult(t *testing.T) {
	tests := []struct {
		name        string
		jsonPayload string
		want        AnthropicContentBlock
	}{
		{
			name:        "is_error true",
			jsonPayload: `{"type": "tool_result", "tool_use_id": "tool_123", "is_error": true}`,
			want: AnthropicContentBlock{
				Type:      AnthropicContentBlockTypeToolResult,
				ToolUseID: strPtr("tool_123"),
				IsError:   boolPtr(true),
			},
		},
		{
			name:        "is_error false",
			jsonPayload: `{"type": "tool_result", "tool_use_id": "tool_123", "is_error": false}`,
			want: AnthropicContentBlock{
				Type:      AnthropicContentBlockTypeToolResult,
				ToolUseID: strPtr("tool_123"),
				IsError:   boolPtr(false),
			},
		},
		{
			name:        "is_error omitted",
			jsonPayload: `{"type": "tool_result", "tool_use_id": "tool_123"}`,
			want: AnthropicContentBlock{
				Type:      AnthropicContentBlockTypeToolResult,
				ToolUseID: strPtr("tool_123"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got AnthropicContentBlock
			if err := sonic.Unmarshal([]byte(tt.jsonPayload), &got); err != nil {
				t.Fatalf("Failed to unmarshal: %v", err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestAnthropicTextCitation(t *testing.T) {
	tests := []struct {
		name        string
		jsonPayload string
		want        AnthropicTextCitation
	}{
		{
			name: "char_location",
			jsonPayload: `{
				"type": "char_location",
				"cited_text": "test quote",
				"document_index": 0,
				"document_title": "Test Doc",
				"start_char_index": 10,
				"end_char_index": 20
			}`,
			want: AnthropicTextCitation{
				Type:           "char_location",
				CitedText:      "test quote",
				DocumentIndex:  intPtr(0),
				DocumentTitle:  "Test Doc",
				StartCharIndex: intPtr(10),
				EndCharIndex:   intPtr(20),
			},
		},
		{
			name: "page_location",
			jsonPayload: `{
				"type": "page_location",
				"cited_text": "page quote",
				"document_index": 1,
				"start_page_number": 5,
				"end_page_number": 7
			}`,
			want: AnthropicTextCitation{
				Type:            "page_location",
				CitedText:       "page quote",
				DocumentIndex:   intPtr(1),
				StartPageNumber: intPtr(5),
				EndPageNumber:   intPtr(7),
			},
		},
		{
			name: "web_search_result_location",
			jsonPayload: `{
				"type": "web_search_result_location",
				"cited_text": "web quote",
				"url": "https://example.com",
				"title": "Example Page"
			}`,
			want: AnthropicTextCitation{
				Type:      "web_search_result_location",
				CitedText: "web quote",
				URL:       "https://example.com",
				Title:     "Example Page",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got AnthropicTextCitation
			if err := sonic.Unmarshal([]byte(tt.jsonPayload), &got); err != nil {
				t.Fatalf("Failed to unmarshal: %v", err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestAnthropicTool_BetaFields(t *testing.T) {
	tests := []struct {
		name        string
		jsonPayload string
		want        AnthropicTool
	}{
		{
			name: "all beta fields",
			jsonPayload: `{
				"name": "test_tool",
				"description": "A test tool",
				"strict": true,
				"allowed_callers": ["direct", "code_execution_20250825"],
				"input_examples": [{"key": "value"}],
				"defer_loading": true
			}`,
			want: AnthropicTool{
				Name:           "test_tool",
				Description:    strPtr("A test tool"),
				Strict:         boolPtr(true),
				AllowedCallers: []string{"direct", "code_execution_20250825"},
				InputExamples:  []map[string]interface{}{{"key": "value"}},
				DeferLoading:   boolPtr(true),
			},
		},
		{
			name: "beta fields omitted",
			jsonPayload: `{
				"name": "test_tool",
				"description": "A test tool"
			}`,
			want: AnthropicTool{
				Name:        "test_tool",
				Description: strPtr("A test tool"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got AnthropicTool
			if err := sonic.Unmarshal([]byte(tt.jsonPayload), &got); err != nil {
				t.Fatalf("Failed to unmarshal: %v", err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestAnthropicMessageRequest_ServiceTier(t *testing.T) {
	tests := []struct {
		name        string
		jsonPayload string
		want        *string
	}{
		{
			name: "auto",
			jsonPayload: `{
				"model": "claude-3-5-sonnet-20241022",
				"max_tokens": 1024,
				"messages": [{"role": "user", "content": "Hello"}],
				"service_tier": "auto"
			}`,
			want: strPtr("auto"),
		},
		{
			name: "standard_only",
			jsonPayload: `{
				"model": "claude-3-5-sonnet-20241022",
				"max_tokens": 1024,
				"messages": [{"role": "user", "content": "Hello"}],
				"service_tier": "standard_only"
			}`,
			want: strPtr("standard_only"),
		},
		{
			name: "omitted",
			jsonPayload: `{
				"model": "claude-3-5-sonnet-20241022",
				"max_tokens": 1024,
				"messages": [{"role": "user", "content": "Hello"}]
			}`,
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got AnthropicMessageRequest
			if err := sonic.Unmarshal([]byte(tt.jsonPayload), &got); err != nil {
				t.Fatalf("Failed to unmarshal: %v", err)
			}
			if diff := cmp.Diff(tt.want, got.ServiceTier); diff != "" {
				t.Errorf("ServiceTier mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// =============================================================================
// Cache Token Tests
// =============================================================================

func TestAnthropicUsage_CacheCreationTokens(t *testing.T) {
	tests := []struct {
		name        string
		jsonPayload string
		want        AnthropicUsage
	}{
		{
			name: "with ephemeral TTL tokens",
			jsonPayload: `{
				"input_tokens": 1000,
				"output_tokens": 500,
				"cache_creation_input_tokens": 200,
				"cache_read_input_tokens": 100,
				"cache_creation": {
					"ephemeral_5m_input_tokens": 150,
					"ephemeral_1h_input_tokens": 50
				}
			}`,
			want: AnthropicUsage{
				InputTokens:              1000,
				OutputTokens:             500,
				CacheCreationInputTokens: 200,
				CacheReadInputTokens:     100,
				CacheCreation: AnthropicUsageCacheCreation{
					Ephemeral5mInputTokens: 150,
					Ephemeral1hInputTokens: 50,
				},
			},
		},
		{
			name: "only 5m ephemeral tokens",
			jsonPayload: `{
				"input_tokens": 500,
				"output_tokens": 200,
				"cache_creation_input_tokens": 100,
				"cache_creation": {
					"ephemeral_5m_input_tokens": 100
				}
			}`,
			want: AnthropicUsage{
				InputTokens:              500,
				OutputTokens:             200,
				CacheCreationInputTokens: 100,
				CacheCreation: AnthropicUsageCacheCreation{
					Ephemeral5mInputTokens: 100,
				},
			},
		},
		{
			name: "only 1h ephemeral tokens",
			jsonPayload: `{
				"input_tokens": 500,
				"output_tokens": 200,
				"cache_creation_input_tokens": 100,
				"cache_creation": {
					"ephemeral_1h_input_tokens": 100
				}
			}`,
			want: AnthropicUsage{
				InputTokens:              500,
				OutputTokens:             200,
				CacheCreationInputTokens: 100,
				CacheCreation: AnthropicUsageCacheCreation{
					Ephemeral1hInputTokens: 100,
				},
			},
		},
		{
			name:        "no cache tokens",
			jsonPayload: `{"input_tokens": 100, "output_tokens": 50}`,
			want: AnthropicUsage{
				InputTokens:  100,
				OutputTokens: 50,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got AnthropicUsage
			if err := sonic.Unmarshal([]byte(tt.jsonPayload), &got); err != nil {
				t.Fatalf("Failed to unmarshal: %v", err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// =============================================================================
// Responses API Conversion Tests
// =============================================================================

func TestToBifrostResponsesResponse(t *testing.T) {
	tests := []struct {
		name                    string
		response                *AnthropicMessageResponse
		wantInputTokensDetails  *schemas.ResponsesResponseInputTokens
		wantOutputTokensDetails *schemas.ResponsesResponseOutputTokens
	}{
		{
			name: "cache read and creation tokens",
			response: &AnthropicMessageResponse{
				ID:         "msg_123",
				Type:       "message",
				Role:       "assistant",
				Model:      "claude-3-5-sonnet-20241022",
				StopReason: AnthropicStopReasonEndTurn,
				Usage: &AnthropicUsage{
					InputTokens:              1000,
					OutputTokens:             500,
					CacheReadInputTokens:     100,
					CacheCreationInputTokens: 200,
				},
			},
			wantInputTokensDetails: &schemas.ResponsesResponseInputTokens{
				CachedTokens:        100,
				CacheReadTokens:     100,
				CacheCreationTokens: 200,
			},
			wantOutputTokensDetails: &schemas.ResponsesResponseOutputTokens{
				CachedTokens: 200,
			},
		},
		{
			name: "ephemeral TTL tokens",
			response: &AnthropicMessageResponse{
				ID:         "msg_123",
				Type:       "message",
				Role:       "assistant",
				Model:      "claude-3-5-sonnet-20241022",
				StopReason: AnthropicStopReasonEndTurn,
				Usage: &AnthropicUsage{
					InputTokens:              1000,
					OutputTokens:             500,
					CacheCreationInputTokens: 200,
					CacheCreation: AnthropicUsageCacheCreation{
						Ephemeral5mInputTokens: 150,
						Ephemeral1hInputTokens: 50,
					},
				},
			},
			wantInputTokensDetails: &schemas.ResponsesResponseInputTokens{
				CacheCreationTokens: 200,
				CacheCreation: &schemas.CacheCreationTokens{
					Ephemeral5mInputTokens: 150,
					Ephemeral1hInputTokens: 50,
				},
			},
			wantOutputTokensDetails: &schemas.ResponsesResponseOutputTokens{
				CachedTokens: 200,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.response.ToBifrostResponsesResponse()
			if got.Usage == nil {
				t.Fatal("Usage is nil")
			}
			if diff := cmp.Diff(tt.wantInputTokensDetails, got.Usage.InputTokensDetails); diff != "" {
				t.Errorf("InputTokensDetails mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantOutputTokensDetails, got.Usage.OutputTokensDetails); diff != "" {
				t.Errorf("OutputTokensDetails mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// =============================================================================
// Tool Conversion Tests
// =============================================================================

func TestConvertAnthropicToolToBifrost(t *testing.T) {
	tests := []struct {
		name string
		tool *AnthropicTool
		want *schemas.ResponsesTool
	}{
		{
			name: "tool_search_tool_bm25_20251119 preserves type and cache_control",
			tool: &AnthropicTool{
				Name:         "tool_search_tool_bm25",
				Type:         func() *AnthropicToolType { t := AnthropicToolTypeToolSearchBm25_20251119; return &t }(),
				CacheControl: &schemas.CacheControl{Type: "ephemeral"},
			},
			want: &schemas.ResponsesTool{
				Type:         schemas.ResponsesToolType(AnthropicToolTypeToolSearchBm25_20251119),
				Name:         strPtr("tool_search_tool_bm25"),
				CacheControl: &schemas.CacheControl{Type: "ephemeral"},
			},
		},
		{
			name: "text_editor_20250728 preserves type, cache_control and max_characters",
			tool: &AnthropicTool{
				Name:          "str_replace_based_edit_tool",
				Type:          func() *AnthropicToolType { t := AnthropicToolTypeTextEditor20250728; return &t }(),
				CacheControl:  &schemas.CacheControl{Type: "ephemeral"},
				MaxCharacters: func() *int64 { v := int64(20000); return &v }(),
			},
			want: &schemas.ResponsesTool{
				Type:          schemas.ResponsesToolType(AnthropicToolTypeTextEditor20250728),
				Name:          strPtr("str_replace_based_edit_tool"),
				CacheControl:  &schemas.CacheControl{Type: "ephemeral"},
				MaxCharacters: func() *int64 { v := int64(20000); return &v }(),
			},
		},
		{
			name: "custom tool with input_schema and defer_loading",
			tool: &AnthropicTool{
				Name:         "my_custom_tool",
				Description:  strPtr("A custom tool"),
				DeferLoading: func() *bool { v := true; return &v }(),
				InputSchema: &schemas.ToolFunctionParameters{
					Type: "object",
				},
			},
			want: &schemas.ResponsesTool{
				Type:         schemas.ResponsesToolTypeFunction,
				Name:         strPtr("my_custom_tool"),
				Description:  strPtr("A custom tool"),
				DeferLoading: func() *bool { v := true; return &v }(),
				ResponsesToolFunction: &schemas.ResponsesToolFunction{
					Parameters: &schemas.ToolFunctionParameters{
						Type: "object",
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convertAnthropicToolToBifrost(tt.tool)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestConvertBifrostToolToAnthropic(t *testing.T) {
	tests := []struct {
		name  string
		model string
		tool  *schemas.ResponsesTool
		want  *AnthropicTool
	}{
		{
			name:  "tool_search_tool_bm25_20251119 preserves type, name and cache_control",
			model: "claude-sonnet-4-0",
			tool: &schemas.ResponsesTool{
				Type:         schemas.ResponsesToolType(AnthropicToolTypeToolSearchBm25_20251119),
				Name:         strPtr("tool_search_tool_bm25"),
				CacheControl: &schemas.CacheControl{Type: "ephemeral"},
			},
			want: &AnthropicTool{
				Type:         func() *AnthropicToolType { t := AnthropicToolTypeToolSearchBm25_20251119; return &t }(),
				Name:         string(AnthropicToolNameToolSearchBm25),
				CacheControl: &schemas.CacheControl{Type: "ephemeral"},
			},
		},
		{
			name:  "text_editor_20250728 preserves type, cache_control and max_characters",
			model: "claude-sonnet-4-0",
			tool: &schemas.ResponsesTool{
				Type:          schemas.ResponsesToolType(AnthropicToolTypeTextEditor20250728),
				Name:          strPtr("str_replace_based_edit_tool"),
				CacheControl:  &schemas.CacheControl{Type: "ephemeral"},
				MaxCharacters: func() *int64 { v := int64(20000); return &v }(),
			},
			want: &AnthropicTool{
				Type:          func() *AnthropicToolType { t := AnthropicToolTypeTextEditor20250728; return &t }(),
				Name:          string(AnthropicToolNameTextEditor),
				CacheControl:  &schemas.CacheControl{Type: "ephemeral"},
				MaxCharacters: func() *int64 { v := int64(20000); return &v }(),
			},
		},
		{
			name:  "function tool becomes custom with input_schema and defer_loading",
			model: "claude-sonnet-4-0",
			tool: &schemas.ResponsesTool{
				Type:         schemas.ResponsesToolTypeFunction,
				Name:         strPtr("my_custom_tool"),
				Description:  strPtr("A custom tool"),
				DeferLoading: func() *bool { v := true; return &v }(),
				ResponsesToolFunction: &schemas.ResponsesToolFunction{
					Parameters: &schemas.ToolFunctionParameters{
						Type: "object",
					},
				},
			},
			want: &AnthropicTool{
				Type:         func() *AnthropicToolType { t := AnthropicToolTypeCustom; return &t }(),
				Name:         "my_custom_tool",
				Description:  strPtr("A custom tool"),
				DeferLoading: func() *bool { v := true; return &v }(),
				InputSchema: &schemas.ToolFunctionParameters{
					Type: "object",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convertBifrostToolToAnthropic(tt.model, tt.tool)
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestToolRoundTrip verifies that built-in Anthropic tools survive the
// Anthropic -> Bifrost -> Anthropic conversion without losing their type.
func TestToolRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		tool *AnthropicTool
	}{
		{
			name: "tool_search_tool_bm25_20251119",
			tool: &AnthropicTool{
				Name: "tool_search_tool_bm25",
				Type: func() *AnthropicToolType { t := AnthropicToolTypeToolSearchBm25_20251119; return &t }(),
			},
		},
		{
			name: "text_editor_20250728",
			tool: &AnthropicTool{
				Name: "str_replace_based_edit_tool",
				Type: func() *AnthropicToolType { t := AnthropicToolTypeTextEditor20250728; return &t }(),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Convert Anthropic -> Bifrost
			bifrostTool := convertAnthropicToolToBifrost(tt.tool)
			if bifrostTool == nil {
				t.Fatal("convertAnthropicToolToBifrost returned nil")
			}

			// Convert Bifrost -> Anthropic
			roundTripped := convertBifrostToolToAnthropic("claude-sonnet-4-0", bifrostTool)
			if roundTripped == nil {
				t.Fatal("convertBifrostToolToAnthropic returned nil")
			}

			// Verify the type is preserved
			if tt.tool.Type == nil {
				t.Fatal("original tool type is nil")
			}
			if roundTripped.Type == nil {
				t.Fatalf("round-tripped tool type is nil, expected %s", *tt.tool.Type)
			}
			if *roundTripped.Type != *tt.tool.Type {
				t.Errorf("type mismatch: got %s, want %s", *roundTripped.Type, *tt.tool.Type)
			}

			// For built-in tools, input_schema should NOT be set
			if roundTripped.InputSchema != nil {
				t.Errorf("built-in tool should not have input_schema, but got: %+v", roundTripped.InputSchema)
			}


		})
	}
}
