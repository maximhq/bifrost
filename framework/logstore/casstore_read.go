package logstore

// CAS read path: hydration reassembles payload fields byte-identically from
// the manifest + segment blobs and merges them back into the Log, mirroring
// hybrid mode's gating (only FindByID/FindFirst/FindAll hydrate; list/search
// views rely on content_summary and the last-user-message preview).
//
// Integrity contract (design doc §7): every blob is verified against its
// content hash on read — codec, length AND digest — and corruption is an
// error. FindByID (the detail/export path) propagates hydration failures to
// the caller; FindFirst/FindAll degrade per log (warn + skip) so one corrupt
// row cannot blank an entire list view.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"gorm.io/gorm"
)

// ErrCasConcurrentModification reports that the log root row changed between
// the caller's scoped read and the CAS hydration snapshot: the in-memory
// object's metadata no longer belongs to the payload revision the CAS tables
// now hold. Hydrating anyway would bind the old row's metadata/authorization
// to the new revision's content. Find* callers catch this sentinel and retry
// the full path (scoped re-read + hydration) so the served log is always one
// consistent revision, freshly authorized.
var ErrCasConcurrentModification = errors.New("log row changed concurrently during hydration")

// casRootRowRetryLimit bounds how often Find* re-runs the full scoped
// read + hydration path after ErrCasConcurrentModification before giving up
// and surfacing the path's normal failure semantics. The budget is larger
// than a minimal 2–3 because each retry is paced by casRetryBackoff: a row
// updated in a tight loop keeps colliding with the tiny window between the
// scoped read and the hydration snapshot, and consecutive immediate retries
// correlate with the writer's phase. With backoff the attempts decorrelate,
// so exhausting this budget means genuinely sustained contention, not bad
// luck — and the failure still degrades the way the path always has.
const casRootRowRetryLimit = 8

// casRetryBackoff is the base pause between ErrCasConcurrentModification
// retry attempts; it doubles per attempt (capped) so retries decorrelate from
// a writer's update phase instead of repeatedly hitting the same window.
const casRetryBackoff = 200 * time.Microsecond

// casRetrySleep waits between concurrent-modification retry attempts. The
// final attempt does not sleep.
func casRetrySleep(attempt int) {
	if attempt+1 >= casRootRowRetryLimit {
		return
	}
	d := casRetryBackoff << attempt
	if d > 5*time.Millisecond {
		d = 5 * time.Millisecond
	}
	time.Sleep(d)
}

func (c *CasLogStore) FindByID(ctx context.Context, id string) (*Log, error) {
	var lastErr error
	for attempt := 0; attempt < casRootRowRetryLimit; attempt++ {
		log, err := c.LogStore.FindByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if err := c.hydrateLog(ctx, log); err != nil {
			lastErr = err
			if errors.Is(err, ErrCasConcurrentModification) {
				// The row was concurrently modified between the scoped read
				// and hydration; retry so both come from one revision.
				casRetrySleep(attempt)
				continue
			}
			c.hydrateErrors.Add(1)
			return nil, fmt.Errorf("logstore/cas: hydrate log %s: %w", id, err)
		}
		return log, nil
	}
	c.hydrateErrors.Add(1)
	return nil, fmt.Errorf("logstore/cas: hydrate log %s: %w", id, lastErr)
}

func (c *CasLogStore) FindFirst(ctx context.Context, query any, fields ...string) (*Log, error) {
	fields, wildcard := casNormalizeProjection(fields)
	needsHydration := wildcard || len(fields) == 0 || fieldsNeedHydration(fields)
	if needsHydration && len(fields) > 0 {
		fields = ensureHydrationFields(fields)
	}
	var lastErr error
	for attempt := 0; attempt < casRootRowRetryLimit; attempt++ {
		log, err := c.LogStore.FindFirst(ctx, query, fields...)
		if err != nil {
			return nil, err
		}
		if !needsHydration {
			return log, nil
		}
		var hydrErr error
		if wildcard {
			hydrErr = c.hydrateLog(ctx, log)
		} else {
			hydrErr = c.hydrateLog(ctx, log, fields...)
		}
		if hydrErr == nil {
			return log, nil
		}
		lastErr = hydrErr
		if errors.Is(hydrErr, ErrCasConcurrentModification) {
			casRetrySleep(attempt)
			continue
		}
		c.hydrateErrors.Add(1)
		c.logger.Warn("logstore/cas: hydrate failed for log %s: %v", log.ID, hydrErr)
		// The row cannot be served whole; a partially hydrated log would
		// masquerade as real content. Match the inner store's
		// record-not-found behavior for an unusable read.
		return nil, ErrNotFound
	}
	// Retry budget exhausted under continuous concurrent modification; the
	// row still cannot be served whole, so keep the same degrade semantics.
	c.hydrateErrors.Add(1)
	c.logger.Warn("logstore/cas: hydrate failed for log after %d attempts: %v", casRootRowRetryLimit, lastErr)
	return nil, ErrNotFound
}

