package migrator

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// dropColTestModel maps to a table that starts WITH the `extra` column, giving
// DropColumnIfExists something to drop.
type dropColTestModel struct {
	ID    uint   `gorm:"primaryKey"`
	Extra string `gorm:"column:extra"`
}

func (dropColTestModel) TableName() string { return "dropcol_test" }

// dropColTestBare maps to the same table but does NOT declare the `extra`
// field, mimicking a migration that drops a legacy column already removed from
// the current struct.
type dropColTestBare struct {
	ID uint `gorm:"primaryKey"`
}

func (dropColTestBare) TableName() string { return "dropcol_test" }

func openDropColumnTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&dropColTestModel{}))
	return db
}

// TestDropColumnIfExistsDropsExistingColumn covers the non-Postgres (SQLite)
// fallback path: the column is present, so it is dropped and logged once.
func TestDropColumnIfExistsDropsExistingColumn(t *testing.T) {
	db := openDropColumnTestDB(t)
	require.True(t, db.Migrator().HasColumn(&dropColTestModel{}, "extra"))

	log := &captureLogger{}
	require.NoError(t, DropColumnIfExists(db, log, &dropColTestModel{}, "Extra"))

	require.False(t, db.Migrator().HasColumn(&dropColTestModel{}, "extra"))
	require.Len(t, log.infos, 1, "should log exactly once when the column is dropped")
}

// TestDropColumnIfExistsIsIdempotent verifies a second call is a silent no-op,
// which is the property that makes dropping the per-call-site HasColumn guard safe.
func TestDropColumnIfExistsIsIdempotent(t *testing.T) {
	db := openDropColumnTestDB(t)
	require.NoError(t, DropColumnIfExists(db, nil, &dropColTestModel{}, "Extra"))

	log := &captureLogger{}
	require.NoError(t, DropColumnIfExists(db, log, &dropColTestModel{}, "Extra"))

	require.False(t, db.Migrator().HasColumn(&dropColTestModel{}, "extra"))
	require.Empty(t, log.infos, "should not log when the column is already gone")
}

// TestDropColumnIfExistsUnknownFieldIsNoOp verifies that passing a name that
// does not correspond to any DB column is a silent no-op: HasColumn returns
// false, so DropColumnIfExists returns early without touching the table (and
// without erroring).
func TestDropColumnIfExistsUnknownFieldIsNoOp(t *testing.T) {
	db := openDropColumnTestDB(t)

	// "extra" exists as a column, but "DoesNotExist" is neither a field nor a
	// column; HasColumn is false for it, so the call is a no-op rather than an
	// error. Assert the safe no-op contract for an unknown field.
	require.NoError(t, DropColumnIfExists(db, nil, &dropColTestModel{}, "DoesNotExist"))
	require.True(t, db.Migrator().HasColumn(&dropColTestModel{}, "extra"))
}

// TestDropColumnIfExistsDropsLegacyColumnNotOnStruct covers the common migration
// case: the column still exists in the table but its Go field has already been
// removed from the current struct. The helper must fall back to the raw column
// name and drop it rather than erroring on the missing field.
func TestDropColumnIfExistsDropsLegacyColumnNotOnStruct(t *testing.T) {
	db := openDropColumnTestDB(t)
	require.True(t, db.Migrator().HasColumn(&dropColTestBare{}, "extra"))

	require.NoError(t, DropColumnIfExists(db, nil, &dropColTestBare{}, "extra"))
	require.False(t, db.Migrator().HasColumn(&dropColTestBare{}, "extra"))
}
