package logstore

import (
	"slices"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

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
	if l == nil || ctx == nil {
		return
	}
	for _, dimension := range []struct {
		idKey, nameKey schemas.BifrostContextKey
		id, name       **string
	}{
		{schemas.BifrostContextKeyGovernanceVirtualKeyID, schemas.BifrostContextKeyGovernanceVirtualKeyName, &l.VirtualKeyID, &l.VirtualKeyName},
		{schemas.BifrostContextKeyUserID, schemas.BifrostContextKeyUserName, &l.UserID, &l.UserName},
		{schemas.BifrostContextKeyGovernanceTeamID, schemas.BifrostContextKeyGovernanceTeamName, &l.TeamID, &l.TeamName},
		{schemas.BifrostContextKeyGovernanceCustomerID, schemas.BifrostContextKeyGovernanceCustomerName, &l.CustomerID, &l.CustomerName},
		{schemas.BifrostContextKeyGovernanceBusinessUnitID, schemas.BifrostContextKeyGovernanceBusinessUnitName, &l.BusinessUnitID, &l.BusinessUnitName},
		{schemas.BifrostContextKeyGovernanceProjectID, schemas.BifrostContextKeyGovernanceProjectName, &l.ProjectID, &l.ProjectName},
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
		{schemas.BifrostContextKeyGovernanceTeamIDs, schemas.BifrostContextKeyGovernanceTeamNames, &l.TeamIDsParsed, &l.TeamNamesParsed},
		{schemas.BifrostContextKeyGovernanceCustomerIDs, schemas.BifrostContextKeyGovernanceCustomerNames, &l.CustomerIDsParsed, &l.CustomerNamesParsed},
		{schemas.BifrostContextKeyGovernanceBusinessUnitIDs, schemas.BifrostContextKeyGovernanceBusinessUnitNames, &l.BusinessUnitIDsParsed, &l.BusinessUnitNamesParsed},
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
		{schemas.BifrostContextKeyGovernanceBudgetIDs, &l.BudgetIDsParsed},
		{schemas.BifrostContextKeyGovernanceRateLimitIDs, &l.RateLimitIDsParsed},
	} {
		if ids, _ := ctx.Value(set.key).([]string); len(ids) > 0 {
			*set.target = slices.Clone(ids)
		}
	}
}
