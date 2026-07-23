package azure

import (
	"encoding/json"
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

// TestResolvePassthroughAlias verifies that the user-facing alias name is
// replaced by the key's wire deployment (alias model_id) in both the
// /deployments/{name} path segment and the top-level "model" body field, so
// aliased models work through /azure_passthrough. Each retry attempt carries
// its own key's ResolvedAlias in ctx, which is what lets multiple keys that
// share an alias load-balance to their own deployments.
func TestResolvePassthroughAlias(t *testing.T) {
	t.Parallel()

	const alias = "mistral-ocr-3-0"
	const deployment = "mistral-document-ai-2512-1-swedencentral-14"

	aliasCtx := func() *schemas.BifrostContext {
		ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{
			Key:    alias,
			Config: &schemas.AliasConfig{ModelID: deployment},
		})
		return ctx
	}

	t.Run("no resolved alias: path and body unchanged", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"model":"` + alias + `"}`)
		gotPath, gotBody := resolvePassthroughAlias(nil, "/openai/deployments/"+alias+"/chat/completions", body)
		if gotPath != "/openai/deployments/"+alias+"/chat/completions" || string(gotBody) != string(body) {
			t.Errorf("expected no-op, got path=%q body=%s", gotPath, gotBody)
		}
		emptyCtx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
		gotPath, gotBody = resolvePassthroughAlias(emptyCtx, "/openai/deployments/"+alias+"/chat/completions", body)
		if gotPath != "/openai/deployments/"+alias+"/chat/completions" || string(gotBody) != string(body) {
			t.Errorf("expected no-op with empty ctx, got path=%q body=%s", gotPath, gotBody)
		}
	})

	t.Run("deployments path segment rewritten", func(t *testing.T) {
		t.Parallel()
		gotPath, _ := resolvePassthroughAlias(aliasCtx(), "/openai/deployments/"+alias+"/chat/completions", nil)
		want := "/openai/deployments/" + deployment + "/chat/completions"
		if gotPath != want {
			t.Errorf("\ngot:  %s\nwant: %s", gotPath, want)
		}
	})

	t.Run("deployments segment at end of path rewritten", func(t *testing.T) {
		t.Parallel()
		gotPath, _ := resolvePassthroughAlias(aliasCtx(), "/openai/deployments/"+alias, nil)
		if want := "/openai/deployments/" + deployment; gotPath != want {
			t.Errorf("\ngot:  %s\nwant: %s", gotPath, want)
		}
	})

	t.Run("deployments segment case-insensitive match", func(t *testing.T) {
		t.Parallel()
		gotPath, _ := resolvePassthroughAlias(aliasCtx(), "/openai/deployments/Mistral-OCR-3-0/embeddings", nil)
		if want := "/openai/deployments/" + deployment + "/embeddings"; gotPath != want {
			t.Errorf("\ngot:  %s\nwant: %s", gotPath, want)
		}
	})

	t.Run("path with different deployment untouched", func(t *testing.T) {
		t.Parallel()
		path := "/openai/deployments/gpt-4o/chat/completions"
		gotPath, _ := resolvePassthroughAlias(aliasCtx(), path, nil)
		if gotPath != path {
			t.Errorf("got %q, want unchanged %q", gotPath, path)
		}
	})

	t.Run("body model field rewritten, other fields preserved", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"model":"` + alias + `","document":{"type":"document_url","document_url":"data:application/pdf;base64,AAAA"}}`)
		_, gotBody := resolvePassthroughAlias(aliasCtx(), "/providers/mistral/azure/ocr", body)
		var parsed struct {
			Model    string `json:"model"`
			Document struct {
				Type        string `json:"type"`
				DocumentURL string `json:"document_url"`
			} `json:"document"`
		}
		if err := json.Unmarshal(gotBody, &parsed); err != nil {
			t.Fatalf("rewritten body is not valid JSON: %v", err)
		}
		if parsed.Model != deployment {
			t.Errorf("model = %q, want %q", parsed.Model, deployment)
		}
		if parsed.Document.Type != "document_url" || parsed.Document.DocumentURL != "data:application/pdf;base64,AAAA" {
			t.Errorf("sibling fields not preserved: %+v", parsed.Document)
		}
	})

	t.Run("body with different model untouched", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"model":"gpt-4o"}`)
		_, gotBody := resolvePassthroughAlias(aliasCtx(), "/openai/v1/chat/completions", body)
		if string(gotBody) != string(body) {
			t.Errorf("got %s, want unchanged %s", gotBody, body)
		}
	})

	t.Run("body without model field untouched", func(t *testing.T) {
		t.Parallel()
		body := []byte(`{"input":"hello"}`)
		_, gotBody := resolvePassthroughAlias(aliasCtx(), "/openai/v1/embeddings", body)
		if string(gotBody) != string(body) {
			t.Errorf("got %s, want unchanged %s", gotBody, body)
		}
	})

	// Multipart bodies must not reach sjson.SetBytes: given a non-JSON input it
	// discards the body and returns a bare {"model":...} object, which would
	// destroy the uploaded file. The leading-'{' guard is what prevents that.
	t.Run("multipart body with model form field untouched", func(t *testing.T) {
		t.Parallel()
		body := []byte("--b\r\nContent-Disposition: form-data; name=\"model\"\r\n\r\n" +
			alias + "\r\n--b\r\nContent-Disposition: form-data; name=\"file\"; filename=\"a.mp3\"\r\n\r\nBINARY\r\n--b--")
		_, gotBody := resolvePassthroughAlias(aliasCtx(), "/openai/deployments/"+alias+"/audio/transcriptions", body)
		if string(gotBody) != string(body) {
			t.Errorf("got %s, want unchanged %s", gotBody, body)
		}
	})

	t.Run("per-key aliases resolve to their own deployments", func(t *testing.T) {
		t.Parallel()
		// Two keys sharing the alias map it to different regional deployments —
		// the ctx carries the attempt's own resolution, so rotation load-balances.
		for _, dep := range []string{"mistral-doc-ai-swedencentral", "mistral-doc-ai-eastus"} {
			ctx := schemas.NewBifrostContext(nil, schemas.NoDeadline)
			ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{
				Key:    alias,
				Config: &schemas.AliasConfig{ModelID: dep},
			})
			_, gotBody := resolvePassthroughAlias(ctx, "/providers/mistral/azure/ocr", []byte(`{"model":"`+alias+`"}`))
			var parsed struct {
				Model string `json:"model"`
			}
			if err := json.Unmarshal(gotBody, &parsed); err != nil || parsed.Model != dep {
				t.Errorf("model = %q (err=%v), want %q", parsed.Model, err, dep)
			}
		}
	})
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
