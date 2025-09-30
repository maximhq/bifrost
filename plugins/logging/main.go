// Package logging provides a GORM-based logging plugin for Bifrost.
// This plugin stores comprehensive logs of all requests and responses with search,
// filter, and pagination capabilities.
package logging

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/pricing"
)

const (
	PluginName = "bifrost-http-logging"
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
	DroppedCreateContextKey ContextKey = "bifrost-logging-dropped"
	CreatedTimestampKey     ContextKey = "bifrost-logging-created-timestamp"
)

// UpdateLogData contains data for log entry updates
type UpdateLogData struct {
	Status              string
	TokenUsage          *schemas.LLMUsage
	Cost                *float64 // Cost in dollars from pricing plugin
	OutputMessage       *schemas.BifrostMessage
	EmbeddingOutput     *[]schemas.BifrostEmbedding
	ToolCalls           *[]schemas.ToolCall
	ErrorDetails        *schemas.BifrostError
	Model               string                     // May be different from request
	Object              string                     // May be different from request
	SpeechOutput        *schemas.BifrostSpeech     // For non-streaming speech responses
	TranscriptionOutput *schemas.BifrostTranscribe // For non-streaming transcription responses
}

// StreamUpdateData contains lightweight data for streaming delta updates
type StreamUpdateData struct {
	ErrorDetails        *schemas.BifrostError
	Model               string // May be different from request
	Object              string // May be different from request
	TokenUsage          *schemas.LLMUsage
	Cost                *float64                    // Cost in dollars from pricing plugin
	Delta               *schemas.BifrostStreamDelta // The actual streaming delta
	FinishReason        *string                     // If the stream is finished
	TranscriptionOutput *schemas.BifrostTranscribe  // For transcription stream responses
}

// LogMessage represents a message in the logging queue
type LogMessage struct {
	Operation          LogOperation
	RequestID          string
	Timestamp          time.Time                  // Of the preHook/postHook call
	InitialData        *InitialLogData            // For create operations
	SemanticCacheDebug *schemas.BifrostCacheDebug // For semantic cache operations
	UpdateData         *UpdateLogData             // For update operations
	StreamUpdateData   *StreamUpdateData          // For stream update operations
}

// InitialLogData contains data for initial log entry creation
type InitialLogData struct {
	Provider           string
	Model              string
	Object             string
	InputHistory       []schemas.BifrostMessage
	Params             *schemas.ModelParameters
	SpeechInput        *schemas.SpeechInput
	TranscriptionInput *schemas.TranscriptionInput
	Tools              *[]schemas.Tool
}

// LogCallback is a function that gets called when a new log entry is created
type LogCallback func(*logstore.Log)

// StreamChunk represents a single streaming chunk
type StreamChunk struct {
	Timestamp          time.Time                   // When chunk was received
	Delta              *schemas.BifrostStreamDelta // The actual delta content
	FinishReason       *string                     // If this is the final chunk
	TokenUsage         *schemas.LLMUsage           // Token usage if available
	SemanticCacheDebug *schemas.BifrostCacheDebug  // Semantic cache debug if available
	Cost               *float64                    // Cost in dollars from pricing plugin
	ErrorDetails       *schemas.BifrostError       // Error if any
}

// StreamAccumulator manages accumulation of streaming chunks
type StreamAccumulator struct {
	RequestID      string
	Chunks         []*StreamChunk
	IsComplete     bool
	FinalTimestamp time.Time
	Object         string // Store object type once for the entire stream
	mu             sync.Mutex
}

