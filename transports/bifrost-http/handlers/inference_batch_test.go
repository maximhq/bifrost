package handlers

import (
	"context"
	"fmt"
	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/jobaccounting"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/plugins/logging"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/integrations"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"time"

	"strings"
	"testing"

	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/valyala/fasthttp"
)

// TestResolveBatchProvider covers the three resolution paths introduced to make
// model optional on POST /v1/batches (OpenAI spec: model lives in the JSONL body).
func TestResolveBatchProvider(t *testing.T) {
	config := &lib.Config{}

	cases := []struct {
		name         string
		model        string
		header       string // x-model-provider; empty = unset
		query        string // ?provider=; empty = unset
		wantProvider string
		wantModel    string
		wantErrMsg   string // non-empty = error expected, substring match
	}{
		{
			name:         "model field: provider+model parsed",
			model:        "openai/gpt-4o-mini",
			wantProvider: "openai",
			wantModel:    "gpt-4o-mini",
		},
		{
			name:         "no model, x-model-provider header",
			header:       "openai",
			wantProvider: "openai",
			wantModel:    "",
		},
		{
			name:         "no model, ?provider= query param",
			query:        "anthropic",
			wantProvider: "anthropic",
			wantModel:    "",
		},
		{
			name:       "no model, no provider → error",
			wantErrMsg: "provider query parameter or x-model-provider header is required",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			if tc.header != "" {
				ctx.Request.Header.Set("x-model-provider", tc.header)
			}
			if tc.query != "" {
				ctx.QueryArgs().Set("provider", tc.query)
			}

			provider, modelName, err := resolveBatchProvider(ctx, config, tc.model)

			if tc.wantErrMsg != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErrMsg)
				}
				if !strings.Contains(err.Error(), tc.wantErrMsg) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErrMsg)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(provider) != tc.wantProvider {
				t.Fatalf("provider = %q, want %q", provider, tc.wantProvider)
			}
			if modelName != tc.wantModel {
				t.Fatalf("modelName = %q, want %q", modelName, tc.wantModel)
			}
		})
	}
}

// A batch create names its work once: an uploaded file, a blob, or inline requests. No provider
// runs inline items beside a file (OpenAI, Azure and Bedrock submit the file and drop them,
// Gemini and Vertex refuse the mix), so the gateway refuses the mix before anything runs rather
// than let a client's inline items vanish.
func TestBatchCreateRefusesFileAndInlineRequestsTogether(t *testing.T) {
	h := &CompletionHandler{config: &lib.Config{}}
	cases := []struct {
		name string
		body string
		want int
	}{
		{"file beside inline requests", `{"model":"openai/gpt-4o-mini","input_file_id":"file-x","endpoint":"/v1/chat/completions","requests":[{"custom_id":"a","body":{"model":"gpt-4o-mini"}}]}`, fasthttp.StatusBadRequest},
		{"blob beside inline requests", `{"model":"azure/gpt-4o-mini","input_blob":"https://blob.example/in.jsonl","endpoint":"/v1/chat/completions","requests":[{"custom_id":"a","body":{"model":"gpt-4o-mini"}}]}`, fasthttp.StatusBadRequest},
		{"nothing named", `{"model":"openai/gpt-4o-mini","endpoint":"/v1/chat/completions"}`, fasthttp.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.Header.SetMethod(fasthttp.MethodPost)
			ctx.Request.Header.SetContentType("application/json")
			ctx.Request.SetBodyString(tc.body)

			h.batchCreate(ctx)

			if ctx.Response.StatusCode() != tc.want {
				t.Fatalf("status = %d, want %d: %s", ctx.Response.StatusCode(), tc.want, ctx.Response.Body())
			}
			if tc.name != "nothing named" && !strings.Contains(string(ctx.Response.Body()), "not both") {
				t.Fatalf("the refusal should say the inputs are exclusive: %s", ctx.Response.Body())
			}
		})
	}
}

// Native batch routes use the same registered credentials on neutral and SDK paths.
type openRouterBatchTestAccount struct{ baseURL string }

func (a openRouterBatchTestAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{schemas.OpenRouter}, nil
}
func (a openRouterBatchTestAccount) GetKeysForProvider(context.Context, schemas.ModelProvider) ([]schemas.Key, error) {
	return []schemas.Key{{ID: "batch-key", Name: "batch-key", Value: *schemas.NewSecretVar("batch-test-secret"), Models: schemas.WhiteList{"*"}, UseForBatchAPI: schemas.Ptr(true), Weight: 1}}, nil
}
func (a openRouterBatchTestAccount) GetConfigForProvider(schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	return &schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: a.baseURL, AllowPrivateNetwork: true, MaxRetries: 0}, ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{Concurrency: 2, BufferSize: 8}}, nil
}

