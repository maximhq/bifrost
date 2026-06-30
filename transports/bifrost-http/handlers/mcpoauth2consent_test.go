package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// fakeResolver is a configurable OAuth2IdentityResolver for consent tests.
type fakeResolver struct {
	userModeAvailable bool
	userID            string
	name              string
	resolveErr        error
	vkBoundUserID     string
	vkBindErr         error
	userVKID          string
	userVKErr         error
}

func (f *fakeResolver) IsUserModeAvailable() bool { return f.userModeAvailable }
func (f *fakeResolver) ResolveUserIdentity(_ *fasthttp.RequestCtx) (string, string, error) {
	return f.userID, f.name, f.resolveErr
}
func (f *fakeResolver) ResolveVKUserUpgrade(_ context.Context, _ string) (string, error) {
	return f.vkBoundUserID, f.vkBindErr
}
func (f *fakeResolver) ResolveUserVirtualKey(_ context.Context, _ string) (string, error) {
	return f.userVKID, f.userVKErr
}

func newConsentStore() *mockOAuth2Store {
	return &mockOAuth2Store{
		vksByValue: map[string]*configtables.TableVirtualKey{},
		clients: map[string]*configtables.TableOAuth2Client{
			"client-1": {ClientID: "client-1", ClientName: "Test Client"},
		},
		authReqs: map[string]*configtables.TableOAuth2AuthorizeRequest{},
	}
}

func seedPendingFlow(store *mockOAuth2Store, id string, expires time.Time) {
	store.authReqs[id] = &configtables.TableOAuth2AuthorizeRequest{
		ID:          id,
		ClientID:    "client-1",
		RedirectURI: "http://127.0.0.1/cb",
		State:       "st",
		Status:      configtables.OAuth2AuthorizeRequestStatusPending,
		ExpiresAt:   expires,
	}
}

func newConsentHandler(store *mockOAuth2Store, resolver OAuth2IdentityResolver, enforceAuth bool) *OAuth2ConsentHandler {
	SetLogger(&mockLogger{})
	cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeBoth, enforceAuth)
	return NewOAuth2ConsentHandler(cfg, nil, resolver)
}

func consentCtx(flowID, body string) *fasthttp.RequestCtx {
	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue("id", flowID)
	if body != "" {
		ctx.Request.SetBody([]byte(body))
	}
	return ctx
}

func TestConsentFlowDetail(t *testing.T) {
	t.Run("pending flow returns client and modes", func(t *testing.T) {
		store := newConsentStore()
		seedPendingFlow(store, "flow-1", time.Now().Add(time.Minute))
		h := newConsentHandler(store, nil, false)
		ctx := consentCtx("flow-1", "")
		h.flowDetail(ctx)
		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())

		var resp consentFlowDetailResponse
		require.NoError(t, json.Unmarshal(ctx.Response.Body(), &resp))
		assert.Equal(t, "Test Client", resp.ClientName)
		assert.Equal(t, []consentFlowMode{consentFlowModeVK, consentFlowModeSession}, resp.AvailableModes)
	})

	t.Run("missing flow returns 404", func(t *testing.T) {
		h := newConsentHandler(newConsentStore(), nil, false)
		ctx := consentCtx("nope", "")
		h.flowDetail(ctx)
		assert.Equal(t, fasthttp.StatusNotFound, ctx.Response.StatusCode())
	})

	t.Run("empty flow id returns 400", func(t *testing.T) {
		h := newConsentHandler(newConsentStore(), nil, false)
		ctx := consentCtx("", "")
		h.flowDetail(ctx)
		assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
	})

	t.Run("expired flow returns 410", func(t *testing.T) {
		store := newConsentStore()
		seedPendingFlow(store, "flow-1", time.Now().Add(-time.Minute))
		h := newConsentHandler(store, nil, false)
		ctx := consentCtx("flow-1", "")
		h.flowDetail(ctx)
		assert.Equal(t, fasthttp.StatusGone, ctx.Response.StatusCode())
	})

	t.Run("already-consented flow returns 410", func(t *testing.T) {
		store := newConsentStore()
		seedPendingFlow(store, "flow-1", time.Now().Add(time.Minute))
		store.authReqs["flow-1"].Status = configtables.OAuth2AuthorizeRequestStatusConsented
		h := newConsentHandler(store, nil, false)
		ctx := consentCtx("flow-1", "")
		h.flowDetail(ctx)
		assert.Equal(t, fasthttp.StatusGone, ctx.Response.StatusCode())
	})
}

