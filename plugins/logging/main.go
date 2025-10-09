// Package logging provides a GORM-based logging plugin for Bifrost.
// This plugin stores comprehensive logs of all requests and responses with search,
// filter, and pagination capabilities.
package logging

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/pricing"
	"github.com/maximhq/bifrost/framework/streaming"
)

const (
	PluginName = "logging"
)

// ContextKey is a custom type for context keys to prevent collisions
type ContextKey string

// LogOperation represents the type of logging operation
type LogOperation string

const (
	LogOperationCreate       LogOperation = "create"
	LogOperationUpdate       LogOperation = "update"
	LogOperationStreamUpdate LogOperation = "stream_update"
)

// Context keys for logging optimization
const (
	DroppedCreateContextKey ContextKey = "logging-dropped"
	CreatedTimestampKey     ContextKey = "logging-created-timestamp"
)

// UpdateLogData contains data for log entry updates
type UpdateLogData struct {
	Status              string
	TokenUsage          *schemas.LLMUsage
	Cost                *float64 // Cost in dollars from pricing plugin
	OutputMessage       *schemas.ChatMessage
	EmbeddingOutput     []schemas.BifrostEmbedding
	ToolCalls           []schemas.ChatAssistantMessageToolCall
	ErrorDetails        *schemas.BifrostError
	Model               string                     // May be different from request
	Object              string                     // May be different from request
	SpeechOutput        *schemas.BifrostSpeech     // For non-streaming speech responses
	TranscriptionOutput *schemas.BifrostTranscribe // For non-streaming transcription responses
	RawResponse         interface{}
}

// LogMessage represents a message in the logging queue
type LogMessage struct {
	Operation          LogOperation
	RequestID          string                             // Unique ID for the request
	ParentRequestID    string                             // Unique ID for the parent request
	Timestamp          time.Time                          // Of the preHook/postHook call
	InitialData        *InitialLogData                    // For create operations
	SemanticCacheDebug *schemas.BifrostCacheDebug         // For semantic cache operations
	UpdateData         *UpdateLogData                     // For update operations
	StreamResponse     *streaming.ProcessedStreamResponse // For streaming delta updates
}

// InitialLogData contains data for initial log entry creation
type InitialLogData struct {
	Provider           string
	Model              string
	Object             string
	InputHistory       []schemas.ChatMessage
	Params             interface{}
	SpeechInput        *schemas.SpeechInput
	TranscriptionInput *schemas.TranscriptionInput
	Tools              []schemas.ChatTool
}

// LogCallback is a function that gets called when a new log entry is created
type LogCallback func(*logstore.Log)

// LoggerPlugin implements the schemas.Plugin interface
type LoggerPlugin struct {
	ctx             context.Context
	store           logstore.LogStore
	pricingManager  *pricing.PricingManager
	mu              sync.Mutex
	done            chan struct{}
	wg              sync.WaitGroup
	logger          schemas.Logger
	logCallback     LogCallback
	droppedRequests atomic.Int64
	cleanupTicker   *time.Ticker           // Ticker for cleaning up old processing logs
	logMsgPool      sync.Pool              // Pool for reusing LogMessage structs
	updateDataPool  sync.Pool              // Pool for reusing UpdateLogData structs
	accumulator     *streaming.Accumulator // Accumulator for streaming chunks
}

// retryOnNotFound retries a function up to 3 times with 1-second delays if it returns logstore.ErrNotFound
func retryOnNotFound(ctx context.Context, operation func() error) error {
	const maxRetries = 3
	const retryDelay = time.Second

	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		err := operation()
		if err == nil {
			return nil
		}

		// Check if the error is logstore.ErrNotFound
		if !errors.Is(err, logstore.ErrNotFound) {
			return err
		}

		lastErr = err

		// Don't wait after the last attempt
		if attempt < maxRetries-1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(retryDelay):
				// Continue to next retry
			}
		}
	}

	return lastErr
}

