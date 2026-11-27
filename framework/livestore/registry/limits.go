package registry

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/queryscope"
	"gorm.io/gorm"
)

// Budget is the record of one budget: the row's columns, embedded, and the holder the loader found
// for it. When a virtual key (directly or through one of its provider configs), a
// team, a customer, a model config or a provider holds it, the record names that holder and its
// IsCalendarAligned is the holder's flag, since a budget's reset window follows its holder's flag.
// A budget no holder here names is loaded too, with no holder, as every budget row always has been;
// its flag is on when an owner a build adds (LimitReads) holds it with its own flag on. Its usage columns are what
// the row held when it was read: the governance store keeps live usage and takes them only for a
// budget it has not seen. When an owner a build adds with a Parent holds it, the record also names
// that holder (LimitLink), whatever other holder it has.
type Budget struct {
	tables.TableBudget
	KeyID    string // the virtual key holding it, directly or through one of its provider configs
	Provider string // the provider whose row names it
	parent   limitParent
}

// RateLimit is the record of one rate limit: the row's columns, embedded, and its holder. Its holder
// names it by ID, so the loader finds the holder by
// looking for that ID: a virtual key (directly or through one of its provider configs), a team, a
// customer, a model config or a provider, whose flag becomes its IsCalendarAligned. A rate limit
// no holder here names is loaded too, with no holder, and its flag set as Budget's is. Its usage
// columns are treated as Budget's are, and so is a holder an owner a build adds with a Parent.
type RateLimit struct {
	tables.TableRateLimit
	KeyID, TeamID, CustomerID, ModelConfigID, Provider string
	parent                                             limitParent
}

// limitParent is the holder of a limit among the build's own records: owner is one more than the
// index, among the build's owners of the limit's kind, of the owner that found it (0 when none
// did), and id is the holder's record ID.
type limitParent struct {
	owner int
	id    string
}

// BudgetID is a budget record's primary key.
func BudgetID(b *Budget) string { return b.ID }

// BudgetHeld reports whether a holder the registry knows holds b: a virtual key or one of its
// provider configs, a team, a customer, a model config or a provider.
func BudgetHeld(b *Budget) bool {
	return b.VirtualKeyID != nil || b.ProviderConfigID != nil || b.TeamID != nil ||
		b.CustomerID != nil || b.ModelConfigID != nil || b.Provider != ""
}

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
func (lr LimitReads) BudgetsPage(ctx context.Context, db *gorm.DB, after string, limit int) ([]*Budget, string, error) {
	rows, err := readBudgets(ctx, db, lr.BudgetOwners, func(q *gorm.DB) *gorm.DB {
		return q.Where("id > ?", after).Order("id").Limit(limit)
	})
	if err != nil || len(rows) < limit {
		return rows, "", err
	}
	return rows, rows[len(rows)-1].ID, nil
}

// BudgetsByID reads the budgets with these IDs; an ID with no row is left out.
func (lr LimitReads) BudgetsByID(ctx context.Context, db *gorm.DB, ids []string) ([]*Budget, error) {
	return readBudgets(ctx, db, lr.BudgetOwners, func(q *gorm.DB) *gorm.DB { return q.Where(queryscope.InStrings(q, "id", ids)) })
}

// BudgetsByKeys reads the budgets of these virtual keys, their provider configs' included. The keys'
// own budgets and their provider configs' are read in two queries: one query with OR across the
// two columns leaves Postgres no index to use, so it scanned every budget on each key write.
func (lr LimitReads) BudgetsByKeys(ctx context.Context, db *gorm.DB, keyIDs []string) ([]*Budget, error) {
	own, err := readBudgets(ctx, db, lr.BudgetOwners, func(q *gorm.DB) *gorm.DB { return q.Where(queryscope.InStrings(q, "virtual_key_id", keyIDs)) })
	if err != nil {
		return nil, err
	}
	viaConfigs, err := readBudgets(ctx, db, lr.BudgetOwners, func(q *gorm.DB) *gorm.DB {
		cond, arg := queryscope.InStrings(q, "virtual_key_id", keyIDs)
		return q.Where("provider_config_id IN (SELECT id FROM governance_virtual_key_provider_configs WHERE "+cond+")", arg)
	})
	if err != nil {
		return nil, err
	}
	return appendNew(own, viaConfigs, BudgetID), nil
}

