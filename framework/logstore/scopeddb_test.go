package logstore

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

func newScopedDBTestLogStore(t *testing.T) *RDBLogStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	return &RDBLogStore{
		db:     db,
		logger: bifrost.NewDefaultLogger(schemas.LogLevelInfo),
	}
}

func TestLogStoreScopedDB_AppliesScope(t *testing.T) {
	s := newScopedDBTestLogStore(t)
	called := false
	scope := queryscope.QueryScope(func(db *gorm.DB) *gorm.DB {
		called = true
		return db.Where("status = ?", "error")
	})
	ctx := queryscope.WithQueryScope(context.Background(), scope)

	got := s.ScopedDB(ctx)
	assert.True(t, called, "ScopedDB should invoke the scope from ctx")
	stmt := got.Session(&gorm.Session{DryRun: true}).Table("logs").Find(&struct{}{}).Statement
	assert.Contains(t, stmt.SQL.String(), "status = ?",
		"the scope's WHERE clause should be on the returned query")
}

func TestLogStoreScopedDB_PassesThroughWhenNoScope(t *testing.T) {
	s := newScopedDBTestLogStore(t)
	got := s.ScopedDB(context.Background())
	require.NotNil(t, got)
	require.NoError(t, got.Exec("SELECT 1").Error)
}

func TestLogStoreScopedDB_BindsContext(t *testing.T) {
	s := newScopedDBTestLogStore(t)
	type ctxKey struct{}
	ctx := context.WithValue(context.Background(), ctxKey{}, "marker")
	got := s.ScopedDB(ctx)
	stmt := got.Session(&gorm.Session{DryRun: true}).Find(&struct{}{}).Statement
	assert.Equal(t, "marker", stmt.Context.Value(ctxKey{}))
}

func TestLogStoreScopedDB_WrongTypeAtScopeKeyIsIgnored(t *testing.T) {
	s := newScopedDBTestLogStore(t)
	ctx := context.WithValue(context.Background(),
		schemas.BifrostContextKeyQueryScope, "not a closure")
	got := s.ScopedDB(ctx)
	require.NotNil(t, got)
	require.NoError(t, got.Exec("SELECT 1").Error)
}

func TestLogStoreScopedDB_TypedNilScopeIsIgnored(t *testing.T) {
	s := newScopedDBTestLogStore(t)
	ctx := queryscope.WithQueryScope(context.Background(), nil)
	got := s.ScopedDB(ctx)
	require.NotNil(t, got)
	require.NoError(t, got.Exec("SELECT 1").Error)
}
