package configstore

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// migrationPerfTestEnvVar gates every test in this file. Seeding ~1,000,000
// rows takes real wall-clock time, so these must never run as part of a
// normal `go test`/CI invocation — only on demand, with this variable set.
const migrationPerfTestEnvVar = "BIFROST_RUN_MIGRATION_PERF_TESTS"

// Row counts for the merge_oauth_token_tables perf test. Shared (admin/
// client-credential) OAuth connections are far rarer than per-user ones in a
// real deployment — one shared token per shared-mode MCP client vs. one
// per-user token per (identity, MCP client) pair — so the split is 1%/99%
// rather than even, while still totaling ~1,000,000 rows.
const (
	perfSharedTokenCount     = 10_000
	perfPerUserTokenCount    = 990_000
	perfSyntheticClientCount = 2_000 // distinct MCP client rows the per-user tokens cycle through
	perfSeedInsertBatchSize  = 500   // rows per INSERT statement; keeps param counts well under SQLite's variable limit
)

// pgPerfTestSchema is a schema dedicated to this file's Postgres perf runs,
// separate from pgTestSchema (used by the rest of the package's functional
// tests) so a full DROP SCHEMA .. CASCADE here can never clobber another
// test's fixtures even if both happened to run in the same invocation.
const pgPerfTestSchema = "configstore_perf_test"

// setupPreMergeSQLiteDB runs every configstore migration EXCEPT the final
// merge_oauth_token_tables step, leaving oauth_tokens/oauth_user_tokens in
// their real pre-merge shape (created by the historical bootstrap
// migrations, complete with the partial unique indexes
// migrationAddOAuthAuthModeColumns adds) and mcp_oauth_tokens not yet
// created — the exact starting state migrationMergeOauthTokenTables expects
// to run against on an existing deployment.
func setupPreMergeSQLiteDB(t *testing.T) *gorm.DB {
	t.Helper()
	n := time.Now().UnixNano() + testDBCounter
	testDBCounter++
	dsn := fmt.Sprintf("file:perftestdb_%d?mode=memory&cache=shared", n)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err, "failed to open perf test sqlite db")

	runPreMergeMigrationChain(t, db)
	return db
}

// trySetupPreMergePostgresDB attempts to run the same pre-merge migration
// chain against a real Postgres connection. Returns nil (without failing the
// test) if Postgres is unavailable, matching this package's existing
// trySetupPostgresDBWithoutStoreRawColumn fallback pattern.
func trySetupPreMergePostgresDB(t *testing.T) *gorm.DB {
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
	// Closed on every failure path below via this defer; once schema setup
	// succeeds and t.Cleanup takes over ownership, ok flips to true and this
	// becomes a no-op (avoids a double-close).
	ok := false
	defer func() {
		if !ok {
			sqlDB.Close()
		}
	}()
	if err := sqlDB.Ping(); err != nil {
		return nil
	}
	// postgresDSN already pins search_path=configstore_test for every pooled
	// connection. `SET search_path` below only changes the single session
	// that happens to run it, so without limiting the pool to that one
	// connection, later queries on a different pooled connection would
	// silently run against configstore_test instead of pgPerfTestSchema.
	sqlDB.SetMaxOpenConns(1)

	// Dedicated schema, dropped and recreated fresh so the full pre-merge
	// chain always runs from a clean slate, same as a brand-new deployment.
	if err := db.Exec("DROP SCHEMA IF EXISTS " + pgPerfTestSchema + " CASCADE").Error; err != nil {
		return nil
	}
	if err := db.Exec("CREATE SCHEMA " + pgPerfTestSchema).Error; err != nil {
		return nil
	}
	if err := db.Exec("SET search_path TO " + pgPerfTestSchema).Error; err != nil {
		return nil
	}

	// Registered before runPreMergeMigrationChain (which asserts via
	// `require` and can stop this goroutine immediately) so a chain failure
	// still drops the schema and closes the connection instead of leaking
	// both.
	t.Cleanup(func() {
		db.Exec("DROP SCHEMA IF EXISTS " + pgPerfTestSchema + " CASCADE")
		sqlDB.Close()
	})
	ok = true

	runPreMergeMigrationChain(t, db)

	return db
}

