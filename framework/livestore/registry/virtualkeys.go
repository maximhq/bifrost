package registry

import (
	"context"
	"sort"
	"time"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/queryscope"
	"gorm.io/gorm"
)

// VirtualKey is one virtual key as request handling reads it. What other records own is named by
// ID: the team, customer and business unit it sits under, and its budgets and rate limit, whose
// settings and usage live with those records. What it may use is its grant set, named by ID and
// shared with every owner granted the same; what funds each provider it may use stays here.
type VirtualKey struct {
	ID, Name string
	// Value is the key's resolved value; "" when an env or vault reference could not be
	// resolved, which leaves the key unreachable by value.
	Value string
	// PreviousValue is the value a rotation retired, accepted until PreviousValueExpiresAt
	// (Unix nanoseconds); "" and 0 when the key is not rotating.
	PreviousValue          string
	PreviousValueExpiresAt int64
	Active                 bool
	ExpiresAt              int64 // Unix nanoseconds; 0 never expires
	CalendarAligned        bool
	DisableContentLogging  *bool
	Metadata               map[string]string
	TeamID                 string
	CustomerID             string
	BusinessUnitID         string
	CreatedByUserID        string
	RateLimitID            string
	BudgetIDs              []string
	// Grants names the key's grant set.
	Grants GrantSetID
	// ProviderLimits fund the key's provider configs, one per config in the order of its grant
	// set's Providers.
	ProviderLimits []ProviderLimit
}

// Index names of the virtual key collection, for GetBy.
const (
	VirtualKeyByValue         = "value"
	VirtualKeyByPreviousValue = "prev_value"
)

// HasActivePreviousValue reports whether the key still accepts its retired value at now.
func (k *VirtualKey) HasActivePreviousValue(now time.Time) bool {
	return k.PreviousValue != "" && k.PreviousValueExpiresAt != 0 && now.UnixNano() < k.PreviousValueExpiresAt
}

// IsExpiredAt reports whether the key's expiry has passed at now.
func (k *VirtualKey) IsExpiredAt(now time.Time) bool {
	return k.ExpiresAt != 0 && now.UnixNano() >= k.ExpiresAt
}

// VirtualKeyID is a virtual key record's primary key.
func VirtualKeyID(k *VirtualKey) string { return k.ID }

// VirtualKeyValue is a virtual key's key in the value index; an unresolved value is not indexed.
func VirtualKeyValue(k *VirtualKey) string { return k.Value }

// VirtualKeyPreviousValue is a rotating key's key in the previous-value index. Postgres lets two
// keys share a retired value, so the index is a multi index; whether the value is still accepted
// is decided when it is read. A key whose current value could not be resolved is left out too, so
// it fails closed on both lookup paths.
func VirtualKeyPreviousValue(k *VirtualKey) []string {
	if k.Value == "" || k.PreviousValue == "" || k.PreviousValue == k.Value {
		return nil
	}
	return []string{k.PreviousValue}
}

// VirtualKeyTeamID is the team a key sits under, "" for none.
func VirtualKeyTeamID(k *VirtualKey) string { return k.TeamID }

// VirtualKeyCustomerID is the customer a key sits under directly, "" for none.
func VirtualKeyCustomerID(k *VirtualKey) string { return k.CustomerID }

// KeyReads reads virtual keys. Grants stores each key's grant set as the key is built and returns
// the ID the key records.
type KeyReads struct {
	Grants func(*GrantSet) GrantSetID
}

// Page reads up to limit virtual keys with IDs after the cursor, in ID order, and returns the
// next cursor, "" after the last page.
func (r KeyReads) Page(ctx context.Context, db *gorm.DB, after string, limit int) ([]*VirtualKey, string, error) {
	rows, err := r.read(ctx, db, func(q *gorm.DB) *gorm.DB {
		return q.Where("id > ?", after).Order("id").Limit(limit)
	})
	if err != nil || len(rows) < limit {
		return rows, "", err
	}
	return rows, rows[len(rows)-1].ID, nil
}

// ByID reads the virtual keys with these IDs; an ID with no row is left out.
func (r KeyReads) ByID(ctx context.Context, db *gorm.DB, ids []string) ([]*VirtualKey, error) {
	return r.read(ctx, db, func(q *gorm.DB) *gorm.DB { return q.Where("id IN ?", ids) })
}

// ByTeams reads the virtual keys under these teams.
func (r KeyReads) ByTeams(ctx context.Context, db *gorm.DB, teamIDs []string) ([]*VirtualKey, error) {
	return r.read(ctx, db, func(q *gorm.DB) *gorm.DB { return q.Where("team_id IN ?", teamIDs) })
}

// ByCustomers reads the virtual keys directly under these customers.
func (r KeyReads) ByCustomers(ctx context.Context, db *gorm.DB, customerIDs []string) ([]*VirtualKey, error) {
	return r.read(ctx, db, func(q *gorm.DB) *gorm.DB { return q.Where("customer_id IN ?", customerIDs) })
}

