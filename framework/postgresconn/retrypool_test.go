package postgresconn

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func cachedPlanErr() *pgconn.PgError {
	return &pgconn.PgError{Code: "0A000", Message: "cached plan must not change result type"}
}

func TestIsCachedPlanError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "cached plan error", err: cachedPlanErr(), want: true},
		{name: "wrapped cached plan error", err: fmt.Errorf("exec failed: %w", cachedPlanErr()), want: true},
		{name: "other 0A000 error", err: &pgconn.PgError{Code: "0A000", Message: "LISTEN is not supported"}, want: false},
		{name: "other sqlstate with matching message", err: &pgconn.PgError{Code: "42P05", Message: "cached plan must not change result type"}, want: false},
		{name: "non-pg error", err: errors.New("cached plan must not change result type"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isCachedPlanError(tt.err); got != tt.want {
				t.Errorf("isCachedPlanError() = %v, want %v", got, tt.want)
			}
		})
	}
}

// stubExecQuerier fails the first failures calls with err, then succeeds.
type stubExecQuerier struct {
	failures int
	err      error
	calls    int
}

func (s *stubExecQuerier) attempt() error {
	s.calls++
	if s.calls <= s.failures {
		return s.err
	}
	return nil
}

func (s *stubExecQuerier) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return nil, s.attempt()
}

func (s *stubExecQuerier) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return nil, s.attempt()
}

func (s *stubExecQuerier) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return nil, s.attempt()
}

func (s *stubExecQuerier) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	s.calls++
	return nil
}

func TestRetryConnPoolRetriesCachedPlanErrors(t *testing.T) {
	tests := []struct {
		name      string
		failures  int
		err       error
		wantErr   bool
		wantCalls int
	}{
		{name: "success without retry", failures: 0, err: nil, wantErr: false, wantCalls: 1},
		{name: "success after one retry", failures: 1, err: cachedPlanErr(), wantErr: false, wantCalls: 2},
		{name: "success on last retry", failures: maxCachedPlanRetries, err: cachedPlanErr(), wantErr: false, wantCalls: maxCachedPlanRetries + 1},
		{name: "retries exhausted", failures: maxCachedPlanRetries + 1, err: cachedPlanErr(), wantErr: true, wantCalls: maxCachedPlanRetries + 1},
		{name: "non-retryable error", failures: 1, err: errors.New("connection refused"), wantErr: true, wantCalls: 1},
	}
	ops := []struct {
		name string
		run  func(pool *retryConnPool) error
	}{
		{name: "ExecContext", run: func(pool *retryConnPool) error {
			_, err := pool.ExecContext(context.Background(), "UPDATE t SET a = $1", 1)
			return err
		}},
		{name: "QueryContext", run: func(pool *retryConnPool) error {
			_, err := pool.QueryContext(context.Background(), "SELECT * FROM t")
			return err
		}},
	}
	for _, op := range ops {
		for _, tt := range tests {
			t.Run(op.name+"/"+tt.name, func(t *testing.T) {
				stub := &stubExecQuerier{failures: tt.failures, err: tt.err}
				pool := &retryConnPool{delegate: stub}
				err := op.run(pool)
				if (err != nil) != tt.wantErr {
					t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
				}
				if tt.wantErr && !errors.Is(err, tt.err) {
					t.Errorf("err = %v, want %v", err, tt.err)
				}
				if stub.calls != tt.wantCalls {
					t.Errorf("calls = %d, want %d", stub.calls, tt.wantCalls)
				}
			})
		}
	}
}

func TestRetryConnPoolDoesNotRetryPrepareOrQueryRow(t *testing.T) {
	stub := &stubExecQuerier{failures: 1, err: cachedPlanErr()}
	pool := &retryConnPool{delegate: stub}
	if _, err := pool.PrepareContext(context.Background(), "SELECT 1"); !errors.Is(err, stub.err) {
		t.Errorf("PrepareContext err = %v, want %v", err, stub.err)
	}
	if stub.calls != 1 {
		t.Errorf("PrepareContext calls = %d, want 1", stub.calls)
	}

	stub = &stubExecQuerier{failures: 1, err: cachedPlanErr()}
	pool = &retryConnPool{delegate: stub}
	pool.QueryRowContext(context.Background(), "SELECT 1")
	if stub.calls != 1 {
		t.Errorf("QueryRowContext calls = %d, want 1", stub.calls)
	}
}

func TestRetryConnPoolGetDBConn(t *testing.T) {
	db := &sql.DB{}
	pool := newRetryConnPool(db)
	got, err := pool.GetDBConn()
	if err != nil {
		t.Fatalf("GetDBConn() error = %v", err)
	}
	if got != db {
		t.Errorf("GetDBConn() = %p, want %p", got, db)
	}
}
