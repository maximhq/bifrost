package logstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// pressureMetrics reports top-level SQL pressure without double-counting nested
// statements. Execution time is server elapsed time, not a CPU utilization metric.
type pressureMetrics struct {
	WallMS        float64 `json:"wall_ms"`
	ExecMS        float64 `json:"exec_ms"`
	SharedHits    int64   `json:"shared_hits"`
	SharedReads   int64   `json:"shared_reads"`
	SharedDirtied int64   `json:"shared_dirtied"`
	TempWritten   int64   `json:"temp_written_blocks"`
	WALBytes      int64   `json:"wal_bytes"`
}

// pressureFixture owns one isolated schema and its pinned maintenance connection.
type pressureFixture struct {
	db     *gorm.DB
	conn   *sql.Conn
	schema string
}

// pressureResult records a reproducible workload phase and all-column accuracy.
type pressureResult struct {
	Rows  int    `json:"rows"`
	Phase string `json:"phase"`
	Mode  string `json:"mode"`
	Trial int    `json:"trial"`
	pressureMetrics
	Differences      int64   `json:"differences"`
	ExpectedRequests int64   `json:"expected_requests,omitempty"`
	ActualRequests   int64   `json:"actual_requests,omitempty"`
	ExpectedCost     float64 `json:"expected_cost,omitempty"`
	ActualCost       float64 `json:"actual_cost,omitempty"`
	CooldownSeconds  float64 `json:"cooldown_seconds,omitempty"`
	DurationSeconds  float64 `json:"duration_seconds,omitempty"`
	RawRowsVisited   int64   `json:"raw_rows_visited,omitempty"`
}

