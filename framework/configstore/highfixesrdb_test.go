package configstore

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// highFixPGSchema isolates these tests from other Postgres-backed tests in this package.
const highFixPGSchema = "configstore_highfix_test"

// namedStore pairs a backend name with a store for dialect subtests.
type namedStore struct {
	name  string
	store *RDBConfigStore
}

// highFixStores returns an in-memory SQLite store and, when the local test
// Postgres is reachable, a Postgres store in a dedicated schema that went through
// the real migration chain.
func highFixStores(t *testing.T) []namedStore {
	t.Helper()
	stores := []namedStore{{name: "sqlite", store: setupRDBTestStore(t)}}

	dsn := strings.Replace(postgresDSN, "search_path="+pgTestSchema, "search_path="+highFixPGSchema, 1)
	if dsn == postgresDSN {
		return stores
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return stores
	}
	sqlDB, err := db.DB()
	if err != nil || sqlDB.Ping() != nil {
		return stores
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.Exec("DROP SCHEMA IF EXISTS "+highFixPGSchema+" CASCADE").Error)
	require.NoError(t, db.Exec("CREATE SCHEMA "+highFixPGSchema).Error)
	require.NoError(t, triggerMigrations(context.Background(), db, testMigrationLogger))

	store := &RDBConfigStore{logger: bifrost.NewDefaultLogger(schemas.LogLevelError)}
	store.db.Store(db)
	store.migrateOnFreshFn = func(ctx context.Context, fn func(context.Context, *gorm.DB) error) error {
		return fn(ctx, store.DB())
	}
	store.refreshPoolFn = func(ctx context.Context) error { return nil }
	return append(stores, namedStore{name: "postgres", store: store})
}

// statementCounter counts executed SQL statements that match a predicate, so a
// test can prove a path issues a constant number of statements instead of one per row.
type statementCounter struct {
	n     atomic.Int64
	match func(sql string) bool
}

// count returns the number of matching statements seen so far.
func (c *statementCounter) count() int64 { return c.n.Load() }

// countStatements registers after-callbacks on every GORM processor and counts the
// statements whose SQL satisfies match. The callbacks are removed on cleanup.
func countStatements(t *testing.T, db *gorm.DB, match func(sql string) bool) *statementCounter {
	t.Helper()
	c := &statementCounter{match: match}
	name := fmt.Sprintf("highfix:count:%p", c)
	hook := func(tx *gorm.DB) {
		if c.match(strings.ToUpper(tx.Statement.SQL.String())) {
			c.n.Add(1)
		}
	}
	cb := db.Callback()
	require.NoError(t, cb.Create().After("gorm:create").Register(name, hook))
	require.NoError(t, cb.Query().After("gorm:query").Register(name, hook))
	require.NoError(t, cb.Update().After("gorm:update").Register(name, hook))
	require.NoError(t, cb.Delete().After("gorm:delete").Register(name, hook))
	require.NoError(t, cb.Row().After("gorm:row").Register(name, hook))
	require.NoError(t, cb.Raw().After("gorm:raw").Register(name, hook))
	t.Cleanup(func() {
		_ = cb.Create().Remove(name)
		_ = cb.Query().Remove(name)
		_ = cb.Update().Remove(name)
		_ = cb.Delete().Remove(name)
		_ = cb.Row().Remove(name)
		_ = cb.Raw().Remove(name)
	})
	return c
}

// deleteTarget returns the table a DELETE statement deletes from (upper-cased,
// unquoted), or "" when sql is not a DELETE. Tables named only in a subquery do not count.
func deleteTarget(sql string) string {
	fields := strings.Fields(sql)
	if len(fields) < 3 || fields[0] != "DELETE" || fields[1] != "FROM" {
		return ""
	}
	return strings.Trim(fields[2], "`\"")
}

// createTestVK inserts a minimal virtual key with the given id and created_at.
func createTestVK(t *testing.T, db *gorm.DB, id string, createdAt time.Time) {
	t.Helper()
	vk := tables.TableVirtualKey{ID: id, Name: "name-" + id, Value: *schemas.NewSecretVar("value-" + id), CreatedAt: createdAt, UpdatedAt: createdAt}
	require.NoError(t, db.Omit("ProviderConfigs", "MCPConfigs").Create(&vk).Error)
}

// createTestMCPClient inserts a minimal MCP client row and returns it.
func createTestMCPClient(t *testing.T, db *gorm.DB, clientID string, allowOnAll bool) tables.TableMCPClient {
	t.Helper()
	c := tables.TableMCPClient{ClientID: clientID, Name: clientID, EndpointSlug: clientID, ConnectionType: "http", AllowByDefault: allowOnAll}
	require.NoError(t, db.Create(&c).Error)
	return c
}

