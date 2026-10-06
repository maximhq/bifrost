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
