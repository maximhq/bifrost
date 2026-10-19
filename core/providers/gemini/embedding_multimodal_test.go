package gemini

import (
	"fmt"
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestToGeminiEmbeddingRequestBatchContentUsesBatchRequest(t *testing.T) {
	one := "one"
	two := "two"
	req, err := ToGeminiEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
		Model: "gemini-embedding-001",
		Input: []schemas.EmbeddingInputItem{
			{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &one}}},
			{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &two}}},
		},
	})
	require.NoError(t, err)

	require.Len(t, req.Requests, 2)
	require.Equal(t, "one", req.Requests[0].Content.Parts[0].Text)
	require.Equal(t, "two", req.Requests[1].Content.Parts[0].Text)
}

func TestGeminiGenerationRequestToBifrostEmbeddingRequestPreservesMultimodalContent(t *testing.T) {
	request := &GeminiGenerationRequest{
		Model: "gemini/gemini-embedding-001",
		Requests: []GeminiEmbeddingRequest{
			{
				Content: &Content{
					Parts: []*Part{
						{Text: "hello"},
						{FileData: &FileData{FileURI: "https://example.com/img.png", MIMEType: "image/png"}},
					},
				},
			},
		},
	}

	bifrostReq := request.ToBifrostEmbeddingRequest(schemas.NewBifrostContext(nil, schemas.NoDeadline))
	require.NotNil(t, bifrostReq)
	require.NotNil(t, bifrostReq.Input)
	require.Len(t, bifrostReq.Input, 1)
	require.Len(t, bifrostReq.Input[0].Content, 2)
	require.Equal(t, schemas.EmbeddingContentPartTypeText, bifrostReq.Input[0].Content[0].Type)
	require.Equal(t, schemas.EmbeddingContentPartTypeImage, bifrostReq.Input[0].Content[1].Type)
}

// Issue #7812: a request-level `taskType` / `title` supplied as a Gemini-native
// extra param on /v1/embeddings must reach every entry of :batchEmbedContents,
// and must not leak to the top level of the batch body (Gemini rejects unknown
// top-level fields).
func TestToGeminiEmbeddingRequestRequestLevelTaskTypeExtraParamReachesEveryInput(t *testing.T) {
	texts := []string{
		"Biodegradable Hydraulic Ester Fluid 46",
		"stainless steel ball valves",
		"contract manufacturing of injection moulded parts",
	}
	input := make(schemas.EmbeddingInput, 0, len(texts))
	for i := range texts {
		input = append(input, schemas.EmbeddingInputItem{
			Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &texts[i]}},
		})
	}
	req, err := ToGeminiEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
		Model: "gemini-embedding-001",
		Input: input,
		Params: &schemas.EmbeddingParameters{
			ExtraParams: map[string]interface{}{
				"taskType": "RETRIEVAL_DOCUMENT",
				"title":    "Industrial catalogue",
			},
		},
	})
	require.NoError(t, err)
	require.Len(t, req.Requests, len(texts))

	for i, r := range req.Requests {
		require.NotNil(t, r.TaskType, "requests[%d].taskType was dropped", i)
		require.Equal(t, "RETRIEVAL_DOCUMENT", *r.TaskType, "requests[%d].taskType", i)
		require.NotNil(t, r.Title, "requests[%d].title was dropped", i)
		require.Equal(t, "Industrial catalogue", *r.Title, "requests[%d].title", i)
	}
	require.NotContains(t, req.ExtraParams, "taskType", "taskType must not leak to the top level of :batchEmbedContents")
	require.NotContains(t, req.ExtraParams, "title", "title must not leak to the top level of :batchEmbedContents")

	// Same invariant on the wire bytes, with extra-param passthrough enabled as the
	// HTTP transport does for x-bf-passthrough-extra-params.
	body, err := providerUtils.MarshalProviderRequest(req)
	require.NoError(t, err)
	body, err = providerUtils.MergeExtraParamsIntoJSON(body, req.GetExtraParams())
	require.NoError(t, err)
	for i := range texts {
		require.Equal(t, "RETRIEVAL_DOCUMENT", gjson.GetBytes(body, fmt.Sprintf("requests.%d.taskType", i)).String(), "wire requests[%d].taskType", i)
		require.Equal(t, "Industrial catalogue", gjson.GetBytes(body, fmt.Sprintf("requests.%d.title", i)).String(), "wire requests[%d].title", i)
	}
	require.False(t, gjson.GetBytes(body, "taskType").Exists(), "top-level taskType on the wire: %s", body)
	require.False(t, gjson.GetBytes(body, "title").Exists(), "top-level title on the wire: %s", body)
}

