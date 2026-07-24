package openai

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

func TestToOpenAIResponsesRequest_ReasoningOnlyMessageSkip(t *testing.T) {
	tests := []struct {
		name                     string
		model                    string
		message                  schemas.ResponsesMessage
		expectedIncluded         bool
		expectedEncryptedContent *string // if non-nil, assert converted message preserves this value
		description              string
	}{
		{
			name:  "reasoning-only message skipped for non-gpt-oss model",
			model: "gpt-4o",
			message: schemas.ResponsesMessage{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				ResponsesReasoning: &schemas.ResponsesReasoning{
					Summary:          []schemas.ResponsesReasoningSummary{}, // empty Summary
					EncryptedContent: nil,                                   // nil EncryptedContent
				},
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{
							Type: schemas.ResponsesOutputMessageContentTypeReasoning,
							Text: schemas.Ptr("reasoning text"),
						},
					}, // non-empty ContentBlocks
				},
			},
			expectedIncluded: false,
			description:      "Message with ResponsesReasoning != nil, empty Summary, non-empty ContentBlocks, non-gpt-oss model, and nil EncryptedContent should be skipped",
		},
		{
			name:  "message with Summary preserved for non-gpt-oss model",
			model: "gpt-4o",
			message: schemas.ResponsesMessage{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				ResponsesReasoning: &schemas.ResponsesReasoning{
					Summary: []schemas.ResponsesReasoningSummary{
						{
							Type: schemas.ResponsesReasoningContentBlockTypeSummaryText,
							Text: "summary text",
						},
					}, // non-empty Summary
					EncryptedContent: nil,
				},
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{
							Type: schemas.ResponsesOutputMessageContentTypeReasoning,
							Text: schemas.Ptr("reasoning text"),
						},
					},
				},
			},
			expectedIncluded: true,
			description:      "Message with non-empty Summary should be preserved even if it has ContentBlocks",
		},
		{
			name:  "message with EncryptedContent skipped for non-reasoning model",
			model: "gpt-4o",
			message: schemas.ResponsesMessage{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				ResponsesReasoning: &schemas.ResponsesReasoning{
					Summary:          []schemas.ResponsesReasoningSummary{}, // empty Summary
					EncryptedContent: schemas.Ptr("encrypted"),              // non-nil EncryptedContent
				},
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{
							Type: schemas.ResponsesOutputMessageContentTypeReasoning,
							Text: schemas.Ptr("reasoning text"),
						},
					},
				},
			},
			expectedIncluded: false,
			description:      "Non-reasoning models don't produce encrypted reasoning; cross-provider content should be skipped",
		},
		{
			name:  "message with EncryptedContent preserved for reasoning model",
			model: "o3",
			message: schemas.ResponsesMessage{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				ResponsesReasoning: &schemas.ResponsesReasoning{
					Summary:          []schemas.ResponsesReasoningSummary{}, // empty Summary
					EncryptedContent: schemas.Ptr("encrypted"),              // non-nil EncryptedContent
				},
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{
							Type: schemas.ResponsesOutputMessageContentTypeReasoning,
							Text: schemas.Ptr("reasoning text"),
						},
					},
				},
			},
			expectedIncluded:         true,
			expectedEncryptedContent: schemas.Ptr("encrypted"),
			description:              "Reasoning models (o1/o3) produce encrypted content; should be preserved for multi-turn",
		},
		{
			name:  "message with empty ContentBlocks preserved for non-gpt-oss model",
			model: "gpt-4o",
			message: schemas.ResponsesMessage{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				ResponsesReasoning: &schemas.ResponsesReasoning{
					Summary:          []schemas.ResponsesReasoningSummary{}, // empty Summary
					EncryptedContent: nil,
				},
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{}, // empty ContentBlocks
				},
			},
			expectedIncluded: true,
			description:      "Message with empty ContentBlocks should be preserved",
		},
		{
			name:  "message with nil Content preserved for non-gpt-oss model",
			model: "gpt-4o",
			message: schemas.ResponsesMessage{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				ResponsesReasoning: &schemas.ResponsesReasoning{
					Summary:          []schemas.ResponsesReasoningSummary{}, // empty Summary
					EncryptedContent: nil,
				},
				Content: nil, // nil Content
			},
			expectedIncluded: true,
			description:      "Message with nil Content should be preserved",
		},
		{
			name:  "reasoning-only message preserved for gpt-oss model",
			model: "gpt-oss",
			message: schemas.ResponsesMessage{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				ResponsesReasoning: &schemas.ResponsesReasoning{
					Summary:          []schemas.ResponsesReasoningSummary{}, // empty Summary
					EncryptedContent: nil,
				},
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{
							Type: schemas.ResponsesOutputMessageContentTypeReasoning,
							Text: schemas.Ptr("reasoning text"),
						},
					},
				},
			},
			expectedIncluded: true,
			description:      "Message with reasoning-only content should be preserved for gpt-oss model",
		},
		{
			name:  "message without ResponsesReasoning preserved",
			model: "gpt-4o",
			message: schemas.ResponsesMessage{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{
							Type: schemas.ResponsesOutputMessageContentTypeText,
							Text: schemas.Ptr("regular text"),
						},
					},
				},
			},
			expectedIncluded: true,
			description:      "Message without ResponsesReasoning should always be preserved",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bifrostReq := &schemas.BifrostResponsesRequest{
				Model: tt.model,
				Input: []schemas.ResponsesMessage{tt.message},
			}

			result := ToOpenAIResponsesRequest(nil, bifrostReq)

			if result == nil {
				t.Fatal("ToOpenAIResponsesRequest returned nil")
			}

			messageCount := len(result.Input.OpenAIResponsesRequestInputArray)
			isIncluded := messageCount > 0

			if isIncluded != tt.expectedIncluded {
				t.Errorf("%s: expected message to be included=%v (messageCount=%d), got included=%v (messageCount=%d)",
					tt.description, tt.expectedIncluded, func() int {
						if tt.expectedIncluded {
							return 1
						}
						return 0
					}(), isIncluded, messageCount)
			}

			// If message should be included, verify it's actually present
			if tt.expectedIncluded && messageCount == 0 {
				t.Error("Expected message to be included but result array is empty")
			}

			// If message should be excluded, verify it's not present
			if !tt.expectedIncluded && messageCount > 0 {
				t.Errorf("Expected message to be excluded but found %d message(s) in result", messageCount)
			}

			// If expectedEncryptedContent is set, verify the converted message preserves it
			if tt.expectedEncryptedContent != nil && messageCount > 0 {
				msg := result.Input.OpenAIResponsesRequestInputArray[0]
				if msg.ResponsesReasoning == nil || msg.ResponsesReasoning.EncryptedContent == nil {
					t.Error("Expected EncryptedContent to be preserved but ResponsesReasoning or EncryptedContent is nil")
				} else if *msg.ResponsesReasoning.EncryptedContent != *tt.expectedEncryptedContent {
					t.Errorf("Expected EncryptedContent=%q, got %q", *tt.expectedEncryptedContent, *msg.ResponsesReasoning.EncryptedContent)
				}
			}
		})
	}
}

// TestToOpenAIResponsesRequest_ReasoningStringContent guards the Codex/GPT-5.5
// replay path: a reasoning item can arrive with content as a string (notably an
// empty "" round-tripped through the response path). OpenAI types reasoning.content
// as an array of reasoning_text blocks and rejects a string with
// "expected an array ... got a string", so the outbound conversion must drop the
// empty string and promote a non-empty one to a reasoning_text block.
func TestToOpenAIResponsesRequest_ReasoningStringContent(t *testing.T) {
	t.Run("empty string content is dropped", func(t *testing.T) {
		bifrostReq := &schemas.BifrostResponsesRequest{
			Model: "gpt-5.5",
			Input: []schemas.ResponsesMessage{{
				Type:               schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
				ResponsesReasoning: &schemas.ResponsesReasoning{EncryptedContent: schemas.Ptr("enc1")},
				Content:            &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("")},
			}},
		}

		result := ToOpenAIResponsesRequest(nil, bifrostReq)
		original := bifrostReq.Input[0].Content
		if original == nil || original.ContentStr == nil || *original.ContentStr != "" {
			t.Fatalf("expected input reasoning content string to remain unchanged, got %#v", original)
		}
		if len(original.ContentBlocks) != 0 {
			t.Fatalf("expected input reasoning content blocks to remain empty, got %#v", original.ContentBlocks)
		}
		if result == nil || len(result.Input.OpenAIResponsesRequestInputArray) != 1 {
			t.Fatalf("expected one converted message, got %#v", result)
		}
		if c := result.Input.OpenAIResponsesRequestInputArray[0].Content; c != nil {
			t.Errorf("expected reasoning Content to be dropped, got %#v", c)
		}

		// End-to-end: the marshalled request must not carry content:"" on the reasoning item.
		out, err := json.Marshal(result)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if strings.Contains(string(out), `"content":""`) {
			t.Errorf("reasoning item serialized with empty-string content: %s", string(out))
		}
	})

	t.Run("non-empty string content becomes a reasoning_text block", func(t *testing.T) {
		bifrostReq := &schemas.BifrostResponsesRequest{
			Model: "gpt-5.5",
			Input: []schemas.ResponsesMessage{{
				Type:               schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
				ResponsesReasoning: &schemas.ResponsesReasoning{EncryptedContent: schemas.Ptr("enc1")},
				Content:            &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("thinking")},
			}},
		}

		result := ToOpenAIResponsesRequest(nil, bifrostReq)
		original := bifrostReq.Input[0].Content
		if original == nil || original.ContentStr == nil || *original.ContentStr != "thinking" {
			t.Fatalf("expected input reasoning content string to remain unchanged, got %#v", original)
		}
		if len(original.ContentBlocks) != 0 {
			t.Fatalf("expected input reasoning content blocks to remain empty, got %#v", original.ContentBlocks)
		}
		if result == nil || len(result.Input.OpenAIResponsesRequestInputArray) != 1 {
			t.Fatalf("expected one converted message, got %#v", result)
		}
		c := result.Input.OpenAIResponsesRequestInputArray[0].Content
		if c == nil || c.ContentStr != nil || len(c.ContentBlocks) != 1 {
			t.Fatalf("expected a single content block, got %#v", c)
		}
		block := c.ContentBlocks[0]
		if block.Type != schemas.ResponsesOutputMessageContentTypeReasoning ||
			block.Text == nil || *block.Text != "thinking" {
			t.Errorf("expected reasoning_text block with text %q, got %#v", "thinking", block)
		}
	})
}

