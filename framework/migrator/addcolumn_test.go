package migrator

import (
	"fmt"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// captureLogger records Info messages so tests can assert that
// AddColumnIfNotExists logs only when it actually adds a column.
type captureLogger struct{ infos []string }

func (c *captureLogger) Debug(string, ...any) {}
func (c *captureLogger) Info(msg string, args ...any) {
	c.infos = append(c.infos, fmt.Sprintf(msg, args...))
}
func (c *captureLogger) Warn(string, ...any)                                             {}
func (c *captureLogger) Error(string, ...any)                                            {}
func (c *captureLogger) Fatal(string, ...any)                                            {}
func (c *captureLogger) SetLevel(schemas.LogLevel)                                       {}
func (c *captureLogger) SetOutputType(schemas.LoggerOutputType)                          {}
func (c *captureLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder { return nil }

// addColTestBase is migrated first so the table exists WITHOUT the `extra`
// column, giving AddColumnIfNotExists something to add.
type addColTestBase struct {
	ID uint `gorm:"primaryKey"`
}

func (addColTestBase) TableName() string { return "addcol_test" }

// addColTestExtended maps to the same table but declares the Extra field the
// tests add via AddColumnIfNotExists.
type addColTestExtended struct {
	ID    uint   `gorm:"primaryKey"`
	Extra string `gorm:"column:extra"`
}

func (addColTestExtended) TableName() string { return "addcol_test" }

func openAddColumnTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&addColTestBase{}))
	return db
}

// TestAddColumnIfNotExistsAddsMissingColumn covers the non-Postgres (SQLite)
// fallback path: the column is absent, so it is added and logged once.
func TestAddColumnIfNotExistsAddsMissingColumn(t *testing.T) {
	db := openAddColumnTestDB(t)
	require.False(t, db.Migrator().HasColumn(&addColTestExtended{}, "extra"))

	log := &captureLogger{}
	require.NoError(t, AddColumnIfNotExists(db, log, &addColTestExtended{}, "Extra"))

	require.True(t, db.Migrator().HasColumn(&addColTestExtended{}, "extra"))
	require.Len(t, log.infos, 1, "should log exactly once when the column is added")
}

// TestAddColumnIfNotExistsIsIdempotent verifies a second call is a silent no-op,
// which is the property that makes dropping the per-call-site HasColumn guard safe.
func TestAddColumnIfNotExistsIsIdempotent(t *testing.T) {
	db := openAddColumnTestDB(t)
	require.NoError(t, AddColumnIfNotExists(db, nil, &addColTestExtended{}, "Extra"))

	log := &captureLogger{}
	require.NoError(t, AddColumnIfNotExists(db, log, &addColTestExtended{}, "Extra"))

	require.True(t, db.Migrator().HasColumn(&addColTestExtended{}, "extra"))
	require.Empty(t, log.infos, "should not log when the column already exists")
}

// TestAddColumnIfNotExistsUnknownFieldReturnsError verifies the schema-lookup
// guard surfaces a clear error rather than emitting bad DDL.
func TestAddColumnIfNotExistsUnknownFieldReturnsError(t *testing.T) {
	db := openAddColumnTestDB(t)

	err := AddColumnIfNotExists(db, nil, &addColTestExtended{}, "DoesNotExist")
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to look up field")
}

// addColTestIndexed declares a single-column index on the column the tests add
// and a composite index whose second column the table never gains.
type addColTestIndexed struct {
	ID      uint    `gorm:"primaryKey"`
	Indexed *string `gorm:"column:indexed;index;index:idx_addcol_test_pair,priority:1"`
	Absent  *string `gorm:"column:absent;index:idx_addcol_test_pair,priority:2"`
}

func (addColTestIndexed) TableName() string { return "addcol_test" }

// TestAddColumnIfNotExistsCreatesTagDeclaredIndex covers issue #7457: GORM
// raises a tag-declared index when it creates the table, never when a column is
// added later, and only Postgres has a builder that raises them afterwards.
func TestAddColumnIfNotExistsCreatesTagDeclaredIndex(t *testing.T) {
	db := openAddColumnTestDB(t)
	require.False(t, db.Migrator().HasIndex(&addColTestIndexed{}, "idx_addcol_test_indexed"))

	require.NoError(t, AddColumnIfNotExists(db, nil, &addColTestIndexed{}, "Indexed"))

	require.True(t, db.Migrator().HasColumn(&addColTestIndexed{}, "indexed"))
	require.True(t, db.Migrator().HasIndex(&addColTestIndexed{}, "idx_addcol_test_indexed"))
}

// TestAddColumnIfNotExistsSkipsIndexWithAbsentColumn pins the composite guard:
// creating an index over a column the table does not have yet would abort the
// migration that added the first one.
func TestAddColumnIfNotExistsSkipsIndexWithAbsentColumn(t *testing.T) {
	db := openAddColumnTestDB(t)

	require.NoError(t, AddColumnIfNotExists(db, nil, &addColTestIndexed{}, "Indexed"))

	require.False(t, db.Migrator().HasColumn(&addColTestIndexed{}, "absent"))
	require.False(t, db.Migrator().HasIndex(&addColTestIndexed{}, "idx_addcol_test_pair"))
}

// TestAddColumnIfNotExistsCompletesCompositeIndexWithLastColumn verifies the
// composite index lands once the migration adding its final column runs.
func TestAddColumnIfNotExistsCompletesCompositeIndexWithLastColumn(t *testing.T) {
	db := openAddColumnTestDB(t)
	require.NoError(t, AddColumnIfNotExists(db, nil, &addColTestIndexed{}, "Indexed"))

	require.NoError(t, AddColumnIfNotExists(db, nil, &addColTestIndexed{}, "Absent"))

	require.True(t, db.Migrator().HasIndex(&addColTestIndexed{}, "idx_addcol_test_pair"))
}
