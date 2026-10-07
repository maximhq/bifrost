package huggingface

import (
	"encoding/json"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// TestToHuggingFaceSpeechRequest_JSONNumberGenerationParams pins that float
// generation parameters arriving as json.Number (how the HTTP transport decodes
// extra params) are mapped, not left behind in ExtraParams.
func TestToHuggingFaceSpeechRequest_JSONNumberGenerationParams(t *testing.T) {
	req, err := ToHuggingFaceSpeechRequest(&schemas.BifrostSpeechRequest{
		Model: "hf-inference/facebook/mms-tts-eng",
		Input: &schemas.SpeechInput{Input: "hello"},
		Params: &schemas.SpeechParameters{ExtraParams: map[string]interface{}{
			"temperature": json.Number("0.7"),
			"top_p":       json.Number("0.9"),
		}},
	})
	require.NoError(t, err)
	gen := req.Parameters.GenerationParameters
	require.NotNil(t, gen)
	require.NotNil(t, gen.Temperature)
	require.Equal(t, 0.7, *gen.Temperature)
	require.NotNil(t, gen.TopP)
	require.Equal(t, 0.9, *gen.TopP)
	require.Empty(t, req.ExtraParams)
}
