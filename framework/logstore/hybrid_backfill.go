package logstore

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
	"gorm.io/gorm"
)

const (
	// DefaultBackfillBatchSize is the number of rows scanned per DB batch
	// during a backfill. Rows are paged with a timestamp cursor so scan cost
	// stays flat regardless of table size.
	DefaultBackfillBatchSize = 100
	// DefaultBackfillConcurrency is the number of parallel upload workers used
	// by the backfill. Matches the online upload worker count.
	DefaultBackfillConcurrency = 10
	// backfillUploadTimeout bounds a single object-store PUT.
	backfillUploadTimeout = 60 * time.Second
	// backfillUpdateTimeout bounds a single DB row rewrite after a successful PUT.
	backfillUpdateTimeout = 30 * time.Second
	// backfillUploadRetries is the number of attempts per object PUT.
	backfillUploadRetries = 3
)

// BackfillOptions controls a one-time migration of existing DB-resident log
// payloads into the object store. The result matches what the online hybrid
// write path produces: a lightweight DB row plus one object per log.
type BackfillOptions struct {
	// BatchSize is the number of rows scanned per DB page. Zero uses
	// DefaultBackfillBatchSize.
	BatchSize int
	// Concurrency is the number of parallel upload workers. Zero uses
	// DefaultBackfillConcurrency.
	Concurrency int
	// OlderThan only migrates rows older than this duration. Zero migrates
	// everything. Use this to protect recent hot rows while they may still be
	// updated by the online write path.
	OlderThan time.Duration
	// IncludeMCP also migrates mcp_tool_logs rows. Defaults to true when nil;
	// set it to false explicitly to skip MCP tool logs.
	IncludeMCP *bool
	// DryRun scans and reports without uploading or modifying rows.
	DryRun bool
	// Progress is invoked after each processed row with a serialized running
	// result snapshot. May be nil.
	Progress func(result BackfillResult)
}

// BackfillError records a single row that could not be fully migrated.
type BackfillError struct {
	LogID string
	MCP   bool
	Err   error
}

func (e BackfillError) Error() string {
	kind := "log"
	if e.MCP {
		kind = "mcp_tool_log"
	}
	return fmt.Sprintf("%s %s: %v", kind, e.LogID, e.Err)
}

// BackfillResult reports the outcome of a backfill run.
type BackfillResult struct {
	Migrated       int64
	Failed         int64
	Skipped        int64 // rows scanned that had nothing to offload
	BytesOffloaded int64
	Duration       time.Duration
	Errors         []BackfillError
}

// errBackfillNothingToOffload marks rows whose payload fields are all empty.
// Treated as "skipped" rather than a failure.
var errBackfillNothingToOffload = errors.New("nothing to offload")

// backfillRow is the tiny projection used to page candidates. Full rows are
// only materialized in the worker, one at a time, to bound memory.
type backfillRow struct {
	ID        string    `gorm:"column:id"`
	Timestamp time.Time `gorm:"column:timestamp"`
}

type backfillCursor struct {
	Timestamp time.Time
	ID        string
	Set       bool
}

// backfillConditionalStore atomically applies the lightweight-row rewrite only
// while the row is still DB-resident. ClickHouse supplies an explicit override
// because its updates are read-modify-write reinserts rather than SQL UPDATEs.
type backfillConditionalStore interface {
	updateLogForBackfill(ctx context.Context, id string, updates map[string]interface{}) (bool, error)
	updateMCPForBackfill(ctx context.Context, id string, updates map[string]interface{}) (bool, error)
}

var (
	_ backfillConditionalStore = (*RDBLogStore)(nil)
	_ backfillConditionalStore = (*ClickHouseLogStore)(nil)
	_ scopedDBLogStore         = (*ClickHouseLogStore)(nil)
)

