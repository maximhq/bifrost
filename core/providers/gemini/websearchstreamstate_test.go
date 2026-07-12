package gemini

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// groundedStreamChunks returns a minimal grounded stream: a text chunk followed
// by a final chunk carrying finishReason plus groundingMetadata, which is how
// the Gemini API delivers google_search results on streaming responses.
func groundedStreamChunks() []*GenerateContentResponse {
	return []*GenerateContentResponse{
		{
			ResponseID:   "resp-grounded",
			ModelVersion: "gemini-2.5-flash",
			Candidates: []*Candidate{{
				Content: &Content{
					Role:  "model",
					Parts: []*Part{{Text: "The Eiffel Tower is 330 metres tall."}},
				},
			}},
		},
		{
			ResponseID:   "resp-grounded",
			ModelVersion: "gemini-2.5-flash",
			Candidates: []*Candidate{{
				FinishReason: FinishReasonStop,
				GroundingMetadata: &GroundingMetadata{
					WebSearchQueries: []string{"eiffel tower height"},
					GroundingChunks: []*GroundingChunk{{
						Web: &GroundingChunkWeb{
							URI:   "https://vertexaisearch.cloud.google.com/grounding-api-redirect/example",
							Title: "toureiffel.paris",
						},
					}},
					GroundingSupports: []*GroundingSupport{{
						Segment: &Segment{
							StartIndex: 0,
							EndIndex:   36,
							Text:       "The Eiffel Tower is 330 metres tall.",
						},
						GroundingChunkIndices: []int32{0},
					}},
					SearchEntryPoint: &SearchEntryPoint{
						RenderedContent: "<div>Google Search Suggestions</div>",
					},
				},
			}},
			UsageMetadata: &GenerateContentResponseUsageMetadata{
				PromptTokenCount:     8,
				CandidatesTokenCount: 12,
				TotalTokenCount:      20,
			},
		},
	}
}

// driveGroundedResponsesStream replays the grounded chunk sequence through
// ToBifrostResponsesStream against the given state, mirroring the
// sequence-number accounting of HandleGeminiResponsesStream.
func driveGroundedResponsesStream(t *testing.T, state *GeminiResponsesStreamState) []*schemas.BifrostResponsesStreamResponse {
	t.Helper()

	var out []*schemas.BifrostResponsesStreamResponse
	seq := 0
	for _, chunk := range groundedStreamChunks() {
		responses, bifrostErr := chunk.ToBifrostResponsesStream(seq, state)
		require.Nil(t, bifrostErr, "unexpected conversion error")
		out = append(out, responses...)
		seq += len(responses)
	}
	return out
}

func responsesStreamEventTypes(responses []*schemas.BifrostResponsesStreamResponse) []schemas.ResponsesStreamResponseType {
	types := make([]schemas.ResponsesStreamResponseType, 0, len(responses))
	for _, r := range responses {
		types = append(types, r.Type)
	}
	return types
}

func findCompletedWebSearchCallItem(responses []*schemas.BifrostResponsesStreamResponse) *schemas.ResponsesMessage {
	for _, r := range responses {
		if r.Type != schemas.ResponsesStreamResponseTypeOutputItemDone || r.Item == nil {
			continue
		}
		if r.Item.Type != nil && *r.Item.Type == schemas.ResponsesMessageTypeWebSearchCall {
			return r.Item
		}
	}
	return nil
}

func TestGeminiResponsesStreamStateFlushResetsWebSearchFlag(t *testing.T) {
	state := &GeminiResponsesStreamState{HasEmittedWebSearch: true}

	state.flush()

	assert.False(t, state.HasEmittedWebSearch,
		"flush must clear HasEmittedWebSearch so a recycled state can emit web search events again")
}

func TestGeminiResponsesStreamWebSearchEmittedAfterStateRecycle(t *testing.T) {
	state := &GeminiResponsesStreamState{}
	state.flush()

	first := driveGroundedResponsesStream(t, state)
	require.NotNil(t, findCompletedWebSearchCallItem(first),
		"fresh state must emit the web_search_call lifecycle")

	// Between two streaming requests the pool recycles the state through
	// releaseGeminiResponsesStreamState and acquireGeminiResponsesStreamState.
	// flush is the only reset either of them runs (twice in total across the
	// two calls, and it is idempotent), so a single flush here is equivalent
	// to that recycle path.
	state.flush()

	second := driveGroundedResponsesStream(t, state)

	assert.Equal(t, responsesStreamEventTypes(first), responsesStreamEventTypes(second),
		"a recycled state must produce the same grounded stream lifecycle as a fresh one")

	done := findCompletedWebSearchCallItem(second)
	require.NotNil(t, done,
		"recycled state must still emit the completed web_search_call item")
	require.NotNil(t, done.ResponsesToolMessage)
	require.NotNil(t, done.ResponsesToolMessage.Action)
	action := done.ResponsesToolMessage.Action.ResponsesWebSearchToolCallAction
	require.NotNil(t, action)
	assert.Equal(t, []string{"eiffel tower height"}, action.Queries)
	require.Len(t, action.Sources, 1)
	assert.Equal(t, "https://vertexaisearch.cloud.google.com/grounding-api-redirect/example", action.Sources[0].URL)
}
