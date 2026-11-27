package configstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"hash"
	"strconv"
	"strings"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
)

// ModelRouterRouting decides which requests a model router takes.
//
// Routers are tried after the routing rules, on the request as the routing
// rules left it. They are walked in the routing rules' scope order (virtual
// key, user, team, customer, global) and by priority within a scope, and the
// first enabled router whose condition holds classifies the request; no other
// router is tried. A router's routing is saved on its own, by
// SetModelRouterRouting, so saving its classifier never changes which requests
// reach it. The store does not compile CelExpression: callers check it with
// the routing plugin's rules package first.
type ModelRouterRouting struct {
	Enabled       bool           `json:"enabled"`
	Scope         string         `json:"scope"`                    // "global" | "team" | "customer" | "virtual_key" | "user"
	ScopeID       *string        `json:"scope_id,omitempty"`       // nil for global, otherwise the entity's id
	CelExpression string         `json:"cel_expression,omitempty"` // narrows the requests in scope; empty takes all; may not use complexity_tier
	Query         map[string]any `json:"query,omitempty"`          // the dashboard's condition builder state; never evaluated
	Priority      int            `json:"priority"`                 // lower is tried first within a scope
}

// ErrModelRouterPriorityTaken is returned when another router in the same
// scope and scope ID already has the priority. It wraps ErrAlreadyExists, as
// ErrRoutingRulePriorityTaken does.
var ErrModelRouterPriorityTaken = fmt.Errorf("%w", ErrAlreadyExists)

// validModelRouterScopes are the scopes a router can take, the same as
// routing rules'.
var validModelRouterScopes = map[string]bool{
	"global":      true,
	"team":        true,
	"customer":    true,
	"virtual_key": true,
	"user":        true,
}

// ValidateModelRouterRouting checks a router's routing on its own and
// normalizes it in place: an empty scope is global, a global scope drops its
// scope ID, and the scope ID and condition are trimmed. Whether the scope's
// entity exists, and whether the condition compiles, are the caller's checks.
func ValidateModelRouterRouting(routing *ModelRouterRouting) error {
	if routing == nil {
		return fmt.Errorf("model router routing is nil")
	}
	routing.Scope = strings.TrimSpace(routing.Scope)
	routing.CelExpression = strings.TrimSpace(routing.CelExpression)
	if routing.Scope == "" {
		routing.Scope = "global"
	}
	if !validModelRouterScopes[routing.Scope] {
		return fmt.Errorf("invalid scope %q: must be one of: global, team, customer, virtual_key, user", routing.Scope)
	}
	if routing.Scope == "global" {
		routing.ScopeID = nil
	} else {
		if routing.ScopeID == nil || strings.TrimSpace(*routing.ScopeID) == "" {
			return fmt.Errorf("scope_id is required when scope is not global")
		}
		routing.ScopeID = bifrost.Ptr(strings.TrimSpace(*routing.ScopeID))
	}
	if routing.Priority < 0 {
		return fmt.Errorf("priority must be 0 or greater, got %d", routing.Priority)
	}
	return nil
}

// writeModelRouterRoutingHash writes a router's routing into its config.json
// hash. The condition builder state is left out: it is the dashboard's, and
// never evaluated.
func writeModelRouterRoutingHash(h hash.Hash, routing ModelRouterRouting) {
	writeHashField(h, "routing.enabled", strconv.FormatBool(routing.Enabled))
	writeHashField(h, "routing.scope", routing.Scope)
	writeHashField(h, "routing.scope_id", derefStr(routing.ScopeID))
	writeHashField(h, "routing.cel_expression", routing.CelExpression)
	writeHashField(h, "routing.priority", strconv.Itoa(routing.Priority))
}

// setModelRouterRoutingColumns lays routing out on a router's row.
func setModelRouterRoutingColumns(row *tables.TableModelRouter, routing ModelRouterRouting) error {
	row.Enabled = routing.Enabled
	row.Scope = routing.Scope
	row.ScopeID = routing.ScopeID
	row.CelExpression = routing.CelExpression
	row.Query = nil
	if routing.Query != nil {
		data, err := json.Marshal(routing.Query)
		if err != nil {
			return fmt.Errorf("encode routing condition builder state: %w", err)
		}
		row.Query = bifrost.Ptr(string(data))
	}
	row.Priority = routing.Priority
	return nil
}

// modelRouterRoutingFromRow reads a router's routing back from its row. The
// condition builder state is the dashboard's, so a value this version cannot
// decode reads as none rather than making the router unreadable.
func modelRouterRoutingFromRow(row *tables.TableModelRouter) *ModelRouterRouting {
	routing := &ModelRouterRouting{
		Enabled:       row.Enabled,
		Scope:         row.Scope,
		ScopeID:       row.ScopeID,
		CelExpression: row.CelExpression,
		Priority:      row.Priority,
	}
	if routing.Scope == "" {
		routing.Scope = "global"
	}
	if row.Query != nil && strings.TrimSpace(*row.Query) != "" {
		var query map[string]any
		if err := json.Unmarshal([]byte(*row.Query), &query); err == nil {
			routing.Query = query
		}
	}
	return routing
}

// checkModelRouterPriority checks, inside the write's transaction, that no
// other router in the routing's scope and scope ID has its priority.
func checkModelRouterPriority(ctx context.Context, txDB *gorm.DB, routerID string, routing ModelRouterRouting) error {
	query := txDB.WithContext(ctx).Model(&tables.TableModelRouter{}).
		Where("scope = ? AND priority = ? AND id <> ?", routing.Scope, routing.Priority, routerID)
	if routing.ScopeID != nil {
		query = query.Where("scope_id = ?", *routing.ScopeID)
	} else {
		query = query.Where("scope_id IS NULL")
	}
	var taken int64
	if err := query.Count(&taken).Error; err != nil {
		return err
	}
	if taken == 0 {
		return nil
	}
	return checkModelRouterPriorityError(routing)
}

