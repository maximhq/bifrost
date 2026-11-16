package registry

import (
	"context"
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/queryscope"
	"gorm.io/gorm"
)

// Budget is the record of one budget: the row's columns, embedded, and the holder the loader found
// for it. When a virtual key (directly or through one of its provider configs), a
// team, a customer, a model config or a provider holds it, the record names that holder and its
// IsCalendarAligned is the holder's flag, since a budget's reset window follows its holder's flag.
// A budget no holder here names is loaded too, with no holder and the flag off, as every budget
// row always has been; its own holder stamps the flag where it keeps it. Its usage columns are what
// the row held when it was read: the governance store keeps live usage and takes them only for a
// budget it has not seen.
type Budget struct {
	tables.TableBudget
	KeyID    string // the virtual key holding it, directly or through one of its provider configs
	Provider string // the provider whose row names it
}

// RateLimit is the record of one rate limit: the row's columns, embedded, and its holder. Its holder
// names it by ID, so the loader finds the holder by
// looking for that ID: a virtual key (directly or through one of its provider configs), a team, a
// customer, a model config or a provider, whose flag becomes its IsCalendarAligned. A rate limit
// no holder here names is loaded too, with no holder and the flag off, as Budget is. Its usage
// columns are treated as Budget's are.
type RateLimit struct {
	tables.TableRateLimit
	KeyID, TeamID, CustomerID, ModelConfigID, Provider string
}

// BudgetID is a budget record's primary key.
func BudgetID(b *Budget) string { return b.ID }

// BudgetKeyID is the virtual key holding a budget, "" for another holder.
func BudgetKeyID(b *Budget) string { return b.KeyID }

// BudgetTeamID is the team holding a budget, "" for another holder.
func BudgetTeamID(b *Budget) string { return deref(b.TeamID) }

// BudgetCustomerID is the customer holding a budget, "" for another holder.
func BudgetCustomerID(b *Budget) string { return deref(b.CustomerID) }

// BudgetModelConfigID is the model config holding a budget, "" for another holder.
func BudgetModelConfigID(b *Budget) string { return deref(b.ModelConfigID) }

// BudgetProvider is the provider holding a budget, "" for another holder.
func BudgetProvider(b *Budget) string { return b.Provider }

// BudgetsPage reads up to limit budgets with IDs after the cursor, in ID order, and returns
// the next cursor, "" after the last page.
func BudgetsPage(ctx context.Context, db *gorm.DB, after string, limit int) ([]*Budget, string, error) {
	rows, err := readBudgets(ctx, db, func(q *gorm.DB) *gorm.DB {
		return q.Where("id > ?", after).Order("id").Limit(limit)
	})
	if err != nil || len(rows) < limit {
		return rows, "", err
	}
	return rows, rows[len(rows)-1].ID, nil
}

// BudgetsByID reads the budgets with these IDs; an ID with no row is left out.
func BudgetsByID(ctx context.Context, db *gorm.DB, ids []string) ([]*Budget, error) {
	return readBudgets(ctx, db, func(q *gorm.DB) *gorm.DB { return q.Where("id IN ?", ids) })
}

// BudgetsByKeys reads the budgets of these virtual keys, their provider configs' included. The keys'
// own budgets and their provider configs' are read in two queries: one query with OR across the
// two columns leaves Postgres no index to use, so it scanned every budget on each key write.
func BudgetsByKeys(ctx context.Context, db *gorm.DB, keyIDs []string) ([]*Budget, error) {
	own, err := readBudgets(ctx, db, func(q *gorm.DB) *gorm.DB { return q.Where("virtual_key_id IN ?", keyIDs) })
	if err != nil {
		return nil, err
	}
	viaConfigs, err := readBudgets(ctx, db, func(q *gorm.DB) *gorm.DB {
		return q.Where("provider_config_id IN (SELECT id FROM governance_virtual_key_provider_configs WHERE virtual_key_id IN ?)", keyIDs)
	})
	if err != nil {
		return nil, err
	}
	return appendNew(own, viaConfigs, BudgetID), nil
}