// TestHighFixDeleteMCPClientConfigSetBased pins H13: deleting an MCP client removes
// every VK assignment for it with one statement, and leaves other clients' rows alone.
func TestHighFixDeleteMCPClientConfigSetBased(t *testing.T) {
	for _, ns := range highFixStores(t) {
		t.Run(ns.name, func(t *testing.T) {
			ctx := context.Background()
			db := ns.store.DB()
			target := createTestMCPClient(t, db, "mcp-target", false)
			other := createTestMCPClient(t, db, "mcp-other", false)
			now := time.Now().UTC()
			for i := 0; i < 12; i++ {
				vkID := fmt.Sprintf("vk-%02d", i)
				createTestVK(t, db, vkID, now)
				require.NoError(t, db.Create(&tables.TableVirtualKeyMCPConfig{VirtualKeyID: vkID, MCPClientID: target.ID}).Error)
				require.NoError(t, db.Create(&tables.TableVirtualKeyMCPConfig{VirtualKeyID: vkID, MCPClientID: other.ID}).Error)
			}

			deletes := countStatements(t, db, func(sql string) bool {
				return deleteTarget(sql) == "GOVERNANCE_VIRTUAL_KEY_MCP_CONFIGS"
			})
			require.NoError(t, ns.store.DeleteMCPClientConfig(ctx, target.ClientID))
			require.EqualValues(t, 1, deletes.count(), "VK assignments must be removed with one statement, not one per row")

			var remainingTarget, remainingOther int64
			require.NoError(t, db.Model(&tables.TableVirtualKeyMCPConfig{}).Where("mcp_client_id = ?", target.ID).Count(&remainingTarget).Error)
			require.NoError(t, db.Model(&tables.TableVirtualKeyMCPConfig{}).Where("mcp_client_id = ?", other.ID).Count(&remainingOther).Error)
			require.Zero(t, remainingTarget)
			require.EqualValues(t, 12, remainingOther)
			var clients int64
			require.NoError(t, db.Model(&tables.TableMCPClient{}).Where("client_id = ?", target.ClientID).Count(&clients).Error)
			require.Zero(t, clients)
		})
	}
}

// providerFixture is the state built by buildProviderFixture.
type providerFixture struct {
	keyIDs      map[string]uint // provider key_id -> config_keys.id
	targetVKPCs []uint          // provider configs for the provider under test
	otherVKPCs  []uint          // provider configs for another provider
}

// buildProviderFixture creates providers "openai" (keys k1, k2) and "anthropic"
// (key a1), and n VKs that each hold one provider config per provider with a
// budget, a rate limit and join rows to every key of that provider.
func buildProviderFixture(t *testing.T, s *RDBConfigStore, n int) providerFixture {
	t.Helper()
	ctx := context.Background()
	db := s.DB()
	require.NoError(t, s.UpdateProvidersConfig(ctx, map[schemas.ModelProvider]ProviderConfig{
		"openai": {Keys: []schemas.Key{
			{ID: "k1", Name: "k1", Value: *schemas.NewSecretVar("sk-1"), Weight: 1},
			{ID: "k2", Name: "k2", Value: *schemas.NewSecretVar("sk-2"), Weight: 1},
		}},
		"anthropic": {Keys: []schemas.Key{
			{ID: "a1", Name: "a1", Value: *schemas.NewSecretVar("sk-a1"), Weight: 1},
		}},
	}))
	var keys []tables.TableKey
	require.NoError(t, db.Find(&keys).Error)
	fx := providerFixture{keyIDs: map[string]uint{}}
	for _, k := range keys {
		fx.keyIDs[k.KeyID] = k.ID
	}
	now := time.Now().UTC()
	for i := 0; i < n; i++ {
		vkID := fmt.Sprintf("vk-p-%02d", i)
		createTestVK(t, db, vkID, now)
		for _, p := range []struct {
			provider string
			keys     []string
		}{{"openai", []string{"k1", "k2"}}, {"anthropic", []string{"a1"}}} {
			rlID := fmt.Sprintf("rl-%s-%s", p.provider, vkID)
			require.NoError(t, db.Create(&tables.TableRateLimit{ID: rlID}).Error)
			pc := tables.TableVirtualKeyProviderConfig{VirtualKeyID: vkID, Provider: p.provider, RateLimitID: &rlID}
			require.NoError(t, db.Omit("Keys", "Budgets", "RateLimit").Create(&pc).Error)
			pcID := pc.ID
			require.NoError(t, db.Create(&tables.TableBudget{ID: fmt.Sprintf("b-%s-%s", p.provider, vkID), MaxLimit: 10, ResetDuration: "1d", ProviderConfigID: &pcID}).Error)
			for _, k := range p.keys {
				require.NoError(t, db.Create(&tables.TableVirtualKeyProviderConfigKey{TableVirtualKeyProviderConfigID: pc.ID, TableKeyID: fx.keyIDs[k]}).Error)
			}
			if p.provider == "openai" {
				fx.targetVKPCs = append(fx.targetVKPCs, pc.ID)
			} else {
				fx.otherVKPCs = append(fx.otherVKPCs, pc.ID)
			}
		}
	}
	return fx
}