// migrationStepIndex returns the position of the migration step registered
// under id in configstoreMigrationSteps, failing the test if it isn't found.
// Locating steps by ID rather than by a fixed offset from the end of the
// slice keeps the pre-chain helpers below correct as later migrations are
// appended to the registry.
func migrationStepIndex(t *testing.T, id string) int {
	t.Helper()
	for i, step := range configstoreMigrationSteps {
		if len(step.IDs) == 1 && step.IDs[0] == id {
			return i
		}
	}
	require.Failf(t, "migration step not found", "expected %q to be a registered migration step", id)
	return -1
}

// runPreMergeMigrationChain runs every configstoreMigrationSteps entry up to
// (but not including) merge_oauth_token_tables and asserts the resulting
// schema is genuinely pre-merge: oauth_tokens/oauth_user_tokens exist,
// mcp_oauth_tokens does not.
func runPreMergeMigrationChain(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NotEmpty(t, configstoreMigrationSteps)
	preMergeSteps := configstoreMigrationSteps[:migrationStepIndex(t, "merge_oauth_token_tables")]

	ctx := context.Background()
	require.NoError(t, runMigrationSteps(ctx, db, testMigrationLogger, preMergeSteps),
		"failed to run the pre-merge migration chain")

	require.False(t, db.Migrator().HasTable(&tables.TableMCPOauthToken{}),
		"mcp_oauth_tokens should not exist before migrationMergeOauthTokenTables runs")
	require.True(t, db.Migrator().HasTable(&tables.TableOauthToken{}),
		"oauth_tokens should exist in its pre-merge shape")
	require.True(t, db.Migrator().HasTable(&tables.TableOauthUserToken{}),
		"oauth_user_tokens should exist in its pre-merge shape")
}

// seedSyntheticMCPClients bulk-inserts count real config_mcp_clients rows so
// the per-user token rows below can reference genuinely existing MCP
// clients (belt-and-suspenders against TableOauthUserToken.MCPClient's
// foreignKey tag producing a real FK constraint on some backends — SQLite
// doesn't enforce FKs without an explicit pragma this test doesn't set, but
// Postgres would).
func seedSyntheticMCPClients(t *testing.T, db *gorm.DB, count int) {
	t.Helper()
	now := time.Now()
	clients := make([]tables.TableMCPClient, 0, perfSeedInsertBatchSize)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		for i := 0; i < count; i++ {
			clients = append(clients, tables.TableMCPClient{
				ClientID:       fmt.Sprintf("perf-mcp-client-%d", i),
				Name:           fmt.Sprintf("perf-mcp-client-%d", i),
				ConnectionType: "streamable_http",
				AuthType:       "per_user_oauth",
				CreatedAt:      now,
				UpdatedAt:      now,
			})
			if len(clients) == perfSeedInsertBatchSize {
				if err := tx.Create(&clients).Error; err != nil {
					return err
				}
				clients = clients[:0]
			}
		}
		if len(clients) > 0 {
			return tx.Create(&clients).Error
		}
		return nil
	}))
}

