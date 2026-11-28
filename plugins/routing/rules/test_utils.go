package rules

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/maximhq/bifrost/plugins/governance"
	"gorm.io/gorm"
)

// MockLogger is a no-op logger that records what it was asked to log.
type MockLogger struct {
	mu       sync.Mutex
	logs     []string
	errors   []string
	debugs   []string
	infos    []string
	warnings []string
}

func NewMockLogger() *MockLogger {
	return &MockLogger{
		logs:     make([]string, 0),
		errors:   make([]string, 0),
		debugs:   make([]string, 0),
		infos:    make([]string, 0),
		warnings: make([]string, 0),
	}
}

func (ml *MockLogger) SetLevel(level schemas.LogLevel) {}

func (ml *MockLogger) SetOutputType(outputType schemas.LoggerOutputType) {}

func (ml *MockLogger) Error(format string, args ...interface{}) {
	ml.mu.Lock()
	defer ml.mu.Unlock()
	ml.errors = append(ml.errors, format)
}

func (ml *MockLogger) Warn(format string, args ...interface{}) {
	ml.mu.Lock()
	defer ml.mu.Unlock()
	ml.warnings = append(ml.warnings, format)
}

func (ml *MockLogger) Info(format string, args ...interface{}) {
	ml.mu.Lock()
	defer ml.mu.Unlock()
	ml.infos = append(ml.infos, format)
}

func (ml *MockLogger) Debug(format string, args ...interface{}) {
	ml.mu.Lock()
	defer ml.mu.Unlock()
	ml.debugs = append(ml.debugs, format)
}

func (ml *MockLogger) Fatal(format string, args ...interface{}) {
	ml.mu.Lock()
	defer ml.mu.Unlock()
	ml.errors = append(ml.errors, format)
}

func (ml *MockLogger) LogHTTPRequest(level schemas.LogLevel, msg string) schemas.LogEventBuilder {
	ml.mu.Lock()
	defer ml.mu.Unlock()
	ml.logs = append(ml.logs, msg)
	return schemas.NoopLogEvent
}

// MockGovernanceStore serves canned virtual keys and usage status in place of the real
// governance store.
type MockGovernanceStore struct {
	VirtualKeys map[string]*configstoreTables.TableVirtualKey
	// Status is returned for every provider/model pair. A nil Status stands for a request
	// with no budgets or rate limits configured.
	Status *governance.BudgetAndRateLimitStatus
}

func NewMockGovernanceStore() *MockGovernanceStore {
	return &MockGovernanceStore{VirtualKeys: map[string]*configstoreTables.TableVirtualKey{}}
}

func (m *MockGovernanceStore) GetBudgetAndRateLimitStatus(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string, budgetBaselines map[string]float64, tokenBaselines map[string]int64, requestBaselines map[string]int64) *governance.BudgetAndRateLimitStatus {
	return m.Status
}

// TestRules holds routing rules for tests the way a gateway does: rows in a SQLite config store,
// brought into a live store by a refresh after each write, and served by a Store. Close releases
// it.
type TestRules struct {
	*Store
	live        *livestore.Store
	configStore configstore.ConfigStore
}

// NewTestRules opens a SQLite config store in dir, loads a live store from it and serves its
// routing rules, none at first.
func NewTestRules(ctx context.Context, dir string, logger schemas.Logger) (*TestRules, error) {
	cs, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: filepath.Join(dir, "config.db")},
	}, logger)
	if err != nil {
		return nil, err
	}
	live, err := livestore.New()
	if err != nil {
		cs.Close(ctx)
		return nil, err
	}
	if err := live.LoadAll(ctx, cs.DB()); err != nil {
		live.Close()
		cs.Close(ctx)
		return nil, err
	}
	store, err := NewStore(live, logger)
	if err != nil {
		live.Close()
		cs.Close(ctx)
		return nil, err
	}
	return &TestRules{Store: store, live: live, configStore: cs}, nil
}