// The first-class snake_case parameters must fan out to every entry too.
func TestToGeminiEmbeddingRequestRequestLevelTaskTypeParamReachesEveryInput(t *testing.T) {
	one, two, three := "one", "two", "three"
	taskType, title := "RETRIEVAL_DOCUMENT", "Catalogue"
	req, err := ToGeminiEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
		Model: "gemini-embedding-001",
		Input: schemas.EmbeddingInput{
			{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &one}}},
			{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &two}}},
			{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &three}}},
		},
		Params: &schemas.EmbeddingParameters{TaskType: &taskType, Title: &title},
	})
	require.NoError(t, err)
	require.Len(t, req.Requests, 3)
	for i, r := range req.Requests {
		require.NotNil(t, r.TaskType, "requests[%d].taskType was dropped", i)
		require.Equal(t, taskType, *r.TaskType, "requests[%d].taskType", i)
		require.NotNil(t, r.Title, "requests[%d].title was dropped", i)
		require.Equal(t, title, *r.Title, "requests[%d].title", i)
	}
}

// GenAI SDKs repeat taskType/outputDimensionality on every batchEmbedContents entry.
// Identical params must land at request level so providers that reject per-item
// params (openai, vertex, bedrock, cohere) still accept the request.
func TestGeminiEmbeddingRequestsHoistUniformParamsToRequestLevel(t *testing.T) {
	dims := 8
	taskType := "RETRIEVAL_DOCUMENT"
	entry := func(text string) GeminiEmbeddingRequest {
		return GeminiEmbeddingRequest{
			Content:              &Content{Parts: []*Part{{Text: text}}},
			TaskType:             &taskType,
			OutputDimensionality: &dims,
		}
	}
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)

	batch, err := (&GeminiBatchEmbeddingRequest{
		Model:    "openai/text-embedding-3-small",
		Requests: []GeminiEmbeddingRequest{entry("one"), entry("two")},
	}).ToBifrostEmbeddingRequest(ctx)
	require.NoError(t, err)

	embedContent := entry("one")
	embedContent.Model = "vertex/text-embedding-005"
	single, err := embedContent.ToBifrostEmbeddingRequest(ctx)
	require.NoError(t, err)

	for _, req := range []*schemas.BifrostEmbeddingRequest{batch, single} {
		require.NoError(t, schemas.EmbeddingInput(req.Input).RejectPerItemParams())
		require.NotNil(t, req.Params)
		require.Equal(t, dims, *req.Params.Dimensions)
		require.Equal(t, taskType, *req.Params.TaskType)
	}
}

// Entries with genuinely different params stay per-item whatever the model string says: the
// converter runs before routing, so a bare or non-Gemini model may still be served by Gemini.
func TestGeminiBatchEmbeddingRequestKeepsDifferingParamsPerItem(t *testing.T) {
	query := "RETRIEVAL_QUERY"
	doc := "RETRIEVAL_DOCUMENT"
	for _, model := range []string{"gemini/gemini-embedding-001", "gemini-embedding-001", "openai/text-embedding-3-small"} {
		t.Run(model, func(t *testing.T) {
			req, err := (&GeminiBatchEmbeddingRequest{
				Model: model,
				Requests: []GeminiEmbeddingRequest{
					{Content: &Content{Parts: []*Part{{Text: "q"}}}, TaskType: &query},
					{Content: &Content{Parts: []*Part{{Text: "d"}}}, TaskType: &doc},
				},
			}).ToBifrostEmbeddingRequest(schemas.NewBifrostContext(nil, schemas.NoDeadline))
			require.NoError(t, err)

			require.Nil(t, req.Params)
			require.Equal(t, query, *req.Input[0].Params.TaskType)
			require.Equal(t, doc, *req.Input[1].Params.TaskType)
		})
	}
}

