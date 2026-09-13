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
//   - Deletes reclaim blobs via reachability, with the row deletion and the
//     CAS cleanup for the same IDs inside ONE transaction (a same-ID recreate
//     between the two would otherwise lose its fresh pointers); the full sweep
//     computes reachability from live log roots so a dead manifest's ref edges
//     cannot keep its segments (and itself) alive forever.

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
)

// casSplitPayload partitions an extracted payload map into fields that go to
// CAS. Hidden rows force every nonempty payload field into CAS because their
// row content is cleared regardless of the normal size threshold; applying the
// threshold there would destroy the only copy of a small field.
func (c *CasLogStore) casSplitPayload(payload map[string]string, forceAll bool) (map[string]struct{}, []casFieldContent) {
	cleared := make(map[string]struct{})
	var toStore []casFieldContent
	for field, content := range payload {
		if content != "" && (forceAll || c.casEligible(field, content)) {
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

// createRow inserts an already serialized and preview-prepared entry. The Log
// BeforeCreate hook only sets CreatedAt and calls SerializeFields; repeating
// serialization here would rebuild an intentionally empty preview from retained
// output. Preserve its timestamp responsibility and skip model hooks only for
// this prepared insert (no callbacks or global hook changes).
func (c *CasLogStore) createRow(tx *gorm.DB, dbEntry *Log, ifNotExists bool) (inserted bool, err error) {
	if dbEntry.CreatedAt.IsZero() {
		dbEntry.CreatedAt = time.Now().UTC()
	}
	db := tx.Session(&gorm.Session{SkipHooks: true})
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
			if err := writeCASInventory(tx, p.entry.ID, "native"); err != nil {
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
	cleared, toStore := c.casSplitPayload(payload, entry.ContentHidden)
	dbEntry := *entry
	// Hybrid-parity content_summary: prepareCasEntry (via prepareDBEntry)
	// overwrites the SerializeFields-built full input+output summary with
	// hybrid's last-user-message preview truncated to 2048 bytes, and clears
	// it entirely for hidden rows. That value stands: CAS mode keeps the same
	// list-preview and search scope as hybrid mode, and the full payload
	// remains available through CAS hydration. Writing the full summary back
	// made the row grow with the whole conversation history (quadratic across
	// agent turns) for no hybrid-equivalent benefit.
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
		if err := writeCASInventory(tx, p.entry.ID, "native"); err != nil {
			return err
		}
		p.entry.ContentSummary = p.dbEntry.ContentSummary
		return nil
	})
}

// Update accepts the three Log entry shapes used by the framework. Other
// shapes fail closed: delegating them would bypass CAS and preview bounds.
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
		return fmt.Errorf("logstore/cas: Update: unsupported entry type %T; accepts map[string]interface{}, Log or *Log", entry)
	}
}

// normalizeUpdateMapKeys rewrites Go struct field-name keys in an update map
// to their DB column names before any CAS decision is made. gorm's own
// Updates accepts both spellings (LookUpField resolves "OutputMessage" to
// output_message), so a map written with Go field names used to slip past the
// payload interception here: the row column changed while the stale CAS
// pointer survived, and hydration resurrected the OLD content over the new
// value — gorm.Expr values bypassed the payload type rejection the same way.
// Normalizing first makes both spellings semantically identical; keys that
// are neither a known column nor a known field pass through unchanged, which
// is exactly what gorm would do with them.
//
// Two distinct input keys that normalize to the SAME database column are an
// ambiguous update: with map iteration order being random, the previous
// last-writer-wins behavior made the effective value (and, for a
// string-vs-gorm.Expr clash, whether the write was accepted at all) depend on
// Go's map ordering. Such maps are now rejected up front, before any CAS or
// row write happens — even when the two values are identical, so the rule
// stays simple and never needs value-equivalence reasoning about gorm.Expr.
//
// Schema resolution failure is also fatal (fail closed): a silent passthrough
// would let Go field-name keys bypass the CAS interception again. The failure
// is captured by logSchemaOnce, so every subsequent call returns the same
// cached error — there is no repeated re-parse, and schema.Parse reports
// failures as errors rather than panicking.
func (c *CasLogStore) normalizeUpdateMapKeys(updates map[string]interface{}) (map[string]interface{}, error) {
	c.logSchemaOnce.Do(func() {
		c.logSchema, c.logSchemaErr = schema.Parse(&Log{}, &sync.Map{}, c.db.NamingStrategy)
	})
	if c.logSchemaErr != nil {
		return nil, fmt.Errorf(
			"logstore/cas: Update: cannot parse Log schema for map key normalization (fail closed): %w",
			c.logSchemaErr)
	}
	if c.logSchema == nil {
		return nil, fmt.Errorf(
			"logstore/cas: Update: Log schema unavailable for map key normalization (fail closed)")
	}
	out := make(map[string]interface{}, len(updates))
	origins := make(map[string]string, len(updates)) // DB column -> first original key seen
	changed := false
	for k, v := range updates {
		var col string
		if _, isColumn := c.logSchema.FieldsByDBName[k]; isColumn {
			col = k // already a column name
		} else if f, isField := c.logSchema.FieldsByName[k]; isField && f.DBName != "" {
			col = f.DBName
			changed = true
		} else {
			out[k] = v // unknown key: transparent passthrough (gorm's behavior)
			continue
		}
		if prev, clash := origins[col]; clash {
			first, second := prev, k
			if second < first {
				first, second = second, first
			}
			return nil, fmt.Errorf(
				"logstore/cas: Update: map keys %q and %q both normalize to column %q; ambiguous update rejected before any write",
				first, second, col)
		}
		origins[col] = k
		out[col] = v
	}
	if !changed {
		return updates, nil
	}
	return out, nil
}

