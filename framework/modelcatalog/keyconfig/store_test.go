package keyconfig

import (
	"slices"
	"sort"
	"sync"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// --- test logger ---

type recordingLogger struct {
	mu     sync.Mutex
	debugs []string
}

func (l *recordingLogger) Debug(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.debugs = append(l.debugs, format)
}
func (l *recordingLogger) Info(format string, args ...any)                   {}
func (l *recordingLogger) Warn(format string, args ...any)                   {}
func (l *recordingLogger) Error(format string, args ...any)                  {}
func (l *recordingLogger) Fatal(format string, args ...any)                  {}
func (l *recordingLogger) SetLevel(level schemas.LogLevel)                   {}
func (l *recordingLogger) SetOutputType(outputType schemas.LoggerOutputType) {}
func (l *recordingLogger) LogHTTPRequest(level schemas.LogLevel, msg string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func (l *recordingLogger) DebugCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.debugs)
}

func ptrBool(b bool) *bool { return &b }

func newStoreFromFixture(fixture map[schemas.ModelProvider][]schemas.Key) (*Store, *recordingLogger) {
	log := &recordingLogger{}
	s := New(log)
	s.Replace(fixture)
	return s, log
}

// --- behavioral tests ---

func TestEmptyKeysAndStandardProviderRemoves(t *testing.T) {
	s, _ := newStoreFromFixture(map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: nil,
	})
	if got := s.AllowedFor(schemas.OpenAI); got != nil {
		t.Errorf("standard provider with no keys: AllowedFor = %v, want nil", got)
	}
	if s.IsAllowed(schemas.OpenAI, "gpt-4o") {
		t.Error("IsAllowed = true for standard provider with no keys")
	}
}

func TestKeylessNonStandardProviderUnrestricted(t *testing.T) {
	custom := schemas.ModelProvider("my-custom")
	s, _ := newStoreFromFixture(map[schemas.ModelProvider][]schemas.Key{
		custom: nil,
	})
	allowed := s.AllowedFor(custom)
	if !slices.Equal(allowed, schemas.WhiteList{"*"}) {
		t.Errorf("keyless custom: AllowedFor = %v, want [*]", allowed)
	}
	if !s.IsAllowed(custom, "anything") {
		t.Error("keyless custom: IsAllowed = false for arbitrary model")
	}
}

func TestUnrestrictedKeyImpliesWildcard(t *testing.T) {
	s, _ := newStoreFromFixture(map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {
			{ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"*"}},
		},
	})
	if got := s.AllowedFor(schemas.OpenAI); !slices.Equal(got, schemas.WhiteList{"*"}) {
		t.Errorf("AllowedFor = %v, want [*]", got)
	}
	if !s.IsAllowed(schemas.OpenAI, "gpt-4o") {
		t.Error("IsAllowed = false on wildcard key")
	}
}

func TestExplicitAllowFiltersBlacklistedPerKey(t *testing.T) {
	s, _ := newStoreFromFixture(map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {
			{
				ID:                "k1",
				Enabled:           ptrBool(true),
				Models:            schemas.WhiteList{"gpt-4o", "o1"},
				BlacklistedModels: schemas.BlackList{"o1"},
			},
		},
	})
	allowed := s.AllowedFor(schemas.OpenAI)
	sort.Strings(allowed)
	if !slices.Equal(allowed, schemas.WhiteList{"gpt-4o"}) {
		t.Errorf("AllowedFor = %v, want [gpt-4o] (o1 filtered by key blacklist)", allowed)
	}
}

func TestBlacklistIntersectionAcrossKeys(t *testing.T) {
	s, _ := newStoreFromFixture(map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {
			{ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"gpt-4o", "o1"},
				BlacklistedModels: schemas.BlackList{"o1", "gpt-3.5"}},
			{ID: "k2", Enabled: ptrBool(true), Models: schemas.WhiteList{"gpt-4o"},
				BlacklistedModels: schemas.BlackList{"o1"}},
		},
	})
	// o1 is blacklisted by both → provider-level blocked. gpt-3.5 only by one → not.
	bl := s.BlacklistedFor(schemas.OpenAI)
	sort.Strings(bl)
	if !slices.Equal(bl, schemas.BlackList{"o1"}) {
		t.Errorf("BlacklistedFor = %v, want [o1] (intersection)", bl)
	}
	if s.IsAllowed(schemas.OpenAI, "o1") {
		t.Error("IsAllowed o1 = true, want false (provider-blocked)")
	}
}