// countRows counts rows of table matching where.
func countTableRows(t *testing.T, db *gorm.DB, table, where string, args ...any) int64 {
	t.Helper()
	var n int64
	q := db.Table(table)
	if where != "" {
		q = q.Where(where, args...)
	}
	require.NoError(t, q.Count(&n).Error)
	return n
}

// TestHighFixDeleteProviderCleansProviderConfigsSetBased pins H14: deleting a
// provider removes its VK provider configs, their join rows, budgets and rate
// limits with a constant number of statements, and leaves other providers intact.
func TestHighFixDeleteProviderCleansProviderConfigsSetBased(t *testing.T) {
	for _, ns := range highFixStores(t) {
		t.Run(ns.name, func(t *testing.T) {
			ctx := context.Background()
			db := ns.store.DB()
			fx := buildProviderFixture(t, ns.store, 10)

			vkpcDeletes := countStatements(t, db, func(sql string) bool {
				return deleteTarget(sql) == "GOVERNANCE_VIRTUAL_KEY_PROVIDER_CONFIGS"
			})
			require.NoError(t, ns.store.DeleteProvider(ctx, "openai"))
			require.LessOrEqual(t, vkpcDeletes.count(), int64(1), "provider configs must be deleted set-based, not one statement per row")

			require.Zero(t, countTableRows(t, db, "governance_virtual_key_provider_configs", "provider = ?", "openai"))
			require.EqualValues(t, 10, countTableRows(t, db, "governance_virtual_key_provider_configs", "provider = ?", "anthropic"))
			require.Zero(t, countTableRows(t, db, "governance_budgets", "id LIKE ?", "b-openai-%"))
			require.EqualValues(t, 10, countTableRows(t, db, "governance_budgets", "id LIKE ?", "b-anthropic-%"))
			require.Zero(t, countTableRows(t, db, "governance_rate_limits", "id LIKE ?", "rl-openai-%"))
			require.EqualValues(t, 10, countTableRows(t, db, "governance_rate_limits", "id LIKE ?", "rl-anthropic-%"))
			require.Zero(t, countTableRows(t, db, "governance_virtual_key_provider_config_keys", "table_key_id IN ?", []uint{fx.keyIDs["k1"], fx.keyIDs["k2"]}))
			require.EqualValues(t, 10, countTableRows(t, db, "governance_virtual_key_provider_config_keys", "table_key_id = ?", fx.keyIDs["a1"]))
			if ns.name == "postgres" {
				// Provider keys go by ON DELETE CASCADE, which the SQLite test store does not enforce.
				require.Zero(t, countTableRows(t, db, "config_keys", "key_id IN ?", []string{"k1", "k2"}))
			}
		})
	}
}

// TestHighFixUpdateProviderRemovedKeysSetBased pins H14's UpdateProvider path:
// dropping a key removes its join rows from every VK provider config of that
// provider in one statement, and keeps the join rows of surviving keys.
func TestHighFixUpdateProviderRemovedKeysSetBased(t *testing.T) {
	for _, ns := range highFixStores(t) {
		t.Run(ns.name, func(t *testing.T) {
			ctx := context.Background()
			db := ns.store.DB()
			fx := buildProviderFixture(t, ns.store, 10)

			joinDeletes := countStatements(t, db, func(sql string) bool {
				return deleteTarget(sql) == "GOVERNANCE_VIRTUAL_KEY_PROVIDER_CONFIG_KEYS"
			})
			require.NoError(t, ns.store.UpdateProvider(ctx, "openai", ProviderConfig{Keys: []schemas.Key{
				{ID: "k1", Name: "k1", Value: *schemas.NewSecretVar("sk-1"), Weight: 1},
			}}))
			require.LessOrEqual(t, joinDeletes.count(), int64(1), "join rows must be deleted set-based, not one statement per provider config")

			require.Zero(t, countTableRows(t, db, "governance_virtual_key_provider_config_keys", "table_key_id = ?", fx.keyIDs["k2"]))
			require.EqualValues(t, 10, countTableRows(t, db, "governance_virtual_key_provider_config_keys", "table_key_id = ?", fx.keyIDs["k1"]))
			require.EqualValues(t, 10, countTableRows(t, db, "governance_virtual_key_provider_config_keys", "table_key_id = ?", fx.keyIDs["a1"]))
			require.EqualValues(t, 20, countTableRows(t, db, "governance_virtual_key_provider_configs", ""))
			require.Zero(t, countTableRows(t, db, "config_keys", "key_id = ?", "k2"))

			require.NoError(t, ns.store.DeleteProviderKey(ctx, "openai", "k1"))
			require.Zero(t, countTableRows(t, db, "governance_virtual_key_provider_config_keys", "table_key_id = ?", fx.keyIDs["k1"]))
			require.EqualValues(t, 10, countTableRows(t, db, "governance_virtual_key_provider_config_keys", "table_key_id = ?", fx.keyIDs["a1"]))
		})
	}
}

