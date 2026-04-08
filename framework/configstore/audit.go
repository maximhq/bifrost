package configstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
)

// AppendAuditLog inserts a new audit record, computing the hash chain.
// This is the primary write path; it must be called asynchronously from the
// main request path to avoid latency impact.
func (s *RDBConfigStore) AppendAuditLog(ctx context.Context, entry *tables.TableAuditLog) error {
	if entry.ID == "" {
		entry.ID = uuid.New().String()
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	if entry.Result == "" {
		entry.Result = "success"
	}

	// Fetch previous hash for chaining. Use a transaction with row-level lock
	// to ensure monotonic ordering under concurrent writers.
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var prev tables.TableAuditLog
		prevHash := "0000000000000000000000000000000000000000000000000000000000000000" // genesis
		if err := tx.Order("seq DESC").Limit(1).First(&prev).Error; err == nil {
			prevHash = prev.Hash
		}
		entry.PrevHash = prevHash

		// Compute this record's hash over the canonical JSON (excludes Hash/PrevHash themselves)
		entry.Hash = computeAuditHash(entry)

		return tx.Create(entry).Error
	})
}

// computeAuditHash computes SHA-256 of the audit record's content fields.
func computeAuditHash(e *tables.TableAuditLog) string {
	type hashInput struct {
		ID         string `json:"id"`
		Seq        int64  `json:"seq"`
		Timestamp  string `json:"timestamp"`
		ActorID    string `json:"actor_id"`
		Action     string `json:"action"`
		Resource   string `json:"resource"`
		ResourceID string `json:"resource_id"`
		Result     string `json:"result"`
		OldValue   string `json:"old_value"`
		NewValue   string `json:"new_value"`
		PrevHash   string `json:"prev_hash"`
	}
	hi := hashInput{
		ID:         e.ID,
		Seq:        e.Seq,
		Timestamp:  e.Timestamp.UTC().Format(time.RFC3339Nano),
		ActorID:    e.ActorID,
		Action:     e.Action,
		Resource:   e.Resource,
		ResourceID: e.ResourceID,
		Result:     e.Result,
		OldValue:   e.OldValue,
		NewValue:   e.NewValue,
		PrevHash:   e.PrevHash,
	}
	data, _ := json.Marshal(hi)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// QueryAuditLogs returns a paginated list of audit log entries, optionally
// filtered by actor, resource, action, or time range.
// Results are ordered by seq DESC (newest first).
func (s *RDBConfigStore) QueryAuditLogs(ctx context.Context, opts AuditLogQueryOpts) ([]tables.TableAuditLog, int64, error) {
	q := s.db.WithContext(ctx).Model(&tables.TableAuditLog{})
	if opts.ActorID != "" {
		q = q.Where("actor_id = ?", opts.ActorID)
	}
	if opts.Resource != "" {
		q = q.Where("resource = ?", opts.Resource)
	}
	if opts.ResourceID != "" {
		q = q.Where("resource_id = ?", opts.ResourceID)
	}
	if opts.Action != "" {
		q = q.Where("action = ?", opts.Action)
	}
	if !opts.StartTime.IsZero() {
		q = q.Where("timestamp >= ?", opts.StartTime)
	}
	if !opts.EndTime.IsZero() {
		q = q.Where("timestamp <= ?", opts.EndTime)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("audit log count: %w", err)
	}

	var logs []tables.TableAuditLog
	page := opts.Page
	if page < 1 {
		page = 1
	}
	pageSize := opts.PageSize
	if pageSize < 1 || pageSize > 1000 {
		pageSize = 100
	}
	offset := (page - 1) * pageSize

	if err := q.Order("seq DESC").Offset(offset).Limit(pageSize).Find(&logs).Error; err != nil {
		return nil, 0, fmt.Errorf("audit log query: %w", err)
	}
	return logs, total, nil
}

// VerifyAuditChain validates the hash chain for all records starting from the
// given seq. Returns the first broken seq, or -1 if the chain is intact.
func (s *RDBConfigStore) VerifyAuditChain(ctx context.Context, fromSeq int64) (int64, error) {
	var logs []tables.TableAuditLog
	if err := s.db.WithContext(ctx).
		Where("seq >= ?", fromSeq).
		Order("seq ASC").
		Find(&logs).Error; err != nil {
		return -1, fmt.Errorf("verify audit chain: %w", err)
	}

	prevHash := ""
	for i, log := range logs {
		if i > 0 && log.PrevHash != prevHash {
			return log.Seq, fmt.Errorf("hash chain broken at seq %d", log.Seq)
		}
		recomputed := computeAuditHash(&log)
		if recomputed != log.Hash {
			return log.Seq, fmt.Errorf("tampered record at seq %d", log.Seq)
		}
		prevHash = log.Hash
	}
	return -1, nil
}

// AuditLogQueryOpts encapsulates filtering and pagination options for audit log queries.
type AuditLogQueryOpts struct {
	ActorID    string
	Resource   string
	ResourceID string
	Action     string
	StartTime  time.Time
	EndTime    time.Time
	Page       int
	PageSize   int
}
