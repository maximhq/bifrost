package logstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// matViewRecordSchema isolates the refresh-record tests from the package's other
// Postgres tests, which drop and recreate the shared views.
const matViewRecordSchema = "logstore_mvrecord_test"

// openMatViewRecordReplica opens a separate pool on the record schema, standing in
// for one replica, or skips the test when Postgres is unreachable.
func openMatViewRecordReplica(t *testing.T, app string) *gorm.DB {
	t.Helper()
	dsn := strings.Replace(postgresDSN, "search_path="+pgTestSchema, "search_path="+matViewRecordSchema, 1) +
		" application_name='" + app + "'"
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Skip("Postgres not available")
	}
	sqlDB, err := db.DB()
	require.NoError(t, err)
	if sqlDB.Ping() != nil {
		t.Skip("Postgres not available")
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// setupMatViewRecord runs the real logstore migrations and matview creation on a
// fresh schema and returns two replicas' pools on it.
func setupMatViewRecord(t *testing.T) (replicaA, replicaB *gorm.DB) {
	t.Helper()
	replicaA = openMatViewRecordReplica(t, "mvrecord-replica-a")
	replicaB = openMatViewRecordReplica(t, "mvrecord-replica-b")
	require.NoError(t, replicaA.Exec("DROP SCHEMA IF EXISTS "+matViewRecordSchema+" CASCADE").Error)
	require.NoError(t, replicaA.Exec("CREATE SCHEMA "+matViewRecordSchema).Error)
	t.Cleanup(func() { _ = replicaA.Exec("DROP SCHEMA IF EXISTS " + matViewRecordSchema + " CASCADE").Error })
	require.NoError(t, triggerMigrations(context.Background(), replicaA, testLogger{}))
	// ensureMatViews skips silently while another session holds the refresh lock
	// (other test packages share this database), so retry until the views exist.
	require.Eventually(t, func() bool {
		if ensureMatViews(context.Background(), replicaA) != nil {
			return false
		}
		var exists bool
		return replicaA.Raw("SELECT to_regclass('mv_logs_hourly') IS NOT NULL").Scan(&exists).Error == nil && exists
	}, 2*time.Minute, 200*time.Millisecond)
	resetTestMatViewRefreshGate()
	t.Cleanup(resetTestMatViewRefreshGate)
	return replicaA, replicaB
}

// matViewRecordRow is one row of matview_refresh_state.
type matViewRecordRow struct {
	Started sql.NullTime
	Success sql.NullTime
	Status  string
	Node    sql.NullString
	Err     sql.NullString
}

// readMatViewRecord returns matview_refresh_state keyed by view.
func readMatViewRecord(t *testing.T, db *gorm.DB) map[string]matViewRecordRow {
	t.Helper()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	rows, err := sqlDB.Query(`SELECT view_name, last_started_at, last_success_at, last_status, last_node, last_error FROM matview_refresh_state`)
	require.NoError(t, err)
	defer rows.Close()
	out := map[string]matViewRecordRow{}
	for rows.Next() {
		var view string
		var r matViewRecordRow
		require.NoError(t, rows.Scan(&view, &r.Started, &r.Success, &r.Status, &r.Node, &r.Err))
		out[view] = r
	}
	require.NoError(t, rows.Err())
	return out
}

// matViewRecordStarts returns last_started_at per view, and the views due for a
// pass with minGap by the same database-clock test refreshMatViews applies. It
// makes no test assertions, so it is safe inside require.Eventually.
func matViewRecordStarts(db *gorm.DB, minGap time.Duration) (starts map[string]time.Time, due []string, err error) {
	sqlDB, err := db.DB()
	if err != nil {
		return nil, nil, err
	}
	rows, err := sqlDB.Query(`SELECT view_name, last_started_at,
		NOT ($1 > 0 AND last_success_at IS NOT NULL AND last_success_at > now() - make_interval(secs => $1))
		FROM matview_refresh_state`, minGap.Seconds())
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	starts = map[string]time.Time{}
	recorded := map[string]bool{}
	for rows.Next() {
		var view string
		var started sql.NullTime
		var isDue bool
		if err := rows.Scan(&view, &started, &isDue); err != nil {
			return nil, nil, err
		}
		recorded[view] = true
		if started.Valid {
			starts[view] = started.Time
		}
		if isDue {
			due = append(due, view)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	for _, view := range matViewRefreshOrder() {
		if !recorded[view] {
			due = append(due, view) // never refreshed, so always due
		}
	}
	return starts, due, nil
}

// refreshAsReplica runs one real refresh pass on db as a separate replica would:
// the in-process activity gate is per process, so it is reset first.
//
// Other test packages share this database and take the same database-scoped
// refresh lock; while one holds it refreshMatViews returns nil without refreshing
// anything. So an attempt only counts once every view that was due before it has
// a newer last_started_at, which only this replica's pass can write on this
// schema. A pass with nothing due is indistinguishable from a skipped one and
// needs no retry.
func refreshAsReplica(t *testing.T, db *gorm.DB, minGap time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	require.Eventually(t, func() bool {
		before, due, err := matViewRecordStarts(db, minGap)
		if err != nil {
			return false
		}
		resetTestMatViewRefreshGate()
		if refreshMatViews(ctx, db, minGap) != nil {
			return false
		}
		after, _, err := matViewRecordStarts(db, minGap)
		if err != nil {
			return false
		}
		for _, view := range due {
			if started, ok := after[view]; !ok || !started.After(before[view]) {
				return false
			}
		}
		var running int64
		return db.Raw("SELECT count(*) FROM matview_refresh_state WHERE last_status = 'running'").Scan(&running).Error == nil && running == 0
	}, time.Minute, 100*time.Millisecond, "this replica's refresh pass never ran")
}

// TestMatViewRefreshRecordSpacesPassesAcrossReplicas reproduces the back-to-back
// refreshing seen on large log stores: each replica has its own ticker, so as soon
// as one finishes a pass the next one's tick starts another. With the record, a
// second replica skips every view the first refreshed within the interval.
func TestMatViewRefreshRecordSpacesPassesAcrossReplicas(t *testing.T) {
	replicaA, replicaB := setupMatViewRecord(t)

	refreshAsReplica(t, replicaA, time.Minute)
	afterA := readMatViewRecord(t, replicaA)
	require.Len(t, afterA, len(matViewRefreshOrder()), "every view must have a record after a full pass")
	for view, r := range afterA {
		require.Equal(t, matViewRefreshStatusSuccess, r.Status, view)
		require.True(t, r.Success.Valid, view)
		require.Equal(t, matViewRefreshNode, r.Node.String, view)
	}

	refreshAsReplica(t, replicaB, time.Minute)
	afterB := readMatViewRecord(t, replicaB)
	for view, r := range afterB {
		require.Equal(t, afterA[view].Started.Time, r.Started.Time, "replica B must not have started a refresh of %s within the interval", view)
		require.Equal(t, afterA[view].Success.Time, r.Success.Time, view)
	}

	t.Run("control_no_gap_refreshes_back_to_back", func(t *testing.T) {
		refreshAsReplica(t, replicaB, 0)
		for view, r := range readMatViewRecord(t, replicaB) {
			require.True(t, r.Success.Time.After(afterA[view].Success.Time), "with no gap %s is refreshed again at once", view)
		}
	})
}

// TestMatViewRefreshRecordGapMeasuredFromFinish pins that the gap runs from the
// last successful finish: once the interval has elapsed since then, the view is
// due again.
func TestMatViewRefreshRecordGapMeasuredFromFinish(t *testing.T) {
	replicaA, replicaB := setupMatViewRecord(t)
	refreshAsReplica(t, replicaA, 2*time.Second)
	first := readMatViewRecord(t, replicaA)

	refreshAsReplica(t, replicaB, 2*time.Second)
	for view, r := range readMatViewRecord(t, replicaB) {
		require.Equal(t, first[view].Success.Time, r.Success.Time, "%s refreshed again inside the gap", view)
	}

	time.Sleep(2500 * time.Millisecond)
	refreshAsReplica(t, replicaB, 2*time.Second)
	for view, r := range readMatViewRecord(t, replicaB) {
		require.True(t, r.Success.Time.After(first[view].Success.Time), "%s must be due once the gap since its finish has passed", view)
	}
}

// TestMatViewRefreshRecordFailedRefreshStaysDue pins that only a success moves
// last_success_at: a refresh that fails (here cut off by its deadline while another
// session holds a lock on logs) is recorded as failed, and the view is refreshed
// on the next pass even though the gap is long.
func TestMatViewRefreshRecordFailedRefreshStaysDue(t *testing.T) {
	replicaA, replicaB := setupMatViewRecord(t)
	require.NoError(t, replicaA.Exec("DELETE FROM matview_refresh_state").Error)

	// Block REFRESH ... CONCURRENTLY of mv_logs_hourly, which reads logs, for
	// longer than the pass's deadline.
	sqlDB, err := replicaA.DB()
	require.NoError(t, err)
	blocker, err := sqlDB.Conn(context.Background())
	require.NoError(t, err)
	_, err = blocker.ExecContext(context.Background(), "BEGIN")
	require.NoError(t, err)
	_, err = blocker.ExecContext(context.Background(), "LOCK TABLE logs IN ACCESS EXCLUSIVE MODE")
	require.NoError(t, err)

	// A pass that finds the shared refresh lock held by another test package
	// returns nil without refreshing, so retry until one actually ran and failed.
	for deadline := time.Now().Add(time.Minute); ; {
		resetTestMatViewRefreshGate()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err = refreshMatViews(ctx, replicaB, time.Minute)
		cancel()
		if err != nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	_, _ = blocker.ExecContext(context.Background(), "ROLLBACK")
	_ = blocker.Close()
	require.Error(t, err, "the blocked refresh must fail at its deadline")

	record := readMatViewRecord(t, replicaB)
	require.Equal(t, matViewRefreshStatusFailed, record["mv_logs_hourly"].Status)
	require.False(t, record["mv_logs_hourly"].Success.Valid, "a failed refresh must not count as a success")
	require.NotEmpty(t, record["mv_logs_hourly"].Err.String)

	// The next pass refreshes the failed view even with a long gap.
	refreshAsReplica(t, replicaA, time.Hour)
	record = readMatViewRecord(t, replicaA)
	require.Equal(t, matViewRefreshStatusSuccess, record["mv_logs_hourly"].Status)
	require.True(t, record["mv_logs_hourly"].Success.Valid)
}

// TestMatViewRepairForgetsRefreshRecord pins that a view dropped and rebuilt by
// the shape repair in ensureMatViews loses its record, so the rebuilt view is not
// skipped because the view it replaced was refreshed recently.
func TestMatViewRepairForgetsRefreshRecord(t *testing.T) {
	replicaA, _ := setupMatViewRecord(t)
	refreshAsReplica(t, replicaA, time.Hour)
	require.Contains(t, readMatViewRecord(t, replicaA), "mv_logs_hourly")

	// An old-shape mv_logs_hourly, as a release with a different definition leaves.
	require.NoError(t, replicaA.Exec("DROP MATERIALIZED VIEW mv_logs_hourly CASCADE").Error)
	require.NoError(t, replicaA.Exec("CREATE MATERIALIZED VIEW mv_logs_hourly AS SELECT 1 AS hour").Error)
	require.Eventually(t, func() bool {
		if ensureMatViews(context.Background(), replicaA) != nil {
			return false
		}
		_, still := readMatViewRecord(t, replicaA)["mv_logs_hourly"]
		return !still
	}, time.Minute, 200*time.Millisecond, "repair must drop the replaced view's refresh record")
}

// TestMatViewRefreshWithoutRecordTableFallsBack pins the fail-open path: with no
// matview_refresh_state table (a database not yet migrated), refreshes run as
// before and nothing errors.
func TestMatViewRefreshWithoutRecordTableFallsBack(t *testing.T) {
	replicaA, _ := setupMatViewRecord(t)
	require.NoError(t, replicaA.Exec("DROP TABLE matview_refresh_state").Error)
	resetTestMatViewRefreshGate()
	require.NoError(t, refreshMatViews(context.Background(), replicaA, time.Minute))
}

// TestMatViewRefreshStateMigrationIsPostgresOnly pins that the migration creates
// the table on Postgres and is a recorded no-op elsewhere.
func TestMatViewRefreshStateMigrationIsPostgresOnly(t *testing.T) {
	replicaA, _ := setupMatViewRecord(t)
	var exists bool
	require.NoError(t, replicaA.Raw("SELECT to_regclass('matview_refresh_state') IS NOT NULL").Scan(&exists).Error)
	require.True(t, exists)

	sqliteDB, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "mvrecord.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, migrationAddMatViewRefreshStateTable(context.Background(), sqliteDB, testLogger{}))
	require.False(t, sqliteDB.Migrator().HasTable("matview_refresh_state"))
}
