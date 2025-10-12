package configstore

import (
	"context"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// MySQLConfig represents the configuration for a MySQL database.
type MySQLConfig struct {
	User string `json:"user"`
	Password string `json:"password"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	DBName   string `json:"db_name"`
	SSLMode  string `json:"ssl_mode"`
}

// newMySQLConfigStore creates a new MySQL config store.
func newMySQLConfigStore(ctx context.Context, config *MySQLConfig, logger schemas.Logger) (ConfigStore, error) {
	db, err := gorm.Open(mysql.Open(fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local&sslmode=%s", config.User, config.Password, config.Host, config.Port, config.DBName, config.SSLMode)), &gorm.Config{})
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
