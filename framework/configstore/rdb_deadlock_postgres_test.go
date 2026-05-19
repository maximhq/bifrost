package configstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupPostgresDeadlockStore(t *testing.T) *RDBConfigStore {
	t.Helper()

	db, err := gorm.Open(postgres.Open(postgresDSN), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Skipf("postgres not available: %v", err)
	}

	require.NoError(t, db.Exec("DROP SCHEMA public CASCADE").Error)
	require.NoError(t, db.Exec("CREATE SCHEMA public").Error)
	require.NoError(t, triggerMigrations(context.Background(), db))

	store := &RDBConfigStore{logger: bifrost.NewDefaultLogger(schemas.LogLevelInfo)}
	store.db.Store(db)
	store.migrateOnFreshFn = func(ctx context.Context, fn func(context.Context, *gorm.DB) error) error {
		return fn(ctx, store.DB())
	}
	store.refreshPoolFn = func(ctx context.Context) error { return nil }

	return store
}

// TestPostgresRoutingRuleUpdateDeleteDoesNotDeadlock verifies routing rule update/delete races do not deadlock.
func TestPostgresRoutingRuleUpdateDeleteDoesNotDeadlock(t *testing.T) {
	store := setupPostgresDeadlockStore(t)
	ctx := context.Background()
	installRoutingRuleDeadlockAmplifier(t, store.DB())

	for i := 0; i < 25; i++ {
		ruleID := fmt.Sprintf("route-deadlock-%02d", i)
		require.NoError(t, store.CreateRoutingRule(ctx, routingRuleFixture(ruleID, i, "openai")))

		start := make(chan struct{})
		errs := make(chan error, 2)
		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			<-start
			errs <- store.UpdateRoutingRule(ctx, routingRuleFixture(ruleID, i, "anthropic"))
		}()
		go func() {
			defer wg.Done()
			<-start
			errs <- store.DeleteRoutingRule(ctx, ruleID)
		}()

		close(start)
		wg.Wait()
		close(errs)

		for err := range errs {
			if isPostgresDeadlock(err) {
				t.Fatalf("routing rule update/delete deadlocked on iteration %d: %v", i, err)
			}
			if err != nil && !errors.Is(err, ErrNotFound) && !isUniqueRace(err) {
				t.Fatalf("unexpected routing rule race error on iteration %d: %v", i, err)
			}
		}
		_ = store.DeleteRoutingRule(ctx, ruleID)
	}
}

// TestPostgresProviderGraphConcurrentMutationsDoNotDeadlock verifies provider graph mutations avoid deadlocks.
func TestPostgresProviderGraphConcurrentMutationsDoNotDeadlock(t *testing.T) {
	store := setupPostgresDeadlockStore(t)
	ctx := context.Background()

	require.NoError(t, seedProviderGraph(ctx, store))

	for i := 0; i < 20; i++ {
		start := make(chan struct{})
		errs := make(chan error, 3)
		var wg sync.WaitGroup
		wg.Add(3)

		go func(iter int) {
			defer wg.Done()
			<-start
			errs <- store.UpdateProvider(ctx, "openai", ProviderConfig{Keys: []schemas.Key{
				{ID: "pg-key-a", Name: "pg-openai-key-a", Value: *schemas.NewEnvVar("sk-a"), Weight: 1.0},
				{ID: fmt.Sprintf("pg-key-new-%d", iter), Name: fmt.Sprintf("pg-openai-key-new-%d", iter), Value: *schemas.NewEnvVar("sk-new"), Weight: 1.0},
			}})
		}(i)
		go func() {
			defer wg.Done()
			<-start
			errs <- updateSeededProviderConfig(ctx, store)
		}()
		go func() {
			defer wg.Done()
			<-start
			errs <- store.DeleteProvider(ctx, "openai")
		}()

		close(start)
		wg.Wait()
		close(errs)

		for err := range errs {
			if isPostgresDeadlock(err) {
				t.Fatalf("provider graph mutation deadlocked on iteration %d: %v", i, err)
			}
			if err != nil && !errors.Is(err, ErrNotFound) && !isUniqueRace(err) && !isForeignKeyRace(err) {
				t.Fatalf("unexpected provider graph race error on iteration %d: %v", i, err)
			}
		}
		_ = seedProviderGraph(ctx, store)
	}
}

// TestPostgresVirtualKeyBudgetConcurrentMutationsDoNotDeadlock verifies VK budget edits avoid deadlocks.
func TestPostgresVirtualKeyBudgetConcurrentMutationsDoNotDeadlock(t *testing.T) {
	store := setupPostgresDeadlockStore(t)
	ctx := context.Background()
	vkID, budgetID := seedVirtualKeyBudget(ctx, t, store)

	for i := 0; i < 30; i++ {
		start := make(chan struct{})
		errs := make(chan error, 3)
		var wg sync.WaitGroup
		wg.Add(3)

		go func(iter int) {
			defer wg.Done()
			<-start
			errs <- store.UpdateBudget(ctx, &tables.TableBudget{
				ID:            budgetID,
				MaxLimit:      float64(1000 + iter),
				ResetDuration: "1d",
				LastReset:     time.Now().UTC(),
				VirtualKeyID:  &vkID,
			})
		}(i)
		go func(iter int) {
			defer wg.Done()
			<-start
			errs <- store.ExecuteTransaction(ctx, func(tx *gorm.DB) error {
				if err := store.UpdateVirtualKey(ctx, &tables.TableVirtualKey{
					ID:       vkID,
					Name:     "PG VK Budget",
					Value:    "pg-vk-budget-value",
					IsActive: schemas.Ptr(true),
				}, tx); err != nil {
					return err
				}
				return store.UpdateBudget(ctx, &tables.TableBudget{
					ID:            budgetID,
					MaxLimit:      float64(2000 + iter),
					ResetDuration: "1d",
					LastReset:     time.Now().UTC(),
					VirtualKeyID:  &vkID,
				}, tx)
			})
		}(i)
		go func(iter int) {
			defer wg.Done()
			<-start
			errs <- store.UpdateBudgetUsage(ctx, budgetID, float64(iter))
		}(i)

		close(start)
		wg.Wait()
		close(errs)

		for err := range errs {
			if isPostgresDeadlock(err) {
				t.Fatalf("virtual key budget mutation deadlocked on iteration %d: %v", i, err)
			}
			if err != nil && !errors.Is(err, ErrNotFound) && !isUniqueRace(err) && !isForeignKeyRace(err) {
				t.Fatalf("unexpected virtual key budget race error on iteration %d: %v", i, err)
			}
		}
	}
}

