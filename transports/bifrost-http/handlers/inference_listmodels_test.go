package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// The listing hands the narrowing to whoever can answer what the request may reach, so the
// providers it publishes are the ones that answer grants.
func TestApplyListModelsProviderFilterDelegatesToTheModelsManager(t *testing.T) {
	permit := grant.NewPermit(grant.PermitVirtualKey, "vk-test", "Test VK", true, false, []schemas.ProviderPermit{
		{Provider: "openai", AllowedModels: []string{"gpt-4o"}},
		// A provider granted no model at all is still asked: the fan-out decides who can
		// serve the request, and the response is filtered per model afterwards.
		{Provider: "anthropic"},
	}, nil)
	manager := &mockModelsManager{access: grant.NewAccess([]schemas.Permit{permit}, nil, "", nil)}
	h := &CompletionHandler{modelsManager: manager}

	bifrostCtx := schemas.NewBifrostContext(context.Background(), time.Time{})

	h.applyListModelsProviderFilter(bifrostCtx)

	if manager.narrowCalls != 1 {
		t.Fatalf("narrow calls = %d, want 1", manager.narrowCalls)
	}
	got, ok := bifrostCtx.Value(schemas.BifrostContextKeyAvailableProviders).([]schemas.ModelProvider)
	if !ok {
		t.Fatalf("expected available providers to be published as []schemas.ModelProvider, got %#v",
			bifrostCtx.Value(schemas.BifrostContextKeyAvailableProviders))
	}
	want := []schemas.ModelProvider{schemas.OpenAI, schemas.Anthropic}
	if len(got) != len(want) {
		t.Fatalf("expected providers %#v, got %#v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected providers %#v, got %#v", want, got)
		}
	}
}

// Nothing resolved must leave the fan-out alone. Publishing an empty list here would mean "no
// provider may serve this", turning an unrestricted request into one that lists nothing.
func TestApplyListModelsProviderFilterLeavesFanOutAloneWhenNothingResolved(t *testing.T) {
	manager := &mockModelsManager{}
	h := &CompletionHandler{modelsManager: manager}

	bifrostCtx := schemas.NewBifrostContext(context.Background(), time.Time{})

	h.applyListModelsProviderFilter(bifrostCtx)

	if manager.narrowCalls != 1 {
		t.Fatalf("narrow calls = %d, want 1", manager.narrowCalls)
	}
	if got := bifrostCtx.Value(schemas.BifrostContextKeyAvailableProviders); got != nil {
		t.Fatalf("expected nothing to be published, got %#v", got)
	}
}

// Narrowing is an optimization, not a permission check, so a handler wired without a models
// manager falls through instead of panicking on the request path.
func TestApplyListModelsProviderFilterWithoutModelsManager(t *testing.T) {
	h := &CompletionHandler{}

	bifrostCtx := schemas.NewBifrostContext(context.Background(), time.Time{})

	h.applyListModelsProviderFilter(bifrostCtx)

	if got := bifrostCtx.Value(schemas.BifrostContextKeyAvailableProviders); got != nil {
		t.Fatalf("expected nothing to be published, got %#v", got)
	}
}

// TestListModels_TagsFilterAndPagination pins tags on GET /v1/models: every model carries its
// gateway-assigned tags (anything an upstream body says is ignored), tags=a,b keeps only models
// carrying every tag, pagination is cut after the filter, and the tags parameter is not
// forwarded to the provider.
func TestListModels_TagsFilterAndPagination(t *testing.T) {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})

	var upstreamQueries []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamQueries = append(upstreamQueries, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[
			{"id":"gpt-4o","object":"model","owned_by":"openai"},
			{"id":"gpt-4o-mini","object":"model","owned_by":"openai","tags":["from-upstream"]},
			{"id":"gpt-5.1","object":"model","owned_by":"openai"},
			{"id":"o3","object":"model","owned_by":"openai"}]}`))
	}))
	defer upstream.Close()

	store := &lib.Config{
		ClientConfig: &configstore.ClientConfig{},
		Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
			schemas.OpenAI: {
				Keys:          []schemas.Key{{ID: "k1", Name: "k1", Value: *schemas.NewSecretVar("sk-test"), Models: schemas.WhiteList{"*"}, Weight: 1}},
				NetworkConfig: &schemas.NetworkConfig{BaseURL: upstream.URL, AllowPrivateNetwork: true, DefaultRequestTimeoutInSeconds: 5},
			},
		},
	}
	catalog := modelcatalog.NewTestCatalogWithConfigStore(&modelTagsConfigStore{tags: map[string]map[string][]string{
		"openai": {"gpt-4o": {"eu", "prod"}, "gpt-5.1": {"prod"}, "o3": {"eu", "prod"}},
	}})
	require.NoError(t, catalog.ReloadModelTags(context.Background()))
	store.ModelCatalog = catalog
	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{Account: lib.NewBaseAccount(store), Logger: bifrost.NewDefaultLogger(schemas.LogLevelError)})
	require.NoError(t, err)
	t.Cleanup(client.Shutdown)
	h := &CompletionHandler{client: client, config: store}

	list := func(query string) (*schemas.BifrostListModelsResponse, int) {
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.Header.SetMethod(fasthttp.MethodGet)
		ctx.Request.SetRequestURI("/v1/models?" + query)
		h.listModels(ctx)
		if ctx.Response.StatusCode() != fasthttp.StatusOK {
			return nil, ctx.Response.StatusCode()
		}
		var resp schemas.BifrostListModelsResponse
		require.NoError(t, sonic.Unmarshal(ctx.Response.Body(), &resp))
		return &resp, fasthttp.StatusOK
	}
	ids := func(resp *schemas.BifrostListModelsResponse) []string {
		out := make([]string, 0, len(resp.Data))
		for _, m := range resp.Data {
			out = append(out, m.ID)
		}
		return out
	}

	resp, status := list("provider=openai")
	require.Equal(t, fasthttp.StatusOK, status)
	tagsByID := map[string][]string{}
	for _, m := range resp.Data {
		tagsByID[m.ID] = m.Tags
	}
	assert.Equal(t, []string{"eu", "prod"}, tagsByID["openai/gpt-4o"])
	assert.Nil(t, tagsByID["openai/gpt-4o-mini"], "tags from an upstream body must not leak through")

	resp, status = list("provider=openai&tags=prod,eu")
	require.Equal(t, fasthttp.StatusOK, status)
	assert.ElementsMatch(t, []string{"openai/gpt-4o", "openai/o3"}, ids(resp))

	page1, status := list("provider=openai&tags=prod&page_size=2")
	require.Equal(t, fasthttp.StatusOK, status)
	require.Len(t, page1.Data, 2, "a page is cut from the filtered list")
	require.NotEmpty(t, page1.NextPageToken)
	page2, status := list("provider=openai&tags=prod&page_size=2&page_token=" + url.QueryEscape(page1.NextPageToken))
	require.Equal(t, fasthttp.StatusOK, status)
	assert.Len(t, page2.Data, 1)
	assert.Empty(t, page2.NextPageToken)
	assert.ElementsMatch(t, []string{"openai/gpt-4o", "openai/gpt-5.1", "openai/o3"}, append(ids(page1), ids(page2)...))

	require.NotEmpty(t, upstreamQueries)
	for _, q := range upstreamQueries {
		assert.NotContains(t, q, "tags", "the tags filter must not be forwarded upstream")
	}

	_, status = list("provider=openai&tags=a%20b")
	assert.Equal(t, fasthttp.StatusBadRequest, status)
}
