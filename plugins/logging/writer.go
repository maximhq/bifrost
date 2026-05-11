package logging

import (
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

const (
	// maxBatchSize is the maximum number of entries to collect before flushing
	maxBatchSize = 1000
	// batchInterval is the maximum time to wait before flushing a partial batch
	batchInterval = 2 * time.Second
	// maxBatchBytes is the approximate byte-size ceiling for a batch (100 MB).
	// When the cumulative estimated size of queued entries hits this limit the
	// batch is flushed immediately, even if maxBatchSize hasn't been reached.
	maxBatchBytes = 100 * 1024 * 1024
	// writeQueueCapacity is the buffer size for the write queue channel
	writeQueueCapacity = 10000
	// maxDeferredUsageConcurrency limits concurrent deferred usage DB updates
	maxDeferredUsageConcurrency = 5
	// pendingLogTTL is how long a pending log entry can stay in memory before cleanup
	pendingLogTTL = 5 * time.Minute
)

// PendingLogData holds PreLLMHook input data until PostLLMHook fires.
// Stored in pendingLogs sync.Map keyed by requestID.
type PendingLogData struct {
	RequestID          string
	ParentRequestID    string
	Timestamp          time.Time
	FallbackIndex      int
	Status             string
	RoutingEnginesUsed []string
	InitialData        *InitialLogData
	CreatedAt          time.Time // For cleanup of stale entries
}

// pendingInjectEntries wraps a slice of log entries so it can be used with sync.Map.
// The mutex protects concurrent appends to the entries slice within the same traceID.
type pendingInjectEntries struct {
	mu        sync.Mutex
	entries   []*logstore.Log
	createdAt time.Time
}

// writeQueueEntry is an entry pushed to the batch write queue.
type writeQueueEntry struct {
	log         *logstore.Log
	mcpLog      *logstore.MCPToolLog
	callback    func(entry *logstore.Log)
	mcpCallback func(entry *logstore.MCPToolLog)
}

// batchWriter is the single writer goroutine that drains the write queue
// and processes entries in batched transactions.
func (p *LoggerPlugin) batchWriter() {
	defer p.wg.Done()

	batch := make([]*writeQueueEntry, 0, maxBatchSize)
	batchBytes := 0
	timer := time.NewTimer(batchInterval)
	timer.Stop()
	timerRunning := false

	flush := func() {
		if timerRunning {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timerRunning = false
		}
		p.safeProcessBatch(batch)
		clear(batch)
		batch = batch[:0]
		batchBytes = 0
	}

	for {
		select {
		case entry, ok := <-p.writeQueue:
			if !ok {
				// Channel closed - flush remaining batch and exit
				p.safeProcessBatch(batch)
				return
			}
			batch = append(batch, entry)
			batchBytes += estimateWriteQueueEntrySize(entry)
			if len(batch) >= maxBatchSize || batchBytes >= maxBatchBytes {
				flush()
			} else if !timerRunning {
				timer.Reset(batchInterval)
				timerRunning = true
			}

		case <-timer.C:
			timerRunning = false
			if len(batch) > 0 {
				flush()
			}
		}
	}
}

// safeProcessBatch wraps processBatch with panic recovery so a single
// bad entry cannot kill the batchWriter goroutine.
func (p *LoggerPlugin) safeProcessBatch(batch []*writeQueueEntry) {
	defer func() {
		if r := recover(); r != nil {
			p.logger.Error("panic in batch writer processBatch (recovered, %d entries dropped): %v", len(batch), r)
			p.droppedRequests.Add(int64(len(batch)))
		}
	}()
	p.processBatch(batch)
}

// processBatch executes a batch of log entries in a single database transaction.
func (p *LoggerPlugin) processBatch(batch []*writeQueueEntry) {
	if len(batch) == 0 {
		return
	}

	// Collect all log entries for batch insert
	logs := make([]*logstore.Log, 0, len(batch))
	mcpLogs := make([]*logstore.MCPToolLog, 0, len(batch))
	for _, entry := range batch {
		if entry.log != nil {
			logs = append(logs, entry.log)
		}
		if entry.mcpLog != nil {
			mcpLogs = append(mcpLogs, entry.mcpLog)
		}
	}

	if len(logs) > 0 {
		if err := p.store.BatchCreateIfNotExists(p.ctx, logs); err != nil {
			p.logger.Warn("batch insert failed for %d entries, falling back to individual inserts: %v", len(logs), err)
			// Individual fallback — isolate the bad entry instead of losing the whole batch
			for _, log := range logs {
				if err := p.store.BatchCreateIfNotExists(p.ctx, []*logstore.Log{log}); err != nil {
					p.logger.Warn("individual insert failed for log %s: %v", log.ID, err)
					p.droppedRequests.Add(1)
				}
			}
		}
	}
	if len(mcpLogs) > 0 {
		if err := p.store.BatchCreateMCPToolLogsIfNotExists(p.ctx, mcpLogs); err != nil {
			p.logger.Warn("batch insert failed for %d MCP tool logs, falling back to individual inserts: %v", len(mcpLogs), err)
			for _, log := range mcpLogs {
				if err := p.store.BatchCreateMCPToolLogsIfNotExists(p.ctx, []*logstore.MCPToolLog{log}); err != nil {
					p.logger.Warn("individual insert failed for MCP tool log %s: %v", log.ID, err)
					p.droppedRequests.Add(1)
				}
			}
		}
	}

	// Collect callbacks that need to fire, then run them in a single goroutine.
	// This avoids blocking the batch writer (synchronous was causing 1+ second stalls
	// during WebSocket broadcast) without creating a goroutine per entry (which caused
	// goroutine explosion to 13K+).
	type cbPair struct {
		cb  func(*logstore.Log)
		log *logstore.Log
	}
	type mcpCbPair struct {
		cb  func(*logstore.MCPToolLog)
		log *logstore.MCPToolLog
	}
	var callbacks []cbPair
	var mcpCallbacks []mcpCbPair
	for _, entry := range batch {
		if entry.callback != nil {
			callbacks = append(callbacks, cbPair{cb: entry.callback, log: entry.log})
		}
		if entry.mcpCallback != nil {
			mcpCallbacks = append(mcpCallbacks, mcpCbPair{cb: entry.mcpCallback, log: entry.mcpLog})
		}
	}
	if len(callbacks) > 0 || len(mcpCallbacks) > 0 {
		go func(callbacks []cbPair, mcpCallbacks []mcpCbPair) {
			defer func() {
				if r := recover(); r != nil {
					p.logger.Warn("log callback panicked: %v", r)
				}
			}()
			for _, pair := range callbacks {
				pair.cb(pair.log)
			}
			for _, pair := range mcpCallbacks {
				pair.cb(pair.log)
			}
		}(callbacks, mcpCallbacks)
	}
}

// cleanupStalePendingLogs removes stale in-memory pending log state.
// Pending LLM entries are dropped to prevent unbounded memory growth. Pending
// MCP entries are converted into terminal error rows and queued for persistence,
// because PreMCPHook does not write a processing row to the database.
func (p *LoggerPlugin) cleanupStalePendingLogs() {
	cutoff := time.Now().Add(-pendingLogTTL)
	p.pendingLogsEntries.Range(func(key, value any) bool {
		if pending, ok := value.(*PendingLogData); ok {
			if pending.CreatedAt.Before(cutoff) {
				p.pendingLogsEntries.Delete(key)
			}
		}
		return true
	})
	p.pendingLogsToInject.Range(func(key, value any) bool {
		if pending, ok := value.(*pendingInjectEntries); ok {
			if pending.createdAt.Before(cutoff) {
				p.pendingLogsToInject.Delete(key)
			}
		}
		return true
	})
	p.pendingMCPLogsToInject.Range(func(key, value any) bool {
		if pending, ok := value.(*logstore.MCPToolLog); ok {
			if pending.CreatedAt.Before(cutoff) {
				actual, loaded := p.pendingMCPLogsToInject.LoadAndDelete(key)
				if !loaded {
					return true
				}
				stalePending, ok := actual.(*logstore.MCPToolLog)
				if !ok || stalePending == nil {
					return true
				}

				p.mu.Lock()
				callback := p.mcpToolLogCallback
				p.mu.Unlock()
				p.enqueueMCPToolLogEntry(buildStaleMCPToolLogEntry(stalePending), callback)
			}
		}
		return true
	})
}

// enqueueLogEntry pushes a complete log entry to the write queue.
// If the queue is full, the entry is dropped to prevent Postgres slowness
// from cascading into request handling goroutines.
func (p *LoggerPlugin) enqueueLogEntry(entry *logstore.Log, callback func(entry *logstore.Log)) {
	if p.closed.Load() {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			// Channel was closed between the check and send; entry is dropped
			p.droppedRequests.Add(1)
		}
	}()
	select {
	case p.writeQueue <- &writeQueueEntry{log: entry, callback: callback}:
		// enqueued successfully
	default:
		p.droppedRequests.Add(1)
		p.logger.Warn("log write queue full, dropping log entry %s", entry.ID)
	}
}