func TestToOpenAIResponsesRequest_NormalizesReasoningEffort(t *testing.T) {
	// Register the custom "deepseek" provider so ParseModelString strips its prefix.
	schemas.RegisterKnownProvider(schemas.ModelProvider("deepseek"))
	defer schemas.UnregisterKnownProvider(schemas.ModelProvider("deepseek"))
	// GLM-5.2 (Z.ai) is also a custom OpenAI-compatible provider.
	schemas.RegisterKnownProvider(schemas.ModelProvider("zai"))
	defer schemas.UnregisterKnownProvider(schemas.ModelProvider("zai"))

	tests := []struct {
		name     string
		provider schemas.ModelProvider
		model    string
		effort   string
		expected string
	}{
		{
			name:     "preserves xhigh for gpt-5.4",
			model:    "gpt-5.4",
			effort:   "xhigh",
			expected: "xhigh",
		},
		{
			name:     "preserves xhigh for gpt-5.2",
			model:    "gpt-5.2",
			effort:   "xhigh",
			expected: "xhigh",
		},
		{
			name:     "preserves xhigh for gpt-5.2 pro",
			model:    "gpt-5.2-pro",
			effort:   "xhigh",
			expected: "xhigh",
		},
		{
			name:     "preserves xhigh for gpt-5.2 codex",
			model:    "gpt-5.2-codex",
			effort:   "xhigh",
			expected: "xhigh",
		},
		{
			name:     "preserves xhigh for gpt-5.3 codex",
			model:    "gpt-5.3-codex",
			effort:   "xhigh",
			expected: "xhigh",
		},
		{
			name:     "preserves xhigh for gpt-5.4 mini",
			model:    "gpt-5.4-mini",
			effort:   "xhigh",
			expected: "xhigh",
		},
		{
			name:     "preserves xhigh for gpt-5.5",
			model:    "gpt-5.5",
			effort:   "xhigh",
			expected: "xhigh",
		},
		{
			name:     "maps xhigh to high for gpt-5",
			model:    "gpt-5",
			effort:   "xhigh",
			expected: "high",
		},
		{
			name:     "maps xhigh to high for gpt-5.1",
			model:    "gpt-5.1",
			effort:   "xhigh",
			expected: "high",
		},
		{
			name:     "maps xhigh to high for gpt-5-pro",
			model:    "gpt-5-pro",
			effort:   "xhigh",
			expected: "high",
		},
		{
			name:     "maps minimal to low",
			model:    "gpt-5.4",
			effort:   "minimal",
			expected: "low",
		},
		{
			name:     "maps max to xhigh for xhigh-capable model",
			model:    "gpt-5.4",
			effort:   "max",
			expected: "xhigh",
		},
		{
			name:     "maps max to high for model without xhigh",
			model:    "gpt-5.1",
			effort:   "max",
			expected: "high",
		},
		{
			// DeepSeek V4 is routed via a custom OpenAI-compatible provider, so the
			// OpenAI-only reasoning-stripping doesn't apply and "max" passes through.
			name:     "preserves max for deepseek-v4-pro",
			provider: schemas.ModelProvider("deepseek"),
			model:    "deepseek-v4-pro",
			effort:   "max",
			expected: "max",
		},
		{
			name:     "preserves max for deepseek-v4-flash",
			provider: schemas.ModelProvider("deepseek"),
			model:    "deepseek-v4-flash",
			effort:   "max",
			expected: "max",
		},
		{
			name:     "preserves max for provider-prefixed deepseek-v4",
			provider: schemas.ModelProvider("deepseek"),
			model:    "deepseek/deepseek-v4-pro",
			effort:   "max",
			expected: "max",
		},
		{
			// GLM-5.2 (Z.ai) natively supports "max" reasoning effort.
			name:     "preserves max for glm-5.2",
			provider: schemas.ModelProvider("zai"),
			model:    "glm-5.2",
			effort:   "max",
			expected: "max",
		},
		{
			name:     "preserves max for provider-prefixed glm-5.2",
			provider: schemas.ModelProvider("zai"),
			model:    "zai/glm-5.2",
			effort:   "max",
			expected: "max",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := tt.provider
			if provider == "" {
				provider = schemas.OpenAI
			}
			req := ToOpenAIResponsesRequest(nil, &schemas.BifrostResponsesRequest{
				Provider: provider,
				Model:    tt.model,
				Input: []schemas.ResponsesMessage{{
					Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
					Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hello")},
				}},
				Params: &schemas.ResponsesParameters{
					Reasoning: &schemas.ResponsesParametersReasoning{
						Effort:    schemas.Ptr(tt.effort),
						MaxTokens: schemas.Ptr(1024),
					},
				},
			})

			if req == nil {
				t.Fatal("expected OpenAI responses request")
			}
			if req.Reasoning == nil || req.Reasoning.Effort == nil {
				t.Fatal("expected reasoning effort to be set")
			}
			if got := *req.Reasoning.Effort; got != tt.expected {
				t.Fatalf("expected reasoning effort %q, got %q", tt.expected, got)
			}
			if req.Reasoning.MaxTokens != nil {
				t.Fatalf("expected reasoning max_tokens to be cleared, got %d", *req.Reasoning.MaxTokens)
			}
		})
	}
}

func TestToOpenAIResponsesRequest_GPTOSS_SummaryToContentBlocks(t *testing.T) {
	tests := []struct {
		name              string
		model             string
		message           schemas.ResponsesMessage
		expectedBlocks    int
		expectedBlockText string
		description       string
	}{
		{
			name:  "gpt-oss converts Summary to ContentBlocks",
			model: "gpt-oss",
			message: schemas.ResponsesMessage{
				ID:     schemas.Ptr("msg-1"),
				Role:   schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Status: schemas.Ptr("completed"),
				ResponsesReasoning: &schemas.ResponsesReasoning{
					Summary: []schemas.ResponsesReasoningSummary{
						{
							Type: schemas.ResponsesReasoningContentBlockTypeSummaryText,
							Text: "First summary",
						},
						{
							Type: schemas.ResponsesReasoningContentBlockTypeSummaryText,
							Text: "Second summary",
						},
					},
					EncryptedContent: nil,
				},
				Content: nil, // No ContentBlocks initially
			},
			expectedBlocks:    2,
			expectedBlockText: "First summary",
			description:       "gpt-oss model should convert Summary to ContentBlocks when Content is nil",
		},
		{
			name:  "gpt-oss preserves message when Content already exists",
			model: "gpt-oss",
			message: schemas.ResponsesMessage{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				ResponsesReasoning: &schemas.ResponsesReasoning{
					Summary: []schemas.ResponsesReasoningSummary{
						{
							Type: schemas.ResponsesReasoningContentBlockTypeSummaryText,
							Text: "summary text",
						},
					},
				},
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{
							Type: schemas.ResponsesOutputMessageContentTypeText,
							Text: schemas.Ptr("existing content"),
						},
					},
				},
			},
			expectedBlocks:    1,
			expectedBlockText: "existing content",
			description:       "gpt-oss model should preserve message when Content already exists",
		},
		{
			name:  "gpt-oss variant model converts Summary to ContentBlocks",
			model: "provider/gpt-oss-variant",
			message: schemas.ResponsesMessage{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				ResponsesReasoning: &schemas.ResponsesReasoning{
					Summary: []schemas.ResponsesReasoningSummary{
						{
							Type: schemas.ResponsesReasoningContentBlockTypeSummaryText,
							Text: "variant summary",
						},
					},
				},
				Content: nil,
			},
			expectedBlocks:    1,
			expectedBlockText: "variant summary",
			description:       "gpt-oss variant model should also convert Summary to ContentBlocks",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bifrostReq := &schemas.BifrostResponsesRequest{
				Model: tt.model,
				Input: []schemas.ResponsesMessage{tt.message},
			}

			result := ToOpenAIResponsesRequest(nil, bifrostReq)

			if result == nil {
				t.Fatal("ToOpenAIResponsesRequest returned nil")
			}

			if len(result.Input.OpenAIResponsesRequestInputArray) != 1 {
				t.Fatalf("Expected 1 message, got %d", len(result.Input.OpenAIResponsesRequestInputArray))
			}

			resultMsg := result.Input.OpenAIResponsesRequestInputArray[0]

			// Check if Summary was converted to ContentBlocks for gpt-oss
			if strings.Contains(tt.model, "gpt-oss") && len(tt.message.ResponsesReasoning.Summary) > 0 && tt.message.Content == nil {
				if resultMsg.Content == nil {
					t.Fatal("Expected Content to be created from Summary")
				}

				if len(resultMsg.Content.ContentBlocks) != tt.expectedBlocks {
					t.Errorf("Expected %d ContentBlocks, got %d", tt.expectedBlocks, len(resultMsg.Content.ContentBlocks))
				}

				if len(resultMsg.Content.ContentBlocks) > 0 {
					firstBlock := resultMsg.Content.ContentBlocks[0]
					if firstBlock.Type != schemas.ResponsesOutputMessageContentTypeReasoning {
						t.Errorf("Expected ContentBlock type to be reasoning_text, got %s", firstBlock.Type)
					}

					if firstBlock.Text == nil || *firstBlock.Text != tt.expectedBlockText {
						t.Errorf("Expected first ContentBlock text to be %q, got %q", tt.expectedBlockText, func() string {
							if firstBlock.Text == nil {
								return "<nil>"
							}
							return *firstBlock.Text
						}())
					}
				}

				// Verify that original message fields are preserved
				if tt.message.ID != nil && (resultMsg.ID == nil || *resultMsg.ID != *tt.message.ID) {
					t.Errorf("Expected ID to be preserved")
				}
				if tt.message.Type != nil && (resultMsg.Type == nil || *resultMsg.Type != *tt.message.Type) {
					t.Errorf("Expected Type to be preserved")
				}
				if tt.message.Status != nil && (resultMsg.Status == nil || *resultMsg.Status != *tt.message.Status) {
					t.Errorf("Expected Status to be preserved")
				}
				if tt.message.Role != nil && (resultMsg.Role == nil || *resultMsg.Role != *tt.message.Role) {
					t.Errorf("Expected Role to be preserved")
				}
			} else {
				// For other cases, verify message is preserved as-is
				if resultMsg.Content != nil && len(resultMsg.Content.ContentBlocks) > 0 {
					if resultMsg.Content.ContentBlocks[0].Text == nil || *resultMsg.Content.ContentBlocks[0].Text != tt.expectedBlockText {
						t.Errorf("Expected ContentBlock text to be preserved as %q", tt.expectedBlockText)
					}
				}
			}
		})
	}
}