// documentOcr / audioTrackExtraction are only accepted inside embedContentConfig on each
// requests[] entry; a nested config must land there, never at the batch top level.
func TestToGeminiEmbeddingRequestPutsEmbedContentConfigOnEachEntry(t *testing.T) {
	text := "alpha"
	req, err := ToGeminiEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
		Model: "gemini-embedding-2",
		Input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &text}}}},
		Params: &schemas.EmbeddingParameters{ExtraParams: map[string]interface{}{
			"embedContentConfig": map[string]interface{}{"documentOcr": true, "audioTrackExtraction": true},
		}},
	})
	require.NoError(t, err)
	body, err := providerUtils.MarshalSorted(req)
	require.NoError(t, err)
	merged, err := providerUtils.MergeExtraParamsIntoJSON(body, req.ExtraParams)
	require.NoError(t, err)

	// Without passthrough only the struct is sent; with it the extras are merged on top.
	for _, wire := range [][]byte{body, merged} {
		require.True(t, gjson.GetBytes(wire, "requests.0.embedContentConfig.documentOcr").Bool(), string(wire))
		require.True(t, gjson.GetBytes(wire, "requests.0.embedContentConfig.audioTrackExtraction").Bool(), string(wire))
		require.False(t, gjson.GetBytes(wire, "embedContentConfig").Exists(), string(wire))
	}
}

// The GenAI integration accepts embedContentConfig on each entry and forwards it in place.
func TestGeminiBatchEmbeddingRequestReadsEmbedContentConfig(t *testing.T) {
	var batch GeminiBatchEmbeddingRequest
	require.NoError(t, schemas.Unmarshal([]byte(`{"requests":[{"content":{"parts":[{"text":"a"}]},"embedContentConfig":{"documentOcr":true}}]}`), &batch))
	batch.Model = "gemini/gemini-embedding-2"
	bifrostReq, err := batch.ToBifrostEmbeddingRequest(schemas.NewBifrostContext(nil, schemas.NoDeadline))
	require.NoError(t, err)

	out, err := ToGeminiEmbeddingRequest(bifrostReq)
	require.NoError(t, err)
	body, err := providerUtils.MarshalSorted(out)
	require.NoError(t, err)
	require.True(t, gjson.GetBytes(body, "requests.0.embedContentConfig.documentOcr").Bool(), string(body))
}

// Character counts and token counts are different observed billing units.
func TestEmbeddingCharacterMeasurements(t *testing.T) {
	cases := []struct {
		name   string
		wire   string
		chars  *int
		tokens *int
	}{
		{"empty vectors", `{"embeddings":[],"metadata":{"billableCharacterCount":17}}`, schemas.Ptr(17), nil},
		{"characters", `{"embeddings":[{"values":[0.1,0.2]}],"metadata":{"billableCharacterCount":17}}`, schemas.Ptr(17), nil},
		{"zero characters", `{"embeddings":[{"values":[0.1,0.2]}],"metadata":{"billableCharacterCount":0}}`, schemas.Ptr(0), nil},
		{"absent character field", `{"embeddings":[{"values":[0.1,0.2]}],"metadata":{}}`, nil, nil},
		{"null characters", `{"embeddings":[{"values":[0.1,0.2]}],"metadata":{"billableCharacterCount":null}}`, nil, nil},
		{"independent tokens and characters", `{"embeddings":[{"values":[0.1,0.2],"statistics":{"tokenCount":3}}],"metadata":{"billableCharacterCount":17}}`, schemas.Ptr(17), schemas.Ptr(3)},
		{"empty token statistics", `{"embeddings":[{"values":[0.1,0.2],"statistics":{}}],"metadata":{"billableCharacterCount":17}}`, schemas.Ptr(17), nil},
		{"explicit zero tokens", `{"embeddings":[{"values":[0.1,0.2],"statistics":{"tokenCount":0}}],"metadata":{"billableCharacterCount":17}}`, schemas.Ptr(17), schemas.Ptr(0)},
		{"partial batch statistics", `{"embeddings":[{"values":[0.1],"statistics":{"tokenCount":3}},{"values":[0.2]}],"metadata":{"billableCharacterCount":17}}`, schemas.Ptr(17), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var decoded GeminiEmbeddingResponse
			require.NoError(t, schemas.Unmarshal([]byte(tc.wire), &decoded))
			got := ToBifrostEmbeddingResponse(&decoded, "embedding-units")
			require.NotNil(t, got)
			if tc.name == "empty vectors" {
				require.Empty(t, got.Data)
			}
			body, err := schemas.Marshal(got)
			require.NoError(t, err)
			chars := gjson.GetBytes(body, "usage.input_characters")
			if tc.chars == nil {
				require.False(t, chars.Exists(), string(body))
			} else {
				require.Equal(t, gjson.Number, chars.Type, string(body))
				require.EqualValues(t, *tc.chars, chars.Int(), string(body))
			}
			input, output, total := got.Usage.TokenMeasurements()
			require.Nil(t, output, string(body))
			if tc.tokens == nil {
				require.Nil(t, input, string(body))
				require.Nil(t, total, string(body))
			} else {
				require.NotNil(t, input, string(body))
				require.Equal(t, *tc.tokens, *input)
				require.NotNil(t, total, string(body))
				require.Equal(t, *tc.tokens, *total)
			}
		})
	}
}

