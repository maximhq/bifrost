package logstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// hourlyRawSelect returns the canonical aggregate with an indexed hour filter.
// Keep the aggregate projection shared with the legacy view so new metrics
// cannot silently disappear from incremental refreshes.
func hourlyRawSelect(predicate string) string {
	query := strings.TrimPrefix(mvLogsHourlyDDL, "\nCREATE MATERIALIZED VIEW IF NOT EXISTS mv_logs_hourly AS\n")
	query = strings.Replace(query, "date_trunc('hour', timestamp)", "bifrost_hourly_bucket(timestamp)", 1)
	return strings.Replace(query, "WHERE status IN ('success', 'error', 'cancelled')",
		"WHERE status IN ('success', 'error', 'cancelled') AND ("+predicate+")", 1)
}

// hourlyMergeSelect replaces every dimension row in selected hours, including
// hours now empty, while retaining all other snapshot rows verbatim.
func hourlyMergeSelect() string {
	// Drive raw reads from selected hours. The lateral boundary prevents the
	// planner from turning a tiny refresh manifest into a scan of all raw logs.
	// OFFSET 0 preserves the parameterized range scan when flattening subqueries.
	raw := strings.Replace(hourlyRawSelect("true"), "FROM logs\n", `FROM bifrost_hourly_run r
	 CROSS JOIN LATERAL (SELECT * FROM logs
	 WHERE timestamp >= r.hour AND timestamp < r.hour + interval '1 hour' OFFSET 0) logs
`, 1)
	return `SELECT s.* FROM mv_logs_hourly_snapshot s
	 WHERE NOT EXISTS (SELECT 1 FROM bifrost_hourly_run r WHERE r.hour=s.hour)
	 UNION ALL ` + raw
}

// installHourlySnapshot creates an independently typed snapshot reader. Its
// dynamic SELECT deliberately has no catalog dependency on mv_logs_hourly:
// the final matview depends on the snapshot, never the other way around.
func installHourlySnapshot(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT attname, format_type(atttypid, atttypmod)
	 FROM pg_attribute WHERE attrelid='mv_logs_hourly'::regclass AND attnum>0 AND NOT attisdropped ORDER BY attnum`)
	if err != nil {
		return err
	}
	var fields, names []string
	for rows.Next() {
		var name, kind string
		if err := rows.Scan(&name, &kind); err != nil {
			rows.Close()
			return err
		}
		fields = append(fields, hourlyIdent(name)+" "+kind)
		names = append(names, hourlyIdent(name))
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(fields) == 0 {
		return fmt.Errorf("hourly snapshot source is missing")
	}
	var schema string
	if err := tx.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		return err
	}
	selectSQL := "SELECT " + strings.Join(names, ",") + " FROM " + hourlyIdent(schema) + `.mv_logs_hourly`
	function := `CREATE OR REPLACE FUNCTION bifrost_hourly_snapshot_source() RETURNS TABLE (` + strings.Join(fields, ",") + `)
	 LANGUAGE plpgsql STABLE SECURITY INVOKER SET search_path = pg_catalog AS $body$
	 BEGIN RETURN QUERY EXECUTE '` + strings.ReplaceAll(selectSQL, "'", "''") + `'; END $body$`
	if _, err := tx.ExecContext(ctx, function); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `CREATE MATERIALIZED VIEW mv_logs_hourly_snapshot AS SELECT * FROM bifrost_hourly_snapshot_source()`)
	return err
}

// hourlyUniqueIndex creates an index on a new, transaction-private matview.
func hourlyUniqueIndex(view, index string) string {
	ddl := strings.Replace(mvLogsHourlyUniqueIdx, "CONCURRENTLY ", "", 1)
	ddl = strings.ReplaceAll(ddl, "mv_logs_hourly_uniq", index)
	return strings.Replace(ddl, "ON mv_logs_hourly (", "ON "+view+" (", 1)
}

