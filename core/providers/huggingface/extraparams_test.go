package huggingface

import (
	"reflect"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToHuggingFaceEmbeddingRequest_DoesNotMutateCallerExtraParams(t *testing.T) {
	newExtra := func() map[string]interface{} {
		return map[string]interface{}{"normalize": true, "prompt_name": "query", "custom": "kept"}
	}
	text := "hello"
	req := &schemas.BifrostEmbeddingRequest{
		Model: string(hfInference) + "/sentence-transformers/all-MiniLM-L6-v2",
		Input: []schemas.EmbeddingInputItem{
			{Content: schemas.EmbeddingContent{{Type: schemas.EmbeddingContentPartTypeText, Text: &text}}},
		},
		Params: &schemas.EmbeddingParameters{ExtraParams: newExtra()},
	}

	out, err := ToHuggingFaceEmbeddingRequest(req)

	require.NoError(t, err)
	assert.True(t, reflect.DeepEqual(newExtra(), req.Params.ExtraParams), "caller's ExtraParams was modified: %v", req.Params.ExtraParams)
	require.NotNil(t, out.Normalize)
	assert.True(t, *out.Normalize)
	require.NotNil(t, out.PromptName)
	assert.Equal(t, "query", *out.PromptName)
	assert.Equal(t, "kept", out.ExtraParams["custom"])
}

func TestToHuggingFaceSpeechRequest_DoesNotMutateCallerExtraParams(t *testing.T) {
	newExtra := func() map[string]interface{} {
		return map[string]interface{}{"do_sample": true, "max_new_tokens": 64, "custom": "kept"}
	}
	req := &schemas.BifrostSpeechRequest{
		Model:  string(hfInference) + "/facebook/mms-tts-eng",
		Input:  &schemas.SpeechInput{Input: "hello"},
		Params: &schemas.SpeechParameters{ExtraParams: newExtra()},
	}

	out, err := ToHuggingFaceSpeechRequest(req)

	require.NoError(t, err)
	assert.True(t, reflect.DeepEqual(newExtra(), req.Params.ExtraParams), "caller's ExtraParams was modified: %v", req.Params.ExtraParams)
	require.NotNil(t, out.Parameters.GenerationParameters)
	require.NotNil(t, out.Parameters.GenerationParameters.DoSample)
	assert.True(t, *out.Parameters.GenerationParameters.DoSample)
	assert.Equal(t, "kept", out.ExtraParams["custom"])
}

func TestToHuggingFaceSpeechRequest_WithoutParamsDoesNotPanic(t *testing.T) {
	req := &schemas.BifrostSpeechRequest{
		Model: string(hfInference) + "/facebook/mms-tts-eng",
		Input: &schemas.SpeechInput{Input: "hello"},
	}

	out, err := ToHuggingFaceSpeechRequest(req)

	require.NoError(t, err)
	assert.Nil(t, out.ExtraParams)
}

func TestToHuggingFaceImageGenerationRequest_HFInferenceDoesNotMutateCallerExtraParams(t *testing.T) {
	newExtra := func() map[string]interface{} {
		return map[string]interface{}{"custom": "kept"}
	}
	req := &schemas.BifrostImageGenerationRequest{
		Model:  string(hfInference) + "/black-forest-labs/FLUX.1-dev",
		Input:  &schemas.ImageGenerationInput{Prompt: "a cat"},
		Params: &schemas.ImageGenerationParameters{ExtraParams: newExtra()},
	}

	out, err := ToHuggingFaceImageGenerationRequest(req)

	require.NoError(t, err)
	require.NotNil(t, out)
	assert.True(t, reflect.DeepEqual(newExtra(), req.Params.ExtraParams))
}