// BackfillObjects migrates existing DB-resident log payloads into the
// configured object store, converting rows to the lightweight hybrid form.
//
// Ordering guarantee per row: the object is uploaded FIRST, and the DB row is
// rewritten (payload fields cleared, has_object=true) only after the PUT
// succeeds. A failure at any point leaves the DB row untouched, so a re-run is
// idempotent. The scan only selects rows with has_object=false, and the row
// rewrite is guarded by "has_object = false" so concurrent online writes are
// never clobbered (worst case: one orphan object).
//
// The context governs the whole run: cancellation stops scanning and stops
// feeding new rows, while already-queued rows finish so no row is left
// half-migrated.
func (h *HybridLogStore) BackfillObjects(ctx context.Context, opts BackfillOptions) (*BackfillResult, error) {
	if opts.BatchSize <= 0 {
		opts.BatchSize = DefaultBackfillBatchSize
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = DefaultBackfillConcurrency
	}
	// IncludeMCP is a pointer so nil can mean the safe default: migrate both
	// regular and MCP logs. Callers can explicitly pass false to skip MCP.

	start := time.Now()
	result := &BackfillResult{}

	db := h.backfillDB(ctx)
	if db == nil {
		return nil, fmt.Errorf("logstore: backfill requires an RDB-backed log store")
	}

	type work struct {
		row backfillRow
		mcp bool
	}
	workCh := make(chan work)
	var workerWG sync.WaitGroup
	var migrated, failed, skipped, bytesOffloaded atomic.Int64
	var errMu sync.Mutex
	var backfillErrors []BackfillError
	appendErr := func(e BackfillError) {
		errMu.Lock()
		defer errMu.Unlock()
		backfillErrors = append(backfillErrors, e)
	}
	var progressMu sync.Mutex
	notifyProgress := func() {
		if opts.Progress == nil {
			return
		}
		// Serialize callbacks and build the snapshot while holding the same
		// lock, so callbacks are monotonic and never race on shared result fields.
		progressMu.Lock()
		defer progressMu.Unlock()
		snapshot := BackfillResult{
			Migrated:       migrated.Load(),
			Failed:         failed.Load(),
			Skipped:        skipped.Load(),
			BytesOffloaded: bytesOffloaded.Load(),
			Duration:       time.Since(start),
		}
		errMu.Lock()
		snapshot.Errors = append([]BackfillError(nil), backfillErrors...)
		errMu.Unlock()
		opts.Progress(snapshot)
	}

	// Cancellation stops scanning and feeding new rows. Work already accepted by
	// a worker finishes with the caller's values but without its cancellation.
	workCtx := context.WithoutCancel(ctx)
	for i := 0; i < opts.Concurrency; i++ {
		workerWG.Add(1)
		go func() {
			defer workerWG.Done()
			for w := range workCh {
				var bytes int64
				var err error
				if w.mcp {
					bytes, err = h.backfillMCPRow(workCtx, w.row, opts.DryRun)
				} else {
					bytes, err = h.backfillLogRow(workCtx, w.row, opts.DryRun)
				}
				switch {
				case err == nil:
					migrated.Add(1)
					bytesOffloaded.Add(bytes)
				case errors.Is(err, errBackfillNothingToOffload):
					skipped.Add(1)
				default:
					failed.Add(1)
					appendErr(BackfillError{LogID: w.row.ID, MCP: w.mcp, Err: err})
				}
				notifyProgress()
			}
		}()
	}

	// Cutoff for OlderThan protection, applied per table below.
	var cutoff time.Time
	if opts.OlderThan > 0 {
		cutoff = time.Now().UTC().Add(-opts.OlderThan)
	}

	// Scan and feed workers. Errors from the scan abort the run after workers
	// drain, leaving completed rows migrated (the run is resumable).
	var scanErr error
	for _, c := range []struct {
		mcp     bool
		table   string
		include bool
	}{
		{mcp: false, table: "logs", include: true},
		{mcp: true, table: "mcp_tool_logs", include: opts.IncludeMCP == nil || *opts.IncludeMCP},
	} {
		if !c.include {
			continue
		}
		var cursor backfillCursor
		for scanErr == nil {
			batch, err := scanBackfillCandidates(ctx, db, c.table, cursor, cutoff, opts.BatchSize)
			if err != nil {
				scanErr = fmt.Errorf("backfill scan failed (%s): %w", c.table, err)
				break
			}
			if len(batch) == 0 {
				break
			}
			for _, row := range batch {
				select {
				case workCh <- work{row: row, mcp: c.mcp}:
				case <-ctx.Done():
					scanErr = ctx.Err()
				}
				if scanErr != nil {
					break
				}
			}
			if scanErr != nil {
				break
			}
			last := batch[len(batch)-1]
			cursor = backfillCursor{Timestamp: last.Timestamp, ID: last.ID, Set: true}
			if len(batch) < opts.BatchSize {
				break
			}
		}
		if scanErr != nil {
			break
		}
	}

	close(workCh)
	workerWG.Wait()

	result.Migrated = migrated.Load()
	result.Failed = failed.Load()
	result.Skipped = skipped.Load()
	result.BytesOffloaded = bytesOffloaded.Load()
	result.Duration = time.Since(start)
	errMu.Lock()
	result.Errors = append([]BackfillError(nil), backfillErrors...)
	errMu.Unlock()

	if scanErr != nil {
		return result, scanErr
	}

	return result, nil
}

