package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	governanceplugin "github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib/libtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// mockModelsManager returns stable filtered and unfiltered model lists for handler tests.
// providerKeyRef records a (provider, keyID) pair a refresh was requested for.
type providerKeyRef struct {
	provider schemas.ModelProvider
	keyID    string
}

type mockModelsManager struct {
	filtered             map[schemas.ModelProvider][]string
	unfiltered           map[schemas.ModelProvider][]string
	reloadCalls          []schemas.ModelProvider
	reloadErr            error
	refreshKeyCalls      []providerKeyRef
	refreshProviderCalls []schemas.ModelProvider
	refreshErr           error
	// access is what the request may reach; nil stands for a request with nothing resolved: no
	// key presented, or a deployment without governance.
	access       schemas.Access
	resolveCalls int
	narrowCalls  int
	// reloadNew is, for each reload, whether it was told the provider was just added.
	reloadNew []bool
	remove    func(context.Context, schemas.ModelProvider) error // stands in for the server's config removal
}

func (m *mockModelsManager) ResolveAccess(_ *schemas.BifrostContext) (schemas.Access, error) {
	m.resolveCalls++
	return m.access, nil
}

// NarrowListModelsProviders stands in for the server, which is what resolves access and narrows
// the fan-out in production. Kept observable so a caller can assert it was asked; the rule itself
// is the server's, and is exercised there and against a live deployment.
func (m *mockModelsManager) NarrowListModelsProviders(bifrostCtx *schemas.BifrostContext) {
	m.narrowCalls++
	access, err := m.ResolveAccess(bifrostCtx)
	if err != nil || access == nil {
		return
	}
	granted := access.GrantedProvidersForModel("")
	providers := make([]schemas.ModelProvider, 0, len(granted))
	for _, provider := range granted {
		providers = append(providers, schemas.ModelProvider(provider))
	}
	bifrostCtx.SetValue(schemas.BifrostContextKeyAvailableProviders, providers)
}

func (m *mockModelsManager) ReloadProvider(_ context.Context, provider schemas.ModelProvider, isNew bool) (*configstoreTables.TableProvider, error) {
	m.reloadNew = append(m.reloadNew, isNew)
	m.reloadCalls = append(m.reloadCalls, provider)
	if m.reloadErr != nil {
		return nil, m.reloadErr
	}
	return nil, nil
}

func (m *mockModelsManager) RemoveProvider(ctx context.Context, provider schemas.ModelProvider) error {
	if m.remove != nil {
		return m.remove(ctx, provider)
	}
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

func (m *mockModelsManager) UpsertModelPricingAttributes(_ context.Context, _ []ModelPricingAttributesEntry) error {
	return nil
}

func (m *mockModelsManager) OnKeyAdded(_ context.Context, _ schemas.ModelProvider, _ schemas.Key) error {
	return nil
}

func (m *mockModelsManager) OnKeyUpdated(_ context.Context, _ schemas.ModelProvider, _ schemas.Key) error {
	return nil
}

func (m *mockModelsManager) OnKeyDeleted(_ context.Context, _ schemas.ModelProvider, _ string) error {
	return nil
}

func (m *mockModelsManager) RefreshLiveModelsForKey(_ context.Context, provider schemas.ModelProvider, keyID string) error {
	m.refreshKeyCalls = append(m.refreshKeyCalls, providerKeyRef{provider: provider, keyID: keyID})
	return m.refreshErr
}

func (m *mockModelsManager) RefreshLiveModelsForAllKeys(_ context.Context, provider schemas.ModelProvider) error {
	m.refreshProviderCalls = append(m.refreshProviderCalls, provider)
	return m.refreshErr
}

// newBareRequestCtx returns an empty request context, as &fasthttp.RequestCtx{} is, but with
// fasthttp's stand-in server behind it, so a handler can hand it to the database as a
// context.Context: a bare one panics when the driver checks Done.
func newBareRequestCtx() *fasthttp.RequestCtx {
	ctx := &fasthttp.RequestCtx{}
	ctx.Init(&fasthttp.Request{}, nil, nil)
	return ctx
}

// providerHandlerForTest builds a handler with fixed provider config and model sets.
func providerHandlerForTest(t testing.TB, provider schemas.ModelProvider, keys []schemas.Key, filtered, unfiltered []string) *ProviderHandler {
	return &ProviderHandler{
		config: libtest.WithProviders(t, &lib.Config{
			ClientConfig: &configstore.ClientConfig{},
		}, map[schemas.ModelProvider]configstore.ProviderConfig{
			provider: {},
		}, map[schemas.ModelProvider][]schemas.Key{
			provider: keys,
		}),
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
		config:        libtest.Config(t, map[schemas.ModelProvider]configstore.ProviderConfig{}, nil),
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

	ctx := newBareRequestCtx()
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
	if _, exists := libtest.Providers(h.config)["mock-openai"]; !exists {
		t.Fatalf("expected provider to be added to in-memory store")
	}
}

// A custom provider created with no keys (direct-key callers bring their own) must resolve
// by its "name/model" prefix from creation, and stop resolving once deleted.
func TestAddProvider_ZeroKeyCustomProviderResolvesByPrefix(t *testing.T) {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})

	const name = schemas.ModelProvider("harness-zero-key-direct")
	t.Cleanup(func() { schemas.UnregisterKnownProvider(name) })
	cfg := libtest.Config(t, map[schemas.ModelProvider]configstore.ProviderConfig{}, nil)
	h := &ProviderHandler{
		config: cfg,
		// The server removes the configuration, which is what unregisters the prefix.
		modelsManager: &mockModelsManager{remove: cfg.RemoveProvider},
	}
	body, err := sonic.Marshal(providerCreatePayload{
		Provider:             name,
		CustomProviderConfig: &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI},
	})
	if err != nil {
		t.Fatalf("failed to marshal request body: %v", err)
	}
	// The create reaches the config store, which needs an initialized request context.
	ctx := newBareRequestCtx()
	ctx.Request.Header.SetMethod(fasthttp.MethodPost)
	ctx.Request.SetRequestURI("/api/providers")
	ctx.Request.SetBody(body)

	h.addProvider(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	if provider, model := schemas.ParseModelString(string(name)+"/gpt-4o-mini", ""); provider != name || model != "gpt-4o-mini" {
		t.Fatalf("after create: ParseModelString = (%q, %q), want (%q, %q)", provider, model, name, "gpt-4o-mini")
	}

	// The delete reaches the config store too.
	delCtx := newBareRequestCtx()
	delCtx.Request.Header.SetMethod(fasthttp.MethodDelete)
	delCtx.SetUserValue("provider", string(name))
	h.deleteProvider(delCtx)

	if provider, _ := schemas.ParseModelString(string(name)+"/gpt-4o-mini", ""); provider != "" {
		t.Fatalf("after delete: prefix still resolves to %q", provider)
	}
}

// TestDeleteProvider_AnswersServerErrorWhenTheDeleteFails pins that a provider delete that fails,
// in the database or in memory, answers 500 instead of reporting the provider deleted.
func TestDeleteProvider_AnswersServerErrorWhenTheDeleteFails(t *testing.T) {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})

	h := &ProviderHandler{
		config: libtest.Config(t, map[schemas.ModelProvider]configstore.ProviderConfig{schemas.OpenAI: {}}, nil),
		modelsManager: &mockModelsManager{remove: func(context.Context, schemas.ModelProvider) error {
			return lib.ErrNoConfigStore
		}},
	}
	ctx := newBareRequestCtx()
	ctx.Request.Header.SetMethod(fasthttp.MethodDelete)
	ctx.SetUserValue("provider", string(schemas.OpenAI))
	h.deleteProvider(ctx)

	require.Equal(t, fasthttp.StatusInternalServerError, ctx.Response.StatusCode(), string(ctx.Response.Body()))
}

// TestUpdateProvider_MergesOntoTheStoredProvider pins that a provider update fills the settings its
// body leaves out from the provider as stored, not from the live record: a setting written
// elsewhere that this node has not re-read yet survives the update.
func TestUpdateProvider_MergesOntoTheStoredProvider(t *testing.T) {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})
	ctx := context.Background()

	cfg := libtest.Config(t, map[schemas.ModelProvider]configstore.ProviderConfig{
		schemas.OpenAI: {},
	}, map[schemas.ModelProvider][]schemas.Key{
		schemas.OpenAI: {{ID: "k1", Name: "first", Value: *schemas.NewSecretVar("sk-1"), Models: schemas.WhiteList{"*"}, Weight: 1}},
	})
	// Written elsewhere: this node's live record does not have it.
	require.NoError(t, cfg.ConfigStore.UpdateProvider(ctx, schemas.OpenAI, configstore.ProviderConfig{OpenAIConfig: &schemas.OpenAIConfig{DisableStore: true}}))
	h := &ProviderHandler{config: cfg, modelsManager: &mockModelsManager{}}
	attachBifrostClient(t, cfg)

	reqCtx := newBareRequestCtx()
	reqCtx.Request.Header.SetMethod(fasthttp.MethodPut)
	reqCtx.Request.SetBody([]byte(`{"concurrency_and_buffer_size": {"concurrency": 2, "buffer_size": 4}}`))
	reqCtx.SetUserValue("provider", string(schemas.OpenAI))
	h.updateProvider(reqCtx)
	require.Equal(t, fasthttp.StatusOK, reqCtx.Response.StatusCode(), string(reqCtx.Response.Body()))

	stored, err := cfg.ConfigStore.GetProviderConfig(ctx, schemas.OpenAI)
	require.NoError(t, err)
	require.NotNil(t, stored.OpenAIConfig, "the update kept the stored setting it was not given")
	require.True(t, stored.OpenAIConfig.DisableStore)
	require.Equal(t, 2, stored.ConcurrencyAndBufferSize.Concurrency)
	storedKeys, err := cfg.ConfigStore.GetProviderKeys(ctx, schemas.OpenAI)
	require.NoError(t, err)
	require.Len(t, storedKeys, 1, "a settings update leaves the keys alone")
}

// TestAddProvider_CustomProviderBaseTypes pins which base_provider_type values
// the provider API accepts: typesafe is a supported base, vertex is not.
func TestAddProvider_CustomProviderBaseTypes(t *testing.T) {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})

	cases := []struct {
		name       string
		provider   schemas.ModelProvider
		base       schemas.ModelProvider
		wantStatus int
	}{
		{name: "typesafe base accepted", provider: "my-typesafe", base: schemas.Typesafe, wantStatus: fasthttp.StatusOK},
		{name: "vertex base rejected", provider: "my-vertex", base: schemas.Vertex, wantStatus: fasthttp.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &ProviderHandler{
				config:        libtest.Config(t, map[schemas.ModelProvider]configstore.ProviderConfig{}, nil),
				modelsManager: &mockModelsManager{},
			}
			body, err := sonic.Marshal(providerCreatePayload{
				Provider:             tc.provider,
				CustomProviderConfig: &schemas.CustomProviderConfig{BaseProviderType: tc.base, IsKeyLess: true},
			})
			if err != nil {
				t.Fatalf("failed to marshal request body: %v", err)
			}
			ctx := newBareRequestCtx()
			ctx.Request.Header.SetMethod(fasthttp.MethodPost)
			ctx.Request.SetRequestURI("/api/providers")
			ctx.Request.SetBody(body)

			h.addProvider(ctx)

			if ctx.Response.StatusCode() != tc.wantStatus {
				t.Fatalf("status got %d, want %d; body=%s", ctx.Response.StatusCode(), tc.wantStatus, ctx.Response.Body())
			}
			_, exists := libtest.Providers(h.config)[tc.provider]
			if exists != (tc.wantStatus == fasthttp.StatusOK) {
				t.Fatalf("provider persisted=%v, want %v", exists, tc.wantStatus == fasthttp.StatusOK)
			}
		})
	}
}

func TestAddProvider_RejectsBaseURLComponents(t *testing.T) {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})
	for _, suffix := range []string{"?x=", "?", "#ignored", "#"} {
		t.Run(suffix, func(t *testing.T) {
			h := &ProviderHandler{
				config:        libtest.Config(t, map[schemas.ModelProvider]configstore.ProviderConfig{}, nil),
				modelsManager: &mockModelsManager{},
			}
			body, err := schemas.MarshalSorted(providerCreatePayload{
				Provider:             "mock-openai",
				CustomProviderConfig: &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI, IsKeyLess: true},
				NetworkConfig:        &schemas.NetworkConfig{BaseURL: "http://127.0.0.1:1/base" + suffix},
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx := newBareRequestCtx()
			ctx.Request.SetBody(body)
			h.addProvider(ctx)
			if ctx.Response.StatusCode() != fasthttp.StatusBadRequest || !strings.Contains(string(ctx.Response.Body()), "base URL must not contain") {
				t.Fatalf("expected base URL rejection, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
			}
			if len(libtest.Providers(h.config)) != 0 {
				t.Fatal("invalid provider was persisted")
			}
		})
	}
}

// The update path must reject the same base URL components as creation, before
// saving or reloading the provider.
func TestUpdateProvider_RejectsBaseURLComponents(t *testing.T) {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})
	const storedURL = "https://1.1.1.1/v1"
	for _, suffix := range []string{"?x=", "?", "#ignored", "#"} {
		t.Run(suffix, func(t *testing.T) {
			customConfig := &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI, IsKeyLess: true}
			modelsManager := &mockModelsManager{}
			h := &ProviderHandler{
				config: libtest.WithProviders(t, &lib.Config{
					ClientConfig: &configstore.ClientConfig{},
				}, map[schemas.ModelProvider]configstore.ProviderConfig{
					"mock-openai": {
						NetworkConfig:            &schemas.NetworkConfig{BaseURL: storedURL},
						ConcurrencyAndBufferSize: &schemas.ConcurrencyAndBufferSize{Concurrency: 1, BufferSize: 4},
						CustomProviderConfig:     customConfig,
					},
				}, nil),
				modelsManager: modelsManager,
			}
			attachBifrostClient(t, h.config)

			body, err := schemas.MarshalSorted(providerUpdatePayload{
				NetworkConfig:            schemas.NetworkConfig{BaseURL: storedURL + suffix},
				ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{Concurrency: 2, BufferSize: 4},
				CustomProviderConfig:     customConfig,
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx := newBareRequestCtx()
			ctx.Request.Header.SetMethod(fasthttp.MethodPut)
			ctx.Request.SetRequestURI("/api/providers/mock-openai")
			ctx.Request.SetBody(body)
			ctx.SetUserValue("provider", "mock-openai")

			h.updateProvider(ctx)

			if ctx.Response.StatusCode() != fasthttp.StatusBadRequest || !strings.Contains(string(ctx.Response.Body()), "base URL must not contain") {
				t.Fatalf("expected base URL rejection, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
			}
			stored := libtest.Providers(h.config)["mock-openai"]
			if stored.NetworkConfig.BaseURL != storedURL || stored.ConcurrencyAndBufferSize.Concurrency != 1 {
				t.Fatalf("rejected update changed the stored provider: base_url=%q concurrency=%d", stored.NetworkConfig.BaseURL, stored.ConcurrencyAndBufferSize.Concurrency)
			}
			if len(modelsManager.reloadCalls) != 0 {
				t.Fatalf("rejected update reloaded the provider: %v", modelsManager.reloadCalls)
			}
		})
	}
}

// An injected_tools list the typed payload cannot hold must be rejected, not decoded
// into an empty or zeroed config: the old object shape, a null entry or a typo'd field
// would otherwise return 200 with no tool configured. Repeated names are rejected too.
// Both write paths are covered.
func TestProviderWrites_RejectMalformedInjectedTools(t *testing.T) {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})
	blocks := map[string]struct{ body, reason string }{
		"old object shape":          {`{"web_search": {"mcp_client_name": "tavily", "tool_name": "search"}}`, "injected_tools must be an array"},
		"null entry":                {`[null]`, "injected_tools[0] must be an object"},
		"unknown field":             {`[{"name": "web_search", "mcp_client_name": "tavily", "tool_name": "search", "max_results": 5}]`, `unknown field \"max_results\"`},
		"duplicate list, last null": {`[{"name": "web_search", "mcp_client_name": "tavily", "tool_name": "search"}], "injected_tools": [null]`, "injected_tools[0] must be an object"},
		"duplicate names":           {`[{"name": "web_search", "mcp_client_name": "tavily", "tool_name": "search"}, {"name": "web_search", "mcp_client_name": "tavily", "tool_name": "crawl"}]`, "used more than once"},
		"invalid name":              {`[{"name": "web search", "mcp_client_name": "tavily", "tool_name": "search"}]`, "must be 1-64"},
	}
	for name, tc := range blocks {
		t.Run("add/"+name, func(t *testing.T) {
			h := &ProviderHandler{
				config:        libtest.Config(t, map[schemas.ModelProvider]configstore.ProviderConfig{}, nil),
				modelsManager: &mockModelsManager{},
			}
			ctx := newBareRequestCtx()
			ctx.Request.SetBody([]byte(`{"provider": "mock-openai", "custom_provider_config": {"base_provider_type": "openai", "is_key_less": true}, "injected_tools": ` + tc.body + `}`))
			h.addProvider(ctx)
			if ctx.Response.StatusCode() != fasthttp.StatusBadRequest || !strings.Contains(string(ctx.Response.Body()), tc.reason) {
				t.Fatalf("expected injected_tools rejection, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
			}
			if len(libtest.Providers(h.config)) != 0 {
				t.Fatal("invalid provider was persisted")
			}
		})
		t.Run("update/"+name, func(t *testing.T) {
			customConfig := &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI, IsKeyLess: true}
			modelsManager := &mockModelsManager{}
			h := &ProviderHandler{
				config: libtest.WithProviders(t, &lib.Config{
					ClientConfig: &configstore.ClientConfig{},
				}, map[schemas.ModelProvider]configstore.ProviderConfig{
					"mock-openai": {
						ConcurrencyAndBufferSize: &schemas.ConcurrencyAndBufferSize{Concurrency: 1, BufferSize: 4},
						CustomProviderConfig:     customConfig,
					},
				}, nil),
				modelsManager: modelsManager,
			}
			attachBifrostClient(t, h.config)
			ctx := newBareRequestCtx()
			ctx.Request.Header.SetMethod(fasthttp.MethodPut)
			ctx.Request.SetRequestURI("/api/providers/mock-openai")
			ctx.Request.SetBody([]byte(`{"concurrency_and_buffer_size": {"concurrency": 2, "buffer_size": 4}, "custom_provider_config": {"base_provider_type": "openai", "is_key_less": true}, "injected_tools": ` + tc.body + `}`))
			ctx.SetUserValue("provider", "mock-openai")
			h.updateProvider(ctx)
			if ctx.Response.StatusCode() != fasthttp.StatusBadRequest || !strings.Contains(string(ctx.Response.Body()), tc.reason) {
				t.Fatalf("expected injected_tools rejection, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
			}
			if got := libtest.Providers(h.config)["mock-openai"].ConcurrencyAndBufferSize.Concurrency; got != 1 {
				t.Fatalf("rejected update changed the stored provider: concurrency=%d", got)
			}
			if len(modelsManager.reloadCalls) != 0 {
				t.Fatalf("rejected update reloaded the provider: %v", modelsManager.reloadCalls)
			}
		})
	}
}

// A passthrough block the typed payload would silently reshape must be rejected: a
// misspelled or missing allowed_routes decodes to an empty list, which forwards no routes
// at all, and a rule the matcher cannot honour would block routes without saying why.
// Both write paths are covered.
func TestProviderWrites_RejectMalformedPassthrough(t *testing.T) {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})
	blocks := map[string]struct{ body, reason string }{
		"not an object":          {`[]`, "passthrough must be an object"},
		"missing allowed_routes": {`{}`, "allowed_routes is required"},
		"misspelled field":       {`{"allowed_route": []}`, `unknown field \"allowed_route\"`},
		"null rule":              {`{"allowed_routes": [null]}`, "allowed_routes[0] must be an object"},
		"unknown rule field":     {`{"allowed_routes": [{"method": "GET", "path": "/v1/files"}]}`, `unknown field \"method\"`},
		"rule without a path":    {`{"allowed_routes": [{"methods": ["GET"]}]}`, "path must start with /"},
		"inner double star":      {`{"allowed_routes": [{"path": "/v1/**/content"}]}`, "only allowed as the last"},
		"unsupported method":     {`{"allowed_routes": [{"methods": ["TRACE"], "path": "/v1/files"}]}`, "unsupported method"},
	}
	for name, tc := range blocks {
		t.Run("add/"+name, func(t *testing.T) {
			h := &ProviderHandler{
				config:        libtest.Config(t, map[schemas.ModelProvider]configstore.ProviderConfig{}, nil),
				modelsManager: &mockModelsManager{},
			}
			ctx := newBareRequestCtx()
			ctx.Request.SetBody([]byte(`{"provider": "mock-openai", "custom_provider_config": {"base_provider_type": "openai", "is_key_less": true}, "passthrough": ` + tc.body + `}`))
			h.addProvider(ctx)
			if ctx.Response.StatusCode() != fasthttp.StatusBadRequest || !strings.Contains(string(ctx.Response.Body()), tc.reason) {
				t.Fatalf("expected passthrough rejection, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
			}
			if len(libtest.Providers(h.config)) != 0 {
				t.Fatal("invalid provider was persisted")
			}
		})
		t.Run("update/"+name, func(t *testing.T) {
			modelsManager := &mockModelsManager{}
			h := &ProviderHandler{
				config: libtest.WithProviders(t, &lib.Config{
					ClientConfig: &configstore.ClientConfig{},
				}, map[schemas.ModelProvider]configstore.ProviderConfig{
					"mock-openai": {
						ConcurrencyAndBufferSize: &schemas.ConcurrencyAndBufferSize{Concurrency: 1, BufferSize: 4},
						CustomProviderConfig:     &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI, IsKeyLess: true},
					},
				}, nil),
				modelsManager: modelsManager,
			}
			attachBifrostClient(t, h.config)
			ctx := newBareRequestCtx()
			ctx.Request.Header.SetMethod(fasthttp.MethodPut)
			ctx.Request.SetRequestURI("/api/providers/mock-openai")
			ctx.Request.SetBody([]byte(`{"concurrency_and_buffer_size": {"concurrency": 2, "buffer_size": 4}, "custom_provider_config": {"base_provider_type": "openai", "is_key_less": true}, "passthrough": ` + tc.body + `}`))
			ctx.SetUserValue("provider", "mock-openai")
			h.updateProvider(ctx)
			if ctx.Response.StatusCode() != fasthttp.StatusBadRequest || !strings.Contains(string(ctx.Response.Body()), tc.reason) {
				t.Fatalf("expected passthrough rejection, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
			}
			if got := libtest.Providers(h.config)["mock-openai"].ConcurrencyAndBufferSize.Concurrency; got != 1 {
				t.Fatalf("rejected update changed the stored provider: concurrency=%d", got)
			}
			if len(modelsManager.reloadCalls) != 0 {
				t.Fatalf("rejected update reloaded the provider: %v", modelsManager.reloadCalls)
			}
		})
	}
}

