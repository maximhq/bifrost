package azure

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestAzureChatCompletionURL pins that an On Your Data request (data_sources) reaches the
// classic deployments route, which runs the data source, instead of the v1 surface, which
// ignores the field and answers 200. Every other chat request stays on v1.
func TestAzureChatCompletionURL(t *testing.T) {
	t.Parallel()

	const endpoint = "https://my-resource.openai.azure.com"
	cases := []struct {
		name    string
		request *schemas.BifrostChatRequest
		want    string
	}{
		{
			name:    "no params stays on v1",
			request: &schemas.BifrostChatRequest{Model: "gpt-4o"},
			want:    endpoint + "/openai/v1/chat/completions",
		},
		{
			name:    "other extra params stay on v1",
			request: &schemas.BifrostChatRequest{Model: "gpt-4o", Params: &schemas.ChatParameters{ExtraParams: map[string]interface{}{"x_future_field": 1}}},
			want:    endpoint + "/openai/v1/chat/completions",
		},
		{
			name:    "data_sources goes to the deployment with an api-version",
			request: &schemas.BifrostChatRequest{Model: "gpt-4o", Params: &schemas.ChatParameters{ExtraParams: map[string]interface{}{"data_sources": []interface{}{map[string]interface{}{"type": "azure_search"}}}}},
			want:    endpoint + "/openai/deployments/gpt-4o/chat/completions?api-version=" + DefaultAzureAPIVersion,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := azureChatCompletionURL(nil, endpoint, tc.request); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestAzureChatCompletionURLKeepsCallerAPIVersion pins the api-version an On Your Data request
// sends to the deployments route: the version the caller put on the Azure SDK route wins, then an
// alias's configured version, then Bifrost's default. Sending Bifrost's own version silently moved
// the caller onto a different Azure API contract. The caller's value is escaped into the query.
func TestAzureChatCompletionURLKeepsCallerAPIVersion(t *testing.T) {
	const endpoint = "https://my-resource.openai.azure.com"
	onYourData := &schemas.BifrostChatRequest{Model: "gpt-4o", Params: &schemas.ChatParameters{ExtraParams: map[string]interface{}{"data_sources": []interface{}{}}}}
	plain := &schemas.BifrostChatRequest{Model: "gpt-4o"}
	aliasVersion := "2024-08-01-preview"
	withAlias := func(ctx *schemas.BifrostContext) *schemas.BifrostContext {
		ctx.SetValue(schemas.BifrostContextKeyResolvedAlias, &schemas.ResolvedAlias{Key: "gpt-4o", Config: &schemas.AliasConfig{AzureAliasCfg: &schemas.AzureAliasCfg{APIVersion: &aliasVersion}}})
		return ctx
	}
	newCtx := func(callerVersion string) *schemas.BifrostContext {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		if callerVersion != "" {
			ctx.SetValue(schemas.BifrostContextKeyAzureDeploymentAPIVersion, callerVersion)
		}
		return ctx
	}
	deployments := endpoint + "/openai/deployments/gpt-4o/chat/completions?api-version="
	cases := []struct {
		name    string
		ctx     *schemas.BifrostContext
		request *schemas.BifrostChatRequest
		want    string
	}{
		{"caller version", newCtx("2024-10-21"), onYourData, deployments + "2024-10-21"},
		{"caller version beats the alias", withAlias(newCtx("2024-10-21")), onYourData, deployments + "2024-10-21"},
		{"alias version without a caller version", withAlias(newCtx("")), onYourData, deployments + aliasVersion},
		{"default without either", newCtx(""), onYourData, deployments + DefaultAzureAPIVersion},
		{"caller version is escaped", newCtx("2024-10-21&x=1"), onYourData, deployments + "2024-10-21%26x%3D1"},
		{"v1 requests carry no api-version", newCtx("2024-10-21"), plain, endpoint + "/openai/v1/chat/completions"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := azureChatCompletionURL(tc.ctx, endpoint, tc.request); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// BenchmarkAzureChatCompletionURL measures the URL choice on the common path (no data_sources).
func BenchmarkAzureChatCompletionURL(b *testing.B) {
	const endpoint = "https://my-resource.openai.azure.com"
	request := &schemas.BifrostChatRequest{Model: "gpt-4o", Params: &schemas.ChatParameters{}}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = azureChatCompletionURL(nil, endpoint, request)
	}
}