// Init creates new logger plugin with given log store
func Init(ctx context.Context, logger schemas.Logger, logsStore logstore.LogStore, pricingManager *pricing.PricingManager) (*LoggerPlugin, error) {
	if logsStore == nil {
		return nil, fmt.Errorf("logs store cannot be nil")
	}
	if pricingManager == nil {
		logger.Warn("logging plugin requires pricing manager to calculate cost, all cost calculations will be skipped.")
	}

	plugin := &LoggerPlugin{
		ctx:            ctx,
		store:          logsStore,
		pricingManager: pricingManager,
		done:           make(chan struct{}),
		logger:         logger,
		logMsgPool: sync.Pool{
			New: func() interface{} {
				return &LogMessage{}
			},
		},
		updateDataPool: sync.Pool{
			New: func() interface{} {
				return &UpdateLogData{}
			},
		},
		accumulator: streaming.NewAccumulator(pricingManager, logger),
	}

	// Prewarm the pools for better performance at startup
	for range 1000 {
		plugin.logMsgPool.Put(&LogMessage{})
		plugin.updateDataPool.Put(&UpdateLogData{})
	}

	// Start cleanup ticker (runs every 30 seconds)
	plugin.cleanupTicker = time.NewTicker(30 * time.Second)
	plugin.wg.Add(1)
	go plugin.cleanupWorker()

	return plugin, nil
}

// cleanupWorker periodically removes old processing logs
func (p *LoggerPlugin) cleanupWorker() {
	defer p.wg.Done()
	for {
		select {
		case <-p.cleanupTicker.C:
			p.cleanupOldProcessingLogs()
		case <-p.done:
			return
		}
	}
}

// cleanupOldProcessingLogs removes processing logs older than 5 minutes
func (p *LoggerPlugin) cleanupOldProcessingLogs() {
	// Calculate timestamp for 5 minutes ago
	fiveMinutesAgo := time.Now().Add(-1 * 5 * time.Minute)
	// Delete processing logs older than 5 minutes using the store
	if err := p.store.Flush(p.ctx, fiveMinutesAgo); err != nil {
		p.logger.Error("failed to cleanup old processing logs: %v", err)
	}
}

// SetLogCallback sets a callback function that will be called for each log entry
func (p *LoggerPlugin) SetLogCallback(callback LogCallback) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.logCallback = callback
}

// GetName returns the name of the plugin
func (p *LoggerPlugin) GetName() string {
	return PluginName
}

// TransportInterceptor is not used for this plugin
func (p *LoggerPlugin) TransportInterceptor(url string, headers map[string]string, body map[string]any) (map[string]string, map[string]any, error) {
	return headers, body, nil
}

