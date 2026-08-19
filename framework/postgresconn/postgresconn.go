package postgresconn

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/maximhq/bifrost/core/schemas"
	"golang.org/x/sync/singleflight"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

const defaultPasswordCommandTimeout = 10 * time.Second

// defaultPasswordCommandCacheTTL is how long a resolved password is reused before
// the command is run again. Without caching the command runs on every new physical
// connection, which for the common case (fetching a short-lived IAM token) is a
// subprocess fork per connection.
//
// Deliberately conservative: pgx's OptionBeforeConnect gets no signal about whether
// the password was actually accepted, so the TTL is the only invalidation mechanism.
const defaultPasswordCommandCacheTTL = 60 * time.Second

// PasswordCommandConfig describes a command that prints a Postgres password to stdout.
type PasswordCommandConfig struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	Timeout string   `json:"timeout,omitempty"`
	// CacheTTL is how long a successfully resolved password is reused across new
	// physical connections (Go duration string, default 60s). Set it below the
	// credential's validity window; a rotation shorter than the TTL will fail
	// connections until the cached value expires.
	CacheTTL string `json:"cache_ttl,omitempty"`
}

// passwordCache memoizes a resolved password for the configured TTL and collapses
// concurrent resolutions onto a single command execution. Pool churn otherwise
// produces one fork per connection, all racing to fetch the same token.
type passwordCache struct {
	group singleflight.Group

	mu        sync.RWMutex
	value     string
	expiresAt time.Time
}

// get returns a cached password when still fresh, otherwise resolves one. Failures
// are never cached, so a transient error does not persist for the whole TTL.
func (c *passwordCache) get(ctx context.Context, config *PasswordCommandConfig) (string, error) {
	ttl, err := parsePasswordCommandCacheTTL(config)
	if err != nil {
		return "", err
	}

	c.mu.RLock()
	value, expiresAt := c.value, c.expiresAt
	c.mu.RUnlock()
	if value != "" && time.Now().Before(expiresAt) {
		return value, nil
	}

	// singleflight keys on a constant: one cache instance serves one config.
	resolved, err, _ := c.group.Do("password", func() (any, error) {
		// Re-check under the flight: a concurrent caller may have just refreshed.
		c.mu.RLock()
		value, expiresAt := c.value, c.expiresAt
		c.mu.RUnlock()
		if value != "" && time.Now().Before(expiresAt) {
			return value, nil
		}
		// Detached from ctx on purpose: singleflight hands this single execution's
		// error to every waiter, so one canceled dial would fail unrelated password
		// resolutions. RunPasswordCommand applies its own timeout, so this stays bounded.
		password, err := RunPasswordCommand(context.WithoutCancel(ctx), config)
		if err != nil {
			return "", err
		}
		c.mu.Lock()
		c.value = password
		c.expiresAt = time.Now().Add(ttl)
		c.mu.Unlock()
		return password, nil
	})
	if err != nil {
		return "", err
	}
	return resolved.(string), nil
}

// Config is the shared Postgres connection configuration used by framework stores.
type Config struct {
	Host            *schemas.SecretVar        `json:"host"`
	Port            *schemas.SecretVar        `json:"port"`
	User            *schemas.SecretVar        `json:"user"`
	Password        *schemas.SecretVar        `json:"password"`
	PasswordCommand *PasswordCommandConfig `json:"password_command,omitempty"`
	DBName          *schemas.SecretVar        `json:"db_name"`
	SSLMode         *schemas.SecretVar        `json:"ssl_mode"`
	MaxIdleConns    int                    `json:"max_idle_conns"`
	MaxOpenConns    int                    `json:"max_open_conns"`
	ConnMaxLifetime string                 `json:"conn_max_lifetime,omitempty"`
	// ConnMaxIdleTime bounds how long an idle physical connection is kept before
	// being closed. Without it, the idle cap is the only thing controlling pool
	// size, so every burst above MaxIdleConns closes connections on return and
	// reopens them on the next query — each reopen forks a Postgres backend.
	ConnMaxIdleTime string `json:"conn_max_idle_time,omitempty"`
}