// BudgetsByTeams reads the budgets of these teams.
func (lr LimitReads) BudgetsByTeams(ctx context.Context, db *gorm.DB, teamIDs []string) ([]*Budget, error) {
	return readBudgets(ctx, db, lr.BudgetOwners, func(q *gorm.DB) *gorm.DB { return q.Where(queryscope.InStrings(q, "team_id", teamIDs)) })
}

// BudgetsByCustomers reads the budgets of these customers.
func (lr LimitReads) BudgetsByCustomers(ctx context.Context, db *gorm.DB, customerIDs []string) ([]*Budget, error) {
	return readBudgets(ctx, db, lr.BudgetOwners, func(q *gorm.DB) *gorm.DB { return q.Where(queryscope.InStrings(q, "customer_id", customerIDs)) })
}

// BudgetsByModelConfigs reads the budgets of these model configs.
func (lr LimitReads) BudgetsByModelConfigs(ctx context.Context, db *gorm.DB, modelConfigIDs []string) ([]*Budget, error) {
	return readBudgets(ctx, db, lr.BudgetOwners, func(q *gorm.DB) *gorm.DB { return q.Where(queryscope.InStrings(q, "model_config_id", modelConfigIDs)) })
}

// BudgetsByProviders reads the budgets these providers' rows name.
func (lr LimitReads) BudgetsByProviders(ctx context.Context, db *gorm.DB, names []string) ([]*Budget, error) {
	return readBudgets(ctx, db, lr.BudgetOwners, func(q *gorm.DB) *gorm.DB {
		cond, arg := queryscope.InStrings(q, "name", names)
		return q.Where("id IN (SELECT budget_id FROM config_providers WHERE "+cond+")", arg)
	})
}