// PreHook is called before a request is processed - FULLY ASYNC, NO DATABASE I/O
func (p *LoggerPlugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
	if ctx == nil {
		// Log error but don't fail the request
		p.logger.Error("context is nil in PreHook")
		return req, nil, nil
	}

	// Extract request ID from context
	requestID, ok := (*ctx).Value(schemas.BifrostContextKeyRequestID).(string)
	if !ok || requestID == "" {
		// Log error but don't fail the request
		p.logger.Error("request-id not found in context or is empty")
		return req, nil, nil
	}

	createdTimestamp := time.Now()
	// If request type is streaming we create a stream accumulator
	if bifrost.IsStreamRequestType(req.RequestType) {
		p.accumulator.CreateStreamAccumulator(requestID, createdTimestamp)
	}
	// Prepare initial log data
	objectType := p.determineObjectType(req.RequestType)
	inputHistory := p.extractInputHistory(req)

	initialData := &InitialLogData{
		Provider:     string(req.Provider),
		Model:        req.Model,
		Object:       objectType,
		InputHistory: inputHistory,
	}

	switch req.RequestType {
	case schemas.TextCompletionRequest, schemas.TextCompletionStreamRequest:
		initialData.Params = req.TextCompletionRequest.Params
	case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest:
		initialData.Params = req.ChatRequest.Params
		if req.ChatRequest.Params != nil && req.ChatRequest.Params.Tools != nil {
			initialData.Tools = req.ChatRequest.Params.Tools
		}
	case schemas.ResponsesRequest, schemas.ResponsesStreamRequest:
		initialData.Params = req.ResponsesRequest.Params

		if req.ResponsesRequest.Params != nil && req.ResponsesRequest.Params.Tools != nil {
			var tools []schemas.ChatTool
			for _, tool := range req.ResponsesRequest.Params.Tools {
				tools = append(tools, *tool.ToChatTool())
			}
			initialData.Tools = tools
		}
	case schemas.EmbeddingRequest:
		initialData.Params = req.EmbeddingRequest.Params
	case schemas.SpeechRequest, schemas.SpeechStreamRequest:
		initialData.Params = req.SpeechRequest.Params
		initialData.SpeechInput = req.SpeechRequest.Input
	case schemas.TranscriptionRequest, schemas.TranscriptionStreamRequest:
		initialData.Params = req.TranscriptionRequest.Params
		initialData.TranscriptionInput = req.TranscriptionRequest.Input
	}
	*ctx = context.WithValue(*ctx, CreatedTimestampKey, createdTimestamp)
	// Queue the log creation message (non-blocking) - Using sync.Pool
	logMsg := p.getLogMessage()
	logMsg.Operation = LogOperationCreate

	// If fallback request ID is present, use it instead of the primary request ID
	fallbackRequestID, ok := (*ctx).Value(schemas.BifrostContextKeyFallbackRequestID).(string)
	if ok && fallbackRequestID != "" {
		logMsg.RequestID = fallbackRequestID
		logMsg.ParentRequestID = requestID
	} else {
		logMsg.RequestID = requestID
	}

	logMsg.Timestamp = createdTimestamp
	logMsg.InitialData = initialData

	go func(logMsg *LogMessage) {
		defer p.putLogMessage(logMsg) // Return to pool when done
		if err := p.insertInitialLogEntry(p.ctx, logMsg.RequestID, logMsg.ParentRequestID, logMsg.Timestamp, logMsg.InitialData); err != nil {
			p.logger.Error("failed to insert initial log entry for request %s: %v", logMsg.RequestID, err)
		} else {
			// Call callback for initial log creation (WebSocket "create" message)
			// Construct LogEntry directly from data we have to avoid database query
			p.mu.Lock()
			if p.logCallback != nil {
				initialEntry := &logstore.Log{
					ID:                 logMsg.RequestID,
					Timestamp:          logMsg.Timestamp,
					Object:             logMsg.InitialData.Object,
					Provider:           logMsg.InitialData.Provider,
					Model:              logMsg.InitialData.Model,
					InputHistoryParsed: logMsg.InitialData.InputHistory,
					ParamsParsed:       logMsg.InitialData.Params,
					ToolsParsed:        logMsg.InitialData.Tools,
					Status:             "processing",
					Stream:             false, // Initially false, will be updated if streaming
					CreatedAt:          logMsg.Timestamp,
				}
				p.logCallback(initialEntry)
			}
			p.mu.Unlock()
		}
	}(logMsg)

	return req, nil, nil
}

