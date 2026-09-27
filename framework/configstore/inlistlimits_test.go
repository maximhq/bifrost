package configstore

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// inListLimitsPGSchema isolates these tests from the other Postgres-backed
// tests in this package, which drop and recreate shared schemas.
const inListLimitsPGSchema = "configstore_inlist_test"

// inListOverLimit is larger than both Postgres's 65,535 bind-parameter limit and
// SQLite's 32,766, so a query that binds one parameter per id fails on either.
const inListOverLimit = 70000

// inListNamedStore pairs a migrated RDBConfigStore with the dialect it runs on.
type inListNamedStore struct {
	name  string
	store *RDBConfigStore
	db    *gorm.DB
}

// inListLimitStores returns a fully migrated store on a dedicated Postgres
// schema, when the local test Postgres is reachable, and one on a fresh SQLite
// file.
func inListLimitStores(t *testing.T) []inListNamedStore {
	t.Helper()
	ctx := context.Background()
	var stores []inListNamedStore

	dsn := strings.Replace(postgresDSN, "search_path="+pgTestSchema, "search_path="+inListLimitsPGSchema, 1)
	if pgDB, err := gorm.Open(postgres.Open(dsn), inListGormConfig()); err == nil {
		if sqlDB, err := pgDB.DB(); err == nil && sqlDB.Ping() == nil {
			// Drop the schema afterwards: postgresIndexIsValid is not schema-scoped,
			// so indexes left here would satisfy other tests' existence checks.
			t.Cleanup(func() {
				_ = pgDB.Exec("DROP SCHEMA IF EXISTS " + inListLimitsPGSchema + " CASCADE").Error
				_ = sqlDB.Close()
			})
			require.NoError(t, pgDB.Exec("DROP SCHEMA IF EXISTS "+inListLimitsPGSchema+" CASCADE").Error)
			require.NoError(t, pgDB.Exec("CREATE SCHEMA "+inListLimitsPGSchema).Error)
			require.NoError(t, triggerMigrations(ctx, pgDB, testMigrationLogger))
			stores = append(stores, inListNamedStore{name: "postgres", store: newInListTestStore(pgDB), db: pgDB})
		}
	}

	sqliteDB, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "inlist.db")), inListGormConfig())
	require.NoError(t, err)
	require.NoError(t, triggerMigrations(ctx, sqliteDB, testMigrationLogger))
	return append(stores, inListNamedStore{name: "sqlite", store: newInListTestStore(sqliteDB), db: sqliteDB})
}

// inListGormConfig returns a fresh silent GORM config. Each connection needs
// its own: gorm.Open writes the dialector into the config it is given, so a
// shared config would switch an earlier Postgres handle to SQLite quoting.
func inListGormConfig() *gorm.Config {
	return &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)}
}

// newInListTestStore wraps db in an RDBConfigStore with no-op pool hooks.
func newInListTestStore(db *gorm.DB) *RDBConfigStore {
	s := &RDBConfigStore{logger: newMockLogger()}
	s.db.Store(db)
	s.migrateOnFreshFn = func(ctx context.Context, fn func(context.Context, *gorm.DB) error) error {
		return fn(ctx, s.DB())
	}
	s.refreshPoolFn = func(ctx context.Context) error { return nil }
	return s
}

// inListSeedSeries runs "INSERT ... SELECT ... FROM n" with n bound to the integers
// 1..count through a recursive CTE, which both SQLite and Postgres accept, so
// tens of thousands of rows are seeded in one statement without bind parameters.
func inListSeedSeries(t *testing.T, db *gorm.DB, count int, insert string) {
	t.Helper()
	sql := fmt.Sprintf("WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM n WHERE i < %d) %s", count, insert)
	require.NoError(t, db.Exec(sql).Error)
}

// inListSyntheticIDs returns count ids of the form prefix-000001 that match no row.
func inListSyntheticIDs(prefix string, count int) []string {
	ids := make([]string, count)
	for i := range ids {
		ids[i] = fmt.Sprintf("%s-%06d", prefix, i+1)
	}
	return ids
}