// BudgetsByTeams reads the budgets of these teams.
func BudgetsByTeams(ctx context.Context, db *gorm.DB, teamIDs []string) ([]*Budget, error) {
	return readBudgets(ctx, db, func(q *gorm.DB) *gorm.DB { return q.Where("team_id IN ?", teamIDs) })
}

// BudgetsByCustomers reads the budgets of these customers.
func BudgetsByCustomers(ctx context.Context, db *gorm.DB, customerIDs []string) ([]*Budget, error) {
	return readBudgets(ctx, db, func(q *gorm.DB) *gorm.DB { return q.Where("customer_id IN ?", customerIDs) })
}

// BudgetsByModelConfigs reads the budgets of these model configs.
func BudgetsByModelConfigs(ctx context.Context, db *gorm.DB, modelConfigIDs []string) ([]*Budget, error) {
	return readBudgets(ctx, db, func(q *gorm.DB) *gorm.DB { return q.Where("model_config_id IN ?", modelConfigIDs) })
}

// BudgetsByProviders reads the budgets these providers' rows name.
func BudgetsByProviders(ctx context.Context, db *gorm.DB, names []string) ([]*Budget, error) {
	return readBudgets(ctx, db, func(q *gorm.DB) *gorm.DB {
		return q.Where("id IN (SELECT budget_id FROM config_providers WHERE name IN ?)", names)
	})
}

// readBudgets loads the budget rows where selects and finds each one's holder and its calendar
// flag. A budget names its holder on its own row; the owners read gives the holder's flag, a
// provider config's key and the name of a provider whose row names the budget.
func readBudgets(ctx context.Context, db *gorm.DB, where func(*gorm.DB) *gorm.DB) ([]*Budget, error) {
	var rows []tables.TableBudget
	if err := where(db.WithContext(ctx)).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(rows))
	for i := range rows {
		ids = append(ids, rows[i].ID)
	}
	found, err := readHolders(ctx, db, budgetOwners, ids)
	if err != nil {
		return nil, err
	}
	of := func(kind holderKind, id string) limitHolder { return found[ownerIndex(budgetOwners, kind)][id] }
	out := make([]*Budget, 0, len(rows))
	for i := range rows {
		b := &Budget{TableBudget: rows[i]}
		switch {
		case b.VirtualKeyID != nil:
			b.KeyID = *b.VirtualKeyID
			b.IsCalendarAligned = of(holderKey, b.ID).CalendarAligned
		case b.ProviderConfigID != nil:
			h := of(holderProviderConfig, b.ID)
			b.KeyID, b.IsCalendarAligned = h.HolderID, h.CalendarAligned
		case b.TeamID != nil:
			b.IsCalendarAligned = of(holderTeam, b.ID).CalendarAligned
		case b.CustomerID != nil:
			b.IsCalendarAligned = of(holderCustomer, b.ID).CalendarAligned
		case b.ModelConfigID != nil:
			b.IsCalendarAligned = of(holderModelConfig, b.ID).CalendarAligned
		default:
			b.Provider = of(holderProvider, b.ID).HolderID
		}
		out = append(out, b)
	}
	return out, nil
}

// RateLimitID is a rate limit record's primary key.
func RateLimitID(r *RateLimit) string { return r.ID }

// RateLimitKeyID is the virtual key holding a rate limit, "" for another holder.
func RateLimitKeyID(r *RateLimit) string { return r.KeyID }

// RateLimitTeamID is the team holding a rate limit, "" for another holder.
func RateLimitTeamID(r *RateLimit) string { return r.TeamID }

// RateLimitCustomerID is the customer holding a rate limit, "" for another holder.
func RateLimitCustomerID(r *RateLimit) string { return r.CustomerID }

// RateLimitModelConfigID is the model config holding a rate limit, "" for another holder.
func RateLimitModelConfigID(r *RateLimit) string { return r.ModelConfigID }

// RateLimitProvider is the provider holding a rate limit, "" for another holder.
func RateLimitProvider(r *RateLimit) string { return r.Provider }