// readBudgets loads the budget rows where selects and finds each one's holder and its calendar
// flag. A budget names its holder on its own row; the owners read gives the holder's flag, a
// provider config's key and the name of a provider whose row names the budget. A budget none of
// them holds is calendar aligned when one of extra holds it with its flag on.
func readBudgets(ctx context.Context, db *gorm.DB, extra []LimitOwner, where func(*gorm.DB) *gorm.DB) ([]*Budget, error) {
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
	found, err := readHolders(ctx, db, slices.Concat(budgetOwners, extra), ids)
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
		if !BudgetHeld(b) {
			b.IsCalendarAligned = anyAligned(found[len(budgetOwners):], b.ID)
		}
		if b.parent, err = buildParent(found[len(budgetOwners):], extra, b.ID); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// RateLimitID is a rate limit record's primary key.
func RateLimitID(r *RateLimit) string { return r.ID }

// RateLimitHeld reports whether a holder the registry knows holds r, as BudgetHeld does for a
// budget.
func RateLimitHeld(r *RateLimit) bool {
	return r.KeyID != "" || r.TeamID != "" || r.CustomerID != "" || r.ModelConfigID != "" || r.Provider != ""
}

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
func (lr LimitReads) RateLimitsPage(ctx context.Context, db *gorm.DB, after string, limit int) ([]*RateLimit, string, error) {
	rows, err := readRateLimits(ctx, db, lr.RateLimitOwners, func(q *gorm.DB) *gorm.DB {
		return q.Where("id > ?", after).Order("id").Limit(limit)
	})
	if err != nil || len(rows) < limit {
		return rows, "", err
	}
	return rows, rows[len(rows)-1].ID, nil
}

// RateLimitsByID reads the rate limits with these IDs; an ID with no row is left out.
func (lr LimitReads) RateLimitsByID(ctx context.Context, db *gorm.DB, ids []string) ([]*RateLimit, error) {
	return readRateLimits(ctx, db, lr.RateLimitOwners, func(q *gorm.DB) *gorm.DB { return q.Where(queryscope.InStrings(q, "id", ids)) })
}

// RateLimitsByKeys reads the rate limits of these virtual keys, their provider configs' included.
// Like BudgetsByKeys it reads the two in separate queries, so each uses the primary key rather than
// scanning every rate limit.
func (lr LimitReads) RateLimitsByKeys(ctx context.Context, db *gorm.DB, keyIDs []string) ([]*RateLimit, error) {
	own, err := readRateLimits(ctx, db, lr.RateLimitOwners, func(q *gorm.DB) *gorm.DB {
		cond, arg := queryscope.InStrings(q, "id", keyIDs)
		return q.Where("id IN (SELECT rate_limit_id FROM governance_virtual_keys WHERE "+cond+")", arg)
	})
	if err != nil {
		return nil, err
	}
	viaConfigs, err := readRateLimits(ctx, db, lr.RateLimitOwners, func(q *gorm.DB) *gorm.DB {
		cond, arg := queryscope.InStrings(q, "virtual_key_id", keyIDs)
		return q.Where("id IN (SELECT rate_limit_id FROM governance_virtual_key_provider_configs WHERE "+cond+")", arg)
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

// RateLimitsByTeams reads the rate limits of these teams: the one each names, and the request
// limits each owns.
func (lr LimitReads) RateLimitsByTeams(ctx context.Context, db *gorm.DB, teamIDs []string) ([]*RateLimit, error) {
	return lr.rateLimitsByHolders(ctx, db, "governance_teams", "team_id", teamIDs)
}

// RateLimitsByCustomers reads the rate limits of these customers: the one each names, and the
// request limits each owns.
func (lr LimitReads) RateLimitsByCustomers(ctx context.Context, db *gorm.DB, customerIDs []string) ([]*RateLimit, error) {
	return lr.rateLimitsByHolders(ctx, db, "governance_customers", "customer_id", customerIDs)
}

// RateLimitsByModelConfigs reads the rate limits of these model configs: the one each names, and
// the request limits each owns.
func (lr LimitReads) RateLimitsByModelConfigs(ctx context.Context, db *gorm.DB, modelConfigIDs []string) ([]*RateLimit, error) {
	return lr.rateLimitsByHolders(ctx, db, "governance_model_configs", "model_config_id", modelConfigIDs)
}

// rateLimitsByHolders reads the rate limits holders of table name by ID, and the request limits
// whose ownerColumn names one of them. Like RateLimitsByKeys it reads the two in separate queries,
// so each uses an index rather than scanning every rate limit.
func (lr LimitReads) rateLimitsByHolders(ctx context.Context, db *gorm.DB, table, ownerColumn string, holderIDs []string) ([]*RateLimit, error) {
	named, err := readRateLimits(ctx, db, lr.RateLimitOwners, func(q *gorm.DB) *gorm.DB {
		cond, arg := queryscope.InStrings(q, "id", holderIDs)
		return q.Where("id IN (SELECT rate_limit_id FROM "+table+" WHERE "+cond+")", arg)
	})
	if err != nil {
		return nil, err
	}
	owned, err := readRateLimits(ctx, db, lr.RateLimitOwners, func(q *gorm.DB) *gorm.DB {
		return q.Where(queryscope.InStrings(q, ownerColumn, holderIDs))
	})
	if err != nil {
		return nil, err
	}
	return appendNew(named, owned, RateLimitID), nil
}

// RateLimitsByProviders reads the rate limits these providers' rows name.
func (lr LimitReads) RateLimitsByProviders(ctx context.Context, db *gorm.DB, names []string) ([]*RateLimit, error) {
	return readRateLimits(ctx, db, lr.RateLimitOwners, func(q *gorm.DB) *gorm.DB {
		cond, arg := queryscope.InStrings(q, "name", names)
		return q.Where("id IN (SELECT rate_limit_id FROM config_providers WHERE "+cond+")", arg)
	})
}

// readRateLimits loads the rate limit rows where selects and finds each one's holder and its
// calendar flag. Holders name a rate limit by its ID, so the owners read finds them; a rate limit
// two holders name takes the first owner in rateLimitOwners' order, and among holders of one owner
// the one with the lowest ID. A rate limit none of them holds takes its flag from extra, as
// readBudgets does.
func readRateLimits(ctx context.Context, db *gorm.DB, extra []LimitOwner, where func(*gorm.DB) *gorm.DB) ([]*RateLimit, error) {
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
	found, err := readHolders(ctx, db, slices.Concat(rateLimitOwners, extra), ids)
	if err != nil {
		return nil, err
	}
	out := make([]*RateLimit, 0, len(rows))
	for i := range rows {
		r := &RateLimit{TableRateLimit: rows[i]}
		for owner, byLimit := range found[:len(rateLimitOwners)] {
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
		if !RateLimitHeld(r) {
			r.IsCalendarAligned = anyAligned(found[len(rateLimitOwners):], r.ID)
		}
		if r.parent, err = buildParent(found[len(rateLimitOwners):], extra, r.ID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// LimitOwner is one way a holder holds a budget or rate limit, as the loader reads it: From is the
// table carrying the limit's ID, with an alias; Column is the column of From that holds the limit's
// ID; Joins reach the holder, aliased o, from From (empty when From is the holder); Calendar is the
// holder's calendar flag column, empty for a holder without one, whose limits reset on rolling
// windows; HolderID is the holder's ID column (of o or a table Joins reaches), empty for an owner
// whose holder no record names, and NumericHolder marks an integer one, whose records are keyed by
// NumericID. Every owner of the limits a read loads is read in one query: the registry's own and
// any a build adds through LimitReads. A limit several holders of one owner hold takes the one
// whose ID sorts first, as text for a numeric one.
//
// The registry's own owners fill the record's holder fields (KeyID, TeamID and the others) from
// HolderID, because a limit's row names its holder and readers look limits up by holder. An owner a
// build adds sets the calendar flag of a limit no registry owner holds, and when it names Parent,
// the kind of the build's own records that hold its limits, the record names its holder of that
// kind too: its limits are that kind's children (LimitLink), so a refresh of a holder re-reads the
// limits it holds and removing it removes them, as for the registry's holders. A limit several such
// owners hold names the first one's holder.
type LimitOwner struct {
	From, Column, Joins, Calendar, HolderID string
	NumericHolder                           bool
	Parent                                  string

	kind holderKind // which holder field of a record this owner fills, for the registry's own
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
	{From: "governance_budgets l", Column: "l.id", Joins: "JOIN governance_virtual_keys o ON o.id = l.virtual_key_id", Calendar: "o.calendar_aligned", HolderID: "o.id", kind: holderKey},
	{From: "governance_budgets l", Column: "l.id", Joins: "JOIN governance_virtual_key_provider_configs pc ON pc.id = l.provider_config_id JOIN governance_virtual_keys o ON o.id = pc.virtual_key_id", Calendar: "o.calendar_aligned", HolderID: "o.id", kind: holderProviderConfig},
	{From: "governance_budgets l", Column: "l.id", Joins: "JOIN governance_teams o ON o.id = l.team_id", Calendar: "o.calendar_aligned", HolderID: "o.id", kind: holderTeam},
	{From: "governance_budgets l", Column: "l.id", Joins: "JOIN governance_customers o ON o.id = l.customer_id", Calendar: "o.calendar_aligned", HolderID: "o.id", kind: holderCustomer},
	{From: "governance_budgets l", Column: "l.id", Joins: "JOIN governance_model_configs o ON o.id = l.model_config_id", Calendar: "o.calendar_aligned", HolderID: "o.id", kind: holderModelConfig},
	{From: "config_providers o", Column: "o.budget_id", HolderID: "o.name", kind: holderProvider},
}

// rateLimitOwners are the holders the registry knows of a rate limit, in the order a rate limit two
// of them name picks its holder. Each names its rate limit by ID; a provider config's holder is its
// key. A request limit names its one owner on its own row instead (team_id, customer_id or
// model_config_id), so those owners start from the rate limit, after every holder that names a rate
// limit by ID: a legacy combined row a holder names keeps that holder.
var rateLimitOwners = []LimitOwner{
	{From: "governance_virtual_keys o", Column: "o.rate_limit_id", Calendar: "o.calendar_aligned", HolderID: "o.id", kind: holderKey},
	{From: "governance_virtual_key_provider_configs pc", Column: "pc.rate_limit_id", Joins: "JOIN governance_virtual_keys o ON o.id = pc.virtual_key_id", Calendar: "o.calendar_aligned", HolderID: "o.id", kind: holderProviderConfig},
	{From: "governance_teams o", Column: "o.rate_limit_id", Calendar: "o.calendar_aligned", HolderID: "o.id", kind: holderTeam},
	{From: "governance_customers o", Column: "o.rate_limit_id", Calendar: "o.calendar_aligned", HolderID: "o.id", kind: holderCustomer},
	{From: "governance_model_configs o", Column: "o.rate_limit_id", Calendar: "o.calendar_aligned", HolderID: "o.id", kind: holderModelConfig},
	{From: "config_providers o", Column: "o.rate_limit_id", HolderID: "o.name", kind: holderProvider},
	{From: "governance_rate_limits l", Column: "l.id", Joins: "JOIN governance_teams o ON o.id = l.team_id", Calendar: "o.calendar_aligned", HolderID: "o.id", kind: holderTeam},
	{From: "governance_rate_limits l", Column: "l.id", Joins: "JOIN governance_customers o ON o.id = l.customer_id", Calendar: "o.calendar_aligned", HolderID: "o.id", kind: holderCustomer},
	{From: "governance_rate_limits l", Column: "l.id", Joins: "JOIN governance_model_configs o ON o.id = l.model_config_id", Calendar: "o.calendar_aligned", HolderID: "o.id", kind: holderModelConfig},
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

// LimitReads reads budgets and rate limits with their holders: the owners the registry knows and
// the ones a build adds for its own entities that hold limits. Its zero value reads the registry's
// owners only.
type LimitReads struct {
	BudgetOwners, RateLimitOwners []LimitOwner
}

// anyAligned reports whether a holder found for limit id under one of found is calendar aligned,
// whichever holder of an owner the record names.
func anyAligned(found []map[string]limitHolder, id string) bool {
	for _, byLimit := range found {
		if h, ok := byLimit[id]; ok && h.anyAligned {
			return true
		}
	}
	return false
}

// buildParent returns the holder of limit id that the first of owners with a Parent found, found
// being what readHolders read for owners. A numeric holder's ID becomes its record ID.
func buildParent(found []map[string]limitHolder, owners []LimitOwner, id string) (limitParent, error) {
	for i, byLimit := range found {
		if owners[i].Parent == "" {
			continue
		}
		h, ok := byLimit[id]
		if !ok || h.HolderID == "" {
			continue
		}
		holderID := h.HolderID
		if owners[i].NumericHolder {
			n, err := strconv.ParseUint(holderID, 10, 64)
			if err != nil {
				return limitParent{}, fmt.Errorf("holder %q of limit %s is not a number: %w", holderID, id, err)
			}
			holderID = NumericID(uint(n))
		}
		return limitParent{owner: i + 1, id: holderID}, nil
	}
	return limitParent{}, nil
}

// LimitLink is how the limits of one kind of a build's own records are that kind's children:
// Parent names the kind, ParentID gives the ID of the record of that kind holding a limit ("" when
// none does), and ChildIDs reads the IDs of the limits the records with these IDs hold, through
// every owner naming Parent. A store declares one ChildIDsOf link per LimitLink.
type LimitLink[T any] struct {
	Parent   string
	ParentID func(*T) string
	ChildIDs func(ctx context.Context, db *gorm.DB, parentIDs []string) ([]string, error)
}

// BudgetLinks returns one link per Parent the budget owners name, in the order first named.
func (lr LimitReads) BudgetLinks() ([]LimitLink[Budget], error) {
	return limitLinks(lr.BudgetOwners, func(b *Budget) limitParent { return b.parent })
}

// RateLimitLinks returns one link per Parent the rate limit owners name, in the order first named.
func (lr LimitReads) RateLimitLinks() ([]LimitLink[RateLimit], error) {
	return limitLinks(lr.RateLimitOwners, func(r *RateLimit) limitParent { return r.parent })
}

// limitLinks builds the links of the Parents owners name, parent giving a record's holder. An owner
// with Parent or NumericHolder but no HolderID, and owners of one Parent that disagree on
// NumericHolder, are errors.
func limitLinks[T any](owners []LimitOwner, parent func(*T) limitParent) ([]LimitLink[T], error) {
	var links []LimitLink[T]
	for i, o := range owners {
		if o.HolderID == "" && (o.Parent != "" || o.NumericHolder) {
			return nil, fmt.Errorf("livestore: limit owner %q names a Parent or NumericHolder without a HolderID", o.From)
		}
		if o.Parent == "" {
			continue
		}
		if first := slices.IndexFunc(owners[:i], func(p LimitOwner) bool { return p.Parent == o.Parent }); first >= 0 {
			if owners[first].NumericHolder != o.NumericHolder {
				return nil, fmt.Errorf("livestore: limit owners of %q disagree on NumericHolder", o.Parent)
			}
			continue
		}
		kind := o.Parent
		links = append(links, LimitLink[T]{
			Parent: kind,
			ParentID: func(rec *T) string {
				p := parent(rec)
				if p.owner == 0 || owners[p.owner-1].Parent != kind {
					return ""
				}
				return p.id
			},
			ChildIDs: func(ctx context.Context, db *gorm.DB, parentIDs []string) ([]string, error) {
				return readChildIDs(ctx, db, owners, kind, parentIDs)
			},
		})
	}
	return links, nil
}

// readChildIDs reads, in one query, the IDs of the limits the holders with record IDs parentIDs
// hold through every one of owners naming parent. A numeric holder's record IDs are read as the
// numbers they stand for. The ID set binds as one argument per owner.
func readChildIDs(ctx context.Context, db *gorm.DB, owners []LimitOwner, parent string, parentIDs []string) ([]string, error) {
	if len(parentIDs) == 0 {
		return nil, nil
	}
	var numbers []uint
	var parts []string
	var args []any
	for _, o := range owners {
		if o.Parent != parent {
			continue
		}
		var cond string
		var arg any
		if o.NumericHolder {
			if numbers == nil {
				numbers = make([]uint, 0, len(parentIDs))
				for _, id := range parentIDs {
					n, err := strconv.ParseUint(id, 10, 64)
					if err != nil {
						return nil, fmt.Errorf("%s record ID %q is not a number: %w", parent, id, err)
					}
					numbers = append(numbers, uint(n))
				}
			}
			cond, arg = queryscope.InUints(db, o.HolderID, numbers)
		} else {
			cond, arg = queryscope.InStrings(db, o.HolderID, parentIDs)
		}
		parts = append(parts, fmt.Sprintf("SELECT %s AS id FROM %s %s WHERE %s AND %s IS NOT NULL", o.Column, o.From, o.Joins, cond, o.Column))
		args = append(args, arg)
	}
	var ids []string
	if err := db.WithContext(ctx).Raw(strings.Join(parts, " UNION ALL "), args...).Scan(&ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// limitHolder is one row the owners read returns: a limit, the index of the owner that holds it,
// the holder's ID ("" for an owner whose holder no record names) and its calendar flag. readHolders
// sets anyAligned when any holder of that owner holding the limit is aligned, not only this one.
type limitHolder struct {
	LimitID         string
	OwnerIndex      int
	HolderID        string
	CalendarAligned bool
	anyAligned      bool
}

// readHolders reads, in one query, the holders each of owners has among the limits ids names, and
// returns them per owner, keyed by limit ID. Rows come ordered by owner, holder ID and then flag,
// aligned first, so a limit several holders of one owner name keeps the one with the lowest ID, and
// for an owner that reads no holder ID, an aligned holder when it has one; whether any of them is
// aligned is kept beside it (anyAligned), for the owners a build adds. The ID set binds as one
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
		holder := o.HolderID
		switch {
		case holder == "":
			holder = "''"
		case o.NumericHolder:
			// As text, so a numeric holder's column unions with the others on Postgres.
			holder = "CAST(" + holder + " AS TEXT)"
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
	if err := db.WithContext(ctx).Raw(strings.Join(parts, " UNION ALL ")+" ORDER BY owner_index, holder_id, calendar_aligned DESC", args...).Scan(&rows).Error; err != nil {
		return nil, err
	}
	for _, h := range rows {
		if h.OwnerIndex < 0 || h.OwnerIndex >= len(found) {
			continue
		}
		kept, taken := found[h.OwnerIndex][h.LimitID]
		if !taken {
			h.anyAligned = h.CalendarAligned
			found[h.OwnerIndex][h.LimitID] = h
		} else if h.CalendarAligned && !kept.anyAligned {
			kept.anyAligned = true
			found[h.OwnerIndex][h.LimitID] = kept
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
