package configstore

import (
	"context"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/migrator"
	"gorm.io/gorm"
)

// forkMigrationSteps are the configstore migrations this fork adds on top of upstream Bifrost.
// They live in their own file and are appended after every upstream step at package init, so
// migrations.go stays identical to upstream and upstream syncs never conflict on the step list.
// Steps are matched by ID, so an upstream step that lands later still runs on databases that
// already applied these.
var forkMigrationSteps = []migrationStep{
	{IDs: []string{"add_key_selection_json_column"}, run: migrationAddKeySelectionJSONColumn},
}

func init() {
	configstoreMigrationSteps = append(configstoreMigrationSteps, forkMigrationSteps...)
}

// migrationAddKeySelectionJSONColumn adds the key_selection_json column to the provider
// table, backing ProviderConfig.KeySelection (per-provider key rotation strategy).
func migrationAddKeySelectionJSONColumn(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	migrationName := "add_key_selection_json_column"
	logger.Info("[configstore] starting migration %s", migrationName)
	defer logger.Info("[configstore] finished migration %s", migrationName)
	m := migrator.New(db, migrator.DefaultOptions, []*migrator.Migration{{
		ID: migrationName,
		Migrate: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			return addColumnIfNotExists(tx, logger, &tables.TableProvider{}, "KeySelectionJSON")
		},
		Rollback: func(tx *gorm.DB) error {
			tx = tx.WithContext(ctx)
			return dropColumnIfExists(tx, logger, &tables.TableProvider{}, "key_selection_json")
		},
	}})
	if err := m.Migrate(); err != nil {
		return fmt.Errorf("error while running %s migration: %s", migrationName, err.Error())
	}
	return nil
}
