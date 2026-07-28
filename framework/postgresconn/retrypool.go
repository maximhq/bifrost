package postgresconn

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// maxCachedPlanRetries bounds retries of statements that fail with SQLSTATE
// 0A000 ("cached plan must not change result type"). Each pooled connection
// caches prepared statements independently and a retry may land on another
// stale connection; every failed attempt invalidates that connection's cache
// entry, so bounded retries converge quickly after a schema change.
const maxCachedPlanRetries = 3

// execQuerier is the subset of *sql.DB the retry wrapper delegates queries to.
type execQuerier interface {
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// retryConnPool is a gorm.ConnPool that retries statements failing with the
// Postgres "cached plan must not change result type" error, raised when DDL
// (e.g. a migration's ADD COLUMN on another instance during a rolling upgrade)
// invalidates a connection's cached prepared statement. The error is raised at
// plan-validation time, before the statement executes, so retrying writes is
// safe; pgx drops the stale cache entry on failure, so the retry re-prepares
// against the current schema.
//
// BeginTx returns a raw *sql.Tx, so statements inside explicit transactions
// intentionally bypass the retry: after an error the transaction is aborted
// and must be retried as a whole by the caller.
type retryConnPool struct {
	delegate execQuerier
	db       *sql.DB
}

func newRetryConnPool(db *sql.DB) *retryConnPool {
	return &retryConnPool{delegate: db, db: db}
}

func (r *retryConnPool) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return r.delegate.PrepareContext(ctx, query)
}

func (r *retryConnPool) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	result, err := r.delegate.ExecContext(ctx, query, args...)
	for attempt := 0; attempt < maxCachedPlanRetries && isCachedPlanError(err); attempt++ {
		result, err = r.delegate.ExecContext(ctx, query, args...)
	}
	return result, err
}

func (r *retryConnPool) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	rows, err := r.delegate.QueryContext(ctx, query, args...)
	for attempt := 0; attempt < maxCachedPlanRetries && isCachedPlanError(err); attempt++ {
		rows, err = r.delegate.QueryContext(ctx, query, args...)
	}
	return rows, err
}

// QueryRowContext cannot retry: *sql.Row surfaces its error only at Scan time.
func (r *retryConnPool) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return r.delegate.QueryRowContext(ctx, query, args...)
}

// BeginTx implements gorm.TxBeginner.
func (r *retryConnPool) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	return r.db.BeginTx(ctx, opts)
}

// GetDBConn implements gorm.GetDBConnector so gorm.DB.DB() keeps resolving the
// underlying *sql.DB for pool tuning and close.
func (r *retryConnPool) GetDBConn() (*sql.DB, error) {
	return r.db, nil
}

// isCachedPlanError reports whether err is the Postgres cached-plan
// invalidation error. SQLSTATE 0A000 (feature_not_supported) covers other
// errors too, so the message is matched as well.
func isCachedPlanError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) &&
		pgErr.Code == "0A000" &&
		strings.Contains(pgErr.Message, "cached plan must not change result type")
}
