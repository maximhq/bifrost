package logstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/framework/objectstore"
)

const (
	uploadOutboxDirEnv      = "BIFROST_LOG_UPLOAD_OUTBOX_DIR"
	uploadOutboxMaxBytesEnv = "BIFROST_LOG_UPLOAD_OUTBOX_MAX_BYTES"
	defaultOutboxMaxBytes   = int64(10_000_000_000)
	uploadOutboxInterval    = time.Minute
)

var errUploadObsolete = errors.New("objectstore: upload log was deleted or superseded")

// uploadOutbox owns immutable, atomically replaced records in a dedicated
// directory. One record per object key keeps the newest pending payload.
// Deletion records use a separate filename and are never evicted by the budget.
// The directory must be private to this log store and survive process restarts.
type uploadOutbox struct {
	dir      string
	maxBytes int64
	mu       sync.Mutex
	sequence uint64
	active   map[string]outboxUploadState
	// Disk operations never hold an upload lock: queue overflow must reach disk
	// even while an S3 request for the same key is blocked.
	fileLocks  [64]sync.Mutex
	retryAfter string // Last dispatched path; only the sweep goroutine writes it.
}

type outboxUploadState struct {
	latest  uint64
	pending int
	work    *uploadWork // Shares the latest live job; cleared before its byte reservation is released.
}

// trackUpload retains the newest generation until all overlapping live jobs finish. Keeping
// only the disk record's version is insufficient once a newer upload succeeds
// and deletes that record: an older queued failure must still be superseded.
func (o *uploadOutbox) trackUpload(work *uploadWork) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.active == nil {
		o.active = make(map[string]outboxUploadState)
	}
	o.sequence++
	work.generation = o.sequence
	state := o.active[work.key]
	state.latest = work.generation
	state.work = work
	state.pending++
	o.active[work.key] = state
}

// finishUpload releases a completed job while retaining ordering metadata until all overlapping
// jobs finish.
func (o *uploadOutbox) finishUpload(work *uploadWork) {
	if work.generation == 0 {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	state := o.active[work.key]
	state.pending--
	if state.latest == work.generation {
		state.work = nil
	}
	if state.pending <= 0 {
		delete(o.active, work.key)
	} else {
		o.active[work.key] = state
	}
}

// superseded reports whether a live job was overtaken by a newer generation for the same key.
func (o *uploadOutbox) superseded(work *uploadWork) bool {
	if work.generation == 0 {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.active[work.key].latest > work.generation
}

type uploadOutboxRecord struct {
	Version   int               `json:"version"`
	QueuedAt  time.Time         `json:"queued_at"`
	LogID     string            `json:"log_id"`
	Timestamp time.Time         `json:"timestamp"`
	Key       string            `json:"key"`
	Kind      uploadKind        `json:"kind"`
	Status    string            `json:"status,omitempty"`
	Payload   json.RawMessage   `json:"payload"`
	Tags      map[string]string `json:"tags,omitempty"`
}

// newUploadOutboxFromEnv reads the optional outbox directory and byte budget, validates access, and
// removes incomplete writes. An unset directory disables the outbox.
func newUploadOutboxFromEnv() (*uploadOutbox, error) {
	dir := strings.TrimSpace(os.Getenv(uploadOutboxDirEnv))
	if dir == "" {
		return nil, nil
	}
	maxBytes := defaultOutboxMaxBytes
	if raw := strings.TrimSpace(os.Getenv(uploadOutboxMaxBytesEnv)); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value <= 0 {
			return nil, fmt.Errorf("%s must be a positive integer number of bytes", uploadOutboxMaxBytesEnv)
		}
		maxBytes = value
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("objectstore: resolve upload outbox directory: %w", err)
	}
	if err := os.MkdirAll(absDir, 0700); err != nil {
		return nil, fmt.Errorf("objectstore: create upload outbox directory: %w", err)
	}
	// Fail startup for an unusable configured outbox instead of silently losing
	// failed uploads. Remove incomplete writes left by a previous process.
	entries, err := os.ReadDir(absDir)
	if err != nil {
		return nil, fmt.Errorf("objectstore: read upload outbox directory: %w", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".upload-") && strings.HasSuffix(entry.Name(), ".tmp") && entry.Type().IsRegular() {
			if err := os.Remove(filepath.Join(absDir, entry.Name())); err != nil {
				return nil, fmt.Errorf("objectstore: remove incomplete outbox write: %w", err)
			}
		}
	}
	probe, err := os.CreateTemp(absDir, ".upload-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("objectstore: upload outbox directory is not writable: %w", err)
	}
	closeErr := probe.Close()
	removeErr := os.Remove(probe.Name())
	if err := errors.Join(closeErr, removeErr); err != nil {
		return nil, fmt.Errorf("objectstore: prepare upload outbox directory: %w", err)
	}
	return &uploadOutbox{dir: absDir, maxBytes: maxBytes}, nil
}

// path returns the upload record filename derived from the object key hash.
func (o *uploadOutbox) path(key string) string {
	digest := sha256.Sum256([]byte(key))
	return filepath.Join(o.dir, "upload-"+hex.EncodeToString(digest[:])+".json")
}

// deletionPath returns the cleanup record filename, separate from evictable upload files.
func (o *uploadOutbox) deletionPath(key string) string {
	digest := sha256.Sum256([]byte(key))
	return filepath.Join(o.dir, "delete-"+hex.EncodeToString(digest[:])+".json")
}

// read decodes an outbox record and validates its version, payload, and key-derived filename.
func (o *uploadOutbox) read(path string) (*uploadWork, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var record uploadOutboxRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("decode outbox record: %w", err)
	}
	deleteOnly := path == o.deletionPath(record.Key)
	if record.Version != 1 || record.Kind > uploadKindAgent || record.LogID == "" || record.QueuedAt.IsZero() || record.Timestamp.IsZero() ||
		len(record.Payload) == 0 || !json.Valid(record.Payload) || (!deleteOnly && path != o.path(record.Key)) ||
		(deleteOnly && (string(record.Payload) != "null" || len(record.Tags) != 0 || record.Status != "")) {
		return nil, fmt.Errorf("invalid upload outbox record %s", filepath.Base(path))
	}
	return &uploadWork{queuedAt: record.QueuedAt, logID: record.LogID, timestamp: record.Timestamp,
		key: record.Key, kind: record.Kind, status: record.Status, payload: record.Payload, tags: record.Tags, deleteOnly: deleteOnly}, nil
}

