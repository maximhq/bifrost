// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains completion request handlers for text and chat completions.
package handlers

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// CompletionHandler manages HTTP requests for completion operations
type CompletionHandler struct {
	client       *bifrost.Bifrost
	handlerStore lib.HandlerStore
	logger       schemas.Logger
}

// NewCompletionHandler creates a new completion handler instance
func NewCompletionHandler(client *bifrost.Bifrost, handlerStore lib.HandlerStore, logger schemas.Logger) *CompletionHandler {
	return &CompletionHandler{
		client:       client,
		handlerStore: handlerStore,
		logger:       logger,
	}
}

// Known fields for CompletionRequest
var completionRequestKnownFields = map[string]bool{
	"model":               true,
	"messages":            true,
	"text":                true,
	"fallbacks":           true,
	"stream":              true,
	"input":               true,
	"voice":               true,
	"instructions":        true,
	"response_format":     true,
	"stream_format":       true,
	"tool_choice":         true,
	"tools":               true,
	"temperature":         true,
	"top_p":               true,
	"top_k":               true,
	"max_tokens":          true,
	"stop_sequences":      true,
	"presence_penalty":    true,
	"frequency_penalty":   true,
	"parallel_tool_calls": true,
	"encoding_format":     true,
	"dimensions":          true,
	"user":                true,
}

// CompletionRequest represents a request for either text or chat completion
type CompletionRequest struct {
	Model     string                   `json:"model"`     // Model to use in "provider/model" format
	Messages  []schemas.BifrostMessage `json:"messages"`  // Chat messages (for chat completion)
	Text      string                   `json:"text"`      // Text input (for text completion)
	Fallbacks []string                 `json:"fallbacks"` // Fallback providers and models in "provider/model" format
	Stream    *bool                    `json:"stream"`    // Whether to stream the response

	// Speech inputs
	Input          VoiceInput               `json:"input"`
	Voice          schemas.SpeechVoiceInput `json:"voice"`
	Instructions   string                   `json:"instructions"`
	ResponseFormat string                   `json:"response_format"`
	StreamFormat   *string                  `json:"stream_format,omitempty"`

	ToolChoice        *schemas.ToolChoice `json:"tool_choice,omitempty"`         // Whether to call a tool
	Tools             *[]schemas.Tool     `json:"tools,omitempty"`               // Tools to use
	Temperature       *float64            `json:"temperature,omitempty"`         // Controls randomness in the output
	TopP              *float64            `json:"top_p,omitempty"`               // Controls diversity via nucleus sampling
	TopK              *int                `json:"top_k,omitempty"`               // Controls diversity via top-k sampling
	MaxTokens         *int                `json:"max_tokens,omitempty"`          // Maximum number of tokens to generate
	StopSequences     *[]string           `json:"stop_sequences,omitempty"`      // Sequences that stop generation
	PresencePenalty   *float64            `json:"presence_penalty,omitempty"`    // Penalizes repeated tokens
	FrequencyPenalty  *float64            `json:"frequency_penalty,omitempty"`   // Penalizes frequent tokens
	ParallelToolCalls *bool               `json:"parallel_tool_calls,omitempty"` // Enables parallel tool calls
	EncodingFormat    *string             `json:"encoding_format,omitempty"`     // Format for embedding output (e.g., "float", "base64")
	Dimensions        *int                `json:"dimensions,omitempty"`          // Number of dimensions for embedding output
	User              *string             `json:"user,omitempty"`                // User identifier for tracking
	// Dynamic parameters that can be provider-specific, they are directly
	// added to the request as is.
	ExtraParams map[string]interface{} `json:"-"`
}

