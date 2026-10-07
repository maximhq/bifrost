package huggingface

import (
	"encoding/json"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// TestToHuggingFaceTranscriptionRequest_JSONNumberGenerationParams pins that float
// generation parameters arriving as json.Number are mapped, not left behind in
// ExtraParams.
func TestToHuggingFaceTranscriptionRequest_JSONNumberGenerationParams(t *testing.T) {
	req, err := ToHuggingFaceTranscriptionRequest(&schemas.BifrostTranscriptionRequest{
		Model: "hf-inference/openai/whisper-large-v3",
		Input: &schemas.TranscriptionInput{File: []byte("audio")},
		Params: &schemas.TranscriptionParameters{ExtraParams: map[string]interface{}{
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
