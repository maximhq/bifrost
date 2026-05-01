package logstore

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/objectstore"
)

const (
	defaultUploadWorkers       = 10
	defaultUploadQueueSize     = 5000
	maxContentSummaryBytes     = 2048
	defaultMaxUploadQueueBytes = 1 << 30 // 1 GiB
)

// uploadWork represents an async S3 upload job.
type uploadWork struct {
	logID     string
	timestamp time.Time
	payload   []byte // JSON-encoded payload
	tags      map[string]string
}

// HybridLogStore wraps an existing LogStore and offloads large payload
// fields to object storage while keeping a lightweight index in the DB.
//
// Method routing:
//   - Delegated directly (40+ methods): all analytics, search, histogram, ranking,
//     distinct, MCP, async job methods
//   - Intercepted: Create, CreateIfNotExists, BatchCreateIfNotExists, FindByID,
//     Update, DeleteLog, DeleteLogs, DeleteLogsBatch, Close
type HybridLogStore struct {
	inner          LogStore
	objects        objectstore.ObjectStore
	prefix         string
	logger         schemas.Logger
	uploadQueue    chan *uploadWork
	wg             sync.WaitGroup
	closed         atomic.Bool
	droppedUploads atomic.Int64
	pendingBytes   atomic.Int64
	// excludedPayloadFields is the set of payload field names (DB column names) that must NOT be offloaded to object storage and must remain in the DB.
	excludedPayloadFields map[string]struct{}
}

// newHybridLogStore creates a HybridLogStore wrapping the given inner store.
// excludeFields lists payload field DB column names that should be kept in the
// database rather than offloaded to object storage. Pass nil for the default
// behaviour of offloading all payload fields.
func newHybridLogStore(inner LogStore, objects objectstore.ObjectStore, prefix string, logger schemas.Logger, excludeFields []string) *HybridLogStore {
	excluded := make(map[string]struct{}, len(excludeFields))
	for _, f := range excludeFields {
		excluded[f] = struct{}{}
	}
	h := &HybridLogStore{
		inner:                 inner,
		objects:               objects,
		prefix:                prefix,
		logger:                logger,
		uploadQueue:           make(chan *uploadWork, defaultUploadQueueSize),
		excludedPayloadFields: excluded,
	}
	// Start upload workers.
	for i := 0; i < defaultUploadWorkers; i++ {
		h.wg.Add(1)
		go h.uploadWorker()
	}
	return h
}

// uploadWorker processes async S3 upload jobs from the queue.
func (h *HybridLogStore) uploadWorker() {
	defer h.wg.Done()
	for work := range h.uploadQueue {
		h.processUpload(work)
	}
}

// processUpload uploads a single payload to object storage.
// This is fire-and-forget by design: on Put failure the upload is dropped and
// counted in droppedUploads. The DB row retains has_object=false, so FindByID
// falls back to whatever data the DB holds. Retries are intentionally omitted
// to keep S3 latency from cascading into the write path.
func (h *HybridLogStore) processUpload(work *uploadWork) {
	payloadSize := int64(len(work.payload))
	defer h.pendingBytes.Add(-payloadSize)

	defer func() {
		if r := recover(); r != nil {
			h.logger.Error("objectstore: panic in upload worker (recovered): %v", r)
			h.droppedUploads.Add(1)
		}
	}()

	key := ObjectKey(h.prefix, work.timestamp, work.logID)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := h.objects.Put(ctx, key, work.payload, work.tags); err != nil {
		h.logger.Warn("objectstore: failed to upload log %s: %v", work.logID, err)
		h.droppedUploads.Add(1)
		return
	}

	// Mark the DB row as having an object. Use a fresh context so that a slow
	// Put doesn't starve the DB update of its deadline. Retry up to 3 times
	// with exponential backoff to avoid orphaning the uploaded object.
	for attempt := 0; attempt < 3; attempt++ {
		dbCtx, dbCancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := h.inner.Update(dbCtx, work.logID, map[string]interface{}{"has_object": true})
		dbCancel()
		if err == nil {
			return
		}
		h.logger.Warn("objectstore: failed to set has_object for log %s (attempt %d/3): %v", work.logID, attempt+1, err)
		if attempt < 2 {
			time.Sleep(time.Duration(1<<attempt) * time.Second) // 1s, 2s backoff
		}
	}
	h.logger.Error("objectstore: failed to set has_object for log %s after 3 attempts; payload orphaned in object store", work.logID)
	h.droppedUploads.Add(1)
}

