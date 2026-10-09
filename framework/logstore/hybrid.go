package logstore

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/objectstore"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"gorm.io/gorm"
)

const (
	defaultUploadWorkers       = 10
	defaultUploadQueueSize     = 5000
	maxContentSummaryBytes     = 2048
	defaultMaxUploadQueueBytes = 1 << 30 // 1 GiB
)

type uploadKind uint8

const (
	uploadKindLog uploadKind = iota
	uploadKindMCP
	uploadKindAgent
)

// uploadWork represents an async S3 upload job.
type uploadWork struct {
	queuedAt   time.Time
	generation uint64 // In-process ordering; persisted records use queuedAt.
	logID      string
	timestamp  time.Time
	key        string
	kind       uploadKind
	status     string
	payload    []byte // JSON-encoded payload
	tags       map[string]string
	deleteOnly bool // Durable remote cleanup; never upload or evict its record.
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
	outbox         *uploadOutbox
	outboxCancel   context.CancelFunc
	uploadLocks    [64]sync.Mutex
	// excludedPayloadFields is the set of payload field names (DB column names) that must NOT be offloaded to object storage and must remain in the DB.
	excludedPayloadFields map[string]struct{}
	// excludedRequestTypes is the set of log object types that are never offloaded; their rows stay complete in the DB.
	excludedRequestTypes map[string]struct{}
	// offloadErrorRawBodies: error_details is DB-resident only through databaseResidentPayloadFields, so its raw bodies are offloaded.
	offloadErrorRawBodies bool
}

type scopedDBLogStore interface {
	ScopedDB(context.Context) *gorm.DB
}

// databaseResidentPayloadFields stay in the database whatever the configured
// exclusions say. error_details backs the error_type, error_code and
// status_code rankings and filters, which query the column directly, so
// offloading it left every error breakdown empty. It is not request content,
// except for the provider raw bodies it can carry, which
// moveErrorRawBodiesToObject sends to object storage. Content-hidden rows
// still offload it with everything else.
//
// This applies to logs written from this change on. Rows written before it
// hold error_details only in their object, so they stay out of the error
// breakdowns: restoring them means one object read per failed log, with no
// bound on the count, and is not done automatically.
var databaseResidentPayloadFields = []string{"error_details"}

// newHybridLogStore creates a HybridLogStore wrapping the given inner store.
// excludeFields lists payload field DB column names that should be kept in the
// database rather than offloaded to object storage, on top of
// databaseResidentPayloadFields. Pass nil to offload every other payload field. excludeRequestTypes lists log
// object types whose rows are written to the DB unchanged and never uploaded.
func newHybridLogStore(inner LogStore, objects objectstore.ObjectStore, prefix string, logger schemas.Logger, excludeFields []string, excludeRequestTypes []string) (*HybridLogStore, error) {
	outbox, err := newUploadOutboxFromEnv()
	if err != nil {
		return nil, err
	}
	excluded := make(map[string]struct{}, len(excludeFields)+len(databaseResidentPayloadFields))
	for _, f := range databaseResidentPayloadFields {
		excluded[f] = struct{}{}
	}
	offloadErrorRawBodies := true
	for _, f := range excludeFields {
		excluded[f] = struct{}{}
		if f == "error_details" {
			offloadErrorRawBodies = false
		}
	}
	excludedTypes := make(map[string]struct{}, len(excludeRequestTypes))
	for _, t := range excludeRequestTypes {
		if t = strings.TrimSpace(t); t != "" {
			excludedTypes[t] = struct{}{}
		}
	}
	h := &HybridLogStore{
		inner:                 inner,
		objects:               objects,
		prefix:                prefix,
		logger:                logger,
		uploadQueue:           make(chan *uploadWork, defaultUploadQueueSize),
		excludedPayloadFields: excluded,
		excludedRequestTypes:  excludedTypes,
		offloadErrorRawBodies: offloadErrorRawBodies,
		outbox:                outbox,
	}
	// Start upload workers.
	for i := 0; i < defaultUploadWorkers; i++ {
		h.wg.Add(1)
		go h.uploadWorker()
	}
	if outbox != nil {
		ctx, cancel := context.WithCancel(context.Background())
		h.outboxCancel = cancel
		h.wg.Add(1)
		go h.runUploadOutbox(ctx)
		logger.Info("objectstore: upload outbox enabled at %s (max %d bytes, retry interval %s)", outbox.dir, outbox.maxBytes, uploadOutboxInterval)
	}
	return h, nil
}

// uploadWorker processes async S3 upload jobs from the queue.
func (h *HybridLogStore) uploadWorker() {
	defer h.wg.Done()
	for work := range h.uploadQueue {
		h.processUpload(work)
	}
}

// processUpload uploads a single payload to object storage.
// Failed uploads are persisted when a disk outbox is configured; otherwise
// they are dropped. The DB row retains has_object=false until upload succeeds.
func (h *HybridLogStore) processUpload(work *uploadWork) {
	payloadSize := int64(len(work.payload))
	defer h.pendingBytes.Add(-payloadSize)
	if h.outbox != nil {
		defer h.outbox.finishUpload(work)
	}

	defer func() {
		if r := recover(); r != nil {
			h.logger.Error("objectstore: panic in upload worker (recovered): %v", r)
			h.preserveFailedUpload(work)
		}
	}()
	if h.outbox != nil {
		lock := h.uploadLock(work.key)
		lock.Lock()
		defer lock.Unlock()
		if h.outbox.superseded(work) {
			return
		}
	}

	if err := h.uploadAndMark(context.Background(), work); errors.Is(err, errUploadObsolete) {
		h.droppedUploads.Add(1)
	} else if err != nil {
		h.logger.Warn("objectstore: failed to upload log %s: %v", work.logID, err)
		h.preserveFailedUpload(work)
		return
	}
	if h.outbox != nil {
		if err := h.outbox.removeThrough(work); err != nil {
			h.logger.Warn("objectstore: failed to remove completed outbox upload for log %s: %v", work.logID, err)
		}
	}
}

// uploadLock returns the lock that orders network operations for an object key.
func (h *HybridLogStore) uploadLock(key string) *sync.Mutex {
	digest := sha256.Sum256([]byte(key))
	return &h.uploadLocks[int(digest[0])%len(h.uploadLocks)]
}

// uploadAndMark is shared by live uploads and disk retries. A successful Put
// alone is insufficient: readers only hydrate payloads after has_object is set.
func (h *HybridLogStore) uploadAndMark(ctx context.Context, work *uploadWork) error {
	if work.deleteOnly {
		return h.removeDeletedUpload(ctx, work)
	}
	putCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	switch work.kind {
	case uploadKindLog:
		if h.outbox != nil {
			present, err := h.inner.IsLogEntryPresent(putCtx, work.logID)
			if err != nil {
				return err
			}
			if !present {
				return h.removeDeletedUpload(ctx, work)
			}
		}
	case uploadKindMCP:
		current, err := h.inner.FindMCPToolLog(putCtx, work.logID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return h.removeDeletedUpload(ctx, work)
			}
			return fmt.Errorf("check MCP tool log before upload: %w", err)
		}
		if h.outbox == nil && work.status != "" && current.Status != work.status {
			return nil // A superseded snapshot is not a lost log.
		}
		if h.outbox != nil {
			// The DB owns current metadata; the pending snapshot owns the full
			// payload fields. A status change does not invalidate those fields.
			var pending MCPToolLog
			if err := MergeMCPToolLogPayloadFromJSON(&pending, work.payload); err != nil {
				return err
			}
			current.Arguments, current.ArgumentsParsed = pending.Arguments, pending.ArgumentsParsed
			current.Result, current.ResultParsed = pending.Result, pending.ResultParsed
			current.ErrorDetails, current.ErrorDetailsParsed = pending.ErrorDetails, pending.ErrorDetailsParsed
			payload, err := MarshalMCPToolLogPayload(current)
			if err != nil {
				return err
			}
			work.payload, work.tags = payload, BuildMCPToolTags(current)
		}
	case uploadKindAgent:
		current, err := h.inner.FindAgentLog(putCtx, work.logID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				return h.removeDeletedUpload(ctx, work)
			}
			return fmt.Errorf("check agent log before upload: %w", err)
		}
		if h.outbox == nil && work.status != "" && current.Status != work.status {
			return nil // A superseded snapshot is not a lost log.
		}
		if h.outbox != nil {
			work.tags = BuildAgentLogTags(current)
		}
	}

	err := h.objects.Put(putCtx, work.key, work.payload, work.tags)
	cancel()
	if err != nil {
		return err
	}

	// Mark the DB row as having an object. Use a fresh context so that a slow
	// Put doesn't starve the DB update of its deadline. Retry up to 3 times
	// with exponential backoff to avoid orphaning the uploaded object.
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		dbCtx, dbCancel := context.WithTimeout(ctx, 10*time.Second)
		var err error
		switch work.kind {
		case uploadKindMCP:
			err = h.inner.UpdateMCPToolLog(dbCtx, work.logID, map[string]interface{}{"has_object": true})
		case uploadKindAgent:
			err = h.inner.UpdateAgentLog(dbCtx, work.logID, map[string]interface{}{"has_object": true})
		default:
			err = h.inner.Update(dbCtx, work.logID, map[string]interface{}{"has_object": true})
		}
		dbCancel()
		if err == nil {
			return nil
		}
		if h.outbox != nil && errors.Is(err, ErrNotFound) {
			return h.removeDeletedUpload(ctx, work)
		}
		lastErr = err
		h.logger.Warn("objectstore: failed to set has_object for log %s (attempt %d/3): %v", work.logID, attempt+1, err)
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(1<<attempt) * time.Second):
			}
		}
	}
	h.logger.Error("objectstore: failed to set has_object for log %s after 3 attempts; payload orphaned in object store", work.logID)
	return fmt.Errorf("set has_object after upload: %w", lastErr)
}

// removeDeletedUpload cleans up an object whose row disappeared before or during
// Put. It retains cleanup metadata until deletion succeeds and returns errUploadObsolete.
func (h *HybridLogStore) removeDeletedUpload(ctx context.Context, work *uploadWork) error {
	if h.outbox != nil {
		// Scrub content even if the remote cleanup itself is unavailable.
		copy := *work
		copy.deleteOnly = true
		if err := h.outbox.save(&copy); err != nil {
			h.logger.Error("objectstore: failed to retire pending log %s: %v", work.logID, err)
		}
		deleteCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := h.objects.Delete(deleteCtx, work.key); err != nil {
			return fmt.Errorf("delete object for removed log: %w", err)
		}
		if err := h.outbox.removeThrough(&copy); err != nil {
			return fmt.Errorf("remove completed deletion from outbox: %w", err)
		}
	}
	return errUploadObsolete
}

