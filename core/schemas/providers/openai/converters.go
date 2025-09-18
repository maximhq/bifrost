package openai

import (
	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
)

// ConvertToBifrostRequest converts an OpenAI chat request to Bifrost format
func (r *OpenAIChatRequest) ConvertToBifrostRequest() *schemas.BifrostRequest {
	provider, model := schemas.ParseModelString(r.Model, schemas.OpenAI)

	// Convert parameters first
	params := r.convertParameters()

	bifrostReq := &schemas.BifrostRequest{
		Provider: provider,
		Model:    model,
		Input: schemas.RequestInput{
			ChatCompletionInput: &r.Messages,
		},
		Params: filterParams(provider, params),
	}

	return bifrostReq
}

// ConvertSpeechRequestToBifrost converts an OpenAI speech request to Bifrost format
func (r *OpenAISpeechRequest) ConvertSpeechRequestToBifrost() *schemas.BifrostRequest {
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

// ConvertTranscriptionRequestToBifrost converts an OpenAI transcription request to Bifrost format
func (r *OpenAITranscriptionRequest) ConvertTranscriptionRequestToBifrost() *schemas.BifrostRequest {
	provider, model := schemas.ParseModelString(r.Model, schemas.OpenAI)

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

	// Convert parameters first
	params := r.convertTranscriptionParameters()

	// Map parameters
	bifrostReq.Params = filterParams(provider, params)

	return bifrostReq
}

// ConvertEmbeddingRequestToBifrost converts an OpenAI embedding request to Bifrost format
func (r *OpenAIEmbeddingRequest) ConvertEmbeddingRequestToBifrost() *schemas.BifrostRequest {
	provider, model := schemas.ParseModelString(r.Model, schemas.OpenAI)

	// Create embedding input
	embeddingInput := &schemas.EmbeddingInput{}

	// Cleaner coercion: marshal input and try to unmarshal into supported shapes
	if raw, err := sonic.Marshal(r.Input); err == nil {
		// 1) string
		var s string
		if err := sonic.Unmarshal(raw, &s); err == nil {
			embeddingInput.Text = &s
		} else {
			// 2) []string
			var ss []string
			if err := sonic.Unmarshal(raw, &ss); err == nil {
				embeddingInput.Texts = ss
			} else {
				// 3) []int
				var i []int
				if err := sonic.Unmarshal(raw, &i); err == nil {
					embeddingInput.Embedding = i
				} else {
					// 4) [][]int
					var ii [][]int
					if err := sonic.Unmarshal(raw, &ii); err == nil {
						embeddingInput.Embeddings = ii
					}
				}
			}
		}
	}

	bifrostReq := &schemas.BifrostRequest{
		Provider: provider,
		Model:    model,
		Input: schemas.RequestInput{
			EmbeddingInput: embeddingInput,
		},
	}

	// Convert parameters first
	params := r.convertEmbeddingParameters()

	// Map parameters
	bifrostReq.Params = filterParams(provider, params)

	return bifrostReq
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
	if r.StreamOptions != nil {
		params.ExtraParams["stream_options"] = r.StreamOptions
	}
	if r.ResponseFormat != nil {
		params.ExtraParams["response_format"] = r.ResponseFormat
	}
	if r.MaxCompletionTokens != nil {
		params.ExtraParams["max_completion_tokens"] = *r.MaxCompletionTokens
	}
	if r.ReasoningEffort != nil {
		params.ExtraParams["reasoning_effort"] = *r.ReasoningEffort
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

// ConvertChatResponseToOpenAI converts a Bifrost response to OpenAI format
func ConvertChatResponseToOpenAI(bifrostResp *schemas.BifrostResponse) *OpenAIChatResponse {
	if bifrostResp == nil {
		return nil
	}

	openaiResp := &OpenAIChatResponse{
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

// ConvertSpeechResponseToOpenAI converts a Bifrost speech response to OpenAI format
func ConvertSpeechResponseToOpenAI(bifrostResp *schemas.BifrostResponse) *schemas.BifrostSpeech {
	if bifrostResp == nil || bifrostResp.Speech == nil {
		return nil
	}

	return bifrostResp.Speech
}

// ConvertTranscriptionResponseToOpenAI converts a Bifrost transcription response to OpenAI format
func ConvertTranscriptionResponseToOpenAI(bifrostResp *schemas.BifrostResponse) *schemas.BifrostTranscribe {
	if bifrostResp == nil || bifrostResp.Transcribe == nil {
		return nil
	}
	return bifrostResp.Transcribe
}

// ConvertEmbeddingResponseToOpenAI converts a Bifrost embedding response to OpenAI format
func ConvertEmbeddingResponseToOpenAI(bifrostResp *schemas.BifrostResponse) *OpenAIEmbeddingResponse {
	if bifrostResp == nil || bifrostResp.Data == nil {
		return nil
	}

	return &OpenAIEmbeddingResponse{
		Object:            "list",
		Data:              bifrostResp.Data,
		Model:             bifrostResp.Model,
		Usage:             bifrostResp.Usage,
		ServiceTier:       bifrostResp.ServiceTier,
		SystemFingerprint: bifrostResp.SystemFingerprint,
	}
}

// ConvertErrorToOpenAI converts a BifrostError to OpenAIChatError
func ConvertErrorToOpenAI(bifrostErr *schemas.BifrostError) *OpenAIChatError {
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
	errorStruct := OpenAIChatErrorStruct{
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

	return &OpenAIChatError{
		EventID: eventID,
		Type:    errorType,
		Error:   errorStruct,
	}
}

// ConvertStreamErrorToOpenAI converts a BifrostError to OpenAI streaming error
func ConvertStreamErrorToOpenAI(bifrostErr *schemas.BifrostError) *OpenAIChatError {
	// For streaming, we use the same error format as regular OpenAI errors
	return ConvertErrorToOpenAI(bifrostErr)
}

// ConvertStreamResponseToOpenAI converts a Bifrost response to OpenAI streaming format
func ConvertStreamResponseToOpenAI(bifrostResp *schemas.BifrostResponse) *OpenAIStreamResponse {
	if bifrostResp == nil {
		return nil
	}

	streamResp := &OpenAIStreamResponse{
		ID:                bifrostResp.ID,
		Object:            "chat.completion.chunk",
		Created:           bifrostResp.Created,
		Model:             bifrostResp.Model,
		SystemFingerprint: bifrostResp.SystemFingerprint,
		Usage:             bifrostResp.Usage,
	}

	// Convert choices to streaming format
	for _, choice := range bifrostResp.Choices {
		streamChoice := OpenAIStreamChoice{
			Index:        choice.Index,
			FinishReason: choice.FinishReason,
		}

		var delta *OpenAIStreamDelta

		// Handle streaming vs non-streaming choices
		if choice.BifrostStreamResponseChoice != nil {
			// This is a streaming response - use the delta directly
			delta = &OpenAIStreamDelta{}

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
			delta = &OpenAIStreamDelta{}

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

// ConvertChatRequestToOpenAI converts a Bifrost chat completion request to OpenAI format
func ConvertChatRequestToOpenAI(bifrostReq *schemas.BifrostRequest) *OpenAIChatRequest {
	if bifrostReq == nil || bifrostReq.Input.ChatCompletionInput == nil {
		return nil
	}

	messages := *bifrostReq.Input.ChatCompletionInput
	params := bifrostReq.Params

	openaiReq := &OpenAIChatRequest{
		Model:    bifrostReq.Model,
		Messages: messages,
	}

	// Map parameters
	if params != nil {
		openaiReq.MaxTokens = params.MaxTokens
		openaiReq.Temperature = params.Temperature
		openaiReq.TopP = params.TopP
		openaiReq.PresencePenalty = params.PresencePenalty
		openaiReq.FrequencyPenalty = params.FrequencyPenalty
		openaiReq.Tools = params.Tools
		openaiReq.ToolChoice = params.ToolChoice

		// Map extra parameters
		if params.ExtraParams != nil {
			if n, ok := params.ExtraParams["n"].(int); ok {
				openaiReq.N = &n
			}
			if logprobs, ok := params.ExtraParams["logprobs"].(bool); ok {
				openaiReq.LogProbs = &logprobs
			}
			if topLogProbs, ok := params.ExtraParams["top_logprobs"].(int); ok {
				openaiReq.TopLogProbs = &topLogProbs
			}
			if stop := params.ExtraParams["stop"]; stop != nil {
				openaiReq.Stop = stop
			}
			if logitBias, ok := params.ExtraParams["logit_bias"].(map[string]float64); ok {
				openaiReq.LogitBias = logitBias
			}
			if user, ok := params.ExtraParams["user"].(string); ok {
				openaiReq.User = &user
			}
			if stream, ok := params.ExtraParams["stream"].(bool); ok {
				openaiReq.Stream = &stream
			}
			if seed, ok := params.ExtraParams["seed"].(int); ok {
				openaiReq.Seed = &seed
			}
			if streamOptions, ok := params.ExtraParams["stream_options"].(map[string]interface{}); ok {
				openaiReq.StreamOptions = &streamOptions
			}
			if responseFormat := params.ExtraParams["response_format"]; responseFormat != nil {
				openaiReq.ResponseFormat = responseFormat
			}
			if maxCompletionTokens, ok := params.ExtraParams["max_completion_tokens"].(int); ok {
				openaiReq.MaxCompletionTokens = &maxCompletionTokens
			}
			if reasoningEffort, ok := params.ExtraParams["reasoning_effort"].(string); ok {
				openaiReq.ReasoningEffort = &reasoningEffort
			}
		}
	}

	return openaiReq
}

// ConvertSpeechRequestToOpenAI converts a Bifrost speech request to OpenAI format
func ConvertSpeechRequestToOpenAI(bifrostReq *schemas.BifrostRequest) *OpenAISpeechRequest {
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

// ConvertTranscriptionRequestToOpenAI converts a Bifrost transcription request to OpenAI format
func ConvertTranscriptionRequestToOpenAI(bifrostReq *schemas.BifrostRequest) *OpenAITranscriptionRequest {
	if bifrostReq == nil || bifrostReq.Input.TranscriptionInput == nil {
		return nil
	}

	transcriptionInput := bifrostReq.Input.TranscriptionInput
	params := bifrostReq.Params

	openaiReq := &OpenAITranscriptionRequest{
		Model: bifrostReq.Model,
		File:  transcriptionInput.File,
	}

	// Set optional fields
	openaiReq.Language = transcriptionInput.Language
	openaiReq.Prompt = transcriptionInput.Prompt
	openaiReq.ResponseFormat = transcriptionInput.ResponseFormat

	// Map parameters
	if params != nil && params.ExtraParams != nil {
		if temperature, ok := params.ExtraParams["temperature"].(float64); ok {
			openaiReq.Temperature = &temperature
		}
		if include, ok := params.ExtraParams["include"].([]string); ok {
			openaiReq.Include = include
		}
		if timestampGranularities, ok := params.ExtraParams["timestamp_granularities"].([]string); ok {
			openaiReq.TimestampGranularities = timestampGranularities
		}
		if stream, ok := params.ExtraParams["stream"].(bool); ok {
			openaiReq.Stream = &stream
		}
	}

	return openaiReq
}

// ConvertEmbeddingRequestToOpenAI converts a Bifrost embedding request to OpenAI format
func ConvertEmbeddingRequestToOpenAI(bifrostReq *schemas.BifrostRequest) *OpenAIEmbeddingRequest {
	if bifrostReq == nil || bifrostReq.Input.EmbeddingInput == nil {
		return nil
	}

	embeddingInput := bifrostReq.Input.EmbeddingInput
	params := bifrostReq.Params

	openaiReq := &OpenAIEmbeddingRequest{
		Model: bifrostReq.Model,
	}

	// Set input - convert to interface{} for flexibility
	if len(embeddingInput.Texts) == 1 {
		openaiReq.Input = embeddingInput.Texts[0] // Single string
	} else {
		openaiReq.Input = embeddingInput.Texts // Array of strings
	}

	// Map parameters
	if params != nil {
		openaiReq.EncodingFormat = params.EncodingFormat
		openaiReq.Dimensions = params.Dimensions
		openaiReq.User = params.User
	}

	return openaiReq
}

func filterParams(provider schemas.ModelProvider, p *schemas.ModelParameters) *schemas.ModelParameters {
	if p == nil {
		return nil
	}
	return schemas.ValidateAndFilterParamsForProvider(provider, p)
}
