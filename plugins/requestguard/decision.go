package requestguard

import (
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/maximhq/bifrost/core/schemas"
)

type CapabilityIssue struct {
	Capability string `json:"capability"`
	Severity   string `json:"severity"`
	Reason     string `json:"reason"`
	Unknown    bool   `json:"unknown"`
}

type ModelRecommendation struct {
	Provider            schemas.ModelProvider `json:"provider"`
	Model               string                `json:"model"`
	EstimatedCostUSD    float64               `json:"estimated_cost_usd"`
	EstimatedSavingsUSD float64               `json:"estimated_savings_usd"`
	SavingsPct          float64               `json:"savings_pct"`
	Reason              string                `json:"reason"`
}

type CostEstimate struct {
	InputTokens      int     `json:"input_tokens"`
	OutputTokens     int     `json:"output_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd"`
	PricingAvailable bool    `json:"pricing_available"`
}

type GuardDecision struct {
	Action              string                `json:"action"`
	OriginalProvider    schemas.ModelProvider `json:"original_provider"`
	OriginalModel       string                `json:"original_model"`
	EffectiveProvider   schemas.ModelProvider `json:"effective_provider"`
	EffectiveModel      string                `json:"effective_model"`
	Downgraded          bool                  `json:"downgraded"`
	Blocked             bool                  `json:"blocked"`
	BlockReason         string                `json:"block_reason,omitempty"`
	CapabilityIssues    []CapabilityIssue     `json:"capability_issues,omitempty"`
	BudgetViolations    []BudgetViolation     `json:"budget_violations,omitempty"`
	CostEstimate        *CostEstimate         `json:"cost_estimate,omitempty"`
	Recommendations     []ModelRecommendation `json:"recommendations,omitempty"`
	CoalesceRole        string                `json:"coalesce_role,omitempty"`
	CoalescedSavingsUSD float64               `json:"coalesced_savings_usd,omitempty"`
	HasWarnings         bool                  `json:"has_warnings,omitempty"`
	MetadataAppended    bool                  `json:"-"`
	Reasons             []string              `json:"reasons,omitempty"`
}

// CapabilityResult describes the capability guard's action. Pass is false
// when enforcement requires a block or downgrade; audit and warn issues pass.
type CapabilityResult struct {
	Action         string            `json:"action"`
	Pass           bool              `json:"pass"`
	Blocked        bool              `json:"blocked"`
	NeedsDowngrade bool              `json:"needs_downgrade"`
	Issues         []CapabilityIssue `json:"issues,omitempty"`
}

const (
	CapabilityActionAllow     = "allow"
	CapabilityActionAudit     = "audit"
	CapabilityActionWarn      = "warn"
	CapabilityActionBlock     = "block"
	CapabilityActionDowngrade = "downgrade"

	IssueSeverityAudit     = "audit"
	IssueSeverityWarning   = "warning"
	IssueSeverityBlock     = "block"
	IssueSeverityDowngrade = "downgrade"
)

// NewBlockingError constructs a policy refusal with fallback behavior
// explicitly set, matching the Bifrost error shape used by governance.
func NewBlockingError(reason string, allowFallbacks bool) *schemas.BifrostError {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "request blocked by request guard policy"
	}
	errorType := "request_guard_blocked"
	statusCode := http.StatusBadRequest
	allow := allowFallbacks
	return &schemas.BifrostError{
		Type:           &errorType,
		IsBifrostError: true,
		StatusCode:     &statusCode,
		Error:          &schemas.ErrorField{Message: reason},
		AllowFallbacks: &allow,
		ExtraFields: schemas.BifrostErrorExtraFields{
			ErrorType: schemas.ErrorTypePolicyModelBlocked,
		},
	}
}

// AppendGuardrailIntervention appends a concise request-guard intervention to
// the active response metadata. The reason must describe the policy outcome,
// not include matched prompt text or other request content.
func AppendGuardrailIntervention(resp *schemas.BifrostResponse, intervention schemas.BifrostGuardrailIntervention) bool {
	if resp == nil {
		return false
	}
	extra := resp.GetExtraFields()
	if extra == nil {
		return false
	}
	intervention.Phase = strings.TrimSpace(intervention.Phase)
	if intervention.Phase == "" {
		intervention.Phase = "request"
	}
	intervention.GuardrailName = strings.TrimSpace(intervention.GuardrailName)
	if intervention.GuardrailName == "" {
		intervention.GuardrailName = "request_guard"
	}
	intervention.Reason = truncateReason(strings.TrimSpace(intervention.Reason), 256)
	if intervention.Reason == "" {
		return false
	}
	if extra.GuardrailDebug == nil {
		extra.GuardrailDebug = &schemas.BifrostGuardrailMetadata{}
	}
	extra.GuardrailDebug.Interventions = append(extra.GuardrailDebug.Interventions, intervention)
	return true
}

func truncateReason(reason string, maxBytes int) string {
	if len(reason) <= maxBytes {
		return reason
	}
	cut := reason[:maxBytes]
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return strings.TrimSpace(cut)
}