// isPayloadEmpty returns true when the payload carries no offloadable content.
// Only the large TEXT payload fields count: metadata like provider, model,
// status and timestamp is populated on every entry (see ExtractPayload), so
// checking the whole map would never report empty and would defeat the skip
// for initial "processing" rows that have no input/output data yet. Skipping
// uploads for empty payloads avoids wasted S3 PUTs.
func isPayloadEmpty(payload map[string]string) bool {
	for _, f := range payloadFields {
		if payload[f] != "" {
			return false
		}
	}
	return true
}

// enqueueUpload pushes an upload job onto the queue. Overflow is written to the
// disk outbox when enabled, keeping the in-memory queue bounded.
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
	h.enqueueRawUpload(logID, timestamp, ObjectKey(h.prefix, timestamp, logID), uploadKindLog, "", data, tags)
}

// enqueueRawUpload submits a pre-serialized payload to the upload queue.
// Queue and memory overflow are persisted when an outbox is enabled, otherwise
// dropped. Used by regular, MCP tool, and agent log uploads.
func (h *HybridLogStore) enqueueRawUpload(logID string, timestamp time.Time, key string, kind uploadKind, status string, data []byte, tags map[string]string) {
	if h.closed.Load() || len(data) == 0 {
		return
	}
	if int64(len(data)) > defaultMaxUploadQueueBytes {
		h.logger.Warn("objectstore: payload exceeds upload memory limit for log %s", logID)
		h.droppedUploads.Add(1)
		return
	}
	work := &uploadWork{queuedAt: time.Now().UTC(), logID: logID, timestamp: timestamp, key: key, kind: kind, status: status, payload: data, tags: tags}
	queued := false
	if h.outbox != nil {
		h.outbox.trackUpload(work)
		defer func() {
			if !queued {
				h.outbox.finishUpload(work)
			}
		}()
	}
	reserved := false
	defer func() {
		if r := recover(); r != nil {
			if reserved {
				h.pendingBytes.Add(-int64(len(data)))
			}
			h.preserveFailedUpload(work)
		}
	}()
	if !h.reserveUploadBytes(int64(len(data))) {
		h.logger.Warn("objectstore: upload queue memory limit reached for log %s", logID)
		h.preserveFailedUpload(work)
		return
	}
	reserved = true
	select {
	case h.uploadQueue <- work:
		queued = true
	default:
		h.pendingBytes.Add(-int64(len(data)))
		reserved = false
		h.logger.Warn("objectstore: upload queue full for log %s", logID)
		h.preserveFailedUpload(work)
	}
}

// reserveUploadBytes atomically reserves bytes shared by live uploads and disk retries, returning
// false when the budget is exhausted.
func (h *HybridLogStore) reserveUploadBytes(size int64) bool {
	for {
		pending := h.pendingBytes.Load()
		if size > defaultMaxUploadQueueBytes-pending {
			return false
		}
		if h.pendingBytes.CompareAndSwap(pending, pending+size) {
			return true
		}
	}
}

// --- Intercepted methods ---

// prepareDBEntry builds the lightweight DB entry by extracting the content
// summary, trimming input history to the last user message, and clearing
// payload fields that will be offloaded to object storage. Fields in the
// excluded set are kept intact in the DB row. Pricing metadata is always kept
// DB-resident regardless of hybrid configuration or content visibility.
// Must be called after SerializeFields() populates the Parsed fields.
func prepareDBEntry(dbEntry *Log, excluded map[string]struct{}) {
	tokenUsage := dbEntry.TokenUsage
	cacheMetadata := dbEntry.CacheDebug
	tokenUsageParsed := dbEntry.TokenUsageParsed
	cacheDebugParsed := dbEntry.CacheDebugParsed
	restorePricingMetadata := func() {
		dbEntry.TokenUsage = tokenUsage
		dbEntry.CacheDebug = cacheMetadata
		dbEntry.TokenUsageParsed = tokenUsageParsed
		dbEntry.CacheDebugParsed = cacheDebugParsed
	}

	// Hidden entries keep no request/response content in the DB row: no summary,
	// last-user-message preview, or exclusion-list carve-outs. Pricing metadata is
	// not content and remains in the log store.
	if dbEntry.ContentHidden {
		ClearPayload(dbEntry)
		restorePricingMetadata()
		dbEntry.ContentSummary = ""
		return
	}

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
	// Attachment payloads (base64 images/files/audio, media URLs) are replaced
	// with a placeholder so they never bloat the DB row; the full message stays
	// in object storage and is merged back on read.
	var lastUserMessage string
	if idx >= 0 {
		stripped := stripChatMessageAttachments(&dbEntry.InputHistoryParsed[idx])
		lastUserMessage, _ = sonic.MarshalString([]schemas.ChatMessage{stripped})
	}

	// Responses API requests carry their history in responses_input_history
	// rather than input_history. Preserve the last user message there too, so
	// the log list can render a preview instead of "-" once the full history is
	// offloaded to object storage. Only needed when there is no chat input
	// history (a request is either chat- or responses-shaped, never both).
	var lastUserResponsesMessage string
	if idx < 0 {
		if responsesIdx := findLastUserResponsesMessageIndex(dbEntry.ResponsesInputHistoryParsed); responsesIdx >= 0 {
			stripped := stripResponsesMessageAttachments(&dbEntry.ResponsesInputHistoryParsed[responsesIdx])
			lastUserResponsesMessage, _ = sonic.MarshalString([]schemas.ResponsesMessage{stripped})
		}
	}

	ClearPayloadFiltered(dbEntry, excluded)
	restorePricingMetadata()

	if _, hasInputHistoryExclusion := excluded["input_history"]; !hasInputHistoryExclusion {
		dbEntry.InputHistory = sanitizeJSONForJSONB(lastUserMessage)
	}
	if _, hasResponsesInputHistoryExclusion := excluded["responses_input_history"]; !hasResponsesInputHistoryExclusion {
		dbEntry.ResponsesInputHistory = sanitizeJSONForJSONB(lastUserResponsesMessage)
	}
}

// Create writes a lightweight DB row (with payload fields stripped per
// excludedPayloadFields) and asynchronously offloads the full payload to
// object storage. The caller's entry is preserved on DB failure by writing to
// a shallow copy; on success, only ContentSummary is propagated back so the
// caller can observe what was persisted.
// extractUploadPayload returns the payload map to offload for entry. Hidden
// entries always upload the complete payload: the exclusion list exists to
// keep fields DB-resident, and hidden rows must not retain content in the DB.
func (h *HybridLogStore) extractUploadPayload(entry *Log) map[string]string {
	if entry.ContentHidden {
		return ExtractPayload(entry)
	}
	return ExtractPayloadFiltered(entry, h.excludedPayloadFields)
}

// errorRawBodyPaths are the error_details paths holding provider raw bodies,
// present only when content logging and raw storage are both on.
var errorRawBodyPaths = []string{"extra_fields.raw_request", "extra_fields.raw_response"}

// moveErrorRawBodiesToObject keeps error_details in the DB row without the
// provider raw bodies it carries, and puts the whole value in the upload
// payload instead, which FindByID merges back over the row's copy. Without it,
// keeping error_details DB-resident would keep raw bodies in the database even
// though raw_request and raw_response are offloaded. An operator who lists
// error_details in object_storage_exclude_fields keeps it whole in the DB.
// Content-hidden rows already offload error_details in full.
func (h *HybridLogStore) moveErrorRawBodiesToObject(dbEntry *Log, payload map[string]string) {
	if !h.offloadErrorRawBodies || dbEntry.ContentHidden || dbEntry.ErrorDetails == "" {
		return
	}
	stripped := dbEntry.ErrorDetails
	for _, path := range errorRawBodyPaths {
		if !gjson.Get(stripped, path).Exists() {
			continue
		}
		next, err := sjson.Delete(stripped, path)
		if err != nil {
			return
		}
		stripped = next
	}
	if stripped == dbEntry.ErrorDetails {
		return
	}
	payload["error_details"] = dbEntry.ErrorDetails
	dbEntry.ErrorDetails = stripped
	// SerializeFields on the write path would re-serialize a parsed copy over it.
	dbEntry.ErrorDetailsParsed = nil
}

// skipsOffload reports whether entry's request type is excluded from object
// storage. Excluded entries are written to the inner store unchanged, which
// builds their content summary on write. Hidden entries are always offloaded:
// their content may only live in object storage, never in the DB row.
func (h *HybridLogStore) skipsOffload(entry *Log) bool {
	if entry.ContentHidden {
		return false
	}
	_, ok := h.excludedRequestTypes[entry.Object]
	return ok
}

func (h *HybridLogStore) Create(ctx context.Context, entry *Log) error {
	if h.skipsOffload(entry) {
		return h.inner.Create(ctx, entry)
	}
	if err := entry.SerializeFields(); err != nil {
		return fmt.Errorf("logstore: serialize before extract: %w", err)
	}
	payload := h.extractUploadPayload(entry)
	tags := BuildTags(entry)
	// Work on a shallow copy so the caller's entry is preserved on DB failure.
	dbEntry := *entry
	prepareDBEntry(&dbEntry, h.excludedPayloadFields)
	h.moveErrorRawBodiesToObject(&dbEntry, payload)
	if err := h.inner.Create(ctx, &dbEntry); err != nil {
		return err
	}
	entry.ContentSummary = dbEntry.ContentSummary
	h.enqueueUpload(entry.ID, entry.Timestamp, payload, tags)
	return nil
}

// CreateIfNotExists writes the lightweight DB row only when no row with the
// same ID exists, then offloads the full payload to object storage on insert.
// Same payload-stripping and shallow-copy semantics as Create.
func (h *HybridLogStore) CreateIfNotExists(ctx context.Context, entry *Log) error {
	if h.skipsOffload(entry) {
		return h.inner.CreateIfNotExists(ctx, entry)
	}
	if err := entry.SerializeFields(); err != nil {
		return fmt.Errorf("logstore: serialize before extract: %w", err)
	}
	payload := h.extractUploadPayload(entry)
	tags := BuildTags(entry)
	// Work on a shallow copy so the caller's entry is preserved on DB failure.
	dbEntry := *entry
	prepareDBEntry(&dbEntry, h.excludedPayloadFields)
	h.moveErrorRawBodiesToObject(&dbEntry, payload)
	if err := h.inner.CreateIfNotExists(ctx, &dbEntry); err != nil {
		return err
	}
	entry.ContentSummary = dbEntry.ContentSummary
	h.enqueueUpload(entry.ID, entry.Timestamp, payload, tags)
	return nil
}