// normalizeCasSummary validates before the transaction's SQLite writer reservation
// or hidden-row override. A typed nil *string is an explicit SQL NULL clear.
func normalizeCasSummary(value any) (any, error) {
	switch v := value.(type) {
	case nil:
		return nil, nil
	case string:
		return truncateTag(v, maxContentSummaryBytes), nil
	case []byte:
		return truncateTag(string(v), maxContentSummaryBytes), nil
	case *string:
		if v == nil {
			return nil, nil
		}
		return truncateTag(*v, maxContentSummaryBytes), nil
	default:
		return nil, fmt.Errorf("logstore/cas: Update: content_summary has unsupported value type %T; accepts nil, string, []byte or *string", value)
	}
}

func (c *CasLogStore) updateFromMap(ctx context.Context, id string, updates map[string]interface{}) error {
	normalized, err := c.normalizeUpdateMapKeys(updates)
	if err != nil {
		return err
	}
	if value, ok := normalized["content_summary"]; ok {
		summary, err := normalizeCasSummary(value)
		if err != nil {
			return err
		}
		copyUpdates := make(map[string]interface{}, len(normalized))
		for k, v := range normalized {
			copyUpdates[k] = v
		}
		copyUpdates["content_summary"] = summary
		normalized = copyUpdates
	}
	return c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// SQLite has no SELECT FOR UPDATE. Acquire its writer reservation
		// before reading, avoiding a deferred read-to-write upgrade that can
		// fail immediately with SQLITE_BUSY under concurrent GC/writers.
		if tx.Dialector.Name() == "sqlite" {
			if err := tx.Model(&Log{}).Where("id = ?", id).UpdateColumn("has_object", gorm.Expr("has_object")).Error; err != nil {
				return err
			}
		}
		var current Log
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", id).Take(&current).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if tx.Dialector.Name() == "sqlite" {
			if err := VerifyCASInventory(tx, id); err != nil {
				return err
			}
		}
		hidden := current.ContentHidden
		if value, ok := normalized["content_hidden"]; ok {
			var valid bool
			hidden, valid = value.(bool)
			if !valid {
				return fmt.Errorf("logstore/cas: Update: content_hidden must be a bool")
			}
		}
		// Copy caller-owned maps before adding the fields that hiding must evacuate.
		updates := make(map[string]interface{}, len(normalized)+len(payloadFields))
		for k, v := range normalized {
			updates[k] = v
		}
		if hidden {
			var pointers []casPayload
			if err := tx.Where("log_id = ?", id).Find(&pointers).Error; err != nil {
				return err
			}
			stored := make(map[string]bool, len(pointers))
			for _, pointer := range pointers {
				stored[pointer.Field] = true
			}
			payload := ExtractPayload(&current)
			for _, field := range payloadFields {
				if _, supplied := updates[field]; !supplied && !stored[field] && payload[field] != "" {
					updates[field] = payload[field]
				}
			}
			updates["content_summary"] = ""
		}
		return c.updateMapTx(tx, id, updates, hidden)
	})
}

