package handlers

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func newSessionsHandler(store *mockOAuth2Store) *OAuth2SessionsHandler {
	SetLogger(&mockLogger{})
	return NewOAuth2SessionsHandler(newTestOAuth2Config(store, configtables.MCPServerAuthModeBoth, false))
}

func TestListSessions(t *testing.T) {
	t.Run("returns the grant rows", func(t *testing.T) {
		store := &mockOAuth2Store{sessionRows: []configstore.OAuth2SessionRow{
			{ID: "s1", ClientID: "c1", BfMode: "vk", BfSub: "vk-1", BfSubDisplay: "Alpha VK"},
		}}
		h := newSessionsHandler(store)
		ctx := &fasthttp.RequestCtx{}
		h.listSessions(ctx)
		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())

		var resp struct {
			Sessions []configstore.OAuth2SessionRow `json:"sessions"`
		}
		require.NoError(t, json.Unmarshal(ctx.Response.Body(), &resp))
		require.Len(t, resp.Sessions, 1)
		assert.Equal(t, "Alpha VK", resp.Sessions[0].BfSubDisplay)
	})

	t.Run("store error surfaces 500", func(t *testing.T) {
		h := newSessionsHandler(&mockOAuth2Store{listErr: errors.New("boom")})
		ctx := &fasthttp.RequestCtx{}
		h.listSessions(ctx)
		assert.Equal(t, fasthttp.StatusInternalServerError, ctx.Response.StatusCode())
	})
}

func TestRevokeSession(t *testing.T) {
	newStore := func(row *configtables.TableOAuth2RefreshToken) *mockOAuth2Store {
		return &mockOAuth2Store{sessionByID: map[string]*configtables.TableOAuth2RefreshToken{row.ID: row}}
	}
	revokeCtx := func(id, callerUserID string) *fasthttp.RequestCtx {
		ctx := &fasthttp.RequestCtx{}
		ctx.SetUserValue("id", id)
		if callerUserID != "" {
			ctx.SetUserValue(schemas.BifrostContextKeyUserID, callerUserID)
		}
		return ctx
	}

	t.Run("vk-mode grant revokes without an identity gate", func(t *testing.T) {
		store := newStore(&configtables.TableOAuth2RefreshToken{ID: "s1", BfMode: "vk", BfSub: "vk-1"})
		h := newSessionsHandler(store)
		ctx := revokeCtx("s1", "")
		h.revokeSession(ctx)
		assert.Equal(t, fasthttp.StatusNoContent, ctx.Response.StatusCode())
		assert.Contains(t, store.revokedIDs, "s1")
	})

	t.Run("user-mode grant revokes when the caller matches bf_sub", func(t *testing.T) {
		store := newStore(&configtables.TableOAuth2RefreshToken{ID: "s1", BfMode: "user", BfSub: "user-1"})
		h := newSessionsHandler(store)
		ctx := revokeCtx("s1", "user-1")
		h.revokeSession(ctx)
		assert.Equal(t, fasthttp.StatusNoContent, ctx.Response.StatusCode())
	})

	t.Run("user-mode grant rejects a mismatched caller", func(t *testing.T) {
		store := newStore(&configtables.TableOAuth2RefreshToken{ID: "s1", BfMode: "user", BfSub: "user-1"})
		h := newSessionsHandler(store)
		ctx := revokeCtx("s1", "intruder")
		h.revokeSession(ctx)
		assert.Equal(t, fasthttp.StatusForbidden, ctx.Response.StatusCode())
		assert.Empty(t, store.revokedIDs)
	})

	t.Run("user-mode grant rejects an unauthenticated caller", func(t *testing.T) {
		store := newStore(&configtables.TableOAuth2RefreshToken{ID: "s1", BfMode: "user", BfSub: "user-1"})
		h := newSessionsHandler(store)
		ctx := revokeCtx("s1", "")
		h.revokeSession(ctx)
		assert.Equal(t, fasthttp.StatusForbidden, ctx.Response.StatusCode())
	})

	t.Run("missing grant returns 404", func(t *testing.T) {
		h := newSessionsHandler(&mockOAuth2Store{sessionByID: map[string]*configtables.TableOAuth2RefreshToken{}})
		ctx := revokeCtx("missing", "")
		h.revokeSession(ctx)
		assert.Equal(t, fasthttp.StatusNotFound, ctx.Response.StatusCode())
	})

	t.Run("empty id returns 400", func(t *testing.T) {
		h := newSessionsHandler(&mockOAuth2Store{})
		ctx := revokeCtx("", "")
		h.revokeSession(ctx)
		assert.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode())
	})
}
