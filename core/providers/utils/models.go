// Package utils — list_models.go
// Centralised pipeline for filtering and backfilling models in ListModels responses.
//
// Every provider's ToBifrostListModelsResponse follows the same logical steps:
//  1. Resolve each API model's name (alias lookup → alias key; else raw model ID)
//  2. Filter (allowlist + blacklist check on the resolved name)
//  3. Backfill entries that were not returned by the API but should appear in output
//
// Providers plug in custom MatchFns to extend the default matching behaviour.
// Example: Bedrock adds region-prefix-aware matching on top of DefaultMatchFns.
package utils

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// ToDisplayName converts a raw model ID or alias key into a human-readable display name.
// Splits on "-" or "_", title-cases each word, and joins with spaces.
//
//	"gemini-pro"      → "Gemini Pro"
//	"claude_3_opus"   → "Claude 3 Opus"
//	"gpt-4-turbo"     → "Gpt 4 Turbo"
func ToDisplayName(id string) string {
	caser := cases.Title(language.English)
	var parts []string
	if strings.Contains(id, "-") {
		parts = strings.Split(id, "-")
	} else if strings.Contains(id, "_") {
		parts = strings.Split(id, "_")
	} else {
		return caser.String(strings.ToLower(id))
	}
	for i, part := range parts {
		if part != "" {
			parts[i] = caser.String(strings.ToLower(part))
		}
	}
	return strings.Join(parts, " ")
}

// MatchFn reports whether two model ID strings should be treated as equivalent.
// Functions are applied in order during every comparison — the first one that
// returns true short-circuits the rest.
//
// Example built-in fns (see DefaultMatchFns):
//
//	exactMatch("gpt-4", "gpt-4")                              → true
//	sameBaseModel("claude-3-5-sonnet-20241022", "claude-3-5") → true
type MatchFn func(a, b string) bool

// DefaultMatchFns returns the standard matching functions used by most providers.
// Currently only performs case-insensitive exact matching.
//
// SameBaseModel (strips version suffixes, e.g. "claude-3-5-sonnet-20241022" ≈ "claude-3-5-sonnet")
// is intentionally excluded — users should use aliases for explicit version-to-base-name mapping.
// It can be appended here if fuzzy base-model matching is ever needed globally.
func DefaultMatchFns() []MatchFn {
	return []MatchFn{
		func(a, b string) bool { return strings.EqualFold(a, b) },
	}
}

// matches reports whether a and b are considered equal by any of the provided fns.
// Returns true on the first fn that returns true.
func matches(a, b string, fns []MatchFn) bool {
	for _, fn := range fns {
		if fn(a, b) {
			return true
		}
	}
	return false
}

// FilterResult is the outcome of running Pipeline.FilterModel for a single model
// from the provider's API response.
type FilterResult struct {
	// Include reports whether this model should appear in the output.
	Include bool

	// ResolvedID is the user-facing model name to use as the ID suffix.
	// If the model matched an alias VALUE, this is the alias KEY.
	// Otherwise this is the original model ID from the API response.
	//
	// Example: API returns "gpt-4-turbo", aliases={"my-gpt4":"gpt-4-turbo"}
	//   → ResolvedID = "my-gpt4"
	// Example: API returns "gpt-3.5-turbo", no alias match
	//   → ResolvedID = "gpt-3.5-turbo"
	ResolvedID string

	// AliasValue is the provider-specific model ID when the model was matched
	// via an alias. Set as the model.Alias field so callers know the underlying ID.
	// Empty when the model was matched directly (no alias involved).
	//
	// Example: API returns "gpt-4-turbo", alias key "my-gpt4" matched
	//   → AliasValue = "gpt-4-turbo"
	AliasValue string
}

