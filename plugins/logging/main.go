// Package logging provides a GORM-based logging plugin for Bifrost.
// This plugin stores comprehensive logs of all requests and responses with search,
// filter, and pagination capabilities.
package logging

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/mcpcatalog"
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
	Status                string
	TokenUsage            *schemas.BifrostLLMUsage
	Cost                  *float64        // Cost in dollars from pricing plugin
	ListModelsOutput      []schemas.Model // For list models requests
	ChatOutput            *schemas.ChatMessage
	ResponsesOutput       []schemas.ResponsesMessage
	EmbeddingOutput       []schemas.EmbeddingData
	RerankOutput          []schemas.RerankResult
	ErrorDetails          *schemas.BifrostError
	SpeechOutput          *schemas.BifrostSpeechResponse          // For non-streaming speech responses
	TranscriptionOutput   *schemas.BifrostTranscriptionResponse   // For non-streaming transcription responses
	ImageGenerationOutput *schemas.BifrostImageGenerationResponse // For non-streaming image generation responses
	VideoGenerationOutput *schemas.BifrostVideoGenerationResponse // For non-streaming video generation responses
	VideoRetrieveOutput   *schemas.BifrostVideoGenerationResponse // For non-streaming video retrieve responses
	VideoDownloadOutput   *schemas.BifrostVideoDownloadResponse   // For non-streaming video download responses
	VideoListOutput       *schemas.BifrostVideoListResponse       // For non-streaming video list responses
	VideoDeleteOutput     *schemas.BifrostVideoDeleteResponse     // For non-streaming video delete responses
	RawRequest            interface{}
	RawResponse           interface{}
}

// RecalculateCostResult represents summary stats from a cost backfill operation
type RecalculateCostResult struct {
	TotalMatched int64 `json:"total_matched"`
	Updated      int   `json:"updated"`
	Skipped      int   `json:"skipped"`
	Remaining    int64 `json:"remaining"`
}

// LogMessage represents a message in the logging queue
type LogMessage struct {
	Operation          LogOperation
	RequestID          string                             // Unique ID for the request
	ParentRequestID    string                             // Unique ID for the parent request (used for fallback requests)
	NumberOfRetries    int                                // Number of retries
	FallbackIndex      int                                // Fallback index
	SelectedKeyID      string                             // Selected key ID
	SelectedKeyName    string                             // Selected key name
	VirtualKeyID       string                             // Virtual key ID
	VirtualKeyName     string                             // Virtual key name
	RoutingEnginesUsed []string                           // List of routing engines used
	RoutingRuleID      string                             // Routing rule ID
	RoutingRuleName    string                             // Routing rule name
	Timestamp          time.Time                          // Of the preHook/postHook call
	Latency            int64                              // For latency updates
	InitialData        *InitialLogData                    // For create operations
	SemanticCacheDebug *schemas.BifrostCacheDebug         // For semantic cache operations
	UpdateData         *UpdateLogData                     // For update operations
	StreamResponse     *streaming.ProcessedStreamResponse // For streaming delta updates
	RoutingEngineLogs  string                             // Formatted routing engine decision logs
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
	ImageGenerationInput  *schemas.ImageGenerationInput
	VideoGenerationInput  *schemas.VideoGenerationInput
	Tools                 []schemas.ChatTool
	Metadata              map[string]interface{}
}

// LogCallback is a function that gets called when a new log entry is created
type LogCallback func(ctx context.Context, logEntry *logstore.Log)

// MCPToolLogCallback is a function that gets called when a new MCP tool log entry is created or updated
type MCPToolLogCallback func(*logstore.MCPToolLog)

type Config struct {
	DisableContentLogging *bool     `json:"disable_content_logging"`
	LoggingHeaders        *[]string `json:"logging_headers"` // Pointer to live config slice; changes are reflected immediately without restart
}

// LoggerPlugin implements the schemas.LLMPlugin and schemas.MCPPlugin interfaces
type LoggerPlugin struct {
	ctx                   context.Context
	store                 logstore.LogStore
	disableContentLogging *bool
	loggingHeaders        *[]string // Pointer to live config slice for headers to capture in metadata
	pricingManager        *modelcatalog.ModelCatalog
	mcpCatalog            *mcpcatalog.MCPCatalog // MCP catalog for tool cost calculation
	mu                    sync.Mutex
	done                  chan struct{}
	cleanupOnce           sync.Once // Ensures cleanup only runs once
	wg                    sync.WaitGroup
	logger                schemas.Logger
	logCallback           LogCallback
	mcpToolLogCallback    MCPToolLogCallback // Callback for MCP tool log entries
	droppedRequests       atomic.Int64
	cleanupTicker         *time.Ticker // Ticker for cleaning up old processing logs
	logMsgPool            sync.Pool    // Pool for reusing LogMessage structs
	updateDataPool        sync.Pool    // Pool for reusing UpdateLogData structs
}

