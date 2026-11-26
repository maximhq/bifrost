package queryscope

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// inStringsTestPGDSN points at the local test Postgres from framework/docker-compose.yml.
const inStringsTestPGDSN = "host=localhost user=bifrost password=bifrost_password dbname=bifrost port=5432 sslmode=disable"

// inStringsTestDBs returns a SQLite database and, when reachable, the local test
// Postgres, each holding a temp table of ids.
func inStringsTestDBs(t *testing.T) map[string]*gorm.DB {
	t.Helper()
	dbs := map[string]*gorm.DB{}
	sqliteDB, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "in.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	dbs["sqlite"] = sqliteDB
	if pgDB, err := gorm.Open(postgres.Open(inStringsTestPGDSN), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)}); err == nil {
		if sqlDB, err := pgDB.DB(); err == nil && sqlDB.Ping() == nil {
			t.Cleanup(func() { _ = sqlDB.Close() })
			dbs["postgres"] = pgDB
		}
	}
	return dbs
}

// TestInStrings_MoreIDsThanParameterLimit pins that a list longer than the
// Postgres (65,535) and SQLite (32,766) parameter limits binds as one argument
// and still selects exactly the listed rows, including ids that need quoting.
func TestInStrings_MoreIDsThanParameterLimit(t *testing.T) {
	for name, db := range inStringsTestDBs(t) {
		t.Run(name, func(t *testing.T) {
			table := "queryscope_instrings_test"
			require.NoError(t, db.Exec("DROP TABLE IF EXISTS "+table).Error)
			require.NoError(t, db.Exec("CREATE TABLE "+table+" (id VARCHAR(255) PRIMARY KEY)").Error)
			t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS " + table) })

			tricky := []string{`quote"id`, `back\slash`, "comma,id", "{brace}", "space id", ""}
			rows := append([]string{"outside-1", "outside-2"}, tricky...)
			for _, id := range rows {
				require.NoError(t, db.Exec("INSERT INTO "+table+" (id) VALUES (?)", id).Error)
			}

			ids := make([]string, 0, 70_000+len(tricky))
			for i := range 70_000 {
				ids = append(ids, fmt.Sprintf("missing-%05d", i))
			}
			ids = append(ids, tricky...)

			where, arg := InStrings(db, "id", ids)
			var got []string
			require.NoError(t, db.Table(table).Where(where, arg).Order("id").Pluck("id", &got).Error)
			require.ElementsMatch(t, tricky, got)

			notWhere, notArg := NotInStrings(db, "id", ids)
			var rest []string
			require.NoError(t, db.Table(table).Where(notWhere, notArg).Pluck("id", &rest).Error)
			require.ElementsMatch(t, []string{"outside-1", "outside-2"}, rest)
		})
	}
}

// TestInStrings_EmptyListMatchesNothing pins that an empty list selects no rows
// on every dialect, matching the previous "col IN ()" callers guarded against.
func TestInStrings_EmptyListMatchesNothing(t *testing.T) {
	for name, db := range inStringsTestDBs(t) {
		t.Run(name, func(t *testing.T) {
			where, arg := InStrings(db, "v", []string{})
			var n int64
			require.NoError(t, db.Raw("SELECT count(*) FROM (SELECT 'x' AS v) t WHERE "+where, arg).Scan(&n).Error)
			require.Zero(t, n)
		})
	}
}

// TestInUints_DialectForms pins the fragment and argument InUints builds on each
// dialect: one bigint[] literal on Postgres, one JSON array on SQLite, and the
// slice itself elsewhere.
func TestInUints_DialectForms(t *testing.T) {
	ids := []uint{3, 1, 18446744073709551615}
	cases := []struct {
		name      string
		db        *gorm.DB
		wantWhere string
		wantArg   any
	}{
		{"postgres", &gorm.DB{Config: &gorm.Config{Dialector: postgres.New(postgres.Config{})}}, "id = ANY(?::bigint[])", "{3,1,18446744073709551615}"},
		{"sqlite", &gorm.DB{Config: &gorm.Config{Dialector: sqlite.Open("")}}, "id IN (SELECT value FROM json_each(?))", "[3,1,18446744073709551615]"},
		{"none", &gorm.DB{Config: &gorm.Config{}}, "id IN ?", ids},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			where, arg := InUints(c.db, "id", ids)
			require.Equal(t, c.wantWhere, where)
			require.Equal(t, c.wantArg, arg)
		})
	}
}

// TestInUints_MoreIDsThanParameterLimit pins that an integer list longer than the
// Postgres (65,535) and SQLite (32,766) parameter limits binds as one argument and
// still selects exactly the listed rows.
func TestInUints_MoreIDsThanParameterLimit(t *testing.T) {
	for name, db := range inStringsTestDBs(t) {
		t.Run(name, func(t *testing.T) {
			table := "queryscope_inuints_test"
			require.NoError(t, db.Exec("DROP TABLE IF EXISTS "+table).Error)
			require.NoError(t, db.Exec("CREATE TABLE "+table+" (id BIGINT PRIMARY KEY)").Error)
			t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS " + table) })
			for _, id := range []uint{1, 2, 3, 100} {
				require.NoError(t, db.Exec("INSERT INTO "+table+" (id) VALUES (?)", id).Error)
			}

			ids := make([]uint, 0, 70_002)
			for i := range uint(70_000) {
				ids = append(ids, 1_000_000+i)
			}
			ids = append(ids, 2, 100)

			where, arg := InUints(db, "id", ids)
			var got []uint
			require.NoError(t, db.Table(table).Where(where, arg).Order("id").Pluck("id", &got).Error)
			require.Equal(t, []uint{2, 100}, got)
		})
	}
}

// TestInUints_EmptyListMatchesNothing pins that an empty integer list selects no rows
// on every dialect.
func TestInUints_EmptyListMatchesNothing(t *testing.T) {
	for name, db := range inStringsTestDBs(t) {
		t.Run(name, func(t *testing.T) {
			where, arg := InUints(db, "v", []uint{})
			var n int64
			require.NoError(t, db.Raw("SELECT count(*) FROM (SELECT 1 AS v) t WHERE "+where, arg).Scan(&n).Error)
			require.Zero(t, n)
		})
	}
}
