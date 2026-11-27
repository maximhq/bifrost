package registry

import (
	"context"
	"sort"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/queryscope"
	"gorm.io/gorm"
)

// RoutingRule is the record of one routing rule as the routing engine reads it: the row's columns,
// embedded, with its targets. Records are shared: readers never change them.
type RoutingRule struct {
	tables.TableRoutingRule
}

// RoutingRuleByScope lists the rules of one scope, by RoutingRuleScopeKey.
const RoutingRuleByScope = "scope"

// RoutingRuleID is a routing rule record's primary key.
func RoutingRuleID(r *RoutingRule) string { return r.ID }

// RoutingRuleScopeKey is the key of a scope in RoutingRuleByScope: the scope, and the entity it
// names, empty for the global scope.
func RoutingRuleScopeKey(scope, scopeID string) string {
	if scope == "global" {
		return "global:"
	}
	return scope + ":" + scopeID
}

// RoutingRuleScopes is a rule's key in RoutingRuleByScope, as the index takes it.
func RoutingRuleScopes(r *RoutingRule) []string {
	scopeID := ""
	if r.ScopeID != nil {
		scopeID = *r.ScopeID
	}
	return []string{RoutingRuleScopeKey(r.Scope, scopeID)}
}

// SortRoutingRules orders rules as the database reads them for evaluation: priority, then newest
// first, then ID.
func SortRoutingRules(rules []*RoutingRule) {
	sort.SliceStable(rules, func(i, j int) bool {
		a, b := &rules[i].TableRoutingRule, &rules[j].TableRoutingRule
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.After(b.CreatedAt)
		}
		return a.ID < b.ID
	})
}

// RoutingRulesPage reads up to limit rules with IDs after the cursor, in ID order.
func RoutingRulesPage(ctx context.Context, db *gorm.DB, after string, limit int) ([]*RoutingRule, string, error) {
	rows, err := readRoutingRules(ctx, db, func(q *gorm.DB) *gorm.DB {
		return q.Where("id > ?", after).Order("id").Limit(limit)
	})
	if err != nil || len(rows) < limit {
		return rows, "", err
	}
	return rows, RoutingRuleID(rows[len(rows)-1]), nil
}

// RoutingRulesByID reads the rules with these IDs, as a refresh of them does; one with no row is
// left out.
func RoutingRulesByID(ctx context.Context, db *gorm.DB, ids []string) ([]*RoutingRule, error) {
	return readRoutingRules(ctx, db, func(q *gorm.DB) *gorm.DB { return q.Where(queryscope.InStrings(q, "id", ids)) })
}

// readRoutingRules reads the rules where selects, unscoped, each with its targets.
func readRoutingRules(ctx context.Context, db *gorm.DB, where func(*gorm.DB) *gorm.DB) ([]*RoutingRule, error) {
	var rows []tables.TableRoutingRule
	if err := configstore.ReadRoutingRules(db.WithContext(ctx), &rows, where); err != nil {
		return nil, err
	}
	out := make([]*RoutingRule, 0, len(rows))
	for i := range rows {
		out = append(out, &RoutingRule{TableRoutingRule: rows[i]})
	}
	return out, nil
}
