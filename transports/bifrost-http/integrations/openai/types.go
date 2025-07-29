package openai

import (
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/core/schemas/api"
)

// ConvertToBifrostRequest converts an OpenAI chat request to Bifrost format
func ConvertChatRequestToBifrostRequest(r *api.OpenAIChatRequest) *schemas.BifrostRequest {
	provider, model := api.ParseModelString(r.Model, schemas.OpenAI)

	bifrostReq := &schemas.BifrostRequest{
		Provider: provider,
		Model:    model,
		Input: schemas.RequestInput{
			ChatCompletionInput: &r.Messages,
		},
	}

	// Map extra parameters and tool settings
	bifrostReq.Params = convertParameters(r)

	return bifrostReq
}

// ConvertToBifrostRequest converts an OpenAI speech request to Bifrost format
func ConvertSpeechRequestToBifrostRequest(r *api.OpenAISpeechRequest) *schemas.BifrostRequest {
	provider, model := api.ParseModelString(r.Model, schemas.OpenAI)

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

	// Map parameters
	bifrostReq.Params = convertSpeechParameters(r)

	return bifrostReq
}

// ConvertToBifrostRequest converts an OpenAI transcription request to Bifrost format
func ConvertTranscriptionRequestToBifrostRequest(r *api.OpenAITranscriptionRequest) *schemas.BifrostRequest {
	provider, model := api.ParseModelString(r.Model, schemas.OpenAI)

	// Create transcription input
	transcriptionInput := &schemas.TranscriptionInput{
		File: r.File,
	}

	// Set optional fields
	if r.Language != nil {
		transcriptionInput.Language = r.Language
	}
	if r.Prompt != nil {
		transcriptionInput.Prompt = r.Prompt
	}
	if r.ResponseFormat != nil {
		transcriptionInput.ResponseFormat = r.ResponseFormat
	}

	bifrostReq := &schemas.BifrostRequest{
		Provider: provider,
		Model:    model,
		Input: schemas.RequestInput{
			TranscriptionInput: transcriptionInput,
		},
	}

	// Map parameters
	bifrostReq.Params = convertTranscriptionParameters(r)

	return bifrostReq
}

// convertParameters converts OpenAI request parameters to Bifrost ModelParameters
// using direct field access for better performance and type safety.
func convertParameters(r *api.OpenAIChatRequest) *schemas.ModelParameters {
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
		params.ExtraParams["n"] = *r.N
	}
	if r.LogProbs != nil {
		params.ExtraParams["logprobs"] = *r.LogProbs
	}
	if r.TopLogProbs != nil {
		params.ExtraParams["top_logprobs"] = *r.TopLogProbs
	}
	if r.Stop != nil {
		params.ExtraParams["stop"] = r.Stop
	}
	if r.LogitBias != nil {
		params.ExtraParams["logit_bias"] = r.LogitBias
	}
	if r.User != nil {
		params.ExtraParams["user"] = *r.User
	}
	if r.Stream != nil {
		params.ExtraParams["stream"] = *r.Stream
	}
	if r.Seed != nil {
		params.ExtraParams["seed"] = *r.Seed
	}

	return params
}

