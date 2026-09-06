package configstore

import (
	"context"
	"errors"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// WarpStore is a narrow optional interface, following NotificationStore: adding
// Warp must not force every ConfigStore test double in the tree to grow methods
// it will never implement.
type WarpStore interface {
	// GetWarpConfig returns the singleton config row, or (nil, nil) when Warp
	// has never been configured. A missing row is an expected state, not an
	// error — most deployments never turn Warp on.
	GetWarpConfig(ctx context.Context) (*tables.TableWarpConfig, error)
	// UpsertWarpConfig writes the singleton row.
	UpsertWarpConfig(ctx context.Context, config *tables.TableWarpConfig) error
}

// GetWarpConfig reads the singleton Warp row. A missing row returns (nil, nil):
// most deployments never turn Warp on, so absence is an ordinary state that
// callers should not have to distinguish from a failure at every call site.
func (s *RDBConfigStore) GetWarpConfig(ctx context.Context) (*tables.TableWarpConfig, error) {
	var config tables.TableWarpConfig
	err := s.DB().WithContext(ctx).First(&config, tables.WarpConfigRowID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &config, nil
}

// UpsertWarpConfig writes the singleton Warp row, pinning the primary key so a
// caller that left ID unset cannot insert a second, autoincremented row that
// GetWarpConfig would never find.
func (s *RDBConfigStore) UpsertWarpConfig(ctx context.Context, config *tables.TableWarpConfig) error {
	// Pin the ID rather than trusting the caller: the table is a singleton by
	// contract, and a caller that passed 0 would otherwise have GORM insert a
	// second, autoincremented row that the read path above would never find.
	config.ID = tables.WarpConfigRowID
	// An explicit ON CONFLICT upsert rather than Save. Save's behaviour for a
	// non-zero primary key with no matching row differs by GORM version and
	// dialect - it can issue an UPDATE that quietly affects zero rows - and
	// "the settings saved but nothing changed" is a bug nobody can see.
	return s.DB().WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "id"}},
		UpdateAll: true,
	}).Create(config).Error
}
