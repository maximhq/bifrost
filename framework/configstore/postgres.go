package configstore

import (
	"context"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// PostgresConfig represents the configuration for a Postgres database.
type PostgresConfig struct {
	Host         *schemas.EnvVar `json:"host"`
	Port         *schemas.EnvVar `json:"port"`
	User         *schemas.EnvVar `json:"user"`
	Password     *schemas.EnvVar `json:"password"`
	DBName       *schemas.EnvVar `json:"db_name"`
	SSLMode      *schemas.EnvVar `json:"ssl_mode"`
	MaxIdleConns int             `json:"max_idle_conns"`
	MaxOpenConns int             `json:"max_open_conns"`
}

// buildPostgresDSN assembles a libpq-style DSN from the validated config.
func buildPostgresDSN(config *PostgresConfig) string {
	return fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		config.Host.GetValue(), config.Port.GetValue(), config.User.GetValue(),
		config.Password.GetValue(), config.DBName.GetValue(), config.SSLMode.GetValue())
}

// openPostresConnection opens a *gorm.DB against the configured Postgres instance
// using the shared bifrost logger. Used for both the throwaway migration pool
// and the runtime pool.
func openPostresConnection(dsn string, logger schemas.Logger) (*gorm.DB, error) {
	return gorm.Open(postgres.New(postgres.Config{DSN: dsn}), &gorm.Config{
		Logger: newGormLogger(logger),
	})
}

// closeDbConn closes the *sql.DB backing a *gorm.DB, logging any error.
// Used in error paths and for the throwaway migration pool.
func closeDbConn(db *gorm.DB, logger schemas.Logger) {
	sqlDB, err := db.DB()
	if err != nil {
		logger.Error("failed to resolve *sql.DB for close: %v", err)
		return
	}
	if err := sqlDB.Close(); err != nil {
		logger.Error("failed to close DB connection: %v", err)
	}
}

// applyPostgresPoolTuning applies MaxIdleConns / MaxOpenConns from config to
// the supplied *gorm.DB, falling back to defaults when the config leaves the
// field at zero.
func applyPostgresPoolTuning(db *gorm.DB, config *PostgresConfig) error {
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
	return nil
}

// newPostgresConfigStore creates a new Postgres config store.
//
// Uses a two-pool lifecycle to avoid SQLSTATE 0A000 ("cached plan must not
// change result type"): a throwaway migration pool runs DDL and is closed
// immediately, then a fresh runtime pool is opened. The runtime pool's
// connections never see pre-migration schema, so their cached prepared-plans
// stay valid for the life of the process.
func newPostgresConfigStore(ctx context.Context, config *PostgresConfig, logger schemas.Logger) (ConfigStore, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}
	if config.Host == nil || config.Host.GetValue() == "" {
		return nil, fmt.Errorf("postgres host is required")
	}
	if config.Port == nil || config.Port.GetValue() == "" {
		return nil, fmt.Errorf("postgres port is required")
	}
	if config.User == nil || config.User.GetValue() == "" {
		return nil, fmt.Errorf("postgres user is required")
	}
	if config.Password == nil {
		return nil, fmt.Errorf("postgres password is required")
	}
	if config.DBName == nil || config.DBName.GetValue() == "" {
		return nil, fmt.Errorf("postgres db name is required")
	}
	if config.SSLMode == nil || config.SSLMode.GetValue() == "" {
		return nil, fmt.Errorf("postgres ssl mode is required")
	}
	dsn := buildPostgresDSN(config)

	// Throwaway pool for schema migrations. Closing it before the runtime pool
	// opens guarantees no cached prepared-plan survives the DDL.
	mDb, err := openPostresConnection(dsn, logger)
	if err != nil {
		return nil, err
	}
	if err := triggerMigrations(ctx, mDb); err != nil {
		closeDbConn(mDb, logger)
		return nil, err
	}
	closeDbConn(mDb, logger)

	// Runtime pool. Opens against post-migration schema.
	db, err := openPostresConnection(dsn, logger)
	if err != nil {
		return nil, err
	}
	if err := applyPostgresPoolTuning(db, config); err != nil {
		closeDbConn(db, logger)
		return nil, err
	}

	d := &RDBConfigStore{logger: logger}
	d.db.Store(db)

	// migrateOnFreshFn: downstream consumers (e.g. bifrost-enterprise) run
	// their migrations via this hook on a throwaway pool that closes after fn.
	d.migrateOnFreshFn = func(ctx context.Context, fn func(context.Context, *gorm.DB) error) error {
		tempDB, err := openPostresConnection(dsn, logger)
		if err != nil {
			return err
		}
		defer closeDbConn(tempDB, logger)
		return fn(ctx, tempDB)
	}

	// refreshPoolFn: open fresh runtime pool first (so a failure leaves the
	// existing pool in place), swap atomically, then close the old pool.
	// sql.DB.Close blocks until in-flight queries finish, so callers already
	// using the old pool complete safely.
	d.refreshPoolFn = func(ctx context.Context) error {
		newDB, err := openPostresConnection(dsn, logger)
		if err != nil {
			return fmt.Errorf("failed to open fresh runtime pool: %w", err)
		}
		if err := applyPostgresPoolTuning(newDB, config); err != nil {
			closeDbConn(newDB, logger)
			return fmt.Errorf("failed to tune fresh runtime pool: %w", err)
		}
		oldDB := d.db.Swap(newDB)
		if oldDB != nil {
			closeDbConn(oldDB, logger)
		}
		return nil
	}

	// Encrypt any plaintext rows if encryption is enabled. Runs on the
	// runtime pool — pure DML (SELECT + UPDATE), no DDL, so cached plans it
	// installs remain valid until the next external migration batch.
	if err := d.EncryptPlaintextRows(ctx); err != nil {
		closeDbConn(db, logger)
		return nil, fmt.Errorf("failed to encrypt plaintext rows: %w", err)
	}
	return d, nil
}