// isPayloadEmpty returns true when every value in the payload map is empty.
// Skipping uploads for empty payloads avoids wasted S3 PUTs (e.g. initial
// "processing" entries that carry no input/output data yet).
func isPayloadEmpty(payload map[string]string) bool {
	for _, v := range payload {
		if v != "" {
			return false
		}
	}
	return true
}

// enqueueUpload pushes an upload job onto the queue. If the queue is full,
// the job is dropped to prevent S3 slowness from cascading.
func (h *HybridLogStore) enqueueUpload(logID string, timestamp time.Time, payload map[string]string, tags map[string]string) {
	if h.closed.Load() || isPayloadEmpty(payload) {
		return
	}
	// Recover from send-on-closed-channel panic: Close() may interleave
	// between the closed check above and the channel send below.
	// Same pattern as plugins/logging/writer.go enqueueLogEntry.
	defer func() {
		if r := recover(); r != nil {
			h.droppedUploads.Add(1)
		}
	}()
	data, err := sonic.Marshal(payload)
	if err != nil {
		h.logger.Warn("objectstore: failed to marshal payload for log %s: %v", logID, err)
		h.droppedUploads.Add(1)
		return
	}
	if h.pendingBytes.Load()+int64(len(data)) > defaultMaxUploadQueueBytes {
		h.droppedUploads.Add(1)
		h.logger.Warn("objectstore: upload queue memory limit reached, dropping upload for log %s", logID)
		return
	}
	select {
	case h.uploadQueue <- &uploadWork{
		logID:     logID,
		timestamp: timestamp,
		payload:   data,
		tags:      tags,
	}:
		h.pendingBytes.Add(int64(len(data)))
	default:
		h.droppedUploads.Add(1)
		h.logger.Warn("objectstore: upload queue full, dropping upload for log %s", logID)
	}
}

// --- Intercepted methods ---

// prepareDBEntry builds the lightweight DB entry by extracting the content
// summary, trimming input history to the last user message, and clearing
// payload fields that will be offloaded to object storage. Fields in the
// excluded set are kept intact in the DB row.
// Must be called after SerializeFields() populates the Parsed fields.
func prepareDBEntry(dbEntry *Log, excluded map[string]struct{}) {
	idx := findLastUserMessageIndex(dbEntry.InputHistoryParsed)

	// Content summary: extract text from the found user message.
	// Falls back to BuildInputContentSummary for non-chat inputs (speech, image, etc.).
	if idx >= 0 {
		dbEntry.ContentSummary = extractChatMessageText(&dbEntry.InputHistoryParsed[idx])
	} else {
		dbEntry.ContentSummary = dbEntry.BuildInputContentSummary()
	}
	// Bound content summary to prevent large prompts from bloating the DB row.
	dbEntry.ContentSummary = truncateTag(dbEntry.ContentSummary, maxContentSummaryBytes)

	// Serialize last user message before ClearPayload zeros everything.
	// msgs[idx:idx+1] reuses the backing array — no heap alloc, no struct copy.
	var lastUserMessage string
	if idx >= 0 {
		lastUserMessage, _ = sonic.MarshalString(dbEntry.InputHistoryParsed[idx : idx+1])
	}

	ClearPayloadFiltered(dbEntry, excluded)

	if _, hasInputHistoryExclusion := excluded["input_history"]; !hasInputHistoryExclusion {
		dbEntry.InputHistory = lastUserMessage
	}
}

func (h *HybridLogStore) Create(ctx context.Context, entry *Log) error {
	if err := entry.SerializeFields(); err != nil {
		return fmt.Errorf("logstore: serialize before extract: %w", err)
	}
	payload := ExtractPayloadFiltered(entry, h.excludedPayloadFields)
	tags := BuildTags(entry)
	// Work on a shallow copy so the caller's entry is preserved on DB failure.
	dbEntry := *entry
	prepareDBEntry(&dbEntry, h.excludedPayloadFields)
	if err := h.inner.Create(ctx, &dbEntry); err != nil {
		return err
	}
	entry.ContentSummary = dbEntry.ContentSummary
	h.enqueueUpload(entry.ID, entry.Timestamp, payload, tags)
	return nil
}

