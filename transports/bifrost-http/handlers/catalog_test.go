package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/valyala/fasthttp"
)

func catalogRequest(t *testing.T, h *ProviderHandler, uri string) []byte {
	t.Helper()
	r := router.New()
	h.RegisterCatalogRoutes(r)
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod("GET")
	ctx.Request.SetRequestURI(uri)
	if len(ctx.Host()) == 0 {
		ctx.Request.SetHost("localhost:8080")
	}
	r.Handler(ctx)
	if ctx.Response.StatusCode() != 200 {
		t.Fatalf("GET %s: %d %s", uri, ctx.Response.StatusCode(), ctx.Response.Body())
	}
	return ctx.Response.Body()
}

func TestCatalogFiltering(t *testing.T) {
	SetLogger(&mockLogger{})
	h := providerHandlerForTest(schemas.OpenAI, nil, []string{"vendor/chat", "retired", "embedding", "unknown", "secret"}, []string{"outside-pool"})
	h.inMemoryStore.ModelCatalog = modelCatalogForPricingJSON(t, []byte(`{
		"vendor/chat":{"provider":"openai","mode":"chat"},
		"retired":{"provider":"openai","mode":"chat","is_deprecated":true},
		"embedding":{"provider":"openai","mode":"embedding"}
	}`))
	tests := []struct {
		name           string
		query          string
		restrictAccess bool
		want           []string
	}{
		{
			name:  "configured pool ignores pagination and unfiltered",
			query: "?limit=1&offset=99&unfiltered=true",
			want:  []string{"openai/secret", "openai/unknown", "openai/vendor/chat"},
		},
		{
			name:  "virtual key restricts models",
			query: "?limit=1&offset=99&unfiltered=true", restrictAccess: true,
			want: []string{"openai/unknown", "openai/vendor/chat"},
		},
		{
			name:  "search preserves upstream slashes",
			query: "?query=vendor%2Fchat", restrictAccess: true,
			want: []string{"openai/vendor/chat"},
		},
		{
			name:  "search hides deprecated models",
			query: "?query=retired", restrictAccess: true,
		},
		{
			name:  "search includes deprecated models when requested",
			query: "?provider=openai&query=retired&include_deprecated=true", restrictAccess: true,
			want: []string{"openai/retired"},
		},
		{
			name:  "virtual key restricts providers",
			query: "?provider=anthropic", restrictAccess: true,
		},
	}
	manager := h.modelsManager.(*mockModelsManager)
	for _, format := range []string{"codex", "claude", "pi", "opencode"} {
		t.Run(format, func(t *testing.T) {
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					manager.access = nil
					if tt.restrictAccess {
						manager.access = accessForProviderPermits(schemas.ProviderPermit{
							Provider: "openai", AllowedModels: []string{"vendor/chat", "retired", "embedding", "unknown"},
						})
					}
					if ids := catalogModelIDs(t, h, format, tt.query); !slices.Equal(ids, tt.want) {
						t.Fatalf("exported IDs: got %v, want %v", ids, tt.want)
					}
				})
			}
		})
	}
}

func TestClaudeCatalogEnvelope(t *testing.T) {
	SetLogger(&mockLogger{})
	h := providerHandlerForTest(schemas.OpenAI, nil, []string{"chat", "unknown"}, nil)
	for _, query := range []string{"", "?provider=anthropic"} {
		body := catalogRequest(t, h, "/catalog/claude"+query)
		var response claudeCatalogResponse
		if err := json.Unmarshal(body, &response); err != nil {
			t.Fatal(err)
		}
		if response.HasMore {
			t.Fatalf("unexpected pagination: %s", body)
		}
		if query == "" {
			for _, model := range response.Data {
				if model.Type != "model" || model.CreatedAt != "1970-01-01T00:00:00Z" {
					t.Fatalf("invalid Anthropic model: %+v", model)
				}
			}
			if response.FirstID == nil || *response.FirstID != "openai/chat" || response.LastID == nil || *response.LastID != "openai/unknown" {
				t.Fatalf("invalid bounds: %s", body)
			}
		} else if response.FirstID != nil || response.LastID != nil {
			t.Fatalf("empty bounds: %s", body)
		}
	}
}

type deniedCatalogManager struct{ ModelsManager }

func (deniedCatalogManager) ResolveAccess(*schemas.BifrostContext) (schemas.Access, error) {
	return nil, errors.New("invalid key")
}

