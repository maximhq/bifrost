package openai

import (
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/integrations"
)

// OpenAIChatRequest represents an OpenAI chat completion request
type OpenAIChatRequest struct {
	Model            string                   `json:"model"`
	Messages         []schemas.BifrostMessage `json:"messages"`
	MaxTokens        *int                     `json:"max_tokens,omitempty"`
	Temperature      *float64                 `json:"temperature,omitempty"`
	TopP             *float64                 `json:"top_p,omitempty"`
	N                *int                     `json:"n,omitempty"`
	Stop             interface{}              `json:"stop,omitempty"`
	PresencePenalty  *float64                 `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64                 `json:"frequency_penalty,omitempty"`
	LogitBias        map[string]float64       `json:"logit_bias,omitempty"`
	User             *string                  `json:"user,omitempty"`
	Tools            *[]schemas.Tool          `json:"tools,omitempty"` // Reuse schema type
	ToolChoice       *schemas.ToolChoice      `json:"tool_choice,omitempty"`
	Stream           *bool                    `json:"stream,omitempty"`
	LogProbs         *bool                    `json:"logprobs,omitempty"`
	TopLogProbs      *int                     `json:"top_logprobs,omitempty"`
	ResponseFormat   interface{}              `json:"response_format,omitempty"`
	Seed             *int                     `json:"seed,omitempty"`
}

// OpenAISpeechRequest represents an OpenAI speech synthesis request
type OpenAISpeechRequest struct {
	Model          string   `json:"model"`
	Input          string   `json:"input"`
	Voice          string   `json:"voice"`
	ResponseFormat *string  `json:"response_format,omitempty"`
	Speed          *float64 `json:"speed,omitempty"`
	Instructions   *string  `json:"instructions,omitempty"`
	StreamFormat   *string  `json:"stream_format,omitempty"`
}

// OpenAITranscriptionRequest represents an OpenAI transcription request
// Note: This is used for JSON body parsing, actual form parsing is handled in the router
type OpenAITranscriptionRequest struct {
	Model                  string   `json:"model"`
	File                   []byte   `json:"file"` // Binary audio data
	Language               *string  `json:"language,omitempty"`
	Prompt                 *string  `json:"prompt,omitempty"`
	ResponseFormat         *string  `json:"response_format,omitempty"`
	Temperature            *float64 `json:"temperature,omitempty"`
	Include                []string `json:"include,omitempty"`
	TimestampGranularities []string `json:"timestamp_granularities,omitempty"`
	Stream                 *bool    `json:"stream,omitempty"`
}

// OpenAIEmbeddingRequest represents an OpenAI embedding request
type OpenAIEmbeddingRequest struct {
	Model          string      `json:"model"`
	Input          interface{} `json:"input"` // Can be string or []string
	EncodingFormat *string     `json:"encoding_format,omitempty"`
	Dimensions     *int        `json:"dimensions,omitempty"`
	User           *string     `json:"user,omitempty"`
}

// IsStreamingRequested implements the StreamingRequest interface
func (r *OpenAIChatRequest) IsStreamingRequested() bool {
	return r.Stream != nil && *r.Stream
}

// IsStreamingRequested implements the StreamingRequest interface for speech
func (r *OpenAISpeechRequest) IsStreamingRequested() bool {
	return r.StreamFormat != nil && *r.StreamFormat == "sse"
}

// IsStreamingRequested implements the StreamingRequest interface for transcription
func (r *OpenAITranscriptionRequest) IsStreamingRequested() bool {
	return r.Stream != nil && *r.Stream
}

// IsStreamingRequested implements the StreamingRequest interface for embeddings
// Note: Embeddings don't support streaming in OpenAI API
func (r *OpenAIEmbeddingRequest) IsStreamingRequested() bool {
	return false
}

// OpenAIChatResponse represents an OpenAI chat completion response
type OpenAIChatResponse struct {
	ID                string                          `json:"id"`
	Object            string                          `json:"object"`
	Created           int                             `json:"created"`
	Model             string                          `json:"model"`
	Choices           []schemas.BifrostResponseChoice `json:"choices"`
	Usage             *schemas.LLMUsage               `json:"usage,omitempty"` // Reuse schema type
	ServiceTier       *string                         `json:"service_tier,omitempty"`
	SystemFingerprint *string                         `json:"system_fingerprint,omitempty"`
}

// OpenAIEmbeddingResponse represents an OpenAI embedding response
type OpenAIEmbeddingResponse struct {
	Object            string                     `json:"object"`
	Data              []schemas.BifrostEmbedding `json:"data"`
	Model             string                     `json:"model"`
	Usage             *schemas.LLMUsage          `json:"usage,omitempty"`
	ServiceTier       *string                    `json:"service_tier,omitempty"`
	SystemFingerprint *string                    `json:"system_fingerprint,omitempty"`
}

// OpenAIChatError represents an OpenAI chat completion error response
type OpenAIChatError struct {
	EventID string `json:"event_id"` // Unique identifier for the error event
	Type    string `json:"type"`     // Type of error
	Error   struct {
		Type    string      `json:"type"`     // Error type
		Code    string      `json:"code"`     // Error code
		Message string      `json:"message"`  // Error message
		Param   interface{} `json:"param"`    // Parameter that caused the error
		EventID string      `json:"event_id"` // Event ID for tracking
	} `json:"error"`
}

// OpenAIChatErrorStruct represents the error structure of an OpenAI chat completion error response
type OpenAIChatErrorStruct struct {
	Type    string      `json:"type"`     // Error type
	Code    string      `json:"code"`     // Error code
	Message string      `json:"message"`  // Error message
	Param   interface{} `json:"param"`    // Parameter that caused the error
	EventID string      `json:"event_id"` // Event ID for tracking
}

// OpenAIStreamChoice represents a choice in a streaming response chunk
type OpenAIStreamChoice struct {
	Index        int                `json:"index"`
	Delta        *OpenAIStreamDelta `json:"delta,omitempty"`
	FinishReason *string            `json:"finish_reason,omitempty"`
	LogProbs     *schemas.LogProbs  `json:"logprobs,omitempty"`
}

// OpenAIStreamDelta represents the incremental content in a streaming chunk
type OpenAIStreamDelta struct {
	Role      *string             `json:"role,omitempty"`
	Content   *string             `json:"content,omitempty"`
	ToolCalls *[]schemas.ToolCall `json:"tool_calls,omitempty"`
}

// OpenAIStreamResponse represents a single chunk in the OpenAI streaming response
type OpenAIStreamResponse struct {
	ID                string               `json:"id"`
	Object            string               `json:"object"`
	Created           int                  `json:"created"`
	Model             string               `json:"model"`
	SystemFingerprint *string              `json:"system_fingerprint,omitempty"`
	Choices           []OpenAIStreamChoice `json:"choices"`
	Usage             *schemas.LLMUsage    `json:"usage,omitempty"`
}

// ConvertToBifrostRequest converts an OpenAI chat request to Bifrost format
func (r *OpenAIChatRequest) ConvertToBifrostRequest(checkProviderFromModel bool) *schemas.BifrostRequest {
	provider, model := integrations.ParseModelString(r.Model, schemas.OpenAI, checkProviderFromModel)

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

// ConvertToBifrostRequest converts an OpenAI speech request to Bifrost format
func (r *OpenAISpeechRequest) ConvertToBifrostRequest(checkProviderFromModel bool) *schemas.BifrostRequest {
	provider, model := integrations.ParseModelString(r.Model, schemas.OpenAI, checkProviderFromModel)

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

// ConvertToBifrostRequest converts an OpenAI transcription request to Bifrost format
func (r *OpenAITranscriptionRequest) ConvertToBifrostRequest(checkProviderFromModel bool) *schemas.BifrostRequest {
	provider, model := integrations.ParseModelString(r.Model, schemas.OpenAI, checkProviderFromModel)

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

// ConvertToBifrostRequest converts an OpenAI embedding request to Bifrost format
func (r *OpenAIEmbeddingRequest) ConvertToBifrostRequest(checkProviderFromModel bool) *schemas.BifrostRequest {
	provider, model := integrations.ParseModelString(r.Model, schemas.OpenAI, checkProviderFromModel)

	// Prepare input texts array
	var texts []string
	switch input := r.Input.(type) {
	case string:
		texts = []string{input}
	case []string:
		texts = input
	case []interface{}:
		// Handle JSON unmarshaling which converts arrays to []interface{}
		texts = make([]string, len(input))
		for i, v := range input {
			if str, ok := v.(string); ok {
				texts[i] = str
			}
		}
	}

	// Create embedding input
	embeddingInput := &schemas.EmbeddingInput{
		Texts: texts,
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

// DeriveOpenAIFromBifrostResponse converts a Bifrost response to OpenAI format
func DeriveOpenAIFromBifrostResponse(bifrostResp *schemas.BifrostResponse) *OpenAIChatResponse {
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

// DeriveOpenAIEmbeddingFromBifrostResponse converts a Bifrost embedding response to OpenAI format
func DeriveOpenAIEmbeddingFromBifrostResponse(bifrostResp *schemas.BifrostResponse) *OpenAIEmbeddingResponse {
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

// DeriveOpenAIErrorFromBifrostError derives a OpenAIChatError from a BifrostError
func DeriveOpenAIErrorFromBifrostError(bifrostErr *schemas.BifrostError) *OpenAIChatError {
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

// DeriveOpenAIStreamFromBifrostError derives an OpenAI streaming error from a BifrostError
func DeriveOpenAIStreamFromBifrostError(bifrostErr *schemas.BifrostError) *OpenAIChatError {
	// For streaming, we use the same error format as regular OpenAI errors
	return DeriveOpenAIErrorFromBifrostError(bifrostErr)
}

// DeriveOpenAIStreamFromBifrostResponse converts a Bifrost response to OpenAI streaming format
func DeriveOpenAIStreamFromBifrostResponse(bifrostResp *schemas.BifrostResponse) *OpenAIStreamResponse {
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

func filterParams(provider schemas.ModelProvider, p *schemas.ModelParameters) *schemas.ModelParameters {
	if p == nil { return nil }
	return integrations.ValidateAndFilterParamsForProvider(provider, p)
}