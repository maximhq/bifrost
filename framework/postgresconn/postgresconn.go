package postgresconn

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
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
	Host            *schemas.SecretVar     `json:"host"`
	Port            *schemas.SecretVar     `json:"port"`
	User            *schemas.SecretVar     `json:"user"`
	Password        *schemas.SecretVar     `json:"password"`
	PasswordCommand *PasswordCommandConfig `json:"password_command,omitempty"`
	DBName          *schemas.SecretVar     `json:"db_name"`
	SSLMode         *schemas.SecretVar     `json:"ssl_mode"`
	MaxIdleConns    int                    `json:"max_idle_conns"`
	MaxOpenConns    int                    `json:"max_open_conns"`
	ConnMaxLifetime string                 `json:"conn_max_lifetime,omitempty"`
	// ConnMaxIdleTime bounds how long an idle physical connection is kept before
	// being closed. Without it, the idle cap is the only thing controlling pool
	// size, so every burst above MaxIdleConns closes connections on return and
	// reopens them on the next query — each reopen forks a Postgres backend.
	ConnMaxIdleTime string `json:"conn_max_idle_time,omitempty"`
	// StatementTimeout is the server-side statement_timeout set on every runtime
	// pool connection (Go duration string, default 120s; "0" or a negative value
	// leaves the server default). It bounds a runaway query so it cannot hold a
	// pooled connection and its locks indefinitely. Migration pools never get it:
	// they run long index builds and backfills.
	StatementTimeout string `json:"statement_timeout,omitempty"`
	// IdleInTransactionSessionTimeout is the server-side
	// idle_in_transaction_session_timeout set on every runtime pool connection
	// (Go duration string, default 120s; "0" or a negative value leaves the server
	// default). It ends a session left idle inside an open transaction, which
	// otherwise holds row locks and blocks vacuum for as long as it stays open.
	IdleInTransactionSessionTimeout string `json:"idle_in_transaction_session_timeout,omitempty"`
}

const (
	// defaultStatementTimeout bounds a single statement on runtime pools.
	defaultStatementTimeout = 120 * time.Second
	// defaultIdleInTransactionSessionTimeout bounds how long a runtime session
	// may sit idle inside an open transaction.
	defaultIdleInTransactionSessionTimeout = 120 * time.Second
)

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
	if _, err := parseStatementTimeout(config); err != nil {
		return err
	}
	if _, err := parseIdleInTransactionSessionTimeout(config); err != nil {
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
//
// A runtime pool gets the configured statement_timeout and
// idle_in_transaction_session_timeout on every physical connection. A migration
// pool does not: configstore and logstore build their migration DSN by forcing
// default_query_exec_mode=simple_protocol, and that is how Open tells the two
// apart (see runtimeSessionSettingsSQL).
func Open(dsn string, config *Config, logger gormlogger.Interface) (*gorm.DB, error) {
	sessionSQL, err := runtimeSessionSettingsSQL(dsn, config)
	if err != nil {
		return nil, err
	}
	if config.PasswordCommand == nil && sessionSQL == "" {
		return gorm.Open(postgres.New(postgres.Config{DSN: dsn}), &gorm.Config{
			Logger: logger,
		})
	}

	pgxConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	var options []stdlib.OptionOpenDB
	if config.PasswordCommand != nil {
		// One cache per pool. BeforeConnect runs for every new physical connection, so
		// without this a bursty workload forks the password command once per connection.
		cache := &passwordCache{}
		options = append(options, stdlib.OptionBeforeConnect(func(ctx context.Context, connConfig *pgx.ConnConfig) error {
			password, err := cache.get(ctx, config.PasswordCommand)
			if err != nil {
				return err
			}
			connConfig.Password = password
			return nil
		}))
	}
	if sessionSQL != "" {
		// Applied with SET semantics after connecting rather than as startup
		// parameters, because connection poolers such as PgBouncer reject unknown
		// startup parameters and would refuse every connection.
		options = append(options, stdlib.OptionAfterConnect(func(ctx context.Context, conn *pgx.Conn) error {
			if _, err := conn.Exec(ctx, sessionSQL); err != nil {
				return fmt.Errorf("failed to apply postgres session timeouts: %w", err)
			}
			return nil
		}))
	}
	connector := stdlib.GetConnector(*pgxConfig, options...)
	sqlDB := sql.OpenDB(poolConnector{connector})
	return openGormFromSQLDB(sqlDB, logger)
}

// poolConnector opens a pool's connections. The driver it reports carries it,
// so ConnectUnpooled can reach it from the pool.
type poolConnector struct{ driver.Connector }

func (c poolConnector) Driver() driver.Driver {
	return poolDriver{Driver: c.Connector.Driver(), connector: c.Connector}
}

type poolDriver struct {
	driver.Driver
	connector driver.Connector
}

// ConnectUnpooled opens a connection that sqlDB does not manage, the same way
// sqlDB opens its own: the password command runs again and the session
// settings apply. The caller closes it. ok is false when sqlDB was not opened
// that way; its connections then need nothing beyond the settings an existing
// one was opened with.
func ConnectUnpooled(ctx context.Context, sqlDB *sql.DB) (conn *pgx.Conn, ok bool, err error) {
	d, ok := sqlDB.Driver().(poolDriver)
	if !ok {
		return nil, false, nil
	}
	dc, err := d.connector.Connect(ctx)
	if err != nil {
		return nil, true, err
	}
	sc, isPgx := dc.(*stdlib.Conn)
	if !isPgx {
		_ = dc.Close()
		return nil, true, fmt.Errorf("postgres driver connection %T", dc)
	}
	return sc.Conn(), true, nil
}

// runtimeSessionSettingsSQL returns the statement that applies the runtime
// session timeouts to a new connection, or "" when none apply: both are
// disabled, or dsn is a migration DSN (simple query protocol).
func runtimeSessionSettingsSQL(dsn string, config *Config) (string, error) {
	statementTimeout, err := parseStatementTimeout(config)
	if err != nil {
		return "", err
	}
	idleTimeout, err := parseIdleInTransactionSessionTimeout(config)
	if err != nil {
		return "", err
	}
	if statementTimeout <= 0 && idleTimeout <= 0 {
		return "", nil
	}
	pgxConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		return "", err
	}
	if pgxConfig.DefaultQueryExecMode == pgx.QueryExecModeSimpleProtocol {
		return "", nil
	}
	var settings []string
	if statementTimeout > 0 {
		settings = append(settings, fmt.Sprintf("set_config('statement_timeout', '%d', false)", durationMillis(statementTimeout)))
	}
	if idleTimeout > 0 {
		settings = append(settings, fmt.Sprintf("set_config('idle_in_transaction_session_timeout', '%d', false)", durationMillis(idleTimeout)))
	}
	return "SELECT " + strings.Join(settings, ", "), nil
}

