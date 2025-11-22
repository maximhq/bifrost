// Package logging provides a GORM-based logging plugin for Bifrost.
// This plugin stores comprehensive logs of all requests and responses with search,
// filter, and pagination capabilities.
package logging

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/streaming"
)

const (
	PluginName = "logging"
)

// LogOperation represents the type of logging operation
type LogOperation string

const (
	LogOperationCreate       LogOperation = "create"
	LogOperationUpdate       LogOperation = "update"
	LogOperationStreamUpdate LogOperation = "stream_update"
)

// UpdateLogData contains data for log entry updates
type UpdateLogData struct {
	Status              string
	TokenUsage          *schemas.BifrostLLMUsage
	Cost                *float64 // Cost in dollars from pricing plugin
	ChatOutput          *schemas.ChatMessage
	ResponsesOutput     []schemas.ResponsesMessage
	EmbeddingOutput     []schemas.EmbeddingData
	ErrorDetails        *schemas.BifrostError
	SpeechOutput        *schemas.BifrostSpeechResponse        // For non-streaming speech responses
	TranscriptionOutput *schemas.BifrostTranscriptionResponse // For non-streaming transcription responses
	RawResponse         interface{}
}

// LogMessage represents a message in the logging queue
type LogMessage struct {
	Operation          LogOperation
	RequestID          string                             // Unique ID for the request
	ParentRequestID    string                             // Unique ID for the parent request
	NumberOfRetries    int                                // Number of retries
	FallbackIndex      int                                // Fallback index
	SelectedKeyID      string                             // Selected key ID
	SelectedKeyName    string                             // Selected key name
	VirtualKeyID       string                             // Virtual key ID
	VirtualKeyName     string                             // Virtual key name
	Timestamp          time.Time                          // Of the preHook/postHook call
	Latency            int64                              // For latency updates
	InitialData        *InitialLogData                    // For create operations
	SemanticCacheDebug *schemas.BifrostCacheDebug         // For semantic cache operations
	UpdateData         *UpdateLogData                     // For update operations
	StreamResponse     *streaming.ProcessedStreamResponse // For streaming delta updates
}

// InitialLogData contains data for initial log entry creation
type InitialLogData struct {
	Provider              string
	Model                 string
	Object                string
	InputHistory          []schemas.ChatMessage
	ResponsesInputHistory []schemas.ResponsesMessage
	Params                interface{}
	SpeechInput           *schemas.SpeechInput
	TranscriptionInput    *schemas.TranscriptionInput
	Tools                 []schemas.ChatTool
}

// LogCallback is a function that gets called when a new log entry is created
type LogCallback func(*logstore.Log)

type Config struct {
	DisableContentLogging *bool `json:"disable_content_logging"`
}

// LoggerPlugin implements the schemas.Plugin interface
type LoggerPlugin struct {
	ctx                   context.Context
	store                 logstore.LogStore
	disableContentLogging *bool
	pricingManager        *modelcatalog.ModelCatalog
	mu                    sync.Mutex
	done                  chan struct{}
	wg                    sync.WaitGroup
	logger                schemas.Logger
	logCallback           LogCallback
	droppedRequests       atomic.Int64
	cleanupTicker         *time.Ticker           // Ticker for cleaning up old processing logs
	logMsgPool            sync.Pool              // Pool for reusing LogMessage structs
	updateDataPool        sync.Pool              // Pool for reusing UpdateLogData structs
	accumulator           *streaming.Accumulator // Accumulator for streaming chunks
}

