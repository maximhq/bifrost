package logstore

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"time"
)

// matViewRefreshStateDDL creates the cluster-wide record of materialized-view
// refreshes: one row per view, written by whichever replica refreshed it.
//
// Every replica runs its own refresh ticker, and the advisory lock only stops two
// passes overlapping; it does not stop the next replica starting a pass the moment
// the previous one finishes. On a large logs table, where one pass outlasts the
// interval, that kept the views refreshing back to back across the cluster. The
// record lets a replica skip a view whose last successful refresh finished less
// than matview_refresh_interval ago, so the cluster as a whole refreshes each view
// at most once per interval, with a quiet gap of at least the interval between
// passes however long a pass takes.
//
// Times are written with the database clock (now()), so clock skew between
// replicas cannot shorten or stretch the gap.
const matViewRefreshStateDDL = `
CREATE TABLE IF NOT EXISTS matview_refresh_state (
	view_name        VARCHAR(128) PRIMARY KEY,
	last_started_at  TIMESTAMPTZ,
	last_success_at  TIMESTAMPTZ,
	last_status      VARCHAR(16) NOT NULL DEFAULT '',
	last_error       TEXT,
	last_duration_ms BIGINT,
	last_node        VARCHAR(255)
)`

// matViewRefreshStatusRunning, matViewRefreshStatusSuccess and
// matViewRefreshStatusFailed are the values of matview_refresh_state.last_status.
const (
	matViewRefreshStatusRunning = "running"
	matViewRefreshStatusSuccess = "success"
	matViewRefreshStatusFailed  = "failed"
)

// matViewRefreshStateTimeout bounds each read or write of the refresh record. The
// record is bookkeeping: a slow or failed write must never hold up or fail the
// refresh itself.
const matViewRefreshStateTimeout = 5 * time.Second

// matViewRefreshErrorMaxLen caps the error text stored in the record.
const matViewRefreshErrorMaxLen = 1000

// matViewRefreshNode identifies this process in the refresh record.
var matViewRefreshNode = func() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	return host + ":" + strconv.Itoa(os.Getpid())
}()

// recentlyRefreshedMatViews returns the views whose last successful refresh, by
// the database clock, finished less than minGap ago. ok is false when the record
// cannot be read (for example the table does not exist yet); callers then refresh
// every view and skip writing the record, which is how refreshes behaved before
// it existed. A non-positive minGap returns an empty set: every view is due.
func recentlyRefreshedMatViews(ctx context.Context, conn *sql.Conn, minGap time.Duration) (fresh map[string]bool, ok bool) {
	fresh = map[string]bool{}
	readCtx, cancel := context.WithTimeout(ctx, matViewRefreshStateTimeout)
	defer cancel()
	rows, err := conn.QueryContext(readCtx, `
		SELECT view_name FROM matview_refresh_state
		WHERE $1 > 0 AND last_success_at > now() - make_interval(secs => $1)`, minGap.Seconds())
	if err != nil {
		return fresh, false
	}
	defer rows.Close()
	for rows.Next() {
		var view string
		if err := rows.Scan(&view); err != nil {
			return map[string]bool{}, false
		}
		fresh[view] = true
	}
	if rows.Err() != nil {
		return map[string]bool{}, false
	}
	return fresh, true
}

// matViewRecordExecer is what the record writers need: the refresher passes its
// pool rather than its locked connection, because a REFRESH cut off by its
// deadline can leave that connection unusable, and the failure must still be
// recorded.
type matViewRecordExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// recordMatViewRefreshStart marks view as being refreshed by this process. It
// leaves last_success_at untouched, so a refresh that never finishes (cancelled,
// timed out, process killed) leaves the view due on the next tick.
func recordMatViewRefreshStart(ctx context.Context, conn matViewRecordExecer, view string) {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), matViewRefreshStateTimeout)
	defer cancel()
	_, _ = conn.ExecContext(writeCtx, `
		INSERT INTO matview_refresh_state (view_name, last_started_at, last_status, last_node)
		VALUES ($1, now(), $2, $3)
		ON CONFLICT (view_name) DO UPDATE
		SET last_started_at = now(), last_status = $2, last_node = $3`,
		view, matViewRefreshStatusRunning, matViewRefreshNode)
}

// recordMatViewRefreshEnd records the outcome of refreshing view. Only a success
// moves last_success_at, which is what the next replica's skip check reads; the
// finish time is taken from the database clock at the end of the refresh, so the
// gap is measured from when the work ended rather than when it began.
func recordMatViewRefreshEnd(ctx context.Context, conn matViewRecordExecer, view string, duration time.Duration, refreshErr error) {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), matViewRefreshStateTimeout)
	defer cancel()
	if refreshErr == nil {
		_, _ = conn.ExecContext(writeCtx, `
			UPDATE matview_refresh_state
			SET last_success_at = now(), last_status = $2, last_error = NULL, last_duration_ms = $3, last_node = $4
			WHERE view_name = $1`,
			view, matViewRefreshStatusSuccess, duration.Milliseconds(), matViewRefreshNode)
		return
	}
	msg := refreshErr.Error()
	if len(msg) > matViewRefreshErrorMaxLen {
		msg = msg[:matViewRefreshErrorMaxLen]
	}
	_, _ = conn.ExecContext(writeCtx, `
		UPDATE matview_refresh_state
		SET last_status = $2, last_error = $3, last_duration_ms = $4, last_node = $5
		WHERE view_name = $1`,
		view, matViewRefreshStatusFailed, msg, duration.Milliseconds(), matViewRefreshNode)
}

// forgetMatViewRefresh drops view's record. Called when a view is dropped for a
// rebuild, so the rebuilt view is never skipped on the strength of a refresh of
// the view it replaced.
func forgetMatViewRefresh(ctx context.Context, conn *sql.Conn, view string) error {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), matViewRefreshStateTimeout)
	defer cancel()
	if _, err := conn.ExecContext(writeCtx, "DELETE FROM matview_refresh_state WHERE view_name = $1", view); err != nil {
		return fmt.Errorf("forget refresh record for %s: %w", view, err)
	}
	return nil
}
