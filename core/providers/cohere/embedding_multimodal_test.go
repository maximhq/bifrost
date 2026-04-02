package cohere

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func TestToCohereEmbeddingRequestTextOnlyUsesTexts(t *testing.T) {
	text := "hello"
	req, err := ToCohereEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
		Model: "embed-v4.0",
		Input: []schemas.EmbeddingContent{{{Type: schemas.EmbeddingContentPartTypeText, Text: &text}}},
	})

	require.NoError(t, err)
	require.NotNil(t, req)
	require.Equal(t, []string{"hello"}, req.Texts)
	require.Empty(t, req.Inputs)
}

func TestToCohereEmbeddingRequestMultimodalUsesInputs(t *testing.T) {
	caption := "caption"
	req, err := ToCohereEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
		Model: "embed-v4.0",
		Input: []schemas.EmbeddingContent{{
			{Type: schemas.EmbeddingContentPartTypeText, Text: &caption},
			{Type: schemas.EmbeddingContentPartTypeImage, Image: &schemas.EmbeddingMediaPart{URL: schemas.Ptr("https://example.com/cat.png")}},
		}},
	})

	require.NoError(t, err)
	require.NotNil(t, req)
	require.Len(t, req.Inputs, 1)
	require.Len(t, req.Inputs[0].Content, 2)
	require.Equal(t, CohereContentBlockTypeText, req.Inputs[0].Content[0].Type)
	require.Equal(t, CohereContentBlockTypeImage, req.Inputs[0].Content[1].Type)
}

func TestToCohereEmbeddingRequestRejectsUnsupportedModalities(t *testing.T) {
	_, err := ToCohereEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
		Model: "embed-v4.0",
		Input: []schemas.EmbeddingContent{{
			{Type: schemas.EmbeddingContentPartTypeAudio, Audio: &schemas.EmbeddingMediaPart{URL: schemas.Ptr("https://example.com/audio.mp3")}},
		}},
	})

	require.Error(t, err)
}