// Init creates new logger plugin with given log store
func Init(ctx context.Context, config *Config, logger schemas.Logger, logsStore logstore.LogStore, pricingManager *modelcatalog.ModelCatalog, mcpCatalog *mcpcatalog.MCPCatalog) (*LoggerPlugin, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}
	if logsStore == nil {
		return nil, fmt.Errorf("logs store cannot be nil")
	}
	if pricingManager == nil {
		logger.Warn("logging plugin requires model catalog to calculate cost, all LLM cost calculations will be skipped.")
	}
	if mcpCatalog == nil {
		logger.Warn("logging plugin requires MCP catalog to calculate cost, all MCP cost calculations will be skipped.")
	}

	plugin := &LoggerPlugin{
		ctx:                   ctx,
		store:                 logsStore,
		pricingManager:        pricingManager,
		mcpCatalog:            mcpCatalog,
		disableContentLogging: config.DisableContentLogging,
		loggingHeaders:        config.LoggingHeaders,
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

	// Delete LLM processing logs older than 30 minutes
	if err := p.store.Flush(p.ctx, thirtyMinutesAgo); err != nil {
		p.logger.Warn("failed to cleanup old processing LLM logs: %v", err)
	}

	// Delete MCP tool processing logs older than 30 minutes
	if err := p.store.FlushMCPToolLogs(p.ctx, thirtyMinutesAgo); err != nil {
		p.logger.Warn("failed to cleanup old processing MCP tool logs: %v", err)
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

// HTTPTransportPreHook is not used for this plugin
func (p *LoggerPlugin) HTTPTransportPreHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	return nil, nil
}

// HTTPTransportPostHook is not used for this plugin
func (p *LoggerPlugin) HTTPTransportPostHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponse) error {
	return nil
}

// HTTPTransportStreamChunkHook passes through streaming chunks unchanged
func (p *LoggerPlugin) HTTPTransportStreamChunkHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, chunk *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error) {
	return chunk, nil
}

// captureLoggingHeaders extracts configured logging headers and x-bf-lh-* prefixed headers
// from the request context. Returns a new metadata map, or nil if no headers were captured.
// System entries (e.g. isAsyncRequest) should be set AFTER calling this so they take precedence.
func (p *LoggerPlugin) captureLoggingHeaders(ctx *schemas.BifrostContext) map[string]interface{} {
	allHeaders, _ := ctx.Value(schemas.BifrostContextKeyRequestHeaders).(map[string]string)
	if allHeaders == nil {
		return nil
	}

	var metadata map[string]interface{}

	// Check configured logging headers
	if p.loggingHeaders != nil {
		for _, h := range *p.loggingHeaders {
			key := strings.ToLower(h)
			if val, ok := allHeaders[key]; ok {
				if metadata == nil {
					metadata = make(map[string]interface{})
				}
				metadata[key] = val
			}
		}
	}

	// Check x-bf-lh-* prefixed headers
	for key, val := range allHeaders {
		if labelName, ok := strings.CutPrefix(key, "x-bf-lh-"); ok && labelName != "" {
			if metadata == nil {
				metadata = make(map[string]interface{})
			}
			metadata[labelName] = val
		}
	}

	return metadata
}

