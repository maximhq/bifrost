package logstore

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/migrator"
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
		if ft == reflect.TypeFor[time.Time]() {
			base = "DateTime64(3)"
		}
	}

	if nullable {
		return "Nullable(" + base + ")"
	}
	return base
}

// chColumnOverrides maps column names to full ClickHouse column definitions
// that replace the default type derived from the Go struct. Used for columns
// that need DEFAULT expressions computed by ClickHouse at INSERT time.
var chColumnOverrides = map[string]string{
	// inc_number: monotonically increasing per-row insert-order number.
	// generateSnowflakeID() produces a unique UInt64 for every row in a batch
	// (12-bit counter = 4096/ms), cast to Int64 for Go compatibility.
	// The DEFAULT fires on fresh inserts (where the column is omitted) and is
	// preserved on re-inserts (updates) because the existing value is carried
	// through the read-modify-write cycle.
	"inc_number": "`inc_number` Int64 DEFAULT CAST(generateSnowflakeID() AS Int64)",
}

// clickhouseColumnDefs parses the GORM schema for model and returns column
// definitions ("`name` Type") for every persisted field, in struct order.
func clickhouseColumnDefs(db *gorm.DB, model any) ([]string, error) {
	st, err := schema.Parse(model, &chSchemaCache, db.NamingStrategy)
	if err != nil {
		return nil, fmt.Errorf("failed to parse schema for clickhouse DDL: %w", err)
	}
	var cols []string
	for _, f := range st.Fields {
		if f.DBName == "" || f.IgnoreMigration {
			continue
		}
		if override, ok := chColumnOverrides[f.DBName]; ok {
			cols = append(cols, override)
			continue
		}
		cols = append(cols, fmt.Sprintf("`%s` %s", f.DBName, clickhouseColumnType(f)))
	}
	return cols, nil
}

// chEscapeIdentifier escapes a value for embedding inside a backtick-quoted
// ClickHouse identifier (backticks are escaped by doubling). Used for the
// config-supplied cluster name, which reaches DDL via fmt.Sprintf.
func chEscapeIdentifier(s string) string {
	return strings.ReplaceAll(s, "`", "``")
}

// chTableOpts describes the engine-level options for a ClickHouse table.
type chTableOpts struct {
	table       string
	partitionBy string   // empty = no PARTITION BY
	orderBy     string   // e.g. "(timestamp, id)"
	ttl         string   // empty = no TTL
	skipIndexes []string // full "INDEX ..." clauses
}

type clickHouseSchemaStore interface {
	EnsureClickHouseTable(ctx context.Context, model any, table, partitionBy, orderBy, ttl string, skipIndexes []string) error
}