// convertSpeechParameters converts OpenAI speech request parameters to Bifrost ModelParameters
func convertSpeechParameters(r *api.OpenAISpeechRequest) *schemas.ModelParameters {
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
func convertTranscriptionParameters(r *api.OpenAITranscriptionRequest) *schemas.ModelParameters {
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

// DeriveOpenAIFromBifrostResponse converts a Bifrost response to OpenAI format
func DeriveOpenAIFromBifrostResponse(bifrostResp *schemas.BifrostResponse) *api.OpenAIChatResponse {
	if bifrostResp == nil {
		return nil
	}

	openaiResp := &api.OpenAIChatResponse{
		ID:                bifrostResp.ID,
		Object:            bifrostResp.Object,
		Created:           bifrostResp.Created,
		Model:             bifrostResp.Model,
		Choices:           bifrostResp.Choices,
		Usage:             bifrostResp.Usage,
		ServiceTier:       bifrostResp.ServiceTier,
		SystemFingerprint: bifrostResp.SystemFingerprint,
	}

	return openaiResp
}

// DeriveOpenAISpeechFromBifrostResponse converts a Bifrost speech response to OpenAI format
func DeriveOpenAISpeechFromBifrostResponse(bifrostResp *schemas.BifrostResponse) *schemas.BifrostSpeech {
	if bifrostResp == nil || bifrostResp.Speech == nil {
		return nil
	}

	return bifrostResp.Speech
}

// DeriveOpenAITranscriptionFromBifrostResponse converts a Bifrost transcription response to OpenAI format
func DeriveOpenAITranscriptionFromBifrostResponse(bifrostResp *schemas.BifrostResponse) *schemas.BifrostTranscribe {
	if bifrostResp == nil || bifrostResp.Transcribe == nil {
		return nil
	}
	return bifrostResp.Transcribe
}

// DeriveOpenAIErrorFromBifrostError derives a OpenAIChatError from a BifrostError
func DeriveOpenAIErrorFromBifrostError(bifrostErr *schemas.BifrostError) *api.OpenAIChatError {
	if bifrostErr == nil {
		return nil
	}

	// Provide blank strings for nil pointer fields
	eventID := ""
	if bifrostErr.EventID != nil {
		eventID = *bifrostErr.EventID
	}

	errorType := ""
	if bifrostErr.Type != nil {
		errorType = *bifrostErr.Type
	}

	// Handle nested error fields with nil checks
	errorStruct := api.OpenAIChatErrorStruct{
		Type:    "",
		Code:    "",
		Message: bifrostErr.Error.Message,
		Param:   bifrostErr.Error.Param,
		EventID: eventID,
	}

	if bifrostErr.Error.Type != nil {
		errorStruct.Type = *bifrostErr.Error.Type
	}

	if bifrostErr.Error.Code != nil {
		errorStruct.Code = *bifrostErr.Error.Code
	}

	if bifrostErr.Error.EventID != nil {
		errorStruct.EventID = *bifrostErr.Error.EventID
	}

	return &api.OpenAIChatError{
		EventID: eventID,
		Type:    errorType,
		Error:   errorStruct,
	}
}

// DeriveOpenAIStreamFromBifrostError derives an OpenAI streaming error from a BifrostError
func DeriveOpenAIStreamFromBifrostError(bifrostErr *schemas.BifrostError) *api.OpenAIChatError {
	// For streaming, we use the same error format as regular OpenAI errors
	return DeriveOpenAIErrorFromBifrostError(bifrostErr)
}

// DeriveOpenAIStreamFromBifrostResponse converts a Bifrost response to OpenAI streaming format
func DeriveOpenAIStreamFromBifrostResponse(bifrostResp *schemas.BifrostResponse) *api.OpenAIStreamResponse {
	if bifrostResp == nil {
		return nil
	}

	streamResp := &api.OpenAIStreamResponse{
		ID:                bifrostResp.ID,
		Object:            "chat.completion.chunk",
		Created:           bifrostResp.Created,
		Model:             bifrostResp.Model,
		SystemFingerprint: bifrostResp.SystemFingerprint,
		Usage:             bifrostResp.Usage,
	}

	// Convert choices to streaming format
	for _, choice := range bifrostResp.Choices {
		streamChoice := api.OpenAIStreamChoice{
			Index:        choice.Index,
			FinishReason: choice.FinishReason,
		}

		var delta *api.OpenAIStreamDelta

		// Handle streaming vs non-streaming choices
		if choice.BifrostStreamResponseChoice != nil {
			// This is a streaming response - use the delta directly
			delta = &api.OpenAIStreamDelta{}

			// Only set fields that are not nil
			if choice.BifrostStreamResponseChoice.Delta.Role != nil {
				delta.Role = choice.BifrostStreamResponseChoice.Delta.Role
			}
			if choice.BifrostStreamResponseChoice.Delta.Content != nil {
				delta.Content = choice.BifrostStreamResponseChoice.Delta.Content
			}
			if len(choice.BifrostStreamResponseChoice.Delta.ToolCalls) > 0 {
				delta.ToolCalls = &choice.BifrostStreamResponseChoice.Delta.ToolCalls
			}
		} else if choice.BifrostNonStreamResponseChoice != nil {
			// This is a non-streaming response - convert message to delta format
			delta = &api.OpenAIStreamDelta{}

			// Convert role
			role := string(choice.BifrostNonStreamResponseChoice.Message.Role)
			delta.Role = &role

			// Convert content
			if choice.BifrostNonStreamResponseChoice.Message.Content.ContentStr != nil {
				delta.Content = choice.BifrostNonStreamResponseChoice.Message.Content.ContentStr
			}

			// Convert tool calls if present (from AssistantMessage)
			if choice.BifrostNonStreamResponseChoice.Message.AssistantMessage != nil &&
				choice.BifrostNonStreamResponseChoice.Message.AssistantMessage.ToolCalls != nil {
				delta.ToolCalls = choice.BifrostNonStreamResponseChoice.Message.AssistantMessage.ToolCalls
			}

			// Set LogProbs from non-streaming choice
			if choice.BifrostNonStreamResponseChoice.LogProbs != nil {
				streamChoice.LogProbs = choice.BifrostNonStreamResponseChoice.LogProbs
			}
		}

		// Ensure we have a valid delta with at least one field set
		// If all fields are nil, we should skip this chunk or set an empty content
		if delta != nil {
			hasValidField := (delta.Role != nil) || (delta.Content != nil) || (delta.ToolCalls != nil)
			if !hasValidField {
				// Set empty content to ensure we have at least one field
				emptyContent := ""
				delta.Content = &emptyContent
			}
			streamChoice.Delta = delta
		}

		streamResp.Choices = append(streamResp.Choices, streamChoice)
	}

	return streamResp
}