func (cr *CompletionRequest) UnmarshalJSON(data []byte) error {
	// Use type alias to avoid infinite recursion
	type Alias CompletionRequest
	aux := (*Alias)(cr)

	// First unmarshal known fields
	if err := sonic.Unmarshal(data, aux); err != nil {
		return err
	}

	// Then unmarshal to map for unknown fields
	var rawData map[string]json.RawMessage
	if err := sonic.Unmarshal(data, &rawData); err != nil {
		return err
	}

	// Initialize ExtraParams
	if cr.ExtraParams == nil {
		cr.ExtraParams = make(map[string]interface{})
	}

	// Extract unknown fields
	for key, value := range rawData {
		if !completionRequestKnownFields[key] {
			var v interface{}
			if err := sonic.Unmarshal(value, &v); err != nil {
				continue // Skip fields that can't be unmarshaled
			}
			cr.ExtraParams[key] = v
		}
	}

	return nil
}

type VoiceInput struct {
	InputString *string   `json:"input_string,omitempty"`
	InputArray  *[]string `json:"input_array,omitempty"`
}

// MarshalJSON implements custom JSON marshaling
func (i VoiceInput) MarshalJSON() ([]byte, error) {
	if i.InputString != nil {
		return sonic.Marshal(*i.InputString)
	}
	if i.InputArray != nil {
		return sonic.Marshal(*i.InputArray)
	}
	return nil, fmt.Errorf("input must be a string or array of strings")
}

// UnmarshalJSON implements custom JSON unmarshaling
func (i *VoiceInput) UnmarshalJSON(data []byte) error {
	// Reset fields
	i.InputString = nil
	i.InputArray = nil

	// Try to unmarshal as string first
	var str string
	if err := sonic.Unmarshal(data, &str); err == nil {
		i.InputString = &str
		return nil
	}

	// If that fails, try to unmarshal as array of strings
	var strArray []string
	if err := sonic.Unmarshal(data, &strArray); err == nil {
		i.InputArray = &strArray
		return nil
	}

	return fmt.Errorf("input must be a string or array of strings")
}

func (cr *CompletionRequest) GetModelParameters() *schemas.ModelParameters {
	params := &schemas.ModelParameters{
		ExtraParams:       make(map[string]interface{}),
		ToolChoice:        cr.ToolChoice,
		Tools:             cr.Tools,
		Temperature:       cr.Temperature,
		TopP:              cr.TopP,
		TopK:              cr.TopK,
		MaxTokens:         cr.MaxTokens,
		StopSequences:     cr.StopSequences,
		PresencePenalty:   cr.PresencePenalty,
		FrequencyPenalty:  cr.FrequencyPenalty,
		ParallelToolCalls: cr.ParallelToolCalls,
		EncodingFormat:    cr.EncodingFormat,
		Dimensions:        cr.Dimensions,
		User:              cr.User,
	}

	if cr.ExtraParams != nil {
		for k, v := range cr.ExtraParams {
			params.ExtraParams[k] = v
		}
	}

	return params
}

type CompletionType string

const (
	CompletionTypeText          CompletionType = "text"
	CompletionTypeChat          CompletionType = "chat"
	CompletionTypeEmbeddings    CompletionType = "embeddings"
	CompletionTypeSpeech        CompletionType = "speech"
	CompletionTypeTranscription CompletionType = "transcription"
)

const (
	// Maximum file size (25MB)
	MaxFileSize = 25 * 1024 * 1024

	// Primary MIME types for audio formats
	AudioMimeMP3   = "audio/mpeg"   // Covers MP3, MPEG, MPGA
	AudioMimeMP4   = "audio/mp4"    // MP4 audio
	AudioMimeM4A   = "audio/x-m4a"  // M4A specific
	AudioMimeOGG   = "audio/ogg"    // OGG audio
	AudioMimeWAV   = "audio/wav"    // WAV audio
	AudioMimeWEBM  = "audio/webm"   // WEBM audio
	AudioMimeFLAC  = "audio/flac"   // FLAC audio
	AudioMimeFLAC2 = "audio/x-flac" // Alternative FLAC
)