// LoggerPlugin implements the schemas.Plugin interface
type LoggerPlugin struct {
	store              logstore.LogStore
	pricingManager     *pricing.PricingManager
	mu                 sync.Mutex
	done               chan struct{}
	wg                 sync.WaitGroup
	logger             schemas.Logger
	logCallback        LogCallback
	droppedRequests    atomic.Int64
	cleanupTicker      *time.Ticker // Ticker for cleaning up old processing logs
	logMsgPool         sync.Pool    // Pool for reusing LogMessage structs
	updateDataPool     sync.Pool    // Pool for reusing UpdateLogData structs
	streamDataPool     sync.Pool    // Pool for reusing StreamUpdateData structs
	streamChunkPool    sync.Pool    // Pool for reusing StreamChunk structs
	streamAccumulators sync.Map     // Track accumulators by request ID (atomic)
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
func Init(logger schemas.Logger, logsStore logstore.LogStore, pricingManager *pricing.PricingManager) (*LoggerPlugin, error) {
	if logsStore == nil {
		return nil, fmt.Errorf("logs store cannot be nil")
	}
	if pricingManager == nil {
		logger.Warn("logging plugin requires pricing manager to calculate cost, all cost calculations will be skipped.")
	}

	plugin := &LoggerPlugin{
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
		streamDataPool: sync.Pool{
			New: func() interface{} {
				return &StreamUpdateData{}
			},
		},
		streamChunkPool: sync.Pool{
			New: func() interface{} {
				return &StreamChunk{}
			},
		},
		streamAccumulators: sync.Map{},
	}

	// Prewarm the pools for better performance at startup
	for range 1000 {
		plugin.logMsgPool.Put(&LogMessage{})
		plugin.updateDataPool.Put(&UpdateLogData{})
		plugin.streamDataPool.Put(&StreamUpdateData{})
		plugin.streamChunkPool.Put(&StreamChunk{})
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
	if err := p.store.CleanupLogs(fiveMinutesAgo); err != nil {
		p.logger.Error("failed to cleanup old processing logs: %v", err)
	}

	// Clean up old stream accumulators
	p.cleanupOldStreamAccumulators()
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

// PreHook is called before a request is processed - FULLY ASYNC, NO DATABASE I/O
func (p *LoggerPlugin) PreHook(ctx *context.Context, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.PluginShortCircuit, error) {
	if ctx == nil {
		// Log error but don't fail the request
		p.logger.Error("context is nil in PreHook")
		return req, nil, nil
	}

	// Extract request ID from context
	requestID, ok := (*ctx).Value(schemas.BifrostContextKey("request-id")).(string)
	if !ok || requestID == "" {
		// Log error but don't fail the request
		p.logger.Error("request-id not found in context or is empty")
		return req, nil, nil
	}

	requestType, ok := (*ctx).Value(schemas.BifrostContextKeyRequestType).(schemas.RequestType)
	if !ok {
		p.logger.Error("request type not found in context")
		return req, nil, nil
	}

	// Prepare initial log data
	objectType := p.determineObjectType(requestType)
	inputHistory := p.extractInputHistory(req.Input)

	initialData := &InitialLogData{
		Provider:           string(req.Provider),
		Model:              req.Model,
		Object:             objectType,
		InputHistory:       inputHistory,
		Params:             req.Params,
		SpeechInput:        req.Input.SpeechInput,
		TranscriptionInput: req.Input.TranscriptionInput,
	}

	if req.Params != nil && req.Params.Tools != nil {
		initialData.Tools = req.Params.Tools
	}

	// Store created timestamp in context for latency calculation optimization
	createdTimestamp := time.Now()
	*ctx = context.WithValue(*ctx, CreatedTimestampKey, createdTimestamp)

	// Queue the log creation message (non-blocking) - Using sync.Pool
	logMsg := p.getLogMessage()
	logMsg.Operation = LogOperationCreate
	logMsg.RequestID = requestID
	logMsg.Timestamp = createdTimestamp
	logMsg.InitialData = initialData

	go func(logMsg *LogMessage) {
		defer p.putLogMessage(logMsg) // Return to pool when done
		if err := p.insertInitialLogEntry(logMsg.RequestID, logMsg.Timestamp, logMsg.InitialData); err != nil {
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
func (p *LoggerPlugin) PostHook(ctx *context.Context, result *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if ctx == nil {
		// Log error but don't fail the request
		p.logger.Error("context is nil in PostHook")
		return result, err, nil
	}

	// Check if the create operation was dropped - if so, skip the update
	if dropped, ok := (*ctx).Value(DroppedCreateContextKey).(bool); ok && dropped {
		// Create was dropped, skip update to avoid wasted processing and errors
		return result, err, nil
	}

	// Extract request ID from context
	requestID, ok := (*ctx).Value(schemas.BifrostContextKey("request-id")).(string)
	if !ok || requestID == "" {
		// Log error but don't fail the request
		p.logger.Error("request-id not found in context or is empty")
		return result, err, nil
	}

	provider, ok := (*ctx).Value(schemas.BifrostContextKeyRequestProvider).(schemas.ModelProvider)
	if !ok {
		p.logger.Error("provider not found in context")
		return result, err, nil
	}

	model, ok := (*ctx).Value(schemas.BifrostContextKeyRequestModel).(string)
	if !ok {
		p.logger.Error("model not found in context")
		return result, err, nil
	}
	// Check if this is a streaming response
	requestType, ok := (*ctx).Value(schemas.BifrostContextKeyRequestType).(schemas.RequestType)
	if !ok {
		p.logger.Error("request type missing/invalid in PostHook for request %s", requestID)
		return result, err, nil
	}
	isAudioStreaming := requestType == schemas.SpeechStreamRequest || requestType == schemas.TranscriptionStreamRequest
	isChatStreaming := requestType == schemas.ChatCompletionStreamRequest

	// Queue the log update message (non-blocking) - use same pattern for both streaming and regular
	logMsg := p.getLogMessage()
	logMsg.RequestID = requestID
	logMsg.Timestamp = time.Now()

	isFinalChunk := bifrost.IsFinalChunk(ctx)

	if isChatStreaming {
		// Handle text-based streaming with ordered accumulation
		return p.handleStreamingResponse(ctx, result, err)
	} else if isAudioStreaming {
		// Handle speech/transcription streaming with original flow
		logMsg.Operation = LogOperationStreamUpdate

		// Prepare lightweight streaming update data
		streamUpdateData := p.getStreamUpdateData()

		if err != nil {
			// Error case
			streamUpdateData.ErrorDetails = err
		} else if result != nil {
			if result.Model != "" {
				streamUpdateData.Model = model
			}

			// Update object type if available
			if result.Object != "" {
				streamUpdateData.Object = result.Object
			}

			// Token usage
			if result.Usage != nil && result.Usage.TotalTokens > 0 {
				streamUpdateData.TokenUsage = result.Usage
			}

			// Extract token usage from speech and transcription streaming (lightweight)
			if result.Speech != nil && result.Speech.Usage != nil && streamUpdateData.TokenUsage == nil {
				streamUpdateData.TokenUsage = &schemas.LLMUsage{
					PromptTokens:     result.Speech.Usage.InputTokens,
					CompletionTokens: result.Speech.Usage.OutputTokens,
					TotalTokens:      result.Speech.Usage.TotalTokens,
				}
			}
			if result.Transcribe != nil && result.Transcribe.Usage != nil && streamUpdateData.TokenUsage == nil {
				transcriptionUsage := result.Transcribe.Usage
				streamUpdateData.TokenUsage = &schemas.LLMUsage{}

				if transcriptionUsage.InputTokens != nil {
					streamUpdateData.TokenUsage.PromptTokens = *transcriptionUsage.InputTokens
				}
				if transcriptionUsage.OutputTokens != nil {
					streamUpdateData.TokenUsage.CompletionTokens = *transcriptionUsage.OutputTokens
				}
				if transcriptionUsage.TotalTokens != nil {
					streamUpdateData.TokenUsage.TotalTokens = *transcriptionUsage.TotalTokens
				}
			}
			if result.Transcribe != nil && result.Transcribe.BifrostTranscribeStreamResponse != nil && result.Transcribe.Text != "" {
				streamUpdateData.TranscriptionOutput = result.Transcribe
			}
		}

		logMsg.StreamUpdateData = streamUpdateData
	} else {
		// Handle regular response
		logMsg.Operation = LogOperationUpdate

		// Prepare update data (latency will be calculated in background worker)
		updateData := p.getUpdateLogData()

		if err != nil {
			// Error case
			updateData.Status = "error"
			updateData.ErrorDetails = err
		} else if result != nil {
			// Success case
			updateData.Status = "success"

			if result.Model != "" {
				updateData.Model = model
			}

			// Update object type if available
			if result.Object != "" {
				updateData.Object = result.Object
			}

			// Token usage
			if result.Usage != nil && result.Usage.TotalTokens > 0 {
				updateData.TokenUsage = result.Usage
			}

			// Output message and tool calls
			if len(result.Choices) > 0 {
				choice := result.Choices[0]

				// Check if this is a non-stream response choice
				if choice.BifrostNonStreamResponseChoice != nil {
					updateData.OutputMessage = &choice.BifrostNonStreamResponseChoice.Message

					// Extract tool calls if present
					if choice.BifrostNonStreamResponseChoice.Message.AssistantMessage != nil &&
						choice.BifrostNonStreamResponseChoice.Message.AssistantMessage.ToolCalls != nil {
						updateData.ToolCalls = choice.BifrostNonStreamResponseChoice.Message.AssistantMessage.ToolCalls
					}
				}
			}

			if result.Data != nil {
				updateData.EmbeddingOutput = &result.Data
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
	}

	// Both streaming and regular updates now use the same async pattern
	go func() {
		defer p.putLogMessage(logMsg) // Return to pool when done

		// Return pooled data structures to their respective pools
		defer func() {
			if logMsg.UpdateData != nil {
				p.putUpdateLogData(logMsg.UpdateData)
			}
			if logMsg.StreamUpdateData != nil {
				p.putStreamUpdateData(logMsg.StreamUpdateData)
			}
		}()

		if result != nil {
			logMsg.SemanticCacheDebug = result.ExtraFields.CacheDebug
		}

		if logMsg.UpdateData != nil && p.pricingManager != nil {
			cost := p.pricingManager.CalculateCostWithCacheDebug(result, provider, model, requestType)
			logMsg.UpdateData.Cost = &cost
		}
		if logMsg.StreamUpdateData != nil && isFinalChunk && p.pricingManager != nil {
			cost := p.pricingManager.CalculateCostWithCacheDebug(result, provider, model, requestType)
			logMsg.StreamUpdateData.Cost = &cost
		}

		var processingErr error
		if logMsg.Operation == LogOperationStreamUpdate {
			processingErr = retryOnNotFound(*ctx, func() error {
				return p.processStreamUpdate(*ctx, logMsg.RequestID, logMsg.Timestamp, logMsg.SemanticCacheDebug, logMsg.StreamUpdateData, isFinalChunk)
			})
		} else {
			processingErr = retryOnNotFound(*ctx, func() error {
				return p.updateLogEntry(*ctx, logMsg.RequestID, logMsg.Timestamp, logMsg.SemanticCacheDebug, logMsg.UpdateData)
			})
		}
		if processingErr != nil {
			p.logger.Error("failed to process log update for request %s: %v", logMsg.RequestID, processingErr)
		} else {
			// Call callback immediately for both streaming and regular updates
			// UI will handle debouncing if needed
			p.mu.Lock()
			if p.logCallback != nil {
				if updatedEntry, getErr := p.getLogEntry(logMsg.RequestID); getErr == nil {
					p.logCallback(updatedEntry)
				}
			}
			p.mu.Unlock()
		}
	}()

	return result, err, nil
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

	// Clean up all stream accumulators
	p.streamAccumulators.Range(func(key, value interface{}) bool {
		acc := value.(*StreamAccumulator)
		for _, c := range acc.Chunks {
			p.putStreamChunk(c)
		}
		p.streamAccumulators.Delete(key)
		return true
	})

	// GORM handles connection cleanup automatically
	return nil
}

// Helper methods

// determineObjectType determines the object type from request input
func (p *LoggerPlugin) determineObjectType(requestType schemas.RequestType) string {
	switch requestType {
	case schemas.TextCompletionRequest:
		return "text.completion"
	case schemas.ChatCompletionRequest:
		return "chat.completion"
	case schemas.ChatCompletionStreamRequest:
		return "chat.completion.chunk"
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
func (p *LoggerPlugin) extractInputHistory(input schemas.RequestInput) []schemas.BifrostMessage {
	if input.ChatCompletionInput != nil {
		return *input.ChatCompletionInput
	}
	if input.TextCompletionInput != nil {
		// Convert text completion to message format
		return []schemas.BifrostMessage{
			{
				Role: schemas.ModelChatMessageRoleUser,
				Content: schemas.MessageContent{
					ContentStr: input.TextCompletionInput,
				},
			},
		}
	}
	if input.EmbeddingInput != nil {
		texts := input.EmbeddingInput.Texts

		if len(texts) == 0 && input.EmbeddingInput.Text != nil {
			texts = []string{*input.EmbeddingInput.Text}
		}

		contentBlocks := make([]schemas.ContentBlock, len(texts))
		for i, text := range texts {
			contentBlocks[i] = schemas.ContentBlock{
				Type: schemas.ContentBlockTypeText,
				Text: &text,
			}
		}
		return []schemas.BifrostMessage{
			{
				Role: schemas.ModelChatMessageRoleUser,
				Content: schemas.MessageContent{
					ContentBlocks: &contentBlocks,
				},
			},
		}
	}
	return []schemas.BifrostMessage{}
}
