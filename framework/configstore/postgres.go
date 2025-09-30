package configstore

import (
	"context"

	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// PostgresConfig represents the configuration for a Postgres database.
type PostgresConfig struct {
	DSN string `json:"dsn"`
}

// newPostgresConfigStore creates a new Postgres config store.
func newPostgresConfigStore(ctx context.Context, config *PostgresConfig, logger schemas.Logger) (ConfigStore, error) {
	db, err := gorm.Open(postgres.Open(config.DSN), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	d := &RDBConfigStore{db: db, logger: logger}
	// Run migrations
	if err := triggerMigrations(ctx, db); err != nil {
		return nil, err
	}
	return d, nil
}
