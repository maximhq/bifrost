package configstore

import (
	"context"
	"errors"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// OdinStore is a narrow optional interface, following NotificationStore: adding
// Odin must not force every ConfigStore test double in the tree to grow methods
// it will never implement.
type OdinStore interface {
	// GetOdinConfig returns the singleton config row, or (nil, nil) when Odin
	// has never been configured. A missing row is an expected state, not an
	// error — most deployments never turn Odin on.
	GetOdinConfig(ctx context.Context) (*tables.TableOdinConfig, error)
	// UpsertOdinConfig writes the singleton row.
	UpsertOdinConfig(ctx context.Context, config *tables.TableOdinConfig) error
}

func (s *RDBConfigStore) GetOdinConfig(ctx context.Context) (*tables.TableOdinConfig, error) {
	var config tables.TableOdinConfig
	err := s.DB().WithContext(ctx).First(&config, tables.OdinConfigRowID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &config, nil
}

func (s *RDBConfigStore) UpsertOdinConfig(ctx context.Context, config *tables.TableOdinConfig) error {
	// Pin the ID rather than trusting the caller: the table is a singleton by
	// contract, and a caller that passed 0 would otherwise have GORM insert a
	// second, autoincremented row that the read path above would never find.
	config.ID = tables.OdinConfigRowID
	// An explicit ON CONFLICT upsert rather than Save. Save's behaviour for a
	// non-zero primary key with no matching row differs by GORM version and
	// dialect - it can issue an UPDATE that quietly affects zero rows - and
	// "the settings saved but nothing changed" is a bug nobody can see.
	return s.DB().WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		UpdateAll: true,
	}).Create(config).Error
}