// bulkInsertOauthTokens seeds count rows into oauth_tokens (the pre-merge
// shared-credential table), each linked to its own oauth_configs row and
// config_mcp_clients row (via oauth_config_id) — the exact join path
// migrationMergeOauthTokenTables walks to derive
// mcp_oauth_tokens.oauth_config_id/mcp_client_id for shared tokens. Without
// these linked rows every seeded token would only exercise that join's
// COALESCE(...,'') miss branch, never a real hit.
//
// The join reads oauth_configs.token_id — a column TableOauthConfig no
// longer maps a Go field to (see its doc comment: the FK shortcut is
// retired) and, per migrationMergeOauthTokenTables's own comment, doesn't
// even exist on a fresh install, since runPreMergeMigrationChain's
// CreateTable(&tables.TableOauthConfig{}) call builds the table from
// today's TokenID-free struct. This test simulates the other real
// deployment shape the migration's HasColumn guard exists for — an
// existing installation that predates the column's retirement — by adding
// the column back with a raw ALTER TABLE before seeding it, rather than
// leaving the join's hit branch permanently untestable.
//
// Batched multi-row INSERTs wrapped in a single transaction — not one
// .Create() call per row — so seeding time stays a small fraction of the
// migration time being measured.
func bulkInsertOauthTokens(t *testing.T, db *gorm.DB, count int) {
	t.Helper()
	require.NoError(t, db.Exec(`ALTER TABLE oauth_configs ADD COLUMN token_id VARCHAR(255)`).Error,
		"simulate a pre-retirement install so the shared-token join has a token_id column to hit")
	now := time.Now()
	expiresAt := now.Add(24 * time.Hour)
	tokenBatch := make([]tables.TableOauthToken, 0, perfSeedInsertBatchSize)
	configBatch := make([]tables.TableOauthConfig, 0, perfSeedInsertBatchSize)
	clientBatch := make([]tables.TableMCPClient, 0, perfSeedInsertBatchSize)
	flush := func(tx *gorm.DB) error {
		if len(tokenBatch) > 0 {
			if err := tx.Create(&tokenBatch).Error; err != nil {
				return err
			}
			tokenBatch = tokenBatch[:0]
		}
		if len(configBatch) > 0 {
			if err := tx.Create(&configBatch).Error; err != nil {
				return err
			}
			configBatch = configBatch[:0]
		}
		if len(clientBatch) > 0 {
			if err := tx.Create(&clientBatch).Error; err != nil {
				return err
			}
			clientBatch = clientBatch[:0]
		}
		return nil
	}
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		for i := 0; i < count; i++ {
			tokenID := fmt.Sprintf("perf-shared-tok-%d", i)
			configID := fmt.Sprintf("perf-shared-cfg-%d", i)
			tokenBatch = append(tokenBatch, tables.TableOauthToken{
				ID:               tokenID,
				AccessToken:      fmt.Sprintf("perf-shared-access-token-%d", i),
				RefreshToken:     fmt.Sprintf("perf-shared-refresh-token-%d", i),
				TokenType:        "Bearer",
				ExpiresAt:        &expiresAt,
				Scopes:           `["read","write"]`,
				LastRefreshedAt:  &now,
				EncryptionStatus: tables.EncryptionStatusPlainText,
				CreatedAt:        now,
				UpdatedAt:        now,
			})
			configBatch = append(configBatch, tables.TableOauthConfig{
				ID:          configID,
				RedirectURI: "https://perf.example.com/callback",
				Status:      "authorized",
				CreatedAt:   now,
				UpdatedAt:   now,
			})
			clientBatch = append(clientBatch, tables.TableMCPClient{
				ClientID:       fmt.Sprintf("perf-shared-mcp-client-%d", i),
				Name:           fmt.Sprintf("perf-shared-mcp-client-%d", i),
				ConnectionType: "streamable_http",
				AuthType:       "oauth",
				OauthConfigID:  &configID,
				CreatedAt:      now,
				UpdatedAt:      now,
			})
			if len(tokenBatch) == perfSeedInsertBatchSize {
				if err := flush(tx); err != nil {
					return err
				}
			}
		}
		return flush(tx)
	}))

	// Backfill the legacy oauth_configs.token_id column (see the doc comment
	// above): a plain string match on the deterministic ID pattern this
	// function uses, so it can run as one statement instead of per-row.
	require.NoError(t, db.Exec(
		`UPDATE oauth_configs SET token_id = REPLACE(id, 'perf-shared-cfg-', 'perf-shared-tok-') WHERE id LIKE 'perf-shared-cfg-%'`,
	).Error)
}