// VirtualKeyFromTable builds a key's record from its table struct, which must carry its grants;
// budgetIDs and virtualMCPIDs are the IDs of the budgets it holds directly and the virtual MCPs
// assigned to it, and grants stores the key's grant set. The loader calls it on the rows it read; a
// gateway without a config store calls it on the keys config.json declares.
func VirtualKeyFromTable(t *tables.TableVirtualKey, budgetIDs []string, virtualMCPIDs []uint, grants func(*GrantSet) GrantSetID) *VirtualKey {
	k := &VirtualKey{
		ID:                    t.ID,
		Name:                  t.Name,
		Value:                 t.Value.GetValue(),
		Active:                t.IsActiveValue(),
		CalendarAligned:       t.CalendarAligned,
		DisableContentLogging: t.DisableContentLogging,
		Metadata:              t.Metadata,
		TeamID:                deref(t.TeamID),
		CustomerID:            deref(t.CustomerID),
		BusinessUnitID:        deref(t.BusinessUnitID),
		CreatedByUserID:       deref(t.CreatedByUserID),
		RateLimitID:           deref(t.RateLimitID),
		BudgetIDs:             budgetIDs,
	}
	if t.ExpiresAt != nil {
		k.ExpiresAt = t.ExpiresAt.UnixNano()
	}
	if t.PreviousValue.IsSet() && t.PreviousValueExpiresAt != nil {
		k.PreviousValue = t.PreviousValue.GetValue()
		k.PreviousValueExpiresAt = t.PreviousValueExpiresAt.UnixNano()
	}
	set := &GrantSet{AllowAllProviders: t.AllowAllProviders, VirtualMCPIDs: virtualMCPIDs}
	if t.ProviderConfigs != nil {
		set.Providers = make([]ProviderGrant, 0, len(t.ProviderConfigs))
		k.ProviderLimits = make([]ProviderLimit, 0, len(t.ProviderConfigs))
	}
	for i := range t.ProviderConfigs {
		pc := &t.ProviderConfigs[i]
		set.Providers = append(set.Providers, ProviderGrant{
			Provider: pc.Provider, Weight: pc.Weight, AllowedModels: pc.AllowedModels,
			BlacklistedModels: pc.BlacklistedModels, KeyIDs: pc.KeyIDs(),
		})
		limit := ProviderLimit{ConfigID: pc.ID, Provider: pc.Provider, RateLimitID: deref(pc.RateLimitID)}
		for _, b := range pc.Budgets {
			limit.BudgetIDs = append(limit.BudgetIDs, b.ID)
		}
		k.ProviderLimits = append(k.ProviderLimits, limit)
	}
	if t.MCPConfigs != nil {
		set.MCP = make([]MCPGrant, 0, len(t.MCPConfigs))
	}
	for i := range t.MCPConfigs {
		mc := &t.MCPConfigs[i]
		set.MCP = append(set.MCP, MCPGrant{
			ClientRowID: mc.MCPClientID, ClientID: mc.MCPClient.ClientID, ClientName: mc.MCPClient.Name, Tools: mc.ToolsToExecute,
		})
	}
	if t.AgentGrants != nil {
		set.Agents = make([]string, 0, len(t.AgentGrants))
	}
	for _, a := range t.AgentGrants {
		set.Agents = append(set.Agents, a.AgentName)
	}
	if t.RelayGrants != nil {
		set.Relays = make([]string, 0, len(t.RelayGrants))
	}
	for _, r := range t.RelayGrants {
		set.Relays = append(set.Relays, r.RelayName)
	}
	k.Grants = grants(SealGrantSet(set))
	return k
}

// read loads the key rows where selects, with their grants, the IDs of the budgets they hold
// directly and the virtual MCPs assigned to them.
func (r KeyReads) read(ctx context.Context, db *gorm.DB, where func(*gorm.DB) *gorm.DB) ([]*VirtualKey, error) {
	var rows []tables.TableVirtualKey
	q := db.WithContext(ctx).
		Preload("ProviderConfigs").
		Preload("ProviderConfigs.Budgets", func(b *gorm.DB) *gorm.DB {
			return b.Select("id, provider_config_id").Order("created_at, id")
		}).
		Preload("ProviderConfigs.Keys", func(k *gorm.DB) *gorm.DB {
			return k.Select("id, key_id")
		}).
		Preload("MCPConfigs").
		Preload("MCPConfigs.MCPClient", func(c *gorm.DB) *gorm.DB { return c.Select("id, client_id, name") }).
		Preload("AgentGrants").
		Preload("RelayGrants")
	if err := where(q).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(rows))
	for i := range rows {
		ids = append(ids, rows[i].ID)
	}
	budgets, err := budgetIDsBy(ctx, db, "virtual_key_id", ids)
	if err != nil {
		return nil, err
	}
	var assignments []tables.TableVirtualKeyVirtualMCP
	if err := db.WithContext(ctx).Where(queryscope.InStrings(db, "virtual_key_id", ids)).Find(&assignments).Error; err != nil {
		return nil, err
	}
	virtualMCPs := make(map[string][]uint)
	for _, a := range assignments {
		virtualMCPs[a.VirtualKeyID] = append(virtualMCPs[a.VirtualKeyID], a.VirtualMCPID)
	}
	out := make([]*VirtualKey, 0, len(rows))
	for i := range rows {
		assigned := virtualMCPs[rows[i].ID]
		sort.Slice(assigned, func(a, b int) bool { return assigned[a] < assigned[b] })
		out = append(out, VirtualKeyFromTable(&rows[i], budgets[rows[i].ID], assigned, r.Grants))
	}
	return out, nil
}

// budgetIDsBy returns the IDs of the budgets whose column names one of ids, grouped by that ID,
// in the order the budgets were created. The IDs bind as one argument, so a read of any number of
// owners stays within the statement parameter limit.
func budgetIDsBy(ctx context.Context, db *gorm.DB, column string, ids []string) (map[string][]string, error) {
	out := make(map[string][]string)
	if len(ids) == 0 {
		return out, nil
	}
	var rows []struct {
		ID    string
		Owner string
	}
	if err := db.WithContext(ctx).Table("governance_budgets").Select("id, " + column + " AS owner").
		Where(queryscope.InStrings(db, column, ids)).Order("created_at, id").Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.Owner] = append(out[r.Owner], r.ID)
	}
	return out, nil
}
