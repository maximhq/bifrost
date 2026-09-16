package logstore

// CAS read path: ONE database transaction — the "unified read snapshot" —
// reads the log root row AND its CAS side tables (pointers, manifests,
// segments). The root-row read applies the caller's read visibility
// (QueryScope + dashboard hidden request types) inside the same transaction,
// so authorization happens at the root and the CAS rows it makes visible are
// followed within one snapshot. There is no second observation point and
// therefore no revision-verification/retry machinery: on PostgreSQL the read
// transaction runs at REPEATABLE READ so every statement in it sees the same
// snapshot; on SQLite (WAL) a transaction reads one snapshot for its whole
// lifetime on the connection.
//
// This replaces the round-4 design (scoped root read outside the transaction
// + verifyRootRowRevision column-list comparison + ErrCasConcurrentModification
// bounded retries), whose manual binding-column list could never be complete
// (model/provider/object_type and other scalars were missing) and whose
// comparison of database columns against possibly business-modified in-memory
// objects produced false positives.
//
// Integrity contract (design doc §7): every blob is verified against its
// content hash on read — codec, length AND digest — and corruption is an
// error. FindByID (the detail/export path) propagates hydration failures to
// the caller; FindFirst/FindAll degrade per log (warn + skip) so one corrupt
// row cannot blank an entire list view.
//
// PostgreSQL note: REPEATABLE READ is requested through gorm's transaction
// options; a read-only REPEATABLE READ transaction never fails with a
// serialization error in PostgreSQL, so no retry loop is needed for it. The
// test suite exercises this path on SQLite; the PostgreSQL isolation level is
// set but not covered by tests here.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/bytedance/sonic"
	"gorm.io/gorm"
)

// errCasRowUnservable aborts a unified read transaction when a hydrated row
// cannot be served whole (corruption, missing blobs). It never reaches the
// caller as-is: FindFirst maps it to the inner store's record-not-found
// behavior for an unusable read, FindAll drops the row from the result.
var errCasRowUnservable = errors.New("logstore/cas: row cannot be served whole after hydration failure")

// casReadTxOptions returns the transaction options a unified read snapshot
// needs. PostgreSQL's default READ COMMITTED takes a fresh snapshot per
// STATEMENT, so the root-row read and the CAS reads after it could still be
// torn apart by a concurrent commit inside one transaction; REPEATABLE READ
// pins one snapshot for the whole transaction. SQLite needs no option: a
// transaction holds one snapshot for its whole lifetime (WAL mode), and the
// mattn/go-sqlite3 driver does not implement driver.ConnBeginTx, so passing
// a non-default isolation level makes database/sql fail the BEGIN — options
// stay nil on every non-postgres dialect.
func (c *CasLogStore) casReadTxOptions() *sql.TxOptions {
	if c.db.Dialector.Name() == "postgres" {
		return &sql.TxOptions{Isolation: sql.LevelRepeatableRead}
	}
	return nil
}

// readTx runs fn inside one read transaction on the unscoped handle. Nothing
// inside a unified read writes, so a non-nil return only rolls the snapshot
// back, which is harmless.
func (c *CasLogStore) readTx(ctx context.Context, fn func(tx *gorm.DB) error) error {
	return c.db.WithContext(ctx).Transaction(fn, c.casReadTxOptions())
}

