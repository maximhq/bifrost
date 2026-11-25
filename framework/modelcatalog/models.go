package modelcatalog

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
	"unsafe"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/livestore"
)

// providersWithPartialListModels enumerates providers whose /v1/models response
// is a strict subset of their callable catalog — Perplexity lists only
// responses-API models and omits the Sonar chat family, which is still callable
// via /chat/completions. For these, the datasheet (not the live list) is the
// authoritative superset, so live must be unioned with the full allowed
// datasheet set rather than only the deprecated backfill. Databricks lists only
// the key's configured models and aliases, so the datasheet carries the rest.
var providersWithPartialListModels = map[schemas.ModelProvider]bool{
	schemas.Perplexity: true,
	schemas.Vertex:     true,
	schemas.Runware:    true,
	schemas.Databricks: true,
	schemas.Elevenlabs: true, // Include catalog-known STT models absent from live discovery.
}

// providersWithListModelsOnly enumerates providers whose unfiltered model list
// is their own /models response alone. The datasheet carries Cloudflare's models
// under several ID shapes (@cf/..., workers-ai/@cf/..., bare clef), so unioning
// it with the live list shows the same model more than once.
var providersWithListModelsOnly = map[schemas.ModelProvider]bool{
	schemas.Cloudflare: true,
}

// The catalog's model index table names. A process has one catalog, so the names are fixed.
const (
	maxOutputTokensTable   = "model_catalog_max_output_tokens"
	providersForModelTable = "model_catalog_providers_for_model"
	modelsForProviderTable = "model_catalog_models_for_provider"
)

// GetModelCreatedTimes returns the creation time, in Unix seconds, that the
// provider last reported for each of its listed models, taken from the cached
// list-models responses in the given mode, so times line up with the listing
// read in that mode. It never calls the provider. Models the provider gave no
// time for are absent. The returned map is owned by the caller.
func (mc *ModelCatalog) GetModelCreatedTimes(provider schemas.ModelProvider, unfiltered bool) map[string]int64 {
	if mc == nil || mc.live == nil {
		return map[string]int64{}
	}
	return mc.live.CreatedForProvider(provider, unfiltered)
}

// GetModelsForProvider returns the effective allowed model set for the
// provider. Filtered live entries are authoritative when present (they were
// pre-gated by ListModelsPipeline against the key's allow/block/aliases);
// otherwise the datasheet view is filtered by the keyconfig aggregates.
//
// Memoized; returns a fresh clone the caller may mutate.
func (mc *ModelCatalog) GetModelsForProvider(provider schemas.ModelProvider) []string {
	// Cloned: callers sort the result in place. An empty answer is not kept.
	return slices.Clone(mc.modelsForProvider.GetOrCompute(string(provider), func() ([]string, time.Duration, bool) {
		models := mc.computeModelsForProvider(provider)
		return models, 0, len(models) > 0
	}))
}

func (mc *ModelCatalog) computeModelsForProvider(provider schemas.ModelProvider) []string {
	blacklisted := mc.keyconf.BlacklistedFor(provider)
	allowed := mc.keyconf.AllowedFor(provider)

	var out []string
	if liveModels := mc.live.ModelsForProvider(provider); len(liveModels) > 0 {
		out = liveModels
		// Datasheet models to reconcile on top of the live list: normally just
		// deprecated ones (dropped from list-models but still callable). For
		// providers whose list-models is a partial subset, use the full
		// datasheet so callable-but-unlisted models (e.g. Perplexity's Sonar
		// chat family) aren't shadowed by the incomplete live list.
		datasheetModelsToAppend := mc.datasheet.DeprecatedDatasheetModelsForProvider(provider)
		if providersWithPartialListModels[provider] {
			datasheetModelsToAppend = mc.datasheet.DatasheetModelsForProvider(provider)
		}
		out = mc.appendAllowedDatasheetModels(out, datasheetModelsToAppend, allowed, blacklisted)
	} else if datasheetModels := mc.datasheet.DatasheetModelsForProvider(provider); len(datasheetModels) > 0 && allowed != nil {
		out = make([]string, 0, len(datasheetModels))
		for _, m := range datasheetModels {
			if blacklisted.IsBlocked(m) {
				continue
			}
			if allowed.IsAllowed(m) {
				out = append(out, m)
			}
		}
	} else {
		out = []string{}
	}

	seen := make(map[string]struct{}, len(out))
	for _, m := range out {
		seen[m] = struct{}{}
	}
	for _, e := range mc.keyconf.EntriesFor(provider) {
		if !e.Enabled {
			continue
		}
		for alias := range e.Aliases {
			if schemas.IsRegexEntry(alias) || blacklisted.IsBlocked(alias) {
				continue
			}
			if allowed == nil || !allowed.IsAllowed(alias) {
				continue
			}
			if _, ok := seen[alias]; ok {
				continue
			}
			seen[alias] = struct{}{}
			out = append(out, alias)
		}
		for _, m := range e.Allowed {
			if m == "*" || schemas.IsRegexEntry(m) || blacklisted.IsBlocked(m) {
				continue
			}
			if _, ok := seen[m]; ok {
				continue
			}
			seen[m] = struct{}{}
			out = append(out, m)
		}
	}
	return out
}

