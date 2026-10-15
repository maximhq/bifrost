package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// newManagedClientsHandler returns a handler over a real sqlite store holding one active VK.
func newManagedClientsHandler(t *testing.T, resolver OAuth2IdentityResolver) (*OAuth2ManagedClientsHandler, string) {
	t.Helper()
	SetLogger(&mockLogger{})
	store := newRealOAuth2Store(t)
	active := true
	vk := &configtables.TableVirtualKey{ID: "vk-managed", Name: "managed", Value: *schemas.NewSecretVar("sk-bf-managed"), IsActive: &active}
	require.NoError(t, store.CreateVirtualKey(context.Background(), vk))
	cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeBoth, false)
	return NewOAuth2ManagedClientsHandler(cfg, resolver), vk.ID
}

// jsonCtx builds an initialized request with a JSON body and optional {id} path value.
func jsonCtx(method, body, id string) *fasthttp.RequestCtx {
	var req fasthttp.Request
	req.Header.SetMethod(method)
	req.Header.SetContentType("application/json")
	req.SetBodyString(body)
	ctx := initCtx(&req)
	if id != "" {
		ctx.SetUserValue("id", id)
	}
	return ctx
}

// decodeManagedClient parses a managed-client response body.
func decodeManagedClient(t *testing.T, ctx *fasthttp.RequestCtx) managedClientView {
	t.Helper()
	var v managedClientView
	require.NoError(t, json.Unmarshal(ctx.Response.Body(), &v), string(ctx.Response.Body()))
	return v
}

func TestManagedClients_CreateConfidentialReturnsSecretOnce(t *testing.T) {
	h, vkID := newManagedClientsHandler(t, nil)
	ctx := jsonCtx("POST", `{"name":"Claude","identity":"vk","virtual_key_id":"`+vkID+`"}`, "")
	h.create(ctx)
	require.Equal(t, fasthttp.StatusCreated, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	created := decodeManagedClient(t, ctx)
	assert.True(t, created.HasSecret)
	assert.NotEmpty(t, created.ClientSecret)
	assert.Contains(t, created.ClientID, managedClientIDPrefix)

	stored, err := h.store.ConfigStore.GetOAuth2ManagedClientByClientID(context.Background(), created.ClientID)
	require.NoError(t, err)
	assert.Equal(t, hashSHA256Hex(created.ClientSecret), stored.ClientSecretHash, "only the hash is stored")

	list := jsonCtx("GET", "", "")
	h.list(list)
	assert.NotContains(t, string(list.Response.Body()), created.ClientSecret)
	assert.NotContains(t, string(list.Response.Body()), stored.ClientSecretHash)
}

func TestManagedClients_CreatePublicWithURLClientID(t *testing.T) {
	h, vkID := newManagedClientsHandler(t, nil)
	cimd := "https://claude.ai/oauth/claude-code-client-metadata"
	ctx := jsonCtx("POST", `{"client_id":"`+cimd+`","public":true,"identity":"vk","virtual_key_id":"`+vkID+`"}`, "")
	h.create(ctx)
	require.Equal(t, fasthttp.StatusCreated, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	created := decodeManagedClient(t, ctx)
	assert.Equal(t, cimd, created.ClientID)
	assert.False(t, created.HasSecret)
	assert.Empty(t, created.ClientSecret)

	dup := jsonCtx("POST", `{"client_id":"`+cimd+`","public":true,"identity":"vk","virtual_key_id":"`+vkID+`"}`, "")
	h.create(dup)
	assert.Equal(t, fasthttp.StatusConflict, dup.Response.StatusCode())
}

func TestManagedClients_Validation(t *testing.T) {
	h, vkID := newManagedClientsHandler(t, nil)
	for name, body := range map[string]string{
		"missing vk":            `{"identity":"vk"}`,
		"unknown vk":            `{"identity":"vk","virtual_key_id":"nope"}`,
		"user without resolver": `{"identity":"user"}`,
		"bad identity":          `{"identity":"session","virtual_key_id":"` + vkID + `"}`,
		"whitespace client_id":  `{"client_id":"a b","identity":"vk","virtual_key_id":"` + vkID + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			ctx := jsonCtx("POST", body, "")
			h.create(ctx)
			assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode(), string(ctx.Response.Body()))
		})
	}
}