// enqueueMCPToolLogEntry pushes a complete MCP tool log entry to the write queue.
// If the queue is full, the entry is dropped to prevent store slowness from
// cascading into request handling goroutines.
func (p *LoggerPlugin) enqueueMCPToolLogEntry(entry *logstore.MCPToolLog, callback func(entry *logstore.MCPToolLog)) {
	if p.closed.Load() {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			p.droppedRequests.Add(1)
		}
	}()
	select {
	case p.writeQueue <- &writeQueueEntry{mcpLog: entry, mcpCallback: callback}:
	default:
		p.droppedRequests.Add(1)
		p.logger.Warn("log write queue full, dropping MCP tool log entry %s", entry.ID)
	}
}

// estimateWriteQueueEntrySize returns the estimated serialized payload size for
// the log entry carried by a write queue item.
func estimateWriteQueueEntrySize(entry *writeQueueEntry) int {
	if entry == nil {
		return 0
	}
	if entry.mcpLog != nil {
		return estimateMCPToolLogEntrySize(entry.mcpLog)
	}
	return estimateLogEntrySize(entry.log)
}

// estimateLogEntrySize returns a rough byte-size estimate for a log entry
// based on its serialized text fields. This is intentionally cheap — no
// marshaling, just string lengths — and is used to cap batch memory.
//
// NOTE: At enqueue time the string fields may still be empty (data lives in the
// Parsed struct fields until GORM's BeforeCreate hook serializes them), so this
// can undercount significantly. That is acceptable — the byte limit is a
// coarse safety valve, not a precise memory cap. Overshooting by 2× is fine;
// maxBatchSize is the primary batching control.
func estimateLogEntrySize(log *logstore.Log) int {
	if log == nil {
		return 0
	}
	// Sum the dominant text/blob fields. Fixed-width columns (IDs, timestamps,
	// ints, bools) are negligible compared to these and covered by the 512-byte
	// baseline below.
	n := len(log.InputHistory) +
		len(log.ResponsesInputHistory) +
		len(log.OutputMessage) +
		len(log.ResponsesOutput) +
		len(log.EmbeddingOutput) +
		len(log.RerankOutput) +
		len(log.OCROutput) +
		len(log.Params) +
		len(log.Tools) +
		len(log.ToolCalls) +
		len(log.SpeechInput) +
		len(log.SpeechOutput) +
		len(log.TranscriptionInput) +
		len(log.TranscriptionOutput) +
		len(log.ImageGenerationInput) +
		len(log.ImageGenerationOutput) +
		len(log.VideoGenerationInput) +
		len(log.VideoGenerationOutput) +
		len(log.VideoRetrieveOutput) +
		len(log.VideoDownloadOutput) +
		len(log.VideoListOutput) +
		len(log.VideoDeleteOutput) +
		len(log.ListModelsOutput) +
		len(log.TokenUsage) +
		len(log.ErrorDetails) +
		len(log.RawRequest) +
		len(log.RawResponse) +
		len(log.PassthroughRequestBody) +
		len(log.PassthroughResponseBody) +
		len(log.ContentSummary) +
		len(log.CacheDebug) +
		len(log.RoutingEngineLogs)
	// Baseline for fixed-width columns and struct overhead
	return n + 512
}

