package logstore

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestHourlyArchiveRefreshWorkload compares full and incremental aggregation on
// 100,000 rows while asserting exact output parity and bounded selected inputs.
// Timings are diagnostics, not machine-dependent pass/fail thresholds.
func TestHourlyArchiveRefreshWorkload(t *testing.T) {
	if testing.Short() {
		t.Skip("workload check skipped in short mode")
	}
	db, conn := hourlyArchiveTestDB(t)
	ctx := context.Background()
	started := time.Now()
	require.NoError(t, db.Exec(`INSERT INTO logs (id,timestamp,created_at,status,provider,model,object_type,selected_key_id,latency,cost)
	 SELECT n::text, date_trunc('hour',now())-(n%720)*interval '1 hour'+interval '1 minute', now(),
	 'success','openai','model-'||(n%10),'chat_completion','',n%1000,0.01 FROM generate_series(1,100000) n`).Error)
	insertTime := time.Since(started)
	require.NoError(t, refreshHourlyArchive(ctx, conn))
	fullDDL := `CREATE MATERIALIZED VIEW hourly_full_reference AS ` + hourlyRawSelect("true")
	require.NoError(t, db.Exec(fullDDL).Error)
	require.NoError(t, db.Exec(hourlyUniqueIndex("hourly_full_reference", "hourly_full_reference_uniq")).Error)
	started = time.Now()
	require.NoError(t, db.Exec(`REFRESH MATERIALIZED VIEW CONCURRENTLY hourly_full_reference`).Error)
	fullTime := time.Since(started)
	started = time.Now()
	require.NoError(t, refreshHourlyArchive(ctx, conn))
	incrementalTime := time.Since(started)
	var differences, selectedRows int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM (
	 (SELECT * FROM hourly_full_reference EXCEPT ALL SELECT * FROM mv_logs_hourly)
	 UNION ALL (SELECT * FROM mv_logs_hourly EXCEPT ALL SELECT * FROM hourly_full_reference)) differences`).Scan(&differences).Error)
	require.Zero(t, differences)
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM logs l JOIN bifrost_hourly_run r
	 ON l.timestamp>=r.hour AND l.timestamp<r.hour+interval '1 hour'`).Scan(&selectedRows).Error)
	require.Less(t, selectedRows, int64(1000))
	t.Logf("100000 rows: batched ingestion=%s (%.0f rows/sec), full refresh=%s, snapshot+merge=%s, selected raw rows=%d",
		insertTime, 100000/insertTime.Seconds(), fullTime, incrementalTime, selectedRows)
}
