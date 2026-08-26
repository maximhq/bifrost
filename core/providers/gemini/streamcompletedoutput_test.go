package gemini

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// completedOutputStreamChunks mirrors a plain Gemini streamed answer: text arrives in
// two chunks, then a terminal chunk carrying only finishReason and usage.
func completedOutputStreamChunks() []*GenerateContentResponse {
	return []*GenerateContentResponse{
		{
			ResponseID:   "resp-output",
			ModelVersion: "gemini-2.5-flash",
			Candidates: []*Candidate{{
				Content: &Content{Role: "model", Parts: []*Part{{Text: "Hello"}}},
			}},
		},
		{
			ResponseID:   "resp-output",
			ModelVersion: "gemini-2.5-flash",
			Candidates: []*Candidate{{
				Content: &Content{Role: "model", Parts: []*Part{{Text: " world"}}},
			}},
		},
		{
			ResponseID:   "resp-output",
			ModelVersion: "gemini-2.5-flash",
			Candidates: []*Candidate{{
				FinishReason: FinishReasonStop,
			}},
			UsageMetadata: &GenerateContentResponseUsageMetadata{
				PromptTokenCount:     4,
				CandidatesTokenCount: 3,
				TotalTokenCount:      7,
			},
		},
	}
}

// TestGeminiResponsesStreamCompletedCarriesOutput pins the OpenAI Responses contract
// that the terminal response.completed event embeds the full output array. Gemini
// streams the text through output_text.delta / output_item.done, but the terminal
// response was built from ID/CreatedAt/Usage only, so Output was left nil and the
// assistant turn vanished from the completed event.
func TestGeminiResponsesStreamCompletedCarriesOutput(t *testing.T) {
	state := &GeminiResponsesStreamState{}
	state.flush()

	var all []*schemas.BifrostResponsesStreamResponse
	seq := 0
	for _, chunk := range completedOutputStreamChunks() {
		events, bifrostErr := chunk.ToBifrostResponsesStream(seq, state)
		require.Nil(t, bifrostErr, "unexpected forward conversion error")
		all = append(all, events...)
		seq += len(events)
	}

	// Precondition: the deltas really did stream, so this is not an empty generation.
	var streamedText string
	for _, event := range all {
		if event.Type == schemas.ResponsesStreamResponseTypeOutputTextDelta && event.Delta != nil {
			streamedText += *event.Delta
		}
	}
	require.Equal(t, "Hello world", streamedText, "precondition: text deltas must have streamed")

	var terminal *schemas.BifrostResponsesStreamResponse
	for _, event := range all {
		if event.Type == schemas.ResponsesStreamResponseTypeCompleted {
			terminal = event
		}
	}
	require.NotNil(t, terminal, "stream must end with a response.completed event")
	require.NotNil(t, terminal.Response, "response.completed must embed a response object")

	require.NotNil(t, terminal.Response.Output,
		"response.completed must carry the output array, not nil (nil marshals to \"output\":null)")
	require.NotEmpty(t, terminal.Response.Output,
		"response.completed output must contain the assistant message that was streamed")

	msg := terminal.Response.Output[0]
	require.NotNil(t, msg.Type)
	assert.Equal(t, schemas.ResponsesMessageTypeMessage, *msg.Type)
	require.NotNil(t, msg.Content)
	require.NotEmpty(t, msg.Content.ContentBlocks)
	require.NotNil(t, msg.Content.ContentBlocks[0].Text)
	assert.Equal(t, "Hello world", *msg.Content.ContentBlocks[0].Text,
		"the completed output must reproduce the streamed text")
}

// TestGeminiResponsesStreamOutputDoesNotLeakAcrossStreams guards the pooled stream
// state: OutputItems must be cleared on flush, or one request's assistant turn would
// reappear in the next request's response.completed.
func TestGeminiResponsesStreamOutputDoesNotLeakAcrossStreams(t *testing.T) {
	state := &GeminiResponsesStreamState{}
	state.flush()

	seq := 0
	for _, chunk := range completedOutputStreamChunks() {
		events, bifrostErr := chunk.ToBifrostResponsesStream(seq, state)
		require.Nil(t, bifrostErr)
		seq += len(events)
	}
	require.NotEmpty(t, state.OutputItems, "precondition: first stream recorded output items")

	// Reusing the pooled state for the next request must start clean.
	state.flush()
	assert.Empty(t, state.OutputItems,
		"flush must clear OutputItems so a pooled state cannot leak output between streams")
}
