package handlers

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	governanceplugin "github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// mockModelsManager returns stable filtered and unfiltered model lists for handler tests.
type mockModelsManager struct {
	filtered    map[schemas.ModelProvider][]string
	unfiltered  map[schemas.ModelProvider][]string
	reloadCalls []schemas.ModelProvider
	reloadErr   error
}

func (m *mockModelsManager) ReloadProvider(_ context.Context, provider schemas.ModelProvider) (*configstoreTables.TableProvider, error) {
	m.reloadCalls = append(m.reloadCalls, provider)
	if m.reloadErr != nil {
		return nil, m.reloadErr
	}
	return nil, nil
}

func (m *mockModelsManager) RemoveProvider(_ context.Context, _ schemas.ModelProvider) error {
	return nil
}

func (m *mockModelsManager) GetModelsForProvider(provider schemas.ModelProvider) []string {
	models := m.filtered[provider]
	result := make([]string, len(models))
	copy(result, models)
	return result
}

func (m *mockModelsManager) GetUnfilteredModelsForProvider(provider schemas.ModelProvider) []string {
	models := m.unfiltered[provider]
	result := make([]string, len(models))
	copy(result, models)
	return result
}

// providerHandlerForTest builds a handler with fixed provider config and model sets.
func providerHandlerForTest(provider schemas.ModelProvider, keys []schemas.Key, filtered, unfiltered []string) *ProviderHandler {
	return &ProviderHandler{
		inMemoryStore: &lib.Config{
			Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
				provider: {
					Keys: keys,
				},
			},
		},
		modelsManager: &mockModelsManager{
			filtered: map[schemas.ModelProvider][]string{
				provider: filtered,
			},
			unfiltered: map[schemas.ModelProvider][]string{
				provider: unfiltered,
			},
		},
	}
}

