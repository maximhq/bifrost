package cohere

import (
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

func TestToCohereEmbeddingRequestTextOnlyUsesTexts(t *testing.T) {
	text := "hello"
	req, err := ToCohereEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
		Model: "embed-v4.0",
		Input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &text}}}},
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
		Input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{
			{Type: schemas.EmbeddingContentPartTypeText, Text: &caption},
			{Type: schemas.EmbeddingContentPartTypeImage, Image: &schemas.EmbeddingMediaPart{URL: schemas.Ptr("https://example.com/cat.png")}},
		}},
		},
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
		Input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{
			{Type: schemas.EmbeddingContentPartTypeAudio, Audio: &schemas.EmbeddingMediaPart{URL: schemas.Ptr("https://example.com/audio.mp3")}},
		}},
		},
	})

	require.Error(t, err)
}

func TestToCohereEmbeddingRequestBuildsDataURIFromDataAndMIMEType(t *testing.T) {
	caption := "caption"
	image := schemas.EmbeddingMediaPart{Data: schemas.Ptr("iVBORw0KGgo="), MIMEType: schemas.Ptr("image/png")}

	lone, err := ToCohereEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
		Model: "embed-v4.0",
		Input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeImage, Image: &image}}}},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"data:image/png;base64,iVBORw0KGgo="}, lone.Images)

	mixed, err := ToCohereEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
		Model: "embed-v4.0",
		Input: []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{
			{Type: schemas.EmbeddingContentPartTypeText, Text: &caption},
			{Type: schemas.EmbeddingContentPartTypeImage, Image: &image},
		}}},
	})
	require.NoError(t, err)
	require.Equal(t, "data:image/png;base64,iVBORw0KGgo=", mixed.Inputs[0].Content[1].ImageURL.URL)
}

func TestToCohereEmbeddingRequestAllLoneImagesUseImages(t *testing.T) {
	a, b := "data:image/png;base64,AAAA", "data:image/jpeg;base64,BBBB"
	req, err := ToCohereEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
		Model: "embed-v4.0",
		Input: []schemas.EmbeddingInputItem{
			{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeImage, Image: &schemas.EmbeddingMediaPart{URL: &a}}}},
			{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeImage, Image: &schemas.EmbeddingMediaPart{URL: &b}}}},
		},
	})

	require.NoError(t, err)
	require.Equal(t, []string{a, b}, req.Images)
	require.Empty(t, req.Inputs)
}

func TestToCohereEmbeddingRequestMapsEncodingFormat(t *testing.T) {
	text := "hello"
	input := []schemas.EmbeddingInputItem{{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &text}}}}

	req, err := ToCohereEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
		Model:  "embed-v4.0",
		Input:  input,
		Params: &schemas.EmbeddingParameters{EncodingFormat: schemas.Ptr("base64")},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"base64"}, req.EmbeddingTypes)

	explicit, err := ToCohereEmbeddingRequest(&schemas.BifrostEmbeddingRequest{
		Model: "embed-v4.0",
		Input: input,
		Params: &schemas.EmbeddingParameters{
			EncodingFormat: schemas.Ptr("base64"),
			ExtraParams:    map[string]interface{}{"embedding_types": []string{"int8"}},
		},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"int8"}, explicit.EmbeddingTypes)
}

func TestCohereEmbeddingRequestRoundTripKeepsMaxTokensAndPriority(t *testing.T) {
	bifrostReq, err := (&CohereEmbeddingRequest{
		Model:     "embed-v4.0",
		InputType: "search_document",
		Texts:     []string{"hello"},
		MaxTokens: schemas.Ptr(64),
		Priority:  schemas.Ptr(5),
	}).ToBifrostEmbeddingRequest(nil)
	require.NoError(t, err)

	req, err := ToCohereEmbeddingRequest(bifrostReq)
	require.NoError(t, err)
	require.Equal(t, 64, *req.MaxTokens)
	require.Equal(t, 5, *req.Priority)
}

func TestCohereEmbeddingRequestUnsupportedBlockIsInvalidRequest(t *testing.T) {
	_, err := (&CohereEmbeddingRequest{
		Model:  "embed-v4.0",
		Inputs: []CohereEmbeddingInput{{Content: []CohereContentBlock{{Type: "audio"}}}},
	}).ToBifrostEmbeddingRequest(nil)

	badRequest, ok := providerUtils.AsBifrostBadRequestError(err)
	require.True(t, ok)
	require.Equal(t, `unsupported cohere embedding block type "audio"`, badRequest.Error.Message)
}
