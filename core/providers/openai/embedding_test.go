package openai_test

import (
	"testing"

	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// The OpenAI converter is shared by OpenAI-compatible providers, so its errors must not name openai.
func TestToOpenAIEmbeddingRequestErrorsDoNotNameOpenAI(t *testing.T) {
	text := "hello"
	dims := 8
	cases := map[string][]schemas.EmbeddingInputItem{
		"image input": {
			{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeImage, Image: &schemas.EmbeddingMediaPart{URL: schemas.Ptr("https://example.com/a.png")}}}},
		},
		"mixed text and tokens": {
			{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &text}, {Type: schemas.EmbeddingContentPartTypeTokens, Tokens: []int{1, 2}}}},
		},
		"per-item params": {
			{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &text}}, Params: &schemas.EmbeddingParameters{Dimensions: &dims}},
		},
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := openai.ToOpenAIEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
				Provider: schemas.Azure,
				Model:    "text-embedding-3-small",
				Input:    input,
			}, schemas.Azure)
			require.Error(t, err)
			require.Contains(t, err.Error(), "this provider")
			require.NotContains(t, err.Error(), "openai")
		})
	}
}
