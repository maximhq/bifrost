package huggingface

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// Regression tests for https://github.com/maximhq/bifrost/issues/4215.
//
// Allowlist entries selected from a previous ListModels response carry an
// inference-provider segment (e.g. "featherless-ai/org/model"). The backfill
// re-wrap used to blindly prepend the current inference provider, producing
// IDs like "huggingface/cohere/featherless-ai/org/model" that duplicate the
// provider segment and fail request routing.
func TestToBifrostListModelsResponse_AllowlistWithInferenceProviderSegment(t *testing.T) {
	t.Parallel()

	allowlist := schemas.WhiteList{"featherless-ai/deepseek-ai/DeepSeek-V4-Pro"}

	t.Run("matching provider emits single correctly-prefixed entry", func(t *testing.T) {
		t.Parallel()
		response := &HuggingFaceListModelsResponse{
			Models: []HuggingFaceModel{
				{
					ID:          "abc123",
					ModelID:     "deepseek-ai/DeepSeek-V4-Pro",
					PipelineTag: "conversational",
				},
			},
		}

		result := response.ToBifrostListModelsResponse(schemas.HuggingFace, featherlessAI, allowlist, nil, nil, false)
		require.NotNil(t, result)
		require.Len(t, result.Data, 1)
		assert.Equal(t, "huggingface/featherless-ai/deepseek-ai/DeepSeek-V4-Pro", result.Data[0].ID)
	})

	t.Run("other providers do not duplicate the entry", func(t *testing.T) {
		t.Parallel()
		response := &HuggingFaceListModelsResponse{
			Models: []HuggingFaceModel{
				{
					ID:          "def456",
					ModelID:     "CohereLabs/aya-vision-32b",
					PipelineTag: "conversational",
				},
			},
		}

		result := response.ToBifrostListModelsResponse(schemas.HuggingFace, cohere, allowlist, nil, nil, false)
		require.NotNil(t, result)
		assert.Empty(t, result.Data, "an allowlist entry pinned to featherless-ai must not be backfilled under cohere")
	})
}

func TestToBifrostListModelsResponse_AllowlistWithBasetenProviderSegment(t *testing.T) {
	t.Parallel()

	provider := baseten
	assert.Contains(t, INFERENCE_PROVIDERS, provider)

	response := &HuggingFaceListModelsResponse{Models: nil}
	allowlist := schemas.WhiteList{"baseten/zai-org/GLM-5.3-Flash"}

	result := response.ToBifrostListModelsResponse(schemas.HuggingFace, provider, allowlist, nil, nil, false)
	require.NotNil(t, result)
	require.Len(t, result.Data, 1)
	assert.Equal(t, "huggingface/baseten/zai-org/GLM-5.3-Flash", result.Data[0].ID)
}

func TestListModelsByKey_DiscoversBasetenModels(t *testing.T) {
	t.Parallel()

	var requested atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/models", r.URL.Path)
		if r.URL.Query().Get("inference_provider") == string(baseten) {
			requested.Store(true)
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`[{"_id":"glm-5.3-flash","modelId":"zai-org/GLM-5.3-Flash","pipeline_tag":"conversational"}]`))
			assert.NoError(t, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`[]`))
		assert.NoError(t, err)
	}))
	defer server.Close()

	provider := &HuggingFaceProvider{
		client: &fasthttp.Client{
			Dial: func(string) (net.Conn, error) {
				return net.Dial("tcp", server.Listener.Addr().String())
			},
			TLSConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // Test server certificate.
		},
	}
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	key := schemas.Key{Value: *schemas.NewSecretVar(""), Models: schemas.WhiteList{"*"}}

	response, bifrostErr := provider.listModelsByKey(ctx, key, &schemas.BifrostListModelsRequest{})

	require.Nil(t, bifrostErr)
	require.True(t, requested.Load(), "model discovery must query the Baseten inference provider")
	require.NotNil(t, response)
	require.Len(t, response.Data, 1)
	assert.Equal(t, "huggingface/baseten/zai-org/GLM-5.3-Flash", response.Data[0].ID)
}