func (h *HybridLogStore) CreateIfNotExists(ctx context.Context, entry *Log) error {
	if err := entry.SerializeFields(); err != nil {
		return fmt.Errorf("logstore: serialize before extract: %w", err)
	}
	payload := ExtractPayloadFiltered(entry, h.excludedPayloadFields)
	tags := BuildTags(entry)
	// Work on a shallow copy so the caller's entry is preserved on DB failure.
	dbEntry := *entry
	prepareDBEntry(&dbEntry, h.excludedPayloadFields)
	if err := h.inner.CreateIfNotExists(ctx, &dbEntry); err != nil {
		return err
	}
	entry.ContentSummary = dbEntry.ContentSummary
	h.enqueueUpload(entry.ID, entry.Timestamp, payload, tags)
	return nil
}

func (h *HybridLogStore) BatchCreateIfNotExists(ctx context.Context, entries []*Log) error {
	type pendingUpload struct {
		logID     string
		timestamp time.Time
		payload   map[string]string
		tags      map[string]string
	}
	var uploads []pendingUpload

	dbEntries := make([]*Log, len(entries))
	for i, entry := range entries {
		if err := entry.SerializeFields(); err != nil {
			return fmt.Errorf("logstore: serialize before extract: %w", err)
		}
		payload := ExtractPayloadFiltered(entry, h.excludedPayloadFields)
		tags := BuildTags(entry)
		// Work on a shallow copy so the caller's entries are preserved on DB failure.
		dbEntry := *entry
		prepareDBEntry(&dbEntry, h.excludedPayloadFields)
		dbEntries[i] = &dbEntry
		uploads = append(uploads, pendingUpload{
			logID:     entry.ID,
			timestamp: entry.Timestamp,
			payload:   payload,
			tags:      tags,
		})
	}

	if err := h.inner.BatchCreateIfNotExists(ctx, dbEntries); err != nil {
		return err
	}

	for i, entry := range entries {
		entry.ContentSummary = dbEntries[i].ContentSummary
	}

	for _, u := range uploads {
		h.enqueueUpload(u.logID, u.timestamp, u.payload, u.tags)
	}
	return nil
}

func (h *HybridLogStore) FindByID(ctx context.Context, id string) (*Log, error) {
	log, err := h.inner.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	h.hydrateLog(ctx, log)
	return log, nil
}

// hydrateLog fetches the offloaded payload from object storage and merges it
// back into the Log struct. It is a no-op when HasObject is false.
//
// When requestedFields is non-empty, only the payload fields present in that
// projection are kept after merge — unrequested payload fields are cleared to
// honour projection semantics and avoid pulling large blobs unnecessarily.
func (h *HybridLogStore) hydrateLog(ctx context.Context, log *Log, requestedFields ...string) {
	if log == nil || !log.HasObject {
		return
	}
	key := ObjectKey(h.prefix, log.Timestamp, log.ID)
	data, err := h.objects.Get(ctx, key)
	if err != nil {
		h.logger.Warn("objectstore: failed to fetch payload for log %s: %v", log.ID, err)
		return // Graceful degradation
	}
	if mergeErr := MergePayloadFromJSON(log, data); mergeErr != nil {
		h.logger.Warn("objectstore: failed to merge payload for log %s: %v", log.ID, mergeErr)
		return
	}
	pruneUnrequestedPayloadFields(log, requestedFields)
}

func (h *HybridLogStore) Update(ctx context.Context, id string, entry any) error {
	// Pass through to inner store for index field updates.
	// Payload fields in the update map are handled separately by the logging plugin.
	return h.inner.Update(ctx, id, entry)
}

func (h *HybridLogStore) DeleteLog(ctx context.Context, id string) error {
	log, findErr := h.inner.FindByID(ctx, id)
	if findErr != nil && !errors.Is(findErr, ErrNotFound) {
		return findErr
	}
	if err := h.inner.DeleteLog(ctx, id); err != nil {
		return err
	}
	if log != nil && log.HasObject {
		key := ObjectKey(h.prefix, log.Timestamp, log.ID)
		if delErr := h.objects.Delete(ctx, key); delErr != nil {
			h.logger.Warn("objectstore: failed to delete object for log %s: %v", id, delErr)
		}
	}
	return nil
}