// validateAudioFile checks if the file size and format are valid
func (h *CompletionHandler) validateAudioFile(fileHeader *multipart.FileHeader) error {
	// Check file size
	if fileHeader.Size > MaxFileSize {
		return fmt.Errorf("file size exceeds maximum limit of %d MB", MaxFileSize/1024/1024)
	}

	// Get file extension
	ext := strings.ToLower(filepath.Ext(fileHeader.Filename))

	// Check file extension
	validExtensions := map[string]bool{
		".flac": true,
		".mp3":  true,
		".mp4":  true,
		".mpeg": true,
		".mpga": true,
		".m4a":  true,
		".ogg":  true,
		".wav":  true,
		".webm": true,
	}

	if !validExtensions[ext] {
		return fmt.Errorf("unsupported file format: %s. Supported formats: flac, mp3, mp4, mpeg, mpga, m4a, ogg, wav, webm", ext)
	}

	// Open file to check MIME type
	file, err := fileHeader.Open()
	if err != nil {
		return fmt.Errorf("failed to open file: %v", err)
	}
	defer file.Close()

	// Read first 512 bytes for MIME type detection
	buffer := make([]byte, 512)
	_, err = file.Read(buffer)
	if err != nil && err != io.EOF {
		return fmt.Errorf("failed to read file header: %v", err)
	}

	// Check MIME type
	mimeType := http.DetectContentType(buffer)
	validMimeTypes := map[string]bool{
		// Primary MIME types
		AudioMimeMP3:   true, // Covers MP3, MPEG, MPGA
		AudioMimeMP4:   true,
		AudioMimeM4A:   true,
		AudioMimeOGG:   true,
		AudioMimeWAV:   true,
		AudioMimeWEBM:  true,
		AudioMimeFLAC:  true,
		AudioMimeFLAC2: true,

		// Alternative MIME types
		"audio/mpeg3":       true,
		"audio/x-wav":       true,
		"audio/vnd.wave":    true,
		"audio/x-mpeg":      true,
		"audio/x-mpeg3":     true,
		"audio/x-mpg":       true,
		"audio/x-mpegaudio": true,
	}

	if !validMimeTypes[mimeType] {
		return fmt.Errorf("invalid file type: %s. Supported audio formats: flac, mp3, mp4, mpeg, mpga, m4a, ogg, wav, webm", mimeType)
	}

	// Reset file pointer for subsequent reads
	_, err = file.Seek(0, 0)
	if err != nil {
		return fmt.Errorf("failed to reset file pointer: %v", err)
	}

	return nil
}

// RegisterRoutes registers all completion-related routes
func (h *CompletionHandler) RegisterRoutes(r *router.Router) {
	// Completion endpoints
	r.POST("/v1/text/completions", h.textCompletion)
	r.POST("/v1/chat/completions", h.chatCompletion)
	r.POST("/v1/embeddings", h.embeddings)
	r.POST("/v1/audio/speech", h.speechCompletion)
	r.POST("/v1/audio/transcriptions", h.transcriptionCompletion)
}

// textCompletion handles POST /v1/text/completions - Process text completion requests
func (h *CompletionHandler) textCompletion(ctx *fasthttp.RequestCtx) {
	h.handleRequest(ctx, CompletionTypeText)
}

// chatCompletion handles POST /v1/chat/completions - Process chat completion requests
func (h *CompletionHandler) chatCompletion(ctx *fasthttp.RequestCtx) {
	h.handleRequest(ctx, CompletionTypeChat)
}

// embeddings handles POST /v1/embeddings - Process embeddings requests
func (h *CompletionHandler) embeddings(ctx *fasthttp.RequestCtx) {
	h.handleRequest(ctx, CompletionTypeEmbeddings)
}

// speechCompletion handles POST /v1/audio/speech - Process speech completion requests
func (h *CompletionHandler) speechCompletion(ctx *fasthttp.RequestCtx) {
	h.handleRequest(ctx, CompletionTypeSpeech)
}