// inListCountRows returns the row count of table matching where.
func inListCountRows(t *testing.T, db *gorm.DB, table, where string, args ...any) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Table(table).Where(where, args...).Count(&n).Error)
	return n
}

// TestInListLimitDeleteModelConfigsWhere pins that tearing down a provider with
// more model configs than the bind-parameter limit succeeds and still removes
// every owned budget (multi-budget and legacy) and rate limit, while leaving
// another provider's configs untouched.
func TestInListLimitDeleteModelConfigsWhere(t *testing.T) {
	for _, ns := range inListLimitStores(t) {
		t.Run(ns.name, func(t *testing.T) {
			ctx := context.Background()
			db := ns.db
			inListSeedSeries(t, db, inListOverLimit, `INSERT INTO governance_model_configs
				(id, model_name, provider, scope, calendar_aligned, config_hash, created_at, updated_at)
				SELECT 'mc-' || i, 'model-' || i, 'openai', 'global', false, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP FROM n`)

			now := time.Now()
			mc1, mc2, mc3 := "mc-1", "mc-2", "mc-3"
			for _, b := range []tables.TableBudget{
				{ID: "b-multi-1", MaxLimit: 1, ResetDuration: "1d", LastReset: now, ModelConfigID: &mc1},
				{ID: "b-multi-2", MaxLimit: 1, ResetDuration: "1w", LastReset: now, ModelConfigID: &mc1},
				{ID: "b-legacy", MaxLimit: 1, ResetDuration: "1d", LastReset: now},
				{ID: "b-keep", MaxLimit: 1, ResetDuration: "1d", LastReset: now},
			} {
				require.NoError(t, db.Create(&b).Error)
			}
			require.NoError(t, db.Create(&tables.TableRateLimit{ID: "rl-1", TokenLastReset: now, RequestLastReset: now}).Error)
			require.NoError(t, db.Create(&tables.TableRateLimit{ID: "rl-keep", TokenLastReset: now, RequestLastReset: now}).Error)
			require.NoError(t, db.Exec("UPDATE governance_model_configs SET budget_id = ? WHERE id = ?", "b-legacy", mc2).Error)
			require.NoError(t, db.Exec("UPDATE governance_model_configs SET rate_limit_id = ? WHERE id = ?", "rl-1", mc3).Error)

			otherProvider := "anthropic"
			keepBudgetID, keepRateLimitID := "b-keep", "rl-keep"
			require.NoError(t, db.Create(&tables.TableModelConfig{
				ID: "mc-keep", ModelName: "model-keep", Provider: &otherProvider, Scope: "global",
				BudgetID: &keepBudgetID, RateLimitID: &keepRateLimitID,
			}).Error)

			err := db.Transaction(func(tx *gorm.DB) error {
				return ns.store.deleteModelConfigsWhere(ctx, tx, "provider = ?", "openai")
			})
			require.NoError(t, err)

			require.Zero(t, inListCountRows(t, db, "governance_model_configs", "provider = ?", "openai"))
			require.Equal(t, int64(1), inListCountRows(t, db, "governance_model_configs", "id = ?", "mc-keep"))
			require.Zero(t, inListCountRows(t, db, "governance_budgets", "id IN ?", []string{"b-multi-1", "b-multi-2", "b-legacy"}))
			require.Equal(t, int64(1), inListCountRows(t, db, "governance_budgets", "id = ?", "b-keep"))
			require.Zero(t, inListCountRows(t, db, "governance_rate_limits", "id = ?", "rl-1"))
			require.Equal(t, int64(1), inListCountRows(t, db, "governance_rate_limits", "id = ?", "rl-keep"))
		})
	}
}