// backfillDB returns the inner store's raw GORM query surface for internal
// scans. Visibility filtering is deliberately bypassed: the backfill is a
// maintenance job and must see every row. ClickHouse supports this through its
// embedded RDBLogStore while retaining its own update implementation.
func (h *HybridLogStore) backfillDB(ctx context.Context) *gorm.DB {
	scoped, ok := h.inner.(scopedDBLogStore)
	if !ok {
		return nil
	}
	// ScopedDB with a plain context applies no row-visibility scope.
	// Use a scope-free context here; scanBackfillCandidates binds the caller's
	// cancellation context afterwards without applying dashboard row scopes.
	return scoped.ScopedDB(context.Background())
}

// scanBackfillCandidates pages candidate rows (has_object=false) in ascending
// timestamp order. `after` is the previous page's last timestamp; `cutoff`
// bounds how recent a row may be (zero = no bound).
func scanBackfillCandidates(ctx context.Context, db *gorm.DB, table string, after backfillCursor, cutoff time.Time, pageSize int) ([]backfillRow, error) {
	query := db.WithContext(ctx).Table(table).
		Select("id, timestamp").
		Where("has_object = ?", false).
		Order("timestamp ASC, id ASC").
		Limit(pageSize)
	if after.Set {
		query = query.Where("timestamp > ? OR (timestamp = ? AND id > ?)", after.Timestamp, after.Timestamp, after.ID)
	}
	if !cutoff.IsZero() {
		query = query.Where("timestamp <= ?", cutoff)
	}
	var rows []backfillRow
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// backfillLogRow migrates one LLM log row. Returns the payload byte size on
// success, errBackfillNothingToOffload when there is nothing to upload, or an
// error describing the failure (DB row untouched in that case).
func (h *HybridLogStore) backfillLogRow(ctx context.Context, row backfillRow, dryRun bool) (int64, error) {
	entry, err := h.inner.FindByID(ctx, row.ID)
	if err != nil {
		return 0, fmt.Errorf("failed to load row: %w", err)
	}
	if entry.HasObject {
		// Migrated between the scan and this read (e.g. by a re-run racing
		// itself); nothing to do.
		return 0, errBackfillNothingToOffload
	}
	if err := entry.DeserializeFields(); err != nil {
		return 0, fmt.Errorf("failed to deserialize row: %w", err)
	}

	payload := h.extractUploadPayload(entry)
	if isPayloadEmpty(payload) {
		return 0, errBackfillNothingToOffload
	}
	data, err := sonic.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal payload: %w", err)
	}
	if dryRun {
		return int64(len(data)), nil
	}

	key := ObjectKey(h.prefix, entry.Timestamp, entry.ID)
	if err := h.backfillPut(ctx, key, data, BuildTags(entry)); err != nil {
		return 0, fmt.Errorf("object upload failed: %w", err)
	}

	// Build the lightweight DB row exactly the way the online write path does.
	dbEntry := *entry
	prepareDBEntry(&dbEntry, h.excludedPayloadFields)
	// has_object is set explicitly (prepareDBEntry does not touch it) so
	// subsequent reads hydrate from the object store.
	dbEntry.HasObject = true
	updated, err := h.rewriteLogRow(ctx, entry.ID, &dbEntry)
	if err != nil {
		return 0, fmt.Errorf("failed to rewrite DB row (object %s is orphaned and safe to delete): %w", key, err)
	}
	if !updated {
		return 0, errBackfillNothingToOffload
	}
	return int64(len(data)), nil
}

