package postgresconn

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
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
	Host            *schemas.EnvVar        `json:"host"`
	Port            *schemas.EnvVar        `json:"port"`
	User            *schemas.EnvVar        `json:"user"`
	Password        *schemas.EnvVar        `json:"password"`
	PasswordCommand *PasswordCommandConfig `json:"password_command,omitempty"`
	DBName          *schemas.EnvVar        `json:"db_name"`
	SSLMode         *schemas.EnvVar        `json:"ssl_mode"`
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
	if config.PasswordCommand != nil {
		if config.PasswordCommand.Command == "" {
			return fmt.Errorf("postgres password_command.command is required")
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
		config.Host.GetValue(), config.Port.GetValue(), config.User.GetValue(),
		password, config.DBName.GetValue(), config.SSLMode.GetValue())
}

// Open opens a *gorm.DB against the configured Postgres instance.
func Open(dsn string, config *Config, logger schemas.Logger) (*gorm.DB, error) {
	if config.PasswordCommand == nil {
		return gorm.Open(postgres.New(postgres.Config{DSN: dsn}), &gorm.Config{
			Logger: NewGormLogger(logger),
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

func openGormFromSQLDB(sqlDB *sql.DB, logger schemas.Logger) (*gorm.DB, error) {
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		Logger: NewGormLogger(logger),
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
		lifetime, err := time.ParseDuration(config.ConnMaxLifetime)
		if err != nil {
			return fmt.Errorf("invalid postgres conn_max_lifetime %q: %w", config.ConnMaxLifetime, err)
		}
		sqlDB.SetConnMaxLifetime(lifetime)
	}
	return nil
}

// Close closes the *sql.DB backing a *gorm.DB, logging any error.
func Close(db *gorm.DB, logger schemas.Logger) {
	sqlDB, err := db.DB()
	if err != nil {
		logger.Error("failed to resolve *sql.DB for close: %v", err)
		return
	}
	if err := sqlDB.Close(); err != nil {
		logger.Error("failed to close DB connection: %v", err)
	}
}

// RunPasswordCommand executes a configured password command and returns stdout.
func RunPasswordCommand(ctx context.Context, config *PasswordCommandConfig) (string, error) {
	timeout, err := parsePasswordCommandTimeout(config)
	if err != nil {
		return "", err
	}
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, config.Command, config.Args...)
	output, err := cmd.Output()
	if cmdCtx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("postgres password_command timed out after %s", timeout)
	}
	if err != nil {
		return "", fmt.Errorf("postgres password_command failed: %w", err)
	}
	password := strings.TrimRight(string(output), "\r\n")
	if password == "" {
		return "", fmt.Errorf("postgres password_command returned empty stdout")
	}
	return password, nil
}

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
