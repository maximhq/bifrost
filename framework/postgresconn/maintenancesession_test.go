package postgresconn

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// sessionSetting reads one setting on conn.
func sessionSetting(t *testing.T, conn *sql.Conn, name string) string {
	t.Helper()
	var value string
	require.NoError(t, conn.QueryRowContext(context.Background(), "SELECT current_setting($1)", name).Scan(&value))
	return value
}

// TestValidTempFileLimit pins the size forms accepted for temp_file_limit.
func TestValidTempFileLimit(t *testing.T) {
	for _, ok := range tempFileLimitAccepted {
		require.True(t, ValidTempFileLimit(ok), ok)
	}
	for _, bad := range append([]string{"", "20gb", "1.5GB", "GB", "-2", "20 GiB", "lots"}, tempFileLimitOutOfRange...) {
		require.False(t, ValidTempFileLimit(bad), bad)
	}
}

// tempFileLimitAccepted and tempFileLimitOutOfRange sit on both sides of the
// server's range: temp_file_limit is an int in kB, so 2147483647 kB is the
// largest value Postgres takes.
var (
	tempFileLimitAccepted   = []string{"-1", "0", "1024", "20GB", "512 MB", "100kB", " 1TB ", "2047GB", "2097151MB", "2147483647"}
	tempFileLimitOutOfRange = []string{"2TB", "2048GB", "2097152MB", "2147483648", "99999999999999999999"}
)

// TestValidTempFileLimitMatchesServer pins ValidTempFileLimit to what a real
// server accepts, so a value that passes startup validation cannot fail every
// session setup afterwards. Forms the server also takes but the validator does
// not (fractions such as "1.5GB") are only dropped with a warning, so they are
// not compared here.
func TestValidTempFileLimitMatchesServer(t *testing.T) {
	cfg := localPostgresConfig()
	db := openLocalPostgres(t, BuildDSN(cfg), cfg)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	ctx := context.Background()
	conn, err := sqlDB.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()

	for _, values := range [][]string{tempFileLimitAccepted, tempFileLimitOutOfRange} {
		for _, v := range values {
			_, serr := conn.ExecContext(ctx, "SELECT set_config('temp_file_limit', $1, false)", strings.TrimSpace(v))
			require.Equal(t, serr == nil, ValidTempFileLimit(v), "value %q: server error %v", v, serr)
		}
	}
	_, err = conn.ExecContext(ctx, "RESET temp_file_limit")
	require.NoError(t, err)
}

// TestBoundMaintenanceSessionSetsAndRestores pins that every bound is applied on
// the session and that restore puts back the pool's values.
func TestBoundMaintenanceSessionSetsAndRestores(t *testing.T) {
	cfg := localPostgresConfig()
	cfg.StatementTimeout = "300ms"
	db := openLocalPostgres(t, BuildDSN(cfg), cfg)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	ctx := context.Background()
	conn, err := sqlDB.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()

	before := map[string]string{}
	for _, name := range []string{"statement_timeout", "client_connection_check_interval", "idle_session_timeout", "temp_file_limit"} {
		before[name] = sessionSetting(t, conn, name)
	}

	restore, result, err := BoundMaintenanceSession(ctx, conn, MaintenanceSessionOptions{
		StatementTimeout:    90 * time.Second,
		ClientCheckInterval: 10 * time.Second,
		IdleSessionTimeout:  time.Minute,
		TempFileLimit:       "2GB",
	})
	require.NoError(t, err)
	require.False(t, result.TempFileLimitDenied, "the test role is a superuser")
	require.Equal(t, "90s", sessionSetting(t, conn, "statement_timeout"))
	require.Equal(t, "10s", sessionSetting(t, conn, "client_connection_check_interval"))
	require.Equal(t, "1min", sessionSetting(t, conn, "idle_session_timeout"))
	require.Equal(t, "2GB", sessionSetting(t, conn, "temp_file_limit"))

	require.NoError(t, restore(ctx))
	for name, want := range before {
		require.Equal(t, want, sessionSetting(t, conn, name), name)
	}
}

// TestBoundMaintenanceSessionZeroStatementTimeout pins that a zero statement
// timeout lifts the pool's timeout, as DisableStatementTimeout does, and that
// unset options leave the other settings alone.
func TestBoundMaintenanceSessionZeroStatementTimeout(t *testing.T) {
	cfg := localPostgresConfig()
	cfg.StatementTimeout = "300ms"
	db := openLocalPostgres(t, BuildDSN(cfg), cfg)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	ctx := context.Background()
	conn, err := sqlDB.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()
	check := sessionSetting(t, conn, "client_connection_check_interval")

	restore, _, err := BoundMaintenanceSession(ctx, conn, MaintenanceSessionOptions{})
	require.NoError(t, err)
	_, err = conn.ExecContext(ctx, "SELECT pg_sleep(0.6)")
	require.NoError(t, err, "statement_timeout must be lifted")
	require.Equal(t, check, sessionSetting(t, conn, "client_connection_check_interval"))
	require.NoError(t, restore(ctx))
	require.Equal(t, "300ms", sessionSetting(t, conn, "statement_timeout"))
}