// =============================================================================
// ResponsesToolMessageActionStruct Marshal/Unmarshal Tests
// =============================================================================

func TestResponsesToolMessageActionStruct_MarshalUnmarshal_ComputerToolAction(t *testing.T) {
	tests := []struct {
		name     string
		action   schemas.ResponsesToolMessageActionStruct
		jsonData string
	}{
		{
			name: "computer tool action - click",
			action: schemas.ResponsesToolMessageActionStruct{
				ResponsesComputerToolCallAction: &schemas.ResponsesComputerToolCallAction{
					Type: "click",
					X:    schemas.Ptr(100),
					Y:    schemas.Ptr(200),
				},
			},
			jsonData: `{"type":"click","x":100,"y":200}`,
		},
		{
			name: "computer tool action - screenshot",
			action: schemas.ResponsesToolMessageActionStruct{
				ResponsesComputerToolCallAction: &schemas.ResponsesComputerToolCallAction{
					Type: "screenshot",
				},
			},
			jsonData: `{"type":"screenshot"}`,
		},
		{
			name: "computer tool action - type with text",
			action: schemas.ResponsesToolMessageActionStruct{
				ResponsesComputerToolCallAction: &schemas.ResponsesComputerToolCallAction{
					Type: "type",
					Text: schemas.Ptr("hello world"),
				},
			},
			jsonData: `{"type":"type","text":"hello world"}`,
		},
		{
			name: "computer tool action - scroll",
			action: schemas.ResponsesToolMessageActionStruct{
				ResponsesComputerToolCallAction: &schemas.ResponsesComputerToolCallAction{
					Type:    "scroll",
					ScrollX: schemas.Ptr(50),
					ScrollY: schemas.Ptr(100),
				},
			},
			jsonData: `{"type":"scroll","scroll_x":50,"scroll_y":100}`,
		},
		{
			name: "computer tool action - zoom with region",
			action: schemas.ResponsesToolMessageActionStruct{
				ResponsesComputerToolCallAction: &schemas.ResponsesComputerToolCallAction{
					Type:   "zoom",
					Region: []int{0, 0, 1024, 768},
				},
			},
			jsonData: `{"type":"zoom","region":[0,0,1024,768]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name+" - marshal", func(t *testing.T) {
			data, err := json.Marshal(tt.action)
			if err != nil {
				t.Fatalf("failed to marshal: %v", err)
			}

			// Unmarshal both to compare as maps (ignoring field order)
			var expected, actual map[string]interface{}
			if err := json.Unmarshal([]byte(tt.jsonData), &expected); err != nil {
				t.Fatalf("failed to unmarshal expected JSON: %v", err)
			}
			if err := json.Unmarshal(data, &actual); err != nil {
				t.Fatalf("failed to unmarshal actual JSON: %v", err)
			}

			if !mapsEqual(expected, actual) {
				t.Errorf("marshaled JSON mismatch\nexpected: %s\nactual:   %s", tt.jsonData, string(data))
			}
		})

		t.Run(tt.name+" - unmarshal", func(t *testing.T) {
			var action schemas.ResponsesToolMessageActionStruct
			if err := json.Unmarshal([]byte(tt.jsonData), &action); err != nil {
				t.Fatalf("failed to unmarshal: %v", err)
			}

			if action.ResponsesComputerToolCallAction == nil {
				t.Fatal("expected ResponsesComputerToolCallAction to be populated")
			}

			if action.ResponsesComputerToolCallAction.Type != tt.action.ResponsesComputerToolCallAction.Type {
				t.Errorf("type mismatch: expected %s, got %s",
					tt.action.ResponsesComputerToolCallAction.Type,
					action.ResponsesComputerToolCallAction.Type)
			}

			// Verify all other fields are nil (union type should have only one set)
			if action.ResponsesWebSearchToolCallAction != nil {
				t.Error("expected ResponsesWebSearchToolCallAction to be nil")
			}
			if action.ResponsesLocalShellToolCallAction != nil {
				t.Error("expected ResponsesLocalShellToolCallAction to be nil")
			}
			if action.ResponsesMCPApprovalRequestAction != nil {
				t.Error("expected ResponsesMCPApprovalRequestAction to be nil")
			}
		})
	}
}

func TestResponsesToolMessageActionStruct_MarshalUnmarshal_WebSearchAction(t *testing.T) {
	tests := []struct {
		name     string
		action   schemas.ResponsesToolMessageActionStruct
		jsonData string
	}{
		{
			name: "web search action - search",
			action: schemas.ResponsesToolMessageActionStruct{
				ResponsesWebSearchToolCallAction: &schemas.ResponsesWebSearchToolCallAction{
					Type:  "search",
					Query: schemas.Ptr("golang testing"),
				},
			},
			jsonData: `{"type":"search","query":"golang testing"}`,
		},
		{
			name: "web search action - open_page",
			action: schemas.ResponsesToolMessageActionStruct{
				ResponsesWebSearchToolCallAction: &schemas.ResponsesWebSearchToolCallAction{
					Type: "open_page",
					URL:  schemas.Ptr("https://example.com"),
				},
			},
			jsonData: `{"type":"open_page","url":"https://example.com"}`,
		},
		{
			name: "web search action - find",
			action: schemas.ResponsesToolMessageActionStruct{
				ResponsesWebSearchToolCallAction: &schemas.ResponsesWebSearchToolCallAction{
					Type:    "find",
					Pattern: schemas.Ptr("error.*occurred"),
				},
			},
			jsonData: `{"type":"find","pattern":"error.*occurred"}`,
		},
		{
			name: "web search action - search with queries array",
			action: schemas.ResponsesToolMessageActionStruct{
				ResponsesWebSearchToolCallAction: &schemas.ResponsesWebSearchToolCallAction{
					Type:    "search",
					Queries: []string{"query1", "query2"},
				},
			},
			jsonData: `{"type":"search","queries":["query1","query2"]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name+" - marshal", func(t *testing.T) {
			data, err := json.Marshal(tt.action)
			if err != nil {
				t.Fatalf("failed to marshal: %v", err)
			}

			var expected, actual map[string]interface{}
			if err := json.Unmarshal([]byte(tt.jsonData), &expected); err != nil {
				t.Fatalf("failed to unmarshal expected JSON: %v", err)
			}
			if err := json.Unmarshal(data, &actual); err != nil {
				t.Fatalf("failed to unmarshal actual JSON: %v", err)
			}

			if !mapsEqual(expected, actual) {
				t.Errorf("marshaled JSON mismatch\nexpected: %s\nactual:   %s", tt.jsonData, string(data))
			}
		})

		t.Run(tt.name+" - unmarshal", func(t *testing.T) {
			var action schemas.ResponsesToolMessageActionStruct
			if err := json.Unmarshal([]byte(tt.jsonData), &action); err != nil {
				t.Fatalf("failed to unmarshal: %v", err)
			}

			if action.ResponsesWebSearchToolCallAction == nil {
				t.Fatal("expected ResponsesWebSearchToolCallAction to be populated")
			}

			if action.ResponsesWebSearchToolCallAction.Type != tt.action.ResponsesWebSearchToolCallAction.Type {
				t.Errorf("type mismatch: expected %s, got %s",
					tt.action.ResponsesWebSearchToolCallAction.Type,
					action.ResponsesWebSearchToolCallAction.Type)
			}

			// Verify all other fields are nil
			if action.ResponsesComputerToolCallAction != nil {
				t.Error("expected ResponsesComputerToolCallAction to be nil")
			}
			if action.ResponsesLocalShellToolCallAction != nil {
				t.Error("expected ResponsesLocalShellToolCallAction to be nil")
			}
			if action.ResponsesMCPApprovalRequestAction != nil {
				t.Error("expected ResponsesMCPApprovalRequestAction to be nil")
			}
		})
	}
}

func TestResponsesToolMessageActionStruct_MarshalUnmarshal_LocalShellAction(t *testing.T) {
	tests := []struct {
		name     string
		action   schemas.ResponsesToolMessageActionStruct
		jsonData string
	}{
		{
			name: "local shell action - simple exec",
			action: schemas.ResponsesToolMessageActionStruct{
				ResponsesLocalShellToolCallAction: &schemas.ResponsesLocalShellToolCallAction{
					Type:    "exec",
					Command: []string{"ls", "-la"},
					Env:     []string{"PATH=/usr/bin"},
				},
			},
			jsonData: `{"type":"exec","command":["ls","-la"],"env":["PATH=/usr/bin"]}`,
		},
		{
			name: "local shell action - with timeout and working directory",
			action: schemas.ResponsesToolMessageActionStruct{
				ResponsesLocalShellToolCallAction: &schemas.ResponsesLocalShellToolCallAction{
					Type:             "exec",
					Command:          []string{"npm", "test"},
					Env:              []string{},
					TimeoutMS:        schemas.Ptr(5000),
					WorkingDirectory: schemas.Ptr("/home/user/project"),
				},
			},
			jsonData: `{"type":"exec","command":["npm","test"],"env":[],"timeout_ms":5000,"working_directory":"/home/user/project"}`,
		},
		{
			name: "local shell action - with user",
			action: schemas.ResponsesToolMessageActionStruct{
				ResponsesLocalShellToolCallAction: &schemas.ResponsesLocalShellToolCallAction{
					Type:    "exec",
					Command: []string{"whoami"},
					Env:     []string{},
					User:    schemas.Ptr("testuser"),
				},
			},
			jsonData: `{"type":"exec","command":["whoami"],"env":[],"user":"testuser"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name+" - marshal", func(t *testing.T) {
			data, err := json.Marshal(tt.action)
			if err != nil {
				t.Fatalf("failed to marshal: %v", err)
			}

			var expected, actual map[string]interface{}
			if err := json.Unmarshal([]byte(tt.jsonData), &expected); err != nil {
				t.Fatalf("failed to unmarshal expected JSON: %v", err)
			}
			if err := json.Unmarshal(data, &actual); err != nil {
				t.Fatalf("failed to unmarshal actual JSON: %v", err)
			}

			if !mapsEqual(expected, actual) {
				t.Errorf("marshaled JSON mismatch\nexpected: %s\nactual:   %s", tt.jsonData, string(data))
			}
		})

		t.Run(tt.name+" - unmarshal", func(t *testing.T) {
			var action schemas.ResponsesToolMessageActionStruct
			if err := json.Unmarshal([]byte(tt.jsonData), &action); err != nil {
				t.Fatalf("failed to unmarshal: %v", err)
			}

			if action.ResponsesLocalShellToolCallAction == nil {
				t.Fatal("expected ResponsesLocalShellToolCallAction to be populated")
			}

			if action.ResponsesLocalShellToolCallAction.Type != "exec" {
				t.Errorf("type mismatch: expected exec, got %s", action.ResponsesLocalShellToolCallAction.Type)
			}

			// Verify all other fields are nil
			if action.ResponsesComputerToolCallAction != nil {
				t.Error("expected ResponsesComputerToolCallAction to be nil")
			}
			if action.ResponsesWebSearchToolCallAction != nil {
				t.Error("expected ResponsesWebSearchToolCallAction to be nil")
			}
			if action.ResponsesMCPApprovalRequestAction != nil {
				t.Error("expected ResponsesMCPApprovalRequestAction to be nil")
			}
		})
	}
}

func TestResponsesToolMessageActionStruct_MarshalUnmarshal_MCPApprovalAction(t *testing.T) {
	tests := []struct {
		name     string
		action   schemas.ResponsesToolMessageActionStruct
		jsonData string
	}{
		{
			name: "mcp approval request action",
			action: schemas.ResponsesToolMessageActionStruct{
				ResponsesMCPApprovalRequestAction: &schemas.ResponsesMCPApprovalRequestAction{
					ID:          "approval-123",
					Type:        "mcp_approval_request",
					Name:        "test_tool",
					ServerLabel: "test-server",
					Arguments:   `{"key":"value"}`,
				},
			},
			jsonData: `{"id":"approval-123","type":"mcp_approval_request","name":"test_tool","server_label":"test-server","arguments":"{\"key\":\"value\"}"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name+" - marshal", func(t *testing.T) {
			data, err := json.Marshal(tt.action)
			if err != nil {
				t.Fatalf("failed to marshal: %v", err)
			}

			var expected, actual map[string]interface{}
			if err := json.Unmarshal([]byte(tt.jsonData), &expected); err != nil {
				t.Fatalf("failed to unmarshal expected JSON: %v", err)
			}
			if err := json.Unmarshal(data, &actual); err != nil {
				t.Fatalf("failed to unmarshal actual JSON: %v", err)
			}

			if !mapsEqual(expected, actual) {
				t.Errorf("marshaled JSON mismatch\nexpected: %s\nactual:   %s", tt.jsonData, string(data))
			}
		})

		t.Run(tt.name+" - unmarshal", func(t *testing.T) {
			var action schemas.ResponsesToolMessageActionStruct
			if err := json.Unmarshal([]byte(tt.jsonData), &action); err != nil {
				t.Fatalf("failed to unmarshal: %v", err)
			}

			if action.ResponsesMCPApprovalRequestAction == nil {
				t.Fatal("expected ResponsesMCPApprovalRequestAction to be populated")
			}

			if action.ResponsesMCPApprovalRequestAction.Type != "mcp_approval_request" {
				t.Errorf("type mismatch: expected mcp_approval_request, got %s", action.ResponsesMCPApprovalRequestAction.Type)
			}

			// Verify all other fields are nil
			if action.ResponsesComputerToolCallAction != nil {
				t.Error("expected ResponsesComputerToolCallAction to be nil")
			}
			if action.ResponsesWebSearchToolCallAction != nil {
				t.Error("expected ResponsesWebSearchToolCallAction to be nil")
			}
			if action.ResponsesLocalShellToolCallAction != nil {
				t.Error("expected ResponsesLocalShellToolCallAction to be nil")
			}
		})
	}
}

func TestResponsesToolMessageActionStruct_EdgeCases(t *testing.T) {
	t.Run("empty action struct - marshal should error", func(t *testing.T) {
		action := schemas.ResponsesToolMessageActionStruct{}
		_, err := json.Marshal(action)
		if err == nil {
			t.Error("expected error when marshaling empty action struct")
		}
	})

	t.Run("unknown action type - unmarshal to computer tool (default)", func(t *testing.T) {
		jsonData := `{"type":"unknown_action"}`
		var action schemas.ResponsesToolMessageActionStruct
		if err := json.Unmarshal([]byte(jsonData), &action); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		// Default behavior is to unmarshal to computer tool
		if action.ResponsesComputerToolCallAction == nil {
			t.Error("expected ResponsesComputerToolCallAction to be populated for unknown type")
		}
	})

	t.Run("round trip - computer action", func(t *testing.T) {
		original := schemas.ResponsesToolMessageActionStruct{
			ResponsesComputerToolCallAction: &schemas.ResponsesComputerToolCallAction{
				Type: "click",
				X:    schemas.Ptr(150),
				Y:    schemas.Ptr(250),
			},
		}

		data, err := json.Marshal(original)
		if err != nil {
			t.Fatalf("marshal failed: %v", err)
		}

		var unmarshaled schemas.ResponsesToolMessageActionStruct
		if err := json.Unmarshal(data, &unmarshaled); err != nil {
			t.Fatalf("unmarshal failed: %v", err)
		}

		if unmarshaled.ResponsesComputerToolCallAction == nil {
			t.Fatal("expected ResponsesComputerToolCallAction to be populated")
		}
		if unmarshaled.ResponsesComputerToolCallAction.Type != "click" {
			t.Errorf("type mismatch: expected click, got %s", unmarshaled.ResponsesComputerToolCallAction.Type)
		}
		if unmarshaled.ResponsesComputerToolCallAction.X == nil || *unmarshaled.ResponsesComputerToolCallAction.X != 150 {
			t.Errorf("X coordinate mismatch")
		}
	})
}

// =============================================================================
// ResponsesTool Marshal/Unmarshal Tests
// =============================================================================

func TestResponsesTool_MarshalUnmarshal_FunctionTool(t *testing.T) {
	tests := []struct {
		name     string
		tool     schemas.ResponsesTool
		jsonData string
	}{
		{
			name: "function tool with name and description",
			tool: schemas.ResponsesTool{
				Type:        schemas.ResponsesToolTypeFunction,
				Name:        schemas.Ptr("get_weather"),
				Description: schemas.Ptr("Get the current weather"),
				ResponsesToolFunction: &schemas.ResponsesToolFunction{
					Strict: schemas.Ptr(true),
				},
			},
			jsonData: `{"type":"function","name":"get_weather","description":"Get the current weather","strict":true}`,
		},
		{
			name: "function tool with cache control",
			tool: schemas.ResponsesTool{
				Type:        schemas.ResponsesToolTypeFunction,
				Name:        schemas.Ptr("search_db"),
				Description: schemas.Ptr("Search database"),
				CacheControl: &schemas.CacheControl{
					Type: "ephemeral",
				},
				ResponsesToolFunction: &schemas.ResponsesToolFunction{
					Strict: schemas.Ptr(false),
				},
			},
			jsonData: `{"type":"function","name":"search_db","description":"Search database","cache_control":{"type":"ephemeral"},"strict":false}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name+" - marshal", func(t *testing.T) {
			data, err := json.Marshal(tt.tool)
			if err != nil {
				t.Fatalf("failed to marshal: %v", err)
			}

			var expected, actual map[string]interface{}
			if err := json.Unmarshal([]byte(tt.jsonData), &expected); err != nil {
				t.Fatalf("failed to unmarshal expected JSON: %v", err)
			}
			if err := json.Unmarshal(data, &actual); err != nil {
				t.Fatalf("failed to unmarshal actual JSON: %v", err)
			}

			if !mapsEqual(expected, actual) {
				t.Errorf("marshaled JSON mismatch\nexpected: %s\nactual:   %s", tt.jsonData, string(data))
			}
		})

		t.Run(tt.name+" - unmarshal", func(t *testing.T) {
			var tool schemas.ResponsesTool
			if err := json.Unmarshal([]byte(tt.jsonData), &tool); err != nil {
				t.Fatalf("failed to unmarshal: %v", err)
			}

			if tool.Type != schemas.ResponsesToolTypeFunction {
				t.Errorf("type mismatch: expected %s, got %s", schemas.ResponsesToolTypeFunction, tool.Type)
			}

			if tool.ResponsesToolFunction == nil {
				t.Fatal("expected ResponsesToolFunction to be populated")
			}

			if tool.Name == nil || *tool.Name != *tt.tool.Name {
				t.Error("name mismatch")
			}
			if tool.Description == nil || *tool.Description != *tt.tool.Description {
				t.Error("description mismatch")
			}
		})
	}
}

func TestResponsesTool_MarshalUnmarshal_FileSearchTool(t *testing.T) {
	jsonData := `{"type":"file_search","vector_store_ids":null}`

	t.Run("file search tool - marshal", func(t *testing.T) {
		tool := schemas.ResponsesTool{
			Type:                    schemas.ResponsesToolTypeFileSearch,
			ResponsesToolFileSearch: &schemas.ResponsesToolFileSearch{},
		}

		data, err := json.Marshal(tool)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var expected, actual map[string]interface{}
		if err := json.Unmarshal([]byte(jsonData), &expected); err != nil {
			t.Fatalf("failed to unmarshal expected JSON: %v", err)
		}
		if err := json.Unmarshal(data, &actual); err != nil {
			t.Fatalf("failed to unmarshal actual JSON: %v", err)
		}

		if !mapsEqual(expected, actual) {
			t.Errorf("marshaled JSON mismatch\nexpected: %s\nactual:   %s", jsonData, string(data))
		}
	})

	t.Run("file search tool - unmarshal", func(t *testing.T) {
		var tool schemas.ResponsesTool
		if err := json.Unmarshal([]byte(jsonData), &tool); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		if tool.Type != schemas.ResponsesToolTypeFileSearch {
			t.Errorf("type mismatch: expected %s, got %s", schemas.ResponsesToolTypeFileSearch, tool.Type)
		}

		if tool.ResponsesToolFileSearch == nil {
			t.Fatal("expected ResponsesToolFileSearch to be populated")
		}
	})
}

func TestResponsesTool_MarshalUnmarshal_ComputerUseTool(t *testing.T) {
	jsonData := `{"type":"computer_use_preview","display_height":1080,"display_width":1920,"environment":"browser"}`

	t.Run("computer use preview tool - marshal", func(t *testing.T) {
		tool := schemas.ResponsesTool{
			Type: schemas.ResponsesToolTypeComputerUsePreview,
			ResponsesToolComputerUsePreview: &schemas.ResponsesToolComputerUsePreview{
				DisplayWidth:  1920,
				DisplayHeight: 1080,
				Environment:   "browser",
			},
		}
		data, err := json.Marshal(tool)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var expected, actual map[string]interface{}
		if err := json.Unmarshal([]byte(jsonData), &expected); err != nil {
			t.Fatalf("failed to unmarshal expected JSON: %v", err)
		}
		if err := json.Unmarshal(data, &actual); err != nil {
			t.Fatalf("failed to unmarshal actual JSON: %v", err)
		}

		if !mapsEqual(expected, actual) {
			t.Errorf("marshaled JSON mismatch\nexpected: %s\nactual:   %s", jsonData, string(data))
		}
	})

	t.Run("computer use preview tool - unmarshal", func(t *testing.T) {
		var tool schemas.ResponsesTool
		if err := json.Unmarshal([]byte(jsonData), &tool); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		if tool.Type != schemas.ResponsesToolTypeComputerUsePreview {
			t.Errorf("type mismatch: expected %s, got %s", schemas.ResponsesToolTypeComputerUsePreview, tool.Type)
		}

		if tool.ResponsesToolComputerUsePreview == nil {
			t.Fatal("expected ResponsesToolComputerUsePreview to be populated")
		}
	})
}

func TestResponsesTool_MarshalUnmarshal_WebSearchTool(t *testing.T) {
	jsonData := `{"type":"web_search","search_context_size":"medium"}`

	t.Run("web search tool - marshal", func(t *testing.T) {
		tool := schemas.ResponsesTool{
			Type: schemas.ResponsesToolTypeWebSearch,
			ResponsesToolWebSearch: &schemas.ResponsesToolWebSearch{
				SearchContextSize: schemas.Ptr("medium"),
			},
		}

		data, err := json.Marshal(tool)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var expected, actual map[string]interface{}
		if err := json.Unmarshal([]byte(jsonData), &expected); err != nil {
			t.Fatalf("failed to unmarshal expected JSON: %v", err)
		}
		if err := json.Unmarshal(data, &actual); err != nil {
			t.Fatalf("failed to unmarshal actual JSON: %v", err)
		}

		if !mapsEqual(expected, actual) {
			t.Errorf("marshaled JSON mismatch\nexpected: %s\nactual:   %s", jsonData, string(data))
		}
	})

	t.Run("web search tool - unmarshal", func(t *testing.T) {
		var tool schemas.ResponsesTool
		if err := json.Unmarshal([]byte(jsonData), &tool); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		if tool.Type != schemas.ResponsesToolTypeWebSearch {
			t.Errorf("type mismatch: expected %s, got %s", schemas.ResponsesToolTypeWebSearch, tool.Type)
		}

		if tool.ResponsesToolWebSearch == nil {
			t.Fatal("expected ResponsesToolWebSearch to be populated")
		}

		if tool.ResponsesToolWebSearch.SearchContextSize == nil || *tool.ResponsesToolWebSearch.SearchContextSize != "medium" {
			t.Error("search_context_size mismatch")
		}
	})
}

func TestResponsesTool_MarshalUnmarshal_MCPTool(t *testing.T) {
	jsonData := `{"type":"mcp","name":"test_mcp_tool","server_label":"mcp-server-1"}`

	t.Run("mcp tool - marshal", func(t *testing.T) {
		tool := schemas.ResponsesTool{
			Type: schemas.ResponsesToolTypeMCP,
			Name: schemas.Ptr("test_mcp_tool"),
			ResponsesToolMCP: &schemas.ResponsesToolMCP{
				ServerLabel: "mcp-server-1",
			},
		}

		data, err := json.Marshal(tool)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var expected, actual map[string]interface{}
		if err := json.Unmarshal([]byte(jsonData), &expected); err != nil {
			t.Fatalf("failed to unmarshal expected JSON: %v", err)
		}
		if err := json.Unmarshal(data, &actual); err != nil {
			t.Fatalf("failed to unmarshal actual JSON: %v", err)
		}

		if !mapsEqual(expected, actual) {
			t.Errorf("marshaled JSON mismatch\nexpected: %s\nactual:   %s", jsonData, string(data))
		}
	})

	t.Run("mcp tool - unmarshal", func(t *testing.T) {
		var tool schemas.ResponsesTool
		if err := json.Unmarshal([]byte(jsonData), &tool); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		if tool.Type != schemas.ResponsesToolTypeMCP {
			t.Errorf("type mismatch: expected %s, got %s", schemas.ResponsesToolTypeMCP, tool.Type)
		}

		if tool.ResponsesToolMCP == nil {
			t.Fatal("expected ResponsesToolMCP to be populated")
		}

		if tool.ResponsesToolMCP.ServerLabel != "mcp-server-1" {
			t.Error("server_label mismatch")
		}
	})
}

func TestResponsesTool_MarshalUnmarshal_CodeInterpreterTool(t *testing.T) {
	jsonData := `{"type":"code_interpreter","container":null}`

	t.Run("code interpreter tool - marshal", func(t *testing.T) {
		tool := schemas.ResponsesTool{
			Type:                         schemas.ResponsesToolTypeCodeInterpreter,
			ResponsesToolCodeInterpreter: &schemas.ResponsesToolCodeInterpreter{},
		}

		data, err := json.Marshal(tool)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var expected, actual map[string]interface{}
		if err := json.Unmarshal([]byte(jsonData), &expected); err != nil {
			t.Fatalf("failed to unmarshal expected JSON: %v", err)
		}
		if err := json.Unmarshal(data, &actual); err != nil {
			t.Fatalf("failed to unmarshal actual JSON: %v", err)
		}

		if !mapsEqual(expected, actual) {
			t.Errorf("marshaled JSON mismatch\nexpected: %s\nactual:   %s", jsonData, string(data))
		}
	})

	t.Run("code interpreter tool - unmarshal", func(t *testing.T) {
		var tool schemas.ResponsesTool
		if err := json.Unmarshal([]byte(jsonData), &tool); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		if tool.Type != schemas.ResponsesToolTypeCodeInterpreter {
			t.Errorf("type mismatch: expected %s, got %s", schemas.ResponsesToolTypeCodeInterpreter, tool.Type)
		}

		if tool.ResponsesToolCodeInterpreter == nil {
			t.Fatal("expected ResponsesToolCodeInterpreter to be populated")
		}
	})
}

func TestResponsesTool_MarshalUnmarshal_ImageGenerationTool(t *testing.T) {
	jsonData := `{"type":"image_generation"}`

	t.Run("image generation tool - marshal", func(t *testing.T) {
		tool := schemas.ResponsesTool{
			Type:                         schemas.ResponsesToolTypeImageGeneration,
			ResponsesToolImageGeneration: &schemas.ResponsesToolImageGeneration{},
		}

		data, err := json.Marshal(tool)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var expected, actual map[string]interface{}
		if err := json.Unmarshal([]byte(jsonData), &expected); err != nil {
			t.Fatalf("failed to unmarshal expected JSON: %v", err)
		}
		if err := json.Unmarshal(data, &actual); err != nil {
			t.Fatalf("failed to unmarshal actual JSON: %v", err)
		}

		if !mapsEqual(expected, actual) {
			t.Errorf("marshaled JSON mismatch\nexpected: %s\nactual:   %s", jsonData, string(data))
		}
	})

	t.Run("image generation tool - unmarshal", func(t *testing.T) {
		var tool schemas.ResponsesTool
		if err := json.Unmarshal([]byte(jsonData), &tool); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		if tool.Type != schemas.ResponsesToolTypeImageGeneration {
			t.Errorf("type mismatch: expected %s, got %s", schemas.ResponsesToolTypeImageGeneration, tool.Type)
		}

		if tool.ResponsesToolImageGeneration == nil {
			t.Fatal("expected ResponsesToolImageGeneration to be populated")
		}
	})
}

func TestResponsesTool_MarshalUnmarshal_LocalShellTool(t *testing.T) {
	jsonData := `{"type":"local_shell"}`

	t.Run("local shell tool - marshal", func(t *testing.T) {
		tool := schemas.ResponsesTool{
			Type:                    schemas.ResponsesToolTypeLocalShell,
			ResponsesToolLocalShell: &schemas.ResponsesToolLocalShell{},
		}

		data, err := json.Marshal(tool)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var expected, actual map[string]interface{}
		if err := json.Unmarshal([]byte(jsonData), &expected); err != nil {
			t.Fatalf("failed to unmarshal expected JSON: %v", err)
		}
		if err := json.Unmarshal(data, &actual); err != nil {
			t.Fatalf("failed to unmarshal actual JSON: %v", err)
		}

		if !mapsEqual(expected, actual) {
			t.Errorf("marshaled JSON mismatch\nexpected: %s\nactual:   %s", jsonData, string(data))
		}
	})

	t.Run("local shell tool - unmarshal", func(t *testing.T) {
		var tool schemas.ResponsesTool
		if err := json.Unmarshal([]byte(jsonData), &tool); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		if tool.Type != schemas.ResponsesToolTypeLocalShell {
			t.Errorf("type mismatch: expected %s, got %s", schemas.ResponsesToolTypeLocalShell, tool.Type)
		}

		if tool.ResponsesToolLocalShell == nil {
			t.Fatal("expected ResponsesToolLocalShell to be populated")
		}
	})
}

func TestResponsesTool_MarshalUnmarshal_CustomTool(t *testing.T) {
	jsonData := `{"type":"custom","name":"custom_tool","description":"A custom tool"}`

	t.Run("custom tool - marshal", func(t *testing.T) {
		tool := schemas.ResponsesTool{
			Type:                schemas.ResponsesToolTypeCustom,
			Name:                schemas.Ptr("custom_tool"),
			Description:         schemas.Ptr("A custom tool"),
			ResponsesToolCustom: &schemas.ResponsesToolCustom{},
		}

		data, err := json.Marshal(tool)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var expected, actual map[string]interface{}
		if err := json.Unmarshal([]byte(jsonData), &expected); err != nil {
			t.Fatalf("failed to unmarshal expected JSON: %v", err)
		}
		if err := json.Unmarshal(data, &actual); err != nil {
			t.Fatalf("failed to unmarshal actual JSON: %v", err)
		}

		if !mapsEqual(expected, actual) {
			t.Errorf("marshaled JSON mismatch\nexpected: %s\nactual:   %s", jsonData, string(data))
		}
	})

	t.Run("custom tool - unmarshal", func(t *testing.T) {
		var tool schemas.ResponsesTool
		if err := json.Unmarshal([]byte(jsonData), &tool); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		if tool.Type != schemas.ResponsesToolTypeCustom {
			t.Errorf("type mismatch: expected %s, got %s", schemas.ResponsesToolTypeCustom, tool.Type)
		}

		if tool.ResponsesToolCustom == nil {
			t.Fatal("expected ResponsesToolCustom to be populated")
		}

		if tool.Name == nil || *tool.Name != "custom_tool" {
			t.Error("name mismatch")
		}
		if tool.Description == nil || *tool.Description != "A custom tool" {
			t.Error("description mismatch")
		}
	})
}

func TestResponsesTool_MarshalUnmarshal_WebSearchPreviewTool(t *testing.T) {
	jsonData := `{"type":"web_search_preview","search_context_size":"high"}`

	t.Run("web search preview tool - marshal", func(t *testing.T) {
		tool := schemas.ResponsesTool{
			Type: schemas.ResponsesToolTypeWebSearchPreview,
			ResponsesToolWebSearchPreview: &schemas.ResponsesToolWebSearchPreview{
				SearchContextSize: schemas.Ptr("high"),
			},
		}

		data, err := json.Marshal(tool)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var expected, actual map[string]interface{}
		if err := json.Unmarshal([]byte(jsonData), &expected); err != nil {
			t.Fatalf("failed to unmarshal expected JSON: %v", err)
		}
		if err := json.Unmarshal(data, &actual); err != nil {
			t.Fatalf("failed to unmarshal actual JSON: %v", err)
		}

		if !mapsEqual(expected, actual) {
			t.Errorf("marshaled JSON mismatch\nexpected: %s\nactual:   %s", jsonData, string(data))
		}
	})

	t.Run("web search preview tool - unmarshal", func(t *testing.T) {
		var tool schemas.ResponsesTool
		if err := json.Unmarshal([]byte(jsonData), &tool); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		if tool.Type != schemas.ResponsesToolTypeWebSearchPreview {
			t.Errorf("type mismatch: expected %s, got %s", schemas.ResponsesToolTypeWebSearchPreview, tool.Type)
		}

		if tool.ResponsesToolWebSearchPreview == nil {
			t.Fatal("expected ResponsesToolWebSearchPreview to be populated")
		}
	})
}

func TestResponsesTool_EdgeCases(t *testing.T) {
	t.Run("missing type field - unmarshal should error", func(t *testing.T) {
		jsonData := `{"name":"test"}`
		var tool schemas.ResponsesTool
		err := json.Unmarshal([]byte(jsonData), &tool)
		if err == nil {
			t.Error("expected error when unmarshaling tool without type field")
		}
	})

	t.Run("round trip - function tool with all fields", func(t *testing.T) {
		original := schemas.ResponsesTool{
			Type:        schemas.ResponsesToolTypeFunction,
			Name:        schemas.Ptr("get_weather"),
			Description: schemas.Ptr("Get weather info"),
			CacheControl: &schemas.CacheControl{
				Type: "ephemeral",
			},
			ResponsesToolFunction: &schemas.ResponsesToolFunction{
				Strict: schemas.Ptr(true),
			},
		}

		data, err := json.Marshal(original)
		if err != nil {
			t.Fatalf("marshal failed: %v", err)
		}

		var unmarshaled schemas.ResponsesTool
		if err := json.Unmarshal(data, &unmarshaled); err != nil {
			t.Fatalf("unmarshal failed: %v", err)
		}

		if unmarshaled.Type != schemas.ResponsesToolTypeFunction {
			t.Error("type mismatch")
		}
		if unmarshaled.Name == nil || *unmarshaled.Name != "get_weather" {
			t.Error("name mismatch")
		}
		if unmarshaled.Description == nil || *unmarshaled.Description != "Get weather info" {
			t.Error("description mismatch")
		}
		if unmarshaled.CacheControl == nil || unmarshaled.CacheControl.Type != "ephemeral" {
			t.Error("cache_control mismatch")
		}
		if unmarshaled.ResponsesToolFunction == nil || unmarshaled.ResponsesToolFunction.Strict == nil || !*unmarshaled.ResponsesToolFunction.Strict {
			t.Error("strict field mismatch")
		}
	})

	t.Run("round trip - web search tool with user location", func(t *testing.T) {
		original := schemas.ResponsesTool{
			Type: schemas.ResponsesToolTypeWebSearch,
			ResponsesToolWebSearch: &schemas.ResponsesToolWebSearch{
				SearchContextSize: schemas.Ptr("medium"),
				UserLocation: &schemas.ResponsesToolWebSearchUserLocation{
					City:     schemas.Ptr("San Francisco"),
					Country:  schemas.Ptr("US"),
					Timezone: schemas.Ptr("America/Los_Angeles"),
				},
			},
		}

		data, err := json.Marshal(original)
		if err != nil {
			t.Fatalf("marshal failed: %v", err)
		}

		var unmarshaled schemas.ResponsesTool
		if err := json.Unmarshal(data, &unmarshaled); err != nil {
			t.Fatalf("unmarshal failed: %v", err)
		}

		if unmarshaled.ResponsesToolWebSearch == nil {
			t.Fatal("expected ResponsesToolWebSearch to be populated")
		}
		if unmarshaled.ResponsesToolWebSearch.UserLocation == nil {
			t.Fatal("expected UserLocation to be populated")
		}
		if unmarshaled.ResponsesToolWebSearch.UserLocation.City == nil || *unmarshaled.ResponsesToolWebSearch.UserLocation.City != "San Francisco" {
			t.Error("city mismatch")
		}
	})

	t.Run("nil embedded struct - should marshal type only", func(t *testing.T) {
		tool := schemas.ResponsesTool{
			Type: schemas.ResponsesToolTypeFunction,
			Name: schemas.Ptr("test"),
			// ResponsesToolFunction is nil
		}

		data, err := json.Marshal(tool)
		if err != nil {
			t.Fatalf("marshal failed: %v", err)
		}

		var result map[string]interface{}
		if err := json.Unmarshal(data, &result); err != nil {
			t.Fatalf("failed to unmarshal result: %v", err)
		}

		if result["type"] != "function" {
			t.Error("type mismatch")
		}
		if result["name"] != "test" {
			t.Error("name mismatch")
		}
	})
}

func TestToOpenAIResponsesRequest_ToolNormalization(t *testing.T) {
	// Create function tool with unsorted properties
	unsortedParams := &schemas.ToolFunctionParameters{
		Type: "object",
		Properties: schemas.NewOrderedMapFromPairs(
			schemas.KV("zebra", map[string]interface{}{"type": "string"}),
			schemas.KV("alpha", map[string]interface{}{"type": "number"}),
		),
		Required: []string{"zebra"},
	}

	bifrostReq := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input: []schemas.ResponsesMessage{
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("hello"),
				},
			},
		},
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{
				{
					Type: schemas.ResponsesToolTypeFunction,
					Name: schemas.Ptr("test_func"),
					ResponsesToolFunction: &schemas.ResponsesToolFunction{
						Parameters: unsortedParams,
					},
				},
				{
					Type:                   schemas.ResponsesToolTypeWebSearch,
					ResponsesToolWebSearch: &schemas.ResponsesToolWebSearch{},
				},
			},
		},
	}

	result := ToOpenAIResponsesRequest(nil, bifrostReq)
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Find the function tool in the result (filterUnsupportedTools may reorder)
	var funcTool *schemas.ResponsesTool
	var nonFuncToolCount int
	for i := range result.Tools {
		if result.Tools[i].Type == schemas.ResponsesToolTypeFunction {
			funcTool = &result.Tools[i]
		} else {
			nonFuncToolCount++
		}
	}

	if funcTool == nil {
		t.Fatal("expected function tool in result")
	}

	// Verify parameters are normalized: Properties keys should preserve original order
	// (user-defined property names are kept in client order for LLM generation quality)
	normalizedParams := funcTool.ResponsesToolFunction.Parameters
	if normalizedParams == nil {
		t.Fatal("expected normalized parameters to be non-nil")
	}
	keys := normalizedParams.Properties.Keys()
	if len(keys) != 2 || keys[0] != "zebra" || keys[1] != "alpha" {
		t.Errorf("expected Properties keys preserved as [zebra, alpha], got %v", keys)
	}

	// Verify non-function tools are present and unaffected
	if nonFuncToolCount != 1 {
		t.Errorf("expected 1 non-function tool, got %d", nonFuncToolCount)
	}

	// Verify original bifrostReq.Params.Tools was NOT mutated
	origParams := bifrostReq.Params.Tools[0].ResponsesToolFunction.Parameters
	origKeys := origParams.Properties.Keys()
	if len(origKeys) != 2 || origKeys[0] != "zebra" || origKeys[1] != "alpha" {
		t.Errorf("original parameters were mutated: expected [zebra, alpha], got %v", origKeys)
	}

	// Verify the ResponsesToolFunction pointer is a different object
	if funcTool.ResponsesToolFunction == bifrostReq.Params.Tools[0].ResponsesToolFunction {
		t.Error("expected ResponsesToolFunction pointer to be a copy, not the original")
	}
}

func TestToOpenAIResponsesRequest_PreservesExplicitEmptyToolParameters(t *testing.T) {
	var tool schemas.ResponsesTool
	err := json.Unmarshal([]byte(`{"type":"function","name":"empty_schema","parameters":{},"strict":false}`), &tool)
	if err != nil {
		t.Fatalf("failed to unmarshal tool: %v", err)
	}

	bifrostReq := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input: []schemas.ResponsesMessage{
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("hello"),
				},
			},
		},
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{tool},
		},
	}

	result := ToOpenAIResponsesRequest(nil, bifrostReq)
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	params := result.Tools[0].ResponsesToolFunction.Parameters
	if params == nil {
		t.Fatal("expected tool parameters to be preserved")
	}

	marshaled, err := schemas.Marshal(params)
	if err != nil {
		t.Fatalf("failed to marshal parameters: %v", err)
	}
	if string(marshaled) != `{}` {
		t.Fatalf("expected parameters to remain {}, got %s", marshaled)
	}
}

func TestResponsesTool_MarshalUnmarshal_ToolSearchTool(t *testing.T) {
	jsonData := `{"type":"tool_search","execution":"client","description":"Search tools","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}}`

	t.Run("tool search tool - marshal", func(t *testing.T) {
		tool := schemas.ResponsesTool{
			Type:        schemas.ResponsesToolTypeToolSearch,
			Description: schemas.Ptr("Search tools"),
			ResponsesToolToolSearch: &schemas.ResponsesToolToolSearch{
				Execution: schemas.Ptr("client"),
				Parameters: &schemas.ToolFunctionParameters{
					Type: "object",
					Properties: schemas.NewOrderedMapFromPairs(
						schemas.KV("query", schemas.NewOrderedMapFromPairs(
							schemas.KV("type", "string"),
						)),
					),
					Required: []string{"query"},
					AdditionalProperties: &schemas.AdditionalPropertiesStruct{
						AdditionalPropertiesBool: schemas.Ptr(false),
					},
				},
			},
		}

		data, err := json.Marshal(tool)
		if err != nil {
			t.Fatalf("failed to marshal: %v", err)
		}

		var expected, actual map[string]interface{}
		if err := json.Unmarshal([]byte(jsonData), &expected); err != nil {
			t.Fatalf("failed to unmarshal expected JSON: %v", err)
		}
		if err := json.Unmarshal(data, &actual); err != nil {
			t.Fatalf("failed to unmarshal actual JSON: %v", err)
		}

		if !mapsEqual(expected, actual) {
			t.Errorf("marshaled JSON mismatch\nexpected: %s\nactual:   %s", jsonData, string(data))
		}
	})

	t.Run("tool search tool - unmarshal", func(t *testing.T) {
		var tool schemas.ResponsesTool
		if err := json.Unmarshal([]byte(jsonData), &tool); err != nil {
			t.Fatalf("failed to unmarshal: %v", err)
		}

		if tool.Type != schemas.ResponsesToolTypeToolSearch {
			t.Errorf("type mismatch: expected %s, got %s", schemas.ResponsesToolTypeToolSearch, tool.Type)
		}
		if tool.ResponsesToolToolSearch == nil {
			t.Fatal("expected ResponsesToolToolSearch to be populated")
		}
		if tool.ResponsesToolToolSearch.Execution == nil || *tool.ResponsesToolToolSearch.Execution != "client" {
			t.Fatal("expected execution=client to be preserved")
		}
		if tool.ResponsesToolToolSearch.Parameters == nil {
			t.Fatal("expected parameters to be preserved")
		}
	})
}

func TestToOpenAIResponsesRequest_PreservesNamespaceAndWebSearchFields(t *testing.T) {
	externalWebAccess := true
	bifrostReq := &schemas.BifrostResponsesRequest{
		Model: "gpt-5.4",
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hello")},
		}},
		Params: &schemas.ResponsesParameters{
			Tools: []schemas.ResponsesTool{
				{
					Type: schemas.ResponsesToolTypeWebSearch,
					ResponsesToolWebSearch: &schemas.ResponsesToolWebSearch{
						ExternalWebAccess:  &externalWebAccess,
						SearchContentTypes: []string{"text", "image"},
					},
				},
				{
					Type:        schemas.ResponsesToolTypeNamespace,
					Name:        schemas.Ptr("mcp__node_repl__"),
					Description: schemas.Ptr("node repl tools"),
					ResponsesToolNamespace: &schemas.ResponsesToolNamespace{
						Tools: []schemas.ResponsesTool{{
							Type:        schemas.ResponsesToolTypeFunction,
							Name:        schemas.Ptr("js"),
							Description: schemas.Ptr("run js"),
							ResponsesToolFunction: &schemas.ResponsesToolFunction{
								Parameters: &schemas.ToolFunctionParameters{
									Type: "object",
									Properties: schemas.NewOrderedMapFromPairs(
										schemas.KV("code", schemas.NewOrderedMapFromPairs(
											schemas.KV("type", "string"),
										)),
									),
									Required: []string{"code"},
								},
							},
						}},
					},
				},
			},
		},
	}

	result := ToOpenAIResponsesRequest(nil, bifrostReq)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.Tools) != 2 {
		t.Fatalf("expected 2 tools to survive conversion, got %d", len(result.Tools))
	}

	webSearch := result.Tools[0].ResponsesToolWebSearch
	if webSearch == nil {
		t.Fatal("expected web_search tool to be preserved")
	}
	if webSearch.ExternalWebAccess == nil || !*webSearch.ExternalWebAccess {
		t.Fatal("expected external_web_access=true to be preserved")
	}
	if len(webSearch.SearchContentTypes) != 2 || webSearch.SearchContentTypes[0] != "text" || webSearch.SearchContentTypes[1] != "image" {
		t.Fatalf("expected search_content_types to be preserved, got %+v", webSearch.SearchContentTypes)
	}

	namespace := result.Tools[1]
	if namespace.Type != schemas.ResponsesToolTypeNamespace {
		t.Fatalf("expected namespace tool to survive conversion, got %s", namespace.Type)
	}
	if namespace.ResponsesToolNamespace == nil || len(namespace.ResponsesToolNamespace.Tools) != 1 {
		t.Fatal("expected namespace child tools to be preserved")
	}
}

// =============================================================================
// Helper Functions
// =============================================================================

// mapsEqual compares two maps for equality (including nested maps and arrays)
func mapsEqual(a, b map[string]interface{}) bool {
	if len(a) != len(b) {
		return false
	}

	for k, v1 := range a {
		v2, ok := b[k]
		if !ok {
			return false
		}

		if !valuesEqual(v1, v2) {
			return false
		}
	}

	return true
}

// valuesEqual compares two values for equality (handles nested structures)
func valuesEqual(v1, v2 interface{}) bool {
	switch val1 := v1.(type) {
	case map[string]interface{}:
		val2, ok := v2.(map[string]interface{})
		if !ok {
			return false
		}
		return mapsEqual(val1, val2)

	case []interface{}:
		val2, ok := v2.([]interface{})
		if !ok {
			return false
		}
		if len(val1) != len(val2) {
			return false
		}
		for i := range val1 {
			if !valuesEqual(val1[i], val2[i]) {
				return false
			}
		}
		return true

	default:
		// For primitives, use direct comparison
		return v1 == v2
	}
}

func TestToOpenAIResponsesRequest_OpenRouterServerToolsPreserved(t *testing.T) {
	makeReq := func(provider schemas.ModelProvider, toolType schemas.ResponsesToolType) *schemas.BifrostResponsesRequest {
		return &schemas.BifrostResponsesRequest{
			Provider: provider,
			Model:    "anthropic/claude-haiku-4.5",
			Input: []schemas.ResponsesMessage{
				{
					Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
					Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")},
				},
			},
			Params: &schemas.ResponsesParameters{
				Tools: []schemas.ResponsesTool{{Type: toolType}},
			},
		}
	}

	// Any tool under the "openrouter:" namespace must survive for the OpenRouter
	// provider (web_search, web_fetch, and any future server tool).
	for _, toolType := range []schemas.ResponsesToolType{"openrouter:web_search", "openrouter:web_fetch"} {
		t.Run("openrouter keeps "+string(toolType), func(t *testing.T) {
			result := ToOpenAIResponsesRequest(nil, makeReq(schemas.OpenRouter, toolType))
			if result == nil {
				t.Fatal("ToOpenAIResponsesRequest returned nil")
			}
			if len(result.Tools) != 1 || result.Tools[0].Type != toolType {
				t.Fatalf("expected %s to be preserved for OpenRouter, got %+v", toolType, result.Tools)
			}
		})
	}

	t.Run("openai strips openrouter: namespace tools", func(t *testing.T) {
		result := ToOpenAIResponsesRequest(nil, makeReq(schemas.OpenAI, "openrouter:web_search"))
		if result == nil {
			t.Fatal("ToOpenAIResponsesRequest returned nil")
		}
		if len(result.Tools) != 0 {
			t.Fatalf("expected openrouter: tools to be stripped for OpenAI, got %+v", result.Tools)
		}
	})
}

// Reverse-direction guard for the Responses path: a Gemini thoughtSignature embedded in
// call_id ("<baseID>_ts_<sig>") must be stripped to the base ID before reaching OpenAI,
// which rejects input[].id over 64 chars. The call and its output strip identically so
// they still pair, and the caller's input is left intact.
func TestToOpenAIResponsesRequest_StripsThoughtSignatureFromCallID(t *testing.T) {
	// "_ts_" is the separator used by the native Gemini converters to embed signatures.
	embeddedID := "search_ts_" + strings.Repeat("A", 6000)

	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-4o",
		Input: []schemas.ResponsesMessage{
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:    schemas.Ptr(embeddedID),
					Name:      schemas.Ptr("search"),
					Arguments: schemas.Ptr("{}"),
				},
			},
			{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID: schemas.Ptr(embeddedID),
					Output: &schemas.ResponsesToolMessageOutputStruct{
						ResponsesToolCallOutputStr: schemas.Ptr("result"),
					},
				},
			},
		},
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()
	result := ToOpenAIResponsesRequest(ctx, req)
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	out := result.Input.OpenAIResponsesRequestInputArray
	callID := *out[0].ResponsesToolMessage.CallID
	outputCallID := *out[1].ResponsesToolMessage.CallID

	if callID != "search" {
		t.Errorf("function_call id: got %q, want %q", callID, "search")
	}
	if len(callID) > 64 {
		t.Errorf("function_call id exceeds OpenAI's 64-char limit: %d chars", len(callID))
	}
	if outputCallID != callID {
		t.Errorf("function_call_output id %q must match function_call id %q", outputCallID, callID)
	}

	// The caller's history must be untouched so a later Gemini turn can recover the signature.
	if *req.Input[0].ResponsesToolMessage.CallID != embeddedID {
		t.Error("original function_call call_id was mutated")
	}
	if *req.Input[1].ResponsesToolMessage.CallID != embeddedID {
		t.Error("original function_call_output call_id was mutated")
	}
}