// TestHighFixVirtualKeyKeysetPaging pins H27: the row-comparison keyset returns
// exactly the rows, in exactly the order, of one ordered scan, including runs of
// identical created_at values that straddle page boundaries.
func TestHighFixVirtualKeyKeysetPaging(t *testing.T) {
	for _, ns := range highFixStores(t) {
		t.Run(ns.name, func(t *testing.T) {
			ctx := context.Background()
			db := ns.store.DB()
			base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
			for i := 0; i < 23; i++ {
				// Groups of 4 share a created_at, with ids inserted out of order.
				createTestVK(t, db, fmt.Sprintf("vk-%02d", (i*7)%23), base.Add(time.Duration(i/4)*time.Second))
			}

			var want []tables.TableVirtualKey
			require.NoError(t, db.Order("created_at ASC, id ASC").Find(&want).Error)
			wantIDs := make([]string, 0, len(want))
			for _, vk := range want {
				wantIDs = append(wantIDs, vk.ID)
			}

			var gotIDs []string
			var lastCreatedAt time.Time
			var lastID string
			hasCursor := false
			for {
				page, err := ns.store.getVirtualKeysPage(ctx, 3, lastCreatedAt, lastID, hasCursor)
				require.NoError(t, err)
				if len(page) == 0 {
					break
				}
				for _, vk := range page {
					gotIDs = append(gotIDs, vk.ID)
				}
				lastCreatedAt, lastID, hasCursor = page[len(page)-1].CreatedAt, page[len(page)-1].ID, true
			}
			require.Equal(t, wantIDs, gotIDs)

			all, err := ns.store.GetVirtualKeys(ctx)
			require.NoError(t, err)
			allIDs := make([]string, 0, len(all))
			for _, vk := range all {
				allIDs = append(allIDs, vk.ID)
			}
			require.Equal(t, wantIDs, allIDs)

			gov, err := ns.store.getGovernanceConfigVirtualKeys(ctx)
			require.NoError(t, err)
			govIDs := make([]string, 0, len(gov))
			for _, vk := range gov {
				govIDs = append(govIDs, vk.ID)
			}
			require.Equal(t, wantIDs, govIDs)
		})
	}
}

// TestHighFixVirtualKeyKeysetUsesCompositeIndex pins that on Postgres the keyset
// predicate is an index condition on idx_virtual_keys_created_at_id rather than a
// filter applied after reading from the start of the created_at index.
func TestHighFixVirtualKeyKeysetUsesCompositeIndex(t *testing.T) {
	var pg *RDBConfigStore
	for _, ns := range highFixStores(t) {
		if ns.name == "postgres" {
			pg = ns.store
		}
	}
	if pg == nil {
		t.Skip("postgres not available")
	}
	db := pg.DB()
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	for i := 0; i < 50; i++ {
		createTestVK(t, db, fmt.Sprintf("vk-%03d", i), base.Add(time.Duration(i/5)*time.Second))
	}
	require.NoError(t, db.Exec("ANALYZE governance_virtual_keys").Error)
	var plan []string
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SET LOCAL enable_seqscan = off").Error; err != nil {
			return err
		}
		stmt := tx.Session(&gorm.Session{DryRun: true}).Model(&tables.TableVirtualKey{}).
			Where(virtualKeyKeysetCondition, base, "vk-010").
			Order("governance_virtual_keys.created_at ASC, governance_virtual_keys.id ASC").Limit(3).Find(&[]tables.TableVirtualKey{}).Statement
		return tx.Raw("EXPLAIN "+stmt.SQL.String(), stmt.Vars...).Scan(&plan).Error
	}))
	joined := strings.Join(plan, "\n")
	require.Contains(t, joined, "idx_virtual_keys_created_at_id", joined)
	require.Contains(t, joined, "Index Cond", joined)
}