// PreLLMHook is called before a request is processed - FULLY ASYNC, NO DATABASE I/O
// Parameters:
//   - ctx: The Bifrost context
//   - req: The Bifrost request
//
// Returns:
//   - *schemas.BifrostRequest: The processed request
//   - *schemas.LLMPluginShortCircuit: The plugin short circuit if the request is not allowed
//   - error: Any error that occurred during processing
func (p *LoggerPlugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	if ctx == nil {
		// Log error but don't fail the request
		p.logger.Error("context is nil in PreLLMHook")
		return req, nil, nil
	}

	// Extract request ID from context
	requestID, ok := ctx.Value(schemas.BifrostContextKeyRequestID).(string)
	if !ok || requestID == "" {
		// Log error but don't fail the request
		p.logger.Error("request-id not found in context or is empty")
		return req, nil, nil
	}

	createdTimestamp := time.Now().UTC()

	// If request type is streaming we create a stream accumulator via the tracer
	if bifrost.IsStreamRequestType(req.RequestType) {
		tracer, traceID, err := bifrost.GetTracerFromContext(ctx)
		if err == nil && tracer != nil && traceID != "" {
			tracer.CreateStreamAccumulator(traceID, createdTimestamp)
		}
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
		case schemas.RerankRequest:
			initialData.Params = req.RerankRequest.Params
		case schemas.SpeechRequest, schemas.SpeechStreamRequest:
			initialData.Params = req.SpeechRequest.Params
			initialData.SpeechInput = req.SpeechRequest.Input
		case schemas.TranscriptionRequest, schemas.TranscriptionStreamRequest:
			initialData.Params = req.TranscriptionRequest.Params
			initialData.TranscriptionInput = req.TranscriptionRequest.Input
		case schemas.ImageGenerationRequest, schemas.ImageGenerationStreamRequest:
			initialData.Params = req.ImageGenerationRequest.Params
			initialData.ImageGenerationInput = req.ImageGenerationRequest.Input
		case schemas.VideoGenerationRequest:
			initialData.Params = req.VideoGenerationRequest.Params
			initialData.VideoGenerationInput = req.VideoGenerationRequest.Input
		case schemas.VideoRemixRequest:
			initialData.Params = &schemas.VideoLogParams{
				VideoID: req.VideoRemixRequest.ID,
			}
			initialData.VideoGenerationInput = req.VideoRemixRequest.Input
		case schemas.VideoRetrieveRequest:
			initialData.Params = &schemas.VideoLogParams{
				VideoID: req.VideoRetrieveRequest.ID,
			}
		case schemas.VideoDownloadRequest:
			initialData.Params = &schemas.VideoLogParams{
				VideoID: req.VideoDownloadRequest.ID,
			}
		case schemas.VideoDeleteRequest:
			initialData.Params = &schemas.VideoLogParams{
				VideoID: req.VideoDeleteRequest.ID,
			}
		}
	}

	// Capture configured logging headers and x-bf-lh-* headers into metadata first
	initialData.Metadata = p.captureLoggingHeaders(ctx)

	// System entries are set after so they take precedence over dynamic header values
	if isAsync, ok := ctx.Value(schemas.BifrostIsAsyncRequest).(bool); ok && isAsync {
		if initialData.Metadata == nil {
			initialData.Metadata = make(map[string]interface{})
		}
		initialData.Metadata["isAsyncRequest"] = true
	}

	// Queue the log creation message (non-blocking) - Using sync.Pool
	logMsg := p.getLogMessage()
	logMsg.Operation = LogOperationCreate

	// If fallback request ID is present, use it instead of the primary request ID
	fallbackRequestID, ok := ctx.Value(schemas.BifrostContextKeyFallbackRequestID).(string)
	if ok && fallbackRequestID != "" {
		logMsg.RequestID = fallbackRequestID
		logMsg.ParentRequestID = requestID
	} else {
		logMsg.RequestID = requestID
	}

	fallbackIndex := bifrost.GetIntFromContext(ctx, schemas.BifrostContextKeyFallbackIndex)
	// Get routing engines array
	routingEngines := []string{}
	if engines, ok := ctx.Value(schemas.BifrostContextKeyRoutingEnginesUsed).([]string); ok {
		routingEngines = engines
	}

	logMsg.Timestamp = createdTimestamp
	logMsg.InitialData = initialData
	logMsg.FallbackIndex = fallbackIndex
	logMsg.RoutingEnginesUsed = routingEngines

	go func(msg *LogMessage) {
		defer p.putLogMessage(msg) // Return to pool when done
		if err := p.insertInitialLogEntry(
			p.ctx,
			msg.RequestID,
			msg.ParentRequestID,
			msg.Timestamp,
			msg.FallbackIndex,
			msg.RoutingEnginesUsed,
			msg.InitialData,
		); err != nil {
			p.logger.Warn("failed to insert initial log entry for request %s: %v", msg.RequestID, err)
		} else {
			// Call callback for initial log creation (WebSocket "create" message)
			// Construct LogEntry directly from data we have to avoid database query
			p.mu.Lock()
			callback := p.logCallback
			p.mu.Unlock()

			if callback != nil {
				initialEntry := &logstore.Log{
					ID:                          msg.RequestID,
					Timestamp:                   msg.Timestamp,
					Object:                      msg.InitialData.Object,
					Provider:                    msg.InitialData.Provider,
					Model:                       msg.InitialData.Model,
					FallbackIndex:               msg.FallbackIndex,
					InputHistoryParsed:          msg.InitialData.InputHistory,
					ResponsesInputHistoryParsed: msg.InitialData.ResponsesInputHistory,
					ParamsParsed:                msg.InitialData.Params,
					ToolsParsed:                 msg.InitialData.Tools,
					MetadataParsed:              msg.InitialData.Metadata,
					VideoGenerationInputParsed:  msg.InitialData.VideoGenerationInput,
					Status:                      "processing",
					Stream:                      false, // Initially false, will be updated if streaming
					CreatedAt:                   msg.Timestamp,
				}
				if len(msg.RoutingEnginesUsed) > 0 {
					initialEntry.RoutingEnginesUsed = msg.RoutingEnginesUsed
				}
				callback(p.ctx, initialEntry)
			}
		}
	}(logMsg)

	return req, nil, nil
}