func TestToOpenAIResponsesRequest_OmitsRoleFromNonMessageInputItems(t *testing.T) {
	assistant := schemas.ResponsesInputMessageRoleAssistant
	user := schemas.ResponsesInputMessageRoleUser
	messageType := schemas.ResponsesMessageTypeMessage
	functionCallType := schemas.ResponsesMessageTypeFunctionCall
	req := &schemas.BifrostResponsesRequest{
		Model: "gpt-4o",
		Input: []schemas.ResponsesMessage{
			{
				Type:    &messageType,
				Role:    &user,
				Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hello")},
			},
			{
				Type: &functionCallType,
				Role: &assistant,
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:    schemas.Ptr("call_123"),
					Name:      schemas.Ptr("search"),
					Arguments: schemas.Ptr(`{"query":"bifrost"}`),
				},
			},
		},
	}

	converted := ToOpenAIResponsesRequest(nil, req)
	if converted == nil {
		t.Fatal("ToOpenAIResponsesRequest returned nil")
	}
	wire, err := sonic.Marshal(converted)
	if err != nil {
		t.Fatalf("marshal wire request: %v", err)
	}

	var payload struct {
		Input []json.RawMessage `json:"input"`
	}
	if err := sonic.Unmarshal(wire, &payload); err != nil {
		t.Fatalf("unmarshal wire request: %v", err)
	}

	items := make(map[string]map[string]json.RawMessage, len(payload.Input))
	for _, raw := range payload.Input {
		var item map[string]json.RawMessage
		if err := sonic.Unmarshal(raw, &item); err != nil {
			t.Fatalf("unmarshal input item: %v", err)
		}
		var itemType string
		if err := sonic.Unmarshal(item["type"], &itemType); err != nil {
			t.Fatalf("unmarshal input item type: %v", err)
		}
		items[itemType] = item
	}

	message := items["message"]
	var messageRole string
	if err := sonic.Unmarshal(message["role"], &messageRole); err != nil || messageRole != "user" {
		t.Errorf("message role: got %q, want %q (err=%v)", messageRole, "user", err)
	}
	functionCall := items["function_call"]
	if _, ok := functionCall["role"]; ok {
		t.Error("function_call role present in wire request")
	}
	for field, want := range map[string]string{"call_id": "call_123", "name": "search", "arguments": `{"query":"bifrost"}`} {
		var got string
		if err := sonic.Unmarshal(functionCall[field], &got); err != nil || got != want {
			t.Errorf("function_call %s: got %q, want %q (err=%v)", field, got, want, err)
		}
	}
	if req.Input[0].Role == nil || req.Input[1].Role == nil {
		t.Error("original input roles were mutated")
	}
}

