package cohere

import (
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// Cohere tool messages take text: media a tool returned becomes a placeholder naming it.
func TestToCohereChatCompletionRequest_ToolResultNamesMedia(t *testing.T) {
	req, err := ToCohereChatCompletionRequest(&schemas.BifrostChatRequest{
		Provider: schemas.Cohere,
		Model:    "command-a-03-2025",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("plot it")}},
			{Role: schemas.ChatMessageRoleTool, ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr("c1")}, Content: &schemas.ChatMessageContent{ContentBlocks: []schemas.ChatContentBlock{
				{Type: schemas.ChatContentBlockTypeText, Text: schemas.Ptr("chart below")},
				{Type: schemas.ChatContentBlockTypeImage, ImageURLStruct: &schemas.ChatInputImage{URL: "data:image/png;base64,iVBORw0KGgo="}},
				{Type: schemas.ChatContentBlockTypeInputAudio, InputAudio: &schemas.ChatInputAudio{Data: "SUQz", Format: schemas.Ptr("mp3")}},
			}}},
		},
	})
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	wire, err := schemas.MarshalSorted(req.Messages[1])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{"chart below", "[image/png", "[audio/mp3"} {
		if !strings.Contains(string(wire), want) {
			t.Fatalf("expected %q in %s", want, wire)
		}
	}
	if strings.Contains(string(wire), "image_url") {
		t.Fatalf("no image part may reach a Cohere tool message: %s", wire)
	}
}

