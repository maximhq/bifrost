package handlers

import (
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// newestTestHandler serves two providers whose list-models responses were
// cached with creation times, plus one model without a time.
func newestTestHandler() *ProviderHandler {
	catalog := modelcatalog.NewTestCatalog(nil)
	catalog.UpsertLiveFromResponse(schemas.OpenAI, "key-a", false, &schemas.BifrostListModelsResponse{Data: []schemas.Model{
		{ID: "openai/synthetic-old", Created: schemas.Ptr(int64(100))},
		{ID: "openai/synthetic-new", Created: schemas.Ptr(int64(300))},
		{ID: "openai/synthetic-mid", Created: schemas.Ptr(int64(200))},
		{ID: "openai/synthetic-untimed"},
	}})
	catalog.UpsertLiveFromResponse(schemas.Mistral, "key-m", false, &schemas.BifrostListModelsResponse{Data: []schemas.Model{
		{ID: "mistral/synthetic-a", Created: schemas.Ptr(int64(9999))},
	}})
	return &ProviderHandler{
		inMemoryStore: &lib.Config{
			ClientConfig: &configstore.ClientConfig{},
			ModelCatalog: catalog,
			Providers: map[schemas.ModelProvider]configstore.ProviderConfig{
				schemas.OpenAI:  {Keys: []schemas.Key{{ID: "key-a"}}},
				schemas.Mistral: {Keys: []schemas.Key{{ID: "key-m"}}},
			},
		},
		modelsManager: &mockModelsManager{filtered: map[schemas.ModelProvider][]string{
			schemas.OpenAI:  {"synthetic-old", "synthetic-new", "synthetic-mid", "synthetic-untimed"},
			schemas.Mistral: {"synthetic-a"},
		}},
	}
}

// callNewestModels sends GET /api/models/newest with query and decodes the answer.
func callNewestModels(t *testing.T, h *ProviderHandler, query string) (int, []NewestModel) {
	t.Helper()
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/models/newest?" + query)
	h.listNewestModels(ctx)
	var body ListNewestModelsResponse
	if ctx.Response.StatusCode() == fasthttp.StatusOK {
		if err := json.Unmarshal(ctx.Response.Body(), &body); err != nil {
			t.Fatal(err)
		}
	}
	return ctx.Response.StatusCode(), body.Models
}

// TestListNewestModels returns each requested provider's newest models, in
// the requested provider order, comparing times only within a provider.
func TestListNewestModels(t *testing.T) {
	h := newestTestHandler()
	status, models := callNewestModels(t, h, "providers=openai,mistral&limit=2")
	if status != fasthttp.StatusOK {
		t.Fatalf("status = %d", status)
	}
	want := []NewestModel{
		{Name: "synthetic-new", Provider: "openai", Created: 300},
		{Name: "synthetic-mid", Provider: "openai", Created: 200},
		{Name: "synthetic-a", Provider: "mistral", Created: 9999},
	}
	if len(models) != len(want) {
		t.Fatalf("models = %+v, want %+v", models, want)
	}
	for i := range want {
		if models[i] != want[i] {
			t.Fatalf("models = %+v, want %+v", models, want)
		}
	}
}

// TestListNewestModelsRejectsBadInput refuses a missing provider list and a
// limit outside 1..50 instead of guessing.
func TestListNewestModelsRejectsBadInput(t *testing.T) {
	h := newestTestHandler()
	for _, query := range []string{"", "providers=openai&limit=0", "providers=openai&limit=51", "providers=openai&limit=x"} {
		if status, _ := callNewestModels(t, h, query); status != fasthttp.StatusBadRequest {
			t.Fatalf("%q: status = %d, want 400", query, status)
		}
	}
}
