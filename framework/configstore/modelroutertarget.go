package configstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/queryscope"
	"gorm.io/gorm"
)

// maxModelRouterTTFTTimeoutMs caps a target's first-token deadline, as routing
// rules cap theirs.
const maxModelRouterTTFTTimeoutMs = 300000

// ErrInvalidModelRouterTargets is matched by the error SetModelRouterTargets
// returns when it refuses the targets themselves, so a caller can tell a bad
// request from a failed write. Only the store knows the mode the targets are
// checked against when the stored router cannot otherwise be read.
var ErrInvalidModelRouterTargets = errors.New("invalid model router targets")

// modelRouterTargetsError marks a targets check's error as
// ErrInvalidModelRouterTargets without changing its message, which callers
// send to the client as it is.
type modelRouterTargetsError struct{ err error }

// Error returns the check's own message.
func (e *modelRouterTargetsError) Error() string { return e.err.Error() }

// Unwrap returns the check's error.
func (e *modelRouterTargetsError) Unwrap() error { return e.err }

// Is reports a match for ErrInvalidModelRouterTargets.
func (e *modelRouterTargetsError) Is(target error) bool {
	return target == ErrInvalidModelRouterTargets
}

// ValidateModelRouterTargets checks each target of a router in mode and, per
// tier, that no two targets are the same and that their weights sum to 1. A
// classifier router's targets each name a tier; a conditional router's name
// none and are checked as one group. It upper-cases each tier and stores a
// first-token deadline of 0, which means none, as nil, both in place. No
// targets is valid: a classifier router then has no model for any tier, and a
// conditional router passes every request on.
func ValidateModelRouterTargets(mode string, targets []tables.TableModelRouterTarget) error {
	conditional := tables.NormalizeModelRouterMode(mode) == tables.ModelRouterModeConditional
	totals := make(map[string]float64, 3)
	seen := make(map[string]struct{}, len(targets))
	for i := range targets {
		t := &targets[i]
		t.Tier = strings.ToUpper(strings.TrimSpace(t.Tier))
		switch {
		case conditional && t.Tier != "":
			return fmt.Errorf("targets[%d]: a conditional router's targets take no tier, got %q", i, t.Tier)
		case !conditional && !tables.IsModelRouterTier(t.Tier):
			return fmt.Errorf("targets[%d]: tier must be SIMPLE, MEDIUM or COMPLEX, got %q", i, t.Tier)
		}
		provider, model, keyID := strings.TrimSpace(derefStr(t.Provider)), strings.TrimSpace(derefStr(t.Model)), strings.TrimSpace(derefStr(t.KeyID))
		if keyID != "" && provider == "" {
			return fmt.Errorf("targets[%d]: key_id requires provider to be set", i)
		}
		// A config.json target may name its key instead; the loader resolves the
		// name to a key_id before the targets are stored. Until then the name
		// tells two targets on the same provider and model apart.
		if keyName := strings.TrimSpace(derefStr(t.ProviderKeyName)); keyName != "" {
			if keyID != "" {
				return fmt.Errorf("targets[%d]: key_id cannot be set together with provider_key_name", i)
			}
			if provider == "" {
				return fmt.Errorf("targets[%d]: provider_key_name requires provider to be set", i)
			}
			keyID = "name:" + keyName
		}
		if !(t.Weight > 0) {
			return fmt.Errorf("targets[%d]: weight must be greater than 0, got %v", i, t.Weight)
		}
		if t.TTFTTimeoutMs != nil {
			if *t.TTFTTimeoutMs < 0 || *t.TTFTTimeoutMs > maxModelRouterTTFTTimeoutMs {
				return fmt.Errorf("targets[%d]: ttft_timeout_ms must be between 1 and %d (0 disables it)", i, maxModelRouterTTFTTimeoutMs)
			}
			if *t.TTFTTimeoutMs == 0 {
				t.TTFTTimeoutMs = nil
			}
		}
		for j, fb := range t.ParsedFallbacks {
			if p := strings.TrimSpace(string(fb.Provider)); p == "" || !schemas.IsKnownProvider(p) {
				return fmt.Errorf("targets[%d].fallbacks[%d] %q must use a known provider prefix", i, j, fb.String())
			}
		}
		identity := t.Tier + "|" + strings.ToLower(provider) + "|" + strings.ToLower(model) + "|" + keyID
		if _, dup := seen[identity]; dup {
			if conditional {
				return fmt.Errorf("targets[%d]: duplicate target: provider=%q model=%q key_id=%q", i, provider, model, keyID)
			}
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
			if conditional {
				return fmt.Errorf("target weights must sum to 1, got %.4f", totals[tier])
			}
			return fmt.Errorf("target weights for tier %s must sum to 1, got %.4f", tier, totals[tier])
		}
	}
	return nil
}

// writeModelRouterTargetsHash writes a router's targets into its config.json
// hash in a canonical order, so reordering them in the file changes nothing.
// A fallback that pins a key, by key_id or provider_key_name, is written in
// its JSON form so the pin is part of the hash; an unpinned one is written as
// its "provider/model" string. A first-token deadline is written only when a
// target sets one, so the hash of an entry written before targets had one is
// unchanged.
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
		fields := []string{
			strings.ToUpper(strings.TrimSpace(t.Tier)),
			strings.TrimSpace(derefStr(t.Provider)),
			strings.TrimSpace(derefStr(t.Model)),
			strings.TrimSpace(derefStr(t.KeyID)),
			strconv.FormatFloat(t.Weight, 'g', -1, 64),
			strings.Join(fallbacks, ","),
		}
		if t.TTFTTimeoutMs != nil && *t.TTFTTimeoutMs > 0 {
			fields = append(fields, "ttft="+strconv.Itoa(*t.TTFTTimeoutMs))
		}
		lines = append(lines, strings.Join(fields, "|"))
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
// one damaged row affects only its own router. The router ids bind as one
// argument, so a read of any number of routers stays within the statement
// parameter limit.
func loadModelRouterTargets(ctx context.Context, db *gorm.DB, routerIDs ...string) (targets map[string][]tables.TableModelRouterTarget, unreadable map[string]error, err error) {
	query := db.WithContext(ctx).
		Order("tier ASC").
		Order("weight DESC").
		Order("COALESCE(provider, '') ASC").
		Order("COALESCE(model, '') ASC").
		Order("COALESCE(key_id, '') ASC")
	if len(routerIDs) > 0 {
		query = query.Where(queryscope.InStrings(db, "router_id", routerIDs))
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

// SetModelRouterTargets replaces a router's targets: the model each tier of a
// classifier router gets, or a conditional router's one target list. The
// router must have been saved: ErrNotFound otherwise. The targets are checked
// against the stored router's mode, under its row lock, which also keeps a
// concurrent DeleteModelRouter from leaving targets behind; targets that do
// not pass are refused with an error matching ErrInvalidModelRouterTargets.
func (s *RDBConfigStore) SetModelRouterTargets(ctx context.Context, routerID string, targets []tables.TableModelRouterTarget, tx ...*gorm.DB) error {
	routerID = strings.TrimSpace(routerID)
	return s.inModelRouterTx(ctx, tx, func(txDB *gorm.DB) error {
		row, err := s.lockModelRouterRow(ctx, txDB, routerID)
		if err != nil {
			return err
		}
		if row == nil {
			return ErrNotFound
		}
		if err := ValidateModelRouterTargets(row.Mode, targets); err != nil {
			return &modelRouterTargetsError{err: err}
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
