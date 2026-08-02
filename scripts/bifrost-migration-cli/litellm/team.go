package litellm

import (
	"context"
	"fmt"
)

// LiteLLMTeam mirrors a row from GET /team/list. Only the fields relevant to a
// Bifrost team are decoded; members, keys, models, metadata and spend are
// intentionally ignored (members/keys/models are their own entities, and
// Bifrost teams carry no membership).
//
// Unlike organizations, a team's spend cap and rate limits are inline on the
// team object (max_budget / budget_duration / tpm_limit / rpm_limit), not in a
// joined litellm_budget_table.
type LiteLLMTeam struct {
	TeamID         string   `json:"team_id"`
	TeamAlias      string   `json:"team_alias"`
	OrganizationID *string  `json:"organization_id"` // nil/empty => standalone team
	Models         []string `json:"models"`          // allowed model names (Bifrost gates these on the VK)
	MaxBudget      *float64 `json:"max_budget"`
	BudgetDuration *string  `json:"budget_duration"`
	TPMLimit       *int64   `json:"tpm_limit"`
	RPMLimit       *int64   `json:"rpm_limit"`
}

// ListTeams fetches every team via GET /team/list.
func (c *LiteLLMClient) ListTeams(ctx context.Context) ([]LiteLLMTeam, error) {
	var teams []LiteLLMTeam
	if err := doGet(ctx, c, "/team/list", &teams); err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}
	return teams, nil
}