// RateLimitsPage reads up to limit rate limits with IDs after the cursor, in ID order, and
// returns the next cursor, "" after the last page.
func RateLimitsPage(ctx context.Context, db *gorm.DB, after string, limit int) ([]*RateLimit, string, error) {
	rows, err := readRateLimits(ctx, db, func(q *gorm.DB) *gorm.DB {
		return q.Where("id > ?", after).Order("id").Limit(limit)
	})
	if err != nil || len(rows) < limit {
		return rows, "", err
	}
	return rows, rows[len(rows)-1].ID, nil
}

// RateLimitsByID reads the rate limits with these IDs; an ID with no row is left out.
func RateLimitsByID(ctx context.Context, db *gorm.DB, ids []string) ([]*RateLimit, error) {
	return readRateLimits(ctx, db, func(q *gorm.DB) *gorm.DB { return q.Where("id IN ?", ids) })
}

// RateLimitsByKeys reads the rate limits of these virtual keys, their provider configs' included.
// Like BudgetsByKeys it reads the two in separate queries, so each uses the primary key rather than
// scanning every rate limit.
func RateLimitsByKeys(ctx context.Context, db *gorm.DB, keyIDs []string) ([]*RateLimit, error) {
	own, err := readRateLimits(ctx, db, func(q *gorm.DB) *gorm.DB {
		return q.Where("id IN (SELECT rate_limit_id FROM governance_virtual_keys WHERE id IN ?)", keyIDs)
	})
	if err != nil {
		return nil, err
	}
	viaConfigs, err := readRateLimits(ctx, db, func(q *gorm.DB) *gorm.DB {
		return q.Where("id IN (SELECT rate_limit_id FROM governance_virtual_key_provider_configs WHERE virtual_key_id IN ?)", keyIDs)
	})
	if err != nil {
		return nil, err
	}
	return appendNew(own, viaConfigs, RateLimitID), nil
}

// appendNew appends to rows the records of more whose ID rows does not hold yet, so a limit both
// queries of a key reader found is returned once.
func appendNew[T any](rows, more []*T, id func(*T) string) []*T {
	if len(more) == 0 {
		return rows
	}
	seen := make(map[string]struct{}, len(rows))
	for _, r := range rows {
		seen[id(r)] = struct{}{}
	}
	for _, r := range more {
		if _, ok := seen[id(r)]; !ok {
			rows = append(rows, r)
		}
	}
	return rows
}

// RateLimitsByTeams reads the rate limits of these teams.
func RateLimitsByTeams(ctx context.Context, db *gorm.DB, teamIDs []string) ([]*RateLimit, error) {
	return readRateLimits(ctx, db, func(q *gorm.DB) *gorm.DB {
		return q.Where("id IN (SELECT rate_limit_id FROM governance_teams WHERE id IN ?)", teamIDs)
	})
}

// RateLimitsByCustomers reads the rate limits of these customers.
func RateLimitsByCustomers(ctx context.Context, db *gorm.DB, customerIDs []string) ([]*RateLimit, error) {
	return readRateLimits(ctx, db, func(q *gorm.DB) *gorm.DB {
		return q.Where("id IN (SELECT rate_limit_id FROM governance_customers WHERE id IN ?)", customerIDs)
	})
}

// RateLimitsByModelConfigs reads the rate limits of these model configs.
func RateLimitsByModelConfigs(ctx context.Context, db *gorm.DB, modelConfigIDs []string) ([]*RateLimit, error) {
	return readRateLimits(ctx, db, func(q *gorm.DB) *gorm.DB {
		return q.Where("id IN (SELECT rate_limit_id FROM governance_model_configs WHERE id IN ?)", modelConfigIDs)
	})
}

// RateLimitsByProviders reads the rate limits these providers' rows name.
func RateLimitsByProviders(ctx context.Context, db *gorm.DB, names []string) ([]*RateLimit, error) {
	return readRateLimits(ctx, db, func(q *gorm.DB) *gorm.DB {
		return q.Where("id IN (SELECT rate_limit_id FROM config_providers WHERE name IN ?)", names)
	})
}

