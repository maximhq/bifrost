package logstore

// CAS read path: hydration reassembles payload fields byte-identically from
// the manifest + segment blobs and merges them back into the Log, mirroring
// hybrid mode's gating (only FindByID/FindFirst/FindAll hydrate; list/search
// views rely on content_summary and the last-user-message preview).

import (
	"context"
	"fmt"

	"github.com/bytedance/sonic"
)

func (c *CasLogStore) FindByID(ctx context.Context, id string) (*Log, error) {
	log, err := c.LogStore.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	c.hydrateLog(ctx, log)
	return log, nil
}

func (c *CasLogStore) FindFirst(ctx context.Context, query any, fields ...string) (*Log, error) {
	needsHydration := len(fields) == 0 || fieldsNeedHydration(fields)
	if needsHydration && len(fields) > 0 {
		fields = ensureHydrationFields(fields)
	}
	log, err := c.LogStore.FindFirst(ctx, query, fields...)
	if err != nil {
		return nil, err
	}
	if needsHydration {
		c.hydrateLog(ctx, log, fields...)
	}
	return log, nil
}

func (c *CasLogStore) FindAll(ctx context.Context, query any, fields ...string) ([]*Log, error) {
	needsHydration := len(fields) == 0 || fieldsNeedHydration(fields)
	if needsHydration && len(fields) > 0 {
		fields = ensureHydrationFields(fields)
	}
	logs, err := c.LogStore.FindAll(ctx, query, fields...)
	if err != nil {
		return nil, err
	}
	if needsHydration {
		for _, log := range logs {
			c.hydrateLog(ctx, log, fields...)
		}
	}
	return logs, nil
}

// hydrateLog restores CAS-stored payload fields into log. It is a no-op when
// the row has no CAS content or content is hidden (read-side enforcement, same
// as hybrid). Per-field failures are logged and skipped so one corrupt field
// cannot blank an entire detail view; hydration never fabricates content.
func (c *CasLogStore) hydrateLog(ctx context.Context, log *Log, requestedFields ...string) {
	if log == nil || !log.HasObject || log.ContentHidden {
		return
	}
	var rows []casPayload
	if err := c.db.WithContext(ctx).Where("log_id = ?", log.ID).Find(&rows).Error; err != nil {
		c.logger.Warn("logstore/cas: list payloads for log %s failed: %v", log.ID, err)
		return
	}
	if len(rows) == 0 {
		return
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
		return
	}
	manifestHashes := make([]string, 0, len(rows))
	for _, r := range rows {
		manifestHashes = append(manifestHashes, r.BlobHash)
	}
	var manifestBlobs []casBlob
	if err := c.db.WithContext(ctx).Where("hash IN ?", manifestHashes).Find(&manifestBlobs).Error; err != nil {
		c.logger.Warn("logstore/cas: fetch manifests for log %s failed: %v", log.ID, err)
		return
	}
	manifestRaw := make(map[string][]byte, len(manifestBlobs))
	segmentHashes := make(map[string]struct{})
	for _, mb := range manifestBlobs {
		raw, err := casDecodeBlob(mb)
		if err != nil {
			c.logger.Warn("logstore/cas: decode manifest for log %s failed: %v", log.ID, err)
			continue
		}
		manifestRaw[mb.Hash] = raw
		var m casManifest
		if err := sonic.Unmarshal(raw, &m); err != nil {
			continue
		}
		for _, p := range m.Parts {
			if p.Hash != "" {
				segmentHashes[p.Hash] = struct{}{}
			}
		}
	}
	if len(manifestRaw) == 0 {
		return
	}
	hashes := make([]string, 0, len(segmentHashes))
	for h := range segmentHashes {
		hashes = append(hashes, h)
	}
	var segmentBlobs []casBlob
	if len(hashes) > 0 {
		if err := c.db.WithContext(ctx).Where("hash IN ?", hashes).Find(&segmentBlobs).Error; err != nil {
			c.logger.Warn("logstore/cas: fetch segments for log %s failed: %v", log.ID, err)
			return
		}
	}
	segments := make(map[string][]byte, len(segmentBlobs))
	for _, sb := range segmentBlobs {
		raw, err := casDecodeBlob(sb)
		if err != nil {
			c.logger.Warn("logstore/cas: decode segment %s failed: %v", sb.Hash, err)
			continue
		}
		segments[sb.Hash] = raw
	}
	lookup := func(hash string) ([]byte, error) {
		if raw, ok := segments[hash]; ok {
			return raw, nil
		}
		return nil, fmt.Errorf("segment blob missing")
	}
	values := make(map[string]string, len(rows))
	for _, r := range rows {
		raw, ok := manifestRaw[r.BlobHash]
		if !ok {
			c.logger.Warn("logstore/cas: manifest %s missing for log %s field %s", r.BlobHash, log.ID, r.Field)
			continue
		}
		content, err := casReconstruct(raw, lookup)
		if err != nil {
			c.logger.Warn("logstore/cas: reconstruct log %s field %s failed: %v", log.ID, r.Field, err)
			continue
		}
		values[r.Field] = string(content)
	}
	if len(values) == 0 {
		return
	}
	// The snapshot values are the raw TEXT column values; merging via the
	// payload-map JSON form matches MergePayloadFromJSON's expectations.
	data, err := sonic.Marshal(values)
	if err != nil {
		c.logger.Warn("logstore/cas: marshal hydrated payload for log %s: %v", log.ID, err)
		return
	}
	if err := MergePayloadFromJSON(log, data); err != nil {
		c.logger.Warn("logstore/cas: merge payload for log %s failed: %v", log.ID, err)
		return
	}
	pruneUnrequestedPayloadFields(log, requestedFields)
}

// casDecodeBlob verifies codec and length, then decompresses. Corruption is
// an error, never silently empty content.
func casDecodeBlob(b casBlob) ([]byte, error) {
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
	return raw, nil
}

// CasStats reports CAS table sizes for observability.
type CasStats struct {
	Blobs       int64 `json:"blobs"`
	BlobBytes   int64 `json:"blob_bytes"`
	Refs        int64 `json:"refs"`
	Payloads    int64 `json:"payloads"`
	SegmentHits int64 `json:"segment_hits"`
}

func (c *CasLogStore) CasStorageStats(ctx context.Context) (*CasStats, error) {
	var stats CasStats
	err := c.db.WithContext(ctx).Raw(
		"SELECT " +
			"(SELECT COUNT(*) FROM cas_blobs) AS blobs, " +
			"(SELECT COALESCE(SUM(LENGTH(data)),0) FROM cas_blobs) AS blob_bytes, " +
			"(SELECT COUNT(*) FROM cas_refs) AS refs, " +
			"(SELECT COUNT(*) FROM cas_payloads) AS payloads, " +
			"(SELECT COUNT(*) FROM cas_blobs b WHERE EXISTS (SELECT 1 FROM cas_refs r WHERE r.target_hash = b.hash)) AS segment_hits",
	).Scan(&stats).Error
	if err != nil {
		return nil, err
	}
	return &stats, nil
}
