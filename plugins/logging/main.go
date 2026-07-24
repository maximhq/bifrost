// Package logging provides a GORM-based logging plugin for Bifrost.
// This plugin stores comprehensive logs of all requests and responses with search,
// filter, and pagination capabilities.
package logging

import (
	"context"
	"fmt"
	"math"
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
	Status                 string
	TokenUsage             *schemas.BifrostLLMUsage
	Cost                   *float64        // Cost in dollars from pricing plugin
	ListModelsOutput       []schemas.Model // For list models requests
	ChatOutput             *schemas.ChatMessage
	ResponsesOutput        []schemas.ResponsesMessage
	EmbeddingOutput        []schemas.EmbeddingData
	RerankOutput           []schemas.RerankResult
	OCROutput              *schemas.BifrostOCRResponse // For OCR responses
	ErrorDetails           *schemas.BifrostError
	SpeechOutput           *schemas.BifrostSpeechResponse          // For non-streaming speech responses
	TranscriptionOutput    *schemas.BifrostTranscriptionResponse   // For non-streaming transcription responses
	ImageGenerationOutput  *schemas.BifrostImageGenerationResponse // For non-streaming image generation responses
	VideoGenerationOutput  *schemas.BifrostVideoGenerationResponse // For non-streaming video generation responses
	VideoRetrieveOutput    *schemas.BifrostVideoGenerationResponse // For non-streaming video retrieve responses
	VideoDownloadOutput    *schemas.BifrostVideoDownloadResponse   // For non-streaming video download responses
	VideoListOutput        *schemas.BifrostVideoListResponse       // For non-streaming video list responses
	VideoDeleteOutput      *schemas.BifrostVideoDeleteResponse     // For non-streaming video delete responses
	RawRequest             any
	RawResponse            any
	IsLargePayloadRequest  bool // When true, RawRequest is a truncated preview string (skip sonic.Marshal)
	IsLargePayloadResponse bool // When true, RawResponse is a truncated preview string (skip sonic.Marshal)
}

// applyLargePayloadPreviews reads large payload/response preview strings from context
// and overrides RawRequest/RawResponse on updateData for truncated logging.
func applyLargePayloadPreviews(ctx *schemas.BifrostContext, updateData *UpdateLogData) {
	if isLargePayload, ok := ctx.Value(schemas.BifrostContextKeyLargePayloadMode).(bool); ok && isLargePayload {
		if preview, ok := ctx.Value(schemas.BifrostContextKeyLargePayloadRequestPreview).(string); ok && preview != "" {
			updateData.RawRequest = preview
			updateData.IsLargePayloadRequest = true
		}
	}
	if isLargeResponse, ok := ctx.Value(schemas.BifrostContextKeyLargeResponseMode).(bool); ok && isLargeResponse {
		if preview, ok := ctx.Value(schemas.BifrostContextKeyLargePayloadResponsePreview).(string); ok && preview != "" {
			updateData.RawResponse = preview
			updateData.IsLargePayloadResponse = true
		}
	}
}

// applyLargePayloadPreviewsToEntry applies the large payload preview values from
// the context to the log entry, if they are available and content logging is enabled.
func applyLargePayloadPreviewsToEntry(ctx *schemas.BifrostContext, entry *logstore.Log, contentLoggingEnabled bool) {
	if ctx == nil || entry == nil {
		return
	}

	updateData := &UpdateLogData{}
	applyLargePayloadPreviews(ctx, updateData)
	shouldStoreRaw, _ := ctx.Value(schemas.BifrostContextKeyShouldStoreRawInLogs).(bool)

	if updateData.IsLargePayloadRequest {
		entry.IsLargePayloadRequest = true
		if shouldStoreRaw && contentLoggingEnabled {
			if preview, ok := updateData.RawRequest.(string); ok {
				entry.RawRequest = preview
			}
		}
	}
	if updateData.IsLargePayloadResponse {
		entry.IsLargePayloadResponse = true
		if shouldStoreRaw && contentLoggingEnabled {
			if preview, ok := updateData.RawResponse.(string); ok {
				entry.RawResponse = preview
			}
		}
	}
}

// attachLogRedactionData copies guardrail redaction data into the log entry for async writers.
func attachLogRedactionData(ctx *schemas.BifrostContext, entry *logstore.Log, contentLoggingEnabled bool) {
	if ctx == nil || entry == nil || !contentLoggingEnabled {
		return
	}
	if data, ok := schemas.RedactionDataFromContext(ctx); ok {
		snapshot := data.Clone()
		entry.RedactionData = &snapshot
	}
}

// sanitizeErrorForLogging returns a shallow copy of err with ExtraFields.RawRequest and
// RawResponse cleared when raw-byte persistence is disabled, preventing raw bytes from
// leaking into the store via JSON serialization.
//
// Every assignment to ErrorDetailsParsed (Log and MCPToolLog alike) must go through this
// function: logstore's SerializeFields, which runs on every write path (BeforeCreate hook,
// hybrid store, rdb batch writes), serializes ErrorDetailsParsed into the error_details
// column and overwrites anything a caller put in ErrorDetails. Callers set only the
// sanitized ErrorDetailsParsed and leave the string serialization to SerializeFields.
func sanitizeErrorForLogging(err *schemas.BifrostError, contentLoggingEnabled, shouldStoreRaw bool) *schemas.BifrostError {
	if err == nil {
		return nil
	}
	if contentLoggingEnabled && shouldStoreRaw {
		return err
	}
	cloned := *err
	cloned.ExtraFields.RawRequest = nil
	cloned.ExtraFields.RawResponse = nil
	return &cloned
}

// contentPolicy is the resolved per-request content handling decision.
type contentPolicy struct {
	// storeContent: content (messages, params, tool results) is populated on
	// the log entry and persisted.
	storeContent bool
	// hidden: the entry is stamped ContentHidden — the hybrid store keeps the
	// DB row content-free and the payload is only retained in object storage,
	// never hydrated back on reads.
	hidden bool
}

// visible reports whether logged content is served back through the API/UI.
func (c contentPolicy) visible() bool { return c.storeContent && !c.hidden }

// resolveContentPolicy resolves content handling for this request. Content
// logging is disabled either by the static disable_content_logging config or
// by the x-bf-disable-content-logging header (honored only when
// BifrostContextKeyAllowPerRequestStorageOverride is true in context, set by
// ConvertToBifrostContext from allow_per_request_content_storage_override
// config). What "disabled" means depends on retain_content_in_object_storage:
//   - off (default) → content is not persisted anywhere.
//   - on → content is offloaded to object storage as hidden: the DB row stays
//     content-free and reads never hydrate the payload back, but the object
//     store keeps the full payload. Requires an object-storage-backed log
//     store; degrades to not-persisted otherwise.
func (p *LoggerPlugin) resolveContentPolicy(ctx *schemas.BifrostContext) contentPolicy {
	disabled := p.disableContentLogging != nil && *p.disableContentLogging
	if ctx != nil {
		if perRequestAllowed, _ := ctx.Value(schemas.BifrostContextKeyAllowPerRequestStorageOverride).(bool); perRequestAllowed {
			if override, ok := ctx.Value(schemas.BifrostContextKeyDisableContentLogging).(bool); ok {
				disabled = override
			}
		}
	}
	if !disabled {
		return contentPolicy{storeContent: true}
	}
	if p.retainContentInObjectStorage != nil && *p.retainContentInObjectStorage {
		if p.objectStorageEnabled {
			return contentPolicy{storeContent: true, hidden: true}
		}
		p.retainWarnOnce.Do(func() {
			p.logger.Warn("retain_content_in_object_storage is enabled but the log store has no object storage configured; content-disabled requests are dropped entirely")
		})
	}
	return contentPolicy{}
}

// contentLoggingEnabled returns true if content (messages, params, tool results) should be
// recorded on the log entry for this request.
func (p *LoggerPlugin) contentLoggingEnabled(ctx *schemas.BifrostContext) bool {
	return p.resolveContentPolicy(ctx).storeContent
}