// TestInListLimitMCPSessionFilters pins that the MCP sessions list accepts
// search-resolved, user-filter and virtual-key-filter id lists larger than the
// bind-parameter limit and still matches the one real row inside them.
func TestInListLimitMCPSessionFilters(t *testing.T) {
	for _, ns := range inListLimitStores(t) {
		t.Run(ns.name, func(t *testing.T) {
			ctx := context.Background()
			db := ns.db
			client := tables.TableMCPClient{ClientID: "client-inlist", Name: "inlist-client", ConnectionType: "http", AuthType: "per_user_oauth"}
			require.NoError(t, db.Create(&client).Error)
			userID := "user-real"
			require.NoError(t, db.Create(&tables.TableMCPOauthToken{
				ID: "tok-1", AuthMode: "user", MCPClientID: client.ClientID, OauthConfigID: "cfg-1",
				UserID: &userID, Status: "active", AccessToken: "x", TokenType: "Bearer", CreatedAt: time.Now(),
			}).Error)

			ids := append(inListSyntheticIDs("user", inListOverLimit), userID)

			tokens, err := ns.store.ListOauthUserTokens(ctx, MCPSessionsFilterParams{Search: "no-such-needle", MatchedUserIDs: ids})
			require.NoError(t, err)
			require.Len(t, tokens, 1)
			require.Equal(t, "tok-1", tokens[0].ID)

			tokens, err = ns.store.ListOauthUserTokens(ctx, MCPSessionsFilterParams{UserIDs: ids})
			require.NoError(t, err)
			require.Len(t, tokens, 1)

			tokens, err = ns.store.ListOauthUserTokens(ctx, MCPSessionsFilterParams{VirtualKeyIDs: inListSyntheticIDs("vk", inListOverLimit)})
			require.NoError(t, err)
			require.Empty(t, tokens)
		})
	}
}

// inListSeedVirtualKeys inserts count virtual keys with ids vk-1000001.. sharing one
// created_at, so the default (created_at, id) order is the id order.
func inListSeedVirtualKeys(t *testing.T, db *gorm.DB, count int) {
	t.Helper()
	inListSeedSeries(t, db, count, `INSERT INTO governance_virtual_keys
		(id, name, description, value, is_active, calendar_aligned, allow_all_providers, created_at, updated_at)
		SELECT 'vk-' || (1000000 + i), 'vk-name-' || i, '', 'sk-bf-' || i, true, false, false, '2026-01-01 00:00:00', '2026-01-01 00:00:00' FROM n`)
}

// TestInListLimitGetRedactedVirtualKeys pins that looking up redacted virtual
// keys by an id list larger than the bind-parameter limit succeeds and returns
// only the keys in that list.
func TestInListLimitGetRedactedVirtualKeys(t *testing.T) {
	for _, ns := range inListLimitStores(t) {
		t.Run(ns.name, func(t *testing.T) {
			inListSeedVirtualKeys(t, ns.db, 3)
			ids := append(inListSyntheticIDs("missing", inListOverLimit), "vk-1000001", "vk-1000003")
			vks, err := ns.store.GetRedactedVirtualKeys(context.Background(), ids)
			require.NoError(t, err)
			got := make([]string, 0, len(vks))
			for _, vk := range vks {
				got = append(got, vk.ID)
			}
			sort.Strings(got)
			require.Equal(t, []string{"vk-1000001", "vk-1000003"}, got)
		})
	}
}