// Allowlist entries prefixed with the "auto" policy must not be re-prefixed
// with an inference provider: "auto" is owned by no provider pass (the listing
// loop iterates INFERENCE_PROVIDERS, which excludes it). To keep the entry from
// being dropped entirely, it is emitted exactly once during the canonical first
// pass and skipped in every other pass, so it surfaces in the listing without
// being duplicated once per provider. The "auto/" prefix is preserved, so it
// stays routable: splitIntoModelProvider recognizes "auto" as a valid policy.
func TestToBifrostListModelsResponse_AllowlistWithAutoPolicySegment(t *testing.T) {
	t.Parallel()

	allowlist := schemas.WhiteList{"auto/deepseek-ai/DeepSeek-V4-Pro"}

	t.Run("canonical first pass emits the auto-policy entry exactly once", func(t *testing.T) {
		t.Parallel()
		response := &HuggingFaceListModelsResponse{Models: nil}
		result := response.ToBifrostListModelsResponse(schemas.HuggingFace, INFERENCE_PROVIDERS[0], allowlist, nil, nil, false)
		require.NotNil(t, result)
		require.Len(t, result.Data, 1)
		assert.Equal(t, "huggingface/auto/deepseek-ai/DeepSeek-V4-Pro", result.Data[0].ID,
			"the auto-policy entry keeps its prefix and is not re-wrapped with an inference provider")
	})

	t.Run("non-canonical passes do not duplicate the auto-policy entry", func(t *testing.T) {
		t.Parallel()
		require.Greater(t, len(INFERENCE_PROVIDERS), 1)
		nonCanonicalProvider := INFERENCE_PROVIDERS[1]

		response := &HuggingFaceListModelsResponse{Models: nil}
		result := response.ToBifrostListModelsResponse(schemas.HuggingFace, nonCanonicalProvider, allowlist, nil, nil, false)
		require.NotNil(t, result)
		assert.Empty(t, result.Data, "a non-canonical pass must not re-emit the auto-policy entry")
	})
}

// Entries without an inference-provider segment keep the existing backfill
// behavior: the current inference provider is prepended.
func TestToBifrostListModelsResponse_BackfillWithoutInferenceProviderSegment(t *testing.T) {
	t.Parallel()

	response := &HuggingFaceListModelsResponse{Models: nil}
	allowlist := schemas.WhiteList{"deepseek-ai/DeepSeek-V4-Pro"}

	result := response.ToBifrostListModelsResponse(schemas.HuggingFace, featherlessAI, allowlist, nil, nil, false)
	require.NotNil(t, result)
	require.Len(t, result.Data, 1)
	assert.Equal(t, "huggingface/featherless-ai/deepseek-ai/DeepSeek-V4-Pro", result.Data[0].ID)
	// No matching entry in response.Models, so there is nothing to enrich from.
	assert.Nil(t, result.Data[0].HuggingFaceID)
	assert.Empty(t, result.Data[0].SupportedMethods)
}

// A backfilled entry whose model is actually present in response.Models (e.g.
// its provider-prefixed allowlist entry didn't string-match model.ModelID
// during the main filter pass) should still surface HuggingFaceID and
// SupportedMethods, not just ID/Name, since the data is available right there
// in the same response.
func TestToBifrostListModelsResponse_BackfillEnrichesHuggingFaceIDAndSupportedMethods(t *testing.T) {
	t.Parallel()

	allowlist := schemas.WhiteList{"featherless-ai/deepseek-ai/DeepSeek-V4-Pro"}
	response := &HuggingFaceListModelsResponse{
		Models: []HuggingFaceModel{
			{
				ID:          "abc123",
				ModelID:     "deepseek-ai/DeepSeek-V4-Pro",
				PipelineTag: "conversational",
			},
		},
	}

	result := response.ToBifrostListModelsResponse(schemas.HuggingFace, featherlessAI, allowlist, nil, nil, false)
	require.NotNil(t, result)
	require.Len(t, result.Data, 1)

	got := result.Data[0]
	assert.Equal(t, "huggingface/featherless-ai/deepseek-ai/DeepSeek-V4-Pro", got.ID)
	require.NotNil(t, got.HuggingFaceID)
	assert.Equal(t, "abc123", *got.HuggingFaceID)
	assert.ElementsMatch(t, []string{
		string(schemas.ChatCompletionRequest),
		string(schemas.ChatCompletionStreamRequest),
		string(schemas.ResponsesRequest),
		string(schemas.ResponsesStreamRequest),
	}, got.SupportedMethods)
}