func TestAddProvider_ReloadsRuntimeEvenWhenModelDiscoveryIsSkipped(t *testing.T) {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})

	modelsManager := &mockModelsManager{}
	h := &ProviderHandler{
		inMemoryStore: &lib.Config{Providers: map[schemas.ModelProvider]configstore.ProviderConfig{}},
		modelsManager: modelsManager,
	}

	body, err := sonic.Marshal(providerCreatePayload{
		Provider: "mock-openai",
		CustomProviderConfig: &schemas.CustomProviderConfig{
			BaseProviderType: schemas.OpenAI,
			IsKeyLess:        true,
		},
	})
	if err != nil {
		t.Fatalf("failed to marshal request body: %v", err)
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod(fasthttp.MethodPost)
	ctx.Request.SetRequestURI("/api/providers")
	ctx.Request.SetBody(body)

	h.addProvider(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	if len(modelsManager.reloadCalls) != 1 || modelsManager.reloadCalls[0] != "mock-openai" {
		t.Fatalf("expected provider reload for mock-openai, got %#v", modelsManager.reloadCalls)
	}
	if _, exists := h.inMemoryStore.Providers["mock-openai"]; !exists {
		t.Fatalf("expected provider to be added to in-memory store")
	}
}

func TestAddProvider_ReturnsErrorWhenRuntimeReloadFails(t *testing.T) {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})

	modelsManager := &mockModelsManager{reloadErr: context.DeadlineExceeded}
	h := &ProviderHandler{
		inMemoryStore: &lib.Config{Providers: map[schemas.ModelProvider]configstore.ProviderConfig{}},
		modelsManager: modelsManager,
	}

	body, err := sonic.Marshal(providerCreatePayload{
		Provider: "mock-openai",
		CustomProviderConfig: &schemas.CustomProviderConfig{
			BaseProviderType: schemas.OpenAI,
			IsKeyLess:        true,
		},
	})
	if err != nil {
		t.Fatalf("failed to marshal request body: %v", err)
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod(fasthttp.MethodPost)
	ctx.Request.SetRequestURI("/api/providers")
	ctx.Request.SetBody(body)

	h.addProvider(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	if len(modelsManager.reloadCalls) != 1 || modelsManager.reloadCalls[0] != "mock-openai" {
		t.Fatalf("expected single provider reload for mock-openai, got %#v", modelsManager.reloadCalls)
	}
	var bifrostErr schemas.BifrostError
	if err := json.Unmarshal(ctx.Response.Body(), &bifrostErr); err != nil {
		t.Fatalf("failed to unmarshal error response: %v", err)
	}
	if bifrostErr.Error == nil || bifrostErr.Error.Message == "" {
		t.Fatalf("expected error message in response, got %#v", bifrostErr)
	}
	if bifrostErr.Error.Message != "Failed to initialize provider after add: context deadline exceeded" {
		t.Fatalf("unexpected error message: %q", bifrostErr.Error.Message)
	}
	if _, exists := h.inMemoryStore.Providers["mock-openai"]; exists {
		t.Fatalf("expected provider rollback after reload failure")
	}
}

// boolPtr keeps pointer-valued key fixtures inline without pulling in pointer helpers.
func boolPtr(v bool) *bool {
	return &v
}

func TestListModels_UnknownKeysDoNotFilter(t *testing.T) {
	SetLogger(&mockLogger{})

	h := providerHandlerForTest(
		schemas.OpenAI,
		[]schemas.Key{{ID: "key-a"}},
		[]string{"gpt-4o", "gpt-4o-mini"},
		[]string{"gpt-4o", "gpt-4o-mini"},
	)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/models?provider=openai&keys=missing")

	h.listModels(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}

	var resp ListModelsResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Total != 2 {
		t.Fatalf("expected total=2, got %d", resp.Total)
	}
	if len(resp.Models) != 2 {
		t.Fatalf("expected all models to be returned, got %#v", resp.Models)
	}
	for _, model := range resp.Models {
		if len(model.AccessibleByKeys) != 0 {
			t.Fatalf("expected no accessible_by_keys annotations, got %#v", resp.Models)
		}
	}
}

func TestListModels_ReturnsExactAccessibleByKeysAndSkipsDisabledKeys(t *testing.T) {
	SetLogger(&mockLogger{})

	h := providerHandlerForTest(
		schemas.OpenAI,
		[]schemas.Key{
			{ID: "key-a", Models: []string{"gpt-4o"}},
			{ID: "key-b", Models: []string{"gpt-4o", "gpt-4o-mini"}},
			{ID: "key-disabled", Enabled: boolPtr(false)},
		},
		[]string{"gpt-4o", "gpt-4o-mini"},
		[]string{"gpt-4o", "gpt-4o-mini"},
	)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/models?provider=openai&keys=key-a,key-b,key-disabled")

	h.listModels(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}

	var resp ListModelsResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Total != 2 {
		t.Fatalf("expected total=2, got %d", resp.Total)
	}

	got := map[string][]string{}
	for _, model := range resp.Models {
		got[model.Name] = model.AccessibleByKeys
	}

	if len(got["gpt-4o"]) != 2 || got["gpt-4o"][0] != "key-a" || got["gpt-4o"][1] != "key-b" {
		t.Fatalf("expected gpt-4o to be accessible by [key-a key-b], got %#v", got["gpt-4o"])
	}
	if len(got["gpt-4o-mini"]) != 1 || got["gpt-4o-mini"][0] != "key-b" {
		t.Fatalf("expected gpt-4o-mini to be accessible by [key-b], got %#v", got["gpt-4o-mini"])
	}
}

func TestListModels_AppliesQueryAndLimitAfterFiltering(t *testing.T) {
	SetLogger(&mockLogger{})

	h := providerHandlerForTest(
		schemas.OpenAI,
		[]schemas.Key{{ID: "key-a"}},
		[]string{"gpt-4o", "gpt-4o-mini", "claude-3-5-sonnet"},
		[]string{"gpt-4o", "gpt-4o-mini", "claude-3-5-sonnet"},
	)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/models?provider=openai&query=gpt&limit=1")

	h.listModels(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}

	var resp ListModelsResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Total != 2 {
		t.Fatalf("expected total=2 after query filtering, got %d", resp.Total)
	}
	if len(resp.Models) != 1 {
		t.Fatalf("expected limit to truncate response to 1 model, got %#v", resp.Models)
	}
	if resp.Models[0].Name != "gpt-4o" {
		t.Fatalf("expected first filtered model to be gpt-4o, got %#v", resp.Models[0])
	}
}

func TestListModels_UnfilteredIgnoresKeys(t *testing.T) {
	SetLogger(&mockLogger{})

	h := providerHandlerForTest(
		schemas.OpenAI,
		[]schemas.Key{
			{ID: "key-b", Models: []string{"gpt-4o-mini"}},
		},
		[]string{"gpt-4o"},
		[]string{"gpt-4o", "gpt-4o-mini"},
	)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/models?provider=openai&keys=key-b&unfiltered=true")

	h.listModels(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}

	var resp ListModelsResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Total != 2 || len(resp.Models) != 2 {
		t.Fatalf("expected both unfiltered models, got %#v", resp.Models)
	}

	for _, model := range resp.Models {
		if len(model.AccessibleByKeys) != 0 {
			t.Fatalf("expected no accessible_by_keys when unfiltered bypasses key filtering, got %#v", resp.Models)
		}
	}
}

func TestListModels_UnfilteredWithoutKeysReturnsAllUnfilteredModels(t *testing.T) {
	SetLogger(&mockLogger{})

	h := providerHandlerForTest(
		schemas.OpenAI,
		[]schemas.Key{
			{ID: "key-b", Models: []string{"gpt-4o-mini"}},
		},
		[]string{"gpt-4o"},
		[]string{"gpt-4o", "gpt-4o-mini"},
	)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/models?provider=openai&unfiltered=true")

	h.listModels(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}

	var resp ListModelsResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Total != 2 || len(resp.Models) != 2 {
		t.Fatalf("expected both unfiltered models, got %#v", resp.Models)
	}

	for _, model := range resp.Models {
		if len(model.AccessibleByKeys) != 0 {
			t.Fatalf("expected no accessible_by_keys when no key filter is requested, got %#v", resp.Models)
		}
	}
}

func TestListModelDetails_ErrorsWhenModelCatalogUnavailable(t *testing.T) {
	SetLogger(&mockLogger{})

	h := providerHandlerForTest(
		schemas.OpenAI,
		[]schemas.Key{{ID: "key-a"}},
		[]string{"gpt-4o"},
		[]string{"gpt-4o"},
	)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/models/details?provider=openai")

	h.listModelDetails(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
}

func TestListModelDetails_UnknownKeysDoNotFilter(t *testing.T) {
	SetLogger(&mockLogger{})

	h := providerHandlerForTest(
		schemas.OpenAI,
		[]schemas.Key{{ID: "key-a"}},
		[]string{"gpt-4o", "gpt-4o-mini"},
		[]string{"gpt-4o", "gpt-4o-mini"},
	)
	h.inMemoryStore.ModelCatalog = &modelcatalog.ModelCatalog{}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/models/details?provider=openai&keys=missing")

	h.listModelDetails(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}

	var resp ListModelDetailsResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Total != 2 || len(resp.Models) != 2 {
		t.Fatalf("expected all models when keys are unknown, got %#v", resp.Models)
	}
}

func TestListModelDetails_SkipsUnknownKeysAndFiltersWithValid(t *testing.T) {
	SetLogger(&mockLogger{})

	h := providerHandlerForTest(
		schemas.OpenAI,
		[]schemas.Key{{ID: "key-a", Models: []string{"gpt-4o"}}},
		[]string{"gpt-4o", "gpt-4o-mini"},
		[]string{"gpt-4o", "gpt-4o-mini"},
	)
	h.inMemoryStore.ModelCatalog = &modelcatalog.ModelCatalog{}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/models/details?provider=openai&keys=key-a,missing")

	h.listModelDetails(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}

	var resp ListModelDetailsResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Total != 1 || len(resp.Models) != 1 {
		t.Fatalf("expected 1 model filtered by valid key, got %#v", resp.Models)
	}
	if resp.Models[0].Name != "gpt-4o" {
		t.Fatalf("expected gpt-4o, got %s", resp.Models[0].Name)
	}
}

func TestListModelDetails_SkipsDisabledKeysAndFiltersWithValid(t *testing.T) {
	SetLogger(&mockLogger{})

	h := providerHandlerForTest(
		schemas.OpenAI,
		[]schemas.Key{
			{ID: "key-a", Models: []string{"gpt-4o"}},
			{ID: "key-disabled", Enabled: boolPtr(false)},
		},
		[]string{"gpt-4o", "gpt-4o-mini"},
		[]string{"gpt-4o", "gpt-4o-mini"},
	)
	h.inMemoryStore.ModelCatalog = &modelcatalog.ModelCatalog{}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/models/details?provider=openai&keys=key-a,key-disabled")

	h.listModelDetails(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}

	var resp ListModelDetailsResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Total != 1 || len(resp.Models) != 1 {
		t.Fatalf("expected 1 model filtered by valid key, got %#v", resp.Models)
	}
	if resp.Models[0].Name != "gpt-4o" {
		t.Fatalf("expected gpt-4o, got %s", resp.Models[0].Name)
	}
}

func TestListModelDetails_UnfilteredIgnoresKeys(t *testing.T) {
	SetLogger(&mockLogger{})

	h := providerHandlerForTest(
		schemas.OpenAI,
		[]schemas.Key{
			{ID: "key-b", Models: []string{"gpt-4o-mini"}},
		},
		[]string{"gpt-4o"},
		[]string{"gpt-4o", "gpt-4o-mini"},
	)
	h.inMemoryStore.ModelCatalog = &modelcatalog.ModelCatalog{}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/models/details?provider=openai&keys=key-b&unfiltered=true")

	h.listModelDetails(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}

	var resp ListModelDetailsResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Total != 2 || len(resp.Models) != 2 {
		t.Fatalf("expected all unfiltered models when unfiltered=true, got %#v", resp.Models)
	}
}

// --- VK-based filtering tests ---

// TestParseVKValueFromRequest verifies that the VK value is extracted from each
// supported header, in priority order, and that non-VK values are ignored.
func TestParseVKValueFromRequest(t *testing.T) {
	const vk = "sk-bf-test-virtual-key"

	cases := []struct {
		name   string
		setup  func(*fasthttp.RequestCtx)
		wantVK string
	}{
		{
			name: "x-bf-vk header",
			setup: func(ctx *fasthttp.RequestCtx) {
				ctx.Request.Header.Set("x-bf-vk", vk)
			},
			wantVK: vk,
		},
		{
			name: "Authorization Bearer header",
			setup: func(ctx *fasthttp.RequestCtx) {
				ctx.Request.Header.Set("Authorization", "Bearer "+vk)
			},
			wantVK: vk,
		},
		{
			name: "x-api-key header",
			setup: func(ctx *fasthttp.RequestCtx) {
				ctx.Request.Header.Set("x-api-key", vk)
			},
			wantVK: vk,
		},
		{
			name: "x-goog-api-key header",
			setup: func(ctx *fasthttp.RequestCtx) {
				ctx.Request.Header.Set("x-goog-api-key", vk)
			},
			wantVK: vk,
		},
		{
			name:   "no header returns empty string",
			setup:  func(*fasthttp.RequestCtx) {},
			wantVK: "",
		},
		{
			name: "non-VK Bearer token returns empty string",
			setup: func(ctx *fasthttp.RequestCtx) {
				ctx.Request.Header.Set("Authorization", "Bearer regular-api-key-123")
			},
			wantVK: "",
		},
		{
			name: "x-bf-vk takes priority over Authorization",
			setup: func(ctx *fasthttp.RequestCtx) {
				ctx.Request.Header.Set("x-bf-vk", vk)
				ctx.Request.Header.Set("Authorization", "Bearer sk-bf-other")
			},
			wantVK: vk,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			tc.setup(ctx)
			got := governanceplugin.ParseVirtualKeyFromFastHTTPRequest(ctx)
			gotValue := ""
			if got != nil {
				gotValue = *got
			}
			if gotValue != tc.wantVK {
				t.Fatalf("expected %q, got %q", tc.wantVK, gotValue)
			}
		})
	}
}

// TestListModels_VKFilterRestrictsToAllowedProviderAndModels verifies that when a
// VK filter is active, only providers listed in VKProviderConfigs are returned and
// only models passing AllowedModels are included.
func TestListModels_VKFilterRestrictsToAllowedProviderAndModels(t *testing.T) {
	SetLogger(&mockLogger{})

	// Two providers configured; VK only allows openai with specific models.
	h := &ProviderHandler{
		inMemoryStore: &lib.Config{
			Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
				schemas.OpenAI:    {Keys: []schemas.Key{{ID: "key-a"}}},
				schemas.Anthropic: {Keys: []schemas.Key{{ID: "key-b"}}},
			},
		},
		modelsManager: &mockModelsManager{
			filtered: map[schemas.ModelProvider][]string{
				schemas.OpenAI:    {"gpt-4o", "gpt-4o-mini", "gpt-3.5-turbo"},
				schemas.Anthropic: {"claude-3-5-sonnet", "claude-3-haiku"},
			},
		},
	}

	query := modelListQuery{
		Limit:       100,
		HasVKFilter: true,
		VKProviderConfigs: []configstoreTables.TableVirtualKeyProviderConfig{
			{
				Provider:      "openai",
				AllowedModels: schemas.WhiteList{"gpt-4o", "gpt-4o-mini"},
			},
		},
	}

	models, total, err := h.listManagementModels(query)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 2 {
		t.Fatalf("expected total=2, got %d", total)
	}
	for _, m := range models {
		if m.Provider != schemas.OpenAI {
			t.Fatalf("expected only openai models, got provider %s", m.Provider)
		}
	}
	names := map[string]bool{}
	for _, m := range models {
		names[m.Name] = true
	}
	if !names["gpt-4o"] || !names["gpt-4o-mini"] {
		t.Fatalf("expected gpt-4o and gpt-4o-mini, got %v", models)
	}
	if names["gpt-3.5-turbo"] {
		t.Fatalf("gpt-3.5-turbo should be denied by AllowedModels")
	}
	if names["claude-3-5-sonnet"] || names["claude-3-haiku"] {
		t.Fatalf("anthropic models should be excluded by VK provider filter")
	}
}

