package handlers

import (
	"context"
	"strings"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/valyala/fasthttp"
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

// OpenAI SDKs percent-encode the id ("openai%2Fgpt-4o-mini") and the router hands the
// catch-all over still encoded, so the target must be read after decoding it once.
func TestModelRetrieveTarget(t *testing.T) {
	for _, test := range []struct {
		name         string
		pathModel    string
		query        string
		wantProvider schemas.ModelProvider
		wantModel    string
		wantErr      string
	}{
		{name: "literal provider/model", pathModel: "openai/gpt-4o-mini", wantProvider: schemas.OpenAI, wantModel: "gpt-4o-mini"},
		{name: "encoded provider/model", pathModel: "openai%2Fgpt-4o-mini", wantProvider: schemas.OpenAI, wantModel: "gpt-4o-mini"},
		{name: "encoded namespaced id", pathModel: "groq%2Fopenai%2Fgpt-oss-120b", wantProvider: schemas.Groq, wantModel: "openai/gpt-oss-120b"},
		{name: "encoded id under ?provider=", pathModel: "openai%2Fgpt-oss-120b", query: "provider=groq", wantProvider: schemas.Groq, wantModel: "openai/gpt-oss-120b"},
		{name: "bare model without a provider", pathModel: "gpt-4o-mini", wantErr: "provider is required"},
		{name: "no model", pathModel: "%2F", wantErr: "model is required"},
		{name: "malformed encoding", pathModel: "openai%2", wantErr: "invalid model encoding"},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.SetRequestURI("/v1/models/x?" + test.query)
			ctx.SetUserValue("model", test.pathModel)

			provider, model, err := modelRetrieveTarget(ctx)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if provider != test.wantProvider || model != test.wantModel {
				t.Errorf("got (%q, %q), want (%q, %q)", provider, model, test.wantProvider, test.wantModel)
			}
		})
	}
}

// TestListModels_TagsFilterAndPagination pins tags on GET /v1/models: every model carries its
// gateway-assigned tags (anything an upstream body says is ignored), tags=a,b keeps only models
// carrying every tag, pagination is cut after the filter, and the tags parameter is not
// forwarded to the provider.
func TestListModels_TagsFilterAndPagination(t *testing.T) {
	SetLogger(&mockLogger{})
	lib.SetLogger(&mockLogger{})

	var upstreamMu sync.Mutex
	var upstreamQueries []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamMu.Lock()
		upstreamQueries = append(upstreamQueries, r.URL.RawQuery)
		upstreamMu.Unlock()
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

	upstreamMu.Lock()
	defer upstreamMu.Unlock()
	require.NotEmpty(t, upstreamQueries)
	for _, q := range upstreamQueries {
		assert.NotContains(t, q, "tags", "the tags filter must not be forwarded upstream")
	}

	_, status = list("provider=openai&tags=a%20b")
	assert.Equal(t, fasthttp.StatusBadRequest, status)
}

// TestListAllProviderModelPages pins the page walk behind a tag-filtered single-provider
// listing: it asks for schemas.DefaultPageSize pages, follows NextPageToken until it is empty,
// drops a repeated page from a provider that ignores page tokens, and fails on a page error.
func TestListAllProviderModelPages(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	pages := map[string]*schemas.BifrostListModelsResponse{
		"":   {Data: []schemas.Model{{ID: "p/a"}, {ID: "p/b"}}, NextPageToken: "t1"},
		"t1": {Data: []schemas.Model{{ID: "p/c"}}, NextPageToken: "t2"},
		"t2": {Data: []schemas.Model{{ID: "p/d"}}},
	}
	var seen []*schemas.BifrostListModelsRequest
	paged := func(_ *schemas.BifrostContext, req *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
		copied := *req
		seen = append(seen, &copied)
		page := *pages[req.PageToken]
		return &page, nil
	}
	resp, bifrostErr := listAllProviderModelPages(ctx, paged, &schemas.BifrostListModelsRequest{Provider: "p", PageSize: 2, PageToken: "client-token"})
	require.Nil(t, bifrostErr)
	ids := make([]string, 0, len(resp.Data))
	for _, m := range resp.Data {
		ids = append(ids, m.ID)
	}
	assert.Equal(t, []string{"p/a", "p/b", "p/c", "p/d"}, ids)
	assert.Empty(t, resp.NextPageToken)
	require.Len(t, seen, 3)
	assert.Equal(t, schemas.DefaultPageSize, seen[0].PageSize)
	assert.Equal(t, "", seen[0].PageToken, "the client's own page token must not be sent upstream")

	// A provider that ignores the page token returns the first page again with the same token.
	ignoring := func(_ *schemas.BifrostContext, _ *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
		return &schemas.BifrostListModelsResponse{Data: []schemas.Model{{ID: "g/a"}}, NextPageToken: "same"}, nil
	}
	resp, bifrostErr = listAllProviderModelPages(ctx, ignoring, &schemas.BifrostListModelsRequest{Provider: "g"})
	require.Nil(t, bifrostErr)
	assert.Len(t, resp.Data, 1, "a repeated page must not duplicate models")

	calls := 0
	failing := func(_ *schemas.BifrostContext, _ *schemas.BifrostListModelsRequest) (*schemas.BifrostListModelsResponse, *schemas.BifrostError) {
		calls++
		if calls == 2 {
			return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "upstream down"}}
		}
		return &schemas.BifrostListModelsResponse{Data: []schemas.Model{{ID: "f/a"}}, NextPageToken: "next"}, nil
	}
	_, bifrostErr = listAllProviderModelPages(ctx, failing, &schemas.BifrostListModelsRequest{Provider: "f"})
	require.NotNil(t, bifrostErr, "a failed page must fail the listing")
}