func BenchmarkEmbeddingCharacterMeasurements(b *testing.B) {
	for _, metadata := range []string{"", `,"metadata":{"billableCharacterCount":17}`} {
		b.Run(fmt.Sprintf("characters=%t", metadata != ""), func(b *testing.B) {
			var decoded GeminiEmbeddingResponse
			if err := schemas.Unmarshal([]byte(`{"embeddings":[{"values":[0.1,0.2],"statistics":{"tokenCount":3}}]`+metadata+`}`), &decoded); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				_ = ToBifrostEmbeddingResponse(&decoded, "embedding-units")
			}
		})
	}
}

func TestEmbeddingNativeResponseDoesNotInventCharacters(t *testing.T) {
	var original schemas.BifrostEmbeddingResponse
	require.NoError(t, schemas.Unmarshal([]byte(`{"data":[{"index":0,"embedding":[0.1]}],"usage":{"prompt_tokens":3,"total_tokens":3}}`), &original))
	converted := ToGeminiEmbeddingResponse(&original)
	wire, err := schemas.Marshal(converted)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(wire, "metadata.billableCharacterCount").Exists(), string(wire))
}

func TestEmbeddingNativeCharacterOnlyRoundTrip(t *testing.T) {
	var original schemas.BifrostEmbeddingResponse
	require.NoError(t, schemas.Unmarshal([]byte(`{"data":[{"index":0,"embedding":[0.1]}],"usage":{"input_characters":0}}`), &original))
	converted := ToGeminiEmbeddingResponse(&original)
	wire, err := schemas.Marshal(converted)
	require.NoError(t, err)
	count := gjson.GetBytes(wire, "metadata.billableCharacterCount")
	require.Equal(t, gjson.Number, count.Type, string(wire))
	require.Zero(t, count.Int(), string(wire))
	require.False(t, gjson.GetBytes(wire, "embeddings.0.statistics.tokenCount").Exists(), string(wire))
	single, err := schemas.Marshal(ToGeminiEmbedContentResponse(&original))
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(single, "embedding.statistics.tokenCount").Exists(), string(single))
}

func BenchmarkEmbeddingDecodeUnits(b *testing.B) {
	for _, metadata := range []string{"", `,"metadata":{"billableCharacterCount":17}`} {
		b.Run(fmt.Sprintf("characters=%t", metadata != ""), func(b *testing.B) {
			raw := []byte(`{"embeddings":[{"values":[0.1,0.2],"statistics":{"tokenCount":3}}]` + metadata + `}`)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				var decoded GeminiEmbeddingResponse
				if err := schemas.Unmarshal(raw, &decoded); err != nil {
					b.Fatal(err)
				}
				_ = ToBifrostEmbeddingResponse(&decoded, "embedding-units")
			}
		})
	}
}

func TestEmbeddingNativeEmptyCharacterReceipt(t *testing.T) {
	var original schemas.BifrostEmbeddingResponse
	require.NoError(t, schemas.Unmarshal([]byte(`{"data":[],"usage":{"input_characters":17}}`), &original))
	for _, converted := range []any{ToGeminiEmbeddingResponse(&original), ToGeminiEmbedContentResponse(&original)} {
		wire, err := schemas.Marshal(converted)
		require.NoError(t, err)
		require.EqualValues(t, 17, gjson.GetBytes(wire, "metadata.billableCharacterCount").Int(), string(wire))
		require.False(t, gjson.GetBytes(wire, "embedding.statistics.tokenCount").Exists(), string(wire))
	}
}
