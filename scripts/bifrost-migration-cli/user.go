package main

import (
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/scripts/bifrost-migration-cli/litellm"
)

// UserPlan is the planned Bifrost user (POST /api/users) plus the LiteLLM team
// ids it should be linked to. Team-id -> Bifrost-team resolution and the
// membership writes are done by the caller (orchestration), keeping the
// transform pure.
type UserPlan struct {
	UserID        string   // LiteLLM user_id, for logging
	Name          string   // Bifrost user name (required, non-empty)
	Email         string   // Bifrost user email (required, valid)
	SourceTeamIDs []string // LiteLLM team_ids to link as memberships
}

// UserMigrationReport collects everything the transform could not faithfully
// carry, so the operator can act on it.
type UserMigrationReport struct {
	SkippedNoEmail    []string // LiteLLM has no email; Bifrost requires one
	DroppedRoles      []string // LiteLLM user_role has no Bifrost numeric role_id mapping
	DroppedBudgets    []string // user-level spend cap (no field on POST /api/users)
	DroppedRateLimits []string // user-level tpm/rpm (no field on POST /api/users)
}

// LiteLLMUsersToBifrostUsers transforms LiteLLM internal users into Bifrost user
// plans. The mapping is pure (no I/O):
//   - user_email -> email (required; user skipped + reported when missing)
//   - user_alias -> name  (falls back to email when blank)
//   - teams      -> SourceTeamIDs (linked downstream)
//
// LiteLLM user_role, max_budget/budget_duration and tpm/rpm have no field on
// Bifrost's create-user API (role is a numeric role_id with no LiteLLM mapping;
// user governance is driven by access profiles), so they are reported, not
// carried.
func LiteLLMUsersToBifrostUsers(users []litellm.LiteLLMUser) ([]UserPlan, UserMigrationReport) {
	var plans []UserPlan
	var report UserMigrationReport

	for _, u := range users {
		email := ""
		if u.UserEmail != nil {
			email = strings.TrimSpace(*u.UserEmail)
		}
		if email == "" {
			report.SkippedNoEmail = append(report.SkippedNoEmail, u.UserID)
			continue
		}

		name := ""
		if u.UserAlias != nil {
			name = strings.TrimSpace(*u.UserAlias)
		}
		if name == "" {
			name = email
		}

		if u.UserRole != nil && strings.TrimSpace(*u.UserRole) != "" {
			report.DroppedRoles = append(report.DroppedRoles, fmt.Sprintf("%s (%s)", u.UserID, strings.TrimSpace(*u.UserRole)))
		}
		if u.MaxBudget != nil && *u.MaxBudget > 0 {
			report.DroppedBudgets = append(report.DroppedBudgets, fmt.Sprintf("%s ($%.2f)", u.UserID, *u.MaxBudget))
		}
		if (u.TPMLimit != nil && *u.TPMLimit > 0) || (u.RPMLimit != nil && *u.RPMLimit > 0) {
			report.DroppedRateLimits = append(report.DroppedRateLimits, u.UserID)
		}

		plans = append(plans, UserPlan{
			UserID:        u.UserID,
			Name:          name,
			Email:         email,
			SourceTeamIDs: u.Teams,
		})
	}

	return plans, report
}