// PostLLMHook is called after a response is received - FULLY ASYNC, NO DATABASE I/O
// Parameters:
//   - ctx: The Bifrost context
//   - result: The Bifrost response to be processed
//   - bifrostErr: The Bifrost error to be processed
//
// Returns:
//   - *schemas.BifrostResponse: The processed response
//   - *schemas.BifrostError: The processed error
//   - error: Any error that occurred during processing
func (p *LoggerPlugin) PostLLMHook(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if ctx == nil {
		// Log error but don't fail the request
		p.logger.Error("context is nil in PostLLMHook")
		return result, bifrostErr, nil
	}
	requestID, ok := ctx.Value(schemas.BifrostContextKeyRequestID).(string)
	if !ok || requestID == "" {
		p.logger.Error("request-id not found in context or is empty")
		return result, bifrostErr, nil
	}
	// If fallback request ID is present, use it instead of the primary request ID
	fallbackRequestID, ok := ctx.Value(schemas.BifrostContextKeyFallbackRequestID).(string)
	if ok && fallbackRequestID != "" {
		requestID = fallbackRequestID
	}
	selectedKeyID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeySelectedKeyID)
	selectedKeyName := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeySelectedKeyName)
	virtualKeyID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceVirtualKeyID)
	virtualKeyName := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceVirtualKeyName)
	routingRuleID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceRoutingRuleID)
	routingRuleName := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceRoutingRuleName)
	numberOfRetries := bifrost.GetIntFromContext(ctx, schemas.BifrostContextKeyNumberOfRetries)

	requestType, _, _ := bifrost.GetResponseFields(result, bifrostErr)

	isFinalChunk := bifrost.IsFinalChunk(ctx)

	var tracer schemas.Tracer
	var traceID string
	if bifrost.IsStreamRequestType(requestType) {
		var err error
		tracer, traceID, err = bifrost.GetTracerFromContext(ctx)
		if err != nil {
			p.logger.Warn("failed to get traceID/tracer from context of logging plugin posthook: %v", err)
			return result, bifrostErr, nil
		}
	}

	// Extract routing engine logs from context before entering goroutine
	routingEngineLogs := formatRoutingEngineLogs(ctx.GetRoutingEngineLogs())

	go func() {
		// Queue the log update message (non-blocking) - use same pattern for both streaming and regular
		logMsg := p.getLogMessage()
		logMsg.RequestID = requestID
		logMsg.SelectedKeyID = selectedKeyID
		logMsg.VirtualKeyID = virtualKeyID
		logMsg.RoutingRuleID = routingRuleID
		logMsg.SelectedKeyName = selectedKeyName
		logMsg.VirtualKeyName = virtualKeyName
		logMsg.RoutingRuleName = routingRuleName
		logMsg.NumberOfRetries = numberOfRetries
		logMsg.RoutingEngineLogs = routingEngineLogs
		defer p.putLogMessage(logMsg) // Return to pool when done

		if result != nil {
			logMsg.Latency = result.GetExtraFields().Latency
		} else {
			logMsg.Latency = 0
		}

		// If response is nil, and there is an error, we update log with error
		if result == nil && bifrostErr != nil {
			// Note: Stream accumulator cleanup is handled by the tracing middleware
			logMsg.Operation = LogOperationUpdate
			updateData := &UpdateLogData{
				Status:       "error",
				ErrorDetails: bifrostErr,
			}

			// Extract raw request from error's ExtraFields
			if p.disableContentLogging == nil || !*p.disableContentLogging {
				if bifrostErr.ExtraFields.RawRequest != nil {
					updateData.RawRequest = bifrostErr.ExtraFields.RawRequest
				}
				if bifrostErr.ExtraFields.RawResponse != nil {
					updateData.RawResponse = bifrostErr.ExtraFields.RawResponse
				}
			}

			logMsg.UpdateData = updateData
			processingErr := retryOnNotFound(p.ctx, func() error {
				return p.updateLogEntry(
					p.ctx,
					logMsg.RequestID,
					logMsg.SelectedKeyID,
					logMsg.SelectedKeyName,
					logMsg.Latency,
					logMsg.VirtualKeyID,
					logMsg.VirtualKeyName,
					logMsg.RoutingRuleID,
					logMsg.RoutingRuleName,
					logMsg.NumberOfRetries,
					logMsg.SemanticCacheDebug,
					logMsg.RoutingEngineLogs,
					logMsg.UpdateData,
				)
			})
			if processingErr != nil {
				p.logger.Warn("failed to process log update for request %s: %v", logMsg.RequestID, processingErr)
			} else {
				// Call callback immediately for both streaming and regular updates
				// UI will handle debouncing if needed
				p.mu.Lock()
				callback := p.logCallback
				p.mu.Unlock()
				if callback != nil {
					if updatedEntry, getErr := p.getLogEntry(p.ctx, logMsg.RequestID); getErr == nil {
						callback(p.ctx, updatedEntry)
					}
				}
			}

			return
		}
		if bifrost.IsStreamRequestType(requestType) {
			p.logger.Debug("[logging] processing streaming response")

			// Process streaming response via tracer's central accumulator
			var streamResponse *streaming.ProcessedStreamResponse
			if tracer != nil && traceID != "" {
				accResult := tracer.ProcessStreamingChunk(traceID, isFinalChunk, result, bifrostErr)
				if accResult != nil {
					streamResponse = convertToProcessedStreamResponse(accResult, requestType)
				}
			} else {
				p.logger.Debug("tracer or traceID not available in streaming path for request %s, skipping stream processing", logMsg.RequestID)
			}

			if streamResponse == nil {
				p.logger.Debug("failed to process streaming response: tracer or traceID not available")
			} else if isFinalChunk {
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
						logMsg.RoutingRuleID,
						logMsg.RoutingRuleName,
						logMsg.NumberOfRetries,
						logMsg.SemanticCacheDebug,
						logMsg.RoutingEngineLogs,
						logMsg.StreamResponse,
						true,
					)
				})
				if processingErr != nil {
					p.logger.Warn("failed to process stream update for request %s: %v", logMsg.RequestID, processingErr)
				} else {
					// Call callback immediately for both streaming and regular updates
					// UI will handle debouncing if needed
					p.mu.Lock()
					callback := p.logCallback
					p.mu.Unlock()
					if callback != nil {
						if updatedEntry, getErr := p.getLogEntry(p.ctx, logMsg.RequestID); getErr == nil {
							callback(p.ctx, updatedEntry)
						}
					}
				}
				// Note: Stream accumulator cleanup is handled by the tracer
				if tracer != nil && traceID != "" {
					p.logger.Debug("cleaning up stream accumulator for trace ID: %s in logging plugin posthook", traceID)
					tracer.CleanupStreamAccumulator(traceID)
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
				case result.RerankResponse != nil && result.RerankResponse.Usage != nil:
					usage = result.RerankResponse.Usage
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
				case result.ImageGenerationResponse != nil && result.ImageGenerationResponse.Usage != nil:
					usage = &schemas.BifrostLLMUsage{}
					usage.PromptTokens = result.ImageGenerationResponse.Usage.InputTokens
					usage.CompletionTokens = result.ImageGenerationResponse.Usage.OutputTokens
					if result.ImageGenerationResponse.Usage.TotalTokens > 0 {
						usage.TotalTokens = result.ImageGenerationResponse.Usage.TotalTokens
					} else {
						usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
					}
				case result.CountTokensResponse != nil:
					usage = &schemas.BifrostLLMUsage{}
					usage.PromptTokens = result.CountTokensResponse.InputTokens
					if result.CountTokensResponse.OutputTokens != nil {
						usage.CompletionTokens = *result.CountTokensResponse.OutputTokens
					}
					if result.CountTokensResponse.TotalTokens != nil {
						usage.TotalTokens = *result.CountTokensResponse.TotalTokens
					} else {
						usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
					}
				}
				updateData.TokenUsage = usage
				// Extract raw response
				extraFields := result.GetExtraFields()
				if p.disableContentLogging == nil || !*p.disableContentLogging {
					if extraFields.RawRequest != nil {
						updateData.RawRequest = extraFields.RawRequest
					}
					if extraFields.RawResponse != nil {
						updateData.RawResponse = extraFields.RawResponse
					}
					if result.ListModelsResponse != nil && result.ListModelsResponse.Data != nil {
						updateData.ListModelsOutput = result.ListModelsResponse.Data
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
					if result.RerankResponse != nil {
						updateData.RerankOutput = result.RerankResponse.Results
					}
					// Handle speech and transcription outputs for NON-streaming responses
					if result.SpeechResponse != nil {
						updateData.SpeechOutput = result.SpeechResponse
					}
					if result.TranscriptionResponse != nil {
						updateData.TranscriptionOutput = result.TranscriptionResponse
					}
					if result.ImageGenerationResponse != nil {
						updateData.ImageGenerationOutput = result.ImageGenerationResponse
					}
					if result.VideoGenerationResponse != nil {
						switch requestType {
						case schemas.VideoGenerationRequest:
							updateData.VideoGenerationOutput = result.VideoGenerationResponse
						case schemas.VideoRetrieveRequest:
							updateData.VideoRetrieveOutput = result.VideoGenerationResponse
						case schemas.VideoRemixRequest:
							updateData.VideoGenerationOutput = result.VideoGenerationResponse
						}
					}
					if result.VideoDownloadResponse != nil {
						updateData.VideoDownloadOutput = result.VideoDownloadResponse
					}
					if result.VideoListResponse != nil {
						updateData.VideoListOutput = result.VideoListResponse
					}
					if result.VideoDeleteResponse != nil {
						updateData.VideoDeleteOutput = result.VideoDeleteResponse
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
				cost := p.pricingManager.CalculateCost(result)
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
					logMsg.RoutingRuleID,
					logMsg.RoutingRuleName,
					logMsg.NumberOfRetries,
					logMsg.SemanticCacheDebug,
					logMsg.RoutingEngineLogs,
					logMsg.UpdateData,
				)
			})
			if processingErr != nil {
				p.logger.Warn("failed to process log update for request %s: %v", logMsg.RequestID, processingErr)
			} else {
				// Call callback immediately for both streaming and regular updates
				// UI will handle debouncing if needed
				p.mu.Lock()
				callback := p.logCallback
				p.mu.Unlock()
				if callback != nil {
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
						if updatedEntry.RoutingRuleID != nil && updatedEntry.RoutingRuleName != nil {
							updatedEntry.RoutingRule = &tables.TableRoutingRule{
								ID:   *updatedEntry.RoutingRuleID,
								Name: *updatedEntry.RoutingRuleName,
							}
						}
						callback(p.ctx, updatedEntry)
					}
				}
			}
		}
	}()
	return result, bifrostErr, nil
}

