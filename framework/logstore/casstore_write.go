package logstore

// CAS write path. Every operation runs the log-row write and the CAS writes
// inside ONE database transaction:
//
//   - Create: row insert first; a payload failure rolls the row back.
//   - CreateIfNotExists/BatchCreateIfNotExists: row insert with ON CONFLICT DO
//     NOTHING first; a conflict returns early without touching CAS, restoring
//     the inner store's idempotency (duplicate creates no longer overwrite an
//     existing log's payload).
//   - Update (column map, *Log, or Log value): pointers are written/replaced,
//     downgraded fields' pointers removed, replaced manifests reclaimed, then
//     the row update runs — including has_object, which is recomputed from the
//     surviving pointer count instead of being left stale.
//   - Deletes reclaim blobs via reachability; the full sweep computes
//     reachability from live log roots so a dead manifest's ref edges cannot
//     keep its segments (and itself) alive forever.

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// casSplitPayload partitions an extracted payload map into the fields that go
// to CAS (cleared) and their contents. Map iteration order is irrelevant: each
// field is stored independently.
func (c *CasLogStore) casSplitPayload(payload map[string]string) (map[string]struct{}, []casFieldContent) {
	cleared := make(map[string]struct{})
	var toStore []casFieldContent
	for field, content := range payload {
		if c.casEligible(field, content) {
			cleared[field] = struct{}{}
			toStore = append(toStore, casFieldContent{field, content})
		}
	}
	return cleared, toStore
}

type casFieldContent struct {
	field   string
	content string
}

// casWriteFields stores every eligible field inside tx and reclaims the
// manifests any replaced pointers previously referenced.
func (c *CasLogStore) casWriteFields(tx *gorm.DB, logID string, toStore []casFieldContent) error {
	var oldManifests []string
	for _, s := range toStore {
		old, fallback, err := casStoreField(tx, logID, s.field, []byte(s.content), c.minChunkBytes)
		if err != nil {
			return fmt.Errorf("logstore/cas: store payload for log %s: %w", logID, err)
		}
		if fallback {
			c.fallbacks.Add(1)
		}
		if old != "" {
			oldManifests = append(oldManifests, old)
		}
	}
	return gcForManifests(tx, oldManifests)
}

// prepareCasEntry mutates dbEntry into the lightweight row form. Fields that
// went to CAS are NOT in the keep set, so prepareDBEntry clears both their
// TEXT and Parsed fields and writes the last-user-message preview into the
// input columns — the same list-view semantics as hybrid mode. The full
// payload lives in CAS and overwrites the preview on hydrate. Config-excluded
// fields (plus token_usage/cache_debug) stay in the keep set, DB-resident.
func prepareCasEntry(dbEntry *Log, excluded map[string]struct{}, cleared map[string]struct{}) {
	keep := make(map[string]struct{}, len(excluded)+len(cleared))
	for f := range excluded {
		keep[f] = struct{}{}
	}
	for _, f := range payloadFields {
		if _, wasCleared := cleared[f]; !wasCleared {
			keep[f] = struct{}{}
		}
	}
	prepareDBEntry(dbEntry, keep)
}

// extractCasPayload mirrors hybrid's extractUploadPayload: hidden entries
// always CAS the complete payload (the row must retain no content), other
// entries respect the exclusion list.
func (c *CasLogStore) extractCasPayload(entry *Log) map[string]string {
	if entry.ContentHidden {
		return ExtractPayload(entry)
	}
	return ExtractPayloadFiltered(entry, c.excluded)
}

// createRow inserts dbEntry inside tx, replicating the inner store's Create
// (GORM model hooks, postgres inc_number omission).
func (c *CasLogStore) createRow(tx *gorm.DB, dbEntry *Log, ifNotExists bool) (inserted bool, err error) {
	db := tx
	if c.db.Dialector.Name() == "postgres" {
		db = db.Omit("inc_number")
	}
	if ifNotExists {
		db = db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoNothing: true,
		})
	}
	res := db.Create(dbEntry)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

func (c *CasLogStore) Create(ctx context.Context, entry *Log) error {
	return c.createEntry(ctx, entry, false)
}

func (c *CasLogStore) CreateIfNotExists(ctx context.Context, entry *Log) error {
	return c.createEntry(ctx, entry, true)
}

