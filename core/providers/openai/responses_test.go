package openai

import (
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestToOpenAIResponsesRequest_ReasoningOnlyMessageSkip(t *testing.T) {
	tests := []struct {
		name             string
		model            string
		message          schemas.ResponsesMessage
		expectedIncluded bool
		description      string
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
			name:  "message with EncryptedContent preserved for non-gpt-oss model",
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
			expectedIncluded: true,
			description:      "Message with non-nil EncryptedContent should be preserved even if Summary is empty",
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

			result := ToOpenAIResponsesRequest(bifrostReq)

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

			result := ToOpenAIResponsesRequest(bifrostReq)

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