// TestHighFixProviderJobTerminalClearsNextCheckAt pins H28: a job that reaches a
// terminal accounting status no longer carries next_check_at, a job released with
// an error keeps it, re-opening an unpriceable job makes it due again, and
// ListDueProviderJobs returns the same non-terminal jobs as before.
func TestHighFixProviderJobTerminalClearsNextCheckAt(t *testing.T) {
	for _, ns := range highFixStores(t) {
		t.Run(ns.name, func(t *testing.T) {
			ctx := context.Background()
			s := ns.store
			db := s.DB()
			require.NoError(t, ensureProviderJobTable(t, db))
			due := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
			for _, id := range []string{"j-accounted", "j-unpriceable", "j-error", "j-pending"} {
				require.NoError(t, s.UpsertProviderJob(ctx, &tables.TableProviderJob{ID: id, Provider: "openai", JobID: id, NextCheckAt: &due}))
			}
			stale := time.Now().UTC().Add(-time.Hour)
			for _, id := range []string{"j-accounted", "j-unpriceable", "j-error"} {
				ok, err := s.ClaimProviderJob(ctx, id, "runner", stale, false)
				require.NoError(t, err)
				require.True(t, ok)
			}
			require.NoError(t, s.CompleteProviderJob(ctx, "j-accounted", "runner"))
			require.NoError(t, s.MarkProviderJobUnpriceable(ctx, "j-unpriceable", "runner", "max_poll_attempts", nil))
			require.NoError(t, s.FailProviderJob(ctx, "j-error", "runner", fmt.Errorf("boom")))

			nextOf := func(id string) *time.Time {
				job, err := s.GetProviderJob(ctx, id)
				require.NoError(t, err)
				return job.NextCheckAt
			}
			require.Nil(t, nextOf("j-accounted"))
			require.Nil(t, nextOf("j-unpriceable"))
			require.NotNil(t, nextOf("j-error"))
			require.NotNil(t, nextOf("j-pending"))

			jobs, err := s.ListDueProviderJobs(ctx, "", "", time.Now().UTC(), 10)
			require.NoError(t, err)
			ids := make([]string, 0, len(jobs))
			for _, j := range jobs {
				ids = append(ids, j.ID)
			}
			sort.Strings(ids)
			require.Equal(t, []string{"j-error", "j-pending"}, ids)

			// Settling an unpriceable job with results in hand re-opens it; if that
			// attempt fails the sweeper must still see it, as it did before the
			// terminal transition cleared next_check_at.
			ok, err := s.ClaimProviderJob(ctx, "j-unpriceable", "runner-2", stale, true)
			require.NoError(t, err)
			require.True(t, ok)
			require.NotNil(t, nextOf("j-unpriceable"))
			require.NoError(t, s.FailProviderJob(ctx, "j-unpriceable", "runner-2", fmt.Errorf("again")))
			jobs, err = s.ListDueProviderJobs(ctx, "", "", time.Now().UTC().Add(time.Second), 10)
			require.NoError(t, err)
			ids = ids[:0]
			for _, j := range jobs {
				ids = append(ids, j.ID)
			}
			sort.Strings(ids)
			require.Equal(t, []string{"j-error", "j-pending", "j-unpriceable"}, ids)
		})
	}
}

// ensureProviderJobTable creates batch_jobs for stores built without the full
// migration chain (the SQLite test store), matching the fresh-install shape.
func ensureProviderJobTable(t *testing.T, db *gorm.DB) error {
	t.Helper()
	if db.Migrator().HasTable(&tables.TableProviderJob{}) {
		return nil
	}
	return db.Migrator().CreateTable(&tables.TableProviderJob{})
}

// credRow is the comparable projection of one credential or flow row.
type credRow struct {
	Tbl    string
	ID     string
	Status string
}