// A valid passthrough policy on create is stored, returned, and an update that does not
// mention it leaves it in place.
func TestProviderWrites_StorePassthrough(t *testing.T) {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})
	policy := &schemas.PassthroughPolicy{AllowedRoutes: []schemas.PassthroughRouteRule{
		{Methods: []string{"POST"}, Path: "/v1/chat/completions"},
		{Methods: []string{"POST"}, Path: "/v1/models/*/predict"},
	}}
	h := &ProviderHandler{
		config:        libtest.WithProviders(t, &lib.Config{ClientConfig: &configstore.ClientConfig{}}, map[schemas.ModelProvider]configstore.ProviderConfig{}, nil),
		modelsManager: &mockModelsManager{},
	}
	body, err := sonic.Marshal(providerCreatePayload{
		Provider:             "mock-openai",
		CustomProviderConfig: &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI, IsKeyLess: true},
		Passthrough:          policy,
	})
	if err != nil {
		t.Fatalf("failed to marshal request body: %v", err)
	}
	ctx := newBareRequestCtx()
	ctx.Request.Header.SetMethod(fasthttp.MethodPost)
	ctx.Request.SetRequestURI("/api/providers")
	ctx.Request.SetBody(body)
	h.addProvider(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("add status %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	var created ProviderResponse
	if err := sonic.Unmarshal(ctx.Response.Body(), &created); err != nil {
		t.Fatalf("decode add response: %v", err)
	}
	if !reflect.DeepEqual(created.Passthrough, policy) {
		t.Fatalf("add response passthrough %+v, want %+v", created.Passthrough, policy)
	}
	if got := libtest.Providers(h.config)["mock-openai"].Passthrough; !reflect.DeepEqual(got, policy) {
		t.Fatalf("stored passthrough %+v, want %+v", got, policy)
	}

	attachBifrostClient(t, h.config)
	ctx = newBareRequestCtx()
	ctx.Request.Header.SetMethod(fasthttp.MethodPut)
	ctx.Request.SetRequestURI("/api/providers/mock-openai")
	ctx.Request.SetBody([]byte(`{"concurrency_and_buffer_size": {"concurrency": 2, "buffer_size": 4}, "custom_provider_config": {"base_provider_type": "openai", "is_key_less": true}}`))
	ctx.SetUserValue("provider", "mock-openai")
	h.updateProvider(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("update status %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	if got := libtest.Providers(h.config)["mock-openai"].Passthrough; !reflect.DeepEqual(got, policy) {
		t.Fatalf("an update without passthrough changed it to %+v", got)
	}
}

// TestAddProvider_RejectsBaseURLWhenAuthBypassed covers the second route to the same
// outcome as the Ollama key case: a custom provider's network_config.base_url + explicit
// allow_private_network:true, which an unauthenticated caller could set together to
// self-authorize its own destination past ValidateExternalURL's private-IP check.
func TestAddProvider_RejectsBaseURLWhenAuthBypassed(t *testing.T) {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})

	h := &ProviderHandler{
		config:        libtest.Config(t, map[schemas.ModelProvider]configstore.ProviderConfig{}, nil),
		modelsManager: &mockModelsManager{},
	}

	body, err := sonic.Marshal(providerCreatePayload{
		Provider: "mock-openai",
		CustomProviderConfig: &schemas.CustomProviderConfig{
			BaseProviderType: schemas.OpenAI,
			IsKeyLess:        true,
		},
		NetworkConfig: &schemas.NetworkConfig{
			BaseURL:             "http://169.254.169.254/",
			AllowPrivateNetwork: true,
		},
	})
	if err != nil {
		t.Fatalf("failed to marshal request body: %v", err)
	}

	ctx := newBareRequestCtx()
	ctx.Request.Header.SetMethod(fasthttp.MethodPost)
	ctx.Request.SetRequestURI("/api/providers")
	ctx.Request.SetBody(body)
	ctx.SetUserValue(schemas.BifrostContextKeyAuthBypassed, true)

	h.addProvider(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusForbidden {
		t.Fatalf("status got %d, want 403; body=%s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	if _, exists := libtest.Providers(h.config)["mock-openai"]; exists {
		t.Fatalf("expected provider not to be persisted")
	}
}

// TestAddProvider_RejectsAllowPrivateNetworkWhenAuthBypassed pins that opting a new provider
// into private networks needs genuine auth even with no base URL: ConfigureDialer applies the
// flag to key-level URLs (Ollama/SGL/VLLM), so it widens what those keys can reach.
func TestAddProvider_RejectsAllowPrivateNetworkWhenAuthBypassed(t *testing.T) {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})

	h := &ProviderHandler{
		config:        libtest.Config(t, map[schemas.ModelProvider]configstore.ProviderConfig{}, nil),
		modelsManager: &mockModelsManager{},
	}

	body, err := sonic.Marshal(providerCreatePayload{
		Provider:      schemas.Ollama,
		NetworkConfig: &schemas.NetworkConfig{AllowPrivateNetwork: true},
	})
	if err != nil {
		t.Fatalf("failed to marshal request body: %v", err)
	}

	ctx := newBareRequestCtx()
	ctx.Request.Header.SetMethod(fasthttp.MethodPost)
	ctx.Request.SetRequestURI("/api/providers")
	ctx.Request.SetBody(body)
	ctx.SetUserValue(schemas.BifrostContextKeyAuthBypassed, true)

	h.addProvider(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusForbidden {
		t.Fatalf("status got %d, want 403; body=%s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	if _, exists := libtest.Providers(h.config)[schemas.Ollama]; exists {
		t.Fatalf("expected provider not to be persisted")
	}
}

// TestProviderInterceptionGuardWhenAuthBypassed pins that the fail-open bypass cannot put a
// third party between Bifrost and the provider without touching base_url: a caller-chosen
// proxy plus a caller-trusted CA (or skipped verification) reads every provider credential
// in flight. The UI echoes the stored proxy back redacted on every save, so that echo with an
// unrelated edit must still go through.
func TestProviderInterceptionGuardWhenAuthBypassed(t *testing.T) {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})

	const storedProxyURL = "http://10.0.0.5:3128"
	const fakePEM = "-----BEGIN CERTIFICATE-----\nMIIBexample\n-----END CERTIFICATE-----\n"
	cases := []struct {
		name      string
		create    bool
		mutate    func(nc *schemas.NetworkConfig, proxy *schemas.ProxyConfig)
		overrides map[schemas.RequestType]string
		want403   bool
	}{
		{name: "update echoing redacted proxy, concurrency edit", mutate: func(*schemas.NetworkConfig, *schemas.ProxyConfig) {}, want403: false},
		{name: "update proxy url", mutate: func(_ *schemas.NetworkConfig, p *schemas.ProxyConfig) {
			p.URL = schemas.NewSecretVar("http://evil.example.com:8080")
		}, want403: true},
		{name: "update adding proxy ca_cert_pem", mutate: func(_ *schemas.NetworkConfig, p *schemas.ProxyConfig) { p.CACertPEM = schemas.NewSecretVar(fakePEM) }, want403: true},
		{name: "update adding network ca_cert_pem", mutate: func(nc *schemas.NetworkConfig, _ *schemas.ProxyConfig) { nc.CACertPEM = schemas.NewSecretVar(fakePEM) }, want403: true},
		{name: "update turning on insecure_skip_verify", mutate: func(nc *schemas.NetworkConfig, _ *schemas.ProxyConfig) { nc.InsecureSkipVerify = true }, want403: true},
		{name: "create with proxy url", create: true, mutate: func(_ *schemas.NetworkConfig, p *schemas.ProxyConfig) {
			p.URL = schemas.NewSecretVar("http://evil.example.com:8080")
		}, want403: true},
		{name: "create with insecure_skip_verify", create: true, mutate: func(nc *schemas.NetworkConfig, _ *schemas.ProxyConfig) { nc.InsecureSkipVerify = true }, want403: true},
		// An absolute request_path_overrides value replaces the whole request URL and gets the
		// key as a bearer token; a path-only override still goes to the stored base URL.
		{name: "update adding absolute request path override", mutate: func(*schemas.NetworkConfig, *schemas.ProxyConfig) {},
			overrides: map[schemas.RequestType]string{schemas.ChatCompletionRequest: "https://evil.example.com/v1/chat/completions"}, want403: true},
		{name: "update adding path-only request path override", mutate: func(*schemas.NetworkConfig, *schemas.ProxyConfig) {},
			overrides: map[schemas.RequestType]string{schemas.ChatCompletionRequest: "/v2/chat/completions"}, want403: false},
		{name: "create with absolute request path override", create: true, mutate: func(*schemas.NetworkConfig, *schemas.ProxyConfig) {},
			overrides: map[schemas.RequestType]string{schemas.ChatCompletionRequest: "https://evil.example.com/v1/chat/completions"}, want403: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			customConfig := &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI, IsKeyLess: true}
			payloadCustomConfig := &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI, IsKeyLess: true, RequestPathOverrides: tc.overrides}
			providers := map[schemas.ModelProvider]configstore.ProviderConfig{}
			if !tc.create {
				providers["mock-openai"] = configstore.ProviderConfig{
					NetworkConfig:            &schemas.NetworkConfig{},
					ProxyConfig:              &schemas.ProxyConfig{Type: schemas.HTTPProxy, URL: schemas.NewSecretVar(storedProxyURL)},
					ConcurrencyAndBufferSize: &schemas.ConcurrencyAndBufferSize{Concurrency: 1, BufferSize: 4},
					CustomProviderConfig:     customConfig,
				}
			}
			h := &ProviderHandler{
				config:        libtest.WithProviders(t, &lib.Config{ClientConfig: &configstore.ClientConfig{}}, providers, nil),
				modelsManager: &mockModelsManager{},
			}
			attachBifrostClient(t, h.config)

			nc := schemas.NetworkConfig{}
			proxy := schemas.ProxyConfig{Type: schemas.HTTPProxy}
			if !tc.create {
				stored := libtest.Providers(h.config)["mock-openai"]
				proxy = *stored.Redacted().ProxyConfig
			}
			tc.mutate(&nc, &proxy)

			ctx := newBareRequestCtx()
			ctx.SetUserValue(schemas.BifrostContextKeyAuthBypassed, true)
			var body []byte
			var err error
			if tc.create {
				body, err = sonic.Marshal(providerCreatePayload{
					Provider:                 "mock-openai",
					NetworkConfig:            &nc,
					ProxyConfig:              &proxy,
					ConcurrencyAndBufferSize: &schemas.ConcurrencyAndBufferSize{Concurrency: 1, BufferSize: 4},
					CustomProviderConfig:     payloadCustomConfig,
				})
				ctx.Request.Header.SetMethod(fasthttp.MethodPost)
				ctx.Request.SetRequestURI("/api/providers")
			} else {
				body, err = sonic.Marshal(providerUpdatePayload{
					NetworkConfig:            nc,
					ProxyConfig:              &proxy,
					ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{Concurrency: 2, BufferSize: 4},
					CustomProviderConfig:     payloadCustomConfig,
				})
				ctx.Request.Header.SetMethod(fasthttp.MethodPut)
				ctx.Request.SetRequestURI("/api/providers/mock-openai")
				ctx.SetUserValue("provider", "mock-openai")
			}
			if err != nil {
				t.Fatalf("failed to marshal request body: %v", err)
			}
			ctx.Request.SetBody(body)

			if tc.create {
				h.addProvider(ctx)
			} else {
				h.updateProvider(ctx)
			}

			got403 := ctx.Response.StatusCode() == fasthttp.StatusForbidden
			if got403 != tc.want403 {
				t.Fatalf("got status %d, want403=%v; body=%s", ctx.Response.StatusCode(), tc.want403, ctx.Response.Body())
			}
			stored, exists := libtest.Providers(h.config)["mock-openai"]
			switch {
			case tc.want403 && tc.create:
				if exists {
					t.Fatalf("expected provider not to be persisted after 403")
				}
			case tc.want403:
				if got := stored.ProxyConfig.URL.GetValue(); got != storedProxyURL {
					t.Fatalf("stored proxy url got %q, want %q", got, storedProxyURL)
				}
				if stored.ProxyConfig.CACertPEM.IsSet() || stored.NetworkConfig.CACertPEM.IsSet() || stored.NetworkConfig.InsecureSkipVerify {
					t.Fatalf("expected TLS trust settings not to be persisted after 403")
				}
				if len(stored.CustomProviderConfig.RequestPathOverrides) != 0 {
					t.Fatalf("expected request path overrides not to be persisted after 403, got %v", stored.CustomProviderConfig.RequestPathOverrides)
				}
			default:
				if ctx.Response.StatusCode() != fasthttp.StatusOK {
					t.Fatalf("got status %d, want 200; body=%s", ctx.Response.StatusCode(), ctx.Response.Body())
				}
				if got := stored.ProxyConfig.URL.GetValue(); got != storedProxyURL {
					t.Fatalf("echoed redacted proxy url was not restored: got %q", got)
				}
				if got := stored.ConcurrencyAndBufferSize.Concurrency; got != 2 {
					t.Fatalf("stored concurrency got %d, want 2", got)
				}
			}
		})
	}
}

// TestUpdateProvider_RejectsBaseURLWhenAuthBypassed is the PUT-endpoint sibling of
// TestAddProvider_RejectsBaseURLWhenAuthBypassed.
func TestUpdateProvider_RejectsBaseURLWhenAuthBypassed(t *testing.T) {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})

	h := &ProviderHandler{
		config: libtest.Config(t, map[schemas.ModelProvider]configstore.ProviderConfig{
			"mock-openai": {
				CustomProviderConfig: &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI, IsKeyLess: true},
			},
		}, nil),
		modelsManager: &mockModelsManager{},
	}

	body, err := sonic.Marshal(struct {
		Keys []schemas.Key `json:"keys"`
		providerUpdatePayload
	}{
		providerUpdatePayload: providerUpdatePayload{
			NetworkConfig: schemas.NetworkConfig{
				BaseURL:             "http://169.254.169.254/",
				AllowPrivateNetwork: true,
			},
			ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{Concurrency: 1, BufferSize: 1},
			CustomProviderConfig:     &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI, IsKeyLess: true},
		},
	})
	if err != nil {
		t.Fatalf("failed to marshal request body: %v", err)
	}

	ctx := newBareRequestCtx()
	ctx.Request.Header.SetMethod(fasthttp.MethodPut)
	ctx.Request.SetRequestURI("/api/providers/mock-openai")
	ctx.Request.SetBody(body)
	ctx.SetUserValue("provider", "mock-openai")
	ctx.SetUserValue(schemas.BifrostContextKeyAuthBypassed, true)

	h.updateProvider(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusForbidden {
		t.Fatalf("status got %d, want 403; body=%s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	if got := libtest.Providers(h.config)["mock-openai"].NetworkConfig; got != nil && got.BaseURL == "http://169.254.169.254/" {
		t.Fatalf("expected base URL not to be persisted")
	}
}

// attachBifrostClient gives store a live Bifrost client backed by its own providers, so
// handler tests can run past the provider reload that follows a successful save.
func attachBifrostClient(t *testing.T, store *lib.Config) {
	t.Helper()
	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account: lib.NewBaseAccount(store),
		Logger:  bifrost.NewDefaultLogger(schemas.LogLevelError),
	})
	if err != nil {
		t.Fatalf("failed to init bifrost client: %v", err)
	}
	t.Cleanup(client.Shutdown)
	store.SetBifrostClient(client)
}

// TestUpdateProvider_BaseURLGuardComparesStoredConfigWhenAuthBypassed pins that the bypass
// guard only fires when the dial target widens. The UI echoes the stored base URL on every
// save, so an unchanged base URL with an unrelated edit (concurrency) must go through.
// Turning allow_private_network on must not, with or without a base URL: ConfigureDialer
// enforces that flag at connect time, both for the base URL (where DNS can move it to a
// private IP) and for key-level URLs (Ollama/SGL/VLLM) when no base URL is set.
func TestUpdateProvider_BaseURLGuardComparesStoredConfigWhenAuthBypassed(t *testing.T) {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})

	// An IP literal keeps ValidateExternalURL off DNS.
	const storedURL = "https://1.1.1.1/v1"
	cases := []struct {
		name                string
		storedBaseURL       string
		storedAllowPrivate  bool
		baseURL             string
		allowPrivateNetwork bool
		authenticated       bool // a real credential, not the fail-open bypass
		wantStatus          int
		wantConcurrency     int
	}{
		{name: "unchanged base url, concurrency edit", storedBaseURL: storedURL, baseURL: storedURL, allowPrivateNetwork: false, wantStatus: fasthttp.StatusOK, wantConcurrency: 2},
		{name: "unchanged base url, allow_private_network turned on", storedBaseURL: storedURL, baseURL: storedURL, allowPrivateNetwork: true, wantStatus: fasthttp.StatusForbidden, wantConcurrency: 1},
		{name: "no base url, concurrency edit", storedBaseURL: "", baseURL: "", allowPrivateNetwork: false, wantStatus: fasthttp.StatusOK, wantConcurrency: 2},
		{name: "no base url, allow_private_network turned on", storedBaseURL: "", baseURL: "", allowPrivateNetwork: true, wantStatus: fasthttp.StatusForbidden, wantConcurrency: 1},
		// Narrowing the dial target needs no authenticated request.
		{name: "stored base url cleared", storedBaseURL: storedURL, baseURL: "", allowPrivateNetwork: false, wantStatus: fasthttp.StatusOK, wantConcurrency: 2},
		{name: "stored allow_private_network turned off", storedBaseURL: storedURL, storedAllowPrivate: true, baseURL: storedURL, allowPrivateNetwork: false, wantStatus: fasthttp.StatusOK, wantConcurrency: 2},
		// Widening is refused only for the bypass; any authenticated caller may widen.
		{name: "authenticated base url change", storedBaseURL: storedURL, baseURL: "https://8.8.8.8/v1", authenticated: true, wantStatus: fasthttp.StatusOK, wantConcurrency: 2},
		{name: "authenticated allow_private_network turned on", storedBaseURL: storedURL, baseURL: storedURL, allowPrivateNetwork: true, authenticated: true, wantStatus: fasthttp.StatusOK, wantConcurrency: 2},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			customConfig := &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI, IsKeyLess: true}
			h := &ProviderHandler{
				config: libtest.WithProviders(t, &lib.Config{
					ClientConfig: &configstore.ClientConfig{},
				}, map[schemas.ModelProvider]configstore.ProviderConfig{
					"mock-openai": {
						NetworkConfig:            &schemas.NetworkConfig{BaseURL: tc.storedBaseURL, AllowPrivateNetwork: tc.storedAllowPrivate},
						ConcurrencyAndBufferSize: &schemas.ConcurrencyAndBufferSize{Concurrency: 1, BufferSize: 4},
						CustomProviderConfig:     customConfig,
					},
				}, nil),
				modelsManager: &mockModelsManager{},
			}
			attachBifrostClient(t, h.config)

			body, err := sonic.Marshal(providerUpdatePayload{
				NetworkConfig:            schemas.NetworkConfig{BaseURL: tc.baseURL, AllowPrivateNetwork: tc.allowPrivateNetwork},
				ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{Concurrency: 2, BufferSize: 4},
				CustomProviderConfig:     customConfig,
			})
			if err != nil {
				t.Fatalf("failed to marshal request body: %v", err)
			}

			ctx := newBareRequestCtx()
			ctx.Request.Header.SetMethod(fasthttp.MethodPut)
			ctx.Request.SetRequestURI("/api/providers/mock-openai")
			ctx.Request.SetBody(body)
			ctx.SetUserValue("provider", "mock-openai")
			if !tc.authenticated {
				ctx.SetUserValue(schemas.BifrostContextKeyAuthBypassed, true)
			}

			h.updateProvider(ctx)

			if ctx.Response.StatusCode() != tc.wantStatus {
				t.Fatalf("status got %d, want %d; body=%s", ctx.Response.StatusCode(), tc.wantStatus, ctx.Response.Body())
			}
			stored := libtest.Providers(h.config)["mock-openai"]
			if got := stored.ConcurrencyAndBufferSize.Concurrency; got != tc.wantConcurrency {
				t.Fatalf("stored concurrency got %d, want %d", got, tc.wantConcurrency)
			}
			if stored.NetworkConfig.AllowPrivateNetwork && tc.wantStatus == fasthttp.StatusForbidden {
				t.Fatalf("expected allow_private_network not to be persisted")
			}
			if tc.wantStatus == fasthttp.StatusOK && (stored.NetworkConfig.BaseURL != tc.baseURL || stored.NetworkConfig.AllowPrivateNetwork != tc.allowPrivateNetwork) {
				t.Fatalf("stored network config got base_url=%q allow_private_network=%v, want %q/%v", stored.NetworkConfig.BaseURL, stored.NetworkConfig.AllowPrivateNetwork, tc.baseURL, tc.allowPrivateNetwork)
			}
		})
	}
}