// bulkInsertOauthUserTokens seeds count rows into oauth_user_tokens (the
// pre-merge per-identity table), all in 'user' auth_mode, cycling through
// numClients distinct MCP clients — same batched-INSERT-in-one-transaction
// approach as bulkInsertOauthTokens. Every row gets a distinct UserID, so no
// row can collide with another under the (user_id, mcp_client_id) partial
// unique index regardless of which client it lands on.
func bulkInsertOauthUserTokens(t *testing.T, db *gorm.DB, count int, numClients int) {
	t.Helper()
	now := time.Now()
	expiresAt := now.Add(24 * time.Hour)
	batch := make([]tables.TableOauthUserToken, 0, perfSeedInsertBatchSize)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		for i := 0; i < count; i++ {
			userID := fmt.Sprintf("perf-user-%d", i)
			batch = append(batch, tables.TableOauthUserToken{
				ID:               fmt.Sprintf("perf-user-tok-%d", i),
				UserID:           &userID,
				MCPClientID:      fmt.Sprintf("perf-mcp-client-%d", i%numClients),
				AuthMode:         "user",
				Status:           "active",
				OauthConfigID:    fmt.Sprintf("perf-oauth-config-%d", i%numClients),
				AccessToken:      fmt.Sprintf("perf-user-access-token-%d", i),
				RefreshToken:     fmt.Sprintf("perf-user-refresh-token-%d", i),
				TokenType:        "Bearer",
				ExpiresAt:        &expiresAt,
				Scopes:           `["read","write"]`,
				LastRefreshedAt:  &now,
				EncryptionStatus: tables.EncryptionStatusPlainText,
				CreatedAt:        now,
				UpdatedAt:        now,
			})
			if len(batch) == perfSeedInsertBatchSize {
				if err := tx.Create(&batch).Error; err != nil {
					return err
				}
				batch = batch[:0]
			}
		}
		if len(batch) > 0 {
			return tx.Create(&batch).Error
		}
		return nil
	}))
}