// TestListModels_VKFilterAllowsAllModelsWithWildcard verifies that AllowedModels=["*"]
// passes all provider models through.
func TestListModels_VKFilterAllowsAllModelsWithWildcard(t *testing.T) {
	SetLogger(&mockLogger{})

	h := &ProviderHandler{
		inMemoryStore: &lib.Config{
			Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
				schemas.OpenAI: {Keys: []schemas.Key{{ID: "key-a"}}},
			},
		},
		modelsManager: &mockModelsManager{
			filtered: map[schemas.ModelProvider][]string{
				schemas.OpenAI: {"gpt-4o", "gpt-4o-mini", "gpt-3.5-turbo"},
			},
		},
	}

	query := modelListQuery{
		Limit:       100,
		HasVKFilter: true,
		VKProviderConfigs: []configstoreTables.TableVirtualKeyProviderConfig{
			{Provider: "openai", AllowedModels: schemas.WhiteList{"*"}},
		},
	}

	models, total, err := h.listManagementModels(query)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 3 {
		t.Fatalf("expected all 3 models with wildcard, got total=%d", total)
	}
	_ = models
}

// TestListModels_VKFilterDeniesAllModelsWhenAllowedModelsEmpty verifies deny-by-default:
// a VK that lists a provider but with an empty AllowedModels returns 0 models.
func TestListModels_VKFilterDeniesAllModelsWhenAllowedModelsEmpty(t *testing.T) {
	SetLogger(&mockLogger{})

	h := &ProviderHandler{
		inMemoryStore: &lib.Config{
			Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
				schemas.OpenAI: {Keys: []schemas.Key{{ID: "key-a"}}},
			},
		},
		modelsManager: &mockModelsManager{
			filtered: map[schemas.ModelProvider][]string{
				schemas.OpenAI: {"gpt-4o", "gpt-4o-mini"},
			},
		},
	}

	query := modelListQuery{
		Limit:       100,
		HasVKFilter: true,
		VKProviderConfigs: []configstoreTables.TableVirtualKeyProviderConfig{
			{Provider: "openai", AllowedModels: schemas.WhiteList{}},
		},
	}

	models, total, err := h.listManagementModels(query)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 0 || len(models) != 0 {
		t.Fatalf("expected 0 models with empty AllowedModels (deny-by-default), got total=%d %v", total, models)
	}
}