// TestProviderWrites_TellTheReloadWhetherTheyAdded pins that the reload after an update, keyless or
// not, is told the provider is not new, and the add endpoint's reload is told it is.
func TestProviderWrites_TellTheReloadWhetherTheyAdded(t *testing.T) {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})

	keyless := &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI, IsKeyLess: true}
	sizes := schemas.ConcurrencyAndBufferSize{Concurrency: 2, BufferSize: 4}
	cases := []struct {
		name     string
		provider schemas.ModelProvider
		body     providerUpdatePayload
		isNew    bool
	}{
		{name: "edit of a provider with keys", provider: schemas.OpenAI, body: providerUpdatePayload{ConcurrencyAndBufferSize: sizes}, isNew: false},
		{name: "edit of a keyless provider", provider: "mock-openai", body: providerUpdatePayload{ConcurrencyAndBufferSize: sizes, CustomProviderConfig: keyless}, isNew: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mgr := &mockModelsManager{}
			h := &ProviderHandler{
				config: libtest.WithProviders(t, &lib.Config{
					ClientConfig: &configstore.ClientConfig{},
				}, map[schemas.ModelProvider]configstore.ProviderConfig{
					schemas.OpenAI: {
						ConcurrencyAndBufferSize: &schemas.ConcurrencyAndBufferSize{Concurrency: 1, BufferSize: 4},
					},
					"mock-openai": {
						ConcurrencyAndBufferSize: &schemas.ConcurrencyAndBufferSize{Concurrency: 1, BufferSize: 4},
						CustomProviderConfig:     keyless,
					},
				}, map[schemas.ModelProvider][]schemas.Key{
					schemas.OpenAI: {{ID: "key-1", Name: "openai-key", Value: *schemas.NewSecretVar("sk-stored"), Weight: 1}},
				}),
				modelsManager: mgr,
			}
			attachBifrostClient(t, h.config)

			body, err := sonic.Marshal(tc.body)
			if err != nil {
				t.Fatalf("failed to marshal request body: %v", err)
			}
			ctx := newBareRequestCtx()
			ctx.Request.Header.SetMethod(fasthttp.MethodPut)
			ctx.Request.SetRequestURI("/api/providers/" + string(tc.provider))
			ctx.Request.SetBody(body)
			ctx.SetUserValue("provider", string(tc.provider))

			h.updateProvider(ctx)

			if ctx.Response.StatusCode() != fasthttp.StatusOK {
				t.Fatalf("status got %d, want 200; body=%s", ctx.Response.StatusCode(), ctx.Response.Body())
			}
			if len(mgr.reloadNew) != 1 || mgr.reloadNew[0] != tc.isNew {
				t.Fatalf("reloads told the provider is new: got %v, want [%v]", mgr.reloadNew, tc.isNew)
			}
		})
	}

	mgr := &mockModelsManager{}
	h := &ProviderHandler{config: libtest.Config(t, map[schemas.ModelProvider]configstore.ProviderConfig{}, nil), modelsManager: mgr}
	body, err := sonic.Marshal(providerCreatePayload{Provider: "mock-new", CustomProviderConfig: keyless})
	if err != nil {
		t.Fatalf("failed to marshal request body: %v", err)
	}
	ctx := newBareRequestCtx()
	ctx.Request.Header.SetMethod(fasthttp.MethodPost)
	ctx.Request.SetRequestURI("/api/providers")
	ctx.Request.SetBody(body)
	h.addProvider(ctx)
	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("add status got %d, want 200; body=%s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	if len(mgr.reloadNew) != 1 || !mgr.reloadNew[0] {
		t.Fatalf("an added provider's reload must be told it is new, got %v", mgr.reloadNew)
	}
}

func TestAddProvider_ReturnsErrorWhenRuntimeReloadFails(t *testing.T) {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})

	modelsManager := &mockModelsManager{reloadErr: context.DeadlineExceeded}
	h := &ProviderHandler{
		config:        libtest.Config(t, map[schemas.ModelProvider]configstore.ProviderConfig{}, nil),
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

	ctx := newBareRequestCtx()
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
	if _, exists := libtest.Providers(h.config)["mock-openai"]; exists {
		t.Fatalf("expected provider rollback after reload failure")
	}
	if schemas.IsKnownProvider("mock-openai") {
		t.Fatal("failed reload left a registered provider prefix")
	}
}

// TestUpdateProvider_RejectsKeysInBody guards against a silent-discard regression
// where `keys` is decoded into `payload.Keys` but never written to the persisted
// `ProviderConfig`. The endpoint manages provider-level config only; key edits
// must go through PUT /api/providers/{provider}/keys/{key_id}. Without this
// guard, callers (third-party API users, older dashboard bundles, integration
// tests) get HTTP 200 with their `blacklisted_models`/`weight`/etc. silently
// dropped — and the in-memory cache is rewritten with the stale `oldConfigRaw`
// keys, causing list/per-key endpoints to diverge from the DB.
func TestUpdateProvider_RejectsKeysInBody(t *testing.T) {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})

	existingKey := schemas.Key{
		ID:                "key-existing",
		Models:            []string{"*"},
		BlacklistedModels: []string{"gpt-3.5-turbo"},
		Weight:            0.8,
	}
	h := &ProviderHandler{
		config: libtest.WithProviders(t, &lib.Config{
			ClientConfig: &configstore.ClientConfig{},
		}, map[schemas.ModelProvider]configstore.ProviderConfig{
			schemas.OpenAI: {},
		}, map[schemas.ModelProvider][]schemas.Key{
			schemas.OpenAI: {existingKey},
		}),
		modelsManager: &mockModelsManager{},
	}

	body, err := sonic.Marshal(struct {
		Keys                     []schemas.Key                    `json:"keys"`
		NetworkConfig            schemas.NetworkConfig            `json:"network_config"`
		ConcurrencyAndBufferSize schemas.ConcurrencyAndBufferSize `json:"concurrency_and_buffer_size"`
	}{
		Keys: []schemas.Key{{
			ID:                "key-existing",
			Models:            []string{"*"},
			BlacklistedModels: []string{"gpt-4o", "o1-preview"},
			Weight:            0.42,
		}},
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{
			Concurrency: 1000,
			BufferSize:  5000,
		},
	})
	if err != nil {
		t.Fatalf("failed to marshal request body: %v", err)
	}

	ctx := newBareRequestCtx()
	ctx.Request.Header.SetMethod(fasthttp.MethodPut)
	ctx.Request.SetRequestURI("/api/providers/openai")
	ctx.Request.SetBody(body)
	ctx.SetUserValue("provider", string(schemas.OpenAI))

	h.updateProvider(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}

	var bifrostErr schemas.BifrostError
	if err := json.Unmarshal(ctx.Response.Body(), &bifrostErr); err != nil {
		t.Fatalf("failed to unmarshal error response: %v", err)
	}
	if bifrostErr.Error == nil || bifrostErr.Error.Message == "" {
		t.Fatalf("expected error message in response, got %#v", bifrostErr)
	}
	if !strings.Contains(bifrostErr.Error.Message, "/keys") {
		t.Fatalf("expected error message to mention the /keys endpoint, got %q", bifrostErr.Error.Message)
	}

	// In-memory cache must NOT have been mutated by the rejected request.
	_, ok := libtest.Providers(h.config)[schemas.OpenAI]
	stored := h.config.GetProviderKeys(schemas.OpenAI)
	if !ok || len(stored) != 1 {
		t.Fatalf("expected provider to retain its single existing key, got %#v", stored)
	}
	if stored[0].Weight != 0.8 || len(stored[0].BlacklistedModels) != 1 || stored[0].BlacklistedModels[0] != "gpt-3.5-turbo" {
		t.Fatalf("expected key to be untouched (weight=0.8, blacklisted=[gpt-3.5-turbo]); got weight=%v blacklisted=%v",
			stored[0].Weight, stored[0].BlacklistedModels)
	}
}

// TestUpdateProvider_PassesThroughForEmptyOrAbsentKeys locks in the explicit
// promise that the keys-guard only rejects NON-empty `keys` arrays. A future
// refactor that accidentally tightens the guard to `payload.Keys != nil` (or
// silently strips the field with `json:",omitempty"`) would silently break
// provider-level config saves that legitimately include an empty/null `keys`
// field, so we assert the guard does NOT fire for those cases.
//
// We can't easily run the handler all the way through to a 200 here because
// `inMemoryStore.UpdateProviderConfig` requires a real *bifrost.Bifrost client
// that's out of scope for a unit test. Instead, we deliberately send
// `concurrency: 0` so the handler short-circuits with a deterministic 400
// from the concurrency validator that lives AFTER the keys-guard. The
// invariant under test is: the error we get is the concurrency error, not the
// keys-not-accepted error.
func TestUpdateProvider_PassesThroughForEmptyOrAbsentKeys(t *testing.T) {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})

	cases := []struct {
		name string
		body string
	}{
		{
			name: "keys field omitted entirely",
			body: `{
				"network_config": {},
				"concurrency_and_buffer_size": {"concurrency": 0, "buffer_size": 0}
			}`,
		},
		{
			name: "keys explicitly null",
			body: `{
				"keys": null,
				"network_config": {},
				"concurrency_and_buffer_size": {"concurrency": 0, "buffer_size": 0}
			}`,
		},
		{
			name: "keys explicitly empty array",
			body: `{
				"keys": [],
				"network_config": {},
				"concurrency_and_buffer_size": {"concurrency": 0, "buffer_size": 0}
			}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &ProviderHandler{
				config: libtest.WithProviders(t, &lib.Config{
					ClientConfig: &configstore.ClientConfig{},
				}, map[schemas.ModelProvider]configstore.ProviderConfig{
					schemas.OpenAI: {},
				}, map[schemas.ModelProvider][]schemas.Key{
					schemas.OpenAI: {{ID: "key-existing"}},
				}),
				modelsManager: &mockModelsManager{},
			}

			ctx := newBareRequestCtx()
			ctx.Request.Header.SetMethod(fasthttp.MethodPut)
			ctx.Request.SetRequestURI("/api/providers/openai")
			ctx.Request.SetBody([]byte(tc.body))
			ctx.SetUserValue("provider", string(schemas.OpenAI))

			h.updateProvider(ctx)

			if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
				t.Fatalf("expected 400 (from concurrency validator, NOT keys-guard), got %d: %s",
					ctx.Response.StatusCode(), string(ctx.Response.Body()))
			}

			var bifrostErr schemas.BifrostError
			if err := json.Unmarshal(ctx.Response.Body(), &bifrostErr); err != nil {
				t.Fatalf("failed to unmarshal error response: %v", err)
			}
			if bifrostErr.Error == nil {
				t.Fatalf("expected error in response, got %#v", bifrostErr)
			}
			if strings.Contains(bifrostErr.Error.Message, "keys are not accepted on this endpoint") {
				t.Fatalf("keys-guard should NOT fire for empty/absent keys, got: %s", bifrostErr.Error.Message)
			}
			if !strings.Contains(bifrostErr.Error.Message, "Concurrency") {
				t.Fatalf("expected concurrency error (proves we passed the keys-guard), got: %s", bifrostErr.Error.Message)
			}
		})
	}
}

// TestProviderHandler_RejectsUnrestorableMaskedHeaders checks that update and add return 400
// when an extra header carries a masked value that no stored header of the same name can
// restore, instead of saving the mask as the header value.
func TestProviderHandler_RejectsUnrestorableMaskedHeaders(t *testing.T) {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})

	newHandler := func() *ProviderHandler {
		return &ProviderHandler{
			config: libtest.WithProviders(t, &lib.Config{
				ClientConfig: &configstore.ClientConfig{},
			}, map[schemas.ModelProvider]configstore.ProviderConfig{
				schemas.OpenAI: {
					NetworkConfig: &schemas.NetworkConfig{ExtraHeaders: map[string]schemas.SecretVar{
						"Authorization": *schemas.NewSecretVar("Bearer real-token"),
					}},
				},
			}, map[schemas.ModelProvider][]schemas.Key{
				schemas.OpenAI: {{ID: "key-existing"}},
			}),
			modelsManager: &mockModelsManager{},
		}
	}
	assertMaskedHeaderError := func(t *testing.T, ctx *fasthttp.RequestCtx, header string) {
		t.Helper()
		if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
			t.Fatalf("expected 400, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
		}
		var bifrostErr schemas.BifrostError
		if err := json.Unmarshal(ctx.Response.Body(), &bifrostErr); err != nil || bifrostErr.Error == nil {
			t.Fatalf("unexpected error body %s: %v", ctx.Response.Body(), err)
		}
		if msg := bifrostErr.Error.Message; !strings.Contains(msg, "masked value") || !strings.Contains(msg, header) {
			t.Fatalf("expected a masked-value error naming %s, got %q", header, msg)
		}
	}

	t.Run("update with a case-only rename", func(t *testing.T) {
		h := newHandler()
		ctx := newBareRequestCtx()
		ctx.Request.Header.SetMethod(fasthttp.MethodPut)
		ctx.Request.SetRequestURI("/api/providers/openai")
		ctx.Request.SetBody([]byte(`{
			"network_config": {"extra_headers": {"authorization": "<REDACTED>"}},
			"concurrency_and_buffer_size": {"concurrency": 1, "buffer_size": 1}
		}`))
		ctx.SetUserValue("provider", string(schemas.OpenAI))

		h.updateProvider(ctx)

		assertMaskedHeaderError(t, ctx, "authorization")
		if stored := libtest.Providers(h.config)[schemas.OpenAI].NetworkConfig.ExtraHeaders["Authorization"]; stored.GetValue() != "Bearer real-token" {
			t.Fatalf("stored header changed to %q", stored.GetValue())
		}
	})

	t.Run("add with a masked value", func(t *testing.T) {
		h := newHandler()
		ctx := newBareRequestCtx()
		ctx.Request.Header.SetMethod(fasthttp.MethodPost)
		ctx.Request.SetRequestURI("/api/providers")
		ctx.Request.SetBody([]byte(`{
			"provider": "hyperpod",
			"custom_provider_config": {"base_provider_type": "openai"},
			"network_config": {"extra_headers": {"X-Client-Secret": "<REDACTED>"}},
			"concurrency_and_buffer_size": {"concurrency": 1, "buffer_size": 1}
		}`))

		h.addProvider(ctx)

		assertMaskedHeaderError(t, ctx, "X-Client-Secret")
	})
}

// TestProviderResponse_ExtraHeadersSecretVars checks that a provider response keeps
// extra_headers as the stored string map and adds extra_headers_secret_vars with the same
// redacted headers as SecretVar objects, leaking no literal or resolved value, and that the
// field is omitted when there are no headers.
func TestProviderResponse_ExtraHeadersSecretVars(t *testing.T) {
	t.Setenv("BF_TEST_RESPONSE_HDR", "resolved-env-secret")
	h := &ProviderHandler{}
	config := configstore.ProviderConfig{NetworkConfig: &schemas.NetworkConfig{
		BaseURL: "https://upstream.example",
		ExtraHeaders: map[string]schemas.SecretVar{
			"X-Plain": *schemas.NewSecretVar("literal-secret"),
			"X-Env":   *schemas.NewSecretVar("env.BF_TEST_RESPONSE_HDR"),
		},
	}}
	body, err := sonic.ConfigStd.Marshal(h.getProviderResponseFromConfig(schemas.OpenAI, *config.Redacted(), ProviderStatusActive))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out struct {
		NetworkConfig struct {
			BaseURL                string                       `json:"base_url"`
			ExtraHeaders           map[string]string            `json:"extra_headers"`
			ExtraHeadersSecretVars map[string]map[string]string `json:"extra_headers_secret_vars"`
		} `json:"network_config"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	nc := out.NetworkConfig
	if nc.BaseURL != "https://upstream.example" {
		t.Errorf("base_url lost: %q", nc.BaseURL)
	}
	if nc.ExtraHeaders["X-Plain"] != "<REDACTED>" || nc.ExtraHeaders["X-Env"] != "env.BF_TEST_RESPONSE_HDR" {
		t.Errorf("extra_headers string map changed: %v", nc.ExtraHeaders)
	}
	if got := nc.ExtraHeadersSecretVars["X-Plain"]; got["value"] != "<REDACTED>" || got["type"] != string(schemas.SecretTypePlainText) {
		t.Errorf("X-Plain secret var: %v", got)
	}
	if got := nc.ExtraHeadersSecretVars["X-Env"]; got["ref"] != "env.BF_TEST_RESPONSE_HDR" || got["type"] != string(schemas.SecretTypeEnv) {
		t.Errorf("X-Env secret var: %v", got)
	}
	for _, leaked := range []string{"literal-secret", "resolved-env-secret"} {
		if strings.Contains(string(body), leaked) {
			t.Errorf("response leaks %q: %s", leaked, body)
		}
	}

	empty, err := sonic.ConfigStd.Marshal(h.getProviderResponseFromConfig(schemas.OpenAI, configstore.ProviderConfig{}, ProviderStatusActive))
	if err != nil {
		t.Fatalf("marshal empty: %v", err)
	}
	if strings.Contains(string(empty), "extra_headers_secret_vars") {
		t.Errorf("field should be omitted without headers: %s", empty)
	}
}

func modelCatalogForPricingJSON(t *testing.T, pricingJSON []byte) *modelcatalog.ModelCatalog {
	t.Helper()
	pricingPath := filepath.Join(t.TempDir(), "pricing.json")
	if err := os.WriteFile(pricingPath, pricingJSON, 0o600); err != nil {
		t.Fatalf("write pricing testdata: %v", err)
	}
	ds := datasheetForTest(t, nil, nil, datasheet.Config{URL: "file://" + pricingPath})
	if err := ds.LoadFromURLIntoMemory(t.Context()); err != nil {
		t.Fatalf("load pricing testdata: %v", err)
	}
	return catalogWithDatasheetForTest(t, ds)
}

func TestListModels_UnknownKeysDoNotFilter(t *testing.T) {
	SetLogger(&mockLogger{})

	h := providerHandlerForTest(t,
		schemas.OpenAI,
		[]schemas.Key{{ID: "key-a"}},
		[]string{"gpt-4o", "gpt-4o-mini"},
		[]string{"gpt-4o", "gpt-4o-mini"},
	)

	ctx := newBareRequestCtx()
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

	h := providerHandlerForTest(t,
		schemas.OpenAI,
		[]schemas.Key{
			{ID: "key-a", Models: []string{"gpt-4o"}},
			{ID: "key-b", Models: []string{"gpt-4o", "gpt-4o-mini"}},
			{ID: "key-disabled", Enabled: new(false)},
		},
		[]string{"gpt-4o", "gpt-4o-mini"},
		[]string{"gpt-4o", "gpt-4o-mini"},
	)

	ctx := newBareRequestCtx()
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

	h := providerHandlerForTest(t,
		schemas.OpenAI,
		[]schemas.Key{{ID: "key-a"}},
		[]string{"gpt-4o", "gpt-4o-mini", "claude-3-5-sonnet"},
		[]string{"gpt-4o", "gpt-4o-mini", "claude-3-5-sonnet"},
	)

	ctx := newBareRequestCtx()
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

// TestListModels_DecisionsKeepsOnlyDecisionModels pins that decisions=true
// lists only models the provider would serve a decision request for, with the
// total counted after filtering.
func TestListModels_DecisionsKeepsOnlyDecisionModels(t *testing.T) {
	SetLogger(&mockLogger{})

	models := []string{"gpt-4o", "gpt-6-luna", "gpt-6-luna-2026-09-01"}
	h := providerHandlerForTest(t, schemas.OpenAI, []schemas.Key{{ID: "key-a"}}, models, models)

	ctx := newBareRequestCtx()
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/models?provider=openai&decisions=true&limit=10")

	h.listModels(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	var resp ListModelsResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	names := make([]string, 0, len(resp.Models))
	for _, model := range resp.Models {
		names = append(names, model.Name)
	}
	if resp.Total != 2 || !slices.Equal(names, []string{"gpt-6-luna", "gpt-6-luna-2026-09-01"}) {
		t.Fatalf("expected only the decision models, got total=%d %v", resp.Total, names)
	}
}

// TestListModels_DecisionsResolvesKeyAliases pins that decisions=true checks a
// key alias as the model it points at, its ModelName before its ModelID, as the
// provider's Decision call does after resolving it, so an alias of a decision
// model is offered and an alias of a chat model is not.
func TestListModels_DecisionsResolvesKeyAliases(t *testing.T) {
	SetLogger(&mockLogger{})

	key := schemas.Key{ID: "key-a", Models: schemas.WhiteList{"*"}, Aliases: schemas.KeyAliases{
		"complexity-judge": {ModelID: "gpt-6-luna"},
		"named-judge":      {ModelID: "luna-deployment-1", ModelName: schemas.Ptr("gpt-6-luna")},
		"fast-chat":        {ModelID: "gpt-4o"},
	}}
	// The listing names key aliases as models alongside the catalog's.
	listed := []string{"complexity-judge", "named-judge", "fast-chat", "gpt-4o"}
	h := providerHandlerForTest(t, schemas.OpenAI, []schemas.Key{key}, listed, listed)
	catalog := modelCatalogForPricingJSON(t, []byte(`{"openai/gpt-4o": {"provider":"openai","mode":"chat"}}`))
	catalog.SetKeyConfigForProvider(schemas.OpenAI, []schemas.Key{key})
	h.config.ModelCatalog = catalog

	ctx := newBareRequestCtx()
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/models?provider=openai&decisions=true&limit=10")

	h.listModels(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	var resp ListModelsResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	names := make([]string, 0, len(resp.Models))
	for _, model := range resp.Models {
		names = append(names, model.Name)
	}
	if !slices.Equal(names, []string{"complexity-judge", "named-judge"}) {
		t.Fatalf("expected only the aliases of decision models, got %v", names)
	}
}

// TestListModels_DecisionsKeepsTypesafeBasedModels pins that decisions=true
// lists every model of a Typesafe-based provider, which serves whichever model
// it is given, without a datasheet row: Typesafe itself and a custom provider
// on its base (a self-hosted Laya, say).
func TestListModels_DecisionsKeepsTypesafeBasedModels(t *testing.T) {
	SetLogger(&mockLogger{})

	for _, provider := range []schemas.ModelProvider{schemas.Typesafe, "laya"} {
		models := []string{"english", "multilingual"}
		h := providerHandlerForTest(t, provider, []schemas.Key{{ID: "key-a"}}, models, models)
		if provider != schemas.Typesafe {
			config := libtest.Providers(h.config)[provider]
			config.CustomProviderConfig = &schemas.CustomProviderConfig{BaseProviderType: schemas.Typesafe, IsKeyLess: true}
			libtest.SetProvider(t, h.config, provider, config, nil)
		}

		ctx := newBareRequestCtx()
		ctx.Request.Header.SetMethod("GET")
		ctx.Request.SetRequestURI("/api/models?provider=" + string(provider) + "&decisions=true&limit=10")
		h.listModels(ctx)

		if ctx.Response.StatusCode() != fasthttp.StatusOK {
			t.Fatalf("%s: expected 200, got %d: %s", provider, ctx.Response.StatusCode(), string(ctx.Response.Body()))
		}
		var resp ListModelsResponse
		if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
			t.Fatalf("failed to unmarshal response: %v", err)
		}
		if resp.Total != 2 {
			t.Fatalf("%s: expected both models listed, got %#v", provider, resp.Models)
		}
	}
}

// TestListModels_DecisionsOpenRouterFollowsDatasheet pins that decisions=true
// lists OpenRouter models by the rule its Decision call gates on: a
// supports_decisions row decides in either direction, and only a model with no
// row falls back to the TypeSafe System One name check. The gpt-6-luna name rule does
// not apply there: OpenRouter's gpt-6-luna chat models are rejected by its decisions
// endpoint, which serves the family as openai/gpt-6-luna-decisions under its own row.
func TestListModels_DecisionsOpenRouterFollowsDatasheet(t *testing.T) {
	SetLogger(&mockLogger{})

	rows := map[string]bool{"microsoft/decision-1": true, "typesafe/jev-router": false, "openai/gpt-6-luna-decisions": true}
	schemas.SetCapabilityResolver(func(_ schemas.ModelProvider, model string) *schemas.ModelCapabilities {
		if supports, ok := rows[model]; ok {
			return &schemas.ModelCapabilities{SupportsDecisions: &supports}
		}
		return nil
	})
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })

	models := []string{"microsoft/decision-1", "typesafe/jev-router", "typesafe/jev-1.13", "openai/gpt-6-luna-decisions", "openai/gpt-6-luna-pro", "openai/gpt-6-luna", "openai/gpt-4o"}
	h := providerHandlerForTest(t, schemas.OpenRouter, []schemas.Key{{ID: "key-a"}}, models, models)

	ctx := newBareRequestCtx()
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/models?provider=openrouter&decisions=true&limit=10")
	h.listModels(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	var resp ListModelsResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	names := make([]string, 0, len(resp.Models))
	for _, model := range resp.Models {
		names = append(names, model.Name)
	}
	slices.Sort(names)
	if want := []string{"microsoft/decision-1", "openai/gpt-6-luna-decisions", "typesafe/jev-1.13"}; !slices.Equal(names, want) {
		t.Fatalf("expected %v, got %v", want, names)
	}
}

// TestListModels_DecisionsCloudflareListsClef pins that decisions=true lists Cloudflare's
// Clef models and nothing else, and that a datasheet row marking one as not a decision
// model wins over the name.
func TestListModels_DecisionsCloudflareListsClef(t *testing.T) {
	SetLogger(&mockLogger{})

	notDecisions := false
	schemas.SetCapabilityResolver(func(_ schemas.ModelProvider, model string) *schemas.ModelCapabilities {
		if model == "@cf/cloudflare/clef" {
			return &schemas.ModelCapabilities{SupportsDecisions: &notDecisions}
		}
		return nil
	})
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })

	models := []string{"@cf/cloudflare/clef", "@cf/cloudflare/clef-flash", "@cf/meta/llama-4-scout-17b-16e-instruct", "@cf/baai/bge-large-en-v1.5"}
	h := providerHandlerForTest(t, schemas.Cloudflare, []schemas.Key{{ID: "key-a"}}, models, models)

	ctx := newBareRequestCtx()
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/models?provider=cloudflare&decisions=true&limit=10")
	h.listModels(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	var resp ListModelsResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	names := make([]string, 0, len(resp.Models))
	for _, model := range resp.Models {
		names = append(names, model.Name)
	}
	if want := []string{"@cf/cloudflare/clef-flash"}; !slices.Equal(names, want) {
		t.Fatalf("expected %v, got %v", want, names)
	}
}

// TestListModels_DecisionsPerplexityListsDeciders pins that decisions=true lists
// Perplexity's decider models and not its chat models.
func TestListModels_DecisionsPerplexityListsDeciders(t *testing.T) {
	SetLogger(&mockLogger{})
	schemas.SetCapabilityResolver(func(schemas.ModelProvider, string) *schemas.ModelCapabilities { return nil })
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })

	models := []string{"pplx-decider-v1-27b", "pplx-decider-v1.1-27b", "sonar-pro"}
	h := providerHandlerForTest(t, schemas.Perplexity, []schemas.Key{{ID: "key-a"}}, models, models)

	ctx := newBareRequestCtx()
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/models?provider=perplexity&decisions=true&limit=10")
	h.listModels(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	var resp ListModelsResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	names := make([]string, 0, len(resp.Models))
	for _, model := range resp.Models {
		names = append(names, model.Name)
	}
	slices.Sort(names)
	if want := []string{"pplx-decider-v1-27b", "pplx-decider-v1.1-27b"}; !slices.Equal(names, want) {
		t.Fatalf("expected %v, got %v", want, names)
	}
}