// PostHook is called after a response is received - FULLY ASYNC, NO DATABASE I/O
func (p *LoggerPlugin) PostHook(ctx *context.Context, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	p.logger.Debug("running post-hook for plugin logging")
	if ctx == nil {
		// Log error but don't fail the request
		p.logger.Error("context is nil in PostHook")
		return result, bifrostErr, nil
	}
	// Check if the create operation was dropped - if so, skip the update
	if dropped, ok := (*ctx).Value(DroppedCreateContextKey).(bool); ok && dropped {
		// Create was dropped, skip update to avoid wasted processing and errors
		return result, bifrostErr, nil
	}
	requestID, ok := (*ctx).Value(schemas.BifrostContextKeyRequestID).(string)
	if !ok || requestID == "" {
		p.logger.Error("request-id not found in context or is empty")
		return result, bifrostErr, nil
	}
	// If fallback request ID is present, use it instead of the primary request ID
	fallbackRequestID, ok := (*ctx).Value(schemas.BifrostContextKeyFallbackRequestID).(string)
	if ok && fallbackRequestID != "" {
		requestID = fallbackRequestID
	}
	requestType, _, _ := bifrost.GetRequestFields(result, bifrostErr)
	// Queue the log update message (non-blocking) - use same pattern for both streaming and regular
	logMsg := p.getLogMessage()
	logMsg.RequestID = requestID
	logMsg.Timestamp = time.Now()
	// If response is nil, and there is an error, we update log with error
	if result == nil && bifrostErr != nil {
		// If request type is streaming, then we trigger cleanup as well
		if bifrost.IsStreamRequestType(requestType) {
			p.accumulator.CleanupStreamAccumulator(requestID)
		}
		logMsg.Operation = LogOperationUpdate
		logMsg.UpdateData = &UpdateLogData{
			Status:       "error",
			ErrorDetails: bifrostErr,
		}
		processingErr := retryOnNotFound(p.ctx, func() error {
			return p.updateLogEntry(p.ctx, logMsg.RequestID, logMsg.Timestamp, logMsg.SemanticCacheDebug, logMsg.UpdateData)
		})
		if processingErr != nil {
			p.logger.Error("failed to process log update for request %s: %v", logMsg.RequestID, processingErr)
		} else {
			// Call callback immediately for both streaming and regular updates
			// UI will handle debouncing if needed
			p.mu.Lock()
			if p.logCallback != nil {
				if updatedEntry, getErr := p.getLogEntry(p.ctx, logMsg.RequestID); getErr == nil {
					p.logCallback(updatedEntry)
				}
			}
			p.mu.Unlock()
		}

		return result, bifrostErr, nil
	}
	if bifrost.IsStreamRequestType(requestType) {
		p.logger.Debug("[logging] processing streaming response")
		streamResponse, err := p.accumulator.ProcessStreamingResponse(ctx, result, bifrostErr)
		if err != nil {
			p.logger.Error("failed to process streaming response: %v", err)
			return result, bifrostErr, err
		}
		if streamResponse != nil && streamResponse.Type == streaming.StreamResponseTypeFinal {
			// Prepare final log data
			logMsg.Operation = LogOperationStreamUpdate
			logMsg.StreamResponse = streamResponse
			go func() {
				defer p.putLogMessage(logMsg) // Return to pool when done
				processingErr := retryOnNotFound(p.ctx, func() error {
					return p.updateStreamingLogEntry(p.ctx, logMsg.RequestID, logMsg.Timestamp, logMsg.SemanticCacheDebug, logMsg.StreamResponse, streamResponse.Type == streaming.StreamResponseTypeFinal)
				})
				if processingErr != nil {
					p.logger.Error("failed to process stream update for request %s: %v", logMsg.RequestID, processingErr)
				} else {
					// Call callback immediately for both streaming and regular updates
					// UI will handle debouncing if needed
					p.mu.Lock()
					if p.logCallback != nil {
						if updatedEntry, getErr := p.getLogEntry(p.ctx, logMsg.RequestID); getErr == nil {
							p.logCallback(updatedEntry)
						}
					}
					p.mu.Unlock()
				}
			}()
		}

	} else {
		// Handle regular response
		logMsg.Operation = LogOperationUpdate
		// Prepare update data (latency will be calculated in background worker)
		updateData := p.getUpdateLogData()
		if bifrostErr != nil {
			// Error case
			updateData.Status = "error"
			updateData.ErrorDetails = bifrostErr
		} else if result != nil {
			// Success case
			updateData.Status = "success"
			if result.Model != "" {
				updateData.Model = result.Model
			}
			// Update object type if available
			if result.Object != "" {
				updateData.Object = result.Object
			}
			// Token usage
			if result.Usage != nil && result.Usage.TotalTokens > 0 {
				updateData.TokenUsage = result.Usage
			}
			if result.ExtraFields.RawResponse != nil {
				updateData.RawResponse = result.ExtraFields.RawResponse
			}
			// Output message and tool calls
			if len(result.Choices) > 0 {
				choice := result.Choices[0]
				if choice.BifrostTextCompletionResponseChoice != nil {
					updateData.OutputMessage = &schemas.ChatMessage{
						Role: schemas.ChatMessageRoleAssistant,
						Content: &schemas.ChatMessageContent{
							ContentStr: choice.BifrostTextCompletionResponseChoice.Text,
						},
					}
				}
				// Check if this is a non-stream response choice
				if choice.BifrostNonStreamResponseChoice != nil {
					updateData.OutputMessage = choice.BifrostNonStreamResponseChoice.Message
					// Extract tool calls if present
					if choice.BifrostNonStreamResponseChoice.Message.ChatAssistantMessage != nil &&
						choice.BifrostNonStreamResponseChoice.Message.ChatAssistantMessage.ToolCalls != nil {
						updateData.ToolCalls = choice.BifrostNonStreamResponseChoice.Message.ChatAssistantMessage.ToolCalls
					}
				}
			}
			if result.ResponsesResponse != nil {
				outputMessages := result.ResponsesResponse.Output
				if len(outputMessages) > 0 {
					chatMessages := schemas.ToChatMessages(outputMessages)
					if len(chatMessages) > 0 {
						lastMessage := chatMessages[len(chatMessages)-1]
						updateData.OutputMessage = &lastMessage

						// Extract tool calls if present
						if lastMessage.ChatAssistantMessage != nil &&
							lastMessage.ChatAssistantMessage.ToolCalls != nil {
							updateData.ToolCalls = lastMessage.ChatAssistantMessage.ToolCalls
						}
					}
				}
			}
			if result.Data != nil {
				updateData.EmbeddingOutput = result.Data
			}
			// Handle speech and transcription outputs for NON-streaming responses
			if result.Speech != nil {
				updateData.SpeechOutput = result.Speech
				// Extract token usage
				if result.Speech.Usage != nil && updateData.TokenUsage == nil {
					updateData.TokenUsage = &schemas.LLMUsage{
						PromptTokens:     result.Speech.Usage.InputTokens,
						CompletionTokens: result.Speech.Usage.OutputTokens,
						TotalTokens:      result.Speech.Usage.TotalTokens,
					}
				}
			}
			if result.Transcribe != nil {
				updateData.TranscriptionOutput = result.Transcribe
				// Extract token usage
				if result.Transcribe.Usage != nil && updateData.TokenUsage == nil {
					transcriptionUsage := result.Transcribe.Usage
					updateData.TokenUsage = &schemas.LLMUsage{}

					if transcriptionUsage.InputTokens != nil {
						updateData.TokenUsage.PromptTokens = *transcriptionUsage.InputTokens
					}
					if transcriptionUsage.OutputTokens != nil {
						updateData.TokenUsage.CompletionTokens = *transcriptionUsage.OutputTokens
					}
					if transcriptionUsage.TotalTokens != nil {
						updateData.TokenUsage.TotalTokens = *transcriptionUsage.TotalTokens
					}
				}
			}
		}
		logMsg.UpdateData = updateData
		go func() {
			defer p.putLogMessage(logMsg) // Return to pool when done
			// Return pooled data structures to their respective pools
			defer func() {
				if logMsg.UpdateData != nil {
					p.putUpdateLogData(logMsg.UpdateData)
				}
			}()
			if result != nil {
				logMsg.SemanticCacheDebug = result.ExtraFields.CacheDebug
			}
			if logMsg.UpdateData != nil && p.pricingManager != nil {
				cost := p.pricingManager.CalculateCostWithCacheDebug(result)
				logMsg.UpdateData.Cost = &cost
			}
			// Here we pass plugin level context for background processing to avoid context cancellation
			processingErr := retryOnNotFound(p.ctx, func() error {
				return p.updateLogEntry(p.ctx, logMsg.RequestID, logMsg.Timestamp, logMsg.SemanticCacheDebug, logMsg.UpdateData)
			})
			if processingErr != nil {
				p.logger.Error("failed to process log update for request %s: %v", logMsg.RequestID, processingErr)
			} else {
				// Call callback immediately for both streaming and regular updates
				// UI will handle debouncing if needed
				p.mu.Lock()
				if p.logCallback != nil {
					if updatedEntry, getErr := p.getLogEntry(p.ctx, logMsg.RequestID); getErr == nil {
						p.logCallback(updatedEntry)
					}
				}
				p.mu.Unlock()
			}
		}()
	}
	return result, bifrostErr, nil
}