func (c *CasLogStore) FindAll(ctx context.Context, query any, fields ...string) ([]*Log, error) {
	fields, wildcard := casNormalizeProjection(fields)
	needsHydration := wildcard || len(fields) == 0 || fieldsNeedHydration(fields)
	if needsHydration && len(fields) > 0 {
		fields = ensureHydrationFields(fields)
	}
	logs, err := c.LogStore.FindAll(ctx, query, fields...)
	if err != nil {
		return nil, err
	}
	if needsHydration {
		// Hydration failures drop the entry from the result (warn + skip, as
		// the design doc promises): serving a partially hydrated log would
		// present empty fields as real content. HydrateErrors is still
		// incremented for observability.
		kept := logs[:0]
		for _, log := range logs {
			var hydrErr error
			for attempt := 0; attempt < casRootRowRetryLimit; attempt++ {
				if wildcard {
					hydrErr = c.hydrateLog(ctx, log)
				} else {
					hydrErr = c.hydrateLog(ctx, log, fields...)
				}
				if hydrErr == nil || !errors.Is(hydrErr, ErrCasConcurrentModification) {
					break
				}
				casRetrySleep(attempt)
			}
			if hydrErr != nil {
				c.hydrateErrors.Add(1)
				c.logger.Warn("logstore/cas: hydrate failed for log %s: %v", log.ID, hydrErr)
				continue
			}
			kept = append(kept, log)
		}
		logs = kept
	}
	return logs, nil
}

// hydrateLog restores CAS-stored payload fields into log. It is a no-op when
// the row has no CAS content or content is hidden (read-side enforcement, same
// as hybrid). Every failure is returned as an error — a corrupt or missing
// blob must never masquerade as an absent field — while still hydrating the
// remaining fields first, so a single corrupt field degrades the detail view,
// not the whole log.
func (c *CasLogStore) hydrateLog(ctx context.Context, log *Log, requestedFields ...string) error {
	return c.hydrateFields(ctx, log, false, requestedFields...)
}

// hydrateFields is the hydration core. includeHidden bypasses the
// ContentHidden serving gate for the billing path, which — exactly like
// hybrid mode — must recover pricing inputs from hidden rows without ever
// exposing their content in a serving read.
func (c *CasLogStore) hydrateFields(ctx context.Context, log *Log, includeHidden bool, requestedFields ...string) error {
	if log == nil || !log.HasObject {
		return nil
	}
	if log.ContentHidden && !includeHidden {
		return nil
	}
	// All CAS table reads run inside ONE read transaction on the unscoped
	// handle, for two reasons.
	//
	// Snapshot consistency: the pointer rows, manifests and segments must come
	// from the same database snapshot. Separate autocommit SELECTs could read
	// the pointer rows of the old revision and then miss its manifest after a
	// concurrent writer's Update committed and reclaimed the old blobs —
	// healthy data misreported as "manifest missing". Inside one transaction
	// every statement sees one snapshot: on SQLite (WAL) all SELECTs share the
	// snapshot begun with the first read. On PostgreSQL the default READ
	// COMMITTED gives per-statement snapshots, so a commit can still land
	// between two statements inside this transaction; the window is far
	// smaller than autocommit's and the read-side hash/length verification
	// still rejects torn content, but a strict guarantee there would require
	// REPEATABLE READ. Documented as a known boundary rather than forced.
	//
	// Authorization: these reads deliberately do NOT apply the caller's
	// QueryScope. A QueryScope carries predicates over logs columns
	// (user_id/virtual_key_id, ...); the CAS tables have none of those columns,
	// so applying the scope turned legitimate hydrations into SQL errors. The
	// authorization model is: row-level access control lives at the log root —
	// every serving and billing path first obtains the log row through the
	// scoped inner store (FindByID/FindFirst/FindAll/billing batch selection
	// all run scoped queries), and CAS references then follow the
	// already-authorized row. Reading CAS rows without the scope grants no
	// extra visibility: the hydrated content is only ever attached to a row
	// the caller was already allowed to read.
	return c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return c.hydrateFieldsTx(tx, log, includeHidden, requestedFields...)
	})
}

