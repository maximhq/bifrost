package logging

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
)

const (
	// pendingLogTTL is the maximum idle gap (time since the last chunk/activity)
	// a pending log entry can sit in memory before cleanup reclaims it. It is an
	// idle timeout, not a total-lifetime cap: actively-streaming requests refresh
	// their LastActivity on every chunk (see PostLLMHook), so a long-running stream
	// is never evicted mid-flight. Matches the 30-minute window used by
	// cleanupOldProcessingLogs so the two reapers agree on what "stale" means.
	pendingLogTTL = 15 * time.Minute
	// endpointStreamPendingTTL bounds how long a passthrough stream's pending row is kept; each entry
	// holds at most a 64 KiB request body.
	endpointStreamPendingTTL = 24 * time.Hour
	// cleanupDrainTimeout caps how long Cleanup spends draining the write queue
	// itself. Matches the outer server shutdown budget at server.go:1596 so the
	// logging plugin can fully drain in the worst case; remaining entries beyond
	// the deadline are dropped so the process is never wedged on a slow store.
	cleanupDrainTimeout = 30 * time.Second
	// batchWriteAttempts is how many times processBatch sends a batch whose
	// failure was transient (connection lost, statement timeout, server
	// shutting down) before counting it as dropped. The per-row fallback is
	// skipped for these: it cannot succeed either and only multiplies the failed
	// statements by the batch size, which is what let the write queue back up.
	batchWriteAttempts = 3
	// batchWriteBackoff is the pause before the second attempt; it doubles on
	// each further attempt, so three attempts wait 250ms + 500ms at most.
	batchWriteBackoff = 250 * time.Millisecond
)

var (
	// maintenancePollInterval is how often a paused batchWriter re-checks the
	// logstore migration lock (see awaitMigrationWindow). A var so tests can
	// shrink it.
	maintenancePollInterval = time.Second
	// maintenanceMaxPause caps one pause. A lock that outlives it (a very long
	// backfill step, or a crashed migrator whose session a proxy keeps alive)
	// must not stall log writes forever, so after the cap the writer flushes
	// anyway, which is exactly what it did before the gate existed.
	maintenanceMaxPause = 2 * time.Minute
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
	// LastActivity is the unix-nano timestamp of the most recent PostLLMHook
	// activity (e.g. each streaming chunk). cleanupStalePendingLogs evicts on
	// idle time using this value, so long-running streams that keep producing
	// chunks are not reaped before they finish. Atomic because the cleanup
	// goroutine reads it concurrently with per-chunk PostLLMHook writes.
	LastActivity atomic.Int64
	// Live is set for a GPT Live session's row: what its units have folded into it so far.
	Live             *liveSessionState
	ProvisionalInput bool           // Input captured before the virtual key's or project's content decision was known
	ContentPolicy    *contentPolicy // Content policy resolved in PreLLMHook; nil means the global policy
	EvictionAttempts int            // Sweeps that could not queue this entry's partial row
	EvictedPartial   bool           // PostLLMHook stub for an already-evicted request; reconciles by UPDATE
}

// pendingInjectEntries wraps a slice of log entries so it can be used with sync.Map.
// The mutex protects concurrent appends to the entries slice within the same traceID.
type pendingInjectEntries struct {
	mu        sync.Mutex
	entries   []*logstore.Log
	windows   []rowWindow // parallel to entries: each row's attempt window, if measured
	createdAt time.Time
	// drained is set by Inject under mu once entries has been handed to the write
	// queue. A storeOrEnqueueEntry that appends after that point would be writing
	// into a slice nobody reads again; it writes directly instead.
	drained bool
}

// rowWindow is a parked row's attempt window; ok is false when none was measured.
type rowWindow struct {
	window schemas.AttemptWindow
	ok     bool
}

// pendingAgentInjectEntries is the Agent analogue of pendingInjectEntries: request
// rows parked by PostA2AHook until Inject backfills authoritative latency
// numbers from the completed trace.
type pendingAgentInjectEntries struct {
	mu        sync.Mutex
	entries   []*logstore.AgentLog
	createdAt time.Time
	// drained is set by injectAgentEntries under mu once entries has been handed
	// to the write queue; a late park writes directly instead.
	drained bool
}

// pendingRelayInjectEntries is the Relay analogue of pendingAgentInjectEntries: the row parked by
// PostRelayHook until Inject backfills upstream, overhead and the breakdown from the completed trace.
type pendingRelayInjectEntries struct {
	mu        sync.Mutex
	entries   []*logstore.RelayLog
	createdAt time.Time
	drained   bool
}

// writeQueueEntry is an entry pushed to the batch write queue.
// Exactly one insert, partial update, transcript correction, MCP log or agent log is set.
type writeQueueEntry struct {
	realtimeInput *realtimeInputUpdate
	log           *logstore.Log
	logUpdate     *logstore.Log
	mcpLog        *logstore.MCPToolLog
	agentLog      *logstore.AgentLog
	endpointLog   *logstore.RelayLog
	callback      func(entry *logstore.Log)
	mcpCallback   func(entry *logstore.MCPToolLog)
}