// TestListModels_DecisionsOllamaListsSystemOneModels pins that decisions=true
// lists Ollama's System One models, tagged or not, and not its chat models.
func TestListModels_DecisionsOllamaListsSystemOneModels(t *testing.T) {
	SetLogger(&mockLogger{})
	schemas.SetCapabilityResolver(func(schemas.ModelProvider, string) *schemas.ModelCapabilities { return nil })
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })

	models := []string{"nimble", "tev1:0.8b", "clef-flash", "llama3.1:latest"}
	h := providerHandlerForTest(t, schemas.Ollama, []schemas.Key{{ID: "key-a"}}, models, models)

	ctx := newBareRequestCtx()
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/models?provider=ollama&decisions=true&limit=10")
	h.listModels(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	var resp ListModelsResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	names := make([]string, 0, len(resp.Models))
	for _, model := range resp.Models {
		names = append(names, model.Name)
	}
	slices.Sort(names)
	if want := []string{"clef-flash", "nimble", "tev1:0.8b"}; !slices.Equal(names, want) {
		t.Fatalf("expected %v, got %v", want, names)
	}
}

// TestListModels_DecisionsBasetenListsDecisionModels pins that decisions=true lists
// Baseten's decision-models/ slugs, which its Decision call serves on /v1/systemone,
// and not its chat models.
func TestListModels_DecisionsBasetenListsDecisionModels(t *testing.T) {
	SetLogger(&mockLogger{})
	schemas.SetCapabilityResolver(func(schemas.ModelProvider, string) *schemas.ModelCapabilities { return nil })
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })

	models := []string{"decision-models/baseten-joey-2b-latest", "decision-models/baseten-elephant-9b-latest", "zai-org/GLM-5.3"}
	h := providerHandlerForTest(t, schemas.Baseten, []schemas.Key{{ID: "key-a"}}, models, models)

	ctx := newBareRequestCtx()
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/models?provider=baseten&decisions=true&limit=10")
	h.listModels(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	var resp ListModelsResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	names := make([]string, 0, len(resp.Models))
	for _, model := range resp.Models {
		names = append(names, model.Name)
	}
	slices.Sort(names)
	if want := []string{"decision-models/baseten-elephant-9b-latest", "decision-models/baseten-joey-2b-latest"}; !slices.Equal(names, want) {
		t.Fatalf("expected %v, got %v", want, names)
	}
}

// TestListModels_DecisionsGeminiListsDecisionModels pins that decisions=true lists
// Gemini's Decision models, which its Decision call serves on /v1beta/decisions, and
// not its chat models, for Gemini itself and for a custom provider on the Gemini
// base, which its Decision gates by the same name.
func TestListModels_DecisionsGeminiListsDecisionModels(t *testing.T) {
	SetLogger(&mockLogger{})
	schemas.SetCapabilityResolver(func(schemas.ModelProvider, string) *schemas.ModelCapabilities { return nil })
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })

	for _, provider := range []schemas.ModelProvider{schemas.Gemini, "my-gemini"} {
		models := []string{"gemini-decision-1", "gemini-3-flash", "gemini-embedding-001"}
		h := providerHandlerForTest(t, provider, []schemas.Key{{ID: "key-a"}}, models, models)
		if provider != schemas.Gemini {
			config := libtest.Providers(h.config)[provider]
			config.CustomProviderConfig = &schemas.CustomProviderConfig{BaseProviderType: schemas.Gemini, AllowedRequests: &schemas.AllowedRequests{ListModels: true, Decision: true}}
			libtest.SetProvider(t, h.config, provider, config, nil)
		}

		ctx := newBareRequestCtx()
		ctx.Request.Header.SetMethod("GET")
		ctx.Request.SetRequestURI("/api/models?provider=" + string(provider) + "&decisions=true&limit=10")
		h.listModels(ctx)

		if ctx.Response.StatusCode() != fasthttp.StatusOK {
			t.Fatalf("%s: expected 200, got %d: %s", provider, ctx.Response.StatusCode(), string(ctx.Response.Body()))
		}
		var resp ListModelsResponse
		if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
			t.Fatalf("failed to unmarshal response: %v", err)
		}
		names := make([]string, 0, len(resp.Models))
		for _, model := range resp.Models {
			names = append(names, model.Name)
		}
		if want := []string{"gemini-decision-1"}; !slices.Equal(names, want) {
			t.Fatalf("%s: expected %v, got %v", provider, want, names)
		}
	}
}

// TestListModels_DecisionsVLLMListsEveryModel pins that decisions=true lists every
// model a vLLM server serves: the engine answers /v1/systemone for whatever it loads.
func TestListModels_DecisionsVLLMListsEveryModel(t *testing.T) {
	SetLogger(&mockLogger{})
	schemas.SetCapabilityResolver(func(schemas.ModelProvider, string) *schemas.ModelCapabilities { return nil })
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })

	models := []string{"clef-flash", "Qwen/Qwen3-0.6B"}
	h := providerHandlerForTest(t, schemas.VLLM, []schemas.Key{{ID: "key-a"}}, models, models)

	ctx := newBareRequestCtx()
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/models?provider=vllm&decisions=true&limit=10")
	h.listModels(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	var resp ListModelsResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	names := make([]string, 0, len(resp.Models))
	for _, model := range resp.Models {
		names = append(names, model.Name)
	}
	slices.Sort(names)
	if want := []string{"Qwen/Qwen3-0.6B", "clef-flash"}; !slices.Equal(names, want) {
		t.Fatalf("expected %v, got %v", want, names)
	}
}

// TestListModels_DecisionsVLLMIgnoresNegativeRows pins that a datasheet row marking a
// model supports_decisions=false does not hide it on vLLM or SGLang: the engine answers
// /v1/systemone for every model it loads, whatever the row says.
func TestListModels_DecisionsVLLMIgnoresNegativeRows(t *testing.T) {
	SetLogger(&mockLogger{})
	schemas.SetCapabilityResolver(func(schemas.ModelProvider, string) *schemas.ModelCapabilities {
		return &schemas.ModelCapabilities{SupportsDecisions: schemas.Ptr(false)}
	})
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })

	for _, provider := range []schemas.ModelProvider{schemas.VLLM, schemas.SGL} {
		models := []string{"clef-flash", "Qwen/Qwen3-0.6B"}
		h := providerHandlerForTest(t, provider, []schemas.Key{{ID: "key-a"}}, models, models)

		ctx := newBareRequestCtx()
		ctx.Request.Header.SetMethod("GET")
		ctx.Request.SetRequestURI("/api/models?provider=" + string(provider) + "&decisions=true&limit=10")
		h.listModels(ctx)

		if ctx.Response.StatusCode() != fasthttp.StatusOK {
			t.Fatalf("%s: expected 200, got %d: %s", provider, ctx.Response.StatusCode(), string(ctx.Response.Body()))
		}
		var resp ListModelsResponse
		if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
			t.Fatalf("%s: failed to unmarshal response: %v", provider, err)
		}
		names := make([]string, 0, len(resp.Models))
		for _, model := range resp.Models {
			names = append(names, model.Name)
		}
		slices.Sort(names)
		if want := []string{"Qwen/Qwen3-0.6B", "clef-flash"}; !slices.Equal(names, want) {
			t.Fatalf("%s: expected %v, got %v", provider, want, names)
		}
	}
}

// TestListModels_DecisionsSGLListsEveryModel pins the same for an SGLang server.
func TestListModels_DecisionsSGLListsEveryModel(t *testing.T) {
	SetLogger(&mockLogger{})
	schemas.SetCapabilityResolver(func(schemas.ModelProvider, string) *schemas.ModelCapabilities { return nil })
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })

	models := []string{"clef-flash", "jaredpalmer/kev-4b"}
	h := providerHandlerForTest(t, schemas.SGL, []schemas.Key{{ID: "key-a"}}, models, models)

	ctx := newBareRequestCtx()
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/models?provider=sgl&decisions=true&limit=10")
	h.listModels(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	var resp ListModelsResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	names := make([]string, 0, len(resp.Models))
	for _, model := range resp.Models {
		names = append(names, model.Name)
	}
	slices.Sort(names)
	if want := []string{"clef-flash", "jaredpalmer/kev-4b"}; !slices.Equal(names, want) {
		t.Fatalf("expected %v, got %v", want, names)
	}
}

// TestListModels_DecisionsAzureListsDecisionModels pins that decisions=true lists
// Azure's decision models, Microsoft's (by name or through a key alias whose target
// is a deployment named otherwise) and the gpt-6-luna family, which Azure serves on
// /openai/v1/decisions, and not chat deployments.
func TestListModels_DecisionsAzureListsDecisionModels(t *testing.T) {
	SetLogger(&mockLogger{})
	schemas.SetCapabilityResolver(func(schemas.ModelProvider, string) *schemas.ModelCapabilities { return nil })
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })

	key := schemas.Key{ID: "key-a", Models: schemas.WhiteList{"*"}, Aliases: schemas.KeyAliases{
		"Microsoft-Decision-1": {ModelID: "Decision-1"},
	}}
	listed := []string{"Microsoft-Decision-1", "microsoft-decision-2", "gpt-6-luna", "gpt-6-luna-2026-09-22", "gpt-4o"}
	h := providerHandlerForTest(t, schemas.Azure, []schemas.Key{key}, listed, listed)
	catalog := modelCatalogForPricingJSON(t, []byte(`{"azure/gpt-4o": {"provider":"azure","mode":"chat"}}`))
	catalog.SetKeyConfigForProvider(schemas.Azure, []schemas.Key{key})
	h.config.ModelCatalog = catalog

	ctx := newBareRequestCtx()
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/models?provider=azure&decisions=true&limit=10")
	h.listModels(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	var resp ListModelsResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	names := make([]string, 0, len(resp.Models))
	for _, model := range resp.Models {
		names = append(names, model.Name)
	}
	slices.Sort(names)
	if want := []string{"Microsoft-Decision-1", "gpt-6-luna", "gpt-6-luna-2026-09-22", "microsoft-decision-2"}; !slices.Equal(names, want) {
		t.Fatalf("expected %v, got %v", want, names)
	}
}

// TestListModels_DecisionsDatabricksListsDecisionModels pins that a decisions listing on
// Databricks offers the models its Decision serves natively, ai_decide and the System One
// models by their Unity Catalog names, and not the workspace's serving endpoints, including
// a System One model's own endpoint name, which the systemone route rejects.
func TestListModels_DecisionsDatabricksListsDecisionModels(t *testing.T) {
	SetLogger(&mockLogger{})
	schemas.SetCapabilityResolver(func(schemas.ModelProvider, string) *schemas.ModelCapabilities { return nil })
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })

	listed := []string{
		"ai_decide", "system.ai.openjev-qwen35-4b", "system.ai.typesafeai-jev-1-14-0",
		"databricks-openjev-qwen35-4b", "databricks-claude-sonnet-4", "databricks-gpt-oss-120b",
	}
	h := providerHandlerForTest(t, schemas.Databricks, []schemas.Key{{ID: "key-a"}}, listed, listed)

	ctx := newBareRequestCtx()
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/models?provider=databricks&decisions=true&limit=10")
	h.listModels(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	var resp ListModelsResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	names := make([]string, 0, len(resp.Models))
	for _, model := range resp.Models {
		names = append(names, model.Name)
	}
	if want := []string{"ai_decide", "system.ai.openjev-qwen35-4b", "system.ai.typesafeai-jev-1-14-0"}; !slices.Equal(names, want) {
		t.Fatalf("expected %v, got %v", want, names)
	}
}

// TestListModels_DecisionsGPT6LunaOnlyOnOpenAI pins that the gpt-6-luna name rule
// lists the family only on OpenAI; any other provider hosting it lists it only
// with a supports_decisions row.
func TestListModels_DecisionsGPT6LunaOnlyOnOpenAI(t *testing.T) {
	SetLogger(&mockLogger{})
	schemas.SetCapabilityResolver(func(schemas.ModelProvider, string) *schemas.ModelCapabilities { return nil })
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })

	for provider, wantListed := range map[schemas.ModelProvider]bool{schemas.OpenAI: true, schemas.Groq: false} {
		models := []string{"gpt-6-luna", "gpt-4o"}
		h := providerHandlerForTest(t, provider, []schemas.Key{{ID: "key-a"}}, models, models)

		ctx := newBareRequestCtx()
		ctx.Request.Header.SetMethod("GET")
		ctx.Request.SetRequestURI("/api/models?provider=" + string(provider) + "&decisions=true&limit=10")
		h.listModels(ctx)

		if ctx.Response.StatusCode() != fasthttp.StatusOK {
			t.Fatalf("%s: expected 200, got %d: %s", provider, ctx.Response.StatusCode(), string(ctx.Response.Body()))
		}
		var resp ListModelsResponse
		if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
			t.Fatalf("failed to unmarshal response: %v", err)
		}
		listed := len(resp.Models) == 1 && resp.Models[0].Name == "gpt-6-luna"
		if listed != wantListed || (!wantListed && len(resp.Models) != 0) {
			t.Fatalf("%s: listed gpt-6-luna = %v (models %#v), want %v", provider, listed, resp.Models, wantListed)
		}
	}
}

// TestListModels_DecisionsListsEveryModelOnCustomOpenAIBase pins that a
// custom provider on OpenAI's base allowing decisions (set, or allowed requests
// unset) lists every model under decisions=true, matching its Decision call,
// which is gated by its allowed requests alone.
func TestListModels_DecisionsListsEveryModelOnCustomOpenAIBase(t *testing.T) {
	SetLogger(&mockLogger{})
	schemas.SetCapabilityResolver(func(schemas.ModelProvider, string) *schemas.ModelCapabilities { return nil })
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })

	for name, allowed := range map[string]*schemas.AllowedRequests{"decisions on": {ListModels: true, Decision: true}, "unset": nil} {
		models := []string{"my-decider", "gpt-6-luna"}
		h := providerHandlerForTest(t, "kserve", []schemas.Key{{ID: "key-a"}}, models, models)
		config := libtest.Providers(h.config)["kserve"]
		config.CustomProviderConfig = &schemas.CustomProviderConfig{BaseProviderType: schemas.OpenAI, IsKeyLess: true, AllowedRequests: allowed}
		libtest.SetProvider(t, h.config, "kserve", config, nil)

		ctx := newBareRequestCtx()
		ctx.Request.Header.SetMethod("GET")
		ctx.Request.SetRequestURI("/api/models?provider=kserve&decisions=true&limit=10")
		h.listModels(ctx)

		if ctx.Response.StatusCode() != fasthttp.StatusOK {
			t.Fatalf("%s: expected 200, got %d: %s", name, ctx.Response.StatusCode(), string(ctx.Response.Body()))
		}
		var resp ListModelsResponse
		if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
			t.Fatalf("failed to unmarshal response: %v", err)
		}
		if resp.Total != len(models) {
			t.Fatalf("%s: expected every model listed, got %#v", name, resp.Models)
		}
	}
}

// TestListModels_DecisionsListsEveryModelOnCustomEngineBase pins that decisions=true
// lists every model of a custom provider built on the vLLM or SGLang base once it
// allows decisions: both engines serve /v1/systemone for whatever they load.
func TestListModels_DecisionsListsEveryModelOnCustomEngineBase(t *testing.T) {
	SetLogger(&mockLogger{})
	schemas.SetCapabilityResolver(func(schemas.ModelProvider, string) *schemas.ModelCapabilities { return nil })
	t.Cleanup(func() { schemas.SetCapabilityResolver(nil) })

	for _, base := range []schemas.ModelProvider{schemas.VLLM, schemas.SGL} {
		models := []string{"clef-flash", "Qwen/Qwen3-0.6B"}
		h := providerHandlerForTest(t, "my-engine", []schemas.Key{{ID: "key-a"}}, models, models)
		config := libtest.Providers(h.config)["my-engine"]
		config.CustomProviderConfig = &schemas.CustomProviderConfig{BaseProviderType: base, IsKeyLess: true, AllowedRequests: &schemas.AllowedRequests{ListModels: true, Decision: true}}
		libtest.SetProvider(t, h.config, "my-engine", config, nil)

		ctx := newBareRequestCtx()
		ctx.Request.Header.SetMethod("GET")
		ctx.Request.SetRequestURI("/api/models?provider=my-engine&decisions=true&limit=10")
		h.listModels(ctx)

		if ctx.Response.StatusCode() != fasthttp.StatusOK {
			t.Fatalf("%s: expected 200, got %d: %s", base, ctx.Response.StatusCode(), string(ctx.Response.Body()))
		}
		var resp ListModelsResponse
		if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
			t.Fatalf("failed to unmarshal response: %v", err)
		}
		if resp.Total != len(models) {
			t.Fatalf("%s: expected every model listed, got %#v", base, resp.Models)
		}
	}
}

// TestListModels_DecisionsSkipsProvidersDenyingDecisions pins that
// decisions=true lists nothing from a custom provider whose allowed requests
// exclude decisions, since its Decision call is denied rather than served:
// one on OpenAI's base with a decisions model, and one on Typesafe's.
func TestListModels_DecisionsSkipsProvidersDenyingDecisions(t *testing.T) {
	SetLogger(&mockLogger{})

	for provider, base := range map[schemas.ModelProvider]schemas.ModelProvider{"my-openai": schemas.OpenAI, "laya": schemas.Typesafe} {
		models := []string{"gpt-6-luna", "english"}
		h := providerHandlerForTest(t, provider, []schemas.Key{{ID: "key-a"}}, models, models)
		config := libtest.Providers(h.config)[provider]
		config.CustomProviderConfig = &schemas.CustomProviderConfig{BaseProviderType: base, IsKeyLess: true, AllowedRequests: &schemas.AllowedRequests{ListModels: true}}
		libtest.SetProvider(t, h.config, provider, config, nil)

		ctx := newBareRequestCtx()
		ctx.Request.Header.SetMethod("GET")
		ctx.Request.SetRequestURI("/api/models?provider=" + string(provider) + "&decisions=true&limit=10")
		h.listModels(ctx)

		if ctx.Response.StatusCode() != fasthttp.StatusOK {
			t.Fatalf("%s: expected 200, got %d: %s", provider, ctx.Response.StatusCode(), string(ctx.Response.Body()))
		}
		var resp ListModelsResponse
		if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
			t.Fatalf("failed to unmarshal response: %v", err)
		}
		if resp.Total != 0 {
			t.Fatalf("%s: expected no models listed, got %#v", provider, resp.Models)
		}
	}
}

