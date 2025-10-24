// Package maxim provides integration for Maxim's SDK as a Bifrost plugin.
// This file contains the main plugin implementation.
package maxim

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/streaming"

	"github.com/maximhq/maxim-go"
	"github.com/maximhq/maxim-go/logging"
)

// PluginName is the canonical name for the maxim plugin.
const (
	PluginName         string = "maxim"
	PluginLoggerPrefix string = "[Maxim Plugin]"
)

// Config is the configuration for the maxim plugin.
//   - APIKey: API key for Maxim SDK authentication
//   - LogRepoID: Optional default ID for the Maxim logger instance
type Config struct {
	LogRepoID string `json:"log_repo_id,omitempty"` // Optional - can be empty
	APIKey    string `json:"api_key"`
}

// Plugin implements the schemas.Plugin interface for Maxim's logger.
// It provides request and response tracing functionality using Maxim logger,
// allowing detailed tracking of requests and responses across different log repositories.
//
// Fields:
//   - mx: The Maxim SDK instance for creating new loggers
//   - defaultLogRepoId: Default log repository ID from config (optional)
//   - loggers: Map of log repo ID to logger instances
//   - loggerMutex: RW mutex for thread-safe access to loggers map
type Plugin struct {
	mx               *maxim.Maxim
	defaultLogRepoID string
	loggers          map[string]*logging.Logger
	loggerMutex      *sync.RWMutex
	accumulator      *streaming.Accumulator
	logger           schemas.Logger
}

// Init initializes and returns a Plugin instance for Maxim's logger.
//
// Parameters:
//   - config: Configuration for the maxim plugin
//
// Returns:
//   - schemas.Plugin: A configured plugin instance for request/response tracing
//   - error: Any error that occurred during plugin initialization
func Init(config *Config, logger schemas.Logger) (schemas.Plugin, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}
	// check if Maxim Logger variables are set
	if config.APIKey == "" {
		return nil, fmt.Errorf("apiKey is not set")
	}

	mx := maxim.Init(&maxim.MaximSDKConfig{ApiKey: config.APIKey})

	plugin := &Plugin{
		mx:               mx,
		defaultLogRepoID: config.LogRepoID,
		loggers:          make(map[string]*logging.Logger),
		loggerMutex:      &sync.RWMutex{},
		accumulator:      streaming.NewAccumulator(nil, logger),
		logger:           logger,
	}

	// Initialize default logger if LogRepoId is provided
	if config.LogRepoID != "" {
		logger, err := mx.GetLogger(&logging.LoggerConfig{Id: config.LogRepoID})
		if err != nil {
			return nil, fmt.Errorf("failed to initialize default logger: %w", err)
		}
		plugin.loggers[config.LogRepoID] = logger
	}

	return plugin, nil
}

