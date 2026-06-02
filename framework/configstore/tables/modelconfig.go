package tables

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

// Model config scope values. Scope determines where a model config applies.
const (
	ModelConfigScopeGlobal     = "global"
	ModelConfigScopeVirtualKey = "virtual_key"
	ModelConfigScopeUser       = "user"
)

// ModelConfigAllModels is the model_name sentinel meaning "all models". Combined with a
// specific provider it expresses provider-level governance (all models on that provider);
// with a nil provider it means all models on all providers.
const ModelConfigAllModels = "*"

// validModelConfigScopes is the runtime registry of accepted scope values.
// OSS seeds it with global + virtual_key; downstream consumers (e.g. the
// enterprise build registering "user") extend it at startup via
// RegisterModelConfigScope. Guarded by validModelConfigScopesMu.
var (
	validModelConfigScopesMu sync.RWMutex
	validModelConfigScopes   = map[string]bool{
		ModelConfigScopeGlobal:     true,
		ModelConfigScopeVirtualKey: true,
	}
)

// RegisterModelConfigScope adds scope to the allow-list consulted by
// IsValidModelConfigScope and TableModelConfig.BeforeSave. Intended to be
// called once at process startup; safe to call concurrently. Whitespace-
// only input is ignored.
func RegisterModelConfigScope(scope string) {
	s := strings.TrimSpace(scope)
	if s == "" {
		return
	}
	validModelConfigScopesMu.Lock()
	validModelConfigScopes[s] = true
	validModelConfigScopesMu.Unlock()
}

// IsValidModelConfigScope reports whether scope is a recognized model config scope.
func IsValidModelConfigScope(scope string) bool {
	validModelConfigScopesMu.RLock()
	defer validModelConfigScopesMu.RUnlock()
	return validModelConfigScopes[scope]
}

// TableModelConfig represents a model configuration with rate limiting and budgeting
type TableModelConfig struct {
	ID        string  `gorm:"primaryKey;type:varchar(255)" json:"id"`
	ModelName string  `gorm:"type:varchar(255);not null;uniqueIndex:idx_model_scope_provider,priority:3" json:"model_name"`
	Provider  *string `gorm:"type:varchar(50);uniqueIndex:idx_model_scope_provider,priority:4" json:"provider,omitempty"` // Optional provider, nullable
	// Scope determines where this config applies: "global" (default) or "virtual_key".
	Scope string `gorm:"type:varchar(50);not null;default:'global';uniqueIndex:idx_model_scope_provider,priority:1" json:"scope"`
	// ScopeID is the target of a non-global scope (e.g. the virtual key ID). NULL for global.
	ScopeID *string `gorm:"type:varchar(255);uniqueIndex:idx_model_scope_provider,priority:2" json:"scope_id,omitempty"`
	// CalendarAligned snaps this config's budget resets to calendar boundaries (e.g. a
	// monthly budget resets on the 1st) rather than rolling windows. Propagated to owned
	// budgets via AfterFind. For virtual_key-scoped configs it inherits the VK's setting.
	CalendarAligned bool    `gorm:"not null;default:false" json:"calendar_aligned"`
	BudgetID        *string `gorm:"type:varchar(255);index:idx_model_config_budget" json:"budget_id,omitempty"`
	RateLimitID     *string `gorm:"type:varchar(255);index:idx_model_config_rate_limit" json:"rate_limit_id,omitempty"`

	// ScopeName is a non-persisted, API-only field carrying the human-readable name of
	// the scope target (e.g. the virtual key's name) so the UI can render a label
	// instead of an opaque scope_id. Populated by the HTTP layer on read.
	ScopeName string `gorm:"-" json:"scope_name,omitempty"`
	// BudgetIDs is a config-file-only field listing pre-declared budget IDs (from
	// governance.budgets) to link to this model config. Not persisted; used by the
	// config sync path to set model_config_id on each referenced budget row.
	BudgetIDs []string `gorm:"-" json:"budget_ids,omitempty"`

	// Relationships
	// Budgets are owned by this model config via TableBudget.ModelConfigID (a model
	// config may carry multiple budgets with different reset windows). This is the
	// active representation. The legacy single Budget/BudgetID below is kept inert
	// for backward compatibility and is no longer read by enforcement.
	Budgets []TableBudget `gorm:"foreignKey:ModelConfigID;constraint:OnDelete:CASCADE" json:"budgets,omitempty"`
	// Legacy (inert): superseded by Budgets. Retained so existing rows/columns keep
	// parsing; not read by the governance store after the multi-budget cutover.
	Budget    *TableBudget    `gorm:"foreignKey:BudgetID;onDelete:CASCADE" json:"budget,omitempty"`
	RateLimit *TableRateLimit `gorm:"foreignKey:RateLimitID;onDelete:CASCADE" json:"rate_limit,omitempty"`

	// Config hash is used to detect the changes synced from config.json file
	// Every time we sync the config.json file, we will update the config hash
	ConfigHash string `gorm:"type:varchar(255);null" json:"config_hash"`

	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`
}

// TableName sets the table name for each model
func (TableModelConfig) TableName() string {
	return "governance_model_configs"
}

// AfterFind propagates calendar_aligned down to owned budgets so the reset path reads
// the stamped value off each budget. Mirrors TableTeam/TableVirtualKey. The governance
// store's Update*InMemory paths re-stamp on every model-config update.
func (mc *TableModelConfig) AfterFind(tx *gorm.DB) error {
	for i := range mc.Budgets {
		mc.Budgets[i].IsCalendarAligned = mc.CalendarAligned
	}
	return nil
}

// BeforeSave hook for ModelConfig to validate required fields
func (mc *TableModelConfig) BeforeSave(tx *gorm.DB) error {
	// Default and validate scope. Global is the implicit default (preserves
	// pre-scope behavior for configs created without an explicit scope).
	if strings.TrimSpace(mc.Scope) == "" {
		mc.Scope = ModelConfigScopeGlobal
	}
	if !IsValidModelConfigScope(mc.Scope) {
		return fmt.Errorf("invalid scope %q for model config", mc.Scope)
	}
	// Enforce scope_id rules: global must not have one; non-global requires it.
	if mc.Scope == ModelConfigScopeGlobal {
		mc.ScopeID = nil
	} else if mc.ScopeID == nil || strings.TrimSpace(*mc.ScopeID) == "" {
		return fmt.Errorf("scope_id is required when scope is %q", mc.Scope)
	}

	// Validate that ModelName is not empty
	if strings.TrimSpace(mc.ModelName) == "" {
		return fmt.Errorf("model_name cannot be empty")
	}

	// Validate that if BudgetID is provided, it's not an empty string
	if mc.BudgetID != nil && strings.TrimSpace(*mc.BudgetID) == "" {
		return fmt.Errorf("budget_id cannot be an empty string")
	}

	// Validate that if RateLimitID is provided, it's not an empty string
	if mc.RateLimitID != nil && strings.TrimSpace(*mc.RateLimitID) == "" {
		return fmt.Errorf("rate_limit_id cannot be an empty string")
	}

	// Validate that if Provider is provided, it's not an empty string
	if mc.Provider != nil && strings.TrimSpace(*mc.Provider) == "" {
		return fmt.Errorf("provider cannot be an empty string")
	}

	return nil
}