// BatchCreateIfNotExists inserts many lightweight DB rows in a single inner
// call, then enqueues one object-storage upload per inserted entry. Nil
// entries are skipped. On DB failure no uploads are enqueued.
func (h *HybridLogStore) BatchCreateIfNotExists(ctx context.Context, entries []*Log) error {
	if len(entries) == 0 {
		return nil
	}
	type pendingUpload struct {
		logID     string
		timestamp time.Time
		payload   map[string]string
		tags      map[string]string
	}
	var uploads []pendingUpload

	dbEntries := make([]*Log, 0, len(entries))
	origEntries := make([]*Log, 0, len(entries))
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		if h.skipsOffload(entry) {
			dbEntries = append(dbEntries, entry)
			origEntries = append(origEntries, entry)
			continue
		}
		if err := entry.SerializeFields(); err != nil {
			return fmt.Errorf("logstore: serialize before extract: %w", err)
		}
		payload := h.extractUploadPayload(entry)
		tags := BuildTags(entry)
		// Work on a shallow copy so the caller's entries are preserved on DB failure.
		dbEntry := *entry
		prepareDBEntry(&dbEntry, h.excludedPayloadFields)
		h.moveErrorRawBodiesToObject(&dbEntry, payload)
		dbEntries = append(dbEntries, &dbEntry)
		origEntries = append(origEntries, entry)
		uploads = append(uploads, pendingUpload{
			logID:     entry.ID,
			timestamp: entry.Timestamp,
			payload:   payload,
			tags:      tags,
		})
	}

	if len(dbEntries) == 0 {
		return nil
	}
	if err := h.inner.BatchCreateIfNotExists(ctx, dbEntries); err != nil {
		return err
	}

	for i, entry := range origEntries {
		entry.ContentSummary = dbEntries[i].ContentSummary
	}

	for _, u := range uploads {
		h.enqueueUpload(u.logID, u.timestamp, u.payload, u.tags)
	}
	return nil
}

// FindByID loads a log row from the DB and hydrates its payload fields from
// object storage when HasObject is true. Object-store fetch failures are
// logged but not returned, so the caller still receives the DB row.
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
	// ContentHidden is the read-side enforcement for hidden logs: the payload
	// stays in object storage and is never fetched back into the serving path.
	if log == nil || !log.HasObject || log.ContentHidden {
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

// hydrateMCPToolLog fetches the offloaded payload from object storage and
// merges it into the MCPToolLog. It is a no-op when log is nil or HasObject
// is false. Errors from the underlying object-store fetch are returned to the
// caller (unlike hydrateLog, MCP tool log payloads are not optional).
func (h *HybridLogStore) hydrateMCPToolLog(ctx context.Context, log *MCPToolLog) error {
	if log == nil || !log.HasObject {
		return nil
	}
	return h.hydrateMCPToolLogFromObject(ctx, log)
}

// hydrateMCPToolLogFromObject unconditionally fetches the payload from object
// storage (skipping the HasObject check) and merges it into the log. Used on
// the update path where the payload must be loaded even when has_object has
// not yet been set on the DB row.
func (h *HybridLogStore) hydrateMCPToolLogFromObject(ctx context.Context, log *MCPToolLog) error {
	if log == nil {
		return nil
	}
	key := MCPToolObjectKey(h.prefix, log.Timestamp, log.ID)
	data, err := h.objects.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("objectstore: fetch MCP tool log payload for %s: %w", log.ID, err)
	}
	if mergeErr := MergeMCPToolLogPayloadFromJSON(log, data); mergeErr != nil {
		return fmt.Errorf("objectstore: merge MCP tool log payload for %s: %w", log.ID, mergeErr)
	}
	return nil
}

// Update passes the update through to the inner store for index/scalar field
// updates. Payload-field changes are not re-offloaded here; the logging
// plugin owns that path and calls processUpload directly when it needs to.
func (h *HybridLogStore) Update(ctx context.Context, id string, entry any) error {
	// Pass through to inner store for index field updates.
	// Payload fields in the update map are handled separately by the logging plugin.
	return h.inner.Update(ctx, id, entry)
}

// DeleteLog removes a single log row from the DB and, when HasObject is true,
// also deletes the corresponding payload from object storage. Object-store
// delete failures are logged but do not fail the operation, since the DB row
// (the source of truth for visibility) is already gone.
func (h *HybridLogStore) DeleteLog(ctx context.Context, id string) error {
	log, findErr := h.inner.FindByID(ctx, id)
	if findErr != nil && !errors.Is(findErr, ErrNotFound) {
		return findErr
	}
	if err := h.inner.DeleteLog(ctx, id); err != nil {
		return err
	}
	if h.outbox != nil && log != nil {
		h.retireDeletedUploads(ctx, []uploadWork{{logID: log.ID, timestamp: log.Timestamp, key: ObjectKey(h.prefix, log.Timestamp, log.ID)}})
		return nil
	}
	if log != nil && log.HasObject {
		key := ObjectKey(h.prefix, log.Timestamp, log.ID)
		if delErr := h.objects.Delete(ctx, key); delErr != nil {
			h.logger.Warn("objectstore: failed to delete object for log %s: %v", id, delErr)
		}
	}
	return nil
}

// DeleteLogs removes many log rows from the DB and batches a single
// DeleteBatch call against object storage for any rows that had HasObject
// set. Keys are collected before the DB delete so we can still identify
// object-store entries to clean up.
func (h *HybridLogStore) DeleteLogs(ctx context.Context, ids []string) error {
	// Collect keys for S3 deletion before removing from DB.
	var keys []string
	var pending []uploadWork
	for _, id := range ids {
		log, findErr := h.inner.FindByID(ctx, id)
		if findErr != nil && !errors.Is(findErr, ErrNotFound) {
			return findErr
		}
		if log != nil && log.HasObject {
			keys = append(keys, ObjectKey(h.prefix, log.Timestamp, log.ID))
		}
		if h.outbox != nil && log != nil {
			pending = append(pending, uploadWork{logID: log.ID, timestamp: log.Timestamp, key: ObjectKey(h.prefix, log.Timestamp, log.ID)})
		}
	}
	if err := h.inner.DeleteLogs(ctx, ids); err != nil {
		return err
	}
	if h.outbox != nil {
		h.retireDeletedUploads(ctx, pending)
		return nil
	}
	if len(keys) > 0 {
		if delErr := h.objects.DeleteBatch(ctx, keys); delErr != nil {
			h.logger.Warn("objectstore: failed to batch delete %d objects: %v", len(keys), delErr)
		}
	}
	return nil
}

// DeleteLogsBatch deletes old DB rows older than cutoff in batches of
// batchSize. Object-store entries are intentionally NOT deleted here: the
// expectation is that the bucket has a lifecycle policy configured to expire
// objects on the same schedule, which is far cheaper than per-row DELETEs.
func (h *HybridLogStore) DeleteLogsBatch(ctx context.Context, cutoff time.Time, batchSize int) (int64, error) {
	// Delegate to inner — S3 objects will be cleaned up by lifecycle policies.
	return h.inner.DeleteLogsBatch(ctx, cutoff, batchSize)
}

// DeleteMCPToolLogsBatch deletes old MCP tool log rows in batches. Like
// DeleteLogsBatch, object-store entries are left to the bucket lifecycle policy.
func (h *HybridLogStore) DeleteMCPToolLogsBatch(ctx context.Context, cutoff time.Time, batchSize int) (int64, error) {
	return h.inner.DeleteMCPToolLogsBatch(ctx, cutoff, batchSize)
}