// backfillMCPRow migrates one MCP tool-log row.
func (h *HybridLogStore) backfillMCPRow(ctx context.Context, row backfillRow, dryRun bool) (int64, error) {
	entry, err := h.inner.FindMCPToolLog(ctx, row.ID)
	if err != nil {
		return 0, fmt.Errorf("failed to load row: %w", err)
	}
	if entry.HasObject {
		return 0, errBackfillNothingToOffload
	}
	if isMCPPayloadEmpty(entry) {
		return 0, errBackfillNothingToOffload
	}

	payload, err := MarshalMCPToolLogPayload(entry)
	if err != nil {
		return 0, fmt.Errorf("failed to marshal payload: %w", err)
	}
	if dryRun {
		return int64(len(payload)), nil
	}

	key := MCPToolObjectKey(h.prefix, entry.Timestamp, entry.ID)
	if err := h.backfillPut(ctx, key, payload, BuildMCPToolTags(entry)); err != nil {
		return 0, fmt.Errorf("object upload failed: %w", err)
	}

	// Build the lightweight DB row exactly the way the online write path does.
	dbEntry := *entry
	PrepareMCPToolDBEntry(&dbEntry)
	// UpdateMCPToolLog routes through prepareMCPToolLogDBUpdates, which drops
	// "result"/"error_details" from struct-driven updates (they are treated as
	// payload that must never be written back). Rewrite the row with an explicit
	// column map instead so the cleared values actually land.
	updates := map[string]interface{}{
		"arguments":     dbEntry.Arguments,
		"result":        "",
		"error_details": "",
		"has_object":    true,
	}
	updater, ok := h.inner.(backfillConditionalStore)
	if !ok {
		return 0, fmt.Errorf("logstore: inner store does not support conditional backfill updates")
	}
	updateCtx, cancel := context.WithTimeout(ctx, backfillUpdateTimeout)
	defer cancel()
	updated, err := updater.updateMCPForBackfill(updateCtx, entry.ID, updates)
	if err != nil {
		return 0, fmt.Errorf("failed to rewrite DB row (object %s is orphaned and safe to delete): %w", key, err)
	}
	if !updated {
		return 0, errBackfillNothingToOffload
	}
	return int64(len(payload)), nil
}

// backfillPut uploads one object with retries and exponential backoff.
func (h *HybridLogStore) backfillPut(ctx context.Context, key string, data []byte, tags map[string]string) error {
	var lastErr error
	for attempt := 0; attempt < backfillUploadRetries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(1<<uint(attempt-1)) * time.Second
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		putCtx, cancel := context.WithTimeout(ctx, backfillUploadTimeout)
		err := h.objects.Put(putCtx, key, data, tags)
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
	}
	return lastErr
}

// rewriteLogRow replaces the payload-bearing columns of a log row with the
// lightweight hybrid form. The write is guarded by has_object=false so a row
// updated by the online path between the PUT and this UPDATE is never
// clobbered. Writes only the columns the hybrid form owns: payload columns
// (via the serialized struct) plus has_object; scalar/index columns are left
// untouched to avoid racing concurrent cost/status updates.
func (h *HybridLogStore) rewriteLogRow(ctx context.Context, id string, dbEntry *Log) (bool, error) {
	updateCtx, cancel := context.WithTimeout(ctx, backfillUpdateTimeout)
	defer cancel()

	updater, ok := h.inner.(backfillConditionalStore)
	if !ok {
		return false, fmt.Errorf("logstore: inner store does not support conditional backfill updates")
	}
	return updater.updateLogForBackfill(updateCtx, id, map[string]interface{}{
		"input_history":             dbEntry.InputHistory,
		"responses_input_history":   dbEntry.ResponsesInputHistory,
		"output_message":            dbEntry.OutputMessage,
		"responses_output":          dbEntry.ResponsesOutput,
		"embedding_output":          dbEntry.EmbeddingOutput,
		"rerank_output":             dbEntry.RerankOutput,
		"ocr_input":                 dbEntry.OCRInput,
		"ocr_output":                dbEntry.OCROutput,
		"params":                    dbEntry.Params,
		"tools":                     dbEntry.Tools,
		"tool_calls":                dbEntry.ToolCalls,
		"speech_input":              dbEntry.SpeechInput,
		"transcription_input":       dbEntry.TranscriptionInput,
		"image_generation_input":    dbEntry.ImageGenerationInput,
		"image_edit_input":          dbEntry.ImageEditInput,
		"image_variation_input":     dbEntry.ImageVariationInput,
		"video_generation_input":    dbEntry.VideoGenerationInput,
		"video_edit_input":          dbEntry.VideoEditInput,
		"speech_output":             dbEntry.SpeechOutput,
		"transcription_output":      dbEntry.TranscriptionOutput,
		"image_generation_output":   dbEntry.ImageGenerationOutput,
		"list_models_output":        dbEntry.ListModelsOutput,
		"video_generation_output":   dbEntry.VideoGenerationOutput,
		"video_retrieve_output":     dbEntry.VideoRetrieveOutput,
		"video_download_output":     dbEntry.VideoDownloadOutput,
		"video_list_output":         dbEntry.VideoListOutput,
		"video_delete_output":       dbEntry.VideoDeleteOutput,
		"cache_debug":               dbEntry.CacheDebug,
		"guardrail_debug":           dbEntry.GuardrailDebug,
		"routing_metadata":          dbEntry.RoutingMetadata,
		"token_usage":               dbEntry.TokenUsage,
		"error_details":             dbEntry.ErrorDetails,
		"raw_request":               dbEntry.RawRequest,
		"raw_response":              dbEntry.RawResponse,
		"passthrough_request_body":  dbEntry.PassthroughRequestBody,
		"passthrough_response_body": dbEntry.PassthroughResponseBody,
		"routing_engine_logs":       dbEntry.RoutingEngineLogs,
		"content_summary":           dbEntry.ContentSummary,
		"has_object":                true,
	})
}