// batchWriter is the single writer goroutine that drains the write queue
// and processes entries in batched transactions.
func (p *LoggerPlugin) batchWriter() {
	defer p.wg.Done()

	writerConfig := p.writerConfig
	batchInterval, err := time.ParseDuration(writerConfig.BatchInterval)
	if err != nil {
		batchInterval = 5 * time.Second
	}

	batch := make([]*writeQueueEntry, 0, writerConfig.MaxBatchSize)
	batchBytes := 0
	timer := time.NewTimer(batchInterval)
	timer.Stop()
	timerRunning := false

	// carry holds entries a flush could not write because batchCtx ended
	// mid-write. They are handed to Cleanup with the unflushed batch so the
	// drain, which has its own deadline, writes them instead of losing them.
	var carry []*writeQueueEntry

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
		// Hold the batch while another node runs schema migrations. The queue
		// keeps buffering behind us (and drops when full, as always); nothing is
		// written until the lock clears, the pause cap passes, or Cleanup cancels.
		p.awaitMigrationWindow(p.batchCtx)
		if p.batchCtx.Err() != nil {
			// Cancelled during the pause: leave the batch intact so the caller's
			// handoff hands it to Cleanup's drain instead of writing it here.
			return
		}
		carry = append(carry, p.safeProcessBatch(p.batchCtx, batch)...)
		clear(batch)
		batch = batch[:0]
		batchBytes = 0
	}

	// handoff parks everything this goroutine still owns in recoveredBatch
	// and signals Cleanup, which owns the drain budget from this point on.
	handoff := func() {
		p.recoveredBatch = append(carry, batch...)
		close(p.batchWriterDone)
	}

	for {
		select {
		case entry, ok := <-p.writeQueue:
			if !ok {
				// Channel closed - flush remaining batch and exit. Nobody drains
				// after this point, so anything the flush could not write is lost.
				if left := p.safeProcessBatch(p.batchCtx, batch); len(left) > 0 {
					p.droppedRequests.Add(int64(len(left)))
				}
				return
			}
			batch = append(batch, entry)
			batchBytes += estimateWriteQueueEntrySize(entry)
			if len(batch) >= writerConfig.MaxBatchSize || batchBytes >= writerConfig.MaxBatchBytes {
				flush()
				if p.batchCtx.Err() != nil {
					// Cancelled during the flush: do not race the Done case below
					// against a still-busy queue, hand off now.
					handoff()
					return
				}
			} else if !timerRunning {
				timer.Reset(batchInterval)
				timerRunning = true
			}

		case <-timer.C:
			timerRunning = false
			if len(batch) > 0 {
				flush()
				if p.batchCtx.Err() != nil {
					handoff()
					return
				}
			}

		case <-p.batchCtx.Done():
			// Cleanup is taking over: hand the local batch back via
			// recoveredBatch, signal exit, and return without touching the
			// store. Cleanup owns the drain budget from this point on.
			handoff()
			return
		}
	}
}

// migrationInProgress reports whether the store says another node holds the
// logstore migration lock. A store without a MaintenanceGate never pauses.
func (p *LoggerPlugin) migrationInProgress(ctx context.Context) bool {
	return logstore.MigrationInProgressFor(ctx, p.store)
}

// awaitMigrationWindow blocks the batchWriter while a migration is in progress.
// Every statement this process sends to the logs table during that window queues
// behind the migrator's pending ALTER TABLE, and once the ALTER is queued every
// later statement from every node queues behind it: the migration takes longer
// and the pool drains. Holding the batch keeps this node out of that queue so the
// DDL completes in milliseconds. It polls every maintenancePollInterval, gives up
// after maintenanceMaxPause (and then stays given up, via maintenanceCapSpent,
// until the lock is seen released), and returns at once when ctx ends so
// Cleanup's handoff is never delayed. While paused, writerPaused is set so the enqueue
// paths can attribute their drops to the migration window.
func (p *LoggerPlugin) awaitMigrationWindow(ctx context.Context) {
	if !p.migrationInProgress(ctx) {
		if p.maintenanceCapSpent.Swap(false) {
			p.logger.Info("logstore migration lock released; log writes are no longer being forced past the pause cap")
		}
		return
	}
	if p.maintenanceCapSpent.Load() {
		// This migration window already cost its maximum pause. Pausing again for
		// every batch would throttle the writer to one batch per cap while the
		// lock stays held, so keep writing until the lock is seen released.
		return
	}
	p.writerPaused.Store(true)
	defer p.writerPaused.Store(false)
	started := time.Now()
	dropsBefore := p.droppedDuringMaintenance.Load()
	p.logger.Warn("logstore migration lock is held by another node; pausing log writes (queue capacity %d, max pause %s)",
		p.writerConfig.WriteQueueCapacity, maintenanceMaxPause)
	timer := time.NewTimer(maintenancePollInterval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if !p.migrationInProgress(ctx) {
			p.logger.Info("logstore migration lock released; resuming log writes after %s (%d entries dropped during the pause)",
				time.Since(started).Round(time.Millisecond), p.droppedDuringMaintenance.Load()-dropsBefore)
			return
		}
		if time.Since(started) >= maintenanceMaxPause {
			p.maintenanceCapSpent.Store(true)
			p.logger.Warn("logstore migration lock still held after %s; resuming log writes anyway until it clears (%d entries dropped during the pause)",
				maintenanceMaxPause, p.droppedDuringMaintenance.Load()-dropsBefore)
			return
		}
		timer.Reset(maintenancePollInterval)
	}
}

// countDrop records one entry dropped at enqueue time, attributing it to the
// migration window as well when the writer is paused for one.
func (p *LoggerPlugin) countDrop() {
	p.droppedRequests.Add(1)
	if p.writerPaused.Load() {
		p.droppedDuringMaintenance.Add(1)
	}
}

// safeProcessBatch wraps processBatch with panic recovery so a single
// bad entry cannot kill the batchWriter goroutine. ctx bounds every store call,
// retry and split inside processBatch: batchWriter passes p.batchCtx so Cleanup
// stops them promptly, drainPending passes its own deadline context. It returns
// the entries processBatch could not write because ctx ended; a recovered panic
// returns none, since the batch was counted as dropped.
func (p *LoggerPlugin) safeProcessBatch(ctx context.Context, batch []*writeQueueEntry) (unwritten []*writeQueueEntry) {
	defer func() {
		if r := recover(); r != nil {
			p.logger.Error("panic in batch writer processBatch (recovered, %d entries dropped): %v", len(batch), r)
			p.droppedRequests.Add(int64(len(batch)))
			unwritten = nil
		}
	}()
	return p.processBatch(ctx, batch)
}

// retryTransientBatch runs write and, while it fails with a transient error
// (see logstore.IsTransientWriteError), retries the whole batch up to
// batchWriteAttempts times with doubling backoff. It returns the last error;
// the caller decides what a persistent failure means for the rows. A Postgres
// statement timeout is returned at once without retrying, because the same
// statement under the same statement_timeout will time out again and the caller
// recovers by splitting instead. Any non-transient error is also returned at once so
// the caller can isolate the offending row. Backoff aborts when ctx is done.
func (p *LoggerPlugin) retryTransientBatch(ctx context.Context, size int, kind string, write func() error) error {
	backoff := batchWriteBackoff
	var err error
	for attempt := 1; attempt <= batchWriteAttempts; attempt++ {
		err = write()
		if err == nil || !logstore.IsTransientWriteError(err) || logstore.IsPostgresStatementTimeoutError(err) {
			return err
		}
		if attempt == batchWriteAttempts {
			break
		}
		p.logger.Warn("batch insert of %d %s entries failed transiently (attempt %d/%d), retrying whole batch in %s: %v", size, kind, attempt, batchWriteAttempts, backoff, err)
		timer := time.NewTimer(backoff)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			p.logger.Warn("batch insert of %d %s entries abandoned, batch writer context done: %v", size, kind, err)
			return err
		}
		backoff *= 2
	}
	p.logger.Warn("batch insert of %d %s entries failed after %d attempts: %v", size, kind, batchWriteAttempts, err)
	return err
}