// applyMCPGovernanceFieldsToEntry stamps MCP log ownership from the request context.
func applyMCPGovernanceFieldsToEntry(ctx *schemas.BifrostContext, entry *logstore.MCPToolLog) {
	if ctx == nil || entry == nil {
		return
	}
	userID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyUserID)
	teamID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceTeamID)
	customerID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceCustomerID)
	businessUnitID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceBusinessUnitID)
	if userID != "" {
		entry.UserID = &userID
	}
	if teamID != "" {
		entry.TeamID = &teamID
	}
	if customerID != "" {
		entry.CustomerID = &customerID
	}
	if businessUnitID != "" {
		entry.BusinessUnitID = &businessUnitID
	}
}

// scheduleDeferredUsageUpdate schedules a deferred usage update for the request.
// applyErrorBillingFromBilledUsage backfills a failed/cancelled request's log
// entry from the usage the provider already processed before the failure
// (carried on BifrostError.ExtraFields.BilledUsage). Token usage is only filled
// when stream accumulation didn't already capture it, but cost is (re)computed
// whenever it is still missing - independent of whether tokens were already
// parsed, since a streaming error can populate usage without a cost.
func (p *LoggerPlugin) applyErrorBillingFromBilledUsage(ctx *schemas.BifrostContext, entry *logstore.Log, billed *schemas.BifrostLLMUsage, requestType schemas.RequestType) {
	if billed == nil {
		return
	}
	if entry.TokenUsageParsed == nil {
		entry.TokenUsageParsed = billed
		entry.PromptTokens = billed.PromptTokens
		entry.CompletionTokens = billed.CompletionTokens
		entry.TotalTokens = billed.TotalTokens
	}
	if entry.Cost == nil && p.pricingManager != nil {
		pricingScopes := modelcatalog.PricingLookupScopesFromContext(ctx, string(entry.Provider))
		if cost := p.pricingManager.CalculateCostForUsage(billed, schemas.ModelProvider(entry.Provider), entry.Model, requestType, pricingScopes); cost > 0 {
			entry.Cost = &cost
		}
	}
}

func (p *LoggerPlugin) scheduleDeferredUsageUpdate(ctx *schemas.BifrostContext, requestID string, usageAlreadyPresent bool) {
	if usageAlreadyPresent || ctx == nil {
		return
	}

	deferredChan, ok := ctx.Value(schemas.BifrostContextKeyDeferredUsage).(<-chan *schemas.BifrostLLMUsage)
	if !ok || deferredChan == nil {
		return
	}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		// Large-response phase B closes this channel after trailing usage extraction completes.
		deferredUsage, chanOpen := <-deferredChan
		if !chanOpen || deferredUsage == nil {
			return
		}

		// Acquire semaphore — drop if all slots busy to prevent unbounded goroutines
		// from exhausting DB connections when Postgres is slow
		select {
		case p.deferredUsageSem <- struct{}{}:
			defer func() { <-p.deferredUsageSem }()
		default:
			p.logger.Warn("deferred usage update dropped for request %s: semaphore full", requestID)
			return
		}
		usageUpdates := map[string]interface{}{
			"prompt_tokens":     deferredUsage.PromptTokens,
			"completion_tokens": deferredUsage.CompletionTokens,
			"total_tokens":      deferredUsage.TotalTokens,
		}
		tempEntry := &logstore.Log{TokenUsageParsed: deferredUsage}
		if serErr := tempEntry.SerializeFields(); serErr == nil {
			usageUpdates["token_usage"] = tempEntry.TokenUsage
			usageUpdates["cached_read_tokens"] = tempEntry.CachedReadTokens
		}

		// Check if log entry present in the store
		// exponential backoff with jitter and 3 retries
		// then fail
		var found bool
		var findErr error
		for i := 0; i < 3; i++ {
			found, findErr = p.store.IsLogEntryPresent(p.ctx, requestID)
			if findErr != nil {
				p.logger.Warn("failed to check if log entry is present for request %s: %v", requestID, findErr)
				continue
			}
			if found {
				break
			}
			time.Sleep(time.Duration(math.Pow(2, float64(i))) * time.Second * 2)
		}
		if !found {
			p.logger.Warn("log entry not found for request %s after 3 retries. failed to update deferred usage for large payload request", requestID)
			return
		}
		if updErr := p.store.Update(p.ctx, requestID, usageUpdates); updErr != nil {
			p.logger.Warn("failed to update deferred usage for request %s: %v", requestID, updErr)
		}
	}()
}

// RecalculateCostResult represents summary stats from a cost backfill operation
type RecalculateCostResult struct {
	TotalMatched int64 `json:"total_matched"`
	Updated      int   `json:"updated"`
	Skipped      int   `json:"skipped"`
	Remaining    int64 `json:"remaining"`
}

// RecalculateCostProgress represents a progress event from a cost backfill operation.
type RecalculateCostProgress struct {
	TotalMatched int64  `json:"total_matched"`
	Processed    int    `json:"processed"`
	Updated      int    `json:"updated"`
	Skipped      int    `json:"skipped"`
	Remaining    *int64 `json:"remaining,omitempty"`
	Done         bool   `json:"done"`
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
	AttemptTrail       []schemas.KeyAttemptRecord         // Per-attempt key selection history
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
	Status                 string
	Provider               string
	Model                  string
	Object                 string
	InputHistory           []schemas.ChatMessage
	ResponsesInputHistory  []schemas.ResponsesMessage
	Params                 any
	SpeechInput            *schemas.SpeechInput
	TranscriptionInput     *schemas.TranscriptionInput
	OCRInput               *schemas.OCRDocument
	ImageGenerationInput   *schemas.ImageGenerationInput
	ImageEditInput         *schemas.ImageEditInput
	ImageVariationInput    *schemas.ImageVariationInput
	VideoGenerationInput   *schemas.VideoGenerationInput
	Tools                  []schemas.ChatTool
	RoutingEngineUsed      []string
	Metadata               map[string]any
	PassthroughRequestBody string // Raw body for passthrough requests (UTF-8)
}

// LogCallback is a function that gets called when a new log entry is created
type LogCallback func(ctx context.Context, logEntry *logstore.Log)

// MCPToolLogCallback is a function that gets called when a new MCP tool log entry is created or updated
type MCPToolLogCallback func(*logstore.MCPToolLog)

type Config struct {
	DisableContentLogging        *bool                  `json:"disable_content_logging"`
	RetainContentInObjectStorage *bool                  `json:"retain_content_in_object_storage"` // Pointer to live config value; when true, content-disabled requests are offloaded to object storage as hidden instead of dropped
	LoggingHeaders               *[]string              `json:"logging_headers"`                  // Pointer to live config slice; changes are reflected immediately without restart
	Writer                       *logstore.WriterConfig `json:"writer,omitempty"`
	ObjectStorageEnabled         bool                   `json:"-"` // Set by the server from the logstore config; required for retain_content_in_object_storage to take effect
}

func validateWriterConfig(config logstore.WriterConfig) error {
	if config.MaxBatchSize <= 0 {
		return fmt.Errorf("writer max_batch_size must be greater than 0")
	}
	if config.BatchInterval == "" {
		return fmt.Errorf("writer batch_interval is required")
	}
	batchInterval, err := time.ParseDuration(config.BatchInterval)
	if err != nil {
		return fmt.Errorf("writer batch_interval must be a valid Go duration: %w", err)
	}
	if batchInterval <= 0 {
		return fmt.Errorf("writer batch_interval must be greater than 0")
	}
	if config.MaxBatchBytes <= 0 {
		return fmt.Errorf("writer max_batch_bytes must be greater than 0")
	}
	if config.WriteQueueCapacity <= 0 {
		return fmt.Errorf("writer write_queue_capacity must be greater than 0")
	}
	if config.DeferredUsageConcurrency <= 0 {
		return fmt.Errorf("writer deferred_usage_concurrency must be greater than 0")
	}
	return nil
}

