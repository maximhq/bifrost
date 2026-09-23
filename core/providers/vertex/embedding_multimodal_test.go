package vertex

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func TestToVertexGeminiEmbeddingRequest(t *testing.T) {
	text := "hello"
	req, err := ToVertexGeminiEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
		Input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{
			{Type: schemas.EmbeddingContentPartTypeText, Text: &text},
			{Type: schemas.EmbeddingContentPartTypeImage, Image: &schemas.EmbeddingMediaPart{URL: schemas.Ptr("https://example.com/img.png")}},
		}},
		},
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
		Input: []schemas.EmbeddingInputItem{
			{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &t1}}},
			{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &t2}}},
		},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "batch")
}

// :predict takes many instances, so the OpenAI batched form maps onto it directly.
func TestToVertexEmbeddingRequestBatch(t *testing.T) {
	t.Run("each input becomes its own instance", func(t *testing.T) {
		t1 := "Hello!"
		t2 := "World"
		req, err := ToVertexEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Input: []schemas.EmbeddingInputItem{
				{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &t1}}},
				{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &t2}}},
			},
			Params: &schemas.EmbeddingParameters{TaskType: schemas.Ptr("RETRIEVAL_DOCUMENT")},
		})
		require.NoError(t, err)
		require.Len(t, req.Instances, 2)
		require.Equal(t, "Hello!", req.Instances[0].Content)
		require.Equal(t, "World", req.Instances[1].Content)
		require.Equal(t, "RETRIEVAL_DOCUMENT", *req.Instances[1].TaskType)
	})

	t.Run("parts within one input still join into one instance", func(t *testing.T) {
		t1 := "first"
		t2 := "second"
		req, err := ToVertexEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{
				{Type: schemas.EmbeddingContentPartTypeText, Text: &t1},
				{Type: schemas.EmbeddingContentPartTypeText, Text: &t2},
			}}},
		})
		require.NoError(t, err)
		require.Len(t, req.Instances, 1)
		require.Equal(t, "first\nsecond", req.Instances[0].Content)
	})

	t.Run("a non-text part in any input is rejected", func(t *testing.T) {
		t1 := "fine"
		_, err := ToVertexEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
			Input: []schemas.EmbeddingInputItem{
				{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &t1}}},
				{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeImage, Image: &schemas.EmbeddingMediaPart{URL: schemas.Ptr("https://example.com/img.png")}}}},
			},
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "only supports text parts")
	})
}
