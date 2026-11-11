package tables

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// TableRateLimit defines rate limiting rules for virtual keys using flexible max+reset approach
type TableRateLimit struct {
	ID string `gorm:"primaryKey;type:varchar(255)" json:"id"`

	// Token limits with flexible duration
	TokenMaxLimit      *int64    `gorm:"default:null" json:"token_max_limit,omitempty"`          // Maximum tokens allowed
	TokenResetDuration *string   `gorm:"type:varchar(50)" json:"token_reset_duration,omitempty"` // e.g., "30s", "5m", "1h", "1d", "1w", "1M", "1Y"
	TokenCurrentUsage  int64     `gorm:"default:0" json:"token_current_usage"`                   // Current token usage
	TokenLastReset     time.Time `gorm:"index" json:"token_last_reset"`                          // Last time token counter was reset

	// Request limits with flexible duration
	RequestMaxLimit      *int64    `gorm:"default:null" json:"request_max_limit,omitempty"`          // Maximum requests allowed
	RequestResetDuration *string   `gorm:"type:varchar(50)" json:"request_reset_duration,omitempty"` // e.g., "30s", "5m", "1h", "1d", "1w", "1M", "1Y"
	RequestCurrentUsage  int64     `gorm:"default:0" json:"request_current_usage"`                   // Current request usage
	RequestLastReset     time.Time `gorm:"index" json:"request_last_reset"`                          // Last time request counter was reset

	Scope LimitScope `gorm:"type:varchar(20);not null;default:'shared'" json:"scope"` // Traffic counted: shared, model or mcp

	// Owner FKs: set only on a request limit, which belongs to exactly one ModelConfig, Team or Customer
	ModelConfigID *string `gorm:"type:varchar(255);index" json:"model_config_id,omitempty"`
	TeamID        *string `gorm:"type:varchar(255);index" json:"team_id,omitempty"`
	CustomerID    *string `gorm:"type:varchar(255);index" json:"customer_id,omitempty"`

	// Deprecated: set calendar_aligned on the parent access profile / VK / team
	// instead. Kept for backward compatibility with older config.json files;
	// the OSS applyV1Compat path and the enterprise access-profile reconciler
	// promote any true value here to the owner's top-level CalendarAligned at
	// load time.
	CalendarAlignedInput *bool `gorm:"-" json:"calendar_aligned,omitempty"`

	// Derived from the owning entity. See TableBudget.IsCalendarAligned.
	IsCalendarAligned bool `gorm:"-" json:"-"`

	// Config hash is used to detect the changes synced from config.json file
	// Every time we sync the config.json file, we will update the config hash
	ConfigHash string `gorm:"type:varchar(255);null" json:"config_hash"`

	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`
}

// TableName sets the table name for each model
func (TableRateLimit) TableName() string { return "governance_rate_limits" }

// IsRequestLimit reports whether the row is a request limit: a request-only limit
// owned by a model config, team or customer through an owner FK, as opposed to
// the single rate limit a holder references through its rate_limit_id.
func (rl *TableRateLimit) IsRequestLimit() bool {
	return rl.ModelConfigID != nil || rl.TeamID != nil || rl.CustomerID != nil
}

// RequestPeriodKey is the identity of a request limit within its owner: its reset duration and scope.
func (rl *TableRateLimit) RequestPeriodKey() string {
	duration := ""
	if rl.RequestResetDuration != nil {
		duration = *rl.RequestResetDuration
	}
	return LimitPeriodKey(duration, rl.Scope)
}

// LegacyRequestLimitID is the ID of the request limit split out of the combined
// rate limit rateLimitID. Deterministic so the migration and the config.json
// loader produce identical rows.
func LegacyRequestLimitID(rateLimitID string) string {
	return rateLimitID + "-requests"
}

// BeforeSave hook for RateLimit to validate reset duration formats
func (rl *TableRateLimit) BeforeSave(tx *gorm.DB) error {
	if !rl.Scope.IsValid() {
		return fmt.Errorf("invalid rate limit scope: %s (must be one of shared, model, mcp)", rl.Scope)
	}
	rl.Scope = rl.Scope.Normalize()
	if rl.IsRequestLimit() {
		owners := 0
		for _, owner := range []*string{rl.ModelConfigID, rl.TeamID, rl.CustomerID} {
			if owner != nil {
				owners++
			}
		}
		if owners > 1 {
			return fmt.Errorf("request limit cannot have more than one owner (model config/team/customer)")
		}
		if rl.RequestMaxLimit == nil || rl.RequestResetDuration == nil {
			return fmt.Errorf("request limit requires request_max_limit and request_reset_duration")
		}
		if rl.TokenMaxLimit != nil || rl.TokenResetDuration != nil {
			return fmt.Errorf("request limit cannot carry a token limit")
		}
	} else if rl.Scope != LimitScopeShared && (rl.RequestMaxLimit == nil || rl.RequestResetDuration == nil || rl.TokenMaxLimit != nil || rl.TokenResetDuration != nil) {
		return fmt.Errorf("rate limit scope %s is only valid on a request-only limit (request_max_limit and request_reset_duration set, no token limit)", rl.Scope)
	}

	// Validate token reset duration if provided
	if rl.TokenResetDuration != nil {
		if d, err := ParseDuration(*rl.TokenResetDuration); err != nil {
			return fmt.Errorf("invalid token reset duration format: %s", *rl.TokenResetDuration)
		} else if d <= 0 {
			return fmt.Errorf("token reset duration cannot be zero or negative: %s", *rl.TokenResetDuration)
		}
	}

	// Validate request reset duration if provided
	if rl.RequestResetDuration != nil {
		if d, err := ParseDuration(*rl.RequestResetDuration); err != nil {
			return fmt.Errorf("invalid request reset duration format: %s", *rl.RequestResetDuration)
		} else if d <= 0 {
			return fmt.Errorf("request reset duration cannot be zero or negative: %s", *rl.RequestResetDuration)
		}
	}

	// Validate that if a max limit is set, a reset duration is also provided
	if rl.TokenMaxLimit != nil && rl.TokenResetDuration == nil {
		return fmt.Errorf("token_reset_duration is required when token_max_limit is set")
	}

	if rl.RequestMaxLimit != nil && rl.RequestResetDuration == nil {
		return fmt.Errorf("request_reset_duration is required when request_max_limit is set")
	}

	// Making sure token limit is greater than zero
	if rl.TokenMaxLimit != nil && *rl.TokenMaxLimit <= 0 {
		return fmt.Errorf("token_max_limit cannot be zero or negative: %d", *rl.TokenMaxLimit)
	}

	// Making sure request limit is greater than zero
	if rl.RequestMaxLimit != nil && *rl.RequestMaxLimit <= 0 {
		return fmt.Errorf("request_max_limit cannot be zero or negative: %d", *rl.RequestMaxLimit)
	}

	return nil
}

// AdoptCalendarAlignment re-anchors this rate limit's open counters onto the
// calendar grid they have just been told to follow, and reports whether either
// moved. Counterpart to TableBudget.AdoptCalendarAlignment; the reasoning for
// moving the boundary rather than resetting is documented there.
//
// The two counters are decided independently: they carry their own durations and
// their own LastReset values, so a monthly token limit can adopt the month
// boundary while an hourly request limit stays rolling in the same call.
// Usage is never cleared - the operator changed a schedule, not a quota.
func (rl *TableRateLimit) AdoptCalendarAlignment(now time.Time) bool {
	if rl == nil || !rl.IsCalendarAligned {
		return false
	}
	adopt := func(duration *string, lastReset *time.Time) bool {
		if duration == nil || !IsCalendarAlignableDuration(*duration) {
			return false
		}
		// Rate limits carry no quarter definition, so the fiscal start is not
		// applicable here; see QuarterStartNotApplicable.
		start := GetCalendarPeriodStart(*duration, now, QuarterStartNotApplicable)
		if !start.After(*lastReset) {
			return false
		}
		*lastReset = start
		return true
	}
	// Both counters are evaluated; || would short-circuit and leave the second
	// one on a stale boundary whenever the first adopted.
	tokenMoved := adopt(rl.TokenResetDuration, &rl.TokenLastReset)
	requestMoved := adopt(rl.RequestResetDuration, &rl.RequestLastReset)
	return tokenMoved || requestMoved
}
