package logstore

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

var clickHouseExtensionTableNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var clickHouseReservedTableNames = map[string]struct{}{
	strings.ToLower((AsyncJob{}).TableName()):   {},
	strings.ToLower((Log{}).TableName()):        {},
	strings.ToLower((MCPToolLog{}).TableName()): {},
}

// ClickHouseExtensionTableOptions defines the schema shape for an
// extension-owned table. All DDL fragments are trusted, code-owned values and
// must never be populated from configuration or user input.
type ClickHouseExtensionTableOptions struct {
	Table       string
	PartitionBy string
	OrderBy     string
	TTL         string
	SkipIndexes []string
}

type clickHouseSchemaStore interface {
	EnsureClickHouseTable(ctx context.Context, model any, opts ClickHouseExtensionTableOptions) error
}

var (
	_ clickHouseSchemaStore = (*ClickHouseLogStore)(nil)
	_ clickHouseSchemaStore = (*HybridLogStore)(nil)
)

// EnsureClickHouseTable creates an extension table and reconciles newly added
// model columns. It preserves the configured cluster and replication settings.
func (s *ClickHouseLogStore) EnsureClickHouseTable(ctx context.Context, model any, opts ClickHouseExtensionTableOptions) error {
	if s == nil || s.RDBLogStore == nil || s.db == nil {
		return fmt.Errorf("clickhouse: logstore is not initialized")
	}
	if err := validateClickHouseExtensionTableOptions(opts); err != nil {
		return err
	}

	tableOpts := chTableOpts{
		table:       opts.Table,
		partitionBy: opts.PartitionBy,
		orderBy:     opts.OrderBy,
		ttl:         opts.TTL,
		skipIndexes: append([]string(nil), opts.SkipIndexes...),
	}
	if err := clickhouseCreateTable(ctx, s.db, model, tableOpts, s.cluster); err != nil {
		return fmt.Errorf("clickhouse: create extension table %s: %w", opts.Table, err)
	}
	return clickhouseReconcileColumns(ctx, s.db, model, opts.Table, s.cluster, s.logger)
}

// EnsureClickHouseTable delegates extension-table schema management to a
// ClickHouse logstore wrapped by hybrid object storage.
func (h *HybridLogStore) EnsureClickHouseTable(ctx context.Context, model any, opts ClickHouseExtensionTableOptions) error {
	schemaStore, ok := h.inner.(clickHouseSchemaStore)
	if !ok {
		return fmt.Errorf("logstore does not support ClickHouse extension tables")
	}
	return schemaStore.EnsureClickHouseTable(ctx, model, opts)
}

// validateClickHouseExtensionTableOptions validates identifiers and invariants
// that can be checked without attempting to parse ClickHouse SQL expressions.
func validateClickHouseExtensionTableOptions(opts ClickHouseExtensionTableOptions) error {
	if !clickHouseExtensionTableNamePattern.MatchString(opts.Table) {
		return fmt.Errorf("clickhouse: invalid extension table name %q", opts.Table)
	}
	if _, reserved := clickHouseReservedTableNames[strings.ToLower(opts.Table)]; reserved {
		return fmt.Errorf("clickhouse: extension table name %q is reserved", opts.Table)
	}
	if strings.TrimSpace(opts.OrderBy) == "" {
		return fmt.Errorf("clickhouse: extension table order by is required")
	}
	return nil
}