// TraceIDKey is the context key used to store and retrieve trace IDs.
// This constant provides a consistent key for tracking request traces
// throughout the request/response lifecycle.
const (
	SessionIDKey      schemas.BifrostContextKey = "session-id"
	TraceIDKey        schemas.BifrostContextKey = "trace-id"
	TraceNameKey      schemas.BifrostContextKey = "trace-name"
	GenerationIDKey   schemas.BifrostContextKey = "generation-id"
	GenerationNameKey schemas.BifrostContextKey = "generation-name"
	TagsKey           schemas.BifrostContextKey = "maxim-tags"
	LogRepoIDKey      schemas.BifrostContextKey = "log-repo-id"
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

// GetName returns the name of the plugin.
func (plugin *Plugin) GetName() string {
	return PluginName
}

// TransportInterceptor is not used for this plugin
func (plugin *Plugin) TransportInterceptor(url string, headers map[string]string, body map[string]any) (map[string]string, map[string]any, error) {
	return headers, body, nil
}

// getEffectiveLogRepoID determines which single log repo ID to use based on priority:
// 1. Header log repo ID (if provided)
// 2. Default log repo ID from config (if configured)
// 3. Empty string (skip logging)
func (plugin *Plugin) getEffectiveLogRepoID(ctx *context.Context) string {
	// Check for header log repo ID first (highest priority)
	if ctx != nil {
		if headerRepoID, ok := (*ctx).Value(LogRepoIDKey).(string); ok && headerRepoID != "" {
			return headerRepoID
		}
	}

	// Fall back to default log repo ID from config
	if plugin.defaultLogRepoID != "" {
		return plugin.defaultLogRepoID
	}

	// Return empty string if neither header nor default is available
	return ""
}

// getOrCreateLogger gets an existing logger or creates a new one for the given log repo ID
func (plugin *Plugin) getOrCreateLogger(logRepoID string) (*logging.Logger, error) {
	// First, try to get existing logger (read lock)
	plugin.loggerMutex.RLock()
	if logger, exists := plugin.loggers[logRepoID]; exists {
		plugin.loggerMutex.RUnlock()
		return logger, nil
	}
	plugin.loggerMutex.RUnlock()

	// Logger doesn't exist, create it (write lock)
	plugin.loggerMutex.Lock()
	defer plugin.loggerMutex.Unlock()

	// Double-check in case another goroutine created it while we were waiting
	if logger, exists := plugin.loggers[logRepoID]; exists {
		return logger, nil
	}

	// Create new logger
	logger, err := plugin.mx.GetLogger(&logging.LoggerConfig{Id: logRepoID})
	if err != nil {
		return nil, fmt.Errorf("failed to create logger for repo ID %s: %w", logRepoID, err)
	}

	plugin.loggers[logRepoID] = logger
	return logger, nil
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
	var traceName string
	var sessionID string
	var generationName string
	var tags map[string]string

	// Get effective log repo ID (header > default > skip)
	effectiveLogRepoID := plugin.getEffectiveLogRepoID(ctx)

	// If no log repo ID available, skip logging
	if effectiveLogRepoID == "" {
		return req, nil, nil
	}

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

		if existingTraceName, ok := (*ctx).Value(TraceNameKey).(string); ok && existingTraceName != "" {
			traceName = existingTraceName
		}

		if existingGenerationName, ok := (*ctx).Value(GenerationNameKey).(string); ok && existingGenerationName != "" {
			generationName = existingGenerationName
		}

		// retrieve all tags from context
		// the transport layer now stores all maxim tags in a single map
		if tagsValue := (*ctx).Value(TagsKey); tagsValue != nil {
			if tagsMap, ok := tagsValue.(map[string]string); ok {
				tags = make(map[string]string)
				for key, value := range tagsMap {
					tags[key] = value
				}
			}
		}
	}

	provider, model, _ := req.GetRequestFields()

	// Determine request type and set appropriate tags
	var messages []logging.CompletionRequest
	var latestMessage string

	// Initialize tags map if not already initialized from context
	if tags == nil {
		tags = make(map[string]string)
	}

	// Add model to tags
	tags["model"] = model

	modelParams := make(map[string]interface{})

	switch req.RequestType {
	case schemas.TextCompletionRequest, schemas.TextCompletionStreamRequest:
		messages = append(messages, logging.CompletionRequest{
			Role:    string(schemas.ChatMessageRoleUser),
			Content: req.TextCompletionRequest.Input,
		})
		if req.TextCompletionRequest.Input.PromptStr != nil {
			latestMessage = *req.TextCompletionRequest.Input.PromptStr
		} else {
			var stringBuilder strings.Builder
			for _, prompt := range req.TextCompletionRequest.Input.PromptArray {
				stringBuilder.WriteString(prompt)
			}
			latestMessage = stringBuilder.String()
		}

		if req.TextCompletionRequest.Params != nil {
			// Convert the struct to a map using reflection or JSON marshaling
			jsonData, err := json.Marshal(req.TextCompletionRequest.Params)
			if err == nil {
				json.Unmarshal(jsonData, &modelParams)
			}
		}
	case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest:
		for _, message := range req.ChatRequest.Input {
			messages = append(messages, logging.CompletionRequest{
				Role:    string(message.Role),
				Content: message.Content,
			})
		}
		if len(req.ChatRequest.Input) > 0 {
			lastMsg := req.ChatRequest.Input[len(req.ChatRequest.Input)-1]
			if lastMsg.Content.ContentStr != nil {
				latestMessage = *lastMsg.Content.ContentStr
			} else if lastMsg.Content.ContentBlocks != nil {
				// Find the last text content block
				for i := len(lastMsg.Content.ContentBlocks) - 1; i >= 0; i-- {
					block := (lastMsg.Content.ContentBlocks)[i]
					if block.Type == schemas.ChatContentBlockTypeText && block.Text != nil {
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

		if req.ChatRequest.Params != nil {
			// Convert the struct to a map using reflection or JSON marshaling
			jsonData, err := json.Marshal(req.ChatRequest.Params)
			if err == nil {
				json.Unmarshal(jsonData, &modelParams)
			}
		}
	case schemas.ResponsesRequest, schemas.ResponsesStreamRequest:
		for _, message := range req.ResponsesRequest.Input {
			if message.Content != nil {
				role := schemas.ChatMessageRoleUser
				if message.Role != nil {
					role = schemas.ChatMessageRole(*message.Role)
				}
				messages = append(messages, logging.CompletionRequest{
					Role:    string(role),
					Content: message.Content,
				})
			}
		}
		if len(req.ResponsesRequest.Input) > 0 {
			lastMsg := req.ResponsesRequest.Input[len(req.ResponsesRequest.Input)-1]
			// Initialize to placeholder in case content is missing or empty
			latestMessage = "-"

			// Check if Content is nil before accessing its fields
			if lastMsg.Content != nil {
				if lastMsg.Content.ContentStr != nil {
					latestMessage = *lastMsg.Content.ContentStr
				} else if lastMsg.Content.ContentBlocks != nil {
					// Find the last text content block
					for i := len(lastMsg.Content.ContentBlocks) - 1; i >= 0; i-- {
						block := (lastMsg.Content.ContentBlocks)[i]
						if block.Text != nil {
							latestMessage = *block.Text
							break
						}
					}
					// If no text block found, keep the placeholder
				}
			}
		}

		if req.ResponsesRequest.Params != nil {
			// Convert the struct to a map using reflection or JSON marshaling
			jsonData, err := json.Marshal(req.ResponsesRequest.Params)
			if err == nil {
				json.Unmarshal(jsonData, &modelParams)
			}
		}
	}

	if traceID == "" {
		// If traceID is not set, create a new trace
		traceID = uuid.New().String()
	}

	name := fmt.Sprintf("bifrost_%s", string(req.RequestType))
	if traceName != "" {
		name = traceName
	}

	traceConfig := logging.TraceConfig{
		Id:   traceID,
		Name: maxim.StrPtr(name),
		Tags: &tags,
	}

	if sessionID != "" {
		traceConfig.SessionId = &sessionID
	}

	// Create trace in the effective log repository
	logger, err := plugin.getOrCreateLogger(effectiveLogRepoID)
	if err != nil {
		return req, nil, fmt.Errorf("failed to create trace: %w", err)
	}

	trace := logger.Trace(&traceConfig)
	trace.SetInput(latestMessage)
	generationID := uuid.New().String()

	generationConfig := logging.GenerationConfig{
		Id:              generationID,
		Model:           model,
		Provider:        string(provider),
		Tags:            &tags,
		Messages:        messages,
		ModelParameters: modelParams,
	}

	if generationName != "" {
		generationConfig.Name = &generationName
	}

	// Add generation to the effective log repository
	logger.AddGenerationToTrace(traceID, &generationConfig)

	if ctx != nil {
		if _, ok := (*ctx).Value(TraceIDKey).(string); !ok {
			*ctx = context.WithValue(*ctx, TraceIDKey, traceID)
		}
		*ctx = context.WithValue(*ctx, GenerationIDKey, generationID)
	}

	// Extract request ID from context
	requestID, ok := (*ctx).Value(schemas.BifrostContextKeyRequestID).(string)
	if !ok || requestID == "" {
		// Log error but don't fail the request
		plugin.logger.Error("%s request id not found in context or is empty, please set schemas.BifrostContextKeyRequestID in ctx", PluginLoggerPrefix)
		return req, nil, nil
	}

	createdTimestamp := time.Now()
	if bifrost.IsStreamRequestType(req.RequestType) {
		plugin.accumulator.CreateStreamAccumulator(requestID, createdTimestamp)
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
//   - result: The Bifrost response to be traced
//   - bifrostErr: The BifrostError returned by the request, if any
//
// Returns:
//   - *schemas.BifrostResponse: The original response, unmodified
//   - *schemas.BifrostError: The original error, unmodified
//   - error: Never returns an error as it handles missing IDs gracefully
func (plugin *Plugin) PostHook(ctx *context.Context, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	requestType, _, _ := bifrost.GetResponseFields(result, bifrostErr)

	requestID, ok := (*ctx).Value(schemas.BifrostContextKeyRequestID).(string)
	if !ok || requestID == "" {
		return result, bifrostErr, nil
	}

	var streamResponse *streaming.ProcessedStreamResponse
	var err error
	if bifrost.IsStreamRequestType(requestType) {
		streamResponse, err = plugin.accumulator.ProcessStreamingResponse(ctx, result, bifrostErr)
		if err != nil {
			plugin.logger.Error("%s failed to process streaming response: %v", PluginLoggerPrefix, err)
			return result, bifrostErr, err
		}

		// Return the result if it is a delta response
		if streamResponse == nil || streamResponse.Type == streaming.StreamResponseTypeDelta {
			return result, bifrostErr, nil
		}
	}

	// Get effective log repo ID for this request
	effectiveLogRepoID := plugin.getEffectiveLogRepoID(ctx)
	if effectiveLogRepoID == "" {
		return result, bifrostErr, nil
	}
	logger, err := plugin.getOrCreateLogger(effectiveLogRepoID)
	if err != nil {
		return result, bifrostErr, nil
	}
	generationID, ok := (*ctx).Value(GenerationIDKey).(string)
	if ok {
		if bifrostErr != nil {
			genErr := logging.GenerationError{
				Message: bifrostErr.Error.Message,
				Code:    bifrostErr.Error.Code,
				Type:    bifrostErr.Error.Type,
			}
			logger.SetGenerationError(generationID, &genErr)

			if bifrost.IsStreamRequestType(requestType) {
				plugin.accumulator.CleanupStreamAccumulator(requestID)
			}
		} else if result != nil {
			switch requestType {
			case schemas.TextCompletionRequest, schemas.TextCompletionStreamRequest:
				if streamResponse != nil {
					logger.AddResultToGeneration(generationID, streamResponse.ToBifrostResponse().TextCompletionResponse)
				} else {
					logger.AddResultToGeneration(generationID, result.TextCompletionResponse)
				}
			case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest:
				if streamResponse != nil {
					logger.AddResultToGeneration(generationID, streamResponse.ToBifrostResponse().ChatResponse)
				} else {
					logger.AddResultToGeneration(generationID, result.ChatResponse)
				}
			case schemas.ResponsesRequest, schemas.ResponsesStreamRequest:
				if streamResponse != nil {
					logger.AddResultToGeneration(generationID, streamResponse.ToBifrostResponse().ResponsesResponse.ToBifrostChatResponse())
				} else {
					logger.AddResultToGeneration(generationID, result.ResponsesResponse.ToBifrostChatResponse())
				}
			}
		}
		logger.EndGeneration(generationID)
	}
	traceID, ok := (*ctx).Value(TraceIDKey).(string)
	if ok {
		logger.EndTrace(traceID)
	}
	// Flush only the effective logger that was used for this request
	logger.Flush()
	return result, bifrostErr, nil
}

func (plugin *Plugin) Cleanup() error {
	if plugin.accumulator != nil {
		plugin.accumulator.Cleanup()
	}
	// Flush all loggers
	plugin.loggerMutex.RLock()
	for _, logger := range plugin.loggers {
		logger.Flush()
	}
	plugin.loggerMutex.RUnlock()

	return nil
}
