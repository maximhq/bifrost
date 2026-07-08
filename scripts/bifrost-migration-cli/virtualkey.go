package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/maximhq/bifrost/scripts/bifrost-migration-cli/litellm"
)

// VKProviderConfig is the planned per-provider key attachment for a VK.
// KeyIDs ["*"] grants all keys of that provider; specific UUIDs limit to those
// keys. AllowedModels is always ["*"] (no model restriction on the VK).
type VKProviderConfig struct {
	Provider string
	KeyIDs   []string // ["*"] = all keys; specific UUIDs = only those keys
}

// VKPlan is the planned Bifrost virtual key. Owner ids are LiteLLM ids; the
// caller resolves them to Bifrost team/customer ids (mutually exclusive) before
// the write. The remaining fields are informational for the migration report.
type VKPlan struct {
	Name            string
	OwnerTeamID     *string // LiteLLM team_id (resolve -> Bifrost team)
	OwnerOrgID      *string // LiteLLM org_id (resolve -> Bifrost customer)
	ProviderConfigs []VKProviderConfig
	Budget          *BifrostCreateBudgetRequest
	RateLimit       *BifrostCreateRateLimitRequest
	IsActive        *bool // set false when the LiteLLM key is blocked

	// Informational (reported, not carried onto the VK):
	SourceUserID   *string  // LiteLLM user_id — no VK-create field for it
	UnmappedModels []string // model names not found in the model index
	Expiry         *string  // LiteLLM expiry — no VK-create field for it
}

// llmAllModelSentinels are LiteLLM model-list tokens that mean "all models".
var llmAllModelSentinels = map[string]bool{
	"*": true, "all-proxy-models": true, "all-team-models": true,
}

// LiteLLMVirtualKeyToBifrost transforms a LiteLLM virtual key into a Bifrost VK
// plan. The mapping is pure (no I/O).
//
// Keys: the VK's allowed model list (union of key + team + org) is used to
// select which provider keys to attach. Keys whose model list matches are
// attached; wildcard keys (models=["*"]) are always included when any specific
// provider from that model is matched. When the union is empty or contains an
// "all models" sentinel, all providers are granted via key_ids=["*"].
// No model restriction is set on the VK — all models are always allowed.
//
// Owner: team_id wins over org_id (Bifrost VK ownership is team XOR customer);
// user_id has no VK-create field and is reported instead.
func LiteLLMVirtualKeyToBifrost(key litellm.LiteLLMVirtualKey, teamModels, orgModels []string, keyModelIdx map[string][]ProviderKeyRef, wildcardKeys []ProviderKeyRef, allProviders []string, maxBudgetPeriod string) (*VKPlan, error) {
	name := ""
	if key.KeyAlias != nil {
		name = strings.TrimSpace(*key.KeyAlias)
	}
	if name == "" && key.KeyName != nil {
		name = strings.TrimSpace(*key.KeyName)
	}
	if name == "" {
		return nil, fmt.Errorf("virtual key has no key_alias or key_name; a name is required")
	}

	plan := &VKPlan{Name: name, SourceUserID: key.UserID}

	if key.TeamID != nil && strings.TrimSpace(*key.TeamID) != "" {
		plan.OwnerTeamID = key.TeamID
	} else if key.OrgID != nil && strings.TrimSpace(*key.OrgID) != "" {
		plan.OwnerOrgID = key.OrgID
	}

	// Effective model set is the intersection of all non-empty, non-sentinel
	// allowed_models lists across the VK, team, and org layers, mirroring
	// LiteLLM's own enforcement: a request must satisfy every layer that has a
	// restriction.  A layer with an empty list or an "all models" sentinel
	// imposes no restriction and is skipped.
	names := intersectModelNames(key.Models, teamModels, orgModels)
	plan.ProviderConfigs, plan.UnmappedModels = resolveProviderKeyConfigs(names, keyModelIdx, wildcardKeys, allProviders)

	b := litellm.LiteLLMBudget{
		MaxBudget:      key.MaxBudget,
		BudgetDuration: key.BudgetDuration,
		TPMLimit:       key.TPMLimit,
		RPMLimit:       key.RPMLimit,
	}
	budget, err := toBudget(b, maxBudgetPeriod)
	if err != nil {
		return nil, fmt.Errorf("virtual key %q: %w", name, err)
	}
	plan.Budget = budget
	plan.RateLimit = toRateLimit(b)

	if key.Blocked != nil && *key.Blocked {
		inactive := false
		plan.IsActive = &inactive
	}

	if key.Expires != nil && strings.TrimSpace(*key.Expires) != "" {
		plan.Expiry = key.Expires
	}

	return plan, nil
}

