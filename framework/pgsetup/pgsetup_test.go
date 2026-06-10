package pgsetup

import (
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const postgresDSN = "host=localhost user=bifrost password=bifrost_password dbname=bifrost port=5432 sslmode=disable"

// TestResolveSchema covers ResolveSchema's handling of nil/empty inputs, valid
// identifiers, regex rejection, and the 63-character length boundary.
func TestResolveSchema(t *testing.T) {
	tests := []struct {
		name    string
		input   *schemas.EnvVar
		want    string
		wantErr bool
	}{
		{name: "nil", input: nil, want: ""},
		{name: "empty value", input: &schemas.EnvVar{Val: ""}, want: ""},
		{name: "valid simple", input: &schemas.EnvVar{Val: "bifrost"}, want: "bifrost"},
		{name: "valid with underscore", input: &schemas.EnvVar{Val: "bif_rost_2"}, want: "bif_rost_2"},
		{name: "valid leading underscore", input: &schemas.EnvVar{Val: "_internal"}, want: "_internal"},
		{name: "starts with digit", input: &schemas.EnvVar{Val: "1bifrost"}, wantErr: true},
		{name: "contains hyphen", input: &schemas.EnvVar{Val: "foo-bar"}, wantErr: true},
		{name: "contains space", input: &schemas.EnvVar{Val: "foo bar"}, wantErr: true},
		{name: "sql injection attempt", input: &schemas.EnvVar{Val: `foo"; DROP TABLE x;--`}, wantErr: true},
		{name: "too long", input: &schemas.EnvVar{Val: strings.Repeat("a", 64)}, wantErr: true},
		{name: "max length boundary", input: &schemas.EnvVar{Val: strings.Repeat("a", 63)}, want: strings.Repeat("a", 63)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveSchema(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestBuildDSN exercises BuildDSN's search_path handling (none, public, custom,
// mixed-case) and password escaping for whitespace, quotes, and backslashes.
func TestBuildDSN(t *testing.T) {
	base := DSN{
		Host:     "localhost",
		Port:     "5432",
		User:     "bifrost",
		Password: "secret",
		DBName:   "bifrost",
		SSLMode:  "disable",
	}

	t.Run("no schema", func(t *testing.T) {
		got := BuildDSN(base)
		assert.NotContains(t, got, "search_path")
		assert.Contains(t, got, "host=localhost")
		assert.Contains(t, got, "dbname=bifrost")
	})

	t.Run("public schema is implicit", func(t *testing.T) {
		d := base
		d.Schema = "public"
		got := BuildDSN(d)
		assert.NotContains(t, got, "search_path")
	})

	t.Run("custom schema leads search_path", func(t *testing.T) {
		d := base
		d.Schema = "tenant_a"
		got := BuildDSN(d)
		assert.Contains(t, got, `search_path="tenant_a",public`)
	})

	t.Run("mixed-case schema preserves case via double quotes", func(t *testing.T) {
		d := base
		d.Schema = "TenantA"
		got := BuildDSN(d)
		assert.Contains(t, got, `search_path="TenantA",public`)
	})

	t.Run("password with space is quoted", func(t *testing.T) {
		d := base
		d.Password = "p ass"
		got := BuildDSN(d)
		assert.Contains(t, got, `password='p ass'`)
	})

	t.Run("password with quote and backslash is escaped", func(t *testing.T) {
		d := base
		d.Password = `a'b\c`
		got := BuildDSN(d)
		assert.Contains(t, got, `password='a\'b\\c'`)
	})

	t.Run("empty password becomes empty quoted", func(t *testing.T) {
		d := base
		d.Password = ""
		got := BuildDSN(d)
		assert.Contains(t, got, `password=''`)
	})
}

// TestEscapeDSNValue covers escapeDSNValue's libpq quoting and escape rules
// for empty, plain, whitespace, quote, and backslash inputs.
func TestEscapeDSNValue(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", "''"},
		{"plain", "plain"},
		{"with space", "'with space'"},
		{"with\ttab", "'with\ttab'"},
		{"a'b", `'a\'b'`},
		{`a\b`, `'a\\b'`},
		{`mix 'and\stuff`, `'mix \'and\\stuff'`},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, escapeDSNValue(tt.in), "input=%q", tt.in)
	}
}

// trySetupPostgresDB connects to Postgres or returns nil when unavailable.
func trySetupPostgresDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(postgres.Open(postgresDSN), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil
	}
	if err := sqlDB.Ping(); err != nil {
		_ = sqlDB.Close()
		return nil
	}
	return db
}

// TestEnsureSchema verifies the empty-name no-op path and that EnsureSchema
// creates a missing schema and is idempotent on a second call.
func TestEnsureSchema(t *testing.T) {
	db := trySetupPostgresDB(t)
	if db == nil {
		t.Skip("Postgres not available, skipping test")
	}
	dbSQL, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, dbSQL.Close()) })

	t.Run("empty name is no-op", func(t *testing.T) {
		require.NoError(t, EnsureSchema(db, ""))
	})

	t.Run("creates missing schema and is idempotent", func(t *testing.T) {
		const name = "pgsetup_test_schema"
		t.Cleanup(func() {
			assert.NoError(t, db.Exec(`DROP SCHEMA IF EXISTS `+name+` CASCADE`).Error)
		})
		require.NoError(t, db.Exec(`DROP SCHEMA IF EXISTS `+name+` CASCADE`).Error)

		require.NoError(t, EnsureSchema(db, name))
		assert.True(t, schemaExists(t, db, name))

		// Second call must not fail and must not re-issue CREATE for an existing schema.
		require.NoError(t, EnsureSchema(db, name))
	})
}