func TestListModels_MarksDeprecatedModelsWithoutFiltering(t *testing.T) {
	SetLogger(&mockLogger{})

	h := providerHandlerForTest(t,
		schemas.OpenAI,
		[]schemas.Key{{ID: "key-a"}},
		[]string{"deprecated-model", "current-model", "another-current-model"},
		[]string{"deprecated-model", "current-model", "another-current-model"},
	)

	pricingJSON := []byte(`{
		"deprecated-model": {"provider":"openai","mode":"chat","base_model":"deprecated-model","is_deprecated":true},
		"current-model": {"provider":"openai","mode":"chat","base_model":"current-model"},
		"another-current-model": {"provider":"openai","mode":"chat","base_model":"another-current-model"}
	}`)
	h.config.ModelCatalog = modelCatalogForPricingJSON(t, pricingJSON)

	ctx := newBareRequestCtx()
	ctx.Request.Header.SetMethod("GET")
	// Searched, because an unsearched listing drops deprecated models outright.
	ctx.Request.SetRequestURI("/api/models?provider=openai&query=model&limit=10")

	h.listModels(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}

	var resp ListModelsResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Total != 3 {
		t.Fatalf("expected total=3 (a search does not filter deprecated models), got %d", resp.Total)
	}
	var deprecated *ModelResponse
	for i := range resp.Models {
		if resp.Models[i].Name == "deprecated-model" {
			deprecated = &resp.Models[i]
		}
	}
	if deprecated == nil {
		t.Fatalf("deprecated model should still be returned, got %#v", resp.Models)
	}
	if !deprecated.IsDeprecated {
		t.Fatalf("deprecated model should carry is_deprecated=true, got %#v", *deprecated)
	}
}

func TestListBaseModels_IncludesDeprecatedPricingRows(t *testing.T) {
	SetLogger(&mockLogger{})

	h := providerHandlerForTest(t,
		schemas.OpenAI,
		[]schemas.Key{{ID: "key-a"}},
		nil,
		nil,
	)
	h.config.ModelCatalog = modelCatalogForPricingJSON(t, []byte(`{
		"deprecated-model": {"provider":"openai","mode":"chat","base_model":"deprecated-base","is_deprecated":true},
		"current-model": {"provider":"openai","mode":"chat","base_model":"current-base"}
	}`))

	ctx := newBareRequestCtx()
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/models/base?limit=10")

	h.listBaseModels(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}

	var resp ListBaseModelsResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	if resp.Total != 2 || !slices.Contains(resp.Models, "current-base") || !slices.Contains(resp.Models, "deprecated-base") {
		t.Fatalf("expected both base models, got %#v", resp)
	}
}

func TestEnrichListModelsResponse_MarksDeprecatedPricingRows(t *testing.T) {
	catalog := modelCatalogForPricingJSON(t, []byte(`{
		"deprecated-model": {"provider":"openai","mode":"chat","base_model":"deprecated-model","is_deprecated":true},
		"current-model": {"provider":"openai","mode":"chat","base_model":"current-model"}
	}`))
	resp := &schemas.BifrostListModelsResponse{Data: []schemas.Model{
		{ID: "openai/deprecated-model"},
		{ID: "openai/current-model"},
		{ID: "openai/provider-deprecated", IsDeprecated: true},
	}}

	catalog.EnrichListModelsResponse(resp)

	if len(resp.Data) != 3 {
		t.Fatalf("expected all models retained, got %#v", resp.Data)
	}
	byID := map[string]schemas.Model{}
	for _, m := range resp.Data {
		byID[m.ID] = m
	}
	if !byID["openai/deprecated-model"].IsDeprecated {
		t.Fatalf("catalog-deprecated model should be marked deprecated: %#v", byID["openai/deprecated-model"])
	}
	if byID["openai/current-model"].IsDeprecated {
		t.Fatalf("current model should not be marked deprecated: %#v", byID["openai/current-model"])
	}
	if !byID["openai/provider-deprecated"].IsDeprecated {
		t.Fatalf("provider-deprecated flag should be preserved: %#v", byID["openai/provider-deprecated"])
	}
}

func TestListModels_UnfilteredIgnoresKeys(t *testing.T) {
	SetLogger(&mockLogger{})

	h := providerHandlerForTest(t,
		schemas.OpenAI,
		[]schemas.Key{
			{ID: "key-b", Models: []string{"gpt-4o-mini"}},
		},
		[]string{"gpt-4o"},
		[]string{"gpt-4o", "gpt-4o-mini"},
	)

	ctx := newBareRequestCtx()
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

	h := providerHandlerForTest(t,
		schemas.OpenAI,
		[]schemas.Key{
			{ID: "key-b", Models: []string{"gpt-4o-mini"}},
		},
		[]string{"gpt-4o"},
		[]string{"gpt-4o", "gpt-4o-mini"},
	)

	ctx := newBareRequestCtx()
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

	h := providerHandlerForTest(t,
		schemas.OpenAI,
		[]schemas.Key{{ID: "key-a"}},
		[]string{"gpt-4o"},
		[]string{"gpt-4o"},
	)

	ctx := newBareRequestCtx()
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/models/details?provider=openai")

	h.listModelDetails(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
}

func TestListModelDetails_UnknownKeysDoNotFilter(t *testing.T) {
	SetLogger(&mockLogger{})

	h := providerHandlerForTest(t,
		schemas.OpenAI,
		[]schemas.Key{{ID: "key-a"}},
		[]string{"gpt-4o", "gpt-4o-mini"},
		[]string{"gpt-4o", "gpt-4o-mini"},
	)
	h.config.ModelCatalog = catalogForTest(t, nil)

	ctx := newBareRequestCtx()
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

	h := providerHandlerForTest(t,
		schemas.OpenAI,
		[]schemas.Key{{ID: "key-a", Models: []string{"gpt-4o"}}},
		[]string{"gpt-4o", "gpt-4o-mini"},
		[]string{"gpt-4o", "gpt-4o-mini"},
	)
	h.config.ModelCatalog = catalogForTest(t, nil)

	ctx := newBareRequestCtx()
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

	h := providerHandlerForTest(t,
		schemas.OpenAI,
		[]schemas.Key{
			{ID: "key-a", Models: []string{"gpt-4o"}},
			{ID: "key-disabled", Enabled: new(false)},
		},
		[]string{"gpt-4o", "gpt-4o-mini"},
		[]string{"gpt-4o", "gpt-4o-mini"},
	)
	h.config.ModelCatalog = catalogForTest(t, nil)

	ctx := newBareRequestCtx()
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

	h := providerHandlerForTest(t,
		schemas.OpenAI,
		[]schemas.Key{
			{ID: "key-b", Models: []string{"gpt-4o-mini"}},
		},
		[]string{"gpt-4o"},
		[]string{"gpt-4o", "gpt-4o-mini"},
	)
	h.config.ModelCatalog = catalogForTest(t, nil)

	ctx := newBareRequestCtx()
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

func TestListModelDetails_IncludesPricing(t *testing.T) {
	SetLogger(&mockLogger{})

	h := providerHandlerForTest(t,
		schemas.OpenAI,
		[]schemas.Key{{ID: "key-a"}},
		[]string{"gpt-4o"},
		[]string{"gpt-4o"},
	)
	h.config.ModelCatalog = modelCatalogForPricingJSON(t, []byte(`{
		"gpt-4o": {
			"provider": "openai",
			"mode": "chat",
			"input_cost_per_token": 0.0000025,
			"output_cost_per_token": 0.00001,
			"cache_creation_input_token_cost": 0.000003125,
			"cache_read_input_token_cost": 0.00000025,
			"max_input_tokens": 128000,
			"max_output_tokens": 16384
		}
	}`))

	ctx := newBareRequestCtx()
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/models/details?provider=openai&limit=100")

	h.listModelDetails(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}

	var resp ListModelDetailsResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Total != 1 || len(resp.Models) != 1 {
		t.Fatalf("expected one model, got %#v", resp.Models)
	}
	if resp.Models[0].InputCostPerToken == nil || *resp.Models[0].InputCostPerToken != 0.0000025 {
		t.Fatalf("expected input cost 0.0000025, got %#v", resp.Models[0].InputCostPerToken)
	}
	if resp.Models[0].OutputCostPerToken == nil || *resp.Models[0].OutputCostPerToken != 0.00001 {
		t.Fatalf("expected output cost 0.00001, got %#v", resp.Models[0].OutputCostPerToken)
	}
	if resp.Models[0].CacheWriteCost == nil || *resp.Models[0].CacheWriteCost != 0.000003125 {
		t.Fatalf("expected cache write cost 0.000003125, got %#v", resp.Models[0].CacheWriteCost)
	}
	if resp.Models[0].CacheReadCost == nil || *resp.Models[0].CacheReadCost != 0.00000025 {
		t.Fatalf("expected cache read cost 0.00000025, got %#v", resp.Models[0].CacheReadCost)
	}
}

func TestListModelDetails_ResolvesCatalogPricing(t *testing.T) {
	SetLogger(&mockLogger{})

	togetherGLM := "zai-org/GLM-5.2"
	azureGLM := "FW-GLM-5.2"
	tests := []struct {
		name        string
		provider    schemas.ModelProvider
		model       string
		alias       *schemas.AliasConfig
		pricingJSON string
		inputCost   float64
		outputCost  float64
		cacheCost   float64
	}{
		{
			name:     "Together catalog provider",
			provider: schemas.ModelProvider("together"),
			model:    "deepseek-ai/DeepSeek-V4-Flash-0731",
			pricingJSON: `{"together_ai/deepseek-ai/DeepSeek-V4-Flash-0731": {
				"provider": "together_ai", "mode": "chat",
				"input_cost_per_token": 0.00000014, "output_cost_per_token": 0.00000028,
				"cache_read_input_token_cost": 0.00000003
			}}`,
			inputCost:  0.00000014,
			outputCost: 0.00000028,
			cacheCost:  0.00000003,
		},
		{
			name:     "Together alias",
			provider: schemas.ModelProvider("together"),
			model:    "glm-5-2",
			alias:    &schemas.AliasConfig{ModelID: togetherGLM, ModelName: &togetherGLM},
			pricingJSON: `{"together_ai/zai-org/GLM-5.2": {
				"provider": "together_ai", "mode": "chat",
				"input_cost_per_token": 0.0000014, "output_cost_per_token": 0.0000044,
				"cache_read_input_token_cost": 0.00000026
			}}`,
			inputCost:  0.0000014,
			outputCost: 0.0000044,
			cacheCost:  0.00000026,
		},
		{
			name:     "Azure alias",
			provider: schemas.Azure,
			model:    "glm-5-2",
			alias:    &schemas.AliasConfig{ModelID: "glm-5-2", ModelName: &azureGLM},
			pricingJSON: `{"azure/glm-5-2": {
				"provider": "azure", "mode": "chat", "input_cost_per_token": 9
			}, "azure/FW-GLM-5.2": {
				"provider": "azure", "mode": "chat",
				"input_cost_per_token": 0.00000154, "output_cost_per_token": 0.00000484,
				"cache_read_input_token_cost": 0.00000015
			}}`,
			inputCost:  0.00000154,
			outputCost: 0.00000484,
			cacheCost:  0.00000015,
		},
		{
			name:     "Azure alias with empty model name",
			provider: schemas.Azure,
			model:    "glm-5-2-empty-name",
			alias:    &schemas.AliasConfig{ModelID: azureGLM, ModelName: schemas.Ptr("")},
			pricingJSON: `{"azure/FW-GLM-5.2": {
				"provider": "azure", "mode": "chat",
				"input_cost_per_token": 0.00000154, "output_cost_per_token": 0.00000484,
				"cache_read_input_token_cost": 0.00000015
			}}`,
			inputCost:  0.00000154,
			outputCost: 0.00000484,
			cacheCost:  0.00000015,
		},
		{
			name:     "Azure alias falls back to alias key",
			provider: schemas.Azure,
			model:    "gpt-4o",
			alias:    &schemas.AliasConfig{ModelID: "my-deployment-123"},
			pricingJSON: `{"azure/gpt-4o": {
				"provider": "azure", "mode": "chat",
				"input_cost_per_token": 0.0000025, "output_cost_per_token": 0.00001,
				"cache_read_input_token_cost": 0.00000025
			}}`,
			inputCost:  0.0000025,
			outputCost: 0.00001,
			cacheCost:  0.00000025,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			key := schemas.Key{ID: "key-a", Models: schemas.WhiteList{"*"}}
			if test.alias != nil {
				key.Aliases = schemas.KeyAliases{test.model: *test.alias}
			}
			catalog := modelCatalogForPricingJSON(t, []byte(test.pricingJSON))
			catalog.SetKeyConfigForProvider(test.provider, []schemas.Key{key})
			h := providerHandlerForTest(t, test.provider, []schemas.Key{key}, []string{test.model}, []string{test.model})
			h.config.ModelCatalog = catalog

			resp, _ := listModelDetailsForTest(t, h, "/api/models/details?provider="+string(test.provider)+"&limit=100")
			if resp.Total != 1 || len(resp.Models) != 1 {
				t.Fatalf("expected one model, got %#v", resp.Models)
			}
			model := resp.Models[0]
			if model.Provider != string(test.provider) {
				t.Fatalf("expected runtime provider %s, got %q", test.provider, model.Provider)
			}
			if model.InputCostPerToken == nil || *model.InputCostPerToken != test.inputCost {
				t.Fatalf("expected input cost %g, got %#v", test.inputCost, model.InputCostPerToken)
			}
			if model.OutputCostPerToken == nil || *model.OutputCostPerToken != test.outputCost {
				t.Fatalf("expected output cost %g, got %#v", test.outputCost, model.OutputCostPerToken)
			}
			if model.CacheReadCost == nil || *model.CacheReadCost != test.cacheCost {
				t.Fatalf("expected cache read cost %g, got %#v", test.cacheCost, model.CacheReadCost)
			}
		})
	}
}

// gpt4oPricingJSON is the base catalog fixture shared by the override tests.
const gpt4oPricingJSON = `{
	"gpt-4o": {
		"provider": "openai",
		"mode": "chat",
		"input_cost_per_token": 0.0000025,
		"output_cost_per_token": 0.00001
	},
	"gpt-4o-mini": {
		"provider": "openai",
		"mode": "chat",
		"input_cost_per_token": 0.0000001,
		"output_cost_per_token": 0.0000004
	}
}`

// listModelDetailsForTest issues the details request and decodes the response,
// also returning the raw body so tests can assert on omitted JSON keys.
func listModelDetailsForTest(t *testing.T, h *ProviderHandler, uri string) (ListModelDetailsResponse, string) {
	t.Helper()
	ctx := newBareRequestCtx()
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI(uri)

	h.listModelDetails(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}
	body := string(ctx.Response.Body())
	var resp ListModelDetailsResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	return resp, body
}

func TestListModelDetails_AppliesGlobalOverrideWithoutMutatingBase(t *testing.T) {
	SetLogger(&mockLogger{})

	h := providerHandlerForTest(t, schemas.OpenAI, []schemas.Key{{ID: "key-a"}}, []string{"gpt-4o"}, []string{"gpt-4o"})
	h.config.ModelCatalog = modelCatalogForPricingJSON(t, []byte(gpt4oPricingJSON))
	if err := h.config.ModelCatalog.SetPricingOverrides([]configstoreTables.TablePricingOverride{{
		ID:               "global-1",
		Name:             "Negotiated rate",
		ScopeKind:        string(modelcatalog.ScopeKindGlobal),
		MatchType:        string(modelcatalog.MatchTypeExact),
		Pattern:          "gpt-4o",
		RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
		PricingPatchJSON: `{"input_cost_per_token":0.000001}`,
	}}); err != nil {
		t.Fatalf("seed overrides: %v", err)
	}

	resp, _ := listModelDetailsForTest(t, h, "/api/models/details?provider=openai&limit=100")

	if len(resp.Models) != 1 {
		t.Fatalf("expected one model, got %#v", resp.Models)
	}
	m := resp.Models[0]
	if m.InputCostPerToken == nil || *m.InputCostPerToken != 0.0000025 {
		t.Fatalf("base input cost must stay unchanged, got %#v", m.InputCostPerToken)
	}
	if m.OverriddenPricing == nil || m.OverriddenPricing.InputCostPerToken == nil ||
		*m.OverriddenPricing.InputCostPerToken != 0.000001 {
		t.Fatalf("expected overridden input cost 0.000001, got %#v", m.OverriddenPricing)
	}
	if m.OverriddenPricing.OutputCostPerToken != nil {
		t.Fatalf("output cost was not patched, expected nil, got %#v", m.OverriddenPricing.OutputCostPerToken)
	}
	if m.AppliedOverrideID != "global-1" {
		t.Fatalf("expected applied override global-1, got %q", m.AppliedOverrideID)
	}
	if len(m.PricingOverrideIDs) != 1 || m.PricingOverrideIDs[0] != "global-1" {
		t.Fatalf("expected one referenced override, got %#v", m.PricingOverrideIDs)
	}
	summary, ok := resp.PricingOverrides["global-1"]
	if !ok {
		t.Fatalf("override index missing global-1: %#v", resp.PricingOverrides)
	}
	if summary.Name != "Negotiated rate" || summary.Pattern != "gpt-4o" {
		t.Fatalf("unexpected override summary %#v", summary)
	}
	if summary.Patch.InputCostPerToken == nil || *summary.Patch.InputCostPerToken != 0.000001 {
		t.Fatalf("expected patch in summary, got %#v", summary.Patch)
	}
}

func TestListModelDetails_OverrideIndexIsDeduplicated(t *testing.T) {
	SetLogger(&mockLogger{})

	h := providerHandlerForTest(t,
		schemas.OpenAI,
		[]schemas.Key{{ID: "key-a"}},
		[]string{"gpt-4o", "gpt-4o-mini"},
		[]string{"gpt-4o", "gpt-4o-mini"},
	)
	h.config.ModelCatalog = modelCatalogForPricingJSON(t, []byte(gpt4oPricingJSON))
	if err := h.config.ModelCatalog.SetPricingOverrides([]configstoreTables.TablePricingOverride{{
		ID:               "wildcard-1",
		Name:             "All GPT models",
		ScopeKind:        string(modelcatalog.ScopeKindGlobal),
		MatchType:        string(modelcatalog.MatchTypeWildcard),
		Pattern:          "gpt-*",
		RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
		PricingPatchJSON: `{"input_cost_per_token":0.000002}`,
	}}); err != nil {
		t.Fatalf("seed overrides: %v", err)
	}

	resp, _ := listModelDetailsForTest(t, h, "/api/models/details?provider=openai&limit=100")

	if len(resp.Models) != 2 {
		t.Fatalf("expected two models, got %#v", resp.Models)
	}
	if len(resp.PricingOverrides) != 1 {
		t.Fatalf("expected the shared override serialized once, got %#v", resp.PricingOverrides)
	}
	for _, m := range resp.Models {
		if m.AppliedOverrideID != "wildcard-1" {
			t.Fatalf("model %s missing applied override, got %q", m.Name, m.AppliedOverrideID)
		}
	}
}

func TestListModelDetails_NoOverridesOmitsNewFields(t *testing.T) {
	SetLogger(&mockLogger{})

	h := providerHandlerForTest(t, schemas.OpenAI, []schemas.Key{{ID: "key-a"}}, []string{"gpt-4o"}, []string{"gpt-4o"})
	h.config.ModelCatalog = modelCatalogForPricingJSON(t, []byte(gpt4oPricingJSON))

	_, body := listModelDetailsForTest(t, h, "/api/models/details?provider=openai&limit=100")

	for _, key := range []string{"overridden_pricing", "applied_override_id", "pricing_override_ids", "pricing_overrides"} {
		if strings.Contains(body, key) {
			t.Fatalf("expected %q to be omitted when no overrides exist: %s", key, body)
		}
	}
}

func TestListModelDetails_PatchToSameValueIsNotMarkedOverridden(t *testing.T) {
	SetLogger(&mockLogger{})

	h := providerHandlerForTest(t, schemas.OpenAI, []schemas.Key{{ID: "key-a"}}, []string{"gpt-4o"}, []string{"gpt-4o"})
	h.config.ModelCatalog = modelCatalogForPricingJSON(t, []byte(gpt4oPricingJSON))
	if err := h.config.ModelCatalog.SetPricingOverrides([]configstoreTables.TablePricingOverride{{
		ID:               "noop-1",
		ScopeKind:        string(modelcatalog.ScopeKindGlobal),
		MatchType:        string(modelcatalog.MatchTypeExact),
		Pattern:          "gpt-4o",
		RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
		PricingPatchJSON: `{"input_cost_per_token":0.0000025}`,
	}}); err != nil {
		t.Fatalf("seed overrides: %v", err)
	}

	resp, _ := listModelDetailsForTest(t, h, "/api/models/details?provider=openai&limit=100")

	m := resp.Models[0]
	if m.OverriddenPricing != nil {
		t.Fatalf("a patch matching the base value must not render as overridden, got %#v", m.OverriddenPricing)
	}
	if m.AppliedOverrideID != "" {
		t.Fatalf("expected no applied override id, got %q", m.AppliedOverrideID)
	}
	// The override still matched the model, so it stays listed for the sheet.
	if len(m.PricingOverrideIDs) != 1 {
		t.Fatalf("expected the override to remain listed, got %#v", m.PricingOverrideIDs)
	}
}

func TestListModelDetails_OverrideWithoutBaseCatalogRow(t *testing.T) {
	SetLogger(&mockLogger{})

	h := providerHandlerForTest(t,
		schemas.OpenAI,
		[]schemas.Key{{ID: "key-a"}},
		[]string{"custom-model"},
		[]string{"custom-model"},
	)
	h.config.ModelCatalog = modelCatalogForPricingJSON(t, []byte(gpt4oPricingJSON))
	if err := h.config.ModelCatalog.SetPricingOverrides([]configstoreTables.TablePricingOverride{{
		ID:               "custom-1",
		ScopeKind:        string(modelcatalog.ScopeKindGlobal),
		MatchType:        string(modelcatalog.MatchTypeExact),
		Pattern:          "custom-model",
		RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
		PricingPatchJSON: `{"input_cost_per_token":0.000009}`,
	}}); err != nil {
		t.Fatalf("seed overrides: %v", err)
	}

	resp, _ := listModelDetailsForTest(t, h, "/api/models/details?provider=openai&limit=100")

	m := resp.Models[0]
	if m.InputCostPerToken != nil {
		t.Fatalf("expected no base pricing, got %#v", m.InputCostPerToken)
	}
	if m.OverriddenPricing == nil || m.OverriddenPricing.InputCostPerToken == nil ||
		*m.OverriddenPricing.InputCostPerToken != 0.000009 {
		t.Fatalf("expected override-only pricing, got %#v", m.OverriddenPricing)
	}
}

func TestListModelDetails_VirtualKeyScopedOverrideIsInformationalOnly(t *testing.T) {
	SetLogger(&mockLogger{})

	vkID := "vk-1"
	h := providerHandlerForTest(t, schemas.OpenAI, []schemas.Key{{ID: "key-a"}}, []string{"gpt-4o"}, []string{"gpt-4o"})
	h.config.ModelCatalog = modelCatalogForPricingJSON(t, []byte(gpt4oPricingJSON))
	if err := h.config.ModelCatalog.SetPricingOverrides([]configstoreTables.TablePricingOverride{{
		ID:               "vk-scoped",
		ScopeKind:        string(modelcatalog.ScopeKindVirtualKey),
		VirtualKeyID:     &vkID,
		MatchType:        string(modelcatalog.MatchTypeExact),
		Pattern:          "gpt-4o",
		RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
		PricingPatchJSON: `{"input_cost_per_token":0.000001}`,
	}}); err != nil {
		t.Fatalf("seed overrides: %v", err)
	}

	resp, _ := listModelDetailsForTest(t, h, "/api/models/details?provider=openai&limit=100")

	m := resp.Models[0]
	if m.OverriddenPricing != nil || m.AppliedOverrideID != "" {
		t.Fatalf("virtual-key scoped overrides must not change the displayed price, got %#v", m.OverriddenPricing)
	}
	if len(m.PricingOverrideIDs) != 1 || m.PricingOverrideIDs[0] != "vk-scoped" {
		t.Fatalf("expected the override listed informationally, got %#v", m.PricingOverrideIDs)
	}
}

// Provider scope is the only non-global scope that changes the displayed price,
// so it is the only case that pins the provider argument threaded into
// GetCatalogPricingOverrides. The global-scope tests above would pass even if
// that argument were wrong. The mismatched anthropic override must not surface
// at all — not even informationally.
func TestListModelDetails_AppliesProviderScopedOverride(t *testing.T) {
	SetLogger(&mockLogger{})

	openaiID := "openai"
	anthropicID := "anthropic"
	h := providerHandlerForTest(t, schemas.OpenAI, []schemas.Key{{ID: "key-a"}}, []string{"gpt-4o"}, []string{"gpt-4o"})
	h.config.ModelCatalog = modelCatalogForPricingJSON(t, []byte(gpt4oPricingJSON))
	if err := h.config.ModelCatalog.SetPricingOverrides([]configstoreTables.TablePricingOverride{
		{
			ID:               "provider-openai",
			Name:             "OpenAI rate",
			ScopeKind:        string(modelcatalog.ScopeKindProvider),
			ProviderID:       &openaiID,
			MatchType:        string(modelcatalog.MatchTypeExact),
			Pattern:          "gpt-4o",
			RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
			PricingPatchJSON: `{"input_cost_per_token":0.000002}`,
		},
		{
			ID:               "provider-anthropic",
			Name:             "Anthropic rate",
			ScopeKind:        string(modelcatalog.ScopeKindProvider),
			ProviderID:       &anthropicID,
			MatchType:        string(modelcatalog.MatchTypeExact),
			Pattern:          "gpt-4o",
			RequestTypes:     []schemas.RequestType{schemas.ChatCompletionRequest},
			PricingPatchJSON: `{"input_cost_per_token":0.000009}`,
		},
	}); err != nil {
		t.Fatalf("seed overrides: %v", err)
	}

	resp, _ := listModelDetailsForTest(t, h, "/api/models/details?provider=openai&limit=100")

	if len(resp.Models) != 1 {
		t.Fatalf("expected one model, got %#v", resp.Models)
	}
	m := resp.Models[0]
	if m.InputCostPerToken == nil || *m.InputCostPerToken != 0.0000025 {
		t.Fatalf("base input cost must stay unchanged, got %#v", m.InputCostPerToken)
	}
	if m.OverriddenPricing == nil || m.OverriddenPricing.InputCostPerToken == nil ||
		*m.OverriddenPricing.InputCostPerToken != 0.000002 {
		t.Fatalf("expected provider-scoped override to set input cost 0.000002, got %#v", m.OverriddenPricing)
	}
	if m.AppliedOverrideID != "provider-openai" {
		t.Fatalf("expected applied override provider-openai, got %q", m.AppliedOverrideID)
	}
	if len(m.PricingOverrideIDs) != 1 || m.PricingOverrideIDs[0] != "provider-openai" {
		t.Fatalf("anthropic-scoped override must not surface for an openai model, got %#v", m.PricingOverrideIDs)
	}
	if _, ok := resp.PricingOverrides["provider-anthropic"]; ok {
		t.Fatalf("override index must not carry the mismatched provider: %#v", resp.PricingOverrides)
	}
	summary, ok := resp.PricingOverrides["provider-openai"]
	if !ok {
		t.Fatalf("override index missing provider-openai: %#v", resp.PricingOverrides)
	}
	if summary.Name != "OpenAI rate" {
		t.Fatalf("unexpected override summary %#v", summary)
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
			ctx := newBareRequestCtx()
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

// accessForProviderPermits builds the access a key-authenticated caller carries, so these tests
// express what the caller may reach rather than a copy of the key's rows.
func accessForProviderPermits(permits ...schemas.ProviderPermit) schemas.Access {
	permit := grant.NewPermit(grant.PermitVirtualKey, "vk-test", "Test VK", true, false, permits, nil)
	return grant.NewAccess([]schemas.Permit{permit}, "", nil, "", nil)
}

// accessAllowingAllProviders builds the access a caller permitted every provider carries, the way
// the governance store builds it: the providers its configs do not name are materialised onto the
// permit, so the permit carries its whole grant and every consumer reads one list.
func accessAllowingAllProviders(configured []string, permits ...schemas.ProviderPermit) schemas.Access {
	permit := grant.NewPermit(grant.PermitVirtualKey, "vk-test", "Test VK", true, false,
		governanceplugin.AppendAllProviderPermits(permits, configured), nil,
		grant.WithAllowAllProviders(true))
	return grant.NewAccess([]schemas.Permit{permit}, "", nil, "", nil)
}

// A caller permitted every provider is listed every provider, including ones it holds no provider
// permit for. Narrowing to the permits it happens to hold would make the listing refuse what the
// request path admits.
func TestListModels_VKFilterListsProviderAllowedOnlyByAllowAll(t *testing.T) {
	SetLogger(&mockLogger{})

	h := &ProviderHandler{
		config: libtest.WithProviders(t, &lib.Config{
			ClientConfig: &configstore.ClientConfig{},
		}, map[schemas.ModelProvider]configstore.ProviderConfig{
			schemas.OpenAI:    {},
			schemas.Anthropic: {},
		}, map[schemas.ModelProvider][]schemas.Key{
			schemas.OpenAI:    {{ID: "key-a"}},
			schemas.Anthropic: {{ID: "key-b"}},
		}),
		modelsManager: &mockModelsManager{
			filtered: map[schemas.ModelProvider][]string{
				schemas.OpenAI:    {"gpt-4o"},
				schemas.Anthropic: {"claude-haiku-4-5"},
			},
		},
	}

	query := modelListQuery{
		Limit:       100,
		HasVKFilter: true,
		Access: accessAllowingAllProviders(
			[]string{string(schemas.OpenAI), string(schemas.Anthropic)},
			schemas.ProviderPermit{Provider: "openai", AllowedModels: schemas.WhiteList{"*"}},
		),
	}
	if !query.Access.IsProviderAllowed(string(schemas.Anthropic)) {
		t.Fatal("control failed: allow-all must permit a provider it holds no permit for")
	}

	models, total, err := h.listManagementModels(context.Background(), query)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	names := map[string]bool{}
	for _, m := range models {
		names[m.Name] = true
	}
	if total != 2 || !names["gpt-4o"] || !names["claude-haiku-4-5"] {
		t.Fatalf("expected both providers listed, got total=%d models=%#v", total, models)
	}
}

// A blacklisted model is not listed. The listing answers the same question a request does, so a
// model the caller would be refused is not advertised to them as available.
func TestListModels_VKFilterHidesBlacklistedModel(t *testing.T) {
	SetLogger(&mockLogger{})

	h := &ProviderHandler{
		config: libtest.WithProviders(t, &lib.Config{
			ClientConfig: &configstore.ClientConfig{},
		}, map[schemas.ModelProvider]configstore.ProviderConfig{
			schemas.OpenAI: {},
		}, map[schemas.ModelProvider][]schemas.Key{
			schemas.OpenAI: {{ID: "key-a"}},
		}),
		modelsManager: &mockModelsManager{
			filtered: map[schemas.ModelProvider][]string{
				schemas.OpenAI: {"gpt-4o", "gpt-4o-mini"},
			},
		},
	}

	query := modelListQuery{
		Limit:       100,
		HasVKFilter: true,
		Access: accessForProviderPermits(schemas.ProviderPermit{
			Provider:          "openai",
			AllowedModels:     schemas.WhiteList{"*"},
			BlacklistedModels: []string{"gpt-4o-mini"},
		}),
	}

	models, total, err := h.listManagementModels(context.Background(), query)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 1 || len(models) != 1 || models[0].Name != "gpt-4o" {
		t.Fatalf("expected only gpt-4o, got total=%d models=%#v", total, models)
	}
}

// Duplicate configs for one provider grant the union of what they allow, as they do on the
// request path: the listing must not stop at the first config it finds.
func TestListModels_VKFilterUnionsDuplicateProviderConfigs(t *testing.T) {
	SetLogger(&mockLogger{})

	h := &ProviderHandler{
		config: libtest.WithProviders(t, &lib.Config{
			ClientConfig: &configstore.ClientConfig{},
		}, map[schemas.ModelProvider]configstore.ProviderConfig{
			schemas.OpenAI: {},
		}, map[schemas.ModelProvider][]schemas.Key{
			schemas.OpenAI: {{ID: "key-a"}},
		}),
		modelsManager: &mockModelsManager{
			filtered: map[schemas.ModelProvider][]string{
				schemas.OpenAI: {"gpt-4o", "gpt-4o-mini", "gpt-3.5-turbo"},
			},
		},
	}

	query := modelListQuery{
		Limit:       100,
		HasVKFilter: true,
		Access: accessForProviderPermits(
			schemas.ProviderPermit{Provider: "openai", AllowedModels: []string{"gpt-4o"}},
			schemas.ProviderPermit{Provider: "openai", AllowedModels: []string{"gpt-4o-mini"}},
		),
	}

	models, total, err := h.listManagementModels(context.Background(), query)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	names := map[string]bool{}
	for _, m := range models {
		names[m.Name] = true
	}
	if total != 2 || !names["gpt-4o"] || !names["gpt-4o-mini"] || names["gpt-3.5-turbo"] {
		t.Fatalf("expected gpt-4o and gpt-4o-mini only, got total=%d models=%#v", total, models)
	}
}

// With a key presented, the listing returns only the providers the caller is granted, and only
// the models those grants allow.
func TestListModels_VKFilterRestrictsToAllowedProviderAndModels(t *testing.T) {
	SetLogger(&mockLogger{})

	// Two providers configured; VK only allows openai with specific models.
	h := &ProviderHandler{
		config: libtest.WithProviders(t, &lib.Config{
			ClientConfig: &configstore.ClientConfig{},
		}, map[schemas.ModelProvider]configstore.ProviderConfig{
			schemas.OpenAI:    {},
			schemas.Anthropic: {},
		}, map[schemas.ModelProvider][]schemas.Key{
			schemas.OpenAI:    {{ID: "key-a"}},
			schemas.Anthropic: {{ID: "key-b"}},
		}),
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
		Access:      accessForProviderPermits(schemas.ProviderPermit{Provider: "openai", AllowedModels: []string{"gpt-4o", "gpt-4o-mini"}}),
	}

	models, total, err := h.listManagementModels(context.Background(), query)
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
		config: libtest.WithProviders(t, &lib.Config{
			ClientConfig: &configstore.ClientConfig{},
		}, map[schemas.ModelProvider]configstore.ProviderConfig{
			schemas.OpenAI: {},
		}, map[schemas.ModelProvider][]schemas.Key{
			schemas.OpenAI: {{ID: "key-a"}},
		}),
		modelsManager: &mockModelsManager{
			filtered: map[schemas.ModelProvider][]string{
				schemas.OpenAI: {"gpt-4o", "gpt-4o-mini", "gpt-3.5-turbo"},
			},
		},
	}

	query := modelListQuery{
		Limit:       100,
		HasVKFilter: true,
		Access:      accessForProviderPermits(schemas.ProviderPermit{Provider: "openai", AllowedModels: []string{"*"}}),
	}

	models, total, err := h.listManagementModels(context.Background(), query)
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
		config: libtest.WithProviders(t, &lib.Config{
			ClientConfig: &configstore.ClientConfig{},
		}, map[schemas.ModelProvider]configstore.ProviderConfig{
			schemas.OpenAI: {},
		}, map[schemas.ModelProvider][]schemas.Key{
			schemas.OpenAI: {{ID: "key-a"}},
		}),
		modelsManager: &mockModelsManager{
			filtered: map[schemas.ModelProvider][]string{
				schemas.OpenAI: {"gpt-4o", "gpt-4o-mini"},
			},
		},
	}

	query := modelListQuery{
		Limit:       100,
		HasVKFilter: true,
		Access:      accessForProviderPermits(schemas.ProviderPermit{Provider: "openai", AllowedModels: []string{}}),
	}

	models, total, err := h.listManagementModels(context.Background(), query)
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
		config: libtest.WithProviders(t, &lib.Config{
			ClientConfig: &configstore.ClientConfig{},
		}, map[schemas.ModelProvider]configstore.ProviderConfig{
			schemas.OpenAI:    {},
			schemas.Anthropic: {},
		}, nil),
		modelsManager: &mockModelsManager{
			filtered: map[schemas.ModelProvider][]string{
				schemas.OpenAI:    {"gpt-4o"},
				schemas.Anthropic: {"claude-3-5-sonnet"},
			},
		},
	}

	query := modelListQuery{
		Limit:       100,
		HasVKFilter: true,
		Access:      accessForProviderPermits(), // a caller granted no provider
	}

	models, total, err := h.listManagementModels(context.Background(), query)
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
		config: libtest.WithProviders(t, &lib.Config{
			ClientConfig: &configstore.ClientConfig{},
		}, map[schemas.ModelProvider]configstore.ProviderConfig{
			schemas.OpenAI:    {},
			schemas.Anthropic: {},
		}, nil),
		modelsManager: &mockModelsManager{
			filtered: map[schemas.ModelProvider][]string{
				schemas.OpenAI:    {"gpt-4o"},
				schemas.Anthropic: {"claude-3-5-sonnet"},
			},
		},
	}

	ctx := newBareRequestCtx()
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/models?provider=anthropic")
	query, ok := h.parseModelListQuery(ctx, requestContextForTest(ctx), 5)
	if !ok {
		t.Fatalf("expected parseModelListQuery to succeed")
	}
	query.HasVKFilter = true
	query.Access = accessForProviderPermits(schemas.ProviderPermit{Provider: "openai", AllowedModels: []string{"*"}})

	models, total, err := h.listManagementModels(context.Background(), query)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 0 || len(models) != 0 {
		t.Fatalf("expected blocked explicit provider to return no models, got total=%d models=%#v", total, models)
	}
}

