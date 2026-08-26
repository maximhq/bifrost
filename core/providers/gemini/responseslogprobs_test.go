package gemini

// Regression tests for issue #6231: the Gemini-native (Responses) route dropped
// logprobs on both sides. Request side: top_logprobs and the response_logprobs
// extra param were never mapped into GenerationConfig by
// convertParamsToGenerationConfigResponses (the chat route already mapped them).
// Response side: candidate logprobsResult was never preserved through the
// Gemini -> Bifrost -> Gemini conversions, non-streaming or streaming.

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// top_logprobs and the response_logprobs extra param (the exact shape
// convertGenerationConfigToResponsesParameters produces on the Gemini-native
// ingress path) must be mapped back into GenerationConfig by
// ToGeminiResponsesRequest -> convertParamsToGenerationConfigResponses.
func TestResponsesLogprobsRequestSide(t *testing.T) {
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.Gemini,
		Model:    "gemini-2.5-flash",
		Input: []schemas.ResponsesMessage{
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("hello"),
				},
			},
		},
		Params: &schemas.ResponsesParameters{
			TopLogProbs: schemas.Ptr(5),
			ExtraParams: map[string]interface{}{
				"response_logprobs": true,
			},
		},
	}

	geminiReq, err := ToGeminiResponsesRequest(nil, req)
	require.NoError(t, err)
	require.NotNil(t, geminiReq)

	assert.True(t, geminiReq.GenerationConfig.ResponseLogprobs,
		"response_logprobs extra param must map to generationConfig.responseLogprobs")
	require.NotNil(t, geminiReq.GenerationConfig.Logprobs,
		"top_logprobs must map to generationConfig.logprobs")
	assert.Equal(t, int32(5), *geminiReq.GenerationConfig.Logprobs)
}

// Gemini caps logprobs at 20; the Responses mapping must apply the same cap as
// the chat route.
func TestResponsesLogprobsRequestSideCap(t *testing.T) {
	req := &schemas.BifrostResponsesRequest{
		Provider: schemas.Gemini,
		Model:    "gemini-2.5-flash",
		Input: []schemas.ResponsesMessage{
			{
				Role: schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{
					ContentStr: schemas.Ptr("hello"),
				},
			},
		},
		Params: &schemas.ResponsesParameters{
			TopLogProbs: schemas.Ptr(50),
		},
	}

	geminiReq, err := ToGeminiResponsesRequest(nil, req)
	require.NoError(t, err)
	require.NotNil(t, geminiReq.GenerationConfig.Logprobs)
	assert.Equal(t, int32(20), *geminiReq.GenerationConfig.Logprobs)
	assert.True(t, geminiReq.GenerationConfig.ResponseLogprobs)
}

// candidates[0].logprobsResult must survive Gemini -> Bifrost Responses ->
// Gemini on the GenAI generateContent path.
func TestResponsesLogprobsResultRoundTrip(t *testing.T) {
	logprobs := &LogprobsResult{
		ChosenCandidates: []*LogprobsResultCandidate{
			{Token: "Hello", TokenID: 42, LogProbability: -0.05},
		},
		TopCandidates: []*LogprobsResultTopCandidates{
			{
				Candidates: []*LogprobsResultCandidate{
					{Token: "Hello", TokenID: 42, LogProbability: -0.05},
					{Token: "Hi", TokenID: 43, LogProbability: -3.1},
				},
			},
		},
	}

	geminiResp := &GenerateContentResponse{
		ResponseID:   "resp-6231",
		ModelVersion: "gemini-2.5-flash",
		Candidates: []*Candidate{
			{
				Index:          0,
				FinishReason:   FinishReasonStop,
				AvgLogprobs:    -0.05,
				LogprobsResult: logprobs,
				Content: &Content{
					Role:  "model",
					Parts: []*Part{{Text: "Hello"}},
				},
			},
		},
	}

	bifrostResp := geminiResp.ToResponsesBifrostResponsesResponse()
	require.NotNil(t, bifrostResp)

	out := ToGeminiResponsesResponse(bifrostResp)
	require.NotNil(t, out)
	require.Len(t, out.Candidates, 1)

	assert.Equal(t, -0.05, out.Candidates[0].AvgLogprobs)
	require.NotNil(t, out.Candidates[0].LogprobsResult,
		"logprobsResult must survive the Gemini -> Bifrost -> Gemini round trip")
	require.Len(t, out.Candidates[0].LogprobsResult.ChosenCandidates, 1)
	assert.Equal(t, "Hello", out.Candidates[0].LogprobsResult.ChosenCandidates[0].Token)
	require.Len(t, out.Candidates[0].LogprobsResult.TopCandidates, 1)
	assert.Len(t, out.Candidates[0].LogprobsResult.TopCandidates[0].Candidates, 2)
}

// Streaming flavor: a terminal streaming chunk carrying logprobsResult must have
// it preserved on the response.completed event's ProviderExtraFields (the
// mechanism used for safetyRatings/avgLogprobs), and the Gemini-native stream
// egress must restore it onto the terminal candidate.
func TestResponsesLogprobsResultStreaming(t *testing.T) {
	state := &GeminiResponsesStreamState{}
	state.flush()

	chunk := &GenerateContentResponse{
		ResponseID:   "resp-6231-stream",
		ModelVersion: "gemini-2.5-flash",
		Candidates: []*Candidate{
			{
				Index:        0,
				FinishReason: FinishReasonStop,
				AvgLogprobs:  -0.05,
				LogprobsResult: &LogprobsResult{
					ChosenCandidates: []*LogprobsResultCandidate{
						{Token: "Hello", TokenID: 42, LogProbability: -0.05},
					},
				},
				Content: &Content{
					Role:  "model",
					Parts: []*Part{{Text: "Hello"}},
				},
			},
		},
	}

	events, bErr := chunk.ToBifrostResponsesStream(0, state)
	require.Nil(t, bErr)

	var completed *schemas.BifrostResponsesStreamResponse
	for _, ev := range events {
		if ev.Type == schemas.ResponsesStreamResponseTypeCompleted {
			completed = ev
		}
	}
	require.NotNil(t, completed, "expected a response.completed event")
	require.NotNil(t, completed.Response)
	require.NotNil(t, completed.Response.ProviderExtraFields)

	assert.Equal(t, -0.05, completed.Response.ProviderExtraFields["avgLogprobs"])
	require.NotNil(t, completed.Response.ProviderExtraFields["logprobsResult"],
		"logprobsResult must be preserved for the streaming egress path")

	// Egress: the completed event converted back to a Gemini stream chunk must
	// carry logprobsResult on its terminal candidate.
	egressState := &BifrostToGeminiStreamState{}
	geminiChunk := ToGeminiResponsesStreamResponse(completed, egressState)
	require.NotNil(t, geminiChunk)
	require.NotEmpty(t, geminiChunk.Candidates)
	require.NotNil(t, geminiChunk.Candidates[0].LogprobsResult,
		"logprobsResult must be restored on the Gemini-native stream egress")
	require.Len(t, geminiChunk.Candidates[0].LogprobsResult.ChosenCandidates, 1)
	assert.Equal(t, "Hello", geminiChunk.Candidates[0].LogprobsResult.ChosenCandidates[0].Token)
}
