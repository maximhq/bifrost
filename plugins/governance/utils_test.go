package governance

import (
	"errors"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// A virtual key presented via Azure's native "api-key" header (used by the
// Azure OpenAI SDK on passthrough) must be parsed the same way as the HTTP
// transport context extractor.
func TestParseVirtualKeyFromFastHTTPRequest_VirtualKeyFromAzureAPIKeyHeader(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("api-key", "sk-bf-azure-passthrough-vk")

	vk := ParseVirtualKeyFromFastHTTPRequest(ctx)
	if vk == nil || *vk != "sk-bf-azure-passthrough-vk" {
		t.Fatalf("virtual key = %#v, want %q", vk, "sk-bf-azure-passthrough-vk")
	}
}

// A real (non-VK) provider key in the "api-key" header must not be misread as
// a virtual key — only the sk-bf- prefix promotes it.
func TestParseVirtualKeyFromFastHTTPRequest_APIKeyHeaderNonVirtualKeyIgnored(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("api-key", "real-azure-api-key")

	if vk := ParseVirtualKeyFromFastHTTPRequest(ctx); vk != nil {
		t.Fatalf("virtual key should not be set from a non-VK api-key value, got %#v", *vk)
	}
}

// A virtual key in Ocp-Apim-Subscription-Key, the header Azure AI Speech clients
// send, must be parsed the same way as the HTTP transport context extractor.
func TestParseVirtualKeyFromFastHTTPRequest_VirtualKeyFromOcpApimSubscriptionKeyHeader(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Ocp-Apim-Subscription-Key", "sk-bf-azure-speech-vk")

	vk := ParseVirtualKeyFromFastHTTPRequest(ctx)
	if vk == nil || *vk != "sk-bf-azure-speech-vk" {
		t.Fatalf("virtual key = %#v, want %q", vk, "sk-bf-azure-speech-vk")
	}
}

// A real Azure Speech resource key in Ocp-Apim-Subscription-Key must not be
// misread as a virtual key.
func TestParseVirtualKeyFromFastHTTPRequest_OcpApimSubscriptionKeyNonVirtualKeyIgnored(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.Set("Ocp-Apim-Subscription-Key", "real-azure-speech-key")

	if vk := ParseVirtualKeyFromFastHTTPRequest(ctx); vk != nil {
		t.Fatalf("virtual key should not be set from a non-VK Ocp-Apim-Subscription-Key value, got %#v", *vk)
	}
}

// csvModelListFilter stands in for a provider's list format: the body is comma-separated model names.
func csvModelListFilter(body []byte, keep func(string) bool) ([]byte, error) {
	if string(body) == "malformed" {
		return nil, errors.New("malformed")
	}
	var kept []string
	for _, m := range strings.Split(string(body), ",") {
		if keep(m) {
			kept = append(kept, m)
		}
	}
	return []byte(strings.Join(kept, ",")), nil
}

func modelListResponse(status int, body string) *schemas.BifrostPassthroughResponse {
	return &schemas.BifrostPassthroughResponse{
		StatusCode:  status,
		Headers:     map[string]string{"Etag": `"abc"`, "Content-Type": "application/json"},
		Body:        []byte(body),
		ExtraFields: schemas.BifrostResponseExtraFields{ProviderResponseHeaders: map[string]string{"Etag": `"abc"`}},
	}
}

// A passthrough model list is trimmed by the same access the responses turns are checked against.
func TestFilterPassthroughModelList(t *testing.T) {
	vk := buildVirtualKeyWithProviders("vk1", "sk-bf-test", "Test VK", []configstoreTables.TableVirtualKeyProviderConfig{
		{Provider: "openai", AllowedModels: schemas.WhiteList{"gpt-6.1-sol", "regex:gpt-5\\.6-.*"}},
	})
	plugin := newAccessTestPlugin(t, vk, nil)
	const list = "gpt-6.1-sol,gpt-6-astra,gpt-5.6-terra,codex-auto-review"

	run := func(ctx *schemas.BifrostContext, resp *schemas.BifrostPassthroughResponse) {
		access, err := plugin.ResolveAccess(ctx)
		require.NoError(t, err)
		plugin.filterPassthroughModelList(ctx, access, "openai", resp)
	}

	t.Run("keeps allowed models and drops the upstream etag", func(t *testing.T) {
		ctx := presentCtx("sk-bf-test")
		ctx.SetValue(schemas.BifrostContextKeyPassthroughModelListFilter, schemas.PassthroughModelListFilter(csvModelListFilter))
		resp := modelListResponse(200, list)
		run(ctx, resp)
		assert.Equal(t, "gpt-6.1-sol,gpt-5.6-terra", string(resp.Body))
		assert.Equal(t, map[string]string{"Content-Type": "application/json"}, resp.Headers)
		assert.Empty(t, resp.ExtraFields.ProviderResponseHeaders)
	})

	t.Run("a credential that resolves to nothing lists nothing", func(t *testing.T) {
		ctx := presentCtx("sk-bf-unknown")
		ctx.SetValue(schemas.BifrostContextKeyPassthroughModelListFilter, schemas.PassthroughModelListFilter(csvModelListFilter))
		resp := modelListResponse(200, list)
		run(ctx, resp)
		assert.Empty(t, string(resp.Body))
	})

	t.Run("untouched without a registered filter", func(t *testing.T) {
		resp := modelListResponse(200, list)
		run(presentCtx("sk-bf-test"), resp)
		assert.Equal(t, list, string(resp.Body))
		assert.Contains(t, resp.Headers, "Etag")
	})

	t.Run("untouched on an upstream error", func(t *testing.T) {
		ctx := presentCtx("sk-bf-test")
		ctx.SetValue(schemas.BifrostContextKeyPassthroughModelListFilter, schemas.PassthroughModelListFilter(csvModelListFilter))
		resp := modelListResponse(401, list)
		run(ctx, resp)
		assert.Equal(t, list, string(resp.Body))
	})

	t.Run("fails open when the body cannot be read", func(t *testing.T) {
		ctx := presentCtx("sk-bf-test")
		ctx.SetValue(schemas.BifrostContextKeyPassthroughModelListFilter, schemas.PassthroughModelListFilter(csvModelListFilter))
		resp := modelListResponse(200, "malformed")
		run(ctx, resp)
		assert.Equal(t, "malformed", string(resp.Body))
		assert.Contains(t, resp.Headers, "Etag")
	})
}