// Init creates new logger plugin with given log store
func Init(ctx context.Context, config *Config, logger schemas.Logger, logsStore logstore.LogStore, pricingManager *modelcatalog.ModelCatalog) (*LoggerPlugin, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}
	if logsStore == nil {
		return nil, fmt.Errorf("logs store cannot be nil")
	}
	if pricingManager == nil {
		logger.Warn("logging plugin requires model catalog to calculate cost, all cost calculations will be skipped.")
	}

	plugin := &LoggerPlugin{
		ctx:                   ctx,
		store:                 logsStore,
		pricingManager:        pricingManager,
		disableContentLogging: config.DisableContentLogging,
		done:                  make(chan struct{}),
		logger:                logger,
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

	// Start cleanup ticker (runs every 1 minute)
	plugin.cleanupTicker = time.NewTicker(1 * time.Minute)
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

// cleanupOldProcessingLogs removes processing logs older than 30 minutes
func (p *LoggerPlugin) cleanupOldProcessingLogs() {
	// Calculate timestamp for 30 minutes ago in UTC to match log entry timestamps
	thirtyMinutesAgo := time.Now().UTC().Add(-1 * 30 * time.Minute)
	p.logger.Debug("cleaning up old processing logs before %s", thirtyMinutesAgo)
	// Delete processing logs older than 30 minutes using the store
	if err := p.store.Flush(p.ctx, thirtyMinutesAgo); err != nil {
		p.logger.Warn("failed to cleanup old processing logs: %v", err)
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
func (p *LoggerPlugin) TransportInterceptor(ctx *context.Context, url string, headers map[string]string, body map[string]any) (map[string]string, map[string]any, error) {
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

	createdTimestamp := time.Now().UTC()

	// If request type is streaming we create a stream accumulator
	if bifrost.IsStreamRequestType(req.RequestType) {
		p.accumulator.CreateStreamAccumulator(requestID, createdTimestamp)
	}

	provider, model, _ := req.GetRequestFields()

	initialData := &InitialLogData{
		Provider: string(provider),
		Model:    model,
		Object:   string(req.RequestType),
	}

	if p.disableContentLogging == nil || !*p.disableContentLogging {
		inputHistory, responsesInputHistory := p.extractInputHistory(req)
		initialData.InputHistory = inputHistory
		initialData.ResponsesInputHistory = responsesInputHistory

		switch req.RequestType {
		case schemas.TextCompletionRequest, schemas.TextCompletionStreamRequest:
			initialData.Params = req.TextCompletionRequest.Params
		case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest:
			initialData.Params = req.ChatRequest.Params
			initialData.Tools = req.ChatRequest.Params.Tools
		case schemas.ResponsesRequest, schemas.ResponsesStreamRequest:
			initialData.Params = req.ResponsesRequest.Params

			var tools []schemas.ChatTool
			for _, tool := range req.ResponsesRequest.Params.Tools {
				tools = append(tools, *tool.ToChatTool())
			}
			initialData.Tools = tools
		case schemas.EmbeddingRequest:
			initialData.Params = req.EmbeddingRequest.Params
		case schemas.SpeechRequest, schemas.SpeechStreamRequest:
			initialData.Params = req.SpeechRequest.Params
			initialData.SpeechInput = req.SpeechRequest.Input
		case schemas.TranscriptionRequest, schemas.TranscriptionStreamRequest:
			initialData.Params = req.TranscriptionRequest.Params
			initialData.TranscriptionInput = req.TranscriptionRequest.Input
		}
	}

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

	numberOfRetries := getIntFromContext(*ctx, schemas.BifrostContextKeyNumberOfRetries)
	fallbackIndex := getIntFromContext(*ctx, schemas.BifrostContextKeyFallbackIndex)

	logMsg.Timestamp = createdTimestamp
	logMsg.InitialData = initialData
	logMsg.NumberOfRetries = numberOfRetries
	logMsg.FallbackIndex = fallbackIndex

	go func(msg *LogMessage) {
		defer p.putLogMessage(msg) // Return to pool when done
		if err := p.insertInitialLogEntry(
			p.ctx,
			msg.RequestID,
			msg.ParentRequestID,
			msg.Timestamp,
			msg.NumberOfRetries,
			msg.FallbackIndex,
			msg.InitialData,
		); err != nil {
			p.logger.Warn("failed to insert initial log entry for request %s: %v", msg.RequestID, err)
		} else {
			// Call callback for initial log creation (WebSocket "create" message)
			// Construct LogEntry directly from data we have to avoid database query
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.logCallback != nil {
				initialEntry := &logstore.Log{
					ID:                          msg.RequestID,
					Timestamp:                   msg.Timestamp,
					Object:                      msg.InitialData.Object,
					Provider:                    msg.InitialData.Provider,
					Model:                       msg.InitialData.Model,
					NumberOfRetries:             msg.NumberOfRetries,
					FallbackIndex:               msg.FallbackIndex,
					InputHistoryParsed:          msg.InitialData.InputHistory,
					ResponsesInputHistoryParsed: msg.InitialData.ResponsesInputHistory,
					ParamsParsed:                msg.InitialData.Params,
					ToolsParsed:                 msg.InitialData.Tools,
					Status:                      "processing",
					Stream:                      false, // Initially false, will be updated if streaming
					CreatedAt:                   msg.Timestamp,
				}
				p.logCallback(initialEntry)
			}
		}
	}(logMsg)

	return req, nil, nil
}

// PostHook is called after a response is received - FULLY ASYNC, NO DATABASE I/O
func (p *LoggerPlugin) PostHook(ctx *context.Context, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if ctx == nil {
		// Log error but don't fail the request
		p.logger.Error("context is nil in PostHook")
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
	selectedKeyID := getStringFromContext(*ctx, schemas.BifrostContextKeySelectedKeyID)
	selectedKeyName := getStringFromContext(*ctx, schemas.BifrostContextKeySelectedKeyName)
	virtualKeyID := getStringFromContext(*ctx, schemas.BifrostContextKey("bf-governance-virtual-key-id"))
	virtualKeyName := getStringFromContext(*ctx, schemas.BifrostContextKey("bf-governance-virtual-key-name"))

	go func() {
		requestType, _, _ := bifrost.GetResponseFields(result, bifrostErr)
		// Queue the log update message (non-blocking) - use same pattern for both streaming and regular
		logMsg := p.getLogMessage()
		logMsg.RequestID = requestID
		logMsg.SelectedKeyID = selectedKeyID
		logMsg.VirtualKeyID = virtualKeyID
		logMsg.SelectedKeyName = selectedKeyName
		logMsg.VirtualKeyName = virtualKeyName
		defer p.putLogMessage(logMsg) // Return to pool when done

		if result != nil {
			logMsg.Latency = result.GetExtraFields().Latency
		} else {
			logMsg.Latency = 0
		}

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
				return p.updateLogEntry(
					p.ctx,
					logMsg.RequestID,
					logMsg.SelectedKeyID,
					logMsg.SelectedKeyName,
					logMsg.Latency,
					logMsg.VirtualKeyID,
					logMsg.VirtualKeyName,
					logMsg.SemanticCacheDebug,
					logMsg.UpdateData,
				)
			})
			if processingErr != nil {
				p.logger.Warn("failed to process log update for request %s: %v", logMsg.RequestID, processingErr)
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

			return
		}
		if bifrost.IsStreamRequestType(requestType) {
			p.logger.Debug("[logging] processing streaming response")

			streamResponse, err := p.accumulator.ProcessStreamingResponse(ctx, result, bifrostErr)
			if err != nil {
				p.logger.Debug("failed to process streaming response: %v", err)
			} else if streamResponse != nil && streamResponse.Type == streaming.StreamResponseTypeFinal {
				// Prepare final log data
				logMsg.Operation = LogOperationStreamUpdate
				logMsg.StreamResponse = streamResponse
				processingErr := retryOnNotFound(p.ctx, func() error {
					return p.updateStreamingLogEntry(
						p.ctx,
						logMsg.RequestID,
						logMsg.SelectedKeyID,
						logMsg.SelectedKeyName,
						logMsg.VirtualKeyID,
						logMsg.VirtualKeyName,
						logMsg.SemanticCacheDebug,
						logMsg.StreamResponse,
						streamResponse.Type == streaming.StreamResponseTypeFinal,
					)
				})
				if processingErr != nil {
					p.logger.Warn("failed to process stream update for request %s: %v", logMsg.RequestID, processingErr)
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
				// Token usage
				var usage *schemas.BifrostLLMUsage
				switch {
				case result.TextCompletionResponse != nil && result.TextCompletionResponse.Usage != nil:
					usage = result.TextCompletionResponse.Usage
				case result.ChatResponse != nil && result.ChatResponse.Usage != nil:
					usage = result.ChatResponse.Usage
				case result.ResponsesResponse != nil && result.ResponsesResponse.Usage != nil:
					usage = result.ResponsesResponse.Usage.ToBifrostLLMUsage()
				case result.EmbeddingResponse != nil && result.EmbeddingResponse.Usage != nil:
					usage = result.EmbeddingResponse.Usage
				case result.TranscriptionResponse != nil && result.TranscriptionResponse.Usage != nil:
					usage = &schemas.BifrostLLMUsage{}
					if result.TranscriptionResponse.Usage.InputTokens != nil {
						usage.PromptTokens = *result.TranscriptionResponse.Usage.InputTokens
					}
					if result.TranscriptionResponse.Usage.OutputTokens != nil {
						usage.CompletionTokens = *result.TranscriptionResponse.Usage.OutputTokens
					}
					if result.TranscriptionResponse.Usage.TotalTokens != nil {
						usage.TotalTokens = *result.TranscriptionResponse.Usage.TotalTokens
					} else {
						usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
					}
				}
				updateData.TokenUsage = usage
				// Extract raw response
				extraFields := result.GetExtraFields()
				if p.disableContentLogging == nil || !*p.disableContentLogging {
					if extraFields.RawResponse != nil {
						updateData.RawResponse = extraFields.RawResponse
					}
					if result.TextCompletionResponse != nil {
						if len(result.TextCompletionResponse.Choices) > 0 {
							choice := result.TextCompletionResponse.Choices[0]
							if choice.TextCompletionResponseChoice != nil {
								updateData.ChatOutput = &schemas.ChatMessage{
									Role: schemas.ChatMessageRoleAssistant,
									Content: &schemas.ChatMessageContent{
										ContentStr: choice.TextCompletionResponseChoice.Text,
									},
								}
							}
						}
					}
					if result.ChatResponse != nil {
						// Output message and tool calls
						if len(result.ChatResponse.Choices) > 0 {
							choice := result.ChatResponse.Choices[0]
							// Check if this is a non-stream response choice
							if choice.ChatNonStreamResponseChoice != nil {
								updateData.ChatOutput = choice.ChatNonStreamResponseChoice.Message
							}
						}
					}
					if result.ResponsesResponse != nil {
						updateData.ResponsesOutput = result.ResponsesResponse.Output
					}
					if result.EmbeddingResponse != nil && len(result.EmbeddingResponse.Data) > 0 {
						updateData.EmbeddingOutput = result.EmbeddingResponse.Data
					}
					// Handle speech and transcription outputs for NON-streaming responses
					if result.SpeechResponse != nil {
						updateData.SpeechOutput = result.SpeechResponse
					}
					if result.TranscriptionResponse != nil {
						updateData.TranscriptionOutput = result.TranscriptionResponse
					}
				}
			}
			logMsg.UpdateData = updateData

			// Return pooled data structures to their respective pools
			defer func() {
				if logMsg.UpdateData != nil {
					p.putUpdateLogData(logMsg.UpdateData)
				}
			}()
			if result != nil {
				logMsg.SemanticCacheDebug = result.GetExtraFields().CacheDebug
			}
			if logMsg.UpdateData != nil && p.pricingManager != nil {
				cost := p.pricingManager.CalculateCostWithCacheDebug(result)
				logMsg.UpdateData.Cost = &cost
			}
			// Here we pass plugin level context for background processing to avoid context cancellation
			processingErr := retryOnNotFound(p.ctx, func() error {
				return p.updateLogEntry(
					p.ctx,
					logMsg.RequestID,
					logMsg.SelectedKeyID,
					logMsg.SelectedKeyName,
					logMsg.Latency,
					logMsg.VirtualKeyID,
					logMsg.VirtualKeyName,
					logMsg.SemanticCacheDebug,
					logMsg.UpdateData,
				)
			})
			if processingErr != nil {
				p.logger.Warn("failed to process log update for request %s: %v", logMsg.RequestID, processingErr)
			} else {
				// Call callback immediately for both streaming and regular updates
				// UI will handle debouncing if needed
				p.mu.Lock()
				if p.logCallback != nil {
					if updatedEntry, getErr := p.getLogEntry(p.ctx, logMsg.RequestID); getErr == nil {
						updatedEntry.SelectedKey = &schemas.Key{
							ID:   updatedEntry.SelectedKeyID,
							Name: updatedEntry.SelectedKeyName,
						}
						if updatedEntry.VirtualKeyID != nil && updatedEntry.VirtualKeyName != nil {
							updatedEntry.VirtualKey = &tables.TableVirtualKey{
								ID:   *updatedEntry.VirtualKeyID,
								Name: *updatedEntry.VirtualKeyName,
							}
						}
						p.logCallback(updatedEntry)
					}
				}
				p.mu.Unlock()
			}
		}
	}()
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