// transcriptionCompletion handles POST /v1/audio/transcriptions - Process transcription requests
func (h *CompletionHandler) transcriptionCompletion(ctx *fasthttp.RequestCtx) {
	// Parse multipart form
	form, err := ctx.MultipartForm()
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Failed to parse multipart form: %v", err), h.logger)
		return
	}

	// Extract model (required)
	modelValues := form.Value["model"]
	if len(modelValues) == 0 || modelValues[0] == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "Model is required", h.logger)
		return
	}

	provider, modelName, err := ParseModel(modelValues[0])
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Model must be in the format of 'provider/model': %v", err), h.logger)
		return
	}

	// Extract file (required)
	fileHeaders := form.File["file"]
	if len(fileHeaders) == 0 {
		SendError(ctx, fasthttp.StatusBadRequest, "File is required", h.logger)
		return
	}

	fileHeader := fileHeaders[0]

	// // Validate file size and format
	// if err := h.validateAudioFile(fileHeader); err != nil {
	// 	SendError(ctx, fasthttp.StatusBadRequest, err.Error(), h.logger)
	// 	return
	// }

	file, err := fileHeader.Open()
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Failed to open uploaded file: %v", err), h.logger)
		return
	}
	defer file.Close()

	// Read file data
	fileData := make([]byte, fileHeader.Size)
	if _, err := file.Read(fileData); err != nil {
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to read uploaded file: %v", err), h.logger)
		return
	}

	// Create transcription input
	transcriptionInput := &schemas.TranscriptionInput{
		File: fileData,
	}

	// Extract optional parameters
	if languageValues := form.Value["language"]; len(languageValues) > 0 && languageValues[0] != "" {
		transcriptionInput.Language = &languageValues[0]
	}

	if promptValues := form.Value["prompt"]; len(promptValues) > 0 && promptValues[0] != "" {
		transcriptionInput.Prompt = &promptValues[0]
	}

	if responseFormatValues := form.Value["response_format"]; len(responseFormatValues) > 0 && responseFormatValues[0] != "" {
		transcriptionInput.ResponseFormat = &responseFormatValues[0]
	}

	// Create BifrostRequest
	bifrostReq := &schemas.BifrostRequest{
		Model:    modelName,
		Provider: schemas.ModelProvider(provider),
		Input: schemas.RequestInput{
			TranscriptionInput: transcriptionInput,
		},
	}

	// Convert context
	bifrostCtx := lib.ConvertToBifrostContext(ctx, h.handlerStore.ShouldAllowDirectKeys())
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context", h.logger)
		return
	}

	if streamValues := form.Value["stream"]; len(streamValues) > 0 && streamValues[0] != "" {
		stream := streamValues[0]
		if stream == "true" {
			h.handleStreamingTranscriptionRequest(ctx, bifrostReq, bifrostCtx)
			return
		}
	}

	// Make transcription request
	resp, bifrostErr := h.client.TranscriptionRequest(*bifrostCtx, bifrostReq)

	// Handle response
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr, h.logger)
		return
	}

	// Send successful response
	SendJSON(ctx, resp, h.logger)
}