// checkModelRouterPriorityError is the error for a priority another router in
// the routing's scope already holds.
func checkModelRouterPriorityError(routing ModelRouterRouting) error {
	if routing.ScopeID != nil {
		return fmt.Errorf("model router with priority %d %w for scope '%s' with scope ID '%s'", routing.Priority, ErrModelRouterPriorityTaken, routing.Scope, *routing.ScopeID)
	}
	return fmt.Errorf("model router with priority %d %w for scope '%s'", routing.Priority, ErrModelRouterPriorityTaken, routing.Scope)
}

// defaultModelRouterRouting is the routing a router gets when it is created
// without any: off, global, and after every other global router, so turning
// it on never puts it ahead of a router already serving.
func defaultModelRouterRouting(ctx context.Context, txDB *gorm.DB) (ModelRouterRouting, error) {
	var last sql.NullInt64
	if err := txDB.WithContext(ctx).Model(&tables.TableModelRouter{}).
		Where("scope = ? AND scope_id IS NULL", "global").
		Select("MAX(priority)").Row().Scan(&last); err != nil {
		return ModelRouterRouting{}, err
	}
	priority := 0
	if last.Valid {
		priority = int(last.Int64) + 1
	}
	return ModelRouterRouting{Scope: "global", Priority: priority}, nil
}

// ModelRouterRoutingChange is one router's new routing in a
// SetModelRouterRoutings batch.
type ModelRouterRoutingChange struct {
	RouterID string
	Routing  ModelRouterRouting
}

// SetModelRouterRoutings replaces the routing of several saved routers in one
// transaction, as SyncRoutingRules writes routing rules: each routing is
// validated, the rows are written without the one-at-a-time priority check,
// and the check runs once on the final state of every scope the batch writes
// to, so routers can swap priorities. A duplicate in that final state is
// ErrModelRouterPriorityTaken and a router that does not exist ErrNotFound;
// either way nothing is written.
func (s *RDBConfigStore) SetModelRouterRoutings(ctx context.Context, changes []ModelRouterRoutingChange, tx ...*gorm.DB) error {
	if len(changes) == 0 {
		return nil
	}
	for i := range changes {
		changes[i].RouterID = strings.TrimSpace(changes[i].RouterID)
		if err := ValidateModelRouterRouting(&changes[i].Routing); err != nil {
			return fmt.Errorf("model router %q: %w", changes[i].RouterID, err)
		}
	}
	return s.inModelRouterTx(ctx, tx, func(txDB *gorm.DB) error {
		scopes := make(map[string]ModelRouterRouting, len(changes))
		for _, change := range changes {
			row, err := s.lockModelRouterRow(ctx, txDB, change.RouterID)
			if err != nil {
				return err
			}
			if row == nil {
				return fmt.Errorf("model router %q: %w", change.RouterID, ErrNotFound)
			}
			if err := setModelRouterRoutingColumns(row, change.Routing); err != nil {
				return err
			}
			if err := txDB.WithContext(ctx).Model(row).
				Select("enabled", "scope", "scope_id", "cel_expression", "query", "priority", "updated_at").
				Updates(row).Error; err != nil {
				return err
			}
			scopeID := ""
			if change.Routing.ScopeID != nil {
				scopeID = *change.Routing.ScopeID
			}
			scopes[change.Routing.Scope+"\x00"+scopeID] = change.Routing
		}
		for _, routing := range scopes {
			query := txDB.WithContext(ctx).Model(&tables.TableModelRouter{}).Where("scope = ?", routing.Scope)
			if routing.ScopeID != nil {
				query = query.Where("scope_id = ?", *routing.ScopeID)
			} else {
				query = query.Where("scope_id IS NULL")
			}
			var duplicates []int
			if err := query.Group("priority").Having("COUNT(*) > 1").Pluck("priority", &duplicates).Error; err != nil {
				return err
			}
			if len(duplicates) > 0 {
				taken := routing
				taken.Priority = duplicates[0]
				return checkModelRouterPriorityError(taken)
			}
		}
		return nil
	})
}

// SetModelRouterRouting replaces which requests a saved router takes. It
// validates the routing (see ValidateModelRouterRouting) and refuses a
// priority another router in the same scope holds with
// ErrModelRouterPriorityTaken; ErrNotFound when the router does not exist.
// It leaves the router's classifier and targets as they are.
func (s *RDBConfigStore) SetModelRouterRouting(ctx context.Context, routerID string, routing ModelRouterRouting, tx ...*gorm.DB) error {
	routerID = strings.TrimSpace(routerID)
	if err := ValidateModelRouterRouting(&routing); err != nil {
		return err
	}
	return s.inModelRouterTx(ctx, tx, func(txDB *gorm.DB) error {
		row, err := s.lockModelRouterRow(ctx, txDB, routerID)
		if err != nil {
			return err
		}
		if row == nil {
			return ErrNotFound
		}
		if err := checkModelRouterPriority(ctx, txDB, routerID, routing); err != nil {
			return err
		}
		if err := setModelRouterRoutingColumns(row, routing); err != nil {
			return err
		}
		// Selected by name: gorm's Updates skips zero values, and false, global
		// and priority 0 are real settings.
		return txDB.WithContext(ctx).Model(row).
			Select("enabled", "scope", "scope_id", "cel_expression", "query", "priority", "updated_at").
			Updates(row).Error
	})
}
