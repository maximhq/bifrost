package logstore

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// ClickHouseLogStore is a LogStore backed by ClickHouse. It embeds *RDBLogStore
// to reuse the (dialect-aware) analytics/read path and overrides only the
// methods ClickHouse cannot satisfy through plain GORM:
//
//   - inserts that relied on ON CONFLICT DO NOTHING (ClickHouse has no upsert;
//     idempotency comes from ReplacingMergeTree dedup + the connection-level
//     `final = 1` setting, so a plain INSERT is correct),
//   - row updates (ClickHouse has no cheap UPDATE; we read-modify-write and
//     re-insert, letting the `ver` DEFAULT now64() column make the newest
//     insert win on merge - see clickhousemigrate.go).
//
// Deletes are left to the embedded methods: the GORM ClickHouse driver emits
// lightweight `DELETE ... WHERE`, and TTL is the primary retention mechanism.
type ClickHouseLogStore struct {
	*RDBLogStore
	// cluster is the optional ON CLUSTER name (empty = single-node). Retained
	// for future cluster-aware DDL.
	cluster string
}

// chSchemaCache is a shared GORM schema parse cache reused across RMW calls.
var chSchemaCache sync.Map

func chParseSchema(db *gorm.DB, model interface{}) (*schema.Schema, error) {
	return schema.Parse(model, &chSchemaCache, db.NamingStrategy)
}

// chApplyUpdateMap applies a column->value map onto a struct pointer using the
// GORM schema field setters (which handle pointer / typed conversions).
func chApplyUpdateMap(ctx context.Context, st *schema.Schema, dest reflect.Value, updates map[string]interface{}) error {
	for col, val := range updates {
		f, ok := st.FieldsByDBName[col]
		if !ok {
			continue
		}
		if err := f.Set(ctx, dest, val); err != nil {
			return fmt.Errorf("clickhouse: set column %s: %w", col, err)
		}
	}
	return nil
}

// chApplyStructUpdate overlays the non-zero fields of src onto dest, mirroring
// GORM's Updates(struct) semantics (zero-valued fields are not written).
func chApplyStructUpdate(ctx context.Context, st *schema.Schema, dest, src reflect.Value) error {
	for _, f := range st.Fields {
		if f.DBName == "" {
			continue
		}
		val, isZero := f.ValueOf(ctx, src)
		if isZero {
			continue
		}
		if err := f.Set(ctx, dest, val); err != nil {
			return fmt.Errorf("clickhouse: set column %s: %w", f.DBName, err)
		}
	}
	return nil
}

// chReinsert re-inserts a (possibly patched) row with hooks skipped so the
// BeforeCreate serialization does not clobber already-serialized base columns.
// The omitted `ver` column defaults to now64(), so this insert supersedes the
// prior version on the next ReplacingMergeTree merge (and immediately under
// `final = 1` reads).
func (s *ClickHouseLogStore) chReinsert(ctx context.Context, v interface{}) error {
	return s.db.WithContext(ctx).Session(&gorm.Session{SkipHooks: true}).Create(v).Error
}

// --- Inserts (no ON CONFLICT; RMT dedup handles idempotency) ---

// CreateIfNotExists inserts a log entry. Duplicate ids collapse on merge (and
// are hidden by `final = 1` reads), so a plain INSERT is idempotent.
func (s *ClickHouseLogStore) CreateIfNotExists(ctx context.Context, entry *Log) error {
	if entry == nil {
		return fmt.Errorf("log entry is nil")
	}
	return s.db.WithContext(ctx).Create(entry).Error
}

// BatchCreateIfNotExists inserts multiple log entries. See CreateIfNotExists.
func (s *ClickHouseLogStore) BatchCreateIfNotExists(ctx context.Context, entries []*Log) error {
	if len(entries) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Create(&entries).Error
}

// BatchCreateMCPToolLogsIfNotExists inserts multiple MCP tool log entries. See
// CreateIfNotExists.
func (s *ClickHouseLogStore) BatchCreateMCPToolLogsIfNotExists(ctx context.Context, entries []*MCPToolLog) error {
	if len(entries) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Create(&entries).Error
}