// LoggerPlugin implements the schemas.LLMPlugin and schemas.MCPPlugin interfaces
type LoggerPlugin struct {
	ctx                          context.Context
	store                        logstore.LogStore
	disableContentLogging        *bool
	retainContentInObjectStorage *bool     // Pointer to live config value; when true, content-disabled requests are stored hidden instead of dropped
	objectStorageEnabled         bool      // Log store offloads payloads to object storage; required for retain_content_in_object_storage
	retainWarnOnce               sync.Once // Warns once when retention is configured without object storage
	loggingHeaders               *[]string // Pointer to live config slice for headers to capture in metadata
	pricingManager               *modelcatalog.ModelCatalog
	mcpCatalog                   *mcpcatalog.MCPCatalog // MCP catalog for tool cost calculation
	mu                           sync.Mutex
	done                         chan struct{}
	cleanupOnce                  sync.Once // Ensures cleanup only runs once
	wg                           sync.WaitGroup
	logger                       schemas.Logger
	logCallback                  LogCallback
	mcpToolLogCallback           MCPToolLogCallback // Callback for MCP tool log entries
	droppedRequests              atomic.Int64
	cleanupTicker                *time.Ticker          // Ticker for cleaning up old processing logs
	logMsgPool                   sync.Pool             // Pool for reusing LogMessage structs
	updateDataPool               sync.Pool             // Pool for reusing UpdateLogData structs
	pendingLogsEntries           sync.Map              // Maps requestID -> *PendingLogData (PreLLMHook input data awaiting PostLLMHook)
	pendingLogsToInject          sync.Map              // Maps traceID -> *pendingInjectEntries (log entries to inject, supports multiple per trace)
	pendingMCPLogsToInject       sync.Map              // Maps mcpLogID -> *logstore.MCPToolLog (PreMCPHook input data awaiting PostMCPHook)
	writerConfig                 logstore.WriterConfig // Resolved async writer queue and batch settings
	writeQueue                   chan *writeQueueEntry // Buffered channel for batch write queue
	closed                       atomic.Bool           // Set during cleanup to prevent sends on closed writeQueue
	deferredUsageSem             chan struct{}         // Limits concurrent deferred usage DB updates
	clusterNodeID                atomic.Value          // Cluster node ID (string) for log attribution in clustered deployments
	batchCtx                     context.Context       // Cancelled by Cleanup to stop the batchWriter goroutine before any further DB work
	batchCancel                  context.CancelFunc    // Cancels batchCtx
	batchWriterDone              chan struct{}         // Closed by batchWriter on exit; receiving from it transfers writeQueue ownership to Cleanup
	recoveredBatch               []*writeQueueEntry    // batchWriter parks its in-memory batch here before exiting; safe to read after batchWriterDone closes (happens-before)
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

	writerConfig := config.Writer.WithDefaults()
	if err := validateWriterConfig(writerConfig); err != nil {
		return nil, err
	}
	logger.Info("initializing logging writer settings: max_batch_size=%d batch_interval=%s max_batch_bytes=%d write_queue_capacity=%d deferred_usage_concurrency=%d",
		writerConfig.MaxBatchSize,
		writerConfig.BatchInterval,
		writerConfig.MaxBatchBytes,
		writerConfig.WriteQueueCapacity,
		writerConfig.DeferredUsageConcurrency,
	)

	batchCtx, batchCancel := context.WithCancel(ctx)
	plugin := &LoggerPlugin{
		ctx:                          ctx,
		store:                        logsStore,
		pricingManager:               pricingManager,
		mcpCatalog:                   mcpCatalog,
		disableContentLogging:        config.DisableContentLogging,
		retainContentInObjectStorage: config.RetainContentInObjectStorage,
		objectStorageEnabled:         config.ObjectStorageEnabled,
		loggingHeaders:               config.LoggingHeaders,
		done:                         make(chan struct{}),
		logger:                       logger,
		writerConfig:                 writerConfig,
		writeQueue:                   make(chan *writeQueueEntry, writerConfig.WriteQueueCapacity),
		deferredUsageSem:             make(chan struct{}, writerConfig.DeferredUsageConcurrency),
		batchCtx:                     batchCtx,
		batchCancel:                  batchCancel,
		batchWriterDone:              make(chan struct{}),
		logMsgPool: sync.Pool{
			New: func() any {
				return &LogMessage{}
			},
		},
		updateDataPool: sync.Pool{
			New: func() any {
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

	// Start the batch writer goroutine (single writer for all DB writes)
	plugin.wg.Add(1)
	go plugin.batchWriter()

	return plugin, nil
}

// SetClusterNodeID sets the cluster node ID that will be attached to all log entries.
// Used in clustered deployments to attribute log entries to specific nodes for
// disconnected node usage recovery. Uses atomic.Value since it is written at
// startup and read concurrently from request hot paths.
func (p *LoggerPlugin) SetClusterNodeID(nodeID string) {
	p.clusterNodeID.Store(nodeID)
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
// and stale pending log entries from the in-memory map
func (p *LoggerPlugin) cleanupOldProcessingLogs() {
	// Calculate timestamp for 30 minutes ago in UTC to match log entry timestamps
	thirtyMinutesAgo := time.Now().UTC().Add(-1 * 30 * time.Minute)

	// Delete LLM processing logs older than 30 minutes
	if err := p.store.Flush(p.ctx, thirtyMinutesAgo); err != nil {
		p.logger.Warn("failed to cleanup old processing LLM logs: %v", err)
	}

	// Delete MCP tool processing logs older than 30 minutes
	if err := p.store.FlushMCPToolLogs(p.ctx, thirtyMinutesAgo); err != nil {
		p.logger.Warn("failed to cleanup old processing MCP tool logs: %v", err)
	}

	// Clean up stale pending log entries (requests where PostLLMHook never fired)
	p.cleanupStalePendingLogs()
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

	var metadata map[string]any

	// Check configured logging headers (supports wildcard patterns like "x-custom-*")
	if p.loggingHeaders != nil {
		for _, h := range *p.loggingHeaders {
			pattern := strings.ToLower(strings.TrimSpace(h))
			for hKey, hVal := range allHeaders {
				if schemas.MatchHeaderPattern(hKey, pattern) {
					if metadata == nil {
						metadata = make(map[string]any)
					}
					metadata[hKey] = hVal
				}
			}
		}
	}

	// Check x-bf-lh-* prefixed headers
	for key, val := range allHeaders {
		if labelName, ok := strings.CutPrefix(key, "x-bf-lh-"); ok && labelName != "" {
			if metadata == nil {
				metadata = make(map[string]any)
			}
			metadata[labelName] = val
		}
	}

	// Include x-bf-dim-* dimensions in metadata.
	if dims, ok := ctx.Value(schemas.BifrostContextKeyDimensions).(map[string]string); ok {
		for k, v := range dims {
			if metadata == nil {
				metadata = make(map[string]any)
			}
			if _, exists := metadata[k]; !exists {
				metadata[k] = v
			}
		}
	}

	return metadata
}

// PreRequestHook implements schemas.LLMPlugin (no-op — required for plugin indexing).
func (p *LoggerPlugin) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
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

	p.logger.Debug("PreLLMHook: request %s type=%q", requestID, req.RequestType)

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
	if req.RequestType == schemas.RealtimeRequest {
		initialData.Object = "realtime.turn"
	}

	if p.contentLoggingEnabled(ctx) {
		inputHistory, responsesInputHistory := p.extractInputHistory(req)
		initialData.InputHistory = inputHistory
		initialData.ResponsesInputHistory = responsesInputHistory

		switch req.RequestType {
		case schemas.TextCompletionRequest, schemas.TextCompletionStreamRequest:
			initialData.Params = req.TextCompletionRequest.Params
		case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest:
			initialData.Params = req.ChatRequest.Params
			initialData.Tools = req.ChatRequest.Params.Tools
		case schemas.ResponsesRequest, schemas.ResponsesStreamRequest, schemas.WebSocketResponsesRequest:
			initialData.Params = req.ResponsesRequest.Params

			var tools []schemas.ChatTool
			for _, tool := range req.ResponsesRequest.Params.Tools {
				tools = append(tools, *tool.ToChatTool())
			}
			initialData.Tools = tools
		case schemas.RealtimeRequest:
			if req.ResponsesRequest != nil {
				initialData.Params = req.ResponsesRequest.Params
				if req.ResponsesRequest.Params != nil {
					var tools []schemas.ChatTool
					for _, tool := range req.ResponsesRequest.Params.Tools {
						tools = append(tools, *tool.ToChatTool())
					}
					initialData.Tools = tools
				}
			}
		case schemas.EmbeddingRequest:
			initialData.Params = req.EmbeddingRequest.Params
		case schemas.RerankRequest:
			initialData.Params = req.RerankRequest.Params
		case schemas.OCRRequest:
			initialData.Params = req.OCRRequest.Params
			initialData.OCRInput = &req.OCRRequest.Document
		case schemas.SpeechRequest, schemas.SpeechStreamRequest:
			initialData.Params = req.SpeechRequest.Params
			initialData.SpeechInput = req.SpeechRequest.Input
		case schemas.TranscriptionRequest, schemas.TranscriptionStreamRequest:
			initialData.Params = req.TranscriptionRequest.Params
			input := req.TranscriptionRequest.Input
			if input != nil {
				reqThreshold, _ := ctx.Value(schemas.BifrostContextKeyLargePayloadRequestThreshold).(int64)
				if reqThreshold > 0 && int64(len(input.File)) > reqThreshold {
					// Strip binary file content when it exceeds the large payload threshold
					// to avoid serializing multi-MB audio into the log database.
					logInput := *input
					logInput.File = nil
					initialData.TranscriptionInput = &logInput
				} else {
					initialData.TranscriptionInput = input
				}
			}
		case schemas.ImageGenerationRequest, schemas.ImageGenerationStreamRequest:
			initialData.Params = req.ImageGenerationRequest.Params
			initialData.ImageGenerationInput = req.ImageGenerationRequest.Input
		case schemas.ImageEditRequest, schemas.ImageEditStreamRequest:
			params := req.ImageEditRequest.Params
			input := req.ImageEditRequest.Input
			if input != nil {
				reqThreshold, _ := ctx.Value(schemas.BifrostContextKeyLargePayloadRequestThreshold).(int64)
				if reqThreshold > 0 {
					var totalSize int64
					for _, img := range input.Images {
						totalSize += int64(len(img.Image))
					}
					if totalSize > reqThreshold {
						logInput := *input
						logInput.Images = nil
						initialData.ImageEditInput = &logInput
					} else {
						initialData.ImageEditInput = input
					}
					if params != nil && int64(len(params.Mask)) > reqThreshold {
						logParams := *params
						logParams.Mask = nil
						initialData.Params = &logParams
					} else {
						initialData.Params = params
					}
				} else {
					initialData.ImageEditInput = input
					initialData.Params = params
				}
			} else {
				initialData.Params = params
			}
		case schemas.ImageVariationRequest:
			initialData.Params = req.ImageVariationRequest.Params
			input := req.ImageVariationRequest.Input
			if input != nil {
				reqThreshold, _ := ctx.Value(schemas.BifrostContextKeyLargePayloadRequestThreshold).(int64)
				if reqThreshold > 0 && int64(len(input.Image.Image)) > reqThreshold {
					logInput := *input
					logInput.Image = schemas.ImageInput{}
					initialData.ImageVariationInput = &logInput
				} else {
					initialData.ImageVariationInput = input
				}
			}
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
		case schemas.PassthroughRequest, schemas.PassthroughStreamRequest:
			initialData.Params = &schemas.PassthroughLogParams{
				Method:   req.PassthroughRequest.Method,
				Path:     req.PassthroughRequest.Path,
				RawQuery: req.PassthroughRequest.RawQuery,
				Model:    req.PassthroughRequest.Model,
			}
			if len(req.PassthroughRequest.Body) > 0 {
				ct := strings.ToLower(req.PassthroughRequest.SafeHeaders["content-type"])
				if strings.Contains(ct, "application/json") {
					initialData.PassthroughRequestBody = string(req.PassthroughRequest.Body)
				}
			}
		}
	}

	// Capture configured logging headers and x-bf-lh-* headers into metadata first
	initialData.Metadata = mergeRealtimeMetadata(p.captureLoggingHeaders(ctx), ctx)

	// System entries are set after so they take precedence over dynamic header values
	if isAsync, ok := ctx.Value(schemas.BifrostIsAsyncRequest).(bool); ok && isAsync {
		if initialData.Metadata == nil {
			initialData.Metadata = make(map[string]interface{})
		}
		initialData.Metadata["isAsyncRequest"] = true
	}

	// If fallback request ID is present, use it instead of the primary request ID
	// Determine effective request ID (fallback override)
	effectiveRequestID := requestID
	var parentRequestID string
	if directParentRequestID, ok := ctx.Value(schemas.BifrostContextKeyParentRequestID).(string); ok && directParentRequestID != "" {
		parentRequestID = directParentRequestID
	}
	fallbackRequestID, ok := ctx.Value(schemas.BifrostContextKeyFallbackRequestID).(string)
	if ok && fallbackRequestID != "" {
		effectiveRequestID = fallbackRequestID
		if parentRequestID == "" {
			parentRequestID = requestID
		}
	}

	fallbackIndex := bifrost.GetIntFromContext(ctx, schemas.BifrostContextKeyFallbackIndex)
	// Get routing engines array
	routingEngines := []string{}
	if engines, ok := ctx.Value(schemas.BifrostContextKeyRoutingEnginesUsed).([]string); ok {
		routingEngines = engines
	}

	initialData.RoutingEngineUsed = routingEngines
	initialData.Status = logStatusProcessing

	// Store input data in pendingLogs for later combination with PostLLMHook output.
	// No DB write here - the write is deferred to PostLLMHook to halve total writes.
	pending := &PendingLogData{
		RequestID:          effectiveRequestID,
		ParentRequestID:    parentRequestID,
		Timestamp:          createdTimestamp,
		FallbackIndex:      fallbackIndex,
		RoutingEnginesUsed: routingEngines,
		InitialData:        initialData,
		CreatedAt:          time.Now(),
		Status:             logStatusProcessing,
	}
	// Seed LastActivity so the first idle-eviction check has a baseline even if no
	// PostLLMHook chunk has fired yet.
	pending.LastActivity.Store(pending.CreatedAt.UnixNano())
	p.pendingLogsEntries.Store(effectiveRequestID, pending)
	// Call callback synchronously for immediate UI feedback (WebSocket "processing" notification).
	// The entry does not exist in the DB yet - it will be written when PostLLMHook fires.
	p.mu.Lock()
	callback := p.logCallback
	p.mu.Unlock()
	if callback != nil {
		callback(p.ctx, buildInitialLogEntry(pending))
	}
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
	selectedPromptName := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeySelectedPromptName)
	selectedPromptVersion := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeySelectedPromptVersion)
	selectedPromptID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeySelectedPromptID)
	teamID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceTeamID)
	teamName := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceTeamName)
	customerID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceCustomerID)
	customerName := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceCustomerName)
	userID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyUserID)
	userName := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyUserName)
	businessUnitID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceBusinessUnitID)
	businessUnitName := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceBusinessUnitName)
	numberOfRetries := bifrost.GetIntFromContext(ctx, schemas.BifrostContextKeyNumberOfRetries)
	attemptTrail, _ := ctx.Value(schemas.BifrostContextKeyAttemptTrail).([]schemas.KeyAttemptRecord)
	providerRequestID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyProviderRequestID)
	providerRequestIDHeader := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyProviderRequestIDHeader)
	providerRequestIDTrail, _ := ctx.Value(schemas.BifrostContextKeyProviderRequestIDTrail).([]schemas.ProviderRequestIDRecord)

	requestType, _, originalModelRequested, resolvedModelUsed := bifrost.GetResponseFields(result, bifrostErr)
	resolvedKeyAlias := bifrost.GetResponseRoutingInfo(result, bifrostErr).ResolvedKeyAlias
	shouldStoreRaw, _ := ctx.Value(schemas.BifrostContextKeyShouldStoreRawInLogs).(bool)
	contentLoggingEnabled := p.contentLoggingEnabled(ctx)

	isFinalChunk := bifrost.IsFinalChunk(ctx)

	p.logger.Debug("PostLLMHook: request %s type=%q isFinalChunk=%v hasError=%v", requestID, requestType, isFinalChunk, bifrostErr != nil)

	// Retrieve pending input data from PreLLMHook
	var pendingVal any
	var hasPending bool
	if !bifrost.IsStreamRequestType(requestType) || isFinalChunk || bifrostErr != nil {
		pendingVal, hasPending = p.pendingLogsEntries.LoadAndDelete(requestID)
	} else {
		pendingVal, hasPending = p.pendingLogsEntries.Load(requestID)
	}

	p.logger.Debug("PostLLMHook: pending data lookup for request %s: found=%v", requestID, hasPending)

	if !hasPending {
		// If we have an error (e.g., cancellation/timeout), still write a minimal error entry
		// so the error is visible in logs. Without PreLLMHook's DB insert, silently returning
		// here means the error is completely lost.
		if bifrostErr != nil {
			p.logger.Warn("no pending log data found for request %s, writing minimal error entry", requestID)
			entry := &logstore.Log{
				ID:        requestID,
				Provider:  string(bifrostErr.ExtraFields.Provider),
				Status:    logStatusForError(bifrostErr),
				Object:    string(requestType),
				Stream:    bifrost.IsStreamRequestType(requestType),
				Timestamp: time.Now().UTC(),
				CreatedAt: time.Now().UTC(),
			}
			entry.MetadataParsed = mergeRealtimeMetadata(p.captureLoggingHeaders(ctx), ctx)
			if isAsync, ok := ctx.Value(schemas.BifrostIsAsyncRequest).(bool); ok && isAsync {
				if entry.MetadataParsed == nil {
					entry.MetadataParsed = make(map[string]interface{})
				}
				entry.MetadataParsed["isAsyncRequest"] = true
			}
			applyModelAlias(entry, originalModelRequested, resolvedModelUsed)
			entry.ProviderRequestID = providerRequestID
			entry.ProviderRequestIDHeader = providerRequestIDHeader
			if len(providerRequestIDTrail) > 0 {
				entry.ProviderRequestIDTrailParsed = providerRequestIDTrail
			}
			applyResolvedAliasInfo(entry, resolvedKeyAlias)
			entry.ErrorDetailsParsed = sanitizeErrorForLogging(bifrostErr, contentLoggingEnabled, shouldStoreRaw)
			if nodeID, _ := p.clusterNodeID.Load().(string); nodeID != "" {
				entry.ClusterNodeID = &nodeID
			}
			applyLargePayloadPreviewsToEntry(ctx, entry, contentLoggingEnabled)
			p.storeOrEnqueueEntry(ctx, entry, p.makePostWriteCallback(nil))
		} else {
			p.logger.Warn("no pending log data found for request %s, skipping log write", requestID)
		}
		return result, bifrostErr, nil
	}

	pending := pendingVal.(*PendingLogData)

	// Refresh the idle clock on every PostLLMHook call (notably each streaming
	// chunk) so a long-running stream is not evicted by cleanupStalePendingLogs
	// before it finishes. Safe to mutate in place: pending is a pointer held in
	// the sync.Map, and LastActivity is atomic.
	pending.LastActivity.Store(time.Now().UnixNano())

	// Should never happen, but just in case
	// Fallback to request type from pending data if request type is not set
	if requestType == "" {
		requestType = schemas.RequestType(pending.InitialData.Object)
		p.logger.Warn("PostLLMHook: request type missing from response extra fields for request %s, falling back to pre-hook value %q", requestID, requestType)
	}

	var tracer schemas.Tracer
	var traceID string
	if bifrost.IsStreamRequestType(requestType) && requestType != schemas.RealtimeRequest {
		var err error
		tracer, traceID, err = bifrost.GetTracerFromContext(ctx)
		if err != nil {
			p.logger.Debug("tracer not available in logging plugin posthook: %v", err)
			// Continue with nil tracer — the rest of the code handles this gracefully
			// via `if tracer != nil && traceID != ""` guards
		}
	}

	// For non-final streaming chunks, process the accumulator synchronously
	// and skip the write queue entirely. The accumulator work (ProcessStreamingChunk)
	// is fast (mutex + append). Only final chunks, errors, and non-streaming
	// responses need a DB write.
	if bifrost.IsStreamRequestType(requestType) && requestType != schemas.RealtimeRequest && !isFinalChunk && result != nil && bifrostErr == nil {
		if tracer != nil && traceID != "" {
			tracer.ProcessStreamingChunk(ctx, traceID, false, result, bifrostErr)
		}
		return result, bifrostErr, nil
	}
	// Extract routing engine logs from context before entering goroutine
	routingEngineLogs := formatRoutingEngineLogs(ctx.GetRoutingEngineLogs())
	if requestType == schemas.RealtimeRequest {
		if resolvedRealtimeSessionID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyRealtimeSessionID); resolvedRealtimeSessionID != "" {
			pending.ParentRequestID = resolvedRealtimeSessionID
		}
		pending.InitialData.Metadata = mergeRealtimeMetadata(pending.InitialData.Metadata, ctx)
		if routingEngines, ok := ctx.Value(schemas.BifrostContextKeyRoutingEnginesUsed).([]string); ok {
			pending.InitialData.RoutingEngineUsed = routingEngines
			pending.RoutingEnginesUsed = routingEngines
		}
	}

	// Build the complete log entry with input (from PreLLMHook) + output (from PostLLMHook)
	entry := buildCompleteLogEntryFromPending(pending)
	// Apply common output fields. For cache hits, prefer the cache-serve
	// latency stamped by the semantic cache plugin over the original provider
	// latency preserved in the cached response.
	var latency int64
	if result != nil {
		ef := result.GetExtraFields()
		latency = ef.Latency
		// Model that actually served the turn when the provider swapped models inside
		// one call. entry.Model still names what the caller asked for.
		if ef.RoutingInfo.ServerSideFallbackModel != nil {
			served := *ef.RoutingInfo.ServerSideFallbackModel
			entry.ServerSideFallbackModel = &served
		}
		if ef.CacheDebug != nil && ef.CacheDebug.CacheHit && ef.CacheDebug.CacheHitLatency != nil {
			latency = *ef.CacheDebug.CacheHitLatency
		}
	} else if bifrostErr != nil {
		latency = bifrostErr.ExtraFields.Latency
	}

	if entry.ServerSideFallbackModel == nil && bifrostErr != nil && bifrostErr.ExtraFields.BilledUsage != nil {
		if served := bifrostErr.ExtraFields.BilledUsage.ServerSideFallbackModel; served != nil {
			m := *served
			entry.ServerSideFallbackModel = &m
		}
	}
	applyOutputFieldsToEntry(entry, selectedKeyID, selectedKeyName, virtualKeyID, virtualKeyName, routingRuleID, routingRuleName, selectedPromptID, selectedPromptName, selectedPromptVersion, teamID, teamName, customerID, customerName, userID, userName, businessUnitID, businessUnitName, numberOfRetries, latency, attemptTrail, providerRequestID, providerRequestIDHeader, providerRequestIDTrail)
	applyResolvedAliasInfo(entry, resolvedKeyAlias)
	// Attach cluster governance metadata for disconnected node usage recovery
	if nodeID, _ := p.clusterNodeID.Load().(string); nodeID != "" {
		entry.ClusterNodeID = &nodeID
	}
	if budgetIDs, ok := ctx.Value(schemas.BifrostContextKeyGovernanceBudgetIDs).([]string); ok && len(budgetIDs) > 0 {
		entry.BudgetIDsParsed = budgetIDs
	}
	if rateLimitIDs, ok := ctx.Value(schemas.BifrostContextKeyGovernanceRateLimitIDs).([]string); ok && len(rateLimitIDs) > 0 {
		entry.RateLimitIDsParsed = rateLimitIDs
	}
	if teamIDs, ok := ctx.Value(schemas.BifrostContextKeyGovernanceTeamIDs).([]string); ok && len(teamIDs) > 0 {
		entry.TeamIDsParsed = teamIDs
	}
	if teamNames, ok := ctx.Value(schemas.BifrostContextKeyGovernanceTeamNames).([]string); ok && len(teamNames) > 0 {
		entry.TeamNamesParsed = teamNames
	}
	if buIDs, ok := ctx.Value(schemas.BifrostContextKeyGovernanceBusinessUnitIDs).([]string); ok && len(buIDs) > 0 {
		entry.BusinessUnitIDsParsed = buIDs
	}
	if buNames, ok := ctx.Value(schemas.BifrostContextKeyGovernanceBusinessUnitNames).([]string); ok && len(buNames) > 0 {
		entry.BusinessUnitNamesParsed = buNames
	}
	if customerIDs, ok := ctx.Value(schemas.BifrostContextKeyGovernanceCustomerIDs).([]string); ok && len(customerIDs) > 0 {
		entry.CustomerIDsParsed = customerIDs
	}
	if customerNames, ok := ctx.Value(schemas.BifrostContextKeyGovernanceCustomerNames).([]string); ok && len(customerNames) > 0 {
		entry.CustomerNamesParsed = customerNames
	}
	entry.MetadataParsed = pending.InitialData.Metadata
	entry.MetadataParsed = mergeRealtimeMetadata(entry.MetadataParsed, ctx)
	entry.RoutingEngineLogs = routingEngineLogs

	// Branch based on response type to populate output-specific fields

	// Path A: Error with nil result
	if result == nil && bifrostErr != nil {
		entry.Status = logStatusForError(bifrostErr)
		applyModelAlias(entry, originalModelRequested, resolvedModelUsed)
		if bifrost.IsStreamRequestType(requestType) {
			entry.Stream = true
		}

		// For streaming errors, finalize and read accumulated chunks so logs retain pre-error stream metadata
		if bifrost.IsStreamRequestType(requestType) &&
			requestType != schemas.RealtimeRequest &&
			tracer != nil &&
			traceID != "" {
			if accResult := tracer.ProcessStreamingChunk(ctx, traceID, true, result, bifrostErr); accResult != nil {
				if streamResponse := convertToProcessedStreamResponse(accResult, requestType); streamResponse != nil {
					p.applyStreamingOutputToEntry(entry, streamResponse, shouldStoreRaw, contentLoggingEnabled)
				}
			}
			tracer.CleanupStreamAccumulator(traceID)
		}

		entry.ErrorDetailsParsed = sanitizeErrorForLogging(bifrostErr, contentLoggingEnabled, shouldStoreRaw)
		if shouldStoreRaw && contentLoggingEnabled {
			if bifrostErr.ExtraFields.RawRequest != nil {
				rawReqBytes, err := sonic.Marshal(bifrostErr.ExtraFields.RawRequest)
				if err == nil {
					entry.RawRequest = string(rawReqBytes)
				}
			}

			if entry.RawResponse == "" && bifrostErr.ExtraFields.RawResponse != nil {
				rawRespBytes, err := sonic.Marshal(bifrostErr.ExtraFields.RawResponse)
				if err == nil {
					entry.RawResponse = string(rawRespBytes)
				}
			}
		}
		// The request failed/was cancelled but the provider still
		// processed tokens (carried on BilledUsage). Record cost + tokens so the
		// logs DB reflects what we were actually billed, mirroring the governance
		// budget.
		p.applyErrorBillingFromBilledUsage(ctx, entry, bifrostErr.ExtraFields.BilledUsage, requestType)
		applyLargePayloadPreviewsToEntry(ctx, entry, contentLoggingEnabled)
		p.storeOrEnqueueEntry(ctx, entry, p.makePostWriteCallback(nil))
		p.scheduleDeferredUsageUpdate(ctx, requestID, entry.TokenUsageParsed != nil)
		return result, bifrostErr, nil
	}

	// Path B: Streaming final chunk
	if bifrost.IsStreamRequestType(requestType) && requestType != schemas.RealtimeRequest {
		var streamResponse *streaming.ProcessedStreamResponse
		if tracer != nil && traceID != "" {
			accResult := tracer.ProcessStreamingChunk(ctx, traceID, isFinalChunk, result, bifrostErr)
			if accResult != nil {
				streamResponse = convertToProcessedStreamResponse(accResult, requestType)
			}
		}

		if bifrostErr != nil {
			entry.Status = logStatusForError(bifrostErr)
			entry.Stream = true
			applyModelAlias(entry, originalModelRequested, resolvedModelUsed)
			entry.ErrorDetailsParsed = sanitizeErrorForLogging(bifrostErr, contentLoggingEnabled, shouldStoreRaw)
			// Backfill raw request/response on streaming-error path so cancellation/timeout
			// log entries still carry raw payloads when content logging + raw storage are
			// enabled. Mirrors the non-streaming Path A pattern at line 872. Prefer the
			// accumulator-captured raw bytes (streamResponse), then fall back to whatever
			// the provider attached to the BifrostError.
			if shouldStoreRaw && contentLoggingEnabled {
				if entry.RawRequest == "" {
					if streamResponse != nil && streamResponse.RawRequest != nil && *streamResponse.RawRequest != nil {
						switch raw := (*streamResponse.RawRequest).(type) {
						case string:
							entry.RawRequest = raw
						default:
							if rawReqBytes, err := sonic.Marshal(raw); err == nil {
								entry.RawRequest = string(rawReqBytes)
							}
						}
					} else if bifrostErr.ExtraFields.RawRequest != nil {
						if rawReqBytes, err := sonic.Marshal(bifrostErr.ExtraFields.RawRequest); err == nil {
							entry.RawRequest = string(rawReqBytes)
						}
					}
				}
				if entry.RawResponse == "" {
					if streamResponse != nil && streamResponse.Data != nil && streamResponse.Data.RawResponse != nil {
						entry.RawResponse = *streamResponse.Data.RawResponse
					} else if bifrostErr.ExtraFields.RawResponse != nil {
						if rawRespBytes, err := sonic.Marshal(bifrostErr.ExtraFields.RawResponse); err == nil {
							entry.RawResponse = string(rawRespBytes)
						}
					}
				}
			}
		} else if streamResponse == nil {
			// tracer or traceID not available, or accumulator returned nil - still write what we have
			entry.Status = logStatusSuccess
			entry.Stream = true
			applyModelAlias(entry, originalModelRequested, resolvedModelUsed)
		} else if isFinalChunk {
			// Apply streaming output fields to the entry
			entry.Stream = true
			p.applyStreamingOutputToEntry(entry, streamResponse, shouldStoreRaw, contentLoggingEnabled)
		}
		if entry.ErrorDetailsParsed != nil {
			entry.Status = logStatusForError(entry.ErrorDetailsParsed)
		}
		// Backfill passthrough status_code from response (streaming path)
		if result != nil && result.PassthroughResponse != nil {
			if params, ok := entry.ParamsParsed.(*schemas.PassthroughLogParams); ok {
				params.StatusCode = result.PassthroughResponse.StatusCode
			}
			if contentLoggingEnabled && len(result.PassthroughResponse.Body) > 0 {
				entry.PassthroughResponseBody = string(result.PassthroughResponse.Body)
			}
			// Flip status for passthrough error responses (4xx/5xx from provider)
			if isPassthroughErrorResponse(result) {
				entry.Status = logStatusError
			}
			// Compute cost for streaming passthrough using StreamUsage set by the accumulator.
			if entry.Cost == nil && p.pricingManager != nil && result.PassthroughResponse.PassthroughUsage != nil {
				pricingScopes := modelcatalog.PricingLookupScopesFromContext(ctx, string(entry.Provider))
				if cost := p.pricingManager.CalculateCost(result, pricingScopes); cost > 0 {
					entry.Cost = &cost
				}
			}
		}
		applyLargePayloadPreviewsToEntry(ctx, entry, contentLoggingEnabled)
		if tracer != nil && traceID != "" {
			tracer.CleanupStreamAccumulator(traceID)
		}
		p.storeOrEnqueueEntry(ctx, entry, p.makePostWriteCallback(nil))
		p.scheduleDeferredUsageUpdate(ctx, requestID, entry.TokenUsageParsed != nil)
		return result, bifrostErr, nil
	}

	// Path C: Non-streaming response
	if bifrostErr != nil {
		entry.Status = logStatusForError(bifrostErr)
		applyModelAlias(entry, originalModelRequested, resolvedModelUsed)
		entry.ErrorDetailsParsed = sanitizeErrorForLogging(bifrostErr, contentLoggingEnabled, shouldStoreRaw)
		// Realtime turns that fail mid-stream still need their input transcript
		// surfaced — backfill from bifrostErr.ExtraFields.RawRequest if present.
		if requestType == schemas.RealtimeRequest {
			applyRealtimeRawRequestBackfill(entry, bifrostErr.ExtraFields.RawRequest, contentLoggingEnabled, shouldStoreRaw)
		}
	} else if result != nil {
		entry.Status = logStatusSuccess
		extraFields := result.GetExtraFields()
		applyModelAlias(entry, extraFields.OriginalModelRequested, extraFields.ResolvedModelUsed)
		if requestType == schemas.RealtimeRequest {
			p.applyRealtimeOutputToEntry(entry, result, shouldStoreRaw, contentLoggingEnabled)
		} else {
			p.applyNonStreamingOutputToEntry(entry, result, shouldStoreRaw, contentLoggingEnabled)
		}
		// Flip status for passthrough error responses (4xx/5xx from provider)
		if isPassthroughErrorResponse(result) {
			entry.Status = logStatusError
		}
	}
	applyLargePayloadPreviewsToEntry(ctx, entry, contentLoggingEnabled)

	// Calculate cost
	var cacheDebug *schemas.BifrostCacheDebug
	if result != nil {
		cacheDebug = result.GetExtraFields().CacheDebug
	}
	entry.CacheDebugParsed = cacheDebug
	if p.pricingManager != nil {
		pricingScopes := modelcatalog.PricingLookupScopesFromContext(ctx, string(entry.Provider))
		if cost := p.pricingManager.CalculateCost(result, pricingScopes); cost > 0 {
			entry.Cost = &cost
		}
	}

	// Pre-apply denormalized fields for WebSocket callback enrichment
	if entry.SelectedKeyID != "" && entry.SelectedKeyName != "" {
		entry.SelectedKey = &schemas.Key{
			ID:   entry.SelectedKeyID,
			Name: entry.SelectedKeyName,
		}
	}
	if entry.VirtualKeyID != nil && entry.VirtualKeyName != nil && *entry.VirtualKeyID != "" && *entry.VirtualKeyName != "" {
		entry.VirtualKey = &tables.TableVirtualKey{
			ID:   *entry.VirtualKeyID,
			Name: *entry.VirtualKeyName,
		}
	}
	if entry.RoutingRuleID != nil && entry.RoutingRuleName != nil && *entry.RoutingRuleID != "" && *entry.RoutingRuleName != "" {
		entry.RoutingRule = &tables.TableRoutingRule{
			ID:   *entry.RoutingRuleID,
			Name: *entry.RoutingRuleName,
		}
	}
	p.storeOrEnqueueEntry(ctx, entry, p.makePostWriteCallback(nil))
	p.scheduleDeferredUsageUpdate(ctx, requestID, entry.TokenUsageParsed != nil)
	return result, bifrostErr, nil
}