func (h *HybridLogStore) DeleteLogs(ctx context.Context, ids []string) error {
	// Collect keys for S3 deletion before removing from DB.
	var keys []string
	for _, id := range ids {
		log, findErr := h.inner.FindByID(ctx, id)
		if findErr != nil && !errors.Is(findErr, ErrNotFound) {
			return findErr
		}
		if log != nil && log.HasObject {
			keys = append(keys, ObjectKey(h.prefix, log.Timestamp, log.ID))
		}
	}
	if err := h.inner.DeleteLogs(ctx, ids); err != nil {
		return err
	}
	if len(keys) > 0 {
		if delErr := h.objects.DeleteBatch(ctx, keys); delErr != nil {
			h.logger.Warn("objectstore: failed to batch delete %d objects: %v", len(keys), delErr)
		}
	}
	return nil
}

func (h *HybridLogStore) DeleteLogsBatch(ctx context.Context, cutoff time.Time, batchSize int) (int64, error) {
	// Delegate to inner — S3 objects will be cleaned up by lifecycle policies.
	return h.inner.DeleteLogsBatch(ctx, cutoff, batchSize)
}

func (h *HybridLogStore) Close(ctx context.Context) error {
	h.closed.Store(true)
	close(h.uploadQueue)
	done := make(chan struct{})
	go func() {
		h.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		h.logger.Warn("objectstore: shutdown cancelled before upload queue drained: %v", ctx.Err())
		// Still wait for workers to finish so we don't close dependencies mid-flight.
		<-done
	}
	if err := h.objects.Close(); err != nil {
		h.logger.Warn("objectstore: error closing object store: %v", err)
	}
	return h.inner.Close(ctx)
}

// DroppedUploads returns the number of S3 uploads that were dropped.
func (h *HybridLogStore) DroppedUploads() int64 {
	return h.droppedUploads.Load()
}

// --- Delegated methods (pass through to inner store unchanged) ---

func (h *HybridLogStore) Ping(ctx context.Context) error {
	return h.inner.Ping(ctx)
}

func (h *HybridLogStore) FindFirst(ctx context.Context, query any, fields ...string) (*Log, error) {
	needsHydration := len(fields) == 0 || fieldsNeedHydration(fields)
	if needsHydration && len(fields) > 0 {
		fields = ensureHydrationFields(fields)
	}
	log, err := h.inner.FindFirst(ctx, query, fields...)
	if err != nil {
		return nil, err
	}
	if needsHydration {
		h.hydrateLog(ctx, log, fields...)
	}
	return log, nil
}

func (h *HybridLogStore) FindAll(ctx context.Context, query any, fields ...string) ([]*Log, error) {
	needsHydration := len(fields) == 0 || fieldsNeedHydration(fields)
	if needsHydration && len(fields) > 0 {
		fields = ensureHydrationFields(fields)
	}
	logs, err := h.inner.FindAll(ctx, query, fields...)
	if err != nil {
		return nil, err
	}
	if needsHydration {
		for _, log := range logs {
			h.hydrateLog(ctx, log, fields...)
		}
	}
	return logs, nil
}

func (h *HybridLogStore) FindAllDistinct(ctx context.Context, query any, fields ...string) ([]*Log, error) {
	return h.inner.FindAllDistinct(ctx, query, fields...)
}

func (h *HybridLogStore) HasLogs(ctx context.Context) (bool, error) {
	return h.inner.HasLogs(ctx)
}

func (h *HybridLogStore) SearchLogs(ctx context.Context, filters SearchFilters, pagination PaginationOptions) (*SearchResult, error) {
	return h.inner.SearchLogs(ctx, filters, pagination)
}

func (h *HybridLogStore) GetSessionLogs(ctx context.Context, sessionID string, pagination PaginationOptions) (*SessionDetailResult, error) {
	return h.inner.GetSessionLogs(ctx, sessionID, pagination)
}

func (h *HybridLogStore) GetSessionSummary(ctx context.Context, sessionID string) (*SessionSummaryResult, error) {
	return h.inner.GetSessionSummary(ctx, sessionID)
}

