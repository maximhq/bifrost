package openai

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestApplyXAICompatibility(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		request  *OpenAIChatRequest
		validate func(t *testing.T, req *OpenAIChatRequest)
	}{
		{
			name:  "grok-3: preserves frequency_penalty and stop, clears presence_penalty and reasoning_effort",
			model: "grok-3",
			request: &OpenAIChatRequest{
				Model:    "grok-3",
				Messages: []OpenAIMessage{},
				ChatParameters: schemas.ChatParameters{
					FrequencyPenalty: schemas.Ptr(0.5),
					PresencePenalty:  schemas.Ptr(0.3),
					Stop:             []string{"STOP"},
					Reasoning: &schemas.ChatReasoning{
						Effort: schemas.Ptr("high"),
					},
				},
			},
			validate: func(t *testing.T, req *OpenAIChatRequest) {
				// frequency_penalty should be preserved
				if req.FrequencyPenalty == nil || *req.FrequencyPenalty != 0.5 {
					t.Errorf("Expected FrequencyPenalty to be preserved at 0.5, got %v", req.FrequencyPenalty)
				}

				// stop should be preserved
				if len(req.Stop) != 1 || req.Stop[0] != "STOP" {
					t.Errorf("Expected Stop to be preserved as ['STOP'], got %v", req.Stop)
				}

				// presence_penalty should be cleared
				if req.PresencePenalty != nil {
					t.Errorf("Expected PresencePenalty to be cleared (nil), got %v", *req.PresencePenalty)
				}

				// reasoning_effort should be cleared for non-mini grok-3
				if req.Reasoning == nil {
					t.Fatal("Expected Reasoning to remain non-nil")
				}
				if req.Reasoning.Effort != nil {
					t.Errorf("Expected Reasoning.Effort to be cleared (nil) for grok-3, got %v", *req.Reasoning.Effort)
				}
			},
		},
		{
			name:  "grok-3-mini: clears all penalties and stop, preserves reasoning_effort",
			model: "grok-3-mini",
			request: &OpenAIChatRequest{
				Model:    "grok-3-mini",
				Messages: []OpenAIMessage{},
				ChatParameters: schemas.ChatParameters{
					FrequencyPenalty: schemas.Ptr(0.5),
					PresencePenalty:  schemas.Ptr(0.3),
					Stop:             []string{"STOP"},
					Reasoning: &schemas.ChatReasoning{
						Effort: schemas.Ptr("medium"),
					},
				},
			},
			validate: func(t *testing.T, req *OpenAIChatRequest) {
				// presence_penalty should be cleared
				if req.PresencePenalty != nil {
					t.Errorf("Expected PresencePenalty to be cleared (nil), got %v", *req.PresencePenalty)
				}

				// frequency_penalty should be cleared for grok-3-mini
				if req.FrequencyPenalty != nil {
					t.Errorf("Expected FrequencyPenalty to be cleared (nil) for grok-3-mini, got %v", *req.FrequencyPenalty)
				}

				// stop should be cleared for grok-3-mini
				if req.Stop != nil {
					t.Errorf("Expected Stop to be cleared (nil) for grok-3-mini, got %v", req.Stop)
				}

				// reasoning_effort should be preserved for grok-3-mini
				if req.Reasoning == nil || req.Reasoning.Effort == nil {
					t.Fatal("Expected Reasoning.Effort to be preserved for grok-3-mini")
				}
				if *req.Reasoning.Effort != "medium" {
					t.Errorf("Expected Reasoning.Effort to be 'medium', got %v", *req.Reasoning.Effort)
				}
			},
		},
		{
			name:  "grok-4: clears all penalties, stop, and reasoning_effort",
			model: "grok-4",
			request: &OpenAIChatRequest{
				Model:    "grok-4",
				Messages: []OpenAIMessage{},
				ChatParameters: schemas.ChatParameters{
					FrequencyPenalty: schemas.Ptr(0.5),
					PresencePenalty:  schemas.Ptr(0.3),
					Stop:             []string{"STOP"},
					Reasoning: &schemas.ChatReasoning{
						Effort: schemas.Ptr("high"),
					},
				},
			},
			validate: func(t *testing.T, req *OpenAIChatRequest) {
				// presence_penalty should be cleared
				if req.PresencePenalty != nil {
					t.Errorf("Expected PresencePenalty to be cleared (nil), got %v", *req.PresencePenalty)
				}

				// frequency_penalty should be cleared for grok-4
				if req.FrequencyPenalty != nil {
					t.Errorf("Expected FrequencyPenalty to be cleared (nil) for grok-4, got %v", *req.FrequencyPenalty)
				}

				// stop should be cleared for grok-4
				if req.Stop != nil {
					t.Errorf("Expected Stop to be cleared (nil) for grok-4, got %v", req.Stop)
				}

				// reasoning_effort should be cleared for grok-4
				if req.Reasoning == nil {
					t.Fatal("Expected Reasoning to remain non-nil")
				}
				if req.Reasoning.Effort != nil {
					t.Errorf("Expected Reasoning.Effort to be cleared (nil) for grok-4, got %v", *req.Reasoning.Effort)
				}
			},
		},
		{
			name:  "grok-4-fast-reasoning: clears all penalties, stop, and reasoning_effort",
			model: "grok-4-fast-reasoning",
			request: &OpenAIChatRequest{
				Model:    "grok-4-fast-reasoning",
				Messages: []OpenAIMessage{},
				ChatParameters: schemas.ChatParameters{
					FrequencyPenalty: schemas.Ptr(0.5),
					PresencePenalty:  schemas.Ptr(0.3),
					Stop:             []string{"STOP", "END"},
					Reasoning: &schemas.ChatReasoning{
						Effort: schemas.Ptr("high"),
					},
				},
			},
			validate: func(t *testing.T, req *OpenAIChatRequest) {
				// presence_penalty should be cleared
				if req.PresencePenalty != nil {
					t.Errorf("Expected PresencePenalty to be cleared (nil), got %v", *req.PresencePenalty)
				}

				// frequency_penalty should be cleared
				if req.FrequencyPenalty != nil {
					t.Errorf("Expected FrequencyPenalty to be cleared (nil), got %v", *req.FrequencyPenalty)
				}

				// stop should be cleared
				if req.Stop != nil {
					t.Errorf("Expected Stop to be cleared (nil), got %v", req.Stop)
				}

				// reasoning_effort should be cleared
				if req.Reasoning == nil {
					t.Fatal("Expected Reasoning to remain non-nil")
				}
				if req.Reasoning.Effort != nil {
					t.Errorf("Expected Reasoning.Effort to be cleared (nil), got %v", *req.Reasoning.Effort)
				}
			},
		},
		{
			name:  "grok-code-fast-1: clears all penalties, stop, and reasoning_effort",
			model: "grok-code-fast-1",
			request: &OpenAIChatRequest{
				Model:    "grok-code-fast-1",
				Messages: []OpenAIMessage{},
				ChatParameters: schemas.ChatParameters{
					FrequencyPenalty: schemas.Ptr(0.2),
					PresencePenalty:  schemas.Ptr(0.1),
					Stop:             []string{"END"},
					Reasoning: &schemas.ChatReasoning{
						Effort: schemas.Ptr("low"),
					},
				},
			},
			validate: func(t *testing.T, req *OpenAIChatRequest) {
				// presence_penalty should be cleared
				if req.PresencePenalty != nil {
					t.Errorf("Expected PresencePenalty to be cleared (nil), got %v", *req.PresencePenalty)
				}

				// frequency_penalty should be cleared
				if req.FrequencyPenalty != nil {
					t.Errorf("Expected FrequencyPenalty to be cleared (nil), got %v", *req.FrequencyPenalty)
				}

				// stop should be cleared
				if req.Stop != nil {
					t.Errorf("Expected Stop to be cleared (nil), got %v", req.Stop)
				}

				// reasoning_effort should be cleared
				if req.Reasoning == nil {
					t.Fatal("Expected Reasoning to remain non-nil")
				}
				if req.Reasoning.Effort != nil {
					t.Errorf("Expected Reasoning.Effort to be cleared (nil), got %v", *req.Reasoning.Effort)
				}
			},
		},
		{
			name:  "non-reasoning grok model: no changes applied",
			model: "grok-2-latest",
			request: &OpenAIChatRequest{
				Model:    "grok-2-latest",
				Messages: []OpenAIMessage{},
				ChatParameters: schemas.ChatParameters{
					FrequencyPenalty: schemas.Ptr(0.5),
					PresencePenalty:  schemas.Ptr(0.3),
					Stop:             []string{"STOP"},
					Reasoning: &schemas.ChatReasoning{
						Effort: schemas.Ptr("high"),
					},
				},
			},
			validate: func(t *testing.T, req *OpenAIChatRequest) {
				// All parameters should be preserved for non-reasoning models
				if req.FrequencyPenalty == nil || *req.FrequencyPenalty != 0.5 {
					t.Errorf("Expected FrequencyPenalty to be preserved at 0.5, got %v", req.FrequencyPenalty)
				}

				if req.PresencePenalty == nil || *req.PresencePenalty != 0.3 {
					t.Errorf("Expected PresencePenalty to be preserved at 0.3, got %v", req.PresencePenalty)
				}

				if len(req.Stop) != 1 || req.Stop[0] != "STOP" {
					t.Errorf("Expected Stop to be preserved as ['STOP'], got %v", req.Stop)
				}

				if req.Reasoning == nil || req.Reasoning.Effort == nil {
					t.Fatal("Expected Reasoning.Effort to be preserved")
				}
				if *req.Reasoning.Effort != "high" {
					t.Errorf("Expected Reasoning.Effort to be 'high', got %v", *req.Reasoning.Effort)
				}
			},
		},
		{
			name:  "grok-3: handles nil reasoning gracefully",
			model: "grok-3",
			request: &OpenAIChatRequest{
				Model:    "grok-3",
				Messages: []OpenAIMessage{},
				ChatParameters: schemas.ChatParameters{
					FrequencyPenalty: schemas.Ptr(0.5),
					PresencePenalty:  schemas.Ptr(0.3),
					Stop:             []string{"STOP"},
					Reasoning:        nil,
				},
			},
			validate: func(t *testing.T, req *OpenAIChatRequest) {
				// Should handle nil reasoning without panicking
				if req.Reasoning != nil {
					t.Errorf("Expected Reasoning to remain nil, got %v", req.Reasoning)
				}

				// Other parameters should still be processed
				if req.PresencePenalty != nil {
					t.Errorf("Expected PresencePenalty to be cleared (nil), got %v", *req.PresencePenalty)
				}

				if req.FrequencyPenalty == nil || *req.FrequencyPenalty != 0.5 {
					t.Errorf("Expected FrequencyPenalty to be preserved at 0.5, got %v", req.FrequencyPenalty)
				}
			},
		},
		{
			name:  "grok-3: preserves other parameters like temperature",
			model: "grok-3",
			request: &OpenAIChatRequest{
				Model:    "grok-3",
				Messages: []OpenAIMessage{},
				ChatParameters: schemas.ChatParameters{
					Temperature:      schemas.Ptr(0.8),
					TopP:             schemas.Ptr(0.9),
					FrequencyPenalty: schemas.Ptr(0.5),
					PresencePenalty:  schemas.Ptr(0.3),
				},
			},
			validate: func(t *testing.T, req *OpenAIChatRequest) {
				// Unrelated parameters should be preserved
				if req.Temperature == nil || *req.Temperature != 0.8 {
					t.Errorf("Expected Temperature to be preserved at 0.8, got %v", req.Temperature)
				}

				if req.TopP == nil || *req.TopP != 0.9 {
					t.Errorf("Expected TopP to be preserved at 0.9, got %v", req.TopP)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Apply the compatibility function
			tt.request.applyXAICompatibility(tt.model)

			// Validate the results
			tt.validate(t, tt.request)
		})
	}
}

