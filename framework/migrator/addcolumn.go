package migrator

import (
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AddColumnIfNotExists adds the column backing struct field `field` of `model`
// in a way that is idempotent at the database statement level, so concurrent
// migration runners (rolling deploys, version skew, or a duplicate side path)
// and re-runs cannot fail with a duplicate-column error (PostgreSQL SQLSTATE
// 42701).
//
// GORM's Migrator.AddColumn emits a bare `ALTER TABLE ... ADD COLUMN`, and the
// usual `if !HasColumn { AddColumn }` guard is a non-atomic check-then-act: two
// sessions can both observe the column as absent and then both issue the ADD,
// and the loser fails when its DDL finally executes. On PostgreSQL we instead
// emit `ALTER TABLE ... ADD COLUMN IF NOT EXISTS`, which is a true no-op when
// the column already exists and therefore never aborts the surrounding
// migration transaction.
//
// Other dialects (e.g. SQLite) do not support `ADD COLUMN IF NOT EXISTS`, so we
// keep the HasColumn guard and fall back to GORM's AddColumn there.
//
// The function owns the existence check, so call sites should NOT wrap it in
// their own `if !HasColumn { ... }` guard — that just adds a second, redundant
// round-trip. The single HasColumn here also lets us emit the "adding column"
// log line only when a column is actually added: pass a non-nil logger to get
// that line, or nil to stay silent.
func AddColumnIfNotExists(tx *gorm.DB, logger schemas.Logger, model interface{}, field string) error {
	mig := tx.Migrator()
	if mig.HasColumn(model, field) {
		return nil
	}

	// Replicate GORM's Migrator.AddColumn DDL with an IF NOT EXISTS guard.
	stmt := &gorm.Statement{DB: tx}
	if err := stmt.Parse(model); err != nil {
		return fmt.Errorf("failed to parse schema for %T: %w", model, err)
	}
	f := stmt.Schema.LookUpField(field)
	if f == nil {
		return fmt.Errorf("failed to look up field with name: %s", field)
	}
	if f.IgnoreMigration {
		return nil
	}

	if logger != nil {
		logger.Info("[migrator] adding column %s to table %s", f.DBName, stmt.Table)
	}

	if tx.Dialector.Name() != "postgres" {
		return mig.AddColumn(model, field)
	}
	return tx.Exec(
		"ALTER TABLE ? ADD COLUMN IF NOT EXISTS ? ?",
		clause.Table{Name: stmt.Table}, clause.Column{Name: f.DBName}, mig.FullDataTypeOf(f),
	).Error
}