// Cleanup is called when the plugin is being shut down
func (p *LoggerPlugin) Cleanup() error {
	// Stop the cleanup ticker
	if p.cleanupTicker != nil {
		p.cleanupTicker.Stop()
	}
	// Signal the background worker to stop
	close(p.done)
	// Wait for the background worker to finish processing remaining items
	p.wg.Wait()
	p.accumulator.Cleanup()
	// GORM handles connection cleanup automatically
	return nil
}

// Helper methods

// determineObjectType determines the object type from request input
func (p *LoggerPlugin) determineObjectType(requestType schemas.RequestType) string {
	switch requestType {
	case schemas.TextCompletionRequest, schemas.TextCompletionStreamRequest:
		return "text.completion"
	case schemas.ChatCompletionRequest:
		return "chat.completion"
	case schemas.ChatCompletionStreamRequest:
		return "chat.completion.chunk"
	case schemas.ResponsesRequest:
		return "response"
	case schemas.ResponsesStreamRequest:
		return "response.completion.chunk"
	case schemas.EmbeddingRequest:
		return "list"
	case schemas.SpeechRequest:
		return "audio.speech"
	case schemas.SpeechStreamRequest:
		return "audio.speech.chunk"
	case schemas.TranscriptionRequest:
		return "audio.transcription"
	case schemas.TranscriptionStreamRequest:
		return "audio.transcription.chunk"
	}
	return "unknown"
}

