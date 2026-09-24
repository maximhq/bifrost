package configstore

import (
	"github.com/bytedance/sonic"
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