func (c *CasLogStore) updateMapTx(tx *gorm.DB, id string, updates map[string]interface{}, hidden bool) error {
	rowUpdates := make(map[string]interface{}, len(updates))
	var toStore []casFieldContent
	var downgrades []string // payload fields set to small/empty values: drop pointers
	for k, val := range updates {
		if _, isPayload := payloadFieldSet[k]; isPayload {
			if val == nil {
				// A payload field set to nil is an explicit clear, same as the
				// downgrade path: the pointer must be dropped and the row column
				// becomes NULL (the inner store writes a nil map value as NULL),
				// otherwise hydration would resurrect the old CAS content.
				downgrades = append(downgrades, k)
				rowUpdates[k] = nil
				continue
			}
			// Payload update values converge to three supported shapes: string,
			// nil (explicit clear above) and []byte (normalized to string here).
			// Anything else — gorm.Expr, typed pointers, driver Valuer wrappers,
			// numbers... — used to pass straight into the row update: the column
			// changed while the stale CAS pointer survived, and hydration
			// resurrected the OLD content over the new value. Fail closed
			// instead: unsupported payload update types are rejected up front,
			// before any CAS or row write happens.
			s, isString := val.(string)
			if !isString {
				if b, isBytes := val.([]byte); isBytes {
					s = string(b)
					isString = true
				} else {
					return fmt.Errorf(
						"logstore/cas: Update: payload field %q has unsupported value type %T; CAS storage accepts string, []byte or nil for payload fields",
						k, val)
				}
			}
			if s != "" && (hidden || c.casEligible(k, s)) {
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
		if k == "content_summary" {
			// Hybrid-parity cap: hybrid's Update passthrough writes the update
			// map verbatim (its create path is the only one that truncates), so
			// callers like the logging plugin's output-only summary update can
			// put a full response into the column there. CAS mode caps every
			// update-path write at the same 2048-byte UTF-8-safe bound the
			// create path uses, keeping row storage bounded; empty and hidden
			// values (already "") pass through unchanged.
			var err error
			val, err = normalizeCasSummary(val)
			if err != nil {
				return err
			}
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
	if hidden {
		// Clear previews as well as excluded and below-threshold payloads.
		for _, field := range payloadFields {
			rowUpdates[field] = ""
		}
	}
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
	return writeCASInventory(tx, id, "native")
}

func (c *CasLogStore) updateFromStruct(ctx context.Context, id string, lg *Log) error {
	if lg == nil {
		return fmt.Errorf("log entry is nil")
	}
	// Preserve GORM's nonzero struct update semantics, but route all payload
	// decisions through the same locked snapshot as map updates. In particular,
	// false does not unhide a row through a struct update; use an explicit map.
	copyEntry := *lg
	if err := copyEntry.SerializeFields(); err != nil {
		return fmt.Errorf("logstore/cas: serialize before update: %w", err)
	}
	if _, err := c.normalizeUpdateMapKeys(nil); err != nil {
		return err
	}
	updates := make(map[string]interface{})
	value := reflect.ValueOf(&copyEntry)
	for _, field := range c.logSchema.Fields {
		if field.DBName == "" || !field.Updatable {
			continue
		}
		if v, zero := field.ValueOf(ctx, value); !zero {
			updates[field.DBName] = v
		}
	}
	return c.updateFromMap(ctx, id, updates)
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
		if tx.Dialector.Name() == "sqlite" {
			if err := tx.Where("NOT EXISTS (SELECT 1 FROM logs WHERE logs.id = cas_inventories.log_id)").Delete(&CASInventory{}).Error; err != nil {
				return err
			}
		}
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
			"NOT EXISTS (SELECT 1 FROM cas_payloads WHERE cas_payloads.blob_hash = cas_blobs.hash)" +
				" AND NOT EXISTS (SELECT 1 FROM cas_refs WHERE cas_refs.target_hash = cas_blobs.hash)",
		).Delete(&casBlob{}).Error
	})
}

// DeleteLog removes the row AND reclaims its CAS content inside ONE
// transaction. Splitting the two (row delete, then cleanup) let a recreate of
// the same ID interleave between them, and the cleanup then deleted the NEW
// log's fresh CAS pointers. Sharing the transaction closes that window: the
// recreate blocks on the logs primary key until the cleanup has committed.
func (c *CasLogStore) DeleteLog(ctx context.Context, id string) error {
	return c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Replicates the inner store's DeleteLog (simple Where Delete, not
		// found is not an error) so the CAS cleanup runs on the same tx.
		if err := tx.Where("id = ?", id).Delete(&Log{}).Error; err != nil {
			return err
		}
		return deleteCasForLogsTx(tx, []string{id})
	})
}

// DeleteLogs is DeleteLog for a batch: row deletion and CAS cleanup for the
// whole ID set share one transaction, same race reasoning as DeleteLog.
func (c *CasLogStore) DeleteLogs(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("id IN ?", ids).Delete(&Log{}).Error; err != nil {
			return err
		}
		return deleteCasForLogsTx(tx, ids)
	})
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
		return deleteCasForLogsTx(tx, ids)
	})
}

// deleteCasForLogsTx removes the CAS pointers for ids inside tx and reclaims
// the manifests they referenced. Callers that delete the log rows themselves
// pass the same tx so row deletion and cleanup are atomic.
func deleteCasForLogsTx(tx *gorm.DB, ids []string) error {
	if tx.Dialector.Name() == "sqlite" {
		if err := tx.Where("log_id IN ?", ids).Delete(&CASInventory{}).Error; err != nil {
			return err
		}
	}
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
}
