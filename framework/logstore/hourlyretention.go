package logstore

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"time"
)

// errHourlyMaintenanceBusy distinguishes contention from an acquisition failure.
var errHourlyMaintenanceBusy = errors.New("hourly maintenance is busy; retention deferred")

// lockHourlyMaintenance obtains the shared matview maintenance lock on conn.
// Acquisition is bounded independently so cancellation cannot leak a session lock.
func lockHourlyMaintenance(ctx context.Context, conn *sql.Conn) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	lockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), matViewUnlockTimeout)
	defer cancel()
	var acquired bool
	if err := conn.QueryRowContext(lockCtx, `SELECT pg_try_advisory_lock($1)`, matviewRefreshAdvisoryLockKey).Scan(&acquired); err != nil {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		return err
	}
	if !acquired {
		return errHourlyMaintenanceBusy
	}
	return nil
}

// unlockHourlyMaintenance releases a session lock even after its operation expires.
func unlockHourlyMaintenance(ctx context.Context, conn *sql.Conn) {
	unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), matViewUnlockTimeout)
	defer cancel()
	if _, err := conn.ExecContext(unlockCtx, `SELECT pg_advisory_unlock($1)`, matviewRefreshAdvisoryLockKey); err != nil {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	}
}

// deleteArchivedLogsBatch seals complete hours before raw retention can remove
// their inputs. The cutoff continues to use created_at, not the aggregate timestamp.
func (s *RDBLogStore) deleteArchivedLogsBatch(ctx context.Context, cutoff time.Time, batchSize int) (int64, error) {
	if batchSize < 1 {
		return 0, fmt.Errorf("retention batch size must be positive")
	}
	sqlDB, err := s.db.DB()
	if err != nil {
		return 0, err
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	if err := lockHourlyMaintenance(ctx, conn); err != nil {
		return 0, err
	}
	defer unlockHourlyMaintenance(ctx, conn)
	if err := recoverHourlyArchive(ctx, conn); err != nil {
		return 0, err
	}
	if !s.matViewMaintenanceDisabled {
		if err := sealExpiringHourlyBuckets(ctx, conn, cutoff, batchSize); err != nil {
			return 0, err
		}
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT set_config('bifrost.hourly_retention','on',true)`); err != nil {
		return 0, err
	}
	// Sealing commits before locking raw rows: an AFTER trigger may already hold
	// a raw row while waiting for the hour lock. Reversing that order deadlocks.
	result, err := tx.ExecContext(ctx, `DELETE FROM logs WHERE id IN (
	 SELECT l.id FROM logs l JOIN bifrost_hourly_hours h ON h.hour=bifrost_hourly_bucket(l.timestamp)
	 WHERE h.frozen AND l.created_at<$1 ORDER BY l.created_at,l.id LIMIT $2 FOR UPDATE OF l SKIP LOCKED
	)`, cutoff, batchSize)
	if err != nil {
		return 0, err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return deleted, nil
}

// sealExpiringHourlyBuckets protects finalized data in both archive copies, then
// freezes only hours whose versions still match. Concurrent changes defer sealing.
func sealExpiringHourlyBuckets(ctx context.Context, conn *sql.Conn, cutoff time.Time, limit int) error {
	rows, err := conn.QueryContext(ctx, `SELECT h.hour FROM bifrost_hourly_hours h
	 WHERE NOT h.frozen AND h.hour < bifrost_hourly_bucket(now())-interval '1 hour'
	 AND EXISTS (SELECT 1 FROM logs l WHERE l.timestamp>=h.hour AND l.timestamp<h.hour+interval '1 hour' AND l.created_at<$1)
	 AND NOT EXISTS (SELECT 1 FROM logs l WHERE l.timestamp>=h.hour AND l.timestamp<h.hour+interval '1 hour'
	 AND l.status NOT IN ('success','error','cancelled')) ORDER BY h.hour LIMIT $2`, cutoff, limit)
	if err != nil {
		return err
	}
	var hours []time.Time
	for rows.Next() {
		var hour time.Time
		if err := rows.Scan(&hour); err != nil {
			rows.Close()
			return err
		}
		hours = append(hours, hour)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(hours) == 0 {
		return err
	}
	if err := refreshHourlyArchive(ctx, conn); err != nil {
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
	for _, hour := range hours {
		// Lock first, then recheck in a new READ COMMITTED statement. A writer
		// that committed while the lock was pending must be visible to the check.
		var locked time.Time
		if err := tx.QueryRowContext(ctx, `SELECT hour FROM bifrost_hourly_hours WHERE hour=$1 FOR UPDATE`, hour).Scan(&locked); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE bifrost_hourly_hours h SET frozen=true,
		 freeze_generation=(SELECT published_generation FROM bifrost_hourly_refresh)
		 WHERE h.hour=$1 AND NOT h.frozen AND h.generation=h.applied_generation
		 AND NOT EXISTS (SELECT 1 FROM logs l WHERE l.timestamp>=h.hour AND l.timestamp<h.hour+interval '1 hour'
		 AND l.status NOT IN ('success','error','cancelled'))`, hour); err != nil {
			return err
		}
	}
	return tx.Commit()
}
