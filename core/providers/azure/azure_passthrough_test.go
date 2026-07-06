package azure

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestBuildPassthroughURL(t *testing.T) {
	t.Parallel()

	provider := &AzureProvider{}
	endpoint := "https://my-resource.openai.azure.com"

	makeKey := func(endpoint string) schemas.Key {
		return schemas.Key{
			AzureKeyConfig: &schemas.AzureKeyConfig{
				Endpoint: *schemas.NewSecretVar(endpoint),
			},
		}
	}

	tests := []struct {
		name     string
		path     string
		rawQuery string
		want     string
	}{
		// --- Path normalisation ---
		{
			name: "normalise /openai/responses to /openai/v1/responses",
			path: "/openai/responses",
			want: endpoint + "/openai/v1/responses?api-version=" + AzureAPIVersionPreview,
		},
		{
			name: "normalise /openai/videos to /openai/v1/videos",
			path: "/openai/videos",
			want: endpoint + "/openai/v1/videos",
		},

		// --- /openai/deployments/ — inject default api-version when absent ---
		{
			name: "deployments route: inject default api-version when missing",
			path: "/openai/deployments/gpt-4o/chat/completions",
			want: endpoint + "/openai/deployments/gpt-4o/chat/completions?api-version=" + DefaultAzureAPIVersion,
		},
		{
			name:     "deployments route: preserve caller-supplied api-version",
			path:     "/openai/deployments/gpt-4o/chat/completions",
			rawQuery: "api-version=2024-10-21",
			want:     endpoint + "/openai/deployments/gpt-4o/chat/completions?api-version=2024-10-21",
		},
		{
			name:     "deployments route: preserve caller api-version alongside other params",
			path:     "/openai/deployments/gpt-4o/chat/completions",
			rawQuery: "api-version=2024-06-01&foo=bar",
			want:     endpoint + "/openai/deployments/gpt-4o/chat/completions?api-version=2024-06-01&foo=bar",
		},

		// --- /openai/v1/responses — inject api-version=preview when absent ---
		{
			name: "responses route: inject preview api-version when missing",
			path: "/openai/v1/responses",
			want: endpoint + "/openai/v1/responses?api-version=" + AzureAPIVersionPreview,
		},
		{
			name:     "responses route: preserve caller-supplied api-version",
			path:     "/openai/v1/responses",
			rawQuery: "api-version=2025-01-01-preview",
			want:     endpoint + "/openai/v1/responses?api-version=2025-01-01-preview",
		},

		// --- Anthropic routes — strip api-version ---
		{
			name:     "anthropic route: strip api-version",
			path:     "/anthropic/v1/messages",
			rawQuery: "api-version=2024-10-21",
			want:     endpoint + "/anthropic/v1/messages",
		},
		{
			name:     "anthropic route: preserve other query params after strip",
			path:     "/anthropic/v1/messages",
			rawQuery: "api-version=preview&foo=bar",
			want:     endpoint + "/anthropic/v1/messages?foo=bar",
		},

		// --- /openai/v1/videos — strip api-version ---
		{
			name:     "videos route: strip api-version",
			path:     "/openai/v1/videos",
			rawQuery: "api-version=2024-10-21",
			want:     endpoint + "/openai/v1/videos",
		},

		// --- Other v1 routes — pass through unchanged ---
		{
			name: "v1 chat completions: no api-version injected",
			path: "/openai/v1/chat/completions",
			want: endpoint + "/openai/v1/chat/completions",
		},
		{
			name:     "v1 chat completions: caller params passed through unchanged",
			path:     "/openai/v1/chat/completions",
			rawQuery: "foo=bar",
			want:     endpoint + "/openai/v1/chat/completions?foo=bar",
		},
		{
			name: "v1 embeddings: no api-version injected",
			path: "/openai/v1/embeddings",
			want: endpoint + "/openai/v1/embeddings",
		},
		{
			name: "v1 audio transcriptions: no api-version injected",
			path: "/openai/v1/audio/transcriptions",
			want: endpoint + "/openai/v1/audio/transcriptions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, _ := provider.buildPassthroughURL(nil, makeKey(endpoint), tt.path, tt.rawQuery)
			if got != tt.want {
				t.Errorf("\ngot:  %s\nwant: %s", got, tt.want)
			}
		})
	}
}

