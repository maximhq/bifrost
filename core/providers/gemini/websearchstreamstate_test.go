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

// multiSourceGroundedStreamChunks mirrors groundedStreamChunks but cites one segment
// from two different sources, plus entries that must be skipped.
func multiSourceGroundedStreamChunks() []*GenerateContentResponse {
	return []*GenerateContentResponse{
		{
			ResponseID:   "resp-multi",
			ModelVersion: "gemini-2.5-flash",
			Candidates: []*Candidate{{
				Content: &Content{
					Role:  "model",
					Parts: []*Part{{Text: "Spain won Euro 2024"}},
				},
			}},
		},
		{
			ResponseID:   "resp-multi",
			ModelVersion: "gemini-2.5-flash",
			Candidates: []*Candidate{{
				FinishReason: FinishReasonStop,
				GroundingMetadata: &GroundingMetadata{
					WebSearchQueries: []string{"euro 2024 winner"},
					GroundingChunks: []*GroundingChunk{
						{Web: &GroundingChunkWeb{URI: "https://example.com/spain", Title: "uefa.com"}},
						{Web: &GroundingChunkWeb{URI: "https://example.com/final", Title: "wikipedia.org"}},
						{RetrievedContext: &GroundingChunkRetrievedContext{URI: "ctx://ignored"}}, // no Web — skipped
						{Web: &GroundingChunkWeb{URI: ""}},                                        // empty URI — skipped
					},
					GroundingSupports: []*GroundingSupport{{
						Segment:               &Segment{StartIndex: 0, EndIndex: 19, Text: "Spain won Euro 2024"},
						GroundingChunkIndices: []int32{0, 1, 2, 3, 99, -1}, // 2/3 unusable, 99 and -1 out of range
					}},
				},
			}},
			UsageMetadata: &GenerateContentResponseUsageMetadata{
				PromptTokenCount:     5,
				CandidatesTokenCount: 5,
				TotalTokenCount:      10,
			},
		},
	}
}

// TestGeminiResponsesStreamMultiSourceAnnotations asserts the streaming Responses path
// emits one annotation per (support, chunk) pair, with a distinct AnnotationIndex per
// event rather than a shared pointer into the loop counter.
func TestGeminiResponsesStreamMultiSourceAnnotations(t *testing.T) {
	state := &GeminiResponsesStreamState{}
	state.flush()

	var annotations []*schemas.BifrostResponsesStreamResponse
	seq := 0
	for _, chunk := range multiSourceGroundedStreamChunks() {
		responses, bifrostErr := chunk.ToBifrostResponsesStream(seq, state)
		require.Nil(t, bifrostErr, "unexpected conversion error")
		seq += len(responses)
		for _, r := range responses {
			if r.Type == schemas.ResponsesStreamResponseTypeOutputTextAnnotationAdded {
				annotations = append(annotations, r)
			}
		}
	}

	require.Len(t, annotations, 2, "both usable chunks should emit an annotation event")
	assert.Equal(t, "https://example.com/spain", *annotations[0].Annotation.URL)
	assert.Equal(t, "uefa.com", *annotations[0].Annotation.Title)
	assert.Equal(t, "https://example.com/final", *annotations[1].Annotation.URL)
	assert.Equal(t, "wikipedia.org", *annotations[1].Annotation.Title)

	// Each event must carry its own index; a shared pointer would read 2 for both.
	require.NotNil(t, annotations[0].AnnotationIndex)
	require.NotNil(t, annotations[1].AnnotationIndex)
	assert.Equal(t, 0, *annotations[0].AnnotationIndex)
	assert.Equal(t, 1, *annotations[1].AnnotationIndex)
}

// TestGeminiGroundedStreamRoundTripKeepsSearchEntryPoint drives the genai passthrough
// loop — Gemini stream -> Bifrost Responses stream -> Gemini stream — and asserts the
// searchEntryPoint survives. It is rebuilt from the rendered-content output_item.done
// event, so that event must carry its item.
func TestGeminiGroundedStreamRoundTripKeepsSearchEntryPoint(t *testing.T) {
	forward := &GeminiResponsesStreamState{}
	forward.flush()
	reverse := NewBifrostToGeminiStreamState()

	var grounded []*GroundingMetadata
	seq := 0
	for _, chunk := range groundedStreamChunks() {
		events, bifrostErr := chunk.ToBifrostResponsesStream(seq, forward)
		require.Nil(t, bifrostErr, "unexpected forward conversion error")
		seq += len(events)

		for _, event := range events {
			out := ToGeminiResponsesStreamResponse(event, reverse)
			if out == nil {
				continue
			}
			for _, candidate := range out.Candidates {
				if candidate.GroundingMetadata != nil {
					grounded = append(grounded, candidate.GroundingMetadata)
				}
			}
		}
	}

	require.Len(t, grounded, 1, "grounding metadata must be rebuilt exactly once")
	metadata := grounded[0]

	require.NotNil(t, metadata.SearchEntryPoint,
		"searchEntryPoint must survive the round trip; Google's terms require rendering Search Suggestions")
	assert.Equal(t, "<div>Google Search Suggestions</div>", metadata.SearchEntryPoint.RenderedContent)

	// The rest of the grounding payload must still round-trip alongside it.
	assert.Equal(t, []string{"eiffel tower height"}, metadata.WebSearchQueries)
	require.Len(t, metadata.GroundingChunks, 1)
	assert.Equal(t, "https://vertexaisearch.cloud.google.com/grounding-api-redirect/example",
		metadata.GroundingChunks[0].Web.URI)
	require.Len(t, metadata.GroundingSupports, 1)
}