// TestListModels_VKFilterNoProviderConfigsDeniesAll verifies that a VK with no
// ProviderConfigs returns 0 models (deny-by-default at provider level).
func TestListModels_VKFilterNoProviderConfigsDeniesAll(t *testing.T) {
	SetLogger(&mockLogger{})

	h := &ProviderHandler{
		inMemoryStore: &lib.Config{
			Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
				schemas.OpenAI:    {},
				schemas.Anthropic: {},
			},
		},
		modelsManager: &mockModelsManager{
			filtered: map[schemas.ModelProvider][]string{
				schemas.OpenAI:    {"gpt-4o"},
				schemas.Anthropic: {"claude-3-5-sonnet"},
			},
		},
	}

	query := modelListQuery{
		Limit:             100,
		HasVKFilter:       true,
		VKProviderConfigs: []configstoreTables.TableVirtualKeyProviderConfig{}, // empty
	}

	models, total, err := h.listManagementModels(query)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 0 || len(models) != 0 {
		t.Fatalf("expected 0 models when VK has no provider configs, got total=%d", total)
	}
}

func TestListModels_VKFilterBlockedExplicitProviderReturnsEmptyResult(t *testing.T) {
	SetLogger(&mockLogger{})

	h := &ProviderHandler{
		inMemoryStore: &lib.Config{
			Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
				schemas.OpenAI:    {},
				schemas.Anthropic: {},
			},
		},
		modelsManager: &mockModelsManager{
			filtered: map[schemas.ModelProvider][]string{
				schemas.OpenAI:    {"gpt-4o"},
				schemas.Anthropic: {"claude-3-5-sonnet"},
			},
		},
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/models?provider=anthropic")
	query, ok := h.parseModelListQuery(ctx, 5)
	if !ok {
		t.Fatalf("expected parseModelListQuery to succeed")
	}
	query.HasVKFilter = true
	query.VKProviderConfigs = []configstoreTables.TableVirtualKeyProviderConfig{
		{Provider: "openai", AllowedModels: schemas.WhiteList{"*"}},
	}

	models, total, err := h.listManagementModels(query)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 0 || len(models) != 0 {
		t.Fatalf("expected blocked explicit provider to return no models, got total=%d models=%#v", total, models)
	}
}

