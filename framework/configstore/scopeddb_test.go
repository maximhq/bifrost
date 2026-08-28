package configstore

import (
	"context"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// newScopedDBTestStore returns a bare RDBConfigStore backed by an in-
// memory SQLite database. No tables are migrated — ScopedDB only
// touches gorm DB wiring, not any specific schema.
func newScopedDBTestStore(t *testing.T) *RDBConfigStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	store := &RDBConfigStore{logger: bifrost.NewDefaultLogger(schemas.LogLevelInfo)}
	store.db.Store(db)
	return store
}

func TestScopedDB_AppliesScopeFromContext(t *testing.T) {
	store := newScopedDBTestStore(t)
	called := false
	scope := queryscope.QueryScope(func(db *gorm.DB) *gorm.DB {
		called = true
		return db.Where("1 = 0") // arbitrary mutation we can inspect
	})
	ctx := queryscope.WithQueryScope(context.Background(), scope)

	got := store.ScopedDB(ctx)

	assert.True(t, called, "ScopedDB should invoke the scope from ctx")
	// The where clause should now be present on the statement.
	stmt := got.Session(&gorm.Session{DryRun: true}).Find(&struct{}{}).Statement
	assert.Contains(t, stmt.SQL.String(), "1 = 0",
		"ScopedDB-returned *gorm.DB should carry the scope's WHERE clause")
}

func TestScopedDB_PassesThroughWhenNoScope(t *testing.T) {
	store := newScopedDBTestStore(t)
	got := store.ScopedDB(context.Background())
	assert.NotNil(t, got, "ScopedDB must always return a usable *gorm.DB")
	// Issuing a trivial query against the no-scope DB must succeed.
	require.NoError(t, got.Exec("SELECT 1").Error)
}

func TestScopedDB_BindsContext(t *testing.T) {
	store := newScopedDBTestStore(t)
	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "marker")
	got := store.ScopedDB(ctx)
	// gorm exposes the bound context on the underlying statement.
	stmt := got.Session(&gorm.Session{DryRun: true}).Find(&struct{}{}).Statement
	assert.Equal(t, "marker", stmt.Context.Value(ctxKey{}),
		"ScopedDB must bind the caller's ctx onto the returned DB")
}

func TestScopedDB_WrongTypeAtScopeKeyIsIgnored(t *testing.T) {
	store := newScopedDBTestStore(t)
	// A foreign caller stashing the wrong type at our context key must
	// not poison ScopedDB; the wrong-type value is treated as "no
	// scope present" and the query passes through.
	ctx := context.WithValue(context.Background(),
		schemas.BifrostContextKeyQueryScope, "not a closure")
	got := store.ScopedDB(ctx)
	require.NotNil(t, got)
	require.NoError(t, got.Exec("SELECT 1").Error)
}

func TestScopedDB_TypedNilScopeIsIgnored(t *testing.T) {
	store := newScopedDBTestStore(t)
	// A typed-nil QueryScope must not be invoked (would panic). The
	// queryscope.FromContext helper returns nil for a nil closure, so
	// ScopedDB falls through to the bare DB.
	ctx := queryscope.WithQueryScope(context.Background(), nil)
	got := store.ScopedDB(ctx)
	require.NotNil(t, got)
	require.NoError(t, got.Exec("SELECT 1").Error)
}

func TestScopedDB_ScopeReturningDB_IsRespected(t *testing.T) {
	store := newScopedDBTestStore(t)
	// A scope that returns the unchanged DB (a degenerate but valid
	// scope) must not break ScopedDB.
	ctx := queryscope.WithQueryScope(context.Background(), func(db *gorm.DB) *gorm.DB {
		return db
	})
	got := store.ScopedDB(ctx)
	require.NotNil(t, got)
	require.NoError(t, got.Exec("SELECT 1").Error)
}

// TestRoutingRuleReads_HonorQueryScope is the regression for the routing-rule
// read family silently ignoring the row-visibility QueryScope on ctx:
// loadRoutingRulesOrdered built its query from DB() instead of ScopedDB(), so
// GetRoutingRules / GetRoutingRule / GetRoutingRulesByScope returned every rule
// to callers whose scope should have narrowed them (the paginated variant
// already honored the scope). A ctx without a scope keeps full visibility.
func TestRoutingRuleReads_HonorQueryScope(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()
	require.NoError(t, store.CreateRoutingRule(ctx, routingRuleFixture("rule-visible", 0, "openai")))
	require.NoError(t, store.CreateRoutingRule(ctx, routingRuleFixture("rule-hidden", 1, "openai")))

	scoped := queryscope.WithQueryScope(ctx, func(db *gorm.DB) *gorm.DB {
		return db.Where("id <> ?", "rule-hidden")
	})

	rules, err := store.GetRoutingRules(scoped)
	require.NoError(t, err)
	require.Len(t, rules, 1, "GetRoutingRules must honor the ctx scope")
	assert.Equal(t, "rule-visible", rules[0].ID)

	_, err = store.GetRoutingRule(scoped, "rule-hidden")
	assert.ErrorIs(t, err, ErrNotFound, "GetRoutingRule must hide scoped-out rules")

	visible, err := store.GetRoutingRule(scoped, "rule-visible")
	require.NoError(t, err)
	assert.Equal(t, "rule-visible", visible.ID)

	byScope, err := store.GetRoutingRulesByScope(scoped, "global", "")
	require.NoError(t, err)
	require.Len(t, byScope, 1, "GetRoutingRulesByScope must honor the ctx scope")
	assert.Equal(t, "rule-visible", byScope[0].ID)

	unscoped, err := store.GetRoutingRules(ctx)
	require.NoError(t, err)
	assert.Len(t, unscoped, 2, "a ctx without a scope keeps full visibility")
}