func TestCatalogRejectsUnresolvedAccess(t *testing.T) {
	SetLogger(&mockLogger{})
	h := providerHandlerForTest(schemas.OpenAI, nil, []string{"secret"}, nil)
	h.modelsManager = deniedCatalogManager{h.modelsManager}
	for _, format := range []string{"codex", "claude", "pi", "opencode"} {
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.SetRequestURI("/catalog/" + format)
		h.catalog(ctx)
		if ctx.Response.StatusCode() != 401 {
			t.Fatalf("expected access error, got %d: %s", ctx.Response.StatusCode(), ctx.Response.Body())
		}
	}
}

func TestCodexCatalogCapabilities(t *testing.T) {
	context := 64000
	fallbackContext := 32000
	medium := "medium"
	entry := &modelcatalog.PricingEntry{ContextLength: &context, MaxInputTokens: &fallbackContext, Architecture: &schemas.Architecture{InputModalities: []string{"text", "image", "video"}}}
	caps := &schemas.ModelCapabilities{ReasoningEffortLevels: []string{"low", "medium", "high"}, DefaultReasoningEffort: &medium}
	model := makeCodexCatalogModel("openai/reasoner", "Description", entry, caps, 0)
	if model.ContextWindow == nil || *model.ContextWindow != context || !reflect.DeepEqual(model.InputModalities, []string{"text", "image"}) {
		t.Fatalf("wrong context/modalities: %+v", model)
	}
	if model.DefaultReasoningLevel == nil || *model.DefaultReasoningLevel != medium || len(model.SupportedReasoningLevels) != 3 || model.SupportedReasoningLevels[2].Effort != "high" {
		t.Fatalf("wrong effort controls: %+v", model)
	}
	entry.ContextLength = nil
	caps.DefaultReasoningEffort = new("unsupported")
	model = makeCodexCatalogModel("openai/reasoner", "", entry, caps, 0)
	if model.ContextWindow != &fallbackContext || model.DefaultReasoningLevel != nil {
		t.Fatalf("fallback or invalid effort default: %+v", model)
	}
	model = makeCodexCatalogModel("custom/unknown", "", nil, nil, 0)
	if model.ContextWindow != nil || model.DefaultReasoningLevel != nil || model.SupportedReasoningLevels == nil || len(model.SupportedReasoningLevels) != 0 || !reflect.DeepEqual(model.InputModalities, []string{"text"}) {
		t.Fatalf("unknown model invented capabilities: %+v", model)
	}
}

func BenchmarkCatalogExport(b *testing.B) {
	SetLogger(&mockLogger{})
	models := make([]string, 100)
	for i := range models {
		models[i] = fmt.Sprintf("model-%03d", i)
	}
	h := providerHandlerForTest(schemas.OpenAI, nil, models, nil)
	for _, format := range []string{"codex", "claude", "pi", "opencode"} {
		b.Run(format, func(b *testing.B) {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.SetRequestURI("http://localhost:8080/catalog/" + format)
			b.ReportAllocs()
			for b.Loop() {
				h.catalog(ctx)
				ctx.Response.Reset()
			}
		})
	}
}

