package configstore

import (
	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

// TestGenerateFrameworkConfigHash_NilLiveIntervalPreservesLegacyDigest pins the
// back-compat contract for adding fields to the framework config hash.
//
// The hash is how ResolveFrameworkPricingConfig decides whether config.json
// changed since it was last applied. If adding a field shifted the digest for
// deployments that do not set it, every upgraded gateway would see a spurious
// "file changed" on first boot and let config.json stomp values the operator
// had edited through the UI. LiveModelsSyncInterval carries `omitempty` so a
// nil pointer marshals to exactly the bytes the struct produced before the
// field existed.
//
// The literals below are deliberately hardcoded rather than recomputed: a test
// that derives the expected value from the same code it is testing would
// happily follow a regression.
func TestGenerateFrameworkConfigHash_NilLiveIntervalPreservesLegacyDigest(t *testing.T) {
	pricingURL := "https://example.com/pricing.json"
	modelParamsURL := "https://example.com/params.json"
	syncInterval := int64(86400)

	t.Run("pricing-only payload", func(t *testing.T) {
		got, err := GenerateFrameworkConfigHash(&pricingURL, &modelParamsURL, &syncInterval)
		if err != nil {
			t.Fatalf("GenerateFrameworkConfigHash returned error: %v", err)
		}
		const want = "4ee61c7fef9dfd0036fbe7973584297c2ed00b9d8c3a5a05ba0e10d34340209d"
		if got != want {
			t.Fatalf("pricing-only digest changed.\n got: %s\nwant: %s\n\nAdding a field to frameworkConfigHashPayload without `omitempty` (or removing it) breaks config.json change detection for every existing deployment.", got, want)
		}
	})

	t.Run("explicit nil live interval matches the no-options form", func(t *testing.T) {
		withoutOpts, err := GenerateFrameworkConfigHash(&pricingURL, &modelParamsURL, &syncInterval)
		if err != nil {
			t.Fatalf("GenerateFrameworkConfigHash returned error: %v", err)
		}
		withNilOpts, err := GenerateFrameworkConfigHash(&pricingURL, &modelParamsURL, &syncInterval, FrameworkConfigHashOptions{})
		if err != nil {
			t.Fatalf("GenerateFrameworkConfigHash returned error: %v", err)
		}
		if withoutOpts != withNilOpts {
			t.Fatalf("an all-nil options struct must hash identically to omitting options entirely:\n  without: %s\n  with:    %s", withoutOpts, withNilOpts)
		}
	})

	t.Run("mcp payload is unchanged by a nil live interval", func(t *testing.T) {
		mcpURL := "https://example.com/mcp.json"
		mcpInterval := int64(86400)

		mcpOnly, err := GenerateFrameworkConfigHash(&pricingURL, &modelParamsURL, &syncInterval, FrameworkConfigHashOptions{
			MCPLibraryURL:          &mcpURL,
			MCPLibrarySyncInterval: &mcpInterval,
		})
		if err != nil {
			t.Fatalf("GenerateFrameworkConfigHash returned error: %v", err)
		}
		const want = "8dbe17439dad62ba0ac9d63bd2dbe6413ad46749c7d18739887c29a6a7cb5652"
		if mcpOnly != want {
			t.Fatalf("mcp payload digest changed.\n got: %s\nwant: %s\n\nExisting deployments with mcp_library_* in config.json rely on this digest staying stable.", mcpOnly, want)
		}
	})

	t.Run("setting the live interval does change the digest", func(t *testing.T) {
		// The flip side of the contract: an operator editing the value in
		// config.json must be detected as a file change.
		base, err := GenerateFrameworkConfigHash(&pricingURL, &modelParamsURL, &syncInterval)
		if err != nil {
			t.Fatalf("GenerateFrameworkConfigHash returned error: %v", err)
		}
		liveInterval := int64(900)
		withLive, err := GenerateFrameworkConfigHash(&pricingURL, &modelParamsURL, &syncInterval, FrameworkConfigHashOptions{
			LiveModelsSyncInterval: &liveInterval,
		})
		if err != nil {
			t.Fatalf("GenerateFrameworkConfigHash returned error: %v", err)
		}
		if base == withLive {
			t.Fatal("expected setting live_models_sync_interval to change the digest; a config.json edit would otherwise go undetected")
		}
	})
}

// ruleWithFallbacks decodes a routing fixture through the persisted fallback wire format.
func ruleWithFallbacks(t *testing.T, fallbacksJSON string) tables.TableRoutingRule {
	t.Helper()
	var parsed []tables.RoutingFallback
	require.NoError(t, sonic.Unmarshal([]byte(fallbacksJSON), &parsed))
	return tables.TableRoutingRule{
		ID:              "rule-1",
		Name:            "route gpt-4o",
		CelExpression:   `model == "gpt-4o"`,
		Scope:           "global",
		Targets:         []tables.TableRoutingTarget{{RuleID: "rule-1", Weight: 1}},
		ParsedFallbacks: parsed,
	}
}

// TestGenerateRoutingRuleHash_LegacyFallbacksPreserveByteShape proves a config-origin rule with legacy-string fallbacks still hashes to its pre-change digest.
func TestGenerateRoutingRuleHash_LegacyFallbacksPreserveByteShape(t *testing.T) {
	for _, fallbacksJSON := range []string{
		`["openai/gpt-4o"]`,
		`["azure/"]`,
		`["anthropic"]`,
		`["openai/gpt-4o","azure/","vertex/gemini-2.5-pro"]`,
	} {
		t.Run(fallbacksJSON, func(t *testing.T) {
			rule := ruleWithFallbacks(t, fallbacksJSON)

			fromParsed, err := GenerateRoutingRuleHash(rule)
			require.NoError(t, err)

			// The DB-origin path hashes the raw column verbatim; the config-origin path marshals ParsedFallbacks, and the two must agree.
			raw := fallbacksJSON
			dbRule := rule
			dbRule.Fallbacks = &raw
			fromRaw, err := GenerateRoutingRuleHash(dbRule)
			require.NoError(t, err)

			assert.Equal(t, fromRaw, fromParsed, "config-origin hash drifted from the DB-origin hash")
		})
	}
}

// TestGenerateRoutingRuleHash_PinnedFallbackChangesHash detects changes to a configured key pin.
func TestGenerateRoutingRuleHash_PinnedFallbackChangesHash(t *testing.T) {
	unpinned, err := GenerateRoutingRuleHash(ruleWithFallbacks(t, `["azure/gpt-4o"]`))
	require.NoError(t, err)

	pinned, err := GenerateRoutingRuleHash(ruleWithFallbacks(t, `[{"provider":"azure","model":"gpt-4o","key_id":"k1"}]`))
	require.NoError(t, err)

	assert.NotEqual(t, unpinned, pinned, "pinning a key must change the rule hash")
}

// TestGenerateRoutingRuleHash_FallbackFormsStableAcrossRestart pins that the config-origin and
// DB-origin hashes agree and that saving and reloading a rule does not change its hash or stored
// column. A drift here rewrites the rule from config.json on every boot.
func TestGenerateRoutingRuleHash_FallbackFormsStableAcrossRestart(t *testing.T) {
	for _, fallbacksJSON := range []string{
		`["anthropic/claude-sonnet-4"]`,
		`["azure/"]`,
		`[{"provider":"vertex","model":"gemini-2.5-pro","key_id":"k1"}]`,
		`[{"provider":"vertex","model":"","key_id":"k1"}]`,
		`["openai/gpt-4o",{"provider":"vertex","model":"gemini-2.5-pro","key_id":"k1"},"azure/"]`,
	} {
		t.Run(fallbacksJSON, func(t *testing.T) {
			rule := ruleWithFallbacks(t, fallbacksJSON)
			fromConfig, err := GenerateRoutingRuleHash(rule)
			require.NoError(t, err)

			saved := rule
			require.NoError(t, saved.BeforeSave(nil))
			require.NotNil(t, saved.Fallbacks)
			fromDB, err := GenerateRoutingRuleHash(saved)
			require.NoError(t, err)
			assert.Equal(t, fromConfig, fromDB, "config-origin and DB-origin hashes disagree")

			reloaded := tables.TableRoutingRule{ID: saved.ID, Name: saved.Name, CelExpression: saved.CelExpression, Scope: saved.Scope, Targets: saved.Targets, Fallbacks: saved.Fallbacks}
			require.NoError(t, reloaded.AfterFind(nil))
			require.NoError(t, reloaded.BeforeSave(nil))
			assert.Equal(t, *saved.Fallbacks, *reloaded.Fallbacks, "re-saving a reloaded rule changed the stored column")
			afterRestart, err := GenerateRoutingRuleHash(reloaded)
			require.NoError(t, err)
			assert.Equal(t, fromConfig, afterRestart, "hash changed across a restart")
		})
	}
}

// TestGenerateRoutingRuleHash_FallbackIdentity pins which fallback edits change the hash: the key
// pin, provider and model do; spelling an unpinned entry as an object instead of a string does not.
func TestGenerateRoutingRuleHash_FallbackIdentity(t *testing.T) {
	hash := func(fallbacksJSON string) string {
		h, err := GenerateRoutingRuleHash(ruleWithFallbacks(t, fallbacksJSON))
		require.NoError(t, err)
		return h
	}
	base := hash(`[{"provider":"vertex","model":"m","key_id":"k1"}]`)
	assert.NotEqual(t, base, hash(`[{"provider":"vertex","model":"m","key_id":"k2"}]`), "a different key pin must change the hash")
	assert.NotEqual(t, base, hash(`[{"provider":"vertex","model":"other","key_id":"k1"}]`), "a different model must change the hash")
	assert.NotEqual(t, base, hash(`[{"provider":"azure","model":"m","key_id":"k1"}]`), "a different provider must change the hash")
	assert.Equal(t, hash(`["vertex/m"]`), hash(`[{"provider":"vertex","model":"m"}]`), "an unpinned object must hash like its string form")
	assert.Equal(t, hash(`["anthropic/"]`), hash(`[{"provider":"anthropic"}]`), "an unpinned provider-only object must hash like its string form")
}

// TestGenerateConfigHash_DeterministicWithMaps guards against hash inputs
// depending on Go's randomized map iteration order. sonic.Marshal does not sort
// map keys, so a provider with several extra_headers / request_path_overrides
// used to get a new ConfigHash on every call; on boot an unchanged config.json
// then looked edited and DB-only keys were dropped.
func TestGenerateConfigHash_DeterministicWithMaps(t *testing.T) {
	cfg := ProviderConfig{
		NetworkConfig: &schemas.NetworkConfig{
			ExtraHeaders:        map[string]string{"a": "1", "b": "2", "c": "3", "d": "4", "e": "5"},
			BetaHeaderOverrides: map[string]bool{"w-": true, "x-": false, "y-": true, "z-": false},
		},
		CustomProviderConfig: &schemas.CustomProviderConfig{
			BaseProviderType: schemas.OpenAI,
			RequestPathOverrides: map[schemas.RequestType]string{
				schemas.ChatCompletionRequest: "/a",
				schemas.ResponsesRequest:      "/b",
				schemas.EmbeddingRequest:      "/c",
				schemas.ListModelsRequest:     "/d",
			},
		},
	}

	want, err := cfg.GenerateConfigHash("custom")
	require.NoError(t, err)
	for i := 0; i < 200; i++ {
		got, err := cfg.GenerateConfigHash("custom")
		require.NoError(t, err)
		require.Equal(t, want, got, "hash changed between calls on an identical config (run %d)", i)
	}
}

// TestHashJSON_MatchesSonicDefaultWithoutMultiEntryMaps pins the back-compat
// contract of hashJSON: sorting map keys must not change the bytes for inputs
// that were already stable (no map, or a single-entry map), otherwise every
// upgraded deployment would see a spurious "file changed" on first boot.
func TestHashJSON_MatchesSonicDefaultWithoutMultiEntryMaps(t *testing.T) {
	inputs := []any{
		schemas.NetworkConfig{BaseURL: "https://example.com", ExtraHeaders: map[string]string{"x-a": "1"}},
		schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI, IsKeyLess: true},
		schemas.KeyAliases{"best": {ModelID: "gpt-4o"}},
		map[string]any{"html": "<a&b>"},
	}
	for _, in := range inputs {
		want, err := sonic.Marshal(in)
		require.NoError(t, err)
		got, err := hashJSON.Marshal(in)
		require.NoError(t, err)
		assert.Equal(t, string(want), string(got))
	}

	// A provider with nothing configured hashes to sha256 of its name alone.
	got, err := (&ProviderConfig{}).GenerateConfigHash("openai")
	require.NoError(t, err)
	assert.Equal(t, "7d3194f79e645c42e4396dda38be04766810ec6a00d00aced3ffc2a0a1f1a9ef", got)
}
