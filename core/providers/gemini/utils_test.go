package gemini

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Gemini 3.x occasionally ends a tool-using turn with MALFORMED_RESPONSE (or
// MALFORMED_FUNCTION_CALL) and a candidate holding only its role: the model tried
// to emit a function call it could not encode. Mapped to "stop", that reached
// Anthropic-shaped clients as end_turn with empty content, and Claude Code stopped
// mid-task. It must instead fail as a retryable 502.
const malformedResponseBody = `{"candidates":[{"content":{"role":"model"},"finishReason":"MALFORMED_RESPONSE"}],` +
	`"usageMetadata":{"promptTokenCount":30,"totalTokenCount":111,"thoughtsTokenCount":81},` +
	`"modelVersion":"gemini-3.1-pro-preview","responseId":"HzkEa9eBCNyRvfIPuK6LKA"}`

func TestMalformedOutputError(t *testing.T) {
	t.Run("MalformedResponseIsRetryable502", func(t *testing.T) {
		var response GenerateContentResponse
		require.NoError(t, json.Unmarshal([]byte(malformedResponseBody), &response))

		err := response.MalformedOutputError()
		require.NotNil(t, err)
		require.NotNil(t, err.StatusCode)
		assert.Equal(t, 502, *err.StatusCode)
		assert.False(t, err.IsBifrostError)
		require.NotNil(t, err.Type)
		assert.Equal(t, "malformed_response", *err.Type)
		assert.Contains(t, err.Error.Message, "MALFORMED_RESPONSE")
	})

	t.Run("MalformedFunctionCallIncludesFinishMessage", func(t *testing.T) {
		response := &GenerateContentResponse{
			Candidates: []*Candidate{{
				FinishReason:  FinishReasonMalformedFunctionCall,
				FinishMessage: "Malformed function call: get_weather(",
			}},
		}

		err := response.MalformedOutputError()
		require.NotNil(t, err)
		assert.Equal(t, "malformed_function_call", *err.Type)
		assert.Contains(t, err.Error.Message, "get_weather(")
	})

	t.Run("OtherFinishReasonsPassThrough", func(t *testing.T) {
		for _, reason := range []FinishReason{
			FinishReasonStop,
			FinishReasonMaxTokens,
			FinishReasonSafety,
		} {
			response := &GenerateContentResponse{
				Candidates: []*Candidate{{FinishReason: reason}},
			}
			assert.Nil(t, response.MalformedOutputError(), reason)
		}
		assert.Nil(t, (&GenerateContentResponse{}).MalformedOutputError())
		assert.Nil(t, (*GenerateContentResponse)(nil).MalformedOutputError())
	})
}