// Cleanup is called when the plugin is being shut down. It stops the
// batchWriter goroutine before it issues any further DB writes, takes over
// ownership of the write queue, and drains whatever is pending under a
// bounded wall-clock deadline (cleanupDrainTimeout). Any entries that do not
// finish within the deadline are dropped so that a slow or wedged log store
// cannot wedge the server's overall 30s shutdown budget.
func (p *LoggerPlugin) Cleanup() error {
	p.cleanupOnce.Do(func() {
		if p.cleanupTicker != nil {
			p.cleanupTicker.Stop()
		}
		// Signal the cleanup worker to stop.
		close(p.done)
		// Stop new producers before killing batchWriter so the channel does
		// not grow further while we drain it ourselves. Any producer that raced
		// past this check is absorbed by the enqueue recover path.
		p.closed.Store(true)
		// Kill batchWriter. Its current in-memory batch is handed back via
		// p.recoveredBatch; it does not issue any further DB writes.
		p.batchCancel()
		// Receiving from batchWriterDone is the ownership handoff: after this
		// point, no other goroutine reads from p.writeQueue, so we can drain
		// it ourselves. This wait is microseconds (no DB work involved).
		<-p.batchWriterDone
		// Drain p.recoveredBatch and whatever is still buffered in
		// p.writeQueue under a bounded deadline.
		p.drainPending()
		// Close the channel as hygiene. The defer/recover in enqueueLogEntry
		// (writer.go:254-259) absorbs any racing producer send.
		close(p.writeQueue)
		// wg.Wait covers the cleanupWorker (exited via close(p.done)) and
		// any in-flight deferred usage updater goroutines. batchWriter has
		// already called wg.Done before closing batchWriterDone above.
		p.wg.Wait()
	})
	return nil
}