// Close shuts the store down cleanly: marks the store closed (so further
// enqueues are dropped), closes the upload queue, waits for workers to drain
// any in-flight uploads, then closes the object store and the inner store.
// If ctx is cancelled before workers drain, the cancellation is logged but
// Close still waits for workers to exit to avoid closing dependencies
// mid-flight.
func (h *HybridLogStore) Close(ctx context.Context) error {
	h.closed.Store(true)
	if h.outboxCancel != nil {
		h.outboxCancel()
	}
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

// ScopedDB exposes the underlying RDB query surface when hybrid mode wraps a
// Postgres/SQLite logstore. Enterprise logstore extensions use this for their
// own logstore-owned tables, such as alert history.
func (h *HybridLogStore) ScopedDB(ctx context.Context) *gorm.DB {
	if scoped, ok := h.inner.(scopedDBLogStore); ok {
		return scoped.ScopedDB(ctx)
	}
	return nil
}

// Ping delegates to the inner store's health check.
func (h *HybridLogStore) Ping(ctx context.Context) error {
	return h.inner.Ping(ctx)
}

// FindFirst returns the first log matching query. When the projection asks
// for payload-bearing fields (or the projection is empty, implying full
// hydration), the result is hydrated from object storage; otherwise the DB
// row is returned as-is.
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

// FindAll returns all logs matching query. Each result is hydrated from
// object storage when the projection requires payload fields. Per-row
// hydration failures are logged inside hydrateLog and do not fail the call.
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

// FindAllDistinct delegates to the inner store. No hydration is performed:
// distinct queries operate purely on DB-resident columns and never need the
// offloaded payload.
func (h *HybridLogStore) FindAllDistinct(ctx context.Context, query any, fields ...string) ([]*Log, error) {
	return h.inner.FindAllDistinct(ctx, query, fields...)
}

// HasLogs delegates to the inner store and reports whether any log rows
// exist. The object store is not consulted.
func (h *HybridLogStore) HasLogs(ctx context.Context) (bool, error) {
	return h.inner.HasLogs(ctx)
}

// SearchLogs delegates to the inner store. Search operates on indexed DB
// columns (including ContentSummary); payloads are not hydrated here.
func (h *HybridLogStore) SearchLogs(ctx context.Context, filters SearchFilters, pagination PaginationOptions) (*SearchResult, error) {
	return h.inner.SearchLogs(ctx, filters, pagination)
}

// SearchLogsForBilling delegates to the inner store. Hydration is deliberately NOT
// done here — see HydrateBillingChunk.
func (h *HybridLogStore) SearchLogsForBilling(ctx context.Context, filters SearchFilters, pagination PaginationOptions) (*SearchResult, error) {
	return h.inner.SearchLogsForBilling(ctx, filters, pagination)
}

// HydrateBillingChunk restores offloaded pricing inputs for a small slice of rows.
//
// This exists because of a real mis-billing bug: a row whose token_usage was offloaded
// comes back with a stub rebuilt from denormalized columns, and because PromptTokens is
// inclusive of the cache buckets, pricing that stub charges every cache-read token at
// the full input rate — inflating a cache-heavy request several fold.
//
// Two deliberate properties:
//
// SEQUENTIAL — one object in flight at a time. A parallel fan-out multiplies peak
// memory by its width, since each in-flight fetch holds the whole object as a []byte
// plus the map[string]string that MergePayloadFromJSON unmarshals it into, and objects
// can carry full message histories and raw request/response bodies. This runs inside a
// checkpointed background job, so predictability beats throughput. Combined with the
// caller paging and chunking at BillingHydrationChunkSize, a recompute worker's
// payload memory is bounded by a handful of rows no matter how large the window.
//
// SKIPS ROWS THAT DO NOT NEED IT — a row already carrying its pricing inputs costs no
// I/O at all. See billingRowNeedsHydration.
func (h *HybridLogStore) HydrateBillingChunk(ctx context.Context, logs []*Log) (BillingHydrationResult, error) {
	var result BillingHydrationResult
	for _, log := range logs {
		if log == nil || !log.HasObject {
			// Never offloaded: the row already carries its own payload.
			continue
		}
		if !billingRowNeedsHydration(log, h.excludedPayloadFields) {
			continue
		}
		// Checked per row rather than relying on Get to notice: on shutdown or job
		// cancellation this should stop fetching immediately, not walk the rest of
		// the chunk collecting context errors.
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if hydrateErr := h.hydrateLogForBilling(ctx, log); hydrateErr != nil {
			h.logger.Warn("objectstore: cannot hydrate pricing inputs for log %s: %v", log.ID, hydrateErr)
			result.Unpriceable = append(result.Unpriceable, log.ID)
			continue
		}
		result.Hydrated = append(result.Hydrated, log.ID)
	}
	return result, nil
}

// billingRowNeedsHydration reports whether an offloaded row is actually missing
// something pricing reads. Fetching a whole object to recover fields the row already
// has is pure waste — and because the store exposes only whole-key Get, one wasted
// fetch downloads the entire payload, message histories included.
//
// excluded is the store's object_storage_exclude_fields set, and it is what makes the
// check precise. An empty column on its own is ambiguous: it can mean "offloaded" or
// "never written". A row with no semantic cache has an empty cache_debug forever, so
// treating empty as "go fetch" would fetch nearly every row and would keep fetching
// the same row after a successful hydration recovered nothing. Consulting the
// exclusion set resolves that: if a column is configured DB-resident, empty means
// genuinely empty and there is nothing in the object to recover.
//
// The fields considered:
//
//   - token_usage: the field whose absence caused the overbill.
//   - cache_debug: absent means a semantic-cache hit could be priced at full LLM cost
//     instead of $0, so it is fetched unless configured resident.
//   - the modality payload: only the column this row's own request type bills on. A
//     chat row is not missing anything by having no speech_output.
func billingRowNeedsHydration(l *Log, excluded map[string]struct{}) bool {
	// Already fetched in this pass: whatever the object held is now on the row, and the
	// fields it did not hold will never appear no matter how often we ask.
	if l.billingPayloadsHydrated {
		return false
	}
	// Content-hidden rows are eligible for billing-only hydration even though ordinary
	// serving reads never hydrate them — but eligible is not the same as force-fetched.
	// They get their own branch rather than falling through because they are written with
	// ClearPayload, which ignores the DB-resident exclusion list entirely, so `excluded`
	// vouches for nothing on them and the checks below would misread it in both
	// directions: it would wave through an emptied-but-"resident" modality column, and it
	// would defeat the backfilled inference for a deployment that keeps token_usage
	// resident, refetching such a row on every pass to recover nothing.
	//
	// A stronger signal replaces it. prepareDBEntry restores token_usage and cache_debug
	// together on every hidden row, so a present token_usage means this row was written
	// after that change and its empty cache_debug is genuinely empty. A blank one is a
	// legacy hidden row whose pricing inputs live only in the retained object.
	if l.ContentHidden {
		if l.TokenUsage == "" {
			return true
		}
		// Modality payloads are cleared unconditionally here and are never backfilled,
		// so an empty column means the object holds the only copy.
		if col := billingPayloadColumnFor(l.Object); col != "" {
			return billingPayloadColumnValue(l, col) == ""
		}
		return false
	}

	resident := func(column string) bool {
		_, ok := excluded[column]
		return ok
	}

	// A previous recompute already fetched this row and wrote the small pricing inputs
	// back (see BulkBackfillBillingPayloads). That state is detectable without a marker
	// column: offloading blanks token_usage, so a has_object row that nonetheless has
	// one can only have been backfilled. The exception is a deployment that configured
	// token_usage to stay resident — there its presence is just config and says nothing
	// about whether anything else was recovered.
	//
	// This matters because writing an empty cache_debug back cannot speak for itself:
	// most rows have no semantic cache, so without this inference the gate would keep
	// refetching them forever and the backfill would buy nothing.
	backfilled := l.TokenUsage != "" && !resident("token_usage")

	vouched := func(column, value string) bool {
		return value != "" || resident(column) || backfilled
	}

	if !vouched("token_usage", l.TokenUsage) || !vouched("cache_debug", l.CacheDebug) {
		return true
	}
	if col := billingPayloadColumnFor(l.Object); col != "" {
		// Modality payloads are never backfilled — they are precisely what offloading
		// exists to keep out of the database — so only the exclusion set can vouch for
		// an empty column here, never a prior backfill.
		return billingPayloadColumnValue(l, col) == "" && !resident(col)
	}
	return false
}

// BulkBackfillBillingPayloads delegates to the inner store.
//
// Safe to write these columns even though the offload path blanked them: Update is a
// plain pass-through here (it does not re-run prepareDBEntry), so nothing re-strips
// them. The object keeps its own copy, so this is a cache, not a move.
func (h *HybridLogStore) BulkBackfillBillingPayloads(ctx context.Context, updates map[string]BillingPayloadBackfill) error {
	return h.inner.BulkBackfillBillingPayloads(ctx, updates)
}

// billingPayloadColumnValue reads the serialized value of one modality payload column.
func billingPayloadColumnValue(l *Log, column string) string {
	switch column {
	case "speech_output":
		return l.SpeechOutput
	case "transcription_output":
		return l.TranscriptionOutput
	case "image_generation_output":
		return l.ImageGenerationOutput
	case "video_generation_output":
		return l.VideoGenerationOutput
	case "ocr_output":
		return l.OCROutput
	default:
		return ""
	}
}

// hydrateLogForBilling fetches the offloaded payload and merges back only the
// fields pricing reads, then re-deserializes so the virtual structs match.
//
// Unlike hydrateLog it returns the error instead of degrading gracefully. Silent
// degradation is right for a rendering path — a missing message preview is
// cosmetic — but here it would leave the lossy usage stub in place and bill it,
// which is exactly the failure this method exists to prevent.
func (h *HybridLogStore) hydrateLogForBilling(ctx context.Context, log *Log) error {
	// Only the payload this row's own request type bills on is kept; the rest are
	// pruned after merge so an unrelated blob never lingers in memory.
	fields := []string{"token_usage", "cache_debug"}
	if col := billingPayloadColumnFor(log.Object); col != "" {
		fields = append(fields, col)
	}

	data, err := h.objects.Get(ctx, ObjectKey(h.prefix, log.Timestamp, log.ID))
	if err != nil {
		return fmt.Errorf("fetch payload: %w", err)
	}
	if err := MergePayloadFromJSON(log, data); err != nil {
		return fmt.Errorf("merge payload: %w", err)
	}
	pruneUnrequestedPayloadFields(log, fields)
	// MergePayloadFromJSON writes the serialized columns only. Re-deserialize so
	// TokenUsageParsed reflects the hydrated payload rather than the stub AfterFind
	// built while token_usage was still blank.
	if err := log.DeserializeFields(); err != nil {
		return fmt.Errorf("deserialize hydrated payload: %w", err)
	}
	// Recorded before the usage check below: the object was read, so re-asking would
	// return the same thing. Only a fetch that never happened should be retried.
	log.billingPayloadsHydrated = true
	// Drop the generated-image bytes the hydrated payload carries but pricing never
	// reads; it only needs the image count.
	stripNonBillingPayloadBytes(log)
	// Only token-billed rows need token_usage. A speech/ocr/video/image row prices off
	// the modality payload recovered above — input chars, pages processed, seconds,
	// image count — and computeOCRCost takes no usage argument at all. Rejecting those
	// here would report Unpriceable, which claims the inputs are unrecoverable, when
	// hydration in fact recovered everything pricing reads.
	if log.TokenUsage == "" && billingPayloadColumnFor(log.Object) == "" {
		// The object exists but carries no usage. Nothing to price from, and the
		// stub is still in place — report rather than let the caller bill it.
		return fmt.Errorf("hydrated payload has no token_usage")
	}
	return nil
}

// GetSessionLogs delegates to the inner store and returns the paginated logs
// belonging to the given session.
func (h *HybridLogStore) GetSessionLogs(ctx context.Context, sessionID string, pagination PaginationOptions) (*SessionDetailResult, error) {
	return h.inner.GetSessionLogs(ctx, sessionID, pagination)
}

// GetSessionSummary delegates to the inner store and returns an aggregated
// summary for the given session.
func (h *HybridLogStore) GetSessionSummary(ctx context.Context, sessionID string) (*SessionSummaryResult, error) {
	return h.inner.GetSessionSummary(ctx, sessionID)
}

// GetStats delegates to the inner store and returns aggregate statistics for
// the matching log rows.
func (h *HybridLogStore) GetStats(ctx context.Context, filters SearchFilters) (*SearchStats, error) {
	return h.inner.GetStats(ctx, filters)
}

// GetHistogram delegates to the inner store and returns a request-count
// histogram bucketed by bucketSizeSeconds.
func (h *HybridLogStore) GetHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*HistogramResult, error) {
	return h.inner.GetHistogram(ctx, filters, bucketSizeSeconds)
}

// GetTokenHistogram delegates to the inner store and returns a token-usage
// histogram bucketed by bucketSizeSeconds.
func (h *HybridLogStore) GetTokenHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*TokenHistogramResult, error) {
	return h.inner.GetTokenHistogram(ctx, filters, bucketSizeSeconds)
}

// GetCostHistogram delegates to the inner store and returns a cost histogram
// bucketed by bucketSizeSeconds.
func (h *HybridLogStore) GetCostHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*CostHistogramResult, error) {
	return h.inner.GetCostHistogram(ctx, filters, bucketSizeSeconds)
}

// GetModelHistogram delegates to the inner store and returns a per-model
// request histogram bucketed by bucketSizeSeconds.
func (h *HybridLogStore) GetModelHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*ModelHistogramResult, error) {
	return h.inner.GetModelHistogram(ctx, filters, bucketSizeSeconds)
}

// GetLatencyHistogram delegates to the inner store and returns a latency
// histogram bucketed by bucketSizeSeconds.
func (h *HybridLogStore) GetLatencyHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*LatencyHistogramResult, error) {
	return h.inner.GetLatencyHistogram(ctx, filters, bucketSizeSeconds)
}

// GetProviderCostHistogram delegates to the inner store and returns a
// per-provider cost histogram bucketed by bucketSizeSeconds.
func (h *HybridLogStore) GetProviderCostHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*ProviderCostHistogramResult, error) {
	return h.inner.GetProviderCostHistogram(ctx, filters, bucketSizeSeconds)
}

