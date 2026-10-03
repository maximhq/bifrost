package migrator

import (
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/schema"
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
		if err := mig.AddColumn(model, field); err != nil {
			return err
		}
		return createFieldIndexes(tx, logger, model, stmt, f)
	}
	return tx.Exec(
		"ALTER TABLE ? ADD COLUMN IF NOT EXISTS ? ?",
		clause.Table{Name: stmt.Table}, clause.Column{Name: f.DBName}, mig.FullDataTypeOf(f),
	).Error
}

// CreateDeclaredIndexes creates every not-yet-existing index that model
// declares by struct tag, skipping any whose columns the table does not have.
//
// It exists to backfill databases whose columns were added before the adder
// created indexes: those migrations are recorded, so they never run again, and
// the index would otherwise stay missing for the life of the installation.
// Callers must keep it away from Postgres, which builds the same set
// CONCURRENTLY outside any migration.
func CreateDeclaredIndexes(tx *gorm.DB, logger schemas.Logger, model interface{}) error {
	stmt := &gorm.Statement{DB: tx}
	if err := stmt.Parse(model); err != nil {
		return fmt.Errorf("failed to parse schema for %T: %w", model, err)
	}
	return createIndexes(tx, logger, model, stmt, nil)
}

// createFieldIndexes creates the not-yet-existing indexes that f declares by
// struct tag. Postgres is excluded by the caller: it raises the same indexes
// CONCURRENTLY from its own builder once the process is up.
func createFieldIndexes(tx *gorm.DB, logger schemas.Logger, model interface{}, stmt *gorm.Statement, f *schema.Field) error {
	return createIndexes(tx, logger, model, stmt, f)
}

// createIndexes creates the missing tag-declared indexes on model, restricted
// to those covering `only` when it is non-nil. A composite index is skipped
// until every column it names exists, so the migration adding the first of them
// cannot abort; the one adding the last raises it.
func createIndexes(
	tx *gorm.DB,
	logger schemas.Logger,
	model interface{},
	stmt *gorm.Statement,
	only *schema.Field,
) error {
	mig := tx.Migrator()
	for _, idx := range stmt.Schema.ParseIndexes() {
		if only != nil && !indexCoversField(idx, only) {
			continue
		}
		if mig.HasIndex(model, idx.Name) || !indexColumnsExist(mig, model, idx) {
			continue
		}
		if logger != nil {
			logger.Info("[migrator] creating index %s on table %s", idx.Name, stmt.Table)
		}
		if err := mig.CreateIndex(model, idx.Name); err != nil {
			return fmt.Errorf("failed to create index %s on table %s: %w", idx.Name, stmt.Table, err)
		}
	}
	return nil
}

// indexCoversField reports whether idx indexes the column backing f.
func indexCoversField(idx *schema.Index, f *schema.Field) bool {
	for _, opt := range idx.Fields {
		if opt.Field != nil && opt.DBName == f.DBName {
			return true
		}
	}
	return false
}

// indexColumnsExist reports whether every column idx names is already on the table.
func indexColumnsExist(mig gorm.Migrator, model interface{}, idx *schema.Index) bool {
	for _, opt := range idx.Fields {
		if opt.Field == nil {
			continue
		}
		if !mig.HasColumn(model, opt.DBName) {
			return false
		}
	}
	return true
}
