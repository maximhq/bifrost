package logstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm"
)

// matViewSchedule carries the configured floor and the shared post-pass cooldown.
// The high-water mark persists across replicas and restarts to avoid oscillation.
type matViewSchedule struct {
	floor  time.Duration
	delay  time.Duration
	logger schemas.Logger
}

// ensureMatViewSchedule installs a singleton metadata row under the maintenance lock.
func ensureMatViewSchedule(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS bifrost_matview_schedule (
	 singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
	 cooldown_ns bigint NOT NULL DEFAULT 0 CHECK (cooldown_ns >= 0),
	 last_duration_ns bigint NOT NULL DEFAULT 0,
	 last_finished_at timestamptz,
	 next_refresh_at timestamptz NOT NULL DEFAULT '-infinity'
	);
	INSERT INTO bifrost_matview_schedule (singleton) VALUES (true) ON CONFLICT DO NOTHING`)
	return err
}

// tunedMatViewDelay raises the cooldown to twice an over-budget pass's duration.
// Fast passes and errors that finish quickly cannot lower an established delay.
func tunedMatViewDelay(current, elapsed time.Duration) time.Duration {
	if elapsed <= current {
		return current
	}
	const maxDuration = time.Duration(1<<63 - 1)
	if elapsed > maxDuration/2 {
		return maxDuration
	}
	return 2 * elapsed
}

// begin reads the database clock and shared cooldown while holding the refresh lock.
// Other replicas may poll, but cannot refresh until the previous pass has rested.
func (s *matViewSchedule) begin(ctx context.Context, conn *sql.Conn) (bool, error) {
	var cooldown int64
	var remaining float64
	err := conn.QueryRowContext(ctx, `SELECT cooldown_ns,
	 CASE WHEN next_refresh_at>clock_timestamp()
	 THEN EXTRACT(EPOCH FROM next_refresh_at-clock_timestamp()) ELSE 0 END
	 FROM bifrost_matview_schedule WHERE singleton`).Scan(&cooldown, &remaining)
	if err != nil {
		return false, err
	}
	s.delay = max(s.floor, time.Duration(cooldown))
	if remaining > 0 {
		s.delay = max(time.Millisecond, time.Duration(remaining*float64(time.Second)))
		return false, nil
	}
	// Reserve a cooldown before work starts. Cancellation can discard the physical
	// connection, preventing finish from persisting its measurement. The lease
	// still prevents a second replica from immediately retrying a timed-out pass.
	budget := maxMatViewRefreshTimeout
	if deadline, ok := ctx.Deadline(); ok {
		budget = max(time.Millisecond, time.Until(deadline))
	}
	_, err = conn.ExecContext(ctx, `UPDATE bifrost_matview_schedule SET
	 next_refresh_at=clock_timestamp()+$1*interval '1 second' WHERE singleton`,
		max(s.delay, 3*budget).Seconds())
	return err == nil, err
}

// finish persists the next eligible time even when the refresh exceeded its budget.
// A separate bounded context records timeout backoff before releasing the lock.
func (s *matViewSchedule) finish(ctx context.Context, conn *sql.Conn, elapsed time.Duration) error {
	previous := s.delay
	s.delay = tunedMatViewDelay(s.delay, elapsed)
	if s.delay > previous && s.logger != nil {
		s.logger.Warn(fmt.Sprintf("logstore: matview refresh took %s; increasing shared refresh cooldown from %s to %s",
			elapsed.Round(time.Millisecond), previous, s.delay.Round(time.Millisecond)))
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), matViewUnlockTimeout)
	defer cancel()
	_, err := conn.ExecContext(writeCtx, `UPDATE bifrost_matview_schedule SET cooldown_ns=$1,
	 last_duration_ns=$2, last_finished_at=clock_timestamp(),
	 next_refresh_at=clock_timestamp()+$3*interval '1 second' WHERE singleton`,
		int64(s.delay), int64(elapsed), s.delay.Seconds())
	if err != nil {
		return fmt.Errorf("persist matview refresh cooldown: %w", err)
	}
	return nil
}

// refreshScheduledMatViews measures the first and subsequent passes identically.
// The returned delay also protects this process if persisting the cooldown fails.
func refreshScheduledMatViews(ctx context.Context, db *gorm.DB, interval time.Duration, logger schemas.Logger) (time.Duration, error) {
	schedule := &matViewSchedule{floor: interval, delay: interval, logger: logger}
	err := runMatViewRefresh(ctx, db, schedule)
	return schedule.delay, err
}