// durationMillis converts a positive duration to whole milliseconds, rounding a
// sub-millisecond value up to 1 so it never turns into "disabled".
func durationMillis(d time.Duration) int64 {
	ms := d.Milliseconds()
	if ms < 1 {
		return 1
	}
	return ms
}

// sqlExecQueryer is the subset of *sql.Conn (and *sql.Tx) the statement-timeout
// escape hatch needs.
type sqlExecQueryer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// DisableStatementTimeout lifts statement_timeout on one dedicated connection
// for runtime work that legitimately runs long (index builds, materialized view
// creation and refresh). It returns a function that restores the connection's
// previous value; call it before the connection goes back to the pool, or close
// the connection instead. RESET is not a substitute: the pool's timeout is set
// after connect, so RESET would drop the connection to the server default.
func DisableStatementTimeout(ctx context.Context, conn sqlExecQueryer) (func(context.Context) error, error) {
	var previous string
	if err := conn.QueryRowContext(ctx, "SELECT current_setting('statement_timeout')").Scan(&previous); err != nil {
		return nil, fmt.Errorf("failed to read statement_timeout: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "SELECT set_config('statement_timeout', '0', false)"); err != nil {
		return nil, fmt.Errorf("failed to disable statement_timeout: %w", err)
	}
	return func(ctx context.Context) error {
		if _, err := conn.ExecContext(ctx, "SELECT set_config('statement_timeout', $1, false)", previous); err != nil {
			return fmt.Errorf("failed to restore statement_timeout: %w", err)
		}
		return nil
	}, nil
}

// rawConn is the subset of *sql.Conn needed to drop a physical connection.
type rawConn interface {
	Raw(f func(driverConn any) error) error
}

// RestoreStatementTimeoutOrDiscard runs restore, the function returned by
// DisableStatementTimeout, and enforces that function's contract: when the
// restore fails, conn is dropped (a Raw callback returning driver.ErrBadConn)
// so database/sql closes the physical connection instead of pooling it with
// statement_timeout lifted. The restore runs under timeout and ignores the
// cancellation of ctx, which is often already done when cleanup runs.
func RestoreStatementTimeoutOrDiscard(ctx context.Context, conn rawConn, restore func(context.Context) error, timeout time.Duration) error {
	restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	if err := restore(restoreCtx); err != nil {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		return err
	}
	return nil
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

// quoteLibpqValue single-quotes a libpq keyword/value DSN value, escaping
// backslashes and quotes.
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

// parseStatementTimeout parses the runtime statement_timeout, falling back to
// defaultStatementTimeout when unset. Zero or negative disables it (returns 0).
func parseStatementTimeout(config *Config) (time.Duration, error) {
	if config == nil {
		return defaultStatementTimeout, nil
	}
	return parseSessionTimeout(config.StatementTimeout, defaultStatementTimeout, "statement_timeout")
}

// parseIdleInTransactionSessionTimeout parses the runtime
// idle_in_transaction_session_timeout, falling back to
// defaultIdleInTransactionSessionTimeout when unset. Zero or negative disables it.
func parseIdleInTransactionSessionTimeout(config *Config) (time.Duration, error) {
	if config == nil {
		return defaultIdleInTransactionSessionTimeout, nil
	}
	return parseSessionTimeout(config.IdleInTransactionSessionTimeout, defaultIdleInTransactionSessionTimeout, "idle_in_transaction_session_timeout")
}

// parseSessionTimeout parses one optional session timeout. "" takes fallback;
// "0" (with or without a unit) or a negative duration returns 0, meaning off.
func parseSessionTimeout(raw string, fallback time.Duration, name string) (time.Duration, error) {
	if raw == "" {
		return fallback, nil
	}
	if raw == "0" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid postgres %s %q: %w", name, raw, err)
	}
	if d <= 0 {
		return 0, nil
	}
	return d, nil
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