// TestToOpenAIResponsesRequest_DefaultsImageDetail verifies input_image blocks
// missing the detail field get "auto" on the wire (OpenAI's schema requires it
// and strict validators like vLLM reject requests without it), explicit values
// are preserved, and the caller's input is never mutated.
func TestToOpenAIResponsesRequest_DefaultsImageDetail(t *testing.T) {
	imageURL := "data:image/png;base64,iVBORw0KGgo="
	bifrostReq := &schemas.BifrostResponsesRequest{
		Model: "gpt-4o",
		Input: []schemas.ResponsesMessage{
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{
							Type: schemas.ResponsesInputMessageContentBlockTypeText,
							Text: schemas.Ptr("what is in this image?"),
						},
						{
							Type: schemas.ResponsesInputMessageContentBlockTypeImage,
							ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{
								ImageURL: &imageURL,
							},
						},
						{
							Type: schemas.ResponsesInputMessageContentBlockTypeImage,
							ResponsesInputMessageContentBlockImage: &schemas.ResponsesInputMessageContentBlockImage{
								ImageURL: &imageURL,
								Detail:   schemas.Ptr("high"),
							},
						},
					},
				},
			},
		},
	}

	req := ToOpenAIResponsesRequest(nil, bifrostReq)
	if req == nil {
		t.Fatal("converted request is nil")
	}

	blocks := req.Input.OpenAIResponsesRequestInputArray[0].Content.ContentBlocks
	if len(blocks) != 3 {
		t.Fatalf("content blocks: got %d, want 3", len(blocks))
	}
	if blocks[1].ResponsesInputMessageContentBlockImage.Detail == nil ||
		*blocks[1].ResponsesInputMessageContentBlockImage.Detail != "auto" {
		t.Errorf("missing detail not defaulted to auto: %v", blocks[1].ResponsesInputMessageContentBlockImage.Detail)
	}
	if blocks[2].ResponsesInputMessageContentBlockImage.Detail == nil ||
		*blocks[2].ResponsesInputMessageContentBlockImage.Detail != "high" {
		t.Errorf("explicit detail not preserved: %v", blocks[2].ResponsesInputMessageContentBlockImage.Detail)
	}

	// Wire-level: the marshaled JSON must carry detail on every input_image.
	data, err := sonic.Marshal(req)
	if err != nil {
		t.Fatalf("marshal converted request: %v", err)
	}
	if strings.Count(string(data), `"detail"`) != 2 {
		t.Errorf("wire JSON does not carry detail on both image blocks: %s", data)
	}

	// Caller's input must remain untouched.
	original := bifrostReq.Input[0].Content.ContentBlocks[1].ResponsesInputMessageContentBlockImage
	if original.Detail != nil {
		t.Errorf("caller's input was mutated: detail = %q", *original.Detail)
	}
}

