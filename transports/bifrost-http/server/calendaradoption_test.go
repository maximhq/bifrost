package server

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/maximhq/bifrost/framework/livestore/registry"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/handlers"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib/libtest"
	"github.com/stretchr/testify/require"
)

// TestOwnerReloadAdoptsCalendarBeforeItsLimitsArrive checks that a write switching an owner's
// calendar alignment on has its limits' open windows moved before the refresh brings the limits in
// aligned: a subscriber that sees the aligned budget and rate limit ahead of governance already
// finds their windows on this month's boundary, so no sweep or charge in between reads them as
// overdue, and the usage is kept.
func TestOwnerReloadAdoptsCalendarBeforeItsLimitsArrive(t *testing.T) {
	handlers.SetLogger(noopTestLogger{})
	previousLogger := logger
	logger = noopTestLogger{}
	t.Cleanup(func() { logger = previousLogger })
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	opened := monthStart.Add(-10 * 24 * time.Hour)
	month, limit := "1M", int64(100)

	type owner struct {
		table  any
		create func(ctx context.Context, store configstore.ConfigStore, id string, rateLimitID *string) error
		reload func(ctx context.Context, server *BifrostHTTPServer, id string) error
		holds  func(budget *configstoreTables.TableBudget, id string)
	}
	owners := map[string]owner{
		"team": {
			table: &configstoreTables.TableTeam{},
			create: func(ctx context.Context, store configstore.ConfigStore, id string, rateLimitID *string) error {
				return store.CreateTeam(ctx, &configstoreTables.TableTeam{ID: id, Name: id, RateLimitID: rateLimitID})
			},
			reload: func(ctx context.Context, server *BifrostHTTPServer, id string) error {
				_, err := server.ReloadTeam(ctx, id)
				return err
			},
			holds: func(budget *configstoreTables.TableBudget, id string) { budget.TeamID = &id },
		},
		"customer": {
			table: &configstoreTables.TableCustomer{},
			create: func(ctx context.Context, store configstore.ConfigStore, id string, rateLimitID *string) error {
				return store.CreateCustomer(ctx, &configstoreTables.TableCustomer{ID: id, Name: id, RateLimitID: rateLimitID})
			},
			reload: func(ctx context.Context, server *BifrostHTTPServer, id string) error {
				_, err := server.ReloadCustomer(ctx, id)
				return err
			},
			holds: func(budget *configstoreTables.TableBudget, id string) { budget.CustomerID = &id },
		},
		"virtual key": {
			table: &configstoreTables.TableVirtualKey{},
			create: func(ctx context.Context, store configstore.ConfigStore, id string, rateLimitID *string) error {
				return store.CreateVirtualKey(ctx, &configstoreTables.TableVirtualKey{ID: id, Name: id, Value: *schemas.NewSecretVar("sk-bf-" + id), IsActive: schemas.Ptr(true), RateLimitID: rateLimitID})
			},
			reload: func(ctx context.Context, server *BifrostHTTPServer, id string) error {
				_, err := server.ReloadVirtualKey(ctx, id)
				return err
			},
			holds: func(budget *configstoreTables.TableBudget, id string) { budget.VirtualKeyID = &id },
		},
		"model config": {
			table: &configstoreTables.TableModelConfig{},
			create: func(ctx context.Context, store configstore.ConfigStore, id string, rateLimitID *string) error {
				return store.CreateModelConfig(ctx, &configstoreTables.TableModelConfig{ID: id, ModelName: "gpt-4o", Scope: configstoreTables.ModelConfigScopeGlobal, RateLimitID: rateLimitID})
			},
			reload: func(ctx context.Context, server *BifrostHTTPServer, id string) error {
				_, err := server.ReloadModelConfig(ctx, id)
				return err
			},
			holds: func(budget *configstoreTables.TableBudget, id string) { budget.ModelConfigID = &id },
		},
	}
	for name, o := range owners {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			config := libtest.Config(t, nil)
			config.ClientConfig = &configstore.ClientConfig{}
			// Registered before governance, so these run ahead of its own subscribers: what they
			// read is what a sweep or charge racing the refresh would read.
			var gs *governance.LocalGovernanceStore
			var budgetSeen, rateLimitSeen atomic.Pointer[time.Time]
			t.Cleanup(config.LiveStore.Collections.Budgets.Subscribe(func(ctx context.Context, changes []livestore.Change[registry.Budget]) {
				for _, c := range changes {
					if c.New != nil && c.New.IsCalendarAligned {
						if live := gs.LoadBudget(ctx, c.ID); live != nil {
							budgetSeen.Store(&live.LastReset)
						}
					}
				}
			}))
			t.Cleanup(config.LiveStore.Collections.RateLimits.Subscribe(func(ctx context.Context, changes []livestore.Change[registry.RateLimit]) {
				for _, c := range changes {
					if c.New != nil && c.New.IsCalendarAligned {
						if live := gs.LoadRateLimit(ctx, c.ID); live != nil {
							rateLimitSeen.Store(&live.TokenLastReset)
						}
					}
				}
			}))
			var err error
			gs, err = governance.NewLocalGovernanceStore(ctx, governance.NewMockLogger(), nil, config.LiveStore, nil)
			require.NoError(t, err)
			t.Cleanup(gs.Close)
			plugins := []schemas.BasePlugin{&reloadVirtualKeyPlugin{store: gs}}
			config.BasePlugins.Store(&plugins)
			server := &BifrostHTTPServer{Ctx: schemas.NewBifrostContext(ctx, schemas.NoDeadline), Config: config}

			store := config.ConfigStore
			rateLimitID := "rate"
			require.NoError(t, store.CreateRateLimit(ctx, &configstoreTables.TableRateLimit{
				ID: rateLimitID, TokenMaxLimit: &limit, TokenResetDuration: &month, TokenCurrentUsage: 17, TokenLastReset: opened,
				RequestMaxLimit: &limit, RequestResetDuration: &month, RequestCurrentUsage: 19, RequestLastReset: opened, CreatedAt: opened,
			}))
			require.NoError(t, o.create(ctx, store, "owner", &rateLimitID))
			budget := &configstoreTables.TableBudget{ID: "budget", MaxLimit: 100, CurrentUsage: 7, ResetDuration: month, CreatedAt: opened, LastReset: opened}
			o.holds(budget, "owner")
			require.NoError(t, store.CreateBudget(ctx, budget))
			require.NoError(t, o.reload(ctx, server, "owner"))
			require.Nil(t, budgetSeen.Load(), "the owner is not aligned yet")

			require.NoError(t, store.DB().Model(o.table).Where("id = ?", "owner").UpdateColumn("calendar_aligned", true).Error)
			require.NoError(t, o.reload(ctx, server, "owner"))

			require.NotNil(t, budgetSeen.Load(), "the reload brought the budget in aligned")
			require.True(t, budgetSeen.Load().Equal(monthStart), "the budget's window was moved before it arrived aligned, not %s", budgetSeen.Load())
			require.NotNil(t, rateLimitSeen.Load(), "the reload brought the rate limit in aligned")
			require.True(t, rateLimitSeen.Load().Equal(monthStart), "the rate limit's window was moved before it arrived aligned, not %s", rateLimitSeen.Load())
			require.Empty(t, gs.ResetExpiredBudgetsInMemory(ctx, "budget"))
			require.Equal(t, 7.0, gs.LoadBudget(ctx, "budget").CurrentUsage)
			require.Equal(t, int64(17), gs.LoadRateLimit(ctx, rateLimitID).TokenCurrentUsage)
		})
	}
}