// GetProviderTokenHistogram delegates to the inner store and returns a
// per-provider token histogram bucketed by bucketSizeSeconds.
func (h *HybridLogStore) GetProviderTokenHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*ProviderTokenHistogramResult, error) {
	return h.inner.GetProviderTokenHistogram(ctx, filters, bucketSizeSeconds)
}

// GetProviderLatencyHistogram delegates to the inner store and returns a
// per-provider latency histogram bucketed by bucketSizeSeconds.
func (h *HybridLogStore) GetProviderLatencyHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*ProviderLatencyHistogramResult, error) {
	return h.inner.GetProviderLatencyHistogram(ctx, filters, bucketSizeSeconds)
}

// GetThroughputHistogram delegates to the inner store and returns a
// token-generation throughput (tokens/sec) histogram bucketed by bucketSizeSeconds.
func (h *HybridLogStore) GetThroughputHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*ThroughputHistogramResult, error) {
	return h.inner.GetThroughputHistogram(ctx, filters, bucketSizeSeconds)
}

// GetProviderThroughputHistogram delegates to the inner store and returns a
// per-provider throughput (tokens/sec) histogram bucketed by bucketSizeSeconds.
func (h *HybridLogStore) GetProviderThroughputHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64) (*ProviderThroughputHistogramResult, error) {
	return h.inner.GetProviderThroughputHistogram(ctx, filters, bucketSizeSeconds)
}

// GetModelRankings delegates to the inner store and returns ranked usage
// aggregates per model for the matching log rows.
func (h *HybridLogStore) GetModelRankings(ctx context.Context, filters SearchFilters) (*ModelRankingResult, error) {
	return h.inner.GetModelRankings(ctx, filters)
}

// GetUserRankings delegates to the inner store and returns ranked usage
// aggregates per user for the matching log rows.
func (h *HybridLogStore) GetUserRankings(ctx context.Context, filters SearchFilters) (*UserRankingResult, error) {
	return h.inner.GetUserRankings(ctx, filters)
}

// GetUserSpend delegates to the inner store and returns each user's total cost in
// the filter window.
func (h *HybridLogStore) GetUserSpend(ctx context.Context, filters SearchFilters) ([]UserSpendEntry, error) {
	return h.inner.GetUserSpend(ctx, filters)
}

func (h *HybridLogStore) GetDimensionRankings(ctx context.Context, filters SearchFilters, dimension RankingDimension) (*DimensionRankingResult, error) {
	return h.inner.GetDimensionRankings(ctx, filters, dimension)
}

// GetDimensionCostHistogram delegates to the inner store and returns a cost
// histogram bucketed by bucketSizeSeconds and grouped by the given dimension.
func (h *HybridLogStore) GetDimensionCostHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64, dimension HistogramDimension) (*DimensionCostHistogramResult, error) {
	return h.inner.GetDimensionCostHistogram(ctx, filters, bucketSizeSeconds, dimension)
}

// GetDimensionTokenHistogram delegates to the inner store and returns a token
// histogram bucketed by bucketSizeSeconds and grouped by the given dimension.
func (h *HybridLogStore) GetDimensionTokenHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64, dimension HistogramDimension) (*DimensionTokenHistogramResult, error) {
	return h.inner.GetDimensionTokenHistogram(ctx, filters, bucketSizeSeconds, dimension)
}

// GetDimensionLatencyHistogram delegates to the inner store and returns a
// latency histogram bucketed by bucketSizeSeconds and grouped by the given
// dimension.
func (h *HybridLogStore) GetDimensionLatencyHistogram(ctx context.Context, filters SearchFilters, bucketSizeSeconds int64, dimension HistogramDimension) (*DimensionLatencyHistogramResult, error) {
	return h.inner.GetDimensionLatencyHistogram(ctx, filters, bucketSizeSeconds, dimension)
}

// BulkUpdateCost delegates to the inner store and updates the cost columns
// (total + input/output/additional split) for each (logID -> CostUpdate) pair
// in a single round trip.
func (h *HybridLogStore) BulkUpdateCost(ctx context.Context, updates map[string]CostUpdate) error {
	return h.inner.BulkUpdateCost(ctx, updates)
}

// GetNodeUsageAfter delegates to the inner store and returns usage
// aggregates for the given node since cursor, used by the cluster gossip
// path to ship incremental usage to peers.
func (h *HybridLogStore) GetNodeUsageAfter(ctx context.Context, nodeID string, cursor NodeUsageCursor) (*NodeUsageAggregate, error) {
	return h.inner.GetNodeUsageAfter(ctx, nodeID, cursor)
}

// Flush delegates to the inner store and forces any buffered DB writes since
// the given timestamp to be persisted. Object-store uploads are independently
// queued and not affected.
func (h *HybridLogStore) Flush(ctx context.Context, since time.Time) error {
	return h.inner.Flush(ctx, since)
}

// IsLogEntryPresent delegates to the inner store and reports whether a log
// row with the given ID exists.
func (h *HybridLogStore) IsLogEntryPresent(ctx context.Context, id string) (bool, error) {
	return h.inner.IsLogEntryPresent(ctx, id)
}

// GetDistinctAliases delegates to the inner store and returns distinct alias
// values matching query, capped at limit.
func (h *HybridLogStore) GetDistinctAliases(ctx context.Context, limit int, query string) ([]string, error) {
	return h.inner.GetDistinctAliases(ctx, limit, query)
}

// GetDistinctModels delegates to the inner store and returns distinct model
// values matching query, capped at limit.
func (h *HybridLogStore) GetDistinctModels(ctx context.Context, limit int, query string) ([]string, error) {
	return h.inner.GetDistinctModels(ctx, limit, query)
}

// GetDistinctKeyPairs delegates to the inner store and returns distinct
// (idCol, nameCol) pairs matching query, capped at limit. Used to populate
// id/name filters in the UI for virtual keys, providers, etc.
func (h *HybridLogStore) GetDistinctKeyPairs(ctx context.Context, idCol, nameCol string, limit int, query string) ([]KeyPairResult, error) {
	return h.inner.GetDistinctKeyPairs(ctx, idCol, nameCol, limit, query)
}

// GetDistinctRoutingEngines delegates to the inner store and returns
// distinct routing-engine values matching query, capped at limit.
func (h *HybridLogStore) GetDistinctRoutingEngines(ctx context.Context, limit int, query string) ([]string, error) {
	return h.inner.GetDistinctRoutingEngines(ctx, limit, query)
}

// GetDistinctToolCallNames delegates to the inner store. tool_call_names is
// not a payload field, so the inner row carries it even when content is offloaded.
func (h *HybridLogStore) GetDistinctToolCallNames(ctx context.Context, limit int, query string) ([]string, error) {
	return h.inner.GetDistinctToolCallNames(ctx, limit, query)
}

// GetDistinctStopReasons delegates to the inner store and returns distinct
// stop-reason values matching query, capped at limit.
func (h *HybridLogStore) GetDistinctStopReasons(ctx context.Context, limit int, query string) ([]string, error) {
	return h.inner.GetDistinctStopReasons(ctx, limit, query)
}

// GetDistinctUserAgents delegates to the inner store and returns distinct
// raw User-Agent strings for the logs "App" filter, capped at limit.
func (h *HybridLogStore) GetDistinctUserAgents(ctx context.Context, limit int, query string) ([]string, error) {
	return h.inner.GetDistinctUserAgents(ctx, limit, query)
}

// GetDistinctApps delegates to the inner store and returns distinct backend-
// detected app labels from recent logs.
func (h *HybridLogStore) GetDistinctApps(ctx context.Context, limit int, query string) ([]string, error) {
	return h.inner.GetDistinctApps(ctx, limit, query)
}

func (h *HybridLogStore) CreateUserAgentMapping(ctx context.Context, mapping *UserAgentMapping) error {
	return h.inner.CreateUserAgentMapping(ctx, mapping)
}

func (h *HybridLogStore) UpdateUserAgentMapping(ctx context.Context, id string, mapping *UserAgentMapping) error {
	return h.inner.UpdateUserAgentMapping(ctx, id, mapping)
}

func (h *HybridLogStore) DeleteUserAgentMapping(ctx context.Context, id string) error {
	return h.inner.DeleteUserAgentMapping(ctx, id)
}

func (h *HybridLogStore) ListUserAgentMappings(ctx context.Context, activeOnly bool) ([]UserAgentMapping, error) {
	return h.inner.ListUserAgentMappings(ctx, activeOnly)
}

// GetDistinctMetadataKeys delegates to the inner store and returns distinct
// metadata keys (and their distinct values) matching query, capped at limit.
func (h *HybridLogStore) GetDistinctMetadataKeys(ctx context.Context, limit int, query string) (map[string][]string, error) {
	return h.inner.GetDistinctMetadataKeys(ctx, limit, query)
}

// MCP Tool Log analytics methods are delegated directly. Detail/write paths
// below offload full MCP log objects while keeping DB rows lightweight.

// GetMCPHistogram delegates to the inner store and returns an MCP tool-call
// count histogram bucketed by bucketSizeSeconds.
func (h *HybridLogStore) GetMCPHistogram(ctx context.Context, filters MCPToolLogSearchFilters, bucketSizeSeconds int64) (*MCPHistogramResult, error) {
	return h.inner.GetMCPHistogram(ctx, filters, bucketSizeSeconds)
}

// GetMCPCostHistogram returns the cost histogram for MCP tool logs.
func (h *HybridLogStore) GetMCPCostHistogram(ctx context.Context, filters MCPToolLogSearchFilters, bucketSizeSeconds int64) (*MCPCostHistogramResult, error) {
	return h.inner.GetMCPCostHistogram(ctx, filters, bucketSizeSeconds)
}

// GetMCPTopTools returns the top tools used in MCP tool logs.
func (h *HybridLogStore) GetMCPTopTools(ctx context.Context, filters MCPToolLogSearchFilters, limit int) (*MCPTopToolsResult, error) {
	return h.inner.GetMCPTopTools(ctx, filters, limit)
}

// applyMCPToolLogUpdate applies the updates from an entry to the target MCPToolLog entry.
func applyMCPToolLogUpdate(target *MCPToolLog, entry any) error {
	switch v := entry.(type) {
	case map[string]interface{}:
		return applyMCPToolLogUpdateMap(target, v)
	case *MCPToolLog:
		if v == nil {
			return nil
		}
		update := *v
		if err := update.SerializeFields(); err != nil {
			return err
		}
		return applyMCPToolLogUpdateStruct(target, &update)
	case MCPToolLog:
		update := v
		if err := update.SerializeFields(); err != nil {
			return err
		}
		return applyMCPToolLogUpdateStruct(target, &update)
	default:
		return nil
	}
}

