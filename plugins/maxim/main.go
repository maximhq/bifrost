// Package plugins provides plugins for the Bifrost system.
// This file contains the Plugin implementation using maxim's logger plugin for bifrost.
package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"

	"github.com/maximhq/maxim-go"
	"github.com/maximhq/maxim-go/logging"
)

// NewMaximLogger initializes and returns a Plugin instance for Maxim's logger.
//
// Parameters:
//   - apiKey: API key for Maxim SDK authentication
//   - loggerId: ID for the Maxim logger instance
//
// Returns:
//   - schemas.Plugin: A configured plugin instance for request/response tracing
//   - error: Any error that occurred during plugin initialization
func NewMaximLoggerPlugin(apiKey string, loggerId string) (schemas.Plugin, error) {
	// check if Maxim Logger variables are set
	if apiKey == "" {
		return nil, fmt.Errorf("apiKey is not set")
	}

	if loggerId == "" {
		return nil, fmt.Errorf("loggerId is not set")
	}

	mx := maxim.Init(&maxim.MaximSDKConfig{ApiKey: apiKey})

	logger, err := mx.GetLogger(&logging.LoggerConfig{Id: loggerId})
	if err != nil {
		return nil, err
	}

	plugin := &Plugin{logger}

	return plugin, nil
}

// contextKey is a custom type for context keys to prevent key collisions in the context.
// It provides type safety for context values and ensures that context keys are unique
// across different packages.
type contextKey string

// traceIDKey is the context key used to store and retrieve trace IDs.
// This constant provides a consistent key for tracking request traces
// throughout the request/response lifecycle.
const (
	traceIDKey      contextKey = "traceID"
	generationIDKey contextKey = "generationID"
)

// Plugin implements the schemas.Plugin interface for Maxim's logger.
// It provides request and response tracing functionality using the Maxim logger,
// allowing detailed tracking of requests and responses.
//
// Fields:
//   - logger: A Maxim logger instance used for tracing requests and responses
type Plugin struct {
	logger *logging.Logger
}

// PreHook is called before a request is processed by Bifrost.
// It creates a new trace for the incoming request and stores the trace ID in the context.
// The trace includes request details that can be used for debugging and monitoring.
//
// Parameters:
//   - ctx: Pointer to the context.Context that will store the trace ID
//   - req: The incoming Bifrost request to be traced
//
// Returns:
//   - *schemas.BifrostRequest: The original request, unmodified
//   - error: Always returns nil as this implementation doesn't produce errors
//
// The trace ID format is "YYYYMMDD_HHmmssSSS" based on the current time.
// If the context is nil, tracing information will still be logged but not stored in context.
func (plugin *Plugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, error) {
	var traceID string

	// Check if context already has traceID and generationID
	if ctx != nil {
		if existingGenerationID, ok := (*ctx).Value(generationIDKey).(string); ok && existingGenerationID != "" {
			// If generationID exists, return early
			return req, nil
		}

		if existingTraceID, ok := (*ctx).Value(traceIDKey).(string); ok && existingTraceID != "" {
			// If traceID exists, and no generationID, create a new generation on the trace
			traceID = existingTraceID
		}
	}

	// Determine request type and set appropriate tags
	var requestType string
	var tags map[string]string
	var messages []logging.CompletionRequest
	var latestMessage string

	if req.Input.ChatCompletionInput != nil {
		requestType = "chat_completion"
		tags = map[string]string{
			"action": "chat_completion",
			"model":  req.Model,
		}
		for _, message := range *req.Input.ChatCompletionInput {
			if message.Content != nil {
				messages = append(messages, logging.CompletionRequest{
					Role:    string(message.Role),
					Content: message.Content,
				})
			} else if message.ImageContent != nil {
				messages = append(messages, logging.CompletionRequest{
					Role:    string(message.Role),
					Content: message.ImageContent,
				})
			} else if message.ToolCalls != nil {
				messages = append(messages, logging.CompletionRequest{
					Role:    string(message.Role),
					Content: message.ToolCalls,
				})
			}
		}
		if len(*req.Input.ChatCompletionInput) > 0 {
			lastMsg := (*req.Input.ChatCompletionInput)[len(*req.Input.ChatCompletionInput)-1]
			if lastMsg.Content != nil {
				latestMessage = *lastMsg.Content
			}
		}
	} else if req.Input.TextCompletionInput != nil {
		requestType = "text_completion"
		tags = map[string]string{
			"action": "text_completion",
			"model":  req.Model,
		}
		messages = append(messages, logging.CompletionRequest{
			Role:    "user",
			Content: req.Input.TextCompletionInput,
		})
		latestMessage = *req.Input.TextCompletionInput
	}

	if traceID == "" {
		// If traceID is not set, create a new trace
		traceID = uuid.New().String()

		trace := plugin.logger.Trace(&logging.TraceConfig{
			Id:   traceID,
			Name: maxim.StrPtr(fmt.Sprintf("bifrost_%s", requestType)),
			Tags: &tags,
		})

		trace.SetInput(latestMessage)
	}

	// Convert ModelParameters to map[string]interface{}
	modelParams := make(map[string]interface{})
	if req.Params != nil {
		// Convert the struct to a map using reflection or JSON marshaling
		jsonData, err := json.Marshal(req.Params)
		if err == nil {
			json.Unmarshal(jsonData, &modelParams)
		}
	}

	generationID := uuid.New().String()

	plugin.logger.AddGenerationToTrace(traceID, &logging.GenerationConfig{
		Id:              generationID,
		Model:           req.Model,
		Provider:        req.Provider,
		Tags:            &tags,
		Messages:        messages,
		ModelParameters: modelParams,
	})

	if ctx != nil {
		if _, ok := (*ctx).Value(traceIDKey).(string); !ok {
			*ctx = context.WithValue(*ctx, traceIDKey, traceID)
		}
		*ctx = context.WithValue(*ctx, generationIDKey, generationID)
	}

	return req, nil
}

// PostHook is called after a request has been processed by Bifrost.
// It retrieves the trace ID from the context and logs the response details.
// This completes the request trace by adding response information.
//
// Parameters:
//   - ctxRef: Pointer to the context.Context containing the trace ID
//   - res: The Bifrost response to be traced
//
// Returns:
//   - *schemas.BifrostResponse: The original response, unmodified
//   - error: Returns an error if the trace ID cannot be retrieved from the context
//
// If the context is nil or the trace ID is not found, an error will be returned
// but the response will still be passed through unmodified.
func (plugin *Plugin) PostHook(ctxRef *context.Context, res *schemas.BifrostResponse) (*schemas.BifrostResponse, error) {
	if ctxRef != nil {
		ctx := *ctxRef

		generationID, ok := ctx.Value(generationIDKey).(string)
		if ok {
			plugin.logger.AddResultToGeneration(generationID, res)
			plugin.logger.EndGeneration(generationID)
		}

		traceID, ok := ctx.Value(traceIDKey).(string)
		if ok {
			plugin.logger.EndTrace(traceID)
		}

		plugin.logger.Flush()
	}

	return res, nil
}
