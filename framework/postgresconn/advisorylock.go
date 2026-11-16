package postgresconn

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// migrationApplicationPrefix tags every migration-pool session so a waiting
// process can tell which sessions belong to the process holding a migration
// lock: the lock connection itself sits idle while that process's DDL runs on
// sibling connections.
const migrationApplicationPrefix = "bifrost-migrate:"

// MigrationDSN derives the DSN for a throwaway migration pool from a runtime DSN.
// simple_protocol keeps cached prepared plans from surviving the DDL and is also
// how Open recognises a migration pool. application_name identifies this process
// so lock waiters in other processes can see what the lock holder is doing.
func MigrationDSN(dsn string) string {
	return dsn + " default_query_exec_mode=simple_protocol application_name=" + quoteLibpqValue(migrationApplicationName())
}

// migrationApplicationName returns the application_name for this process's
// migration sessions: host name plus process ID, so it is unique per process
// wherever Bifrost runs (several processes can share a host name, e.g. on one VM
// or with host networking). The host part is truncated so the whole name fits
// Postgres' 63-byte identifier limit without losing the process ID.
func migrationApplicationName() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	suffix := ":" + strconv.Itoa(os.Getpid())
	if room := 63 - len(migrationApplicationPrefix) - len(suffix); len(host) > room {
		host = host[:room]
	}
	return migrationApplicationPrefix + host + suffix
}

// rowQueryer is the single-row query method shared by *sql.Conn and *sql.DB.
type rowQueryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// AdvisoryLockHolder describes the session holding a session-level advisory lock
// and what the process behind it is doing right now.
type AdvisoryLockHolder struct {
	PID             int
	ApplicationName string
	ClientAddr      string
	State           string
	// Query is the holder session's own current (or, when idle, last) statement.
	Query        string
	BackendStart time.Time
	// BusySessions counts the holder's sibling sessions (same migration
	// application_name, else same client address) that are running a statement,
	// sitting inside an open transaction, or finished a statement in the last 15
	// seconds, including the holder itself. A live instance between short polls (for
	// example while it waits to pause matview refreshes) still counts as busy; a
	// crashed instance's lingering sessions go quiet and do not.
	BusySessions int
	// BusyQuery and BusyFor describe the longest-running of those statements.
	BusyQuery string
	BusyFor   time.Duration
	// BusyWait is the wait event of that statement, e.g. "Lock/virtualxid".
	BusyWait string
}

// Busy reports whether the holder's process is visibly doing work, as opposed to
// a crashed instance's lingering session that will never release the lock.
func (h *AdvisoryLockHolder) Busy() bool {
	return h != nil && h.BusySessions > 0
}

// String renders the holder for logs and errors.
func (h *AdvisoryLockHolder) String() string {
	if h == nil {
		return "holder unknown"
	}
	who := fmt.Sprintf("pid %d", h.PID)
	if h.ApplicationName != "" {
		who += " (" + h.ApplicationName
		if h.ClientAddr != "" {
			who += ", " + h.ClientAddr
		}
		who += ")"
	} else if h.ClientAddr != "" {
		who += " (" + h.ClientAddr + ")"
	}
	if !h.Busy() {
		return who + " with no running statement"
	}
	query := strings.Join(strings.Fields(h.BusyQuery), " ")
	if len(query) > 160 {
		query = query[:160] + "…"
	}
	s := fmt.Sprintf("%s running %q for %s", who, query, h.BusyFor.Round(time.Second))
	if h.BusyWait != "" {
		s += ", waiting on " + h.BusyWait
	}
	return s
}