func TestConsentAvailableModes(t *testing.T) {
	cases := []struct {
		name        string
		resolver    OAuth2IdentityResolver
		enforceAuth bool
		disableVK   bool
		want        []consentFlowMode
	}{
		{"vk and session when auth not enforced", nil, false, false, []consentFlowMode{consentFlowModeVK, consentFlowModeSession}},
		{"vk only when auth enforced", nil, true, false, []consentFlowMode{consentFlowModeVK}},
		{"adds user when resolver offers it", &fakeResolver{userModeAvailable: true}, false, false, []consentFlowMode{consentFlowModeVK, consentFlowModeSession, consentFlowModeUser}},
		// DisableVKIdentity drops vk, but only when user mode is available so the
		// flow always keeps a usable identity path.
		{"disable vk drops vk when user mode available", &fakeResolver{userModeAvailable: true}, false, true, []consentFlowMode{consentFlowModeSession, consentFlowModeUser}},
		{"disable vk leaves user-only when auth enforced", &fakeResolver{userModeAvailable: true}, true, true, []consentFlowMode{consentFlowModeUser}},
		{"disable vk ignored without user mode", nil, false, true, []consentFlowMode{consentFlowModeVK, consentFlowModeSession}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newConsentHandler(newConsentStore(), tc.resolver, tc.enforceAuth)
			h.store.ClientConfig.OAuth2ServerConfig.DisableVKIdentity = tc.disableVK
			assert.Equal(t, tc.want, h.availableModes())
		})
	}
}

func TestConsentFlowSubmit_VK(t *testing.T) {
	activeVK := &configtables.TableVirtualKey{ID: "vk-row-1", Value: "sk-bf-active", IsActive: new(true)}
	inactiveVK := &configtables.TableVirtualKey{ID: "vk-row-2", Value: "sk-bf-inactive", IsActive: new(false)}

	t.Run("active VK mints a code", func(t *testing.T) {
		store := newConsentStore()
		store.vksByValue["sk-bf-active"] = activeVK
		seedPendingFlow(store, "flow-1", time.Now().Add(time.Minute))
		h := newConsentHandler(store, nil, false)
		ctx := consentCtx("flow-1", `{"mode":"vk","value":"sk-bf-active"}`)
		h.flowSubmit(ctx)
		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))

		var resp consentFlowSubmitResponse
		require.NoError(t, json.Unmarshal(ctx.Response.Body(), &resp))
		assert.Contains(t, resp.RedirectURL, "code=")
		assert.Contains(t, resp.RedirectURL, "state=st")
		// The flow is now consented and bound to the VK row id.
		assert.Equal(t, "vk", store.authReqs["flow-1"].BfMode)
		assert.Equal(t, "vk-row-1", store.authReqs["flow-1"].BfSub)
	})

	t.Run("inactive VK is rejected", func(t *testing.T) {
		store := newConsentStore()
		store.vksByValue[inactiveVK.Value] = inactiveVK
		seedPendingFlow(store, "flow-1", time.Now().Add(time.Minute))
		h := newConsentHandler(store, nil, false)
		ctx := consentCtx("flow-1", `{"mode":"vk","value":"sk-bf-inactive"}`)
		h.flowSubmit(ctx)
		assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
	})

	t.Run("unknown VK is rejected", func(t *testing.T) {
		store := newConsentStore()
		seedPendingFlow(store, "flow-1", time.Now().Add(time.Minute))
		h := newConsentHandler(store, nil, false)
		ctx := consentCtx("flow-1", `{"mode":"vk","value":"sk-bf-missing"}`)
		h.flowSubmit(ctx)
		assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
	})

	t.Run("empty VK value is rejected", func(t *testing.T) {
		store := newConsentStore()
		seedPendingFlow(store, "flow-1", time.Now().Add(time.Minute))
		h := newConsentHandler(store, nil, false)
		ctx := consentCtx("flow-1", `{"mode":"vk","value":""}`)
		h.flowSubmit(ctx)
		assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
	})

	t.Run("double submit returns 410 on the second attempt", func(t *testing.T) {
		store := newConsentStore()
		store.vksByValue[activeVK.Value] = activeVK
		seedPendingFlow(store, "flow-1", time.Now().Add(time.Minute))
		h := newConsentHandler(store, nil, false)

		first := consentCtx("flow-1", `{"mode":"vk","value":"sk-bf-active"}`)
		h.flowSubmit(first)
		require.Equal(t, fasthttp.StatusOK, first.Response.StatusCode())

		second := consentCtx("flow-1", `{"mode":"vk","value":"sk-bf-active"}`)
		h.flowSubmit(second)
		assert.Equal(t, fasthttp.StatusGone, second.Response.StatusCode())
	})
}

