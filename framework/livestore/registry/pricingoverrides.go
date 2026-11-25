package registry

import (
	"context"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/queryscope"
	"gorm.io/gorm"
)

// PricingOverride is the record of one saved pricing override, the row's columns embedded, as the
// model catalog prices requests with it. Records are shared: readers never change them.
type PricingOverride struct {
	tables.TablePricingOverride
}

// PricingOverrideID is a pricing override record's primary key.
func PricingOverrideID(o *PricingOverride) string { return o.ID }

// PricingOverridesPage reads up to limit overrides with IDs after the cursor, in ID order.
func PricingOverridesPage(ctx context.Context, db *gorm.DB, after string, limit int) ([]*PricingOverride, string, error) {
	rows, err := readPricingOverrides(ctx, db, func(q *gorm.DB) *gorm.DB { return q.Where("id > ?", after).Order("id").Limit(limit) })
	if err != nil || len(rows) < limit {
		return rows, "", err
	}
	return rows, rows[len(rows)-1].ID, nil
}

// PricingOverridesByID reads the overrides with these IDs; one with no row is left out.
func PricingOverridesByID(ctx context.Context, db *gorm.DB, ids []string) ([]*PricingOverride, error) {
	return readPricingOverrides(ctx, db, func(q *gorm.DB) *gorm.DB { return q.Where(queryscope.InStrings(q, "id", ids)) })
}

// readPricingOverrides reads the overrides where selects.
func readPricingOverrides(ctx context.Context, db *gorm.DB, where func(*gorm.DB) *gorm.DB) ([]*PricingOverride, error) {
	var rows []tables.TablePricingOverride
	if err := where(db.WithContext(ctx)).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*PricingOverride, 0, len(rows))
	for i := range rows {
		out = append(out, &PricingOverride{TablePricingOverride: rows[i]})
	}
	return out, nil
}