// snapshotCredentialState returns every OAuth and header credential/flow row as
// (table, id, status), sorted, so two reconcile strategies can be compared.
func snapshotCredentialState(t *testing.T, db *gorm.DB) []credRow {
	t.Helper()
	var out []credRow
	for _, table := range []string{"mcp_oauth_tokens", "mcp_oauth_flows", "mcp_per_user_header_credentials", "mcp_per_user_header_flows"} {
		var rows []credRow
		require.NoError(t, db.Table(table).Select("? AS tbl, id, status", table).Scan(&rows).Error)
		out = append(out, rows...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tbl != out[j].Tbl {
			return out[i].Tbl < out[j].Tbl
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// seedReconcileFixture builds MCP clients, VKs, assignments and credential/flow
// rows covering every reconcile branch: allowed and disallowed clients, explicit
// and allow-on-all grants, VKs with no grants at all, orphaned rows that must
// come back, pending and non-pending flows, non-VK modes, and rows of other MCPs.
func seedReconcileFixture(t *testing.T, db *gorm.DB, everyone bool) {
	t.Helper()
	target := createTestMCPClient(t, db, "mcp-target", false)
	granted := createTestMCPClient(t, db, "mcp-granted", false)
	createTestMCPClient(t, db, "mcp-everyone", everyone)
	createTestMCPClient(t, db, "mcp-revoked", false)
	now := time.Now().UTC()
	exp := now.Add(time.Hour)
	for i := 0; i < 6; i++ {
		createTestVK(t, db, fmt.Sprintf("vk-r-%d", i), now)
	}
	// vk-r-0: target granted; vk-r-1: granted only; vk-r-2..: no explicit grants.
	require.NoError(t, db.Create(&tables.TableVirtualKeyMCPConfig{VirtualKeyID: "vk-r-0", MCPClientID: target.ID}).Error)
	require.NoError(t, db.Create(&tables.TableVirtualKeyMCPConfig{VirtualKeyID: "vk-r-1", MCPClientID: granted.ID}).Error)
	require.NoError(t, db.Create(&tables.TableVirtualKeyMCPConfig{VirtualKeyID: "vk-r-3", MCPClientID: granted.ID}).Error)

	strp := func(s string) *string { return &s }
	n := 0
	token := func(vk *string, client, mode, status string) {
		n++
		require.NoError(t, db.Create(&tables.TableMCPOauthToken{ID: fmt.Sprintf("tok-%02d", n), AuthMode: mode, MCPClientID: client, VirtualKeyID: vk,
			Status: status, AccessToken: "at", TokenType: "Bearer", CreatedAt: now, UpdatedAt: now}).Error)
	}
	flow := func(vk *string, client, mode, status string) {
		n++
		require.NoError(t, db.Create(&tables.TableMCPOauthFlow{ID: fmt.Sprintf("flow-%02d", n), MCPClientID: client, OauthConfigID: "oc", State: fmt.Sprintf("s-%d", n),
			VirtualKeyID: vk, FlowMode: mode, Status: status, ExpiresAt: exp, CreatedAt: now, UpdatedAt: now}).Error)
	}
	header := func(vk *string, client, mode, status string) {
		n++
		require.NoError(t, db.Create(&tables.TableMCPPerUserHeaderCredential{ID: fmt.Sprintf("hdr-%02d", n), VirtualKeyID: vk, MCPClientID: client, AuthMode: mode,
			Status: status, HeadersJSON: "{}", CreatedAt: now, UpdatedAt: now}).Error)
	}
	hflow := func(vk *string, client, mode, status string) {
		n++
		require.NoError(t, db.Create(&tables.TableMCPPerUserHeaderFlow{ID: fmt.Sprintf("hflow-%02d", n), MCPClientID: client, VirtualKeyID: vk,
			FlowMode: mode, Status: status, ExpiresAt: exp, CreatedAt: now, UpdatedAt: now}).Error)
	}
	statuses := []string{"active", "orphaned", "needs_reauth"}
	for i := 0; i < 6; i++ {
		vk := strp(fmt.Sprintf("vk-r-%d", i))
		for ci, client := range []string{"mcp-target", "mcp-granted", "mcp-everyone", "mcp-revoked", ""} {
			// One vk-mode credential per (VK, client): the partial unique indexes allow no more.
			token(vk, client, "vk", statuses[(i+ci)%len(statuses)])
			for _, status := range []string{"pending", "authorized", "expired"} {
				flow(vk, client, "vk", status)
			}
			token(vk, client, "user", "active")
			flow(vk, client, "user", "pending")
			// Header rows carry Postgres FKs to the client, so only real clients get them.
			if client == "" {
				continue
			}
			header(vk, client, "vk", statuses[(i+ci+1)%len(statuses)])
			for _, status := range []string{"pending", "completed", "expired"} {
				hflow(vk, client, "vk", status)
			}
			header(vk, client, "user", "active")
			hflow(vk, client, "user", "pending")
		}
	}
	// NULL client ids: kept by a VK with grants (NOT IN is unknown for NULL), and
	// orphaned by a VK whose allowlist is empty (no NOT IN filter at all).
	for _, vk := range []string{"vk-r-0", "vk-r-2"} {
		token(strp(vk), "placeholder-"+vk, "vk", "active")
		require.NoError(t, db.Exec("UPDATE mcp_oauth_tokens SET mcp_client_id = NULL WHERE mcp_client_id = ?", "placeholder-"+vk).Error)
	}
	// A VK that holds nothing for the target MCP must not be reconciled at all.
	createTestVK(t, db, "vk-untouched", now)
	token(strp("vk-untouched"), "mcp-revoked", "vk", "active")
	header(strp("vk-untouched"), "mcp-revoked", "vk", "active")
	// Rows with no VK id.
	token(nil, "mcp-target", "vk", "active")
	header(nil, "mcp-target", "vk", "active")
}

// legacyReconcileAfterMCPChange is the per-VK loop the set-based rewrite replaced,
// kept verbatim as the reference the new statements are compared against.
func legacyReconcileAfterMCPChange(t *testing.T, db *gorm.DB, mcpClientID string, headers bool) {
	t.Helper()
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		read, reconcile := readVKsHoldingOauthCredsForMCP, reconcileVKDirectTokensDB
		if headers {
			read, reconcile = readVKsHoldingHeaderCredsForMCP, reconcileVKDirectHeaderRowsDB
		}
		vkIDs, err := read(tx, mcpClientID)
		if err != nil {
			return err
		}
		sort.Strings(vkIDs)
		for _, vkID := range vkIDs {
			if err := reconcile(tx, vkID); err != nil {
				return err
			}
		}
		return nil
	}))
}

// TestHighFixReconcileAfterMCPChangeMatchesLegacy pins H12: the set-based
// ReconcileOauthAfterMCPChange / ReconcileMCPHeadersAfterMCPChange leave exactly
// the same rows, with the same statuses, as the per-VK loop they replaced, and
// issue a constant number of statements.
func TestHighFixReconcileAfterMCPChangeMatchesLegacy(t *testing.T) {
	for _, ns := range highFixStores(t) {
		t.Run(ns.name, func(t *testing.T) {
			ctx := context.Background()
			db := ns.store.DB()
			for _, variant := range []struct {
				name     string
				clientID string
				everyone bool
			}{
				{"target", "mcp-target", true}, {"target-no-allow-all", "mcp-target", false},
				{"revoked", "mcp-revoked", true}, {"revoked-no-allow-all", "mcp-revoked", false},
				{"everyone", "mcp-everyone", true}, {"granted", "mcp-granted", false},
			} {
				clientID := variant.clientID
				t.Run(variant.name, func(t *testing.T) {
					resetReconcileTables(t, db)
					seedReconcileFixture(t, db, variant.everyone)
					before := snapshotCredentialState(t, db)
					legacyReconcileAfterMCPChange(t, db, clientID, false)
					legacyReconcileAfterMCPChange(t, db, clientID, true)
					want := snapshotCredentialState(t, db)
					require.NotEqual(t, before, want, "fixture must exercise at least one change")

					resetReconcileTables(t, db)
					seedReconcileFixture(t, db, variant.everyone)
					runSetBasedReconcile(t, ctx, ns.store, clientID)
					require.Equal(t, want, snapshotCredentialState(t, db))
				})
			}
		})
	}
}

// runSetBasedReconcile runs both MCP-change reconciles through the store and
// asserts the statement count does not grow with the number of VKs.
func runSetBasedReconcile(t *testing.T, ctx context.Context, s *RDBConfigStore, clientID string) {
	t.Helper()
	stmts := countStatements(t, s.DB(), func(sql string) bool {
		return strings.Contains(sql, "MCP_OAUTH_") || strings.Contains(sql, "MCP_PER_USER_HEADER_")
	})
	require.NoError(t, s.ReconcileOauthAfterMCPChange(ctx, clientID))
	require.NoError(t, s.ReconcileMCPHeadersAfterMCPChange(ctx, clientID))
	require.LessOrEqual(t, stmts.count(), int64(14), "reconcile must be set-based, not ~5 statements per VK")
}

// resetReconcileTables empties the tables seedReconcileFixture writes.
func resetReconcileTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, table := range []string{"mcp_oauth_tokens", "mcp_oauth_flows", "mcp_per_user_header_credentials", "mcp_per_user_header_flows",
		"governance_virtual_key_mcp_configs", "governance_virtual_keys", "config_mcp_clients"} {
		require.NoError(t, db.Exec("DELETE FROM "+table).Error)
	}
}