// handleCompletion processes both text and chat completion requests
// It handles request parsing, validation, and response formatting
func (h *CompletionHandler) handleRequest(ctx *fasthttp.RequestCtx, completionType CompletionType) {
	var req CompletionRequest
	if err := sonic.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err), h.logger)
		return
	}

	if req.Model == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "Model is required", h.logger)
		return
	}

	provider, modelName, err := ParseModel(req.Model)
	if err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Model must be in the format of 'provider/model': %v", err), h.logger)
		return
	}

	fallbacks := make([]schemas.Fallback, len(req.Fallbacks))
	for i, fallback := range req.Fallbacks {
		fallbackProvider, fallbackModelName, err := ParseModel(fallback)
		if err != nil {
			SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Fallback must be in the format of 'provider/model': %v", err), h.logger)
			return
		}
		if fallbackProvider == "" || fallbackModelName == "" {
			SendError(ctx, fasthttp.StatusBadRequest, "Fallback must be in the format of 'provider/model'", h.logger)
			return
		}
		fallbacks[i] = schemas.Fallback{
			Provider: schemas.ModelProvider(fallbackProvider),
			Model:    fallbackModelName,
		}
	}

	// Create BifrostRequest
	bifrostReq := &schemas.BifrostRequest{
		Model:     modelName,
		Provider:  schemas.ModelProvider(provider),
		Params:    req.GetModelParameters(),
		Fallbacks: fallbacks,
	}

	// Validate and set input based on completion type
	switch completionType {
	case CompletionTypeText:
		if req.Text == "" {
			SendError(ctx, fasthttp.StatusBadRequest, "Text is required for text completion", h.logger)
			return
		}
		bifrostReq.Input = schemas.RequestInput{
			TextCompletionInput: &req.Text,
		}
	case CompletionTypeChat:
		if len(req.Messages) == 0 {
			SendError(ctx, fasthttp.StatusBadRequest, "Messages array is required for chat completion", h.logger)
			return
		}
		bifrostReq.Input = schemas.RequestInput{
			ChatCompletionInput: &req.Messages,
		}
	case CompletionTypeEmbeddings:
		if req.Input.InputString == nil && req.Input.InputArray == nil {
			SendError(ctx, fasthttp.StatusBadRequest, "Input is required for embeddings completion", h.logger)
			return
		}

		var texts []string
		if req.Input.InputString != nil {
			texts = []string{*req.Input.InputString}
		}
		if req.Input.InputArray != nil {
			texts = *req.Input.InputArray
		}
		bifrostReq.Input = schemas.RequestInput{
			EmbeddingInput: &schemas.EmbeddingInput{
				Texts: texts,
			},
		}
	case CompletionTypeSpeech:
		if req.Input.InputString == nil {
			SendError(ctx, fasthttp.StatusBadRequest, "Input is required for speech completion", h.logger)
			return
		}
		if req.Voice.Voice == nil && len(req.Voice.MultiVoiceConfig) == 0 {
			SendError(ctx, fasthttp.StatusBadRequest, "Voice is required for speech completion", h.logger)
			return
		}
		bifrostReq.Input = schemas.RequestInput{
			SpeechInput: &schemas.SpeechInput{
				Input:          *req.Input.InputString,
				VoiceConfig:    req.Voice,
				Instructions:   req.Instructions,
				ResponseFormat: req.ResponseFormat,
			},
		}
	}

	// Convert context
	bifrostCtx := lib.ConvertToBifrostContext(ctx, h.handlerStore.ShouldAllowDirectKeys())
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context", h.logger)
		return
	}

	// Check if streaming is requested
	isStreaming := req.Stream != nil && *req.Stream || req.StreamFormat != nil && *req.StreamFormat == "sse"

	// Handle streaming for chat completions only
	if isStreaming {
		switch completionType {
		case CompletionTypeChat:
			h.handleStreamingChatCompletion(ctx, bifrostReq, bifrostCtx)
			return
		case CompletionTypeSpeech:
			h.handleStreamingSpeech(ctx, bifrostReq, bifrostCtx)
			return
		}
	}

	// Handle non-streaming requests
	var resp *schemas.BifrostResponse
	var bifrostErr *schemas.BifrostError

	switch completionType {
	case CompletionTypeText:
		resp, bifrostErr = h.client.TextCompletionRequest(*bifrostCtx, bifrostReq)
	case CompletionTypeChat:
		resp, bifrostErr = h.client.ChatCompletionRequest(*bifrostCtx, bifrostReq)
	case CompletionTypeEmbeddings:
		resp, bifrostErr = h.client.EmbeddingRequest(*bifrostCtx, bifrostReq)
	case CompletionTypeSpeech:
		resp, bifrostErr = h.client.SpeechRequest(*bifrostCtx, bifrostReq)
	}

	// Handle response
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr, h.logger)
		return
	}

	if completionType == CompletionTypeSpeech {
		if resp.Speech.Audio == nil {
			SendError(ctx, fasthttp.StatusInternalServerError, "Speech response is missing audio data", h.logger)
			return
		}

		ctx.Response.Header.Set("Content-Type", "audio/mpeg")
		ctx.Response.Header.Set("Content-Disposition", "attachment; filename=speech.mp3")
		ctx.Response.Header.Set("Content-Length", strconv.Itoa(len(resp.Speech.Audio)))
		ctx.Response.SetBody(resp.Speech.Audio)
		return
	}

	// Send successful response
	SendJSON(ctx, resp, h.logger)
}