// Cleanup is called when the plugin is being shut down
func (p *LoggerPlugin) Cleanup() error {
	p.cleanupOnce.Do(func() {
		// Stop the cleanup ticker
		if p.cleanupTicker != nil {
			p.cleanupTicker.Stop()
		}
		// Signal the background worker to stop
		close(p.done)
		// Wait for the background worker to finish processing remaining items
		p.wg.Wait()
		// Note: Accumulator cleanup is handled by the tracer, not the logging plugin
		// GORM handles connection cleanup automatically
	})
	return nil
}

// MCP Plugin Interface Implementation

// SetMCPToolLogCallback sets a callback function that will be called for each MCP tool log entry
func (p *LoggerPlugin) SetMCPToolLogCallback(callback MCPToolLogCallback) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.mcpToolLogCallback = callback
}

// PreMCPHook is called before an MCP tool execution - creates initial log entry
// Parameters:
//   - ctx: The Bifrost context
//   - req: The MCP request containing tool call information
//
// Returns:
//   - *schemas.BifrostMCPRequest: The unmodified request
//   - *schemas.MCPPluginShortCircuit: nil (no short-circuiting)
//   - error: nil (errors are logged but don't fail the request)
func (p *LoggerPlugin) PreMCPHook(ctx *schemas.BifrostContext, req *schemas.BifrostMCPRequest) (*schemas.BifrostMCPRequest, *schemas.MCPPluginShortCircuit, error) {
	if ctx == nil {
		p.logger.Error("context is nil in PreMCPHook")
		return req, nil, nil
	}

	requestID, ok := ctx.Value(schemas.BifrostContextKeyRequestID).(string)
	if !ok || requestID == "" {
		p.logger.Error("request-id not found in context or is empty in PreMCPHook")
		return req, nil, nil
	}

	// Get parent request ID if this MCP call is part of a larger LLM request (using the MCP agent original request ID)
	parentRequestID, _ := ctx.Value(schemas.BifrostMCPAgentOriginalRequestID).(string)

	createdTimestamp := time.Now().UTC()

	// Extract tool name and arguments from the request
	var toolName string
	var serverLabel string

	fullToolName := req.GetToolName()
	arguments := req.GetToolArguments()
	// Skip execution for codemode tools
	if bifrost.IsCodemodeTool(fullToolName) {
		return req, nil, nil
	}

	// Extract server label from tool name (format: {client}-{tool_name})
	// The first part before hyphen is the client/server label
	if fullToolName != "" {
		if idx := strings.Index(fullToolName, "-"); idx > 0 {
			serverLabel = fullToolName[:idx]
			toolName = fullToolName[idx+1:]
		} else {
			toolName = fullToolName
		}
		switch toolName {
		case mcp.ToolTypeListToolFiles, mcp.ToolTypeReadToolFile, mcp.ToolTypeExecuteToolCode:
			if serverLabel == "" {
				serverLabel = "codemode"
			}
		}
	}

	// Get virtual key information from context - using same method as normal LLM logging
	virtualKeyID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceVirtualKeyID)
	virtualKeyName := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceVirtualKeyName)

	go func() {
		entry := &logstore.MCPToolLog{
			ID:          requestID,
			Timestamp:   createdTimestamp,
			ToolName:    toolName,
			ServerLabel: serverLabel,
			Status:      "processing",
			CreatedAt:   createdTimestamp,
		}

		if parentRequestID != "" {
			entry.LLMRequestID = &parentRequestID
		}

		if virtualKeyID != "" {
			entry.VirtualKeyID = &virtualKeyID
		}
		if virtualKeyName != "" {
			entry.VirtualKeyName = &virtualKeyName
		}

		// Set arguments if content logging is enabled
		if p.disableContentLogging == nil || !*p.disableContentLogging {
			entry.ArgumentsParsed = arguments
		}

		// Capture configured logging headers and x-bf-lh-* headers into metadata
		entry.MetadataParsed = p.captureLoggingHeaders(ctx)

		if err := p.store.CreateMCPToolLog(p.ctx, entry); err != nil {
			p.logger.Warn("Failed to insert initial MCP tool log entry for request %s: %v", requestID, err)
		} else {
			// Capture callback under lock, then call it outside the critical section
			p.mu.Lock()
			callback := p.mcpToolLogCallback
			p.mu.Unlock()

			if callback != nil {
				callback(entry)
			}
		}
	}()

	return req, nil, nil
}

