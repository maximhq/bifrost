package openai

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestConvertBifrostMessagesToOpenAIMessages_ReasoningContentPassthrough(t *testing.T) {
	reasoning := "normalized reasoning"
	reasoningContent := "provider reasoning content"
	reasoningText := "step by step"

	messages := []schemas.ChatMessage{
		{
			Role: schemas.ChatMessageRoleAssistant,
			ChatAssistantMessage: &schemas.ChatAssistantMessage{
				Reasoning:        &reasoning,
				ReasoningContent: &reasoningContent,
				ReasoningDetails: []schemas.ChatReasoningDetails{
					{
						Index: 0,
						Type:  schemas.BifrostReasoningDetailsTypeText,
						Text:  &reasoningText,
					},
				},
			},
		},
	}

	converted := ConvertBifrostMessagesToOpenAIMessages(messages)
	if len(converted) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(converted))
	}

	assistant := converted[0].OpenAIChatAssistantMessage
	if assistant == nil {
		t.Fatal("Expected assistant message to be present")
	}

	if assistant.Reasoning != nil {
		t.Fatalf("Expected reasoning to be omitted when reasoning_content is present, got %q", *assistant.Reasoning)
	}

	if assistant.ReasoningContent == nil || *assistant.ReasoningContent != reasoningContent {
		t.Fatalf("Expected reasoning_content %q, got %+v", reasoningContent, assistant.ReasoningContent)
	}

	if len(assistant.ReasoningDetails) != 1 {
		t.Fatalf("Expected 1 reasoning detail, got %d", len(assistant.ReasoningDetails))
	}

	if assistant.ReasoningDetails[0].Text == nil || *assistant.ReasoningDetails[0].Text != reasoningText {
		t.Fatalf("Expected reasoning detail text %q, got %+v", reasoningText, assistant.ReasoningDetails[0].Text)
	}
}

func TestConvertBifrostMessagesToOpenAIMessages_ReasoningFallback(t *testing.T) {
	reasoning := "legacy reasoning"

	messages := []schemas.ChatMessage{
		{
			Role: schemas.ChatMessageRoleAssistant,
			ChatAssistantMessage: &schemas.ChatAssistantMessage{
				Reasoning: &reasoning,
			},
		},
	}

	converted := ConvertBifrostMessagesToOpenAIMessages(messages)
	assistant := converted[0].OpenAIChatAssistantMessage
	if assistant == nil {
		t.Fatal("Expected assistant message to be present")
	}
	if assistant.Reasoning == nil || *assistant.Reasoning != reasoning {
		t.Fatalf("Expected reasoning %q, got %+v", reasoning, assistant.Reasoning)
	}
	if assistant.ReasoningContent != nil {
		t.Fatalf("Expected reasoning_content to be nil, got %q", *assistant.ReasoningContent)
	}
}

func TestConvertOpenAIMessagesToBifrostMessages_ReasoningContentNormalized(t *testing.T) {
	reasoningContent := "provider reasoning"

	messages := []OpenAIMessage{
		{
			Role: schemas.ChatMessageRoleAssistant,
			OpenAIChatAssistantMessage: &OpenAIChatAssistantMessage{
				ReasoningContent: &reasoningContent,
				ReasoningDetails: []schemas.ChatReasoningDetails{
					{
						Index: 0,
						Type:  schemas.BifrostReasoningDetailsTypeText,
						Text:  &reasoningContent,
					},
				},
			},
		},
	}

	converted := ConvertOpenAIMessagesToBifrostMessages(messages)
	if len(converted) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(converted))
	}

	assistant := converted[0].ChatAssistantMessage
	if assistant == nil {
		t.Fatal("Expected assistant message to be present")
	}

	if assistant.ReasoningContent == nil || *assistant.ReasoningContent != reasoningContent {
		t.Fatalf("Expected reasoning_content %q, got %+v", reasoningContent, assistant.ReasoningContent)
	}

	if assistant.Reasoning == nil || *assistant.Reasoning != reasoningContent {
		t.Fatalf("Expected normalized reasoning %q, got %+v", reasoningContent, assistant.Reasoning)
	}

	if len(assistant.ReasoningDetails) != 1 {
		t.Fatalf("Expected 1 reasoning detail, got %d", len(assistant.ReasoningDetails))
	}
}
