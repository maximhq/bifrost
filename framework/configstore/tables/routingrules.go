package tables

import (
	"bytes"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm"
)

// TableRoutingRule represents a routing rule in the database
type TableRoutingRule struct {
	ID            string `gorm:"primaryKey;type:varchar(255)" json:"id"`
	ConfigHash    string `gorm:"type:varchar(255)" json:"config_hash"` // Hash of config.json version, used for change detection
	Name          string `gorm:"type:varchar(255);not null;uniqueIndex:idx_routing_rule_scope_name" json:"name"`
	Description   string `gorm:"type:text" json:"description"`
	Enabled       *bool  `gorm:"not null;default:true" json:"enabled,omitempty"` // nil = DB default (true); use EnabledValue() to read
	CelExpression string `gorm:"type:text;not null" json:"cel_expression"`

	// Routing Targets (output) — 1:many relationship; weights must sum to 1
	Targets []TableRoutingTarget `gorm:"foreignKey:RuleID;constraint:OnDelete:CASCADE" json:"targets"`

	Fallbacks       *string           `gorm:"type:text" json:"-"`           // JSON array of fallback chains
	ParsedFallbacks []RoutingFallback `gorm:"-" json:"fallbacks,omitempty"` // Parsed fallbacks from JSON

	Query       *string        `gorm:"type:text" json:"-"`
	ParsedQuery map[string]any `gorm:"-" json:"query,omitempty"`

	// Scope: where this rule applies
	Scope   string  `gorm:"type:varchar(50);not null;uniqueIndex:idx_routing_rule_scope_name" json:"scope"` // "global" | "team" | "customer" | "virtual_key" | "user"
	ScopeID *string `gorm:"type:varchar(255);uniqueIndex:idx_routing_rule_scope_name" json:"scope_id"`      // nil for global, otherwise entity ID

	// Chaining
	ChainRule bool `gorm:"not null;default:false" json:"chain_rule"` // If true, re-evaluates routing chain after this rule matches

	// TTFTTimeoutMs is the time-to-first-token deadline for streaming requests
	// this rule routes. An attempt with no first token in time is cut off and
	// the next fallback runs; the last attempt is never cut off. nil = off.
	TTFTTimeoutMs *int `gorm:"column:ttft_timeout_ms" json:"ttft_timeout_ms,omitempty"`

	// Execution
	Priority int `gorm:"type:int;not null;default:0;index" json:"priority"` // Lower = evaluated first within scope

	// Timestamps
	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`
}

// TableName for TableRoutingRule
func (TableRoutingRule) TableName() string { return "routing_rules" }

// EnabledValue returns the effective Enabled bool, treating nil as true (DB default).
func (r *TableRoutingRule) EnabledValue() bool {
	if r == nil {
		return false
	}
	if r.Enabled == nil {
		return true
	}
	return *r.Enabled
}

// BeforeSave hook for TableRoutingRule to serialize JSON fields
func (r *TableRoutingRule) BeforeSave(tx *gorm.DB) error {
	if len(r.ParsedFallbacks) > 0 {
		data, err := sonic.Marshal(r.ParsedFallbacks)
		if err != nil {
			return err
		}
		r.Fallbacks = bifrost.Ptr(string(data))
	} else {
		r.Fallbacks = nil
	}
	if r.ParsedQuery != nil {
		data, err := sonic.Marshal(r.ParsedQuery)
		if err != nil {
			return err
		}
		r.Query = bifrost.Ptr(string(data))
	} else {
		r.Query = nil
	}
	return nil
}

// AfterFind hook for TableRoutingRule to deserialize JSON fields
func (r *TableRoutingRule) AfterFind(tx *gorm.DB) error {
	if r.Fallbacks != nil && strings.TrimSpace(*r.Fallbacks) != "" {
		if err := sonic.Unmarshal([]byte(*r.Fallbacks), &r.ParsedFallbacks); err != nil {
			return err
		}
	}
	if r.Query != nil && strings.TrimSpace(*r.Query) != "" {
		if err := sonic.Unmarshal([]byte(*r.Query), &r.ParsedQuery); err != nil {
			return err
		}
	}
	return nil
}

// RoutingFallback is one entry in a routing rule's fallback chain, decoded from either the legacy "provider/model" string or an object that pins a provider key.
type RoutingFallback struct {
	schemas.Fallback // provider, model and key_id; the same fields core routes on

	ProviderKeyName *string `json:"provider_key_name,omitempty"` // config-only alias; resolved to key_id during load

	raw string // verbatim legacy string, replayed by MarshalJSON so unpinned entries round-trip byte-identically
}

// IsKeyPinned reports whether this fallback names a specific provider key.
func (f RoutingFallback) IsKeyPinned() bool {
	return strings.TrimSpace(f.KeyID) != "" || (f.ProviderKeyName != nil && strings.TrimSpace(*f.ProviderKeyName) != "")
}

// String renders the legacy "provider/model" form.
func (f RoutingFallback) String() string {
	if f.raw != "" {
		return f.raw
	}
	if f.Provider == "" {
		return f.Model
	}
	return string(f.Provider) + "/" + f.Model
}

// MarshalJSON emits the legacy string unless a key is pinned, so unpinned rules keep their config hash.
func (f RoutingFallback) MarshalJSON() ([]byte, error) {
	if !f.IsKeyPinned() {
		return sonic.Marshal(f.String())
	}
	type alias RoutingFallback
	return sonic.Marshal(alias(f))
}

// UnmarshalJSON accepts the legacy "provider/model" string and the object form.
func (f *RoutingFallback) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var raw string
		if err := sonic.Unmarshal(trimmed, &raw); err != nil {
			return err
		}
		provider, model := schemas.ParseModelString(raw, "")
		*f = RoutingFallback{Fallback: schemas.Fallback{Provider: provider, Model: model}, raw: raw}
		return nil
	}
	type alias RoutingFallback
	var decoded alias
	if err := sonic.Unmarshal(trimmed, &decoded); err != nil {
		return err
	}
	*f = RoutingFallback(decoded)
	// Trim now: an unpinned object is persisted as the legacy string, where padding would become an unknown provider prefix after a restart.
	f.Provider = schemas.ModelProvider(strings.TrimSpace(string(f.Provider)))
	f.Model = strings.TrimSpace(f.Model)
	f.KeyID = strings.TrimSpace(f.KeyID)
	return nil
}

// Resolved returns the fallback to route on. A legacy "provider/model" string is re-parsed on each
// call, because rules are decoded at boot before custom providers are registered (#7538).
func (f RoutingFallback) Resolved() schemas.Fallback {
	if f.raw == "" {
		return f.Fallback
	}
	provider, model := schemas.ParseModelString(f.raw, "")
	return schemas.Fallback{Provider: provider, Model: model, KeyID: f.KeyID}
}

// RoutingFallbackStrings renders a fallback slice in its legacy string form, for logs.
func RoutingFallbackStrings(fallbacks []RoutingFallback) []string {
	out := make([]string, 0, len(fallbacks))
	for _, fb := range fallbacks {
		out = append(out, fb.String())
	}
	return out
}

// TableRoutingTarget represents a weighted routing target for probabilistic routing.
// Multiple targets can be associated with a single routing rule; weights determine
// the probability of each target being selected and must sum to 1 across all targets in a rule.
// The composite (RuleID, Provider, Model, KeyID) is unique to prevent duplicate target configs.
type TableRoutingTarget struct {
	RuleID          string  `gorm:"type:varchar(255);not null;index;uniqueIndex:idx_routing_target_config" json:"-"`
	Provider        *string `gorm:"type:varchar(255);uniqueIndex:idx_routing_target_config" json:"provider,omitempty"` // nil = use incoming provider
	Model           *string `gorm:"type:varchar(255);uniqueIndex:idx_routing_target_config" json:"model,omitempty"`    // nil = use incoming model
	KeyID           *string `gorm:"type:varchar(255);uniqueIndex:idx_routing_target_config" json:"key_id,omitempty"`   // persisted key pin
	ProviderKeyName *string `gorm:"-" json:"provider_key_name,omitempty"`                                              // config-only alias; resolved to key_id during load
	Weight          float64 `gorm:"not null;default:1" json:"weight"`                                                  // must sum to 1 across all targets in a rule
}

// TableName for TableRoutingTarget
func (TableRoutingTarget) TableName() string { return "routing_targets" }