// If the backfilled model is present in response.Models but has no
// recognizable pipeline tag/tags, HuggingFaceID is still recovered but
// SupportedMethods stays unset rather than being forced to an empty slice.
func TestToBifrostListModelsResponse_BackfillEnrichesHuggingFaceIDOnlyWhenMethodsUnknown(t *testing.T) {
	t.Parallel()

	allowlist := schemas.WhiteList{"deepseek-ai/DeepSeek-V4-Pro"}
	response := &HuggingFaceListModelsResponse{
		Models: []HuggingFaceModel{
			{
				ID:          "abc123",
				ModelID:     "deepseek-ai/DeepSeek-V4-Pro",
				PipelineTag: "some-unrecognized-pipeline",
			},
		},
	}

	result := response.ToBifrostListModelsResponse(schemas.HuggingFace, featherlessAI, allowlist, nil, nil, false)
	require.NotNil(t, result)
	require.Len(t, result.Data, 1)

	got := result.Data[0]
	require.NotNil(t, got.HuggingFaceID)
	assert.Equal(t, "abc123", *got.HuggingFaceID)
	assert.Nil(t, got.SupportedMethods)
}

// TestListModelsByKeyDiscoversDeepInfraWithoutRetiredProviders pins the Hub
// request set as well as the discovered model IDs, so dropped provider errors
// cannot hide stale catalogue entries.
func TestListModelsByKeyDiscoversDeepInfraWithoutRetiredProviders(t *testing.T) {
	t.Parallel()

	var deepInfraRequested atomic.Bool
	var retiredRequests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/models", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("inference_provider") {
		case "hyperbolic", "nebius", "sambanova":
			retiredRequests.Add(1)
			w.WriteHeader(http.StatusBadRequest)
			_, err := w.Write([]byte(`{"error":"invalid inference provider"}`))
			assert.NoError(t, err)
		case "deepinfra":
			deepInfraRequested.Store(true)
			_, err := w.Write([]byte(`[{"_id":"qwen","modelId":"Qwen/Qwen3.8-27B","pipeline_tag":"conversational"}]`))
			assert.NoError(t, err)
		default:
			_, err := w.Write([]byte(`[]`))
			assert.NoError(t, err)
		}
	}))
	defer server.Close()

	provider := &HuggingFaceProvider{
		client: &fasthttp.Client{
			Dial: func(string) (net.Conn, error) {
				return net.Dial("tcp", server.Listener.Addr().String())
			},
			TLSConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // Test server certificate.
		},
	}
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	key := schemas.Key{Value: *schemas.NewSecretVar(""), Models: schemas.WhiteList{"*"}}
	response, bifrostErr := provider.listModelsByKey(ctx, key, &schemas.BifrostListModelsRequest{})

	require.Nil(t, bifrostErr)
	assert.True(t, deepInfraRequested.Load(), "model discovery must query DeepInfra")
	assert.Zero(t, retiredRequests.Load(), "the Hub rejects these retired inference providers")
	require.NotNil(t, response)
	require.Len(t, response.Data, 1)
	assert.Equal(t, "huggingface/deepinfra/Qwen/Qwen3.8-27B", response.Data[0].ID)
}

// TestToBifrostListModelsResponseAllowlistWithDeepInfraProviderSegment checks
// that a DeepInfra-qualified allowlist ID is emitted only by its own provider
// pass and keeps a single inference-provider prefix.
func TestToBifrostListModelsResponseAllowlistWithDeepInfraProviderSegment(t *testing.T) {
	t.Parallel()
	allowlist := schemas.WhiteList{"deepinfra/Qwen/Qwen3.8-27B"}
	response := &HuggingFaceListModelsResponse{}

	t.Run("matching provider keeps the selected model ID", func(t *testing.T) {
		t.Parallel()
		result := response.ToBifrostListModelsResponse(schemas.HuggingFace, inferenceProvider("deepinfra"), allowlist, nil, nil, false)
		require.NotNil(t, result)
		require.Len(t, result.Data, 1)
		assert.Equal(t, "huggingface/deepinfra/Qwen/Qwen3.8-27B", result.Data[0].ID)
	})
	t.Run("other providers do not backfill the DeepInfra model", func(t *testing.T) {
		t.Parallel()
		result := response.ToBifrostListModelsResponse(schemas.HuggingFace, cohere, allowlist, nil, nil, false)
		require.NotNil(t, result)
		assert.Empty(t, result.Data)
	})
}