// TestToOpenAIResponsesRequest_FallbackBlockDropped verifies that Anthropic's
// server-side fallback boundary marker never reaches OpenAI. Unlike a compaction
// block (which is promoted to text), it carries no user content, so it is dropped.
func TestToOpenAIResponsesRequest_FallbackBlockDropped(t *testing.T) {
	t.Run("fallback block is dropped, surrounding content survives", func(t *testing.T) {
		bifrostReq := &schemas.BifrostResponsesRequest{
			Model: "gpt-5.5",
			Input: []schemas.ResponsesMessage{{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{
						{
							Type: schemas.ResponsesOutputMessageContentTypeFallback,
							ResponsesOutputMessageContentFallback: &schemas.ResponsesOutputMessageContentFallback{
								FromModel: "claude-fable-5",
								ToModel:   "claude-opus-4-8",
							},
						},
						{Type: schemas.ResponsesOutputMessageContentTypeText, Text: schemas.Ptr("Hi there")},
					},
				},
			}},
		}

		result := ToOpenAIResponsesRequest(nil, bifrostReq)
		if result == nil || len(result.Input.OpenAIResponsesRequestInputArray) != 1 {
			t.Fatalf("expected one converted input message, got %#v", result)
		}
		msg := result.Input.OpenAIResponsesRequestInputArray[0]
		if msg.Content == nil {
			t.Fatal("expected converted message to retain content")
		}
		for _, b := range msg.Content.ContentBlocks {
			if b.Type == schemas.ResponsesOutputMessageContentTypeFallback {
				t.Fatalf("fallback block leaked to OpenAI: %#v", msg.Content.ContentBlocks)
			}
			// The marker must not be smuggled through as text either.
			if b.Text != nil && strings.Contains(*b.Text, "claude-fable-5") {
				t.Fatalf("fallback marker rendered as text: %q", *b.Text)
			}
		}
		if len(msg.Content.ContentBlocks) != 1 || msg.Content.ContentBlocks[0].Text == nil || *msg.Content.ContentBlocks[0].Text != "Hi there" {
			t.Fatalf("expected only the surviving text block, got %#v", msg.Content.ContentBlocks)
		}
	})

	t.Run("message with only a fallback block is skipped entirely", func(t *testing.T) {
		bifrostReq := &schemas.BifrostResponsesRequest{
			Model: "gpt-5.5",
			Input: []schemas.ResponsesMessage{{
				Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
				Content: &schemas.ResponsesMessageContent{
					ContentBlocks: []schemas.ResponsesMessageContentBlock{{
						Type: schemas.ResponsesOutputMessageContentTypeFallback,
						ResponsesOutputMessageContentFallback: &schemas.ResponsesOutputMessageContentFallback{
							FromModel: "claude-fable-5",
							ToModel:   "claude-opus-4-8",
						},
					}},
				},
			}},
		}

		result := ToOpenAIResponsesRequest(nil, bifrostReq)
		if result != nil && len(result.Input.OpenAIResponsesRequestInputArray) != 0 {
			t.Fatalf("expected the fallback-only message to be skipped, got %#v", result.Input.OpenAIResponsesRequestInputArray)
		}
	})
}

