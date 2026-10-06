package logstore

import (
	"slices"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// governanceSnapshotTarget describes the persisted request-time attribution shared
// by MCP and A2A logs.
type governanceSnapshotTarget struct {
	virtualKeyID, virtualKeyName       **string
	userID, userName                   **string
	teamID, teamName                   **string
	customerID, customerName           **string
	businessUnitID, businessUnitName   **string
	projectID, projectName             **string
	teamIDs, teamNames                 *[]string
	customerIDs, customerNames         *[]string
	businessUnitIDs, businessUnitNames *[]string
	budgetIDs, rateLimitIDs            *[]string
}

// ApplyGovernanceContext records the request's governance attribution on the entry: every id the
// request settled, and the name that id had at the time.
//
// The names are snapshots, not a cache of the current directory. That is the whole point of writing
// them here rather than resolving them when the log is read: a row keeps saying who made the call
// after the team is renamed or the key is deleted, and it says it without a lookup.
//
// Two rules follow from that. A value the context does not carry leaves whatever is already on the
// entry alone, so a later hook stamping a partially settled identity cannot blank what an earlier one
// knew. And changing an id clears the name beside it, because a name belongs to the id it was read
// with — an id paired with some other entity's name is worse than an id with no name at all.
func (l *MCPToolLog) ApplyGovernanceContext(ctx *schemas.BifrostContext) {
	if l == nil {
		return
	}
	applyGovernanceContext(ctx, governanceSnapshotTarget{
		virtualKeyID: &l.VirtualKeyID, virtualKeyName: &l.VirtualKeyName,
		userID: &l.UserID, userName: &l.UserName,
		teamID: &l.TeamID, teamName: &l.TeamName,
		customerID: &l.CustomerID, customerName: &l.CustomerName,
		businessUnitID: &l.BusinessUnitID, businessUnitName: &l.BusinessUnitName,
		projectID: &l.ProjectID, projectName: &l.ProjectName,
		teamIDs: &l.TeamIDsParsed, teamNames: &l.TeamNamesParsed,
		customerIDs: &l.CustomerIDsParsed, customerNames: &l.CustomerNamesParsed,
		businessUnitIDs: &l.BusinessUnitIDsParsed, businessUnitNames: &l.BusinessUnitNamesParsed,
		budgetIDs: &l.BudgetIDsParsed, rateLimitIDs: &l.RateLimitIDsParsed,
	})
}

func (l *AgentLog) ApplyGovernanceContext(ctx *schemas.BifrostContext) {
	if l == nil {
		return
	}
	applyGovernanceContext(ctx, governanceSnapshotTarget{
		virtualKeyID: &l.VirtualKeyID, virtualKeyName: &l.VirtualKeyName,
		userID: &l.UserID, userName: &l.UserName,
		teamID: &l.TeamID, teamName: &l.TeamName,
		customerID: &l.CustomerID, customerName: &l.CustomerName,
		businessUnitID: &l.BusinessUnitID, businessUnitName: &l.BusinessUnitName,
		projectID: &l.ProjectID, projectName: &l.ProjectName,
		teamIDs: &l.TeamIDsParsed, teamNames: &l.TeamNamesParsed,
		customerIDs: &l.CustomerIDsParsed, customerNames: &l.CustomerNamesParsed,
		businessUnitIDs: &l.BusinessUnitIDsParsed, businessUnitNames: &l.BusinessUnitNamesParsed,
		budgetIDs: &l.BudgetIDsParsed, rateLimitIDs: &l.RateLimitIDsParsed,
	})
}

func applyGovernanceContext(ctx *schemas.BifrostContext, target governanceSnapshotTarget) {
	if ctx == nil {
		return
	}
	for _, dimension := range []struct {
		idKey, nameKey schemas.BifrostContextKey
		id, name       **string
	}{
		{schemas.BifrostContextKeyGovernanceVirtualKeyID, schemas.BifrostContextKeyGovernanceVirtualKeyName, target.virtualKeyID, target.virtualKeyName},
		{schemas.BifrostContextKeyUserID, schemas.BifrostContextKeyUserName, target.userID, target.userName},
		{schemas.BifrostContextKeyGovernanceTeamID, schemas.BifrostContextKeyGovernanceTeamName, target.teamID, target.teamName},
		{schemas.BifrostContextKeyGovernanceCustomerID, schemas.BifrostContextKeyGovernanceCustomerName, target.customerID, target.customerName},
		{schemas.BifrostContextKeyGovernanceBusinessUnitID, schemas.BifrostContextKeyGovernanceBusinessUnitName, target.businessUnitID, target.businessUnitName},
		{schemas.BifrostContextKeyGovernanceProjectID, schemas.BifrostContextKeyGovernanceProjectName, target.projectID, target.projectName},
	} {
		id := bifrost.GetStringFromContext(ctx, dimension.idKey)
		if id == "" {
			continue
		}
		if *dimension.id == nil || **dimension.id != id {
			*dimension.name = nil
		}
		*dimension.id = &id
		if name := bifrost.GetStringFromContext(ctx, dimension.nameKey); name != "" && (*dimension.name == nil || **dimension.name == "") {
			*dimension.name = &name
		}
	}
	// The ids and names of a set are stored index-aligned, so they are read from
	// the context and written to the entry together, never one without the other.
	for _, set := range []struct {
		idsKey, namesKey schemas.BifrostContextKey
		ids, names       *[]string
	}{
		{schemas.BifrostContextKeyGovernanceTeamIDs, schemas.BifrostContextKeyGovernanceTeamNames, target.teamIDs, target.teamNames},
		{schemas.BifrostContextKeyGovernanceCustomerIDs, schemas.BifrostContextKeyGovernanceCustomerNames, target.customerIDs, target.customerNames},
		{schemas.BifrostContextKeyGovernanceBusinessUnitIDs, schemas.BifrostContextKeyGovernanceBusinessUnitNames, target.businessUnitIDs, target.businessUnitNames},
	} {
		ids, _ := ctx.Value(set.idsKey).([]string)
		if len(ids) == 0 {
			continue
		}
		names, _ := ctx.Value(set.namesKey).([]string)
		*set.ids = slices.Clone(ids)
		*set.names = slices.Clone(names)
	}
	// Budgets and rate limits are recorded as ids alone, as they are on the logs
	// table: neither entity carries a display name.
	for _, set := range []struct {
		key    schemas.BifrostContextKey
		target *[]string
	}{
		{schemas.BifrostContextKeyGovernanceBudgetIDs, target.budgetIDs},
		{schemas.BifrostContextKeyGovernanceRateLimitIDs, target.rateLimitIDs},
	} {
		if ids, _ := ctx.Value(set.key).([]string); len(ids) > 0 {
			*set.target = slices.Clone(ids)
		}
	}
}