func TestDisabledKeyDoesNotAffectAggregates(t *testing.T) {
	s, _ := newStoreFromFixture(map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {
			{ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"gpt-4o"}},
			{ID: "k2", Enabled: ptrBool(false), Models: schemas.WhiteList{"o1"}, BlacklistedModels: schemas.BlackList{"gpt-4o"}},
		},
	})
	allowed := s.AllowedFor(schemas.OpenAI)
	if !slices.Equal(allowed, schemas.WhiteList{"gpt-4o"}) {
		t.Errorf("AllowedFor = %v, want [gpt-4o] (k2 disabled)", allowed)
	}
	if !s.IsAllowed(schemas.OpenAI, "gpt-4o") {
		t.Error("IsAllowed gpt-4o = false: disabled key's blacklist should not affect")
	}
}

func TestBlockAllKeySkipped(t *testing.T) {
	s, _ := newStoreFromFixture(map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {
			{ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"gpt-4o"}},
			{ID: "k2", Enabled: ptrBool(true), Models: schemas.WhiteList{"o1"}, BlacklistedModels: schemas.BlackList{"*"}},
		},
	})
	allowed := s.AllowedFor(schemas.OpenAI)
	if !slices.Equal(allowed, schemas.WhiteList{"gpt-4o"}) {
		t.Errorf("AllowedFor = %v, want [gpt-4o]; block-all key should be skipped", allowed)
	}
}

func TestEntriesForIncludesDisabled(t *testing.T) {
	s, _ := newStoreFromFixture(map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {
			{ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"gpt-4o"}},
			{ID: "k2", Enabled: ptrBool(false), Models: schemas.WhiteList{"o1"}},
		},
	})
	entries := s.EntriesFor(schemas.OpenAI)
	if len(entries) != 2 {
		t.Fatalf("EntriesFor returned %d entries, want 2 (including disabled)", len(entries))
	}
	hasDisabled := false
	for _, e := range entries {
		if e.KeyID == "k2" && !e.Enabled {
			hasDisabled = true
		}
	}
	if !hasDisabled {
		t.Error("disabled entry missing from EntriesFor")
	}
}

func TestEntryForLookup(t *testing.T) {
	s, _ := newStoreFromFixture(map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {
			{ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"gpt-4o"}},
		},
	})
	e, ok := s.EntryFor(schemas.OpenAI, "k1")
	if !ok {
		t.Fatal("EntryFor returned !ok for existing key")
	}
	if e.KeyID != "k1" {
		t.Errorf("KeyID = %q, want k1", e.KeyID)
	}
	if _, ok := s.EntryFor(schemas.OpenAI, "missing"); ok {
		t.Error("EntryFor returned ok for missing key")
	}
}

func TestResolveAliasReturnsOwner(t *testing.T) {
	s, _ := newStoreFromFixture(map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {
			{
				ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"*"},
				Aliases: schemas.KeyAliases{
					"my-prod": schemas.AliasConfig{ModelID: "gpt-4o-2024-08-06"},
				},
			},
		},
	})
	owner, ok := s.ResolveAlias(schemas.OpenAI, "my-prod")
	if !ok {
		t.Fatal("ResolveAlias !ok for known alias")
	}
	if owner.KeyID != "k1" {
		t.Errorf("owner.KeyID = %q, want k1", owner.KeyID)
	}
	if owner.Config.ModelID != "gpt-4o-2024-08-06" {
		t.Errorf("owner.Config.ModelID = %q, want gpt-4o-2024-08-06", owner.Config.ModelID)
	}
}

func TestResolveAliasMissing(t *testing.T) {
	s, _ := newStoreFromFixture(map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {{ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"*"}}},
	})
	if _, ok := s.ResolveAlias(schemas.OpenAI, "nope"); ok {
		t.Error("ResolveAlias ok for missing alias")
	}
	if _, ok := s.ResolveAlias(schemas.ModelProvider("absent"), "anything"); ok {
		t.Error("ResolveAlias ok for absent provider")
	}
}

