package governance

import (
	"testing"

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