// newPressureFixture creates equivalent raw schemas without touching live data.
func newPressureFixture(t *testing.T, admin *gorm.DB, dsn string, archive bool) pressureFixture {
	t.Helper()
	schema := "pressure_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, admin.Exec("CREATE SCHEMA "+hourlyIdent(schema)).Error)
	db, err := gorm.Open(postgres.Open(dsn+" search_path="+schema+",public"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = sqlDB.Close()
		require.NoError(t, admin.Exec("DROP SCHEMA "+hourlyIdent(schema)+" CASCADE").Error)
	})
	require.NoError(t, db.AutoMigrate(&Log{}))
	// AutoMigrate supplies the same timestamp and dimension indexes in both modes.
	require.NoError(t, ensureMatViews(context.Background(), db, archive))
	conn, err := sqlDB.Conn(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return pressureFixture{db: db, conn: conn, schema: schema}
}

// measurePressure resets counters only in the explicitly supplied benchmark DB.
// The caller must provide an otherwise idle database with pg_stat_statements.
func measurePressure(t *testing.T, admin *gorm.DB, work func()) pressureMetrics {
	t.Helper()
	require.NoError(t, admin.Exec(`SELECT public.pg_stat_statements_reset(0,(SELECT oid FROM pg_database WHERE datname=current_database()),0)`).Error)
	start := time.Now()
	work()
	wallMS := float64(time.Since(start)) / float64(time.Millisecond)
	var metrics pressureMetrics
	require.NoError(t, admin.Raw(`SELECT COALESCE(SUM(total_exec_time),0) AS exec_ms,
	 COALESCE(SUM(shared_blks_hit),0) AS shared_hits, COALESCE(SUM(shared_blks_read),0) AS shared_reads,
	 COALESCE(SUM(shared_blks_dirtied),0) AS shared_dirtied,
	 COALESCE(SUM(temp_blks_written),0) AS temp_written, COALESCE(SUM(wal_bytes),0) AS wal_bytes
	 FROM public.pg_stat_statements WHERE dbid=(SELECT oid FROM pg_database WHERE datname=current_database())
	 AND toplevel AND query NOT LIKE '%pg_stat_statements%' AND query NOT LIKE '%pg_stat_activity%'`).Scan(&metrics).Error)
	metrics.WallMS = wallMS
	t.Logf("pressure: wall=%.1fms server=%.1fms hits=%d reads=%d temp=%d WAL=%d",
		metrics.WallMS, metrics.ExecMS, metrics.SharedHits, metrics.SharedReads, metrics.TempWritten, metrics.WALBytes)
	return metrics
}

// insertPressureRows distributes terminal requests over 30 days and ten models.
// Binary-exact costs allow strict equality checks without floating summation noise.
func insertPressureRows(t *testing.T, f pressureFixture, rows int, anchor time.Time) {
	t.Helper()
	require.NoError(t, f.db.Exec(`INSERT INTO logs
	 (id,timestamp,created_at,status,provider,model,object_type,selected_key_id,user_id,latency,
	 overhead_latency,cost,input_cost,output_cost,prompt_tokens,completion_tokens,total_tokens,cached_read_tokens)
	 SELECT n::text, ?::timestamptz-(n%720)*interval '1 hour'+interval '1 minute',
	 ?::timestamptz-(n%720)*interval '1 hour',
	 CASE WHEN n%13=0 THEN 'error' ELSE 'success' END,'openai','model-'||((n/720)%10),
	 'chat_completion','','user-'||((n/720)%10),n%1000,2,0.125,0.0625,0.0625,100,50,150,10
	 FROM generate_series(1,?) n`, anchor, anchor, rows).Error)
}

// refreshPressureHourly performs the production hourly publication for each mode.
func refreshPressureHourly(t *testing.T, f pressureFixture, archive bool) {
	t.Helper()
	if archive {
		require.NoError(t, refreshHourlyArchive(context.Background(), f.conn))
	} else {
		require.NoError(t, f.db.Exec(`REFRESH MATERIALIZED VIEW CONCURRENTLY mv_logs_hourly`).Error)
	}
}

// pressureDifferences compares every aggregate field, including percentiles.
func pressureDifferences(t *testing.T, admin *gorm.DB, left, right string) int64 {
	t.Helper()
	var count int64
	require.NoError(t, admin.Raw(`SELECT COUNT(*) FROM ((SELECT * FROM `+left+` EXCEPT ALL SELECT * FROM `+right+`)
	 UNION ALL (SELECT * FROM `+right+` EXCEPT ALL SELECT * FROM `+left+`)) diff`).Scan(&count).Error)
	return count
}

// TestMatViewPressureComparison is an opt-in PostgreSQL pressure and accuracy
// experiment. It requires a disposable, idle database and never resets live stats.
func TestMatViewPressureComparison(t *testing.T) {
	dsn := os.Getenv("BIFROST_MATVIEW_PRESSURE_DSN")
	if dsn == "" {
		t.Skip("set BIFROST_MATVIEW_PRESSURE_DSN to an isolated pg_stat_statements database")
	}
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	adminSQL, err := admin.DB()
	require.NoError(t, err)
	defer adminSQL.Close()
	var enabled bool
	require.NoError(t, admin.Raw(`SELECT EXISTS(SELECT 1 FROM pg_extension WHERE extname='pg_stat_statements')`).Scan(&enabled).Error)
	require.True(t, enabled, "enable pg_stat_statements in the disposable database first")
	var results []pressureResult
	anchor := time.Now().UTC().Truncate(time.Hour)
	rowCounts := []int{100000, 1000000}
	if os.Getenv("BIFROST_MATVIEW_PRESSURE_SMOKE") == "1" {
		rowCounts = []int{1000}
	}
	for _, rows := range rowCounts {
		t.Run(fmt.Sprint(rows), func(t *testing.T) {
			fixtures := []pressureFixture{newPressureFixture(t, admin, dsn, false), newPressureFixture(t, admin, dsn, true)}
			modes := []string{"full", "snapshot_merge"}
			for i, f := range fixtures {
				m := measurePressure(t, admin, func() { insertPressureRows(t, f, rows, anchor) })
				results = append(results, pressureResult{Rows: rows, Phase: "bulk_ingest", Mode: modes[i], pressureMetrics: m})
				require.NoError(t, f.db.Exec(`ANALYZE logs`).Error)
				refreshPressureHourly(t, f, i == 1)
			}
			left := hourlyIdent(fixtures[0].schema) + ".mv_logs_hourly"
			right := hourlyIdent(fixtures[1].schema) + ".mv_logs_hourly"
			require.Zero(t, pressureDifferences(t, admin, left, right))
			// Distinguish bootstrap cost from ongoing, batched current-hour writes.
			for i, f := range fixtures {
				m := measurePressure(t, admin, func() {
					for batch := 0; batch < 100; batch++ {
						require.NoError(t, f.db.Exec(`INSERT INTO logs
						 (id,timestamp,created_at,status,provider,model,object_type,selected_key_id,user_id,latency,cost)
						 SELECT 'recent-'||n, ?::timestamptz+interval '5 minutes',?::timestamptz,
						 'success','openai','model-'||(n%10),'chat_completion','','user-'||(n%10),n%1000,0.125
						 FROM generate_series(?::int,?::int) n`, anchor, anchor, batch*100+1, (batch+1)*100).Error)
					}
				})
				results = append(results, pressureResult{Rows: rows, Phase: "recent_ingest_100x100", Mode: modes[i], pressureMetrics: m})
				refreshPressureHourly(t, f, i == 1)
			}
			require.Zero(t, pressureDifferences(t, admin, left, right))
			for trial := 0; trial < 3; trial++ {
				// Alternate measurement order to reduce warm-cache/order bias. A dirty
				// recent hour is processed in every pass in both modes.
				for _, i := range []int{trial % 2, 1 - trial%2} {
					f := fixtures[i]
					require.NoError(t, f.db.Exec(`UPDATE logs SET cost=cost+0.125 WHERE id='720'`).Error)
					m := measurePressure(t, admin, func() { refreshPressureHourly(t, f, i == 1) })
					results = append(results, pressureResult{Rows: rows, Phase: "hourly_refresh", Mode: modes[i], Trial: trial, pressureMetrics: m})
				}
				require.Zero(t, pressureDifferences(t, admin, left, right))
			}
			// Measure unchanged filter work alongside hourly work for the real pass.
			for trial := 0; trial < 3; trial++ {
				for _, i := range []int{trial % 2, 1 - trial%2} {
					f := fixtures[i]
					require.NoError(t, f.db.Exec(`UPDATE logs SET cost=cost+0.125 WHERE id='720'`).Error)
					resetTestMatViewRefreshGate()
					m := measurePressure(t, admin, func() { require.NoError(t, refreshMatViews(context.Background(), f.db)) })
					results = append(results, pressureResult{Rows: rows, Phase: "whole_pass", Mode: modes[i], Trial: trial, pressureMetrics: m})
				}
				require.Zero(t, pressureDifferences(t, admin, left, right))
			}
			// Verify the actual plan avoids unrelated historical raw tuples.
			visited := hourlyPlanRawRows(t, fixtures[1].db)
			require.Less(t, visited, int64(15000))
			results = append(results, pressureResult{Rows: rows, Phase: "raw_scan_plan", Mode: "snapshot_merge", RawRowsVisited: visited})
			// Exercise the production scheduler at its supported five-second floor.
			resetTestMatViewRefreshGate()
			adaptive := fixtures[1]
			var cooldown time.Duration
			m := measurePressure(t, admin, func() {
				var err error
				cooldown, err = refreshScheduledMatViews(context.Background(), adaptive.db, 5*time.Second, testLogger{})
				require.NoError(t, err)
			})
			var duration int64
			require.NoError(t, adaptive.db.Raw(`SELECT last_duration_ns FROM bifrost_matview_schedule`).Scan(&duration).Error)
			require.Equal(t, tunedMatViewDelay(5*time.Second, time.Duration(duration)), cooldown)
			results = append(results, pressureResult{Rows: rows, Phase: "adaptive_pass_5s", Mode: "snapshot_merge", pressureMetrics: m,
				CooldownSeconds: cooldown.Seconds(), DurationSeconds: time.Duration(duration).Seconds()})
			resetTestMatViewRefreshGate()
			m = measurePressure(t, admin, func() {
				_, err := refreshScheduledMatViews(context.Background(), adaptive.db, 5*time.Second, testLogger{})
				require.NoError(t, err)
			})
			results = append(results, pressureResult{Rows: rows, Phase: "cooldown_poll", Mode: "snapshot_merge", pressureMetrics: m})
			require.Zero(t, pressureDifferences(t, admin, left, right))
			// Preserve a pre-expiry reference, then exercise the production cleaner.
			for i, f := range fixtures {
				require.NoError(t, f.db.Exec(`CREATE MATERIALIZED VIEW expected_before_expiry AS SELECT * FROM mv_logs_hourly`).Error)
				store := &RDBLogStore{db: f.db, hourlyArchiveRequested: i == 1}
				m := measurePressure(t, admin, func() {
					var total int64
					for {
						n, err := store.DeleteLogsBatch(context.Background(), anchor.Add(-7*24*time.Hour), 5000)
						require.NoError(t, err)
						total += n
						if n == 0 {
							break
						}
					}
					require.Positive(t, total)
					refreshPressureHourly(t, f, i == 1)
				})
				diff := pressureDifferences(t, admin, hourlyIdent(f.schema)+".expected_before_expiry", hourlyIdent(f.schema)+".mv_logs_hourly")
				if i == 1 {
					require.Zero(t, diff)
				} else {
					require.Positive(t, diff)
				}
				result := pressureResult{Rows: rows, Phase: "retention_and_refresh", Mode: modes[i], pressureMetrics: m, Differences: diff}
				var totals struct {
					ExpectedRequests, ActualRequests int64
					ExpectedCost, ActualCost         float64
				}
				require.NoError(t, f.db.Raw(`SELECT (SELECT SUM(count) FROM expected_before_expiry) AS expected_requests,
				 (SELECT SUM(count) FROM mv_logs_hourly) AS actual_requests,
				 (SELECT SUM(total_cost) FROM expected_before_expiry) AS expected_cost,
				 (SELECT SUM(total_cost) FROM mv_logs_hourly) AS actual_cost`).Scan(&totals).Error)
				result.ExpectedRequests, result.ActualRequests = totals.ExpectedRequests, totals.ActualRequests
				result.ExpectedCost, result.ActualCost = totals.ExpectedCost, totals.ActualCost
				results = append(results, result)
			}
		})
	}
	data, err := json.MarshalIndent(results, "", "  ")
	require.NoError(t, err)
	if path := os.Getenv("BIFROST_MATVIEW_PRESSURE_OUTPUT"); path != "" {
		require.NoError(t, os.WriteFile(path, append(data, '\n'), 0644))
	}
	t.Log(string(data))
}
