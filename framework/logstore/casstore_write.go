package logstore

// CAS write path: Create/CreateIfNotExists/BatchCreateIfNotExists store the
// payload fields first (transactional, synchronous), then insert the
// lightweight row. Update intercepts column-keyed payload writes from the
// logging plugin. Deletes reclaim blobs via reachability.

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// casStoreFields stores every eligible payload field of one entry in a single
// transaction and returns the set of fields that were cleared from the row.
// The payload map keys are DB column names (see ExtractPayload); non-payload
// keys (provider, model, metadata, ...) are ignored here — they always belong
// to the row.
func (c *CasLogStore) casStoreFields(ctx context.Context, logID string, payload map[string]string) (map[string]struct{}, error) {
	cleared := make(map[string]struct{})
	type fieldContent struct {
		field   string
		content string
	}
	var toStore []fieldContent
	for field, content := range payload {
		if c.casEligible(field, content) {
			cleared[field] = struct{}{}
			toStore = append(toStore, fieldContent{field, content})
		}
	}
	if len(toStore) == 0 {
		return cleared, nil
	}
	err := c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, s := range toStore {
			if err := casStoreField(tx, logID, s.field, []byte(s.content), c.minChunkBytes); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("logstore/cas: store payload for log %s: %w", logID, err)
	}
	return cleared, nil
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

func (c *CasLogStore) Create(ctx context.Context, entry *Log) error {
	return c.createEntry(ctx, entry, func(e *Log) error { return c.LogStore.Create(ctx, e) })
}

func (c *CasLogStore) CreateIfNotExists(ctx context.Context, entry *Log) error {
	return c.createEntry(ctx, entry, func(e *Log) error { return c.LogStore.CreateIfNotExists(ctx, e) })
}

func (c *CasLogStore) BatchCreateIfNotExists(ctx context.Context, entries []*Log) error {
	for _, entry := range entries {
		if err := c.createEntry(ctx, entry, func(e *Log) error {
			return c.LogStore.CreateIfNotExists(ctx, e)
		}); err != nil {
			return err
		}
	}
	return nil
}

func (c *CasLogStore) createEntry(ctx context.Context, entry *Log, insert func(*Log) error) error {
	if err := entry.SerializeFields(); err != nil {
		return fmt.Errorf("logstore/cas: serialize before store: %w", err)
	}
	payload := c.extractCasPayload(entry)
	// CAS first: on failure nothing is persisted; on row-insert failure only
	// orphan blobs remain (harmless, reclaimed by GC).
	cleared, err := c.casStoreFields(ctx, entry.ID, payload)
	if err != nil {
		return err
	}
	dbEntry := *entry
	prepareCasEntry(&dbEntry, c.excluded, cleared)
	dbEntry.HasObject = len(cleared) > 0
	return insert(&dbEntry)
}

// Update intercepts the two shapes the logging plugin uses:
//
//   - map[string]interface{} with DB column keys ("output_message", ...): large
//     string values of payload fields go to CAS and the row column is emptied;
//     everything else (status, token_usage, small fields, excluded fields)
//     stays a row update.
//   - *Log: payload fields are extracted, large ones CAS'd, and the struct is
//     passed on with only the CAS'd fields cleared (gorm Updates updates the
//     remaining non-zero fields, as before).
func (c *CasLogStore) Update(ctx context.Context, id string, entry any) error {
	switch v := entry.(type) {
	case map[string]interface{}:
		return c.updateFromMap(ctx, id, v)
	case *Log:
		return c.updateFromStruct(ctx, id, v)
	default:
		return c.LogStore.Update(ctx, id, entry)
	}
}

func (c *CasLogStore) updateFromMap(ctx context.Context, id string, updates map[string]interface{}) error {
	rowUpdates := make(map[string]interface{}, len(updates))
	casFields := make(map[string]string)
	for k, val := range updates {
		s, isString := val.(string)
		if isString && c.casEligible(k, s) {
			casFields[k] = s
			rowUpdates[k] = "" // empty the row column; CAS is now authoritative
			continue
		}
		rowUpdates[k] = val
	}
	if len(rowUpdates) == 0 {
		// gorm Updates with an empty map is a no-op that reports 0 rows
		// affected, which RDBLogStore turns into ErrNotFound. Keep that
		// semantics observable with a self-assigning expression.
		rowUpdates["has_object"] = gorm.Expr("has_object")
	}
	if err := c.LogStore.Update(ctx, id, rowUpdates); err != nil {
		return err
	}
	return c.replaceFields(ctx, id, casFields)
}

func (c *CasLogStore) updateFromStruct(ctx context.Context, id string, lg *Log) error {
	if err := lg.SerializeFields(); err != nil {
		return fmt.Errorf("logstore/cas: serialize before update: %w", err)
	}
	payload := ExtractPayloadFiltered(lg, c.excluded)
	casFields := make(map[string]string)
	keep := make(map[string]struct{}, len(c.excluded)+len(payloadFields))
	for f := range c.excluded {
		keep[f] = struct{}{}
	}
	for _, f := range payloadFields {
		content, has := payload[f]
		if !has || !c.casEligible(f, content) {
			keep[f] = struct{}{}
			continue
		}
		casFields[f] = content
	}
	dbEntry := *lg
	ClearPayloadFiltered(&dbEntry, keep)
	if err := c.LogStore.Update(ctx, id, &dbEntry); err != nil {
		return err
	}
	return c.replaceFields(ctx, id, casFields)
}

// replaceFields rewrites the CAS pointers for logID: fields present in
// newPayload get fresh manifests; fields absent keep their existing CAS
// content (Update is a partial update).
func (c *CasLogStore) replaceFields(ctx context.Context, logID string, newPayload map[string]string) error {
	if len(newPayload) == 0 {
		return nil
	}
	err := c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for field, content := range newPayload {
			if err := casStoreField(tx, logID, field, []byte(content), c.minChunkBytes); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("logstore/cas: update payload for log %s: %w", logID, err)
	}
	return nil
}

// deleteCasFields removes the CAS pointers for the given fields and reclaims
// blobs that became unreachable.
func (c *CasLogStore) deleteCasFields(ctx context.Context, logID string, fields []string) error {
	if len(fields) == 0 {
		return nil
	}
	return c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
	})
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

// gcOrphanCas is the full reachability sweep: pointers to missing log rows are
// dropped, then unreferenced blobs, then ref rows whose manifest is gone. Run
// only from batch deletions — it scans the CAS tables.
func (c *CasLogStore) gcOrphanCas(ctx context.Context) error {
	return c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where(
			"NOT EXISTS (SELECT 1 FROM logs WHERE logs.id = cas_payloads.log_id)",
		).Delete(&casPayload{}).Error; err != nil {
			return err
		}
		if err := tx.Where(
			"NOT EXISTS (SELECT 1 FROM cas_payloads WHERE cas_payloads.blob_hash = cas_blobs.hash)"+
				" AND NOT EXISTS (SELECT 1 FROM cas_refs WHERE cas_refs.target_hash = cas_blobs.hash)"+
				" AND NOT EXISTS (SELECT 1 FROM cas_refs WHERE cas_refs.owner_hash = cas_blobs.hash)",
		).Delete(&casBlob{}).Error; err != nil {
			return err
		}
		return tx.Where(
			"NOT EXISTS (SELECT 1 FROM cas_blobs WHERE cas_blobs.hash = cas_refs.owner_hash)",
		).Delete(&casRef{}).Error
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
