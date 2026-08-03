package gemini

import (
	"encoding/json"
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

// TestGeminiGoogleSearchToolRoundTrip asserts the full googleSearch request surface —
// searchTypes, excludeDomains, timeRangeFilter and the toolConfig latLng that localizes
// results — survives genai -> Bifrost Responses -> Gemini.
func TestGeminiGoogleSearchToolRoundTrip(t *testing.T) {
	body := `{
	  "contents":[{"role":"user","parts":[{"text":"eiffel tower at night"}]}],
	  "tools":[{"googleSearch":{"searchTypes":{"imageSearch":{},"webSearch":{}},"excludeDomains":["spam.com"],
	             "timeRangeFilter":{"startTime":"2025-01-01T00:00:00Z","endTime":"2025-02-01T00:00:00Z"}}}],
	  "toolConfig":{"retrievalConfig":{"latLng":{"latitude":37.7749,"longitude":-122.4194}}},
	  "model":"gemini-3.1-flash-image"
	}`

	var request GeminiGenerationRequest
	require.NoError(t, json.Unmarshal([]byte(body), &request))

	ctx := &schemas.BifrostContext{}
	bifrostReq := request.ToBifrostResponsesRequest(ctx)

	// The neutral hop must stay on OpenAI's search_content_types enum: Gemini's
	// webSearch returns text results, so it is carried as "text", never "web".
	require.Len(t, bifrostReq.Params.Tools, 1)
	assert.Equal(t, []string{"text", "image"}, bifrostReq.Params.Tools[0].ResponsesToolWebSearch.SearchContentTypes)

	out, err := ToGeminiResponsesRequest(ctx, bifrostReq)
	require.NoError(t, err)
	require.Len(t, out.Tools, 1)

	googleSearch := out.Tools[0].GoogleSearch
	require.NotNil(t, googleSearch)
	require.NotNil(t, googleSearch.SearchTypes, "searchTypes must survive")
	assert.NotNil(t, googleSearch.SearchTypes.WebSearch)
	assert.NotNil(t, googleSearch.SearchTypes.ImageSearch)
	assert.Equal(t, []string{"spam.com"}, googleSearch.ExcludeDomains)
	require.NotNil(t, googleSearch.TimeRangeFilter)

	require.NotNil(t, out.ToolConfig)
	require.NotNil(t, out.ToolConfig.RetrievalConfig)
	require.NotNil(t, out.ToolConfig.RetrievalConfig.LatLng, "latLng must survive")
	assert.Equal(t, 37.7749, *out.ToolConfig.RetrievalConfig.LatLng.Latitude)
	assert.Equal(t, -122.4194, *out.ToolConfig.RetrievalConfig.LatLng.Longitude)
}

// TestGeminiGoogleSearchToolRoundTripSnakeCase covers the snake_case aliases, and asserts
// an unselected search type is not synthesized on the way out.
func TestGeminiGoogleSearchToolRoundTripSnakeCase(t *testing.T) {
	body := `{
	  "contents":[{"role":"user","parts":[{"text":"x"}]}],
	  "tools":[{"google_search":{"search_types":{"image_search":{}}}}],
	  "toolConfig":{"retrieval_config":{"latLng":{"latitude":1.5,"longitude":2.5}}},
	  "model":"gemini-3.1-flash-image"
	}`

	var request GeminiGenerationRequest
	require.NoError(t, json.Unmarshal([]byte(body), &request))

	ctx := &schemas.BifrostContext{}
	out, err := ToGeminiResponsesRequest(ctx, request.ToBifrostResponsesRequest(ctx))
	require.NoError(t, err)
	require.Len(t, out.Tools, 1)

	searchTypes := out.Tools[0].GoogleSearch.SearchTypes
	require.NotNil(t, searchTypes)
	assert.NotNil(t, searchTypes.ImageSearch)
	assert.Nil(t, searchTypes.WebSearch, "web search must not be synthesized when unselected")

	require.NotNil(t, out.ToolConfig)
	require.NotNil(t, out.ToolConfig.RetrievalConfig)
	require.NotNil(t, out.ToolConfig.RetrievalConfig.LatLng)
}

// TestGeminiImageGroundingRoundTrip asserts image-search grounding survives the genai
// round trip: the image chunk keeps its asset URL and domain alongside the page it
// attributes to, and imageSearchQueries stay separate from webSearchQueries.
func TestGeminiImageGroundingRoundTrip(t *testing.T) {
	body := `{
	  "candidates":[{
	    "content":{"role":"model","parts":[{"text":"The Eiffel Tower is lit nightly."}]},
	    "finishReason":"STOP",
	    "groundingMetadata":{
	      "webSearchQueries":["eiffel tower lighting"],
	      "imageSearchQueries":["eiffel tower at night"],
	      "searchEntryPoint":{"renderedContent":"<div>chip</div>"},
	      "groundingChunks":[
	        {"web":{"uri":"https://en.wikipedia.org/wiki/Eiffel_Tower","title":"wikipedia.org"}},
	        {"image":{"sourceUri":"https://example.com/gallery","imageUri":"https://example.com/tower.jpg","title":"Tower gallery","domain":"example.com"}}
	      ],
	      "groundingSupports":[
	        {"segment":{"startIndex":0,"endIndex":32,"text":"The Eiffel Tower is lit nightly."},"groundingChunkIndices":[0,1]}
	      ]
	    }
	  }],
	  "usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":8,"totalTokenCount":13},
	  "modelVersion":"gemini-3.1-flash-image"
	}`

	var response GenerateContentResponse
	require.NoError(t, json.Unmarshal([]byte(body), &response))

	got := ToGeminiResponsesResponse(response.ToResponsesBifrostResponsesResponse()).Candidates[0].GroundingMetadata
	require.NotNil(t, got)

	assert.Equal(t, []string{"eiffel tower lighting"}, got.WebSearchQueries)
	assert.Equal(t, []string{"eiffel tower at night"}, got.ImageSearchQueries)

	require.Len(t, got.GroundingChunks, 2)
	require.NotNil(t, got.GroundingChunks[0].Web)
	assert.Equal(t, "https://en.wikipedia.org/wiki/Eiffel_Tower", got.GroundingChunks[0].Web.URI)

	image := got.GroundingChunks[1].Image
	require.NotNil(t, image, "image chunk must survive as an image chunk, not collapse to web")
	assert.Equal(t, "https://example.com/gallery", image.SourceURI)
	assert.Equal(t, "https://example.com/tower.jpg", image.ImageURI)
	assert.Equal(t, "Tower gallery", image.Title)
	assert.Equal(t, "example.com", image.Domain)

	require.Len(t, got.GroundingSupports, 1)
	assert.Equal(t, []int32{0, 1}, got.GroundingSupports[0].GroundingChunkIndices,
		"a segment citing both a web and an image chunk must keep both")
}

// multiSourceGroundedResponse mirrors a real gemini-3.6-flash grounded answer: four
// supports over four chunks, three of them citing two sources at once.
func multiSourceGroundedResponse() *GenerateContentResponse {
	return &GenerateContentResponse{
		ResponseID:   "lqtpavmlNfrOjuMPrIHtkQo",
		ModelVersion: "gemini-3.6-flash",
		Candidates: []*Candidate{{
			FinishReason: FinishReasonStop,
			Content: &Content{
				Role: "model",
				Parts: []*Part{{Text: "**Spain** won UEFA Euro 2024. \n\nThey defeated **England 2-1** in the final on July 14, 2024, at the Olympiastadion in Berlin. Nico Williams scored first for Spain, followed by an equalizer from England's Cole Palmer, before Mikel Oyarzabal scored the winning goal in the 86th minute. \n\nWith this victory, Spain secured a record-breaking fourth UEFA European Championship title (having previously won in 1964, 2008, and 2012)."}},
			},
			GroundingMetadata: &GroundingMetadata{
				WebSearchQueries: []string{"Euro 2024 winner"},
				SearchEntryPoint: &SearchEntryPoint{RenderedContent: "<div>Euro 2024 winner</div>"},
				GroundingChunks: []*GroundingChunk{
					{Web: &GroundingChunkWeb{URI: "https://vertexaisearch.cloud.google.com/grounding-api-redirect/a", Title: "youtube.com"}},
					{Web: &GroundingChunkWeb{URI: "https://vertexaisearch.cloud.google.com/grounding-api-redirect/b", Title: "youtube.com"}},
					{Web: &GroundingChunkWeb{URI: "https://vertexaisearch.cloud.google.com/grounding-api-redirect/c", Title: "youtube.com"}},
					{Web: &GroundingChunkWeb{URI: "https://vertexaisearch.cloud.google.com/grounding-api-redirect/d", Title: "wikipedia.org"}},
				},
				GroundingSupports: []*GroundingSupport{
					{Segment: &Segment{EndIndex: 28, Text: "**Spain** won UEFA Euro 2024"}, GroundingChunkIndices: []int32{0}},
					{Segment: &Segment{StartIndex: 32, EndIndex: 126, Text: "They defeated **England 2-1** in the final on July 14, 2024, at the Olympiastadion in Berlin"}, GroundingChunkIndices: []int32{0, 1}},
					{Segment: &Segment{StartIndex: 128, EndIndex: 284, Text: "Nico Williams scored first for Spain, followed by an equalizer from England's Cole Palmer, before Mikel Oyarzabal scored the winning goal in the 86th minute"}, GroundingChunkIndices: []int32{0, 2}},
					{Segment: &Segment{StartIndex: 288, EndIndex: 426, Text: "With this victory, Spain secured a record-breaking fourth UEFA European Championship title (having previously won in 1964, 2008, and 2012)"}, GroundingChunkIndices: []int32{0, 3}},
				},
			},
		}},
		UsageMetadata: &GenerateContentResponseUsageMetadata{
			PromptTokenCount:     204,
			CandidatesTokenCount: 169,
			TotalTokenCount:      671,
		},
	}
}

// TestGeminiGroundedRoundTripMergesMultiSourceSupports drives the genai passthrough loop
// for a non-streaming grounded answer. Annotations carry one entry per (support, chunk)
// pair, so rebuilding must regroup them by segment — otherwise the four supports here
// come back as seven, each citing a single source with a duplicated segment.
func TestGeminiGroundedRoundTripMergesMultiSourceSupports(t *testing.T) {
	response := multiSourceGroundedResponse()
	want := response.Candidates[0].GroundingMetadata

	got := ToGeminiResponsesResponse(response.ToResponsesBifrostResponsesResponse()).Candidates[0].GroundingMetadata
	require.NotNil(t, got, "grounding metadata must survive the round trip")

	require.Len(t, got.GroundingSupports, len(want.GroundingSupports),
		"supports must regroup by segment, not fan out per cited chunk")
	for i, wantSupport := range want.GroundingSupports {
		gotSupport := got.GroundingSupports[i]
		assert.Equal(t, wantSupport.GroundingChunkIndices, gotSupport.GroundingChunkIndices, "support %d indices", i)
		assert.Equal(t, wantSupport.Segment.StartIndex, gotSupport.Segment.StartIndex, "support %d start", i)
		assert.Equal(t, wantSupport.Segment.EndIndex, gotSupport.Segment.EndIndex, "support %d end", i)
		assert.Equal(t, wantSupport.Segment.Text, gotSupport.Segment.Text, "support %d text", i)
	}

	require.Len(t, got.GroundingChunks, len(want.GroundingChunks))
	for i, wantChunk := range want.GroundingChunks {
		assert.Equal(t, wantChunk.Web.URI, got.GroundingChunks[i].Web.URI, "chunk %d uri", i)
	}
	assert.Equal(t, want.WebSearchQueries, got.WebSearchQueries)
	require.NotNil(t, got.SearchEntryPoint)
	assert.Equal(t, want.SearchEntryPoint.RenderedContent, got.SearchEntryPoint.RenderedContent)
}

// TestGeminiGroundedStreamRoundTripMergesMultiSourceSupports is the streaming counterpart:
// the reverse stream converter rebuilds grounding from the same buffered annotations.
func TestGeminiGroundedStreamRoundTripMergesMultiSourceSupports(t *testing.T) {
	source := multiSourceGroundedResponse()
	want := source.Candidates[0].GroundingMetadata

	// Split into the shape Gemini streams: text first, grounding on the finish event.
	text := &GenerateContentResponse{
		ResponseID:   source.ResponseID,
		ModelVersion: source.ModelVersion,
		Candidates:   []*Candidate{{Content: source.Candidates[0].Content}},
	}
	finish := &GenerateContentResponse{
		ResponseID:   source.ResponseID,
		ModelVersion: source.ModelVersion,
		Candidates: []*Candidate{{
			FinishReason:      FinishReasonStop,
			GroundingMetadata: want,
		}},
		UsageMetadata: source.UsageMetadata,
	}

	forward := &GeminiResponsesStreamState{}
	forward.flush()
	reverse := NewBifrostToGeminiStreamState()

	var grounded []*GroundingMetadata
	seq := 0
	for _, chunk := range []*GenerateContentResponse{text, finish} {
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
	require.Len(t, grounded[0].GroundingSupports, len(want.GroundingSupports),
		"supports must regroup by segment, not fan out per cited chunk")
	for i, wantSupport := range want.GroundingSupports {
		assert.Equal(t, wantSupport.GroundingChunkIndices, grounded[0].GroundingSupports[i].GroundingChunkIndices,
			"support %d indices", i)
	}
}