// estimateMCPToolLogEntrySize returns a rough byte-size estimate for an MCP
// tool log entry based on its serialized text fields.
func estimateMCPToolLogEntrySize(log *logstore.MCPToolLog) int {
	if log == nil {
		return 0
	}
	return len(log.Arguments) + len(log.Result) + len(log.ErrorDetails) + len(log.Metadata) + 512
}

// buildStaleMCPToolLogEntry converts a pending MCP processing row into a
// terminal error entry suitable for the batch writer.
func buildStaleMCPToolLogEntry(pending *logstore.MCPToolLog) *logstore.MCPToolLog {
	entry := *pending
	entry.Status = "error"
	entry.Result = ""
	entry.ResultParsed = nil
	entry.ErrorDetails = ""
	entry.ErrorDetailsParsed = &schemas.BifrostError{
		IsBifrostError: true,
		Error: &schemas.ErrorField{
			Message: "MCP tool execution did not complete before pending log TTL",
		},
	}
	return &entry
}

// buildInitialLogEntry constructs a logstore.Log from PendingLogData (input)
// without writing to the database. Used for the UI callback in PreLLMHook.
func buildInitialLogEntry(pending *PendingLogData) *logstore.Log {
	entry := &logstore.Log{
		ID:                          pending.RequestID,
		Timestamp:                   pending.Timestamp,
		Object:                      pending.InitialData.Object,
		Provider:                    pending.InitialData.Provider,
		Model:                       pending.InitialData.Model,
		FallbackIndex:               pending.FallbackIndex,
		Status:                      "processing",
		Stream:                      false,
		CreatedAt:                   pending.Timestamp,
		InputHistoryParsed:          pending.InitialData.InputHistory,
		ResponsesInputHistoryParsed: pending.InitialData.ResponsesInputHistory,
		ParamsParsed:                pending.InitialData.Params,
		ToolsParsed:                 pending.InitialData.Tools,
		PassthroughRequestBody:      pending.InitialData.PassthroughRequestBody,
	}
	if pending.ParentRequestID != "" {
		entry.ParentRequestID = &pending.ParentRequestID
	}
	if len(pending.RoutingEnginesUsed) > 0 {
		entry.RoutingEnginesUsed = pending.RoutingEnginesUsed
	}
	return entry
}