// applyMCPToolLogUpdateMap applies the updates from a map to the target MCPToolLog entry.
func applyMCPToolLogUpdateMap(target *MCPToolLog, updates map[string]interface{}) error {
	for key, value := range updates {
		switch key {
		case "request_id":
			if v, ok := value.(string); ok {
				target.RequestID = v
			}
		case "llm_request_id":
			if v, ok := value.(string); ok {
				target.LLMRequestID = &v
			}
		case "tool_name":
			if v, ok := value.(string); ok {
				target.ToolName = v
			}
		case "server_label":
			if v, ok := value.(string); ok {
				target.ServerLabel = v
			}
		case "virtual_key_id":
			if v, ok := value.(string); ok {
				target.VirtualKeyID = &v
			}
		case "virtual_key_name":
			if v, ok := value.(string); ok {
				target.VirtualKeyName = &v
			}
		case "arguments":
			if v, ok := value.(string); ok {
				target.Arguments = v
				target.ArgumentsParsed = nil
			}
		case "result":
			if v, ok := value.(string); ok {
				target.Result = v
				target.ResultParsed = nil
			}
		case "error_details":
			if v, ok := value.(string); ok {
				target.ErrorDetails = v
				target.ErrorDetailsParsed = nil
			}
		case "metadata":
			if v, ok := value.(string); ok {
				target.Metadata = v
				target.MetadataParsed = nil
			}
		case "redaction_mapping":
			if v, ok := value.(string); ok {
				target.RedactionMapping = v
			}
		case "latency":
			if v, ok := numericToFloat64(value); ok {
				target.Latency = &v
			}
		case "cost":
			if v, ok := numericToFloat64(value); ok {
				target.Cost = &v
			}
		case "status":
			if v, ok := value.(string); ok {
				target.Status = v
			}
		}
	}
	return target.DeserializeFields()
}

// applyMCPToolLogUpdateStruct applies the updates from a MCPToolLog struct to the target MCPToolLog entry.
func applyMCPToolLogUpdateStruct(target *MCPToolLog, update *MCPToolLog) error {
	if update.RequestID != "" {
		target.RequestID = update.RequestID
	}
	if update.LLMRequestID != nil {
		target.LLMRequestID = update.LLMRequestID
	}
	if !update.Timestamp.IsZero() {
		target.Timestamp = update.Timestamp
	}
	if update.ToolName != "" {
		target.ToolName = update.ToolName
	}
	if update.ServerLabel != "" {
		target.ServerLabel = update.ServerLabel
	}
	if update.VirtualKeyID != nil {
		target.VirtualKeyID = update.VirtualKeyID
	}
	if update.VirtualKeyName != nil {
		target.VirtualKeyName = update.VirtualKeyName
	}
	if update.Arguments != "" {
		target.Arguments = update.Arguments
		target.ArgumentsParsed = nil
	}
	if update.Result != "" {
		target.Result = update.Result
		target.ResultParsed = nil
	}
	if update.ErrorDetails != "" {
		target.ErrorDetails = update.ErrorDetails
		target.ErrorDetailsParsed = nil
	}
	if update.Latency != nil {
		target.Latency = update.Latency
	}
	if update.Cost != nil {
		target.Cost = update.Cost
	}
	if update.Status != "" {
		target.Status = update.Status
	}
	if update.Metadata != "" {
		target.Metadata = update.Metadata
		target.MetadataParsed = nil
	}
	if update.RedactionMapping != "" {
		target.RedactionMapping = update.RedactionMapping
	}
	if !update.CreatedAt.IsZero() {
		target.CreatedAt = update.CreatedAt
	}
	return target.DeserializeFields()
}

// prepareMCPToolLogDBUpdates prepares a map of DB updates from a MCPToolLog entry.
func prepareMCPToolLogDBUpdates(entry any) (map[string]any, error) {
	switch v := entry.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(v))
		for key, value := range v {
			switch key {
			case "has_object":
				continue
			case "result", "error_details":
				out[key] = ""
			case "arguments":
				if s, ok := value.(string); ok {
					out[key] = marshalMCPPreviewString(truncateRunes(s, maxMCPToolInputPreviewRunes))
				}
			default:
				out[key] = value
			}
		}
		return out, nil
	case *MCPToolLog:
		if v == nil {
			return nil, nil
		}
		return prepareMCPToolLogDBUpdatesFromStruct(*v)
	case MCPToolLog:
		return prepareMCPToolLogDBUpdatesFromStruct(v)
	default:
		return nil, nil
	}
}

// prepareMCPToolLogDBUpdatesFromStruct prepares a map of DB updates from a MCPToolLog struct.
func prepareMCPToolLogDBUpdatesFromStruct(update MCPToolLog) (map[string]any, error) {
	if err := update.SerializeFields(); err != nil {
		return nil, err
	}
	PrepareMCPToolDBEntry(&update)
	out := make(map[string]any)
	if update.RequestID != "" {
		out["request_id"] = update.RequestID
	}
	if update.LLMRequestID != nil {
		out["llm_request_id"] = *update.LLMRequestID
	}
	if !update.Timestamp.IsZero() {
		out["timestamp"] = update.Timestamp
	}
	if update.ToolName != "" {
		out["tool_name"] = update.ToolName
	}
	if update.ServerLabel != "" {
		out["server_label"] = update.ServerLabel
	}
	if update.VirtualKeyID != nil {
		out["virtual_key_id"] = *update.VirtualKeyID
	}
	if update.VirtualKeyName != nil {
		out["virtual_key_name"] = *update.VirtualKeyName
	}
	if update.Arguments != "" {
		out["arguments"] = update.Arguments
	}
	if update.Latency != nil {
		out["latency"] = *update.Latency
	}
	if update.Cost != nil {
		out["cost"] = *update.Cost
	}
	if update.Status != "" {
		out["status"] = update.Status
	}
	if update.Metadata != "" {
		out["metadata"] = update.Metadata
	}
	if update.RedactionMapping != "" {
		out["redaction_mapping"] = update.RedactionMapping
	}
	if !update.CreatedAt.IsZero() {
		out["created_at"] = update.CreatedAt
	}
	return out, nil
}

// numericToFloat64 converts a numeric value to a float64, returning the value and a success flag.
func numericToFloat64(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case int32:
		return float64(v), true
	default:
		return 0, false
	}
}

// marshalMCPPreviewString marshals a string into a JSON string for use as an MCP preview field.
func marshalMCPPreviewString(s string) string {
	data, err := sonic.Marshal(s)
	if err != nil {
		return ""
	}
	return string(data)
}

// CreateMCPToolLog creates a new MCP tool log in the DB and enqueues it for offload to object storage.
func (h *HybridLogStore) CreateMCPToolLog(ctx context.Context, entry *MCPToolLog) error {
	payload, err := MarshalMCPToolLogPayload(entry)
	if err != nil {
		return fmt.Errorf("logstore: serialize MCP tool log before offload: %w", err)
	}
	tags := BuildMCPToolTags(entry)

	dbEntry := *entry
	PrepareMCPToolDBEntry(&dbEntry)
	if err := h.inner.CreateMCPToolLog(ctx, &dbEntry); err != nil {
		return err
	}
	h.enqueueRawUpload(entry.ID, entry.Timestamp, MCPToolObjectKey(h.prefix, entry.Timestamp, entry.ID), uploadKindMCP, entry.Status, payload, tags)
	return nil
}

// BatchCreateMCPToolLogsIfNotExists creates MCP tool logs in the DB and enqueues them for offload to object storage, if they do not already exist.
func (h *HybridLogStore) BatchCreateMCPToolLogsIfNotExists(ctx context.Context, entries []*MCPToolLog) error {
	if len(entries) == 0 {
		return nil
	}
	type pendingUpload struct {
		logID     string
		timestamp time.Time
		status    string
		payload   []byte
		tags      map[string]string
	}
	var uploads []pendingUpload

	dbEntries := make([]*MCPToolLog, 0, len(entries))
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		payload, err := MarshalMCPToolLogPayload(entry)
		if err != nil {
			return fmt.Errorf("logstore: serialize MCP tool log before offload: %w", err)
		}
		dbEntry := *entry
		PrepareMCPToolDBEntry(&dbEntry)
		dbEntries = append(dbEntries, &dbEntry)
		uploads = append(uploads, pendingUpload{
			logID:     entry.ID,
			timestamp: entry.Timestamp,
			status:    entry.Status,
			payload:   payload,
			tags:      BuildMCPToolTags(entry),
		})
	}

	if len(dbEntries) == 0 {
		return nil
	}
	if err := h.inner.BatchCreateMCPToolLogsIfNotExists(ctx, dbEntries); err != nil {
		return err
	}

	for _, u := range uploads {
		h.enqueueRawUpload(u.logID, u.timestamp, MCPToolObjectKey(h.prefix, u.timestamp, u.logID), uploadKindMCP, u.status, u.payload, u.tags)
	}
	return nil
}