// TestBuildPassthroughURL_AliasAPIVersionOverride verifies that when the
// resolved alias carries an AzureAliasCfg.APIVersion override, it takes
// precedence over the route default (DefaultAzureAPIVersion for /deployments/,
// AzureAPIVersionPreview for /openai/v1/responses) — only in the path where
// the caller did NOT supply api-version themselves. Caller-supplied wins over
// alias override; alias override wins over route default.
func TestBuildPassthroughURL_AliasAPIVersionOverride(t *testing.T) {
	t.Parallel()

	provider := &AzureProvider{}
	endpoint := "https://my-resource.openai.azure.com"
	makeKey := schemas.Key{
		AzureKeyConfig: &schemas.AzureKeyConfig{
			Endpoint: *schemas.NewSecretVar(endpoint),
		},
	}

	// Build a ctx carrying an alias with APIVersion override.
	overrideVer := "2024-10-21"
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{
		Key: "best-model",
		Config: &schemas.AliasConfig{
			ModelID: "gpt-4o-deployment",
			AzureAliasCfg: &schemas.AzureAliasCfg{
				APIVersion: &overrideVer,
			},
		},
	})

	t.Run("deployments route: alias APIVersion overrides default", func(t *testing.T) {
		got, _ := provider.buildPassthroughURL(ctx, makeKey, "/openai/deployments/gpt-4o/chat/completions", "")
		want := endpoint + "/openai/deployments/gpt-4o/chat/completions?api-version=" + overrideVer
		if got != want {
			t.Errorf("\ngot:  %s\nwant: %s", got, want)
		}
	})

	t.Run("responses route: alias APIVersion overrides preview default", func(t *testing.T) {
		got, _ := provider.buildPassthroughURL(ctx, makeKey, "/openai/v1/responses", "")
		want := endpoint + "/openai/v1/responses?api-version=" + overrideVer
		if got != want {
			t.Errorf("\ngot:  %s\nwant: %s", got, want)
		}
	})

	t.Run("caller-supplied api-version wins over alias override", func(t *testing.T) {
		got, _ := provider.buildPassthroughURL(ctx, makeKey, "/openai/deployments/gpt-4o/chat/completions", "api-version=2023-01-01")
		want := endpoint + "/openai/deployments/gpt-4o/chat/completions?api-version=2023-01-01"
		if got != want {
			t.Errorf("\ngot:  %s\nwant: %s", got, want)
		}
	})
}

// TestResolveAPIVersion_NoAlias verifies the helper returns the route default
// when no resolved alias is in ctx (covers the legacy code path).
func TestResolveAPIVersion_NoAlias(t *testing.T) {
	if got := resolveAPIVersion(nil, DefaultAzureAPIVersion); got != DefaultAzureAPIVersion {
		t.Errorf("got %q, want %q", got, DefaultAzureAPIVersion)
	}
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	if got := resolveAPIVersion(ctx, AzureAPIVersionPreview); got != AzureAPIVersionPreview {
		t.Errorf("got %q, want %q", got, AzureAPIVersionPreview)
	}
}

// TestResolveAzureEndpoint_AliasOverride verifies the Endpoint override path.
// Lets one Azure credential cover deployments hosted on multiple cognitive-
// services resources.
func TestResolveAzureEndpoint_AliasOverride(t *testing.T) {
	keyEndpoint := "https://primary.openai.azure.com"
	aliasEndpoint := "https://anthropic-resource.openai.azure.com"
	key := schemas.Key{
		AzureKeyConfig: &schemas.AzureKeyConfig{
			Endpoint: *schemas.NewSecretVar(keyEndpoint),
		},
	}

	// No alias: falls back to key-level endpoint.
	if got := resolveAzureEndpoint(nil, key); got != keyEndpoint {
		t.Errorf("nil ctx: got %q, want key-level %q", got, keyEndpoint)
	}
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	if got := resolveAzureEndpoint(ctx, key); got != keyEndpoint {
		t.Errorf("empty ctx: got %q, want key-level %q", got, keyEndpoint)
	}

	// With alias-level Endpoint override, alias wins.
	override := schemas.NewSecretVar(aliasEndpoint)
	ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{
		Key: "best-claude",
		Config: &schemas.AliasConfig{
			ModelID: "claude-deployment",
			AzureAliasCfg: &schemas.AzureAliasCfg{
				Endpoint: override,
			},
		},
	})
	if got := resolveAzureEndpoint(ctx, key); got != aliasEndpoint {
		t.Errorf("alias override: got %q, want %q", got, aliasEndpoint)
	}

	// Alias with empty Endpoint value falls through to key-level — guards against
	// a misconfigured alias accidentally erasing the endpoint.
	emptyOverride := schemas.NewSecretVar("")
	ctx2 := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	ctx2.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{
		Key: "x",
		Config: &schemas.AliasConfig{
			ModelID: "x",
			AzureAliasCfg: &schemas.AzureAliasCfg{
				Endpoint: emptyOverride,
			},
		},
	})
	if got := resolveAzureEndpoint(ctx2, key); got != keyEndpoint {
		t.Errorf("empty alias endpoint should fall through: got %q, want %q", got, keyEndpoint)
	}
}

// TestResolveAnthropicVersion_AliasOverride verifies the AnthropicVersion
// override path mirrors the APIVersion behavior.
func TestResolveAnthropicVersion_AliasOverride(t *testing.T) {
	if got := resolveAnthropicVersion(nil); got != AzureAnthropicAPIVersionDefault {
		t.Errorf("nil ctx: got %q, want default", got)
	}
	override := "2024-10-22"
	ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{
		Key: "best-claude",
		Config: &schemas.AliasConfig{
			ModelID: "claude-deployment",
			AzureAliasCfg: &schemas.AzureAliasCfg{
				AnthropicVersion: &override,
			},
		},
	})
	if got := resolveAnthropicVersion(ctx); got != override {
		t.Errorf("got %q, want %q", got, override)
	}
}
