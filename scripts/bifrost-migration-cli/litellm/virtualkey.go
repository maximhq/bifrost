package litellm

import (
	"context"
	"fmt"
)

// LiteLLMVirtualKey mirrors a row from GET /key/list (return_full_object=true).
// Only the fields a Bifrost virtual key can carry are decoded.
type LiteLLMVirtualKey struct {
	KeyAlias       *string  `json:"key_alias"`
	KeyName        *string  `json:"key_name"` // masked token, used as a name fallback
	UserID         *string  `json:"user_id"`  // not linkable on VK create (see report)
	TeamID         *string  `json:"team_id"`
	OrgID          *string  `json:"org_id"`
	Models         []string `json:"models"` // allowed model names ([] => all)
	MaxBudget      *float64 `json:"max_budget"`
	BudgetDuration *string  `json:"budget_duration"`
	TPMLimit       *int64   `json:"tpm_limit"`
	RPMLimit       *int64   `json:"rpm_limit"`
	Blocked        *bool    `json:"blocked"`
	Expires        *string  `json:"expires"`
}

// ListVirtualKeys fetches every virtual key via GET /key/list, following
// pagination. return_full_object=true yields the budget/limit/model fields and
// include_team_keys=true includes team-scoped keys.
func (c *LiteLLMClient) ListVirtualKeys(ctx context.Context) ([]LiteLLMVirtualKey, error) {
	const pageSize = 100

	var all []LiteLLMVirtualKey
	for page := 1; ; page++ {
		path := fmt.Sprintf("/key/list?include_team_keys=true&return_full_object=true&page=%d&page_size=%d", page, pageSize)

		var pageResp struct {
			Keys       []LiteLLMVirtualKey `json:"keys"`
			TotalPages int                 `json:"total_pages"`
		}
		if err := doGet(ctx, c, path, &pageResp); err != nil {
			return nil, fmt.Errorf("list virtual keys: %w", err)
		}
		all = append(all, pageResp.Keys...)

		if page >= pageResp.TotalPages || len(pageResp.Keys) == 0 {
			break
		}
	}
	return all, nil
}