// readRateLimits loads the rate limit rows where selects and finds each one's holder and its
// calendar flag. Holders name a rate limit by its ID, so the owners read finds them; a rate limit
// two holders name takes the first owner in rateLimitOwners' order, and among holders of one owner
// the one with the lowest ID.
func readRateLimits(ctx context.Context, db *gorm.DB, where func(*gorm.DB) *gorm.DB) ([]*RateLimit, error) {
	var rows []tables.TableRateLimit
	if err := where(db.WithContext(ctx)).Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(rows))
	for i := range rows {
		ids = append(ids, rows[i].ID)
	}
	found, err := readHolders(ctx, db, rateLimitOwners, ids)
	if err != nil {
		return nil, err
	}
	out := make([]*RateLimit, 0, len(rows))
	for i := range rows {
		r := &RateLimit{TableRateLimit: rows[i]}
		for owner, byLimit := range found {
			h, ok := byLimit[r.ID]
			if !ok {
				continue
			}
			switch rateLimitOwners[owner].kind {
			case holderKey, holderProviderConfig:
				r.KeyID = h.HolderID
			case holderTeam:
				r.TeamID = h.HolderID
			case holderCustomer:
				r.CustomerID = h.HolderID
			case holderModelConfig:
				r.ModelConfigID = h.HolderID
			case holderProvider:
				r.Provider = h.HolderID
			}
			r.IsCalendarAligned = h.CalendarAligned
			break
		}
		out = append(out, r)
	}
	return out, nil
}

// LimitOwner is one way a holder holds a budget or rate limit, as the loader reads it: From is the
// table carrying the limit's ID, with an alias; Column is the column of From that holds the limit's
// ID; Joins reach the holder, aliased o, from From (empty when From is the holder); Calendar is the
// holder's calendar flag column, empty for a holder without one, whose limits reset on rolling
// windows. Every owner of the limits a read loads is read in one query.
type LimitOwner struct {
	From, Column, Joins, Calendar string

	holder string     // the holder's ID column, for an owner whose holder a record names
	kind   holderKind // which holder field of a record this owner fills
}

// holderKind is which holder field of a record an owner fills. The zero kind fills none.
type holderKind int

const (
	holderKey holderKind = iota + 1
	holderProviderConfig
	holderTeam
	holderCustomer
	holderModelConfig
	holderProvider
)

// budgetOwners are the holders the registry knows of a budget. A budget names its holder on its own
// row, so each owner starts from the budget; a provider names its budget on the provider's row.
var budgetOwners = []LimitOwner{
	{From: "governance_budgets l", Column: "l.id", Joins: "JOIN governance_virtual_keys o ON o.id = l.virtual_key_id", Calendar: "o.calendar_aligned", holder: "o.id", kind: holderKey},
	{From: "governance_budgets l", Column: "l.id", Joins: "JOIN governance_virtual_key_provider_configs pc ON pc.id = l.provider_config_id JOIN governance_virtual_keys o ON o.id = pc.virtual_key_id", Calendar: "o.calendar_aligned", holder: "o.id", kind: holderProviderConfig},
	{From: "governance_budgets l", Column: "l.id", Joins: "JOIN governance_teams o ON o.id = l.team_id", Calendar: "o.calendar_aligned", holder: "o.id", kind: holderTeam},
	{From: "governance_budgets l", Column: "l.id", Joins: "JOIN governance_customers o ON o.id = l.customer_id", Calendar: "o.calendar_aligned", holder: "o.id", kind: holderCustomer},
	{From: "governance_budgets l", Column: "l.id", Joins: "JOIN governance_model_configs o ON o.id = l.model_config_id", Calendar: "o.calendar_aligned", holder: "o.id", kind: holderModelConfig},
	{From: "config_providers o", Column: "o.budget_id", holder: "o.name", kind: holderProvider},
}