// buildCompleteLogEntryFromPending constructs a logstore.Log with both input (from PendingLogData)
// and output fields fully populated. The caller provides a function to apply output-specific fields.
func buildCompleteLogEntryFromPending(pending *PendingLogData) *logstore.Log {
	entry := &logstore.Log{
		ID:            pending.RequestID,
		Timestamp:     pending.Timestamp,
		Object:        pending.InitialData.Object,
		Provider:      pending.InitialData.Provider,
		Model:         pending.InitialData.Model,
		FallbackIndex: pending.FallbackIndex,
		Status:        "success",
		CreatedAt:     pending.Timestamp,
		// Set parsed fields for serialization via GORM hooks
		InputHistoryParsed:          pending.InitialData.InputHistory,
		ResponsesInputHistoryParsed: pending.InitialData.ResponsesInputHistory,
		ParamsParsed:                pending.InitialData.Params,
		ToolsParsed:                 pending.InitialData.Tools,
		SpeechInputParsed:           pending.InitialData.SpeechInput,
		TranscriptionInputParsed:    pending.InitialData.TranscriptionInput,
		OCRInputParsed:              pending.InitialData.OCRInput,
		ImageGenerationInputParsed:  pending.InitialData.ImageGenerationInput,
		ImageEditInputParsed:        pending.InitialData.ImageEditInput,
		ImageVariationInputParsed:   pending.InitialData.ImageVariationInput,
		VideoGenerationInputParsed:  pending.InitialData.VideoGenerationInput,
		PassthroughRequestBody:      pending.InitialData.PassthroughRequestBody,
	}
	if pending.ParentRequestID != "" {
		entry.ParentRequestID = &pending.ParentRequestID
	}
	if len(pending.RoutingEnginesUsed) > 0 {
		entry.RoutingEnginesUsed = pending.RoutingEnginesUsed
	}
	return entry
}

