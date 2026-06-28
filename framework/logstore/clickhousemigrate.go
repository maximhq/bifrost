package logstore

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// clickhouseColumnType maps a GORM-parsed field to a ClickHouse column type.
// Pointer fields become Nullable(...). This keeps the ClickHouse DDL in lockstep
// with the shared Log/MCPToolLog/AsyncJob structs so the reused read path (which
// references DB column names) never drifts from the physical schema.
func clickhouseColumnType(f *schema.Field) string {
	ft := f.FieldType
	nullable := false
	for ft.Kind() == reflect.Ptr {
		nullable = true
		ft = ft.Elem()
	}

	base := "String"
	switch ft.Kind() {
	case reflect.String:
		base = "String"
	case reflect.Bool:
		base = "Bool"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		base = "Int64"
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		base = "UInt64"
	case reflect.Float32, reflect.Float64:
		base = "Float64"
	case reflect.Struct:
		if ft == reflect.TypeOf(time.Time{}) {
			base = "DateTime64(3)"
		}
	}

	if nullable {
		return "Nullable(" + base + ")"
	}
	return base
}

// clickhouseColumnDefs parses the GORM schema for model and returns column
// definitions ("`name` Type") for every persisted field, in struct order.
func clickhouseColumnDefs(db *gorm.DB, model interface{}) ([]string, error) {
	st, err := schema.Parse(model, &sync.Map{}, db.NamingStrategy)
	if err != nil {
		return nil, fmt.Errorf("failed to parse schema for clickhouse DDL: %w", err)
	}
	var cols []string
	for _, f := range st.Fields {
		if f.DBName == "" || f.IgnoreMigration {
			continue
		}
		cols = append(cols, fmt.Sprintf("`%s` %s", f.DBName, clickhouseColumnType(f)))
	}
	return cols, nil
}

// chTableOpts describes the engine-level options for a ClickHouse table.
type chTableOpts struct {
	table       string
	partitionBy string   // empty = no PARTITION BY
	orderBy     string   // e.g. "(timestamp, id)"
	ttl         string   // empty = no TTL
	skipIndexes []string // full "INDEX ..." clauses
}