// FindMCPToolLog finds an MCP tool log by its ID and hydrates it with any associated object storage data.
func (h *HybridLogStore) FindMCPToolLog(ctx context.Context, id string) (*MCPToolLog, error) {
	log, err := h.inner.FindMCPToolLog(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := h.hydrateMCPToolLog(ctx, log); err != nil {
		h.logger.Warn("%v", err)
	}
	return log, nil
}

// UpdateMCPToolLog updates an existing MCP tool log with the given ID using the provided entry data.
func (h *HybridLogStore) UpdateMCPToolLog(ctx context.Context, id string, entry any) error {
	current, err := h.inner.FindMCPToolLog(ctx, id)
	if err != nil {
		return err
	}
	if h.outbox != nil {
		lock := h.uploadLock(MCPToolObjectKey(h.prefix, current.Timestamp, current.ID))
		lock.Lock()
		defer lock.Unlock()
		current, err = h.inner.FindMCPToolLog(ctx, id)
		if err != nil {
			return err
		}
	}
	if err := h.hydrateMCPToolLogForUpdate(ctx, current); err != nil {
		return err
	}
	if err := applyMCPToolLogUpdate(current, entry); err != nil {
		return err
	}

	dbUpdates, err := prepareMCPToolLogDBUpdates(entry)
	if err != nil {
		return err
	}
	if len(dbUpdates) > 0 {
		if err := h.inner.UpdateMCPToolLog(ctx, id, dbUpdates); err != nil {
			return err
		}
	}

	payload, err := MarshalMCPToolLogPayload(current)
	if err != nil {
		return fmt.Errorf("logstore: serialize MCP tool log update before offload: %w", err)
	}
	h.enqueueRawUpload(current.ID, current.Timestamp, MCPToolObjectKey(h.prefix, current.Timestamp, current.ID), uploadKindMCP, current.Status, payload, BuildMCPToolTags(current))
	return nil
}

func (h *HybridLogStore) BatchCreateAgentLogsIfNotExists(ctx context.Context, entries []*AgentLog) ([]string, error) {
	type pendingUpload struct {
		id        string
		timestamp time.Time
		status    string
		key       string
		payload   []byte
		tags      map[string]string
	}
	dbEntries := make([]*AgentLog, 0, len(entries))
	uploads := make([]pendingUpload, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		if _, ok := seen[entry.ID]; ok {
			continue
		}
		seen[entry.ID] = struct{}{}
		dbEntry := *entry
		if AgentLogHasPayload(entry) {
			key := AgentLogObjectKey(h.prefix, entry.Timestamp, entry.ID)
			payload, err := MarshalAgentLogPayload(entry)
			if err != nil {
				return nil, fmt.Errorf("logstore: serialize A2A log before offload: %w", err)
			}
			PrepareAgentLogDBEntry(&dbEntry, key)
			uploads = append(uploads, pendingUpload{
				id: entry.ID, timestamp: entry.Timestamp, status: entry.Status,
				key: key, payload: payload, tags: BuildAgentLogTags(entry),
			})
		}
		dbEntries = append(dbEntries, &dbEntry)
	}
	if len(dbEntries) == 0 {
		return nil, nil
	}
	insertedIDs, err := h.inner.BatchCreateAgentLogsIfNotExists(ctx, dbEntries)
	if err != nil {
		return nil, err
	}
	inserted := make(map[string]struct{}, len(insertedIDs))
	for _, id := range insertedIDs {
		inserted[id] = struct{}{}
	}
	for _, upload := range uploads {
		if _, ok := inserted[upload.id]; !ok {
			continue
		}
		h.enqueueRawUpload(upload.id, upload.timestamp, upload.key, uploadKindAgent, upload.status, upload.payload, upload.tags)
	}
	return insertedIDs, nil
}

func (h *HybridLogStore) ReconcileAgentCorrelation(ctx context.Context, entries []*AgentLog) error {
	return h.inner.ReconcileAgentCorrelation(ctx, entries)
}

func (h *HybridLogStore) FindAgentLog(ctx context.Context, id string) (*AgentLog, error) {
	entry, err := h.inner.FindAgentLog(ctx, id)
	if err != nil {
		return nil, err
	}
	h.hydrateAgentLog(ctx, entry)
	return entry, nil
}

func (h *HybridLogStore) FindAgentLogsForDeletion(ctx context.Context, ids []string) ([]*AgentLog, error) {
	return h.inner.FindAgentLogsForDeletion(ctx, ids)
}

func (h *HybridLogStore) ListAgentLogHistory(ctx context.Context, filter AgentLogHistoryFilter, pagination PaginationOptions) (*AgentLogHistoryResult, error) {
	return h.inner.ListAgentLogHistory(ctx, filter, pagination)
}

// ListAgentLogOperations hydrates payload objects for the bounded operation page.
func (h *HybridLogStore) ListAgentLogOperations(ctx context.Context, filter AgentLogHistoryFilter, pagination PaginationOptions) (*AgentLogOperationResult, error) {
	result, err := h.inner.ListAgentLogOperations(ctx, filter, pagination)
	if err != nil {
		return nil, err
	}
	for i := range result.Logs {
		h.hydrateAgentLogDetail(ctx, &result.Logs[i].AgentLogDetail)
		for j := range result.Logs[i].Events {
			h.hydrateAgentLogDetail(ctx, &result.Logs[i].Events[j])
		}
	}
	return result, nil
}

// FindAgentLogOperation hydrates the request and correlated event payload objects.
func (h *HybridLogStore) FindAgentLogOperation(ctx context.Context, id string) (*AgentLogOperation, error) {
	operation, err := h.inner.FindAgentLogOperation(ctx, id)
	if err != nil {
		return nil, err
	}
	h.hydrateAgentLogDetail(ctx, &operation.AgentLogDetail)
	for i := range operation.Events {
		h.hydrateAgentLogDetail(ctx, &operation.Events[i])
	}
	return operation, nil
}

// hydrateAgentLogDetail hydrates one public detail through the existing Agent log object path.
func (h *HybridLogStore) hydrateAgentLogDetail(ctx context.Context, detail *AgentLogDetail) {
	if !detail.hasObject || detail.contentHidden {
		return
	}
	entry := &AgentLog{ID: detail.ID, Timestamp: detail.Timestamp, HasObject: detail.hasObject, ContentHidden: detail.contentHidden, PayloadReference: detail.payloadReference}
	h.hydrateAgentLog(ctx, entry)
	detail.RequestBody = entry.RequestBody
	detail.ResponseBody = entry.ResponseBody
	detail.EventBody = entry.EventBody
}

func (h *HybridLogStore) hydrateAgentLog(ctx context.Context, entry *AgentLog) {
	if entry == nil || !entry.HasObject || entry.ContentHidden {
		return
	}
	key := AgentLogObjectKey(h.prefix, entry.Timestamp, entry.ID)
	if entry.PayloadReference != nil && *entry.PayloadReference != "" {
		key = *entry.PayloadReference
	}
	data, err := h.objects.Get(ctx, key)
	if err != nil {
		h.logger.Warn("objectstore: failed to hydrate A2A log %s: %v", entry.ID, err)
		return
	}
	if err := MergeAgentLogPayloadFromJSON(entry, data); err != nil {
		h.logger.Warn("objectstore: failed to merge A2A log %s: %v", entry.ID, err)
	}
}

func (h *HybridLogStore) GetAgentLogStats(ctx context.Context, filter AgentLogHistoryFilter) (*AgentLogStats, error) {
	return h.inner.GetAgentLogStats(ctx, filter)
}

// GetAgentTopAgents delegates to the database store; the ranking uses only
// database-resident columns.
func (h *HybridLogStore) GetAgentTopAgents(ctx context.Context, filter AgentLogHistoryFilter, limit int) (*AgentTopAgentsResult, error) {
	return h.inner.GetAgentTopAgents(ctx, filter, limit)
}

func (h *HybridLogStore) GetAgentFilterData(ctx context.Context, dimensions []string, limit int, query string) (*AgentFilterData, error) {
	return h.inner.GetAgentFilterData(ctx, dimensions, limit, query)
}

func (h *HybridLogStore) GetAgentHistogram(ctx context.Context, filter AgentLogHistoryFilter, bucketSizeSeconds int64) (*AgentHistogramResult, error) {
	return h.inner.GetAgentHistogram(ctx, filter, bucketSizeSeconds)
}

func (h *HybridLogStore) hydrateAgentLogFromObject(ctx context.Context, entry *AgentLog) error {
	if entry == nil {
		return nil
	}
	key := AgentLogObjectKey(h.prefix, entry.Timestamp, entry.ID)
	if entry.PayloadReference != nil && *entry.PayloadReference != "" {
		key = *entry.PayloadReference
	}
	data, err := h.objects.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("objectstore: fetch Agent log payload for %s: %w", entry.ID, err)
	}
	if err := MergeAgentLogPayloadFromJSON(entry, data); err != nil {
		return fmt.Errorf("objectstore: merge Agent log payload for %s: %w", entry.ID, err)
	}
	return nil
}

func applyAgentLogBodyUpdate(entry *AgentLog, field string, value any) error {
	var body *string
	switch value := value.(type) {
	case nil:
	case string:
		body = &value
	case *string:
		body = value
	default:
		return fmt.Errorf("logstore: unsupported %s update type %T", field, value)
	}
	switch field {
	case "request_body":
		entry.RequestBody = body
	case "response_body":
		entry.ResponseBody = body
	case "event_body":
		entry.EventBody = body
	}
	return nil
}

// UpdateAgentLog applies agent updates. Map updates containing body fields first
// restore the full payload and fail without changing the row if hydration fails.
func (h *HybridLogStore) UpdateAgentLog(ctx context.Context, id string, entry any) error {
	updates, ok := entry.(map[string]interface{})
	if !ok {
		return h.inner.UpdateAgentLog(ctx, id, entry)
	}
	payloadUpdate := false
	for _, field := range []string{"request_body", "response_body", "event_body"} {
		if _, ok := updates[field]; ok {
			payloadUpdate = true
			break
		}
	}
	if !payloadUpdate {
		return h.inner.UpdateAgentLog(ctx, id, entry)
	}

	current, err := h.inner.FindAgentLog(ctx, id)
	if err != nil {
		return err
	}
	if h.outbox != nil {
		key := AgentLogObjectKey(h.prefix, current.Timestamp, current.ID)
		if current.PayloadReference != nil && *current.PayloadReference != "" {
			key = *current.PayloadReference
		}
		lock := h.uploadLock(key)
		lock.Lock()
		defer lock.Unlock()
		// A recovery may have completed since the first read. Re-read its
		// hydration flag while holding the same lock as the upload worker.
		current, err = h.inner.FindAgentLog(ctx, id)
		if err != nil {
			return err
		}
	}
	if err := h.hydrateAgentLogForUpdate(ctx, current); err != nil {
		return err
	}

	dbUpdates := make(map[string]interface{}, len(updates)+1)
	for field, value := range updates {
		switch field {
		case "request_body", "response_body", "event_body":
			if err := applyAgentLogBodyUpdate(current, field, value); err != nil {
				return err
			}
		default:
			dbUpdates[field] = value
		}
	}
	key := AgentLogObjectKey(h.prefix, current.Timestamp, current.ID)
	if current.PayloadReference != nil && *current.PayloadReference != "" {
		key = *current.PayloadReference
	}
	payload, err := MarshalAgentLogPayload(current)
	if err != nil {
		return fmt.Errorf("logstore: serialize Agent log update before offload: %w", err)
	}
	dbEntry := *current
	PrepareAgentLogDBEntry(&dbEntry, key)
	dbUpdates["request_body"] = dbEntry.RequestBody
	dbUpdates["response_body"] = dbEntry.ResponseBody
	dbUpdates["event_body"] = dbEntry.EventBody
	dbUpdates["payload_reference"] = dbEntry.PayloadReference
	dbUpdates["has_object"] = false
	if err := h.inner.UpdateAgentLog(ctx, id, dbUpdates); err != nil {
		return err
	}
	h.enqueueRawUpload(current.ID, current.Timestamp, key, uploadKindAgent, current.Status, payload, BuildAgentLogTags(current))
	return nil
}

func (h *HybridLogStore) FlushAgentLogs(ctx context.Context, since time.Time) error {
	// Time-based row cleanup intentionally leaves objects to the bucket lifecycle
	// policy, which must expire the A2A prefix on the same retention schedule.
	return h.inner.FlushAgentLogs(ctx, since)
}