// TestHighFixGetRedactedVirtualKeysAppliesQueryScope pins that a QueryScope on ctx
// restricts GetRedactedVirtualKeys on both the id-list and the list-all paths, as
// it does for GetVirtualKey, so callers that resolve VK names through it cannot see
// keys outside the caller's scope.
func TestHighFixGetRedactedVirtualKeysAppliesQueryScope(t *testing.T) {
	for _, ns := range highFixStores(t) {
		t.Run(ns.name, func(t *testing.T) {
			db := ns.store.DB()
			now := time.Now().UTC()
			createTestVK(t, db, "vk-visible", now)
			createTestVK(t, db, "vk-hidden", now)
			ctx := queryscope.WithQueryScope(context.Background(), func(db *gorm.DB) *gorm.DB {
				return db.Where("governance_virtual_keys.id = ?", "vk-visible")
			})

			byID, err := ns.store.GetRedactedVirtualKeys(ctx, []string{"vk-visible", "vk-hidden"})
			require.NoError(t, err)
			require.Len(t, byID, 1)
			require.Equal(t, "vk-visible", byID[0].ID)

			all, err := ns.store.GetRedactedVirtualKeys(ctx, nil)
			require.NoError(t, err)
			require.Len(t, all, 1)
			require.Equal(t, "vk-visible", all[0].ID)

			unscoped, err := ns.store.GetRedactedVirtualKeys(context.Background(), nil)
			require.NoError(t, err)
			require.Len(t, unscoped, 2)
		})
	}
}

