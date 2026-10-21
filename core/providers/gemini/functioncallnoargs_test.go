package gemini_test

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/providers/gemini"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Gemini omits `args` for a function call to a tool without parameters. The
// chat completion's tool call arguments must still be a JSON object, because
// OpenAI-shaped clients parse the string as is.
func noArgsResponse(args json.RawMessage) *gemini.GenerateContentResponse {
	return &gemini.GenerateContentResponse{
		ResponseID: "noargs-test",
		Candidates: []*gemini.Candidate{{
			Content: &gemini.Content{
				Role:  "model",
				Parts: []*gemini.Part{{FunctionCall: &gemini.FunctionCall{Name: "now", Args: args}}},
			},
			FinishReason: gemini.FinishReasonStop,
		}},
	}
}

func TestFunctionCallWithoutArgsGetsEmptyObject(t *testing.T) {
	for name, args := range map[string]json.RawMessage{"omitted": nil, "empty object": json.RawMessage(`{}`)} {
		t.Run("non-stream/"+name, func(t *testing.T) {
			resp := noArgsResponse(args).ToBifrostChatResponse()
			require.NotNil(t, resp)
			require.Len(t, resp.Choices, 1)
			calls := resp.Choices[0].ChatNonStreamResponseChoice.Message.ChatAssistantMessage.ToolCalls
			require.Len(t, calls, 1)
			assert.Equal(t, "{}", calls[0].Function.Arguments)
		})
		t.Run("stream/"+name, func(t *testing.T) {
			chunks, bifrostErr, _ := noArgsResponse(args).ToBifrostChatCompletionStream(gemini.NewGeminiStreamState())
			require.Nil(t, bifrostErr)
			var arguments string
			for _, chunk := range chunks {
				for _, choice := range chunk.Choices {
					if choice.ChatStreamResponseChoice == nil || choice.ChatStreamResponseChoice.Delta == nil {
						continue
					}
					for _, call := range choice.ChatStreamResponseChoice.Delta.ToolCalls {
						arguments += call.Function.Arguments
					}
				}
			}
			assert.Equal(t, "{}", arguments)
		})
	}
}

func BenchmarkFunctionCallChatConversion(b *testing.B) {
	response := noArgsResponse(json.RawMessage(`{"city":"NYC"}`))
	b.Run("non-stream", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = response.ToBifrostChatResponse()
		}
	})
	b.Run("stream", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_, _, _ = response.ToBifrostChatCompletionStream(gemini.NewGeminiStreamState())
		}
	})
}
