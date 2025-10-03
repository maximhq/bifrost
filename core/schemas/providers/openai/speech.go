package openai

import "github.com/maximhq/bifrost/core/schemas"

// ToBifrostRequest converts an OpenAI speech request to Bifrost format
func (r *OpenAISpeechRequest) ToBifrostRequest() *schemas.BifrostRequest {
	provider, model := schemas.ParseModelString(r.Model, schemas.OpenAI)

	// Create speech input
	speechInput := &schemas.SpeechInput{
		Input: r.Input,
		VoiceConfig: schemas.SpeechVoiceInput{
			Voice: &r.Voice,
		},
	}

	// Set response format if provided
	if r.ResponseFormat != nil {
		speechInput.ResponseFormat = *r.ResponseFormat
	}

	// Set instructions if provided
	if r.Instructions != nil {
		speechInput.Instructions = *r.Instructions
	}

	bifrostReq := &schemas.BifrostRequest{
		Provider: provider,
		Model:    model,
		Input: schemas.RequestInput{
			SpeechInput: speechInput,
		},
	}

	// Convert parameters first
	params := r.convertSpeechParameters()

	// Map parameters
	bifrostReq.Params = filterParams(provider, params)

	return bifrostReq
}

// ToOpenAISpeechResponse converts a Bifrost speech response to OpenAI format
func ToOpenAISpeechResponse(bifrostResp *schemas.BifrostResponse) *schemas.BifrostSpeech {
	if bifrostResp == nil || bifrostResp.Speech == nil {
		return nil
	}

	return bifrostResp.Speech
}

// ToOpenAISpeechRequest converts a Bifrost speech request to OpenAI format
func ToOpenAISpeechRequest(bifrostReq *schemas.BifrostRequest) *OpenAISpeechRequest {
	if bifrostReq == nil || bifrostReq.Input.SpeechInput == nil {
		return nil
	}

	speechInput := bifrostReq.Input.SpeechInput
	params := bifrostReq.Params

	openaiReq := &OpenAISpeechRequest{
		Model: bifrostReq.Model,
		Input: speechInput.Input,
	}

	// Set voice
	if speechInput.VoiceConfig.Voice != nil {
		openaiReq.Voice = *speechInput.VoiceConfig.Voice
	}

	// Set optional fields
	if speechInput.ResponseFormat != "" {
		openaiReq.ResponseFormat = &speechInput.ResponseFormat
	}
	if speechInput.Instructions != "" {
		openaiReq.Instructions = &speechInput.Instructions
	}

	// Map parameters
	if params != nil && params.ExtraParams != nil {
		if speed, ok := params.ExtraParams["speed"].(float64); ok {
			openaiReq.Speed = &speed
		}
		if streamFormat, ok := params.ExtraParams["stream_format"].(string); ok {
			openaiReq.StreamFormat = &streamFormat
		}
	}

	return openaiReq
}