// save never exposes a partial JSON record, and fsyncs before publication.
func (o *uploadOutbox) save(work *uploadWork) error {
	lock := o.fileLock(o.path(work.key))
	lock.Lock()
	defer lock.Unlock()
	return o.saveLocked(work)
}

// saveLocked atomically persists the newest record. The caller must hold its file lock; deletion
// records are synced before pending content is removed.
func (o *uploadOutbox) saveLocked(work *uploadWork) error {
	if !work.deleteOnly && o.superseded(work) {
		return nil
	}
	if work.queuedAt.IsZero() {
		work.queuedAt = time.Now().UTC()
	}
	path := o.path(work.key)
	if work.deleteOnly {
		path = o.deletionPath(work.key)
	} else if _, err := os.Stat(o.deletionPath(work.key)); err == nil {
		return nil // A deleted log must never regain pending request content.
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	current, err := o.read(path)
	if err == nil && current.queuedAt.After(work.queuedAt) {
		return nil // A newer failed upload already owns this key.
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	record := uploadOutboxRecord{
		Version: 1, QueuedAt: work.queuedAt, LogID: work.logID, Timestamp: work.timestamp,
		Key: work.key, Kind: work.kind, Status: work.status, Payload: work.payload, Tags: work.tags,
	}
	if work.deleteOnly {
		record.Payload, record.Tags, record.Status = json.RawMessage(`null`), nil, ""
	}
	data, err := providerUtils.MarshalSorted(record)
	if err != nil {
		return fmt.Errorf("encode upload outbox record: %w", err)
	}
	file, err := os.CreateTemp(o.dir, ".upload-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(o.dir)
	if err != nil {
		return err
	}
	defer dir.Close()
	if work.deleteOnly {
		// Persist cleanup before unlinking the payload. A crash between these
		// operations may leave both files, but cannot lose the deletion record.
		if err := dir.Sync(); err != nil {
			return err
		}
		if err := os.Remove(o.path(work.key)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return dir.Sync()
}

// removeThrough never removes a newer pending version of the same object.
func (o *uploadOutbox) removeThrough(work *uploadWork) error {
	path := o.path(work.key)
	if work.deleteOnly {
		path = o.deletionPath(work.key)
	}
	lock := o.fileLock(path)
	lock.Lock()
	defer lock.Unlock()
	current, err := o.read(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.queuedAt.After(work.queuedAt) {
		return nil
	}
	return os.Remove(path)
}

// preserveFailedUpload persists failed work when enabled and counts persistence
// failures as dropped uploads. Missing rows are saved as cleanup identifiers.
func (h *HybridLogStore) preserveFailedUpload(work *uploadWork) {
	if h.outbox == nil {
		h.droppedUploads.Add(1)
		return
	}
	lock := h.outbox.fileLock(h.outbox.path(work.key))
	lock.Lock()
	defer lock.Unlock()
	if h.outbox.superseded(work) {
		return
	}
	// Serialize this check with deletion's disk cleanup. An in-flight failure
	// must not restore request content after its row has been explicitly deleted.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	present, err := h.uploadLogPresent(ctx, work)
	if err != nil {
		h.logger.Warn("objectstore: failed to check log %s before saving outbox: %v", work.logID, err)
	} else if !present {
		copy := *work
		copy.deleteOnly = true
		work = &copy
	}
	if err := h.outbox.saveLocked(work); err == nil {
		h.logger.Warn("objectstore: saved log %s to disk outbox for retry", work.logID)
		return
	} else {
		h.logger.Error("objectstore: failed to save log %s to disk outbox: %v", work.logID, err)
	}
	h.droppedUploads.Add(1)
}

// uploadLogPresent checks the underlying store for the regular, MCP, or agent log referenced by an
// upload.
func (h *HybridLogStore) uploadLogPresent(ctx context.Context, work *uploadWork) (bool, error) {
	var err error
	switch work.kind {
	case uploadKindMCP:
		_, err = h.inner.FindMCPToolLog(ctx, work.logID)
	case uploadKindAgent:
		_, err = h.inner.FindAgentLog(ctx, work.logID)
	default:
		return h.inner.IsLogEntryPresent(ctx, work.logID)
	}
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// retireDeletedUploads replaces pending content with cleanup identifiers and
// attempts remote deletion. Failures are logged; saved cleanup records remain
// until deletion succeeds.
func (h *HybridLogStore) retireDeletedUploads(ctx context.Context, works []uploadWork) {
	if len(works) == 0 {
		return
	}
	keys := make([]string, 0, len(works))
	for i := range works {
		work := &works[i]
		work.queuedAt = time.Now().UTC()
		work.deleteOnly = true
		if err := h.outbox.save(work); err != nil {
			h.logger.Error("objectstore: failed to retire pending log %s: %v", work.logID, err)
		}
		keys = append(keys, work.key)
	}
	var err error
	if len(keys) == 1 {
		err = h.objects.Delete(ctx, keys[0])
	} else {
		err = h.objects.DeleteBatch(ctx, keys)
	}
	if err != nil {
		h.logger.Warn("objectstore: failed to delete %d objects; cleanup retained in outbox: %v", len(keys), err)
		return
	}
	for i := range works {
		if err := h.outbox.removeThrough(&works[i]); err != nil {
			h.logger.Warn("objectstore: failed to remove deleted log %s from outbox: %v", works[i].logID, err)
		}
	}
}

// runUploadOutbox schedules retry sweeps each minute until its context is canceled.
func (h *HybridLogStore) runUploadOutbox(ctx context.Context) {
	defer h.wg.Done()
	ticker := time.NewTicker(uploadOutboxInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cycleCtx, cancel := context.WithTimeout(ctx, uploadOutboxInterval)
			h.sweepUploadOutbox(cycleCtx)
			cancel()
		}
	}
}

type uploadOutboxFile struct {
	path       string
	info       os.FileInfo
	deleteOnly bool
}

// files lists upload and deletion records in eviction order, counting only upload files against the
// byte budget.
func (o *uploadOutbox) files() ([]uploadOutboxFile, int64, error) {
	entries, err := os.ReadDir(o.dir)
	if err != nil {
		return nil, 0, err
	}
	var files []uploadOutboxFile
	var size int64
	for _, entry := range entries {
		name := entry.Name()
		deleteOnly := strings.HasPrefix(name, "delete-")
		if (!strings.HasPrefix(name, "upload-") && !deleteOnly) || !strings.HasSuffix(name, ".json") {
			continue
		}
		info, err := entry.Info()
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, 0, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		files = append(files, uploadOutboxFile{path: filepath.Join(o.dir, name), info: info, deleteOnly: deleteOnly})
		if !deleteOnly {
			size += info.Size()
		}
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].info.ModTime().Equal(files[j].info.ModTime()) {
			return files[i].path < files[j].path
		}
		return files[i].info.ModTime().Before(files[j].info.ModTime())
	})
	return files, size, nil
}

// sweepUploadOutbox first enforces the payload budget, then retries uploads and
// deletions with bounded concurrency. Cleanup identifiers survive quota eviction.
func (h *HybridLogStore) sweepUploadOutbox(ctx context.Context) {
	files, size, err := h.outbox.files()
	if err != nil {
		h.logger.Warn("objectstore: failed to scan upload outbox: %v", err)
		return
	}
	for _, file := range files {
		if size <= h.outbox.maxBytes || ctx.Err() != nil {
			break
		}
		if file.deleteOnly {
			continue
		}
		// Derive the same lock from the filename even if its contents are corrupt.
		lock := h.outbox.fileLock(file.path)
		lock.Lock()
		current, statErr := os.Stat(file.path)
		if statErr == nil && current.ModTime().Equal(file.info.ModTime()) && current.Size() == file.info.Size() {
			if err := os.Remove(file.path); err == nil {
				size -= file.info.Size()
				h.droppedUploads.Add(1)
				h.logger.Warn("objectstore: upload outbox exceeds %d bytes, evicted oldest record %s", h.outbox.maxBytes, filepath.Base(file.path))
			} else {
				h.logger.Warn("objectstore: failed to evict outbox record: %v", err)
			}
		} else if errors.Is(statErr, os.ErrNotExist) {
			size -= file.info.Size()
		} else {
			// A live upload replaced this record. Recheck the budget next minute
			// rather than deleting a newer file using an outdated size/age snapshot.
			lock.Unlock()
			break
		}
		lock.Unlock()
	}
	jobs := make(chan string)
	var workers sync.WaitGroup
	for range defaultUploadWorkers {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for path := range jobs {
				if ctx.Err() == nil {
					h.retryOutboxFile(ctx, path)
				}
			}
		}()
	}
	// Retry order is independent of eviction age. Resume after the previous
	// cycle so a full batch of slow failures cannot starve the rest of the disk.
	sort.Slice(files, func(i, j int) bool { return files[i].path < files[j].path })
	start := sort.Search(len(files), func(i int) bool { return files[i].path > h.outbox.retryAfter })
	for i := range files {
		file := files[(start+i)%len(files)]
		if ctx.Err() != nil {
			break
		}
		select {
		case jobs <- file.path:
			h.outbox.retryAfter = file.path
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return
		}
	}
	close(jobs)
	workers.Wait()
}

// retryOutboxFile retries a record under its object lock and a shared memory reservation. Failed
// work remains on disk.
func (h *HybridLogStore) retryOutboxFile(ctx context.Context, path string) {
	defer func() {
		if recovered := recover(); recovered != nil {
			h.logger.Error("objectstore: panic retrying outbox record %s (retained): %v", filepath.Base(path), recovered)
		}
	}()
	if ctx.Err() != nil {
		return
	}
	lock := h.outboxFileLock(path)
	lock.Lock()
	defer lock.Unlock()
	if ctx.Err() != nil {
		return
	}
	// Keep the size reservation and the read on the same immutable version.
	fileLock := h.outbox.fileLock(path)
	fileLock.Lock()
	info, err := os.Stat(path)
	if err != nil {
		fileLock.Unlock()
		if !errors.Is(err, os.ErrNotExist) {
			h.logger.Warn("objectstore: failed to stat upload outbox record: %v", err)
		}
		return
	}
	// Reserve before reading the payload so disk retries share the live queue's
	// memory budget. A record whose JSON envelope pushes it over the payload
	// limit runs alone, with the entire budget reserved.
	reserved := min(info.Size(), int64(defaultMaxUploadQueueBytes))
	if !h.reserveUploadBytes(reserved) {
		fileLock.Unlock()
		return
	}
	defer h.pendingBytes.Add(-reserved)
	work, err := h.outbox.read(path)
	fileLock.Unlock()
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			h.logger.Warn("objectstore: failed to read upload outbox record: %v", err)
		}
		return
	}
	key := ObjectKey(h.prefix, work.timestamp, work.logID)
	switch work.kind {
	case uploadKindMCP:
		key = MCPToolObjectKey(h.prefix, work.timestamp, work.logID)
	case uploadKindAgent:
		key = AgentLogObjectKey(h.prefix, work.timestamp, work.logID)
	}
	if work.key != key {
		h.logger.Warn("objectstore: outbox record for log %s belongs to a different object prefix", work.logID)
		return
	}
	if err := h.uploadAndMark(ctx, work); errors.Is(err, errUploadObsolete) {
		h.droppedUploads.Add(1)
	} else if err != nil {
		h.logger.Warn("objectstore: outbox retry failed for log %s: %v", work.logID, err)
		return
	}
	if err := h.outbox.removeThrough(work); err != nil {
		h.logger.Warn("objectstore: failed to remove uploaded outbox record for log %s: %v", work.logID, err)
		return
	}
	h.logger.Info("objectstore: completed outbox upload for log %s", work.logID)
}

// outboxFileLock maps a record filename to the network lock for its object key.
func (h *HybridLogStore) outboxFileLock(path string) *sync.Mutex {
	return uploadFileLock(path, &h.uploadLocks)
}

// fileLock maps upload and deletion filenames to the same disk mutation lock.
func (o *uploadOutbox) fileLock(path string) *sync.Mutex {
	return uploadFileLock(path, &o.fileLocks)
}

// uploadFileLock selects a lock from the filename digest, falling back to the path hash for
// malformed names.
func uploadFileLock(path string, locks *[64]sync.Mutex) *sync.Mutex {
	name := filepath.Base(path)
	// Upload and deletion filenames share a digest and must use the same lock.
	if (strings.HasPrefix(name, "upload-") || strings.HasPrefix(name, "delete-")) && len(name) == len("upload-")+64+len(".json") {
		if digest, err := hex.DecodeString(name[len("upload-") : len("upload-")+64]); err == nil {
			return &locks[int(digest[0])%len(locks)]
		}
	}
	digest := sha256.Sum256([]byte(path))
	return &locks[int(digest[0])%len(locks)]
}

// pendingUpload is called with the upload lock held, so a live worker cannot
// mutate its payload while an update reads it. Queued jobs may be newer than
// the disk record, or may not have attempted their first upload yet.
func (o *uploadOutbox) pendingUpload(key string) (*uploadWork, error) {
	pending, err := o.read(o.path(key))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	o.mu.Lock()
	queued := o.active[key].work
	o.mu.Unlock()
	if queued != nil && (pending == nil || !queued.queuedAt.Before(pending.queuedAt)) {
		return queued, nil
	}
	return pending, err
}

// hydrateMCPToolLogForUpdate restores full pending or stored tool payloads before
// merging an update. With an outbox enabled, the caller must hold the upload lock.
// A missing remote object leaves the available database fields intact.
func (h *HybridLogStore) hydrateMCPToolLogForUpdate(ctx context.Context, log *MCPToolLog) error {
	if h.outbox != nil {
		key := MCPToolObjectKey(h.prefix, log.Timestamp, log.ID)
		pending, err := h.outbox.pendingUpload(key)
		if err == nil {
			return MergeMCPToolLogPayloadFromJSON(log, pending.payload)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("objectstore: read pending MCP tool log %s: %w", log.ID, err)
		}
	}
	err := h.hydrateMCPToolLogFromObject(ctx, log)
	if objectstore.IsNotFound(err) {
		return nil
	}
	return err
}

// hydrateAgentLogForUpdate restores the full pending or stored agent payload.
// With an outbox enabled, the caller must hold the upload lock. Missing referenced
// payloads fail hydration.
func (h *HybridLogStore) hydrateAgentLogForUpdate(ctx context.Context, log *AgentLog) error {
	if h.outbox != nil {
		key := AgentLogObjectKey(h.prefix, log.Timestamp, log.ID)
		if log.PayloadReference != nil && *log.PayloadReference != "" {
			key = *log.PayloadReference
		}
		pending, err := h.outbox.pendingUpload(key)
		if err == nil {
			return MergeAgentLogPayloadFromJSON(log, pending.payload)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("objectstore: read pending agent log %s: %w", log.ID, err)
		}
	}
	if log.HasObject || (h.outbox != nil && log.PayloadReference != nil && *log.PayloadReference != "") {
		// A referenced payload may still be queued or have failed its DB marker.
		// If neither disk nor object storage has it, fail the update rather than
		// publishing a truncated preview over the full pending request.
		return h.hydrateAgentLogFromObject(ctx, log)
	}
	return nil
}