// verifyRootRowRevision re-reads the log root row's observable state INSIDE
// the hydration snapshot transaction and rejects it when that state no longer
// matches the in-memory object the caller authorized. The inner scoped read
// and the CAS snapshot are two separate database observations; a concurrent
// Update (or a delete + same-ID recreate) can commit between them, leaving
// hydration about to merge the NEW revision's payload into the OLD revision's
// metadata — including rows whose ownership or content_hidden changed, which
// would silently bypass the caller's scope. Detecting the divergence and
// failing with ErrCasConcurrentModification forces the Find* wrappers to retry
// the whole path, so the row state and the hydrated content are always bound
// to one revision and authorization always comes from the fresh scoped read.
//
// The comparison set is chosen for reliability without false positives:
//
//   - has_object and content_hidden are always compared; both are selected by
//     every projection that hydrates (ensureHydrationFields).
//   - Full-row serving reads (no projection) additionally compare the
//     ownership/authorization columns (casRootRowBindingColumns) and every
//     payload TEXT column byte-for-byte: the inner read loaded all of them,
//     so any concurrent row write that changes what the row is or whom it
//     belongs to is detected. This is what binds "metadata is version A" to
//     "payload is version A" for the A/B update race.
//   - Projected reads (serving or billing) compare only the requested payload
//     columns: unprojected columns are zero in the in-memory object and would
//     false-positive. A concurrent write to a column outside the projection
//     is therefore not detected — the projection never serves it either.
//
// casRootRowBindingColumns are the non-payload row columns a full-row serving
// read additionally binds to the hydration snapshot. They are the columns
// whose staleness would matter: the ownership/authorization set the caller's
// QueryScope predicates run over (a row whose user_id changed under a read
// must not be hydrated with the previous scope decision), plus status and
// content_summary as serving-visible metadata. A concurrent update touching
// ONLY an unlisted column (latency, cost, ...) is not detected — it never
// changes what the row is allowed to show.
var casRootRowBindingColumns = []string{
	"user_id", "virtual_key_id", "team_id", "customer_id", "business_unit_id",
	"project_id", "selected_key_id", "status", "content_summary",
}

func verifyRootRowRevision(tx *gorm.DB, log *Log, requestedFields []string, includeHidden bool) error {
	fullRow := len(requestedFields) == 0 && !includeHidden
	colSet := map[string]struct{}{"has_object": {}, "content_hidden": {}}
	compare := map[string]struct{}{}
	if fullRow {
		for _, f := range casRootRowBindingColumns {
			colSet[f] = struct{}{}
			compare[f] = struct{}{}
		}
		for _, f := range payloadFields {
			colSet[f] = struct{}{}
			compare[f] = struct{}{}
		}
	} else {
		for _, f := range requestedFields {
			if _, isPayload := payloadFieldSet[f]; isPayload {
				colSet[f] = struct{}{}
				compare[f] = struct{}{}
			}
		}
	}
	cols := make([]string, 0, len(colSet))
	for col := range colSet {
		cols = append(cols, col)
	}
	sort.Strings(cols) // stable SQL across calls (map iteration is not)

	var state map[string]any
	if err := tx.Table("logs").
		Select(strings.Join(cols, ", ")).
		Where("id = ?", log.ID).
		Take(&state).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// The row the caller authorized is gone (deleted, possibly
			// recreated with different ownership): never hydrate onto it.
			return fmt.Errorf("logstore/cas: log %s: root row changed before hydration: %w", log.ID, ErrCasConcurrentModification)
		}
		return fmt.Errorf("logstore/cas: log %s: re-read root row: %w", log.ID, err)
	}
	mismatch := func(what string) error {
		return fmt.Errorf("logstore/cas: log %s: root row %s changed concurrently: %w", log.ID, what, ErrCasConcurrentModification)
	}
	if casScanBool(state["has_object"]) != log.HasObject {
		return mismatch("has_object")
	}
	if casScanBool(state["content_hidden"]) != log.ContentHidden {
		return mismatch("content_hidden")
	}
	if len(compare) > 0 {
		inMemory := ExtractPayload(log)
		for f := range compare {
			var memory string
			if _, isPayload := payloadFieldSet[f]; isPayload {
				memory = inMemory[f]
			} else {
				memory = casLogColumnString(log, f)
			}
			if casScanString(state[f]) != memory {
				return mismatch("column " + f)
			}
		}
	}
	return nil
}

// casLogColumnString reads one non-payload binding column from the in-memory
// Log, with NULL and zero value converging to "".
func casLogColumnString(l *Log, column string) string {
	deref := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	switch column {
	case "user_id":
		return deref(l.UserID)
	case "virtual_key_id":
		return deref(l.VirtualKeyID)
	case "team_id":
		return deref(l.TeamID)
	case "customer_id":
		return deref(l.CustomerID)
	case "business_unit_id":
		return deref(l.BusinessUnitID)
	case "project_id":
		return deref(l.ProjectID)
	case "selected_key_id":
		return l.SelectedKeyID
	case "status":
		return l.Status
	case "content_summary":
		return l.ContentSummary
	default:
		return ""
	}
}

// casScanString normalizes a raw driver value from a TEXT/nullable column to
// the empty-string representation the Log struct uses for NULL/empty.
func casScanString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []byte:
		return string(t)
	default:
		return fmt.Sprintf("%v", t)
	}
}

