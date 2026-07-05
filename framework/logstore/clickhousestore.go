package logstore

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"reflect"
	"sort"
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
	// rmwLocks serializes read-modify-write cycles per row key within this
	// process. Because updates re-insert the whole row, two concurrent updaters
	// of the same id (e.g. object offload setting has_object while the
	// completion writer sets status/cost) would otherwise both read the same
	// base row and the higher `ver` would silently drop the other's patch.
	// Cross-pod races are not covered, but a given request id is only mutated
	// by the pod that processed it.
	rmwLocks [chRMWShards]sync.Mutex
}

// chRMWShards is the number of RMW lock shards; keys are hashed onto them.
const chRMWShards = 128

func chRMWShard(table, id string) int {
	h := fnv.New32a()
	h.Write([]byte(table))
	h.Write([]byte{0})
	h.Write([]byte(id))
	return int(h.Sum32() % chRMWShards)
}

// lockRMW locks the shard for a single row key and returns the unlock func.
func (s *ClickHouseLogStore) lockRMW(table, id string) func() {
	mu := &s.rmwLocks[chRMWShard(table, id)]
	mu.Lock()
	return mu.Unlock
}

// lockRMWBatch locks the distinct shards covering a set of row keys in
// ascending shard order (so concurrent batch lockers cannot deadlock) and
// returns the unlock func.
func (s *ClickHouseLogStore) lockRMWBatch(table string, ids []string) func() {
	seen := make(map[int]struct{}, len(ids))
	for _, id := range ids {
		seen[chRMWShard(table, id)] = struct{}{}
	}
	shards := make([]int, 0, len(seen))
	for sh := range seen {
		shards = append(shards, sh)
	}
	sort.Ints(shards)
	for _, sh := range shards {
		s.rmwLocks[sh].Lock()
	}
	return func() {
		for i := len(shards) - 1; i >= 0; i-- {
			s.rmwLocks[shards[i]].Unlock()
		}
	}
}

// chSchemaCache is a shared GORM schema parse cache reused across RMW calls.
var chSchemaCache sync.Map

func chParseSchema(db *gorm.DB, model interface{}) (*schema.Schema, error) {
	return schema.Parse(model, &chSchemaCache, db.NamingStrategy)
}

// chImmutableColumns are the ReplacingMergeTree dedup key columns (the tables'
// ORDER BY is `(timestamp, id)` / `id` - see clickhousemigrate.go). Updates must
// never rewrite them: a reinserted row with a different key value would be a
// new logical row instead of replacing the old one, so the helpers below skip
// them the same way a SQL UPDATE never rewrites its WHERE key.
var chImmutableColumns = map[string]struct{}{
	"id":         {},
	"timestamp":  {},
	"inc_number": {}, // DB-assigned monotonic insert-order number; must survive re-inserts
}

// chApplyUpdateMap applies a column->value map onto a struct pointer using the
// GORM schema field setters (which handle pointer / typed conversions).
// Dedup key columns are skipped.
func chApplyUpdateMap(ctx context.Context, st *schema.Schema, dest reflect.Value, updates map[string]interface{}) error {
	for col, val := range updates {
		if _, immutable := chImmutableColumns[col]; immutable {
			continue
		}
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
// Dedup key columns are skipped.
func chApplyStructUpdate(ctx context.Context, st *schema.Schema, dest, src reflect.Value) error {
	for _, f := range st.Fields {
		if f.DBName == "" {
			continue
		}
		if _, immutable := chImmutableColumns[f.DBName]; immutable {
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
	// Omit inc_number so ClickHouse's DEFAULT generateSnowflakeID() fires.
	return s.db.WithContext(ctx).Omit("inc_number").Create(entry).Error
}

// BatchCreateIfNotExists inserts multiple log entries. See CreateIfNotExists.
func (s *ClickHouseLogStore) BatchCreateIfNotExists(ctx context.Context, entries []*Log) error {
	if len(entries) == 0 {
		return nil
	}
	// Omit inc_number so ClickHouse's DEFAULT generateSnowflakeID() fires.
	return s.db.WithContext(ctx).Omit("inc_number").Create(&entries).Error
}

// BatchCreateMCPToolLogsIfNotExists inserts multiple MCP tool log entries. See
// CreateIfNotExists.
func (s *ClickHouseLogStore) BatchCreateMCPToolLogsIfNotExists(ctx context.Context, entries []*MCPToolLog) error {
	if len(entries) == 0 {
		return nil
	}
	// Omit inc_number so ClickHouse's DEFAULT generateSnowflakeID() fires.
	return s.db.WithContext(ctx).Omit("inc_number").Create(&entries).Error
}

// --- Updates (read-modify-write + re-insert) ---

// Update applies an update (a column->value map, or a *Log/Log whose non-zero
// fields are written) to the log row by re-inserting a patched copy.
func (s *ClickHouseLogStore) Update(ctx context.Context, id string, entry any) error {
	st, err := chParseSchema(s.db, &Log{})
	if err != nil {
		return err
	}
	defer s.lockRMW("logs", id)()
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
		if err := func() error {
			defer s.lockRMWBatch("logs", chunk)()
			var rows []*Log
			if err := s.db.WithContext(ctx).Where("id IN ?", chunk).Find(&rows).Error; err != nil {
				return err
			}
			if len(rows) == 0 {
				return nil
			}
			for _, r := range rows {
				cost := updates[r.ID]
				r.Cost = &cost
			}
			return s.chReinsert(ctx, &rows)
		}(); err != nil {
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
	defer s.lockRMW("mcp_tool_logs", id)()
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
	defer s.lockRMW("async_jobs", id)()
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