// requestContextForTest derives the request context a management route would hand to the query
// parser, so the parse sees the presented key the same way it does in production.
func requestContextForTest(ctx *fasthttp.RequestCtx) *schemas.BifrostContext {
	bifrostCtx, _ := lib.ConvertToBifrostContext(ctx, &lib.Config{ClientConfig: &configstore.ClientConfig{}})
	return bifrostCtx
}

// modelListQueryHandlerForTest builds a handler that answers a management model listing from
// the given models manager.
func modelListQueryHandlerForTest(t testing.TB, manager *mockModelsManager) *ProviderHandler {
	return &ProviderHandler{
		config: libtest.FileOnly(t, &lib.Config{
			ClientConfig: &configstore.ClientConfig{},
		}, map[schemas.ModelProvider]configstore.ProviderConfig{
			schemas.OpenAI: {},
		}, nil),
		modelsManager: manager,
	}
}

// A key-authenticated management listing is filtered to what that request may reach, resolved
// from the request itself rather than from the key's stored rows.
func TestParseModelListQuery_VKAppliesResolvedAccess(t *testing.T) {
	SetLogger(&mockLogger{})

	access := accessForProviderPermits(schemas.ProviderPermit{Provider: "openai", AllowedModels: []string{"*"}})
	manager := &mockModelsManager{access: access}
	h := modelListQueryHandlerForTest(t, manager)

	ctx := newBareRequestCtx()
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/models")
	ctx.Request.Header.Set("x-bf-vk", "sk-bf-test-virtual-key")

	query, ok := h.parseModelListQuery(ctx, requestContextForTest(ctx), 5)
	if !ok {
		t.Fatalf("expected parseModelListQuery to succeed")
	}
	if manager.resolveCalls != 1 {
		t.Fatalf("resolve calls = %d, want 1", manager.resolveCalls)
	}
	if !query.HasVKFilter {
		t.Fatal("expected the listing to be filtered")
	}
	if query.Access != access {
		t.Fatalf("expected the resolved access to be carried on the query, got %#v", query.Access)
	}
}

// What decides the filter is the answer, not the headers: the parse always asks, and filters
// only when something was resolved. A request with no key and one whose key resolved to nothing
// (unknown key, or a deployment without governance) are the same case, and the listing stays
// unfiltered instead of narrowing to nothing or failing.
func TestParseModelListQuery_LeavesListingUnfilteredWhenNothingResolved(t *testing.T) {
	SetLogger(&mockLogger{})

	for _, tc := range []struct {
		name    string
		vkValue string
	}{
		{name: "no key presented"},
		{name: "key resolves to nothing", vkValue: "sk-bf-test-virtual-key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := &mockModelsManager{}
			h := modelListQueryHandlerForTest(t, manager)

			ctx := newBareRequestCtx()
			ctx.Request.Header.SetMethod("GET")
			ctx.Request.SetRequestURI("/api/models")
			if tc.vkValue != "" {
				ctx.Request.Header.Set("x-bf-vk", tc.vkValue)
			}

			query, ok := h.parseModelListQuery(ctx, requestContextForTest(ctx), 5)
			if !ok {
				t.Fatalf("expected parseModelListQuery to succeed")
			}
			if manager.resolveCalls != 1 {
				t.Fatalf("resolve calls = %d, want 1", manager.resolveCalls)
			}
			if query.HasVKFilter || query.Access != nil {
				t.Fatalf("expected an unfiltered listing, got HasVKFilter=%v access=%#v", query.HasVKFilter, query.Access)
			}
		})
	}
}

// An unfiltered listing is what the dashboard pickers that configure the gateway ask for (a key's
// allowed models, deployments), so it is the provider's whole catalog whoever asks, not the part
// the caller's access reaches. A listing that is not unfiltered is still narrowed to that access.
// Both listing routes parse the same query, so every case runs against each of them.
func TestListModels_UnfilteredListingSkipsCallerAccess(t *testing.T) {
	SetLogger(&mockLogger{})

	sessionAccess := func(permits ...schemas.ProviderPermit) schemas.Access {
		permit := grant.NewPermit(grant.PermitAccessProfile, "profile-test", "Test Profile", true, false, permits, nil)
		return grant.NewAccess([]schemas.Permit{permit}, "", nil, "", nil)
	}
	signedIn := func(ctx *fasthttp.RequestCtx) {
		ctx.SetUserValue(schemas.BifrostContextKeyAuthCredential, grant.NewCredential(grant.CredentialSessionToken, "session-token"))
	}
	withVirtualKey := func(ctx *fasthttp.RequestCtx) {
		ctx.Request.Header.Set("x-bf-vk", "sk-bf-test-virtual-key")
	}

	for _, tc := range []struct {
		name   string
		access schemas.Access
		caller func(*fasthttp.RequestCtx)
		query  string
		want   []string
	}{
		{
			name:   "signed-in caller, unfiltered listing",
			access: sessionAccess(schemas.ProviderPermit{Provider: "openai", AllowedModels: []string{"gpt-4o"}}),
			caller: signedIn,
			query:  "provider=openai&unfiltered=true&limit=10",
			want:   []string{"gpt-4.1", "gpt-4o", "gpt-4o-mini"},
		},
		{
			name:   "signed-in caller not granted the provider, unfiltered listing",
			access: sessionAccess(schemas.ProviderPermit{Provider: "anthropic", AllowedModels: []string{"*"}}),
			caller: signedIn,
			query:  "provider=openai&unfiltered=true&limit=10",
			want:   []string{"gpt-4.1", "gpt-4o", "gpt-4o-mini"},
		},
		{
			name:   "signed-in caller, filtered listing",
			access: sessionAccess(schemas.ProviderPermit{Provider: "openai", AllowedModels: []string{"gpt-4o"}}),
			caller: signedIn,
			query:  "provider=openai&limit=10",
			want:   []string{"gpt-4o"},
		},
		{
			name:   "virtual key, unfiltered listing",
			access: accessForProviderPermits(schemas.ProviderPermit{Provider: "openai", AllowedModels: []string{"gpt-4o"}}),
			caller: withVirtualKey,
			query:  "provider=openai&unfiltered=true&limit=10",
			want:   []string{"gpt-4.1", "gpt-4o", "gpt-4o-mini"},
		},
		{
			name:   "virtual key, filtered listing",
			access: accessForProviderPermits(schemas.ProviderPermit{Provider: "openai", AllowedModels: []string{"gpt-4o"}}),
			caller: withVirtualKey,
			query:  "provider=openai&limit=10",
			want:   []string{"gpt-4o"},
		},
	} {
		for _, route := range []struct {
			name   string
			path   string
			handle func(*ProviderHandler, *fasthttp.RequestCtx)
		}{
			{name: "models", path: "/api/models", handle: (*ProviderHandler).listModels},
			{name: "model details", path: "/api/models/details", handle: (*ProviderHandler).listModelDetails},
		} {
			t.Run(route.name+", "+tc.name, func(t *testing.T) {
				h := providerHandlerForTest(t,
					schemas.OpenAI,
					[]schemas.Key{{ID: "key-a", Models: []string{"gpt-4o", "gpt-4o-mini"}}},
					[]string{"gpt-4o", "gpt-4o-mini"},
					[]string{"gpt-4.1", "gpt-4o", "gpt-4o-mini"},
				)
				h.config.ModelCatalog = catalogForTest(t, nil)
				h.modelsManager.(*mockModelsManager).access = tc.access

				ctx := newBareRequestCtx()
				ctx.Request.Header.SetMethod("GET")
				ctx.Request.SetRequestURI(route.path + "?" + tc.query)
				tc.caller(ctx)

				route.handle(h, ctx)

				if ctx.Response.StatusCode() != fasthttp.StatusOK {
					t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
				}
				var resp struct {
					Models []struct {
						Name string `json:"name"`
					} `json:"models"`
				}
				if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
					t.Fatalf("failed to unmarshal response: %v", err)
				}
				got := make([]string, 0, len(resp.Models))
				for _, model := range resp.Models {
					got = append(got, model.Name)
				}
				if !slices.Equal(got, tc.want) {
					t.Fatalf("models = %v, want %v", got, tc.want)
				}
			})
		}
	}
}

