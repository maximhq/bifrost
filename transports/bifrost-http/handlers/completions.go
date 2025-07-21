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

	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// CompletionHandler manages HTTP requests for completion operations
type CompletionHandler struct {
	client *bifrost.Bifrost
	logger schemas.Logger
}

// NewCompletionHandler creates a new completion handler instance
func NewCompletionHandler(client *bifrost.Bifrost, logger schemas.Logger) *CompletionHandler {
	return &CompletionHandler{
		client: client,
		logger: logger,
	}
}

// CompletionRequest represents a request for either text or chat completion
type CompletionRequest struct {
	Model     string                   `json:"model"`     // Model to use in "provider/model" format
	Messages  []schemas.BifrostMessage `json:"messages"`  // Chat messages (for chat completion)
	Text      string                   `json:"text"`      // Text input (for text completion)
	Params    *schemas.ModelParameters `json:"params"`    // Additional model parameters
	Fallbacks []string                 `json:"fallbacks"` // Fallback providers and models in "provider/model" format
	Stream    *bool                    `json:"stream"`    // Whether to stream the response

	// Speech inputs
	Input          string                   `json:"input"`
	Voice          schemas.SpeechVoiceInput `json:"voice"`
	Instructions   string                   `json:"instructions"`
	ResponseFormat string                   `json:"response_format"`
	StreamFormat   *string                  `json:"stream_format,omitempty"`
}

type CompletionType string

const (
	CompletionTypeText          CompletionType = "text"
	CompletionTypeChat          CompletionType = "chat"
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
	r.POST("/v1/text/completions", h.TextCompletion)
	r.POST("/v1/chat/completions", h.ChatCompletion)
	r.POST("/v1/audio/speech", h.SpeechCompletion)
	r.POST("/v1/audio/transcriptions", h.TranscriptionCompletion)
}

// TextCompletion handles POST /v1/text/completions - Process text completion requests
func (h *CompletionHandler) TextCompletion(ctx *fasthttp.RequestCtx) {
	h.handleRequest(ctx, CompletionTypeText)
}

// ChatCompletion handles POST /v1/chat/completions - Process chat completion requests
func (h *CompletionHandler) ChatCompletion(ctx *fasthttp.RequestCtx) {
	h.handleRequest(ctx, CompletionTypeChat)
}

// SpeechCompletion handles POST /v1/audio/speech - Process speech completion requests
func (h *CompletionHandler) SpeechCompletion(ctx *fasthttp.RequestCtx) {
	h.handleRequest(ctx, CompletionTypeSpeech)
}

// TranscriptionCompletion handles POST /v1/audio/transcriptions - Process transcription requests
func (h *CompletionHandler) TranscriptionCompletion(ctx *fasthttp.RequestCtx) {
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

	modelParts := strings.SplitN(modelValues[0], "/", 2)
	if len(modelParts) < 2 {
		SendError(ctx, fasthttp.StatusBadRequest, "Model must be in the format of 'provider/model'", h.logger)
		return
	}

	provider := modelParts[0]
	modelName := modelParts[1]

	// Extract file (required)
	fileHeaders := form.File["file"]
	if len(fileHeaders) == 0 {
		SendError(ctx, fasthttp.StatusBadRequest, "File is required", h.logger)
		return
	}

	fileHeader := fileHeaders[0]

	// Validate file size and format
	if err := h.validateAudioFile(fileHeader); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, err.Error(), h.logger)
		return
	}

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
	bifrostCtx := lib.ConvertToBifrostContext(ctx)
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
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err), h.logger)
		return
	}

	if req.Model == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "Model is required", h.logger)
		return
	}

	model := strings.SplitN(req.Model, "/", 2)
	if len(model) < 2 {
		SendError(ctx, fasthttp.StatusBadRequest, "Model must be in the format of 'provider/model'", h.logger)
		return
	}

	provider := model[0]
	modelName := model[1]

	fallbacks := make([]schemas.Fallback, len(req.Fallbacks))
	for i, fallback := range req.Fallbacks {
		fallbackModel := strings.Split(fallback, "/")
		if len(fallbackModel) != 2 {
			SendError(ctx, fasthttp.StatusBadRequest, "Fallback must be in the format of 'provider/model'", h.logger)
			return
		}
		fallbacks[i] = schemas.Fallback{
			Provider: schemas.ModelProvider(fallbackModel[0]),
			Model:    fallbackModel[1],
		}
	}

	// Create BifrostRequest
	bifrostReq := &schemas.BifrostRequest{
		Model:     modelName,
		Provider:  schemas.ModelProvider(provider),
		Params:    req.Params,
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
	case CompletionTypeSpeech:
		if req.Input == "" {
			SendError(ctx, fasthttp.StatusBadRequest, "Input is required for speech completion", h.logger)
			return
		}
		if req.Voice.Voice == nil && len(req.Voice.MultiVoiceConfig) == 0 {
			SendError(ctx, fasthttp.StatusBadRequest, "Voice is required for speech completion", h.logger)
			return
		}
		bifrostReq.Input = schemas.RequestInput{
			SpeechInput: &schemas.SpeechInput{
				Input:          req.Input,
				VoiceConfig:    req.Voice,
				Instructions:   req.Instructions,
				ResponseFormat: req.ResponseFormat,
			},
		}
	}

	// Convert context
	bifrostCtx := lib.ConvertToBifrostContext(ctx)
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
			responseJSON, err := json.Marshal(data)
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
