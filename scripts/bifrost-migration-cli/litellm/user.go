package litellm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// LiteLLMUser mirrors a row from GET /user/list (LiteLLM_UserTable). Only the
// fields relevant to a Bifrost user are decoded. teams holds the LiteLLM
// team_ids the user belongs to; the migration links the user to the Bifrost
// team of the same name (resolved by the caller).
type LiteLLMUser struct {
	UserID         string   `json:"user_id"`
	UserEmail      *string  `json:"user_email"`
	UserAlias      *string  `json:"user_alias"`
	UserRole       *string  `json:"user_role"`
	Teams          []string `json:"teams"` // LiteLLM team_ids
	MaxBudget      *float64 `json:"max_budget"`
	BudgetDuration *string  `json:"budget_duration"`
	TPMLimit       *int64   `json:"tpm_limit"`
	RPMLimit       *int64   `json:"rpm_limit"`
}

// ListUsers fetches every internal user via GET /user/list, following
// pagination because the endpoint returns a fixed page.
func (c *LiteLLMClient) ListUsers(ctx context.Context) ([]LiteLLMUser, error) {
	const pageSize = 100
	base := strings.TrimRight(c.BaseURL, "/") + "/user/list"

	var all []LiteLLMUser
	for page := 1; ; page++ {
		endpoint := fmt.Sprintf("%s?page=%d&page_size=%d", base, page, pageSize)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+c.APIKey)

		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("list users: %w", err)
		}
		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("list users: read body: %w", err)
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("list users: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}

		var pageResp struct {
			Users      []LiteLLMUser `json:"users"`
			TotalPages int           `json:"total_pages"`
		}
		if err := json.Unmarshal(body, &pageResp); err != nil {
			return nil, fmt.Errorf("decode users: %w", err)
		}
		all = append(all, pageResp.Users...)

		if page >= pageResp.TotalPages || len(pageResp.Users) == 0 {
			break
		}
	}
	return all, nil
}