// clickhouseCreateTable derives the column list from the GORM model, appends the
// ReplacingMergeTree version column (`ver`, defaulted to now64() so every INSERT
// is auto-versioned), and runs an idempotent CREATE TABLE IF NOT EXISTS.
func clickhouseCreateTable(ctx context.Context, db *gorm.DB, model interface{}, opts chTableOpts, cluster string) error {
	cols, err := clickhouseColumnDefs(db, model)
	if err != nil {
		return err
	}
	// Version column for ReplacingMergeTree dedup. Not part of the Go struct, so
	// INSERTs omit it and ClickHouse fills now64(); a later re-insert (cost
	// backfill, has_object flip, idempotent retry) gets a higher ver and wins.
	cols = append(cols, "`ver` DateTime64(3) DEFAULT now64()")
	cols = append(cols, opts.skipIndexes...)

	engine := "ReplacingMergeTree(ver)"
	onCluster := ""
	if cluster != "" {
		onCluster = fmt.Sprintf(" ON CLUSTER `%s`", cluster)
		engine = fmt.Sprintf("ReplicatedReplacingMergeTree('/clickhouse/tables/{shard}/%s', '{replica}', ver)", opts.table)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TABLE IF NOT EXISTS `%s`%s (\n  %s\n) ENGINE = %s\n", opts.table, onCluster, strings.Join(cols, ",\n  "), engine)
	if opts.partitionBy != "" {
		fmt.Fprintf(&b, "PARTITION BY %s\n", opts.partitionBy)
	}
	fmt.Fprintf(&b, "ORDER BY %s\n", opts.orderBy)
	if opts.ttl != "" {
		fmt.Fprintf(&b, "TTL %s\n", opts.ttl)
	}
	b.WriteString("SETTINGS index_granularity = 8192")

	return db.WithContext(ctx).Exec(b.String()).Error
}

// clickhouseExistingColumns returns the set of column names already present on a
// ClickHouse table.
func clickhouseExistingColumns(ctx context.Context, db *gorm.DB, table string) (map[string]struct{}, error) {
	var names []string
	if err := db.WithContext(ctx).
		Raw("SELECT name FROM system.columns WHERE database = currentDatabase() AND table = ?", table).
		Scan(&names).Error; err != nil {
		return nil, err
	}
	set := make(map[string]struct{}, len(names))
	for _, n := range names {
		set[n] = struct{}{}
	}
	return set, nil
}

// clickhouseReconcileColumns adds any model columns missing from the live table
// via ALTER TABLE ... ADD COLUMN IF NOT EXISTS. This is the forward-evolution
// path: the shared Log/MCPToolLog structs gain fields over time, and CREATE
// TABLE IF NOT EXISTS only ever runs once. We do this ourselves (rather than the
// gorm driver's AutoMigrate) because AutoMigrate maps the structs' Postgres/
// SQLite tags (type:varchar(255) -> FixedString(255), etc.) incorrectly for
// ClickHouse; clickhouseColumnType maps Go kinds to clean String/Nullable types.
func clickhouseReconcileColumns(ctx context.Context, db *gorm.DB, model interface{}, table, cluster string, logger schemas.Logger) error {
	existing, err := clickhouseExistingColumns(ctx, db, table)
	if err != nil {
		return fmt.Errorf("clickhouse: read columns for %s: %w", table, err)
	}
	st, err := schema.Parse(model, &chSchemaCache, db.NamingStrategy)
	if err != nil {
		return fmt.Errorf("clickhouse: parse schema for %s: %w", table, err)
	}
	onCluster := ""
	if cluster != "" {
		onCluster = fmt.Sprintf(" ON CLUSTER `%s`", cluster)
	}
	for _, f := range st.Fields {
		if f.DBName == "" || f.IgnoreMigration {
			continue
		}
		if _, ok := existing[f.DBName]; ok {
			continue
		}
		stmt := fmt.Sprintf("ALTER TABLE `%s`%s ADD COLUMN IF NOT EXISTS `%s` %s", table, onCluster, f.DBName, clickhouseColumnType(f))
		logger.Info("[logstore] clickhouse: adding column %s.%s", table, f.DBName)
		if err := db.WithContext(ctx).Exec(stmt).Error; err != nil {
			return fmt.Errorf("clickhouse: add column %s.%s: %w", table, f.DBName, err)
		}
	}
	return nil
}

// clickhouseMigrate creates the logs, mcp_tool_logs and async_jobs tables if
// missing, then reconciles any columns added to the models since. CREATE TABLE
// IF NOT EXISTS is inherently idempotent and concurrency-safe across pods, so
// (unlike the Postgres path) no advisory lock is needed.
func clickhouseMigrate(ctx context.Context, db *gorm.DB, cluster string, retentionDays int, logger schemas.Logger) error {
	logsTTL := ""
	if retentionDays >= 1 {
		logsTTL = fmt.Sprintf("toDateTime(created_at) + INTERVAL %d DAY", retentionDays)
	}

	logger.Info("[logstore] clickhouse: creating table logs")
	if err := clickhouseCreateTable(ctx, db, &Log{}, chTableOpts{
		table:       "logs",
		partitionBy: "toYYYYMM(timestamp)",
		orderBy:     "(timestamp, id)",
		ttl:         logsTTL,
		skipIndexes: []string{
			"INDEX idx_logs_provider provider TYPE bloom_filter GRANULARITY 1",
			"INDEX idx_logs_model model TYPE bloom_filter GRANULARITY 1",
			"INDEX idx_logs_status status TYPE bloom_filter GRANULARITY 1",
			"INDEX idx_logs_team_id team_id TYPE bloom_filter GRANULARITY 1",
			"INDEX idx_logs_virtual_key_id virtual_key_id TYPE bloom_filter GRANULARITY 1",
			"INDEX idx_logs_user_id user_id TYPE bloom_filter GRANULARITY 1",
			"INDEX idx_logs_selected_key_id selected_key_id TYPE bloom_filter GRANULARITY 1",
		},
	}, cluster); err != nil {
		return fmt.Errorf("clickhouse: create logs table: %w", err)
	}

	logger.Info("[logstore] clickhouse: creating table mcp_tool_logs")
	if err := clickhouseCreateTable(ctx, db, &MCPToolLog{}, chTableOpts{
		table:       "mcp_tool_logs",
		partitionBy: "toYYYYMM(timestamp)",
		orderBy:     "(timestamp, id)",
		ttl:         logsTTL,
		skipIndexes: []string{
			"INDEX idx_mcp_logs_status status TYPE bloom_filter GRANULARITY 1",
			"INDEX idx_mcp_logs_virtual_key_id virtual_key_id TYPE bloom_filter GRANULARITY 1",
			"INDEX idx_mcp_logs_tool_name tool_name TYPE bloom_filter GRANULARITY 1",
		},
	}, cluster); err != nil {
		return fmt.Errorf("clickhouse: create mcp_tool_logs table: %w", err)
	}

	// async_jobs is a small queue. A hard 7-day TTL on created_at is a safety
	// backstop; the AsyncJobCleaner's DeleteExpired/DeleteStale handle normal
	// (sub-hour) expiry.
	logger.Info("[logstore] clickhouse: creating table async_jobs")
	if err := clickhouseCreateTable(ctx, db, &AsyncJob{}, chTableOpts{
		table:   "async_jobs",
		orderBy: "id",
		ttl:     "toDateTime(created_at) + INTERVAL 7 DAY",
	}, cluster); err != nil {
		return fmt.Errorf("clickhouse: create async_jobs table: %w", err)
	}

	return nil
}