// TestMigrationMergeOauthTokenTablesPerf seeds ~1,000,000 rows across the
// pre-merge oauth_tokens/oauth_user_tokens tables and measures how long the
// real migrationMergeOauthTokenTables takes to run against that data. Both
// PR1's mcp_oauth_tokens migration and PR3's mcp_oauth_flows migration are
// CREATE-new-table + INSERT...SELECT-from-old-table(s) migrations that run
// synchronously at boot behind a Postgres advisory lock, so their wall-clock
// cost at scale is an operational question worth measuring directly rather
// than guessing at.
//
// Gated behind BIFROST_RUN_MIGRATION_PERF_TESTS=1 — never runs as part of a
// normal `go test ./...` or CI invocation.
func TestMigrationMergeOauthTokenTablesPerf(t *testing.T) {
	if os.Getenv(migrationPerfTestEnvVar) != "1" {
		t.Skipf("set %s=1 to run this migration performance test (seeds ~1,000,000 rows; slow by design)", migrationPerfTestEnvVar)
	}

	dbs := []namedDB{{"sqlite", setupPreMergeSQLiteDB(t)}}
	if pgDB := trySetupPreMergePostgresDB(t); pgDB != nil {
		dbs = append(dbs, namedDB{"postgres", pgDB})
	} else {
		t.Log("postgres unavailable for this run; measuring against sqlite only")
	}

	for _, ndb := range dbs {
		ndb := ndb
		t.Run(ndb.name, func(t *testing.T) {
			db := ndb.db
			ctx := context.Background()

			// Close the SQLite pool once this subtest finishes so the shared
			// in-memory database (and its ~1M seeded rows) is freed before
			// the Postgres subtest runs, rather than staying open for the
			// rest of the test.
			if ndb.name == "sqlite" {
				t.Cleanup(func() {
					sqlDB, err := db.DB()
					if err == nil {
						_ = sqlDB.Close()
					}
				})
			}

			seedStart := time.Now()
			seedSyntheticMCPClients(t, db, perfSyntheticClientCount)
			bulkInsertOauthTokens(t, db, perfSharedTokenCount)
			bulkInsertOauthUserTokens(t, db, perfPerUserTokenCount, perfSyntheticClientCount)
			t.Logf("[%s] seeded %d oauth_tokens + %d oauth_user_tokens rows in %v",
				ndb.name, perfSharedTokenCount, perfPerUserTokenCount, time.Since(seedStart))

			var sharedCount, userCount int64
			require.NoError(t, db.Table("oauth_tokens").Count(&sharedCount).Error)
			require.NoError(t, db.Table("oauth_user_tokens").Count(&userCount).Error)
			require.EqualValues(t, perfSharedTokenCount, sharedCount)
			require.EqualValues(t, perfPerUserTokenCount, userCount)

			// This is the real function, invoked exactly like the migration
			// registry invokes it — not a reimplementation of its SQL.
			migrateStart := time.Now()
			err := migrationMergeOauthTokenTables(ctx, db, testMigrationLogger)
			elapsed := time.Since(migrateStart)
			require.NoError(t, err, "migrationMergeOauthTokenTables should succeed against the seeded dataset")

			t.Logf("[%s] migrationMergeOauthTokenTables took %v against %d seeded rows (%d shared + %d per-user)",
				ndb.name, elapsed, perfSharedTokenCount+perfPerUserTokenCount, perfSharedTokenCount, perfPerUserTokenCount)

			var mergedCount int64
			require.NoError(t, db.Table("mcp_oauth_tokens").Count(&mergedCount).Error)
			require.EqualValues(t, perfSharedTokenCount+perfPerUserTokenCount, mergedCount,
				"every seeded row should have been copied into mcp_oauth_tokens")

			// Every shared token was seeded with a linked oauth_configs +
			// config_mcp_clients row (see bulkInsertOauthTokens), so the
			// migration's join must have derived a real, nonempty
			// oauth_config_id/mcp_client_id for each one — not just fallen
			// through to the COALESCE(...,'') miss branch.
			var linkedCount int64
			require.NoError(t, db.Table("mcp_oauth_tokens").
				Where("auth_mode = ? AND oauth_config_id != '' AND mcp_client_id != ''", "shared").
				Count(&linkedCount).Error)
			require.EqualValues(t, perfSharedTokenCount, linkedCount,
				"every migrated shared token should retain the oauth_config_id/mcp_client_id derived from its linked oauth_configs/config_mcp_clients rows")
		})
	}
}

// perfFlowRowCount is the row count for the create_mcp_oauth_flows_table
// perf test below. Unlike the token-table merge above, there is only one
// source table here (oauth_user_sessions) and no shared/per-user split to
// mirror, so this is a single flat ~1,000,000-row seed.
const perfFlowRowCount = 1_000_000

// setupPreCreateFlowsSQLiteDB runs every configstore migration EXCEPT the
// final two steps (create_mcp_oauth_flows_table and its follow-on
// drop_oauth_config_pkce_columns), leaving oauth_user_sessions in its real
// pre-migration shape (complete with the uniqueIndex on state that
// TableOauthUserSession's struct tag has always carried) and mcp_oauth_flows
// not yet created — the exact starting state migrationCreateMCPOauthFlowsTable
// expects to run against on an existing deployment.
func setupPreCreateFlowsSQLiteDB(t *testing.T) *gorm.DB {
	t.Helper()
	n := time.Now().UnixNano() + testDBCounter
	testDBCounter++
	dsn := fmt.Sprintf("file:perftestdb_flows_%d?mode=memory&cache=shared", n)
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err, "failed to open perf test sqlite db")

	runPreCreateFlowsMigrationChain(t, db)
	return db
}

