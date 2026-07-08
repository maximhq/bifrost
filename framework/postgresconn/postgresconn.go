package postgresconn

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

const defaultPasswordCommandTimeout = 10 * time.Second

// PasswordCommandConfig describes a command that prints a Postgres password to stdout.
type PasswordCommandConfig struct {
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	Timeout string   `json:"timeout,omitempty"`
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
	sqlDB := stdlib.OpenDB(*pgxConfig, stdlib.OptionBeforeConnect(func(ctx context.Context, connConfig *pgx.ConnConfig) error {
		password, err := RunPasswordCommand(ctx, config.PasswordCommand)
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

// ApplyPoolTuning applies MaxIdleConns, MaxOpenConns, and ConnMaxLifetime.
func ApplyPoolTuning(db *gorm.DB, config *Config) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	maxIdleConns := config.MaxIdleConns
	if maxIdleConns == 0 {
		maxIdleConns = 5
	}
	sqlDB.SetMaxIdleConns(maxIdleConns)
	maxOpenConns := config.MaxOpenConns
	if maxOpenConns == 0 {
		maxOpenConns = 50
	}
	sqlDB.SetMaxOpenConns(maxOpenConns)
	if config.ConnMaxLifetime != "" {
		lifetime, err := parseConnMaxLifetime(config)
		if err != nil {
			return err
		}
		sqlDB.SetConnMaxLifetime(lifetime)
	}
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
