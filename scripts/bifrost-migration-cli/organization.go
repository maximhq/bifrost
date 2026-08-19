package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/maximhq/bifrost/scripts/bifrost-migration-cli/litellm"
)

// rateLimitResetWindow is the Bifrost reset duration for migrated rate limits.
// LiteLLM's tpm_limit / rpm_limit are defined per minute, so both the token and
// request limits reset every minute in Bifrost.
const rateLimitResetWindow = "1m"

// defaultMaxBudgetPeriod is the Bifrost reset window used when a LiteLLM budget
// has a spend cap but no budget_duration. LiteLLM allows a never-resetting
// budget; Bifrost requires a reset window, so a long default stands in for
// "effectively never".
const defaultMaxBudgetPeriod = "10Y"

// litellmDurationRe matches a LiteLLM budget_duration: a positive integer
// followed by a unit (s, m, h, d, w, mo). It is intentionally unanchored at the
// end to mirror LiteLLM's own lenient parser (e.g. "1hr" -> "1h"). Named
// durations like "daily" have no leading digit and therefore do not match.
var litellmDurationRe = regexp.MustCompile(`^(\d+)(mo|[smhdw])`)

// LiteLLMOrganizationToBifrostCustomer transforms a LiteLLM organization into a Bifrost
// create-customer request. The mapping is pure (no I/O) so each field can be
// unit-tested in isolation.
//
// Mapping:
//   - organization_alias        -> name (required)
//   - budget.max_budget         -> budgets[0].max_limit       (omitted when <= 0)
//   - budget.budget_duration    -> budgets[0].reset_duration  ("mo" -> "M")
//   - budget.tpm_limit          -> rate_limit.token_max_limit   (reset "1m")
//   - budget.rpm_limit          -> rate_limit.request_max_limit (reset "1m")
//
// Models and allowed MCP servers are intentionally not migrated: a Bifrost
// customer has no models, and MCP linkage is handled separately.
func LiteLLMOrganizationToBifrostCustomer(org litellm.LiteLLMOrganization, cfg MigrationRunConfig) (*BifrostCreateCustomerRequest, error) {
	name := strings.TrimSpace(org.OrganizationAlias)
	if name == "" {
		return nil, fmt.Errorf("organization %q has no organization_alias; a customer name is required", org.OrganizationID)
	}

	req := &BifrostCreateCustomerRequest{Name: name}

	if org.Budget != nil {
		budget, err := toBudget(*org.Budget, cfg.MaxBudgetPeriod)
		if err != nil {
			return nil, fmt.Errorf("organization %q: %w", org.OrganizationID, err)
		}
		if budget != nil {
			req.Budgets = []BifrostCreateBudgetRequest{*budget}
		}
		req.RateLimit = toRateLimit(*org.Budget)
	}

	return req, nil
}

// toBudget maps the LiteLLM spend cap to a Bifrost budget.
//
//   - max_budget nil or <= 0 -> no budget (nil, nil); LiteLLM treats this as
//     "no spend cap", and Bifrost requires a positive max_limit.
//   - max_budget > 0 with a missing/blank budget_duration -> maxBudgetPeriod;
//     LiteLLM allows a never-reset budget but Bifrost requires a window, so the
//     operator-supplied default stands in.
//   - max_budget > 0 with an unparseable budget_duration -> error.
func toBudget(b litellm.LiteLLMBudget, maxBudgetPeriod string) (*BifrostCreateBudgetRequest, error) {
	if b.MaxBudget == nil || *b.MaxBudget <= 0 {
		return nil, nil
	}

	if b.BudgetDuration == nil || strings.TrimSpace(*b.BudgetDuration) == "" {
		return &BifrostCreateBudgetRequest{MaxLimit: *b.MaxBudget, ResetDuration: maxBudgetPeriod}, nil
	}

	reset, err := convertBudgetDuration(*b.BudgetDuration)
	if err != nil {
		return nil, err
	}

	return &BifrostCreateBudgetRequest{MaxLimit: *b.MaxBudget, ResetDuration: reset}, nil
}

// toRateLimit maps tpm_limit / rpm_limit onto a Bifrost rate limit. Each
// dimension is independent: a non-positive or nil limit is treated as "no
// limit" and omitted. Returns nil when neither dimension has a positive limit.
func toRateLimit(b litellm.LiteLLMBudget) *BifrostCreateRateLimitRequest {
	rl := &BifrostCreateRateLimitRequest{}
	set := false

	if b.TPMLimit != nil && *b.TPMLimit > 0 {
		v := *b.TPMLimit
		reset := rateLimitResetWindow
		rl.TokenMaxLimit = &v
		rl.TokenResetDuration = &reset
		set = true
	}

	if b.RPMLimit != nil && *b.RPMLimit > 0 {
		v := *b.RPMLimit
		reset := rateLimitResetWindow
		rl.RequestMaxLimit = &v
		rl.RequestResetDuration = &reset
		set = true
	}

	if !set {
		return nil
	}
	return rl
}

// convertBudgetDuration translates a LiteLLM budget_duration into Bifrost's
// duration format. The only unit difference is months: LiteLLM uses "mo",
// Bifrost uses "M". All other units (s, m, h, d, w) are identical.
func convertBudgetDuration(d string) (string, error) {
	d = strings.TrimSpace(d)
	m := litellmDurationRe.FindStringSubmatch(d)
	if m == nil {
		return "", fmt.Errorf("unsupported budget_duration %q: expected <number><s|m|h|d|w|mo>", d)
	}

	value, unit := m[1], m[2]
	if unit == "mo" {
		unit = "M"
	}
	return value + unit, nil
}