// initializeHourlyArchive converts an existing hourly matview without discarding
// its historical baseline. The caller must hold the maintenance advisory lock.
// preserveBaseline is false only when the source view was just built from logs.
func initializeHourlyArchive(ctx context.Context, conn *sql.Conn, preserveBaseline bool) error {
	if exists, err := hourlyStateExists(ctx, conn); err != nil {
		return err
	} else if exists {
		return validateHourlyArchive(ctx, conn)
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := ensureHourlyState(ctx, tx); err != nil {
		return err
	}
	if err := installHourlySnapshot(ctx, tx); err != nil {
		return fmt.Errorf("snapshot legacy hourly aggregates: %w", err)
	}
	if _, err := tx.ExecContext(ctx, hourlyUniqueIndex("mv_logs_hourly_snapshot", "mv_logs_hourly_snapshot_uniq")); err != nil {
		return err
	}
	if preserveBaseline {
		// A legacy view has no raw-completeness ledger. Preserve older hours,
		// and any recent hour whose raw population is smaller than the snapshot.
		_, err = tx.ExecContext(ctx, `INSERT INTO bifrost_hourly_hours AS h (hour, frozen, applied_generation, freeze_generation)
		 SELECT s.hour, true, 1, 1 FROM mv_logs_hourly_snapshot s GROUP BY s.hour
		 HAVING s.hour < bifrost_hourly_bucket(now()) - interval '1 hour'
		 OR SUM(s.count) > (SELECT COUNT(*) FROM logs l WHERE l.timestamp>=s.hour
		 AND l.timestamp<s.hour+interval '1 hour' AND l.status IN ('success','error','cancelled'))
		 ON CONFLICT (hour) DO UPDATE SET frozen=true, applied_generation=h.generation, freeze_generation=1`)
		if err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO bifrost_hourly_run SELECT hour,generation FROM bifrost_hourly_hours WHERE NOT frozen`); err != nil {
		return err
	}
	// The snapshot's function does not depend on this relation, so RESTRICT is
	// sufficient. Unexpected external dependencies abort instead of cascading.
	if _, err := tx.ExecContext(ctx, `DROP MATERIALIZED VIEW mv_logs_hourly`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `CREATE MATERIALIZED VIEW mv_logs_hourly AS `+hourlyMergeSelect()); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, hourlyUniqueIndex("mv_logs_hourly", "mv_logs_hourly_uniq")); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE bifrost_hourly_hours h SET applied_generation=r.generation
	 FROM bifrost_hourly_run r WHERE h.hour=r.hour;
	 UPDATE bifrost_hourly_refresh SET initialized=true, published_generation=1, refreshed_at=clock_timestamp()`); err != nil {
		return err
	}
	if err := copyHourlySnapshot(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// validateHourlyArchive refuses lossy schema repair and missing archive copies.
// Recovery of a missing published view is handled separately, before snapshotting.
func validateHourlyArchive(ctx context.Context, db hourlySQL) error {
	var initialized bool
	var version int
	if err := db.QueryRowContext(ctx, `SELECT initialized,schema_version FROM bifrost_hourly_refresh WHERE singleton`).Scan(&initialized, &version); err != nil {
		return err
	}
	if !initialized || version != 1 {
		return fmt.Errorf("hourly archive state is incomplete or incompatible; preserve it for recovery")
	}
	for _, view := range []string{"mv_logs_hourly", "mv_logs_hourly_snapshot"} {
		var populated bool
		if err := db.QueryRowContext(ctx, `SELECT relispopulated FROM pg_class WHERE oid=to_regclass($1) AND relkind='m'`, view).Scan(&populated); err != nil || !populated {
			return fmt.Errorf("hourly archive %s is missing or unpopulated; restore the archive before refreshing", view)
		}
		var columns int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pg_attribute
		 WHERE attrelid=to_regclass($1) AND attnum>0 AND NOT attisdropped AND attname=ANY($2)`, view, mvLogsHourlyRequiredColumns).Scan(&columns); err != nil {
			return err
		}
		if columns != len(mvLogsHourlyRequiredColumns) {
			return fmt.Errorf("hourly archive %s requires a history-preserving schema migration", view)
		}
	}
	return nil
}

// copyHourlySnapshot records the exact generation copied in the same transaction.
// The caller has already checked that the published view is safe to copy.
func copyHourlySnapshot(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `REFRESH MATERIALIZED VIEW mv_logs_hourly_snapshot`); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE bifrost_hourly_refresh SET snapshot_generation=published_generation`)
	return err
}

// refreshHourlyArchive publishes a complete merged generation atomically. Writes
// after manifest capture remain dirty because only captured versions are applied.
// The caller holds the session advisory lock throughout the operation.
func refreshHourlyArchive(ctx context.Context, conn *sql.Conn) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateHourlyArchive(ctx, conn); err != nil {
		return err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := copyHourlySnapshot(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM bifrost_hourly_run;
	 INSERT INTO bifrost_hourly_run SELECT hour,generation FROM bifrost_hourly_hours
	 WHERE NOT frozen AND (generation>applied_generation OR hour>=bifrost_hourly_bucket(now())-interval '1 hour')`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `REFRESH MATERIALIZED VIEW CONCURRENTLY mv_logs_hourly`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE bifrost_hourly_hours h SET applied_generation=r.generation
	 FROM bifrost_hourly_run r WHERE h.hour=r.hour;
	 UPDATE bifrost_hourly_refresh SET published_generation=published_generation+1,refreshed_at=clock_timestamp()`); err != nil {
		return err
	}
	return tx.Commit()
}

// recoverHourlyArchive restores a missing copy without ever replacing frozen
// history with raw logs. It is called only under the maintenance advisory lock.
func recoverHourlyArchive(ctx context.Context, conn *sql.Conn) error {
	var final, snapshot bool
	if err := conn.QueryRowContext(ctx, `SELECT to_regclass('mv_logs_hourly') IS NOT NULL,
	 to_regclass('mv_logs_hourly_snapshot') IS NOT NULL`).Scan(&final, &snapshot); err != nil {
		return err
	}
	if final && snapshot {
		return validateHourlyArchive(ctx, conn)
	}
	if !final && !snapshot {
		return fmt.Errorf("both hourly archive copies are missing; restore from backup")
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if final {
		if err := installHourlySnapshot(ctx, tx); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, hourlyUniqueIndex("mv_logs_hourly_snapshot", "mv_logs_hourly_snapshot_uniq")); err != nil {
			return err
		}
	} else {
		var safe bool
		if err := tx.QueryRowContext(ctx, `SELECT NOT EXISTS (SELECT 1 FROM bifrost_hourly_hours h
		 CROSS JOIN bifrost_hourly_refresh s WHERE h.frozen AND h.freeze_generation>s.snapshot_generation)`).Scan(&safe); err != nil {
			return err
		}
		if !safe {
			return fmt.Errorf("hourly snapshot does not cover frozen history; restore from backup")
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM bifrost_hourly_run;
		 INSERT INTO bifrost_hourly_run SELECT hour,generation FROM bifrost_hourly_hours WHERE NOT frozen`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `CREATE MATERIALIZED VIEW mv_logs_hourly AS `+hourlyMergeSelect()); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, hourlyUniqueIndex("mv_logs_hourly", "mv_logs_hourly_uniq")); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE bifrost_hourly_hours h SET applied_generation=r.generation
		 FROM bifrost_hourly_run r WHERE h.hour=r.hour;
		 UPDATE bifrost_hourly_refresh SET published_generation=published_generation+1,refreshed_at=clock_timestamp()`); err != nil {
			return err
		}
	}
	if err := copyHourlySnapshot(ctx, tx); err != nil {
		return err
	}
	if err := validateHourlyArchive(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}
