// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains completion request handlers for text and chat completions.
package handlers

import (
	"encoding/json"
	"fmt"

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
	Provider  schemas.ModelProvider    `json:"provider"`  // The AI model provider to use
	Messages  []schemas.BifrostMessage `json:"messages"`  // Chat messages (for chat completion)
	Text      string                   `json:"text"`      // Text input (for text completion)
	Model     string                   `json:"model"`     // Model to use
	Params    *schemas.ModelParameters `json:"params"`    // Additional model parameters
	Fallbacks []schemas.Fallback       `json:"fallbacks"` // Fallback providers and models
}

type CompletionType string

const (
	CompletionTypeText CompletionType = "text"
	CompletionTypeChat CompletionType = "chat"
)

// RegisterRoutes registers all completion-related routes
func (h *CompletionHandler) RegisterRoutes(r *router.Router) {
	// Completion endpoints
	r.POST("/v1/text/completions", h.TextCompletion)
	r.POST("/v1/chat/completions", h.ChatCompletion)
}

// TextCompletion handles POST /v1/text/completions - Process text completion requests
func (h *CompletionHandler) TextCompletion(ctx *fasthttp.RequestCtx) {
	h.handleCompletion(ctx, CompletionTypeText)
}

// ChatCompletion handles POST /v1/chat/completions - Process chat completion requests
func (h *CompletionHandler) ChatCompletion(ctx *fasthttp.RequestCtx) {
	h.handleCompletion(ctx, CompletionTypeChat)
}

// handleCompletion processes both text and chat completion requests
// It handles request parsing, validation, and response formatting
func (h *CompletionHandler) handleCompletion(ctx *fasthttp.RequestCtx, completionType CompletionType) {
	var req CompletionRequest
	if err := json.Unmarshal(ctx.PostBody(), &req); err != nil {
		SendError(ctx, fasthttp.StatusBadRequest, fmt.Sprintf("Invalid request format: %v", err), h.logger)
		return
	}

	// Validate required fields
	if req.Provider == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "Provider is required", h.logger)
		return
	}

	if req.Model == "" {
		SendError(ctx, fasthttp.StatusBadRequest, "Model is required", h.logger)
		return
	}

	// Create BifrostRequest
	bifrostReq := &schemas.BifrostRequest{
		Provider:  req.Provider,
		Model:     req.Model,
		Params:    req.Params,
		Fallbacks: req.Fallbacks,
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
	}

	// Convert context
	bifrostCtx := lib.ConvertToBifrostContext(ctx)
	if bifrostCtx == nil {
		SendError(ctx, fasthttp.StatusInternalServerError, "Failed to convert context", h.logger)
		return
	}

	// Execute request
	var resp *schemas.BifrostResponse
	var bifrostErr *schemas.BifrostError

	switch completionType {
	case CompletionTypeText:
		resp, bifrostErr = h.client.TextCompletionRequest(*bifrostCtx, bifrostReq)
	case CompletionTypeChat:
		resp, bifrostErr = h.client.ChatCompletionRequest(*bifrostCtx, bifrostReq)
	}

	// Handle response
	if bifrostErr != nil {
		SendBifrostError(ctx, bifrostErr, h.logger)
		return
	}

	// Send successful response
	SendJSON(ctx, resp, h.logger)
}
