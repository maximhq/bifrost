// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains common utility functions used across all handlers.
package handlers

import (
	"encoding/json"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// SendJSON sends a JSON response with 200 OK status
func SendJSON(ctx *fasthttp.RequestCtx, data interface{}, logger schemas.Logger) {
	ctx.SetStatusCode(fasthttp.StatusOK)
	ctx.SetContentType("application/json")

	if err := json.NewEncoder(ctx).Encode(data); err != nil {
		logger.Warn(fmt.Sprintf("Failed to encode JSON response: %v", err))
		SendError(ctx, fasthttp.StatusInternalServerError, fmt.Sprintf("Failed to encode response: %v", err), logger)
	}
}

// SendError sends a BifrostError response
func SendError(ctx *fasthttp.RequestCtx, statusCode int, message string, logger schemas.Logger) {
	bifrostErr := &schemas.BifrostError{
		IsBifrostError: false,
		StatusCode:     &statusCode,
		Error: schemas.ErrorField{
			Message: message,
		},
	}
	SendBifrostError(ctx, bifrostErr, logger)
}

// SendBifrostError sends a BifrostError response
func SendBifrostError(ctx *fasthttp.RequestCtx, bifrostErr *schemas.BifrostError, logger schemas.Logger) {
	if bifrostErr.StatusCode != nil {
		ctx.SetStatusCode(*bifrostErr.StatusCode)
	} else {
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
	}

	ctx.SetContentType("application/json")
	if encodeErr := json.NewEncoder(ctx).Encode(bifrostErr); encodeErr != nil {
		logger.Warn(fmt.Sprintf("Failed to encode error response: %v", encodeErr))
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
		ctx.SetBodyString(fmt.Sprintf("Failed to encode error response: %v", encodeErr))
	}
}

// SendSSEError sends an error in Server-Sent Events format
func SendSSEError(ctx *fasthttp.RequestCtx, bifrostErr *schemas.BifrostError, logger schemas.Logger) {
	errorJSON, err := json.Marshal(map[string]interface{}{
		"error": bifrostErr,
	})
	if err != nil {
		logger.Error(fmt.Errorf("failed to marshal error for SSE: %w", err))
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
		return
	}

	if _, err := fmt.Fprintf(ctx, "data: %s\n\n", errorJSON); err != nil {
		logger.Warn(fmt.Sprintf("Failed to write SSE error: %v", err))
	}
}
