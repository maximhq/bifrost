package vertex

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func TestToVertexGeminiEmbeddingRequest(t *testing.T) {
	text := "hello"
	req, err := ToVertexGeminiEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
		Input: []schemas.EmbeddingContent{{
			{Type: schemas.EmbeddingContentPartTypeText, Text: &text},
			{Type: schemas.EmbeddingContentPartTypeImage, Image: &schemas.EmbeddingMediaPart{URL: schemas.Ptr("https://example.com/img.png")}},
		}},
		Params: &schemas.EmbeddingParameters{
			TaskType:   schemas.Ptr("RETRIEVAL_DOCUMENT"),
			Dimensions: schemas.Ptr(128),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, req.Content)
	require.Len(t, req.Content.Parts, 2)
	require.Equal(t, "hello", req.Content.Parts[0].Text)
	require.NotNil(t, req.Content.Parts[1].FileData)
	require.Equal(t, 128, *req.OutputDimensionality)
}

func TestToVertexGeminiEmbeddingRequestRejectsBatch(t *testing.T) {
	t1 := "first"
	t2 := "second"
	_, err := ToVertexGeminiEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
		Input: []schemas.EmbeddingContent{
			{{Type: schemas.EmbeddingContentPartTypeText, Text: &t1}},
			{{Type: schemas.EmbeddingContentPartTypeText, Text: &t2}},
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "batch")
}