// TestInListLimitGetVirtualKeysPaginatedExport pins that an export page of
// 10,000 virtual keys whose provider configs outnumber the bind-parameter limit
// loads successfully, in the same order and with the same total as before, and
// that an offset export window returns exactly the matching slice.
func TestInListLimitGetVirtualKeysPaginatedExport(t *testing.T) {
	const vkCount = 10000
	const configsPerVK = 7
	for _, ns := range inListLimitStores(t) {
		t.Run(ns.name, func(t *testing.T) {
			ctx := context.Background()
			inListSeedVirtualKeys(t, ns.db, vkCount)
			inListSeedSeries(t, ns.db, configsPerVK, `INSERT INTO governance_virtual_key_provider_configs
				(virtual_key_id, provider, allow_all_keys)
				SELECT vk.id, 'provider-' || n.i, false FROM governance_virtual_keys vk CROSS JOIN n`)
			expected := make([]string, vkCount)
			for i := range expected {
				expected[i] = fmt.Sprintf("vk-%d", 1000000+i+1)
			}

			vks, total, err := ns.store.GetVirtualKeysPaginated(ctx, VirtualKeyQueryParams{Export: true})
			require.NoError(t, err)
			require.Equal(t, int64(vkCount), total)
			require.Len(t, vks, vkCount)
			for i, vk := range vks {
				require.Equal(t, expected[i], vk.ID)
				require.Len(t, vk.ProviderConfigs, configsPerVK)
			}

			vks, total, err = ns.store.GetVirtualKeysPaginated(ctx, VirtualKeyQueryParams{Export: true, Offset: 500, Limit: 2500})
			require.NoError(t, err)
			require.Equal(t, int64(vkCount), total)
			require.Len(t, vks, 2500)
			for i, vk := range vks {
				require.Equal(t, expected[500+i], vk.ID)
			}

			vks, _, err = ns.store.GetVirtualKeysPaginated(ctx, VirtualKeyQueryParams{Export: true, Offset: 9500, SortBy: "name", Order: "desc"})
			require.NoError(t, err)
			require.Len(t, vks, 500)

			vks, _, err = ns.store.GetVirtualKeysPaginated(ctx, VirtualKeyQueryParams{Offset: 10, Limit: 5})
			require.NoError(t, err)
			require.Len(t, vks, 5)
			require.Equal(t, expected[10], vks[0].ID)
		})
	}
}

// TestInListLimitGetVirtualKeysPaginatedExportSnapshot pins that a chunked export
// reads every chunk from one snapshot. A virtual key created between chunks that
// sorts ahead of the current offset must not shift a row from the first chunk into
// the second (a duplicate), and the total must match the rows returned.
func TestInListLimitGetVirtualKeysPaginatedExportSnapshot(t *testing.T) {
	const vkCount = virtualKeyInternalPageSize + 10
	for _, ns := range inListLimitStores(t) {
		t.Run(ns.name, func(t *testing.T) {
			ctx := context.Background()
			if ns.name == "sqlite" {
				// Production runs SQLite in WAL mode, where a writer can commit while a
				// read transaction holds its snapshot.
				require.NoError(t, ns.db.Exec("PRAGMA journal_mode=WAL").Error)
			}
			inListSeedVirtualKeys(t, ns.db, vkCount)

			// After the first chunk's Find, create a key that sorts first, from a
			// connection outside any export transaction.
			var inserted bool
			callbackName := "test:insert-vk-between-export-chunks"
			require.NoError(t, ns.db.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
				if inserted || tx.Error != nil || tx.Statement.Table != "governance_virtual_keys" {
					return
				}
				if _, ok := tx.Statement.Dest.(*[]tables.TableVirtualKey); !ok {
					return
				}
				inserted = true
				require.NoError(t, ns.db.Session(&gorm.Session{NewDB: true}).Exec(`INSERT INTO governance_virtual_keys
					(id, name, description, value, is_active, calendar_aligned, allow_all_providers, created_at, updated_at)
					VALUES ('vk-0000000', 'vk-name-early', '', 'sk-bf-early', true, false, false, '2025-01-01 00:00:00', '2025-01-01 00:00:00')`).Error)
			}))
			t.Cleanup(func() { _ = ns.db.Callback().Query().Remove(callbackName) })

			vks, total, err := ns.store.GetVirtualKeysPaginated(ctx, VirtualKeyQueryParams{Export: true})
			require.NoError(t, err)
			require.True(t, inserted, "the mid-export insert never ran")

			seen := make(map[string]bool, len(vks))
			for _, vk := range vks {
				require.False(t, seen[vk.ID], "export returned %s twice", vk.ID)
				seen[vk.ID] = true
			}
			require.False(t, seen["vk-0000000"], "export mixed in a key created after it started")
			require.Len(t, vks, vkCount)
			require.Equal(t, int64(len(vks)), total)
		})
	}
}