func TestAliasCollisionLastEnabledWinsAndLogs(t *testing.T) {
	s, log := newStoreFromFixture(map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {
			{
				ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"*"},
				Aliases: schemas.KeyAliases{"my-prod": schemas.AliasConfig{ModelID: "from-k1"}},
			},
			{
				ID: "k2", Enabled: ptrBool(true), Models: schemas.WhiteList{"*"},
				Aliases: schemas.KeyAliases{"my-prod": schemas.AliasConfig{ModelID: "from-k2"}},
			},
		},
	})
	owner, _ := s.ResolveAlias(schemas.OpenAI, "my-prod")
	if owner.KeyID != "k2" {
		t.Errorf("owner.KeyID = %q, want k2 (last key in slice wins)", owner.KeyID)
	}
	if log.DebugCount() == 0 {
		t.Error("expected collision debug log, got none")
	}
}

func TestKeysAllowingModel(t *testing.T) {
	s, _ := newStoreFromFixture(map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {
			{ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"gpt-4o", "o1"}},
			{ID: "k2", Enabled: ptrBool(true), Models: schemas.WhiteList{"gpt-4o"}, BlacklistedModels: schemas.BlackList{"o1"}},
			{ID: "k3", Enabled: ptrBool(false), Models: schemas.WhiteList{"gpt-4o"}},
			{ID: "k4", Enabled: ptrBool(true), Models: schemas.WhiteList{"*"}},
		},
	})
	got := s.KeysAllowingModel(schemas.OpenAI, "gpt-4o")
	sort.Strings(got)
	if !slices.Equal(got, []string{"k1", "k2", "k4"}) {
		t.Errorf("KeysAllowingModel gpt-4o = %v, want [k1 k2 k4]", got)
	}
	got = s.KeysAllowingModel(schemas.OpenAI, "o1")
	sort.Strings(got)
	if !slices.Equal(got, []string{"k1", "k4"}) {
		t.Errorf("KeysAllowingModel o1 = %v, want [k1 k4] (k2 blacklists, k3 disabled)", got)
	}
}

func TestSetProviderIsolated(t *testing.T) {
	s := New(nil)
	s.Replace(map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI:    {{ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"gpt-4o"}}},
		schemas.Anthropic: {{ID: "k2", Enabled: ptrBool(true), Models: schemas.WhiteList{"claude-3-5-sonnet"}}},
	})

	s.SetProvider(schemas.OpenAI, []schemas.Key{
		{ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"gpt-4o-new"}},
	})

	if got := s.AllowedFor(schemas.OpenAI); !slices.Equal(got, schemas.WhiteList{"gpt-4o-new"}) {
		t.Errorf("openai after SetProvider = %v, want [gpt-4o-new]", got)
	}
	if got := s.AllowedFor(schemas.Anthropic); !slices.Equal(got, schemas.WhiteList{"claude-3-5-sonnet"}) {
		t.Errorf("anthropic perturbed by openai SetProvider: %v", got)
	}
}

func TestReplaceDropsDisappearedProviders(t *testing.T) {
	s := New(nil)
	s.Replace(map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI:    {{ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"gpt-4o"}}},
		schemas.Anthropic: {{ID: "k2", Enabled: ptrBool(true), Models: schemas.WhiteList{"claude-3-5-sonnet"}}},
	})

	s.Replace(map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {{ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"gpt-4o"}}},
	})

	if got := s.AllowedFor(schemas.Anthropic); got != nil {
		t.Errorf("anthropic still present after Replace dropped it: %v", got)
	}
}

func TestSetProviderToEmptyDropsStandard(t *testing.T) {
	s := New(nil)
	s.Replace(map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {{ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"gpt-4o"}}},
	})

	s.SetProvider(schemas.OpenAI, nil)

	if got := s.AllowedFor(schemas.OpenAI); got != nil {
		t.Errorf("after SetProvider(empty), AllowedFor = %v, want nil", got)
	}
}

func TestRemoveProvider(t *testing.T) {
	s, _ := newStoreFromFixture(map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {{ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"gpt-4o"}}},
	})
	s.RemoveProvider(schemas.OpenAI)
	if got := s.AllowedFor(schemas.OpenAI); got != nil {
		t.Errorf("after RemoveProvider, AllowedFor = %v, want nil", got)
	}
}

func TestEntriesForReturnsDefensiveCopy(t *testing.T) {
	s, _ := newStoreFromFixture(map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {{ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"gpt-4o"}}},
	})
	entries := s.EntriesFor(schemas.OpenAI)
	entries[0].KeyID = "MUTATED"
	again := s.EntriesFor(schemas.OpenAI)
	if again[0].KeyID != "k1" {
		t.Errorf("store mutated through EntriesFor: KeyID = %q", again[0].KeyID)
	}
}