func TestOpenRouterNativeBatchRoutesAndAccounting(t *testing.T) {
	var nextID int
	var ids []string
	var hits []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer batch-test-secret" {
			t.Errorf("operator credential not used")
		}
		id := strings.TrimPrefix(r.URL.Path, "/v1/batches/")
		switch {
		case r.Method == "POST":
			nextID++
			id = fmt.Sprintf("batch-%d", nextID)
			ids = append(ids, id)
			body, _ := io.ReadAll(r.Body)
			if !gjson.GetBytes(body, "provider.only.0").Exists() || gjson.GetBytes(body, "provider.only.0").String() != "openai" {
				t.Errorf("provider preferences lost: %s", body)
			}
			w.WriteHeader(202)
			fmt.Fprintf(w, `{"id":%q,"object":"batch","endpoint":"/v1/chat/completions","model":"openai/gpt-4o","status":"validating","request_counts":{"total":1,"completed":0,"failed":0}}`, id)
		case r.Method == "DELETE":
			fmt.Fprintf(w, `{"id":%q,"object":"batch","deletion":{"openrouter":"deleted"}}`, id)
		default:
			if id == "batch-no-charge" {
				fmt.Fprint(w, `{"id":"batch-no-charge","model":"openai/gpt-4o","status":"completed","request_counts":{"total":1,"completed":1,"failed":0},"results":[{"custom_id":"a","response":{"status_code":200}}],"usage":{"total_tokens":30}}`)
				return
			}
			fmt.Fprintf(w, `{"id":%q,"object":"batch","endpoint":"/v1/chat/completions","model":"openai/gpt-4o","status":"completed","request_counts":{"total":1,"completed":1,"failed":0},"usage":{"prompt_tokens":20,"completion_tokens":10,"total_tokens":30,"cost":0.123,"is_byok":true},"results":[{"custom_id":"request-1","response":{"status_code":200,"body":{"choices":[{"message":{"content":"done"}}]}}}]}`, id)
		}
	}))
	defer upstream.Close()
	logger := bifrost.NewNoOpLogger()
	state, e := configstore.NewConfigStore(t.Context(), &configstore.Config{Enabled: true, Type: configstore.ConfigStoreTypeSQLite, Config: &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "config.db")}}, logger)
	require.NoError(t, e)
	defer state.Close(context.Background())
	logs, e := logstore.NewLogStore(t.Context(), &logstore.Config{Enabled: true, Type: logstore.LogStoreTypeSQLite, Config: &logstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "logs.db")}}, logger)
	require.NoError(t, e)
	defer logs.Close(context.Background())
	catalog := modelcatalog.NewTestCatalogWithDatasheet(datasheet.New(nil, logger, datasheet.Config{}))
	logPlugin, e := logging.Init(t.Context(), &logging.Config{}, logger, logs, state, catalog, nil)
	require.NoError(t, e)
	budget := configtables.TableBudget{ID: "batch-budget", MaxLimit: 10, ResetDuration: "1d", LastReset: time.Now(), VirtualKeyID: schemas.Ptr("vk-owner")}
	governanceConfig := &configstore.GovernanceConfig{Budgets: []configtables.TableBudget{budget}, VirtualKeys: []configtables.TableVirtualKey{
		{ID: "vk-owner", Name: "owner", Value: *schemas.NewSecretVar("sk-bf-owner"), IsActive: schemas.Ptr(true), Budgets: []configtables.TableBudget{budget}, ProviderConfigs: []configtables.TableVirtualKeyProviderConfig{{Provider: "openrouter", AllowedModels: schemas.WhiteList{"*"}, AllowAllKeys: true}}},
		{ID: "vk-other", Name: "other", Value: *schemas.NewSecretVar("sk-bf-other"), IsActive: schemas.Ptr(true), ProviderConfigs: []configtables.TableVirtualKeyProviderConfig{{Provider: "openrouter", AllowedModels: schemas.WhiteList{"*"}, AllowAllKeys: true}}},
	}}
	local, e := governance.NewLocalGovernanceStore(t.Context(), logger, nil, governanceConfig, nil, nil)
	require.NoError(t, e)
	gov, e := governance.InitFromStore(t.Context(), &governance.Config{}, logger, local, state, nil, nil, nil)
	require.NoError(t, e)
	logPlugin.SetBatchUsageReporter(gov)
	client, e := bifrost.Init(t.Context(), schemas.BifrostConfig{Account: openRouterBatchTestAccount{baseURL: upstream.URL}, Logger: logger, LLMPlugins: []schemas.LLMPlugin{logPlugin, gov}})
	require.NoError(t, e)
	defer client.Shutdown()
	cfg := &lib.Config{ClientConfig: &configstore.ClientConfig{}}
	routes := router.New()
	integrations.NewOpenAIRouter(client, cfg, nil, logger).RegisterRoutes(routes)
	NewInferenceHandler(nil, client, cfg).RegisterRoutes(routes)
	invoke := func(method, uri, body, credential string) *fasthttp.RequestCtx {
		ctx := &fasthttp.RequestCtx{}
		ctx.Request.Header.SetMethod(method)
		ctx.Request.SetRequestURI(uri)
		ctx.Request.Header.SetContentType("application/json")
		ctx.Request.Header.Set("x-bf-vk", credential)
		ctx.Request.Header.Set("x-bf-passthrough-extra-params", "true")
		ctx.Request.SetBodyString(body)
		routes.Handler(ctx)
		return ctx
	}
	for _, prefix := range []string{"", "/openai"} {
		t.Run(prefix, func(t *testing.T) {
			body := `{"model":"openrouter/openai/gpt-4o","endpoint":"/v1/chat/completions","provider":{"only":["openai"]},"requests":[{"custom_id":"request-1","body":{"messages":[{"role":"user","content":"hello"}]}}]}`
			if prefix != "" {
				body = `{"provider":"openrouter","model":"openai/gpt-4o","endpoint":"/v1/chat/completions","extra_params":{"provider":{"only":["openai"]}},"requests":[{"custom_id":"request-1","body":{"messages":[{"role":"user","content":"hello"}]}}]}`
			}
			created := invoke("POST", prefix+"/v1/batches", body, "sk-bf-owner")
			require.Equal(t, 200, created.Response.StatusCode(), string(created.Response.Body()))
			id := gjson.GetBytes(created.Response.Body(), "id").String()
			require.NotEmpty(t, id)
			before := len(hits)
			foreign := invoke("GET", prefix+"/v1/batches/"+id+"?provider=openrouter", "", "sk-bf-other")
			require.Equal(t, 404, foreign.Response.StatusCode(), string(foreign.Response.Body()))
			require.Len(t, hits, before, "ownership is checked before reaching the shared upstream")
			for _, suffix := range []string{"", "", "/results", "/results"} {
				got := invoke("GET", prefix+"/v1/batches/"+id+suffix+"?provider=openrouter", "", "sk-bf-owner")
				require.Equal(t, 200, got.Response.StatusCode(), string(got.Response.Body()))
				require.Equal(t, "request-1", gjson.GetBytes(got.Response.Body(), "results.0.custom_id").String())
			}
			removed := invoke("DELETE", prefix+"/v1/batches/"+id+"?provider=openrouter", "", "sk-bf-owner")
			require.Equal(t, 200, removed.Response.StatusCode(), string(removed.Response.Body()))
			entry, e := logs.FindByID(t.Context(), jobaccounting.AccountingLogID(jobaccounting.ProviderJobKindBatch, schemas.OpenRouter, id))
			require.NoError(t, e)
			require.NotNil(t, entry.Cost)
			require.Equal(t, 0.123, *entry.Cost)
		})
	}
	blocked := invoke("DELETE", "/v1/batches/batch-no-charge?provider=openrouter", "", "sk-bf-owner")
	require.Equal(t, 503, blocked.Response.StatusCode(), string(blocked.Response.Body()))
	for _, hit := range hits {
		require.NotEqual(t, "DELETE /v1/batches/batch-no-charge", hit, "pending accounting must not destroy the only settlement input")
	}
	// Both batches were settled once despite repeated polling, downloads and the
	// deletion preflight, and billing stayed on the original creating virtual key.
	charged := gov.GetGovernanceStore().GetGovernanceData(t.Context()).Budgets["batch-budget"]
	require.NotNil(t, charged)
	require.InDelta(t, 0.246, charged.CurrentUsage, 1e-12)
	for _, hit := range hits {
		require.NotContains(t, hit, "/results")
		require.NotContains(t, hit, "/files")
	}
	require.Len(t, ids, 2)
}