// resolveProviderKeyConfigs turns a set of LiteLLM model names into Bifrost VK
// provider configs by selecting which provider keys serve those models.
//
// An empty set (no restriction) or an "all models" sentinel grants key_ids=["*"]
// on every provider in allProviders (allows all keys, all models).
//
// For a specific model list:
//   - Keys explicitly modelling those names are attached by UUID.
//   - Wildcard keys (models=["*"]) of any provider already matched are also
//     attached, since they can serve any request.
//   - Models that resolve to no key are returned as unmapped (reported, not granted).
//
// No AllowedModels restriction is set — all models are allowed on attached keys.
func resolveProviderKeyConfigs(names []string, keyModelIdx map[string][]ProviderKeyRef, wildcardKeys []ProviderKeyRef, allProviders []string) ([]VKProviderConfig, []string) {
	if len(names) == 0 || hasAllSentinel(names) {
		return allProvidersAllKeys(allProviders), nil
	}

	providerSet := make(map[string]bool, len(allProviders))
	for _, p := range allProviders {
		providerSet[p] = true
	}

	// perProvider accumulates UUIDs of keys to attach, keyed by provider.
	perProvider := map[string]map[string]bool{} // provider → set of keyIDs
	fullWildcard := map[string]bool{}            // provider → granted via "<provider>/*"
	var provOrder []string
	var unmapped []string

	ensureProvider := func(p string) {
		if perProvider[p] == nil {
			perProvider[p] = map[string]bool{}
			provOrder = append(provOrder, p)
		}
	}

	for _, n := range names {
		if n != "*" && trimModelPrefix(n) == "*" {
			provider, _, _ := strings.Cut(n, "/")
			if !providerSet[provider] {
				unmapped = append(unmapped, n)
				continue
			}
			ensureProvider(provider)
			fullWildcard[provider] = true
			continue
		}
		refs := keyModelIdx[n]
		if len(refs) == 0 {
			unmapped = append(unmapped, n)
			continue
		}
		for _, r := range refs {
			ensureProvider(r.Provider)
			perProvider[r.Provider][r.KeyID] = true
		}
	}
	// Attach wildcard keys only for providers already matched above.
	for _, wk := range wildcardKeys {
		if perProvider[wk.Provider] != nil {
			perProvider[wk.Provider][wk.KeyID] = true
		}
	}

	sort.Strings(provOrder)

	configs := make([]VKProviderConfig, 0, len(provOrder))
	for _, p := range provOrder {
		keyIDs := []string{"*"}
		if !fullWildcard[p] {
			keyIDs = keysSorted(perProvider[p])
		}
		configs = append(configs, VKProviderConfig{Provider: p, KeyIDs: keyIDs})
	}
	return configs, unmapped
}

func hasAllSentinel(names []string) bool {
	for _, n := range names {
		if llmAllModelSentinels[strings.TrimSpace(n)] {
			return true
		}
	}
	return false
}

func allProvidersAllKeys(providers []string) []VKProviderConfig {
	configs := make([]VKProviderConfig, 0, len(providers))
	for _, p := range providers {
		configs = append(configs, VKProviderConfig{Provider: p, KeyIDs: []string{"*"}})
	}
	return configs
}

// intersectModelNames returns the intersection of all non-empty, non-sentinel
// model lists. A list that is empty or contains only "all models" sentinels
// imposes no restriction at that level and is skipped entirely. If all levels
// are unrestricted, nil is returned (caller maps this to "all models").
func intersectModelNames(lists ...[]string) []string {
	var base map[string]bool
	for _, l := range lists {
		var active []string
		for _, m := range l {
			if t := strings.TrimSpace(m); t != "" {
				active = append(active, t)
			}
		}
		if len(active) == 0 || hasAllSentinel(active) {
			continue
		}
		if base == nil {
			base = make(map[string]bool, len(active))
			for _, m := range active {
				base[m] = true
			}
		} else {
			next := make(map[string]bool)
			for _, m := range active {
				if base[m] {
					next[m] = true
				}
			}
			base = next
		}
	}
	if len(base) == 0 {
		return nil
	}
	out := make([]string, 0, len(base))
	for m := range base {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

func keysSorted(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}