// TestGetRedactedTeamsAndCustomers pins the batch name lookups rankings use to
// show each team's and customer's current name: exactly the listed ids come
// back with their names, an unknown id is simply absent, and an empty list
// returns nothing rather than every row.
func TestGetRedactedTeamsAndCustomers(t *testing.T) {
	for _, ns := range highFixStores(t) {
		t.Run(ns.name, func(t *testing.T) {
			ctx := context.Background()
			db := ns.store.DB()
			require.NoError(t, db.Create(&tables.TableCustomer{ID: "cust-1", Name: "Acme"}).Error)
			require.NoError(t, db.Create(&tables.TableCustomer{ID: "cust-2", Name: "Globex"}).Error)
			require.NoError(t, db.Create(&tables.TableTeam{ID: "team-1", Name: "Platform"}).Error)
			require.NoError(t, db.Create(&tables.TableTeam{ID: "team-2", Name: "Search"}).Error)

			teams, err := ns.store.GetRedactedTeams(ctx, []string{"team-2", "team-missing"})
			require.NoError(t, err)
			require.Len(t, teams, 1)
			require.Equal(t, "team-2", teams[0].ID)
			require.Equal(t, "Search", teams[0].Name)

			customers, err := ns.store.GetRedactedCustomers(ctx, []string{"cust-1"})
			require.NoError(t, err)
			require.Len(t, customers, 1)
			require.Equal(t, "Acme", customers[0].Name)

			none, err := ns.store.GetRedactedTeams(ctx, nil)
			require.NoError(t, err)
			require.Empty(t, none, "an empty id list must not return every team")
			noCustomers, err := ns.store.GetRedactedCustomers(ctx, nil)
			require.NoError(t, err)
			require.Empty(t, noCustomers, "an empty id list must not return every customer")
		})
	}
}

// TestHighFixReconcileLockOrderMatchesPerVKPath pins that the bulk MCP-change
// reconcile takes row locks in the same order as the per-VK path (active rows,
// then orphaned rows), so concurrent MCP and VK edits cannot deadlock. The test
// forces the bad interleave: the per-VK transaction holds the active row while
// the bulk reconcile runs, then reaches for the orphaned row the bulk reconcile
// would already hold if it locked in (virtual_key_id, id) order.
func TestHighFixReconcileLockOrderMatchesPerVKPath(t *testing.T) {
	var pg *RDBConfigStore
	for _, ns := range highFixStores(t) {
		if ns.name == "postgres" {
			pg = ns.store
		}
	}
	if pg == nil {
		t.Skip("Postgres not available, skipping lock-order test")
	}
	ctx := context.Background()
	db := pg.DB()
	resetReconcileTables(t, db)
	t.Cleanup(func() { resetReconcileTables(t, db) })

	createTestMCPClient(t, db, "mcp-granted", false)
	granted := createTestMCPClient(t, db, "mcp-granted-2", false)
	createTestMCPClient(t, db, "mcp-revoked", false)
	now := time.Now().UTC()
	createTestVK(t, db, "vk-lock", now)
	require.NoError(t, db.Create(&tables.TableVirtualKeyMCPConfig{VirtualKeyID: "vk-lock", MCPClientID: granted.ID}).Error)
	vk := "vk-lock"
	// The orphaned row sorts first by id, so a (virtual_key_id, id) ordered lock
	// reaches it before the active row.
	for _, row := range []struct{ id, client, status string }{
		{"tok-a-orphaned", "mcp-granted-2", "orphaned"},
		{"tok-b-active", "mcp-revoked", "active"},
	} {
		require.NoError(t, db.Create(&tables.TableMCPOauthToken{ID: row.id, AuthMode: "vk", MCPClientID: row.client, VirtualKeyID: &vk,
			Status: row.status, AccessToken: "at", TokenType: "Bearer", CreatedAt: now, UpdatedAt: now}).Error)
	}

	perVK := db.Begin()
	require.NoError(t, perVK.Error)
	defer perVK.Rollback()
	// The per-VK path's first step locks the active row.
	require.NoError(t, perVK.Exec("UPDATE mcp_oauth_tokens SET status = status WHERE id = ?", "tok-b-active").Error)

	bulkDone := make(chan error, 1)
	go func() { bulkDone <- pg.ReconcileOauthAfterMCPChange(ctx, "mcp-granted-2") }()

	require.Eventually(t, func() bool {
		var waiting int64
		require.NoError(t, db.Raw(`SELECT COUNT(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock' AND query ILIKE '%mcp_oauth_tokens%'`).Scan(&waiting).Error)
		return waiting > 0
	}, 10*time.Second, 50*time.Millisecond, "the bulk reconcile should block on the row the per-VK transaction holds")

	require.NoError(t, reconcileVKDirectTokensDB(perVK, "vk-lock"), "per-VK reconcile must not be the deadlock victim")
	require.NoError(t, perVK.Commit().Error)
	select {
	case err := <-bulkDone:
		require.NoError(t, err, "bulk reconcile must not be the deadlock victim")
	case <-time.After(15 * time.Second):
		t.Fatal("bulk reconcile did not finish")
	}

	var statuses []string
	require.NoError(t, db.Model(&tables.TableMCPOauthToken{}).Order("id").Pluck("status", &statuses).Error)
	require.Equal(t, []string{"active", "orphaned"}, statuses, "the orphaned grant comes back and the revoked one is orphaned")
}