func TestBuildResponsesRetrieveQuery_Stream(t *testing.T) {
	t.Run("emits stream=true and other params when set", func(t *testing.T) {
		req := &schemas.BifrostResponsesRetrieveRequest{
			ResponseID:         "resp_123",
			Include:            []string{"reasoning.encrypted_content"},
			StartingAfter:      schemas.Ptr(7),
			IncludeObfuscation: schemas.Ptr(false),
			Stream:             schemas.Ptr(true),
		}
		parsed, err := url.ParseQuery(buildResponsesRetrieveQuery(req))
		if err != nil {
			t.Fatalf("query did not parse: %v", err)
		}
		if got := parsed.Get("stream"); got != "true" {
			t.Fatalf("stream = %q, want \"true\"", got)
		}
		if got := parsed.Get("starting_after"); got != "7" {
			t.Fatalf("starting_after = %q, want \"7\"", got)
		}
		if got := parsed.Get("include_obfuscation"); got != "false" {
			t.Fatalf("include_obfuscation = %q, want \"false\"", got)
		}
		if got := parsed.Get("include"); got != "reasoning.encrypted_content" {
			t.Fatalf("include = %q, want \"reasoning.encrypted_content\"", got)
		}
	})

	t.Run("omits stream when unset (unary retrieve)", func(t *testing.T) {
		req := &schemas.BifrostResponsesRetrieveRequest{ResponseID: "resp_123"}
		if strings.Contains(buildResponsesRetrieveQuery(req), "stream") {
			t.Fatalf("unary retrieve query must not contain stream: %q", buildResponsesRetrieveQuery(req))
		}
	})
}

func TestBifrostResponsesRetrieveRequest_IsStreamingRequested(t *testing.T) {
	cases := []struct {
		name string
		req  *schemas.BifrostResponsesRetrieveRequest
		want bool
	}{
		{"nil request", nil, false},
		{"stream unset", &schemas.BifrostResponsesRetrieveRequest{}, false},
		{"stream false", &schemas.BifrostResponsesRetrieveRequest{Stream: schemas.Ptr(false)}, false},
		{"stream true", &schemas.BifrostResponsesRetrieveRequest{Stream: schemas.Ptr(true)}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.req.IsStreamingRequested(); got != tc.want {
				t.Fatalf("IsStreamingRequested() = %v, want %v", got, tc.want)
			}
		})
	}
}
