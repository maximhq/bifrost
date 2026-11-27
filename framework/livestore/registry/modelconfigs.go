package registry

import (
	"context"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/queryscope"
	"gorm.io/gorm"
)

// ModelConfig is one model config as request handling reads it: the model and provider it covers,
// the scope it belongs to, and its budgets and rate limit by ID. The keys governance looks configs
// up by depend on the model catalog, so governance builds that index itself from this collection.
type ModelConfig struct {
	ID, ModelName   string
	Provider        *string // nil covers every provider
	Scope, ScopeID  string
	RateLimitID     string
	BudgetIDs       []string
	CalendarAligned bool
	// BudgetID is the config's legacy single budget_id column. Nothing enforces it; owners of
	// budgets read it to attribute a budget to the config.
	BudgetID string
}

// ModelConfigByScope is the index of model configs by scope and scope ID, for GetBy and List.
const ModelConfigByScope = "scope"

// ModelConfigScopeKey is the scope index key of a scope and scope ID.
func ModelConfigScopeKey(scope, scopeID string) string { return scope + "\x00" + scopeID }

// ModelConfigID is a model config record's primary key.
func ModelConfigID(m *ModelConfig) string { return m.ID }

// ModelConfigScope is a model config's key in the scope index.
func ModelConfigScope(m *ModelConfig) string { return ModelConfigScopeKey(m.Scope, m.ScopeID) }

// ModelConfigKeyID is the virtual key a key-scoped model config belongs to, "" for another scope.
func ModelConfigKeyID(m *ModelConfig) string {
	if m.Scope != tables.ModelConfigScopeVirtualKey {
		return ""
	}
	return m.ScopeID
}

// ModelConfigsPage reads up to limit model configs with IDs after the cursor, in ID order, and
// returns the next cursor, "" after the last page.
func ModelConfigsPage(ctx context.Context, db *gorm.DB, after string, limit int) ([]*ModelConfig, string, error) {
	rows, err := readModelConfigs(ctx, db, func(q *gorm.DB) *gorm.DB {
		return q.Where("id > ?", after).Order("id").Limit(limit)
	})
	if err != nil || len(rows) < limit {
		return rows, "", err
	}
	return rows, rows[len(rows)-1].ID, nil
}

// ModelConfigsByID reads the model configs with these IDs; an ID with no row is left out.
func ModelConfigsByID(ctx context.Context, db *gorm.DB, ids []string) ([]*ModelConfig, error) {
	return readModelConfigs(ctx, db, func(q *gorm.DB) *gorm.DB { return q.Where(queryscope.InStrings(q, "id", ids)) })
}

// ModelConfigsByKeys reads the model configs scoped to these virtual keys.
func ModelConfigsByKeys(ctx context.Context, db *gorm.DB, keyIDs []string) ([]*ModelConfig, error) {
	return readModelConfigs(ctx, db, func(q *gorm.DB) *gorm.DB {
		cond, arg := queryscope.InStrings(q, "scope_id", keyIDs)
		return q.Where("scope = ? AND "+cond, tables.ModelConfigScopeVirtualKey, arg)
	})
}

// ModelConfigFromTable builds a model config's record from its table struct; budgetIDs are the
// budgets it holds. A gateway without a config store calls it on the configs config.json
// declares.
func ModelConfigFromTable(t *tables.TableModelConfig, budgetIDs []string) *ModelConfig {
	scope := t.Scope
	if scope == "" {
		scope = tables.ModelConfigScopeGlobal
	}
	return &ModelConfig{
		ID: t.ID, ModelName: t.ModelName, Provider: t.Provider, Scope: scope, ScopeID: deref(t.ScopeID),
		RateLimitID: deref(t.RateLimitID), BudgetIDs: budgetIDs, CalendarAligned: t.CalendarAligned,
		BudgetID: deref(t.BudgetID),
	}
}

// readModelConfigs loads the model config rows where selects, with the IDs of their budgets.
func readModelConfigs(ctx context.Context, db *gorm.DB, where func(*gorm.DB) *gorm.DB) ([]*ModelConfig, error) {
	var rows []tables.TableModelConfig
	q := db.WithContext(ctx).Select("id, model_name, provider, scope, scope_id, rate_limit_id, budget_id, calendar_aligned")
	if err := where(q).Find(&rows).Error; err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for i := range rows {
		ids = append(ids, rows[i].ID)
	}
	budgets, err := budgetIDsBy(ctx, db, "model_config_id", ids)
	if err != nil {
		return nil, err
	}
	out := make([]*ModelConfig, 0, len(rows))
	for i := range rows {
		out = append(out, ModelConfigFromTable(&rows[i], budgets[rows[i].ID]))
	}
	return out, nil
}