// These lock down behaviors that the LB plugin used to maintain locally and
// that the keyconfig store now owns. The "aliases don't leak into allowed"
// suite documents the structural isolation: Aliases write to aliasIndex,
// Models write to allowed — they never mix.

func TestAliases_WildcardModels_AllowedStaysWildcard(t *testing.T) {
	s, _ := newStoreFromFixture(map[schemas.ModelProvider][]schemas.Key{
		schemas.Bedrock: {
			{
				ID:      "bk1",
				Enabled: ptrBool(true),
				Models:  schemas.WhiteList{"*"},
				Aliases: schemas.KeyAliases{
					"my-claude-alias": schemas.AliasConfig{ModelID: "anthropic.claude-3-5-sonnet-20241022-v2:0"},
				},
			},
		},
	})
	if got := s.AllowedFor(schemas.Bedrock); !slices.Equal(got, schemas.WhiteList{"*"}) {
		t.Errorf("AllowedFor = %v, want [*] (Models field wins; aliases do not leak)", got)
	}
	if _, ok := s.ResolveAlias(schemas.Bedrock, "my-claude-alias"); !ok {
		t.Error("alias missing from aliasIndex; should be present alongside ['*']")
	}
}

func TestAliases_SpecificModels_AllowedDoesNotIncludeAliasName(t *testing.T) {
	s, _ := newStoreFromFixture(map[schemas.ModelProvider][]schemas.Key{
		schemas.Azure: {
			{
				ID:      "az1",
				Enabled: ptrBool(true),
				Models:  schemas.WhiteList{"gpt-4o"},
				Aliases: schemas.KeyAliases{
					"gpt4o-prod": schemas.AliasConfig{ModelID: "gpt-4o"},
				},
			},
		},
	})
	got := s.AllowedFor(schemas.Azure)
	sort.Strings(got)
	if !slices.Equal(got, schemas.WhiteList{"gpt-4o"}) {
		t.Errorf("AllowedFor = %v, want [gpt-4o] only — alias name 'gpt4o-prod' must NOT appear in allowed", got)
	}
	if _, ok := s.ResolveAlias(schemas.Azure, "gpt4o-prod"); !ok {
		t.Error("alias missing from aliasIndex")
	}
}

func TestAliases_EmptyModels_ProviderAbsent(t *testing.T) {
	// Models=[] with aliases present: aliases are name swappers, not implicit
	// model grants. Operators must explicitly list models in Models field.
	// Provider should be absent from aggregates (no enabled allow path).
	s, _ := newStoreFromFixture(map[schemas.ModelProvider][]schemas.Key{
		schemas.Bedrock: {
			{
				ID:      "bk1",
				Enabled: ptrBool(true),
				Models:  schemas.WhiteList{},
				Aliases: schemas.KeyAliases{
					"prod": schemas.AliasConfig{ModelID: "anthropic.claude-3-5-sonnet-20241022-v2:0"},
				},
			},
		},
	})
	if got := s.AllowedFor(schemas.Bedrock); got != nil {
		t.Errorf("AllowedFor = %v, want nil — aliases alone must not produce an allowed entry", got)
	}
}

func TestAllKeysBlockAll_ProviderAbsent(t *testing.T) {
	// Every key has BlacklistedModels=["*"]. All are skipped by the aggregator,
	// enabledKeysCount stays 0, provider is dropped from the store.
	s, _ := newStoreFromFixture(map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {
			{ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"gpt-4o"}, BlacklistedModels: schemas.BlackList{"*"}},
			{ID: "k2", Enabled: ptrBool(true), Models: schemas.WhiteList{"o1"}, BlacklistedModels: schemas.BlackList{"*"}},
		},
	})
	if got := s.AllowedFor(schemas.OpenAI); got != nil {
		t.Errorf("AllowedFor = %v, want nil — every key is block-all", got)
	}
	if s.IsAllowed(schemas.OpenAI, "gpt-4o") {
		t.Error("IsAllowed = true for provider with all-block-all keys")
	}
}

