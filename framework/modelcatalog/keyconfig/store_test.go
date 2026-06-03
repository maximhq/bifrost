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
	mu    sync.Mutex
	warns []string
}

func (l *recordingLogger) Debug(format string, args ...any) {}
func (l *recordingLogger) Info(format string, args ...any)  {}
func (l *recordingLogger) Warn(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns = append(l.warns, format)
}
func (l *recordingLogger) Error(format string, args ...any)                  {}
func (l *recordingLogger) Fatal(format string, args ...any)                  {}
func (l *recordingLogger) SetLevel(level schemas.LogLevel)                   {}
func (l *recordingLogger) SetOutputType(outputType schemas.LoggerOutputType) {}
func (l *recordingLogger) LogHTTPRequest(level schemas.LogLevel, msg string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func (l *recordingLogger) WarnCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.warns)
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

func TestAliasCollisionLastEnabledWinsAndWarns(t *testing.T) {
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
	if owner.KeyID != "k1" && owner.KeyID != "k2" {
		t.Errorf("owner.KeyID = %q, want k1 or k2", owner.KeyID)
	}
	if log.WarnCount() == 0 {
		t.Error("expected collision warning, got none")
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
