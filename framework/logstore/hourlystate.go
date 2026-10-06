package logstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// hourlySQL is implemented by sql.Conn and sql.Tx. All maintenance statements
// use the connection holding the matview advisory lock, never a pooled session.
type hourlySQL interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// hourlyIdent quotes a PostgreSQL identifier without interpreting its contents.
func hourlyIdent(value string) string { return `"` + strings.ReplaceAll(value, `"`, `""`) + `"` }

// hourlyStateExists reports whether this schema has installed archive bookkeeping.
func hourlyStateExists(ctx context.Context, db hourlySQL) (bool, error) {
	var exists bool
	err := db.QueryRowContext(ctx, `SELECT to_regclass('bifrost_hourly_refresh') IS NOT NULL`).Scan(&exists)
	return exists, err
}

// ensureHourlyState must run in a transaction: trigger installation and the
// initial scan must be atomic with respect to writers. It stores no aggregates.
func ensureHourlyState(ctx context.Context, tx *sql.Tx) error {
	var schema string
	if err := tx.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		return err
	}
	path := hourlyIdent(schema) + ", pg_catalog"
	statements := []string{
		`CREATE TABLE IF NOT EXISTS bifrost_hourly_refresh (
		 singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
		 schema_version integer NOT NULL DEFAULT 1,
		 bucket_timezone text NOT NULL DEFAULT current_setting('TimeZone'),
		 activated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
		 initialized boolean NOT NULL DEFAULT false,
		 published_generation bigint NOT NULL DEFAULT 0,
		 snapshot_generation bigint NOT NULL DEFAULT 0,
		 refreshed_at timestamptz
		)`,
		`INSERT INTO bifrost_hourly_refresh (singleton) VALUES (true) ON CONFLICT DO NOTHING`,
		`CREATE TABLE IF NOT EXISTS bifrost_hourly_hours (
		 hour timestamptz PRIMARY KEY,
		 generation bigint NOT NULL DEFAULT 1,
		 applied_generation bigint NOT NULL DEFAULT 0,
		 frozen boolean NOT NULL DEFAULT false,
		 freeze_generation bigint,
		 ignored_changes bigint NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS bifrost_hourly_dirty ON bifrost_hourly_hours (hour)
		 WHERE NOT frozen AND generation > applied_generation`,
		`CREATE TABLE IF NOT EXISTS bifrost_hourly_run (
		 hour timestamptz PRIMARY KEY, generation bigint NOT NULL
		)`,
		`CREATE OR REPLACE FUNCTION bifrost_hourly_bucket(ts timestamptz) RETURNS timestamptz
		 LANGUAGE sql STABLE SECURITY INVOKER SET search_path = ` + path + ` AS $$
		 SELECT date_trunc('hour', ts, bucket_timezone) FROM bifrost_hourly_refresh WHERE singleton
		 $$`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("create hourly refresh state: %w", err)
		}
	}

	// Compare only aggregation inputs. Payload offloading and other unrelated
	// updates should not repeatedly invalidate expensive percentile calculations.
	columns := []string{"timestamp", "provider", "model", "status", "object_type", "selected_key_id", "virtual_key_id", "routing_rule_id", "user_id", "team_id", "customer_id", "business_unit_id", "project_id", "alias", "canonical_model_name", "user_agent", "app", "latency", "overhead_latency", "prompt_tokens", "completion_tokens", "total_tokens", "cached_read_tokens", "cost", "input_cost", "output_cost", "additional_cost", "cache_debug"}
	var oldColumns, newColumns []string
	for _, column := range columns {
		oldColumns = append(oldColumns, "o."+hourlyIdent(column))
		newColumns = append(newColumns, "n."+hourlyIdent(column))
	}
	changed := "ROW(" + strings.Join(oldColumns, ",") + ") IS DISTINCT FROM ROW(" + strings.Join(newColumns, ",") + ")"
	for _, event := range []struct{ name, references, source string }{
		{"insert", "NEW TABLE AS new_rows", "SELECT timestamp FROM new_rows"},
		{"delete", "OLD TABLE AS old_rows", "SELECT timestamp FROM old_rows"},
		{"update", "OLD TABLE AS old_rows NEW TABLE AS new_rows",
			"SELECT o.timestamp FROM old_rows o LEFT JOIN new_rows n ON o.id = n.id WHERE n.id IS NULL OR " + changed +
				" UNION SELECT n.timestamp FROM new_rows n LEFT JOIN old_rows o ON o.id = n.id WHERE o.id IS NULL OR " + changed},
	} {
		name := "bifrost_hourly_" + event.name
		body := `CREATE OR REPLACE FUNCTION ` + name + `() RETURNS trigger
		 LANGUAGE plpgsql SECURITY INVOKER SET search_path = ` + path + ` AS $$
		 BEGIN
		 INSERT INTO bifrost_hourly_hours AS h (hour)
		 SELECT DISTINCT bifrost_hourly_bucket(timestamp) FROM (` + event.source + `) changed
		 WHERE timestamp IS NOT NULL ORDER BY 1
		 ON CONFLICT (hour) DO UPDATE SET
		 generation = h.generation + CASE WHEN h.frozen THEN 0 ELSE 1 END,
		 ignored_changes = h.ignored_changes + CASE WHEN h.frozen
		 AND current_setting('bifrost.hourly_retention', true) IS DISTINCT FROM 'on' THEN 1 ELSE 0 END;
		 RETURN NULL;
		 END $$`
		if _, err := tx.ExecContext(ctx, body); err != nil {
			return fmt.Errorf("create hourly %s function: %w", event.name, err)
		}
		if _, err := tx.ExecContext(ctx, `CREATE OR REPLACE TRIGGER `+name+` AFTER `+strings.ToUpper(event.name)+
			` ON logs REFERENCING `+event.references+` FOR EACH STATEMENT EXECUTE FUNCTION `+name+`() `); err != nil {
			return fmt.Errorf("create hourly %s trigger: %w", event.name, err)
		}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO bifrost_hourly_hours (hour)
	 SELECT DISTINCT bifrost_hourly_bucket(timestamp) FROM logs ORDER BY 1 ON CONFLICT DO NOTHING`)
	return err
}
