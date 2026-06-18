package litellm

import (
	"context"
	"fmt"
)

// LiteLLMOrganization mirrors LiteLLM_OrganizationTableWithMembers from
// GET /organization/list. Only the fields relevant to a Bifrost customer are
// decoded; everything else (teams, members, keys, models, spend, metadata) is
// intentionally ignored — those are migrated as their own entities.
type LiteLLMOrganization struct {
	OrganizationID    string         `json:"organization_id"`
	OrganizationAlias string         `json:"organization_alias"`
	Models            []string       `json:"models"` // allowed model names (Bifrost gates these on the VK)
	Budget            *LiteLLMBudget `json:"litellm_budget_table"`
}

// LiteLLMBudget mirrors the joined LiteLLM_BudgetTable row. In LiteLLM the
// budget carries both the spend limit (max_budget / budget_duration) and the
// rate limits (tpm_limit / rpm_limit) for the organization.
type LiteLLMBudget struct {
	MaxBudget      *float64 `json:"max_budget"`      // spend cap in USD
	BudgetDuration *string  `json:"budget_duration"` // reset window, e.g. "30d", "1mo"
	TPMLimit       *int64   `json:"tpm_limit"`       // tokens per minute
	RPMLimit       *int64   `json:"rpm_limit"`       // requests per minute
}

// ListOrganizations fetches every organization via GET /organization/list.
func (c *LiteLLMClient) ListOrganizations(ctx context.Context) ([]LiteLLMOrganization, error) {
	var orgs []LiteLLMOrganization
	if err := doGet(ctx, c, "/organization/list", &orgs); err != nil {
		return nil, fmt.Errorf("list organizations: %w", err)
	}
	return orgs, nil
}
