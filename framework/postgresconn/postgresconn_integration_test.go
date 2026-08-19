package postgresconn

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	gormlogger "gorm.io/gorm/logger"
)

// newPasswordCommandTestConfig builds a config whose password command appends a
// line to a counter file on each run. MaxIdleConns is forced to 0 by the callers so
// every query opens a fresh physical connection, which is when BeforeConnect fires.
func newPasswordCommandTestConfig(t *testing.T, cacheTTL string) (*Config, string) {
	t.Helper()

	password := getenvDefault("BIFROST_POSTGRES_PASSWORD_COMMAND_TEST_PASSWORD", "bifrost_password")
	dir := t.TempDir()
	counterPath := filepath.Join(dir, "password-command-count")
	scriptPath := filepath.Join(dir, "password-command.sh")
	script := fmt.Sprintf("#!/bin/sh\nprintf 'called\\n' >> %q\nprintf %%s %q\n", counterPath, password)
	require.NoError(t, os.WriteFile(scriptPath, []byte(script), 0o700))

	return &Config{
		Host:            schemas.NewSecretVar(getenvDefault("BIFROST_POSTGRES_PASSWORD_COMMAND_TEST_HOST", "localhost")),
		Port:            schemas.NewSecretVar(getenvDefault("BIFROST_POSTGRES_PASSWORD_COMMAND_TEST_PORT", "5432")),
		User:            schemas.NewSecretVar(getenvDefault("BIFROST_POSTGRES_PASSWORD_COMMAND_TEST_USER", "bifrost")),
		DBName:          schemas.NewSecretVar(getenvDefault("BIFROST_POSTGRES_PASSWORD_COMMAND_TEST_DB", "bifrost")),
		SSLMode:         schemas.NewSecretVar(getenvDefault("BIFROST_POSTGRES_PASSWORD_COMMAND_TEST_SSLMODE", "disable")),
		PasswordCommand: &PasswordCommandConfig{Command: scriptPath, Timeout: "5s", CacheTTL: cacheTTL},
	}, counterPath
}

func passwordCommandRuns(t *testing.T, counterPath string) int {
	t.Helper()
	raw, err := os.ReadFile(counterPath)
	if err != nil {
		return 0
	}
	return strings.Count(string(raw), "called")
}

// TestPasswordCommandOpensRealPostgresConnections verifies password_command auth
// works end to end against a real server, and that the resolved password is cached.
//
// This previously asserted the command ran at least once per new physical
// connection. That is the behaviour the cache deliberately removes: BeforeConnect
// fires for every physical connection, so under pool churn the old behaviour forked
// a subprocess per connection — expensive, and pure waste for the common case of a
// short-lived IAM token that stays valid for minutes.
func TestPasswordCommandOpensRealPostgresConnections(t *testing.T) {
	if os.Getenv("BIFROST_POSTGRES_PASSWORD_COMMAND_TEST") != "1" {
		t.Skip("set BIFROST_POSTGRES_PASSWORD_COMMAND_TEST=1 to run against local Postgres")
	}

	t.Run("password is cached across new physical connections", func(t *testing.T) {
		cfg, counterPath := newPasswordCommandTestConfig(t, "")

		require.NoError(t, Validate(cfg, true))
		db, err := Open(BuildDSN(cfg), cfg, gormlogger.Default)
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		defer sqlDB.Close()

		// No idle connections: every query below opens a fresh physical connection.
		sqlDB.SetMaxOpenConns(1)
		sqlDB.SetMaxIdleConns(0)

		for range 4 {
			require.NoError(t, db.Exec("SELECT 1").Error)
			time.Sleep(10 * time.Millisecond)
		}

		require.Equal(t, 1, passwordCommandRuns(t, counterPath),
			"the password command should run once and be reused for the default TTL")
	})

	t.Run("expired cache resolves again", func(t *testing.T) {
		// A TTL shorter than the gap between queries forces a re-resolve, which is
		// what lets rotating credentials keep working.
		cfg, counterPath := newPasswordCommandTestConfig(t, "50ms")

		require.NoError(t, Validate(cfg, true))
		db, err := Open(BuildDSN(cfg), cfg, gormlogger.Default)
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		defer sqlDB.Close()

		sqlDB.SetMaxOpenConns(1)
		sqlDB.SetMaxIdleConns(0)

		require.NoError(t, db.Exec("SELECT 1").Error)
		time.Sleep(150 * time.Millisecond)
		require.NoError(t, db.Exec("SELECT 1").Error)

		require.GreaterOrEqual(t, passwordCommandRuns(t, counterPath), 2,
			"a cache entry past its TTL must be refreshed")
	})
}

func getenvDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