// applyModelAlias sets entry.Model to resolvedModel (falling back to requestedModel if empty)
// and entry.Alias to requestedModel when the two differ (i.e. an alias mapping was applied).
func applyModelAlias(entry *logstore.Log, requestedModel, resolvedModel string) {
	if resolvedModel != "" && resolvedModel != requestedModel {
		entry.Model = resolvedModel
		entry.Alias = &requestedModel
	} else {
		// No alias mapping; keep whichever value is non-empty as the model.
		if resolvedModel != "" {
			entry.Model = resolvedModel
		} else if requestedModel != "" {
			entry.Model = requestedModel
		}
		entry.Alias = nil
	}
}

// applyOutputFieldsToEntry sets common output fields on a log entry.
func applyOutputFieldsToEntry(
	entry *logstore.Log,
	selectedKeyID, selectedKeyName string,
	virtualKeyID, virtualKeyName string,
	routingRuleID, routingRuleName string,
	selectedPromptID, selectedPromptName, selectedPromptVersion string,
	teamID, teamName string,
	customerID, customerName string,
	userID, userName string,
	businessUnitID, businessUnitName string,
	numberOfRetries int,
	latency int64,
	attemptTrail []schemas.KeyAttemptRecord,
) {
	entry.SelectedKeyID = selectedKeyID
	entry.SelectedKeyName = selectedKeyName
	if virtualKeyID != "" {
		entry.VirtualKeyID = &virtualKeyID
	}
	if virtualKeyName != "" {
		entry.VirtualKeyName = &virtualKeyName
	}
	if routingRuleID != "" {
		entry.RoutingRuleID = &routingRuleID
	}
	if routingRuleName != "" {
		entry.RoutingRuleName = &routingRuleName
	}
	if selectedPromptID != "" {
		entry.SelectedPromptID = &selectedPromptID
	}
	if selectedPromptName != "" {
		entry.SelectedPromptName = &selectedPromptName
	}
	if selectedPromptVersion != "" {
		entry.SelectedPromptVersion = &selectedPromptVersion
	}
	if teamID != "" {
		entry.TeamID = &teamID
	}
	if teamName != "" {
		entry.TeamName = &teamName
	}
	if customerID != "" {
		entry.CustomerID = &customerID
	}
	if customerName != "" {
		entry.CustomerName = &customerName
	}
	if userID != "" {
		entry.UserID = &userID
	}
	if userName != "" {
		entry.UserName = &userName
	}
	if businessUnitID != "" {
		entry.BusinessUnitID = &businessUnitID
	}
	if businessUnitName != "" {
		entry.BusinessUnitName = &businessUnitName
	}
	if numberOfRetries != 0 {
		entry.NumberOfRetries = numberOfRetries
	}
	if latency != 0 {
		latF := float64(latency)
		entry.Latency = &latF
	}
	if len(attemptTrail) > 0 {
		entry.AttemptTrailParsed = attemptTrail
	}
}