// Put writes rule's row and its targets, replacing any earlier version, and refreshes it, as a save
// does. It writes the rows directly, so it skips the checks the config store applies to API
// writes.
func (r *TestRules) Put(ctx context.Context, rule *configstoreTables.TableRoutingRule) error {
	if rule == nil {
		return fmt.Errorf("routing rule cannot be nil")
	}
	row := *rule
	row.Targets = nil
	err := r.configStore.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("rule_id = ?", rule.ID).Delete(&configstoreTables.TableRoutingTarget{}).Error; err != nil {
			return err
		}
		if err := tx.Omit("Targets").Save(&row).Error; err != nil {
			return err
		}
		for _, target := range rule.Targets {
			// Written as a column map, so a zero the test set (a weight of 0, say) is stored as given
			// rather than replaced by the column's default.
			if err := tx.Model(&configstoreTables.TableRoutingTarget{}).Create(map[string]any{
				"rule_id":         rule.ID,
				"provider":        target.Provider,
				"model":           target.Model,
				"key_id":          target.KeyID,
				"weight":          target.Weight,
				"ttft_timeout_ms": target.TTFTTimeoutMs,
			}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return r.live.Refresh(ctx, []livestore.Ref{{Kind: livestore.KindRoutingRule, ID: rule.ID}})
}

// Delete removes rule id's row and its record, as a delete does.
func (r *TestRules) Delete(ctx context.Context, id string) error {
	err := r.configStore.DB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("rule_id = ?", id).Delete(&configstoreTables.TableRoutingTarget{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&configstoreTables.TableRoutingRule{}).Error
	})
	if err != nil {
		return err
	}
	return r.live.Refresh(ctx, []livestore.Ref{{Kind: livestore.KindRoutingRule, ID: id, Remove: true}})
}

// SaveModelRouter saves router, its targets, its fallbacks and its routing through the config
// store, creating it or replacing what was saved, and refreshes it, as a save does. A router saved without routing
// keeps the routing it had, or the default a new router gets.
func (r *TestRules) SaveModelRouter(ctx context.Context, router *configstore.ModelRouter) error {
	saved := *router
	_, err := r.configStore.GetModelRouter(ctx, router.ID)
	switch {
	case errors.Is(err, configstore.ErrNotFound):
		err = r.configStore.CreateModelRouter(ctx, &saved)
	case err == nil:
		err = r.configStore.UpdateModelRouter(ctx, &saved)
		if err == nil && router.Routing != nil {
			err = r.configStore.SetModelRouterRouting(ctx, router.ID, *router.Routing)
		}
	}
	if err != nil {
		return err
	}
	if err := r.configStore.SetModelRouterTargets(ctx, router.ID, router.Targets); err != nil {
		return err
	}
	if err := r.configStore.SetModelRouterFallbacks(ctx, router.ID, router.Fallbacks); err != nil {
		return err
	}
	return r.live.Refresh(ctx, []livestore.Ref{{Kind: livestore.KindModelRouter, ID: router.ID}})
}

// SaveModelRouterTargets replaces a saved router's targets and refreshes it, as saving its targets
// does.
func (r *TestRules) SaveModelRouterTargets(ctx context.Context, id string, targets []configstoreTables.TableModelRouterTarget) error {
	if err := r.configStore.SetModelRouterTargets(ctx, id, targets); err != nil {
		return err
	}
	return r.live.Refresh(ctx, []livestore.Ref{{Kind: livestore.KindModelRouter, ID: id}})
}

// ConfigStore returns the config store the rules and model routers are saved in.
func (r *TestRules) ConfigStore() configstore.ConfigStore {
	return r.configStore
}

// Live returns the live store the rules and model routers are served from.
func (r *TestRules) Live() *livestore.Store {
	return r.live
}

// Close releases the rule store, its live store and its config store.
func (r *TestRules) Close() {
	r.Store.Close()
	r.live.Close()
	r.configStore.Close(context.Background())
}

// newTestRuleStore builds an empty rule store over its own config store in a temporary directory
// of t, and closes it when t ends, so no test leaves its database handle or live store open; tests
// add rules through Put.
func newTestRuleStore(t testing.TB) (*TestRules, error) {
	t.Helper()
	rules, err := NewTestRules(context.Background(), t.TempDir(), NewMockLogger())
	if err != nil {
		return nil, err
	}
	t.Cleanup(rules.Close)
	return rules, nil
}

// newTestEngine builds a rule store and an engine over it, returning both so tests can add
// rules and then evaluate them. The rule store is closed when t ends.
func newTestEngine(t testing.TB, chainMaxDepth int) (*TestRules, *Engine, error) {
	t.Helper()
	rules, err := newTestRuleStore(t)
	if err != nil {
		return nil, nil, err
	}
	engine, err := NewEngine(rules.Store, NewMockGovernanceStore(), NewMockLogger(), &chainMaxDepth)
	if err != nil {
		return nil, nil, err
	}
	return rules, engine, nil
}
