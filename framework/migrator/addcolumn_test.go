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