func (c *CasLogStore) BatchCreateIfNotExists(ctx context.Context, entries []*Log) error {
	if len(entries) == 0 {
		return nil
	}
	// Prepare everything up front so a serialization failure aborts before
	// any transaction work, mirroring the inner store's fail-fast batch.
	var batch []casPreparedCreate
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		p, err := c.prepareCreate(entry)
		if err != nil {
			return err
		}
		batch = append(batch, *p)
	}
	if len(batch) == 0 {
		return nil
	}
	// One transaction for the whole batch, preserving the inner store's
	// all-or-nothing batch semantics.
	return c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for i := range batch {
			p := &batch[i]
			inserted, err := c.createRow(tx, &p.dbEntry, true)
			if err != nil {
				return err
			}
			if !inserted {
				continue // row already exists: leave it and its payload alone
			}
			if err := c.casWriteFields(tx, p.entry.ID, p.toStore); err != nil {
				return err
			}
			p.entry.ContentSummary = p.dbEntry.ContentSummary
		}
		return nil
	})
}

// casPreparedCreate is a serialized entry plus its lightweight row form and
// the payload fields bound for CAS.
type casPreparedCreate struct {
	entry   *Log
	dbEntry Log
	toStore []casFieldContent
}

// prepareCreate serializes the entry and derives its lightweight row form.
func (c *CasLogStore) prepareCreate(entry *Log) (*casPreparedCreate, error) {
	if entry == nil {
		return nil, fmt.Errorf("log entry is nil")
	}
	if err := entry.SerializeFields(); err != nil {
		return nil, fmt.Errorf("logstore/cas: serialize before store: %w", err)
	}
	payload := c.extractCasPayload(entry)
	cleared, toStore := c.casSplitPayload(payload)
	dbEntry := *entry
	prepareCasEntry(&dbEntry, c.excluded, cleared)
	dbEntry.HasObject = len(cleared) > 0
	return &casPreparedCreate{entry: entry, dbEntry: dbEntry, toStore: toStore}, nil
}

// createEntry inserts the row and stores its payload in one transaction. The
// row insert comes first: on payload failure the row rolls back, and a
// CreateIfNotExists conflict returns before any CAS write, so duplicate
// creates are side-effect free.
func (c *CasLogStore) createEntry(ctx context.Context, entry *Log, ifNotExists bool) error {
	p, err := c.prepareCreate(entry)
	if err != nil {
		return err
	}
	return c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		inserted, err := c.createRow(tx, &p.dbEntry, ifNotExists)
		if err != nil {
			return err
		}
		if !inserted {
			return nil
		}
		if err := c.casWriteFields(tx, p.entry.ID, p.toStore); err != nil {
			return err
		}
		p.entry.ContentSummary = p.dbEntry.ContentSummary
		return nil
	})
}

// Update intercepts the three Log entry shapes the rest of the framework uses
// and delegates anything else unchanged:
//
//   - map[string]interface{} with DB column keys ("output_message", ...): large
//     string values of payload fields go to CAS and the row column is emptied;
//     a payload field set to a small/empty string DROPS its CAS pointer (the
//     row value is authoritative again); everything else stays a row update.
//   - *Log: payload fields are extracted, large ones CAS'd; present-but-small
//     fields drop their pointers; the struct passes on with the CAS'd fields
//     cleared (gorm Updates omits them) and a follow-up map write empties
//     their columns and syncs has_object.
//   - Log (value form): the inner store accepts it, so it is intercepted too —
//     otherwise its payload fields would bypass CAS entirely.
func (c *CasLogStore) Update(ctx context.Context, id string, entry any) error {
	switch v := entry.(type) {
	case map[string]interface{}:
		return c.updateFromMap(ctx, id, v)
	case *Log:
		return c.updateFromStruct(ctx, id, v)
	case Log:
		copyEntry := v
		return c.updateFromStruct(ctx, id, &copyEntry)
	default:
		return c.LogStore.Update(ctx, id, entry)
	}
}