// Validate checks required Postgres connection fields.
func Validate(config *Config, requireStaticPassword bool) error {
	if config == nil {
		return fmt.Errorf("config is required")
	}
	if config.Host == nil || config.Host.GetValue() == "" {
		return fmt.Errorf("postgres host is required")
	}
	if config.Port == nil || config.Port.GetValue() == "" {
		return fmt.Errorf("postgres port is required")
	}
	if config.User == nil || config.User.GetValue() == "" {
		return fmt.Errorf("postgres user is required")
	}
	if config.DBName == nil || config.DBName.GetValue() == "" {
		return fmt.Errorf("postgres db name is required")
	}
	if config.SSLMode == nil || config.SSLMode.GetValue() == "" {
		return fmt.Errorf("postgres ssl mode is required")
	}
	if _, err := parseConnMaxLifetime(config); err != nil {
		return err
	}
	if _, err := parseConnMaxIdleTime(config); err != nil {
		return err
	}
	if config.PasswordCommand != nil {
		if err := validatePasswordCommand(config.PasswordCommand); err != nil {
			return err
		}
		if config.Password != nil && config.Password.GetValue() != "" {
			return fmt.Errorf("postgres password and password_command are mutually exclusive")
		}
		if _, err := parsePasswordCommandTimeout(config.PasswordCommand); err != nil {
			return err
		}
		if _, err := parsePasswordCommandCacheTTL(config.PasswordCommand); err != nil {
			return err
		}
		return nil
	}
	if config.Password == nil {
		return fmt.Errorf("postgres password is required")
	}
	if requireStaticPassword && config.Password.GetValue() == "" {
		return fmt.Errorf("postgres password is required")
	}
	return nil
}

// BuildDSN assembles a libpq-style DSN from the validated config.
func BuildDSN(config *Config) string {
	password := ""
	if config.Password != nil {
		password = config.Password.GetValue()
	}
	return fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		quoteLibpqValue(config.Host.GetValue()), quoteLibpqValue(config.Port.GetValue()), quoteLibpqValue(config.User.GetValue()),
		quoteLibpqValue(password), quoteLibpqValue(config.DBName.GetValue()), quoteLibpqValue(config.SSLMode.GetValue()))
}

// Open opens a *gorm.DB against the configured Postgres instance.
func Open(dsn string, config *Config, logger gormlogger.Interface) (*gorm.DB, error) {
	if config.PasswordCommand == nil {
		return gorm.Open(postgres.New(postgres.Config{DSN: dsn}), &gorm.Config{
			Logger: logger,
		})
	}

	pgxConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	// One cache per pool. BeforeConnect runs for every new physical connection, so
	// without this a bursty workload forks the password command once per connection.
	cache := &passwordCache{}
	sqlDB := stdlib.OpenDB(*pgxConfig, stdlib.OptionBeforeConnect(func(ctx context.Context, connConfig *pgx.ConnConfig) error {
		password, err := cache.get(ctx, config.PasswordCommand)
		if err != nil {
			return err
		}
		connConfig.Password = password
		return nil
	}))
	return openGormFromSQLDB(sqlDB, logger)
}

// openGormFromSQLDB opens a GORM connection over an existing sql.DB.
func openGormFromSQLDB(sqlDB *sql.DB, logger gormlogger.Interface) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		Logger: logger,
	})
	if err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return db, nil
}

const (
	// defaultMaxIdleConns / defaultMaxOpenConns are the pool sizes used when the
	// config leaves them at zero.
	defaultMaxIdleConns = 5
	defaultMaxOpenConns = 50

	// defaultConnMaxIdleTime reaps genuinely idle connections without making the
	// idle cap the only pool-size control. Paired with MaxIdleConns, this is what
	// keeps a bursty workload from churning physical connections.
	defaultConnMaxIdleTime = 5 * time.Minute

	// Migration pools run strictly serial DDL, so they need almost nothing. Left
	// untuned they inherit database/sql's defaults, where MaxOpenConns is unlimited.
	migrationMaxOpenConns = 2
	migrationMaxIdleConns = 1
)