// drainPending processes p.recoveredBatch followed by any entries still
// buffered in p.writeQueue. Runs synchronously under a wall-clock deadline;
// remaining entries past the deadline are counted as dropped.
func (p *LoggerPlugin) drainPending() {
	deadline := time.Now().Add(cleanupDrainTimeout)
	batch := p.recoveredBatch
	p.recoveredBatch = nil

	// Pull everything currently buffered in the channel. Non-blocking — we
	// only want what is there right now; new sends are already blocked by
	// p.closed.
drainQueue:
	for {
		select {
		case entry := <-p.writeQueue:
			batch = append(batch, entry)
		default:
			break drainQueue
		}
	}

	// Process in chunks of writerConfig.MaxBatchSize, checking the wall-clock deadline
	// between chunks so a single slow processBatch cannot consume the whole
	// budget and starve later chunks.
	for len(batch) > 0 {
		if time.Now().After(deadline) {
			p.droppedRequests.Add(int64(len(batch)))
			p.logger.Warn("logging plugin cleanup deadline reached; dropping %d entries", len(batch))
			return
		}
		chunkSize := p.writerConfig.MaxBatchSize
		if chunkSize > len(batch) {
			chunkSize = len(batch)
		}
		p.safeProcessBatch(batch[:chunkSize])
		batch = batch[chunkSize:]
	}
}