func TestParseModelListQuery_VKWithoutDBStoreReturnsServiceUnavailable(t *testing.T) {
	SetLogger(&mockLogger{})

	h := &ProviderHandler{
		inMemoryStore: &lib.Config{
			Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
				schemas.OpenAI: {},
			},
		},
		modelsManager: &mockModelsManager{
			filtered: map[schemas.ModelProvider][]string{
				schemas.OpenAI: {"gpt-4o", "gpt-4o-mini"},
			},
		},
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/models")
	ctx.Request.Header.Set("x-bf-vk", "sk-bf-test-virtual-key")

	query, ok := h.parseModelListQuery(ctx, 5)
	if ok {
		t.Fatalf("expected parseModelListQuery to fail without dbStore, got query=%#v", query)
	}
	if ctx.Response.StatusCode() != fasthttp.StatusServiceUnavailable {
		t.Fatalf("expected 503 when dbStore is unavailable, got %d", ctx.Response.StatusCode())
	}
}

// TestListModels_NoVKFilterReturnsAll verifies that without a VK filter the endpoint
// returns all providers and models as normal.
func TestListModels_NoVKFilterReturnsAll(t *testing.T) {
	SetLogger(&mockLogger{})

	h := &ProviderHandler{
		inMemoryStore: &lib.Config{
			Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
				schemas.OpenAI:    {},
				schemas.Anthropic: {},
			},
		},
		modelsManager: &mockModelsManager{
			filtered: map[schemas.ModelProvider][]string{
				schemas.OpenAI:    {"gpt-4o"},
				schemas.Anthropic: {"claude-3-5-sonnet"},
			},
		},
	}

	query := modelListQuery{
		Limit:       100,
		HasVKFilter: false, // no filter
	}

	models, total, err := h.listManagementModels(query)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 2 || len(models) != 2 {
		t.Fatalf("expected 2 models (one per provider), got total=%d", total)
	}
}