// TestToBifrostListModelsResponseLegacyProviderSegments preserves legacy ID
// interpretation even though retired providers are no longer queried by the Hub
// discovery loop.
func TestToBifrostListModelsResponseLegacyProviderSegments(t *testing.T) {
	t.Parallel()
	for _, legacy := range []inferenceProvider{hyperbolic, nebius, sambanova} {
		t.Run(string(legacy), func(t *testing.T) {
			t.Parallel()
			response := &HuggingFaceListModelsResponse{}
			allowlist := schemas.WhiteList{string(legacy) + "/org/model"}
			result := response.ToBifrostListModelsResponse(schemas.HuggingFace, legacy, allowlist, nil, nil, false)
			require.NotNil(t, result)
			require.Len(t, result.Data, 1)
			assert.Equal(t, "huggingface/"+string(legacy)+"/org/model", result.Data[0].ID)
		})
	}
}

// Retired provider IDs must survive the active-only discovery loop, even when
// the Hub returns no models. Calling a retired provider's converter directly
// does not exercise the production path that previously dropped these IDs.
func TestListModelsByKeyPreservesConfiguredLegacyProviderModels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		key            schemas.Key
		expected       []string
		alias          string
		failCanonical  bool
		bareModel      bool
		failAll        bool
		customProvider bool
	}{
		{
			name:      "explicit allowlist preserves retired IDs and existing routing",
			bareModel: true,
			key: schemas.Key{Models: schemas.WhiteList{
				"hyperbolic/org/model", "nebius/org/model", "sambanova/org/model",
				"NeBiUs/org/case-model", "auto/org/auto-model", "deepinfra/org/active-model", "org/bare-model",
			}},
			expected: []string{
				"huggingface/hyperbolic/org/model", "huggingface/nebius/org/model", "huggingface/sambanova/org/model",
				"huggingface/NeBiUs/org/case-model", "huggingface/auto/org/auto-model", "huggingface/deepinfra/org/active-model",
			},
		},
		{
			name: "wildcard allowlist preserves a retired provider alias",
			key: schemas.Key{
				Models:  schemas.WhiteList{"*"},
				Aliases: schemas.KeyAliases{"nebius/org/aliased-model": {ModelID: "org/original-model"}},
			},
			expected: []string{"huggingface/nebius/org/aliased-model"},
			alias:    "org/original-model",
		},
		{
			name: "partial discovery failure preserves retired IDs and auto",
			key: schemas.Key{Models: schemas.WhiteList{
				"hyperbolic/org/model", "nebius/org/model", "sambanova/org/model", "auto/org/auto-model",
			}},
			expected: []string{
				"huggingface/hyperbolic/org/model", "huggingface/nebius/org/model", "huggingface/sambanova/org/model", "huggingface/auto/org/auto-model",
			},
			failCanonical: true,
		},
		{
			name: "blacklist still excludes configured retired models",
			key: schemas.Key{
				Models:            schemas.WhiteList{"nebius/org/model", "sambanova/org/model"},
				BlacklistedModels: schemas.BlackList{"nebius/org/model"},
			},
			expected: []string{"huggingface/sambanova/org/model"},
		},
		{
			name:    "all discovery failures still return an error",
			key:     schemas.Key{Models: schemas.WhiteList{"nebius/org/model"}},
			failAll: true,
		},
		{
			name:           "custom provider keeps its own outer prefix",
			key:            schemas.Key{Models: schemas.WhiteList{"nebius/org/model", "auto/org/model", "deepinfra/org/model"}},
			expected:       []string{"huggingface_custom/nebius/org/model", "huggingface_custom/auto/org/model", "huggingface_custom/deepinfra/org/model"},
			customProvider: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var retiredRequests atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/api/models", r.URL.Path)
				if tt.failAll || (tt.failCanonical && r.URL.Query().Get("inference_provider") == string(INFERENCE_PROVIDERS[0])) {
					w.WriteHeader(http.StatusInternalServerError)
					_, err := w.Write([]byte(`{"error":"temporary Hub failure"}`))
					assert.NoError(t, err)
					return
				}
				switch r.URL.Query().Get("inference_provider") {
				case "hyperbolic", "nebius", "sambanova":
					retiredRequests.Add(1)
				}
				w.Header().Set("Content-Type", "application/json")
				_, err := w.Write([]byte(`[]`))
				assert.NoError(t, err)
			}))
			defer server.Close()

			provider := &HuggingFaceProvider{
				client: &fasthttp.Client{
					Dial: func(string) (net.Conn, error) {
						return net.Dial("tcp", server.Listener.Addr().String())
					},
					TLSConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // Test server certificate.
				},
			}
			if tt.customProvider {
				provider.customProviderConfig = &schemas.CustomProviderConfig{CustomProviderKey: "huggingface_custom", BaseProviderType: schemas.HuggingFace}
			}
			ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
			tt.key.Value = *schemas.NewSecretVar("")
			response, bifrostErr := provider.listModelsByKey(ctx, tt.key, &schemas.BifrostListModelsRequest{})

			if tt.failAll {
				require.NotNil(t, bifrostErr)
				assert.Nil(t, response)
				return
			}
			require.Nil(t, bifrostErr)
			require.NotNil(t, response)
			assert.Zero(t, retiredRequests.Load(), "preserving configured IDs must not query retired Hub providers")
			ids := make([]string, 0, len(response.Data))
			for _, model := range response.Data {
				ids = append(ids, model.ID)
				if tt.alias != "" {
					require.NotNil(t, model.Alias)
					assert.Equal(t, tt.alias, *model.Alias)
				}
			}
			if tt.bareModel {
				for _, inferenceProvider := range INFERENCE_PROVIDERS {
					tt.expected = append(tt.expected, "huggingface/"+string(inferenceProvider)+"/org/bare-model")
				}
			}
			assert.ElementsMatch(t, tt.expected, ids, "configured provider IDs must appear exactly once while bare IDs keep per-provider backfill")
		})
	}
}

