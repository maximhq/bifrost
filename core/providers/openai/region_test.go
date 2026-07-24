package openai

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestNewOpenAIProvider_RegionBaseURL verifies the OpenAI regional-processing
// region drives the default base URL: an "eu" region with no explicit base_url
// resolves to the EU data-residency host, an explicit base_url always wins, and
// the default/unset region falls back to the global endpoint.
func TestNewOpenAIProvider_RegionBaseURL(t *testing.T) {
	cases := []struct {
		name     string
		region   string
		baseURL  string
		wantBase string
	}{
		{"eu region defaults to eu host", "eu", "", "https://eu.api.openai.com"},
		{"no region defaults to global host", "", "", "https://api.openai.com"},
		{"unknown region defaults to global host", "apac", "", "https://api.openai.com"},
		{"explicit base_url overrides eu region", "eu", "https://proxy.internal/openai", "https://proxy.internal/openai"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &schemas.ProviderConfig{
				NetworkConfig: schemas.NetworkConfig{BaseURL: tc.baseURL},
				OpenAIConfig:  &schemas.OpenAIConfig{Region: tc.region},
			}
			p := NewOpenAIProvider(cfg, testNoopLogger{})
			if p.networkConfig.BaseURL != tc.wantBase {
				t.Fatalf("BaseURL = %q, want %q", p.networkConfig.BaseURL, tc.wantBase)
			}
		})
	}
}

// TestResolveBaseURL_ContextRegion verifies per-request base-URL resolution:
// a per-key/per-request region stashed in the context (as core does) overrides
// the provider-level region, an explicit operator base_url always wins, and the
// provider region is the fallback when the context carries none.
func TestResolveBaseURL_ContextRegion(t *testing.T) {
	newProvider := func(baseURL, providerRegion string) *OpenAIProvider {
		return NewOpenAIProvider(&schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{BaseURL: baseURL},
			OpenAIConfig:  &schemas.OpenAIConfig{Region: providerRegion},
		}, testNoopLogger{})
	}
	ctxWithRegion := func(region string) *schemas.BifrostContext {
		ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
		if region != "" {
			ctx.SetValue(schemas.BifrostContextKeyOpenAIRegion, region)
		}
		return ctx
	}

	cases := []struct {
		name           string
		baseURL        string
		providerRegion string
		ctxRegion      string
		want           string
	}{
		{"ctx eu region routes to eu host", "", "", "eu", "https://eu.api.openai.com"},
		{"ctx region overrides provider region", "", "us", "eu", "https://eu.api.openai.com"},
		{"no ctx region falls back to provider region", "", "eu", "", "https://eu.api.openai.com"},
		{"no region anywhere → global", "", "", "", "https://api.openai.com"},
		{"explicit base_url wins over ctx region", "https://proxy.internal/openai", "", "eu", "https://proxy.internal/openai"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newProvider(tc.baseURL, tc.providerRegion)
			if got := p.resolveBaseURL(ctxWithRegion(tc.ctxRegion)); got != tc.want {
				t.Fatalf("resolveBaseURL = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestResolveBaseURLForKey verifies the key-driven variant used by realtime /
// WebSocket URL builders: the key's region overrides the provider region, and an
// explicit base_url still wins.
func TestResolveBaseURLForKey(t *testing.T) {
	keyWithRegion := func(region string) schemas.Key {
		k := schemas.Key{}
		if region != "" {
			k.OpenAIKeyConfig = &schemas.OpenAIKeyConfig{Region: region}
		}
		return k
	}
	cases := []struct {
		name           string
		baseURL        string
		providerRegion string
		keyRegion      string
		want           string
	}{
		{"key eu region routes to eu host", "", "", "eu", "https://eu.api.openai.com"},
		{"key region overrides provider region", "", "us", "eu", "https://eu.api.openai.com"},
		{"no key region falls back to provider region", "", "eu", "", "https://eu.api.openai.com"},
		{"explicit base_url wins over key region", "https://proxy.internal/openai", "", "eu", "https://proxy.internal/openai"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := NewOpenAIProvider(&schemas.ProviderConfig{
				NetworkConfig: schemas.NetworkConfig{BaseURL: tc.baseURL},
				OpenAIConfig:  &schemas.OpenAIConfig{Region: tc.providerRegion},
			}, testNoopLogger{})
			if got := p.resolveBaseURLForKey(keyWithRegion(tc.keyRegion)); got != tc.want {
				t.Fatalf("resolveBaseURLForKey = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestOpenAIRegionBaseURL verifies the region→host map, including case/space
// normalization and the empty default.
func TestOpenAIRegionBaseURL(t *testing.T) {
	for region, want := range map[string]string{
		"eu":   "https://eu.api.openai.com",
		"EU":   "https://eu.api.openai.com",
		" eu ": "https://eu.api.openai.com",
		"us":   "",
		"":     "",
		"apac": "",
	} {
		if got := schemas.OpenAIRegionBaseURL(region); got != want {
			t.Errorf("OpenAIRegionBaseURL(%q) = %q, want %q", region, got, want)
		}
	}
}
