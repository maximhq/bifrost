package logstore

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// governanceCtx builds a hook context carrying the values a fully settled request
// leaves behind.
func governanceCtx(t *testing.T, values map[schemas.BifrostContextKey]any) *schemas.BifrostContext {
	t.Helper()
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	for key, value := range values {
		ctx.SetValue(key, value)
	}
	return ctx
}

// TestApplyGovernanceContextRecordsEveryDimension covers the whole point of the
// change: the entry carries its own attribution, ids and names together, so a
// reader never has to resolve anything.
func TestApplyGovernanceContextRecordsEveryDimension(t *testing.T) {
	entry := &MCPToolLog{}
	entry.ApplyGovernanceContext(governanceCtx(t, map[schemas.BifrostContextKey]any{
		schemas.BifrostContextKeyGovernanceVirtualKeyID:      "vk1",
		schemas.BifrostContextKeyGovernanceVirtualKeyName:    "VK One",
		schemas.BifrostContextKeyUserID:                      "user1",
		schemas.BifrostContextKeyUserName:                    "User One",
		schemas.BifrostContextKeyGovernanceTeamID:            "team1",
		schemas.BifrostContextKeyGovernanceTeamName:          "Team One",
		schemas.BifrostContextKeyGovernanceCustomerID:        "cust1",
		schemas.BifrostContextKeyGovernanceCustomerName:      "Customer One",
		schemas.BifrostContextKeyGovernanceBusinessUnitID:    "bu1",
		schemas.BifrostContextKeyGovernanceBusinessUnitName:  "BU One",
		schemas.BifrostContextKeyGovernanceProjectID:         "proj1",
		schemas.BifrostContextKeyGovernanceProjectName:       "Project One",
		schemas.BifrostContextKeyGovernanceTeamIDs:           []string{"team1", "team2"},
		schemas.BifrostContextKeyGovernanceTeamNames:         []string{"Team One", "Team Two"},
		schemas.BifrostContextKeyGovernanceCustomerIDs:       []string{"cust1"},
		schemas.BifrostContextKeyGovernanceCustomerNames:     []string{"Customer One"},
		schemas.BifrostContextKeyGovernanceBusinessUnitIDs:   []string{"bu1"},
		schemas.BifrostContextKeyGovernanceBusinessUnitNames: []string{"BU One"},
		schemas.BifrostContextKeyGovernanceBudgetIDs:         []string{"budget1"},
		schemas.BifrostContextKeyGovernanceRateLimitIDs:      []string{"rl1"},
	}))

	for _, field := range []struct {
		name string
		got  *string
		want string
	}{
		{"virtual_key_id", entry.VirtualKeyID, "vk1"},
		{"virtual_key_name", entry.VirtualKeyName, "VK One"},
		{"user_id", entry.UserID, "user1"},
		{"user_name", entry.UserName, "User One"},
		{"team_id", entry.TeamID, "team1"},
		{"team_name", entry.TeamName, "Team One"},
		{"customer_id", entry.CustomerID, "cust1"},
		{"customer_name", entry.CustomerName, "Customer One"},
		{"business_unit_id", entry.BusinessUnitID, "bu1"},
		{"business_unit_name", entry.BusinessUnitName, "BU One"},
		{"project_id", entry.ProjectID, "proj1"},
		{"project_name", entry.ProjectName, "Project One"},
	} {
		if field.got == nil || *field.got != field.want {
			t.Fatalf("%s = %v, want %q", field.name, field.got, field.want)
		}
	}
	assertStrings(t, "team_ids", entry.TeamIDsParsed, []string{"team1", "team2"})
	assertStrings(t, "team_names", entry.TeamNamesParsed, []string{"Team One", "Team Two"})
	assertStrings(t, "customer_ids", entry.CustomerIDsParsed, []string{"cust1"})
	assertStrings(t, "customer_names", entry.CustomerNamesParsed, []string{"Customer One"})
	assertStrings(t, "business_unit_ids", entry.BusinessUnitIDsParsed, []string{"bu1"})
	assertStrings(t, "business_unit_names", entry.BusinessUnitNamesParsed, []string{"BU One"})
	assertStrings(t, "budget_ids", entry.BudgetIDsParsed, []string{"budget1"})
	assertStrings(t, "rate_limit_ids", entry.RateLimitIDsParsed, []string{"rl1"})
}

// TestApplyGovernanceContextKeepsRecordedName covers a second hook re-stamping the
// same identity. The name already recorded is the one read with that id, so it
// stands rather than being rewritten from a cache that may have moved on.
func TestApplyGovernanceContextKeepsRecordedName(t *testing.T) {
	recorded := "Name At Call Time"
	id := "team1"
	entry := &MCPToolLog{TeamID: &id, TeamName: &recorded}
	entry.ApplyGovernanceContext(governanceCtx(t, map[schemas.BifrostContextKey]any{
		schemas.BifrostContextKeyGovernanceTeamID:   "team1",
		schemas.BifrostContextKeyGovernanceTeamName: "Renamed Since",
	}))
	if *entry.TeamName != recorded {
		t.Fatalf("team_name = %q, want the name recorded with the id", *entry.TeamName)
	}
}

// TestApplyGovernanceContextClearsNameWhenIDChanges pins the rule that keeps
// attribution honest: an id paired with some other entity's name is worse than an
// id with no name at all.
func TestApplyGovernanceContextClearsNameWhenIDChanges(t *testing.T) {
	stale, staleName := "team-old", "Old Team"
	entry := &MCPToolLog{TeamID: &stale, TeamName: &staleName}
	entry.ApplyGovernanceContext(governanceCtx(t, map[schemas.BifrostContextKey]any{
		schemas.BifrostContextKeyGovernanceTeamID: "team-new",
	}))
	if entry.TeamID == nil || *entry.TeamID != "team-new" {
		t.Fatalf("team_id = %v, want the new id", entry.TeamID)
	}
	if entry.TeamName != nil {
		t.Fatalf("team_name = %q, want the stale name cleared", *entry.TeamName)
	}
}

// TestApplyGovernanceContextPreservesUnknownDimensions covers a partially settled
// context: what an earlier stamp knew is not blanked by a later one that does not.
func TestApplyGovernanceContextPreservesUnknownDimensions(t *testing.T) {
	id, name := "cust1", "Customer One"
	entry := &MCPToolLog{CustomerID: &id, CustomerName: &name, TeamIDsParsed: []string{"team1"}}
	entry.ApplyGovernanceContext(governanceCtx(t, map[schemas.BifrostContextKey]any{
		schemas.BifrostContextKeyUserID: "user1",
	}))
	if entry.CustomerID == nil || *entry.CustomerID != id || entry.CustomerName == nil || *entry.CustomerName != name {
		t.Fatalf("existing customer attribution lost: %+v", entry)
	}
	assertStrings(t, "team_ids", entry.TeamIDsParsed, []string{"team1"})
}

// TestApplyGovernanceContextNilSafe covers the paths that build an entry before
// any context exists.
func TestApplyGovernanceContextNilSafe(t *testing.T) {
	var entry *MCPToolLog
	entry.ApplyGovernanceContext(nil)
	(&MCPToolLog{}).ApplyGovernanceContext(nil)
}

func assertStrings(t *testing.T, field string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", field, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s = %v, want %v", field, got, want)
		}
	}
}