func TestManagedClients_UserIdentityNeedsResolver(t *testing.T) {
	h, _ := newManagedClientsHandler(t, &fakeResolver{userModeAvailable: true})
	ctx := jsonCtx("POST", `{"identity":"user","virtual_key_id":"ignored"}`, "")
	h.create(ctx)
	require.Equal(t, fasthttp.StatusCreated, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	assert.Empty(t, decodeManagedClient(t, ctx).VirtualKeyID, "a user-identity client carries no VK binding")
}

func TestManagedClients_UpdateRotateDelete(t *testing.T) {
	h, vkID := newManagedClientsHandler(t, nil)
	create := jsonCtx("POST", `{"public":true,"identity":"vk","virtual_key_id":"`+vkID+`"}`, "")
	h.create(create)
	id := decodeManagedClient(t, create).ID

	patch := jsonCtx("PATCH", `{"name":"renamed","disabled":true}`, id)
	h.update(patch)
	require.Equal(t, fasthttp.StatusOK, patch.Response.StatusCode(), string(patch.Response.Body()))
	updated := decodeManagedClient(t, patch)
	assert.Equal(t, "renamed", updated.Name)
	assert.True(t, updated.Disabled)

	// A key that dies after binding must not block disabling or renaming the
	// client, but re-binding still needs a live key.
	require.NoError(t, h.store.ConfigStore.DeleteVirtualKey(context.Background(), vkID))
	deadKey := jsonCtx("PATCH", `{"name":"still renamable","disabled":false}`, id)
	h.update(deadKey)
	require.Equal(t, fasthttp.StatusOK, deadKey.Response.StatusCode(), string(deadKey.Response.Body()))
	assert.Equal(t, "still renamable", decodeManagedClient(t, deadKey).Name)
	rebind := jsonCtx("PATCH", `{"virtual_key_id":"vk-gone"}`, id)
	h.update(rebind)
	assert.Equal(t, fasthttp.StatusBadRequest, rebind.Response.StatusCode(), string(rebind.Response.Body()))

	rotate := jsonCtx("POST", "", id)
	h.rotateSecret(rotate)
	require.Equal(t, fasthttp.StatusOK, rotate.Response.StatusCode())
	rotated := decodeManagedClient(t, rotate)
	assert.True(t, rotated.HasSecret)
	assert.NotEmpty(t, rotated.ClientSecret)

	del := jsonCtx("DELETE", "", id)
	h.delete(del)
	assert.Equal(t, fasthttp.StatusOK, del.Response.StatusCode())
	missing := jsonCtx("PATCH", `{"name":"x"}`, id)
	h.update(missing)
	assert.Equal(t, fasthttp.StatusNotFound, missing.Response.StatusCode())
}

func TestManagedClients_WritesRefusedWhenAuthBypassed(t *testing.T) {
	h, vkID := newManagedClientsHandler(t, nil)
	ctx := jsonCtx("POST", `{"identity":"vk","virtual_key_id":"`+vkID+`"}`, "")
	ctx.SetUserValue(schemas.BifrostContextKeyAuthBypassed, true)
	h.create(ctx)
	assert.Equal(t, fasthttp.StatusForbidden, ctx.Response.StatusCode())
}

// suggestingResolver offers a trusted-issuer hint, as the enterprise resolver does.
type suggestingResolver struct{ fakeResolver }

// SuggestedManagedAuthIssuer returns a fixed Okta org issuer.
func (suggestingResolver) SuggestedManagedAuthIssuer(context.Context) string { return "https://acme.okta.com" }

func TestManagedClients_ListCarriesIssuerSuggestion(t *testing.T) {
	h, _ := newManagedClientsHandler(t, &suggestingResolver{})
	ctx := jsonCtx("GET", "", "")
	h.list(ctx)
	var body map[string]any
	require.NoError(t, json.Unmarshal(ctx.Response.Body(), &body))
	assert.Equal(t, "https://acme.okta.com", body["suggested_trusted_issuer"])

	plain, _ := newManagedClientsHandler(t, nil)
	ctx = jsonCtx("GET", "", "")
	plain.list(ctx)
	assert.NotContains(t, string(ctx.Response.Body()), "suggested_trusted_issuer")
}