func (h *HybridLogStore) GetStats(ctx context.Context, filters SearchFilters) (*SearchStats, error) {
	return h.inner.GetStats(ctx, filters)
}

func (h *HybridLogStore) GetHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*HistogramResult, error) {
	return h.inner.GetHistogram(ctx, filters, bucketSizeSeconds)
}

func (h *HybridLogStore) GetTokenHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*TokenHistogramResult, error) {
	return h.inner.GetTokenHistogram(ctx, filters, bucketSizeSeconds)
}

func (h *HybridLogStore) GetCostHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*CostHistogramResult, error) {
	return h.inner.GetCostHistogram(ctx, filters, bucketSizeSeconds)
}

func (h *HybridLogStore) GetModelHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*ModelHistogramResult, error) {
	return h.inner.GetModelHistogram(ctx, filters, bucketSizeSeconds)
}

func (h *HybridLogStore) GetLatencyHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*LatencyHistogramResult, error) {
	return h.inner.GetLatencyHistogram(ctx, filters, bucketSizeSeconds)
}

func (h *HybridLogStore) GetProviderCostHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*ProviderCostHistogramResult, error) {
	return h.inner.GetProviderCostHistogram(ctx, filters, bucketSizeSeconds)
}

func (h *HybridLogStore) GetProviderTokenHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*ProviderTokenHistogramResult, error) {
	return h.inner.GetProviderTokenHistogram(ctx, filters, bucketSizeSeconds)
}

func (h *HybridLogStore) GetProviderLatencyHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*ProviderLatencyHistogramResult, error) {
	return h.inner.GetProviderLatencyHistogram(ctx, filters, bucketSizeSeconds)
}

func (h *HybridLogStore) GetModelRankings(ctx context.Context, filters SearchFilters) (*ModelRankingResult, error) {
	return h.inner.GetModelRankings(ctx, filters)
}

func (h *HybridLogStore) GetUserRankings(ctx context.Context, filters SearchFilters) (*UserRankingResult, error) {
	return h.inner.GetUserRankings(ctx, filters)
}

func (h *HybridLogStore) GetDimensionCostHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64, dimension HistogramDimension) (*DimensionCostHistogramResult, error) {
	return h.inner.GetDimensionCostHistogram(ctx, filters, bucketSizeSeconds, dimension)
}

func (h *HybridLogStore) GetDimensionTokenHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64, dimension HistogramDimension) (*DimensionTokenHistogramResult, error) {
	return h.inner.GetDimensionTokenHistogram(ctx, filters, bucketSizeSeconds, dimension)
}

func (h *HybridLogStore) GetDimensionLatencyHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64, dimension HistogramDimension) (*DimensionLatencyHistogramResult, error) {
	return h.inner.GetDimensionLatencyHistogram(ctx, filters, bucketSizeSeconds, dimension)
}

func (h *HybridLogStore) BulkUpdateCost(ctx context.Context, updates map[string]float64) error {
	return h.inner.BulkUpdateCost(ctx, updates)
}

func (h *HybridLogStore) Flush(ctx context.Context, since time.Time) error {
	return h.inner.Flush(ctx, since)
}

func (h *HybridLogStore) IsLogEntryPresent(ctx context.Context, id string) (bool, error) {
	return h.inner.IsLogEntryPresent(ctx, id)
}

func (h *HybridLogStore) GetDistinctAliases(ctx context.Context) ([]string, error) {
	return h.inner.GetDistinctAliases(ctx)
}

func (h *HybridLogStore) GetDistinctModels(ctx context.Context) ([]string, error) {
	return h.inner.GetDistinctModels(ctx)
}

func (h *HybridLogStore) GetDistinctKeyPairs(ctx context.Context, idCol, nameCol string) ([]KeyPairResult, error) {
	return h.inner.GetDistinctKeyPairs(ctx, idCol, nameCol)
}

func (h *HybridLogStore) GetDistinctRoutingEngines(ctx context.Context) ([]string, error) {
	return h.inner.GetDistinctRoutingEngines(ctx)
}

func (h *HybridLogStore) GetDistinctStopReasons(ctx context.Context) ([]string, error) {
	return h.inner.GetDistinctStopReasons(ctx)
}