func TestApplyReasoningMessageCompatibility(t *testing.T) {
	tests := []struct {
		name               string
		model              string
		request            *OpenAIChatRequest
		expectReasoning    *string
		expectReasoningRaw *string
	}{
		{
			name:  "fireworks model remaps reasoning to reasoning_content",
			model: "accounts/fireworks/models/kimi-k2p5",
			request: &OpenAIChatRequest{
				Messages: []OpenAIMessage{
					{
						Role: schemas.ChatMessageRoleAssistant,
						OpenAIChatAssistantMessage: &OpenAIChatAssistantMessage{
							Reasoning: schemas.Ptr("internal trace"),
						},
					},
				},
			},
			expectReasoning:    nil,
			expectReasoningRaw: schemas.Ptr("internal trace"),
		},
		{
			name:  "fireworks model preserves explicit reasoning_content",
			model: "accounts/fireworks/models/kimi-k2p5",
			request: &OpenAIChatRequest{
				Messages: []OpenAIMessage{
					{
						Role: schemas.ChatMessageRoleAssistant,
						OpenAIChatAssistantMessage: &OpenAIChatAssistantMessage{
							Reasoning:        schemas.Ptr("normalized"),
							ReasoningContent: schemas.Ptr("raw provider content"),
						},
					},
				},
			},
			expectReasoning:    nil,
			expectReasoningRaw: schemas.Ptr("raw provider content"),
		},
		{
			name:  "non-fireworks model keeps reasoning field",
			model: "gpt-4o",
			request: &OpenAIChatRequest{
				Messages: []OpenAIMessage{
					{
						Role: schemas.ChatMessageRoleAssistant,
						OpenAIChatAssistantMessage: &OpenAIChatAssistantMessage{
							Reasoning: schemas.Ptr("internal trace"),
						},
					},
				},
			},
			expectReasoning:    schemas.Ptr("internal trace"),
			expectReasoningRaw: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.request.applyReasoningMessageCompatibility(tt.model)
			assistant := tt.request.Messages[0].OpenAIChatAssistantMessage
			if assistant == nil {
				t.Fatal("Expected assistant message")
			}

			if tt.expectReasoning == nil {
				if assistant.Reasoning != nil {
					t.Fatalf("Expected reasoning to be nil, got %q", *assistant.Reasoning)
				}
			} else if assistant.Reasoning == nil || *assistant.Reasoning != *tt.expectReasoning {
				t.Fatalf("Expected reasoning %q, got %+v", *tt.expectReasoning, assistant.Reasoning)
			}

			if tt.expectReasoningRaw == nil {
				if assistant.ReasoningContent != nil {
					t.Fatalf("Expected reasoning_content to be nil, got %q", *assistant.ReasoningContent)
				}
			} else if assistant.ReasoningContent == nil || *assistant.ReasoningContent != *tt.expectReasoningRaw {
				t.Fatalf("Expected reasoning_content %q, got %+v", *tt.expectReasoningRaw, assistant.ReasoningContent)
			}
		})
	}
}