// --- Updates (read-modify-write + re-insert) ---

// Update applies an update (a column->value map, or a *Log/Log whose non-zero
// fields are written) to the log row by re-inserting a patched copy.
func (s *ClickHouseLogStore) Update(ctx context.Context, id string, entry any) error {
	st, err := chParseSchema(s.db, &Log{})
	if err != nil {
		return err
	}
	var existing Log
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&existing).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}
	dest := reflect.ValueOf(&existing).Elem()
	switch v := entry.(type) {
	case map[string]interface{}:
		if err := chApplyUpdateMap(ctx, st, dest, v); err != nil {
			return err
		}
	case *Log:
		if v == nil {
			return fmt.Errorf("clickhouse: nil *Log update")
		}
		if err := v.SerializeFields(); err != nil {
			return err
		}
		if err := chApplyStructUpdate(ctx, st, dest, reflect.ValueOf(v).Elem()); err != nil {
			return err
		}
	case Log:
		if err := v.SerializeFields(); err != nil {
			return err
		}
		if err := chApplyStructUpdate(ctx, st, dest, reflect.ValueOf(&v).Elem()); err != nil {
			return err
		}
	default:
		return fmt.Errorf("clickhouse: unsupported Update entry type %T", entry)
	}
	return s.chReinsert(ctx, &existing)
}

// BulkUpdateCost backfills costs by reading each chunk of rows, patching cost,
// and re-inserting. Reading the full row is required because the re-insert must
// reproduce every column (the ReplacingMergeTree dedup key includes timestamp).
func (s *ClickHouseLogStore) BulkUpdateCost(ctx context.Context, updates map[string]float64) error {
	if len(updates) == 0 {
		return nil
	}
	ids := make([]string, 0, len(updates))
	for id := range updates {
		ids = append(ids, id)
	}
	for start := 0; start < len(ids); start += bulkUpdateCostChunkSize {
		end := start + bulkUpdateCostChunkSize
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		var rows []*Log
		if err := s.db.WithContext(ctx).Where("id IN ?", chunk).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			continue
		}
		for _, r := range rows {
			cost := updates[r.ID]
			r.Cost = &cost
		}
		if err := s.chReinsert(ctx, &rows); err != nil {
			return err
		}
	}
	return nil
}

// UpdateMCPToolLog applies an update to an MCP tool log row via read-modify-write.
func (s *ClickHouseLogStore) UpdateMCPToolLog(ctx context.Context, id string, entry any) error {
	st, err := chParseSchema(s.db, &MCPToolLog{})
	if err != nil {
		return err
	}
	var existing MCPToolLog
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&existing).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}
	dest := reflect.ValueOf(&existing).Elem()
	switch v := entry.(type) {
	case map[string]interface{}:
		if err := chApplyUpdateMap(ctx, st, dest, v); err != nil {
			return err
		}
	case *MCPToolLog:
		if v == nil {
			return fmt.Errorf("clickhouse: nil *MCPToolLog update")
		}
		if err := v.SerializeFields(); err != nil {
			return err
		}
		if err := chApplyStructUpdate(ctx, st, dest, reflect.ValueOf(v).Elem()); err != nil {
			return err
		}
	case MCPToolLog:
		if err := v.SerializeFields(); err != nil {
			return err
		}
		if err := chApplyStructUpdate(ctx, st, dest, reflect.ValueOf(&v).Elem()); err != nil {
			return err
		}
	default:
		return fmt.Errorf("clickhouse: unsupported UpdateMCPToolLog entry type %T", entry)
	}
	return s.chReinsert(ctx, &existing)
}

// UpdateAsyncJob applies a column->value map to an async job row via
// read-modify-write.
func (s *ClickHouseLogStore) UpdateAsyncJob(ctx context.Context, id string, updates map[string]interface{}) error {
	st, err := chParseSchema(s.db, &AsyncJob{})
	if err != nil {
		return err
	}
	var existing AsyncJob
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&existing).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}
	dest := reflect.ValueOf(&existing).Elem()
	if err := chApplyUpdateMap(ctx, st, dest, updates); err != nil {
		return err
	}
	return s.chReinsert(ctx, &existing)
}
