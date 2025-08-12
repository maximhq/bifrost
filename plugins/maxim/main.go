// Package maxim provides integration for Maxim's SDK as a Bifrost plugin.
// This file contains the main plugin implementation.
package maxim

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"

	"github.com/maximhq/maxim-go"
	"github.com/maximhq/maxim-go/logging"
)

// PluginName is the canonical name for the bifrost-maxim plugin.
const PluginName = "bifrost-maxim"

// NewMaximLoggerPlugin initializes and returns a Plugin instance for Maxim's logger.
//
// Parameters:
//   - apiKey: API key for Maxim SDK authentication
//   - logRepoId: ID for the Maxim logger instance
//
// Returns:
//   - schemas.Plugin: A configured plugin instance for request/response tracing
//   - error: Any error that occurred during plugin initialization
func NewMaximLoggerPlugin(apiKey string, logRepoId string) (schemas.Plugin, error) {
	// check if Maxim Logger variables are set
	if apiKey == "" {
		return nil, fmt.Errorf("apiKey is not set")
	}

	if logRepoId == "" {
		return nil, fmt.Errorf("log repo id is not set")
	}

	mx := maxim.Init(&maxim.MaximSDKConfig{ApiKey: apiKey})

	logger, err := mx.GetLogger(&logging.LoggerConfig{Id: logRepoId})
	if err != nil {
		return nil, err
	}

	plugin := &Plugin{logger}

	return plugin, nil
}

// ContextKey is a custom type for context keys to prevent key collisions in the context.
// It provides type safety for context values and ensures that context keys are unique
// across different packages.
type ContextKey string

// TraceIDKey is the context key used to store and retrieve trace IDs.
// This constant provides a consistent key for tracking request traces
// throughout the request/response lifecycle.
const (
	SessionIDKey    ContextKey = "session-id"
	TraceIDKey      ContextKey = "trace-id"
	GenerationIDKey ContextKey = "generation-id"
)

// The plugin provides request/response tracing functionality by integrating with Maxim's logging system.
// It supports both chat completion and text completion requests, tracking the entire lifecycle of each request
// including inputs, parameters, and responses.
//
// Key Features:
// - Automatic trace and generation ID management
// - Support for both chat and text completion requests
// - Contextual tracking across request lifecycle
// - Graceful handling of existing trace/generation IDs
//
// The plugin uses context values to maintain trace and generation IDs throughout the request lifecycle.
// These IDs can be propagated from external systems through HTTP headers (x-bf-maxim-trace-id and x-bf-maxim-generation-id).

// Plugin implements the schemas.Plugin interface for Maxim's logger.
// It provides request and response tracing functionality using the Maxim logger,
// allowing detailed tracking of requests and responses.
//
// Fields:
//   - logger: A Maxim logger instance used for tracing requests and responses
type Plugin struct {
	logger *logging.Logger
}

// GetName returns the name of the plugin.
func (plugin *Plugin) GetName() string {
	return PluginName
}

