package configstore

import (
	"context"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
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

// promptFixture builds a minimal prompt row for the scope tests below.
func promptFixture(id, name string) *tables.TablePrompt {
	return &tables.TablePrompt{ID: id, Name: name}
}

// TestPromptChildReads_HonorQueryScope is the regression for the prompt child-read family
// ignoring the row-visibility QueryScope: GetPromptByID correctly treated a scoped-out prompt as
// absent, but every read of that prompt's children built its query from DB() and returned the
// child rows - version metadata and preloaded message contents - using only the caller-supplied
// ID. The parent was hidden and the children were not.
//
// The scope predicate is written against the prompts table, so these reads reach the parent
// through the scope and gate on that answer rather than trying to scope the child query itself.
func TestPromptChildReads_HonorQueryScope(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	require.NoError(t, store.CreatePrompt(ctx, promptFixture("prompt-visible", "Visible")))
	require.NoError(t, store.CreatePrompt(ctx, promptFixture("prompt-hidden", "Hidden")))
	require.NoError(t, store.CreatePromptVersion(ctx, &tables.TablePromptVersion{
		PromptID: "prompt-hidden", VersionNumber: 1, CommitMessage: "hidden-secret", IsLatest: true,
	}))
	require.NoError(t, store.CreatePromptVersion(ctx, &tables.TablePromptVersion{
		PromptID: "prompt-visible", VersionNumber: 1, CommitMessage: "visible", IsLatest: true,
	}))
	require.NoError(t, store.CreatePromptSession(ctx, &tables.TablePromptSession{PromptID: "prompt-hidden"}))

	hiddenVersions, err := store.GetPromptVersions(ctx, "prompt-hidden")
	require.NoError(t, err)
	require.Len(t, hiddenVersions, 1, "fixture sanity: the hidden prompt has a version to leak")
	hiddenVersionID := hiddenVersions[0].ID

	hiddenSessions, err := store.GetPromptSessions(ctx, "prompt-hidden")
	require.NoError(t, err)
	require.Len(t, hiddenSessions, 1, "fixture sanity: the hidden prompt has a session to leak")
	hiddenSessionID := hiddenSessions[0].ID

	scoped := queryscope.WithQueryScope(ctx, func(db *gorm.DB) *gorm.DB {
		return db.Where("prompts.id <> ?", "prompt-hidden")
	})

	// The negative control the reported issue relies on: the parent is already correctly hidden.
	_, err = store.GetPromptByID(scoped, "prompt-hidden")
	require.ErrorIs(t, err, ErrNotFound, "negative control: the parent must already be hidden")

	// Reads keyed by the parent ID.
	_, err = store.GetPromptVersions(scoped, "prompt-hidden")
	assert.ErrorIs(t, err, ErrNotFound, "GetPromptVersions must hide a scoped-out prompt's versions")
	_, err = store.GetLatestPromptVersion(scoped, "prompt-hidden")
	assert.ErrorIs(t, err, ErrNotFound, "GetLatestPromptVersion must hide a scoped-out prompt's version")
	_, err = store.GetPromptSessions(scoped, "prompt-hidden")
	assert.ErrorIs(t, err, ErrNotFound, "GetPromptSessions must hide a scoped-out prompt's sessions")

	// Reads keyed by the child's own ID, where the parent is only known after the row is read.
	_, err = store.GetPromptVersionByID(scoped, hiddenVersionID)
	assert.ErrorIs(t, err, ErrNotFound, "GetPromptVersionByID must hide a version whose prompt is scoped out")
	_, err = store.GetPromptSessionByID(scoped, hiddenSessionID)
	assert.ErrorIs(t, err, ErrNotFound, "GetPromptSessionByID must hide a session whose prompt is scoped out")

	// The visible prompt is unaffected: this narrows visibility, it does not break the feature.
	visibleVersions, err := store.GetPromptVersions(scoped, "prompt-visible")
	require.NoError(t, err)
	assert.Len(t, visibleVersions, 1, "an in-scope prompt's versions stay readable")

	// A ctx with no scope keeps full visibility, as everywhere else in this store.
	unscoped, err := store.GetPromptVersions(ctx, "prompt-hidden")
	require.NoError(t, err)
	assert.Len(t, unscoped, 1, "a ctx without a scope keeps full visibility")
}

// TestPromptWrites_HonorQueryScope covers the mutating half of the same defect. Cross-tenant
// modification is a worse outcome than disclosure, and deletePrompt / deletePromptVersion reach
// the store with no scoped parent lookup at all, so the gate has to live in the store.
func TestPromptWrites_HonorQueryScope(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	require.NoError(t, store.CreatePrompt(ctx, promptFixture("prompt-hidden", "Hidden")))
	require.NoError(t, store.CreatePrompt(ctx, promptFixture("prompt-visible", "Visible")))
	require.NoError(t, store.CreatePromptVersion(ctx, &tables.TablePromptVersion{
		PromptID: "prompt-hidden", VersionNumber: 1, IsLatest: true,
	}))
	require.NoError(t, store.CreatePromptSession(ctx, &tables.TablePromptSession{PromptID: "prompt-hidden", Name: "original"}))

	hiddenVersions, err := store.GetPromptVersions(ctx, "prompt-hidden")
	require.NoError(t, err)
	require.Len(t, hiddenVersions, 1)
	hiddenSessions, err := store.GetPromptSessions(ctx, "prompt-hidden")
	require.NoError(t, err)
	require.Len(t, hiddenSessions, 1)

	scoped := queryscope.WithQueryScope(ctx, func(db *gorm.DB) *gorm.DB {
		return db.Where("prompts.id <> ?", "prompt-hidden")
	})

	assert.ErrorIs(t, store.DeletePrompt(scoped, "prompt-hidden"), ErrNotFound,
		"DeletePrompt must refuse a scoped-out prompt")
	assert.ErrorIs(t, store.UpdatePrompt(scoped, promptFixture("prompt-hidden", "Renamed")), ErrNotFound,
		"UpdatePrompt must refuse a scoped-out prompt")
	assert.ErrorIs(t, store.DeletePromptVersion(scoped, hiddenVersions[0].ID), ErrNotFound,
		"DeletePromptVersion must refuse a version whose prompt is scoped out")
	assert.ErrorIs(t, store.DeletePromptSession(scoped, hiddenSessions[0].ID), ErrNotFound,
		"DeletePromptSession must refuse a session whose prompt is scoped out")
	assert.ErrorIs(t, store.RenamePromptSession(scoped, hiddenSessions[0].ID, "renamed"), ErrNotFound,
		"RenamePromptSession must refuse a session whose prompt is scoped out")

	// UpdatePromptSession takes the whole row, parent included, so the parent it must scope is
	// the one already stored under session.ID, not the one the caller supplies. Otherwise a
	// caller who can see prompt-visible pairs it with a hidden session's ID, passes the scope
	// check on the visible parent, and Save overwrites the hidden session and re-parents it.
	assert.ErrorIs(t, store.UpdatePromptSession(scoped, &tables.TablePromptSession{
		ID: hiddenSessions[0].ID, PromptID: "prompt-visible", Name: "stolen",
	}), ErrNotFound, "UpdatePromptSession must scope the stored parent, not the supplied one")
	// Even with full visibility a session cannot be moved between prompts through an update.
	assert.ErrorContains(t, store.UpdatePromptSession(ctx, &tables.TablePromptSession{
		ID: hiddenSessions[0].ID, PromptID: "prompt-visible", Name: "moved",
	}), "session does not belong to the specified prompt", "UpdatePromptSession must reject a parent change")

	// Creation is a write too: a scoped caller must not be able to attach a new version or
	// session to a prompt it cannot see, even though the handlers look the parent up first.
	assert.ErrorIs(t, store.CreatePromptVersion(scoped, &tables.TablePromptVersion{
		PromptID: "prompt-hidden", CommitMessage: "planted",
	}), ErrNotFound, "CreatePromptVersion must refuse a scoped-out parent")
	assert.ErrorIs(t, store.CreatePromptSession(scoped, &tables.TablePromptSession{
		PromptID: "prompt-hidden", Name: "planted",
	}), ErrNotFound, "CreatePromptSession must refuse a scoped-out parent")
	versionsAfter, err := store.GetPromptVersions(ctx, "prompt-hidden")
	require.NoError(t, err)
	assert.Len(t, versionsAfter, 1, "a refused version creation must not persist a row")
	sessionsAfter, err := store.GetPromptSessions(ctx, "prompt-hidden")
	require.NoError(t, err)
	assert.Len(t, sessionsAfter, 1, "a refused session creation must not persist a row")

	// Nothing was actually removed or rewritten by any of the refused calls.
	stillThere, err := store.GetPromptByID(ctx, "prompt-hidden")
	require.NoError(t, err)
	assert.Equal(t, "Hidden", stillThere.Name, "a refused write must not have taken effect")
	untouched, err := store.GetPromptSessionByID(ctx, hiddenSessions[0].ID)
	require.NoError(t, err)
	assert.Equal(t, "prompt-hidden", untouched.PromptID, "a refused update must not re-parent the session")
	assert.Equal(t, "original", untouched.Name, "a refused update must not overwrite the session")
}