// DeleteAgentLogs deletes the identified Agent log rows, correlated stream-event
// rows, and every offloaded object belonging to those rows.
func (h *HybridLogStore) DeleteAgentLogs(ctx context.Context, ids []string) error {
	entries, err := h.inner.FindAgentLogsForDeletion(ctx, ids)
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(entries))
	var pending []uploadWork
	for _, entry := range entries {
		if !entry.HasObject && h.outbox == nil {
			continue
		}
		key := AgentLogObjectKey(h.prefix, entry.Timestamp, entry.ID)
		if entry.PayloadReference != nil && *entry.PayloadReference != "" {
			key = *entry.PayloadReference
		}
		keys = append(keys, key)
		if h.outbox != nil {
			pending = append(pending, uploadWork{logID: entry.ID, timestamp: entry.Timestamp, key: key, kind: uploadKindAgent})
		}
	}
	if err := h.inner.DeleteAgentLogs(ctx, ids); err != nil {
		return err
	}
	if h.outbox != nil {
		h.retireDeletedUploads(ctx, pending)
		return nil
	}
	if len(keys) > 0 {
		if delErr := h.objects.DeleteBatch(ctx, keys); delErr != nil {
			h.logger.Warn("objectstore: failed to batch delete %d A2A log objects: %v", len(keys), delErr)
		}
	}
	return nil
}

// SearchMCPToolLogs searches for MCP tool logs that match the given filters and returns the results.
func (h *HybridLogStore) SearchMCPToolLogs(ctx context.Context, filters MCPToolLogSearchFilters, pagination PaginationOptions) (*MCPToolLogSearchResult, error) {
	return h.inner.SearchMCPToolLogs(ctx, filters, pagination)
}

// GetMCPToolLogStats returns statistics about MCP tool logs that match the given filters.
func (h *HybridLogStore) GetMCPToolLogStats(ctx context.Context, filters MCPToolLogSearchFilters) (*MCPToolLogStats, error) {
	return h.inner.GetMCPToolLogStats(ctx, filters)
}

// HasMCPToolLogs returns whether there are any MCP tool logs in the store.
func (h *HybridLogStore) HasMCPToolLogs(ctx context.Context) (bool, error) {
	return h.inner.HasMCPToolLogs(ctx)
}

// DeleteMCPToolLogs deletes MCP tool logs with the given IDs, including any associated object storage objects.
func (h *HybridLogStore) DeleteMCPToolLogs(ctx context.Context, ids []string) error {
	var keys []string
	var pending []uploadWork
	for _, id := range ids {
		log, findErr := h.inner.FindMCPToolLog(ctx, id)
		if findErr != nil && !errors.Is(findErr, ErrNotFound) {
			return findErr
		}
		if log != nil && log.HasObject {
			keys = append(keys, MCPToolObjectKey(h.prefix, log.Timestamp, log.ID))
		}
		if h.outbox != nil && log != nil {
			pending = append(pending, uploadWork{logID: log.ID, timestamp: log.Timestamp, key: MCPToolObjectKey(h.prefix, log.Timestamp, log.ID), kind: uploadKindMCP})
		}
	}
	if err := h.inner.DeleteMCPToolLogs(ctx, ids); err != nil {
		return err
	}
	if h.outbox != nil {
		h.retireDeletedUploads(ctx, pending)
		return nil
	}
	if len(keys) > 0 {
		if delErr := h.objects.DeleteBatch(ctx, keys); delErr != nil {
			h.logger.Warn("objectstore: failed to batch delete %d MCP tool log objects: %v", len(keys), delErr)
		}
	}
	return nil
}

// FlushMCPToolLogs flushes MCP tool logs that are older than the given time.
func (h *HybridLogStore) FlushMCPToolLogs(ctx context.Context, since time.Time) error {
	return h.inner.FlushMCPToolLogs(ctx, since)
}

// GetAvailableToolNames returns a list of tool names that match the given query.
func (h *HybridLogStore) GetAvailableToolNames(ctx context.Context, limit int, query string) ([]string, error) {
	return h.inner.GetAvailableToolNames(ctx, limit, query)
}

// GetAvailableMCPUserAgents returns distinct raw User-Agent strings from MCP tool logs.
func (h *HybridLogStore) GetAvailableMCPUserAgents(ctx context.Context, limit int, query string) ([]string, error) {
	return h.inner.GetAvailableMCPUserAgents(ctx, limit, query)
}

// GetAvailableMCPApps delegates to the inner store and returns distinct backend-
// detected app labels from MCP tool logs.
func (h *HybridLogStore) GetAvailableMCPApps(ctx context.Context, limit int, query string) ([]string, error) {
	return h.inner.GetAvailableMCPApps(ctx, limit, query)
}

// GetAvailableServerLabels returns a list of server labels that match the given query.
func (h *HybridLogStore) GetAvailableServerLabels(ctx context.Context, limit int, query string) ([]string, error) {
	return h.inner.GetAvailableServerLabels(ctx, limit, query)
}

// GetAvailableMCPVirtualKeys returns a list of MCP tool log virtual keys that match the given query.
func (h *HybridLogStore) GetAvailableMCPVirtualKeys(ctx context.Context, limit int, query string) ([]MCPToolLog, error) {
	return h.inner.GetAvailableMCPVirtualKeys(ctx, limit, query)
}

// Async Job methods — delegated directly.

// CreateAsyncJob creates a new async job.
func (h *HybridLogStore) CreateAsyncJob(ctx context.Context, job *AsyncJob) error {
	return h.inner.CreateAsyncJob(ctx, job)
}

// FindAsyncJobByID finds an async job by its ID.
func (h *HybridLogStore) FindAsyncJobByID(ctx context.Context, id string) (*AsyncJob, error) {
	return h.inner.FindAsyncJobByID(ctx, id)
}

// UpdateAsyncJob updates an async job with the given ID using the provided updates.
func (h *HybridLogStore) UpdateAsyncJob(ctx context.Context, id string, updates map[string]interface{}) error {
	return h.inner.UpdateAsyncJob(ctx, id, updates)
}

// DeleteExpiredAsyncJobs deletes async jobs that have not been updated since the given time.
func (h *HybridLogStore) DeleteExpiredAsyncJobs(ctx context.Context) (int64, error) {
	return h.inner.DeleteExpiredAsyncJobs(ctx)
}

// DeleteStaleAsyncJobs deletes async jobs that have not been updated since the given time.
func (h *HybridLogStore) DeleteStaleAsyncJobs(ctx context.Context, staleSince time.Time) (int64, error) {
	return h.inner.DeleteStaleAsyncJobs(ctx, staleSince)
}

// Webhook Delivery methods — delegated directly; delivery history rows are
// metadata-only and never carry offloadable payloads.

// CreateWebhookDelivery appends one webhook delivery attempt record.
func (h *HybridLogStore) CreateWebhookDelivery(ctx context.Context, delivery *WebhookDelivery) error {
	return h.inner.CreateWebhookDelivery(ctx, delivery)
}

// FindWebhookDeliveryByID retrieves a webhook delivery attempt by its ID.
func (h *HybridLogStore) FindWebhookDeliveryByID(ctx context.Context, id string) (*WebhookDelivery, error) {
	return h.inner.FindWebhookDeliveryByID(ctx, id)
}

// SearchWebhookDeliveries returns one page of matching delivery history.
func (h *HybridLogStore) SearchWebhookDeliveries(ctx context.Context, filters *WebhookDeliverySearchFilters, pagination PaginationOptions) (*WebhookDeliverySearchResult, error) {
	return h.inner.SearchWebhookDeliveries(ctx, filters, pagination)
}

// DeleteExpiredWebhookDeliveries deletes delivery history whose expiry has passed.
func (h *HybridLogStore) DeleteExpiredWebhookDeliveries(ctx context.Context) (int64, error) {
	return h.inner.DeleteExpiredWebhookDeliveries(ctx)
}

// Warp conversation methods - delegated directly. Transcripts are stored whole
// in the database and never offloaded: they are small, they are read as a unit
// when a thread is reopened, and an object-store round trip per message would
// make opening a saved chat slower than having asked the question again.

// ListWarpConversations returns an owner's threads, most recent first.
func (h *HybridLogStore) ListWarpConversations(ctx context.Context, ownerID string, limit int) ([]WarpConversation, error) {
	return h.inner.ListWarpConversations(ctx, ownerID, limit)
}

// GetWarpConversation returns one thread with its messages in order.
func (h *HybridLogStore) GetWarpConversation(ctx context.Context, ownerID, id string) (*WarpConversation, error) {
	return h.inner.GetWarpConversation(ctx, ownerID, id)
}

// CreateWarpConversation starts a thread.
func (h *HybridLogStore) CreateWarpConversation(ctx context.Context, conversation *WarpConversation) error {
	return h.inner.CreateWarpConversation(ctx, conversation)
}

// AppendWarpMessages adds turns to a thread and bumps its updated time.
func (h *HybridLogStore) AppendWarpMessages(ctx context.Context, ownerID, conversationID string, messages []WarpMessage) error {
	return h.inner.AppendWarpMessages(ctx, ownerID, conversationID, messages)
}

// DeleteWarpConversation removes a thread and its messages.
func (h *HybridLogStore) DeleteWarpConversation(ctx context.Context, ownerID, id string) error {
	return h.inner.DeleteWarpConversation(ctx, ownerID, id)
}

// SetDistributedLocker hands the locker to the inner store when it has use for
// one (ClickHouse); the SQL stores serialize with row locks instead.
func (h *HybridLogStore) SetDistributedLocker(locker DistributedLocker) {
	if lockable, ok := h.inner.(interface{ SetDistributedLocker(DistributedLocker) }); ok {
		lockable.SetDistributedLocker(locker)
	}
}

// DeleteWarpConversationIfEmpty removes a thread only if it has no messages.
func (h *HybridLogStore) DeleteWarpConversationIfEmpty(ctx context.Context, ownerID, id string) (bool, error) {
	return h.inner.DeleteWarpConversationIfEmpty(ctx, ownerID, id)
}

// PruneWarpConversations drops an owner's oldest threads beyond keep.
func (h *HybridLogStore) PruneWarpConversations(ctx context.Context, ownerID string, keep int) (int64, error) {
	return h.inner.PruneWarpConversations(ctx, ownerID, keep)
}

// DeleteWarpConversationsOlderThan drops threads last touched before the cutoff,
// across all owners, one bounded batch per call - callers loop until a call
// returns zero.
func (h *HybridLogStore) DeleteWarpConversationsOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	return h.inner.DeleteWarpConversationsOlderThan(ctx, cutoff)
}

// CountWarpMessages returns message counts for the given threads in one query.
func (h *HybridLogStore) CountWarpMessages(ctx context.Context, conversationIDs []string) (map[string]int, error) {
	return h.inner.CountWarpMessages(ctx, conversationIDs)
}

// SumWarpMessageUsage returns each thread's total tokens and cost in one query.
func (h *HybridLogStore) SumWarpMessageUsage(ctx context.Context, conversationIDs []string) (map[string]WarpUsageTotals, error) {
	return h.inner.SumWarpMessageUsage(ctx, conversationIDs)
}