// PreHook is called before a request is processed by Bifrost.
// It manages trace and generation tracking for incoming requests by either:
// - Creating a new trace if none exists
// - Reusing an existing trace ID from the context
// - Creating a new generation within an existing trace
// - Skipping trace/generation creation if they already exist
//
// The function handles both chat completion and text completion requests,
// capturing relevant metadata such as:
// - Request type (chat/text completion)
// - Model information
// - Message content and role
// - Model parameters
//
// Parameters:
//   - ctx: Pointer to the context.Context that may contain existing trace/generation IDs
//   - req: The incoming Bifrost request to be traced
//
// Returns:
//   - *schemas.BifrostRequest: The original request, unmodified
//   - error: Any error that occurred during trace/generation creation
func (plugin *Plugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
	var traceID string
	var sessionID string

	// Check if context already has traceID and generationID
	if ctx != nil {
		if existingGenerationID, ok := (*ctx).Value(GenerationIDKey).(string); ok && existingGenerationID != "" {
			// If generationID exists, return early
			return req, nil, nil
		}

		if existingTraceID, ok := (*ctx).Value(TraceIDKey).(string); ok && existingTraceID != "" {
			// If traceID exists, and no generationID, create a new generation on the trace
			traceID = existingTraceID
		}

		if existingSessionID, ok := (*ctx).Value(SessionIDKey).(string); ok && existingSessionID != "" {
			sessionID = existingSessionID
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
			messages = append(messages, logging.CompletionRequest{
				Role:    string(message.Role),
				Content: message.Content,
			})
		}
		if len(*req.Input.ChatCompletionInput) > 0 {
			lastMsg := (*req.Input.ChatCompletionInput)[len(*req.Input.ChatCompletionInput)-1]
			if lastMsg.Content.ContentStr != nil {
				latestMessage = *lastMsg.Content.ContentStr
			} else if lastMsg.Content.ContentBlocks != nil {
				// Find the last text content block
				for i := len(*lastMsg.Content.ContentBlocks) - 1; i >= 0; i-- {
					block := (*lastMsg.Content.ContentBlocks)[i]
					if block.Type == "text" && block.Text != nil {
						latestMessage = *block.Text
						break
					}
				}
				// If no text block found, use placeholder
				if latestMessage == "" {
					latestMessage = "-"
				}
			}
		}
	} else if req.Input.TextCompletionInput != nil {
		requestType = "text_completion"
		tags = map[string]string{
			"action": "text_completion",
			"model":  req.Model,
		}
		messages = append(messages, logging.CompletionRequest{
			Role:    string(schemas.ModelChatMessageRoleUser),
			Content: req.Input.TextCompletionInput,
		})
		latestMessage = *req.Input.TextCompletionInput
	}

	if traceID == "" {
		// If traceID is not set, create a new trace
		traceID = uuid.New().String()

		traceConfig := logging.TraceConfig{
			Id:   traceID,
			Name: maxim.StrPtr(fmt.Sprintf("bifrost_%s", requestType)),
			Tags: &tags,
		}

		if sessionID != "" {
			traceConfig.SessionId = &sessionID
		}

		trace := plugin.logger.Trace(&traceConfig)

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
		Provider:        string(req.Provider),
		Tags:            &tags,
		Messages:        messages,
		ModelParameters: modelParams,
	})

	if ctx != nil {
		if _, ok := (*ctx).Value(TraceIDKey).(string); !ok {
			*ctx = context.WithValue(*ctx, TraceIDKey, traceID)
		}
		*ctx = context.WithValue(*ctx, GenerationIDKey, generationID)
	}

	return req, nil, nil
}

// PostHook is called after a request has been processed by Bifrost.
// It completes the request trace by:
// - Adding response data to the generation if a generation ID exists
// - Logging error details if bifrostErr is provided
// - Ending the generation if it exists
// - Ending the trace if a trace ID exists
// - Flushing all pending log data
//
// The function gracefully handles cases where trace or generation IDs may be missing,
// ensuring that partial logging is still performed when possible.
//
// Parameters:
//   - ctxRef: Pointer to the context.Context containing trace/generation IDs
//   - res: The Bifrost response to be traced
//   - bifrostErr: The BifrostError returned by the request, if any
//
// Returns:
//   - *schemas.BifrostResponse: The original response, unmodified
//   - *schemas.BifrostError: The original error, unmodified
//   - error: Never returns an error as it handles missing IDs gracefully
func (plugin *Plugin) PostHook(ctxRef *context.Context, res *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if ctxRef != nil {
		ctx := *ctxRef

		generationID, ok := ctx.Value(GenerationIDKey).(string)
		if ok {
			if bifrostErr != nil {
				genErr := logging.GenerationError{
					Message: bifrostErr.Error.Message,
					Code:    bifrostErr.Error.Code,
					Type:    bifrostErr.Error.Type,
				}
				plugin.logger.SetGenerationError(generationID, &genErr)
			} else if res != nil {
				plugin.logger.AddResultToGeneration(generationID, res)
			}

			plugin.logger.EndGeneration(generationID)
		}

		traceID, ok := ctx.Value(TraceIDKey).(string)
		if ok {
			plugin.logger.EndTrace(traceID)
		}
	}
	plugin.logger.Flush()

	return res, bifrostErr, nil
}

func (plugin *Plugin) Cleanup() error {
	plugin.logger.Flush()

	return nil
}
