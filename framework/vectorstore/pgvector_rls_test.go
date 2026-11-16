package vectorstore

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The row-level security tests need two things the plain tests do not:
//   - a role that is NOT a superuser and has no BYPASSRLS, because PostgreSQL skips every policy
//     for those, even on a table with FORCE ROW LEVEL SECURITY. The test database's own user is a
//     superuser, so the tests create the roles below with it.
//   - a second role that does not own the tables, to show a runtime role needs no DDL rights.
const (
	rlsTestOwnerRole      = "bifrost_rls_app"
	rlsTestOwnerPassword  = "rls_app_pw"
	rlsTestRuntimeRole    = "bifrost_rls_runtime"
	rlsTestRuntimePasswd  = "rls_runtime_pw"
	rlsTestTenantA        = "scope-tenant-a"
	rlsTestTenantB        = "scope-tenant-b"
	rlsTestSharedDimesion = 4
)

// ensureRLSTestRoles creates the ordinary (non-bypass) roles through the superuser store.
func ensureRLSTestRoles(t testing.TB, admin *PgVectorTestSetup) {
	t.Helper()
	for _, role := range []struct{ name, password string }{
		{rlsTestOwnerRole, rlsTestOwnerPassword},
		{rlsTestRuntimeRole, rlsTestRuntimePasswd},
	} {
		var exists bool
		require.NoError(t, admin.Store.pool.QueryRow(admin.ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, role.name).Scan(&exists))
		if !exists {
			_, err := admin.Store.pool.Exec(admin.ctx, fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD '%s' NOSUPERUSER NOBYPASSRLS NOCREATEDB NOCREATEROLE`, role.name, role.password))
			if err != nil {
				// Another test process may have created it a moment ago.
				require.NoError(t, admin.Store.pool.QueryRow(admin.ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, role.name).Scan(&exists))
				require.True(t, exists, "create role: %v", err)
			}
		}
	}
	_, err := admin.Store.pool.Exec(admin.ctx, fmt.Sprintf(`GRANT USAGE, CREATE ON SCHEMA public TO %s`, rlsTestOwnerRole))
	require.NoError(t, err)
	_, err = admin.Store.pool.Exec(admin.ctx, fmt.Sprintf(`GRANT USAGE ON SCHEMA public TO %s`, rlsTestRuntimeRole))
	require.NoError(t, err)
}

func pgConfigForRole(user, password string, rls bool) PgVectorConfig {
	return PgVectorConfig{
		Host:             *schemas.NewSecretVar(getEnvWithDefault("PGVECTOR_HOST", PgVectorTestDefaultHost)),
		Port:             *schemas.NewSecretVar(getEnvWithDefault("PGVECTOR_PORT", PgVectorTestDefaultPort)),
		User:             *schemas.NewSecretVar(user),
		Password:         *schemas.NewSecretVar(password),
		Database:         *schemas.NewSecretVar(getEnvWithDefault("PGVECTOR_DATABASE", "bifrost")),
		SSLMode:          *schemas.NewSecretVar(getEnvWithDefault("PGVECTOR_SSLMODE", "disable")),
		RowLevelSecurity: rls,
	}
}

func openRoleStore(t testing.TB, ctx context.Context, config PgVectorConfig) *PgVectorStore {
	t.Helper()
	store, err := newPgVectorStore(ctx, &config, bifrost.NewDefaultLogger(schemas.LogLevelError))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close(context.Background(), "") })
	return store
}

// rlsFixture is a namespace owned by the ordinary owner role with row-level security on.
type rlsFixture struct {
	admin     *PgVectorTestSetup
	store     *PgVectorStore // ordinary owner role, row_level_security on
	namespace string
	table     string
	ctx       context.Context
}

func newRLSFixture(t testing.TB) *rlsFixture {
	t.Helper()
	admin := NewPgVectorTestSetup(t)
	ensureRLSTestRoles(t, admin)
	store := openRoleStore(t, admin.ctx, pgConfigForRole(rlsTestOwnerRole, rlsTestOwnerPassword, true))
	namespace := "bifrost_rls_" + strings.ReplaceAll(generateUUID(), "-", "")[:12]
	require.NoError(t, store.CreateNamespace(admin.ctx, namespace, rlsTestSharedDimesion, nil))
	t.Cleanup(func() { _ = store.DeleteNamespace(context.Background(), namespace) })
	table, err := store.table(namespace)
	require.NoError(t, err)
	return &rlsFixture{admin: admin, store: store, namespace: namespace, table: table, ctx: admin.ctx}
}

func (f *rlsFixture) as(scope string) context.Context { return WithTenantScope(f.ctx, scope) }

func rlsMetadata(scope string, extra map[string]interface{}) map[string]interface{} {
	metadata := map[string]interface{}{"tenant_scope": scope, "cache_key": "shared-key", "from_bifrost_semantic_cache_plugin": true}
	for k, v := range extra {
		metadata[k] = v
	}
	return metadata
}

// seed stores one row per tenant with the very same embedding.
func (f *rlsFixture) seed(t testing.TB) []float32 {
	t.Helper()
	embedding := []float32{0.1, 0.2, 0.3, 0.4}
	require.NoError(t, f.store.Add(f.as(rlsTestTenantA), f.namespace, "row-a", embedding, rlsMetadata(rlsTestTenantA, map[string]interface{}{"response": "secret of A"})))
	require.NoError(t, f.store.Add(f.as(rlsTestTenantB), f.namespace, "row-b", embedding, rlsMetadata(rlsTestTenantB, map[string]interface{}{"response": "secret of B"})))
	return embedding
}

func TestPgVectorRLS_TenantsCannotSeeEachOther(t *testing.T) {
	f := newRLSFixture(t)
	embedding := f.seed(t)
	sameKey := []Query{{Field: "cache_key", Operator: QueryOperatorEqual, Value: "shared-key"}}

	assert.True(t, f.store.EnforcesTenantScope())
	assert.True(t, EnforcesTenantScope(f.store))

	// Each tenant finds exactly its own row, although both rows are a perfect match.
	for scope, want := range map[string]string{rlsTestTenantA: "row-a", rlsTestTenantB: "row-b"} {
		results, err := f.store.GetNearest(f.as(scope), f.namespace, embedding, sameKey, nil, 0.5, 10)
		require.NoError(t, err)
		require.Len(t, results, 1, scope)
		assert.Equal(t, want, results[0].ID)

		all, _, err := f.store.GetAll(f.as(scope), f.namespace, nil, nil, nil, 10)
		require.NoError(t, err)
		assert.Equal(t, []string{want}, resultIDs(all))

		chunks, err := f.store.GetChunks(f.as(scope), f.namespace, []string{"row-a", "row-b"})
		require.NoError(t, err)
		assert.Equal(t, []string{want}, resultIDs(chunks))
	}

	// Another tenant's row by id is simply not found.
	_, err := f.store.GetChunk(f.as(rlsTestTenantB), f.namespace, "row-a")
	assert.ErrorIs(t, err, ErrNotFound)
	chunk, err := f.store.GetChunk(f.as(rlsTestTenantA), f.namespace, "row-a")
	require.NoError(t, err)
	assert.Equal(t, "secret of A", chunk.Properties["response"])

	// No tenant scope at all: deny everything, on every read.
	for name, ctx := range map[string]context.Context{"background": f.ctx, "empty scope": WithTenantScope(f.ctx, "")} {
		results, err := f.store.GetNearest(ctx, f.namespace, embedding, sameKey, nil, 0.5, 10)
		require.NoError(t, err, name)
		assert.Empty(t, results, name)
		all, _, err := f.store.GetAll(ctx, f.namespace, nil, nil, nil, 10)
		require.NoError(t, err, name)
		assert.Empty(t, all, name)
		_, err = f.store.GetChunk(ctx, f.namespace, "row-a")
		assert.ErrorIs(t, err, ErrNotFound, name)
	}

	// A tenant scope that was never used sees nothing either.
	results, err := f.store.GetNearest(f.as("scope-never-seen"), f.namespace, embedding, nil, nil, 0.5, 10)
	require.NoError(t, err)
	assert.Empty(t, results)
}

// A hand-written SELECT through the application's own role is held to the same rule. This is the
// "someone ran a query without the plugin's filters" case, and the owner is subject to the policy
// because the table is FORCEd.
func TestPgVectorRLS_HandWrittenSQLThroughAppRoleSeesNothing(t *testing.T) {
	f := newRLSFixture(t)
	f.seed(t)

	var count int
	require.NoError(t, f.store.pool.QueryRow(f.ctx, fmt.Sprintf(`SELECT count(*) FROM %s`, f.table)).Scan(&count))
	assert.Equal(t, 0, count, "a plain SELECT with no tenant setting sees no rows")

	var forced, enabled bool
	require.NoError(t, f.store.pool.QueryRow(f.ctx, `SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = to_regclass($1)`, f.table).Scan(&enabled, &forced))
	assert.True(t, enabled)
	assert.True(t, forced, "FORCE makes the table owner obey the policy too")

	// The same SELECT with a tenant's setting sees only that tenant's rows.
	for scope, want := range map[string]int{rlsTestTenantA: 1, rlsTestTenantB: 1, "scope-other": 0} {
		tx, err := f.store.pool.Begin(f.ctx)
		require.NoError(t, err)
		_, err = tx.Exec(f.ctx, `SELECT set_config('app.tenant_scope', $1, true)`, scope)
		require.NoError(t, err)
		require.NoError(t, tx.QueryRow(f.ctx, fmt.Sprintf(`SELECT count(*) FROM %s`, f.table)).Scan(&count))
		require.NoError(t, tx.Rollback(f.ctx))
		assert.Equal(t, want, count, scope)
	}

	// The setting does not leak from one pooled connection to the next: a store call with another
	// scope on the same connection starts from scratch, and a bare query afterwards sees nothing.
	_, err := f.store.GetChunk(f.as(rlsTestTenantA), f.namespace, "row-a")
	require.NoError(t, err)
	require.NoError(t, f.store.pool.QueryRow(f.ctx, fmt.Sprintf(`SELECT count(*) FROM %s`, f.table)).Scan(&count))
	assert.Equal(t, 0, count)

	// An admin-less caller cannot bypass the policy by setting only the scope they do not own.
	assert.Equal(t, 0, f.countAs(t, "app.tenant_scope", "scope-other"))
}

func (f *rlsFixture) countAs(t testing.TB, setting, value string) int {
	t.Helper()
	tx, err := f.store.pool.Begin(f.ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(f.ctx) }()
	_, err = tx.Exec(f.ctx, `SELECT set_config($1, $2, true)`, setting, value)
	require.NoError(t, err)
	var count int
	require.NoError(t, tx.QueryRow(f.ctx, fmt.Sprintf(`SELECT count(*) FROM %s`, f.table)).Scan(&count))
	return count
}

// ATTACK: the plugin's own filter is deliberately bypassed. The store is called directly as
// tenant B, with a filter that would match tenant A's row, an embedding identical to A's, and a
// forged tenant_scope. Only the database stands between B and A's data.
func TestPgVectorRLS_AttackBypassingThePluginFilter(t *testing.T) {
	f := newRLSFixture(t)
	embedding := f.seed(t)
	asB := f.as(rlsTestTenantB)

	aRowFilters := [][]Query{
		{{Field: "tenant_scope", Operator: QueryOperatorEqual, Value: rlsTestTenantA}},
		{{Field: "cache_key", Operator: QueryOperatorEqual, Value: "shared-key"}, {Field: "tenant_scope", Operator: QueryOperatorEqual, Value: rlsTestTenantA}},
		{{Field: "tenant_scope", Operator: QueryOperatorNotEqual, Value: rlsTestTenantB}},
		{{Field: "response", Operator: QueryOperatorLike, Value: "secret of A"}},
	}
	for i, filters := range aRowFilters {
		results, err := f.store.GetNearest(asB, f.namespace, embedding, filters, nil, -1, 10)
		require.NoError(t, err)
		assert.Empty(t, results, "filter set %d must not reach A's row", i)
		all, _, err := f.store.GetAll(asB, f.namespace, filters, nil, nil, 10)
		require.NoError(t, err)
		assert.Empty(t, all, "GetAll filter set %d", i)
	}

	// B cannot write a row that claims A's scope, nor overwrite A's row by id.
	err := f.store.Add(asB, f.namespace, "forged", embedding, rlsMetadata(rlsTestTenantA, nil))
	require.Error(t, err, "a row stamped for another tenant is rejected by the policy")
	assert.Contains(t, err.Error(), "row-level security")
	err = f.store.Add(asB, f.namespace, "row-a", embedding, rlsMetadata(rlsTestTenantB, map[string]interface{}{"response": "B overwrote A"}))
	require.Error(t, err, "overwriting A's row must fail")

	// B cannot delete A's row, by id or with a catch-all filter.
	require.NoError(t, f.store.Delete(asB, f.namespace, "row-a"))
	deleted, err := f.store.DeleteAll(asB, f.namespace, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{"row-b"}, resultIDs(toSearchResults(deleted)), "an unfiltered DeleteAll only reaches the caller's own rows")
	deleted, err = f.store.DeleteAll(f.ctx, f.namespace, nil)
	require.NoError(t, err)
	assert.Empty(t, deleted, "with no tenant scope a DeleteAll removes nothing")

	// Through the admin marker (the deliberate cross-tenant path) A's row is still intact and unchanged.
	chunk, err := f.store.GetChunk(WithTenantAdmin(f.ctx), f.namespace, "row-a")
	require.NoError(t, err)
	assert.Equal(t, "secret of A", chunk.Properties["response"])
}

func toSearchResults(deleted []DeleteResult) []SearchResult {
	out := make([]SearchResult, 0, len(deleted))
	for _, d := range deleted {
		out = append(out, SearchResult{ID: d.ID})
	}
	return out
}

func TestPgVectorRLS_AdminMarkerCrossesTenantsOnPurpose(t *testing.T) {
	f := newRLSFixture(t)
	embedding := f.seed(t)
	admin := WithTenantAdmin(f.ctx)

	results, err := f.store.GetNearest(admin, f.namespace, embedding, nil, nil, 0.5, 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"row-a", "row-b"}, resultIDs(results))

	// DeleteAll by key, the way ClearCacheForKey does it.
	deleted, err := f.store.DeleteAll(admin, f.namespace, []Query{{Field: "cache_key", Operator: QueryOperatorEqual, Value: "shared-key"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"row-a", "row-b"}, resultIDs(toSearchResults(deleted)))
}

func TestPgVectorRLS_NamespaceLifecycleAndPingStillWork(t *testing.T) {
	f := newRLSFixture(t)
	f.seed(t)

	require.NoError(t, f.store.Ping(f.ctx))
	require.NoError(t, f.store.CreateNamespace(f.ctx, f.namespace, rlsTestSharedDimesion, nil), "creating again is a no-op")
	namespaces, err := f.store.ListNamespaces(f.ctx, "bifrost_rls_")
	require.NoError(t, err)
	assert.Contains(t, namespaces, f.namespace)

	var policies int
	require.NoError(t, f.admin.Store.pool.QueryRow(f.ctx, `SELECT count(*) FROM pg_policy WHERE polrelid = to_regclass($1)`, f.table).Scan(&policies))
	assert.Equal(t, 1, policies, "re-creating does not pile up policies")

	// The ownership marker still protects tables Bifrost did not create.
	foreign := "foreign_rls_" + strings.ReplaceAll(generateUUID(), "-", "")[:12]
	_, err = f.store.pool.Exec(f.ctx, fmt.Sprintf(`CREATE TABLE %s (id int)`, foreign))
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = f.store.pool.Exec(context.Background(), `DROP TABLE IF EXISTS `+foreign) })
	err = f.store.CreateNamespace(f.ctx, foreign, rlsTestSharedDimesion, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not created by Bifrost")
	require.Error(t, f.store.DeleteNamespace(f.ctx, foreign))

	// Deleting the namespace works for the owner even though no tenant scope is set.
	require.NoError(t, f.store.DeleteNamespace(f.ctx, f.namespace))
	var exists bool
	require.NoError(t, f.store.pool.QueryRow(f.ctx, `SELECT to_regclass($1) IS NOT NULL`, f.table).Scan(&exists))
	assert.False(t, exists)
}

// A runtime role that owns nothing and has only data rights can start against tables an owner
// prepared with row_level_security, and is held to the policy.
func TestPgVectorRLS_NonOwnerRuntimeRole(t *testing.T) {
	f := newRLSFixture(t)
	embedding := f.seed(t)
	_, err := f.store.pool.Exec(f.ctx, fmt.Sprintf(`GRANT SELECT, INSERT, UPDATE, DELETE ON %s TO %s`, f.table, rlsTestRuntimeRole))
	require.NoError(t, err)

	runtime := openRoleStore(t, f.ctx, pgConfigForRole(rlsTestRuntimeRole, rlsTestRuntimePasswd, true))
	require.NoError(t, runtime.CreateNamespace(f.ctx, f.namespace, rlsTestSharedDimesion, nil), "an already protected table needs no DDL")

	results, err := runtime.GetNearest(f.as(rlsTestTenantA), f.namespace, embedding, nil, nil, 0.5, 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"row-a"}, resultIDs(results))
	results, err = runtime.GetNearest(f.ctx, f.namespace, embedding, nil, nil, 0.5, 10)
	require.NoError(t, err)
	assert.Empty(t, results)

	// The expiry sweep works for it too (it cannot build the expiry index, which only costs speed).
	require.NoError(t, f.store.Add(f.as(rlsTestTenantA), f.namespace, "row-a-old", embedding, rlsMetadata(rlsTestTenantA, map[string]interface{}{"expires_at": time.Now().Unix() - 60})))
	swept, err := runtime.DeleteExpired(WithTenantAdmin(f.ctx), f.namespace, time.Now(), 10)
	require.NoError(t, err)
	assert.Equal(t, 1, swept)

	// It cannot switch the protection off.
	_, err = runtime.pool.Exec(f.ctx, fmt.Sprintf(`ALTER TABLE %s DISABLE ROW LEVEL SECURITY`, f.table))
	require.Error(t, err, "only the owner can change the policy")

	// And a runtime role asked to protect a table that is not yet protected fails clearly.
	plain := "bifrost_rls_plain_" + strings.ReplaceAll(generateUUID(), "-", "")[:8]
	require.NoError(t, f.admin.Store.CreateNamespace(f.ctx, plain, rlsTestSharedDimesion, nil))
	t.Cleanup(func() { _ = f.admin.Store.DeleteNamespace(context.Background(), plain) })
	plainTable, _ := f.admin.Store.table(plain)
	_, err = f.admin.Store.pool.Exec(f.ctx, fmt.Sprintf(`GRANT SELECT, INSERT ON %s TO %s`, plainTable, rlsTestRuntimeRole))
	require.NoError(t, err)
	err = runtime.CreateNamespace(f.ctx, plain, rlsTestSharedDimesion, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "row-level security")
}

func TestPgVectorRLS_RefusesARoleThatBypassesIt(t *testing.T) {
	admin := NewPgVectorTestSetup(t) // the test database's user is a superuser
	config := pgConfigForRole(getEnvWithDefault("PGVECTOR_USER", "bifrost"), getEnvWithDefault("PGVECTOR_PASSWORD", "bifrost_password"), true)
	_, err := newPgVectorStore(admin.ctx, &config, bifrost.NewDefaultLogger(schemas.LogLevelError))
	require.Error(t, err, "a superuser would silently skip the policy")
	assert.Contains(t, err.Error(), "BYPASSRLS")
	assert.NotContains(t, err.Error(), config.Password.GetValue())
}

// With row_level_security off (the default) nothing changes: no policy is created, a tenant scope
// in the context is ignored, and the old unprotected behaviour works as before.
func TestPgVectorRLS_OffByDefaultKeepsTheOldBehaviour(t *testing.T) {
	ts := NewPgVectorTestSetup(t)
	namespace := ts.newNamespace(t, rlsTestSharedDimesion)
	table := mustTable(t, ts.Store, namespace)
	assert.False(t, ts.Store.EnforcesTenantScope())
	assert.False(t, EnforcesTenantScope(ts.Store))

	embedding := []float32{0.1, 0.2, 0.3, 0.4}
	require.NoError(t, ts.Store.Add(WithTenantScope(ts.ctx, "a"), namespace, "x", embedding, map[string]interface{}{"tenant_scope": "a"}))
	require.NoError(t, ts.Store.Add(ts.ctx, namespace, "y", embedding, map[string]interface{}{}))

	results, err := ts.Store.GetNearest(WithTenantScope(ts.ctx, "other"), namespace, embedding, nil, nil, 0.5, 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"x", "y"}, resultIDs(results), "the scope is ignored when row_level_security is off")

	var enabled bool
	var policies int
	require.NoError(t, ts.Store.pool.QueryRow(ts.ctx, `SELECT relrowsecurity FROM pg_class WHERE oid = to_regclass($1)`, table).Scan(&enabled))
	require.NoError(t, ts.Store.pool.QueryRow(ts.ctx, `SELECT count(*) FROM pg_policy WHERE polrelid = to_regclass($1)`, table).Scan(&policies))
	assert.False(t, enabled)
	assert.Zero(t, policies)
}

// Turning the option on for a table that already holds data adds the policy to that table, and the
// rows written before have no tenant_scope, so they are hidden from every tenant (deny, not allow).
func TestPgVectorRLS_EnablingOnAnExistingTableHidesUnstampedRows(t *testing.T) {
	admin := NewPgVectorTestSetup(t)
	ensureRLSTestRoles(t, admin)
	namespace := "bifrost_rls_up_" + strings.ReplaceAll(generateUUID(), "-", "")[:10]
	plainOwner := openRoleStore(t, admin.ctx, pgConfigForRole(rlsTestOwnerRole, rlsTestOwnerPassword, false))
	require.NoError(t, plainOwner.CreateNamespace(admin.ctx, namespace, rlsTestSharedDimesion, nil))
	t.Cleanup(func() { _ = plainOwner.DeleteNamespace(context.Background(), namespace) })
	embedding := []float32{0.1, 0.2, 0.3, 0.4}
	require.NoError(t, plainOwner.Add(admin.ctx, namespace, "old-row", embedding, map[string]interface{}{"cache_key": "k"}))

	protected := openRoleStore(t, admin.ctx, pgConfigForRole(rlsTestOwnerRole, rlsTestOwnerPassword, true))
	require.NoError(t, protected.CreateNamespace(admin.ctx, namespace, rlsTestSharedDimesion, nil))
	results, err := protected.GetNearest(WithTenantScope(admin.ctx, rlsTestTenantA), namespace, embedding, nil, nil, 0.5, 10)
	require.NoError(t, err)
	assert.Empty(t, results, "a row without tenant_scope is visible to no tenant")
	results, err = protected.GetNearest(WithTenantAdmin(admin.ctx), namespace, embedding, nil, nil, 0.5, 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"old-row"}, resultIDs(results), "only the admin marker reaches it")
}

// ---------------------------------------------------------------------------
// Expired entries
// ---------------------------------------------------------------------------

func insertExpiryRows(t testing.TB, ts *PgVectorTestSetup, namespace string) {
	t.Helper()
	embedding := []float32{0.1, 0.2, 0.3, 0.4}
	now := time.Now().Unix()
	for i := 0; i < 10; i++ {
		meta := map[string]interface{}{"expires_at": now - int64(100+i)}
		require.NoError(t, ts.Store.Add(ts.ctx, namespace, fmt.Sprintf("expired-%02d", i), embedding, meta))
	}
	live := map[string]interface{}{"expires_at": now + 3600}
	noExpiry := map[string]interface{}{"note": "never expires"}
	textExpiry := map[string]interface{}{"expires_at": "not-a-number"}
	for id, meta := range map[string]map[string]interface{}{"live": live, "no-expiry": noExpiry, "text-expiry": textExpiry} {
		require.NoError(t, ts.Store.Add(ts.ctx, namespace, id, embedding, meta))
	}
}

func TestPgVectorStore_DeleteExpiredIsBatchedAndSparesLiveRows(t *testing.T) {
	ts := NewPgVectorTestSetup(t)
	namespace := ts.newNamespace(t, rlsTestSharedDimesion)
	insertExpiryRows(t, ts, namespace)

	var remover ExpiredEntryRemover = ts.Store
	_, err := remover.DeleteExpired(ts.ctx, namespace, time.Now(), 0)
	require.Error(t, err, "a limit is required")

	// The batch limit is respected: 10 expired rows, 4 per call.
	var removed []int
	for {
		n, err := remover.DeleteExpired(ts.ctx, namespace, time.Now(), 4)
		require.NoError(t, err)
		removed = append(removed, n)
		if n == 0 {
			break
		}
		require.LessOrEqual(t, n, 4)
	}
	assert.Equal(t, []int{4, 4, 2, 0}, removed)

	all, _, err := ts.Store.GetAll(ts.ctx, namespace, nil, nil, nil, 100)
	require.NoError(t, err)
	assert.Equal(t, []string{"live", "no-expiry", "text-expiry"}, resultIDs(all), "live rows, rows without expires_at and rows with a non-numeric one stay")

	// The sweep created its index.
	var indexDef string
	require.NoError(t, ts.Store.pool.QueryRow(ts.ctx, `SELECT indexdef FROM pg_indexes WHERE tablename = $1 AND indexname = $2`, pgvectorTableName(namespace), pgvectorTableName(namespace)+"_expires_idx").Scan(&indexDef))
	assert.Contains(t, indexDef, "expires_at")

	// A cutoff in the future takes the live row too.
	n, err := remover.DeleteExpired(ts.ctx, namespace, time.Now().Add(2*time.Hour), 100)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
}

// The same cleanup as plain SQL, for people who would rather schedule it with pg_cron than run it
// from Bifrost. This is the exact statement given in the docs.
func TestPgVectorStore_DocumentedPgCronStatementWorks(t *testing.T) {
	ts := NewPgVectorTestSetup(t)
	namespace := ts.newNamespace(t, rlsTestSharedDimesion)
	table := mustTable(t, ts.Store, namespace)
	insertExpiryRows(t, ts, namespace)

	statement := fmt.Sprintf(`DELETE FROM %[1]s WHERE id IN (
  SELECT id FROM %[1]s
  WHERE (CASE WHEN jsonb_typeof(metadata -> 'expires_at') = 'number' THEN (metadata ->> 'expires_at')::float8 END) < extract(epoch FROM now())
  LIMIT 5000 FOR UPDATE SKIP LOCKED)`, table)
	tag, err := ts.Store.pool.Exec(ts.ctx, statement)
	require.NoError(t, err)
	assert.Equal(t, int64(10), tag.RowsAffected())
	all, _, err := ts.Store.GetAll(ts.ctx, namespace, nil, nil, nil, 100)
	require.NoError(t, err)
	assert.Equal(t, []string{"live", "no-expiry", "text-expiry"}, resultIDs(all))
}

func TestPgVectorRLS_DeleteExpiredAcrossTenants(t *testing.T) {
	f := newRLSFixture(t)
	embedding := []float32{0.1, 0.2, 0.3, 0.4}
	past := time.Now().Unix() - 60
	future := time.Now().Unix() + 3600
	for _, scope := range []string{rlsTestTenantA, rlsTestTenantB} {
		require.NoError(t, f.store.Add(f.as(scope), f.namespace, "old-"+scope, embedding, map[string]interface{}{"tenant_scope": scope, "expires_at": past}))
		require.NoError(t, f.store.Add(f.as(scope), f.namespace, "new-"+scope, embedding, map[string]interface{}{"tenant_scope": scope, "expires_at": future}))
	}

	n, err := f.store.DeleteExpired(f.ctx, f.namespace, time.Now(), 100)
	require.NoError(t, err)
	assert.Zero(t, n, "without the admin marker the sweep sees no rows")
	n, err = f.store.DeleteExpired(f.as(rlsTestTenantA), f.namespace, time.Now(), 100)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "a tenant sweeps only its own expired rows")
	n, err = f.store.DeleteExpired(WithTenantAdmin(f.ctx), f.namespace, time.Now(), 100)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "the admin sweep takes the rest")

	all, _, err := f.store.GetAll(WithTenantAdmin(f.ctx), f.namespace, nil, nil, nil, 100)
	require.NoError(t, err)
	assert.Equal(t, []string{"new-" + rlsTestTenantA, "new-" + rlsTestTenantB}, resultIDs(all))
}

// ---------------------------------------------------------------------------
// Measurements (opt in: BIFROST_PGVECTOR_MEASURE=1 go test -run Measure -v)
// ---------------------------------------------------------------------------

// countingConn counts the writes a connection makes; pgx sends one write per flush, so the number
// of writes per call is the number of client-to-server round trips.
type countingConn struct {
	net.Conn
	writes *atomic.Int64
}

func (c countingConn) Write(b []byte) (int, error) {
	c.writes.Add(1)
	return c.Conn.Write(b)
}

func TestPgVectorRLS_SearchStaysOneRoundTrip(t *testing.T) {
	f := newRLSFixture(t)
	embedding := f.seed(t)

	var writes atomic.Int64
	config, err := pgxpool.ParseConfig(fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", rlsTestOwnerRole, rlsTestOwnerPassword,
		getEnvWithDefault("PGVECTOR_HOST", PgVectorTestDefaultHost), getEnvWithDefault("PGVECTOR_PORT", PgVectorTestDefaultPort), getEnvWithDefault("PGVECTOR_DATABASE", "bifrost")))
	require.NoError(t, err)
	config.MaxConns = 1
	config.AfterConnect = registerPgVectorType
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	config.ConnConfig.DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := dialer.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return countingConn{Conn: conn, writes: &writes}, nil
	}
	pool, err := pgxpool.NewWithConfig(f.ctx, config)
	require.NoError(t, err)
	defer pool.Close()
	counted := &PgVectorStore{pool: pool, schema: f.store.schema, logger: f.store.logger, timeout: f.store.timeout, binaryVectors: true,
		vectorType: pgvectorTypeVector, rowLevelSecurity: true, iterativeScan: true}

	// Warm up so statement preparation is not counted.
	for i := 0; i < 3; i++ {
		_, err = counted.GetNearest(f.as(rlsTestTenantA), f.namespace, embedding, nil, nil, 0.5, 1)
		require.NoError(t, err)
	}
	before := writes.Load()
	results, err := counted.GetNearest(f.as(rlsTestTenantA), f.namespace, embedding, nil, nil, 0.5, 1)
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, int64(1), writes.Load()-before, "tenant setting and search leave in one write: a single round trip")

	counted.rowLevelSecurity = false
	before = writes.Load()
	_, err = counted.GetNearest(f.as(rlsTestTenantA), f.namespace, embedding, nil, nil, 0.5, 1)
	require.NoError(t, err)
	assert.Equal(t, int64(1), writes.Load()-before, "same as the search without RLS")
}

func TestPgVectorRLS_MeasureOverhead(t *testing.T) {
	if getEnvWithDefault("BIFROST_PGVECTOR_MEASURE", "") == "" {
		t.Skip("set BIFROST_PGVECTOR_MEASURE=1 to run the measurements")
	}
	admin := NewPgVectorTestSetup(t)
	ensureRLSTestRoles(t, admin)
	const dimension, rows, tenants, queries = 384, 2000, 20, 400

	build := func(rls bool) (*PgVectorStore, string, map[string][]float32) {
		store := openRoleStore(t, admin.ctx, pgConfigForRole(rlsTestOwnerRole, rlsTestOwnerPassword, rls))
		namespace := fmt.Sprintf("bifrost_rls_m_%t_%s", rls, strings.ReplaceAll(generateUUID(), "-", "")[:8])
		require.NoError(t, store.CreateNamespace(admin.ctx, namespace, dimension, nil))
		t.Cleanup(func() { _ = store.DeleteNamespace(context.Background(), namespace) })
		probes := map[string][]float32{}
		for i := 0; i < rows; i++ {
			scope := fmt.Sprintf("tenant-%02d", i%tenants)
			vector := generateTestEmbedding(dimension)
			if _, ok := probes[scope]; !ok {
				probes[scope] = vector
			}
			require.NoError(t, store.Add(WithTenantScope(admin.ctx, scope), namespace, generateUUID(), vector, map[string]interface{}{
				"tenant_scope": scope, "cache_key": scope, "params_hash": "f00dfeed", "from_bifrost_semantic_cache_plugin": true,
				"response": "cached response body", "expires_at": time.Now().Add(time.Hour).Unix(),
			}))
		}
		_, err := store.pool.Exec(admin.ctx, fmt.Sprintf(`ANALYZE %s`, mustTable(t, store, namespace)))
		require.NoError(t, err)
		return store, namespace, probes
	}
	measure := func(store *PgVectorStore, namespace string, probes map[string][]float32, withFilters bool) (median, p95 time.Duration) {
		durations := make([]time.Duration, 0, queries)
		for i := 0; i < queries+20; i++ {
			scope := fmt.Sprintf("tenant-%02d", i%tenants)
			filters := []Query{{Field: "cache_key", Operator: QueryOperatorEqual, Value: scope}, {Field: "params_hash", Operator: QueryOperatorEqual, Value: "f00dfeed"}, {Field: "from_bifrost_semantic_cache_plugin", Operator: QueryOperatorEqual, Value: true}}
			if !withFilters {
				filters = nil
			}
			start := time.Now()
			results, err := store.GetNearest(WithTenantScope(admin.ctx, scope), namespace, probes[scope], filters, []string{"response", "expires_at"}, 0.8, 1)
			elapsed := time.Since(start)
			require.NoError(t, err)
			require.Len(t, results, 1)
			if i >= 20 { // the first 20 are warm-up
				durations = append(durations, elapsed)
			}
		}
		sortDurations(durations)
		return durations[len(durations)/2], durations[len(durations)*95/100]
	}

	plainStore, plainNS, plainProbes := build(false)
	rlsStore, rlsNS, rlsProbes := build(true)
	for _, withFilters := range []bool{true, false} {
		pm, pp := measure(plainStore, plainNS, plainProbes, withFilters)
		rm, rp := measure(rlsStore, rlsNS, rlsProbes, withFilters)
		t.Logf("MEASURE rows=%d dim=%d tenants=%d plugin_filters=%t: without RLS median=%v p95=%v | with RLS median=%v p95=%v | median overhead=%v (%.1f%%)",
			rows, dimension, tenants, withFilters, pm, pp, rm, rp, rm-pm, 100*float64(rm-pm)/float64(pm))
	}

	// The explicit-transaction alternative (BEGIN, set_config, query, COMMIT) for comparison.
	var txDurations []time.Duration
	for i := 0; i < queries; i++ {
		scope := fmt.Sprintf("tenant-%02d", i%tenants)
		start := time.Now()
		tx, err := rlsStore.pool.Begin(admin.ctx)
		require.NoError(t, err)
		_, err = tx.Exec(admin.ctx, `SELECT set_config('app.tenant_scope', $1, true)`, scope)
		require.NoError(t, err)
		var id string
		table := mustTable(t, rlsStore, rlsNS)
		require.NoError(t, tx.QueryRow(admin.ctx, fmt.Sprintf(`SELECT id FROM %s WHERE embedding IS NOT NULL ORDER BY embedding <=> $1::vector LIMIT 1`, table), rlsProbes[scope]).Scan(&id))
		require.NoError(t, tx.Commit(admin.ctx))
		txDurations = append(txDurations, time.Since(start))
	}
	sortDurations(txDurations)
	t.Logf("MEASURE explicit transaction (4 round trips) median=%v p95=%v", txDurations[len(txDurations)/2], txDurations[len(txDurations)*95/100])

}

func sortDurations(d []time.Duration) {
	for i := 1; i < len(d); i++ {
		for j := i; j > 0 && d[j] < d[j-1]; j-- {
			d[j], d[j-1] = d[j-1], d[j]
		}
	}
}