// TestListModels_NoVKFilterReturnsAll verifies that without a VK filter the endpoint
// returns all providers and models as normal.
func TestListModels_NoVKFilterReturnsAll(t *testing.T) {
	SetLogger(&mockLogger{})

	h := &ProviderHandler{
		config: libtest.WithProviders(t, &lib.Config{
			ClientConfig: &configstore.ClientConfig{},
		}, map[schemas.ModelProvider]configstore.ProviderConfig{
			schemas.OpenAI:    {},
			schemas.Anthropic: {},
		}, nil),
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

	models, total, err := h.listManagementModels(context.Background(), query)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if total != 2 || len(models) != 2 {
		t.Fatalf("expected 2 models (one per provider), got total=%d", total)
	}
}

func TestListModels_UsesCatalogAwareAliasMatchingForKeyAllowlist(t *testing.T) {
	SetLogger(&mockLogger{})

	h := providerHandlerForTest(t,
		schemas.OpenAI,
		[]schemas.Key{
			{ID: "key-a", Models: []string{"gpt-4o-2024-08-06"}},
		},
		[]string{"gpt-4o"},
		[]string{"gpt-4o"},
	)
	h.config.ModelCatalog = catalogForTest(t, map[string]string{
		"gpt-4o-2024-08-06": "gpt-4o",
	})

	ctx := newBareRequestCtx()
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

	h := providerHandlerForTest(t,
		schemas.OpenAI,
		[]schemas.Key{
			{ID: "key-a", Models: []string{"GPT-4O", "GPT-4O-MINI"}},
		},
		[]string{"gpt-4o", "gpt-4o-mini", "gpt-3.5-turbo"},
		[]string{"gpt-4o", "gpt-4o-mini", "gpt-3.5-turbo"},
	)

	ctx := newBareRequestCtx()
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

	h := providerHandlerForTest(t,
		schemas.OpenAI,
		[]schemas.Key{
			{ID: "key-a", BlacklistedModels: []string{"GPT-3.5-TURBO"}},
		},
		[]string{"gpt-4o", "gpt-4o-mini", "gpt-3.5-turbo"},
		[]string{"gpt-4o", "gpt-4o-mini", "gpt-3.5-turbo"},
	)

	ctx := newBareRequestCtx()
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

func TestListModels_UnsearchedListingOmitsDeprecatedModels(t *testing.T) {
	SetLogger(&mockLogger{})

	models := []string{"old-a", "old-b", "current-a", "current-b"}
	h := providerHandlerForTest(t, schemas.OpenAI, []schemas.Key{{ID: "key-a"}}, models, models)
	h.config.ModelCatalog = modelCatalogForPricingJSON(t, []byte(`{
		"old-a": {"provider":"openai","mode":"chat","base_model":"old-a","is_deprecated":true},
		"old-b": {"provider":"openai","mode":"chat","base_model":"old-b","is_deprecated":true},
		"current-a": {"provider":"openai","mode":"chat","base_model":"current-a"},
		"current-b": {"provider":"openai","mode":"chat","base_model":"current-b"}
	}`))

	resp := listModelsForTest(t, h, "/api/models?provider=openai&limit=10")

	if resp.Total != 2 {
		t.Fatalf("expected total=2 (deprecated models dropped), got %d", resp.Total)
	}
	for _, model := range resp.Models {
		if model.IsDeprecated {
			t.Fatalf("unsearched listing should hold no deprecated models, got %#v", resp.Models)
		}
	}
}

func TestListModels_SearchIncludesDeprecatedModelsBelowLiveOnes(t *testing.T) {
	SetLogger(&mockLogger{})

	// The deprecated models sort first by name, so an unordered listing would lead with them.
	models := []string{"gpt-old-a", "gpt-old-b", "gpt-zed"}
	h := providerHandlerForTest(t, schemas.OpenAI, []schemas.Key{{ID: "key-a"}}, models, models)
	h.config.ModelCatalog = modelCatalogForPricingJSON(t, []byte(`{
		"gpt-old-a": {"provider":"openai","mode":"chat","base_model":"gpt-old-a","is_deprecated":true},
		"gpt-old-b": {"provider":"openai","mode":"chat","base_model":"gpt-old-b","is_deprecated":true},
		"gpt-zed": {"provider":"openai","mode":"chat","base_model":"gpt-zed"}
	}`))

	resp := listModelsForTest(t, h, "/api/models?provider=openai&query=gpt&limit=10")

	if resp.Total != 3 {
		t.Fatalf("expected total=3 (a search keeps deprecated models), got %d", resp.Total)
	}
	if len(resp.Models) != 3 {
		t.Fatalf("expected 3 models, got %#v", resp.Models)
	}
	if resp.Models[0].Name != "gpt-zed" {
		t.Fatalf("expected the live model first, got %#v", resp.Models)
	}
	if !resp.Models[1].IsDeprecated || !resp.Models[2].IsDeprecated {
		t.Fatalf("expected the deprecated models to sink to the end, got %#v", resp.Models)
	}
}

func TestListModels_IncludeDeprecatedOptsOutOfHiding(t *testing.T) {
	SetLogger(&mockLogger{})

	models := []string{"old-a", "current-a"}
	h := providerHandlerForTest(t, schemas.OpenAI, []schemas.Key{{ID: "key-a"}}, models, models)
	h.config.ModelCatalog = modelCatalogForPricingJSON(t, []byte(`{
		"old-a": {"provider":"openai","mode":"chat","base_model":"old-a","is_deprecated":true},
		"current-a": {"provider":"openai","mode":"chat","base_model":"current-a"}
	}`))

	resp := listModelsForTest(t, h, "/api/models?provider=openai&limit=10&include_deprecated=true")

	if resp.Total != 2 {
		t.Fatalf("expected total=2 with include_deprecated=true, got %d", resp.Total)
	}
	if resp.Models[0].Name != "current-a" || !resp.Models[1].IsDeprecated {
		t.Fatalf("expected the deprecated model kept but sunk, got %#v", resp.Models)
	}
}

func TestListModelDetails_KeepsDeprecatedModelsWhenUnsearched(t *testing.T) {
	SetLogger(&mockLogger{})

	models := []string{"old-a", "current-a"}
	h := providerHandlerForTest(t, schemas.OpenAI, []schemas.Key{{ID: "key-a"}}, models, models)
	h.config.ModelCatalog = modelCatalogForPricingJSON(t, []byte(`{
		"old-a": {"provider":"openai","mode":"chat","base_model":"old-a","is_deprecated":true},
		"current-a": {"provider":"openai","mode":"chat","base_model":"current-a"}
	}`))

	ctx := newBareRequestCtx()
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/api/models/details?provider=openai&limit=10")

	h.listModelDetails(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}

	var resp ListModelDetailsResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	// The model catalog is an inventory, not a picker: it lists what exists, deprecated included.
	if resp.Total != 2 {
		t.Fatalf("expected total=2, got %d", resp.Total)
	}
}

func listModelsForTest(t *testing.T, h *ProviderHandler, uri string) ListModelsResponse {
	t.Helper()

	ctx := newBareRequestCtx()
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI(uri)

	h.listModels(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}

	var resp ListModelsResponse
	if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}
	return resp
}

func TestProviderBaseURLShape(t *testing.T) {
	for _, raw := range []string{"https://user:pass@127.0.0.1/base", "https://127.0.0.1/base?", "https://127.0.0.1/base#", "https://127.0.0.1/base?q=1"} {
		if validateProviderBaseURLShape(raw) == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	for _, raw := range []string{"", "http://127.0.0.1:1234/v1", "https://example.com/nested/path", "https://example.com/a%3Fb%23c"} {
		if err := validateProviderBaseURLShape(raw); err != nil {
			t.Errorf("rejected %q: %v", raw, err)
		}
	}
}

// datasheetHandlerForTest boots a real catalog on sqlite with a local
// datasheet, wired into a ProviderHandler the way the server wires it.
func datasheetHandlerForTest(t *testing.T) *ProviderHandler {
	t.Helper()
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})
	ctx := context.Background()
	dir := t.TempDir()
	pricingPath := filepath.Join(dir, "pricing.json")
	paramsPath := filepath.Join(dir, "params.json")
	if err := os.WriteFile(pricingPath, []byte(`{"gpt-4o":{"provider":"openai","mode":"chat","context_length":128000,"input_cost_per_token":0.0000025,"output_cost_per_token":0.00001}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paramsPath, []byte(`{"gpt-4o":{"provider":"openai","mode":"chat","max_output_tokens":16384}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cs, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: filepath.Join(dir, "config.db")},
	}, bifrost.NewNoOpLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close(ctx) })
	pricingURL, paramsURL := "file://"+pricingPath, "file://"+paramsPath
	disabled := int64(0)
	live, err := livestore.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(live.Close)
	if err := live.LoadAll(ctx, cs.DB()); err != nil {
		t.Fatal(err)
	}
	mc, err := modelcatalog.Init(ctx, &modelcatalog.Config{PricingURL: &pricingURL, ModelParametersURL: &paramsURL, MCPLibrarySyncInterval: &disabled}, cs, live, bifrost.NewNoOpLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mc.Cleanup() })
	return &ProviderHandler{config: &lib.Config{ModelCatalog: mc}, configStore: cs}
}

// datasheetRequest builds a RequestCtx through Init so it carries a server and
// works as a context.Context for the real config store underneath.
func datasheetRequest(method, uri, body string) *fasthttp.RequestCtx {
	var req fasthttp.Request
	req.Header.SetMethod(method)
	req.SetRequestURI(uri)
	if body != "" {
		req.SetBodyString(body)
	}
	ctx := newBareRequestCtx()
	ctx.Init(&req, nil, nil)
	return ctx
}

// Save, list, read and reset an override through the HTTP handlers.
func TestDatasheetOverrideHandlers_SaveListResetDelete(t *testing.T) {
	h := datasheetHandlerForTest(t)

	save := datasheetRequest("PUT", "/api/models/datasheet/overrides",
		`{"provider":"openai","model":"gpt-4o","mode":"chat","pricing_patch":{"context_length":64000},"params_patch":{"max_output_tokens":4096}}`)
	h.saveDatasheetOverride(save)
	if save.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("save: %d %s", save.Response.StatusCode(), save.Response.Body())
	}
	var saved struct {
		Override configstoreTables.TableDatasheetOverride `json:"override"`
	}
	if err := json.Unmarshal(save.Response.Body(), &saved); err != nil || saved.Override.ID == "" {
		t.Fatalf("save response: %v %s", err, save.Response.Body())
	}

	list := datasheetRequest("GET", "/api/models/datasheet?query=gpt-4o&overridden_only=true", "")
	h.listDatasheetEntries(list)
	var listed ListDatasheetEntriesResponse
	if err := json.Unmarshal(list.Response.Body(), &listed); err != nil {
		t.Fatalf("list: %v %s", err, list.Response.Body())
	}
	if listed.TotalCount != 1 || *listed.Entries[0].Effective.ContextLength != 64000 || *listed.Entries[0].Upstream.ContextLength != 128000 {
		t.Fatalf("list returned %s", list.Response.Body())
	}

	entry := datasheetRequest("GET", "/api/models/datasheet/entry?provider=openai&model=gpt-4o&mode=chat", "")
	h.getDatasheetEntry(entry)
	var detail modelcatalog.DatasheetEntryDetail
	if err := json.Unmarshal(entry.Response.Body(), &detail); err != nil {
		t.Fatalf("entry: %v %s", err, entry.Response.Body())
	}
	if !strings.Contains(string(detail.EffectiveParams), `"max_output_tokens":4096`) || !strings.Contains(string(detail.UpstreamParams), `"max_output_tokens":16384`) {
		t.Fatalf("entry params: %s", entry.Response.Body())
	}

	// A misspelled patch field is rejected, not read as "no patches" (a reset).
	typo := datasheetRequest("PUT", "/api/models/datasheet/overrides", `{"provider":"openai","model":"gpt-4o","mode":"chat","pricing_pach":{"context_length":1}}`)
	h.saveDatasheetOverride(typo)
	if typo.Response.StatusCode() != fasthttp.StatusBadRequest {
		t.Fatalf("unknown field: %d, want 400: %s", typo.Response.StatusCode(), typo.Response.Body())
	}
	if got := *h.config.ModelCatalog.GetModelCapabilityEntryForModel("gpt-4o", schemas.OpenAI).ContextLength; got != 64000 {
		t.Fatalf("context_length after rejected save = %d, want the override's 64000", got)
	}

	// Saving with no patches resets the target to upstream; an empty object
	// with whitespace inside is still empty.
	reset := datasheetRequest("PUT", "/api/models/datasheet/overrides", "{\"provider\":\"openai\",\"model\":\"gpt-4o\",\"mode\":\"chat\",\"pricing_patch\":{ \n }}")
	h.saveDatasheetOverride(reset)
	if reset.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("reset: %d %s", reset.Response.StatusCode(), reset.Response.Body())
	}
	if got := *h.config.ModelCatalog.GetModelCapabilityEntryForModel("gpt-4o", schemas.OpenAI).ContextLength; got != 128000 {
		t.Fatalf("context_length after reset = %d, want upstream 128000", got)
	}

	missing := datasheetRequest("DELETE", "/api/models/datasheet/overrides/"+saved.Override.ID, "")
	missing.SetUserValue("id", saved.Override.ID)
	h.deleteDatasheetOverride(missing)
	if missing.Response.StatusCode() != fasthttp.StatusNotFound {
		t.Fatalf("delete of a reset override: %d, want 404", missing.Response.StatusCode())
	}

	// A live override deleted by ID: 200 and the upstream value is restored.
	again := datasheetRequest("PUT", "/api/models/datasheet/overrides", `{"provider":"openai","model":"gpt-4o","mode":"chat","pricing_patch":{"context_length":32000}}`)
	h.saveDatasheetOverride(again)
	if again.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("save before delete: %d %s", again.Response.StatusCode(), again.Response.Body())
	}
	var live struct {
		Override configstoreTables.TableDatasheetOverride `json:"override"`
	}
	if err := json.Unmarshal(again.Response.Body(), &live); err != nil {
		t.Fatal(err)
	}
	if got := *h.config.ModelCatalog.GetModelCapabilityEntryForModel("gpt-4o", schemas.OpenAI).ContextLength; got != 32000 {
		t.Fatalf("context_length before delete = %d, want 32000", got)
	}
	del := datasheetRequest("DELETE", "/api/models/datasheet/overrides/"+live.Override.ID, "")
	del.SetUserValue("id", live.Override.ID)
	h.deleteDatasheetOverride(del)
	if del.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("delete: %d %s", del.Response.StatusCode(), del.Response.Body())
	}
	if got := *h.config.ModelCatalog.GetModelCapabilityEntryForModel("gpt-4o", schemas.OpenAI).ContextLength; got != 128000 {
		t.Fatalf("context_length after delete = %d, want upstream 128000", got)
	}
	repeat := datasheetRequest("DELETE", "/api/models/datasheet/overrides/"+live.Override.ID, "")
	repeat.SetUserValue("id", live.Override.ID)
	h.deleteDatasheetOverride(repeat)
	if repeat.Response.StatusCode() != fasthttp.StatusNotFound {
		t.Fatalf("second delete of the same id: %d, want 404", repeat.Response.StatusCode())
	}

	// Without a model catalog or a config store, delete is unavailable.
	for name, unavailable := range map[string]*ProviderHandler{
		"no catalog": {config: &lib.Config{}, configStore: h.configStore},
		"no store":   {config: h.config},
	} {
		req := datasheetRequest("DELETE", "/api/models/datasheet/overrides/x", "")
		req.SetUserValue("id", "x")
		unavailable.deleteDatasheetOverride(req)
		if req.Response.StatusCode() != fasthttp.StatusServiceUnavailable {
			t.Fatalf("%s: delete %d, want 503", name, req.Response.StatusCode())
		}
	}
}

// A pricing patch for a row another override already prices (bedrock_mantle
// shares bedrock's pricing rows) is a conflict the client can act on, not a
// server error.
func TestDatasheetOverrideHandlers_PricingClaimConflictIs409(t *testing.T) {
	h := datasheetHandlerForTest(t)
	first := datasheetRequest("PUT", "/api/models/datasheet/overrides", `{"provider":"bedrock","model":"m","mode":"chat","pricing_patch":{"input_cost_per_token":0.000005}}`)
	h.saveDatasheetOverride(first)
	if first.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("first save: %d: %s", first.Response.StatusCode(), first.Response.Body())
	}
	second := datasheetRequest("PUT", "/api/models/datasheet/overrides", `{"provider":"bedrock_mantle","model":"m","mode":"chat","pricing_patch":{"input_cost_per_token":0.000006}}`)
	h.saveDatasheetOverride(second)
	if second.Response.StatusCode() != fasthttp.StatusConflict {
		t.Fatalf("colliding save: %d, want 409: %s", second.Response.StatusCode(), second.Response.Body())
	}
}

func TestDatasheetOverrideHandlers_RejectInvalidBodies(t *testing.T) {
	h := datasheetHandlerForTest(t)
	for name, body := range map[string]string{
		"unknown field":    `{"provider":"openai","model":"gpt-4o","mode":"chat","pricing_patch":{"nope":1}}`,
		"negative":         `{"provider":"openai","model":"gpt-4o","mode":"chat","pricing_patch":{"input_cost_per_token":-1}}`,
		"custom no mode":   `{"provider":"openai","model":"ft","is_custom":true}`,
		"missing model":    `{"provider":"openai","pricing_patch":{"context_length":1}}`,
		"malformed json":   `{"provider":`,
		"patch not object": `{"provider":"openai","model":"gpt-4o","pricing_patch":[1]}`,
	} {
		t.Run(name, func(t *testing.T) {
			ctx := datasheetRequest("PUT", "/api/models/datasheet/overrides", body)
			h.saveDatasheetOverride(ctx)
			if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
				t.Fatalf("status %d, want 400: %s", ctx.Response.StatusCode(), ctx.Response.Body())
			}
		})
	}

	missing := datasheetRequest("GET", "/api/models/datasheet/entry?provider=openai&model=nope&mode=chat", "")
	h.getDatasheetEntry(missing)
	if missing.Response.StatusCode() != fasthttp.StatusNotFound {
		t.Fatalf("entry for unknown model: %d, want 404", missing.Response.StatusCode())
	}
}

// TestListModels_IncludeAliases lists key aliases only when an unfiltered listing asks for them.
func TestListModels_IncludeAliases(t *testing.T) {
	SetLogger(&mockLogger{})

	for _, tc := range []struct {
		name      string
		query     string
		want      []string
		wantTotal int
	}{
		{"unfiltered alone", "unfiltered=true&limit=10", []string{"gpt-4.1", "gpt-4o", "gpt-4o-mini"}, 3},
		{"unfiltered with aliases", "unfiltered=true&include_aliases=true&limit=10", []string{"fast-chat", "gpt-4.1", "gpt-4o", "gpt-4o-mini", "team-gpt"}, 5},
		{"searching for an alias", "unfiltered=true&include_aliases=true&query=TEAM&limit=10", []string{"team-gpt"}, 1},
		{"first page", "unfiltered=true&include_aliases=true&limit=2", []string{"fast-chat", "gpt-4.1"}, 5},
		{"last page", "unfiltered=true&include_aliases=true&limit=2&offset=4", []string{"team-gpt"}, 5},
		{"without unfiltered the listing is narrowed as before", "include_aliases=true&limit=10", []string{"gpt-4o"}, 1},
		{"false", "unfiltered=true&include_aliases=false&limit=10", []string{"gpt-4.1", "gpt-4o", "gpt-4o-mini"}, 3},
	} {
		for _, route := range []struct {
			name   string
			path   string
			handle func(*ProviderHandler, *fasthttp.RequestCtx)
		}{
			{name: "models", path: "/api/models", handle: (*ProviderHandler).listModels},
			{name: "model details", path: "/api/models/details", handle: (*ProviderHandler).listModelDetails},
		} {
			t.Run(route.name+", "+tc.name, func(t *testing.T) {
				// gpt-4o is an alias name and a listed model, so it must appear once.
				keys := []schemas.Key{{ID: "key-a", Models: []string{"*"}, Aliases: schemas.KeyAliases{
					"team-gpt":        {ModelID: "unlisted-a"},
					"fast-chat":       {ModelID: "unlisted-b"},
					"gpt-4o":          {ModelID: "unlisted-c"},
					"regex:claude-.*": {ModelID: "unlisted-d"},
				}}}
				h := providerHandlerForTest(t, schemas.OpenAI, keys, []string{"gpt-4o", "gpt-4o-mini"}, []string{"gpt-4.1", "gpt-4o", "gpt-4o-mini"})
				catalog, err := modelcatalog.NewTestCatalog(nil)
				require.NoError(t, err)
				h.config.ModelCatalog = catalog
				h.config.ModelCatalog.SetKeyConfigForProvider(schemas.OpenAI, keys)
				permit := grant.NewPermit(grant.PermitAccessProfile, "profile-test", "Test Profile", true, false,
					[]schemas.ProviderPermit{{Provider: "openai", AllowedModels: []string{"gpt-4o"}}}, nil)
				h.modelsManager.(*mockModelsManager).access = grant.NewAccess([]schemas.Permit{permit}, "", nil, "", nil)

				ctx := &fasthttp.RequestCtx{}
				ctx.Request.Header.SetMethod("GET")
				ctx.Request.SetRequestURI(route.path + "?provider=openai&" + tc.query)
				ctx.SetUserValue(schemas.BifrostContextKeyAuthCredential, grant.NewCredential(grant.CredentialSessionToken, "session-token"))

				route.handle(h, ctx)

				if ctx.Response.StatusCode() != fasthttp.StatusOK {
					t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), string(ctx.Response.Body()))
				}
				var resp struct {
					Models []struct {
						Name string `json:"name"`
					} `json:"models"`
					Total int `json:"total"`
				}
				if err := json.Unmarshal(ctx.Response.Body(), &resp); err != nil {
					t.Fatalf("failed to unmarshal response: %v", err)
				}
				got := make([]string, 0, len(resp.Models))
				for _, model := range resp.Models {
					got = append(got, model.Name)
				}
				if !slices.Equal(got, tc.want) || resp.Total != tc.wantTotal {
					t.Fatalf("models = %v (total %d), want %v (total %d)", got, resp.Total, tc.want, tc.wantTotal)
				}
			})
		}
	}
}

