package registry

import (
	"context"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/queryscope"
	"gorm.io/gorm"
)

// VectorStoreSettings is one configured vector store's settings, keyed by the store's name:
// the row's columns, embedded. The hourly sweeps read it for how long vectors built from logs
// are kept in the store and for its storage limit. Records are shared: readers never change them.
type VectorStoreSettings struct {
	tables.TableVectorStoreSettings
}

// VectorStoreSettingsName is a settings record's primary key, the store's name.
func VectorStoreSettingsName(v *VectorStoreSettings) string { return v.Name }

// VectorStoreSettingsPage reads up to limit settings rows with names after the cursor, in name order.
func VectorStoreSettingsPage(ctx context.Context, db *gorm.DB, after string, limit int) ([]*VectorStoreSettings, string, error) {
	rows, err := readVectorStoreSettings(ctx, db, func(q *gorm.DB) *gorm.DB { return q.Where("name > ?", after).Order("name").Limit(limit) })
	if err != nil || len(rows) < limit {
		return rows, "", err
	}
	return rows, rows[len(rows)-1].Name, nil
}

// VectorStoreSettingsByName reads the settings of these stores; a name with no row is left out.
func VectorStoreSettingsByName(ctx context.Context, db *gorm.DB, names []string) ([]*VectorStoreSettings, error) {
	return readVectorStoreSettings(ctx, db, func(q *gorm.DB) *gorm.DB { return q.Where(queryscope.InStrings(q, "name", names)) })
}

func readVectorStoreSettings(ctx context.Context, db *gorm.DB, where func(*gorm.DB) *gorm.DB) ([]*VectorStoreSettings, error) {
	var rows []tables.TableVectorStoreSettings
	if err := where(db.WithContext(ctx)).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*VectorStoreSettings, 0, len(rows))
	for i := range rows {
		out = append(out, &VectorStoreSettings{TableVectorStoreSettings: rows[i]})
	}
	return out, nil
}