func (c *CasLogStore) FindByID(ctx context.Context, id string) (*Log, error) {
	var out *Log
	err := c.readTx(ctx, func(tx *gorm.DB) error {
		// Root row first, with the caller's read visibility applied: an
		// out-of-scope or dashboard-hidden id is simply not found, which is
		// the authorization decision. The row and everything hydration reads
		// afterwards come from this one snapshot.
		log, err := c.rdb.findLogByIDTx(ctx, tx, id)
		if err != nil {
			return err
		}
		if err := c.hydrateFieldsTx(tx, log, false); err != nil {
			c.hydrateErrors.Add(1)
			return fmt.Errorf("logstore/cas: hydrate log %s: %w", id, err)
		}
		out = log
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Child-cost rollup mirrors the inner FindByID. It aggregates OTHER rows,
	// so it runs after the snapshot commit instead of inside it — the same
	// post-processing position it always had.
	rows := []Log{*out}
	if err := c.rdb.attachChildAggregates(ctx, rows, SearchFilters{}); err != nil {
		c.logger.Warn("logstore/cas: child aggregates unavailable for log %s, returning it without them: %v", id, err)
		return out, nil
	}
	return &rows[0], nil
}

func (c *CasLogStore) FindFirst(ctx context.Context, query any, fields ...string) (*Log, error) {
	fields, wildcard := casNormalizeProjection(fields)
	needsHydration := wildcard || len(fields) == 0 || fieldsNeedHydration(fields)
	if needsHydration && len(fields) > 0 {
		fields = ensureHydrationFields(fields)
	}
	var out *Log
	hydrateFailed := false
	err := c.readTx(ctx, func(tx *gorm.DB) error {
		log, err := c.rdb.findLogTx(ctx, tx, query, fields...)
		if err != nil {
			return err
		}
		if !needsHydration {
			out = log
			return nil
		}
		var hydrErr error
		if wildcard {
			hydrErr = c.hydrateFieldsTx(tx, log, false)
		} else {
			hydrErr = c.hydrateFieldsTx(tx, log, false, fields...)
		}
		if hydrErr != nil {
			hydrateFailed = true
			c.hydrateErrors.Add(1)
			c.logger.Warn("logstore/cas: hydrate failed for log %s: %v", log.ID, hydrErr)
			// The row cannot be served whole; a partially hydrated log would
			// masquerade as real content. Match the inner store's
			// record-not-found behavior for an unusable read.
			return errCasRowUnservable
		}
		out = log
		return nil
	})
	if err != nil {
		if hydrateFailed {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return out, nil
}

func (c *CasLogStore) FindAll(ctx context.Context, query any, fields ...string) ([]*Log, error) {
	fields, wildcard := casNormalizeProjection(fields)
	needsHydration := wildcard || len(fields) == 0 || fieldsNeedHydration(fields)
	if needsHydration && len(fields) > 0 {
		fields = ensureHydrationFields(fields)
	}
	var out []*Log
	err := c.readTx(ctx, func(tx *gorm.DB) error {
		logs, err := c.rdb.findLogsTx(ctx, tx, query, fields...)
		if err != nil {
			return err
		}
		out = logs
		if !needsHydration {
			return nil
		}
		// Hydration failures drop the entry from the result (warn + skip, as
		// the design doc promises): serving a partially hydrated log would
		// present empty fields as real content. HydrateErrors is still
		// incremented for observability.
		kept := logs[:0]
		for _, log := range logs {
			var hydrErr error
			if wildcard {
				hydrErr = c.hydrateFieldsTx(tx, log, false)
			} else {
				hydrErr = c.hydrateFieldsTx(tx, log, false, fields...)
			}
			if hydrErr != nil {
				c.hydrateErrors.Add(1)
				c.logger.Warn("logstore/cas: hydrate failed for log %s: %v", log.ID, hydrErr)
				continue
			}
			kept = append(kept, log)
		}
		out = kept
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// hydrateFieldsTx reads pointers, manifests and segments from the caller's
// unified read snapshot transaction and merges the reconstructed payload into
// log. It is a no-op when the row has no CAS content or content is hidden
// (read-side enforcement, same as hybrid). Every failure is returned as an
// error — a corrupt or missing blob must never masquerade as an absent field
// — while still hydrating the remaining fields first, so a single corrupt
// field degrades the detail view, not the whole log.
//
// includeHidden bypasses the ContentHidden serving gate for the billing path,
// which — exactly like hybrid mode — must recover pricing inputs from hidden
// rows without ever exposing their content in a serving read.
//
// Authorization: these reads deliberately do NOT apply the caller's
// QueryScope. A QueryScope carries predicates over logs columns
// (user_id/virtual_key_id, ...); the CAS tables have none of those columns,
// so applying the scope turned legitimate hydrations into SQL errors. The
// authorization model is: row-level access control lives at the log root —
// every serving path reads the root row through a visibility-filtered query
// in the SAME transaction before this runs, and CAS references then follow
// the already-authorized row. Reading CAS rows without the scope grants no
// extra visibility: the hydrated content is only ever attached to a row the
// caller was already allowed to read.
func (c *CasLogStore) hydrateFieldsTx(tx *gorm.DB, log *Log, includeHidden bool, requestedFields ...string) error {
	if log == nil {
		return nil
	}
	if tx.Dialector.Name() == "sqlite" {
		if err := VerifyCASInventory(tx, log.ID); err != nil {
			return err
		}
	}
	if !log.HasObject {
		return nil
	}
	if log.ContentHidden && !includeHidden {
		return nil
	}
	var rows []casPayload
	if err := tx.Where("log_id = ?", log.ID).Find(&rows).Error; err != nil {
		return fmt.Errorf("list payloads: %w", err)
	}
	if len(rows) == 0 {
		// has_object=true promises CAS pointers. An empty pointer set means
		// the cas_payloads rows were lost (manual surgery, partial corruption);
		// serving the row anyway would silently present empty content as the
		// real payload, so it must be an error.
		//
		return fmt.Errorf("logstore/cas: log %s: has_object is set but cas payloads are missing", log.ID)
	}
	if len(requestedFields) > 0 {
		requested := make(map[string]struct{}, len(requestedFields))
		for _, f := range requestedFields {
			requested[f] = struct{}{}
		}
		filtered := rows[:0]
		for _, r := range rows {
			if _, ok := requested[r.Field]; ok {
				filtered = append(filtered, r)
			}
		}
		rows = filtered
	}
	if len(rows) == 0 {
		return nil
	}
	manifestHashes := make([]string, 0, len(rows))
	for _, r := range rows {
		manifestHashes = append(manifestHashes, r.BlobHash)
	}
	var manifestBlobs []casBlob
	if err := tx.Where("hash IN ?", manifestHashes).Find(&manifestBlobs).Error; err != nil {
		return fmt.Errorf("fetch manifests: %w", err)
	}
	manifestRaw := make(map[string][]byte, len(manifestBlobs))
	segmentHashes := make(map[string]struct{})
	for _, mb := range manifestBlobs {
		raw, err := casDecodeBlob(mb, casManifestDomain)
		if err != nil {
			return fmt.Errorf("decode manifest %s: %w", mb.Hash, err)
		}
		manifestRaw[mb.Hash] = raw
		var m casManifest
		if err := sonic.Unmarshal(raw, &m); err != nil {
			return fmt.Errorf("unmarshal manifest %s: %w", mb.Hash, err)
		}
		for _, p := range m.Parts {
			if p.Hash != "" {
				segmentHashes[p.Hash] = struct{}{}
			}
		}
	}
	if len(manifestRaw) == 0 {
		return fmt.Errorf("manifest blobs missing: %v", manifestHashes)
	}
	hashes := make([]string, 0, len(segmentHashes))
	for h := range segmentHashes {
		hashes = append(hashes, h)
	}
	var segmentBlobs []casBlob
	if len(hashes) > 0 {
		if err := tx.Where("hash IN ?", hashes).Find(&segmentBlobs).Error; err != nil {
			return fmt.Errorf("fetch segments: %w", err)
		}
	}
	segments := make(map[string][]byte, len(segmentBlobs))
	for _, sb := range segmentBlobs {
		raw, err := casDecodeBlob(sb, casDataDomain)
		if err != nil {
			return fmt.Errorf("decode segment %s: %w", sb.Hash, err)
		}
		segments[sb.Hash] = raw
	}
	lookup := func(hash string) ([]byte, error) {
		if raw, ok := segments[hash]; ok {
			return raw, nil
		}
		return nil, fmt.Errorf("segment blob missing")
	}
	var hydrateErr error
	values := make(map[string]string, len(rows))
	for _, r := range rows {
		raw, ok := manifestRaw[r.BlobHash]
		if !ok {
			err := fmt.Errorf("manifest %s missing for field %s", r.BlobHash, r.Field)
			if hydrateErr == nil {
				hydrateErr = err
			}
			c.logger.Warn("logstore/cas: log %s: %v", log.ID, err)
			continue
		}
		content, err := casReconstruct(raw, lookup)
		if err != nil {
			if hydrateErr == nil {
				hydrateErr = fmt.Errorf("field %s: %w", r.Field, err)
			}
			c.logger.Warn("logstore/cas: log %s: %v", log.ID, err)
			continue
		}
		values[r.Field] = string(content)
	}
	if len(values) > 0 {
		// The root row's content_summary is persistent search/index metadata and
		// remains authoritative. MergePayloadFromJSON rebuilds it as a legacy
		// object-store repair step; CAS hydration must not silently change the
		// detail/export representation relative to the row-resident baseline.
		contentSummary := log.ContentSummary
		// The snapshot values are the raw TEXT column values; merging via the
		// payload-map JSON form matches MergePayloadFromJSON's expectations.
		data, err := sonic.Marshal(values)
		if err != nil {
			if hydrateErr == nil {
				hydrateErr = fmt.Errorf("marshal hydrated payload: %w", err)
			}
		} else if err := MergePayloadFromJSON(log, data); err != nil {
			if hydrateErr == nil {
				hydrateErr = fmt.Errorf("merge payload: %w", err)
			}
			c.logger.Warn("logstore/cas: merge payload for log %s failed: %v", log.ID, err)
		} else {
			log.ContentSummary = contentSummary
		}
		pruneUnrequestedPayloadFields(log, requestedFields)
	}
	return hydrateErr
}

// casDecodeBlob verifies codec, decompresses, checks the recorded length, and
// re-computes the content hash against the blob's claimed identity in the
// given domain. A valid zstd frame of the right length but wrong content is
// corruption, not a successful read.
func casDecodeBlob(b casBlob, domain string) ([]byte, error) {
	if b.Codec != casCodecZstd {
		return nil, fmt.Errorf("logstore/cas: unknown codec %q", b.Codec)
	}
	raw, err := casDecoder.DecodeAll(b.Data, nil)
	if err != nil {
		return nil, fmt.Errorf("logstore/cas: zstd decode: %w", err)
	}
	if int64(len(raw)) != b.OrigLen {
		return nil, fmt.Errorf("logstore/cas: decoded length %d != recorded %d", len(raw), b.OrigLen)
	}
	if got := casHash(domain, raw); got != b.Hash {
		return nil, fmt.Errorf("logstore/cas: content hash mismatch: blob %s actually hashes to %s", b.Hash, got)
	}
	return raw, nil
}

// HydrateBillingChunk restores the payload fields pricing reads (token_usage,
// cache_debug and this row's modality payload column) for offloaded rows.
// Mirrors hybrid's contract: rows whose inputs cannot be recovered are
// reported as Unpriceable instead of being billed from a lossy stub. Each row
// gets its own short unified-snapshot transaction (root row + CAS reads);
// the batch is deliberately NOT wrapped in one long transaction.
func (c *CasLogStore) HydrateBillingChunk(ctx context.Context, logs []*Log) (BillingHydrationResult, error) {
	var result BillingHydrationResult
	for _, log := range logs {
		if log == nil || !log.HasObject {
			// Never offloaded: the row already carries its own payload.
			continue
		}
		if !billingRowNeedsHydration(log, c.excluded) {
			continue
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if hydrateErr := c.hydrateLogForBilling(ctx, log); hydrateErr != nil {
			c.hydrateErrors.Add(1)
			c.logger.Warn("logstore/cas: cannot hydrate pricing inputs for log %s: %v", log.ID, hydrateErr)
			result.Unpriceable = append(result.Unpriceable, log.ID)
			continue
		}
		result.Hydrated = append(result.Hydrated, log.ID)
	}
	return result, nil
}

// billingModalityInputPresent reports whether the row carries the pricing
// input for its modality column. The serialized column is the primary
// evidence, but image_generation_output has a second legal state: the billing
// search already ran stripNonBillingPayloadBytes, which clears the serialized
// column while leaving the parsed structure — the image count pricing
// actually reads — intact. Accepting the parsed structure mirrors the strip
// exactly (it clears the column whenever ImageGenerationOutputParsed is
// non-nil), so the fail-closed case is unchanged: a CAS-offloaded image whose
// pointer was lost parses nothing, and an empty column beside a nil parsed
// struct still rejects the row.
func billingModalityInputPresent(l *Log, column string) bool {
	if billingPayloadColumnValue(l, column) != "" {
		return true
	}
	if column == "image_generation_output" && l.ImageGenerationOutputParsed != nil {
		return true
	}
	return false
}

// hydrateLogForBilling replaces the caller's billing row only after a complete
// authorized root + CAS snapshot has been recovered and validated. The batch
// projection is an earlier observation: retaining any of its pricing metadata
// (model, provider, ownership, object type, etc.) would mix revisions. Derive
// the modality from the fresh root, and publish metadata and payload together.
func (c *CasLogStore) hydrateLogForBilling(ctx context.Context, log *Log) error {
	var ready *Log
	if err := c.readTx(ctx, func(tx *gorm.DB) error {
		fresh, err := c.rdb.findLogByIDTx(ctx, tx, log.ID)
		if err != nil {
			return fmt.Errorf("re-read log row: %w", err)
		}
		fields := []string{"token_usage", "cache_debug"}
		if col := billingPayloadColumnFor(fresh.Object); col != "" {
			fields = append(fields, col)
		}
		if err := c.hydrateFieldsTx(tx, fresh, true, fields...); err != nil {
			return err
		}
		if err := fresh.DeserializeFields(); err != nil {
			return fmt.Errorf("deserialize hydrated payload: %w", err)
		}
		if fresh.TokenUsage == "" && billingPayloadColumnFor(fresh.Object) == "" {
			return fmt.Errorf("hydrated payload has no token_usage")
		}
		if col := billingPayloadColumnFor(fresh.Object); col != "" && !billingModalityInputPresent(fresh, col) {
			return fmt.Errorf("hydrated payload has no %s", col)
		}
		fresh.billingPayloadsHydrated = true
		stripNonBillingPayloadBytes(fresh)
		ready = fresh
		return nil
	}); err != nil {
		return err
	}
	*log = *ready
	return nil
}

// CasStats reports CAS table sizes and operational counters for
// observability.
type CasStats struct {
	Blobs     int64 `json:"blobs"`
	BlobBytes int64 `json:"blob_bytes"`
	Refs      int64 `json:"refs"`
	Payloads  int64 `json:"payloads"`
	// SegmentHits counts segment blobs referenced by more than one manifest —
	// the rows that exist only because of cross-request dedup.
	SegmentHits int64 `json:"segment_hits"`
	// Fallbacks counts fields stored as one opaque blob because the JSON
	// array split failed (design doc: fallback metric).
	Fallbacks int64 `json:"fallbacks"`
	// HydrateErrors counts read-side hydration failures (corruption, missing
	// blobs).
	HydrateErrors int64 `json:"hydrate_errors"`
}

func (c *CasLogStore) CasStorageStats(ctx context.Context) (*CasStats, error) {
	var stats CasStats
	err := c.scopedDB(ctx).Raw(
		"SELECT " +
			"(SELECT COUNT(*) FROM cas_blobs) AS blobs, " +
			"(SELECT COALESCE(SUM(LENGTH(data)),0) FROM cas_blobs) AS blob_bytes, " +
			"(SELECT COUNT(*) FROM cas_refs) AS refs, " +
			"(SELECT COUNT(*) FROM cas_payloads) AS payloads, " +
			"(SELECT COUNT(*) FROM cas_blobs b WHERE (SELECT COUNT(DISTINCT r.owner_id) FROM cas_refs r WHERE r.target_id = b.id) > 1) AS segment_hits",
	).Scan(&stats).Error
	if err != nil {
		return nil, err
	}
	stats.Fallbacks = c.fallbacks.Load()
	stats.HydrateErrors = c.hydrateErrors.Load()
	return &stats, nil
}