// trySetupPreCreateFlowsPostgresDB is trySetupPreMergePostgresDB's
// counterpart for the flows migration — same fallback-to-nil-on-unavailable
// behavior, same dedicated pgPerfTestSchema (safe to reuse across both perf
// tests in this file: each wipes and recreates the schema fresh before
// seeding, and the two perf tests never run concurrently against it).
func trySetupPreCreateFlowsPostgresDB(t *testing.T) *gorm.DB {
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
	// Closed on every failure path below via this defer; once schema setup
	// succeeds and t.Cleanup takes over ownership, ok flips to true and this
	// becomes a no-op (avoids a double-close). Mirrors
	// trySetupPreMergePostgresDB's identical pattern above.
	ok := false
	defer func() {
		if !ok {
			sqlDB.Close()
		}
	}()
	if err := sqlDB.Ping(); err != nil {
		return nil
	}
	// postgresDSN already pins search_path=configstore_test for every pooled
	// connection. `SET search_path` below only changes the single session
	// that happens to run it, so without limiting the pool to that one
	// connection, later queries on a different pooled connection would
	// silently run against configstore_test instead of pgPerfTestSchema.
	sqlDB.SetMaxOpenConns(1)

	if err := db.Exec("DROP SCHEMA IF EXISTS " + pgPerfTestSchema + " CASCADE").Error; err != nil {
		return nil
	}
	if err := db.Exec("CREATE SCHEMA " + pgPerfTestSchema).Error; err != nil {
		return nil
	}
	if err := db.Exec("SET search_path TO " + pgPerfTestSchema).Error; err != nil {
		return nil
	}

	t.Cleanup(func() {
		db.Exec("DROP SCHEMA IF EXISTS " + pgPerfTestSchema + " CASCADE")
		sqlDB.Close()
	})
	ok = true

	runPreCreateFlowsMigrationChain(t, db)

	return db
}

// runPreCreateFlowsMigrationChain runs every configstoreMigrationSteps entry
// up to (but not including) create_mcp_oauth_flows_table and asserts the
// resulting schema is genuinely pre-flows-migration: oauth_user_sessions
// exists, mcp_oauth_flows does not. Everything from create_mcp_oauth_flows_table
// onward (including drop_oauth_config_pkce_columns) is excluded — the latter
// runs after create_mcp_oauth_flows_table and touches oauth_configs, not
// oauth_user_sessions, so leaving it out changes nothing this test cares about.
func runPreCreateFlowsMigrationChain(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NotEmpty(t, configstoreMigrationSteps)
	preFlowSteps := configstoreMigrationSteps[:migrationStepIndex(t, "create_mcp_oauth_flows_table")]

	ctx := context.Background()
	require.NoError(t, runMigrationSteps(ctx, db, testMigrationLogger, preFlowSteps),
		"failed to run the pre-flows-migration chain")

	require.False(t, db.Migrator().HasTable(&tables.TableMCPOauthFlow{}),
		"mcp_oauth_flows should not exist before migrationCreateMCPOauthFlowsTable runs")
	require.True(t, db.Migrator().HasTable(&tables.TableOauthUserSession{}),
		"oauth_user_sessions should exist in its pre-migration shape")
}

// bulkInsertOauthUserSessions seeds count rows into oauth_user_sessions (the
// pre-migration flow table), all in 'user' flow_mode, cycling through
// numClients distinct MCP clients — same batched-INSERT-in-one-transaction
// approach as bulkInsertOauthTokens/bulkInsertOauthUserTokens above. Every
// row gets a distinct State (the column's real uniqueIndex would otherwise
// reject the batch) and a distinct UserID.
func bulkInsertOauthUserSessions(t *testing.T, db *gorm.DB, count int, numClients int) {
	t.Helper()
	now := time.Now()
	expiresAt := now.Add(15 * time.Minute)
	batch := make([]tables.TableOauthUserSession, 0, perfSeedInsertBatchSize)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		for i := 0; i < count; i++ {
			userID := fmt.Sprintf("perf-flow-user-%d", i)
			batch = append(batch, tables.TableOauthUserSession{
				ID:               fmt.Sprintf("perf-flow-%d", i),
				MCPClientID:      fmt.Sprintf("perf-mcp-client-%d", i%numClients),
				OauthConfigID:    fmt.Sprintf("perf-oauth-config-%d", i%numClients),
				State:            fmt.Sprintf("perf-flow-state-%d", i),
				RedirectURI:      "https://example.com/callback",
				CodeVerifier:     fmt.Sprintf("perf-flow-verifier-%d", i),
				UserID:           &userID,
				FlowMode:         "user",
				Status:           "pending",
				EncryptionStatus: tables.EncryptionStatusPlainText,
				ExpiresAt:        expiresAt,
				CreatedAt:        now,
				UpdatedAt:        now,
			})
			if len(batch) == perfSeedInsertBatchSize {
				if err := tx.Create(&batch).Error; err != nil {
					return err
				}
				batch = batch[:0]
			}
		}
		if len(batch) > 0 {
			return tx.Create(&batch).Error
		}
		return nil
	}))
}