// advisoryLockHolderQuery finds the session granted a bigint advisory key in the
// current database, plus a summary of the busy sessions that belong to the same
// process (see AdvisoryLockHolder.BusySessions). Sibling sessions are matched on application_name when the holder set
// one (migration pools do, see MigrationDSN) and on client address otherwise.
// Postgres splits a bigint key into classid (high 32 bits) and objid (low 32 bits).
const advisoryLockHolderQuery = `
WITH holder AS (
	SELECT a.pid, COALESCE(a.application_name, '') AS app, COALESCE(host(a.client_addr), '') AS addr,
	       COALESCE(a.state, '') AS state, COALESCE(a.query, '') AS query, a.backend_start
	FROM pg_locks l
	JOIN pg_stat_activity a ON a.pid = l.pid
	WHERE l.locktype = 'advisory' AND l.granted AND l.objsubid = 1
	  AND l.database = (SELECT oid FROM pg_database WHERE datname = current_database())
	  AND l.classid = $1 AND l.objid = $2
	LIMIT 1
), busy AS (
	SELECT s.query, now() - COALESCE(s.query_start, s.xact_start, now()) AS running,
	       COALESCE(s.wait_event_type || '/' || s.wait_event, '') AS wait
	FROM pg_stat_activity s, holder h
	WHERE s.pid <> pg_backend_pid()
	  AND (s.state IN ('active', 'idle in transaction', 'idle in transaction (aborted)')
	       OR (s.state = 'idle' AND s.state_change > now() - make_interval(secs => $3)))
	  AND (s.pid = h.pid OR (h.app <> '' AND s.application_name = h.app)
	       OR (h.app = '' AND h.addr <> '' AND host(s.client_addr) = h.addr))
)
SELECT h.pid, h.app, h.addr, h.state, h.query, h.backend_start,
       (SELECT count(*) FROM busy),
       COALESCE((SELECT query FROM busy ORDER BY running DESC LIMIT 1), ''),
       COALESCE((SELECT EXTRACT(EPOCH FROM running) FROM busy ORDER BY running DESC LIMIT 1), 0),
       COALESCE((SELECT wait FROM busy ORDER BY running DESC LIMIT 1), '')
FROM holder h`

// defaultRecentActivityWindow is how recently an idle sibling session must have
// finished a statement for the holder's process to count as alive.
const defaultRecentActivityWindow = 15 * time.Second

// FindAdvisoryLockHolder returns the session holding advisory key in the current
// database, or nil when nobody holds it.
func FindAdvisoryLockHolder(ctx context.Context, q rowQueryer, key int64) (*AdvisoryLockHolder, error) {
	return findAdvisoryLockHolder(ctx, q, key, defaultRecentActivityWindow)
}

// findAdvisoryLockHolder is FindAdvisoryLockHolder with an explicit
// recent-activity window (see AdvisoryLockHolder.BusySessions).
func findAdvisoryLockHolder(ctx context.Context, q rowQueryer, key int64, recent time.Duration) (*AdvisoryLockHolder, error) {
	classid, objid := advisoryKeyHalves(key)
	var (
		h       AdvisoryLockHolder
		busyFor float64
	)
	err := q.QueryRowContext(ctx, advisoryLockHolderQuery, classid, objid, recent.Seconds()).Scan(
		&h.PID, &h.ApplicationName, &h.ClientAddr, &h.State, &h.Query, &h.BackendStart,
		&h.BusySessions, &h.BusyQuery, &busyFor, &h.BusyWait)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	h.BusyFor = time.Duration(busyFor * float64(time.Second))
	return &h, nil
}

// advisoryKeyHalves splits a bigint advisory key the way pg_locks reports it.
func advisoryKeyHalves(key int64) (classid, objid int64) {
	u := uint64(key)
	return int64(u >> 32), int64(u & 0xffffffff)
}

// AdvisoryWaitOptions configures WaitForAdvisoryLock.
type AdvisoryWaitOptions struct {
	// Key is the advisory lock key.
	Key int64
	// Label names the lock in logs and errors, e.g. "configstore migration".
	Label string
	// LogPrefix prefixes log lines, e.g. "[configstore]".
	LogPrefix string
	// RetryInterval is the wait between pg_try_advisory_lock attempts.
	RetryInterval time.Duration
	// Timeout is how long to wait while the holder shows no sign of work, the
	// signature of a crashed instance's lingering session.
	Timeout time.Duration
	// BusyTimeout is how long to keep waiting while the holder is visibly busy.
	// Values at or below Timeout disable the extension.
	BusyTimeout time.Duration
	// HolderLogInterval throttles the log line describing the holder.
	HolderLogInterval time.Duration
	// RecentActivityWindow is how recently the holder's process must have run a
	// statement to count as busy; defaultRecentActivityWindow when zero.
	RecentActivityWindow time.Duration
}

