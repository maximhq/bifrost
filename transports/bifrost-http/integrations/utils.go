package integrations

import (
	"bytes"
	"fmt"
	"log"
	"reflect"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

var bifrostContextKeyProvider = schemas.BifrostContextKey("provider")

var availableIntegrations = []string{
	"openai",
	"anthropic",
	"genai",
	"litellm",
	"langchain",
}

// newBifrostError wraps a standard error into a BifrostError with IsBifrostError set to false.
// This helper function reduces code duplication when handling non-Bifrost errors.
func newBifrostError(err error, message string) *schemas.BifrostError {
	if err == nil {
		return &schemas.BifrostError{
			IsBifrostError: false,
			Error: &schemas.ErrorField{
				Message: message,
			},
		}
	}

	return &schemas.BifrostError{
		IsBifrostError: false,
		Error: &schemas.ErrorField{
			Message: message,
			Error:   err,
		},
	}
}

// safeGetRequestType safely obtains the request type from a BifrostStream chunk.
// It checks multiple sources in order of preference:
// 1. Response ExtraFields if any response is available
// 2. BifrostError ExtraFields if error is available and not nil
// 3. Falls back to "unknown" if no source is available
func safeGetRequestType(chunk *schemas.BifrostStreamChunk) string {
	if chunk == nil {
		return "unknown"
	}

	// Try to get RequestType from response ExtraFields (preferred source)
	switch {
	case chunk.BifrostTextCompletionResponse != nil:
		return string(chunk.BifrostTextCompletionResponse.ExtraFields.RequestType)
	case chunk.BifrostChatResponse != nil:
		return string(chunk.BifrostChatResponse.ExtraFields.RequestType)
	case chunk.BifrostResponsesStreamResponse != nil:
		return string(chunk.BifrostResponsesStreamResponse.ExtraFields.RequestType)
	case chunk.BifrostSpeechStreamResponse != nil:
		return string(chunk.BifrostSpeechStreamResponse.ExtraFields.RequestType)
	case chunk.BifrostTranscriptionStreamResponse != nil:
		return string(chunk.BifrostTranscriptionStreamResponse.ExtraFields.RequestType)
	}

	// Try to get RequestType from error ExtraFields (fallback)
	if chunk.BifrostError != nil && chunk.BifrostError.ExtraFields.RequestType != "" {
		return string(chunk.BifrostError.ExtraFields.RequestType)
	}

	// Final fallback
	return "unknown"
}

// extractHeadersFromRequest extracts headers from the request and returns them as a map.
// It uses the fasthttp.RequestCtx.Header.All() method to iterate over all headers.
func extractHeadersFromRequest(ctx *fasthttp.RequestCtx) map[string][]string {
	headers := make(map[string][]string)

	for key, value := range ctx.Request.Header.All() {
		keyStr := string(key)
		headers[keyStr] = append(headers[keyStr], string(value))
	}

	return headers
}

// extractExactPath returns the request path *after* the integration prefix,
// preserving the original query string exactly as sent by the client.
//
// Example:
//
//	/openai/v1/chat/completions?model=gpt-4o  ->  v1/chat/completions?model=gpt-4o
func extractExactPath(ctx *fasthttp.RequestCtx) string {
	// ctx.Path() returns only the path (no query) as a []byte backed by fasthttp’s internal buffers.
	// Treat it as read-only; don’t append to it directly.
	path := ctx.Path() // e.g. "/openai/v1/chat/completions"

	// Strip the integration prefix only if it’s at the start.
	for _, integration := range availableIntegrations {
		if bytes.HasPrefix(path, []byte("/"+integration+"/")) {
			path = path[len("/"+integration+"/"):]
			break
		}
	}

	// Raw query string as sent by client (unparsed, preserves ordering/duplicates/encoding).
	q := ctx.URI().QueryString() // e.g. "model=gpt-4o&stream=true"

	if len(q) == 0 {
		// No query → just return the (possibly trimmed) path.
		return string(path)
	}

	// --- Build "<path>?<query>" efficiently and safely ---
	//
	// Why not do: return string(path) + "?" + string(q) ?
	//   - That allocates multiple temporary strings and may copy data more than necessary.
	//
	// Why not append into 'path' directly?
	//   - 'path' may alias fasthttp’s internal buffers; mutating/expanding it could corrupt request state.
	//
	// We instead allocate a new buffer with exact capacity and copy into it,
	// staying in []byte until the final string conversion (1 allocation for the new slice).
	out := make([]byte, 0, len(path)+1+len(q)) // pre-size: path + "?" + query
	out = append(out, path...)                 // copy path bytes
	out = append(out, '?')                     // separator
	out = append(out, q...)                    // copy raw query bytes

	return string(out)
}

// sendStreamError sends an error in streaming format using the stream error converter if available
func (g *GenericRouter) sendStreamError(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, config RouteConfig, bifrostErr *schemas.BifrostError) {
	var errorResponse interface{}

	// Use stream error converter if available, otherwise fallback to regular error converter
	if config.StreamConfig != nil && config.StreamConfig.ErrorConverter != nil {
		errorResponse = config.StreamConfig.ErrorConverter(bifrostCtx, bifrostErr)
	} else {
		errorResponse = config.ErrorConverter(bifrostCtx, bifrostErr)
	}

	errorJSON, err := sonic.Marshal(map[string]interface{}{
		"error": errorResponse,
	})
	if err != nil {
		log.Printf("Failed to marshal error for SSE: %v", err)
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
		return
	}

	if _, err := fmt.Fprintf(ctx, "data: %s\n\n", errorJSON); err != nil {
		log.Printf("Failed to write SSE error: %v", err)
	}
}

// sendError sends an error response with the appropriate status code and JSON body.
// It handles different error types (string, error interface, or arbitrary objects).
func (g *GenericRouter) sendError(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, errorConverter ErrorConverter, bifrostErr *schemas.BifrostError) {
	if bifrostErr.StatusCode != nil {
		ctx.SetStatusCode(*bifrostErr.StatusCode)
	} else {
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
	}
	ctx.SetContentType("application/json")

	// Marshal the error for response and log the error for diagnostics
	responseObj := errorConverter(bifrostCtx, bifrostErr)
	errorBody, err := sonic.Marshal(responseObj)
	if err != nil {
		// Log the marshal failure and return a plain text error
		g.logger.Error("failed to marshal error response", "err", err, "path", extractExactPath(ctx))
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
		ctx.SetBodyString(fmt.Sprintf("failed to encode error response: %v", err))
		return
	}

	ctx.SetBody(errorBody)
}

// sendSuccess sends a successful response with HTTP 200 status and JSON body.
func (g *GenericRouter) sendSuccess(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext, errorConverter ErrorConverter, response interface{}) {
	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("application/json")

	responseBody, err := sonic.Marshal(response)
	if err != nil {
		g.sendError(ctx, bifrostCtx, errorConverter, newBifrostError(err, "failed to encode response"))
		return
	}

	ctx.SetBody(responseBody)
}

// extractAndParseFallbacks extracts fallbacks from the integration request and adds them to the BifrostRequest
func (g *GenericRouter) extractAndParseFallbacks(req interface{}, bifrostReq *schemas.BifrostRequest) error {
	// Check if the request has a fallbacks field ([]string)
	fallbacks, err := g.extractFallbacksFromRequest(req)
	if err != nil {
		return fmt.Errorf("failed to extract fallbacks: %w", err)
	}

	if len(fallbacks) == 0 {
		return nil // No fallbacks to process
	}

	provider, _, _ := bifrostReq.GetRequestFields()

	// Parse fallbacks from strings to Fallback structs
	parsedFallbacks := make([]schemas.Fallback, 0, len(fallbacks))
	for _, fallbackStr := range fallbacks {
		if fallbackStr == "" {
			continue // Skip empty strings
		}

		// Use ParseModelString to extract provider and model
		provider, model := schemas.ParseModelString(fallbackStr, provider)

		parsedFallback := schemas.Fallback{
			Provider: provider,
			Model:    model,
		}
		parsedFallbacks = append(parsedFallbacks, parsedFallback)
	}

	if len(parsedFallbacks) == 0 {
		return nil // No valid fallbacks found
	}

	// Add fallbacks to the main BifrostRequest
	bifrostReq.SetFallbacks(parsedFallbacks)

	// Also add fallbacks to the specific request type if it exists
	switch bifrostReq.RequestType {
	case schemas.TextCompletionRequest, schemas.TextCompletionStreamRequest:
		if bifrostReq.TextCompletionRequest != nil {
			bifrostReq.TextCompletionRequest.Fallbacks = parsedFallbacks
		}
	case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest:
		if bifrostReq.ChatRequest != nil {
			bifrostReq.ChatRequest.Fallbacks = parsedFallbacks
		}
	case schemas.ResponsesRequest, schemas.ResponsesStreamRequest:
		if bifrostReq.ResponsesRequest != nil {
			bifrostReq.ResponsesRequest.Fallbacks = parsedFallbacks
		}
	case schemas.EmbeddingRequest:
		if bifrostReq.EmbeddingRequest != nil {
			bifrostReq.EmbeddingRequest.Fallbacks = parsedFallbacks
		}
	case schemas.SpeechRequest, schemas.SpeechStreamRequest:
		if bifrostReq.SpeechRequest != nil {
			bifrostReq.SpeechRequest.Fallbacks = parsedFallbacks
		}
	case schemas.TranscriptionRequest, schemas.TranscriptionStreamRequest:
		if bifrostReq.TranscriptionRequest != nil {
			bifrostReq.TranscriptionRequest.Fallbacks = parsedFallbacks
		}
	case schemas.ImageGenerationRequest, schemas.ImageGenerationStreamRequest:
		if bifrostReq.ImageGenerationRequest != nil {
			bifrostReq.ImageGenerationRequest.Fallbacks = parsedFallbacks
		}
	}

	return nil
}

// extractFallbacksFromRequest uses reflection to extract fallbacks field from any request type
func (g *GenericRouter) extractFallbacksFromRequest(req interface{}) ([]string, error) {
	if req == nil {
		return nil, nil
	}

	// Try to use reflection to find a "fallbacks" field
	reqValue := reflect.ValueOf(req)
	if reqValue.Kind() == reflect.Ptr {
		reqValue = reqValue.Elem()
	}

	if reqValue.Kind() != reflect.Struct {
		return nil, nil // Not a struct, no fallbacks
	}

	// Look for the "fallbacks" field
	fallbacksField := reqValue.FieldByName("fallbacks")
	if !fallbacksField.IsValid() {
		return nil, nil // No fallbacks field found
	}

	// Handle different types of fallbacks field
	switch fallbacksField.Kind() {
	case reflect.Slice:
		if fallbacksField.Type().Elem().Kind() == reflect.String {
			// []string case
			fallbacks := make([]string, fallbacksField.Len())
			for i := 0; i < fallbacksField.Len(); i++ {
				fallbacks[i] = fallbacksField.Index(i).String()
			}
			return fallbacks, nil
		}
	case reflect.String:
		// Single string case - treat as one fallback
		return []string{fallbacksField.String()}, nil
	}

	return nil, nil
}

// isAnthropicAPIKeyAuth checks if the request uses standard API key authentication.
// Returns true for API key auth (x-api-key header), false for OAuth (Bearer sk-ant-oat*).
// This is required for Claude Code specifically, which may use OAuth authentication.
// Default behavior is to assume API mode when neither x-api-key nor OAuth token is present.
func isAnthropicAPIKeyAuth(ctx *fasthttp.RequestCtx) bool {
	// If x-api-key header is present - this is definitely API mode
	if apiKey := string(ctx.Request.Header.Peek("x-api-key")); apiKey != "" {
		return true
	}
	// Check for OAuth token in Authorization header
	if authHeader := string(ctx.Request.Header.Peek("Authorization")); authHeader != "" {
		if strings.HasPrefix(strings.ToLower(authHeader), "bearer sk-ant-oat") {
			return false // OAuth mode, NOT API
		}
	}
	// Default to API mode
	return true
}