func TestToBifrostListModelsResponseDiscoveredQualifiedAliases(t *testing.T) {
	t.Parallel()
	for _, aliasProvider := range []inferenceProvider{hyperbolic, nebius, sambanova, auto, deepInfra} {
		t.Run(string(aliasProvider), func(t *testing.T) {
			t.Parallel()
			aliasKey := string(aliasProvider) + "/org/aliased-model"
			aliases := schemas.KeyAliases{aliasKey: {ModelID: "org/original-model"}}
			response := &HuggingFaceListModelsResponse{Models: []HuggingFaceModel{{
				ID: "original", ModelID: "org/original-model", PipelineTag: "conversational",
			}}}
			var models []schemas.Model
			var originalModels int
			for _, provider := range INFERENCE_PROVIDERS {
				result := response.ToBifrostListModelsResponse(schemas.HuggingFace, provider, schemas.WhiteList{"*"}, nil, aliases, false)
				for _, model := range result.Data {
					if model.Alias != nil {
						models = append(models, model)
					} else {
						originalModels++
					}
				}
			}
			assert.Equal(t, len(INFERENCE_PROVIDERS), originalModels, "wildcard discovery still exposes the original model per active provider")
			require.Equal(t, 1, len(models), "a qualified alias must belong to exactly one discovery pass")
			assert.Equal(t, "huggingface/"+aliasKey, models[0].ID)
			assert.Equal(t, "org/original-model", *models[0].Name)
			assert.Equal(t, "org/original-model", *models[0].Alias)
			assert.Equal(t, "original", *models[0].HuggingFaceID)
			assert.Contains(t, models[0].SupportedMethods, string(schemas.ChatCompletionRequest))
		})
	}
}

func TestToBifrostListModelsResponseProviderNamedOrganization(t *testing.T) {
	t.Parallel()
	response := &HuggingFaceListModelsResponse{Models: []HuggingFaceModel{{
		ID: "model", ModelID: "nebius/model", PipelineTag: "conversational",
	}}}
	result := response.ToBifrostListModelsResponse(schemas.HuggingFace, featherlessAI, schemas.WhiteList{"*"}, nil, nil, false)
	require.Len(t, result.Data, 1)
	assert.Equal(t, "huggingface/featherless-ai/nebius/model", result.Data[0].ID, "a real Hub organization is not an alias provider segment")
}
