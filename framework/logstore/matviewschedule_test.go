package logstore

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestTunedMatViewDelay checks overload headroom, stable cooldowns, and overflow.
func TestTunedMatViewDelay(t *testing.T) {
	require.Equal(t, time.Minute, tunedMatViewDelay(time.Minute, 10*time.Second))
	require.Equal(t, time.Minute, tunedMatViewDelay(time.Minute, time.Minute))
	require.Equal(t, 150*time.Second, tunedMatViewDelay(time.Minute, 75*time.Second))
	require.Equal(t, 150*time.Second, tunedMatViewDelay(150*time.Second, time.Second))
	require.Equal(t, time.Duration(1<<63-1), tunedMatViewDelay(time.Second, time.Duration(1<<63-1)))
}

// TestMatViewScheduleSharedCooldown checks first-pass tuning, replica handoff,
// restart persistence, timeout recording, and the configured minimum interval.
func TestMatViewScheduleSharedCooldown(t *testing.T) {
	_, db := hourlyTestDB(t)
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, lockHourlyMaintenance(ctx, conn))
	defer unlockHourlyMaintenance(ctx, conn)
	require.NoError(t, ensureMatViewSchedule(ctx, conn))
	first := &matViewSchedule{floor: time.Minute}
	due, err := first.begin(ctx, conn)
	require.NoError(t, err)
	require.True(t, due)
	// A cancelled refresh still persists the measured cooldown.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	require.NoError(t, first.finish(cancelled, conn, 75*time.Second))
	require.Equal(t, 150*time.Second, first.delay)
	replica := &matViewSchedule{floor: time.Minute}
	due, err = replica.begin(ctx, conn)
	require.NoError(t, err)
	require.False(t, due)
	require.InDelta(t, 150, replica.delay.Seconds(), 2)
	_, err = conn.ExecContext(ctx, `UPDATE bifrost_matview_schedule SET next_refresh_at=clock_timestamp()-interval '1 second'`)
	require.NoError(t, err)
	due, err = replica.begin(ctx, conn)
	require.NoError(t, err)
	require.True(t, due)
	require.Equal(t, 150*time.Second, replica.delay)
	require.NoError(t, replica.finish(ctx, conn, time.Second))
	_, err = conn.ExecContext(ctx, `UPDATE bifrost_matview_schedule SET next_refresh_at='-infinity'`)
	require.NoError(t, err)
	replica.floor = 5 * time.Minute
	due, err = replica.begin(ctx, conn)
	require.NoError(t, err)
	require.True(t, due)
	require.Equal(t, 5*time.Minute, replica.delay)
}

// TestScheduledMatViewRefreshSkipsOtherReplica proves the shared cooldown is
// enforced by the real refresh path even with dirty archive work queued.
func TestScheduledMatViewRefreshSkipsOtherReplica(t *testing.T) {
	db, conn := hourlyArchiveTestDB(t)
	ctx := context.Background()
	require.NoError(t, ensureMatViews(ctx, db))
	resetTestMatViewRefreshGate()
	insertHourlyLog(t, db, "before", time.Now(), 1)
	delay, err := refreshScheduledMatViews(ctx, db, time.Nanosecond, testLogger{})
	require.NoError(t, err)
	require.Greater(t, delay, time.Nanosecond)
	var duration int64
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT last_duration_ns FROM bifrost_matview_schedule`).Scan(&duration))
	require.Equal(t, 2*time.Duration(duration), delay)
	// Use a long explicit cooldown so the test does not depend on machine speed.
	_, err = conn.ExecContext(ctx, `UPDATE bifrost_matview_schedule SET next_refresh_at=clock_timestamp()+interval '5 minutes'`)
	require.NoError(t, err)
	insertHourlyLog(t, db, "after", time.Now(), 2)
	_, err = refreshScheduledMatViews(ctx, db, time.Minute, testLogger{})
	require.NoError(t, err)
	var count int64
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT SUM(count) FROM mv_logs_hourly`).Scan(&count))
	require.EqualValues(t, 1, count)
	_, err = conn.ExecContext(ctx, `UPDATE bifrost_matview_schedule SET next_refresh_at='-infinity'`)
	require.NoError(t, err)
	_, err = refreshScheduledMatViews(ctx, db, time.Minute, testLogger{})
	require.NoError(t, err)
	require.NoError(t, conn.QueryRowContext(ctx, `SELECT SUM(count) FROM mv_logs_hourly`).Scan(&count))
	require.EqualValues(t, 2, count)
}

// TestScheduledMatViewTimeoutKeepsCooldown prevents immediate retries even if
// PostgreSQL cancellation discards the connection used to record final timing.
func TestScheduledMatViewTimeoutKeepsCooldown(t *testing.T) {
	db, _ := hourlyArchiveTestDB(t)
	ctx := context.Background()
	require.NoError(t, ensureMatViews(ctx, db))
	insertHourlyLog(t, db, "dirty", time.Now(), 1)
	blocker := db.Begin()
	require.NoError(t, blocker.Error)
	defer blocker.Rollback()
	require.NoError(t, blocker.Exec(`LOCK TABLE logs IN ACCESS EXCLUSIVE MODE`).Error)
	limited, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	_, err := refreshScheduledMatViews(limited, db, time.Minute, testLogger{})
	require.Error(t, err)
	require.NoError(t, blocker.Rollback().Error)
	var future bool
	require.NoError(t, db.Raw(`SELECT next_refresh_at>clock_timestamp() FROM bifrost_matview_schedule`).Scan(&future).Error)
	require.True(t, future)
	_, err = refreshScheduledMatViews(ctx, db, time.Minute, testLogger{})
	require.NoError(t, err)
	var rows int64
	require.NoError(t, db.Table("mv_logs_hourly").Count(&rows).Error)
	require.Zero(t, rows)
}