// ApplyPoolTuning applies MaxIdleConns, MaxOpenConns, ConnMaxLifetime, and
// ConnMaxIdleTime. When a logger is supplied the effective pool shape is logged
// once, so operators can size against the server's max_connections without
// reading source — note that logstore and configstore open separate pools, so a
// pod's ceiling is the sum across them.
func ApplyPoolTuning(db *gorm.DB, config *Config, logger ...schemas.Logger) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	maxIdleConns := config.MaxIdleConns
	if maxIdleConns == 0 {
		maxIdleConns = defaultMaxIdleConns
	}
	sqlDB.SetMaxIdleConns(maxIdleConns)
	maxOpenConns := config.MaxOpenConns
	if maxOpenConns == 0 {
		maxOpenConns = defaultMaxOpenConns
	}
	sqlDB.SetMaxOpenConns(maxOpenConns)

	lifetime, err := parseConnMaxLifetime(config)
	if err != nil {
		return err
	}
	if lifetime > 0 {
		sqlDB.SetConnMaxLifetime(lifetime)
	}

	idleTime, err := parseConnMaxIdleTime(config)
	if err != nil {
		return err
	}
	sqlDB.SetConnMaxIdleTime(idleTime)

	if len(logger) > 0 && logger[0] != nil {
		lifetimeDesc := "unlimited"
		if lifetime > 0 {
			lifetimeDesc = lifetime.String()
		}
		logger[0].Info("postgres pool tuned: max_open_conns=%d max_idle_conns=%d conn_max_idle_time=%s conn_max_lifetime=%s (this is one pool; logstore and configstore each open their own)",
			maxOpenConns, maxIdleConns, idleTime, lifetimeDesc)
	}
	return nil
}

// ApplyMigrationPoolTuning pins a throwaway migration pool to a minimal size.
// Migration pools are opened, used for serial DDL, and closed; without this they
// inherit database/sql's defaults, where MaxOpenConns is unlimited.
func ApplyMigrationPoolTuning(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	sqlDB.SetMaxOpenConns(migrationMaxOpenConns)
	sqlDB.SetMaxIdleConns(migrationMaxIdleConns)
	return nil
}

// Close closes the *sql.DB backing a *gorm.DB, logging any error.
func Close(db *gorm.DB, logger schemas.Logger) {
	if db == nil {
		if logger != nil {
			logger.Debug("skipping close for nil DB connection")
		}
		return
	}
	sqlDB, err := db.DB()
	if err != nil {
		if logger != nil {
			logger.Error("failed to resolve *sql.DB for close: %v", err)
		}
		return
	}
	if err := sqlDB.Close(); err != nil {
		if logger != nil {
			logger.Error("failed to close DB connection: %v", err)
		}
	}
}

// RunPasswordCommand executes a configured password command and returns stdout.
func RunPasswordCommand(ctx context.Context, config *PasswordCommandConfig) (string, error) {
	if config == nil {
		return "", fmt.Errorf("postgres password_command config is required")
	}
	if err := validatePasswordCommand(config); err != nil {
		return "", err
	}
	timeout, err := parsePasswordCommandTimeout(config)
	if err != nil {
		return "", err
	}
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.Command(config.Command, config.Args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("postgres password_command failed to start: %w", err)
	}

	waitErr := make(chan error, 1)
	go func() {
		waitErr <- cmd.Wait()
	}()

	select {
	case err := <-waitErr:
		if err != nil {
			return "", passwordCommandError(err, stderr.String())
		}
	case <-cmdCtx.Done():
		_ = cmd.Process.Kill()
		drainedErr := <-waitErr
		if drainedErr == nil {
			// The command completed successfully while the deadline/cancel branch won the select race.
			break
		}
		if cmdCtx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("postgres password_command timed out after %s", timeout)
		}
		return "", fmt.Errorf("postgres password_command canceled: %w", cmdCtx.Err())
	}

	password := strings.TrimRight(stdout.String(), "\r\n")
	if password == "" {
		return "", fmt.Errorf("postgres password_command returned empty stdout")
	}
	return password, nil
}