func TestExplicitModels_UnionAcrossEnabledKeys(t *testing.T) {
	s, _ := newStoreFromFixture(map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {
			{ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"gpt-4o", "o1"}},
			{ID: "k2", Enabled: ptrBool(true), Models: schemas.WhiteList{"gpt-4o", "gpt-4.5"}},
			{ID: "k3", Enabled: ptrBool(false), Models: schemas.WhiteList{"sekret-model"}},
		},
	})
	got := s.AllowedFor(schemas.OpenAI)
	sort.Strings(got)
	// AllowedFor returns the deduped union of enabled keys' Models, minus
	// per-key blacklisted entries. The same model appearing in multiple keys
	// is collapsed to a single entry.
	want := schemas.WhiteList{"gpt-4.5", "gpt-4o", "o1"}
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Errorf("AllowedFor = %v, want union of enabled keys' Models = %v (k3 disabled, excluded)", got, want)
	}
	// Cross-check via IsAllowed (the actual consumer path).
	for _, m := range []string{"gpt-4o", "o1", "gpt-4.5"} {
		if !s.IsAllowed(schemas.OpenAI, m) {
			t.Errorf("IsAllowed(%s) = false, want true (model is in union of enabled keys)", m)
		}
	}
	if s.IsAllowed(schemas.OpenAI, "sekret-model") {
		t.Error("IsAllowed(sekret-model) = true, want false (only on disabled key k3)")
	}
}

// TestIsAllowed_ProviderAbsent ensures IsAllowed returns false
// (deny-by-default) for a provider that has no cached state.
func TestIsAllowed_ProviderAbsent(t *testing.T) {
	s, _ := newStoreFromFixture(map[schemas.ModelProvider][]schemas.Key{})
	if s.IsAllowed(schemas.OpenAI, "gpt-4o") {
		t.Error("IsAllowed on empty store = true, want false")
	}
}

// TestIsAllowed_BlacklistWinsOverAllow asserts blacklist takes precedence even
// when the model is also in the allowed set — the per-key gating uses both,
// but the aggregated view must respect the cross-key intersection blacklist.
func TestIsAllowed_BlacklistWinsOverAllow(t *testing.T) {
	s, _ := newStoreFromFixture(map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {
			{ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"gpt-4o"}, BlacklistedModels: schemas.BlackList{"gpt-4o"}},
			{ID: "k2", Enabled: ptrBool(true), Models: schemas.WhiteList{"gpt-4o"}, BlacklistedModels: schemas.BlackList{"gpt-4o"}},
		},
	})
	if s.IsAllowed(schemas.OpenAI, "gpt-4o") {
		t.Error("IsAllowed = true; want false (every enabled key blacklists gpt-4o)")
	}
}

// TestProviders_EmptyStore exercises the no-state baseline.
func TestProviders_EmptyStore(t *testing.T) {
	s, _ := newStoreFromFixture(map[schemas.ModelProvider][]schemas.Key{})
	if got := s.Providers(); len(got) != 0 {
		t.Errorf("Providers() on empty store = %v, want []", got)
	}
}

// TestProviders_StandardWithKeys ensures a standard provider with at least
// one enabled non-block-all key is enumerated.
func TestProviders_StandardWithKeys(t *testing.T) {
	s, _ := newStoreFromFixture(map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI:    {{ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"*"}}},
		schemas.Anthropic: {{ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"*"}}},
	})
	got := s.Providers()
	sort.Slice(got, func(i, j int) bool { return string(got[i]) < string(got[j]) })
	want := []schemas.ModelProvider{schemas.Anthropic, schemas.OpenAI}
	if !slices.Equal(got, want) {
		t.Errorf("Providers() = %v, want %v", got, want)
	}
}

// TestProviders_StandardWithoutKeys verifies a standard provider with no
// enabled keys is dropped from the enumeration.
func TestProviders_StandardWithoutKeys(t *testing.T) {
	s, _ := newStoreFromFixture(map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {{ID: "k1", Enabled: ptrBool(false), Models: schemas.WhiteList{"*"}}},
	})
	if got := s.Providers(); len(got) != 0 {
		t.Errorf("Providers() = %v, want [] (all keys disabled)", got)
	}
}

// TestProviders_KeylessNonStandardIncluded covers the keyless custom-provider
// branch: a non-standard provider with zero keys is still routable
// (ambient/IAM auth) and must appear in Providers(). buildState marks it
// unrestricted via the allModelsAllowed branch.
func TestProviders_KeylessNonStandardIncluded(t *testing.T) {
	custom := schemas.ModelProvider("my-custom-bedrock")
	s, _ := newStoreFromFixture(map[schemas.ModelProvider][]schemas.Key{custom: nil})
	got := s.Providers()
	if !slices.Contains(got, custom) {
		t.Errorf("Providers() = %v, want to include %q (keyless non-standard provider)", got, custom)
	}
	if !s.IsAllowed(custom, "anything") {
		t.Error("IsAllowed on keyless non-standard provider = false, want true (allModelsAllowed)")
	}
}