// extractInputHistory extracts input history from request input
// extractInputHistory extracts input history from request input
func (p *LoggerPlugin) extractInputHistory(request *schemas.BifrostRequest) []schemas.ChatMessage {
	if request.ChatRequest != nil {
		return request.ChatRequest.Input
	}
	if request.ResponsesRequest != nil {
		messages := schemas.ToChatMessages(request.ResponsesRequest.Input)
		if len(messages) > 0 {
			return messages
		}
	}
	if request.TextCompletionRequest != nil {
		var text string
		if request.TextCompletionRequest.Input.PromptStr != nil {
			text = *request.TextCompletionRequest.Input.PromptStr
		} else {
			var stringBuilder strings.Builder
			for _, prompt := range request.TextCompletionRequest.Input.PromptArray {
				stringBuilder.WriteString(prompt)
			}
			text = stringBuilder.String()
		}
		return []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentStr: &text,
				},
			},
		}
	}
	if request.EmbeddingRequest != nil {
		texts := request.EmbeddingRequest.Input.Texts

		if len(texts) == 0 && request.EmbeddingRequest.Input.Text != nil {
			texts = []string{*request.EmbeddingRequest.Input.Text}
		}

		contentBlocks := make([]schemas.ChatContentBlock, len(texts))
		for i, text := range texts {
			// Create a per-iteration copy to avoid reusing the same memory address
			t := text
			contentBlocks[i] = schemas.ChatContentBlock{
				Type: schemas.ChatContentBlockTypeText,
				Text: &t,
			}
		}
		return []schemas.ChatMessage{
			{
				Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{
					ContentBlocks: contentBlocks,
				},
			},
		}
	}
	return []schemas.ChatMessage{}
}
