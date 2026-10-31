package postgresconn

import (
	"context"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	// DefaultSmallTableMaxBytes and DefaultSmallTableMaxRows are the thresholds
	// below which an index is built without CONCURRENTLY: below both, a plain
	// build finishes in well under a second, so blocking the table's writes for
	// that long is cheaper than waiting on every long transaction in the database.
	DefaultSmallTableMaxBytes int64   = 256 << 20
	DefaultSmallTableMaxRows  float64 = 100_000

	// boundedDDLLockTimeout caps each attempt's wait for the table lock, so a
	// queued statement never stalls the table's other readers and writers for long.
	boundedDDLLockTimeout = "5s"
	// boundedDDLStatementTimeout caps the statement itself. The migration pool
	// runs without a statement_timeout, and a small-table build that ran this
	// long would mean the size estimate was badly wrong.
	boundedDDLStatementTimeout = "120s"
	// boundedDDLRetryBudget is how long lock timeouts are retried in place before
	// the error surfaces to the caller's step-level retry.
	boundedDDLRetryBudget = 2 * time.Minute
	// describeLockHoldersTimeout bounds DescribeTableLockHolders's catalog query.
	describeLockHoldersTimeout = 5 * time.Second
)

// TableIsSmall reports whether table is below both maxBytes and maxRows, i.e.
// small enough to index without CONCURRENTLY.
//
// CREATE INDEX CONCURRENTLY waits for every transaction in the database older
// than it, whatever table that transaction touches, so on a database shared with
// a large log store a tiny table's index waits out the longest open transaction
// (an old replica's materialized-view refresh or unbounded dashboard query). A
// plain build only takes a SHARE lock on its own table, which on a small table is
// held for milliseconds.
//
// Size comes from pg_total_relation_size; the row estimate from
// pg_class.reltuples, which is -1 for a never-analyzed table, in which case the
// byte size alone decides. A missing table reports small: there is nothing to
// scan, and the build fails on its own with a clear error.
func TableIsSmall(tx *gorm.DB, table string, maxBytes int64, maxRows float64) (bool, error) {
	var rows []struct {
		Tuples float64
		Bytes  int64
	}
	err := tx.Raw(`SELECT c.reltuples AS tuples, pg_total_relation_size(c.oid) AS bytes
		FROM pg_catalog.pg_class c WHERE c.oid = to_regclass(?)`, table).Scan(&rows).Error
	if err != nil {
		return false, err
	}
	if len(rows) == 0 {
		return true, nil
	}
	r := rows[0]
	return r.Bytes < maxBytes && r.Tuples < maxRows, nil
}

// ExecWithBoundedLock runs stmts in one short transaction with a bounded lock
// wait and statement time, retrying lock timeouts with backoff. It is for DDL on
// small tables that would otherwise use CONCURRENTLY, and runs fine from a
// migration with UseTransaction=false because it opens its own transaction.
func ExecWithBoundedLock(tx *gorm.DB, label string, stmts ...string) error {
	ctx := tx.Statement.Context
	if ctx == nil {
		ctx = context.Background()
	}
	return RunWithLockRetry(ctx, nil, label, LockRetryOptions{Budget: boundedDDLRetryBudget}, func() error {
		return tx.Transaction(func(t *gorm.DB) error {
			if err := t.Exec("SET LOCAL lock_timeout = '" + boundedDDLLockTimeout + "'").Error; err != nil {
				return err
			}
			if err := t.Exec("SET LOCAL statement_timeout = '" + boundedDDLStatementTimeout + "'").Error; err != nil {
				return err
			}
			for _, stmt := range stmts {
				if err := t.Exec(stmt).Error; err != nil {
					return err
				}
			}
			return nil
		})
	})
}

