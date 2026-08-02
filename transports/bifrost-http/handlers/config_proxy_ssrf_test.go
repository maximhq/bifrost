package handlers

import (
	"encoding/json"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/valyala/fasthttp"

	"testing"
)

// mockConfigStoreForProxy embeds the interface so unimplemented methods
// panic. The rejected-auth-bypassed path never calls any store method, so
// none need overriding here.
type mockConfigStoreForProxy struct {
	configstore.ConfigStore
}

// TestUpdateProxyConfig_UnauthenticatedWriteRejected proves an unauthenticated
// (default-open, fail-open) caller cannot set the outbound proxy URL at all -
// this capability is at least as sensitive as setting a provider's base URL,
// since an enabled proxy replaces the client Dial function entirely and
// routes every outbound provider request (including its Authorization/
// x-api-key headers) through the attacker-chosen host, public or private.
func TestUpdateProxyConfig_UnauthenticatedWriteRejected(t *testing.T) {
	h := &ConfigHandler{store: &lib.Config{ConfigStore: &mockConfigStoreForProxy{}}}

	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue(schemas.BifrostContextKeyAuthBypassed, true)
	body, _ := json.Marshal(map[string]any{
		"enabled": true,
		"type":    "http",
		"url":     "http://attacker.example:8080",
	})
	ctx.Request.SetBody(body)

	h.updateProxyConfig(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusForbidden {
		t.Fatalf("expected status %d, got %d (body: %s)", fasthttp.StatusForbidden, ctx.Response.StatusCode(), ctx.Response.Body())
	}
}

// TestUpdateProxyConfig_EmptyURLCheckRunsBeforeAuthGate proves the pre-existing
// "URL is required" validation still fires first for a malformed request, so an
// unauthenticated caller gets a plain 400 (not a 403 that would leak whether
// dashboard auth is configured) when the request is simply invalid.
func TestUpdateProxyConfig_EmptyURLCheckRunsBeforeAuthGate(t *testing.T) {
	h := &ConfigHandler{store: &lib.Config{ConfigStore: &mockConfigStoreForProxy{}}}

	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue(schemas.BifrostContextKeyAuthBypassed, true)
	body, _ := json.Marshal(map[string]any{
		"enabled": true,
		"type":    "http",
		"url":     "",
	})
	ctx.Request.SetBody(body)

	h.updateProxyConfig(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusBadRequest {
		t.Fatalf("expected status %d, got %d (body: %s)", fasthttp.StatusBadRequest, ctx.Response.StatusCode(), ctx.Response.Body())
	}
}
