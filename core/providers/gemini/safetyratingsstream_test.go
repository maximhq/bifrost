package gemini

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// safetyRatingsStreamChunks mirrors how Gemini/Vertex deliver a streamed answer: a text
// chunk followed by a final chunk carrying finishReason plus per-candidate safetyRatings
// and avgLogprobs, which only ever appear on the terminal chunk.
func safetyRatingsStreamChunks() []*GenerateContentResponse {
	return []*GenerateContentResponse{
		{
			ResponseID:   "resp-safety",
			ModelVersion: "gemini-2.5-flash",
			Candidates: []*Candidate{{
				Content: &Content{
					Role:  "model",
					Parts: []*Part{{Text: "hello there"}},
				},
			}},
		},
		{
			ResponseID:   "resp-safety",
			ModelVersion: "gemini-2.5-flash",
			Candidates: []*Candidate{{
				FinishReason: FinishReasonStop,
				AvgLogprobs:  -0.1234,
				SafetyRatings: []*SafetyRating{
					{Category: "HARM_CATEGORY_HARASSMENT", Probability: "NEGLIGIBLE"},
					{Category: "HARM_CATEGORY_DANGEROUS_CONTENT", Probability: "LOW"},
				},
			}},
			UsageMetadata: &GenerateContentResponseUsageMetadata{
				PromptTokenCount:     4,
				CandidatesTokenCount: 3,
				TotalTokenCount:      7,
			},
		},
	}
}

// TestGeminiSafetyRatingsAvgLogprobsResponseIDStreamRoundTrip drives the genai
// streamGenerateContent passthrough loop -- Gemini stream -> Bifrost Responses stream ->
// Gemini stream -- and asserts candidates[0].safetyRatings, avgLogprobs, and the native
// responseId survive, matching the non-streaming fix for
// https://github.com/maximhq/bifrost/issues/5843.
func TestGeminiSafetyRatingsAvgLogprobsResponseIDStreamRoundTrip(t *testing.T) {
	forward := &GeminiResponsesStreamState{}
	forward.flush()
	reverse := NewBifrostToGeminiStreamState()

	var restored *Candidate
	var restoredResponseID string
	seq := 0
	for _, chunk := range safetyRatingsStreamChunks() {
		events, bifrostErr := chunk.ToBifrostResponsesStream(seq, forward)
		require.Nil(t, bifrostErr, "unexpected forward conversion error")
		seq += len(events)

		for _, event := range events {
			out := ToGeminiResponsesStreamResponse(event, reverse)
			if out == nil {
				continue
			}
			if out.ResponseID != "" {
				restoredResponseID = out.ResponseID
			}
			for _, candidate := range out.Candidates {
				if len(candidate.SafetyRatings) > 0 || candidate.AvgLogprobs != 0 {
					restored = candidate
				}
			}
		}
	}

	require.NotNil(t, restored, "safetyRatings/avgLogprobs must be rebuilt on the response.completed chunk")
	assert.InDelta(t, -0.1234, restored.AvgLogprobs, 0.0001)
	require.Len(t, restored.SafetyRatings, 2)
	assert.Equal(t, "HARM_CATEGORY_HARASSMENT", restored.SafetyRatings[0].Category)
	assert.Equal(t, "NEGLIGIBLE", restored.SafetyRatings[0].Probability)
	assert.Equal(t, "HARM_CATEGORY_DANGEROUS_CONTENT", restored.SafetyRatings[1].Category)
	assert.Equal(t, "LOW", restored.SafetyRatings[1].Probability)

	assert.Equal(t, "resp-safety", restoredResponseID,
		"native responseId must be restored on the streamed response.completed chunk, not a synthesized internal ID")
}

// TestGeminiURLContextMetadataStreamRoundTrip pins the streamGenerateContent twin of
// TestGeminiURLContextMetadataSurvivesResponsesRoundTrip: urlContextMetadata arrives on a
// streamed candidate (here a non-terminal one, ahead of the finish chunk) and must be
// rebuilt on the GenAI stream egress, since Bifrost's Responses schema has no field for it.
func TestGeminiURLContextMetadataStreamRoundTrip(t *testing.T) {
	chunks := []*GenerateContentResponse{
		{
			ResponseID:   "resp-url",
			ModelVersion: "gemini-2.5-flash",
			Candidates: []*Candidate{{
				Content: &Content{Role: "model", Parts: []*Part{{Text: "The page is about cats."}}},
				URLContextMetadata: &URLContextMetadata{URLMetadata: []*URLMetadata{
					{RetrievedURL: "https://example.com/cats", URLRetrievalStatus: "URL_RETRIEVAL_STATUS_SUCCESS"},
				}},
			}},
		},
		{
			ResponseID:   "resp-url",
			ModelVersion: "gemini-2.5-flash",
			Candidates:   []*Candidate{{FinishReason: FinishReasonStop}},
		},
	}

	forward := &GeminiResponsesStreamState{}
	forward.flush()
	reverse := NewBifrostToGeminiStreamState()

	var restored *URLContextMetadata
	seq := 0
	for _, chunk := range chunks {
		events, bifrostErr := chunk.ToBifrostResponsesStream(seq, forward)
		require.Nil(t, bifrostErr, "unexpected forward conversion error")
		seq += len(events)
		for _, event := range events {
			out := ToGeminiResponsesStreamResponse(event, reverse)
			if out == nil {
				continue
			}
			for _, candidate := range out.Candidates {
				if candidate.URLContextMetadata != nil {
					restored = candidate.URLContextMetadata
				}
			}
		}
	}

	require.NotNil(t, restored, "urlContextMetadata must appear on a streamed GenAI chunk")
	require.Len(t, restored.URLMetadata, 1)
	assert.Equal(t, "https://example.com/cats", restored.URLMetadata[0].RetrievedURL)
	assert.Equal(t, "URL_RETRIEVAL_STATUS_SUCCESS", restored.URLMetadata[0].URLRetrievalStatus)

	// A pooled state is flushed and reused: the next stream, which fetched no URL, must not
	// inherit the previous stream's urlContextMetadata.
	forward.flush()
	reverse = NewBifrostToGeminiStreamState()
	seq = 0
	for _, chunk := range safetyRatingsStreamChunks() {
		events, bifrostErr := chunk.ToBifrostResponsesStream(seq, forward)
		require.Nil(t, bifrostErr, "unexpected forward conversion error")
		seq += len(events)
		for _, event := range events {
			out := ToGeminiResponsesStreamResponse(event, reverse)
			if out == nil {
				continue
			}
			for _, candidate := range out.Candidates {
				assert.Nil(t, candidate.URLContextMetadata, "urlContextMetadata leaked across a flushed stream state")
			}
		}
	}
}
