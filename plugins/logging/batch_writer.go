package logging

import (
	"time"

	"github.com/maximhq/bifrost/framework/logstore"
)

const (
	// maxBatchSize is the maximum number of entries to collect before flushing
	maxBatchSize = 100
	// batchInterval is the maximum time to wait before flushing a partial batch
	batchInterval = 50 * time.Millisecond
	// writeQueueCapacity is the buffer size for the write queue channel
	writeQueueCapacity = 10000
	// pendingLogTTL is how long a pending log entry can stay in memory before cleanup
	pendingLogTTL = 5 * time.Minute
)

// PendingLogData holds PreLLMHook input data until PostLLMHook fires.
// Stored in pendingLogs sync.Map keyed by requestID.
type PendingLogData struct {
	RequestID         string
	ParentRequestID   string
	Timestamp         time.Time
	FallbackIndex     int
	RoutingEngineUsed string
	InitialData       *InitialLogData
	CreatedAt         time.Time // For cleanup of stale entries
}

// writeQueueEntry is an entry pushed to the batch write queue.
type writeQueueEntry struct {
	log      *logstore.Log            // Complete log entry ready for INSERT
	callback func(entry *logstore.Log) // Post-commit callback receives the inserted entry (no DB re-read needed)
}

// batchWriter is the single writer goroutine that drains the write queue
// and processes entries in batched transactions.
func (p *LoggerPlugin) batchWriter() {
	defer p.wg.Done()

	batch := make([]*writeQueueEntry, 0, maxBatchSize)
	timer := time.NewTimer(batchInterval)
	timer.Stop()
	timerRunning := false

	for {
		select {
		case entry, ok := <-p.writeQueue:
			if !ok {
				// Channel closed - flush remaining batch and exit
				if len(batch) > 0 {
					p.processBatch(batch)
				}
				return
			}
			batch = append(batch, entry)
			if len(batch) >= maxBatchSize {
				if timerRunning {
					timer.Stop()
					timerRunning = false
				}
				p.processBatch(batch)
				batch = batch[:0]
			} else if !timerRunning {
				timer.Reset(batchInterval)
				timerRunning = true
			}

		case <-timer.C:
			timerRunning = false
			if len(batch) > 0 {
				p.processBatch(batch)
				batch = batch[:0]
			}
		}
	}
}

// processBatch executes a batch of log entries in a single database transaction.
func (p *LoggerPlugin) processBatch(batch []*writeQueueEntry) {
	if len(batch) == 0 {
		return
	}

	// Collect all log entries for batch insert
	logs := make([]*logstore.Log, 0, len(batch))
	for _, entry := range batch {
		if entry.log != nil {
			logs = append(logs, entry.log)
		}
	}

	if len(logs) > 0 {
		if err := p.store.BatchCreateIfNotExists(p.ctx, logs); err != nil {
			p.logger.Warn("batch insert failed for %d log entries: %v", len(logs), err)
			return
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
	var callbacks []cbPair
	for _, entry := range batch {
		if entry.callback != nil {
			callbacks = append(callbacks, cbPair{cb: entry.callback, log: entry.log})
		}
	}
	if len(callbacks) > 0 {
		go func() {
			for _, pair := range callbacks {
				pair.cb(pair.log)
			}
		}()
	}
}

// cleanupStalePendingLogs removes entries from pendingLogs that have been
// waiting longer than pendingLogTTL. This handles cases where PostLLMHook
// never fires for a request (e.g., request was cancelled before reaching the provider).
func (p *LoggerPlugin) cleanupStalePendingLogs() {
	cutoff := time.Now().Add(-pendingLogTTL)
	p.pendingLogs.Range(func(key, value any) bool {
		if pending, ok := value.(*PendingLogData); ok {
			if pending.CreatedAt.Before(cutoff) {
				p.pendingLogs.Delete(key)
			}
		}
		return true
	})
}

// enqueueLogEntry pushes a complete log entry to the write queue.
// If the queue is full, it blocks until space is available (backpressure).
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
		// Fall through to blocking send
		p.writeQueue <- &writeQueueEntry{log: entry, callback: callback}
	}
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
	}
	if pending.ParentRequestID != "" {
		entry.ParentRequestID = &pending.ParentRequestID
	}
	if pending.RoutingEngineUsed != "" {
		entry.RoutingEngineUsed = &pending.RoutingEngineUsed
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
		Status:        "completed",
		CreatedAt:     pending.Timestamp,
		// Set parsed fields for serialization via GORM hooks
		InputHistoryParsed:          pending.InitialData.InputHistory,
		ResponsesInputHistoryParsed: pending.InitialData.ResponsesInputHistory,
		ParamsParsed:                pending.InitialData.Params,
		ToolsParsed:                 pending.InitialData.Tools,
		SpeechInputParsed:           pending.InitialData.SpeechInput,
		TranscriptionInputParsed:    pending.InitialData.TranscriptionInput,
		ImageGenerationInputParsed:  pending.InitialData.ImageGenerationInput,
	}
	if pending.ParentRequestID != "" {
		entry.ParentRequestID = &pending.ParentRequestID
	}
	if pending.RoutingEngineUsed != "" {
		entry.RoutingEngineUsed = &pending.RoutingEngineUsed
	}
	return entry
}

// applyOutputFieldsToEntry sets common output fields on a log entry.
func applyOutputFieldsToEntry(
	entry *logstore.Log,
	selectedKeyID, selectedKeyName string,
	virtualKeyID, virtualKeyName string,
	routingRuleID, routingRuleName string,
	numberOfRetries int,
	latency int64,
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
	if numberOfRetries != 0 {
		entry.NumberOfRetries = numberOfRetries
	}
	if latency != 0 {
		latF := float64(latency)
		entry.Latency = &latF
	}
}