// AliasNamesForProvider returns the sorted alias names the provider's enabled keys can serve, pattern aliases left out.
func (mc *ModelCatalog) AliasNamesForProvider(provider schemas.ModelProvider) []string {
	var names []string
	for _, e := range mc.keyconf.EntriesFor(provider) {
		if !e.Enabled {
			continue
		}
		for alias := range e.Aliases {
			// Key selection checks a key's own lists, so a wider one on another key does not count.
			if schemas.IsRegexEntry(alias) || e.Blacklisted.IsBlocked(alias) || !e.Allowed.IsAllowed(alias) {
				continue
			}
			names = append(names, alias)
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

func (mc *ModelCatalog) appendAllowedDatasheetModels(out []string, models []string, allowed schemas.WhiteList, blacklisted schemas.BlackList) []string {
	if len(models) == 0 {
		return out
	}
	seen := make(map[string]struct{}, len(out))
	for _, m := range out {
		seen[m] = struct{}{}
	}
	for _, m := range models {
		if _, ok := seen[m]; ok {
			continue
		}
		if blacklisted.IsBlocked(m) {
			continue
		}
		if allowed != nil && !allowed.IsAllowed(m) {
			continue
		}
		seen[m] = struct{}{}
		out = append(out, m)
	}
	slices.Sort(out)
	return out
}

// GetUnfilteredModelsForProvider returns the raw catalog view (no gate
// applied): union of live unfiltered entries and the datasheet view.
func (mc *ModelCatalog) GetUnfilteredModelsForProvider(provider schemas.ModelProvider) []string {
	liveModels := mc.live.UnfilteredModelsForProvider(provider)
	if providersWithListModelsOnly[provider] {
		return liveModels
	}
	datasheetModels := mc.datasheet.DatasheetModelsForProvider(provider)
	if len(liveModels) == 0 {
		return datasheetModels
	}
	if len(datasheetModels) == 0 {
		return liveModels
	}
	seen := make(map[string]struct{}, len(liveModels)+len(datasheetModels))
	out := make([]string, 0, len(liveModels)+len(datasheetModels))
	for _, m := range liveModels {
		if _, ok := seen[m]; !ok {
			seen[m] = struct{}{}
			out = append(out, m)
		}
	}
	for _, m := range datasheetModels {
		if _, ok := seen[m]; !ok {
			seen[m] = struct{}{}
			out = append(out, m)
		}
	}
	slices.Sort(out)
	return out
}

// GetDistinctBaseModelNames returns all unique base model names from the
// datasheet. Used by governance for cross-provider model selection.
func (mc *ModelCatalog) GetDistinctBaseModelNames() []string {
	return mc.datasheet.DistinctBaseModelNames()
}

// catalogNegativeTTL is how long the catalog memos keep an answer for a model name the catalog does
// not list: that it knows nothing of the model, or what it knows under another spelling. Model names
// come from requests, so such answers are kept briefly rather than for the generation, and names
// callers invent cannot grow the memos for long; names the catalog lists stay for the generation.
const catalogNegativeTTL = 30 * time.Second

// listedFor reports whether provider's model list names model exactly as written. Many spellings
// resolve to one listed model (a version suffix strips to its base model), so a memo keeps an answer
// for the generation only when this holds: the names kept that long are bounded by what the catalog
// lists, not by what callers send.
func (mc *ModelCatalog) listedFor(provider schemas.ModelProvider, model string) bool {
	return slices.Contains(mc.GetModelsForProvider(provider), model)
}

// catalogGeneration sums the three stores' write counters. Each write bumps one
// counter by one, so the sum strictly increases: no two catalog states collide.
func (mc *ModelCatalog) catalogGeneration() uint64 {
	return mc.datasheet.WriteGen() + mc.keyconf.WriteGen() + mc.live.WriteGen()
}

// initCaches builds the capability cache and the catalog's generation tables in live. Every
// catalog has them: Init and the test constructors both call it.
func (mc *ModelCatalog) initCaches(live *livestore.Store) error {
	ctx := context.Background()
	var err error
	// Concurrent lookups of one model share one load, as the cache this replaced did.
	if mc.capabilities, err = live.NewCache[capabilityMemo](ctx, "model_catalog_capabilities",
		livestore.WithCacheTTL(capabilityMemo.ttl),
		livestore.WithSharedFills[capabilityMemo]()); err != nil {
		return err
	}
	// catalogGeneration covers all three: each answer derives from the same three stores.
	gen := livestore.WithGeneration(mc.catalogGeneration)
	if mc.providersForModel, err = live.NewTable[string, []schemas.ModelProvider](ctx, providersForModelTable, gen); err != nil {
		return err
	}
	if mc.modelsForProvider, err = live.NewTable[string, []string](ctx, modelsForProviderTable, gen); err != nil {
		return err
	}
	mc.maxOutputTokens, err = live.NewTable[string, int](ctx, maxOutputTokensTable, gen)
	return err
}

// GetProvidersForModel returns every provider that can serve the model.
// Memoized; returns a fresh clone the caller may mutate.
func (mc *ModelCatalog) GetProvidersForModel(model string) []schemas.ModelProvider {
	// Cloned: the resolver sorts the result in place. An empty answer is not kept.
	return slices.Clone(mc.providersForModel.GetOrCompute(model, func() ([]schemas.ModelProvider, time.Duration, bool) {
		providers := mc.computeProvidersForModel(model)
		if len(providers) == 0 {
			return providers, 0, false
		}
		// An answer for a name no provider lists is kept only for catalogNegativeTTL. A provider
		// that lists the name is in the answer, so only those are checked.
		for _, p := range providers {
			if mc.listedFor(p, model) {
				return providers, 0, true
			}
		}
		return providers, catalogNegativeTTL, true
	}))
}

// computeProvidersForModel composes the uncached answer across stores,
// including the openrouter / vertex / groq-gpt / bedrock-claude special cases.
func (mc *ModelCatalog) computeProvidersForModel(model string) []schemas.ModelProvider {
	baseModel := mc.datasheet.BaseModelName(model)

	providers := make([]schemas.ModelProvider, 0)
	seen := make(map[schemas.ModelProvider]struct{})
	for _, p := range mc.knownProviders() {
		models := mc.GetModelsForProvider(p)
		matched := false
		for _, m := range models {
			if m == model || mc.datasheet.BaseModelName(m) == baseModel {
				matched = true
				break
			}
		}
		if matched {
			if _, ok := seen[p]; !ok {
				providers = append(providers, p)
				seen[p] = struct{}{}
			}
		}
	}

	// Cross-provider special cases
	if _, ok := seen[schemas.OpenRouter]; !ok {
		openRouterModels := mc.GetModelsForProvider(schemas.OpenRouter)
		for _, p := range providers {
			if slices.Contains(openRouterModels, string(p)+"/"+model) {
				providers = append(providers, schemas.OpenRouter)
				seen[schemas.OpenRouter] = struct{}{}
				break
			}
		}
	}
	if _, ok := seen[schemas.Vertex]; !ok {
		vertexModels := mc.GetModelsForProvider(schemas.Vertex)
		for _, p := range providers {
			if slices.Contains(vertexModels, string(p)+"/"+model) {
				providers = append(providers, schemas.Vertex)
				seen[schemas.Vertex] = struct{}{}
				break
			}
		}
	}
	if _, ok := seen[schemas.Groq]; !ok && strings.Contains(model, "gpt-") {
		if slices.Contains(mc.GetModelsForProvider(schemas.Groq), "openai/"+model) {
			providers = append(providers, schemas.Groq)
		}
	}
	if _, ok := seen[schemas.Bedrock]; !ok && strings.Contains(model, "claude") {
		for _, bedrockModel := range mc.GetModelsForProvider(schemas.Bedrock) {
			if strings.Contains(bedrockModel, model) {
				providers = append(providers, schemas.Bedrock)
				break
			}
		}
	}

	for _, p := range mc.keyconf.Providers() {
		if _, ok := seen[p]; ok {
			continue
		}
		if mc.keyconf.BlacklistedFor(p).IsBlocked(model) {
			continue
		}
		allowed := mc.keyconf.AllowedFor(p)
		matched := false
		if _, hit := mc.keyconf.ResolveAlias(p, model); hit && allowed.IsAllowed(model) {
			matched = true
		} else if allowed.Contains(model) {
			matched = true
		} else if allowed.IsUnrestricted() &&
			len(mc.datasheet.DatasheetModelsForProvider(p)) == 0 &&
			len(mc.live.UnfilteredModelsForProvider(p)) == 0 {
			matched = true
		}
		if matched {
			providers = append(providers, p)
			seen[p] = struct{}{}
		}
	}

	return providers
}

// IsModelAllowedForProvider checks whether the model is allowed for the
// provider given an explicit allowedModels list (used by VK governance
// checks, not by the static keyconfig allow set).
//
//   - allowedModels=["*"]: defer to GetProvidersForModel (with custom-provider
//     fast path when list-models is disabled).
//   - allowedModels=[]: deny-by-default.
//   - explicit allowedModels: direct or provider-prefixed match against the
//     provider's catalog.
//
// providerConfig is the provider's configuration, nil when it has none. keys is read only for key
// aliases, which may widen an explicit list; a caller that wants no alias widening passes nil.
func (mc *ModelCatalog) IsModelAllowedForProvider(provider schemas.ModelProvider, model string, providerConfig *configstore.ProviderConfig, keys []schemas.Key, allowedModels schemas.WhiteList) bool {
	isCustomProvider := false
	hasListModelsEndpointDisabled := false
	if providerConfig != nil && providerConfig.CustomProviderConfig != nil {
		isCustomProvider = true
		hasListModelsEndpointDisabled = !providerConfig.CustomProviderConfig.IsOperationAllowed(schemas.ListModelsRequest)
	}

	if allowedModels.IsUnrestricted() {
		if isCustomProvider && hasListModelsEndpointDisabled {
			return true
		}
		return slices.Contains(mc.GetProvidersForModel(model), provider)
	}
	if allowedModels.IsEmpty() {
		return false
	}

	if mc.explicitListAllows(provider, model, allowedModels) {
		return true
	}

	// A key alias is a client-facing name for a provider model, resolved only after governance has
	// decided, so a request for "claude-sonnet" reaches this check under the alias, not under the
	// model it stands for. An allowlist that names the model should permit it.
	return MayBeKeyAlias(provider, model, keys) &&
		mc.aliasesResolveToAllowed(provider, model, keys, allKeyIDs, allowedModels)
}

// allKeyIDs is the key restriction that restricts nothing.
var allKeyIDs = schemas.WhiteList{"*"}

// KeyAliasesResolveToAllowed is the key-alias half of IsModelAllowedForProvider for a caller that
// knows which keys can serve the request: keys is the provider's keys, already narrowed to a pinned
// key when the request pins one, and keyIDs the key restriction it is served under (["*"] for every
// key, empty for none). Keys outside keyIDs cannot be selected, so their missing alias does not veto
// the name. It answers false for a wildcard or empty allowlist, which IsModelAllowedForProvider
// decides without aliases.
func (mc *ModelCatalog) KeyAliasesResolveToAllowed(provider schemas.ModelProvider, model string, keys []schemas.Key, keyIDs, allowedModels schemas.WhiteList) bool {
	if allowedModels.IsUnrestricted() || allowedModels.IsEmpty() {
		return false
	}
	return mc.aliasesResolveToAllowed(provider, model, keys, keyIDs, allowedModels)
}

// aliasesResolveToAllowed reports whether model is a key alias that resolves to an allowed model
// on every key that could serve it. Key selection runs after this check and may pick any enabled
// key, and a key without the alias sends the name to the provider as written, so one key mapping
// the name to an allowed model is not enough: each enabled key must map it, and to an allowed
// model. The match is one way: an allowlist naming only the alias does not open the model's own id.
func (mc *ModelCatalog) aliasesResolveToAllowed(provider schemas.ModelProvider, model string, keys []schemas.Key, keyIDs, allowedModels schemas.WhiteList) bool {
	matched := false
	// Last target that passed aliasTargetAllowed for this model and allowlist; keys that agree on
	// a target skip the catalog work. Only passing targets are stored.
	lastAllowed := ""
	check := func(target string) bool {
		if lastAllowed != "" && target == lastAllowed {
			return true
		}
		if !mc.aliasTargetAllowed(provider, model, target, allowedModels) {
			return false
		}
		lastAllowed = target
		return true
	}
	for i := range keys {
		// A key that cannot serve the name (disabled, or its models or blacklisted_models exclude
		// it, both written in alias names) cannot be picked, so its missing alias must not veto
		// the request.
		if !keyServesName(&keys[i], model) || !keyIDs.IsAllowed(keys[i].ID) {
			continue
		}
		// Same resolution as schemas.KeyAliases.ResolveConfig (exact name, then case-insensitive),
		// without the pointer it returns, which would allocate on this per-request path. When names
		// differing only in case map to different models, every one must be allowed.
		found := false
		if ac, ok := keys[i].Aliases[model]; ok {
			if !check(ac.ModelID) {
				return false
			}
			found = true
		} else {
			for name, ac := range keys[i].Aliases {
				if !strings.EqualFold(name, model) {
					continue
				}
				if !check(ac.ModelID) {
					return false
				}
				found = true
			}
		}
		if !found {
			return false
		}
		matched = true
	}
	return matched
}

func (mc *ModelCatalog) aliasTargetAllowed(provider schemas.ModelProvider, model, target string, allowedModels schemas.WhiteList) bool {
	return target != "" && target != model && mc.explicitListAllows(provider, target, allowedModels)
}

// keyServesName reports whether key selection can pick key for the requested name. It mirrors the
// list checks of core's keySupportsModel; both lists are written in alias names.
func keyServesName(key *schemas.Key, model string) bool {
	if key.Enabled != nil && !*key.Enabled {
		return false
	}
	return key.Models.IsAllowed(model) && !key.BlacklistedModels.IsBlocked(model)
}

// HasKeyAliases reports whether any key carries an alias. Callers gate alias work on it so that a
// provider without aliases pays one loop over its keys and nothing more.
func HasKeyAliases(keys []schemas.Key) bool {
	for i := range keys {
		if len(keys[i].Aliases) > 0 {
			return true
		}
	}
	return false
}

// aliasNameIndex is the set of alias names a provider's keys carry, case-folded, for one published
// keys slice and the alias maps its keys held when it was built.
type aliasNameIndex struct {
	keys    *schemas.Key // first element of the indexed slice; holding it keeps the address from being reused
	n       int
	aliases []schemas.KeyAliases // each key's alias map; holding them keeps their identities from being reused
	sizes   []int                // each alias map's length when indexed
	names   map[string]struct{}
}

// matches reports whether the index was built from keys as they are now: the same slice, and on each
// key the same alias map with the same number of names. It allocates nothing.
func (idx *aliasNameIndex) matches(keys []schemas.Key) bool {
	if idx.keys != &keys[0] || idx.n != len(keys) {
		return false
	}
	for i := range keys {
		if len(keys[i].Aliases) != idx.sizes[i] || !sameAliasMap(keys[i].Aliases, idx.aliases[i]) {
			return false
		}
	}
	return true
}

// sameAliasMap reports whether a and b are the same map, not merely equal ones. A map value is a
// single pointer to the map's header, so comparing the two words compares identity.
func sameAliasMap(a, b schemas.KeyAliases) bool {
	return *(*unsafe.Pointer)(unsafe.Pointer(&a)) == *(*unsafe.Pointer)(unsafe.Pointer(&b))
}

// aliasNameIndexes holds one aliasNameIndex per provider; a new keys slice replaces its entry.
var aliasNameIndexes sync.Map // schemas.ModelProvider -> *aliasNameIndex

// MayBeKeyAlias reports whether some key of keys carries an alias whose name equals model under
// case folding, the comparison alias resolution uses. False means model is no alias on any key, so a
// caller can skip the per-key alias scan, which ranges over every alias map and is what a provider
// with aliases otherwise pays on every request. True decides nothing: the scan still decides.
//
// keys must be the provider's whole published keys slice, not a narrowed one. The set is built once
// per slice and reused while the provider's configuration hands back that same slice with the same
// alias maps (by identity and length), so an edited key, a replaced alias map, or a name added to or
// removed from a map in place rebuilds it. Renaming an alias in place, which keeps the map and its
// length, is not detected: a configuration source must publish a new slice or map for every alias
// change, never write into one it has handed out (the contract the live store's records keep, and
// the one every store here keeps). Memory:
// one entry per provider, holding each distinct alias name once plus a map reference per key, freed
// when the provider's next keys slice replaces it.
func MayBeKeyAlias(provider schemas.ModelProvider, model string, keys []schemas.Key) bool {
	if len(keys) == 0 {
		return false
	}
	var idx *aliasNameIndex
	if v, ok := aliasNameIndexes.Load(provider); ok {
		if cached := v.(*aliasNameIndex); cached.matches(keys) {
			idx = cached
		}
	}
	if idx == nil {
		idx = &aliasNameIndex{keys: &keys[0], n: len(keys), aliases: make([]schemas.KeyAliases, len(keys)), sizes: make([]int, len(keys)), names: map[string]struct{}{}}
		for i := range keys {
			idx.aliases[i] = keys[i].Aliases
			idx.sizes[i] = len(keys[i].Aliases)
			for name := range keys[i].Aliases {
				idx.names[foldName(name)] = struct{}{}
			}
		}
		aliasNameIndexes.Store(provider, idx)
	}
	_, ok := idx.names[foldName(model)]
	return ok
}

// foldName maps s to a string that is equal for two names exactly when strings.EqualFold reports
// them equal: each rune becomes the representative of its simple case-folding orbit. A name of
// ASCII lower-case letters, digits and punctuation is its own representative and is returned as is.
func foldName(s string) string {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= utf8.RuneSelf || ('A' <= c && c <= 'Z') {
			var b strings.Builder
			b.Grow(len(s))
			for _, r := range s {
				b.WriteRune(foldRune(r))
			}
			return b.String()
		}
	}
	return s
}

// foldRune is the representative of r's simple case-folding orbit (the runes strings.EqualFold treats
// as equal to r): its ASCII lower-case letter when the orbit has one, otherwise its smallest rune.
func foldRune(r rune) rune {
	rep := r
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		if f < rep {
			rep = f
		}
	}
	if 'A' <= rep && rep <= 'Z' {
		return rep + ('a' - 'A')
	}
	return rep
}

// KeyAliasTargets calls visit with each model an eligible key maps the requested name to, exact
// name first, then case-insensitively, the same lookup order as schemas.KeyAliases.ResolveConfig
// without the pointer it returns. A key is eligible when it can serve the name and keyIDs allows it
// (["*"] for every key, empty for none). Empty targets and targets equal to the name are skipped.
// Duplicates are not collapsed: keys that agree call visit once each.
func KeyAliasTargets(model string, keys []schemas.Key, keyIDs schemas.WhiteList, visit func(target string)) {
	for i := range keys {
		key := &keys[i]
		if len(key.Aliases) == 0 || !keyServesName(key, model) || !keyIDs.IsAllowed(key.ID) {
			continue
		}
		if ac, ok := key.Aliases[model]; ok {
			if ac.ModelID != "" && ac.ModelID != model {
				visit(ac.ModelID)
			}
			continue
		}
		for name, ac := range key.Aliases {
			if strings.EqualFold(name, model) && ac.ModelID != "" && ac.ModelID != model {
				visit(ac.ModelID)
			}
		}
	}
}

// explicitListAllows matches one model name against an explicit (non-empty, non-wildcard)
// allowlist: by name or regex: entry, then by provider-prefixed entry through the provider catalog.
func (mc *ModelCatalog) explicitListAllows(provider schemas.ModelProvider, model string, allowedModels schemas.WhiteList) bool {
	// Bare-name match (exact or regex:) needs no catalog access and covers most allowlists.
	if allowedModels.Contains(model) {
		return true
	}

	// Only provider-prefixed entries ("openai/gpt-4o") need the provider
	// catalog; build it once, and only when one exists.
	if !slices.ContainsFunc(allowedModels, func(m string) bool { return strings.Contains(m, "/") }) {
		return false
	}
	providerCatalogModels := mc.GetModelsForProvider(provider)
	for _, allowedModel := range allowedModels {
		if !strings.Contains(allowedModel, "/") {
			continue
		}
		if slices.Contains(providerCatalogModels, allowedModel) {
			_, modelPart := schemas.ParseModelString(allowedModel, "")
			if modelPart == model {
				return true
			}
		}
	}
	return false
}

func (mc *ModelCatalog) GetBaseModelName(model string) string {
	return mc.datasheet.BaseModelName(model)
}

func (mc *ModelCatalog) IsSameModel(model1, model2 string) bool {
	return mc.datasheet.IsSameModel(model1, model2)
}

// RefineModelForProvider refines a model identifier for providers whose
// catalog names carry a leading "provider/" segment (Groq, Replicate,
// Perplexity, OpenRouter), resolving a bare request like "gpt-oss-120b" to
// the provider's catalog slug ("openai/gpt-oss-120b"). Returns the original
// model unchanged when no refinement applies, or an error when multiple
// catalog candidates match ambiguously.
//
// Idempotent: a model that already carries a known provider prefix is the
// refined form itself — routing plugins may refine the same request more
// than once, so it is returned unchanged without a catalog scan. The one
// exception is a model carrying the TARGET provider's own prefix (e.g. a
// fallback entry built from another provider's refined form, or a
// double-prefixed input): the prefix is stripped and the bare remainder
// re-refined, so canonical own-prefixed names ("perplexity/sonar",
// "openrouter/auto") round-trip through the catalog scan unchanged.
func (mc *ModelCatalog) RefineModelForProvider(provider schemas.ModelProvider, model string) (string, error) {
	if prefixProvider, modelPart := schemas.ParseModelString(model, ""); prefixProvider != "" {
		if prefixProvider == provider {
			return mc.RefineModelForProvider(provider, modelPart)
		}
		return model, nil
	}
	switch provider {
	case schemas.Groq, schemas.Replicate, schemas.Perplexity, schemas.OpenRouter:
		return mc.refineNestedProviderModel(provider, model)
	case schemas.Databricks:
		// A key alias is resolved later, against the name it is given, so prefixing it here
		// would turn "my-alias" into a name no key alias matches.
		if _, ok := mc.keyconf.ResolveAlias(provider, model); ok {
			return model, nil
		}
		return refineDatabricksModel(model), nil
	}
	return model, nil
}

// Mirrors Databricks model refinement:
// - Catalog-qualified names (2+ dots) and `databricks-*` endpoints pass through.
// - Other names get the `system.ai.` prefix.
// - A single dot is treated as a version separator (e.g. `gpt-5.5`).
//
// Key aliases never reach here (the Databricks case returns them unchanged). Explicit
// `api_format` is handled by the provider and is not visible here.
func refineDatabricksModel(model string) string {
	const (
		// defaultCatalogPrefix is the Unity Catalog prefix under which Databricks publishes
		// its ready-to-use AI Gateway models.
		defaultCatalogPrefix = "system.ai."
		// modelServingEndpointPrefix is the naming convention for Databricks pay-per-token
		// Foundation Model endpoints, which live on Model Serving and take a bare name.
		modelServingEndpointPrefix = "databricks-"
	)
	if model == "" || strings.Count(model, ".") >= 2 {
		return model
	}
	if strings.HasPrefix(strings.ToLower(model), modelServingEndpointPrefix) {
		return model
	}
	return defaultCatalogPrefix + model
}

// refineNestedProviderModel resolves provider-native model slugs such as
// "openai/gpt-5-nano" from a base model request like "gpt-5-nano". Only
// considers catalog entries whose leading segment is a known Bifrost
// provider so Replicate owner/model identifiers like "meta/llama-3-8b" are
// left untouched.
func (mc *ModelCatalog) refineNestedProviderModel(provider schemas.ModelProvider, model string) (string, error) {
	models := mc.GetModelsForProvider(provider)
	if len(models) == 0 {
		return model, nil
	}

	candidateModels := make([]string, 0)
	seenCandidates := make(map[string]struct{})
	for _, poolModel := range models {
		providerPart, modelPart := schemas.ParseModelString(poolModel, "")
		if providerPart == "" || model != modelPart {
			continue
		}
		candidate := string(providerPart) + "/" + modelPart
		if _, seen := seenCandidates[candidate]; seen {
			continue
		}
		seenCandidates[candidate] = struct{}{}
		candidateModels = append(candidateModels, candidate)
	}

	switch len(candidateModels) {
	case 0:
		return model, nil
	case 1:
		return candidateModels[0], nil
	default:
		return "", fmt.Errorf("multiple compatible models found for model %s: %v", model, candidateModels)
	}
}