// TestBlacklistIntersection_CaseInsensitive verifies the count-bucket
// normalisation: two keys blocking the same model under different casing
// must aggregate as one entry so the cross-key intersection reaches
// enabledKeysCount and the model gets promoted to the provider-wide blacklist.
// Without strings.ToLower in the count map, this would silently fail.
func TestBlacklistIntersection_CaseInsensitive(t *testing.T) {
	s, _ := newStoreFromFixture(map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {
			{ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"*"}, BlacklistedModels: schemas.BlackList{"gpt-4o"}},
			{ID: "k2", Enabled: ptrBool(true), Models: schemas.WhiteList{"*"}, BlacklistedModels: schemas.BlackList{"GPT-4o"}},
		},
	})
	if s.IsAllowed(schemas.OpenAI, "gpt-4o") {
		t.Error("IsAllowed(gpt-4o) = true; want false (both keys block under different casing, intersection should still fire)")
	}
	if s.IsAllowed(schemas.OpenAI, "GpT-4O") {
		t.Error("IsAllowed(GpT-4O) = true; want false (BlackList.IsBlocked is case-insensitive)")
	}
}

// TestIsAllowed_NoRoutableKey verifies IsAllowed reflects actual routability,
// not just the coarse aggregated allow/block: when one key is unrestricted but
// blacklists a model and another key simply doesn't list it, no key can serve
// the model, so IsAllowed must return false (matching KeysAllowingModel) rather
// than passing it through the gate only for routing to then fail.
func TestIsAllowed_NoRoutableKey(t *testing.T) {
	s, _ := newStoreFromFixture(map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {
			{ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"*"}, BlacklistedModels: schemas.BlackList{"gpt-4o-mini"}},
			{ID: "k2", Enabled: ptrBool(true), Models: schemas.WhiteList{"gpt-4o"}},
		},
	})
	if s.IsAllowed(schemas.OpenAI, "gpt-4o-mini") {
		t.Error("IsAllowed(gpt-4o-mini) = true, want false (k1 blacklists it, k2 doesn't list it — no routable key)")
	}
	if got := s.KeysAllowingModel(schemas.OpenAI, "gpt-4o-mini"); len(got) != 0 {
		t.Errorf("KeysAllowingModel(gpt-4o-mini) = %v, want empty — must agree with IsAllowed", got)
	}
	// gpt-4o is routable via k1 (unrestricted, not blacklisted) and k2.
	if !s.IsAllowed(schemas.OpenAI, "gpt-4o") {
		t.Error("IsAllowed(gpt-4o) = false, want true (servable by k1 and k2)")
	}
}

// TestBlacklistedFor_PreservesOriginalCasing verifies the provider-level
// blacklist emits the original casing of the first key that blacklisted the
// model (matching AllowedFor), even though the cross-key intersection is
// computed case-insensitively.
func TestBlacklistedFor_PreservesOriginalCasing(t *testing.T) {
	s, _ := newStoreFromFixture(map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {
			{ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"*"}, BlacklistedModels: schemas.BlackList{"GPT-4o"}},
			{ID: "k2", Enabled: ptrBool(true), Models: schemas.WhiteList{"*"}, BlacklistedModels: schemas.BlackList{"gpt-4o"}},
		},
	})
	bl := s.BlacklistedFor(schemas.OpenAI)
	if !slices.Equal(bl, schemas.BlackList{"GPT-4o"}) {
		t.Errorf("BlacklistedFor = %v, want [GPT-4o] (original casing from first key, not lowercased)", bl)
	}
}