// rateLimitOwners are the holders the registry knows of a rate limit, in the order a rate limit two
// of them name picks its holder. Each names its rate limit by ID; a provider config's holder is its
// key.
var rateLimitOwners = []LimitOwner{
	{From: "governance_virtual_keys o", Column: "o.rate_limit_id", Calendar: "o.calendar_aligned", holder: "o.id", kind: holderKey},
	{From: "governance_virtual_key_provider_configs pc", Column: "pc.rate_limit_id", Joins: "JOIN governance_virtual_keys o ON o.id = pc.virtual_key_id", Calendar: "o.calendar_aligned", holder: "o.id", kind: holderProviderConfig},
	{From: "governance_teams o", Column: "o.rate_limit_id", Calendar: "o.calendar_aligned", holder: "o.id", kind: holderTeam},
	{From: "governance_customers o", Column: "o.rate_limit_id", Calendar: "o.calendar_aligned", holder: "o.id", kind: holderCustomer},
	{From: "governance_model_configs o", Column: "o.rate_limit_id", Calendar: "o.calendar_aligned", holder: "o.id", kind: holderModelConfig},
	{From: "config_providers o", Column: "o.rate_limit_id", holder: "o.name", kind: holderProvider},
}

// ownerIndex is the index in owners of the owner of kind, -1 when owners has none.
func ownerIndex(owners []LimitOwner, kind holderKind) int {
	for i, o := range owners {
		if o.kind == kind {
			return i
		}
	}
	return -1
}

// limitHolder is one row the owners read returns: a limit, the index of the owner that holds it,
// the holder's ID ("" for an owner whose holder no record names) and its calendar flag.
type limitHolder struct {
	LimitID         string
	OwnerIndex      int
	HolderID        string
	CalendarAligned bool
}

// readHolders reads, in one query, the holders each of owners has among the limits ids names, and
// returns them per owner, keyed by limit ID. Rows come ordered by owner and then holder ID, so a
// limit several holders of one owner name keeps the one with the lowest ID. The ID set binds as one
// argument per owner, so a page of any size stays within the statement parameter limit. No IDs
// reads nothing.
func readHolders(ctx context.Context, db *gorm.DB, owners []LimitOwner, ids []string) ([]map[string]limitHolder, error) {
	found := make([]map[string]limitHolder, len(owners))
	for i := range found {
		found[i] = make(map[string]limitHolder)
	}
	if len(ids) == 0 || len(owners) == 0 {
		return found, nil
	}
	parts := make([]string, 0, len(owners))
	args := make([]any, 0, len(owners))
	for i, o := range owners {
		holder := o.holder
		if holder == "" {
			holder = "''"
		}
		calendar := o.Calendar
		if calendar == "" {
			calendar = "FALSE"
		}
		cond, arg := queryscope.InStrings(db, o.Column, ids)
		parts = append(parts, fmt.Sprintf("SELECT %s AS limit_id, %d AS owner_index, %s AS holder_id, %s AS calendar_aligned FROM %s %s WHERE %s",
			o.Column, i, holder, calendar, o.From, o.Joins, cond))
		args = append(args, arg)
	}
	var rows []limitHolder
	if err := db.WithContext(ctx).Raw(strings.Join(parts, " UNION ALL ")+" ORDER BY owner_index, holder_id", args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, h := range rows {
		if h.OwnerIndex < 0 || h.OwnerIndex >= len(found) {
			continue
		}
		if _, taken := found[h.OwnerIndex][h.LimitID]; !taken {
			found[h.OwnerIndex][h.LimitID] = h
		}
	}
	return found, nil
}

// deref is the string p points to, "" for nil.
func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// NumericID is the record ID of a row whose primary key is a number: the number zero-padded to 20
// digits, the width of the largest uint64, so record IDs sort as the numbers do and a List in ID
// order is creation order. Every kind keyed by a numeric row ID uses it.
func NumericID(id uint) string {
	// Digits written right to left into a fixed buffer: one allocation (the string), where Sprintf
	// boxed the number and formatted through reflection on every lookup by numeric ID.
	var buf [20]byte
	for i := len(buf) - 1; i >= 0; i-- {
		buf[i] = byte('0' + id%10)
		id /= 10
	}
	return string(buf[:])
}