// writeWithRecovery writes entries as one batch and recovers from failure by
// error class. ctx is the context every store call runs under, so Cleanup's
// cancel or the drain deadline interrupts a blocked write instead of waiting on
// it. Entries that could not be written because ctx ended are returned, not
// dropped: the caller hands them to whoever owns the next context (the drain
// after batchWriter is cancelled), and re-inserting them is idempotent.
//
//   - Postgres statement timeout (SQLSTATE 57014): the batch is split in half and each
//     half written the same way, down to single rows, so a batch that is too
//     large for statement_timeout still lands; a single row that times out is
//     dropped.
//   - any other transient error (connection lost, server shutting down, lock
//     timeout, SQLite busy/locked): the batch was already retried whole by
//     retryTransientBatch and is dropped; per-row retries cannot succeed
//     against the same outage, and on SQLite each one would wait out
//     busy_timeout on the same writer lock.
//   - anything else: the existing per-row fallback, perRow, isolates the bad
//     row so one poisoned entry does not drop its neighbours. perRow reports
//     false when ctx ended before the row was settled.
func writeWithRecovery[T any](p *LoggerPlugin, ctx context.Context, kind string, entries []T, write func(context.Context, []T) error, perRow func(context.Context, T) bool) (unwritten []T) {
	if len(entries) == 0 {
		return nil
	}
	err := p.retryTransientBatch(ctx, len(entries), kind, func() error { return write(ctx, entries) })
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil:
		// The write was interrupted, not refused: hand the rows back whole.
		p.logger.Warn("batch insert of %d %s entries interrupted (%v), handing them back for the drain", len(entries), kind, ctx.Err())
		return entries
	case logstore.IsPostgresStatementTimeoutError(err):
		if len(entries) == 1 {
			p.logger.Warn("batch insert of %d %s entries timed out and cannot be split further, dropping: %v", len(entries), kind, err)
			p.droppedRequests.Add(int64(len(entries)))
			return nil
		}
		mid := len(entries) / 2
		p.logger.Warn("batch insert of %d %s entries hit statement_timeout, splitting into %d + %d: %v", len(entries), kind, mid, len(entries)-mid, err)
		unwritten = append(unwritten, writeWithRecovery(p, ctx, kind, entries[:mid], write, perRow)...)
		unwritten = append(unwritten, writeWithRecovery(p, ctx, kind, entries[mid:], write, perRow)...)
		return unwritten
	case logstore.IsTransientWriteError(err):
		p.logger.Warn("batch insert of %d %s entries dropped after transient store failure (per-row retry cannot help): %v", len(entries), kind, err)
		p.droppedRequests.Add(int64(len(entries)))
		return nil
	default:
		p.logger.Warn("batch insert failed for %d %s entries, falling back to individual inserts: %v", len(entries), kind, err)
		for i, entry := range entries {
			if ctx.Err() != nil {
				return append(unwritten, entries[i:]...)
			}
			if !perRow(ctx, entry) {
				unwritten = append(unwritten, entry)
			}
		}
		return unwritten
	}
}

// insertLogIndividually is the per-row fallback for a log entry whose batch
// failed with a row-level error. It isolates the bad entry instead of losing
// the whole batch, and as a last resort strips the parsed payload fields (one
// of them failed serialization) and keeps the scalar row: a log without
// content beats a silently dropped request. It reports false when ctx ended
// before the row was written or dropped, so the caller can hand it back.
func (p *LoggerPlugin) insertLogIndividually(ctx context.Context, log *logstore.Log) bool {
	err := p.store.BatchCreateIfNotExists(ctx, []*logstore.Log{log})
	if err == nil {
		return true
	}
	if ctx.Err() != nil {
		return false
	}
	p.logger.Warn("individual insert failed for log %s, retrying without payload fields: %v", log.ID, err)
	stripUnserializablePayloads(log)
	err = p.store.BatchCreateIfNotExists(ctx, []*logstore.Log{log})
	if err == nil {
		return true
	}
	if ctx.Err() != nil {
		return false
	}
	p.logger.Warn("payload-stripped insert failed for log %s: %v", log.ID, err)
	p.droppedRequests.Add(1)
	return true
}

// insertMCPLogIndividually is the per-row fallback for an MCP tool log entry.
// It reports false when ctx ended before the row was written or dropped.
func (p *LoggerPlugin) insertMCPLogIndividually(ctx context.Context, log *logstore.MCPToolLog) bool {
	err := p.store.BatchCreateMCPToolLogsIfNotExists(ctx, []*logstore.MCPToolLog{log})
	if err == nil {
		return true
	}
	if ctx.Err() != nil {
		return false
	}
	p.logger.Warn("individual insert failed for MCP tool log %s: %v", log.ID, err)
	p.droppedRequests.Add(1)
	return true
}

// insertAgentLogIndividually is the per-row fallback for an Agent log entry.
// It reports false when ctx ended before the row was written or dropped.
func (p *LoggerPlugin) insertAgentLogIndividually(ctx context.Context, log *logstore.AgentLog) bool {
	_, err := p.store.BatchCreateAgentLogsIfNotExists(ctx, []*logstore.AgentLog{log})
	if err == nil {
		return true
	}
	if ctx.Err() != nil {
		return false
	}
	p.logger.Warn("individual insert failed for Agent log %s: %v", log.ID, err)
	p.droppedRequests.Add(1)
	return true
}

// insertEndpointLogIndividually is the per-row fallback for a Relay Logs entry. It reports
// false when ctx ended before the row was written or dropped.
func (p *LoggerPlugin) insertEndpointLogIndividually(ctx context.Context, log *logstore.RelayLog) bool {
	_, err := p.store.BatchCreateRelayLogsIfNotExists(ctx, []*logstore.RelayLog{log})
	if err == nil {
		return true
	}
	if ctx.Err() != nil {
		return false
	}
	p.logger.Warn("individual insert failed for relay log %s: %v", log.ID, err)
	p.droppedRequests.Add(1)
	return true
}

// updateLogIndividually is the per-row fallback for a partial log update whose
// batch failed with a row-level error. It reports false when ctx ended before
// the row was written or dropped.
func (p *LoggerPlugin) updateLogIndividually(ctx context.Context, log *logstore.Log) bool {
	err := p.store.BatchUpdate(ctx, []*logstore.Log{log})
	if err == nil {
		return true
	}
	if ctx.Err() != nil {
		return false
	}
	p.logger.Warn("individual update failed for log %s: %v", log.ID, err)
	p.droppedRequests.Add(1)
	return true
}

