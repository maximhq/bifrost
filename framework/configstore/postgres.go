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
	Username string `json:"username"`
	Password string `json:"password"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	DBName   string `json:"db_name"`
	SSLMode  string `json:"ssl_mode"`
}

// newPostgresConfigStore creates a new Postgres config store.
func newPostgresConfigStore(ctx context.Context, config *PostgresConfig, logger schemas.Logger) (ConfigStore, error) {
	dsn := fmt.Sprintf("host=%s user=%s password=%s dbname=%s port=%d sslmode=%s", 
		config.Host, config.Username, config.Password, config.DBName, config.Port, config.SSLMode)
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
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