func catalogModelIDs(t *testing.T, h *ProviderHandler, format, query string) []string {
	t.Helper()
	body := catalogRequest(t, h, "/catalog/"+format+query)
	var response struct {
		Models    json.RawMessage `json:"models"`
		Data      json.RawMessage `json:"data"`
		Providers struct {
			Bifrost struct {
				Models json.RawMessage `json:"models"`
			} `json:"bifrost"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	raw := response.Models
	switch format {
	case "claude":
		raw = response.Data
	case "pi", "opencode":
		raw = response.Providers.Bifrost.Models
	}
	ids := []string{}
	if format == "opencode" {
		var models map[string]json.RawMessage
		if err := json.Unmarshal(raw, &models); err != nil || models == nil {
			t.Fatalf("expected models object: %s (%v)", body, err)
		}
		for id := range models {
			ids = append(ids, id)
		}
	} else {
		var models []struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
		}
		if err := json.Unmarshal(raw, &models); err != nil || models == nil {
			t.Fatalf("expected models array: %s (%v)", body, err)
		}
		for _, model := range models {
			if format == "codex" {
				ids = append(ids, model.Slug)
			} else {
				ids = append(ids, model.ID)
			}
		}
	}
	slices.Sort(ids)
	return ids
}

func TestNativeCatalogCredentials(t *testing.T) {
	SetLogger(&mockLogger{})
	h := providerHandlerForTest(schemas.OpenAI, nil, []string{"model"}, nil)
	for _, format := range []string{"pi", "opencode"} {
		t.Run(format, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.SetRequestURI("/catalog/" + format + "?base_url=https%3A%2F%2Fgateway.example%2Fprefix%2Fopenai%2Fv1")
			ctx.Request.Header.Set("Authorization", "Bearer do-not-export-this-key")
			h.catalog(ctx)
			body := string(ctx.Response.Body())
			if ctx.Response.StatusCode() != 200 || strings.Contains(body, "do-not-export-this-key") || !strings.Contains(body, "https://gateway.example/prefix/openai/v1") || !strings.Contains(body, "BIFROST_VIRTUAL_KEY") {
				t.Fatalf("unsafe or unusable configuration: %s", body)
			}
		})
	}
}

func TestNativeCatalogBaseURL(t *testing.T) {
	SetLogger(&mockLogger{})
	h := providerHandlerForTest(schemas.OpenAI, nil, []string{"model"}, nil)
	for _, format := range []string{"pi", "opencode"} {
		for _, base := range []string{"file:///tmp/socket", "https://user:secret@example.com/v1", "https://example.com/v1?key=secret", "https://example.com/v1#fragment", "/relative"} {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.SetRequestURI("/catalog/" + format)
			ctx.QueryArgs().Set("base_url", base)
			h.catalog(ctx)
			if ctx.Response.StatusCode() != 400 {
				t.Fatalf("%s accepted %s: %s", format, base, ctx.Response.Body())
			}
		}
		body := catalogRequest(t, h, "http://gateway.example:8187/catalog/"+format)
		if !strings.Contains(string(body), "http://gateway.example:8187/openai/v1") {
			t.Fatalf("missing origin inference URL: %s", body)
		}
	}
}

func TestNativeCatalogMetadata(t *testing.T) {
	entry := &modelcatalog.PricingEntry{
		ContextLength: new(64000), MaxInputTokens: new(60000), MaxOutputTokens: new(4096),
		Architecture: &schemas.Architecture{InputModalities: []string{"text", "image"}, OutputModalities: []string{"text"}},
	}
	entry.InputCostPerToken = new(0.000002)
	entry.OutputCostPerToken = new(0.000008)
	entry.CacheReadInputTokenCost = new(0.0000005)
	entry.CacheCreationInputTokenCost = new(0.000003)
	caps := &schemas.ModelCapabilities{SupportsReasoning: new(true), SupportsFunctionCalling: new(false), ReasoningEffortLevels: []string{"none", "low", "high"}}
	for _, format := range []string{"pi", "opencode"} {
		var value any
		if format == "pi" {
			value = makePiCatalogModel("custom/vendor/model", entry, caps)
		} else {
			value = makeOpenCodeCatalogModel("custom/vendor/model", entry, caps)
		}
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var row map[string]any
		if err = json.Unmarshal(body, &row); err != nil {
			t.Fatal(err)
		}
		cost := row["cost"].(map[string]any)
		if cost["input"] != float64(2) || cost["output"] != float64(8) {
			t.Fatalf("wrong price units: %s", body)
		}
		if format == "pi" {
			if row["contextWindow"] != float64(64000) || row["maxTokens"] != float64(4096) || cost["cacheRead"] != 0.5 || cost["cacheWrite"] != float64(3) {
				t.Fatalf("wrong limits/cache prices: %s", body)
			}
			thinking := row["thinkingLevelMap"].(map[string]any)
			if thinking["off"] != "none" || thinking["high"] != "high" || thinking["medium"] != nil {
				t.Fatalf("invented thinking level: %s", body)
			}
		} else {
			limits := row["limit"].(map[string]any)
			cache := cost["cache"].(map[string]any)
			capabilities := row["capabilities"].(map[string]any)
			if limits["context"] != float64(64000) || limits["input"] != float64(60000) || limits["output"] != float64(4096) || cache["read"] != 0.5 || cache["write"] != float64(3) || capabilities["tools"] != false {
				t.Fatalf("wrong native metadata: %s", body)
			}
		}
		if format == "pi" {
			value = makePiCatalogModel("custom/unknown", nil, nil)
		} else {
			value = makeOpenCodeCatalogModel("custom/unknown", nil, nil)
		}
		body, err = json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		row = map[string]any{}
		if err = json.Unmarshal(body, &row); err != nil {
			t.Fatal(err)
		}
		for key := range row {
			if slices.Contains([]string{"cost", "limit", "contextWindow", "maxTokens", "thinkingLevelMap", "reasoning"}, key) {
				t.Fatalf("unknown model invented %s: %s", key, body)
			}
		}
	}
}
