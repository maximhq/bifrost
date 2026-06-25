package migrator

import (
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DropColumnIfExists drops the column backing struct field `field` of `model`
// in a way that is idempotent at the database statement level, so concurrent
// migration runners (rolling deploys, version skew, or a duplicate side path)
// and re-runs cannot fail with an "undefined column" error (PostgreSQL SQLSTATE
// 42703).
//
// GORM's Migrator.DropColumn emits a bare `ALTER TABLE ... DROP COLUMN`, and the
// usual `if HasColumn { DropColumn }` guard is a non-atomic check-then-act: two
// sessions can both observe the column as present and then both issue the DROP,
// and the loser fails when its DDL finally executes. On PostgreSQL we instead
// emit `ALTER TABLE ... DROP COLUMN IF EXISTS`, which is a true no-op when the
// column is already gone and therefore never aborts the surrounding migration
// transaction.
//
// Other dialects (e.g. SQLite) do not support `DROP COLUMN IF EXISTS`, so we
// keep the HasColumn guard and fall back to GORM's DropColumn there.
//
// The function owns the existence check, so call sites should NOT wrap it in
// their own `if HasColumn { ... }` guard — that just adds a second, redundant
// round-trip. The single HasColumn here also lets us emit the "dropping column"
// log line only when a column is actually dropped: pass a non-nil logger to get
// that line, or nil to stay silent.
func DropColumnIfExists(tx *gorm.DB, logger schemas.Logger, model interface{}, field string) error {
	mig := tx.Migrator()
	if !mig.HasColumn(model, field) {
		return nil
	}

	// Resolve the field to its DB column name so the DDL and log line match
	// what GORM's Migrator.DropColumn would target. Migrations routinely drop
	// *legacy* columns whose Go struct field has already been removed from the
	// model, so a missing field is expected: fall back to treating `field` as
	// the raw column name, exactly as GORM's DropColumn does.
	stmt := &gorm.Statement{DB: tx}
	if err := stmt.Parse(model); err != nil {
		return fmt.Errorf("failed to parse schema for %T: %w", model, err)
	}
	columnName := field
	if f := stmt.Schema.LookUpField(field); f != nil {
		columnName = f.DBName
	}

	if logger != nil {
		logger.Info("[migrator] dropping column %s from table %s", columnName, stmt.Table)
	}

	if tx.Dialector.Name() != "postgres" {
		return mig.DropColumn(model, field)
	}
	return tx.Exec(
		"ALTER TABLE ? DROP COLUMN IF EXISTS ?",
		clause.Table{Name: stmt.Table}, clause.Column{Name: columnName},
	).Error
}
