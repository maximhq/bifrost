package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func scopeSQL(t *testing.T, scope queryscope.QueryScope) string {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{DryRun: true})
	require.NoError(t, err)
	stmt := scope(db.Table("logs")).Find(&[]map[string]any{}).Statement
	return stmt.SQL.String()
}

// A team-associated key also carries its parent customer; it must be narrowed to its own team,
// not widened to every row the customer owns.
func TestStampQueryScope_TeamWinsOverCustomer(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyGovernanceTeamID, "team-1")
	ctx.SetValue(schemas.BifrostContextKeyGovernanceCustomerID, "cust-1")

	stampQueryScope(ctx)

	scope := queryscope.FromContext(ctx)
	require.NotNil(t, scope)
	sql := scopeSQL(t, scope)
	require.Contains(t, sql, "team_id = ?")
	require.NotContains(t, sql, "customer_id")
}

func TestStampQueryScope_CustomerOnly(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyGovernanceCustomerID, "cust-1")

	stampQueryScope(ctx)

	scope := queryscope.FromContext(ctx)
	require.NotNil(t, scope)
	require.Contains(t, scopeSQL(t, scope), "customer_id = ?")
}

func TestStampQueryScope_TeamOnly(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyGovernanceTeamID, "team-1")

	stampQueryScope(ctx)

	scope := queryscope.FromContext(ctx)
	require.NotNil(t, scope)
	require.Contains(t, scopeSQL(t, scope), "team_id = ?")
}

func TestStampQueryScope_NoIdentityStaysUnscoped(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})

	stampQueryScope(ctx)

	require.Nil(t, queryscope.FromContext(ctx))
}

// An enterprise data-access-control plugin that has already set the scope is authoritative:
// the transport's derivation must step aside rather than overwrite it.
func TestStampQueryScope_DefersToExistingScope(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyGovernanceCustomerID, "cust-1")
	enterprise := queryscope.QueryScope(func(db *gorm.DB) *gorm.DB { return db.Where("tenant = ?", "dac") })
	ctx.SetValue(schemas.BifrostContextKeyQueryScope, enterprise)

	stampQueryScope(ctx)

	require.Contains(t, scopeSQL(t, queryscope.FromContext(ctx)), "tenant = ?")
}