// DescribeTableLockHolders lists the other sessions holding a granted lock on
// any of tables, oldest transaction first, with a pg_cancel_backend hint. It is
// meant for RunWithLockRetry's Describe: on a rolling upgrade the blockers are
// usually an old replica's long reads, which an operator can cancel safely. It
// returns "" when the lookup fails or nothing is held. The lookup is a
// diagnostic on a retry path, so it is bounded by describeLockHoldersTimeout
// rather than allowed to hold up the retry itself.
//
// The statement text is never included: pg_stat_activity.query carries the
// session's current or most recent statement verbatim, literals and all, and
// this output lands in logs and in the boot error. Each holder is classified
// into a fixed set of statement kinds (see statementKind); together with the
// session's application_name that says enough to tell a cancellable read from a
// write and to identify the replica.
func DescribeTableLockHolders(ctx context.Context, db *gorm.DB, tables ...string) string {
	ctx, cancel := context.WithTimeout(ctx, describeLockHoldersTimeout)
	defer cancel()
	var rows []struct {
		PID     int
		Rel     string
		Mode    string
		State   string
		XactAge string
		App     string
		Query   string
	}
	// a.query is fetched only to be classified by statementKind; it is never
	// written anywhere.
	err := db.WithContext(ctx).Raw(`
		SELECT a.pid, l.relation::regclass::text AS rel, l.mode, COALESCE(a.state, '') AS state,
		       COALESCE(date_trunc('second', now() - a.xact_start)::text, '') AS xact_age,
		       COALESCE(a.application_name, '') AS app,
		       COALESCE(a.query, '') AS query
		FROM pg_locks l
		JOIN pg_stat_activity a ON a.pid = l.pid
		WHERE l.granted AND l.locktype = 'relation' AND a.pid <> pg_backend_pid()
		  AND l.relation = ANY (SELECT to_regclass(t) FROM unnest(?::text[]) AS t)
		ORDER BY a.xact_start NULLS LAST
		LIMIT 20`, "{"+strings.Join(tables, ",")+"}").Scan(&rows).Error
	if err != nil || len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&b, "  pid=%d rel=%s mode=%s state=%s xact_age=%s app=%q statement=%s\n", r.PID, r.Rel, r.Mode, r.State, r.XactAge, r.App, statementKind(r.Query))
	}
	b.WriteString("  read-only blockers (SELECT, REFRESH MATERIALIZED VIEW) can be cancelled with SELECT pg_cancel_backend(<pid>);")
	return b.String()
}

// statementKinds is the allowlist of leading SQL keywords statementKind reports.
// Anything else, including a statement that opens with a comment, is UNKNOWN,
// so no substring of the statement text ever reaches a log line.
var statementKinds = map[string]struct{}{
	"SELECT": {}, "WITH": {}, "INSERT": {}, "UPDATE": {}, "DELETE": {}, "MERGE": {}, "COPY": {},
	"REFRESH": {}, "CREATE": {}, "ALTER": {}, "DROP": {}, "TRUNCATE": {}, "VACUUM": {}, "ANALYZE": {},
	"REINDEX": {}, "LOCK": {}, "BEGIN": {}, "START": {}, "COMMIT": {}, "ROLLBACK": {}, "CALL": {}, "DO": {},
}

// statementKind classifies a statement by its leading keyword: the run of ASCII
// letters at the start of the text, after any whitespace, upper-cased and looked
// up in statementKinds. It returns UNKNOWN for an empty statement, a statement
// that starts with anything but letters (a comment, a parenthesis) or a keyword
// outside the allowlist. Only allowlisted constants are ever returned.
func statementKind(query string) string {
	i := 0
	for i < len(query) && (query[i] == ' ' || query[i] == '\t' || query[i] == '\n' || query[i] == '\r') {
		i++
	}
	j := i
	for j < len(query) && ((query[j] >= 'a' && query[j] <= 'z') || (query[j] >= 'A' && query[j] <= 'Z')) {
		j++
	}
	if j == i {
		return "UNKNOWN"
	}
	kind := strings.ToUpper(query[i:j])
	if _, ok := statementKinds[kind]; !ok {
		return "UNKNOWN"
	}
	return kind
}