// TestBuildDSN_IntegrationCustomSchema connects to a real Postgres with a
// mixed-case schema set in the DSN and verifies that an unqualified CREATE
// TABLE lands in that schema (not `public`).
func TestBuildDSN_IntegrationCustomSchema(t *testing.T) {
	probe := trySetupPostgresDB(t)
	if probe == nil {
		t.Skip("Postgres not available, skipping test")
	}
	probeSQL, err := probe.DB()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, probeSQL.Close()) })

	const schemaName = "PgsetupIT_TenantA"
	const dropSQL = `DROP SCHEMA IF EXISTS "PgsetupIT_TenantA" CASCADE`
	require.NoError(t, probe.Exec(dropSQL).Error)
	t.Cleanup(func() {
		assert.NoError(t, probe.Exec(dropSQL).Error)
	})

	dsn := BuildDSN(DSN{
		Host:     "localhost",
		Port:     "5432",
		User:     "bifrost",
		Password: "bifrost_password",
		DBName:   "bifrost",
		SSLMode:  "disable",
		Schema:   schemaName,
	})
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	dbSQL, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, dbSQL.Close()) })

	require.NoError(t, EnsureSchema(db, schemaName))
	require.NoError(t, db.Exec(`CREATE TABLE search_path_probe (id int)`).Error)

	var landed string
	require.NoError(t, db.Raw(
		`SELECT n.nspname FROM pg_class c
		 JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE c.relname = 'search_path_probe' AND n.nspname = ?`,
		schemaName,
	).Scan(&landed).Error)
	assert.Equal(t, schemaName, landed,
		"unqualified CREATE TABLE landed in %q, expected %q (search_path quoting may be wrong)", landed, schemaName)
}

// TestBuildDSN_IntegrationSpecialCharPassword connects to Postgres as a role
// whose password contains whitespace, single quotes, and backslashes.
func TestBuildDSN_IntegrationSpecialCharPassword(t *testing.T) {
	probe := trySetupPostgresDB(t)
	if probe == nil {
		t.Skip("Postgres not available, skipping test")
	}
	probeSQL, err := probe.DB()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, probeSQL.Close()) })

	const role = "pgsetup_it_specialchars"
	const password = `pa ss'word\x`
	// PG SQL literal: double the single quote, leave backslash literal.
	const createSQL = `CREATE ROLE pgsetup_it_specialchars LOGIN PASSWORD 'pa ss''word\x'`
	const dropSQL = `DROP ROLE IF EXISTS pgsetup_it_specialchars`
	require.NoError(t, probe.Exec(dropSQL).Error)
	require.NoError(t, probe.Exec(createSQL).Error)
	t.Cleanup(func() {
		assert.NoError(t, probe.Exec(dropSQL).Error)
	})

	dsn := BuildDSN(DSN{
		Host:     "localhost",
		Port:     "5432",
		User:     role,
		Password: password,
		DBName:   "bifrost",
		SSLMode:  "disable",
	})
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	dbSQL, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, dbSQL.Close()) })

	require.NoError(t, dbSQL.Ping(), "libpq rejected the DSN — escapeDSNValue output is wrong")

	var who string
	require.NoError(t, db.Raw(`SELECT current_user`).Scan(&who).Error)
	assert.Equal(t, role, who, "authenticated as wrong user — password escaping likely truncated the value")
}

// schemaExists returns true when a schema with the given name is present in
// information_schema.schemata.
func schemaExists(t *testing.T, db *gorm.DB, name string) bool {
	t.Helper()
	var exists bool
	require.NoError(t, db.Raw(
		`SELECT EXISTS (SELECT 1 FROM information_schema.schemata WHERE schema_name = ?)`,
		name,
	).Scan(&exists).Error)
	return exists
}
