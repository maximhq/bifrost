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