// clickhouseCreateTable derives the column list from the GORM model, appends the
// ReplacingMergeTree version column (`ver`, defaulted to now64() so every INSERT
// is auto-versioned), and runs an idempotent CREATE TABLE IF NOT EXISTS.
func clickhouseCreateTable(ctx context.Context, db *gorm.DB, model any, opts chTableOpts, cluster string) error {
	cols, err := clickhouseColumnDefs(db, model)
	if err != nil {
		return err
	}
	// Version column for ReplacingMergeTree dedup. Not part of the Go struct, so
	// INSERTs omit it and ClickHouse fills now64(); a later re-insert (cost
	// backfill, has_object flip, idempotent retry) gets a higher ver and wins.
	// Nanosecond precision: with now64()'s default millisecond resolution, a
	// create + immediate update landing in the same millisecond would tie on
	// `ver` and leave the winner to merge order instead of latest-write-wins.
	cols = append(cols, "`ver` DateTime64(9) DEFAULT now64(9)")
	cols = append(cols, opts.skipIndexes...)

	engine := "ReplacingMergeTree(ver)"
	onCluster := ""
	if cluster != "" {
		onCluster = fmt.Sprintf(" ON CLUSTER `%s`", chEscapeIdentifier(cluster))
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

// EnsureClickHouseTable creates an extension-owned table and reconciles model
// columns while preserving the configured cluster and replication settings.
func (s *ClickHouseLogStore) EnsureClickHouseTable(ctx context.Context, model any, table, partitionBy, orderBy, ttl string, skipIndexes []string) error {
	opts := chTableOpts{
		table:       table,
		partitionBy: partitionBy,
		orderBy:     orderBy,
		ttl:         ttl,
		skipIndexes: skipIndexes,
	}
	if err := clickhouseCreateTable(ctx, s.db, model, opts, s.cluster); err != nil {
		return fmt.Errorf("clickhouse: create extension table %s: %w", opts.table, err)
	}
	return clickhouseReconcileColumns(ctx, s.db, model, opts.table, s.cluster, s.logger)
}

// EnsureClickHouseTable delegates extension-table schema management to the
// ClickHouse logstore wrapped by hybrid object storage.
func (h *HybridLogStore) EnsureClickHouseTable(ctx context.Context, model any, table, partitionBy, orderBy, ttl string, skipIndexes []string) error {
	schemaStore, ok := h.inner.(clickHouseSchemaStore)
	if !ok {
		return fmt.Errorf("logstore does not support ClickHouse extension tables")
	}
	return schemaStore.EnsureClickHouseTable(ctx, model, table, partitionBy, orderBy, ttl, skipIndexes)
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
func clickhouseReconcileColumns(ctx context.Context, db *gorm.DB, model any, table, cluster string, logger schemas.Logger) error {
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
		onCluster = fmt.Sprintf(" ON CLUSTER `%s`", chEscapeIdentifier(cluster))
	}
	for _, f := range st.Fields {
		if f.DBName == "" || f.IgnoreMigration {
			continue
		}
		if _, ok := existing[f.DBName]; ok {
			continue
		}
		columnDef := fmt.Sprintf("`%s` %s", f.DBName, clickhouseColumnType(f))
		if override, ok := chColumnOverrides[f.DBName]; ok {
			columnDef = override
		}
		stmt := fmt.Sprintf("ALTER TABLE `%s`%s ADD COLUMN IF NOT EXISTS %s", table, onCluster, columnDef)
		logger.Info("[logstore] clickhouse: adding column %s.%s", table, f.DBName)
		if err := db.WithContext(ctx).Exec(stmt).Error; err != nil {
			return fmt.Errorf("clickhouse: add column %s.%s: %w", table, f.DBName, err)
		}
	}
	return nil
}

// chLogsTTL derives the logs/mcp_tool_logs TTL clause from the configured
// retention. Values < 1 leave TTL unset (the LogsCleaner still prunes via
// DeleteLogsBatch).
func chLogsTTL(retentionDays int) string {
	if retentionDays < 1 {
		return ""
	}
	return fmt.Sprintf("toDateTime(created_at) + INTERVAL %d DAY", retentionDays)
}

// clickhouseMigrationStep is one per-table migration: create the table if
// missing, then reconcile any columns added to its model since.
type clickhouseMigrationStep func(ctx context.Context, db *gorm.DB, cluster string, retentionDays int, logger schemas.Logger) error

// migrationClickHouseLogsTable creates the logs table and reconciles it with
// the Log struct.
func migrationClickHouseLogsTable(ctx context.Context, db *gorm.DB, cluster string, retentionDays int, logger schemas.Logger) error {
	logger.Info("[logstore] clickhouse: creating table logs")
	if err := clickhouseCreateTable(ctx, db, &Log{}, chTableOpts{
		table:       "logs",
		partitionBy: "toYYYYMM(timestamp)",
		orderBy:     "(timestamp, id)",
		ttl:         chLogsTTL(retentionDays),
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
	return clickhouseReconcileColumns(ctx, db, &Log{}, "logs", cluster, logger)
}

// migrationClickHouseMCPToolLogsTable creates the mcp_tool_logs table and
// reconciles it with the MCPToolLog struct.
func migrationClickHouseMCPToolLogsTable(ctx context.Context, db *gorm.DB, cluster string, retentionDays int, logger schemas.Logger) error {
	logger.Info("[logstore] clickhouse: creating table mcp_tool_logs")
	if err := clickhouseCreateTable(ctx, db, &MCPToolLog{}, chTableOpts{
		table:       "mcp_tool_logs",
		partitionBy: "toYYYYMM(timestamp)",
		orderBy:     "(timestamp, id)",
		ttl:         chLogsTTL(retentionDays),
		skipIndexes: []string{
			"INDEX idx_mcp_logs_status status TYPE bloom_filter GRANULARITY 1",
			"INDEX idx_mcp_logs_virtual_key_id virtual_key_id TYPE bloom_filter GRANULARITY 1",
			"INDEX idx_mcp_logs_tool_name tool_name TYPE bloom_filter GRANULARITY 1",
		},
	}, cluster); err != nil {
		return fmt.Errorf("clickhouse: create mcp_tool_logs table: %w", err)
	}
	return clickhouseReconcileColumns(ctx, db, &MCPToolLog{}, "mcp_tool_logs", cluster, logger)
}

// migrationClickHouseAsyncJobsTable creates the async_jobs table and reconciles
// it with the AsyncJob struct. async_jobs is a small queue: a hard 7-day TTL on
// created_at is a safety backstop (independent of the logs retention setting);
// the AsyncJobCleaner's DeleteExpired/DeleteStale handle normal (sub-hour)
// expiry.
func migrationClickHouseAsyncJobsTable(ctx context.Context, db *gorm.DB, cluster string, _ int, logger schemas.Logger) error {
	logger.Info("[logstore] clickhouse: creating table async_jobs")
	if err := clickhouseCreateTable(ctx, db, &AsyncJob{}, chTableOpts{
		table:   "async_jobs",
		orderBy: "id",
		ttl:     "toDateTime(created_at) + INTERVAL 7 DAY",
	}, cluster); err != nil {
		return fmt.Errorf("clickhouse: create async_jobs table: %w", err)
	}
	return clickhouseReconcileColumns(ctx, db, &AsyncJob{}, "async_jobs", cluster, logger)
}

// migrationClickHouseWebhookDeliveriesTable creates the webhook_deliveries
// table and reconciles it with the WebhookDelivery struct. Delivery history
// is insert-only metadata, so it follows the logs retention setting for its
// TTL backstop; DeleteExpiredWebhookDeliveries handles normal expiry from
// each row's expires_at.
func migrationClickHouseWebhookDeliveriesTable(ctx context.Context, db *gorm.DB, cluster string, retentionDays int, logger schemas.Logger) error {
	logger.Info("[logstore] clickhouse: creating table webhook_deliveries")
	if err := clickhouseCreateTable(ctx, db, &WebhookDelivery{}, chTableOpts{
		table:   "webhook_deliveries",
		orderBy: "id",
		ttl:     chLogsTTL(retentionDays),
	}, cluster); err != nil {
		return fmt.Errorf("clickhouse: create webhook_deliveries table: %w", err)
	}
	return clickhouseReconcileColumns(ctx, db, &WebhookDelivery{}, "webhook_deliveries", cluster, logger)
}

// clickhouseMigrationSteps lists the per-table migrations in execution order,
// mirroring logstoreMigrationSteps for the SQL stores.
var clickhouseMigrationSteps = []clickhouseMigrationStep{
	migrationClickHouseLogsTable,
	migrationClickHouseMCPToolLogsTable,
	migrationClickHouseAsyncJobsTable,
	migrationClickHouseWebhookDeliveriesTable,
}

// triggerClickHouseMigrations runs all registered ClickHouse table migrations
// in order. Analogous to triggerMigrations for Postgres/SQLite, but with no
// migration ledger or advisory lock: CREATE TABLE IF NOT EXISTS and ADD COLUMN
// IF NOT EXISTS are inherently idempotent and concurrency-safe across pods.
func triggerClickHouseMigrations(ctx context.Context, db *gorm.DB, cluster string, retentionDays int, logger schemas.Logger) error {
	// No migration ledger exists for ClickHouse, so unlike the SQL stores we
	// cannot verify the schema is current — --no-migrate just trusts the
	// out-of-band --migrate-only run.
	if migrator.SkipStartupMigrations() {
		logger.Info("[logstore] --no-migrate set; skipping clickhouse migration run")
		return nil
	}
	for _, step := range clickhouseMigrationSteps {
		if err := step(ctx, db, cluster, retentionDays, logger); err != nil {
			return err
		}
	}
	return nil
}