func TestConsentFlowSubmit_Session(t *testing.T) {
	t.Run("session mode mints a server-side token when auth not enforced", func(t *testing.T) {
		store := newConsentStore()
		seedPendingFlow(store, "flow-1", time.Now().Add(time.Minute))
		h := newConsentHandler(store, nil, false)
		ctx := consentCtx("flow-1", `{"mode":"session"}`)
		h.flowSubmit(ctx)
		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
		assert.Equal(t, "session", store.authReqs["flow-1"].BfMode)
		assert.NotEmpty(t, store.authReqs["flow-1"].BfSub) // server-minted, not client-asserted
	})

	t.Run("session mode is unavailable when auth is enforced", func(t *testing.T) {
		store := newConsentStore()
		seedPendingFlow(store, "flow-1", time.Now().Add(time.Minute))
		h := newConsentHandler(store, nil, true)
		ctx := consentCtx("flow-1", `{"mode":"session"}`)
		h.flowSubmit(ctx)
		assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
		assert.Contains(t, string(ctx.Response.Body()), "not available")
	})
}

func TestConsentFlowSubmit_User(t *testing.T) {
	t.Run("user mode rejected when no resolver (mode not offered)", func(t *testing.T) {
		store := newConsentStore()
		seedPendingFlow(store, "flow-1", time.Now().Add(time.Minute))
		h := newConsentHandler(store, nil, false)
		ctx := consentCtx("flow-1", `{"mode":"user"}`)
		h.flowSubmit(ctx)
		assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
	})

	t.Run("resolved session yields user mode", func(t *testing.T) {
		store := newConsentStore()
		seedPendingFlow(store, "flow-1", time.Now().Add(time.Minute))
		h := newConsentHandler(store, &fakeResolver{userModeAvailable: true, userID: "user-1", name: "Alice"}, false)
		ctx := consentCtx("flow-1", `{"mode":"user"}`)
		h.flowSubmit(ctx)
		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
		assert.Equal(t, "user", store.authReqs["flow-1"].BfMode)
		assert.Equal(t, "user-1", store.authReqs["flow-1"].BfSub)
	})

	t.Run("user mode without a session is rejected", func(t *testing.T) {
		store := newConsentStore()
		seedPendingFlow(store, "flow-1", time.Now().Add(time.Minute))
		h := newConsentHandler(store, &fakeResolver{userModeAvailable: true, userID: ""}, false)
		ctx := consentCtx("flow-1", `{"mode":"user"}`)
		h.flowSubmit(ctx)
		assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
	})
}

func TestConsentFlowSubmit_VKUserBinding(t *testing.T) {
	boundVK := &configtables.TableVirtualKey{ID: "vk-row-1", Value: "sk-bf-bound", IsActive: new(true)}

	t.Run("bound VK upgrades to user when logged-in user matches", func(t *testing.T) {
		store := newConsentStore()
		store.vksByValue[boundVK.Value] = boundVK
		seedPendingFlow(store, "flow-1", time.Now().Add(time.Minute))
		h := newConsentHandler(store, &fakeResolver{userModeAvailable: true, userID: "owner-1", vkBoundUserID: "owner-1"}, false)
		ctx := consentCtx("flow-1", `{"mode":"vk","value":"sk-bf-bound"}`)
		h.flowSubmit(ctx)
		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
		assert.Equal(t, "user", store.authReqs["flow-1"].BfMode)
		assert.Equal(t, "owner-1", store.authReqs["flow-1"].BfSub)
	})

	t.Run("bound VK rejected when logged-in user differs", func(t *testing.T) {
		store := newConsentStore()
		store.vksByValue[boundVK.Value] = boundVK
		seedPendingFlow(store, "flow-1", time.Now().Add(time.Minute))
		h := newConsentHandler(store, &fakeResolver{userModeAvailable: true, userID: "intruder", vkBoundUserID: "owner-1"}, false)
		ctx := consentCtx("flow-1", `{"mode":"vk","value":"sk-bf-bound"}`)
		h.flowSubmit(ctx)
		assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
	})

	t.Run("bound VK rejected when not signed in", func(t *testing.T) {
		store := newConsentStore()
		store.vksByValue[boundVK.Value] = boundVK
		seedPendingFlow(store, "flow-1", time.Now().Add(time.Minute))
		h := newConsentHandler(store, &fakeResolver{userModeAvailable: true, userID: "", vkBoundUserID: "owner-1"}, false)
		ctx := consentCtx("flow-1", `{"mode":"vk","value":"sk-bf-bound"}`)
		h.flowSubmit(ctx)
		assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
	})
}