// Pipeline holds all the context needed to filter and backfill models in a
// single ListModels response. Construct one per ToBifrostListModelsResponse call
// and use its methods instead of passing params + matchFns to every function.
//
//	pipeline := &providerUtils.ListModelsPipeline{
//	    AllowedModels:     key.Models,
//	    BlacklistedModels: key.BlacklistedModels,
//	    Aliases:           key.Aliases,
//	    Unfiltered:        request.Unfiltered,
//	    ProviderKey:       schemas.OpenAI,
//	    MatchFns:          providerUtils.DefaultMatchFns(),
//	}
//	if pipeline.ShouldEarlyExit() { return empty }
//	result := pipeline.FilterModel(model.ID)
//	pipeline.BackfillModels(included)
type ListModelsPipeline struct {
	AllowedModels     schemas.WhiteList
	BlacklistedModels schemas.BlackList
	// Aliases maps user-facing alias keys to provider-specific model IDs.
	// e.g. {"my-gpt4": "gpt-4-turbo-2024-04-09"}
	Aliases     map[string]string
	Unfiltered  bool
	ProviderKey schemas.ModelProvider
	// MatchFns is the ordered list of equivalence functions used for every
	// model ID comparison. Use DefaultMatchFns() for standard behaviour;
	// providers may append additional fns (e.g. Bedrock's region-prefix remover).
	MatchFns []MatchFn
}

// ShouldEarlyExit reports whether ToBifrostListModelsResponse should immediately
// return an empty response without processing any models.
//
// Returns true when:
//   - not unfiltered AND allowlist is empty AND no aliases configured
//     (there is nothing to match against — all models would be filtered out anyway)
//   - not unfiltered AND blacklist blocks everything
//
// Note: allowlist empty + aliases present → do NOT early exit.
// The aliases drive backfill in the wildcard-allowlist case (Case B of BackfillModels).
func (p *ListModelsPipeline) ShouldEarlyExit() bool {
	if p.Unfiltered {
		return false
	}
	if p.BlacklistedModels.IsBlockAll() {
		return true
	}
	if p.AllowedModels.IsEmpty() && len(p.Aliases) == 0 {
		return true
	}
	return false
}

// resolveModelID checks whether modelID matches any alias VALUE using the pipeline's MatchFns.
//
// If a match is found → returns (aliasKey, aliasValue).
//
//	The alias key becomes the user-facing ID; the alias value is stored as Alias.
//	Example: modelID="gpt-4-turbo", aliases={"my-gpt4":"gpt-4-turbo"}
//	  → ("my-gpt4", "gpt-4-turbo")
//
// If no alias match → returns (modelID, "").
//
//	The original model ID is used as-is.
//	Example: modelID="gpt-3.5-turbo", no alias match
//	  → ("gpt-3.5-turbo", "")
func (p *ListModelsPipeline) resolveModelID(modelID string) (resolvedName, aliasValue string) {
	for aliasKey, providerID := range p.Aliases {
		if matches(modelID, providerID, p.MatchFns) {
			return aliasKey, providerID
		}
	}
	return modelID, ""
}

// FilterModel applies the full filter pipeline for a single model from the API response.
//
// Steps:
//  1. Resolve name — check alias VALUES for a match (uses MatchFns).
//     If matched: resolvedName = alias KEY, aliasValue = provider ID.
//     If not matched: resolvedName = original modelID, aliasValue = "".
//  2. Allowlist check (only when allowlist is restricted, i.e. not wildcard):
//     Skip if resolvedName is not in AllowedModels.
//  3. Blacklist check (always):
//     Skip if resolvedName is blacklisted. Blacklist takes precedence over everything.
//  4. Return Include=true with resolvedName and aliasValue.
//
// Examples:
//
//	allowedModels=["my-gpt4"], aliases={"my-gpt4":"gpt-4-turbo"}, blacklist=[]
//	  FilterModel("gpt-4-turbo") → {true,  "my-gpt4",    "gpt-4-turbo"}
//	  FilterModel("gpt-3.5")     → {false, "",            ""}  (not in allowlist)
//
//	allowedModels=*, aliases={"my-gpt4":"gpt-4-turbo"}, blacklist=["bad-model"]
//	  FilterModel("gpt-4-turbo") → {true,  "my-gpt4",    "gpt-4-turbo"}
//	  FilterModel("gpt-3.5")     → {true,  "gpt-3.5",    ""}
//	  FilterModel("bad-model")   → {false, "",            ""}  (blacklisted)
//
//	allowedModels=["gpt-3.5"], aliases={}, blacklist=[]
//	  FilterModel("gpt-3.5")     → {true,  "gpt-3.5",    ""}
//	  FilterModel("gpt-4")       → {false, "",            ""}
func (p *ListModelsPipeline) FilterModel(modelID string) FilterResult {
	// Step 1: resolve name — alias lookup takes priority over direct model ID.
	resolvedName, aliasValue := p.resolveModelID(modelID)

	// Step 2: allowlist check.
	// Only applies when the allowlist is restricted (caller has specified explicit models).
	// When allowlist is wildcard (*) / empty, all non-blacklisted models pass through.
	if !p.Unfiltered && p.AllowedModels.IsRestricted() {
		allowed := false
		for _, entry := range p.AllowedModels {
			if matches(resolvedName, entry, p.MatchFns) {
				allowed = true
				break
			}
		}
		if !allowed {
			return FilterResult{}
		}
	}

	// Step 3: blacklist check — blacklist always wins regardless of allowlist or aliases.
	if !p.Unfiltered {
		for _, entry := range p.BlacklistedModels {
			if matches(resolvedName, entry, p.MatchFns) {
				return FilterResult{}
			}
		}
	}

	return FilterResult{
		Include:    true,
		ResolvedID: resolvedName,
		AliasValue: aliasValue,
	}
}

