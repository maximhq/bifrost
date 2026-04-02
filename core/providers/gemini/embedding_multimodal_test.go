package gemini

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func TestToGeminiEmbeddingRequestBatchContentUsesBatchRequest(t *testing.T) {
	one := "one"
	two := "two"
	req, err := ToGeminiEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
		Model: "gemini-embedding-001",
		Input: []schemas.EmbeddingContent{
			{{Type: schemas.EmbeddingContentPartTypeText, Text: &one}},
			{{Type: schemas.EmbeddingContentPartTypeText, Text: &two}},
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
	require.Len(t, bifrostReq.Input[0], 2)
	require.Equal(t, schemas.EmbeddingContentPartTypeText, bifrostReq.Input[0][0].Type)
	require.Equal(t, schemas.EmbeddingContentPartTypeImage, bifrostReq.Input[0][1].Type)
}
