package gemini

import "github.com/maximhq/bifrost/core/schemas"

func ToGeminiSpeechRequest(bifrostReq *schemas.BifrostSpeechRequest, responseModalities []string) *GeminiGenerationRequest {
	if bifrostReq == nil {
		return nil
	}

	// Create the base Gemini generation request
	geminiReq := &GeminiGenerationRequest{
		Model: bifrostReq.Model,
	}

	// Set response modalities for speech generation
	if len(responseModalities) > 0 {
		geminiReq.ResponseModalities = responseModalities
	}

	// Convert parameters to generation config
	if len(responseModalities) > 0 {
		var modalities []Modality
		for _, mod := range responseModalities {
			modalities = append(modalities, Modality(mod))
		}
		geminiReq.GenerationConfig.ResponseModalities = modalities
	}

	// Convert speech input to Gemini format
	if bifrostReq.Input.Input != "" {
		geminiReq.Contents = []CustomContent{
			{
				Parts: []*CustomPart{
					{
						Text: bifrostReq.Input.Input,
					},
				},
			},
		}

		// Add speech config to generation config if voice config is provided
		if bifrostReq.Params != nil && bifrostReq.Params.VoiceConfig != nil && bifrostReq.Params.VoiceConfig.Voice != nil {
			addSpeechConfigToGenerationConfig(&geminiReq.GenerationConfig, bifrostReq.Params.VoiceConfig)
		}
	}

	return geminiReq
}