// handleStreamingResponse is a generic function to handle streaming responses using Server-Sent Events (SSE)
func (h *CompletionHandler) handleStreamingResponse(ctx *fasthttp.RequestCtx, getStream func() (chan *schemas.BifrostStream, *schemas.BifrostError), extractResponse func(*schemas.BifrostStream) (interface{}, bool)) {
	// Set SSE headers
	ctx.SetContentType("text/event-stream")
	ctx.Response.Header.Set("Cache-Control", "no-cache")
	ctx.Response.Header.Set("Connection", "keep-alive")
	ctx.Response.Header.Set("Access-Control-Allow-Origin", "*")

	// Get the streaming channel
	stream, bifrostErr := getStream()
	if bifrostErr != nil {
		// Send error in SSE format
		SendSSEError(ctx, bifrostErr, h.logger)
		return
	}

	// Use streaming response writer
	ctx.Response.SetBodyStreamWriter(func(w *bufio.Writer) {
		defer w.Flush()

		// Process streaming responses
		for response := range stream {
			if response == nil {
				continue
			}

			// Extract and validate the response data
			data, valid := extractResponse(response)
			if !valid {
				continue
			}

			// Convert response to JSON
			responseJSON, err := sonic.Marshal(data)
			if err != nil {
				h.logger.Warn(fmt.Sprintf("Failed to marshal streaming response: %v", err))
				continue
			}

			// Send as SSE data
			if _, err := fmt.Fprintf(w, "data: %s\n\n", responseJSON); err != nil {
				h.logger.Warn(fmt.Sprintf("Failed to write SSE data: %v", err))
				break
			}

			// Flush immediately to send the chunk
			if err := w.Flush(); err != nil {
				h.logger.Warn(fmt.Sprintf("Failed to flush SSE data: %v", err))
				break
			}
		}

		// Send the [DONE] marker to indicate the end of the stream
		if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
			h.logger.Warn(fmt.Sprintf("Failed to write SSE done marker: %v", err))
		}
	})
}

// handleStreamingChatCompletion handles streaming chat completion requests using Server-Sent Events (SSE)
func (h *CompletionHandler) handleStreamingChatCompletion(ctx *fasthttp.RequestCtx, req *schemas.BifrostRequest, bifrostCtx *context.Context) {
	getStream := func() (chan *schemas.BifrostStream, *schemas.BifrostError) {
		return h.client.ChatCompletionStreamRequest(*bifrostCtx, req)
	}

	extractResponse := func(response *schemas.BifrostStream) (interface{}, bool) {
		return response, true
	}

	h.handleStreamingResponse(ctx, getStream, extractResponse)
}

// handleStreamingSpeech handles streaming speech requests using Server-Sent Events (SSE)
func (h *CompletionHandler) handleStreamingSpeech(ctx *fasthttp.RequestCtx, req *schemas.BifrostRequest, bifrostCtx *context.Context) {
	getStream := func() (chan *schemas.BifrostStream, *schemas.BifrostError) {
		return h.client.SpeechStreamRequest(*bifrostCtx, req)
	}

	extractResponse := func(response *schemas.BifrostStream) (interface{}, bool) {
		if response.Speech == nil || response.Speech.BifrostSpeechStreamResponse == nil {
			return nil, false
		}
		return response.Speech, true
	}

	h.handleStreamingResponse(ctx, getStream, extractResponse)
}

// handleStreamingTranscriptionRequest handles streaming transcription requests using Server-Sent Events (SSE)
func (h *CompletionHandler) handleStreamingTranscriptionRequest(ctx *fasthttp.RequestCtx, req *schemas.BifrostRequest, bifrostCtx *context.Context) {
	getStream := func() (chan *schemas.BifrostStream, *schemas.BifrostError) {
		return h.client.TranscriptionStreamRequest(*bifrostCtx, req)
	}

	extractResponse := func(response *schemas.BifrostStream) (interface{}, bool) {
		if response.Transcribe == nil || response.Transcribe.BifrostTranscribeStreamResponse == nil {
			return nil, false
		}
		return response.Transcribe, true
	}

	h.handleStreamingResponse(ctx, getStream, extractResponse)
}