// WaitForAdvisoryLock acquires a session advisory lock on conn with
// pg_try_advisory_lock, retrying until it succeeds or the wait budget runs out.
// It never blocks inside Postgres, so a crashed holder cannot hang the caller.
//
// The budget depends on what the holder is doing. A holder whose process is
// running statements (a live instance doing slow but legitimate work, such as a
// CREATE INDEX CONCURRENTLY waiting on a long transaction) earns BusyTimeout;
// an idle holder only gets Timeout. The caller owns conn and closes it on error.
func WaitForAdvisoryLock(ctx context.Context, conn *sql.Conn, logger schemas.Logger, opts AdvisoryWaitOptions) error {
	if opts.RetryInterval <= 0 {
		opts.RetryInterval = 5 * time.Second
	}
	if opts.HolderLogInterval <= 0 {
		opts.HolderLogInterval = 30 * time.Second
	}
	if opts.RecentActivityWindow <= 0 {
		opts.RecentActivityWindow = defaultRecentActivityWindow
	}
	busyTimeout := max(opts.BusyTimeout, opts.Timeout)
	started := time.Now()
	var (
		holder     *AdvisoryLockHolder
		lastLogged time.Time
	)
	for attempt := 1; ; attempt++ {
		// Bound each round trip so a stalled database cannot outlast the budget.
		attemptCtx, cancel := context.WithTimeout(ctx, max(opts.RetryInterval, 5*time.Second))
		var acquired bool
		err := conn.QueryRowContext(attemptCtx, "SELECT pg_try_advisory_lock($1)", opts.Key).Scan(&acquired)
		cancel()
		if err != nil {
			return fmt.Errorf("failed to attempt %s advisory lock: %w", opts.Label, err)
		}
		if acquired {
			if attempt > 1 && logger != nil {
				logger.Info("%s %s lock acquired after %d attempts over %s", opts.LogPrefix, opts.Label, attempt, time.Since(started).Round(time.Second))
			}
			return nil
		}

		lookupCtx, cancelLookup := context.WithTimeout(ctx, 5*time.Second)
		h, lookupErr := findAdvisoryLockHolder(lookupCtx, conn, opts.Key, opts.RecentActivityWindow)
		cancelLookup()
		if lookupErr == nil && h == nil {
			// The holder released the lock between the try-lock and the lookup:
			// it is free now, so try again at once rather than judging the wait
			// by a holder that no longer exists.
			continue
		}
		if lookupErr == nil {
			holder = h
		}
		waited := time.Since(started)
		budget := opts.Timeout
		if holder.Busy() {
			budget = busyTimeout
		}
		if waited >= budget {
			return advisoryWaitError(opts, attempt, waited, holder)
		}
		if logger != nil {
			if time.Since(lastLogged) >= opts.HolderLogInterval {
				lastLogged = time.Now()
				logger.Info("%s waiting for %s lock (attempt %d, %s of %s budget used) - held by %s; retrying in %s...",
					opts.LogPrefix, opts.Label, attempt, waited.Round(time.Second), budget, holder, opts.RetryInterval)
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled while waiting for %s lock: %w", opts.Label, ctx.Err())
		case <-time.After(opts.RetryInterval):
		}
	}
}

// advisoryWaitError builds the give-up error. A busy holder is a live instance doing
// slow work, so the guidance points at what it is blocked on; an idle holder is
// most likely a crashed instance's session, so the guidance is to terminate it.
func advisoryWaitError(opts AdvisoryWaitOptions, attempts int, waited time.Duration, holder *AdvisoryLockHolder) error {
	classid, objid := advisoryKeyHalves(opts.Key)
	head := fmt.Sprintf("failed to acquire %s lock (key=%d) after %d attempts over %s; held by %s",
		opts.Label, opts.Key, attempts, waited.Round(time.Second), holder)
	if holder.Busy() {
		return fmt.Errorf("%s\n\n"+
			"The holder is a live process still doing work, not a stale session. Do not terminate it.\n"+
			"It is usually blocked behind a long-running transaction elsewhere in the database\n"+
			"(for example a REFRESH MATERIALIZED VIEW from another replica). To see what it waits on:\n"+
			"   SELECT pid, application_name, state, wait_event_type, wait_event, now() - query_start AS running, query\n"+
			"   FROM pg_stat_activity WHERE application_name = '%s' OR pid = %d;\n"+
			"Restart this instance once that work finishes; it will skip migrations another node already applied.",
			head, holder.ApplicationName, holder.PID)
	}
	return fmt.Errorf("%s\n\n"+
		"This usually means a previous crashed instance's lingering database session is still holding the lock.\n"+
		"To diagnose and resolve:\n\n"+
		"1. Find who holds the lock:\n"+
		"   SELECT pid, usename, application_name, client_addr, backend_start, state, query\n"+
		"   FROM pg_stat_activity\n"+
		"   WHERE pid IN (SELECT pid FROM pg_locks WHERE locktype = 'advisory' AND classid = %d AND objid = %d AND granted);\n\n"+
		"2. If the session belongs to a dead/crashed instance, terminate it:\n"+
		"   SELECT pg_terminate_backend(<pid_from_step_1>);\n\n"+
		"After terminating the stale session, restart this instance and it should proceed normally.",
		head, classid, objid)
}