// isMCPPayloadEmpty reports whether an MCP tool-log row carries no offloadable
// content. Note the serialized object JSON has no "arguments"/"result" strings
// (those tags are json:"-"), so the emptiness check must run against the row
// struct itself, not against the marshaled payload.
func isMCPPayloadEmpty(l *MCPToolLog) bool {
	return l.Arguments == "" && l.Result == "" && l.ErrorDetails == ""
}

// updateLogForBackfill atomically rewrites an RDB log row only while it still
// has no object. RowsAffected == 0 means another writer migrated or removed the
// row after it was scanned.
func (s *RDBLogStore) updateLogForBackfill(ctx context.Context, id string, updates map[string]interface{}) (bool, error) {
	tx := s.db.WithContext(ctx).
		Model(&Log{}).
		Where("id = ? AND has_object = ?", id, false).
		Updates(updates)
	if tx.Error != nil {
		return false, tx.Error
	}
	return tx.RowsAffected > 0, nil
}

// updateMCPForBackfill is the MCP counterpart of updateLogForBackfill.
func (s *RDBLogStore) updateMCPForBackfill(ctx context.Context, id string, updates map[string]interface{}) (bool, error) {
	tx := s.db.WithContext(ctx).
		Model(&MCPToolLog{}).
		Where("id = ? AND has_object = ?", id, false).
		Updates(updates)
	if tx.Error != nil {
		return false, tx.Error
	}
	return tx.RowsAffected > 0, nil
}

// ClickHouse overrides the promoted RDB methods because it cannot use SQL
// UPDATE. Serialize the has_object check and reinsert with the same RMW lock as
// ordinary ClickHouse updates.
func (s *ClickHouseLogStore) updateLogForBackfill(ctx context.Context, id string, updates map[string]interface{}) (bool, error) {
	st, err := chParseSchema(s.db, &Log{})
	if err != nil {
		return false, err
	}
	defer s.lockRMW("logs", id)()
	var existing Log
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&existing).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	if existing.HasObject {
		return false, nil
	}
	if err := chApplyUpdateMap(ctx, st, reflect.ValueOf(&existing).Elem(), updates); err != nil {
		return false, err
	}
	if err := s.chReinsert(ctx, &existing); err != nil {
		return false, err
	}
	return true, nil
}

func (s *ClickHouseLogStore) updateMCPForBackfill(ctx context.Context, id string, updates map[string]interface{}) (bool, error) {
	st, err := chParseSchema(s.db, &MCPToolLog{})
	if err != nil {
		return false, err
	}
	defer s.lockRMW("mcp_tool_logs", id)()
	var existing MCPToolLog
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&existing).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	if existing.HasObject {
		return false, nil
	}
	if err := chApplyUpdateMap(ctx, st, reflect.ValueOf(&existing).Elem(), updates); err != nil {
		return false, err
	}
	if err := s.chReinsert(ctx, &existing); err != nil {
		return false, err
	}
	return true, nil
}