// BenchmarkListModels_Unfiltered measures the management listing the dashboard pickers read, with and without aliases.
func BenchmarkListModels_Unfiltered(b *testing.B) {
	SetLogger(&mockLogger{})
	models := make([]string, 300)
	for i := range models {
		models[i] = fmt.Sprintf("model-%03d", i)
	}
	for _, aliasesPerKey := range []int{0, 10} {
		keys := make([]schemas.Key, 4)
		for i := range keys {
			keys[i] = schemas.Key{ID: fmt.Sprintf("key-%d", i), Models: []string{"*"}, Aliases: schemas.KeyAliases{}}
			for j := range aliasesPerKey {
				keys[i].Aliases[fmt.Sprintf("alias-%d-%d", i, j)] = schemas.AliasConfig{ModelID: "unlisted"}
			}
		}
		h := providerHandlerForTest(b, schemas.OpenAI, keys, models, models)
		catalog, err := modelcatalog.NewTestCatalog(nil)
		require.NoError(b, err)
		h.config.ModelCatalog = catalog
		h.config.ModelCatalog.SetKeyConfigForProvider(schemas.OpenAI, keys)
		for _, flag := range []string{"", "&include_aliases=true"} {
			b.Run(fmt.Sprintf("aliases=%d,include=%t", len(keys)*aliasesPerKey, flag != ""), func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					ctx := &fasthttp.RequestCtx{}
					ctx.Request.Header.SetMethod("GET")
					ctx.Request.SetRequestURI("/api/models?provider=openai&unfiltered=true&limit=20" + flag)
					h.listModels(ctx)
				}
			})
		}
	}
}

// TestRedactedKeys_PreservesEnabled pins that /api/keys reports each key's enabled
// flag. redactedKeys builds its keys field by field, and once dropped Enabled, so a
// disabled key read as enabled to every client of the endpoint.
func TestRedactedKeys_PreservesEnabled(t *testing.T) {
	cfg := libtest.FileOnly(t, &lib.Config{}, map[schemas.ModelProvider]configstore.ProviderConfig{
		"typesafe": {},
	}, map[schemas.ModelProvider][]schemas.Key{
		"typesafe": {
			{ID: "disabled-key", Name: "disabled", Value: *schemas.NewSecretVar("sk-disabled"), Enabled: new(false)},
			{ID: "enabled-key", Name: "enabled", Value: *schemas.NewSecretVar("sk-enabled"), Enabled: new(true)},
			{ID: "unset-key", Name: "unset", Value: *schemas.NewSecretVar("sk-unset")},
		},
	})

	keys := redactedKeys(map[schemas.ModelProvider][]schemas.Key{"typesafe": cfg.GetProviderKeys("typesafe")})
	byID := make(map[string]configstoreTables.TableKey, len(keys))
	for _, key := range keys {
		byID[key.KeyID] = key
	}

	if got := byID["disabled-key"].Enabled; got == nil || *got {
		t.Fatalf("disabled key: expected Enabled=false, got %v", got)
	}
	if got := byID["enabled-key"].Enabled; got == nil || !*got {
		t.Fatalf("enabled key: expected Enabled=true, got %v", got)
	}
	// Unset stays unset: the field is omitted and clients read it as enabled.
	if got := byID["unset-key"].Enabled; got != nil {
		t.Fatalf("unset key: expected Enabled=nil, got %v", *got)
	}

	// The flag is what clients act on, so pin it on the wire too.
	body, err := json.Marshal(byID["disabled-key"])
	if err != nil {
		t.Fatalf("marshal disabled key: %v", err)
	}
	if !strings.Contains(string(body), `"enabled":false`) {
		t.Fatalf("disabled key JSON should carry \"enabled\":false, got %s", body)
	}
}

// TestRedactedKeys_RedactsAnthropicWorkloadIdentity pins that /api/keys carries a federated key's
// workload identity block with the identity token masked and the identifiers readable.
func TestRedactedKeys_RedactsAnthropicWorkloadIdentity(t *testing.T) {
	const jwt = "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ3b3JrbG9hZCJ9.c2lnbmF0dXJl"
	cfg := libtest.FileOnly(t, &lib.Config{}, map[schemas.ModelProvider]configstore.ProviderConfig{
		schemas.Anthropic: {},
	}, map[schemas.ModelProvider][]schemas.Key{
		schemas.Anthropic: {{ID: "wif-key", Name: "wif", AnthropicKeyConfig: &schemas.AnthropicKeyConfig{
			WorkloadIdentity: &schemas.AnthropicWorkloadIdentityConfig{
				FederationRuleID: *schemas.NewSecretVar("fdrl_01abc"),
				OrganizationID:   *schemas.NewSecretVar("org-uuid"),
				ServiceAccountID: *schemas.NewSecretVar("svac_01def"),
				IdentitySource:   &schemas.IdentitySource{Type: schemas.IdentitySourceInline, Token: schemas.NewSecretVar(jwt)},
			},
		}}},
	})

	keys := redactedKeys(map[schemas.ModelProvider][]schemas.Key{schemas.Anthropic: cfg.GetProviderKeys(schemas.Anthropic)})
	require.Len(t, keys, 1)
	require.True(t, keys[0].AnthropicKeyConfig.HasWorkloadIdentity())
	wif := keys[0].AnthropicKeyConfig.WorkloadIdentity
	assert.Equal(t, "svac_01def", wif.ServiceAccountID.GetValue())
	assert.True(t, wif.IdentitySource.Token.IsMaskedPlaceholder())
	body, err := json.Marshal(keys[0])
	require.NoError(t, err)
	assert.NotContains(t, string(body), jwt)
	assert.Contains(t, string(body), `"anthropic_key_config"`)
	assert.Equal(t, jwt, cfg.GetProviderKeys(schemas.Anthropic)[0].AnthropicKeyConfig.WorkloadIdentity.IdentitySource.Token.GetValue(), "the live config is untouched")
}

// TestRedactedKeys_MasksBedrockBatchRoleAndEndpoints pins that /api/keys masks a Bedrock key the
// way the providers API does: the batch role ARN, the env-backed endpoints and project ID, and the
// credentials come back masked, while the key ID, name, provider and region stay readable.
func TestRedactedKeys_MasksBedrockBatchRoleAndEndpoints(t *testing.T) {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})
	t.Setenv("BEDROCK_KEYS_TEST_ENDPOINT", "https://bedrock-runtime.internal.example.com")
	t.Setenv("BEDROCK_KEYS_TEST_PROJECT", "bedrock-project-secret-0123")
	const batchRole = "arn:aws:iam::123456789012:role/bedrock-batch-example"
	key := schemas.Key{ID: "bedrock-key", Name: "bedrock", Models: schemas.WhiteList{"*"}, BedrockKeyConfig: &schemas.BedrockKeyConfig{
		AccessKey:    *schemas.NewSecretVar("AKIAEXAMPLEBEDROCK01"),
		SecretKey:    *schemas.NewSecretVar("bedrock-secret-key-example"),
		Region:       schemas.NewSecretVar("us-east-1"),
		BatchRoleARN: schemas.NewSecretVar(batchRole),
		ProjectID:    schemas.NewSecretVar("env.BEDROCK_KEYS_TEST_PROJECT"),
		Endpoints:    &schemas.BedrockEndpoints{Runtime: schemas.NewSecretVar("env.BEDROCK_KEYS_TEST_ENDPOINT")},
	}}
	cfg := libtest.FileOnly(t, &lib.Config{}, map[schemas.ModelProvider]configstore.ProviderConfig{schemas.Bedrock: {}}, map[schemas.ModelProvider][]schemas.Key{schemas.Bedrock: {key}})
	h := &ProviderHandler{config: cfg}

	ctx := keyRouteRequest(h.listKeys, schemas.Bedrock, "")
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	body := string(ctx.Response.Body())
	for _, secret := range []string{batchRole, "bedrock-runtime.internal.example.com", "bedrock-project-secret-0123", "AKIAEXAMPLEBEDROCK01", "bedrock-secret-key-example"} {
		assert.NotContains(t, body, secret)
	}
	assert.Contains(t, body, "env.BEDROCK_KEYS_TEST_ENDPOINT", "the endpoint's reference stays visible")
	var listed []struct {
		KeyID            string `json:"key_id"`
		Name             string `json:"name"`
		Provider         string `json:"provider"`
		BedrockKeyConfig struct {
			Region struct {
				Value string `json:"value"`
			} `json:"region"`
		} `json:"bedrock_key_config"`
	}
	require.NoError(t, json.Unmarshal(ctx.Response.Body(), &listed))
	require.Len(t, listed, 1)
	assert.Equal(t, "bedrock-key", listed[0].KeyID)
	assert.Equal(t, "bedrock", listed[0].Name)
	assert.Equal(t, string(schemas.Bedrock), listed[0].Provider)
	assert.Equal(t, "us-east-1", listed[0].BedrockKeyConfig.Region.Value, "the region is an identifier, not a credential")
	assert.Equal(t, batchRole, cfg.GetProviderKeys(schemas.Bedrock)[0].BedrockKeyConfig.BatchRoleARN.GetValue(), "the live key is untouched")
}

// TestListKeys_NeverReturnsASecretValue pins that GET /api/keys, read from the database and from
// memory, masks every credential a key config can hold, whether it is a literal, an env reference
// or a vault reference. A reference stays visible and its resolved value is masked, identifiers
// included; a literal identifier reads back as stored. Each value is unique, so a leak names its
// field, and the count of masks proves every field reached the body.
func TestListKeys_NeverReturnsASecretValue(t *testing.T) {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})
	vaultValues := map[string]string{}
	previousHook := schemas.VaultResolveHook
	schemas.VaultResolveHook = func(_ context.Context, value *string) error {
		resolved, ok := vaultValues[*value]
		if !ok {
			return fmt.Errorf("unknown vault reference %q", *value)
		}
		*value = resolved
		return nil
	}
	t.Cleanup(func() { schemas.VaultResolveHook = previousHook })
	// Redacted keeps the first and last four characters, so every value starts and ends the same
	// and its mask is this string.
	const mask = "head************************tail"

	for _, source := range []string{"literal", "env", "vault"} {
		t.Run(source, func(t *testing.T) {
			type field struct {
				name, value, ref string
				credential       bool
			}
			var fields []field
			// secret returns a SecretVar for one field, from source. A credential is masked even as a
			// literal; an identifier is masked only when it comes from a reference.
			secret := func(name string, credential bool) *schemas.SecretVar {
				value := "head-" + source + "-" + name + "-tail"
				input := value
				switch source {
				case "env":
					input = "env.LISTKEYS_" + strings.ToUpper(strings.NewReplacer(".", "_", "-", "_").Replace(name))
					t.Setenv(strings.TrimPrefix(input, "env."), value)
				case "vault":
					input = "vault.listkeys/" + name
					vaultValues[input] = value
				}
				ref := ""
				if source != "literal" {
					ref = input
				}
				fields = append(fields, field{name: name, value: value, ref: ref, credential: credential})
				sv := schemas.NewSecretVar(input)
				// An unresolved reference would otherwise make the leak check pass.
				require.Equal(t, value, sv.GetValue(), "setup: %s must resolve", name)
				return sv
			}
			const cred, ident = true, false
			endpoints := func(prefix string) *schemas.BedrockEndpoints {
				return &schemas.BedrockEndpoints{
					Runtime: secret(prefix+".endpoints.runtime", ident), ControlPlane: secret(prefix+".endpoints.control_plane", ident),
					Mantle: secret(prefix+".endpoints.mantle", ident), AgentRuntime: secret(prefix+".endpoints.agent_runtime", ident),
					S3: secret(prefix+".endpoints.s3", ident),
				}
			}
			// extra_params values are literal strings, never resolved, and always masked.
			extraParam := "head-" + source + "-oauth.extra_param-tail"
			fields = append(fields, field{name: "oauth.extra_param", value: extraParam, credential: true})
			keys := map[schemas.ModelProvider][]schemas.Key{
				schemas.OpenAI: {{ID: "openai-key", Name: "openai", Models: schemas.WhiteList{"*"},
					Value: *secret("openai.value", cred),
					OAuthKeyConfig: &schemas.OAuthKeyConfig{
						GrantType: schemas.OAuthGrantClientCredentials, TokenURL: *secret("oauth.token_url", ident),
						ClientID: secret("oauth.client_id", cred), ClientSecret: secret("oauth.client_secret", cred),
						PrivateKey: secret("oauth.private_key", cred), ExtraParams: map[string]string{"client_assertion": extraParam},
					},
				}},
				schemas.Azure: {{ID: "azure-key", Name: "azure", Models: schemas.WhiteList{"*"},
					AzureKeyConfig: &schemas.AzureKeyConfig{
						Endpoint: *secret("azure.endpoint", ident), ClientID: secret("azure.client_id", cred),
						ClientSecret: secret("azure.client_secret", cred), TenantID: secret("azure.tenant_id", cred),
					},
					Aliases: schemas.KeyAliases{"gpt": {ModelID: "gpt-deployment", AzureAliasCfg: &schemas.AzureAliasCfg{Endpoint: secret("azure.alias.endpoint", ident)}}},
				}},
				schemas.Vertex: {{ID: "vertex-key", Name: "vertex", Models: schemas.WhiteList{"*"},
					VertexKeyConfig: &schemas.VertexKeyConfig{
						ProjectID: *secret("vertex.project_id", cred), ProjectNumber: *secret("vertex.project_number", cred),
						Region: *secret("vertex.region", ident), AuthCredentials: *secret("vertex.auth_credentials", cred),
						AWSWorkloadIdentity: &schemas.VertexAWSWorkloadIdentityConfig{
							Audience: *secret("vertex.wif.audience", ident), ServiceAccountEmail: secret("vertex.wif.service_account_email", ident),
							AWSRegion: secret("vertex.wif.aws_region", ident), AWSRoleARN: secret("vertex.wif.aws_role_arn", cred),
						},
					},
					Aliases: schemas.KeyAliases{
						"gemini": {ModelID: "gemini-model", Region: secret("vertex.alias.region", ident), ProjectID: secret("vertex.alias.project_id", ident),
							VertexAliasCfg: &schemas.VertexAliasCfg{ProjectNumber: secret("vertex.alias.project_number", ident)}},
						// MarshalJSON promotes the deprecated project ID; it must be masked before that.
						"legacy": {ModelID: "legacy-model", VertexAliasCfg: &schemas.VertexAliasCfg{ProjectID: secret("vertex.alias.legacy_project_id", ident)}},
					},
				}},
				schemas.Bedrock: {{ID: "bedrock-key", Name: "bedrock", Models: schemas.WhiteList{"*"},
					BedrockKeyConfig: &schemas.BedrockKeyConfig{
						AccessKey: *secret("bedrock.access_key", cred), SecretKey: *secret("bedrock.secret_key", cred),
						SessionToken: secret("bedrock.session_token", cred), Region: secret("bedrock.region", ident),
						ARN: secret("bedrock.arn", cred), RoleARN: secret("bedrock.role_arn", cred),
						ExternalID: secret("bedrock.external_id", cred), RoleSessionName: secret("bedrock.session_name", cred),
						BatchRoleARN: secret("bedrock.batch_role_arn", cred), ProjectID: secret("bedrock.project_id", ident),
						BatchS3Config: &schemas.BatchS3Config{Buckets: []schemas.S3BucketConfig{{BucketName: "listkeys-batch-bucket", IsDefault: true}}},
						Endpoints:     endpoints("bedrock"),
					},
					Aliases: schemas.KeyAliases{"claude": {ModelID: "claude-model", BedrockAliasCfg: &schemas.BedrockAliasCfg{InferenceProfileARN: secret("bedrock.alias.inference_profile_arn", ident)}}},
				}},
				schemas.BedrockMantle: {{ID: "mantle-key", Name: "mantle", Models: schemas.WhiteList{"*"},
					BedrockMantleKeyConfig: &schemas.BedrockMantleKeyConfig{
						AccessKey: *secret("mantle.access_key", cred), SecretKey: *secret("mantle.secret_key", cred),
						SessionToken: secret("mantle.session_token", cred), Region: secret("mantle.region", ident),
						RoleARN: secret("mantle.role_arn", cred), ExternalID: secret("mantle.external_id", cred),
						RoleSessionName: secret("mantle.session_name", cred), ProjectID: secret("mantle.project_id", ident),
						Endpoints: endpoints("mantle"),
					},
				}},
				schemas.SageMaker: {{ID: "sagemaker-key", Name: "sagemaker", Models: schemas.WhiteList{"*"},
					SageMakerKeyConfig: &schemas.SageMakerKeyConfig{
						AccessKey: *secret("sagemaker.access_key", cred), SecretKey: *secret("sagemaker.secret_key", cred),
						SessionToken: secret("sagemaker.session_token", cred), Region: secret("sagemaker.region", ident),
						RoleARN: secret("sagemaker.role_arn", cred), ExternalID: secret("sagemaker.external_id", cred),
						RoleSessionName: secret("sagemaker.session_name", cred),
						Endpoints:       &schemas.SageMakerEndpoints{Runtime: secret("sagemaker.endpoints.runtime", ident)},
					},
				}},
				schemas.VLLM:       {{ID: "vllm-key", Name: "vllm", Models: schemas.WhiteList{"*"}, VLLMKeyConfig: &schemas.VLLMKeyConfig{URL: *secret("vllm.url", ident), ModelName: "served-model"}}},
				schemas.Ollama:     {{ID: "ollama-key", Name: "ollama", Models: schemas.WhiteList{"*"}, OllamaKeyConfig: &schemas.OllamaKeyConfig{URL: *secret("ollama.url", ident)}}},
				schemas.SGL:        {{ID: "sgl-key", Name: "sgl", Models: schemas.WhiteList{"*"}, SGLKeyConfig: &schemas.SGLKeyConfig{URL: *secret("sgl.url", ident)}}},
				schemas.Cloudflare: {{ID: "cloudflare-key", Name: "cloudflare", Models: schemas.WhiteList{"*"}, CloudflareKeyConfig: &schemas.CloudflareKeyConfig{AccountID: *secret("cloudflare.account_id", ident)}}},
				schemas.Databricks: {{ID: "databricks-key", Name: "databricks", Models: schemas.WhiteList{"*"},
					DatabricksKeyConfig: &schemas.DatabricksKeyConfig{
						WorkspaceURL: *secret("databricks.workspace_url", ident),
						ClientID:     secret("databricks.client_id", cred), ClientSecret: secret("databricks.client_secret", cred),
					},
				}},
				schemas.Anthropic: {{ID: "anthropic-key", Name: "anthropic", Models: schemas.WhiteList{"*"},
					AnthropicKeyConfig: &schemas.AnthropicKeyConfig{WorkloadIdentity: &schemas.AnthropicWorkloadIdentityConfig{
						FederationRuleID: *secret("anthropic.federation_rule_id", ident), OrganizationID: *secret("anthropic.organization_id", ident),
						ServiceAccountID: *secret("anthropic.service_account_id", ident), WorkspaceID: secret("anthropic.workspace_id", ident),
						// Only one of path and token is valid at once; both are set to check both.
						IdentitySource: &schemas.IdentitySource{Type: schemas.IdentitySourceInline,
							Path: secret("anthropic.identity_path", ident), Token: secret("anthropic.identity_token", cred)},
					}},
				}},
				schemas.Baseten: {{ID: "baseten-key", Name: "baseten", Models: schemas.WhiteList{"*"},
					Aliases: schemas.KeyAliases{"dedicated": {ModelID: "dedicated-model", BasetenAliasCfg: &schemas.BasetenAliasCfg{DeploymentURL: secret("baseten.alias.deployment_url", ident)}}},
				}},
			}
			providers := make(map[schemas.ModelProvider]configstore.ProviderConfig, len(keys))
			for provider := range keys {
				providers[provider] = configstore.ProviderConfig{}
			}
			withStore := libtest.Config(t, providers, keys)

			for name, h := range map[string]*ProviderHandler{
				"database": {config: withStore, configStore: withStore.ConfigStore},
				"memory":   {config: libtest.FileOnly(t, &lib.Config{}, providers, keys)},
			} {
				t.Run(name, func(t *testing.T) {
					ctx := keyRouteRequest(h.listKeys, "", "")
					require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), "body %s", ctx.Response.Body())
					body := string(ctx.Response.Body())
					var listed []map[string]any
					require.NoError(t, json.Unmarshal(ctx.Response.Body(), &listed))
					require.Len(t, listed, len(keys))
					masks := 0
					for _, f := range fields {
						if f.credential || f.ref != "" {
							// The middle is what the mask hides; the whole value contains it.
							assert.NotContains(t, body, f.value[4:len(f.value)-4], "%s leaked", f.name)
							masks++
						} else {
							assert.Contains(t, body, `"`+f.value+`"`, "the literal identifier %s reads back as stored", f.name)
						}
						if f.ref != "" {
							assert.Contains(t, body, `"`+f.ref+`"`, "the reference of %s stays visible", f.name)
						}
					}
					assert.Equal(t, masks, strings.Count(body, `"`+mask+`"`), "every masked field reaches the body once: %s", body)
					assert.Contains(t, body, `"listkeys-batch-bucket"`, "the batch bucket is not a secret")
				})
			}
		})
	}
}