// storeOrEnqueueEntry stores a log entry in pendingLogs keyed by traceID for later
// retrieval by Inject(), or enqueues directly if no traceID is available (Go SDK path).
// Multiple entries per traceID are supported (e.g. fallback/retry attempts within the same trace).
func (p *LoggerPlugin) storeOrEnqueueEntry(ctx *schemas.BifrostContext, entry *logstore.Log, callback func(entry *logstore.Log)) {
	policy := p.resolveContentPolicy(ctx)
	// ContentHidden marks entries whose content the API/UI never serves back —
	// both the retained-in-object-storage case and the dropped-entirely case.
	entry.ContentHidden = !policy.visible()
	// Redaction mappings exist to reveal redacted content on permitted UI
	// reads; hidden entries serve no content back, so attach only when the
	// content is actually visible.
	attachLogRedactionData(ctx, entry, policy.visible())
	traceID, _ := ctx.Value(schemas.BifrostContextKeyTraceID).(string)
	if traceID != "" {
		// Append to slice for Inject() to pick up — supports multiple attempts per trace
		existing, loaded := p.pendingLogsToInject.LoadOrStore(traceID, &pendingInjectEntries{entries: []*logstore.Log{entry}, createdAt: time.Now()})
		if !loaded {
			return
		}
		pending := existing.(*pendingInjectEntries)
		pending.mu.Lock()
		pending.entries = append(pending.entries, entry)
		pending.mu.Unlock()
	} else {
		// Fallback: no tracing (Go SDK path), enqueue directly
		p.enqueueLogEntry(entry, callback)
	}
}