// TestBoundMaintenanceSessionServerTimeoutWithoutClientCancel pins the backstop
// for a client that died: with no context deadline at all, the server itself
// ends the statement.
func TestBoundMaintenanceSessionServerTimeoutWithoutClientCancel(t *testing.T) {
	cfg := localPostgresConfig()
	db := openLocalPostgres(t, BuildDSN(cfg), cfg)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	ctx := context.Background()
	conn, err := sqlDB.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()

	restore, _, err := BoundMaintenanceSession(ctx, conn, MaintenanceSessionOptions{StatementTimeout: time.Second})
	require.NoError(t, err)
	defer func() { require.NoError(t, restore(ctx)) }()
	started := time.Now()
	_, err = conn.ExecContext(ctx, "SELECT pg_sleep(30)")
	require.Error(t, err)
	require.Contains(t, err.Error(), "statement timeout")
	require.Less(t, time.Since(started), 10*time.Second)
}

// TestBoundMaintenanceSessionTempFileLimitDenied pins that a role without the
// privilege gets the other bounds and a denial report, not a failure.
func TestBoundMaintenanceSessionTempFileLimitDenied(t *testing.T) {
	cfg := localPostgresConfig()
	admin := openLocalPostgres(t, BuildDSN(cfg), cfg)
	role := fmt.Sprintf("bf_nosu_%d", time.Now().UnixNano())
	if err := admin.Exec(fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD 'pw'", role)).Error; err != nil {
		t.Skipf("cannot create a non-superuser role: %v", err)
	}
	t.Cleanup(func() { _ = admin.Exec("DROP ROLE IF EXISTS " + role).Error })

	limited := localPostgresConfig()
	limited.User = schemas.NewSecretVar(role)
	limited.Password = schemas.NewSecretVar("pw")
	db := openLocalPostgres(t, BuildDSN(limited), limited)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	ctx := context.Background()
	conn, err := sqlDB.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()
	temp := sessionSetting(t, conn, "temp_file_limit")

	restore, result, err := BoundMaintenanceSession(ctx, conn, MaintenanceSessionOptions{
		StatementTimeout:    time.Minute,
		ClientCheckInterval: 5 * time.Second,
		TempFileLimit:       "1GB",
	})
	require.NoError(t, err)
	require.True(t, result.TempFileLimitDenied)
	require.Equal(t, temp, sessionSetting(t, conn, "temp_file_limit"))
	require.Equal(t, "5s", sessionSetting(t, conn, "client_connection_check_interval"))
	require.NoError(t, restore(ctx))
}

// clientCheckRejectingConn forwards to a real connection but answers the
// client_connection_check_interval set the way a server on a platform without
// socket-close reporting (for example Windows) does.
type clientCheckRejectingConn struct {
	*sql.Conn
}

func (c clientCheckRejectingConn) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if strings.Contains(query, "set_config('client_connection_check_interval'") {
		return nil, &pgconn.PgError{Code: "22023", Message: `invalid value for parameter "client_connection_check_interval": "10000"`}
	}
	return c.Conn.ExecContext(ctx, query, args...)
}

// TestBoundMaintenanceSessionClientCheckUnsupported pins that a server which
// rejects client_connection_check_interval gets the other bounds and a report,
// not a failure.
func TestBoundMaintenanceSessionClientCheckUnsupported(t *testing.T) {
	cfg := localPostgresConfig()
	db := openLocalPostgres(t, BuildDSN(cfg), cfg)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	ctx := context.Background()
	conn, err := sqlDB.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()

	before := map[string]string{}
	for _, name := range []string{"statement_timeout", "client_connection_check_interval", "temp_file_limit"} {
		before[name] = sessionSetting(t, conn, name)
	}

	restore, result, err := BoundMaintenanceSession(ctx, clientCheckRejectingConn{conn}, MaintenanceSessionOptions{
		StatementTimeout:    90 * time.Second,
		ClientCheckInterval: 10 * time.Second,
		TempFileLimit:       "2GB",
	})
	require.NoError(t, err)
	require.True(t, result.ClientCheckUnsupported)
	require.False(t, result.TempFileLimitDenied, "the test role is a superuser")
	require.Equal(t, "90s", sessionSetting(t, conn, "statement_timeout"))
	require.Equal(t, before["client_connection_check_interval"], sessionSetting(t, conn, "client_connection_check_interval"))
	require.Equal(t, "2GB", sessionSetting(t, conn, "temp_file_limit"))

	require.NoError(t, restore(ctx))
	for name, want := range before {
		require.Equal(t, want, sessionSetting(t, conn, name), name)
	}
}
