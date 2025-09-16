// Package handlers provides HTTP request handlers for the Bifrost HTTP transport.
// This file contains common utility functions used across all handlers.
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

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
	} else if !bifrostErr.IsBifrostError {
		ctx.SetStatusCode(fasthttp.StatusBadRequest)
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
		logger.Error("failed to marshal error for SSE: %v", err)
		ctx.SetStatusCode(fasthttp.StatusInternalServerError)
		return
	}

	if _, err := fmt.Fprintf(ctx, "data: %s\n\n", errorJSON); err != nil {
		logger.Warn(fmt.Sprintf("Failed to write SSE error: %v", err))
	}
}

// IsOriginAllowed checks if the given origin is allowed based on localhost rules and configured allowed origins.
// Localhost origins are always allowed. Additional origins can be configured in allowedOrigins.
func IsOriginAllowed(origin string, allowedOrigins []string) bool {
	// Always allow localhost origins
	if isLocalhostOrigin(origin) {
		return true
	}

	// Check configured allowed origins
	return slices.Contains(allowedOrigins, origin)
}

// isLocalhostOrigin checks if the given origin is a localhost origin
func isLocalhostOrigin(origin string) bool {
	return strings.HasPrefix(origin, "http://localhost:") ||
		strings.HasPrefix(origin, "https://localhost:") ||
		strings.HasPrefix(origin, "http://127.0.0.1:") ||
		strings.HasPrefix(origin, "http://0.0.0.0:") ||
		strings.HasPrefix(origin, "https://127.0.0.1:")
}

// ParseModel parses a model string in the format "provider/model" or "provider/nested/model"
// Returns the provider and full model name after the first slash
func ParseModel(model string) (string, string, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", "", fmt.Errorf("model cannot be empty")
	}

	parts := strings.SplitN(model, "/", 2)
	if len(parts) < 2 {
		return "", "", fmt.Errorf("model must be in the format 'provider/model'")
	}

	provider := strings.TrimSpace(parts[0])
	name := strings.TrimSpace(parts[1])
	if provider == "" || name == "" {
		return "", "", fmt.Errorf("model must be in the format 'provider/model' with non-empty provider and model")
	}
	return provider, name, nil
}

func getRetryCount(ctx *context.Context) int {
	if ctx != nil {
		if v, ok := (*ctx).Value(schemas.BifrostContextKeyRetryCount).(int); ok {
			return v
		}
	}
	return -1
}