// TestAliasIndex_CaseInsensitive_CollisionDetected verifies the alias-name
// normalisation: two keys defining the same alias under different casing
// must collide so the last-wins warning fires AND only one entry lands in
// the index. Without strings.ToLower, both would persist as separate entries
// and the collision warning would never fire.
func TestAliasIndex_CaseInsensitive_CollisionDetected(t *testing.T) {
	s, log := newStoreFromFixture(map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {
			{
				ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"*"},
				Aliases: schemas.KeyAliases{"My-Prod": schemas.AliasConfig{ModelID: "from-k1"}},
			},
			{
				ID: "k2", Enabled: ptrBool(true), Models: schemas.WhiteList{"*"},
				Aliases: schemas.KeyAliases{"my-prod": schemas.AliasConfig{ModelID: "from-k2"}},
			},
		},
	})
	if log.DebugCount() == 0 {
		t.Error("expected collision debug log on case-different aliases across keys, got none")
	}
	owner, ok := s.ResolveAlias(schemas.OpenAI, "MY-PROD")
	if !ok {
		t.Fatal("ResolveAlias(MY-PROD) = (_, false); want a match (lookup is case-insensitive)")
	}
	if owner.KeyID != "k2" {
		t.Errorf("owner.KeyID = %q, want k2 (last key in slice wins after case-normalisation)", owner.KeyID)
	}
}

// TestResolveAlias_CaseInsensitiveLookup verifies the lookup side of the
// case-normalisation contract — an alias defined as "best-claude" must
// resolve regardless of the case used at the call site.
func TestResolveAlias_CaseInsensitiveLookup(t *testing.T) {
	s, _ := newStoreFromFixture(map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {
			{
				ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"*"},
				Aliases: schemas.KeyAliases{"best-claude": schemas.AliasConfig{ModelID: "claude-sonnet"}},
			},
		},
	})
	for _, q := range []string{"best-claude", "BEST-CLAUDE", "Best-Claude"} {
		owner, ok := s.ResolveAlias(schemas.OpenAI, q)
		if !ok || owner.KeyID != "k1" {
			t.Errorf("ResolveAlias(%q) = (%+v, %v); want owner k1, true", q, owner, ok)
		}
	}
}

// TestNewNilLogger_DoesNotPanic exercises the NoOpLogger default path. If
// New(nil) left logger as nil, the alias-collision Debug call would deref
// nil and crash the test.
func TestNewNilLogger_DoesNotPanic(t *testing.T) {
	s := New(nil)
	s.Replace(map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {
			{
				ID: "k1", Enabled: ptrBool(true), Models: schemas.WhiteList{"*"},
				Aliases: schemas.KeyAliases{"my-prod": schemas.AliasConfig{ModelID: "a"}},
			},
			{
				ID: "k2", Enabled: ptrBool(true), Models: schemas.WhiteList{"*"},
				Aliases: schemas.KeyAliases{"my-prod": schemas.AliasConfig{ModelID: "b"}},
			},
		},
	})
	if _, ok := s.ResolveAlias(schemas.OpenAI, "my-prod"); !ok {
		t.Error("ResolveAlias = false; want true (Replace completed without panic)")
	}
}

// TestReplace_AtomicSnapshot verifies that concurrent readers during a
// Replace never observe a mid-resync provider count. Without atomic
// publish (e.g. the prior sync.Map mutate-in-place design) readers could
// see a count between len(old) and len(new). With the build-then-swap
// design every snapshot read returns either the old or the new size.
func TestReplace_AtomicSnapshot(t *testing.T) {
	old := map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI:    {{ID: "k", Enabled: ptrBool(true), Models: schemas.WhiteList{"*"}}},
		schemas.Anthropic: {{ID: "k", Enabled: ptrBool(true), Models: schemas.WhiteList{"*"}}},
		schemas.Cohere:    {{ID: "k", Enabled: ptrBool(true), Models: schemas.WhiteList{"*"}}},
		schemas.Gemini:    {{ID: "k", Enabled: ptrBool(true), Models: schemas.WhiteList{"*"}}},
	}
	next := map[schemas.ModelProvider][]schemas.Key{
		schemas.Bedrock: {{ID: "k", Enabled: ptrBool(true), Models: schemas.WhiteList{"*"}}},
	}
	s, _ := newStoreFromFixture(old)
	oldSize, nextSize := len(old), len(next)

	stop := make(chan struct{})
	var reader sync.WaitGroup
	reader.Add(1)
	bad := make(chan int, 1)
	go func() {
		defer reader.Done()
		for {
			select {
			case <-stop:
				return
			default:
				n := len(s.Providers())
				if n != oldSize && n != nextSize {
					select {
					case bad <- n:
					default:
					}
					return
				}
			}
		}
	}()

	for i := 0; i < 200; i++ {
		s.Replace(next)
		s.Replace(old)
	}
	close(stop)
	reader.Wait()
	select {
	case n := <-bad:
		t.Errorf("reader observed torn snapshot size %d (want %d or %d)", n, oldSize, nextSize)
	default:
	}
}