func (c *CasLogStore) updateFromMap(ctx context.Context, id string, updates map[string]interface{}) error {
	rowUpdates := make(map[string]interface{}, len(updates))
	var toStore []casFieldContent
	var downgrades []string // payload fields set to small/empty values: drop pointers
	for k, val := range updates {
		s, isString := val.(string)
		if _, isPayload := payloadFieldSet[k]; isString && isPayload && !c.isExcludedPayload(k) {
			if c.casEligible(k, s) {
				toStore = append(toStore, casFieldContent{k, s})
				rowUpdates[k] = "" // empty the row column; CAS is now authoritative
			} else {
				// Explicitly set to a small or empty value: the row column
				// (written below) is the new truth; any stale pointer must go.
				downgrades = append(downgrades, k)
				rowUpdates[k] = s
			}
			continue
		}
		rowUpdates[k] = val
	}
	touchedCAS := len(toStore) > 0 || len(downgrades) > 0
	if len(rowUpdates) == 0 && !touchedCAS {
		// gorm Updates with an empty map is a no-op that reports 0 rows
		// affected, which RDBLogStore turns into ErrNotFound. Keep that
		// semantics observable with a self-assigning expression.
		rowUpdates["has_object"] = gorm.Expr("has_object")
	}
	return c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := c.casWriteFields(tx, id, toStore); err != nil {
			return err
		}
		if err := c.dropFields(tx, id, downgrades); err != nil {
			return err
		}
		if touchedCAS {
			n, err := casCountPayloads(tx, id)
			if err != nil {
				return err
			}
			rowUpdates["has_object"] = n > 0
		}
		res := tx.Model(&Log{}).Where("id = ?", id).Updates(rowUpdates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (c *CasLogStore) updateFromStruct(ctx context.Context, id string, lg *Log) error {
	if lg == nil {
		return fmt.Errorf("log entry is nil")
	}
	if err := lg.SerializeFields(); err != nil {
		return fmt.Errorf("logstore/cas: serialize before update: %w", err)
	}
	payload := ExtractPayloadFiltered(lg, c.excluded)
	var toStore []casFieldContent
	var downgrades []string
	keep := make(map[string]struct{}, len(c.excluded)+len(payloadFields))
	for f := range c.excluded {
		keep[f] = struct{}{}
	}
	for _, f := range payloadFields {
		content, has := payload[f]
		if !has || content == "" {
			// Absent or empty: gorm struct updates omit zero fields, so an
			// empty string means "not being updated" (same as the inner
			// store) — the field and any existing pointer stay untouched.
			keep[f] = struct{}{}
			continue
		}
		if !c.casEligible(f, content) {
			keep[f] = struct{}{}
			// Explicitly set to a small value: the row value (written by the
			// struct update below) is authoritative; drop any stale pointer
			// so hydration cannot resurrect the old content.
			downgrades = append(downgrades, f)
			continue
		}
		toStore = append(toStore, casFieldContent{f, content})
	}
	dbEntry := *lg
	ClearPayloadFiltered(&dbEntry, keep)
	touchedCAS := len(toStore) > 0 || len(downgrades) > 0
	return c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := c.casWriteFields(tx, id, toStore); err != nil {
			return err
		}
		if err := c.dropFields(tx, id, downgrades); err != nil {
			return err
		}
		// Struct updates: gorm omits zero fields, so the CAS-cleared columns
		// are emptied explicitly, and has_object is synced when pointers
		// changed.
		extra := make(map[string]interface{}, len(toStore)+1)
		for _, s := range toStore {
			extra[s.field] = ""
		}
		if touchedCAS {
			n, err := casCountPayloads(tx, id)
			if err != nil {
				return err
			}
			extra["has_object"] = n > 0
		}
		res := tx.Model(&Log{}).Where("id = ?", id).Updates(&dbEntry)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			// An all-zero struct (only CAS'd payload fields were set) gives
			// gorm nothing to write; distinguish that from a missing row.
			var n int64
			if err := tx.Model(&Log{}).Where("id = ?", id).Count(&n).Error; err != nil {
				return err
			}
			if n == 0 {
				return ErrNotFound
			}
		}
		if len(extra) > 0 {
			if err := tx.Model(&Log{}).Where("id = ?", id).Updates(extra).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (c *CasLogStore) isExcludedPayload(field string) bool {
	if _, isPayload := payloadFieldSet[field]; !isPayload {
		return false
	}
	_, excl := c.excluded[field]
	return excl
}

// dropFields removes the CAS pointers for fields that were downgraded to
// row-resident values and reclaims their manifests.
func (c *CasLogStore) dropFields(tx *gorm.DB, logID string, fields []string) error {
	if len(fields) == 0 {
		return nil
	}
	var manifests []string
	if err := tx.Model(&casPayload{}).
		Where("log_id = ? AND field IN ?", logID, fields).
		Pluck("blob_hash", &manifests).Error; err != nil {
		return err
	}
	if err := tx.Where("log_id = ? AND field IN ?", logID, fields).Delete(&casPayload{}).Error; err != nil {
		return err
	}
	return gcForManifests(tx, manifests)
}

// gcForManifests reclaims blobs reachable only through the given manifests.
// Identical field content maps to an identical manifest hash, which can be
// shared by several logs; only manifests no surviving pointer references may
// be reclaimed. Shared content is preserved by the NOT EXISTS guards.
func gcForManifests(tx *gorm.DB, manifestHashes []string) error {
	if len(manifestHashes) == 0 {
		return nil
	}
	var stillReferenced []string
	if err := tx.Model(&casPayload{}).
		Where("blob_hash IN ?", manifestHashes).
		Pluck("blob_hash", &stillReferenced).Error; err != nil {
		return err
	}
	if len(stillReferenced) > 0 {
		keep := make(map[string]struct{}, len(stillReferenced))
		for _, h := range stillReferenced {
			keep[h] = struct{}{}
		}
		filtered := manifestHashes[:0]
		for _, h := range manifestHashes {
			if _, alive := keep[h]; !alive {
				filtered = append(filtered, h)
			}
		}
		manifestHashes = filtered
		if len(manifestHashes) == 0 {
			return nil
		}
	}
	var targets []string
	if err := tx.Model(&casRef{}).
		Where("owner_hash IN ?", manifestHashes).
		Pluck("target_hash", &targets).Error; err != nil {
		return err
	}
	if err := tx.Where("owner_hash IN ?", manifestHashes).Delete(&casRef{}).Error; err != nil {
		return err
	}
	candidates := append(append([]string{}, manifestHashes...), targets...)
	return tx.Where(
		"hash IN ? AND NOT EXISTS (SELECT 1 FROM cas_payloads WHERE cas_payloads.blob_hash = cas_blobs.hash)"+
			" AND NOT EXISTS (SELECT 1 FROM cas_refs WHERE cas_refs.target_hash = cas_blobs.hash)"+
			" AND NOT EXISTS (SELECT 1 FROM cas_refs WHERE cas_refs.owner_hash = cas_blobs.hash)",
		candidates,
	).Delete(&casBlob{}).Error
}

// gcOrphanCas is the full reachability sweep computed from live log roots:
//
//  1. pointers to missing log rows are dropped (their manifests die with them);
//  2. refs owned by manifests no pointer references are deleted — this is what
//     breaks the mutual keep-alive where a dead manifest and its segments
//     vouched for each other forever;
//  3. blobs that are neither live manifests nor referenced by a surviving ref
//     are deleted.
//
// Run from batch deletions and Flush; it scans the CAS tables.
func (c *CasLogStore) gcOrphanCas(ctx context.Context) error {
	return c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where(
			"NOT EXISTS (SELECT 1 FROM logs WHERE logs.id = cas_payloads.log_id)",
		).Delete(&casPayload{}).Error; err != nil {
			return err
		}
		if err := tx.Where(
			"NOT EXISTS (SELECT 1 FROM cas_payloads WHERE cas_payloads.blob_hash = cas_refs.owner_hash)",
		).Delete(&casRef{}).Error; err != nil {
			return err
		}
		return tx.Where(
			"NOT EXISTS (SELECT 1 FROM cas_payloads WHERE cas_payloads.blob_hash = cas_blobs.hash)"+
				" AND NOT EXISTS (SELECT 1 FROM cas_refs WHERE cas_refs.target_hash = cas_blobs.hash)",
		).Delete(&casBlob{}).Error
	})
}

func (c *CasLogStore) DeleteLog(ctx context.Context, id string) error {
	if err := c.LogStore.DeleteLog(ctx, id); err != nil {
		return err
	}
	return c.deleteCasForLogs(ctx, []string{id})
}

func (c *CasLogStore) DeleteLogs(ctx context.Context, ids []string) error {
	if err := c.LogStore.DeleteLogs(ctx, ids); err != nil {
		return err
	}
	return c.deleteCasForLogs(ctx, ids)
}

func (c *CasLogStore) DeleteLogsBatch(ctx context.Context, cutoff time.Time, batchSize int) (int64, error) {
	n, err := c.LogStore.DeleteLogsBatch(ctx, cutoff, batchSize)
	if err != nil {
		return n, err
	}
	if err := c.gcOrphanCas(ctx); err != nil {
		c.logger.Warn("logstore/cas: orphan sweep after batch delete failed: %v", err)
	}
	return n, nil
}

// Flush deletes stale processing rows (delegated) and then reclaims the CAS
// content those rows pointed at — the inner delete bypasses DeleteLog(s), so
// without the sweep the blobs would only die with the next full GC.
func (c *CasLogStore) Flush(ctx context.Context, since time.Time) error {
	if err := c.LogStore.Flush(ctx, since); err != nil {
		return err
	}
	if err := c.gcOrphanCas(ctx); err != nil {
		c.logger.Warn("logstore/cas: orphan sweep after flush failed: %v", err)
	}
	return nil
}

func (c *CasLogStore) deleteCasForLogs(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var manifests []string
		if err := tx.Model(&casPayload{}).
			Where("log_id IN ?", ids).
			Pluck("blob_hash", &manifests).Error; err != nil {
			return err
		}
		if err := tx.Where("log_id IN ?", ids).Delete(&casPayload{}).Error; err != nil {
			return err
		}
		return gcForManifests(tx, manifests)
	})
}