func TestToBifrostChatMessage_ToolPlanSetsReasoning(t *testing.T) {
	funcName := "get_weather"

	t.Run("tool plan alone with tool calls", func(t *testing.T) {
		plan := "I need to check the weather in Paris before answering."
		cm := &CohereMessage{
			Role:     "assistant",
			ToolPlan: &plan,
			ToolCalls: []CohereToolCall{
				{
					ID:   schemas.Ptr("call_123"),
					Type: "function",
					Function: &CohereFunction{
						Name:      &funcName,
						Arguments: `{"city":"Paris"}`,
					},
				},
			},
		}

		bifrostMsg := cm.ToBifrostChatMessage()
		if bifrostMsg == nil {
			t.Fatal("expected non-nil message")
		}
		if bifrostMsg.ChatAssistantMessage == nil {
			t.Fatal("expected non-nil ChatAssistantMessage")
		}
		if bifrostMsg.ChatAssistantMessage.Reasoning == nil {
			t.Fatal("expected non-nil Reasoning on ChatAssistantMessage")
		}
		if *bifrostMsg.ChatAssistantMessage.Reasoning != plan {
			t.Fatalf("got Reasoning %q, want %q", *bifrostMsg.ChatAssistantMessage.Reasoning, plan)
		}
		if len(bifrostMsg.ChatAssistantMessage.ReasoningDetails) != 1 {
			t.Fatalf("got %d ReasoningDetails, want 1", len(bifrostMsg.ChatAssistantMessage.ReasoningDetails))
		}
		detail := bifrostMsg.ChatAssistantMessage.ReasoningDetails[0]
		if detail.Type != schemas.BifrostReasoningDetailsTypeText {
			t.Fatalf("got detail type %q, want %q", detail.Type, schemas.BifrostReasoningDetailsTypeText)
		}
		if detail.Text == nil || *detail.Text != plan {
			t.Fatalf("got detail text %v, want %q", detail.Text, plan)
		}
	})

	t.Run("tool plan merged with existing thinking block", func(t *testing.T) {
		thinking := "Let me think about this query."
		plan := "Now I will call the weather tool."
		cm := &CohereMessage{
			Role: "assistant",
			Content: &CohereMessageContent{
				BlocksContent: []CohereContentBlock{
					{
						Type:     CohereContentBlockTypeThinking,
						Thinking: &thinking,
					},
				},
			},
			ToolPlan: &plan,
			ToolCalls: []CohereToolCall{
				{
					ID:   schemas.Ptr("call_123"),
					Type: "function",
					Function: &CohereFunction{
						Name:      &funcName,
						Arguments: `{"city":"Paris"}`,
					},
				},
			},
		}

		bifrostMsg := cm.ToBifrostChatMessage()
		if bifrostMsg == nil || bifrostMsg.ChatAssistantMessage == nil {
			t.Fatal("expected non-nil assistant message")
		}
		wantReasoning := thinking + "\n" + plan
		if bifrostMsg.ChatAssistantMessage.Reasoning == nil || *bifrostMsg.ChatAssistantMessage.Reasoning != wantReasoning {
			t.Fatalf("got Reasoning %v, want %q", bifrostMsg.ChatAssistantMessage.Reasoning, wantReasoning)
		}
		if len(bifrostMsg.ChatAssistantMessage.ReasoningDetails) != 2 {
			t.Fatalf("got %d ReasoningDetails, want 2", len(bifrostMsg.ChatAssistantMessage.ReasoningDetails))
		}
		if *bifrostMsg.ChatAssistantMessage.ReasoningDetails[0].Text != thinking {
			t.Fatalf("detail 0 text = %q, want %q", *bifrostMsg.ChatAssistantMessage.ReasoningDetails[0].Text, thinking)
		}
		if *bifrostMsg.ChatAssistantMessage.ReasoningDetails[1].Text != plan {
			t.Fatalf("detail 1 text = %q, want %q", *bifrostMsg.ChatAssistantMessage.ReasoningDetails[1].Text, plan)
		}
		if bifrostMsg.ChatAssistantMessage.ReasoningDetails[1].Index != 1 {
			t.Fatalf("detail 1 index = %d, want 1", bifrostMsg.ChatAssistantMessage.ReasoningDetails[1].Index)
		}
	})

	t.Run("empty or nil tool plan does not set reasoning", func(t *testing.T) {
		emptyPlan := ""
		cm := &CohereMessage{
			Role:     "assistant",
			ToolPlan: &emptyPlan,
			ToolCalls: []CohereToolCall{
				{
					ID:   schemas.Ptr("call_123"),
					Type: "function",
					Function: &CohereFunction{
						Name:      &funcName,
						Arguments: `{"city":"Paris"}`,
					},
				},
			},
		}

		bifrostMsg := cm.ToBifrostChatMessage()
		if bifrostMsg == nil || bifrostMsg.ChatAssistantMessage == nil {
			t.Fatal("expected non-nil assistant message")
		}
		if bifrostMsg.ChatAssistantMessage.Reasoning != nil {
			t.Fatalf("expected nil Reasoning for empty ToolPlan, got %q", *bifrostMsg.ChatAssistantMessage.Reasoning)
		}
		if len(bifrostMsg.ChatAssistantMessage.ReasoningDetails) != 0 {
			t.Fatalf("expected 0 ReasoningDetails, got %d", len(bifrostMsg.ChatAssistantMessage.ReasoningDetails))
		}
	})
}

func BenchmarkToBifrostChatMessage_Common(b *testing.B) {
	text := "Hello world"
	cm := &CohereMessage{
		Role:    "assistant",
		Content: &CohereMessageContent{StringContent: &text},
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = cm.ToBifrostChatMessage()
	}
}

func BenchmarkToBifrostChatMessage_WithToolPlan(b *testing.B) {
	plan := "I need to check the weather in Paris before answering."
	funcName := "get_weather"
	cm := &CohereMessage{
		Role:     "assistant",
		ToolPlan: &plan,
		ToolCalls: []CohereToolCall{
			{
				ID:   schemas.Ptr("call_123"),
				Type: "function",
				Function: &CohereFunction{
					Name:      &funcName,
					Arguments: `{"city":"Paris"}`,
				},
			},
		},
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = cm.ToBifrostChatMessage()
	}
}