func TestListModels_UsesCatalogAwareAliasMatchingForKeyAllowlist(t *testing.T) {
	SetLogger(&mockLogger{})

	h := providerHandlerForTest(
		schemas.OpenAI,
		[]schemas.Key{
			{ID: "key-a", Models: []string{"gpt-4o-2024-08-06"}},
		},
		[]string{"gpt-4o"},
		[]string{"gpt-4o"},
	)
	h.inMemoryStore.ModelCatalog = modelcatalog.NewTestCatalog(map[string]string{
		"gpt-4o-2024-08-06": "gpt-4o",
	})

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/models?provider=openai&keys=key-a")

	h.listModels(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}

	var resp ListModelsResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Total != 1 || len(resp.Models) != 1 || resp.Models[0].Name != "gpt-4o" {
		t.Fatalf("expected gpt-4o to be matched through alias allowlist, got %#v", resp.Models)
	}
}

// TestListModels_KeyModelAllowlistIsCaseInsensitive verifies that key.Models matching
// uses case-insensitive comparison so "GPT-4O" in the allowlist matches "gpt-4o" in the pool.
func TestListModels_KeyModelAllowlistIsCaseInsensitive(t *testing.T) {
	SetLogger(&mockLogger{})

	h := providerHandlerForTest(
		schemas.OpenAI,
		[]schemas.Key{
			{ID: "key-a", Models: []string{"GPT-4O", "GPT-4O-MINI"}},
		},
		[]string{"gpt-4o", "gpt-4o-mini", "gpt-3.5-turbo"},
		[]string{"gpt-4o", "gpt-4o-mini", "gpt-3.5-turbo"},
	)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/models?provider=openai&keys=key-a&limit=10")

	h.listModels(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}

	var resp ListModelsResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Total != 2 {
		t.Fatalf("expected total=2 (gpt-4o and gpt-4o-mini matched case-insensitively), got total=%d %v", resp.Total, resp.Models)
	}
	names := map[string]bool{}
	for _, m := range resp.Models {
		names[m.Name] = true
	}
	if !names["gpt-4o"] || !names["gpt-4o-mini"] {
		t.Fatalf("expected gpt-4o and gpt-4o-mini, got %v", resp.Models)
	}
	if names["gpt-3.5-turbo"] {
		t.Fatalf("gpt-3.5-turbo should not be returned (not in key allowlist)")
	}
}

// TestListModels_KeyBlacklistIsCaseInsensitive verifies that key.BlacklistedModels uses
// case-insensitive matching so "GPT-3.5-TURBO" blocks "gpt-3.5-turbo" in the pool.
func TestListModels_KeyBlacklistIsCaseInsensitive(t *testing.T) {
	SetLogger(&mockLogger{})

	h := providerHandlerForTest(
		schemas.OpenAI,
		[]schemas.Key{
			{ID: "key-a", BlacklistedModels: []string{"GPT-3.5-TURBO"}},
		},
		[]string{"gpt-4o", "gpt-4o-mini", "gpt-3.5-turbo"},
		[]string{"gpt-4o", "gpt-4o-mini", "gpt-3.5-turbo"},
	)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/models?provider=openai&keys=key-a&limit=10")

	h.listModels(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}

	var resp ListModelsResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Total != 2 {
		t.Fatalf("expected total=2 (gpt-3.5-turbo blocked case-insensitively), got total=%d %v", resp.Total, resp.Models)
	}
	for _, m := range resp.Models {
		if strings.EqualFold(m.Name, "gpt-3.5-turbo") {
			t.Fatalf("gpt-3.5-turbo should be blocked by blacklist, got %v", resp.Models)
		}
	}
}