func (h *HybridLogStore) GetDistinctMetadataKeys(ctx context.Context) (map[string][]string, error) {
	return h.inner.GetDistinctMetadataKeys(ctx)
}

// MCP Tool Log methods — delegated directly.

func (h *HybridLogStore) GetMCPHistogram(ctx context.Context, filters MCPToolLogSearchFilters, bucketSizeSeconds int64) (*MCPHistogramResult, error) {
	return h.inner.GetMCPHistogram(ctx, filters, bucketSizeSeconds)
}

func (h *HybridLogStore) GetMCPCostHistogram(ctx context.Context, filters MCPToolLogSearchFilters, bucketSizeSeconds int64) (*MCPCostHistogramResult, error) {
	return h.inner.GetMCPCostHistogram(ctx, filters, bucketSizeSeconds)
}

func (h *HybridLogStore) GetMCPTopTools(ctx context.Context, filters MCPToolLogSearchFilters, limit int) (*MCPTopToolsResult, error) {
	return h.inner.GetMCPTopTools(ctx, filters, limit)
}

func (h *HybridLogStore) CreateMCPToolLog(ctx context.Context, entry *MCPToolLog) error {
	return h.inner.CreateMCPToolLog(ctx, entry)
}

func (h *HybridLogStore) FindMCPToolLog(ctx context.Context, id string) (*MCPToolLog, error) {
	return h.inner.FindMCPToolLog(ctx, id)
}

func (h *HybridLogStore) UpdateMCPToolLog(ctx context.Context, id string, entry any) error {
	return h.inner.UpdateMCPToolLog(ctx, id, entry)
}

func (h *HybridLogStore) SearchMCPToolLogs(ctx context.Context, filters MCPToolLogSearchFilters, pagination PaginationOptions) (*MCPToolLogSearchResult, error) {
	return h.inner.SearchMCPToolLogs(ctx, filters, pagination)
}

func (h *HybridLogStore) GetMCPToolLogStats(ctx context.Context, filters MCPToolLogSearchFilters) (*MCPToolLogStats, error) {
	return h.inner.GetMCPToolLogStats(ctx, filters)
}

func (h *HybridLogStore) HasMCPToolLogs(ctx context.Context) (bool, error) {
	return h.inner.HasMCPToolLogs(ctx)
}

func (h *HybridLogStore) DeleteMCPToolLogs(ctx context.Context, ids []string) error {
	return h.inner.DeleteMCPToolLogs(ctx, ids)
}

func (h *HybridLogStore) FlushMCPToolLogs(ctx context.Context, since time.Time) error {
	return h.inner.FlushMCPToolLogs(ctx, since)
}

func (h *HybridLogStore) GetAvailableToolNames(ctx context.Context) ([]string, error) {
	return h.inner.GetAvailableToolNames(ctx)
}

func (h *HybridLogStore) GetAvailableServerLabels(ctx context.Context) ([]string, error) {
	return h.inner.GetAvailableServerLabels(ctx)
}

func (h *HybridLogStore) GetAvailableMCPVirtualKeys(ctx context.Context) ([]MCPToolLog, error) {
	return h.inner.GetAvailableMCPVirtualKeys(ctx)
}

// Async Job methods — delegated directly.

func (h *HybridLogStore) CreateAsyncJob(ctx context.Context, job *AsyncJob) error {
	return h.inner.CreateAsyncJob(ctx, job)
}

func (h *HybridLogStore) FindAsyncJobByID(ctx context.Context, id string) (*AsyncJob, error) {
	return h.inner.FindAsyncJobByID(ctx, id)
}

func (h *HybridLogStore) UpdateAsyncJob(ctx context.Context, id string, updates map[string]interface{}) error {
	return h.inner.UpdateAsyncJob(ctx, id, updates)
}

func (h *HybridLogStore) DeleteExpiredAsyncJobs(ctx context.Context) (int64, error) {
	return h.inner.DeleteExpiredAsyncJobs(ctx)
}

func (h *HybridLogStore) DeleteStaleAsyncJobs(ctx context.Context, staleSince time.Time) (int64, error) {
	return h.inner.DeleteStaleAsyncJobs(ctx, staleSince)
}