// passwordCommandError includes stderr when a password command exits with an error.
func passwordCommandError(err error, stderr string) error {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return fmt.Errorf("postgres password_command failed: %w", err)
	}
	return fmt.Errorf("postgres password_command failed: %w: %s", err, stderr)
}

func quoteLibpqValue(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `'`, `\'`)
	return "'" + value + "'"
}

// validatePasswordCommand checks that password_command is a direct executable invocation.
func validatePasswordCommand(config *PasswordCommandConfig) error {
	if config == nil {
		return fmt.Errorf("postgres password_command config is required")
	}
	command := strings.TrimSpace(config.Command)
	if command == "" {
		return fmt.Errorf("postgres password_command.command is required")
	}
	if command != config.Command || strings.IndexFunc(command, unicode.IsSpace) >= 0 || strings.ContainsRune(command, 0) {
		return fmt.Errorf("postgres password_command.command must be a single executable path or name; pass arguments via password_command.args")
	}
	base := strings.ToLower(filepath.Base(command))
	base = strings.TrimSuffix(base, ".exe")
	switch base {
	case "sh", "bash", "dash", "zsh", "fish", "ksh", "cmd", "powershell", "pwsh":
		return fmt.Errorf("postgres password_command.command must not invoke a shell interpreter directly")
	}
	return nil
}

// parseConnMaxLifetime parses the optional physical connection lifetime.
func parseConnMaxLifetime(config *Config) (time.Duration, error) {
	if config == nil || config.ConnMaxLifetime == "" {
		return 0, nil
	}
	lifetime, err := time.ParseDuration(config.ConnMaxLifetime)
	if err != nil {
		return 0, fmt.Errorf("invalid postgres conn_max_lifetime %q: %w", config.ConnMaxLifetime, err)
	}
	if lifetime <= 0 {
		return 0, fmt.Errorf("postgres conn_max_lifetime must be positive")
	}
	return lifetime, nil
}

// parsePasswordCommandCacheTTL parses the optional password cache TTL.
func parsePasswordCommandCacheTTL(config *PasswordCommandConfig) (time.Duration, error) {
	if config == nil || config.CacheTTL == "" {
		return defaultPasswordCommandCacheTTL, nil
	}
	ttl, err := time.ParseDuration(config.CacheTTL)
	if err != nil {
		return 0, fmt.Errorf("invalid postgres password_command.cache_ttl %q: %w", config.CacheTTL, err)
	}
	if ttl <= 0 {
		return 0, fmt.Errorf("postgres password_command.cache_ttl must be positive")
	}
	return ttl, nil
}

// parseConnMaxIdleTime parses the optional idle-connection timeout, falling back
// to defaultConnMaxIdleTime when unset.
func parseConnMaxIdleTime(config *Config) (time.Duration, error) {
	if config == nil || config.ConnMaxIdleTime == "" {
		return defaultConnMaxIdleTime, nil
	}
	idleTime, err := time.ParseDuration(config.ConnMaxIdleTime)
	if err != nil {
		return 0, fmt.Errorf("invalid postgres conn_max_idle_time %q: %w", config.ConnMaxIdleTime, err)
	}
	if idleTime <= 0 {
		return 0, fmt.Errorf("postgres conn_max_idle_time must be positive")
	}
	return idleTime, nil
}

// parsePasswordCommandTimeout parses the optional password command timeout.
func parsePasswordCommandTimeout(config *PasswordCommandConfig) (time.Duration, error) {
	if config == nil || config.Timeout == "" {
		return defaultPasswordCommandTimeout, nil
	}
	timeout, err := time.ParseDuration(config.Timeout)
	if err != nil {
		return 0, fmt.Errorf("invalid postgres password_command.timeout %q: %w", config.Timeout, err)
	}
	if timeout <= 0 {
		return 0, fmt.Errorf("postgres password_command.timeout must be positive")
	}
	return timeout, nil
}
