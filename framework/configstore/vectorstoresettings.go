package configstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// MaxVectorRetentionDays is the longest retention_days whose span fits in a
// time.Duration (about 292 years): a longer one would wrap the sweep's cutoff into
// the future and delete every vector. Enforced at every entry point, matching the
// maximum published in config.schema.json.
const MaxVectorRetentionDays = int(math.MaxInt64 / int64(24*time.Hour))

// MaxVectorMaxSizeMB is the largest max_size_mb whose size in bytes fits in an
// int64: a larger one would overflow the sweep's limit into a negative number.
// Enforced at every entry point, matching the maximum published in
// config.schema.json and the Helm chart's values.schema.json.
const MaxVectorMaxSizeMB = int(math.MaxInt64 >> 20)

// ValidateVectorStoreSettings checks one store's retention_days and max_size_mb.
func ValidateVectorStoreSettings(retentionDays, maxSizeMB int) error {
	if retentionDays < 0 || retentionDays > MaxVectorRetentionDays {
		return fmt.Errorf("retention_days %d must be between 0 and %d", retentionDays, MaxVectorRetentionDays)
	}
	if maxSizeMB < 0 || maxSizeMB > MaxVectorMaxSizeMB {
		return fmt.Errorf("max_size_mb %d must be between 0 and %d", maxSizeMB, MaxVectorMaxSizeMB)
	}
	return nil
}

// GenerateVectorStoreSettingsHash is the hash of the settings config.json gives a
// store, stored with its row so a restart can tell a changed config.json value,
// which wins, from an unchanged one, which leaves a dashboard edit in place.
func GenerateVectorStoreSettingsHash(retentionDays, maxSizeMB int) string {
	sum := sha256.Sum256([]byte("retention_days:" + strconv.Itoa(retentionDays) + "|max_size_mb:" + strconv.Itoa(maxSizeMB)))
	return hex.EncodeToString(sum[:])
}

// ListVectorStoreSettings returns every stored vector store's settings, by name.
func (s *RDBConfigStore) ListVectorStoreSettings(ctx context.Context) ([]tables.TableVectorStoreSettings, error) {
	var rows []tables.TableVectorStoreSettings
	if err := s.DB().WithContext(ctx).Order("name").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// GetVectorStoreSettings returns one vector store's settings, or ErrNotFound.
func (s *RDBConfigStore) GetVectorStoreSettings(ctx context.Context, name string) (*tables.TableVectorStoreSettings, error) {
	var row tables.TableVectorStoreSettings
	if err := s.DB().WithContext(ctx).Where("name = ?", name).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &row, nil
}

// UpsertVectorStoreSettings writes one vector store's settings, creating its row
// or replacing its values and config hash.
func (s *RDBConfigStore) UpsertVectorStoreSettings(ctx context.Context, settings *tables.TableVectorStoreSettings, tx ...*gorm.DB) error {
	db := s.DB()
	if len(tx) > 0 && tx[0] != nil {
		db = tx[0]
	}
	return db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "name"}},
		DoUpdates: clause.AssignmentColumns([]string{"retention_days", "max_size_mb", "config_hash", "updated_at"}),
	}).Create(settings).Error
}

// UpdateVectorStoreSettings writes only the settings given, creating the row with the others
// at 0 when there is none. A save that changes one setting never writes back the other from an
// earlier read, so an overlapping save of the other setting is not undone. The config hash is
// left as it is: the edit lasts until config.json changes the value.
//
// It returns the row as it was before the write (nil when there was none) and as the write left
// it, both read in the write's transaction under the row's lock: an overlapping save of the other
// setting that committed first shows in them, as it does in the database, so a record of the
// change is the change that was stored.
func (s *RDBConfigStore) UpdateVectorStoreSettings(ctx context.Context, name string, retentionDays, maxSizeMB *int) (before, after *tables.TableVectorStoreSettings, err error) {
	row := &tables.TableVectorStoreSettings{Name: name}
	columns := []string{"updated_at"}
	if retentionDays != nil {
		row.RetentionDays = *retentionDays
		columns = append(columns, "retention_days")
	}
	if maxSizeMB != nil {
		row.MaxSizeMB = *maxSizeMB
		columns = append(columns, "max_size_mb")
	}
	err = s.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var stored tables.TableVectorStoreSettings
		switch err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("name = ?", name).First(&stored).Error; {
		case errors.Is(err, gorm.ErrRecordNotFound):
			before = nil
		case err != nil:
			return err
		default:
			before = &stored
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "name"}},
			DoUpdates: clause.AssignmentColumns(columns),
		}).Create(row).Error; err != nil {
			return err
		}
		var written tables.TableVectorStoreSettings
		if err := tx.Where("name = ?", name).First(&written).Error; err != nil {
			return err
		}
		after = &written
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return before, after, nil
}