// Inject receives a completed trace and writes the log entries with plugin logs to DB.
// This implements the ObservabilityPlugin interface.
func (p *LoggerPlugin) Inject(_ context.Context, trace *schemas.Trace) error {
	if trace == nil {
		return nil
	}
	// Retrieve pending log entries built by PostLLMHook (stored by traceID)
	entryVal, ok := p.pendingLogsToInject.LoadAndDelete(trace.TraceID)
	if !ok {
		return nil
	}
	pending, ok := entryVal.(*pendingInjectEntries)
	if !ok {
		return nil
	}
	// Serialize plugin logs once for all entries
	var pluginLogsJSON string
	if len(trace.PluginLogs) > 0 {
		grouped := schemas.GroupPluginLogsByName(trace.PluginLogs)
		if data, err := sonic.Marshal(grouped); err == nil {
			pluginLogsJSON = string(data)
		}
	}
	p.logger.Debug("Inject: enqueuing %d log entries", len(pending.entries))
	// Enqueue each log entry (supports multiple attempts per trace)
	for _, entry := range pending.entries {
		entry.PluginLogs = pluginLogsJSON
		p.logger.Debug("Inject: enqueuing log entry %s", entry.ID)
		p.enqueueLogEntry(entry, p.makePostWriteCallback(nil))
	}
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

	// Only log for tool execute requests
	if !req.RequestType.IsExecuteTool() {
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
	// Skip logging for codemode meta-tools. Check both the full name (bare,
	// e.g. "executeToolCode") and the suffix after the client prefix (e.g.
	// "myclient-executeToolCode") so PreMCP and PostMCP agree on what to skip
	// and we never leave an orphan pending row to expire via the TTL path.
	if bifrost.IsCodemodeTool(fullToolName) || bifrost.IsCodemodeTool(toolName) {
		return req, nil, nil
	}

	// Get virtual key information from context - using same method as normal LLM logging
	virtualKeyID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceVirtualKeyID)
	virtualKeyName := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceVirtualKeyName)

	// Use the per-tool-call unique MCP log ID (set by agent executor per goroutine) as the
	// primary key. Fall back to requestID if not set (e.g. direct single tool call).
	mcpLogID, ok := ctx.Value(schemas.BifrostContextKeyMCPLogID).(string)
	if !ok || mcpLogID == "" {
		mcpLogID = requestID
	}

	entry := &logstore.MCPToolLog{
		ID:          mcpLogID,
		RequestID:   requestID,
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
	applyMCPGovernanceFieldsToEntry(ctx, entry)

	// Set arguments if content logging is enabled. MCP tool logs have no
	// hidden-content mode, so content is only stored when it is also visible.
	if p.resolveContentPolicy(ctx).visible() {
		entry.ArgumentsParsed = arguments
	}

	// Capture configured logging headers and x-bf-lh-* headers into metadata
	entry.MetadataParsed = p.captureLoggingHeaders(ctx)

	p.pendingMCPLogsToInject.Store(mcpLogID, entry)

	p.mu.Lock()
	callback := p.mcpToolLogCallback
	p.mu.Unlock()
	if callback != nil {
		callback(entry)
	}

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

	// Skip non tool-execute envelopes (Ping/ListTools). The MCP gate stamps
	// MCPRequestType on both the success response and the error, so a single check
	// covers both paths — no pending MCP log entry was created in PreMCPHook for
	// anything but execute-tool requests.
	mcpReqType := schemas.MCPRequestType("")
	if resp != nil {
		mcpReqType = resp.ExtraFields.MCPRequestType
	} else if bifrostErr != nil {
		mcpReqType = bifrostErr.ExtraFields.MCPRequestType
	}
	if !mcpReqType.IsExecuteTool() {
		return resp, bifrostErr, nil
	}
	// Skip logging for codemode tools (executeToolCode, listToolFiles, readToolFile)
	if resp != nil && bifrost.IsCodemodeTool(resp.ExtraFields.ToolName) {
		return resp, bifrostErr, nil
	}

	requestID, ok := ctx.Value(schemas.BifrostContextKeyRequestID).(string)
	if !ok || requestID == "" {
		p.logger.Error("request-id not found in context or is empty in PostMCPHook")
		return resp, bifrostErr, nil
	}

	// Use the per-tool-call unique MCP log ID to find the correct log entry.
	mcpLogID, ok := ctx.Value(schemas.BifrostContextKeyMCPLogID).(string)
	if !ok || mcpLogID == "" {
		mcpLogID = requestID
	}

	// Extract virtual key ID and name from context (set by governance plugin)
	virtualKeyID := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceVirtualKeyID)
	virtualKeyName := bifrost.GetStringFromContext(ctx, schemas.BifrostContextKeyGovernanceVirtualKeyName)

	pendingVal, hasPending := p.pendingMCPLogsToInject.LoadAndDelete(mcpLogID)
	var entry *logstore.MCPToolLog
	if hasPending {
		if pending, ok := pendingVal.(*logstore.MCPToolLog); ok {
			entry = pending
		}
	}
	if entry == nil {
		entry = &logstore.MCPToolLog{
			ID:        mcpLogID,
			RequestID: requestID,
			Timestamp: time.Now().UTC(),
			Status:    "processing",
			CreatedAt: time.Now().UTC(),
		}
	}

	if virtualKeyID != "" {
		entry.VirtualKeyID = &virtualKeyID
	}
	if virtualKeyName != "" {
		entry.VirtualKeyName = &virtualKeyName
	}
	applyMCPGovernanceFieldsToEntry(ctx, entry)
	if resp != nil {
		latency := float64(resp.ExtraFields.Latency)
		entry.Latency = &latency
	}

	success := resp != nil && bifrostErr == nil
	if success && p.mcpCatalog != nil && resp.ExtraFields.ClientName != "" && resp.ExtraFields.ToolName != "" {
		if pricingEntry, ok := p.mcpCatalog.GetPricingData(resp.ExtraFields.ClientName, resp.ExtraFields.ToolName); ok {
			toolCost := pricingEntry.CostPerExecution
			entry.Cost = &toolCost
			p.logger.Debug("MCP tool cost for %s.%s: $%.6f", resp.ExtraFields.ClientName, resp.ExtraFields.ToolName, toolCost)
		}
	}

	if bifrostErr != nil {
		entry.Status = "error"
		shouldStoreRaw, _ := ctx.Value(schemas.BifrostContextKeyShouldStoreRawInLogs).(bool)
		entry.ErrorDetailsParsed = sanitizeErrorForLogging(bifrostErr, p.resolveContentPolicy(ctx).visible(), shouldStoreRaw)
	} else if resp != nil {
		entry.Status = "success"
		// MCP tool logs have no hidden-content mode, so content is only
		// stored when it is also visible.
		if p.resolveContentPolicy(ctx).visible() {
			var result interface{}
			if resp.ChatMessage != nil {
				if resp.ChatMessage.Content != nil && resp.ChatMessage.Content.ContentStr != nil {
					contentStr := *resp.ChatMessage.Content.ContentStr
					var parsedContent interface{}
					if err := sonic.Unmarshal([]byte(contentStr), &parsedContent); err == nil {
						result = parsedContent
					} else {
						result = resp.ChatMessage
					}
				} else {
					result = resp.ChatMessage
				}
			} else if resp.ResponsesMessage != nil {
				result = resp.ResponsesMessage
			}
			if result != nil {
				entry.ResultParsed = result
			}
		}
	} else {
		entry.Status = "error"
		entry.ErrorDetailsParsed = &schemas.BifrostError{
			IsBifrostError: true,
			Error: &schemas.ErrorField{
				Message: "MCP tool execution returned nil response",
			},
		}
	}

	p.mu.Lock()
	callback := p.mcpToolLogCallback
	p.mu.Unlock()
	p.enqueueMCPToolLogEntry(entry, callback)

	return resp, bifrostErr, nil
}