// processBatch writes a batch of log entries, chunked by the store, and
// recovers from failures per writeWithRecovery. ctx bounds the store calls,
// retries and splitting. It returns the entries that were not written because
// ctx ended; their callbacks are not run, since nothing was persisted for them.
func (p *LoggerPlugin) processBatch(ctx context.Context, batch []*writeQueueEntry) []*writeQueueEntry {
	if len(batch) == 0 {
		return nil
	}

	// Collect all log entries, separating inserts from partial updates
	logs := make([]*logstore.Log, 0, len(batch))
	logUpdates := make([]*logstore.Log, 0, len(batch))
	mcpLogs := make([]*logstore.MCPToolLog, 0, len(batch))
	agentLogs := make([]*logstore.AgentLog, 0, len(batch))
	endpointLogs := make([]*logstore.RelayLog, 0)
	for _, entry := range batch {
		if entry.log != nil {
			p.applyParsedPassthrough(entry.log)
			omitBinaryBodies(entry.log)
			logs = append(logs, entry.log)
		}
		if entry.logUpdate != nil {
			p.applyParsedPassthrough(entry.logUpdate)
			omitBinaryBodies(entry.logUpdate)
			logUpdates = append(logUpdates, entry.logUpdate)
		}
		if entry.mcpLog != nil {
			mcpLogs = append(mcpLogs, entry.mcpLog)
		}
		if entry.agentLog != nil {
			agentLogs = append(agentLogs, entry.agentLog)
		}
		if entry.endpointLog != nil {
			endpointLogs = append(endpointLogs, entry.endpointLog)
		}
	}

	unwrittenLogs := writeWithRecovery(p, ctx, "log", logs, func(ctx context.Context, chunk []*logstore.Log) error {
		return p.store.BatchCreateIfNotExists(ctx, chunk)
	}, p.insertLogIndividually)
	unwrittenMCP := writeWithRecovery(p, ctx, "MCP tool log", mcpLogs, func(ctx context.Context, chunk []*logstore.MCPToolLog) error {
		return p.store.BatchCreateMCPToolLogsIfNotExists(ctx, chunk)
	}, p.insertMCPLogIndividually)
	unwrittenAgent := writeWithRecovery(p, ctx, "Agent log", agentLogs, func(ctx context.Context, chunk []*logstore.AgentLog) error {
		_, err := p.store.BatchCreateAgentLogsIfNotExists(ctx, chunk)
		return err
	}, p.insertAgentLogIndividually)
	unwrittenEndpoint := writeWithRecovery(p, ctx, "relay log", endpointLogs, func(ctx context.Context, chunk []*logstore.RelayLog) error {
		_, err := p.store.BatchCreateRelayLogsIfNotExists(ctx, chunk)
		return err
	}, p.insertEndpointLogIndividually)
	// Updates run after the inserts so a partial-row insert and its reconciling
	// update landing in the same batch resolve correctly: the row exists by the
	// time the update runs. The queue is FIFO and the insert is always enqueued
	// first (at eviction), so it is never in a later batch than its update.
	unwrittenUpdates := writeWithRecovery(p, ctx, "log update", logUpdates, func(ctx context.Context, chunk []*logstore.Log) error {
		return p.store.BatchUpdate(ctx, chunk)
	}, p.updateLogIndividually)
	for _, entry := range batch {
		if entry.realtimeInput != nil {
			if err := p.writeRealtimeInputUpdate(ctx, entry.realtimeInput); err != nil {
				// Trace injection may enqueue the original turn after its transcript.
				// Reuse the bounded queue and normal write retry budget, with no waiter.
				entry.realtimeInput.attempts++
				if errors.Is(err, logstore.ErrNotFound) && entry.realtimeInput.attempts < batchWriteAttempts {
					p.enqueueRealtimeInputUpdate(entry.realtimeInput)
					continue
				}
				p.logger.Warn("failed to update realtime transcript for log %s: %v", entry.realtimeInput.connectionID, err)
				p.countDrop()
			}
		}
	}

	// Map unwritten rows back to their queue entries. An entry carrying more
	// than one log type is handed back whole if any part is unwritten; persisted
	// parts are re-inserted as no-ops by conflict-safe insertion.
	var unwritten []*writeQueueEntry
	unwrittenCount := len(unwrittenLogs) + len(unwrittenUpdates) + len(unwrittenMCP) + len(unwrittenAgent) + len(unwrittenEndpoint)
	skip := make(map[*writeQueueEntry]struct{}, unwrittenCount)
	if unwrittenCount > 0 {
		pending := make(map[any]struct{}, unwrittenCount)
		for _, l := range unwrittenLogs {
			pending[l] = struct{}{}
		}
		for _, u := range unwrittenUpdates {
			pending[u] = struct{}{}
		}
		for _, m := range unwrittenMCP {
			pending[m] = struct{}{}
		}
		for _, a := range unwrittenAgent {
			pending[a] = struct{}{}
		}
		for _, e := range unwrittenEndpoint {
			pending[e] = struct{}{}
		}
		for _, entry := range batch {
			_, logPending := pending[entry.log]
			_, updatePending := pending[entry.logUpdate]
			_, mcpPending := pending[entry.mcpLog]
			_, agentPending := pending[entry.agentLog]
			_, endpointPending := pending[entry.endpointLog]
			if (entry.log != nil && logPending) || (entry.logUpdate != nil && updatePending) || (entry.mcpLog != nil && mcpPending) || (entry.agentLog != nil && agentPending) || (entry.endpointLog != nil && endpointPending) {
				skip[entry] = struct{}{}
				unwritten = append(unwritten, entry)
			}
		}
	}

	if len(agentLogs) > 0 && len(unwrittenAgent) == 0 {
		if err := p.store.ReconcileAgentCorrelation(ctx, agentLogs); err != nil {
			p.logger.Warn("Agent correlation reconciliation failed for %d logs: %v", len(agentLogs), err)
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
		if _, held := skip[entry]; held {
			continue
		}
		if entry.callback != nil {
			// Insert entries carry log; update entries carry logUpdate.
			cbLog := entry.log
			if cbLog == nil {
				cbLog = entry.logUpdate
			}
			callbacks = append(callbacks, cbPair{cb: entry.callback, log: cbLog})
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
	return unwritten
}

// cleanupStalePendingLogs removes stale in-memory pending log state.
// Before a stale LLM entry is dropped, its input is persisted as a partial
// "processing" row so a request that finishes late can still be reconciled
// onto it (see the evicted-partial path in PostLLMHook) instead of losing its
// log entirely. Pending MCP entries are converted into terminal error rows and
// queued for persistence, because PreMCPHook does not write a processing row
// to the database.
func (p *LoggerPlugin) cleanupStalePendingLogs() {
	cutoff := time.Now().Add(-pendingLogTTL)
	p.pendingLogsEntries.Range(func(key, value any) bool {
		if pending, ok := value.(*PendingLogData); ok {
			// Evict on idle time: use the last chunk/activity timestamp, falling
			// back to CreatedAt for entries that never saw a PostLLMHook (e.g. a
			// request abandoned before its first chunk). This keeps actively
			// streaming requests alive past the TTL while still reaping dead ones.
			lastActive := pending.CreatedAt
			if nanos := pending.LastActivity.Load(); nanos > 0 {
				lastActive = time.Unix(0, nanos)
			}
			if lastActive.Before(cutoff) {
				// persistEvictedPartial claims the entry itself; a claim that fails
				// means PostLLMHook took it first and will write the full row.
				p.persistEvictedPartial(key, pending)
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
				stalePending, ok := p.claimStaleMCPEntry(key)
				if !ok {
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
	// A relayed stream can outlive pendingLogTTL; its request side is kept for the longer stream bound
	// so the row written when it ends still carries the path, route and billable flag.
	streamCutoff := time.Now().Add(-endpointStreamPendingTTL)
	p.pendingEndpointLogs.Range(func(key, value any) bool {
		pending, ok := value.(*logstore.RelayLog)
		if !ok {
			return true
		}
		if pending.Timestamp.Before(cutoff) && (!pending.Streamed || pending.Timestamp.Before(streamCutoff)) {
			p.pendingEndpointLogs.Delete(key)
		}
		return true
	})
	p.pendingAgentLogs.Range(func(key, value any) bool {
		if pending, ok := value.(*logstore.AgentLog); ok && pending.Timestamp.Before(cutoff) {
			p.pendingAgentLogs.Delete(key)
		}
		return true
	})
	p.pendingAgentLogsToInject.Range(func(key, value any) bool {
		if pending, ok := value.(*pendingAgentInjectEntries); ok {
			if pending.createdAt.Before(cutoff) {
				p.pendingAgentLogsToInject.Delete(key)
			}
		}
		return true
	})
	p.pendingRelayLogsToInject.Range(func(key, value any) bool {
		if pending, ok := value.(*pendingRelayInjectEntries); ok {
			if pending.createdAt.Before(cutoff) {
				p.pendingRelayLogsToInject.Delete(key)
			}
		}
		return true
	})
	// Drop evicted-partial markers whose request never finished. A later finish
	// still finds the row through the DB probe while flushStaleProcessingLogs
	// has not reaped it yet.
	p.evictedPartials.Range(func(key, value any) bool {
		if ts, ok := value.(time.Time); ok && ts.Before(cutoff) {
			p.evictedPartials.Delete(key)
		}
		return true
	})
}

// maxEvictionAttempts caps how many sweeps may try to queue a stale entry's
// partial row. A failed attempt keeps the entry in memory so its input is not
// lost to a momentarily full write queue; past the cap it is dropped and
// counted, so a queue that stays full cannot pin evicted entries forever.
const maxEvictionAttempts = 3

// pendingClaimShards is the number of locks pendingClaimLock stripes request
// IDs over. Only eviction and terminal PostLLMHook calls take these locks, so
// contention is negligible; the stripe count just bounds memory.
const pendingClaimShards = 64

// pendingClaimLock returns the lock that serializes who takes a request's
// pending entry: the stale sweep (which then queues a partial row and a
// marker) or the terminal PostLLMHook (which then reads the marker). Without
// it the two can interleave so that the partial row lands after the completed
// insert, or the marker is read before the partial is queued.
func (p *LoggerPlugin) pendingClaimLock(requestID string) *sync.Mutex {
	h := fnv.New32a()
	_, _ = h.Write([]byte(requestID))
	return &p.pendingClaimLocks[h.Sum32()%pendingClaimShards]
}

// persistEvictedPartial claims a stale pending entry and, when the claim
// succeeds, queues a partial "processing" row for it and records the eviction
// so a late finish can reconcile onto the row with an UPDATE. The claim is a
// LoadAndDelete under the request's claim lock: if PostLLMHook took the entry
// first nothing is written, since the completion will insert the full row.
// The marker is set only when the partial insert was actually queued. When the
// write queue is full the entry is put back and EvictionAttempts counts the
// failure, so the next sweep retries and a completion can still take the entry
// and write the full row; past maxEvictionAttempts the entry is dropped and
// counted. Content follows the policy resolved for the request in PreLLMHook
// (ContentPolicy; the global policy when none was recorded), because eviction
// has no request context. Input captured provisionally, before the virtual
// key's or project's decision was known, is never kept: PostLLMHook's final-policy drop
// cannot run for it. CreatedAt is reset to now so flushStaleProcessingLogs
// gives the row a fresh window; Timestamp keeps the original request time. No
// callback: PreLLMHook already announced the processing entry to subscribers.
func (p *LoggerPlugin) persistEvictedPartial(key any, pending *PendingLogData) {
	id, ok := key.(string)
	if !ok {
		p.pendingLogsEntries.Delete(key)
		return
	}
	lock := p.pendingClaimLock(id)
	lock.Lock()
	defer lock.Unlock()
	if _, claimed := p.pendingLogsEntries.LoadAndDelete(key); !claimed {
		return
	}
	if pending.Live != nil {
		// A GPT Live session's units are folded in by postLiveUnit, which runs before
		// the reconcile path, so a partial row could never be reconciled; it would
		// only sit in "processing" until the stale-row reaper removes it.
		return
	}
	// Every captured input kind, not just chat history: a late speech, image or
	// video completion has nothing else to recover its input from.
	partial := buildCompleteLogEntryFromPending(pending)
	// Request metadata (logging headers, isAsyncRequest) has no other source: the
	// reconcile writes none, so the partial row must carry it.
	partial.MetadataParsed = pending.InitialData.Metadata
	partial.Status = logStatusProcessing
	partial.CreatedAt = time.Now().UTC()
	policy := p.resolveContentPolicy(nil)
	if pending.ContentPolicy != nil {
		policy = *pending.ContentPolicy
	}
	if pending.ProvisionalInput || !policy.storeContent {
		dropCapturedInput(partial)
	}
	partial.ContentHidden = !policy.visible()
	if !p.tryEnqueueLogEntry(partial, nil) {
		// Nothing persisted: keep the entry so its input survives. The next sweep
		// retries the insert, or a completion takes the entry and writes the full
		// row. Still under the claim lock, so no terminal PostLLMHook can observe
		// the gap between the claim above and this store.
		pending.EvictionAttempts++
		if pending.EvictionAttempts < maxEvictionAttempts {
			p.pendingLogsEntries.Store(key, pending)
			return
		}
		p.countDrop()
		p.logger.Warn("log write queue full, dropping evicted request %s after %d attempts to persist its partial row", id, pending.EvictionAttempts)
		return
	}
	// Queued before the marker is visible, so a finish that sees the marker
	// enqueues its update behind the insert; processBatch also applies inserts
	// before updates within a batch.
	p.evictedPartials.Store(id, time.Now())
}

// claimStaleMCPEntry takes a pending MCP entry away from PostMCPHook for the stale-entry reaper.
// It reports false when the entry is already gone, which means PostMCPHook claimed it first.
func (p *LoggerPlugin) claimStaleMCPEntry(key any) (*logstore.MCPToolLog, bool) {
	actual, loaded := p.pendingMCPLogsToInject.LoadAndDelete(key)
	if !loaded {
		// PostMCPHook claimed it first and still needs the held arguments.
		return nil, false
	}
	// Arguments held back for an unresolved key or project go with the entry: a stale entry never
	// learns its final content decision, so it never gets them.
	p.provisionalMCPArguments.Delete(key)
	entry, ok := actual.(*logstore.MCPToolLog)
	if !ok || entry == nil {
		return nil, false
	}
	return entry, true
}

// enqueueLogEntry pushes a complete log entry to the write queue.
// If the queue is full, the entry is dropped to prevent Postgres slowness
// from cascading into request handling goroutines.
func (p *LoggerPlugin) enqueueLogEntry(entry *logstore.Log, callback func(entry *logstore.Log)) {
	if p.closed.Load() {
		return
	}
	// A row whose output-monitor guardrail result is still pending is written
	// once that result lands (holdForDeferredGuardrail). The result is taken off
	// the row here, so the row passes straight through when it comes back.
	if deferred := entry.DeferredGuardrail; deferred != nil {
		entry.DeferredGuardrail = nil
		p.holdForDeferredGuardrail(deferred, entry, callback)
		return
	}
	if !p.tryEnqueueLogEntry(entry, callback) {
		p.countDrop()
		p.logger.Warn("log write queue full, dropping log entry %s", entry.ID)
	}
}

// holdForDeferredGuardrail parks a completed row until its output-monitor
// guardrail result lands, then writes it once with that result: the same
// detections and redacted content an enforced rule would have recorded, and the
// evaluation's plugin log lines after the request's own. No
// goroutine waits; the guardrails plugin runs the callback when its background
// evaluation finishes, which it always does within its own timeout. Cleanup
// writes rows still held at shutdown as they were built.
func (p *LoggerPlugin) holdForDeferredGuardrail(deferred *schemas.DeferredGuardrailResult, entry *logstore.Log, callback func(entry *logstore.Log)) {
	p.heldGuardrailRows.Store(entry.ID, &writeQueueEntry{log: entry, callback: callback})
	deferred.OnComplete(func(metadata *schemas.BifrostGuardrailMetadata, redaction schemas.RedactionData, pluginLogs []schemas.PluginLogEntry) {
		if _, held := p.heldGuardrailRows.LoadAndDelete(entry.ID); !held {
			return // Cleanup already wrote the row.
		}
		entry.GuardrailDebugParsed = metadata
		entry.PluginLogs = appendPluginLogs(entry.PluginLogs, pluginLogs)
		// Same rule as attachLogRedactionData: hidden content carries no mappings.
		if !entry.ContentHidden && redaction.HasReplacements() {
			entry.RedactionData = &redaction
		}
		p.writeHeldRow(entry, callback)
	})
}

// releaseHeldGuardrailRows writes every row still waiting on an output-monitor
// result as it was built, so shutdown loses only the late result, never the row.
// Cleanup runs it after setting closed, so no row can be held behind it.
func (p *LoggerPlugin) releaseHeldGuardrailRows() {
	p.heldGuardrailRows.Range(func(key, _ any) bool {
		if value, held := p.heldGuardrailRows.LoadAndDelete(key); held {
			row := value.(*writeQueueEntry)
			p.writeHeldRow(row.log, row.callback)
		}
		return true
	})
}

// writeHeldRow queues a row that was held before Cleanup set closed. It skips
// the closed check: the row was accepted before shutdown, and Cleanup drains
// the queue after releasing held rows. A send racing the final channel close
// is absorbed, as for any other producer.
func (p *LoggerPlugin) writeHeldRow(entry *logstore.Log, callback func(entry *logstore.Log)) {
	queued := func() (queued bool) {
		defer func() {
			if recover() != nil {
				queued = false
			}
		}()
		select {
		case p.writeQueue <- &writeQueueEntry{log: entry, callback: callback}:
			return true
		default:
			return false
		}
	}()
	if !queued {
		p.countDrop()
		p.logger.Warn("log write queue full, dropping log entry %s", entry.ID)
	}
}

// tryEnqueueLogEntry pushes a log entry to the write queue without blocking and
// reports whether it was queued. It neither counts nor logs a failure, so a
// caller that keeps the entry for a retry does not inflate droppedRequests.
func (p *LoggerPlugin) tryEnqueueLogEntry(entry *logstore.Log, callback func(entry *logstore.Log)) (queued bool) {
	if p.closed.Load() {
		return false
	}
	defer func() {
		if r := recover(); r != nil {
			// Channel was closed between the check and send.
			queued = false
		}
	}()
	select {
	case p.writeQueue <- &writeQueueEntry{log: entry, callback: callback}:
		return true
	default:
		return false
	}
}

// EnqueueLogEntry pushes a complete log entry through the logging plugin's
// normal async write queue.
func (p *LoggerPlugin) EnqueueLogEntry(entry *logstore.Log) {
	p.enqueueLogEntry(entry, p.makePostWriteCallback(nil))
}

// enqueueLogUpdate pushes a partial log update (ID plus the non-zero output
// fields) to the write queue. The batch writer applies it through
// store.BatchUpdate after the batch's inserts, so it reconciles an existing row
// (the partial persisted at eviction) instead of inserting. A full queue drops
// the update, matching enqueueLogEntry's backpressure.
func (p *LoggerPlugin) enqueueLogUpdate(entry *logstore.Log, callback func(entry *logstore.Log)) {
	if p.closed.Load() {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			p.logger.Error("recovered from a panic %v. dropping log update", r)
			// Channel was closed between the check and send; entry is dropped
			p.droppedRequests.Add(1)
		}
	}()
	select {
	case p.writeQueue <- &writeQueueEntry{logUpdate: entry, callback: callback}:
		// enqueued successfully
	default:
		p.countDrop()
		p.logger.Warn("log write queue full, dropping log update %s", entry.ID)
	}
}

// EnqueueMCPToolLogEntry pushes a completed MCP log through the normal async write queue.
func (p *LoggerPlugin) EnqueueMCPToolLogEntry(entry *logstore.MCPToolLog) {
	p.mu.Lock()
	callback := p.mcpToolLogCallback
	p.mu.Unlock()
	p.enqueueMCPToolLogEntry(entry, callback)
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
		p.countDrop()
		p.logger.Warn("log write queue full, dropping MCP tool log entry %s", entry.ID)
	}
}

func (p *LoggerPlugin) enqueueAgentLogEntry(entry *logstore.AgentLog) {
	if entry == nil || p.closed.Load() {
		return
	}
	defer func() {
		if recover() != nil {
			p.droppedRequests.Add(1)
		}
	}()
	select {
	case p.writeQueue <- &writeQueueEntry{agentLog: entry}:
	default:
		p.countDrop()
		p.logger.Warn("log write queue full, dropping A2A log entry %s", entry.ID)
	}
}

func (p *LoggerPlugin) enqueueEndpointLogEntry(entry *logstore.RelayLog) {
	if entry == nil || p.closed.Load() {
		return
	}
	defer func() {
		if recover() != nil {
			p.droppedRequests.Add(1)
		}
	}()
	select {
	case p.writeQueue <- &writeQueueEntry{endpointLog: entry}:
	default:
		p.countDrop()
		p.logger.Warn("log write queue full, dropping relay log entry %s", entry.ID)
	}
}

// estimateWriteQueueEntrySize returns the estimated serialized payload size for
// the log entry carried by a write queue item.
func estimateWriteQueueEntrySize(entry *writeQueueEntry) int {
	if entry == nil {
		return 0
	}
	if entry.realtimeInput != nil {
		return len(entry.realtimeInput.raw) + len(entry.realtimeInput.connectionID) + len(entry.realtimeInput.itemID) + len(entry.realtimeInput.transcript)
	}
	if entry.agentLog != nil {
		size := len(entry.agentLog.PluginLogs) + len(entry.agentLog.ErrorDetails) + 512
		if entry.agentLog.RawRequest != nil {
			size += len(*entry.agentLog.RawRequest)
		}
		if entry.agentLog.RawResponse != nil {
			size += len(*entry.agentLog.RawResponse)
		}
		if entry.agentLog.RequestBody != nil {
			size += len(*entry.agentLog.RequestBody)
		}
		if entry.agentLog.ResponseBody != nil {
			size += len(*entry.agentLog.ResponseBody)
		}
		if entry.agentLog.EventBody != nil {
			size += len(*entry.agentLog.EventBody)
		}
		return size
	}
	if entry.endpointLog != nil {
		size := len(entry.endpointLog.PluginLogs) + len(entry.endpointLog.ErrorDetails) + 512
		for k, v := range entry.endpointLog.MetadataParsed {
			size += len(k) + len(fmt.Sprint(v)) + 6
		}
		for _, body := range []*string{entry.endpointLog.RequestBody, entry.endpointLog.ResponseBody} {
			if body != nil {
				size += len(*body)
			}
		}
		// Headers are still maps here (the store serializes them on write), so they are sized as
		// such; the serialized columns are nil until then.
		for _, headers := range []map[string]string{entry.endpointLog.RequestHeadersParsed, entry.endpointLog.ResponseHeadersParsed} {
			for k, v := range headers {
				size += len(k) + len(v) + 6
			}
		}
		return size
	}
	if entry.mcpLog != nil {
		return estimateMCPToolLogEntrySize(entry.mcpLog)
	}
	if entry.log != nil {
		return estimateLogEntrySize(entry.log)
	}
	return estimateLogEntrySize(entry.logUpdate)
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
		len(log.EmbeddingInput) +
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
		len(log.VideoEditInput) +
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
		len(log.GuardrailDebug) +
		len(log.RoutingMetadata) +
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
	return len(log.Arguments) + len(log.Result) + len(log.ErrorDetails) + len(log.Metadata) + len(log.PluginLogs) + 512
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
		EmbeddingInputParsed:        pending.InitialData.EmbeddingInput,
		ParamsParsed:                pending.InitialData.Params,
		ToolsParsed:                 pending.InitialData.Tools,
		MetadataParsed:              pending.InitialData.Metadata,
		PassthroughRequestBody:      pending.InitialData.PassthroughRequestBody,
	}
	if pending.ParentRequestID != "" {
		entry.ParentRequestID = &pending.ParentRequestID
	}
	if len(pending.RoutingEnginesUsed) > 0 {
		entry.RoutingEnginesUsed = pending.RoutingEnginesUsed
	}
	applyUserAgent(entry, pending.InitialData.UserAgent)
	applyApp(entry, pending.InitialData.App)
	applyAgentCorrelationID(entry, pending.InitialData.AgentCorrelationID)
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
		EmbeddingInputParsed:        pending.InitialData.EmbeddingInput,
		ParamsParsed:                pending.InitialData.Params,
		ToolsParsed:                 pending.InitialData.Tools,
		SpeechInputParsed:           pending.InitialData.SpeechInput,
		TranscriptionInputParsed:    pending.InitialData.TranscriptionInput,
		OCRInputParsed:              pending.InitialData.OCRInput,
		ImageGenerationInputParsed:  pending.InitialData.ImageGenerationInput,
		ImageEditInputParsed:        pending.InitialData.ImageEditInput,
		ImageVariationInputParsed:   pending.InitialData.ImageVariationInput,
		VideoGenerationInputParsed:  pending.InitialData.VideoGenerationInput,
		VideoEditInputParsed:        pending.InitialData.VideoEditInput,
		PassthroughRequestBody:      pending.InitialData.PassthroughRequestBody,
	}
	if pending.ParentRequestID != "" {
		entry.ParentRequestID = &pending.ParentRequestID
	}
	if len(pending.RoutingEnginesUsed) > 0 {
		entry.RoutingEnginesUsed = pending.RoutingEnginesUsed
	}
	applyUserAgent(entry, pending.InitialData.UserAgent)
	applyApp(entry, pending.InitialData.App)
	applyAgentCorrelationID(entry, pending.InitialData.AgentCorrelationID)
	return entry
}

func applyAgentCorrelationID(entry *logstore.Log, agentCorrelationID string) {
	if agentCorrelationID != "" {
		agentCorrelationID = clampString(agentCorrelationID, maxPersistedAgentCorrelationIDLen)
		entry.AgentCorrelationID = &agentCorrelationID
	}
}

// User-Agent and App map to fixed-width DB columns (varchar(512) / varchar(128)).
// User-Agent is an untrusted, unbounded client header, so clamp both before
// persisting to avoid an insert that fails (and silently drops the log) when a
// client sends an oversized header.
const (
	maxPersistedUserAgentLen          = 512
	maxPersistedAppLen                = 128
	maxPersistedAgentCorrelationIDLen = 255
	maxPersistedMCPToolNameLen        = 255 // mcp_tool_logs.tool_name; a resource read's URI is unbounded
)

// clampString truncates s to at most max bytes. The columns are sized in
// characters but ASCII User-Agent headers make bytes a safe lower bound. A raw
// byte-slice truncation can split a multi-byte UTF-8 rune, so ToValidUTF8
// strips the dangling partial rune to keep the result valid UTF-8 for the
// varchar insert.
func clampString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return strings.ToValidUTF8(s[:max], "")
}

func applyUserAgent(entry *logstore.Log, userAgent string) {
	if userAgent == "" {
		return
	}
	entry.UserAgent = new(clampString(userAgent, maxPersistedUserAgentLen))
	if entry.App == nil {
		if app := schemas.DetectAppFromUserAgent(userAgent); app != "" {
			entry.App = new(clampString(app, maxPersistedAppLen))
		}
	}
}

func applyApp(entry *logstore.Log, app string) {
	if app == "" {
		return
	}
	entry.App = new(clampString(app, maxPersistedAppLen))
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

// applyResolvedAliasInfo copies the canonical model name and model family from the
// resolved key alias onto the entry when the alias config defines them. Both fields
// stay nil when no alias matched or the alias doesn't configure them.
func applyResolvedAliasInfo(entry *logstore.Log, resolvedAlias *schemas.ResolvedKeyAlias) {
	if resolvedAlias == nil {
		return
	}
	if resolvedAlias.ModelName != nil && *resolvedAlias.ModelName != "" {
		name := *resolvedAlias.ModelName
		entry.CanonicalModelName = &name
	}
	if resolvedAlias.ModelFamily != nil && *resolvedAlias.ModelFamily != "" {
		family := string(*resolvedAlias.ModelFamily)
		entry.AliasModelFamily = &family
	}
}

// applyServedModel records the model the provider named on the response body when
// it differs from the one the caller addressed.
func applyServedModel(entry *logstore.Log, result *schemas.BifrostResponse) {
	if entry == nil {
		return
	}
	served := result.ServedModel()
	if served == "" || served == entry.Model {
		return
	}
	entry.ServedModel = &served
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
	projectID, projectName string,
	numberOfRetries int,
	latency int64,
	upstreamLatency, overheadLatency *int64,
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
	if projectID != "" {
		entry.ProjectID = &projectID
	}
	if projectName != "" {
		entry.ProjectName = &projectName
	}
	if numberOfRetries != 0 {
		entry.NumberOfRetries = numberOfRetries
	}
	if latency != 0 {
		latF := float64(latency)
		entry.Latency = &latF
	}
	setUpstreamOverheadLatency(entry, upstreamLatency, overheadLatency)
	if len(attemptTrail) > 0 {
		entry.AttemptTrailParsed = attemptTrail
	}
}

// setUpstreamOverheadLatency copies upstream/overhead onto the entry. nil stays nil,
// so an absent measurement is never persisted as zero.
func setUpstreamOverheadLatency(entry *logstore.Log, upstreamLatency, overheadLatency *int64) {
	if upstreamLatency != nil {
		upF := float64(*upstreamLatency)
		entry.UpstreamLatency = &upF
	}
	if overheadLatency != nil {
		ovF := float64(*overheadLatency)
		entry.OverheadLatency = &ovF
	}
}

// applyUpstreamOverheadToEntry copies upstream/overhead from a response's ExtraFields
// onto the entry. Used by the streaming path.
func applyUpstreamOverheadToEntry(entry *logstore.Log, ef *schemas.BifrostResponseExtraFields) {
	if ef == nil {
		return
	}
	setUpstreamOverheadLatency(entry, ef.UpstreamLatency, ef.OverheadLatency)
}

// omitBinaryBodies replaces the body columns of log that hold binary data with
// a size placeholder. Bodies are copied into the entry as raw bytes (audio
// passthrough responses, multipart uploads, Bedrock event streams), and their
// NUL bytes and invalid UTF-8 are rejected by Postgres TEXT, failing the whole
// batch INSERT; even where a store accepts them they render as garbage.
func omitBinaryBodies(log *logstore.Log) {
	log.RawRequest = omitBinaryBody(log.RawRequest)
	log.RawResponse = omitBinaryBody(log.RawResponse)
	log.PassthroughRequestBody = omitBinaryBody(log.PassthroughRequestBody)
	log.PassthroughResponseBody = omitBinaryBody(log.PassthroughResponseBody)
}

// omitBinaryBody returns s when it is text and a "<binary content omitted: N
// bytes>" placeholder otherwise. A partial UTF-8 sequence at the very end is a
// preview cut by byte count, not binary data, so it is dropped and the text
// kept.
func omitBinaryBody(s string) string {
	if s == "" {
		return s
	}
	text := s
	for i := len(text) - 1; i >= 0 && i >= len(text)-utf8.UTFMax; i-- {
		if utf8.RuneStart(text[i]) {
			if !utf8.FullRuneInString(text[i:]) {
				text = text[:i]
			}
			break
		}
	}
	if strings.IndexByte(text, 0) < 0 && utf8.ValidString(text) {
		return text
	}
	return fmt.Sprintf("<binary content omitted: %d bytes>", len(s))
}
