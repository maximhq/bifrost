package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// e2eAccount is a minimal schemas.Account backing a single provider, pointed at a mock
// OpenAI-compatible upstream server for end-to-end retrieve-model tests.
type e2eAccount struct {
	config *schemas.ProviderConfig
}

func (a *e2eAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{schemas.OpenAI}, nil
}

func (a *e2eAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	return a.config, nil
}

func (a *e2eAccount) GetKeysForProvider(_ context.Context, _ schemas.ModelProvider) ([]schemas.Key, error) {
	return []schemas.Key{{ID: "test-key", Value: *schemas.NewSecretVar("sk-test"), Weight: 100, Models: schemas.WhiteList{"*"}}}, nil
}

// e2eCustomModelJSON is a minimal OpenAI-compatible /v1/models response for a single model
// carrying non-standard fields, as a self-hosted OpenAI-compatible backend (e.g. vLLM) might.
const e2eCustomModelJSON = `{"object":"list","data":[{"id":"custom-model-1","object":"model","owned_by":"acme","max_model_len":8192,"quantization":"Q4_K_M"}]}`

// newE2ERetrieveModelHandler spins up a mock OpenAI-compatible upstream and a real
// *bifrost.Bifrost client pointed at it, wired into a CompletionHandler exactly as the HTTP
// transport constructs it, so retrieveModel is exercised end-to-end: HTTP handler -> core
// Bifrost client -> real Provider.ListModels -> real HTTP call to the mock server.
func newE2ERetrieveModelHandler(t *testing.T, includeCustomModelFields bool) (*CompletionHandler, func()) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(e2eCustomModelJSON))
	}))

	account := &e2eAccount{
		config: &schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{
				BaseURL:                        server.URL,
				DefaultRequestTimeoutInSeconds: 5,
			},
			IncludeCustomModelFields: includeCustomModelFields,
		},
	}

	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{Account: account})
	if err != nil {
		server.Close()
		t.Fatalf("bifrost.Init failed: %v", err)
	}

	h := &CompletionHandler{client: client, config: &lib.Config{ClientConfig: &configstore.ClientConfig{}}}
	return h, server.Close
}

