package openai

import "github.com/maximhq/bifrost/core/schemas"

func filterParams(provider schemas.ModelProvider, p *schemas.ModelParameters) *schemas.ModelParameters {
	if p == nil {
		return nil
	}
	return schemas.ValidateAndFilterParamsForProvider(provider, p)
}

// convertParameters converts OpenAI request parameters to Bifrost ModelParameters
// using direct field access for better performance and type safety.
func (r *OpenAIChatRequest) convertParameters() *schemas.ModelParameters {
	params := &schemas.ModelParameters{
		ExtraParams: make(map[string]interface{}),
	}

	params.Tools = r.Tools
	params.ToolChoice = r.ToolChoice

	// Direct field mapping
	if r.MaxTokens != nil {
		params.MaxTokens = r.MaxTokens
	}
	if r.Temperature != nil {
		params.Temperature = r.Temperature
	}
	if r.TopP != nil {
		params.TopP = r.TopP
	}
	if r.PresencePenalty != nil {
		params.PresencePenalty = r.PresencePenalty
	}
	if r.FrequencyPenalty != nil {
		params.FrequencyPenalty = r.FrequencyPenalty
	}
	if r.N != nil {
		params.N = r.N
	}
	if r.LogProbs != nil {
		params.LogProbs = r.LogProbs
	}
	if r.TopLogProbs != nil {
		params.TopLogProbs = r.TopLogProbs
	}
	if r.Stop != nil {
		params.Stop = r.Stop
	}
	if r.LogitBias != nil {
		params.LogitBias = r.LogitBias
	}
	if r.User != nil {
		params.User = r.User
	}
	if r.Stream != nil {
		params.Stream = r.Stream
	}
	if r.Seed != nil {
		params.Seed = r.Seed
	}
	if r.StreamOptions != nil {
		params.StreamOptions = r.StreamOptions
	}
	if r.ResponseFormat != nil {
		params.ResponseFormat = r.ResponseFormat
	}
	if r.MaxCompletionTokens != nil {
		params.MaxCompletionTokens = r.MaxCompletionTokens
	}
	if r.ReasoningEffort != nil {
		params.ReasoningEffort = r.ReasoningEffort
	}

	return params
}

// convertSpeechParameters converts OpenAI speech request parameters to Bifrost ModelParameters
func (r *OpenAISpeechRequest) convertSpeechParameters() *schemas.ModelParameters {
	params := &schemas.ModelParameters{
		ExtraParams: make(map[string]interface{}),
	}

	// Add speech-specific parameters
	if r.Speed != nil {
		params.ExtraParams["speed"] = *r.Speed
	}

	return params
}

// convertTranscriptionParameters converts OpenAI transcription request parameters to Bifrost ModelParameters
func (r *OpenAITranscriptionRequest) convertTranscriptionParameters() *schemas.ModelParameters {
	params := &schemas.ModelParameters{
		ExtraParams: make(map[string]interface{}),
	}

	// Add transcription-specific parameters
	if r.Temperature != nil {
		params.ExtraParams["temperature"] = *r.Temperature
	}
	if len(r.TimestampGranularities) > 0 {
		params.ExtraParams["timestamp_granularities"] = r.TimestampGranularities
	}
	if len(r.Include) > 0 {
		params.ExtraParams["include"] = r.Include
	}

	return params
}

// convertEmbeddingParameters converts OpenAI embedding request parameters to Bifrost ModelParameters
func (r *OpenAIEmbeddingRequest) convertEmbeddingParameters() *schemas.ModelParameters {
	params := &schemas.ModelParameters{
		ExtraParams: make(map[string]interface{}),
	}

	// Add embedding-specific parameters
	if r.EncodingFormat != nil {
		params.EncodingFormat = r.EncodingFormat
	}
	if r.Dimensions != nil {
		params.Dimensions = r.Dimensions
	}
	if r.User != nil {
		params.User = r.User
	}

	return params
}