// installRoutingRuleDeadlockAmplifier widens the old routing-rule lock inversion window.
func installRoutingRuleDeadlockAmplifier(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.Callback().Update().After("gorm:update").Register("bifrost:test_sleep_after_routing_rule_update", func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == "routing_rules" {
			_ = tx.Session(&gorm.Session{NewDB: true}).Exec("SELECT pg_sleep(0.05)").Error
		}
	}))
	require.NoError(t, db.Callback().Delete().After("gorm:delete").Register("bifrost:test_sleep_after_routing_target_delete", func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == "routing_targets" {
			_ = tx.Session(&gorm.Session{NewDB: true}).Exec("SELECT pg_sleep(0.05)").Error
		}
	}))
}

// routingRuleFixture builds a routing rule with one deterministic target.
func routingRuleFixture(id string, priority int, provider string) *tables.TableRoutingRule {
	enabled := true
	model := "gpt-test"
	return &tables.TableRoutingRule{
		ID:            id,
		Name:          id,
		Enabled:       &enabled,
		CelExpression: "true",
		Scope:         "global",
		Priority:      priority,
		Targets: []tables.TableRoutingTarget{
			{Provider: &provider, Model: &model, Weight: 1.0},
		},
	}
}

// seedProviderGraph creates the shared provider/key/virtual-key graph used by deadlock tests.
func seedProviderGraph(ctx context.Context, store *RDBConfigStore) error {
	_ = store.DeleteProvider(ctx, "openai")
	_ = store.DeleteVirtualKey(ctx, "pg-vk")

	if err := store.UpdateProvidersConfig(ctx, map[schemas.ModelProvider]ProviderConfig{
		"openai": {
			Keys: []schemas.Key{
				{ID: "pg-key-a", Name: "pg-openai-key-a", Value: *schemas.NewEnvVar("sk-a"), Weight: 1.0},
				{ID: "pg-key-b", Name: "pg-openai-key-b", Value: *schemas.NewEnvVar("sk-b"), Weight: 1.0},
			},
		},
	}); err != nil {
		return err
	}
	if err := store.CreateVirtualKey(ctx, &tables.TableVirtualKey{
		ID:       "pg-vk",
		Name:     "PG VK",
		Value:    fmt.Sprintf("pg-vk-value-%d", time.Now().UnixNano()),
		IsActive: schemas.Ptr(true),
	}); err != nil && !isUniqueRace(err) {
		return err
	}
	weight := 1.0
	return store.CreateVirtualKeyProviderConfig(ctx, &tables.TableVirtualKeyProviderConfig{
		VirtualKeyID: "pg-vk",
		Provider:     "openai",
		Weight:       &weight,
		Keys: []tables.TableKey{
			{Name: "pg-openai-key-b"},
		},
	})
}

// seedVirtualKeyBudget creates one virtual key with one owned budget for deadlock tests.
func seedVirtualKeyBudget(ctx context.Context, t *testing.T, store *RDBConfigStore) (string, string) {
	t.Helper()
	vkID := "pg-vk-budget"
	budgetID := "pg-vk-budget-1d"
	require.NoError(t, store.CreateVirtualKey(ctx, &tables.TableVirtualKey{
		ID:       vkID,
		Name:     "PG VK Budget",
		Value:    "pg-vk-budget-value",
		IsActive: schemas.Ptr(true),
	}))
	require.NoError(t, store.CreateBudget(ctx, &tables.TableBudget{
		ID:            budgetID,
		MaxLimit:      1000,
		ResetDuration: "1d",
		LastReset:     time.Now().UTC(),
		VirtualKeyID:  &vkID,
	}))
	return vkID, budgetID
}

// updateSeededProviderConfig updates the seeded virtual-key provider config association.
func updateSeededProviderConfig(ctx context.Context, store *RDBConfigStore) error {
	var pc tables.TableVirtualKeyProviderConfig
	if err := store.DB().WithContext(ctx).Where("virtual_key_id = ? AND provider = ?", "pg-vk", "openai").First(&pc).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ErrNotFound
		}
		return err
	}
	weight := 0.5
	pc.Weight = &weight
	pc.Keys = []tables.TableKey{{Name: "pg-openai-key-a"}}
	return store.UpdateVirtualKeyProviderConfig(ctx, &pc)
}

// isPostgresDeadlock reports whether an error is PostgreSQL deadlock SQLSTATE 40P01.
func isPostgresDeadlock(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "SQLSTATE 40P01") || strings.Contains(strings.ToLower(msg), "deadlock detected")
}

// isUniqueRace reports whether an error is an expected unique-conflict race outcome.
func isUniqueRace(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate key") || strings.Contains(msg, "unique constraint")
}

// isForeignKeyRace reports whether an error is an expected foreign-key race outcome.
func isForeignKeyRace(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "foreign key") || strings.Contains(msg, "violates foreign key constraint")
}