func TestRetrieveModel_E2E_CustomFieldsPreservedWhenFlagEnabled(t *testing.T) {
	t.Logf("mock upstream GET /v1/models raw response:\n%s", e2eCustomModelJSON)

	h, cleanup := newE2ERetrieveModelHandler(t, true)
	defer cleanup()

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/v1/models/openai/custom-model-1")
	ctx.SetUserValue("model_id", "openai/custom-model-1")

	h.retrieveModel(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	body := string(ctx.Response.Body())
	t.Logf("GET /v1/models/openai/custom-model-1 (include_custom_model_fields=true) -> 200:\n%s", body)
	for _, want := range []string{`"id":"openai/custom-model-1"`, `"max_model_len":8192`, `"quantization":"Q4_K_M"`} {
		if !strings.Contains(body, want) {
			t.Errorf("expected response to contain %s, got: %s", want, body)
		}
	}
}

func TestRetrieveModel_E2E_CustomFieldsDroppedWhenFlagDisabled(t *testing.T) {
	h, cleanup := newE2ERetrieveModelHandler(t, false)
	defer cleanup()

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/v1/models/openai/custom-model-1")
	ctx.SetUserValue("model_id", "openai/custom-model-1")

	h.retrieveModel(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	body := string(ctx.Response.Body())
	t.Logf("GET /v1/models/openai/custom-model-1 (include_custom_model_fields=false) -> 200:\n%s", body)
	if !strings.Contains(body, `"id":"openai/custom-model-1"`) {
		t.Fatalf("expected response to contain the model id, got: %s", body)
	}
	for _, notWant := range []string{"max_model_len", "quantization"} {
		if strings.Contains(body, notWant) {
			t.Errorf("expected response to NOT contain %s when flag disabled, got: %s", notWant, body)
		}
	}
}

// TestRetrieveModel_E2E_CatchAllLeadingSlashStripped is a regression test for a routing bug:
// {model_id:*} is a fasthttp/router catch-all (required since Bifrost model IDs are
// "provider/model", which contain a "/" that a plain {model_id} param can't match), and
// catch-all params come back from the router with a leading "/" that must be stripped before
// use. The other tests in this file bypass the router (they call ctx.SetUserValue directly
// without the leading slash), so they didn't catch this; this test reproduces what the real
// router hands the handler.
func TestRetrieveModel_E2E_CatchAllLeadingSlashStripped(t *testing.T) {
	h, cleanup := newE2ERetrieveModelHandler(t, true)
	defer cleanup()

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/v1/models/openai/custom-model-1")
	ctx.SetUserValue("model_id", "/openai/custom-model-1") // leading "/", as fasthttp/router's catch-all produces

	h.retrieveModel(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Fatalf("expected 200, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	body := string(ctx.Response.Body())
	if !strings.Contains(body, `"id":"openai/custom-model-1"`) {
		t.Errorf("expected leading slash to be stripped from model_id, got: %s", body)
	}
}

// twoProviderVKAccount backs two providers (openai, groq) each pointed at their own mock
// server, so a test can prove a virtual-key provider restriction actually blocks the
// disallowed one rather than just filtering its output after the fact.
type twoProviderVKAccount struct {
	openaiURL string
	groqURL   string
	groqHits  *int
}

func (a *twoProviderVKAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{schemas.OpenAI, schemas.Groq}, nil
}

func (a *twoProviderVKAccount) GetConfigForProvider(provider schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	url := a.openaiURL
	if provider == schemas.Groq {
		url = a.groqURL
	}
	return &schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: url, DefaultRequestTimeoutInSeconds: 5},
	}, nil
}

func (a *twoProviderVKAccount) GetKeysForProvider(_ context.Context, _ schemas.ModelProvider) ([]schemas.Key, error) {
	return []schemas.Key{{ID: "test-key", Value: *schemas.NewSecretVar("sk-test"), Weight: 100, Models: schemas.WhiteList{"*"}}}, nil
}

// TestRetrieveModel_E2E_VirtualKeyProviderRestrictionBlocksExplicitProvider is a regression
// test for a bug found in review: retrieveModel only ran applyListModelsVirtualKeyProviderFilter
// (which resolves the VK's allowed-provider set onto the context) when no provider was given,
// so a virtual key restricted to a provider subset could retrieve metadata from an excluded
// provider just by naming it explicitly in the path/query. Verifies the disallowed provider's
// mock server is never even hit.
func TestRetrieveModel_E2E_VirtualKeyProviderRestrictionBlocksExplicitProvider(t *testing.T) {
	groqHits := 0
	openaiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-4o","object":"model","owned_by":"openai"}]}`))
	}))
	defer openaiServer.Close()
	groqServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		groqHits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"llama3-groq","object":"model","owned_by":"groq"}]}`))
	}))
	defer groqServer.Close()

	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account: &twoProviderVKAccount{openaiURL: openaiServer.URL, groqURL: groqServer.URL, groqHits: &groqHits},
	})
	if err != nil {
		t.Fatalf("bifrost.Init failed: %v", err)
	}

	h := &CompletionHandler{
		client: client,
		config: &lib.Config{
			ClientConfig: &configstore.ClientConfig{},
			ConfigStore: &mockListModelsVKConfigStore{vk: &configstoreTables.TableVirtualKey{
				Value:           *schemas.NewSecretVar("sk-bf-vk-openai-only"),
				IsActive:        new(true),
				ProviderConfigs: []configstoreTables.TableVirtualKeyProviderConfig{{Provider: "openai"}},
			}},
		},
	}

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.Header.Set("Authorization", "Bearer sk-bf-vk-openai-only")
	ctx.Request.SetRequestURI("/v1/models/groq/llama3-groq")
	ctx.SetUserValue("model_id", "groq/llama3-groq")

	h.retrieveModel(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusNotFound {
		t.Fatalf("expected 404 (provider outside VK allow-list), got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
	if groqHits != 0 {
		t.Errorf("expected groq's server to never be called, got %d hits", groqHits)
	}
}

func TestRetrieveModel_E2E_NotFoundReturns404(t *testing.T) {
	h, cleanup := newE2ERetrieveModelHandler(t, true)
	defer cleanup()

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI("/v1/models/openai/does-not-exist")
	ctx.SetUserValue("model_id", "openai/does-not-exist")

	h.retrieveModel(ctx)

	t.Logf("GET /v1/models/openai/does-not-exist -> %d:\n%s", ctx.Response.StatusCode(), ctx.Response.Body())
	if ctx.Response.StatusCode() != fasthttp.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
	}
}