// casScanBool normalizes a raw driver value from a boolean column across
// dialects (SQLite stores 0/1 integers, PostgreSQL native bool; gorm's map
// scan widens integers to float64).
func casScanBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case int64:
		return t != 0
	case int:
		return t != 0
	case float64:
		return t != 0
	case []byte:
		return len(t) > 0 && t[0] != '0' && t[0] != 0
	case string:
		return t == "t" || t == "true" || t == "1"
	default:
		return false
	}
}

// hydrateFieldsTx reads pointers, manifests and segments from the single
// snapshot tx and merges the reconstructed payload into log. A non-nil return
// rolls the (read-only) transaction back, which is harmless: nothing in here
// writes.
func (c *CasLogStore) hydrateFieldsTx(tx *gorm.DB, log *Log, includeHidden bool, requestedFields ...string) error {
	// Bind the hydration snapshot to the revision of the root row the caller
	// actually read: any divergence (concurrent update, delete + recreate) is
	// a retryable failure, never something to hydrate onto.
	if err := verifyRootRowRevision(tx, log, requestedFields, includeHidden); err != nil {
		return err
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
		// Known structural limit: a SINGLE missing pointer row among several
		// is indistinguishable from a field that legitimately lives in the row
		// (was downgraded with has_object left stale) and cannot be detected
		// here; per-pointer integrity is enforced once the pointer is read.
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
// reported as Unpriceable instead of being billed from a lossy stub.
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

// hydrateLogForBilling reassembles only the fields pricing reads from the
// CAS pointers, then re-deserializes so the virtual structs match. Unlike
// hydrateLog it never serves partially-hydrated content: an error here means
// the row stays Unpriceable rather than being billed from a stub.
func (c *CasLogStore) hydrateLogForBilling(ctx context.Context, log *Log) error {
	fields := []string{"token_usage", "cache_debug"}
	if col := billingPayloadColumnFor(log.Object); col != "" {
		fields = append(fields, col)
	}
	if err := c.hydrateFields(ctx, log, true, fields...); err != nil {
		return err
	}
	// MergePayloadFromJSON wrote the serialized columns; re-deserialize so
	// TokenUsageParsed reflects the hydrated payload.
	if err := log.DeserializeFields(); err != nil {
		return fmt.Errorf("deserialize hydrated payload: %w", err)
	}
	// Only token-billed rows need token_usage. A modality row prices off the
	// payload recovered above, so an empty usage is not an error for them.
	if log.TokenUsage == "" && billingPayloadColumnFor(log.Object) == "" {
		return fmt.Errorf("hydrated payload has no token_usage")
	}
	// A modality row (speech/ocr/image_generation/... — see
	// billingPayloadColumns) prices off its modality payload column, not
	// token_usage. If that column is still empty after hydration — the CAS
	// pointer row was lost, or hydration recovered nothing for it — pricing
	// has no input, and a "successful" hydration that counts the row as
	// Hydrated would let billing continue on a lossy stub. Fail closed the
	// same way a missing token_usage does: the row goes to Unpriceable.
	//
	// The checks run BEFORE stripNonBillingPayloadBytes: strip clears
	// ImageGenerationOutput (rdb.go), and billing reads the modality payload
	// through billingPayloadColumnValue, so checking after the strip
	// misjudged every healthy image_generation row — parsed billing input
	// fully recovered, raw column deliberately released — as Unpriceable.
	// Verified here is exactly what billing reads before hydration success is
	// declared: the hydrated serialized column (equivalently its parsed
	// struct, which DeserializeFields just built from it).
	if col := billingPayloadColumnFor(log.Object); col != "" && billingPayloadColumnValue(log, col) == "" {
		return fmt.Errorf("hydrated payload has no %s", col)
	}
	// All checks passed. Only now may the row carry the hydrated marker: the
	// marker suppresses billingRowNeedsHydration, and the billing caller
	// blocks only on Unpriceable — setting it before the checks let a failed
	// row sail through a second HydrateBillingChunk call as "nothing left to
	// fetch" and be billed from its lossy stub.
	log.billingPayloadsHydrated = true
	// Drop payload bytes pricing never reads; it only needs counts/inputs.
	stripNonBillingPayloadBytes(log)
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
			"(SELECT COUNT(*) FROM cas_blobs b WHERE (SELECT COUNT(DISTINCT r.owner_hash) FROM cas_refs r WHERE r.target_hash = b.hash) > 1) AS segment_hits",
	).Scan(&stats).Error
	if err != nil {
		return nil, err
	}
	stats.Fallbacks = c.fallbacks.Load()
	stats.HydrateErrors = c.hydrateErrors.Load()
	return &stats, nil
}
