package queue

import (
	"context"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/migrator"
	"gorm.io/gorm"
)

// sqlTableMigration creates one queue table and the indexes its struct tags
// declare. IDs share the logstore's migrations ledger, hence the prefix.
type sqlTableMigration struct {
	id      string
	model   any
	indexes []string
}

var sqlMigrations = []sqlTableMigration{
	{id: "queue_topics_init", model: &tableQueueTopic{}},
	{id: "queue_groups_init", model: &tableQueueGroup{}},
	{id: "queue_messages_init", model: &tableQueueMessage{}, indexes: []string{
		"idx_queue_messages_id",
		"idx_queue_messages_topic_created",
		"idx_queue_messages_created",
	}},
	{id: "queue_deliveries_init", model: &tableQueueDelivery{}, indexes: []string{
		"idx_queue_deliveries_id",
		"idx_queue_deliveries_message_group",
		"idx_queue_deliveries_ready",
		"idx_queue_deliveries_key",
		"idx_queue_deliveries_claim_token",
		"idx_queue_deliveries_created",
	}},
}

// runSQLMigrations creates the queue tables. Each step is idempotent.
func runSQLMigrations(ctx context.Context, db *gorm.DB, logger schemas.Logger) error {
	steps := make([]*migrator.Migration, 0, len(sqlMigrations))
	for _, m := range sqlMigrations {
		steps = append(steps, &migrator.Migration{
			ID: m.id,
			Migrate: func(tx *gorm.DB) error {
				tx = tx.WithContext(ctx)
				mg := tx.Migrator()
				if !mg.HasTable(m.model) {
					if logger != nil {
						logger.Info("[queue] %s: creating table", m.id)
					}
					if err := mg.CreateTable(m.model); err != nil {
						return err
					}
				}
				for _, idx := range m.indexes {
					if mg.HasIndex(m.model, idx) {
						continue
					}
					if err := mg.CreateIndex(m.model, idx); err != nil {
						return fmt.Errorf("create index %s: %w", idx, err)
					}
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.WithContext(ctx).Migrator().DropTable(m.model)
			},
		})
	}
	if err := migrator.New(db, migrator.DefaultOptions, steps).Migrate(); err != nil {
		return fmt.Errorf("queue migrations: %w", err)
	}
	return nil
}