// BackfillModels adds model entries that were configured by the caller but not
// returned by the provider's API response (or not matched during filtering).
//
// The `included` map tracks model IDs (lowercased) already added during the
// filter pass, used to avoid duplicates.
//
// Two cases depending on whether the allowlist is restricted:
//
// Case A — allowlist restricted (caller specified explicit model names):
//
//	Add each allowlist entry that is not yet in `included`, skip if blacklisted.
//	If the entry has an alias mapping (aliases[entry] exists), set Alias to the
//	provider-specific ID so callers can route to the right model.
//
//	Example: allowedModels=["my-gpt4","gpt-3.5"], aliases={"my-gpt4":"gpt-4-turbo"}
//	  "my-gpt4" not in included → add {ID:"openai/my-gpt4", Alias:"gpt-4-turbo"}
//	  "gpt-3.5" not in included → add {ID:"openai/gpt-3.5"}
//
// Case B — allowlist wildcard/empty (*):
//
//	We don't know all model names (no explicit list), so we only backfill entries
//	that were explicitly configured via aliases and not yet matched from the API.
//
//	Example: aliases={"my-gpt4":"gpt-4-turbo"}, "my-gpt4" not in included
//	  → add {ID:"openai/my-gpt4", Alias:"gpt-4-turbo"}
//
// Blacklist always wins — nothing blacklisted is added in either case.
func (p *ListModelsPipeline) BackfillModels(included map[string]bool) []schemas.Model {
	var result []schemas.Model

	if !p.Unfiltered && p.AllowedModels.IsRestricted() {
		// Case A: backfill explicit allowlist entries not yet matched.
		for _, entry := range p.AllowedModels {
			if included[strings.ToLower(entry)] {
				continue
			}
			// Blacklist check.
			blacklisted := false
			for _, bl := range p.BlacklistedModels {
				if matches(entry, bl, p.MatchFns) {
					blacklisted = true
					break
				}
			}
			if blacklisted {
				continue
			}
			m := schemas.Model{
				ID:   string(p.ProviderKey) + "/" + entry,
				Name: schemas.Ptr(ToDisplayName(entry)),
			}
			// If this allowlist entry has an alias, surface the provider-specific ID.
			if providerID, ok := p.Aliases[entry]; ok {
				m.Alias = schemas.Ptr(providerID)
			}
			result = append(result, m)
		}
		return result
	}

	// Case B: wildcard allowlist — backfill only explicitly configured aliases.
	if !p.Unfiltered && len(p.Aliases) > 0 {
		for aliasKey, providerID := range p.Aliases {
			if included[strings.ToLower(aliasKey)] {
				continue
			}
			// Blacklist check.
			blacklisted := false
			for _, bl := range p.BlacklistedModels {
				if matches(aliasKey, bl, p.MatchFns) {
					blacklisted = true
					break
				}
			}
			if blacklisted {
				continue
			}
			result = append(result, schemas.Model{
				ID:    string(p.ProviderKey) + "/" + aliasKey,
				Name:  schemas.Ptr(ToDisplayName(aliasKey)),
				Alias: schemas.Ptr(providerID),
			})
		}
	}

	return result
}