// PostMCPHook is called after an MCP tool execution - updates the log entry with results
// Parameters:
//   - ctx: The Bifrost context
//   - resp: The MCP response containing tool execution result
//   - bifrostErr: Any error that occurred during execution
//
// Returns:
//   - *schemas.BifrostMCPResponse: The unmodified response
//   - *schemas.BifrostError: The unmodified error
//   - error: nil (errors are logged but don't fail the request)
func (p *LoggerPlugin) PostMCPHook(ctx *schemas.BifrostContext, resp *schemas.BifrostMCPResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostMCPResponse, *schemas.BifrostError, error) {
	if ctx == nil {
		p.logger.Error("context is nil in PostMCPHook")
		return resp, bifrostErr, nil
	}

	// Skip logging for codemode tools (executeToolCode, listToolFiles, readToolFile)
	// We check the tool name from the response instead of context flags
	if resp != nil && bifrost.IsCodemodeTool(resp.ExtraFields.ToolName) {
		return resp, bifrostErr, nil
	}

	requestID, ok := ctx.Value(schemas.BifrostContextKeyRequestID).(string)
	if !ok || requestID == "" {
		p.logger.Error("request-id not found in context or is empty in PostMCPHook")
		return resp, bifrostErr, nil
	}

	// Extract virtual key ID and name from context (set by governance plugin)
	virtualKeyID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceVirtualKeyID)
	virtualKeyName := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceVirtualKeyName)

	go func() {
		updates := make(map[string]interface{})

		// Update virtual key ID and name if they are set (from governance plugin)
		if virtualKeyID != "" {
			updates["virtual_key_id"] = virtualKeyID
		}
		if virtualKeyName != "" {
			updates["virtual_key_name"] = virtualKeyName
		}

		// Get latency from response ExtraFields
		if resp != nil {
			updates["latency"] = float64(resp.ExtraFields.Latency)
		}

		// Calculate MCP tool cost from catalog if available
		var toolCost float64
		success := (resp != nil && bifrostErr == nil)
		if success && resp != nil && p.mcpCatalog != nil && resp.ExtraFields.ClientName != "" && resp.ExtraFields.ToolName != "" {
			// Use separate client name and tool name fields
			if pricingEntry, ok := p.mcpCatalog.GetPricingData(resp.ExtraFields.ClientName, resp.ExtraFields.ToolName); ok {
				toolCost = pricingEntry.CostPerExecution
				updates["cost"] = toolCost
				p.logger.Debug("MCP tool cost for %s.%s: $%.6f", resp.ExtraFields.ClientName, resp.ExtraFields.ToolName, toolCost)
			}
		}

		if bifrostErr != nil {
			updates["status"] = "error"
			// Serialize error details
			tempEntry := &logstore.MCPToolLog{}
			tempEntry.ErrorDetailsParsed = bifrostErr
			if err := tempEntry.SerializeFields(); err == nil {
				updates["error_details"] = tempEntry.ErrorDetails
			}
		} else if resp != nil {
			updates["status"] = "success"
			// Store result if content logging is enabled
			if p.disableContentLogging == nil || !*p.disableContentLogging {
				var result interface{}
				if resp.ChatMessage != nil {
					// For ChatMessage, try to parse the content as JSON if it's a string
					if resp.ChatMessage.Content != nil && resp.ChatMessage.Content.ContentStr != nil {
						contentStr := *resp.ChatMessage.Content.ContentStr
						var parsedContent interface{}
						if err := sonic.Unmarshal([]byte(contentStr), &parsedContent); err == nil {
							// Content is valid JSON, use parsed version
							result = parsedContent
						} else {
							// Content is not valid JSON or failed to parse, store the whole message
							result = resp.ChatMessage
						}
					} else {
						result = resp.ChatMessage
					}
				} else if resp.ResponsesMessage != nil {
					result = resp.ResponsesMessage
				}
				if result != nil {
					tempEntry := &logstore.MCPToolLog{}
					tempEntry.ResultParsed = result
					if err := tempEntry.SerializeFields(); err == nil {
						updates["result"] = tempEntry.Result
					}
				}
			}
		} else {
			updates["status"] = "error"
			tempEntry := &logstore.MCPToolLog{}
			tempEntry.ErrorDetailsParsed = &schemas.BifrostError{
				IsBifrostError: true,
				Error: &schemas.ErrorField{
					Message: "MCP tool execution returned nil response",
				},
			}
			if err := tempEntry.SerializeFields(); err == nil {
				updates["error_details"] = tempEntry.ErrorDetails
			}
		}

		processingErr := retryOnNotFound(p.ctx, func() error {
			return p.store.UpdateMCPToolLog(p.ctx, requestID, updates)
		})
		if processingErr != nil {
			p.logger.Warn("failed to process MCP tool log update for request %s: %v", requestID, processingErr)
		} else {
			// Capture callback under lock, then perform DB I/O and invoke callback outside critical section
			p.mu.Lock()
			callback := p.mcpToolLogCallback
			p.mu.Unlock()

			if callback != nil {
				if updatedEntry, getErr := p.store.FindMCPToolLog(p.ctx, requestID); getErr == nil {
					callback(updatedEntry)
				} else {
					p.logger.Warn("failed to find updated entry for callback: %v", getErr)
				}
			}
		}
	}()

	return resp, bifrostErr, nil
}
