package configstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"gorm.io/gorm"
)

// ValidateModelRouterTargets checks each target and, per tier, that no two
// targets are the same and that their weights sum to 1. It upper-cases each
// tier in place. No targets is valid: every tier then keeps the request's
// model.
func ValidateModelRouterTargets(targets []tables.TableModelRouterTarget) error {
	totals := make(map[string]float64, 3)
	seen := make(map[string]struct{}, len(targets))
	for i := range targets {
		t := &targets[i]
		t.Tier = strings.ToUpper(strings.TrimSpace(t.Tier))
		if !tables.IsModelRouterTier(t.Tier) {
			return fmt.Errorf("targets[%d]: tier must be SIMPLE, MEDIUM or COMPLEX, got %q", i, t.Tier)
		}
		provider, model, keyID := strings.TrimSpace(derefStr(t.Provider)), strings.TrimSpace(derefStr(t.Model)), strings.TrimSpace(derefStr(t.KeyID))
		if provider == "" && model == "" {
			return fmt.Errorf("targets[%d]: a target must set a provider, a model or both", i)
		}
		if keyID != "" && provider == "" {
			return fmt.Errorf("targets[%d]: key_id requires provider to be set", i)
		}
		if !(t.Weight > 0) {
			return fmt.Errorf("targets[%d]: weight must be greater than 0, got %v", i, t.Weight)
		}
		for j, fb := range t.ParsedFallbacks {
			if p := strings.TrimSpace(string(fb.Provider)); p == "" || !schemas.IsKnownProvider(p) {
				return fmt.Errorf("targets[%d].fallbacks[%d] %q must use a known provider prefix", i, j, fb.String())
			}
		}
		identity := t.Tier + "|" + strings.ToLower(provider) + "|" + strings.ToLower(model) + "|" + keyID
		if _, dup := seen[identity]; dup {
			return fmt.Errorf("targets[%d]: duplicate target for tier %s: provider=%q model=%q key_id=%q", i, t.Tier, provider, model, keyID)
		}
		seen[identity] = struct{}{}
		totals[t.Tier] += t.Weight
	}
	tiers := make([]string, 0, len(totals))
	for tier := range totals {
		tiers = append(tiers, tier)
	}
	sort.Strings(tiers)
	for _, tier := range tiers {
		if math.Abs(totals[tier]-1.0) > 0.001 {
			return fmt.Errorf("target weights for tier %s must sum to 1, got %.4f", tier, totals[tier])
		}
	}
	return nil
}

// writeModelRouterTargetsHash writes a router's targets into its config.json
// hash in a canonical order, so reordering them in the file changes nothing.
// A fallback that pins a key, by key_id or provider_key_name, is written in
// its JSON form so the pin is part of the hash; an unpinned one is written as
// its "provider/model" string.
func writeModelRouterTargetsHash(h hash.Hash, targets []tables.TableModelRouterTarget) {
	lines := make([]string, 0, len(targets))
	for _, t := range targets {
		fallbacks := make([]string, 0, len(t.ParsedFallbacks))
		for _, fb := range t.ParsedFallbacks {
			entry := fb.String()
			if fb.IsKeyPinned() {
				if data, err := fb.MarshalJSON(); err == nil {
					entry = string(data)
				}
			}
			fallbacks = append(fallbacks, entry)
		}
		lines = append(lines, strings.Join([]string{
			strings.ToUpper(strings.TrimSpace(t.Tier)),
			strings.TrimSpace(derefStr(t.Provider)),
			strings.TrimSpace(derefStr(t.Model)),
			strings.TrimSpace(derefStr(t.KeyID)),
			strconv.FormatFloat(t.Weight, 'g', -1, 64),
			strings.Join(fallbacks, ","),
		}, "|"))
	}
	sort.Strings(lines)
	writeHashField(h, "targets", strings.Join(lines, "\n"))
}

// ModelRouterTargetsHash identifies a set of targets regardless of their
// order, so config.json sync can tell whether the file's targets differ from
// the stored ones.
func ModelRouterTargetsHash(targets []tables.TableModelRouterTarget) string {
	h := sha256.New()
	writeModelRouterTargetsHash(h, targets)
	return hex.EncodeToString(h.Sum(nil))
}

// loadModelRouterTargets reads the targets of the given routers, or of every
// router when none are named, keyed by router id. Targets are grouped by tier,
// then by weight DESC and provider, model and key, so every read lists them
// the same way. A router with a target this version cannot decode is left out
// of the targets and reported in unreadable, wrapping ErrConfigUnreadable, so
// one damaged row affects only its own router.
func loadModelRouterTargets(ctx context.Context, db *gorm.DB, routerIDs ...string) (targets map[string][]tables.TableModelRouterTarget, unreadable map[string]error, err error) {
	query := db.WithContext(ctx).
		Order("tier ASC").
		Order("weight DESC").
		Order("COALESCE(provider, '') ASC").
		Order("COALESCE(model, '') ASC").
		Order("COALESCE(key_id, '') ASC")
	if len(routerIDs) > 0 {
		query = query.Where("router_id IN ?", routerIDs)
	}
	var rows []tables.TableModelRouterTarget
	if err := query.Find(&rows).Error; err != nil {
		return nil, nil, err
	}
	targets = make(map[string][]tables.TableModelRouterTarget)
	unreadable = make(map[string]error)
	for _, row := range rows {
		if unreadable[row.RouterID] != nil {
			continue
		}
		if err := row.DecodeFallbacks(); err != nil {
			unreadable[row.RouterID] = fmt.Errorf("%w: a target of model router %q has fallbacks that cannot be read: %w", ErrConfigUnreadable, row.RouterID, err)
			delete(targets, row.RouterID)
			continue
		}
		targets[row.RouterID] = append(targets[row.RouterID], row)
	}
	return targets, unreadable, nil
}

// SetModelRouterTargets replaces a router's targets, the model each tier of
// the router gets. The router must have been saved: ErrNotFound otherwise.
// The write holds the router's row lock, so a concurrent DeleteModelRouter
// cannot leave targets behind.
func (s *RDBConfigStore) SetModelRouterTargets(ctx context.Context, routerID string, targets []tables.TableModelRouterTarget, tx ...*gorm.DB) error {
	routerID = strings.TrimSpace(routerID)
	if err := ValidateModelRouterTargets(targets); err != nil {
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
		return writeModelRouterTargets(ctx, txDB, routerID, targets)
	})
}

// writeModelRouterTargets replaces a router's targets inside the caller's
// transaction.
func writeModelRouterTargets(ctx context.Context, txDB *gorm.DB, routerID string, targets []tables.TableModelRouterTarget) error {
	if err := txDB.WithContext(ctx).Where("router_id = ?", routerID).Delete(&tables.TableModelRouterTarget{}).Error; err != nil {
		return err
	}
	for i := range targets {
		row := targets[i]
		row.RouterID = routerID
		if err := txDB.WithContext(ctx).Create(&row).Error; err != nil {
			return err
		}
	}
	return nil
}
