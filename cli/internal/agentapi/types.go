package agentapi

import "time"

// VirtualKeyListResponse is the user-scoped assigned-key response.
type VirtualKeyListResponse struct {
	VirtualKeys          []VirtualKey `json:"virtual_keys"`
	SelectedVirtualKeyID string       `json:"selected_virtual_key_id"`
	ForcedVirtualKeyID   string       `json:"forced_virtual_key_id"`
}

// VirtualKey describes a selectable key without containing its secret value.
type VirtualKey struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	Description     string   `json:"description,omitempty"`
	IsActive        bool     `json:"is_active"`
	Providers       []string `json:"providers"`
	Budgets         []Budget `json:"budgets"`
	BudgetLimit     float64  `json:"budget_limit"`
	BudgetConsumed  float64  `json:"budget_consumed"`
	BudgetAvailable float64  `json:"budget_available"`
}

// Budget describes one budget attached to an assigned virtual key.
type Budget struct {
	ID              string    `json:"id"`
	BudgetLimit     float64   `json:"budget_limit"`
	BudgetConsumed  float64   `json:"budget_consumed"`
	BudgetAvailable float64   `json:"budget_available"`
	ResetDuration   string    `json:"reset_duration"`
	LastReset       time.Time `json:"last_reset"`
}

// UsageSummaryResponse is the user-scoped budget and usage response.
type UsageSummaryResponse struct {
	Budgets     []UsageBudget    `json:"budgets"`
	RateLimits  []UsageRateLimit `json:"rate_limits"`
	Budget      UsageBudget      `json:"budget"`
	TopModels   []UsageRow       `json:"top_models"`
	TopApps     []UsageRow       `json:"top_apps"`
	GeneratedAt time.Time        `json:"generated_at"`
	Window      UsageWindow      `json:"window"`
}

// UsageLimitSource identifies the access profile and scope that owns a limit.
type UsageLimitSource struct {
	AccessProfileID   uint   `json:"access_profile_id,omitempty"`
	AccessProfileName string `json:"access_profile_name,omitempty"`
	Scope             string `json:"scope,omitempty"`
	Provider          string `json:"provider,omitempty"`
	Model             string `json:"model,omitempty"`
}

// UsageBudget describes one monetary budget visible to the current user.
type UsageBudget struct {
	UsageLimitSource
	ID            string     `json:"id,omitempty"`
	ResetDuration string     `json:"reset_duration,omitempty"`
	Used          float64    `json:"used"`
	Limit         float64    `json:"limit"`
	Available     float64    `json:"available"`
	ResetAt       *time.Time `json:"reset_at,omitempty"`
}

// UsageRateLimit keeps token and request counters independent.
type UsageRateLimit struct {
	UsageLimitSource
	ID       string            `json:"id"`
	Tokens   *UsageRateCounter `json:"tokens,omitempty"`
	Requests *UsageRateCounter `json:"requests,omitempty"`
}

// UsageRateCounter retains exact integer counts from the gateway response.
type UsageRateCounter struct {
	Used          int64      `json:"used"`
	Limit         int64      `json:"limit"`
	Available     int64      `json:"available"`
	ResetDuration string     `json:"reset_duration"`
	ResetAt       *time.Time `json:"reset_at,omitempty"`
}

// UsageRow describes one ranked model or application.
type UsageRow struct {
	Label         string  `json:"label"`
	Provider      string  `json:"provider,omitempty"`
	TotalRequests int64   `json:"total_requests"`
	TotalTokens   int64   `json:"total_tokens"`
	TotalCost     float64 `json:"total_cost"`
}

// UsageWindow describes the log interval used for usage rankings.
type UsageWindow struct {
	Start *time.Time `json:"start,omitempty"`
	End   *time.Time `json:"end,omitempty"`
}