// TestMigrationCreateMCPOauthFlowsTablePerf seeds ~1,000,000 rows into the
// pre-migration oauth_user_sessions table and measures how long the real
// migrationCreateMCPOauthFlowsTable takes to run against that data. Same
// rationale as TestMigrationMergeOauthTokenTablesPerf above (see its doc
// comment) — this is also a CREATE-new-table +
// INSERT...SELECT-from-old-table migration that runs synchronously at boot
// behind a Postgres advisory lock.
//
// Gated behind BIFROST_RUN_MIGRATION_PERF_TESTS=1 — never runs as part of a
// normal `go test ./...` or CI invocation.
func TestMigrationCreateMCPOauthFlowsTablePerf(t *testing.T) {
	if os.Getenv(migrationPerfTestEnvVar) != "1" {
		t.Skipf("set %s=1 to run this migration performance test (seeds ~1,000,000 rows; slow by design)", migrationPerfTestEnvVar)
	}

	dbs := []namedDB{{"sqlite", setupPreCreateFlowsSQLiteDB(t)}}
	if pgDB := trySetupPreCreateFlowsPostgresDB(t); pgDB != nil {
		dbs = append(dbs, namedDB{"postgres", pgDB})
	} else {
		t.Log("postgres unavailable for this run; measuring against sqlite only")
	}

	for _, ndb := range dbs {
		ndb := ndb
		t.Run(ndb.name, func(t *testing.T) {
			db := ndb.db
			ctx := context.Background()

			// Close the SQLite pool once this subtest finishes so the shared
			// in-memory database (and its ~1M seeded rows) is freed before
			// the Postgres subtest runs, rather than staying open for the
			// rest of the test.
			if ndb.name == "sqlite" {
				t.Cleanup(func() {
					sqlDB, err := db.DB()
					if err == nil {
						_ = sqlDB.Close()
					}
				})
			}

			seedStart := time.Now()
			seedSyntheticMCPClients(t, db, perfSyntheticClientCount)
			bulkInsertOauthUserSessions(t, db, perfFlowRowCount, perfSyntheticClientCount)
			t.Logf("[%s] seeded %d oauth_user_sessions rows in %v",
				ndb.name, perfFlowRowCount, time.Since(seedStart))

			var sessionCount int64
			require.NoError(t, db.Table("oauth_user_sessions").Count(&sessionCount).Error)
			require.EqualValues(t, perfFlowRowCount, sessionCount)

			// This is the real function, invoked exactly like the migration
			// registry invokes it — not a reimplementation of its SQL.
			migrateStart := time.Now()
			err := migrationCreateMCPOauthFlowsTable(ctx, db, testMigrationLogger)
			elapsed := time.Since(migrateStart)
			require.NoError(t, err, "migrationCreateMCPOauthFlowsTable should succeed against the seeded dataset")

			t.Logf("[%s] migrationCreateMCPOauthFlowsTable took %v against %d seeded rows",
				ndb.name, elapsed, perfFlowRowCount)

			var flowCount int64
			require.NoError(t, db.Table("mcp_oauth_flows").Count(&flowCount).Error)
			require.EqualValues(t, perfFlowRowCount, flowCount,
				"every seeded row should have been copied into mcp_oauth_flows")
		})
	}
}
